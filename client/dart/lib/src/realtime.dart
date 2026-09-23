import 'dart:async';
import 'dart:convert';

import 'package:http/http.dart' as http;

/// Connection state of a [RealtimeUpdater]. Surfaced for status display
/// and tests; background loops NEVER throw — failures land here plus
/// [RealtimeUpdater.lastError].
enum RealtimeStatus {
  /// Never connected, or [RealtimeUpdater.disconnect] was called.
  disconnected,

  /// A stream GET is in flight (initial connect or reconnect backoff wait).
  connecting,

  /// Stream GET returned 200 and frames are being read.
  connected,

  /// Last stream attempt failed (non-200, 401 bad key, network down,
  /// malformed URL). The poll fallback keeps running; the stream loop
  /// keeps retrying with backoff until [RealtimeUpdater.disconnect].
  error,
}

/// Live-update driver over the adopted custom SSE stream (T2 decision),
/// with a poll fallback that guarantees eventual freshness.
///
/// Transport: `GET {baseUrl}/api/v1/env/{env}/stream` with the
/// `X-ConfigWire-Key` header. Frames are `event: config_update` with a
/// JSON `data: {"version":N,"etag":"...","env":"..."}` payload.
///
/// ## Staleness bound (the contract T17 relies on)
///
/// * Stream connected: push is best-effort ~instant (locally observed
///   within a 2s window on the spike path). NO sub-second guarantee is
///   claimed — delivery can lag or drop under load.
/// * Stream down/unavailable: freshness is bounded by [pollInterval]
///   (default 15 minutes). The poll timer runs ALWAYS, even while the
///   stream is healthy (belt + suspenders), so worst-case staleness is
///   `<= pollInterval + one fetch latency` in every state.
///
/// ## Robustness rules
///
/// * SSE parsing is chunk-split safe: bytes accumulate in a buffer and a
///   frame is parsed only at a blank-line boundary. `:` comment/
///   keepalive lines are ignored. Truncated frames (stream closed before
///   the blank line) are dropped, never throw.
/// * Malformed `data` (non-JSON, non-object) is ignored, never throws.
/// * Unexpected stream close triggers reconnect with backoff
///   1s, 2s, 4s, … capped at 30s. A non-200 status (e.g. 401 bad key)
///   surfaces via [status]/[lastError] WITHOUT throwing out of [connect].
/// * [onRefresh] (wired by `ConfigWire.connectRealtime` to a forced
///   `fetchAndActivate`) is invoked fire-and-forget per valid event.
///   Overlapping refreshes are last-wins (Dart is single-threaded; no
///   lock, no deadlock) — same discipline as `fetchAndActivate` itself.
/// * Nothing here ever throws out of a timer/stream callback: errors are
///   recorded on [lastError]/[status] only.
///
/// ## Timer lifecycle (RISK note)
///
/// [connect] starts one `Timer.periodic` + one stream loop.
/// [disconnect] cancels both and closes the owned stream client.
/// Forgetting [disconnect] leaks a periodic timer that keeps the
/// isolate alive — `ConfigWire.dispose()` calls it automatically, and
/// tests MUST dispose every client (a dangling timer shows up as a
/// suite that never exits).
class RealtimeUpdater {
  RealtimeUpdater({
    required this.baseUrl,
    required this.apiKey,
    required this.env,
    this.pollInterval = const Duration(minutes: 15),
    this.onRefresh,
    http.Client? streamClient,
  }) : _streamClient = streamClient ?? http.Client(),
       _ownsClient = streamClient == null;

  /// Base URL of the ConfigWire server (no trailing slash needed).
  final String baseUrl;

  /// SDK key, sent ONLY as the `X-ConfigWire-Key` header, never logged.
  final String apiKey;

  /// Environment slug appended to `/api/v1/env/{env}/stream`.
  final String env;

  /// Poll fallback period. Default 15 minutes (plan contract); tests
  /// pass millisecond-scale values. Runs ALWAYS, stream up or down.
  final Duration pollInterval;

  /// Called (fire-and-forget, errors swallowed) on every valid
  /// `config_update` event AND on every poll tick. `ConfigWire`
  /// wires this to `() => fetchAndActivate(force: true)` + conditional
  /// `onUpdate` emit. Return value is ignored here.
  final Future<void> Function()? onRefresh;

  final http.Client _streamClient;
  final bool _ownsClient;

  RealtimeStatus _status = RealtimeStatus.disconnected;
  String? _lastError;
  bool _running = false;
  int _backoffAttempt = 0;
  Timer? _pollTimer;
  StreamSubscription<String>? _streamSub;
  bool _disposed = false;

  final StreamController<Map<String, Object?>> _notificationController =
      StreamController<Map<String, Object?>>.broadcast();

  /// Current connection state. Never throws.
  RealtimeStatus get status => _status;

  /// Human-readable reason for the last stream failure (null when the
  /// stream is healthy or was never attempted). Never contains the key.
  String? get lastError => _lastError;

  /// True while a 200 stream is open and frames are being read.
  bool get isConnected => _status == RealtimeStatus.connected;

  /// Decoded `{version, etag}` maps, one per valid `config_update`
  /// event. Broadcast: late listeners miss earlier events.
  Stream<Map<String, Object?>> get notifications =>
      _notificationController.stream;

  /// Starts the poll timer + the stream reconnect loop. Returns
  /// immediately; NEVER throws (even with a malformed baseUrl or a
  /// dead port — those surface via [status]/[lastError]).
  /// Idempotent: a second call disconnects the previous run first.
  void connect() {
    if (_disposed) return;
    disconnectSync();
    _running = true;
    _status = RealtimeStatus.connecting;
    _pollTimer = Timer.periodic(pollInterval, (_) => _pollTick());
    // Fire-and-forget by design: the loop records errors on status.
    unawaited(_runLoop());
  }

  /// Stops the poll timer + the stream loop and closes the owned HTTP
  /// client. Safe to call when already disconnected. Never throws.
  Future<void> disconnect() async {
    disconnectSync();
    // Let any in-flight stream read observe _running == false.
    await Future<void>.delayed(Duration.zero);
  }

  void disconnectSync() {
    _running = false;
    _pollTimer?.cancel();
    _pollTimer = null;
    try {
      _streamSub?.cancel();
    } catch (_) {
      // Cancel on a dead subscription must not throw.
    }
    _streamSub = null;
    if (_status != RealtimeStatus.disconnected) {
      _status = RealtimeStatus.disconnected;
    }
  }

  /// [disconnect] + closes the [notifications] controller.
  Future<void> dispose() async {
    _disposed = true;
    disconnectSync();
    await Future<void>.delayed(Duration.zero);
    if (_ownsClient) {
      try {
        _streamClient.close();
      } catch (_) {
        // Close must not throw.
      }
    }
    try {
      await _notificationController.close();
    } catch (_) {
      // Double-dispose must not throw.
    }
  }

  void _pollTick() {
    if (!_running || _disposed) return;
    final refresh = onRefresh;
    if (refresh == null) return;
    // Fire-and-forget: a throwing/slow refresh must never kill the timer.
    unawaited(_guard(refresh));
  }

  Future<void> _guard(Future<void> Function() fn) async {
    try {
      await fn();
    } catch (_) {
      // Swallowed: background updaters report via status/log only.
    }
  }

  Future<void> _runLoop() async {
    while (_running && !_disposed) {
      try {
        _status = RealtimeStatus.connecting;
        final uri = Uri.parse(
          '${baseUrl.replaceAll(RegExp(r'/+$'), '')}/api/v1/env/$env/stream',
        );
        final req = http.Request('GET', uri)
          ..headers['X-ConfigWire-Key'] = apiKey
          ..headers['Accept'] = 'text/event-stream';
        final resp = await _streamClient.send(req).timeout(
          const Duration(seconds: 10),
        );
        if (!_running || _disposed) {
          try {
            await resp.stream.drain();
          } catch (_) {
            // Best effort drain; ignore.
          }
          return;
        }
        if (resp.statusCode != 200) {
          // e.g. 401 bad key: surface, do NOT throw, back off + retry.
          _lastError = 'stream HTTP ${resp.statusCode}';
          _status = RealtimeStatus.error;
          try {
            await resp.stream.drain();
          } catch (_) {
            // Best effort drain; ignore.
          }
          await _backoffWait();
          continue;
        }
        _status = RealtimeStatus.connected;
        _backoffAttempt = 0;
        await _readStream(resp);
        // Stream closed without disconnect(): unexpected close.
        if (!_running || _disposed) return;
        _lastError = 'stream closed by server';
        _status = RealtimeStatus.error;
        await _backoffWait();
      } catch (e) {
        if (!_running || _disposed) return;
        // Network down, DNS, timeout, malformed baseUrl: surface only.
        _lastError = '$e'.replaceAll(apiKey, '<redacted>');
        _status = RealtimeStatus.error;
        await _backoffWait();
      }
    }
  }

  Future<void> _backoffWait() async {
    // 1s, 2s, 4s, 8s, 16s, then capped at 30s.
    var seconds = 1 << _backoffAttempt;
    if (seconds > 30) seconds = 30;
    if (_backoffAttempt < 10) _backoffAttempt++;
    final deadline = DateTime.now().add(Duration(seconds: seconds));
    while (_running && !_disposed && DateTime.now().isBefore(deadline)) {
      final remaining = deadline.difference(DateTime.now());
      final step = remaining > const Duration(milliseconds: 100)
          ? const Duration(milliseconds: 100)
          : remaining;
      await Future<void>.delayed(step);
    }
  }

  final StringBuffer _buf = StringBuffer();

  Future<void> _readStream(http.StreamedResponse resp) {
    final done = Completer<void>();
    late final StreamSubscription<String> sub;
    sub = resp.stream.transform(utf8.decoder).listen(
      (chunk) {
        if (!_running || _disposed) return;
        try {
          _ingestChunk(chunk);
        } catch (_) {
          // A poison chunk must never kill the stream read.
        }
      },
      onError: (_) {
        if (!done.isCompleted) done.complete();
      },
      onDone: () {
        if (!done.isCompleted) done.complete();
      },
      cancelOnError: false,
    );
    _streamSub = sub;
    return done.future.whenComplete(() {
      if (identical(_streamSub, sub)) _streamSub = null;
      try {
        sub.cancel();
      } catch (_) {
        // Ignore cancel errors.
      }
    });
  }

  void _ingestChunk(String chunk) {
    _buf.write(chunk);
    var s = _buf.toString();
    final sep = RegExp(r'\r?\n\r?\n');
    var m = sep.firstMatch(s);
    while (m != null) {
      final frame = s.substring(0, m.start);
      s = s.substring(m.end);
      _handleFrame(frame);
      m = sep.firstMatch(s);
    }
    _buf.clear();
    _buf.write(s);
  }

  void _handleFrame(String frame) {
    var event = 'message';
    final dataLines = <String>[];
    for (final rawLine in frame.split(RegExp(r'\r?\n'))) {
      final line = rawLine;
      if (line.isEmpty) continue;
      // ':' comments/keepalives are ignored (never throw).
      if (line.startsWith(':')) continue;
      final idx = line.indexOf(':');
      if (idx < 0) continue; // garbage line: ignore.
      final field = line.substring(0, idx).trim();
      var value = line.substring(idx + 1);
      if (value.startsWith(' ')) value = value.substring(1);
      if (field == 'event') {
        event = value.trim();
      } else if (field == 'data') {
        dataLines.add(value);
      }
      // id:/retry: intentionally unsupported (fixed backoff); ignored.
    }
    if (event != 'config_update') return;
    if (dataLines.isEmpty) return;
    Map<String, Object?> decoded;
    try {
      final parsed = jsonDecode(dataLines.join('\n'));
      if (parsed is! Map) return;
      decoded = Map<String, Object?>.from(parsed);
    } catch (_) {
      return; // malformed data: ignore, no throw.
    }
    final version = decoded['version'];
    final etag = decoded['etag'];
    final notification = <String, Object?>{
      'version': version is int
          ? version
          : (version is num ? version.toInt() : 0),
      'etag': etag is String ? etag : '',
    };
    if (!_notificationController.isClosed) {
      try {
        _notificationController.add(notification);
      } catch (_) {
        // A closed/controller error must not kill the stream read.
      }
    }
    final refresh = onRefresh;
    if (refresh != null) {
      // Fire-and-forget: concurrent event fetches are last-wins
      // (single-threaded, no lock — same discipline as fetchAndActivate).
      unawaited(_guard(refresh));
    }
  }
}

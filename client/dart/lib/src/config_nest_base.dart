import 'dart:async';
import 'dart:convert';

import 'package:http/http.dart' as http;

import 'cache.dart';
import 'events.dart';
import 'realtime.dart';

export 'cache.dart';
export 'events.dart';
export 'realtime.dart';

/// Result of the last fetch attempt.
enum FetchStatus {
  /// No fetch has completed yet (fresh client, cache load only).
  none,

  /// 200 with new values: values/etag/version/fetchAt replaced, persisted.
  success,

  /// 304: server state unchanged, cached values kept.
  cached,

  /// Skipped by [ConfigNest.minimumFetchInterval] throttling.
  throttled,

  /// Network failure, malformed 200, or non-200/304 status.
  /// Cached values (or in-app defaults when cold) are kept; never throws.
  error,
}

/// Pure-Dart ConfigNest client: offline-first fetch + file cache (todo 12).
///
/// Lifecycle: [ensureInitialized] loads the cache file into memory, then
/// [fetchAndActivate] refreshes from the server. All reads are synchronous
/// typed getters over the in-memory view.
///
/// Value layering: the in-memory view is `{...defaults, ...serverValues}`
/// — in-app [defaults] are the underlay so a fresh env (server 200 with
/// `values:{}` per the empty-release policy) still serves defaults.
/// A 200 replaces the server layer wholesale.
///
/// Variants: the server's `variants` map only carries flags targeted by an
/// experiment, and an anonymous/default-variant arm is served as `""`.
/// An empty-string variant is NOT a real arm: such keys are OMITTED from
/// every variant view ([getVariant] returns null, [getVariants] skips them).
///
/// Concurrency: [fetchAndActivate] has no lock (single-threaded Dart).
/// Two overlapping calls both run; last-completes-wins on values/etag.
/// Callers that need serialization should `await` each call.
///
/// Privacy: the apiKey is sent only as the `X-ConfigNest-Key` header and
/// is never printed or logged by this client.
class ConfigNest {
  ConfigNest({
    required this.apiKey,
    required this.env,
    required this.baseUrl,
    Map<String, Object?> defaults = const {},
    http.Client? client,
    String? cacheFile,
    this.minimumFetchInterval = const Duration(hours: 12),
    this.fetchTimeout = const Duration(seconds: 60),
  })  : _defaults = Map<String, Object?>.from(defaults),
        _values = Map<String, Object?>.from(defaults),
        _client = client,
        cacheFile = cacheFile ?? '.config_nest_$env-cache.json';

  final String apiKey;
  final String env;
  final String baseUrl;

  /// In-app defaults (underlay; never mutated after construction).
  final Map<String, Object?> _defaults;

  /// Live view: {...defaults, ...serverValues}.
  final Map<String, Object?> _values;

  /// Variant arms, with `""` (anonymous) entries omitted — never surfaced.
  final Map<String, String> _variants = {};

  final http.Client? _client;
  http.Client? _owned;

  /// Cache file path. Default: `.config_nest_<env>-cache.json` in the
  /// current working directory (RISK: cwd-dependent; pass an explicit
  /// app-documents path in production — see notepad T12 entry).
  final String cacheFile;

  /// Minimum time between server fetches; `Duration.zero` disables
  /// throttling (dev). Default 12h. Only successful fetches (200/304)
  /// arm the throttle — errors never block retries (offline devices
  /// must keep retrying).
  final Duration minimumFetchInterval;

  /// Per-request timeout for the config GET. Errors (incl. timeouts)
  /// fall back to stale cache/defaults and never throw.
  final Duration fetchTimeout;

  final StreamController<Map<String, Object?>> _updateController =
      StreamController<Map<String, Object?>>.broadcast();

  /// Broadcast stream of value snapshots. Emits ONLY when a realtime
  /// event or poll tick produced a 200 that replaced values (see
  /// [connectRealtime]); plain [fetchAndActivate] calls never emit.
  /// Broadcast semantics: late listeners miss earlier snapshots.
  Stream<Map<String, Object?>> get onUpdate => _updateController.stream;

  RealtimeUpdater? _realtime;

  /// Active realtime updater, or null when [connectRealtime] was never
  /// called (or after [disconnectRealtime]). Exposed for status checks
  /// (`realtime?.status`, `realtime?.lastError`); tests use it to
  /// assert the 401-bad-key path surfaces without throwing.
  RealtimeUpdater? get realtime => _realtime;

  String? _etag;
  int _version = 0;
  DateTime? _lastFetchAt;
  FetchStatus _lastFetchStatus = FetchStatus.none;

  /// Status of the last fetch attempt (`none` before the first attempt).
  FetchStatus get lastFetchStatus => _lastFetchStatus;

  /// Time of the last successful fetch (200/304), or null if none yet.
  DateTime? get fetchTime => _lastFetchAt;

  /// Stored etag served verbatim back via `If-None-Match` (exact match).
  String? get etag => _etag;

  /// Latest activated release version (0 when nothing activated yet).
  int get version => _version;

  bool getBool(String key, {bool fallback = false}) {
    final v = _values[key] ?? _defaults[key];
    return v is bool ? v : fallback;
  }

  String getString(String key, {String fallback = ''}) {
    final v = _values[key] ?? _defaults[key];
    return v is String ? v : fallback;
  }

  int getInt(String key, {int fallback = 0}) {
    final v = _values[key] ?? _defaults[key];
    return v is int ? v : fallback;
  }

  double getDouble(String key, {double fallback = 0.0}) {
    final v = _values[key] ?? _defaults[key];
    if (v is double) return v;
    if (v is int) return v.toDouble();
    return fallback;
  }

  Map<String, Object?> getJSON(String key) {
    final v = _values[key] ?? _defaults[key];
    if (v is Map) return Map<String, Object?>.from(v);
    return const {};
  }

  Map<String, Object?> getAll() => Map<String, Object?>.from(_values);

  /// Assigned experiment arm for [key], or null when the flag has no
  /// targeting experiment or the arm is anonymous (`""` is omitted,
  /// never surfaced as a real arm).
  String? getVariant(String key) => _variants[key];

  /// All non-anonymous variant arms (empty-string values omitted).
  Map<String, String> getVariants() => Map<String, String>.from(_variants);

  http.Client get _http => _client ?? (_owned ??= http.Client());

  /// Loads the cache file into memory, then runs [fetchAndActivate].
  /// Never throws: a missing/corrupt cache means defaults until the
  /// fetch resolves (which itself never throws).
  Future<void> ensureInitialized({String? userId}) async {
    final cached = await loadCacheFile(cacheFile);
    if (cached != null) {
      _applyServerValues(
        values: cached.values,
        etag: cached.etag,
        version: cached.version,
        fetchedAt: cached.fetchedAt,
        variants: const {},
      );
    }
    await fetchAndActivate(userId: userId);
  }

  /// Fetches the latest release and activates it.
  ///
  /// Returns true only on a 200 that replaced values. 304 (unchanged),
  /// throttled skips, and every failure mode return false — network or
  /// parse failure keeps the stale cache, a cold cache keeps in-app
  /// defaults, and nothing here ever throws.
  ///
  /// [force] skips the [minimumFetchInterval] throttle check. The
  /// realtime path ([connectRealtime]) always passes `force: true` so
  /// push events and poll ticks stay fresh regardless of the throttle;
  /// direct callers keep the default `false`.
  Future<bool> fetchAndActivate({String? userId, bool force = false}) async {
    final now = DateTime.now().toUtc();
    if (!force &&
        minimumFetchInterval > Duration.zero &&
        _lastFetchAt != null &&
        now.difference(_lastFetchAt!) < minimumFetchInterval) {
      _lastFetchStatus = FetchStatus.throttled;
      return false;
    }

    final uid = (userId ?? '').trim();
    final client = _http;
    try {
      final uri = Uri.parse(
        '${baseUrl.replaceAll(RegExp(r'/+$'), '')}/api/v1/env/$env/config${uid.isEmpty ? '' : '?uid=${Uri.encodeComponent(uid)}'}',
      );
      final headers = <String, String>{'X-ConfigNest-Key': apiKey};
      if (_etag != null && _etag!.isNotEmpty) {
        // Stored verbatim; the server 304s on EXACT match only.
        headers['If-None-Match'] = _etag!;
      }
      final resp =
          await client.get(uri, headers: headers).timeout(fetchTimeout);

      if (resp.statusCode == 304) {
        _lastFetchAt = now;
        _lastFetchStatus = FetchStatus.cached;
        return false;
      }
      if (resp.statusCode != 200) {
        _lastFetchStatus = FetchStatus.error;
        return false;
      }

      final parsed = _parseFetchBody(resp.body);
      if (parsed == null) {
        // Malformed 200: keep stale cache, no throw.
        _lastFetchStatus = FetchStatus.error;
        return false;
      }

      final at = DateTime.now().toUtc();
      _applyServerValues(
        values: parsed.values,
        etag: parsed.etag,
        version: parsed.version,
        fetchedAt: at,
        variants: parsed.variants,
      );
      _lastFetchAt = at;
      _lastFetchStatus = FetchStatus.success;

      // Best-effort persistence + analytics: neither may fail the fetch.
      try {
        await saveCacheFile(
          cacheFile,
          CacheData(
            etag: _etag ?? parsed.etag,
            version: _version,
            fetchedAt: at,
            values: parsed.values,
          ),
        );
      } catch (_) {
        // Swallowed: fetch already succeeded.
      }
      await postFetchEvent(
        client: client,
        baseUrl: baseUrl,
        env: env,
        apiKey: apiKey,
        userId: uid,
        variants: Map<String, String>.from(_variants),
      );
      return true;
    } catch (_) {
      // Network failure, timeout, malformed URL, sync throw from the
      // transport: stale cache (or cold defaults) + error status.
      _lastFetchStatus = FetchStatus.error;
      return false;
    }
  }

  /// Applies one server layer: values merged over defaults, anonymous
  /// (`""`) variants omitted, etag/version/fetchAt replaced verbatim.
  void _applyServerValues({
    required Map<String, Object?> values,
    required String etag,
    required int version,
    required DateTime fetchedAt,
    required Map<String, String> variants,
  }) {
    _values
      ..clear()
      ..addAll(_defaults)
      ..addAll(values);
    _variants
      ..clear()
      ..addEntries(
        variants.entries.where((e) => e.value.isNotEmpty),
      );
    _etag = etag;
    _version = version;
    // Cache-loaded rows arm the throttle (offline-first: a fresh cache
    // means "just fetched"); live 200/304 paths set _lastFetchAt
    // explicitly after this call.
    _lastFetchAt = fetchedAt;
  }

  /// Starts live updates: SSE stream + poll fallback (todo 14).
  ///
  /// Every valid `config_update` event AND every [pollInterval] tick
  /// runs `fetchAndActivate(force: true)`; a values snapshot is added
  /// to [onUpdate] ONLY when the fetch returned true (200 with new
  /// values). 304/throttled/error refreshes emit nothing.
  ///
  /// Staleness bound: push is best-effort ~instant while connected,
  /// `<= pollInterval + one fetch` otherwise (poll runs always, even
  /// while the stream is healthy). Default 15 minutes per plan;
  /// pass a short interval in tests/dev.
  ///
  /// Never throws (bad key/dead port surface on `realtime.status` /
  /// `realtime.lastError`). Idempotent: reconnects cleanly when
  /// called twice. Cancel with [disconnectRealtime] (also called by
  /// [dispose]).
  Future<void> connectRealtime({
    Duration pollInterval = const Duration(minutes: 15),
  }) async {
    await disconnectRealtime();
    final updater = RealtimeUpdater(
      baseUrl: baseUrl,
      apiKey: apiKey,
      env: env,
      pollInterval: pollInterval,
      onRefresh: () async {
        // Forced: realtime freshness must not be throttled by
        // minimumFetchInterval (the staleness bound assumes it).
        final changed = await fetchAndActivate(force: true);
        if (changed && !_updateController.isClosed) {
          try {
            _updateController.add(getAll());
          } catch (_) {
            // A closed-listener race must not break the refresh loop.
          }
        }
      },
    );
    _realtime = updater;
    updater.connect();
  }

  /// Stops live updates started by [connectRealtime]. Safe when never
  /// connected. Never throws.
  Future<void> disconnectRealtime() async {
    final updater = _realtime;
    _realtime = null;
    if (updater != null) {
      try {
        await updater.dispose();
      } catch (_) {
        // Dispose must not throw out of disconnect.
      }
    }
  }

  /// Closes the realtime updater (if any), then the [onUpdate] stream
  /// and the owned HTTP client. Test/smoke only.
  Future<void> dispose() async {
    await disconnectRealtime();
    try {
      await _updateController.close();
    } catch (_) {
      // Double-dispose must not throw.
    }
    _owned?.close();
  }
}

/// Parsed 200 body; null when the body is not a well-formed fetch document.
class _FetchDoc {
  _FetchDoc({required this.values, required this.variants, required this.etag, required this.version});

  final Map<String, Object?> values;
  final Map<String, String> variants;
  final String etag;
  final int version;
}

_FetchDoc? _parseFetchBody(String body) {
  try {
    final decoded = jsonDecode(body);
    if (decoded is! Map) return null;
    final m = Map<String, Object?>.from(decoded);
    final values = m['values'];
    final etag = m['etag'];
    if (values is! Map || etag is! String) return null;
    final variants = <String, String>{};
    final rawVariants = m['variants'];
    if (rawVariants is Map) {
      for (final e in rawVariants.entries) {
        if (e.key is String && e.value is String) {
          variants[e.key as String] = e.value as String;
        }
      }
    }
    final version = m['version'];
    final v = version is int
        ? version
        : (version is num ? version.toInt() : 0);
    return _FetchDoc(
      values: Map<String, Object?>.from(values),
      variants: variants,
      etag: etag,
      version: v,
    );
  } catch (_) {
    return null;
  }
}

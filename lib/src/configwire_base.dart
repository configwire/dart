import 'dart:async';
import 'dart:convert';

import 'package:http/http.dart' as http;

import 'cache.dart';
import 'cw_logger.dart';
import 'events.dart';
import 'realtime.dart';
import 'targeting.dart';

export 'cache.dart';
export 'events.dart';
export 'realtime.dart';
export 'targeting.dart';

/// Result of the last fetch attempt.
enum FetchStatus {
  /// No fetch has completed yet (fresh client, cache load only).
  none,

  /// 200 with new values: values/etag/version/fetchAt replaced, persisted.
  success,

  /// 304: server state unchanged, cached values kept.
  cached,

  /// Skipped by [ConfigWire.minimumFetchInterval] throttling.
  throttled,

  /// Network failure, malformed 200, or non-200/304 status.
  /// Cached values (or in-app defaults when cold) are kept; never throws.
  error,
}

/// Pure-Dart ConfigWire client: offline-first fetch + pluggable cache.
///
/// Lifecycle: [ensureInitialized] loads the cache into memory, then
/// [fetchAndActivate] refreshes from the server. All reads are synchronous
/// typed getters over the in-memory view.
///
/// Persistence is bring-your-own: pass a [CacheStore] implementation
/// to persist across restarts. The default is [MemoryCacheStore]
/// (session only, no disk).
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
/// Privacy: the apiKey is sent only as the `X-ConfigWire-Key` header and
/// is never printed or logged by this client.
class ConfigWire {
  ConfigWire({
    required this.apiKey,
    required this.env,
    required this.baseUrl,
    Map<String, Object?> defaults = const {},
    this._client,
    this.minimumFetchInterval = const Duration(hours: 12),
    this.fetchTimeout = const Duration(seconds: 60),
    this.verbose = false,
    CacheStore? store,
  }) : _defaults = Map<String, Object?>.from(defaults),
       _values = Map<String, Object?>.from(defaults),
       _store = store ?? MemoryCacheStore();

  /// SDK key, sent only as the `X-ConfigWire-Key` header and never logged.
  final String apiKey;

  /// Environment slug appended to `/api/v1/env/{env}/config`.
  final String env;

  /// Base URL of the ConfigWire server (trailing slashes are stripped).
  final String baseUrl;

  /// In-app defaults (underlay; never mutated after construction).
  final Map<String, Object?> _defaults;

  /// Live view: {...defaults, ...serverValues}.
  final Map<String, Object?> _values;

  /// Variant arms, with `""` (anonymous) entries omitted — never surfaced.
  final Map<String, String> _variants = {};

  final http.Client? _client;
  http.Client? _owned;

  /// Last targeting context used for fetch (sticky across calls).
  /// Updated on every [fetchAndActivate]/[ensureInitialized] call that
  /// passes targeting params; realtime refreshes reuse it verbatim so
  /// push/poll ticks stay evaluated for the same user/device.
  TargetingContext _targeting = TargetingContext.empty;

  /// Current targeting context (sticky; see [_targeting]).
  TargetingContext get targeting => _targeting;

  /// Replaces the sticky targeting context without fetching.
  /// The next [fetchAndActivate] (incl. realtime ticks) uses it unless
  /// that call passes explicit targeting params.
  void setTargeting(TargetingContext context) {
    _targeting = context;
  }

  /// Cache persistence. When null at construction, [MemoryCacheStore]
  /// is used (session only, no disk); pass a [CacheStore]
  /// implementation for disk persistence.
  final CacheStore _store;

  /// Minimum time between server fetches; `Duration.zero` disables
  /// throttling (dev). Default 12h. Only successful fetches (200/304)
  /// arm the throttle — errors never block retries (offline devices
  /// must keep retrying).
  final Duration minimumFetchInterval;

  /// Per-request timeout for the config GET. Errors (incl. timeouts)
  /// fall back to stale cache/defaults and never throw.
  final Duration fetchTimeout;

  /// Opt-in verbose debug logging. When true, lifecycle/fetch/cache/
  /// realtime events are emitted via `cwDebug`; when false (default)
  /// the client stays silent. Logging only: never affects behavior,
  /// return values, or defaults. The apiKey is never logged.
  final bool verbose;

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

  /// In-flight initialization shared by concurrent [ensureInitialized]
  /// callers; null when no init is running.
  Future<void>? _initFuture;

  /// True once the first [ensureInitialized] attempt has finished
  /// (success or soft-failure). Later calls are no-ops unless they pass
  /// an explicit [TargetingContext], which triggers a forced refresh.
  bool _initialized = false;

  /// Status of the last fetch attempt (`none` before the first attempt).
  FetchStatus get lastFetchStatus => _lastFetchStatus;

  /// Time of the last successful fetch (200/304), or null if none yet.
  DateTime? get fetchTime => _lastFetchAt;

  /// Stored etag served verbatim back via `If-None-Match` (exact match).
  String? get etag => _etag;

  /// Latest activated release version (0 when nothing activated yet).
  int get version => _version;

  /// Returns the bool value for [key], or [fallback] when missing or mistyped.
  bool getBool(String key, {bool fallback = false}) {
    final v = _values[key] ?? _defaults[key];
    return v is bool ? v : fallback;
  }

  /// Returns the String value for [key], or [fallback] when missing or mistyped.
  String getString(String key, {String fallback = ''}) {
    final v = _values[key] ?? _defaults[key];
    return v is String ? v : fallback;
  }

  /// Returns the int value for [key], or [fallback] when missing or mistyped.
  int getInt(String key, {int fallback = 0}) {
    final v = _values[key] ?? _defaults[key];
    return v is int ? v : fallback;
  }

  /// Returns the double value for [key], coercing ints via `toDouble()`,
  /// or [fallback] when missing or mistyped.
  double getDouble(String key, {double fallback = 0.0}) {
    final v = _values[key] ?? _defaults[key];
    if (v is double) return v;
    if (v is int) return v.toDouble();
    return fallback;
  }

  /// Returns the value for [key] as [T], or [fallback] when missing or mistyped.
  ///
  /// Coerces ints via `toDouble()` when `T` is `double` (parity with
  /// [getDouble]). When [fallback] is omitted and the key is missing or
  /// mistyped, returns `null` for nullable [T] and throws a cast error
  /// for non-nullable [T]. Collections from the server decode as
  /// `List<dynamic>`/`Map<String, dynamic>`, so prefer [getList]/[getMap]
  /// for those (a direct `get<List<String>>` over server values misses).
  T get<T>(String key, {T? fallback}) {
    final v = _values[key] ?? _defaults[key];
    if (v is T) {
      // Defensive copies so callers can't mutate the live view.
      if (v is Map) return Map.from(v) as T;
      if (v is List) return List.from(v) as T;
      return v;
    }
    if (v is int && T == double) return v.toDouble() as T;
    return fallback as T;
  }

  /// Returns a copy of the list at [key] with every element of type [T].
  ///
  /// On any error (missing key, mistyped value, or a mistyped element)
  /// [fallback] is returned; when [fallback] is null the error is
  /// rethrown — mirroring the `onError` contract in the Firebase
  /// reference (`asList`/`getList`).
  ///
  /// Unlike the reference (which `jsonDecode`s a string then `cast<T>()`),
  /// values here are already decoded, so each element is checked eagerly
  /// instead of returning a lazy cast view. Int elements are coerced via
  /// `toDouble()` when `T` is `double` (parity with [getDouble]);
  /// the reference is strict and would fall back there.
  List<T> getList<T>(String key, {List<T>? fallback = const []}) {
    try {
      final v = _values[key] ?? _defaults[key];
      if (v is List) {
        if (v.isEmpty) return <T>[];
        final out = <T>[];
        for (final e in v) {
          if (e is T) {
            out.add(e);
          } else if (e is int && T == double) {
            out.add(e.toDouble() as T);
          } else {
            throw StateError('getList($key): element ${e.runtimeType} is not $T');
          }
        }
        return out;
      }
      throw StateError('getList($key): value is ${v.runtimeType}, not List');
    } catch (_) {
      if (fallback == null) rethrow;
      return fallback;
    }
  }

  /// Returns a copy of the JSON object at [key] with every value of type
  /// [T].
  ///
  /// On any error (missing key, mistyped value, non-String key, or a
  /// mistyped entry) [fallback] is returned; when [fallback] is null the
  /// error is rethrown — mirroring the `onError` contract in the Firebase
  /// reference (`asMap`/`getMap`).
  ///
  /// Unlike the reference (which `jsonDecode`s a string then
  /// `cast<String, T>()`), values here are already decoded, so each entry
  /// is checked eagerly instead of returning a lazy cast view. Int values
  /// are coerced via `toDouble()` when `T` is `double` (parity with
  /// [getDouble]); the reference is strict and would fall back there.
  Map<String, T> getMap<T>(String key, {Map<String, T>? fallback = const {}}) {
    try {
      final v = _values[key] ?? _defaults[key];
      if (v is Map) {
        if (v.isEmpty) return <String, T>{};
        final out = <String, T>{};
        for (final e in v.entries) {
          if (e.key is! String) {
            throw StateError('getMap($key): key ${e.key.runtimeType} is not String');
          }
          final k = e.key as String;
          final val = e.value;
          if (val is T) {
            out[k] = val;
          } else if (val is int && T == double) {
            out[k] = val.toDouble() as T;
          } else {
            throw StateError('getMap($key): value of "$k" is ${val.runtimeType}, not $T');
          }
        }
        return out;
      }
      throw StateError('getMap($key): value is ${v.runtimeType}, not Map');
    } catch (_) {
      if (fallback == null) rethrow;
      return fallback;
    }
  }

  /// Returns a copy of the live view (`{...defaults, ...serverValues}`).
  Map<String, Object?> getAll() => Map<String, Object?>.from(_values);

  /// Assigned experiment arm for [key], or null when the flag has no
  /// targeting experiment or the arm is anonymous (`""` is omitted,
  /// never surfaced as a real arm).
  String? getVariant(String key) => _variants[key];

  /// All non-anonymous variant arms (empty-string values omitted).
  Map<String, String> getVariants() => Map<String, String>.from(_variants);

  http.Client get _http => _client ?? (_owned ??= http.Client());

  /// Loads the cache into memory, then runs [fetchAndActivate]
  /// with `force: true` so the server refresh is never throttled by
  /// [minimumFetchInterval] (a fresh cache arms the throttle via
  /// [_applyServerValues]).
  /// Never throws: a missing/corrupt cache means defaults until the
  /// fetch resolves (which itself never throws); a failed fetch keeps
  /// the cached values (or defaults on a cold cache).
  ///
  /// Safe to call multiple times: concurrent calls share the same
  /// in-flight initialization, and calls after the first successful
  /// (or soft-failed) attempt return immediately without re-loading
  /// the cache or re-fetching — unless a non-null [context] is passed,
  /// which triggers a forced `fetchAndActivate` refresh with the new
  /// targeting.
  ///
  /// Targeting ([context]) is sticky: a non-null [context] replaces the
  /// stored [targeting] for this and all future fetches (incl. realtime
  /// ticks); null reuses the stored value. To change one field:
  /// `fetchAndActivate(context: cw.targeting.copyWith(platform: 'ios'))`.
  /// To clear: pass a context with `''` (or `{}` for `customAttrs`).
  Future<void> ensureInitialized({TargetingContext? context}) async {
    cwDebug(
      verbose,
      () => 'ensureInitialized start env=$env hasContext=${context != null} '
          'initialized=$_initialized',
    );
    if (_initialized) {
      if (context != null) {
        await fetchAndActivate(context: context, force: true);
      }
      cwDebug(
        verbose,
        () => 'ensureInitialized complete (already initialized) env=$env '
            'status=$_lastFetchStatus',
      );
      return;
    }
    final inFlight = _initFuture;
    if (inFlight != null) {
      await inFlight;
      if (context != null && _initialized) {
        await fetchAndActivate(context: context, force: true);
      }
      cwDebug(
        verbose,
        () => 'ensureInitialized complete (joined in-flight) env=$env '
            'status=$_lastFetchStatus',
      );
      return;
    }
    final future = _doEnsureInitialized(context);
    _initFuture = future;
    try {
      await future;
      _initialized = true;
      cwDebug(
        verbose,
        () => 'ensureInitialized complete env=$env status=$_lastFetchStatus',
      );
    } finally {
      _initFuture = null;
    }
  }

  /// Single initialization attempt: cache load + forced fetch.
  /// Never throws (see [ensureInitialized]).
  Future<void> _doEnsureInitialized(TargetingContext? context) async {
    cwDebug(verbose, () => 'init start env=$env baseUrl=$baseUrl');
    CacheData? cached;
    var loadFailed = false;
    try {
      cached = await _store.load();
    } catch (_) {
      // A throwing store behaves as a cold cache: defaults until the
      // fetch resolves (which itself never throws).
      loadFailed = true;
      cached = null;
    }
    if (cached != null) {
      final hit = cached;
      cwDebug(
        verbose,
        () => 'cache hit values=${hit.values.length} keys '
            'version=${hit.version} etag=${hit.etag}',
      );
      _applyServerValues(
        values: hit.values,
        etag: hit.etag,
        version: hit.version,
        fetchedAt: hit.fetchedAt,
        variants: hit.variants,
      );
    } else if (loadFailed) {
      cwDebug(verbose, () => 'cache corrupt (load threw) env=$env');
    } else {
      cwDebug(verbose, () => 'cache miss env=$env');
    }
    await fetchAndActivate(context: context, force: true);
  }

  /// Fetches the latest release and activates it.
  ///
  /// Returns true only on a 200 that replaced values. 304 (unchanged),
  /// throttled skips, and every failure mode return false — network or
  /// parse failure keeps the stale cache, a cold cache keeps in-app
  /// defaults, and nothing here ever throws.
  ///
  /// Latency note: on a successful fetch this awaits one best-effort
  /// analytics POST (`postFetchEvent`, up to 5s timeout) before
  /// returning. The POST is intentionally awaited (not fire-and-forget)
  /// so callers/tests observe the event body; failures are swallowed
  /// and never fail the fetch.
  ///
  /// [force] skips the [minimumFetchInterval] throttle check. The
  /// realtime path ([connectRealtime]) always passes `force: true` so
  /// push events and poll ticks stay fresh regardless of the throttle;
  /// direct callers keep the default `false`.
  ///
  /// Targeting ([context]) is sticky: a non-null [context] replaces the
  /// stored [targeting] wholesale; null reuses it. Single-field update:
  /// `fetchAndActivate(context: cw.targeting.copyWith(platform: 'ios'))`.
  Future<bool> fetchAndActivate({
    TargetingContext? context,
    bool force = false,
  }) async {
    final now = DateTime.now().toUtc();
    if (!force &&
        minimumFetchInterval > Duration.zero &&
        _lastFetchAt != null &&
        now.difference(_lastFetchAt!) < minimumFetchInterval) {
      cwDebug(verbose, () => 'fetch throttled skip env=$env');
      _lastFetchStatus = FetchStatus.throttled;
      return false;
    }

    if (context != null) _targeting = context;
    final uid = _targeting.userId.trim();
    final client = _http;
    try {
      final uri = _buildConfigUri(_targeting);
      final headers = <String, String>{'X-ConfigWire-Key': apiKey};
      if (_etag != null && _etag!.isNotEmpty) {
        // Stored verbatim; the server 304s on EXACT match only.
        headers['If-None-Match'] = _etag!;
      }
      cwDebug(
        verbose,
        () => 'fetch start env=$env hasEtag=${_etag != null && _etag!.isNotEmpty}',
      );
      final resp = await client
          .get(uri, headers: headers)
          .timeout(fetchTimeout);

      if (resp.statusCode == 304) {
        cwDebug(verbose, () => 'fetch 304 cached env=$env');
        _lastFetchAt = now;
        _lastFetchStatus = FetchStatus.cached;
        return false;
      }
      if (resp.statusCode != 200) {
        final status = resp.statusCode;
        cwDebug(
          verbose,
          () => 'fetch non-200 status=$status env=$env',
        );
        _lastFetchStatus = FetchStatus.error;
        return false;
      }

      final parsed = _parseFetchBody(resp.body);
      if (parsed == null) {
        // Malformed 200: keep stale cache, no throw.
        cwDebug(verbose, () => 'fetch malformed 200 env=$env');
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
      cwDebug(
        verbose,
        () => 'fetch 200 version=${parsed.version} etag=${parsed.etag} '
            'keys=${parsed.values.length}',
      );

      // Best-effort persistence + analytics: neither may fail the fetch.
      try {
        await _store.save(
          CacheData(
            etag: _etag ?? parsed.etag,
            version: _version,
            fetchedAt: at,
            values: parsed.values,
            variants: Map<String, String>.from(_variants),
          ),
        );
      } catch (e) {
        final saveError = apiKey.isNotEmpty
            ? '$e'.replaceAll(apiKey, '<redacted>')
            : '$e';
        cwDebug(verbose, () => 'store.save failed env=$env error=$saveError');
      }
      await postFetchEvent(
        client: client,
        baseUrl: baseUrl,
        env: env,
        apiKey: apiKey,
        userId: uid,
        variants: Map<String, String>.from(_variants),
        verbose: verbose,
      );
      return true;
    } catch (e) {
      // Network failure, timeout, malformed URL, sync throw from the
      // transport: stale cache (or cold defaults) + error status.
      final fetchError = apiKey.isNotEmpty
          ? '$e'.replaceAll(apiKey, '<redacted>')
          : '$e';
      cwDebug(verbose, () => 'fetch error env=$env error=$fetchError');
      _lastFetchStatus = FetchStatus.error;
      return false;
    }
  }

  /// Builds `GET /api/v1/env/{env}/config?...` with targeting query
  /// params omitted when empty (`TargetingContext.toQueryParameters`).
  Uri _buildConfigUri(TargetingContext targeting) {
    final base = baseUrl.replaceAll(RegExp(r'/+$'), '');
    final path = '/api/v1/env/${Uri.encodeComponent(env)}/config';
    final query = targeting.toQueryParameters();
    if (query.isEmpty) return Uri.parse('$base$path');
    return Uri.parse('$base$path').replace(queryParameters: query);
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
    cwDebug(
      verbose,
      () => 'apply version=$version etag=$etag keys=${values.length} '
          'variants=${variants.length}',
    );
    _values
      ..clear()
      ..addAll(_defaults)
      ..addAll(values);
    _variants
      ..clear()
      ..addEntries(variants.entries.where((e) => e.value.isNotEmpty));
    _etag = etag;
    _version = version;
    // Cache-loaded rows arm the throttle (offline-first: a fresh cache
    // means "just fetched"); live 200/304 paths set _lastFetchAt
    // explicitly after this call.
    _lastFetchAt = fetchedAt;
  }

  /// Starts live updates: SSE stream + poll fallback.
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
    cwDebug(verbose, () => 'realtime connect start env=$env baseUrl=$baseUrl');
    await disconnectRealtime();
    final updater = RealtimeUpdater(
      baseUrl: baseUrl,
      apiKey: apiKey,
      env: env,
      pollInterval: pollInterval,
      verbose: verbose,
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
    cwDebug(verbose, () => 'realtime connected env=$env');
  }

  /// Stops live updates started by [connectRealtime]. Safe when never
  /// connected. Never throws.
  Future<void> disconnectRealtime() async {
    final updater = _realtime;
    _realtime = null;
    cwDebug(
      verbose,
      () => 'realtime disconnect start env=$env active=${updater != null}',
    );
    if (updater != null) {
      try {
        await updater.dispose();
      } catch (_) {
        // Dispose must not throw out of disconnect.
      }
    }
    cwDebug(
      verbose,
      () => 'realtime disconnected env=$env wasActive=${updater != null}',
    );
  }

  /// Closes the realtime updater (if any), then the [onUpdate] stream
  /// and the owned HTTP client. Test/smoke only.
  Future<void> dispose() async {
    cwDebug(verbose, () => 'dispose start env=$env');
    await disconnectRealtime();
    try {
      await _updateController.close();
    } catch (_) {
      // Double-dispose must not throw.
    }
    _owned?.close();
    cwDebug(verbose, () => 'dispose complete env=$env');
  }
}

/// Parsed 200 body; null when the body is not a well-formed fetch document.
class _FetchDoc {
  _FetchDoc({
    required this.values,
    required this.variants,
    required this.etag,
    required this.version,
  });

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
    final v = version is int ? version : (version is num ? version.toInt() : 0);
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

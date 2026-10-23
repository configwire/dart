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
/// The stable bucket is the exception: parallel cold fetches are
/// first-write-wins (a losing racer never clobbers the winner).
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
    this._targeting = Targeting.empty,
  }) : _defaults = Map<String, Object?>.from(defaults),
       _values = Map<String, Object?>.from(defaults),
       _store = store ?? MemoryCacheStore(),
       _storeExplicit = store != null;

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

  /// Last targeting attributes used for fetch (sticky across calls).
  /// Set via the `targeting:` constructor param, [setTargeting], or
  /// [updateTargeting]; realtime refreshes reuse it verbatim so
  /// push/poll ticks stay evaluated for the same user/device.
  ///
  /// Seeded by the `targeting:` constructor param (defaults to
  /// [Targeting.empty]).
  Targeting _targeting;

  /// Current targeting attributes (sticky; see [_targeting]).
  Targeting get targeting => _targeting;

  /// Replaces the sticky targeting attributes without fetching.
  /// The next [fetchAndActivate] (incl. realtime ticks) uses it.
  /// For single-field merges use [updateTargeting].
  void setTargeting(Targeting targeting) {
    _targeting = targeting;
  }

  /// Merges the given non-null fields into the sticky [_targeting]
  /// without fetching. Fields left null keep their current values
  /// (via [Targeting.copyWith]); the next [fetchAndActivate] (incl.
  /// realtime ticks) uses the merged value.
  void updateTargeting({
    String? userId,
    String? platform,
    String? appVersion,
    String? locale,
    String? country,
    Map<String, Object?>? customAttrs,
  }) {
    _targeting = _targeting.copyWith(
      userId: userId,
      platform: platform,
      appVersion: appVersion,
      locale: locale,
      country: country,
      customAttrs: customAttrs,
    );
  }

  /// Cache persistence. When null at construction, [MemoryCacheStore]
  /// is used (session only, no disk); pass a [CacheStore]
  /// implementation for disk persistence. May be replaced once via
  /// [setStore] before [ensureInitialized] runs.
  CacheStore _store;

  bool _storeExplicit;

  /// Backing [CacheStore], exposed for Flutter validation.
  CacheStore get store => _store;

  /// Whether a [CacheStore] was explicitly provided; the Flutter companion
  /// auto-attaches its default store only when false.
  bool get hasExplicitStore => _storeExplicit;

  /// Replaces the backing [CacheStore] before initialization.
  ///
  /// Lets `configureFlutter(store: ...)` upgrade a [ConfigWire] built
  /// without `store:`. Late calls (after [ensureInitialized] starts) are
  /// ignored — swapping then would fork cached state. Never throws.
  void setStore(CacheStore store) {
    if (_initialized || _initFuture != null) {
      assert(
        false,
        'setStore: ignored, already initialized (store is fixed after '
        'ensureInitialized starts). Pass store: in the ConfigWire '
        'constructor or call setStore before ensureInitialized.',
      );
      cwDebug(verbose, () => 'setStore: ignored, already initialized');
      return;
    }
    _store = store;
    _storeExplicit = true;
  }

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

  /// Server-minted stable bucket (`0-9999`, null when absent).
  /// Persisted beside the verbatim etag, sent as `?b=`, echoed by the
  /// server on later 200s. A 304 never clears it; a 200 without the
  /// field (old server) keeps the prior value.
  int? _bucket;
  DateTime? _lastFetchAt;
  FetchStatus _lastFetchStatus = FetchStatus.none;

  /// In-flight initialization shared by concurrent [ensureInitialized]
  /// callers; null when no init is running.
  Future<void>? _initFuture;

  /// True once the first [ensureInitialized] attempt has finished
  /// (success or soft-failure). Later calls are no-ops.
  bool _initialized = false;

  /// Status of the last fetch attempt (`none` before the first attempt).
  FetchStatus get lastFetchStatus => _lastFetchStatus;

  /// Time of the last successful fetch (200/304), or null if none yet.
  DateTime? get fetchTime => _lastFetchAt;

  /// Stored etag served verbatim back via `If-None-Match` (exact match).
  String? get etag => _etag;

  /// Persisted stable bucket (`0-9999`), or null when no 200 has minted
  /// one yet (or the server predates the bucket field). Sent as `?b=`.
  int? get bucket => _bucket;

  /// Latest activated release version (0 when nothing activated yet).
  int get version => _version;

  /// Returns the bool value for [key], or [fallback] (null by default)
  /// when missing or mistyped.
  bool? getBool(String key, {bool? fallback}) {
    final v = _values[key] ?? _defaults[key];
    return v is bool ? v : fallback;
  }

  /// Returns the String value for [key], or [fallback] (null by default)
  /// when missing or mistyped.
  String? getString(String key, {String? fallback}) {
    final v = _values[key] ?? _defaults[key];
    return v is String ? v : fallback;
  }

  /// Returns the int value for [key], or [fallback] (null by default)
  /// when missing or mistyped.
  int? getInt(String key, {int? fallback}) {
    final v = _values[key] ?? _defaults[key];
    return v is int ? v : fallback;
  }

  /// Returns the double value for [key], coercing ints via `toDouble()`,
  /// or [fallback] (null by default) when missing or mistyped.
  double? getDouble(String key, {double? fallback}) {
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

  /// Returns a copy of the list at [key] with every element of type [T],
  /// or [fallback] (null by default) on any error (missing key, mistyped
  /// value, or a mistyped element).
  ///
  /// Values here are already decoded, so each element is checked eagerly
  /// and a copy is returned instead of a lazy cast view. Int elements
  /// are coerced via `toDouble()` when `T` is `double` (parity with
  /// [getDouble]).
  List<T>? getList<T>(String key, {List<T>? fallback}) {
    try {
      return _coerceList<T>(key, _values[key] ?? _defaults[key]);
    } catch (_) {
      return fallback;
    }
  }

  /// Returns a copy of the JSON object at [key] with every value of type
  /// [T], or [fallback] (null by default) on any error (missing key,
  /// mistyped value, non-String key, or a mistyped entry).
  ///
  /// Values here are already decoded, so each entry is checked eagerly
  /// and a copy is returned instead of a lazy cast view. Int values
  /// are coerced via `toDouble()` when `T` is `double` (parity with
  /// [getDouble]).
  Map<String, T>? getMap<T>(String key, {Map<String, T>? fallback}) {
    try {
      return _coerceMap<T>(key, _values[key] ?? _defaults[key]);
    } catch (_) {
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
  /// the cache or re-fetching.
  ///
  /// Targeting is sticky ([targeting], seeded by the `targeting:`
  /// constructor param): to fetch as a user, call [setTargeting] or
  /// [updateTargeting] before [ensureInitialized]/[fetchAndActivate].
  /// To clear: `setTargeting(const Targeting())`.
  Future<void> ensureInitialized() async {
    cwDebug(
      verbose,
      () =>
          'ensureInitialized start env=$env '
          'initialized=$_initialized',
    );
    if (_initialized) {
      cwDebug(
        verbose,
        () =>
            'ensureInitialized complete (already initialized) env=$env '
            'status=$_lastFetchStatus',
      );
      return;
    }
    final inFlight = _initFuture;
    if (inFlight != null) {
      await inFlight;
      cwDebug(
        verbose,
        () =>
            'ensureInitialized complete (joined in-flight) env=$env '
            'status=$_lastFetchStatus',
      );
      return;
    }
    final future = _doEnsureInitialized();
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
  Future<void> _doEnsureInitialized() async {
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
        () =>
            'cache hit values=${hit.values.length} keys '
            'version=${hit.version} etag=${hit.etag}',
      );
      _applyServerValues(
        values: hit.values,
        etag: hit.etag,
        version: hit.version,
        fetchedAt: hit.fetchedAt,
        variants: hit.variants,
        bucket: hit.bucket,
      );
    } else if (loadFailed) {
      cwDebug(verbose, () => 'cache corrupt (load threw) env=$env');
    } else {
      cwDebug(verbose, () => 'cache miss env=$env');
    }
    await fetchAndActivate(force: true);
  }

  /// Fetches the latest release and activates it.
  ///
  /// Returns true only on a 200 that replaced values. 304 (unchanged),
  /// throttled skips, and every failure mode return false — network or
  /// parse failure keeps the stale cache, a cold cache keeps in-app
  /// defaults, and nothing here ever throws.
  ///
  /// Latency note: on a successful fetch this awaits one best-effort
  /// analytics POST (`postEvent`, up to 5s timeout) before
  /// returning. The POST is intentionally awaited (not fire-and-forget)
  /// so callers/tests observe the event body; failures are swallowed
  /// and never fail the fetch.
  ///
  /// [force] skips the [minimumFetchInterval] throttle check. The
  /// realtime path ([connectRealtime]) always passes `force: true` so
  /// push events and poll ticks stay fresh regardless of the throttle;
  /// direct callers keep the default `false`.
  ///
  /// Targeting is sticky ([targeting], seeded by the `targeting:`
  /// constructor param): [fetchAndActivate] always evaluates the stored
  /// value. To fetch as another user, call [setTargeting] for a wholesale
  /// replace or [updateTargeting] for a single-field merge, then fetch:
  /// `cw.setTargeting(const Targeting(userId: 'u')); await cw.fetchAndActivate();`
  /// `cw.updateTargeting(platform: 'ios'); await cw.fetchAndActivate();`
  Future<bool> fetchAndActivate({bool force = false}) async {
    final now = DateTime.now().toUtc();
    if (!force &&
        minimumFetchInterval > Duration.zero &&
        _lastFetchAt != null &&
        now.difference(_lastFetchAt!) < minimumFetchInterval) {
      cwDebug(verbose, () => 'fetch throttled skip env=$env');
      _lastFetchStatus = FetchStatus.throttled;
      return false;
    }

    final client = _http;
    // Snapshot the bucket at request start for the first-write-wins
    // guard below: parallel cold fetches all start bucket-less, and the
    // winner is whichever 200 applies first.
    final bucketAtStart = _bucket;
    try {
      final uri = _buildConfigUri(_targeting);
      final headers = <String, String>{'X-ConfigWire-Key': apiKey};
      if (_etag != null && _etag!.isNotEmpty) {
        // Stored verbatim; the server 304s on EXACT match only.
        headers['If-None-Match'] = _etag!;
      }
      cwDebug(
        verbose,
        () =>
            'fetch start env=$env hasEtag=${_etag != null && _etag!.isNotEmpty}',
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
        cwDebug(verbose, () => 'fetch non-200 status=$status env=$env');
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
      // First-write-wins: this fetch started bucket-less but a racing
      // fetch already persisted one while we were in flight. Keep the
      // winner (pass null = keep prior) instead of clobbering it with
      // our own freshly minted bucket. A racing second 200 therefore
      // never overwrites an already-persisted bucket, and the losing
      // save below re-persists the winner so the store stays consistent.
      // Sequential fetches are unaffected: they start with the bucket
      // set, so their 200 (a server echo of the same number) applies.
      int? bucketToApply = parsed.bucket;
      if (bucketAtStart == null && _bucket != null) {
        bucketToApply = null;
      }
      _applyServerValues(
        values: parsed.values,
        etag: parsed.etag,
        version: parsed.version,
        fetchedAt: at,
        variants: parsed.variants,
        bucket: bucketToApply,
      );
      _lastFetchAt = at;
      _lastFetchStatus = FetchStatus.success;
      cwDebug(
        verbose,
        () =>
            'fetch 200 version=${parsed.version} etag=${parsed.etag} '
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
            bucket: _bucket,
          ),
        );
      } catch (e) {
        final saveError = apiKey.isNotEmpty
            ? '$e'.replaceAll(apiKey, '<redacted>')
            : '$e';
        cwDebug(verbose, () => 'store.save failed env=$env error=$saveError');
      }
      await trackFetch();
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

  /// Reports that the current user was exposed to [flag]'s evaluated value.
  ///
  /// Call this where [flag]'s value actually influenced behavior —
  /// after a `getBool`/`getString`/… read decided what to render or do —
  /// not on every fetch. That is what keeps `exposures` and
  /// `perVariant`/`rates` in `GET .../stats?flag=<key>` meaningful:
  /// one exposure per user decision, not per config refresh.
  ///
  /// ```dart
  /// await cw.fetchAndActivate();
  /// final on = cw.getBool('checkout') ?? false;
  /// render(on);
  /// await cw.trackExposure('checkout');
  /// ```
  ///
  /// Sends one best-effort `exposure` event snapping the sticky state at
  /// call time: `variant` is `_variants[flag] ?? ''` (unknown or
  /// non-experiment flags record `''`), identity is the trimmed sticky
  /// user id (hashed to 16 hex chars, anonymous sends `''`), and
  /// `version` is the current [_version]. Empty/whitespace flags are a
  /// no-op (an exposure without a flag key is meaningless).
  ///
  /// Never throws: offline, timeout, and malformed-URL faults are
  /// swallowed, with a `cwDebug` line when [verbose] is true.
  Future<void> trackExposure(String flag) async {
    try {
      if (flag.trim().isEmpty) {
        cwDebug(verbose, () => 'trackExposure skipped env=$env empty flag');
        return;
      }
      await postEvent(
        client: _http,
        baseUrl: baseUrl,
        env: env,
        apiKey: apiKey,
        userId: _targeting.userId.trim(),
        variants: Map<String, String>.from(_variants),
        flag: flag,
        version: _version,
        verbose: verbose,
        kind: EventKind.exposure,
        bucket: _bucket,
      );
    } catch (e) {
      final safe = apiKey.isNotEmpty
          ? '$e'.replaceAll(apiKey, '<redacted>')
          : '$e';
      cwDebug(verbose, () => 'trackExposure failed env=$env error=$safe');
    }
  }

  /// Reports a manual fetch event, optionally attributed to one flag.
  ///
  /// You rarely need this: [fetchAndActivate] already sends the env-wide
  /// aggregate (`flag: ''`) on every 200 (and calls this helper to do
  /// it). Call it only when one evaluated flag deserves its own fetch
  /// attribution alongside the aggregate — the per-flag counterpart to
  /// [trackExposure].
  ///
  /// ```dart
  /// await cw.trackFetch(flag: 'checkout'); // per-flag fetch counts
  /// await cw.trackFetch(); // env-wide aggregate, same as the auto event
  /// ```
  ///
  /// Same snapshot semantics as [trackExposure] but with kind `fetch`:
  /// `variant` is `_variants[flag] ?? ''`, identity is the trimmed
  /// sticky user id, and `version` is the current [_version]. The
  /// default `flag: ''` sends the env-wide aggregate (no single flag
  /// attribution).
  ///
  /// Never throws: every failure mode is swallowed, with a `cwDebug`
  /// line when [verbose] is true (see [trackExposure]).
  Future<void> trackFetch({String flag = ''}) async {
    try {
      await postEvent(
        client: _http,
        baseUrl: baseUrl,
        env: env,
        apiKey: apiKey,
        userId: _targeting.userId.trim(),
        variants: Map<String, String>.from(_variants),
        flag: flag,
        version: _version,
        verbose: verbose,
        kind: EventKind.fetch,
        bucket: _bucket,
      );
    } catch (e) {
      final safe = apiKey.isNotEmpty
          ? '$e'.replaceAll(apiKey, '<redacted>')
          : '$e';
      cwDebug(verbose, () => 'trackFetch failed env=$env error=$safe');
    }
  }

  /// Builds `GET /api/v1/env/{env}/config?...` with targeting query
  /// params omitted when empty ([Targeting.toQueryParameters]).
  /// Identity is hash-only: `userId` emits as `?userHash=<sha256hex16>`,
  /// never raw `?uid=`; empty identity sends the bare path.
  /// When a stable bucket is persisted it is sent as `?b=<n>` alongside
  /// the targeting params (canonical plain int string, never mangled).
  Uri _buildConfigUri(Targeting targeting) {
    final base = baseUrl.replaceAll(RegExp(r'/+$'), '');
    final path = '/api/v1/env/${Uri.encodeComponent(env)}/config';
    final query = targeting.toQueryParameters();
    if (_bucket != null) query['b'] = '$_bucket';
    if (query.isEmpty) return Uri.parse('$base$path');
    return Uri.parse('$base$path').replace(queryParameters: query);
  }

  /// Applies one server layer: values merged over defaults, anonymous
  /// (`""`) variants omitted, etag/version/fetchAt replaced verbatim.
  /// A non-null [bucket] replaces the persisted bucket; null keeps the
  /// prior value (304 path never calls here, old servers omit the field,
  /// and the first-write-wins guard passes null for a losing racer).
  void _applyServerValues({
    required Map<String, Object?> values,
    required String etag,
    required int version,
    required DateTime fetchedAt,
    required Map<String, String> variants,
    int? bucket,
  }) {
    cwDebug(
      verbose,
      () =>
          'apply version=$version etag=$etag keys=${values.length} '
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
    if (bucket != null) _bucket = bucket;
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

  /// Clears the stable bucket without touching values, etag, version,
  /// or targeting — the logout/user-switch seam. Nulls the in-memory
  /// [_bucket] and re-persists the current row with `bucket: null`, so
  /// the next [fetchAndActivate] sends no `?b=` and a later restart
  /// stays bucket-absent. Blocked storage degrades to a memory-only
  /// clear. Never throws.
  Future<void> clearBucket() async {
    _bucket = null;
    try {
      await _store.save(
        CacheData(
          etag: _etag ?? '',
          version: _version,
          fetchedAt: _lastFetchAt ?? DateTime.now().toUtc(),
          values: Map<String, Object?>.from(_values),
          variants: Map<String, String>.from(_variants),
          bucket: null,
        ),
      );
    } catch (e) {
      final safe = apiKey.isNotEmpty
          ? '$e'.replaceAll(apiKey, '<redacted>')
          : '$e';
      cwDebug(verbose, () => 'clearBucket save failed env=$env error=$safe');
    }
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

/// Rebuilds [v] as a `List<E>` copy, checking every element eagerly.
/// Int elements coerce via `toDouble()` when `E` is `double`.
/// Throws a [StateError] when [v] is not a List or an element mistypes —
/// the shared rule behind [ConfigWire.getList].
List<E> _coerceList<E>(String key, Object? v) {
  if (v is! List) {
    throw StateError('getList($key): value is ${v.runtimeType}, not List');
  }
  if (v.isEmpty) return <E>[];
  final out = <E>[];
  for (final e in v) {
    if (e is E) {
      out.add(e);
    } else if (e is int && E == double) {
      out.add(e.toDouble() as E);
    } else {
      throw StateError('getList($key): element ${e.runtimeType} is not $E');
    }
  }
  return out;
}

/// Rebuilds [v] as a `Map<String, V>` copy, checking every entry eagerly.
/// Int values coerce via `toDouble()` when `V` is `double`.
/// Throws a [StateError] when [v] is not a Map, a key is not a String,
/// or an entry mistypes — the shared rule behind [ConfigWire.getMap].
Map<String, V> _coerceMap<V>(String key, Object? v) {
  if (v is! Map) {
    throw StateError('getMap($key): value is ${v.runtimeType}, not Map');
  }
  if (v.isEmpty) return <String, V>{};
  final out = <String, V>{};
  for (final e in v.entries) {
    if (e.key is! String) {
      throw StateError('getMap($key): key ${e.key.runtimeType} is not String');
    }
    final k = e.key as String;
    final val = e.value;
    if (val is V) {
      out[k] = val;
    } else if (val is int && V == double) {
      out[k] = val.toDouble() as V;
    } else {
      throw StateError(
        'getMap($key): value of "$k" is ${val.runtimeType}, not $V',
      );
    }
  }
  return out;
}

/// Parsed 200 body; null when the body is not a well-formed fetch document.
class _FetchDoc {
  _FetchDoc({
    required this.values,
    required this.variants,
    required this.etag,
    required this.version,
    this.bucket,
  });

  final Map<String, Object?> values;
  final Map<String, String> variants;
  final String etag;
  final int version;

  /// Server-minted stable bucket (`0-9999`); null when the field is
  /// absent or misshapen (old server) — the caller keeps the prior value.
  final int? bucket;
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
    // Tolerant bucket parse (mirrors CacheData.fromJson): canonical
    // `0-9999` ints only; missing, wrong shapes, and out-of-range parse
    // as absent (null) so old servers keep working and never throw.
    int? bucket;
    final rawBucket = m['bucket'];
    if (rawBucket is int && rawBucket >= 0 && rawBucket <= 9999) {
      bucket = rawBucket;
    }
    return _FetchDoc(
      values: Map<String, Object?>.from(values),
      variants: variants,
      etag: etag,
      version: v,
      bucket: bucket,
    );
  } catch (_) {
    return null;
  }
}

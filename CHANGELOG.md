## 0.3.2

### Changed

- Backward compatible with 0.3.x: no migration needed — stable install
  IDs work with no signature change.
  `@Deprecated` lifted from `Targeting.userId`
  (`lib/src/targeting.dart`) and `sha256Hex16` (`lib/src/events.dart`):
  `userId` is hashed client-side to `?userHash=<16-hex>` (raw IDs never
  leave the device); the server ignores `userHash` for bucketing
  (accepted-but-ignored, `X-CW-Deprecated-userHash` compat header
  stamped) and buckets from its daily anonID unless a valid `?b=`
  overrides it. Existing `Targeting(userId: ...)` calls compile
  unchanged. Pairs with server `v0.1.13`. `Targeting.country` stays
  deprecated (server derives country from headers).
- Stable bucket `?b=` (`lib/src/cache_model.dart`,
  `lib/src/configwire_base.dart`, `lib/src/events.dart`,
  `lib/src/targeting.dart`): the client persists the server-minted
  `bucket` first-write-wins and sends it as `?b=` on fetch and events,
  so bucketing survives daily anonID rotation; servers without `bucket`
  support accept the same fetches as anonymous (still `200`).

## 0.3.1

### Deprecated

- `Targeting.userId` / `Targeting.country` (`lib/src/targeting.dart`)
  are deprecated (accepted-but-ignored): the server derives an
  ephemeral anonID per request and derives country from request
  headers, ignoring the client-sent values. `toQueryParameters`
  keeps emitting `?userHash=` / `country` for the compat window;
  removal lands in the next major release.
  **Migration:** no action needed — stop setting identity; existing
  `Targeting(userId: ...)` calls compile unchanged.
  Supported server: `configwire ^0.1.9` or later.
- `sha256Hex16` / `postEvent` `userHash` compat emission
  (`lib/src/events.dart`) is deprecated: the server ignores
  client-sent identity hashes. Kept for the compat window; will be
  removed in the next major release. No action needed.

## 0.3.0

### Changed

- **Breaking:** fetch identity is now userHash-only (`lib/src/targeting.dart`
  `toQueryParameters` emits `?userHash=<sha256Hex16(userId)>`;
  `lib/src/events.dart` posts `userHash`). Raw `uid` is never sent:
  no `?uid=` emission and no dual-send (hash-only by design).
  **Migration:** no client code change needed unless you depended on
  `?uid=` server-side — key rules off `UserHash` instead.
  Supported server: `configwire ^0.1.9` or later (dual-accept window):
  **upgrade the server first**; older servers (`<0.1.9`) will treat
  these fetches as anonymous.

## 0.2.1

### Added

- Opt-in `ConfigWire.trackExposure(flag)` / `ConfigWire.trackFetch({flag})`
  helpers (`lib/src/configwire_base.dart`) plus an `EventKind`
  (`fetch` / `exposure`, serialized via `kind.name`) `kind` param on the
  canonical `postEvent(...)` (`lib/src/events.dart`).
  `trackExposure('exp_bool')` posts
  `{"events":[{"kind":"exposure","flag":"exp_bool",
  "variant":"<arm>","userHash":"<sha256hex16>","version":<n>}]}`;
  `trackFetch()` posts the env-wide `flag: ''` aggregate. Both helpers
  return `Future<void>` and never throw (offline/timeout/malformed URL
  swallowed; empty exposure flags send nothing). `fetchAndActivate`
  auto-send behavior unchanged (env-wide `flag: ''`). Legacy
  `postFetchEvent` is deprecated and forwards to `postEvent` (old calls
  compile unchanged).
- Restored per-flag attribution on the fetch-event POST
  (`lib/src/events.dart`): `postFetchEvent(...)` takes a new optional
  `flag` (`String`, defaults to `''`) and sends it as `'flag': flag`
  with `variant: variants[flag] ?? ''`
  (`{"events":[{"kind":"fetch","flag":"exp_bool","variant":"control",
  "userHash":"<sha256hex16(userId)>","version":1}]}`) so stats can
  break fetches down by flag (`GET .../stats?flag=<key>`).
  `ConfigWire.fetchAndActivate` keeps sending the env-wide aggregate
  (`flag: ''`): a whole-release fetch has no single flag attribution.
  Fire-and-forget posture unchanged (never fails the fetch; key
  redacted from error strings). Requires server with per-flag events;
  older env-wide-only servers ignore the `flag` key.

## 0.2.0

### Changed

- **Breaking:** scalar getters are now nullable with no silent default
  (`lib/src/configwire_base.dart`): `getBool` returns `bool?` (was
  `bool` defaulting to `false`), `getString` returns `String?` (was
  `''`), `getInt` returns `int?` (was `0`), `getDouble` returns
  `double?` (was `0.0`). A missing or mistyped key returns `null`
  unless an explicit `fallback:` is passed.
  **Migration**: `cw.getBool('k')` -> `cw.getBool('k') ?? false`;
  `cw.getString('k')` -> `cw.getString('k') ?? ''`;
  `cw.getInt('k')` -> `cw.getInt('k') ?? 0`;
  `cw.getDouble('k')` -> `cw.getDouble('k') ?? 0.0`;
  or branch on `null` to detect a missing/mistyped flag.
- **Breaking:** `getList<T>` returns `List<T>?` (was `List<T>`
  defaulting to `const []`) and `getMap<T>` returns
  `Map<String, T>?` (was `Map<String, T>` defaulting to `const {}`).
  The `fallback` default is now `null`, and the old
  `fallback: null` rethrow contract is removed: any error (missing
  key, mistyped value, mistyped element/entry) returns `fallback`.
  **Migration**: `cw.getList<String>('k')` ->
  `cw.getList<String>('k') ?? const []`;
  `cw.getMap<int>('k')` -> `cw.getMap<int>('k') ?? const {}`;
  drop `fallback: null` where it was used to rethrow.
- **Breaking:** `RealtimeUpdater.disconnectSync` is now private
  (`_disconnectSync`). Use `disconnect()` (async) instead.
- **Breaking:** the fetch-event POST no longer sends the deprecated
  `'flag': ''` key (`lib/src/events.dart`). ConfigWire server v0.1.0
  removed per-event flag attribution (ingest takes
  kind/variant/userHash/version only; `1790000008_drop_event_flag`
  dropped the `events.flag` / `event_daily.flag` columns), so payloads
  are now `{"events":[{"kind":"fetch","variant":"",
  "userHash":"<sha256hex16>","version":1}]}`. Requires server
  >= v0.1.0; older servers need SDK <0.2.0.

## 0.1.2

### Changed

- Docs: expanded `README.md` to cover the full client surface
  (reading values, variants, fetch lifecycle, startup, targeting,
  realtime, analytics, logging, cache). No code change.

## 0.1.1

### Changed

- Deprecated per-event `flag` attribution (`lib/src/events.dart`):
  analytics are env-wide, so the fetch-event POST keeps sending
  `'flag': ''` as a minimal backward-compat shim (pre-removal servers
  still validate the payload; new servers ignore it). No action
  needed; do not send per-flag keys.

## 0.1.0

### Changed
- **Breaking:** `TargetingContext` is renamed to `Targeting`
  (`lib/src/targeting.dart`, still exported from
  `package:configwire/configwire.dart`). Targeting inputs live in exactly
  3 places: `ConfigWire(targeting: ...)` seeds the sticky targeting at
  construction, `setTargeting(Targeting targeting)` replaces it wholesale,
  and `updateTargeting(userId: ..., platform: ..., ...)` merges single
  fields — all without fetching. `ensureInitialized()` and
  `fetchAndActivate({bool force})` take no targeting param and always
  evaluate the stored targeting.
  Migrate: `const TargetingContext(userId: 'u')` ->
  `const Targeting(userId: 'u')`; `fetchAndActivate(targeting: ...)` ->
  `setTargeting(...); fetchAndActivate();`, single-field
  `fetchAndActivate(targeting: cw.targeting.copyWith(platform: 'ios'))` ->
  `updateTargeting(platform: 'ios'); fetchAndActivate();`.
- Added `ConfigWire.updateTargeting({String? userId, String? platform,
  String? appVersion, String? locale, String? country,
  Map<String, Object?>? customAttrs})`: merges the given non-null fields
  into the sticky targeting via `copyWith` without fetching, so
  single-field updates no longer need a manual rebuild.

### Added
- Fetch-event version tracking (`lib/src/events.dart`,
  `lib/src/configwire_base.dart`): `postFetchEvent(...)` takes a new
  optional `version` (`int`, defaults to `0`) and includes it in the
  fetch-event POST (`{"events":[{"kind":"fetch","flag":"","variant":"",
  "userHash":"<sha256hex16(userId)>","version":1}]}`).
  `ConfigWire.fetchAndActivate` passes `parsed.version` so stats can
  break fetches down per release version. Backward compatible: old
  servers ignore the unknown `version` field; old SDKs send no
  `version` and the server records `0`. Analytics stays
  fire-and-forget (all failure modes swallowed).

## 0.0.5

### Added
- Opt-in verbose debug logging via `lite_logger`
  (`lib/src/cw_logger.dart`, new `lite_logger: ^0.3.0` dependency).
  Off by default; pass `verbose: true` to `ConfigWire(...)`,
  `RealtimeUpdater(...)`, or `postFetchEvent(...)` to emit debug logs
  for init, cache hit/miss, fetch start/304/non-200/malformed/success,
  store-save failures, SSE connect/status/close, poll ticks, and the
  fetch-event POST. Messages are lazy closures so nothing is built when
  `verbose` is false. The `apiKey` is never logged (only sent as the
  `X-ConfigWire-Key` header); caught errors are redacted before logging.
  `ConfigWire(verbose: true)` propagates to its realtime updater and
  fetch-event calls.

## 0.0.4

### Changed
- `ensureInitialized` (`lib/src/configwire_base.dart`) is now idempotent
  and safe to call multiple times. Concurrent calls share the same
  in-flight initialization (single cache load + single forced fetch),
  and calls after the first attempt return immediately without
  re-loading the cache or re-fetching. Passing a non-null `context`
  still triggers a forced `fetchAndActivate` refresh with the new
  targeting, so retargeting via `ensureInitialized` keeps working.

## 0.0.3

### Added
- `get<T>` (`lib/src/configwire_base.dart`): generic typed getter.
  Returns the value for `key` as `T`, or `fallback` when missing or
  mistyped. Coerces ints via `toDouble()` when `T` is `double` (parity
  with `getDouble`). With no `fallback`, a missing/mistyped key returns
  `null` for nullable `T` and throws a cast error for non-nullable `T`.
  Maps and lists are returned as defensive copies. Note: server
  collections decode as `List<dynamic>`/`Map<String, dynamic>`, so a
  direct `get<List<String>>` over server values misses — use
  `getList`/`getMap` for those.
- `getList<T>` (`lib/src/configwire_base.dart`): typed list getter.
  Eagerly checks every element (plus `int` -> `double` coercion when
  `T` is `double`); defaults `fallback` to `const []`. On any error
  (missing key, mistyped value, or mistyped element) returns `fallback`;
  when `fallback` is null the error is rethrown.
- `getMap<T>` (`lib/src/configwire_base.dart`): typed map getter.
  Eagerly checks every entry is `String` -> `T` (plus `int` -> `double`
  coercion when `T` is `double`); defaults `fallback` to `const {}`.
  On any error (missing key, mistyped value, non-String key, or mistyped
  entry) returns `fallback`; when `fallback` is null the error is
  rethrown.

### Changed
- **Breaking:** `getJSON(key)` is removed. It returned an untyped
  `Map<String, Object?>` (or `{}` on miss/mistype).
  Migrate: `getJSON('k')` -> `getMap<Object?>('k')`, or pick a value
  type such as `getMap<int>('k')` / `getMap<String>('k')`.

## 0.0.2

### Added
- `TargetingContext` (`lib/src/targeting.dart`, exported from
  `package:configwire/configwire.dart`): carries `userId`, `platform`,
  `appVersion`, `locale`, `country`, `customAttrs` for server-side rule
  evaluation. Empty fields are omitted from the fetch query; `customAttrs`
  is sent as `?attrs=<json>`. Includes `copyWith`, `empty`, and
  `toQueryParameters()`.

### Changed
- **Breaking:** `fetchAndActivate` and `ensureInitialized` now take only
  `{TargetingContext? context, bool force}`. The flattened
  `String? userId/platform/appVersion/locale/country` and
  `Map? customAttrs` params are removed.
  Migrate: `fetchAndActivate(userId: 'u')` ->
  `fetchAndActivate(context: const TargetingContext(userId: 'u'))`;
  single-field update ->
  `fetchAndActivate(context: cw.targeting.copyWith(platform: 'ios'))`.
- Targeting is sticky: a non-null `context` replaces `cw.targeting` for
  this and all future fetches, including `connectRealtime` poll/SSE
  refreshes (`fetchAndActivate(force: true)` reuses the stored context).
  `setTargeting` replaces the stored context without fetching.

## 0.0.1

- Initial release of the pure-Dart `configwire` client.

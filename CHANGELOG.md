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

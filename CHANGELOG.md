## Unreleased

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

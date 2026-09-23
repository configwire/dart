# configwire

Pure-Dart ConfigWire client (fetch, cache, typed getters, realtime).
Works on Dart VM and Flutter from this single package — no Flutter
facade needed. Requires Dart SDK `>=3.12.0`.

## Install

This package is not on pub.dev. Add it to your `pubspec.yaml` via a
path (or git) reference, then run `dart pub get` (`flutter pub get`
on Flutter):

```yaml
dependencies:
  configwire:
    path: ../configwire # adjust to your checkout layout
```

## Usage

Bring-your-own-box: init Hive yourself, open the box, and hand it to
`HiveCacheStore`. Omit `store:` for a session-only in-memory cache.

```dart
import 'package:configwire/configwire.dart';
import 'package:hive_ce/hive_ce.dart';

// Dart VM:
Hive.init('/app/data/cw-hive');
// Flutter: await Hive.initFlutter();
// Web: no init (IndexedDB), just open the box.
final box = await Hive.openBox('configwire_cache');

final cw = ConfigWire(
  apiKey: 'YOUR_SDK_KEY', // sent as X-ConfigWire-Key, never printed
  env: 'dev',
  baseUrl: 'http://127.0.0.1:8090',
  defaults: {'launch_flag': false},
  store: HiveCacheStore(box: box, env: 'dev'),
);
await cw.ensureInitialized();
await cw.fetchAndActivate();
final on = cw.getBool('launch_flag');
await cw.dispose(); // never closes the box; host owns Hive.close()
```

Realtime: `cw.connectRealtime()` opens SSE plus a 15min poll fallback,
so freshness is at most `pollInterval` plus one fetch on every path.

See `example/main.dart` for a runnable demo. Wire details live in the
repo `docs/CONTRACT.md`.

## Cache

Persistence is one JSON string per environment under `cache_<env>` in
your box, via `hive_ce` (pure-Dart part only — this package stays
Flutter-free, no `hive_ce_flutter` dependency). Pass an explicit
`cacheKey` to isolate tenants sharing one box:

```dart
store: HiveCacheStore(box: box, env: 'dev', cacheKey: 'tenant_a'),
```

Values persist as cleartext JSON: never put tokens, secrets, or PII
into flag values or defaults (on Web the box is additionally readable
by site JS, so the XSS framing applies). Blocked storage (private
mode, denied quota) degrades to load-null/save-noop — the client keeps
serving defaults plus server fetches and never throws. Web fetches
need server CORS allowing the app origin.

Run the browser smoke suite with
`dart test -p chrome test/chrome_smoke_test.dart` (needs Chrome;
`make test-chrome` wraps it, while `make test` stays VM-only).

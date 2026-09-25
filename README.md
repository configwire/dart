# configwire

Pure-Dart [ConfigWire](https://github.com/configwire/configwire) client
(fetch, cache, typed getters, realtime).
Works on Dart VM and Flutter from this single package — no Flutter
facade needed. Requires Dart SDK `>=3.12.0`.

Server wire details live in the server repo:
`https://github.com/configwire/configwire/blob/main/docs/CONTRACT.md`.

## Install

```bash
dart pub add configwire
```

Or pin it in your `pubspec.yaml`:

```yaml
dependencies:
  configwire: ^0.0.1
```

Git fallback (before/while pub.dev review is pending):

```yaml
dependencies:
  configwire:
    git:
      url: https://github.com/configwire/dart.git
      ref: main
```

Then `dart pub get` (`flutter pub get` on Flutter).

## Usage

Omit `store:` for the default session-only in-memory cache
(`MemoryCacheStore`). For disk persistence, implement the `CacheStore`
seam and pass it as `store:`:

```dart
import 'dart:convert';
import 'dart:io';

import 'package:configwire/configwire.dart';

class JsonFileStore implements CacheStore {
  JsonFileStore(this.file);
  final File file;
  @override
  Future<CacheData?> load() async {
    try {
      final decoded = jsonDecode(await file.readAsString());
      if (decoded is! Map) return null;
      return CacheData.fromJson(Map<String, Object?>.from(decoded));
    } catch (_) {
      return null; // miss/corruption: caller falls back to defaults
    }
  }
  @override
  Future<void> save(CacheData data) async {
    await file.parent.create(recursive: true);
    await file.writeAsString(jsonEncode(data.toJson()));
  }
}

final cw = ConfigWire(
  apiKey: 'YOUR_SDK_KEY', // sent as X-ConfigWire-Key, never printed
  env: 'dev',
  baseUrl: 'http://127.0.0.1:8090',
  defaults: {'launch_flag': false},
  // Omit `store:` for the session-only memory cache.
  // store: JsonFileStore(File('.configwire-cache/cache_dev.json')),
);
await cw.ensureInitialized();
await cw.fetchAndActivate();
final on = cw.getBool('launch_flag');
await cw.dispose();
```

## Targeting

Targeting attributes are sent as fetch query params so the server can
evaluate `rules` per fetch (`userId` → `?uid=`, `platform`,
`appVersion`, `locale`, `country`, `customAttrs` → `?attrs=` JSON).
Empty strings / empty maps are omitted (anonymous).

Targeting is sticky: `setTargeting` replaces the stored context without
fetching, and the next `fetchAndActivate` (including realtime ticks)
uses it. Passing `context:` to `fetchAndActivate` replaces it wholesale
for that fetch and onward:

```dart
cw.setTargeting(const TargetingContext(
  userId: 'user-7',
  platform: 'ios',
  appVersion: '1.2.3',
  locale: 'en-US',
  country: 'US',
  customAttrs: {'plan': 'pro'},
));
await cw.fetchAndActivate();

// Single-field update without rebuilding the whole context:
await cw.fetchAndActivate(
  context: cw.targeting.copyWith(platform: 'android'),
);
```

Realtime: `cw.connectRealtime()` opens SSE plus a 15min poll fallback,
so freshness is at most `pollInterval` plus one fetch on every path.

See `example/main.dart` for a runnable demo.

## Cache

Persistence is bring-your-own via the `CacheStore` seam: implement
`load()`/`save()` over the backend of your choice (JSON file,
shared_preferences, Isar, or any disk store you prefer).
`MemoryCacheStore` is session-only (no disk) and is the default when
`store:` is omitted.

Values persist as cleartext JSON: never put tokens, secrets, or PII
into flag values or defaults (on Web the store is additionally readable
by site JS, so the XSS framing applies). Blocked storage (private
mode, denied quota) degrades to load-null/save-noop — the client keeps
serving defaults plus server fetches and never throws. Web fetches
need server CORS allowing the app origin.

Run the browser smoke suite with
`dart test -p chrome test/chrome_smoke_test.dart` (needs Chrome).
The smoke suite runs on the session-only memory store with a mock HTTP
client, so no disk or backend setup is needed.

## License

MIT — Copyright (c) 2026 Lam Thanh Nhan. See [LICENSE](LICENSE).

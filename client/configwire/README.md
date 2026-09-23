# configwire

Pure-Dart ConfigWire client (fetch, cache, typed getters, realtime).

## Install

This package is not on pub.dev. Add it to your `pubspec.yaml` via a
path (or git) reference, then run `dart pub get`:

```yaml
dependencies:
  configwire:
    path: ../configwire # adjust to your checkout layout
```

Requires Dart SDK `>=3.12.0`.

## Usage

```dart
import 'package:configwire/configwire.dart';

final cw = ConfigWire(
  apiKey: 'YOUR_SDK_KEY', // sent as X-ConfigWire-Key, never printed
  env: 'dev',
  baseUrl: 'http://127.0.0.1:8090',
  defaults: {'launch_flag': false},
  cacheFile: '/tmp/cw-cache.json', // default is cwd-relative
);
await cw.ensureInitialized();
await cw.fetchAndActivate();
final on = cw.getBool('launch_flag');
await cw.dispose();
```

Realtime: `cw.connectRealtime()` opens SSE plus a 15min poll fallback,
so freshness is at most `pollInterval` plus one fetch on every path.

See `example/main.dart` for a runnable demo. Wire details live in the
repo `docs/CONTRACT.md`.

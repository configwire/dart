# configwire

Pure-Dart [ConfigWire](https://github.com/configwire/configwire) client
(fetch, cache, typed getters, realtime).
Works on Dart VM and Flutter from this single package — no Flutter
facade needed. Requires Dart SDK `>=3.12.0`.

Server wire details live in the server repo:
`https://github.com/configwire/configwire/blob/main/configwire/docs/CONTRACT.md`.

## Install

```bash
dart pub add configwire
```

Or pin it in your `pubspec.yaml`:

```yaml
dependencies:
  configwire: ^0.3.2
```

If pinning to git:

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

## Reading values

All reads are synchronous over the in-memory view
(`{...defaults, ...serverValues}`). Every getter returns a nullable
type and takes a nullable fallback (default null) used on missing
keys or type mismatches:

```dart
cw.getBool('launch_flag'); // bool? — null when missing/mistyped
cw.getString('welcome', fallback: 'hi'); // String? — 'hi' on miss
cw.getInt('retries'); // int? — null when missing/mistyped
cw.getDouble('ratio'); // double? — null when missing; coerces int via toDouble()
cw.get<String>('welcome');
cw.getAll(); // copy of the live view
```

`get<T>` returns a defensive copy for maps and lists so callers
cannot mutate the live view. It coerces `int` to `double` when
`T` is `double`. Without a fallback it returns `null` for nullable
`T` and throws a cast error for non-nullable `T`. Server collections
decode as `List<dynamic>` / `Map<String, dynamic>`, so prefer the
helpers below over a direct `get<List<String>>`.

`getList<T>` and `getMap<T>` check every element eagerly and return
copies (or null):

```dart
cw.getList<String>('allowlist'); // List<String>? — null on any error
cw.getMap<int>('limits'); // Map<String, int>? — null on any error
```

On any error (missing key, mistyped value, mistyped element) the
fallback (null by default) is returned. Pass an explicit fallback to
substitute it.
`int` elements coerce to `double` when `T` is `double`.

## Variants

```dart
cw.getVariant('checkout'); // arm name, or null when none
cw.getVariants(); // all non-anonymous arms
```

The server only sends `variants` for flags in an experiment. An
empty-string arm means anonymous or default and is omitted from
both views: `getVariant` returns `null` for it and `getVariants`
skips it.

## Fetch lifecycle

`fetchAndActivate` returns `true` only on a 200 that replaced values.
304, throttled skips, and every failure mode return `false` and keep
the current view. Nothing here ever throws: network faults, timeouts,
and malformed 200 bodies keep stale cache (or defaults when cold).

```dart
final changed = await cw.fetchAndActivate(); // default: throttled
await cw.fetchAndActivate(force: true); // skip the throttle
```

Inspect the last attempt:

```dart
cw.lastFetchStatus; // FetchStatus none/success/cached/throttled/error
cw.fetchTime; // last 200/304 time, or null
cw.etag; // sent back verbatim as If-None-Match
cw.version; // latest activated release, 0 when none
cw.bucket; // stable ?b= slot (0-9999), or null when none minted yet
```

Throttle and timeout knobs (constructor params):

- `minimumFetchInterval` defaults to 12h. Only 200/304 responses arm
  it, so errors never block retries. `Duration.zero` disables it.
- `fetchTimeout` defaults to 60s per config GET.

Wire behavior: the stored etag goes out as `If-None-Match` and an
unchanged server answers `304` (status `cached`). A 200 replaces the
server layer wholesale over the defaults underlay, persists to the
store (including the minted `bucket` when present, first-write-wins),
then posts one analytics event (see below).

## Startup

```dart
await cw.ensureInitialized(); // cache load + forced fetch
```

`ensureInitialized` loads the cache into memory, then runs
`fetchAndActivate(force: true)` so the first server refresh is never
throttled. A loaded cache row arms the throttle. It never throws:
a missing or corrupt cache means defaults until the fetch resolves.

It is idempotent. Concurrent callers share the same in-flight init,
and later calls return at once without reloading or refetching.

## Targeting

Targeting attributes are sent as fetch query params so the server can
evaluate `rules` per fetch (`userId` → `?userHash=<sha256hex16>`,
`platform`,
`appVersion`, `locale`, `country`, `customAttrs` → `?attrs=` JSON).
Empty strings (after trim) / empty maps are omitted (anonymous).
`userId` and `country` are deprecated, accepted-but-ignored: the
server ignores `userHash` for bucketing (stamps
`X-CW-Deprecated-userHash`) and derives country from request headers,
so stop setting both — emission stays only for the compat window and
is removed in the next major release. Raw IDs never leave the device
(only the hash travels).

Bucketing rides the stable `?b=` slot, not identity: each evaluated
`200` may mint a `bucket` (`0`-`9999`) that the client persists
first-write-wins and sends as `?b=` on fetch and events, so arms
survive the server's daily anonID rotation. A `304` never clears it;
a `200` without the field (older server) keeps the prior value.
Inspect via `cw.bucket`; call `clearBucket()` on logout/user-switch
to drop it (memory + store) so the next fetch sends no `?b=`.

`customAttrs` keys must avoid PII/device identifiers (`email`,
`phone`, `ip`, `deviceId`, `advertisingId`, `adid`, `idfa`, `idfv`,
`androidId`, `serial`, `mac`, `imei`, case-insensitive) and stay
under 8192 bytes: violations answer `400`/`414`, which surface as a
fetch `error` status (`false`, never a throw).

Targeting is sticky: `setTargeting` replaces the stored targeting without
fetching, and the next `fetchAndActivate` (including realtime ticks)
uses it. Set it at construction, replace it wholesale, or merge one
field — then fetch:

```dart
cw.setTargeting(const Targeting(
  userId: 'user-7',
  platform: 'ios',
  appVersion: '1.2.3',
  locale: 'en-US',
  country: 'US',
  customAttrs: {'plan': 'pro'},
));
await cw.fetchAndActivate();

// Single-field update without rebuilding the whole targeting:
cw.updateTargeting(platform: 'android');
await cw.fetchAndActivate();

// Clear back to anonymous:
cw.setTargeting(const Targeting());
await cw.fetchAndActivate();
```

Pass `targeting:` at construction to fetch as a user on the first
call (including `ensureInitialized`):

```dart
final cw = ConfigWire(
  apiKey: 'YOUR_SDK_KEY',
  env: 'dev',
  baseUrl: 'http://127.0.0.1:8090',
  targeting: const Targeting(userId: 'user-7'),
);
```

## Realtime

```dart
await cw.connectRealtime(); // SSE + 15min poll fallback
cw.onUpdate.listen((values) => render(values));
await cw.disconnectRealtime(); // required; dispose() calls it too
```

Each valid `config_update` event and each poll tick runs a forced
fetch. `onUpdate` emits a snapshot only when that fetch is a 200
with new values. Plain `fetchAndActivate` never emits. `onUpdate` is
a broadcast stream, so late listeners miss earlier snapshots.

Lifecycle notes:

- Always pair `connectRealtime` with `disconnectRealtime` or
  `dispose`. A forgotten connection leaks a periodic timer that
  keeps the isolate alive, so tests never exit.
- `connectRealtime` never throws and reconnects cleanly when called
  twice. Bad keys and dead ports surface on status, not as throws.
- `pollInterval` defaults to 15min and runs always, stream up or
  down. Worst-case staleness is `pollInterval` plus one fetch.
  Values under 1s are clamped to 1s.
- Stream reconnects back off 1s, 2s, 4s, 8s, 16s, then capped at 30s.
  The server pings every 20s and closes streams at a 10min cap, and
  caps 5 concurrent streams per SDK key (`429`): all surface on
  status with a backoff retry, never as throws.
- Inspect via `cw.realtime?.status`, `cw.realtime?.lastError`, and
  `cw.realtime?.isConnected`. Raw per-event payloads
  (`{version, etag}`) are on `cw.realtime?.notifications`.

See `example/main.dart` for a runnable demo.

## Analytics

Every 200 fetch posts one best-effort env-wide fetch event (`flag: ""`)
with `sha256Hex16(userId)[:16]` as the identity hash (empty userId sends
`""`). The server ignores `userHash` for exposure identity
(accepted-but-ignored, deprecated) — raw IDs never
leave the device. When a stable bucket is persisted it rides along as
`?b=` on the events POST too, so exposures join the fetch arm. The POST is awaited with a 5s timeout but never fails the
fetch: network faults, timeouts, and non-2xx responses are swallowed.

`postEvent` takes an optional `flag` param (default `""`) and a
`kind` (`EventKind.fetch` / `EventKind.exposure`, serialized via
`kind.name`). The whole-release fetch in `fetchAndActivate` sends
`flag: ""`, the env-wide aggregate with the flag relation unset. Callers attributing a single flag
pass `flag: <key>` with `variant: variants[flag] ?? ""` and the fetched
release as `version`, so exposure events carry per-flag attribution and
stats can break counts down by flag. Both shapes share the same best-effort
5s never-fail-fetch behavior.

Stats filter by flag:

```text
GET /api/v1/admin/env/{env}/stats?since=7d&flag=<key>
```

With `flag=<key>` the server returns filtered counts plus `flagFound` and
`echo.flag`. Unknown keys return zeros with `200` and `flagFound: false`.
Omitting `flag` returns the unfiltered env-wide counts.

Opt-in manual helpers (per-flag attribution is manual; the auto fetch
event stays env-wide):

```dart
await cw.trackExposure('checkout'); // kind=exposure, variant from _variants
await cw.trackFetch(flag: 'checkout'); // kind=fetch, per-flag
await cw.trackFetch(); // kind=fetch, flag '' = env-wide aggregate
```

Both helpers return `Future<void>` and never throw: offline, timeout,
and malformed-URL faults are swallowed (empty/whitespace exposure flags
send nothing). The auto `fetchAndActivate` event is always the env-wide
`flag: ""` aggregate; call the helpers when one evaluated flag deserves
its own exposure/fetch attribution. Raw `postEvent` is available for
custom send paths; legacy `postFetchEvent` remains as a deprecated
forwarder to `postEvent`.

## Logging

Pass `verbose: true` for lifecycle, fetch, cache, realtime, and
analytics debug lines via `cwDebug`. Default is silent, and logging
never changes behavior or return values. The apiKey is sent only as
the `X-ConfigWire-Key` header, never printed, and redacted from error
strings.

## Cache

Persistence is bring-your-own via the `CacheStore` seam: implement
`load()`/`save()` over a pure-Dart backend of your choice (JSON file
via `dart:io`, as in the example above, or any other disk store with
no Flutter dependency).
`MemoryCacheStore` is session-only (no disk) and is the default when
`store:` is omitted. Flutter-only plugins such as shared_preferences
stay app-side: you may bridge one through `CacheStore` in your own
app code, but the package itself takes no Flutter dependency.

Cached rows are `CacheData` JSON:

```json
{"etag": "b41b62605c0df712", "version": 1,
 "fetchedAt": "2026-09-22T00:00:00.000Z",
 "values": {"launch_flag": true}, "variants": {"checkout": "control"},
 "bucket": 1234}
```

`bucket` is optional (absent on rows saved before the server minted
one): it round-trips only canonical `0`-`9999` ints.

Parsing is tolerant: files without `variants` load as `{}`,
non-string variant entries are skipped, a missing/misshapen/out-of-range
`bucket` parses as absent (null), and bad JSON or wrong shapes
load as `null` (caller falls back to defaults, never throws). Use
`cacheDataFromJsonString` for the same null-on-bad-input parse from a
raw string, or `CacheData.fromJson` on a decoded map. A loaded cache
row arms the `minimumFetchInterval` throttle.

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

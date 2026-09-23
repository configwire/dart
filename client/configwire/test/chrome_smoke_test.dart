// Core Web smoke: Hive IndexedDB round-trip on pure-Dart Web.
//
// Web-safe by construction: zero `dart:io` here AND transitively —
// plain `Hive.openBox` (no init on Web) for storage, MockClient only
// for HTTP (no `dart:io` HttpServer; it cannot compile to JS).
// Run with: dart test -p chrome test/chrome_smoke_test.dart
// (VM runs skip these tests; `dart test` stays hermetic.)
import 'dart:convert';

import 'package:configwire/configwire.dart';
import 'package:hive_ce/hive_ce.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:test/test.dart';

/// True on JS runtimes (ints and doubles share representation), false
/// on the VM. `package:flutter/foundation`'s `kIsWeb` is unavailable in
/// pure Dart, so this local equivalent gates the Web-only tests.
bool get _isWeb => identical(0, 0.0);

String _webFetch200() => jsonEncode({
      'version': 3,
      'etag': 'web-etag-1',
      'values': {
        'launch_flag': true,
        'welcome': 'hola-web',
        'limit': 7,
      },
      'variants': <String, String>{},
      'fetchAt': '2026-09-23T00:00:00.000Z',
    });

void main() {
  tearDownAll(() async {
    // Close only our own temp test boxes; never Hive.close() anything
    // else, and no deleteBoxFromDisk anywhere.
    for (final name in ['cw_core_smoke', 'cw_core_smoke_down']) {
      try {
        if (Hive.isBoxOpen(name)) await Hive.box(name).close();
      } catch (_) {
        // Best-effort test cleanup; never throws the suite.
      }
    }
  });

  test('IndexedDB box round-trip survives reload offline',
      skip: !_isWeb, () async {
    final live = MockClient((req) async {
      if (req.method == 'POST') {
        return http.Response('{"accepted":1}', 202);
      }
      return http.Response(_webFetch200(), 200);
    });
    addTearDown(live.close);

    // Cold start over a host-opened IndexedDB box (no init on Web).
    final box = await Hive.openBox('cw_core_smoke');
    final first = ConfigWire(
      apiKey: 'web-key',
      env: 'dev',
      baseUrl: 'http://localhost:8090',
      defaults: const {'launch_flag': false},
      minimumFetchInterval: Duration.zero,
      client: live,
      store: HiveCacheStore(box: box, env: 'dev'),
    );
    // NOTE: `completes` (future passed directly), NOT
    // `expectLater(() => ..., returnsNormally)`: on chrome the closure
    // form can return before the future finishes, while `completes`
    // awaits the real work. Same no-throw contract.
    await expectLater(first.ensureInitialized(), completes);
    expect(first.getBool('launch_flag'), isTrue);
    expect(first.getString('welcome'), 'hola-web');
    expect(first.getInt('limit'), 7);
    expect(first.etag, 'web-etag-1');
    expect(first.version, 3);
    expect(first.lastFetchStatus, FetchStatus.success);

    // Simulated reload: dispose the client WITHOUT closing the box,
    // then reopen through the same box with a dead transport.
    await expectLater(first.dispose(), completes);
    expect(Hive.isBoxOpen('cw_core_smoke'), isTrue);

    final dead = MockClient(
      (_) async => throw http.ClientException('offline-reload'),
    );
    addTearDown(dead.close);
    final second = ConfigWire(
      apiKey: 'web-key',
      env: 'dev',
      baseUrl: 'http://localhost:8090',
      defaults: const {'launch_flag': false},
      minimumFetchInterval: Duration.zero,
      client: dead,
      store: HiveCacheStore(box: box, env: 'dev'),
    );
    addTearDown(second.dispose);

    await expectLater(second.ensureInitialized(), completes);
    expect(second.getBool('launch_flag'), isTrue);
    expect(second.getString('welcome'), 'hola-web');
    expect(second.getInt('limit'), 7);
    expect(second.etag, 'web-etag-1');
    expect(second.version, 3);
    expect(second.lastFetchStatus, FetchStatus.error);
  });

  test('memory default fetches without Hive, no throw',
      skip: !_isWeb, () async {
    final live = MockClient((req) async {
      if (req.method == 'POST') {
        return http.Response('{"accepted":1}', 202);
      }
      return http.Response(_webFetch200(), 200);
    });
    addTearDown(live.close);
    final client = ConfigWire(
      apiKey: 'web-key',
      env: 'dev',
      baseUrl: 'http://localhost:8090',
      defaults: const {'launch_flag': false},
      minimumFetchInterval: Duration.zero,
      client: live,
    );
    addTearDown(client.dispose);

    await expectLater(client.ensureInitialized(), completes);
    expect(client.getBool('launch_flag'), isTrue);
    expect(client.lastFetchStatus, FetchStatus.success);
  });

  test('IndexedDB unavailable degrades to defaults with error, no throw',
      skip: !_isWeb, () async {
    // Failure QA: a closed box simulates blocked/unavailable IndexedDB
    // (every get/put throws inside the store, swallowed to null/noop).
    final box = await Hive.openBox('cw_core_smoke_down');
    await box.close();

    final dead = MockClient(
      (_) async => throw http.ClientException('indexeddb-blocked'),
    );
    addTearDown(dead.close);
    final facade = ConfigWire(
      apiKey: 'web-key',
      env: 'dev',
      baseUrl: 'http://localhost:8090',
      defaults: const {'launch_flag': false, 'welcome': 'fallback'},
      minimumFetchInterval: Duration.zero,
      client: dead,
      store: HiveCacheStore(box: box, env: 'dev'),
    );
    addTearDown(facade.dispose);

    await expectLater(facade.ensureInitialized(), completes);
    expect(facade.getBool('launch_flag'), isFalse);
    expect(facade.getString('welcome'), 'fallback');
    expect(facade.lastFetchStatus, FetchStatus.error);
  });
}

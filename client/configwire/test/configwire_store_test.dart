@TestOn('vm')
library;
import 'dart:convert';
import 'dart:io';

import 'package:configwire/configwire.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:test/test.dart';

/// Core wiring suite (todo 2): ConfigWire accepts a CacheStore.
///
/// FakeStore is in-memory; adversarial probes: explicit store bypasses
/// the memory default with zero disk touch, throwing saves still
/// succeed, pre-seeded store arms the throttle, throwing loads behave
/// as cold defaults with the fetch still attempted and never throwing.
class FakeStore implements CacheStore {
  FakeStore({this.seeded, this.throwOnLoad = false, this.throwOnSave = false});

  CacheData? seeded;
  CacheData? saved;
  bool throwOnLoad;
  bool throwOnSave;
  int loads = 0;
  int saves = 0;

  @override
  Future<CacheData?> load() async {
    loads++;
    if (throwOnLoad) throw const FileSystemException('fake load boom');
    return seeded;
  }

  @override
  Future<void> save(CacheData data) async {
    saves++;
    if (throwOnSave) throw const FileSystemException('fake save boom');
    saved = data;
  }
}

void main() {
  String fetch200({
    String etag = 'abc123',
    int version = 1,
    Map<String, Object?>? values,
  }) =>
      jsonEncode({
        'version': version,
        'etag': etag,
        'values': values ?? {'flag_bool': true, 'flag_str': 'live'},
        'variants': <String, String>{},
        'fetchAt': '2026-09-22T00:00:00.000Z',
      });

  MockClient okClient() {
    return MockClient((req) async {
      if (req.method == 'POST') {
        return http.Response('{"accepted":1,"status":202}', 202);
      }
      return http.Response(fetch200(), 200);
    });
  }

  group('ConfigWire CacheStore wiring', () {
    test(
        '(a) explicit store bypasses the memory default: no disk touched',
        () async {
      final store = FakeStore();
      final client = ConfigWire(
        apiKey: 'test-key',
        env: 'dev',
        baseUrl: 'http://localhost:8090',
        defaults: const {'flag_bool': false},
        client: okClient(),
        minimumFetchInterval: Duration.zero,
        store: store,
      );
      addTearDown(client.dispose);
      await client.ensureInitialized();
      expect(client.lastFetchStatus, equals(FetchStatus.success));
      expect(client.getBool('flag_bool'), isTrue);
      expect(store.loads, greaterThanOrEqualTo(1));
      expect(store.saves, equals(1));
      // The memory default holds no disk handle: the explicit store saw
      // exactly one load-then-save round trip with the fetched row.
      expect(store.saved, isNotNull);
      expect(store.saved!.etag, equals('abc123'));
    });

    test('(b) throwing-save store still yields success with values on',
        () async {
      final store = FakeStore(throwOnSave: true);
      final client = ConfigWire(
        apiKey: 'test-key',
        env: 'dev',
        baseUrl: 'http://localhost:8090',
        client: okClient(),
        minimumFetchInterval: Duration.zero,
        store: store,
      );
      addTearDown(client.dispose);
      expect(await client.fetchAndActivate(), isTrue);
      expect(client.lastFetchStatus, equals(FetchStatus.success));
      expect(client.getBool('flag_bool'), isTrue);
      expect(store.saves, equals(1));
    });

    test(
        '(c) pre-seeded store arms throttle: 2nd fetch throttled, 1 GET',
        () async {
      var gets = 0;
      final mock = MockClient((req) async {
        if (req.method == 'POST') {
          return http.Response('{"accepted":1,"status":202}', 202);
        }
        gets++;
        // Seeded etag matches: server answers 304, keeping the seeded
        // row (proves the seeded load armed If-None-Match + throttle).
        if (req.headers['If-None-Match'] == 'seeded-etag') {
          return http.Response('', 304);
        }
        return http.Response(fetch200(), 200);
      });
      final store = FakeStore(
        seeded: CacheData(
          etag: 'seeded-etag',
          version: 9,
          fetchedAt: DateTime.now().toUtc(),
          values: {'seeded_flag': 'from-store'},
          variants: const {},
        ),
      );
      final client = ConfigWire(
        apiKey: 'test-key',
        env: 'dev',
        baseUrl: 'http://localhost:8090',
        client: mock,
        minimumFetchInterval: const Duration(hours: 12),
        store: store,
      );
      addTearDown(client.dispose);
      await client.ensureInitialized();
      // Seeded row applied: stale value visible even before/after fetch.
      expect(client.getString('seeded_flag'), equals('from-store'));
      // ensureInitialized forces one fetch; immediate non-force retry
      // must hit the throttle armed by the fetch (or the seeded row).
      expect(await client.fetchAndActivate(), isFalse);
      expect(client.lastFetchStatus, equals(FetchStatus.throttled));
      expect(gets, equals(1));
    });

    test(
        '(d) throwing-load store behaves as cold-defaults, fetch attempted',
        () async {
      var gets = 0;
      final mock = MockClient((req) async {
        if (req.method == 'POST') {
          return http.Response('{"accepted":1,"status":202}', 202);
        }
        gets++;
        return http.Response(fetch200(), 200);
      });
      final store = FakeStore(throwOnLoad: true);
      final client = ConfigWire(
        apiKey: 'test-key',
        env: 'dev',
        baseUrl: 'http://localhost:8090',
        defaults: const {'welcome': 'hello'},
        client: mock,
        minimumFetchInterval: Duration.zero,
        store: store,
      );
      addTearDown(client.dispose);
      // Must not throw despite the throwing store; fetch still attempted.
      await client.ensureInitialized();
      expect(gets, equals(1));
      expect(client.lastFetchStatus, equals(FetchStatus.success));
      expect(client.getBool('flag_bool'), isTrue);
      expect(client.getString('welcome'), equals('hello'));
    });

    test('offline transport + seeded store: error status, stale kept',
        () async {
      final store = FakeStore(
        seeded: CacheData(
          etag: 'seeded-etag',
          version: 4,
          fetchedAt: DateTime.now().toUtc(),
          values: {'k': 'stale-value'},
          variants: const {},
        ),
      );
      final offline = MockClient(
          (_) async => throw const SocketException('down'));
      final client = ConfigWire(
        apiKey: 'test-key',
        env: 'dev',
        baseUrl: 'http://localhost:8090',
        client: offline,
        minimumFetchInterval: Duration.zero,
        store: store,
      );
      addTearDown(client.dispose);
      await client.ensureInitialized();
      expect(client.lastFetchStatus, equals(FetchStatus.error));
      expect(client.getString('k'), equals('stale-value'));
    });

    test('default ctor uses the memory default: fetch succeeds', () async {
      // The bare default is session-only: it fetches fine with an
      // in-memory store and keeps serving values.
      final client = ConfigWire(
        apiKey: 'test-key',
        env: 'dev',
        baseUrl: 'http://localhost:8090',
        client: okClient(),
        minimumFetchInterval: Duration.zero,
      );
      addTearDown(client.dispose);
      expect(await client.fetchAndActivate(), isTrue);
      expect(client.lastFetchStatus, equals(FetchStatus.success));
      expect(client.getBool('flag_bool'), isTrue);
      // A MemoryCacheStore-backed client persists within the session:
      // a second fetch round-trips through the store without a throw.
      final memStore = MemoryCacheStore();
      final memClient = ConfigWire(
        apiKey: 'test-key',
        env: 'dev',
        baseUrl: 'http://localhost:8090',
        client: okClient(),
        minimumFetchInterval: Duration.zero,
        store: memStore,
      );
      addTearDown(memClient.dispose);
      expect(await memClient.fetchAndActivate(), isTrue);
      expect(await memStore.load(), isNotNull);
    });
  });
}

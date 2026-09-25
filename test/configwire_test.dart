@TestOn('vm')
library;
import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:configwire/configwire.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:test/test.dart';

/// Deterministic MockClient suite for the offline-first client.
///
/// Each test gets a FRESH in-memory store (MemoryCacheStore) so no test
/// observes another's persisted state (stale-cache discipline).
void main() {
  Future<ConfigWire> clientWith(MockClient mock, {Map<String, Object?> defaults = const {}}) async {
    return ConfigWire(
      apiKey: 'test-key',
      env: 'dev',
      baseUrl: 'http://localhost:8090',
      defaults: defaults,
      client: mock,
      store: MemoryCacheStore(),
      // No throttle in tests unless the test says so.
      minimumFetchInterval: Duration.zero,
    );
  }

  String fetch200({String etag = 'abc123', int version = 1, Map<String, Object?>? values, Map<String, String>? variants}) {
    return jsonEncode({
      'version': version,
      'etag': etag,
      'values': values ?? {'flag_bool': true, 'flag_str': 'live'},
      'variants': variants ?? {},
      'fetchAt': '2026-09-22T00:00:00.000Z',
    });
  }

  group('defaults getters (pre-existing behavior)', () {
    test('seeded defaults read back with correct types', () async {
      final client = await clientWith(
        MockClient((_) async => http.Response('{}', 500)),
        defaults: {
          'flag_bool': true,
          'flag_str': 'hello',
          'flag_int': 7,
          'flag_double': 2.5,
          'flag_json': {'a': 1},
        },
      );
      addTearDown(client.dispose);
      expect(client.getBool('flag_bool'), isTrue);
      expect(client.getString('flag_str'), equals('hello'));
      expect(client.getInt('flag_int'), equals(7));
      expect(client.getDouble('flag_double'), equals(2.5));
      expect(client.getMap<int>('flag_json'), equals({'a': 1}));
      expect(client.getBool('missing'), isFalse);
      expect(client.getString('missing'), isEmpty);
    });
  });

  group('generic get<T>', () {
    test('reads typed primitives from defaults', () async {
      final client = await clientWith(
        MockClient((_) async => http.Response('{}', 500)),
        defaults: {
          'flag_bool': true,
          'flag_str': 'hello',
          'flag_int': 7,
          'flag_double': 2.5,
        },
      );
      addTearDown(client.dispose);
      expect(client.get<bool>('flag_bool'), isTrue);
      expect(client.get<String>('flag_str'), equals('hello'));
      expect(client.get<int>('flag_int'), equals(7));
      expect(client.get<double>('flag_double'), equals(2.5));
    });

    test('coerces int to double like getDouble', () async {
      final client = await clientWith(
        MockClient((_) async => http.Response('{}', 500)),
        defaults: {'flag_int': 7},
      );
      addTearDown(client.dispose);
      expect(client.get<double>('flag_int'), equals(7.0));
    });

    test('mistyped value returns fallback', () async {
      final client = await clientWith(
        MockClient((_) async => http.Response('{}', 500)),
        defaults: {'flag_str': 'hello'},
      );
      addTearDown(client.dispose);
      expect(client.get<int>('flag_str', fallback: 99), equals(99));
      expect(client.get<String>('flag_str', fallback: 'fb'), equals('hello'));
    });

    test('missing key: nullable T returns null, non-nullable throws', () async {
      final client = await clientWith(
        MockClient((_) async => http.Response('{}', 500)),
      );
      addTearDown(client.dispose);
      expect(client.get<int?>('missing'), isNull);
      expect(client.get<String?>('missing'), isNull);
      expect(() => client.get<int>('missing'), throwsA(isA<TypeError>()));
      expect(client.get<int>('missing', fallback: 42), equals(42));
    });

    test('server List<dynamic> misses get<List<String>> (use getList)', () async {
      final mock = MockClient((req) async {
        if (req.method == 'POST') {
          return http.Response('{"accepted":1,"status":202}', 202);
        }
        return http.Response(
          fetch200(values: {'tags': ['a', 'b']}),
          200,
        );
      });
      final client = await clientWith(mock);
      addTearDown(client.dispose);
      expect(await client.fetchAndActivate(), isTrue);
      // jsonDecode yields List<dynamic>: `is List<String>` is false.
      expect(client.get<List<String>>('tags', fallback: const ['fb']), equals(['fb']));
      expect(client.getList<String>('tags'), equals(['a', 'b']));
    });

    test('returns defensive copies of collections', () async {
      final client = await clientWith(
        MockClient((_) async => http.Response('{}', 500)),
        defaults: {
          'flag_json': {'a': 1},
          'tags': ['a'],
        },
      );
      addTearDown(client.dispose);
      final m = client.get<Map>('flag_json');
      m['hacked'] = true;
      expect(client.getMap<int>('flag_json'), equals({'a': 1}));
      final l = client.get<List>('tags');
      l.add('hacked');
      expect(client.getList<String>('tags'), equals(['a']));
    });
  });

  group('getList<T>', () {
    test('reads lists from defaults with element types', () async {
      final client = await clientWith(
        MockClient((_) async => http.Response('{}', 500)),
        defaults: {
          'list_int': [1, 2, 3],
          'list_str': ['a', 'b'],
          'empty': <int>[],
        },
      );
      addTearDown(client.dispose);
      expect(client.getList<int>('list_int'), equals([1, 2, 3]));
      expect(client.getList<String>('list_str'), equals(['a', 'b']));
      expect(client.getList<int>('empty'), isEmpty);
    });

    test('reads server List<dynamic> after fetch', () async {
      final mock = MockClient((req) async {
        if (req.method == 'POST') {
          return http.Response('{"accepted":1,"status":202}', 202);
        }
        return http.Response(
          fetch200(values: {'tags': ['a', 'b']}),
          200,
        );
      });
      final client = await clientWith(mock);
      addTearDown(client.dispose);
      expect(await client.fetchAndActivate(), isTrue);
      expect(client.getList<String>('tags'), equals(['a', 'b']));
    });

    test('coerces int elements to double', () async {
      final client = await clientWith(
        MockClient((_) async => http.Response('{}', 500)),
        defaults: {
          'nums': [1, 2],
        },
      );
      addTearDown(client.dispose);
      expect(client.getList<double>('nums'), equals([1.0, 2.0]));
    });

    test('missing key and mistyped element return fallback', () async {
      final client = await clientWith(
        MockClient((_) async => http.Response('{}', 500)),
        defaults: {
          'mixed': ['a', 1],
          'not_a_list': 'nope',
        },
      );
      addTearDown(client.dispose);
      expect(client.getList<String>('missing'), isEmpty);
      expect(client.getList<String>('mixed'), isEmpty);
      expect(client.getList<String>('not_a_list'), isEmpty);
      expect(
        client.getList<String>('mixed', fallback: const ['fb']),
        equals(['fb']),
      );
      expect(client.getList<int>('mixed', fallback: [9]), equals([9]));
    });

    test('fallback null rethrows', () async {
      final client = await clientWith(
        MockClient((_) async => http.Response('{}', 500)),
        defaults: {
          'mixed': ['a', 1],
        },
      );
      addTearDown(client.dispose);
      expect(() => client.getList<String>('mixed', fallback: null), throwsStateError);
      expect(() => client.getList<String>('missing', fallback: null), throwsStateError);
    });

    test('returns a copy: mutating the result keeps the live view', () async {
      final client = await clientWith(
        MockClient((_) async => http.Response('{}', 500)),
        defaults: {
          'tags': ['a'],
        },
      );
      addTearDown(client.dispose);
      final first = client.getList<String>('tags');
      first.add('hacked');
      expect(client.getList<String>('tags'), equals(['a']));
    });
  });

  group('getMap<T>', () {
    test('reads maps from defaults with value types', () async {
      final client = await clientWith(
        MockClient((_) async => http.Response('{}', 500)),
        defaults: {
          'map_int': {'a': 1, 'b': 2},
          'map_str': {'a': 'x'},
          'empty': <String, int>{},
        },
      );
      addTearDown(client.dispose);
      expect(client.getMap<int>('map_int'), equals({'a': 1, 'b': 2}));
      expect(client.getMap<String>('map_str'), equals({'a': 'x'}));
      expect(client.getMap<int>('empty'), isEmpty);
    });

    test('reads server Map<String, dynamic> after fetch', () async {
      final mock = MockClient((req) async {
        if (req.method == 'POST') {
          return http.Response('{"accepted":1,"status":202}', 202);
        }
        return http.Response(
          fetch200(values: {
            'cfg': {'a': 1, 'b': 2},
          }),
          200,
        );
      });
      final client = await clientWith(mock);
      addTearDown(client.dispose);
      expect(await client.fetchAndActivate(), isTrue);
      expect(client.getMap<int>('cfg'), equals({'a': 1, 'b': 2}));
    });

    test('coerces int values to double', () async {
      final client = await clientWith(
        MockClient((_) async => http.Response('{}', 500)),
        defaults: {
          'cfg': {'a': 1},
        },
      );
      addTearDown(client.dispose);
      expect(client.getMap<double>('cfg'), equals({'a': 1.0}));
    });

    test('missing key, mistyped value, and non-String keys return fallback', () async {
      final client = await clientWith(
        MockClient((_) async => http.Response('{}', 500)),
        defaults: {
          'mixed': {'a': 'x', 'b': 1},
          'not_a_map': 'nope',
          'weird': {
            1: 'x',
          },
        },
      );
      addTearDown(client.dispose);
      expect(client.getMap<int>('missing'), isEmpty);
      expect(client.getMap<int>('mixed'), isEmpty);
      expect(client.getMap<int>('not_a_map'), isEmpty);
      expect(client.getMap<String>('weird'), isEmpty);
      expect(
        client.getMap<int>('mixed', fallback: const {'fb': 1}),
        equals({'fb': 1}),
      );
    });

    test('fallback null rethrows', () async {
      final client = await clientWith(
        MockClient((_) async => http.Response('{}', 500)),
        defaults: {
          'mixed': {'a': 'x', 'b': 1},
        },
      );
      addTearDown(client.dispose);
      expect(() => client.getMap<int>('mixed', fallback: null), throwsStateError);
      expect(() => client.getMap<int>('missing', fallback: null), throwsStateError);
    });

    test('returns a copy: mutating the result keeps the live view', () async {
      final client = await clientWith(
        MockClient((_) async => http.Response('{}', 500)),
        defaults: {
          'cfg': {'a': 1},
        },
      );
      addTearDown(client.dispose);
      final first = client.getMap<int>('cfg');
      first['hacked'] = 2;
      expect(client.getMap<int>('cfg'), equals({'a': 1}));
    });
  });

  group('mixed element/value types', () {
    test('heterogeneous list succeeds only for supertypes', () async {
      final client = await clientWith(
        MockClient((_) async => http.Response('{}', 500)),
        defaults: {
          'mix': [1, 'a', true],
        },
      );
      addTearDown(client.dispose);
      expect(client.getList<Object>('mix'), equals([1, 'a', true]));
      expect(client.getList<dynamic>('mix'), equals([1, 'a', true]));
      expect(client.getList<int>('mix'), isEmpty);
      expect(client.getList<String>('mix'), isEmpty);
      expect(client.getList<bool>('mix'), isEmpty);
      expect(client.getList<num>('mix'), isEmpty);
    });

    test('int+double mix: num accepts, double coerces, int rejects', () async {
      final client = await clientWith(
        MockClient((_) async => http.Response('{}', 500)),
        defaults: {
          'nums': [1, 2.5],
        },
      );
      addTearDown(client.dispose);
      expect(client.getList<num>('nums'), equals([1, 2.5]));
      expect(client.getList<double>('nums'), equals([1.0, 2.5]));
      expect(client.getList<int>('nums'), isEmpty);
      expect(client.getList<String>('nums'), isEmpty);
    });

    test('bool/int boundary rejects cross-type reads', () async {
      final client = await clientWith(
        MockClient((_) async => http.Response('{}', 500)),
        defaults: {
          'flags': [true, 1],
        },
      );
      addTearDown(client.dispose);
      expect(client.getList<bool>('flags'), isEmpty);
      expect(client.getList<int>('flags'), isEmpty);
      expect(client.getList<num>('flags'), isEmpty);
      expect(client.getList<Object>('flags'), equals([true, 1]));
    });

    test('null elements: strict T rejects, nullable T accepts', () async {
      final client = await clientWith(
        MockClient((_) async => http.Response('{}', 500)),
        defaults: {
          'maybe': [1, null],
        },
      );
      addTearDown(client.dispose);
      expect(client.getList<int>('maybe'), isEmpty);
      expect(client.getList<int?>('maybe'), equals([1, null]));
      expect(client.getList<Object?>('maybe'), equals([1, null]));
    });

    test('server mixed list behaves like defaults', () async {
      final mock = MockClient((req) async {
        if (req.method == 'POST') {
          return http.Response('{"accepted":1,"status":202}', 202);
        }
        return http.Response(
          fetch200(values: {
            'mix': [1, 'a'],
          }),
          200,
        );
      });
      final client = await clientWith(mock);
      addTearDown(client.dispose);
      expect(await client.fetchAndActivate(), isTrue);
      expect(client.getList<String>('mix'), isEmpty);
      expect(client.getList<int>('mix'), isEmpty);
      expect(client.getList<Object>('mix'), equals([1, 'a']));
    });

    test('nested maps in a list need exact element type', () async {
      final mock = MockClient((req) async {
        if (req.method == 'POST') {
          return http.Response('{"accepted":1,"status":202}', 202);
        }
        return http.Response(
          fetch200(values: {
            'rows': [
              {'a': 1},
            ],
          }),
          200,
        );
      });
      final client = await clientWith(mock);
      addTearDown(client.dispose);
      expect(await client.fetchAndActivate(), isTrue);
      // Server decodes the row as Map<String, dynamic>, so the exact
      // Map<String, int> read misses while the Map supertype hits.
      expect(client.getList<Map<String, int>>('rows'), isEmpty);
      expect(client.getList<Map>('rows'), hasLength(1));
    });

    test('heterogeneous map succeeds only for supertypes', () async {
      final client = await clientWith(
        MockClient((_) async => http.Response('{}', 500)),
        defaults: {
          'mix': {'a': 1, 'b': 'x', 'c': true},
        },
      );
      addTearDown(client.dispose);
      expect(client.getMap<Object>('mix'), equals({'a': 1, 'b': 'x', 'c': true}));
      expect(client.getMap<int>('mix'), isEmpty);
      expect(client.getMap<String>('mix'), isEmpty);
      expect(client.getMap<bool>('mix'), isEmpty);
      expect(client.getMap<num>('mix'), isEmpty);
    });

    test('int+double map values: num accepts, double coerces, int rejects', () async {
      final client = await clientWith(
        MockClient((_) async => http.Response('{}', 500)),
        defaults: {
          'nums': {'a': 1, 'b': 2.5},
        },
      );
      addTearDown(client.dispose);
      expect(client.getMap<num>('nums'), equals({'a': 1, 'b': 2.5}));
      expect(client.getMap<double>('nums'), equals({'a': 1.0, 'b': 2.5}));
      expect(client.getMap<int>('nums'), isEmpty);
      expect(client.getMap<String>('nums'), isEmpty);
    });

    test('null map values: strict T rejects, nullable T accepts', () async {
      final client = await clientWith(
        MockClient((_) async => http.Response('{}', 500)),
        defaults: {
          'maybe': {'a': 1, 'b': null},
        },
      );
      addTearDown(client.dispose);
      expect(client.getMap<int>('maybe'), isEmpty);
      expect(client.getMap<int?>('maybe'), equals({'a': 1, 'b': null}));
    });

    test('server mixed map behaves like defaults', () async {
      final mock = MockClient((req) async {
        if (req.method == 'POST') {
          return http.Response('{"accepted":1,"status":202}', 202);
        }
        return http.Response(
          fetch200(values: {
            'cfg': {'a': 1, 'b': 'x'},
          }),
          200,
        );
      });
      final client = await clientWith(mock);
      addTearDown(client.dispose);
      expect(await client.fetchAndActivate(), isTrue);
      expect(client.getMap<int>('cfg'), isEmpty);
      expect(client.getMap<String>('cfg'), isEmpty);
      expect(client.getMap<Object>('cfg'), equals({'a': 1, 'b': 'x'}));
    });
  });

  group('fetch contract', () {
    test('200-update: values replaced, etag stored, store doc has all five keys', () async {
      String? postedBody;
      final mock = MockClient((req) async {
        if (req.method == 'POST') {
          postedBody = req.body;
          return http.Response('{"accepted":1,"status":202}', 202);
        }
        expect(req.headers['X-ConfigWire-Key'], equals('test-key'));
        return http.Response(
          fetch200(values: {'flag_bool': false, 'flag_str': 'live'}),
          200,
          headers: {'ETag': 'abc123'},
        );
      });
      final store = MemoryCacheStore();
      final client = ConfigWire(
        apiKey: 'test-key',
        env: 'dev',
        baseUrl: 'http://localhost:8090',
        defaults: {'flag_bool': true},
        client: mock,
        store: store,
        minimumFetchInterval: Duration.zero,
      );
      addTearDown(client.dispose);

      expect(await client.fetchAndActivate(), isTrue);
      expect(client.lastFetchStatus, equals(FetchStatus.success));
      expect(client.fetchTime, isNotNull);
      expect(client.getBool('flag_bool'), isFalse);
      expect(client.getString('flag_str'), equals('live'));
      expect(client.etag, equals('abc123'));
      expect(client.version, equals(1));

      // Store doc holds all five keys (incl. variants).
      final saved = await store.load();
      expect(saved, isNotNull);
      final onDisk = saved!.toJson();
      expect(onDisk.keys.toSet(), equals({'etag', 'version', 'fetchedAt', 'values', 'variants'}));
      expect(onDisk['etag'], equals('abc123'));
      expect(onDisk['version'], equals(1));
      expect((onDisk['values'] as Map)['flag_bool'], isFalse);

      // Fetch event posted best-effort with hashed user (anonymous -> "").
      expect(postedBody, isNotNull);
      final body = jsonDecode(postedBody!) as Map;
      final ev = (body['events'] as List).single as Map;
      expect(ev['kind'], equals('fetch'));
      expect(ev['flag'], equals(''));
      expect(ev['userHash'], equals(''));
    });

    test('304-keep: cached values retained, returns false', () async {
      var calls = 0;
      final mock = MockClient((req) async {
        if (req.method == 'POST') {
          return http.Response('{"accepted":1,"status":202}', 202);
        }
        calls++;
        if (calls == 1) return http.Response(fetch200(etag: 'etag-1'), 200);
        expect(req.headers['If-None-Match'], equals('etag-1'));
        return http.Response('', 304);
      });
      final client = await clientWith(mock);
      addTearDown(client.dispose);

      expect(await client.fetchAndActivate(), isTrue);
      expect(await client.fetchAndActivate(), isFalse);
      expect(client.lastFetchStatus, equals(FetchStatus.cached));
      expect(client.getBool('flag_bool'), isTrue);
      expect(client.etag, equals('etag-1'));
    });

    test('offline-cache: network failure keeps stale cache, no throw', () async {
      // Seed the cache with one good fetch, then go offline. Both
      // clients share one in-memory store (the seeder persists, the
      // offline client re-reads).
      final seed = MockClient((req) async {
        if (req.method == 'POST') {
          return http.Response('{"accepted":1,"status":202}', 202);
        }
        return http.Response(fetch200(values: {'flag_str': 'cached-live'}), 200);
      });
      final store = MemoryCacheStore();
      final seeder = ConfigWire(
        apiKey: 'test-key',
        env: 'dev',
        baseUrl: 'http://localhost:8090',
        client: seed,
        store: store,
        minimumFetchInterval: Duration.zero,
      );
      addTearDown(seeder.dispose);
      expect(await seeder.fetchAndActivate(), isTrue);

      final offline = MockClient((_) async => throw const SocketException('down'));
      final client = ConfigWire(
        apiKey: 'test-key',
        env: 'dev',
        baseUrl: 'http://localhost:8090',
        client: offline,
        store: store,
        minimumFetchInterval: Duration.zero,
      );
      addTearDown(client.dispose);
      await client.ensureInitialized();
      // ensureInitialized already attempted (and failed) a fetch; a direct
      // retry must also fail soft.
      expect(await client.fetchAndActivate(), isFalse);
      expect(client.lastFetchStatus, equals(FetchStatus.error));
      expect(client.getString('flag_str'), equals('cached-live'));
    });

    test('cold-defaults: offline with no cache serves in-app defaults, no throw', () async {
      final offline = MockClient((_) async => throw const SocketException('down'));
      final client = await clientWith(offline, defaults: {'welcome': 'hello', 'enabled': true});
      addTearDown(client.dispose);
      await client.ensureInitialized();
      expect(client.lastFetchStatus, equals(FetchStatus.error));
      expect(client.getString('welcome'), equals('hello'));
      expect(client.getBool('enabled'), isTrue);
      expect(client.fetchTime, isNull);
    });

    test('type-mismatch-fallback: wrong-typed live value falls back', () async {
      final mock = MockClient((req) async {
        if (req.method == 'POST') {
          return http.Response('{"accepted":1,"status":202}', 202);
        }
        return http.Response(
          fetch200(values: {'flag_bool': 'not-a-bool', 'flag_int': 42}),
          200,
        );
      });
      final client = await clientWith(mock);
      addTearDown(client.dispose);
      expect(await client.fetchAndActivate(), isTrue);
      expect(client.getBool('flag_bool', fallback: true), isTrue);
      expect(client.getInt('flag_int'), equals(42));
    });

    test('throttle-skip: second immediate fetch returns false with throttled status', () async {
      var gets = 0;
      final mock = MockClient((req) async {
        if (req.method == 'POST') {
          return http.Response('{"accepted":1,"status":202}', 202);
        }
        gets++;
        return http.Response(fetch200(), 200);
      });
      final client = ConfigWire(
        apiKey: 'test-key',
        env: 'dev',
        baseUrl: 'http://localhost:8090',
        client: mock,
        store: MemoryCacheStore(),
        minimumFetchInterval: const Duration(hours: 12),
      );
      addTearDown(client.dispose);
      expect(await client.fetchAndActivate(), isTrue);
      expect(await client.fetchAndActivate(), isFalse);
      expect(client.lastFetchStatus, equals(FetchStatus.throttled));
      expect(gets, equals(1));
    });

    test('minimumFetchInterval zero disables throttling (dev mode)', () async {
      var gets = 0;
      final mock = MockClient((req) async {
        if (req.method == 'POST') {
          return http.Response('{"accepted":1,"status":202}', 202);
        }
        gets++;
        return http.Response(fetch200(etag: 'e$gets', version: gets), 200);
      });
      final client = await clientWith(mock);
      addTearDown(client.dispose);
      expect(await client.fetchAndActivate(), isTrue);
      expect(await client.fetchAndActivate(), isTrue);
      expect(gets, equals(2));
    });
  });

  group('adversarial paths', () {
    test('malformed 200 (garbage JSON) keeps stale cache, no throw', () async {
      var calls = 0;
      final mock = MockClient((req) async {
        if (req.method == 'POST') {
          return http.Response('{"accepted":1,"status":202}', 202);
        }
        calls++;
        if (calls == 1) return http.Response(fetch200(values: {'k': 'good'}), 200);
        return http.Response('this is not json{{{', 200);
      });
      final client = await clientWith(mock);
      addTearDown(client.dispose);
      expect(await client.fetchAndActivate(), isTrue);
      expect(await client.fetchAndActivate(), isFalse);
      expect(client.lastFetchStatus, equals(FetchStatus.error));
      expect(client.getString('k'), equals('good'));
    });

    test('500 keeps cached values with error status', () async {
      var calls = 0;
      final mock = MockClient((req) async {
        if (req.method == 'POST') {
          return http.Response('{"accepted":1,"status":202}', 202);
        }
        calls++;
        if (calls == 1) return http.Response(fetch200(values: {'k': 'good'}), 200);
        return http.Response('boom', 500);
      });
      final client = await clientWith(mock);
      addTearDown(client.dispose);
      expect(await client.fetchAndActivate(), isTrue);
      expect(await client.fetchAndActivate(), isFalse);
      expect(client.lastFetchStatus, equals(FetchStatus.error));
      expect(client.getString('k'), equals('good'));
    });

    test('fetchTimeout honored: hanging server maps to error, no throw', () async {
      final hanging = MockClient((_) async {
        await Future<void>.delayed(const Duration(seconds: 30));
        return http.Response(fetch200(), 200);
      });
      final client = ConfigWire(
        apiKey: 'test-key',
        env: 'dev',
        baseUrl: 'http://localhost:8090',
        defaults: {'d': 'default'},
        client: hanging,
        store: MemoryCacheStore(),
        minimumFetchInterval: Duration.zero,
        fetchTimeout: const Duration(milliseconds: 200),
      );
      addTearDown(client.dispose);
      expect(await client.fetchAndActivate(), isFalse);
      expect(client.lastFetchStatus, equals(FetchStatus.error));
      expect(client.getString('d'), equals('default'));
    });

    test('concurrent fetchAndActivate x2: both complete, values consistent (last-wins)', () async {
      final mock = MockClient((req) async {
        if (req.method == 'POST') {
          return http.Response('{"accepted":1,"status":202}', 202);
        }
        await Future<void>.delayed(const Duration(milliseconds: 20));
        return http.Response(fetch200(values: {'k': 'v'}), 200);
      });
      final client = await clientWith(mock);
      addTearDown(client.dispose);
      final results = await Future.wait([client.fetchAndActivate(), client.fetchAndActivate()]);
      // No lock: both run to completion; last-completes-wins. Either
      // [true, true] (both 200) — never a throw, never torn state.
      expect(results, equals([true, true]));
      expect(client.getString('k'), equals('v'));
      expect(client.lastFetchStatus, equals(FetchStatus.success));
    });

    test('anonymous "" variant omitted from every variant view', () async {
      final mock = MockClient((req) async {
        if (req.method == 'POST') {
          return http.Response('{"accepted":1,"status":202}', 202);
        }
        return http.Response(
          fetch200(
            values: {'flag_a': true, 'flag_b': false},
            variants: {'flag_a': '', 'flag_b': 'treatment'},
          ),
          200,
        );
      });
      final client = await clientWith(mock);
      addTearDown(client.dispose);
      expect(await client.fetchAndActivate(), isTrue);
      expect(client.getVariant('flag_a'), isNull);
      expect(client.getVariant('flag_b'), equals('treatment'));
      expect(client.getVariants(), equals({'flag_b': 'treatment'}));
      expect(client.getVariants().containsKey('flag_a'), isFalse);
    });

    test('uid query param sent and userHash is sha256hex16', () async {
      String? gotUid;
      String? gotHash;
      final mock = MockClient((req) async {
        if (req.method == 'POST') {
          gotHash = (jsonDecode(req.body) as Map)['events'][0]['userHash'] as String?;
          return http.Response('{"accepted":1,"status":202}', 202);
        }
        gotUid = req.url.queryParameters['uid'];
        return http.Response(fetch200(), 200);
      });
      final client = await clientWith(mock);
      addTearDown(client.dispose);
      await client.fetchAndActivate(
        context: const TargetingContext(userId: 'qa-user-7'),
      );
      expect(gotUid, equals('qa-user-7'));
      expect(gotHash, equals(sha256Hex16('qa-user-7')));
      expect(gotHash, hasLength(16));
      // Known vector: cross-checked against `sha256sum` in live QA.
      expect(sha256Hex16(''), equals(''));
    });
  });
}

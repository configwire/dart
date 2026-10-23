@TestOn('vm')
library;

import 'dart:convert';

import 'package:configwire/configwire.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:test/test.dart';

// T8 backward-compat regression guards (dart client). Additive only —
// no existing test assertion is touched here.
//
// Already pinned elsewhere (referenced, not duplicated):
//   - old JSON without bucket loads intact: cache_store_test.dart
//     'old JSON without bucket loads with all fields intact'
//   - bucket round-trip / missing-absent / wrong-shape-absent:
//     cache_store_test.dart bucket cases (T4)
//   - 200 persists bucket / ?b= sent-absent-cold / etag-change survival /
//     304-keeps / old-server-keeps / first-write-wins / verbatim etag:
//     configwire_test.dart bucket cases (T5)
//   - no-?b= event body byte-identical: events_bucket_test.dart
//     'no bucket: no ?b= query, behavior byte-identical'

void main() {
  String fetch200Bucket({String etag = 'e1', int? bucket}) {
    final m = <String, Object?>{
      'version': 1,
      'etag': etag,
      'values': {'k': 'v'},
      'variants': <String, String>{},
      'fetchAt': '2026-09-22T00:00:00.000Z',
    };
    if (bucket != null) m['bucket'] = bucket;
    return jsonEncode(m);
  }

  group('compat: targeting emission undisturbed by bucket', () {
    test('country + userHash flow verbatim alongside ?b=', () async {
      final seen = <Map<String, String>>[];
      final mock = MockClient((req) async {
        if (req.method == 'POST') {
          return http.Response('{"accepted":1,"status":202}', 202);
        }
        seen.add(Map<String, String>.from(req.url.queryParameters));
        return http.Response(fetch200Bucket(bucket: 42), 200);
      });
      final client = ConfigWire(
        apiKey: 'test-key',
        env: 'dev',
        baseUrl: 'http://localhost:8090',
        client: mock,
        store: MemoryCacheStore(),
        minimumFetchInterval: Duration.zero,
        targeting: const Targeting(
          userId: 'user-7',
          platform: 'ios',
          country: 'US',
        ),
      );
      addTearDown(client.dispose);
      expect(await client.fetchAndActivate(), isTrue);
      expect(await client.fetchAndActivate(), isTrue);
      final second = seen.last;
      expect(second['b'], equals('42'));
      expect(second['userHash'], equals(sha256Hex16('user-7')));
      expect(second['country'], equals('US'));
      expect(second['platform'], equals('ios'));
      expect(second.containsKey('uid'), isFalse);
    });

    test('Targeting never emits ?b= itself (client wire owns it)',
        () async {
      const q = Targeting(userId: 'user-7', country: 'US');
      expect(q.toQueryParameters().containsKey('b'), isFalse);
      expect(q.toQueryParameters()['country'], equals('US'));
    });
  });

  group('compat: tolerant parse with future keys beside bucket', () {
    test('unknown future keys load with bucket intact, never throw',
        () async {
      final raw = jsonEncode({
        'etag': 'e9',
        'version': 2,
        'fetchedAt': '2026-09-22T00:00:00.000Z',
        'values': {'k': true},
        'variants': {'checkout': 'control'},
        'bucket': 7,
        'someFutureKey': {'nested': [1, 2, 3]},
      });
      final data = cacheDataFromJsonString(raw);
      expect(data, isNotNull);
      final row = data;
      expect(row?.bucket, equals(7));
      expect(row?.etag, equals('e9'));
      expect(row?.variants['checkout'], equals('control'));
    });
  });
}

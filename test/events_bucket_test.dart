import 'dart:convert';

import 'package:configwire/configwire.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:test/test.dart';

// T7 events/ingest bucket join (client arm): postEvent sends the
// persisted bucket as the ?b= query value on the events POST — the
// same ?b= the fetch used — so the server deriveIdentity honors it
// and fetch arms join exposure attribution. Body userHash stays
// accepted-but-ignored (byte-identical sha256Hex16 emission).

void main() {
  group('postEvent bucket ?b= join', () {
    test('bucket persisted: ?b= sent, body userHash unchanged', () async {
      Uri? postedUri;
      String? postedBody;
      final mock = MockClient((req) async {
        postedUri = req.url;
        postedBody = req.body;
        return http.Response('{"accepted":1,"status":202}', 202);
      });
      await postEvent(
        client: mock,
        baseUrl: 'http://localhost:8090',
        env: 'dev',
        apiKey: 'test-key',
        userId: 'user-7',
        variants: {'exp_bool': 'treatment'},
        flag: 'exp_bool',
        version: 3,
        bucket: 42,
      );
      expect(postedUri, isNotNull);
      expect(postedUri!.queryParameters['b'], equals('42'));
      final body = jsonDecode(postedBody!) as Map;
      final ev = (body['events'] as List).single as Map;
      // Body identity byte-identical: still sha256Hex16(userId).
      expect(ev['userHash'], equals(sha256Hex16('user-7')));
      expect(ev['kind'], equals('fetch'));
      expect(ev['flag'], equals('exp_bool'));
      expect(ev['variant'], equals('treatment'));
    });

    test('bucket edges 0 and 9999 send canonical ?b=', () async {
      for (final b in [0, 9999]) {
        Uri? postedUri;
        final mock = MockClient((req) async {
          postedUri = req.url;
          return http.Response('{"accepted":1,"status":202}', 202);
        });
        await postEvent(
          client: mock,
          baseUrl: 'http://localhost:8090',
          env: 'dev',
          apiKey: 'test-key',
          userId: 'user-7',
          variants: const {},
          bucket: b,
        );
        expect(
          postedUri!.queryParameters['b'],
          equals('$b'),
          reason: 'bucket=$b',
        );
      }
    });

    test('no bucket: no ?b= query, behavior byte-identical', () async {
      Uri? postedUri;
      String? postedBody;
      final mock = MockClient((req) async {
        postedUri = req.url;
        postedBody = req.body;
        return http.Response('{"accepted":1,"status":202}', 202);
      });
      await postEvent(
        client: mock,
        baseUrl: 'http://localhost:8090',
        env: 'dev',
        apiKey: 'test-key',
        userId: 'user-7',
        variants: {'exp_bool': 'treatment'},
        flag: 'exp_bool',
        version: 3,
      );
      expect(postedUri, isNotNull);
      expect(postedUri!.queryParameters.containsKey('b'), isFalse);
      final body = jsonDecode(postedBody!) as Map;
      final ev = (body['events'] as List).single as Map;
      expect(ev['userHash'], equals(sha256Hex16('user-7')));
    });
  });
}

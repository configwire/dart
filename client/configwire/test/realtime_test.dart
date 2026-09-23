@TestOn('vm')
library;
import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:configwire/configwire.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:test/test.dart';

/// Realtime (todo 14) suite: `RealtimeUpdater` SSE + poll fallback.
///
/// The stream side is a LOCAL `dart:io` HttpServer test double (no
/// dependency on the Go server); the fetch side is a MockClient-backed
/// ConfigWire (the updater's stream client is always real, so the two
/// sides never interfere). Every test uses a FRESH in-memory store and
/// disposes its client (no dangling Timers — a leak would hang the
/// suite). All live waits are bounded (`.timeout`), so the suite
/// finishes in seconds, never near the 120s budget.
void main() {
  String fetch200({
    String etag = 'e2',
    int version = 2,
    Map<String, Object?>? values,
  }) {
    return jsonEncode({
      'version': version,
      'etag': etag,
      'values': values ?? {'live_flag': true},
      'variants': {},
      'fetchAt': '2026-09-22T00:00:00.000Z',
    });
  }

  /// Mock fetch client: GET config -> canned 200, POST events -> 202.
  /// [gets] counts config GETs (proves how many refreshes fired).
  MockClient fetchMock({required String body, required List<int> gets}) {
    return MockClient((req) async {
      if (req.method == 'POST') {
        return http.Response('{"accepted":1,"status":202}', 202);
      }
      gets.add(1);
      return http.Response(body, 200);
    });
  }

  Future<ConfigWire> makeClient(MockClient mock, String baseUrl) async {
    final client = ConfigWire(
      apiKey: 'test-key',
      env: 'dev',
      baseUrl: baseUrl,
      defaults: {'live_flag': false},
      client: mock,
      store: MemoryCacheStore(),
      minimumFetchInterval: Duration.zero,
    );
    addTearDown(client.dispose);
    return client;
  }

  /// Starts a local SSE double. [onStream] handles each
  /// `/api/v1/env/dev/stream` GET; everything else 404s.
  ///
  /// NOTE: dart:io `HttpResponse` buffers output by default and
  /// `write`+`flush` does NOT push chunked body bytes until close, so
  /// every SSE handler MUST set `resp.bufferOutput = false` before
  /// writing (verified empirically: without it the client sees headers
  /// but zero body chunks). The real Go server flushes correctly;
  /// this is purely a test-double gotcha.
  Future<HttpServer> startServer(
    Future<void> Function(HttpRequest req) onStream,
  ) async {
    final server = await HttpServer.bind(InternetAddress.loopbackIPv4, 0);
    addTearDown(() async {
      try {
        await server.close(force: true);
      } catch (_) {
        // Already closed; ignore.
      }
    });
    server.listen((req) async {
      try {
        if (req.uri.path == '/api/v1/env/dev/stream') {
          await onStream(req);
        } else {
          req.response.statusCode = 404;
          await req.response.close();
        }
      } catch (_) {
        // Client went away mid-test; ignore.
      }
    });
    return server;
  }

  /// Holds the SSE response open until the test tears the server down.
  Future<void> holdOpen(HttpResponse resp) async {
    try {
      await Future<void>.delayed(const Duration(seconds: 30));
      await resp.close();
    } catch (_) {
      // Server force-closed underneath; ignore.
    }
  }

  group('realtime SSE + poll fallback', () {
    test(
      'canned SSE event (chunk-split) triggers fetch; onUpdate emits values',
      timeout: const Timeout(Duration(seconds: 30)),
      () async {
        String? seenKey;
        final server = await startServer((req) async {
          seenKey = req.headers.value('x-configwire-key');
          final resp = req.response;
          resp.statusCode = 200;
          resp.bufferOutput = false;
          resp.headers.contentType = ContentType.parse('text/event-stream');
          resp.headers.set('Cache-Control', 'no-store');
          // Deliberate chunk split mid-`data:` line: the parser must
          // buffer until the blank line before decoding.
          resp.write('event: config_update\ndata: {"vers');
          await resp.flush();
          await Future<void>.delayed(const Duration(milliseconds: 50));
          resp.write('ion":2,"etag":"e2","env":"dev"}\n\n');
          await resp.flush();
          await holdOpen(resp);
        });

        final gets = <int>[];
        final client = await makeClient(
          fetchMock(body: fetch200(), gets: gets),
          'http://127.0.0.1:${server.port}',
        );
        await client.connectRealtime(
          pollInterval: const Duration(minutes: 10),
        );

        final snap = await client.onUpdate.first.timeout(
          const Duration(seconds: 10),
        );
        expect(snap['live_flag'], isTrue);
        expect(client.version, equals(2));
        expect(client.etag, equals('e2'));
        expect(client.getBool('live_flag'), isTrue);
        // Stream carried the SDK key header; exactly one refresh fired
        // (poll is 10min away, so the event caused it).
        expect(seenKey, equals('test-key'));
        expect(gets.length, equals(1));
      },
    );

    test(
      'stream killed (dead port) -> poll tick still triggers fetch',
      timeout: const Timeout(Duration(seconds: 30)),
      () async {
        // Reserve then release a port so nothing listens on it.
        final probe = await ServerSocket.bind(InternetAddress.loopbackIPv4, 0);
        final deadPort = probe.port;
        await probe.close();

        final gets = <int>[];
        final client = await makeClient(
          fetchMock(
            body: fetch200(values: {'live_flag': true}),
            gets: gets,
          ),
          'http://127.0.0.1:$deadPort',
        );
        // Must not throw despite the refused stream connection.
        await client.connectRealtime(
          pollInterval: const Duration(milliseconds: 100),
        );

        final snap = await client.onUpdate.first.timeout(
          const Duration(seconds: 10),
        );
        expect(snap['live_flag'], isTrue);
        expect(client.getBool('live_flag'), isTrue);
        expect(gets.isNotEmpty, isTrue);
        // The stream side surfaced an error status (connection refused)
        // instead of throwing out of connect.
        final deadline =
            DateTime.now().add(const Duration(seconds: 10));
        while (client.realtime?.lastError == null &&
            DateTime.now().isBefore(deadline)) {
          await Future<void>.delayed(const Duration(milliseconds: 50));
        }
        expect(client.realtime?.lastError, isNotNull);
      },
    );

    test(
      'malformed SSE chunks ignored, no throw; valid event still works',
      timeout: const Timeout(Duration(seconds: 30)),
      () async {
        final server = await startServer((req) async {
          final resp = req.response;
          resp.statusCode = 200;
          resp.bufferOutput = false;
          resp.headers.contentType = ContentType.parse('text/event-stream');
          resp.write(': keepalive comment\n\n');
          resp.write('garbage line without colon\n\n');
          resp.write('event: config_update\ndata: not-json{{{\n\n');
          resp.write('event: other\ndata: {"x":1}\n\n');
          resp.write('event: config_update\ndata: \n\n');
          await resp.flush();
          await Future<void>.delayed(const Duration(milliseconds: 100));
          resp.write(
            'event: config_update\ndata: {"version":2,"etag":"e2"}\n\n',
          );
          await resp.flush();
          await holdOpen(resp);
        });

        final gets = <int>[];
        final client = await makeClient(
          fetchMock(body: fetch200(), gets: gets),
          'http://127.0.0.1:${server.port}',
        );
        final seen = <Map<String, Object?>>[];
        final sub = client.onUpdate.listen(seen.add);
        addTearDown(sub.cancel);
        await client.connectRealtime(
          pollInterval: const Duration(minutes: 10),
        );

        final snap = await client.onUpdate.first.timeout(
          const Duration(seconds: 10),
        );
        expect(snap['live_flag'], isTrue);
        // Only the ONE valid event triggered a refresh; garbage,
        // wrong-event, and empty-data frames were ignored.
        await Future<void>.delayed(const Duration(milliseconds: 400));
        expect(gets.length, equals(1));
        expect(seen.length, equals(1));
      },
    );

    test(
      '401 stream (bad key) surfaces error status without throwing',
      timeout: const Timeout(Duration(seconds: 30)),
      () async {
        final server = await startServer((req) async {
          req.response.statusCode = 401;
          req.response.write('Missing or invalid SDK key.');
          await req.response.close();
        });

        final gets = <int>[];
        final client = await makeClient(
          fetchMock(body: fetch200(), gets: gets),
          'http://127.0.0.1:${server.port}',
        );
        // Must not throw despite the 401 stream.
        await client.connectRealtime(
          pollInterval: const Duration(minutes: 10),
        );

        final deadline =
            DateTime.now().add(const Duration(seconds: 10));
        while (client.realtime?.status != RealtimeStatus.error &&
            DateTime.now().isBefore(deadline)) {
          await Future<void>.delayed(const Duration(milliseconds: 50));
        }
        expect(client.realtime?.status, equals(RealtimeStatus.error));
        expect(client.realtime?.lastError, contains('401'));
        expect(client.realtime?.isConnected, isFalse);
        // No refresh fired: only the stream failed, poll is 10min out.
        expect(gets, isEmpty);
        await client.disconnectRealtime();
        expect(client.realtime, isNull);
      },
    );
  });
}

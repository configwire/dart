import 'dart:async';
import 'dart:convert';

import 'package:crypto/crypto.dart' as crypto;
import 'package:http/http.dart' as http;

import 'cw_logger.dart';

/// Best-effort fetch-event POST helper.
///
/// After a successful 200 fetch the SDK reports one analytics event:
///
/// ```json
/// {"events":[{"kind":"fetch","variant":"",
///             "userHash":"<sha256hex16(userId)>","version":1}]}
/// ```
///
/// Analytics are env-wide: the canonical event shape carries
/// kind/variant/userHash/version only. `variant` is `""` here because
/// the variants map has no `""` key (`variants[""] ?? ""`). `version`
/// is the fetched release version so stats can break fetches down
/// per version.
///
/// Deprecated `flag` compatibility: the POST body still sends
/// `'flag': ''` (see below) so payloads validate against pre-removal
/// servers; new servers ignore it. Do not add per-flag keys —
/// analytics are never broken down by flag.
///
/// Fire-and-forget: every failure mode (network, timeout, non-2xx,
/// malformed baseUrl) is swallowed and the future always completes
/// normally. It MUST never fail the fetch.
Future<void> postFetchEvent({
  required http.Client client,
  required String baseUrl,
  required String env,
  required String apiKey,
  required String userId,
  required Map<String, String> variants,
  int version = 0,
  Duration timeout = const Duration(seconds: 5),
  bool verbose = false,
}) async {
  try {
    cwDebug(verbose, () => 'events POST start env=$env');
    final uri = Uri.parse(
      '${baseUrl.replaceAll(RegExp(r'/+$'), '')}/api/v1/env/${Uri.encodeComponent(env)}/events',
    );
    final body = jsonEncode({
      'events': [
        {
          'kind': 'fetch',
          // Deprecated: env-wide analytics have no per-flag breakdown.
          // Sent as "" so pre-removal servers still validate the payload;
          // new servers ignore it.
          'flag': '',
          'variant': variants[''] ?? '',
          'userHash': sha256Hex16(userId),
          'version': version,
        },
      ],
    });
    final resp = await client
        .post(
          uri,
          headers: {
            'Content-Type': 'application/json',
            'X-ConfigWire-Key': apiKey,
          },
          body: body,
        )
        .timeout(timeout);
    cwDebug(
      verbose,
      () => 'events POST success env=$env status=${resp.statusCode}',
    );
    // Response status/body intentionally ignored: any status
    // (202, 429, 401, ...) is fine — best effort only.
  } catch (e) {
    // Intentionally swallowed: analytics must never break fetching.
    // REDACT: never surface the SDK key in logs.
    final raw = '$e';
    final safe = apiKey.isNotEmpty ? raw.replaceAll(apiKey, '<redacted>') : raw;
    cwDebug(verbose, () => 'events POST failed env=$env error=$safe');
  }
}

/// `sha256hex16`: lowercase `hex(sha256(utf8(userId)))[:16]`.
///
/// CHOICE (recorded per contract): the `crypto` package (pure-Dart,
/// no Flutter, no native) instead of hand-rolling SHA-256 — audited
/// implementation, zero platform risk. Matches the server's
/// `HashUserID` exactly, including empty input -> `""`
/// (an anonymous fetch carries no identity, not even a hash).
String sha256Hex16(String userId) {
  if (userId.isEmpty) return '';
  return crypto.sha256.convert(utf8.encode(userId)).toString().substring(0, 16);
}

import 'dart:async';
import 'dart:convert';

import 'package:crypto/crypto.dart' as crypto;
import 'package:http/http.dart' as http;

/// Best-effort fetch-event POST helper (todo 12).
///
/// After a successful 200 fetch the SDK reports one analytics event:
///
/// ```json
/// {"events":[{"kind":"fetch","flag":"","variant":"",
///             "userHash":"<sha256hex16(userId)>"}]}
/// ```
///
/// `flag` is always `""` (a fetch is env-wide, not per-flag); `variant`
/// is `""` here because the variants map has no `""` key
/// (`variants[""] ?? ""`). The server stores unknown/empty flag keys with
/// the relation unset while preserving kind/variant/userHash, so this
/// single aggregate event is safe to send.
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
  Duration timeout = const Duration(seconds: 5),
}) async {
  try {
    final uri = Uri.parse(
      '${baseUrl.replaceAll(RegExp(r'/+$'), '')}/api/v1/env/$env/events',
    );
    final body = jsonEncode({
      'events': [
        {
          'kind': 'fetch',
          'flag': '',
          'variant': variants[''] ?? '',
          'userHash': sha256Hex16(userId),
        },
      ],
    });
    final resp = await client
        .post(
          uri,
          headers: {
            'Content-Type': 'application/json',
            'X-ConfigNest-Key': apiKey,
          },
          body: body,
        )
        .timeout(timeout);
    // Drain explicitly so linters see the response is handled; any
    // status (202, 429, 401, ...) is fine — best effort only.
    if (resp.statusCode == -1) return;
  } catch (_) {
    // Intentionally swallowed: analytics must never break fetching.
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

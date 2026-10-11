import 'dart:async';
import 'dart:convert';

import 'package:crypto/crypto.dart' as crypto;
import 'package:http/http.dart' as http;

import 'cw_logger.dart';

/// Analytics event kind sent as `kind` in the events POST body.
///
/// Serialized with `kind.name` (`fetch` / `exposure`), matching the
/// server ingest contract (`configwire/ingest/ingest.go`).
enum EventKind { fetch, exposure }

/// Best-effort analytics-event POST helper.
///
/// After a successful 200 fetch the SDK reports one analytics event:
///
/// ```json
/// {"events":[{"kind":"fetch","flag":"exp_bool","variant":"control",
///             "userHash":"<sha256hex16(userId)>","version":1}]}
/// ```
///
/// `flag` carries the per-flag attribution key so stats can break
/// fetches down by flag (`GET .../stats?flag=<key>`); `variant` is
/// `variants[flag] ?? ""` (flags without an experiment arm record
/// `""`). `version` is the fetched release version so stats can break
/// fetches down per version. The default `flag: ""` keeps the historic
/// env-wide aggregate shape: a fetch with no single flag attribution
/// (the whole-release fetch in `ConfigWire.fetchAndActivate`) sends
/// `flag: ""`, and the server stores it with the relation unset while
/// preserving kind/variant/userHash/version.
///
/// Most callers should prefer `ConfigWire.trackExposure` /
/// `ConfigWire.trackFetch`, which snapshot targeting, variants, and
/// version for you.
///
/// Fire-and-forget: every failure mode (network, timeout, non-2xx,
/// malformed baseUrl) is swallowed and the future always completes
/// normally. It MUST never fail the fetch.
Future<void> postEvent({
  required http.Client client,
  required String baseUrl,
  required String env,
  required String apiKey,
  required String userId,
  required Map<String, String> variants,
  String flag = '',
  int version = 0,
  Duration timeout = const Duration(seconds: 5),
  bool verbose = false,
  EventKind kind = EventKind.fetch,
}) async {
  try {
    cwDebug(verbose, () => 'events POST start env=$env');
    final uri = Uri.parse(
      '${baseUrl.replaceAll(RegExp(r'/+$'), '')}/api/v1/env/${Uri.encodeComponent(env)}/events',
    );
    final body = jsonEncode({
      'events': [
        {
          'kind': kind.name,
          'flag': flag,
          'variant': variants[flag] ?? '',
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

/// Legacy fetch-event sender. Prefer `postEvent` with [EventKind], or
/// the easier `ConfigWire.trackExposure` / `ConfigWire.trackFetch`,
/// which snapshot targeting, variants, and version for you.
@Deprecated(
  'Use postEvent(kind: EventKind.fetch|EventKind.exposure) instead, '
  'or ConfigWire.trackExposure/trackFetch for the easy path.',
)
Future<void> postFetchEvent({
  required http.Client client,
  required String baseUrl,
  required String env,
  required String apiKey,
  required String userId,
  required Map<String, String> variants,
  String flag = '',
  int version = 0,
  Duration timeout = const Duration(seconds: 5),
  bool verbose = false,
  String kind = 'fetch',
}) async {
  if (kind != 'fetch' && kind != 'exposure') {
    cwDebug(verbose, () => 'events POST skipped env=$env invalid kind=$kind');
    return;
  }
  return postEvent(
    client: client,
    baseUrl: baseUrl,
    env: env,
    apiKey: apiKey,
    userId: userId,
    variants: variants,
    flag: flag,
    version: version,
    timeout: timeout,
    verbose: verbose,
    kind: kind == 'exposure' ? EventKind.exposure : EventKind.fetch,
  );
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

import 'dart:convert';

import 'events.dart';

/// Targeting attributes sent as fetch query params so the server can
/// evaluate `rules` per-fetch (`configwire/fetch/fetch.go:BuildContext`
/// -> `configwire/eval/eval.go:Evaluate`).
///
/// Mapping (client -> server `eval.Context`):
/// * [userId] -> `?userHash=<sha256hex16>` -> `UserHash` (percentile/experiment bucketing).
/// * [platform] -> `?platform=` -> `Platform` (`==/!=/contains/regex`).
/// * [appVersion] -> `?appVersion=` -> strict semver compare.
/// * [locale] -> `?locale=`, [country] -> `?country=`.
/// * [customAttrs] -> `?attrs=<json>` -> `custom.*` flat lookup.
///
/// Empty strings / empty maps are omitted from the query (anonymous).
class Targeting {
  const Targeting({
    this.userId = '',
    this.platform = '',
    this.appVersion = '',
    this.locale = '',
    this.country = '',
    this.customAttrs = const {},
  });

  /// Stable install ID hashed to `userHash` + `percentile` bucketing (`Bucket(userHash,seed)`).
  /// Hashed client-side to `?userHash=<sha256hex16>`; raw ID never leaves the device.
  ///
  /// The server honors a well-formed 16-hex hash for bucketing/exposure
  /// identity and falls back to its daily anonID otherwise.
  final String userId;

  /// e.g. `ios`, `android`, `web`.
  final String platform;

  /// Strict semver `MAJOR.MINOR.PATCH`; unparseable -> condition false (200).
  final String appVersion;

  /// e.g. `en-US` (exact match; use `contains` for prefix).
  final String locale;

  /// e.g. `US`.
  ///
  /// Deprecated: the server derives country from request headers and
  /// ignores the client-sent value. Will be removed in the next major
  /// release; stop setting it.
  @Deprecated('Server derives country from headers; country is ignored and will be removed next major.')
  final String country;

  /// Flat map for `custom.<name>` conditions (`?attrs=` JSON object).
  final Map<String, Object?> customAttrs;

  /// Empty (anonymous) targeting: no query params.
  static const empty = Targeting();

  Targeting copyWith({
    String? userId,
    String? platform,
    String? appVersion,
    String? locale,
    String? country,
    Map<String, Object?>? customAttrs,
  }) {
    return Targeting(
      userId: userId ?? this.userId,
      platform: platform ?? this.platform,
      appVersion: appVersion ?? this.appVersion,
      locale: locale ?? this.locale,
      country: country ?? this.country,
      customAttrs: customAttrs ?? this.customAttrs,
    );
  }

  /// Query params for `GET /api/v1/env/{env}/config`. Empty fields omitted.
  /// `customAttrs` encoded as compact JSON; empty map omitted.
  /// [userId] is emitted as `?userHash=<sha256hex16>` (empty omitted;
  /// see [userId] for the honored-hash rule).
  ///
  /// Deprecated: `country` is accepted-but-ignored (the server derives
  /// country per request); emission stays for compat and will be removed
  /// in the next major release.
  Map<String, String> toQueryParameters() {
    final q = <String, String>{};
    if (userId.trim().isNotEmpty) q['userHash'] = sha256Hex16(userId.trim());
    if (platform.isNotEmpty) q['platform'] = platform;
    if (appVersion.isNotEmpty) q['appVersion'] = appVersion;
    if (locale.isNotEmpty) q['locale'] = locale;
    if (country.isNotEmpty) q['country'] = country;
    if (customAttrs.isNotEmpty) q['attrs'] = jsonEncode(customAttrs);
    return q;
  }
}

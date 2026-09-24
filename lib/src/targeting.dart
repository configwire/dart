import 'dart:convert';

/// Targeting attributes sent as fetch query params so the server can
/// evaluate `rules` per-fetch (`configwire/fetch/fetch.go:BuildContext`
/// -> `configwire/eval/eval.go:Evaluate`).
///
/// Mapping (client -> server `eval.Context`):
/// * [userId] -> `?uid=` -> `UserID` (percentile/experiment bucketing).
/// * [platform] -> `?platform=` -> `Platform` (`==/!=/contains/regex`).
/// * [appVersion] -> `?appVersion=` -> strict semver compare.
/// * [locale] -> `?locale=`, [country] -> `?country=`.
/// * [customAttrs] -> `?attrs=<json>` -> `custom.*` flat lookup.
///
/// Empty strings / empty maps are omitted from the query (anonymous).
class TargetingContext {
  const TargetingContext({
    this.userId = '',
    this.platform = '',
    this.appVersion = '',
    this.locale = '',
    this.country = '',
    this.customAttrs = const {},
  });

  /// User identity for `uid` + `percentile` bucketing (`Bucket(userID,seed)`).
  final String userId;

  /// e.g. `ios`, `android`, `web`.
  final String platform;

  /// Strict semver `MAJOR.MINOR.PATCH`; unparseable -> condition false (200).
  final String appVersion;

  /// e.g. `en-US` (exact match; use `contains` for prefix).
  final String locale;

  /// e.g. `US`.
  final String country;

  /// Flat map for `custom.<name>` conditions (`?attrs=` JSON object).
  final Map<String, Object?> customAttrs;

  /// Empty (anonymous) context: no query params.
  static const empty = TargetingContext();

  TargetingContext copyWith({
    String? userId,
    String? platform,
    String? appVersion,
    String? locale,
    String? country,
    Map<String, Object?>? customAttrs,
  }) {
    return TargetingContext(
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
  Map<String, String> toQueryParameters() {
    final q = <String, String>{};
    if (userId.trim().isNotEmpty) q['uid'] = userId.trim();
    if (platform.isNotEmpty) q['platform'] = platform;
    if (appVersion.isNotEmpty) q['appVersion'] = appVersion;
    if (locale.isNotEmpty) q['locale'] = locale;
    if (country.isNotEmpty) q['country'] = country;
    if (customAttrs.isNotEmpty) q['attrs'] = jsonEncode(customAttrs);
    return q;
  }
}

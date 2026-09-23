import 'dart:convert';

/// Pure-Dart file-JSON cache for the ConfigWire SDK (todo 12).
///
/// On-disk schema (all four keys always present after a successful fetch):
///
/// ```json
/// {"etag":"<stored etag verbatim>","version":1,
///  "fetchedAt":"2026-09-22T00:00:00.000Z",
///  "values":{"flag_key": <typed json value>}}
/// ```
///
/// `shared_preferences` was deliberately dropped: it is Flutter-only
/// and would violate the pure-Dart rule. Persistence lives behind the
/// [CacheStore] seam instead — Hive by default (see `hive_store.dart`),
/// which serves VM disks and browser IndexedDB from one dependency.
///
/// Corrupt files (missing file, bad JSON, wrong shapes) load as `null` —
/// the caller falls back to in-app defaults and never throws.
class CacheData {
  CacheData({
    required this.etag,
    required this.version,
    required this.fetchedAt,
    required this.values,
    Map<String, String>? variants,
  }) : variants = variants ?? {};

  final String etag;
  final int version;
  final DateTime fetchedAt;
  final Map<String, Object?> values;
  final Map<String, String> variants;

  Map<String, Object?> toJson() => {
        'etag': etag,
        'version': version,
        'fetchedAt': fetchedAt.toUtc().toIso8601String(),
        'values': values,
        'variants': variants,
      };

  /// Returns null when [json] is not a well-formed cache document.
  static CacheData? fromJson(Map<String, Object?> json) {
    final etag = json['etag'];
    final version = json['version'];
    final fetchedAt = json['fetchedAt'];
    final values = json['values'];
    if (etag is! String || values is! Map) return null;
    final v = version is int ? version : (version is num ? version.toInt() : 0);
    DateTime at;
    if (fetchedAt is String) {
      at = DateTime.tryParse(fetchedAt) ??
          DateTime.fromMillisecondsSinceEpoch(0, isUtc: true);
    } else {
      at = DateTime.fromMillisecondsSinceEpoch(0, isUtc: true);
    }
    // Tolerant parsing: old cache files without `variants` default to {}.
    final variants = <String, String>{};
    final rawVariants = json['variants'];
    if (rawVariants is Map) {
      for (final e in rawVariants.entries) {
        if (e.key is String && e.value is String) {
          variants[e.key as String] = e.value as String;
        }
      }
    }
    return CacheData(
      etag: etag,
      version: v,
      fetchedAt: at,
      values: Map<String, Object?>.from(values),
      variants: variants,
    );
  }
}

/// Decodes one JSON string into [CacheData], or null when malformed.
/// Used to keep `dart:convert` referenced in this model-only library.
CacheData? cacheDataFromJsonString(String raw) {
  try {
    final decoded = jsonDecode(raw);
    if (decoded is! Map) return null;
    return CacheData.fromJson(Map<String, Object?>.from(decoded));
  } catch (_) {
    return null;
  }
}

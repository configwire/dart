import 'dart:convert';
import 'dart:io';

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
/// `shared_preferences` was deliberately dropped (T4): it is Flutter-only
/// and would violate the pure-Dart rule. `dart:io` File is stdlib and
/// works on every Dart target with a filesystem.
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

/// Loads the cache file at [path], or null when absent/corrupt.
/// Never throws: every I/O and parse failure maps to null.
Future<CacheData?> loadCacheFile(String path) async {
  try {
    final file = File(path);
    if (!await file.exists()) return null;
    final raw = await file.readAsString();
    final decoded = jsonDecode(raw);
    if (decoded is! Map) return null;
    return CacheData.fromJson(Map<String, Object?>.from(decoded));
  } catch (_) {
    return null;
  }
}

/// Persists [data] to [path] (parent directories created as needed).
/// Throws on I/O failure — the caller decides whether a write failure
/// should fail the fetch (it must not: fetch stays successful).
Future<void> saveCacheFile(String path, CacheData data) async {
  final file = File(path);
  await file.parent.create(recursive: true);
  await file.writeAsString(jsonEncode(data.toJson()));
}

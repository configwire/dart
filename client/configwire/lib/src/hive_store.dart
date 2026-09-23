import 'dart:convert';

import 'package:hive_ce/hive_ce.dart';

import 'cache_model.dart';
import 'cache_store.dart';

/// Hive-backed [CacheStore] over a host-opened [box] (bring-your-own-box).
///
/// Storage scheme: one JSON string per environment under [cacheKey]
/// (default `'cache_<env>'`). An explicit [cacheKey] override isolates
/// tenants sharing one box. Only plain JSON strings are stored, so no
/// Hive type adapters are needed.
///
/// The host owns Hive entirely: init it (`Hive.init` on VM,
/// `Hive.initFlutter` on Flutter, nothing on Web where the box lives in
/// IndexedDB), open the box, then hand it here. This class never
/// initializes Hive, never opens or closes anything: [dispose] is a
/// deliberate no-op and the host owns `Hive.close()` (and [box]).
///
/// Never-throw contract: [load] returns null on any miss, corruption, or
/// backend failure and never throws; [save] swallows every failure into
/// a silent no-op and never throws. Values are stored as cleartext
/// JSON — no encryption, no apiKey is accepted or stored here.
class HiveCacheStore implements CacheStore {
  /// Creates a Hive-backed cache store over an already-opened [box].
  ///
  /// Never throws: argument wiring only, no I/O happens here.
  HiveCacheStore({
    required this.box,
    required this.env,
    String? cacheKey,
  }) : cacheKey = cacheKey ?? 'cache_$env';

  /// Host-opened box. Never closed by this class.
  final Box box;

  /// Environment slug this store caches (part of the default [cacheKey]).
  ///
  /// Stored for key derivation only; never written to the box.
  final String env;

  /// Key within [box]. Defaults to `'cache_<env>'`.
  ///
  /// Pass an explicit value to isolate tenants sharing one box.
  final String cacheKey;

  @override
  Future<CacheData?> load() async {
    // Never throws: every miss, corruption, or backend failure → null.
    try {
      Object? raw;
      try {
        raw = box.get(cacheKey);
      } catch (_) {
        return null;
      }
      if (raw is String) {
        // The core codec is itself never-throw (try/catch → null).
        return cacheDataFromJsonString(raw);
      }
      // Absent key or wrong-type value (never written by save).
      return null;
    } catch (_) {
      return null;
    }
  }

  @override
  Future<void> save(CacheData data) async {
    // Never throws: every failure is swallowed into a silent no-op.
    try {
      try {
        await box.put(cacheKey, jsonEncode(data.toJson()));
      } catch (_) {
        // Swallowed: the core fetch caller swallows too (double safety).
      }
    } catch (_) {
      // Swallowed: save degrades to a no-op, never throws.
    }
  }

  /// Releases this store's handle without closing anything.
  ///
  /// Deliberate no-op: the host owns `Hive.close()` (and [box]).
  /// Never throws and never calls `Box.close()`.
  Future<void> dispose() async {}

  @override
  String toString() {
    // Redacted: cacheKey only. Never values, etag, or apiKey
    // (no apiKey is stored on this class at all).
    return 'HiveCacheStore(cacheKey: $cacheKey)';
  }
}

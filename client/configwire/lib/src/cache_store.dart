import 'cache_model.dart';

/// The default is [MemoryCacheStore] (session only); implement
/// [CacheStore] for disk persistence. `load` returns null on any miss/corruption and never
/// throws; `save` may throw on I/O failure for stores that allow it
/// (the fetch caller swallows it; fetch stays success).
abstract interface class CacheStore {
  /// Loads the last saved row, or null on any miss/corruption. Never throws.
  Future<CacheData?> load();

  /// Persists [data], overwriting the previous row. May throw on I/O
  /// failure; the fetch caller swallows it and stays successful.
  Future<void> save(CacheData data);
}

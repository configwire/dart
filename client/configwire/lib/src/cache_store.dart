import 'cache_model.dart';

/// Seam for cache persistence.
///
/// The default is [MemoryCacheStore] (session only); implement
/// [CacheStore] for disk persistence. `load` returns null on any miss/corruption and never
/// throws; `save` may throw on I/O failure for stores that allow it
/// (the fetch caller swallows it; fetch stays success).
abstract interface class CacheStore {
  Future<CacheData?> load();
  Future<void> save(CacheData data);
}

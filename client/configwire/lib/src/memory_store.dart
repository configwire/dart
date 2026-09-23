import 'cache_model.dart';
import 'cache_store.dart';

/// Holds the last saved row for the session only (no disk).
/// Passing an explicit store (e.g. your own CacheStore) opts into
/// persistence.
class MemoryCacheStore implements CacheStore {
  MemoryCacheStore({CacheData? seeded}) : _data = seeded;

  CacheData? _data;

  @override
  Future<CacheData?> load() async => _data;

  @override
  Future<void> save(CacheData data) async {
    _data = data;
  }
}

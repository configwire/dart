import 'cache_model.dart';
import 'cache_store.dart';

/// Ephemeral in-memory [CacheStore]: the [ConfigWire] default.
///
/// Holds the last saved row for the session only (no disk).
/// Never throws: [load] returns null when empty, [save] overwrites.
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

// Barrel for the cache seam.
//
// `CacheData` lives in `cache_model.dart`, the [CacheStore] seam in
// `cache_store.dart`. The default backend is the in-memory store in
// `memory_store.dart`; the Hive backend in `hive_store.dart` persists
// over a host-opened box — every existing `cache.dart` import path
// keeps compiling unchanged.
export 'cache_model.dart';
export 'cache_store.dart';
export 'hive_store.dart';
export 'memory_store.dart';

## Unreleased (breaking)

- Bring-your-own-box: `HiveCacheStore` now requires an opened `Box`
  (`HiveCacheStore(box: ..., env: ...)`); `boxName`/`dir`/`initDefault`/
  `ensureOpen` auto-open is gone. The host inits Hive (`Hive.init` on VM,
  `Hive.initFlutter` on Flutter, nothing on Web) and opens the box.
  `ConfigWire` defaults to a session-only `MemoryCacheStore` (no disk);
  pass `HiveCacheStore` for persistence. `client/configwire_flutter`
  is deleted: use this single package on Flutter too.
- File cache removed: `FileCacheStore`, `loadCacheFile`/`saveCacheFile`,
  and the `cacheFile:` parameter are gone. The default cache is now a
  Hive box (`configwire_cache`, key `cache_<env>`) in a cwd-relative
  `.configwire-hive` dir on VM and IndexedDB on Web — pass an explicit
  `store:` (e.g. `HiveCacheStore(env: ..., dir: ...)`) in production.
  Stale `.configwire_*-cache.json` files are orphaned (never auto-
  deleted); clients refetch once on next launch (cold cache).
- New dependency: `hive_ce` (pure-Dart part only; core stays Flutter-free).

## 0.0.1

- Moved from `client/dart` (`config_wire` 0.1.0); full package rename to `configwire`.
- Breaking default cache-filename rename `.config_wire_` → `.configwire_` (no fallback; stale files rebuild on next fetch).

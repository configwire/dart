## Unreleased

- Removed (breaking): the external box-cache dependency and the
  box-backed store are gone. Cache persistence is now bring-your-own
  via the `CacheStore` seam; `MemoryCacheStore` (session-only) remains
  the default. See README for a custom-store example. Migration: delete
  box init/openBox code, pass your own `CacheStore` or omit `store:`.

## Unreleased (breaking)

- Bring-your-own-box: the box-backed store required a host-opened box
  (`box:` plus `env:` parameters); `boxName`/`dir`/`initDefault`/
  `ensureOpen` auto-open was gone. The host initialized the backend (VM
  path init, Flutter init, nothing on Web) and opened the box.
  `ConfigWire` defaulted to a session-only `MemoryCacheStore` (no disk);
  the box-backed store was passed for persistence.
  `client/configwire_flutter` was deleted: use this single package on
  Flutter too.
- File cache removed: `FileCacheStore`, `loadCacheFile`/`saveCacheFile`,
  and the `cacheFile:` parameter are gone. The default cache was a
  host-opened box (`configwire_cache`, key `cache_<env>`) in a
  cwd-relative disk dir on VM and IndexedDB on Web — an explicit
  `store:` was passed in production. Stale `.configwire_*-cache.json`
  files are orphaned (never auto-deleted); clients refetch once on next
  launch (cold cache).
- New dependency (later removed, see above): a pure-Dart third-party box
  backend (core stayed Flutter-free).

## 0.0.1

- Moved from `client/dart` (`config_wire` 0.1.0); full package rename to `configwire`.
- Breaking default cache-filename rename `.config_wire_` → `.configwire_` (no fallback; stale files rebuild on next fetch).

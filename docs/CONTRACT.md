# ConfigNest Wire Contract (frozen)

ConfigNest-native wire. No external parity claims.

Conventions used below:

- `{env}` is the environment slug (for example `dev`).
- SDK auth header on SDK paths: `X-ConfigNest-Key: <full opaque key>`.
- Admin paths use superuser auth (`Authorization: <token>`, bare, Bearer optional).
- `Ref` cites the handler source that owns each shape.

## 1. Fetch — `GET /api/v1/env/{env}/config`

Ref: `server/fetch/fetch.go:286` (order), `:87` (route + gzip).

Query params: `uid`, `platform`, `appVersion`, `locale`, `country`,
`attrs` (JSON object, max 8192 bytes), `exp` (status override).

Auth/env order: 401 (key) then 404 (unknown env slug) then 401
(key not scoped to this env) then 400/414 (attrs) then 200/304.
Ref: `server/fetch/fetch.go:289-308`.

Multi-project slugs: the env is resolved deterministically from the
key's env record (the key's env IS the env), so a slug shared by
several projects can never misroute on SDK paths. Admin paths take an
optional `?project=<projectId>` qualifier; a slug shared by 2+
projects without it then `400` (ambiguous, names the collision),
while unambiguous slugs keep working without it. Ref:
`server/envresolve/resolve.go`.

### 1a. `200` config

```json
{"version": 1, "etag": "b41b62605c0df712", "values": {"launch_flag": true}, "variants": {"launch_flag": "treatment"}, "fetchAt": "2026-09-22T00:00:00.000000000Z"}
```

- `version`: max release version for the env (int).
- `etag`: stored release etag, served verbatim, also in the `ETag` response header. Never recomputed. Ref: `:324-325`.
- `values`: evaluated flag values, typed per flag.
- `variants`: one entry per flag targeted by at least one experiment row (first row in snapshot id order wins); flags with no targeting experiment have no entry. Anonymous or default-variant fallthrough is recorded as `""`. Ref: `:152-178`.
- `fetchAt`: server time, RFC3339Nano.
- Gzip when the client sends `Accept-Encoding: gzip`. Ref: `:87`.
- CORS `Access-Control-Allow-Origin: *` by default, or `CONFIGNEST_CORS_ORIGIN` when set. Ref: `:76-83,279-281`.
- `OPTIONS /api/v1/env/{env}/config` answers `204` with `Allow-Methods`, `Allow-Headers`, `Max-Age: 86400`. Ref: `:93-101`.

### 1b. Empty release (env has no release row yet) — `200`, never 404

```json
{"version": 0, "etag": "none", "values": {}, "variants": {}, "fetchAt": "2026-09-22T00:00:00.000000000Z"}
```

Ref: `:314-322`. Values are empty, not flag defaults; SDKs use in-app defaults.

### 1c. `304` not modified

`If-None-Match` equals the stored etag by exact string match only
(`W/`-prefixed or quoted values do not match) then `304` with an
empty body. Ref: `:140-143,326-328`.

### 1d. Fetch errors

Malformed `attrs` (bad JSON, or valid JSON that is not an object):

```json
{"data": {}, "message": "malformed attrs: not a JSON object", "status": 400}
```

`attrs` over 8192 bytes:

```json
{"message": "attrs too large: max 8192 bytes", "status": 414}
```

Ref: `:109-136,302-308`. `attrs` shape errors come from `re.BadRequestError`
(PocketBase `data/message/status` shape); oversize comes from explicit
`re.JSON` with `message/status` only. Unknown `appVersion` values fall
through to defaults with `200`, never `400`.

## 2. Publish — `POST /api/v1/admin/env/{env}/publish`

Ref: `server/releases/handler.go:32-35` (route + superuser-only), `:83-149` (handler).
Optional `?project=<projectId>` disambiguates a slug shared by
several projects (`400` ambiguous without it); unambiguous slugs keep
working without it. Ref: `server/envresolve/resolve.go`.

Request (note optional, baseVersion required, must equal the env's
current max, 0 on first publish):

```json
{"note": "ship hero_button", "baseVersion": 0}
```

Empty body or malformed JSON then `400`. A `baseVersion` sent as a
JSON string fails decoding then `400`. Ref: `:68-76`.

Success `200`:

```json
{"version": 1, "etag": "5dadac695eb4c603"}
```

Snapshot schema (server-side build only, never client-supplied;
flags/rules scoped to the env's project, sorted by key/priority;
experiments included as-is, all rows):

```json
{"flags": [{"key": "launch_flag", "type": "bool", "default": false, "group": "", "rules": [{"priority": 0, "condition": {"field": "platform", "op": "==", "value": "ios"}, "value": true}]}], "experiments": [{"id": "abc123", "flag": "launch_flag", "seed": "exp-seed-1", "variants": [{"name": "control"}, {"name": "treatment"}], "status": "running"}]}
```

Ref: `server/releases/snapshot.go:77-106` (shapes), `:187-269` (builder).

ETag rule: `etag = hex(sha256(version + ":" + snapshot))[:16]` over the
canonical server-marshaled bytes. The version is hashed in, so a
rollback row gets a fresh etag with byte-identical snapshots.
Ref: `server/releases/snapshot.go:108-115`.

Stale `baseVersion` then `409` with no write:

```json
{"message": "Stale baseVersion: a newer release exists.", "status": 409, "currentVersion": 2}
```

Ref: `:102-108`. Concurrent double-publish with the same baseVersion
yields exactly one `200` plus one `409` (process-local write mutex).
Ref: `:21-27`.

Publish validation failures then `400`: empty project
(`nothing to publish`), bad key shape (`^[A-Za-z_][A-Za-z0-9_]*$`,
1-128 chars), over 1000 flags/project, bad flag type (must be
`number|string|bool|json`), default or rule value not coercible to the
flag type, bad rule condition shape (unknown field/op, missing value
key, bare `custom`), bad experiment weights (must sum to exactly 10000
bps). Ref: `server/releases/snapshot.go:396-436`.

## 3. Rollback — `POST /api/v1/admin/releases/{version}/rollback`

Ref: `server/releases/handler.go:182-257`.

Request (note optional; empty body means no note; only malformed
non-empty bodies are `400`):

```json
{"note": "revert bad rollout"}
```

Success `200` (source snapshot bytes copied verbatim into a new row,
`version = max+1` in the source row's env, fresh etag):

```json
{"version": 3, "etag": "1f75363af9defec2"}
```

Unknown version then `404`. Version shared by 2+ envs then `400`
(`ambiguous version`). Non-integer version then `400`. Releases rows
are immutable (updates denied on every path). Ref: `:200-221`.

## 4. Events ingest — `POST /api/v1/env/{env}/events`

Ref: `server/ingest/handler.go:111-171` (order), `server/ingest/ingest.go:101-130` (body).

Order: 401 (key) then 404 (unknown env slug) / 400 (ambiguous slug,
key-less callers only) / 401 (env-scope mismatch) then 429 (rate)
then 400/413 (body) then 202.
Ref: `server/ingest/handler.go:108-109`.

Request:

```json
{"events": [{"kind": "exposure", "flag": "launch_flag", "variant": "treatment", "userHash": "4017c6d850aedf6b", "ts": "2026-09-22T00:00:00Z"}]}
```

- `kind` must be `fetch` or `exposure`.
- `flag`, `variant`, `userHash`, `ts` are all optional. Unknown flag
  keys are stored with the flag relation unset (variant preserved).
- `ts` accepts RFC3339 string, unix-seconds number, or absent/null
  (server time). Anything else then `400`.
- Raw `userId`/`ip` keys are strictly rejected (presence, not value,
  even null-valued) then `400`. Clients send `userHash` only.
- 1..100 events per request (0 or over 100 then `400`).
- Single event over 64KB or body over 128KB then `413`.
- Rate: per-key fixed window, `sdk_keys.rateLimit` req/60s, default 60
  when missing/non-positive; every authed hit consumes a token (even
  later-400s); over-limit then `429`. Full buffer then `503`.
  Ref: `server/ingest/ingest.go:44-58`, `server/ingest/handler.go:124-126,164-168`.

Success `202`:

```json
{"accepted": 2, "status": 202}
```

Rate-limit hit `429`:

```json
{"message": "Rate limit exceeded.", "status": 429}
```

Body errors `400`/`413` share the shape:

```json
{"message": "batch too large: max 100 events per request.", "status": 400}
```

Flush bound: buffered channel (cap 2048), flush every 1s or 500 rows;
worst-case ingest lag about 1s plus write time (bounded at 2s).
Ref: `server/ingest/ingest.go:25-28`.

## 5. Stats — `GET /api/v1/admin/env/{env}/stats?flag=X&since=7d`

Ref: `server/stats/stats.go:130-174` (handler). Superuser-only;
SDK-key-only or unauth then `401`.

Success `200`:

```json
{"fetches": 100, "exposures": 100, "perVariant": {"control": 60, "treatment": 40}, "version": 1}
```

- Exact integer counts, no rounding. `perVariant` covers exposures
  only, verbatim variant names (including `""` if stored).
- `version` is the env's max release version.
- `userHash` is never read (aggregate counts only).
- `since` grammar: `?since=<N>d`, absent/blank means 7d default,
  `N > 90` clamped to 90d (never an error), `N < 1` or wrong shape
  (`abc`, `-5d`, `0d`, `7h`, bare `7`) then `400`. Cutoff is
  now-UTC minus days; rows with ts before cutoff or zero ts excluded.
  Ref: `:49-65,98-119`.
- Unknown flag keys (including path-ish `../x`) then zeros `200`
  with version populated, never `404`. Events with unset flag
  relation drop out under any `?flag=` filter, count when absent.
- Unknown env slug then `404`. Optional `?project=<projectId>`
  disambiguates a slug shared by several projects (`400` ambiguous
  without it). Ref: `:144-155`.
- Aggregation is an in-Go O(n) scan over `events` (v1-appropriate;
  indexed replacement is the documented follow-up past ~100k rows).
  Ref: `:16-21,176-194`.

## 6. Stream — `GET /api/v1/env/{env}/stream`

Ref: `server/spike_probe.go:48-89`.

Real-key auth via `ingest.RequireSDKKey` plus env-scope check, same
order as fetch/ingest: 401 (key) then 404 (unknown env slug) / 400
(ambiguous slug, key-less callers only) then 401
(key env mismatch). Ref: `:49-60`.

After auth the handler holds SSE long-lived:

- Headers: `Content-Type: text/event-stream`, `Cache-Control: no-store`,
  `X-Accel-Buffering: no`.
- Keepalive: `: ping` comment frame (8 bytes, `: ping` plus blank line)
  every 20s until client disconnect or a 10min cap. No canned
  `config_update` event. Ref: `:67-88`.
- Loop runs inline on the request goroutine (no per-conn goroutine;
  ticker/timer stopped via defer; write/flush errors end the handler).
- `POST /api/v1/spike/publish/{clientId}` is a QA-only hook gated by a
  hardcoded spike key, never production; no publish-triggered fan-out.

## 7. Purge — `POST /api/v1/admin/maintenance/purge`

Ref: `server/purge/purge.go:298-336` (handler). Superuser-only.

`?dry=` absent/`0`/`false` means live; `1`/`true` means dry-run
(zero writes, same count); anything else (for example `abc`) then `400`.

Live `200`:

```json
{"deleted": 1, "dry": false, "cutoff": "2026-08-23T00:00:00Z"}
```

Dry-run `200`:

```json
{"deleted": 1, "dry": true, "cutoff": "2026-08-23T00:00:00Z"}
```

Empty table then `200` with `deleted: 0`. A daily 24h process-local
ticker purges with `cutoff = now - 30d`. Ref: `:284-296`.

## 8. Status-code index (exact bodies)

| Code | Meaning | Body | Ref |
| ---- | ------- | ---- | --- |
| 200 | fetch config / publish / rollback / stats / purge | shapes in sections 1, 2, 3, 5, 7 | fetch `:340-346`, releases `:148,257`, stats `:168-173`, purge `:320-335` |
| 202 | events accepted | `{"accepted": N, "status": 202}` | ingest handler `:170` |
| 204 | fetch CORS preflight (`OPTIONS`) | empty | fetch `:93-101` |
| 304 | fetch not modified (exact etag match) | empty | fetch `:326-328` |
| 400 | bad publish/rollback/ingest/stats/purge input; ambiguous env slug without `?project=` | PocketBase errors: `{"data": {}, "message": "...", "status": 400}`; ingest custom: `{"message": "...", "status": 400}` | releases `:70-76,193-195`, ingest `:173-175`, stats `:140`, purge `:312`, envresolve `resolve.go` |
| 401 | missing/unknown/revoked/env-mismatched SDK key; non-superuser on admin paths | PocketBase shape: `{"data": {}, "message": "Missing or invalid SDK key.", "status": 401}` (SDK paths) | ingest `:213-241`, fetch `:289-300`, spike `:49-60` |
| 404 | unknown env slug / unknown release version | PocketBase shape: `{"data": {}, "message": "Unknown env.", "status": 404}` | fetch `:294-297`, releases `:220-221` |
| 409 | stale publish baseVersion (no write) | `{"message": "Stale baseVersion: a newer release exists.", "status": 409, "currentVersion": N}` | releases `:102-108` |
| 413 | ingest body/event too large; fetch attrs too large is 414 | `{"message": "body exceeds 128KB.", "status": 413}` | ingest `:252-262` |
| 414 | fetch attrs over 8192 bytes | `{"message": "attrs too large: max 8192 bytes", "status": 414}` | fetch `:302-306` |
| 429 | ingest rate limit hit | `{"message": "Rate limit exceeded.", "status": 429}` | ingest handler `:124-126` |
| 503 | ingest buffer full (cap 2048, retry) | `{"message": "Ingest buffer full, retry.", "status": 503}` | ingest handler `:164-168` |

## 9. Staleness bound

Push is best-effort (near-instant while connected; no sub-second
guarantee). Otherwise freshness is at most `pollInterval` (Dart
default 15min) plus one fetch: the client poller runs in every state
(stream healthy, down, or 401), so the bound holds on all paths.
Ref: `client/dart/lib/src/realtime.dart` (RealtimeUpdater), `docs/SECURITY.md` section 7.

## 10. Retention

Raw `events` rows live 30 days, daily `event_daily` rollups keyed
`(day, env, flag, variant)` live 90 days. Cutoff is strictly-older-than
(`ts` before cutoff deleted, exactly-equal kept, zero-ts kept).
Rollup-before-delete runs in the same op (upsert then delete; a crash
between them double-counts only the in-flight bucket on re-run, and a
clean re-run converges to `deleted: 0`). Ref: `server/purge/purge.go:70-83,124-182`.

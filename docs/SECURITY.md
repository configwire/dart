# ConfigNest Security — Operator Guide

Single-binary deployment model (<10k DAU / <100rps). No WAF, no paid
infra, no external auth provider: the controls below are all in the
binary plus operator procedure.

## 1. SDK keys — storage

- The full key is an opaque string presented in `X-ConfigNest-Key`.
- `sdk_keys` rows store **only**:
  - `prefix` = first 8 chars of the full key (fast prefilter),
  - `hash` = lowercase `hex(sha256(fullKey))` (constant-time compare).
- **The full key is never stored.** A database dump cannot be replayed
  as credentials (sha256 is non-reversible); the prefix alone matches
  nothing without the hash preimage.
- Lookup order per request: prefix prefilter → constant-time hash
  compare → `revoked` check → env-scope check. Unknown, missing,
  revoked, or env-mismatched keys → `401`. Never log full keys
  (hashes only).

## 2. Rotation

### SDK keys (revoke flag + reissue)

1. Create the replacement key row (`prefix`, `hash`, `env`,
   `rateLimit`), distribute the full key out-of-band.
2. Flip the old row: `sdk_keys.revoked = true`.
3. Effect is immediate: revoked keys → `401` on fetch/ingest/stream
   (verified live: revoke → `401`, un-revoke restores `200`).
4. Delete the old row once clients have rolled.

### Superuser (password/secret rotation invalidates tokens)

- Auth tokens (including **impersonate tokens** minted via the
  PocketBase superuser impersonate endpoint) are JWTs signed with
  `record.tokenKey + collection.AuthToken.Secret`.
- PocketBase **rotates the per-record `tokenKey` on every password or
  email change** (`core/record_model.go`: `RefreshTokenKey()` on save
  when the password/email differs). The signing key therefore changes,
  and **all outstanding tokens for that superuser — including
  impersonate tokens — fail validation immediately**.
- Operator procedure on suspected compromise:
  1. Change the superuser password (dashboard `_/` or API).
     Old password stops working instantly; old tokens die with it.
  2. Optionally also rotate the auth collection's token secret
     (Settings → Application / auth collection options) and restart
     for a global invalidate across all auth records.
  3. Re-issue SDK keys (section above) if key material may have leaked.

## 3. PII — userHash-only, retention 30d raw / 90d rollups

- The ingest path **strictly rejects** raw `userId`/`ip` keys — even
  `null`-valued ones (presence, not value) → `400`. Clients must send
  the opaque `userHash` field.
- Server-side derivation, when needed, is `hex(sha256(userID))[:16]`
  (64 bits: enough to join fetch/exposure rows, non-reversible).
- Stats endpoints return aggregate counts only; `userHash` never
  leaves the server through them (never read, never exported).
- Retention (enforced by the `server/purge` daily job + manual
  `POST /api/v1/admin/maintenance/purge`, superuser-only):
  - **raw `events` rows: 30 days**, then rolled up and deleted;
  - **daily `event_daily` rollups `(day, env, flag, variant)`: 90 days**;
  - rollups carry counts only — no PII survives past the raw window.

## 4. Rate limits — 60/min default per key

- Fixed-window limiter, per key-hash: `sdk_keys.rateLimit` requests
  per 60s window; missing/non-positive → **default 60**.
- Every authenticated ingest hit consumes one token (even later-`400`s:
  no free probing). Over-limit → `429` (never `500`).
- Proven live (200rps × 15s burst on a default-limit key):
  **3000 requests → 60×`202` + 2940×`429` + 0×`500`**.
- Limiter state is process-local (resets on restart; one map entry per
  active key-hash, idle entries swept). Fetch/stream paths are not
  token-metered; size the key `rateLimit` for ingest load
  (load-gate keys use a high quota).

## 5. CORS

- Fetch responses (including `304`) send
  `Access-Control-Allow-Origin`, default **`*`** (Flutter/web dev).
- Production: set `CONFIGNEST_CORS_ORIGIN=https://app.example.com`
  (single origin, plain header, no infra). `OPTIONS` preflight →
  `204` with `Allow-Methods/Headers` + 86400s max-age.

## 6. Admin — superuser-only

- Publish, rollback, stats, and purge routes bind
  `RequireSuperuserAuth`. SDK-key-only callers → `401`.
- All 9 collections default-deny (`nil` rules → `403` for
  non-superusers). Releases rows are additionally immutable via
  server hooks (updates denied on every path incl. dashboard).
- Admin UI token lives in memory (+ optional localStorage copy):
  shared machines should skip persistence; logout clears both.

## 7. Stream — 10min cap + poll bound

- `GET /api/v1/env/:env/stream` requires a real SDK key + env scope
  (same `401 → 404 → 401` order as fetch/ingest), holds SSE with
  `: ping` keepalives every 20s, and **caps at 10 minutes** (server
  closes; client reconnects with backoff).
- Freshness bound: push is best-effort (~instant while connected);
  otherwise ≤ `pollInterval` (default 15min) + one fetch — the client
  poller runs in every state, so the bound holds stream-up, -down,
  and -`401` alike.

## 8. Response headers

- API handlers (releases, stats, fetch) stamp, via the shared
  `server/security` helper (ownership: those packages' files only,
  never `main.go`):
  - `X-Content-Type-Options: nosniff`,
  - `X-Frame-Options: DENY`,
  - `Referrer-Policy: no-referrer`.

## 9. Scale gate (measured 2026-09-22, k6 v2.3.0, port 8106)

- 60s at **100rps fetch + 50rps exposure** (9001 reqs):
  fetch **p95 0.78ms** (< 200ms budget, ~256× headroom),
  **failed 0.00%** (< 1% budget).
- `sqlite3 journal_mode` == `wal` (live re-verified).
- Ingest lag spot: 100-batch → countable in **636ms** (≤2s bound).
- Post-load matrix: publish `200` (v2), fetch `304`, stats `200`.
- Post-burst 30s at 100rps: 3001×`200`, 0×`429`, 0×`500` (no stuck state).
- Full JSON-parsed evidence: `.omo/evidence/task-16-confignest.log`.
- Runner: **k6** (`k6 --version` → v2.3.0; installed via brew for the
  gate). No `hey` fallback shipped — k6 was present.

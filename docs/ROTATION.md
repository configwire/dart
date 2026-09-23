# ConfigWire Key Rotation Runbook (breaking `cw-` reissue)

Why this exists: the rebrand renamed the SDK header to
`X-ConfigWire-Key` (`server/ingest/ingest.go:47`) and new keys carry the
`cw-` prefix. There is no compat shim: the old header answers `401` by
design, and old-prefix rows keep working only until you revoke them.
Rotate with the steps below.

Key facts (landed code, todos 5-7):

- `sdk_keys` rows store `prefix` = first 8 chars of the full key plus
  `hash` = lowercase `hex(sha256(fullKey))`; the full key is never
  stored. Lookup per request: prefix prefilter, constant-time hash
  compare, `revoked` check, env-scope check.
- Missing, unknown, revoked, or env-mismatched keys answer `401`
  `Missing or invalid SDK key.` on fetch, ingest, and stream.
- Admin session keys are `cw_admin_*` localStorage (memory-first copy;
  logout clears both).
- Orphaned old Dart `.config_nest_*-cache.json` files are harmless; the
  cache rebuilds on the next fetch (schema unchanged).

## Procedure (scratch-port script, copy-paste verbatim)

Run from the repo root on port 8120. Every command below was executed
in order for the todo-8 proof (log:
`.omo/evidence/configwire-t8-baseline.log` holds the 401/200 pair).

```bash
PORT=8120
BASE=http://127.0.0.1:$PORT
DATA_DIR=/tmp/cw-rotate-pbdata
rm -rf $DATA_DIR
cd server
go build -o /tmp/cw-rotate-bin .
/tmp/cw-rotate-bin superuser upsert rotate-op@example.com rotate-proof-pass-01 --dir=$DATA_DIR
(nohup /tmp/cw-rotate-bin serve --http=127.0.0.1:$PORT --dir=$DATA_DIR >/tmp/cw-rotate-serve.log 2>&1 &)
sleep 12
curl -s -o /dev/null -w "hello:%{http_code}\n" $BASE/hello
```

Expected: `hello:200`.

```bash
TOKEN=$(curl -s -X POST $BASE/api/collections/_superusers/auth-with-password -H 'Content-Type: application/json' -d '{"identity":"rotate-op@example.com","password":"rotate-proof-pass-01"}' | python3 -c 'import json,sys; print(json.load(sys.stdin)["token"])')
PROJ=$(curl -s -X POST $BASE/api/collections/projects/records -H "Authorization: $TOKEN" -H 'Content-Type: application/json' -d '{"name":"rotate-proj"}' | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')
ENVID=$(curl -s -X POST $BASE/api/collections/environments/records -H "Authorization: $TOKEN" -H 'Content-Type: application/json' -d "{\"project\":\"$PROJ\",\"slug\":\"rotate\"}" | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')
curl -s -o /dev/null -w "flag:%{http_code}\n" -X POST $BASE/api/collections/flags/records -H "Authorization: $TOKEN" -H 'Content-Type: application/json' -d "{\"key\":\"rotate_flag\",\"type\":\"bool\",\"defaultValue\":false,\"project\":\"$PROJ\"}"
```

Expected: `flag:200`.

### 1. Issue the new `cw-` key

```bash
NEW_KEY='cw-rotation-demo-key-01'
python3 -c 'import hashlib; print(hashlib.sha256(b"cw-rotation-demo-key-01").hexdigest())'
```

Expected: `599678969c40b5d24e1bb066d6a8227b2fffc242c8fee6a5ec8514d70abb9d63`.

```bash
NEW_PREFIX=${NEW_KEY:0:8}
echo "prefix:$NEW_PREFIX"
NEW_HASH=$(python3 -c "import hashlib; print(hashlib.sha256(b'$NEW_KEY').hexdigest())")
KEYROW=$(curl -s -X POST $BASE/api/collections/sdk_keys/records -H "Authorization: $TOKEN" -H 'Content-Type: application/json' -d "{\"prefix\":\"$NEW_PREFIX\",\"hash\":\"$NEW_HASH\",\"env\":\"$ENVID\",\"revoked\":false,\"rateLimit\":100000}" | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')
echo "keyrow:$KEYROW"
curl -s -o /dev/null -w "publish:%{http_code}\n" -X POST $BASE/api/v1/admin/env/rotate/publish -H "Authorization: $TOKEN" -H 'Content-Type: application/json' -d '{"note":"rotation proof","baseVersion":0}'
```

Expected: `prefix:cw-rotat`, then `publish:200`.
(`prefix` is the first 8 chars of the full key; `hash` is the lowercase
hex sha256 printed above.)

### 2. Prove the new key works (200)

```bash
curl -s -w "\nnew:%{http_code}\n" "$BASE/api/v1/env/rotate/config?uid=rotate-u1" -H "X-ConfigWire-Key: $NEW_KEY"
```

Expected body shape plus `new:200`:

```json
{"version":1,"etag":"<hex>","values":{"rotate_flag":false},"variants":{},"fetchAt":"<timestamp>"}
```

(`etag` is the release etag and `fetchAt` is now; both vary per run.
The proof run returned `etag ca8b8ecbb4742977` on its seed data.)

### 3. Prove the old header is dead (401)

```bash
curl -s -w "\nold:%{http_code}\n" "$BASE/api/v1/env/rotate/config?uid=rotate-u1" -H "X-ConfigNest-Key: $NEW_KEY"
```

Expected, byte-exact:

```json
{"data":{},"message":"Missing or invalid SDK key.","status":401}
old:401
```

Same `401` body answers unknown keys, revoked keys, and env-mismatched
keys on fetch, ingest (`POST .../events`), and the SSE stream.

### 4. Roll to a second key, revoke the first

Issue the replacement the same way (`cw-rotation-demo-key-02`,
sha256 `038028ca9e6e6cef58beae2cfb066385debb98ed79d60f9e4cde7ceeba484e5b`,
prefix `cw-rotat`), distribute it out-of-band, then flip the old row:

```bash
OLD_ROW=$KEYROW
NEW_KEY2='cw-rotation-demo-key-02'
NEW_HASH2=$(python3 -c "import hashlib; print(hashlib.sha256(b'$NEW_KEY2').hexdigest())")
curl -s -o /dev/null -w "key2:%{http_code}\n" -X POST $BASE/api/collections/sdk_keys/records -H "Authorization: $TOKEN" -H 'Content-Type: application/json' -d "{\"prefix\":\"${NEW_KEY2:0:8}\",\"hash\":\"$NEW_HASH2\",\"env\":\"$ENVID\",\"revoked\":false,\"rateLimit\":100000}"
curl -s -o /dev/null -w "revoke:%{http_code}\n" -X PATCH $BASE/api/collections/sdk_keys/records/$OLD_ROW -H "Authorization: $TOKEN" -H 'Content-Type: application/json' -d '{"revoked":true}'
curl -s -o /dev/null -w "old-fetch:%{http_code}\n" "$BASE/api/v1/env/rotate/config?uid=rotate-u1" -H "X-ConfigWire-Key: $NEW_KEY"
curl -s -o /dev/null -w "new-fetch:%{http_code}\n" "$BASE/api/v1/env/rotate/config?uid=rotate-u1" -H "X-ConfigWire-Key: $NEW_KEY2"
```

Expected: `key2:200`, `revoke:200`, `old-fetch:401` (same
`Missing or invalid SDK key.` body as step 3), `new-fetch:200`.
Revocation is immediate; un-revoking (`revoked:false`) restores `200`.
Delete the old row once all clients have rolled to the new key.

### 5. Cleanup

```bash
kill $(lsof -ti:8120)
rm -rf /tmp/cw-rotate-pbdata /tmp/cw-rotate-bin /tmp/cw-rotate-serve.log
```

Receipt: `curl $BASE/hello` refuses and `lsof -ti:8120` is empty.

## Client notes

- Dart (`config_wire` package, `ConfigWire` class): update the import
  to `package:config_wire/config_wire.dart`; the client already sends
  `X-ConfigWire-Key`. Delete stale `.config_nest_*-cache.json` files or
  leave them; they are never read again.
- Admin UI: first load restores the stored session from localStorage.
  An empty or missing token lands on the
  login form; a garbage token shows an error toast and stays in the
  shell (pre-existing PocketBase 403 behavior, unchanged by the
  rebrand).
- k6 gate: pass the new key as `GATE_KEY=<cw-key> k6 run
  --summary-export=/tmp/cw-gate-summary.json scripts/k6-fetch.js`.

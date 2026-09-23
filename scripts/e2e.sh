#!/usr/bin/env bash
# T17 end-to-end green path: fetch -> activate -> exposure -> stats -> rollback.
# FROZEN PATH: T18's Makefile references scripts/e2e.sh — do not move/rename.
# Single command from REPO ROOT:  bash scripts/e2e.sh   (script cds to server/ itself)
# NOT idempotent by design: every run uses a FRESH temp dir (/tmp/cw-t17-pbdata,
# rm -rf at start) so version numbers always start at 1. Re-run = fresh run.
# Hang bounds: macOS has no GNU timeout, so every curl carries --max-time 10
# and the boot-wait loop is bounded (30 x 1s). Server kill is in trap EXIT.
set -u -o pipefail

# ---- cwd-robust: always operate from server/; repo root derived from $0 ----
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$REPO_ROOT/server"

PORT="${E2E_PORT:-8108}"
BASE_URL="${BASE_URL:-http://127.0.0.1:$PORT}"
DATA_DIR="/tmp/cw-t17-pbdata"
BIN="/tmp/cw-t17-configwire"
LOG="$REPO_ROOT/.omo/evidence/task-17-configwire.log"
mkdir -p "$REPO_ROOT/.omo/evidence"
# Script tees itself (stdout+stderr) to the evidence log.
exec > >(tee "$LOG") 2>&1

PASS=0
FAIL=0
pass() { PASS=$((PASS + 1)); echo "PASS: $1"; }
fail() { FAIL=$((FAIL + 1)); echo "FAIL: $1"; }

SU_EMAIL="e2e-t17@example.com"
SU_PASS="e2e-t17-super-secret-01"
SDK_KEY="cw-e2e17-key-0123456789abcdef"
E2E_UID="e2e-user-7"
DEAD_PORT="8199"

SERVER_PID=""
cleanup() {
  if [ -n "$SERVER_PID" ] && kill -0 "$SERVER_PID" 2>/dev/null; then
    kill "$SERVER_PID" 2>/dev/null || true
    sleep 2
    if kill -0 "$SERVER_PID" 2>/dev/null; then
      kill -9 "$SERVER_PID" 2>/dev/null || true
    fi
  fi
  # Hunt orphans by port (go run / reaped-binary class, T1 rule).
  for p in $(lsof -ti:"$PORT" 2>/dev/null || true); do
    kill "$p" 2>/dev/null || true
  done
  sleep 1
  if curl -s -o /dev/null --max-time 3 "$BASE_URL/hello" 2>/dev/null; then
    echo "cleanup: server still up (unexpected)"
  else
    echo "DOWN_CONFIRMED (curl 000, port $PORT free)"
  fi
  rm -rf "$DATA_DIR" "$BIN"
  echo "CLEANUP receipts: rm -rf $DATA_DIR $BIN; no pb_data in repo"
}
trap cleanup EXIT

echo "=== T17 e2e start: port=$PORT base=$BASE_URL fresh dir=$DATA_DIR ==="

# ---- boot: fresh dir, build, serve -----------------------------------------
rm -rf "$DATA_DIR" "$BIN"
go build -o "$BIN" . || { fail "go build"; exit 1; }
pass "go build (unaffected, no Go changes expected)"
"$BIN" superuser upsert "$SU_EMAIL" "$SU_PASS" --dir="$DATA_DIR" >/tmp/cw-t17-upsert.log 2>&1 || {
  fail "superuser upsert ($(cat /tmp/cw-t17-upsert.log | head -c 300))"; exit 1;
}
pass "superuser upsert"
nohup "$BIN" serve --http="127.0.0.1:$PORT" --dir="$DATA_DIR" >/tmp/cw-t17-serve.log 2>&1 &
disown || true
sleep 1
UP=0
for _ in $(seq 1 30); do
  if curl -s -o /dev/null --max-time 2 "$BASE_URL/hello" 2>/dev/null; then UP=1; break; fi
  sleep 1
done
[ "$UP" = "1" ] && pass "server up on $PORT" || { fail "server up on $PORT"; exit 1; }
SERVER_PID="$(lsof -ti:"$PORT" 2>/dev/null | head -n 1 || true)"
echo "server PID-tracked: ${SERVER_PID:-unknown}"

# ---- helpers ---------------------------------------------------------------
# api POST/GET/PATCH with bare superuser token; prints body to file, echoes code.
su_post() { # url body outfile
  curl -s -o "$3" -w "%{http_code}" --max-time 10 -X POST "$1" \
    -H "Authorization: $TOKEN" -H "Content-Type: application/json" -d "$2" 2>/dev/null || echo "000"
}
su_patch() { # url body outfile
  curl -s -o "$3" -w "%{http_code}" --max-time 10 -X PATCH "$1" \
    -H "Authorization: $TOKEN" -H "Content-Type: application/json" -d "$2" 2>/dev/null || echo "000"
}
pyid() { python3 -c "import json,sys; print(json.load(open('$1')).get('$2',''))"; }

# ---- STAGE 1: seed via data API + publish v1 --------------------------------
echo "--- STAGE 1: seed + publish v1 ---"
CODE=$(curl -s -o /tmp/cw-t17-login.json -w "%{http_code}" --max-time 10 -X POST \
  "$BASE_URL/api/collections/_superusers/auth-with-password" \
  -H "Content-Type: application/json" \
  -d "{\"identity\":\"$SU_EMAIL\",\"password\":\"$SU_PASS\"}" 2>/dev/null || echo "000")
[ "$CODE" = "200" ] && pass "superuser login 200 (bare token)" || { fail "superuser login (code $CODE)"; exit 1; }
TOKEN="$(python3 -c "import json; print(json.load(open('/tmp/cw-t17-login.json'))['token'])")"

C="$BASE_URL/api/collections"
[ "$(su_post "$C/projects/records" '{"name":"e2e-proj","owner":"e2e"}' /tmp/cw-t17-proj.json)" = "200" ] \
  && pass "create project" || { fail "create project"; exit 1; }
PROJ="$(pyid /tmp/cw-t17-proj.json id)"
ENV_BODY="{\"project\":\"$PROJ\",\"slug\":\"e2e\"}"
[ "$(su_post "$C/environments/records" "$ENV_BODY" /tmp/cw-t17-env.json)" = "200" ] \
  && pass "create env e2e" || { fail "create env"; exit 1; }
ENVID="$(pyid /tmp/cw-t17-env.json id)"
GROUP_CORE_BODY="{\"name\":\"core\",\"project\":\"$PROJ\"}"
GROUP_GROWTH_BODY="{\"name\":\"growth\",\"project\":\"$PROJ\"}"
[ "$(su_post "$C/groups/records" "$GROUP_CORE_BODY" /tmp/cw-t17-g1.json)" = "200" ] \
  && pass "create group core" || { fail "create group core"; exit 1; }
[ "$(su_post "$C/groups/records" "$GROUP_GROWTH_BODY" /tmp/cw-t17-g2.json)" = "200" ] \
  && pass "create group growth" || { fail "create group growth"; exit 1; }
G1="$(pyid /tmp/cw-t17-g1.json id)"
G2="$(pyid /tmp/cw-t17-g2.json id)"

mkflag() { # key type defaultJson groupId outfile
  FLAG_PAYLOAD="{\"key\":\"$1\",\"type\":\"$2\",\"defaultValue\":$3,\"project\":\"$PROJ\",\"group\":\"$4\"}"
  su_post "$C/flags/records" "$FLAG_PAYLOAD" "$5"
}
[ "$(mkflag welcome_text string '"hello"' "$G1" /tmp/cw-t17-f1.json)" = "200" ] && pass "flag welcome_text:string" || { fail "flag welcome_text"; exit 1; }
[ "$(mkflag max_items number '10' "$G1" /tmp/cw-t17-f2.json)" = "200" ] && pass "flag max_items:number" || { fail "flag max_items"; exit 1; }
[ "$(mkflag exp_bool bool 'false' "$G2" /tmp/cw-t17-f3.json)" = "200" ] && pass "flag exp_bool:bool" || { fail "flag exp_bool"; exit 1; }
[ "$(mkflag home_config json '{"a":1}' "$G2" /tmp/cw-t17-f4.json)" = "200" ] && pass "flag home_config:json" || { fail "flag home_config"; exit 1; }
F1="$(pyid /tmp/cw-t17-f1.json id)"
F2="$(pyid /tmp/cw-t17-f2.json id)"
F3="$(pyid /tmp/cw-t17-f3.json id)"
F4="$(pyid /tmp/cw-t17-f4.json id)"

RULE1_BODY="{\"flag\":\"$F1\",\"priority\":10,\"condition\":{\"field\":\"platform\",\"op\":\"==\",\"value\":\"ios\"},\"value\":\"ios-hello\"}"
RULE2_BODY="{\"flag\":\"$F2\",\"priority\":10,\"condition\":{\"field\":\"appVersion\",\"op\":\">=\",\"value\":\"2.0.0\"},\"value\":20}"
[ "$(su_post "$C/rules/records" "$RULE1_BODY" /tmp/cw-t17-r1.json)" = "200" ] \
  && pass "rule platform==ios" || { fail "rule platform"; exit 1; }
[ "$(su_post "$C/rules/records" "$RULE2_BODY" /tmp/cw-t17-r2.json)" = "200" ] \
  && pass "rule appVersion>=2.0.0" || { fail "rule semver"; exit 1; }

EXP_BODY="{\"name\":\"e2e-exp\",\"flag\":\"$F3\",\"seed\":\"e2e-seed-1\",\"status\":\"running\",\"variants\":[{\"name\":\"control\",\"weightBps\":5000,\"values\":{\"exp_bool\":false}},{\"name\":\"treatment\",\"weightBps\":5000,\"values\":{\"exp_bool\":true}}]}"
[ "$(su_post "$C/experiments/records" "$EXP_BODY" /tmp/cw-t17-exp.json)" = "200" ] \
  && pass "experiment running 50/50" || { fail "experiment"; exit 1; }

PREFIX="${SDK_KEY:0:8}"
HASH="$(python3 -c "import hashlib; print(hashlib.sha256('$SDK_KEY'.encode()).hexdigest())")"
SDK_KEY_BODY="{\"prefix\":\"$PREFIX\",\"hash\":\"$HASH\",\"env\":\"$ENVID\",\"revoked\":false,\"rateLimit\":100000}"
[ "$(su_post "$C/sdk_keys/records" "$SDK_KEY_BODY" /tmp/cw-t17-key.json)" = "200" ] \
  && pass "sdk key (prefix=first8+sha256)" || { fail "sdk key"; exit 1; }

CODE="$(su_post "$BASE_URL/api/v1/admin/env/e2e/publish" '{"note":"t17 v1","baseVersion":0}' /tmp/cw-t17-pub1.json)"
[ "$CODE" = "200" ] && pass "publish v1 200" || { fail "publish v1 (code $CODE)"; exit 1; }
python3 -c "import json,sys; d=json.load(open('/tmp/cw-t17-pub1.json')); sys.exit(0 if d.get('version')==1 else 1)" \
  && pass "publish v1 version==1" || fail "publish v1 version==1"
ETAG_V1="$(python3 -c "import json; print(json.load(open('/tmp/cw-t17-pub1.json'))['etag'])")"
echo "v1 etag=$ETAG_V1"

# ---- STAGE 2: fetch typed values + variant ----------------------------------
echo "--- STAGE 2: fetch platform=ios appVersion=2.1.0 ---"
FQ="uid=$E2E_UID&platform=ios&appVersion=2.1.0"
CODE1=$(curl -s -o /tmp/cw-t17-fetch1.json -w "%{http_code}" --max-time 10 \
  "$BASE_URL/api/v1/env/e2e/config?$FQ" -H "X-ConfigWire-Key: $SDK_KEY" 2>/dev/null || echo "000")
CODE2=$(curl -s -o /tmp/cw-t17-fetch2.json -w "%{http_code}" --max-time 10 \
  "$BASE_URL/api/v1/env/e2e/config?$FQ" -H "X-ConfigWire-Key: $SDK_KEY" 2>/dev/null || echo "000")
[ "$CODE1" = "200" ] && [ "$CODE2" = "200" ] && pass "fetch 200 x2" || fail "fetch 200 x2 ($CODE1/$CODE2)"
python3 - <<'EOF' && pass "typed values EXACT (string/number/bool/json)" || fail "typed values EXACT"
import json, sys
d = json.load(open('/tmp/cw-t17-fetch1.json'))
v = d.get('values', {})
exp = json.load(open('e2e/fixtures/expected.json'))['stage2_fetch']
ok = (v.get('welcome_text') == exp['welcome_text']
      and v.get('max_items') == exp['max_items']
      and v.get('home_config') == exp['home_config'])
sys.exit(0 if ok else 1)
EOF
python3 - <<'EOF' && pass "variant present + arm-consistent + sticky" || fail "variant arm/sticky"
import json, sys
a = json.load(open('/tmp/cw-t17-fetch1.json'))
b = json.load(open('/tmp/cw-t17-fetch2.json'))
va, vb = a.get('variants', {}), b.get('variants', {})
exp = json.load(open('e2e/fixtures/expected.json'))['stage2_fetch']['exp_bool_values_by_variant']
arm = va.get('exp_bool')
ok = (arm in ('control', 'treatment')
      and vb.get('exp_bool') == arm                      # sticky across fetches
      and a['values'].get('exp_bool') is exp[arm]         # value matches arm
      and b['values'].get('exp_bool') is exp[arm])
sys.exit(0 if ok else 1)
EOF
cp /tmp/cw-t17-fetch1.json /tmp/cw-t17-fetch-stage2.json

# ---- STAGE 3: exposures via T9 route + stats --------------------------------
echo "--- STAGE 3: 1 fetch + 3 exposures -> stats ---"
EV='{"events":[{"kind":"fetch","flag":"exp_bool","variant":"","userHash":"aaaabbbbccccdddd"},{"kind":"exposure","flag":"exp_bool","variant":"control","userHash":"aaaabbbbccccdddd"},{"kind":"exposure","flag":"exp_bool","variant":"control","userHash":"bbbbccccddddeeee"},{"kind":"exposure","flag":"exp_bool","variant":"treatment","userHash":"ccccddddeeeeffff"}]}'
CODE=$(curl -s -o /tmp/cw-t17-ev.json -w "%{http_code}" --max-time 10 -X POST \
  "$BASE_URL/api/v1/env/e2e/events" -H "X-ConfigWire-Key: $SDK_KEY" \
  -H "Content-Type: application/json" -d "$EV" 2>/dev/null || echo "000")
[ "$CODE" = "202" ] && pass "ingest 202 (1 fetch + 3 exposures)" || fail "ingest (code $CODE)"
sleep 3  # batcher flush lag (~1s, T9 note)
CODE=$(curl -s -o /tmp/cw-t17-stats.json -w "%{http_code}" --max-time 10 \
  "$BASE_URL/api/v1/admin/env/e2e/stats?flag=exp_bool&since=7d" \
  -H "Authorization: $TOKEN" 2>/dev/null || echo "000")
[ "$CODE" = "200" ] && pass "stats 200" || fail "stats (code $CODE)"
python3 - <<'EOF' && pass "stats exact {fetches:1, exposures:3, perVariant}" || fail "stats exact"
import json, sys
d = json.load(open('/tmp/cw-t17-stats.json'))
exp = json.load(open('e2e/fixtures/expected.json'))['stage3_stats']
ok = (d.get('fetches') == exp['fetches']
      and d.get('exposures') == exp['exposures']
      and d.get('perVariant') == exp['perVariant'])
sys.exit(0 if ok else 1)
EOF
python3 - <<'EOF' && pass "stats new keys {flagFound,echo,total,rates,sources,approximate:false}" || fail "stats new keys"
import json, sys
d = json.load(open('/tmp/cw-t17-stats.json'))
exp = json.load(open('e2e/fixtures/expected.json'))['stage3_stats']
echo = d.get('echo', {})
rates = d.get('rates', {})
sources = d.get('sources', {})
expecho = exp['echo']
exprates = exp['rates']
expsources = exp['sources']
ok = (d.get('flagFound') is True
      and d.get('total') == exp['total'] == d.get('fetches') + d.get('exposures')
      and echo.get('env') == expecho['env']
      and echo.get('flag') == expecho['flag']
      and echo.get('since') == expecho['since']
      and echo.get('sinceDays') == expecho['sinceDays']
      and echo.get('horizon') == expecho['since']
      and isinstance(echo.get('cutoff'), str) and len(echo.get('cutoff')) > 0
      and isinstance(echo.get('rollupHorizon'), str) and len(echo.get('rollupHorizon')) > 0
      and d.get('approximate') is False
      and sources.get('events') == expsources['events']
      and sources.get('rollups') == expsources['rollups']
      and abs(rates.get('control', -1) - exprates['control']) < 1e-9
      and abs(rates.get('treatment', -1) - exprates['treatment']) < 1e-9)
sys.exit(0 if ok else 1)
EOF

# ---- STAGE 3F: failure asserts (unknown flag zeros + malformed since 400) ---
echo "--- STAGE 3F: unknown flag 200-zeros + malformed since 400 ---"
CODE=$(curl -s -o /tmp/cw-t17-stats-unknown.json -w "%{http_code}" --max-time 10 \
  "$BASE_URL/api/v1/admin/env/e2e/stats?flag=no-such-flag-xyz&since=7d" \
  -H "Authorization: $TOKEN" 2>/dev/null || echo "000")
[ "$CODE" = "200" ] && pass "stats unknown flag 200" || fail "stats unknown flag (code $CODE)"
python3 - <<'EOF' && pass "stats unknown flag zeros + flagFound:false" || fail "stats unknown flag zeros"
import json, sys
d = json.load(open('/tmp/cw-t17-stats-unknown.json'))
ok = (d.get('fetches') == 0
      and d.get('exposures') == 0
      and d.get('perVariant') == {}
      and d.get('flagFound') is False
      and d.get('total') == 0
      and d.get('approximate') is False)
sys.exit(0 if ok else 1)
EOF
CODE=$(curl -s -o /tmp/cw-t17-stats-badsince.json -w "%{http_code}" --max-time 10 \
  "$BASE_URL/api/v1/admin/env/e2e/stats?flag=exp_bool&since=abc" \
  -H "Authorization: $TOKEN" 2>/dev/null || echo "000")
[ "$CODE" = "400" ] && pass "stats malformed since 400" || fail "stats malformed since (code $CODE)"

# ---- STAGE 3B: purge -> stats (backdated seed + dry parity + live purge) -----
echo "--- STAGE 3B: seed ~35d-old events -> purge -> 90d vs 7d ---"
OLD_TS="$(python3 -c "from datetime import datetime,timedelta,timezone; print((datetime.now(timezone.utc)-timedelta(days=35)).strftime('%Y-%m-%d %H:%M:%S.000Z'))")"
mkold() { # kind variant userHash outfile
  su_post "$C/events/records" "{\"env\":\"$ENVID\",\"flag\":\"$F3\",\"kind\":\"$1\",\"variant\":\"$2\",\"userHash\":\"$3\",\"ts\":\"$OLD_TS\"}" "$4"
}
[ "$(mkold fetch "" oldfetch01aaaabbbb /tmp/cw-t17-old1.json)" = "200" ] \
  && pass "seed old fetch 1/2 (~35d)" || fail "seed old fetch 1/2"
[ "$(mkold fetch "" oldfetch02aaaabbbb /tmp/cw-t17-old2.json)" = "200" ] \
  && pass "seed old fetch 2/2 (~35d)" || fail "seed old fetch 2/2"
[ "$(mkold exposure control oldexp01aaaabbbb /tmp/cw-t17-old3.json)" = "200" ] \
  && pass "seed old exposure control (~35d)" || fail "seed old exposure"
CODE="$(su_post "$BASE_URL/api/v1/admin/maintenance/purge?dry=1" '{}' /tmp/cw-t17-purge-dry.json)"
[ "$CODE" = "200" ] && pass "purge dry 200" || fail "purge dry (code $CODE)"
python3 - <<'EOF' && pass "purge dry parity {deleted:3, dry:true}" || fail "purge dry parity"
import json, sys
d = json.load(open('/tmp/cw-t17-purge-dry.json'))
exp = json.load(open('e2e/fixtures/expected.json'))['stage3b_purge']
ok = (d.get('deleted') == exp['dryDeleted'] and d.get('dry') is True)
sys.exit(0 if ok else 1)
EOF
CODE="$(su_post "$BASE_URL/api/v1/admin/maintenance/purge" '{}' /tmp/cw-t17-purge-live.json)"
[ "$CODE" = "200" ] && pass "purge live 200" || fail "purge live (code $CODE)"
python3 - <<'EOF' && pass "purge live {deleted:3, dry:false}" || fail "purge live counts"
import json, sys
d = json.load(open('/tmp/cw-t17-purge-live.json'))
exp = json.load(open('e2e/fixtures/expected.json'))['stage3b_purge']
ok = (d.get('deleted') == exp['liveDeleted'] and d.get('dry') is False)
sys.exit(0 if ok else 1)
EOF
CODE=$(curl -s -o /tmp/cw-t17-stats-90d.json -w "%{http_code}" --max-time 10 \
  "$BASE_URL/api/v1/admin/env/e2e/stats?flag=exp_bool&since=90d" \
  -H "Authorization: $TOKEN" 2>/dev/null || echo "000")
[ "$CODE" = "200" ] && pass "stats 90d 200" || fail "stats 90d (code $CODE)"
python3 - <<'EOF' && pass "stats 90d rolled-up {fetches:3, exposures:4, approximate:true}" || fail "stats 90d rolled-up"
import json, sys
d = json.load(open('/tmp/cw-t17-stats-90d.json'))
exp = json.load(open('e2e/fixtures/expected.json'))['stage3b_purge']['stats90d']
echo = d.get('echo', {})
sources = d.get('sources', {})
ok = (d.get('fetches') == exp['fetches']
      and d.get('exposures') == exp['exposures']
      and d.get('perVariant') == exp['perVariant']
      and d.get('total') == exp['total']
      and d.get('flagFound') is True
      and d.get('approximate') is True
      and sources.get('events') == exp['sources']['events']
      and sources.get('rollups') == exp['sources']['rollups'] > 0
      and echo.get('sinceDays') == exp['echo']['sinceDays']
      and echo.get('since') == exp['echo']['since'])
sys.exit(0 if ok else 1)
EOF
CODE=$(curl -s -o /tmp/cw-t17-stats-7d.json -w "%{http_code}" --max-time 10 \
  "$BASE_URL/api/v1/admin/env/e2e/stats?flag=exp_bool&since=7d" \
  -H "Authorization: $TOKEN" 2>/dev/null || echo "000")
[ "$CODE" = "200" ] && pass "stats 7d 200 (post-purge)" || fail "stats 7d post-purge (code $CODE)"
python3 - <<'EOF' && pass "stats 7d excludes history {approximate:false, rollups:0}" || fail "stats 7d excludes history"
import json, sys
d = json.load(open('/tmp/cw-t17-stats-7d.json'))
exp = json.load(open('e2e/fixtures/expected.json'))['stage3b_purge']['stats7d']
sources = d.get('sources', {})
ok = (d.get('fetches') == exp['fetches']
      and d.get('exposures') == exp['exposures']
      and d.get('perVariant') == exp['perVariant']
      and d.get('total') == exp['total']
      and d.get('approximate') is False
      and sources.get('events') == exp['sources']['events']
      and sources.get('rollups') == exp['sources']['rollups'])
sys.exit(0 if ok else 1)
EOF

# ---- STAGE 4: publish v2 breaking change ------------------------------------
echo "--- STAGE 4: flip home_config default -> publish v2 ---"
CODE="$(su_patch "$C/flags/records/$F4" '{"defaultValue":{"a":2}}' /tmp/cw-t17-patch.json)"
[ "$CODE" = "200" ] && pass "flag PATCH default" || { fail "flag PATCH (code $CODE)"; exit 1; }
CODE="$(su_post "$BASE_URL/api/v1/admin/env/e2e/publish" '{"note":"t17 v2 breaking","baseVersion":1}' /tmp/cw-t17-pub2.json)"
[ "$CODE" = "200" ] && pass "publish v2 200" || { fail "publish v2 (code $CODE)"; exit 1; }
python3 -c "import json,sys; sys.exit(0 if json.load(open('/tmp/cw-t17-pub2.json')).get('version')==2 else 1)" \
  && pass "publish v2 version==2" || fail "publish v2 version==2"
curl -s -o /tmp/cw-t17-fetch-v2.json --max-time 10 \
  "$BASE_URL/api/v1/env/e2e/config?$FQ" -H "X-ConfigWire-Key: $SDK_KEY" 2>/dev/null || true
python3 - <<'EOF' && pass "fetch v2: new json value + rules intact" || fail "fetch v2 values"
import json, sys
d = json.load(open('/tmp/cw-t17-fetch-v2.json'))
v = d.get('values', {})
exp = json.load(open('e2e/fixtures/expected.json'))['stage4_fetch']
ok = (d.get('version') == exp['version']
      and v.get('home_config') == exp['home_config']
      and v.get('welcome_text') == exp['welcome_text']
      and v.get('max_items') == exp['max_items'])
sys.exit(0 if ok else 1)
EOF

# ---- STAGE 5: rollback v1 ----------------------------------------------------
echo "--- STAGE 5: rollback v1 -> version 3, fresh etag, values restored ---"
CODE="$(su_post "$BASE_URL/api/v1/admin/releases/1/rollback" '{"note":"t17 rollback to v1"}' /tmp/cw-t17-rb.json)"
[ "$CODE" = "200" ] && pass "rollback 200" || { fail "rollback (code $CODE)"; exit 1; }
python3 - <<EOF && pass "rollback version==3 AND etag!=v1 etag" || fail "rollback version/etag"
import json, sys
d = json.load(open('/tmp/cw-t17-rb.json'))
sys.exit(0 if (d.get('version') == 3 and d.get('etag') != '$ETAG_V1') else 1)
EOF
curl -s -o /tmp/cw-t17-fetch-v3.json --max-time 10 \
  "$BASE_URL/api/v1/env/e2e/config?$FQ" -H "X-ConfigWire-Key: $SDK_KEY" 2>/dev/null || true
python3 - <<'EOF' && pass "fetch v3: v1 values byte-equal + version 3" || fail "fetch v3 restored"
import json, sys
old = json.load(open('/tmp/cw-t17-fetch-stage2.json'))
new = json.load(open('/tmp/cw-t17-fetch-v3.json'))
exp = json.load(open('e2e/fixtures/expected.json'))['stage5_rollback']
keys = ('welcome_text', 'max_items', 'home_config', 'exp_bool')
ok = (new.get('version') == exp['version']
      and all(new['values'].get(k) == old['values'].get(k) for k in keys)
      and new['values'].get('home_config') == exp['home_config']
      and new.get('variants', {}).get('exp_bool') == old.get('variants', {}).get('exp_bool'))
sys.exit(0 if ok else 1)
EOF

# ---- STAGE 6 (negative): dead port fails cleanly ------------------------------
echo "--- STAGE 6 (negative): fetch against dead port ---"
NEG_OUT="$(curl -s -o /tmp/cw-t17-neg.json -w "HTTP %{http_code}" --max-time 5 \
  "http://127.0.0.1:$DEAD_PORT/api/v1/env/e2e/config?$FQ" -H "X-ConfigWire-Key: $SDK_KEY" 2>&1)"
NEG_EXIT=$?
if [ "$NEG_EXIT" -ne 0 ]; then
  echo "NEGATIVE OK: fetch against dead port 127.0.0.1:$DEAD_PORT failed cleanly (curl exit $NEG_EXIT) — is the server down? start it and retry."
  if grep -qi "panic\|Traceback" /tmp/cw-t17-neg.json 2>/dev/null; then
    fail "negative: no panic/trace in output"
  else
    pass "negative: clean failure, no panic/trace"
  fi
else
  fail "negative: dead-port fetch unexpectedly succeeded ($NEG_OUT)"
fi

# ---- summary -----------------------------------------------------------------
echo "=== T17 e2e summary: PASS=$PASS FAIL=$FAIL ==="
rm -f /tmp/cw-t17-*.json /tmp/cw-t17-*.log
if [ "$FAIL" -gt 0 ]; then echo "E2E RESULT: FAIL"; exit 1; fi
echo "E2E RESULT: PASS"

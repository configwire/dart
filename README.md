![ConfigWire — Real-Time Remote Configuration](server/pb_public/img/banner.png)

# ConfigWire

Single-binary remote config: publish immutable releases, evaluate flags
per SDK fetch, ingest fetch/exposure events, query stats, stream updates
over SSE, purge old events on a retention schedule. Admin UI ships as
static files; Dart client is pure-Dart (no Flutter) and works on Flutter;
persistence is bring-your-own CacheStore.

Wire details live in `docs/CONTRACT.md`. Operator security notes live in
`docs/SECURITY.md`.

## Prereqs

- Go (version floor per `server/go.mod`)
- Dart SDK `>=3.12.0` (pure-Dart client in `client/configwire`,
  Flutter-compatible; persistence is bring-your-own CacheStore)
- `make`, `bash`, `curl`, `python3`

## Serving (cwd rule)

> Serve MUST run from `server/` so `./pb_public` resolves. Started from
> anywhere else, `/` answers 404 while `/hello` stays 200. `make serve`
> handles this with `cd server`.

```bash
make serve
```

That is shorthand for `cd server && go run . serve` (default
`127.0.0.1:8090`, data in `server/pb_data`). Flags pass through Go,
not make: `cd server && go run . serve --http 127.0.0.1:8109 --dir /tmp/cw-pbdata`.

## Quickstart (scratch run, port 8109)

Fresh temp dir, backgrounded server, superuser to fetch to stats.
Copy-paste verbatim; every command below was executed in order for the
T18 proof (log: `.omo/evidence/task-18-configwire.log`).

```bash
mkdir -p /tmp/cw-qs && cd server
go run . superuser upsert admin@example.com password123 --dir /tmp/cw-qs/pbdata
(go run . serve --http 127.0.0.1:8109 --dir /tmp/cw-qs/pbdata > /tmp/cw-qs/serve.log 2>&1 &)
sleep 12
curl -s http://127.0.0.1:8109/hello
TOKEN=$(curl -s -X POST http://127.0.0.1:8109/api/collections/_superusers/auth-with-password -H 'Content-Type: application/json' -d '{"identity":"admin@example.com","password":"password123"}' | python3 -c 'import json,sys; print(json.load(sys.stdin)["token"])')
PROJ=$(curl -s -X POST http://127.0.0.1:8109/api/collections/projects/records -H "Authorization: $TOKEN" -H 'Content-Type: application/json' -d '{"name":"demo"}' | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')
ENV=$(curl -s -X POST http://127.0.0.1:8109/api/collections/environments/records -H "Authorization: $TOKEN" -H 'Content-Type: application/json' -d "{\"project\":\"$PROJ\",\"slug\":\"dev\"}" | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')
curl -s -X POST http://127.0.0.1:8109/api/collections/flags/records -H "Authorization: $TOKEN" -H 'Content-Type: application/json' -d "{\"project\":\"$PROJ\",\"key\":\"launch_flag\",\"type\":\"bool\",\"defaultValue\":false}"
KEY=cw-qs-demo-key-001
python3 -c 'import hashlib; print(hashlib.sha256(b"cw-qs-demo-key-001").hexdigest())'
curl -s -X POST http://127.0.0.1:8109/api/collections/sdk_keys/records -H "Authorization: $TOKEN" -H 'Content-Type: application/json' -d "{\"prefix\":\"cw-qs-de\",\"hash\":\"<sha256-of-key>\",\"env\":\"$ENV\",\"rateLimit\":100000}"
curl -s -X POST http://127.0.0.1:8109/api/v1/admin/env/dev/publish -H "Authorization: $TOKEN" -H 'Content-Type: application/json' -d '{"note":"first release","baseVersion":0}'
curl -s http://127.0.0.1:8109/api/v1/env/dev/config -H 'X-ConfigWire-Key: cw-qs-demo-key-001'
curl -s -X POST http://127.0.0.1:8109/api/v1/env/dev/events -H 'X-ConfigWire-Key: cw-qs-demo-key-001' -H 'Content-Type: application/json' -d '{"events":[{"kind":"fetch","flag":"launch_flag"},{"kind":"exposure","flag":"launch_flag","variant":"control","userHash":"abc123"}]}'
sleep 3
curl -s "http://127.0.0.1:8109/api/v1/admin/env/dev/stats?flag=launch_flag&since=7d" -H "Authorization: $TOKEN"
kill $(lsof -ti:8109)
```

Replace `<sha256-of-key>` with the hash printed two steps earlier.
Expected tail: publish `{"version":1,...}`, fetch `{"version":1,...}`,
events `{"accepted":2,...}`, stats `{"fetches":1,"exposures":1,...,"flagFound":true,"total":2,"rates":{"control":1},"sources":{"events":2,"rollups":0},"approximate":false,...}` (full shape in `docs/CONTRACT.md` section 5).

Cleanup: `kill $(lsof -ti:8109)` (receipt: `curl` refuses + `lsof`
empty), then `rm -rf /tmp/cw-qs`.

## SDK fetch

```bash
curl -s 'http://127.0.0.1:8090/api/v1/env/dev/config?uid=user-7&platform=ios' -H 'X-ConfigWire-Key: <sdk-key>'
```

```json
{"version": 1, "etag": "b41b62605c0df712", "values": {"launch_flag": true}, "variants": {}, "fetchAt": "2026-09-22T00:00:00.000000000Z"}
```

Conditional refresh: send `If-None-Match: <etag>`; unchanged config
answers `304` empty.

## Dart snippet

```dart
import 'package:configwire/configwire.dart';

final cw = ConfigWire(
  apiKey: 'YOUR_SDK_KEY', // sent as X-ConfigWire-Key, never printed
  env: 'dev',
  baseUrl: 'http://127.0.0.1:8090',
  defaults: {'launch_flag': false},
  // Omit store: for session-only memory cache, or pass your own
  // CacheStore for disk.
);
await cw.ensureInitialized();
await cw.fetchAndActivate();
final on = cw.getBool('launch_flag');
await cw.dispose();
```

Realtime: `cw.connectRealtime()` opens SSE plus a 15min poll fallback,
so freshness is at most `pollInterval` plus one fetch on every path.
See `client/configwire/example/main.dart` for a runnable demo.

## Ports

| Use | Port |
| --- | ---- |
| Default serve (`make serve`) | 8090 |
| README quickstart proof | 8109 |
| T17 e2e (`scripts/e2e.sh`) | 8108 (owned by T17, do not reuse) |

## Retention and scale defaults

- Raw `events`: 30 days, then rolled into `event_daily` and deleted.
- `event_daily` rollups `(day, env, flag, variant)`: 90 days.
- Stats stays rollup-backed to 90d: windows past the 30d horizon merge
  `event_daily` buckets and answer `approximate:true` (with
  `sources:{events,rollups}` splitting the merge); events-only windows
  stay exact (`approximate:false`). See `docs/CONTRACT.md` section 5.
- Manual/dry-run: `POST /api/v1/admin/maintenance/purge?dry=1`.
- Ingest rate: 60 req/min per key default (`sdk_keys.rateLimit`).
- Ingest lag bound: about 1s plus write (bounded 2s); buffer cap 2048
  (full then `503`, never silent drop).
- Load gate (k6, `scripts/k6-fetch.js`): 60s at 100rps fetch plus
  50rps exposure, fetch p95 0.78ms (< 200ms budget), 0.00% failed
  (< 1% budget). See `docs/SECURITY.md` section 9.

## Make targets

| Target | Runs |
| ------ | ---- |
| `make serve` | `cd server && go run . serve` |
| `make migrate ARGS="up"` | `cd server && go run . migrate <args>` |
| `make test` | Go build+vet+test plus `dart analyze`+`dart test` (exit 0) |
| `make test-chrome` | `dart test -p chrome` core smoke (needs Chrome; VM suites stay hermetic) |
| `make lint` | `gofmt` check + `go vet` + `dart analyze` |
| `make e2e` | `bash scripts/e2e.sh` (T17 owns that script; reference only) |

CI (`.github/workflows/ci.yml`) runs Go build/vet/test plus Dart
analyze/test on push/PR, including `make lint` and `make test`.
First run needs network for `go mod download` / `dart pub get`;
after priming, `make test` needs nothing beyond module/pub caches.

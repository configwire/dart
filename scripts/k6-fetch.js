// ConfigWire load + security gate (plan todo 16).
//
// k6 scenario: 100rps SDK fetch + 50rps exposure ingest, sustained 60s.
// Thresholds: fetch p95 < 200ms, overall http_req_failed < 1%.
//
// RUNNER: k6 v2.3.0 (brew). No hey fallback shipped — k6 was installed
// and is the recorded runner for the gate evidence log.
// If k6 is ever absent, an equivalent `hey`-based probe must hold the
// same targets (100rps fetch / 50rps exposure / 60s / same thresholds).
//
// Usage:
//   GATE_KEY=<sdk-key> k6 run --summary-export=/tmp/cw-t16-summary.json scripts/k6-fetch.js
//   BASE (optional): base URL, default http://127.0.0.1:8106
//   ENV_SLUG (optional): default dev
//
// Duration is hard-bounded at 60s (+30s gracefulStop) so a hung server
// cannot hang the gate; threshold verdicts are parsed from the JSON
// summary programmatically (see task evidence log), never eyeballed.
import http from 'k6/http';
import { check } from 'k6';
import { Trend, Rate } from 'k6/metrics';

const BASE = __ENV.BASE || 'http://127.0.0.1:8106';
const ENV_SLUG = __ENV.ENV_SLUG || 'dev';
const KEY = __ENV.GATE_KEY;
if (!KEY) {
  throw new Error('GATE_KEY env is required (X-ConfigWire-Key value)');
}

const fetchTrend = new Trend('fetch_duration', true);
const exposureTrend = new Trend('exposure_duration', true);
const fetchFail = new Rate('fetch_failed');
const exposureFail = new Rate('exposure_failed');

export const options = {
  scenarios: {
    fetch: {
      executor: 'constant-arrival-rate',
      rate: 100, // 100rps
      timeUnit: '1s',
      duration: '60s',
      preAllocatedVUs: 20,
      maxVUs: 100,
      exec: 'fetchFn',
    },
    exposure: {
      executor: 'constant-arrival-rate',
      rate: 50, // 50rps
      timeUnit: '1s',
      duration: '60s',
      preAllocatedVUs: 10,
      maxVUs: 50,
      exec: 'exposureFn',
    },
  },
  thresholds: {
    // p95 of the SDK fetch path only (not blended with ingest).
    fetch_duration: ['p(95)<200'],
    // Global failure budget across both scenarios.
    http_req_failed: ['rate<0.01'],
  },
  gracefulStop: '30s',
};

const headers = { 'X-ConfigWire-Key': KEY };

export function fetchFn() {
  const uid = `gate-user-${__VU}-${__ITER}`;
  const res = http.get(
    `${BASE}/api/v1/env/${ENV_SLUG}/config?uid=${uid}&platform=ios`,
    { headers, tags: { kind: 'fetch' } },
  );
  fetchTrend.add(res.timings.duration);
  fetchFail.add(res.status !== 200);
  check(res, {
    'fetch status 200': (r) => r.status === 200,
    'fetch has values': (r) => {
      try {
        return r.json('values') !== undefined;
      } catch (e) {
        return false;
      }
    },
  });
}

export function exposureFn() {
  const body = JSON.stringify({
    events: [
      {
        kind: 'exposure',
        flag: 'hero_button',
        variant: 'control',
        userHash: `gate-hash-${__VU}-${__ITER}`,
      },
    ],
  });
  const res = http.post(`${BASE}/api/v1/env/${ENV_SLUG}/events`, body, {
    headers: Object.assign({ 'Content-Type': 'application/json' }, headers),
    tags: { kind: 'exposure' },
  });
  exposureTrend.add(res.timings.duration);
  exposureFail.add(res.status !== 202);
  check(res, {
    'exposure status 202': (r) => r.status === 202,
  });
}

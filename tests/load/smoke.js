// k6 smoke load test (S-175 stand-in, docs/test/load-test-plan.md). Synthetic data and fake
// vendors only; never point this at production. Run through tools/ci/load-smoke.sh.
//
// Scenarios (plan section 3):
//   L1 search        POST /v1/practices/search            p95 < 300 ms
//   L2 slots         GET  /v1/practices/{id}/slots        p95 < 300 ms
//   L5 reads         GET  /v1/me/consents (records gate)  p95 < 300 ms
//   L3 one slot      N patients hold the same slot at once: exactly 1 wins, the rest 409, no 5xx
//   L11 OTP flood    one address asks codes for many numbers: throttled, no 5xx
import http from 'k6/http';
import { check } from 'k6';
import { Counter } from 'k6/metrics';
import exec from 'k6/execution';

const data = JSON.parse(open('/data/load.json'));
const BASE = __ENV.TARGET || 'http://mugizh-dast-target:18081';
const RATE = parseInt(__ENV.RATE || '40', 10); // requests per second per read scenario
const DURATION = __ENV.DURATION || '60s';
// Pack values for the target's country pack (IN by default); never constants in product code (K-GLB-01).
const ZONE = __ENV.PACK_ZONE || 'Asia/Kolkata';
const CC = __ENV.PACK_CC || '+91';

const holdWins = new Counter('l3_hold_wins');
const holdTaken = new Counter('l3_hold_taken');
const holdOther = new Counter('l3_hold_other');
const otpThrottled = new Counter('l11_otp_throttled');
const otpAccepted = new Counter('l11_otp_accepted');
const server5xx = new Counter('server_5xx');

function uuid() {
  const h = '0123456789abcdef';
  let s = '';
  for (let i = 0; i < 32; i++) s += h[Math.floor(Math.random() * 16)];
  return `${s.slice(0, 8)}-${s.slice(8, 12)}-7${s.slice(13, 16)}-8${s.slice(17, 20)}-${s.slice(20, 32)}`;
}
function headers(token, post) {
  const h = { 'Content-Type': 'application/json' };
  if (token) h.Authorization = `Bearer ${token}`;
  if (post) h['Idempotency-Key'] = uuid();
  return { headers: h };
}
function count5xx(r) { if (r.status >= 500) server5xx.add(1); }

export const options = {
  scenarios: {
    l1_search: { executor: 'constant-arrival-rate', rate: RATE, timeUnit: '1s', duration: DURATION, preAllocatedVUs: 50, exec: 'search' },
    l2_slots: { executor: 'constant-arrival-rate', rate: RATE, timeUnit: '1s', duration: DURATION, preAllocatedVUs: 50, exec: 'slots' },
    l5_reads: { executor: 'constant-arrival-rate', rate: RATE, timeUnit: '1s', duration: DURATION, preAllocatedVUs: 50, exec: 'reads' },
    l3_one_slot: { executor: 'per-vu-iterations', vus: data.patients.length, iterations: 1, startTime: '5s', exec: 'holdSameSlot' },
    l11_otp_flood: { executor: 'shared-iterations', vus: 20, iterations: 300, startTime: '10s', exec: 'otpFlood' },
  },
  thresholds: {
    'http_req_duration{scenario:l1_search}': ['p(95)<300'],
    'http_req_duration{scenario:l2_slots}': ['p(95)<300'],
    'http_req_duration{scenario:l5_reads}': ['p(95)<300'],
    l3_hold_wins: ['count==1'],
    l3_hold_other: ['count==0'],
    l11_otp_accepted: ['count<=10'],
    server_5xx: ['count==0'],
  },
};

export function search() {
  const r = http.post(`${BASE}/v1/practices/search`, JSON.stringify({ location: { lat: data.lat, lng: data.lng }, mode: 'video' }), headers('', true));
  count5xx(r);
  check(r, { 'search 200': (x) => x.status === 200 });
}

export function slots() {
  const t = data.patients[Math.floor(Math.random() * data.patients.length)];
  const r = http.get(`${BASE}/v1/practices/${data.practice}/slots`, headers(t, false));
  count5xx(r);
  check(r, { 'slots 200': (x) => x.status === 200 });
}

export function reads() {
  const t = data.patients[Math.floor(Math.random() * data.patients.length)];
  const r = http.get(`${BASE}/v1/me/consents`, headers(t, false));
  count5xx(r);
  check(r, { 'consents 200': (x) => x.status === 200 });
}

export function holdSameSlot() {
  const t = data.patients[exec.vu.idInTest % data.patients.length];
  const r = http.post(`${BASE}/v1/practices/${data.practice}/holds`, JSON.stringify({ slotId: data.slot, mode: 'video',
    patientTimeZone: ZONE, patientInIndiaConfirmed: true }), headers(t, true));
  count5xx(r);
  if (r.status === 201) holdWins.add(1);
  else if (r.status === 409) holdTaken.add(1);
  else holdOther.add(1);
}

export function otpFlood() {
  const n = String(7000000000 + Math.floor(Math.random() * 999999999));
  const r = http.post(`${BASE}/v1/auth/otp-challenges`, JSON.stringify({ phone: `${CC}${n}`, app: 'patient' }), headers('', true));
  count5xx(r);
  if (r.status === 429) otpThrottled.add(1);
  else if (r.status === 202) otpAccepted.add(1);
}

# k6 smoke load test, 2026-11-23 (S-175 stand-in)

**Story:** S-175 · **Plan:** [load-test-plan.md](load-test-plan.md) · **Edge cases:** E-BK-01 (TC-22), TC-55
**Status:** In Testing. This is a smoke run on a laptop. The launch-profile run (load-test-plan.md section 2) belongs on staging (S-093), with production-sized synthetic data.

## Set-up

- **Command:** `task load:smoke` (`tools/ci/load-smoke.sh`) with RATE=40 and DURATION=60s.
- **Tools:** `grafana/k6:0.54.0` and `tests/load/smoke.js`.
- **Target:** the whole Core API assembled by `backend/internal/app/load_test.go` (build tag `load`), on a test PostgreSQL 17 + PostGIS 3.5 database with the fake vendors.
- **Data:** one verified doctor with a video practice and 100 signed-in synthetic patients, each with P-ACCOUNT and P-CARE.
- **Machine caveat:** a Windows laptop. Docker, the database, the API and k6 all share it, so the numbers are a smoke signal, not capacity evidence.
- **Rate limits:** the target's per-IP and per-user limits are set high so that one k6 address is not throttled. The per-IP OTP limit (L11) is the real one.

## Result: every threshold met

| Scenario | Threshold | Result |
|---|---|---|
| L1 radius search (40/s for 60 s) | p95 < 300 ms | **p95 16.7 ms** (max 278 ms) |
| L2 slots (40/s) | p95 < 300 ms | **p95 21.7 ms** (max 489 ms) |
| L5 reads through the records gate (40/s) | p95 < 300 ms | **p95 16.7 ms** (max 313 ms) |
| L3 100 patients hold the same slot at once | exactly 1 wins, the rest 409, no errors | **1 win, 99 × 409, 0 other** (TC-22) |
| L11 OTP flood from one address to random numbers | throttled, no 5xx | **10 accepted, 290 refused (429)** (TC-55) |
| All | no 5xx | **0 of 7,602 requests** |

126 requests per second overall; checks 100% (7,202 of 7,202).

## Not covered here (open)

- L1 at 50,000 providers over HTTP. The module-level measurement exists: S-067, `TestS067RadiusSearchLoad` with `DIRECTORY_LOAD_50K=1`.
- L4 (hold, pay and webhook with duplicates) under load. The duplicate and delayed-webhook logic is covered by the TC-28, TC-30 and TC-58 tests.
- L6 (signing) is blocked by N5.
- L7 (reminder soak) was measured at module level in S-068; the 24-hour soak is not done.
- L8 (provider outage), L9 (spike), L10 (8-hour soak) and L12 (slow key service).
- All of these at the launch profile on staging.

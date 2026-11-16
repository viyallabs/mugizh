# Performance and reliability targets per service (S-065)

**Phase P07 · Epic EP09 · Depends on:** S-035 · **Date:** 2026-11-15
**Status:** In Testing, until the founder signs off the targets (acceptance criterion: targets agreed). The source of record is the **Performance sheet**. This document explains the targets, adds the ones the designs since S-034 need (Performance sheet rows 24 to 31), and says how each is measured.

**Who this is for:** engineers (S-066 to S-069 prototypes, S-084 load plan, S-175 load tests) and the founder signing off.

## 1. Existing targets (Performance sheet rows 5 to 23), reviewed

| Area | Target | Review |
|---|---|---|
| Radius search | p95 < 300 ms at 50k providers | Keep. Patient location used only for the query (no logging). |
| Slot availability | p95 < 300 ms | Keep |
| Booking concurrency | 0 double bookings; p95 < 500 ms | Keep. Add the hold limit of 2 per account (E-SEC-04). |
| Payment state | 0 lost or duplicated payments | Keep. Now backed by durable operation keys and `findRefund` (API standards §6). |
| Reminder dispatch | 99.9% within 60 s; 1M/day | Keep |
| Notification fallback | No silent loss; retries < 5 min | Keep. Push → SMS for critical items only. |
| Video join | < 5 s; audio fallback | Keep (vendor spike S-039) |
| Rules engine | < 50 ms per prescription | Keep. Covers both layers and checks P01 to P07. **P08 and P09 (step-up, e-signature) are excluded**, because they involve the doctor's action and a provider. |
| Drug search | p95 < 150 ms | Keep |
| Symptom → specialty | Agreed accuracy on 200 cases | **v1.1, and only if F8 option B is chosen.** Option A (recommended) computes nothing from symptoms. |
| Prescription scan | Review within agreed hours | v1.1 |
| Verification | Auto < 1 min; manual < 48 h | Keep. Beta is manual only. |
| Consent check | < 20 ms | Keep. Cache plus synchronous revoke (consent design §4). |
| Audit logging | Updated in S-059: transactional outbox, one insert per request | **Changed.** Was "async, no added latency"; see row 18. |
| Mobile | Cold start < 3 s on low-end Android | Keep. Define the device: 2 GB RAM Android Go class [CONFIRM]. |
| APIs | Read p95 < 300 ms; write p95 < 800 ms; 99.9% monthly | Keep |

## 2. New targets (added to the Performance sheet)

| Service | Target | Why | How measured |
|---|---|---|---|
| **App stability** | Crash-free sessions ≥ 99.5% (staged rollout pauses below it) | Release policy §4 | Crash reporting (scrubbed) |
| **Audit outbox lag** | 99% of events chained within 1 min; alert if any event is older than 5 min | Audit design §4, §7 | Outbox age metric |
| **Recovery objectives** | RPO ≤ 15 min; RTO ≤ 4 h (ADR-27) | Infrastructure §5 | DR drill (S-185) |
| **OTP delivery** | p95 ≤ 30 s from request to delivery [VALIDATE with SMS provider] | Sign-in is the first impression; also an abuse signal | SMS delivery reports |
| **Prescription signing (server side)** | p95 < 2 s from "Sign" to an immutable PDF, excluding the doctor's step-up and e-signature interaction | Faster than paper (persona Dr. Arun) | API trace |
| **Data export** | Generated within 24 h of the request (target: 1 h) | Data-rights flows §3; 15-day legal target | Job metrics |
| **Pro web page load** | Largest contentful paint < 2.5 s on a typical clinic broadband connection | Doctors work in a busy clinic | Synthetic monitoring |
| **Key service** | 99.95% of decrypt calls succeed; fail closed otherwise | Encryption design §7 | KMS metrics |

## 3. How targets are used

1. The **prototypes** prove the hardest targets before build: S-066 booking concurrency, S-067 radius search, S-068 reminders.
2. **k6 load tests** run nightly against each endpoint's target (S-084, S-175).
3. **Production alerts** fire on breach. Error budgets (0.1% a month for 99.9%) decide when to pause feature work for reliability [CONFIRM].
4. The "Measured result" and "Status" columns in the sheet are filled from test results.

## 4. Sign-off

| Item | Founder decision |
|---|---|
| Existing targets as reviewed (section 1) | ☐ Agree ☐ Change: |
| New targets (section 2) | ☐ Agree ☐ Change: |
| Low-end device definition for the cold-start target | |
| Name / date | |

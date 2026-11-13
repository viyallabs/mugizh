# Success Metrics and Analytics Event List (S-005) — DRAFT v0.1 for founder approval

**Phase P00 · Epic EP01 · Depends on:** S-004 (v1.0 requirements)
**Compliance refs:** C01 (DPDP). **Contract refs:** K-DP-04 (no health data or full identifiers
in logs, analytics, crash reports or errors).
**Sources:** feature-roadmap.md v0.3 (v0 and v1.0 exit criteria), Performance sheet.
Targets marked "suggested" come from the roadmap and should be adjusted after the first
conversations. **[CONFIRM]** marks my own proposals.

---

## 1. Measurement principles
1. **Analytics never contains health data or full identifiers** (K-DP-04). See section 5.
2. **Pseudonymous IDs only.** Events use a random `user_key` and `session_key`, never a phone
   number, name, email, ABHA/Aadhaar or registration number.
3. **Counts and categories, not content.** We record *that* a reminder was sent for a
   "medicine dose", never *which* medicine.
4. **Consent first.** Product analytics is a stated purpose in the privacy notice (R-02); events
   fire only for users who have not opted out. **[CONFIRM with lawyer]**
5. **One definition per metric.** Numerator, denominator, window and source are written down,
   so the dashboard and the exit-gate review agree.

---

## 2. v0 exit criteria (concierge test, 4–6 weeks)
v0 is manual, so these are measured from a spreadsheet and WhatsApp, not product events.

| ID | Metric | Definition | Target (suggested) |
|---|---|---|---|
| V0-1 | Doctors committed | Doctors who agree to put prescriptions into a digital format for every patient for 4 weeks | ≥ 10 |
| V0-2 | Reminder engagement at 60 days | Chronic patients still engaging with reminders at day 60 ÷ chronic patients enrolled | ≥ 40% |
| V0-3 | Willingness to pay | Doctors who say they would pay for the tool | ≥ 3 |

"Engaging" must be defined before v0 starts. Proposed: replied to, confirmed or marked a
reminder taken at least once in the last 14 days. **[CONFIRM]**

---

## 3. v1.0 exit criteria and metrics

### 3.1 Exit-gate metrics (from the roadmap, suggested targets)
| ID | Metric | Definition | Target |
|---|---|---|---|
| M-01 | Prescription capture rate | Prescriptions a doctor writes in the product ÷ all prescriptions that doctor writes, per onboarded doctor, averaged across doctors, over a 30-day window | ≥ 70% |
| M-02 | 60-day patient activity | Patients with at least 1 active day in days 53–67 after first booking ÷ patients whose first booking was ≥ 60 days ago | ≥ 35% |
| M-03 | Payment success | Payments ending `succeeded` ÷ payments started (excluding user-cancelled) | ≥ 95% |
| M-04 | Compliance incidents | Count of reportable privacy or safety incidents | 0 |

An "active day" = the patient opened the app, acted on a reminder, booked, or paid. **[CONFIRM]**

**Data caveat for M-01.** The denominator (prescriptions written elsewhere or on paper) is not
visible to us. It must come from a short doctor survey or a sample, not product events. Until
then, report the product-side count only, plus the self-reported share.

### 3.2 North-star metric **[CONFIRM]**
**Plan adherence rate:** reminders acted on (taken, done or confirmed) within the grace window
÷ reminders sent. This is the closest measure of "patients can follow their plan", which is the
product's reason to exist. Report by coarse reminder *type* only (medication, test, appointment), and only as aggregates
across many patients, never per patient.

### 3.3 Supporting metrics
| ID | Area | Metric | Definition |
|---|---|---|---|
| M-10 | Doctor supply | Verified doctors | Count in state `Verified` |
| M-11 | Doctor supply | Verification turnaround | Median and p90 time Submitted → decision; auto checks < 1 min, manual < 48 h |
| M-12 | Doctor activation | Time to first prescription | Days from `Verified` to first signed prescription |
| M-13 | Prescription speed | Time to sign a prescription | Median seconds from prescription started to signed ("faster than paper" proxy) |
| M-14 | Booking | Booking completion | Bookings confirmed ÷ booking flows started |
| M-15 | Booking | Double bookings | Must be 0 |
| M-16 | Consult | Video join success | Consults joined ÷ consults started; join time < 5 s; audio-fallback rate |
| M-17 | Reminders | On-time delivery | Reminders sent within 60 s of due time ÷ reminders due; target 99.9% |
| M-18 | Reminders | Opt-out rate | Patients switching off reminders ÷ patients with reminders, by type |
| M-19 | Channels | WhatsApp reach | Reminders delivered on WhatsApp ÷ reminders sent on WhatsApp |
| M-20 | History | Consent grant rate | Consult consent prompts granted ÷ prompts shown |
| M-21 | Revenue signal | Doctors willing to pay | Count and share of active doctors who accept a pricing offer |
| M-22 | Retention | Patient return | Patients with a second booking within 90 days ÷ patients with a first booking |

### 3.3 Guardrail metrics (must not get worse)
| Guardrail | Limit |
|---|---|
| API read p95 / write p95 | < 300 ms / < 800 ms |
| Monthly availability | ≥ 99.9% |
| Reminder lost silently | 0 |
| Prescription blocked by rules engine, then overridden | 0 (no override path) |
| Lost or duplicated payments | 0 |
| Crash-free sessions | ≥ 99% **[CONFIRM]** |
| App cold start (low-end Android) | < 3 s |

---

## 4. Analytics event list (v0 and v1.0)

**Naming:** `object_action`, lowercase snake_case. **Common properties on every event**
(all pseudonymous): `event_id`, `timestamp_utc`, `user_key`, `session_key`, `role`
(patient/doctor/clinic_admin/verifier), `app_version`, `platform` (android/ios/web),
`locale`, `country_code`, `city_tier` (coarse bucket, no coordinates).

### 4.1 Account and consent
| Event | Extra properties (allowed) | Source metric |
|---|---|---|
| `signup_started` | method (otp) | funnel |
| `signup_completed` | role | M-02 |
| `consent_given` | purpose_id, notice_version | M-20 |
| `consent_withdrawn` | purpose_id | privacy |
| `erasure_requested` | none | privacy |

### 4.2 Verification
| Event | Extra properties | Source |
|---|---|---|
| `verification_submitted` | subject_type (doctor/clinic) | M-10, M-11 |
| `verification_decided` | subject_type, outcome (verified/rejected/needs_info), path (auto/manual), duration_s | M-11 |
| `verification_suspended` | subject_type, reason_code | M-10 |

### 4.3 Discovery and booking
| Event | Extra properties | Source |
|---|---|---|
| `search_performed` | radius_bucket_km, mode (in_person/online), result_count_bucket | funnel |
| `provider_viewed` | none | funnel |
| `booking_started` | mode | M-14 |
| `booking_confirmed` | mode, lead_time_bucket | M-14 |
| `booking_cancelled` | by (patient/doctor/clinic), lead_time_bucket | M-14 |
| `booking_conflict_prevented` | none | M-15 |

### 4.4 Consultation and prescription
| Event | Extra properties | Source |
|---|---|---|
| `consult_started` | mode | M-16 |
| `consult_joined` | join_time_ms, audio_fallback (bool) | M-16 |
| `consult_completed` | duration_bucket | M-16 |
| `prescription_started` | none | M-13 |
| `prescription_signed` | item_count_bucket, used_template (bool), duration_s | M-01, M-13 |
| `prescription_blocked_by_rule` | rule_id (K-/table id, not drug name) | guardrail |
| `history_consent_prompted` | none | M-20 |
| `history_consent_granted` / `history_consent_revoked` | scope (consult), duration_bucket | M-20 |

### 4.5 Reminders and calendar
| Event | Extra properties | Source |
|---|---|---|
| `reminder_scheduled` | type (medication/test/appointment), channel | M-17 |
| `reminder_sent` | type, channel, delay_ms_bucket | M-17 |
| `reminder_delivered` | type, channel | M-19 |
| `reminder_acted_on` | type, channel, within_grace (bool) | north star |
| `reminder_switched_off` | type | M-18 |
| `channel_opt_in` / `channel_opt_out` | channel | M-19 |
| `calendar_viewed` | range (day/week/month) | engagement |

### 4.6 Payments
| Event | Extra properties | Source |
|---|---|---|
| `payment_started` | amount_bucket | M-03 |
| `payment_succeeded` / `payment_failed` | failure_code_class (no card data) | M-03 |
| `refund_issued` | reason_code (patient_no_show/doctor_no_show/doctor_cancel) | M-03 |

### 4.7 Technical (crash and performance)
| Event | Extra properties | Source |
|---|---|---|
| `app_started` | cold_start_ms | guardrail |
| `api_error` | endpoint_group, status_class | guardrail |
| `crash` | error_code, app_version (scrubbed stack trace) | guardrail |

---

## 5. What an event must NEVER contain (K-DP-04)

| Never include | Use instead |
|---|---|
| Diagnosis, condition, symptom, test result | Nothing. Do not log. |
| Medicine, injection or test **name**; and whether a user has injections or tests | Coarse type only: `medication`, `test`, `appointment` (injections are counted as `medication`) |
| Specialty searched (can reveal a condition) | Not logged on patient events **[CONFIRM]**; doctor specialty allowed only on doctor-role events |
| Free text (notes, search text, chat) | Length bucket only, if needed |
| Full name, phone, email | `user_key` (random) |
| Aadhaar, ABHA number, registration number, PAN | Never. Aadhaar must not be stored or logged (K-DP-05) |
| Exact location, GPS coordinates, address | `city_tier` and radius bucket |
| Exact amounts linked to a person's care | `amount_bucket` |
| Card data, UPI ID | Never (K-PAY-01) |
| Prescription or report content, file names | Never |

---

## 6. Enforcement (so the rule is checked, not just written)
- **Event schema allow-list.** Only properties listed above are accepted; any unlisted property
  is dropped and raises a CI/test failure.
- **Log scrubber + CI test with planted data.** A test plants a fake name, phone and drug in
  inputs and fails the build if any appears in analytics, logs or crash output (K-DP-04).
- **Crash reports** strip variable values and request bodies before sending.
- **Data inventory entry** (K-DP-06) for the analytics `user_key`, with purpose, consent basis,
  retention and deletion path; erasure also deletes analytics rows (K-DP-07).
- **Vendor register.** Any analytics vendor is a processor needing an agreement before it
  receives data (K-DP-08), with India-region hosting for personal data (K-DP-01). Prefer a
  self-hosted or India-hosted tool for v1.0. **[CONFIRM]**
- **Retention.** Raw events kept for a short, stated period (proposal: 13 months), then
  aggregated. **[CONFIRM]**

---

## 7. Dashboards (first cut)
1. **Exit-gate view:** M-01 to M-04 with targets.
2. **Doctor funnel:** applied → verified → first prescription → weekly active.
3. **Patient loop:** booking → consult → prescription → reminders → acted on → return.
4. **Health of the system:** guardrails.

## 8. Open items for the founder
1. Approve the **north-star metric** (plan adherence rate)?
2. Confirm the definition of an "engaged" patient (v0) and an "active day" (v1.0).
3. How will we measure the **denominator for M-01** (doctor survey vs sampling)?
4. Analytics tool: self-hosted / India-hosted, or a vendor?
5. Lawyer to confirm that product analytics is covered by the privacy notice and consent.

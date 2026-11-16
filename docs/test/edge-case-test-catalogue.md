# Edge-case test catalogue: every Critical and High edge case (S-053)

**Phase P03 · Epic EP05 · Depends on:** S-028 · **Contract refs:** K-DOD-01 · **Edge cases:** all E-IDs · **Date:** 2026-11-15
**Status:** Done (acceptance criterion: no Critical or High edge case without a linked story and a test). The tests are **specified** here; each one is written and must pass in the story named in the "Tested in" column, before that story is Done (K-DOD-01).

**Who this is for:** developers writing the tests in each story; reviewers checking K-DOD-01.

## How to use this

- **Test IDs (TC-xx)** go in the test name or tag, so a CI report can show which edge cases pass.
- **Level:** Unit · Integration · E2E (end to end through an app) · Load · Security (attack test) · CI (a pipeline check) · Drill (a run-through with people) · Manual (a scripted manual check, with evidence kept) · Review (a design or code review with a checklist).
- **Synthetic data only** (S-044). Aadhaar test values must fail the Verhoeff check, or be official UIDAI test numbers (AAD-10).
- The Edge Cases sheet "Notes" column now names each row's test ID.

**Housekeeping done in this story:**
1. **Duplicate ID fixed.** The marketing-without-opt-in case is now **E-MSG-02**; E-MSG-01 stays the WhatsApp-fallback case.
2. **Gap found: no v1.0 story builds product analytics in the apps.** E-DP-25 is tested through S-092 and S-174 for now. A story is proposed for the founder (section 3).

## 1. Catalogue (66 Critical and High edge cases)

### Data protection (E-DP)

| Test | E-ID | Sev | Level | Version | Tested in | Given / When / Then |
|---|---|---|---|---|---|---|
| TC-01 | E-DP-01 | Critical | Integration | v1.0 | S-168, S-097 | Given a doctor viewing a shared record / when the patient revokes / then the next request returns 403 `consent.required`, the cached view is cleared, and the doctor sees "access ended" |
| TC-02 | E-DP-02 | High | Integration | v1.0 | S-169 | Given sharing ends in 10 min / then the doctor gets a warning; when it expires / then reads fail; an extension request needs the patient's approval |
| TC-03 | E-DP-03 | High | Integration | v1.0 | S-143, S-060 | Given notes under RET-02 / when the patient erases the account / then the notes are restricted (hidden, readable only by the patient, treating doctor or legal order), everything else is deleted, and the report lists what was kept and why |
| TC-04 | E-DP-04 | High | Drill | v1.0 | S-062, S-185 | Restore a backup taken before an erasure / the erased patient's encrypted fields cannot be decrypted (key destroyed); retention-key records stay readable until their date |
| TC-05 | E-DP-05 | High | Integration | v1.0 | S-098 | Rotate the key while reads and writes run / no errors; old data readable with the old key version; background re-encryption completes |
| TC-06 | E-DP-06 | High | Integration | v1.0 | S-098, S-056 | Key service unavailable / sensitive reads and writes fail with a clear error and an alert; no plaintext fallback, and nothing written unencrypted |
| TC-07 | E-DP-07 | High | E2E | v1.0 | S-115, S-180 | Sign out a lost device from another device / its refresh token is revoked within 1 min; local data stays encrypted behind the app lock |
| TC-08 | E-DP-11 | High | Integration | v1.1 | S-212, S-200 | Child turns 18 / guardian access ends that day; the profile is frozen until the new adult claims it |
| TC-09 | E-DP-12 | High | E2E | v1.0 | S-156, S-079 | Any reminder or booking notification on a locked phone / no condition or specialty shown; medicine name only with P-MEDNAMES |
| TC-10 | E-DP-13 | High | CI | v1.0 | S-095, S-059 | A test plants a phone number, name, diagnosis and Aadhaar-like number in a log call / the scrubber removes them and the CI check fails if any reaches log output |
| TC-11 | E-DP-16 | Critical | Security | v1.0 | S-128, S-181, S-116 | A staff account without a just-in-time grant opens a patient record / denied and logged; with a grant / allowed only in scope and time; break-glass raises an alert |
| TC-12 | E-DP-17 | High | Drill | v1.0 | S-064 | Tabletop: the SMS vendor reports a breach / the runbook vendor path is followed; the data the vendor holds is confirmed minimal; notifications assessed |
| TC-13 | E-DP-19 | High | Review | v1.0 | S-056 | Checklist review of search indexes and caches / no SH-C fields in any index; caches with SH-L or SH-C data encrypted with a short TTL |
| TC-14 | E-DP-20 | High | Security | v1.0 | S-159 | Upload an EICAR test file, a script renamed `.pdf`, and an oversized image / rejected, rejected, re-encoded or rejected; nothing executable stored |
| TC-15 | E-DP-21 | High | Security | v1.0 | S-171, S-151 | Use a PDF link after expiry, as another user, and a second time / each refused |
| TC-16 | E-DP-22 | Critical | Integration | v1.0 | S-104 | A recycled number signs in on a new device / no old data shown until re-verification passes |
| TC-17 | E-DP-23 | High | E2E | v1.0 | S-115 | Patient lost their number / recovery needs an identity check beyond OTP; old sessions revoked |
| TC-18 | E-DP-24 | Critical | Drill | v1.0 | S-184, S-064 | Out-of-hours drill / on-call paged; the CERT-In report is ready within 6 h; DPDP Board and patient notices drafted |
| TC-19 | E-DP-25 | High | CI + Review | v1.0 | S-092, S-174 (see section 3) | CI fails if an analytics, ads or attribution SDK is in an app build; event schema review: no health fields; no events from health screens |
| TC-20 | E-DP-26 | High | Unit | v1.0 | S-108, S-139, S-095 | Enter a Verhoeff-valid 12-digit number in profile, notes or a booking note / rejected with a reason; nothing stored; not in logs |
| TC-21 | E-DP-27 | High | Manual | v1.0 | S-129 | The reviewer receives an Aadhaar card as ID / rejects it; the file is hard-deleted at once (audit event); the doctor is asked for another ID |

### Booking, payments, consults (E-BK, E-PAY, E-TC)

| Test | E-ID | Sev | Level | Version | Tested in | Given / When / Then |
|---|---|---|---|---|---|---|
| TC-22 | E-BK-01 | Critical | Load | v1.0 | S-121, S-175 | 100 parallel holds on one slot / exactly 1 succeeds; 99 get "slot taken" with the next slots offered |
| TC-23 | E-BK-02 | High | Integration | v1.0 | S-113 | The doctor changes availability over booked slots / bookings kept; the conflict is shown; reschedule offered to patients |
| TC-24 | E-BK-03 | High | Integration | v1.0 | S-123, S-122, S-136 | A patient in Dubai books an **in-person** visit with an Indian doctor / each sees their own local time; stored in UTC with the zone. **The same patient tries to book or join an online consult without confirming "I am in India now" / refused (P10, RX_PATIENT_OUTSIDE_INDIA)** (review R-02) |
| TC-25 | E-BK-04 | High | Integration | v1.0 | S-148, S-133 | Doctor not joined 10 min after the start / automatic full refund (RF03); reliability recorded |
| TC-26 | E-BK-07 | High | Integration | v1.0 | S-113, S-145 | The clinic does not reconfirm the doctor the day before / the patient is warned and offered a reschedule or full refund |
| TC-27 | E-BK-08 | High | Integration | v1.0 | S-148, S-133 | One side never joined / "Completed" cannot be set; under 2 min / flagged; a patient dispute within 24 h leads to a policy refund |
| TC-28 | E-PAY-01 | High | Integration | v1.0 | S-125, S-146 | Payment captured, webhook delayed / booking pending; polling confirms; after 15 min the hold expires and a late debit is auto-refunded |
| TC-29 | E-PAY-02 | Critical | Integration | v1.0 | S-125 | Money debited, booking creation fails / automatic refund and alert; the patient sees the status |
| TC-30 | E-PAY-03 | High | Integration | v1.0 | S-124 | The same webhook arrives 3 times / processed once |
| TC-31 | E-PAY-05 | High | Integration | v1.0 | S-163, S-133 | Refund from the clinic account rejected (low balance) / retry, alert the clinic, grievance ticket; new bookings paused after 48 h; patient sees status |
| TC-32 | E-TC-01 | High | E2E | v1.0 | S-137 | Throttle the network mid-call / audio fallback; rejoin works; the consult is not auto-completed |
| TC-33 | E-TC-02 | High | Integration | v1.0 | S-148 | Doctor converts to in-person / outcome recorded; prohibited drugs stay blocked; conversion credit per RF06 |
| TC-34 | E-TC-03 | High | E2E | v1.0 | S-147 | Consult starts / the doctor must confirm name and age before the composer opens (P02) |

### Prescribing, reminders, verification (E-RX, E-REM, E-VER)

| Test | E-ID | Sev | Level | Version | Tested in | Given / When / Then |
|---|---|---|---|---|---|---|
| TC-35 | E-RX-01 | Critical | Unit | v1.0 | S-138 | Rule-table tests T01 to T30 from the rules spec, including: Schedule X in a teleconsult → RX_PROHIBITED, no override; clonazepam → regulatory allow but PL03 block |
| TC-36 | E-RX-02 | High | Unit | v1.0 | S-149 | Dose, frequency or duration missing / signing blocked (P07); free text only in instructions |
| TC-37 | E-RX-03 | High | Integration | v1.0 | S-164 | Amend a signed prescription / a new version; old version immutable; patient notified; reminders updated |
| TC-38 | E-RX-06 | High | Unit | v1.0 | S-138 | Video dropped to audio before signing, List A first prescription / blocked, RX_NEEDS_VIDEO_FIRST, with a prompt to restore video |
| TC-39 | E-REM-01 | High | Integration | v1.0 | S-140 | Patient changes time zone; daylight-saving transition in a test zone / reminders follow the home or local setting with no duplicates or gaps |
| TC-40 | E-REM-02 | High | E2E | v1.0 | S-165, S-153 | Phone offline for 48 h / on-device reminders fire for the next 72 h; critical items fall back to SMS |
| TC-41 | E-REM-05 | High | Integration | v1.1 | S-210 | Same medicine from a scan and a platform prescription / duplicate flagged; never merged automatically |
| TC-42 | E-VER-01 | High | Integration | v1.0 | S-129 (S-111 after the beta) | Register name differs from ID / goes to manual review; never auto-approved |
| TC-43 | E-VER-02 | High | Unit | v1.0 | S-110 | Registration number without a council / rejected; same number in two councils / manual review |
| TC-44 | E-VER-03 | Critical | Integration | v1.0 | S-160, S-118 | Licence suspended / listing and prescribing blocked server-side at the next check; signing returns RX_DOCTOR_NOT_VERIFIED |
| TC-45 | E-VER-04 | High | Integration | v1.0 | S-109 | Registry or vendor down / case queued for manual review; never auto-approved |
| TC-46 | E-VER-05 | High | Unit | v2.0 | S-224 | A test outside the lab's NABL scope / cannot be booked at that lab |
| TC-66 | E-VER-06 | High | Integration | v1.0 | S-160, S-133 | A doctor with 3 paid future bookings is suspended / all 3 are cancelled automatically, refunded in full (RF04), patients notified and offered another doctor, clinic notified; no consult or signing possible (review R-07) |

### Family, SOS, scan, reviews, messaging, global, accessibility

| Test | E-ID | Sev | Level | Version | Tested in | Given / When / Then |
|---|---|---|---|---|---|---|
| TC-47 | E-FAM-02 | High | Integration | v1.1 | S-207 | A caregiver opens a hidden sensitive category / not shown unless the patient allowed it |
| TC-48 | E-FAM-04 | High | Manual | v1.0 | S-147 | The doctor finds the patient is a minor / consult stopped; account handled under CH-12 |
| TC-49 | E-SOS-01 | Critical | E2E | v1.1 | S-195, S-197 | No data connection, cellular signal present / the dialler calls 112 directly; the app shows only "Calling 112" and the real call state, never "help is on the way" |
| TC-50 | E-OCR-02 | Critical | Manual | v1.1 | S-206 | Unclear handwriting / item marked unclear and never scheduled; only the prescribing doctor's clarification unlocks it |
| TC-51 | E-REV-01 | High | Integration | v1.1 | S-209 | An account without a completed visit / cannot review |
| TC-52 | E-MSG-02 | High | Unit | v1.0 | S-141 | A promotional template is sent to a user without consent / the adapter blocks it and logs the attempt |
| TC-53 | E-GLB-01 | High | Integration | v1.0 (pack loader); v5.0 (second country) | S-099; S-273 | CI loads a synthetic test pack "XX" (different drug table, currency, emergency number) / core flows pass with no code change |
| TC-54 | E-ACC-01 | High | Manual | v1.0 | S-176 | Screen reader, 200% text and contrast check on every v1.0 patient screen / WCAG 2.1 AA target met, or issues logged |

### Security (E-SEC, from the threat model S-043)

| Test | E-ID | Sev | Level | Version | Tested in | Given / When / Then |
|---|---|---|---|---|---|---|
| TC-55 | E-SEC-01 | High | Security | v1.0 | S-104 | 6 wrong OTPs / locked with backoff; 100 OTP requests from one IP or device / throttled; non-+91 number refused; spend cap trips the kill switch |
| TC-56 | E-SEC-02 | Critical | Security | v1.0 | S-105, S-150 | A doctor signs in on a new device / MFA required; signing needs step-up and an e-signature; an anomaly alert fires on 3× normal volume |
| TC-57 | E-SEC-03 | Critical | Security | v1.0 | S-116, S-181 | Every endpoint called with another patient's ID / 404 and logged (generated suite covers 100% of operations) |
| TC-58 | E-SEC-05 | Critical | Security | v1.0 | S-124, S-125, S-146 | Webhook with a bad signature, a replayed event ID, a wrong amount, or a client-sent amount / each rejected; no booking confirmed |
| TC-59 | E-SEC-06 | Critical | Security | v1.0 | S-138, S-099 | Call the sign endpoint directly with a blocked drug / rejected; load a rule file with a bad checksum / loader refuses to start |
| TC-60 | E-SEC-07 | High | Security | v1.0 | S-150, S-151 | Change 1 byte in a signed PDF / signature check fails; verification page says "not valid" and shows no patient data |
| TC-61 | E-SEC-08 | High | CI | v1.0 | S-127, S-152 | Add `medicineName` to the scheduler-core spec / lint fails (fixture in docs/arch/lint-fixtures); runtime rejects unknown fields |
| TC-62 | E-SEC-09 | Critical | Manual | v1.0 | S-129, S-110 | Registration with a real doctor's number and a mismatched ID / never approved; the real doctor notified on go-live |
| TC-63 | E-SEC-10 | High | Integration | v1.0 | S-143, S-142 | Erasure without step-up / refused; with step-up / notice to the phone; undo works inside the window [VALIDATE window] |
| TC-64 | E-SEC-11 | Critical | Security | v1.0 | S-128, S-091, S-094 | Admin sign-in without a phishing-resistant factor, or from outside the allow-list / refused; an unsigned build cannot be promoted to production |
| TC-65 | E-SEC-12 | High | Security | v1.0 | S-096 | Alter one audit row in a test database / the daily chain check flags the break and alerts; module roles cannot UPDATE or DELETE |

## 2. Check

| Criterion | Result |
|---|---|
| Every Critical or High edge case has a linked story | **Yes**, 66 of 66 (E-DP-25 linked to S-092 and S-174 until an analytics story exists) |
| Every Critical or High edge case has a test | **Yes**, TC-01 to TC-66 (TC-66 added 2026-11-16) |
| Medium and Low edge cases | Get tests in their own stories; not required by this acceptance criterion |

## 3. Proposed new story (founder)

**"Privacy-first product analytics in the apps (v1.0)":** implement the event list from metrics-and-events.md.
- Opt-in only (P-ANALYTICS).
- A pseudonymous key only.
- No third-party SDKs.
- No events from health screens.
- Hosted in India (ADR-26).

Today no v1.0 story builds it, so the metrics in S-005 would have no data. **[CONFIRM]** whether to add it.

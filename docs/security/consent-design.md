# Consent and permission design (S-057)

**Phase P06 · Epic EP08 · Depends on:** S-010 ([consent-texts.md](../privacy/consent-texts.md)) · **Contract refs:** K-DP-02 (also K-DP-09, K-DP-10, K-MSG-02) · **Edge cases:** E-DP-01, E-DP-02 · **Compliance:** C01 · **Date:** 2026-11-15
**Status:** In Testing, until the founder approves the design (acceptance criterion). It covers E-DP-01 and E-DP-02 (section 7). Built in S-097 (service and middleware), S-107 (sign-up), S-168 and S-169 (sharing), S-156 (notification choices).

**Who this is for:** the backend lead (records module), app developers, and reviewers checking K-DP-02.

## 1. Two different things, kept apart

| Concept | What it answers | Examples | Stored as |
|---|---|---|---|
| **Purpose consent** | "May we process this person's data for purpose X?" (DPDP s.6) | P-ACCOUNT, P-CARE, P-RECORD, P-REMIND, P-MEDNAMES, P-ANALYTICS, P-NEWS; D-VERIFY, D-PROFILE, D-STAFF, D-ANALYTICS | `ConsentRecord` (event log) + `ConsentState` (current) |
| **Access permission** | "May this doctor read this patient's data right now?" (K-DP-02) | Access tiers T1 to T4 | `PermissionGrant` |

Both live in the **records** module ([module-boundaries](../arch/module-boundaries-and-domain-model.md)). **No other module stores consent.** Others ask through the public interface.

## 2. Data model

**ConsentRecord** (append-only; RET-05):

| Field | Notes |
|---|---|
| id, subject_ref | The person the data is about |
| given_by_ref, on_behalf_of_ref | Equal for adults. For children (v1.1, CH-05): guardian and child. |
| purpose_id, text_version, notice_version | Exactly what was shown (for example `P-CARE@2`, `PN-1.0`) |
| action | `grant` / `withdraw` |
| channel | `app` / `web` / `paper` (v0) / `consent_manager` (reserved; DPDP registered Consent Managers are not supported in v1.0) |
| evidence_ref | Paper form scan (v0) |
| verification_method | Guardian verification (v1.1, S-017 option) |
| occurred_at | UTC, NTP-synced time |

**ConsentState:** the current value per (subject, purpose), derived from the records. Cached (section 4).

**PermissionGrant:**

| Field | Notes |
|---|---|
| id, patient_ref, grantee_ref (doctor), clinic_ref | |
| tier | `T1_current_care` / `T2_own_records` / `T3_history_share` / `T4_extension` |
| scope | T1: one booking. T2: records created by this doctor or clinic. T3/T4: the whole record except hidden categories. |
| basis | `P-CARE` / `legal_record_duty` / `P-SHARE` |
| starts_at, expires_at, revoked_at, revoked_by | T1 ends 24 h after the consult closes [CONFIRM]. T2 has no expiry while RET-02 runs. T3 default: 7 days after the appointment. |
| created_from | booking ID, consult ID or share request ID |

**How grants are created:**
- **T1** is created by the system on `appointment.confirmed`.
- **T2** is created when a doctor signs the first record for a patient.
- **T3** is created only by the patient's tap on the P-SHARE screen.
- **T4** is created only when the patient approves the doctor's extension request.

## 3. Enforcement point

**One gate, in the records module, used by every read of patient data by someone else:**

```
records.authorize(actor, patient_ref, data_kind, purpose) -> allow(tier, grant_id) | deny(reason)
```

1. The deny-by-default role check runs first (K-SEC-04, access control design S-058).
2. Then the tier check: is there an active grant whose scope covers `data_kind`?
3. Then the purpose check: is the purpose consent this data use relies on still granted? For example, reminders need P-REMIND.
4. **On allow:** write the audit event durably, before returning data (ADR-28), with the grant ID and tier.
5. **On deny:** return `403 consent.required` (or 404 if the actor may not know the record exists), and log the denial.

**Where it runs:**
- The API middleware calls it for every request touching patient data.
- **Workers and other modules call it too.** The notifications module checks P-REMIND and P-MEDNAMES at **send time**, not when the reminder was scheduled.

**No bypass.** There is no "system" or "admin" flag that skips it. Staff access goes through just-in-time grants (K-SEC-08), which the same gate checks.

## 4. Speed and freshness (performance target under 20 ms)

- ConsentState and active grants are cached per patient with a **short TTL (60 s)** and **invalidated at once** by `consent.granted/revoked` events.
- **A revocation is acted on synchronously:** the revoke call deletes the cache entry before it returns, then publishes the event for other instances. The TTL is only a safety net.
- **Expiry is checked on every request** against `expires_at` (no waiting for a job), so access ends at the exact time.
- **Cache holds only IDs, tiers, scopes and times.** It never holds health data (E-DP-19).

## 5. What each withdrawal does (DPDP s.6(4) and s.6(6): stop processing within a reasonable time)

| Withdrawn | Effect | When |
|---|---|---|
| P-REMIND | Scheduler activities for the patient cancelled; no reminder sends; on-device backup reminders cleared at next sync; history of past doses kept in the record | Immediate (next request) |
| P-MEDNAMES | Notifications switch to neutral text | Next send |
| P-ANALYTICS | Events stop; the `user_key` link is deleted (RET-11) | Immediate |
| P-NEWS | No more news messages | Immediate |
| P-SHARE (a T3/T4 grant) | Doctor loses access; open views cleared on the next request (E-DP-01) | Immediate |
| P-ACCOUNT, P-CARE or P-RECORD (needed) | Only by closing the account. This triggers erasure (S-060); clinical records restricted under RET-02 | Erasure flow |
| D-* (doctor) | Listing removed; verification evidence kept under RET-08 | Erasure flow |

The patient sees the effect stated in plain words (consent texts §D).

## 6. Notice changes and re-consent

- **A new text version** of a purpose needs a new grant before the changed use starts. Until then, the old version's scope applies, or the feature pauses for that user (consent texts §D).
- **Notice version change** without a purpose change: show the "what changed" summary at the next app open. No re-consent needed unless a purpose changed [VALIDATE].
- **Existing users when the DPDP duties start (13 May 2027):** DPDP s.5(2) requires a notice "as soon as reasonably practicable" to people whose data was collected before. **Every v0 and beta user is shown the DPDP notice and asked to confirm consents** before that date [VALIDATE timing].

## 7. Edge cases (acceptance criterion)

| Edge case | Design | Test |
|---|---|---|
| **E-DP-01** patient revokes while a doctor has the record open | Revoke deletes the cache synchronously and publishes an event. The doctor's next request is denied. The Pro web client gets a push event (websocket or poll) and clears the cached view; it shows "Access ended". Data already rendered on screen cannot be recalled; the view is cleared at the next interaction or within the poll interval (at most 30 s) [CONFIRM]. | TC-01 |
| **E-DP-02** sharing expires mid-consultation | The doctor sees a warning 10 min before expiry. A one-tap "ask for more time" sends a request to the patient. On expiry, reads are denied, with no silent extension. T1 (current-care data) still works until 24 h after the consult closes. | TC-02 |
| E-DP-14 doctor leaves a clinic | Clinic-scoped T2 grants move with the record owner rule: the clinic keeps records it must legally hold, the departed doctor loses access; patient-granted T3 grants to that doctor are revoked | Integration (Medium) |
| E-FAM-02 caregiver and sensitive categories (v1.1) | Caregiver grants have a category scope; sensitive categories excluded unless the patient adds them | TC-47 |

## 8. Audit

Every grant, withdrawal, permission creation, extension, expiry, denial and allowed read produces an audit event (ADR-28) with: actor, patient_ref, tier, grant_id, purpose, data_kind, result.
- The patient's access log (S-170) shows the **allowed reads by people**, in plain words.
- Denials and system reads are kept for security but not shown to the patient [CONFIRM].

## 9. Changes to existing stories

| Story | Change |
|---|---|
| **S-155** "Reminders are on by default for every prescription item" | **Only when P-REMIND is granted.** If it is off, the app offers it once when the prescription arrives. Each item can still be switched off. |
| S-157 "Booking confirmations and reminders by app and WhatsApp" | WhatsApp on hold: app (push) and SMS only. Booking messages are under P-CARE. |
| S-097 | Build sections 2 to 4 and the `authorize` gate. Contract test: no module reads patient data without calling it. |
| S-168, S-169 | Build T3 and T4, plus E-DP-01 and E-DP-02 behaviour |

## 10. Decisions [CONFIRM]

1. T1 completion window: 24 h after the consult closes.
2. Pro web view cleared within 30 s of a revocation, by push or poll.
3. Show patients only reads by people in the access log, not system reads or denials.
4. Re-consent campaign for v0 and beta users before 13 May 2027 (lawyer to confirm timing).

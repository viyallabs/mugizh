# User journeys (S-045)

**Phase P09 · Epic EP11 · Depends on:** S-003 ([personas.md](../personas.md)) · **Date:** 2026-11-15
**Status:** In Testing. The acceptance criterion says "reviewed with v0 learnings", and the v0 pilot has not run yet (S-025). Review again after the v0 go/no-go memo (S-082).

**Who this is for:** designers (S-046, S-078, S-086) and developers building each flow. Every step names its screen (Screens sheet), the compliance gate that must hold, and the edge cases to design for.

**Current decisions these journeys follow:**
- WhatsApp is on hold, so channels are push, SMS and email.
- Clinic-collected payments (mode B): money goes to the clinic, never to us.
- Adults only in v1.0; child profiles are gated by CH-11.
- Prescription reminders are opt-in (P-REMIND). Once on, every prescription item gets a reminder, and each can be switched off.
- Signing means step-up (P08) plus an electronic signature (P09).
- Doctor access follows tiers T1 to T4 ([consent-texts.md](../privacy/consent-texts.md) A2).

## 1. Rules every journey keeps

| # | Rule | Source |
|---|---|---|
| J1 | No step asks for Aadhaar, ever | K-DP-05 |
| J2 | Each data use is asked separately and is never pre-ticked. Optional items never block the journey. | Consent texts rule 1 |
| J3 | Before paying: seller (clinic), full fee, refund summary with version, grievance contact | money-flow §3, K-PAY-05 |
| J4 | No dark patterns: no countdown pressure, no forced rating, no hidden fees | E-REV-03, dark-pattern guidelines |
| J5 | Lock screen and SMS never show a condition or specialty; medicine names only with P-MEDNAMES | K-DP-09 |
| J6 | "Not for emergencies, call 112" wherever a consult is offered | C33, consent texts C |
| J7 | Every screen designed for empty, loading, error, offline and permission-denied states | Screens sheet rule |
| J8 | Large text and screen-reader labels on every patient step (WCAG 2.1 AA target) | E-ACC-01 |

## 2. Patient — Lakshmi, 58 (v1.0)

| Stage | What happens | Screen | Gate that must hold | Edge cases |
|---|---|---|---|---|
| 1. Sign up | Phone number → OTP → date of birth (18+ gate) | SC-01 | OTP limits (E-SEC-01); under 18 stopped, nothing stored (CH-01) | E-DP-22 recycled number; OTP failure |
| 2. Consent | Three needed items (P-ACCOUNT, P-CARE, P-RECORD), then optional ones (P-REMIND, P-MEDNAMES, P-ANALYTICS, P-NEWS), all off | SC-02 | One toggle per purpose; full notice one tap away | — |
| 3. Find a doctor | Specialty and distance; location or PIN code | SC-04 | Only Verified doctors listed (K-CS-01); no paid ranking; ranking factors explained | E-GEO-01 location denied |
| 4. Choose | Profile: verified badge, registration number and council, fees, timings | SC-05 | One price, the same as at the clinic (T04) | Doctor unavailable |
| 5. Book | Pick slot → 15-minute hold | SC-06 | Max 2 holds per account (E-SEC-04) | E-BK-01 slot taken; E-BK-03 time zones |
| 6. Pay | Hosted checkout on the clinic's account; refund summary first | SC-07 | J3; no card data with us (K-PAY-01) | E-PAY-01 pending; E-PAY-02 debited but failed |
| 7. Confirmed | Confirmation by push and SMS (SMS-02) | SC-08 | Booking messages are part of P-CARE | — |
| 8. Before the visit | Reminders 24 h and 2 h before (SMS-03). Clinic reconfirms the doctor the day before. | SC-03 | J5 | E-BK-07 doctor absent → warned, reschedule or refund |
| 9a. Online consult | Device check → teleconsult consent ("not for emergencies"; **"I am in India now"**) → optional share history (P-SHARE) → join | SC-09, SC-13, SC-10 | Consent recorded at join (P04); **online consults only for patients in India (P10)**; sharing is optional and asked each time | E-TC-01 weak network; E-TC-03 wrong person; E-BK-03 patient abroad (in-person only) |
| 9b. In-person visit | Arrives; front desk marks arrived | — | — | E-BK-09 counter asks for more |
| 10. Prescription | Gets the signed prescription and PDF; "new version" notice if amended | SC-11 | Signed versions immutable (K-CS-05) | E-RX-03 new version |
| 11. Reminders | If P-REMIND is off, offered once when the prescription arrives. If on, each item gets reminders, with taken, skip and snooze. Calendar shows the week. | SC-15, SC-14, SC-03 | J5; offline backup for 72 h (S-165) | E-REM-01 time zones; E-REM-02 offline; E-REM-03 grouped; E-RX-04 as-needed |
| 12. Record | Timeline of visits, prescriptions and uploads; who viewed it | SC-12, SC-13 | Every non-patient read visible (S-170) | E-DP-20 bad upload; Aadhaar upload quarantined (AAD-05) |
| 13. Something goes wrong | Refund status; complaint; data request | SC-17, SC-18 | Grievance: 24 h acknowledgement, 7 days to resolve; rights requests 15 days | E-PAY-05 refund from the clinic fails |
| 14. Leave | Close account, export data | Settings (SC-52, new) | Clinical records restricted, not deleted (RET-02); step-up to erase (E-SEC-10) | E-DP-03 |

**Where Lakshmi struggles today (from the Practo review themes, S-027), and our answer:**
- Doctor absent on the day → reconfirmation (E-BK-07).
- Refund silence → visible refund status.
- Consult "completed" without happening → both sides must connect (E-BK-08).
- A different price at the counter → one price (E-BK-09).

## 3. Doctor — Dr. Arun, 42 (v1.0)

| Stage | What happens | Screen | Gate | Edge cases |
|---|---|---|---|---|
| 1. Invite or sign up | Clinic invites him, or he signs up himself. OTP, then MFA set-up (authenticator or passkey). | SC-19 | MFA mandatory (K-SEC-03); provider notice and D-VERIFY/D-PROFILE consents; Provider Agreement accepted | Invitation expired (30 days) |
| 2. Verification | Council, registration number, year, qualification, a non-Aadhaar photo ID. Reviewer video check [CONFIRM]. | SC-19 | Never auto-approved on a mismatch (E-VER-01); Aadhaar card rejected (AAD-04) | E-VER-02 same number in two councils; E-SEC-09 impersonation |
| 3. Practice set-up | Profile (NMC-compliant: no testimonials, no "best doctor"), fees, consult modes, availability | SC-21 | Public profile rules (CD rules); one price | E-BK-02 availability changed with bookings |
| 4. Day view | Today's list: arrived, late, no-show | SC-22 | Marks audited | E-BK-04 he is late (patient auto-refunded after 10 min) |
| 5. Consult | Joins room; confirms patient identity (name, age) | SC-24 | Tier T1 access only; history only with P-SHARE (T3) | Patient not joined; E-TC-01 |
| 6. History | Sees his own past notes (T2); other history only if shared (T3), with expiry warning | SC-23 | Every read audited (K-DP-02) | E-DP-01 revoked mid-view; E-DP-02 expiry |
| 7. Prescribe | Structured items; the rules engine shows blocks with reasons | SC-25 | Server-side rules, fail closed (K-CS-02); never suggests a drug or dose (K-CS-03) | E-RX-01 prohibited; E-RX-06 video dropped to audio; E-RX-02 dose fields |
| 8. Sign | Step-up (P08) + electronic signature (P09) → immutable PDF with registration number and verification reference | SC-26 | P01 to P10 all pass | Signature provider down → not issued (fail closed) |
| 9. Outcome | Completed / converted to in-person / patient no-show / technical failure / referred / emergency referral | SC-24 | "Completed" only if both joined (E-BK-08) | E-TC-02 convert |
| 10. Later | Amend → new version; sees payments and refunds for his consults | SC-25, SC-27 | Amendments are new versions (K-CS-05) | E-RX-03 |

**His target:** a prescription faster than paper. Templates (Should) and the drug search under 150 ms support this. The time target is still to be confirmed.

## 4. Clinic admin and front desk (v1.0, supporting role)

| Stage | What happens | Screen | Gate |
|---|---|---|---|
| Clinic set-up | Legal name, address, registration where the state requires it, GST status, merchant account through the partner programme | SC-20 | Provider Agreement B1; clinic registration (C24) |
| Staff | Invite front-desk staff, who accept D-STAFF@1 at activation | SC-50 (new) | Staff see booking fields only, never clinical notes |
| Daily | Mark arrivals and no-shows; close the clinic for a day with bulk reschedule or cancel | SC-22, SC-21 | Refunds per policy (E-BK-06); day-before reconfirmation (E-BK-07) |
| Money | Payments received on the clinic's own account, refunds, subscription invoices | SC-27 (renamed) | Refund duty in mode B; low balance leads to an alert and a pause after 48 h (E-PAY-05) |

## 5. Internal staff (v1.0)

| Role | Journey | Screen | Gate |
|---|---|---|---|
| Verification reviewer | Queue → register check → ID and video check → decision with reason | SC-28 | Second reviewer for overrides; Aadhaar rejected and deleted at once |
| Grievance Officer | Complaint or rights request → timer → reply | SC-30 | 7 days for complaints; 15 days for rights requests; 36 h, 3 h and 2 h content timers |
| Support | Look up a booking or payment without seeing health data; ask for just-in-time access only when needed | SC-29 | K-SEC-08: reason, approval, end time, alert on break-glass |
| Ops | Refund failures, clinic payment problems | SC-53 (new) | E-PAY-05 |

## 6. Caregiver — Karthik, 32 (v1.1, outline)

1. Lakshmi invites him and chooses what he may see and do. Sensitive categories stay hidden unless she allows them (E-FAM-02). She can revoke at any time.
2. He gets alerts for missed doses (after a grace period) and missed appointments. From v1.1 he also gets SOS alerts, and can book and pay on her behalf. The payer and the patient are recorded separately (CH-08).
3. Every action he takes is audited, and Lakshmi can see it.
4. If he is abroad, the data stays hosted in India and he can only view it (E-DP-18, lawyer to confirm).
5. **Children's profiles do not launch until the CH-11 confidentiality and safeguarding protocol is approved.**

## 7. Nurse — Meena, 34 (v4.0, outline only; S-031 option A keeps nurses out of v1.x)

Verification (state council registration or NUID, agency confirmation) → visit list and route → pre-visit safety checklist (valid prescription, allergies, first-dose policy, cold chain, emergency kit) → a failed checklist blocks the visit → visit record written to the patient's history → escalation path for adverse reactions. Screens SC-46 to SC-48. The role is reserved in identity now; no accounts until v4.0.

## 8. Review with v0 learnings (to do)

After the v0 pilot (S-025) and the go/no-go memo (S-082), check:
1. Did patients actually turn reminders on when offered? This tests the opt-in P-REMIND design.
2. Which reminder channel worked without WhatsApp?
3. How long did doctors take per prescription?
4. Where did patients drop off?

Update this document and mark the story Done.

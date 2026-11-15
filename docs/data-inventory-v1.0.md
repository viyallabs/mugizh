# Data inventory and retention schedule, v1.0 (S-011) — DRAFT for founder, lawyer and CA review

**Phase P01 · Epic EP02 · Depends on:** S-008 · **Compliance:** C01, C06, C30 (also C02, C03, C07, C20, C23, N1) · **Contract:** K-DP-06 · **Edge case:** E-DP-03
**Date:** 2026-11-14 · **Not legal advice.** **[VALIDATE]** = legal point to confirm; **[CONFIRM]** = proposal for the founder.

**Who this is for and what it decides:** for the founder, lawyer and CA, to approve how long each kind of data is kept and how it is deleted. For engineering it is the register K-DP-06 points to: **any PR that adds a personal-data field must add a row here** (purpose, basis, retention, deletion path). Sensitivity classes are proposals; S-044 confirms them.

**Inputs:** requirements v1.0 (R-xx), privacy notice and purpose IDs (docs/privacy/), metrics and events, Architecture sheet (module map, ADRs), Contracts and Edge Cases sheets, S-008 review. **WhatsApp is on hold** (founder, 14 Nov 2026), so WhatsApp fields are listed but marked ON HOLD.

---

## 1. Retention rules

Each field below points to one of these rules.

| Rule | Data | How long | Then | Why / source |
|---|---|---|---|---|
| **RET-01** | Account and profile data | While the account is active | Erased within 30 days of account closure (DEL-ERASE) | DPDP s.8(7): erase when the purpose ends. **Inactive accounts:** propose erasure after 3 years with no sign-in, with 48 hours' notice first [CONFIRM]. The DPDP Rules' 3-year inactivity rule is for very large platforms only and does not bind us [VALIDATE]. |
| **RET-02** | Clinical records: notes, diagnoses, prescriptions, prescription PDFs, teleconsultation consent, consult outcome | **3 years after the patient's last consultation with that doctor**, longer while a complaint or legal claim is open (legal hold). While the account is active, the patient keeps their own copy for as long as they want. | On erasure: **restricted, not deleted** (DEL-RESTRICT), then hard-deleted when the period ends | See section 2. C30, E-DP-03. |
| **RET-03** | Documents the patient uploads | Until the patient deletes them or closes the account | DEL-SELF / DEL-ERASE | No legal duty for us to keep the patient's own uploads. If a doctor copied content into their notes, that copy follows RET-02. |
| **RET-04** | Financial: payments, refunds, ledger, invoices | **8 years from the end of the financial year** [VALIDATE with CA] | DEL-RESTRICT, then delete | GST (CGST Act s.36: 72 months from the annual-return due date) and income-tax book-keeping rules; 8 years covers both with margin [VALIDATE]. Invoices must not name a diagnosis or specialty that reveals a condition [CONFIRM]. |
| **RET-05** | Consent records, opt-ins and opt-outs | Account life **+ 3 years** | DEL-RESTRICT, then delete | DPDP s.6(10) puts the burden of proving consent on us; 3 years matches the main limitation period [VALIDATE]. |
| **RET-06** | Record access log (who opened a patient's record) | Same as the clinical record it relates to (RET-02), and at least 1 year | DEL-RESTRICT, then delete | Evidence for K-DP-02; the patient sees it (R-83). DPDP Rules 1-year minimum log retention [VALIDATE]. |
| **RET-07** | Security and system logs (scrubbed: no health data, K-DP-04) | **1 year rolling**, in India | DEL-ROLL | CERT-In needs 180 days (C07); DPDP Rules 1 year [VALIDATE]. One year meets both. ADR-21 and K-SEC-01 say 180 days and should move to 1 year if confirmed. |
| **RET-08** | Doctor and clinic verification evidence and decisions | While listed **+ 3 years** after leaving | DEL-RESTRICT, then delete | Proof that we checked registration (C10, K-CS-01) if a claim arises [CONFIRM]. **ID document images: deleted 90 days after the decision**; we keep the result and the document type only (minimisation, C23) [CONFIRM]. |
| **RET-09** | Grievances and data-rights requests | **3 years after closure** | DEL-JOB | Evidence for C02, C08 and C19 SLA reports [VALIDATE]. |
| **RET-10** | Message delivery logs (SMS, push, email) | **90 days** | DEL-ROLL | Operational troubleshooting only [CONFIRM]. Opt-in records follow RET-05. |
| **RET-11** | Product analytics events (random `user_key`, no health data) | **13 months**, then aggregated with no `user_key` | DEL-ROLL; `user_key` link removed on erasure | docs/metrics-and-events.md; year-on-year comparison [CONFIRM]. |
| **RET-12** | Short-lived technical data: OTPs, slot holds, video room tokens, search location | Minutes to the end of the session; **search location is never stored** | DEL-TTL | Minimisation. |
| **RET-13** | Waitlist contacts | Until unsubscribe, or **6 months after v1.0 launch** | DEL-JOB | Purpose ends at launch [CONFIRM]. |
| **RET-14** | v0 pilot data | **30 days after the pilot ends**, unless the patient joins the app and consents again | DEL-JOB | Promise in the v0 consent form. The doctor's paper prescription is the legal record, not our copy. |
| **RET-15** | Nominee details | Account life; after a nominee request, 3 years after it closes | DEL-ERASE / DEL-JOB | C02, E-DP-10. |
| **RET-16** | Under-18 sign-up attempts | Nothing kept except a daily count | none | Founder decision 3; C04. |
| **RET-17** | Backups (encrypted, India) | **35 days rolling** [CONFIRM in P05] | Expire; erased users' data unreadable by key destruction | K-DP-07, E-DP-04. |

## 2. Why clinical records are kept for 3 years (acceptance criterion)

1. **Doctors must keep records.** The Indian Medical Council (Professional Conduct, Etiquette and Ethics) Regulations 2002, reg. 1.3.1, require records to be kept for **3 years from the start of treatment**; reg. 1.3.2 requires copies on request within 72 hours. The text speaks of indoor patients; it is widely applied to all consultations [VALIDATE]. The NMC 2023 regulations that would have replaced it are on hold (S-008, C11).
2. **The Telemedicine Practice Guidelines 2020** require the doctor to keep the teleconsultation log, records, images and prescriptions. They set no separate period, so the 2002 period applies [VALIDATE].
3. **DPDP allows keeping data when the law requires it** (s.8(7)), so a patient's erasure request cannot remove these records during the period. We restrict them instead: hidden from the app, reachable only by the patient, the treating doctor or a legal order (E-DP-03, K-DP-07).
4. **Claims can come later.** A consumer complaint must usually be filed within 2 years of the cause of action (Consumer Protection Act 2019 s.69), and the Limitation Act 1963 gives 3 years for most suits. A negligence claim can start from when harm is discovered, so 3 years is a floor, not a guarantee [VALIDATE].
5. **Proposal:** 3 years after the **last** consultation with that doctor, rather than after the first, so a long-running treatment is never cut off midway, plus a **legal hold** that pauses deletion while a complaint, dispute or legal order is open. The lawyer may advise longer (for example 5 years) for negligence risk; changing the period is one config value [CONFIRM].
6. **Open legal question:** under DPDP, is the doctor or the platform the "data fiduciary" for clinical records, and do we need a processing agreement with each doctor? This decides who must answer erasure and correction requests for notes. **Lawyer (S-009).**

## 3. Deletion paths

| Path | What happens | Built in |
|---|---|---|
| **DEL-SELF** | The user deletes the item; removed from every store, including object storage and the search index. | S-106 to S-108, records UI |
| **DEL-ERASE** | Account-erasure job (K-DP-07): database, search index, caches, object storage, notification records, analytics `user_key` link, and deletion requests to each processor (DEL-VENDOR). Non-retained data in backups becomes unreadable by destroying keys. Ends with a report listing what was kept and why. | S-060 |
| **DEL-RESTRICT** | Data under a legal duty is moved to a restricted state, invisible in the app, then hard-deleted on its date by a scheduled job. Legal holds pause the job. | S-060, S-062 |
| **DEL-TTL** | Automatic expiry (cache or database time-to-live). | per module |
| **DEL-ROLL** | Rolling retention window in log, analytics or message stores. | S-059, S-095 |
| **DEL-JOB** | Scheduled purge for a whole data set (waitlist, pilot, closed tickets). | ops |
| **DEL-VENDOR** | Written deletion request to a processor, with confirmation stored. | S-060, vendor register |

**Design point for P05/P06 (S-056, S-062):** crypto-shredding uses per-patient keys (ADR-12), but restricted clinical records must stay readable for their period. Records under RET-02 and RET-04 therefore need a **separate retention key** per patient (or per patient and doctor) that is destroyed only when their period ends. Otherwise erasing the patient key would destroy records the law requires us to keep.

## 4. Inventory

**Class** (proposed; S-044 confirms): **S** = sensitive health · **P** = personal · **F** = financial · **T** = technical · **Pub** = public by design.
**Basis:** **C** = consent (purpose ID from docs/privacy/consent-texts.md) · **L** = legal obligation · **D** = doctor/clinic agreement (purposes D-VERIFY, D-PROFILE, D-PAY, proposed; doctor privacy notice still to be written, see section 6).

### 4.1 identity (accounts, sessions, devices)
| Field | Subject | Class | Purpose | Basis | Retention | Deletion |
|---|---|---|---|---|---|---|
| Mobile number | Patient, doctor, clinic admin | P | P-ACCOUNT / D-PROFILE | C / D | RET-01 | DEL-ERASE |
| Full name | All users | P | P-ACCOUNT / D-PROFILE | C / D | RET-01 | DEL-ERASE |
| Date of birth | Patient | P | P-ACCOUNT (18+ check); P-CARE (age on prescription) | C | RET-01; copy in each prescription follows RET-02 | DEL-ERASE |
| Sex | Patient | P | P-CARE (prescription format) [CONFIRM it is needed] | C | RET-01; prescription copy RET-02 | DEL-ERASE |
| Preferred language | All users | P | P-ACCOUNT | C | RET-01 | DEL-SELF / DEL-ERASE |
| Role(s), account status | All users | T | P-ACCOUNT / D-PROFILE | C / D | RET-01 | DEL-ERASE |
| Device ID, push token, app version | All users | T | P-ACCOUNT (secure sign-in, remote sign-out, E-DP-07) | C | RET-01 (token replaced on change) | DEL-ERASE |
| Session tokens | All users | T | P-ACCOUNT | C | RET-12 | DEL-TTL |
| OTP codes and attempt counts | All users | T | P-ACCOUNT (rate limiting) | C | RET-12 | DEL-TTL |
| MFA factor | Doctor, clinic admin, staff | T | D-PROFILE (K-SEC-03) | D | RET-01 | DEL-ERASE |
| Clinic staff account: name, mobile, work email (optional), role, clinic | Clinic staff | P | D-STAFF (staff account, role-limited access) | C (D-STAFF@1) | RET-01; deleted when removed by the clinic, or 30 days after the clinic leaves | DEL-ERASE |
| Pending invitation: name, mobile, role, inviting clinic | Invited doctor or staff (third party) | P | Send one invitation | D (clinic confirms it told them, Provider Agreement B1) | Deleted 30 days after sending if not accepted | DEL-TTL |
| Age-gate rejection count | none (count only) | T | Reporting | n/a | RET-16 | n/a |

### 4.2 records (consent, sharing, uploads, access log, rights)
| Field | Subject | Class | Purpose | Basis | Retention | Deletion |
|---|---|---|---|---|---|---|
| Consent record: user key, purpose ID, text version, notice version, action, time, channel, evidence ref | Patient | P | Proof of consent (K-DP-02) | L | RET-05 | DEL-RESTRICT |
| History-sharing grant: doctor, scope, start, expiry, revoked time | Patient, doctor | S | P-SHARE | C | RET-06 | DEL-RESTRICT |
| Uploaded document (file) and metadata (title, type, date) | Patient | S | P-RECORD | C | RET-03 | DEL-SELF / DEL-ERASE |
| Virus-scan result | Patient | T | P-RECORD | C | RET-03 | with the document |
| Record access log: who, when, record ref, reason | Patient, doctor, staff | S | Security; shown to the patient (R-83) | C / L | RET-06 | DEL-RESTRICT |
| Nominee: name, relation, phone | Nominee (third party) | P | Data-rights nominee (C02) | L | RET-15 | DEL-ERASE |
| Data-rights request: type, dates, outcome | Patient | P | C02 compliance | L | RET-09 | DEL-JOB |

### 4.3 directory (doctor and clinic profiles)
| Field | Subject | Class | Purpose | Basis | Retention | Deletion |
|---|---|---|---|---|---|---|
| Doctor display name, specialty, qualifications, languages | Doctor | Pub | D-PROFILE | D | While listed | Removed from search on leaving; verification copy RET-08 |
| Registration number and council (shown on profile and prescription, R-15) | Doctor | Pub | D-PROFILE; legal display (C09, C10) | D / L | While listed; prescription copy RET-02 | as above |
| Profile photo | Doctor | Pub | D-PROFILE | D | While listed | DEL-ERASE. **Check the NMC Oct 2026 advertising guidelines before display (C11, open decision 7).** |
| Fee, consult modes, timings | Doctor, clinic | Pub | D-PROFILE | D | While listed | Removed on leaving |
| Clinic name, address, map location, phone | Clinic | Pub | D-PROFILE; radius search (R-20) | D | While listed | Removed on leaving |
| Clinic registration documents, HFR ID | Clinic | P | D-VERIFY (C24) | D / L | RET-08 | DEL-RESTRICT |

### 4.4 verification (F17-a)
| Field | Subject | Class | Purpose | Basis | Retention | Deletion |
|---|---|---|---|---|---|---|
| Registration number, council, year, qualification | Doctor | P | D-VERIFY (C10) | D / L | RET-08 | DEL-RESTRICT |
| Identity document image | Doctor | P | D-VERIFY | D | **90 days after decision** (RET-08) | DEL-JOB. **Aadhaar card not accepted; if one is received, the reviewer rejects it and it is deleted at once (AAD-04 in docs/privacy/aadhaar-policy.md; C23, K-DP-05; open decision 16).** |
| Verification photo | Doctor | P | D-VERIFY | D | RET-08 | DEL-RESTRICT |
| Automatic check results and registry snapshots | Doctor, clinic | P | D-VERIFY | D / L | RET-08 | DEL-RESTRICT |
| Decision, reviewer, reasons, four-eyes override | Doctor, clinic, staff | P | D-VERIFY (K-CS-01) | L | RET-08 | DEL-RESTRICT |
| Re-verification dates and outcomes | Doctor | P | D-VERIFY (R-13) | L | RET-08 | DEL-RESTRICT |

### 4.5 booking (F1-a)
| Field | Subject | Class | Purpose | Basis | Retention | Deletion |
|---|---|---|---|---|---|---|
| Appointment: patient ref, doctor, clinic, slot, type, status | Patient, doctor | P (S in context: reveals a specialty) | P-CARE | C | RET-01; if it led to a consultation, RET-02 | DEL-ERASE / DEL-RESTRICT |
| Slot hold | Patient | T | P-CARE (no double booking) | C | RET-12 | DEL-TTL |
| Cancellation or reschedule reason | Patient, doctor | P | P-CARE (refund rules, R-32) | C | RET-01; refund-related copy RET-04 | DEL-ERASE |
| Reason for visit (free text) | Patient | S | P-CARE | C | RET-02 | DEL-RESTRICT. **Not in requirements; [CONFIRM] whether we collect it at all.** |
| Doctor availability rules | Doctor | T | D-PROFILE | D | While listed | Removed on leaving |

### 4.6 payments (F6-a)
| Field | Subject | Class | Purpose | Basis | Retention | Deletion |
|---|---|---|---|---|---|---|
| Order and payment ID, amount, status, payment-company reference | Patient | F | P-CARE; tax | C / L | RET-04 | DEL-RESTRICT |
| **Card, UPI or bank details** | none | none | **Never stored** (K-PAY-01, C18) | none | none | none |
| Refund status and reason | Patient | F | P-CARE (R-61) | C / L | RET-04 | DEL-RESTRICT |
| Invoice: name, amount, invoice number, GST details | Patient, clinic | F | Tax (C20) | L | RET-04 | DEL-RESTRICT |
| Clinic billing details and GSTIN (clinic-collected fallback, R-62) | Clinic | F | D-PAY | D / L | RET-04 | DEL-RESTRICT |
| Linked-account reference at the payment company (split mode only) | Clinic | F | D-PAY | D | RET-04 | DEL-RESTRICT |
| Reconciliation records and webhooks | none (references only) | T/F | Tax, integrity (R-63) | L | RET-04 | DEL-RESTRICT |

### 4.7 consult (F1-b)
| Field | Subject | Class | Purpose | Basis | Retention | Deletion |
|---|---|---|---|---|---|---|
| Room ID and join tokens | Patient, doctor | T | P-CARE | C | RET-12 | DEL-TTL |
| Join and leave times, duration | Patient, doctor | P | P-CARE; teleconsult record (C09) | C / L | RET-02 | DEL-RESTRICT |
| Teleconsultation consent (time, booking ID) | Patient | S | C09 | L | RET-02 | DEL-RESTRICT |
| Connection quality metrics | none (pseudonymous) | T | Service quality | C | RET-07 | DEL-ROLL |
| **Video or audio recording** | none | none | **Not recorded** (ADR-13) | none | none | none |

### 4.8 clinical (F1-c)
| Field | Subject | Class | Purpose | Basis | Retention | Deletion |
|---|---|---|---|---|---|---|
| Clinical notes, with signed versions (K-CS-05) | Patient | S | P-CARE / P-RECORD | C / L | RET-02 | DEL-RESTRICT |
| Diagnosis or provisional diagnosis | Patient | S | P-CARE | C / L | RET-02 | DEL-RESTRICT |
| Prescription items: drug, dose, frequency, duration, instructions, injections, tests | Patient | S | P-CARE | C / L | RET-02 | DEL-RESTRICT |
| Rules-engine decision and block reason | Patient, doctor | S | Safety (K-CS-02, C09, C13) | L | RET-02 | DEL-RESTRICT |
| Prescription PDF | Patient | S | P-CARE / P-RECORD | C / L | RET-02 | DEL-RESTRICT |
| Doctor signing record (who, when, key ref) | Doctor | P | Integrity (C12, proposed row N5) | L | RET-02 | DEL-RESTRICT |
| Prescription templates | Doctor | T | D-PROFILE (R-53) | D | While listed | DEL-ERASE |

### 4.9 scheduler-core (separate service, F10-a)
| Field | Subject | Class | Purpose | Basis | Retention | Deletion |
|---|---|---|---|---|---|---|
| Opaque activity ref, recurrence rule, time zone | Patient (pseudonymous) | P | P-REMIND | C | Until the schedule ends or the account closes | DEL-ERASE. **No health data by design** (R-75): reminder wording is built by notifications at send time. |
| Occurrences and dispatch status | Patient (pseudonymous) | T | P-REMIND | C | Rolling window + 90 days | DEL-ROLL |

### 4.10 notifications
| Field | Subject | Class | Purpose | Basis | Retention | Deletion |
|---|---|---|---|---|---|---|
| Channel choice per reminder type; medicine-names flag | Patient | P | P-REMIND, P-MEDNAMES | C | RET-01 | DEL-ERASE |
| SMS delivery log: phone, DLT template ID, status | Patient | P | P-REMIND, P-ACCOUNT | C | RET-10 | DEL-ROLL |
| Push delivery log | Patient | T | P-REMIND | C | RET-10 | DEL-ROLL |
| WhatsApp opt-in (time, source) and delivery log | Patient | P | P-WHATSAPP | C | RET-05 / RET-10 | **ON HOLD: not collected until WhatsApp resumes** |

### 4.11 admin and audit
| Field | Subject | Class | Purpose | Basis | Retention | Deletion |
|---|---|---|---|---|---|---|
| Grievance: reference, contact, text, dates, outcome | Patient, doctor | P (may contain S) | C02, C08, C19 | L | RET-09 | DEL-JOB |
| Support ticket | Any user | P (may contain S) | Support | C | RET-09 | DEL-JOB |
| Staff just-in-time access: staff, reason, scope, start and end | Staff, patient | P | K-SEC-08 | L | RET-06 | DEL-RESTRICT |
| Staff accounts (name, work email, role, MFA) | Staff | P | Employment, security | L | Employment + 1 year [CONFIRM] | DEL-JOB |
| Audit events (actor, action, subject ref, time, hash chain) | All | P | Tamper-evident audit | L | Longest of the related records | DEL-RESTRICT. The hash chain must survive deletion (store references, not content). |

### 4.12 analytics, logs, marketing, v0
| Field | Subject | Class | Purpose | Basis | Retention | Deletion |
|---|---|---|---|---|---|---|
| Analytics `user_key` (random) and events | Patient, doctor | P (pseudonymous) | P-ANALYTICS (opt-in per S-010 draft) | C | RET-11 | DEL-ROLL; link deleted on erasure |
| IP address, user agent, request ID, scrubbed error traces | All users | T | Security (C07) | L | RET-07 | DEL-ROLL |
| Waitlist contact (email or phone), time, text version | Prospect | P | P-WAITLIST | C | RET-13 | DEL-JOB |
| v0: name, phone, transcribed prescription, reminder schedule, feedback notes, consent form scan | Pilot patient | S | V0-PILOT, V0-FEEDBACK | C | RET-14 | DEL-JOB, with a deletion record kept |

## 5. Processors (vendor register skeleton, C06 and K-DP-08)

No vendor receives personal data until it has a processor agreement and appears here.

| Processor type | Data sent | Location | Agreement | Chosen in |
|---|---|---|---|---|
| Cloud hosting (two India regions) | All, encrypted | India | [FILL] | ADR-09, P05 |
| Managed identity (OTP, MFA) | Phone, device, session | India (ADR-11) | [FILL] | ADR-11 |
| SMS provider (DLT) | Phone, template text (discreet) | [FILL] | [FILL] | S-041 |
| Push (FCM / APNs) | Push token, discreet text | Outside India (Google, Apple) [VALIDATE against decision 4: no health data in pushes] | Platform terms | ADR-15 |
| Video provider | Room tokens, session metadata | India region (ADR-13) | [FILL] | spike a08 |
| Payment aggregator | Amount, reference, phone | India | [FILL] | S-040 |
| Verification API (if bought) | Doctor registration details | [FILL] | [FILL] | S-042 |
| Geocoding | Clinic address (not patient location) | [FILL] | [FILL] | ADR-16 |
| Email provider | Email, discreet text | [FILL] | [FILL] | P05 |
| WhatsApp provider | Phone, discreet text | **ON HOLD** | n/a | S-041 (paused) |
| v0 pilot tools | Pilot data | **Must be India-hosted** (v0 form promise) | [FILL] | open decision 5 |

## 6. Acceptance criteria check

| Criterion | Met? | Evidence |
|---|---|---|
| Inventory complete for v1.0 fields | **Yes, as a draft.** All v1.0 modules (identity, records, directory, verification, booking, payments, consult, clinical, scheduler-core, notifications, admin, audit, analytics, logs) plus waitlist and v0 are covered; each field has a purpose, basis, retention rule and deletion path. | Section 4 |
| Medical-record retention justified | **Yes, pending the lawyer.** | Section 2 (3 years after the last consultation, plus legal hold) |

**Story status:** In Testing. It is Done when the founder approves, the lawyer confirms RET-02, RET-05, RET-06, RET-09 and the fiduciary question, and the CA confirms RET-04. New fields get added here in their own PRs (K-DP-06).

## 7. Open decisions and follow-ups

1. **RET-02 period:** 3 years after the last consultation plus legal hold, or longer on the lawyer's advice?
2. **Who is the data fiduciary for clinical records**, the doctor or us? Do we need a processing agreement with each doctor? (Lawyer, S-009.)
3. **RET-04:** 8-year tax retention. (CA.)
4. **Inactive accounts:** erase after 3 years without sign-in? [CONFIRM]
5. **ID images:** delete 90 days after the verification decision; Aadhaar handling (open decision 16).
6. **Do we collect "reason for visit" and patient sex?** Collect only if needed.
7. **Retention-key design** for restricted records (section 3) goes to S-056 and S-062.
8. **Log retention:** if the DPDP 1-year minimum is confirmed, ADR-21 and K-SEC-01 (180 days) need an ADR to move to 1 year (contracts are not edited directly, K-DOD-03).
9. **Doctor and clinic privacy notice** (purposes D-VERIFY, D-PROFILE, D-PAY) is still missing; suggested as a new story before S-110 (also raised in S-010).
10. **Push notifications go through Google and Apple, outside India.** This is acceptable under decision 4 only if push text never contains health data (K-DP-09 discreet mode); confirm in S-079.
11. **Backup window** of 35 days and analytics window of 13 months. [CONFIRM in P05]

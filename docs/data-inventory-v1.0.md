# Data inventory and retention schedule, v1.0 (S-011) — DRAFT for founder, lawyer and CA review

**Phase P01 · Epic EP02 · Depends on:** S-008 · **Compliance:** C01, C06, C30 (also C02, C03, C07, C20, C23, N1) · **Contract:** K-DP-06 · **Edge case:** E-DP-03
**Date:** 2026-11-14 · **Not legal advice.** **[VALIDATE]** = legal point to confirm; **[CONFIRM]** = proposal for the founder.

**Who this is for and what it decides:** for the founder, lawyer and CA, to approve how long each kind of data is kept and how it is deleted. For engineering it is the register K-DP-06 points to: **any PR that adds a personal-data field must add a row here** (purpose, basis, retention, deletion path). Sensitivity classes were confirmed in S-044 (section 4.0), with handling rules per class. **A new field also needs a class.**

**Inputs:** requirements v1.0 (R-xx), privacy notice and purpose IDs (docs/privacy/), metrics and events, Architecture sheet (module map, ADRs), Contracts and Edge Cases sheets, S-008 review. **WhatsApp is on hold** (founder, 14 Nov 2026), so WhatsApp fields are listed but marked ON HOLD.

---

## 1. Retention rules

Each field below points to one of these rules.

| Rule | Data | How long | Then | Why / source |
|---|---|---|---|---|
| **RET-01** | Account and profile data | While the account is active | On closure: **restricted under the legal-minimum hold (RET-18)**, then erased (DEL-ERASE) | DPDP s.8(7): erase when the purpose ends. **Inactive accounts:** propose erasure after 3 years with no sign-in, with 48 hours' notice first [CONFIRM]. The DPDP Rules' 3-year inactivity rule is for very large platforms only and does not bind us [VALIDATE]. |
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
| **RET-18** | **Legal-minimum hold** after erasure or account closure (added 2026-11-16, compliance review R-01) | **Registration data** (phone, name, date of birth, device, sign-up records): **180 days after the account is closed** (Intermediary Rules 2021, rule 3(1)(h); applies if we are an intermediary [VALIDATE]). **From 13 May 2027:** personal data, associated traffic data and processing logs **at least 1 year from the date of processing** (DPDP Rules 2025, rule 8(3), Seventh Schedule purposes) [VALIDATE]. Content we remove for breaking the rules: 180 days (rule 3(1)(g)). | **Restricted:** hidden from the app and from all staff roles; released only for a verified legal order (lawful-request procedure). Then hard-deleted. The patient key is destroyed only when the hold ends. | DPDP s.8(7) allows keeping data that the law requires. The longest applicable period wins. |
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
| **DEL-ERASE** | Account-erasure job (K-DP-07). **Step 1, at once:** data hidden from the app, search index and caches cleared, processing stopped, analytics `user_key` link removed; data under the legal-minimum hold (RET-18) restricted. **Step 2, when the hold ends:** database rows, object storage and notification records deleted; deletion requests sent to each processor (DEL-VENDOR); patient key destroyed, so copies in backups become unreadable. Ends with a report listing what was kept, why and until when. | S-060 |
| **DEL-RESTRICT** | Data under a legal duty is moved to a restricted state, invisible in the app, then hard-deleted on its date by a scheduled job. Legal holds pause the job. | S-060, S-062 |
| **DEL-TTL** | Automatic expiry (cache or database time-to-live). | per module |
| **DEL-ROLL** | Rolling retention window in log, analytics or message stores. | S-059, S-095 |
| **DEL-JOB** | Scheduled purge for a whole data set (waitlist, pilot, closed tickets). | ops |
| **DEL-VENDOR** | Written deletion request to a processor, with confirmation stored. | S-060, vendor register |

**Design point for P05/P06 (S-056, S-062):** crypto-shredding uses per-patient keys (ADR-12), but restricted clinical records must stay readable for their period. Records under RET-02 and RET-04 therefore need a **separate retention key** per patient (or per patient and doctor) that is destroyed only when their period ends. Otherwise erasing the patient key would destroy records the law requires us to keep.

## 4. Inventory

### 4.0 Data classification (S-044, confirmed 2026-11-15)

**Legal basis for the scheme:**
- **DPDP Act 2023** protects all **personal data**: any data about an individual who can be identified from it, directly or with other data. It has no separate "sensitive" category, so every class below except Public and Internal is personal data, and **every breach of it must be notified** (DPDP Rules, rule 7).
- **SPDI Rules 2011** (IT Act s.43A) still treat **health conditions and medical records** as sensitive personal data until the DPDP Act's repeal of s.43A takes effect (expected 13 May 2027) [VALIDATE; N1].
- **K-DP-03** requires field-level encryption for clinical notes, diagnoses, prescriptions, documents and contact details.

"Sensitive health" is our own higher-protection class, built on all three. **Pseudonymous data** (random keys, opaque references) is still personal data, because we hold the mapping.

| Class | Code | What it means | Examples |
|---|---|---|---|
| **Public** | PUB | Published on purpose | Doctor public profile, clinic address, fees, timings |
| **Internal** | INT | Business data about no individual | Rule files, drug master, config, aggregate counts, virus-scan verdicts |
| **Personal** | PER | Identifies a person, but reveals no health information | Name, phone, date of birth, device ID, IP address, consent records, pseudonymous analytics key |
| Personal, financial | PER-F | Personal data under tax and payment rules | Orders, refunds, receipts, GSTIN of a sole proprietor |
| Personal, credential | PER-K | Secrets that let someone act as the user | OTPs, session and refresh tokens, MFA secrets, video join tokens |
| **Sensitive health, link** | SH-L | Reveals that a person receives or seeks care, or from whom, without clinical content | Appointment (reveals the specialty), slot hold, consult join times, sharing grants, record access log, audit events |
| **Sensitive health, content** | SH-C | Clinical content or free text that may contain it | Notes, diagnoses, prescriptions, prescription PDFs, uploads, reason for visit, grievance and support text, v0 pilot data |
| **Prohibited** | — | Never collected or stored | Aadhaar number or VID (K-DP-05), card, UPI or bank credentials (K-PAY-01), biometrics, call recordings |

**Handling rules by class.** These are the rules engineering builds to; S-056 to S-059 design the details.

| Rule | PUB | INT | PER / PER-F | PER-K | SH-L | SH-C |
|---|---|---|---|---|---|---|
| Encrypted at rest (disk and database) | Yes | Yes | Yes | Yes | Yes | Yes |
| Field-level encryption (K-DP-03) | No | No | **Contact details only** (phone, email), with a keyed hash (blind index) for sign-in lookup | Hashed (OTPs, tokens) or KMS-encrypted (MFA secrets) | Not required by K-DP-03; S-056 decides per field | **Required**, with the per-patient key; clinical records also under the retention key (§3) |
| In logs, traces, crash reports | Allowed | Allowed | **No.** Only the pseudonymous user key and request IDs | **Never** | **Never** | **Never** |
| In product analytics | Allowed | Allowed | Pseudonymous key only (P-ANALYTICS) | Never | Never | Never |
| In notifications (lock screen, SMS) | Allowed | n/a | Own first name at most | Never, except the OTP in SMS-01 | **Never** (no doctor specialty in reminder text) | **Never**, except a medicine name with P-MEDNAMES |
| In search index or caches | Allowed | Allowed | Not indexed; caches encrypted, short TTL | Never cached in plaintext | Not indexed; encrypted cache, short TTL | Never indexed; encrypted cache only |
| Who can read | Anyone | Staff by role | The person, plus roles that need it | System only | Access tiers T1 to T4 and roles; **every non-patient read audited** (K-DP-02) | Same as SH-L, plus the K-DP-02 consent check |
| Non-production (dev, staging, tests) | Synthetic | Allowed | **Synthetic only, never real data** | Synthetic | Synthetic only | Synthetic only |
| Included in the patient's data export | n/a | No | Yes | **No** | Yes | Yes |
| Erasure | Removed on leaving | n/a | DEL-ERASE (RET-01) | DEL-TTL | DEL-ERASE, or DEL-RESTRICT when tied to a clinical record | DEL-RESTRICT under RET-02, otherwise DEL-ERASE |
| Breach notification (DPDP rule 7; CERT-In 6 h for reportable incidents) | No | Usually no | **Yes** | **Yes**, and rotate at once | **Yes** | **Yes** |

**Mixed views take the highest class of anything shown.** For example, a payment row shown with the doctor's name is SH-L.

**Notes from classification:**
1. **Free text is SH-C by default.** Cancellation reasons should be a pick-list (then PER). Any optional free-text box is SH-C.
2. **scheduler-core** holds only PER (pseudonymous) data. Its `typeKey` must stay a coarse category (for example `medication`, `appointment`), never a drug or condition. The subject mapping lives only in the core database.
3. **Verification photos** are PER, not biometric data, **as long as no automated face matching is used.** Adding face matching would need a new review (DPDP and the provider notice).
4. **SMS delivery logs** store the template ID and status, **never the variables**. A medicine name sent with SMS-10-N must not be logged.

**Legend for the "Class" column below** (replaces the earlier draft letters; S = SH-C or SH-L, P = PER, F = PER-F, T = INT or PER-K, Pub = PUB).
**Basis:** **C** = consent (purpose ID from docs/privacy/consent-texts.md) · **L** = legal obligation · **D** = doctor/clinic agreement (purposes D-VERIFY, D-PROFILE, D-PAY, proposed; doctor privacy notice still to be written, see section 6).

### 4.1 identity (accounts, sessions, devices)
| Field | Subject | Class | Purpose | Basis | Retention | Deletion |
|---|---|---|---|---|---|---|
| Mobile number | Patient, doctor, clinic admin | PER (field-level encrypted + blind index) | P-ACCOUNT / D-PROFILE | C / D | RET-01 | DEL-ERASE |
| Full name | All users | PER | P-ACCOUNT / D-PROFILE | C / D | RET-01 | DEL-ERASE |
| Date of birth | Patient | PER | P-ACCOUNT (18+ check); P-CARE (age on prescription) | C | RET-01; copy in each prescription follows RET-02 | DEL-ERASE |
| Sex | Patient | PER | P-CARE (prescription format) [CONFIRM it is needed] | C | RET-01; prescription copy RET-02 | DEL-ERASE |
| Preferred language | All users | PER | P-ACCOUNT | C | RET-01 | DEL-SELF / DEL-ERASE |
| Role(s), account status | All users | PER | P-ACCOUNT / D-PROFILE | C / D | RET-01 | DEL-ERASE |
| Profile storage of name, date of birth and sex (added 2026-11-17, S-108): each sealed with the patient key in `identity.profile` (no new field; this row records the storage and the adult-only check, K-DP-10) | Patient | PER (field-level encrypted) | P-ACCOUNT (18+ check); P-CARE | C | RET-01 | DEL-ERASE (key shredded) |
| Blood group (added 2026-11-17, S-108) | Patient | SH-C (field-level encrypted with the patient key; kept in identity because section 4.2 has no profile rows, and the encryption design puts profile health fields under the patient key) | P-CARE (profile the patient keeps; shown to a doctor only through a records permission) **[CONFIRM purpose ID and that it is needed in v1.0]** | C | RET-01 | DEL-SELF / DEL-ERASE (key shredded) |
| Allergies, free-text list of up to 20 short entries (added 2026-11-17, S-108); Aadhaar-like numbers are rejected | Patient | SH-C (field-level encrypted with the patient key) | P-CARE (as above) **[CONFIRM]**; no allergy checking is done by the software (K-CS-03) | C | RET-01 | DEL-SELF / DEL-ERASE (key shredded) |
| Emergency contact: name and mobile number (added 2026-11-17, S-108) | Patient; the contact is a third party | PER (field-level encrypted with the patient key, as for any contact detail) | P-CARE (for use by the patient's own SOS flow in v1.1; nothing is sent to the contact in v1.0, K-CS-06) **[CONFIRM purpose; VALIDATE the third-party notice]** | C | RET-01 | DEL-SELF / DEL-ERASE (key shredded) |
| Device ID, push token, app version | All users | PER | P-ACCOUNT (secure sign-in, remote sign-out, E-DP-07) | C | RET-01 (token replaced on change) | DEL-ERASE |
| Session tokens | All users | PER-K | P-ACCOUNT | C | RET-12 | DEL-TTL |
| OTP codes and attempt counts | All users | PER-K (hashed) | P-ACCOUNT (rate limiting) | C | RET-12 | DEL-TTL |
| Device binding public key (added 2026-11-17, S-104) | All users | PER (public key, not secret; identifies a device) | P-ACCOUNT (device binding, K-SEC-03; E-DP-22) | C | RET-01 | DEL-ERASE |
| OTP and sign-in limit counters: keyed hashes of phone and IP, counts, lock-until time (added 2026-11-17, S-104) | All users | PER-K (keyed hashes only) | P-ACCOUNT (abuse prevention, TM-01, TM-02) | C | RET-12 (counters: their window; locks: until expiry) | DEL-TTL |
| Keyed hash of client IP in audit events (added 2026-11-17, S-096) | All users | PER (pseudonymous) | Security and audit (K-SEC-01) | Legal obligation / legitimate security use [VALIDATE] | RET-06 | Kept with the audit event; the hash key is per environment |
| HTTP idempotency replay cache: stored API responses, encrypted with a service key (added 2026-11-17, S-090) | All users | PER-K / as the response | Prevent duplicate actions (API standards §6) | C | 24 hours | DEL-TTL |
| MFA factor | Doctor, clinic admin, staff | PER-K | D-PROFILE (K-SEC-03) | D | RET-01 | DEL-ERASE |
| Clinic staff account: name, mobile, work email (optional), role, clinic | Clinic staff | PER | D-STAFF (staff account, role-limited access) | C (D-STAFF@1) | RET-01; deleted when removed by the clinic, or 30 days after the clinic leaves | DEL-ERASE |
| Pending invitation: name, mobile, role, inviting clinic | Invited doctor or staff (third party) | PER | Send one invitation | D (clinic confirms it told them, Provider Agreement B1) | Deleted 30 days after sending if not accepted | DEL-TTL |
| Age-gate rejection count | none (count only) | INT | Reporting | n/a | RET-16 | n/a |

### 4.2 records (consent, sharing, uploads, access log, rights)
| Field | Subject | Class | Purpose | Basis | Retention | Deletion |
|---|---|---|---|---|---|---|
| Consent record: user key, purpose ID, text version, notice version, action, time, channel, evidence ref | Patient | PER | Proof of consent (K-DP-02) | L | RET-05 | DEL-RESTRICT |
| History-sharing grant: doctor, scope, start, expiry, revoked time | Patient, doctor | SH-L | P-SHARE | C | RET-06 | DEL-RESTRICT |
| History-share extension request: patient, doctor, clinic, status, requested, expiry and decision times, resulting grant (added 2026-11-20, S-168; references and times only, E-DP-02) | Patient, doctor | SH-L | P-SHARE (evidence of the request and the patient's decision) | C | RET-06, as the grant it extends [CONFIRM] | DEL-RESTRICT, as the grant [CONFIRM] |
| Uploaded document (file) and metadata (title, type, date) | Patient | SH-C | P-RECORD | C | RET-03 | DEL-SELF / DEL-ERASE |
| Virus-scan result | Patient | INT | P-RECORD | C | RET-03 | with the document |
| Record access log: who, when, record ref, reason | Patient, doctor, staff | SH-L | Security; shown to the patient (R-83) | C / L | RET-06 | DEL-RESTRICT |
| Nominee: name, relation, phone | Nominee (third party) | PER (contact: field-level encrypted) | Data-rights nominee (C02) | L | RET-15 | DEL-ERASE |
| Data-rights request: type, dates, outcome | Patient | PER | C02 compliance | L | RET-09 | DEL-JOB |
| Account restriction (erasure under the legal-minimum hold): user key, request, time | Patient | INT | Hide the account from app and staff during RET-18 | L | RET-18 | DEL-RESTRICT |
| Erasure journal (write-once storage, outside the database): request ref, user key, event (requested, cancelled, completed), request, undo and event times; plus which entries are written (`journaled`) on the request row (added 2026-11-23, S-185, D-74; references and times only) | Patient | INT (pseudonymous references) | Replay erasures after a backup restore so a restored account stays hidden or erased (E-DP-04, K-DP-07; backup-and-restore.md section 2) | L | **Kept** while backups that predate the request can exist, as the key destruction log (backup-and-restore.md section 1) [CONFIRM: delete entries 35 days after completion or undo, once no older backup exists] | DEL-RESTRICT (write-once) |
| Secure file link: keyed hash of the token (never the token), user, document or export reference, expiry, used time | Patient, doctor | INT | S-171 short-lived single-use download | C | 24 hours after expiry | DEL-TTL |
| Export file (JSON of the patient's records data, sealed with the patient key) | Patient | SH-C | C02 access and export | L | 24 hours of availability, then not offered; ciphertext removed at final deletion | DEL-ERASE |
| Data-rights request detail: correction text or nominee details (sealed with the patient key); erasure state, undo and hold times, erasure report (codes only) | Patient, nominee (third party) | SH-C (nominee contact: field-level encrypted) | C02 data rights, E-DP-03 report | L | RET-09; detail erased at final deletion (RET-18 hold first) | DEL-ERASE (detail); DEL-RESTRICT (request record) |

### 4.3 directory (doctor and clinic profiles)
| Field | Subject | Class | Purpose | Basis | Retention | Deletion |
|---|---|---|---|---|---|---|
| Doctor display name, specialty, qualifications, languages | Doctor | PUB | D-PROFILE | D | While listed | Removed from search on leaving; verification copy RET-08 |
| Registration number and council (shown on profile and prescription, R-15) | Doctor | PUB | D-PROFILE; legal display (C09, C10) | D / L | While listed; prescription copy RET-02 | as above |
| Profile photo | Doctor | PUB | D-PROFILE | D | While listed | DEL-ERASE. **Check the NMC Oct 2026 advertising guidelines before display (C11, open decision 7).** |
| Fee, consult modes, timings | Doctor, clinic | PUB | D-PROFILE | D | While listed | Removed on leaving |
| Clinic name, address, map location, phone | Clinic | PUB | D-PROFILE; radius search (R-20) | D | While listed | Removed on leaving |
| Clinic branch: name, address, map location, time zone, active flag; practices and availability refer to a branch (added 2026-11-20, S-130; business data, not personal data) | Clinic | PUB | D-PROFILE; each branch located and searched separately (E-GEO-02) | D | While listed | Removed with the clinic; legal retention applies if the clinic is erased |
| Clinic registration documents, HFR ID | Clinic | PER | D-VERIFY (C24) | D / L | RET-08 | DEL-RESTRICT |
| Doctor languages (directory profile) | Doctor | PUB | D-PROFILE | D | While listed | Removed on leaving |
| Recognised qualifications and the specialty they support (RG-04), recorded by verification or admin | Doctor | PUB | D-PROFILE; legal display (C09) | D / L | While listed; verification copy RET-08 | as above |
| Fee version and fee-updated time per practice (booking copies the fee at hold time) | Doctor, clinic | PUB | D-PROFILE | D | While listed | Removed on leaving |
| Clinic location source (geocoded or manual pin), geocoding accuracy, time zone | Clinic | PUB | D-PROFILE; radius search | D | While listed | Removed on leaving |
| Creating user reference of a clinic (opaque ID, `created_by`) | Clinic admin | PER | D-VERIFY; audit | D | RET-08 | DEL-RESTRICT |
| Verification status view: professional or clinic reference, status, time (read model from `verification.status_changed`, B5) | Doctor, clinic | PER | D-PROFILE; K-CS-01 listing check | D / L | While listed; rebuilt from events | Removed on leaving |
| Locality table: postal code and centroid (public geography, no person) | None | PUB | Radius search fallback (R-20) | D | Until replaced | n/a |
| Search location (point or postal code) | Patient | n/a | **Not stored and not logged** (RET-12); used only for the query | n/a | None | n/a |

### 4.4 verification (F17-a)
| Field | Subject | Class | Purpose | Basis | Retention | Deletion |
|---|---|---|---|---|---|---|
| Registration number, council, year, qualification | Doctor | PER | D-VERIFY (C10) | D / L | RET-08 | DEL-RESTRICT |
| Identity document image | Doctor | PER (high risk: ID document) | D-VERIFY | D | **90 days after decision** (RET-08) | DEL-JOB. **Aadhaar card not accepted; if one is received, the reviewer rejects it and it is deleted at once (AAD-04 in docs/privacy/aadhaar-policy.md; C23, K-DP-05; open decision 16).** |
| Verification photo | Doctor | PER (no automated face matching) | D-VERIFY | D | RET-08 | DEL-RESTRICT |
| Automatic check results and registry snapshots | Doctor, clinic | PER | D-VERIFY | D / L | RET-08 | DEL-RESTRICT |
| Decision, reviewer, reasons, four-eyes override | Doctor, clinic, staff | PER | D-VERIFY (K-CS-01) | L | RET-08 | DEL-RESTRICT |
| Re-verification dates and outcomes | Doctor | PER | D-VERIFY (R-13) | L | RET-08 | DEL-RESTRICT |
| Registered name, specialty claim, photo-ID type chosen (verification case fields) | Doctor | PER | D-VERIFY (C10) | D / L | RET-08 | DEL-RESTRICT. Name and claim are plain columns; reviewer sees them; the ID type is a code from the pack allow-list. |
| Clinic verification case fields (D-38): registering authority, registration or establishment number, year, legal name (stored in the same case columns; the clinic ID is the subject reference) | Clinic | PER | D-VERIFY (C24) | D / L | RET-08 | DEL-RESTRICT. Plain columns, seen by the clinic admin and reviewers. No prohibited identifier is ever stored (K-DP-05). |
| Approved prescriber snapshot: registered name, qualifications, council and registration number as approved at the last final "verified" decision (added 2026-11-17, D-44; copies of the case fields above, no new data) | Doctor | PER | D-VERIFY (C10); printed on prescriptions (prescription format, R-15) | D / L | RET-08 | DEL-RESTRICT |
| Clinic evidence: registration certificate, establishment proof (same per-item encryption and metadata as doctor evidence; no photo ID) | Clinic | PER | D-VERIFY (C24) | D / L | RET-08; files deleted 90 days after the decision | DEL-JOB, then DEL-RESTRICT for the metadata. Uploaded and read under the clinic admin's D-VERIFY consent; the acting admin user ID is audited. |
| Evidence file metadata (kind, type, size, state) and the key owner reference | Doctor | PER | D-VERIFY | D | RET-08 | DEL-RESTRICT. The file bytes are encrypted per item; the key is destroyed 90 days after the final decision, or at once for a prohibited ID (AAD-04). |
| Register lookup result: outcome code plus registered name (name sealed with a service key) | Doctor | PER | D-VERIFY | D / L | RET-08 | DEL-RESTRICT |
| Duplicate-registration and name-mismatch flags (reviewer only) | Doctor | PER | D-VERIFY (TM-28) | L | RET-08 | DEL-RESTRICT |

### 4.5 booking (F1-a)
| Field | Subject | Class | Purpose | Basis | Retention | Deletion |
|---|---|---|---|---|---|---|
| Appointment: patient ref, doctor, clinic, slot, type, status | Patient, doctor | SH-L | P-CARE | C | RET-01; if it led to a consultation, RET-02 | DEL-ERASE / DEL-RESTRICT |
| Slot hold | Patient | SH-L | P-CARE (no double booking) | C | RET-12 | DEL-TTL |
| Appointment message markers: when the day-before reminder, the short reminder and the online-consult "starting" notice were handled (added 2026-11-18, S-157; timestamps only, cleared on reschedule) | Patient | SH-L | P-CARE (appointment messages) | C | As the appointment | As the appointment |
| Clinic closure: clinic, branch, date, mode (cancel or reschedule), state, counts, staff reference (added 2026-11-20, S-145; references and counts only) | Clinic, staff | SH-L | E-BK-06 bulk handling of a closed day; refunds follow the stored policy | L | As booking records (RET-01; refund-related RET-04) | DEL-RESTRICT |
| Cancellation or reschedule reason | Patient, doctor | PER if pick-list; SH-C if free text | P-CARE (refund rules, R-32) | C | RET-01; refund-related copy RET-04 | DEL-ERASE |
| Reason for visit (free text) | Patient | SH-C | P-CARE | C | RET-02 | DEL-RESTRICT. **Not in requirements; [CONFIRM] whether we collect it at all.** |
| Doctor availability rules | Doctor | PUB | D-PROFILE | D | While listed | Removed on leaving |
| Appointment times in UTC plus the practice and patient IANA time zones (`patient_time_zone` is the zone the patient booked from, K-ARC-04) | Patient | SH-L | P-CARE | C | RET-01; if it led to a consultation, RET-02 | DEL-ERASE / DEL-RESTRICT |
| Appointment copies of the fee, currency, fee version, settlement mode and refund policy version (K-PAY-05) | Patient, clinic | PER-F | P-CARE; refund rules | C / L | RET-04 | DEL-RESTRICT |
| "I am in India now" confirmation for video and audio bookings (P10), stored with the appointment | Patient | SH-L | P-CARE; telemedicine rules P10 | C / L | RET-02 | DEL-RESTRICT |
| Cancelled-by side, applied refund rule ID, no-show side, reschedule count, needs-reschedule flag, payment order reference | Patient, doctor | PER-F | P-CARE (refund rules) | C | RET-04 | DEL-RESTRICT |
| Reconfirmation task (appointment, clinic, due time, state, who reconfirmed: user ref) | Clinic staff | PER | P-CARE (E-BK-07) | C | RET-01 | DEL-ERASE |
| Weekly availability template, exceptions (leave, closure) and materialised slots (practice, start, mode, state) | Doctor | PUB | D-PROFILE | D | While listed | Removed on leaving |

### 4.6 payments (F6-a)
| Field | Subject | Class | Purpose | Basis | Retention | Deletion |
|---|---|---|---|---|---|---|
| Order and payment ID, amount, status, payment-company reference | Patient | PER-F (SH-L when shown with the doctor) | P-CARE; tax | C / L | RET-04 | DEL-RESTRICT |
| **Card, UPI or bank details** | none | Prohibited | **Never stored** (K-PAY-01, C18) | none | none | none |
| Refund status and reason | Patient | PER-F | P-CARE (R-61) | C / L | RET-04 | DEL-RESTRICT |
| Invoice: name, amount, invoice number, GST details | Patient, clinic | PER-F | Tax (C20) | L | RET-04 | DEL-RESTRICT |
| Clinic billing details and GSTIN (clinic-collected fallback, R-62) | Clinic | PER-F | D-PAY | D / L | RET-04 | DEL-RESTRICT |
| Linked-account reference at the payment company (split mode only) | Clinic | PER-F | D-PAY | D | RET-04 | DEL-RESTRICT |
| Reconciliation records and webhooks | none (references only) | PER-F | Tax, integrity (R-63) | L | RET-04 | DEL-RESTRICT |
| Operation keys and merchant references of orders and refunds (`order:{appointmentRef}`, `refund:{appointmentRef}:{rule}`), policy version applied, payment-attempt IDs (S-124, S-125) | Patient | PER-F | Money correctness, K-PAY-02 | L | RET-04 | DEL-RESTRICT |
| Ledger transactions and entries: signed amounts in minor units, memo account references per clinic, append-only (S-132) | none (references only) | PER-F | Audit, tax (K-PAY-04) | L | RET-04 | DEL-RESTRICT |
| Clinic merchant link: aggregator, settlement mode, **credential references only** (the secret stays in the secrets manager, A8) | Clinic | PER-F | D-PAY | D | RET-04 | DEL-RESTRICT |
| Payment tickets and alerts: kind, clinic ref, subject reference (refund shortfall, reconciliation mismatch, blocked rule, rejected webhook) (E-PAY-05, S-146) | none (references only) | PER-F | Grievance, integrity | L | RET-04 | DEL-RESTRICT |
| Clinic document profile: legal name, address, tax registration status and registration ID (added 2026-11-18, S-134; copied onto each order when it is created) | Clinic; for a sole proprietor the registration ID contains the proprietor's PAN | PER-F (personal data when the clinic is a sole proprietor [VALIDATE]) | Patient documents issued in the clinic's name (money-flow T5) | L / C | RET-04 | DEL-RESTRICT |
| Patient payment document: type, number, date, clinic legal name, address and registration ID (registered clinics only), amount, currency, our payment reference, appointment reference (added 2026-11-18, S-134; no patient name [VALIDATE-CA whether one is required]) | Patient (pseudonymous ref), clinic | PER-F | Receipt or bill of supply for the patient (money-flow T1, T5) | L | RET-04 | DEL-RESTRICT (append-only) |
| Reconciliation run: link, UTC day, line and mismatch counts (S-146) | none | PER-F | Integrity (C17) | L | RET-04 | DEL-RESTRICT |

### 4.7 consult (F1-b)
| Field | Subject | Class | Purpose | Basis | Retention | Deletion |
|---|---|---|---|---|---|---|
| Room ID and join tokens | Patient, doctor | PER-K | P-CARE | C | RET-12 | DEL-TTL |
| Join and leave times, duration | Patient, doctor | SH-L | P-CARE; teleconsult record (C09) | C / L | RET-02 | DEL-RESTRICT |
| Teleconsultation consent (time, booking ID) | Patient | SH-L | C09 | L | RET-02 | DEL-RESTRICT |
| Consult session: appointment, patient, doctor and clinic references, slot times, booked mode and mode now, state, outcome, flags (short call, minor found) (added 2026-11-17, S-135, S-148) | Patient, doctor | SH-L (references and codes only) | P-CARE; teleconsult record (C09) | C / L | RET-02 | DEL-RESTRICT |
| Pre-join record: device check result (camera, microphone, network OK), TELE-CONSENT text ID and version, the patient's "I am in India now" declaration, time (added 2026-11-17, S-135; P10, TC-24) | Patient | SH-L | C09; P10 | L | RET-02 | DEL-RESTRICT |
| Opaque participant reference per consult (sent to the video vendor instead of the user ID), tokens issued count and time; the token itself is never stored (added 2026-11-17, S-136) | Patient, doctor | PER-K | P-CARE | C | RET-12 | DEL-TTL: the reference is replaced with a random value and the token time cleared 24 h after the consult ends (`consult.purge_participant_refs`, review F11) |
| Participant join and leave events from the video vendor: role, kind, time (added 2026-11-17, S-137) | Patient, doctor | SH-L | P-CARE; proof both sides connected (E-BK-08) | C / L | RET-02 | DEL-RESTRICT |
| Identity confirmation by the doctor: who and when (the confirmed name and age are not copied) (added 2026-11-17, S-147; P02) | Patient, doctor | SH-L | C09; P02 | L | RET-02 | DEL-RESTRICT |
| Mode changes during the call: from, to, reason, time (added 2026-11-17, S-137; E-RX-06, mode at signing) | Patient, doctor | SH-L | C09; clinical safety | L | RET-02 | DEL-RESTRICT |
| Connection quality metrics | none (pseudonymous) | PER (pseudonymous) | Service quality | C | RET-07 | DEL-ROLL |
| **Video or audio recording** | none | Prohibited | **Not recorded** (ADR-13) | none | none | none |

### 4.8 clinical (F1-c)
| Field | Subject | Class | Purpose | Basis | Retention | Deletion |
|---|---|---|---|---|---|---|
| Clinical notes, with signed versions (K-CS-05) | Patient | SH-C | P-CARE / P-RECORD | C / L | RET-02 | DEL-RESTRICT |
| Diagnosis or provisional diagnosis | Patient | SH-C | P-CARE | C / L | RET-02 | DEL-RESTRICT |
| Prescription items: drug, dose, frequency, duration, instructions, injections, tests | Patient | SH-C | P-CARE | C / L | RET-02 | DEL-RESTRICT |
| Rules-engine decision and block reason | Patient, doctor | SH-C | Safety (K-CS-02, C09, C13) | L | RET-02 | DEL-RESTRICT |
| Prescription PDF | Patient | SH-C | P-CARE / P-RECORD | C / L | RET-02 | DEL-RESTRICT |
| Doctor signing record (who, when, key ref) | Doctor | PER | Integrity (C12, proposed row N5) | L | RET-02 | DEL-RESTRICT |
| Signed document (canonical JSON, sealed with the care key), its SHA-256, the electronic signature value and chain, and the signing provider (S-150) | Patient, doctor | SH-C | P-CARE / P-RECORD; integrity (C12) | C / L | RET-02 | DEL-RESTRICT |
| Prescription verification reference: only its SHA-256 is stored (a random 256-bit token; the token itself is only inside the sealed document) (S-150, R-09) | Patient | PER (reference only, no health content) | Integrity: public verification page | L | RET-02 | DEL-RESTRICT |
| Reminder links: opaque activity and item references between a prescription version and scheduler-core, and the "offer once" outcome (S-152) | Patient | PER (references only; no drug name, K-ARC-02) | P-REMIND | C | While reminders run, then RET-02 | DEL-ERASE (reminders) / DEL-RESTRICT (version link) |
| Reminder switch per prescription item: on or off, a generation number, time (added 2026-11-20, S-155; kept across amendments) | Patient | PER (references only; no drug name) | P-REMIND | C | As the prescription (RET-02) | DEL-RESTRICT |
| Prescription templates | Doctor | INT (must hold no patient data) | D-PROFILE (R-53) | D | While listed | DEL-ERASE |

### 4.9 scheduler-core (separate service, F10-a)
| Field | Subject | Class | Purpose | Basis | Retention | Deletion |
|---|---|---|---|---|---|---|
| Opaque activity ref, recurrence rule, time zone | Patient (pseudonymous) | PER (pseudonymous) | P-REMIND | C | Until the schedule ends or the account closes | DEL-ERASE. **No health data by design** (R-75): reminder wording is built by notifications at send time. |
| Occurrences and dispatch status | Patient (pseudonymous) | PER (pseudonymous) | P-REMIND | C | Rolling window + 90 days | DEL-ROLL |
| Dose actions on an occurrence: taken or skipped, acted time, snooze time and count, and the client action IDs that make offline actions idempotent (added 2026-11-20, S-154; opaque, no health data) | Patient (pseudonymous) | PER (pseudonymous; a behavioural trace) | P-REMIND | C | As the occurrence | DEL-ROLL; erased with the subject |

### 4.10 notifications
| Field | Subject | Class | Purpose | Basis | Retention | Deletion |
|---|---|---|---|---|---|---|
| Channel choice per reminder type; medicine-names flag | Patient | PER | P-REMIND, P-MEDNAMES | C | RET-01 | DEL-ERASE |
| SMS delivery log: phone, DLT template ID, status | Patient | PER (no variables logged) | P-REMIND, P-ACCOUNT | C | RET-10 | DEL-ROLL |
| Push delivery log | Patient | PER | P-REMIND | C | RET-10 | DEL-ROLL |
| Notification preferences: discreet mode, quiet hours on/off, time zone, language | Patient | PER | P-REMIND, P-ACCOUNT | C | RET-01 | DEL-ERASE |
| Dispatch record: user ref, template key, state, attempts, opaque reminder refs (occurrence, activity). Message variables (for example doctor name, time) are held only while the dispatch is in flight and cleared at its final state | Patient | PER (pseudonymous refs); variables PER while in flight | P-CARE, P-REMIND | C | Finished dispatches RET-10 | DEL-ROLL |
| Reminder action record: client action ID, user ref, occurrence ref, action (taken, skipped, snoozed), acted time (added 2026-11-20, S-154; each action audited once and idempotent) | Patient | PER (pseudonymous ref, behavioural) | P-REMIND | C | RET-10 (90 days) | DEL-ROLL; deleted at erasure step 1 |
| Device refresh request: user ref, pending flag, requested, next-attempt and last-sent instants, attempt count (added 2026-11-21, S-165; tells the patient's devices to fetch the on-device reminder schedule again; no content) | Patient | PER (pseudonymous ref) | P-REMIND | C | Finished rows 1 day | DEL-ROLL; deleted at erasure step 1 |
| WhatsApp opt-in (time, source) and delivery log | Patient | PER | P-WHATSAPP | C | RET-05 / RET-10 | **ON HOLD: not collected until WhatsApp resumes** |

### 4.11 admin and audit
| Field | Subject | Class | Purpose | Basis | Retention | Deletion |
|---|---|---|---|---|---|---|
| Grievance: reference, contact, text, dates, outcome | Patient, doctor | SH-C | C02, C08, C19 | L | RET-09 | DEL-JOB |
| Support ticket | Any user | SH-C | Support | C | RET-09 | DEL-JOB |
| Staff just-in-time access: staff, reason, scope, start and end | Staff, patient | SH-L | K-SEC-08 | L | RET-06 | DEL-RESTRICT |
| Monthly access review record: month, reviewer, count | Staff | INT | Evidence of the monthly review (S-051) | L | RET-06 | DEL-RESTRICT |
| Grievance event trail: item, action, actor, outcome code, time (append-only) | Patient, staff | SH-L | Evidence for SLA reports (C02, C08, C19) | L | RET-09 | DEL-JOB |
| CH-12 case (kind `child_account`): patient reference and the consult session reference only, opened when a doctor flags that the patient is a minor (added 2026-11-17, TC-48, D-42) | Patient | SH-L (references only) | CH-12 (children-data-policy); K-DP-10 | L | RET-09 | DEL-JOB |
| Support account lookup record: staff ref, ticket ref, whether an account was found, time; the number searched is never stored (added 2026-11-18, S-172) | Staff; the patient indirectly | SH-L | E-DP-16 limit and monthly access review (K-SEC-08) | L | RET-06 | DEL-RESTRICT (append-only) |
| Staff access request: staff, role, patient ref, data kinds, reason code, ticket ref, approver, break-glass flag, written justification (sealed with a service key) | Staff, patient | SH-L (justification: SH-C) | K-SEC-08 request and approval; monthly review evidence | L | RET-06 | DEL-RESTRICT |
| Staff accounts (name, work email, role, MFA) | Staff | PER | Employment, security | L | Employment + 1 year [CONFIRM] | DEL-JOB |
| Audit events (actor, action, subject ref, time, hash chain) | All | SH-L | Tamper-evident audit | L | Longest of the related records | DEL-RESTRICT. The hash chain must survive deletion (store references, not content). |

### 4.12 analytics, logs, marketing, v0
| Field | Subject | Class | Purpose | Basis | Retention | Deletion |
|---|---|---|---|---|---|---|
| Analytics `user_key` (random) and events | Patient, doctor | PER (pseudonymous) | P-ANALYTICS (opt-in per S-010 draft) | C | RET-11 | DEL-ROLL; link deleted on erasure |
| IP address, user agent, request ID, scrubbed error traces | All users | PER | Security (C07) | L | RET-07 | DEL-ROLL |
| Waitlist contact (email or phone), time, text version | Prospect | PER | P-WAITLIST | C | RET-13 | DEL-JOB |
| v0: name, phone, transcribed prescription, reminder schedule, feedback notes, consent form scan | Pilot patient | SH-C | V0-PILOT, V0-FEEDBACK | C | RET-14 | DEL-JOB, with a deletion record kept |

## 5. Processors (vendor register skeleton, C06 and K-DP-08)

No vendor receives personal data until it has a processor agreement and appears here.

| Processor type | Data sent | Location | Agreement | Chosen in |
|---|---|---|---|---|
| Cloud hosting (two India regions) | All, encrypted | India | [FILL] | ADR-09, P05 |
| Managed identity (OTP, MFA) | Phone, device, session | India (ADR-11) | [FILL] | ADR-11 |
| SMS provider (DLT) | Phone, template text (discreet) | [FILL] | [FILL] | S-041 |
| Push (FCM / APNs) | Push token, discreet text | Outside India (Google, Apple) [VALIDATE against decision 4: no health data in pushes] | Platform terms | ADR-15 |
| Video provider | Room tokens, session metadata | India region (ADR-13) | [FILL] | spike a08 |
| **Crash reporting / app performance monitoring** (added 2026-11-16, review R-05) | Device ID, IP address, app version, scrubbed stack traces (no request data) | **Self-hosted or India-processing only** | [FILL] | S-095 / mobile rule M5 |
| Electronic signature provider (P09) | Document hash, signer name and registration number | India (licensed eSign service provider, if that method is chosen under N5) | [FILL] | N5 decision |
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
| Every field classified (S-044) | **Yes** | Section 4.0 scheme and handling rules; Class column on all 71 rows |

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

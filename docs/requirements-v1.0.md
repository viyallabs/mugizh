# v1.0 Requirements Spec (S-004) — DRAFT v0.1 for founder approval

**Phase P00 · Epic EP01 · Depends on:** S-002 (vision), S-003 (personas)
**Sources:** feature-roadmap.md v0.3 (section 4, v1.0), healthcare-app-agile-roadmap.xlsx
(Performance, Contracts, Roles sheets).
**Scope:** v1.0 MVP "Consult → Prescription → Reminders". Hypothesis: doctors will write
structured prescriptions in our product if it is faster than paper; patients with automatic
reminders come back for refills, tests and follow-ups.
**Not in v1.0:** labs booking, pharmacy, nurses, ABDM, caregiver mode, SOS, symptom search,
paper-prescription scan, reviews, automated hold/release (see section 6).

Priority: **M** = Must for v1.0, **S** = Should, **C** = Could. Story IDs `R-xx` are
requirement stories; they will be broken into build stories in the Backlog (P11).
Contract IDs (K-…) come from the Contracts sheet.

---

## 1. v1.0 features in scope
| Feature | Summary |
|---|---|
| F17-a | Verification of doctors and clinics |
| F1-a | Appointment booking |
| F1-b | Online consultation |
| F1-c | Doctor notes + structured prescription |
| F1-d | Reminders on by default (includes F1.2/F1.3 reminders and F7-a) |
| F2-a | Basic medical history with consent |
| F5-a | Range-based clinic and doctor finder |
| F6-a | Payment before consultation |
| F11-a | WhatsApp channel (booking and reminders) |
| F10-a | Calendar core engine (separate service) |

---

## 2. Functional requirements: user stories by role

### 2.1 Accounts and onboarding (all roles)
| ID | Role | Story | Acceptance criteria | Pri |
|---|---|---|---|---|
| R-01 | Patient | Sign up and log in with my mobile number and OTP. | OTP login works; no password required; rate-limited; session can be ended from all devices. | M |
| R-02 | Patient | See and accept a clear privacy notice and give specific consent before any processing. | Notice in plain language; separate consent for each purpose; consent stored with version and time; can be withdrawn (K-DP-02). | M |
| R-03 | Patient | Manage my profile and communication preferences. | Can edit name, language, channel per reminder type; WhatsApp opt-in/out takes effect immediately (F11-a). | M |
| R-04 | Patient | Request deletion of my data and raise a grievance. | Grievance contact visible; erasure request reaches all stores (K-DP-07). | M |
| R-05 | Clinic admin | Create and manage a clinic profile and its doctors. | Clinic address, map location, registration documents captured; owning doctor linked. | M |

### 2.2 Verification (F17-a)
| ID | Role | Story | Acceptance criteria | Pri |
|---|---|---|---|---|
| R-10 | Doctor | Submit my registration number, state council, year, qualification, photo and ID for verification. | Application moves Submitted → Automatic checks → Manual review → Verified / Rejected / Needs more info. | M |
| R-11 | Internal verifier | Review an application with evidence and decide. | Evidence stored; decision audit-logged; four-eyes for overrides; unavailable registry/vendor never auto-approves (K-CS-01). | M |
| R-12 | Doctor | See my verification status and be told what is missing. | Clear status and reasons; can resubmit. | M |
| R-13 | System | Re-verify on licence expiry, periodically (e.g. 12 months) and after complaints. | Scheduler triggers re-verification; lapsed doctors are suspended from search, booking and prescribing. | M |
| R-14 | Clinic admin | Get my clinic verified (address, registration docs where the state requires, lead doctor verified, HFR ID if any). | Clinic cannot take bookings until verified. | M |
| R-15 | Patient | See a "verified" mark with the doctor's registration number. | Shown on profile and on the prescription PDF. | M |

### 2.3 Finding care (F5-a)
| ID | Role | Story | Acceptance criteria | Pri |
|---|---|---|---|---|
| R-20 | Patient | Find clinics and doctors within a radius I set in km. | Filters: specialty, in-person/online, fee, next available slot; only verified results; p95 < 300 ms. | M |
| R-21 | Patient | See a doctor's profile, fee and available slots. | Shows verified mark, clinic, languages, fee, slots. | M |

### 2.4 Booking (F1-a)
| ID | Role | Story | Acceptance criteria | Pri |
|---|---|---|---|---|
| R-30 | Doctor/Clinic admin | Set availability for in-person and online slots. | Recurring availability; slots regenerate on change; works for a clinic with several doctors. | M |
| R-31 | Patient | Book an in-person or online slot. | No double booking (unique doctor+slot with a short hold); confirmation by app and WhatsApp. | M |
| R-32 | Patient | Reschedule or cancel. | Rules by time to appointment; refund per policy (see R-60). | M |
| R-33 | Clinic admin | See the day's appointments. | Day view with status; updates live. | M |
| R-34 | Doctor | Be notified of new, changed and cancelled bookings. | Notification within 60 s. | M |

### 2.5 Online consultation (F1-b)
| ID | Role | Story | Acceptance criteria | Pri |
|---|---|---|---|---|
| R-40 | Patient/Doctor | Join a video consultation. | Join < 5 s; audio fallback on poor bandwidth; consent captured before start. | M |
| R-41 | Doctor | Mark a consultation complete. | Completion triggers prescription and payment-release flow (v1.2 automates release). | M |

### 2.6 Notes and prescriptions (F1-c)
| ID | Role | Story | Acceptance criteria | Pri |
|---|---|---|---|---|
| R-50 | Doctor | Write clinical notes per visit. | Notes encrypted at field level (K-DP-03); signed notes immutable, edits are new versions (K-CS-05). | M |
| R-51 | Doctor | Write a structured prescription: drugs (name, dose, frequency, duration, instructions), injections (drug, dose, schedule), scans/tests (name, due date or interval). | Drug search p95 < 150 ms; rules engine < 50 ms. | M |
| R-52 | System | Enforce telemedicine rules on every prescription. | Block Schedule X, narcotic and psychotropic drugs by consult mode; per Telemedicine Practice Guidelines 2020 (K-CS-02). | M |
| R-53 | Doctor | Use templates for common prescriptions. | Create, save and reuse templates. | S |
| R-54 | Patient/Doctor | Get a prescription PDF with the doctor's registration number. | PDF generated on signing; shared securely, not as an open link. | M |

### 2.7 Reminders and calendar (F1-d, F10-a, F11-a)
| ID | Role | Story | Acceptance criteria | Pri |
|---|---|---|---|---|
| R-70 | Patient | Get reminders created automatically from every prescription item (doses, injection dates, tests due). | On by default; can switch off item by item; 99.9% sent within 60 s of due time. | M |
| R-71 | Patient | See my medicines, appointments, injections and tests in a calendar (today, week, month). | Event types: medicine dose, injection, appointment, test due. | M |
| R-72 | Patient | Receive reminders on WhatsApp as well as the app. | Official WhatsApp Business Platform, approved templates, recorded opt-in, immediate opt-out. | M |
| R-73 | Patient | Choose my channel per reminder type. | Discreet by default: no diagnosis; medicine names only if I opt in (K-DP-09). | M |
| R-74 | System | Keep reminders working offline and during provider outages. | On-device backup; retry with backoff; channel fallback; no silent loss. | M |
| R-75 | Developer | Run the calendar as a separate service with its own API and data. | `scheduler-core` holds no health logic and never reads the health DB; stores only external references. | M |

### 2.8 Medical history and consent (F2-a)
| ID | Role | Story | Acceptance criteria | Pri |
|---|---|---|---|---|
| R-80 | Patient | Have notes, prescriptions and reports created on the platform stored in my record. | Automatic; encrypted. | M |
| R-81 | Patient | Upload older documents. | Image/PDF upload; virus scan; stored encrypted. | M |
| R-82 | Patient | Grant a doctor time-limited consent to my history for a consultation, and revoke it. | Consent check < 20 ms; revocation effective immediately (K-DP-02). | M |
| R-83 | Patient | See who accessed my record. | Access log visible; every read is audited (async, tamper-evident). | M |
| R-84 | Doctor | View a patient's history only with active consent. | Without consent: no access; each view is audit-logged. | M |

### 2.9 Payments (F6-a)
| ID | Role | Story | Acceptance criteria | Pri |
|---|---|---|---|---|
| R-60 | Patient | Pay when I book. | Hosted checkout only; no card data stored (K-PAY-01); idempotent payments (K-PAY-02). | M |
| R-61 | Patient | Get refunds under clear rules for patient no-show, doctor no-show and doctor cancellation. | Policy shown before payment; refund status visible. | M |
| R-62 | Clinic admin | See what I am owed and receive payouts. | Fallback if the platform does not yet qualify for split settlement: clinic collects through its own account and the platform invoices its fee separately. | M |
| R-63 | System | Reconcile payments. | Signed webhooks + polling fallback + daily reconciliation; no lost or duplicated payments. | M |

---

## 3. Non-functional requirements

### 3.1 Performance (targets from the Performance sheet)
| Area | Target |
|---|---|
| Radius search | p95 < 300 ms at 50,000 providers |
| Slot availability | p95 < 300 ms |
| Booking | 0 double bookings; p95 < 500 ms |
| Reminder dispatch | 99.9% within 60 s of due time; capacity 1M/day |
| Notification delivery | No silent loss; retries within 5 min |
| Video join | < 5 s; audio fallback under bandwidth threshold |
| Prescription rules engine | < 50 ms per prescription |
| Drug search | p95 < 150 ms |
| Consent check | < 20 ms per request |
| Verification | Auto checks < 1 min; manual review < 48 h |
| General APIs | Read p95 < 300 ms; write p95 < 800 ms |
| Mobile app | Cold start < 3 s on low-end Android; size budget set in CI |

### 3.2 Availability and reliability
- 99.9% monthly API availability.
- Payments: zero lost or duplicated payments; reconciliation daily.
- Reminders keep working offline (on-device backup) and survive a notification provider outage.
- Backups and disaster recovery in India (primary + DR region); recovery objectives to be set in P05. **[CONFIRM]**

### 3.3 Security
- Encryption in transit and at rest; field-level encryption for sensitive data (K-DP-03).
- Role-based access per the Roles sheet; internal staff access is time-limited, reason-coded and audit-logged (JIT, K-SEC-08).
- No Aadhaar storage or logging (K-DP-05); no card data (K-PAY-01).
- SAST, secret and dependency scans pass in CI; independent VAPT before real patient data (P12).
- Async append-only audit log with hash chaining (tamper evidence).

### 3.4 Privacy and compliance (DPDP Act 2023 + DPDP Rules 2025)
- Health data stored and processed only in India-region infrastructure and India-resident vendors (K-DP-01).
- Specific, revocable consent; purpose limitation; retention policy; breach-response plan; grievance contact.
- Logs, analytics and crash reports contain no health data or full identifiers (K-DP-04).
- Every new personal-data field has a data-inventory entry (K-DP-06); erasure reaches every store (K-DP-07).
- No vendor receives personal data without a processor agreement (K-DP-08).
- Notifications never show a diagnosis (K-DP-09).
- Prescriptions follow Telemedicine Practice Guidelines 2020; lawyer-check items confirmed by a qualified lawyer.

### 3.5 Accessibility and usability
- Usable on low-end Android phones and weak connections.
- Large, readable text and touch targets; support OS font scaling; sufficient colour contrast (target WCAG 2.1 AA). **[CONFIRM]**
- Screen-reader support for core patient flows. **[CONFIRM]**
- Plain-language copy for consent and safety messages; language options per the launch-language decision. **[DECISION]**
- Doctor prescription flow measured against a "faster than paper" time target. **[CONFIRM target]**

### 3.6 Maintainability and process
- Every story passes the Definition of Done (K-DOD-01); contract files change only via an ADR (K-DOD-03).
- Country-specific items live in config or adapters (K-GLB-01).

---

## 4. v1.0 exit criteria (from the roadmap, suggested)
- At least 70% of onboarded doctors' prescriptions are written in the product.
- At least 35% of patients active at 60 days.
- At least 95% payment success.
- No compliance incidents.

## 5. Dependencies and risks
- **Payments:** depends on eligibility for an RBI-authorised aggregator's marketplace/split product; fallback in R-62.
- **WhatsApp:** depends on an approved Business Platform provider and approved templates.
- **Verification:** vendor spike (S-042) decides third-party API vs manual checks; fallback is manual review.
- **Doctor supply:** early shortage of doctors is a risk; mitigated by v0 validation and, later, paper scan (v1.1).
- **Nurse timing:** open decision, see section 6.

## 6. Out of scope for v1.0 (and where it goes)
| Item | Version |
|---|---|
| Symptom search, SOS, paper scan, family/caregiver, emergency ID, reviews | v1.1 |
| Automated hold-and-release, payout dashboard | v1.2 |
| Lab booking, reports, ABDM, adherence tracking, follow-up autopilot | v2.0 |
| Medicines marketplace | v3.0 |
| Nurse verification and visits | v4.0 |
| Calendar as standalone product, second country | v5.0 |

## 7. Open decisions for the founder
1. **Nurse timing:** keep nurses at v4.0, or bring a smaller nurse feature forward before v1.0 scope freeze?
2. **Launch languages** for v1.0.
3. **Accessibility target:** WCAG 2.1 AA?
4. **"Faster than paper" target:** the maximum time for a doctor to write a standard prescription.
5. **Recovery objectives** (RPO/RTO) for backups and disaster recovery.

# [Product Name] — Feature Roadmap (India-first)

**Status:** Draft v0.3
**Scope:** The founder's original 10 features (F1–F10), 6 additions approved after review (F11–F16), and the verification pipeline requested by the founder (F17), sequenced into build versions. Primary roles: Patient, Doctor, Nurse.

---

## 1. How to read this document

- **F1 to F10** are the original features, numbered as the founder listed them. Sub-features keep their numbers (F1.1, F1.2, F1.3).
- **F11 to F16** are additions the founder approved after review. Each one notes the suggestion it came from (S2, S3, and so on).
- **F17** is a feature the founder requested directly: verification of doctors, nurses, clinics and laboratories.
- When a feature is split across versions, each part gets a letter suffix (F1-a, F2-b, and so on).
- **Each version tests one hypothesis.** Do not start the next version until the current version meets its exit criteria.
- A **compliance gate** is a legal or safety requirement that must be cleared before that feature ships.
- **Assumption:** F7 ("tracking of drug") is read as tracking medicine intake (adherence). Delivery tracking for medicine orders is covered under F1.1.

## 2. Sequencing principles

1. **Build the loop that creates data and retention first:** consultation → structured prescription → reminders. Labs, pharmacy and nurses all consume that prescription, so they come after it.
2. **Build regulated, operations-heavy or liability-heavy features later:** medicine delivery and home injections. Run them through licensed partners instead of owning inventory or staff.
3. **Build F10 (calendar) as an independent module from day one,** so it can become a standalone app later without a rewrite.
4. **Make compliance part of the design from the start** (consent, audit logs, prescription rules), because health data and prescriptions are regulated in India.

## 2a. Roles

**Primary roles.** These are the core users the product is built around.
- **Patient:** books care, receives prescriptions and reminders, owns their health record and gives or withdraws consent. Caregiver access (F13) is an extension of the patient role: a caregiver can act only with the patient's permission.
- **Doctor:** must be verified (F17) before appearing in search or prescribing. Manages availability, consults, writes notes and prescriptions, and sees a patient's history only with consent.
- **Nurse:** must be verified (F17-c). Delivers prescribed injections and home care (F1.3, F4).

**Supporting roles.** Features need these to work, but they get smaller portals rather than full apps.
- **Clinic admin (v1.0):** manages the clinic profile, schedules and payouts.
- **Lab partner (v2.0):** receives test bookings and uploads reports.
- **Pharmacy partner (v3.0):** verifies prescriptions and dispenses medicines.
- **Internal team:** verification reviewer, support agent, administrator. Their access to personal data is time-limited, needs a stated reason, and is recorded in the audit log.

**Open decision:** nurses are a primary role, but the nurse features are scheduled for v4.0. Decide before v1.0 scope is frozen whether to keep that order (nurse accounts and verification arrive with v4.0) or bring a smaller nurse feature forward.

## 3. Feature index: original list mapped to versions

| ID | Original feature | First ships | Complete in |
|---|---|---|---|
| F1 | Appointment booking → consultation → notes + prescriptions (drugs, injections, scans with intervals), reminders on by default | v1.0 | v1.0 |
| F1.1 | Drug marketplace (prescription-based; OTC where law allows) | v3.0 | v3.0 |
| F1.2 | Lab/scan pending list + scheduling | v1.0 (as reminders) | v2.0 (booking) |
| F1.3 | Injection reminders + nurse scheduling with safety precautions | v1.0 (reminders) | v4.0 (nurse visits) |
| F2 | Doctor access to patient's medical history (as allowed by law) | v1.0 (platform records + uploads) | v2.0 (ABDM) |
| F3 | Lab uploads digital report, routed to doctor | v2.0 | v2.0 |
| F4 | Nurse and home services and tests | v2.0 (home sample collection) | v4.0 (nursing and home care) |
| F5 | Range (km) based clinic/lab finder | v1.0 (clinics) | v2.0 (labs) |
| F6 | Pay before consult → hold → release to clinic after completion | v1.0 (prepay) | v1.2 (automated hold and release) |
| F7 | Drug intake tracking + regular notifications | v1.0 (reminders) | v2.0 (adherence + refill alerts) |
| F8 | Symptom-based specialist search | v1.1 | v1.1 |
| F9 | SOS / emergency contact | v1.1 | v1.1 |
| F10 | Modular calendar: medicines, appointments, diet plans and more | v1.0 (core engine) | v5.0 (standalone app) |
| F11 | WhatsApp channel for booking, reminders and reports (from S2) | v1.0 (booking + reminders) | v2.0 (report alerts) |
| F12 | Paper prescription scan → reminders, human-verified (from S3) | v1.1 | v1.1 |
| F13 | Family and caregiver mode (from S4) | v1.1 | v1.1 |
| F14 | Emergency medical ID (from S6) | v1.1 | v1.1 |
| F15 | Reviews only from verified visits (from S7) | v1.1 | v1.1 |
| F16 | Follow-up autopilot (from S8) | v2.0 | v2.0 |
| F17 | Verification pipeline for licensed doctors, nurses, clinics and certified labs (founder request) | v1.0 (doctors, clinics) | v4.0 (nurses; labs in v2.0) |

---

## 4. Version details

### v0 — Validation (before production code)

This is not a product release. It is a 4–6 week test of the F1 loop with a small number of doctors, run manually ("concierge"): booking through a simple form, prescriptions entered by the team, reminders sent by WhatsApp.

**Exit criteria (suggested targets; adjust after the first conversations):**
- At least 10 doctors agree to put their prescriptions into a digital format for every patient for 4 weeks.
- At least 40% of chronic patients are still engaging with reminders after 60 days.
- At least 3 doctors say they would pay for the tool.

---

### v1.0 — MVP: "Consult → Prescription → Reminders"

**Hypothesis:** Doctors will write structured prescriptions in our product if it is faster than paper. Patients who get automatic reminders will come back for refills, tests and follow-ups.

**F17-a — Verification pipeline: doctors and clinics** (founder request)
- No doctor or clinic can appear in search, take bookings or prescribe until verified. This also delivers the F1-c compliance gate ("verify each doctor's medical registration").
- **Doctor checks:**
  - Registration number, state medical council and year matched against an official register: the state medical council, the NMC registers, or the ABDM Healthcare Professionals Registry (HPR) when the doctor has an HPR ID.
  - Name, qualification and photo match an identity check.
  - Registration is active, not suspended or lapsed.
- **Clinic checks:**
  - Address and map location confirmed.
  - Registration documents where the state requires them (Clinical Establishments registration).
  - The owning or lead doctor is verified.
  - ABDM Health Facility Registry (HFR) ID recorded where the clinic has one.
- **How the pipeline works:**
  - An orchestrator moves each application through states: Submitted → Automatic checks → Manual review → Verified, Rejected or Needs more info. Later states are Suspended and Re-verification due.
  - Every decision stores its evidence and is written to the audit log.
  - Re-verification runs when a licence expires, periodically (for example every 12 months), and after any complaint.
  - If a registry or vendor is unavailable, the application waits for manual review. It is never approved automatically.
- **To explore in v1.0:** a third-party verifier API compared with manual checks against official registers. Section 7 compares the options.

**F1-a — Appointment booking**
- In-person and online slots, doctor and clinic availability, reschedule and cancel, confirmations.
- Clinic-side view of the day's appointments.

**F1-b — Online consultation**
- Video consultation, with audio as a fallback on poor connections.
- Patient consent captured before the consultation starts.

**F1-c — Doctor notes + structured prescription**
- Clinical notes per visit.
- Structured prescription items:
  - **Drugs:** name, dose, frequency, duration, instructions.
  - **Injections:** drug, dose, schedule.
  - **Scans/lab tests:** test name, due date or interval.
- Templates for common prescriptions; prescription PDF showing the doctor's registration number.
- **Compliance gate:**
  - Verify each doctor's medical registration before they can prescribe.
  - Enforce the Telemedicine Practice Guidelines 2020 drug lists by consultation type and mode.
  - Block drugs that may not be prescribed through telemedicine (Schedule X, narcotic and psychotropic drugs).

**F1-d — Reminders on by default** (also F1.2 and F1.3 in reminder form, and F7-a)
- Every prescription item automatically creates reminders: doses, injection dates, and scans/tests due.
- Reminders are on by default; the patient can turn them off item by item.

**F2-a — Medical history (basic)**
- Every note, prescription and report created on the platform goes into the patient's record.
- Patients can upload older documents.
- A doctor sees a patient's history only after the patient grants consent for that consultation; the consent is time-limited and can be revoked.
- **Compliance gate (DPDP Act 2023 + DPDP Rules 2025):**
  - Clear privacy notice and specific consent.
  - Purpose limitation and retention policy.
  - Breach-response plan.
  - Grievance contact.
  - Audit log of every access to a record.

**F5-a — Range-based clinic and doctor finder**
- Search within a radius set in km, filtered by specialty, in-person or online, fee and next available slot.

**F6-a — Payment before consultation**
- Patient pays when booking; cancellation and refund rules for patient no-show, doctor no-show and doctor cancellation.
- **Compliance gate:** collect and split money only through an RBI-authorised payment aggregator's marketplace or split-settlement product.
  - Confirm eligibility first. Some products need a minimum business turnover.
  - Until the platform qualifies, the fallback is: the clinic collects through its own payment account, and the platform invoices its fee separately.

**F11-a — WhatsApp channel** (approved addition, from S2)
- Booking confirmations, appointment reminders, and medicine, injection and test reminders on WhatsApp, in addition to app notifications.
- The patient opts in to WhatsApp and can choose the channel for each reminder type.
- Reminders are discreet by default: no diagnosis, and medicine names only if the patient turns that on. Details open in the app.
- **Compliance gate:**
  - Use the official WhatsApp Business Platform through an approved provider, with pre-approved message templates.
  - Record each opt-in and honour opt-outs immediately.

**F10-a — Calendar core engine**
- Built as a separate service with its own API and data model. Its entities are Activity, Schedule (recurrence rules), Occurrence, Reminder and Channel.
- The health app is only a client of this engine. The engine contains no health-specific logic, which is what makes the later extraction possible.
- Event types in v1.0: medicine dose, injection, appointment, test due.
- Patient calendar view: today, week and month.

**v1.0 exit criteria (suggested):**
- Doctors on the platform write at least 70% of their prescriptions in the product.
- At least 35% of patients are active at 60 days.
- At least 95% of payments succeed.
- No compliance incidents.

---

### v1.1 — Discovery, safety and family

**F8 — Symptom-based specialist search**
- A symptom-to-specialty mapping, written and reviewed by doctors, that recommends which kind of doctor to see. It never offers a diagnosis.
- Red-flag symptoms (for example, chest pain or stroke signs) send the user to F9 emergency options instead of a booking screen.

**F9 — SOS / emergency**
- One tap dials 112 (India's emergency number).
- Alerts the user's emergency contacts by SMS or WhatsApp, with live location.
- Shows the nearest hospitals with emergency departments, using the F5 geo service.
- Clear wording that the app does not dispatch an ambulance itself, unless a licensed ambulance partner is added later.

**F12 — Paper prescription scan → reminders** (approved addition, from S3)
- The patient photographs a paper prescription from any doctor, including doctors who are not on the platform.
- Text recognition extracts medicines, doses, frequency and duration, plus tests and follow-up dates.
- Before any reminder is created:
  - A trained reviewer (for example, a pharmacist) checks the extracted items against the image.
  - The patient then confirms the items.
- **Safety rule:** if any item is unclear (handwriting, dose, frequency), it is not scheduled until it is clarified. The system never guesses a dose.
- Items are labelled "from uploaded prescription" so the patient and doctors can tell them apart from prescriptions written on the platform.
- The scan is stored in the patient's record (F2). It can be used for medicine orders later (F1.1), subject to the pharmacy partner's own verification.
- **Why in v1.1:** patients get value even when their doctor is not on the platform. This helps with the early shortage of doctors on the platform.

**F13 — Family and caregiver mode** (approved addition, from S4)
- One account manages several profiles: children, elderly parents and other family members.
- An adult can invite a caregiver, such as a son or daughter living elsewhere or abroad, to view and help manage their care.
  - The adult chooses what the caregiver can see and do.
  - The adult can revoke access at any time.
- **Caregiver alerts:**
  - Missed doses, after a grace period the family sets.
  - Missed appointments.
  - Upcoming tests.
  - SOS events (F9).
- Family view in the calendar (F10): everyone's medicines, appointments and tests in one place.
- A caregiver can book and pay for appointments on behalf of a family member.
- **Compliance gate:**
  - Each adult gives their own consent for every caregiver link.
  - Children's profiles are managed by a parent or guardian, with verifiable parental consent under the DPDP Rules.
  - All caregiver access is recorded in the audit log.

**F14 — Emergency medical ID** (approved addition, from S6)
- The patient records:
  - Blood group.
  - Allergies.
  - Current medicines (filled in automatically from prescriptions).
  - Major conditions.
  - Implants or devices.
  - Emergency contacts.
- Available from the phone's lock screen (widget) and as a QR code a first responder can scan.
- The patient chooses exactly which fields can be seen without logging in.
- Works offline (stored on the device).
- Shared automatically with emergency contacts when SOS (F9) is triggered.

**F15 — Reviews only from verified visits** (approved addition, from S7)
- Only patients with a completed consultation, in-person or online, can rate and review. They must do so within a set window after the visit (for example, 14 days).
- Ratings cover things patients can judge directly, such as waiting time, communication and the clinic, rather than clinical decisions.
- Moderation removes:
  - Abusive content.
  - Personal health details.
  - Information that identifies the patient.
- Doctors can post one public reply.
- Search ranking is never for sale. Any paid placement is clearly labelled.

---

### v1.2 — Payments complete (F6-b)

- Automated hold: the payment is held until the consultation is marked complete, then released after a short dispute window.
- Automated refunds for doctor no-shows; dispute handling.
- Clinic payout dashboard, settlement reports and tax invoices.
- **Dependency:** eligibility for the payment aggregator's marketplace or split product.

---

### v2.0 — Diagnostics loop and connected history

**Hypothesis:** Patients will book the tests their doctor prescribed through us, and doctors value getting the results automatically.

**F1.2 — Lab and scan scheduling**
- The pending list is created automatically from the prescription.
- Patients book at partner labs, with slots and prices shown.

**F4-a — Home sample collection**
- Booked through partner labs that offer collection at home.

**F5-b — Range-based lab finder**
- Same radius search as F5-a, applied to labs and scan centres.

**F17-b — Verification of certified laboratories**
- The lab's NABL accreditation certificate number, validity dates and scope (which tests are accredited) are checked against NABL's public directory.
- Patients can book only tests that fall inside the lab's accredited scope.
- HFR ID and Clinical Establishments registration recorded where applicable. The pathologist who signs reports is verified as a doctor through F17-a.
- With ABDM integration (F2-b), HPR and HFR checks are added as automatic sources for doctors, clinics and labs.

**F3 — Digital report upload and routing**
- The partner lab uploads the report to the patient's record.
- The report is shared automatically with the doctor who ordered it; the patient's consent is captured when the test is booked.
- The doctor is notified and can add a follow-up note.

**F2-b — Full medical history through ABDM**
- ABHA creation and linking.
- Integrate as a health information provider and user, with ABDM consent flows.
- Pull the patient's records from other ABDM-connected hospitals and labs, with consent.
- **Compliance gate:** ABDM sandbox testing and certification.

**F7-b — Adherence tracking**
- Patient marks each dose as taken or missed.
- Adherence history is visible to the patient, and to the doctor with the patient's consent.
- "Refill due" alert based on how many doses remain.

**F10-b — Diet plan notifications**
- A doctor or dietitian adds diet instructions, which are scheduled as calendar reminders.

**F11-b — Report alerts on WhatsApp**
- The patient gets a WhatsApp message when a lab report is ready, with a secure link to view it in the app.
- The report itself is not sent as a WhatsApp attachment by default.

**F16 — Follow-up autopilot** (approved addition, from S8)
- The doctor adds a follow-up plan to the prescription: when to come back and which tests to do first. Example: "review in 30 days with HbA1c and lipid profile".
- The app works backwards from the follow-up date:
  1. A few days before, it prompts the patient to book the tests (F1.2) and offers a home sample collection slot (F4-a).
  2. It then prompts the patient to book the follow-up appointment for a date after the results are expected.
- When results arrive (F3), they are attached to the follow-up appointment, so the doctor sees them before the visit.
- If the patient doesn't act, reminders escalate: first in the app, then on WhatsApp (F11), then to the caregiver if one is set up (F13).
- The doctor can see which of their patients are overdue for follow-up.
- **Depends on:** F1-a, F1-c, F1.2, F3, F11, F13.

---

### v3.0 — Medicines (F1.1)

**Hypothesis:** Patients with reminders and refill alerts will order their prescribed medicines in-app.

- Order prescribed medicines from licensed partner pharmacies. The partner's registered pharmacist verifies the prescription and dispenses.
- Medicines that do not need a prescription can be ordered without one, where the law allows.
- One-tap refill from F7-b refill alerts.
- Order and delivery tracking.
- **Compliance gate:**
  - Partners must hold a valid retail drug licence and have a registered pharmacist.
  - Validate prescriptions and keep sale records.
  - No Schedule X, narcotic or psychotropic drugs.
  - The platform holds no inventory at first.
  - Track the e-pharmacy rules, which are still an unnotified draft, and be ready to change the model.

---

### v4.0 — Home care (F1.3 complete + F4 complete)

**Hypothesis:** Patients on injection schedules will book a nurse through us, and we can deliver this safely and profitably through partners.

**F17-c — Verification of nurses**
- Registration with the state nursing council, the Nurse Unique ID (NUID) from the Indian Nursing Council's Nurses Registration and Tracking System (NRTS), or an HPR ID.
- Identity check, confirmation from the partner agency, and liability insurance.
- Uses the same orchestrator as F17-a; the nurse checks are switched on when nurse features launch.

**F1.3-b — Nurse scheduling for prescribed injections**
- Visits are booked against the injection schedule in the prescription.
- Pre-visit safety checks:
  - Valid prescription.
  - Allergy and reaction history.
  - First-dose policy: certain first doses must be given in a clinic.
  - Cold-chain check for medicines that need refrigeration.
  - Emergency kit carried by the nurse.
- Post-injection observation period.
- Sharps and biomedical waste disposal.
- Visit record written to the patient's history.

**F4-b — Nursing and other home services**
- Verified nurses, through partner agencies, for other home-care needs.
- **Compliance gate:**
  - Verify nurse registration.
  - Escalation path for adverse reactions.
  - Liability insurance.
  - Partner service-level agreements.

---

### v5.0 — Calendar as a standalone product (F10-c)

- Extract the F10 engine into its own app, with general activity types (fitness, habits, diet, family events, and so on).
- The health app keeps using the same engine through its API.
- Public API so other apps can schedule activities in it.

---

## 5. Shared building blocks

These are not new features. The original features above depend on them.

| Building block | Required by |
|---|---|
| Accounts and roles. Primary: patient, doctor, nurse. Supporting: caregiver link, clinic admin, lab partner, pharmacy partner, internal team. | All |
| Verification orchestrator (state machine, evidence store, adapters per source, re-verification scheduler) | F17, F1-c, F3, F1.1, F4 |
| Consent management + access audit log | F2, F3, F1.2, F1.1, F13 |
| Notification service (push, SMS, WhatsApp, email) | F1, F7, F9, F10, F11, F13, F16 |
| Profile and relationship model (one account, many profiles, caregiver links) | F13, F10, F6, F14 |
| Document processing + human review queue | F12, F3 |
| Geo/location service (radius search, nearest facility) | F5, F9, F4 |
| Payments ledger (holds, splits, refunds, payouts) | F6, F1.1, F4 |
| Prescription rules engine (drug lists per consultation type, blocked drugs). Configurable per country for later global launch. | F1, F1.1 |
| Security baseline: encryption in transit and at rest, role-based access, health data hosted in India | F2, F3 |

## 6. Architecture note: keeping F10 modular

- `scheduler-core` is a separate service with its own database. It never reads the health database directly; it stores only external references, such as a prescription item ID.
- The health app creates Activities with recurrence rules. The engine produces Occurrences and sends Reminders through the notification service.
- Health-specific behaviour, such as "refill due" logic, lives in the health app. The engine itself stays general-purpose.

## 7. Verification pipeline: build or use a third-party verifier?

### Where each check can come from

| What to verify | Official source | Third-party options | Notes |
|---|---|---|---|
| Doctors | State medical council registers (the licence to practise); NMC registers (Indian Medical Register; the new National Medical Register); ABDM HPR | Doctor or NMC verification APIs from vendors such as IDfy, Surepass, Decentro and AuthBridge | The National Medical Register is still filling up: in June 2026 about 1,800 certificates had been issued and more than 30,000 applications were pending. It cannot be the only source yet. Registration numbers can repeat across councils, so always capture the council. |
| Nurses | State nursing registration council; INC NRTS (NUID); ABDM HPR | Nurse verification API that checks the NUID against NRTS (for example AuthBridge) | Enrolment in NRTS is not itself the licence; the state council registration is. |
| Clinics | ABDM HFR; state Clinical Establishments registration where the state requires it | Facility and document verification services; address checks | Requirements differ by state. |
| Labs | NABL directory (search by lab name or certificate number; ISO 15189 accreditation); ABDM HFR | Mostly manual checks, plus facility verification vendors | NABL accreditation is voluntary. Decide whether to partner only with NABL-accredited labs. |

### Options

| Option | Advantages | Disadvantages |
|---|---|---|
| A. Build our own pipeline over official registries | Low cost per check; full control; no extra data processor | Few official APIs, so checks are manual or rely on scraping public websites, which breaks easily and may breach site terms; slower; needs operations staff |
| B. Use a third-party verifier only | Fast API results; audit trail; scales easily | Fee per check; the vendor becomes a data processor under the DPDP Act and needs a contract; coverage gaps across councils; still needs a fallback when the vendor fails |
| **C. Hybrid (recommended)** | Our own orchestrator (states, evidence, audit, re-verification) with pluggable adapters: ABDM HPR/HFR, one third-party API, and manual review | Slightly more to build up front, but each adapter can be swapped, and other countries can plug in their own registries through the country pack |

**Recommended sequence:** v1.0 uses manual review plus one third-party API, which suits the low early volume. v2.0 adds HPR and HFR through the ABDM integration and NABL checks for labs. v4.0 adds the nurse adapter.

**Exploration task:** pilot three vendors on about 20 doctors whose registration is already known. Compare coverage across state councils, accuracy, cost per check, contract terms and where the vendor stores data.

### Sources
- ABDM HPR covers doctors and nurses: https://kiranma72.github.io/abdm-docs/7-hpr/index.html
- National Medical Register status, June 2026: https://medicaldialogues.in/health-news/nmc/only-1800-certificates-issued-over-30k-applications-pending-nmc-mulls-committee-to-fast-track-national-medical-register-172862
- NRTS and the state council licence: https://anantamias.com/?p=40086
- Nurse verification through the NUID: https://authbridge.com/checks/nurse-verification/
- Doctor verification API inputs: https://www.idfy.com/doctor-verification/
- Searching NABL-accredited labs: https://padd.qci.org.in/ck-docs/1643702010.How%20to%20search%20NABL%20Accredited%20Laboratories.pdf

## 8. Change log

- **v0.3:** Added F17 (verification pipeline for doctors, nurses, clinics and labs) at the founder's request, with an options analysis (section 7). Added section 2a defining the primary roles (Patient, Doctor, Nurse) and the supporting roles.
- **v0.2:** Added F11–F16 after founder review (from suggestions S2, S3, S4, S6, S7 and S8).
- **Not selected, can be revisited later:**
  - S1: doctor practice app.
  - S5: live clinic queue.
  - S9: vitals logging.
  - S10: drug-interaction alerts.
  - S11: regional languages and voice reminders.
  - S12: pre-consultation intake.
  - S13: generic price information.
  - S14: subscriptions and employer plans.

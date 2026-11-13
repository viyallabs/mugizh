# Product Vision (S-002) — DRAFT v0.2 for founder approval

**Product:** [Product Name — TBD, pending trademark search in S-007]
**Status:** Draft v0.2 · Phase P00 · Epic EP01
**Sources:** Resources/feature-roadmap.md (v0.3) and Resources/healthcare-app-agile-roadmap.xlsx.
Statements marked **[CONFIRM]** are my reading of the roadmap, not words the roadmap uses.

## 1. The problem we solve
In India, a doctor consultation ends, and the care stops being connected. What the patient
must do next is spread across paper, memory and several separate places:

- **The prescription is unstructured.** It is on paper or in a photo, handwritten, easy to
  lose or misread. Nothing turns it into a plan (doses, injection dates, tests due).
- **Patients forget.** Doses, refills, injections, scans and follow-up visits are missed,
  especially by people with chronic conditions. The doctor usually never finds out.
- **Follow-up is manual.** Tests must be done before the review visit, and results must reach
  the doctor. Today the patient has to chase every step.
- **The medical record is scattered** across clinics, labs and paper, so the next doctor sees
  only part of the history.
- **Trust is hard to judge.** Patients cannot easily tell whether a doctor, clinic, lab or
  nurse is genuinely registered and licensed.
- **Family cannot help.** A son or daughter living elsewhere has no way to support a parent's
  care with the parent's permission.

**One-line problem statement:** *After the consultation, patients cannot reliably follow their
own treatment plan, and doctors cannot see whether they did.* **[CONFIRM]**

## 2. Our answer (wedge)
**Care after the consultation.** One connected loop:

**Consultation → structured prescription → automatic reminders → tests, medicines and nurse
visits → results back to the doctor → follow-up.**

The prescription is the starting point. Labs, pharmacy and nurses all consume it, so they are
built after it (roadmap sequencing principle 1).

## 3. Who it is for
| Role | Who | Type |
|---|---|---|
| Patient | Especially people managing chronic conditions | Primary |
| Doctor | Verified clinic GP or specialist | Primary |
| Nurse | Verified, for injections and home care (arrives v4.0) | Primary |
| Caregiver | Family member, acts only with the patient's permission | Extension of the patient role |
| Supporting | Clinic admin (v1.0), lab partner (v2.0), pharmacy partner (v3.0), internal verification/support/admin | Smaller portals |

**Market:** India first, built global-ready. Everything country-specific lives in config or
adapters, so a second country is a new country pack, not a rewrite.

## 4. Core bet (v1.0 hypothesis)
Doctors will write structured prescriptions in our product **if it is faster than paper**.
Patients who get automatic reminders will **come back for refills, tests and follow-ups**.


## 5. What makes us different
1. **Reminders by default.** Every prescription item creates reminders automatically.
2. **Verified professionals only.** No doctor or clinic appears in search, takes bookings or
   prescribes until verified (F17).
3. **Privacy by design.** Doctors see history only with the patient's time-limited,
   revocable consent; every access is audited; health data stays in India (DPDP Act).
4. **Works even when the doctor is not on the platform.** A photographed paper prescription
   becomes reminders after human review (F12, v1.1).
5. **Family-aware.** Caregiver mode with the adult's own consent (F13, v1.1).
6. **A calendar engine built as a separate module,** which can later become a standalone app.

## 6. How the product grows
| Version | Hypothesis / adds |
|---|---|
| v0 | Concierge test of prescriptions and reminders |
| v1.0 | MVP: booking, online consult, structured prescription, reminders, verification of doctors and clinics, consented history, clinic finder, prepay, WhatsApp, calendar core |
| v1.1 | Symptom-based specialist search, SOS, paper-prescription scan, family mode, emergency medical ID, verified-visit reviews |
| v1.2 | Automated hold-and-release payments |
| v2.0 | Book prescribed tests, labs upload results to the doctor, ABDM health records, adherence tracking, follow-up autopilot |
| v3.0 | Order prescribed medicines through licensed partner pharmacies |
| v4.0 | Book a verified nurse for injections and home care, through partners |
| v5.0 | Calendar as a standalone product; first market outside India |

Rule: each version tests one hypothesis. The next version does not start until the current one
meets its exit criteria.

## 7. Non-goals
- **No own medicine inventory.** Medicines come from licensed partner pharmacies.
- **No ambulance dispatch.** SOS dials 112 and alerts contacts; the app does not dispatch an
  ambulance unless a licensed partner is added later.
- **No diagnosis by software.** Symptom search recommends a type of specialist only; red-flag
  symptoms go to emergency options.
- **No prescribing of Schedule X, narcotic or psychotropic drugs** through telemedicine.
- **No selling of search ranking.** Reviews come only from verified visits; paid placement is
  always labelled.

## 8. How we will know it works (v1.0 exit criteria, suggested)
- At least 70% of prescriptions from onboarded doctors are written in the product.
- At least 35% of patients are active at 60 days.
- At least 95% of payments succeed.
- No compliance incidents.

## 9. Open items for the founder
1. Product name, or keep the placeholder for now?
2. Is the one-line problem statement in section 1 right?
3. Nurse is a primary role but arrives in v4.0. The roadmap asks you to decide, before v1.0 scope
   is frozen, whether to keep that order or bring a smaller nurse feature forward.

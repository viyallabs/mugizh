# Medical device software check and intended-use statements (S-022) — DRAFT for lawyer view

**Phase P01 · Epic EP02 · Depends on:** S-009 (lawyer) · **Compliance:** C16 · **Contract:** K-CS-04 · **Features:** F8, F12, F16 (plus the v1.0 rules engine, F7 and F9, as S-008 asked)
**Date:** 2026-11-15 · **Not legal advice.** The views below are **my preliminary reading** for the lawyer to confirm or change. **[VALIDATE]** = lawyer. **[CONFIRM]** = founder.

**Who this is for:** the lawyer, to record a view per feature (acceptance criterion); the founder, to fix the intended-use wording; and design and marketing, who must never make claims beyond it.

## 1. The rule

**Source:** CDSCO "Guidance Document on Medical Device Software under MDR 2017", **CDSCO/MD/GD/MDSW/01/2026, issued 21 Jul 2026** (final, after the Oct 2025 draft). I read secondary summaries only ([Emergo by UL](https://www.emergobyul.com/news/india-cdsco-finalizes-guidance-medical-device-software), [MedDeviceGuide](https://meddeviceguide.com/blog/india-cdsco-medical-device-software-mdsw-guidance-2026-guide), [Cyril Amarchand Mangaldas](https://corporate.cyrilamarchandblogs.com/2026/01/medical-device-as-software-has-cdsco-guidance-changed-the-rules/)). The lawyer must read the 62-page document [VALIDATE].

- **When software is regulated:** it is medical device software (MDSW) when the maker **intends a medical purpose** under s.3(b) of the Drugs and Cosmetics Act: diagnosis, prevention, monitoring, prediction, prognosis, treatment or alleviation.
- **What counts as "intended":** it is judged from **claims, UI text, store listings, website and marketing**, not only from the label. **"Disclaimers do not override functionality or marketing claims."**
- **What is not MDSW:**
  - General wellness software.
  - ERP or quality-system software.
  - Administrative health IT with **no interpretive algorithm**: registration, billing, **basic appointment scheduling**, archiving, and moving records or results.
- **Classes:** class (A lowest to D highest) depends on the seriousness of the condition and the significance of the software's information:

| Condition | Treat or diagnose | Drive clinical management | Inform clinical management |
|---|---|---|---|
| Critical | D | C | B |
| Serious | C | B | A |
| Non-serious | B | A | A |

- **Triage:** software that **"aids in triage" counts as "drives clinical management"**.
- **Lay users:** software used by lay people for a serious condition without clinical support may be treated as critical.
- **Decision support:** software that **recommends drug doses**, scores diagnoses or flags lesions is MDSW.

## 2. Feature-by-feature view

| Feature | Version | What it does | My preliminary view | Why | Lawyer view [VALIDATE] |
|---|---|---|---|---|---|
| **F8 Symptom-based specialist search** | v1.1 | User picks symptoms and gets a suggested **specialty** to book (doctor-written table). Red-flag symptoms open emergency options. | **Highest risk. Borderline.** Specialty suggestion alone is close to directory navigation. **Red-flag routing looks like triage** ("drive clinical management"; conditions such as chest pain are critical; users are lay people). If it is MDSW, likely **Class B or C**. | Triage example in the guidance; lay-user escalation | — |
| **F12 Paper prescription scan → reminders** | v1.1 | Text recognition copies a doctor's paper prescription into reminders, after a trained reviewer and the patient confirm. It never guesses a dose. | **Likely not MDSW.** It transcribes an existing prescription and makes no clinical interpretation or recommendation. | Data management or transfer with no interpretive algorithm; a human checks before use | — |
| **F16 Follow-up autopilot** | v2.0 | Schedules the doctor's own follow-up plan (tests, then visit), escalates reminders, and shows the doctor a list of who is overdue. | **Likely not MDSW**, as long as it only schedules what the doctor ordered and never predicts risk or ranks patients by clinical risk. | Scheduling and reminders | — |
| v1.0 prescription rules engine (S-138) | v1.0 | Blocks items that the telemedicine law forbids for the consult mode; checks that fields are complete. **Never suggests a drug or dose.** | **Likely not MDSW.** It is a regulatory gate, not a clinical decision. Becomes MDSW if it ever recommends doses or checks interactions. | Dose recommendation is the MDSW example; we do not do it (K-CS-03) | — |
| F7 Medicine reminders (v1.0); F7-b adherence and refill alerts (v2.0) | v1.0 / v2.0 | Reminders at the times the doctor prescribed; patient marks doses taken; "refill due" from a pill count | **Likely not MDSW.** Scheduling and arithmetic, with no clinical interpretation. Adherence history is shown, not scored. | — | — |
| F9 SOS | v1.1 | Dials 112, alerts contacts, shows nearby hospitals | **Not MDSW.** No assessment. | Communication | — |

**Lines we must not cross without a new assessment:**
- Any AI or chatbot that gives health advice.
- Symptom "risk scores" or likely conditions.
- Drug interaction or dose suggestions.
- Reading vitals, images or lab values to flag abnormal results.
- Predicting who will miss a follow-up based on clinical data.

## 3. Options for F8 (choose before S-194 and S-196) [CONFIRM after the lawyer's view]

| Option | Design | Regulatory position | Product effect |
|---|---|---|---|
| **A. Navigation only (recommended to start)** | The user browses body areas or common concerns and sees **specialties described in plain words** ("Doctors who treat skin, hair and nails: Dermatologist"). **A permanent emergency banner on every search screen** ("Chest pain, trouble breathing, signs of stroke? Call 112") is shown to everyone. Nothing is computed from symptoms. | Strongest case for not MDSW: a directory with a static safety notice | Less "smart", but meets the goal of helping people pick the right doctor |
| B. Symptom picker with red-flag routing (as written in the roadmap) | The user selects symptoms; the table maps them to a specialty; red-flag combinations jump to the SOS screen | Possibly MDSW, Class B or C (triage). Needs CDSCO licensing, clinical evaluation, and post-market duties | Needs budget and time for licensing before v1.1 |
| C. A, then B later | Ship A in v1.1; license B later if the data shows value | — | — |

## 4. Intended-use statements (fixed wording, draft) [CONFIRM founder · VALIDATE lawyer]

These exact statements go into the store listing, website, the in-app "About this feature" text and the claims register (section 5). **All copy must stay inside them.**

- **F8 (option A):** "Specialist search helps you choose which type of doctor to book, by describing what each specialty treats. It does not assess your symptoms, tell you how urgent they are, or suggest what condition you may have. If you think you have an emergency, call 112."
- **F12:** "Prescription scan copies the medicines, doses and dates from a prescription your doctor wrote into reminders, after a trained reviewer and you check every item. It does not interpret, change or suggest any medicine or dose. If anything is unclear, ask your doctor or pharmacist."
- **F16:** "Follow-up reminders schedule the tests and the return visit your doctor has asked for, and remind you to book them. They do not judge your health or decide what tests you need."
- **Rules engine (doctor-facing):** "Prescription checks block medicines that Indian telemedicine rules do not allow for the type of consultation, and check that required fields are filled. They never suggest a medicine, dose or diagnosis. The doctor is responsible for every prescription."
- **F7 reminders:** "Medicine reminders remind you to take medicines at the times your doctor prescribed. They are not medical advice."

## 5. Guardrails (to build into contracts and reviews)

| ID | Rule | Where |
|---|---|---|
| MD-01 | **Claims register:** every feature claim in UI, store, website, ads and sales decks is checked against section 4 before release. Banned words unless the lawyer approves: "diagnose", "symptom checker", "AI doctor", "detect", "predict", "triage", "health score". | S-048 marketing guide, S-089 microcopy, release checklist |
| MD-02 | Symptom search returns a specialty only, never a diagnosis; red flags go to emergency options (K-CS-04 as written; the wording follows option A or B). | S-072, S-196 |
| MD-03 | Any new feature that interprets health data needs an MDSW check (this document's section 2 table) before design starts. | Story template (S-006) and definition of done |
| MD-04 | The rules engine and F12 never output a drug or dose that the doctor did not write (K-CS-03). | S-138, S-206, S-210 |

## 6. Acceptance criteria check

| Criterion | Met? | Evidence |
|---|---|---|
| Lawyer view recorded per feature | **No, pending** | The section 2 column is empty until S-009 |
| Intended-use wording fixed | **Drafted** | Section 4; founder to approve |

**Story status:** In Testing.

## 7. Decisions and questions

1. **[Founder]** F8: option A (navigation plus a permanent emergency banner, recommended), B (symptom picker with red-flag routing, possibly licensed), or C (A now, B later)?
2. **[Founder]** Approve the section 4 statements and the banned-word list (MD-01).
3. **[Lawyer]** Record a view per feature in section 2. Confirm F12 and the rules engine are outside MDSW, and whether F8 option B would be Class B or C. Confirm the licensing route and timeline if needed.

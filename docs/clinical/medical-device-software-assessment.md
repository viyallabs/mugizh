# Medical device software check and intended-use statements (S-022) — DRAFT for lawyer view

**Phase P01 · Epic EP02 · Depends on:** S-009 (lawyer) · **Compliance:** C16 · **Contract:** K-CS-04 · **Features:** F8, F12, F16 (plus the v1.0 rules engine, F7 and F9, as S-008 asked)
**Date:** 2026-11-15 · **Not legal advice.** The views below are **my preliminary reading** for the lawyer to confirm or change. **[VALIDATE]** = lawyer. **[CONFIRM]** = founder.

**Who this is for:** the lawyer, to record a view per feature (acceptance criterion); the founder, to fix the intended-use wording; and design and marketing, who must never make claims beyond it.

## 1. The rule

**Source:** CDSCO "Guidance Document on Medical Device Software under MDR 2017", **CDSCO/MD/GD/MDSW/01/2026, issued 21 Jul 2026** (final, after the Oct 2025 draft), [official PDF on cdsco.gov.in](https://www.cdsco.gov.in/opencms/export/sites/CDSCO_WEB/Pdf-documents/Guidance-document-on-Medical-Device-Software-under-MDR-2017.pdf).

- **Primary text checked (2026-11-15):** sections 1–8 (pages 1–21: definitions, examples, intended use, classification). Text was extracted from the PDF.
- **Not checked:** sections 9–12 (quality system, licensing pathway, submissions, post-market).
- **Earlier secondary sources:**
  - [Emergo by UL](https://www.emergobyul.com/news/india-cdsco-finalizes-guidance-medical-device-software)
  - [MedDeviceGuide](https://meddeviceguide.com/blog/india-cdsco-medical-device-software-mdsw-guidance-2026-guide)
  - [Cyril Amarchand Mangaldas](https://corporate.cyrilamarchandblogs.com/2026/01/medical-device-as-software-has-cdsco-guidance-changed-the-rules/)
- The guidance says it is for awareness, "should not be misconstrued as a new regulatory control" (§2), and that **CDSCO confirms the risk class** (§7.1). **Every view below is my preliminary reading, not a legal conclusion.** The lawyer must still confirm it [VALIDATE].

- **When software is regulated (§4.9, §4.10):** software is medical device software (MDSW) when its maker intends it for a medical purpose:
  - diagnosis, prevention, monitoring, treatment or alleviation of a disease or disorder;
  - the same for an injury or disability;
  - investigation or support of the anatomy or a physiological process;
  - supporting life.
  - **Mitigation and prediction** "may also be considered" medical purposes.
- **What counts as "intended" (§4.7, §7):** the use stated "on the labelling or in the instructions for use… or in promotional material", and in "labelling details… websites, promotional material, and other information provided by the manufacturer". So UI text, store listings, the website and ads all count. *(An earlier version of this document quoted the guidance as saying "disclaimers do not override functionality or marketing claims". That sentence came from a secondary summary and is not in the guidance. The principle follows from §4.7 and §7, but it is not a quote.)*
- **What is not MDSW (§5.2), in the guidance's words:**
  - Software that performs "transfer, storage, archive data, convert, format, communication, simple search".
  - Hospital and clinical information systems "intended only for patient admission, for scheduling patient appointments/visits, for insurance and billing/invoicing purposes, enabling clinical communication such as voice calling, video calling, to store and transfer patient information".
  - General communication systems used to send messages "such as prescription, referrals, images, patient records".
  - General wellness software that makes no reference to diseases.
  - ERP and quality-system software.
  - **But:** if such a system "has any additional functions that allow its use for any medical purposes… then it may be considered as a medical device" (§5.2 note). When a product mixes device and non-device functions, the device functions are the regulated part (§5.0 note iv).
- **Classes (§7.1, Table 2):** for standalone software, the class (A lowest to D highest) depends on the seriousness of the condition and the significance of the software's information. The table below matches Table 2 exactly:

| Condition | Treat or diagnose | Drive clinical management | Inform clinical management |
|---|---|---|---|
| Critical | D | C | B |
| Serious | C | B | A |
| Non-serious | B | A | A |

- **Triage (§7.1 a-ii), confirmed:** "Drive clinical management" includes information used "to triage or identify early signs of a disease or condition", and "to aid in treatment by providing enhanced support to safe and effective use of medicinal products".
- **Lay users (Table 2 note), confirmed:** software "intended to be used by non-clinical users in a 'serious situation or condition'… without the support from specialized professionals, may be considered as… 'critical'".
- **Decision support (§5.1, §6):** examples of MDSW include software that suggests or excludes conditions from X-rays or ECGs, AI triage of lesions, and an insulin pump calculating a dose. The outputs that make software MDSW include "diagnosis… prediction… prescription, recommended treatment/therapy… recommended additional tests".
- **Also MDSW (§5.1 examples 9 and 10), relevant to later features:**
  - "A behavior-change and digital therapeutics platform intended to mitigate the progression of chronic diseases (e.g., Type 2 diabetes, hypertension) through remote coaching and digital tracking".
  - Platforms that track chronic conditions with "data analytics and interventions to mitigate acute health episodes".
  - **What this means for us:** how we describe reminders, adherence and follow-up features decides which side of the line they fall on.
- **Not mentioned anywhere in the guidance:** symptom checkers, specialist search, medicine reminders, adherence and prescription transcription. Their positions below are my inference from the rules above.

## 2. Feature-by-feature view

| Feature | Version | What it does | My preliminary view | Why | Lawyer view [VALIDATE] |
|---|---|---|---|---|---|
| **F8 Symptom-based specialist search** | v1.1 | User picks symptoms and gets a suggested **specialty** to book (doctor-written table). Red-flag symptoms open emergency options. | **Highest risk. Borderline.** Specialty suggestion alone is close to directory navigation ("simple search"). **Red-flag routing is triage** ("drive clinical management", §7.1), for conditions such as chest pain that are critical, used by lay people. That combination is **likely Class C** (critical × drive clinical management). A specialty suggestion without red flags could be lower. | §7.1 triage wording; Table 2 and the lay-user note | — |
| **F12 Paper prescription scan → reminders** | v1.1 | Text recognition copies a doctor's paper prescription into reminders, after a trained reviewer and the patient confirm. It never guesses a dose. | **Likely not MDSW.** It transcribes an existing prescription and makes no clinical interpretation or recommendation. | §5.2: "transfer, storage… convert, format"; a human checks before use | — |
| **F16 Follow-up autopilot** | v2.0 | Schedules the doctor's own follow-up plan (tests, then visit), escalates reminders, and shows the doctor a list of who is overdue. | **Likely not MDSW**, as long as it only schedules what the doctor ordered and never predicts risk or ranks patients by clinical risk. **Marketing it as improving control of a chronic disease would move it towards §5.1 example 10** (digital therapeutics). | §5.2: scheduling appointments/visits | — |
| v1.0 prescription rules engine (S-138) | v1.0 | Blocks items that the telemedicine law forbids for the consult mode; checks that fields are complete. **Never suggests a drug or dose.** | **Likely not MDSW.** It is a regulatory gate, not a clinical decision. **Counter-argument for the lawyer:** §7.1 counts "enhanced support to safe and effective use of medicinal products" as driving clinical management. Blocking drugs by legal category could be read that way. Our position: its purpose is legal compliance (what the telemedicine guidelines allow per consult mode), and it never recommends anything. It becomes MDSW if it ever recommends doses or checks interactions. | §6 outputs ("prescription, recommended treatment"); we produce none (K-CS-03) | — |
| F7 Medicine reminders (v1.0); F7-b adherence and refill alerts (v2.0) | v1.0 / v2.0 | Reminders at the times the doctor prescribed; patient marks doses taken; "refill due" from a pill count | **Likely not MDSW.** Scheduling and arithmetic, with no clinical interpretation. Adherence history is shown, not scored. **Not if marketed as managing a disease** (§5.1 example 10). | Not named in the guidance; inference | — |
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
| B. Symptom picker with red-flag routing (as written in the roadmap) | The user selects symptoms; the table maps them to a specialty; red-flag combinations jump to the SOS screen | Likely MDSW, probably Class C (triage of critical conditions by lay users, §7.1). Needs CDSCO licensing, clinical evaluation, and post-market duties | Needs budget and time for licensing before v1.1 |
| C. A, then B later | Ship A in v1.1; license B later if the data shows value | — | — |

## 4. Intended-use statements (fixed wording, draft) [CONFIRM founder · VALIDATE lawyer]

These exact statements go into the store listing, website, the in-app "About this feature" text and the claims register (section 5). **All copy must stay inside them.**

- **F8 (option A):** "Specialist search helps you choose which type of doctor to book, by describing what each specialty treats. It does not assess your symptoms, tell you how urgent they are, or suggest what condition you may have. If you think you have an emergency, call 112."
- **F12:** "Prescription scan copies the medicines, doses and dates from a prescription your doctor wrote into reminders, after a trained reviewer and you check every item. It does not interpret, change or suggest any medicine or dose. If anything is unclear, it is not scheduled until the doctor who wrote the prescription clarifies it."
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
| MD-05 | Reminder, adherence and follow-up features are never described as treating, controlling or slowing a disease (for example "manage your diabetes", "better BP control"). That wording points to digital therapeutics (§5.1 example 10). | S-048, S-089, claims register |

## 6. Acceptance criteria check

| Criterion | Met? | Evidence |
|---|---|---|
| Lawyer view recorded per feature | **No, pending** | The section 2 column is empty until S-009 |
| Intended-use wording fixed | **Drafted** | Section 4; founder to approve |

**Story status:** In Testing.

## 7. Decisions and questions

1. **[Founder]** F8: option A (navigation plus a permanent emergency banner, recommended), B (symptom picker with red-flag routing, likely Class C if regulated), or C (A now, B later)? An external review (2026-11-15) also recommends **option A for v1.1**, with red-flag routing kept out until its class is established.
2. **[Founder]** Approve the section 4 statements and the banned-word list (MD-01).
3. **[Lawyer]**
   - Record a view per feature in section 2.
   - Confirm that F12 and the rules engine are outside MDSW. For the rules engine, weigh the §7.1 "safe and effective use of medicinal products" wording.
   - Confirm the class of F8 option B (my reading: C).
   - Read sections 9–12 of the guidance (not yet checked) for the licensing route and timeline.
   - Check CDSCO's published risk-classification list (§7.1 links) for similar software.

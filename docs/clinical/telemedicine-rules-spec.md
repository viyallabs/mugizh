# Telemedicine prescribing rules spec, India (S-012) — DRAFT for doctor review

**Phase P01 · Epic EP02 · Depends on:** S-008 · **Compliance:** C09, C12, C13 · **Contracts:** K-CS-02 (plus K-CS-01, K-CS-03, K-CS-05, K-CS-07, K-GLB-01) · **Edge case:** E-RX-01
**Machine-readable tables:** [rules/IN/telemedicine-rules.v1.draft.yaml](rules/IN/telemedicine-rules.v1.draft.yaml)
**Date:** 2026-11-14 · **Not legal or clinical advice.** **[VALIDATE]** = check against the primary guideline text. **[CONFIRM]** = proposal for the doctor reviewer or founder.

**Who this is for and what it decides:** for a **registered doctor** to review (acceptance criterion; K-CS-07). It decides what the prescription rules engine (S-138) allows or blocks. Engineering builds the engine to load the YAML tables from the India country pack (S-099) and writes one test per table row (K-CS-02).

## 1. Source and how sure we are

- **Source:** Telemedicine Practice Guidelines 2020 (Appendix 5 to the IMC Professional Conduct Regulations 2002), Board of Governors notice of 25 Mar 2020. An NMC notice of Oct 2026 still cites them (S-008, C09).
- **Primary text not opened.** The MoHFW PDF (mohfw.gov.in/pdf/Telemedicine.pdf) returned "forbidden" on 14 Nov 2026. Rules were checked against law-firm summaries:
  - [PSA Legal FAQ](https://psalegal.com/telemedicine-guidelines-2020-faq/)
  - [Khaitan & Co summary](https://khaitanco.com/thought-leaderships/Telemedicine-Practice-Guidelines)
- **Confirmed by those summaries:**
  - Prohibited list: Schedule X and NDPS drugs.
  - List A: first prescription only in a video consult; refills in a follow-up.
  - List B: add-on medicines in a follow-up after an in-person consult, for the same condition.
  - Consent: implied when the patient starts the consult, explicit otherwise.
  - Identification: no anonymous consults; doctor shows registration number.
  - Minors: only with an accompanying adult.
  - Prescriptions: patient gets a signed copy; sent to a pharmacy only with explicit consent.
  - Platform duties: listed in section 7.
- **From my reading of the guidelines, not confirmed by the summaries [VALIDATE]:**
  - The **6-month window** for a follow-up.
  - The exclusions (new symptoms outside the condition; doctor does not recall the context).
  - The **audio and text rows** of the table (List O only for a first consult).
  - The exact prescription-format fields.

The doctor reviewer should check these against the official PDF before approving.

## 2. What the engine decides

For **each prescription item**, the engine looks up:

| Input | Values | Where it comes from |
|---|---|---|
| Drug category | prohibited · List O · List A · unclassified | Drug master (S-126), curated and doctor-reviewed |
| Consult type | first · follow-up | Derived (section 3) |
| Mode at signing | video · audio · text · in-person | Consult record. **A video call that dropped to audio before signing counts as audio.** |
| Context | new drug · refill · add-on | Patient's prescription history with this doctor, plus the doctor's selection |

The first matching row in the decision table wins, and **no match means block** (fail closed). Every block shows a reason (E-RX-01). Prohibited items have **no override**; for other blocks, the doctor can switch to video, or ask the patient to visit in person.

## 3. First consult or follow-up

A consult is a **follow-up** only if all of these are true [VALIDATE]:
1. Same doctor.
2. Within **6 months (183 days)** of the patient's previous **in-person** consult with that doctor.
3. For the **same health condition**: the doctor confirms this with a tick box, which is audited.
4. No new symptoms outside that condition, and the doctor recalls the earlier context: the doctor confirms this too.

Otherwise it is a **first consult**. Evidence of the in-person visit is either a platform record or the doctor's attestation with a date (open question Q2).

## 4. Decision table (summary of the YAML)

| Row | Category | Consult | Mode | Context | Result |
|---|---|---|---|---|---|
| R01 | Prohibited (Schedule X, NDPS) | any | any | any | **Block** RX_PROHIBITED (E-RX-01) |
| R02 | any | any | in-person | any | Allow (telemedicine lists do not apply in person; R01 still blocks prohibited drugs, by platform policy) |
| R03 | List O | first | video, audio, text | any | Allow |
| R04 | List A / unclassified | first | video | new drug | Allow |
| R05 | List A / unclassified | first | audio, text | any | **Block** RX_NEEDS_VIDEO_FIRST [VALIDATE] |
| R06 | List O | follow-up | any remote | any | Allow |
| R07 | List A / unclassified | follow-up | any remote | refill | Allow |
| R08 | List A / unclassified | follow-up | any remote | add-on (List B) | Allow [VALIDATE] |
| R09 | List A / unclassified | follow-up | video | new drug (new problem) | Allow, treated as a first consult on video [VALIDATE] |
| R10 | List A / unclassified | follow-up | audio, text | new drug | **Block** RX_NEEDS_VIDEO_FIRST |

**Platform policy, stricter than the guidelines:** Schedule X and NDPS drugs are blocked in every mode, including in-person prescriptions written in the product (register C13: "blocks these categories everywhere"; product non-goal). Doctors who need them write on paper under the full legal requirements (Q4).

## 5. Checks on the whole prescription (before signing)

| Check | Rule | Source |
|---|---|---|
| P01 | Doctor verified and registration active | K-CS-01, C10 |
| P02 | Doctor confirmed the patient's identity (name, age, plus verified phone or a non-Aadhaar ID) | Guidelines; C23 |
| P03 | Patient age present | Prescription format |
| P04 | Teleconsultation consent recorded: implied if the patient booked, explicit if the doctor, a caregiver or a health worker started it | Guidelines; docs/privacy/consent-texts.md section C |
| P05 | Diagnosis or provisional diagnosis present | Prescribing without one is misconduct (guidelines) |
| P06 | Follow-up attestations present if it is a follow-up | Section 3 |
| P07 | Dose, frequency and duration fields complete; never inferred | E-RX-02, K-CS-03 |

## 6. Identification, consent and records

- **Doctor shows** name, qualification, registration number and council at the start of the consult, on the profile and on the prescription (R-15).
- **Patient identity:** no anonymous consults. The doctor confirms name and age; our OTP-verified phone counts as the contact detail. Never ask for an Aadhaar number (C23).
- **Minors:** not supported in v1.0 (founder decision 3). From v1.1 the guidelines need an accompanying adult whose identity is confirmed, and caregivers need proof of the relationship (S-200).
- **Consent:** see P04. Explicit consent can be a tap in the app, a recorded statement or a text message, and is stored with the booking.
- **Records** (doctor's duty; we store them under RET-02 in docs/data-inventory-v1.0.md): consult log, call or video metadata (**no recording**), chat transcript if text, images and reports shared, the prescription and the consent record. Signed records are immutable; changes are new versions (K-CS-05).
- **Sending to a pharmacy:** only with the patient's explicit consent (v3.0). In v1.0 the patient receives the signed PDF and shares it themselves.

## 7. Platform duties (what the product must do)

| Duty [VALIDATE wording] | How we meet it |
|---|---|
| Only registered practitioners | Verification pipeline; prescribing gate P01 (K-CS-01) |
| Show each practitioner's name, qualification, registration number and contact details | Doctor profile and prescription (R-15, R-21) |
| Mechanism for queries and grievances | Grievance process (docs/privacy/data-rights-and-grievance.md) |
| AI or machine-learning platforms must not counsel patients or prescribe | The engine only **blocks**; it never suggests a drug or dose (K-CS-03). No AI chat advice in v1.0. |
| Non-compliance can lead to blacklisting | Rules tested per row; doctor review of every rule change (K-CS-07) |

## 8. Prescription format (fields the PDF must carry) [VALIDATE against the guidelines' sample]

- **Doctor:** name, qualification, registration number and council, clinic address, contact.
- **Patient:** name, age, sex, contact; date.
- **Clinical:** chief complaints, relevant findings, diagnosis or provisional diagnosis, suggested investigations.
- **Rx:** drug name (generic name), strength, form, dose, frequency, duration, route, instructions.
- **Other:** special instructions or advice, follow-up date.
- **Signature:** the doctor's signature or electronic signature (validity is a lawyer question, proposed row N5), and the line "Issued after a teleconsultation (mode: video/audio)" [CONFIRM].

## 9. Test cases for S-138 (one or more per row)

| Test | Given | Expect |
|---|---|---|
| T01 (E-RX-01) | Teleconsult, video, first, Schedule X drug | Block, RX_PROHIBITED, no override |
| T02 | In-person, NDPS drug | Block (platform policy) |
| T03 | First, audio, paracetamol (List O) | Allow |
| T04 | First, video, new List A drug | Allow |
| T05 | First, audio, List A drug | Block, RX_NEEDS_VIDEO_FIRST |
| T06 | Video call dropped to audio before signing; List A, first | Block (mode at signing = audio) |
| T07 | Follow-up (in-person 120 days ago, same doctor and condition), audio, refill of a List A drug | Allow |
| T08 | Same, but in-person visit 200 days ago | Treated as first; List A on audio blocked |
| T09 | Follow-up, text, add-on drug for the same condition | Allow (List B) |
| T10 | Follow-up, audio, new drug for a new problem | Block, RX_NEEDS_VIDEO_FIRST |
| T11 | Unclassified drug, first, video | Allow (treated as List A) |
| T12 | Unclassified drug, first, audio | Block |
| T13 | Doctor licence lapsed (E-VER-03) | Block whole prescription, RX_DOCTOR_NOT_VERIFIED |
| T14 | No diagnosis entered | Block, RX_DIAGNOSIS_MISSING |
| T15 | Doctor-initiated consult, no explicit consent | Block, RX_CONSENT_MISSING |
| T16 | Follow-up claimed but attestations missing | Block, RX_FOLLOW_UP_NOT_ESTABLISHED |
| T17 | Dose field empty | Block, RX_DOSE_INCOMPLETE |
| T18 | Any block | Reason shown with an i18n key; audit event written |
| T19 | Performance | Under 50 ms per prescription (Performance sheet) |
| T20 | Unknown combination not in the table | Block (fail closed) |

## 10. Acceptance criteria check

| Criterion | Met? | Evidence |
|---|---|---|
| Machine-readable spec drafted for the rules engine | **Yes** | YAML decision tables, checks, consent, identification, records and reason messages |
| Reviewed by a registered doctor | **No, pending** | Needs a doctor reviewer (name, registration number, date in `reviewed_by`). No doctor advisor is named yet (S-008 decision 1). |

**Story status:** In Testing.

## 11. Open questions for the doctor reviewer [CONFIRM]

1. **Q1 Unclassified drugs:** treat as List A (video needed for the first prescription) until curated, as drafted? Or block until curated?
2. **Q2 Off-platform in-person visits:** may a doctor attest a paper or clinic visit with a date to unlock follow-up rules? It is audited.
3. **Q3 Video-then-audio:** if a patient's first video consult happened on the platform, can a later audio consult refill a List A drug? The strict reading says a follow-up needs an in-person visit first.
4. **Q4 Schedule X and NDPS in person:** block in the product everywhere (drafted), or allow in-person e-prescriptions with the extra legal requirements?
5. **Q5 List O and List A curation:** who curates the drug-to-category map in the drug master (S-126), and how often?
6. **Q6 Schedule H1** record requirements (pharmacist register) affect v3.0 only; confirm nothing is needed in v1.0.
7. **Q7** Is the 6-month window measured from the last in-person visit (drafted) or from the first?

**Proposed new edge case (for the Edge Cases sheet):** E-RX-06, "Video consult falls back to audio before the prescription is signed": rules use the mode at signing, and List A first prescriptions are blocked, with a prompt to restore video.

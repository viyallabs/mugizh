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
- **Amendment of April 2020 (Board of Governors; sources give 11, 12 or 18 Apr):** phenobarbitone, clobazam and clonazepam were moved to List A, for first consults and follow-ups. They are NDPS psychotropics, so the plain "NDPS = prohibited" match is wrong for these three. Reported by Medical Dialogues, Business Standard and the Indian Psychiatric Society; **original notice not read, and not found online on 15 Nov 2026 [VALIDATE]**. Recorded as `AMD-2020-04` in the YAML. Platform rule PL03 keeps blocking them until the notice and whether it is still in force are confirmed (lawyer), and the doctor reviewer decides (Q11).
- **From my reading of the guidelines, not confirmed by the summaries [VALIDATE]:**
  - The **6-month window** for a follow-up.
  - The exclusions (new symptoms outside the condition; doctor does not recall the context).
  - The **audio and text rows** of the table (List O only for a first consult).
  - The exact prescription-format fields.
  - **Patient address:** the guidelines list address among the patient's identity details (name, age, address, email, phone, ID). Is it mandatory, or one of several options? It is not collected now; add it only if required (data minimisation).

The doctor reviewer should check these against the official PDF before approving.

## 2. What the engine decides

For **each prescription item**, the engine looks up:

| Input | Values | Where it comes from |
|---|---|---|
| Drug lists | prohibited · List O · List A · List B (a drug can be in more than one list); no list = unclassified | Drug master (S-126), curated and doctor-reviewed. **The classification has its own version**, stored with every prescription. |
| Consult type | first · follow-up, **decided per item**: in a follow-up, an item the doctor marks as being for a new condition is evaluated as a first consult | Derived (section 3) plus the doctor's selection |
| Mode at signing | video · audio · text · in-person | Consult record. **A video call that dropped to audio before signing counts as audio.** |
| Context | new drug · refill · add-on | Patient's prescription history with this doctor, plus the doctor's selection |

There are **two layers**: the regulatory table (what the guidelines allow) and platform rules (stricter limits we choose). An item is allowed only if the regulatory table allows it **and** no platform rule blocks it. In the regulatory table the first matching row wins, and **no match means block** (fail closed). Every block shows a reason (E-RX-01). Prohibited items have **no override**. For an RX_NEEDS_VIDEO_FIRST block, the doctor can switch to video or ask the patient to visit in person. **Switching to video never unlocks** a prohibited, unclassified or platform-blocked drug.

## 3. First consult or follow-up

A consult is a **follow-up** only if all of these are true [VALIDATE]:
1. Same doctor.
2. Within **6 calendar months** of the patient's previous **in-person** consult with that doctor, counted in the patient's time zone. The day is included, and month ends clamp: 31 Aug + 6 months = end of February.
3. For the **same health condition**: the doctor confirms this with a tick box, which is audited.
4. No new symptoms outside that condition, and the doctor recalls the earlier context: the doctor confirms this too.

Otherwise it is a **first consult**. Evidence of the in-person visit is either a platform record or the doctor's attestation with a date (open question Q2).

## 4. Decision table (summary of the YAML)

**Precedence:** a drug can never be both prohibited and permitted. "Prohibited" wins over every list. Only a drug named in an amendment's exception leaves "prohibited". The drug master import rejects any drug that matches "prohibited" and is also in List O, A or B without such an exception.

**Regulatory table**

| Row | List | Consult | Mode | Context | Result |
|---|---|---|---|---|---|
| R01 | Prohibited (Schedule X, NDPS; except the three amendment drugs) | any | any remote | any | **Block** RX_PROHIBITED (E-RX-01) |
| R02 | any | any | in-person | any | Allow (the telemedicine lists do not govern in-person prescribing) |
| R03 | List O | first | any remote | any | Allow |
| R04 | List A | first | video | new drug | Allow |
| R05 | List A | first | audio, text | any | **Block** RX_NEEDS_VIDEO_FIRST [VALIDATE] |
| R06 | List O | follow-up | any remote | any | Allow |
| R07 | List A | follow-up | any remote | refill | Allow |
| R08 | **List B** | follow-up | any remote | add-on | Allow [VALIDATE] |
| R09 | List A | follow-up | video | new drug or add-on, same condition | Allow: video meets the List A first-prescription rule [VALIDATE] |
| R10 | List A | follow-up | audio, text | new drug, or add-on not in List B | **Block** RX_NEEDS_VIDEO_FIRST |

A drug in no list (unclassified) matches no remote row, so it is blocked.

**Platform rules (stricter than the guidelines; product decisions)**

| Rule | Applies to | Mode | Result | Decision |
|---|---|---|---|---|
| PL01 | Prohibited drugs | in-person | **Block** RX_PROHIBITED_IN_PRODUCT. Doctors who need them write on paper under the full legal requirements (register C13; product non-goal). | Q4 |
| PL02 | Unclassified drugs | any remote | **Block** RX_NOT_CLASSIFIED until clinically classified | Q1 |
| PL03 | Phenobarbitone, clobazam, clonazepam | any | **Block** RX_CONTROLLED_IN_PRODUCT, although the April 2020 amendment allows them | Q11 |

## 5. Checks on the whole prescription (before signing)

| Check | Rule | Source |
|---|---|---|
| P01 | Doctor verified and registration active | K-CS-01, C10 |
| P02 | Doctor confirmed the patient's identity (name, age, plus verified phone or a non-Aadhaar ID; address if the official text requires it [VALIDATE]) | Guidelines; C23 |
| P03 | Patient age present | Prescription format |
| P04 | Teleconsultation consent recorded: implied if the patient booked, explicit if the doctor, a caregiver or a health worker started it | Guidelines; docs/privacy/consent-texts.md section C |
| P05 | Diagnosis or provisional diagnosis present | Prescribing without one is misconduct (guidelines) |
| P06 | Follow-up attestations present if it is a follow-up | Section 3 |
| P07 | Dose, frequency and duration fields complete; never inferred | E-RX-02, K-CS-03 |
| P08 | Doctor re-authenticated (step-up): proves **who** is signing | K-SEC-03 |
| P09 | Prescription signed with the **approved electronic signature method**: authenticates the **document** | C12, N5 (IT Act 2000) |

**P08 is not a signature.** P09 signs a hash of the final prescription with the method the lawyer approves under N5. Examples: an electronic signature from a licensed eSign service provider, or a Class 3 digital signature certificate. The signed hash makes any later change detectable. **Until N5 is decided, P09 blocks signing in production** (fail closed). The provider sits behind an adapter (K-ARC-03).

**Every evaluation is stored** with the prescription (RET-02): the rule-set version, the drug classification version, the item inputs, the rules matched, the layer, the outcome and the reason.

**Not in this ruleset, on purpose:**
- **Allergy, interaction and dose-range checks.** The guidelines don't require them. Automated clinical checks are clinical decision support, so they must go through the [medical device software assessment](medical-device-software-assessment.md) before being added.
- **Clinical appropriateness** stays the doctor's judgement.
- **The doctor can always end without prescribing:** if the information is not enough, they ask for an in-person visit. In an emergency they advise immediate care and refer. The consult outcomes are "completed with prescription", "completed without prescription", "referred in person" and "emergency referral".

## 6. Identification, consent and records

- **Doctor shows** name, qualification, registration number and council at the start of the consult, on the profile and on the prescription (R-15).
- **Patient identity:** no anonymous consults. The doctor confirms name and age; our OTP-verified phone counts as the contact detail. Never ask for an Aadhaar number (C23).
- **Minors:** not supported in v1.0 (founder decision 3). **This is a product decision, not a legal ban.** From v1.1 the guidelines need an accompanying adult whose identity is confirmed, and caregivers need proof of the relationship (S-200).
- **Consent:** see P04. Explicit consent can be a tap in the app, a recorded statement or a text message, and is stored with the booking.
- **Records** (doctor's duty; we store them under RET-02 in docs/data-inventory-v1.0.md: 3 years after the patient's last consult with that doctor, plus legal hold [VALIDATE]): consult log, call or video metadata (**no recording**), chat transcript if text, images and reports shared, the prescription and the consent record. Signed records are immutable; changes are new versions (K-CS-05).
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
- **Patient:** name, age, sex, contact; address only if required [VALIDATE]; date.
- **Clinical:** chief complaints, relevant findings, diagnosis or provisional diagnosis, suggested investigations.
- **Rx:** drug name (generic name), strength, form, dose, frequency, duration, route, instructions.
- **Other:** special instructions or advice, follow-up date.
- **Signature:** the doctor's electronic signature applied under P09 (method decided by the lawyer, row N5), with a verification reference printed on the PDF, and the line "Issued after a teleconsultation (mode: video/audio)" [CONFIRM].

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
| T09 | Follow-up, text, add-on drug in List B, same condition | Allow (R08) |
| T10 | Follow-up, audio, List A drug marked "new condition" | Evaluated as first: Block, RX_NEEDS_VIDEO_FIRST |
| T11 | Unclassified drug, first, video | Block, RX_NOT_CLASSIFIED (PL02) |
| T12 | Unclassified drug, first, audio | Block, RX_NOT_CLASSIFIED (PL02) |
| T13 | Doctor licence lapsed (E-VER-03) | Block whole prescription, RX_DOCTOR_NOT_VERIFIED |
| T14 | No diagnosis entered | Block, RX_DIAGNOSIS_MISSING |
| T15 | Doctor-initiated consult, no explicit consent | Block, RX_CONSENT_MISSING |
| T16 | Follow-up claimed but attestations missing | Block, RX_FOLLOW_UP_NOT_ESTABLISHED |
| T17 | Dose field empty | Block, RX_DOSE_INCOMPLETE |
| T18 | Any block | Reason shown with an i18n key; audit event written |
| T19 | Performance | Under 50 ms per prescription (Performance sheet) |
| T20 | Unknown combination not in the table | Block (fail closed) |
| T21 | Follow-up, audio, add-on of a List A drug that is not in List B | Block, RX_NEEDS_VIDEO_FIRST (R10) |
| T22 | Clonazepam, first, video | Regulatory allow (List A by amendment); Block, RX_CONTROLLED_IN_PRODUCT (PL03) |
| T23 | Other NDPS drug, first, video | Block, RX_PROHIBITED (R01) |
| T24 | In-person visit 31 Aug; teleconsult on the last day of February | Follow-up (window clamps to month end); 1 March is not |
| T25 | Missing step-up at signing | Block, RX_SIGNATURE_REQUIRED (P08) |
| T26 | Any evaluation | Stored with rule-set and drug classification versions |
| T27 | Step-up done, but no electronic signature applied | Block, RX_ESIGN_REQUIRED (P09) |
| T28 | Signed prescription changed by one byte | Signature check fails; the change is detected |
| T29 | Unclassified drug blocked on audio; doctor switches the consult to video | Still blocked, RX_NOT_CLASSIFIED (PL02) |
| T30 | Drug master import with a drug that matches "prohibited" and is also in List A, without an amendment exception | Import rejected; no new classification version |

## 10. Acceptance criteria check

| Criterion | Met? | Evidence |
|---|---|---|
| Machine-readable spec drafted for the rules engine | **Yes** | YAML decision tables, checks, consent, identification, records and reason messages |
| Reviewed by a registered doctor | **No, pending** | Needs a doctor reviewer (name, registration number, date in `reviewed_by`). No doctor advisor is named yet (S-008 decision 1). |

**Story status:** In Testing.

## 11. Open questions for the doctor reviewer [CONFIRM]

1. **Q1 Unclassified drugs:** now drafted as **block in remote consults until classified** (PL02, fail closed). Confirm, or allow them as List A?
2. **Q2 Off-platform in-person visits:** may a doctor attest a paper or clinic visit with a date to unlock follow-up rules? It is audited.
3. **Q3 Video-then-audio:** if a patient's first video consult happened on the platform, can a later audio consult refill a List A drug? The strict reading says a follow-up needs an in-person visit first.
4. **Q4 Schedule X and NDPS in person:** block in the product everywhere (drafted), or allow in-person e-prescriptions with the extra legal requirements?
5. **Q5 List O and List A curation:** who curates the drug-to-category map in the drug master (S-126), and how often?
6. **Q6 Schedule H1** record requirements (pharmacist register) affect v3.0 only; confirm nothing is needed in v1.0.
7. **Q7** Is the 6-month window measured from the last in-person visit (drafted) or from the first?
8. **Q11 Amendment drugs (phenobarbitone, clobazam, clonazepam):** the April 2020 amendment allows them as List A. Keep them blocked in the product (PL03, drafted), or allow them under the List A rules? Also confirm the amendment is still in force.
9. **List B curation:** List B is now its own curated list (R08). Which add-on medicines go in it, per condition?

**For the lawyer (S-009):**
- **N5:** which electronic signature method makes the prescription legally valid (P09)?
- **Amendment:** confirm the original notice of the April 2020 amendment and whether it is still in force (PL03).
- **Patient address:** is it a mandatory identity detail (P02)?

**Before production:** registered-doctor review (`reviewed_by`) and legal review (`legal_reviewed_by`) are both required. Status goes draft → doctor_reviewed → legal_reviewed → approved.

**Change log:** v1.1.0-draft (2026-11-15):
- List B is its own list.
- Unclassified drugs are blocked.
- The follow-up window is 6 calendar months.
- A new condition in a follow-up is evaluated as a first consult.
- The April 2020 amendment is reconciled.
- Regulatory and platform layers are separated.
- P08 and audit fields are added.
- Doctor-judgement outcomes and pharmacy consent are recorded.
- The RET-02 period is stated.
- Step-up (P08) is separated from the electronic signature (P09, N5).
- The precedence rule and drug master import checks are added.
- Patient address is added for validation.
- Tests T27–T30 are added.

**Proposed new edge case (for the Edge Cases sheet):** E-RX-06, "Video consult falls back to audio before the prescription is signed": rules use the mode at signing, and List A first prescriptions are blocked, with a prompt to restore video.

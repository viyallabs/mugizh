# Aadhaar handling policy (S-013) — DRAFT

**Phase P01 · Epic EP02 · Depends on:** S-008 · **Compliance:** C23 · **Contract:** K-DP-05 · **Features:** F17, F2
**Date:** 2026-11-15 · **Status:** DRAFT for founder approval and lawyer check (S-009). Not legal advice.
**[VALIDATE]** = legal point for the lawyer. **[CONFIRM]** = proposal for the founder.

**Who this is for:** founder and lawyer to approve; engineering to build against. **Where it goes:** the acceptance criterion says "policy added to the data-protection contract". Claude may not write contracts (K-DOD-03), and `/contracts` is created in P08. S-071 copies section 3 of this file into `contracts/compliance/data-protection.md` under K-DP-05.

## 1. The rule in one line

**We never collect, store, log or display an Aadhaar number, VID or Aadhaar biometric, in any version, unless a later story adds an authorised flow that the lawyer has approved.**

**The one thing we cannot fully prevent:** a patient may upload an image that shows an Aadhaar number despite the warning. **We cannot detect this automatically in v1.0**; OCR detection comes in v1.1. So the honest promise is: we never ask for it, and we never read it into data. When an upload is found to contain one, it is **quarantined at once and deleted** (AAD-05).

## 2. Why (legal basis) [VALIDATE all of section 2]

The register's own source for C23 is "general knowledge, confirm with lawyer". I did not re-check these against primary text for this story.

- **Aadhaar Act 2016, as amended in 2019:** only entities that the law or UIDAI permits may do Aadhaar authentication. After the Supreme Court's 2018 Puttaswamy (Aadhaar) judgment, private companies cannot require Aadhaar. We are not a permitted requesting entity.
- **Aadhaar (Sharing of Information) Regulations 2016:** an entity that holds Aadhaar numbers must not publish or display them, and must protect them.
- **UIDAI circulars (2017 onward):** an entity allowed to store Aadhaar numbers must keep them in an encrypted "Aadhaar Data Vault". We avoid this cost and risk by never storing them.
- **Good-governance authentication rules, amended 2025:** private entities can apply to the government to do Aadhaar authentication for listed purposes. We are **not** applying in v1.0.
- **DPDP Act 2023 (data minimisation):** we do not need Aadhaar for any v1.0 purpose, so collecting it would breach the minimisation duty.
- **Telemedicine Practice Guidelines 2020:** patient identity can be confirmed without Aadhaar (name, age, verified phone or other ID). See [telemedicine-rules-spec.md](../clinical/telemedicine-rules-spec.md), check P02.

## 3. Policy rules (to copy into K-DP-05 in S-071)

| ID | Rule | Applies to | How it is checked |
|---|---|---|---|
| AAD-01 | No data field, form field, API field or database column holds an Aadhaar number or VID. No field is named `aadhaar`, `uid`, `vid` or similar, except in deny-lists and tests. | All modules | Schema review; CI scan for field names (S-091) |
| AAD-02 | Free-text input that contains a valid Aadhaar number is **rejected** with a message asking the user to remove it. A valid number has 12 digits, the first digit 2–9, and a correct Verhoeff check digit. A 16-digit VID is handled the same way. | Booking notes, chat, clinical notes, support tickets, names, addresses | Shared validator in `packages/shared-types`; unit tests with valid, invalid and phone-number cases |
| AAD-03 | Logs, traces, analytics events and error reports pass through a scrubber that replaces any valid Aadhaar number or VID with `[REDACTED-AADHAAR]`. | Logging, analytics, error reporting | Scrubbing test in S-095; planted number never reaches log storage |
| AAD-04 | **Doctors' identity documents:** an Aadhaar card is **not accepted**. Accept PAN, passport, driving licence or voter ID, plus the council registration certificate. If a reviewer sees an Aadhaar card in an upload, they reject it and the file is deleted at once (not after 90 days). [CONFIRM, open decision 16] | Verification (S-110, S-258) | Upload screen lists accepted IDs; reviewer checklist (S-129 review queue); deletion audited |
| AAD-05 | **Patients' uploaded documents:** before every upload, the app shows "Do not upload an Aadhaar card. If a document shows an Aadhaar number, cover it first." If an upload is found to contain an Aadhaar number (patient report, support, doctor report or a later scan): (1) **immediately**, on the report and without anyone opening the file again, it is **quarantined**: hidden from the record, from doctors and from sharing; no staff role can open it; excluded from search, exports and new backups. (2) The patient is told, and asked to upload a masked copy if they still need the document. (3) The quarantined file is **hard-deleted automatically within 24 hours** [CONFIRM]; only the deletion job can touch it. (4) The event (file reference, reporter, time, deletion time; **never the number**) is audit-logged. Existing backups age out under the backup policy and are covered by the patient's crypto-shredding key on erasure. | Records upload (S-159), prescription photos (S-199) | Copy in i18n file; support runbook; from v1.1, the OCR adapter (S-205) also flags Aadhaar numbers |
| AAD-06 | A **masked Aadhaar** (only the last 4 digits visible, as UIDAI's masked e-Aadhaar shows) is allowed in an uploaded image where unavoidable. We never extract or store the last 4 digits as data. | Uploads | Reviewer checklist |
| AAD-07 | We never ask for Aadhaar to sign up, book, consult, pay or verify. Phone OTP is the sign-in method. | Identity (S-104) | Design review; usability test |
| AAD-08 | **ABHA (v2.0, S-229):** if a patient creates an ABHA using Aadhaar, that happens inside the NHA/ABDM flow. Our app passes the Aadhaar-based OTP request to ABDM and does not store, log or cache the Aadhaar number, even briefly in memory beyond the request. We store only the ABHA number and ABHA address. [VALIDATE against ABDM integration rules] | ABDM integration | Sandbox test; log scan; ABDM certification |
| AAD-09 | Any future flow that **does** use Aadhaar (eKYC, offline XML/QR verification, DigiLocker documents) needs a new ADR, a lawyer's sign-off, and a change to this policy before work starts. | All future stories | ADR; PR check (K-DOD) |
| AAD-10 | Test data and fixtures use only numbers that fail the Verhoeff check, or the official UIDAI test numbers, never real ones. | Engineering | CI scan |

**Edge cases this adds (proposed for the Edge Cases sheet):**
- **E-DP-26:** a patient types an Aadhaar number into a chat or booking note. Expected: the message is blocked with a reason, nothing is stored, and no log line holds the number.
- **E-DP-27:** a doctor uploads an Aadhaar card as ID. Expected: the reviewer rejects it, the file is deleted immediately, and the doctor is asked for another ID.

## 4. What this changes in other documents

| Document or story | Change |
|---|---|
| [privacy-notice.md](privacy-notice.md) section 3 | Already says we do not collect Aadhaar and asks patients to cover the number. No change. |
| [data-inventory-v1.0.md](../data-inventory-v1.0.md), identity document row | Add "Aadhaar card not accepted (AAD-04); if received, deleted at once". Done in this story. |
| [telemedicine-rules-spec.md](../clinical/telemedicine-rules-spec.md), check P02 | Already says non-Aadhaar ID. No change. |
| S-071 (data-protection contract) | Copy section 3 into K-DP-05. |
| S-091 (CI pipeline) | Add the field-name scan and the fixture scan (AAD-01, AAD-10). |
| S-095 (observability) | Add the Aadhaar and VID pattern to the scrubber (AAD-03). |
| S-104 (phone sign-up) | No Aadhaar anywhere in sign-up (AAD-07). |
| S-129 (verification review queue) | Reviewer checklist: reject Aadhaar cards and delete them at once (AAD-04, AAD-06). |
| S-110, S-258 (doctor and nurse ID) | Accepted ID list without Aadhaar; immediate deletion if one is uploaded (AAD-04). |
| S-159, S-199 (patient uploads) | Upload warning; a "report Aadhaar" action for patients, doctors and support; immediate quarantine; deletion within 24 hours (AAD-05). |
| S-205 (OCR, v1.1) | Flag Aadhaar numbers in extracted text (AAD-05). |
| S-229 (ABHA, v2.0) | Pass-through only (AAD-08). |

## 5. Acceptance criteria check

| Criterion | Met? | Evidence |
|---|---|---|
| Policy written: never store Aadhaar numbers; only authorised flows | **Yes** | Sections 1 and 3 |
| Policy added to the data-protection contract | **Ready, not added** | The contract file does not exist until S-071 (P08), and Claude may not edit contracts (K-DOD-03). Section 3 is ready to copy. |

**Story status:** In Testing, until the founder decides open decision 16 and the lawyer confirms section 2.

## 6. Decisions needed

1. **[Founder, open decision 16]** Doctors' ID: reject Aadhaar cards completely (recommended, AAD-04), or accept masked ones?
2. **[Founder]** Immediate quarantine, then deletion within 24 hours, for Aadhaar found in patient uploads (AAD-05; was 7 days with the file still visible)?
3. **[Lawyer]** Confirm section 2, and confirm that rejecting free text with an Aadhaar number (AAD-02) and pass-through ABHA creation (AAD-08) are enough.

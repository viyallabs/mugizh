# Contract: Clinical safety (K-CS)

**Story:** S-072 · **Status: DRAFT, binding on Claude now; formally in force after the lawyer and doctor review (S-085) and founder approval.** · **Source:** Contracts sheet, generated 2026-11-15

**Rules for this file (K-DOD-03):**

- The **Rule** text below is copied word for word from the Contracts sheet.
- **It changes only through an ADR the founder approves.** Claude must never edit this file to make code pass.
- **Implementation notes** are not part of the rule. They point to the design that meets it, and may be updated with the design.

## K-CS-01

**Rule:** Only Verified professionals with active registration may be listed, booked, prescribe or be assigned visits; checked server-side.

| Why | How it is enforced | Applies to |
|---|---|---|
| C10, C26 | Server guard + tests | verification, clinical, homecare |

*Implementation notes (not part of the rule):* Server-side checks in directory, booking, consult and clinical (rules check P01). Re-verification (S-160) suspends listing and prescribing. Impersonation controls: threat model TM-28 (E-SEC-09).

## K-CS-02

**Rule:** Every prescription and order passes the rules engine (consult mode, first/follow-up, drug lists, prohibited categories).

| Why | How it is enforced | Applies to |
|---|---|---|
| C09, C12, C13 | Rules-engine tests per table row | clinical, pharmacy |

*Implementation notes (not part of the rule):* Rules spec and YAML v1.1.0-draft (docs/clinical/telemedicine-rules-spec.md): regulatory table first-match-wins, platform rules PL01-PL03, fail closed, per-item consult type, checks P01-P10 re-run at signing, rule-set and drug-classification versions stored (docs/arch/algorithms.md section 1). Not approved until doctor and lawyer review.

## K-CS-03

**Rule:** The system never guesses, auto-fills or infers a dose. Unreviewed or low-confidence items never create reminders.

| Why | How it is enforced | Applies to |
|---|---|---|
| Patient safety (F12) | Tests | prescription scan |

*Implementation notes (not part of the rule):* Applies beyond prescription scan: the rules engine only allows or blocks and never suggests a drug or dose (MD-04). Unclear scanned items are clarified only by the prescribing doctor (E-OCR-02).

## K-CS-04

**Rule:** Symptom search returns a specialty only, never a diagnosis; red-flag symptoms go to emergency options.

| Why | How it is enforced | Applies to |
|---|---|---|
| C16 | Copy review + tests | symptom search |

*Implementation notes (not part of the rule):* F8 option A (recommended, docs/clinical/medical-device-software-assessment.md): no symptom computation, a permanent emergency banner. Banned claims MD-01.

## K-CS-05

**Rule:** Signed notes and prescriptions are immutable; changes are new versions with audit.

| Why | How it is enforced | Applies to |
|---|---|---|
| C09, C30 | DB constraint + tests | clinical |

*Implementation notes (not part of the rule):* Amendments are new versions (S-164). Signing = step-up (P08) + electronic signature over the document hash (P09). Database role cannot UPDATE signed rows.

## K-CS-06

**Rule:** SOS dials the emergency number directly even when our servers are unreachable; the app never claims to dispatch help.

| Why | How it is enforced | Applies to |
|---|---|---|
| C33 | Offline E2E test | emergency |

*Implementation notes (not part of the rule):* E-SOS-01: the phone dialler calls the number from the country pack (112 in India); the app shows only "Calling 112" and the real call state.

## K-CS-07

**Rule:** Any change to clinical rules, clinical copy or safety checklists needs review by a registered doctor, recorded in the PR.

| Why | How it is enforced | Applies to |
|---|---|---|
| Patient safety | CODEOWNERS (clinical reviewer) | clinical content |

*Implementation notes (not part of the rule):* Covers the telemedicine rule YAML, the drug classification, clinical copy (block messages, reminder wording, SMS-09 to SMS-11) and safety checklists. CODEOWNERS: clinical reviewer.

## Proposed changes (each needs an ADR approved by the founder)

- **New K-CS-08** (from S-018): "Search ranking and listing visibility MUST NOT take payment, subscription tier or partner revenue as an input; ranking factors are documented, published and tested."


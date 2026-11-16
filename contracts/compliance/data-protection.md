# Contract: Data protection (K-DP)

**Story:** S-071 · **Status: DRAFT, binding on Claude now; formally in force after the lawyer and doctor review (S-085) and founder approval.** · **Source:** Contracts sheet, generated 2026-11-15

**Rules for this file (K-DOD-03):**

- The **Rule** text below is copied word for word from the Contracts sheet.
- **It changes only through an ADR the founder approves.** Claude must never edit this file to make code pass.
- **Implementation notes** are not part of the rule. They point to the design that meets it, and may be updated with the design.

## K-DP-01

**Rule:** Personal and health data MUST be stored and processed only in India-region infrastructure and India-resident vendors.

| Why | How it is enforced | Applies to |
|---|---|---|
| C05, C07 | IaC policy check; vendor register | All |

*Implementation notes (not part of the rule):* Residency checks R1-R7 in docs/arch/infrastructure-and-environments.md section 4; vendor register; country pack `residency` (docs/arch/country-pack-model.md). India-only is our own stricter policy: DPDP s.16 restricts transfers only to countries the government lists (C05). Known exception awaiting the lawyer: push via Google/Apple with no health data.

## K-DP-02

**Rule:** Any read of a patient's data by anyone other than the patient MUST check an active consent (or permission) record and write an audit event.

| Why | How it is enforced | Applies to |
|---|---|---|
| C01, C09 | Middleware + automated tests | records, clinical, family, labs |

*Implementation notes (not part of the rule):* One gate: `records.authorize` (docs/security/consent-design.md section 3) with access tiers T1-T4 (docs/privacy/consent-texts.md A2). Workers and other modules call it too (for example notifications at send time). Audit event written durably before data is returned (ADR-28). No bypass flag; staff use just-in-time grants (K-SEC-08).

## K-DP-03

**Rule:** Sensitive fields (clinical notes, diagnoses, prescriptions, documents, contact details) MUST use field-level encryption with KMS envelope keys; per-patient keys allow crypto-shredding.

| Why | How it is enforced | Applies to |
|---|---|---|
| C03 | Lint rule on schema annotations + tests | All data stores |

*Implementation notes (not part of the rule):* docs/security/encryption-design.md sections 2-5: AES-256-GCM, row-bound associated data, patient key (erasable data), care key per patient-doctor (legal-retention clinical records), blind index for phone lookup, separate key store with backups of at most 35 days.

## K-DP-04

**Rule:** Logs, analytics, crash reports and error messages MUST NOT contain health data or full identifiers.

| Why | How it is enforced | Applies to |
|---|---|---|
| C01, C03 | Log scrubber + CI test with planted data | All |

*Implementation notes (not part of the rule):* docs/security/audit-logging-design.md section 3 (never-logged list, allow-list logger, scrubber, CI planted-data test TC-10); classification rules in docs/data-inventory-v1.0.md section 4.0. "Full identifiers" includes phone numbers, emails, OTPs and tokens.

## K-DP-05

**Rule:** Aadhaar numbers MUST NOT be stored or logged.

| Why | How it is enforced | Applies to |
|---|---|---|
| C23 | CI pattern scan + schema review | identity, verification |

*Implementation notes (not part of the rule):* docs/privacy/aadhaar-policy.md AAD-01 to AAD-10: never collect, store, log or display (includes VID); reject Verhoeff-valid input; quarantine and delete uploads within 24 h (AAD-05); doctors may not use Aadhaar as ID (AAD-04). Validator listed under `people.prohibited_identifiers` in the country pack.

## K-DP-06

**Rule:** A new personal-data field needs a data-inventory entry (purpose, consent basis, retention, deletion path) in the same PR.

| Why | How it is enforced | Applies to |
|---|---|---|
| C01, C06 | PR checklist + schema diff check | All |

*Implementation notes (not part of the rule):* The row must also give the field's class (PUB, INT, PER, PER-F, PER-K, SH-L, SH-C) from docs/data-inventory-v1.0.md section 4.0, which sets its handling rules.

## K-DP-07

**Rule:** Erasure MUST reach every store: database, search index, caches, object storage, vendors; backups expire or are crypto-shredded. Legal-retention exceptions are restricted, not deleted.

| Why | How it is enforced | Applies to |
|---|---|---|
| C02, C30 | Erasure integration test | All |

*Implementation notes (not part of the rule):* docs/security/data-rights-flows.md section 5 (orchestration, per-module tasks, vendor deletion, restriction under RET-02, and the legal-minimum hold RET-18 before final deletion) and docs/security/backup-and-restore.md sections 2-3 (crypto-shredding, 35-day key-store backups, replay of destruction and erasure logs on every restore). scheduler-core and vendors are included.

## K-DP-08

**Rule:** No vendor receives personal data until it has a processor agreement and is in the vendor register.

| Why | How it is enforced | Applies to |
|---|---|---|
| C06 | PR checklist | adapters |

*Implementation notes (not part of the rule):* Adapter rule A3 (docs/arch/adapter-interfaces.md): the config loader refuses to enable an adapter whose vendor is not in the vendor register with a processor agreement and allowed processing location.

## K-DP-09

**Rule:** Notifications MUST NOT show diagnoses; medicine names only if the patient opted in.

| Why | How it is enforced | Applies to |
|---|---|---|
| C01, C22 | Template lint + tests | notifications |

*Implementation notes (not part of the rule):* Classification handling rules (docs/data-inventory-v1.0.md section 4.0): never a diagnosis or a specialty in notification text; wording resolved at send time (docs/arch/algorithms.md section 3); deep links carry opaque IDs only.

## K-DP-10

**Rule:** Children's profiles need a verified guardian and parental consent before any processing.

| Why | How it is enforced | Applies to |
|---|---|---|
| C04 | Tests | family |

*Implementation notes (not part of the rule):* docs/privacy/children-data-policy.md CH-01 to CH-13. v1.0 is adults only. Child profiles do not launch until the CH-11 confidentiality and safeguarding protocol is approved by the doctor reviewer and the lawyer.

## Proposed changes (each needs an ADR approved by the founder)

- **K-DP-01:** cite C05 and DPDP s.16 as the basis; describe India-only as our stricter policy; add a lawyer-approved exceptions list (push via Google/Apple, no health data).
- **K-DP-03:** add "legal-retention clinical records use separate retention (care) keys, destroyed when the retention period ends", so crypto-shredding never destroys records the law requires.
- **K-DP-05:** widen to "collected, stored, logged or displayed", and include the Aadhaar VID (matches AAD-01).


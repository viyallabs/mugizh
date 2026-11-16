## Story

**S-___:** <story text, short>

- [ ] One story only. Title starts with the story ID (K-DOD-02).
- [ ] Every dependency of the story is Done.

## Contract rules touched (K-DOD-02)

List every K-ID this change touches, and how it is met. Use the full rule text in `contracts/`.

| K-ID | How this change meets it (file and line, or test) |
|---|---|
| | |

## Edge cases and tests

| E-ID | Test (TC-ID from docs/test/edge-case-test-catalogue.md, or new test name) | Passing? |
|---|---|---|
| | | |

## Data

- [ ] No new personal-data field, **or** each new field has a row in `docs/data-inventory-v1.0.md` with purpose, basis, retention, deletion path **and class** (K-DP-06).
- [ ] No real personal data in code, fixtures, tests or screenshots. Synthetic only; Aadhaar-like test values fail the Verhoeff check.
- [ ] No health data, full identifiers, tokens or request bodies in logs, analytics, crash reports or errors (K-DP-04).

## Safety checklist (tick or write N/A)

- [ ] Reads of another person's data go through `records.authorize` and write the audit event first (K-DP-02).
- [ ] Sensitive fields use the encryption library types (K-DP-03).
- [ ] No Aadhaar collected, stored, logged or displayed (K-DP-05).
- [ ] No vendor SDK outside `adapters/`; any new vendor is in the vendor register with a processor agreement (K-ARC-03, K-DP-08).
- [ ] No country-specific values in code (K-GLB-01). User-facing text uses translation keys (K-GLB-02).
- [ ] No flag can turn off consent, the rules engine, the prescribing gate, audit or encryption.
- [ ] Prescribing: the software never suggests or infers a drug or dose (K-CS-03). Rule or clinical-copy changes have the clinical reviewer's approval below (K-CS-07).
- [ ] Payments: hosted checkout only; idempotency keys and durable operation records (K-PAY-01, K-PAY-02).
- [ ] OpenAPI updated; no breaking change in `/v1` (K-ARC-06).
- [ ] **No file under `/contracts` changed** (K-DOD-03), or an approved ADR is linked here: ____

## Definition of Done (K-DOD-01)

- [ ] Acceptance criteria met (list each one with its evidence below).
- [ ] Tests written and passing.
- [ ] Claude contract review run in a fresh session (docs/process/code-review-prompt-template.md). Verdict: ____ (Blockers fixed).
- [ ] SAST, secret and dependency scans pass.
- [ ] Docs updated.
- [ ] Deployed to staging.
- [ ] Tracker updated: Status, Code review, Tests, Owner, Done on, Notes/PR link.

## Approvals needed

- [ ] Clinical reviewer (if clinical rules, clinical copy or safety checklists changed): ____
- [ ] Founder (if refund policy, contracts or a [CONFIRM] item changed): ____

## What I did not do, and what I assumed


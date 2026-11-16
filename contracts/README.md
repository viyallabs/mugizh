# Contracts

**Stories:** S-071 to S-075 · **Status: DRAFT.** Formally in force after the lawyer and doctor review (S-085) and founder approval. **They bind Claude from now on** (CLAUDE.md §1).

These files hold the rules that code must follow. **Each rule's text is copied word for word from the Contracts sheet** in `Resources/healthcare-app-agile-roadmap.xlsx`, which is the master list. The rule IDs are cited on every story and every PR (K-DOD-02).

| File | Rules | Story |
|---|---|---|
| [compliance/data-protection.md](compliance/data-protection.md) | K-DP-01 to K-DP-10 | S-071 |
| [compliance/clinical-safety.md](compliance/clinical-safety.md) | K-CS-01 to K-CS-07 (K-CS-08 proposed) | S-072 (doctor review pending) |
| [compliance/payments.md](compliance/payments.md) | K-PAY-01 to K-PAY-05 | S-073 |
| [compliance/messaging.md](compliance/messaging.md) | K-MSG-01 to K-MSG-03 | S-073 |
| [compliance/security-logging.md](compliance/security-logging.md) | K-SEC-01 to K-SEC-08 | S-074 |
| [architecture/modularity.md](architecture/modularity.md) | K-ARC-01 to K-ARC-03 | S-075 |
| [architecture/api-and-data.md](architecture/api-and-data.md) | K-ARC-04 to K-ARC-06 | S-075 |
| [architecture/country-pack.md](architecture/country-pack.md) | K-GLB-01 to K-GLB-03 | S-075 |
| `CLAUDE.md` §8 (repo root) | K-DOD-01 to K-DOD-04 | S-070 |

## How a contract changes (K-DOD-03)

1. Write an ADR: the rule ID, the old text, the new text, why, and the effect on code and tests.
2. **The founder approves.** Clinical rules also need the clinical reviewer; legal wording needs the lawyer.
3. Update the Contracts sheet first, then the file (the text must match), in one PR that cites the ADR.
4. **Claude never edits a rule to make code pass.** CODEOWNERS requires the founder's approval for any change under `/contracts`.
5. **Implementation notes** under each rule are not part of the rule. They point to the design and may be updated with it.

**Proposed changes waiting for ADRs** are listed at the end of each file. Summary:
- K-SEC-01: 180 days → 1 year.
- K-SEC-03: add the electronic signature.
- K-DP-01: basis and exceptions.
- K-DP-03: retention keys.
- K-DP-05: widen to "collect or display" and include the VID.
- K-PAY-02: durable operation records.
- K-ARC-03: more provider types.
- New K-CS-08: no paid ranking.

## How they are enforced

The "How it is enforced" column of each rule becomes a CI check, a test or a review step:
- **S-092:** automated contract checks.
- **S-076:** the merge-request checklist and the review prompt.
- **S-085:** legal and clinical review.

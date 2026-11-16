# Contract: Country pack (K-GLB)

**Story:** S-075 · **Status: DRAFT, binding on Claude now; formally in force after the lawyer and doctor review (S-085) and founder approval.** · **Source:** Contracts sheet, generated 2026-11-15

**Rules for this file (K-DOD-03):**

- The **Rule** text below is copied word for word from the Contracts sheet.
- **It changes only through an ADR the founder approves.** Claude must never edit this file to make code pass.
- **Implementation notes** are not part of the rule. They point to the design that meets it, and may be updated with the design.

## K-GLB-01

**Rule:** No country-specific constants in code (laws, drug lists, emergency numbers, currency, ID systems, providers); they live in country packs.

| Why | How it is enforced | Applies to |
|---|---|---|
| Global launch with small changes | Lint + config tests | All |

*Implementation notes (not part of the rule):* docs/arch/country-pack-model.md: manifest, loader rules (validate + checksum, fail closed, approved-only in production), CI check for country literals, synthetic test pack XX.

## K-GLB-02

**Rule:** All user-facing text uses translation keys; dates, numbers and currency are locale-formatted.

| Why | How it is enforced | Applies to |
|---|---|---|
| Languages | Lint | apps |

*Implementation notes (not part of the rule):* Launch locale en-IN only; hi-IN and ta-IN prepared (S-100). Clinical and legal texts are translated for meaning and versioned separately.

## K-GLB-03

**Rule:** Each country pack declares its data-residency region; data for that country stays there.

| Why | How it is enforced | Applies to |
|---|---|---|
| Data residency | IaC + config tests | infra |

*Implementation notes (not part of the rule):* Country pack `residency` section; organisation policy denies resources outside the declared regions (infrastructure doc R1).


# Contract: Modularity (K-ARC-01 to K-ARC-03)

**Story:** S-075 · **Status: DRAFT, binding on Claude now; formally in force after the lawyer and doctor review (S-085) and founder approval.** · **Source:** Contracts sheet, generated 2026-11-15

**Rules for this file (K-DOD-03):**

- The **Rule** text below is copied word for word from the Contracts sheet.
- **It changes only through an ADR the founder approves.** Claude must never edit this file to make code pass.
- **Implementation notes** are not part of the rule. They point to the design that meets it, and may be updated with the design.

## K-ARC-01

**Rule:** Modular monolith: modules talk only through public interfaces or events; no module reads another module's tables.

| Why | How it is enforced | Applies to |
|---|---|---|
| Modularity / extraction | Lint (import boundaries) + DB grants | All |

*Implementation notes (not part of the rule):* docs/arch/module-boundaries-and-domain-model.md B1-B10. The only shared table is `audit_outbox` (INSERT-only for every module role).

## K-ARC-02

**Rule:** scheduler-core never imports health modules and never stores health data; it holds only references.

| Why | How it is enforced | Applies to |
|---|---|---|
| F10 standalone | Lint + contract test | scheduler-core |

*Implementation notes (not part of the rule):* Opaque subject and activity references only; `typeKey` stays a coarse category; OpenAPI lint rule `no-health-fields-in-scheduler` with fixtures (docs/arch/lint-fixtures).

## K-ARC-03

**Rule:** Every external provider (payments, SMS, WhatsApp, video, maps, OCR, verification) is used only through an adapter interface.

| Why | How it is enforced | Applies to |
|---|---|---|
| Swap vendors; global | Lint (no vendor SDK imports outside adapters) | All |

*Implementation notes (not part of the rule):* docs/arch/adapter-interfaces.md rules A1-A12. Also covers push, email, geocoding, electronic signature and age verification adapters.

## Proposed changes (each needs an ADR approved by the founder)

- **K-ARC-03:** add push, email, geocoding, electronic signature and age verification to the list of providers.


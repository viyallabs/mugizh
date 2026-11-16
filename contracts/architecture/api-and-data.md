# Contract: API and data (K-ARC-04 to K-ARC-06)

**Story:** S-075 · **Status: DRAFT, binding on Claude now; formally in force after the lawyer and doctor review (S-085) and founder approval.** · **Source:** Contracts sheet, generated 2026-11-15

**Rules for this file (K-DOD-03):**

- The **Rule** text below is copied word for word from the Contracts sheet.
- **It changes only through an ADR the founder approves.** Claude must never edit this file to make code pass.
- **Implementation notes** are not part of the rule. They point to the design that meets it, and may be updated with the design.

## K-ARC-04

**Rule:** Times stored in UTC with an IANA time zone; recurrence uses RFC 5545 RRULE.

| Why | How it is enforced | Applies to |
|---|---|---|
| Correct reminders | Lint + tests | All |

*Implementation notes (not part of the rule):* Recurrence in scheduler-core uses RRULE; month-end clamping for calendar-month windows (telemedicine follow-up).

## K-ARC-05

**Rule:** Clinical data maps to FHIR R4 resources at the boundary.

| Why | How it is enforced | Applies to |
|---|---|---|
| ABDM, global interoperability | Mapping tests | clinical, records |

*Implementation notes (not part of the rule):* Mapping designed in S-083 (docs/arch/fhir-r4-mapping.md); used at the boundary from v2.0 (ABDM).

## K-ARC-06

**Rule:** APIs are versioned with OpenAPI as the source of truth; no breaking changes within a version.

| Why | How it is enforced | Applies to |
|---|---|---|
| Mobile app compatibility | OpenAPI diff check in CI | All APIs |

*Implementation notes (not part of the rule):* docs/arch/api-standards.md and the Spectral ruleset docs/arch/openapi-lint.spectral.yaml (not verified until CI runs the fixtures, S-092).


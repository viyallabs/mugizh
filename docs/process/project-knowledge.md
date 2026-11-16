# Project knowledge: what Claude reads, and where it lives

Give Claude this list at the start of a session. Items marked **(not yet)** do not exist yet;
the phase that creates them is in brackets.

## Always read
| Item | Location | Created in |
|---|---|---|
| Rules for Claude (references contracts, DoD, process) | `CLAUDE.md` | S-070 (P08) |
| Process and templates | `docs/process/` | S-006 (this story) |
| Product vision | `docs/product-vision.md` | S-002 |
| Personas | `docs/personas.md` | S-003 |
| v1.0 requirements | `docs/requirements-v1.0.md` | S-004 |
| Metrics and event list | `docs/metrics-and-events.md` | S-005 |
| Feature roadmap | `Resources/feature-roadmap.md` | Provided |
| Tracker | `Resources/healthcare-app-agile-roadmap.xlsx` | Provided |

## Read per story (listed on the story)
| Item | Location | Created in |
|---|---|---|
| Contracts named in "Contract refs" (K-IDs) | `contracts/` (DRAFT since 2026-11-15, word for word from the Contracts sheet; formal review in S-085) | S-071 to S-075 |
| Edge cases named in "Edge case refs" (E-IDs) | Edge Cases sheet; later `docs/` export | P03 / P06 |
| Compliance items (C-IDs) | Compliance sheet | P01 |
| Architecture decisions (ADRs) | Architecture sheet (column J: S-035 recommendation); `docs/arch/adr-decisions.md` | P05 |
| Architecture overview, module boundaries, adapter interfaces, API standards | `docs/arch/` | S-034 to S-038 (P05) |
| Designs for UI stories | Screens sheet / design files | P09 |

## Contracts (since 2026-11-15)
`/contracts` holds DRAFT contract files whose rule text matches the **Contracts sheet** word for
word (S-071 to S-075). They bind Claude now, and they are read-only for Claude (K-DOD-03).
The sheet stays the master list; formal approval comes in S-085.

## What Claude must not assume
- `CLAUDE.md` and the `/contracts` files exist (drafts). Never invent a rule that is not in them; ask instead.
- Items marked "Lawyer check needed" in the Compliance sheet are not legal advice.
- Architecture choices are proposals (status Proposed) until accepted in P05.

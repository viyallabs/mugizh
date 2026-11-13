# Project knowledge: what Claude reads, and where it lives

Give Claude this list at the start of a session. Items marked **(not yet)** do not exist yet;
the phase that creates them is in brackets.

## Always read
| Item | Location | Created in |
|---|---|---|
| Rules for Claude (references contracts, DoD, process) | `CLAUDE.md` **(not yet)** | S-070 (P08) |
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
| Contracts named in "Contract refs" (K-IDs) | `contracts/` **(not yet)**; until P08, use the Contracts sheet | P08 |
| Edge cases named in "Edge case refs" (E-IDs) | Edge Cases sheet; later `docs/` export | P03 / P06 |
| Compliance items (C-IDs) | Compliance sheet | P01 |
| Architecture decisions (ADRs) | Architecture sheet; later `docs/adr/` | P05 |
| Designs for UI stories | Screens sheet / design files | P09 |

## Until contracts exist (P00 to P07)
`/contracts` is created in P08. Until then Claude uses the **Contracts sheet** rows for the K-IDs
on the story as if they were the contract files. They are read-only for Claude.

## What Claude must not assume
- No contract file or `CLAUDE.md` exists before P08/S-070. Say so rather than inventing one.
- Items marked "Lawyer check needed" in the Compliance sheet are not legal advice.
- Architecture choices are proposals (status Proposed) until accepted in P05.

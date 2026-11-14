# Backlog check (S-033)

**Phase P03 · Epic EP05 · Depends on:** S-030 · **Date:** 2026-11-15 · **Status:** Done (acceptance criterion: no orphan stories; dependency order checked)

**Who this is for:** the founder and anyone planning sprints from the Backlog sheet.

## 1. What was checked (automatically, all 275 stories, S-001 to S-275)

| Check | Result |
|---|---|
| Every story has a feature (column K) | **Pass**: 275/275 |
| Feature IDs are valid (F-IDs from the roadmap, or All / Shared / Supporting / Global / Marketing / version) | **Pass** |
| Every story has a version (E) and a suggested sprint (F) | **Pass** |
| Every dependency (O) exists | **Pass** |
| No story depends on a story planned for a later sprint | **Pass** |
| No dependency cycles | **Pass** |
| No story depends on a Won't (this version) story | **Pass** |
| No story is planned before the version its feature ships in | **Pass** |
| Edge-case links: every story named in the Edge Cases sheet exists; every Critical or High edge case has a linked story; every E-ID cited in the Backlog exists | **Pass** (81 edge cases) |
| Orphans: no dependencies and nothing depends on them | **1: S-001** (tracker set-up). Expected; no action. |

## 2. Logical gaps fixed (dependencies added)

These were not errors in the sheet's structure, but in each case the build story needs a planning output that it did not list. Every added dependency is scheduled in an earlier sprint, so the order is unchanged.

| Story | Added dependency | Why |
|---|---|---|
| S-025 v0 concierge loop | S-020 | CERT-In point of contact and the pre-v0 checklist (C07 due at v0) |
| S-047 landing page | S-020 | The waitlist page is a live system under CERT-In |
| S-048 marketing guide | S-022 | Claims register and banned words (MD-01) |
| S-104 phone OTP | S-102 | OTP SMS needs the DLT template SMS-01 |
| S-133 refund engine | S-019 | Refund policy values RF01–RF09 |
| S-138 prescribing rules engine | S-029 | Doctor sign-off of clinical rules (K-CS-07) |
| S-142 grievance queue | S-023 | 7-day, 36 h, 2 h and 3 h timers |
| S-162 search ranking | S-018 | No paid ranking; published factors (CD-05) |
| S-187 store submission | S-023 | Terms and policies must be live for listing |
| S-196 symptom search | S-022 | The F8 option decides the design and possible licensing |

## 3. Flagged for the founder (not changed)

1. **S-219 overlaps S-133.** The v1.0 refund engine already refunds doctor no-shows automatically. S-219 (v1.2) should cover only split-settlement refunds and the dispute workflow. A note has been added.
2. **Log retention text:** S-059 ("180+ days") and S-095 ("180-day log retention") contradict RET-07 (1 year). Change them when the founder approves 1 year (S-020 decision 2).
3. **Releases sheet:** v1.0 still lists F11-a (WhatsApp), which is out of v1.0 while on hold (S-030). Update it after the scope sign-off.
4. **S-002 to S-006 still show "Backlog"**, although their outputs are in the repo (product-vision.md, personas.md, requirements-v1.0.md, metrics-and-events.md, docs/process/). The founder should mark them Done or In Testing after reviewing them.
5. **Stories blocked on people:**
   - S-029 needs a doctor.
   - S-009 needs a lawyer; it feeds 10 or more drafts.
   - S-014 needs doctor recruitment.
   - S-016 needs a name.
   - S-007 needs the company set up; it blocks DLT registration (S-102) and GST.

## 4. How to re-run

The check script is in the working scratchpad, not in the repo. Re-run it after big backlog edits, or turn it into a CI check on the exported sheet once the repo exists (S-092).

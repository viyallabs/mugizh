# Way of working with Claude (S-006)

**Phase P00 · Epic EP01 · Depends on:** S-004 · **Contract refs:** K-DOD-01, K-DOD-02

This folder defines how every story is built and reviewed with Claude. The tracker
(`Resources/healthcare-app-agile-roadmap.xlsx`) is the source of truth for stories; these
files tell Claude and the team what to read and what to do.

| File | Purpose |
|---|---|
| [project-knowledge.md](project-knowledge.md) | What Claude must read at the start of a session, and where each item lives |
| [story-prompt-template.md](story-prompt-template.md) | The prompt used to start every story |
| [code-review-prompt-template.md](code-review-prompt-template.md) | The prompt used to review every PR against the contracts |
| [dry-run.md](dry-run.md) | First story run through both templates, with lessons |
| [release-and-versioning.md](release-and-versioning.md) | Versions, release trains, store releases, feature flags, rollback (S-032) |

## The loop (from the Start Here sheet)
1. Copy the story text, acceptance criteria, Contract refs and Edge case refs from the Backlog.
2. Start the session with `CLAUDE.md` and the contract files listed on the story (K-DOD-04).
3. Ask Claude to list which contract rules apply, then write the tests first.
4. Build the story. One PR per story.
5. Run the code-review prompt. Claude checks the diff against every listed contract rule and edge case.
6. CI must pass (tests, contract lint, security scans). Then In Testing, then Done.

Statuses: Backlog → Ready → In Progress → In Code Review → In Testing → Done
(Blocked at any point; Won't Do removes a story from progress totals).

## Non-code stories
Research, compliance, design and marketing stories show N/A for code review and tests. They are
Done when their acceptance criteria are met and the output is saved in `/docs`. Use the
"non-code" variant in the story template.

## Rules that never bend
- Claude never edits a file in `/contracts` (K-DOD-03). Contract changes go through an ADR the founder approves.
- A story is Done only when K-DOD-01 is met (acceptance criteria, passing tests, contract review approved, scans pass, docs updated, deployed to staging, tracker columns updated).
- Every PR names its story ID and lists the contract rule IDs it touches (K-DOD-02).
- Claude states which contract rules apply and how it will meet each **before** writing code.
- If a contract and a story conflict, stop and ask. Do not resolve it silently.

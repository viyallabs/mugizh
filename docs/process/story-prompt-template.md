# Story prompt template

Fill every `[ ]` from the story's row in the Backlog. Paste the whole block to Claude.

## Code story
```
Story [S-ID]: [story text].
Role: [role]. Phase: [P-ID]. Version: [version]. Depends on: [S-IDs, all Done?].

Acceptance criteria:
[paste from Backlog]

Compliance refs: [C-IDs]
Contracts: [K-IDs]
Edge cases: [E-IDs]

Read first: CLAUDE.md, the contract files above, and docs/process/project-knowledge.md.

Do this in order:
1. State which contract rules apply and how you will meet each. Flag any conflict between the
   story and a contract, and stop for my decision if there is one.
2. List which edge cases apply and which test covers each.
3. Write failing tests first.
4. Then write the code, as one PR for this story only.
5. Do not edit any file in /contracts.
6. Finish with: the PR checklist (story ID, contract IDs touched, checklist ticked per K-DOD-02),
   what you did not do, and anything you assumed.
```

## Non-code story (research, compliance, design, marketing, planning)
```
Story [S-ID]: [story text].
Acceptance criteria:
[paste from Backlog]

Compliance refs: [C-IDs]   Contracts: [K-IDs]   Edge cases: [E-IDs]
Read first: docs/process/project-knowledge.md and the docs this story depends on.

Do this in order:
1. Say who the output is for and what decision it supports.
2. Draft the output into /docs/[file].md.
3. Mark every statement that is your inference, not sourced, as [CONFIRM] or [VALIDATE].
4. Check the draft against each acceptance criterion, one by one, and report which are met.
5. List the open decisions I must make. Do not decide them for me.
Compliance outputs are not legal advice; mark items that need a lawyer.
```

## Checklist before sending (Definition of Ready)
- [ ] Story and acceptance criteria are clear.
- [ ] Dependencies are Done.
- [ ] Contract and edge-case refs are listed.
- [ ] Designs are signed off (UI stories).

## After the story
- [ ] Update Status, Code review, Tests, Owner, Started, Done on, Notes/PR link in the Backlog.
- [ ] Output saved in `/docs` (non-code stories).

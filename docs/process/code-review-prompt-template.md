# Code-review prompt template

Run in a **fresh** Claude session (or sub-agent) so the review is not anchored on the author's
reasoning. Give it the diff, not the author's conclusion. This is the "Claude code review against
the contracts" required by K-DOD-01.

```
Review this diff for Story [S-ID]: [story text].
Acceptance criteria: [paste]
Contracts that apply: [K-IDs]  (read each rule fully)
Edge cases that apply: [E-IDs]
Compliance refs: [C-IDs]

Diff:
[paste or point to the PR]

Do this:
1. For each listed contract rule, answer MET / NOT MET / NOT APPLICABLE with the file and line
   that proves it. "Looks fine" is not an answer.
2. For each listed edge case, point to the test that covers it, or say there is none.
3. Check the PR itself: names the story ID; lists the contract IDs; one story only;
   no edit to /contracts (K-DOD-03).
4. Look for the usual regulated-health traps even if no rule was listed: health data or
   identifiers in logs/analytics/errors (K-DP-04), Aadhaar stored (K-DP-05), a read of another
   person's record without a consent check (K-DP-02), card data (K-PAY-01), a dose guessed or
   inferred (K-CS-03), a diagnosis shown in a notification (K-DP-09), hard-coded country values
   (K-GLB-01), secrets in code. Also check:
   - every non-patient read goes through `records.authorize` and writes the audit event first;
   - new personal-data fields have a data-inventory row **with a class** (K-DP-06);
   - no vendor SDK outside `adapters/` (K-ARC-03);
   - no health field reaches scheduler-core (K-ARC-02);
   - no feature flag can switch off consent, the rules engine, the prescribing gate, audit or
     encryption;
   - money operations use durable operation keys (K-PAY-02);
   - the TC tests from docs/test/edge-case-test-catalogue.md are present by ID.
   Read the full rule text in `contracts/` (CLAUDE.md §1).
5. Report findings ranked: Blocker / Should fix / Nit. For each: file:line, what is wrong,
   a concrete failing case, and the fix.
6. End with a verdict: APPROVE, APPROVE WITH CHANGES, or REJECT, and say why.
Do not fix the code. Do not edit /contracts. Say what you could not check.
```

## What the human does with the result
- Blocker: not merged until fixed and re-reviewed.
- Should fix: fix, or record why not in Notes/PR link.
- Update the Backlog "Code review" column: Approved / Changes requested / N/A.
- A safety-related rule or clinical copy change also needs a registered-doctor review (K-CS-07).

## Limits
Claude's review does not replace the SAST, secret and dependency scans, the tests, or the
independent VAPT in P12. They are all required (K-DOD-01).

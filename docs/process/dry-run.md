# Dry run of the templates (S-006)

**Acceptance criterion:** templates saved in `/docs/process`; first story run through them as a
dry run.

**Honest scope note.** No code exists yet (first code is P10), so this dry run uses a finished
non-code story, **S-005 (metrics and analytics events)**, because it has a real rule to check
(K-DP-04). The code-story template and the contract-file loading path are **not** proven yet;
re-run this on the first real code story in P10 and update this file.

## Run 1: non-code story template on S-005
- Audience and decision stated: founder, to approve the v1.0 success definition and analytics scope.
- Output saved: `docs/metrics-and-events.md`.
- Inferences marked `[CONFIRM]`: yes (north star, active-day definition, retention period, vendor).
- Acceptance criteria checked one by one: "metric definitions written" met; "no event carries
  health data or full identifiers" **met after the fixes below**.
- Open decisions listed for the founder: yes (5 items).

## Run 2: review prompt on the S-005 output (rule K-DP-04, contract refs C01)
Each applicable rule was checked against the document, not code. Done in the authoring session,
so treat it as a self-review.

| Rule | Verdict | Evidence |
|---|---|---|
| K-DP-04 no health data in analytics | **NOT MET at first pass** | `prescription_signed` carried `has_injection` and `has_test`; `reminder_*` events carried type `injection`/`test`. A pseudonymous `user_key` tied to "has injections" is health information by inference. |
| K-DP-05 no Aadhaar | MET | Section 5 forbids Aadhaar, ABHA and registration numbers. |
| K-DP-06 data inventory entry | MET (planned) | Section 6 requires an entry for `user_key`; to be created in P06. |
| K-DP-07 erasure reaches analytics | MET (planned) | Section 6. Needs an erasure test when built. |
| K-DP-08 vendor needs processor agreement | MET (planned) | Section 6, vendor register. |
| K-DP-01 India hosting | MET (planned) | Section 6, prefer India-hosted tool. |

### Findings and what was done
| Rank | Finding | Action |
|---|---|---|
| Should fix | `has_injection` and `has_test` on `prescription_signed` reveal treatment type per user | **Removed** from the event. |
| Should fix | Reminder `type` of `injection`/`test` per user reveals treatment type | **Changed** to coarse `medication` / `test` / `appointment` (injection counted under `medication`). North-star reporting is aggregate-only. |
| Nit | `test` type still hints at a lab test being due | Left; raise with the lawyer (D-1). |
| Nit | `prescription_blocked_by_rule.rule_id` could imply a patient tried to get a restricted drug | Event is doctor-side and has no drug name; left; confirm in D-1. |

Verdict: **APPROVE WITH CHANGES**; changes applied.

## Lessons for the templates
1. **The review step found a real issue** in the author's own draft. Caveat: this dry run was done
   in the **same session** that wrote the draft, so it was not an independent review. The
   template says to use a fresh session or sub-agent; do that on the first real code story, and
   repeat this review of S-005 in a fresh session if you want an independent check.
2. **"Inference" counts as health data.** Added to the "usual traps" idea: a category per
   user can leak a condition even without a name. (Already covered by the review prompt item 4;
   the founder should keep this in mind when approving events.)
3. **Contract files are not there yet.** Until P08 the reviewer used the Contracts sheet rows.
   That worked, but contract text is truncated in some cells, so read the full cell.
4. **Templates need a code story to be proven.** Re-run in P10 with the Hello-world story and
   confirm that CI, the PR checklist and the review column all line up.

## Open decisions
- **D-1:** Ask the lawyer (P01) whether coarse reminder type (`medication`/`test`/`appointment`)
  per pseudonymous user is acceptable analytics under DPDP, or whether it should be aggregate-only
  with no per-user storage.

# Rules engine: items for the doctor reviewer (S-138, 2026-11-17)

**Who this is for:** the registered doctor who reviews the telemedicine prescribing rules (S-029, K-CS-07), and the founder.
**Why:** building the rules engine from `docs/clinical/telemedicine-rules-spec.md` and `config/country-packs/IN/rules/telemedicine-rules.yaml` (v1.1.0-draft) found places where the two disagree or say nothing. Claude did **not** change either file (clinical files need your approval). The engine takes the stricter reading in every case and blocks when unsure.

## 1. Disagreements and gaps

| # | What we found | What the engine does now | What we need from you |
|---|---|---|---|
| 1 | Reason codes: the build task named `RX_UNCLASSIFIED_DRUG` and `RX_STRUCTURE_INCOMPLETE`; the spec and YAML define `RX_NOT_CLASSIFIED` (PL02) and `RX_DOSE_INCOMPLETE` (P07) | Uses the spec's codes. Adds engine-only codes for cases the spec does not cover: `RX_NO_RULE_MATCHED`, `RX_INPUT_INVALID`, `RX_UNKNOWN_DRUG`, `RX_RULESET_MISMATCH`, `RX_INTERNAL_ERROR` (all block) | Approve the extra codes and add their wording (translation keys) to the YAML `reasons` table |
| 2 | P06 wording `rx.block.follow_up` (YAML line 202) says a follow-up needs "an in-person visit with you in the last 6 months"; spec test T08 says a visit 200 days ago is "treated as first" | Follows T08: an expired window, another doctor, no prior visit or unknown evidence all mean **first consult** (stricter rules), not a P06 block. P06 blocks only when the window is met but the doctor's attestations are missing (T16) | Confirm T08 is right and fix the P06 message wording |
| 3 | No regulatory row covers refilling a **List B** drug in a remote follow-up (R08 is "add-on" only) | Blocks with `RX_NO_RULE_MATCHED` | Is refilling a List B add-on on a later remote follow-up meant to be blocked? |
| 4 | No row covers a **List A** item marked "refill" in a **first** video consult (R04 requires `new_drug`) | Blocks with `RX_NO_RULE_MATCHED` | Confirm, or add a row |
| 5 | YAML `v1_enabled_modes` excludes text consults, but spec T09 uses text | The engine accepts every mode listed in `consult.modes`; which modes are switched on per release is decided by the caller | Confirm text consults stay off in v1.0 |
| 6 | YAML `categories.prohibited.match` mentions schedule and NDPS flags; the drug master stores one "prohibited" membership plus a source reference | Prohibited means prohibited; no override | Confirm one "prohibited" flag (with its legal source) is enough |

## 2. Assumptions the engine makes

- The doctor chooses each item's context (new drug, refill, add-on); the platform supplies the prescribing history. A "refill" the history does not support is treated as a **new drug** (stricter).
- P04 and P06 apply to remote consults only; P10 (patient in India) applies to remote consults only; P01, P02, P03 and P05 apply to every mode.
- Drug classification file format (the pack file is empty today, so **every drug is blocked** until you fill it): each entry has `ref`, generic and brand names, strength, form, route, list memberships, amendment exception, condition tags and a legal `source`.
- Import refuses a drug that is both prohibited and on a permitted list without an amendment exception, duplicates, entries without a source, and a List B drug without a condition tag. If the table has any entries, the three amendment drugs (phenobarbitone, clobazam, clonazepam) must be present; they are List A under the April 2020 amendment and are blocked by platform rule PL03 until you decide (Q11).
- Speed: about 1.4 µs per prescription check (limit 50 ms, T19).

## 3. Status

T01–T31 pass except T28 (signature check on a changed byte), which belongs to the electronic-signature story (S-150, S-151, TC-60). The rule file is a **draft**, so production refuses to load it (country-pack loader) until you approve it.

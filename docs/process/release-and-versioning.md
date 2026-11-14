# Release and versioning policy (S-032)

**Phase P03 · Epic EP05 · Depends on:** S-030 ([scope-v1.0.md](../scope-v1.0.md)) · **Related:** Releases sheet (six gates), ADR-23 (feature flags), K-ARC-06 (API versions), K-DOD-01, S-094 (deployment), S-099 (flags), S-186 (readiness), S-187 (stores) · **Date:** 2026-11-15 · **Status:** Done (acceptance criterion: policy in /docs/process). The founder can change any value.

**Who this is for:** everyone who ships code or releases apps.

## 1. Three kinds of version (don't mix them up)

| Kind | Format | Example | Who sets it |
|---|---|---|---|
| **Product version**: what the roadmap and Releases sheet call v1.0, v1.1 and so on | `vMAJOR.MINOR`, tied to one hypothesis | v1.0 "Consult → Prescription → Reminders" | Founder, at the release gate |
| **App and service build**: what ships to the stores and servers | Semantic version `MAJOR.MINOR.PATCH` + build number | Patient app `1.0.3 (142)`, api `1.4.0` | Release pipeline (S-094) |
| **API version**: what mobile apps call | `/v1/…` path, with OpenAPI as the source of truth | `/v1/bookings` | ADR plus founder for a new major |

- **Product v1.0 starts app 1.0.0.** Each later product version bumps the app's MINOR (v1.1 → app 1.1.0). PATCH is for fixes.
- A service's version moves on its own (api, scheduler-core). It is recorded in the release notes, not tied to the product version.
- **API:** no breaking change inside `/v1` (K-ARC-06; an OpenAPI diff check in CI). A breaking change needs `/v2`, run side by side with `/v1` for **at least 6 months**, or until under 2% of active installs use `/v1`, whichever is later. **Minimum supported app version:** the server can force an update screen only for security or legal reasons.
- Configuration has its own version: country packs and rule files carry their own versions (for example `IN-REFUND 1.0.0`, `telemedicine-prescribing 1.0.0`). Each booking or prescription stores the version it used (K-PAY-05, K-CS-02).

## 2. Release trains

| Train | Cadence | What goes on it |
|---|---|---|
| **Server (api, scheduler-core, web portals)** | Continuous to staging on every merge. **To production twice a week** (Tue and Thu, 11:00 IST), behind flags. No production releases Fri–Sun or on public holidays, except hotfixes. | Everything merged and passing |
| **Mobile apps** | **Every 2 weeks** at the end of a sprint, with a staged rollout | The sprint's mobile work; incomplete features hidden by flags |
| **Hotfix** | Any time, with founder or on-call approval | Security fixes, data-safety bugs, payment errors, prescribing-rule errors |
| **Rule and config changes** | With the server train, through a reviewed pull request | Country-pack and rule files. **Clinical rule changes need the doctor reviewer's approval in the PR** (K-CS-07); tax values need the CA (C20). |

## 3. Path to production (every server release)

1. Merge to `main`: CI passes (tests, contract lint, SAST, secrets, dependencies; K-DOD-01).
2. Automatic deploy to **staging**, plus database migrations (expand-and-contract only: never drop or rename a column in the same release that stops using it).
3. Automated end-to-end suite on staging (S-174), and a manual check of any payment or prescribing change.
4. **Manual promotion to production** by an approved person. Promotion is logged.
5. Watch for 30 minutes: errors, latency, payment success rate, reminder dispatch lag. Roll back on any alert threshold.

## 4. Mobile store releases

| Step | Rule |
|---|---|
| Build | Signed in CI. Version and build number set by the pipeline. No debug flags. |
| Internal testing | Play internal track / TestFlight: the team, plus a smoke test on a low-end Android phone |
| Beta (closed beta, S-030) | Play closed track / TestFlight external: invited doctors and patients |
| Production | **Staged rollout: Android 5% → 20% → 50% → 100%** over about 5 days, paused automatically if crash-free sessions drop below 99.5%. iOS phased release over 7 days. |
| Store review | Allow 1–3 days. Health declarations and privacy labels kept in sync with the privacy notice and data inventory (S-187). |
| Release notes | Plain English. Mention privacy changes and any new consent. |

## 5. Feature flags (ADR-23; S-099)

- **Every user-visible feature ships dark first**, behind a flag, and is turned on gradually: internal → beta → 10% → 100%, or by clinic or city.
- **Kill switches** for: online consultations, payments (switch to "pay at clinic"), SMS sending, reminders dispatch, uploads, and each external adapter.
- **Flags never bypass compliance:** a flag cannot turn off consent checks, the prescribing rules engine, the prescribing gate, audit logging or encryption. These are not flaggable, and a contract lint check enforces it.
- Flags have an owner and an end date. A flag at 100% for 30 days is removed in the next sprint.
- Flag changes in production are audit-logged.

## 6. Rollback

| What | How | Target time |
|---|---|---|
| Server | Re-deploy the previous image. Migrations are backward-compatible, so no database rollback is needed. | **Under 15 minutes** |
| Feature | Turn the flag or kill switch off | Under 2 minutes |
| Rule or config file | Revert the PR. Bookings and prescriptions keep the version they were made with. | Under 15 minutes |
| Mobile app | Halt the staged rollout. Ship a fixed build as a hotfix. Use the server-side flag to disable the broken feature in the meantime. Apps cannot be rolled back in stores. | Halt at once; fix in 1–2 days |
| Data | Restore from backup only through the DR runbook (S-185). Restores must re-apply erasures (deletion keys). | Per recovery objectives (P05) |

## 7. Product release gates (from the Releases sheet)

A **product version** ships only when all six gates are Yes:
1. Features complete.
2. Regression pass.
3. Security test pass.
4. Compliance sign-off.
5. Performance targets met.
6. Store approval.

The founder records the decision and the date in the Releases sheet. The closed beta (S-030) needs gates 2–4 and 6. Gate 1 is measured against the beta cut.

## 8. Freeze and support windows

- **Code freeze** for a product version: 1 week before the planned release. Only fixes after that.
- **Supported app versions:** the current version and the one before it. Older versions get an update prompt, and a forced update only for security or legal reasons (section 1).
- **Incidents during a rollout** follow the [incident response runbook](../security/incident-response-runbook.md) (S-020).

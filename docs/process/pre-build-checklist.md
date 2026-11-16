# Pre-build checklist: what must be true before code starts

**Date:** 2026-11-16 (updated the same day after the founder's tech-stack decisions) · **Based on:** repo at commit `cbca9ff` ("planning completed, tech stack pending"), the Backlog, Architecture, Phases and Contracts sheets, `CLAUDE.md`, and `docs/`.
**Stack decided (2026-11-16):** Go backend · React Native mobile (two apps) · Next.js web · PostgreSQL + PostGIS · **dev on Oracle Cloud (OCI), staging, production and DR on AWS India regions.** Details and my open proposals: [adr-decisions.md](../arch/adr-decisions.md) §6.
**Who this is for:** the founder, to decide what to do first; and the team, to know what is blocking Sprint 0 (P10).
**How to use it:** tick a box only when the thing is true, not when it is drafted. Items marked **[YOU]** can only be done by the founder; **[ME]** can be done by Claude on request; **[3RD]** needs an outside party and has lead time.

---

## 0. Where the project stands (verified from the repo and tracker)

| Area | State |
|---|---|
| Planning documents | 93 files in the repo: vision, requirements, scope, metrics, architecture (overview, modules, adapters, API standards, country pack, FHIR, infra), security (12 designs), privacy, legal, clinical rules, payments, design IA, test catalogue (TC-01 to TC-66) |
| `CLAUDE.md`, `/contracts` (8 files), PR template, `CODEOWNERS` | Exist. Contracts are **DRAFT** (formal review is S-085) but bind Claude |
| Code | **None.** No source files, no `.gitignore`, `README.md` is still the GitLab template |
| Tech stack | **Core stack decided 2026-11-16** (Go, React Native, Next.js, PostgreSQL + PostGIS; OCI for dev, AWS for staging, production and DR). **Still open:** Go framework, queue, monorepo, test tools, CI tool, identity service (ADR-03, 07, 10, 11, 24, 25: my proposals await your confirmation). The Architecture sheet still shows all ADR rows as "Proposed", and **`CLAUDE.md` §4 still says the stack is undecided** |
| Sprint 0 (P10) | **13 of 13 stories in Backlog** (S-090 to S-103) |
| Stories awaiting founder sign-off | About 40 are "In Testing" (drafted, not signed): most of P01, P04, P05, P06 |
| Not started | P02 concierge test (S-014, S-024, S-025, S-082), vendor spikes (S-039 to S-042), prototypes (S-066 to S-068), most of P09 design, S-009 lawyer, S-007 company basics |
| Blocked | S-029 (doctor review of clinical edge cases) |

**Tracker hygiene:** the sheet still shows P00 stories (S-001 to S-007) as Backlog although the documents exist, and Start Here still says "[Product Name]". Update statuses so the Dashboard is honest.

---

## 1. Three gates

| Gate | Means | Unlocks |
|---|---|---|
| **Gate A: Stack and decisions** | Section 2 done (**core stack ticked; the open items remain**) | Infra design (S-055) and everything below |
| **Gate B: Ready for Sprint 0** | Gate A + sections 3 and 5 | S-090 to S-103 (foundation: repo, CI, infra, audit, consent, encryption, flags, i18n) |
| **Gate C: Ready for MVP features** | Gate B + sections 4, 6, 7, 8 | P11 (S-104 onward) |

You can start **Sprint 0** before every item in sections 4, 6, 7 and 8 is done. You should **not** start P11 before them. Reasons are given per item.

---

## 2. Gate A: decisions only you can make **[YOU]**

Source: [adr-decisions.md](../arch/adr-decisions.md) §5 and §6. **CLAUDE.md §4 forbids Claude from choosing a language, framework or cloud beyond what is recorded**, so code stories wait until the open items below are confirmed and `CLAUDE.md` §4 is updated.

**Decisions recorded 2026-11-16**

| ADR | Decision | Note |
|---|---|---|
| ADR-02 Language | Go (backend, scheduler-core); TypeScript for clients | Changed from "TypeScript end to end" |
| ADR-04 Mobile | React Native, two apps | Expo vs bare: not stated |
| ADR-05 Web | React with Next.js | Accepted |
| ADR-06 Database | PostgreSQL + PostGIS | Accepted; Oracle Database **not** used by the app (my reading, confirm) |
| ADR-09 Cloud | OCI for dev; AWS for staging, production and DR | Changed to two clouds; see rules in adr-decisions §6.3 |

- [ ] **Team skills.** Who will build and maintain this, and what do they know? (ADR-decisions §3.1: this matters more than any technical comparison.)
- [x] **Language and apps** (ADR-02, 04, 05): **decided 2026-11-16**: Go backend, React Native mobile (two apps), Next.js web. See [adr-decisions.md](../arch/adr-decisions.md) §6.
- [x] **Database** (ADR-06): **decided**: PostgreSQL + PostGIS.
- [x] **Cloud** (ADR-09): **decided**: dev on Oracle Cloud (OCI); staging, production and DR on AWS India regions. **[VALIDATE region names and OCI service availability.]**
- [ ] **Confirm my Oracle reading:** OCI for dev, and **no Oracle Database** in the application (you answered both "OCI and Oracle Database" and "PostgreSQL").
- [ ] **Confirm the Go proposals** (ADR-03 chi + OpenAPI-first + pgx/sqlc; ADR-07 PostgreSQL-backed queue; ADR-24 plain monorepo; ADR-25 test tools). Expo or bare React Native.
- [ ] **CI/CD** (ADR-10): the repo is on **GitLab** (`gitlab.com/viyallabs/mugizh`), so GitLab CI is proposed; confirm.
- [ ] **Identity service** (ADR-11): still open; a portable self-hosted option suits a two-cloud setup.
- [ ] **Accept the two-cloud cost** (or move dev to AWS): see adr-decisions §6.3.
- [ ] **Approve the CLAUDE.md §4 change** (proposed text in adr-decisions §6.5). Until then CLAUDE.md still says the stack is undecided.
- [ ] Update the Architecture sheet ADR rows (status, "Decided on"; add ADR-30, 31).
- [ ] **Accept the 15 stack-independent ADRs** (ADR-01, 08, 12 to 23, 26), including the five "accept with change".
- [ ] **ADR-21 log retention:** 180 days (contract text today) or 1 year (proposed). Choosing 1 year means changing K-SEC-01, S-059 and S-095 through an ADR.
- [ ] **scheduler-core option a, b or c** (adr-decisions §2a). Recommended **b**. Option c requires you to amend requirement R-75.
- [ ] **ADR-27 recovery objectives** (RPO 15 min, RTO 4 h proposed), **ADR-28** (outbox + events), **ADR-29** (error format and idempotency).
- [ ] **Does v1.0 need the Pro mobile app,** or only Pro web at first? (From architecture-overview §10.)
- [ ] **Sign the v1.0 scope** and beta cut ([scope-v1.0.md](../scope-v1.0.md) §6).
- [ ] **Sign money flow** (clinic-collected, S-019) and the nurse decision (S-031).
- [ ] **Approve or change the other "In Testing" documents** so they can move to Done. At minimum those that Sprint 0 depends on: S-055 (infra), S-056 (encryption), S-057 (consent), S-059 (audit), S-036 (module boundaries), S-038 (API standards), S-065 (performance targets).

---

## 3. Gate B: things that must exist before Sprint 0 starts

### 3.1 Accounts and access **[YOU]**
- [ ] **OCI** tenancy for dev (India region **[VALIDATE]**), with billing, budget alerts and MFA on the admin account. Synthetic data only.
- [ ] **AWS organisation** with **separate accounts** for staging and for production/DR (infrastructure doc §1 rule 1); India regions only, enforced by a service control policy (residency check R1); billing, budget alerts, root MFA.
- [ ] Short-lived CI deployment identity set up per cloud (no long-lived access keys).
- [ ] Check OCI's managed PostgreSQL supports PostGIS and the same PostgreSQL major version as AWS **[VALIDATE]**; if not, plan to run PostgreSQL in a container in dev.
- [ ] GitLab project settings: protected `main`; merge needs approvals; **"code owner approval" ON** (otherwise `CODEOWNERS` does nothing); push rules / secret detection on.
- [ ] Replace the placeholders in `CODEOWNERS` (`@FOUNDER_USERNAME`, `@CLINICAL_REVIEWER_USERNAME`) with real usernames. **A named registered doctor as clinical reviewer is required** (K-CS-07).
- [ ] Team members invited with least-privilege roles.
- [ ] A password manager and a place for secrets (no secrets in the repo, K-SEC-05).

### 3.2 Repository hygiene **[ME]**
- [ ] Replace the template `README.md` with a real one (what this is, how to read `CLAUDE.md`, where docs live).
- [ ] Add `.gitignore` (Go, Node/pnpm, React Native, Terraform state, `.env`) and `.editorconfig`. The stack is now known, so this can be done as soon as you ask.
- [ ] Branch naming and commit rules are in `CLAUDE.md` §6; add a GitLab MR template link check.

### 3.3 Sprint 0 stories and their prerequisites (from the Backlog)
| Story | What | Needs first |
|---|---|---|
| S-090 | Monorepo: define the **Go + pnpm layout** (the story text still says "apps and packages") | S-036 signed; ADR-24 confirmed |
| S-091 | CI: build, tests, lint, SAST, secret and dependency scan, with **Go tools** (`govulncheck`, `gosec`) and React Native / Next.js tooling | S-090; ADR-10 (CI tool) confirmed |
| S-093 | Infrastructure as code: **two providers** (OCI dev, AWS staging/prod/DR); keep dev small | S-055 signed (update §9 done); OCI and AWS accounts |
| S-094 | Continuous deployment: to **AWS staging** on merge; manual promotion to prod; migrations with rollback | S-091, S-093 |
| S-095 | Observability with scrubbing, 180-day (or 1-year) logs in India | S-093; ADR-21 decision |
| S-096 | Audit log service | S-059 signed; S-090 |
| S-097 | Consent service skeleton | S-057 signed; S-090 |
| S-098 | Field-level encryption, per-patient keys, behind a **`keys` interface** with OCI, AWS and a local-only provider (refused in staging and production) | S-056 signed; S-093 |
| S-099 | Feature flags + country-pack loader | S-054 (Done) |
| S-100 | Internationalisation (English first) | S-090 |
| S-101 | Test harness: Go tests against **real PostgreSQL + PostGIS in containers**, adapter contract tests, React Native end-to-end (Maestro or Detox), Playwright for web | S-091; ADR-25 confirmed |
| S-102 | **DLT registration** (entity, headers, templates) | S-041 spike; S-007 legal entity |
| S-103 | App store accounts, signing, health-app declarations | None, but needs a legal entity in practice **[VALIDATE]** |

### 3.3b Process proven **[ME]**
- [ ] Re-run the CLAUDE.md start-of-story check on a Sprint 0 story once the stack is set (dry-run.md Run 3 says to do this). It stopped correctly at "dependencies not Done" last time.

---

## 4. Real-world items with lead time: start now **[3RD] / [YOU]**

These do not need code, but they gate code and they are slow.

| Item | Why it matters | Blocks | Status |
|---|---|---|---|
| **S-007 Company basics:** legal entity, bank account, GST, trademark search | DLT registration, app stores, payment aggregator onboarding, vendor contracts, clinic agreements | S-016, S-102, S-103, merchant accounts | Backlog |
| **S-009 Engage a health-tech and privacy lawyer** | Every `[VALIDATE]` in the docs lands here; the compliance review (2026-11-16) has 11 items for them | S-022, S-023, S-275 → doctor onboarding (S-110) | Backlog |
| **Name decision** (S-016) | Domains, DLT headers (sender ID), store listings, trademarks. Changing it later is expensive | Branding, S-102 header | Backlog |
| **Doctor reviewer named** (S-029, S-085) | Rules engine wording, clinical copy, contracts | S-138 (rules engine) | **Blocked** |
| **Vendor spikes** | S-039 video → S-135; S-040 payments → S-124; S-041 SMS/DLT → S-102; S-042 verification → S-109 | Adapters | Backlog |
| **Payment aggregator partner access** on clinic accounts, plus clinic merchant KYC (about 2 to 7 days per clinic) | v1.0 money flow depends on it. If no aggregator offers partner access, the design must change | S-124, S-125, S-163 | Unknown |
| **VAPT auditor** quotes (S-051) | Independent test is required before real patient data | P12 | In Testing |
| **App store accounts** (Apple, Google Play) | Review times; health-app declarations | Beta release | Backlog |
| **Lawyer decisions already queued:** retention hold (R-01), online consults only inside India (R-02), languages (R-03), law-enforcement requests (R-06), analytics D-1 | Affect data model and consent flows | S-097, S-098, data-rights stories | Docs updated; lawyer confirmation pending |

---

## 5. Contracts and process readiness

- [ ] **S-085 Lawyer and doctor review of the compliance contracts.** Until then they are DRAFT but bind Claude. Plan: review before the first PR that depends on them, ideally before Sprint 0 ends.
- [ ] **S-092 Automated contract checks in CI** (no vendor SDK outside `adapters/`, no country constants, import boundaries, OpenAPI lint). The stack is now Go, so the proposed tools are Go `internal/` packages plus an import-rule linter. The lint fixtures and Spectral rules already exist in `docs/arch/`.
- [ ] **OpenAPI → TypeScript client generation** (proposed ADR-30) in CI, so the Go API and the React Native and Next.js apps cannot drift apart.
- [ ] **Dual-cloud rules** (adr-decisions §6.3) turned into checks where possible: no cloud secrets SDK in app code, no cloud-specific managed service outside adapters, the local key provider refused outside dev.
- [ ] **S-076 PR template + review prompt:** exist; confirm they work in GitLab (open a test MR).
- [ ] **Synthetic data rule** (CLAUDE.md §5.3): decide where synthetic test data comes from and who owns it. Aadhaar-like values must fail Verhoeff.
- [ ] **Definition of Done requires "deployed to staging"** (K-DOD-01): for the first stories this needs S-093 and S-094, so Sprint 0 infra comes before feature work.

---

## 6. Prototypes that de-risk the MVP (need the stack, run early in Sprint 0)

| Story | Prototype | Pass bar | Blocks |
|---|---|---|---|
| S-066 | 100 simultaneous bookings on one slot | Exactly one success | S-121 (booking concurrency) |
| S-067 | Radius search on 50,000 synthetic clinics | p95 under 300 ms | S-119 (search) |
| S-068 | scheduler-core at 1,000,000 reminders per day | 99.9% within 60 s | S-127 (scheduler) |

- [ ] All three done and recorded in `docs/arch/performance-targets.md` before the stories they block.
- [ ] If a prototype fails, revisit the related ADR (06, 07, 19) before building on it.
- [ ] **S-068 now also decides the queue.** The proposed PostgreSQL-backed queue (ADR-07, instead of Redis + BullMQ) must handle 1M reminders per day with 99.9% within 60 s. If it fails, the fallbacks are a Redis-based Go queue or AWS SQS.
- [ ] **Run S-066 and S-067 on real PostgreSQL + PostGIS** with the same major version as AWS staging, not on a mock. Then **repeat the key numbers once on AWS staging**, because dev is on OCI.
- [ ] **S-097** also decides the consent cache (Redis is no longer in the stack): in-process with a very short TTL plus revocation events, or a managed Redis used only for this. Measure the under-20 ms target.

---

## 7. Design readiness (needed for UI stories, not for Sprint 0)

- [ ] S-077 design system (WCAG 2.1 AA target, light/dark, large text).
- [ ] S-078 wireframes for every v1.0 screen, S-086 high-fidelity including empty, error, offline and permission-denied states.
- [ ] S-087 usability test: 5 patients (2 aged 55+) and 5 doctors.
- [ ] S-088 design sign-off and handoff.
- [ ] S-079, S-089 (privacy UX, microcopy) signed; clinical wording reviewed by the named doctor.

The tracker marks P09 for sprints 3 to 4. UI stories in P11 depend on it.

---

## 8. Validation before the MVP (the roadmap's own rule)

The roadmap says v0 is a validation step **before production code**, and that you should not start the next version until the current one meets its exit criteria.

- [ ] **S-014** recruit 10 to 15 doctors in one city; **S-024** interview 30 to 50 patients and caregivers; **S-025** run the concierge loop.
- [ ] **S-082** measure v0 exit criteria and write the go/no-go memo. Targets (suggested): at least 10 doctors committed, at least 40% patient engagement at day 60, at least 3 doctors willing to pay.

**Decision for you:** the phase plan overlaps v0 (weeks 2 to 7) with the engineering foundation (week 7 onward) and starts the MVP at week 9. **Nothing in the tracker depends on S-082** (it blocks zero stories), so the tool will not stop P11 from starting before the memo exists. Options:
1. **Recommended:** build Sprint 0 (foundation) in parallel with v0, and make **S-082 a dependency of S-104** so the MVP waits for the go/no-go.
2. Start the MVP without waiting. Faster, but you may build features for a hypothesis v0 later disproves.

A 4-week v0 cannot finish before about the end of December 2026 if it starts after doctors are recruited, so this affects the calendar.

---

## 9. Suggested order of work (next 2 weeks)

1. **This week [YOU]:** ~~decide the stack and cloud~~ (core decided 2026-11-16). Now: confirm the Go proposals, the Oracle reading, the identity service and the two-cloud cost; approve the `CLAUDE.md` §4 change; name the clinical reviewer; start S-007 (company) and S-009 (lawyer); start the name decision.
2. **This week [ME]:** README, `.gitignore` (Go, Node, React Native, Terraform), tracker status list, an updated CLAUDE.md §4 once you approve; draft the Go + pnpm layout for S-090; prepare the S-039 to S-042 spike plans so vendor choices can move; check OCI service availability (PostGIS, key vault, India regions) **[VALIDATE]**.
3. **Next week [YOU + ME]:** sign the "In Testing" designs that Sprint 0 needs; S-055 infra design becomes final (with the OCI + AWS update); open the OCI and AWS accounts.
4. **Then:** S-090 monorepo → S-091 CI → S-093 infra → the rest of Sprint 0, with the three prototypes (S-066, 067, 068) early.
5. In parallel: S-014 to S-025 (v0), P09 design, spikes.

---

## 10. Things I could not verify

- Whether the "In Testing" documents were reviewed by you offline; I treated them as unsigned because the sheet says so.
- Names and availability of cloud regions, store requirements and aggregator partner programmes (marked **[VALIDATE]**; these change).
- **OCI specifics:** whether its managed PostgreSQL offers PostGIS, which India regions and key-vault features exist, and how it compares with AWS. I have not checked any of this.
- Whether you meant Oracle Database for anything besides dev hosting (see Gate A).
- Team size and skills, budget and the real start date for v0 recruiting. Those decide how realistic the calendar is.

# CLAUDE.md: how Claude works on this repository

**Story:** S-070 · **Contracts:** K-DOD-01, K-DOD-02, K-DOD-03, K-DOD-04 · **Last updated:** 2026-11-15

This is a **healthcare app for India**: patients find verified doctors, book and pay, consult in person or by video, get structured prescriptions, and receive reminders. **It handles health data and prescribing**, so the law and patient safety come before speed. When a rule here and a request conflict, **stop and ask.**

## 1. Sources of truth (read in this order)

| What | Where |
|---|---|
| This file | `CLAUDE.md` |
| Stories, statuses, edge cases, compliance register, roles, ADRs | `Resources/healthcare-app-agile-roadmap.xlsx` (Backlog, Edge Cases, Compliance, Contracts, Roles, Architecture, Performance sheets) |
| **Contracts** (the rules code must follow) | `contracts/compliance/*.md`, `contracts/architecture/*.md`. Until they are approved (S-085) they are DRAFT, but **they still bind Claude.** |
| Process: story and review templates, release policy | `docs/process/` |
| Architecture, modules, adapters, API standards, country packs | `docs/arch/` |
| Security: threat model, encryption, consent, access, audit, data rights, mobile, backups, breach plan | `docs/security/` |
| Privacy: notices, consent texts, Aadhaar policy, children's policy | `docs/privacy/` |
| Clinical: telemedicine prescribing rules (spec + YAML), device-software assessment, doctor review pack | `docs/clinical/` |
| Payments, legal, messaging, scope, requirements, data inventory | `docs/payments/`, `docs/legal/`, `docs/messaging/`, `docs/scope-v1.0.md`, `docs/requirements-v1.0.md`, `docs/data-inventory-v1.0.md` |
| Tests every Critical or High edge case needs | `docs/test/edge-case-test-catalogue.md` (TC-01 to TC-66) |

## 2. Start of every story (K-DOD-04)

1. Read the story row: text, acceptance criteria, **Contract refs (K-IDs)**, **Edge case refs (E-IDs)**, **Compliance refs (C-IDs)**, and dependencies. **Every dependency must be Done**, or stop.
2. Read the contract files for those K-IDs, the edge cases, their TC tests, and the docs the story names.
3. **Before writing code, write down:**
   - which contract rules apply and how you will meet each;
   - which edge cases apply and which test covers each;
   - any conflict between the story and a contract or decision. **If there is a conflict, stop for the founder's decision.**
4. Write the failing tests first, then the code. **One PR per story.**
5. Use the story template in `docs/process/story-prompt-template.md`, and the review prompt in `docs/process/code-review-prompt-template.md`.

## 3. Non-negotiable rules (summary; the contract files are authoritative)

**Data protection (contracts/compliance/data-protection.md)**
- **K-DP-01:** personal and health data only in India-region infrastructure and India-processing vendors.
- **K-DP-02:** every read of a patient's data by anyone other than the patient goes through `records.authorize` (access tiers T1 to T4; [consent-design](docs/security/consent-design.md)) and writes a durable audit event. **There is no bypass flag.**
- **K-DP-03:** field-level encryption with per-patient keys for clinical content and contact details ([encryption-design](docs/security/encryption-design.md)).
- **K-DP-04:** no health data, full identifiers, tokens or request bodies in logs, analytics, crash reports or error messages.
- **K-DP-05:** never collect, store, log or display an Aadhaar number or VID. Reject Verhoeff-valid 12-digit input.
- **K-DP-06:** any new personal-data field needs a data-inventory row (purpose, basis, retention, deletion, **class**) in the same PR.
- **K-DP-07:** erasure reaches every store, including scheduler-core and vendors. Legal-retention records are restricted, not deleted. **Account data is restricted under the legal-minimum hold (RET-18) before final deletion.**
- **K-DP-08:** no vendor gets personal data until it is in the vendor register with a processor agreement.
- **K-DP-09:** notifications never show a diagnosis or specialty. Medicine names only with P-MEDNAMES.
- **K-DP-10:** no child profiles. Adults only in v1.0; children are gated by CH-11 in v1.1.

**Clinical safety (contracts/compliance/clinical-safety.md)**
- **K-CS-01:** only Verified doctors are listed, booked or can prescribe. Checked on the server.
- **K-CS-02:** every prescription item passes the rules engine (two layers, fail closed) using the country-pack tables. The checks are re-run at signing.
- **K-CS-03:** **the software never suggests, infers or auto-fills a drug or dose.** It only allows or blocks.
- **K-CS-04:** symptom search returns a specialty only (v1.1; F8 option A means no symptom computation).
- **K-CS-05:** signed notes and prescriptions are immutable. Changes are new versions.
- **K-CS-06:** SOS dials the emergency number directly. Never claim help was dispatched.
- **K-CS-07:** clinical rules and clinical wording change only with a registered doctor's approval in the PR.

**Payments (contracts/compliance/payments.md)**
- **K-PAY-01:** hosted checkout only; no card, UPI or bank data.
- **K-PAY-02:** idempotency keys plus durable operation records. State changes only from verified webhooks or status polls.
- **K-PAY-03:** we never hold patient money. Clinic-collected (mode B) in v1.0.
- **K-PAY-04:** append-only double-entry ledger.
- **K-PAY-05:** refunds only from the versioned policy, stored with each booking. **RF08 and RF09 are blocked** until the fairness decision (FAIRNESS-01).

**Messaging (contracts/compliance/messaging.md)**
- **K-MSG-01:** SMS only through registered DLT templates with typed variables; the adapter sends the Principal Entity ID, header and template ID.
- **K-MSG-02:** WhatsApp is **on hold**. Build nothing against a provider.
- **K-MSG-03:** quiet hours for non-critical messages.

**Security (contracts/compliance/security-logging.md)**
- **K-SEC-01:** logs in India for at least 1 year (proposed; contract text says 180 days until its ADR); NTP sync.
- **K-SEC-02:** incident runbook (CERT-In 6 h; DPDP notices).
- **K-SEC-03:** patient OTP + device binding. Doctors and staff need MFA. Signing needs step-up (P08) **and** an electronic signature (P09).
- **K-SEC-04:** deny by default plus ownership checks; others' resources return 404.
- **K-SEC-05:** no secrets in code.
- **K-SEC-06:** dependencies pinned and scanned.
- **K-SEC-07:** ASVS L2 and MASVS L2.
- **K-SEC-08:** staff access only through just-in-time grants.

**Architecture (contracts/architecture/*.md)**
- **K-ARC-01:** modules talk only through public interfaces or events; one schema per module; no cross-schema reads.
- **K-ARC-02:** scheduler-core holds no health data.
- **K-ARC-03:** vendors only through adapters.
- **K-ARC-04:** UTC + IANA time zone.
- **K-ARC-05:** FHIR R4 at the boundary.
- **K-ARC-06:** OpenAPI is the source of truth; no breaking changes inside `/v1`.
- **K-GLB-01:** no country constants in code; they live in country packs.
- **K-GLB-02:** translation keys for all text.
- **K-GLB-03:** each pack declares its residency.

## 4. Current decisions you must build to (do not re-decide them)

| Decision | Source |
|---|---|
| **WhatsApp is on hold.** Channels are push, SMS (DLT) and email. | Founder, 14 Nov 2026 |
| **Clinic-collected payments (mode B).** Money goes to the clinic's own account; we never hold it. Split settlement is v1.2. | money-flow-v1.0.md |
| **Adults only in v1.0. English app screens at launch,** but the privacy notices, consent texts, Terms and grievance page must be offered in the user's choice of English or an Eighth Schedule language (DPDP s.5(3), s.6(3); Intermediary Rules 3(1)(a)). | scope-v1.0.md; compliance review R-03 |
| **Online consults only for patients in India** (P10). In-person bookings from abroad are fine. | telemedicine-rules-spec.md; compliance review R-02 |
| **Prescription reminders are opt-in (P-REMIND).** Once on, each item gets reminders that can be switched off. | consent-texts.md |
| **Doctor access tiers T1 to T4.** No "all patients" directory. | consent-texts.md A2 |
| **Nurses are reserved for v4.0.** The role exists; no accounts. | S-031 option A |
| **Tech stack (decided 2026-11-16):** backend, workers and scheduler-core in **Go** (`net/http` + chi, OpenAPI-first with oapi-codegen, pgx + sqlc, slog, OpenTelemetry); queue **River** on PostgreSQL (must pass S-068); mobile **React Native with Expo prebuild** (two apps), web **Next.js**; TypeScript clients generated from OpenAPI (openapi-typescript); database **PostgreSQL + PostGIS**; identity **Ory Kratos, self-hosted** (spike in S-104/S-105); repo **go.work + pnpm + Taskfile**; CI **GitLab CI**; IaC **OpenTofu**; tests: Go testing + testcontainers, Playwright, Maestro, k6, ZAP, govulncheck, gosec, Semgrep, Gitleaks, Trivy. **Dev on Oracle Cloud (OCI), synthetic data only; staging, production and DR on AWS India regions.** Follow the dual-cloud rules (no cloud SDK in app code; `keys` interface; S3-compatible storage; same images and PostgreSQL version). **Do not add a framework, library family, queue, cloud service or vendor beyond this without an ADR.** | adr-decisions.md §6 |
| Telemedicine rules **v1.1.0-draft**. Not approved until the doctor and lawyer review it. | docs/clinical/ |

## 5. Never do these

1. **Never edit a file in `/contracts`** to make code pass (K-DOD-03). Changing a contract needs an ADR the founder approves.
2. **Never invent a legal or clinical fact.** If unsure, write `[VALIDATE]` (lawyer or primary text), `[CONFIRM]` (founder) or `[FILL]` (missing detail). Never present an inference as law.
3. **Never use real personal data outside production.** Dev, staging, tests and fixtures are synthetic only. Aadhaar test values must fail the Verhoeff check.
4. Never import a vendor SDK outside `adapters/`. Never put a country value (for example `+91`, `112`, `INR`, `Asia/Kolkata`) in code.
5. Never weaken a test, skip a security check, or add a feature flag that can turn off consent, the rules engine, the prescribing gate, audit or encryption.
6. Never resolve a conflict silently. Never decide a `[CONFIRM]` item for the founder.

## 6. Story and PR conventions (K-DOD-02)

- **Branch:** `story/S-123-short-name`.
- **PR title:** `S-123: <story text, short>`.
- **One PR per story.**
- **PR description:** the merge-request template (S-076), with:
  - the story ID;
  - the **K-IDs touched**, each with how it is met;
  - the E-IDs and TC tests;
  - the data-inventory rows added;
  - the ticked checklist.
- **Commits:** small, imperative ("Add consent gate to records API"), and referencing the story ID.
- **Definition of Done (K-DOD-01):**
  - acceptance criteria met;
  - tests written and passing;
  - the Claude contract review done and approved;
  - SAST, secret and dependency scans pass;
  - docs updated;
  - deployed to staging;
  - tracker columns updated: Status, Code review, Tests, Owner, Done on, Notes/PR link.
- **Non-code stories** are Done when their acceptance criteria are met and the output is in `/docs`. Code review and Tests show N/A.
- **Statuses:** Backlog → Ready → In Progress → In Code Review → In Testing → Done (or Blocked, Won't Do).

## 7. When to stop and ask

- A dependency is not Done, or a decision the story needs is still open.
- The story conflicts with a contract, a decision in section 4, or the law as written in the docs.
- You would need real data, a new vendor, a new personal-data field without an inventory row, or a change to clinical rules or wording.
- Anything that touches prescribing, consent, encryption, audit, erasure or payments behaves differently than the design says.

## 8. Process contracts (word for word from the Contracts sheet; change only by ADR, K-DOD-03)

- **K-DOD-01:** A story is Done only when: acceptance criteria met; tests written and passing; Claude review against contracts done and approved; SAST, secret and dependency scans pass; docs updated; deployed to staging.
- **K-DOD-02:** Every PR names its story ID and lists the contract rule IDs it touches, with the checklist ticked.
- **K-DOD-03:** Contract files change only through an ADR approved by the founder. Claude must never edit a contract to make code pass.
- **K-DOD-04:** Before writing code for a story, Claude reads CLAUDE.md, the contracts listed on the story, and the story's edge cases, and states which rules apply.

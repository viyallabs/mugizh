# Architecture decision records: recommendations (S-035)

**Phase P05 · Epic EP07 · Depends on:** S-034 ([architecture-overview.md](architecture-overview.md)) · **Contract refs:** K-ARC-* · **Date:** 2026-11-15
**Status:** In Testing. Every ADR has a recommendation with reasoning. **The founder decides each one** (Accept, Change or Reject, K-DOD-03). The tech stack ADRs are deliberately left open (section 3).

**Who this is for:** the founder, to decide each ADR; and the team, to know what is settled.

## 1. How to read this

- **Source:** the Architecture sheet, rows ADR-01 to ADR-26 (status Proposed). A new column J in that sheet carries the recommendation summary.
- **Recommendation values:** **Accept** (as written) · **Accept with change** (the change is described) · **Founder: stack** (open on purpose; options and a default are given in section 3).
- In the sheet, the status stays **Proposed** until the founder decides. Then set it to Accepted, Changed or Rejected and fill "Decided on".
- **The architecture documents don't depend on the open stack ADRs.** [architecture-overview.md](architecture-overview.md), [module-boundaries-and-domain-model.md](module-boundaries-and-domain-model.md), [adapter-interfaces.md](adapter-interfaces.md) and [api-standards.md](api-standards.md) name roles, not products.

## 2. Architecture ADRs (stack-independent)

| ADR | Area | Recommendation | Reasoning |
|---|---|---|---|
| ADR-01 | Architecture style: modular monolith + scheduler-core separate | **Accept, with the lighter deployment option b (section 2a)** | Fastest for a small team. Boundaries are enforced by database grants and lint ([module-boundaries](module-boundaries-and-domain-model.md) B1–B10), so extraction stays cheap. For scheduler-core, see section 2a. |
| ADR-08 | File storage: private buckets in India, KMS, signed links | **Accept** | Meets residency (K-DP-01) and download-link edge cases. Uploads go straight to storage through signed URLs (API standards §8). |
| ADR-12 | Encryption: TLS 1.2+, KMS envelope, field-level, per-patient keys | **Accept** | Needed for crypto-shredding, so erasure reaches backups (K-DP-03, K-DP-07). Detailed design in S-056. |
| ADR-13 | Video behind an adapter; no recording by default | **Accept** (vendor from S-039) | Adapter written ([adapter-interfaces](adapter-interfaces.md) §9). Add: media region India; recording forced off and tested. |
| ADR-14 | Payments behind an adapter | **Accept with change** | **Change:** v1.0 is mode B, clinic-collected through partner access, so the deciding factor is **partner or platform access on clinic accounts**, not split eligibility. Split settlement moves to v1.2 (S-217). Vendor from S-040. |
| ADR-15 | Messaging: WhatsApp, DLT SMS, push via the notification module | **Accept with change** | **Change:** WhatsApp is on hold, so v1.0 uses push + DLT SMS (+ email for receipts). The WhatsApp adapter interface is kept but disabled. SMS sends registered templates only (K-MSG-01). Vendor from S-041. |
| ADR-16 | Maps: PostGIS radius search; geocoding behind an adapter | **Accept** | Patient locations never leave our system; only clinic addresses go to the geocoder ([adapter-interfaces](adapter-interfaces.md) §10). Depends on ADR-06. |
| ADR-17 | Own model mapped to FHIR R4 at the boundary | **Accept** | Needed for ABDM in v2.0 (K-ARC-05). Mapping design in S-083. |
| ADR-18 | Prescription rules as decision tables in the country pack | **Accept** | Already used by the telemedicine rules spec: fail closed, versioned, doctor-reviewed (K-CS-02, K-CS-07). |
| ADR-19 | Scheduler: RFC 5545, rolling 14-day occurrences, delayed jobs, on-device backup | **Accept** | Meets "99.9% within 60 s" and the offline requirement. Prototype in S-068. |
| ADR-20 | OCR chosen by spike; human review always | **Accept with change** | **Change:** a vision model or cloud document AI is allowed **only** with India processing and a processor agreement (K-DP-01, K-DP-08). The output never sets a dose (K-CS-03). v1.1. |
| ADR-21 | Observability: OpenTelemetry; logs scrubbed; 180-day retention in India | **Accept with change** | **Change:** retention **1 year** as proposed in S-020 (RET-07), if the founder approves that decision. Then the K-SEC-01 contract text, S-059 and S-095 must change too, through an ADR (K-DOD-03). Otherwise keep 180 days. |
| ADR-22 | Search: PostgreSQL full-text + trigram first | **Accept** | Less infrastructure. Meets the drug search target (under 150 ms) per the Performance sheet. Depends on ADR-06. |
| ADR-23 | Self-hosted flags + country-pack config loader | **Accept with change** | **Add:** the non-flaggable list (consent, rules engine, prescribing gate, audit, encryption), and "the config loader refuses an adapter whose vendor isn't in the register" ([release policy](../process/release-and-versioning.md) §5; [adapter-interfaces](adapter-interfaces.md) A3). |
| ADR-26 | Self-hosted, privacy-first analytics in India; no health data | **Accept** | DPDP and E-DP-25; matches the metrics and events list (S-005). Tool choice follows the stack. |

## 2a. scheduler-core: separate from day 1? (review point, 2026-11-15) [CONFIRM founder]

**Why it was separate:**
- **Requirement R-75** (Must): "Run the calendar as a separate service with its own API and data".
- **The v5.0 plan** to make it a standalone product.
- **The privacy guarantee:** it holds no health data (K-ARC-02). A separate service with its own database makes that guarantee **physical**, not just a lint rule.

**What it costs:**
- a second deployable, with its own health checks and alerts;
- service-to-service authentication;
- network calls instead of function calls;
- a second database to back up and restore;
- more for a 1–3 person team to run.

| Option | What it means | Keeps K-ARC-02? | Running cost |
|---|---|---|---|
| a. Fully separate (as drafted) | Own service, own database server | Yes, physically | Highest |
| **b. Separate process, shared database server (recommended)** | Its own deployable and API, built from the same repo and pipeline. Its own **database and role** on the same managed database server. No other module's role can reach its database. | Yes. Separate database and role, separate process, plus the lint and contract test | Low: one database server, one pipeline; one extra process |
| c. Module inside the monolith | A module like the others, with schema and lint isolation only | Only by lint and grants; the network boundary is lost | Lowest. **Breaks R-75 as written**, so the requirement needs changing |

**Recommendation: option b.** It keeps R-75 and the physical privacy boundary, and removes most of the operational cost. The only extra running piece is one process. Moving to option a later means moving one database, with no code change. **Choosing option c needs the founder to amend R-75.**

## 3. Tech stack ADRs: open for the founder

These need the founder's decision. I recommend deciding **before sprint 3**, because S-055 (infrastructure design, sprint 3) and S-090 (monorepo, sprint 4) can't start without them. Each row gives the proposal from the sheet, the alternatives, and what would change.

### 3.1 What should decide the stack

1. **Who will build and maintain it.** The skills of the people you can hire in your city matter more than anything below. **[CONFIRM: team skills]**
2. **One language or two.** One language across apps and API means shared types and fewer hand-offs.
3. **Claude-assisted development.** All the options below are well supported. Mainstream, strongly typed stacks give the best results with AI-written code and review.
4. **India residency.** Every managed service must run in an India region (K-DP-01).
5. **Low-end Android phones.** App size and cold start under 3 s (Performance sheet).

### 3.2 The open ADRs

| ADR | Area | Proposed (sheet) | Main alternatives | What changes if not the proposal |
|---|---|---|---|---|
| ADR-02 | Language | TypeScript end to end | Python backend + TypeScript apps; Kotlin or Java backend; Go backend | Two languages: types must be generated from OpenAPI for each side (works, but more tooling). |
| ADR-03 | Backend framework | NestJS | Fastify (lighter); Django or FastAPI (if Python); Spring Boot (if Kotlin/Java) | Only how modules and guards are coded. Module rules (B1–B10) apply the same way. |
| ADR-04 | Mobile apps | React Native with Expo; two apps (Patient, Pro) | Flutter; native Android + iOS | Flutter: Dart instead of TypeScript; strong performance on low-end phones; no code sharing with web. Native: highest quality, twice the work. **Keep two separate apps whatever the choice.** |
| ADR-05 | Web apps | Next.js (Pro web, Admin, public pages) | React with a lighter build tool; server-rendered pages from the backend framework | Public pages need search-engine friendly rendering. Pro web and Admin can be a plain single-page app. |
| ADR-06 | Database | Managed PostgreSQL + PostGIS, one schema per module | MySQL; MongoDB | **Strongly recommended to keep.** Radius search, trigram search, row locks for bookings, and schema-level grants all assume PostgreSQL. Changing it affects ADR-16, ADR-22 and the Performance sheet. |
| ADR-07 | Jobs and queues | Redis + BullMQ | Cloud queue service; a PostgreSQL-based queue; RabbitMQ | BullMQ is tied to Node/TypeScript. With another language, use that language's equivalent or a cloud queue. Needs delayed jobs, retries and a dead-letter queue. |
| ADR-09 | Cloud | One major cloud with two India regions | AWS (Mumbai + Hyderabad); Azure (Pune + Chennai); Google Cloud (Mumbai + Delhi) | All three have two India regions. Decide on credits, team familiarity, managed PostgreSQL with PostGIS, KMS, and India-region identity. Check MeitY empanelment if government sales are planned. **[VALIDATE region names and service availability at decision time]** |
| ADR-10 | Infrastructure and delivery | Terraform + GitHub Actions | Cloud-native tools; **GitLab CI** | **Note:** this repository is hosted on GitLab (see README). Use GitLab CI unless you plan to move to GitHub. Terraform (or OpenTofu) works with either. |
| ADR-11 | Authentication | Managed identity in an India region, or Keycloak; OTP + MFA | Cloud identity service; self-hosted Keycloak; custom on a library | Must support phone OTP, device binding, MFA, step-up for prescription signing, and India hosting (K-SEC-03). |
| ADR-24 | Monorepo | Turborepo or Nx | Separate repositories | Turborepo/Nx assume JavaScript/TypeScript. With mixed languages, use a plain monorepo with per-folder tools. |
| ADR-25 | Testing stack | Jest/Vitest, Playwright, Maestro/Detox, k6, ZAP, Semgrep, Gitleaks, Trivy | Language equivalents | Playwright, k6, ZAP, Semgrep, Gitleaks and Trivy work with any stack. Only the unit-test and mobile end-to-end tools follow the language. |

### 3.3 My default if you have no strong preference

Keep the sheet's proposal: **TypeScript end to end, NestJS, React Native (Expo), Next.js, PostgreSQL + PostGIS, Redis + BullMQ**. Add **GitLab CI** (because the repo is on GitLab) and choose the **cloud by credits and team familiarity**. Reasons:
- one language for a small team;
- shared types between the API and apps;
- every proposal here is well documented and works well with Claude-assisted development.

**Change it if your developers are stronger in another stack:** Python plus Flutter is the most common alternative. The architecture documents stay valid either way.

## 4. New ADRs proposed by this phase (add to the sheet when accepted)

| ADR | Area | Proposal | Why |
|---|---|---|---|
| ADR-27 | Recovery objectives | **Up to 15 minutes of data loss (RPO); service back within 4 hours (RTO)** for the core database. Point-in-time recovery; backups copied to the DR region; restore re-applies erasures. | Requirements §3.2 left this open. A sensible start for a beta; tighten for public launch. **[CONFIRM]** |
| ADR-28 | Module communication and audit durability | In-process public interfaces for queries; **transactional outbox** + queue for events; sagas for hold → pay → confirm. **Audit events use the same outbox pattern:** written in the same transaction, or before a read returns; hash-chained later. | Makes K-ARC-01 work without distributed transactions. A crash can never lose a mandatory audit record (K-DP-02, K-CS-05) ([module-boundaries](module-boundaries-and-domain-model.md) B4–B6, audit). |
| ADR-29 | API error format and idempotency | RFC 9457 problem details; `Idempotency-Key` stored 24 hours; cursor pagination | Settled in [api-standards.md](api-standards.md). Recorded as an ADR so changes need approval. |

## 5. Decision table for the founder

| Group | Decision |
|---|---|
| Section 2: accept all 15 recommendations (including the 5 "with change") | ☐ Agree ☐ Change: |
| ADR-21 retention: 1 year (only if S-020 decision 2 is approved) | ☐ 1 year ☐ Keep 180 days |
| Section 3: tech stack | ☐ Default (3.3) ☐ Other: |
| ADR-09 cloud provider | ☐ AWS ☐ Azure ☐ Google Cloud |
| ADR-10 CI: GitLab CI or GitHub Actions | ☐ GitLab CI ☐ GitHub Actions |
| Section 4: ADR-27, 28, 29 | ☐ Agree ☐ Change: |
| Name / date | |

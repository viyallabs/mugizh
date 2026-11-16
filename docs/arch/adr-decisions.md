# Architecture decision records: recommendations (S-035)

**Phase P05 · Epic EP07 · Depends on:** S-034 ([architecture-overview.md](architecture-overview.md)) · **Contract refs:** K-ARC-* · **Date:** 2026-11-15
**Status:** In Testing. Every ADR has a recommendation with reasoning. **The founder decides each one** (Accept, Change or Reject, K-DOD-03). The tech stack ADRs were left open (section 3); **the founder decided the core stack on 2026-11-16 (Go, React Native, Next.js, PostgreSQL + PostGIS, OCI for dev, AWS for staging and production): see section 6.**

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

---

## 6. Founder decisions on the tech stack (2026-11-16)

**Status:** recorded from the founder's message and answers. The **Architecture sheet still shows "Proposed"** for every ADR; update it when you confirm this section (Accepted or Changed, with "Decided on").
**Not yet in `CLAUDE.md` §4**, which still says the stack is undecided (proposed wording in §6.5; Claude does not edit it without your approval).

### 6.1 What was decided

| ADR | Area | Decision | Compared with the sheet |
|---|---|---|---|
| ADR-02 | Language | **Go** for the backend (core API, workers, scheduler-core). **TypeScript** for the clients (React Native, Next.js), which follows from the choices below. | **Changed** (was TypeScript end to end) |
| ADR-04 | Mobile | **React Native**; two apps (Patient, Pro). Expo or bare React Native: not stated. **[CONFIRM: Expo proposed]** | Accepted |
| ADR-05 | Web | **React with Next.js** (Pro web, admin console, public pages) | Accepted |
| ADR-06 | Database | **PostgreSQL + PostGIS**, one schema per module | Accepted |
| ADR-09 | Cloud | **Two clouds:** **Oracle Cloud Infrastructure (OCI) for development**; **AWS for staging, production and DR** (India regions: Mumbai `ap-south-1`, Hyderabad `ap-south-2` **[VALIDATE]**) | **Changed** (was one cloud for all) |

**Reading I made, please confirm:** you answered "Both OCI and Oracle Database" for Oracle, but chose PostgreSQL + PostGIS for the application database. I recorded **OCI as the dev cloud and PostgreSQL as the only application database. Oracle Database is not used by the application.** If you intended Oracle Database for something else (for example an existing licence or a reporting system), tell me, because it would be a new ADR and a new vendor-register entry (K-DP-08).

### 6.2 ADRs that these decisions re-open

The sheet's proposals for these assumed TypeScript, so they need a new decision. **The defaults below are my proposals, not your decisions. [CONFIRM each]**

| ADR | Area | Was | Proposed now | Why |
|---|---|---|---|---|
| ADR-03 | Backend framework | NestJS | Go standard library `net/http` with the `chi` router; **OpenAPI-first**: server stubs generated from the OpenAPI file (for example `oapi-codegen`); `pgx` + `sqlc` for typed SQL; `slog` for logs; OpenTelemetry | The OpenAPI file stays the source of truth (K-ARC-06). Typed SQL keeps per-module schema grants visible. Few dependencies suit a health product. |
| ADR-07 | Jobs and queues | Redis + BullMQ (Node only) | **PostgreSQL-backed queue** (for example River) | The transactional outbox (ADR-28) needs a job saved in the **same transaction** as the state change; a Postgres queue does that natively and removes Redis. **S-068 (1M reminders per day) must prove it.** Fallback: a Redis-based Go queue (asynq) or AWS SQS. |
| ADR-24 | Monorepo | Turborepo or Nx | **Plain monorepo:** one Go module (or `go.work`) plus a `pnpm` workspace for React Native and Next.js; a task runner such as `make` or Taskfile | Turborepo and Nx are JavaScript tools. Module walls use Go `internal/` packages plus an import-rule linter (K-ARC-01), and a rule banning vendor SDK imports outside `adapters/` (K-ARC-03). |
| ADR-25 | Testing | Jest/Vitest + others | Go `testing` with **real PostgreSQL + PostGIS in containers** (testcontainers); `govulncheck`, `gosec`, Semgrep, Gitleaks, Trivy; Playwright (web); Maestro or Detox (mobile); k6; OWASP ZAP | Tests against a real database prove the grants and locks the booking design relies on. |
| ADR-10 | CI/CD and IaC | Terraform + GitHub Actions | **GitLab CI** (repo is on GitLab) **[CONFIRM]**; **Terraform or OpenTofu** with two provider sets (OCI, AWS) | Short-lived cloud credentials from CI (OIDC), one per cloud (infrastructure §7). |
| ADR-11 | Authentication | Managed identity in India, or Keycloak | **Still open.** Dev and prod are on different clouds, so a **self-hosted, portable** option (Keycloak) behaves the same in both; a cloud-managed service would differ per cloud. | Decide as a follow-up to S-055; needs phone OTP, device binding, MFA and step-up signing (K-SEC-03). |
| New: ADR-30 | Client API types | none | **Generate TypeScript clients and types from the OpenAPI file** for React Native and Next.js; CI fails if generated code is out of date | Two languages must not drift apart. |
| New: ADR-31 | Dual-cloud portability | none | The rules in §6.3 | Keeps what passes in OCI dev passing in AWS staging. |

**Effects of Go and the Postgres queue on other designs**
- **Consent cache (K-DP-02, under 20 ms).** The design assumed a short-lived cache invalidated on revocation; Redis is no longer in the stack. **Open for S-097:** an in-process cache with a very short TTL plus revocation events, or a managed Redis used only for this. Measure before choosing.
- **ADR-12 encryption in Go.** Envelope keys with per-patient keys still apply; the key service is called through a `keys` interface (§6.3).
- **Two languages.** The Go API and the TypeScript clients share only the OpenAPI contract and the generated types.

### 6.3 Dev on OCI, staging and prod on AWS: rules that keep them equal

The risk is something working in dev and failing in staging because the clouds differ.

| Where they differ | Rule |
|---|---|
| Key management (OCI Vault vs AWS KMS) | App code calls a **`keys` interface**; one implementation per cloud, chosen by config. A local software-key provider may exist **only for local and dev**; the config loader refuses it in staging and production. |
| Secrets | The app reads secrets from environment variables injected at deploy time. **No cloud secrets SDK in app code** (K-SEC-05). |
| Object storage | Use the **S3-compatible API** on both; one client. |
| Database | **Same PostgreSQL major version and extensions (PostGIS, pg_trgm)** in dev and staging. **[VALIDATE]** that OCI's managed PostgreSQL supports PostGIS; if not, run PostgreSQL in a container in dev. |
| Queue | PostgreSQL-backed, so identical everywhere. |
| Containers | The **same container images** run in dev, staging and prod; keep orchestration manifests simple, because the OCI and AWS orchestrators differ. |
| CI deployment identity | Short-lived workload identity per cloud (no long-lived keys). |
| IaC | Two provider module sets. **Keep dev small;** staging and prod share one set of modules (same cloud). |
| App code | **No cloud-specific managed service in app code.** Cloud features go behind an adapter (K-ARC-03). |
| **Staging** | **Staging is the production rehearsal.** The VAPT, load tests and the Definition of Done "deployed to staging" (K-DOD-01) run on AWS. A story is not Done because it works in dev. |

**Residency.** Real personal data exists only in AWS production and DR, in India regions. **Dev (OCI) and staging hold synthetic data only** (CLAUDE.md §5.3). The residency checks R1 to R7 (infrastructure doc §4) apply to production, DR and staging on AWS; OCI dev should still use an India region for consistency **[VALIDATE region names]**.

**Cost to accept:** two clouds mean two sets of accounts, IAM, networking and billing for a small team. If the reason for OCI is credits or cost, that may be worth it. If not, **running dev on AWS too would remove the whole table above.** That is your call.

### 6.4 What to update

| Item | Change | Status |
|---|---|---|
| Architecture sheet, ADR rows | Set status and "Decided on"; update ADR-02, 03, 07, 09, 24, 25; add ADR-30, 31 | **You** (I do not edit the workbook: it was last saved by LibreOffice and a script could damage it) |
| [architecture-overview.md](architecture-overview.md) | Queue label "Redis + BullMQ" → PostgreSQL-backed | Done |
| [infrastructure-and-environments.md](infrastructure-and-environments.md) | Dev on OCI, rest on AWS | Done (§9 added) |
| S-090 (monorepo) | Story text lists "apps and packages"; define the Go + pnpm layout | Pending |
| S-093 (IaC) | Two providers; dev minimal | Pending |
| S-098 (encryption library) | `keys` interface with OCI, AWS and local providers | Pending |
| S-091, S-101 (CI, test harness) | Go and React Native tooling | Pending |
| S-068 (scheduler prototype) | Prove the PostgreSQL queue at 1M per day | Pending |
| S-097 (consent) | Decide the consent cache | Pending |
| `CLAUDE.md` §4 | See §6.5 | **Needs your approval** |

### 6.5 Proposed replacement for the CLAUDE.md §4 stack row (not applied)

> **Tech stack:** backend and scheduler-core in **Go**; mobile in **React Native** (two apps); web in **React/Next.js**; database **PostgreSQL + PostGIS**; **dev on Oracle Cloud (OCI); staging, production and DR on AWS India regions.** Defaults still to confirm: ADR-03, 07, 10, 11, 24, 25. **Do not choose a framework, queue, identity service or CI tool beyond what `docs/arch/adr-decisions.md` §6 records.** Source: founder, 2026-11-16.

### 6.6 Still open

1. Confirm the Oracle reading (OCI for dev; no Oracle Database in the app).
2. Confirm the Go proposals in §6.2 (framework, queue, monorepo, tests, CI), or change them.
3. Expo or bare React Native.
4. ADR-11 identity service.
5. Consent cache approach (decided in S-097 after measurement).
6. Why OCI for dev (cost or credits?), and whether the two-cloud cost in §6.3 is acceptable.
7. ADR-21 retention, scheduler-core option, ADR-27 to 29, and accepting the 15 stack-independent ADRs (§5 table, still pending).

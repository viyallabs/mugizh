# Mugizh (working name)

A healthcare app for India: patients find verified doctors, book and pay at the clinic, consult in person or by video, receive structured prescriptions and get reminders. It handles health data and prescribing, so **the law and patient safety come before speed**.

**Status (2026-11-17):** planning complete; backend foundation and first modules being built. The UI/UX is not final, so there are no apps yet. Nothing here has real patient data; everything outside production is synthetic.

## Read first

| What | Where |
|---|---|
| Rules for anyone (human or AI) changing this repo | [CLAUDE.md](CLAUDE.md) |
| Contracts: the rules code must follow | [contracts/](contracts/) |
| Build decisions made without the founder, for review | [docs/process/build-decisions-log.md](docs/process/build-decisions-log.md) |
| Architecture, modules, adapters, API standards, country packs | [docs/arch/](docs/arch/) |
| Security, privacy, clinical rules, payments, legal | [docs/security/](docs/security/), [docs/privacy/](docs/privacy/), [docs/clinical/](docs/clinical/), [docs/payments/](docs/payments/), [docs/legal/](docs/legal/) |
| Tests every Critical or High edge case needs | [docs/test/edge-case-test-catalogue.md](docs/test/edge-case-test-catalogue.md) |
| Backlog, statuses, compliance register | `Resources/healthcare-app-agile-roadmap.xlsx` |

## Repository layout

```
backend/            Core API and workers (Go modular monolith; conventions in backend/README.md)
scheduler-core/     Reminder scheduler: separate Go service, own database, no health data (K-ARC-02)
api/                OpenAPI 3.1 specs: one per module (api/core) and scheduler-core (api/scheduler)
config/             Country packs (config/country-packs/IN, synthetic XX) and the vendor register
contracts/          Rule files (change only by founder-approved ADR)
docs/               Planning, design, security, legal and process documents
deploy/local/       Local stack (PostgreSQL + PostGIS in Docker)
tools/              Dev image, contract-check helpers, code generation
apps/, packages/    Mobile and web apps (after the UI/UX is final)
infra/              Infrastructure as code (OpenTofu, S-093)
```

## Run it locally

Requirements: Docker. Go runs inside the pinned dev image (`tools/devimage`), so no local Go install is needed.

```sh
docker build -t mugizh-dev:1 tools/devimage   # once
bash deploy/local/up.sh                        # database, migrations, per-module roles → deploy/local/.env
tools/dev.sh backend go test ./...             # unit + integration tests (real PostgreSQL in containers)
tools/dev.sh scheduler-core go test ./...
```

`Taskfile.yml` wraps the same commands (`task ci` runs lint, tests and security scans). CI is `.gitlab-ci.yml`.

## Things that are deliberately not done yet

Staging and production refuse to start until the cloud key service, S3-compatible storage and SMS gateway adapters exist (fail closed). Electronic prescription signing stays blocked until the lawyer decides the signature method (N5). See the open items in the build decisions log.

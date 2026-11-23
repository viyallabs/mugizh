# Third-party dependencies register (decision D-13)

**Why:** CLAUDE.md §4 forbids adding a framework, library family, queue, cloud service or vendor beyond the decided stack without a record. This list is that record for code libraries. **Anything not listed here may not be imported** (checked by `backend/archtest`, S-092). Adding a line is a reviewed change; a new library family or vendor SDK needs an ADR.

**Pinned versions** live in `go.mod` / `go.sum` (K-SEC-06); scanning by `govulncheck`, Trivy and GitLab dependency scanning.

## Backend and scheduler-core (Go)

| Module path | Why | Stack decision |
|---|---|---|
| `github.com/go-chi/chi/v5` | HTTP router | ADR-03 |
| `github.com/oapi-codegen/runtime`, `github.com/oapi-codegen/nethttp-middleware`, `github.com/getkin/kin-openapi` | OpenAPI-first server code and request validation | ADR-03, K-ARC-06 |
| `github.com/jackc/pgx/v5` | PostgreSQL driver and pools | ADR-03 |
| `github.com/riverqueue/river` (+ `riverdriver/riverpgxv5`, `rivermigrate`, `rivertype`) | Job queue on PostgreSQL | ADR-07 (must pass S-068) |
| `github.com/google/uuid` | UUIDv7 identifiers | API standards §1 |
| `go.yaml.in/yaml/v3` | Country-pack and rule-file parsing | S-099 |
| `github.com/pquerna/otp` | TOTP (RFC 6238) for professional MFA | Proposed ADR-32 (D-04) |
| `github.com/teambition/rrule-go` | RFC 5545 recurrence in scheduler-core | K-ARC-04 |
| `go.opentelemetry.io/otel` (+ sdk, exporters) | Traces and metrics | ADR-03 |
| `golang.org/x/crypto` | HKDF and constant-time helpers, only inside `internal/platform/crypto` | encryption-design §10 |
| `github.com/testcontainers/testcontainers-go` (+ `modules/postgres`) | Integration tests only (`_test.go`, `internal/testkit`) | ADR-25 |

**Not allowed** (examples): ORMs, web frameworks other than chi, Redis clients (no Redis in the stack), any cloud SDK outside `internal/adapters/<kind>/<vendor>/` (dual-cloud rule ADR-31), analytics, ads or attribution SDKs (K-SEC-06 note, E-DP-25).

## Tools (not linked into binaries)

oapi-codegen v2.8.2, golangci-lint v2.15.0, gosec v2.30.0, govulncheck v1.9.0 (pinned in `tools/devimage/Dockerfile`); Spectral 6.15.0, Gitleaks 8.28.0, sqlc 1.31.1 (pinned container images).

## Apps (TypeScript)

Not started (decision D-12). `openapi-typescript` and `openapi-fetch` are planned (ADR-30).

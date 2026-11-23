# CI and contract checks (S-091, S-092)

**Contracts:** K-DOD-01, K-DOD-02 · **Last updated:** 2026-11-17

Every contract rule that a machine can check is checked on every push; a failing job blocks the merge (all jobs are `allow_failure: false`; set "Pipelines must succeed" on `main`). Run everything locally with `task ci` (needs Docker). The Go checks live in `backend/archtest/` (standard library only). Each rule has a test on the real tree and a **negative test** that feeds the same checker a known violation and fails if the rule stays silent. Rule scans exempt `backend/archtest/` itself (it has to name forbidden things) and `testdata/` folders; they fail closed if no source is found (`TestLoaderFindsSources`).

## Automated checks

| Rule / edge case | Check (file, test) | CI job |
|---|---|---|
| B4 / K-ARC-01 modules import only `modules/<other>/public` | `archtest/imports_test.go` `TestB4_ModuleImports` (+ `_Negative`) | `archtest` |
| B7 platform imports no modules, adapters or app | `imports_test.go` `TestB7_PlatformImports` | `archtest` |
| A1 / D-19 only `internal/app` and `cmd` import vendor adapters | `imports_test.go` `TestA1_VendorAdapters` | `archtest` |
| D-13 dependency allow-list (`docs/arch/dependencies.md`), testcontainers only in tests/testkit; ADR-31 cloud SDKs only in `internal/adapters/<kind>/<vendor>/`; E-DP-25 / TC-19 (no analytics, ads or attribution SDK in backend) | `imports_test.go` `TestD13_DependencyAllowList` | `archtest` |
| encryption-design §10.1 / K-DP-03 raw crypto only in platform/crypto, platform/keys, adapters/keys | `imports_test.go` `TestEncryptionDesign10_CryptoImports`; also depguard in `.golangci.yml` | `archtest`, `golangci-lint` |
| B10 / K-ARC-02 scheduler-core never depends on backend | `imports_test.go` `TestB10_SchedulerCoreIsolated` | `archtest` |
| K-GLB-01 no country constants in shipped code | `archtest/literals_test.go` `TestKGLB01_NoCountryLiterals` | `archtest` |
| K-DP-05 no Aadhaar / UIDAI / vid in code (non-test), tags, strings, SQL | `literals_test.go` `TestKDP05_NoAadhaar` | `archtest` |
| K-PAY-01 no card, CVV, UPI PIN, bank account fields | `literals_test.go` `TestKPAY01_NoCardData` | `archtest` |
| K-DP-04 / E-DP-13 / TC-10 no std `log`, `fmt.Print*`, `println`; slog messages are constant strings (static half; the scrubber runtime half is `platform/logging` + `platform/scrub` tests) | `literals_test.go` `TestKDP04_LoggingDiscipline` | `archtest` |
| Release policy §5 / CLAUDE.md §5.5 consent, audit, encryption, prescribing, rules engine cannot be flagged off | `literals_test.go` `TestReleasePolicy5_NonFlaggableControls` | `archtest` |
| B1 migrations touch only their own schema | `archtest/migrations_test.go` `TestB1_MigrationSchemas` | `archtest` |
| Every migration has `.up.sql` and `.down.sql` | `migrations_test.go` `TestMigrations_UpAndDownPairs` | `archtest` |
| K-ARC-06 / S-038 API standards; K-ARC-02 + E-SEC-08 / TC-61 scheduler spec holds no health fields | `docs/arch/openapi-lint.spectral.yaml`, `api/.spectral.yaml`, `tools/ci/spectral-fixtures.sh` (proves the ruleset on the fixtures, then lints `api/core/*.yaml`, `api/scheduler/*.yaml`) | `spectral` |
| K-SEC-05 no secrets in code | `.gitleaks.toml`; `tools/ci/planted-secret-test.sh` proves a planted AWS key and private key are caught | `gitleaks`, GitLab Secret Detection |
| K-SEC-06 dependencies pinned and scanned | `go.mod`/`go.sum` pins; `govulncheck`; Trivy filesystem scan fails on CRITICAL; GitLab Dependency Scanning | `govulncheck`, `trivy-fs` |
| K-SEC-07 / K-DOD-01 SAST | `gosec` (high severity) ; golangci-lint (`gosec`, `staticcheck`, `errorlint`, `bodyclose`, `errcheck`); GitLab SAST | `gosec`, `golangci-lint`, SAST template |
| K-DOD-01 formatting, vet, tests | gofmt, `go vet`, `go test -short` (unit), `go test` with Docker-in-Docker (integration) | `gofmt`, `vet`, `unit`, `integration` |
| E-SEC-11 / TC-64 (build side only) the dev image is built from a pinned Dockerfile on `main` | `tools/devimage/Dockerfile` | `devimage` |

## Review-only (cannot be automated)

These stay in the Claude contract review and human review (`docs/process/code-review-prompt-template.md`).

| Rule | Why it cannot be a check |
|---|---|
| K-DP-01 India-region infrastructure and vendors | A property of deployed infrastructure and contracts, not of source. Checked in IaC review (S-093) and the vendor register. |
| K-DP-02 every non-patient read goes through `records.authorize` and writes an audit event | Needs data-flow understanding; only the import and flag aspects are mechanical. Covered by TC tests for each read path. |
| K-DP-03 the right fields are encrypted | Which field is sensitive is a judgement; only where crypto may be imported is mechanical. |
| K-DP-06 data-inventory row for a new personal-data field | Needs a human to decide what is personal data (migrations are listed in the PR for the reviewer). |
| K-DP-07, K-DP-08, K-DP-09, K-DP-10 | Erasure reach, vendor register, notification wording and adults-only are behaviour and process checked by TC tests and review. |
| K-CS-01 to K-CS-07 | Clinical safety, rules-engine behaviour and doctor-approved wording need a registered doctor (K-CS-07) and TC tests, not static checks. |
| K-PAY-02 to K-PAY-05, K-MSG-01 to K-MSG-03 | Behavioural (idempotency, ledger, refund policy, DLT templates, quiet hours); tested by TC tests. Only the card-data names (K-PAY-01) are static. |
| K-SEC-01 to K-SEC-04, K-SEC-08 | Runtime and operational (log retention, runbook, MFA, deny-by-default, just-in-time grants). K-SEC-04 deny-by-default is tested per endpoint. |
| B2, B3, B5, B6, B8, B9 | Database grants and foreign keys across schemas are infrastructure and tests (B2); B5/B6 and cycle design are review. Package import cycles are already a Go compile error. |
| K-ARC-03 vendors only through adapters | Mechanical part is A1 and D-13 above; judging that a vendor call is wrapped properly is review. |
| K-ARC-04, K-ARC-05, K-GLB-02, K-GLB-03 | Time-zone, FHIR and translation-key conventions need semantic judgement; partly covered by K-GLB-01 literals. |
| K-DOD-02 PR names the story and K-IDs; K-DOD-03 never edit `/contracts` | PR description and review. (A future merge-request template check or CODEOWNERS on `/contracts` can enforce K-DOD-03; not part of S-092.) |
| TC-19 app-build half, E-DP-25 | Mobile and web SDK checks start when the apps exist. Event schema review is manual. |
| TC-64 admin sign-in and signed-build promotion | Security tests and release pipeline (S-128, S-094), not this story. |

## Known limits of the automated checks

- Aadhaar, vid and card-name checks skip comments by design. The Aadhaar identifier and string check skips `_test.go` files, because the scrubber tests must contain synthetic Aadhaar-like values; SQL files are never skipped.
- The logging check recognises logger receivers by name (contains `log`, or `l`); a logger held in a variable with another name would escape it. golangci-lint and review cover the gap.
- B1 treats any `<schema>.` reference to another module's schema in a migration as a violation, including `audit.audit_outbox`. The INSERT grant on the shared outbox (B2) belongs in infrastructure, not module migrations. If a module migration needs it, that is a decision to record, not a silent exception.
- Spectral does not apply `overrides` of an extended ruleset, so `api/.spectral.yaml` repeats the scheduler-core override.

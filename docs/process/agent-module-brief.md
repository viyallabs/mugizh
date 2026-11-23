# Brief for agents building a backend module

**Who this is for:** an AI agent (or developer) building one module of the Core API. Read it fully before writing code. Opus (the orchestrator) reviews and wires your work.

## Read first
1. `CLAUDE.md` (rules; §3 non-negotiables; §5 never-do list).
2. `backend/README.md` (layout and code rules — follow exactly).
3. The contracts your story cites (`contracts/**`), `docs/arch/module-boundaries-and-domain-model.md` (§1 rules B1–B10, your module's rows in §2–§4), `docs/arch/api-standards.md`, `docs/arch/adapter-interfaces.md` (if you call a vendor), your module's section of `docs/data-inventory-v1.0.md` §4, the TC tests in `docs/test/edge-case-test-catalogue.md`, and `docs/process/build-decisions-log.md`.

## Copy these patterns (reference implementations)
- Module layout: `backend/internal/modules/identity/` and `.../records/` — `migrations/` (embedded, `NNNN_name.up.sql` + `.down.sql`), root package with `Source()` and `New(...)`, `public/` (the only package other modules may import), `httpapi/` (`gen.go` generated from `api/core/<module>.v1.yaml` + `handlers.go` implementing `StrictServerInterface`; return `*problem.Problem` errors, never write responses by hand).
- Audit inside the same transaction: `auditpublic.Record(ctx, tx, ...)` (`backend/internal/modules/audit/public`). References only.
- Events: copy the `event_outbox` + `processed_event` DDL from `backend/internal/platform/outbox/outbox.go` (`TableDDL`) into your first migration; emit with `outbox.Emit(ctx, tx, ...)` (refs and codes only); consume with a handler `func(ctx, outbox.Event) error` that uses `outbox.Once(...)` for idempotency. Export the handler; Opus subscribes it.
- Encryption: `platform/crypto` (`SealPatient`, `SealCare`, `SealVerification`, `Open`), typed `crypto.Sealed[...]` columns as `bytea`.
- Reads of a patient's data by anyone but the patient: call the records gate (`records/public.Gate.Authorize`) and refuse on deny (404 when `Decision.Hide`, else 403 `consent.required`).
- Tests: `backend/internal/testkit` (real PostgreSQL + PostGIS; `TestMain` calls `testkit.Main`); crypto test set-up in `backend/internal/modules/identity/identity_test.go`; HTTP tests in `backend/internal/app/app_test.go` (build a router with `app.BuildPolicies(yourSpec)`, `app.PolicyMiddleware`, `app.NewRouter`, `app.UseStrictJSON()`).

## How to run (native Go is blocked on this machine)
From the repo root with Bash:
- `tools/dev.sh backend go test ./internal/modules/<module>/...` (also `go vet`, `gofmt -l`)
- Code generation for your spec only: `tools/dev.sh . sh tools/gen-api.sh <module>`
- Spectral: `MSYS_NO_PATHCONV=1 docker run --rm -v "$(pwd -W):/w" -w /w stoplight/spectral:6.15.0 lint -r api/.spectral.yaml api/core/<module>.v1.yaml` — 0 errors required.
- Contract checks: `tools/dev.sh backend go test ./archtest/...` must still pass.

## Boundaries (hard)
- Edit only: `backend/internal/modules/<your module>/**`, `api/core/<your module>.v1.yaml`, adapter port/fake folders you are told to own, and your module's rows in `docs/data-inventory-v1.0.md` (K-DP-06: every new personal-data field gets a row in the same change).
- Do **not** edit `go.mod`/`go.sum`, `backend/internal/app/**`, `backend/internal/platform/**`, other modules, `contracts/**`, `CLAUDE.md`, or the country packs. If you need something there, define a consumer-side interface in your module, use a fake in tests, and report it.
- Logging: only keys already in `backend/internal/platform/logging/logging.go`; messages are constant event names.
- No country literals in non-test code (phone codes, currency codes, time zones, emergency numbers, drug schedule names); read them from the country pack (`modules/config/public`) passed in through your constructor.
- No Aadhaar fields; no card/UPI/bank data.
- Synthetic test data only. Test names carry the TC ID (`TestTC22...`).
- Write the tests first, then the code. Fail closed on every error path that touches safety, money, consent or privacy.

## Report back (concise)
Files created; public interface you expose; events you emit/consume (with exported handler names); tests and results; every assumption or spec ambiguity you resolved (with the doc reference); anything you need Opus to wire or add (deps, platform changes, other modules' interfaces); anything left undone.

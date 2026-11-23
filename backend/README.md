# Backend (Core API, workers) — conventions

**Stories:** S-090 (layout), S-101 (test harness) · **Read first:** `/CLAUDE.md`, `/contracts/`, `docs/arch/module-boundaries-and-domain-model.md`, `docs/arch/api-standards.md`, `docs/process/build-decisions-log.md`.

## Layout

```
backend/
  cmd/api, cmd/worker, cmd/migrate   entry points (composition only)
  internal/app/                      composition root: wires modules, adapters, HTTP server
  internal/platform/                 shared utilities and infra wrappers (B7): ids, problem,
                                     logging, scrub, db, migrate, dbschema, blobstore, httpx,
                                     crypto, keys, outbox, idempotency. MUST NOT import modules.
  internal/modules/<name>/           one folder per module (module map S-036)
      public/                        the ONLY package other modules may import (B4)
      migrations/                    NNNN_name.up.sql + .down.sql, own schema only (B1)
      *.go                           module internals (constructor New, workers, HTTP handlers)
  internal/adapters/<kind>/          port interface + common error model for a provider kind
  internal/adapters/<kind>/<vendor>/ vendor implementation; only internal/app and cmd import it (A1)
  internal/testkit/                  integration-test harness (real PostgreSQL + PostGIS)
  archtest/                          contract checks as Go tests (S-092)
```

## Rules that code must follow (summary; contracts are authoritative)

1. **Module boundaries (K-ARC-01):** a module imports another module only through `internal/modules/<other>/public`. No cross-schema SQL. Side effects across modules go through the module's own `event_outbox` table (decision D-07), never a shared transaction.
2. **Database:** each module uses its own pool (`db.Pools.For("<module>")`), whose role can touch only its schema plus `INSERT` on `audit.audit_outbox`. Use `db.InTx` for transactions; it retries serialization failures, so the function must be safe to re-run.
3. **Audit (K-DP-02, K-SEC-01):** call `auditpublic.Record(ctx, tx, event)` inside the same transaction as the change, and **before returning data** on reads of patient data. References only, never content.
4. **Logging (K-DP-04):** use the `*slog.Logger` from `platform/logging`. The message is an event name (`"booking.hold_expired"`), attributes use allow-listed keys only (see `logging.go`). Never log request bodies, names, phone numbers, OTPs, tokens or clinical text. `fmt.Print*` and the std `log` package are banned outside `cmd/`.
5. **Errors to clients:** return `*problem.Problem` with a stable `code` (`<module>.<reason>`); `detail` never contains personal or internal data. Other people's resources return **404**, not 403 (K-SEC-04).
6. **Encryption (K-DP-03):** sensitive fields go through `platform/crypto` only. No direct use of `crypto/aes`, `crypto/cipher` or KMS SDKs elsewhere.
7. **No country values in code (K-GLB-01):** phone prefixes, emergency numbers, currency, time zones, drug lists, ID validators in use: all from the country pack (`modules/config/public`).
8. **Aadhaar (K-DP-05):** never collect, store, log or display; reject Verhoeff-valid input with `scrub.ContainsProhibitedID`.
9. **Time (K-ARC-04):** store `timestamptz` in UTC; keep an IANA zone column where local time matters.
10. **IDs:** `ids.New()` (UUIDv7). Never sequential IDs in APIs.
11. **Tests first:** every story writes its TC-xx tests (docs/test/edge-case-test-catalogue.md) before the code; test names carry the TC ID (`TestTC22...`). Synthetic data only.
12. **Dependencies:** only those in `docs/arch/dependencies.md`. Do not edit `go.mod` to add one without updating that list (CLAUDE.md §4).

## Running

The founder's Windows laptop blocks unsigned Go tool binaries (Application Control), so every Go command runs in the pinned dev image:

```sh
tools/dev.sh backend go test ./...          # unit + integration (needs Docker)
tools/dev.sh backend go test -short ./...   # unit only
tools/dev.sh backend go vet ./...
```

Integration tests use `testkit.New(t, testkit.Spec{Sources: ...})` and each test package needs `func TestMain(m *testing.M) { testkit.Main(m) }`.

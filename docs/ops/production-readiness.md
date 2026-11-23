# Production readiness: services, alerts, runbooks, rollback (S-186)

**Story:** S-186 · **Contracts:** K-SEC-01, K-SEC-02, K-DP-04, K-DP-07 · **Date:** 2026-11-23
**Status:** In Progress. This is the backend half, written from what the code does today. The review meeting itself, on-call names and the staging dry run wait for staging (S-093), the VAPT (S-183) and the people in [incident-response-runbook.md](../security/incident-response-runbook.md) section 2.

**Who this is for:** the on-call engineer and the founder at the go-live review.

## 1. Services and health

| Service | Entry | Health | Start-up refuses to run when |
|---|---|---|---|
| Core API | `backend/cmd/api` | `GET /health/live` (process), `GET /health/ready` (every module pool and the key store) | Any of these is true: settings missing; country pack checksum wrong or draft pack in production; a vendor without a processor agreement outside dev; key-store tombstone replay or erasure-journal replay fails; notice versions missing |
| Core worker | `backend/cmd/worker` | Logs `worker.*`; alerts | Same start-up checks as the API (same `BuildCore`) |
| scheduler-core | `scheduler-core/cmd/*` | Own health endpoints | Signed callback secret missing |
| Migrations | `backend/cmd/migrate up` | Exit code | Any migration fails; each module's migrations touch only its own schema (B1) |

All three use the same image per release. Configuration comes only from environment variables and the reviewed country pack. There is no runtime config screen (D-58).

## 2. Alerts (route every `ops.alert` log line to on-call)

Every alert is one ERROR log line with `msg = "ops.alert"` and `reason = <code>` (D-76), at most once a minute per code per process. Each alert is also counted as the metric `ops.alerts{reason}` (S-095). Rule for the log store: **page on any `ops.alert`**; group by `reason`.

| Code | Means | First action (runbook section 3) |
|---|---|---|
| `crypto.key_unavailable` | The key service (KMS or key store) cannot be reached; requests answer 503 | R1 |
| `audit.chain_broken` | Audit hash chain or a write-once anchor does not verify (possible tampering, TM-38) | R2, **and treat as a possible incident** (incident-response-runbook section 3) |
| `audit.outbox_backlog` | Audit events are not being chained (worker stopped or slow) | R3 |
| `access.break_glass` | A staff member used break-glass access to patient data | R4 |
| `identity.sms_cap_reached` | The daily OTP SMS cap tripped (SMS pumping or a surge, TM-02); new codes are refused | R5 |
| `identity.refresh_reuse` | A refresh token was used twice: a stolen token, so the session was revoked (TM-05) | R6 |
| `identity.mfa_failures` | A professional session failed MFA 5 times and was revoked (TM-04) | R6 |
| `notifications.dead_letter.<reason>` | A message could not be delivered after retries and fallback (TM-25) | R7 |
| `payments.webhook_rejected` | A payment webhook failed signature, amount or order checks (TM-09) | R8 |
| `payments.reconciliation_mismatch` | Daily reconciliation found a difference with the aggregator | R8 |
| `payments.refund_failed`, `payments.refund_not_possible`, `payments.refund_shortfall` | A refund could not be sent or the clinic account cannot cover it | R8 |
| `payments.order_failed`, `payments.vendor_auth_failed` | Checkout cannot be created; aggregator credentials rejected | R8 |
| `payments.document_profile_missing` | A payment was captured for a clinic with no document profile (no receipt issued) | R8 |
| `alert.invalid_code` | A caller passed an invalid alert code (a bug, not an incident) | Fix the caller |

**Also alert on these** (log-store and infra rules, not code):
- 5xx rate above 1% for 5 minutes;
- `http.server.request.duration` p95 above the targets in [performance-targets.md](../arch/performance-targets.md);
- `/health/ready` failing;
- worker log silence for 10 minutes;
- certificate expiry within 21 days;
- NTP drift above 1 second (CERT-In).

## 3. Runbooks

- **R1 Key service unavailable.**
  1. Check the KMS and key-store database health.
  2. Do not add a plaintext fallback or disable encryption (CLAUDE.md section 5.5).
  3. The API keeps answering 503 with Retry-After until keys are reachable.
  4. After recovery, watch for `crypto.*` errors.
- **R2 Audit chain broken.**
  1. Do not repair rows.
  2. Freeze the audit database snapshot, compare with the write-once anchors and find the first broken sequence.
  3. Open an incident; the 6-hour CERT-In clock may start.
- **R3 Audit backlog.** Check the worker is running and the audit role can write. The backlog clears on its own once the worker runs.
- **R4 Break-glass used.**
  1. Confirm the written justification arrives within 24 hours.
  2. The security owner reviews the access in the access log and the monthly review.
- **R5 SMS cap reached.**
  1. Look for SMS pumping (many numbers, few countries or prefixes, one network).
  2. Raise the cap only after ruling that out; the cap is config.
  3. Sign-in for existing devices still works (refresh tokens).
- **R6 Token reuse or MFA failures.**
  1. The session is already revoked.
  2. If one account repeats, contact the user through the phone on file.
  3. For professionals, consider suspending the account pending a call.
- **R7 Dead letters.**
  1. Check the vendor's status.
  2. Critical reminders already fell back to SMS where allowed.
  3. After the outage, re-drive is not automatic: confirm with the clinical lead before resending anything time-sensitive.
- **R8 Payments.**
  1. Never edit the ledger (append-only).
  2. Compare with the aggregator's dashboard.
  3. A rejected webhook with a valid-looking signature from an unknown address is a possible attack (TM-09).
  4. Refunds follow only the versioned policy (K-PAY-05).

## 4. Deploy and rollback

1. **Forward-only migrations.** The `.down.sql` files are for development only and must never run against staging or production. Every migration must be safe for the previous release to run against (expand, then contract in a later release).
2. **Order:** run migrations, then deploy the worker, then the API (same image tag). scheduler-core deploys on its own and keeps its API stable (K-ARC-06).
3. **Rollback:** redeploy the previous image tag. Migrations stay; they are written to be backward compatible. If a migration itself is wrong, fix it forward with a new migration.
4. **Feature flags** (pack `features`) never switch off consent, the rules engine, the prescribing gate, audit or encryption (enforced by `config.ValidateFlagName`). A change to the pack goes through a reviewed PR and its checksum.
5. **Before every production deploy:**
   - tests and scans are green;
   - the staging smoke has passed (S-174 journeys, `task load:smoke`, `task dast` on staging);
   - the change log names the stories.
6. **Restore:** follow [backup-and-restore.md](../security/backup-and-restore.md) section 3. Key tombstones and the erasure journal are replayed automatically at start-up (D-74).

## 5. Before the go-live review (checklist)

- [ ] On-call rota and paging named (incident-response-runbook section 2) [FILL]
- [ ] Log-store rule: page on `ops.alert`; the other rules in section 2
- [ ] Telemetry collector in India (`MUGIZH_OTLP_ENDPOINT`, R3)
- [ ] Staging dry run of deploy and rollback (S-094)
- [ ] DR drill on AWS (S-185) and incident drill (S-184)
- [ ] VAPT findings closed (S-183)
- [ ] Founder approves the decisions log items marked [CONFIRM] that affect launch

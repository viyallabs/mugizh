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

### Deploying (S-094)

**Status: written, never run** (no AWS accounts, blocker V1). Files: `tools/ci/deploy.sh`, `tools/ci/tofu.sh`, `tools/ci/check-gitlab-protection.sh`, `tools/deployimage/Dockerfile`, the `deploy` stage of `.gitlab-ci.yml`, `infra/modules/ci_access`.

**What a deploy does** (`deploy.sh <env> <tag>`; the tag is the commit short SHA that `images` built on `main`):

1. Assumes the environment's deploy role with the job's GitLab OIDC token (regional STS, `ap-south-1`). No stored keys. The role name must be exactly `mugizh-<env>-gitlab-deploy`.
2. Copies `core` and `scheduler-core` from the GitLab registry to ECR **by digest** with `crane`. Staging copies exactly the digests the `images` job wrote to its dotenv artifact (`CORE_IMAGE_DIGEST`, `SCHEDULER_CORE_IMAGE_DIGEST`) and fails if they are absent. Production resolves the release tag in the GitLab registry (precondition 4 below). A re-run finds the same digest and copies nothing; a tag that exists in ECR with another digest fails the job.
3. Registers new revisions of `migrate` and `scheduler-migrate` (only the image changes, by digest), runs them, and fails the deploy unless every container exits 0. Services are untouched until then.
4. Updates `worker`, then `scheduler-core`, then `api`, each waiting until stable **and** on the new revision (the ECS circuit breaker can roll back by itself, and a rolled-back service also reports "stable").
5. Smoke check: `GET <API_PUBLIC_URL>/health/ready` must answer 200 three times in a row within 5 minutes.
6. Any failure after step 4 starts rolls the services back to the revisions recorded before the deploy. It first assumes the role again from the same token, so an expired session cannot block it, and it ignores TERM and INT while rolling back. It waits for the services to be stable and exits non-zero. Migrations are never rolled back.

Infrastructure is **never** changed by a deploy. `tofu apply` is the separate manual jobs `tofu-plan-<env>` then `tofu-apply-<env>` (they apply the saved plan artifact only, with the separate infra-apply role). All jobs of one environment, deploy and tofu, share one `resource_group` (`staging`, `production`), so they never run at the same time.

#### What actually protects production (read this)

AWS can check only the OIDC `aud` and `sub` claims. It cannot check GitLab's `ref_protected` or environment claims. The audience is set in `.gitlab-ci.yml`, is not secret and is **not a control** (it only stops a token meant for another service from being accepted). `sub` names the project and the tag, so **the control is GitLab**: who can create a tag matching `v*`, who can push to `main`, and who must approve the production environments. Anyone who can create a `v*` tag in this project, or merge to `main`, can obtain the production deploy role and the infra-apply role. The preconditions below are therefore mandatory.

**Mandatory preconditions (numbered; all true before the first production deploy, and re-checked after any change)**

1. **Protected tag `v*`**: create allowed for **Maintainers only** (no Developers, no extra users or groups).
2. **Protected branch `main`**: **no direct push** (allowed to push: No one), force push off, and **at least one merge request approval required** (approval rule).
3. **Protected environments `production` and `production-infra`** with **required approval** (approvers are the release approvers only). The staging jobs run from `main` only.
4. **Immutable release images in the GitLab registry**: enable protected container-repository tags (tag rules) for `core:*` and `scheduler-core:*` so only Maintainers can push or overwrite them and nobody can retag a digest afterwards [VALIDATE the GitLab version supports protected or immutable container tags]. Production resolves the release tag (tag pipelines cannot read the `images` artifact of `main`), so a mutable tag would let someone change what is deployed. Make release tags on a commit of `main` that already has its image. Keep registry cleanup policies from deleting those tags [VALIDATE].
5. **Run `tools/ci/check-gitlab-protection.sh <project-id>`** with a maintainer's read-only personal token (`GITLAB_TOKEN`, scope `read_api`). It checks 1 to 3 through the API (`protected_tags`, `protected_branches`, `approval_rules`, `protected_environments`) and exits non-zero if any is missing. It cannot check 4; confirm that by hand.
6. **The approver of every production deploy compares digests.** The `deploy-production` log prints the two image digests. The approver compares them with the digests printed by the `deploy-staging` job for the same commit (cross-account, so no script can do it) and approves only if they are equal.

Residual risk, accepted and explicit: a compromised Maintainer account, or a GitLab administrator, can still create a release tag, approve, and so reach production; the alerts below detect, they do not prevent. The infra-apply role is AdministratorAccess in its account (below). The deploy role can push images to the two ECR repositories and register task definitions that use the data-access task roles, because IAM cannot bind an image to a role. The mitigations are the alerts and the digest comparison.

#### Infra-apply role and alerts (`modules/ci_access`)

- **Role `mugizh-<env>-gitlab-infra-apply`**: staging trusts `ref_type:branch:ref:main` only; production trusts the protected tag patterns `v0*` to `v9*` only; maximum session 1 hour. Permission: `AdministratorAccess`, accepted **only** because the organisation SCP (`infra/org`: region lock R1 and the audit and key denies) is attached to the OU of the account, and because every assumption raises an alert. Apply `infra/org` before anything else. **[CONFIRM]** with the founder; narrow it later.
- **Alerts to the SNS alerts topic** (EventBridge rules on CloudTrail management events in `ap-south-1`; the message holds only the event name, caller ARN and time): (a) the infra role was assumed (expected only during a manual tofu job); (b) a task definition was registered with any image outside `<account>.dkr.ecr.ap-south-1.amazonaws.com/mugizh-<env>/`; (c) `RegisterTaskDefinition` or `RunTask` by any principal other than the deploy and infra roles. `RunTask` carries no image, so it is covered by (c). Not caught: a task definition registered by the deploy role that uses an image from the environment's own repositories, and calls by a principal with no session issuer (root). [VALIDATE] all patterns against real events at the first deploy (checklist step 8) and subscribe an on-call endpoint (`alert_endpoint`).
- The deploy role holds only what the script calls: register, describe and tag task definitions, `RunTask` on the two migrate families, `DescribeTasks`, update and describe the three services, `PassRole` for the task and execution roles, ECR push to the two repositories.

#### GitLab setup (once)

- Preconditions 1 to 5 above. Create the environments `staging`, `production`, `staging-infra`, `production-infra`.
- Run the `deployimage` job once on `main`. It scans the image with Trivy (a HIGH or CRITICAL finding with a fix fails the job and nothing is pushed), pushes `deploy:1` and prints `DEPLOY_IMAGE_DIGEST=sha256:...`. Create the CI variable `DEPLOY_IMAGE_DIGEST` with that `sha256:...` value (protected). The deploy and tofu jobs run the image `deploy@$DEPLOY_IMAGE_DIGEST` and the scripts refuse to run without it. Update it only after reviewing a new build.
- Trivy gate (D-91): the tool image uses OpenTofu 1.13.9 (newest line, release at least two weeks old), a Debian snapshot at least two weeks old with `apt-get upgrade` (no Debian finding is ever ignored), and `tools/deployimage/.trivyignore` for the HIGH findings with fixes that remain inside upstream binaries we cannot rebuild (crane, OpenTofu dependencies, the AWS CLI bundle). Every entry expires 2026-12-23: after that the `deployimage` job fails until the tool is bumped or the entry is re-reviewed. The job passes `--ignorefile tools/deployimage/.trivyignore`.
- The tool image's apt packages come from a dated Debian snapshot (`SNAPSHOT` in the Dockerfile), so the exact pins stay installable.
- Accepted Low: the other job images (docker, trivy, gitleaks and so on) are pinned by version tag, not by digest.

**CI variables to create** (Settings, CI/CD, Variables; set **Protected**, scope each by environment, so the same name holds a different value in `staging` and `production`)

| Variable | Scope | Value |
|---|---|---|
| `AWS_OIDC_AUDIENCE` | all | Audience of the AWS OIDC provider (`audience` in `modules/ci_access`, default `https://gitlab.com`). Not a secret, not a control |
| `DEPLOY_IMAGE_DIGEST` | all | `sha256:...` printed by `deployimage` |
| `DEPLOY_ROLE_ARN` | staging, production | `deploy_role_arn` output (exactly `mugizh-<env>-gitlab-deploy`) |
| `DEPLOY_SUBNET_IDS` | staging, production | Comma-separated `app_subnet_ids` |
| `DEPLOY_APP_SG_ID` | staging, production | `app_security_group_id` (migrate task) |
| `DEPLOY_SCHEDULER_SG_ID` | staging, production | `scheduler_security_group_id` (scheduler-migrate task) |
| `API_PUBLIC_URL` | staging, production | `https://<api host>`, no path, no trailing slash (the `api_public_url` tfvar) |
| `AWS_REGION` | optional | Default `ap-south-1`. The cluster, task families and ECR path are fixed as `mugizh-<env>` |
| `INFRA_ROLE_ARN` | staging-infra, production-infra | `infra_role_arn` output (exactly `mugizh-<env>-gitlab-infra-apply`) |
| `TF_STATE_BUCKET`, `TF_STATE_KMS_KEY_ID` | staging-infra, production-infra | The state bucket and key from the bootstrap |
| `TFVARS_FILE` (type **File**) | staging-infra, production-infra | The environment's `terraform.tfvars` (no secrets belong in it) |

`DEPLOY_ROLE_ARN`, `INFRA_ROLE_ARN`, `DEPLOY_SUBNET_IDS`, `DEPLOY_APP_SG_ID` and `DEPLOY_SCHEDULER_SG_ID` are printed by `tofu output -json` (`deploy_ci_variables`, `infra_role_arn`) in each environment root.

**First-run checklist** (preconditions first)

1. Preconditions 1 to 5 of "What actually protects production" are **mandatory**, with `check-gitlab-protection.sh` passing. Do not continue otherwise.
2. AWS accounts, `infra/org` (SCP and CloudTrail), OIDC thumbprint, state bucket, certificate, DNS: infra/README.md bootstrap steps 0 to 4. The first `tofu apply` of each environment is by hand (the infra role does not exist before it); afterwards use the manual tofu jobs.
3. The task definitions that OpenTofu creates point at `image_tag` from the tfvars. Copy that image into the environment's ECR first (`crane copy`, by digest), or accept that the services fail until the first deploy.
4. Run `deployimage`, create `DEPLOY_IMAGE_DIGEST` and the other variables. Subscribe on-call to the alerts topic.
5. Populate the Secrets Manager secrets (infra/README.md, "What the migrate jobs do").
6. Run `deploy-staging` (manual) from a `main` pipeline. Read the log: digests equal the `images` job's, migrations with exit code 0, services stable, smoke passed.
7. Break test on staging: an unset `CORE_IMAGE_DIGEST` (must fail before any change) and a build whose `/health/ready` fails (must roll back).
8. [VALIDATE] the GitLab `sub` claim format, the id_token lifetime (the rollback re-assumes the role from the same token, which works only while the token has not expired), and that the three alert rules fire on a test registration with a foreign image.
9. Then make `deploy-staging` automatic (remove `when: manual` in `.gitlab-ci.yml`).

**Rollback**

- Automatic: a failed migration stops the deploy before any service changes. A failed rollout, a service not on the new revision, or a failed smoke check rolls the touched services back to their previous revisions (the job log lists them).
- If the automatic rollback itself fails, the log says `MANUAL INTERVENTION REQUIRED` with the previous revisions: run `aws ecs update-service --cluster <cluster> --service <name> --task-definition <family>:<revision>` for each service (api, scheduler-core, worker) under break-glass access, then `aws ecs wait services-stable`.
- Later rollback of a released version: re-run `deploy-production` from the previous release tag (same procedure, the previous image digest). Migrations stay (forward-only); a wrong migration is fixed with a new migration.

## 5. Before the go-live review (checklist)

- [ ] On-call rota and paging named (incident-response-runbook section 2) [FILL]
- [ ] Log-store rule: page on `ops.alert`; the other rules in section 2
- [ ] Telemetry collector in India (`MUGIZH_OTLP_ENDPOINT`, R3)
- [ ] Staging dry run of deploy and rollback (S-094)
- [ ] DR drill on AWS (S-185) and incident drill (S-184)
- [ ] VAPT findings closed (S-183)
- [ ] Founder approves the decisions log items marked [CONFIRM] that affect launch

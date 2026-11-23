# Infrastructure as code (S-093, AWS part)

OpenTofu for **staging, production and DR on AWS India regions**: primary Mumbai `ap-south-1`, DR Hyderabad `ap-south-2`. Design: [docs/arch/infrastructure-and-environments.md](../docs/arch/infrastructure-and-environments.md). Contracts: K-DP-01, K-DP-03, K-DP-07, K-SEC-01, K-SEC-05, K-ARC-03.

**Status: validated, never applied.** There are no AWS accounts yet (blocker V1). Nothing here has been planned or applied. OCI dev (S-093 part 2, also never applied) is in [oci/README.md](oci/README.md).

## Layout

```
infra/
  org/                  Service Control Policy (R1), attached to the staging and production OUs
  envs/staging/         one AWS account; single NAT; no egress firewall by default; Multi-AZ off
  envs/production/      one AWS account; primary in ap-south-1 + DR copy in ap-south-2 (aliased provider "dr")
  modules/network/      VPC, 2 AZs, public / nat / (firewall) / app / data subnets, endpoints, flow logs, egress firewall
  modules/keys/         KMS multi-region data key + separate key-store backup key (replicas for DR)
  modules/storage/      blobs, worm (Object Lock COMPLIANCE), logs buckets; cross-region replication
  modules/database/     RDS PostgreSQL 17: core and key store as separate instances
  modules/compute/      ECS Fargate (api, worker, scheduler-core, migrate), ECR, Secrets Manager shells, log groups
  modules/edge/         ALB (TLS 1.2+ only) + regional WAF
  modules/ci_access/    GitLab OIDC provider and deploy role (ADR-10)
  modules/guardduty/    GuardDuty detector (instantiated in both India regions per env)
  org/cloudtrail.tf     organisation CloudTrail, all regions, Object Lock bucket in ap-south-1
  modules/observability/ alarms, ops.alert metric filter, SNS topic
  oci/envs/dev/        OCI dev (Mumbai), separate OpenTofu root: see oci/README.md
```

Pinned: OpenTofu `>= 1.10` (S3 native state locking, `use_lockfile`, needs 1.10; the brief said 1.8, see "Decisions beyond the brief"), AWS provider `5.100.0`. Lock files (`.terraform.lock.hcl`) are committed; `.terraform/` is ignored.

Every resource gets `project`, `env`, `data_class`, `owner` through provider `default_tags` (staging `synthetic`, production `SH-C`).

## Bootstrap order

0. **Enable the opt-in region `ap-south-2` (Hyderabad)** in the production account, and in the staging account (staging creates a GuardDuty detector there), before the first apply. Use regional STS endpoints (`AWS_STS_REGIONAL_ENDPOINTS=regional`, `sts.ap-south-1.amazonaws.com`), because credentials from the global STS endpoint are not valid in opt-in regions. Enable CloudTrail trusted access in AWS Organizations (`cloudtrail.amazonaws.com`) for the organisation trail.
1. **State buckets** (by hand, once per account): an S3 bucket in `ap-south-1`, versioned, private, SSE-KMS, TLS-only. Put its name and KMS key in the `backend "s3"` blocks (`[FILL]`). Locking is `use_lockfile = true`, no DynamoDB table.
2. **`infra/org/`** from the management account, **before anything else**: create the OUs, then `tofu apply` with `staging_ou_id` and `production_ou_id`. This turns on R1 and creates the organisation CloudTrail and its Object Lock bucket (365 days, COMPLIANCE, irreversible).
3. **`envs/staging`**, then **`envs/production`** (each in its own account). Copy `terraform.tfvars.example` to `terraform.tfvars` (ignored by git) and fill it. No secrets belong in it.
4. Validate the ACM certificate (regional, in the primary region) and create the DNS record to the ALB (`alb_dns_name` output) by hand. Route 53 is not managed here. `api_public_url` is that host name.
5. Run the **migrate** and **scheduler-migrate** tasks once (see "What the migrate jobs do"), then populate the secrets listed in the `secrets_to_populate` output.
6. **Mandatory before first use of the production deploy or infra roles:** in GitLab, protect the tag pattern `v*` (Maintainers only), protect `main` (no direct push, MR approval required), create protected environments `production` and `production-infra` with required approval, and run `tools/ci/check-gitlab-protection.sh`. The AWS trust cannot check these (only `aud` and `sub`; `aud` is not secret, so it is not a control). The AWS trust matches only project path plus tag names `v0*` to `v9*` (IAM `StringLike` has no `[0-9]`), so GitLab protection is the real control. Whether the `ref_protected` claim can be used in the trust policy is **[VALIDATE]**. Staging trusts the protected branch `main` only. Then add the deploy role ARN to CI.

## Variables to fill (all `[FILL]` or `[CONFIRM]`)

| Variable | Where | Note |
|---|---|---|
| `account_id`, `owner` | both envs | Provider guard (`allowed_account_ids`) and the `owner` tag |
| `certificate_arn` | both envs | Regional ACM certificate |
| `image_tag` | both envs | Release tag; ECR is immutable, `latest` is rejected |
| `gitlab_project_path`, `oidc_provider_thumbprints` | both envs | Trust is limited to this project. Staging: branch `main`. Production: release tags `v0*` to `v9*` only |
| `egress_allowed_domains` | both envs | Vendor domains from the vendor register (K-DP-08). Required when the firewall is on (production default) |
| `worm_retention_days` | production (no default) | **[CONFIRM]**. COMPLIANCE mode cannot be shortened or removed, even by root. Staging default is 1 day |
| `api_public_url` | both envs | Public HTTPS base URL (no trailing slash). scheduler-core calls `<url>/internal/v1/scheduler/occurrence-due` through the ALB, so the host must be on `egress_allowed_domains` when the firewall is on |
| `trail_retention_days` | org | Default 365, COMPLIANCE, irreversible [CONFIRM] |
| `alert_endpoint`, `alert_protocol` | both envs | **[FILL]** email or pager webhook for the SNS topic |
| `otlp_endpoint` | both envs | India-hosted collector (R3) **[FILL]** |
| `key_admin_arns` | both envs | Break-glass or infra role that may administer, not use, the keys |
| `staging_ou_id`, `production_ou_id` | org | OU IDs |
| Backend `bucket` and `kms_key_id` | `versions.tf` of each root | State bucket |

## Residency checklist (docs/arch/infrastructure-and-environments.md section 4)

| # | Check | Built in |
|---|---|---|
| R1 | No resource outside the India regions | `org/main.tf` SCP (`aws:RequestedRegion`, global services exempt); provider variable validation allows only the two regions |
| R2 | Databases, backups, snapshots, buckets, replicas in India | `modules/database` (RDS, backup replication to `ap-south-2`), `modules/storage` (buckets and replicas), provider regions. The monthly scan is still to be written |
| R3 | Logs, traces, metrics stored in India | CloudWatch log groups (365 days, KMS) in `modules/compute`, `network`, `edge`, `database`; alarms in `modules/observability`. The OTLP collector must be India-hosted **[FILL]** |
| R4 | Vendors processing personal data in India | Not infrastructure: vendor register (K-DP-08). The network side is the egress allow-list (`modules/network`, TM-41). R4-5: production images must run as non-root. The task definitions force user `10001:10001`, read-only root filesystem and dropped capabilities |
| R5 | CDN caches no personal data | No CloudFront for the API. The ALB serves it directly; `Cache-Control: no-store` stays an application test |
| R6 | Key material stays in India | `modules/keys`: multi-region KMS keys with replica only in `ap-south-2`. Crypto-shredding must destroy keys in both regions (S-062) |
| R7 | CI/CD holds no production data and no stored cloud keys | `modules/ci_access`: GitLab OIDC, short-lived credentials, deploy role limited to ECS, ECR and the migrate task. Pipeline: [production-readiness.md](../docs/ops/production-readiness.md) section 4, "Deploying". Build-log hygiene is a CI setting |

Other controls: TLS-only (`rds.force_ssl=1`, bucket policies, ALB TLS 1.2+ policy); databases not public and in subnets with no internet route; 35-day backups with point-in-time recovery (RET-17); key store in its own RDS instance under its own KMS key; Object Lock COMPLIANCE bucket for key tombstones, the erasure journal and audit anchors (TM-38, D-74); WAF logs redact query strings, `authorization` and `cookie` (K-DP-04).

## DR (RPO 15 min, RTO 4 h)

Built in `ap-south-2` by `envs/production`: replica KMS keys, a VPC with the same subnet layout, and `blobs` and `worm` buckets receiving replication. RDS automated backups (core and key store) are replicated to `ap-south-2` under the replica keys, so point-in-time restore is possible in DR.

Also built for DR: ECR replication of both repositories into pre-created KMS-encrypted repositories in `ap-south-2`, Secrets Manager replicas of every secret (encrypted with the DR key replica), and the same egress firewall setting as production (production cannot turn it off).

**Not built (decision, see below):** DR database, ECS services and ALB. At failover: restore the replicated backup into the DR VPC, replay the key destruction and erasure logs before opening (backup-and-restore.md section 3), apply compute and edge against the DR network, move DNS.

**Decommissioning and crypto-shredding (K-DP-07):** the key-store RDS instance takes no final snapshot (`skip_final_snapshot`). The core instance takes one named `<env>-core-<final_snapshot_suffix>`; it holds only field-encrypted data, and must be deleted by hand within 35 days. Never take manual snapshots of the key store: they would outlive the 35-day window.

## Audit and detection

- **Organisation CloudTrail** (`org/cloudtrail.tf`): all regions, all management events (IAM and KMS included), log file validation on, bucket in `ap-south-1` with Object Lock COMPLIANCE (365 days) and a customer KMS key. Apply it from the management account.
- **GuardDuty** in `ap-south-1` and `ap-south-2` for staging and production (`modules/guardduty`, S3 data events and RDS login events on).
- **ALB access logs** are kept (security) in the logs bucket, 365-day lifecycle. The API does not put personal data in query strings (location is sent in POST bodies; tokens are never in URLs except the single-use download token, D-34). ALB logs for `/v1/files/*` must be excluded, or the bucket restricted to the security role **[VALIDATE]**: not built yet.
- **WAF tuning (M3):** the common rule set is split into three scoped rules. `SizeRestrictions_BODY` is counted only on `POST /v1/me/documents`, `/v1/me/verification/evidence` and `/v1/clinics/*/verification/evidence`. `CrossSiteScripting_BODY` is counted only on `/v1/consults*`, `/v1/appointments/*/notes*`, `/v1/prescriptions*`. Every other rule and route stays blocking. The route regexes are **[VALIDATE]** against the real API; the app must enforce its own size limits on the counted routes.

## Images, tasks and secrets

- **One core image** (`deploy/images/core.Dockerfile`, ECR repository `<env>/core`) holds `/app/api`, `/app/worker` and `/app/migrate`. Task definitions set `entryPoint` per task: `api`, `worker`, and `migrate` with `command ["up"]`. **scheduler-core** has its own image and repository (`<env>/scheduler-core`, ENTRYPOINT `/app/scheduler-core`); the `scheduler-migrate` task runs it with `command ["migrate"]`. All containers run as `65532:65532`.
- **scheduler-core variables** (from `scheduler-core/internal/config/config.go`): `SCHEDULER_DATABASE_URL` (secret), `SCHEDULER_LISTEN_ADDR` (`:8081`), `SCHEDULER_CALLBACK_URL` (`<api_public_url>/internal/v1/scheduler/occurrence-due`, must be https), `SCHEDULER_CALLBACK_HMAC_SECRET` and `SCHEDULER_SERVICE_TOKEN_SHA256` (secrets); `SCHEDULER_MIGRATE_DATABASE_URL` for the migrate task.
- **One secret, two variables:** `scheduler_callback_secret` feeds core's `MUGIZH_SCHEDULER_CALLBACK_SECRET` and scheduler-core's `SCHEDULER_CALLBACK_HMAC_SECRET` (the same value, base64 of at least 32 bytes). The service token is secret `scheduler_service_token` for core (`MUGIZH_SCHEDULER_TOKEN`); its SHA-256 hex digest goes into `scheduler_service_token_sha256` for scheduler-core.
- **Callback path:** core reaches scheduler-core by Service Connect (`http://scheduler-core:8081`). scheduler-core calls core over HTTPS through the public ALB (via NAT). A WAF rule blocks `/internal/*` unless the source is one of the NAT public addresses; the call is also HMAC-signed and timestamped. With the egress firewall on, the API host name must be allow-listed.
- **Network isolation:** scheduler-core has its own security group: HTTPS out, 5432 to the VPC, and the key-store database admits core tasks only. scheduler-core's database lives on the **core RDS instance** (ADR-01 option b), so the port is reachable, and isolation from core data is by PostgreSQL roles (its role may connect only to `mugizh_scheduler`). A third RDS instance would give network isolation as well **[CONFIRM]**.

## What the migrate jobs do (not done by OpenTofu)

OpenTofu creates the RDS instances with RDS-managed master secrets and **no module login roles**. The one-off `migrate` task connects as the owner, runs the migrations and creates one login role per module (`mugizh_app_<module>` in `db.Modules`, plus `mugizh_app_queue` and `mugizh_app_keystore`), as `deploy/local/up.sh` does locally. The `mugizh_scheduler` database and role and PostGIS are also created by migration work, then `scheduler-migrate` applies the scheduler-core schema. The connection strings go into the empty Secrets Manager secrets (`mugizh/<env>/mugizh_db_url_<module>` and others). The owner URLs (`mugizh_db_url_owner`, `mugizh_keystore_owner_url`, `scheduler_migrate_database_url`) are built by an operator from the RDS master secrets and are read only by the migrate tasks.

The Go start-up refuses `APP_ENV=staging|production` until the cloud KMS, S3 and SMS adapters exist, so the services will not run on this infrastructure before those stories are done.

## Time and logging

Fargate tasks use the Amazon Time Sync Service. CERT-In asks for NTP traceable to NIC or NPL: whether this is acceptable is **[VALIDATE]** with AWS and the lawyer. Logs are kept 365 days (K-SEC-01 proposed; RET-07). Application roles cannot delete logs or from the `worm` bucket.

## Accepted Trivy findings

Scan: `trivy config --severity HIGH,CRITICAL infra/` (Trivy 0.65.0): **0 findings** after these inline `#trivy:ignore` suppressions:

| Rule | Where | Reason |
|---|---|---|
| AVD-AWS-0053 (public ALB) | `modules/edge` | The patient and doctor apps reach the API over the internet. Protected by WAF, TLS 1.2+, application authentication and rate limits |
| AVD-AWS-0104 (egress to the internet) | `modules/network`, app SG on 443 | Tasks must reach allow-listed vendors through NAT. The allow-list is the Network Firewall (production); staging has none by default (synthetic data only) |
| AVD-AWS-0132 (S3 not encrypted with a customer key) | `modules/storage`, logs bucket | ALB access logs cannot be delivered to an SSE-KMS bucket. The bucket holds access logs only, no health data (K-DP-04) |
| AVD-AWS-0057 (wildcard resource) | `modules/compute` exec role, `modules/ci_access` | `ecr:GetAuthorizationToken`, `ecs:RegisterTaskDefinition` and `ecs:DescribeTaskDefinition` have no resource-level permissions. Everything else is scoped to ARNs |

Trivy runs without variable values, so some checks evaluate with unknown inputs. Re-run it with a filled `terraform.tfvars` in CI.

## Decisions beyond the brief

- **OpenTofu >= 1.10, not 1.8**: `use_lockfile` (S3 native locking) does not exist in 1.8 or 1.9. `required_version` stays `>= 1.10.0`. The deploy tool image and the validation use 1.13.9 (D-91, to clear scan findings). Validated (fmt, validate for staging, production, org and oci/envs/dev) with `ghcr.io/opentofu/opentofu:1.13.9`. AWS provider `5.100.0`.
- **Separate NAT subnets**, plus firewall subnets when the firewall is on, so ALB-to-task traffic does not detour through the firewall. The routing (app to firewall to NAT to internet, with a return route in the NAT subnet) is untested **[VALIDATE]** on first apply.
- **Service Connect** carries api/worker to scheduler-core. Internal TLS on that hop is not set up (needs a Private CA) **[VALIDATE]** against "TLS on every connection" in encryption-design section 1.
- **KMS key policy** enables IAM policies for the account (standard pattern). Task roles get their key actions from their own policies, scoped to the data key only. The key-store backup key is not granted to any task role.
- **Production DR is partial** (see DR above).
- **Endpoint policies** restrict the S3 gateway endpoint to this account's buckets (plus the AWS-owned ECR layer bucket) and the interface endpoints to this account's principals. Untested **[VALIDATE]**.
- **SCP**: `cloudfront:*` is not exempt (no CDN yet; add it later, with the us-east-1 WAF and ACM calls it needs, after the founder agrees). `account:*` is narrowed to `Get*` and `List*`, so `EnableRegion` and `DisableRegion` work only from the management account, which SCPs do not bind.
- **Deploy role** (S-094, exactly the calls of `tools/ci/deploy.sh`): register, describe and tag task definitions, run the two migrate task definitions, describe tasks, update and describe the three services, pass the task and execution roles to ECS, push to the two ECR repositories. `ecs:DeregisterTaskDefinition`, `ecs:StopTask` and the migrate log read were removed (not used). `deploy_ci_variables` and `infra_role_arn` outputs list the CI variable values.
- **Infra-apply role** `mugizh-<env>-gitlab-infra-apply` (`modules/ci_access/infra_role.tf`): same OIDC trust as the deploy role of the environment (staging `main`, production tags `v0*` to `v9*`), 1 h sessions, `AdministratorAccess` **only** under the organisation SCP and with an alert on every assumption **[CONFIRM]**. Used by the manual `tofu-plan-<env>` and `tofu-apply-<env>` CI jobs.
- **CI alerts** (`modules/ci_access/alerts.tf`, EventBridge on CloudTrail, to the alerts SNS topic): infra role assumed; task definition registered with an image outside this environment's ECR repositories; RegisterTaskDefinition or RunTask by a principal other than the two CI roles. The key and the SNS topic policy allow `events.amazonaws.com`. Patterns are **[VALIDATE]** on first use. Residual risks and the GitLab preconditions (protected tags, branch, environments; `tools/ci/check-gitlab-protection.sh`): production-readiness.md, "Deploying".
- **Task roles**: no `s3:DeleteObject` anywhere. Erasure is by crypto-shredding, and old versions expire by lifecycle. scheduler-core gets no KMS and no S3 (K-ARC-02).
- **Secrets are created empty** in Secrets Manager (names derived from `secret_name_prefix`), so values never enter state.
- The deploy role cannot change infrastructure. `tofu apply` is manual or break-glass **[CONFIRM]**.
- IAM trust uses `aud` and `sub` only; protection of the branch and tags is enforced in GitLab **[VALIDATE]** (the `ref_protected` claim is not used in the trust policy).

## Still manual or not done

- AWS accounts, OUs, the state buckets, ACM certificate, DNS, the OIDC thumbprint (blocker V1), and the GitLab protections (protected tag `v*`, branch `main`, environments `production` and `production-infra`): mandatory, verified by `tools/ci/check-gitlab-protection.sh`.
- Break-glass and just-in-time access roles, SSO (K-SEC-08). The infrastructure-apply role is built (`modules/ci_access`), but never applied.
- Backup vault lock (backup-and-restore.md section 1 rule 3) and the monthly restore-test account; the monthly R2 scan; the cross-account backup copy.
- DR compute and DR database; Route 53 failover. Opting in to `ap-south-2` and CloudTrail trusted access.
- Admin console zero-trust proxy or IP allow-list; static-assets CDN.
- Autoscaling; the log collector for OTLP; Network Firewall logs of flows (alert logs only).
- No `plan` or `apply` has been run.

## Validate locally

```
export MSYS_NO_PATHCONV=1
docker run --rm -v "$(pwd -W)/infra:/infra" -w /infra/envs/staging ghcr.io/opentofu/opentofu:1.13.9 init -backend=false
docker run --rm -v "$(pwd -W)/infra:/infra" -w /infra/envs/staging ghcr.io/opentofu/opentofu:1.13.9 validate
# same for envs/production and org
docker run --rm -v "$(pwd -W):/src" -w /src aquasec/trivy:0.65.0 config --severity HIGH,CRITICAL infra/
```

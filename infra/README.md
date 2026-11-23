# Infrastructure as code (S-093, AWS part)

OpenTofu for **staging, production and DR on AWS India regions**: primary Mumbai `ap-south-1`, DR Hyderabad `ap-south-2`. Design: [docs/arch/infrastructure-and-environments.md](../docs/arch/infrastructure-and-environments.md). Contracts: K-DP-01, K-DP-03, K-DP-07, K-SEC-01, K-SEC-05, K-ARC-03.

**Status: validated, never applied.** There are no AWS accounts yet (blocker V1). Nothing here has been planned or applied. OCI dev is out of scope for now: see [oci/README.md](oci/README.md).

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
  modules/observability/ alarms, ops.alert metric filter, SNS topic
  oci/                  placeholder (out of scope)
```

Pinned: OpenTofu `>= 1.10` (S3 native state locking, `use_lockfile`, needs 1.10; the brief said 1.8, see "Decisions beyond the brief"), AWS provider `5.100.0`. Lock files (`.terraform.lock.hcl`) are committed; `.terraform/` is ignored.

Every resource gets `project`, `env`, `data_class`, `owner` through provider `default_tags` (staging `synthetic`, production `SH-C`).

## Bootstrap order

1. **State buckets** (by hand, once per account): an S3 bucket in `ap-south-1`, versioned, private, SSE-KMS, TLS-only. Put its name and KMS key in the `backend "s3"` blocks (`[FILL]`). Locking is `use_lockfile = true`, no DynamoDB table.
2. **`infra/org/`** from the management account, **before anything else**: create the OUs, then `tofu apply` with `staging_ou_id` and `production_ou_id`. This turns on R1.
3. **`envs/staging`**, then **`envs/production`** (each in its own account). Copy `terraform.tfvars.example` to `terraform.tfvars` (ignored by git) and fill it. No secrets belong in it.
4. Validate the ACM certificate (regional, in the primary region) and create the DNS record to the ALB (`alb_dns_name` output) by hand. Route 53 is not managed here.
5. Run the **migrate** task once (see "What the migrate job does"), then populate the secrets listed in the `secrets_to_populate` output.
6. Set the GitLab protected branch (`main`) and protected tags (`v*`), then add the deploy role ARN to CI.

## Variables to fill (all `[FILL]` or `[CONFIRM]`)

| Variable | Where | Note |
|---|---|---|
| `account_id`, `owner` | both envs | Provider guard (`allowed_account_ids`) and the `owner` tag |
| `certificate_arn` | both envs | Regional ACM certificate |
| `image_tag` | both envs | Release tag; ECR is immutable, `latest` is rejected |
| `gitlab_project_path`, `oidc_provider_thumbprints` | both envs | Trust is limited to this project. Staging: branch `main`. Production: tags `v*` only |
| `egress_allowed_domains` | both envs | Vendor domains from the vendor register (K-DP-08). Required when the firewall is on (production default) |
| `worm_retention_days` | production (no default) | **[CONFIRM]**. COMPLIANCE mode cannot be shortened or removed, even by root. Staging default is 1 day |
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
| R7 | CI/CD holds no production data and no stored cloud keys | `modules/ci_access`: GitLab OIDC, short-lived credentials, deploy role limited to ECS, ECR and the migrate task. Build-log hygiene is a CI setting |

Other controls: TLS-only (`rds.force_ssl=1`, bucket policies, ALB TLS 1.2+ policy); databases not public and in subnets with no internet route; 35-day backups with point-in-time recovery (RET-17); key store in its own RDS instance under its own KMS key; Object Lock COMPLIANCE bucket for key tombstones, the erasure journal and audit anchors (TM-38, D-74); WAF logs redact query strings, `authorization` and `cookie` (K-DP-04).

## DR (RPO 15 min, RTO 4 h)

Built in `ap-south-2` by `envs/production`: replica KMS keys, a VPC with the same subnet layout, and `blobs` and `worm` buckets receiving replication. RDS automated backups (core and key store) are replicated to `ap-south-2` under the replica keys, so point-in-time restore is possible in DR.

**Not built (decision, see below):** DR database, ECS services and ALB. At failover: restore the replicated backup into the DR VPC, replay the key destruction and erasure logs before opening (backup-and-restore.md section 3), apply compute and edge against the DR network, move DNS. Secrets are not replicated yet.

## What the migrate job does (not done by OpenTofu)

OpenTofu creates the RDS instances with RDS-managed master secrets and **no module login roles**. The one-off `migrate` task (`command: up`) connects as the owner, runs the migrations and creates one login role per module (`mugizh_app_<module>` in `db.Modules`, plus `mugizh_app_queue` and `mugizh_app_keystore`), as `deploy/local/up.sh` does locally. It must also create the `mugizh_scheduler` database and role, and enable PostGIS. The connection strings then go into the empty Secrets Manager secrets (`mugizh/<env>/mugizh_db_url_<module>` and others). `MUGIZH_DB_URL_OWNER` and `MUGIZH_KEYSTORE_OWNER_URL` (read only by the migrate task) are built by an operator from the RDS master secrets. The `migrate` binary reads those two names today. `MUGIZH_SCHEDULER_DB_URL` is a proposed name for scheduler-core **[CONFIRM]**.

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

- **OpenTofu 1.10, not 1.8**: `use_lockfile` (S3 native locking) does not exist in 1.8 or 1.9. Validated with `ghcr.io/opentofu/opentofu:1.10.6`. AWS provider `5.100.0`.
- **Separate NAT subnets**, plus firewall subnets when the firewall is on, so ALB-to-task traffic does not detour through the firewall. The routing (app to firewall to NAT to internet, with a return route in the NAT subnet) is untested **[VALIDATE]** on first apply.
- **Service Connect** carries api/worker to scheduler-core. Internal TLS on that hop is not set up (needs a Private CA) **[VALIDATE]** against "TLS on every connection" in encryption-design section 1.
- **KMS key policy** enables IAM policies for the account (standard pattern). Task roles get their key actions from their own policies, scoped to the data key only. The key-store backup key is not granted to any task role.
- **Production DR is partial** (see DR above).
- **Task roles**: no `s3:DeleteObject` anywhere. Erasure is by crypto-shredding, and old versions expire by lifecycle. scheduler-core gets no KMS and no S3 (K-ARC-02).
- **Secrets are created empty** in Secrets Manager (names derived from `secret_name_prefix`), so values never enter state.
- The deploy role cannot change infrastructure. `tofu apply` is manual or break-glass **[CONFIRM]**.
- IAM trust uses `aud` and `sub` only; protection of the branch and tags is enforced in GitLab **[VALIDATE]** (the `ref_protected` claim is not used in the trust policy).

## Still manual or not done

- AWS accounts, OUs, the state buckets, ACM certificate, DNS, GitLab protected refs and the OIDC thumbprint (blocker V1).
- Break-glass and just-in-time access roles, SSO (K-SEC-08), and the infrastructure-apply role.
- Backup vault lock (backup-and-restore.md section 1 rule 3) and the monthly restore-test account; the monthly R2 scan; the cross-account backup copy.
- DR compute, DR database and secret replication; Route 53 failover.
- Admin console zero-trust proxy or IP allow-list; static-assets CDN.
- Autoscaling; the log collector for OTLP; Network Firewall logs of flows (alert logs only).
- No `plan` or `apply` has been run.

## Validate locally

```
export MSYS_NO_PATHCONV=1
docker run --rm -v "$(pwd -W)/infra:/infra" -w /infra/envs/staging ghcr.io/opentofu/opentofu:1.10.6 init -backend=false
docker run --rm -v "$(pwd -W)/infra:/infra" -w /infra/envs/staging ghcr.io/opentofu/opentofu:1.10.6 validate
# same for envs/production and org
docker run --rm -v "$(pwd -W):/src" -w /src aquasec/trivy:0.65.0 config --severity HIGH,CRITICAL infra/
```

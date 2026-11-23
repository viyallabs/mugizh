# OCI (development), S-093 part 2

OpenTofu for the **dev environment on Oracle Cloud Infrastructure**, India region Mumbai `ap-mumbai-1` (Hyderabad `ap-hyderabad-1` through the `region` variable; no other region passes validation). Staging, production and DR are on AWS: see [../README.md](../README.md). Decisions: [adr-decisions.md](../../docs/arch/adr-decisions.md) section 6 (ADR-10), [infrastructure-and-environments.md](../../docs/arch/infrastructure-and-environments.md) sections 9 and 10. Contracts: K-DP-01, K-DP-03, K-SEC-01, K-SEC-05, K-ARC-03.

**Status: validated, never applied.** There is no OCI tenancy yet. Checked with OpenTofu 1.10.6 (`init -backend=false`, `validate`, `fmt -check`) and Trivy 0.65.0 (`config --severity HIGH,CRITICAL`: 0 findings). Nothing has been planned or applied.

## The synthetic-data-only rule

**Dev holds synthetic data only** (CLAUDE.md section 5.3). No real patient, doctor or staff data, no real phone numbers or e-mail addresses, no real Aadhaar values. This is why dev may use the app's local root key, a software-protected Vault key and a registry token in a CI variable. The tag namespace `mugizh-dev` allows only `env=dev` and `data_class=synthetic`: a different value is rejected by OCI. If real data is ever needed, it goes to AWS staging or production, never here.

## Layout

```
infra/oci/
  .gitignore            .terraform, *.tfvars, state
  envs/dev/
    versions.tf         OpenTofu >= 1.10, oracle/oci 7.22.0 (exact), S3-compatible backend (placeholders), provider
    variables.tf        all variables ([FILL] and [CONFIRM] marked)
    main.tf             compartment mugizh-dev, tag namespace (env, data_class), common tags
    network.tf          VCN, private subnet, NAT and service gateways, locked default security list, NSG, Bastion
    keys.tf             OCI Vault (DEFAULT) and one AES-256 master key, yearly auto-rotation
    storage.tf          blobs bucket (private, versioned, key-encrypted) and logs bucket
    logging.tf          VCN flow log, service connector: flow log + compartment audit -> logs bucket
    iam.tf              policies for the storage key, the log archive, and the dev-only CI group
    compute.tf          Ubuntu image by data source, flex VM, encrypted block volume, paravirtualised attachment
    cloud-init.yaml.tftpl  first-boot setup (Docker, volume, PostGIS) and the deploy script
    outputs.tf          compartment, VM IP, Bastion ID, S3-compatible endpoint, key ID
    terraform.tfvars.example
```

Every resource gets the freeform tags `project=mugizh`, `env=dev`, `data_class=synthetic`, `owner=<var.owner>`, and, where the resource supports them, the defined tags `mugizh-dev.env` and `mugizh-dev.data_class`. The `.terraform.lock.hcl` is committed; `.terraform/` is ignored.

## Network and access

- One VCN, one **private** subnet. There is **no internet gateway and no public subnet**: the Bastion service needs only a private endpoint, and the VM needs only outbound access. Decision beyond the brief; add a public subnet only if a load balancer is ever needed.
- Egress through a **NAT gateway** (TCP 443 and 80 only). A **service gateway** routes Object Storage and other Oracle services inside the Oracle network.
- **No inbound from the internet.** The security list has no ingress rule; the VCN default security list (SSH from anywhere) is taken over and emptied. SSH (port 22) is allowed only from the subnet itself, where the Bastion private endpoint lives. The VM has no public IP and IMDSv1 is off.
- **SSH only through the OCI Bastion service**, limited to `bastion_client_cidrs` (0.0.0.0/0 is rejected). Sessions are made by hand or by CI, not by OpenTofu (a session needs a key):

```
oci bastion session create-managed-ssh --bastion-id <bastion_id> --target-resource-id <vm_id> \
  --target-os-username ubuntu --ssh-public-key-file <key.pub> --session-ttl-in-seconds 3600
# port forwarding to the API: create-port-forwarding with --target-private-ip <vm_private_ip> --target-port 8080
```

## Residency (R1) on OCI

- The `region` variable accepts only `ap-mumbai-1` and `ap-hyderabad-1`.
- **Tenancy-level guard [VALIDATE what OCI supports]:** OCI IAM cannot deny a region. The controls are: (1) **subscribe the tenancy only to Mumbai (and Hyderabad if used)**; a region that is not subscribed cannot be used; (2) a **quota policy** in the root compartment that sets compute, block volume and object storage quotas to zero where `request.region` is not an India region (syntax and which families support the `request.region` condition to be checked; not created here because it cannot be tested without a tenancy); (3) **Cloud Guard** with a detector or Security Zone recipe on the `mugizh-dev` compartment. Dev holds no personal data, so this is defence in depth, not a legal control.

## Keys

An **OCI Vault** (DEFAULT type) and one **software-protected AES-256 key** (no HSM, no cost; dev only) encrypt the **database block volume, the boot volume and both buckets**. Policies let the Block Volume and Object Storage services use that one key. The **application's own key provider in dev stays the local root key** (`MUGIZH_LOCAL_ROOT_KEY`, generated on the VM, synthetic data only), as in `deploy/local`. The `keys` interface for OCI Vault is S-098. The local provider is refused in staging and production.

## Logging

- **VCN flow logs** on the private subnet (OCI Logging keeps at most 180 days).
- A **service connector** copies the flow log and the **dev compartment's audit events** to the `mugizh-dev-logs` bucket (key-encrypted, private).
- **At least 365 days [CONFIRM for dev]:** `log_retention_days` (default 365, minimum 365) is the target. The provider has no retention-rule resource, so create the rule by hand after the first apply: `oci os retention-rule create --bucket-name mugizh-dev-logs --namespace-name <ns> --display-name logs-min-retention --time-amount 365 --time-unit DAYS` (leave it unlocked in dev) [VALIDATE].
- VM operating-system audit (auditd) is not shipped yet; add the Unified Monitoring Agent custom log if wanted [CONFIRM].

## What runs on the VM

One flex VM (default `VM.Standard.E4.Flex`, 2 OCPU, 12 GB; boot volume 50 GB), Ubuntu 24.04 chosen by data source (the latest build; changes do not replace the VM). **In-transit encryption is on** for the instance and the volume attachment. The database lives on a separate **encrypted 50 GB block volume** mounted at `/srv/mugizh-db`.

cloud-init (clones nothing, holds no secret):

1. Installs `docker.io` and `docker-compose-v2` from the Ubuntu archive. Versions are variables (`docker_io_version`, `docker_compose_version`); **empty installs the current version, so pin both after the first build [FILL]**.
2. Mounts the volume, generates the database owner password **on the VM** (`/etc/mugizh/owner_pw`, mode 0600), and starts **PostGIS `17-3.5`** (the `deploy/local` image; the keystore database is created by an init script). The database is not published on the host.
3. Writes `/opt/mugizh/docker-compose.yml`, the same stack as `deploy/local/docker-compose.yml` (db, api, worker, one-off migrate), with the core image `<core_image>:<core_image_tag>`.

Phase 2 is by hand, through a Bastion session, because the GitLab registry needs a token that must not be in code:

```
sudo docker login registry.gitlab.com      # read-only deploy token, entered at the prompt
sudo /opt/mugizh/deploy.sh                 # pull, migrate, one login role per module, start api and worker
```

`deploy.sh` follows `deploy/local/up.sh` (`APP_ENV=dev`, random passwords and keys generated on the VM into `/etc/mugizh/app.env`, never into code or state). The API listens on `127.0.0.1:8080` of the VM; reach it with a Bastion port-forwarding session. scheduler-core is not run yet (not in the brief) [CONFIRM when needed].

**Why PostgreSQL in a container and not OCI managed PostgreSQL:** the pre-build checklist (section 3.1) asks to check that the managed service has PostGIS and the same major version as AWS **[VALIDATE]**. That is not checked, and the container is the stated fallback, identical to local and to the migration tests. Switch only after a check.

## Object Storage

`mugizh-dev-blobs`: private (`NoPublicAccess`), versioning on, encrypted with the Vault key. The app writes blobs to a local volume in dev for now. For the future S3 adapter, the S3-compatible endpoint is the output `s3_compatible_endpoint` (`https://<namespace>.compat.objectstorage.<region>.oraclecloud.com`, path-style, credentials are an OCI customer secret key kept outside code).

## CI access (ADR-10)

- **Preferred [VALIDATE]:** OCI IAM identity domains have an identity-propagation trust that exchanges an external OIDC token for an OCI token. Whether GitLab's OIDC issuer can be used is not checked. If it works, a trust for the dev project replaces the key below.
- **Fallback built here (ADR-10 allows it for dev):** `iam.tf` creates the group `mugizh-dev-ci` and a policy limited to the dev compartment (open Bastion sessions, read instances and network) [CONFIRM scope]. Create a CI user **by hand**, add it to the group, create an API key, and store it as a **protected, masked GitLab variable scoped to the `dev` environment only**. No key, OCID of a secret or token is in code or state. Set `ci_policy_enabled = false` to leave it out.

## Bootstrap

1. Create the tenancy, enable MFA on the admin, set a budget alert, subscribe **only** to Mumbai (and Hyderabad if used) [VALIDATE the region guard above].
2. Create the state bucket by hand in the tenancy (private, versioned) and an OCI customer secret key for it. Export `AWS_ACCESS_KEY_ID` and `AWS_SECRET_ACCESS_KEY` from that key in your shell. Replace the `[FILL]` values in the `backend "s3"` block of `envs/dev/versions.tf` (bucket and namespace). If OCI refuses the S3 backend options, fall back to a local backend and keep state out of git [VALIDATE].
3. Copy `terraform.tfvars.example` to `terraform.tfvars` (git-ignored) and fill it. Authenticate with the OCI CLI profile or `OCI_*` environment variables.
4. `tofu init`, `tofu plan` (review), `tofu apply` as a user allowed to manage compartments, tag namespaces and policies in the parent compartment.
5. Wait for cloud-init (a few minutes), open a Bastion session, run phase 2 above.
6. Create the 365-day retention rule on the logs bucket (Logging section).

## Variables to fill

| Variable | Note |
|---|---|
| `tenancy_ocid`, `parent_compartment_ocid` | Parent is the tenancy for the root. The policies and the tag namespace are created there |
| `owner` | Tag value (a role or team) |
| `ssh_public_key` | Public key for the VM default user (`ubuntu`) |
| `bastion_client_cidrs` | Office or VPN CIDRs; no default; `0.0.0.0/0` rejected |
| `core_image`, `core_image_tag` | GitLab registry path and release tag; `latest` rejected |
| `docker_io_version`, `docker_compose_version` | Pin after the first build |
| `log_retention_days` | Default 365 [CONFIRM for dev] |
| `region` | Default `ap-mumbai-1`; optional `ap-hyderabad-1` |
| `vm_shape`, `vm_ocpus`, `vm_memory_gbs`, `db_volume_gbs` | Small defaults; check shape availability and limits [VALIDATE] |
| Backend `bucket` and endpoint namespace | In `versions.tf` (no variables in backend blocks) |

## Trivy

`aquasec/trivy:0.65.0 config --severity HIGH,CRITICAL infra/oci`: **0 findings** (also 0 at LOW and MEDIUM). No finding is accepted or suppressed. Trivy has few OCI-specific checks, so the table above, not the scan, carries the security argument.

## [VALIDATE] and [CONFIRM] items

- **[VALIDATE]** OCI regional guard (quota policy `request.region` support, Cloud Guard, subscribing only to India regions).
- **[VALIDATE]** S3-compatible backend: `encrypt = true` accepted; `use_lockfile` (conditional writes) not enabled.
- **[VALIDATE]** OCI managed PostgreSQL with PostGIS and the same major version as AWS (container used until then).
- **[VALIDATE]** GitLab OIDC identity-propagation trust for CI.
- **[VALIDATE]** `VM.Standard.E4.Flex` availability and limits in Mumbai; Ubuntu 24.04 image listing for the shape.
- **[VALIDATE]** Retention rule and versioning on the logs bucket; the service connector policy; the `_Audit` log group as a connector source.
- **[CONFIRM]** 365-day log retention for dev; CI policy scope; VM audit shipping; whether scheduler-core runs in dev.
- Not run: `plan` and `apply` (no account). Provider schema arguments were checked only by `validate`, not against a live tenancy.

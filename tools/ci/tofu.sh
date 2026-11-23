#!/usr/bin/env bash
# Infrastructure plan and apply (S-094), always manual jobs, never part of a code deploy.
#
#   tools/ci/tofu.sh <staging|production> plan    writes infra/envs/<env>/plan.tfplan (a CI artifact)
#   tools/ci/tofu.sh <staging|production> apply   applies exactly that saved plan, nothing else
#
# Uses the infrastructure-apply role INFRA_ROLE_ARN, a different role from the deploy role
# (DEPLOY_ROLE_ARN): infra/modules/ci_access/infra_role.tf, named mugizh-<env>-gitlab-infra-apply, AdministratorAccess
# under the organisation SCP, with an alert on every assumption. It trusts the same OIDC provider and refs as the
# deploy role of the environment.
# Inputs (GitLab CI variables scoped to the environment):
#   GITLAB_OIDC_TOKEN  id_token of the job       INFRA_ROLE_ARN        infrastructure-apply role
#   DEPLOY_IMAGE_DIGEST digest of the tool image (sanity check)
#   TF_STATE_BUCKET    state bucket (ap-south-1)  TF_STATE_KMS_KEY_ID   its KMS key
#   TFVARS_FILE        File-type variable with the env's terraform.tfvars (no secrets belong in it)
# Never prints a token or a credential; never uses `set -x`.
set -euo pipefail

log() { printf '[tofu] %s\n' "$*"; }
die() { printf '[tofu] ERROR: %s\n' "$*" >&2; exit 1; }
need() { [[ -n "${!1:-}" ]] || die "required variable $1 is not set"; }

[[ $# -eq 2 ]] || die "usage: tofu.sh <staging|production> <plan|apply>"
ENV_NAME="$1"
ACTION="$2"
case "$ENV_NAME" in staging | production) ;; *) die "environment must be staging or production" ;; esac
case "$ACTION" in plan | apply) ;; *) die "action must be plan or apply" ;; esac
for v in GITLAB_OIDC_TOKEN INFRA_ROLE_ARN TF_STATE_BUCKET TF_STATE_KMS_KEY_ID TFVARS_FILE CI_PIPELINE_ID; do need "$v"; done
[[ "${DEPLOY_IMAGE_DIGEST:-}" =~ ^sha256:[0-9a-f]{64}$ ]] || die "DEPLOY_IMAGE_DIGEST is not set: pin the deploy tool image by digest"
# The role is exactly mugizh-<env>-gitlab-infra-apply (infra/modules/ci_access/infra_role.tf); ENV_NAME is validated above.
INFRA_ROLE_NAME="mugizh-${ENV_NAME}-gitlab-infra-apply"
[[ "$INFRA_ROLE_ARN" =~ ^arn:aws:iam::[0-9]{12}:role/${INFRA_ROLE_NAME}$ ]] || die "INFRA_ROLE_ARN is not the ${INFRA_ROLE_NAME} role"
[[ -f "$TFVARS_FILE" ]] || die "TFVARS_FILE must be a File-type CI variable"

if [[ "$ENV_NAME" == production ]]; then
  [[ "${CI_COMMIT_TAG:-}" =~ ^v[0-9] ]] || die "production infrastructure jobs run only from a release tag v<digit>*"
else
  [[ -n "${CI_COMMIT_BRANCH:-}" && "${CI_COMMIT_BRANCH}" == "${CI_DEFAULT_BRANCH:-}" ]] || die "staging infrastructure jobs run only from the default branch"
fi

export AWS_REGION="${AWS_REGION:-ap-south-1}" AWS_STS_REGIONAL_ENDPOINTS=regional AWS_PAGER=""
export AWS_DEFAULT_REGION="$AWS_REGION"

WORK="$(mktemp -d)"
chmod 700 "$WORK"
trap 'rm -rf "$WORK"' EXIT

(umask 077 && printf '%s' "$GITLAB_OIDC_TOKEN" >"$WORK/oidc-token")
unset GITLAB_OIDC_TOKEN
creds="$(aws sts assume-role-with-web-identity \
  --role-arn "$INFRA_ROLE_ARN" \
  --role-session-name "gitlab-infra-${CI_PIPELINE_ID}" \
  --web-identity-token "file://$WORK/oidc-token" \
  --duration-seconds 3600 \
  --query Credentials --output json)"
rm -f "$WORK/oidc-token"
AWS_ACCESS_KEY_ID="$(jq -er .AccessKeyId <<<"$creds")"
AWS_SECRET_ACCESS_KEY="$(jq -er .SecretAccessKey <<<"$creds")"
AWS_SESSION_TOKEN="$(jq -er .SessionToken <<<"$creds")"
export AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY AWS_SESSION_TOKEN
unset creds
caller_arn="$(aws sts get-caller-identity --query Arn --output text)"
[[ "$caller_arn" == *":assumed-role/${INFRA_ROLE_NAME}/"* ]] || die "assumed identity is not the expected infrastructure role"
log "assumed the infrastructure role for $ENV_NAME"

cd "infra/envs/${ENV_NAME}"
# The backend block in versions.tf holds [FILL] placeholders; the real values come from CI variables.
tofu init -input=false -lockfile=readonly \
  -backend-config="bucket=${TF_STATE_BUCKET}" \
  -backend-config="kms_key_id=${TF_STATE_KMS_KEY_ID}"

if [[ "$ACTION" == plan ]]; then
  tofu plan -input=false -lock-timeout=5m -var-file="$TFVARS_FILE" -out=plan.tfplan
  log "plan saved as plan.tfplan; review it, then run the apply job of the same pipeline"
else
  [[ -f plan.tfplan ]] || die "plan.tfplan is missing: run the plan job of this pipeline first"
  # Applying a saved plan: OpenTofu refuses it if the state changed since the plan was made.
  tofu apply -input=false -lock-timeout=5m plan.tfplan
fi

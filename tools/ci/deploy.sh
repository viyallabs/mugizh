#!/usr/bin/env bash
# Continuous deployment (S-094): copy the release images to ECR by digest, run the migrations,
# update the services (worker, scheduler-core, api), smoke-check, roll the services back on failure.
#
#   tools/ci/deploy.sh <staging|production> <image_tag>
#
# Runs in the deploy tool image (tools/deployimage) from a GitLab CI job that requested an id_token.
# Design: docs/ops/production-readiness.md section 4. It does NOT apply infrastructure (tofu apply is
# a separate manual job) and never rolls migrations back (forward-only). Fails closed: any unexpected
# error stops the script (set -e), and once the services are touched an EXIT trap rolls them back.
# Never prints a token, a credential or a response body; never uses `set -x`.
#
# Inputs (GitLab CI variables, scoped to the environment; names in docs/ops/production-readiness.md):
#   GITLAB_OIDC_TOKEN   id_token of the job (aud = the audience the AWS trust policy expects)
#   DEPLOY_ROLE_ARN     the env's GitLab deploy role (infra output deploy_role_arn)
#   DEPLOY_SUBNET_IDS   comma-separated private app subnet ids
#   DEPLOY_APP_SG_ID    security group of the migrate task; DEPLOY_SCHEDULER_SG_ID: of scheduler-migrate
#   API_PUBLIC_URL      https base URL of the API (no trailing slash)
#   Optional: AWS_REGION (default ap-south-1). Names are fixed: prefix, cluster and ECR path are mugizh-<env>.
#   Staging: CORE_IMAGE_DIGEST and SCHEDULER_CORE_IMAGE_DIGEST (dotenv artifact of the `images` job; required,
#   the script deploys exactly those digests). Production: the digests are read from the release tag in the
#   GitLab registry, which must be protected/immutable (docs/ops/production-readiness.md, Deploying).
#   DEPLOY_IMAGE_DIGEST  digest of this tool image (a sanity check; the job image is pinned by it)
#   GitLab predefined: CI_REGISTRY, CI_REGISTRY_IMAGE, CI_REGISTRY_USER, CI_REGISTRY_PASSWORD, CI_PIPELINE_ID,
#                      CI_COMMIT_TAG, CI_COMMIT_BRANCH, CI_DEFAULT_BRANCH
set -euo pipefail

SMOKE_DEADLINE_SECONDS=300
SMOKE_CONSECUTIVE_OK=3

log() { printf '[deploy] %s\n' "$*"; }
die() { printf '[deploy] ERROR: %s\n' "$*" >&2; exit 1; }
need() { [[ -n "${!1:-}" ]] || die "required variable $1 is not set"; }

# ---------------------------------------------------------------- arguments and guards
[[ $# -eq 2 ]] || die "usage: deploy.sh <staging|production> <image_tag>"
ENV_NAME="$1"
IMAGE_TAG="$2"
case "$ENV_NAME" in staging | production) ;; *) die "environment must be staging or production" ;; esac
[[ "$IMAGE_TAG" =~ ^[A-Za-z0-9_.-]{1,128}$ ]] || die "image tag has an invalid form"
[[ "$IMAGE_TAG" != "latest" ]] || die "image tag 'latest' is not allowed (immutable repositories)"

# Defence in depth: the AWS trust policy and the GitLab protected refs are the real controls.
if [[ "$ENV_NAME" == production ]]; then
  [[ "${CI_COMMIT_TAG:-}" =~ ^v[0-9] ]] || die "production deploys run only from a release tag v<digit>*"
else
  [[ -n "${CI_COMMIT_BRANCH:-}" && "${CI_COMMIT_BRANCH}" == "${CI_DEFAULT_BRANCH:-}" ]] || die "staging deploys run only from the default branch"
fi

for v in GITLAB_OIDC_TOKEN DEPLOY_ROLE_ARN DEPLOY_SUBNET_IDS DEPLOY_APP_SG_ID DEPLOY_SCHEDULER_SG_ID API_PUBLIC_URL \
  CI_REGISTRY CI_REGISTRY_IMAGE CI_REGISTRY_USER CI_REGISTRY_PASSWORD CI_PIPELINE_ID; do
  need "$v"
done

[[ "${DEPLOY_IMAGE_DIGEST:-}" =~ ^sha256:[0-9a-f]{64}$ ]] || die "DEPLOY_IMAGE_DIGEST is not set: pin the deploy tool image by digest"
if [[ "$ENV_NAME" == staging ]]; then
  for v in CORE_IMAGE_DIGEST SCHEDULER_CORE_IMAGE_DIGEST; do
    [[ "${!v:-}" =~ ^sha256:[0-9a-f]{64}$ ]] || die "$v is missing or malformed: staging deploys only the digests the images job wrote"
  done
fi

AWS_REGION="${AWS_REGION:-ap-south-1}"
# Fixed from the validated environment name (never from a variable), so it is safe inside the regexes below.
PREFIX="mugizh-${ENV_NAME}"
CLUSTER="$PREFIX"
API_URL="${API_PUBLIC_URL%/}"
export AWS_REGION AWS_DEFAULT_REGION="$AWS_REGION" AWS_STS_REGIONAL_ENDPOINTS=regional AWS_PAGER=""

[[ "$API_URL" =~ ^https://[A-Za-z0-9.-]+(:[0-9]+)?$ ]] || die "API_PUBLIC_URL must be an https base URL without a path"
[[ "$DEPLOY_SUBNET_IDS" =~ ^subnet-[0-9a-f]+(,subnet-[0-9a-f]+)*$ ]] || die "DEPLOY_SUBNET_IDS must be comma-separated subnet ids"
[[ "$DEPLOY_APP_SG_ID" =~ ^sg-[0-9a-f]+$ && "$DEPLOY_SCHEDULER_SG_ID" =~ ^sg-[0-9a-f]+$ ]] || die "security group ids are malformed"
# The role name is "<prefix>-gitlab-deploy" (infra/modules/ci_access). This stops one environment's
# variables from being used for the other.
[[ "$DEPLOY_ROLE_ARN" =~ ^arn:aws:iam::[0-9]{12}:role/${PREFIX}-gitlab-deploy$ ]] || die "DEPLOY_ROLE_ARN is not the ${PREFIX}-gitlab-deploy role"

WORK="$(mktemp -d)"
chmod 700 "$WORK"
# crane writes registry credentials under DOCKER_CONFIG; keep them in the temp dir that on_exit deletes.
export DOCKER_CONFIG="$WORK/docker-config"

declare -A PREV_TD NEW_TD DIGEST
TOUCHED=()
DEPLOY_OK=0

# ---------------------------------------------------------------- exit handling and rollback
rollback_services() {
  log "ROLLBACK: restoring the services to their previous task-definition revisions (migrations stay: forward-only)"
  local failed=0 i svc
  # The session from the start may have expired during a long deploy: get fresh credentials from the same
  # token file (kept until on_exit). If that fails too, try the current credentials.
  assume_role || log "could not re-assume the deploy role; trying the existing credentials"
  for ((i = ${#TOUCHED[@]} - 1; i >= 0; i--)); do
    svc="${TOUCHED[i]}"
    if ! aws ecs update-service --cluster "$CLUSTER" --service "$svc" --task-definition "${PREV_TD[$svc]}" \
      --query 'service.serviceName' --output text >/dev/null; then
      log "ROLLBACK could not be started for service $svc"
      failed=1
    fi
  done
  for svc in "${TOUCHED[@]}"; do
    if ! aws ecs wait services-stable --cluster "$CLUSTER" --services "$svc"; then
      log "ROLLBACK did not stabilise service $svc"
      failed=1
    fi
  done
  if ((failed)); then
    log "MANUAL INTERVENTION REQUIRED. Previous revisions: $(for s in "${TOUCHED[@]}"; do printf '%s=%s ' "$s" "${PREV_TD[$s]##*/}"; done)"
  else
    log "rollback complete; the previous revisions are serving again"
  fi
}

on_exit() {
  local rc=$?
  trap - EXIT
  # A TERM or INT must not interrupt the rollback.
  trap "" TERM INT
  set +e
  if ((rc != 0 && DEPLOY_OK == 0 && ${#TOUCHED[@]} > 0)); then
    rollback_services
  fi
  rm -rf "$WORK" "$HOME/.docker/config.json"
  exit "$rc"
}
trap on_exit EXIT
trap 'exit 143' TERM
trap 'exit 130' INT

# ---------------------------------------------------------------- 1. assume the deploy role (GitLab OIDC)
(umask 077 && printf '%s' "$GITLAB_OIDC_TOKEN" >"$WORK/oidc-token")
unset GITLAB_OIDC_TOKEN
ECR_REGISTRY=""
assume_role() {
  local creds caller_arn account_id
  creds="$(aws sts assume-role-with-web-identity \
    --role-arn "$DEPLOY_ROLE_ARN" \
    --role-session-name "gitlab-${CI_PIPELINE_ID}" \
    --web-identity-token "file://$WORK/oidc-token" \
    --duration-seconds 3600 \
    --query Credentials --output json)" || return 1
  AWS_ACCESS_KEY_ID="$(jq -er .AccessKeyId <<<"$creds")" || return 1
  AWS_SECRET_ACCESS_KEY="$(jq -er .SecretAccessKey <<<"$creds")" || return 1
  AWS_SESSION_TOKEN="$(jq -er .SessionToken <<<"$creds")" || return 1
  export AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY AWS_SESSION_TOKEN
  caller_arn="$(aws sts get-caller-identity --query Arn --output text)" || return 1
  account_id="$(aws sts get-caller-identity --query Account --output text)" || return 1
  [[ "$caller_arn" == *":assumed-role/${PREFIX}-gitlab-deploy/"* ]] || { log "assumed identity is not the expected deploy role"; return 1; }
  ECR_REGISTRY="${account_id}.dkr.ecr.${AWS_REGION}.amazonaws.com"
}
log "environment=$ENV_NAME region=$AWS_REGION cluster=$CLUSTER tag=$IMAGE_TAG"
assume_role || die "could not assume the deploy role"
log "assumed the deploy role in ${ECR_REGISTRY%%.*}"

# ---------------------------------------------------------------- 2. copy images to ECR by digest
log "logging in to the registries"
# shellcheck disable=SC2153  # CI_REGISTRY is a GitLab predefined variable
printf '%s' "$CI_REGISTRY_PASSWORD" | crane auth login "$CI_REGISTRY" -u "$CI_REGISTRY_USER" --password-stdin
aws ecr get-login-password | crane auth login "$ECR_REGISTRY" -u AWS --password-stdin

# copy_image <name> <pinned_digest_or_empty>: GitLab registry -> ECR, always by digest. Sets DIGEST[name].
# Staging passes the digest the images job built (that exact digest is copied). Production passes nothing and
# resolves the release tag in the GitLab registry, which must be protected/immutable (a mutable tag would let
# someone with push rights change what is deployed); the approver compares the logged digests with staging's.
# ECR is IMMUTABLE: a re-run of the same tag finds the same digest and copies nothing. A tag that
# already exists in ECR with a different digest makes crane copy fail, which fails the deploy.
copy_image() {
  local name="$1" digest="$2" src dst existing
  src="${CI_REGISTRY_IMAGE}/${name}:${IMAGE_TAG}"
  dst="${ECR_REGISTRY}/${PREFIX}/${name}"
  if [[ -z "$digest" ]]; then digest="$(crane digest "$src")"; fi
  [[ "$digest" =~ ^sha256:[0-9a-f]{64}$ ]] || die "source digest for $name is malformed"
  existing="$(crane digest "${dst}:${IMAGE_TAG}" 2>/dev/null || true)"
  if [[ "$existing" == "$digest" ]]; then
    log "$name ${IMAGE_TAG}: already in ECR at $digest"
  else
    crane copy "${CI_REGISTRY_IMAGE}/${name}@${digest}" "${dst}:${IMAGE_TAG}"
    [[ "$(crane digest "${dst}:${IMAGE_TAG}")" == "$digest" ]] || die "ECR digest for $name differs from the source after copy"
    log "$name ${IMAGE_TAG}: copied to ECR at $digest"
  fi
  DIGEST[$name]="$digest"
}
copy_image core "${CORE_IMAGE_DIGEST:-}"
copy_image scheduler-core "${SCHEDULER_CORE_IMAGE_DIGEST:-}"

# ---------------------------------------------------------------- task definitions
# register_td <family> <container> <image_repo_name>: new revision of the family's latest active
# revision with only the container image replaced by the digest reference. Prints the new ARN.
register_td() {
  local family="$1" container="$2" repo="$3" image current resp new
  image="${ECR_REGISTRY}/${PREFIX}/${repo}@${DIGEST[$repo]}"
  resp="$(aws ecs describe-task-definition --task-definition "$family" --include TAGS --output json)"
  current="$(jq -er --arg c "$container" '[.taskDefinition.containerDefinitions[] | select(.name == $c) | .image] | if length == 1 then .[0] else error("container not found exactly once") end' <<<"$resp")"
  [[ "$current" == "${ECR_REGISTRY}/${PREFIX}/${repo}"[:@]* ]] || die "task definition $family does not use the ${PREFIX}/${repo} repository"
  new="$(jq --arg c "$container" --arg img "$image" '
      (.taskDefinition
        | del(.taskDefinitionArn, .revision, .status, .requiresAttributes, .compatibilities, .registeredAt, .registeredBy, .deregisteredAt)
        | .containerDefinitions |= map(if .name == $c then .image = $img else . end))
      + (if ((.tags // []) | length) > 0 then {tags: .tags} else {} end)' <<<"$resp")"
  printf '%s' "$new" >"$WORK/td-${family}.json"
  aws ecs register-task-definition --cli-input-json "file://$WORK/td-${family}.json" \
    --query 'taskDefinition.taskDefinitionArn' --output text
}

# ---------------------------------------------------------------- record the previous service revisions
SERVICES=(worker scheduler-core api)
for svc in "${SERVICES[@]}"; do
  prev="$(aws ecs describe-services --cluster "$CLUSTER" --services "$svc" --output json |
    jq -er 'if (.failures | length) == 0 and (.services | length) == 1 and .services[0].status == "ACTIVE" then .services[0].taskDefinition else error("service missing or not active") end')"
  PREV_TD[$svc]="$prev"
done
log "previous revisions: worker=${PREV_TD[worker]##*/} scheduler-core=${PREV_TD[scheduler-core]##*/} api=${PREV_TD[api]##*/}"

# ---------------------------------------------------------------- 3. migrations first (forward-only)
# run_migration <task> <repo> <security_group>: one-off Fargate task; every container must exit 0.
run_migration() {
  local task="$1" repo="$2" sg="$3" td netcfg run task_arn result
  td="$(register_td "${PREFIX}-${task}" "$task" "$repo")"
  NEW_TD[$task]="$td"
  netcfg="$(jq -nc --arg s "$DEPLOY_SUBNET_IDS" --arg g "$sg" \
    '{awsvpcConfiguration: {subnets: ($s | split(",")), securityGroups: [$g], assignPublicIp: "DISABLED"}}')"
  run="$(aws ecs run-task --cluster "$CLUSTER" --launch-type FARGATE --count 1 --task-definition "$td" \
    --network-configuration "$netcfg" --started-by "gitlab-${CI_PIPELINE_ID}" --output json)"
  task_arn="$(jq -er 'if (.failures | length) == 0 and (.tasks | length) == 1 then .tasks[0].taskArn else error("run-task failed to start the task") end' <<<"$run")"
  log "migration $task started (${task_arn##*/}); waiting for it to stop"
  # Not stopped within the waiter's limit (10 minutes) is a failure too; the task is left running
  # for the operator to inspect (stopping a migration half-way is worse).
  aws ecs wait tasks-stopped --cluster "$CLUSTER" --tasks "$task_arn" ||
    die "migration $task did not stop in time (task ${task_arn##*/} may still be running); check the ECS console and the $task log group"
  result="$(aws ecs describe-tasks --cluster "$CLUSTER" --tasks "$task_arn" --output json |
    jq -c '.tasks[0] | {status: .lastStatus, reason: (.stoppedReason // ""), codes: [.containers[] | .exitCode]}')"
  if [[ "$(jq -r '(.status == "STOPPED") and (.codes | length > 0) and (all(.codes[]; . == 0))' <<<"$result")" != true ]]; then
    die "migration $task failed (status, reason, exit codes: $result); the log group /mugizh/${ENV_NAME}/${task} has details. Services are untouched"
  fi
  log "migration $task finished with exit code 0"
}
run_migration migrate core "$DEPLOY_APP_SG_ID"
run_migration scheduler-migrate scheduler-core "$DEPLOY_SCHEDULER_SG_ID"

# ---------------------------------------------------------------- 4. services: worker, scheduler-core, api
NEW_TD[worker]="$(register_td "${PREFIX}-worker" worker core)"
NEW_TD[scheduler-core]="$(register_td "${PREFIX}-scheduler-core" scheduler-core scheduler-core)"
NEW_TD[api]="$(register_td "${PREFIX}-api" api core)"

# update_service <service>: new revision, then wait until it is the stable primary deployment.
# The ECS circuit breaker may roll a failed deployment back by itself and the waiter then still
# reports "stable", so the service must also be on the new revision with the rollout COMPLETED.
update_service() {
  local svc="$1"
  TOUCHED+=("$svc")
  aws ecs update-service --cluster "$CLUSTER" --service "$svc" --task-definition "${NEW_TD[$svc]}" \
    --query 'service.serviceName' --output text >/dev/null
  aws ecs wait services-stable --cluster "$CLUSTER" --services "$svc"
  aws ecs describe-services --cluster "$CLUSTER" --services "$svc" --output json |
    jq -e --arg td "${NEW_TD[$svc]}" '
      .services[0]
      | .taskDefinition == $td
        and (.deployments | length) == 1
        and .deployments[0].rolloutState == "COMPLETED"
        and .runningCount == .desiredCount' >/dev/null ||
    die "service $svc is not running the new revision (deployment failed or was rolled back by ECS)"
  log "service $svc is stable on ${NEW_TD[$svc]##*/}"
}
for svc in "${SERVICES[@]}"; do update_service "$svc"; done

# ---------------------------------------------------------------- 5. smoke check
log "smoke check: GET ${API_URL}/health/ready (200 ${SMOKE_CONSECUTIVE_OK} times in a row within ${SMOKE_DEADLINE_SECONDS}s)"
ok=0
SECONDS=0
while ((SECONDS < SMOKE_DEADLINE_SECONDS)); do
  code="$(curl -sS -o /dev/null -w '%{http_code}' --max-time 10 "${API_URL}/health/ready" 2>/dev/null || true)"
  if [[ "$code" == 200 ]]; then
    ok=$((ok + 1))
    if ((ok >= SMOKE_CONSECUTIVE_OK)); then break; fi
  else
    ok=0
  fi
  sleep 10
done
((ok >= SMOKE_CONSECUTIVE_OK)) || die "smoke check failed: /health/ready did not answer 200 in time"
DEPLOY_OK=1

# ---------------------------------------------------------------- 6. summary (no secrets)
log "SUMMARY environment=$ENV_NAME tag=$IMAGE_TAG"
log "  (production approvers: compare these digests with the ones staging deployed, in the deploy-staging job log)"
log "  core            ${DIGEST[core]}"
log "  scheduler-core  ${DIGEST[scheduler-core]}"
for t in migrate scheduler-migrate worker scheduler-core api; do
  log "  task definition ${NEW_TD[$t]##*/}"
done
log "deploy succeeded"

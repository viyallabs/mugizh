#!/usr/bin/env bash
# Verifies the GitLab settings that the AWS deploy trust relies on (S-094, H1). Run by a maintainer, by hand,
# before the first production deploy and after any change to roles, tags or environments.
#
#   GITLAB_TOKEN=<personal token, scope read_api> tools/ci/check-gitlab-protection.sh <project-id-or-url-encoded-path>
#   Optional: GITLAB_API_URL (default https://gitlab.com/api/v4)
#
# Why: AWS can only check the OIDC `aud` and `sub` claims. `sub` names the project and the tag or branch, but a
# tag name is only as trustworthy as GitLab's rules about who may create it. The audience is not a secret and
# is not a control. So the real control is this GitLab configuration, and this script fails if any part is missing:
#   1. protected tag `v*`: create allowed for Maintainers (or nobody) only, never Developers
#   2. protected branch `main`: nobody may push directly, no force push, and at least one MR approval is required
#   3. protected environments `production` and `production-infra`: at least one required approval
# Read-only: it only issues GET requests. The token never appears on a command line or in the output.
set -euo pipefail

API="${GITLAB_API_URL:-https://gitlab.com/api/v4}"
[[ $# -eq 1 ]] || { echo "usage: check-gitlab-protection.sh <project-id-or-url-encoded-path>" >&2; exit 2; }
PROJECT="$1"
[[ -n "${GITLAB_TOKEN:-}" ]] || { echo "GITLAB_TOKEN (read_api) is not set" >&2; exit 2; }
[[ "$PROJECT" =~ ^[0-9A-Za-z._%-]+$ ]] || { echo "project must be a numeric id or a URL-encoded path (group%2Fproject)" >&2; exit 2; }
[[ "$API" =~ ^https:// ]] || { echo "GITLAB_API_URL must be https" >&2; exit 2; }

FAILS=0
pass() { printf 'PASS  %s\n' "$*"; }
fail() { printf 'FAIL  %s\n' "$*"; FAILS=$((FAILS + 1)); }

# get <path>: prints the JSON body, or nothing (and returns 1) when the request fails or is not allowed.
# The token goes through a curl config on a pipe, not through the argument list.
get() {
  curl -sS --fail --max-time 30 -K <(printf 'header = "PRIVATE-TOKEN: %s"\n' "$GITLAB_TOKEN") "${API}/projects/${PROJECT}/$1" 2>/dev/null
}

# ---- 1. protected tag v*
if tags="$(get protected_tags)"; then
  if jq -e '[.[] | select(.name == "v*")] | length == 1' <<<"$tags" >/dev/null; then
    if jq -e '[.[] | select(.name == "v*") | .create_access_levels[] | .access_level] | length > 0 and all(. == 40 or . == 60 or . == 0)' <<<"$tags" >/dev/null; then
      pass "tag v* is protected; creation limited to Maintainers (or nobody)"
    else
      fail "tag v*: creation is allowed for a role below Maintainer (Developers must not create release tags)"
    fi
    if jq -e '[.[] | select(.name == "v*") | .create_access_levels[] | select((.user_id // null) != null or (.group_id // null) != null)] | length == 0' <<<"$tags" >/dev/null; then
      pass "tag v*: no extra users or groups are allowed to create it"
    else
      fail "tag v*: specific users or groups are allowed to create it (review who they are)"
    fi
  else
    fail "protected tag pattern exactly v* does not exist"
  fi
else
  fail "could not read protected_tags (token scope or project id)"
fi

# ---- 2. protected branch main
if branches="$(get protected_branches)"; then
  if jq -e '[.[] | select(.name == "main")] | length == 1' <<<"$branches" >/dev/null; then
    if jq -e '[.[] | select(.name == "main") | .push_access_levels[] | .access_level] | all(. == 0)' <<<"$branches" >/dev/null; then pass "branch main: nobody may push directly"; else fail "branch main: direct push is allowed for some role (must be 'No one')"; fi
    if jq -e '[.[] | select(.name == "main") | .allow_force_push] | all(. == false)' <<<"$branches" >/dev/null; then pass "branch main: force push is off"; else fail "branch main: force push is allowed"; fi
  else
    fail "branch main is not protected"
  fi
else
  fail "could not read protected_branches"
fi
if rules="$(get approval_rules)"; then
  if jq -e '[.[] | select(.approvals_required >= 1)] | length >= 1' <<<"$rules" >/dev/null; then
    pass "merge requests need at least one approval"
  else
    fail "no merge request approval rule requires an approval"
  fi
else
  fail "could not read approval_rules (a GitLab tier without approval rules cannot enforce MR approval)"
fi

# ---- 3. protected environments
if envs="$(get protected_environments)"; then
  for e in production production-infra; do
    if jq -e --arg e "$e" '[.[] | select(.name == $e)] | length == 1' <<<"$envs" >/dev/null; then
      if jq -e --arg e "$e" '[.[] | select(.name == $e) | .approval_rules[]? | .required_approvals] | length > 0 and all(. >= 1)' <<<"$envs" >/dev/null; then
        pass "environment $e is protected and requires approval"
      else
        fail "environment $e is protected but has no required approval rule"
      fi
    else
      fail "environment $e is not a protected environment"
    fi
  done
else
  fail "could not read protected_environments"
fi

if ((FAILS > 0)); then
  printf '\n%d check(s) failed. Do not run production deploys or production infrastructure jobs until all pass.\n' "$FAILS" >&2
  exit 1
fi
printf '\nAll checks passed.\n'

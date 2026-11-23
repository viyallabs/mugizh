#!/bin/sh
# S-092: prove the OpenAPI ruleset works (good fixture passes, each bad fixture fails on the rule it targets),
# then lint the real specs under api/core and api/scheduler if any exist.
# Uses a local `spectral` binary if present (CI image), else the pinned container.
set -u
cd "$(dirname "$0")/../.."
IMAGE="${SPECTRAL_IMAGE:-stoplight/spectral:6.15.0}"
if command -v spectral >/dev/null 2>&1; then
  sp() { spectral "$@"; }
else
  ROOT="$(pwd -W 2>/dev/null || pwd)"
  sp() { MSYS_NO_PATHCONV=1 docker run --rm -v "${ROOT}:/src" -w /src "$IMAGE" "$@"; }
fi
RULESET=docs/arch/openapi-lint.spectral.yaml
FIX=docs/arch/lint-fixtures
fail=0

sp lint --ruleset "$RULESET" --fail-severity error "$FIX/valid.core-api.v1.yaml" || { echo "FAIL: valid fixture must pass"; fail=1; }

out=$(sp lint --ruleset "$RULESET" --fail-severity error "$FIX/invalid.core-api.v1.yaml"); rc=$?
[ $rc -ne 0 ] || { echo "FAIL: invalid fixture must fail"; fail=1; }
for r in paths-versioned paths-kebab-case properties-camel-case post-idempotency-key \
  idempotency-key-required-for-money-and-messages error-responses-problem-json roles-declared \
  no-offset-pagination list-cursor-pagination no-pii-in-query money-is-integer timestamps-date-time operation-operationId; do
  echo "$out" | grep -q "[[:space:]]$r[[:space:]]" || { echo "FAIL: rule $r did not fire on invalid fixture"; fail=1; }
done

out=$(sp lint --ruleset "$RULESET" --fail-severity error "$FIX/scheduler-core.invalid.v1.yaml"); rc=$?
[ $rc -ne 0 ] || { echo "FAIL: scheduler fixture must fail"; fail=1; }
echo "$out" | grep -q "no-health-fields-in-scheduler" || { echo "FAIL: no-health-fields-in-scheduler did not fire"; fail=1; }

specs=$(ls api/core/*.yaml api/scheduler/*.yaml 2>/dev/null || true)
if [ -n "$specs" ]; then
  # shellcheck disable=SC2086
  sp lint --ruleset api/.spectral.yaml --fail-severity error $specs || fail=1
else
  echo "no api/core or api/scheduler specs yet; skipped"
fi
[ $fail -eq 0 ] && echo "spectral: OK" || echo "spectral: FAILED"
exit $fail

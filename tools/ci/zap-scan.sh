#!/usr/bin/env bash
# S-179: OWASP ZAP API scan (DAST) of the whole Core API, before staging exists.
#
# The target is the assembled API built by backend/internal/app/dast_test.go (build tag
# "dast"): a test database, fake vendors, synthetic people only. It never ships. ZAP runs
# its API scan against every module spec three times: anonymous, as a signed-in patient and
# as a verified doctor with MFA. Reports go to OUT (default: a temp directory, never the repo:
# they contain the synthetic session tokens in request headers).
#
# Usage: tools/ci/zap-scan.sh [OUT_DIR]      (Docker must be running)
# On staging, run the same scan with TARGET set to the staging URL and real test accounts.
set -euo pipefail
export MSYS_NO_PATHCONV=1
ZAP_IMAGE="${ZAP_IMAGE:-ghcr.io/zaproxy/zaproxy:2.16.1}"
ROOT="$(cd "$(dirname "$0")/../.." && (pwd -W 2>/dev/null || pwd))"
OUT="${1:-$(mktemp -d)}"
mkdir -p "$OUT/specs"
NET=mugizh-dast
TARGET_NAME=mugizh-dast-target
TARGET="http://${TARGET_NAME}:18081"

docker network inspect "$NET" >/dev/null 2>&1 || docker network create "$NET" >/dev/null
docker rm -f "$TARGET_NAME" >/dev/null 2>&1 || true
cleanup() { docker rm -f "$TARGET_NAME" >/dev/null 2>&1 || true; }
trap cleanup EXIT

echo "starting the DAST target"
docker run -d --name "$TARGET_NAME" --network "$NET" \
  -v "${ROOT}:/src" -v mugizh-gomod:/go/pkg/mod -v mugizh-gobuild:/root/.cache/go-build \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -e TESTCONTAINERS_HOST_OVERRIDE=host.docker.internal -e TESTCONTAINERS_RYUK_DISABLED=true \
  -e MUGIZH_DAST_ADDR=0.0.0.0:18081 -e MUGIZH_DAST_TOKENS=/tmp/dast.json -e MUGIZH_DAST_FOR="${DAST_FOR:-90m}" \
  -w /src/backend mugizh-dev:1 go test -count=1 -tags dast -run TestDASTServer -timeout 3h -v ./internal/app/ >/dev/null

for _ in $(seq 1 180); do
  docker exec "$TARGET_NAME" test -s /tmp/dast.json 2>/dev/null && break
  if ! docker ps -q -f name="$TARGET_NAME" | grep -q .; then echo "target stopped"; exit 1; fi
  sleep 2
done
TOKENS="$(docker exec "$TARGET_NAME" cat /tmp/dast.json)"
tok() { echo "$TOKENS" | sed -E "s/.*\"$1\":\"([^\"]+)\".*/\1/"; }
PATIENT="$(tok patient)"; DOCTOR="$(tok doctor)"

cp "$ROOT"/api/core/*.v1.yaml "$OUT/specs/"
chmod -R a+rwX "$OUT" 2>/dev/null || true
fail=0
for spec in "$OUT"/specs/*.v1.yaml; do
  mod="$(basename "$spec" .v1.yaml)"
  for who in anon patient doctor; do
    auth=()
    case $who in
      patient) auth=(-e "ZAP_AUTH_HEADER=Authorization" -e "ZAP_AUTH_HEADER_VALUE=Bearer ${PATIENT}") ;;
      doctor) auth=(-e "ZAP_AUTH_HEADER=Authorization" -e "ZAP_AUTH_HEADER_VALUE=Bearer ${DOCTOR}") ;;
    esac
    echo "scan ${mod} as ${who}"
    rc=0
    docker run --rm --network "$NET" -v "$(cd "$OUT" && (pwd -W 2>/dev/null || pwd)):/zap/wrk:rw" "${auth[@]}" "$ZAP_IMAGE" \
      zap-api-scan.py -t "/zap/wrk/specs/${mod}.v1.yaml" -f openapi -O "$TARGET" -I \
      -J "${mod}-${who}.json" -r "${mod}-${who}.html" -z "-config api.disablekey=true" >"$OUT/${mod}-${who}.log" 2>&1 || rc=$?
    # -I: warnings do not fail the run; FAIL-level alerts give rc 1, errors rc 3.
    if [ "$rc" -ne 0 ]; then echo "  rc=${rc} (see ${OUT}/${mod}-${who}.log)"; fail=1; fi
  done
done
echo "reports in ${OUT}"
exit $fail

#!/usr/bin/env bash
# S-175 stand-in: k6 smoke load test (tests/load/smoke.js) against the assembled API built by
# backend/internal/app/load_test.go (build tag "load"): a test database, fake vendors,
# synthetic people. It never ships and never touches a real vendor.
#
# Usage: tools/ci/load-smoke.sh [OUT_DIR]   (env: RATE=40 DURATION=60s PATIENTS=100)
# Results: k6 summary in OUT_DIR/summary.json and the console. Laptop numbers are a smoke
# signal only; the launch-profile run (load-test-plan.md) belongs on staging (S-093).
set -euo pipefail
export MSYS_NO_PATHCONV=1
K6_IMAGE="${K6_IMAGE:-grafana/k6:0.54.0}"
ROOT="$(cd "$(dirname "$0")/../.." && (pwd -W 2>/dev/null || pwd))"
OUT="${1:-$(mktemp -d)}"
mkdir -p "$OUT"
NET=mugizh-dast
NAME=mugizh-load-target

docker network inspect "$NET" >/dev/null 2>&1 || docker network create "$NET" >/dev/null
docker rm -f "$NAME" >/dev/null 2>&1 || true
trap 'docker rm -f "$NAME" >/dev/null 2>&1 || true' EXIT

docker run -d --name "$NAME" --network "$NET" \
  -v "${ROOT}:/src" -v mugizh-gomod:/go/pkg/mod -v mugizh-gobuild:/root/.cache/go-build \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -e TESTCONTAINERS_HOST_OVERRIDE=host.docker.internal -e TESTCONTAINERS_RYUK_DISABLED=true \
  -e MUGIZH_DAST_ADDR=0.0.0.0:18081 -e MUGIZH_LOAD_OUT=/tmp/load.json -e MUGIZH_DAST_FOR=40m \
  -e MUGIZH_LOAD_PATIENTS="${PATIENTS:-100}" \
  -w /src/backend mugizh-dev:1 go test -count=1 -tags load -run TestLoadServer -timeout 2h -v ./internal/app/ >/dev/null

for _ in $(seq 1 300); do
  docker exec "$NAME" test -s /tmp/load.json 2>/dev/null && break
  if ! docker ps -q -f name="$NAME" | grep -q .; then echo "target stopped"; docker logs "$NAME" 2>&1 | tail -20; exit 1; fi
  sleep 2
done
docker exec "$NAME" cat /tmp/load.json > "$OUT/load.json"
OUTW="$(cd "$OUT" && (pwd -W 2>/dev/null || pwd))"
rc=0
docker run --rm --network "$NET" -v "${ROOT}/tests/load:/scripts:ro" -v "${OUTW}:/data" \
  -e TARGET="http://${NAME}:18081" -e RATE="${RATE:-40}" -e DURATION="${DURATION:-60s}" \
  "$K6_IMAGE" run --summary-export /data/summary.json /scripts/smoke.js || rc=$?
rm -f "$OUT/load.json" # synthetic tokens; do not keep them
exit $rc

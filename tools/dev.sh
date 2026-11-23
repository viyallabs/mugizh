#!/usr/bin/env bash
# Run a command inside the pinned dev image (tools/devimage). Usage: tools/dev.sh <dir> <cmd...>
# Example: tools/dev.sh backend go test ./...
# Mounts the repo, a Go module cache volume and the Docker socket (testcontainers).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd -W 2>/dev/null || pwd)"
DIR="${1:?dir}"; shift
export MSYS_NO_PATHCONV=1
exec docker run --rm -i \
  -v "${ROOT}:/src" \
  -v mugizh-gomod:/go/pkg/mod \
  -v mugizh-gobuild:/root/.cache/go-build \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -e TESTCONTAINERS_HOST_OVERRIDE=host.docker.internal \
  -e TESTCONTAINERS_RYUK_DISABLED=true \
  -e GOFLAGS="${GOFLAGS:-}" \
  -w "/src/${DIR}" \
  mugizh-dev:1 "$@"

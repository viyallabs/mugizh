#!/bin/sh
# Generate Go server code from the per-module OpenAPI specs (K-ARC-06: the spec is the
# source of truth; code that does not match it does not compile). Run inside the dev image:
#   tools/dev.sh . sh tools/gen-api.sh            # every module
#   tools/dev.sh . sh tools/gen-api.sh booking    # one module (api/core/booking.v1.yaml)
set -eu
if [ $# -gt 0 ]; then specs="api/core/$1.v1.yaml"; else specs=$(ls api/core/*.v1.yaml); fi
for spec in $specs; do
  mod=$(basename "$spec" .v1.yaml)
  out="backend/internal/modules/$mod/httpapi"
  mkdir -p "$out"
  cfg=$(mktemp)
  cat > "$cfg" <<CFG
package: httpapi
generate:
  chi-server: true
  strict-server: true
  models: true
  embedded-spec: true
output: $out/gen.go
output-options:
  skip-prune: true
CFG
  oapi-codegen -config "$cfg" "$spec"
  rm -f "$cfg"
  echo "generated $out/gen.go"
done

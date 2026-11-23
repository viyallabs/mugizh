#!/usr/bin/env bash
# Start the local stack, apply migrations, create one login role per module with random
# passwords, and write deploy/local/.env (git-ignored). Synthetic data only.
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"; root="$(cd "$here/../.." && pwd)"
mkdir -p "$here/.secrets"
[ -s "$here/.secrets/owner_pw" ] || head -c 24 /dev/urandom | base64 | tr -d '/+=' > "$here/.secrets/owner_pw"
OWNER_PW="$(cat "$here/.secrets/owner_pw")"
docker compose -f "$here/docker-compose.yml" up -d --wait db
HOST=host.docker.internal; PORT=55432
OWNER_URL="postgres://owner:${OWNER_PW}@${HOST}:${PORT}/mugizh?sslmode=disable"
KS_OWNER_URL="postgres://owner:${OWNER_PW}@${HOST}:${PORT}/mugizh_keystore?sslmode=disable"
MSYS_NO_PATHCONV=1 docker run --rm -e MUGIZH_DB_URL_OWNER="$OWNER_URL" -e MUGIZH_KEYSTORE_OWNER_URL="$KS_OWNER_URL" \
  -v "$(cd "$root" && (pwd -W 2>/dev/null || pwd)):/src" -v mugizh-gomod:/go/pkg/mod -v mugizh-gobuild:/root/.cache/go-build \
  -w /src/backend mugizh-dev:1 go run ./cmd/migrate up
env_file="$here/.env"
{
  echo "APP_ENV=local"
  echo "MUGIZH_PACK_DIR=../config/country-packs/IN"
  echo "MUGIZH_VENDOR_REGISTER=../config/vendor-register.yaml"
  echo "MUGIZH_BLOB_DIR=/tmp/mugizh-blobs"
  echo "MUGIZH_BLIND_INDEX_KEY=$(head -c 32 /dev/urandom | base64)"
  echo "MUGIZH_LOCAL_ROOT_KEY=$(head -c 32 /dev/urandom | base64)"
  echo "MUGIZH_SCHEDULER_CALLBACK_SECRET=$(head -c 32 /dev/urandom | base64)"
} > "$env_file"
roles="admin audit booking clinical consult directory identity notifications payments platform records reviews verification"
sql=""
for m in $roles queue keystore; do
  pw="$(head -c 18 /dev/urandom | base64 | tr -d '/+=')"
  case $m in
    queue) group=mugizh_queue; db=mugizh; var=MUGIZH_DB_URL_QUEUE ;;
    keystore) group=mugizh_keystore; db=mugizh_keystore; var=MUGIZH_KEYSTORE_URL ;;
    *) group="mugizh_mod_$m"; db=mugizh; var="MUGIZH_DB_URL_$(echo "$m" | tr a-z A-Z)" ;;
  esac
  sql="$sql DROP ROLE IF EXISTS mugizh_app_$m; CREATE ROLE mugizh_app_$m LOGIN PASSWORD '$pw' IN ROLE $group;"
  echo "$var=postgres://mugizh_app_$m:${pw}@${HOST}:${PORT}/${db}?sslmode=disable" >> "$env_file"
done
docker compose -f "$here/docker-compose.yml" exec -T db psql -q -U owner -d mugizh -c "$sql" >/dev/null
echo "local stack ready; settings in deploy/local/.env (git-ignored)"

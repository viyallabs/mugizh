#!/usr/bin/env bash
# S-091 acceptance: "a planted secret is caught". Plants fake secrets in a temp dir and asserts gitleaks exits non-zero
# for each, using the same .gitleaks.toml as the real scan. Also asserts a clean file exits 0 (the check can pass).
# Uses a local `gitleaks` binary if present (CI image), else the pinned container.
set -uo pipefail
cd "$(dirname "$0")/../.."
REPO="$(pwd)"
IMAGE="${GITLEAKS_IMAGE:-zricethezav/gitleaks:v8.28.0}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

scan() { # scan <dir>; returns gitleaks exit code
  if command -v gitleaks >/dev/null 2>&1; then
    gitleaks detect --source "$1" --no-git --config "$REPO/.gitleaks.toml" --no-banner >/dev/null 2>&1
  else
    local d c; d="$(cd "$1" && (pwd -W 2>/dev/null || pwd))"; c="$(pwd -W 2>/dev/null || pwd)"
    MSYS_NO_PATHCONV=1 docker run --rm -v "$d:/scan" -v "$c/.gitleaks.toml:/cfg.toml:ro" "$IMAGE" \
      detect --source /scan --no-git --config /cfg.toml --no-banner >/dev/null 2>&1
  fi
}

fail=0
mkdir "$TMP/aws" "$TMP/pem" "$TMP/clean"
# Fake values assembled at runtime so this script itself never contains a scannable secret.
{ printf 'aws_access_key_id = %s%s\n' 'AKIA' 'IOSFODNN7EXAMPL3'; printf 'aws_secret_access_key = %s\n' 'wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY'; } > "$TMP/aws/creds.go.txt"
{ printf -- '-----BEGIN %s PRIVATE KEY-----\n' 'RSA'
  printf 'MIIEowIBAAKCAQEAxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx\n%.0s' 1 2 3 4
  printf -- '-----END %s PRIVATE KEY-----\n' 'RSA'; } > "$TMP/pem/key.txt"
printf 'package x\n\nconst Name = "booking.hold_expired"\n' > "$TMP/clean/ok.go"

scan "$TMP/aws"   && { echo "FAIL: planted AWS key NOT caught"; fail=1; } || echo "ok: planted AWS key caught"
scan "$TMP/pem"   && { echo "FAIL: planted private key NOT caught"; fail=1; } || echo "ok: planted private key caught"
scan "$TMP/clean" && echo "ok: clean file passes" || { echo "FAIL: clean file flagged"; fail=1; }
exit $fail

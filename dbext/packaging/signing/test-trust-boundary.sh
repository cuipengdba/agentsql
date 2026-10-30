#!/bin/sh
# A package must not authenticate itself with a public key it carries.
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/../../.." && pwd -P)
installer="$root/dbext/packaging/install-binder.sh"
scratch=$(mktemp -d "${TMPDIR:-/tmp}/agentsql-binder-trust.XXXXXX")
trap 'rm -rf -- "$scratch"' EXIT HUP INT TERM
mkdir "$scratch/dist"
printf 'synthetic untrusted key\n' > "$scratch/dist/DEV-ONLY-public-key.asc"
ln -s "$scratch/dist/DEV-ONLY-public-key.asc" "$scratch/outside-alias.asc"

expect_failure() {
  expected_code=$1
  expected_message=$2
  shift 2
  set +e
  response=$(sh "$installer" "$@" 2>&1)
  code=$?
  set -e
  if [ "$code" -ne "$expected_code" ] || ! printf '%s\n' "$response" | grep -Fq -- "$expected_message"; then
    printf 'unexpected binder trust result: exit=%s output=%s\n' "$code" "$response" >&2
    exit 1
  fi
}

expect_failure 64 '--trusted-public-key is required' --dist "$scratch/dist"
expect_failure 65 'must be outside the package directory' --dist "$scratch/dist" \
  --trusted-public-key "$scratch/dist/DEV-ONLY-public-key.asc"
expect_failure 65 'must be outside the package directory' --dist "$scratch/dist" \
  --trusted-public-key "$scratch/outside-alias.asc"
expect_failure 66 'trusted public key file is missing' --dist "$scratch/dist" \
  --trusted-public-key "$scratch/missing.asc"
expect_failure 64 'Usage: install-binder.sh' --dist "$scratch/dist" --trusted-public-key
printf 'BINDER_TRUST_BOUNDARY=PASS\n'

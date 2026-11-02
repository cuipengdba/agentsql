#!/bin/sh
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/../../.." && pwd)
scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT INT TERM
dist="$scratch/dist"
mkdir -p "$dist/deb/pg16/amd64" "$dist/apk/pg14/amd64"
fresh='deb/pg16/amd64/fresh.deb'
stale='apk/pg14/amd64/archived.apk'
printf 'fresh package\n' > "$dist/$fresh"
printf 'archived package\n' > "$dist/$stale"
printf 'preserve existing signature\n' > "$dist/$stale.asc"
printf '%s\n' "$fresh" > "$dist/.selection"
sh "$root/dbext/packaging/signing/sign-dev.sh" "$dist" "$dist/.selection" >/dev/null 2>&1
test -f "$dist/$fresh.asc"
test "$(cat "$dist/$stale.asc")" = 'preserve existing signature'
test "$(wc -l < "$dist/SHA256SUMS" | tr -d ' ')" = 1
grep -F "  $fresh" "$dist/SHA256SUMS" >/dev/null
if grep -F "$stale" "$dist/SHA256SUMS" >/dev/null; then
  echo 'stale package entered signed manifest' >&2
  exit 1
fi
printf '%s\n' '../outside.deb' > "$dist/.selection"
if sh "$root/dbext/packaging/signing/sign-dev.sh" "$dist" "$dist/.selection" >/dev/null 2>&1; then
  echo 'path traversal entered signing selection' >&2
  exit 1
fi
printf '%s\n%s\n' "$fresh" "$fresh" > "$dist/.selection"
if sh "$root/dbext/packaging/signing/sign-dev.sh" "$dist" "$dist/.selection" >/dev/null 2>&1; then
  echo 'duplicate artifact entered signing selection' >&2
  exit 1
fi
echo 'BINDER_SIGN_SELECTION=PASS'

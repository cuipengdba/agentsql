#!/bin/sh
set -eu

dist=${1:-/out}
test -d "$dist"
package=$(find "$dist" -type f \( -name '*.deb' -o -name '*.rpm' -o -name '*.apk' \) | head -1)
test -n "$package" && test -f "$package.asc"

home=$(mktemp -d)
trap 'rm -rf "$home"' EXIT INT TERM
chmod 0700 "$home"
gpg --batch --homedir "$home" --import "$dist/DEV-ONLY-public-key.asc" >/dev/null 2>&1
cp "$package" "$home/tampered-package"
printf 'tamper' >> "$home/tampered-package"
if gpg --batch --homedir "$home" --verify "$package.asc" "$home/tampered-package" >/dev/null 2>&1; then
  echo 'tampered artifact signature was accepted' >&2
  exit 1
fi
echo 'tampered artifact signature rejection: PASS'

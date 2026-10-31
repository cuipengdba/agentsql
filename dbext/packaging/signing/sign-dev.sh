#!/bin/sh
set -eu

output=${1:-/out}
test -d "$output"
selection=${2:?an explicit fresh-artifact selection file is required}
test -f "$selection" && test ! -L "$selection"
homedir=$(mktemp -d)
trap 'rm -rf "$homedir"' EXIT INT TERM
chmod 0700 "$homedir"
: > "$homedir/artifacts"
while IFS= read -r relative; do
  case "$relative" in
    ''|/*|*'..'*|*[!a-zA-Z0-9._/-]*) echo 'unsafe artifact selection' >&2; exit 64 ;;
    *.deb|*.rpm|*.apk|*.cdx.json|*.provenance.json|*.oci.tar) ;;
    *) echo 'unsupported artifact selection' >&2; exit 64 ;;
  esac
  artifact=$(realpath -e "$output/$relative")
  case "$artifact" in "$output"/*) ;; *) echo 'artifact outside output directory' >&2; exit 64 ;; esac
  test -f "$artifact" && test ! -L "$output/$relative"
  printf '%s\n' "$artifact" >> "$homedir/artifacts"
done < "$selection"
test -s "$homedir/artifacts"
LC_ALL=C sort -u "$homedir/artifacts" > "$homedir/unique"
test "$(wc -l < "$homedir/artifacts")" -eq "$(wc -l < "$homedir/unique")" || { echo 'duplicate artifact selection' >&2; exit 64; }
mv "$homedir/unique" "$homedir/artifacts"
while IFS= read -r artifact; do rm -f "${artifact}.asc"; done < "$homedir/artifacts"
rm -f "$output/SHA256SUMS" "$output/SHA256SUMS.asc" "$output/DEV-ONLY-public-key.asc" "$output/DEV-ONLY-key-fingerprint.txt" "$output/DEV-ONLY-SIGNING-NOTICE.txt"

cat > "$homedir/key.conf" <<EOF
Key-Type: RSA
Key-Length: 3072
Name-Real: AgentSQL Binder DEV ONLY One-Time
Name-Email: dev-only-do-not-release@agentsql.invalid
Expire-Date: 1d
%no-protection
%commit
EOF

gpg --batch --homedir "$homedir" --generate-key "$homedir/key.conf"
fingerprint=$(gpg --batch --homedir "$homedir" --with-colons --list-secret-keys | awk -F: '$1=="fpr" {print $10; exit}')
test -n "$fingerprint"
gpg --batch --homedir "$homedir" --armor --export "$fingerprint" > "$output/DEV-ONLY-public-key.asc"
printf '%s\n' "$fingerprint" > "$output/DEV-ONLY-key-fingerprint.txt"
cat > "$output/DEV-ONLY-SIGNING-NOTICE.txt" <<EOF
DEV ONLY - NOT A RELEASE SIGNING IDENTITY
Fingerprint: $fingerprint
This one-time key was generated inside the local signing container and expires in one day.
Its private key is deleted when this container exits. Generate a new key for every local build.
Production private keys are an owner-controlled offline release gate and must never use this identity.
EOF

: > "$output/SHA256SUMS"
while IFS= read -r artifact; do
  relative=${artifact#"$output"/}
  digest=$(sha256sum "$artifact" | awk '{print $1}')
  printf '%s  %s\n' "$digest" "$relative" >> "$output/SHA256SUMS"
  gpg --batch --homedir "$homedir" --armor --detach-sign --local-user "$fingerprint" --output "${artifact}.asc" "$artifact"
done < "$homedir/artifacts"
gpg --batch --homedir "$homedir" --armor --detach-sign --local-user "$fingerprint" --output "$output/SHA256SUMS.asc" "$output/SHA256SUMS"

while IFS= read -r artifact; do
  gpg --batch --homedir "$homedir" --verify "${artifact}.asc" "$artifact"
done < "$homedir/artifacts"
gpg --batch --homedir "$homedir" --verify "$output/SHA256SUMS.asc" "$output/SHA256SUMS"

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
expect_failure 64 '--dsn and --runtime-role are required with --create-extension' \
  --dist "$scratch/dist" --trusted-public-key "$scratch/dist/DEV-ONLY-public-key.asc" --create-extension
expect_failure 64 '--dsn and --runtime-role require --create-extension' \
  --dist "$scratch/dist" --trusted-public-key "$scratch/dist/DEV-ONLY-public-key.asc" --dsn synthetic
expect_failure 64 'runtime role must be a simple PostgreSQL identifier' \
  --dist "$scratch/dist" --trusted-public-key "$scratch/dist/DEV-ONLY-public-key.asc" \
  --create-extension --dsn synthetic --runtime-role 'bad;role'

# Exercise the accepted path with a temporary signing identity. Stub the package
# manager and PostgreSQL paths so the test never installs a system package.
command -v gpg >/dev/null 2>&1 || { echo 'gpg is required for the signing fixture' >&2; exit 69; }
mkdir -p "$scratch/bin" "$scratch/sign-home" "$scratch/sharedir/extension" "$scratch/pkglibdir" \
  "$scratch/dist/deb/pg16/amd64"
chmod 0700 "$scratch/sign-home"
cat > "$scratch/bin/pg_config" <<EOF
#!/bin/sh
case "\$1" in
  --version) echo 'PostgreSQL 16.0' ;;
  --sharedir) echo '$scratch/sharedir' ;;
  --pkglibdir) echo '$scratch/pkglibdir' ;;
  *) exit 64 ;;
esac
EOF
cat > "$scratch/bin/dpkg" <<EOF
#!/bin/sh
printf '%s\n' "\$*" > '$scratch/package-manager-called'
EOF
cat > "$scratch/bin/uname" <<'EOF'
#!/bin/sh
test "$1" = '-m' && { echo x86_64; exit 0; }
exit 64
EOF
chmod 0755 "$scratch/bin/pg_config" "$scratch/bin/dpkg" "$scratch/bin/uname"
touch "$scratch/sharedir/extension/agentsql_binder.control" \
  "$scratch/sharedir/extension/agentsql_binder--0.4.sql" \
  "$scratch/pkglibdir/agentsql_binder-0.4.so"
package="$scratch/dist/deb/pg16/amd64/agentsql-binder-pg16-fixture.deb"
printf 'synthetic package; never install\n' > "$package"
printf '{"properties":[{"name":"agentsql:postgres-major","value":"16"},{"name":"agentsql:architecture","value":"amd64"}]}\n' > "${package}.cdx.json"
gpg --batch --quiet --homedir "$scratch/sign-home" --pinentry-mode loopback --passphrase '' \
  --quick-generate-key 'AgentSQL Binder Test <binder-test@agentsql.invalid>' rsa2048 sign 1d
gpg --batch --quiet --homedir "$scratch/sign-home" --armor --export > "$scratch/trusted-outside.asc"
relative=${package#"$scratch/dist"/}
digest=$(sha256sum "$package" | awk '{print $1}')
printf '%s  %s\n' "$digest" "$relative" > "$scratch/dist/SHA256SUMS"
for artifact in "$package" "${package}.cdx.json" "$scratch/dist/SHA256SUMS"; do
  gpg --batch --quiet --homedir "$scratch/sign-home" --armor --detach-sign \
    --output "${artifact}.asc" "$artifact"
done
PATH="$scratch/bin:$PATH" sh "$installer" --dist "$scratch/dist" \
  --trusted-public-key "$scratch/trusted-outside.asc" > "$scratch/accepted.log" 2>&1
test -s "$scratch/package-manager-called" || { echo 'verified fixture did not reach package manager stub' >&2; exit 1; }
rm -f "$scratch/package-manager-called"
duplicate="$scratch/dist/deb/pg16/amd64/agentsql-binder-pg16-other.deb"
printf 'second package\n' > "$duplicate"
PATH="$scratch/bin:$PATH" expect_failure 66 'expected exactly one regular package' \
  --dist "$scratch/dist" --trusted-public-key "$scratch/trusted-outside.asc"
test ! -e "$scratch/package-manager-called" || { echo 'ambiguous package reached package manager stub' >&2; exit 1; }
rm -f "$duplicate"
printf 'tampered\n' >> "$package"
set +e
PATH="$scratch/bin:$PATH" sh "$installer" --dist "$scratch/dist" \
  --trusted-public-key "$scratch/trusted-outside.asc" > "$scratch/tampered.log" 2>&1
tampered_code=$?
set -e
test "$tampered_code" -ne 0 && test ! -e "$scratch/package-manager-called" || {
  echo 'tampered fixture reached package manager stub' >&2
  exit 1
}
printf 'BINDER_TRUST_BOUNDARY=PASS\n'

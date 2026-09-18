#!/bin/sh

set -eu
umask 022

VERSION=${VERSION:-v0.2.0}
SOURCE_DATE_EPOCH=${SOURCE_DATE_EPOCH:-1704067200}

die() {
  printf 'ERROR: %s\n' "$*" >&2
  exit 1
}

printf '%s\n' "$VERSION" | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' >/dev/null 2>&1 || die "VERSION must match vX.Y.Z exactly."
printf '%s\n' "$SOURCE_DATE_EPOCH" | grep -E '^[0-9]+$' >/dev/null 2>&1 || die "SOURCE_DATE_EPOCH must be a non-negative integer."

for pr_cmd in tar gzip sort find awk sed grep chmod cp mv mkdir mktemp basename rm; do
  command -v "$pr_cmd" >/dev/null 2>&1 || die "Required command '$pr_cmd' is missing."
done
if ! command -v sha256sum >/dev/null 2>&1 && ! command -v shasum >/dev/null 2>&1; then
  die "A SHA-256 checker is required (sha256sum or shasum)."
fi

AGENTSQL_BINARY=bin/agentsql-linux-amd64
CTL_BINARY=bin/agentsqlctl-linux-amd64
[ -f "$AGENTSQL_BINARY" ] && [ -x "$AGENTSQL_BINARY" ] || die "Missing $AGENTSQL_BINARY. Run 'make docker-linux-amd64 VERSION=$VERSION' first."
[ -f "$CTL_BINARY" ] && [ -x "$CTL_BINARY" ] || die "Missing $CTL_BINARY. Run 'make docker-linux-amd64 VERSION=$VERSION' first."
[ -f scripts/install.sh ] || die "Missing scripts/install.sh."
[ -f deploy/systemd/agentsql.service ] || die "Missing deploy/systemd/agentsql.service."
[ -f deploy/systemd/config.yaml ] || die "Missing deploy/systemd/config.yaml."
[ -f LICENSE ] || die "Missing repository LICENSE."

pr_agentsql_version=$("$AGENTSQL_BINARY" --version 2>/dev/null) || die "Could not execute $AGENTSQL_BINARY."
pr_ctl_version=$("$CTL_BINARY" version 2>/dev/null) || die "Could not execute $CTL_BINARY."
[ "$pr_agentsql_version" = "$VERSION" ] || die "$AGENTSQL_BINARY reports '$pr_agentsql_version', expected '$VERSION'."
[ "$pr_ctl_version" = "$VERSION" ] || die "$CTL_BINARY reports '$pr_ctl_version', expected '$VERSION'."

check_elf() {
  ce_binary=$1
  if command -v file >/dev/null 2>&1; then
    ce_output=$(file "$ce_binary")
    printf '%s\n' "$ce_output" | grep -E 'ELF 64-bit.*x86-64' >/dev/null 2>&1 || die "$ce_binary is not an ELF64 x86-64 binary."
    printf '%s\n' "$ce_output"
  fi
}

check_glibc_version() {
  cgv_binary=$1
  cgv_name=$2
  command -v objdump >/dev/null 2>&1 || return 0
  cgv_versions="$TMP_DIR/${cgv_name}.glibc-versions"
  cgv_comparison="$TMP_DIR/${cgv_name}.glibc-comparison"
  objdump -T "$cgv_binary" | grep -oE 'GLIBC_[0-9.]+' | sort -V -u > "$cgv_versions"
  cgv_highest=
  while IFS= read -r cgv_item; do
    [ -n "$cgv_item" ] && cgv_highest=$cgv_item
  done < "$cgv_versions"
  [ -n "$cgv_highest" ] || die "No GLIBC version symbols found in $cgv_binary."
  printf '%s\n%s\n' "$cgv_highest" GLIBC_2.28 | sort -V -u > "$cgv_comparison"
  cgv_greatest=
  while IFS= read -r cgv_item; do
    [ -n "$cgv_item" ] && cgv_greatest=$cgv_item
  done < "$cgv_comparison"
  [ "$cgv_greatest" = GLIBC_2.28 ] || die "$cgv_binary requires $cgv_highest, newer than GLIBC_2.28."
  printf '%s requires at most %s\n' "$cgv_binary" "$cgv_highest"
}

check_dependencies() {
  cd_binary=$1
  command -v ldd >/dev/null 2>&1 || return 0
  cd_output="$TMP_DIR/$(basename "$cd_binary").ldd"
  ldd "$cd_binary" > "$cd_output" 2>&1 || true
  if grep -F 'not found' "$cd_output" >/dev/null 2>&1; then
    sed 's/^/  /' "$cd_output" >&2
    die "$cd_binary has a missing shared-library dependency."
  fi
  sed 's/^/  /' "$cd_output"
}

sha_file() {
  sf_path=$1
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$sf_path" | awk '{print $1}'
  else
    shasum -a 256 "$sf_path" | awk '{print $1}'
  fi
}

check_manifest() {
  cm_dir=$1
  if command -v sha256sum >/dev/null 2>&1; then
    (cd "$cm_dir" && sha256sum -c SHA256SUMS)
  else
    (cd "$cm_dir" && shasum -a 256 -c SHA256SUMS)
  fi
}

TMP_DIR=$(mktemp -d "${TMPDIR:-/tmp}/agentsql-package.XXXXXX") || die "Could not create a temporary directory."
chmod 0700 "$TMP_DIR"
cleanup() {
  pr_status=$?
  trap - EXIT HUP INT TERM
  case "$TMP_DIR" in
    /tmp/agentsql-package.*|*/agentsql-package.*) rm -rf "$TMP_DIR" ;;
    *) printf 'WARNING: refusing to remove unexpected temporary path: %s\n' "$TMP_DIR" >&2 ;;
  esac
  exit "$pr_status"
}
trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

for pr_binary in "$AGENTSQL_BINARY" "$CTL_BINARY"; do
  check_elf "$pr_binary"
  check_glibc_version "$pr_binary" "$(basename "$pr_binary")"
  check_dependencies "$pr_binary"
done

ROOT_NAME="agentsql-${VERSION}-linux-amd64"
STAGE="$TMP_DIR/$ROOT_NAME"
mkdir -p "$STAGE/deploy/systemd"
cp "$AGENTSQL_BINARY" "$STAGE/agentsql"
cp "$CTL_BINARY" "$STAGE/agentsqlctl"
cp scripts/install.sh "$STAGE/install.sh"
cp deploy/systemd/agentsql.service "$STAGE/deploy/systemd/agentsql.service"
cp deploy/systemd/config.yaml "$STAGE/deploy/systemd/config.yaml"
cp LICENSE "$STAGE/LICENSE"
printf '%s\n' "$VERSION" > "$STAGE/VERSION"
chmod 0755 "$STAGE/agentsql" "$STAGE/agentsqlctl" "$STAGE/install.sh"
chmod 0644 "$STAGE/deploy/systemd/agentsql.service" "$STAGE/deploy/systemd/config.yaml" "$STAGE/LICENSE" "$STAGE/VERSION"

pr_file_list="$TMP_DIR/payload-files"
(cd "$STAGE" && find . -type f ! -path './SHA256SUMS' -print | sed 's#^\./##' | LC_ALL=C sort) > "$pr_file_list"
: > "$STAGE/SHA256SUMS"
while IFS= read -r pr_file; do
  [ -n "$pr_file" ] || continue
  pr_digest=$(sha_file "$STAGE/$pr_file")
  printf '%s  %s\n' "$pr_digest" "$pr_file" >> "$STAGE/SHA256SUMS"
done < "$pr_file_list"
chmod 0644 "$STAGE/SHA256SUMS"

mkdir -p dist
TARBALL_NAME="${ROOT_NAME}.tar.gz"
TARBALL_TMP="$TMP_DIR/$TARBALL_NAME"
TAR_TMP="$TMP_DIR/${ROOT_NAME}.tar"
(cd "$TMP_DIR" && tar --sort=name --owner=0 --group=0 --numeric-owner --mtime="@$SOURCE_DATE_EPOCH" --format=gnu -cf "$TAR_TMP" "$ROOT_NAME")
gzip -n -c "$TAR_TMP" > "$TARBALL_TMP"

VERIFY_DIR="$TMP_DIR/verify"
mkdir "$VERIFY_DIR"
tar -xzf "$TARBALL_TMP" -C "$VERIFY_DIR"
check_manifest "$VERIFY_DIR/$ROOT_NAME" >/dev/null
pr_verify_agentsql=$("$VERIFY_DIR/$ROOT_NAME/agentsql" --version 2>/dev/null) || die "Re-extracted agentsql could not run."
pr_verify_ctl=$("$VERIFY_DIR/$ROOT_NAME/agentsqlctl" version 2>/dev/null) || die "Re-extracted agentsqlctl could not run."
[ "$pr_verify_agentsql" = "$VERSION" ] || die "Re-extracted agentsql version mismatch."
[ "$pr_verify_ctl" = "$VERSION" ] || die "Re-extracted agentsqlctl version mismatch."

OUT_TARBALL="dist/$TARBALL_NAME"
OUT_SIDECAR="${OUT_TARBALL}.sha256"
mv "$TARBALL_TMP" "$OUT_TARBALL"
pr_outer_digest=$(sha_file "$OUT_TARBALL")
printf '%s  %s\n' "$pr_outer_digest" "$TARBALL_NAME" > "$OUT_SIDECAR"
cp scripts/install.sh "$TMP_DIR/install.sh"
chmod 0755 "$TMP_DIR/install.sh"
mv "$TMP_DIR/install.sh" dist/install.sh

printf 'Artifact: %s\n' "$OUT_TARBALL"
printf 'Checksum: %s\n' "$OUT_SIDECAR"
printf 'Bootstrap: dist/install.sh\n'
printf 'Outer SHA256: %s\n' "$pr_outer_digest"
printf 'PACKAGE_RELEASE_DONE\n'

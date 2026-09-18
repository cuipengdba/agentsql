#!/bin/sh

set -eu

GITHUB_REPO="cuipengdba/agentsql"
DEFAULT_DOWNLOAD_BASE="https://github.com/${GITHUB_REPO}/releases/download"
BOOTSTRAP_URL="https://github.com/${GITHUB_REPO}/releases/latest/download/install.sh"
INSTALLER_VERSION="1"

BIN_DIR="/usr/local/bin"
CONFIG_DIR="/etc/agentsql"
DATA_DIR="/var/lib/agentsql"
UNIT_PATH="/etc/systemd/system/agentsql.service"
STATE_PATH="${CONFIG_DIR}/install-state"
ENV_PATH="${CONFIG_DIR}/agentsql.env"
CONFIG_PATH="${CONFIG_DIR}/config.yaml"

ACTION=
VERSION=
FROM_PATH=
NO_START=0
SHOW_PASSWORD=0
ASSUME_YES=0
EXTERNAL_BACKUP_DONE=0
PURGE=0
REMOVE_USER=0
TMP_DIR=
LOCK_DIR=
PKG_ROOT=
ENV_CREATED=0
GENERATED_PASSWORD=
CREATED_USER=0
CREATED_GROUP=0
NOLOGIN_SHELL=
TX_ACTIVE=0
TX_KIND=
OLD_AGENTSQL_PRESENT=0
OLD_CTL_PRESENT=0
OLD_UNIT_PRESENT=0
OLD_ACTIVE=0
OLD_ENABLED=0
UPGRADE_BACKUP_DIR=
BACKUP_CONFIG_PRESENT=0
BACKUP_ENV_PRESENT=0
BACKUP_DB_PRESENT=0
BACKUP_WAL_PRESENT=0
BACKUP_SHM_PRESENT=0
EXTERNAL_DB_MODE=0
SERVICE_GUARD=0

say() {
  printf '%s\n' "$*"
}

warn() {
  printf 'WARNING: %s\n' "$*" >&2
}

die() {
  printf 'ERROR: %s\n' "$*" >&2
  exit 1
}

usage() {
  cat >&2 <<'EOF'
Usage:
  install.sh install   [--version vX.Y.Z | --from PATH] [--no-start] [--show-password] [--yes] [--external-backup-done]
  install.sh upgrade   [--version vX.Y.Z | --from PATH] [--no-start] [--external-backup-done]
  install.sh uninstall [--purge] [--yes] [--remove-user]
EOF
  exit 2
}

is_version() {
  printf '%s\n' "$1" | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' >/dev/null 2>&1
}

validate_version() {
  is_version "$1" || die "Version must match vX.Y.Z exactly."
}

require_root() {
  [ "$(id -u)" -eq 0 ] || die "This action requires root privileges. Run it with sudo or as root."
}

require_command() {
  rc_name=$1
  rc_package=$2
  command -v "$rc_name" >/dev/null 2>&1 || die "Required command '$rc_name' is missing. Install the '$rc_package' system package and retry."
}

state_value() {
  sv_key=$1
  [ -f "$STATE_PATH" ] || return 1
  awk -F= -v wanted="$sv_key" '$1 == wanted { print substr($0, index($0, "=") + 1); exit }' "$STATE_PATH"
}

is_managed_install() {
  [ -f "$STATE_PATH" ] && [ "$(state_value INSTALLER_VERSION 2>/dev/null || true)" = "$INSTALLER_VERSION" ]
}

cleanup_paths() {
  cleanup_status=$?
  trap - EXIT HUP INT TERM
  if [ "$TX_ACTIVE" -eq 1 ]; then
    warn "The operation was interrupted; attempting to restore the previous installation."
    rollback_transaction || warn "Automatic rollback did not complete. Preserve the host for manual recovery."
  elif [ "$SERVICE_GUARD" -eq 1 ]; then
    warn "The upgrade stopped before replacement; restoring the previous service state."
    restore_service_state || warn "The previous service state could not be restored automatically."
  fi
  rm -f "$BIN_DIR/.agentsql.new.$$" "$BIN_DIR/.agentsqlctl.new.$$" "/etc/systemd/system/.agentsql.service.new.$$" 2>/dev/null || true
  if [ -n "$TMP_DIR" ]; then
    case "$TMP_DIR" in
      /tmp/agentsql-install.*|*/agentsql-install.*) rm -rf "$TMP_DIR" ;;
      *) warn "Refusing to remove unexpected temporary path: $TMP_DIR" ;;
    esac
  fi
  if [ -n "$LOCK_DIR" ]; then
    rmdir "$LOCK_DIR" 2>/dev/null || true
  fi
  exit "$cleanup_status"
}

trap cleanup_paths EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

parse_arguments() {
  [ "$#" -ge 1 ] || usage
  ACTION=$1
  shift
  case "$ACTION" in
    install|upgrade|uninstall) ;;
    *) usage ;;
  esac

  while [ "$#" -gt 0 ]; do
    case "$1" in
      --version)
        [ "$ACTION" != uninstall ] || usage
        [ "$#" -ge 2 ] || usage
        [ -z "$VERSION" ] || die "--version may be specified only once."
        VERSION=$2
        shift 2
        ;;
      --from)
        [ "$ACTION" != uninstall ] || usage
        [ "$#" -ge 2 ] || usage
        [ -z "$FROM_PATH" ] || die "--from may be specified only once."
        FROM_PATH=$2
        shift 2
        ;;
      --no-start)
        [ "$ACTION" != uninstall ] || usage
        NO_START=1
        shift
        ;;
      --show-password)
        [ "$ACTION" = install ] || usage
        SHOW_PASSWORD=1
        shift
        ;;
      --yes)
        [ "$ACTION" != upgrade ] || usage
        ASSUME_YES=1
        shift
        ;;
      --external-backup-done)
        [ "$ACTION" != uninstall ] || usage
        EXTERNAL_BACKUP_DONE=1
        shift
        ;;
      --purge)
        [ "$ACTION" = uninstall ] || usage
        PURGE=1
        shift
        ;;
      --remove-user)
        [ "$ACTION" = uninstall ] || usage
        REMOVE_USER=1
        shift
        ;;
      *) usage ;;
    esac
  done

  if [ -n "$VERSION" ] && [ -n "$FROM_PATH" ]; then
    die "--version and --from are mutually exclusive."
  fi
  if [ -n "$VERSION" ]; then
    validate_version "$VERSION"
  fi
  if [ "$REMOVE_USER" -eq 1 ] && [ "$PURGE" -ne 1 ]; then
    die "--remove-user is allowed only together with --purge."
  fi
}

detect_platform() {
  [ "$(uname -s)" = Linux ] || die "Only Linux is supported by this installer."
  dp_arch=$(uname -m)
  case "$dp_arch" in
    x86_64|amd64) ;;
    *) die "Native packages are available only for x86_64/amd64. ARM64 and other hosts must use an x86 container with emulation or a supported amd64 host." ;;
  esac

  dp_musl=0
  [ -f /etc/alpine-release ] && dp_musl=1
  if command -v ldd >/dev/null 2>&1; then
    dp_ldd_output=$(LC_ALL=C ldd --version 2>&1 || true)
    printf '%s\n' "$dp_ldd_output" | grep -i musl >/dev/null 2>&1 && dp_musl=1
  fi
  if command -v readelf >/dev/null 2>&1 && [ -r /proc/self/exe ]; then
    dp_interp=$(readelf -l /proc/self/exe 2>/dev/null | sed -n 's/.*Requesting program interpreter: \([^]]*\).*/\1/p' | sed -n '1p')
    if [ -n "$dp_interp" ]; then
      case "$dp_interp" in
        *ld-linux*|*/ld64.so*) ;;
        *) dp_musl=1 ;;
      esac
    fi
  fi
  [ "$dp_musl" -eq 0 ] || die "musl/Alpine is not supported by the native package. Use the Debian-based glibc container image instead."

  dp_glibc=
  if command -v getconf >/dev/null 2>&1; then
    dp_getconf=$(getconf GNU_LIBC_VERSION 2>/dev/null || true)
    case "$dp_getconf" in
      glibc\ *) dp_glibc=${dp_getconf#glibc } ;;
    esac
  fi
  if [ -z "$dp_glibc" ]; then
    command -v ldd >/dev/null 2>&1 || die "Cannot determine glibc version. Install the libc-bin or glibc-common package."
    dp_first_line=$(LC_ALL=C ldd --version 2>&1 | sed -n '1p')
    dp_glibc=$(printf '%s\n' "$dp_first_line" | sed -n 's/^[^0-9]*\([0-9][0-9]*\.[0-9][0-9]*\).*$/\1/p')
  fi
  printf '%s\n' "$dp_glibc" | grep -E '^[0-9]+\.[0-9]+' >/dev/null 2>&1 || die "Could not parse the glibc version from the host."
  dp_major=$(printf '%s\n' "$dp_glibc" | cut -d. -f1)
  dp_minor=$(printf '%s\n' "$dp_glibc" | cut -d. -f2 | sed 's/[^0-9].*$//')
  dp_major=$(printf '%s\n' "$dp_major" | sed 's/^0*//')
  dp_minor=$(printf '%s\n' "$dp_minor" | sed 's/^0*//')
  [ -n "$dp_major" ] || dp_major=0
  [ -n "$dp_minor" ] || dp_minor=0
  if [ "$dp_major" -lt 2 ] || { [ "$dp_major" -eq 2 ] && [ "$dp_minor" -lt 28 ]; }; then
    die "glibc 2.28 or newer is required; this host reports glibc $dp_glibc. CentOS 7 is not supported."
  fi
}

check_dependencies() {
  for cd_item in "awk:awk" "sed:sed" "grep:grep" "cut:coreutils" "tr:coreutils" "head:coreutils" "sort:coreutils" "wc:coreutils" "id:coreutils" "mktemp:coreutils" "chmod:coreutils" "chown:coreutils" "cp:coreutils" "mv:coreutils" "rm:coreutils" "mkdir:coreutils" "date:coreutils" "stat:coreutils" "basename:coreutils" "dirname:coreutils" "find:findutils" "cmp:diffutils"; do
    cd_cmd=${cd_item%%:*}
    cd_pkg=${cd_item#*:}
    require_command "$cd_cmd" "$cd_pkg"
  done

  if [ "$ACTION" != uninstall ]; then
    require_command tar tar
    require_command gzip gzip
    require_command install coreutils
    require_command getent "libc-bin (Debian/Ubuntu) or glibc-common (RHEL-compatible systems)"
    require_command groupadd "passwd (Debian/Ubuntu) or shadow-utils (RHEL-compatible systems)"
    require_command useradd "passwd (Debian/Ubuntu) or shadow-utils (RHEL-compatible systems)"
    require_command sleep coreutils
    if ! command -v sha256sum >/dev/null 2>&1 && ! command -v shasum >/dev/null 2>&1; then
      die "A SHA-256 checker is required. Install coreutils (sha256sum) or the package providing shasum."
    fi
    if ! command -v openssl >/dev/null 2>&1; then
      require_command base64 coreutils
    fi
    if [ -z "$FROM_PATH" ]; then
      require_command curl curl
    fi
  fi

  if [ "$ACTION" = uninstall ] && [ "$PURGE" -eq 1 ]; then
    require_command mountpoint util-linux
    require_command find findutils
  fi
  if [ "$ACTION" = uninstall ] && [ "$REMOVE_USER" -eq 1 ]; then
    require_command userdel "passwd (Debian/Ubuntu) or shadow-utils (RHEL-compatible systems)"
    require_command groupdel "passwd (Debian/Ubuntu) or shadow-utils (RHEL-compatible systems)"
    require_command getent "libc-bin (Debian/Ubuntu) or glibc-common (RHEL-compatible systems)"
  fi
}

check_systemd_gate() {
  [ "$ACTION" != install ] && [ "$ACTION" != upgrade ] && return 0
  if [ "$NO_START" -eq 1 ]; then
    return 0
  fi
  command -v systemctl >/dev/null 2>&1 || die "systemd is required for a started installation. Use --no-start only for chroot or image preinstallation."
  [ -d /run/systemd/system ] || die "systemd is not PID 1 on this host. Use --no-start only for chroot or image preinstallation."
}

acquire_lock_and_temp() {
  if [ -d /run ] && [ -w /run ]; then
    LOCK_DIR=/run/agentsql-install.lock
  else
    LOCK_DIR=/tmp/agentsql-install.lock
  fi
  if ! mkdir "$LOCK_DIR" 2>/dev/null; then
    die "Another AgentSQL installer instance is already running (lock: $LOCK_DIR)."
  fi
  chmod 0700 "$LOCK_DIR"
  TMP_DIR=$(mktemp -d "${TMPDIR:-/tmp}/agentsql-install.XXXXXX") || die "Could not create a private temporary directory."
  chmod 0700 "$TMP_DIR"
}

systemd_available() {
  command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]
}

find_self_package() {
  fsp_dir=
  case "$0" in
    */*) fsp_dir=$(CDPATH= cd -- "$(dirname -- "$0")" 2>/dev/null && pwd -P || true) ;;
  esac
  if [ -n "$fsp_dir" ] && [ -f "$fsp_dir/VERSION" ] && [ -x "$fsp_dir/agentsql" ] && [ -x "$fsp_dir/agentsqlctl" ] && [ -f "$fsp_dir/SHA256SUMS" ]; then
    FROM_PATH=$fsp_dir
  fi
}

resolve_latest_version() {
  rlv_url=$(curl -fsSLo /dev/null -w '%{url_effective}' "https://github.com/${GITHUB_REPO}/releases/latest") || die "Could not resolve the latest GitHub Release."
  rlv_prefix="https://github.com/${GITHUB_REPO}/releases/tag/"
  case "$rlv_url" in
    "${rlv_prefix}"*) VERSION=${rlv_url#"$rlv_prefix"} ;;
    *) die "GitHub latest redirected to an unexpected URL: $rlv_url" ;;
  esac
  validate_version "$VERSION"
  [ "$rlv_url" = "${rlv_prefix}${VERSION}" ] || die "GitHub latest URL contains unexpected trailing data."
}

sha256_check_file() {
  scf_manifest=$1
  scf_directory=$2
  if command -v sha256sum >/dev/null 2>&1; then
    (cd "$scf_directory" && sha256sum -c "$scf_manifest")
  else
    (cd "$scf_directory" && shasum -a 256 -c "$scf_manifest")
  fi
}

verify_outer_checksum() {
  voc_tar=$1
  voc_sidecar=$2
  [ -f "$voc_sidecar" ] || die "Missing outer checksum sidecar: $voc_sidecar"
  [ "$(wc -l < "$voc_sidecar" | tr -d ' ')" -eq 1 ] || die "The outer SHA-256 sidecar must contain exactly one line."
  [ "$(awk 'NR == 1 { print NF }' "$voc_sidecar")" -eq 2 ] || die "The outer SHA-256 sidecar is malformed."
  voc_hash=$(awk 'NF == 2 { print $1; exit }' "$voc_sidecar")
  voc_named=$(awk 'NF == 2 { print $2; exit }' "$voc_sidecar")
  [ "$(printf '%s' "$voc_hash" | wc -c | tr -d ' ')" -eq 64 ] || die "The outer SHA-256 sidecar is malformed."
  printf '%s\n' "$voc_hash" | grep -E '^[0-9A-Fa-f]+$' >/dev/null 2>&1 || die "The outer SHA-256 sidecar contains an invalid digest."
  case "$voc_named" in
    \** ) voc_named=${voc_named#\*} ;;
  esac
  [ "$voc_named" = "$(basename "$voc_tar")" ] || die "The checksum sidecar names an unexpected file."
  voc_check_dir="$TMP_DIR/outer-check"
  mkdir "$voc_check_dir"
  cp "$voc_tar" "$voc_check_dir/$(basename "$voc_tar")"
  printf '%s  %s\n' "$voc_hash" "$(basename "$voc_tar")" > "$voc_check_dir/check.sha256"
  if ! sha256_check_file check.sha256 "$voc_check_dir" >/dev/null 2>&1; then
    die "Outer tarball SHA-256 verification failed. The installer will not try another source."
  fi
}

inspect_tarball() {
  it_tar=$1
  it_types="$TMP_DIR/tar-types"
  it_names="$TMP_DIR/tar-names"
  tar -tvzf "$it_tar" > "$it_types" || die "Could not list tarball metadata."
  tar -tzf "$it_tar" > "$it_names" || die "Could not list tarball members."
  [ -s "$it_names" ] || die "The release tarball is empty."
  while IFS= read -r it_line; do
    case "$it_line" in
      d*|-*) ;;
      *) die "The release tarball contains a link, device, or unsupported member type." ;;
    esac
  done < "$it_types"
  it_top=
  while IFS= read -r it_member; do
    [ -n "$it_member" ] || die "The release tarball contains an empty member name."
    case "$it_member" in
      /*|*\\*|*' '*|*"	"*) die "The release tarball contains an unsafe member name." ;;
      ..|../*|*/..|*/../*|*/./*|./*|*//* ) die "The release tarball contains path traversal or a non-canonical path." ;;
    esac
    it_member_top=${it_member%%/*}
    if [ -z "$it_top" ]; then
      it_top=$it_member_top
    elif [ "$it_top" != "$it_member_top" ]; then
      die "The release tarball contains more than one top-level directory."
    fi
  done < "$it_names"
  case "$it_top" in
    agentsql-v*-linux-amd64) ;;
    *) die "The release tarball top-level directory has an unexpected name." ;;
  esac
  it_inferred=${it_top#agentsql-}
  it_inferred=${it_inferred%-linux-amd64}
  validate_version "$it_inferred"
  if [ -n "$VERSION" ] && [ "$VERSION" != "$it_inferred" ]; then
    die "The tarball version $it_inferred does not match requested version $VERSION."
  fi
  VERSION=$it_inferred
  EXPECTED_TOP=$it_top
}

validate_manifest_path() {
  vmp_path=$1
  case "$vmp_path" in
    ''|/*|..|../*|*/..|*/../*|./*|*/./*|*\\*|*' '*) return 1 ;;
  esac
  return 0
}

verify_package_root() {
  vpr_root=$1
  [ -d "$vpr_root" ] || die "Package root is not a directory: $vpr_root"
  [ ! -L "$vpr_root" ] || die "Package root must not be a symbolic link."
  [ "$(basename "$vpr_root")" = "agentsql-${VERSION}-linux-amd64" ] || die "Package directory name does not match its VERSION."
  for vpr_expected in agentsql agentsqlctl install.sh deploy/systemd/agentsql.service deploy/systemd/config.yaml LICENSE VERSION SHA256SUMS; do
    [ -f "$vpr_root/$vpr_expected" ] || die "Release package is missing $vpr_expected."
    [ ! -L "$vpr_root/$vpr_expected" ] || die "Release package file must not be a symbolic link: $vpr_expected"
  done
  [ -z "$(find "$vpr_root" -type l -print -quit 2>/dev/null)" ] || die "Release package directories must not contain symbolic links."

  vpr_manifest_list="$TMP_DIR/manifest-files"
  awk '
    NF != 2 { exit 2 }
    length($1) != 64 || $1 !~ /^[0-9A-Fa-f]+$/ { exit 2 }
    { print $2 }
  ' "$vpr_root/SHA256SUMS" > "$vpr_manifest_list" || die "Package SHA256SUMS is malformed."
  [ -s "$vpr_manifest_list" ] || die "Package SHA256SUMS is empty."
  while IFS= read -r vpr_file; do
    validate_manifest_path "$vpr_file" || die "Package SHA256SUMS contains an unsafe path."
    [ -f "$vpr_root/$vpr_file" ] && [ ! -L "$vpr_root/$vpr_file" ] || die "Package SHA256SUMS references a missing or unsafe file: $vpr_file"
  done < "$vpr_manifest_list"
  vpr_manifest_sorted="$TMP_DIR/manifest-files-sorted"
  vpr_actual_sorted="$TMP_DIR/package-files-sorted"
  LC_ALL=C sort -u "$vpr_manifest_list" > "$vpr_manifest_sorted"
  [ "$(wc -l < "$vpr_manifest_list" | tr -d ' ')" = "$(wc -l < "$vpr_manifest_sorted" | tr -d ' ')" ] || die "Package SHA256SUMS contains duplicate entries."
  (cd "$vpr_root" && find . -type f ! -path './SHA256SUMS' -print | sed 's#^\./##' | LC_ALL=C sort) > "$vpr_actual_sorted"
  cmp -s "$vpr_manifest_sorted" "$vpr_actual_sorted" || die "Package SHA256SUMS does not cover every payload file exactly once."
  for vpr_expected in agentsql agentsqlctl install.sh deploy/systemd/agentsql.service deploy/systemd/config.yaml LICENSE VERSION; do
    grep -F "  $vpr_expected" "$vpr_root/SHA256SUMS" >/dev/null 2>&1 || die "Package SHA256SUMS does not cover $vpr_expected."
  done
  if ! sha256_check_file SHA256SUMS "$vpr_root" >/dev/null 2>&1; then
    die "Package payload SHA-256 verification failed."
  fi

  vpr_file_version=$(sed -n '1p' "$vpr_root/VERSION")
  [ "$vpr_file_version" = "$VERSION" ] || die "Package VERSION does not match requested version $VERSION."
  [ "$(wc -l < "$vpr_root/VERSION" | tr -d ' ')" -eq 1 ] || die "Package VERSION must contain exactly one line."
  vpr_agentsql_version=$("$vpr_root/agentsql" --version 2>/dev/null) || die "Candidate agentsql binary could not run."
  vpr_ctl_version=$("$vpr_root/agentsqlctl" version 2>/dev/null) || die "Candidate agentsqlctl binary could not run."
  [ "$vpr_agentsql_version" = "$VERSION" ] || die "Candidate agentsql reports version '$vpr_agentsql_version', expected '$VERSION'."
  [ "$vpr_ctl_version" = "$VERSION" ] || die "Candidate agentsqlctl reports version '$vpr_ctl_version', expected '$VERSION'."

  for vpr_binary in agentsql agentsqlctl; do
    if command -v file >/dev/null 2>&1; then
      vpr_file_output=$(file "$vpr_root/$vpr_binary")
      printf '%s\n' "$vpr_file_output" | grep -E 'ELF 64-bit.*x86-64' >/dev/null 2>&1 || die "$vpr_binary is not an ELF64 x86-64 binary."
    fi
    if command -v readelf >/dev/null 2>&1; then
      readelf -h "$vpr_root/$vpr_binary" 2>/dev/null | grep -E 'Class:[[:space:]]+ELF64' >/dev/null 2>&1 || die "$vpr_binary is not ELF64."
      readelf -h "$vpr_root/$vpr_binary" 2>/dev/null | grep -E 'Machine:[[:space:]]+(Advanced Micro Devices X86-64|AMD x86-64)' >/dev/null 2>&1 || die "$vpr_binary is not x86-64."
    fi
  done
  PKG_ROOT=$vpr_root
}

acquire_release() {
  if [ -n "$FROM_PATH" ]; then
    if [ -d "$FROM_PATH" ]; then
      ar_root=$(CDPATH= cd -- "$FROM_PATH" 2>/dev/null && pwd -P) || die "Could not resolve package directory: $FROM_PATH"
      [ -f "$ar_root/VERSION" ] || die "Local package directory is missing VERSION."
      VERSION=$(sed -n '1p' "$ar_root/VERSION")
      validate_version "$VERSION"
      verify_package_root "$ar_root"
      return
    fi
    [ -f "$FROM_PATH" ] || die "Local package path does not exist: $FROM_PATH"
    [ ! -L "$FROM_PATH" ] || die "Local package tarball must not be a symbolic link."
    ar_tar=$(CDPATH= cd -- "$(dirname -- "$FROM_PATH")" 2>/dev/null && pwd -P)/$(basename "$FROM_PATH")
    verify_outer_checksum "$ar_tar" "${ar_tar}.sha256"
    inspect_tarball "$ar_tar"
    ar_extract="$TMP_DIR/extracted"
    mkdir "$ar_extract"
    tar -xzf "$ar_tar" -C "$ar_extract" || die "Could not extract the verified release tarball."
    verify_package_root "$ar_extract/$EXPECTED_TOP"
    return
  fi

  if [ -z "$VERSION" ]; then
    resolve_latest_version
  fi
  ar_base=${AGENTSQL_DOWNLOAD_BASE:-$DEFAULT_DOWNLOAD_BASE}
  case "$ar_base" in
    https://*) ;;
    *) die "AGENTSQL_DOWNLOAD_BASE must use HTTPS." ;;
  esac
  ar_base=${ar_base%/}
  ar_asset="agentsql-${VERSION}-linux-amd64.tar.gz"
  ar_tar="$TMP_DIR/$ar_asset"
  ar_sidecar="${ar_tar}.sha256"
  ar_url="${ar_base}/${VERSION}/${ar_asset}"
  say "Downloading AgentSQL $VERSION from $ar_base"
  curl -fsSL -o "$ar_tar" "$ar_url" || die "Could not download $ar_url. No fallback source will be attempted."
  curl -fsSL -o "$ar_sidecar" "${ar_url}.sha256" || die "Could not download ${ar_url}.sha256. No fallback source will be attempted."
  verify_outer_checksum "$ar_tar" "$ar_sidecar"
  inspect_tarball "$ar_tar"
  ar_extract="$TMP_DIR/extracted"
  mkdir "$ar_extract"
  tar -xzf "$ar_tar" -C "$ar_extract" || die "Could not extract the verified release tarball."
  verify_package_root "$ar_extract/$EXPECTED_TOP"
}

reject_managed_symlinks() {
  for rms_path in "$BIN_DIR" "$CONFIG_DIR" "$DATA_DIR" "$BIN_DIR/agentsql" "$BIN_DIR/agentsqlctl" "$UNIT_PATH" "$CONFIG_PATH" "$ENV_PATH" "$STATE_PATH" "$DATA_DIR/agentsql.db" "$DATA_DIR/agentsql.db-wal" "$DATA_DIR/agentsql.db-shm"; do
    [ ! -L "$rms_path" ] || die "Managed path must not be a symbolic link: $rms_path"
  done
}

prepare_service_account() {
  psa_group_entry=$(getent group agentsql 2>/dev/null || true)
  psa_user_entry=$(getent passwd agentsql 2>/dev/null || true)
  if [ -n "$psa_user_entry" ]; then
    [ -n "$psa_group_entry" ] || die "User 'agentsql' already exists but group 'agentsql' does not. Resolve this account collision manually."
    psa_uid=$(printf '%s\n' "$psa_user_entry" | cut -d: -f3)
    psa_user_gid=$(printf '%s\n' "$psa_user_entry" | cut -d: -f4)
    psa_group_gid=$(printf '%s\n' "$psa_group_entry" | cut -d: -f3)
    if [ "$psa_uid" -ge 1000 ] || [ "$psa_user_gid" != "$psa_group_gid" ]; then
      die "An incompatible ordinary or mismatched 'agentsql' user already exists. Rename or remove it manually before installation."
    fi
    CREATED_USER=$(state_value CREATED_USER 2>/dev/null || printf '0')
    CREATED_GROUP=$(state_value CREATED_GROUP 2>/dev/null || printf '0')
    NOLOGIN_SHELL=$(printf '%s\n' "$psa_user_entry" | cut -d: -f7)
    return
  fi

  if [ -z "$psa_group_entry" ]; then
    groupadd --system agentsql || die "Could not create system group 'agentsql'."
    CREATED_GROUP=1
  fi
  if [ -x /usr/sbin/nologin ]; then
    NOLOGIN_SHELL=/usr/sbin/nologin
  elif [ -x /sbin/nologin ]; then
    NOLOGIN_SHELL=/sbin/nologin
  elif command -v nologin >/dev/null 2>&1; then
    NOLOGIN_SHELL=$(command -v nologin)
  else
    NOLOGIN_SHELL=/bin/false
    warn "nologin was not found; /bin/false will be used for the service account."
  fi
  useradd --system --gid agentsql --home-dir "$DATA_DIR" --no-create-home --shell "$NOLOGIN_SHELL" agentsql || die "Could not create system user 'agentsql'."
  CREATED_USER=1
}

ensure_directories() {
  install -d -o root -g agentsql -m 0750 "$CONFIG_DIR"
  install -d -o agentsql -g agentsql -m 0750 "$DATA_DIR"
  ed_config_stat=$(stat -c '%a:%U:%G' "$CONFIG_DIR")
  ed_data_stat=$(stat -c '%a:%U:%G' "$DATA_DIR")
  [ "$ed_config_stat" = "750:root:agentsql" ] || die "Could not enforce ownership and mode on $CONFIG_DIR."
  [ "$ed_data_stat" = "750:agentsql:agentsql" ] || die "Could not enforce ownership and mode on $DATA_DIR."
}

generate_random_value() {
  grv_bytes=$1
  if command -v openssl >/dev/null 2>&1; then
    openssl rand -base64 "$grv_bytes" | tr -d '\r\n'
  else
    head -c "$grv_bytes" /dev/urandom | base64 | tr -d '\r\n'
  fi
}

ensure_configuration() {
  if [ ! -e "$CONFIG_PATH" ]; then
    install -o root -g agentsql -m 0640 "$PKG_ROOT/deploy/systemd/config.yaml" "$CONFIG_PATH"
  fi
  if [ ! -e "$ENV_PATH" ]; then
    ec_secret=$(generate_random_value 24)
    GENERATED_PASSWORD=$(generate_random_value 18)
    [ "$(printf '%s' "$ec_secret" | wc -c | tr -d ' ')" -eq 32 ] || die "Generated AGENTSQL_SECRET did not contain exactly 32 ASCII bytes."
    [ "$(printf '%s' "$GENERATED_PASSWORD" | wc -c | tr -d ' ')" -ge 12 ] || die "Generated administrator password was unexpectedly short."
    ec_old_umask=$(umask)
    umask 077
    ec_tmp="${CONFIG_DIR}/.agentsql.env.new.$$"
    printf 'AGENTSQL_SECRET=%s\nAGENTSQL_ADMIN_USER=admin\nAGENTSQL_ADMIN_PASSWORD=%s\n' "$ec_secret" "$GENERATED_PASSWORD" > "$ec_tmp"
    chown root:root "$ec_tmp"
    chmod 0600 "$ec_tmp"
    mv "$ec_tmp" "$ENV_PATH"
    umask "$ec_old_umask"
    ENV_CREATED=1
    ec_secret=
  fi
}

record_service_state() {
  OLD_ACTIVE=0
  OLD_ENABLED=0
  if systemd_available; then
    systemctl is-active --quiet agentsql 2>/dev/null && OLD_ACTIVE=1 || true
    systemctl is-enabled --quiet agentsql 2>/dev/null && OLD_ENABLED=1 || true
  fi
}

stage_replacements() {
  sr_agent_new="$BIN_DIR/.agentsql.new.$$"
  sr_ctl_new="$BIN_DIR/.agentsqlctl.new.$$"
  sr_unit_new="/etc/systemd/system/.agentsql.service.new.$$"
  cp "$PKG_ROOT/agentsql" "$sr_agent_new"
  cp "$PKG_ROOT/agentsqlctl" "$sr_ctl_new"
  cp "$PKG_ROOT/deploy/systemd/agentsql.service" "$sr_unit_new"
  chown root:root "$sr_agent_new" "$sr_ctl_new" "$sr_unit_new"
  chmod 0755 "$sr_agent_new" "$sr_ctl_new"
  chmod 0644 "$sr_unit_new"
  [ "$("$sr_agent_new" --version 2>/dev/null)" = "$VERSION" ] || die "Staged agentsql binary failed version verification."
  [ "$("$sr_ctl_new" version 2>/dev/null)" = "$VERSION" ] || die "Staged agentsqlctl binary failed version verification."
}

activate_replacements() {
  OLD_AGENTSQL_PRESENT=0
  OLD_CTL_PRESENT=0
  OLD_UNIT_PRESENT=0
  TX_ACTIVE=1
  if [ -e "$BIN_DIR/agentsql" ]; then
    OLD_AGENTSQL_PRESENT=1
    mv "$BIN_DIR/agentsql" "$BIN_DIR/.agentsql.backup.$$"
  fi
  if [ -e "$BIN_DIR/agentsqlctl" ]; then
    OLD_CTL_PRESENT=1
    mv "$BIN_DIR/agentsqlctl" "$BIN_DIR/.agentsqlctl.backup.$$"
  fi
  if [ -e "$UNIT_PATH" ]; then
    OLD_UNIT_PRESENT=1
    mv "$UNIT_PATH" "/etc/systemd/system/.agentsql.service.backup.$$"
  fi
  mv "$BIN_DIR/.agentsql.new.$$" "$BIN_DIR/agentsql"
  mv "$BIN_DIR/.agentsqlctl.new.$$" "$BIN_DIR/agentsqlctl"
  mv "/etc/systemd/system/.agentsql.service.new.$$" "$UNIT_PATH"
}

remove_transaction_backups() {
  rm -f "$BIN_DIR/.agentsql.backup.$$" "$BIN_DIR/.agentsqlctl.backup.$$" "/etc/systemd/system/.agentsql.service.backup.$$"
}

restore_replacement_files() {
  rrf_ok=0
  if [ "$OLD_AGENTSQL_PRESENT" -eq 1 ]; then
    if [ -e "$BIN_DIR/.agentsql.backup.$$" ]; then
      rm -f "$BIN_DIR/agentsql" || rrf_ok=1
      mv "$BIN_DIR/.agentsql.backup.$$" "$BIN_DIR/agentsql" || rrf_ok=1
    fi
  else
    rm -f "$BIN_DIR/agentsql" "$BIN_DIR/.agentsql.backup.$$" || rrf_ok=1
  fi
  if [ "$OLD_CTL_PRESENT" -eq 1 ]; then
    if [ -e "$BIN_DIR/.agentsqlctl.backup.$$" ]; then
      rm -f "$BIN_DIR/agentsqlctl" || rrf_ok=1
      mv "$BIN_DIR/.agentsqlctl.backup.$$" "$BIN_DIR/agentsqlctl" || rrf_ok=1
    fi
  else
    rm -f "$BIN_DIR/agentsqlctl" "$BIN_DIR/.agentsqlctl.backup.$$" || rrf_ok=1
  fi
  if [ "$OLD_UNIT_PRESENT" -eq 1 ]; then
    if [ -e "/etc/systemd/system/.agentsql.service.backup.$$" ]; then
      rm -f "$UNIT_PATH" || rrf_ok=1
      mv "/etc/systemd/system/.agentsql.service.backup.$$" "$UNIT_PATH" || rrf_ok=1
    fi
  else
    rm -f "$UNIT_PATH" "/etc/systemd/system/.agentsql.service.backup.$$" || rrf_ok=1
  fi
  rm -f "$BIN_DIR/.agentsql.new.$$" "$BIN_DIR/.agentsqlctl.new.$$" "/etc/systemd/system/.agentsql.service.new.$$" || rrf_ok=1
  [ "$rrf_ok" -eq 0 ]
}

restore_service_state() {
  systemd_available || return 0
  systemctl daemon-reload || return 1
  if [ "$OLD_ENABLED" -eq 1 ]; then
    systemctl enable agentsql >/dev/null 2>&1 || return 1
  else
    systemctl disable agentsql >/dev/null 2>&1 || true
  fi
  if [ "$OLD_ACTIVE" -eq 1 ]; then
    systemctl start agentsql || return 1
  fi
}

health_check_version() {
  hcv_expected=$1
  hcv_count=0
  while [ "$hcv_count" -lt 30 ]; do
    if systemctl is-active --quiet agentsql 2>/dev/null; then
      if "$BIN_DIR/agentsqlctl" health --url http://127.0.0.1:7780/healthz > "$TMP_DIR/health.json" 2>/dev/null; then
        tr -d ' \t\r\n' < "$TMP_DIR/health.json" > "$TMP_DIR/health-normalized.json"
        if grep -F '"version":"'"$hcv_expected"'"' "$TMP_DIR/health-normalized.json" >/dev/null 2>&1; then
          if command -v curl >/dev/null 2>&1; then
            curl -fsS http://127.0.0.1:7780/readyz >/dev/null 2>&1 || warn "The optional readiness probe is not ready yet; the required health probe passed."
          fi
          return 0
        fi
      fi
    fi
    hcv_count=$((hcv_count + 1))
    sleep 2
  done
  return 1
}

restore_backup_file() {
  rbf_source=$1
  rbf_target=$2
  rbf_stage="${rbf_target}.rollback.$$"
  cp -p "$rbf_source" "$rbf_stage" || return 1
  mv "$rbf_stage" "$rbf_target" || return 1
}

restore_upgrade_payload() {
  [ -n "$UPGRADE_BACKUP_DIR" ] || return 1
  rup_stage="$TMP_DIR/rollback"
  mkdir -p "$rup_stage"
  for rup_name in agentsql agentsqlctl; do
    if [ -f "$UPGRADE_BACKUP_DIR/$rup_name" ]; then
      restore_backup_file "$UPGRADE_BACKUP_DIR/$rup_name" "$BIN_DIR/$rup_name" || return 1
    else
      rm -f "$BIN_DIR/$rup_name" || return 1
    fi
  done
  if [ -f "$UPGRADE_BACKUP_DIR/agentsql.service" ]; then
    restore_backup_file "$UPGRADE_BACKUP_DIR/agentsql.service" "$UNIT_PATH" || return 1
  else
    rm -f "$UNIT_PATH" || return 1
  fi
  if [ "$BACKUP_CONFIG_PRESENT" -eq 1 ]; then
    restore_backup_file "$UPGRADE_BACKUP_DIR/config.yaml" "$CONFIG_PATH" || return 1
  else
    rm -f "$CONFIG_PATH" || return 1
  fi
  if [ "$BACKUP_ENV_PRESENT" -eq 1 ]; then
    restore_backup_file "$UPGRADE_BACKUP_DIR/agentsql.env" "$ENV_PATH" || return 1
  else
    rm -f "$ENV_PATH" || return 1
  fi
  for rup_db_name in agentsql.db agentsql.db-wal agentsql.db-shm; do
    case "$rup_db_name" in
      agentsql.db) rup_present=$BACKUP_DB_PRESENT ;;
      agentsql.db-wal) rup_present=$BACKUP_WAL_PRESENT ;;
      agentsql.db-shm) rup_present=$BACKUP_SHM_PRESENT ;;
    esac
    if [ "$rup_present" -eq 1 ]; then
      restore_backup_file "$UPGRADE_BACKUP_DIR/$rup_db_name" "$DATA_DIR/$rup_db_name" || return 1
    else
      rm -f "$DATA_DIR/$rup_db_name" || return 1
    fi
  done
  return 0
}

rollback_transaction() {
  rbt_ok=0
  if systemd_available; then
    systemctl stop agentsql >/dev/null 2>&1 || true
  fi
  if [ "$TX_KIND" = upgrade ] && [ -n "$UPGRADE_BACKUP_DIR" ]; then
    restore_upgrade_payload || rbt_ok=1
    rm -f "$BIN_DIR/.agentsql.backup.$$" "$BIN_DIR/.agentsqlctl.backup.$$" "/etc/systemd/system/.agentsql.service.backup.$$"
  else
    restore_replacement_files || rbt_ok=1
  fi
  if [ "$TX_KIND" = upgrade ] && [ "$EXTERNAL_DB_MODE" -eq 1 ]; then
    if systemd_available; then
      systemctl daemon-reload || rbt_ok=1
      if [ "$OLD_ENABLED" -eq 1 ]; then systemctl enable agentsql >/dev/null 2>&1 || rbt_ok=1; else systemctl disable agentsql >/dev/null 2>&1 || true; fi
    fi
    warn "Previous files were restored, but the service remains stopped. Restore the external database backup before starting the old version."
    rbt_ok=1
  else
    restore_service_state || rbt_ok=1
  fi
  TX_ACTIVE=0
  SERVICE_GUARD=0
  if [ "$rbt_ok" -ne 0 ]; then
    return 1
  fi
  if [ "$OLD_ACTIVE" -eq 1 ]; then
    rbt_old_version=$("$BIN_DIR/agentsql" --version 2>/dev/null || true)
    if [ -n "$rbt_old_version" ] && health_check_version "$rbt_old_version"; then
      say "Previous AgentSQL version was restored and is healthy."
      return 0
    fi
    warn "Previous files were restored, but the old service did not become healthy. Backup: $UPGRADE_BACKUP_DIR"
    return 1
  fi
  say "Previous AgentSQL files and service state were restored."
  return 0
}

apply_selinux_contexts() {
  if command -v restorecon >/dev/null 2>&1; then
    restorecon -F "$BIN_DIR/agentsql" "$BIN_DIR/agentsqlctl" "$UNIT_PATH" 2>/dev/null || warn "restorecon reported an error on installed files."
    restorecon -RF "$CONFIG_DIR" "$DATA_DIR" 2>/dev/null || warn "restorecon reported an error on AgentSQL directories."
  fi
  if command -v getenforce >/dev/null 2>&1 && [ "$(getenforce 2>/dev/null || true)" = Enforcing ]; then
    say "SELinux is enforcing. If startup is denied, inspect AVC records with: ausearch -m AVC"
  fi
}

write_install_state() {
  wis_tmp="${CONFIG_DIR}/.install-state.new.$$"
  {
    printf 'INSTALLER_VERSION=%s\n' "$INSTALLER_VERSION"
    printf 'SOFTWARE_VERSION=%s\n' "$VERSION"
    printf 'PLATFORM=linux-amd64-glibc\n'
    printf 'CREATED_USER=%s\n' "$CREATED_USER"
    printf 'CREATED_GROUP=%s\n' "$CREATED_GROUP"
    printf 'NOLOGIN_SHELL=%s\n' "$NOLOGIN_SHELL"
    printf 'MANAGED_FILE_1=%s\n' "$BIN_DIR/agentsql"
    printf 'MANAGED_FILE_2=%s\n' "$BIN_DIR/agentsqlctl"
    printf 'MANAGED_FILE_3=%s\n' "$UNIT_PATH"
    printf 'MANAGED_FILE_4=%s\n' "$CONFIG_PATH"
    printf 'MANAGED_FILE_5=%s\n' "$ENV_PATH"
  } > "$wis_tmp"
  chown root:root "$wis_tmp"
  chmod 0644 "$wis_tmp"
  mv "$wis_tmp" "$STATE_PATH"
}

show_install_result() {
  if [ "$NO_START" -eq 1 ]; then
    warn "AgentSQL was installed but not started or health-checked. It is not available until systemd starts it successfully."
  else
    say "AgentSQL $VERSION is running at http://127.0.0.1:7780"
  fi
  if [ "$ENV_CREATED" -eq 1 ]; then
    if [ "$SHOW_PASSWORD" -eq 1 ] || [ -t 1 ]; then
      say "Automatically generated administrator password: $GENERATED_PASSWORD"
      say "Login address: http://127.0.0.1:7780"
      say "Credentials are stored in $ENV_PATH with mode 0600."
      say "For remote access, use SSH local forwarding instead of exposing the service publicly."
    else
      say "Administrator credentials were generated but are not displayed on non-interactive output. Root can inspect $ENV_PATH."
    fi
  else
    say "Existing configuration, credentials, and data were preserved."
  fi
  GENERATED_PASSWORD=
}

managed_unit_preflight() {
  if [ -e "$UNIT_PATH" ] && ! is_managed_install; then
    mup_stamp=$(date -u +%Y%m%dT%H%M%SZ)
    mup_backup="${UNIT_PATH}.preexisting-${mup_stamp}"
    cp -p "$UNIT_PATH" "$mup_backup" || die "A non-managed unit exists and could not be backed up."
    die "A non-managed $UNIT_PATH already exists. It was backed up to $mup_backup and was not overwritten; resolve it manually."
  fi
}

current_installed_version() {
  civ_state=$(state_value SOFTWARE_VERSION 2>/dev/null || true)
  if is_version "$civ_state"; then
    printf '%s\n' "$civ_state"
    return 0
  fi
  if [ -x "$BIN_DIR/agentsql" ]; then
    civ_binary=$("$BIN_DIR/agentsql" --version 2>/dev/null || true)
    if is_version "$civ_binary"; then
      printf '%s\n' "$civ_binary"
      return 0
    fi
  fi
  return 1
}

version_compare() {
  vc_left=${1#v}
  vc_right=${2#v}
  vc_old_ifs=$IFS
  IFS=.
  set -- $vc_left
  vc_l1=$1 vc_l2=$2 vc_l3=$3
  set -- $vc_right
  vc_r1=$1 vc_r2=$2 vc_r3=$3
  IFS=$vc_old_ifs
  for vc_pair in "$vc_l1:$vc_r1" "$vc_l2:$vc_r2" "$vc_l3:$vc_r3"; do
    vc_a=${vc_pair%%:*}
    vc_b=${vc_pair#*:}
    vc_a=$(printf '%s\n' "$vc_a" | sed 's/^0*//')
    vc_b=$(printf '%s\n' "$vc_b" | sed 's/^0*//')
    [ -n "$vc_a" ] || vc_a=0
    [ -n "$vc_b" ] || vc_b=0
    if [ "$vc_a" -lt "$vc_b" ]; then printf '%s\n' -1; return; fi
    if [ "$vc_a" -gt "$vc_b" ]; then printf '%s\n' 1; return; fi
  done
  printf '%s\n' 0
}

install_action() {
  ia_current=$(current_installed_version 2>/dev/null || true)
  if [ -n "$ia_current" ] && [ "$ia_current" != "$VERSION" ]; then
    die "AgentSQL $ia_current is already installed. Use the upgrade subcommand to install $VERSION."
  fi
  managed_unit_preflight
  reject_managed_symlinks
  ia_reinstall=0
  if [ -n "$ia_current" ] && [ "$ia_current" = "$VERSION" ] && is_managed_install; then
    ia_reinstall=1
    [ -f "$CONFIG_PATH" ] && [ -f "$ENV_PATH" ] || die "Same-version repair preserves configuration and credentials; restore the missing config.yaml or agentsql.env first."
  fi
  prepare_service_account
  ensure_directories
  if [ "$ia_reinstall" -eq 0 ]; then
    ensure_configuration
  fi
  record_service_state
  stage_replacements
  TX_KIND=install
  activate_replacements
  apply_selinux_contexts

  if [ "$NO_START" -eq 0 ]; then
    if ! systemctl daemon-reload || ! systemctl enable agentsql >/dev/null 2>&1; then
      warn "systemd setup failed; rolling back."
      rollback_transaction || true
      die "AgentSQL installation failed during systemd setup."
    fi
    if [ "$OLD_ACTIVE" -eq 1 ]; then
      ia_start_command=restart
    else
      ia_start_command=start
    fi
    if ! systemctl "$ia_start_command" agentsql || ! health_check_version "$VERSION"; then
      warn "Startup or health verification failed; rolling back."
      rollback_transaction || true
      die "AgentSQL failed to become healthy. Inspect: systemctl status agentsql and journalctl -u agentsql"
    fi
  fi
  write_install_state
  TX_ACTIVE=0
  remove_transaction_backups
  show_install_result
}

backup_upgrade_files() {
  buf_stamp=$(date -u +%Y%m%dT%H%M%SZ)
  install -d -o root -g root -m 0700 /var/backups/agentsql
  UPGRADE_BACKUP_DIR="/var/backups/agentsql/upgrade-${buf_stamp}-$$"
  install -d -o root -g root -m 0700 "$UPGRADE_BACKUP_DIR"
  for buf_item in "$BIN_DIR/agentsql:agentsql" "$BIN_DIR/agentsqlctl:agentsqlctl" "$UNIT_PATH:agentsql.service"; do
    buf_source=${buf_item%%:*}
    buf_name=${buf_item#*:}
    [ ! -L "$buf_source" ] || die "Refusing to back up symbolic link: $buf_source"
    if [ -f "$buf_source" ]; then cp -p "$buf_source" "$UPGRADE_BACKUP_DIR/$buf_name"; fi
  done
  if [ -f "$CONFIG_PATH" ]; then cp -p "$CONFIG_PATH" "$UPGRADE_BACKUP_DIR/config.yaml"; BACKUP_CONFIG_PRESENT=1; fi
  if [ -f "$ENV_PATH" ]; then cp -p "$ENV_PATH" "$UPGRADE_BACKUP_DIR/agentsql.env"; BACKUP_ENV_PRESENT=1; fi
  if [ -f "$DATA_DIR/agentsql.db" ]; then cp -p "$DATA_DIR/agentsql.db" "$UPGRADE_BACKUP_DIR/agentsql.db"; BACKUP_DB_PRESENT=1; fi
  if [ -f "$DATA_DIR/agentsql.db-wal" ]; then cp -p "$DATA_DIR/agentsql.db-wal" "$UPGRADE_BACKUP_DIR/agentsql.db-wal"; BACKUP_WAL_PRESENT=1; fi
  if [ -f "$DATA_DIR/agentsql.db-shm" ]; then cp -p "$DATA_DIR/agentsql.db-shm" "$UPGRADE_BACKUP_DIR/agentsql.db-shm"; BACKUP_SHM_PRESENT=1; fi
  {
    printf 'source_version=%s\n' "$CURRENT_VERSION"
    printf 'target_version=%s\n' "$VERSION"
    printf 'created_utc=%s\n' "$buf_stamp"
    printf 'default_sqlite_backed_up=%s\n' "$BACKUP_DB_PRESENT"
  } > "$UPGRADE_BACKUP_DIR/backup-state"
  chmod 0600 "$UPGRADE_BACKUP_DIR/backup-state"
}

upgrade_action() {
  is_managed_install || die "No installer-managed AgentSQL installation was found. Run install first."
  CURRENT_VERSION=$(current_installed_version 2>/dev/null || true)
  is_version "$CURRENT_VERSION" || die "Could not determine the installed AgentSQL version."
  ua_comparison=$(version_compare "$VERSION" "$CURRENT_VERSION")
  [ "$ua_comparison" -ge 0 ] || die "Downgrade from $CURRENT_VERSION to $VERSION is refused. Restore a matching database snapshot manually if a downgrade is required."
  [ "$VERSION" != "$CURRENT_VERSION" ] || die "AgentSQL $VERSION is already installed. Use install for an idempotent repair."
  reject_managed_symlinks
  if [ ! -f "$DATA_DIR/agentsql.db" ] && [ "$EXTERNAL_BACKUP_DONE" -ne 1 ]; then
    die "The default SQLite database was not found. For PostgreSQL or a custom SQLite path, complete an external backup and retry with --external-backup-done."
  fi
  if [ ! -f "$DATA_DIR/agentsql.db" ]; then
    EXTERNAL_DB_MODE=1
  fi
  record_service_state
  if systemd_available; then
    if ! systemctl stop agentsql; then
      [ "$OLD_ACTIVE" -eq 1 ] && systemctl start agentsql >/dev/null 2>&1 || true
      die "Could not stop AgentSQL before upgrade."
    fi
    if systemctl is-active --quiet agentsql 2>/dev/null; then
      die "AgentSQL is still active after the stop request."
    fi
    SERVICE_GUARD=1
  elif [ "$NO_START" -ne 1 ]; then
    die "systemd is unavailable; use --no-start only for an offline preinstallation."
  fi
  backup_upgrade_files
  stage_replacements
  TX_KIND=upgrade
  activate_replacements
  apply_selinux_contexts

  if systemd_available; then
    if ! systemctl daemon-reload; then
      rollback_transaction || true
      die "systemd daemon-reload failed; the previous version was restored."
    fi
    if [ "$OLD_ENABLED" -eq 1 ]; then systemctl enable agentsql >/dev/null 2>&1 || true; else systemctl disable agentsql >/dev/null 2>&1 || true; fi
    if [ "$NO_START" -eq 0 ] && [ "$OLD_ACTIVE" -eq 1 ]; then
      if ! systemctl start agentsql || ! health_check_version "$VERSION"; then
        warn "The upgraded service failed health verification; rolling back files and the default database snapshot."
        rollback_transaction || true
        die "Upgrade failed. The rollback backup is retained at $UPGRADE_BACKUP_DIR."
      fi
    fi
  fi
  write_install_state
  TX_ACTIVE=0
  SERVICE_GUARD=0
  remove_transaction_backups
  say "AgentSQL was upgraded from $CURRENT_VERSION to $VERSION. Backup retained at $UPGRADE_BACKUP_DIR"
  if [ "$NO_START" -eq 1 ] || [ "$OLD_ACTIVE" -eq 0 ]; then
    warn "The upgraded service was not started or health-checked because the prior service state was inactive or --no-start was used."
  else
    say "AgentSQL $VERSION is healthy at http://127.0.0.1:7780"
  fi
}

confirm_purge() {
  [ "$ASSUME_YES" -eq 1 ] && return 0
  if [ -r /dev/tty ] && [ -w /dev/tty ]; then
    printf 'Permanently delete /etc/agentsql and /var/lib/agentsql? Type "purge": ' > /dev/tty
    IFS= read -r cp_answer < /dev/tty || die "Purge confirmation was not received."
    [ "$cp_answer" = purge ] || die "Purge cancelled."
  else
    die "Purge requires an interactive /dev/tty or explicit --yes."
  fi
}

safe_purge_directory() {
  spd_path=$1
  case "$spd_path" in
    /etc/agentsql|/var/lib/agentsql) ;;
    *) die "Refusing to purge unexpected path: $spd_path" ;;
  esac
  [ ! -L "$spd_path" ] || die "Refusing to purge a symbolic link: $spd_path"
  if [ -e "$spd_path" ]; then
    if mountpoint -q "$spd_path"; then
      die "Refusing to purge mount point: $spd_path"
    fi
    rm -rf "$spd_path"
  fi
}

remove_service_account() {
  rsa_created_user=$1
  rsa_created_group=$2
  [ "$rsa_created_user" = 1 ] || die "The installer did not create user 'agentsql'; it will not be removed."
  rsa_owned=$(find / -user agentsql -print -quit 2>/dev/null || true)
  [ -z "$rsa_owned" ] || die "User 'agentsql' still owns $rsa_owned. Remove or reassign it manually before --remove-user."
  userdel agentsql || die "Could not remove user 'agentsql'."
  if [ "$rsa_created_group" = 1 ] && getent group agentsql >/dev/null 2>&1; then
    rsa_gid=$(getent group agentsql | cut -d: -f3)
    rsa_members=$(getent group agentsql | cut -d: -f4)
    rsa_primary=$(getent passwd | awk -F: -v gid="$rsa_gid" '$4 == gid { print $1; exit }')
    if [ -z "$rsa_members" ] && [ -z "$rsa_primary" ]; then
      groupdel agentsql || die "Could not remove group 'agentsql'."
    else
      warn "Group 'agentsql' is still in use and was preserved."
    fi
  fi
}

uninstall_action() {
  ua_created_user=$(state_value CREATED_USER 2>/dev/null || printf '0')
  ua_created_group=$(state_value CREATED_GROUP 2>/dev/null || printf '0')
  if systemd_available; then
    systemctl stop agentsql >/dev/null 2>&1 || true
    systemctl disable agentsql >/dev/null 2>&1 || true
  fi
  for ua_path in "$UNIT_PATH" "$BIN_DIR/agentsql" "$BIN_DIR/agentsqlctl"; do
    [ ! -L "$ua_path" ] || die "Refusing to remove symbolic link at managed path: $ua_path"
    rm -f "$ua_path"
  done
  if systemd_available; then
    systemctl daemon-reload || warn "systemctl daemon-reload failed."
    systemctl reset-failed agentsql >/dev/null 2>&1 || true
  fi
  if [ "$PURGE" -eq 1 ]; then
    confirm_purge
    safe_purge_directory "$CONFIG_DIR"
    safe_purge_directory "$DATA_DIR"
    say "AgentSQL configuration and default data directories were purged."
    if [ "$REMOVE_USER" -eq 1 ]; then
      remove_service_account "$ua_created_user" "$ua_created_group"
      say "The installer-created AgentSQL service account was removed."
    else
      say "The AgentSQL system user and group were preserved."
    fi
  else
    say "AgentSQL binaries and systemd unit were removed."
    say "Configuration and data were preserved at $CONFIG_DIR and $DATA_DIR."
    if [ -d /var/backups/agentsql ]; then
      say "Upgrade backups were preserved at /var/backups/agentsql."
    fi
    say "The AgentSQL system user and group were preserved."
  fi
}

main() {
  parse_arguments "$@"
  require_root
  detect_platform
  if [ "$ACTION" != uninstall ] && [ -z "$FROM_PATH" ]; then
    find_self_package
  fi
  check_dependencies
  check_systemd_gate
  acquire_lock_and_temp
  case "$ACTION" in
    install)
      acquire_release
      install_action
      ;;
    upgrade)
      is_managed_install || die "No installer-managed AgentSQL installation was found. Run install first."
      acquire_release
      upgrade_action
      ;;
    uninstall)
      uninstall_action
      ;;
  esac
}

main "$@"

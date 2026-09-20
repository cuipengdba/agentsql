#!/bin/sh

set -eu

ARCH=${ARCH:-amd64}
VERSION=${VERSION:-v0.3.0}
GO_VERSION=${GO_VERSION:-1.25.14}
GOPROXY=${GOPROXY:-https://goproxy.cn,direct}

die() {
  printf 'ERROR: %s\n' "$*" >&2
  exit 1
}

case "$ARCH" in
  amd64|arm64) ;;
  *) die "ARCH must be amd64 or arm64 (got '$ARCH')." ;;
esac

container_arch=$(uname -m)
case "$container_arch" in
  x86_64|amd64) detected_arch=amd64 ;;
  aarch64|arm64) detected_arch=arm64 ;;
  *) die "Unsupported container architecture '$container_arch'; expected x86_64/amd64 or aarch64/arm64." ;;
esac
[ "$detected_arch" = "$ARCH" ] || die "Container architecture '$container_arch' does not match ARCH=$ARCH."

if ! printf '%s\n' "$VERSION" | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' >/dev/null 2>&1; then
  echo "ERROR: VERSION must match vX.Y.Z exactly" >&2
  exit 1
fi

cat /etc/redhat-release
ldd --version

dnf -y install --nodocs --setopt=install_weak_deps=False gcc glibc-devel glibc-common curl binutils file findutils gawk tar gzip grep sed

curl -fsSL -o /tmp/go-releases.json "https://go.dev/dl/?mode=json&include=all"
go_filename="go${GO_VERSION}.linux-${ARCH}.tar.gz"
curl -fsSL -o /tmp/go.tgz "https://mirrors.aliyun.com/golang/${go_filename}"
expected_go_sha=$(awk -v wanted="$go_filename" '
  index($0, "\"filename\": \"" wanted "\"") { found = 1; next }
  found && /"sha256":/ {
    value = $0
    sub(/^.*"sha256":[[:space:]]*"/, "", value)
    sub(/".*$/, "", value)
    print value
    exit
  }
' /tmp/go-releases.json)
if ! printf '%s\n' "$expected_go_sha" | grep -E '^[0-9A-Fa-f]{64}$' >/dev/null 2>&1; then
  echo "ERROR: the official go.dev release manifest has no valid SHA-256 for $go_filename" >&2
  exit 1
fi
actual_go_sha=$(sha256sum /tmp/go.tgz | awk '{print $1}')
printf 'Go tarball: %s\nExpected SHA-256: %s\nActual SHA-256:   %s\n' "$go_filename" "$expected_go_sha" "$actual_go_sha"
if [ "$actual_go_sha" != "$expected_go_sha" ]; then
  die "Go tarball SHA-256 does not match the official go.dev digest for $go_filename."
fi
echo "Go tarball SHA-256 verified against go.dev: $go_filename"
tar -C /usr/local -xzf /tmp/go.tgz
export PATH="/usr/local/go/bin:$PATH"
export GOROOT=/usr/local/go

cd /src
export GOPROXY GOOS=linux GOARCH="$ARCH" CGO_ENABLED=1 GOCACHE=/tmp/gocache GOTMPDIR=/tmp
go version
go env GOOS GOARCH CGO_ENABLED CC GOARM64
mkdir -p bin
go mod download

LDFLAGS="-s -w -X github.com/cuipengdba/agentsql/internal/version.Version=${VERSION}"
go build -trimpath -ldflags "$LDFLAGS" -o "bin/agentsql-linux-${ARCH}" ./cmd/agentsql
go build -trimpath -ldflags "$LDFLAGS" -o "bin/agentsqlctl-linux-${ARCH}" ./cmd/agentsqlctl

if ! command -v objdump >/dev/null 2>&1; then
  echo "ERROR: objdump is required for the GLIBC compatibility check" >&2
  exit 1
fi

check_glibc_version() {
  binary=$1
  name=$2
  versions_file="/tmp/${name}.glibc-versions"
  comparison_file="/tmp/${name}.glibc-comparison"

  objdump -T "$binary" | grep -oE 'GLIBC_[0-9.]+' | sort -V -u > "$versions_file"

  highest=
  while IFS= read -r glibc_version; do
    if [ -n "$glibc_version" ]; then
      highest=$glibc_version
    fi
  done < "$versions_file"

  if [ -z "$highest" ]; then
    echo "ERROR: no GLIBC version symbols found in $binary" >&2
    exit 1
  fi

  printf '%s\n%s\n' "$highest" GLIBC_2.28 | sort -V -u > "$comparison_file"
  greatest=
  while IFS= read -r glibc_version; do
    if [ -n "$glibc_version" ]; then
      greatest=$glibc_version
    fi
  done < "$comparison_file"

  echo "$binary requires at most $highest"
  if [ "$greatest" != GLIBC_2.28 ]; then
    echo "ERROR: $binary requires $highest, which is newer than GLIBC_2.28" >&2
    exit 1
  fi
}

check_glibc_version "bin/agentsql-linux-${ARCH}" "agentsql-linux-${ARCH}"
check_glibc_version "bin/agentsqlctl-linux-${ARCH}" "agentsqlctl-linux-${ARCH}"

ls -la "bin/agentsql-linux-${ARCH}" "bin/agentsqlctl-linux-${ARCH}"

for binary in "bin/agentsql-linux-${ARCH}" "bin/agentsqlctl-linux-${ARCH}"; do
  strings_file="/tmp/$(basename "$binary").strings"
  strings "$binary" > "$strings_file"
  if ! grep -F "$VERSION" "$strings_file" >/dev/null; then
    echo "ERROR: version string $VERSION not found in $binary" >&2
    exit 1
  fi
  echo "$binary contains version string $VERSION"
done

agentsql_version=$("bin/agentsql-linux-${ARCH}" --version)
agentsqlctl_version=$("bin/agentsqlctl-linux-${ARCH}" version)
if [ "$agentsql_version" != "$VERSION" ]; then
  echo "ERROR: bin/agentsql-linux-${ARCH} reports $agentsql_version, expected $VERSION" >&2
  exit 1
fi
if [ "$agentsqlctl_version" != "$VERSION" ]; then
  echo "ERROR: bin/agentsqlctl-linux-${ARCH} reports $agentsqlctl_version, expected $VERSION" >&2
  exit 1
fi
echo "Both release binaries executed and reported $VERSION"

command -v file >/dev/null 2>&1 || die "file is required for the ELF release check."
command -v readelf >/dev/null 2>&1 || die "readelf is required for the ELF release check."

for binary in "bin/agentsql-linux-${ARCH}" "bin/agentsqlctl-linux-${ARCH}"; do
  file_output=$(LC_ALL=C file "$binary")
  echo "$file_output"
  case "$ARCH" in
    amd64) file_pattern='ELF 64-bit.*x86-64'; machine_pattern='Machine:[[:space:]]+(Advanced Micro Devices X86-64|AMD x86-64)' ;;
    arm64) file_pattern='ELF 64-bit.*(ARM aarch64|aarch64)'; machine_pattern='Machine:[[:space:]]+AArch64' ;;
  esac
  if ! printf '%s\n' "$file_output" | grep -E "$file_pattern" >/dev/null 2>&1; then
    die "$binary is not an ELF64 $ARCH binary."
  fi
  readelf_output=$(LC_ALL=C readelf -h "$binary")
  printf '%s\n' "$readelf_output"
  if ! printf '%s\n' "$readelf_output" | grep -E 'Class:[[:space:]]+ELF64' >/dev/null 2>&1; then
    die "$binary is not ELF64."
  fi
  if ! printf '%s\n' "$readelf_output" | grep -E "$machine_pattern" >/dev/null 2>&1; then
    die "$binary ELF machine does not match ARCH=$ARCH."
  fi
  if command -v ldd >/dev/null 2>&1; then
    ldd_output="/tmp/$(basename "$binary").ldd"
    ldd "$binary" > "$ldd_output" 2>&1 || true
    cat "$ldd_output"
    if grep -F 'not found' "$ldd_output" >/dev/null 2>&1; then
      echo "ERROR: $binary has a missing shared-library dependency" >&2
      exit 1
    fi
  fi
done

echo BUILD_RELEASE_LINUX_DONE

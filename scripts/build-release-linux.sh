#!/bin/sh

set -eu

VERSION=${VERSION:-v0.2.0}
GO_VERSION=${GO_VERSION:-1.25.14}
GOPROXY=${GOPROXY:-https://goproxy.cn,direct}

if ! printf '%s\n' "$VERSION" | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' >/dev/null 2>&1; then
  echo "ERROR: VERSION must match vX.Y.Z exactly" >&2
  exit 1
fi

cat /etc/redhat-release
ldd --version

dnf -y install --nodocs gcc glibc-devel

curl -fsSL -o /tmp/go-releases.json "https://go.dev/dl/?mode=json&include=all"
curl -fsSL -o /tmp/go.tgz "https://mirrors.aliyun.com/golang/go${GO_VERSION}.linux-amd64.tar.gz"
go_filename="go${GO_VERSION}.linux-amd64.tar.gz"
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
if [ "$actual_go_sha" != "$expected_go_sha" ]; then
  echo "ERROR: Go tarball SHA-256 does not match the official go.dev digest" >&2
  exit 1
fi
echo "Go tarball SHA-256 verified against go.dev"
tar -C /usr/local -xzf /tmp/go.tgz
export PATH="/usr/local/go/bin:$PATH"
export GOROOT=/usr/local/go
go version

cd /src
export GOPROXY CGO_ENABLED=1 GOCACHE=/tmp/gocache GOTMPDIR=/tmp
mkdir -p bin
go mod download

LDFLAGS="-s -w -X github.com/cuipengdba/agentsql/internal/version.Version=${VERSION}"
go build -trimpath -ldflags "$LDFLAGS" -o bin/agentsql-linux-amd64 ./cmd/agentsql
go build -trimpath -ldflags "$LDFLAGS" -o bin/agentsqlctl-linux-amd64 ./cmd/agentsqlctl

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

check_glibc_version bin/agentsql-linux-amd64 agentsql-linux-amd64
check_glibc_version bin/agentsqlctl-linux-amd64 agentsqlctl-linux-amd64

ls -la bin/agentsql-linux-amd64 bin/agentsqlctl-linux-amd64

for binary in bin/agentsql-linux-amd64 bin/agentsqlctl-linux-amd64; do
  strings_file="/tmp/$(basename "$binary").strings"
  strings "$binary" > "$strings_file"
  if ! grep -F "$VERSION" "$strings_file" >/dev/null; then
    echo "ERROR: version string $VERSION not found in $binary" >&2
    exit 1
  fi
  echo "$binary contains version string $VERSION"
done

agentsql_version=$(bin/agentsql-linux-amd64 --version)
agentsqlctl_version=$(bin/agentsqlctl-linux-amd64 version)
if [ "$agentsql_version" != "$VERSION" ]; then
  echo "ERROR: bin/agentsql-linux-amd64 reports $agentsql_version, expected $VERSION" >&2
  exit 1
fi
if [ "$agentsqlctl_version" != "$VERSION" ]; then
  echo "ERROR: bin/agentsqlctl-linux-amd64 reports $agentsqlctl_version, expected $VERSION" >&2
  exit 1
fi
echo "Both release binaries executed and reported $VERSION"

for binary in bin/agentsql-linux-amd64 bin/agentsqlctl-linux-amd64; do
  if command -v file >/dev/null 2>&1; then
    file_output=$(file "$binary")
    echo "$file_output"
    if ! printf '%s\n' "$file_output" | grep -E 'ELF 64-bit.*x86-64' >/dev/null 2>&1; then
      echo "ERROR: $binary is not an ELF64 x86-64 binary" >&2
      exit 1
    fi
  fi
  if command -v readelf >/dev/null 2>&1; then
    readelf -h "$binary"
    if ! readelf -h "$binary" | grep -E 'Class:[[:space:]]+ELF64' >/dev/null 2>&1; then
      echo "ERROR: $binary is not ELF64" >&2
      exit 1
    fi
    if ! readelf -h "$binary" | grep -E 'Machine:[[:space:]]+(Advanced Micro Devices X86-64|AMD x86-64)' >/dev/null 2>&1; then
      echo "ERROR: $binary is not x86-64" >&2
      exit 1
    fi
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

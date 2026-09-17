#!/bin/sh

set -eu

VERSION=${VERSION:-v0.2.0}
GO_VERSION=${GO_VERSION:-1.25.14}
GOPROXY=${GOPROXY:-https://goproxy.cn,direct}

cat /etc/redhat-release
ldd --version

dnf -y install --nodocs gcc glibc-devel

curl -fsSL -o /tmp/go.tgz "https://mirrors.aliyun.com/golang/go${GO_VERSION}.linux-amd64.tar.gz"
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

echo BUILD_RELEASE_LINUX_DONE

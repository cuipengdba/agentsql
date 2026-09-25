#!/bin/sh
set -eu

repo=$(CDPATH= cd -- "$(dirname "$0")/../.." && pwd)
pwsh=${POWERSHELL:-pwsh}
command -v "$pwsh" >/dev/null 2>&1 || { echo 'pwsh is required by this cross-platform wrapper' >&2; exit 1; }
exec "$pwsh" -NoProfile -File "$repo/dbext/packaging/build-packages.ps1" "$@"

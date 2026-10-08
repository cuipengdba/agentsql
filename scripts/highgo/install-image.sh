#!/bin/bash
set -euo pipefail

echo 'STEP install: verify read-only license secret and cached OS'
test -s /opt/highgo/license/license.dat
mountpoint -q /opt/highgo/license/license.dat
test "$(. /etc/os-release; echo "$ID:$VERSION_ID")" = 'rocky:8.9'
ldd --version | head -1

echo 'STEP install: extract official Makeself payload without executing it'
/bin/sh /tmp/installer.bin --noexec --target /tmp/hg-installer --noprogress
test -x /tmp/hg-installer/jdk/bin/java
test -f /tmp/hg-installer/hgdb_install.jar

cat > /tmp/highgo-install.properties <<'EOF'
INSTALL_PATH=/opt/highgo
LICENSE_FILE_PATH=/opt/highgo/license/license.dat
DB_INIT_ENABLED=false
DB_REGISTER_SERVICE=false
DB_MODE=pg
EOF
chown -R highgo:highgo /tmp/hg-installer /tmp/highgo-install.properties

echo 'STEP install: run vendor documented -defaults-file/-auto silent mode'
# The vendor launcher returns zero even if Java fails. Check installed binaries
# and the read-only mount afterward rather than trusting its exit status.
if ! runuser -u highgo -- /bin/bash /tmp/hg-installer/luancher.sh -defaults-file /tmp/highgo-install.properties -auto; then
    echo 'FAIL vendor installer returned nonzero' >&2
    exit 1
fi
for name in initdb pg_ctl postgres psql; do
    test -x "/opt/highgo/bin/$name" || { echo "FAIL missing $name" >&2; exit 1; }
done
mountpoint -q /opt/highgo/license/license.dat || { echo 'FAIL license mount replaced' >&2; exit 1; }
shopt -s globstar nullglob
license_files=(/opt/highgo/**/license.dat)
test "${#license_files[@]}" -eq 1 || {
    echo 'FAIL unexpected license copy in installation' >&2
    exit 1
}
echo 'PASS installer binaries and sole license mount verified'

echo 'STEP install: inspect database executable linkage'
ldd /opt/highgo/bin/postgres | grep 'not found' && { echo 'FAIL missing library' >&2; exit 1; } || true
/opt/highgo/bin/postgres --version
rm -rf /tmp/hg-installer /tmp/highgo-install.properties

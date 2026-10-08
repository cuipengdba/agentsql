#!/bin/bash
set -euo pipefail

: "${HGDB_HOME:=/opt/highgo}"
: "${HGDB_DATA:=/var/lib/highgo/data}"
: "${HGDB_PORT:=5866}"
if [[ ! -s "$HGDB_HOME/license/license.dat" ]]; then
    echo 'FAIL: read-only license mount missing' >&2
    exit 1
fi
if [[ "$HGDB_PORT" != 5866 ]]; then
    echo 'FAIL: supplied license specifies port 5866' >&2
    exit 1
fi

if [[ ! -f "$HGDB_DATA/PG_VERSION" ]]; then
    : "${HGDB_PASSWORD:?HGDB_PASSWORD is required for first initialization}"
    mkdir -p "$HGDB_DATA"
    umask 077
    pwfile="$(mktemp)"
    trap 'rm -f "$pwfile"' EXIT
    printf '%s\n' "$HGDB_PASSWORD" > "$pwfile"
    "$HGDB_HOME/bin/initdb" -D "$HGDB_DATA" -U highgo -P "$HGDB_PORT" \
        -m pg -E UTF-8 --no-locale -A scram-sha-256 --pwfile="$pwfile"
    rm -f "$pwfile"
    trap - EXIT
fi
unset HGDB_PASSWORD
exec "$HGDB_HOME/bin/postgres" -D "$HGDB_DATA" -p "$HGDB_PORT" -c "listen_addresses=*"

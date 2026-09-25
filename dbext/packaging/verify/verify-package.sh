#!/bin/sh
set -eu

pg_config_path=$(command -v pg_config || find /usr -type f -name pg_config | head -1)
test -n "$pg_config_path"
bindir=$($pg_config_path --bindir)
export PATH="$bindir:$PATH"
data=/tmp/agentsql-binder-pgdata
socket=/tmp/agentsql-binder-socket
mkdir -p "$data" "$socket"
chown postgres "$data" "$socket"

as_postgres() {
  if command -v su-exec >/dev/null 2>&1; then
    su-exec postgres "$@"
  else
    runuser -u postgres -- "$@"
  fi
}

as_postgres initdb -D "$data" --no-locale --encoding=UTF8 --auth=trust >/dev/null
as_postgres pg_ctl -D "$data" -o "-F -k $socket" -w start >/dev/null
trap 'as_postgres pg_ctl -D "$data" -m immediate -w stop >/dev/null 2>&1 || true' EXIT INT TERM
as_postgres psql -h "$socket" -d postgres -X --no-psqlrc --set=ON_ERROR_STOP=1 <<'SQL' >/dev/null
CREATE SCHEMA agentsql_catalog;
CREATE EXTENSION agentsql_binder WITH SCHEMA agentsql_catalog VERSION '0.4';
CREATE TABLE public.binder_selftest(id integer, note text);
BEGIN;
SELECT agentsql_catalog.prepare('agentsql_package_selftest', 'SELECT id FROM public.binder_selftest');
SELECT agentsql_catalog.seal_prepared('agentsql_package_selftest');
SELECT count(*) FROM agentsql_catalog.prepared_manifest('agentsql_package_selftest');
COMMIT;
SQL
result=$(as_postgres psql -h "$socket" -d postgres -X --no-psqlrc --set=ON_ERROR_STOP=1 --tuples-only --no-align <<'SQL'
SELECT (SELECT extversion='0.4' FROM pg_catalog.pg_extension WHERE extname='agentsql_binder')
   AND c->>'abi'='agentsql-binder-4.1'
   AND c->>'extension_version'='0.4'
   AND c->>'server_major'=(current_setting('server_version_num')::integer/10000)::text
   AND length(c->>'build_hash')=64
   AND length(c->>'extension_hash')=64
   AND length(c->>'node_manifest_hash')=64
   AND length(c->>'allowlist_hash')=64
FROM (SELECT agentsql_catalog.capabilities() c) s;
SQL
)
test "$result" = t
echo "package CREATE EXTENSION and binder self-test: PASS"

#!/bin/sh
set -eu

: "${PGHOST:=/var/run/postgresql}"
: "${PGPORT:=5432}"
: "${PGUSER:=postgres}"
: "${PGDATABASE:=${POSTGRES_DB:-postgres}}"

result=$(psql -X --no-psqlrc --set=ON_ERROR_STOP=1 --tuples-only --no-align <<'SQL'
SELECT (SELECT extversion = '0.4' FROM pg_catalog.pg_extension WHERE extname = 'agentsql_binder')
   AND capabilities->>'abi' = 'agentsql-binder-4.1'
   AND capabilities->>'extension_version' = '0.4'
   AND capabilities->>'server_major' = (current_setting('server_version_num')::integer / 10000)::text
   AND length(capabilities->>'build_hash') = 64
   AND length(capabilities->>'extension_hash') = 64
   AND length(capabilities->>'node_manifest_hash') = 64
   AND length(capabilities->>'allowlist_hash') = 64
FROM (SELECT agentsql_catalog.capabilities() AS capabilities) AS probe;
SQL
)
test "$result" = t

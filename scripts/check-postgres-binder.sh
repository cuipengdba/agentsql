#!/bin/sh
# Read-only PostgreSQL binder preflight. libpq reads PGHOST, PGPORT, PGUSER,
# PGDATABASE and PGPASSFILE/PGPASSWORD; no credential is passed on argv.
set -eu

usage() {
  echo 'Usage: PGDATABASE=... [PGHOST=... PGUSER=...] check-postgres-binder.sh [--runtime-role ROLE]' >&2
}

runtime_role=
while [ "$#" -gt 0 ]; do
  case "$1" in
    --runtime-role)
      if [ "$#" -lt 2 ]; then usage; exit 64; fi
      runtime_role=$2
      shift 2
      ;;
    -h|--help) usage; exit 0 ;;
    *) usage; exit 64 ;;
  esac
done

if [ -z "${PGDATABASE:-}" ]; then
  echo 'POSTGRES_BINDER=FAIL reason=DATABASE_REQUIRED'
  exit 64
fi
if ! command -v psql >/dev/null 2>&1; then
  echo 'POSTGRES_BINDER=FAIL reason=PSQL_REQUIRED'
  exit 69
fi

set_role=
if [ -n "$runtime_role" ]; then
  case "$runtime_role" in
    [A-Za-z_]* ) ;;
    * ) echo 'POSTGRES_BINDER=FAIL reason=INVALID_ROLE'; exit 64 ;;
  esac
  case "$runtime_role" in
    *[!A-Za-z0-9_]* ) echo 'POSTGRES_BINDER=FAIL reason=INVALID_ROLE'; exit 64 ;;
  esac
  set_role="SET ROLE \"$runtime_role\";"
fi

# Resolve permissions through catalog OIDs so a missing schema is reported as
# a stable failure instead of making the preflight itself error at parse time.
if ! inventory=$(psql -X -qAt --no-psqlrc -v ON_ERROR_STOP=1 2>/dev/null <<SQL
$set_role
SELECT CASE
  WHEN NOT EXISTS (
    SELECT 1 FROM pg_catalog.pg_extension
    WHERE extname='agentsql_binder' AND extversion='0.4'
  ) THEN 'EXTENSION_MISSING_OR_WRONG_VERSION'
  WHEN NOT COALESCE((
    SELECT pg_catalog.has_schema_privilege(current_user, n.oid, 'USAGE')
    FROM pg_catalog.pg_namespace n WHERE n.nspname='agentsql_catalog'
  ), false) THEN 'SCHEMA_USAGE_MISSING'
  WHEN (
    SELECT pg_catalog.count(DISTINCT p.proname)
    FROM pg_catalog.pg_proc p
    JOIN pg_catalog.pg_namespace n ON n.oid=p.pronamespace
    WHERE n.nspname='agentsql_catalog'
      AND p.proname IN (
        'capabilities', 'dml_capabilities', 'prepare', 'prepare_dml',
        'seal_prepared', 'prepared_manifest', 'prepared_dml_manifest',
        'prepared_relations', 'prepared_vars', 'prepared_objects'
      )
      AND pg_catalog.has_function_privilege(current_user, p.oid, 'EXECUTE')
  ) <> 10 THEN 'FUNCTION_EXECUTE_MISSING'
  ELSE 'READY'
END;
SQL
); then
  echo 'POSTGRES_BINDER=FAIL reason=DATABASE_QUERY_FAILED'
  exit 1
fi
if [ "$inventory" != READY ]; then
  echo "POSTGRES_BINDER=FAIL reason=$inventory"
  exit 1
fi

if ! capability=$(psql -X -qAt --no-psqlrc -v ON_ERROR_STOP=1 2>/dev/null <<SQL
$set_role
SELECT CASE WHEN capability->>'abi'='agentsql-binder-4.2'
  AND capability->>'extension_version'='0.4-s3m'
  AND capability->>'server_major'=(current_setting('server_version_num')::integer / 10000)::text
  AND length(capability->>'build_hash')=64
  AND length(capability->>'extension_hash')=64
  THEN 'READY' ELSE 'CAPABILITY_MISMATCH' END
FROM (SELECT agentsql_catalog.capabilities() AS capability) AS native;
SQL
); then
  echo 'POSTGRES_BINDER=FAIL reason=CAPABILITY_QUERY_FAILED'
  exit 1
fi
if [ "$capability" != READY ]; then
  echo "POSTGRES_BINDER=FAIL reason=$capability"
  exit 1
fi
echo 'POSTGRES_BINDER=PASS'

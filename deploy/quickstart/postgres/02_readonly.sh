#!/bin/sh
set -eu

: "${DEMO_PG_RO_PASSWORD:?DEMO_PG_RO_PASSWORD is required}"

# Keep init-time quoting portable across psql, shell, and Compose. Demo
# passwords must be non-empty ASCII using this conservative character set.
case "$DEMO_PG_RO_PASSWORD" in
  *[!A-Za-z0-9._~!@#%^+=-]*)
    echo "DEMO_PG_RO_PASSWORD must contain only safe ASCII characters" >&2
    exit 1
    ;;
esac

psql -v ON_ERROR_STOP=1 \
  --username "$POSTGRES_USER" \
  --dbname "$POSTGRES_DB" \
  -v ro_password="$DEMO_PG_RO_PASSWORD" <<'SQL'
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'agentsql_demo_ro') THEN
        CREATE ROLE agentsql_demo_ro LOGIN;
    END IF;
END
$$;

ALTER ROLE agentsql_demo_ro WITH LOGIN PASSWORD :'ro_password';
REVOKE CONNECT ON DATABASE agentsql_demo FROM PUBLIC;
REVOKE CREATE ON SCHEMA public FROM PUBLIC;
REVOKE ALL ON ALL TABLES IN SCHEMA public FROM PUBLIC;
REVOKE ALL ON ALL SEQUENCES IN SCHEMA public FROM PUBLIC;
GRANT CONNECT ON DATABASE agentsql_demo TO agentsql_demo_ro;
GRANT USAGE ON SCHEMA public TO agentsql_demo_ro;
GRANT SELECT ON ALL TABLES IN SCHEMA public TO agentsql_demo_ro;
GRANT UPDATE (balance, status) ON TABLE demo_tx_accounts TO agentsql_demo_ro;
ALTER DEFAULT PRIVILEGES IN SCHEMA public
    GRANT SELECT ON TABLES TO agentsql_demo_ro;

-- Extension SQL starts default-deny. Grant only the fixed runtime role and
-- only when init found and created the optional extension.
SELECT 'GRANT USAGE ON SCHEMA agentsql_catalog TO agentsql_demo_ro'
WHERE EXISTS (SELECT 1 FROM pg_catalog.pg_extension WHERE extname='agentsql_binder' AND extversion='0.4') \gexec
SELECT 'GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA agentsql_catalog TO agentsql_demo_ro'
WHERE EXISTS (SELECT 1 FROM pg_catalog.pg_extension WHERE extname='agentsql_binder' AND extversion='0.4') \gexec
SQL

\set ON_ERROR_STOP on

-- The demo image installs the extension files before initdb. Keep creation
-- conditional so the same init directory remains usable with a stock
-- PostgreSQL image: AgentSQL will then select CATALOG_CLOSED_V1.
SELECT 'CREATE SCHEMA IF NOT EXISTS agentsql_catalog'
WHERE EXISTS (
    SELECT 1 FROM pg_catalog.pg_available_extension_versions
    WHERE name = 'agentsql_binder' AND version = '0.4'
) \gexec

SELECT 'CREATE EXTENSION IF NOT EXISTS agentsql_binder WITH SCHEMA agentsql_catalog VERSION ''0.4'''
WHERE EXISTS (
    SELECT 1 FROM pg_catalog.pg_available_extension_versions
    WHERE name = 'agentsql_binder' AND version = '0.4'
) \gexec

DO $agentsql_demo_binder$
DECLARE
    available boolean;
    installed boolean;
    capability jsonb;
BEGIN
    SELECT EXISTS (
        SELECT 1 FROM pg_catalog.pg_available_extension_versions
        WHERE name = 'agentsql_binder' AND version = '0.4'
    ) INTO available;
    SELECT EXISTS (
        SELECT 1 FROM pg_catalog.pg_extension
        WHERE extname = 'agentsql_binder' AND extversion = '0.4'
    ) INTO installed;

    IF available AND NOT installed THEN
        RAISE EXCEPTION 'agentsql_binder 0.4 files are available but CREATE EXTENSION did not complete';
    ELSIF NOT available THEN
        RAISE WARNING 'agentsql_binder 0.4 files are unavailable; AgentSQL will use CATALOG_CLOSED_V1';
        RETURN;
    END IF;

    SELECT agentsql_catalog.capabilities() INTO capability;
    IF capability->>'abi' <> 'agentsql-binder-4.2'
       OR capability->>'extension_version' <> '0.4-s3m'
       OR capability->>'server_major' <> (current_setting('server_version_num')::integer / 10000)::text
       OR length(capability->>'build_hash') <> 64
       OR length(capability->>'extension_hash') <> 64
       OR length(capability->>'node_manifest_hash') <> 64
       OR length(capability->>'allowlist_hash') <> 64 THEN
        RAISE EXCEPTION 'agentsql_binder capability self-test failed: %', capability;
    END IF;
    RAISE NOTICE 'agentsql_binder 0.4 created and capability self-test passed';
END
$agentsql_demo_binder$;

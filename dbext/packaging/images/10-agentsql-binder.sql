\set ON_ERROR_STOP on
CREATE SCHEMA IF NOT EXISTS agentsql_catalog;
CREATE EXTENSION IF NOT EXISTS agentsql_binder WITH SCHEMA agentsql_catalog VERSION '0.4';
DO $agentsql$
BEGIN
  IF agentsql_catalog.capabilities()->>'extension_version' <> '0.4' THEN
    RAISE EXCEPTION 'agentsql_binder capability self-test failed';
  END IF;
END
$agentsql$;

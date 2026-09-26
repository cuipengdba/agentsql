CREATE FUNCTION capabilities() RETURNS jsonb
AS 'MODULE_PATHNAME', 'agentsql_binder_capabilities'
LANGUAGE C STABLE PARALLEL RESTRICTED;

CREATE FUNCTION prepare(statement_name text, raw_sql text) RETURNS void
AS 'MODULE_PATHNAME', 'agentsql_binder_prepare'
LANGUAGE C VOLATILE PARALLEL UNSAFE;

CREATE FUNCTION dml_capabilities() RETURNS jsonb
LANGUAGE SQL STABLE PARALLEL RESTRICTED
AS $$
  SELECT agentsql_catalog.capabilities() || pg_catalog.jsonb_build_object(
    'abi', 'agentsql-binder-dml-1',
    'extension_version', '0.4-s5b',
    'build_hash', pg_catalog.encode(pg_catalog.sha256(pg_catalog.convert_to('agentsql-binder-dml-build-v1-pg' || (current_setting('server_version_num')::integer / 10000)::text, 'UTF8')), 'hex'),
    'extension_hash', pg_catalog.encode(pg_catalog.sha256(pg_catalog.convert_to('agentsql-binder-dml-source-v1-pg' || (current_setting('server_version_num')::integer / 10000)::text, 'UTF8')), 'hex'),
    'node_manifest_hash', pg_catalog.encode(pg_catalog.sha256(pg_catalog.convert_to('query-dml-write-reference-v1-pg' || (current_setting('server_version_num')::integer / 10000)::text, 'UTF8')), 'hex'),
    'allowlist_hash', pg_catalog.encode(pg_catalog.sha256(pg_catalog.convert_to('builtin-exact-oids-dml-v1-pg' || (current_setting('server_version_num')::integer / 10000)::text, 'UTF8')), 'hex'),
    'matview', false
  )
$$;

CREATE FUNCTION prepare_dml(statement_name text, raw_sql text) RETURNS void
AS 'MODULE_PATHNAME', 'agentsql_binder_dml_prepare'
LANGUAGE C VOLATILE PARALLEL UNSAFE;

CREATE FUNCTION seal_prepared(statement_name text) RETURNS void
AS 'MODULE_PATHNAME', 'agentsql_binder_seal'
LANGUAGE C VOLATILE PARALLEL UNSAFE;

CREATE FUNCTION prepared_manifest(statement_name text)
RETURNS TABLE(
  statement_name text, backend_pid integer, transaction_id text,
  role_oid oid, role_name text, search_path text,
  analyzed_digest text, dependency_digest text,
  plan_generation bigint, replan_count bigint, invalidated boolean,
  command_type text, has_recursive boolean, has_modifying_cte boolean,
  node_count integer, edge_count integer, work_units integer
)
AS 'MODULE_PATHNAME', 'agentsql_binder_manifest'
LANGUAGE C STABLE STRICT PARALLEL RESTRICTED;

CREATE FUNCTION prepared_dml_manifest(statement_name text)
RETURNS TABLE(
  statement_name text, backend_pid integer, transaction_id text,
  role_oid oid, role_name text, search_path text,
  analyzed_digest text, dependency_digest text,
  plan_generation bigint, replan_count bigint, invalidated boolean,
  command_type text, has_recursive boolean, has_modifying_cte boolean,
  node_count integer, edge_count integer, work_units integer,
  target_relation_oid oid, dml_shape text, has_returning boolean
)
AS 'MODULE_PATHNAME', 'agentsql_binder_dml_manifest'
LANGUAGE C STABLE STRICT PARALLEL RESTRICTED;

CREATE FUNCTION prepared_relations(statement_name text)
RETURNS TABLE(
  relation_oid oid, relkind "char", inh boolean, required_perms integer,
  check_as_user oid, view_depth integer, node_path text
)
AS 'MODULE_PATHNAME', 'agentsql_binder_relations'
LANGUAGE C STABLE STRICT PARALLEL RESTRICTED;

CREATE FUNCTION prepared_vars(statement_name text)
RETURNS TABLE(
  site text, relation_oid oid, attnum smallint, type_oid oid,
  collation_oid oid, usage text, query_depth integer, view_depth integer,
  contributor_group integer, contributor_complete boolean,
  is_whole_row boolean, result_type_oid oid, result_is_composite boolean
)
AS 'MODULE_PATHNAME', 'agentsql_binder_vars'
LANGUAGE C STABLE STRICT PARALLEL RESTRICTED;

CREATE FUNCTION prepared_objects(statement_name text)
RETURNS TABLE(object_kind text, object_oid oid)
AS 'MODULE_PATHNAME', 'agentsql_binder_objects'
LANGUAGE C STABLE STRICT PARALLEL RESTRICTED;

-- The extension is installed by a bootstrap owner. Runtime access is granted
-- explicitly by the gateway bootstrap after this default-deny baseline.
REVOKE ALL ON FUNCTION capabilities() FROM PUBLIC;
REVOKE ALL ON FUNCTION dml_capabilities() FROM PUBLIC;
REVOKE ALL ON FUNCTION prepare(text,text) FROM PUBLIC;
REVOKE ALL ON FUNCTION prepare_dml(text,text) FROM PUBLIC;
REVOKE ALL ON FUNCTION seal_prepared(text) FROM PUBLIC;
REVOKE ALL ON FUNCTION prepared_manifest(text) FROM PUBLIC;
REVOKE ALL ON FUNCTION prepared_dml_manifest(text) FROM PUBLIC;
REVOKE ALL ON FUNCTION prepared_relations(text) FROM PUBLIC;
REVOKE ALL ON FUNCTION prepared_vars(text) FROM PUBLIC;
REVOKE ALL ON FUNCTION prepared_objects(text) FROM PUBLIC;

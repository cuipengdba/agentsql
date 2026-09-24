CREATE FUNCTION capabilities() RETURNS jsonb
AS 'MODULE_PATHNAME', 'agentsql_binder_capabilities'
LANGUAGE C STABLE PARALLEL RESTRICTED;

CREATE FUNCTION prepare(statement_name text, raw_sql text) RETURNS void
AS 'MODULE_PATHNAME', 'agentsql_binder_prepare'
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

REVOKE ALL ON FUNCTION prepare(text,text) FROM PUBLIC;
REVOKE ALL ON FUNCTION seal_prepared(text) FROM PUBLIC;

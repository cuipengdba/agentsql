package parser

import (
	"encoding/json"
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestPostgresUtilityClassification(t *testing.T) {
	tests := []struct {
		name       string
		sql        string
		stmtType   model.StmtType
		operations []string
	}{
		{name: "cluster all", sql: "CLUSTER", stmtType: "ADMIN", operations: []string{"CLUSTER"}},
		{name: "cluster table", sql: "CLUSTER public.orders", stmtType: "ADMIN", operations: []string{"CLUSTER"}},
		{name: "cluster table using index", sql: "CLUSTER public.orders USING idx_orders", stmtType: "ADMIN", operations: []string{"CLUSTER"}},
		{name: "create database", sql: "CREATE DATABASE app", stmtType: "DDL", operations: []string{"CREATE DATABASE"}},
		{name: "drop database", sql: "DROP DATABASE app", stmtType: "DDL", operations: []string{"DROP DATABASE"}},
		{name: "alter database", sql: "ALTER DATABASE app WITH CONNECTION LIMIT 10", stmtType: "DDL", operations: []string{"ALTER DATABASE"}},
		{name: "alter database set", sql: "ALTER DATABASE app SET work_mem = '64MB'", stmtType: "DDL", operations: []string{"ALTER DATABASE"}},
		{name: "alter database refresh collation", sql: "ALTER DATABASE app REFRESH COLLATION VERSION", stmtType: "DDL", operations: []string{"ALTER DATABASE"}},
		{name: "create extension", sql: "CREATE EXTENSION IF NOT EXISTS pg_trgm", stmtType: "DDL", operations: []string{"CREATE EXTENSION"}},
		{name: "alter sequence", sql: "ALTER SEQUENCE public.order_id_seq RESTART WITH 1", stmtType: "DDL", operations: []string{"ALTER SEQUENCE"}},
		{name: "refresh materialized view", sql: "REFRESH MATERIALIZED VIEW public.order_summary", stmtType: "DDL", operations: []string{"REFRESH MATERIALIZED VIEW"}},
		{name: "comment on", sql: "COMMENT ON TABLE public.orders IS 'orders'", stmtType: "DDL", operations: []string{"COMMENT ON"}},
		{name: "alter system", sql: "ALTER SYSTEM SET work_mem = '64MB'", stmtType: "ADMIN", operations: []string{"ALTER SYSTEM"}},
		{name: "alter role", sql: "ALTER ROLE report_user WITH LOGIN", stmtType: "ADMIN", operations: []string{"ALTER ROLE"}},
		{name: "alter role set", sql: "ALTER ROLE report_user SET statement_timeout = '5s'", stmtType: "ADMIN", operations: []string{"ALTER ROLE"}},
		{name: "create role", sql: "CREATE ROLE report_user LOGIN", stmtType: "ADMIN", operations: []string{"CREATE ROLE"}},
		{name: "drop role", sql: "DROP ROLE report_user", stmtType: "ADMIN", operations: []string{"DROP ROLE"}},
		{name: "checkpoint", sql: "CHECKPOINT", stmtType: "ADMIN", operations: []string{"CHECKPOINT"}},
		{name: "lock table", sql: "LOCK TABLE public.orders IN ACCESS SHARE MODE", stmtType: "ADMIN", operations: []string{"LOCK TABLE"}},
		{name: "load high risk", sql: "LOAD 'plugin.so'", stmtType: "ADMIN", operations: []string{"LOAD"}},
		{name: "discard", sql: "DISCARD ALL", stmtType: "ADMIN", operations: []string{"DISCARD"}},
		{name: "prepare", sql: "PREPARE find_order(bigint) AS SELECT id FROM public.orders WHERE id = $1", stmtType: "ADMIN", operations: []string{"PREPARE"}},
		{name: "execute", sql: "EXECUTE find_order(1)", stmtType: "ADMIN", operations: []string{"EXECUTE"}},
		{name: "deallocate", sql: "DEALLOCATE find_order", stmtType: "ADMIN", operations: []string{"DEALLOCATE"}},
		{name: "call", sql: "CALL public.refresh_reports()", stmtType: "ADMIN", operations: []string{"CALL"}},
		{name: "notify", sql: "NOTIFY audit_events, 'done'", stmtType: "ADMIN", operations: []string{"NOTIFY"}},
		{name: "listen", sql: "LISTEN audit_events", stmtType: "ADMIN", operations: []string{"LISTEN"}},
		{name: "unlisten", sql: "UNLISTEN audit_events", stmtType: "ADMIN", operations: []string{"UNLISTEN"}},
		{name: "explain select", sql: "EXPLAIN SELECT id FROM public.orders", stmtType: "SELECT", operations: []string{"EXPLAIN", "SELECT"}},
		{name: "explain analyze bare", sql: "EXPLAIN ANALYZE SELECT id FROM public.orders", stmtType: "SELECT", operations: []string{"EXPLAIN ANALYZE", "SELECT"}},
		{name: "explain analyze parenthesized", sql: "EXPLAIN (ANALYZE) SELECT id FROM public.orders", stmtType: "SELECT", operations: []string{"EXPLAIN ANALYZE", "SELECT"}},
		{name: "explain analyze select", sql: "EXPLAIN (ANALYZE, BUFFERS) SELECT id FROM public.orders", stmtType: "SELECT", operations: []string{"EXPLAIN ANALYZE", "SELECT"}},
		{name: "explain analyze true", sql: "EXPLAIN (ANALYZE TRUE) SELECT id FROM public.orders", stmtType: "SELECT", operations: []string{"EXPLAIN ANALYZE", "SELECT"}},
		{name: "explain analyze update", sql: "EXPLAIN ANALYZE UPDATE public.orders SET status = 'done' WHERE id = 1", stmtType: "UPDATE", operations: []string{"EXPLAIN ANALYZE", "UPDATE"}},
		{name: "explain analyze false", sql: "EXPLAIN (ANALYZE FALSE) SELECT id FROM public.orders", stmtType: "SELECT", operations: []string{"EXPLAIN", "SELECT"}},
	}

	approvedParser := &postgresParser{}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ast, err := approvedParser.Parse(test.sql)
			require.NoError(t, err)
			require.Equal(t, test.stmtType, ast.StmtType)
			require.NotEqual(t, model.StmtType("UNKNOWN"), ast.StmtType)
			require.Equal(t, sortedStrings(test.operations), sortedStrings(ast.Operations))
		})
	}
}

func TestPostgresBooleanNodeEncodings(t *testing.T) {
	tests := []struct {
		name  string
		node  map[string]any
		value bool
		ok    bool
	}{
		{name: "boolean true", node: map[string]any{"Boolean": map[string]any{"boolval": true}}, value: true, ok: true},
		{name: "boolean omitted false", node: map[string]any{"Boolean": map[string]any{}}, value: false, ok: true},
		{name: "string true", node: map[string]any{"String": map[string]any{"sval": " TRUE "}}, value: true, ok: true},
		{name: "string false", node: map[string]any{"String": map[string]any{"sval": " FaLsE "}}, value: false, ok: true},
		{name: "integer one", node: map[string]any{"Integer": map[string]any{"ival": json.Number("1")}}, value: true, ok: true},
		{name: "integer zero", node: map[string]any{"Integer": map[string]any{"ival": json.Number("0")}}, value: false, ok: true},
		{name: "unknown string", node: map[string]any{"String": map[string]any{"sval": "on"}}, value: false, ok: false},
		{name: "unknown integer", node: map[string]any{"Integer": map[string]any{"ival": json.Number("2")}}, value: false, ok: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value, ok := postgresBooleanNode(test.node)
			require.Equal(t, test.value, value)
			require.Equal(t, test.ok, ok)
		})
	}

	unknownOptions := []any{map[string]any{
		"DefElem": map[string]any{
			"defname": "analyze",
			"arg":     map[string]any{"String": map[string]any{"sval": "on"}},
		},
	}}
	require.True(t, postgresDefElemEnabled(unknownOptions, "analyze"))
}

func TestPostgresExplainUsesSyntheticInnerDDL(t *testing.T) {
	node := map[string]any{
		"query": map[string]any{
			"AlterTableStmt": map[string]any{},
		},
		"options": []any{
			map[string]any{"DefElem": map[string]any{"defname": "analyze"}},
		},
	}

	stmtType, operations, err := postgresStatementSignals("ExplainStmt", node)

	require.NoError(t, err)
	require.Equal(t, model.StmtType("DDL"), stmtType)
	require.Equal(t, []string{"ALTER TABLE", "EXPLAIN ANALYZE"}, sortedStrings(operations))
}

func TestPostgresExistingClassificationRegression(t *testing.T) {
	tests := []struct {
		name     string
		sql      string
		stmtType model.StmtType
	}{
		{name: "drop schema", sql: "DROP SCHEMA reporting", stmtType: "DDL"},
		{name: "drop sequence", sql: "DROP SEQUENCE public.order_id_seq", stmtType: "DDL"},
		{name: "drop function", sql: "DROP FUNCTION public.refresh_reports()", stmtType: "DDL"},
		{name: "drop trigger", sql: "DROP TRIGGER audit_trigger ON public.orders", stmtType: "DDL"},
		{name: "drop type", sql: "DROP TYPE public.order_state", stmtType: "DDL"},
		{name: "drop materialized view", sql: "DROP MATERIALIZED VIEW public.order_summary", stmtType: "DDL"},
		{name: "create schema", sql: "CREATE SCHEMA reporting", stmtType: "DDL"},
		{name: "create index", sql: "CREATE INDEX idx_orders_id ON public.orders(id)", stmtType: "DDL"},
		{name: "create sequence", sql: "CREATE SEQUENCE public.order_id_seq", stmtType: "DDL"},
		{name: "create function", sql: "CREATE FUNCTION public.answer() RETURNS integer LANGUAGE SQL AS 'SELECT 42'", stmtType: "DDL"},
		{name: "create trigger", sql: "CREATE TRIGGER audit_trigger BEFORE INSERT ON public.orders FOR EACH ROW EXECUTE FUNCTION public.audit_order()", stmtType: "DDL"},
		{name: "alter index", sql: "ALTER INDEX public.idx_orders_id RENAME TO idx_orders_pk", stmtType: "DDL"},
		{name: "set", sql: "SET work_mem = '64MB'", stmtType: "ADMIN"},
		{name: "reset", sql: "RESET work_mem", stmtType: "ADMIN"},
		{name: "grant", sql: "GRANT SELECT ON public.orders TO report_user", stmtType: "ADMIN"},
		{name: "revoke", sql: "REVOKE SELECT ON public.orders FROM report_user", stmtType: "ADMIN"},
		{name: "truncate", sql: "TRUNCATE TABLE public.orders", stmtType: "DDL"},
		{name: "begin", sql: "BEGIN", stmtType: "ADMIN"},
		{name: "commit", sql: "COMMIT", stmtType: "ADMIN"},
		{name: "rollback", sql: "ROLLBACK", stmtType: "ADMIN"},
		{name: "savepoint", sql: "SAVEPOINT before_update", stmtType: "ADMIN"},
		{name: "select into", sql: "SELECT id INTO public.order_copy FROM public.orders", stmtType: "SELECT"},
	}

	approvedParser := &postgresParser{}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ast, err := approvedParser.Parse(test.sql)
			require.NoError(t, err)
			require.Equal(t, test.stmtType, ast.StmtType)
		})
	}
}

func TestPostgresTextCommentSignalIsDistinctFromCommentOn(t *testing.T) {
	tests := []struct {
		name       string
		sql        string
		operations []string
	}{
		{name: "block comment", sql: "/* audit */ SELECT 1", operations: []string{"SELECT", sqlCommentOperation}},
		{name: "line comment", sql: "-- audit\nSELECT 1", operations: []string{"SELECT", sqlCommentOperation}},
		{name: "comment on statement", sql: "COMMENT ON TABLE public.orders IS 'orders'", operations: []string{"COMMENT ON"}},
	}

	approvedParser := &postgresParser{}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ast, err := approvedParser.Parse(test.sql)
			require.NoError(t, err)
			require.Equal(t, sortedStrings(test.operations), sortedStrings(ast.Operations))
		})
	}
}

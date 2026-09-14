package parser

import (
	"fmt"
	"strings"
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestT231WhereTautologySignals(t *testing.T) {
	for _, dialect := range []model.DBDialect{postgresDialect, mysqlDialect} {
		t.Run(string(dialect), func(t *testing.T) {
			approved, err := NewParser(dialect)
			require.NoError(t, err)
			for _, sql := range []string{
				"UPDATE t SET a=1 WHERE id=id",
				"DELETE FROM t WHERE col=col",
				"UPDATE t SET a=1 WHERE EXISTS (SELECT 1)",
				"DELETE FROM t WHERE EXISTS (SELECT NULL)",
			} {
				ast, err := approved.Parse(sql)
				require.NoError(t, err, sql)
				require.True(t, ast.WhereTautology, sql)
			}
			for _, sql := range []string{
				"UPDATE t SET a=1 WHERE id=1",
				"UPDATE t SET a=1 WHERE EXISTS (SELECT 1 FROM other WHERE other.id=t.id)",
			} {
				ast, err := approved.Parse(sql)
				require.NoError(t, err, sql)
				require.False(t, ast.WhereTautology, sql)
			}
		})
	}
}

func TestT231QueryComplexitySignals(t *testing.T) {
	deepSQL := "SELECT * FROM t"
	for depth := 0; depth < 18; depth++ {
		deepSQL = fmt.Sprintf("SELECT * FROM (%s) AS q%d", deepSQL, depth)
	}
	unionParts := make([]string, 19)
	for index := range unionParts {
		unionParts[index] = "SELECT id FROM t"
	}
	unionSQL := strings.Join(unionParts, " UNION ALL ")
	for _, dialect := range []model.DBDialect{postgresDialect, mysqlDialect} {
		t.Run(string(dialect), func(t *testing.T) {
			approved, err := NewParser(dialect)
			require.NoError(t, err)
			deep, err := approved.Parse(deepSQL)
			require.NoError(t, err)
			require.Contains(t, deep.Operations, nestingDepthOperation+":18")
			union, err := approved.Parse(unionSQL)
			require.NoError(t, err)
			require.Contains(t, union.Operations, unionCountOperation+":18")
			require.Contains(t, union.Operations, nestingDepthOperation+":0")
			shallow, err := approved.Parse("SELECT id FROM t LIMIT 1")
			require.NoError(t, err)
			require.Contains(t, shallow.Operations, nestingDepthOperation+":0")
			require.Contains(t, shallow.Operations, unionCountOperation+":0")
			cte, err := approved.Parse("WITH q AS (SELECT id FROM t) SELECT id FROM q")
			require.NoError(t, err)
			require.Contains(t, cte.Operations, nestingDepthOperation+":1")
		})
	}
}

func TestT231AggregateAndProjectionSignals(t *testing.T) {
	for _, dialect := range []model.DBDialect{postgresDialect, mysqlDialect} {
		t.Run(string(dialect), func(t *testing.T) {
			approved, err := NewParser(dialect)
			require.NoError(t, err)
			pure, err := approved.Parse("SELECT count(*), sum(amount) FROM orders")
			require.NoError(t, err)
			require.True(t, pure.IsPureAggregate)
			require.False(t, pure.HasGroupBy)
			grouped, err := approved.Parse("SELECT status, count(*) FROM orders GROUP BY status")
			require.NoError(t, err)
			require.True(t, grouped.HasGroupBy)
			require.False(t, grouped.IsPureAggregate)
			mixed, err := approved.Parse("SELECT id, count(*) FROM orders")
			require.NoError(t, err)
			require.False(t, mixed.IsPureAggregate)
			projection, err := approved.Parse("SELECT id, name, amount FROM orders WHERE user_id=1 LIMIT 20")
			require.NoError(t, err)
			for _, column := range []string{"id", "name", "amount"} {
				require.Contains(t, projection.Operations, selectColumnOperation+":"+column)
			}
			require.NotContains(t, projection.Operations, selectColumnOperation+":user_id")
		})
	}
}

func TestT231PostgresDropAndIndexSignals(t *testing.T) {
	approved, err := NewParser(postgresDialect)
	require.NoError(t, err)
	for sql, operation := range map[string]string{
		"DROP SCHEMA secret CASCADE":                  "DROP SCHEMA",
		"DROP SEQUENCE seq_demo":                      "DROP SEQUENCE",
		"DROP FUNCTION f(integer)":                    "DROP FUNCTION",
		"DROP PROCEDURE p(integer)":                   "DROP PROCEDURE",
		"DROP VIEW old_view":                          "DROP VIEW",
		"DROP MATERIALIZED VIEW old_materialized":     "DROP MATERIALIZED VIEW",
		"CREATE INDEX idx_t_id ON t(id)":              "CREATE INDEX",
		"CREATE INDEX CONCURRENTLY idx_t_id ON t(id)": "CREATE INDEX CONCURRENTLY",
	} {
		ast, err := approved.Parse(sql)
		require.NoError(t, err, sql)
		require.Contains(t, ast.Operations, operation, sql)
	}
}

func TestT231MySQLCommentAndSetScopeSignals(t *testing.T) {
	approved, err := NewParser(mysqlDialect)
	require.NoError(t, err)
	for sql, operation := range map[string]string{
		"SELECT /*!50000 id*/ FROM users":            sqlCommentOperation,
		"SET GLOBAL max_connections = 100":           "SET GLOBAL",
		"SET @@global.max_connections = 100":         "SET GLOBAL",
		"SET SESSION sql_mode = 'STRICT_ALL_TABLES'": "SET SESSION",
		"SET @application_flag = 1":                  "SET SESSION",
	} {
		ast, err := approved.Parse(sql)
		require.NoError(t, err, sql)
		require.Contains(t, ast.Operations, operation, sql)
	}
	hint, err := approved.Parse("SELECT /*+ INDEX(users idx_users_id) */ id FROM users WHERE id=1 LIMIT 1")
	require.NoError(t, err)
	require.NotContains(t, hint.Operations, sqlCommentOperation)
}

func TestT231MultiStatementRetainsStructuredSignals(t *testing.T) {
	approved, err := NewParser(postgresDialect)
	require.NoError(t, err)
	ast, err := approved.Parse("SELECT 1; COPY users TO PROGRAM 'cat /etc/passwd'")
	require.Error(t, err)
	require.True(t, ast.IsMulti)
	require.Equal(t, model.StmtType("UNKNOWN"), ast.StmtType)
	require.Contains(t, ast.Operations, "COPY PROGRAM")
}

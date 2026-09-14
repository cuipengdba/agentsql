package parser

import (
	"fmt"
	"strings"
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestT232PostgresSetOperationComplexity(t *testing.T) {
	approved, err := NewParser(model.DBDialect("postgres"))
	require.NoError(t, err)

	unionParts := make([]string, 19)
	for index := range unionParts {
		unionParts[index] = "SELECT id FROM t"
	}
	unionAST, err := approved.Parse(strings.Join(unionParts, " UNION ALL "))
	require.NoError(t, err)
	require.Contains(t, unionAST.Operations, unionCountOperation+":18")
	require.Contains(t, unionAST.Operations, nestingDepthOperation+":0")

	deepSQL := "SELECT * FROM t"
	for depth := 0; depth < 18; depth++ {
		deepSQL = fmt.Sprintf("SELECT * FROM (%s) AS q%d", deepSQL, depth)
	}
	deepAST, err := approved.Parse(deepSQL)
	require.NoError(t, err)
	require.Contains(t, deepAST.Operations, nestingDepthOperation+":18")
	require.Contains(t, deepAST.Operations, unionCountOperation+":0")

	valuesAST, err := approved.Parse("VALUES (1) UNION ALL VALUES (2) UNION ALL VALUES (3)")
	require.NoError(t, err)
	require.Contains(t, valuesAST.Operations, unionCountOperation+":2")
	require.Contains(t, valuesAST.Operations, nestingDepthOperation+":0")
}

func TestT232MySQLJoinONCountsAsWhere(t *testing.T) {
	approved, err := NewParser(model.DBDialect("mysql"))
	require.NoError(t, err)
	tests := []struct {
		name           string
		sql            string
		hasWhere       bool
		whereTautology bool
	}{
		{name: "S040 joined delete", sql: "DELETE a FROM t1 a JOIN t2 b ON a.id=b.id", hasWhere: true},
		{name: "S041 joined update", sql: "UPDATE t1 JOIN t2 ON t1.id=t2.id SET t1.x=t2.y", hasWhere: true},
		{name: "tautological join condition", sql: "UPDATE t1 JOIN t2 ON 1=1 SET t1.x=1", hasWhere: true, whereTautology: true},
		{name: "multiple join conditions combine with and", sql: "UPDATE t1 JOIN t2 ON 1=1 JOIN t3 ON t2.id=t3.id SET t1.x=1", hasWhere: true},
		{name: "single delete remains unbounded", sql: "DELETE FROM t"},
		{name: "single update remains unbounded", sql: "UPDATE t SET x=1"},
		{name: "cartesian multi table remains unbounded", sql: "UPDATE t1, t2 SET t1.x=1"},
		{name: "join using has no on condition", sql: "DELETE t1 FROM t1 JOIN t2 USING (id)"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ast, err := approved.Parse(test.sql)
			require.NoError(t, err)
			require.Equal(t, test.hasWhere, ast.HasWhere)
			require.Equal(t, test.whereTautology, ast.WhereTautology)
		})
	}
}

func TestT232MySQLExplainUsesInnerStatementType(t *testing.T) {
	approved, err := NewParser(model.DBDialect("mysql"))
	require.NoError(t, err)
	tests := []struct {
		name       string
		sql        string
		stmtType   model.StmtType
		operations []string
	}{
		{name: "explain select", sql: "EXPLAIN SELECT id FROM users", stmtType: "SELECT", operations: []string{"EXPLAIN", "SELECT"}},
		{name: "explain union", sql: "EXPLAIN SELECT id FROM users UNION ALL SELECT id FROM archived_users", stmtType: "SELECT", operations: []string{"EXPLAIN", "SELECT"}},
		{name: "explain update", sql: "EXPLAIN UPDATE users SET name='x' WHERE id=1", stmtType: "UPDATE", operations: []string{"EXPLAIN", "UPDATE"}},
		{name: "explain table", sql: "EXPLAIN users", stmtType: "ADMIN", operations: []string{"EXPLAIN"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ast, err := approved.Parse(test.sql)
			require.NoError(t, err)
			require.Equal(t, test.stmtType, ast.StmtType)
			require.ElementsMatch(t, test.operations, legacyCorpusOperations(ast.Operations))
		})
	}
}

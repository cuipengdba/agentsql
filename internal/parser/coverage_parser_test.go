package parser

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
	"vitess.io/vitess/go/vt/sqlparser"
)

func TestCoverageParserRecoversDialectPanic(t *testing.T) {
	var approved *mysqlParser
	ast, err := approved.Parse("SELECT 1")
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrUnparseable))
	require.Equal(t, mysqlDialect, ast.Dialect)
	require.Equal(t, "SELECT 1", ast.RawSQL)
	require.Contains(t, err.Error(), "runtime error")

	panicError := recoveredError(postgresDialect, errors.New("synthetic parser panic"))
	require.True(t, errors.Is(panicError, ErrUnparseable))
	require.Contains(t, panicError.Error(), "synthetic parser panic")
}

func TestCoverageMySQLSecurityShapes(t *testing.T) {
	approved, err := NewParser(mysqlDialect)
	require.NoError(t, err)

	tests := []struct {
		name       string
		sql        string
		operation  string
		hasWhere   bool
		tautology  bool
		projection []string
	}{
		{
			name:      "select outfile",
			sql:       "SELECT * FROM users INTO OUTFILE '/tmp/a.csv'",
			operation: "INTO OUTFILE",
		},
		{
			name:      "select dumpfile",
			sql:       "SELECT * FROM users INTO DUMPFILE '/tmp/udf.so'",
			operation: "INTO DUMPFILE",
		},
		{
			name:       "single CTE star exposes source projection",
			sql:        "WITH q AS (SELECT id, email FROM users) SELECT * FROM q",
			operation:  "SELECT",
			projection: []string{"email", "id"},
		},
		{
			name:       "qualified CTE star through alias",
			sql:        "WITH q AS (SELECT id, email FROM users) SELECT x.* FROM q AS x",
			operation:  "SELECT",
			projection: []string{"email", "id"},
		},
		{
			name:      "nested update joins count as predicate",
			sql:       "UPDATE a JOIN (b JOIN c ON b.c_id = c.id) ON a.b_id = b.id SET a.flag = 1",
			operation: "UPDATE",
			hasWhere:  true,
		},
		{
			name:      "right branch union predicate",
			sql:       "SELECT id FROM a UNION ALL SELECT id FROM b WHERE id = id",
			operation: "SELECT",
			hasWhere:  true,
			tautology: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ast, parseErr := approved.Parse(test.sql)
			require.NoError(t, parseErr)
			require.Contains(t, ast.Operations, test.operation)
			require.Equal(t, test.hasWhere, ast.HasWhere)
			require.Equal(t, test.tautology, ast.WhereTautology)
			if test.projection != nil {
				for _, column := range test.projection {
					require.Contains(t, ast.Operations, selectColumnOperation+":"+column)
				}
			}
			require.NotContains(t, ast.Normalized, "/tmp/")
		})
	}
}

func TestMySQLExplainAnalyzeCarriesSideEffectOperation(t *testing.T) {
	approved, err := NewParser(mysqlDialect)
	require.NoError(t, err)
	ast, err := approved.Parse("EXPLAIN ANALYZE SELECT id FROM orders")
	require.NoError(t, err)
	require.Equal(t, model.StmtType("SELECT"), ast.StmtType)
	require.Contains(t, ast.Operations, "EXPLAIN ANALYZE")
}

func TestCoverageMySQLKeywordAndConstantHelpers(t *testing.T) {
	vitessParser, err := sqlparser.New(sqlparser.Options{})
	require.NoError(t, err)

	for _, test := range []struct {
		name    string
		sql     string
		keyword string
	}{
		{name: "leading comments", sql: "/* audit */ -- line\nSHOW TABLES", keyword: "SHOW"},
		{name: "identifier fallback", sql: "custom_token payload", keyword: "CUSTOM_TOKEN"},
	} {
		t.Run(test.name, func(t *testing.T) {
			keyword, keywordErr := mysqlFirstKeyword(vitessParser, test.sql)
			require.NoError(t, keywordErr)
			require.Equal(t, test.keyword, keyword)
		})
	}

	_, err = mysqlFirstKeyword(vitessParser, "")
	require.ErrorContains(t, err, "operation is empty")
	_, err = mysqlFirstKeyword(vitessParser, "\x00")
	require.ErrorContains(t, err, "lexer rejected")

	tests := []struct {
		name     string
		expr     sqlparser.Expr
		expected mysqlConstant
		ok       bool
	}{
		{name: "boolean", expr: sqlparser.BoolVal(true), expected: mysqlConstant{kind: "bool", value: "true"}, ok: true},
		{name: "integer", expr: sqlparser.NewIntLiteral("42"), expected: mysqlConstant{kind: "1", value: "42"}, ok: true},
		{name: "column is not constant", expr: &sqlparser.ColName{Name: sqlparser.NewIdentifierCI("id")}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			constant, ok := mysqlConstantValue(test.expr)
			require.Equal(t, test.ok, ok)
			require.Equal(t, test.expected, constant)
		})
	}

	require.True(t, mysqlConstantExpression(&sqlparser.NullVal{}))
	require.False(t, mysqlConstantExpression(&sqlparser.ColName{}))
	require.True(t, mysqlFromIsOnlyDual(nil))
	require.False(t, mysqlFromIsOnlyDual([]sqlparser.TableExpr{&sqlparser.JoinTableExpr{}}))
	require.False(t, mysqlFromIsOnlyDual([]sqlparser.TableExpr{
		&sqlparser.AliasedTableExpr{},
		&sqlparser.AliasedTableExpr{},
	}))
}

func TestCoverageMySQLJoinCollectorNilSafety(t *testing.T) {
	conditions := []sqlparser.Expr{}
	mysqlCollectJoinConditions((*sqlparser.JoinTableExpr)(nil), &conditions)
	mysqlCollectJoinConditions((*sqlparser.ParenTableExpr)(nil), &conditions)
	mysqlCollectJoinConditions(&sqlparser.AliasedTableExpr{}, nil)
	require.Empty(t, conditions)

	condition := &sqlparser.ComparisonExpr{
		Operator: sqlparser.EqualOp,
		Left:     sqlparser.NewIntLiteral("1"),
		Right:    sqlparser.NewIntLiteral("1"),
	}
	nested := &sqlparser.ParenTableExpr{Exprs: []sqlparser.TableExpr{
		&sqlparser.JoinTableExpr{Condition: &sqlparser.JoinCondition{On: condition}},
	}}
	mysqlCollectJoinConditions(nested, &conditions)
	require.Equal(t, []sqlparser.Expr{condition}, conditions)
}

func TestCoveragePostgresIntegerAndNodeHelpers(t *testing.T) {
	for _, test := range []struct {
		name  string
		value any
		want  int64
		ok    bool
	}{
		{name: "json number", value: json.Number("42"), want: 42, ok: true},
		{name: "invalid json number", value: json.Number("4.2")},
		{name: "int", value: int(7), want: 7, ok: true},
		{name: "int64", value: int64(8), want: 8, ok: true},
		{name: "float zero", value: float64(0), ok: true},
		{name: "float one", value: float64(1), want: 1, ok: true},
		{name: "fraction rejected", value: float64(0.5)},
		{name: "string rejected", value: "1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, ok := postgresIntegerValue(test.value)
			require.Equal(t, test.ok, ok)
			require.Equal(t, test.want, got)
		})
	}

	column, ok := postgresColumnRef(map[string]any{"fields": []any{
		map[string]any{"String": map[string]any{"sval": "users"}},
		map[string]any{"A_Star": map[string]any{}},
	}})
	require.True(t, ok)
	require.Equal(t, "*", column)
	_, ok = postgresColumnRef(map[string]any{"fields": []any{17}})
	require.False(t, ok)

	name, ok := postgresNameListField(map[string]any{"name": []any{
		map[string]any{"String": map[string]any{"sval": "pg_catalog"}},
		17,
		map[string]any{"String": map[string]any{"sval": "count"}},
	}}, "name")
	require.True(t, ok)
	require.Equal(t, "pg_catalog.count", name)
	_, ok = postgresNameListField(map[string]any{"name": []any{17}}, "name")
	require.False(t, ok)
}

func TestCoveragePostgresRootAndExplainFailures(t *testing.T) {
	_, _, err := postgresRoot(postgresRawStatement{})
	require.ErrorContains(t, err, "got 0")
	_, _, err = postgresRoot(postgresRawStatement{Statement: map[string]json.RawMessage{
		"SelectStmt": json.RawMessage(`{}`),
		"UpdateStmt": json.RawMessage(`{}`),
	}})
	require.ErrorContains(t, err, "got 2")
	_, _, err = postgresRoot(postgresRawStatement{Statement: map[string]json.RawMessage{
		"SelectStmt": json.RawMessage(`{`),
	}})
	require.ErrorContains(t, err, "decode PostgreSQL SelectStmt")

	for _, node := range []any{
		"not an object",
		map[string]any{},
		map[string]any{"query": map[string]any{}},
		map[string]any{"query": map[string]any{"SelectStmt": map[string]any{}, "DeleteStmt": map[string]any{}}},
	} {
		_, _, explainErr := postgresExplainQuery(node)
		require.Error(t, explainErr)
	}
	nodeType, node, err := postgresExplainQuery(map[string]any{
		"query": map[string]any{"SelectStmt": map[string]any{"limitCount": nil}},
	})
	require.NoError(t, err)
	require.Equal(t, "SelectStmt", nodeType)
	require.Equal(t, map[string]any{"limitCount": nil}, node)
}

func TestCoveragePostgresTautologyShapes(t *testing.T) {
	approved, err := NewParser(model.DBDialect("postgres"))
	require.NoError(t, err)

	for _, sql := range []string{
		"DELETE FROM t WHERE true OR id = 7",
		"DELETE FROM t WHERE (1 = 1) AND ('x'::text = 'x'::text)",
		"DELETE FROM t WHERE EXISTS (SELECT NULL::integer)",
		"DELETE FROM t WHERE EXISTS (SELECT true, 1, 'x')",
	} {
		ast, parseErr := approved.Parse(sql)
		require.NoError(t, parseErr, sql)
		require.True(t, ast.HasWhere, sql)
		require.True(t, ast.WhereTautology, sql)
	}

	for _, sql := range []string{
		"DELETE FROM t WHERE false AND 1 = 1",
		"DELETE FROM t WHERE EXISTS (SELECT NULL FROM other)",
		"DELETE FROM t WHERE EXISTS (SELECT 1 WHERE true)",
		"DELETE FROM t WHERE EXISTS (SELECT id)",
	} {
		ast, parseErr := approved.Parse(sql)
		require.NoError(t, parseErr, sql)
		require.True(t, ast.HasWhere, sql)
		require.False(t, ast.WhereTautology, sql)
	}
}

func TestCoveragePostgresConstantEncodings(t *testing.T) {
	tests := []struct {
		name     string
		value    any
		expected postgresConstant
		ok       bool
	}{
		{
			name:     "typed boolean",
			value:    map[string]any{"Boolean": map[string]any{"boolval": true}},
			expected: postgresConstant{kind: "bool", value: "true"},
			ok:       true,
		},
		{
			name: "string constant",
			value: map[string]any{"A_Const": map[string]any{
				"sval": map[string]any{"sval": "secret"},
			}},
			expected: postgresConstant{kind: "string", value: "secret"},
			ok:       true,
		},
		{
			name: "json numeric constant",
			value: map[string]any{"A_Const": map[string]any{
				"ival": map[string]any{"ival": json.Number("12")},
			}},
			expected: postgresConstant{kind: "integer", value: "12"},
			ok:       true,
		},
		{
			name:  "null is deliberately not comparable",
			value: map[string]any{"A_Const": map[string]any{"isnull": true}},
		},
		{
			name:  "unknown constant encoding",
			value: map[string]any{"A_Const": map[string]any{"ival": map[string]any{}}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			constant, ok := postgresConstantValue(test.value)
			require.Equal(t, test.ok, ok)
			require.Equal(t, test.expected, constant)
		})
	}

	typedNull := map[string]any{"TypeCast": map[string]any{
		"arg": map[string]any{"A_Const": map[string]any{"isnull": true}},
	}}
	require.True(t, postgresExistsConstantValue(typedNull))
	require.False(t, postgresExistsConstantValue(map[string]any{"ColumnRef": map[string]any{}}))
}

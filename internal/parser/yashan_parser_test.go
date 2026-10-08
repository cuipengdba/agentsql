package parser

import (
	"errors"
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestYashanOfflineSelectProfile(t *testing.T) {
	standard, err := NewParser(model.DialectYashan)
	require.NoError(t, err)
	require.IsType(t, &yashanParser{}, standard)
	require.Equal(t, model.DialectYashan, mustYashanDialect(t, standard))
	tests := []struct {
		name       string
		sql        string
		tables     []model.ObjectRef
		columns    []string
		hasWhere   bool
		tautology  bool
		hasLimit   bool
		outputName string
	}{
		{
			name: "DUAL constant", sql: "SELECT 1 AS one FROM DUAL",
			tables: []model.ObjectRef{{Table: "DUAL"}}, columns: []string{}, outputName: "one",
		},
		{
			name: "bounded ROWNUM", sql: "SELECT c.id FROM app.customers c WHERE ROWNUM <= 10",
			tables:  []model.ObjectRef{{Schema: "app", Table: "customers", Alias: "c"}},
			columns: []string{"id"}, hasWhere: true, hasLimit: true, outputName: "id",
		},
		{
			name: "quoted case and alias", sql: `SELECT "C"."Mixed" AS "Output" FROM "App"."Customers" "C"`,
			tables:  []model.ObjectRef{{Schema: "App", Table: "Customers", Alias: "C"}},
			columns: []string{"Mixed"}, outputName: "Output",
		},
		{
			name: "quoted syntax marker remains an identifier", sql: `SELECT "ILIKE" FROM customers`,
			tables: []model.ObjectRef{{Table: "customers"}}, columns: []string{"ILIKE"}, outputName: "ILIKE",
		},
		{
			name: "implicit column alias in ORDER BY", sql: "SELECT id identifier FROM customers ORDER BY identifier",
			tables: []model.ObjectRef{{Table: "customers"}}, columns: []string{"id"}, outputName: "identifier",
		},
		{
			name: "FETCH FIRST", sql: "SELECT id FROM customers FETCH FIRST 5 ROWS ONLY",
			tables: []model.ObjectRef{{Table: "customers"}}, columns: []string{"id"}, hasLimit: true, outputName: "id",
		},
		{
			name: "empty string IS NULL", sql: "SELECT id FROM customers WHERE '' IS NULL",
			tables: []model.ObjectRef{{Table: "customers"}}, columns: []string{"id"},
			hasWhere: true, tautology: true, outputName: "id",
		},
		{
			name: "empty string equality is unknown", sql: "SELECT id FROM customers WHERE '' = ''",
			tables: []model.ObjectRef{{Table: "customers"}}, columns: []string{"id"},
			hasWhere: true, outputName: "id",
		},
		{
			name: "join lineage", sql: "SELECT c.id, o.total FROM customers c JOIN orders o ON c.id = o.customer_id",
			tables:  []model.ObjectRef{{Table: "customers", Alias: "c"}, {Table: "orders", Alias: "o"}},
			columns: []string{"customer_id", "id", "total"}, hasWhere: true, outputName: "id",
		},
	}
	approved := NewYashanParser()
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ast, err := approved.Parse(test.sql)
			require.NoError(t, err)
			require.Equal(t, model.DialectYashan, ast.Dialect)
			require.Equal(t, test.sql, ast.RawSQL)
			require.Equal(t, model.StmtType("SELECT"), ast.StmtType)
			require.Equal(t, test.tables, ast.Tables)
			require.Equal(t, test.columns, ast.Columns)
			require.Equal(t, test.hasWhere, ast.HasWhere)
			require.Equal(t, test.tautology, ast.WhereTautology)
			require.Equal(t, test.hasLimit, ast.HasLimit)
			require.NotEmpty(t, ast.Normalized)
			require.Equal(t, test.outputName, ast.ProjectionLineages[0].OutputName)
			if test.name == "join lineage" {
				require.Equal(t, model.ObjectRef{Table: "customers"},
					ast.ProjectionLineages[0].Arms[0].Dependencies[0].Origin.Relation)
				require.Equal(t, model.ObjectRef{Table: "orders"},
					ast.ProjectionLineages[1].Arms[0].Dependencies[0].Origin.Relation)
			}
		})
	}
}

func mustYashanDialect(t *testing.T, approved Parser) model.DBDialect {
	t.Helper()
	ast, err := approved.Parse("SELECT 1 AS one FROM DUAL")
	require.NoError(t, err)
	return ast.Dialect
}

func TestYashanUnsupportedShapesFailClosed(t *testing.T) {
	tests := []struct {
		name string
		sql  string
	}{
		{"NVL function", "SELECT NVL(name, 'n/a') FROM customers"},
		{"DECODE function", "SELECT DECODE(state, 1, 'yes', 'no') FROM customers"},
		{"PostgreSQL LIMIT", "SELECT id FROM customers LIMIT 5"},
		{"PostgreSQL cast", "SELECT id::text FROM customers"},
		{"ILIKE alias ambiguity", "SELECT id ILIKE FROM customers"},
		{"PIVOT alias ambiguity", "SELECT id PIVOT FROM customers"},
		{"AS table alias", "SELECT c.id FROM customers AS c"},
		{"unsupported ROWNUM comparison", "SELECT id FROM customers WHERE ROWNUM > 1"},
		{"ROWNUM with OR", "SELECT id FROM customers WHERE ROWNUM < 5 OR active = 1"},
		{"hierarchical query", "SELECT id FROM customers CONNECT BY id = parent_id"},
		{"bind marker", "SELECT id FROM customers WHERE id = :id"},
		{"multiple statements", "SELECT id FROM customers; SELECT id FROM orders"},
		{"unterminated literal", "SELECT 'x FROM DUAL"},
		{"invalid UTF-8", "SELECT \xff FROM DUAL"},
	}
	approved := NewYashanParser()
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var firstError string
			for attempt := 0; attempt < 2; attempt++ {
				require.NotPanics(t, func() {
					ast, err := approved.Parse(test.sql)
					require.ErrorIs(t, err, ErrUnparseable)
					require.False(t, errors.Is(err, ErrUnsupportedDialect))
					require.Equal(t, &model.AST{Dialect: model.DialectYashan, RawSQL: test.sql}, ast)
					if attempt == 0 {
						firstError = err.Error()
					} else {
						require.Equal(t, firstError, err.Error())
					}
				})
			}
		})
	}
}

package pipeline

import (
	"testing"

	"github.com/cuipengdba/agentsql/internal/mask"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestPipelineProjectionLineageRedactionBothDialects(t *testing.T) {
	for _, dialect := range []string{"mysql", "postgres"} {
		dialect := dialect
		t.Run(dialect, func(t *testing.T) {
			castSQL := "SELECT CAST(phone AS CHAR) AS value FROM customers"
			emptyConcatSQL := "SELECT CONCAT(phone, '') AS value FROM customers"
			nonEmptyConcatSQL := "SELECT CONCAT(phone, 'x') AS value FROM customers"
			multiSourceSQL := "SELECT CONCAT(c.phone, o.note) AS value FROM customers c JOIN orders o ON o.id = c.id"
			if dialect == "postgres" {
				castSQL = "SELECT CAST(phone AS TEXT) AS value FROM customers"
				emptyConcatSQL = "SELECT phone || '' AS value FROM customers"
				nonEmptyConcatSQL = "SELECT phone || 'x' AS value FROM customers"
				multiSourceSQL = "SELECT c.phone || o.note AS value FROM customers c JOIN orders o ON o.id = c.id"
			}

			tests := []struct {
				name       string
				sql        string
				column     string
				input      string
				expected   string
				typeValue  mask.SensitiveType
				masked     int
				unresolved bool
			}{
				{name: "direct alias remains exact", sql: "SELECT c.phone AS mobile FROM customers c", column: "mobile", input: "13812345678", expected: "138****5678", typeValue: mask.TypePhone, masked: 1},
				{name: "qualified join remains exact", sql: "SELECT c.phone AS mobile FROM customers c JOIN orders o ON o.id = c.id", column: "mobile", input: "13812345678", expected: "138****5678", typeValue: mask.TypePhone, masked: 1},
				{name: "qualified self join remains exact", sql: "SELECT left_c.phone AS mobile FROM customers left_c JOIN customers right_c ON right_c.id = left_c.id", column: "mobile", input: "13812345678", expected: "138****5678", typeValue: mask.TypePhone, masked: 1},
				{name: "empty concat is exact", sql: emptyConcatSQL, column: "value", input: "13812345678", expected: "138****5678", typeValue: mask.TypePhone, masked: 1},
				{name: "upper email blocks", sql: "SELECT UPPER(email) AS value FROM customers", column: "value", input: "USER@EXAMPLE.COM", expected: mask.BlockPlaceholder, typeValue: mask.TypeEmail, masked: 1},
				{name: "substr phone blocks", sql: "SELECT SUBSTR(phone, 1, 3) AS value FROM customers", column: "value", input: "138", expected: mask.BlockPlaceholder, typeValue: mask.TypePhone, masked: 1},
				{name: "cast blocks", sql: castSQL, column: "value", input: "13812345678", expected: mask.BlockPlaceholder, typeValue: mask.TypePhone, masked: 1},
				{name: "nonempty concat blocks", sql: nonEmptyConcatSQL, column: "value", input: "13812345678x", expected: mask.BlockPlaceholder, typeValue: mask.TypePhone, masked: 1},
				{name: "multi source any sensitive blocks", sql: multiSourceSQL, column: "value", input: "13812345678note", expected: mask.BlockPlaceholder, typeValue: mask.TypePhone, masked: 1},
				{name: "count star stays visible", sql: "SELECT COUNT(*) AS value FROM customers", column: "value", input: "2", expected: "2"},
				{name: "count sensitive column blocks", sql: "SELECT COUNT(phone) AS value FROM customers", column: "value", input: "2", expected: mask.BlockPlaceholder, typeValue: mask.TypePhone, masked: 1},
				{name: "same column union is exact", sql: "SELECT phone FROM customers UNION ALL SELECT phone FROM customers", column: "phone", input: "13812345678", expected: "138****5678", typeValue: mask.TypePhone, masked: 1},
				{name: "different table union blocks", sql: "SELECT phone FROM customers UNION ALL SELECT phone FROM orders", column: "phone", input: "13812345678", expected: mask.BlockPlaceholder, typeValue: mask.TypePhone, masked: 1},
				{name: "constant union arm blocks", sql: "SELECT phone FROM customers UNION ALL SELECT 'literal'", column: "phone", input: "13812345678", expected: mask.BlockPlaceholder, typeValue: mask.TypePhone, masked: 1},
				{name: "cte direct column is exact", sql: "WITH x AS (SELECT phone FROM customers) SELECT phone FROM x", column: "phone", input: "13812345678", expected: "138****5678", typeValue: mask.TypePhone, masked: 1},
				{name: "derived direct column is exact", sql: "SELECT phone FROM (SELECT phone FROM customers) x", column: "phone", input: "13812345678", expected: "138****5678", typeValue: mask.TypePhone, masked: 1},
				{name: "join star related column blocks", sql: "SELECT * FROM customers c JOIN orders o ON o.id = c.id", column: "phone", input: "13812345678", expected: mask.BlockPlaceholder, typeValue: mask.TypePhone, masked: 1, unresolved: true},
			}

			for _, test := range tests {
				t.Run(test.name, func(t *testing.T) {
					fixture := newLineageDialectFixture(t, dialect)
					response := runT46S4Query(t, fixture, test.sql, model.QueryResult{
						Columns: []string{test.column}, Rows: [][]string{{test.input}}, RowCount: 1,
					})
					require.Equal(t, test.expected, response.Result.Rows[0][0])
					require.Equal(t, test.masked, response.Redact.MaskedCells)
					if test.masked == 0 {
						require.Empty(t, response.Redact.TouchedColumns)
					} else {
						require.Equal(t, map[int]mask.SensitiveType{0: test.typeValue}, response.Redact.TouchedColumns)
					}
					if test.unresolved {
						require.Equal(t, map[int]mask.SensitiveType{0: test.typeValue}, response.Redact.UnresolvedScopedColumns)
					} else {
						require.Empty(t, response.Redact.UnresolvedScopedColumns)
					}
				})
			}
		})
	}
}

func newLineageDialectFixture(t *testing.T, dialect string) *pipelineFixture {
	t.Helper()
	fixture := newT46S4Fixture(t, []mask.Rule{
		{Table: "customers", Column: "phone", SensitiveType: mask.TypePhone, Algorithm: mask.AlgoMask},
		{Table: "customers", Column: "email", SensitiveType: mask.TypeEmail, Algorithm: mask.AlgoMask},
	})
	fixture.datasources.datasource.DBType = dialect
	fixture.executor.dialect = dialect
	return fixture
}

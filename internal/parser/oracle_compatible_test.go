package parser

import (
	"errors"
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestOracleCompatibleParsersAreRegistered(t *testing.T) {
	for _, dialect := range []model.DBDialect{dmDialect, oracleDialect} {
		t.Run(string(dialect), func(t *testing.T) {
			approved, err := NewParser(dialect)
			require.NoError(t, err)
			require.IsType(t, &oracleCompatibleParser{}, approved)
		})
	}
}

func TestOracleCompatibleDirectProjectionLineage(t *testing.T) {
	for _, dialect := range []model.DBDialect{dmDialect, oracleDialect} {
		t.Run(string(dialect), func(t *testing.T) {
			ast := parseOracleCompatible(t, dialect,
				"SELECT c.id, c.name AS customer_name FROM app.customers c WHERE c.active = 1")

			require.Equal(t, model.StmtType("SELECT"), ast.StmtType)
			require.Equal(t, []model.ObjectRef{{Schema: "app", Table: "customers", Alias: "c"}}, ast.Tables)
			require.Equal(t, []string{"active", "id", "name"}, ast.Columns)
			require.True(t, ast.HasWhere)
			require.False(t, ast.WhereTautology)
			require.False(t, ast.HasLimit)
			require.NotEmpty(t, ast.Normalized)
			require.NotContains(t, ast.Normalized, " 1 ")

			require.Len(t, ast.ProjectionLineages, 2)
			assertOracleCompatibleDirectArm(t, ast.ProjectionLineages[0], "id", model.ObjectRef{Schema: "app", Table: "customers"})
			assertOracleCompatibleDirectArm(t, ast.ProjectionLineages[1], "customer_name", model.ObjectRef{Schema: "app", Table: "customers"})
			require.Len(t, ast.DirectProjections, 2)
			require.Equal(t, model.ObjectRef{Schema: "app", Table: "customers"}, ast.DirectProjections[0].Source)
		})
	}
}

func TestOracleCompatibleMultiTableAliasAndWildcardLineage(t *testing.T) {
	for _, dialect := range []model.DBDialect{dmDialect, oracleDialect} {
		t.Run(string(dialect), func(t *testing.T) {
			ast := parseOracleCompatible(t, dialect, `SELECT c.*, o.total
FROM customers c JOIN orders o ON c.id = o.customer_id
WHERE o.state = 'PAID'`)

			require.Equal(t, []model.ObjectRef{
				{Table: "customers", Alias: "c"},
				{Table: "orders", Alias: "o"},
			}, ast.Tables)
			require.Equal(t, []string{"customer_id", "id", "state", "total"}, ast.Columns)
			require.True(t, ast.HasWhere)

			wildcard := ast.ProjectionLineages[0]
			require.True(t, wildcard.Variadic)
			require.Equal(t, model.LineageWildcard, wildcard.Arms[0].Kind)
			require.Equal(t, model.LineageResolved, wildcard.Arms[0].Status)
			require.Equal(t, []model.ObjectRef{{Table: "customers"}}, wildcard.Arms[0].PossibleRelations)
			assertOracleCompatibleDirectArm(t, ast.ProjectionLineages[1], "total", model.ObjectRef{Table: "orders"})
		})
	}
}

func TestOracleCompatibleUnqualifiedWildcardAndAmbiguity(t *testing.T) {
	for _, dialect := range []model.DBDialect{dmDialect, oracleDialect} {
		t.Run(string(dialect), func(t *testing.T) {
			single := parseOracleCompatible(t, dialect, "SELECT * FROM customers")
			require.True(t, single.ProjectionLineages[0].Variadic)
			require.Equal(t, model.LineageWildcard, single.ProjectionLineages[0].Arms[0].Kind)
			require.Equal(t, []model.ObjectRef{{Table: "customers"}}, single.ProjectionLineages[0].Arms[0].PossibleRelations)

			multiple := parseOracleCompatible(t, dialect, "SELECT *, id FROM customers c, orders o")
			starArm := multiple.ProjectionLineages[0].Arms[0]
			require.Equal(t, model.LineageOpaque, starArm.Kind)
			require.Equal(t, model.LineageAmbiguous, starArm.Status)
			require.Equal(t, []model.ObjectRef{{Table: "customers"}, {Table: "orders"}}, starArm.PossibleRelations)
			columnArm := multiple.ProjectionLineages[1].Arms[0]
			require.Equal(t, model.LineageDirect, columnArm.Kind)
			require.Equal(t, model.LineageAmbiguous, columnArm.Status)
			require.Empty(t, columnArm.Dependencies)
			require.Equal(t, starArm.PossibleRelations, columnArm.PossibleRelations)
		})
	}
}

func TestOracleCompatibleDistinctConstantsAndTautology(t *testing.T) {
	for _, dialect := range []model.DBDialect{dmDialect, oracleDialect} {
		t.Run(string(dialect), func(t *testing.T) {
			ast := parseOracleCompatible(t, dialect,
				"SELECT DISTINCT 1 AS one, d.dummy FROM DUAL d WHERE d.dummy = 'X' OR 1 = 1")
			require.True(t, ast.WhereTautology)
			require.Equal(t, []string{"dummy"}, ast.Columns)
			require.Equal(t, model.LineageConstant, ast.ProjectionLineages[0].Arms[0].Kind)
			require.Equal(t, model.LineageSourceFree, ast.ProjectionLineages[0].Arms[0].Status)
			assertOracleCompatibleDirectArm(t, ast.ProjectionLineages[1], "dummy", model.ObjectRef{Table: "DUAL"})
		})
	}
}

func TestDMOracleCompatiblePaginationProfiles(t *testing.T) {
	dmCases := []string{
		"SELECT TOP 2 id FROM customers ORDER BY id",
		"SELECT DISTINCT id FROM customers ORDER BY id LIMIT 10 OFFSET 5",
		"SELECT id FROM customers LIMIT 5, 10",
		"SELECT id FROM customers OFFSET 5 ROWS FETCH NEXT 10 ROWS ONLY",
	}
	for _, sql := range dmCases {
		ast := parseOracleCompatible(t, dmDialect, sql)
		require.True(t, ast.HasLimit, sql)
	}

	oracleCases := []string{
		"SELECT DISTINCT id FROM customers ORDER BY id OFFSET 5 ROWS FETCH NEXT 10 ROWS ONLY",
		"SELECT id FROM customers FETCH FIRST 10 ROWS ONLY",
	}
	for _, sql := range oracleCases {
		ast := parseOracleCompatible(t, oracleDialect, sql)
		require.True(t, ast.HasLimit, sql)
	}

	for _, sql := range []string{
		"SELECT TOP 2 id FROM customers",
		"SELECT id FROM customers LIMIT 2",
	} {
		assertOracleCompatibleRejected(t, oracleDialect, sql)
	}
}

func TestOracleCompatibleUnknownStructuresFailClosed(t *testing.T) {
	tests := []string{
		"",
		"UPDATE customers SET name = 'x'",
		"SELECT id FROM customers; SELECT id FROM orders",
		"SELECT id FROM (SELECT id FROM customers) nested",
		"SELECT UPPER(name) FROM customers",
		"SELECT price + tax FROM orders",
		"SELECT CASE WHEN active = 1 THEN name ELSE email END FROM customers",
		"SELECT id FROM customers UNION SELECT id FROM archive",
		"WITH c AS (SELECT id FROM customers) SELECT id FROM c",
		"SELECT id FROM customers GROUP BY id",
		"SELECT id FROM customers CONNECT BY id = parent_id",
		"SELECT id",
		"SELECT *",
		"SELECT 1",
		"SELECT c.id FROM customers x",
		"SELECT c.id FROM customers c WHERE missing.id = 1",
		"SELECT a.id FROM a JOIN b ON future.id = b.id JOIN future ON future.id = a.id",
		"SELECT id FROM customers -- comment",
		"SELECT id FROM customers WHERE id IN (1, 2)",
	}
	for _, dialect := range []model.DBDialect{dmDialect, oracleDialect} {
		t.Run(string(dialect), func(t *testing.T) {
			for _, sql := range tests {
				t.Run(sql, func(t *testing.T) {
					assertOracleCompatibleRejected(t, dialect, sql)
				})
			}
		})
	}
}

func TestOracleCompatibleQuotedIdentifiers(t *testing.T) {
	for _, dialect := range []model.DBDialect{dmDialect, oracleDialect} {
		ast := parseOracleCompatible(t, dialect,
			`SELECT "C"."MixedName" AS "Output" FROM "App"."Customer" "C"`)
		require.Equal(t, []model.ObjectRef{{Schema: "App", Table: "Customer", Alias: "C"}}, ast.Tables)
		require.Equal(t, []string{"MixedName"}, ast.Columns)
		assertOracleCompatibleDirectArm(t, ast.ProjectionLineages[0], "Output", model.ObjectRef{Schema: "App", Table: "Customer"})
	}
}

func parseOracleCompatible(t *testing.T, dialect model.DBDialect, sql string) *model.AST {
	t.Helper()
	approved, err := NewParser(dialect)
	require.NoError(t, err)
	ast, err := approved.Parse(sql)
	require.NoError(t, err, sql)
	require.NotNil(t, ast)
	require.Equal(t, dialect, ast.Dialect)
	require.Equal(t, sql, ast.RawSQL)
	return ast
}

func assertOracleCompatibleRejected(t *testing.T, dialect model.DBDialect, sql string) {
	t.Helper()
	approved, err := NewParser(dialect)
	require.NoError(t, err)
	ast, err := approved.Parse(sql)
	require.Error(t, err, sql)
	require.ErrorIs(t, err, ErrUnparseable, sql)
	require.NotNil(t, ast)
	require.Equal(t, dialect, ast.Dialect)
	require.Equal(t, sql, ast.RawSQL)
	require.False(t, errors.Is(err, ErrUnsupportedDialect))
}

func assertOracleCompatibleDirectArm(
	t *testing.T,
	lineage model.ProjectionLineage,
	output string,
	relation model.ObjectRef,
) {
	t.Helper()
	require.Equal(t, output, lineage.OutputName)
	require.False(t, lineage.Variadic)
	require.Len(t, lineage.Arms, 1)
	arm := lineage.Arms[0]
	require.Equal(t, model.LineageDirect, arm.Kind)
	require.Equal(t, model.LineageResolved, arm.Status)
	require.Len(t, arm.Dependencies, 1)
	require.Equal(t, relation, arm.Dependencies[0].Origin.Relation)
	require.Equal(t, model.DependencyValue, arm.Dependencies[0].Role)
}

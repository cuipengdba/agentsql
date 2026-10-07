package parser

import (
	"errors"
	"sort"
	"testing"
	"time"

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

func TestOracleRownumBoundedWhere(t *testing.T) {
	for _, sql := range []string{
		"SELECT id FROM app.customers WHERE ROWNUM < 10",
		"SELECT id FROM app.customers WHERE active = 1 AND ROWNUM <= 10 ORDER BY id",
		"SELECT 1 AS one FROM DUAL WHERE ROWNUM = 1",
	} {
		t.Run(sql, func(t *testing.T) {
			ast := parseOracleCompatible(t, oracleDialect, sql)
			require.True(t, ast.HasWhere)
			require.True(t, ast.HasLimit)
			require.NotContains(t, ast.Columns, "ROWNUM")
			require.False(t, ast.WhereTautology)
		})
	}
}

func TestOracleRownumUnsupportedShapesFailClosed(t *testing.T) {
	for _, sql := range []string{
		"SELECT ROWNUM FROM customers",
		"SELECT id FROM customers WHERE ROWNUM > 1",
		"SELECT id FROM customers WHERE 1 = ROWNUM",
		"SELECT id FROM customers WHERE ROWNUM <= 5 OR active = 1",
		"SELECT id FROM customers WHERE active = 1 OR ROWNUM < 5",
		"SELECT id FROM customers WHERE ROWNUM < 5 AND ROWNUM < 3",
		"SELECT id FROM customers WHERE ROWNUM <= -1",
		"SELECT id FROM customers WHERE ROWNUM <= 1.5",
		"SELECT id FROM customers JOIN orders ON ROWNUM < 5",
	} {
		t.Run(sql, func(t *testing.T) { assertOracleCompatibleRejected(t, oracleDialect, sql) })
	}
	assertOracleCompatibleRejected(t, dmDialect, "SELECT id FROM customers WHERE ROWNUM <= 5")
}

func TestOracleCompatibleOrderByProjectionReferences(t *testing.T) {
	for _, dialect := range []model.DBDialect{dmDialect, oracleDialect} {
		t.Run(string(dialect), func(t *testing.T) {
			for _, test := range []struct {
				sql     string
				columns []string
			}{
				{"SELECT c.id AS identifier FROM app.customers c ORDER BY identifier", []string{"id"}},
				{`SELECT c.id AS "Output" FROM app.customers c ORDER BY "Output"`, []string{"id"}},
				{"SELECT c.id, c.name FROM app.customers c ORDER BY 2 DESC, 1", []string{"id", "name"}},
				{"SELECT 1 AS one FROM DUAL ORDER BY one", []string{}},
				{"SELECT c.id FROM app.customers c ORDER BY c.id", []string{"id"}},
			} {
				ast := parseOracleCompatible(t, dialect, test.sql)
				require.Equal(t, test.columns, ast.Columns, test.sql)
			}
			for _, sql := range []string{
				"SELECT id FROM customers ORDER BY 0",
				"SELECT id FROM customers ORDER BY 2",
				"SELECT id FROM customers ORDER BY 1.5",
				"SELECT id AS x, name AS x FROM customers ORDER BY x",
			} {
				assertOracleCompatibleRejected(t, dialect, sql)
			}
		})
	}
}

func TestOracleNullPredicateBoundaries(t *testing.T) {
	for _, dialect := range []model.DBDialect{dmDialect, oracleDialect} {
		t.Run(string(dialect), func(t *testing.T) {
			for _, sql := range []string{
				"SELECT id FROM customers WHERE NULL = NULL",
				"SELECT id FROM customers WHERE NULL != NULL",
				"SELECT id FROM customers WHERE NULL = NULL OR 1 = 0",
			} {
				ast := parseOracleCompatible(t, dialect, sql)
				require.False(t, ast.WhereTautology, sql)
			}
			ast := parseOracleCompatible(t, dialect, "SELECT id FROM customers WHERE NULL IS NULL")
			require.True(t, ast.WhereTautology)
		})
	}
	for _, test := range []struct {
		sql       string
		tautology bool
	}{
		{"SELECT id FROM customers WHERE '' IS NULL", true},
		{"SELECT id FROM customers WHERE '' IS NOT NULL", false},
		{"SELECT id FROM customers WHERE '' = ''", false},
	} {
		ast := parseOracleCompatible(t, oracleDialect, test.sql)
		require.Equal(t, test.tautology, ast.WhereTautology, test.sql)
	}
	for _, sql := range []string{
		"SELECT id FROM customers WHERE '' IS NULL",
		"SELECT id FROM customers WHERE '' IS NOT NULL",
		"SELECT id FROM customers WHERE '' = ''",
	} {
		ast := parseOracleCompatible(t, dmDialect, sql)
		require.False(t, ast.WhereTautology, sql)
	}
}

func TestDMOraclePaginationAndAdministrativeBoundaries(t *testing.T) {
	for _, test := range []struct {
		dialect model.DBDialect
		sql     string
		accept  bool
	}{
		{dmDialect, "SELECT id FROM app.customers LIMIT 5 OFFSET 2", true},
		{dmDialect, "SELECT id FROM app.customers LIMIT 2, 5", true},
		{dmDialect, "SELECT TOP 5 id FROM app.customers", true},
		{oracleDialect, "SELECT id FROM app.customers OFFSET 2 ROWS FETCH NEXT 5 ROWS ONLY", true},
		{oracleDialect, "SELECT id FROM app.customers FETCH FIRST 5 ROWS ONLY", true},
		{oracleDialect, "SELECT id FROM app.customers LIMIT 5", false},
		{oracleDialect, "SELECT TOP 5 id FROM app.customers", false},
		{dmDialect, "SELECT id FROM app.customers LIMIT 5 OFFSET -1", false},
		{dmDialect, "SELECT id FROM app.customers LIMIT 5,", false},
		{dmDialect, "SELECT TOP 5 id FROM app.customers LIMIT 2", false},
		{dmDialect, "SELECT TOP 5 id FROM app.customers FETCH FIRST 2 ROWS ONLY", false},
		{oracleDialect, "SELECT id FROM app.customers FETCH FIRST 5 ROWS WITH TIES", false},
		{oracleDialect, "ALTER SESSION SET CURRENT_SCHEMA = app", false},
		{dmDialect, "BACKUP DATABASE FULL TO local_backup", false},
		{dmDialect, "RESTORE DATABASE FROM local_backup", false},
		{oracleDialect, "MERGE INTO customers USING incoming ON customers.id = incoming.id", false},
	} {
		t.Run(string(test.dialect)+"/"+test.sql, func(t *testing.T) {
			if test.accept {
				ast := parseOracleCompatible(t, test.dialect, test.sql)
				require.True(t, ast.HasLimit)
			} else {
				assertOracleCompatibleRejected(t, test.dialect, test.sql)
			}
		})
	}
}

func TestDMOracleCompatibleP99Budget(t *testing.T) {
	const sql = `SELECT c.id, c.name, c.status, c.region, o.id, o.total, o.state, o.created_at
FROM app.customers c JOIN app.orders o ON c.id = o.customer_id
WHERE c.active = 1 AND o.state = 'PAID' ORDER BY o.created_at FETCH FIRST 100 ROWS ONLY`
	const parses = 2000
	for _, dialect := range []model.DBDialect{dmDialect, oracleDialect} {
		t.Run(string(dialect), func(t *testing.T) {
			approved, err := NewParser(dialect)
			require.NoError(t, err)
			_, err = approved.Parse(sql)
			require.NoError(t, err)
			durations := make([]time.Duration, parses)
			for i := range durations {
				start := time.Now()
				_, err = approved.Parse(sql)
				durations[i] = time.Since(start)
				require.NoError(t, err)
			}
			sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
			p99 := durations[(parses*99+99)/100-1]
			t.Logf("%s representative SELECT parses=%d P99=%s", dialect, parses, p99)
			require.LessOrEqual(t, p99, 5*time.Millisecond)
		})
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

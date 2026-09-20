package parser

import (
	"fmt"
	"strings"
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestPostgresLineageDirectTransparentAndComposite(t *testing.T) {
	ast := parsePostgresLineage(t, `SELECT
		p.phone,
		p.phone COLLATE "C" AS collated,
		COALESCE(p.phone, NULL) AS fallback,
		p.phone || '' AS right_empty,
		'' || p.phone AS left_empty,
		upper(p.phone) AS upper_phone,
		trim(p.phone) AS trimmed,
		p.phone::text AS casted,
		substring(p.phone FROM 2) AS sliced,
		p.phone || p.phone AS duplicated,
		p.payload->>'phone' AS json_phone,
		p.amount + 1 AS arithmetic
	FROM app.people p`)

	require.Len(t, ast.ProjectionLineages, 12)
	require.Equal(t, model.ProjectionLineage{
		SelectIndex: 0, OutputName: "phone",
		Arms: []model.LineageArm{{
			Kind: model.LineageDirect, Operation: "column", Status: model.LineageResolved,
			Dependencies:      []model.ColumnDependency{lineageDependency("app", "people", "phone", 0, model.DependencyValue)},
			PossibleRelations: []model.ObjectRef{{Schema: "app", Table: "people"}},
		}},
	}, ast.ProjectionLineages[0])
	assertPostgresArm(t, ast.ProjectionLineages[0].Arms[0], model.LineageDirect, "column", model.LineageResolved)
	for index, operation := range []string{"collate", "coalesce_null", "concat_empty", "concat_empty"} {
		arm := ast.ProjectionLineages[index+1].Arms[0]
		assertPostgresArm(t, arm, model.LineageTransparent, operation, model.LineageResolved)
		assertPostgresDependency(t, arm.Dependencies[0], "app", "people", "phone", model.DependencyValue, 0)
	}
	operations := []string{"upper", "trim", "cast", "substr", "concat", "json", "arithmetic"}
	for index, operation := range operations {
		arm := ast.ProjectionLineages[index+5].Arms[0]
		assertPostgresArm(t, arm, model.LineageComposite, operation, model.LineageResolved)
		require.NotEmpty(t, arm.Dependencies)
	}
	require.Len(t, ast.ProjectionLineages[9].Arms[0].Dependencies, 1, "dependency dedup must not make repeated columns transparent")
}

func TestPostgresLineageCaseAggregateGroupAndFilter(t *testing.T) {
	sourceFree := parsePostgresLineage(t, `SELECT count(*) AS total, count(1) AS constants FROM people`)
	assertPostgresArm(t, sourceFree.ProjectionLineages[0].Arms[0], model.LineageAggregate, "aggregate:count_star", model.LineageSourceFree)
	assertPostgresArm(t, sourceFree.ProjectionLineages[1].Arms[0], model.LineageAggregate, "aggregate:count_constant", model.LineageSourceFree)

	ast := parsePostgresLineage(t, `SELECT
		CASE WHEN p.secret_flag THEN 1 ELSE p.phone END AS chosen,
		count(*) AS total,
		count(1) AS constants,
		count(DISTINCT p.phone) FILTER (WHERE p.active) AS distinct_phones,
		max(p.phone) AS max_phone
	FROM people p GROUP BY p.tenant_id`)

	caseArm := ast.ProjectionLineages[0].Arms[0]
	assertPostgresArm(t, caseArm, model.LineageComposite, "case", model.LineageResolved)
	assertPostgresDependencyRoles(t, caseArm.Dependencies, map[string]model.DependencyRole{
		"secret_flag": model.DependencyControl,
		"phone":       model.DependencyValue,
	})

	assertPostgresArm(t, ast.ProjectionLineages[1].Arms[0], model.LineageAggregate, "aggregate:count_star", model.LineageResolved)
	assertPostgresArm(t, ast.ProjectionLineages[2].Arms[0], model.LineageAggregate, "aggregate:count_constant", model.LineageResolved)
	require.Equal(t, model.DependencyGroup, dependencyRoleForColumn(ast.ProjectionLineages[1].Arms[0], "tenant_id"))
	require.Equal(t, model.DependencyGroup, dependencyRoleForColumn(ast.ProjectionLineages[2].Arms[0], "tenant_id"))
	distinct := ast.ProjectionLineages[3].Arms[0]
	assertPostgresArm(t, distinct, model.LineageAggregate, "aggregate:count", model.LineageResolved)
	assertPostgresDependencyRoles(t, distinct.Dependencies, map[string]model.DependencyRole{
		"phone":     model.DependencyValue,
		"active":    model.DependencyFilter,
		"tenant_id": model.DependencyGroup,
	})
	maximum := ast.ProjectionLineages[4].Arms[0]
	assertPostgresArm(t, maximum, model.LineageAggregate, "aggregate:max", model.LineageResolved)
	assertPostgresDependencyRoles(t, maximum.Dependencies, map[string]model.DependencyRole{
		"phone":     model.DependencyValue,
		"tenant_id": model.DependencyGroup,
	})
}

func TestPostgresLineageWindowAndDistinctOn(t *testing.T) {
	ast := parsePostgresLineage(t, `SELECT
		row_number() OVER (PARTITION BY tenant_id ORDER BY created_at) AS rn,
		lag(phone) OVER w AS previous_phone
	FROM people
	WINDOW w AS (PARTITION BY tenant_id ORDER BY created_at)`)
	require.Len(t, ast.ProjectionLineages, 2)
	for index, operation := range []string{"window:row_number", "window:lag"} {
		arm := ast.ProjectionLineages[index].Arms[0]
		assertPostgresArm(t, arm, model.LineageWindow, operation, model.LineageResolved)
		assertPostgresDependencyRoles(t, arm.Dependencies, map[string]model.DependencyRole{
			"tenant_id":  model.DependencyGroup,
			"created_at": model.DependencyOrder,
		})
	}
	require.Equal(t, model.DependencyValue, dependencyRoleForColumn(ast.ProjectionLineages[1].Arms[0], "phone"))

	distinct := parsePostgresLineage(t, `SELECT DISTINCT ON (tenant_id) phone FROM people ORDER BY tenant_id`)
	arm := distinct.ProjectionLineages[0].Arms[0]
	assertPostgresArm(t, arm, model.LineageComposite, "distinct_on", model.LineageResolved)
	assertPostgresDependencyRoles(t, arm.Dependencies, map[string]model.DependencyRole{
		"phone": model.DependencyValue, "tenant_id": model.DependencyControl,
	})
}

func TestPostgresLineageSetOperations(t *testing.T) {
	tests := []struct {
		sql       string
		operation string
		tables    []string
	}{
		{"SELECT phone FROM customers UNION ALL SELECT phone FROM archive", "union_all", []string{"customers", "archive"}},
		{"SELECT phone FROM customers UNION SELECT phone FROM archive", "union_distinct", []string{"customers", "archive"}},
		{"SELECT phone FROM customers INTERSECT SELECT phone FROM archive", "intersect", []string{"customers", "archive"}},
		{"SELECT phone FROM customers EXCEPT SELECT phone FROM archive", "except", []string{"customers", "archive"}},
		{"SELECT phone FROM customers UNION SELECT phone FROM archive INTERSECT SELECT phone FROM current_customers", "mixed", []string{"customers", "archive", "current_customers"}},
	}
	for _, test := range tests {
		ast := parsePostgresLineage(t, test.sql)
		require.Len(t, ast.ProjectionLineages, 1)
		lineage := ast.ProjectionLineages[0]
		require.Equal(t, test.operation, lineage.SetOp)
		require.Len(t, lineage.Arms, len(test.tables))
		for index, table := range test.tables {
			assertPostgresDependency(t, lineage.Arms[index].Dependencies[0], "", table, "phone", model.DependencyValue, 0)
		}
		require.Nil(t, ast.DirectProjections)
	}

	star := parsePostgresLineage(t, "SELECT * FROM customers UNION ALL SELECT * FROM archive")
	require.True(t, star.ProjectionLineages[0].Variadic)
	assertPostgresArm(t, star.ProjectionLineages[0].Arms[0], model.LineageWildcard, "wildcard", model.LineageResolved)
	assertPostgresArm(t, star.ProjectionLineages[0].Arms[1], model.LineageOpaque, "set_branch_wildcard", model.LineageOpaqueState)
}

func TestPostgresLineageCTEDerivedLateralAndScalarRoutes(t *testing.T) {
	tests := []struct {
		name  string
		sql   string
		route model.LineageRoute
	}{
		{"cte", "WITH x(mobile) AS (SELECT phone FROM customers) SELECT x.mobile FROM x", model.RouteCTE},
		{"derived", "SELECT x.mobile FROM (SELECT phone FROM customers) x(mobile)", model.RouteDerived},
		{"lateral", "SELECT x.phone FROM customers c JOIN LATERAL (SELECT c.phone) x ON true", model.RouteDerived | model.RouteLateral},
		{"scalar", "SELECT (SELECT c.phone) AS mobile FROM customers c", model.RouteScalarSubquery},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ast := parsePostgresLineage(t, test.sql)
			arm := ast.ProjectionLineages[0].Arms[0]
			assertPostgresArm(t, arm, model.LineageDirect, "column", model.LineageResolved)
			require.Len(t, arm.Dependencies, 1)
			require.Equal(t, test.route, arm.Dependencies[0].Origin.Route)
			require.Empty(t, arm.Dependencies[0].Origin.Relation.Alias)
		})
	}

	correlated := parsePostgresLineage(t, `SELECT (SELECT u.phone FROM users u WHERE u.id = c.user_id) FROM customers c`)
	arm := correlated.ProjectionLineages[0].Arms[0]
	assertPostgresArm(t, arm, model.LineageComposite, "scalar_subquery", model.LineageResolved)
	assertPostgresDependencyRoles(t, arm.Dependencies, map[string]model.DependencyRole{
		"phone": model.DependencyValue, "id": model.DependencyFilter, "user_id": model.DependencyFilter,
	})
	for _, dependency := range arm.Dependencies {
		require.NotZero(t, dependency.Origin.Route&model.RouteScalarSubquery)
	}

	nonLateral := parsePostgresLineage(t, "SELECT x.phone FROM customers c JOIN (SELECT c.phone) x ON true")
	require.NotEqual(t, model.LineageResolved, nonLateral.ProjectionLineages[0].Arms[0].Status)

	lateralPredicate := parsePostgresLineage(t, "SELECT x.phone FROM customers c JOIN LATERAL (SELECT c.phone WHERE c.id > 0) x ON true")
	lateralArm := lateralPredicate.ProjectionLineages[0].Arms[0]
	assertPostgresArm(t, lateralArm, model.LineageComposite, "lateral_subquery", model.LineageResolved)
	require.Equal(t, model.DependencyFilter, dependencyRoleForColumn(lateralArm, "id"))

	nestedScalar := parsePostgresLineage(t, "SELECT (SELECT o.phone FROM orders o) || '-suffix' FROM customers c")
	nestedArm := nestedScalar.ProjectionLineages[0].Arms[0]
	assertPostgresArm(t, nestedArm, model.LineageComposite, "concat", model.LineageResolved)
	require.Equal(t, model.DependencyValue, dependencyRoleForColumn(nestedArm, "phone"))
	require.NotZero(t, nestedArm.Dependencies[0].Origin.Route&model.RouteScalarSubquery)
}

func TestPostgresLineageWildcardsOpaqueSourcesAndUnknown(t *testing.T) {
	single := parsePostgresLineage(t, "SELECT *, t.* FROM tenant.people t")
	for _, lineage := range single.ProjectionLineages {
		require.True(t, lineage.Variadic)
		assertPostgresArm(t, lineage.Arms[0], model.LineageWildcard, "wildcard", model.LineageResolved)
		require.Equal(t, []model.ObjectRef{{Schema: "tenant", Table: "people"}}, lineage.Arms[0].PossibleRelations)
	}
	multiple := parsePostgresLineage(t, "SELECT *, t.phone, t.* FROM tenant.people t")
	require.True(t, multiple.ProjectionLineages[0].Variadic)
	require.False(t, multiple.ProjectionLineages[1].Variadic)
	require.True(t, multiple.ProjectionLineages[2].Variadic)
	assertPostgresArm(t, multiple.ProjectionLineages[1].Arms[0], model.LineageDirect, "column", model.LineageResolved)

	joined := parsePostgresLineage(t, "SELECT * FROM customers c JOIN orders o ON c.id=o.id")
	assertPostgresArm(t, joined.ProjectionLineages[0].Arms[0], model.LineageOpaque, "wildcard_relation_ambiguous", model.LineageAmbiguous)

	rangeFunction := parsePostgresLineage(t, "SELECT f.x FROM customers c, LATERAL unnest(c.phone) AS f(x)")
	arm := rangeFunction.ProjectionLineages[0].Arms[0]
	assertPostgresArm(t, arm, model.LineageOpaque, "range_function", model.LineageResolved)
	require.Equal(t, model.DependencyValue, dependencyRoleForColumn(arm, "phone"))
	for _, dependency := range arm.Dependencies {
		if dependency.Origin.Column == "phone" {
			require.NotZero(t, dependency.Origin.Route&model.RouteLateral)
		}
	}
	require.Len(t, rangeFunction.DirectProjections, 1)
	require.Equal(t, "x", rangeFunction.DirectProjections[0].Column)
	require.Empty(t, rangeFunction.DirectProjections[0].Source.Table)

	rowsFrom := parsePostgresLineage(t, "SELECT f.x FROM customers c, LATERAL ROWS FROM (unnest(c.phone)) AS f(x)")
	rowsArm := rowsFrom.ProjectionLineages[0].Arms[0]
	assertPostgresArm(t, rowsArm, model.LineageOpaque, "range_function", model.LineageResolved)
	require.Equal(t, model.DependencyValue, dependencyRoleForColumn(rowsArm, "phone"))

	builder := &postgresLineageBuilder{}
	unknown := builder.postgresExprLineage(map[string]any{
		"FutureNode": map[string]any{"value": map[string]any{"ColumnRef": map[string]any{
			"fields": []any{map[string]any{"String": map[string]any{"sval": "phone"}}},
		}}},
	}, newLineageScope(nil), nil)
	assertPostgresArm(t, unknown, model.LineageOpaque, "unsupported:FutureNode", model.LineageUnsupported)
}

func TestPostgresLineageCTEColumnMismatchAndJoinMergedColumns(t *testing.T) {
	mismatch := parsePostgresLineage(t, "WITH x(a,b) AS (SELECT phone FROM customers) SELECT x.a FROM x")
	arm := mismatch.ProjectionLineages[0].Arms[0]
	require.NotEqual(t, model.LineageResolved, arm.Status)

	chain := parsePostgresLineage(t, "WITH first(mobile) AS (SELECT phone FROM customers), second AS (SELECT mobile FROM first), unused AS (SELECT id FROM orders) SELECT second.mobile FROM second")
	chainArm := chain.ProjectionLineages[0].Arms[0]
	assertPostgresArm(t, chainArm, model.LineageDirect, "column", model.LineageResolved)
	require.Equal(t, model.RouteCTE, chainArm.Dependencies[0].Origin.Route)
	require.Equal(t, []model.ObjectRef{{Table: "customers"}}, chainArm.PossibleRelations)

	recursive := parsePostgresLineage(t, "WITH RECURSIVE x(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM x WHERE n<2) SELECT x.n FROM x")
	require.NotEqual(t, model.LineageResolved, recursive.ProjectionLineages[0].Arms[0].Status)

	for _, sql := range []string{
		"SELECT id FROM customers c JOIN orders o USING (id)",
		"SELECT id FROM customers c NATURAL JOIN orders o",
	} {
		ast := parsePostgresLineage(t, sql)
		arm := ast.ProjectionLineages[0].Arms[0]
		assertPostgresArm(t, arm, model.LineageOpaque, "joined_merged_column", model.LineageAmbiguous)
		require.Len(t, arm.PossibleRelations, 2)
		require.Len(t, ast.DirectProjections, 1)
		require.Empty(t, ast.DirectProjections[0].Source.Table)
	}
}

func TestPostgresLineageHardLimitsFailClosed(t *testing.T) {
	t.Run("projection slots", func(t *testing.T) {
		columns := make([]string, lineageMaxProjections+1)
		for index := range columns {
			columns[index] = fmt.Sprintf("c%d", index)
		}
		_, err := (&postgresParser{}).Parse("SELECT " + strings.Join(columns, ",") + " FROM customers")
		require.ErrorContains(t, err, "projection slots")
	})

	t.Run("set leaves", func(t *testing.T) {
		leaves := make([]string, lineageMaxSetLeaves+1)
		for index := range leaves {
			leaves[index] = fmt.Sprintf("SELECT %d", index)
		}
		_, err := (&postgresParser{}).Parse(strings.Join(leaves, " UNION ALL "))
		require.ErrorContains(t, err, "set leaves")
	})

	t.Run("nesting depth", func(t *testing.T) {
		expression := "phone"
		for index := 0; index < lineageMaxDepth+1; index++ {
			expression = "(SELECT " + expression + " FROM customers)"
		}
		_, err := (&postgresParser{}).Parse("SELECT " + expression)
		require.ErrorContains(t, err, "nesting depth")
	})

	t.Run("dependencies", func(t *testing.T) {
		builder := &postgresLineageBuilder{dependencyKeys: make(map[string]struct{})}
		dependencies := make([]model.ColumnDependency, lineageMaxDependencies+1)
		for index := range dependencies {
			dependencies[index] = lineageDependency("", "customers", fmt.Sprintf("c%d", index), 0, model.DependencyValue)
		}
		err := builder.accountLineages([]model.ProjectionLineage{{Arms: []model.LineageArm{{Dependencies: dependencies}}}})
		require.ErrorContains(t, err, "lineage dependencies")
	})

	t.Run("AST nodes", func(t *testing.T) {
		nodes := make([]any, lineageMaxASTNodes+1)
		for index := range nodes {
			nodes[index] = map[string]any{"A_Const": map[string]any{"ival": map[string]any{"ival": index}}}
		}
		require.ErrorContains(t, postgresValidateLineageStructure(nodes), "AST nodes")
	})
}

func parsePostgresLineage(t *testing.T, sql string) *model.AST {
	t.Helper()
	ast, err := (&postgresParser{}).Parse(sql)
	require.NoError(t, err)
	require.NotNil(t, ast)
	require.NotEmpty(t, ast.ProjectionLineages)
	for _, lineage := range ast.ProjectionLineages {
		require.NotEmpty(t, lineage.Arms, sql)
		for _, arm := range lineage.Arms {
			require.NotEmpty(t, arm.Kind, sql)
			require.NotEmpty(t, arm.Status, sql)
			for _, dependency := range arm.Dependencies {
				require.Empty(t, dependency.Origin.Relation.Alias, sql)
				require.NotEmpty(t, dependency.Role, sql)
			}
			for _, relation := range arm.PossibleRelations {
				require.Empty(t, relation.Alias, sql)
			}
		}
	}
	return ast
}

func assertPostgresArm(t *testing.T, arm model.LineageArm, kind model.LineageKind, operation string, status model.LineageStatus) {
	t.Helper()
	require.Equal(t, kind, arm.Kind)
	require.Equal(t, operation, arm.Operation)
	require.Equal(t, status, arm.Status)
}

func assertPostgresDependency(
	t *testing.T,
	dependency model.ColumnDependency,
	schema, table, column string,
	role model.DependencyRole,
	route model.LineageRoute,
) {
	t.Helper()
	require.Equal(t, schema, dependency.Origin.Relation.Schema)
	require.Equal(t, table, dependency.Origin.Relation.Table)
	require.Empty(t, dependency.Origin.Relation.Alias)
	require.Equal(t, column, dependency.Origin.Column)
	require.Equal(t, role, dependency.Role)
	require.Equal(t, route, dependency.Origin.Route)
}

func assertPostgresDependencyRoles(t *testing.T, dependencies []model.ColumnDependency, expected map[string]model.DependencyRole) {
	t.Helper()
	for column, role := range expected {
		require.Equal(t, role, dependencyRoleForColumn(model.LineageArm{Dependencies: dependencies}, column), column)
	}
}

func dependencyRoleForColumn(arm model.LineageArm, column string) model.DependencyRole {
	for _, dependency := range arm.Dependencies {
		if dependency.Origin.Column == column {
			return dependency.Role
		}
	}
	return ""
}

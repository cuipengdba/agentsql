package parser

import (
	"fmt"
	"strings"
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
	"vitess.io/vitess/go/vt/sqlparser"
)

func parseMySQLLineage(t *testing.T, query string) *model.AST {
	t.Helper()
	approved, err := NewParser(mysqlDialect)
	require.NoError(t, err)
	ast, err := approved.Parse(query)
	require.NoError(t, err, query)
	require.NotEmpty(t, ast.ProjectionLineages, query)
	for _, lineage := range ast.ProjectionLineages {
		require.NotEmpty(t, lineage.Arms, query)
		for _, arm := range lineage.Arms {
			require.NotEmpty(t, arm.Kind, query)
			require.NotEmpty(t, arm.Status, query)
			for _, dependency := range arm.Dependencies {
				require.Empty(t, dependency.Origin.Relation.Alias, query)
				require.NotEmpty(t, dependency.Role, query)
			}
			for _, relation := range arm.PossibleRelations {
				require.Empty(t, relation.Alias, query)
			}
		}
	}
	return ast
}

func lineageRelation(schema, table string) model.ObjectRef {
	return model.ObjectRef{Schema: schema, Table: table}
}

func lineageDependency(schema, table, column string, route model.LineageRoute, role model.DependencyRole) model.ColumnDependency {
	return model.ColumnDependency{
		Origin: model.ColumnOrigin{
			Relation: lineageRelation(schema, table), Column: column, Route: route,
		},
		Role: role,
	}
}

func TestMySQLLineageDirectScopeAndAliases(t *testing.T) {
	ast := parseMySQLLineage(t, "SELECT phone, crm.customers.email AS contact FROM crm.customers")
	require.Equal(t, []model.ProjectionLineage{
		{
			SelectIndex: 0, OutputName: "phone",
			Arms: []model.LineageArm{{
				Kind: model.LineageDirect, Operation: "column", Status: model.LineageResolved,
				Dependencies:      []model.ColumnDependency{lineageDependency("crm", "customers", "phone", 0, model.DependencyValue)},
				PossibleRelations: []model.ObjectRef{lineageRelation("crm", "customers")},
			}},
		},
		{
			SelectIndex: 1, OutputName: "contact",
			Arms: []model.LineageArm{{
				Kind: model.LineageDirect, Operation: "column", Status: model.LineageResolved,
				Dependencies:      []model.ColumnDependency{lineageDependency("crm", "customers", "email", 0, model.DependencyValue)},
				PossibleRelations: []model.ObjectRef{lineageRelation("crm", "customers")},
			}},
		},
	}, ast.ProjectionLineages)

	ambiguous := parseMySQLLineage(t, "SELECT phone FROM customers c JOIN orders o ON c.id=o.id")
	require.Equal(t, model.LineageDirect, ambiguous.ProjectionLineages[0].Arms[0].Kind)
	require.Equal(t, model.LineageAmbiguous, ambiguous.ProjectionLineages[0].Arms[0].Status)
	require.Equal(t, []model.ObjectRef{{Table: "customers"}, {Table: "orders"}}, ambiguous.ProjectionLineages[0].Arms[0].PossibleRelations)

	selfJoin := parseMySQLLineage(t, "SELECT c1.phone, c2.phone FROM customers c1 JOIN customers c2 ON c1.id=c2.id")
	for _, lineage := range selfJoin.ProjectionLineages {
		require.Equal(t, lineageDependency("", "customers", "phone", 0, model.DependencyValue), lineage.Arms[0].Dependencies[0])
	}

	shadowed := parseMySQLLineage(t, "SELECT x.phone FROM customers x WHERE EXISTS (SELECT 1 FROM orders x WHERE x.id=1)")
	require.Equal(t, "customers", shadowed.ProjectionLineages[0].Arms[0].Dependencies[0].Origin.Relation.Table)
}

func TestMySQLLineageTransparentWhitelistAndCompositeBoundary(t *testing.T) {
	ast := parseMySQLLineage(t, "SELECT (phone) AS p, phone COLLATE utf8mb4_bin AS c, COALESCE(phone,NULL,NULL) AS n, CONCAT('',phone) AS e, UPPER(phone) AS u, TRIM(phone) AS t, CAST(phone AS CHAR) AS ca, SUBSTR(phone,1,2) AS s, phone+1 AS a, CONCAT(phone,'x') AS x, CONCAT(phone,phone) AS r FROM customers")
	expected := []struct {
		kind      model.LineageKind
		operation string
	}{
		{model.LineageDirect, "column"},
		{model.LineageTransparent, "collate"},
		{model.LineageTransparent, "coalesce_null"},
		{model.LineageTransparent, "concat_empty"},
		{model.LineageComposite, "upper"},
		{model.LineageComposite, "trim"},
		{model.LineageComposite, "cast"},
		{model.LineageComposite, "substr"},
		{model.LineageComposite, "binary"},
		{model.LineageComposite, "concat"},
		{model.LineageComposite, "concat"},
	}
	require.Len(t, ast.ProjectionLineages, len(expected))
	for index, expectation := range expected {
		arm := ast.ProjectionLineages[index].Arms[0]
		require.Equal(t, expectation.kind, arm.Kind, "projection %d", index)
		require.Equal(t, expectation.operation, arm.Operation, "projection %d", index)
		require.Equal(t, model.LineageResolved, arm.Status, "projection %d", index)
		require.Equal(t, []model.ColumnDependency{
			lineageDependency("", "customers", "phone", 0, model.DependencyValue),
		}, arm.Dependencies, "projection %d", index)
	}
}

func TestMySQLLineageCompositeAndCaseRoles(t *testing.T) {
	ast := parseMySQLLineage(t, "SELECT c.phone+1 AS one, CONCAT(c.phone,o.email) AS many, CONCAT(c.phone,c.phone) AS repeated, CASE WHEN c.secret=1 THEN c.phone ELSE c.email END AS choice FROM customers c JOIN orders o ON c.id=o.id")
	for index := 0; index < 3; index++ {
		require.Equal(t, model.LineageComposite, ast.ProjectionLineages[index].Arms[0].Kind)
		require.Equal(t, model.LineageResolved, ast.ProjectionLineages[index].Arms[0].Status)
	}
	require.Equal(t, []model.ColumnDependency{
		lineageDependency("", "customers", "phone", 0, model.DependencyValue),
		lineageDependency("", "orders", "email", 0, model.DependencyValue),
	}, ast.ProjectionLineages[1].Arms[0].Dependencies)
	require.Equal(t, []model.ColumnDependency{
		lineageDependency("", "customers", "phone", 0, model.DependencyValue),
	}, ast.ProjectionLineages[2].Arms[0].Dependencies, "dependency de-dup must not make a repeated expression transparent")
	require.Equal(t, []model.ColumnDependency{
		lineageDependency("", "customers", "secret", 0, model.DependencyControl),
		lineageDependency("", "customers", "phone", 0, model.DependencyValue),
		lineageDependency("", "customers", "email", 0, model.DependencyValue),
	}, ast.ProjectionLineages[3].Arms[0].Dependencies)
}

func TestMySQLLineageJSONAndDateOperationsAreResolvedComposite(t *testing.T) {
	ast := parseMySQLLineage(t, "SELECT JSON_EXTRACT(payload,'$.phone') AS j, DATE_ADD(created_at, INTERVAL 1 DAY) AS d FROM customers")
	require.Equal(t, model.LineageComposite, ast.ProjectionLineages[0].Arms[0].Kind)
	require.Equal(t, "json_extract", ast.ProjectionLineages[0].Arms[0].Operation)
	require.Equal(t, model.LineageResolved, ast.ProjectionLineages[0].Arms[0].Status)
	require.Equal(t, []model.ColumnDependency{
		lineageDependency("", "customers", "payload", 0, model.DependencyValue),
	}, ast.ProjectionLineages[0].Arms[0].Dependencies)
	require.Equal(t, model.LineageComposite, ast.ProjectionLineages[1].Arms[0].Kind)
	require.Equal(t, "date_operation", ast.ProjectionLineages[1].Arms[0].Operation)
	require.Equal(t, model.LineageResolved, ast.ProjectionLineages[1].Arms[0].Status)
	require.Equal(t, []model.ColumnDependency{
		lineageDependency("", "customers", "created_at", 0, model.DependencyValue),
	}, ast.ProjectionLineages[1].Arms[0].Dependencies)
}

func TestMySQLLineageAggregateOperationsAndRoles(t *testing.T) {
	sourceFree := parseMySQLLineage(t, "SELECT COUNT(*) AS a, COUNT(NULL) AS b, COUNT(1) AS c FROM customers")
	for index, operation := range []string{"aggregate:count_star", "aggregate:count_constant", "aggregate:count_constant"} {
		arm := sourceFree.ProjectionLineages[index].Arms[0]
		require.Equal(t, model.LineageAggregate, arm.Kind)
		require.Equal(t, operation, arm.Operation)
		require.Equal(t, model.LineageSourceFree, arm.Status)
		require.Empty(t, arm.Dependencies)
		require.Empty(t, arm.PossibleRelations)
	}

	aggregates := parseMySQLLineage(t, "SELECT COUNT(phone), COUNT(DISTINCT phone), MAX(phone), MIN(phone), ANY_VALUE(phone), SUM(phone), AVG(phone), GROUP_CONCAT(phone ORDER BY email), JSON_ARRAYAGG(phone), STDDEV(phone), VARIANCE(phone), BIT_AND(phone), BIT_OR(phone), STRING_AGG(phone), MEDIAN(phone), PERCENTILE(phone), BOOL_AND(phone), BOOL_OR(phone) FROM customers GROUP BY tenant_id")
	expectedOperations := []string{
		"aggregate:count", "aggregate:count", "aggregate:max", "aggregate:min", "aggregate:any_value",
		"aggregate:sum", "aggregate:avg", "aggregate:group_concat", "aggregate:json_arrayagg",
		"aggregate:stddev", "aggregate:variance", "aggregate:bit_and", "aggregate:bit_or",
		"aggregate:string_agg", "aggregate:median", "aggregate:percentile", "aggregate:bool_and", "aggregate:bool_or",
	}
	require.Len(t, aggregates.ProjectionLineages, len(expectedOperations))
	for index, operation := range expectedOperations {
		arm := aggregates.ProjectionLineages[index].Arms[0]
		require.Equal(t, model.LineageAggregate, arm.Kind, operation)
		require.Equal(t, operation, arm.Operation)
		require.Equal(t, model.LineageResolved, arm.Status)
		require.Contains(t, arm.Dependencies, lineageDependency("", "customers", "phone", 0, model.DependencyValue))
		require.Contains(t, arm.Dependencies, lineageDependency("", "customers", "tenant_id", 0, model.DependencyGroup))
	}
	require.Contains(t, aggregates.ProjectionLineages[7].Arms[0].Dependencies,
		lineageDependency("", "customers", "email", 0, model.DependencyOrder))
}

func TestMySQLLineageWindowValuePartitionOrder(t *testing.T) {
	ast := parseMySQLLineage(t, "SELECT ROW_NUMBER() OVER (PARTITION BY tenant_id ORDER BY secret), RANK() OVER (ORDER BY secret), LAG(phone) OVER (PARTITION BY tenant_id ORDER BY id), LEAD(email) OVER (ORDER BY id), FIRST_VALUE(phone) OVER (ORDER BY id), MAX(phone) OVER (ORDER BY id) FROM customers")
	expectedOperations := []string{"window:row_number", "window:rank", "window:lag", "window:lead", "window:first_value", "window:max"}
	for index, operation := range expectedOperations {
		arm := ast.ProjectionLineages[index].Arms[0]
		require.Equal(t, model.LineageWindow, arm.Kind)
		require.Equal(t, operation, arm.Operation)
		require.Equal(t, model.LineageResolved, arm.Status)
	}
	require.Equal(t, []model.ColumnDependency{
		lineageDependency("", "customers", "tenant_id", 0, model.DependencyGroup),
		lineageDependency("", "customers", "secret", 0, model.DependencyOrder),
	}, ast.ProjectionLineages[0].Arms[0].Dependencies)
	require.Contains(t, ast.ProjectionLineages[2].Arms[0].Dependencies,
		lineageDependency("", "customers", "phone", 0, model.DependencyValue))
}

func TestMySQLLineageCTEDerivedAndLateralRoutes(t *testing.T) {
	cte := parseMySQLLineage(t, "WITH first(mobile) AS (SELECT phone FROM customers), second AS (SELECT mobile FROM first), unused AS (SELECT id FROM orders) SELECT second.mobile FROM second")
	require.Equal(t, []model.ColumnDependency{
		lineageDependency("", "customers", "phone", model.RouteCTE, model.DependencyValue),
	}, cte.ProjectionLineages[0].Arms[0].Dependencies)
	require.Equal(t, []model.ObjectRef{{Table: "customers"}}, cte.ProjectionLineages[0].Arms[0].PossibleRelations)
	require.NotContains(t, cte.ProjectionLineages[0].Arms[0].PossibleRelations, model.ObjectRef{Table: "orders"})

	derived := parseMySQLLineage(t, "SELECT d.mobile FROM (SELECT phone AS mobile FROM customers) d")
	require.Equal(t, model.RouteDerived, derived.ProjectionLineages[0].Arms[0].Dependencies[0].Origin.Route)

	lateral := parseMySQLLineage(t, "SELECT d.mobile FROM customers c JOIN LATERAL (SELECT c.phone AS mobile) d ON 1=1")
	require.Equal(t, model.RouteDerived|model.RouteLateral, lateral.ProjectionLineages[0].Arms[0].Dependencies[0].Origin.Route)

	lateralPredicate := parseMySQLLineage(t, "SELECT d.mobile FROM customers c JOIN LATERAL (SELECT c.phone AS mobile WHERE c.id>0) d ON 1=1")
	require.Equal(t, model.LineageComposite, lateralPredicate.ProjectionLineages[0].Arms[0].Kind)
	require.Equal(t, "lateral_subquery", lateralPredicate.ProjectionLineages[0].Arms[0].Operation)
	require.Contains(t, lateralPredicate.ProjectionLineages[0].Arms[0].Dependencies,
		lineageDependency("", "customers", "id", model.RouteDerived|model.RouteLateral, model.DependencyFilter))

	nonLateral := parseMySQLLineage(t, "SELECT d.mobile FROM customers c JOIN (SELECT c.phone AS mobile) d ON 1=1")
	require.NotEqual(t, model.LineageResolved, nonLateral.ProjectionLineages[0].Arms[0].Status)

	duplicate := parseMySQLLineage(t, "WITH x AS (SELECT phone AS v, email AS v FROM customers) SELECT x.v FROM x")
	require.Equal(t, model.LineageAmbiguous, duplicate.ProjectionLineages[0].Arms[0].Status)

	mismatch := parseMySQLLineage(t, "WITH x(a,b) AS (SELECT phone FROM customers) SELECT x.a FROM x")
	require.Equal(t, model.LineageOpaqueState, mismatch.ProjectionLineages[0].Arms[0].Status)

	recursive := parseMySQLLineage(t, "WITH RECURSIVE x(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM x WHERE n<2) SELECT x.n FROM x")
	require.NotEqual(t, model.LineageResolved, recursive.ProjectionLineages[0].Arms[0].Status)
	require.Equal(t, model.LineageDirect, recursive.ProjectionLineages[0].Arms[0].Kind)
}

func TestMySQLLineageScalarSubquery(t *testing.T) {
	ast := parseMySQLLineage(t, "SELECT (SELECT o.phone FROM orders o) AS plain, (SELECT o.phone FROM orders o WHERE o.customer_id=c.id LIMIT 1) AS filtered FROM customers c")
	plain := ast.ProjectionLineages[0].Arms[0]
	require.Equal(t, model.LineageDirect, plain.Kind)
	require.Equal(t, model.LineageResolved, plain.Status)
	require.Equal(t, model.RouteScalarSubquery, plain.Dependencies[0].Origin.Route)

	filtered := ast.ProjectionLineages[1].Arms[0]
	require.Equal(t, model.LineageComposite, filtered.Kind)
	require.Equal(t, "scalar_subquery", filtered.Operation)
	require.Equal(t, model.LineageResolved, filtered.Status)
	require.Contains(t, filtered.Dependencies,
		lineageDependency("", "customers", "id", model.RouteScalarSubquery, model.DependencyFilter))
	require.Contains(t, filtered.Dependencies,
		lineageDependency("", "orders", "customer_id", model.RouteScalarSubquery, model.DependencyFilter))
	require.Contains(t, filtered.Dependencies,
		lineageDependency("", "orders", "phone", model.RouteScalarSubquery, model.DependencyValue))
}

func TestMySQLLineageUnionArmsAndSetOperations(t *testing.T) {
	all := parseMySQLLineage(t, "SELECT phone FROM customers UNION ALL SELECT phone FROM archive")
	require.Equal(t, "union_all", all.ProjectionLineages[0].SetOp)
	require.Len(t, all.ProjectionLineages[0].Arms, 2)
	require.Equal(t, "customers", all.ProjectionLineages[0].Arms[0].Dependencies[0].Origin.Relation.Table)
	require.Equal(t, "archive", all.ProjectionLineages[0].Arms[1].Dependencies[0].Origin.Relation.Table)
	require.Nil(t, all.DirectProjections)

	distinct := parseMySQLLineage(t, "SELECT phone FROM customers UNION SELECT phone FROM archive")
	require.Equal(t, "union_distinct", distinct.ProjectionLineages[0].SetOp)

	mixed := parseMySQLLineage(t, "SELECT phone FROM customers UNION ALL SELECT phone FROM archive UNION SELECT phone FROM old_customers")
	require.Equal(t, "mixed", mixed.ProjectionLineages[0].SetOp)
	require.Len(t, mixed.ProjectionLineages[0].Arms, 3)

	star := parseMySQLLineage(t, "SELECT * FROM customers UNION ALL SELECT * FROM archive")
	require.True(t, star.ProjectionLineages[0].Variadic)
	require.Equal(t, model.LineageWildcard, star.ProjectionLineages[0].Arms[0].Kind)
	require.Equal(t, model.LineageOpaque, star.ProjectionLineages[0].Arms[1].Kind)
	require.Equal(t, "set_branch_wildcard", star.ProjectionLineages[0].Arms[1].Operation)

	approved, err := NewParser(mysqlDialect)
	require.NoError(t, err)
	_, err = approved.Parse("SELECT phone FROM customers UNION ALL SELECT phone, email FROM archive")
	require.ErrorContains(t, err, "set projection count mismatch")
}

func TestMySQLLineageWildcardJSONTableNaturalAndUsing(t *testing.T) {
	single := parseMySQLLineage(t, "SELECT *, c.* FROM customers c")
	for _, lineage := range single.ProjectionLineages {
		require.True(t, lineage.Variadic)
		require.Equal(t, model.LineageWildcard, lineage.Arms[0].Kind)
		require.Equal(t, []model.ObjectRef{{Table: "customers"}}, lineage.Arms[0].PossibleRelations)
	}

	joined := parseMySQLLineage(t, "SELECT *, c.*, c.phone, o.* FROM customers c JOIN orders o USING(id)")
	require.Equal(t, model.LineageOpaque, joined.ProjectionLineages[0].Arms[0].Kind)
	require.Equal(t, model.LineageAmbiguous, joined.ProjectionLineages[0].Arms[0].Status)
	require.Equal(t, model.LineageWildcard, joined.ProjectionLineages[1].Arms[0].Kind)
	require.Equal(t, model.LineageDirect, joined.ProjectionLineages[2].Arms[0].Kind)
	require.Equal(t, model.LineageWildcard, joined.ProjectionLineages[3].Arms[0].Kind)

	for _, query := range []string{
		"SELECT id FROM customers c JOIN orders o USING(id)",
		"SELECT id FROM customers c NATURAL JOIN orders o",
	} {
		ast := parseMySQLLineage(t, query)
		require.Equal(t, model.LineageAmbiguous, ast.ProjectionLineages[0].Arms[0].Status)
	}

	jsonTable := parseMySQLLineage(t, "SELECT jt.n FROM customers c, JSON_TABLE(c.payload, '$[*]' COLUMNS(n INT PATH '$')) jt")
	arm := jsonTable.ProjectionLineages[0].Arms[0]
	require.Equal(t, model.LineageDirect, arm.Kind)
	require.Equal(t, model.LineageOpaqueState, arm.Status)
	require.Equal(t, "json_table", arm.Operation)
	require.Contains(t, arm.Dependencies,
		lineageDependency("", "customers", "payload", 0, model.DependencyValue))
	require.Equal(t, []model.ObjectRef{{Table: "customers"}}, arm.PossibleRelations)
}

func TestMySQLLineageUnknownExpressionIsUnsupportedAndRetainsDependencies(t *testing.T) {
	scope := newLineageScope(nil)
	scope.addBinding(relationBinding{
		visibleName: "customers", kind: bindingPhysical, object: model.ObjectRef{Table: "customers"},
	})
	builder := &mysqlLineageBuilder{}
	arm := builder.mysqlExprLineage(&sqlparser.Offset{
		V: 1, Original: &sqlparser.ColName{Name: sqlparser.NewIdentifierCI("phone")},
	}, scope, nil)
	require.Equal(t, model.LineageOpaque, arm.Kind)
	require.Equal(t, model.LineageUnsupported, arm.Status)
	require.Contains(t, arm.Operation, "unsupported:")
	require.Equal(t, []model.ColumnDependency{
		lineageDependency("", "customers", "phone", 0, model.DependencyValue),
	}, arm.Dependencies)
}

func TestMySQLLineageHardLimitsFailBeforeExecution(t *testing.T) {
	approved, err := NewParser(mysqlDialect)
	require.NoError(t, err)

	t.Run("projection slots", func(t *testing.T) {
		columns := make([]string, lineageMaxProjections+1)
		for index := range columns {
			columns[index] = fmt.Sprintf("c%d", index)
		}
		_, parseErr := approved.Parse("SELECT " + strings.Join(columns, ",") + " FROM customers")
		require.ErrorContains(t, parseErr, "projection slots")
	})

	t.Run("set leaves", func(t *testing.T) {
		leaves := make([]string, lineageMaxSetLeaves+1)
		for index := range leaves {
			leaves[index] = fmt.Sprintf("SELECT %d", index)
		}
		_, parseErr := approved.Parse(strings.Join(leaves, " UNION ALL "))
		require.ErrorContains(t, parseErr, "set leaves")
	})

	t.Run("nesting depth", func(t *testing.T) {
		expression := "phone"
		for index := 0; index < lineageMaxDepth+1; index++ {
			expression = "(SELECT " + expression + " FROM customers)"
		}
		_, parseErr := approved.Parse("SELECT " + expression)
		require.ErrorContains(t, parseErr, "nesting depth")
	})

	t.Run("dependencies", func(t *testing.T) {
		builder := &mysqlLineageBuilder{}
		dependencies := make([]model.ColumnDependency, lineageMaxDependencies+1)
		for index := range dependencies {
			dependencies[index] = lineageDependency("", "customers", fmt.Sprintf("c%d", index), 0, model.DependencyValue)
		}
		err := builder.accountLineages([]model.ProjectionLineage{{Arms: []model.LineageArm{{Dependencies: dependencies}}}})
		require.ErrorContains(t, err, "lineage dependencies")
	})

	t.Run("AST nodes", func(t *testing.T) {
		expressions := make([]sqlparser.SelectExpr, lineageMaxASTNodes+1)
		for index := range expressions {
			expressions[index] = &sqlparser.AliasedExpr{
				Expr: &sqlparser.Literal{Type: sqlparser.IntVal, Val: "1"},
			}
		}
		statement := &sqlparser.Select{SelectExprs: &sqlparser.SelectExprs{Exprs: expressions}}
		require.ErrorContains(t, mysqlValidateLineageStructure(statement), "AST nodes")
	})
}

func TestMySQLValuesIntersectAndExceptRemainRejected(t *testing.T) {
	approved, err := NewParser(mysqlDialect)
	require.NoError(t, err)
	for _, query := range []string{
		"VALUES ROW(1)",
		"SELECT 1 UNION VALUES ROW(2)",
		"SELECT phone FROM customers INTERSECT SELECT phone FROM archive",
		"SELECT phone FROM customers EXCEPT SELECT phone FROM archive",
	} {
		ast, parseErr := approved.Parse(query)
		require.Error(t, parseErr, query)
		require.Empty(t, ast.ProjectionLineages, query)
	}
}

package policy

import (
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestResolverMergesPoliciesWithDenyPriority(t *testing.T) {
	columnsOne := "id, total, id"
	columnsTwo := "status,total"
	policies := []model.Policy{
		{ObjectType: "table", ObjectName: "public.orders", Action: "allow"},
		{ObjectType: "schema", ObjectName: "public.*", Action: "allow"},
		{ObjectType: "database", ObjectName: "*", Action: "allow"},
		{ObjectType: "table", ObjectName: "public.orders", Action: "allow"},
		{ObjectType: "table", ObjectName: "public.secrets", Action: "allow"},
		{ObjectType: "table", ObjectName: "public.secrets", Action: "deny"},
		{ObjectType: "table", ObjectName: "public.secrets", Action: "allow"},
		{ObjectType: "column", ObjectName: "public.orders", Columns: &columnsOne, Action: "allow"},
		{ObjectType: "column", ObjectName: "public.orders", Columns: &columnsTwo, Action: "allow"},
	}

	decision, err := NewResolver().Resolve(policies, "dml")

	require.NoError(t, err)
	require.Equal(t, "dml", decision.Level)
	require.Equal(t, []string{"public.orders", "public.*", "*"}, decision.AllowedTables)
	require.Equal(t, []string{"public.secrets"}, decision.DeniedTables)
	require.Equal(t, []string{"id", "total", "status"}, decision.ColumnACL["public.orders"])
}

func TestResolverReturnsNonNilDefaultDenyDecision(t *testing.T) {
	decision, err := NewResolver().Resolve(nil, "readonly")

	require.NoError(t, err)
	require.NotNil(t, decision)
	require.NotNil(t, decision.AllowedTables)
	require.NotNil(t, decision.DeniedTables)
	require.NotNil(t, decision.ColumnACL)
	require.Empty(t, decision.AllowedTables)
	require.Empty(t, decision.DeniedTables)
	require.Empty(t, decision.ColumnACL)
	require.Equal(t, "readonly", decision.Level)
	require.False(t, MatchesAnyTable(decision.AllowedTables, model.ObjectRef{Table: "orders"}))
}

func TestResolverPassesThroughValidLevels(t *testing.T) {
	for _, level := range []string{"readonly", "dml", "ddl"} {
		t.Run(level, func(t *testing.T) {
			decision, err := NewResolver().Resolve([]model.Policy{}, level)
			require.NoError(t, err)
			require.Equal(t, level, decision.Level)
		})
	}
}

func TestResolverRejectsInvalidInputs(t *testing.T) {
	columns := "id"
	tests := []struct {
		name     string
		resolver *Resolver
		level    string
		policies []model.Policy
	}{
		{name: "nil resolver", resolver: nil, level: "readonly"},
		{name: "empty level", resolver: NewResolver()},
		{name: "uppercase level", resolver: NewResolver(), level: "READONLY"},
		{name: "spaced level", resolver: NewResolver(), level: " readonly "},
		{name: "invalid object type", resolver: NewResolver(), level: "readonly", policies: []model.Policy{{ObjectType: "view", ObjectName: "public.orders", Action: "allow"}}},
		{name: "invalid action", resolver: NewResolver(), level: "readonly", policies: []model.Policy{{ObjectType: "table", ObjectName: "public.orders", Action: "permit"}}},
		{name: "invalid pattern", resolver: NewResolver(), level: "readonly", policies: []model.Policy{{ObjectType: "table", ObjectName: "db.public.orders", Action: "allow"}}},
		{name: "column without columns", resolver: NewResolver(), level: "readonly", policies: []model.Policy{{ObjectType: "column", ObjectName: "public.orders", Action: "allow"}}},
		{name: "column with empty identifier", resolver: NewResolver(), level: "readonly", policies: []model.Policy{{ObjectType: "column", ObjectName: "public.orders", Columns: stringPointer("id,,total"), Action: "allow"}}},
		{name: "column wildcard object", resolver: NewResolver(), level: "readonly", policies: []model.Policy{{ObjectType: "column", ObjectName: "public.*", Columns: &columns, Action: "allow"}}},
		{name: "column deny unsupported", resolver: NewResolver(), level: "readonly", policies: []model.Policy{{ObjectType: "column", ObjectName: "public.orders", Columns: &columns, Action: "deny"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := test.resolver.Resolve(test.policies, test.level)
			require.Error(t, err)
		})
	}
}

func TestSharedTableMatcher(t *testing.T) {
	table := model.ObjectRef{Schema: "public", Table: "orders", Alias: "o"}
	require.True(t, MatchesAnyTable([]string{"public.orders"}, table))
	require.True(t, MatchesAnyTable([]string{"public.*"}, table))
	require.True(t, MatchesAnyTable([]string{"*"}, table))
	require.False(t, MatchesAnyTable([]string{"orders"}, table))
	require.False(t, MatchesAnyTable([]string{"Public.*"}, table))
	require.False(t, MatchesAnyTable(nil, table))
	require.True(t, HasBroadTableGrant([]string{"public.*"}, table))
	require.False(t, HasBroadTableGrant([]string{"public.orders"}, table))
}

func TestValidateTablePatterns(t *testing.T) {
	require.NoError(t, ValidateTablePatterns(nil))
	require.NoError(t, ValidateTablePatterns([]string{"*", "public.*", "public.orders", "orders"}))
	for _, patterns := range [][]string{
		{""},
		{" public.orders"},
		{"db.public.orders"},
		{".orders"},
		{"public."},
		{"*.orders"},
	} {
		require.Error(t, ValidateTablePatterns(patterns))
	}
}

func TestAuthorizeColumns(t *testing.T) {
	baseAST := &model.AST{
		StmtType: model.StmtType("SELECT"),
		Tables:   []model.ObjectRef{{Schema: "public", Table: "orders", Alias: "o"}},
		Columns:  []string{"id", "total"},
	}
	tests := []struct {
		name         string
		ast          *model.AST
		decision     *model.PolicyDecision
		unauthorized []string
	}{
		{name: "allowed columns", ast: baseAST, decision: decisionWithColumns([]string{"public.orders"}, "public.orders", []string{"id", "total"})},
		{name: "unauthorized column", ast: clonePolicyAST(baseAST, "id", "secret"), decision: decisionWithColumns([]string{"public.orders"}, "public.orders", []string{"id"}), unauthorized: []string{"public.orders.secret"}},
		{name: "column wildcard", ast: clonePolicyAST(baseAST, "id", "secret"), decision: decisionWithColumns([]string{"public.orders"}, "public.orders", []string{"*"})},
		{name: "global table wildcard", ast: clonePolicyAST(baseAST, "id", "secret"), decision: decisionWithColumns([]string{"*"}, "public.orders", []string{"id"})},
		{name: "schema table wildcard", ast: clonePolicyAST(baseAST, "id", "secret"), decision: decisionWithColumns([]string{"public.*"}, "public.orders", []string{"id"})},
		{name: "no column policy adds no restriction", ast: clonePolicyAST(baseAST, "id", "secret"), decision: &model.PolicyDecision{AllowedTables: []string{"public.orders"}, DeniedTables: []string{}, ColumnACL: map[string][]string{}}},
		{name: "non select ignored", ast: &model.AST{StmtType: "UPDATE", Tables: baseAST.Tables, Columns: []string{"secret"}}, decision: decisionWithColumns([]string{"public.orders"}, "public.orders", []string{"id"})},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			unauthorized, err := AuthorizeColumns(test.ast, test.decision)
			require.NoError(t, err)
			require.NotNil(t, unauthorized)
			expected := test.unauthorized
			if expected == nil {
				expected = []string{}
			}
			require.Equal(t, expected, unauthorized)
		})
	}

	_, err := AuthorizeColumns(nil, &model.PolicyDecision{})
	require.Error(t, err)
	_, err = AuthorizeColumns(baseAST, nil)
	require.Error(t, err)
}

func TestFilterColumnsForSchemaOnlyRemoves(t *testing.T) {
	input := []SchemaColumn{
		{Schema: "public", Table: "orders", Column: "id"},
		{Schema: "public", Table: "orders", Column: "secret"},
		{Schema: "public", Table: "customers", Column: "email"},
		{Schema: "public", Table: "secrets", Column: "value"},
		{Schema: "audit", Table: "events", Column: "id"},
	}
	decision := &model.PolicyDecision{
		AllowedTables: []string{"public.orders", "public.customers", "public.secrets"},
		DeniedTables:  []string{"public.secrets"},
		ColumnACL: map[string][]string{
			"public.orders": {"id"},
		},
	}

	filtered, err := FilterColumnsForSchema(input, decision)

	require.NoError(t, err)
	require.Equal(t, []SchemaColumn{
		{Schema: "public", Table: "orders", Column: "id"},
		{Schema: "public", Table: "customers", Column: "email"},
	}, filtered)
	require.LessOrEqual(t, len(filtered), len(input))

	empty, err := FilterColumnsForSchema(nil, decision)
	require.NoError(t, err)
	require.NotNil(t, empty)
	require.Empty(t, empty)

	_, err = FilterColumnsForSchema(input, nil)
	require.Error(t, err)
}

func TestFilterColumnsForSchemaBroadGrant(t *testing.T) {
	input := []SchemaColumn{
		{Schema: "public", Table: "orders", Column: "id"},
		{Schema: "public", Table: "orders", Column: "secret"},
	}
	decision := &model.PolicyDecision{
		AllowedTables: []string{"public.*"},
		DeniedTables:  []string{},
		ColumnACL:     map[string][]string{"public.orders": {"id"}},
	}

	filtered, err := FilterColumnsForSchema(input, decision)

	require.NoError(t, err)
	require.Equal(t, input, filtered)
}

func decisionWithColumns(
	allowedTables []string,
	object string,
	columns []string,
) *model.PolicyDecision {
	return &model.PolicyDecision{
		AllowedTables: allowedTables,
		DeniedTables:  []string{},
		ColumnACL:     map[string][]string{object: columns},
	}
}

func clonePolicyAST(source *model.AST, columns ...string) *model.AST {
	cloned := *source
	cloned.Tables = append([]model.ObjectRef{}, source.Tables...)
	cloned.Columns = append([]string{}, columns...)
	return &cloned
}

func stringPointer(value string) *string {
	return &value
}

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

func TestResolverUsesOnlyHealthyFormalOutputNamesForCoarseColumnGate(t *testing.T) {
	bindingID := "bound"
	policy := model.Policy{ID: "p", ObjectType: "column", ObjectName: "app.customers", Action: "allow",
		RelationBindingID: &bindingID, RelationBinding: &model.RelationPolicyBinding{ID: bindingID, Status: "healthy"},
		ColumnPermissions: []model.PolicyColumnPermission{
			{RelationEnrollmentID: bindingID, ColumnName: "id", Usage: "output"},
			{RelationEnrollmentID: bindingID, ColumnName: "phone", Usage: "output"},
			{RelationEnrollmentID: bindingID, ColumnName: "secret", Usage: "reference"},
		}}
	decision, err := NewResolver().Resolve([]model.Policy{{ObjectType: "table", ObjectName: "app.customers", Action: "allow"}, policy}, "readonly")
	require.NoError(t, err)
	require.Equal(t, []string{"app.customers"}, decision.AllowedTables)
	require.Equal(t, []string{"id", "phone"}, decision.ColumnACL["app.customers"])
	policy.RelationBinding.Status = "staging"
	_, err = NewResolver().Resolve([]model.Policy{policy}, "readonly")
	require.Error(t, err)
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
		{name: "table row filter cannot be silently ignored", resolver: NewResolver(), level: "readonly", policies: []model.Policy{{ObjectType: "table", ObjectName: "public.orders", RowFilter: stringPointer("region = 'west'"), Action: "allow"}}},
		{name: "column row filter cannot be silently ignored", resolver: NewResolver(), level: "readonly", policies: []model.Policy{{ObjectType: "column", ObjectName: "public.orders", Columns: &columns, RowFilter: stringPointer(""), Action: "allow"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := test.resolver.Resolve(test.policies, test.level)
			require.Error(t, err)
		})
	}
}

func TestResolverForBoundSelectAdmitsOnlyExactSafeRowRules(t *testing.T) {
	filter := "id = 1"
	base := model.Policy{ObjectType: "table", ObjectName: "public.orders", RowFilter: &filter, Action: "allow"}
	_, err := NewResolver().Resolve([]model.Policy{base}, "readonly")
	require.Error(t, err, "the ordinary resolver must remain fail-closed")
	decision, err := NewResolver().ResolveWithPostgresRowPolicies([]model.Policy{base}, "readonly")
	require.NoError(t, err)
	require.Equal(t, []string{"public.orders"}, decision.AllowedTables)
	for _, mutate := range []func(*model.Policy){
		func(p *model.Policy) { p.ObjectName = "public.*" },
		func(p *model.Policy) { p.Action = "deny" },
		func(p *model.Policy) { p.ObjectType = "column" },
		func(p *model.Policy) { p.RelationBindingID = &filter },
		func(p *model.Policy) { p.LegacyUnrepresentable = true },
		func(p *model.Policy) { bad := "id = 1 OR id = 2"; p.RowFilter = &bad },
	} {
		bad := base
		mutate(&bad)
		_, err := NewResolver().ResolveWithPostgresRowPolicies([]model.Policy{bad}, "readonly")
		require.Error(t, err)
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

func stringPointer(value string) *string {
	return &value
}

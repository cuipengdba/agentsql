package bootstrap

import (
	"testing"

	executor "github.com/cuipengdba/agentsql/internal/authorizedexecute"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestPostgresColumnEnrollmentRejectsUnprovenIdentity(t *testing.T) {
	columns := "id,phone"
	policy := model.Policy{ID: "p", DatasourceID: "d", ObjectName: "public.customers", Columns: &columns}
	relation := executor.PostgresPolicyRelation{DatabaseOID: 1, RelationOID: 2, Schema: "public", Name: "customers", Kind: 'r', CatalogFingerprint: "catalog-v1"}
	base := executor.PostgresPolicyEnrollment{Mode: "CATALOG_CLOSED_V1", StatementClass: "SELECT",
		Relations: []executor.PostgresPolicyRelation{relation}, ColumnUses: []executor.PostgresPolicyColumnUse{
			{RelationOID: 2, Attnum: 1, Name: "id", TypeOID: 23, TypeModifier: -1, Usage: "output"},
			{RelationOID: 2, Attnum: 2, Name: "phone", TypeOID: 25, TypeModifier: -1, CollationOID: 100, Usage: "output"},
		}}
	requested := map[string]struct{}{"id": {}, "phone": {}}
	bound, err := postgresColumnEnrollment(policy, requested, base, "binding-1")
	require.NoError(t, err)
	require.Equal(t, "pg:1:2", *bound.Binding.StableObjectID)
	require.Len(t, bound.Permissions, 4)
	require.Equal(t, "pg:type=25;typmod=-1;collation=100", bound.Permissions[2].ColumnTypeDigest)
	native := base
	native.Mode = "NATIVE_C_V1"
	nativeBound, err := postgresColumnEnrollment(policy, requested, native, "binding-2")
	require.NoError(t, err)
	require.Equal(t, "pg:1:2", *nativeBound.Binding.StableObjectID)
	require.Len(t, nativeBound.Permissions, 4)

	for name, mutate := range map[string]func(*executor.PostgresPolicyEnrollment){
		"unknown relation": func(v *executor.PostgresPolicyEnrollment) { v.Relations[0].Name = "internal_notes" },
		"recreated object": func(v *executor.PostgresPolicyEnrollment) { v.ColumnUses[0].RelationOID = 3 },
		"extra reference": func(v *executor.PostgresPolicyEnrollment) {
			v.ColumnUses = append(v.ColumnUses, executor.PostgresPolicyColumnUse{RelationOID: 2, Attnum: 3, Name: "secret", TypeOID: 25, Usage: "reference"})
		},
		"missing column": func(v *executor.PostgresPolicyEnrollment) { v.ColumnUses = v.ColumnUses[:1] },
		"reference only": func(v *executor.PostgresPolicyEnrollment) { v.ColumnUses[1].Usage = "reference" },
		"native mode":    func(v *executor.PostgresPolicyEnrollment) { v.Mode = "NATIVE_V1" },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := base
			candidate.Relations = append([]executor.PostgresPolicyRelation(nil), base.Relations...)
			candidate.ColumnUses = append([]executor.PostgresPolicyColumnUse(nil), base.ColumnUses...)
			mutate(&candidate)
			_, err := postgresColumnEnrollment(policy, requested, candidate, "binding-1")
			require.Error(t, err)
		})
	}
}

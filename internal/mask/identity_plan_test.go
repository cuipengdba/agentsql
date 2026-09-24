package mask

import (
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestIdentityOnlyMeetAndSingleFinalMask(t *testing.T) {
	redactor, err := NewRedactor([]Rule{
		{Column: "phone", SensitiveType: TypePhone, Algorithm: AlgoMask},
		{Schema: "public", Table: "customers", Column: "phone", SensitiveType: TypePhone, Algorithm: AlgoMask},
	})
	require.NoError(t, err)
	typed := redactor.(IdentityOnlyRedactor)
	plans, err := typed.BuildIdentityPlan([]IdentityRequest{{OutputIndex: 0, ResultName: "contact", Sources: []PhysicalColumn{{Schema: "public", Table: "customers", Column: "phone", InputType: "text"}}}})
	require.NoError(t, err)
	require.Len(t, plans, 1)

	result, report, err := typed.ApplyIdentityPlan(model.QueryResult{Columns: []string{"contact"}, Rows: [][]string{{"13812345678"}}}, plans)
	require.NoError(t, err)
	require.Equal(t, "138****5678", result.Rows[0][0])
	require.Equal(t, 1, report.MaskedCells, "the final output position is transformed exactly once")
}

func TestIdentityOnlyMeetDropsWholePlanOnMismatch(t *testing.T) {
	redactor, err := NewRedactor([]Rule{
		{Column: "phone", SensitiveType: TypePhone, Algorithm: AlgoMask},
		{Schema: "public", Table: "customers", Column: "phone", SensitiveType: TypeGeneric, Algorithm: AlgoBlock},
	})
	require.NoError(t, err)
	typed := redactor.(IdentityOnlyRedactor)
	plans, err := typed.BuildIdentityPlan([]IdentityRequest{{OutputIndex: 0, Sources: []PhysicalColumn{{Schema: "public", Table: "customers", Column: "phone", InputType: "text"}}}})
	require.ErrorIs(t, err, ErrIdentityMeetUndefined)
	require.Empty(t, plans)

	plans, err = typed.BuildIdentityPlan([]IdentityRequest{{OutputIndex: 0, Sources: []PhysicalColumn{
		{Schema: "public", Table: "customers", Column: "phone", InputType: "text"},
		{Schema: "public", Table: "customers", Column: "phone", InputType: "int4"},
	}}})
	require.ErrorIs(t, err, ErrIdentityMeetUndefined)
	require.Empty(t, plans)
}

func TestIdentityPlanRejectsTamperingWithoutPartialResult(t *testing.T) {
	redactor, err := NewRedactor([]Rule{{Column: "phone", SensitiveType: TypePhone, Algorithm: AlgoMask}})
	require.NoError(t, err)
	typed := redactor.(IdentityOnlyRedactor)
	plans, err := typed.BuildIdentityPlan([]IdentityRequest{{OutputIndex: 0, ResultName: "phone", Sources: []PhysicalColumn{{Column: "phone", InputType: "text"}}}})
	require.NoError(t, err)
	require.Len(t, plans, 1)
	plans[0].Identity.SemanticVersion = "tampered"

	result, report, err := typed.ApplyIdentityPlan(model.QueryResult{Columns: []string{"phone"}, Rows: [][]string{{"13812345678"}}}, plans)
	require.ErrorIs(t, err, ErrIdentityPlanInvalid)
	require.Empty(t, result.Rows)
	require.Zero(t, report.MaskedCells)
}

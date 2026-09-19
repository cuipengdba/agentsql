package bootstrap

import (
	"context"
	"testing"

	"github.com/cuipengdba/agentsql/internal/mask"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestT46S4RedactorBuilderPreservesPhysicalScope(t *testing.T) {
	builder := &redactorBuilder{repository: staticMaskRuleReader{rules: []model.MaskRule{{
		ID: "public-customers-phone", SchemaName: "public", TableName: "customers", ColumnName: "phone",
		SensitiveType: string(mask.TypePhone), Algo: string(mask.AlgoMask), Enabled: true,
	}}}}
	redactor, err := builder.RedactorFor(context.Background(), "ds-1")
	require.NoError(t, err)
	relationAware, ok := redactor.(mask.RelationSourceAwareRedactor)
	require.True(t, ok)

	result, report := relationAware.ApplyWithColumnSources(
		model.QueryResult{Columns: []string{"phone"}, Rows: [][]string{{"13812345678"}}, RowCount: 1},
		[]mask.ColumnSource{{Column: "phone", Source: model.ObjectRef{Schema: "public", Table: "customers"}}},
		[]model.ObjectRef{{Schema: "public", Table: "customers"}},
	)
	require.Equal(t, "138****5678", result.Rows[0][0])
	require.Equal(t, map[int]mask.SensitiveType{0: mask.TypePhone}, report.TouchedColumns)
}

func TestT46S4RedactorBuilderUsesFullPhysicalDuplicateKey(t *testing.T) {
	t.Run("same column on different tables coexists", func(t *testing.T) {
		builder := &redactorBuilder{repository: staticMaskRuleReader{rules: []model.MaskRule{
			{ID: "customers-phone", TableName: "customers", ColumnName: "phone", SensitiveType: string(mask.TypePhone), Algo: string(mask.AlgoMask), Enabled: true},
			{ID: "orders-phone", TableName: "orders", ColumnName: "phone", SensitiveType: string(mask.TypeGeneric), Algo: string(mask.AlgoBlock), Enabled: true},
		}}}
		_, err := builder.RedactorFor(context.Background(), "ds-1")
		require.NoError(t, err)
	})

	t.Run("same normalized physical key is rejected", func(t *testing.T) {
		builder := &redactorBuilder{repository: staticMaskRuleReader{rules: []model.MaskRule{
			{ID: "first", SchemaName: "public", TableName: "customers", ColumnName: "phone", SensitiveType: string(mask.TypePhone), Algo: string(mask.AlgoMask), Enabled: true},
			{ID: "second", SchemaName: "public", TableName: "customers", ColumnName: " PHONE ", SensitiveType: string(mask.TypeGeneric), Algo: string(mask.AlgoBlock), Enabled: true},
		}}}
		_, err := builder.RedactorFor(context.Background(), "ds-1")
		require.ErrorIs(t, err, mask.ErrDuplicateMaskColumn)
	})
}

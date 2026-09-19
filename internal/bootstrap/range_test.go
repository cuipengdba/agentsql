package bootstrap

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/cuipengdba/agentsql/internal/config"
	"github.com/cuipengdba/agentsql/internal/mask"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestRedactorBuilderCompilesStoredRangeRulesWithoutAliasing(t *testing.T) {
	width, offset := int64(10), int64(3)
	granularity := string(mask.RangeQuarter)
	number := model.MaskRule{
		ID: "number", ColumnName: "amount", SensitiveType: string(mask.TypeNumber),
		Algo: string(mask.AlgoRange), Enabled: true,
		RangeBucketWidth: &width, RangeBucketOffset: &offset,
	}
	date := model.MaskRule{
		ID: "date", ColumnName: "created_at", SensitiveType: string(mask.TypeDate),
		Algo: string(mask.AlgoRange), Enabled: true, RangeGranularity: &granularity,
	}

	numberParams := rangeParamsFromStored(number)
	dateParams := rangeParamsFromStored(date)
	require.NotNil(t, numberParams)
	require.Equal(t, int64(10), *numberParams.BucketWidth)
	require.Equal(t, int64(3), *numberParams.BucketOffset)
	require.Nil(t, numberParams.Granularity)
	require.NotNil(t, dateParams)
	require.Nil(t, dateParams.BucketWidth)
	require.Nil(t, dateParams.BucketOffset)
	require.Equal(t, mask.RangeQuarter, *dateParams.Granularity)
	require.NotSame(t, number.RangeBucketWidth, numberParams.BucketWidth)
	require.NotSame(t, number.RangeBucketOffset, numberParams.BucketOffset)
	require.NotSame(t, date.RangeGranularity, dateParams.Granularity)

	builder := &redactorBuilder{}
	redactor, err := builder.compile([]model.MaskRule{number, date}, "")
	require.NoError(t, err)
	width, offset, granularity = 99, 99, string(mask.RangeMonth)
	require.Equal(t, int64(10), *numberParams.BucketWidth)
	require.Equal(t, int64(3), *numberParams.BucketOffset)
	require.Equal(t, mask.RangeQuarter, *dateParams.Granularity)

	result, report := redactor.Apply(model.QueryResult{
		Columns: []string{"amount", "created_at"},
		Rows:    [][]string{{"27", "2020-05-17"}},
	})
	require.Equal(t, []string{"[23,33)", "2020Q2"}, result.Rows[0])
	require.Equal(t, map[int]mask.SensitiveType{0: mask.TypeNumber, 1: mask.TypeDate}, report.TouchedColumns)
}

func TestRedactorBuilderRangeActivationAndHashGate(t *testing.T) {
	width, offset := int64(10), int64(0)
	builder := &redactorBuilder{}
	rangeRule := mask.Rule{
		Column: "amount", SensitiveType: mask.TypeNumber, Algorithm: mask.AlgoRange,
		Range: &mask.RangeParams{BucketWidth: &width, BucketOffset: &offset},
	}
	require.NoError(t, builder.ValidateRedactionActivation(rangeRule))

	storedRange := model.MaskRule{
		ID: "range", ColumnName: "amount", SensitiveType: string(mask.TypeNumber),
		Algo: string(mask.AlgoRange), Enabled: true,
		RangeBucketWidth: &width, RangeBucketOffset: &offset,
	}
	_, err := builder.compile([]model.MaskRule{storedRange}, "")
	require.NoError(t, err, "a range-only rule set must compile without a hash key")
	_, err = builder.compile([]model.MaskRule{
		storedRange,
		{ID: "hash", ColumnName: "name", SensitiveType: string(mask.TypeGeneric), Algo: string(mask.AlgoHash), Enabled: true},
	}, "")
	require.ErrorIs(t, err, mask.ErrHashKeyRequired)
}

func TestAssembleRangeStartupValidationSQLite(t *testing.T) {
	t.Run("enabled range without hash key starts", func(t *testing.T) {
		t.Setenv(config.RedactionHashKeyEnv, "")
		path := filepath.Join(t.TempDir(), "valid-range.db")
		width, offset := int64(10), int64(0)
		seedBootstrapMaskRule(t, path, model.MaskRule{
			ID: "valid-number", ColumnName: "amount", SensitiveType: string(mask.TypeNumber),
			Algo: string(mask.AlgoRange), Enabled: true,
			RangeBucketWidth: &width, RangeBucketOffset: &offset,
		})
		runtime, err := Assemble(context.Background(), bootstrapTestConfig(path), bootstrapTestSecret)
		require.NoError(t, err)
		require.NoError(t, runtime.Close())
	})

	tests := []struct {
		name string
		rule model.MaskRule
	}{
		{
			name: "enabled number missing width",
			rule: model.MaskRule{ID: "bad-number", ColumnName: "amount", SensitiveType: string(mask.TypeNumber), Algo: string(mask.AlgoRange), Enabled: true},
		},
		{
			name: "enabled date invalid granularity",
			rule: model.MaskRule{ID: "bad-date", ColumnName: "created_at", SensitiveType: string(mask.TypeDate), Algo: string(mask.AlgoRange), Enabled: true, RangeGranularity: stringPointerBootstrap("week")},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv(config.RedactionHashKeyEnv, "")
			path := filepath.Join(t.TempDir(), "invalid-range.db")
			seedBootstrapMaskRule(t, path, test.rule)
			runtime, err := Assemble(context.Background(), bootstrapTestConfig(path), bootstrapTestSecret)
			require.Nil(t, runtime)
			require.ErrorIs(t, err, mask.ErrInvalidRangeParams)
		})
	}

	t.Run("disabled invalid range follows draft policy", func(t *testing.T) {
		t.Setenv(config.RedactionHashKeyEnv, "")
		path := filepath.Join(t.TempDir(), "disabled-invalid-range.db")
		seedBootstrapMaskRule(t, path, model.MaskRule{
			ID: "disabled-bad-number", ColumnName: "amount", SensitiveType: string(mask.TypeNumber),
			Algo: string(mask.AlgoRange), Enabled: false,
		})
		runtime, err := Assemble(context.Background(), bootstrapTestConfig(path), bootstrapTestSecret)
		require.NoError(t, err)
		require.NoError(t, runtime.Close())
	})
}

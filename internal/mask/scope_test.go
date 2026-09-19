package mask

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestRelationSourceAwareScopedMatching(t *testing.T) {
	phone := "13812345678"
	tests := []struct {
		name              string
		rules             []Rule
		source            ColumnSource
		possibleRelations []model.ObjectRef
		want              string
	}{
		{
			name: "table wildcard exact hit",
			rules: []Rule{{
				Table: "customers", Column: "phone", SensitiveType: TypePhone, Algorithm: AlgoMask,
			}},
			source: ColumnSource{Column: "phone", Source: model.ObjectRef{Table: "customers"}},
			want:   "138****5678",
		},
		{
			name: "different table same column does not hit",
			rules: []Rule{{
				Table: "customers", Column: "phone", SensitiveType: TypePhone, Algorithm: AlgoMask,
			}},
			source:            ColumnSource{Column: "phone", Source: model.ObjectRef{Table: "orders"}},
			possibleRelations: []model.ObjectRef{{Table: "orders"}},
			want:              phone,
		},
		{
			name: "schema exact wins over wildcard",
			rules: []Rule{
				{Schema: "public", Table: "customers", Column: "phone", SensitiveType: TypePhone, Algorithm: AlgoBlock},
				{Table: "customers", Column: "phone", SensitiveType: TypePhone, Algorithm: AlgoMask},
			},
			source: ColumnSource{Column: "phone", Source: model.ObjectRef{Schema: "public", Table: "customers"}},
			want:   BlockPlaceholder,
		},
		{
			name: "other schema falls through to wildcard",
			rules: []Rule{
				{Schema: "public", Table: "customers", Column: "phone", SensitiveType: TypePhone, Algorithm: AlgoBlock},
				{Table: "customers", Column: "phone", SensitiveType: TypePhone, Algorithm: AlgoMask},
			},
			source: ColumnSource{Column: "phone", Source: model.ObjectRef{Schema: "private", Table: "customers"}},
			want:   "138****5678",
		},
		{
			name: "missing source schema does not guess exact schema",
			rules: []Rule{{
				Schema: "public", Table: "customers", Column: "phone", SensitiveType: TypePhone, Algorithm: AlgoBlock,
			}},
			source:            ColumnSource{Column: "phone", Source: model.ObjectRef{Table: "customers"}},
			possibleRelations: []model.ObjectRef{{Table: "customers"}},
			want:              phone,
		},
		{
			name: "missing source schema still hits wildcard",
			rules: []Rule{
				{Schema: "public", Table: "customers", Column: "phone", SensitiveType: TypePhone, Algorithm: AlgoBlock},
				{Table: "customers", Column: "phone", SensitiveType: TypePhone, Algorithm: AlgoMask},
			},
			source: ColumnSource{Column: "phone", Source: model.ObjectRef{Table: "customers"}},
			want:   "138****5678",
		},
		{
			name: "schema comparison is case sensitive",
			rules: []Rule{{
				Schema: "Public", Table: "customers", Column: "phone", SensitiveType: TypePhone, Algorithm: AlgoBlock,
			}},
			source:            ColumnSource{Column: "phone", Source: model.ObjectRef{Schema: "public", Table: "customers"}},
			possibleRelations: []model.ObjectRef{{Schema: "public", Table: "customers"}},
			want:              phone,
		},
		{
			name: "table comparison is case sensitive",
			rules: []Rule{{
				Table: "Customers", Column: "phone", SensitiveType: TypePhone, Algorithm: AlgoBlock,
			}},
			source:            ColumnSource{Column: "phone", Source: model.ObjectRef{Table: "customers"}},
			possibleRelations: []model.ObjectRef{{Table: "customers"}},
			want:              phone,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			redactor, err := NewRedactor(test.rules)
			require.NoError(t, err)
			result, report := relationAware(t, redactor).ApplyWithColumnSources(
				model.QueryResult{Columns: []string{"phone"}, Rows: [][]string{{phone}}},
				[]ColumnSource{test.source},
				test.possibleRelations,
			)
			require.Equal(t, test.want, result.Rows[0][0])
			if test.want == phone {
				require.Equal(t, RedactReport{}, report)
			} else {
				require.Equal(t, map[int]SensitiveType{0: TypePhone}, report.TouchedColumns)
				require.Equal(t, 1, report.MaskedCells)
			}
		})
	}
}

func TestRelationSourceAwareAliasGlobalPriorityAndUnaliasedScopedRule(t *testing.T) {
	redactor, err := NewRedactor([]Rule{
		{Column: "secret", SensitiveType: TypeGeneric, Algorithm: AlgoBlock},
		{Table: "customers", Column: "phone", SensitiveType: TypePhone, Algorithm: AlgoMask},
	})
	require.NoError(t, err)

	result, report := relationAware(t, redactor).ApplyWithColumnSources(
		model.QueryResult{
			Columns: []string{"secret", "phone"},
			Rows:    [][]string{{"13812345678", "13912345678"}},
		},
		[]ColumnSource{
			{Column: "phone", Source: model.ObjectRef{Table: "customers"}},
			{Column: "phone", Source: model.ObjectRef{Table: "customers"}},
		},
		nil,
	)

	require.Equal(t, []string{BlockPlaceholder, "139****5678"}, result.Rows[0])
	require.Equal(t, map[int]SensitiveType{0: TypeGeneric, 1: TypePhone}, report.TouchedColumns)
	require.Equal(t, 2, report.MaskedCells)
	require.Nil(t, report.UnresolvedScopedColumns)
}

func TestRelationSourceAwareUnresolvedScopedFallbackScenarios(t *testing.T) {
	redactor, err := NewRedactor([]Rule{{
		Table: "customers", Column: "phone", SensitiveType: TypePhone, Algorithm: AlgoMask,
	}})
	require.NoError(t, err)

	tests := []struct {
		name              string
		resultColumn      string
		sources           []ColumnSource
		possibleRelations []model.ObjectRef
		want              string
		wantFallback      bool
	}{
		{
			name:              "join bare column",
			resultColumn:      "phone",
			sources:           []ColumnSource{{Column: "phone"}},
			possibleRelations: []model.ObjectRef{{Table: "customers", Alias: "c"}, {Table: "orders", Alias: "o"}},
			want:              BlockPlaceholder,
			wantFallback:      true,
		},
		{
			name:              "derived table without protected relation",
			resultColumn:      "phone",
			sources:           []ColumnSource{{Column: "phone"}},
			possibleRelations: []model.ObjectRef{{Table: "orders"}},
			want:              "13812345678",
		},
		{
			name:              "select star missing source slot",
			resultColumn:      "phone",
			sources:           nil,
			possibleRelations: []model.ObjectRef{{Table: "customers"}},
			want:              BlockPlaceholder,
			wantFallback:      true,
		},
		{
			name:              "cte outer unresolved column",
			resultColumn:      "phone",
			sources:           []ColumnSource{{}},
			possibleRelations: []model.ObjectRef{{Table: "customers"}},
			want:              BlockPlaceholder,
			wantFallback:      true,
		},
		{
			name:              "unresolved renamed column matches source name",
			resultColumn:      "mobile",
			sources:           []ColumnSource{{Column: "phone"}},
			possibleRelations: []model.ObjectRef{{Table: "customers"}},
			want:              BlockPlaceholder,
			wantFallback:      true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, report := relationAware(t, redactor).ApplyWithColumnSources(
				model.QueryResult{Columns: []string{test.resultColumn}, Rows: [][]string{{"13812345678"}}},
				test.sources,
				test.possibleRelations,
			)
			require.Equal(t, test.want, result.Rows[0][0])
			if test.wantFallback {
				require.Equal(t, map[int]SensitiveType{0: TypePhone}, report.TouchedColumns)
				require.Equal(t, map[int]SensitiveType{0: TypePhone}, report.UnresolvedScopedColumns)
				require.Equal(t, 1, report.MaskedCells)
			} else {
				require.Equal(t, RedactReport{}, report)
			}
		})
	}
}

func TestRelationSourceAwareSelfJoinAndExplicitSchema(t *testing.T) {
	t.Run("self join aliases resolve to same physical table", func(t *testing.T) {
		redactor, err := NewRedactor([]Rule{{
			Table: "customers", Column: "phone", SensitiveType: TypePhone, Algorithm: AlgoMask,
		}})
		require.NoError(t, err)
		result, report := relationAware(t, redactor).ApplyWithColumnSources(
			model.QueryResult{Columns: []string{"left_phone", "right_phone"}, Rows: [][]string{{"13812345678", "13912345678"}}},
			[]ColumnSource{
				{Column: "phone", Source: model.ObjectRef{Table: "customers", Alias: "c1"}},
				{Column: "phone", Source: model.ObjectRef{Table: "customers", Alias: "c2"}},
			},
			nil,
		)
		require.Equal(t, []string{"138****5678", "139****5678"}, result.Rows[0])
		require.Equal(t, map[int]SensitiveType{0: TypePhone, 1: TypePhone}, report.TouchedColumns)
		require.Equal(t, 2, report.MaskedCells)
	})

	t.Run("exact schema fallback requires explicit same schema relation", func(t *testing.T) {
		redactor, err := NewRedactor([]Rule{{
			Schema: "public", Table: "customers", Column: "phone", SensitiveType: TypePhone, Algorithm: AlgoMask,
		}})
		require.NoError(t, err)
		v2 := relationAware(t, redactor)
		input := model.QueryResult{Columns: []string{"phone"}, Rows: [][]string{{"13812345678"}}}

		result, report := v2.ApplyWithColumnSources(input, nil, []model.ObjectRef{{Table: "customers"}})
		require.Equal(t, "13812345678", result.Rows[0][0])
		require.Equal(t, RedactReport{}, report)

		result, report = v2.ApplyWithColumnSources(input, nil, []model.ObjectRef{{Schema: "public", Table: "customers", Alias: "c"}})
		require.Equal(t, BlockPlaceholder, result.Rows[0][0])
		require.Equal(t, map[int]SensitiveType{0: TypePhone}, report.UnresolvedScopedColumns)
	})
}

func TestRelationSourceAwareFallbackTypePriorityAndEmptyValues(t *testing.T) {
	redactor, err := NewRedactor([]Rule{
		{Table: "customers", Column: "phone", SensitiveType: TypeEmail, Algorithm: AlgoMask},
		{Table: "orders", Column: "phone", SensitiveType: TypePhone, Algorithm: AlgoMask},
	})
	require.NoError(t, err)
	v2 := relationAware(t, redactor)
	input := model.QueryResult{
		Columns: []string{"phone"},
		Rows:    [][]string{{""}, {"  "}, {"NULL"}, {" <NIL> "}, {"13812345678"}, nil},
	}
	result, report := v2.ApplyWithColumnSources(input, nil, []model.ObjectRef{{Table: "customers"}, {Table: "orders"}})

	for rowIndex := 0; rowIndex < 4; rowIndex++ {
		require.Equal(t, input.Rows[rowIndex][0], result.Rows[rowIndex][0])
	}
	require.Equal(t, BlockPlaceholder, result.Rows[4][0])
	require.Equal(t, map[int]SensitiveType{0: TypePhone}, report.TouchedColumns)
	require.Equal(t, map[int]SensitiveType{0: TypePhone}, report.UnresolvedScopedColumns)
	require.Equal(t, 1, report.MaskedCells)

	emptyResult, emptyReport := v2.ApplyWithColumnSources(
		model.QueryResult{Columns: []string{"phone"}, Rows: [][]string{{""}, {"NULL"}, {"<nil>"}}},
		nil,
		[]model.ObjectRef{{Table: "customers"}},
	)
	require.Equal(t, [][]string{{""}, {"NULL"}, {"<nil>"}}, emptyResult.Rows)
	require.Equal(t, map[int]SensitiveType{0: TypeEmail}, emptyReport.TouchedColumns)
	require.Zero(t, emptyReport.MaskedCells)
	require.Nil(t, emptyReport.UnresolvedScopedColumns)
}

func TestRelationSourceAwareScopedAlgorithmsRemainOrthogonal(t *testing.T) {
	width := int64(1)
	month := RangeMonth
	redactor, err := NewRedactor([]Rule{
		{Table: "customers", Column: "phone", SensitiveType: TypePhone, Algorithm: AlgoMask},
		{Table: "customers", Column: "name", SensitiveType: TypeGeneric, Algorithm: AlgoHash},
		{Table: "customers", Column: "secret", SensitiveType: TypeGeneric, Algorithm: AlgoBlock},
		{Table: "customers", Column: "amount", SensitiveType: TypeNumber, Algorithm: AlgoRange, Range: &RangeParams{BucketWidth: &width}},
		{Table: "customers", Column: "created", SensitiveType: TypeDate, Algorithm: AlgoRange, Range: &RangeParams{Granularity: &month}},
	}, WithHashKey([]byte("0123456789abcdef0123456789abcdef")))
	require.NoError(t, err)

	columns := []string{"phone", "name", "secret", "amount", "created"}
	sources := make([]ColumnSource, len(columns))
	for index, column := range columns {
		sources[index] = ColumnSource{Column: column, Source: model.ObjectRef{Table: "customers"}}
	}
	result, report := relationAware(t, redactor).ApplyWithColumnSources(
		model.QueryResult{Columns: columns, Rows: [][]string{{"13812345678", "Alice Zhang", "raw", "1.0000000000000001", "1990-08-21"}}},
		sources,
		nil,
	)

	require.Equal(t, []string{
		"138****5678",
		"h.d119be750b2bb0be4dd7e2fc9fdae30f",
		BlockPlaceholder,
		"[1,2)",
		"1990-08",
	}, result.Rows[0])
	require.Equal(t, 5, report.MaskedCells)
	require.Nil(t, report.UnresolvedScopedColumns)
}

func TestNewRedactorScopedDuplicateKeys(t *testing.T) {
	base := Rule{Column: "phone", SensitiveType: TypePhone, Algorithm: AlgoMask}
	tests := []struct {
		name    string
		rules   []Rule
		wantErr bool
	}{
		{
			name:  "global and scoped same column coexist",
			rules: []Rule{base, {Table: "customers", Column: "PHONE", SensitiveType: TypePhone, Algorithm: AlgoMask}},
		},
		{
			name: "different tables same column coexist",
			rules: []Rule{
				{Table: "customers", Column: "phone", SensitiveType: TypePhone, Algorithm: AlgoMask},
				{Table: "orders", Column: "PHONE", SensitiveType: TypePhone, Algorithm: AlgoMask},
			},
		},
		{
			name: "different schemas same table and column coexist",
			rules: []Rule{
				{Schema: "public", Table: "customers", Column: "phone", SensitiveType: TypePhone, Algorithm: AlgoMask},
				{Schema: "private", Table: "customers", Column: "PHONE", SensitiveType: TypePhone, Algorithm: AlgoMask},
			},
		},
		{
			name: "duplicate wildcard scoped key rejected",
			rules: []Rule{
				{Table: "customers", Column: "phone", SensitiveType: TypePhone, Algorithm: AlgoMask},
				{Table: "customers", Column: " PHONE ", SensitiveType: TypeEmail, Algorithm: AlgoMask},
			},
			wantErr: true,
		},
		{
			name: "duplicate exact scoped key rejected",
			rules: []Rule{
				{Schema: "public", Table: "customers", Column: "phone", SensitiveType: TypePhone, Algorithm: AlgoMask},
				{Schema: "public", Table: "customers", Column: "`PHONE`", SensitiveType: TypeEmail, Algorithm: AlgoMask},
			},
			wantErr: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewRedactor(test.rules)
			if test.wantErr {
				require.ErrorIs(t, err, ErrDuplicateMaskColumn)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestRelationSourceAwareIndexesAndReportJSONAreFrozen(t *testing.T) {
	rules := []Rule{{Table: "customers", Column: "phone", SensitiveType: TypePhone, Algorithm: AlgoMask}}
	redactor, err := NewRedactor(rules)
	require.NoError(t, err)
	rules[0].Table = "orders"
	rules[0].Column = "email"

	result, report := relationAware(t, redactor).ApplyWithColumnSources(
		model.QueryResult{Columns: []string{"phone"}, Rows: [][]string{{"13812345678"}}},
		[]ColumnSource{{Column: "phone", Source: model.ObjectRef{Table: "customers"}}},
		nil,
	)
	require.Equal(t, "138****5678", result.Rows[0][0])
	require.Equal(t, 1, report.MaskedCells)

	report.UnresolvedScopedColumns = map[int]SensitiveType{0: TypePhone}
	encoded, err := json.Marshal(report)
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"unresolved_scoped_columns":{"0":"phone"}`)

	_, err = NewRedactor([]Rule{{Schema: "public", Column: "phone", SensitiveType: TypePhone, Algorithm: AlgoMask}})
	require.Error(t, err)
	require.False(t, errors.Is(err, ErrDuplicateMaskColumn))
}

func relationAware(t *testing.T, redactor Redactor) RelationSourceAwareRedactor {
	t.Helper()
	v2, ok := redactor.(RelationSourceAwareRedactor)
	require.True(t, ok)
	return v2
}

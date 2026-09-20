package mask

import (
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestProjectionLineageDirectAndTransparentExecuteEveryAlgorithm(t *testing.T) {
	width := int64(10)
	tests := []struct {
		name     string
		rule     Rule
		option   Option
		value    string
		expected string
	}{
		{
			name:     "mask",
			rule:     Rule{Table: "customers", Column: "phone", SensitiveType: TypePhone, Algorithm: AlgoMask},
			value:    "13812345678",
			expected: "138****5678",
		},
		{
			name:     "hash",
			rule:     Rule{Table: "customers", Column: "name", SensitiveType: TypeGeneric, Algorithm: AlgoHash},
			option:   WithHashKey([]byte("0123456789abcdef0123456789abcdef")),
			value:    "Alice Zhang",
			expected: "h.d119be750b2bb0be4dd7e2fc9fdae30f",
		},
		{
			name:     "block",
			rule:     Rule{Table: "customers", Column: "secret", SensitiveType: TypeGeneric, Algorithm: AlgoBlock},
			value:    "classified",
			expected: BlockPlaceholder,
		},
		{
			name: "range",
			rule: Rule{
				Table: "customers", Column: "amount", SensitiveType: TypeNumber, Algorithm: AlgoRange,
				Range: &RangeParams{BucketWidth: &width},
			},
			value:    "17",
			expected: "[10,20)",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			options := []Option{}
			if test.option != nil {
				options = append(options, test.option)
			}
			redactor, err := NewRedactor([]Rule{test.rule}, options...)
			require.NoError(t, err)
			for _, arm := range []model.LineageArm{
				directLineageArm("", "customers", test.rule.Column),
				transparentLineageArm("", "customers", test.rule.Column, "parentheses"),
				transparentLineageArm("", "customers", test.rule.Column, "collate"),
				transparentLineageArm("", "customers", test.rule.Column, "coalesce_null"),
			} {
				result, report := lineageAware(t, redactor).ApplyWithProjectionLineages(
					model.QueryResult{Columns: []string{"output"}, Rows: [][]string{{test.value}}},
					[]model.LineageArm{arm},
				)
				require.Equal(t, test.expected, result.Rows[0][0])
				require.Equal(t, 1, report.MaskedCells)
				require.Equal(t, test.rule.SensitiveType, report.TouchedColumns[0])
				require.Nil(t, report.UnresolvedScopedColumns)
			}
		})
	}
}

func TestProjectionLineageEmptyConcatWhitelistAndRangeException(t *testing.T) {
	width := int64(10)
	tests := []struct {
		name     string
		rule     Rule
		option   Option
		value    string
		expected string
	}{
		{
			name:  "mask exact",
			rule:  Rule{Table: "customers", Column: "phone", SensitiveType: TypePhone, Algorithm: AlgoMask},
			value: "13812345678", expected: "138****5678",
		},
		{
			name:   "hash exact",
			rule:   Rule{Table: "customers", Column: "name", SensitiveType: TypeGeneric, Algorithm: AlgoHash},
			option: WithHashKey([]byte("0123456789abcdef0123456789abcdef")),
			value:  "Alice Zhang", expected: "h.d119be750b2bb0be4dd7e2fc9fdae30f",
		},
		{
			name:  "block exact",
			rule:  Rule{Table: "customers", Column: "secret", SensitiveType: TypeGeneric, Algorithm: AlgoBlock},
			value: "classified", expected: BlockPlaceholder,
		},
		{
			name:  "range blocks",
			rule:  Rule{Table: "customers", Column: "amount", SensitiveType: TypeNumber, Algorithm: AlgoRange, Range: &RangeParams{BucketWidth: &width}},
			value: "17", expected: BlockPlaceholder,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			options := []Option{}
			if test.option != nil {
				options = append(options, test.option)
			}
			redactor, err := NewRedactor([]Rule{test.rule}, options...)
			require.NoError(t, err)
			result, report := lineageAware(t, redactor).ApplyWithProjectionLineages(
				model.QueryResult{Columns: []string{"output"}, Rows: [][]string{{test.value}}},
				[]model.LineageArm{transparentLineageArm("", "customers", test.rule.Column, "concat_empty")},
			)
			require.Equal(t, test.expected, result.Rows[0][0])
			require.Equal(t, 1, report.MaskedCells)
			require.Nil(t, report.UnresolvedScopedColumns)
		})
	}
}

func TestProjectionLineageUnsafeTransformsAlwaysBlock(t *testing.T) {
	redactor, err := NewRedactor([]Rule{{
		Table: "customers", Column: "phone", SensitiveType: TypePhone, Algorithm: AlgoMask,
	}})
	require.NoError(t, err)

	operations := []string{
		"upper", "lower", "trim", "ltrim", "rtrim", "cast", "cast_type",
		"substr", "left", "right", "replace", "rpad", "lpad", "arithmetic",
		"date_arithmetic", "concat_nonempty", "json_extract",
	}
	for _, operation := range operations {
		t.Run(operation, func(t *testing.T) {
			result, report := lineageAware(t, redactor).ApplyWithProjectionLineages(
				model.QueryResult{Columns: []string{"mobile"}, Rows: [][]string{{"13812345678"}}},
				[]model.LineageArm{transparentLineageArm("", "customers", "phone", operation)},
			)
			require.Equal(t, BlockPlaceholder, result.Rows[0][0])
			require.Equal(t, 1, report.MaskedCells)
			require.Nil(t, report.UnresolvedScopedColumns)
		})
	}
}

func TestProjectionLineageCompositeControlWindowAndAggregateBlock(t *testing.T) {
	redactor, err := NewRedactor([]Rule{{
		Table: "customers", Column: "phone", SensitiveType: TypePhone, Algorithm: AlgoMask,
	}})
	require.NoError(t, err)
	phone := valueDependency("", "customers", "phone")

	tests := []struct {
		name string
		arm  model.LineageArm
	}{
		{
			name: "same source repeated composite",
			arm: model.LineageArm{Kind: model.LineageComposite, Status: model.LineageResolved,
				Dependencies: []model.ColumnDependency{phone, phone}},
		},
		{
			name: "multiple physical sources",
			arm: model.LineageArm{Kind: model.LineageComposite, Status: model.LineageResolved,
				Dependencies: []model.ColumnDependency{phone, valueDependency("", "orders", "note")}},
		},
		{
			name: "case control dependency",
			arm: model.LineageArm{Kind: model.LineageConstant, Status: model.LineageResolved,
				Dependencies: []model.ColumnDependency{{Origin: phone.Origin, Role: model.DependencyControl}}},
		},
		{
			name: "window value dependency",
			arm: model.LineageArm{Kind: model.LineageWindow, Operation: "window:first_value", Status: model.LineageResolved,
				Dependencies: []model.ColumnDependency{phone}},
		},
		{
			name: "window order dependency",
			arm: model.LineageArm{Kind: model.LineageWindow, Operation: "window:row_number", Status: model.LineageResolved,
				Dependencies: []model.ColumnDependency{{Origin: phone.Origin, Role: model.DependencyOrder}}},
		},
		{
			name: "aggregate filter dependency",
			arm: model.LineageArm{Kind: model.LineageAggregate, Operation: "aggregate:count", Status: model.LineageResolved,
				Dependencies: []model.ColumnDependency{{Origin: phone.Origin, Role: model.DependencyFilter}}},
		},
		{
			name: "aggregate group dependency",
			arm: model.LineageArm{Kind: model.LineageAggregate, Operation: "aggregate:sum", Status: model.LineageResolved,
				Dependencies: []model.ColumnDependency{{Origin: phone.Origin, Role: model.DependencyGroup}}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, report := lineageAware(t, redactor).ApplyWithProjectionLineages(
				model.QueryResult{Columns: []string{"output"}, Rows: [][]string{{"13812345678"}}},
				[]model.LineageArm{test.arm},
			)
			require.Equal(t, BlockPlaceholder, result.Rows[0][0])
			require.Equal(t, 1, report.MaskedCells)
			require.Nil(t, report.UnresolvedScopedColumns, "resolved unsafe constructs are not unresolved sources")
		})
	}
}

func TestProjectionLineageRuleConflictsBlock(t *testing.T) {
	redactor, err := NewRedactor([]Rule{
		{Column: "mobile", SensitiveType: TypePhone, Algorithm: AlgoMask},
		{Table: "customers", Column: "phone", SensitiveType: TypeGeneric, Algorithm: AlgoBlock},
	})
	require.NoError(t, err)

	result, report := lineageAware(t, redactor).ApplyWithProjectionLineages(
		model.QueryResult{Columns: []string{"mobile"}, Rows: [][]string{{"13812345678"}}},
		[]model.LineageArm{directLineageArm("", "customers", "phone")},
	)
	require.Equal(t, BlockPlaceholder, result.Rows[0][0])
	require.Equal(t, TypePhone, report.TouchedColumns[0])
	require.Equal(t, 1, report.MaskedCells)
	require.Nil(t, report.UnresolvedScopedColumns)
}

func TestProjectionLineageSetOperationClosure(t *testing.T) {
	redactor, err := NewRedactor([]Rule{
		{Table: "customers", Column: "phone", SensitiveType: TypePhone, Algorithm: AlgoMask},
		{Table: "orders", Column: "phone", SensitiveType: TypePhone, Algorithm: AlgoMask},
		{Table: "customers", Column: "email", SensitiveType: TypeEmail, Algorithm: AlgoMask},
		{Table: "accounts", Column: "phone", SensitiveType: TypeGeneric, Algorithm: AlgoBlock},
	})
	require.NoError(t, err)
	direct := directLineageArm("", "customers", "phone")

	tests := []struct {
		name       string
		arms       []model.LineageArm
		expected   string
		unresolved bool
	}{
		{name: "same physical column and rule is exact", arms: []model.LineageArm{direct, direct}, expected: "138****5678"},
		{name: "same column transparent is exact", arms: []model.LineageArm{direct, transparentLineageArm("", "customers", "phone", "collate")}, expected: "138****5678"},
		{name: "different table blocks", arms: []model.LineageArm{direct, directLineageArm("", "orders", "phone")}, expected: BlockPlaceholder},
		{name: "different schema blocks", arms: []model.LineageArm{direct, directLineageArm("crm", "customers", "phone")}, expected: BlockPlaceholder},
		{name: "different column blocks", arms: []model.LineageArm{direct, directLineageArm("", "customers", "email")}, expected: BlockPlaceholder},
		{name: "different effective rule blocks", arms: []model.LineageArm{direct, directLineageArm("", "accounts", "phone")}, expected: BlockPlaceholder},
		{name: "constant mixed in blocks", arms: []model.LineageArm{direct, {Kind: model.LineageConstant, Status: model.LineageSourceFree}}, expected: BlockPlaceholder},
		{
			name: "unknown arm blocks",
			arms: []model.LineageArm{direct, {
				Kind: model.LineageOpaque, Status: model.LineageUnsupported,
				PossibleRelations: []model.ObjectRef{{Table: "customers"}},
			}},
			expected: BlockPlaceholder, unresolved: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, report := lineageAware(t, redactor).ApplyWithProjectionLineages(
				model.QueryResult{Columns: []string{"mobile"}, Rows: [][]string{{"13812345678"}}},
				test.arms,
			)
			require.Equal(t, test.expected, result.Rows[0][0])
			require.Equal(t, 1, report.MaskedCells)
			if test.unresolved {
				require.Equal(t, map[int]SensitiveType{0: TypePhone}, report.UnresolvedScopedColumns)
			} else {
				require.Nil(t, report.UnresolvedScopedColumns)
			}
		})
	}
}

func TestProjectionLineageAggregateCountRules(t *testing.T) {
	redactor, err := NewRedactor([]Rule{{
		Table: "customers", Column: "phone", SensitiveType: TypePhone, Algorithm: AlgoMask,
	}})
	require.NoError(t, err)
	tests := []struct {
		name     string
		arm      model.LineageArm
		input    string
		expected string
		touched  bool
	}{
		{
			name: "count star is source free",
			arm: model.LineageArm{
				Kind: model.LineageAggregate, Operation: "aggregate:count_star", Status: model.LineageSourceFree,
				PossibleRelations: []model.ObjectRef{{Table: "customers"}},
			},
			input: "2", expected: "2",
		},
		{
			name:  "count non-null constant is source free",
			arm:   model.LineageArm{Kind: model.LineageAggregate, Operation: "aggregate:count_constant", Status: model.LineageSourceFree},
			input: "2", expected: "2",
		},
		{
			name:  "count null is source free",
			arm:   model.LineageArm{Kind: model.LineageAggregate, Operation: "aggregate:count", Status: model.LineageSourceFree},
			input: "0", expected: "0",
		},
		{
			name: "count column blocks",
			arm: model.LineageArm{Kind: model.LineageAggregate, Operation: "aggregate:count", Status: model.LineageResolved,
				Dependencies: []model.ColumnDependency{valueDependency("", "customers", "phone")}},
			input: "2", expected: BlockPlaceholder, touched: true,
		},
		{
			name: "count distinct column blocks",
			arm: model.LineageArm{Kind: model.LineageAggregate, Operation: "aggregate:count_distinct", Status: model.LineageResolved,
				Dependencies: []model.ColumnDependency{valueDependency("", "customers", "phone")}},
			input: "2", expected: BlockPlaceholder, touched: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, report := lineageAware(t, redactor).ApplyWithProjectionLineages(
				model.QueryResult{Columns: []string{"count"}, Rows: [][]string{{test.input}}},
				[]model.LineageArm{test.arm},
			)
			require.Equal(t, test.expected, result.Rows[0][0])
			if test.touched {
				require.Equal(t, map[int]SensitiveType{0: TypePhone}, report.TouchedColumns)
				require.Equal(t, 1, report.MaskedCells)
			} else {
				require.Nil(t, report.TouchedColumns)
				require.Zero(t, report.MaskedCells)
			}
		})
	}
}

func TestProjectionLineageSensitiveAggregateCatalogAlwaysBlocks(t *testing.T) {
	redactor, err := NewRedactor([]Rule{{
		Table: "customers", Column: "phone", SensitiveType: TypePhone, Algorithm: AlgoMask,
	}})
	require.NoError(t, err)
	operations := []string{
		"aggregate:max", "aggregate:min", "aggregate:any_value", "aggregate:sum", "aggregate:avg",
		"aggregate:string_agg", "aggregate:group_concat", "aggregate:array_agg", "aggregate:json_agg",
		"aggregate:stddev", "aggregate:variance", "aggregate:median", "aggregate:percentile", "aggregate:mode",
		"aggregate:bool_and", "aggregate:bool_or", "aggregate:bit_and", "aggregate:bit_or", "aggregate:every",
	}
	for _, operation := range operations {
		t.Run(operation, func(t *testing.T) {
			arm := model.LineageArm{
				Kind: model.LineageAggregate, Operation: operation, Status: model.LineageResolved,
				Dependencies: []model.ColumnDependency{valueDependency("", "customers", "phone")},
			}
			result, report := lineageAware(t, redactor).ApplyWithProjectionLineages(
				model.QueryResult{Columns: []string{"aggregate"}, Rows: [][]string{{"sensitive-derived-value"}}},
				[]model.LineageArm{arm},
			)
			require.Equal(t, BlockPlaceholder, result.Rows[0][0])
			require.Equal(t, 1, report.MaskedCells)
			require.Nil(t, report.UnresolvedScopedColumns)
		})
	}
}

func TestProjectionLineageRangeSumAndAverageAlwaysBlock(t *testing.T) {
	width := int64(10)
	redactor, err := NewRedactor([]Rule{{
		Table: "orders", Column: "amount", SensitiveType: TypeNumber, Algorithm: AlgoRange,
		Range: &RangeParams{BucketWidth: &width},
	}})
	require.NoError(t, err)
	for _, operation := range []string{"aggregate:sum", "aggregate:avg"} {
		t.Run(operation, func(t *testing.T) {
			result, report := lineageAware(t, redactor).ApplyWithProjectionLineages(
				model.QueryResult{Columns: []string{"total"}, Rows: [][]string{{"117"}}},
				[]model.LineageArm{{
					Kind: model.LineageAggregate, Operation: operation, Status: model.LineageResolved,
					Dependencies: []model.ColumnDependency{valueDependency("", "orders", "amount")},
				}},
			)
			require.Equal(t, BlockPlaceholder, result.Rows[0][0])
			require.Equal(t, 1, report.MaskedCells)
		})
	}
}

func TestProjectionLineageSourceFreeResultNameRuleStillProtects(t *testing.T) {
	redactor, err := NewRedactor([]Rule{{
		Column: "declared_secret", SensitiveType: TypeGeneric, Algorithm: AlgoBlock,
	}})
	require.NoError(t, err)
	for _, arm := range []model.LineageArm{
		{Kind: model.LineageConstant, Status: model.LineageSourceFree},
		{Kind: model.LineageAggregate, Operation: "aggregate:count_star", Status: model.LineageSourceFree},
	} {
		result, report := lineageAware(t, redactor).ApplyWithProjectionLineages(
			model.QueryResult{Columns: []string{"declared_secret"}, Rows: [][]string{{"value"}}},
			[]model.LineageArm{arm},
		)
		require.Equal(t, BlockPlaceholder, result.Rows[0][0])
		require.Equal(t, 1, report.MaskedCells)
		require.Nil(t, report.UnresolvedScopedColumns)
	}
}

func TestProjectionLineageEmptyAndUnresolvedReportSemantics(t *testing.T) {
	redactor, err := NewRedactor([]Rule{
		{Table: "customers", Column: "phone", SensitiveType: TypePhone, Algorithm: AlgoMask},
		{Table: "customers", Column: "email", SensitiveType: TypeEmail, Algorithm: AlgoMask},
	})
	require.NoError(t, err)
	opaque := model.LineageArm{
		Kind: model.LineageOpaque, Status: model.LineageOpaqueState,
		PossibleRelations: []model.ObjectRef{{Table: "customers"}},
	}

	emptyResult, emptyReport := lineageAware(t, redactor).ApplyWithProjectionLineages(
		model.QueryResult{Columns: []string{"unknown"}, Rows: [][]string{{""}, {"  "}, {"NULL"}, {" <NIL> "}}},
		[]model.LineageArm{opaque},
	)
	require.Equal(t, [][]string{{""}, {"  "}, {"NULL"}, {" <NIL> "}}, emptyResult.Rows)
	require.Equal(t, map[int]SensitiveType{0: TypePhone}, emptyReport.TouchedColumns)
	require.Zero(t, emptyReport.MaskedCells)
	require.Nil(t, emptyReport.UnresolvedScopedColumns)

	result, report := lineageAware(t, redactor).ApplyWithProjectionLineages(
		model.QueryResult{Columns: []string{"unknown"}, Rows: [][]string{{"secret-1"}, {""}, {"secret-2"}}},
		[]model.LineageArm{opaque},
	)
	require.Equal(t, [][]string{{BlockPlaceholder}, {""}, {BlockPlaceholder}}, result.Rows)
	require.Equal(t, map[int]SensitiveType{0: TypePhone}, report.TouchedColumns)
	require.Equal(t, 2, report.MaskedCells)
	require.Equal(t, map[int]SensitiveType{0: TypePhone}, report.UnresolvedScopedColumns)
}

func TestProjectionLineageResolvedWithoutRulePreservesValue(t *testing.T) {
	redactor, err := NewRedactor([]Rule{{
		Table: "customers", Column: "phone", SensitiveType: TypePhone, Algorithm: AlgoMask,
	}})
	require.NoError(t, err)
	input := model.QueryResult{Columns: []string{"nickname"}, Rows: [][]string{{"byte-for-byte"}}}
	result, report := lineageAware(t, redactor).ApplyWithProjectionLineages(
		input,
		[]model.LineageArm{directLineageArm("", "customers", "nickname")},
	)
	require.Equal(t, input.Rows, result.Rows)
	require.Nil(t, report.TouchedColumns)
	require.Zero(t, report.MaskedCells)
	require.Nil(t, report.UnresolvedScopedColumns)
}

func TestProjectionLineageOpaqueCandidateCanProveScopedRuleIrrelevant(t *testing.T) {
	redactor, err := NewRedactor([]Rule{{
		Table: "customers", Column: "phone", SensitiveType: TypePhone, Algorithm: AlgoMask,
	}})
	require.NoError(t, err)
	input := model.QueryResult{Columns: []string{"unknown"}, Rows: [][]string{{"byte-for-byte"}}}
	result, report := lineageAware(t, redactor).ApplyWithProjectionLineages(
		input,
		[]model.LineageArm{{
			Kind: model.LineageOpaque, Status: model.LineageOpaqueState,
			PossibleRelations: []model.ObjectRef{{Table: "orders"}},
		}},
	)
	require.Equal(t, input.Rows, result.Rows)
	require.Nil(t, report.TouchedColumns)
	require.Zero(t, report.MaskedCells)
	require.Nil(t, report.UnresolvedScopedColumns)
}

func TestProjectionLineageOpaqueWithoutCandidatesDoesNotTreatMissingInformationAsSafe(t *testing.T) {
	redactor, err := NewRedactor([]Rule{{
		Table: "customers", Column: "phone", SensitiveType: TypePhone, Algorithm: AlgoMask,
	}})
	require.NoError(t, err)
	result, report := lineageAware(t, redactor).ApplyWithProjectionLineages(
		model.QueryResult{Columns: []string{"unknown"}, Rows: [][]string{{"secret"}}},
		[]model.LineageArm{{Kind: model.LineageOpaque, Status: model.LineageOpaqueState}},
	)
	require.Equal(t, BlockPlaceholder, result.Rows[0][0])
	require.Equal(t, 1, report.MaskedCells)
	require.Equal(t, map[int]SensitiveType{0: TypePhone}, report.UnresolvedScopedColumns)
}

func TestProjectionLineageResolvedUnknownNodeStillUsesSensitiveCandidates(t *testing.T) {
	redactor, err := NewRedactor([]Rule{{
		Table: "customers", Column: "phone", SensitiveType: TypePhone, Algorithm: AlgoMask,
	}})
	require.NoError(t, err)
	result, report := lineageAware(t, redactor).ApplyWithProjectionLineages(
		model.QueryResult{Columns: []string{"unknown"}, Rows: [][]string{{"secret"}}},
		[]model.LineageArm{{
			Kind: model.LineageOpaque, Status: model.LineageResolved,
			PossibleRelations: []model.ObjectRef{{Table: "customers"}},
		}},
	)
	require.Equal(t, BlockPlaceholder, result.Rows[0][0])
	require.Equal(t, 1, report.MaskedCells)
	require.Nil(t, report.UnresolvedScopedColumns, "the relation is known even though the transform is unsafe")
}

func lineageAware(t *testing.T, redactor Redactor) ProjectionLineageAwareRedactor {
	t.Helper()
	aware, ok := redactor.(ProjectionLineageAwareRedactor)
	require.True(t, ok)
	return aware
}

func directLineageArm(schema, table, column string) model.LineageArm {
	return model.LineageArm{
		Kind:         model.LineageDirect,
		Status:       model.LineageResolved,
		Dependencies: []model.ColumnDependency{valueDependency(schema, table, column)},
	}
}

func transparentLineageArm(schema, table, column, operation string) model.LineageArm {
	arm := directLineageArm(schema, table, column)
	arm.Kind = model.LineageTransparent
	arm.Operation = operation
	return arm
}

func valueDependency(schema, table, column string) model.ColumnDependency {
	return model.ColumnDependency{
		Origin: model.ColumnOrigin{
			Relation: model.ObjectRef{Schema: schema, Table: table},
			Column:   column,
		},
		Role: model.DependencyValue,
	}
}

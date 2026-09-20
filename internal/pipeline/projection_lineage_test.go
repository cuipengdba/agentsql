package pipeline

import (
	"errors"
	"testing"

	"github.com/cuipengdba/agentsql/internal/mask"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestResolveProjectionLineagesSingleWildcardAlignsEdgesAndSpan(t *testing.T) {
	lineages := []model.ProjectionLineage{
		projection(0, fixedArm("customers", "id")),
		variadicProjection(1, wildcardArm("customers")),
		projection(2, fixedArm("customers", "created_at")),
	}

	aligned := resolveProjectionLineages(lineages, []string{"id", "phone", "email", "created_at"})

	require.Len(t, aligned, 4)
	require.Equal(t, "id", alignedDependencyColumn(t, aligned[0]))
	require.Equal(t, "phone", alignedDependencyColumn(t, aligned[1]))
	require.Equal(t, "email", alignedDependencyColumn(t, aligned[2]))
	require.Equal(t, "created_at", alignedDependencyColumn(t, aligned[3]))
	for _, index := range []int{1, 2} {
		require.Equal(t, model.LineageDirect, aligned[index][0].Kind)
		require.Equal(t, "customers", aligned[index][0].Dependencies[0].Origin.Relation.Table)
	}
}

func TestResolveProjectionLineagesMultipleWildcardsKeepOnlySafeEdges(t *testing.T) {
	lineages := []model.ProjectionLineage{
		projection(0, fixedArm("customers", "id")),
		variadicProjection(1, wildcardArm("customers")),
		projection(2, fixedArm("orders", "phone")),
		variadicProjection(3, wildcardArm("accounts")),
		projection(4, fixedArm("customers", "created_at")),
	}

	aligned := resolveProjectionLineages(
		lineages,
		[]string{"id", "customer_phone", "phone", "account_phone", "other", "created_at"},
	)

	require.Len(t, aligned, 6)
	require.Equal(t, "id", alignedDependencyColumn(t, aligned[0]))
	require.Equal(t, "created_at", alignedDependencyColumn(t, aligned[5]))
	for index := 1; index < 5; index++ {
		require.Len(t, aligned[index], 1)
		require.Equal(t, model.LineageOpaque, aligned[index][0].Kind)
		require.Equal(t, model.LineageOpaqueState, aligned[index][0].Status)
		require.ElementsMatch(t, []model.ObjectRef{
			{Table: "customers"}, {Table: "orders"}, {Table: "accounts"},
		}, aligned[index][0].PossibleRelations, "the fixed expression between stars must remain a candidate")
	}
}

func TestResolveProjectionLineagesInvalidLayoutsAreExplicitOpaque(t *testing.T) {
	customers := []model.ObjectRef{{Table: "customers"}}
	tests := []struct {
		name     string
		lineages []model.ProjectionLineage
		columns  []string
	}{
		{
			name: "negative select index",
			lineages: []model.ProjectionLineage{{
				SelectIndex: -1, Arms: []model.LineageArm{fixedArm("customers", "phone")},
			}},
			columns: []string{"phone"},
		},
		{
			name: "select index out of bounds",
			lineages: []model.ProjectionLineage{{
				SelectIndex: 1, Arms: []model.LineageArm{fixedArm("customers", "phone")},
			}},
			columns: []string{"phone"},
		},
		{
			name: "duplicate projection position",
			lineages: []model.ProjectionLineage{
				projection(0, fixedArm("customers", "phone")),
				projection(0, fixedArm("customers", "email")),
			},
			columns: []string{"phone", "email"},
		},
		{
			name: "fixed result has too few columns",
			lineages: []model.ProjectionLineage{
				projection(0, fixedArm("customers", "phone")),
				projection(1, fixedArm("customers", "email")),
			},
			columns: []string{"phone"},
		},
		{
			name: "fixed result has too many columns",
			lineages: []model.ProjectionLineage{
				projection(0, fixedArm("customers", "phone")),
			},
			columns: []string{"phone", "email"},
		},
		{
			name: "negative wildcard span",
			lineages: []model.ProjectionLineage{
				projection(0, fixedArm("customers", "id")),
				variadicProjection(1, wildcardArm("customers")),
				projection(2, fixedArm("customers", "phone")),
			},
			columns: []string{"only_one"},
		},
		{
			name: "multiple wildcard safe edges overlap",
			lineages: []model.ProjectionLineage{
				projection(0, fixedArm("customers", "id")),
				projection(1, fixedArm("customers", "email")),
				variadicProjection(2, wildcardArm("customers")),
				variadicProjection(3, wildcardArm("customers")),
				projection(4, fixedArm("customers", "phone")),
			},
			columns: []string{"id", "phone"},
		},
		{
			name: "set branches have inconsistent projection arms",
			lineages: []model.ProjectionLineage{
				projection(0, fixedArm("customers", "phone")),
				{SelectIndex: 1, Arms: []model.LineageArm{
					fixedArm("customers", "email"), fixedArm("customers", "email"),
				}},
			},
			columns: []string{"phone", "email"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			aligned := resolveProjectionLineages(test.lineages, test.columns)
			require.NotNil(t, aligned)
			require.Len(t, aligned, len(test.columns))
			for _, arms := range aligned {
				require.Len(t, arms, 1)
				require.Equal(t, model.LineageOpaque, arms[0].Kind)
				require.Equal(t, model.LineageOpaqueState, arms[0].Status)
				require.Equal(t, customers, arms[0].PossibleRelations)
			}
		})
	}
}

func TestResolveProjectionLineagesUnknownSetWildcardArmStaysOpaque(t *testing.T) {
	lineages := []model.ProjectionLineage{{
		SelectIndex: 0,
		Variadic:    true,
		SetOp:       "union_all",
		Arms: []model.LineageArm{
			wildcardArm("customers"),
			wildcardArm("customers"),
		},
	}}

	aligned := resolveProjectionLineages(lineages, []string{"phone"})

	require.Len(t, aligned, 1)
	require.Len(t, aligned[0], 2)
	require.Equal(t, model.LineageDirect, aligned[0][0].Kind)
	require.Equal(t, model.LineageOpaque, aligned[0][1].Kind)
	require.Equal(t, []model.ObjectRef{{Table: "customers"}}, aligned[0][1].PossibleRelations)
	assertOpaqueCandidateBlocks(t, aligned[0])
}

func TestResolveProjectionLineagesFailureCandidatesBlockInsteadOfNil(t *testing.T) {
	aligned := resolveProjectionLineages(
		[]model.ProjectionLineage{{
			SelectIndex: 2,
			Arms: []model.LineageArm{{
				Kind: model.LineageDirect, Status: model.LineageResolved,
				Dependencies: []model.ColumnDependency{{
					Origin: model.ColumnOrigin{Relation: model.ObjectRef{Table: "customers"}, Column: "phone"},
					Role:   model.DependencyValue,
				}},
			}},
		}},
		[]string{"mobile"},
	)

	require.NotNil(t, aligned)
	require.Len(t, aligned, 1)
	require.Equal(t, model.LineageOpaque, aligned[0][0].Kind)
	require.Equal(t, []model.ObjectRef{{Table: "customers"}}, aligned[0][0].PossibleRelations)
	assertOpaqueCandidateBlocks(t, aligned[0])
}

func TestValidateResultRectangleRejectsShortAndLongRows(t *testing.T) {
	require.NoError(t, validateResultRectangle(model.QueryResult{
		Columns: []string{"a", "b"}, Rows: [][]string{{"1", "2"}, {"3", "4"}},
	}))
	for _, test := range []struct {
		name string
		rows [][]string
	}{
		{name: "short row", rows: [][]string{{"1"}}},
		{name: "long row", rows: [][]string{{"1", "2", "3"}}},
		{name: "nil row", rows: [][]string{nil}},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateResultRectangle(model.QueryResult{Columns: []string{"a", "b"}, Rows: test.rows})
			require.Error(t, err)
			require.True(t, errors.Is(err, errNonRectangularResult))
		})
	}
}

func assertOpaqueCandidateBlocks(t *testing.T, arms []model.LineageArm) {
	t.Helper()
	redactor, err := mask.NewRedactor([]mask.Rule{{
		Table: "customers", Column: "phone", SensitiveType: mask.TypePhone, Algorithm: mask.AlgoMask,
	}})
	require.NoError(t, err)
	lineageAware, ok := redactor.(mask.ProjectionLineageAwareRedactor)
	require.True(t, ok)
	result, report := lineageAware.ApplyWithProjectionLineages(
		model.QueryResult{Columns: []string{"mobile"}, Rows: [][]string{{"13812345678"}}},
		[][]model.LineageArm{arms},
	)
	require.Equal(t, mask.BlockPlaceholder, result.Rows[0][0])
	require.Equal(t, 1, report.MaskedCells)
	require.Equal(t, map[int]mask.SensitiveType{0: mask.TypePhone}, report.UnresolvedScopedColumns)
}

func projection(index int, arm model.LineageArm) model.ProjectionLineage {
	return model.ProjectionLineage{SelectIndex: index, Arms: []model.LineageArm{arm}}
}

func variadicProjection(index int, arm model.LineageArm) model.ProjectionLineage {
	lineage := projection(index, arm)
	lineage.Variadic = true
	return lineage
}

func fixedArm(table, column string) model.LineageArm {
	return model.LineageArm{
		Kind: model.LineageDirect, Status: model.LineageResolved,
		Dependencies: []model.ColumnDependency{{
			Origin: model.ColumnOrigin{Relation: model.ObjectRef{Table: table}, Column: column},
			Role:   model.DependencyValue,
		}},
	}
}

func wildcardArm(table string) model.LineageArm {
	return model.LineageArm{
		Kind: model.LineageWildcard, Status: model.LineageResolved,
		PossibleRelations: []model.ObjectRef{{Table: table, Alias: "ignored"}},
	}
}

func alignedDependencyColumn(t *testing.T, arms []model.LineageArm) string {
	t.Helper()
	require.Len(t, arms, 1)
	require.Len(t, arms[0].Dependencies, 1)
	return arms[0].Dependencies[0].Origin.Column
}

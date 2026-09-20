package pipeline

import (
	"errors"
	"fmt"

	"github.com/cuipengdba/agentsql/internal/model"
)

var errNonRectangularResult = errors.New("query result rows do not match columns")

// resolveProjectionLineages aligns SQL target-list lineage to runtime result
// positions. Alignment failure is represented by an explicit opaque arm at
// every result position; nil is never used as a failure signal.
func resolveProjectionLineages(
	lineages []model.ProjectionLineage,
	resultColumns []string,
) [][]model.LineageArm {
	columnCount := len(resultColumns)
	ordered, valid := orderProjectionLineages(lineages)
	if !valid || !consistentArmCounts(ordered) {
		return opaqueProjectionAlignment(columnCount, lineages, "invalid_projection_layout")
	}
	if len(ordered) == 0 {
		if columnCount == 0 {
			return make([][]model.LineageArm, 0)
		}
		return opaqueProjectionAlignment(columnCount, lineages, "missing_projection_lineage")
	}

	variadicIndexes := make([]int, 0, 2)
	for index, lineage := range ordered {
		if lineage.Variadic {
			variadicIndexes = append(variadicIndexes, index)
		}
	}
	fixedCount := len(ordered) - len(variadicIndexes)
	if columnCount < fixedCount {
		return opaqueProjectionAlignment(columnCount, ordered, "negative_variadic_span")
	}

	switch len(variadicIndexes) {
	case 0:
		if len(ordered) != columnCount {
			return opaqueProjectionAlignment(columnCount, ordered, "fixed_projection_count_mismatch")
		}
		aligned := make([][]model.LineageArm, columnCount)
		for index := range ordered {
			aligned[index] = cloneLineageArms(ordered[index].Arms)
		}
		return aligned
	case 1:
		return alignSingleVariadicProjection(ordered, variadicIndexes[0], resultColumns)
	default:
		return alignMultipleVariadicProjections(ordered, variadicIndexes, resultColumns)
	}
}

func orderProjectionLineages(lineages []model.ProjectionLineage) ([]model.ProjectionLineage, bool) {
	if len(lineages) == 0 {
		return []model.ProjectionLineage{}, true
	}
	ordered := make([]model.ProjectionLineage, len(lineages))
	occupied := make([]bool, len(lineages))
	for _, lineage := range lineages {
		if lineage.SelectIndex < 0 || lineage.SelectIndex >= len(lineages) || occupied[lineage.SelectIndex] {
			return nil, false
		}
		occupied[lineage.SelectIndex] = true
		ordered[lineage.SelectIndex] = lineage
	}
	return ordered, true
}

func consistentArmCounts(lineages []model.ProjectionLineage) bool {
	if len(lineages) == 0 {
		return true
	}
	armCount := len(lineages[0].Arms)
	if armCount == 0 {
		return false
	}
	for _, lineage := range lineages[1:] {
		if len(lineage.Arms) != armCount {
			return false
		}
	}
	return true
}

func alignSingleVariadicProjection(
	lineages []model.ProjectionLineage,
	variadicIndex int,
	resultColumns []string,
) [][]model.LineageArm {
	columnCount := len(resultColumns)
	fixedCount := len(lineages) - 1
	span := columnCount - fixedCount
	if span < 0 || variadicIndex < 0 || variadicIndex >= len(lineages) {
		return opaqueProjectionAlignment(columnCount, lineages, "invalid_single_variadic_span")
	}
	prefixCount := variadicIndex
	suffixCount := len(lineages) - variadicIndex - 1
	if prefixCount+suffixCount > columnCount {
		return opaqueProjectionAlignment(columnCount, lineages, "overlapping_projection_edges")
	}

	aligned := make([][]model.LineageArm, columnCount)
	for index := 0; index < prefixCount; index++ {
		aligned[index] = cloneLineageArms(lineages[index].Arms)
	}
	for suffixOffset := 0; suffixOffset < suffixCount; suffixOffset++ {
		resultIndex := columnCount - suffixCount + suffixOffset
		lineageIndex := variadicIndex + 1 + suffixOffset
		if resultIndex < prefixCount || resultIndex < 0 || resultIndex >= columnCount {
			return opaqueProjectionAlignment(columnCount, lineages, "overlapping_projection_edges")
		}
		aligned[resultIndex] = cloneLineageArms(lineages[lineageIndex].Arms)
	}
	for resultIndex := prefixCount; resultIndex < prefixCount+span; resultIndex++ {
		aligned[resultIndex] = expandWildcardArms(lineages[variadicIndex].Arms, resultColumns[resultIndex])
	}
	for _, arms := range aligned {
		if len(arms) == 0 {
			return opaqueProjectionAlignment(columnCount, lineages, "unoccupied_result_position")
		}
	}
	return aligned
}

func alignMultipleVariadicProjections(
	lineages []model.ProjectionLineage,
	variadicIndexes []int,
	resultColumns []string,
) [][]model.LineageArm {
	columnCount := len(resultColumns)
	firstVariadic := variadicIndexes[0]
	lastVariadic := variadicIndexes[len(variadicIndexes)-1]
	prefixCount := firstVariadic
	suffixCount := len(lineages) - lastVariadic - 1
	if prefixCount+suffixCount > columnCount {
		return opaqueProjectionAlignment(columnCount, lineages, "overlapping_projection_edges")
	}

	aligned := make([][]model.LineageArm, columnCount)
	for index := 0; index < prefixCount; index++ {
		aligned[index] = cloneLineageArms(lineages[index].Arms)
	}
	for suffixOffset := 0; suffixOffset < suffixCount; suffixOffset++ {
		resultIndex := columnCount - suffixCount + suffixOffset
		lineageIndex := lastVariadic + 1 + suffixOffset
		if resultIndex < prefixCount || resultIndex < 0 || resultIndex >= columnCount {
			return opaqueProjectionAlignment(columnCount, lineages, "overlapping_projection_edges")
		}
		aligned[resultIndex] = cloneLineageArms(lineages[lineageIndex].Arms)
	}

	middleRelations := projectionPossibleRelations(lineages[firstVariadic : lastVariadic+1])
	for index := prefixCount; index < columnCount-suffixCount; index++ {
		aligned[index] = []model.LineageArm{{
			Kind:              model.LineageOpaque,
			Operation:         "multiple_wildcard_alignment",
			Status:            model.LineageOpaqueState,
			PossibleRelations: cloneObjectRefs(middleRelations),
		}}
	}
	for _, arms := range aligned {
		if len(arms) == 0 {
			return opaqueProjectionAlignment(columnCount, lineages, "unoccupied_result_position")
		}
	}
	return aligned
}

func expandWildcardArms(arms []model.LineageArm, resultColumn string) []model.LineageArm {
	expanded := make([]model.LineageArm, len(arms))
	for index, arm := range arms {
		cloned := cloneLineageArm(arm)
		if index != 0 || arm.Kind != model.LineageWildcard || arm.Status != model.LineageResolved {
			expanded[index] = opaqueArmFrom(cloned, "wildcard_branch_unknown")
			continue
		}
		relations := possibleRelationsFromArm(cloned)
		if len(relations) != 1 || resultColumn == "" {
			expanded[index] = opaqueArmFrom(cloned, "wildcard_relation_ambiguous")
			continue
		}
		relation := relations[0]
		relation.Alias = ""
		expanded[index] = model.LineageArm{
			Kind:      model.LineageDirect,
			Operation: "wildcard_column",
			Status:    model.LineageResolved,
			Dependencies: []model.ColumnDependency{{
				Origin: model.ColumnOrigin{Relation: relation, Column: resultColumn},
				Role:   model.DependencyValue,
			}},
			PossibleRelations: []model.ObjectRef{relation},
		}
	}
	return expanded
}

func opaqueProjectionAlignment(
	columnCount int,
	lineages []model.ProjectionLineage,
	operation string,
) [][]model.LineageArm {
	if columnCount < 0 {
		columnCount = 0
	}
	relations := projectionPossibleRelations(lineages)
	aligned := make([][]model.LineageArm, columnCount)
	for index := range aligned {
		aligned[index] = []model.LineageArm{{
			Kind:              model.LineageOpaque,
			Operation:         operation,
			Status:            model.LineageOpaqueState,
			PossibleRelations: cloneObjectRefs(relations),
		}}
	}
	return aligned
}

func opaqueArmFrom(arm model.LineageArm, operation string) model.LineageArm {
	return model.LineageArm{
		Kind:              model.LineageOpaque,
		Operation:         operation,
		Status:            model.LineageOpaqueState,
		Dependencies:      append([]model.ColumnDependency(nil), arm.Dependencies...),
		PossibleRelations: possibleRelationsFromArm(arm),
	}
}

func projectionPossibleRelations(lineages []model.ProjectionLineage) []model.ObjectRef {
	seen := make(map[string]struct{})
	relations := make([]model.ObjectRef, 0)
	for _, lineage := range lineages {
		for _, arm := range lineage.Arms {
			for _, relation := range possibleRelationsFromArm(arm) {
				relation.Alias = ""
				key := relation.Schema + "\x00" + relation.Table
				if relation.Table == "" {
					continue
				}
				if _, exists := seen[key]; exists {
					continue
				}
				seen[key] = struct{}{}
				relations = append(relations, relation)
			}
		}
	}
	return relations
}

func possibleRelationsFromArm(arm model.LineageArm) []model.ObjectRef {
	seen := make(map[string]struct{})
	relations := make([]model.ObjectRef, 0, len(arm.PossibleRelations)+len(arm.Dependencies))
	add := func(relation model.ObjectRef) {
		relation.Alias = ""
		if relation.Table == "" {
			return
		}
		key := relation.Schema + "\x00" + relation.Table
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		relations = append(relations, relation)
	}
	for _, relation := range arm.PossibleRelations {
		add(relation)
	}
	for _, dependency := range arm.Dependencies {
		add(dependency.Origin.Relation)
	}
	return relations
}

func cloneLineageArms(arms []model.LineageArm) []model.LineageArm {
	if arms == nil {
		return nil
	}
	cloned := make([]model.LineageArm, len(arms))
	for index, arm := range arms {
		cloned[index] = cloneLineageArm(arm)
	}
	return cloned
}

func cloneLineageArm(arm model.LineageArm) model.LineageArm {
	arm.Dependencies = append([]model.ColumnDependency(nil), arm.Dependencies...)
	arm.PossibleRelations = cloneObjectRefs(arm.PossibleRelations)
	return arm
}

func cloneObjectRefs(relations []model.ObjectRef) []model.ObjectRef {
	return append([]model.ObjectRef(nil), relations...)
}

// validateResultRectangle is provided now so the production switch can reject
// ragged rows before redaction. It is intentionally not wired into Process in
// this implementation step.
func validateResultRectangle(result model.QueryResult) error {
	for rowIndex, row := range result.Rows {
		if len(row) != len(result.Columns) {
			return fmt.Errorf("%w: row %d has %d cells, want %d", errNonRectangularResult, rowIndex, len(row), len(result.Columns))
		}
	}
	return nil
}

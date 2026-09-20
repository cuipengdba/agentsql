package parser

import (
	"fmt"
	"strings"

	"github.com/cuipengdba/agentsql/internal/model"
)

const (
	lineageMaxDepth        = 64
	lineageMaxSetLeaves    = 256
	lineageMaxProjections  = 4096
	lineageMaxDependencies = 16384
	lineageMaxASTNodes     = 50000
)

type bindingKind uint8

const (
	bindingPhysical bindingKind = iota
	bindingCTE
	bindingDerived
	bindingOpaque
)

type namedLineage struct {
	name     string
	lineage  model.ProjectionLineage
	position int
}

type derivedRelation struct {
	outputs     map[string][]model.LineageArm
	outputOrder []namedLineage
	complete    bool
	opaque      model.LineageArm
}

type relationBinding struct {
	visibleName string
	alias       string
	kind        bindingKind
	object      model.ObjectRef
	outputs     map[string][]model.LineageArm
	outputOrder []namedLineage
	complete    bool
	opaque      model.LineageArm
	route       model.LineageRoute
}

type lineageScope struct {
	parent   *lineageScope
	bindings []relationBinding
	byAlias  map[string][]int
	byName   map[string][]int
	ctes     map[string]derivedRelation
}

func newLineageScope(parent *lineageScope) *lineageScope {
	return &lineageScope{
		parent:  parent,
		byAlias: make(map[string][]int),
		byName:  make(map[string][]int),
		ctes:    make(map[string]derivedRelation),
	}
}

// cteOnlyLineageScope preserves CTE visibility while deliberately removing
// relation bindings. It is used for non-LATERAL derived tables and CTE bodies,
// neither of which may capture a sibling FROM binding.
func cteOnlyLineageScope(scope *lineageScope) *lineageScope {
	if scope == nil {
		return nil
	}
	cloned := newLineageScope(cteOnlyLineageScope(scope.parent))
	for key, relation := range scope.ctes {
		cloned.ctes[key] = relation
	}
	return cloned
}

func lineageIdentifierKey(value string) string {
	return strings.TrimSpace(value)
}

func (scope *lineageScope) addBinding(binding relationBinding) {
	if scope == nil {
		return
	}
	index := len(scope.bindings)
	scope.bindings = append(scope.bindings, binding)
	if binding.alias != "" {
		key := lineageIdentifierKey(binding.alias)
		scope.byAlias[key] = append(scope.byAlias[key], index)
		return
	}
	if binding.visibleName != "" {
		key := lineageIdentifierKey(binding.visibleName)
		scope.byName[key] = append(scope.byName[key], index)
	}
}

func (scope *lineageScope) lookupCTE(name string) (derivedRelation, bool) {
	key := lineageIdentifierKey(name)
	for current := scope; current != nil; current = current.parent {
		if relation, exists := current.ctes[key]; exists {
			return relation, true
		}
	}
	return derivedRelation{}, false
}

func resolveQualifiedBinding(scope *lineageScope, schema, qualifier string) ([]relationBinding, bool) {
	qualifierKey := lineageIdentifierKey(qualifier)
	for current := scope; current != nil; current = current.parent {
		indexes := current.byAlias[qualifierKey]
		if len(indexes) > 0 {
			bindings := make([]relationBinding, 0, len(indexes))
			for _, index := range indexes {
				bindings = append(bindings, current.bindings[index])
			}
			return bindings, true
		}

		indexes = current.byName[qualifierKey]
		if len(indexes) == 0 {
			continue
		}
		bindings := make([]relationBinding, 0, len(indexes))
		for _, index := range indexes {
			binding := current.bindings[index]
			if schema != "" && (binding.kind != bindingPhysical || !strings.EqualFold(binding.object.Schema, schema)) {
				continue
			}
			bindings = append(bindings, binding)
		}
		if len(bindings) > 0 {
			return bindings, true
		}
	}
	return nil, false
}

func bindingCanResolveColumn(binding relationBinding, column string) bool {
	if binding.kind == bindingPhysical || binding.kind == bindingOpaque || !binding.complete {
		return true
	}
	return len(binding.outputs[lineageIdentifierKey(column)]) > 0
}

func resolveUnqualifiedBinding(scope *lineageScope, column string) ([]relationBinding, bool) {
	for current := scope; current != nil; current = current.parent {
		// Keep every FROM binding as a candidate. Besides preserving the
		// established DirectProjections fallback contract, this is conservative
		// for catalog-less parsing: a non-physical binding can expose unknown or
		// duplicate output names even when its visible target list looks complete.
		matches := current.bindings
		if len(matches) > 0 {
			return matches, true
		}
		// A local physical/opaque source has an unknown column set. The loop
		// above necessarily included it, so reaching here is safe to continue.
	}
	return nil, false
}

func physicalRelation(object model.ObjectRef) model.ObjectRef {
	return model.ObjectRef{Schema: object.Schema, Table: object.Table}
}

func opaqueArm(operation string, status model.LineageStatus, relations []model.ObjectRef) model.LineageArm {
	if status == "" {
		status = model.LineageOpaqueState
	}
	return model.LineageArm{
		Kind:              model.LineageOpaque,
		Operation:         operation,
		Status:            status,
		PossibleRelations: mergePossibleRelations(relations, nil),
	}
}

func mergePossibleRelations(left, right []model.ObjectRef) []model.ObjectRef {
	seen := make(map[string]struct{}, len(left)+len(right))
	merged := make([]model.ObjectRef, 0, len(left)+len(right))
	for _, relations := range [][]model.ObjectRef{left, right} {
		for _, relation := range relations {
			relation.Alias = ""
			if relation.Table == "" {
				continue
			}
			key := lineageIdentifierKey(relation.Schema) + "\x00" + lineageIdentifierKey(relation.Table)
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			merged = append(merged, relation)
		}
	}
	if len(merged) == 0 {
		return nil
	}
	return merged
}

func possibleRelationsForDependencies(dependencies []model.ColumnDependency) []model.ObjectRef {
	relations := make([]model.ObjectRef, 0, len(dependencies))
	for _, dependency := range dependencies {
		relations = append(relations, dependency.Origin.Relation)
	}
	return mergePossibleRelations(relations, nil)
}

func mergeLineageDependencies(groups ...[]model.ColumnDependency) []model.ColumnDependency {
	seen := make(map[string]struct{})
	merged := make([]model.ColumnDependency, 0)
	for _, group := range groups {
		for _, dependency := range group {
			dependency.Origin.Relation.Alias = ""
			key := lineageIdentifierKey(dependency.Origin.Relation.Schema) + "\x00" +
				lineageIdentifierKey(dependency.Origin.Relation.Table) + "\x00" +
				lineageIdentifierKey(dependency.Origin.Column) + "\x00" +
				fmt.Sprint(dependency.Origin.Route) + "\x00" + string(dependency.Role)
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			merged = append(merged, dependency)
		}
	}
	if len(merged) == 0 {
		return nil
	}
	return merged
}

func addLineageRoute(arms []model.LineageArm, route model.LineageRoute) []model.LineageArm {
	result := make([]model.LineageArm, len(arms))
	for index, arm := range arms {
		arm.Dependencies = append([]model.ColumnDependency(nil), arm.Dependencies...)
		for dependencyIndex := range arm.Dependencies {
			arm.Dependencies[dependencyIndex].Origin.Route |= route
			arm.Dependencies[dependencyIndex].Origin.Relation.Alias = ""
		}
		arm.PossibleRelations = mergePossibleRelations(arm.PossibleRelations, possibleRelationsForDependencies(arm.Dependencies))
		result[index] = arm
	}
	return result
}

func relationFromLineages(lineages []model.ProjectionLineage) derivedRelation {
	relation := derivedRelation{
		outputs: make(map[string][]model.LineageArm), complete: true,
	}
	allRelations := make([]model.ObjectRef, 0)
	for index, lineage := range lineages {
		if lineage.Variadic || lineage.OutputName == "" || len(lineage.Arms) == 0 {
			relation.complete = false
		}
		entry := namedLineage{name: lineage.OutputName, lineage: lineage, position: index}
		relation.outputOrder = append(relation.outputOrder, entry)
		if lineage.OutputName != "" && !lineage.Variadic {
			key := lineageIdentifierKey(lineage.OutputName)
			relation.outputs[key] = append(relation.outputs[key], lineage.Arms...)
		}
		for _, arm := range lineage.Arms {
			allRelations = append(allRelations, arm.PossibleRelations...)
			allRelations = append(allRelations, possibleRelationsForDependencies(arm.Dependencies)...)
		}
	}
	allRelations = mergePossibleRelations(allRelations, nil)
	relation.opaque = opaqueArm("derived_output_unknown", model.LineageOpaqueState, allRelations)
	return relation
}

func overrideDerivedColumns(relation derivedRelation, columns []string) derivedRelation {
	if len(columns) == 0 {
		return relation
	}
	if len(columns) != len(relation.outputOrder) {
		relation.complete = false
		relation.outputs = make(map[string][]model.LineageArm)
		relation.opaque.Operation = "derived_column_count_mismatch"
		return relation
	}
	relation.outputs = make(map[string][]model.LineageArm, len(columns))
	for index, column := range columns {
		entry := relation.outputOrder[index]
		entry.name = column
		entry.lineage.OutputName = column
		relation.outputOrder[index] = entry
		relation.outputs[lineageIdentifierKey(column)] = append(
			relation.outputs[lineageIdentifierKey(column)], entry.lineage.Arms...,
		)
	}
	return relation
}

func deriveDirectProjections(lineages []model.ProjectionLineage) []model.DirectProjectionRef {
	items := make([]directProjectionItem, len(lineages))
	for index, lineage := range lineages {
		if lineage.SetOp != "" {
			return nil
		}
		if lineage.Variadic {
			items[index].star = true
			continue
		}
		if len(lineage.Arms) != 1 {
			continue
		}
		arm := lineage.Arms[0]
		if arm.Kind != model.LineageDirect || arm.Status == model.LineageSourceFree {
			continue
		}
		if len(arm.Dependencies) == 0 {
			if arm.Status != model.LineageResolved && lineage.OutputName != "" {
				items[index].column = lineage.OutputName
			}
			continue
		}
		dependency := arm.Dependencies[0]
		if dependency.Role != model.DependencyValue {
			continue
		}
		if dependency.Origin.Route&model.RouteScalarSubquery != 0 {
			continue
		}
		if dependency.Origin.Route != 0 || arm.Status != model.LineageResolved {
			items[index].column = lineage.OutputName
		} else {
			items[index].column = dependency.Origin.Column
		}
		if items[index].column == "" {
			items[index].column = dependency.Origin.Column
		}
		if arm.Status == model.LineageResolved && dependency.Origin.Route == 0 && len(arm.Dependencies) == 1 {
			items[index].source = physicalRelation(dependency.Origin.Relation)
		}
	}
	return positionDirectProjections(items)
}

func validateLineageLimits(lineages []model.ProjectionLineage) error {
	if len(lineages) > lineageMaxProjections {
		return fmt.Errorf("projection slots %d exceed limit %d", len(lineages), lineageMaxProjections)
	}
	dependencies := make(map[string]struct{})
	for _, lineage := range lineages {
		for _, arm := range lineage.Arms {
			for _, dependency := range arm.Dependencies {
				key := lineageIdentifierKey(dependency.Origin.Relation.Schema) + "\x00" +
					lineageIdentifierKey(dependency.Origin.Relation.Table) + "\x00" +
					lineageIdentifierKey(dependency.Origin.Column) + "\x00" +
					fmt.Sprint(dependency.Origin.Route) + "\x00" + string(dependency.Role)
				dependencies[key] = struct{}{}
				if len(dependencies) > lineageMaxDependencies {
					return fmt.Errorf("lineage dependencies exceed limit %d", lineageMaxDependencies)
				}
			}
		}
	}
	return nil
}

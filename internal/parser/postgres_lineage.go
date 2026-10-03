package parser

import (
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/cuipengdba/agentsql/internal/model"
)

type postgresLineageBuilder struct {
	projectionSlots int
	setLeaves       int
	dependencyKeys  map[string]struct{}
	selectScopes    map[uintptr]*lineageScope
	joinUsing       map[*lineageScope]map[string]struct{}
	joinNatural     map[*lineageScope]bool
}

func postgresProjectionLineages(nodeType string, node any) ([]model.ProjectionLineage, error) {
	if nodeType != "SelectStmt" {
		return nil, nil
	}
	selectNode, ok := node.(map[string]any)
	if !ok {
		return nil, errors.New("PostgreSQL lineage SELECT is malformed")
	}
	if err := postgresValidateLineageStructure(selectNode); err != nil {
		return nil, err
	}
	builder := &postgresLineageBuilder{
		dependencyKeys: make(map[string]struct{}),
		selectScopes:   make(map[uintptr]*lineageScope),
	}
	lineages, err := builder.postgresSelectLineages(selectNode, nil)
	if err != nil {
		return nil, err
	}
	if err := validateLineageLimits(lineages); err != nil {
		return nil, err
	}
	return lineages, nil
}

// PostgreSQL commonly creates many short-lived scopes for UNION branches.
// Keep their indexes lazy so a simple physical FROM source pays only for the
// alias or name index that it actually uses. The shared lineage constructor is
// left unchanged for the other dialects.
func newPostgresLineageScope(parent *lineageScope) *lineageScope {
	return &lineageScope{parent: parent}
}

func postgresCTEOnlyLineageScope(scope *lineageScope) *lineageScope {
	if scope == nil {
		return nil
	}
	parent := postgresCTEOnlyLineageScope(scope.parent)
	if len(scope.ctes) == 0 {
		return parent
	}
	cloned := newPostgresLineageScope(parent)
	cloned.ctes = make(map[string]derivedRelation, len(scope.ctes))
	for key, relation := range scope.ctes {
		cloned.ctes[key] = relation
	}
	return cloned
}

func postgresAddLineageBinding(scope *lineageScope, binding relationBinding) {
	if scope == nil {
		return
	}
	if binding.alias != "" && scope.byAlias == nil {
		scope.byAlias = make(map[string][]int)
	} else if binding.alias == "" && binding.visibleName != "" && scope.byName == nil {
		scope.byName = make(map[string][]int)
	}
	scope.addBinding(binding)
}

func postgresValidateLineageStructure(node any) error {
	nodes := 0
	var walk func(any, int) error
	walk = func(value any, depth int) error {
		switch typed := value.(type) {
		case map[string]any:
			nodes++
			if nodes > lineageMaxASTNodes {
				return fmt.Errorf("AST nodes exceed limit %d", lineageMaxASTNodes)
			}
			for key, child := range typed {
				childDepth := depth
				if postgresNestedQueryBoundary(key) {
					childDepth++
					if childDepth > lineageMaxDepth {
						return fmt.Errorf("lineage nesting depth %d exceeds limit %d", childDepth, lineageMaxDepth)
					}
				}
				if err := walk(child, childDepth); err != nil {
					return err
				}
			}
		case []any:
			for _, child := range typed {
				if err := walk(child, depth); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return walk(node, 0)
}

func (builder *postgresLineageBuilder) postgresSelectLineages(
	selectNode map[string]any,
	parent *lineageScope,
) ([]model.ProjectionLineage, error) {
	if selectNode == nil {
		return nil, errors.New("PostgreSQL lineage SELECT is nil")
	}
	if postgresSetOperation(selectNode) != "" {
		return builder.postgresSetLineages(selectNode, parent)
	}
	scope, err := builder.postgresApplyWith(selectNode["withClause"], parent)
	if err != nil {
		return nil, err
	}
	var cteOnly *lineageScope
	fromClause, _ := selectNode["fromClause"].([]any)
	for _, source := range fromClause {
		if err := builder.postgresAppendLineageBinding(source, scope, &cteOnly); err != nil {
			return nil, err
		}
	}
	builder.selectScopes[reflect.ValueOf(selectNode).Pointer()] = scope

	targets, _ := selectNode["targetList"].([]any)
	if len(targets) == 0 {
		if values, ok := selectNode["valuesLists"].([]any); ok && len(values) > 0 {
			return builder.postgresValuesLineages(values, scope)
		}
		return nil, nil
	}
	builder.projectionSlots += len(targets)
	if builder.projectionSlots > lineageMaxProjections {
		return nil, fmt.Errorf("projection slots exceed limit %d", lineageMaxProjections)
	}

	groupDependencies, groupStatus, groupRelations := builder.postgresClauseDependencies(
		postgresNodeList(selectNode["groupClause"]), scope, selectNode, model.DependencyGroup,
	)
	distinctExpressions := postgresNonEmptyNodes(postgresNodeList(selectNode["distinctClause"]))
	distinctDependencies, distinctStatus, distinctRelations := builder.postgresClauseDependencies(
		distinctExpressions, scope, selectNode, model.DependencyControl,
	)

	lineages := make([]model.ProjectionLineage, 0, len(targets))
	for index, target := range targets {
		lineage := model.ProjectionLineage{SelectIndex: index}
		result, ok := postgresWrappedNode(target, "ResTarget")
		if !ok {
			lineage.Arms = []model.LineageArm{opaqueArm(
				"unsupported_select_expression", model.LineageUnsupported, scopePossibleRelations(scope),
			)}
			lineages = append(lineages, lineage)
			continue
		}
		if name, _ := result["name"].(string); name != "" {
			lineage.OutputName = name
		}
		value := result["val"]
		if name, qualifiers, star := postgresLineageColumn(value); star {
			lineage.Variadic = true
			lineage.Arms = []model.LineageArm{builder.postgresWildcardLineage(qualifiers, scope)}
		} else {
			if lineage.OutputName == "" {
				lineage.OutputName = name
			}
			arm := builder.postgresExprLineage(value, scope, selectNode)
			if arm.Kind == model.LineageAggregate && len(groupDependencies) > 0 {
				arm.Dependencies = mergeLineageDependencies(arm.Dependencies, groupDependencies)
				arm.PossibleRelations = mergePossibleRelations(arm.PossibleRelations, groupRelations)
				arm.Status = mergeLineageStatus(arm.Status, groupStatus)
			}
			if len(distinctExpressions) > 0 {
				arm.Dependencies = mergeLineageDependencies(arm.Dependencies, distinctDependencies)
				arm.PossibleRelations = mergePossibleRelations(arm.PossibleRelations, distinctRelations)
				arm.Status = mergeLineageStatus(arm.Status, distinctStatus)
				arm.Kind = model.LineageComposite
				arm.Operation = "distinct_on"
			}
			lineage.Arms = []model.LineageArm{arm}
		}
		lineages = append(lineages, lineage)
	}
	if err := builder.accountLineages(lineages); err != nil {
		return nil, err
	}
	return lineages, nil
}

func (builder *postgresLineageBuilder) postgresValuesLineages(values []any, scope *lineageScope) ([]model.ProjectionLineage, error) {
	first, _ := postgresUnwrapList(values[0])
	lineages := make([]model.ProjectionLineage, len(first))
	for index, expression := range first {
		lineages[index] = model.ProjectionLineage{
			SelectIndex: index,
			Arms:        []model.LineageArm{builder.postgresExprLineage(expression, scope, nil)},
		}
	}
	for _, row := range values[1:] {
		items, _ := postgresUnwrapList(row)
		if len(items) != len(lineages) {
			return nil, errors.New("PostgreSQL VALUES projection count mismatch")
		}
		for index, expression := range items {
			lineages[index].Arms = append(lineages[index].Arms, builder.postgresExprLineage(expression, scope, nil))
		}
	}
	return lineages, builder.accountLineages(lineages)
}

func (builder *postgresLineageBuilder) postgresApplyWith(value any, parent *lineageScope) (*lineageScope, error) {
	scope := newPostgresLineageScope(parent)
	withClause, ok := value.(map[string]any)
	if !ok {
		return scope, nil
	}
	recursive, _ := withClause["recursive"].(bool)
	ctes, _ := withClause["ctes"].([]any)
	if len(ctes) > 0 {
		scope.ctes = make(map[string]derivedRelation, len(ctes))
	}
	for _, item := range ctes {
		cte, ok := postgresWrappedNode(item, "CommonTableExpr")
		if !ok {
			continue
		}
		name, _ := cte["ctename"].(string)
		key := lineageIdentifierKey(name)
		if recursive {
			scope.ctes[key] = derivedRelation{
				outputs: make(map[string][]model.LineageArm), outputCount: make(map[string]int),
				complete: false, opaque: opaqueArm("recursive_cte", model.LineageOpaqueState, nil),
			}
		}
		query, ok := postgresWrappedNode(cte["ctequery"], "SelectStmt")
		if !ok {
			scope.ctes[key] = derivedRelation{
				outputs: make(map[string][]model.LineageArm), outputCount: make(map[string]int),
				complete: false, opaque: opaqueArm("unsupported_cte_query", model.LineageUnsupported, nil),
			}
			continue
		}
		lineages, err := builder.postgresSelectLineages(query, postgresCTEOnlyLineageScope(scope))
		if err != nil {
			return nil, err
		}
		relation := relationFromLineages(lineages)
		relation = overrideDerivedColumns(relation, postgresStringNodes(cte["aliascolnames"]))
		if recursive {
			postgresSealRecursiveRelation(&relation)
		}
		scope.ctes[key] = relation
	}
	return scope, nil
}

func postgresSealRecursiveRelation(relation *derivedRelation) {
	if relation == nil {
		return
	}
	for index := range relation.outputOrder {
		lineage := &relation.outputOrder[index].lineage
		for armIndex := range lineage.Arms {
			if lineage.Arms[armIndex].Status != model.LineageResolved &&
				lineage.Arms[armIndex].Status != model.LineageSourceFree {
				lineage.Arms[armIndex] = opaqueArm(
					"recursive_cte", model.LineageOpaqueState, lineage.Arms[armIndex].PossibleRelations,
				)
			}
		}
	}
}

func (builder *postgresLineageBuilder) postgresAppendLineageBinding(
	value any,
	scope *lineageScope,
	cteOnly **lineageScope,
) error {
	wrapper, ok := value.(map[string]any)
	if !ok || len(wrapper) == 0 {
		postgresAddLineageBinding(scope, relationBinding{kind: bindingOpaque, opaque: opaqueArm(
			"unsupported_from_item", model.LineageUnsupported, scopePossibleRelations(scope),
		)})
		return nil
	}
	if source, ok := wrapper["RangeVar"].(map[string]any); ok {
		object, valid := postgresRangeVar(source)
		if !valid {
			postgresAddLineageBinding(scope, relationBinding{kind: bindingOpaque, opaque: opaqueArm(
				"malformed_range_var", model.LineageUnsupported, scopePossibleRelations(scope),
			)})
			return nil
		}
		alias := object.Alias
		if object.Schema == "" {
			if relation, exists := scope.lookupCTE(object.Table); exists {
				postgresAddLineageBinding(scope, relationBinding{
					visibleName: object.Table, alias: alias, kind: bindingCTE,
					outputs: relation.outputs, outputCount: relation.outputCount, outputOrder: relation.outputOrder,
					complete: relation.complete, opaque: relation.opaque, route: model.RouteCTE,
				})
				return nil
			}
		}
		postgresAddLineageBinding(scope, relationBinding{
			visibleName: object.Table, alias: alias, kind: bindingPhysical,
			object: object, complete: false,
		})
		return nil
	}
	if source, ok := wrapper["RangeSubselect"].(map[string]any); ok {
		alias := postgresAliasName(source)
		lateral, _ := source["lateral"].(bool)
		if !lateral && *cteOnly == nil {
			*cteOnly = postgresCTEOnlyLineageScope(scope)
		}
		parent := *cteOnly
		route := model.RouteDerived
		if lateral {
			parent = scope
			route |= model.RouteLateral
		}
		query, valid := postgresWrappedNode(source["subquery"], "SelectStmt")
		if !valid {
			postgresAddLineageBinding(scope, relationBinding{alias: alias, kind: bindingOpaque, opaque: opaqueArm(
				"unsupported_derived_query", model.LineageUnsupported, scopePossibleRelations(scope),
			)})
			return nil
		}
		lineages, err := builder.postgresSelectLineages(query, parent)
		if err != nil {
			return err
		}
		if lateral && postgresSelectHasSemanticClauses(query) {
			dependencies, status, relations := builder.postgresSubqueryClauseDependencies(query)
			if status == model.LineageSourceFree {
				status = model.LineageResolved
			}
			for lineageIndex := range lineages {
				for armIndex := range lineages[lineageIndex].Arms {
					arm := &lineages[lineageIndex].Arms[armIndex]
					arm.Kind, arm.Operation = model.LineageComposite, "lateral_subquery"
					arm.Status = mergeLineageStatus(arm.Status, status)
					arm.Dependencies = mergeLineageDependencies(arm.Dependencies, dependencies)
					arm.PossibleRelations = mergePossibleRelations(arm.PossibleRelations, relations)
				}
			}
		}
		relation := relationFromLineages(lineages)
		if aliasValue, ok := source["alias"].(map[string]any); ok {
			relation = overrideDerivedColumns(relation, postgresStringNodes(aliasValue["colnames"]))
		}
		postgresAddLineageBinding(scope, relationBinding{
			alias: alias, kind: bindingDerived, outputs: relation.outputs,
			outputCount: relation.outputCount, outputOrder: relation.outputOrder, complete: relation.complete,
			opaque: relation.opaque, route: route,
		})
		return nil
	}
	if join, ok := wrapper["JoinExpr"].(map[string]any); ok {
		if alias := postgresAliasName(join); alias != "" {
			relations := postgresFromPossibleRelations(join)
			postgresAddLineageBinding(scope, relationBinding{alias: alias, kind: bindingOpaque, opaque: opaqueArm(
				"joined_relation_alias", model.LineageOpaqueState, relations,
			)})
			return nil
		}
		if err := builder.postgresAppendLineageBinding(join["larg"], scope, cteOnly); err != nil {
			return err
		}
		if err := builder.postgresAppendLineageBinding(join["rarg"], scope, cteOnly); err != nil {
			return err
		}
		if natural, _ := join["isNatural"].(bool); natural {
			if builder.joinNatural == nil {
				builder.joinNatural = make(map[*lineageScope]bool)
			}
			builder.joinNatural[scope] = true
		}
		for _, column := range postgresStringNodes(join["usingClause"]) {
			if builder.joinUsing == nil {
				builder.joinUsing = make(map[*lineageScope]map[string]struct{})
			}
			if builder.joinUsing[scope] == nil {
				builder.joinUsing[scope] = make(map[string]struct{})
			}
			builder.joinUsing[scope][lineageIdentifierKey(column)] = struct{}{}
		}
		return nil
	}
	if source, ok := wrapper["RangeFunction"].(map[string]any); ok {
		return builder.postgresAppendOpaqueRangeSource(source, scope, "range_function")
	}
	if source, ok := wrapper["RangeTableFunc"].(map[string]any); ok {
		return builder.postgresAppendOpaqueRangeSource(source, scope, "range_table_function")
	}
	if sample, ok := wrapper["RangeTableSample"].(map[string]any); ok {
		return builder.postgresAppendLineageBinding(sample["relation"], scope, cteOnly)
	}
	postgresAddLineageBinding(scope, relationBinding{alias: postgresAliasName(wrapper), kind: bindingOpaque, opaque: opaqueArm(
		"unsupported_from_source", model.LineageUnsupported, scopePossibleRelations(scope),
	)})
	return nil
}

func (builder *postgresLineageBuilder) postgresAppendOpaqueRangeSource(
	source map[string]any,
	scope *lineageScope,
	operation string,
) error {
	dependencies, status, relations := builder.postgresNodeDependencies(source, scope, nil, model.DependencyValue)
	if lateral, _ := source["lateral"].(bool); lateral {
		dependencies = dependenciesWithRoute(dependencies, model.RouteLateral)
	}
	if status == model.LineageSourceFree {
		status = model.LineageOpaqueState
	}
	arm := opaqueArm(operation, status, relations)
	arm.Dependencies = dependencies
	alias := postgresAliasName(source)
	postgresAddLineageBinding(scope, relationBinding{alias: alias, kind: bindingOpaque, complete: false, opaque: arm})
	return nil
}

func (builder *postgresLineageBuilder) postgresWildcardLineage(qualifiers []string, scope *lineageScope) model.LineageArm {
	var bindings []relationBinding
	if len(qualifiers) > 0 {
		schema, qualifier := postgresQualifierParts(qualifiers)
		bindings, _ = resolveQualifiedBinding(scope, schema, qualifier)
	} else if scope != nil {
		bindings = append(bindings, scope.bindings...)
	}
	if len(bindings) != 1 {
		return opaqueArm("wildcard_relation_ambiguous", model.LineageAmbiguous, bindingsPossibleRelations(bindings))
	}
	relations := bindingPossibleRelations(bindings[0])
	if len(relations) != 1 || bindings[0].kind != bindingPhysical {
		arm := opaqueArm("wildcard_relation_unknown", model.LineageOpaqueState, relations)
		arm.Dependencies = bindings[0].opaque.Dependencies
		return arm
	}
	return model.LineageArm{
		Kind: model.LineageWildcard, Operation: "wildcard", Status: model.LineageResolved,
		PossibleRelations: relations,
	}
}

func (builder *postgresLineageBuilder) postgresExprLineage(
	value any,
	scope *lineageScope,
	selectNode map[string]any,
) model.LineageArm {
	wrapper, ok := value.(map[string]any)
	if !ok || len(wrapper) == 0 {
		return opaqueArm("unsupported:expression", model.LineageUnsupported, scopePossibleRelations(scope))
	}
	if node, ok := wrapper["ColumnRef"].(map[string]any); ok {
		return builder.postgresColumnLineage(node, scope)
	}
	if _, ok := wrapper["A_Const"].(map[string]any); ok {
		return model.LineageArm{Kind: model.LineageConstant, Operation: "constant", Status: model.LineageSourceFree}
	}
	if node, ok := wrapper["CollateClause"].(map[string]any); ok {
		return mysqlTransparentLineage(builder.postgresExprLineage(node["arg"], scope, selectNode), "collate")
	}
	if node, ok := wrapper["RelabelType"].(map[string]any); ok {
		return mysqlTransparentLineage(builder.postgresExprLineage(node["arg"], scope, selectNode), "collate")
	}
	if node, ok := wrapper["CoalesceExpr"].(map[string]any); ok {
		return builder.postgresCoalesceLineage(node, scope, selectNode)
	}
	if node, ok := wrapper["FuncCall"].(map[string]any); ok {
		return builder.postgresFunctionLineage(node, scope, selectNode)
	}
	if node, ok := wrapper["CaseExpr"].(map[string]any); ok {
		return builder.postgresCaseLineage(node, scope, selectNode)
	}
	if node, ok := wrapper["SubLink"].(map[string]any); ok {
		return builder.postgresSubLinkLineage(node, scope)
	}
	if node, ok := wrapper["TypeCast"].(map[string]any); ok {
		return builder.postgresCompositeLineage("cast", node, scope, selectNode)
	}
	if node, ok := wrapper["A_Expr"].(map[string]any); ok {
		return builder.postgresOperatorLineage(node, scope, selectNode)
	}
	if node, ok := wrapper["BoolExpr"].(map[string]any); ok {
		operation := "boolean"
		switch postgresString(node["boolop"]) {
		case "AND_EXPR":
			operation = "and"
		case "OR_EXPR":
			operation = "or"
		case "NOT_EXPR":
			operation = "not"
		}
		return builder.postgresCompositeLineage(operation, node, scope, selectNode)
	}
	known := map[string]string{
		"NullTest": "is", "BooleanTest": "is", "MinMaxExpr": "minmax",
		"NullIfExpr": "nullif", "SQLValueFunction": "sql_value", "ArrayExpr": "array",
		"RowExpr": "row", "ParamRef": "parameter", "SetToDefault": "default",
		"Indirection": "subscript", "A_Indirection": "subscript", "XmlExpr": "xml",
	}
	for nodeType, operation := range known {
		if node, exists := wrapper[nodeType]; exists {
			return builder.postgresCompositeLineage(operation, node, scope, selectNode)
		}
	}
	for nodeType, node := range wrapper {
		dependencies, _, relations := builder.postgresNodeDependencies(node, scope, selectNode, model.DependencyValue)
		if len(dependencies) == 0 && len(relations) == 0 {
			relations = scopePossibleRelations(scope)
		}
		return model.LineageArm{
			Kind: model.LineageOpaque, Operation: "unsupported:" + nodeType,
			Status: model.LineageUnsupported, Dependencies: dependencies, PossibleRelations: relations,
		}
	}
	return opaqueArm("unsupported:empty_expression", model.LineageUnsupported, scopePossibleRelations(scope))
}

func (builder *postgresLineageBuilder) postgresColumnLineage(column map[string]any, scope *lineageScope) model.LineageArm {
	name, qualifiers, star := postgresDirectColumn(column)
	if star || name == "" {
		return opaqueArm("invalid_column", model.LineageUnsupported, scopePossibleRelations(scope))
	}
	var bindings []relationBinding
	var found bool
	if len(qualifiers) == 0 {
		bindings, found = resolveUnqualifiedBinding(scope, name)
		if found && len(bindings) > 1 {
			_, using := builder.joinUsing[scope][lineageIdentifierKey(name)]
			if using || builder.joinNatural[scope] {
				return model.LineageArm{
					Kind: model.LineageOpaque, Operation: "joined_merged_column", Status: model.LineageAmbiguous,
					Dependencies:      []model.ColumnDependency{{Origin: model.ColumnOrigin{Column: name}, Role: model.DependencyValue}},
					PossibleRelations: bindingsPossibleRelations(bindings),
				}
			}
		}
	} else {
		schema, qualifier := postgresQualifierParts(qualifiers)
		bindings, found = resolveQualifiedBinding(scope, schema, qualifier)
	}
	if !found || len(bindings) == 0 {
		return model.LineageArm{
			Kind: model.LineageDirect, Operation: "column", Status: model.LineageOpaqueState,
			Dependencies:      []model.ColumnDependency{{Origin: model.ColumnOrigin{Column: name}, Role: model.DependencyValue}},
			PossibleRelations: scopePossibleRelations(scope),
		}
	}
	if len(bindings) != 1 {
		return model.LineageArm{
			Kind: model.LineageDirect, Operation: "column", Status: model.LineageAmbiguous,
			Dependencies:      []model.ColumnDependency{{Origin: model.ColumnOrigin{Column: name}, Role: model.DependencyValue}},
			PossibleRelations: bindingsPossibleRelations(bindings),
		}
	}
	return postgresBindingColumnLineage(bindings[0], name)
}

func postgresBindingColumnLineage(binding relationBinding, column string) model.LineageArm {
	switch binding.kind {
	case bindingPhysical:
		relation := physicalRelation(binding.object)
		return model.LineageArm{
			Kind: model.LineageDirect, Operation: "column", Status: model.LineageResolved,
			Dependencies: []model.ColumnDependency{{
				Origin: model.ColumnOrigin{Relation: relation, Column: column}, Role: model.DependencyValue,
			}},
			PossibleRelations: []model.ObjectRef{relation},
		}
	case bindingCTE, bindingDerived:
		key := lineageIdentifierKey(column)
		count := binding.outputCount[key]
		if count != 1 {
			status := model.LineageOpaqueState
			if count > 1 {
				status = model.LineageAmbiguous
			}
			arm := binding.opaque
			arm.Kind, arm.Status, arm.Operation = model.LineageDirect, status, "derived_column"
			arm.Dependencies = mergeLineageDependencies(arm.Dependencies, []model.ColumnDependency{{
				Origin: model.ColumnOrigin{Column: column}, Role: model.DependencyValue,
			}})
			return arm
		}
		arms := addLineageRoute(binding.outputs[key], binding.route)
		if len(arms) != 1 {
			return model.LineageArm{
				Kind: model.LineageDirect, Operation: "derived_set_column", Status: model.LineageOpaqueState,
				Dependencies: mergeArmsDependencies(arms), PossibleRelations: armsPossibleRelations(arms),
			}
		}
		return arms[0]
	default:
		arm := binding.opaque
		if arm.Kind == "" {
			arm = opaqueArm("opaque_relation_column", model.LineageOpaqueState, bindingPossibleRelations(binding))
		}
		arm.Dependencies = mergeLineageDependencies(arm.Dependencies, []model.ColumnDependency{{
			Origin: model.ColumnOrigin{Column: column}, Role: model.DependencyValue,
		}})
		return arm
	}
}

func (builder *postgresLineageBuilder) postgresCoalesceLineage(node map[string]any, scope *lineageScope, selectNode map[string]any) model.LineageArm {
	args := postgresNodeList(node["args"])
	var value any
	for _, argument := range args {
		if postgresNullConstant(argument) {
			continue
		}
		if value != nil {
			return builder.postgresCompositeLineage("coalesce", node, scope, selectNode)
		}
		value = argument
	}
	if value == nil {
		return model.LineageArm{Kind: model.LineageConstant, Operation: "constant", Status: model.LineageSourceFree}
	}
	return mysqlTransparentLineage(builder.postgresExprLineage(value, scope, selectNode), "coalesce_null")
}

func (builder *postgresLineageBuilder) postgresOperatorLineage(node map[string]any, scope *lineageScope, selectNode map[string]any) model.LineageArm {
	operator := postgresNameFromNodes(node["name"])
	if operator == "||" {
		if postgresEmptyStringLiteral(node["lexpr"]) {
			return mysqlTransparentLineage(builder.postgresExprLineage(node["rexpr"], scope, selectNode), "concat_empty")
		}
		if postgresEmptyStringLiteral(node["rexpr"]) {
			return mysqlTransparentLineage(builder.postgresExprLineage(node["lexpr"], scope, selectNode), "concat_empty")
		}
		return builder.postgresCompositeLineage("concat", node, scope, selectNode)
	}
	operation := "comparison"
	switch operator {
	case "+", "-", "*", "/", "%", "^":
		operation = "arithmetic"
	case "->", "->>", "#>", "#>>", "@>", "<@", "?", "?|", "?&", "#-", "@?", "@@":
		operation = "json"
	}
	return builder.postgresCompositeLineage(operation, node, scope, selectNode)
}

func (builder *postgresLineageBuilder) postgresCompositeLineage(
	operation string,
	node any,
	scope *lineageScope,
	selectNode map[string]any,
) model.LineageArm {
	dependencies, status, relations := builder.postgresNodeDependencies(node, scope, selectNode, model.DependencyValue)
	if status == model.LineageSourceFree {
		status = model.LineageResolved
	}
	return model.LineageArm{
		Kind: model.LineageComposite, Operation: operation, Status: status,
		Dependencies: dependencies, PossibleRelations: relations,
	}
}

func (builder *postgresLineageBuilder) postgresFunctionLineage(
	function map[string]any,
	scope *lineageScope,
	selectNode map[string]any,
) model.LineageArm {
	name := postgresCanonicalFunctionName(postgresNameFromNodes(function["funcname"]))
	if _, window := function["over"]; window {
		return builder.postgresWindowLineage(name, function, scope, selectNode)
	}
	if postgresAggregateName(name) {
		return builder.postgresAggregateLineage(name, function, scope, selectNode)
	}
	return builder.postgresCompositeLineage(name, function, scope, selectNode)
}

func (builder *postgresLineageBuilder) postgresAggregateLineage(
	name string,
	function map[string]any,
	scope *lineageScope,
	selectNode map[string]any,
) model.LineageArm {
	args := postgresNodeList(function["args"])
	star, _ := function["agg_star"].(bool)
	if name == "count" && (star || len(args) == 0) {
		arm := model.LineageArm{Kind: model.LineageAggregate, Operation: "aggregate:count_star", Status: model.LineageSourceFree}
		return builder.postgresAddAggregateFilter(arm, function, scope, selectNode)
	}
	if name == "count" && len(args) == 1 && postgresSourceFreeExpression(args[0]) {
		arm := model.LineageArm{Kind: model.LineageAggregate, Operation: "aggregate:count_constant", Status: model.LineageSourceFree}
		return builder.postgresAddAggregateFilter(arm, function, scope, selectNode)
	}
	dependencies, status, relations := builder.postgresClauseDependencies(args, scope, selectNode, model.DependencyValue)
	if status == model.LineageSourceFree {
		status = model.LineageResolved
	}
	arm := model.LineageArm{
		Kind: model.LineageAggregate, Operation: "aggregate:" + name, Status: status,
		Dependencies: dependencies, PossibleRelations: relations,
	}
	orderDependencies, orderStatus, orderRelations := builder.postgresClauseDependencies(
		postgresSortExpressions(function["agg_order"]), scope, selectNode, model.DependencyOrder,
	)
	arm.Dependencies = mergeLineageDependencies(arm.Dependencies, orderDependencies)
	arm.PossibleRelations = mergePossibleRelations(arm.PossibleRelations, orderRelations)
	arm.Status = mergeLineageStatus(arm.Status, orderStatus)
	return builder.postgresAddAggregateFilter(arm, function, scope, selectNode)
}

func (builder *postgresLineageBuilder) postgresAddAggregateFilter(
	arm model.LineageArm,
	function map[string]any,
	scope *lineageScope,
	selectNode map[string]any,
) model.LineageArm {
	filter, exists := function["agg_filter"]
	if !exists || filter == nil {
		return arm
	}
	dependencies, status, relations := builder.postgresNodeDependencies(filter, scope, selectNode, model.DependencyFilter)
	arm.Dependencies = mergeLineageDependencies(arm.Dependencies, dependencies)
	arm.PossibleRelations = mergePossibleRelations(arm.PossibleRelations, relations)
	arm.Status = mergeLineageStatus(arm.Status, status)
	if len(dependencies) > 0 && arm.Status == model.LineageSourceFree {
		arm.Status = model.LineageResolved
	}
	return arm
}

func (builder *postgresLineageBuilder) postgresWindowLineage(
	name string,
	function map[string]any,
	scope *lineageScope,
	selectNode map[string]any,
) model.LineageArm {
	dependencies, status, relations := builder.postgresClauseDependencies(
		postgresNodeList(function["args"]), scope, selectNode, model.DependencyValue,
	)
	over, _ := function["over"].(map[string]any)
	over = postgresWindowDefinition(over, selectNode)
	if over != nil {
		groupDependencies, groupStatus, groupRelations := builder.postgresClauseDependencies(
			postgresNodeList(over["partitionClause"]), scope, selectNode, model.DependencyGroup,
		)
		orderDependencies, orderStatus, orderRelations := builder.postgresClauseDependencies(
			postgresSortExpressions(over["orderClause"]), scope, selectNode, model.DependencyOrder,
		)
		frameDependencies, frameStatus, frameRelations := builder.postgresClauseDependencies(
			postgresFrameExpressions(over), scope, selectNode, model.DependencyOrder,
		)
		dependencies = mergeLineageDependencies(dependencies, groupDependencies, orderDependencies, frameDependencies)
		relations = mergePossibleRelations(relations, groupRelations)
		relations = mergePossibleRelations(relations, orderRelations)
		relations = mergePossibleRelations(relations, frameRelations)
		status = mergeLineageStatus(status, mergeLineageStatus(groupStatus, mergeLineageStatus(orderStatus, frameStatus)))
	}
	if filter := function["agg_filter"]; filter != nil {
		filterDependencies, filterStatus, filterRelations := builder.postgresNodeDependencies(
			filter, scope, selectNode, model.DependencyFilter,
		)
		dependencies = mergeLineageDependencies(dependencies, filterDependencies)
		relations = mergePossibleRelations(relations, filterRelations)
		status = mergeLineageStatus(status, filterStatus)
	}
	if status == model.LineageSourceFree {
		status = model.LineageResolved
	}
	return model.LineageArm{
		Kind: model.LineageWindow, Operation: "window:" + name, Status: status,
		Dependencies: dependencies, PossibleRelations: relations,
	}
}

func (builder *postgresLineageBuilder) postgresCaseLineage(
	caseExpression map[string]any,
	scope *lineageScope,
	selectNode map[string]any,
) model.LineageArm {
	dependencies := make([]model.ColumnDependency, 0)
	relations := make([]model.ObjectRef, 0)
	status := model.LineageResolved
	if argument := caseExpression["arg"]; argument != nil {
		arm := builder.postgresExprLineage(argument, scope, selectNode)
		dependencies = append(dependencies, dependenciesWithRole(arm.Dependencies, model.DependencyControl)...)
		relations = append(relations, arm.PossibleRelations...)
		status = mergeLineageStatus(status, arm.Status)
	}
	for _, item := range postgresNodeList(caseExpression["args"]) {
		when, ok := postgresWrappedNode(item, "CaseWhen")
		if !ok {
			continue
		}
		condition := builder.postgresExprLineage(when["expr"], scope, selectNode)
		result := builder.postgresExprLineage(when["result"], scope, selectNode)
		dependencies = append(dependencies, dependenciesWithRole(condition.Dependencies, model.DependencyControl)...)
		dependencies = append(dependencies, result.Dependencies...)
		relations = append(relations, condition.PossibleRelations...)
		relations = append(relations, result.PossibleRelations...)
		status = mergeLineageStatus(status, mergeLineageStatus(condition.Status, result.Status))
	}
	if result := caseExpression["defresult"]; result != nil {
		arm := builder.postgresExprLineage(result, scope, selectNode)
		dependencies = append(dependencies, arm.Dependencies...)
		relations = append(relations, arm.PossibleRelations...)
		status = mergeLineageStatus(status, arm.Status)
	}
	return model.LineageArm{
		Kind: model.LineageComposite, Operation: "case", Status: status,
		Dependencies: mergeLineageDependencies(dependencies), PossibleRelations: mergePossibleRelations(relations, nil),
	}
}

func (builder *postgresLineageBuilder) postgresSubLinkLineage(subLink map[string]any, scope *lineageScope) model.LineageArm {
	typeName, _ := subLink["subLinkType"].(string)
	query, ok := postgresWrappedNode(subLink["subselect"], "SelectStmt")
	if !ok {
		return opaqueArm("scalar_subquery_nil", model.LineageUnsupported, scopePossibleRelations(scope))
	}
	lineages, err := builder.postgresSelectLineages(query, scope)
	if err != nil {
		return opaqueArm("scalar_subquery_error", model.LineageOpaqueState, scopePossibleRelations(scope))
	}
	if typeName == "EXPR_SUBLINK" && len(lineages) == 1 && !lineages[0].Variadic &&
		len(lineages[0].Arms) == 1 && postgresPureScalarSubquery(query) {
		arm := addLineageRoute(lineages[0].Arms, model.RouteScalarSubquery)[0]
		if arm.Kind == model.LineageDirect && arm.Status == model.LineageResolved {
			return arm
		}
	}
	dependencies := make([]model.ColumnDependency, 0)
	relations := make([]model.ObjectRef, 0)
	status := model.LineageResolved
	for _, lineage := range lineages {
		for _, arm := range addLineageRoute(lineage.Arms, model.RouteScalarSubquery) {
			dependencies = append(dependencies, arm.Dependencies...)
			relations = append(relations, arm.PossibleRelations...)
			status = mergeLineageStatus(status, arm.Status)
		}
	}
	clauseDependencies, clauseStatus, clauseRelations := builder.postgresSubqueryClauseDependencies(query)
	dependencies = append(dependencies, dependenciesWithRoute(clauseDependencies, model.RouteScalarSubquery)...)
	relations = append(relations, clauseRelations...)
	status = mergeLineageStatus(status, clauseStatus)
	if status == model.LineageSourceFree {
		status = model.LineageResolved
	}
	operation := "scalar_subquery"
	if typeName != "" && typeName != "EXPR_SUBLINK" {
		operation = "sublink:" + strings.ToLower(strings.TrimSuffix(typeName, "_SUBLINK"))
	}
	return model.LineageArm{
		Kind: model.LineageComposite, Operation: operation, Status: status,
		Dependencies: mergeLineageDependencies(dependencies), PossibleRelations: mergePossibleRelations(relations, nil),
	}
}

func (builder *postgresLineageBuilder) postgresSetLineages(selectNode map[string]any, scope *lineageScope) ([]model.ProjectionLineage, error) {
	leaves := make([][]model.ProjectionLineage, 0, 2)
	operations := make([]string, 0, 2)
	if err := builder.postgresFlattenSet(selectNode, scope, &leaves, &operations); err != nil {
		return nil, err
	}
	if len(leaves) == 0 {
		return nil, errors.New("PostgreSQL set operation has no leaves")
	}
	setOperation := operations[0]
	for _, operation := range operations[1:] {
		if operation != setOperation {
			setOperation = "mixed"
			break
		}
	}
	width := len(leaves[0])
	for _, leaf := range leaves[1:] {
		if len(leaf) != width {
			return nil, fmt.Errorf("set projection count mismatch: got %d, want %d", len(leaf), width)
		}
	}
	merged := make([]model.ProjectionLineage, width)
	for position := 0; position < width; position++ {
		merged[position] = model.ProjectionLineage{
			SelectIndex: position, OutputName: leaves[0][position].OutputName,
			Variadic: leaves[0][position].Variadic, SetOp: setOperation,
		}
		for leafIndex, leaf := range leaves {
			arms := leaf[position].Arms
			if leafIndex > 0 && leaf[position].Variadic {
				arms = []model.LineageArm{opaqueArm(
					"set_branch_wildcard", model.LineageOpaqueState,
					projectionLineageRelations([]model.ProjectionLineage{leaf[position]}),
				)}
			}
			merged[position].Arms = append(merged[position].Arms, arms...)
		}
	}
	return merged, builder.accountLineages(merged)
}

func (builder *postgresLineageBuilder) postgresFlattenSet(
	selectNode map[string]any,
	parent *lineageScope,
	leaves *[][]model.ProjectionLineage,
	operations *[]string,
) error {
	if operation := postgresSetOperation(selectNode); operation != "" {
		scope, err := builder.postgresApplyWith(selectNode["withClause"], parent)
		if err != nil {
			return err
		}
		*operations = append(*operations, operation)
		for _, key := range []string{"larg", "rarg"} {
			child, ok := selectNode[key].(map[string]any)
			if !ok {
				return fmt.Errorf("PostgreSQL set operation missing %s", key)
			}
			if err := builder.postgresFlattenSet(child, scope, leaves, operations); err != nil {
				return err
			}
		}
		return nil
	}
	builder.setLeaves++
	if builder.setLeaves > lineageMaxSetLeaves {
		return fmt.Errorf("set leaves exceed limit %d", lineageMaxSetLeaves)
	}
	lineages, err := builder.postgresSelectLineages(selectNode, parent)
	if err != nil {
		return err
	}
	*leaves = append(*leaves, lineages)
	return nil
}

func (builder *postgresLineageBuilder) postgresNodeDependencies(
	node any,
	scope *lineageScope,
	selectNode map[string]any,
	role model.DependencyRole,
) ([]model.ColumnDependency, model.LineageStatus, []model.ObjectRef) {
	dependencies := make([]model.ColumnDependency, 0)
	relations := make([]model.ObjectRef, 0)
	status := model.LineageSourceFree
	var walk func(any)
	walk = func(value any) {
		switch typed := value.(type) {
		case map[string]any:
			if subLink, ok := typed["SubLink"].(map[string]any); ok {
				arm := builder.postgresSubLinkLineage(subLink, scope)
				dependencies = append(dependencies, dependenciesWithRole(arm.Dependencies, role)...)
				relations = append(relations, arm.PossibleRelations...)
				status = mergeLineageStatus(status, arm.Status)
				return
			}
			if column, ok := typed["ColumnRef"].(map[string]any); ok {
				arm := builder.postgresColumnLineage(column, scope)
				dependencies = append(dependencies, dependenciesWithRole(arm.Dependencies, role)...)
				relations = append(relations, arm.PossibleRelations...)
				status = mergeLineageStatus(status, arm.Status)
				return
			}
			if _, nested := typed["SelectStmt"]; nested {
				return
			}
			for _, child := range typed {
				walk(child)
			}
		case []any:
			for _, child := range typed {
				walk(child)
			}
		}
	}
	walk(node)
	return mergeLineageDependencies(dependencies), status, mergePossibleRelations(relations, nil)
}

func (builder *postgresLineageBuilder) postgresClauseDependencies(
	expressions []any,
	scope *lineageScope,
	selectNode map[string]any,
	role model.DependencyRole,
) ([]model.ColumnDependency, model.LineageStatus, []model.ObjectRef) {
	dependencies := make([]model.ColumnDependency, 0)
	relations := make([]model.ObjectRef, 0)
	status := model.LineageSourceFree
	for _, expression := range expressions {
		arm := builder.postgresExprLineage(postgresSortExpression(expression), scope, selectNode)
		dependencies = append(dependencies, dependenciesWithRole(arm.Dependencies, role)...)
		relations = append(relations, arm.PossibleRelations...)
		status = mergeLineageStatus(status, arm.Status)
	}
	return mergeLineageDependencies(dependencies), status, mergePossibleRelations(relations, nil)
}

func (builder *postgresLineageBuilder) postgresSubqueryClauseDependencies(
	selectNode map[string]any,
) ([]model.ColumnDependency, model.LineageStatus, []model.ObjectRef) {
	scope := builder.selectScopes[reflect.ValueOf(selectNode).Pointer()]
	dependencies := make([]model.ColumnDependency, 0)
	relations := make([]model.ObjectRef, 0)
	status := model.LineageSourceFree
	add := func(expressions []any, role model.DependencyRole) {
		currentDependencies, currentStatus, currentRelations := builder.postgresClauseDependencies(
			expressions, scope, selectNode, role,
		)
		dependencies = append(dependencies, currentDependencies...)
		relations = append(relations, currentRelations...)
		status = mergeLineageStatus(status, currentStatus)
	}
	if where := selectNode["whereClause"]; where != nil {
		add([]any{where}, model.DependencyFilter)
	}
	if having := selectNode["havingClause"]; having != nil {
		add([]any{having}, model.DependencyFilter)
	}
	add(postgresNodeList(selectNode["groupClause"]), model.DependencyGroup)
	add(postgresSortExpressions(selectNode["sortClause"]), model.DependencyOrder)
	return mergeLineageDependencies(dependencies), status, mergePossibleRelations(relations, nil)
}

func (builder *postgresLineageBuilder) accountLineages(lineages []model.ProjectionLineage) error {
	for _, lineage := range lineages {
		for _, arm := range lineage.Arms {
			for _, dependency := range arm.Dependencies {
				key := lineageIdentifierKey(dependency.Origin.Relation.Schema) + "\x00" +
					lineageIdentifierKey(dependency.Origin.Relation.Table) + "\x00" +
					lineageIdentifierKey(dependency.Origin.Column) + "\x00" +
					fmt.Sprint(dependency.Origin.Route) + "\x00" + string(dependency.Role)
				builder.dependencyKeys[key] = struct{}{}
				if len(builder.dependencyKeys) > lineageMaxDependencies {
					return fmt.Errorf("lineage dependencies exceed limit %d", lineageMaxDependencies)
				}
			}
		}
	}
	return nil
}

func postgresSetOperation(selectNode map[string]any) string {
	op, _ := selectNode["op"].(string)
	switch op {
	case "SETOP_UNION":
		if all, _ := selectNode["all"].(bool); all {
			return "union_all"
		}
		return "union_distinct"
	case "SETOP_INTERSECT":
		return "intersect"
	case "SETOP_EXCEPT":
		return "except"
	default:
		return ""
	}
}

func postgresAggregateName(name string) bool {
	_, ok := postgresAggregateFunctions[name]
	if ok {
		return true
	}
	switch name {
	case "json_object_agg", "jsonb_object_agg", "mode", "percentile_cont", "percentile_disc":
		return true
	default:
		return false
	}
}

func postgresCanonicalFunctionName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	if parts := strings.Split(name, "."); len(parts) > 0 {
		name = parts[len(parts)-1]
	}
	switch name {
	case "substring":
		return "substr"
	case "btrim", "ltrim", "rtrim":
		return "trim"
	default:
		return name
	}
}

func postgresWindowDefinition(over map[string]any, selectNode map[string]any) map[string]any {
	if over == nil {
		return nil
	}
	name, _ := over["name"].(string)
	if name == "" || selectNode == nil {
		return over
	}
	for _, item := range postgresNodeList(selectNode["windowClause"]) {
		definition, ok := postgresWrappedNode(item, "WindowDef")
		if ok && postgresString(definition["name"]) == name {
			return definition
		}
	}
	return over
}

func postgresPureScalarSubquery(selectNode map[string]any) bool {
	if selectNode == nil || postgresSetOperation(selectNode) != "" {
		return false
	}
	for _, key := range []string{"whereClause", "havingClause", "limitCount", "limitOffset"} {
		if selectNode[key] != nil {
			return false
		}
	}
	return len(postgresNodeList(selectNode["groupClause"])) == 0 &&
		len(postgresNodeList(selectNode["sortClause"])) == 0 &&
		len(postgresNonEmptyNodes(postgresNodeList(selectNode["distinctClause"]))) == 0
}

func postgresSelectHasSemanticClauses(selectNode map[string]any) bool {
	if !postgresPureScalarSubquery(selectNode) {
		return true
	}
	return false
}

func postgresLineageColumn(value any) (string, []string, bool) {
	wrapper, ok := value.(map[string]any)
	if !ok {
		return "", nil, false
	}
	column, ok := wrapper["ColumnRef"].(map[string]any)
	if !ok {
		return "", nil, false
	}
	return postgresDirectColumn(column)
}

func postgresQualifierParts(qualifiers []string) (string, string) {
	if len(qualifiers) == 0 {
		return "", ""
	}
	qualifier := qualifiers[len(qualifiers)-1]
	schema := ""
	if len(qualifiers) > 1 {
		schema = qualifiers[len(qualifiers)-2]
	}
	return schema, qualifier
}

func postgresWrappedNode(value any, kind string) (map[string]any, bool) {
	wrapper, ok := value.(map[string]any)
	if !ok {
		return nil, false
	}
	node, ok := wrapper[kind].(map[string]any)
	return node, ok
}

func postgresNodeList(value any) []any {
	items, _ := value.([]any)
	return items
}

func postgresUnwrapList(value any) ([]any, bool) {
	if items, ok := value.([]any); ok {
		return items, true
	}
	list, ok := postgresWrappedNode(value, "List")
	if !ok {
		return nil, false
	}
	items, ok := list["items"].([]any)
	return items, ok
}

func postgresStringNodes(value any) []string {
	items := postgresNodeList(value)
	result := make([]string, 0, len(items))
	for _, item := range items {
		if name, ok := postgresStringNode(item); ok {
			result = append(result, name)
		}
	}
	return result
}

func postgresNameFromNodes(value any) string {
	return strings.Join(postgresStringNodes(value), ".")
}

func postgresString(value any) string {
	result, _ := value.(string)
	return result
}

func postgresNonEmptyNodes(values []any) []any {
	result := make([]any, 0, len(values))
	for _, value := range values {
		if object, ok := value.(map[string]any); ok && len(object) == 0 {
			continue
		}
		result = append(result, value)
	}
	return result
}

func postgresNullConstant(value any) bool {
	constant, ok := postgresWrappedNode(value, "A_Const")
	if !ok {
		return false
	}
	isNull, _ := constant["isnull"].(bool)
	return isNull
}

func postgresEmptyStringLiteral(value any) bool {
	constant, ok := postgresWrappedNode(value, "A_Const")
	if !ok {
		return false
	}
	stringValue, ok := constant["sval"].(map[string]any)
	if !ok {
		return false
	}
	valueString, ok := stringValue["sval"].(string)
	return ok && valueString == ""
}

func postgresSourceFreeExpression(value any) bool {
	wrapper, ok := value.(map[string]any)
	if !ok || len(wrapper) == 0 {
		return false
	}
	_, constant := wrapper["A_Const"]
	_, parameter := wrapper["ParamRef"]
	return constant || parameter
}

func postgresSortExpression(value any) any {
	if sortBy, ok := postgresWrappedNode(value, "SortBy"); ok {
		return sortBy["node"]
	}
	return value
}

func postgresSortExpressions(value any) []any {
	items := postgresNodeList(value)
	result := make([]any, 0, len(items))
	for _, item := range items {
		result = append(result, postgresSortExpression(item))
	}
	return result
}

func postgresFrameExpressions(window map[string]any) []any {
	result := make([]any, 0, 2)
	for _, key := range []string{"startOffset", "endOffset"} {
		if value := window[key]; value != nil {
			result = append(result, value)
		}
	}
	return result
}

func postgresFromPossibleRelations(value any) []model.ObjectRef {
	relations := make([]model.ObjectRef, 0)
	walkPostgresNode(value, func(key string, node any) {
		if key != "RangeVar" {
			return
		}
		if object, ok := postgresRangeVar(node); ok {
			relations = append(relations, physicalRelation(object))
		}
	})
	return mergePossibleRelations(relations, nil)
}

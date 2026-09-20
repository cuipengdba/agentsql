package parser

import (
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/cuipengdba/agentsql/internal/model"
	"vitess.io/vitess/go/vt/sqlparser"
)

type mysqlLineageBuilder struct {
	projectionSlots int
	setLeaves       int
	dependencyKeys  map[string]struct{}
	selectScopes    map[*sqlparser.Select]*lineageScope
}

func mysqlProjectionLineages(statement sqlparser.Statement) ([]model.ProjectionLineage, error) {
	if statement == nil {
		return nil, errors.New("MySQL lineage statement is nil")
	}
	if err := mysqlValidateLineageStructure(statement); err != nil {
		return nil, err
	}

	tableStatement, ok := statement.(sqlparser.TableStatement)
	if !ok {
		return nil, nil
	}
	builder := &mysqlLineageBuilder{
		dependencyKeys: make(map[string]struct{}),
		selectScopes:   make(map[*sqlparser.Select]*lineageScope),
	}
	lineages, err := builder.mysqlTableStatementLineages(tableStatement, nil)
	if err != nil {
		return nil, err
	}
	if err := validateLineageLimits(lineages); err != nil {
		return nil, err
	}
	return lineages, nil
}

func mysqlValidateLineageStructure(statement sqlparser.Statement) error {
	nodeCount := 0
	if err := sqlparser.Walk(func(sqlparser.SQLNode) (bool, error) {
		nodeCount++
		if nodeCount > lineageMaxASTNodes {
			return false, fmt.Errorf("AST nodes exceed limit %d", lineageMaxASTNodes)
		}
		return true, nil
	}, statement); err != nil {
		return err
	}
	depth, _ := mysqlQueryComplexity(statement)
	if depth > lineageMaxDepth {
		return fmt.Errorf("lineage nesting depth %d exceeds limit %d", depth, lineageMaxDepth)
	}
	return nil
}

func (builder *mysqlLineageBuilder) mysqlTableStatementLineages(
	statement sqlparser.TableStatement,
	parent *lineageScope,
) ([]model.ProjectionLineage, error) {
	switch typed := statement.(type) {
	case *sqlparser.Select:
		return builder.mysqlSelectLineages(typed, parent)
	case *sqlparser.Union:
		return builder.mysqlSetLineages(typed, parent)
	case *sqlparser.ValuesStatement:
		return nil, errors.New("MySQL VALUES table statement is unsupported")
	case nil:
		return nil, errors.New("MySQL lineage table statement is nil")
	default:
		return nil, fmt.Errorf("unsupported MySQL lineage table statement %T", statement)
	}
}

func (builder *mysqlLineageBuilder) mysqlApplyWith(
	with *sqlparser.With,
	parent *lineageScope,
) (*lineageScope, error) {
	scope := newLineageScope(parent)
	if with == nil {
		return scope, nil
	}
	for _, cte := range with.CTEs {
		if cte == nil {
			continue
		}
		key := lineageIdentifierKey(cte.ID.String())
		if with.Recursive {
			placeholder := derivedRelation{
				outputs:  make(map[string][]model.LineageArm),
				opaque:   opaqueArm("recursive_cte", model.LineageOpaqueState, nil),
				complete: false,
			}
			scope.ctes[key] = placeholder
		}
		definitionScope := cteOnlyLineageScope(scope)
		lineages, err := builder.mysqlTableStatementLineages(cte.Subquery, definitionScope)
		if err != nil {
			return nil, err
		}
		relation := relationFromLineages(lineages)
		columns := make([]string, len(cte.Columns))
		for index, column := range cte.Columns {
			columns[index] = column.String()
		}
		relation = overrideDerivedColumns(relation, columns)
		if with.Recursive {
			for outputIndex := range relation.outputOrder {
				lineage := &relation.outputOrder[outputIndex].lineage
				for armIndex := range lineage.Arms {
					if lineage.Arms[armIndex].Status != model.LineageResolved &&
						lineage.Arms[armIndex].Status != model.LineageSourceFree {
						lineage.Arms[armIndex] = opaqueArm(
							"recursive_cte", model.LineageOpaqueState,
							lineage.Arms[armIndex].PossibleRelations,
						)
					}
				}
			}
		}
		scope.ctes[key] = relation
	}
	return scope, nil
}

func (builder *mysqlLineageBuilder) mysqlSelectLineages(
	selectNode *sqlparser.Select,
	parent *lineageScope,
) ([]model.ProjectionLineage, error) {
	if selectNode == nil {
		return nil, errors.New("MySQL lineage SELECT is nil")
	}
	scope, err := builder.mysqlApplyWith(selectNode.With, parent)
	if err != nil {
		return nil, err
	}
	cteOnly := cteOnlyLineageScope(scope)
	for _, tableExpression := range selectNode.From {
		if err := builder.mysqlAppendLineageBinding(tableExpression, scope, cteOnly); err != nil {
			return nil, err
		}
	}
	if builder.selectScopes == nil {
		builder.selectScopes = make(map[*sqlparser.Select]*lineageScope)
	}
	builder.selectScopes[selectNode] = scope

	selectExpressions := selectNode.GetColumns()
	builder.projectionSlots += len(selectExpressions)
	if builder.projectionSlots > lineageMaxProjections {
		return nil, fmt.Errorf("projection slots exceed limit %d", lineageMaxProjections)
	}
	groupDependencies, groupStatus, groupRelations := mysqlClauseDependencies(mysqlGroupByExprs(selectNode), scope, model.DependencyGroup)
	lineages := make([]model.ProjectionLineage, 0, len(selectExpressions))
	for index, expression := range selectExpressions {
		lineage := model.ProjectionLineage{SelectIndex: index}
		switch typed := expression.(type) {
		case *sqlparser.StarExpr:
			lineage.Variadic = true
			lineage.Arms = []model.LineageArm{mysqlWildcardLineage(typed, scope)}
		case *sqlparser.AliasedExpr:
			if !typed.As.IsEmpty() {
				lineage.OutputName = typed.As.String()
			} else if column, direct := typed.Expr.(*sqlparser.ColName); direct {
				lineage.OutputName = column.Name.String()
			}
			arm := builder.mysqlExprLineage(typed.Expr, scope, selectNode)
			if arm.Kind == model.LineageAggregate && len(groupDependencies) > 0 {
				arm.Dependencies = mergeLineageDependencies(arm.Dependencies, groupDependencies)
				arm.PossibleRelations = mergePossibleRelations(arm.PossibleRelations, groupRelations)
				arm.Status = mergeLineageStatus(arm.Status, groupStatus)
			}
			lineage.Arms = []model.LineageArm{arm}
		default:
			lineage.Arms = []model.LineageArm{opaqueArm(
				"unsupported_select_expression", model.LineageUnsupported, scopePossibleRelations(scope),
			)}
		}
		lineages = append(lineages, lineage)
	}
	if len(lineages) == 0 {
		return nil, errors.New("MySQL SELECT has no lineage projection")
	}
	if err := builder.accountLineages(lineages); err != nil {
		return nil, err
	}
	return lineages, nil
}

func mysqlGroupByExprs(selectNode *sqlparser.Select) []sqlparser.Expr {
	if selectNode == nil || selectNode.GroupBy == nil {
		return nil
	}
	return selectNode.GroupBy.Exprs
}

func (builder *mysqlLineageBuilder) mysqlAppendLineageBinding(
	expression sqlparser.TableExpr,
	scope *lineageScope,
	cteOnly *lineageScope,
) error {
	switch typed := expression.(type) {
	case *sqlparser.AliasedTableExpr:
		if typed == nil {
			return nil
		}
		alias := typed.As.String()
		switch source := typed.Expr.(type) {
		case sqlparser.TableName:
			name := source.Name.String()
			if source.Qualifier.IsEmpty() {
				if relation, exists := scope.lookupCTE(name); exists {
					scope.addBinding(relationBinding{
						visibleName: name, alias: alias, kind: bindingCTE,
						outputs: relation.outputs, outputOrder: relation.outputOrder,
						complete: relation.complete, opaque: relation.opaque, route: model.RouteCTE,
					})
					return nil
				}
				if strings.EqualFold(name, "dual") {
					return nil
				}
			}
			scope.addBinding(relationBinding{
				visibleName: name, alias: alias, kind: bindingPhysical,
				object: model.ObjectRef{Schema: source.Qualifier.String(), Table: name}, complete: false,
			})
		case *sqlparser.DerivedTable:
			visibleParent := cteOnly
			route := model.RouteDerived
			if source.Lateral {
				visibleParent = scope
				route |= model.RouteLateral
			}
			lineages, err := builder.mysqlTableStatementLineages(source.Select, visibleParent)
			if err != nil {
				return err
			}
			if source.Lateral && mysqlTableStatementHasSemanticClauses(source.Select) {
				clauseDependencies, clauseStatus, clauseRelations := builder.mysqlSubqueryClauseDependencies(source.Select)
				if clauseStatus == model.LineageSourceFree {
					clauseStatus = model.LineageResolved
				}
				for lineageIndex := range lineages {
					for armIndex := range lineages[lineageIndex].Arms {
						arm := &lineages[lineageIndex].Arms[armIndex]
						arm.Kind = model.LineageComposite
						arm.Operation = "lateral_subquery"
						arm.Status = mergeLineageStatus(arm.Status, clauseStatus)
						arm.Dependencies = mergeLineageDependencies(arm.Dependencies, clauseDependencies)
						arm.PossibleRelations = mergePossibleRelations(arm.PossibleRelations, clauseRelations)
					}
				}
			}
			relation := relationFromLineages(lineages)
			columns := make([]string, len(typed.Columns))
			for index, column := range typed.Columns {
				columns[index] = column.String()
			}
			relation = overrideDerivedColumns(relation, columns)
			scope.addBinding(relationBinding{
				alias: alias, kind: bindingDerived, outputs: relation.outputs,
				outputOrder: relation.outputOrder, complete: relation.complete,
				opaque: relation.opaque, route: route,
			})
		default:
			scope.addBinding(relationBinding{alias: alias, kind: bindingOpaque, opaque: opaqueArm(
				"unsupported_from_source", model.LineageUnsupported, scopePossibleRelations(scope),
			)})
		}
	case *sqlparser.JoinTableExpr:
		if typed == nil {
			return nil
		}
		if err := builder.mysqlAppendLineageBinding(typed.LeftExpr, scope, cteOnly); err != nil {
			return err
		}
		return builder.mysqlAppendLineageBinding(typed.RightExpr, scope, cteOnly)
	case *sqlparser.ParenTableExpr:
		if typed == nil {
			return nil
		}
		for _, child := range typed.Exprs {
			if err := builder.mysqlAppendLineageBinding(child, scope, cteOnly); err != nil {
				return err
			}
		}
	case *sqlparser.JSONTableExpr:
		arm := builder.mysqlExprLineage(typed.Expr, scope, nil)
		relations := mergePossibleRelations(arm.PossibleRelations, possibleRelationsForDependencies(arm.Dependencies))
		opaque := opaqueArm("json_table", model.LineageOpaqueState, relations)
		opaque.Dependencies = arm.Dependencies
		scope.addBinding(relationBinding{
			alias: typed.Alias.String(), kind: bindingOpaque, complete: false,
			opaque: opaque,
		})
	default:
		scope.addBinding(relationBinding{kind: bindingOpaque, opaque: opaqueArm(
			"unsupported_from_item", model.LineageUnsupported, scopePossibleRelations(scope),
		)})
	}
	return nil
}

func mysqlWildcardLineage(star *sqlparser.StarExpr, scope *lineageScope) model.LineageArm {
	if star == nil {
		return opaqueArm("nil_wildcard", model.LineageUnsupported, scopePossibleRelations(scope))
	}
	var bindings []relationBinding
	if !star.TableName.Name.IsEmpty() {
		bindings, _ = resolveQualifiedBinding(scope, star.TableName.Qualifier.String(), star.TableName.Name.String())
	} else if scope != nil {
		bindings = append(bindings, scope.bindings...)
	}
	if len(bindings) != 1 {
		return opaqueArm("wildcard_relation_ambiguous", model.LineageAmbiguous, bindingsPossibleRelations(bindings))
	}
	relations := bindingPossibleRelations(bindings[0])
	if len(relations) != 1 {
		return opaqueArm("wildcard_relation_unknown", model.LineageOpaqueState, relations)
	}
	return model.LineageArm{
		Kind: model.LineageWildcard, Operation: "wildcard", Status: model.LineageResolved,
		PossibleRelations: relations,
	}
}

func (builder *mysqlLineageBuilder) mysqlExprLineage(
	expression sqlparser.Expr,
	scope *lineageScope,
	selectNode *sqlparser.Select,
) model.LineageArm {
	if expression == nil {
		return opaqueArm("nil_expression", model.LineageUnsupported, scopePossibleRelations(scope))
	}
	switch typed := expression.(type) {
	case *sqlparser.ColName:
		return mysqlColumnLineage(typed, scope)
	case *sqlparser.Literal, *sqlparser.NullVal, sqlparser.BoolVal, *sqlparser.Argument:
		return model.LineageArm{Kind: model.LineageConstant, Operation: "constant", Status: model.LineageSourceFree}
	case *sqlparser.CollateExpr:
		return mysqlTransparentLineage(builder.mysqlExprLineage(typed.Expr, scope, selectNode), "collate")
	case *sqlparser.FuncExpr:
		return builder.mysqlFunctionLineage(typed, scope, selectNode)
	case sqlparser.AggrFunc:
		if over := mysqlAggregateOverClause(typed); over != nil {
			return builder.mysqlWindowAggregateLineage(typed, over, scope, selectNode)
		}
		return builder.mysqlAggregateLineage(typed, scope, selectNode)
	case *sqlparser.ArgumentLessWindowExpr:
		return builder.mysqlWindowLineage(typed.Type.ToString(), nil, typed.OverClause, scope, selectNode)
	case *sqlparser.FirstOrLastValueExpr:
		return builder.mysqlWindowLineage(typed.Type.ToString(), []sqlparser.Expr{typed.Expr}, typed.OverClause, scope, selectNode)
	case *sqlparser.LagLeadExpr:
		return builder.mysqlWindowLineage(typed.Type.ToString(), []sqlparser.Expr{typed.Expr, typed.N, typed.Default}, typed.OverClause, scope, selectNode)
	case *sqlparser.NtileExpr:
		return builder.mysqlWindowLineage("ntile", []sqlparser.Expr{typed.N}, typed.OverClause, scope, selectNode)
	case *sqlparser.NTHValueExpr:
		return builder.mysqlWindowLineage("nth_value", []sqlparser.Expr{typed.Expr, typed.N}, typed.OverClause, scope, selectNode)
	case *sqlparser.CaseExpr:
		return builder.mysqlCaseLineage(typed, scope, selectNode)
	case *sqlparser.Subquery:
		return builder.mysqlScalarSubqueryLineage(typed, scope)
	case *sqlparser.CastExpr:
		return builder.mysqlCompositeLineage("cast", typed, scope, selectNode)
	case *sqlparser.ConvertExpr:
		return builder.mysqlCompositeLineage("convert", typed, scope, selectNode)
	case *sqlparser.ConvertUsingExpr:
		return builder.mysqlCompositeLineage("convert_using", typed, scope, selectNode)
	case *sqlparser.SubstrExpr:
		return builder.mysqlCompositeLineage("substr", typed, scope, selectNode)
	case *sqlparser.TrimFuncExpr:
		return builder.mysqlCompositeLineage("trim", typed, scope, selectNode)
	case *sqlparser.BinaryExpr:
		return builder.mysqlCompositeLineage("binary", typed, scope, selectNode)
	case *sqlparser.UnaryExpr:
		return builder.mysqlCompositeLineage("unary", typed, scope, selectNode)
	case *sqlparser.ComparisonExpr:
		return builder.mysqlCompositeLineage("comparison", typed, scope, selectNode)
	case *sqlparser.AndExpr:
		return builder.mysqlCompositeLineage("and", typed, scope, selectNode)
	case *sqlparser.OrExpr:
		return builder.mysqlCompositeLineage("or", typed, scope, selectNode)
	case *sqlparser.NotExpr:
		return builder.mysqlCompositeLineage("not", typed, scope, selectNode)
	case *sqlparser.IsExpr:
		return builder.mysqlCompositeLineage("is", typed, scope, selectNode)
	case *sqlparser.BetweenExpr:
		return builder.mysqlCompositeLineage("between", typed, scope, selectNode)
	case *sqlparser.IntroducerExpr:
		return builder.mysqlCompositeLineage("introducer", typed, scope, selectNode)
	case *sqlparser.IntervalDateExpr:
		return builder.mysqlCompositeLineage("date_operation", typed, scope, selectNode)
	case *sqlparser.TimestampDiffExpr:
		return builder.mysqlCompositeLineage("timestampdiff", typed, scope, selectNode)
	case *sqlparser.ExtractFuncExpr:
		return builder.mysqlCompositeLineage("extract", typed, scope, selectNode)
	case *sqlparser.JSONExtractExpr:
		return builder.mysqlCompositeLineage("json_extract", typed, scope, selectNode)
	case *sqlparser.JSONValueExpr:
		return builder.mysqlCompositeLineage("json_value", typed, scope, selectNode)
	case *sqlparser.JSONUnquoteExpr:
		return builder.mysqlCompositeLineage("json_unquote", typed, scope, selectNode)
	default:
		return builder.mysqlUnsupportedLineage(typed, scope, selectNode)
	}
}

func mysqlColumnLineage(column *sqlparser.ColName, scope *lineageScope) model.LineageArm {
	if column == nil {
		return opaqueArm("nil_column", model.LineageUnsupported, scopePossibleRelations(scope))
	}
	name := column.Name.String()
	var bindings []relationBinding
	var found bool
	if column.Qualifier.Name.IsEmpty() {
		bindings, found = resolveUnqualifiedBinding(scope, name)
	} else {
		bindings, found = resolveQualifiedBinding(
			scope, column.Qualifier.Qualifier.String(), column.Qualifier.Name.String(),
		)
	}
	if !found || len(bindings) == 0 {
		return model.LineageArm{
			Kind: model.LineageDirect, Operation: "column", Status: model.LineageOpaqueState,
			Dependencies: []model.ColumnDependency{{
				Origin: model.ColumnOrigin{Column: name}, Role: model.DependencyValue,
			}},
			PossibleRelations: scopePossibleRelations(scope),
		}
	}
	if len(bindings) != 1 {
		return model.LineageArm{
			Kind: model.LineageDirect, Operation: "column", Status: model.LineageAmbiguous,
			Dependencies: []model.ColumnDependency{{
				Origin: model.ColumnOrigin{Column: name}, Role: model.DependencyValue,
			}},
			PossibleRelations: bindingsPossibleRelations(bindings),
		}
	}
	return mysqlBindingColumnLineage(bindings[0], name)
}

func mysqlBindingColumnLineage(binding relationBinding, column string) model.LineageArm {
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
		matches := make([]namedLineage, 0, 2)
		for _, output := range binding.outputOrder {
			if strings.EqualFold(output.name, column) {
				matches = append(matches, output)
			}
		}
		if len(matches) != 1 {
			status := model.LineageOpaqueState
			if len(matches) > 1 {
				status = model.LineageAmbiguous
			}
			arm := binding.opaque
			arm.Kind, arm.Status, arm.Operation = model.LineageDirect, status, "derived_column"
			return arm
		}
		arms := addLineageRoute(matches[0].lineage.Arms, binding.route)
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
		arm.Kind = model.LineageDirect
		arm.Dependencies = mergeLineageDependencies(arm.Dependencies, []model.ColumnDependency{{
			Origin: model.ColumnOrigin{Column: column}, Role: model.DependencyValue,
		}})
		return arm
	}
}

func (builder *mysqlLineageBuilder) mysqlFunctionLineage(
	function *sqlparser.FuncExpr,
	scope *lineageScope,
	selectNode *sqlparser.Select,
) model.LineageArm {
	name := strings.ToLower(function.Name.String())
	if mysqlGenericAggregateName(name) {
		dependencies, status, relations := mysqlClauseDependenciesWithBuilder(
			builder, function.Exprs, scope, selectNode, model.DependencyValue,
		)
		if status == model.LineageSourceFree {
			status = model.LineageResolved
		}
		return model.LineageArm{
			Kind: model.LineageAggregate, Operation: "aggregate:" + name, Status: status,
			Dependencies: dependencies, PossibleRelations: relations,
		}
	}
	switch name {
	case "coalesce":
		var value sqlparser.Expr
		for _, argument := range function.Exprs {
			if _, isNull := argument.(*sqlparser.NullVal); isNull {
				continue
			}
			if value != nil {
				return builder.mysqlCompositeLineage(name, function, scope, selectNode)
			}
			value = argument
		}
		if value != nil {
			return mysqlTransparentLineage(builder.mysqlExprLineage(value, scope, selectNode), "coalesce_null")
		}
	case "concat":
		if len(function.Exprs) == 2 {
			if mysqlEmptyStringLiteral(function.Exprs[0]) {
				return mysqlTransparentLineage(builder.mysqlExprLineage(function.Exprs[1], scope, selectNode), "concat_empty")
			}
			if mysqlEmptyStringLiteral(function.Exprs[1]) {
				return mysqlTransparentLineage(builder.mysqlExprLineage(function.Exprs[0], scope, selectNode), "concat_empty")
			}
		}
	}
	return builder.mysqlCompositeLineage(name, function, scope, selectNode)
}

func mysqlTransparentLineage(inner model.LineageArm, operation string) model.LineageArm {
	if inner.Status != model.LineageResolved || inner.Kind != model.LineageDirect ||
		len(inner.Dependencies) != 1 || inner.Dependencies[0].Role != model.DependencyValue {
		inner.Kind = model.LineageComposite
		inner.Operation = operation
		return inner
	}
	inner.Kind = model.LineageTransparent
	inner.Operation = operation
	return inner
}

func (builder *mysqlLineageBuilder) mysqlCompositeLineage(
	operation string,
	node sqlparser.SQLNode,
	scope *lineageScope,
	selectNode *sqlparser.Select,
) model.LineageArm {
	dependencies, status, relations := builder.mysqlNodeDependencies(node, scope, selectNode, model.DependencyValue)
	if status == model.LineageSourceFree {
		status = model.LineageResolved
	}
	return model.LineageArm{
		Kind: model.LineageComposite, Operation: operation, Status: status,
		Dependencies: dependencies, PossibleRelations: relations,
	}
}

func (builder *mysqlLineageBuilder) mysqlUnsupportedLineage(
	node sqlparser.SQLNode,
	scope *lineageScope,
	selectNode *sqlparser.Select,
) model.LineageArm {
	dependencies, _, relations := builder.mysqlNodeDependencies(node, scope, selectNode, model.DependencyValue)
	typeName := reflect.TypeOf(node).String()
	if len(dependencies) == 0 && len(relations) == 0 {
		relations = scopePossibleRelations(scope)
	}
	return model.LineageArm{
		Kind: model.LineageOpaque, Operation: "unsupported:" + typeName,
		Status: model.LineageUnsupported, Dependencies: dependencies,
		PossibleRelations: relations,
	}
}

func (builder *mysqlLineageBuilder) mysqlCaseLineage(
	caseExpression *sqlparser.CaseExpr,
	scope *lineageScope,
	selectNode *sqlparser.Select,
) model.LineageArm {
	dependencies := make([]model.ColumnDependency, 0)
	relations := make([]model.ObjectRef, 0)
	status := model.LineageResolved
	if caseExpression.Expr != nil {
		arm := builder.mysqlExprLineage(caseExpression.Expr, scope, selectNode)
		dependencies = append(dependencies, dependenciesWithRole(arm.Dependencies, model.DependencyControl)...)
		relations = append(relations, arm.PossibleRelations...)
		status = mergeLineageStatus(status, arm.Status)
	}
	for _, when := range caseExpression.Whens {
		if when == nil {
			continue
		}
		condition := builder.mysqlExprLineage(when.Cond, scope, selectNode)
		value := builder.mysqlExprLineage(when.Val, scope, selectNode)
		dependencies = append(dependencies, dependenciesWithRole(condition.Dependencies, model.DependencyControl)...)
		dependencies = append(dependencies, value.Dependencies...)
		relations = append(relations, condition.PossibleRelations...)
		relations = append(relations, value.PossibleRelations...)
		status = mergeLineageStatus(status, mergeLineageStatus(condition.Status, value.Status))
	}
	if caseExpression.Else != nil {
		value := builder.mysqlExprLineage(caseExpression.Else, scope, selectNode)
		dependencies = append(dependencies, value.Dependencies...)
		relations = append(relations, value.PossibleRelations...)
		status = mergeLineageStatus(status, value.Status)
	}
	return model.LineageArm{
		Kind: model.LineageComposite, Operation: "case", Status: status,
		Dependencies: mergeLineageDependencies(dependencies), PossibleRelations: mergePossibleRelations(relations, nil),
	}
}

func (builder *mysqlLineageBuilder) mysqlAggregateLineage(
	aggregate sqlparser.AggrFunc,
	scope *lineageScope,
	selectNode *sqlparser.Select,
) model.LineageArm {
	name := strings.ToLower(aggregate.AggrName())
	if _, star := aggregate.(*sqlparser.CountStar); star {
		return model.LineageArm{
			Kind: model.LineageAggregate, Operation: "aggregate:count_star", Status: model.LineageSourceFree,
		}
	}
	arguments := aggregate.GetArgs()
	if name == "count" && len(arguments) == 1 && mysqlSourceFreeExpression(arguments[0]) {
		return model.LineageArm{
			Kind: model.LineageAggregate, Operation: "aggregate:count_constant", Status: model.LineageSourceFree,
		}
	}
	dependencies, status, relations := mysqlClauseDependenciesWithBuilder(builder, arguments, scope, selectNode, model.DependencyValue)
	if status == model.LineageSourceFree {
		status = model.LineageResolved
	}
	if groupConcat, ok := aggregate.(*sqlparser.GroupConcatExpr); ok {
		orderExpressions := make([]sqlparser.Expr, 0, len(groupConcat.OrderBy))
		for _, order := range groupConcat.OrderBy {
			if order != nil {
				orderExpressions = append(orderExpressions, order.Expr)
			}
		}
		orderDependencies, orderStatus, orderRelations := mysqlClauseDependenciesWithBuilder(
			builder, orderExpressions, scope, selectNode, model.DependencyOrder,
		)
		dependencies = mergeLineageDependencies(dependencies, orderDependencies)
		relations = mergePossibleRelations(relations, orderRelations)
		status = mergeLineageStatus(status, orderStatus)
	}
	return model.LineageArm{
		Kind: model.LineageAggregate, Operation: "aggregate:" + name, Status: status,
		Dependencies: dependencies, PossibleRelations: relations,
	}
}

func (builder *mysqlLineageBuilder) mysqlWindowAggregateLineage(
	aggregate sqlparser.AggrFunc,
	over *sqlparser.OverClause,
	scope *lineageScope,
	selectNode *sqlparser.Select,
) model.LineageArm {
	return builder.mysqlWindowLineage(aggregate.AggrName(), aggregate.GetArgs(), over, scope, selectNode)
}

func (builder *mysqlLineageBuilder) mysqlWindowLineage(
	name string,
	arguments []sqlparser.Expr,
	over *sqlparser.OverClause,
	scope *lineageScope,
	selectNode *sqlparser.Select,
) model.LineageArm {
	dependencies, status, relations := mysqlClauseDependenciesWithBuilder(builder, arguments, scope, selectNode, model.DependencyValue)
	specification := mysqlWindowSpecification(over, selectNode)
	if specification != nil {
		partitionDependencies, partitionStatus, partitionRelations := mysqlClauseDependenciesWithBuilder(
			builder, specification.PartitionClause, scope, selectNode, model.DependencyGroup,
		)
		orderExpressions := make([]sqlparser.Expr, 0, len(specification.OrderClause))
		for _, order := range specification.OrderClause {
			if order != nil {
				orderExpressions = append(orderExpressions, order.Expr)
			}
		}
		orderDependencies, orderStatus, orderRelations := mysqlClauseDependenciesWithBuilder(
			builder, orderExpressions, scope, selectNode, model.DependencyOrder,
		)
		dependencies = mergeLineageDependencies(dependencies, partitionDependencies, orderDependencies)
		relations = mergePossibleRelations(relations, partitionRelations)
		relations = mergePossibleRelations(relations, orderRelations)
		status = mergeLineageStatus(status, mergeLineageStatus(partitionStatus, orderStatus))
		if specification.FrameClause != nil {
			frameExpressions := make([]sqlparser.Expr, 0, 2)
			if specification.FrameClause.Start != nil && specification.FrameClause.Start.Expr != nil {
				frameExpressions = append(frameExpressions, specification.FrameClause.Start.Expr)
			}
			if specification.FrameClause.End != nil && specification.FrameClause.End.Expr != nil {
				frameExpressions = append(frameExpressions, specification.FrameClause.End.Expr)
			}
			frameDependencies, frameStatus, frameRelations := mysqlClauseDependenciesWithBuilder(
				builder, frameExpressions, scope, selectNode, model.DependencyOrder,
			)
			dependencies = mergeLineageDependencies(dependencies, frameDependencies)
			relations = mergePossibleRelations(relations, frameRelations)
			status = mergeLineageStatus(status, frameStatus)
		}
	}
	if status == model.LineageSourceFree {
		status = model.LineageResolved
	}
	return model.LineageArm{
		Kind: model.LineageWindow, Operation: "window:" + strings.ToLower(name), Status: status,
		Dependencies: dependencies, PossibleRelations: relations,
	}
}

func (builder *mysqlLineageBuilder) mysqlScalarSubqueryLineage(
	subquery *sqlparser.Subquery,
	scope *lineageScope,
) model.LineageArm {
	if subquery == nil || subquery.Select == nil {
		return opaqueArm("scalar_subquery_nil", model.LineageUnsupported, scopePossibleRelations(scope))
	}
	lineages, err := builder.mysqlTableStatementLineages(subquery.Select, scope)
	if err != nil {
		return opaqueArm("scalar_subquery_error", model.LineageOpaqueState, scopePossibleRelations(scope))
	}
	if len(lineages) == 1 && !lineages[0].Variadic && len(lineages[0].Arms) == 1 && mysqlPureScalarSubquery(subquery.Select) {
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
	clauseDependencies, clauseStatus, clauseRelations := builder.mysqlSubqueryClauseDependencies(subquery.Select)
	clauseDependencies = dependenciesWithRoute(clauseDependencies, model.RouteScalarSubquery)
	dependencies = append(dependencies, clauseDependencies...)
	relations = append(relations, clauseRelations...)
	status = mergeLineageStatus(status, clauseStatus)
	if status == model.LineageSourceFree {
		status = model.LineageResolved
	}
	return model.LineageArm{
		Kind: model.LineageComposite, Operation: "scalar_subquery", Status: status,
		Dependencies: mergeLineageDependencies(dependencies), PossibleRelations: mergePossibleRelations(relations, nil),
	}
}

func (builder *mysqlLineageBuilder) mysqlSetLineages(
	union *sqlparser.Union,
	parent *lineageScope,
) ([]model.ProjectionLineage, error) {
	if union == nil {
		return nil, errors.New("MySQL lineage UNION is nil")
	}
	scope, err := builder.mysqlApplyWith(union.With, parent)
	if err != nil {
		return nil, err
	}
	leaves := make([][]model.ProjectionLineage, 0, 2)
	operation := "union_all"
	if union.Distinct {
		operation = "union_distinct"
	}
	operations := []string{operation}
	if err := builder.mysqlFlattenSet(union.Left, scope, &leaves, &operations); err != nil {
		return nil, err
	}
	if err := builder.mysqlFlattenSet(union.Right, scope, &leaves, &operations); err != nil {
		return nil, err
	}
	if len(leaves) == 0 {
		return nil, errors.New("MySQL UNION has no leaves")
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
			if position >= len(leaf) {
				merged[position].Arms = append(merged[position].Arms, opaqueArm(
					"set_projection_count_mismatch", model.LineageOpaqueState, projectionLineageRelations(leaf),
				))
				continue
			}
			arms := leaf[position].Arms
			if leafIndex > 0 && leaf[position].Variadic {
				arms = []model.LineageArm{opaqueArm(
					"set_branch_wildcard", model.LineageOpaqueState, projectionLineageRelations([]model.ProjectionLineage{leaf[position]}),
				)}
			}
			merged[position].Arms = append(merged[position].Arms, arms...)
		}
	}
	return merged, nil
}

func (builder *mysqlLineageBuilder) accountLineages(lineages []model.ProjectionLineage) error {
	if builder.dependencyKeys == nil {
		builder.dependencyKeys = make(map[string]struct{})
	}
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

func (builder *mysqlLineageBuilder) mysqlFlattenSet(
	statement sqlparser.TableStatement,
	parent *lineageScope,
	leaves *[][]model.ProjectionLineage,
	operations *[]string,
) error {
	switch typed := statement.(type) {
	case *sqlparser.Union:
		scope := parent
		var err error
		if typed.With != nil {
			scope, err = builder.mysqlApplyWith(typed.With, parent)
			if err != nil {
				return err
			}
		}
		operation := "union_all"
		if typed.Distinct {
			operation = "union_distinct"
		}
		*operations = append(*operations, operation)
		if err := builder.mysqlFlattenSet(typed.Left, scope, leaves, operations); err != nil {
			return err
		}
		return builder.mysqlFlattenSet(typed.Right, scope, leaves, operations)
	case *sqlparser.Select:
		builder.setLeaves++
		if builder.setLeaves > lineageMaxSetLeaves {
			return fmt.Errorf("set leaves exceed limit %d", lineageMaxSetLeaves)
		}
		lineages, err := builder.mysqlSelectLineages(typed, parent)
		if err != nil {
			return err
		}
		*leaves = append(*leaves, lineages)
		return nil
	case *sqlparser.ValuesStatement:
		return errors.New("MySQL VALUES table statement is unsupported")
	default:
		return fmt.Errorf("unsupported MySQL set leaf %T", statement)
	}
}

func (builder *mysqlLineageBuilder) mysqlNodeDependencies(
	node sqlparser.SQLNode,
	scope *lineageScope,
	selectNode *sqlparser.Select,
	role model.DependencyRole,
) ([]model.ColumnDependency, model.LineageStatus, []model.ObjectRef) {
	dependencies := make([]model.ColumnDependency, 0)
	relations := make([]model.ObjectRef, 0)
	status := model.LineageSourceFree
	_ = sqlparser.Walk(func(current sqlparser.SQLNode) (bool, error) {
		if current != node {
			if _, nested := current.(*sqlparser.Subquery); nested {
				return false, nil
			}
		}
		column, ok := current.(*sqlparser.ColName)
		if !ok {
			return true, nil
		}
		arm := mysqlColumnLineage(column, scope)
		dependencies = append(dependencies, dependenciesWithRole(arm.Dependencies, role)...)
		relations = append(relations, arm.PossibleRelations...)
		status = mergeLineageStatus(status, arm.Status)
		return false, nil
	}, node)
	return mergeLineageDependencies(dependencies), status, mergePossibleRelations(relations, nil)
}

func mysqlClauseDependencies(
	expressions []sqlparser.Expr,
	scope *lineageScope,
	role model.DependencyRole,
) ([]model.ColumnDependency, model.LineageStatus, []model.ObjectRef) {
	return mysqlClauseDependenciesWithBuilder(&mysqlLineageBuilder{}, expressions, scope, nil, role)
}

func mysqlClauseDependenciesWithBuilder(
	builder *mysqlLineageBuilder,
	expressions []sqlparser.Expr,
	scope *lineageScope,
	selectNode *sqlparser.Select,
	role model.DependencyRole,
) ([]model.ColumnDependency, model.LineageStatus, []model.ObjectRef) {
	dependencies := make([]model.ColumnDependency, 0)
	relations := make([]model.ObjectRef, 0)
	status := model.LineageSourceFree
	for _, expression := range expressions {
		if expression == nil {
			continue
		}
		arm := builder.mysqlExprLineage(expression, scope, selectNode)
		dependencies = append(dependencies, dependenciesWithRole(arm.Dependencies, role)...)
		relations = append(relations, arm.PossibleRelations...)
		status = mergeLineageStatus(status, arm.Status)
	}
	return mergeLineageDependencies(dependencies), status, mergePossibleRelations(relations, nil)
}

func dependenciesWithRole(dependencies []model.ColumnDependency, role model.DependencyRole) []model.ColumnDependency {
	result := append([]model.ColumnDependency(nil), dependencies...)
	for index := range result {
		if result[index].Role == model.DependencyValue {
			result[index].Role = role
		}
	}
	return result
}

func dependenciesWithRoute(dependencies []model.ColumnDependency, route model.LineageRoute) []model.ColumnDependency {
	result := append([]model.ColumnDependency(nil), dependencies...)
	for index := range result {
		result[index].Origin.Route |= route
	}
	return result
}

func mergeLineageStatus(left, right model.LineageStatus) model.LineageStatus {
	if left == "" {
		return right
	}
	if right == "" {
		return left
	}
	rank := func(status model.LineageStatus) int {
		switch status {
		case model.LineageUnsupported:
			return 5
		case model.LineageOpaqueState:
			return 4
		case model.LineageAmbiguous:
			return 3
		case model.LineageResolved:
			return 2
		case model.LineageSourceFree:
			return 1
		default:
			return 6
		}
	}
	if rank(right) > rank(left) {
		return right
	}
	return left
}

func mysqlEmptyStringLiteral(expression sqlparser.Expr) bool {
	literal, ok := expression.(*sqlparser.Literal)
	return ok && literal.Type == sqlparser.StrVal && literal.Val == ""
}

func mysqlSourceFreeExpression(expression sqlparser.Expr) bool {
	switch expression.(type) {
	case *sqlparser.Literal, *sqlparser.NullVal, sqlparser.BoolVal, *sqlparser.Argument:
		return true
	default:
		return false
	}
}

func mysqlGenericAggregateName(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "array_agg", "string_agg", "json_agg", "median", "percentile", "percentile_cont",
		"percentile_disc", "mode", "bool_and", "bool_or", "every":
		return true
	default:
		return false
	}
}

func (builder *mysqlLineageBuilder) mysqlSubqueryClauseDependencies(
	statement sqlparser.TableStatement,
) ([]model.ColumnDependency, model.LineageStatus, []model.ObjectRef) {
	dependencies := make([]model.ColumnDependency, 0)
	relations := make([]model.ObjectRef, 0)
	status := model.LineageSourceFree
	var collect func(sqlparser.TableStatement)
	collect = func(current sqlparser.TableStatement) {
		switch selectNode := current.(type) {
		case *sqlparser.Select:
			scope := builder.selectScopes[selectNode]
			add := func(expression sqlparser.Expr, role model.DependencyRole) {
				if expression == nil {
					return
				}
				currentDependencies, currentStatus, currentRelations := builder.mysqlNodeDependencies(
					expression, scope, selectNode, role,
				)
				dependencies = append(dependencies, currentDependencies...)
				relations = append(relations, currentRelations...)
				status = mergeLineageStatus(status, currentStatus)
			}
			if selectNode.Where != nil {
				add(selectNode.Where.Expr, model.DependencyFilter)
			}
			if selectNode.Having != nil {
				add(selectNode.Having.Expr, model.DependencyFilter)
			}
			if selectNode.GroupBy != nil {
				for _, expression := range selectNode.GroupBy.Exprs {
					add(expression, model.DependencyGroup)
				}
			}
			for _, order := range selectNode.OrderBy {
				if order != nil {
					add(order.Expr, model.DependencyOrder)
				}
			}
		case *sqlparser.Union:
			collect(selectNode.Left)
			collect(selectNode.Right)
		}
	}
	collect(statement)
	return mergeLineageDependencies(dependencies), status, mergePossibleRelations(relations, nil)
}

func mysqlAggregateOverClause(aggregate sqlparser.AggrFunc) *sqlparser.OverClause {
	switch typed := aggregate.(type) {
	case *sqlparser.Count:
		return typed.OverClause
	case *sqlparser.CountStar:
		return typed.OverClause
	case *sqlparser.Sum:
		return typed.OverClause
	case *sqlparser.Avg:
		return typed.OverClause
	case *sqlparser.Min:
		return typed.OverClause
	case *sqlparser.Max:
		return typed.OverClause
	case *sqlparser.BitAnd:
		return typed.OverClause
	case *sqlparser.BitOr:
		return typed.OverClause
	case *sqlparser.BitXor:
		return typed.OverClause
	case *sqlparser.Std:
		return typed.OverClause
	case *sqlparser.StdDev:
		return typed.OverClause
	case *sqlparser.StdPop:
		return typed.OverClause
	case *sqlparser.StdSamp:
		return typed.OverClause
	case *sqlparser.VarPop:
		return typed.OverClause
	case *sqlparser.VarSamp:
		return typed.OverClause
	case *sqlparser.Variance:
		return typed.OverClause
	case *sqlparser.JSONArrayAgg:
		return typed.OverClause
	case *sqlparser.JSONObjectAgg:
		return typed.OverClause
	default:
		return nil
	}
}

func mysqlWindowSpecification(over *sqlparser.OverClause, selectNode *sqlparser.Select) *sqlparser.WindowSpecification {
	if over == nil {
		return nil
	}
	if over.WindowSpec != nil {
		return over.WindowSpec
	}
	if over.WindowName.IsEmpty() || selectNode == nil {
		return nil
	}
	for _, named := range selectNode.Windows {
		if named == nil {
			continue
		}
		for _, definition := range named.Windows {
			if definition != nil && strings.EqualFold(definition.Name.String(), over.WindowName.String()) {
				return definition.WindowSpec
			}
		}
	}
	return nil
}

func mysqlPureScalarSubquery(statement sqlparser.TableStatement) bool {
	selectNode, ok := statement.(*sqlparser.Select)
	return ok && selectNode.Where == nil && selectNode.Having == nil && selectNode.GroupBy == nil &&
		selectNode.Limit == nil && len(selectNode.OrderBy) == 0 && !selectNode.Distinct
}

func mysqlTableStatementHasSemanticClauses(statement sqlparser.TableStatement) bool {
	switch selectNode := statement.(type) {
	case *sqlparser.Select:
		if selectNode.Where != nil || selectNode.Having != nil || selectNode.GroupBy != nil ||
			selectNode.Limit != nil || len(selectNode.OrderBy) > 0 || selectNode.Distinct {
			return true
		}
		return false
	case *sqlparser.Union:
		return selectNode.Limit != nil || len(selectNode.OrderBy) > 0 ||
			mysqlTableStatementHasSemanticClauses(selectNode.Left) ||
			mysqlTableStatementHasSemanticClauses(selectNode.Right)
	default:
		return true
	}
}

func bindingPossibleRelations(binding relationBinding) []model.ObjectRef {
	if binding.kind == bindingPhysical {
		return []model.ObjectRef{physicalRelation(binding.object)}
	}
	relations := binding.opaque.PossibleRelations
	for _, output := range binding.outputOrder {
		relations = append(relations, projectionLineageRelations([]model.ProjectionLineage{output.lineage})...)
	}
	return mergePossibleRelations(relations, nil)
}

func bindingsPossibleRelations(bindings []relationBinding) []model.ObjectRef {
	var relations []model.ObjectRef
	for _, binding := range bindings {
		relations = append(relations, bindingPossibleRelations(binding)...)
	}
	return mergePossibleRelations(relations, nil)
}

func scopePossibleRelations(scope *lineageScope) []model.ObjectRef {
	if scope == nil {
		return nil
	}
	return bindingsPossibleRelations(scope.bindings)
}

func mergeArmsDependencies(arms []model.LineageArm) []model.ColumnDependency {
	var dependencies []model.ColumnDependency
	for _, arm := range arms {
		dependencies = mergeLineageDependencies(dependencies, arm.Dependencies)
	}
	return dependencies
}

func armsPossibleRelations(arms []model.LineageArm) []model.ObjectRef {
	var relations []model.ObjectRef
	for _, arm := range arms {
		relations = append(relations, arm.PossibleRelations...)
		relations = append(relations, possibleRelationsForDependencies(arm.Dependencies)...)
	}
	return mergePossibleRelations(relations, nil)
}

func projectionLineageRelations(lineages []model.ProjectionLineage) []model.ObjectRef {
	var relations []model.ObjectRef
	for _, lineage := range lineages {
		relations = append(relations, armsPossibleRelations(lineage.Arms)...)
	}
	return mergePossibleRelations(relations, nil)
}

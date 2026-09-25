package businessdb

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"

	closedparser "github.com/cuipengdba/agentsql/internal/parser"
	"github.com/jackc/pgx/v5"
)

const closedSelectMaxBindRetries = 1

type closedOperatorIdentity struct {
	OID         uint32
	FunctionOID uint32
	ResultType  uint32
}

type closedOperatorLookup func(context.Context, string, uint32, uint32, PostgresCatalogBudget) (closedOperatorIdentity, error)

type closedResolvedType struct {
	OID       uint32
	Collation uint32
}

type closedSelectBinding struct {
	relation PostgresRelationIdentity
	columns  map[string]PostgresColumnIdentity
	visible  string
	aliased  bool
	path     string
}

type closedSelectScope struct {
	bindings []*closedSelectBinding
	parent   *closedSelectScope
}

type closedSelectResolver struct {
	ctx                context.Context
	frame              PostgresCatalogFrame
	datasourceIdentity string
	budget             PostgresCatalogBudget
	lookup             closedOperatorLookup
	relationsByName    map[string]PostgresRelationIdentity
	columnsByRelation  map[uint32]map[string]PostgresColumnIdentity
	firstPathByOID     map[uint32]string
	relationSeen       map[uint32]struct{}
	columnSeen         map[string]struct{}
	objectSeen         map[string]struct{}
	facts              SemanticFacts
}

// BindClosedSelect runs the feature-off CATALOG_CLOSED_V1 SELECT lifecycle.
// The candidate parse and catalog frame are hints only: the raw SQL is parsed
// and resolved again after ordered relation locks and Fpre are established.
func (executor *PostgresExecutor) BindClosedSelect(ctx context.Context, request BindRequest, budget PostgresCatalogBudget) (*PostgresClosedPrepared, error) {
	if executor == nil || ctx == nil || budget == nil || request.Identity.DatasourceIdentity == "" {
		return nil, NewCatalogFailure("AUTH_CATALOG_INCOMPLETE")
	}
	parsed, err := parseClosedSelectForBinding(request.RawSQL, budget)
	if err != nil {
		return nil, err
	}
	refs := closedSelectRelationRefs(parsed)
	if len(refs) == 0 {
		return nil, NewPrecisionFailure(BinderCodeModeRequired)
	}
	for attempt := 0; attempt <= closedSelectMaxBindRetries; attempt++ {
		candidate, err := executor.DiscoverClosedCatalog(ctx, refs, budget)
		if err != nil {
			return nil, err
		}
		if !closedRequestIdentityMatches(request.Identity, candidate.Identity) {
			return nil, NewIdentityDriftFailure()
		}
		prepared, err := executor.prepareClosedCatalogResolved(ctx, request.RawSQL, candidate, budget,
			func(bindContext context.Context, tx pgx.Tx, frame PostgresCatalogFrame, bindBudget PostgresCatalogBudget) (SemanticFacts, error) {
				lockedAST, parseErr := parseClosedSelectForBinding(request.RawSQL, bindBudget)
				if parseErr != nil {
					return SemanticFacts{}, parseErr
				}
				if !sameClosedRelationRefs(refs, closedSelectRelationRefs(lockedAST)) {
					return SemanticFacts{}, NewCatalogFailure("AUTH_CATALOG_RACE")
				}
				return resolvePostgresClosedSelect(bindContext, lockedAST, frame, request.Identity.DatasourceIdentity,
					func(operatorContext context.Context, name string, left, right uint32, operatorBudget PostgresCatalogBudget) (closedOperatorIdentity, error) {
						return lookupClosedOperator(operatorContext, tx, name, left, right, operatorBudget)
					}, bindBudget)
			})
		if err == nil {
			return prepared, nil
		}
		if attempt == closedSelectMaxBindRetries || !closedRetryableBindError(err) {
			return nil, err
		}
	}
	return nil, NewCatalogFailure("AUTH_CATALOG_RACE")
}

func parseClosedSelectForBinding(rawSQL string, budget PostgresCatalogBudget) (*closedparser.PostgresClosedSelect, error) {
	if strings.TrimSpace(rawSQL) == "" {
		return nil, NewCapabilityFailure(BinderCodeModeUnsupported)
	}
	if err := budget.ChargeBinderBytes(len(rawSQL)); err != nil {
		return nil, err
	}
	parsed, err := closedparser.ParsePostgresClosedSelect(rawSQL)
	if err != nil {
		return nil, mapClosedParserError(err)
	}
	if err := budget.ChargeNodes(parsed.NodeCount); err != nil {
		return nil, err
	}
	if err := budget.ChargeWork(parsed.NodeCount + parsed.MaxDepth); err != nil {
		return nil, err
	}
	return parsed, nil
}

func mapClosedParserError(err error) error {
	kind, ok := closedparser.PostgresClosedErrorClass(err)
	if !ok {
		return NewCapabilityFailure(BinderCodeModeUnsupported)
	}
	switch kind {
	case closedparser.PostgresClosedColumnShape:
		return NewPrecisionFailure("AUTH_COLUMN_SHAPE_UNSUPPORTED")
	case closedparser.PostgresClosedModeRequired:
		return NewPrecisionFailure(BinderCodeModeRequired)
	default:
		return NewCapabilityFailure(BinderCodeModeUnsupported)
	}
}

func closedSelectRelationRefs(parsed *closedparser.PostgresClosedSelect) []ClosedRelationRef {
	if parsed == nil {
		return nil
	}
	seen := make(map[string]struct{}, len(parsed.Relations))
	refs := make([]ClosedRelationRef, 0, len(parsed.Relations))
	for _, relation := range parsed.Relations {
		key := relation.Schema + "\x00" + relation.Name
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		refs = append(refs, ClosedRelationRef{Schema: relation.Schema, Name: relation.Name})
	}
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].Schema == refs[j].Schema {
			return refs[i].Name < refs[j].Name
		}
		return refs[i].Schema < refs[j].Schema
	})
	return refs
}

func sameClosedRelationRefs(left, right []ClosedRelationRef) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func closedRequestIdentityMatches(request, actual SemanticIdentity) bool {
	return (request.DatabaseOID == 0 || request.DatabaseOID == actual.DatabaseOID) &&
		(request.SessionUser == "" || request.SessionUser == actual.SessionUser) &&
		(request.CurrentUser == "" || request.CurrentUser == actual.CurrentUser) &&
		(request.RoleOID == 0 || request.RoleOID == actual.RoleOID) &&
		(request.FixedSearchPath == "" || request.FixedSearchPath == actual.FixedSearchPath) &&
		(request.SearchPathDigest == "" || request.SearchPathDigest == actual.SearchPathDigest)
}

func closedRetryableBindError(err error) bool {
	var binderErr *BinderError
	if !errors.As(err, &binderErr) {
		return false
	}
	return binderErr.Code == "AUTH_CATALOG_RACE" || binderErr.Code == "AUTH_BIND_CLOSURE_MISMATCH"
}

func resolvePostgresClosedSelect(ctx context.Context, parsed *closedparser.PostgresClosedSelect, frame PostgresCatalogFrame,
	datasourceIdentity string, lookup closedOperatorLookup, budget PostgresCatalogBudget) (SemanticFacts, error) {
	if ctx == nil || parsed == nil || frame.Fingerprint == "" || frame.DatabaseOID == 0 ||
		datasourceIdentity == "" || lookup == nil || budget == nil {
		return SemanticFacts{}, NewCatalogFailure("AUTH_CATALOG_INCOMPLETE")
	}
	resolver := &closedSelectResolver{ctx: ctx, frame: frame, datasourceIdentity: datasourceIdentity, budget: budget, lookup: lookup,
		relationsByName:   make(map[string]PostgresRelationIdentity, len(frame.Relations)),
		columnsByRelation: make(map[uint32]map[string]PostgresColumnIdentity, len(frame.Relations)),
		firstPathByOID:    make(map[uint32]string), relationSeen: make(map[uint32]struct{}),
		columnSeen: make(map[string]struct{}), objectSeen: make(map[string]struct{})}
	for _, relation := range frame.Relations {
		resolver.relationsByName[relation.Schema+"\x00"+relation.Name] = relation
		resolver.columnsByRelation[relation.OID] = make(map[string]PostgresColumnIdentity)
	}
	for _, column := range frame.Columns {
		columns, ok := resolver.columnsByRelation[column.RelationOID]
		if !ok || column.Attnum <= 0 || column.Name == "" {
			return SemanticFacts{}, NewCatalogFailure("AUTH_CATALOG_INCOMPLETE")
		}
		columns[column.Name] = column
	}
	resolver.facts = SemanticFacts{Schema: SemanticFactsSchemaID, SchemaVersion: SemanticFactsVersion,
		StatementClass: BinderStatementSelect, Identity: SemanticIdentity{DatasourceIdentity: datasourceIdentity,
			DatabaseOID: frame.DatabaseOID, CatalogDigest: frame.Fingerprint}}
	if _, err := resolver.resolveQuery(parsed, nil, "query", 0); err != nil {
		return SemanticFacts{}, err
	}
	for index := range resolver.facts.Relations {
		resolver.facts.Relations[index].BindingAlias = resolver.firstPathByOID[resolver.facts.Relations[index].RelationOID]
	}
	for index := range resolver.facts.ColumnUses {
		resolver.facts.ColumnUses[index].BindingAlias = resolver.firstPathByOID[resolver.facts.ColumnUses[index].RelationOID]
	}
	return resolver.facts, nil
}

func (resolver *closedSelectResolver) resolveQuery(query *closedparser.PostgresClosedSelect, parent *closedSelectScope,
	path string, queryDepth int) ([]closedResolvedType, error) {
	if query == nil || query.From == nil || len(query.Targets) == 0 {
		return nil, NewPrecisionFailure(BinderCodeModeRequired)
	}
	scope := &closedSelectScope{parent: parent}
	rteIndex := 0
	var joinConditions []*closedparser.PostgresClosedExpr
	if err := resolver.addFromBindings(query.From, scope, path, &rteIndex, &joinConditions); err != nil {
		return nil, err
	}
	visible := make(map[string]struct{}, len(scope.bindings))
	for _, binding := range scope.bindings {
		if _, duplicate := visible[binding.visible]; duplicate {
			return nil, NewPrecisionFailure(BinderCodeModeRequired)
		}
		visible[binding.visible] = struct{}{}
	}
	for _, condition := range joinConditions {
		valueType, err := resolver.resolveExpr(condition, scope, SemanticUsageReference, "join_where", -1, queryDepth)
		if err != nil {
			return nil, err
		}
		if valueType.OID != 16 {
			return nil, NewPrecisionFailure(BinderCodeModeRequired)
		}
	}
	if query.Where != nil {
		valueType, err := resolver.resolveExpr(query.Where, scope, SemanticUsageReference, "join_where", -1, queryDepth)
		if err != nil {
			return nil, err
		}
		if valueType.OID != 16 {
			return nil, NewPrecisionFailure(BinderCodeModeRequired)
		}
	}
	result := make([]closedResolvedType, 0, len(query.Targets))
	for index, target := range query.Targets {
		usage, site, outputIndex := SemanticUsageOutput, "target."+strconv.Itoa(index+1), index
		if queryDepth > 0 {
			usage, site, outputIndex = SemanticUsageReference, "subquery", -1
		}
		valueType, err := resolver.resolveExpr(target, scope, usage, site, outputIndex, queryDepth)
		if err != nil {
			return nil, err
		}
		if valueType.OID == 0 || valueType.OID == 705 {
			return nil, NewPrecisionFailure(BinderCodeModeRequired)
		}
		result = append(result, valueType)
	}
	return result, nil
}

func (resolver *closedSelectResolver) addFromBindings(from *closedparser.PostgresClosedFrom, scope *closedSelectScope,
	path string, rteIndex *int, conditions *[]*closedparser.PostgresClosedExpr) error {
	if from == nil {
		return NewPrecisionFailure(BinderCodeModeRequired)
	}
	if from.Relation != nil {
		*rteIndex++
		relation, ok := resolver.relationsByName[from.Relation.Schema+"\x00"+from.Relation.Name]
		if !ok {
			return NewCatalogFailure("AUTH_CATALOG_INCOMPLETE")
		}
		visible := from.Relation.Name
		aliased := from.Relation.Alias != ""
		if aliased {
			visible = from.Relation.Alias
		}
		binding := &closedSelectBinding{relation: relation, columns: resolver.columnsByRelation[relation.OID],
			visible: visible, aliased: aliased, path: path + ".rte" + strconv.Itoa(*rteIndex)}
		scope.bindings = append(scope.bindings, binding)
		if _, exists := resolver.firstPathByOID[relation.OID]; !exists {
			resolver.firstPathByOID[relation.OID] = binding.path
		}
		if _, exists := resolver.relationSeen[relation.OID]; !exists {
			resolver.relationSeen[relation.OID] = struct{}{}
			resolver.facts.Relations = append(resolver.facts.Relations, SemanticRelation{DatasourceID: resolver.datasourceIdentity,
				DatabaseOID: relation.DatabaseOID, RelationOID: relation.OID, NamespaceOID: relation.NamespaceOID,
				Schema: relation.Schema, Name: relation.Name, Kind: relation.Kind, Persistence: relation.Persistence,
				CatalogFingerprint: resolver.frame.Fingerprint})
		}
		return nil
	}
	if from.Join == nil || from.Join.Condition == nil {
		return NewPrecisionFailure(BinderCodeModeRequired)
	}
	if err := resolver.addFromBindings(from.Join.Left, scope, path, rteIndex, conditions); err != nil {
		return err
	}
	if err := resolver.addFromBindings(from.Join.Right, scope, path, rteIndex, conditions); err != nil {
		return err
	}
	*rteIndex++ // analyzed PostgreSQL rtable contains one RTE_JOIN after its inputs
	*conditions = append(*conditions, from.Join.Condition)
	return nil
}

func (resolver *closedSelectResolver) resolveExpr(expression *closedparser.PostgresClosedExpr, scope *closedSelectScope,
	usage SemanticUsage, site string, outputIndex, queryDepth int) (closedResolvedType, error) {
	if expression == nil {
		return closedResolvedType{}, NewPrecisionFailure(BinderCodeModeRequired)
	}
	switch expression.Kind {
	case closedparser.PostgresClosedColumn:
		binding, column, err := resolver.resolveColumn(scope, expression.Column)
		if err != nil {
			return closedResolvedType{}, err
		}
		if !closedBuiltinScalarType(column.TypeOID) || column.Collation >= 16384 {
			return closedResolvedType{}, NewPrecisionFailure("AUTH_COLUMN_SHAPE_UNSUPPORTED")
		}
		resolver.addObject("type", column.TypeOID)
		resolver.addObject("collation", column.Collation)
		resolver.addColumnUse(binding, column, usage, site, outputIndex)
		return closedResolvedType{OID: column.TypeOID, Collation: column.Collation}, nil
	case closedparser.PostgresClosedInteger:
		if expression.Integer < -2147483648 || expression.Integer > 2147483647 {
			return closedResolvedType{}, NewPrecisionFailure(BinderCodeModeRequired)
		}
		resolver.addObject("type", 23)
		return closedResolvedType{OID: 23}, nil
	case closedparser.PostgresClosedBoolean:
		resolver.addObject("type", 16)
		return closedResolvedType{OID: 16}, nil
	case closedparser.PostgresClosedNull:
		resolver.addObject("type", 705)
		return closedResolvedType{OID: 705}, nil
	case closedparser.PostgresClosedOperator:
		return resolver.resolveOperatorExpr(expression, scope, usage, site, outputIndex, queryDepth)
	case closedparser.PostgresClosedBoolExpr:
		for _, argument := range expression.Args {
			argumentType, err := resolver.resolveExpr(argument, scope, usage, site, outputIndex, queryDepth)
			if err != nil {
				return closedResolvedType{}, err
			}
			if argumentType.OID != 16 {
				return closedResolvedType{}, NewPrecisionFailure(BinderCodeModeRequired)
			}
		}
		resolver.addObject("type", 16)
		return closedResolvedType{OID: 16}, nil
	case closedparser.PostgresClosedNullTest:
		if len(expression.Args) != 1 {
			return closedResolvedType{}, NewPrecisionFailure(BinderCodeModeRequired)
		}
		if _, err := resolver.resolveExpr(expression.Args[0], scope, usage, site, outputIndex, queryDepth); err != nil {
			return closedResolvedType{}, err
		}
		resolver.addObject("type", 16)
		return closedResolvedType{OID: 16}, nil
	case closedparser.PostgresClosedSubquery:
		return resolver.resolveSubquery(expression, scope, usage, site, outputIndex, queryDepth)
	default:
		return closedResolvedType{}, NewPrecisionFailure(BinderCodeModeRequired)
	}
}

func (resolver *closedSelectResolver) resolveOperatorExpr(expression *closedparser.PostgresClosedExpr, scope *closedSelectScope,
	usage SemanticUsage, site string, outputIndex, queryDepth int) (closedResolvedType, error) {
	if len(expression.Args) != 2 {
		return closedResolvedType{}, NewPrecisionFailure(BinderCodeModeRequired)
	}
	left, err := resolver.resolveExpr(expression.Args[0], scope, usage, site, outputIndex, queryDepth)
	if err != nil {
		return closedResolvedType{}, err
	}
	right, err := resolver.resolveExpr(expression.Args[1], scope, usage, site, outputIndex, queryDepth)
	if err != nil {
		return closedResolvedType{}, err
	}
	if left.OID == 0 || left.OID == 705 || left.OID != right.OID || left.Collation != right.Collation {
		return closedResolvedType{}, NewPrecisionFailure(BinderCodeModeRequired)
	}
	name := expression.Operator
	if name == "!=" {
		name = "<>"
	}
	expected := left.OID
	if closedComparisonOperator(name) {
		expected = 16
	} else if !closedArithmeticOperator(name) || !closedArithmeticType(left.OID) {
		return closedResolvedType{}, NewPrecisionFailure(BinderCodeModeRequired)
	}
	operator, err := resolver.lookup(resolver.ctx, name, left.OID, right.OID, resolver.budget)
	if err != nil {
		return closedResolvedType{}, err
	}
	if operator.OID == 0 || operator.FunctionOID == 0 || operator.ResultType != expected || !closedBuiltinScalarType(operator.ResultType) {
		return closedResolvedType{}, NewPrecisionFailure(BinderCodeModeRequired)
	}
	resolver.addObject("operator", operator.OID)
	resolver.addObject("function", operator.FunctionOID)
	resolver.addObject("type", operator.ResultType)
	resultCollation := uint32(0)
	if operator.ResultType == left.OID {
		resultCollation = left.Collation
	}
	return closedResolvedType{OID: operator.ResultType, Collation: resultCollation}, nil
}

func (resolver *closedSelectResolver) resolveSubquery(expression *closedparser.PostgresClosedExpr, scope *closedSelectScope,
	usage SemanticUsage, site string, outputIndex, queryDepth int) (closedResolvedType, error) {
	resolver.addObject("type", 16)
	if expression.Subquery == nil {
		return closedResolvedType{}, NewPrecisionFailure(BinderCodeModeRequired)
	}
	switch expression.SubLinkType {
	case "EXISTS_SUBLINK":
		if _, err := resolver.resolveQuery(expression.Subquery, scope, "expression_query", queryDepth+1); err != nil {
			return closedResolvedType{}, err
		}
	case "ANY_SUBLINK":
		if len(expression.Args) != 1 {
			return closedResolvedType{}, NewPrecisionFailure(BinderCodeModeRequired)
		}
		left, err := resolver.resolveExpr(expression.Args[0], scope, usage, site, outputIndex, queryDepth)
		if err != nil {
			return closedResolvedType{}, err
		}
		targets, err := resolver.resolveQuery(expression.Subquery, scope, "expression_query", queryDepth+1)
		if err != nil {
			return closedResolvedType{}, err
		}
		if len(targets) != 1 || left.OID == 705 || left.OID != targets[0].OID || left.Collation != targets[0].Collation {
			return closedResolvedType{}, NewPrecisionFailure(BinderCodeModeRequired)
		}
		operator, err := resolver.lookup(resolver.ctx, "=", left.OID, targets[0].OID, resolver.budget)
		if err != nil || operator.OID == 0 || operator.FunctionOID == 0 || operator.ResultType != 16 {
			return closedResolvedType{}, NewPrecisionFailure(BinderCodeModeRequired)
		}
		resolver.addObject("operator", operator.OID)
		resolver.addObject("function", operator.FunctionOID)
	default:
		return closedResolvedType{}, NewPrecisionFailure(BinderCodeModeRequired)
	}
	return closedResolvedType{OID: 16}, nil
}

func (resolver *closedSelectResolver) resolveColumn(scope *closedSelectScope, parts []string) (*closedSelectBinding, PostgresColumnIdentity, error) {
	if len(parts) < 1 || len(parts) > 3 {
		return nil, PostgresColumnIdentity{}, NewPrecisionFailure("AUTH_COLUMN_SHAPE_UNSUPPORTED")
	}
	columnName := parts[len(parts)-1]
	for current := scope; current != nil; current = current.parent {
		var matches []*closedSelectBinding
		for _, binding := range current.bindings {
			match := false
			switch len(parts) {
			case 1:
				_, match = binding.columns[columnName]
			case 2:
				match = binding.visible == parts[0]
			case 3:
				match = !binding.aliased && binding.relation.Schema == parts[0] && binding.relation.Name == parts[1]
			}
			if match {
				matches = append(matches, binding)
			}
		}
		if len(matches) == 0 {
			if len(parts) == 1 && closedScopeHasVisibleRelation(current, columnName) {
				return nil, PostgresColumnIdentity{}, NewPrecisionFailure("AUTH_COLUMN_SHAPE_UNSUPPORTED")
			}
			continue
		}
		if len(matches) != 1 {
			return nil, PostgresColumnIdentity{}, NewPrecisionFailure(BinderCodeModeRequired)
		}
		column, ok := matches[0].columns[columnName]
		if !ok {
			if closedSystemColumn(columnName) {
				return nil, PostgresColumnIdentity{}, NewPrecisionFailure("AUTH_COLUMN_SHAPE_UNSUPPORTED")
			}
			return nil, PostgresColumnIdentity{}, NewPrecisionFailure(BinderCodeModeRequired)
		}
		return matches[0], column, nil
	}
	if closedSystemColumn(columnName) {
		return nil, PostgresColumnIdentity{}, NewPrecisionFailure("AUTH_COLUMN_SHAPE_UNSUPPORTED")
	}
	return nil, PostgresColumnIdentity{}, NewPrecisionFailure(BinderCodeModeRequired)
}

func (resolver *closedSelectResolver) addColumnUse(binding *closedSelectBinding, column PostgresColumnIdentity,
	usage SemanticUsage, site string, outputIndex int) {
	key := strings.Join([]string{strconv.FormatUint(uint64(column.RelationOID), 10), strconv.Itoa(int(column.Attnum)),
		string(usage), site, strconv.Itoa(outputIndex)}, "\x00")
	if _, exists := resolver.columnSeen[key]; exists {
		return
	}
	resolver.columnSeen[key] = struct{}{}
	resolver.facts.ColumnUses = append(resolver.facts.ColumnUses, SemanticColumnUse{RelationOID: column.RelationOID,
		Attnum: column.Attnum, Name: column.Name, TypeOID: column.TypeOID, TypeModifier: column.Typmod,
		CollationOID: column.Collation, Usage: usage, Site: site, OutputIndex: outputIndex,
		BindingAlias: resolver.firstPathByOID[binding.relation.OID]})
}

func (resolver *closedSelectResolver) addObject(kind SemanticObjectKind, oid uint32) {
	if oid == 0 {
		return
	}
	key := string(kind) + "\x00" + strconv.FormatUint(uint64(oid), 10)
	if _, exists := resolver.objectSeen[key]; exists {
		return
	}
	resolver.objectSeen[key] = struct{}{}
	resolver.facts.ObjectUses = append(resolver.facts.ObjectUses, SemanticObjectUse{Kind: kind, ObjectOID: oid})
}

func lookupClosedOperator(ctx context.Context, tx pgx.Tx, name string, left, right uint32, budget PostgresCatalogBudget) (closedOperatorIdentity, error) {
	if ctx == nil || tx == nil || budget == nil || name == "" || left == 0 || right == 0 {
		return closedOperatorIdentity{}, NewPrecisionFailure(BinderCodeModeRequired)
	}
	if err := budget.ChargeCatalogRoundTrips(1); err != nil {
		return closedOperatorIdentity{}, err
	}
	const query = `SELECT o.oid,o.oprcode::oid,o.oprresult
FROM pg_catalog.pg_operator o
JOIN pg_catalog.pg_namespace n ON n.oid=o.oprnamespace
JOIN pg_catalog.pg_proc p ON p.oid=o.oprcode
JOIN pg_catalog.pg_namespace pn ON pn.oid=p.pronamespace
WHERE n.nspname='pg_catalog' AND pn.nspname='pg_catalog' AND o.oprname=$1
  AND o.oprleft=$2 AND o.oprright=$3 AND o.oid<16384 AND p.oid<16384
  AND p.provolatile='i' AND NOT p.prosecdef
ORDER BY o.oid`
	rows, err := tx.Query(ctx, query, name, left, right)
	if err != nil {
		return closedOperatorIdentity{}, NewCatalogFailure("AUTH_CATALOG_INCOMPLETE")
	}
	defer rows.Close()
	var result closedOperatorIdentity
	count := 0
	for rows.Next() {
		if err := budget.ChargeCatalogRows(1); err != nil {
			return closedOperatorIdentity{}, err
		}
		count++
		if count != 1 || rows.Scan(&result.OID, &result.FunctionOID, &result.ResultType) != nil {
			return closedOperatorIdentity{}, NewPrecisionFailure(BinderCodeModeRequired)
		}
	}
	if rows.Err() != nil {
		return closedOperatorIdentity{}, NewCatalogFailure("AUTH_CATALOG_INCOMPLETE")
	}
	if count != 1 {
		return closedOperatorIdentity{}, NewPrecisionFailure(BinderCodeModeRequired)
	}
	return result, nil
}

func closedBuiltinScalarType(oid uint32) bool {
	switch oid {
	case 16, 17, 18, 19, 20, 21, 23, 25, 26, 700, 701, 705, 790, 829, 869, 650, 1042, 1043,
		1082, 1083, 1114, 1184, 1186, 1266, 1560, 1562, 1700, 2950, 3802:
		return true
	default:
		return false
	}
}

func closedArithmeticType(oid uint32) bool {
	switch oid {
	case 20, 21, 23, 700, 701, 1700:
		return true
	default:
		return false
	}
}

func closedComparisonOperator(name string) bool {
	switch name {
	case "=", "<>", "<", ">", "<=", ">=":
		return true
	default:
		return false
	}
}

func closedArithmeticOperator(name string) bool {
	switch name {
	case "+", "-", "*", "/", "%":
		return true
	default:
		return false
	}
}

func closedSystemColumn(name string) bool {
	switch name {
	case "ctid", "xmin", "cmin", "xmax", "cmax", "tableoid":
		return true
	default:
		return false
	}
}

func closedScopeHasVisibleRelation(scope *closedSelectScope, name string) bool {
	for _, binding := range scope.bindings {
		if binding.visible == name {
			return true
		}
	}
	return false
}

// CompareClosedWithNativeSelect is the differential gate for the common
// support set. Any field-level difference is AUTH_BINDER_DIVERGENCE.
func CompareClosedWithNativeSelect(closed BoundProgram, datasourceID string, session SemanticIdentity,
	manifest PostgresPreparedManifest, frame PostgresCatalogFrame) error {
	if closed.Mode != BinderModeCatalogClosedV1 || closed.Facts.StatementClass != BinderStatementSelect {
		return binderFailure(BinderFailureDivergence, BinderCodeDivergence)
	}
	native, err := NativeSelectSemanticFacts(datasourceID, session, manifest, frame)
	if err != nil {
		return err
	}
	return CompareSemanticFacts(closed.Facts, native)
}

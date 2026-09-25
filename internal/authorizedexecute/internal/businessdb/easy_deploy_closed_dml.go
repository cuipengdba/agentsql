package businessdb

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"

	"github.com/cuipengdba/agentsql/internal/b5dml"
	closedparser "github.com/cuipengdba/agentsql/internal/parser"
	"github.com/jackc/pgx/v5"
)

type closedDMLResolvedType struct {
	oid       uint32
	collation uint32
	unknown   bool
	literal   bool
	null      bool
}

type closedDMLResolver struct {
	ctx        context.Context
	frame      PostgresCatalogFrame
	datasource string
	budget     PostgresCatalogBudget
	lookup     closedOperatorLookup
	relation   PostgresRelationIdentity
	alias      string
	target     b5dml.RelationIdentity
	columns    map[string]PostgresColumnIdentity
	references []b5dml.Reference
	seenRefs   map[string]struct{}
}

// BindClosedDML runs the feature-off CATALOG_CLOSED_V1 simple-DML lifecycle.
// It returns proof material only and deliberately exposes no execution method.
func (executor *PostgresExecutor) BindClosedDML(ctx context.Context, request BindRequest, budget PostgresCatalogBudget) (*PostgresClosedPrepared, error) {
	if executor == nil || ctx == nil || budget == nil || request.Identity.DatasourceIdentity == "" {
		return nil, NewCatalogFailure("AUTH_CATALOG_INCOMPLETE")
	}
	parsed, err := parseClosedDMLForBinding(request.RawSQL, budget)
	if err != nil {
		return nil, err
	}
	refs := []ClosedRelationRef{{Schema: parsed.Relation.Schema, Name: parsed.Relation.Name}}
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
				lockedAST, parseErr := parseClosedDMLForBinding(request.RawSQL, bindBudget)
				if parseErr != nil {
					return SemanticFacts{}, parseErr
				}
				if lockedAST.Action != parsed.Action || lockedAST.Relation.Schema != parsed.Relation.Schema || lockedAST.Relation.Name != parsed.Relation.Name {
					return SemanticFacts{}, NewCatalogFailure("AUTH_CATALOG_RACE")
				}
				if gateErr := rejectClosedDMLIndexes(bindContext, tx, frame, bindBudget); gateErr != nil {
					return SemanticFacts{}, gateErr
				}
				return resolvePostgresClosedDML(bindContext, lockedAST, frame, request.Identity.DatasourceIdentity,
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

// Index maintenance has implicit reads whose opclass/opfamily/support closure
// is not represented by the S4 SQL binder. Until that manifest is available,
// every target-table index (including an ordinary primary-key index) is denied.
func rejectClosedDMLIndexes(ctx context.Context, tx pgx.Tx, frame PostgresCatalogFrame, budget PostgresCatalogBudget) error {
	if ctx == nil || tx == nil || budget == nil || len(frame.Relations) != 1 {
		return NewCatalogFailure("AUTH_CATALOG_INCOMPLETE")
	}
	if err := budget.ChargeCatalogRoundTrips(1); err != nil {
		return err
	}
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_catalog.pg_index WHERE indrelid=$1)`, frame.Relations[0].OID).Scan(&exists); err != nil {
		return NewCatalogFailure("AUTH_CATALOG_INCOMPLETE")
	}
	if exists {
		if err := budget.ChargeCatalogRows(1); err != nil {
			return err
		}
		return NewPrecisionFailure("AUTH_IMPLICIT_OBJECT_UNSUPPORTED")
	}
	return nil
}

func parseClosedDMLForBinding(rawSQL string, budget PostgresCatalogBudget) (*closedparser.PostgresClosedDML, error) {
	if strings.TrimSpace(rawSQL) == "" {
		return nil, NewCapabilityFailure(BinderCodeModeUnsupported)
	}
	if err := budget.ChargeBinderBytes(len(rawSQL)); err != nil {
		return nil, err
	}
	parsed, err := closedparser.ParsePostgresClosedDML(rawSQL)
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

func resolvePostgresClosedDML(ctx context.Context, parsed *closedparser.PostgresClosedDML, frame PostgresCatalogFrame,
	datasource string, lookup closedOperatorLookup, budget PostgresCatalogBudget) (SemanticFacts, error) {
	if ctx == nil || parsed == nil || frame.Fingerprint == "" || frame.DatabaseOID == 0 || len(frame.Relations) != 1 ||
		datasource == "" || lookup == nil || budget == nil {
		return SemanticFacts{}, NewCatalogFailure("AUTH_CATALOG_INCOMPLETE")
	}
	relation := frame.Relations[0]
	if relation.Schema != parsed.Relation.Schema || relation.Name != parsed.Relation.Name || relation.Kind != 'r' || relation.Persistence != 'p' {
		return SemanticFacts{}, NewCatalogFailure("AUTH_CATALOG_RACE")
	}
	target := b5dml.RelationIdentity{DatasourceID: datasource, DatabaseOID: frame.DatabaseOID,
		RelationOID: relation.OID, RelationKind: relation.Kind, Schema: relation.Schema, Name: relation.Name,
		CatalogFingerprint: frame.Fingerprint}
	resolver := &closedDMLResolver{ctx: ctx, frame: frame, datasource: datasource, budget: budget, lookup: lookup,
		relation: relation, alias: parsed.Relation.Alias, target: target, columns: make(map[string]PostgresColumnIdentity), seenRefs: make(map[string]struct{})}
	for _, column := range frame.Columns {
		if column.RelationOID != relation.OID || column.Attnum <= 0 || column.Name == "" {
			return SemanticFacts{}, NewCatalogFailure("AUTH_CATALOG_INCOMPLETE")
		}
		if !closedDMLColumnType(column) {
			return SemanticFacts{}, NewPrecisionFailure("AUTH_COLUMN_SHAPE_UNSUPPORTED")
		}
		resolver.columns[column.Name] = column
	}
	if len(resolver.columns) == 0 {
		return SemanticFacts{}, NewCatalogFailure("AUTH_CATALOG_INCOMPLETE")
	}

	statement := b5dml.StatementFacts{Dialect: b5dml.DialectPostgreSQL, Shape: b5dml.ShapeSimple, Target: target}
	switch parsed.Action {
	case "INSERT":
		statement.Action = b5dml.ActionInsert
		if err := resolver.resolveInsert(parsed, &statement); err != nil {
			return SemanticFacts{}, err
		}
	case "UPDATE":
		statement.Action = b5dml.ActionUpdate
		if err := resolver.resolveUpdate(parsed, &statement); err != nil {
			return SemanticFacts{}, err
		}
	case "DELETE":
		statement.Action = b5dml.ActionDelete
		if err := resolver.resolveDelete(parsed, &statement); err != nil {
			return SemanticFacts{}, err
		}
	default:
		return SemanticFacts{}, NewCapabilityFailure(BinderCodeModeUnsupported)
	}
	statement.References = append(statement.References, resolver.references...)
	decision := b5dml.AnalyzeStatement(statement)
	if !decision.Allowed {
		return SemanticFacts{}, NewPrecisionFailure(string(decision.Reason))
	}
	return closedDMLSemanticFacts(frame, relation, datasource, statement.Action, decision.Writes, decision.References), nil
}

func (resolver *closedDMLResolver) resolveInsert(parsed *closedparser.PostgresClosedDML, statement *b5dml.StatementFacts) error {
	if len(parsed.InsertColumns) == 0 || len(parsed.Values) == 0 {
		return NewPrecisionFailure(BinderCodeModeRequired)
	}
	explicit := make(map[string]struct{}, len(parsed.InsertColumns))
	for _, name := range parsed.InsertColumns {
		column, ok := resolver.columns[name]
		if !ok {
			return NewPrecisionFailure(BinderCodeModeRequired)
		}
		explicit[name] = struct{}{}
		_ = column
	}
	for _, row := range parsed.Values {
		if len(row) != len(parsed.InsertColumns) {
			return NewPrecisionFailure(BinderCodeModeRequired)
		}
		for _, expression := range row {
			if !closedDMLLiteral(expression) {
				return NewPrecisionFailure(BinderCodeModeRequired)
			}
		}
	}
	columns := append([]PostgresColumnIdentity(nil), resolver.frame.Columns...)
	sort.Slice(columns, func(i, j int) bool { return columns[i].Attnum < columns[j].Attnum })
	for _, column := range columns {
		assignment := b5dml.AssignmentOmitted
		if _, ok := explicit[column.Name]; ok {
			assignment = b5dml.AssignmentExplicitValue
		}
		identity := resolver.b5Column(column)
		statement.InsertColumns = append(statement.InsertColumns, b5dml.InsertColumnFact{Column: identity,
			Assignment: assignment, Identity: column.Identity != 0, Generated: column.Generated != 0})
	}
	return nil
}

func (resolver *closedDMLResolver) resolveUpdate(parsed *closedparser.PostgresClosedDML, statement *b5dml.StatementFacts) error {
	if len(parsed.Assignments) == 0 || parsed.Where == nil {
		return NewPrecisionFailure(BinderCodeModeRequired)
	}
	for _, assignment := range parsed.Assignments {
		column, ok := resolver.columns[assignment.Column]
		if !ok {
			return NewPrecisionFailure(BinderCodeModeRequired)
		}
		valueType, err := resolver.resolveExpr(assignment.Value, "expression")
		if err != nil {
			return err
		}
		if !valueType.literal && (valueType.unknown || valueType.oid != column.TypeOID || valueType.collation != column.Collation) {
			return NewPrecisionFailure(BinderCodeModeRequired)
		}
		statement.Writes = append(statement.Writes, b5dml.ColumnWrite(resolver.b5Column(column), b5dml.WriteSourceExplicit))
	}
	condition, err := resolver.resolveExpr(parsed.Where, "where")
	if err != nil {
		return err
	}
	if condition.unknown || condition.oid != 16 {
		return NewPrecisionFailure(BinderCodeModeRequired)
	}
	return nil
}

func (resolver *closedDMLResolver) resolveDelete(parsed *closedparser.PostgresClosedDML, statement *b5dml.StatementFacts) error {
	if parsed.Where == nil {
		return NewPrecisionFailure(BinderCodeModeRequired)
	}
	condition, err := resolver.resolveExpr(parsed.Where, "where")
	if err != nil {
		return err
	}
	if condition.unknown || condition.oid != 16 {
		return NewPrecisionFailure(BinderCodeModeRequired)
	}
	statement.Writes = []b5dml.WriteTarget{b5dml.RowDelete(resolver.target)}
	return nil
}

func (resolver *closedDMLResolver) resolveExpr(expression *closedparser.PostgresClosedExpr, site string) (closedDMLResolvedType, error) {
	if expression == nil {
		return closedDMLResolvedType{}, NewPrecisionFailure(BinderCodeModeRequired)
	}
	switch expression.Kind {
	case closedparser.PostgresClosedColumn:
		column, err := resolver.resolveColumn(expression.Column)
		if err != nil {
			return closedDMLResolvedType{}, err
		}
		resolver.addReference(column, site)
		return closedDMLResolvedType{oid: column.TypeOID, collation: column.Collation}, nil
	case closedparser.PostgresClosedInteger:
		if expression.Integer < -2147483648 || expression.Integer > 2147483647 {
			return closedDMLResolvedType{}, NewPrecisionFailure(BinderCodeModeRequired)
		}
		return closedDMLResolvedType{oid: 23, literal: true}, nil
	case closedparser.PostgresClosedBoolean:
		return closedDMLResolvedType{oid: 16, literal: true}, nil
	case closedparser.PostgresClosedNull:
		return closedDMLResolvedType{oid: 705, unknown: true, literal: true, null: true}, nil
	case closedparser.PostgresClosedString, closedparser.PostgresClosedFloat, closedparser.PostgresClosedBit:
		return closedDMLResolvedType{oid: 705, unknown: true, literal: true}, nil
	case closedparser.PostgresClosedBoolExpr:
		for _, argument := range expression.Args {
			value, err := resolver.resolveExpr(argument, site)
			if err != nil {
				return closedDMLResolvedType{}, err
			}
			if value.unknown || value.oid != 16 {
				return closedDMLResolvedType{}, NewPrecisionFailure(BinderCodeModeRequired)
			}
		}
		return closedDMLResolvedType{oid: 16}, nil
	case closedparser.PostgresClosedNullTest:
		if len(expression.Args) != 1 {
			return closedDMLResolvedType{}, NewPrecisionFailure(BinderCodeModeRequired)
		}
		if _, err := resolver.resolveExpr(expression.Args[0], site); err != nil {
			return closedDMLResolvedType{}, err
		}
		return closedDMLResolvedType{oid: 16}, nil
	case closedparser.PostgresClosedOperator:
		return resolver.resolveOperator(expression, site)
	default:
		return closedDMLResolvedType{}, NewPrecisionFailure(BinderCodeModeRequired)
	}
}

func (resolver *closedDMLResolver) resolveOperator(expression *closedparser.PostgresClosedExpr, site string) (closedDMLResolvedType, error) {
	if len(expression.Args) != 2 {
		return closedDMLResolvedType{}, NewPrecisionFailure(BinderCodeModeRequired)
	}
	left, err := resolver.resolveExpr(expression.Args[0], site)
	if err != nil {
		return closedDMLResolvedType{}, err
	}
	right, err := resolver.resolveExpr(expression.Args[1], site)
	if err != nil {
		return closedDMLResolvedType{}, err
	}
	if left.null || right.null {
		return closedDMLResolvedType{}, NewPrecisionFailure(BinderCodeModeRequired)
	}
	if left.unknown && right.unknown {
		return closedDMLResolvedType{}, NewPrecisionFailure(BinderCodeModeRequired)
	}
	if left.unknown {
		left.oid, left.collation, left.unknown = right.oid, right.collation, false
	}
	if right.unknown {
		right.oid, right.collation, right.unknown = left.oid, left.collation, false
	}
	name := expression.Operator
	if name == "!=" {
		name = "<>"
	}
	if !closedComparisonOperator(name) && (!closedArithmeticOperator(name) || !closedArithmeticType(left.oid) || !closedArithmeticType(right.oid)) {
		return closedDMLResolvedType{}, NewPrecisionFailure(BinderCodeModeRequired)
	}
	operator, err := resolver.lookup(resolver.ctx, name, left.oid, right.oid, resolver.budget)
	if err != nil {
		return closedDMLResolvedType{}, err
	}
	if operator.OID == 0 || operator.FunctionOID == 0 || !closedBuiltinScalarType(operator.ResultType) {
		return closedDMLResolvedType{}, NewPrecisionFailure(BinderCodeModeRequired)
	}
	if closedComparisonOperator(name) && operator.ResultType != 16 {
		return closedDMLResolvedType{}, NewPrecisionFailure(BinderCodeModeRequired)
	}
	collation := uint32(0)
	if operator.ResultType == left.oid {
		collation = left.collation
	} else if operator.ResultType == right.oid {
		collation = right.collation
	}
	return closedDMLResolvedType{oid: operator.ResultType, collation: collation}, nil
}

func (resolver *closedDMLResolver) resolveColumn(parts []string) (PostgresColumnIdentity, error) {
	if len(parts) < 1 || len(parts) > 3 {
		return PostgresColumnIdentity{}, NewPrecisionFailure("AUTH_COLUMN_SHAPE_UNSUPPORTED")
	}
	name := parts[len(parts)-1]
	alias := resolver.relation.Name
	if resolver.alias != "" {
		alias = resolver.alias
	}
	switch len(parts) {
	case 2:
		if parts[0] != alias {
			return PostgresColumnIdentity{}, NewPrecisionFailure(BinderCodeModeRequired)
		}
	case 3:
		if resolver.alias != "" || parts[0] != resolver.relation.Schema || parts[1] != resolver.relation.Name {
			return PostgresColumnIdentity{}, NewPrecisionFailure(BinderCodeModeRequired)
		}
	}
	column, ok := resolver.columns[name]
	if !ok {
		if closedSystemColumn(name) {
			return PostgresColumnIdentity{}, NewPrecisionFailure("AUTH_COLUMN_SHAPE_UNSUPPORTED")
		}
		return PostgresColumnIdentity{}, NewPrecisionFailure(BinderCodeModeRequired)
	}
	return column, nil
}

func (resolver *closedDMLResolver) b5Column(column PostgresColumnIdentity) b5dml.ColumnIdentity {
	return b5dml.ColumnIdentity{Relation: resolver.target, Attnum: column.Attnum, Name: column.Name,
		TypeOID: column.TypeOID, TypeModifier: column.Typmod, CollationOID: column.Collation}
}

func (resolver *closedDMLResolver) addReference(column PostgresColumnIdentity, site string) {
	key := strconv.Itoa(int(column.Attnum)) + "\x00" + site
	if _, ok := resolver.seenRefs[key]; ok {
		return
	}
	resolver.seenRefs[key] = struct{}{}
	referenceSite := b5dml.ReferenceExpression
	if site == "where" {
		referenceSite = b5dml.ReferenceWhere
	}
	resolver.references = append(resolver.references, b5dml.ColumnReference(resolver.b5Column(column), referenceSite))
}

func closedDMLColumnType(column PostgresColumnIdentity) bool {
	return column.Attnum > 0 && column.TypeOID > 0 && column.TypeOID < 16384 &&
		closedBuiltinScalarType(column.TypeOID) && column.Collation < 16384
}

func closedDMLLiteral(value *closedparser.PostgresClosedExpr) bool {
	if value == nil {
		return false
	}
	switch value.Kind {
	case closedparser.PostgresClosedInteger, closedparser.PostgresClosedBoolean, closedparser.PostgresClosedNull,
		closedparser.PostgresClosedString, closedparser.PostgresClosedFloat, closedparser.PostgresClosedBit:
		return true
	default:
		return false
	}
}

func closedDMLSemanticFacts(frame PostgresCatalogFrame, relation PostgresRelationIdentity, datasource string,
	action b5dml.Action, writes []b5dml.WriteTarget, references []b5dml.Reference) SemanticFacts {
	facts := SemanticFacts{Schema: SemanticFactsSchemaID, SchemaVersion: SemanticFactsVersion,
		StatementClass: binderStatementClassForDML(action), Action: action,
		Relations: []SemanticRelation{{DatasourceID: datasource, DatabaseOID: relation.DatabaseOID,
			RelationOID: relation.OID, NamespaceOID: relation.NamespaceOID, Schema: relation.Schema, Name: relation.Name,
			Kind: relation.Kind, Persistence: relation.Persistence, CatalogFingerprint: frame.Fingerprint}},
		Identity: SemanticIdentity{DatasourceIdentity: datasource, DatabaseOID: frame.DatabaseOID, CatalogDigest: frame.Fingerprint}}
	for _, write := range writes {
		value := SemanticWriteTarget{RelationOID: write.Relation.RelationOID, Kind: write.Kind, Source: write.Source}
		if write.Kind == b5dml.WriteTargetColumn {
			value.Attnum, value.Name, value.TypeOID, value.TypeModifier, value.CollationOID = write.Column.Attnum,
				write.Column.Name, write.Column.TypeOID, write.Column.TypeModifier, write.Column.CollationOID
		}
		facts.WriteTargets = append(facts.WriteTargets, value)
	}
	for _, reference := range references {
		facts.ColumnUses = append(facts.ColumnUses, SemanticColumnUse{RelationOID: reference.Relation.RelationOID,
			Attnum: reference.Column.Attnum, Name: reference.Column.Name, TypeOID: reference.Column.TypeOID,
			TypeModifier: reference.Column.TypeModifier, CollationOID: reference.Column.CollationOID,
			Usage: SemanticUsageReference, Site: nativeReferenceSite(reference.Site), OutputIndex: -1})
	}
	return facts
}

// CompareClosedWithNativeDML is the field-level differential gate for the
// common simple-DML support set.
func CompareClosedWithNativeDML(closed BoundProgram, datasourceID string, session SemanticIdentity, enrollment PostgresDMLEnrollment) error {
	if closed.Mode != BinderModeCatalogClosedV1 || !validNativeDMLAction(closed.Facts.Action) ||
		closed.Facts.StatementClass != binderStatementClassForDML(closed.Facts.Action) {
		return binderFailure(BinderFailureDivergence, BinderCodeDivergence)
	}
	native, err := NativeDMLSemanticFacts(datasourceID, session, enrollment)
	if err != nil {
		return err
	}
	if err := CompareSemanticFacts(closed.Facts, native); err != nil {
		var binderErr *BinderError
		if errors.As(err, &binderErr) {
			return binderFailure(BinderFailureDivergence, BinderCodeDivergence)
		}
		return err
	}
	return nil
}

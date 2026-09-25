package parser

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	pg_query "github.com/pganalyze/pg_query_go/v5"
)

// PostgresClosedDML is the deliberately small raw-tree contract used by the
// zero-install B5 binder. It contains names and expressions only; catalog OIDs
// and type identities are resolved again while relation locks are held.
type PostgresClosedDML struct {
	Action        string
	Relation      PostgresClosedRelation
	InsertColumns []string
	Values        [][]*PostgresClosedExpr
	Assignments   []PostgresClosedAssignment
	Where         *PostgresClosedExpr
	ASTDigest     string
	NodeCount     int
	MaxDepth      int
}

type PostgresClosedAssignment struct {
	Column string
	Value  *PostgresClosedExpr
}

const (
	PostgresClosedString PostgresClosedExprKind = "string"
	PostgresClosedFloat  PostgresClosedExprKind = "float"
	PostgresClosedBit    PostgresClosedExprKind = "bit"
)

// ParsePostgresClosedDML accepts exactly one schema-qualified INSERT VALUES,
// UPDATE ... WHERE, or DELETE ... WHERE statement. Every richer raw-tree node
// is rejected before a business connection is acquired.
func ParsePostgresClosedDML(sql string) (*PostgresClosedDML, error) {
	if strings.TrimSpace(sql) == "" {
		return nil, postgresClosedFailure(PostgresClosedUnsupported)
	}
	pieces, err := pg_query.SplitWithScanner(sql, true)
	if err != nil || len(pieces) != 1 {
		return nil, postgresClosedFailure(PostgresClosedUnsupported)
	}
	scan, err := pg_query.Scan(sql)
	if err != nil || len(scan.Tokens) > postgresClosedMaxTokens {
		return nil, postgresClosedFailure(PostgresClosedUnsupported)
	}
	parsedJSON, err := pg_query.ParseToJSON(sql)
	if err != nil {
		return nil, postgresClosedFailure(PostgresClosedUnsupported)
	}
	document, err := decodePostgresDocument(parsedJSON)
	if err != nil || len(document.Statements) != 1 {
		return nil, postgresClosedFailure(PostgresClosedUnsupported)
	}
	nodeType, node, err := postgresRoot(document.Statements[0])
	if err != nil {
		return nil, postgresClosedFailure(PostgresClosedUnsupported)
	}
	parser := &postgresClosedParser{}
	var result *PostgresClosedDML
	switch nodeType {
	case "InsertStmt":
		result, err = parser.parseClosedInsert(node, 1)
	case "UpdateStmt":
		result, err = parser.parseClosedUpdate(node, 1)
	case "DeleteStmt":
		result, err = parser.parseClosedDelete(node, 1)
	default:
		return nil, postgresClosedFailure(PostgresClosedUnsupported)
	}
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256([]byte(parsedJSON))
	result.ASTDigest = "postgres-closed-dml-v1:" + hex.EncodeToString(digest[:])
	result.NodeCount, result.MaxDepth = parser.nodes, parser.maxDepth
	return result, nil
}

func (parser *postgresClosedParser) parseClosedInsert(value any, depth int) (*PostgresClosedDML, error) {
	if err := parser.charge(depth); err != nil {
		return nil, err
	}
	node, ok := value.(map[string]any)
	if !ok || !closedKeys(node, "relation", "cols", "selectStmt", "onConflictClause", "returningList", "withClause", "override") ||
		closedPresent(node["onConflictClause"]) || closedPresent(node["returningList"]) ||
		closedPresent(node["withClause"]) {
		return nil, postgresClosedFailure(PostgresClosedModeRequired)
	}
	if override, _ := node["override"].(string); override != "" && override != "OVERRIDING_NOT_SET" {
		return nil, postgresClosedFailure(PostgresClosedModeRequired)
	}
	relation, err := parser.parseClosedDMLRelation(node["relation"], false, depth+1)
	if err != nil {
		return nil, err
	}
	cols, ok := node["cols"].([]any)
	if !ok || len(cols) == 0 {
		return nil, postgresClosedFailure(PostgresClosedModeRequired)
	}
	result := &PostgresClosedDML{Action: "INSERT", Relation: relation}
	seen := make(map[string]struct{}, len(cols))
	for _, value := range cols {
		if err := parser.charge(depth + 1); err != nil {
			return nil, err
		}
		wrapper, ok := value.(map[string]any)
		target, targetOK := wrapper["ResTarget"].(map[string]any)
		if !ok || len(wrapper) != 1 || !targetOK || !closedKeys(target, "name", "indirection", "val", "location") ||
			closedPresent(target["indirection"]) || closedPresent(target["val"]) {
			return nil, postgresClosedFailure(PostgresClosedModeRequired)
		}
		name, _ := target["name"].(string)
		if name == "" {
			return nil, postgresClosedFailure(PostgresClosedModeRequired)
		}
		if _, duplicate := seen[name]; duplicate {
			return nil, postgresClosedFailure(PostgresClosedModeRequired)
		}
		seen[name] = struct{}{}
		result.InsertColumns = append(result.InsertColumns, name)
	}
	selectWrapper, ok := node["selectStmt"].(map[string]any)
	selectNode, selectOK := selectWrapper["SelectStmt"].(map[string]any)
	if !ok || len(selectWrapper) != 1 || !selectOK || !closedKeys(selectNode, "targetList", "fromClause", "whereClause", "op", "all", "larg", "rarg",
		"sortClause", "distinctClause", "groupClause", "groupDistinct", "havingClause", "windowClause", "valuesLists", "limitOffset", "limitCount",
		"limitOption", "lockingClause", "withClause", "intoClause") {
		return nil, postgresClosedFailure(PostgresClosedModeRequired)
	}
	if operation, _ := selectNode["op"].(string); operation != "" && operation != "SETOP_NONE" {
		return nil, postgresClosedFailure(PostgresClosedModeRequired)
	}
	for _, field := range []string{"targetList", "fromClause", "whereClause", "larg", "rarg", "sortClause", "distinctClause", "groupClause", "havingClause", "windowClause", "limitOffset", "limitCount", "lockingClause", "withClause", "intoClause"} {
		if closedPresent(selectNode[field]) {
			return nil, postgresClosedFailure(PostgresClosedModeRequired)
		}
	}
	rows, ok := selectNode["valuesLists"].([]any)
	if !ok || len(rows) == 0 {
		return nil, postgresClosedFailure(PostgresClosedModeRequired)
	}
	for _, rowValue := range rows {
		if err := parser.charge(depth + 1); err != nil {
			return nil, err
		}
		wrapper, ok := rowValue.(map[string]any)
		list, listOK := wrapper["List"].(map[string]any)
		items, itemsOK := list["items"].([]any)
		if !ok || len(wrapper) != 1 || !listOK || !closedKeys(list, "items") || !itemsOK || len(items) != len(result.InsertColumns) {
			return nil, postgresClosedFailure(PostgresClosedModeRequired)
		}
		row := make([]*PostgresClosedExpr, 0, len(items))
		for _, item := range items {
			expression, err := parser.parseClosedDMLExpression(item, depth+2)
			if err != nil || !postgresClosedDMLLiteral(expression) {
				return nil, postgresClosedFailure(PostgresClosedModeRequired)
			}
			row = append(row, expression)
		}
		result.Values = append(result.Values, row)
	}
	return result, nil
}

func (parser *postgresClosedParser) parseClosedUpdate(value any, depth int) (*PostgresClosedDML, error) {
	if err := parser.charge(depth); err != nil {
		return nil, err
	}
	node, ok := value.(map[string]any)
	if !ok || !closedKeys(node, "relation", "targetList", "whereClause", "fromClause", "returningList", "withClause") ||
		closedPresent(node["fromClause"]) || closedPresent(node["returningList"]) || closedPresent(node["withClause"]) || !closedPresent(node["whereClause"]) {
		return nil, postgresClosedFailure(PostgresClosedModeRequired)
	}
	relation, err := parser.parseClosedDMLRelation(node["relation"], true, depth+1)
	if err != nil {
		return nil, err
	}
	targets, ok := node["targetList"].([]any)
	if !ok || len(targets) == 0 {
		return nil, postgresClosedFailure(PostgresClosedModeRequired)
	}
	result := &PostgresClosedDML{Action: "UPDATE", Relation: relation}
	seen := make(map[string]struct{}, len(targets))
	for _, value := range targets {
		if err := parser.charge(depth + 1); err != nil {
			return nil, err
		}
		wrapper, ok := value.(map[string]any)
		target, targetOK := wrapper["ResTarget"].(map[string]any)
		if !ok || len(wrapper) != 1 || !targetOK || !closedKeys(target, "name", "indirection", "val", "location") || closedPresent(target["indirection"]) {
			return nil, postgresClosedFailure(PostgresClosedColumnShape)
		}
		name, _ := target["name"].(string)
		if name == "" {
			return nil, postgresClosedFailure(PostgresClosedModeRequired)
		}
		if _, duplicate := seen[name]; duplicate {
			return nil, postgresClosedFailure(PostgresClosedModeRequired)
		}
		seen[name] = struct{}{}
		expression, err := parser.parseClosedDMLExpression(target["val"], depth+2)
		if err != nil {
			return nil, err
		}
		result.Assignments = append(result.Assignments, PostgresClosedAssignment{Column: name, Value: expression})
	}
	result.Where, err = parser.parseClosedDMLExpression(node["whereClause"], depth+1)
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (parser *postgresClosedParser) parseClosedDelete(value any, depth int) (*PostgresClosedDML, error) {
	if err := parser.charge(depth); err != nil {
		return nil, err
	}
	node, ok := value.(map[string]any)
	if !ok || !closedKeys(node, "relation", "usingClause", "whereClause", "returningList", "withClause") ||
		closedPresent(node["usingClause"]) || closedPresent(node["returningList"]) || closedPresent(node["withClause"]) || !closedPresent(node["whereClause"]) {
		return nil, postgresClosedFailure(PostgresClosedModeRequired)
	}
	relation, err := parser.parseClosedDMLRelation(node["relation"], true, depth+1)
	if err != nil {
		return nil, err
	}
	where, err := parser.parseClosedDMLExpression(node["whereClause"], depth+1)
	if err != nil {
		return nil, err
	}
	return &PostgresClosedDML{Action: "DELETE", Relation: relation, Where: where}, nil
}

func (parser *postgresClosedParser) parseClosedDMLRelation(value any, allowAlias bool, depth int) (PostgresClosedRelation, error) {
	if err := parser.charge(depth); err != nil {
		return PostgresClosedRelation{}, err
	}
	node, ok := value.(map[string]any)
	if !ok || !closedKeys(node, "catalogname", "schemaname", "relname", "inh", "relpersistence", "alias", "location") {
		return PostgresClosedRelation{}, postgresClosedFailure(PostgresClosedModeRequired)
	}
	catalog, _ := node["catalogname"].(string)
	schema, _ := node["schemaname"].(string)
	name, _ := node["relname"].(string)
	inherit, _ := node["inh"].(bool)
	if catalog != "" || schema == "" || name == "" || !inherit {
		return PostgresClosedRelation{}, postgresClosedFailure(PostgresClosedModeRequired)
	}
	alias, err := postgresClosedAlias(node["alias"])
	if err != nil || !allowAlias && alias != "" {
		return PostgresClosedRelation{}, postgresClosedFailure(PostgresClosedModeRequired)
	}
	return PostgresClosedRelation{Schema: schema, Name: name, Alias: alias}, nil
}

func (parser *postgresClosedParser) parseClosedDMLExpression(value any, depth int) (*PostgresClosedExpr, error) {
	if wrapper, ok := value.(map[string]any); ok && len(wrapper) == 1 {
		if constant, ok := wrapper["A_Const"].(map[string]any); ok {
			if err := parser.charge(depth); err != nil {
				return nil, err
			}
			return postgresClosedDMLConstant(constant)
		}
	}
	expression, err := parser.parseExpr(value, depth)
	if err != nil {
		return nil, err
	}
	if postgresClosedContainsSubquery(expression) {
		return nil, postgresClosedFailure(PostgresClosedModeRequired)
	}
	return expression, nil
}

func postgresClosedDMLConstant(node map[string]any) (*PostgresClosedExpr, error) {
	if !closedKeys(node, "ival", "boolval", "fval", "sval", "bsval", "isnull", "location") {
		return nil, postgresClosedFailure(PostgresClosedModeRequired)
	}
	if isNull, _ := node["isnull"].(bool); isNull {
		return &PostgresClosedExpr{Kind: PostgresClosedNull}, nil
	}
	if _, ok := node["ival"].(map[string]any); ok {
		return postgresClosedConstant(node)
	}
	if _, ok := node["boolval"].(map[string]any); ok {
		return postgresClosedConstant(node)
	}
	for field, kind := range map[string]PostgresClosedExprKind{"sval": PostgresClosedString, "fval": PostgresClosedFloat, "bsval": PostgresClosedBit} {
		wrapper, ok := node[field].(map[string]any)
		if !ok {
			continue
		}
		if value, present := wrapper[field]; present {
			if _, ok := value.(string); !ok {
				return nil, postgresClosedFailure(PostgresClosedModeRequired)
			}
		}
		if len(wrapper) > 1 {
			return nil, postgresClosedFailure(PostgresClosedModeRequired)
		}
		return &PostgresClosedExpr{Kind: kind}, nil
	}
	return nil, postgresClosedFailure(PostgresClosedModeRequired)
}

func postgresClosedDMLLiteral(value *PostgresClosedExpr) bool {
	if value == nil {
		return false
	}
	switch value.Kind {
	case PostgresClosedInteger, PostgresClosedBoolean, PostgresClosedNull, PostgresClosedString, PostgresClosedFloat, PostgresClosedBit:
		return true
	default:
		return false
	}
}

func postgresClosedContainsSubquery(value *PostgresClosedExpr) bool {
	if value == nil {
		return false
	}
	if value.Kind == PostgresClosedSubquery || value.Subquery != nil {
		return true
	}
	for _, argument := range value.Args {
		if postgresClosedContainsSubquery(argument) {
			return true
		}
	}
	return false
}

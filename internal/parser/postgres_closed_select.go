package parser

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	pg_query "github.com/pganalyze/pg_query_go/v5"
)

// PostgresClosedErrorKind classifies failures from the deliberately small
// CATALOG_CLOSED_V1 grammar. The database layer maps these classifications to
// stable authorization errors without retaining parser or database text.
type PostgresClosedErrorKind string

const (
	PostgresClosedModeRequired PostgresClosedErrorKind = "mode_required"
	PostgresClosedUnsupported  PostgresClosedErrorKind = "unsupported"
	PostgresClosedColumnShape  PostgresClosedErrorKind = "column_shape"
)

type PostgresClosedError struct{ Kind PostgresClosedErrorKind }

func (err *PostgresClosedError) Error() string {
	if err == nil || err.Kind == "" {
		return "postgres closed SELECT rejected"
	}
	return "postgres closed SELECT rejected: " + string(err.Kind)
}

func PostgresClosedErrorClass(err error) (PostgresClosedErrorKind, bool) {
	var closed *PostgresClosedError
	if !errors.As(err, &closed) {
		return "", false
	}
	return closed.Kind, true
}

type PostgresClosedSelect struct {
	Targets   []*PostgresClosedExpr
	From      *PostgresClosedFrom
	Where     *PostgresClosedExpr
	Relations []PostgresClosedRelation
	ASTDigest string
	NodeCount int
	MaxDepth  int
}

type PostgresClosedRelation struct {
	Schema string
	Name   string
	Alias  string
}

type PostgresClosedFrom struct {
	Relation *PostgresClosedRelation
	Join     *PostgresClosedJoin
}

type PostgresClosedJoin struct {
	Kind      string
	Left      *PostgresClosedFrom
	Right     *PostgresClosedFrom
	Condition *PostgresClosedExpr
}

type PostgresClosedExprKind string

const (
	PostgresClosedColumn   PostgresClosedExprKind = "column"
	PostgresClosedInteger  PostgresClosedExprKind = "integer"
	PostgresClosedBoolean  PostgresClosedExprKind = "boolean"
	PostgresClosedNull     PostgresClosedExprKind = "null"
	PostgresClosedOperator PostgresClosedExprKind = "operator"
	PostgresClosedBoolExpr PostgresClosedExprKind = "bool_expr"
	PostgresClosedNullTest PostgresClosedExprKind = "null_test"
	PostgresClosedSubquery PostgresClosedExprKind = "subquery"
)

type PostgresClosedExpr struct {
	Kind        PostgresClosedExprKind
	Column      []string
	Integer     int64
	Boolean     bool
	Operator    string
	Args        []*PostgresClosedExpr
	SubLinkType string
	Subquery    *PostgresClosedSelect
}

const (
	postgresClosedMaxTokens = 4096
	postgresClosedMaxNodes  = 8192
	postgresClosedMaxDepth  = 32
)

type postgresClosedParser struct {
	nodes    int
	maxDepth int
}

// ParsePostgresClosedSelect parses one PostgreSQL SELECT into a closed raw
// tree. It does not resolve names or types; that must happen against a locked
// catalog frame. Every unrecognized query or expression form is rejected.
func ParsePostgresClosedSelect(sql string) (*PostgresClosedSelect, error) {
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
	if err != nil || nodeType != "SelectStmt" {
		return nil, postgresClosedFailure(PostgresClosedUnsupported)
	}
	parser := &postgresClosedParser{}
	result, err := parser.parseSelect(node, 1)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256([]byte(parsedJSON))
	result.ASTDigest = "postgres-closed-select-v1:" + hex.EncodeToString(digest[:])
	result.NodeCount = parser.nodes
	result.MaxDepth = parser.maxDepth
	return result, nil
}

func (parser *postgresClosedParser) charge(depth int) error {
	parser.nodes++
	if depth > parser.maxDepth {
		parser.maxDepth = depth
	}
	if parser.nodes > postgresClosedMaxNodes || depth > postgresClosedMaxDepth {
		return postgresClosedFailure(PostgresClosedUnsupported)
	}
	return nil
}

func (parser *postgresClosedParser) parseSelect(value any, depth int) (*PostgresClosedSelect, error) {
	if err := parser.charge(depth); err != nil {
		return nil, err
	}
	node, ok := value.(map[string]any)
	if !ok || !closedKeys(node, "targetList", "fromClause", "whereClause", "op", "all", "larg", "rarg",
		"sortClause", "distinctClause", "groupClause", "groupDistinct", "havingClause", "windowClause",
		"valuesLists", "limitOffset", "limitCount", "limitOption", "lockingClause", "withClause", "intoClause") {
		return nil, postgresClosedFailure(PostgresClosedModeRequired)
	}
	if operation, _ := node["op"].(string); operation != "" && operation != "SETOP_NONE" {
		return nil, postgresClosedFailure(PostgresClosedModeRequired)
	}
	for _, field := range []string{"larg", "rarg", "sortClause", "distinctClause", "groupClause", "havingClause",
		"windowClause", "valuesLists", "limitOffset", "limitCount", "lockingClause", "withClause", "intoClause"} {
		if closedPresent(node[field]) {
			return nil, postgresClosedFailure(PostgresClosedModeRequired)
		}
	}
	if groupDistinct, _ := node["groupDistinct"].(bool); groupDistinct {
		return nil, postgresClosedFailure(PostgresClosedModeRequired)
	}
	targets, ok := node["targetList"].([]any)
	if !ok || len(targets) == 0 {
		return nil, postgresClosedFailure(PostgresClosedUnsupported)
	}
	from, ok := node["fromClause"].([]any)
	if !ok || len(from) != 1 {
		return nil, postgresClosedFailure(PostgresClosedModeRequired)
	}
	result := &PostgresClosedSelect{}
	parsedFrom, err := parser.parseFrom(from[0], depth+1)
	if err != nil {
		return nil, err
	}
	result.From = parsedFrom
	result.Relations = closedFromRelations(result.From)
	for _, targetValue := range targets {
		wrapper, ok := targetValue.(map[string]any)
		if !ok || len(wrapper) != 1 {
			return nil, postgresClosedFailure(PostgresClosedModeRequired)
		}
		target, ok := wrapper["ResTarget"].(map[string]any)
		if !ok || !closedKeys(target, "name", "indirection", "val", "location") || closedPresent(target["indirection"]) {
			return nil, postgresClosedFailure(PostgresClosedModeRequired)
		}
		expression, err := parser.parseExpr(target["val"], depth+1)
		if err != nil {
			return nil, err
		}
		result.Targets = append(result.Targets, expression)
	}
	if closedPresent(node["whereClause"]) {
		result.Where, err = parser.parseExpr(node["whereClause"], depth+1)
		if err != nil {
			return nil, err
		}
	}
	for _, target := range result.Targets {
		result.Relations = append(result.Relations, closedExprRelations(target)...)
	}
	result.Relations = append(result.Relations, closedExprRelations(result.Where)...)
	return result, nil
}

func (parser *postgresClosedParser) parseFrom(value any, depth int) (*PostgresClosedFrom, error) {
	if err := parser.charge(depth); err != nil {
		return nil, err
	}
	wrapper, ok := value.(map[string]any)
	if !ok || len(wrapper) != 1 {
		return nil, postgresClosedFailure(PostgresClosedModeRequired)
	}
	if value, ok := wrapper["RangeVar"].(map[string]any); ok {
		if !closedKeys(value, "catalogname", "schemaname", "relname", "inh", "relpersistence", "alias", "location") {
			return nil, postgresClosedFailure(PostgresClosedModeRequired)
		}
		catalog, _ := value["catalogname"].(string)
		schema, _ := value["schemaname"].(string)
		name, _ := value["relname"].(string)
		if catalog != "" || schema == "" || name == "" {
			return nil, postgresClosedFailure(PostgresClosedModeRequired)
		}
		alias, err := postgresClosedAlias(value["alias"])
		if err != nil {
			return nil, err
		}
		relation := &PostgresClosedRelation{Schema: schema, Name: name, Alias: alias}
		return &PostgresClosedFrom{Relation: relation}, nil
	}
	join, ok := wrapper["JoinExpr"].(map[string]any)
	if !ok || !closedKeys(join, "jointype", "isNatural", "larg", "rarg", "usingClause", "join_using_alias", "quals", "alias", "rtindex") {
		return nil, postgresClosedFailure(PostgresClosedModeRequired)
	}
	kind, _ := join["jointype"].(string)
	if kind != "JOIN_INNER" && kind != "JOIN_LEFT" {
		return nil, postgresClosedFailure(PostgresClosedModeRequired)
	}
	if natural, _ := join["isNatural"].(bool); natural || closedPresent(join["usingClause"]) ||
		closedPresent(join["join_using_alias"]) || closedPresent(join["alias"]) || !closedPresent(join["quals"]) {
		return nil, postgresClosedFailure(PostgresClosedModeRequired)
	}
	left, err := parser.parseFrom(join["larg"], depth+1)
	if err != nil {
		return nil, err
	}
	right, err := parser.parseFrom(join["rarg"], depth+1)
	if err != nil {
		return nil, err
	}
	condition, err := parser.parseExpr(join["quals"], depth+1)
	if err != nil {
		return nil, err
	}
	return &PostgresClosedFrom{Join: &PostgresClosedJoin{Kind: kind, Left: left, Right: right, Condition: condition}}, nil
}

func (parser *postgresClosedParser) parseExpr(value any, depth int) (*PostgresClosedExpr, error) {
	if err := parser.charge(depth); err != nil {
		return nil, err
	}
	wrapper, ok := value.(map[string]any)
	if !ok || len(wrapper) != 1 {
		return nil, postgresClosedFailure(PostgresClosedModeRequired)
	}
	if column, ok := wrapper["ColumnRef"].(map[string]any); ok {
		if !closedKeys(column, "fields", "location") {
			return nil, postgresClosedFailure(PostgresClosedModeRequired)
		}
		fields, ok := column["fields"].([]any)
		if !ok || len(fields) == 0 || len(fields) > 3 {
			return nil, postgresClosedFailure(PostgresClosedColumnShape)
		}
		parts := make([]string, 0, len(fields))
		for _, field := range fields {
			if object, ok := field.(map[string]any); ok {
				if _, star := object["A_Star"]; star {
					return nil, postgresClosedFailure(PostgresClosedColumnShape)
				}
			}
			part, ok := postgresStringNode(field)
			if !ok || part == "" {
				return nil, postgresClosedFailure(PostgresClosedColumnShape)
			}
			parts = append(parts, part)
		}
		return &PostgresClosedExpr{Kind: PostgresClosedColumn, Column: parts}, nil
	}
	if constant, ok := wrapper["A_Const"].(map[string]any); ok {
		return postgresClosedConstant(constant)
	}
	if operation, ok := wrapper["A_Expr"].(map[string]any); ok {
		if !closedKeys(operation, "kind", "name", "lexpr", "rexpr", "location") {
			return nil, postgresClosedFailure(PostgresClosedModeRequired)
		}
		kind, _ := operation["kind"].(string)
		if kind != "AEXPR_OP" || !closedPresent(operation["lexpr"]) || !closedPresent(operation["rexpr"]) {
			return nil, postgresClosedFailure(PostgresClosedModeRequired)
		}
		name := postgresNameFromNodes(operation["name"])
		if !postgresClosedOperatorName(name) {
			return nil, postgresClosedFailure(PostgresClosedModeRequired)
		}
		left, err := parser.parseExpr(operation["lexpr"], depth+1)
		if err != nil {
			return nil, err
		}
		right, err := parser.parseExpr(operation["rexpr"], depth+1)
		if err != nil {
			return nil, err
		}
		return &PostgresClosedExpr{Kind: PostgresClosedOperator, Operator: name, Args: []*PostgresClosedExpr{left, right}}, nil
	}
	if boolean, ok := wrapper["BoolExpr"].(map[string]any); ok {
		if !closedKeys(boolean, "boolop", "args", "location") {
			return nil, postgresClosedFailure(PostgresClosedModeRequired)
		}
		operation, _ := boolean["boolop"].(string)
		arguments, ok := boolean["args"].([]any)
		if !ok || len(arguments) == 0 || operation != "AND_EXPR" && operation != "OR_EXPR" && operation != "NOT_EXPR" || operation == "NOT_EXPR" && len(arguments) != 1 {
			return nil, postgresClosedFailure(PostgresClosedModeRequired)
		}
		result := &PostgresClosedExpr{Kind: PostgresClosedBoolExpr, Operator: operation}
		for _, argument := range arguments {
			expression, err := parser.parseExpr(argument, depth+1)
			if err != nil {
				return nil, err
			}
			result.Args = append(result.Args, expression)
		}
		return result, nil
	}
	if nullTest, ok := wrapper["NullTest"].(map[string]any); ok {
		if !closedKeys(nullTest, "arg", "nulltesttype", "argisrow", "location") {
			return nil, postgresClosedFailure(PostgresClosedModeRequired)
		}
		operation, _ := nullTest["nulltesttype"].(string)
		isRow, _ := nullTest["argisrow"].(bool)
		if isRow || operation != "IS_NULL" && operation != "IS_NOT_NULL" {
			return nil, postgresClosedFailure(PostgresClosedColumnShape)
		}
		argument, err := parser.parseExpr(nullTest["arg"], depth+1)
		if err != nil {
			return nil, err
		}
		return &PostgresClosedExpr{Kind: PostgresClosedNullTest, Operator: operation, Args: []*PostgresClosedExpr{argument}}, nil
	}
	if subLink, ok := wrapper["SubLink"].(map[string]any); ok {
		return parser.parseSubLink(subLink, depth+1)
	}
	return nil, postgresClosedFailure(PostgresClosedModeRequired)
}

func (parser *postgresClosedParser) parseSubLink(node map[string]any, depth int) (*PostgresClosedExpr, error) {
	if !closedKeys(node, "subLinkType", "subLinkId", "testexpr", "operName", "subselect", "location") {
		return nil, postgresClosedFailure(PostgresClosedModeRequired)
	}
	typeName, _ := node["subLinkType"].(string)
	result := &PostgresClosedExpr{Kind: PostgresClosedSubquery, SubLinkType: typeName}
	switch typeName {
	case "EXISTS_SUBLINK":
		if closedPresent(node["testexpr"]) || closedPresent(node["operName"]) {
			return nil, postgresClosedFailure(PostgresClosedModeRequired)
		}
	case "ANY_SUBLINK":
		operator := postgresNameFromNodes(node["operName"])
		if operator != "" && operator != "=" || !closedPresent(node["testexpr"]) {
			return nil, postgresClosedFailure(PostgresClosedModeRequired)
		}
		test, err := parser.parseExpr(node["testexpr"], depth+1)
		if err != nil {
			return nil, err
		}
		result.Operator = "="
		result.Args = []*PostgresClosedExpr{test}
	default:
		return nil, postgresClosedFailure(PostgresClosedModeRequired)
	}
	wrapper, ok := node["subselect"].(map[string]any)
	if !ok || len(wrapper) != 1 {
		return nil, postgresClosedFailure(PostgresClosedModeRequired)
	}
	selectNode, ok := wrapper["SelectStmt"]
	if !ok {
		return nil, postgresClosedFailure(PostgresClosedModeRequired)
	}
	subquery, err := parser.parseSelect(selectNode, depth+1)
	if err != nil {
		return nil, err
	}
	result.Subquery = subquery
	return result, nil
}

func postgresClosedAlias(value any) (string, error) {
	if !closedPresent(value) {
		return "", nil
	}
	alias, ok := value.(map[string]any)
	if !ok || !closedKeys(alias, "aliasname", "colnames") || closedPresent(alias["colnames"]) {
		return "", postgresClosedFailure(PostgresClosedModeRequired)
	}
	name, _ := alias["aliasname"].(string)
	if name == "" {
		return "", postgresClosedFailure(PostgresClosedModeRequired)
	}
	return name, nil
}

func postgresClosedConstant(node map[string]any) (*PostgresClosedExpr, error) {
	if !closedKeys(node, "ival", "boolval", "fval", "sval", "bsval", "isnull", "location") {
		return nil, postgresClosedFailure(PostgresClosedModeRequired)
	}
	if isNull, _ := node["isnull"].(bool); isNull {
		return &PostgresClosedExpr{Kind: PostgresClosedNull}, nil
	}
	if wrapper, ok := node["ival"].(map[string]any); ok {
		if len(wrapper) == 0 {
			return &PostgresClosedExpr{Kind: PostgresClosedInteger}, nil
		}
		value, ok := postgresJSONInteger(wrapper["ival"])
		if !ok {
			return nil, postgresClosedFailure(PostgresClosedModeRequired)
		}
		return &PostgresClosedExpr{Kind: PostgresClosedInteger, Integer: value}, nil
	}
	if wrapper, ok := node["boolval"].(map[string]any); ok {
		if len(wrapper) == 0 {
			return &PostgresClosedExpr{Kind: PostgresClosedBoolean}, nil
		}
		value, ok := wrapper["boolval"].(bool)
		if !ok {
			return nil, postgresClosedFailure(PostgresClosedModeRequired)
		}
		return &PostgresClosedExpr{Kind: PostgresClosedBoolean, Boolean: value}, nil
	}
	return nil, postgresClosedFailure(PostgresClosedModeRequired)
}

func postgresJSONInteger(value any) (int64, bool) {
	switch typed := value.(type) {
	case json.Number:
		result, err := typed.Int64()
		return result, err == nil
	case float64:
		result := int64(typed)
		return result, float64(result) == typed
	case int64:
		return typed, true
	}
	return 0, false
}

func postgresClosedOperatorName(value string) bool {
	switch value {
	case "=", "<>", "!=", "<", ">", "<=", ">=", "+", "-", "*", "/", "%":
		return true
	default:
		return false
	}
}

func closedFromRelations(value *PostgresClosedFrom) []PostgresClosedRelation {
	if value == nil {
		return nil
	}
	if value.Relation != nil {
		return []PostgresClosedRelation{*value.Relation}
	}
	if value.Join == nil {
		return nil
	}
	result := closedFromRelations(value.Join.Left)
	return append(result, closedFromRelations(value.Join.Right)...)
}

func closedExprRelations(value *PostgresClosedExpr) []PostgresClosedRelation {
	if value == nil {
		return nil
	}
	var result []PostgresClosedRelation
	if value.Subquery != nil {
		result = append(result, value.Subquery.Relations...)
	}
	for _, argument := range value.Args {
		result = append(result, closedExprRelations(argument)...)
	}
	return result
}

func closedKeys(value map[string]any, allowed ...string) bool {
	set := make(map[string]struct{}, len(allowed))
	for _, key := range allowed {
		set[key] = struct{}{}
	}
	for key := range value {
		if _, ok := set[key]; !ok {
			return false
		}
	}
	return true
}

func closedPresent(value any) bool {
	if value == nil {
		return false
	}
	switch typed := value.(type) {
	case string:
		return typed != ""
	case []any:
		return len(typed) != 0
	case map[string]any:
		return len(typed) != 0
	case bool:
		return typed
	case float64:
		return typed != 0
	case json.Number:
		return typed.String() != "0"
	default:
		return true
	}
}

func postgresClosedFailure(kind PostgresClosedErrorKind) error {
	return &PostgresClosedError{Kind: kind}
}

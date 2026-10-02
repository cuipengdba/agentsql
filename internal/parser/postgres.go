package parser

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/cuipengdba/agentsql/internal/model"
	pg_query "github.com/pganalyze/pg_query_go/v5"
)

type postgresParser struct{}

var _ Parser = (*postgresParser)(nil)

type postgresDocument struct {
	Statements []postgresRawStatement `json:"stmts"`
}

type postgresRawStatement struct {
	Statement map[string]any `json:"stmt"`
}

func (parser *postgresParser) Parse(sql string) (ast *model.AST, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			ast = &model.AST{Dialect: postgresDialect, RawSQL: sql}
			err = recoveredError(postgresDialect, recovered)
		}
	}()
	return parser.parse(sql)
}

func (parser *postgresParser) parse(sql string) (*model.AST, error) {
	if strings.TrimSpace(sql) == "" {
		return &model.AST{Dialect: postgresDialect, RawSQL: sql}, unparseableError(
			postgresDialect,
			errors.New("SQL is empty"),
		)
	}
	// A PostgreSQL statement cannot contain a second statement when it has no
	// semicolon. Keep the scanner path unchanged for every potentially multi-
	// statement input, while avoiding an otherwise redundant C scanner call on
	// the overwhelmingly common single-statement path.
	if strings.ContainsRune(sql, ';') {
		pieces, err := pg_query.SplitWithScanner(sql, true)
		if err != nil {
			return &model.AST{Dialect: postgresDialect, RawSQL: sql}, unparseableError(postgresDialect, err)
		}
		if len(pieces) != 1 {
			return parser.postgresMultiAST(sql, pieces), unparseableError(
				postgresDialect,
				fmt.Errorf("expected one statement, got %d", len(pieces)),
			)
		}
	}

	parsedJSON, err := pg_query.ParseToJSON(sql)
	if err != nil {
		return &model.AST{Dialect: postgresDialect, RawSQL: sql}, unparseableError(postgresDialect, err)
	}
	document, err := decodePostgresDocument(parsedJSON)
	if err != nil {
		return &model.AST{Dialect: postgresDialect, RawSQL: sql}, unparseableError(postgresDialect, err)
	}
	if len(document.Statements) != 1 {
		return &model.AST{
			Dialect: postgresDialect,
			RawSQL:  sql,
			IsMulti: len(document.Statements) > 1,
		}, unparseableError(postgresDialect, fmt.Errorf("expected one statement, got %d", len(document.Statements)))
	}

	nodeType, node, err := postgresRoot(document.Statements[0])
	if err != nil {
		return &model.AST{Dialect: postgresDialect, RawSQL: sql}, unparseableError(postgresDialect, err)
	}
	scanResult, err := pg_query.Scan(sql)
	if err != nil {
		return &model.AST{Dialect: postgresDialect, RawSQL: sql}, unparseableError(
			postgresDialect,
			fmt.Errorf("scan PostgreSQL SQL before normalization: %w", err),
		)
	}
	normalized, err := normalizePostgresScanResult(sql, scanResult)
	if err != nil {
		return &model.AST{Dialect: postgresDialect, RawSQL: sql}, unparseableError(postgresDialect, err)
	}

	tables := make(objectSet)
	columns := make(stringSet)
	functions := make(stringSet)
	commonTableExpressions := make(stringSet)
	walkPostgresNode(node, func(key string, value any) {
		if object, ok := postgresRangeVar(value); ok {
			tables.add(object)
		}
		switch key {
		case "CommonTableExpr":
			if name, ok := postgresStringField(value, "ctename"); ok {
				commonTableExpressions.add(name)
			}
		case "ColumnRef":
			if column, ok := postgresColumnRef(value); ok {
				columns.add(column)
			}
		case "ColumnDef":
			if column, ok := postgresStringField(value, "colname"); ok {
				columns.add(column)
			}
		case "ResTarget":
			if column, ok := postgresStringField(value, "name"); ok {
				columns.add(column)
			}
		case "FuncCall":
			if function, ok := postgresNameListField(value, "funcname"); ok {
				functions.add(function)
			}
		}
	})
	tables.removeUnqualified(commonTableExpressions)
	if nodeType == "DropStmt" {
		for _, object := range postgresDropObjects(node) {
			tables.add(object)
		}
	}

	statementType, statementOperations, err := postgresStatementSignals(nodeType, node)
	if err != nil {
		return &model.AST{Dialect: postgresDialect, RawSQL: sql}, unparseableError(postgresDialect, err)
	}
	operations := make(stringSet)
	for _, operation := range statementOperations {
		operations.add(operation)
	}
	analysisType, analysisNode, err := postgresAnalysisRoot(nodeType, node)
	if err != nil {
		return &model.AST{Dialect: postgresDialect, RawSQL: sql}, unparseableError(postgresDialect, err)
	}
	nestingDepth, unionCount := postgresQueryComplexity(analysisType, analysisNode)
	operations.add(fmt.Sprintf("%s:%d", nestingDepthOperation, nestingDepth))
	operations.add(fmt.Sprintf("%s:%d", unionCountOperation, unionCount))
	hasGroupBy, isPureAggregate := postgresAggregateShape(analysisType, analysisNode)
	projectionLineages, err := postgresProjectionLineages(nodeType, node)
	if err != nil {
		return &model.AST{Dialect: postgresDialect, RawSQL: sql}, unparseableError(postgresDialect, err)
	}
	directProjections := deriveDirectProjections(projectionLineages)
	for _, column := range postgresProjectedColumns(analysisType, analysisNode) {
		operations.add(selectColumnOperation + ":" + column)
	}
	hasComment := postgresScanHasComment(scanResult)
	if hasComment {
		operations.add(sqlCommentOperation)
	}
	rootObject, rootIsObject := node.(map[string]any)
	hasWhere := false
	hasLimit := false
	whereTautology := false
	if rootIsObject {
		switch statementType {
		case model.StmtType("SELECT"):
			where, exists := rootObject["whereClause"]
			hasWhere = exists && where != nil
			whereTautology = hasWhere && postgresExpressionTautology(where)
			limit, exists := rootObject["limitCount"]
			hasLimit = exists && limit != nil
		case model.StmtType("UPDATE"), model.StmtType("DELETE"):
			where, exists := rootObject["whereClause"]
			hasWhere = exists && where != nil
			whereTautology = hasWhere && postgresExpressionTautology(where)
		}
	}

	return &model.AST{
		Dialect:            postgresDialect,
		RawSQL:             sql,
		Normalized:         normalized,
		StmtType:           statementType,
		IsMulti:            false,
		Tables:             tables.sorted(),
		Columns:            columns.sorted(),
		DirectProjections:  directProjections,
		ProjectionLineages: projectionLineages,
		HasWhere:           hasWhere,
		WhereTautology:     whereTautology,
		HasLimit:           hasLimit,
		HasGroupBy:         hasGroupBy,
		IsPureAggregate:    isPureAggregate,
		Functions:          functions.sorted(),
		Operations:         operations.sorted(),
		Explain:            nil,
	}, nil
}

func (parser *postgresParser) postgresMultiAST(sql string, pieces []string) *model.AST {
	tables := make(objectSet)
	columns := make(stringSet)
	functions := make(stringSet)
	operations := make(stringSet)
	for _, piece := range pieces {
		parsed, err := parser.parse(piece)
		if err != nil || parsed == nil {
			continue
		}
		for _, table := range parsed.Tables {
			tables.add(table)
		}
		for _, column := range parsed.Columns {
			columns.add(column)
		}
		for _, function := range parsed.Functions {
			functions.add(function)
		}
		for _, operation := range parsed.Operations {
			operations.add(operation)
		}
	}
	return &model.AST{
		Dialect:    postgresDialect,
		RawSQL:     sql,
		StmtType:   model.StmtType("UNKNOWN"),
		IsMulti:    len(pieces) > 1,
		Tables:     tables.sorted(),
		Columns:    columns.sorted(),
		Functions:  functions.sorted(),
		Operations: operations.sorted(),
	}
}

func postgresAnalysisRoot(nodeType string, node any) (string, any, error) {
	if nodeType != "ExplainStmt" {
		return nodeType, node, nil
	}
	return postgresExplainQuery(node)
}

func postgresQueryComplexity(nodeType string, node any) (int, int) {
	if nodeType != "SelectStmt" {
		return 0, 0
	}
	return postgresSelectComplexity(node, 0)
}

func postgresSelectComplexity(node any, depth int) (int, int) {
	maximumDepth := depth
	unionCount := 0
	var walk func(any, int)
	walk = func(value any, currentDepth int) {
		switch typed := value.(type) {
		case map[string]any:
			if operation, _ := typed["op"].(string); operation == "SETOP_UNION" {
				unionCount++
			}
			for key, child := range typed {
				childDepth := currentDepth
				if postgresNestedQueryBoundary(key) {
					childDepth++
					if childDepth > maximumDepth {
						maximumDepth = childDepth
					}
				}
				walk(child, childDepth)
			}
		case []any:
			for _, child := range typed {
				walk(child, currentDepth)
			}
		}
	}
	walk(node, depth)
	return maximumDepth, unionCount
}

func postgresNestedQueryBoundary(key string) bool {
	switch key {
	case "subquery", "subselect", "ctequery":
		return true
	default:
		return false
	}
}

func postgresAggregateShape(nodeType string, node any) (bool, bool) {
	if nodeType != "SelectStmt" {
		return false, false
	}
	object, ok := node.(map[string]any)
	if !ok {
		return false, false
	}
	groupClause, _ := object["groupClause"].([]any)
	hasGroupBy := len(groupClause) > 0
	targets, _ := object["targetList"].([]any)
	if hasGroupBy || len(targets) == 0 {
		return hasGroupBy, false
	}
	for _, target := range targets {
		if !postgresTargetIsAggregate(target) {
			return false, false
		}
	}
	return false, true
}

func postgresTargetIsAggregate(value any) bool {
	wrapper, ok := value.(map[string]any)
	if !ok {
		return false
	}
	target, ok := wrapper["ResTarget"].(map[string]any)
	if !ok {
		return false
	}
	return postgresExpressionIsAggregate(target["val"])
}

func postgresExpressionIsAggregate(value any) bool {
	wrapper, ok := value.(map[string]any)
	if !ok || len(wrapper) != 1 {
		return false
	}
	if typeCast, ok := wrapper["TypeCast"].(map[string]any); ok {
		return postgresExpressionIsAggregate(typeCast["arg"])
	}
	function, ok := wrapper["FuncCall"].(map[string]any)
	if !ok {
		return false
	}
	name, ok := postgresNameListField(function, "funcname")
	if !ok {
		return false
	}
	parts := strings.Split(strings.ToLower(name), ".")
	name = parts[len(parts)-1]
	_, ok = postgresAggregateFunctions[name]
	return ok
}

var postgresAggregateFunctions = map[string]struct{}{
	"array_agg": {}, "avg": {}, "bit_and": {}, "bit_or": {}, "bool_and": {},
	"bool_or": {}, "count": {}, "every": {}, "json_agg": {}, "jsonb_agg": {},
	"max": {}, "min": {}, "stddev": {}, "stddev_pop": {}, "stddev_samp": {},
	"string_agg": {}, "sum": {}, "var_pop": {}, "var_samp": {}, "variance": {},
}

func postgresProjectedColumns(nodeType string, node any) []string {
	if nodeType != "SelectStmt" {
		return nil
	}
	object, ok := node.(map[string]any)
	if !ok {
		return nil
	}
	targets, _ := object["targetList"].([]any)
	cteColumns, expandsCTEStar := postgresSingleCTEProjectedColumns(object)
	columns := make(stringSet)
	for _, target := range targets {
		wrapper, _ := target.(map[string]any)
		result, _ := wrapper["ResTarget"].(map[string]any)
		if postgresDirectStarProjection(result["val"]) {
			if expandsCTEStar {
				for _, column := range cteColumns {
					columns.add(column)
				}
			} else {
				columns.add("*")
			}
			continue
		}
		postgresWalkProjection(result["val"], columns)
	}
	return columns.sorted()
}

func postgresDirectProjections(nodeType string, node any) []model.DirectProjectionRef {
	if nodeType != "SelectStmt" {
		return nil
	}
	selectNode, ok := node.(map[string]any)
	if !ok {
		return nil
	}
	if operation, _ := selectNode["op"].(string); operation != "" && operation != "SETOP_NONE" {
		return nil
	}
	bindings := postgresTopBindings(selectNode)
	targets, _ := selectNode["targetList"].([]any)
	items := make([]directProjectionItem, len(targets))
	for index, target := range targets {
		wrapper, _ := target.(map[string]any)
		result, _ := wrapper["ResTarget"].(map[string]any)
		valueWrapper, ok := result["val"].(map[string]any)
		if !ok || len(valueWrapper) != 1 {
			continue
		}
		column, ok := valueWrapper["ColumnRef"].(map[string]any)
		if !ok {
			continue
		}
		name, qualifiers, star := postgresDirectColumn(column)
		items[index] = directProjectionItem{
			column: name,
			star:   star,
			source: resolveDirectProjectionSource(qualifiers, bindings),
		}
	}
	return positionDirectProjections(items)
}

func postgresDirectColumn(column map[string]any) (string, []string, bool) {
	fields, ok := column["fields"].([]any)
	if !ok || len(fields) == 0 {
		return "", nil, false
	}
	last := fields[len(fields)-1]
	if wrapper, ok := last.(map[string]any); ok {
		if _, star := wrapper["A_Star"]; star {
			return "", nil, true
		}
	}
	name, ok := postgresStringNode(last)
	if !ok {
		return "", nil, false
	}
	qualifiers := make([]string, 0, len(fields)-1)
	for _, field := range fields[:len(fields)-1] {
		qualifier, ok := postgresStringNode(field)
		if !ok {
			return name, nil, false
		}
		qualifiers = append(qualifiers, qualifier)
	}
	return name, qualifiers, false
}

func postgresTopBindings(selectNode map[string]any) []topRelationBinding {
	cteNames := make(stringSet)
	if withClause, ok := selectNode["withClause"].(map[string]any); ok {
		if ctes, ok := withClause["ctes"].([]any); ok {
			for _, item := range ctes {
				wrapper, _ := item.(map[string]any)
				if cte, ok := wrapper["CommonTableExpr"].(map[string]any); ok {
					if name, ok := postgresStringField(cte, "ctename"); ok {
						cteNames.add(name)
					}
				}
			}
		}
	}
	fromClause, _ := selectNode["fromClause"].([]any)
	bindings := make([]topRelationBinding, 0, len(fromClause))
	for _, expression := range fromClause {
		postgresAppendTopBindings(expression, cteNames, &bindings)
	}
	return bindings
}

func postgresAppendTopBindings(value any, cteNames stringSet, bindings *[]topRelationBinding) {
	wrapper, ok := value.(map[string]any)
	if !ok || len(wrapper) == 0 {
		*bindings = append(*bindings, nonPhysicalTopBinding("", ""))
		return
	}
	if rangeVar, ok := wrapper["RangeVar"].(map[string]any); ok {
		object, valid := postgresRangeVar(rangeVar)
		if !valid {
			*bindings = append(*bindings, nonPhysicalTopBinding("", ""))
			return
		}
		if object.Schema == "" {
			if _, isCTE := cteNames[object.Table]; isCTE {
				*bindings = append(*bindings, nonPhysicalTopBinding(object.Table, object.Alias))
				return
			}
		}
		*bindings = append(*bindings, physicalTopBinding(object))
		return
	}
	if join, ok := wrapper["JoinExpr"].(map[string]any); ok {
		if alias := postgresAliasName(join); alias != "" {
			*bindings = append(*bindings, nonPhysicalTopBinding("", alias))
			return
		}
		postgresAppendTopBindings(join["larg"], cteNames, bindings)
		postgresAppendTopBindings(join["rarg"], cteNames, bindings)
		return
	}
	for _, kind := range []string{"RangeSubselect", "RangeFunction", "RangeTableFunc"} {
		if source, ok := wrapper[kind].(map[string]any); ok {
			*bindings = append(*bindings, nonPhysicalTopBinding("", postgresAliasName(source)))
			return
		}
	}
	if sample, ok := wrapper["RangeTableSample"].(map[string]any); ok {
		postgresAppendTopBindings(sample["relation"], cteNames, bindings)
		return
	}
	*bindings = append(*bindings, nonPhysicalTopBinding("", postgresAliasName(wrapper)))
}

func postgresAliasName(value map[string]any) string {
	alias, _ := value["alias"].(map[string]any)
	name, _ := alias["aliasname"].(string)
	return name
}

func postgresDirectStarProjection(value any) bool {
	wrapper, ok := value.(map[string]any)
	if !ok {
		return false
	}
	column, ok := wrapper["ColumnRef"]
	if !ok {
		return false
	}
	name, ok := postgresColumnRef(column)
	return ok && name == "*"
}

func postgresSingleCTEProjectedColumns(selectNode map[string]any) ([]string, bool) {
	from, _ := selectNode["fromClause"].([]any)
	if len(from) != 1 {
		return nil, false
	}
	fromWrapper, _ := from[0].(map[string]any)
	rangeVar, _ := fromWrapper["RangeVar"].(map[string]any)
	source, ok := postgresStringField(rangeVar, "relname")
	if !ok {
		return nil, false
	}
	if schema, _ := rangeVar["schemaname"].(string); schema != "" {
		return nil, false
	}
	withClause, _ := selectNode["withClause"].(map[string]any)
	ctes, _ := withClause["ctes"].([]any)
	for _, item := range ctes {
		itemWrapper, _ := item.(map[string]any)
		cte, _ := itemWrapper["CommonTableExpr"].(map[string]any)
		name, ok := postgresStringField(cte, "ctename")
		if !ok || name != source {
			continue
		}
		queryWrapper, _ := cte["ctequery"].(map[string]any)
		query, ok := queryWrapper["SelectStmt"]
		if !ok {
			return nil, false
		}
		columns := postgresProjectedColumns("SelectStmt", query)
		if len(columns) == 0 {
			return nil, false
		}
		return columns, true
	}
	return nil, false
}

func postgresWalkProjection(value any, columns stringSet) {
	switch typed := value.(type) {
	case map[string]any:
		if column, exists := typed["ColumnRef"]; exists {
			if name, ok := postgresColumnRef(column); ok {
				columns.add(name)
			}
			return
		}
		if _, isSubquery := typed["SelectStmt"]; isSubquery {
			return
		}
		for _, child := range typed {
			postgresWalkProjection(child, columns)
		}
	case []any:
		for _, child := range typed {
			postgresWalkProjection(child, columns)
		}
	}
}

func normalizePostgres(sql string) (string, error) {
	scanResult, err := pg_query.Scan(sql)
	if err != nil {
		return "", fmt.Errorf("scan PostgreSQL SQL before normalization: %w", err)
	}
	return normalizePostgresScanResult(sql, scanResult)
}

func normalizePostgresScanResult(sql string, scanResult *pg_query.ScanResult) (string, error) {
	if !postgresScanContainsLiteral(scanResult) {
		return sql, nil
	}
	normalized, err := pg_query.Normalize(sql)
	if err != nil {
		return "", fmt.Errorf("normalize PostgreSQL SQL: %w", err)
	}
	redacted, err := redactPostgresStringConstants(normalized)
	if err != nil {
		return "", fmt.Errorf("redact PostgreSQL string constants: %w", err)
	}
	return redacted, nil
}

func postgresScanContainsLiteral(scanResult *pg_query.ScanResult) bool {
	if scanResult == nil {
		return false
	}
	for _, token := range scanResult.GetTokens() {
		switch token.GetToken().String() {
		case "ICONST", "FCONST", "SCONST", "BCONST", "XCONST", "USCONST":
			return true
		}
	}
	return false
}

func redactPostgresStringConstants(sql string) (string, error) {
	scanResult, err := pg_query.Scan(sql)
	if err != nil {
		return "", fmt.Errorf("scan normalized PostgreSQL SQL: %w", err)
	}

	var builder strings.Builder
	builder.Grow(len(sql))
	cursor := 0
	replaced := false
	for _, token := range scanResult.GetTokens() {
		switch token.GetToken().String() {
		case "SCONST", "USCONST":
		default:
			continue
		}

		start := int(token.GetStart())
		end := int(token.GetEnd())
		if start < cursor || end < start || end > len(sql) {
			return "", fmt.Errorf(
				"invalid PostgreSQL string token range [%d,%d) for %d-byte SQL: %w",
				start,
				end,
				len(sql),
				errors.New("scanner token range is invalid"),
			)
		}
		builder.WriteString(sql[cursor:start])
		builder.WriteByte('?')
		cursor = end
		replaced = true
	}
	if !replaced {
		return sql, nil
	}
	builder.WriteString(sql[cursor:])
	return builder.String(), nil
}

func postgresHasComment(sql string) (bool, error) {
	scanResult, err := pg_query.Scan(sql)
	if err != nil {
		return false, fmt.Errorf("scan PostgreSQL tokens: %w", err)
	}
	return postgresScanHasComment(scanResult), nil
}

func postgresScanHasComment(scanResult *pg_query.ScanResult) bool {
	if scanResult == nil {
		return false
	}
	for _, token := range scanResult.GetTokens() {
		switch token.GetToken().String() {
		case "C_COMMENT", "SQL_COMMENT":
			return true
		}
	}
	return false
}

func decodePostgresDocument(parsedJSON string) (postgresDocument, error) {
	decoder := json.NewDecoder(strings.NewReader(parsedJSON))
	decoder.UseNumber()
	var document postgresDocument
	if err := decoder.Decode(&document); err != nil {
		return postgresDocument{}, fmt.Errorf("decode PostgreSQL AST: %w", err)
	}
	return document, nil
}

func postgresRoot(statement postgresRawStatement) (string, any, error) {
	if len(statement.Statement) != 1 {
		return "", nil, fmt.Errorf("expected one PostgreSQL root node, got %d", len(statement.Statement))
	}
	for nodeType, node := range statement.Statement {
		if node == nil {
			return "", nil, fmt.Errorf("PostgreSQL %s root node is null", nodeType)
		}
		return nodeType, node, nil
	}
	return "", nil, errors.New("PostgreSQL statement has no root node")
}

func postgresStatementType(nodeType string) model.StmtType {
	switch nodeType {
	case "SelectStmt":
		return model.StmtType("SELECT")
	case "InsertStmt":
		return model.StmtType("INSERT")
	case "UpdateStmt":
		return model.StmtType("UPDATE")
	case "DeleteStmt":
		return model.StmtType("DELETE")
	case "CreateStmt", "CreateTableAsStmt", "AlterTableStmt", "AlterDomainStmt",
		"AlterObjectSchemaStmt", "AlterOwnerStmt", "CompositeTypeStmt", "CreateEnumStmt",
		"CreateFunctionStmt", "CreateSchemaStmt", "CreateSeqStmt", "CreateTrigStmt",
		"DefineStmt", "DropStmt", "IndexStmt", "RenameStmt", "RuleStmt",
		"TruncateStmt", "ViewStmt", "CreatedbStmt", "DropdbStmt", "AlterDatabaseStmt",
		"AlterDatabaseSetStmt", "AlterDatabaseRefreshCollStmt", "CreateExtensionStmt",
		"AlterSeqStmt", "RefreshMatViewStmt", "CommentStmt":
		return model.StmtType("DDL")
	case "CopyStmt", "DoStmt", "GrantStmt", "GrantRoleStmt", "ReindexStmt",
		"TransactionStmt", "VacuumStmt", "VariableSetStmt", "ClusterStmt",
		"AlterSystemStmt", "AlterRoleStmt", "AlterRoleSetStmt", "CreateRoleStmt",
		"DropRoleStmt", "CheckPointStmt", "LockStmt", "LoadStmt", "DiscardStmt",
		"PrepareStmt", "ExecuteStmt", "DeallocateStmt", "CallStmt", "NotifyStmt",
		"ListenStmt", "UnlistenStmt":
		return model.StmtType("ADMIN")
	default:
		return model.StmtType("UNKNOWN")
	}
}

func postgresStatementSignals(
	nodeType string,
	node any,
) (model.StmtType, []string, error) {
	if nodeType != "ExplainStmt" {
		if nodeType == "SelectStmt" {
			if root, ok := node.(map[string]any); ok {
				if into, exists := root["intoClause"]; exists && into != nil {
					return model.StmtType("SELECT"), []string{"SELECT", "SELECT INTO"}, nil
				}
			}
		}
		return postgresStatementType(nodeType), []string{postgresOperation(nodeType, node)}, nil
	}

	innerType, innerNode, err := postgresExplainQuery(node)
	if err != nil {
		return model.StmtType("UNKNOWN"), nil, err
	}
	statementType, operations, err := postgresStatementSignals(innerType, innerNode)
	if err != nil {
		return model.StmtType("UNKNOWN"), nil, err
	}
	explainOperation := "EXPLAIN"
	if root, ok := node.(map[string]any); ok && postgresDefElemEnabled(root["options"], "analyze") {
		explainOperation = "EXPLAIN ANALYZE"
	}
	return statementType, append([]string{explainOperation}, operations...), nil
}

func postgresExplainQuery(node any) (string, any, error) {
	root, ok := node.(map[string]any)
	if !ok {
		return "", nil, errors.New("PostgreSQL EXPLAIN node is not an object")
	}
	query, ok := root["query"].(map[string]any)
	if !ok || len(query) != 1 {
		return "", nil, errors.New("PostgreSQL EXPLAIN has no single inner statement")
	}
	for nodeType, innerNode := range query {
		return nodeType, innerNode, nil
	}
	return "", nil, errors.New("PostgreSQL EXPLAIN inner statement is empty")
}

func postgresOperation(nodeType string, node any) string {
	object, _ := node.(map[string]any)
	switch nodeType {
	case "SelectStmt":
		return "SELECT"
	case "InsertStmt":
		return "INSERT"
	case "UpdateStmt":
		return "UPDATE"
	case "DeleteStmt":
		return "DELETE"
	case "CreateStmt", "CreateTableAsStmt":
		return "CREATE TABLE"
	case "CreatedbStmt":
		return "CREATE DATABASE"
	case "DropdbStmt":
		return "DROP DATABASE"
	case "AlterDatabaseStmt", "AlterDatabaseSetStmt", "AlterDatabaseRefreshCollStmt":
		return "ALTER DATABASE"
	case "CreateExtensionStmt":
		return "CREATE EXTENSION"
	case "AlterSeqStmt":
		return "ALTER SEQUENCE"
	case "RefreshMatViewStmt":
		return "REFRESH MATERIALIZED VIEW"
	case "CommentStmt":
		return "COMMENT ON"
	case "AlterTableStmt":
		return "ALTER TABLE"
	case "IndexStmt":
		if concurrent, _ := object["concurrent"].(bool); concurrent {
			return "CREATE INDEX CONCURRENTLY"
		}
		return "CREATE INDEX"
	case "DropStmt":
		removeType, _ := object["removeType"].(string)
		return "DROP " + postgresObjectType(removeType)
	case "TruncateStmt":
		return "TRUNCATE TABLE"
	case "VacuumStmt":
		if postgresContainsDefElem(object["options"], "full") {
			return "VACUUM FULL"
		}
		if isVacuum, ok := object["isVacuumcmd"].(bool); ok && !isVacuum {
			return "ANALYZE"
		}
		return "VACUUM"
	case "ReindexStmt":
		return "REINDEX"
	case "ClusterStmt":
		return "CLUSTER"
	case "GrantStmt":
		isGrant, found := object["is_grant"].(bool)
		if !found {
			isGrant, found = object["isGrant"].(bool)
		}
		if found && !isGrant {
			return "REVOKE"
		}
		return "GRANT"
	case "GrantRoleStmt":
		isGrant, found := object["is_grant"].(bool)
		if !found {
			isGrant, found = object["isGrant"].(bool)
		}
		if found && !isGrant {
			return "REVOKE ROLE"
		}
		return "GRANT ROLE"
	case "CopyStmt":
		if isProgram, ok := object["is_program"].(bool); ok && isProgram {
			return "COPY PROGRAM"
		}
		if isProgram, ok := object["isProgram"].(bool); ok && isProgram {
			return "COPY PROGRAM"
		}
		return "COPY"
	case "VariableSetStmt":
		return "SET"
	case "TransactionStmt":
		return "TRANSACTION"
	case "AlterSystemStmt":
		return "ALTER SYSTEM"
	case "AlterRoleStmt", "AlterRoleSetStmt":
		return "ALTER ROLE"
	case "CreateRoleStmt":
		return "CREATE ROLE"
	case "DropRoleStmt":
		return "DROP ROLE"
	case "CheckPointStmt":
		return "CHECKPOINT"
	case "LockStmt":
		return "LOCK TABLE"
	case "LoadStmt":
		return "LOAD"
	case "DiscardStmt":
		return "DISCARD"
	case "PrepareStmt":
		return "PREPARE"
	case "ExecuteStmt":
		return "EXECUTE"
	case "DeallocateStmt":
		return "DEALLOCATE"
	case "CallStmt":
		return "CALL"
	case "NotifyStmt":
		return "NOTIFY"
	case "ListenStmt":
		return "LISTEN"
	case "UnlistenStmt":
		return "UNLISTEN"
	default:
		return strings.ToUpper(strings.TrimSuffix(nodeType, "Stmt"))
	}
}

func postgresObjectType(removeType string) string {
	switch removeType {
	case "OBJECT_SCHEMA":
		return "SCHEMA"
	case "OBJECT_SEQUENCE":
		return "SEQUENCE"
	case "OBJECT_FUNCTION":
		return "FUNCTION"
	case "OBJECT_PROCEDURE":
		return "PROCEDURE"
	case "OBJECT_VIEW":
		return "VIEW"
	case "OBJECT_MATVIEW":
		return "MATERIALIZED VIEW"
	}
	value := strings.TrimPrefix(removeType, "OBJECT_")
	value = strings.ReplaceAll(value, "_", " ")
	if value == "" {
		return "OBJECT"
	}
	return value
}

func walkPostgresNode(value any, visit func(key string, value any)) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			visit(key, child)
			walkPostgresNode(child, visit)
		}
	case []any:
		for _, child := range typed {
			walkPostgresNode(child, visit)
		}
	}
}

func collectPostgresRangeVars(value any, tables objectSet) {
	switch typed := value.(type) {
	case map[string]any:
		if object, ok := postgresRangeVar(typed); ok {
			tables.add(object)
		}
		for _, child := range typed {
			collectPostgresRangeVars(child, tables)
		}
	case []any:
		for _, child := range typed {
			collectPostgresRangeVars(child, tables)
		}
	}
}

func postgresRangeVar(value any) (model.ObjectRef, bool) {
	object, ok := value.(map[string]any)
	if !ok {
		return model.ObjectRef{}, false
	}
	table, ok := object["relname"].(string)
	if !ok || table == "" {
		return model.ObjectRef{}, false
	}
	schema, _ := object["schemaname"].(string)
	alias := ""
	if aliasObject, ok := object["alias"].(map[string]any); ok {
		if aliasName, exists := aliasObject["aliasname"].(string); exists {
			alias = aliasName
		}
	}
	return model.ObjectRef{Schema: schema, Table: table, Alias: alias}, true
}

func postgresColumnRef(value any) (string, bool) {
	object, ok := value.(map[string]any)
	if !ok {
		return "", false
	}
	fields, ok := object["fields"].([]any)
	if !ok || len(fields) == 0 {
		return "", false
	}
	for index := len(fields) - 1; index >= 0; index-- {
		if name, ok := postgresStringNode(fields[index]); ok {
			return name, true
		}
		if wrapper, ok := fields[index].(map[string]any); ok {
			if _, isStar := wrapper["A_Star"]; isStar {
				return "*", true
			}
		}
	}
	return "", false
}

func postgresStringField(value any, field string) (string, bool) {
	object, ok := value.(map[string]any)
	if !ok {
		return "", false
	}
	result, ok := object[field].(string)
	return result, ok && result != ""
}

func postgresNameListField(value any, field string) (string, bool) {
	object, ok := value.(map[string]any)
	if !ok {
		return "", false
	}
	items, ok := object[field].([]any)
	if !ok {
		return "", false
	}
	parts := make([]string, 0, len(items))
	for _, item := range items {
		if part, ok := postgresStringNode(item); ok {
			parts = append(parts, part)
		}
	}
	if len(parts) == 0 {
		return "", false
	}
	return strings.Join(parts, "."), true
}

func postgresStringNode(value any) (string, bool) {
	wrapper, ok := value.(map[string]any)
	if !ok {
		return "", false
	}
	stringObject, ok := wrapper["String"].(map[string]any)
	if !ok {
		return "", false
	}
	valueString, ok := stringObject["sval"].(string)
	return valueString, ok
}

func postgresDropObjects(node any) []model.ObjectRef {
	root, ok := node.(map[string]any)
	if !ok {
		return nil
	}
	objects, ok := root["objects"].([]any)
	if !ok {
		return nil
	}
	result := make([]model.ObjectRef, 0, len(objects))
	for _, object := range objects {
		wrapper, ok := object.(map[string]any)
		if !ok {
			continue
		}
		list, ok := wrapper["List"].(map[string]any)
		if !ok {
			continue
		}
		items, ok := list["items"].([]any)
		if !ok || len(items) == 0 {
			continue
		}
		parts := make([]string, 0, len(items))
		for _, item := range items {
			if part, ok := postgresStringNode(item); ok {
				parts = append(parts, part)
			}
		}
		if len(parts) == 1 {
			result = append(result, model.ObjectRef{Table: parts[0]})
		} else if len(parts) >= 2 {
			result = append(result, model.ObjectRef{Schema: parts[len(parts)-2], Table: parts[len(parts)-1]})
		}
	}
	return result
}

func postgresContainsDefElem(value any, name string) bool {
	found := false
	walkPostgresNode(value, func(key string, child any) {
		if found || key != "DefElem" {
			return
		}
		if defname, ok := postgresStringField(child, "defname"); ok && strings.EqualFold(defname, name) {
			found = true
		}
	})
	return found
}

func postgresDefElemEnabled(value any, name string) bool {
	found := false
	enabled := false
	walkPostgresNode(value, func(key string, child any) {
		if found || key != "DefElem" {
			return
		}
		defElem, ok := child.(map[string]any)
		if !ok {
			return
		}
		defname, ok := postgresStringField(defElem, "defname")
		if !ok || !strings.EqualFold(defname, name) {
			return
		}
		found = true
		argument, hasArgument := defElem["arg"]
		if !hasArgument || argument == nil {
			enabled = true
			return
		}
		if boolean, ok := postgresBooleanNode(argument); ok {
			enabled = boolean
			return
		}
		// Unknown option encodings are treated as enabled to avoid hiding execution.
		enabled = true
	})
	return found && enabled
}

func postgresBooleanNode(value any) (bool, bool) {
	wrapper, ok := value.(map[string]any)
	if !ok {
		return false, false
	}
	if boolean, exists := wrapper["Boolean"].(map[string]any); exists {
		valueBool, hasValue := boolean["boolval"].(bool)
		if !hasValue {
			// Proto JSON omits the default false scalar.
			return false, true
		}
		return valueBool, true
	}
	if stringNode, exists := wrapper["String"].(map[string]any); exists {
		valueString, hasValue := stringNode["sval"].(string)
		if !hasValue {
			return false, false
		}
		switch strings.ToLower(strings.TrimSpace(valueString)) {
		case "true":
			return true, true
		case "false":
			return false, true
		default:
			return false, false
		}
	}
	if integer, exists := wrapper["Integer"].(map[string]any); exists {
		valueInteger, hasValue := postgresIntegerValue(integer["ival"])
		if !hasValue {
			return false, false
		}
		switch valueInteger {
		case 1:
			return true, true
		case 0:
			return false, true
		default:
			return false, false
		}
	}
	return false, false
}

func postgresIntegerValue(value any) (int64, bool) {
	switch typed := value.(type) {
	case json.Number:
		integer, err := typed.Int64()
		return integer, err == nil
	case int:
		return int64(typed), true
	case int64:
		return typed, true
	case float64:
		if typed == 0 || typed == 1 {
			return int64(typed), true
		}
	}
	return 0, false
}

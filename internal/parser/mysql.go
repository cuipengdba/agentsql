package parser

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/cuipengdba/agentsql/internal/model"
	"vitess.io/vitess/go/vt/sqlparser"
)

type mysqlParser struct {
	parser *sqlparser.Parser
}

var _ Parser = (*mysqlParser)(nil)

func newMySQLParser() (Parser, error) {
	approvedParser, err := sqlparser.New(sqlparser.Options{})
	if err != nil {
		return nil, fmt.Errorf("create Vitess MySQL parser: %w", err)
	}
	return &mysqlParser{parser: approvedParser}, nil
}

func (parser *mysqlParser) Parse(sql string) (ast *model.AST, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			ast = &model.AST{Dialect: mysqlDialect, RawSQL: sql}
			err = recoveredError(mysqlDialect, recovered)
		}
	}()
	return parser.parse(sql)
}

func (parser *mysqlParser) parse(sql string) (*model.AST, error) {
	if strings.TrimSpace(sql) == "" {
		return &model.AST{Dialect: mysqlDialect, RawSQL: sql}, unparseableError(
			mysqlDialect,
			errors.New("SQL is empty"),
		)
	}

	pieces, err := parser.parser.SplitStatementToPieces(sql)
	if err != nil {
		return &model.AST{Dialect: mysqlDialect, RawSQL: sql}, unparseableError(mysqlDialect, err)
	}
	if len(pieces) != 1 {
		return parser.mysqlMultiAST(sql, pieces), unparseableError(
			mysqlDialect,
			fmt.Errorf("expected one statement, got %d", len(pieces)),
		)
	}

	statement, err := parser.parser.ParseStrictDDL(sql)
	if err != nil {
		return &model.AST{Dialect: mysqlDialect, RawSQL: sql}, unparseableError(mysqlDialect, err)
	}
	normalized, err := normalizeMySQL(parser.parser, statement, sql)
	if err != nil {
		return &model.AST{Dialect: mysqlDialect, RawSQL: sql}, unparseableError(mysqlDialect, err)
	}

	statementType := mysqlStatementType(statement)
	tables := make(objectSet)
	columns := make(stringSet)
	functions := make(stringSet)
	commonTableExpressions := make(stringSet)
	if err := sqlparser.Walk(func(node sqlparser.SQLNode) (bool, error) {
		switch typed := node.(type) {
		case *sqlparser.CommonTableExpr:
			commonTableExpressions.add(typed.ID.String())
		case *sqlparser.AliasedTableExpr:
			if tableName, ok := typed.Expr.(sqlparser.TableName); ok {
				tables.add(model.ObjectRef{
					Schema: tableName.Qualifier.String(),
					Table:  tableName.Name.String(),
					Alias:  typed.As.String(),
				})
			}
		case *sqlparser.ColName:
			columns.add(typed.Name.String())
		case *sqlparser.StarExpr:
			columns.add("*")
		case *sqlparser.ColumnDefinition:
			columns.add(typed.Name.String())
		case *sqlparser.FuncExpr:
			functions.add(typed.Name.Lowered())
		case sqlparser.AggrFunc:
			functions.add(typed.AggrName())
		case *sqlparser.CurTimeFuncExpr:
			functions.add(typed.Name.Lowered())
		}
		return true, nil
	}, statement); err != nil {
		return &model.AST{Dialect: mysqlDialect, RawSQL: sql}, unparseableError(mysqlDialect, err)
	}
	tables.removeUnqualifiedFold(commonTableExpressions)
	tables.removeUnqualifiedFold(stringSet{"dual": {}})

	if ddl, ok := statement.(sqlparser.DDLStatement); ok {
		for _, tableName := range ddl.AffectedTables() {
			tables.add(model.ObjectRef{Schema: tableName.Qualifier.String(), Table: tableName.Name.String()})
		}
	}
	if insert, ok := statement.(*sqlparser.Insert); ok {
		for _, column := range insert.Columns {
			columns.add(column.String())
		}
	}
	if analyze, ok := statement.(*sqlparser.Analyze); ok {
		tables.add(model.ObjectRef{
			Schema: analyze.Table.Qualifier.String(),
			Table:  analyze.Table.Name.String(),
		})
	}

	operations := make(stringSet)
	operation, err := mysqlOperation(parser.parser, statement, sql)
	if err != nil {
		return &model.AST{Dialect: mysqlDialect, RawSQL: sql}, unparseableError(mysqlDialect, err)
	}
	operations.add(operation)
	if explained, ok := statement.(*sqlparser.ExplainStmt); ok {
		if explained.Type == sqlparser.AnalyzeType {
			operations.add("EXPLAIN ANALYZE")
		} else {
			operations.add("EXPLAIN")
		}
	}
	nestingDepth, unionCount := mysqlQueryComplexity(statement)
	operations.add(fmt.Sprintf("%s:%d", nestingDepthOperation, nestingDepth))
	operations.add(fmt.Sprintf("%s:%d", unionCountOperation, unionCount))
	hasGroupBy, isPureAggregate := mysqlAggregateShape(statement)
	directProjections := mysqlDirectProjections(statement)
	for _, column := range mysqlProjectedColumns(statement) {
		operations.add(selectColumnOperation + ":" + column)
	}
	hasComment, err := mysqlHasComment(parser.parser, sql)
	if err != nil {
		return &model.AST{Dialect: mysqlDialect, RawSQL: sql}, unparseableError(mysqlDialect, err)
	}
	if hasComment {
		operations.add(sqlCommentOperation)
	}
	for function := range functions {
		if strings.EqualFold(function, "load_file") {
			operations.add("LOAD_FILE")
		}
	}
	if selectStatement, ok := statement.(*sqlparser.Select); ok && selectStatement.Into != nil {
		switch selectStatement.Into.Type {
		case sqlparser.IntoOutfile, sqlparser.IntoOutfileS3:
			operations.add("INTO OUTFILE")
		case sqlparser.IntoDumpfile:
			operations.add("INTO DUMPFILE")
		}
	}
	if unionStatement, ok := statement.(*sqlparser.Union); ok && unionStatement.Into != nil {
		switch unionStatement.Into.Type {
		case sqlparser.IntoOutfile, sqlparser.IntoOutfileS3:
			operations.add("INTO OUTFILE")
		case sqlparser.IntoDumpfile:
			operations.add("INTO DUMPFILE")
		}
	}

	hasWhere, whereExpression := mysqlRootWhere(statement)
	return &model.AST{
		Dialect:           mysqlDialect,
		RawSQL:            sql,
		Normalized:        normalized,
		StmtType:          statementType,
		IsMulti:           false,
		Tables:            tables.sorted(),
		Columns:           columns.sorted(),
		DirectProjections: directProjections,
		HasWhere:          hasWhere,
		WhereTautology:    hasWhere && mysqlExpressionTautology(whereExpression),
		HasLimit:          mysqlHasLimit(statement),
		HasGroupBy:        hasGroupBy,
		IsPureAggregate:   isPureAggregate,
		Functions:         functions.sorted(),
		Operations:        operations.sorted(),
		Explain:           nil,
	}, nil
}

func (parser *mysqlParser) mysqlMultiAST(sql string, pieces []string) *model.AST {
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
		Dialect:    mysqlDialect,
		RawSQL:     sql,
		StmtType:   model.StmtType("UNKNOWN"),
		IsMulti:    len(pieces) > 1,
		Tables:     tables.sorted(),
		Columns:    columns.sorted(),
		Functions:  functions.sorted(),
		Operations: operations.sorted(),
	}
}

func mysqlHasComment(parser *sqlparser.Parser, sql string) (bool, error) {
	// Vitess expands MySQL version comments before exposing tokens, so retain
	// the raw conditional-comment signal for R006 before scanning the stream.
	if strings.Contains(sql, "/*!") {
		return true, nil
	}
	tokenizer := parser.NewStringTokenizer(sql)
	for {
		token, value := tokenizer.Scan()
		if token == sqlparser.COMMENT {
			if strings.HasPrefix(strings.TrimSpace(value), "/*+") {
				continue
			}
			return true, nil
		}
		if token == sqlparser.LEX_ERROR {
			return false, errors.New("MySQL lexer rejected a token")
		}
		if token == 0 {
			return false, nil
		}
	}
}

func normalizeMySQL(parser *sqlparser.Parser, statement sqlparser.Statement, sql string) (string, error) {
	normalized, err := parser.RedactSQLQuery(sql)
	if err != nil {
		return "", fmt.Errorf("redact MySQL literals: %w", err)
	}
	if !mysqlHasOutputFile(statement) {
		return normalized, nil
	}
	normalizedStatement, err := parser.ParseStrictDDL(normalized)
	if err != nil {
		return "", fmt.Errorf("parse normalized MySQL SQL: %w", err)
	}
	switch typed := normalizedStatement.(type) {
	case *sqlparser.Select:
		redactMySQLSelectInto(typed.Into)
	case *sqlparser.Union:
		redactMySQLSelectInto(typed.Into)
	}
	return sqlparser.String(normalizedStatement), nil
}

func mysqlHasOutputFile(statement sqlparser.Statement) bool {
	switch typed := statement.(type) {
	case *sqlparser.Select:
		return typed.Into != nil && typed.Into.FileName != ""
	case *sqlparser.Union:
		return typed.Into != nil && typed.Into.FileName != ""
	default:
		return false
	}
}

func redactMySQLSelectInto(into *sqlparser.SelectInto) {
	if into != nil && into.FileName != "" {
		into.FileName = "?"
	}
}

func mysqlStatementType(statement sqlparser.Statement) model.StmtType {
	switch typed := statement.(type) {
	case *sqlparser.Select, *sqlparser.Union:
		return model.StmtType("SELECT")
	case *sqlparser.Insert:
		return model.StmtType("INSERT")
	case *sqlparser.Update:
		return model.StmtType("UPDATE")
	case *sqlparser.Delete:
		return model.StmtType("DELETE")
	case sqlparser.DDLStatement, sqlparser.DBDDLStatement:
		return model.StmtType("DDL")
	case *sqlparser.ExplainStmt:
		if typed == nil || typed.Statement == nil {
			return model.StmtType("UNKNOWN")
		}
		return mysqlStatementType(typed.Statement)
	case *sqlparser.Set, *sqlparser.Flush, *sqlparser.Show, *sqlparser.Use,
		*sqlparser.Begin, *sqlparser.Commit, *sqlparser.Rollback, *sqlparser.SRollback,
		*sqlparser.Savepoint, *sqlparser.Release, *sqlparser.CallProc, *sqlparser.LockTables,
		*sqlparser.UnlockTables, *sqlparser.ExplainTab,
		*sqlparser.PrepareStmt, *sqlparser.ExecuteStmt, *sqlparser.DeallocateStmt,
		*sqlparser.Analyze, *sqlparser.OtherAdmin, *sqlparser.Load,
		*sqlparser.PurgeBinaryLogs, *sqlparser.Kill:
		return model.StmtType("ADMIN")
	default:
		return model.StmtType("UNKNOWN")
	}
}

func mysqlOperation(parser *sqlparser.Parser, statement sqlparser.Statement, sql string) (string, error) {
	switch typed := statement.(type) {
	case *sqlparser.Select, *sqlparser.Union:
		return "SELECT", nil
	case *sqlparser.Insert:
		return "INSERT", nil
	case *sqlparser.Update:
		return "UPDATE", nil
	case *sqlparser.Delete:
		return "DELETE", nil
	case *sqlparser.DropTable:
		return "DROP TABLE", nil
	case *sqlparser.TruncateTable:
		return "TRUNCATE TABLE", nil
	case *sqlparser.CreateTable:
		return "CREATE TABLE", nil
	case *sqlparser.AlterTable:
		return "ALTER TABLE", nil
	case *sqlparser.DropDatabase:
		return "DROP DATABASE", nil
	case *sqlparser.CreateDatabase:
		return "CREATE DATABASE", nil
	case sqlparser.DDLStatement:
		return strings.ToUpper(typed.GetAction().ToString()), nil
	case *sqlparser.ExplainStmt:
		if typed == nil || typed.Statement == nil {
			return "", errors.New("MySQL EXPLAIN has no inner statement")
		}
		return mysqlOperation(parser, typed.Statement, sql)
	case *sqlparser.ExplainTab:
		return "EXPLAIN", nil
	case *sqlparser.Set:
		return mysqlSetOperation(typed), nil
	case *sqlparser.Flush:
		return "FLUSH", nil
	case *sqlparser.Show:
		return "SHOW", nil
	case *sqlparser.Use:
		return "USE", nil
	case *sqlparser.Begin:
		return "BEGIN", nil
	case *sqlparser.Commit:
		return "COMMIT", nil
	case *sqlparser.Rollback, *sqlparser.SRollback:
		return "ROLLBACK", nil
	case *sqlparser.Analyze:
		return "ANALYZE TABLE", nil
	case *sqlparser.Load:
		return "LOAD", nil
	case *sqlparser.Kill:
		return "KILL", nil
	case *sqlparser.OtherAdmin:
		return mysqlFirstKeyword(parser, sql)
	default:
		return mysqlFirstKeyword(parser, sql)
	}
}

func mysqlSetOperation(statement *sqlparser.Set) string {
	if statement != nil {
		for _, expression := range statement.Exprs {
			if expression != nil && expression.Var != nil && expression.Var.Scope == sqlparser.GlobalScope {
				return "SET GLOBAL"
			}
		}
	}
	return "SET SESSION"
}

func mysqlFirstKeyword(parser *sqlparser.Parser, sql string) (string, error) {
	tokenizer := parser.NewStringTokenizer(sql)
	var token int
	var value string
	for {
		token, value = tokenizer.Scan()
		if token != sqlparser.COMMENT {
			break
		}
	}
	if token == sqlparser.LEX_ERROR {
		return "", errors.New("MySQL lexer rejected the operation token")
	}
	if tokenizer.LastError != nil {
		return "", fmt.Errorf("scan MySQL operation: %w", tokenizer.LastError)
	}
	keyword := strings.TrimSpace(sqlparser.KeywordString(token))
	if keyword == "" {
		keyword = strings.TrimSpace(value)
	}
	if keyword == "" {
		return "", errors.New("MySQL AST operation is empty")
	}
	return strings.ToUpper(keyword), nil
}

func mysqlRootWhere(statement sqlparser.Statement) (bool, sqlparser.Expr) {
	switch typed := statement.(type) {
	case *sqlparser.Select:
		if typed.Where != nil {
			return true, typed.Where.Expr
		}
	case *sqlparser.Update:
		if typed.Where != nil {
			return true, typed.Where.Expr
		}
		return mysqlJoinWhere(typed.TableExprs)
	case *sqlparser.Delete:
		if typed.Where != nil {
			return true, typed.Where.Expr
		}
		return mysqlJoinWhere(typed.TableExprs)
	case *sqlparser.Union:
		leftHasWhere, leftExpression := mysqlSelectWhere(typed.Left)
		if leftHasWhere {
			return true, leftExpression
		}
		return mysqlSelectWhere(typed.Right)
	}
	return false, nil
}

func mysqlJoinWhere(tableExpressions []sqlparser.TableExpr) (bool, sqlparser.Expr) {
	conditions := make([]sqlparser.Expr, 0)
	for _, tableExpression := range tableExpressions {
		mysqlCollectJoinConditions(tableExpression, &conditions)
	}
	if len(conditions) == 0 {
		return false, nil
	}
	combined := conditions[0]
	for _, condition := range conditions[1:] {
		combined = &sqlparser.AndExpr{Left: combined, Right: condition}
	}
	return true, combined
}

func mysqlCollectJoinConditions(tableExpression sqlparser.TableExpr, conditions *[]sqlparser.Expr) {
	if conditions == nil {
		return
	}
	switch typed := tableExpression.(type) {
	case *sqlparser.JoinTableExpr:
		if typed == nil {
			return
		}
		mysqlCollectJoinConditions(typed.LeftExpr, conditions)
		mysqlCollectJoinConditions(typed.RightExpr, conditions)
		if typed.Condition != nil && typed.Condition.On != nil {
			*conditions = append(*conditions, typed.Condition.On)
		}
	case *sqlparser.ParenTableExpr:
		if typed == nil {
			return
		}
		for _, expression := range typed.Exprs {
			mysqlCollectJoinConditions(expression, conditions)
		}
	}
}

func mysqlSelectWhere(statement sqlparser.SelectStatement) (bool, sqlparser.Expr) {
	switch typed := statement.(type) {
	case *sqlparser.Select:
		if typed.Where != nil {
			return true, typed.Where.Expr
		}
	case *sqlparser.Union:
		leftHasWhere, leftExpression := mysqlSelectWhere(typed.Left)
		if leftHasWhere {
			return true, leftExpression
		}
		return mysqlSelectWhere(typed.Right)
	}
	return false, nil
}

func mysqlHasLimit(statement sqlparser.Statement) bool {
	switch typed := statement.(type) {
	case *sqlparser.Select:
		return typed.Limit != nil
	case *sqlparser.Union:
		return typed.Limit != nil
	case *sqlparser.Update:
		return typed.Limit != nil
	case *sqlparser.Delete:
		return typed.Limit != nil
	default:
		return false
	}
}

type mysqlConstant struct {
	kind  string
	value string
}

func mysqlExpressionTautology(expression sqlparser.Expr) bool {
	switch typed := expression.(type) {
	case sqlparser.BoolVal:
		return bool(typed)
	case *sqlparser.Literal:
		if typed.Type != sqlparser.IntVal && typed.Type != sqlparser.DecimalVal && typed.Type != sqlparser.FloatVal {
			return false
		}
		value, err := strconv.ParseFloat(typed.Val, 64)
		return err == nil && value != 0
	case *sqlparser.ComparisonExpr:
		if typed.Operator != sqlparser.EqualOp && typed.Operator != sqlparser.NullSafeEqualOp {
			return false
		}
		if left, leftOK := typed.Left.(*sqlparser.ColName); leftOK {
			right, rightOK := typed.Right.(*sqlparser.ColName)
			return rightOK && left.Equal(right)
		}
		left, leftOK := mysqlConstantValue(typed.Left)
		right, rightOK := mysqlConstantValue(typed.Right)
		return leftOK && rightOK && left == right
	case *sqlparser.ExistsExpr:
		return mysqlExistsConstantSelect(typed.Subquery)
	case *sqlparser.OrExpr:
		return mysqlExpressionTautology(typed.Left) || mysqlExpressionTautology(typed.Right)
	case *sqlparser.AndExpr:
		return mysqlExpressionTautology(typed.Left) && mysqlExpressionTautology(typed.Right)
	default:
		return false
	}
}

func mysqlExistsConstantSelect(subquery *sqlparser.Subquery) bool {
	if subquery == nil {
		return false
	}
	selectNode, ok := subquery.Select.(*sqlparser.Select)
	if !ok || selectNode.Where != nil || selectNode.Having != nil || selectNode.GroupBy != nil || selectNode.Limit != nil {
		return false
	}
	if !mysqlFromIsOnlyDual(selectNode.From) || len(selectNode.SelectExprs) == 0 {
		return false
	}
	for _, expression := range selectNode.SelectExprs {
		aliased, ok := expression.(*sqlparser.AliasedExpr)
		if !ok || !mysqlConstantExpression(aliased.Expr) {
			return false
		}
	}
	return true
}

func mysqlFromIsOnlyDual(from []sqlparser.TableExpr) bool {
	if len(from) == 0 {
		return true
	}
	if len(from) != 1 {
		return false
	}
	aliased, ok := from[0].(*sqlparser.AliasedTableExpr)
	if !ok {
		return false
	}
	table, ok := aliased.Expr.(sqlparser.TableName)
	return ok && table.Qualifier.String() == "" && strings.EqualFold(table.Name.String(), "dual")
}

func mysqlConstantExpression(expression sqlparser.Expr) bool {
	switch expression.(type) {
	case sqlparser.BoolVal, *sqlparser.Literal, *sqlparser.NullVal:
		return true
	default:
		return false
	}
}

func mysqlQueryComplexity(statement sqlparser.Statement) (int, int) {
	depth := 0
	maximumDepth := 0
	unionCount := 0
	sqlparser.Rewrite(
		statement,
		func(cursor *sqlparser.Cursor) bool {
			switch cursor.Node().(type) {
			case *sqlparser.Subquery, *sqlparser.DerivedTable, *sqlparser.CommonTableExpr:
				depth++
				if depth > maximumDepth {
					maximumDepth = depth
				}
			case *sqlparser.Union:
				unionCount++
			}
			return true
		},
		func(cursor *sqlparser.Cursor) bool {
			switch cursor.Node().(type) {
			case *sqlparser.Subquery, *sqlparser.DerivedTable, *sqlparser.CommonTableExpr:
				depth--
			}
			return true
		},
	)
	return maximumDepth, unionCount
}

func mysqlAggregateShape(statement sqlparser.Statement) (bool, bool) {
	selectNode, ok := statement.(*sqlparser.Select)
	if !ok {
		return false, false
	}
	hasGroupBy := selectNode.GroupBy != nil && len(selectNode.GroupBy.Exprs) > 0
	if hasGroupBy || len(selectNode.SelectExprs) == 0 {
		return hasGroupBy, false
	}
	for _, expression := range selectNode.SelectExprs {
		aliased, ok := expression.(*sqlparser.AliasedExpr)
		if !ok {
			return false, false
		}
		if _, aggregate := aliased.Expr.(sqlparser.AggrFunc); !aggregate {
			return false, false
		}
	}
	return false, true
}

func mysqlProjectedColumns(statement sqlparser.Statement) []string {
	selectNode, ok := statement.(*sqlparser.Select)
	if !ok {
		return nil
	}
	columns := make(stringSet)
	for _, expression := range selectNode.SelectExprs {
		switch typed := expression.(type) {
		case *sqlparser.StarExpr:
			if cteColumns, ok := mysqlSingleCTEProjectedColumns(selectNode, typed); ok {
				for _, column := range cteColumns {
					columns.add(column)
				}
			} else {
				columns.add("*")
			}
		case *sqlparser.AliasedExpr:
			_ = sqlparser.Walk(func(node sqlparser.SQLNode) (bool, error) {
				if _, isSubquery := node.(*sqlparser.Subquery); isSubquery {
					return false, nil
				}
				if column, isColumn := node.(*sqlparser.ColName); isColumn {
					columns.add(column.Name.String())
				}
				return true, nil
			}, typed.Expr)
		}
	}
	return columns.sorted()
}

type directProjectionItem struct {
	column string
	star   bool
	source model.ObjectRef
}

func mysqlDirectProjections(statement sqlparser.Statement) []model.DirectProjectionRef {
	selectNode, ok := statement.(*sqlparser.Select)
	if !ok {
		return nil
	}
	bindings := mysqlTopBindings(selectNode)
	items := make([]directProjectionItem, len(selectNode.SelectExprs))
	for index, expression := range selectNode.SelectExprs {
		switch typed := expression.(type) {
		case *sqlparser.AliasedExpr:
			column, direct := typed.Expr.(*sqlparser.ColName)
			if direct {
				items[index].column = column.Name.String()
				items[index].source = resolveDirectProjectionSource(mysqlColumnQualifiers(column), bindings)
			}
		case *sqlparser.StarExpr:
			items[index].star = true
		}
	}
	return positionDirectProjections(items)
}

func mysqlColumnQualifiers(column *sqlparser.ColName) []string {
	if column == nil || column.Qualifier.Name.IsEmpty() {
		return nil
	}
	if column.Qualifier.Qualifier.IsEmpty() {
		return []string{column.Qualifier.Name.String()}
	}
	return []string{column.Qualifier.Qualifier.String(), column.Qualifier.Name.String()}
}

func mysqlTopBindings(selectNode *sqlparser.Select) []topRelationBinding {
	if selectNode == nil {
		return nil
	}
	cteNames := make(stringSet)
	if selectNode.With != nil {
		for _, cte := range selectNode.With.CTEs {
			if cte != nil {
				cteNames.add(cte.ID.String())
			}
		}
	}
	bindings := make([]topRelationBinding, 0, len(selectNode.From))
	for _, expression := range selectNode.From {
		mysqlAppendTopBindings(expression, cteNames, &bindings)
	}
	return bindings
}

func mysqlAppendTopBindings(
	expression sqlparser.TableExpr,
	cteNames stringSet,
	bindings *[]topRelationBinding,
) {
	switch typed := expression.(type) {
	case *sqlparser.AliasedTableExpr:
		if typed == nil {
			return
		}
		alias := typed.As.String()
		switch source := typed.Expr.(type) {
		case sqlparser.TableName:
			name := source.Name.String()
			if source.Qualifier.IsEmpty() && (mysqlNameInSet(name, cteNames) || strings.EqualFold(name, "dual")) {
				*bindings = append(*bindings, nonPhysicalTopBinding(name, alias))
				return
			}
			*bindings = append(*bindings, physicalTopBinding(model.ObjectRef{
				Schema: source.Qualifier.String(), Table: name, Alias: alias,
			}))
		default:
			*bindings = append(*bindings, nonPhysicalTopBinding("", alias))
		}
	case *sqlparser.JoinTableExpr:
		if typed == nil {
			return
		}
		mysqlAppendTopBindings(typed.LeftExpr, cteNames, bindings)
		mysqlAppendTopBindings(typed.RightExpr, cteNames, bindings)
	case *sqlparser.ParenTableExpr:
		if typed == nil {
			return
		}
		for _, child := range typed.Exprs {
			mysqlAppendTopBindings(child, cteNames, bindings)
		}
	case *sqlparser.JSONTableExpr:
		if typed != nil {
			*bindings = append(*bindings, nonPhysicalTopBinding("", typed.Alias.String()))
		}
	default:
		// An unknown FROM item must still count as a visible non-physical
		// source so that a bare column cannot be attributed optimistically.
		*bindings = append(*bindings, nonPhysicalTopBinding("", ""))
	}
}

func mysqlNameInSet(name string, names stringSet) bool {
	for candidate := range names {
		if strings.EqualFold(name, candidate) {
			return true
		}
	}
	return false
}

func positionDirectProjections(items []directProjectionItem) []model.DirectProjectionRef {
	firstStar := -1
	lastStar := -1
	starCount := 0
	for index, item := range items {
		if !item.star {
			continue
		}
		if firstStar < 0 {
			firstStar = index
		}
		lastStar = index
		starCount++
	}

	projections := make([]model.DirectProjectionRef, 0, len(items)-starCount)
	for index, item := range items {
		if item.column == "" {
			continue
		}
		switch {
		case starCount == 0:
			projections = append(projections, model.DirectProjectionRef{
				Column: item.column, Offset: index, Source: item.source,
			})
		case starCount == 1 && index < firstStar:
			projections = append(projections, model.DirectProjectionRef{
				Column: item.column, Offset: index, Source: item.source,
			})
		case starCount == 1 && index > firstStar:
			projections = append(projections, model.DirectProjectionRef{
				Column: item.column, Offset: len(items) - 1 - index, FromEnd: true, Source: item.source,
			})
		case starCount > 1 && index < firstStar:
			projections = append(projections, model.DirectProjectionRef{
				Column: item.column, Offset: index, Source: item.source,
			})
		case starCount > 1 && index > lastStar:
			projections = append(projections, model.DirectProjectionRef{
				Column: item.column, Offset: len(items) - 1 - index, FromEnd: true, Source: item.source,
			})
		}
	}
	if len(projections) == 0 {
		return nil
	}
	return projections
}

func mysqlSingleCTEProjectedColumns(selectNode *sqlparser.Select, star *sqlparser.StarExpr) ([]string, bool) {
	if selectNode == nil || star == nil || selectNode.With == nil || len(selectNode.From) != 1 {
		return nil, false
	}
	aliased, ok := selectNode.From[0].(*sqlparser.AliasedTableExpr)
	if !ok {
		return nil, false
	}
	table, ok := aliased.Expr.(sqlparser.TableName)
	if !ok || !table.Qualifier.IsEmpty() {
		return nil, false
	}
	source := table.Name.String()
	starSource := star.TableName.Name.String()
	if !star.TableName.Qualifier.IsEmpty() ||
		(starSource != "" && !strings.EqualFold(starSource, source) &&
			!strings.EqualFold(starSource, aliased.As.String())) {
		return nil, false
	}
	for _, cte := range selectNode.With.CTEs {
		if cte == nil || !strings.EqualFold(cte.ID.String(), source) {
			continue
		}
		columns := mysqlProjectedColumns(cte.Subquery)
		if len(columns) == 0 {
			return nil, false
		}
		return columns, true
	}
	return nil, false
}

func mysqlConstantValue(expression sqlparser.Expr) (mysqlConstant, bool) {
	switch typed := expression.(type) {
	case sqlparser.BoolVal:
		return mysqlConstant{kind: "bool", value: strconv.FormatBool(bool(typed))}, true
	case *sqlparser.Literal:
		return mysqlConstant{kind: strconv.Itoa(int(typed.Type)), value: typed.Val}, true
	default:
		return mysqlConstant{}, false
	}
}

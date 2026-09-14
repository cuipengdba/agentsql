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
	nestingDepth, unionCount := mysqlQueryComplexity(statement)
	operations.add(fmt.Sprintf("%s:%d", nestingDepthOperation, nestingDepth))
	operations.add(fmt.Sprintf("%s:%d", unionCountOperation, unionCount))
	hasGroupBy, isPureAggregate := mysqlAggregateShape(statement)
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
		Dialect:         mysqlDialect,
		RawSQL:          sql,
		Normalized:      normalized,
		StmtType:        statementType,
		IsMulti:         false,
		Tables:          tables.sorted(),
		Columns:         columns.sorted(),
		HasWhere:        hasWhere,
		WhereTautology:  hasWhere && mysqlExpressionTautology(whereExpression),
		HasLimit:        mysqlHasLimit(statement),
		HasGroupBy:      hasGroupBy,
		IsPureAggregate: isPureAggregate,
		Functions:       functions.sorted(),
		Operations:      operations.sorted(),
		Explain:         nil,
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
	switch statement.(type) {
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
	case *sqlparser.Set, *sqlparser.Flush, *sqlparser.Show, *sqlparser.Use,
		*sqlparser.Begin, *sqlparser.Commit, *sqlparser.Rollback, *sqlparser.SRollback,
		*sqlparser.Savepoint, *sqlparser.Release, *sqlparser.CallProc, *sqlparser.LockTables,
		*sqlparser.UnlockTables, *sqlparser.ExplainStmt, *sqlparser.ExplainTab,
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
	case *sqlparser.Delete:
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
			columns.add("*")
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

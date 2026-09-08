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
		return &model.AST{
			Dialect: mysqlDialect,
			RawSQL:  sql,
			IsMulti: len(pieces) > 1,
		}, unparseableError(mysqlDialect, fmt.Errorf("expected one statement, got %d", len(pieces)))
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
		Dialect:        mysqlDialect,
		RawSQL:         sql,
		Normalized:     normalized,
		StmtType:       statementType,
		IsMulti:        false,
		Tables:         tables.sorted(),
		Columns:        columns.sorted(),
		HasWhere:       hasWhere,
		WhereTautology: hasWhere && mysqlExpressionTautology(whereExpression),
		HasLimit:       mysqlHasLimit(statement),
		Functions:      functions.sorted(),
		Operations:     operations.sorted(),
		Explain:        nil,
	}, nil
}

func mysqlHasComment(parser *sqlparser.Parser, sql string) (bool, error) {
	tokenizer := parser.NewStringTokenizer(sql)
	for {
		token, _ := tokenizer.Scan()
		if token == sqlparser.COMMENT {
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
		return "SET", nil
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
		left, leftOK := mysqlConstantValue(typed.Left)
		right, rightOK := mysqlConstantValue(typed.Right)
		return leftOK && rightOK && left == right
	case *sqlparser.OrExpr:
		return mysqlExpressionTautology(typed.Left) || mysqlExpressionTautology(typed.Right)
	case *sqlparser.AndExpr:
		return mysqlExpressionTautology(typed.Left) && mysqlExpressionTautology(typed.Right)
	default:
		return false
	}
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

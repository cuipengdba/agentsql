package parser

import (
	"errors"
	"strconv"
	"strings"

	"vitess.io/vitess/go/vt/sqlparser"
)

// MySQLColumnSelectShape is a deliberately small, syntax-only description.
// Physical column identity and object type must still be proven on the same
// business connection before this shape can authorize an execution.
type MySQLColumnSelectShape struct {
	Schema  string
	Table   string
	Alias   string
	Outputs []string
	Uses    []MySQLColumnUse
}

type MySQLColumnUse struct {
	Name        string
	Usage       string
	Site        string
	OutputIndex int
}

var errMySQLColumnShape = errors.New("unsupported MySQL column SELECT shape")

// ParseMySQLColumnSelectShape accepts only a single, plain base-table SELECT.
// It never rewrites SQL or treats parsed names as authority-bearing identities.
func ParseMySQLColumnSelectShape(raw string) (shape MySQLColumnSelectShape, parseErr error) {
	defer func() {
		if recover() != nil {
			shape = MySQLColumnSelectShape{}
			parseErr = errMySQLColumnShape
		}
	}()
	if len(raw) == 0 || len(raw) > 64<<10 || strings.Contains(raw, "/*") ||
		strings.Contains(raw, "--") || strings.Contains(raw, "#") {
		return shape, errMySQLColumnShape
	}
	approved, err := newMySQLParser()
	if err != nil {
		return shape, errMySQLColumnShape
	}
	vitess := approved.(*mysqlParser).parser
	parts, err := vitess.SplitStatementToPieces(raw)
	if err != nil || len(parts) != 1 {
		return shape, errMySQLColumnShape
	}
	statement, err := vitess.ParseStrictDDL(raw)
	if err != nil {
		return shape, errMySQLColumnShape
	}
	if err := mysqlValidateLineageStructure(statement); err != nil {
		return shape, errMySQLColumnShape
	}
	selectNode, ok := statement.(*sqlparser.Select)
	if !ok || selectNode.With != nil || selectNode.Comments != nil || selectNode.Distinct ||
		selectNode.Cache != nil || selectNode.HighPriority || selectNode.StraightJoinHint ||
		selectNode.SQLSmallResult || selectNode.SQLBigResult || selectNode.SQLBufferResult ||
		selectNode.SQLCalcFoundRows || selectNode.GroupBy != nil || selectNode.Having != nil ||
		len(selectNode.Windows) != 0 || selectNode.Into != nil || selectNode.Lock != sqlparser.NoLock ||
		len(selectNode.From) != 1 {
		return shape, errMySQLColumnShape
	}
	tableExpr, ok := selectNode.From[0].(*sqlparser.AliasedTableExpr)
	if !ok || tableExpr == nil || len(tableExpr.Partitions) != 0 || len(tableExpr.Hints) != 0 || len(tableExpr.Columns) != 0 {
		return shape, errMySQLColumnShape
	}
	table, ok := tableExpr.Expr.(sqlparser.TableName)
	if !ok || !mysqlColumnIdentifier(table.Name.String()) ||
		(table.Qualifier.String() != "" && !mysqlColumnIdentifier(table.Qualifier.String())) {
		return shape, errMySQLColumnShape
	}
	shape.Schema, shape.Table, shape.Alias = table.Qualifier.String(), table.Name.String(), tableExpr.As.String()
	if shape.Alias != "" && !mysqlColumnIdentifier(shape.Alias) {
		return MySQLColumnSelectShape{}, errMySQLColumnShape
	}
	columns := selectNode.GetColumns()
	if len(columns) == 0 || len(columns) > 128 {
		return MySQLColumnSelectShape{}, errMySQLColumnShape
	}
	for index, expression := range columns {
		projection, ok := expression.(*sqlparser.AliasedExpr)
		if !ok || projection == nil {
			return MySQLColumnSelectShape{}, errMySQLColumnShape
		}
		column, ok := projection.Expr.(*sqlparser.ColName)
		if !ok || !shape.validColumn(column) {
			return MySQLColumnSelectShape{}, errMySQLColumnShape
		}
		output := projection.As.String()
		if output == "" {
			output = column.Name.String()
		}
		if !mysqlColumnIdentifier(output) {
			return MySQLColumnSelectShape{}, errMySQLColumnShape
		}
		shape.Outputs = append(shape.Outputs, output)
		shape.Uses = append(shape.Uses, MySQLColumnUse{Name: column.Name.String(), Usage: "output", Site: "target", OutputIndex: index})
	}
	if selectNode.Where != nil {
		if selectNode.Where.Expr == nil || !shape.appendPredicate(selectNode.Where.Expr, 0) {
			return MySQLColumnSelectShape{}, errMySQLColumnShape
		}
	}
	if len(selectNode.OrderBy) > 16 {
		return MySQLColumnSelectShape{}, errMySQLColumnShape
	}
	for _, order := range selectNode.OrderBy {
		if order == nil {
			return MySQLColumnSelectShape{}, errMySQLColumnShape
		}
		column, ok := order.Expr.(*sqlparser.ColName)
		// Unqualified ORDER BY names resolve output aliases before base columns
		// in MySQL. Requiring the source qualifier removes that ambiguity.
		if !ok || !shape.validColumn(column) || column.Qualifier.Name.IsEmpty() {
			return MySQLColumnSelectShape{}, errMySQLColumnShape
		}
		shape.Uses = append(shape.Uses, MySQLColumnUse{Name: column.Name.String(), Usage: "reference", Site: "order_by", OutputIndex: -1})
	}
	if selectNode.Limit != nil {
		if selectNode.Limit.Offset != nil || !mysqlColumnBoundedUint(selectNode.Limit.Rowcount, 10000) {
			return MySQLColumnSelectShape{}, errMySQLColumnShape
		}
	}
	return shape, nil
}

func mysqlColumnIdentifier(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for index, ch := range name {
		if ch == '_' || ch >= 'A' && ch <= 'Z' || ch >= 'a' && ch <= 'z' || index > 0 && ch >= '0' && ch <= '9' {
			continue
		}
		return false
	}
	return true
}

func (shape *MySQLColumnSelectShape) validColumn(column *sqlparser.ColName) bool {
	if column == nil || !mysqlColumnIdentifier(column.Name.String()) || !column.Qualifier.Qualifier.IsEmpty() {
		return false
	}
	qualifier := column.Qualifier.Name.String()
	if qualifier == "" {
		return true
	}
	if shape.Alias != "" {
		return qualifier == shape.Alias
	}
	return qualifier == shape.Table
}

func (shape *MySQLColumnSelectShape) appendPredicate(expr sqlparser.Expr, depth int) bool {
	if depth > 16 || len(shape.Uses) > 256 {
		return false
	}
	switch node := expr.(type) {
	case *sqlparser.AndExpr:
		return shape.appendPredicate(node.Left, depth+1) && shape.appendPredicate(node.Right, depth+1)
	case *sqlparser.OrExpr:
		return shape.appendPredicate(node.Left, depth+1) && shape.appendPredicate(node.Right, depth+1)
	case *sqlparser.ComparisonExpr:
		column, ok := node.Left.(*sqlparser.ColName)
		if !ok || !shape.validColumn(column) || node.Escape != nil || node.Modifier != 0 {
			return false
		}
		if node.Operator == sqlparser.InOp || node.Operator == sqlparser.NotInOp {
			values, ok := node.Right.(sqlparser.ValTuple)
			if !ok || len(values) == 0 || len(values) > 100 {
				return false
			}
			for _, value := range values {
				if !mysqlColumnLiteral(value) {
					return false
				}
			}
		} else {
			switch node.Operator {
			case sqlparser.EqualOp, sqlparser.NotEqualOp, sqlparser.LessThanOp, sqlparser.LessEqualOp,
				sqlparser.GreaterThanOp, sqlparser.GreaterEqualOp:
			default:
				return false
			}
			if !mysqlColumnLiteral(node.Right) {
				return false
			}
		}
		shape.Uses = append(shape.Uses, MySQLColumnUse{Name: column.Name.String(), Usage: "reference", Site: "where", OutputIndex: -1})
		return true
	case *sqlparser.IsExpr:
		column, ok := node.Left.(*sqlparser.ColName)
		if !ok || !shape.validColumn(column) || node.Right != sqlparser.IsNullOp && node.Right != sqlparser.IsNotNullOp {
			return false
		}
		shape.Uses = append(shape.Uses, MySQLColumnUse{Name: column.Name.String(), Usage: "reference", Site: "where", OutputIndex: -1})
		return true
	default:
		return false
	}
}

func mysqlColumnLiteral(expr sqlparser.Expr) bool {
	literal, ok := expr.(*sqlparser.Literal)
	return ok && literal != nil && len(literal.Val) <= 4096 &&
		(literal.Type == sqlparser.StrVal || literal.Type == sqlparser.IntVal ||
			literal.Type == sqlparser.DecimalVal || literal.Type == sqlparser.FloatVal)
}

func mysqlColumnBoundedUint(expr sqlparser.Expr, maximum uint64) bool {
	literal, ok := expr.(*sqlparser.Literal)
	if !ok || literal == nil || literal.Type != sqlparser.IntVal {
		return false
	}
	value, err := strconv.ParseUint(literal.Val, 10, 64)
	return err == nil && value <= maximum
}

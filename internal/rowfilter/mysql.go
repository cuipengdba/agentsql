package rowfilter

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/cuipengdba/agentsql/internal/parser"
	"vitess.io/vitess/go/vt/sqlparser"
)

// RewriteMySQLSingleTable combines a bounded compatibility row rule with a
// simple, schema-qualified SELECT. The caller must establish the live table
// and field identities and still execute through the controlled gateway.
func RewriteMySQLSingleTable(rawSQL, schema, table string, filters []string) (string, string, error) {
	if !mysqlRowName(schema) || !mysqlRowName(table) || len(filters) == 0 || len(filters) > 16 {
		return "", "", ErrUnsupported
	}
	shape, err := parser.ParseMySQLColumnSelectShape(rawSQL)
	if err != nil || shape.Schema != schema || shape.Table != table || len(shape.Uses) == 0 {
		return "", "", ErrUnsupported
	}
	vitess, err := sqlparser.New(sqlparser.Options{MySQLServerVersion: "8.0.30"})
	if err != nil {
		return "", "", ErrUnsupported
	}
	statement, err := vitess.ParseStrictDDL(rawSQL)
	if err != nil {
		return "", "", ErrUnsupported
	}
	selectNode, ok := statement.(*sqlparser.Select)
	if !ok {
		return "", "", ErrUnsupported
	}
	for _, filter := range filters {
		if len(filter) == 0 || len(filter) > maxPredicateBytes || strings.TrimSpace(filter) != filter ||
			strings.Contains(filter, "/*") || strings.Contains(filter, "--") || strings.Contains(filter, "#") {
			return "", "", ErrUnsupported
		}
		probe := "SELECT `" + shape.Uses[0].Name + "` FROM `" + schema + "`.`" + table + "` WHERE " + filter
		if _, err := parser.ParseMySQLColumnSelectShape(probe); err != nil {
			return "", "", ErrUnsupported
		}
		parsedFilter, err := vitess.ParseStrictDDL(probe)
		if err != nil {
			return "", "", ErrUnsupported
		}
		filterSelect, ok := parsedFilter.(*sqlparser.Select)
		if !ok || filterSelect.Where == nil || !mysqlRowSafePredicate(filterSelect.Where.Expr, 0) {
			return "", "", ErrUnsupported
		}
		selectNode.AddWhere(filterSelect.Where.Expr)
	}
	rewritten := sqlparser.String(selectNode)
	checked, err := parser.ParseMySQLColumnSelectShape(rewritten)
	if err != nil || checked.Schema != schema || checked.Table != table {
		return "", "", ErrUnsupported
	}
	digest := sha256.Sum256([]byte("agentsql-mysql-rowfilter-v1\x00" + rewritten))
	return rewritten, hex.EncodeToString(digest[:]), nil
}

func mysqlRowSafePredicate(expr sqlparser.Expr, depth int) bool {
	if depth > 16 {
		return false
	}
	switch node := expr.(type) {
	case *sqlparser.AndExpr:
		return mysqlRowSafePredicate(node.Left, depth+1) && mysqlRowSafePredicate(node.Right, depth+1)
	case *sqlparser.OrExpr:
		return mysqlRowSafePredicate(node.Left, depth+1) && mysqlRowSafePredicate(node.Right, depth+1)
	case *sqlparser.ComparisonExpr:
		if _, ok := node.Left.(*sqlparser.ColName); !ok || node.Escape != nil || node.Modifier != 0 {
			return false
		}
		if literal, ok := node.Right.(*sqlparser.Literal); !ok || literal.Type != sqlparser.StrVal {
			return false
		}
		switch node.Operator {
		case sqlparser.EqualOp, sqlparser.NotEqualOp, sqlparser.LessThanOp, sqlparser.LessEqualOp,
			sqlparser.GreaterThanOp, sqlparser.GreaterEqualOp:
			return true
		}
	case *sqlparser.IsExpr:
		if _, ok := node.Left.(*sqlparser.ColName); !ok {
			return false
		}
		return node.Right == sqlparser.IsNullOp || node.Right == sqlparser.IsNotNullOp
	}
	return false
}

func mysqlRowName(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for i, ch := range value {
		if ch == '_' || ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || i > 0 && ch >= '0' && ch <= '9' {
			continue
		}
		return false
	}
	return true
}

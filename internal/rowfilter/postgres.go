// Package rowfilter contains a deliberately narrow PostgreSQL row predicate
// compiler. Callers must still bind the rewritten SQL, enforce the complete
// control snapshot, audit, and fence the result before delivery.
package rowfilter

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/cuipengdba/agentsql/internal/model"
	pg_query "github.com/pganalyze/pg_query_go/v5"
	"google.golang.org/protobuf/reflect/protoreflect"
)

var ErrUnsupported = errors.New("row filter cannot be proven for this SQL shape")

const maxPredicateBytes = 4096

// CompileForPolicies selects applicable stored row policies from one control
// snapshot. Until the caller also seals this result into B2 authorization and
// audit, it must not use the returned SQL for execution. Unrelated policies
// from other agents or datasources are ignored; every applicable row policy
// must describe the same exact physical table, otherwise the request closes.
func CompileForPolicies(rawSQL, agentID, datasourceID string, policies []model.Policy) (string, string, string, error) {
	var schema, table string
	filters := make([]string, 0)
	for _, policy := range policies {
		if policy.RowFilter == nil ||
			(policy.AgentID != "" && policy.AgentID != agentID) ||
			(policy.DatasourceID != "" && policy.DatasourceID != datasourceID) {
			continue
		}
		parts := strings.Split(policy.ObjectName, ".")
		if policy.ObjectType != "table" || policy.Action != "allow" || policy.Columns != nil ||
			policy.RelationBindingID != nil || policy.RelationBinding != nil ||
			len(policy.ColumnPermissions) != 0 || len(policy.ColumnStaging) != 0 || policy.LegacyUnrepresentable ||
			len(parts) != 2 || parts[0] == "" || parts[1] == "" || parts[0] == "*" || parts[1] == "*" ||
			(schema != "" && (schema != parts[0] || table != parts[1])) {
			return "", "", "", ErrUnsupported
		}
		schema, table = parts[0], parts[1]
		filters = append(filters, *policy.RowFilter)
	}
	if len(filters) == 0 {
		return rawSQL, "", "", nil
	}
	rewritten, digest, err := RewriteSingleTable(rawSQL, schema, table, filters)
	if err != nil {
		return "", "", "", err
	}
	return rewritten, digest, schema + "." + table, nil
}

// CompilePredicate accepts only comparisons of one unqualified column with a
// literal, IS [NOT] NULL, and bounded AND/OR combinations of those expressions.
// In particular, functions, parameters, subqueries, casts, and NOT are excluded.
// The returned node must be used only in a single-table SELECT validated by
// RewriteSingleTable; parsing it is not an authorization decision.
func CompilePredicate(filter string) (*pg_query.Node, error) {
	if len(filter) == 0 || len(filter) > maxPredicateBytes || strings.TrimSpace(filter) != filter {
		return nil, ErrUnsupported
	}
	parsed, err := pg_query.Parse("SELECT 1 WHERE (" + filter + ")")
	if err != nil || len(parsed.GetStmts()) != 1 {
		return nil, ErrUnsupported
	}
	selectStmt := parsed.Stmts[0].Stmt.GetSelectStmt()
	if selectStmt == nil || selectStmt.WhereClause == nil || !validPredicate(selectStmt.WhereClause, 0) {
		return nil, ErrUnsupported
	}
	return selectStmt.WhereClause, nil
}

// ProbeColumn returns a column already referenced by a valid row rule. A
// zero-row metadata probe can use this column in a type-independent null
// contradiction without requiring reference rights on every output column.
func ProbeColumn(filter string) (string, error) {
	node, err := CompilePredicate(filter)
	if err != nil {
		return "", err
	}
	for node.GetBoolExpr() != nil {
		node = node.GetBoolExpr().Args[0]
	}
	var column *pg_query.Node
	if expression := node.GetAExpr(); expression != nil {
		column = expression.Lexpr
	} else if expression := node.GetNullTest(); expression != nil {
		column = expression.Arg
	}
	if column == nil || column.GetColumnRef() == nil || len(column.GetColumnRef().Fields) != 1 ||
		column.GetColumnRef().Fields[0].GetString_() == nil {
		return "", ErrUnsupported
	}
	return column.GetColumnRef().Fields[0].GetString_().Sval, nil
}

func validPredicate(node *pg_query.Node, depth int) bool {
	if node == nil || depth > 16 {
		return false
	}
	if expr := node.GetBoolExpr(); expr != nil {
		if expr.Boolop != pg_query.BoolExprType_AND_EXPR && expr.Boolop != pg_query.BoolExprType_OR_EXPR ||
			len(expr.Args) < 2 || len(expr.Args) > 100 {
			return false
		}
		for _, item := range expr.Args {
			if !validPredicate(item, depth+1) {
				return false
			}
		}
		return true
	}
	if expr := node.GetAExpr(); expr != nil {
		if expr.Kind != pg_query.A_Expr_Kind_AEXPR_OP || len(expr.Name) != 1 ||
			expr.Name[0].GetString_() == nil || !validColumn(expr.Lexpr) || !validLiteral(expr.Rexpr) {
			return false
		}
		switch expr.Name[0].GetString_().Sval {
		case "=", "<>", "<", "<=", ">", ">=":
			return true
		}
		return false
	}
	if expr := node.GetNullTest(); expr != nil {
		return !expr.Argisrow && validColumn(expr.Arg) &&
			(expr.Nulltesttype == pg_query.NullTestType_IS_NULL || expr.Nulltesttype == pg_query.NullTestType_IS_NOT_NULL)
	}
	return false
}

func validColumn(node *pg_query.Node) bool {
	if node == nil || node.GetColumnRef() == nil || len(node.GetColumnRef().Fields) != 1 {
		return false
	}
	field := node.GetColumnRef().Fields[0].GetString_()
	return field != nil && field.Sval != ""
}

func validLiteral(node *pg_query.Node) bool {
	if node == nil || node.GetAConst() == nil {
		return false
	}
	value := node.GetAConst()
	return !value.Isnull && (value.GetSval() != nil || value.GetIval() != nil ||
		value.GetFval() != nil || value.GetBoolval() != nil)
}

// RewriteSingleTable applies every supplied predicate with AND to a SELECT
// whose only source is the exact schema-qualified base relation. The result
// remains subject to B2 database-backed column authorization: predicate
// columns become reference uses and must be granted there. No caller may
// bypass the existing fail-closed row-policy path merely because this helper
// returned SQL.
func RewriteSingleTable(rawSQL, schema, table string, filters []string) (string, string, error) {
	if schema == "" || table == "" || len(filters) == 0 || len(filters) > 16 {
		return "", "", ErrUnsupported
	}
	parsed, err := pg_query.Parse(rawSQL)
	if err != nil || len(parsed.GetStmts()) != 1 {
		return "", "", ErrUnsupported
	}
	root := parsed.Stmts[0].Stmt.GetSelectStmt()
	if root == nil || root.WithClause != nil || root.IntoClause != nil || root.Larg != nil || root.Rarg != nil ||
		root.Op != pg_query.SetOperation_SETOP_NONE || len(root.LockingClause) != 0 ||
		len(root.FromClause) != 1 || countSelectStatements(root.ProtoReflect()) != 1 {
		return "", "", ErrUnsupported
	}
	relation := root.FromClause[0].GetRangeVar()
	if relation == nil || relation.Catalogname != "" || relation.Schemaname != schema || relation.Relname != table {
		return "", "", ErrUnsupported
	}
	clauses := make([]*pg_query.Node, 0, len(filters)+1)
	if root.WhereClause != nil {
		clauses = append(clauses, root.WhereClause)
	}
	for _, filter := range filters {
		predicate, err := CompilePredicate(filter)
		if err != nil {
			return "", "", err
		}
		clauses = append(clauses, predicate)
	}
	if len(clauses) == 1 {
		root.WhereClause = clauses[0]
	} else {
		root.WhereClause = &pg_query.Node{Node: &pg_query.Node_BoolExpr{BoolExpr: &pg_query.BoolExpr{
			Boolop: pg_query.BoolExprType_AND_EXPR, Args: clauses,
		}}}
	}
	rewritten, err := pg_query.Deparse(parsed)
	if err != nil {
		return "", "", ErrUnsupported
	}
	digest := sha256.Sum256([]byte("agentsql-pg-rowfilter-v1\x00" + rewritten))
	return rewritten, hex.EncodeToString(digest[:]), nil
}

func countSelectStatements(message protoreflect.Message) int {
	count := 0
	if _, ok := message.Interface().(*pg_query.SelectStmt); ok {
		count++
	}
	message.Range(func(field protoreflect.FieldDescriptor, value protoreflect.Value) bool {
		if field.Kind() != protoreflect.MessageKind {
			return true
		}
		if field.IsList() {
			list := value.List()
			for index := 0; index < list.Len(); index++ {
				count += countSelectStatements(list.Get(index).Message())
			}
		} else if field.IsMap() {
			value.Map().Range(func(_ protoreflect.MapKey, entry protoreflect.Value) bool {
				count += countSelectStatements(entry.Message())
				return true
			})
		} else {
			count += countSelectStatements(value.Message())
		}
		return true
	})
	return count
}

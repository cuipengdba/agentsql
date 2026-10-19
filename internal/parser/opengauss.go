package parser

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/cuipengdba/agentsql/internal/model"
	pg_query "github.com/pganalyze/pg_query_go/v5"
)

// openGaussParser accepts only the explicitly checked PG-compatible SELECT
// shape. PostgreSQL protocol compatibility does not imply B2/B5 support.
type openGaussParser struct{}

var _ Parser = (*openGaussParser)(nil)

func (*openGaussParser) Parse(sql string) (*model.AST, error) {
	ast, err := (&postgresParser{}).Parse(sql)
	if ast == nil {
		ast = &model.AST{RawSQL: sql}
	}
	ast.Dialect = model.DialectOpenGauss
	if err != nil {
		return ast, unparseableError(model.DialectOpenGauss, err)
	}
	if ast.IsMulti || ast.StmtType != model.StmtType("SELECT") || len(ast.Tables) != 1 ||
		ast.Tables[0].Schema == "" || !ast.HasWhere || ast.WhereTautology || !ast.HasLimit ||
		ast.HasGroupBy || ast.IsPureAggregate || len(ast.Functions) != 0 ||
		len(ast.ProjectionLineages) == 0 || len(ast.ProjectionLineages) != len(ast.DirectProjections) {
		return ast, openGaussShapeError()
	}
	schema := strings.ToLower(ast.Tables[0].Schema)
	if strings.HasPrefix(schema, "pg_") || schema == "information_schema" || schema == "sys" {
		return ast, openGaussShapeError()
	}
	for _, projection := range ast.ProjectionLineages {
		if projection.Variadic || projection.SetOp != "" || len(projection.Arms) != 1 {
			return ast, openGaussShapeError()
		}
		arm := projection.Arms[0]
		if arm.Kind != model.LineageDirect || arm.Status != model.LineageResolved || len(arm.Dependencies) != 1 ||
			arm.Dependencies[0].Origin.Route != 0 || arm.Dependencies[0].Origin.Relation.Schema != ast.Tables[0].Schema ||
			arm.Dependencies[0].Origin.Relation.Table != ast.Tables[0].Table {
			return ast, openGaussShapeError()
		}
	}
	for _, operation := range ast.Operations {
		if operation == "SELECT" || operation == "NESTING_DEPTH:0" || operation == "UNION_COUNT:0" ||
			strings.HasPrefix(operation, "SELECT_COLUMN:") {
			continue
		}
		return ast, openGaussShapeError()
	}
	// Shared AST fields do not cover all root clauses, including row locks,
	// SELECT INTO, and vendor extensions. Reject them from the parse tree.
	encoded, err := pg_query.ParseToJSON(sql)
	if err != nil {
		return ast, unparseableError(model.DialectOpenGauss, err)
	}
	document, err := decodePostgresDocument(encoded)
	if err != nil || len(document.Statements) != 1 {
		return ast, openGaussShapeError()
	}
	nodeType, node, err := postgresRoot(document.Statements[0])
	if err != nil || nodeType != "SelectStmt" {
		return ast, openGaussShapeError()
	}
	root, ok := node.(map[string]any)
	if !ok {
		return ast, openGaussShapeError()
	}
	if !closedKeys(root, "targetList", "fromClause", "whereClause", "limitCount", "limitOffset", "limitOption", "op", "all") {
		return ast, openGaussShapeError()
	}
	for _, clause := range []string{"intoClause", "withClause", "lockingClause", "groupClause", "havingClause", "windowClause", "distinctClause", "sortClause", "larg", "rarg", "valuesLists"} {
		if value, present := root[clause]; present && value != nil {
			return ast, openGaussShapeError()
		}
	}
	if root["limitOffset"] != nil || !openGaussLiteralLimit(root["limitCount"]) {
		return ast, openGaussShapeError()
	}
	if option, present := root["limitOption"].(string); present && option != "LIMIT_OPTION_COUNT" {
		return ast, openGaussShapeError()
	}
	if value, present := root["op"]; present && value != nil && value != "SETOP_NONE" {
		return ast, openGaussShapeError()
	}
	from, ok := root["fromClause"].([]any)
	if !ok || len(from) != 1 {
		return ast, openGaussShapeError()
	}
	fromObject, ok := from[0].(map[string]any)
	if !ok || len(fromObject) != 1 || fromObject["RangeVar"] == nil {
		return ast, openGaussShapeError()
	}
	// Ordinary table references include inherited child rows. ONLY prevents a
	// grant for one table from silently reading a different child relation.
	relation, ok := fromObject["RangeVar"].(map[string]any)
	if !ok || relation["inh"] == true {
		return ast, openGaussShapeError()
	}
	return ast, nil
}

func openGaussLiteralLimit(value any) bool {
	outer, ok := value.(map[string]any)
	if !ok || len(outer) != 1 {
		return false
	}
	constant, ok := outer["A_Const"].(map[string]any)
	if !ok {
		return false
	}
	integer, ok := constant["ival"].(map[string]any)
	if !ok {
		return false
	}
	number, ok := integer["ival"].(json.Number)
	if !ok {
		return false
	}
	limit, err := number.Int64()
	return err == nil && limit >= 1 && limit <= 10_000
}

func openGaussShapeError() error {
	return unparseableError(model.DialectOpenGauss, errors.New("only bounded single-table schema-qualified SELECT is supported"))
}

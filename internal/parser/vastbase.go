package parser

import (
	"errors"
	"strings"

	"github.com/cuipengdba/agentsql/internal/model"
	pg_query "github.com/pganalyze/pg_query_go/v5"
)

// vastbaseParser accepts a deliberately small PostgreSQL-compatible subset.
// G100 PG compatibility is not evidence that all PostgreSQL syntax or safety
// contracts are identical.
type vastbaseParser struct{}

var _ Parser = (*vastbaseParser)(nil)

func (*vastbaseParser) Parse(sql string) (*model.AST, error) {
	ast, err := (&postgresParser{}).Parse(sql)
	if ast == nil {
		ast = &model.AST{RawSQL: sql}
	}
	ast.Dialect = model.DialectVastbase
	if err != nil {
		return ast, unparseableError(model.DialectVastbase, err)
	}
	if ast.IsMulti || ast.StmtType != model.StmtType("SELECT") || len(ast.Tables) != 1 ||
		ast.Tables[0].Schema == "" || !ast.HasWhere || ast.WhereTautology || !ast.HasLimit ||
		ast.HasGroupBy || ast.IsPureAggregate || len(ast.Functions) != 0 ||
		len(ast.ProjectionLineages) == 0 || len(ast.ProjectionLineages) != len(ast.DirectProjections) {
		return ast, vastbaseShapeError()
	}
	for _, projection := range ast.ProjectionLineages {
		if projection.Variadic || projection.SetOp != "" || len(projection.Arms) != 1 {
			return ast, vastbaseShapeError()
		}
		arm := projection.Arms[0]
		if arm.Kind != model.LineageDirect || arm.Status != model.LineageResolved || len(arm.Dependencies) != 1 ||
			arm.Dependencies[0].Origin.Route != 0 || arm.Dependencies[0].Origin.Relation.Schema != ast.Tables[0].Schema ||
			arm.Dependencies[0].Origin.Relation.Table != ast.Tables[0].Table {
			return ast, vastbaseShapeError()
		}
	}
	for _, operation := range ast.Operations {
		if operation == "SELECT" || operation == "NESTING_DEPTH:0" || operation == "UNION_COUNT:0" ||
			strings.HasPrefix(operation, "SELECT_COLUMN:") {
			continue
		}
		return ast, vastbaseShapeError()
	}
	// Root syntax checks cover clauses that are not represented in the shared
	// AST, especially SELECT FOR UPDATE and PostgreSQL-specific extensions.
	encoded, err := pg_query.ParseToJSON(sql)
	if err != nil {
		return ast, unparseableError(model.DialectVastbase, err)
	}
	document, err := decodePostgresDocument(encoded)
	if err != nil || len(document.Statements) != 1 {
		return ast, vastbaseShapeError()
	}
	nodeType, node, err := postgresRoot(document.Statements[0])
	if err != nil || nodeType != "SelectStmt" {
		return ast, vastbaseShapeError()
	}
	root, ok := node.(map[string]any)
	if !ok {
		return ast, vastbaseShapeError()
	}
	for _, clause := range []string{"intoClause", "withClause", "lockingClause", "groupClause", "havingClause", "windowClause", "distinctClause", "sortClause", "larg", "rarg", "valuesLists"} {
		if value, present := root[clause]; present && value != nil {
			return ast, vastbaseShapeError()
		}
	}
	if value, present := root["op"]; present && value != nil && value != "SETOP_NONE" {
		return ast, vastbaseShapeError()
	}
	from, ok := root["fromClause"].([]any)
	if !ok || len(from) != 1 {
		return ast, vastbaseShapeError()
	}
	fromObject, ok := from[0].(map[string]any)
	if !ok || len(fromObject) != 1 || fromObject["RangeVar"] == nil {
		return ast, vastbaseShapeError()
	}
	return ast, nil
}

func vastbaseShapeError() error {
	return unparseableError(model.DialectVastbase, errors.New("only bounded single-table schema-qualified SELECT is supported"))
}

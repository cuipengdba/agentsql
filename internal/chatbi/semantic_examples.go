package chatbi

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/parser"
)

type ExampleHint struct {
	Question string `json:"question"`
	SQL      string `json:"sql"`
}

// SafeExamples selects short, read-only examples whose single physical table
// and every referenced column are present in the gateway's current visible
// schema. The example is prompt context only, never an executable request.
func (service *Service) SafeExamples(ctx context.Context, p Principal, datasource ID, dialect string, visible []Column, question string) ([]ExampleHint, error) {
	binding, err := service.binding(ctx, p, datasource)
	if err != nil {
		return nil, err
	}
	if binding.Dialect != dialect {
		return nil, ErrUnavailable
	}
	var parseDialect model.DBDialect
	switch dialect {
	case "pg":
		parseDialect = model.DialectPostgres
	case "mysql":
		parseDialect = model.DialectMySQL
	default:
		return nil, ErrUnavailable
	}
	approved, err := parser.NewParser(parseDialect)
	if err != nil {
		return nil, ErrUnavailable
	}
	raw, err := service.Store.list(ctx, "sql-example", string(p.Workspace), "workspace")
	if err != nil {
		return nil, err
	}
	if len(raw) > 500 {
		return nil, ErrUnavailable
	}
	hints := make([]ExampleHint, 0, 4)
	used := 2
	for _, item := range raw {
		var example SQLExample
		if json.Unmarshal(item, &example) != nil || !validExample(example, p.Workspace) || example.ID <= 0 {
			return nil, ErrUnavailable
		}
		if !example.Enabled || ID(strconv.FormatInt(example.Datasource, 10)) != datasource || !semanticQuestionMatch(question, example.Question) {
			continue
		}
		if !exampleVisibleSQL(approved, example.Description, visible) {
			continue
		}
		hint := ExampleHint{Question: example.Question, SQL: example.Description}
		encoded, err := json.Marshal(hint)
		if err != nil {
			return nil, ErrUnavailable
		}
		if used+len(encoded)+1 > 6144 {
			continue
		}
		used += len(encoded) + 1
		hints = append(hints, hint)
		if len(hints) == 4 {
			break
		}
	}
	return hints, nil
}

func semanticQuestionMatch(question, example string) bool {
	query := strings.ToLower(strings.TrimSpace(question))
	sample := strings.ToLower(strings.TrimSpace(example))
	return query != "" && sample != "" && (strings.Contains(query, sample) || strings.Contains(sample, query))
}

func exampleVisibleSQL(approved parser.Parser, sqlText string, visible []Column) bool {
	if len(sqlText) == 0 || len(sqlText) > 4096 || strings.Contains(sqlText, ";") || strings.Contains(sqlText, "--") || strings.Contains(sqlText, "/*") || strings.Contains(sqlText, "*/") {
		return false
	}
	ast, err := approved.Parse(sqlText)
	if err != nil || ast == nil || ast.IsMulti || ast.StmtType != "SELECT" || len(ast.Tables) != 1 || len(ast.Columns) == 0 {
		return false
	}
	table := ast.Tables[0]
	allowed := map[string]bool{}
	for _, column := range visible {
		if strings.EqualFold(column.Schema, table.Schema) && strings.EqualFold(column.Table, table.Table) {
			allowed[strings.ToLower(column.Name)] = true
		}
	}
	if len(allowed) == 0 {
		return false
	}
	for _, column := range ast.Columns {
		if column == "*" || !allowed[strings.ToLower(column)] {
			return false
		}
	}
	for _, function := range ast.Functions {
		name := strings.ToLower(function)
		name = strings.TrimPrefix(name, "pg_catalog.")
		switch name {
		case "sum", "count", "avg", "min", "max":
		default:
			return false
		}
	}
	return true
}

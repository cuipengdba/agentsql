package chatbi

import (
	"strings"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/parser"
)

// Prior SQL is model context, not an authorization source. Recheck every
// historical turn against the schema visible for this question so a rule
// revocation cannot put old column names back into a model prompt.
func visibleHistoryForModel(dialect string, columns []Column, history []TurnContext) []TurnContext {
	if len(history) == 0 {
		return history
	}
	var dbDialect model.DBDialect
	switch dialect {
	case "pg":
		dbDialect = model.DialectPostgres
	case "mysql":
		dbDialect = model.DialectMySQL
	default:
		return nil
	}
	approved, err := parser.NewParser(dbDialect)
	if err != nil {
		return nil
	}
	visible := make([]TurnContext, 0, len(history))
	for _, turn := range history {
		if exampleVisibleSQL(approved, turn.SQL, columns) {
			visible = append(visible, turn)
		}
	}
	return visible
}

// includeVisibleHistoryTables keeps the table from a completed, currently
// visible turn in the narrowed model schema. Vector ranking can otherwise
// discard that table for a pronoun-only follow-up. Historical SQL is checked
// against the full live schema first and never grants a table or column.
func includeVisibleHistoryTables(dialect string, authorized []Column, remarks []RemarkHint,
	selected []Column, selectedRemarks []RemarkHint, history []TurnContext) ([]Column, []RemarkHint) {
	if len(history) == 0 {
		return selected, selectedRemarks
	}
	var dbDialect model.DBDialect
	switch dialect {
	case "pg":
		dbDialect = model.DialectPostgres
	case "mysql":
		dbDialect = model.DialectMySQL
	default:
		return selected, selectedRemarks
	}
	approved, err := parser.NewParser(dbDialect)
	if err != nil {
		return selected, selectedRemarks
	}
	wanted := make(map[string]bool, len(history))
	for _, turn := range history {
		if !exampleVisibleSQL(approved, turn.SQL, authorized) {
			continue
		}
		ast, err := approved.Parse(turn.SQL)
		if err != nil || len(ast.Tables) != 1 {
			continue
		}
		table := ast.Tables[0]
		wanted[strings.ToLower(table.Schema)+"\x00"+strings.ToLower(table.Table)] = true
	}
	if len(wanted) == 0 {
		return selected, selectedRemarks
	}
	present := make(map[string]bool, len(selected))
	for _, column := range selected {
		present[strings.ToLower(column.Schema)+"\x00"+strings.ToLower(column.Table)] = true
	}
	for _, column := range authorized {
		key := strings.ToLower(column.Schema) + "\x00" + strings.ToLower(column.Table)
		if wanted[key] && !present[key] {
			selected = append(selected, column)
		}
	}
	for _, remark := range remarks {
		key := strings.ToLower(remark.Schema) + "\x00" + strings.ToLower(remark.Table)
		if wanted[key] && !present[key] {
			selectedRemarks = append(selectedRemarks, remark)
		}
	}
	return selected, selectedRemarks
}

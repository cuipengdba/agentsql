package chatbi

import (
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

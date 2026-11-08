package chatbi

import (
	"strings"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/parser"
	"github.com/cuipengdba/agentsql/internal/rowfilter"
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

// focusReferentialFollowup avoids sending unrelated selected tables to a
// small local model when the question clearly refers to the last result set.
// The previous SQL is never trusted as a schema grant: its single table must
// still be visible under the current authorization snapshot. An explicit
// reference to another authorized table keeps normal multi-table selection.
func focusReferentialFollowup(dialect, question string, authorized, selected []Column,
	remarks []RemarkHint, history []TurnContext) ([]Column, []RemarkHint) {
	if !isReferentialFollowup(question) || len(history) == 0 {
		return selected, remarks
	}
	q := strings.ToLower(strings.TrimSpace(question))
	var dbDialect model.DBDialect
	switch dialect {
	case "pg":
		dbDialect = model.DialectPostgres
	case "mysql":
		dbDialect = model.DialectMySQL
	default:
		return selected, remarks
	}
	approved, err := parser.NewParser(dbDialect)
	if err != nil {
		return selected, remarks
	}
	last := history[len(history)-1]
	if !exampleVisibleSQL(approved, last.SQL, authorized) {
		return selected, remarks
	}
	ast, err := approved.Parse(last.SQL)
	if err != nil || len(ast.Tables) != 1 {
		return selected, remarks
	}
	table := ast.Tables[0]
	key := strings.ToLower(table.Schema) + "\x00" + strings.ToLower(table.Table)
	for _, column := range authorized {
		other := strings.ToLower(column.Schema) + "\x00" + strings.ToLower(column.Table)
		if other != key && strings.Contains(q, strings.ToLower(column.Table)) {
			return selected, remarks
		}
	}
	focused := make([]Column, 0)
	for _, column := range selected {
		if strings.ToLower(column.Schema)+"\x00"+strings.ToLower(column.Table) == key {
			focused = append(focused, column)
		}
	}
	if len(focused) == 0 {
		return selected, remarks
	}
	focusedRemarks := make([]RemarkHint, 0)
	for _, remark := range remarks {
		if strings.ToLower(remark.Schema)+"\x00"+strings.ToLower(remark.Table) == key {
			focusedRemarks = append(focusedRemarks, remark)
		}
	}
	return focused, focusedRemarks
}

func isReferentialFollowup(question string) bool {
	q := strings.ToLower(strings.TrimSpace(question))
	for _, prefix := range []string{"这些", "那些", "其中", "这批", "上述", "它们", "these ", "those ", "their ", "among them", "of them"} {
		if strings.HasPrefix(q, prefix) {
			return true
		}
	}
	return false
}

// Preserve the last turn's simple PostgreSQL filter only for an unambiguous
// reference to that result. The caller has already removed history that is no
// longer visible under the live schema. The gateway reauthorizes the result.
func preserveReferentialWhere(dialect, question string, history []TurnContext, sql string) (string, error) {
	if dialect != "pg" || !isReferentialFollowup(question) || len(history) == 0 {
		return sql, nil
	}
	q := strings.ToLower(question)
	for _, change := range []string{" where ", " only ", " except ", " instead ", " rather than ", "只看", "仅看", "改成", "改为", "换成", "除了", "除外", "不含", "其他", "另外"} {
		if strings.Contains(q, change) {
			return sql, nil
		}
	}
	previous := history[len(history)-1].SQL
	approved, err := parser.NewParser(model.DialectPostgres)
	if err != nil {
		return "", ErrModel
	}
	ast, err := approved.Parse(previous)
	if err != nil {
		return "", ErrModel
	}
	if !ast.HasWhere {
		return sql, nil
	}
	rewritten, err := rowfilter.CarrySingleTableWhere(previous, sql)
	if err != nil {
		return "", ErrModel
	}
	return rewritten, nil
}

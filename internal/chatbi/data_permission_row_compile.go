package chatbi

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/cuipengdba/agentsql/internal/rowfilter"
)

// compileSQLBotRowPermission turns one original-client row-rule item into a
// bounded PostgreSQL predicate. The dialect form also accepts a smaller MySQL
// subset. The caller must still bind the live gateway
// identity, apply every applicable rule, recheck revisions, and execute through
// the controlled gateway. A compiled predicate alone never authorizes a query.
func compileSQLBotRowPermission(raw json.RawMessage, datasource ID, columns []Column) (string, string, string, error) {
	return compileSQLBotRowPermissionForDialect(raw, datasource, columns, "pg")
}

func compileSQLBotRowPermissionForDialect(raw json.RawMessage, datasource ID, columns []Column, dialect string) (string, string, string, error) {
	return compileSQLBotRowPermissionWithVariables(raw, datasource, columns, dialect, nil)
}

// The resolver supplies a value from the current user's server-held binding.
// A missing resolver deliberately keeps variable rules unsupported in callers
// that have not established a live identity and definition snapshot.
func compileSQLBotRowPermissionWithVariables(raw json.RawMessage, datasource ID, columns []Column, dialect string, resolve func(int64) (string, bool)) (string, string, string, error) {
	if dialect != "pg" && dialect != "mysql" {
		return "", "", "", ErrInvalidDataPermission
	}
	var input struct {
		Type           string          `json:"type"`
		Datasource     json.RawMessage `json:"ds_id"`
		Table          json.RawMessage `json:"table_id"`
		ExpressionTree json.RawMessage `json:"expression_tree"`
	}
	if len(raw) == 0 || len(raw) > 8192 || json.Unmarshal(raw, &input) != nil || input.Type != "row" {
		return "", "", "", ErrInvalidDataPermission
	}
	dsID, valid := parseSystemVariableID(input.Datasource)
	if !valid || strconv.FormatInt(dsID, 10) != string(datasource) {
		return "", "", "", ErrInvalidDataPermission
	}
	tableID, valid := parseSystemVariableID(input.Table)
	if !valid || tableID < 1 || tableID > maxSQLBotBrowserID {
		return "", "", "", ErrInvalidDataPermission
	}
	var schema, table string
	fields := make(map[int64]string)
	probeField := ""
	for _, column := range columns {
		if column.Schema == "" || column.Table == "" || column.Name == "" ||
			browserTableID(datasource, column.Schema, column.Table) != tableID {
			continue
		}
		if schema != "" && (schema != column.Schema || table != column.Table) {
			return "", "", "", ErrInvalidDataPermission
		}
		schema, table = column.Schema, column.Table
		fieldID := browserFieldID(tableID, column.Name)
		if prior, exists := fields[fieldID]; exists && prior != column.Name {
			return "", "", "", ErrInvalidDataPermission
		}
		fields[fieldID] = column.Name
		if probeField == "" && (dialect == "pg" || mysqlRowIdentifier(column.Name)) {
			probeField = column.Name
		}
	}
	if schema == "" || len(fields) == 0 || len(input.ExpressionTree) == 0 {
		return "", "", "", ErrInvalidDataPermission
	}
	treeJSON := input.ExpressionTree
	if treeJSON[0] == '"' {
		var serialized string
		if json.Unmarshal(treeJSON, &serialized) != nil || len(serialized) > 8192 {
			return "", "", "", ErrInvalidDataPermission
		}
		treeJSON = []byte(serialized)
	}
	var tree sqlbotRowTree
	if json.Unmarshal(treeJSON, &tree) != nil {
		return "", "", "", ErrInvalidDataPermission
	}
	remaining := 16
	predicate, valid := compileSQLBotRowTree(tree, fields, dialect, 0, &remaining, resolve)
	if !valid || len(predicate) > 4096 {
		return "", "", "", ErrInvalidDataPermission
	}
	if dialect == "pg" {
		if _, err := rowfilter.CompilePredicate(predicate); err != nil {
			return "", "", "", ErrInvalidDataPermission
		}
	} else if _, _, err := rowfilter.RewriteMySQLSingleTable(
		"SELECT `"+probeField+"` FROM `"+schema+"`.`"+table+"`", schema, table, []string{predicate}); err != nil {
		return "", "", "", ErrInvalidDataPermission
	}
	return schema, table, predicate, nil
}

type sqlbotRowTree struct {
	Logic string              `json:"logic"`
	Items []sqlbotRowTreeItem `json:"items"`
}

type sqlbotRowTreeItem struct {
	Type       string          `json:"type"`
	SubTree    *sqlbotRowTree  `json:"sub_tree"`
	FieldID    json.RawMessage `json:"field_id"`
	FilterType string          `json:"filter_type"`
	Term       string          `json:"term"`
	Value      string          `json:"value"`
	ValueType  string          `json:"value_type"`
	VariableID json.RawMessage `json:"variable_id"`
}

func compileSQLBotRowTree(tree sqlbotRowTree, fields map[int64]string, dialect string, depth int, remaining *int, resolve func(int64) (string, bool)) (string, bool) {
	logic := strings.ToLower(tree.Logic)
	if depth > 8 || len(tree.Items) == 0 || len(tree.Items) > 16 ||
		(logic != "and" && logic != "or") || (dialect != "mysql" && logic == "or" && len(tree.Items) > 1) {
		return "", false
	}
	parts := make([]string, 0, len(tree.Items))
	for _, item := range tree.Items {
		var part string
		var valid bool
		switch item.Type {
		case "tree":
			if item.SubTree == nil {
				return "", false
			}
			part, valid = compileSQLBotRowTree(*item.SubTree, fields, dialect, depth+1, remaining, resolve)
		case "item":
			if item.SubTree != nil || *remaining == 0 {
				return "", false
			}
			*remaining--
			part, valid = compileSQLBotRowItem(item, fields, dialect, resolve)
		default:
			return "", false
		}
		if !valid {
			return "", false
		}
		parts = append(parts, part)
	}
	if len(parts) == 1 {
		return parts[0], true
	}
	join := " AND "
	if logic == "or" {
		join = " OR "
	}
	return "(" + strings.Join(parts, join) + ")", true
}

func compileSQLBotRowItem(item sqlbotRowTreeItem, fields map[int64]string, dialect string, resolve func(int64) (string, bool)) (string, bool) {
	fieldID, valid := parseSystemVariableID(item.FieldID)
	name, exists := fields[fieldID]
	if !valid || !exists || item.FilterType != "logic" {
		return "", false
	}
	value := item.Value
	if item.ValueType == "variable" {
		variableID, valid := parseSystemVariableID(item.VariableID)
		if !valid || variableID < 1 || resolve == nil {
			return "", false
		}
		value, valid = resolve(variableID)
		if !valid {
			return "", false
		}
	} else if item.ValueType != "" && item.ValueType != "normal" || len(item.VariableID) != 0 && string(item.VariableID) != "null" {
		return "", false
	}
	if len(value) > 256 || strings.ContainsAny(value, "\\\x00") {
		return "", false
	}
	column := `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
	if dialect == "mysql" {
		if !mysqlRowIdentifier(name) {
			return "", false
		}
		column = "`" + name + "`"
	}
	switch item.Term {
	case "null":
		return column + " IS NULL", true
	case "not_null":
		return column + " IS NOT NULL", true
	}
	if value == "" {
		return "", false
	}
	if dialect == "mysql" && !mysqlRowLiteral(value) {
		return "", false
	}
	operators := map[string]string{"eq": "=", "not_eq": "<>", "lt": "<", "le": "<=", "gt": ">", "ge": ">="}
	op, exists := operators[item.Term]
	if !exists {
		return "", false
	}
	return column + " " + op + " '" + strings.ReplaceAll(value, "'", "''") + "'", true
}

func mysqlRowIdentifier(value string) bool {
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

// MySQL string modes disagree on backslash escaping. Keep compatibility rule
// values in a mode-independent subset until the dialect is fixed per binding.
func mysqlRowLiteral(value string) bool {
	if value == "" || len(value) > 256 {
		return false
	}
	for _, ch := range value {
		if ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' ||
			ch == '_' || ch == '-' || ch == ' ' || ch == '.' || ch == ':' || ch == '+' {
			continue
		}
		return false
	}
	return true
}

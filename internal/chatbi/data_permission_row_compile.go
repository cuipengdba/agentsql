package chatbi

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/cuipengdba/agentsql/internal/rowfilter"
)

// compileSQLBotRowPermission turns one original-client row-rule item into a
// bounded PostgreSQL predicate. The caller must still bind the live gateway
// identity, apply every applicable rule, recheck revisions, and execute through
// the controlled gateway. A compiled predicate alone never authorizes a query.
func compileSQLBotRowPermission(raw json.RawMessage, datasource ID, columns []Column) (string, string, string, error) {
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
	predicate, valid := compileSQLBotRowTree(tree, fields, 0, &remaining)
	if !valid || len(predicate) > 4096 {
		return "", "", "", ErrInvalidDataPermission
	}
	if _, err := rowfilter.CompilePredicate(predicate); err != nil {
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
}

func compileSQLBotRowTree(tree sqlbotRowTree, fields map[int64]string, depth int, remaining *int) (string, bool) {
	logic := strings.ToLower(tree.Logic)
	if depth > 8 || len(tree.Items) == 0 || len(tree.Items) > 16 ||
		(logic != "and" && logic != "or") || (logic == "or" && len(tree.Items) > 1) {
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
			part, valid = compileSQLBotRowTree(*item.SubTree, fields, depth+1, remaining)
		case "item":
			if item.SubTree != nil || *remaining == 0 {
				return "", false
			}
			*remaining--
			part, valid = compileSQLBotRowItem(item, fields)
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
	return "(" + strings.Join(parts, " AND ") + ")", true
}

func compileSQLBotRowItem(item sqlbotRowTreeItem, fields map[int64]string) (string, bool) {
	fieldID, valid := parseSystemVariableID(item.FieldID)
	name, exists := fields[fieldID]
	if !valid || !exists || item.FilterType != "logic" ||
		(item.ValueType != "" && item.ValueType != "normal") ||
		len(item.Value) > 256 || strings.ContainsAny(item.Value, "\\\x00") {
		return "", false
	}
	column := `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
	switch item.Term {
	case "null":
		return column + " IS NULL", true
	case "not_null":
		return column + " IS NOT NULL", true
	}
	if item.Value == "" {
		return "", false
	}
	operators := map[string]string{"eq": "=", "not_eq": "<>", "lt": "<", "le": "<=", "gt": ">", "ge": ">="}
	op, exists := operators[item.Term]
	if !exists {
		return "", false
	}
	return column + " " + op + " '" + strings.ReplaceAll(item.Value, "'", "''") + "'", true
}

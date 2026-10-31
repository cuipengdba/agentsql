package chatbi

import (
	"encoding/json"
	"strconv"
)

// compileSQLBotColumnPermission resolves the original field IDs against the
// current gateway schema. The resulting deny set is not an authorization
// decision: callers must still fence the control snapshot and use the gateway.
func compileSQLBotColumnPermission(raw json.RawMessage, datasource ID, columns []Column) (map[string]bool, error) {
	var input struct {
		Type        string          `json:"type"`
		Datasource  json.RawMessage `json:"ds_id"`
		Table       json.RawMessage `json:"table_id"`
		Permissions json.RawMessage `json:"permissions"`
	}
	if len(raw) == 0 || len(raw) > 8192 || json.Unmarshal(raw, &input) != nil || input.Type != "column" {
		return nil, ErrInvalidDataPermission
	}
	dsID, valid := parseSystemVariableID(input.Datasource)
	if !valid || strconv.FormatInt(dsID, 10) != string(datasource) {
		return nil, ErrInvalidDataPermission
	}
	tableID, valid := parseSystemVariableID(input.Table)
	if !valid || tableID < 1 || tableID > maxSQLBotBrowserID || len(input.Permissions) == 0 {
		return nil, ErrInvalidDataPermission
	}
	var schema, table string
	fields := make(map[int64]string)
	for _, column := range columns {
		if column.Schema == "" || column.Table == "" || column.Name == "" ||
			browserTableID(datasource, column.Schema, column.Table) != tableID {
			continue
		}
		if schema != "" && (schema != column.Schema || table != column.Table) ||
			!databaseRemarkName(column.Schema) || !databaseRemarkName(column.Table) || !databaseRemarkName(column.Name) {
			return nil, ErrInvalidDataPermission
		}
		schema, table = column.Schema, column.Table
		fieldID := browserFieldID(tableID, column.Name)
		if prior, exists := fields[fieldID]; exists && prior != column.Name {
			return nil, ErrInvalidDataPermission
		}
		fields[fieldID] = column.Name
	}
	if schema == "" || len(fields) == 0 {
		return nil, ErrInvalidDataPermission
	}
	listJSON := input.Permissions
	if listJSON[0] == '"' {
		var serialized string
		if json.Unmarshal(listJSON, &serialized) != nil || len(serialized) > 8192 {
			return nil, ErrInvalidDataPermission
		}
		listJSON = []byte(serialized)
	}
	var permissions []struct {
		FieldID json.RawMessage `json:"field_id"`
		Enable  *bool           `json:"enable"`
	}
	if json.Unmarshal(listJSON, &permissions) != nil || len(permissions) == 0 || len(permissions) > 4096 {
		return nil, ErrInvalidDataPermission
	}
	seen := make(map[int64]bool, len(permissions))
	blocked := make(map[string]bool)
	for _, permission := range permissions {
		id, valid := parseSystemVariableID(permission.FieldID)
		name, exists := fields[id]
		if !valid || !exists || seen[id] || permission.Enable == nil {
			return nil, ErrInvalidDataPermission
		}
		seen[id] = true
		if !*permission.Enable {
			blocked[disabledFieldKey(schema, table, name)] = true
		}
	}
	return blocked, nil
}

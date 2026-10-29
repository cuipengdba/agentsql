package chatbi

import (
	"encoding/json"
	"strings"
)

func disabledTableKey(schema, table string) string { return schema + "\x00" + table }

// Table status is SQLBot-compatible semantic configuration. Current gateway
// permissions still decide whether a database relation may be queried.
func disabledTables(binding Binding) (map[string]bool, error) {
	if binding.DisabledTables == "" {
		return nil, nil
	}
	if len(binding.DisabledTables) > 131072 {
		return nil, ErrUnavailable
	}
	var tables map[string]bool
	if json.Unmarshal([]byte(binding.DisabledTables), &tables) != nil || tables == nil || len(tables) > 256 {
		return nil, ErrUnavailable
	}
	for key, disabled := range tables {
		parts := strings.Split(key, "\x00")
		if !disabled || len(parts) != 2 || !databaseRemarkName(parts[0]) || !databaseRemarkName(parts[1]) {
			return nil, ErrUnavailable
		}
	}
	return tables, nil
}

func setTableStatus(binding *Binding, schema, table string, checked bool) error {
	if binding == nil || !databaseRemarkName(schema) || !databaseRemarkName(table) {
		return ErrNotFound
	}
	tables, err := disabledTables(*binding)
	if err != nil {
		return err
	}
	if tables == nil {
		tables = make(map[string]bool)
	}
	key := disabledTableKey(schema, table)
	if checked {
		delete(tables, key)
	} else {
		tables[key] = true
	}
	if len(tables) > 256 {
		return ErrUnavailable
	}
	binding.DisabledTables = ""
	if len(tables) != 0 {
		encoded, err := json.Marshal(tables)
		if err != nil || len(encoded) > 131072 {
			return ErrUnavailable
		}
		binding.DisabledTables = string(encoded)
	}
	return nil
}

func filterDisabledTables(binding Binding, columns []Column) ([]Column, error) {
	disabled, err := disabledTables(binding)
	if err != nil || len(disabled) == 0 {
		return columns, err
	}
	visible := make([]Column, 0, len(columns))
	for _, column := range columns {
		if !disabled[disabledTableKey(column.Schema, column.Table)] {
			visible = append(visible, column)
		}
	}
	return visible, nil
}

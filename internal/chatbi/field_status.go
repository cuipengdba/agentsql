package chatbi

import (
	"encoding/json"
	"strings"
)

// Field status controls SQLBot-compatible semantic selection and preview. It
// does not grant or revoke gateway database permissions.
func disabledFieldKey(schema, table, field string) string {
	return schema + "\x00" + table + "\x00" + field
}

func disabledFields(binding Binding) (map[string]bool, error) {
	if binding.DisabledFields == "" {
		return nil, nil
	}
	if len(binding.DisabledFields) > 2<<20 {
		return nil, ErrUnavailable
	}
	var fields map[string]bool
	if json.Unmarshal([]byte(binding.DisabledFields), &fields) != nil || len(fields) > 4096 {
		return nil, ErrUnavailable
	}
	for key, disabled := range fields {
		parts := strings.Split(key, "\x00")
		if !disabled || len(parts) != 3 || !databaseRemarkName(parts[0]) ||
			!databaseRemarkName(parts[1]) || !databaseRemarkName(parts[2]) {
			return nil, ErrUnavailable
		}
	}
	return fields, nil
}

func setFieldStatus(binding *Binding, schema, table, field string, checked bool) error {
	if binding == nil || !databaseRemarkName(schema) || !databaseRemarkName(table) || !databaseRemarkName(field) {
		return ErrNotFound
	}
	fields, err := disabledFields(*binding)
	if err != nil {
		return err
	}
	if fields == nil {
		fields = make(map[string]bool)
	}
	key := disabledFieldKey(schema, table, field)
	if checked {
		delete(fields, key)
	} else {
		fields[key] = true
	}
	if len(fields) > 4096 {
		return ErrUnavailable
	}
	binding.DisabledFields = ""
	if len(fields) != 0 {
		encoded, err := json.Marshal(fields)
		if err != nil || len(encoded) > 2<<20 {
			return ErrUnavailable
		}
		binding.DisabledFields = string(encoded)
	}
	return nil
}

func filterDisabledFields(binding Binding, columns []Column) ([]Column, error) {
	disabled, err := disabledFields(binding)
	if err != nil || len(disabled) == 0 {
		return columns, err
	}
	visible := make([]Column, 0, len(columns))
	for _, column := range columns {
		if !disabled[disabledFieldKey(column.Schema, column.Table, column.Name)] {
			visible = append(visible, column)
		}
	}
	return visible, nil
}

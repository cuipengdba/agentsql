package chatbi

import "sort"

// Database remarks are owner-authored descriptions, never schema or grants.
// The caller supplies only the current gateway-visible and selected columns.
func databaseRemarkHints(binding Binding, columns []Column) ([]RemarkHint, error) {
	if !databaseBinding(binding) {
		return nil, nil
	}
	tables, err := databaseRemarks(binding, false)
	if err != nil {
		return nil, err
	}
	fields, err := databaseRemarks(binding, true)
	if err != nil {
		return nil, err
	}
	if len(tables) == 0 && len(fields) == 0 {
		return nil, nil
	}
	type visibleTable struct {
		schema, name string
		fields       map[string]bool
	}
	visible := make(map[string]*visibleTable)
	for _, column := range columns {
		if !databaseRemarkName(column.Schema) || !databaseRemarkName(column.Table) || !databaseRemarkName(column.Name) {
			return nil, ErrGateway
		}
		key := databaseTableCommentKey(column.Schema, column.Table)
		entry := visible[key]
		if entry == nil {
			entry = &visibleTable{schema: column.Schema, name: column.Table, fields: make(map[string]bool)}
			visible[key] = entry
		}
		entry.fields[column.Name] = true
	}
	keys := make([]string, 0, len(visible))
	for key := range visible {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]RemarkHint, 0, len(tables)+len(fields))
	for _, key := range keys {
		entry := visible[key]
		if comment := tables[key]; comment != "" {
			result = append(result, RemarkHint{Schema: entry.schema, Table: entry.name, Remark: comment})
		}
		fieldNames := make([]string, 0, len(entry.fields))
		for field := range entry.fields {
			fieldNames = append(fieldNames, field)
		}
		sort.Strings(fieldNames)
		for _, field := range fieldNames {
			if comment := fields[databaseFieldCommentKey(entry.schema, entry.name, field)]; comment != "" {
				result = append(result, RemarkHint{Schema: entry.schema, Table: entry.name, Column: field, Remark: comment})
			}
		}
	}
	return boundRemarkHints(result)
}

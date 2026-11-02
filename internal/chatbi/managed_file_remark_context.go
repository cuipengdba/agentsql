package chatbi

import (
	"encoding/json"
	"sort"
)

// RemarkHint is descriptive text supplied by an administrator. It never
// changes the gateway schema or a query authorization decision.
type RemarkHint struct {
	Schema string `json:"schema"`
	Table  string `json:"table"`
	Column string `json:"column,omitempty"`
	Remark string `json:"remark"`
}

// Build context only from columns the gateway just returned for this binding.
// Stored comments for deselected or revoked objects cannot enter a prompt.
func managedFileRemarkHints(binding Binding, columns []Column) ([]RemarkHint, error) {
	if binding.Type != "excel" || binding.Dialect != "pg" ||
		!managedFileOperationPattern.MatchString(binding.ManagedFileID) {
		return nil, nil
	}
	tables, err := managedFileTableComments(binding)
	if err != nil {
		return nil, err
	}
	fields, err := managedFileFieldComments(binding)
	if err != nil {
		return nil, err
	}
	if len(tables) == 0 && len(fields) == 0 {
		return nil, nil
	}
	visible := make(map[string]map[string]bool)
	for _, column := range columns {
		if column.Schema != "agentsql_files" || !managedFileNamePattern.MatchString(column.Table) || column.Name == "" {
			continue
		}
		if visible[column.Table] == nil {
			visible[column.Table] = make(map[string]bool)
		}
		visible[column.Table][column.Name] = true
	}
	names := make([]string, 0, len(visible))
	for table := range visible {
		names = append(names, table)
	}
	sort.Strings(names)
	result := make([]RemarkHint, 0, len(tables)+len(fields))
	for _, table := range names {
		if comment := tables[table]; comment != "" {
			result = append(result, RemarkHint{Schema: "agentsql_files", Table: table, Remark: comment})
		}
		fieldNames := make([]string, 0, len(visible[table]))
		for field := range visible[table] {
			fieldNames = append(fieldNames, field)
		}
		sort.Strings(fieldNames)
		for _, field := range fieldNames {
			if comment := fields[managedFileFieldCommentKey(table, field)]; comment != "" {
				result = append(result, RemarkHint{Schema: "agentsql_files", Table: table, Column: field, Remark: comment})
			}
		}
	}
	// Remarks are optional context. Bound the prompt without preventing a valid
	// authorized query when many long comments have been saved.
	for len(result) > 0 {
		encoded, err := json.Marshal(result)
		if err != nil {
			return nil, ErrUnavailable
		}
		if len(encoded) <= 256<<10 {
			break
		}
		result = result[:len(result)-1]
	}
	return result, nil
}

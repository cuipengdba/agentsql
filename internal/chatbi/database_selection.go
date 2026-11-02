package chatbi

import (
	"encoding/json"
	"strings"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/parser"
)

type databaseTableChoice struct {
	Schema string `json:"schema"`
	Table  string `json:"table"`
}

func databaseBinding(b Binding) bool {
	return b.ManagedFileID == "" && (b.Dialect == "pg" || b.Dialect == "mysql") &&
		(b.Type == "" || b.Type == b.Dialect)
}

// An empty value preserves existing bindings: every currently gateway-visible
// table is selected. "!" is an explicit empty set. A nonempty choice is a
// canonical list of qualified table names, never a browser authorization fact.
func databaseSelectedTables(b Binding) (map[string]string, error) {
	if !databaseBinding(b) {
		return nil, nil
	}
	if b.SelectedTables == "" {
		return nil, nil
	}
	if b.SelectedTables == "!" {
		return map[string]string{}, nil
	}
	if len(b.SelectedTables) > 65536 {
		return nil, ErrGateway
	}
	var choices []databaseTableChoice
	if json.Unmarshal([]byte(b.SelectedTables), &choices) != nil || len(choices) == 0 || len(choices) > 256 {
		return nil, ErrGateway
	}
	selected := make(map[string]string, len(choices))
	for _, choice := range choices {
		if choice.Schema == "" || choice.Table == "" || len(choice.Schema) > 256 || len(choice.Table) > 256 ||
			strings.ContainsRune(choice.Schema, 0) || strings.ContainsRune(choice.Table, 0) || selected[choice.Table] != "" {
			return nil, ErrGateway
		}
		selected[choice.Table] = choice.Schema
	}
	return selected, nil
}

func filterDatabaseSchema(b Binding, columns []Column) ([]Column, error) {
	selected, err := databaseSelectedTables(b)
	if err != nil || selected == nil {
		return columns, err
	}
	visible := make([]Column, 0, len(columns))
	found := make(map[string]bool, len(selected))
	for _, column := range columns {
		if selected[column.Table] == column.Schema && column.Schema != "" {
			visible = append(visible, column)
			found[column.Table] = true
		}
	}
	if len(found) != len(selected) {
		return nil, ErrGateway
	}
	return visible, nil
}

func authorizeDatabaseSelectedSQL(b Binding, sql string) error {
	selected, err := databaseSelectedTables(b)
	if err != nil || selected == nil {
		return err
	}
	if len(selected) == 0 {
		return ErrGateway
	}
	var relations []model.ObjectRef
	if b.Dialect == "pg" {
		parsed, parseErr := parser.ParsePostgresClosedSelect(sql)
		if parseErr != nil {
			return ErrGateway
		}
		for _, relation := range parsed.Relations {
			relations = append(relations, model.ObjectRef{Schema: relation.Schema, Table: relation.Name})
		}
	} else {
		approved, parseErr := parser.NewParser(model.DialectMySQL)
		if parseErr != nil {
			return ErrGateway
		}
		parsed, parseErr := approved.Parse(sql)
		if parseErr != nil || parsed.IsMulti || parsed.StmtType != "SELECT" {
			return ErrGateway
		}
		relations = parsed.Tables
	}
	if len(relations) == 0 {
		return ErrGateway
	}
	for _, relation := range relations {
		schema := selected[relation.Table]
		if schema == "" || (relation.Schema != "" && relation.Schema != schema) {
			return ErrGateway
		}
	}
	return nil
}

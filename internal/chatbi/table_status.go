package chatbi

import (
	"encoding/json"
	"strings"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/parser"
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

func authorizeEnabledTablesSQL(binding Binding, sql string) error {
	disabled, err := disabledTables(binding)
	if err != nil || len(disabled) == 0 {
		return err
	}
	var relations []model.ObjectRef
	switch binding.Dialect {
	case "pg":
		query, parseErr := parser.ParsePostgresClosedSelect(sql)
		if parseErr != nil {
			return ErrGateway
		}
		for _, relation := range postgresClosedAllRelations(query) {
			relations = append(relations, model.ObjectRef{Schema: relation.Schema, Table: relation.Name})
		}
	case "mysql":
		approved, parseErr := parser.NewParser(model.DialectMySQL)
		if parseErr != nil {
			return ErrGateway
		}
		query, parseErr := approved.Parse(sql)
		if parseErr != nil || query.IsMulti || query.StmtType != "SELECT" {
			return ErrGateway
		}
		relations = query.Tables
	default:
		return ErrGateway
	}
	for _, relation := range relations {
		if relation.Schema != "" {
			if disabled[disabledTableKey(relation.Schema, relation.Table)] {
				return ErrGateway
			}
			if binding.Dialect == "mysql" {
				for key := range disabled {
					parts := strings.Split(key, "\x00")
					if strings.EqualFold(parts[0], relation.Schema) && strings.EqualFold(parts[1], relation.Table) {
						return ErrGateway
					}
				}
			}
			continue
		}
		// An unqualified name may resolve to any schema in the database. Deny it
		// if a disabled table has the same name, rather than guessing search_path.
		for key := range disabled {
			if strings.HasSuffix(key, "\x00"+relation.Table) ||
				binding.Dialect == "mysql" && strings.EqualFold(strings.Split(key, "\x00")[1], relation.Table) {
				return ErrGateway
			}
		}
	}
	return nil
}

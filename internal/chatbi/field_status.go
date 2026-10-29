package chatbi

import (
	"encoding/json"
	"strings"

	"github.com/cuipengdba/agentsql/internal/parser"
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
	if json.Unmarshal([]byte(binding.DisabledFields), &fields) != nil || fields == nil || len(fields) > 4096 {
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

// A stale conversation must not use SQL to bypass a field switch. While a
// field is disabled, only the closed, inspectable SELECT shapes are accepted.
// Gateway permissions remain a separate, mandatory authorization layer.
func authorizeEnabledFieldsSQL(binding Binding, sql string) error {
	disabled, err := disabledFields(binding)
	if err != nil || len(disabled) == 0 {
		return err
	}
	switch binding.Dialect {
	case "pg":
		query, err := parser.ParsePostgresClosedSelect(sql)
		if err != nil {
			return ErrGateway
		}
		blocked := make(map[string]bool)
		for _, relation := range postgresClosedAllRelations(query) {
			for key := range disabled {
				parts := strings.Split(key, "\x00")
				if parts[0] == relation.Schema && parts[1] == relation.Name {
					blocked[parts[2]] = true
				}
			}
		}
		if postgresSelectUsesDisabledField(query, blocked) {
			return ErrGateway
		}
	case "mysql":
		shape, err := parser.ParseMySQLColumnSelectShape(sql)
		if err != nil {
			return ErrGateway
		}
		for key := range disabled {
			parts := strings.Split(key, "\x00")
			if !strings.EqualFold(parts[1], shape.Table) ||
				shape.Schema != "" && !strings.EqualFold(parts[0], shape.Schema) {
				continue
			}
			for _, use := range shape.Uses {
				if strings.EqualFold(use.Name, parts[2]) {
					return ErrGateway
				}
			}
		}
	default:
		return ErrGateway
	}
	return nil
}

func postgresClosedAllRelations(query *parser.PostgresClosedSelect) []parser.PostgresClosedRelation {
	if query == nil {
		return nil
	}
	relations := append([]parser.PostgresClosedRelation(nil), query.Relations...)
	var visitExpr func(*parser.PostgresClosedExpr)
	visitExpr = func(expr *parser.PostgresClosedExpr) {
		if expr == nil {
			return
		}
		if expr.Subquery != nil {
			relations = append(relations, postgresClosedAllRelations(expr.Subquery)...)
		}
		for _, argument := range expr.Args {
			visitExpr(argument)
		}
	}
	var visitFrom func(*parser.PostgresClosedFrom)
	visitFrom = func(from *parser.PostgresClosedFrom) {
		if from == nil || from.Join == nil {
			return
		}
		visitExpr(from.Join.Condition)
		visitFrom(from.Join.Left)
		visitFrom(from.Join.Right)
	}
	visitFrom(query.From)
	return relations
}

func postgresSelectUsesDisabledField(query *parser.PostgresClosedSelect, blocked map[string]bool) bool {
	if query == nil {
		return false
	}
	for _, target := range query.Targets {
		if postgresExprUsesDisabledField(target, blocked) {
			return true
		}
	}
	return postgresExprUsesDisabledField(query.Where, blocked) || postgresFromUsesDisabledField(query.From, blocked)
}

func postgresFromUsesDisabledField(from *parser.PostgresClosedFrom, blocked map[string]bool) bool {
	if from == nil || from.Join == nil {
		return false
	}
	return postgresExprUsesDisabledField(from.Join.Condition, blocked) ||
		postgresFromUsesDisabledField(from.Join.Left, blocked) ||
		postgresFromUsesDisabledField(from.Join.Right, blocked)
}

func postgresExprUsesDisabledField(expr *parser.PostgresClosedExpr, blocked map[string]bool) bool {
	if expr == nil {
		return false
	}
	if len(expr.Column) != 0 && blocked[expr.Column[len(expr.Column)-1]] {
		return true
	}
	if postgresSelectUsesDisabledField(expr.Subquery, blocked) {
		return true
	}
	for _, argument := range expr.Args {
		if postgresExprUsesDisabledField(argument, blocked) {
			return true
		}
	}
	return false
}

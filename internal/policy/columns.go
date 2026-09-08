package policy

import (
	"fmt"
	"strings"

	"github.com/cuipengdba/agentsql/internal/model"
)

// SchemaColumn identifies one column returned by a schema-listing source.
type SchemaColumn struct {
	Schema string
	Table  string
	Column string
}

// AuthorizeColumns returns qualified unauthorized columns for SELECT only.
// If an exactly authorized table has no ColumnACL entry, its columns receive
// no additional restriction; table authorization remains R010's responsibility.
func AuthorizeColumns(
	ast *model.AST,
	decision *model.PolicyDecision,
) ([]string, error) {
	if ast == nil {
		return nil, fmt.Errorf("authorize columns: nil AST")
	}
	if decision == nil {
		return nil, fmt.Errorf("authorize columns: nil policy decision")
	}
	if err := validateDecision(decision); err != nil {
		return nil, fmt.Errorf("authorize columns: %w", err)
	}
	unauthorized := make([]string, 0)
	if ast.StmtType != model.StmtType("SELECT") {
		return unauthorized, nil
	}
	for _, column := range ast.Columns {
		if strings.TrimSpace(column) == "" || strings.TrimSpace(column) != column {
			return nil, fmt.Errorf("authorize columns: invalid column name %q", column)
		}
	}
	seen := make(map[string]struct{})
	for _, table := range ast.Tables {
		object, err := tableName(table)
		if err != nil {
			return nil, fmt.Errorf("authorize columns: %w", err)
		}
		if HasBroadTableGrant(decision.AllowedTables, table) {
			continue
		}
		allowedColumns, constrained := decision.ColumnACL[object]
		if !constrained || containsIdentifier(allowedColumns, "*") {
			continue
		}
		for _, column := range ast.Columns {
			if containsIdentifier(allowedColumns, column) {
				continue
			}
			qualified := object + "." + column
			if _, exists := seen[qualified]; exists {
				continue
			}
			seen[qualified] = struct{}{}
			unauthorized = append(unauthorized, qualified)
		}
	}
	return unauthorized, nil
}

// FilterColumnsForSchema removes unauthorized schema columns while preserving
// input order. It never synthesizes columns. A table without a ColumnACL entry
// receives no extra column restriction once its table authorization succeeds.
func FilterColumnsForSchema(
	columns []SchemaColumn,
	decision *model.PolicyDecision,
) ([]SchemaColumn, error) {
	if decision == nil {
		return nil, fmt.Errorf("filter schema columns: nil policy decision")
	}
	if err := validateDecision(decision); err != nil {
		return nil, fmt.Errorf("filter schema columns: %w", err)
	}
	filtered := make([]SchemaColumn, 0, len(columns))
	for _, column := range columns {
		table := model.ObjectRef{Schema: column.Schema, Table: column.Table}
		object, err := tableName(table)
		if err != nil || strings.TrimSpace(column.Column) == "" ||
			strings.TrimSpace(column.Column) != column.Column {
			if err == nil {
				err = fmt.Errorf("schema column %s has invalid column name %q", object, column.Column)
			}
			return nil, fmt.Errorf("filter schema columns: %w", err)
		}
		if MatchesAnyTable(decision.DeniedTables, table) ||
			!MatchesAnyTable(decision.AllowedTables, table) {
			continue
		}
		if HasBroadTableGrant(decision.AllowedTables, table) {
			filtered = append(filtered, column)
			continue
		}
		allowedColumns, constrained := decision.ColumnACL[object]
		if !constrained || containsIdentifier(allowedColumns, "*") ||
			containsIdentifier(allowedColumns, column.Column) {
			filtered = append(filtered, column)
		}
	}
	return filtered, nil
}

func validateDecision(decision *model.PolicyDecision) error {
	patterns := make([]string, 0, len(decision.AllowedTables)+len(decision.DeniedTables))
	patterns = append(patterns, decision.AllowedTables...)
	patterns = append(patterns, decision.DeniedTables...)
	if err := ValidateTablePatterns(patterns); err != nil {
		return err
	}
	for object, columns := range decision.ColumnACL {
		if err := validateColumnObject(object); err != nil {
			return fmt.Errorf("invalid ColumnACL object: %w", err)
		}
		if len(columns) == 0 {
			return fmt.Errorf("ColumnACL for %q has no columns", object)
		}
		for _, column := range columns {
			if strings.TrimSpace(column) == "" || strings.TrimSpace(column) != column {
				return fmt.Errorf("ColumnACL for %q contains invalid column %q", object, column)
			}
		}
	}
	return nil
}

func tableName(table model.ObjectRef) (string, error) {
	if strings.TrimSpace(table.Table) == "" {
		return "", fmt.Errorf("table reference has an empty table name")
	}
	if strings.TrimSpace(table.Table) != table.Table || strings.TrimSpace(table.Schema) != table.Schema {
		return "", fmt.Errorf("table reference contains surrounding whitespace")
	}
	if table.Schema == "" {
		return table.Table, nil
	}
	return table.Schema + "." + table.Table, nil
}

func containsIdentifier(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

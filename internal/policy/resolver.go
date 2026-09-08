package policy

import (
	"fmt"
	"strings"

	"github.com/cuipengdba/agentsql/internal/model"
)

// Resolver merges already-loaded policy rows without accessing storage.
type Resolver struct{}

// NewResolver returns a stateless policy resolver.
func NewResolver() *Resolver {
	return &Resolver{}
}

// Resolve merges policy rows for one Agent and datasource. The caller supplies
// the authenticated Agent level; the resolver never infers it.
func (resolver *Resolver) Resolve(
	policies []model.Policy,
	level string,
) (*model.PolicyDecision, error) {
	if resolver == nil {
		return nil, fmt.Errorf("resolve policies: nil resolver")
	}
	if !validLevel(level) {
		return nil, fmt.Errorf("resolve policies: invalid agent level %q", level)
	}

	decision := &model.PolicyDecision{
		AllowedTables: []string{},
		DeniedTables:  []string{},
		ColumnACL:     make(map[string][]string),
		Level:         level,
	}
	allowedSeen := make(map[string]struct{})
	deniedSeen := make(map[string]struct{})
	columnSeen := make(map[string]map[string]struct{})

	for index, stored := range policies {
		objectType := strings.TrimSpace(stored.ObjectType)
		action := strings.TrimSpace(stored.Action)
		if objectType != stored.ObjectType || !validObjectType(objectType) {
			return nil, fmt.Errorf(
				"resolve policy at position %d: invalid object type %q",
				index,
				stored.ObjectType,
			)
		}
		if action != stored.Action || (action != "allow" && action != "deny") {
			return nil, fmt.Errorf(
				"resolve policy at position %d: invalid action %q",
				index,
				stored.Action,
			)
		}
		if err := ValidateTablePatterns([]string{stored.ObjectName}); err != nil {
			return nil, fmt.Errorf("resolve policy at position %d: %w", index, err)
		}

		if objectType == "column" {
			if err := validateColumnObject(stored.ObjectName); err != nil {
				return nil, fmt.Errorf("resolve policy at position %d: %w", index, err)
			}
			if action != "allow" {
				return nil, fmt.Errorf(
					"resolve policy at position %d: column deny cannot be represented safely",
					index,
				)
			}
			columns, err := policyColumns(stored.Columns)
			if err != nil {
				return nil, fmt.Errorf("resolve policy at position %d: %w", index, err)
			}
			if _, exists := columnSeen[stored.ObjectName]; !exists {
				columnSeen[stored.ObjectName] = make(map[string]struct{})
			}
			for _, column := range columns {
				if _, exists := columnSeen[stored.ObjectName][column]; exists {
					continue
				}
				columnSeen[stored.ObjectName][column] = struct{}{}
				decision.ColumnACL[stored.ObjectName] = append(
					decision.ColumnACL[stored.ObjectName],
					column,
				)
			}
			continue
		}

		if action == "deny" {
			if _, exists := deniedSeen[stored.ObjectName]; !exists {
				deniedSeen[stored.ObjectName] = struct{}{}
				decision.DeniedTables = append(decision.DeniedTables, stored.ObjectName)
			}
			continue
		}
		if _, denied := deniedSeen[stored.ObjectName]; denied {
			continue
		}
		if _, exists := allowedSeen[stored.ObjectName]; !exists {
			allowedSeen[stored.ObjectName] = struct{}{}
			decision.AllowedTables = append(decision.AllowedTables, stored.ObjectName)
		}
	}

	if len(deniedSeen) > 0 {
		filtered := make([]string, 0, len(decision.AllowedTables))
		for _, pattern := range decision.AllowedTables {
			if _, denied := deniedSeen[pattern]; !denied {
				filtered = append(filtered, pattern)
			}
		}
		decision.AllowedTables = filtered
	}
	return decision, nil
}

func validLevel(level string) bool {
	switch level {
	case "readonly", "dml", "ddl":
		return true
	default:
		return false
	}
}

func validObjectType(objectType string) bool {
	switch objectType {
	case "database", "schema", "table", "column":
		return true
	default:
		return false
	}
}

func policyColumns(columns *string) ([]string, error) {
	if columns == nil {
		return nil, fmt.Errorf("column policy has no columns")
	}
	parts := strings.Split(*columns, ",")
	result := make([]string, 0, len(parts))
	seen := make(map[string]struct{})
	for _, part := range parts {
		// Preserve identifier case for quoted names; only storage separators are trimmed.
		column := strings.TrimSpace(part)
		if column == "" {
			return nil, fmt.Errorf("column policy contains an empty identifier")
		}
		if _, exists := seen[column]; exists {
			continue
		}
		seen[column] = struct{}{}
		result = append(result, column)
	}
	return result, nil
}

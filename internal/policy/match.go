package policy

import (
	"fmt"
	"strings"

	"github.com/cuipengdba/agentsql/internal/model"
)

// ValidateTablePatterns validates the exact, global, and schema wildcard
// patterns shared by the policy resolver and R010.
func ValidateTablePatterns(patterns []string) error {
	for _, pattern := range patterns {
		trimmed := strings.TrimSpace(pattern)
		if trimmed == "" {
			return fmt.Errorf("policy contains an empty table pattern")
		}
		if trimmed != pattern {
			return fmt.Errorf("policy table pattern %q contains surrounding whitespace", pattern)
		}
		parts := strings.Split(trimmed, ".")
		if len(parts) > 2 || parts[0] == "" || (len(parts) == 2 && parts[1] == "") {
			return fmt.Errorf("policy table pattern %q is not schema.table", pattern)
		}
		if len(parts) == 2 && parts[0] == "*" {
			return fmt.Errorf("policy table pattern %q has an invalid schema wildcard", pattern)
		}
	}
	return nil
}

func validateColumnObject(object string) error {
	if err := ValidateTablePatterns([]string{object}); err != nil {
		return err
	}
	if object == "*" || strings.HasSuffix(object, ".*") {
		return fmt.Errorf("column policy object %q must identify one table", object)
	}
	return nil
}

// MatchesAnyTable applies the exact wildcard semantics consumed by R010:
// *, schema.*, or an exact unqualified/qualified table name.
func MatchesAnyTable(patterns []string, table model.ObjectRef) bool {
	for _, pattern := range patterns {
		if matchTablePattern(strings.TrimSpace(pattern), table) != tableMatchNone {
			return true
		}
	}
	return false
}

// HasBroadTableGrant reports whether a global or schema wildcard grants all
// columns on table. An exact table grant is intentionally not broad.
func HasBroadTableGrant(patterns []string, table model.ObjectRef) bool {
	for _, pattern := range patterns {
		match := matchTablePattern(strings.TrimSpace(pattern), table)
		if match == tableMatchGlobal || match == tableMatchSchema {
			return true
		}
	}
	return false
}

type tableMatch uint8

const (
	tableMatchNone tableMatch = iota
	tableMatchExact
	tableMatchSchema
	tableMatchGlobal
)

func matchTablePattern(pattern string, table model.ObjectRef) tableMatch {
	if pattern == "*" {
		return tableMatchGlobal
	}
	if table.Schema != "" && pattern == table.Schema+".*" {
		return tableMatchSchema
	}
	object := table.Table
	if table.Schema != "" {
		object = table.Schema + "." + table.Table
	}
	if pattern == object {
		return tableMatchExact
	}
	return tableMatchNone
}

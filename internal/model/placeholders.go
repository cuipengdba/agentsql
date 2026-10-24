package model

// PolicyDecision carries the merged policy inputs consumed by guard rules.
// Policy loading and merging remain the responsibility of T08.
type PolicyDecision struct {
	AllowedTables []string
	DeniedTables  []string
	AllowedKeys   []string
	DeniedKeys    []string
	HashFieldACL  map[string][]string
	ColumnACL     map[string][]string
	Level         string
}

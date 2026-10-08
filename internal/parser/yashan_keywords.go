package parser

import "strings"

// These unquoted words can be mistaken for implicit aliases by the shared
// Oracle grammar. Their syntax has not been qualified for the Yashan profile.
func yashanUnsupportedWord(word string) bool {
	switch strings.ToUpper(word) {
	case "ILIKE", "SIMILAR", "PIVOT", "UNPIVOT", "QUALIFY", "WINDOW",
		"OVER", "PARTITION", "RECURSIVE", "LATERAL", "RETURNING",
		"CONFLICT", "PRIOR", "LEVEL", "CONNECT_BY_ROOT", "JSON_TABLE",
		"UNNEST", "MATCH_RECOGNIZE":
		return true
	default:
		return false
	}
}

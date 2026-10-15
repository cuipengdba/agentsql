package model

import (
	"regexp"
	"strings"
)

// These forms can make a top-level SELECT perform writes or take row locks.
// Until every supported dialect has a verified nested-AST side-effect walk,
// temporary task grants admit only the conservative SELECT subset.
var taskReadUnsafeSQL = regexp.MustCompile(`(?i)\b(WITH|INTO)\b|\bFOR\s+(UPDATE|SHARE|NO\s+KEY\s+UPDATE|KEY\s+SHARE)\b|\bLOCK\s+IN\s+SHARE\s+MODE\b`)

func TaskReadOnlySelect(sqlText string, ast *AST) bool {
	if ast == nil || ast.IsMulti || ast.StmtType != StmtType("SELECT") {
		return false
	}
	trimmed := strings.TrimSpace(sqlText)
	if len(trimmed) < len("SELECT") || !strings.EqualFold(trimmed[:len("SELECT")], "SELECT") {
		return false
	}
	if len(trimmed) > len("SELECT") && !strings.ContainsAny(trimmed[len("SELECT"):len("SELECT")+1], " \t\r\n(") {
		return false
	}
	// Existing PostgreSQL/MySQL tokenizer signals are authoritative when
	// present. The text guard also covers conservative dialect parsers.
	if strings.Contains(sqlText, "/*") || strings.Contains(sqlText, "--") ||
		strings.Contains(sqlText, "#") || strings.ContainsAny(sqlText, "()") {
		// Parentheses are excluded until dialect-specific nested statements
		// and function calls can be proven free of side effects.
		return false
	}
	if taskReadUnsafeSQL.MatchString(sqlText) {
		return false
	}
	for _, operation := range ast.Operations {
		if operation == "SELECT INTO" || operation == "EXPLAIN ANALYZE" || operation == "SQL_COMMENT" {
			return false
		}
	}
	return true
}

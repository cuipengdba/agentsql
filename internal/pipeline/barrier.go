package pipeline

import (
	"strings"

	"github.com/cuipengdba/agentsql/internal/model"
)

type executionBarrier uint8

const (
	barrierDeny executionBarrier = iota
	barrierRead
	barrierTransactionalWrite
	barrierPreIntent
)

// classifyExecutionBarrier is deliberately independent of rule and MCP entry
// filtering. Any AST shape not explicitly covered here fails closed.
func classifyExecutionBarrier(ast *model.AST) executionBarrier {
	if ast == nil {
		return barrierDeny
	}
	switch ast.StmtType {
	case model.StmtType("SELECT"):
		return barrierRead
	case model.StmtType("INSERT"), model.StmtType("UPDATE"), model.StmtType("DELETE"):
		return barrierTransactionalWrite
	case model.StmtType("DDL"):
		return barrierPreIntent
	case model.StmtType("ADMIN"):
		if isTransactionControlAST(ast) {
			return barrierDeny
		}
		return barrierPreIntent
	default:
		return barrierDeny
	}
}

func isTransactionControlAST(ast *model.AST) bool {
	for _, operation := range ast.Operations {
		switch strings.ToUpper(strings.TrimSpace(operation)) {
		case "BEGIN", "START TRANSACTION", "COMMIT", "END", "ROLLBACK", "ABORT", "TRANSACTION",
			"SAVEPOINT", "RELEASE", "RELEASE SAVEPOINT", "SET TRANSACTION":
			return true
		}
	}
	words := leadingSQLWords(ast.RawSQL, 4)
	if len(words) == 0 {
		words = leadingSQLWords(ast.Normalized, 4)
	}
	if len(words) == 0 {
		return false
	}
	switch words[0] {
	case "BEGIN", "COMMIT", "END", "ROLLBACK", "ABORT", "SAVEPOINT":
		return true
	case "START":
		return len(words) > 1 && words[1] == "TRANSACTION"
	case "RELEASE":
		return len(words) > 1 && words[1] == "SAVEPOINT"
	case "SET":
		for _, word := range words[1:] {
			if word == "TRANSACTION" || word == "AUTOCOMMIT" {
				return true
			}
		}
	}
	return false
}

func leadingSQLWords(sqlText string, limit int) []string {
	words := make([]string, 0, limit)
	for index := 0; index < len(sqlText) && len(words) < limit; {
		for index < len(sqlText) && (sqlText[index] == ' ' || sqlText[index] == '\t' || sqlText[index] == '\r' || sqlText[index] == '\n' || sqlText[index] == ';') {
			index++
		}
		if index+1 < len(sqlText) && sqlText[index:index+2] == "--" {
			if end := strings.IndexAny(sqlText[index+2:], "\r\n"); end >= 0 {
				index += end + 3
				continue
			}
			return words
		}
		if index+1 < len(sqlText) && sqlText[index:index+2] == "/*" {
			end := strings.Index(sqlText[index+2:], "*/")
			if end < 0 {
				return words
			}
			index += end + 4
			continue
		}
		start := index
		for index < len(sqlText) {
			character := sqlText[index]
			if !((character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || character == '_') {
				break
			}
			index++
		}
		if start == index {
			index++
			continue
		}
		words = append(words, strings.ToUpper(sqlText[start:index]))
	}
	return words
}

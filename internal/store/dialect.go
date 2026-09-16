package store

import (
	"fmt"
	"strconv"
	"strings"
)

// Dialect identifies the SQL dialect used by the metadata store.
type Dialect string

const (
	// DialectSQLite selects the SQLite metadata-store dialect.
	DialectSQLite Dialect = "sqlite"
	// DialectPostgres selects the PostgreSQL metadata-store dialect.
	DialectPostgres Dialect = "postgres"
)

// ParseDialect normalizes and validates a metadata-store dialect name.
func ParseDialect(value string) (Dialect, error) {
	dialect := Dialect(strings.ToLower(strings.TrimSpace(value)))
	switch dialect {
	case DialectSQLite, DialectPostgres:
		return dialect, nil
	default:
		return "", fmt.Errorf("parse metadata dialect %q: unsupported dialect", value)
	}
}

// rebind converts question-mark placeholders to the target dialect while
// leaving SQL literals, identifiers, and comments untouched.
func rebind(dialect Dialect, query string) (string, error) {
	switch dialect {
	case DialectSQLite:
		return query, nil
	case DialectPostgres:
		return rebindPostgres(query), nil
	default:
		return "", fmt.Errorf("rebind query: unsupported metadata dialect %q", dialect)
	}
}

func rebindPostgres(query string) string {
	var rebound strings.Builder
	rebound.Grow(len(query))
	placeholder := 1

	for index := 0; index < len(query); {
		switch {
		case query[index] == '\'':
			index = copyQuotedSQL(&rebound, query, index, '\'')
		case query[index] == '"':
			index = copyQuotedSQL(&rebound, query, index, '"')
		case strings.HasPrefix(query[index:], "--"):
			index = copyLineComment(&rebound, query, index)
		case strings.HasPrefix(query[index:], "/*"):
			index = copyBlockComment(&rebound, query, index)
		case query[index] == '$':
			delimiter, ok := dollarQuoteDelimiter(query[index:])
			if !ok {
				rebound.WriteByte(query[index])
				index++
				continue
			}
			index = copyDollarQuoted(&rebound, query, index, delimiter)
		case query[index] == '?':
			rebound.WriteByte('$')
			rebound.WriteString(strconv.Itoa(placeholder))
			placeholder++
			index++
		default:
			rebound.WriteByte(query[index])
			index++
		}
	}

	return rebound.String()
}

func copyQuotedSQL(destination *strings.Builder, query string, start int, quote byte) int {
	destination.WriteByte(quote)
	for index := start + 1; index < len(query); index++ {
		destination.WriteByte(query[index])
		if query[index] != quote {
			continue
		}
		if index+1 < len(query) && query[index+1] == quote {
			destination.WriteByte(query[index+1])
			index++
			continue
		}
		return index + 1
	}
	return len(query)
}

func copyLineComment(destination *strings.Builder, query string, start int) int {
	end := strings.IndexByte(query[start:], '\n')
	if end < 0 {
		destination.WriteString(query[start:])
		return len(query)
	}
	end += start + 1
	destination.WriteString(query[start:end])
	return end
}

func copyBlockComment(destination *strings.Builder, query string, start int) int {
	depth := 1
	index := start
	destination.WriteString("/*")
	index += 2
	for index < len(query) {
		switch {
		case strings.HasPrefix(query[index:], "/*"):
			destination.WriteString("/*")
			depth++
			index += 2
		case strings.HasPrefix(query[index:], "*/"):
			destination.WriteString("*/")
			depth--
			index += 2
			if depth == 0 {
				return index
			}
		default:
			destination.WriteByte(query[index])
			index++
		}
	}
	return len(query)
}

func dollarQuoteDelimiter(query string) (string, bool) {
	if len(query) < 2 || query[0] != '$' {
		return "", false
	}
	if query[1] == '$' {
		return "$$", true
	}
	if !isIdentifierStart(query[1]) {
		return "", false
	}
	for index := 2; index < len(query); index++ {
		if query[index] == '$' {
			return query[:index+1], true
		}
		if !isIdentifierPart(query[index]) {
			return "", false
		}
	}
	return "", false
}

func isIdentifierStart(value byte) bool {
	return value == '_' || value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z'
}

func isIdentifierPart(value byte) bool {
	return isIdentifierStart(value) || value >= '0' && value <= '9'
}

func copyDollarQuoted(destination *strings.Builder, query string, start int, delimiter string) int {
	destination.WriteString(delimiter)
	contentsStart := start + len(delimiter)
	closingOffset := strings.Index(query[contentsStart:], delimiter)
	if closingOffset < 0 {
		destination.WriteString(query[contentsStart:])
		return len(query)
	}
	end := contentsStart + closingOffset + len(delimiter)
	destination.WriteString(query[contentsStart:end])
	return end
}

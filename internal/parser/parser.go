package parser

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/cuipengdba/agentsql/internal/model"
)

const (
	postgresDialect       model.DBDialect = "postgres"
	mysqlDialect          model.DBDialect = "mysql"
	sqlCommentOperation                   = "SQL_COMMENT"
	nestingDepthOperation                 = "NESTING_DEPTH"
	unionCountOperation                   = "UNION_COUNT"
	selectColumnOperation                 = "SELECT_COLUMN"
)

var (
	// ErrUnparseable indicates that SQL cannot be proven to be one valid statement.
	ErrUnparseable = errors.New("SQL is not one parseable statement")
	// ErrUnsupportedDialect indicates that no approved parser exists for a dialect.
	ErrUnsupportedDialect = errors.New("unsupported database dialect")
)

// Parser converts one SQL statement into the frozen AgentSQL AST.
type Parser interface {
	Parse(sql string) (*model.AST, error)
}

// NewParser returns the approved parser implementation for dialect.
func NewParser(dialect model.DBDialect) (Parser, error) {
	switch dialect {
	case postgresDialect:
		return &postgresParser{}, nil
	case mysqlDialect:
		return newMySQLParser()
	default:
		return nil, fmt.Errorf("create parser for %q: %w", dialect, ErrUnsupportedDialect)
	}
}

func unparseableError(dialect model.DBDialect, cause error) error {
	return fmt.Errorf("parse %s SQL: %w", dialect, errors.Join(ErrUnparseable, cause))
}

func recoveredError(dialect model.DBDialect, recovered any) error {
	var cause error
	switch value := recovered.(type) {
	case error:
		cause = value
	default:
		cause = fmt.Errorf("parser panic: %v", value)
	}
	return unparseableError(dialect, cause)
}

type stringSet map[string]struct{}

func (set stringSet) add(value string) {
	value = strings.TrimSpace(value)
	if value != "" {
		set[value] = struct{}{}
	}
}

func (set stringSet) sorted() []string {
	values := make([]string, 0, len(set))
	for value := range set {
		values = append(values, value)
	}
	sort.Strings(values)
	return values
}

type objectSet map[string]model.ObjectRef

func (set objectSet) add(object model.ObjectRef) {
	if object.Table == "" {
		return
	}
	key := object.Schema + "\x00" + object.Table + "\x00" + object.Alias
	set[key] = object
}

func (set objectSet) removeUnqualified(names stringSet) {
	for key, object := range set {
		if object.Schema != "" {
			continue
		}
		if _, exists := names[object.Table]; exists {
			delete(set, key)
		}
	}
}

func (set objectSet) removeUnqualifiedFold(names stringSet) {
	for key, object := range set {
		if object.Schema != "" {
			continue
		}
		for name := range names {
			if strings.EqualFold(object.Table, name) {
				delete(set, key)
				break
			}
		}
	}
}

func (set objectSet) sorted() []model.ObjectRef {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	objects := make([]model.ObjectRef, 0, len(keys))
	for _, key := range keys {
		objects = append(objects, set[key])
	}
	return objects
}

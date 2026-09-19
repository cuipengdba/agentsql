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

// topRelationBinding describes one source visible in the root SELECT's FROM
// scope. Non-physical sources deliberately have an empty object.
type topRelationBinding struct {
	object   model.ObjectRef
	name     string
	alias    string
	physical bool
}

func physicalTopBinding(object model.ObjectRef) topRelationBinding {
	return topRelationBinding{
		object: model.ObjectRef{Schema: object.Schema, Table: object.Table},
		name:   object.Table, alias: object.Alias, physical: true,
	}
}

func nonPhysicalTopBinding(name, alias string) topRelationBinding {
	return topRelationBinding{name: name, alias: alias}
}

// resolveDirectProjectionSource resolves only against the bindings visible in
// the root SELECT. It intentionally never consults AST.Tables, which includes
// relations from nested queries and unused CTEs.
func resolveDirectProjectionSource(qualifiers []string, bindings []topRelationBinding) model.ObjectRef {
	switch len(qualifiers) {
	case 0:
		if len(bindings) == 1 && bindings[0].physical {
			return bindings[0].object
		}
	case 1:
		// An explicit alias hides the underlying relation name and takes
		// precedence over an unaliased relation with the same name.
		if matched, count := matchingTopBinding(bindings, func(binding topRelationBinding) bool {
			return binding.alias != "" && binding.alias == qualifiers[0]
		}); count != 0 {
			if count == 1 && matched.physical {
				return matched.object
			}
			return model.ObjectRef{}
		}
		if matched, count := matchingTopBinding(bindings, func(binding topRelationBinding) bool {
			return binding.alias == "" && binding.name == qualifiers[0]
		}); count == 1 && matched.physical {
			return matched.object
		}
	case 2:
		if matched, count := matchingTopBinding(bindings, func(binding topRelationBinding) bool {
			return binding.physical && binding.alias == "" &&
				binding.object.Schema == qualifiers[0] && binding.object.Table == qualifiers[1]
		}); count == 1 {
			return matched.object
		}
	}
	return model.ObjectRef{}
}

func matchingTopBinding(
	bindings []topRelationBinding,
	matches func(topRelationBinding) bool,
) (topRelationBinding, int) {
	var matched topRelationBinding
	count := 0
	for _, binding := range bindings {
		if !matches(binding) {
			continue
		}
		count++
		if count == 1 {
			matched = binding
		}
	}
	return matched, count
}

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

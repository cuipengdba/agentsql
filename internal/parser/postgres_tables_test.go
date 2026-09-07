package parser

import (
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestCollectPostgresRangeVarsSupportsWrappedAndInlineTargets(t *testing.T) {
	tree := map[string]any{
		"SelectStmt": map[string]any{
			"fromClause": []any{wrappedPostgresRangeVar("app", "selected", "s")},
		},
		"UpdateStmt": map[string]any{
			"relation": inlinePostgresRangeVar("app", "updated", "u"),
		},
		"DeleteStmt": map[string]any{
			"relation": inlinePostgresRangeVar("app", "deleted", "d"),
		},
		"InsertStmt": map[string]any{
			"relation": inlinePostgresRangeVar("app", "inserted", "i"),
		},
		"CopyStmt": map[string]any{
			"relation": inlinePostgresRangeVar("app", "copied", ""),
		},
		"VacuumStmt": map[string]any{
			"rels": []any{map[string]any{
				"VacuumRelation": map[string]any{
					"relation": inlinePostgresRangeVar("app", "vacuumed", ""),
				},
			}},
		},
		"CreateStmt": map[string]any{
			"relation": inlinePostgresRangeVar("app", "created", ""),
		},
		"AlterTableStmt": map[string]any{
			"relation": inlinePostgresRangeVar("app", "altered", ""),
		},
		"IndexStmt": map[string]any{
			"relation": inlinePostgresRangeVar("app", "indexed", ""),
		},
		"ReindexStmt": map[string]any{
			"relation": inlinePostgresRangeVar("app", "reindexed", ""),
		},
		"TruncateStmt": map[string]any{
			"relations": []any{wrappedPostgresRangeVar("app", "truncated", "")},
		},
		"GrantStmt": map[string]any{
			"objects": []any{wrappedPostgresRangeVar("app", "granted", "")},
		},
	}

	tables := make(objectSet)
	collectPostgresRangeVars(tree, tables)

	require.Equal(t, sortedObjects([]model.ObjectRef{
		{Schema: "app", Table: "selected", Alias: "s"},
		{Schema: "app", Table: "updated", Alias: "u"},
		{Schema: "app", Table: "deleted", Alias: "d"},
		{Schema: "app", Table: "inserted", Alias: "i"},
		{Schema: "app", Table: "copied"},
		{Schema: "app", Table: "vacuumed"},
		{Schema: "app", Table: "created"},
		{Schema: "app", Table: "altered"},
		{Schema: "app", Table: "indexed"},
		{Schema: "app", Table: "reindexed"},
		{Schema: "app", Table: "truncated"},
		{Schema: "app", Table: "granted"},
	}), sortedObjects(tables.sorted()))
}

func inlinePostgresRangeVar(schema, table, alias string) map[string]any {
	rangeVar := map[string]any{
		"schemaname": schema,
		"relname":    table,
	}
	if alias != "" {
		rangeVar["alias"] = map[string]any{"aliasname": alias}
	}
	return rangeVar
}

func wrappedPostgresRangeVar(schema, table, alias string) map[string]any {
	return map[string]any{"RangeVar": inlinePostgresRangeVar(schema, table, alias)}
}

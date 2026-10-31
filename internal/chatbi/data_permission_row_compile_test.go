package chatbi

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cuipengdba/agentsql/internal/rowfilter"
	"github.com/stretchr/testify/require"
)

func TestOriginalRowRuleCompilesOnlyCurrentGatewayFields(t *testing.T) {
	datasource := ID("10")
	tableID := browserTableID(datasource, "public", "sales")
	fieldID := browserFieldID(tableID, "region")
	columns := []Column{{Schema: "public", Table: "sales", Name: "region"},
		{Schema: "public", Table: "sales", Name: "amount"},
		{Schema: "public", Table: "private", Name: "region"}}
	tree := map[string]any{"logic": "or", "items": []any{map[string]any{
		"type": "item", "field_id": fieldID, "filter_type": "logic", "term": "eq",
		"value": "east' OR 1=1 --", "value_type": "normal",
	}}}
	serialized, err := json.Marshal(tree)
	require.NoError(t, err)
	for _, expression := range []any{tree, string(serialized)} {
		item, err := json.Marshal(map[string]any{"type": "row", "ds_id": datasource,
			"table_id": tableID, "expression_tree": expression})
		require.NoError(t, err)
		schema, table, predicate, err := compileSQLBotRowPermission(item, datasource, columns)
		require.NoError(t, err)
		require.Equal(t, "public", schema)
		require.Equal(t, "sales", table)
		require.Equal(t, `"region" = 'east'' OR 1=1 --'`, predicate)
		_, err = rowfilter.CompilePredicate(predicate)
		require.NoError(t, err)
		rewritten, digest, err := rowfilter.RewriteSingleTable(
			`SELECT region, amount FROM public.sales`, schema, table, []string{predicate})
		require.NoError(t, err)
		require.NotEmpty(t, digest)
		require.Contains(t, rewritten, "east'' OR 1=1 --")
	}
}

func TestOriginalMySQLRowRuleNestedOrRemainsInsideGatewayConjunction(t *testing.T) {
	datasource := ID("10")
	tableID := browserTableID(datasource, "agentsql_demo", "orders")
	fieldID := browserFieldID(tableID, "status")
	columns := []Column{{Schema: "agentsql_demo", Table: "orders", Name: "status"}}
	term := func(value string) map[string]any {
		return map[string]any{"type": "item", "field_id": fieldID, "filter_type": "logic",
			"term": "eq", "value_type": "normal", "value": value}
	}
	tree := map[string]any{"logic": "and", "items": []any{map[string]any{
		"type": "tree", "sub_tree": map[string]any{"logic": "or", "items": []any{term("paid"), term("shipped")}},
	}, term("paid")}}
	raw, err := json.Marshal(map[string]any{"type": "row", "ds_id": datasource,
		"table_id": tableID, "expression_tree": tree})
	require.NoError(t, err)
	schema, table, predicate, err := compileSQLBotRowPermissionForDialect(raw, datasource, columns, "mysql")
	require.NoError(t, err)
	require.Equal(t, "((`status` = 'paid' OR `status` = 'shipped') AND `status` = 'paid')", predicate)
	rewritten, _, err := rowfilter.RewriteMySQLSingleTable(
		"SELECT status FROM agentsql_demo.orders WHERE status = 'shipped' OR status = 'paid'",
		schema, table, []string{predicate})
	require.NoError(t, err)
	require.Contains(t, strings.ToLower(rewritten), "or `status` = 'paid') and")
	require.Contains(t, strings.ToLower(rewritten), "(`status` = 'paid' or `status` = 'shipped') and `status` = 'paid'")
	_, _, postgresPredicate, err := compileSQLBotRowPermission(raw, datasource, columns)
	require.NoError(t, err)
	require.Equal(t, `(("status" = 'paid' OR "status" = 'shipped') AND "status" = 'paid')`, postgresPredicate)
	_, err = rowfilter.CompilePredicate(postgresPredicate)
	require.NoError(t, err)
}

func TestOriginalRowRuleRejectsForgedOrUnsupportedExpressions(t *testing.T) {
	datasource := ID("10")
	tableID := browserTableID(datasource, "public", "sales")
	fieldID := browserFieldID(tableID, "region")
	columns := []Column{{Schema: "public", Table: "sales", Name: "region"}}
	good := map[string]any{"type": "item", "field_id": fieldID, "filter_type": "logic",
		"term": "eq", "value": "east", "value_type": "normal"}
	goodTree := map[string]any{"logic": "and", "items": []any{good}}
	encode := func(ds, table any, tree any) json.RawMessage {
		raw, err := json.Marshal(map[string]any{"type": "row", "ds_id": ds,
			"table_id": table, "expression_tree": tree})
		require.NoError(t, err)
		return raw
	}
	for name, raw := range map[string]json.RawMessage{
		"wrong datasource": encode(11, tableID, goodTree),
		"wrong table":      encode(10, tableID+1, goodTree),
		"no tree":          encode(10, tableID, map[string]any{"logic": "and", "items": []any{}}),
		"forged field": encode(10, tableID, map[string]any{"logic": "and", "items": []any{
			map[string]any{"type": "item", "field_id": fieldID + 1, "filter_type": "logic", "term": "eq", "value": "east"}}}),
		"variable": encode(10, tableID, map[string]any{"logic": "and", "items": []any{
			map[string]any{"type": "item", "field_id": fieldID, "filter_type": "logic", "term": "eq",
				"value_type": "variable", "variable_id": 5, "value": "east"}}}),
		"unsupported term": encode(10, tableID, map[string]any{"logic": "and", "items": []any{
			map[string]any{"type": "item", "field_id": fieldID, "filter_type": "logic", "term": "like", "value": "east"}}}),
		"backslash literal": encode(10, tableID, map[string]any{"logic": "and", "items": []any{
			map[string]any{"type": "item", "field_id": fieldID, "filter_type": "logic", "term": "eq", "value": `east\west`}}}),
		"oversized value": encode(10, tableID, map[string]any{"logic": "and", "items": []any{
			map[string]any{"type": "item", "field_id": fieldID, "filter_type": "logic", "term": "eq", "value": strings.Repeat("x", 257)}}}),
	} {
		t.Run(name, func(t *testing.T) {
			_, _, _, err := compileSQLBotRowPermission(raw, datasource, columns)
			require.ErrorIs(t, err, ErrInvalidDataPermission)
		})
	}
	valid, err := json.Marshal(map[string]any{"type": "row", "ds_id": 10,
		"table_id": tableID, "expression_tree": goodTree})
	require.NoError(t, err)
	_, _, _, err = compileSQLBotRowPermission(valid, datasource,
		[]Column{{Schema: "public", Table: "sales", Name: "other"}})
	require.ErrorIs(t, err, ErrInvalidDataPermission, "a field removed from the live schema cannot compile")
}

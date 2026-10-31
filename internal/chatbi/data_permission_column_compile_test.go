package chatbi

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOriginalColumnRuleCompilesCurrentFieldDenySet(t *testing.T) {
	columns := []Column{{Schema: "public", Table: "sales", Name: "region"},
		{Schema: "public", Table: "sales", Name: "amount"}}
	tableID := browserTableID("10", "public", "sales")
	regionID := browserFieldID(tableID, "region")
	amountID := browserFieldID(tableID, "amount")
	raw, err := json.Marshal(map[string]any{
		"type": "column", "ds_id": 10, "table_id": tableID,
		"permissions": []map[string]any{{"field_id": regionID, "enable": true},
			{"field_id": amountID, "enable": false}},
	})
	require.NoError(t, err)
	blocked, err := compileSQLBotColumnPermission(raw, "10", columns)
	require.NoError(t, err)
	require.Equal(t, map[string]bool{disabledFieldKey("public", "sales", "amount"): true}, blocked)
	for _, malformed := range []map[string]any{
		{"type": "column", "ds_id": 11, "table_id": tableID, "permissions": []any{}},
		{"type": "column", "ds_id": 10, "table_id": tableID, "permissions": []any{map[string]any{"field_id": 999, "enable": false}}},
		{"type": "column", "ds_id": 10, "table_id": tableID, "permissions": []any{map[string]any{"field_id": amountID}}},
		{"type": "column", "ds_id": 10, "table_id": tableID, "permissions": []any{map[string]any{"field_id": amountID, "enable": false}, map[string]any{"field_id": amountID, "enable": true}}},
	} {
		encoded, err := json.Marshal(malformed)
		require.NoError(t, err)
		_, err = compileSQLBotColumnPermission(encoded, "10", columns)
		require.ErrorIs(t, err, ErrInvalidDataPermission)
	}
}

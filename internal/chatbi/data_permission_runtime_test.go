package chatbi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func testActiveRowPermission(t *testing.T, value string) DataPermissionRule {
	t.Helper()
	tableID := browserTableID("10", "public", "sales")
	fieldID := browserFieldID(tableID, "region")
	tree, err := json.Marshal(map[string]any{"logic": "and", "items": []any{map[string]any{
		"type": "item", "field_id": fieldID, "filter_type": "logic", "term": "eq",
		"value_type": "normal", "value": value,
	}}})
	require.NoError(t, err)
	item, err := json.Marshal(map[string]any{"id": 201, "type": "row", "ds_id": 10,
		"table_id": tableID, "expression_tree": string(tree)})
	require.NoError(t, err)
	return DataPermissionRule{ID: 101, OID: 1, Enable: true, Name: "active row",
		Permissions: []json.RawMessage{item}, Users: []int64{1}, WhiteList: "[]", Created: time.Now().UTC()}
}

func TestActiveOriginalRowPermissionRewritesControlledQuery(t *testing.T) {
	service, principal, gateway, _, _ := serviceFixture(t)
	ctx := context.Background()
	gateway.columns = []Column{{Schema: "public", Table: "sales", Name: "region", DataType: "text"},
		{Schema: "public", Table: "sales", Name: "amount", DataType: "numeric"}}
	rule := testActiveRowPermission(t, "east")
	require.NoError(t, service.Store.put(ctx, permissionRuleKey("1", rule.ID), rule, 0))
	binding, err := service.binding(ctx, principal, "10")
	require.NoError(t, err)
	columns, err := service.schema(ctx, principal, binding)
	require.NoError(t, err)
	require.Len(t, columns, 2, "a row rule does not conceal table metadata")
	rawSQL := "SELECT region, amount FROM public.sales"
	_, err = service.query(ctx, principal, binding, rawSQL)
	require.NoError(t, err)
	require.Equal(t, 1, gateway.queries)
	require.NotEqual(t, rawSQL, gateway.lastQuery)
	require.Contains(t, gateway.lastQuery, "region = 'east'")
	_, err = service.query(ctx, principal, binding,
		"SELECT sales.region FROM public.sales JOIN public.other ON true")
	require.ErrorIs(t, err, ErrGateway, "unsupported joins must not bypass the row rule")
	require.Equal(t, 1, gateway.queries)
}

func TestOriginalRuleGroupSaveActivatesOnlyLivePostgresRowPermission(t *testing.T) {
	service, handler, adminToken, _ := adminUsersFixture(t)
	ctx := context.Background()
	gateway := service.Gateway.(*fakeGateway)
	gateway.columns = []Column{{Schema: "public", Table: "sales", Name: "region", DataType: "text"},
		{Schema: "public", Table: "sales", Name: "amount", DataType: "numeric"}}
	tableID := browserTableID("10", "public", "sales")
	fieldID := browserFieldID(tableID, "region")
	tree, err := json.Marshal(map[string]any{"logic": "and", "items": []any{map[string]any{
		"type": "item", "field_id": fieldID, "filter_type": "logic", "term": "eq",
		"value_type": "normal", "value": "east",
	}}})
	require.NoError(t, err)
	item := map[string]any{"id": 1760000000000, "type": "row", "ds_id": 10,
		"table_id": tableID, "expression_tree": string(tree)}
	request, err := json.Marshal(map[string]any{"name": "sales by region", "users": []int64{1},
		"permissions": []any{item}})
	require.NoError(t, err)
	created := accountRequest(t, handler, http.MethodPost, "/api/v1/ds_permission/save", string(request), adminToken)
	require.Equal(t, http.StatusOK, created.Code, created.Body.String())
	principal, err := service.Identity.Verify(ctx, adminToken)
	require.NoError(t, err)
	binding, err := service.binding(ctx, principal, "10")
	require.NoError(t, err)
	_, err = service.query(ctx, principal, binding, "SELECT region, amount FROM public.sales")
	require.NoError(t, err)
	require.Contains(t, gateway.lastQuery, "region = 'east'")
	bad := map[string]any{"type": "row", "ds_id": 10, "table_id": tableID,
		"expression_tree": `{"logic":"and","items":[{"type":"item","field_id":999999,"filter_type":"logic","term":"eq","value":"east"}]}`}
	request, err = json.Marshal(map[string]any{"name": "forged field", "users": []int64{1},
		"permissions": []any{bad}})
	require.NoError(t, err)
	rejected := accountRequest(t, handler, http.MethodPost, "/api/v1/ds_permission/save", string(request), adminToken)
	require.Equal(t, http.StatusUnprocessableEntity, rejected.Code)
	rules, err := service.Store.DataPermissionRules(ctx, "1")
	require.NoError(t, err)
	require.Len(t, rules, 1)
}

func TestActiveOriginalRowPermissionChangeSuppressesInFlightResult(t *testing.T) {
	service, principal, gateway, _, _ := serviceFixture(t)
	ctx := context.Background()
	gateway.columns = []Column{{Schema: "public", Table: "sales", Name: "region", DataType: "text"},
		{Schema: "public", Table: "sales", Name: "amount", DataType: "numeric"}}
	rule := testActiveRowPermission(t, "east")
	require.NoError(t, service.Store.put(ctx, permissionRuleKey("1", rule.ID), rule, 0))
	binding, err := service.binding(ctx, principal, "10")
	require.NoError(t, err)
	gateway.afterQuery = func() {
		changed := testActiveRowPermission(t, "west")
		require.NoError(t, service.Store.put(ctx, permissionRuleKey("1", changed.ID), changed, 1))
		gateway.afterQuery = nil
	}
	data, err := service.query(ctx, principal, binding, "SELECT region, amount FROM public.sales")
	require.ErrorIs(t, err, ErrGateway)
	require.Empty(t, data.Data, "rows calculated under an old rule cannot be returned")
	require.Equal(t, 1, gateway.queries)
}

func TestActiveColumnRuleStillClosesBeforeGateway(t *testing.T) {
	service, principal, gateway, _, _ := serviceFixture(t)
	ctx := context.Background()
	item := json.RawMessage(`{"id":201,"type":"column","ds_id":10,"table_id":20,"permissions":"[]"}`)
	rule := DataPermissionRule{ID: 101, OID: 1, Enable: true, Name: "column not executable",
		Permissions: []json.RawMessage{item}, Users: []int64{1}, WhiteList: "[]", Created: time.Now().UTC()}
	require.NoError(t, service.Store.put(ctx, permissionRuleKey("1", rule.ID), rule, 0))
	binding, err := service.binding(ctx, principal, "10")
	require.NoError(t, err)
	_, err = service.schema(ctx, principal, binding)
	require.ErrorIs(t, err, ErrGateway)
	require.Zero(t, gateway.schemaCalls)
	_, err = service.query(ctx, principal, binding, "SELECT amount FROM public.sales")
	require.ErrorIs(t, err, ErrGateway)
	require.Zero(t, gateway.queries)
}

func TestActiveRuleForAnotherUserDoesNotRewriteThisUsersQuery(t *testing.T) {
	service, principal, gateway, _, _ := serviceFixture(t)
	ctx := context.Background()
	rule := testActiveRowPermission(t, "east")
	rule.Users = []int64{2}
	require.NoError(t, service.Store.put(ctx, permissionRuleKey("1", rule.ID), rule, 0))
	binding, err := service.binding(ctx, principal, "10")
	require.NoError(t, err)
	rawSQL := "SELECT amount FROM public.sales"
	_, err = service.query(ctx, principal, binding, rawSQL)
	require.NoError(t, err)
	require.Equal(t, rawSQL, gateway.lastQuery)
}

func TestActiveRuleRewritesTargetOrdinaryUserWithOwnBinding(t *testing.T) {
	service, admin, gateway, _, _ := serviceFixture(t)
	ctx := context.Background()
	guest := testUser(t, "2", "row-guest")
	require.NoError(t, service.Store.CreateUser(ctx, guest))
	guestPrincipal := Principal{User: guest, Workspace: admin.Workspace}
	adminBinding, err := service.binding(ctx, admin, "10")
	require.NoError(t, err)
	guestBinding := adminBinding
	guestBinding.User = guest.ID
	require.NoError(t, service.Store.put(ctx, bindingKey(guestPrincipal, "10"), guestBinding, 0))
	gateway.columns = []Column{{Schema: "public", Table: "sales", Name: "region", DataType: "text"}}
	rule := testActiveRowPermission(t, "east")
	rule.Users = []int64{2}
	require.NoError(t, service.Store.put(ctx, permissionRuleKey("1", rule.ID), rule, 0))
	binding, err := service.binding(ctx, guestPrincipal, "10")
	require.NoError(t, err)
	_, err = service.query(ctx, guestPrincipal, binding, "SELECT region FROM public.sales")
	require.NoError(t, err)
	require.Contains(t, gateway.lastQuery, "region = 'east'")
}

func TestActiveGroupCannotBeBypassedByNestedItemEnableFlag(t *testing.T) {
	service, principal, gateway, _, _ := serviceFixture(t)
	ctx := context.Background()
	gateway.columns = []Column{{Schema: "public", Table: "sales", Name: "region", DataType: "text"},
		{Schema: "public", Table: "sales", Name: "amount", DataType: "numeric"}}
	rule := testActiveRowPermission(t, "east")
	var item map[string]any
	require.NoError(t, json.Unmarshal(rule.Permissions[0], &item))
	item["enable"] = false
	encoded, err := json.Marshal(item)
	require.NoError(t, err)
	rule.Permissions[0] = encoded
	require.NoError(t, service.Store.put(ctx, permissionRuleKey("1", rule.ID), rule, 0))
	binding, err := service.binding(ctx, principal, "10")
	require.NoError(t, err)
	_, err = service.query(ctx, principal, binding, "SELECT region, amount FROM public.sales")
	require.NoError(t, err)
	require.Contains(t, gateway.lastQuery, "region = 'east'")
}

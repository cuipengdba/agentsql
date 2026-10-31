package chatbi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func variableRowFixture(t *testing.T, definition SystemVariable, selected json.RawMessage) (*Service, Principal, *fakeGateway, Binding) {
	t.Helper()
	service, principal, gateway, _, _ := serviceFixture(t)
	ctx := context.Background()
	gateway.columns = []Column{{Schema: "public", Table: "sales", Name: "region", DataType: "text"},
		{Schema: "public", Table: "sales", Name: "amount", DataType: "numeric"}}
	definition.Created = time.Now().UTC()
	definition.CreatedBy = principal.User.ID
	require.NoError(t, service.Store.put(ctx, systemVariableKey("1", definition.ID), definition, 0))
	user, revision, err := service.Store.User(ctx, principal.User.ID)
	require.NoError(t, err)
	if selected != nil {
		user.SystemVariables = []UserVariableBinding{{Workspace: "1", VariableID: definition.ID,
			VariableValues: []json.RawMessage{selected}}}
	}
	user.AuthRevision++
	require.NoError(t, service.Store.put(ctx, userKey(user.ID), user, revision))
	principal.User = user
	tableID := browserTableID("10", "public", "sales")
	tree, err := json.Marshal(map[string]any{"logic": "and", "items": []any{map[string]any{
		"type": "item", "field_id": browserFieldID(tableID, "region"), "filter_type": "logic",
		"term": "eq", "value_type": "variable", "variable_id": definition.ID,
	}}})
	require.NoError(t, err)
	item, err := json.Marshal(map[string]any{"type": "row", "ds_id": 10, "table_id": tableID,
		"expression_tree": string(tree)})
	require.NoError(t, err)
	rule := DataPermissionRule{ID: 101, OID: 1, Enable: true, Name: "variable region",
		Permissions: []json.RawMessage{item}, Users: []int64{1}, WhiteList: "[]", Created: time.Now().UTC()}
	require.NoError(t, service.Store.put(ctx, permissionRuleKey("1", rule.ID), rule, 0))
	binding, err := service.binding(ctx, principal, "10")
	require.NoError(t, err)
	return service, principal, gateway, binding
}

func TestCustomTextVariableRowPermissionUsesCurrentBoundValue(t *testing.T) {
	definition := SystemVariable{ID: 1000000000001, Name: "region", VarType: "text", Type: "custom",
		Value: json.RawMessage(`["east","west"]`)}
	service, principal, gateway, binding := variableRowFixture(t, definition, json.RawMessage(`"east"`))
	ctx := context.Background()
	_, err := service.schema(ctx, principal, binding)
	require.NoError(t, err)
	_, err = service.query(ctx, principal, binding, "SELECT region, amount FROM public.sales")
	require.NoError(t, err)
	require.Equal(t, 1, gateway.queries)
	require.Contains(t, gateway.lastQuery, "region = 'east'")

	changed := definition
	changed.Value = json.RawMessage(`["west"]`)
	changed.Created = time.Now().UTC()
	changed.CreatedBy = principal.User.ID
	require.NoError(t, service.Store.put(ctx, systemVariableKey("1", definition.ID), changed, 1))
	_, err = service.query(ctx, principal, binding, "SELECT region, amount FROM public.sales")
	require.ErrorIs(t, err, ErrGateway)
	require.Equal(t, 1, gateway.queries, "invalidated binding must not reach gateway")
}

func TestCustomVariableRowPermissionRejectsMissingBinding(t *testing.T) {
	definition := SystemVariable{ID: 1000000000002, Name: "region", VarType: "text", Type: "custom",
		Value: json.RawMessage(`["east"]`)}
	service, principal, gateway, binding := variableRowFixture(t, definition, nil)
	_, err := service.query(context.Background(), principal, binding, "SELECT region, amount FROM public.sales")
	require.ErrorIs(t, err, ErrGateway)
	require.Zero(t, gateway.queries)
}

func TestCustomVariableChangeDuringGatewayQuerySuppressesResult(t *testing.T) {
	definition := SystemVariable{ID: 1000000000003, Name: "region", VarType: "text", Type: "custom",
		Value: json.RawMessage(`["east","west"]`)}
	service, principal, gateway, binding := variableRowFixture(t, definition, json.RawMessage(`"east"`))
	ctx := context.Background()
	gateway.afterQuery = func() {
		changed := definition
		changed.Value = json.RawMessage(`["east"]`)
		changed.Created = time.Now().UTC()
		changed.CreatedBy = principal.User.ID
		require.NoError(t, service.Store.put(ctx, systemVariableKey("1", definition.ID), changed, 1))
		gateway.afterQuery = nil
	}
	data, err := service.query(ctx, principal, binding, "SELECT region, amount FROM public.sales")
	require.ErrorIs(t, err, ErrGateway)
	require.Empty(t, data.Data)
	require.Equal(t, 1, gateway.queries)
}

func TestCustomVariableUserBindingChangeDuringGatewayQuerySuppressesResult(t *testing.T) {
	definition := SystemVariable{ID: 1000000000004, Name: "region", VarType: "text", Type: "custom",
		Value: json.RawMessage(`["east","west"]`)}
	service, principal, gateway, binding := variableRowFixture(t, definition, json.RawMessage(`"east"`))
	ctx := context.Background()
	gateway.afterQuery = func() {
		user, revision, err := service.Store.User(ctx, principal.User.ID)
		require.NoError(t, err)
		user.SystemVariables[0].VariableValues = []json.RawMessage{json.RawMessage(`"west"`)}
		user.AuthRevision++
		require.NoError(t, service.Store.put(ctx, userKey(user.ID), user, revision))
		gateway.afterQuery = nil
	}
	data, err := service.query(ctx, principal, binding, "SELECT region, amount FROM public.sales")
	require.ErrorIs(t, err, ErrGateway)
	require.Empty(t, data.Data)
	require.Equal(t, 1, gateway.queries)
}

func TestCustomVariableBindingDigestChangeWithoutAuthRevisionSuppressesResult(t *testing.T) {
	definition := SystemVariable{ID: 1000000000007, Name: "region", VarType: "text", Type: "custom",
		Value: json.RawMessage(`["east","west"]`)}
	service, principal, gateway, binding := variableRowFixture(t, definition, json.RawMessage(`"east"`))
	ctx := context.Background()
	gateway.afterQuery = func() {
		user, revision, err := service.Store.User(ctx, principal.User.ID)
		require.NoError(t, err)
		user.SystemVariables[0].VariableValues = []json.RawMessage{json.RawMessage(`"west"`)}
		require.NoError(t, service.Store.put(ctx, userKey(user.ID), user, revision))
		gateway.afterQuery = nil
	}
	data, err := service.query(ctx, principal, binding, "SELECT region, amount FROM public.sales")
	require.ErrorIs(t, err, ErrGateway)
	require.Empty(t, data.Data)
	require.Equal(t, 1, gateway.queries)
}

func TestCustomVariableRowPermissionEscapesBoundTextAsOneLiteral(t *testing.T) {
	value := "east' OR 1=1 --"
	allowed, err := json.Marshal([]string{value})
	require.NoError(t, err)
	selected, err := json.Marshal(value)
	require.NoError(t, err)
	definition := SystemVariable{ID: 1000000000005, Name: "region", VarType: "text", Type: "custom", Value: allowed}
	service, principal, gateway, binding := variableRowFixture(t, definition, selected)
	_, err = service.query(context.Background(), principal, binding, "SELECT region, amount FROM public.sales")
	require.NoError(t, err)
	require.Equal(t, 1, gateway.queries)
	require.Contains(t, gateway.lastQuery, "east'' OR 1=1 --")
}

func TestOriginalRuleActivationAcceptsDefinedVariableAndRejectsMissingDefinition(t *testing.T) {
	service, handler, adminToken, _ := adminUsersFixture(t)
	ctx := context.Background()
	gateway := service.Gateway.(*fakeGateway)
	gateway.columns = []Column{{Schema: "public", Table: "sales", Name: "region", DataType: "text"}}
	definition := SystemVariable{ID: 1000000000006, Name: "region", VarType: "text", Type: "custom",
		Value: json.RawMessage(`["east","west"]`), Created: time.Now().UTC(), CreatedBy: "1"}
	require.NoError(t, service.Store.put(ctx, systemVariableKey("1", definition.ID), definition, 0))
	tableID := browserTableID("10", "public", "sales")
	tree, err := json.Marshal(map[string]any{"logic": "and", "items": []any{map[string]any{
		"type": "item", "field_id": browserFieldID(tableID, "region"), "filter_type": "logic",
		"term": "eq", "value_type": "variable", "variable_id": definition.ID,
	}}})
	require.NoError(t, err)
	rule := map[string]any{"name": "variable region", "users": []int64{1},
		"permissions": []any{map[string]any{"type": "row", "ds_id": 10, "table_id": tableID,
			"expression_tree": string(tree)}}}
	body, err := json.Marshal(rule)
	require.NoError(t, err)
	missingBinding := accountRequest(t, handler, http.MethodPost, "/api/v1/ds_permission/save", string(body), adminToken)
	require.Equal(t, http.StatusUnprocessableEntity, missingBinding.Code)
	user, revision, err := service.Store.User(ctx, "1")
	require.NoError(t, err)
	user.SystemVariables = []UserVariableBinding{{Workspace: "1", VariableID: definition.ID,
		VariableValues: []json.RawMessage{json.RawMessage(`"east"`)}}}
	require.NoError(t, service.Store.put(ctx, userKey("1"), user, revision))
	accepted := accountRequest(t, handler, http.MethodPost, "/api/v1/ds_permission/save", string(body), adminToken)
	require.Equal(t, http.StatusOK, accepted.Code, accepted.Body.String())

	definition.ID++
	tree, err = json.Marshal(map[string]any{"logic": "and", "items": []any{map[string]any{
		"type": "item", "field_id": browserFieldID(tableID, "region"), "filter_type": "logic",
		"term": "eq", "value_type": "variable", "variable_id": definition.ID,
	}}})
	require.NoError(t, err)
	rule["permissions"] = []any{map[string]any{"type": "row", "ds_id": 10, "table_id": tableID,
		"expression_tree": string(tree)}}
	body, err = json.Marshal(rule)
	require.NoError(t, err)
	rejected := accountRequest(t, handler, http.MethodPost, "/api/v1/ds_permission/save", string(body), adminToken)
	require.Equal(t, http.StatusUnprocessableEntity, rejected.Code)
}

func TestCustomVariableTypesResolveOnlyValidatedBoundValues(t *testing.T) {
	for _, test := range []struct {
		name, kind, definition, selected, expected string
	}{
		{"text", "text", `["east","west"]`, `"east"`, "east"},
		{"key value", "kv", `[{"key":"east","value":"华东"}]`, `"华东"`, "华东"},
		{"number", "number", `[0,5.25]`, `5.25`, "5.25"},
		{"date", "datetime", `["2026-01-01","2026-12-31"]`, `"2026-10-31"`, "2026-10-31"},
	} {
		t.Run(test.name, func(t *testing.T) {
			definition := SystemVariable{ID: 1000000000010, Name: "region", VarType: test.kind,
				Type: "custom", Value: json.RawMessage(test.definition)}
			service, principal, gateway, binding := variableRowFixture(t, definition, json.RawMessage(test.selected))
			_, err := service.query(context.Background(), principal, binding, "SELECT region, amount FROM public.sales")
			require.NoError(t, err)
			require.Equal(t, 1, gateway.queries)
			require.Contains(t, gateway.lastQuery, "region = '"+test.expected+"'")
		})
	}
}

func TestMySQLVariableNumberUsesModeIndependentLiteral(t *testing.T) {
	datasource := ID("10")
	tableID := browserTableID(datasource, "agentsql_demo", "orders")
	fieldID := browserFieldID(tableID, "amount")
	item, err := json.Marshal(map[string]any{"type": "row", "ds_id": 10, "table_id": tableID,
		"expression_tree": map[string]any{"logic": "and", "items": []any{map[string]any{
			"type": "item", "field_id": fieldID, "filter_type": "logic", "term": "eq",
			"value_type": "variable", "variable_id": 1000000000011,
		}}}})
	require.NoError(t, err)
	_, _, predicate, err := compileSQLBotRowPermissionWithVariables(item, datasource,
		[]Column{{Schema: "agentsql_demo", Table: "orders", Name: "amount", DataType: "decimal"}}, "mysql",
		func(id int64) ([]string, bool) { return []string{"5.25"}, id == 1000000000011 })
	require.NoError(t, err)
	require.Equal(t, "`amount` = '5.25'", predicate)
}

func TestVariableMultiValueMembershipKeepsEveryBoundValue(t *testing.T) {
	datasource := ID("10")
	tableID := browserTableID(datasource, "agentsql_demo", "orders")
	fieldID := browserFieldID(tableID, "status")
	for _, test := range []struct{ term, mysql, postgres string }{
		{"in", "`status` IN ('paid', 'shipped')", `("status" = 'paid' OR "status" = 'shipped')`},
		{"not in", "`status` NOT IN ('paid', 'shipped')", `("status" <> 'paid' AND "status" <> 'shipped')`},
	} {
		t.Run(test.term, func(t *testing.T) {
			item, err := json.Marshal(map[string]any{"type": "row", "ds_id": 10, "table_id": tableID,
				"expression_tree": map[string]any{"logic": "and", "items": []any{map[string]any{
					"type": "item", "field_id": fieldID, "filter_type": "logic", "term": test.term,
					"value_type": "variable", "variable_id": 1000000000012,
				}}}})
			require.NoError(t, err)
			columns := []Column{{Schema: "agentsql_demo", Table: "orders", Name: "status", DataType: "varchar"}}
			resolver := func(id int64) ([]string, bool) {
				return []string{"paid", "shipped"}, id == 1000000000012
			}
			_, _, predicate, err := compileSQLBotRowPermissionWithVariables(item, datasource, columns, "mysql", resolver)
			require.NoError(t, err)
			require.Equal(t, test.mysql, predicate)
			_, _, predicate, err = compileSQLBotRowPermissionWithVariables(item, datasource, columns, "pg", resolver)
			require.NoError(t, err)
			require.Equal(t, test.postgres, predicate)
			_, _, _, err = compileSQLBotRowPermissionWithVariables(item, datasource, columns, "mysql",
				func(int64) ([]string, bool) { return []string{"paid", "shipped' OR 1=1 --"}, true })
			require.ErrorIs(t, err, ErrInvalidDataPermission)
			_, _, predicate, err = compileSQLBotRowPermissionWithVariables(item, datasource, columns, "pg",
				func(int64) ([]string, bool) { return []string{"paid", "shipped' OR 1=1 --"}, true })
			require.NoError(t, err)
			require.Contains(t, predicate, "shipped'' OR 1=1 --")
		})
	}
}

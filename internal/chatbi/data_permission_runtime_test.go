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

func TestActiveOriginalMySQLRowPermissionRewritesAndClosesUnsupportedQueries(t *testing.T) {
	service, principal, gateway, _, _ := serviceFixture(t)
	ctx := context.Background()
	var binding Binding
	revision, err := service.Store.get(ctx, bindingKey(principal, "10"), &binding)
	require.NoError(t, err)
	binding.Dialect = "mysql"
	binding.Type = "mysql"
	require.NoError(t, service.Store.put(ctx, bindingKey(principal, "10"), binding, revision))
	gateway.columns = []Column{{Schema: "agentsql_demo", Table: "orders", Name: "status", DataType: "text"},
		{Schema: "agentsql_demo", Table: "orders", Name: "id", DataType: "integer"}}
	tableID := browserTableID("10", "agentsql_demo", "orders")
	tree, err := json.Marshal(map[string]any{"logic": "and", "items": []any{map[string]any{
		"type": "item", "field_id": browserFieldID(tableID, "status"), "filter_type": "logic",
		"term": "eq", "value_type": "normal", "value": "paid",
	}}})
	require.NoError(t, err)
	item, err := json.Marshal(map[string]any{"type": "row", "ds_id": 10, "table_id": tableID,
		"expression_tree": string(tree)})
	require.NoError(t, err)
	rule := DataPermissionRule{ID: 101, OID: 1, Enable: true, Name: "MySQL status",
		Permissions: []json.RawMessage{item}, Users: []int64{1}, WhiteList: "[]", Created: time.Now().UTC()}
	require.NoError(t, service.Store.put(ctx, permissionRuleKey("1", rule.ID), rule, 0))
	binding, err = service.binding(ctx, principal, "10")
	require.NoError(t, err)
	_, err = service.query(ctx, principal, binding, "SELECT status FROM agentsql_demo.orders WHERE status = 'paid' OR status = 'shipped'")
	require.NoError(t, err)
	require.Equal(t, 1, gateway.queries)
	require.Contains(t, gateway.lastQuery, "`status` = 'paid'")
	require.Contains(t, gateway.lastQuery, "(`status` = 'paid' or `status` = 'shipped') and")
	for _, sql := range []string{
		"SELECT status FROM orders", "SELECT * FROM agentsql_demo.orders",
		"SELECT count(*) FROM agentsql_demo.orders", "SELECT status FROM agentsql_demo.orders JOIN agentsql_demo.users ON true",
	} {
		_, err = service.query(ctx, principal, binding, sql)
		require.ErrorIs(t, err, ErrGateway, sql)
		require.Equal(t, 1, gateway.queries)
	}
	gateway.afterQuery = func() {
		changed := rule
		changed.Enable = false
		require.NoError(t, service.Store.put(ctx, permissionRuleKey("1", rule.ID), changed, 1))
		gateway.afterQuery = nil
	}
	data, err := service.query(ctx, principal, binding, "SELECT status FROM agentsql_demo.orders")
	require.ErrorIs(t, err, ErrGateway)
	require.Empty(t, data.Data)
	require.Equal(t, 2, gateway.queries)
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
	item["auth_target_type"] = "role"
	request, err = json.Marshal(map[string]any{"name": "unsupported role target", "users": []int64{1},
		"permissions": []any{item}})
	require.NoError(t, err)
	rejected = accountRequest(t, handler, http.MethodPost, "/api/v1/ds_permission/save", string(request), adminToken)
	require.Equal(t, http.StatusUnprocessableEntity, rejected.Code)
	rules, err := service.Store.DataPermissionRules(ctx, "1")
	require.NoError(t, err)
	require.Len(t, rules, 1)
}

func TestSingleUserExplicitTargetAppliesRowRuleAndRejectsMismatch(t *testing.T) {
	service, handler, adminToken, _ := adminUsersFixture(t)
	ctx := context.Background()
	gateway := service.Gateway.(*fakeGateway)
	gateway.columns = []Column{{Schema: "public", Table: "sales", Name: "region", DataType: "text"},
		{Schema: "public", Table: "sales", Name: "amount", DataType: "numeric"}}
	tableID := browserTableID("10", "public", "sales")
	tree, err := json.Marshal(map[string]any{"logic": "and", "items": []any{map[string]any{
		"type": "item", "field_id": browserFieldID(tableID, "region"), "filter_type": "logic",
		"term": "eq", "value_type": "normal", "value": "east",
	}}})
	require.NoError(t, err)
	item := map[string]any{"type": "row", "ds_id": 10, "table_id": tableID,
		"expression_tree": string(tree), "auth_target_type": "user", "auth_target_id": 1}
	request, err := json.Marshal(map[string]any{"name": "one explicit member", "users": []int64{1},
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
	require.Equal(t, 1, gateway.queries)
	require.Contains(t, gateway.lastQuery, "region = 'east'")

	item["auth_target_id"] = 2
	request, err = json.Marshal(map[string]any{"name": "mismatched member", "users": []int64{1},
		"permissions": []any{item}})
	require.NoError(t, err)
	rejected := accountRequest(t, handler, http.MethodPost, "/api/v1/ds_permission/save", string(request), adminToken)
	require.Equal(t, http.StatusUnprocessableEntity, rejected.Code)
	require.Equal(t, 1, gateway.queries)
	require.False(t, supportedDataPermissionTarget(json.RawMessage(`{"auth_target_type":"user","auth_target_id":1}`), []int64{1, 2}))
	require.False(t, supportedDataPermissionTarget(json.RawMessage(`{"auth_target_id":1}`), []int64{1}))
}

func TestRestoredMismatchedExplicitTargetClosesBeforeGateway(t *testing.T) {
	service, principal, gateway, _, _ := serviceFixture(t)
	ctx := context.Background()
	rule := testActiveRowPermission(t, "east")
	var item map[string]any
	require.NoError(t, json.Unmarshal(rule.Permissions[0], &item))
	item["auth_target_type"] = "user"
	item["auth_target_id"] = 2
	rule.Permissions[0], _ = json.Marshal(item)
	require.NoError(t, service.Store.put(ctx, permissionRuleKey("1", rule.ID), rule, 0))
	binding, err := service.binding(ctx, principal, "10")
	require.NoError(t, err)
	_, err = service.schema(ctx, principal, binding)
	require.ErrorIs(t, err, ErrGateway)
	require.Zero(t, gateway.schemaCalls)
	_, err = service.query(ctx, principal, binding, "SELECT region FROM public.sales")
	require.ErrorIs(t, err, ErrGateway)
	require.Zero(t, gateway.queries)
}

func TestRestoredActiveRoleTargetClosesForWorkspaceBeforeGateway(t *testing.T) {
	service, principal, gateway, _, _ := serviceFixture(t)
	ctx := context.Background()
	rule := testActiveRowPermission(t, "east")
	rule.Users = nil
	rule.Permissions[0] = json.RawMessage(`{"type":"row","auth_target_type":"role","auth_target_id":2,"ds_id":10}`)
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

func TestRestoredActiveWhitelistClosesBeforeGateway(t *testing.T) {
	service, principal, gateway, _, _ := serviceFixture(t)
	ctx := context.Background()
	rule := testActiveRowPermission(t, "east")
	rule.WhiteList = "[2]"
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

func TestActiveRoleQualifiedMultiUserWhitelistEnforcesAndRevokes(t *testing.T) {
	service, handler, adminToken, _ := adminUsersFixture(t)
	ctx := context.Background()
	gateway := service.Gateway.(*fakeGateway)
	gateway.columns = []Column{{Schema: "public", Table: "sales", Name: "region", DataType: "text"},
		{Schema: "public", Table: "sales", Name: "amount", DataType: "numeric"}}
	admin, err := service.Identity.Verify(ctx, adminToken)
	require.NoError(t, err)
	adminBinding, err := service.binding(ctx, admin, "10")
	require.NoError(t, err)
	alice := testUser(t, "3", "role-alice")
	require.NoError(t, service.Store.CreateUser(ctx, alice))
	for _, id := range []ID{"2", "3"} {
		user, _, err := service.Store.User(ctx, id)
		require.NoError(t, err)
		binding := adminBinding
		binding.User = id
		require.NoError(t, service.Store.put(ctx, bindingKey(Principal{User: user, Workspace: "1"}, "10"), binding, 0))
	}
	rule := testActiveRowPermission(t, "east")
	var item map[string]any
	require.NoError(t, json.Unmarshal(rule.Permissions[0], &item))
	item["auth_target_type"] = "role"
	item["auth_target_id"] = 2 // ordinary workspace member
	item["white_list_user"] = "[3]"
	tableID := browserTableID("10", "public", "sales")
	columnItem := map[string]any{"type": "column", "ds_id": 10, "table_id": tableID,
		"permissions": []any{map[string]any{"field_id": browserFieldID(tableID, "region"), "enable": true},
			map[string]any{"field_id": browserFieldID(tableID, "amount"), "enable": false}},
		"auth_target_type": "role", "auth_target_id": 2, "white_list_user": "[2]"}
	request, err := json.Marshal(map[string]any{"name": "two members", "users": []int64{2, 3},
		"white_list_user": "[2]", "permissions": []any{item, columnItem}})
	require.NoError(t, err)
	created := accountRequest(t, handler, http.MethodPost, "/api/v1/ds_permission/save", string(request), adminToken)
	require.Equal(t, http.StatusOK, created.Code, created.Body.String())
	for _, id := range []ID{"2", "3"} {
		user, _, err := service.Store.User(ctx, id)
		require.NoError(t, err)
		principal := Principal{User: user, Workspace: "1"}
		binding, err := service.binding(ctx, principal, "10")
		require.NoError(t, err)
		_, err = service.query(ctx, principal, binding, "SELECT region FROM public.sales")
		require.NoError(t, err)
		require.Contains(t, gateway.lastQuery, "region = 'east'", id)
		visible, err := service.schema(ctx, principal, binding)
		require.NoError(t, err)
		require.Len(t, visible, 1)
		require.Equal(t, "region", visible[0].Name)
		_, err = service.query(ctx, principal, binding, "SELECT amount FROM public.sales")
		require.ErrorIs(t, err, ErrGateway)
	}
	require.Equal(t, 2, gateway.queries)

	// A newly joined role member is denied until included and preflighted.
	newMember := testUser(t, "4", "role-new")
	require.NoError(t, service.Store.CreateUser(ctx, newMember))
	newBinding := adminBinding
	newBinding.User = newMember.ID
	newPrincipal := Principal{User: newMember, Workspace: "1"}
	require.NoError(t, service.Store.put(ctx, bindingKey(newPrincipal, "10"), newBinding, 0))
	_, err = service.query(ctx, newPrincipal, newBinding, "SELECT region FROM public.sales")
	require.ErrorIs(t, err, ErrGateway)
	require.Equal(t, 2, gateway.queries)

	// A listed user's role change invalidates the rule before gateway execution.
	bob, revision, err := service.Store.User(ctx, "2")
	require.NoError(t, err)
	bob.Memberships[0].Weight = 1
	bob.AuthRevision++
	require.NoError(t, service.Store.put(ctx, userKey(bob.ID), bob, revision))
	bobPrincipal := Principal{User: bob, Workspace: "1"}
	bobBinding, err := service.binding(ctx, bobPrincipal, "10")
	require.NoError(t, err)
	_, err = service.query(ctx, bobPrincipal, bobBinding, "SELECT region FROM public.sales")
	require.ErrorIs(t, err, ErrGateway)
	require.Equal(t, 2, gateway.queries)

	// An active whitelist change is a policy revision even though the listed
	// users never bypass the row predicate.
	bob.Memberships[0].Weight = 0
	bob.AuthRevision++
	require.NoError(t, service.Store.put(ctx, userKey(bob.ID), bob, revision+1))
	bobPrincipal.User = bob
	alice, aliceRevision, err := service.Store.User(ctx, "3")
	require.NoError(t, err)
	alicePrincipal := Principal{User: alice, Workspace: "1"}
	aliceBinding, err := service.binding(ctx, alicePrincipal, "10")
	require.NoError(t, err)
	gateway.afterQuery = func() {
		changed := alice
		changed.Memberships[0].Weight = 1
		changed.AuthRevision++
		require.NoError(t, service.Store.put(ctx, userKey(changed.ID), changed, aliceRevision))
		gateway.afterQuery = nil
	}
	result, err := service.query(ctx, alicePrincipal, aliceBinding, "SELECT region FROM public.sales")
	require.ErrorIs(t, err, ErrGateway)
	require.Empty(t, result.Data)
	require.Equal(t, 3, gateway.queries)
	alice, aliceRevision, err = service.Store.User(ctx, "3")
	require.NoError(t, err)
	alice.Memberships[0].Weight = 0
	alice.AuthRevision++
	require.NoError(t, service.Store.put(ctx, userKey(alice.ID), alice, aliceRevision))

	stored, err := service.Store.DataPermissionRules(ctx, "1")
	require.NoError(t, err)
	require.Len(t, stored, 1)
	gateway.afterQuery = func() {
		changed := stored[0]
		changed.WhiteList = "[3]"
		require.NoError(t, service.Store.put(ctx, permissionRuleKey("1", changed.ID), changed, 1))
		gateway.afterQuery = nil
	}
	result, err = service.query(ctx, bobPrincipal, bobBinding, "SELECT region FROM public.sales")
	require.ErrorIs(t, err, ErrGateway)
	require.Empty(t, result.Data)
	require.Equal(t, 4, gateway.queries)
}

func TestRoleTargetAndWhitelistCannotExpandExplicitScope(t *testing.T) {
	service, handler, adminToken, _ := adminUsersFixture(t)
	rule := testActiveRowPermission(t, "east")
	var item map[string]any
	require.NoError(t, json.Unmarshal(rule.Permissions[0], &item))
	item["auth_target_type"] = "role"
	item["auth_target_id"] = 2
	for _, request := range []map[string]any{
		{"name": "unknown role", "users": []int64{2}, "permissions": []any{map[string]any{"type": "row", "auth_target_type": "role", "auth_target_id": 9}}},
		{"name": "outside group list", "users": []int64{2}, "white_list_user": "[3]", "permissions": []any{item}},
		{"name": "outside item list", "users": []int64{2}, "permissions": []any{func() map[string]any {
			copy := make(map[string]any, len(item)+1)
			for key, value := range item {
				copy[key] = value
			}
			copy["white_list_user"] = "[3]"
			return copy
		}()}},
	} {
		body, err := json.Marshal(request)
		require.NoError(t, err)
		response := accountRequest(t, handler, http.MethodPost, "/api/v1/ds_permission/save", string(body), adminToken)
		require.Equal(t, http.StatusUnprocessableEntity, response.Code, response.Body.String())
	}
	rules, err := service.Store.DataPermissionRules(context.Background(), "1")
	require.NoError(t, err)
	require.Empty(t, rules)

	rule.WhiteList = "[1]"
	first, err := activationDigest(rule)
	require.NoError(t, err)
	rule.WhiteList = "[]"
	second, err := activationDigest(rule)
	require.NoError(t, err)
	require.NotEqual(t, first, second)
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

type permissionChangingEmbedding struct {
	change func()
}

func (model *permissionChangingEmbedding) Encode(ctx context.Context, texts []string) ([][]float32, error) {
	if model.change != nil {
		model.change()
		model.change = nil
	}
	return fixedEmbedding{}.Encode(ctx, texts)
}

func TestAskClosesWhenPermissionChangesDuringEmbeddingBeforeModel(t *testing.T) {
	service, principal, gateway, generator, token := serviceFixture(t)
	ctx := context.Background()
	gateway.columns = []Column{{Schema: "public", Table: "sales", Name: "region", DataType: "text"},
		{Schema: "public", Table: "sales", Name: "amount", DataType: "numeric"}}
	rule := testActiveRowPermission(t, "east")
	require.NoError(t, service.Store.put(ctx, permissionRuleKey("1", rule.ID), rule, 0))
	user, revision, err := service.Store.User(ctx, principal.User.ID)
	require.NoError(t, err)
	user.Admin = true
	require.NoError(t, service.Store.put(ctx, userKey(user.ID), user, revision))
	admin := principal
	admin.User = user
	_, err = service.SaveTerminology(ctx, admin, Terminology{Word: "revenue", Description: "approved measure", Enabled: true,
		SpecificDS: true, DatasourceIDs: []int64{10}})
	require.NoError(t, err)
	settings := defaultChatSettings()
	settings.Embedding.Enabled = true
	require.NoError(t, service.Store.put(ctx, chatSettingsKey(), settings, 0))
	service.Embedding = &permissionChangingEmbedding{change: func() {
		revoked := rule
		revoked.Enable = false
		require.NoError(t, service.Store.put(ctx, permissionRuleKey("1", rule.ID), revoked, 1))
	}}
	chat, err := service.Start(ctx, principal, "10", "first", 0)
	require.NoError(t, err)
	check := func(ctx context.Context) error { _, err := service.Identity.Verify(ctx, token); return err }
	record, err := service.Ask(ctx, principal, chat.ID, "sales amount", check, nil)
	require.ErrorIs(t, err, ErrGateway)
	require.Zero(t, generator.calls, "revoked schema reached the model")
	require.Zero(t, gateway.queries, "revoked schema reached the gateway query")
	require.Empty(t, record.SQL)
	require.Nil(t, record.Data)
}

func TestAskClosesWhenPermissionChangesDuringGenerationBeforeQuery(t *testing.T) {
	service, principal, gateway, generator, token := serviceFixture(t)
	ctx := context.Background()
	gateway.columns = []Column{{Schema: "public", Table: "sales", Name: "region", DataType: "text"},
		{Schema: "public", Table: "sales", Name: "amount", DataType: "numeric"}}
	rule := testActiveRowPermission(t, "east")
	require.NoError(t, service.Store.put(ctx, permissionRuleKey("1", rule.ID), rule, 0))
	chat, err := service.Start(ctx, principal, "10", "first", 0)
	require.NoError(t, err)
	generator.after = func() {
		revoked := rule
		revoked.Enable = false
		require.NoError(t, service.Store.put(ctx, permissionRuleKey("1", rule.ID), revoked, 1))
	}
	check := func(ctx context.Context) error { _, err := service.Identity.Verify(ctx, token); return err }
	record, err := service.Ask(ctx, principal, chat.ID, "sales amount", check, nil)
	require.ErrorIs(t, err, ErrGateway)
	require.Equal(t, 1, generator.calls)
	require.Zero(t, gateway.queries, "SQL generated under a revoked rule reached the gateway")
	require.Empty(t, record.SQL)
	require.Nil(t, record.Data)
}

func TestAskClosesWhenRowPermissionChangesAfterTerminalSaveBeforeDelivery(t *testing.T) {
	service, principal, gateway, _, token := serviceFixture(t)
	ctx := context.Background()
	gateway.columns = []Column{{Schema: "public", Table: "sales", Name: "region", DataType: "text"},
		{Schema: "public", Table: "sales", Name: "amount", DataType: "numeric"}}
	rule := testActiveRowPermission(t, "east")
	require.NoError(t, service.Store.put(ctx, permissionRuleKey("1", rule.ID), rule, 0))
	chat, err := service.Start(ctx, principal, "10", "first", 0)
	require.NoError(t, err)
	changed := false
	check := func(ctx context.Context) error {
		if _, err := service.Identity.Verify(ctx, token); err != nil {
			return err
		}
		if changed {
			return nil
		}
		stored, _, err := service.chat(ctx, principal, chat.ID)
		if err != nil {
			return err
		}
		if len(stored.Records) == 1 && stored.Records[0].Finish && stored.Records[0].Data != nil {
			revoked := rule
			revoked.Enable = false
			if err := service.Store.put(ctx, permissionRuleKey("1", rule.ID), revoked, 1); err != nil {
				return err
			}
			changed = true
		}
		return nil
	}
	result, err := service.Ask(ctx, principal, chat.ID, "sales amount", check, nil)
	require.True(t, changed, "the row rule did not change after terminal persistence")
	require.ErrorIs(t, err, ErrGateway)
	require.Nil(t, result.Data)
	require.Empty(t, result.SQL)
	require.Equal(t, 1, gateway.queries)
	history, err := service.ReadChat(ctx, principal, chat.ID, true)
	require.NoError(t, err)
	require.Len(t, history.Records, 1)
	require.Nil(t, history.Records[0].Data)
	require.Empty(t, history.Records[0].SQL)
	require.Equal(t, 1, gateway.queries, "old SQL was re-executed after the rule changed")
}

func TestRevokedColumnDoesNotReturnThroughModelHistory(t *testing.T) {
	service, principal, gateway, _, token := serviceFixture(t)
	ctx := context.Background()
	gateway.columns = []Column{{Schema: "public", Table: "sales", Name: "region", DataType: "text"},
		{Schema: "public", Table: "sales", Name: "amount", DataType: "numeric"}}
	generator := &historyModel{}
	service.Model = generator
	chat, err := service.Start(ctx, principal, "10", "first", 0)
	require.NoError(t, err)
	check := func(ctx context.Context) error { _, err := service.Identity.Verify(ctx, token); return err }
	_, err = service.Ask(ctx, principal, chat.ID, "first", check, nil)
	require.NoError(t, err)

	tableID := browserTableID("10", "public", "sales")
	permissionList, err := json.Marshal([]map[string]any{
		{"field_id": browserFieldID(tableID, "region"), "enable": true},
		{"field_id": browserFieldID(tableID, "amount"), "enable": false},
	})
	require.NoError(t, err)
	item, err := json.Marshal(map[string]any{"id": 201, "type": "column", "ds_id": 10,
		"table_id": tableID, "permissions": string(permissionList)})
	require.NoError(t, err)
	rule := DataPermissionRule{ID: 101, OID: 1, Enable: true, Name: "hide amount",
		Permissions: []json.RawMessage{item}, Users: []int64{1}, WhiteList: "[]", Created: time.Now().UTC()}
	require.NoError(t, service.Store.put(ctx, permissionRuleKey("1", rule.ID), rule, 0))
	_, err = service.Ask(ctx, principal, chat.ID, "second", check, nil)
	require.ErrorIs(t, err, ErrGateway)
	require.Len(t, generator.histories, 2)
	require.Empty(t, generator.histories[1], "revoked SQL reached the model as history")
	require.Equal(t, 1, gateway.queries)
}

func TestRowPermissionChangeDropsOtherwiseVisibleModelHistory(t *testing.T) {
	service, principal, gateway, _, token := serviceFixture(t)
	ctx := context.Background()
	gateway.columns = []Column{{Schema: "public", Table: "sales", Name: "region", DataType: "text"},
		{Schema: "public", Table: "sales", Name: "amount", DataType: "numeric"}}
	generator := &historyModel{}
	service.Model = generator
	chat, err := service.Start(ctx, principal, "10", "first", 0)
	require.NoError(t, err)
	check := func(ctx context.Context) error { _, err := service.Identity.Verify(ctx, token); return err }
	_, err = service.Ask(ctx, principal, chat.ID, "before rule", check, nil)
	require.NoError(t, err)
	rule := testActiveRowPermission(t, "east")
	require.NoError(t, service.Store.put(ctx, permissionRuleKey("1", rule.ID), rule, 0))
	_, err = service.Ask(ctx, principal, chat.ID, "after rule", check, nil)
	require.NoError(t, err)
	require.Empty(t, generator.histories[1], "SQL remains visible but its old row policy must not enter the model")
	_, err = service.Ask(ctx, principal, chat.ID, "same rule", check, nil)
	require.NoError(t, err)
	require.Len(t, generator.histories[2], 1)
	require.Equal(t, "after rule", generator.histories[2][0].Question)
	require.Equal(t, 3, gateway.queries)
}

func TestRowPermissionChangeRedactsHistoricalRecordResponses(t *testing.T) {
	service, principal, gateway, _, token := serviceFixture(t)
	ctx := context.Background()
	gateway.columns = []Column{{Schema: "public", Table: "sales", Name: "region", DataType: "text"},
		{Schema: "public", Table: "sales", Name: "amount", DataType: "numeric"}}
	service.Model = &historyModel{}
	chat, err := service.Start(ctx, principal, "10", "before rule", 0)
	require.NoError(t, err)
	check := func(ctx context.Context) error { _, err := service.Identity.Verify(ctx, token); return err }
	first, err := service.Ask(ctx, principal, chat.ID, "before rule", check, nil)
	require.NoError(t, err)
	require.NotEmpty(t, first.SQL)
	require.Equal(t, 1, gateway.queries)

	rule := testActiveRowPermission(t, "east")
	require.NoError(t, service.Store.put(ctx, permissionRuleKey("1", rule.ID), rule, 0))
	old, err := service.Record(ctx, principal, first.ID, true)
	require.NoError(t, err)
	require.Equal(t, "before rule", old.Question)
	require.Empty(t, old.SQL)
	require.Empty(t, old.SQLAnswer)
	require.Empty(t, old.Chart)
	require.Nil(t, old.Data)
	require.Equal(t, 1, gateway.queries, "stale SQL was re-executed")
	history, err := service.ReadChat(ctx, principal, chat.ID, true)
	require.NoError(t, err)
	require.Len(t, history.Records, 1)
	require.Empty(t, history.Records[0].SQL)
	require.Nil(t, history.Records[0].Data)
	require.Equal(t, 1, gateway.queries)

	second, err := service.Ask(ctx, principal, chat.ID, "after rule", check, nil)
	require.NoError(t, err)
	current, err := service.Record(ctx, principal, second.ID, true)
	require.NoError(t, err)
	require.NotEmpty(t, current.SQL, "new answer under the current policy was hidden")
	require.NotNil(t, current.Data)
}

func TestLegacyChatTurnWithoutPolicyFingerprintIsNotModelContext(t *testing.T) {
	service, principal, _, _, token := serviceFixture(t)
	ctx := context.Background()
	generator := &historyModel{}
	service.Model = generator
	chat, err := service.Start(ctx, principal, "10", "first", 0)
	require.NoError(t, err)
	check := func(ctx context.Context) error { _, err := service.Identity.Verify(ctx, token); return err }
	first, err := service.Ask(ctx, principal, chat.ID, "before upgrade", check, nil)
	require.NoError(t, err)
	require.NoError(t, service.Store.delete(ctx, recordPolicyKey(principal, first.ID), 1))
	_, err = service.Ask(ctx, principal, chat.ID, "after upgrade", check, nil)
	require.NoError(t, err)
	require.Len(t, generator.histories, 2)
	require.Empty(t, generator.histories[1])
}

func TestInvalidActiveColumnRuleClosesBeforeQuery(t *testing.T) {
	service, principal, gateway, _, _ := serviceFixture(t)
	ctx := context.Background()
	item := json.RawMessage(`{"id":201,"type":"column","ds_id":10,"table_id":20,"permissions":"[]"}`)
	rule := DataPermissionRule{ID: 101, OID: 1, Enable: true, Name: "column not executable",
		Permissions: []json.RawMessage{item}, Users: []int64{1}, WhiteList: "[]", Created: time.Now().UTC()}
	require.NoError(t, service.Store.put(ctx, permissionRuleKey("1", rule.ID), rule, 0))
	binding, err := service.binding(ctx, principal, "10")
	require.NoError(t, err)
	columns, err := service.schema(ctx, principal, binding)
	require.ErrorIs(t, err, ErrGateway)
	require.Empty(t, columns)
	_, err = service.query(ctx, principal, binding, "SELECT amount FROM public.sales")
	require.ErrorIs(t, err, ErrGateway)
	require.Zero(t, gateway.queries)
}

func TestActiveOriginalColumnPermissionHidesAndDeniesFields(t *testing.T) {
	service, principal, gateway, _, _ := serviceFixture(t)
	ctx := context.Background()
	gateway.columns = []Column{{Schema: "public", Table: "sales", Name: "region", DataType: "text"},
		{Schema: "public", Table: "sales", Name: "amount", DataType: "numeric"}}
	tableID := browserTableID("10", "public", "sales")
	permissionList, err := json.Marshal([]map[string]any{
		{"field_id": browserFieldID(tableID, "region"), "enable": true},
		{"field_id": browserFieldID(tableID, "amount"), "enable": false},
	})
	require.NoError(t, err)
	item, err := json.Marshal(map[string]any{"id": 201, "type": "column", "ds_id": 10,
		"table_id": tableID, "permissions": string(permissionList)})
	require.NoError(t, err)
	rule := DataPermissionRule{ID: 101, OID: 1, Enable: true, Name: "hide amount",
		Permissions: []json.RawMessage{item}, Users: []int64{1}, WhiteList: "[]", Created: time.Now().UTC()}
	require.NoError(t, service.Store.put(ctx, permissionRuleKey("1", rule.ID), rule, 0))
	binding, err := service.binding(ctx, principal, "10")
	require.NoError(t, err)
	visible, err := service.schema(ctx, principal, binding)
	require.NoError(t, err)
	require.Len(t, visible, 1)
	require.Equal(t, "region", visible[0].Name)
	for _, sql := range []string{
		"SELECT amount FROM public.sales",
		"SELECT region FROM public.sales WHERE amount > 0",
		"SELECT * FROM public.sales",
		"SELECT sales.* FROM public.sales",
	} {
		_, err := service.query(ctx, principal, binding, sql)
		require.ErrorIs(t, err, ErrGateway, sql)
		require.Zero(t, gateway.queries, sql)
	}
	_, err = service.query(ctx, principal, binding, "SELECT region FROM public.sales")
	require.NoError(t, err)
	require.Equal(t, 1, gateway.queries)
	gateway.afterQuery = func() {
		changed := rule
		changed.Permissions = []json.RawMessage{json.RawMessage(`{"type":"column","ds_id":10,"table_id":20,"permissions":"[]"}`)}
		require.NoError(t, service.Store.put(ctx, permissionRuleKey("1", rule.ID), changed, 1))
		gateway.afterQuery = nil
	}
	data, err := service.query(ctx, principal, binding, "SELECT region FROM public.sales")
	require.ErrorIs(t, err, ErrGateway)
	require.Empty(t, data.Data)
	require.Equal(t, 2, gateway.queries)
}

func TestOriginalColumnRuleSaveActivatesCurrentGatewayFields(t *testing.T) {
	service, handler, adminToken, _ := adminUsersFixture(t)
	ctx := context.Background()
	gateway := service.Gateway.(*fakeGateway)
	gateway.columns = []Column{{Schema: "public", Table: "sales", Name: "region", DataType: "text"},
		{Schema: "public", Table: "sales", Name: "amount", DataType: "numeric"}}
	tableID := browserTableID("10", "public", "sales")
	item := map[string]any{"type": "column", "ds_id": 10, "table_id": tableID,
		"permissions": []any{map[string]any{"field_id": browserFieldID(tableID, "region"), "enable": true},
			map[string]any{"field_id": browserFieldID(tableID, "amount"), "enable": false}}}
	request, err := json.Marshal(map[string]any{"name": "hide amount", "users": []int64{1},
		"permissions": []any{item}})
	require.NoError(t, err)
	created := accountRequest(t, handler, http.MethodPost, "/api/v1/ds_permission/save", string(request), adminToken)
	require.Equal(t, http.StatusOK, created.Code, created.Body.String())
	principal, err := service.Identity.Verify(ctx, adminToken)
	require.NoError(t, err)
	binding, err := service.binding(ctx, principal, "10")
	require.NoError(t, err)
	_, err = service.query(ctx, principal, binding, "SELECT amount FROM public.sales")
	require.ErrorIs(t, err, ErrGateway)
	require.Zero(t, gateway.queries)
	item["permissions"] = []any{map[string]any{"field_id": 999999, "enable": false}}
	request, err = json.Marshal(map[string]any{"name": "forged column", "users": []int64{1},
		"permissions": []any{item}})
	require.NoError(t, err)
	rejected := accountRequest(t, handler, http.MethodPost, "/api/v1/ds_permission/save", string(request), adminToken)
	require.Equal(t, http.StatusUnprocessableEntity, rejected.Code)
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

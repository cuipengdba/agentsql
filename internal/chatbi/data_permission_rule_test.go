package chatbi

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOriginalDataPermissionRuleGroupIsScopedAndAudited(t *testing.T) {
	service, handler, adminToken, memberToken := adminUsersFixture(t)
	path := "/api/v1/ds_permission/"
	list := accountRequest(t, handler, http.MethodPost, path+"list", "", adminToken)
	require.Equal(t, http.StatusOK, list.Code)
	require.Contains(t, list.Body.String(), `"data":[]`)
	require.Equal(t, http.StatusUnauthorized,
		accountRequest(t, handler, http.MethodPost, path+"list", "", memberToken).Code)
	require.Equal(t, http.StatusUnauthorized,
		accountRequest(t, handler, http.MethodPost, path+"list", "", "").Code)
	created := accountRequest(t, handler, http.MethodPost, path+"save",
		`{"name":"sales scope","users":[2],"permissions":[]}`, adminToken)
	require.Equal(t, http.StatusOK, created.Code, created.Body.String())
	var envelope struct {
		Data struct {
			ID      int64   `json:"id"`
			OID     int64   `json:"oid"`
			Enabled bool    `json:"enable"`
			Users   []int64 `json:"user_list"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(created.Body.Bytes(), &envelope))
	require.Positive(t, envelope.Data.ID)
	require.EqualValues(t, 1, envelope.Data.OID)
	require.True(t, envelope.Data.Enabled)
	require.Equal(t, []int64{2}, envelope.Data.Users)
	id := strconv.FormatInt(envelope.Data.ID, 10)
	read := accountRequest(t, handler, http.MethodPost, path+"get/"+id, "", adminToken)
	require.Equal(t, http.StatusOK, read.Code)
	require.Contains(t, read.Body.String(), `"permissions":[]`)
	require.Contains(t, read.Body.String(), `"users":[2]`)
	require.Equal(t, http.StatusUnauthorized,
		accountRequest(t, handler, http.MethodPost, path+"get/"+id, "", memberToken).Code)
	updated := accountRequest(t, handler, http.MethodPost, path+"save",
		`{"id":"`+id+`","oid":1,"name":"sales scope updated","users":[2],"permissions":[]}`, adminToken)
	require.Equal(t, http.StatusOK, updated.Code, updated.Body.String())
	rules, err := service.Store.DataPermissionRules(context.Background(), "1")
	require.NoError(t, err)
	require.Len(t, rules, 1)
	require.Equal(t, "sales scope updated", rules[0].Name)
	require.Equal(t, http.StatusUnprocessableEntity,
		accountRequest(t, handler, http.MethodPost, path+"save",
			`{"id":"`+id+`","oid":2,"name":"forged workspace","permissions":[]}`, adminToken).Code)
	require.Equal(t, http.StatusUnauthorized,
		accountRequest(t, handler, http.MethodPost, path+"delete/"+id, "", memberToken).Code)
	deleted := accountRequest(t, handler, http.MethodPost, path+"delete/"+id, "", adminToken)
	require.Equal(t, http.StatusOK, deleted.Code)
	rules, err = service.Store.DataPermissionRules(context.Background(), "1")
	require.NoError(t, err)
	require.Empty(t, rules)
	audits, err := service.Store.list(context.Background(), "management-audit", "system", "system")
	require.NoError(t, err)
	require.Len(t, audits, 3)
}

func TestDataPermissionDraftCannotBecomeActiveBeforeEnforcement(t *testing.T) {
	service, handler, adminToken, _ := adminUsersFixture(t)
	path := "/api/v1/ds_permission/save"
	draft := accountRequest(t, handler, http.MethodPost, path,
		`{"name":"row draft","enable":false,"users":[2],"permissions":[{"type":"row","ds_id":10,"table_id":20,"expression_tree":"{}"}]}`, adminToken)
	require.Equal(t, http.StatusOK, draft.Code, draft.Body.String())
	rules, err := service.Store.DataPermissionRules(context.Background(), "1")
	require.NoError(t, err)
	require.Len(t, rules, 1)
	require.False(t, rules[0].Enable)
	require.Len(t, rules[0].Permissions, 1)
	id := strconv.FormatInt(rules[0].ID, 10)
	active := accountRequest(t, handler, http.MethodPost, path,
		`{"id":"`+id+`","name":"row draft","enable":true,"users":[2],"permissions":[{"type":"row","ds_id":10,"table_id":20,"expression_tree":"{}"}]}`, adminToken)
	require.Equal(t, http.StatusUnprocessableEntity, active.Code)
	current, err := service.Store.DataPermissionRule(context.Background(), "1", rules[0].ID)
	require.NoError(t, err)
	require.False(t, current.Enable)
}

func TestUnresolvedActiveDataPermissionClosesBeforeGatewayAndAfterQuery(t *testing.T) {
	service, principal, gateway, _, _ := serviceFixture(t)
	ctx := context.Background()
	rule := DataPermissionRule{ID: 100, OID: 1, Enable: true, Name: "restored active rule",
		Permissions: []json.RawMessage{json.RawMessage(`{"type":"row"}`)}, Users: []int64{1},
		WhiteList: "[]", Created: time.Now().UTC()}
	require.NoError(t, service.Store.put(ctx, permissionRuleKey("1", rule.ID), rule, 0))
	binding, err := service.binding(ctx, principal, "10")
	require.NoError(t, err)
	_, err = service.schema(ctx, principal, binding)
	require.ErrorIs(t, err, ErrGateway)
	require.Zero(t, gateway.schemaCalls)
	_, err = service.gatewaySchema(ctx, principal, binding)
	require.ErrorIs(t, err, ErrGateway, "unfiltered schema discovery must also close")
	require.Zero(t, gateway.schemaCalls)
	_, err = service.query(ctx, principal, binding, "SELECT amount FROM public.sales")
	require.ErrorIs(t, err, ErrGateway)
	require.Zero(t, gateway.queries)
	rule.Enable = false
	require.NoError(t, service.Store.put(ctx, permissionRuleKey("1", rule.ID), rule, 1))
	_, err = service.schema(ctx, principal, binding)
	require.NoError(t, err)
	require.Equal(t, 1, gateway.schemaCalls)
	_, err = service.query(ctx, principal, binding, "SELECT amount FROM public.sales")
	require.NoError(t, err)
	require.Equal(t, 1, gateway.queries)
	gateway.afterSchema = func() {
		rule.Enable = true
		require.NoError(t, service.Store.put(ctx, permissionRuleKey("1", rule.ID), rule, 2))
		gateway.afterSchema = nil
	}
	_, err = service.schema(ctx, principal, binding)
	require.ErrorIs(t, err, ErrGateway, "new policy during schema read must suppress metadata")
	rule.Enable = false
	require.NoError(t, service.Store.put(ctx, permissionRuleKey("1", rule.ID), rule, 3))
	gateway.afterQuery = func() {
		rule.Enable = true
		require.NoError(t, service.Store.put(ctx, permissionRuleKey("1", rule.ID), rule, 4))
		gateway.afterQuery = nil
	}
	_, err = service.query(ctx, principal, binding, "SELECT amount FROM public.sales")
	require.ErrorIs(t, err, ErrGateway, "new policy during query must suppress rows")
	require.Equal(t, 2, gateway.queries)
}

func TestDataPermissionAuditFailureRollsBackRule(t *testing.T) {
	service, handler, adminToken, _ := adminUsersFixture(t)
	_, err := service.Store.db.ExecContext(context.Background(),
		`CREATE TRIGGER reject_permission_audit BEFORE INSERT ON chatbi_objects WHEN NEW.kind='management-audit' BEGIN SELECT RAISE(ABORT,'synthetic audit fault'); END`)
	require.NoError(t, err)
	response := accountRequest(t, handler, http.MethodPost, "/api/v1/ds_permission/save",
		`{"name":"must roll back","permissions":[],"users":[]}`, adminToken)
	require.Equal(t, http.StatusServiceUnavailable, response.Code)
	rules, err := service.Store.DataPermissionRules(context.Background(), "1")
	require.NoError(t, err)
	require.Empty(t, rules)
}

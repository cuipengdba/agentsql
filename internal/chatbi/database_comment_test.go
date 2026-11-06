package chatbi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOriginalDatabaseRemarksAreScopedToCurrentGatewaySchemaAndAudited(t *testing.T) {
	ctx := context.Background()
	service, principal, base, _, token := serviceFixture(t)
	user, revision, err := service.Store.User(ctx, principal.User.ID)
	require.NoError(t, err)
	user.Admin = true
	require.NoError(t, service.Store.put(ctx, userKey(user.ID), user, revision))
	principal, err = service.Identity.Verify(ctx, token)
	require.NoError(t, err)
	binding, revision, _, err := service.bindingSnapshot(ctx, principal, "10")
	require.NoError(t, err)
	binding.Configuration = "server-owned-database-credential"
	require.NoError(t, service.Store.put(ctx, bindingKey(principal, "10"), binding, revision))
	gateway := &selectionTestGateway{fakeGateway: base, columns: []Column{
		{Schema: "public", Table: "sales", Name: "amount", DataType: "numeric"},
		{Schema: "public", Table: "sales", Name: "region", DataType: "text"},
	}}
	service.Gateway = gateway
	handler, err := NewHandler(service, "test")
	require.NoError(t, err)
	request := func(method, path, body, bearer string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if bearer != "" {
			r.Header.Set("X-SQLBOT-TOKEN", "Bearer "+bearer)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	tableID := browserTableID("10", "public", "sales")
	fieldID := browserFieldID(tableID, "amount")
	tableEdit := func(comment string) string {
		body, err := json.Marshal(map[string]any{"id": tableID, "ds_id": 10,
			"table_name": "sales", "table_comment": "", "custom_comment": comment,
			"checked": true, "embedding": nil})
		require.NoError(t, err)
		return string(body)
	}
	fieldEdit := func(comment string) string {
		body, err := json.Marshal(map[string]any{"id": fieldID, "ds_id": 10,
			"table_id": tableID, "field_name": "amount", "field_type": "numeric",
			"field_index": 0, "field_comment": nil, "custom_comment": comment,
			"checked": true})
		require.NoError(t, err)
		return string(body)
	}
	require.Equal(t, http.StatusUnauthorized,
		request(http.MethodPost, "/api/v1/datasource/editTable", tableEdit("private"), "").Code)
	require.Equal(t, http.StatusOK,
		request(http.MethodPost, "/api/v1/datasource/editTable", tableEdit("Sales table"), token).Code)
	require.Equal(t, http.StatusOK,
		request(http.MethodPost, "/api/v1/datasource/editField", fieldEdit("Sales amount"), token).Code)
	current, err := service.binding(ctx, principal, "10")
	require.NoError(t, err)
	require.Equal(t, binding.Configuration, current.Configuration)
	require.Equal(t, binding.BindingID, current.BindingID)
	tables, err := databaseRemarks(current, false)
	require.NoError(t, err)
	require.Equal(t, "Sales table", tables[databaseTableCommentKey("public", "sales")])
	fields, err := databaseRemarks(current, true)
	require.NoError(t, err)
	require.Equal(t, "Sales amount", fields[databaseFieldCommentKey("public", "sales", "amount")])
	list := request(http.MethodPost, "/api/v1/datasource/tableList/10", "", token)
	require.Equal(t, http.StatusOK, list.Code)
	require.Contains(t, list.Body.String(), "Sales table")
	require.NotContains(t, list.Body.String(), binding.Configuration)
	fieldList := request(http.MethodPost, "/api/v1/datasource/fieldList/10/"+strconv.FormatInt(tableID, 10), `{}`, token)
	require.Equal(t, http.StatusOK, fieldList.Code)
	require.Contains(t, fieldList.Body.String(), "Sales amount")
	require.NotContains(t, fieldList.Body.String(), binding.ConnectorToken)

	wrong := strings.Replace(tableEdit("forged"), `"table_name":"sales"`, `"table_name":"other"`, 1)
	require.NotEqual(t, http.StatusOK,
		request(http.MethodPost, "/api/v1/datasource/editTable", wrong, token).Code)
	wrong = strings.Replace(fieldEdit("forged"), `"field_index":0`, `"field_index":1`, 1)
	require.NotEqual(t, http.StatusOK,
		request(http.MethodPost, "/api/v1/datasource/editField", wrong, token).Code)
	_, err = service.Store.db.ExecContext(ctx, `CREATE TRIGGER reject_database_remark_audit BEFORE INSERT ON chatbi_objects
		WHEN NEW.kind='management-audit' BEGIN SELECT RAISE(FAIL, 'audit offline'); END`)
	require.NoError(t, err)
	require.NotEqual(t, http.StatusOK,
		request(http.MethodPost, "/api/v1/datasource/editTable", tableEdit("rolled back"), token).Code)
	current, err = service.binding(ctx, principal, "10")
	require.NoError(t, err)
	tables, err = databaseRemarks(current, false)
	require.NoError(t, err)
	require.Equal(t, "Sales table", tables[databaseTableCommentKey("public", "sales")])
	_, err = service.Store.db.ExecContext(ctx, `DROP TRIGGER reject_database_remark_audit`)
	require.NoError(t, err)
	gateway.columns = []Column{{Schema: "public", Table: "other", Name: "amount", DataType: "numeric"}}
	require.NotEqual(t, http.StatusOK,
		request(http.MethodPost, "/api/v1/datasource/editTable", tableEdit("revoked"), token).Code)
	require.NotEqual(t, http.StatusOK,
		request(http.MethodPost, "/api/v1/datasource/editField", fieldEdit("revoked"), token).Code)
	list = request(http.MethodPost, "/api/v1/datasource/tableList/10", "", token)
	require.Equal(t, http.StatusOK, list.Code)
	require.NotContains(t, list.Body.String(), "Sales table")

	audits, err := service.Store.list(ctx, "management-audit", "system", "system")
	require.NoError(t, err)
	seen := map[string]bool{}
	for _, raw := range audits {
		var event struct {
			Action string `json:"action"`
		}
		require.NoError(t, json.Unmarshal(raw, &event))
		seen[event.Action] = true
		require.NotContains(t, string(raw), "Sales amount")
		require.NotContains(t, string(raw), "Sales table")
	}
	require.True(t, seen["database.table-comment"])
	require.True(t, seen["database.field-comment"])
}

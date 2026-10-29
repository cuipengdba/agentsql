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

func TestOriginalDatabaseTableStatusControlsSemanticSchemaAndPreview(t *testing.T) {
	ctx := context.Background()
	service, principal, gateway, _, token := serviceFixture(t)
	user, revision, err := service.Store.User(ctx, principal.User.ID)
	require.NoError(t, err)
	user.Admin = true
	require.NoError(t, service.Store.put(ctx, userKey(user.ID), user, revision))
	principal, err = service.Identity.Verify(ctx, token)
	require.NoError(t, err)
	gateway.columns = []Column{{Schema: "public", Table: "sales", Name: "amount", DataType: "numeric"}}
	handler, err := NewHandler(service, "test")
	require.NoError(t, err)
	request := func(path, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		r.Header.Set("X-SQLBOT-TOKEN", "Bearer "+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	tableID := browserTableID("10", "public", "sales")
	edit := func(checked bool, name string) string {
		t.Helper()
		body, err := json.Marshal(map[string]any{"id": tableID, "ds_id": 10,
			"table_name": name, "table_comment": "", "custom_comment": "",
			"checked": checked, "embedding": nil})
		require.NoError(t, err)
		return string(body)
	}
	require.Equal(t, http.StatusOK, request("/api/v1/datasource/editTable", edit(false, "sales")).Code)
	current, err := service.binding(ctx, principal, "10")
	require.NoError(t, err)
	disabled, err := disabledTables(current)
	require.NoError(t, err)
	require.True(t, disabled[disabledTableKey("public", "sales")])
	list := request("/api/v1/datasource/tableList/10", "")
	require.Equal(t, http.StatusOK, list.Code)
	require.Contains(t, list.Body.String(), `"checked":false`)
	semantic, err := service.semanticSchema(ctx, principal, current)
	require.NoError(t, err)
	require.Empty(t, semantic)
	before := gateway.queries
	preview := request("/api/v1/datasource/previewData/10", `{"table":{"id":1,"table_name":"sales"}}`)
	require.NotEqual(t, http.StatusOK, preview.Code)
	require.Equal(t, before, gateway.queries)
	require.NotEqual(t, http.StatusOK, request("/api/v1/datasource/editTable", edit(true, "forged")).Code)
	_, err = service.Store.db.ExecContext(ctx, `CREATE TRIGGER reject_table_status_audit BEFORE INSERT ON chatbi_objects
		WHEN NEW.kind='management-audit' BEGIN SELECT RAISE(FAIL, 'audit offline'); END`)
	require.NoError(t, err)
	require.NotEqual(t, http.StatusOK, request("/api/v1/datasource/editTable", edit(true, "sales")).Code)
	current, err = service.binding(ctx, principal, "10")
	require.NoError(t, err)
	disabled, err = disabledTables(current)
	require.NoError(t, err)
	require.True(t, disabled[disabledTableKey("public", "sales")])
	_, err = service.Store.db.ExecContext(ctx, `DROP TRIGGER reject_table_status_audit`)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, request("/api/v1/datasource/editTable", edit(true, "sales")).Code)
	current, err = service.binding(ctx, principal, "10")
	require.NoError(t, err)
	disabled, err = disabledTables(current)
	require.NoError(t, err)
	require.Empty(t, disabled)
	list = request("/api/v1/datasource/tableList/10", "")
	require.Equal(t, http.StatusOK, list.Code)
	require.NotContains(t, list.Body.String(), `"checked":false`)
	preview = request("/api/v1/datasource/previewData/10", `{"table":{"id":`+strconv.FormatInt(tableID, 10)+`,"table_name":"sales"}}`)
	require.Equal(t, http.StatusOK, preview.Code)
	require.Equal(t, before+1, gateway.queries)
	audits, err := service.Store.list(ctx, "management-audit", "system", "system")
	require.NoError(t, err)
	states := map[bool]bool{}
	for _, raw := range audits {
		var event struct {
			Action  string `json:"action"`
			Checked *bool  `json:"checked"`
		}
		require.NoError(t, json.Unmarshal(raw, &event))
		if event.Action == "database.table-status" {
			require.NotNil(t, event.Checked)
			states[*event.Checked] = true
		}
	}
	require.True(t, states[false])
	require.True(t, states[true])
}

func TestDisabledTableMetadataRejectsMalformedStoredState(t *testing.T) {
	for _, encoded := range []string{`{"public\u0000sales":false}`, `{"bad":true}`, `[]`, `null`} {
		_, err := disabledTables(Binding{DisabledTables: encoded})
		require.Error(t, err)
	}
}

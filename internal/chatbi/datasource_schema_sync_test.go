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

func TestOriginalDatabaseSyncFieldsRequiresCurrentVisibleTableAndAudit(t *testing.T) {
	ctx := context.Background()
	service, principal, base, _, token := serviceFixture(t)
	user, revision, err := service.Store.User(ctx, principal.User.ID)
	require.NoError(t, err)
	user.Admin = true
	require.NoError(t, service.Store.put(ctx, userKey(user.ID), user, revision))
	principal, err = service.Identity.Verify(ctx, token)
	require.NoError(t, err)
	gateway := &selectionTestGateway{fakeGateway: base, columns: []Column{
		{Schema: "public", Table: "sales", Name: "amount", DataType: "numeric"},
		{Schema: "public", Table: "sales", Name: "region", DataType: "text"},
	}}
	service.Gateway = gateway
	handler, err := NewHandler(service, "test")
	require.NoError(t, err)
	request := func(path, bearer string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(""))
		if bearer != "" {
			r.Header.Set("X-SQLBOT-TOKEN", "Bearer "+bearer)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	path := "/api/v1/datasource/syncFields/10/" + strconv.FormatInt(browserTableID("10", "public", "sales"), 10)
	require.Equal(t, http.StatusUnauthorized, request(path, "").Code)
	require.Equal(t, http.StatusUnprocessableEntity, request("/api/v1/datasource/syncFields/10/01", token).Code)
	require.Equal(t, http.StatusOK, request(path, token).Code)
	require.Greater(t, gateway.schemaCalls, 0)
	audits, err := service.Store.list(ctx, "management-audit", "system", "system")
	require.NoError(t, err)
	found := false
	for _, raw := range audits {
		var event struct {
			Action string `json:"action"`
			Table  string `json:"table"`
			Fields int    `json:"fields"`
		}
		require.NoError(t, json.Unmarshal(raw, &event))
		if event.Action == "datasource.schema-refresh" {
			found = true
			require.Equal(t, "sales", event.Table)
			require.Equal(t, 2, event.Fields)
		}
	}
	require.True(t, found)
	_, err = service.Store.db.ExecContext(ctx, `CREATE TRIGGER reject_schema_refresh_audit BEFORE INSERT ON chatbi_objects
		WHEN NEW.kind='management-audit' BEGIN SELECT RAISE(FAIL, 'audit offline'); END`)
	require.NoError(t, err)
	require.NotEqual(t, http.StatusOK, request(path, token).Code)
	_, err = service.Store.db.ExecContext(ctx, `DROP TRIGGER reject_schema_refresh_audit`)
	require.NoError(t, err)
	gateway.columns = []Column{{Schema: "public", Table: "people", Name: "id", DataType: "integer"}}
	require.NotEqual(t, http.StatusOK, request(path, token).Code)
}

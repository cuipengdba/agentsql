package adminapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/cuipengdba/agentsql/internal/rbac"
	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/stretchr/testify/require"
)

func sqlbotHTTPBinding(t *testing.T, f *adminFixture) store.SQLBotBinding {
	t.Helper()
	a := f.agent
	a.Level = "readonly"
	_, err := f.store.Agents().Update(context.Background(), a)
	require.NoError(t, err)
	return store.SQLBotBinding{WorkspaceID: "1", DatasourceName: "Sales", SQLBotUserID: "1", AgentSQLUserID: "user_bootstrap_admin", AgentID: a.ID, DatasourceID: f.datasource.ID, SchemaName: "public", Enabled: true}
}

func TestSQLBotBindingManagementPermissionAndTenantIsolation(t *testing.T) {
	f := newAdminFixture(t)
	input := sqlbotHTTPBinding(t, f)
	encoded, err := json.Marshal(input)
	require.NoError(t, err)
	for _, auth := range []string{"", "Bearer " + f.agentKey} {
		status, _ := f.request(http.MethodPost, "/api/v1/integrations/sqlbot/bindings", auth, string(encoded))
		require.Equal(t, http.StatusUnauthorized, status)
	}
	limited := taskGrantTestToken(t, f, rbac.DefaultTenantID, "sqlbot-strategy", []string{rbac.PermissionStrategyManage})
	status, _ := f.request(http.MethodPost, "/api/v1/integrations/sqlbot/bindings", limited, string(encoded))
	require.Equal(t, http.StatusForbidden, status)
	status, body := f.request(http.MethodPost, "/api/v1/integrations/sqlbot/bindings", f.adminToken, string(encoded))
	require.Equal(t, http.StatusOK, status, body)
	var created struct {
		Data struct {
			Binding    store.SQLBotBinding `json:"binding"`
			Credential string              `json:"connector_token"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &created))
	require.Len(t, created.Data.Credential, 43)
	b := created.Data.Binding
	path := "/api/v1/integrations/sqlbot/bindings/" + b.ID
	status, body = f.request(http.MethodGet, path, f.adminToken, "")
	require.Equal(t, http.StatusOK, status, body)
	require.NotContains(t, body, created.Data.Credential)
	require.NotContains(t, body, "connector_token_hash")
	other := taskGrantTestToken(t, f, "sqlbot-other-tenant", "sqlbot-other", []string{rbac.PermissionStrategyManage, rbac.PermissionUserManage, rbac.PermissionDatasourceManage})
	status, body = f.request(http.MethodGet, path, other, "")
	require.Equal(t, http.StatusNotFound, status, body)
	status, body = f.request(http.MethodGet, "/api/v1/integrations/sqlbot/bindings", other, "")
	require.Equal(t, http.StatusOK, status, body)
	require.NotContains(t, body, b.ID)
	status, body = f.request(http.MethodPost, "/api/v1/integrations/sqlbot/bindings", other, string(encoded))
	require.Equal(t, http.StatusUnprocessableEntity, status, body)
	status, body = f.request(http.MethodPost, path+"/rotate", limited, `{"revision":1}`)
	require.Equal(t, http.StatusForbidden, status, body)
	status, body = f.request(http.MethodPost, path+"/rotate", f.adminToken, `{"revision":1}`)
	require.Equal(t, http.StatusOK, status, body)
	require.NotContains(t, body, created.Data.Credential)
	status, _ = f.request(http.MethodPost, path+"/rotate", f.adminToken, `{"revision":1}`)
	require.Equal(t, http.StatusPreconditionFailed, status)
	status, body = f.request(http.MethodPost, path+"/rotate", "Bearer "+created.Data.Credential, `{"revision":2}`)
	require.Equal(t, http.StatusUnauthorized, status, body)
	require.NotContains(t, f.logs.String(), created.Data.Credential)
}

func TestSQLBotBindingExportAndDiagnosticsDoNotGrantOrExecute(t *testing.T) {
	f := newAdminFixture(t)
	input := sqlbotHTTPBinding(t, f)
	// SQLBot Snowflake identifiers exceed JavaScript's exact integer range.
	input.WorkspaceID = "7518977644151443456"
	input.SQLBotUserID = "7518977644151443457"
	encoded, _ := json.Marshal(input)
	status, body := f.request(http.MethodPost, "/api/v1/integrations/sqlbot/bindings", f.adminToken, string(encoded))
	require.Equal(t, http.StatusOK, status, body)
	var created struct {
		Data struct {
			Binding    store.SQLBotBinding `json:"binding"`
			Credential string              `json:"connector_token"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &created))
	require.Equal(t, input.WorkspaceID, created.Data.Binding.WorkspaceID)
	require.Contains(t, body, `"workspace_id":"7518977644151443456"`)
	status, body = f.request(http.MethodGet, "/api/v1/integrations/sqlbot/bindings/"+created.Data.Binding.ID+"/diagnostics", f.adminToken, "")
	require.Equal(t, http.StatusOK, status, body)
	require.Contains(t, body, `"configuration_ready":true`)
	require.Contains(t, body, `"live_query_verified":false`)
	require.Zero(t, f.pinger.calls)
	status, body = f.request(http.MethodPost, "/api/v1/integrations/sqlbot/export", f.adminToken, `{"endpoint":"https://gateway.example/mcp"}`)
	require.Equal(t, http.StatusOK, status, body)
	require.Contains(t, body, `"version":2`)
	require.Contains(t, body, `"workspace_id":"7518977644151443456"`)
	require.Contains(t, body, `"sqlbot_user_id":"7518977644151443457"`)
	require.NotContains(t, body, created.Data.Credential)
	require.NotContains(t, body, f.agentKey)
	require.NotContains(t, body, "connector_token_hash")
	for _, endpoint := range []string{"http://public.example/mcp", "https://a:b@gateway.example/mcp", "https://gateway.example/mcp?token=secret", "https://gateway.example/other", "https://gateway.example/mcp#x"} {
		data, _ := json.Marshal(map[string]string{"endpoint": endpoint})
		status, _ = f.request(http.MethodPost, "/api/v1/integrations/sqlbot/export", f.adminToken, string(data))
		require.Equal(t, http.StatusBadRequest, status)
	}
	b := created.Data.Binding
	b.Enabled = false
	encoded, _ = json.Marshal(b)
	status, body = f.request(http.MethodPut, "/api/v1/integrations/sqlbot/bindings/"+b.ID, f.adminToken, string(encoded))
	require.Equal(t, http.StatusOK, status, body)
	status, body = f.request(http.MethodPost, "/api/v1/integrations/sqlbot/export", f.adminToken, `{"endpoint":"http://127.0.0.1/mcp"}`)
	require.Equal(t, http.StatusOK, status, body)
	require.Contains(t, body, `"bindings":[]`)
}

func TestSQLBotBindingExportRejectsCredentialEnvironmentCollisions(t *testing.T) {
	f := newAdminFixture(t)
	input := sqlbotHTTPBinding(t, f)
	for _, userID := range []string{"1", "2"} {
		input.SQLBotUserID = userID
		encoded, err := json.Marshal(input)
		require.NoError(t, err)
		status, body := f.request(http.MethodPost, "/api/v1/integrations/sqlbot/bindings", f.adminToken, string(encoded))
		require.Equal(t, http.StatusOK, status, body)
	}
	status, body := f.request(http.MethodPost, "/api/v1/integrations/sqlbot/export", f.adminToken, `{"endpoint":"https://gateway.example/mcp"}`)
	require.Equal(t, http.StatusConflict, status, body)
}

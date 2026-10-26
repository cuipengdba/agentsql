package adminapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/rbac"
	"github.com/stretchr/testify/require"
)

func TestManagedFileEndpointRequiresBothCredentialsAndExplicitProvisioner(t *testing.T) {
	f, binding, _, verifier := sqlbotIdentityFixture(t)
	request := func(path, agentKey, userToken string) int {
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
		if agentKey != "" {
			r.Header.Set("Authorization", "Bearer "+agentKey)
		}
		if userToken != "" {
			r.Header.Set("X-AgentSQL-User-Authorization", "Bearer "+userToken)
		}
		response := httptest.NewRecorder()
		f.handler.ServeHTTP(response, r)
		return response.Code
	}
	for _, path := range []string{"/api/v1/integrations/sqlbot/managed-files/import", "/api/v1/integrations/sqlbot/managed-files/delete"} {
		require.Equal(t, http.StatusUnauthorized, request(path, "", ""))
		require.Equal(t, http.StatusUnauthorized, request(path, f.agentKey, ""))
		require.Equal(t, http.StatusUnauthorized, request(path, "invalid", "SQB1.fake"))
	}
	token, _, err := verifier.issue(binding, time.Now().UTC())
	require.NoError(t, err)
	for _, path := range []string{"/api/v1/integrations/sqlbot/managed-files/import", "/api/v1/integrations/sqlbot/managed-files/delete"} {
		require.Equal(t, http.StatusForbidden, request(path, f.agentKey, token), "ordinary schema cannot invoke managed files")
	}
	binding.SchemaName = "agentsql_files"
	updated, err := f.store.SQLBotBindings().Update(context.Background(), binding, rbac.BootstrapUserID)
	require.NoError(t, err)
	token, _, err = verifier.issue(updated, time.Now().UTC())
	require.NoError(t, err)
	for _, path := range []string{"/api/v1/integrations/sqlbot/managed-files/import", "/api/v1/integrations/sqlbot/managed-files/delete"} {
		require.Equal(t, http.StatusServiceUnavailable, request(path, f.agentKey, token), "cloud/demo without provisioner remains closed")
	}
}

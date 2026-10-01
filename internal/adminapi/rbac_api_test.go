package adminapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRBACAPIAllowsAssignedPermissionAndDeniesByDefault(t *testing.T) {
	fixture := newAdminFixture(t)
	status, body := fixture.request(http.MethodPost, "/api/v1/roles", fixture.adminToken,
		`{"name":"audit-only","permissions":["audit.view"]}`)
	require.Equal(t, http.StatusOK, status, body)
	roleID := responseString(t, body, "id")

	status, body = fixture.request(http.MethodPost, "/api/v1/users", fixture.adminToken,
		`{"username":"auditor-user","display_name":"Auditor","password":"correct horse battery staple","role_ids":["`+roleID+`"]}`)
	require.Equal(t, http.StatusOK, status, body)
	require.NotContains(t, body, "password_hash")

	status, body = fixture.request(http.MethodPost, "/api/v1/auth/login", "",
		`{"username":"auditor-user","password":"correct horse battery staple"}`)
	require.Equal(t, http.StatusOK, status, body)
	token := "Bearer " + responseString(t, body, "token")
	status, body = fixture.request(http.MethodGet, "/api/v1/audit", token, "")
	require.Equal(t, http.StatusOK, status, body)
	status, body = fixture.request(http.MethodGet, "/api/v1/agents", token, "")
	require.Equal(t, http.StatusForbidden, status, body)
}

func TestRBACAPICrossTenantAndLegacyResourceAccessAreDenied(t *testing.T) {
	fixture := newAdminFixture(t)
	status, body := fixture.request(http.MethodPost, "/api/v1/tenants", fixture.adminToken,
		`{"id":"tenant_b","name":"Tenant B"}`)
	require.Equal(t, http.StatusOK, status, body)
	status, body = fixture.request(http.MethodPost, "/api/v1/roles", fixture.adminToken,
		`{"tenant_id":"tenant_b","name":"tenant-admin","permissions":["user.manage","role.manage","audit.view"]}`)
	require.Equal(t, http.StatusOK, status, body)
	roleID := responseString(t, body, "id")
	status, body = fixture.request(http.MethodPost, "/api/v1/users", fixture.adminToken,
		`{"tenant_id":"tenant_b","username":"bob","display_name":"Bob","password":"a different strong password","role_ids":["`+roleID+`"]}`)
	require.Equal(t, http.StatusOK, status, body)
	status, body = fixture.request(http.MethodPost, "/api/v1/auth/login", "",
		`{"tenant_id":"tenant_b","username":"bob","password":"a different strong password"}`)
	require.Equal(t, http.StatusOK, status, body)
	token := "Bearer " + responseString(t, body, "token")

	status, body = fixture.request(http.MethodGet, "/api/v1/users?tenant_id=tenant_default", token, "")
	require.Equal(t, http.StatusForbidden, status, body)
	status, body = fixture.request(http.MethodGet, "/api/v1/audit", token, "")
	require.Equal(t, http.StatusForbidden, status, body)
}

func responseString(t *testing.T, body, key string) string {
	t.Helper()
	var response struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &response))
	value, ok := response.Data[key].(string)
	require.True(t, ok, "response field %s is not a string: %s", key, body)
	require.NotEmpty(t, value)
	return value
}

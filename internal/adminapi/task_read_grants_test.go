package adminapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/auth"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/rbac"
	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/stretchr/testify/require"
)

func TestTaskReadGrantAdminLifecycleAndSQLBoundary(t *testing.T) {
	fixture := newAdminFixture(t)
	sqlText := "   select id FROM public.customers LIMIT 1"
	body := func(sql string, expiry time.Time) string {
		encoded, err := json.Marshal(map[string]any{
			"agent_id": fixture.agent.ID, "datasource_id": fixture.datasource.ID,
			"sql": sql, "expires_at": expiry.UTC().Format(time.RFC3339Nano),
		})
		require.NoError(t, err)
		return string(encoded)
	}
	for _, invalid := range []string{
		"DELETE FROM public.customers WHERE id = 1",
		"SELECT 1; DELETE FROM public.customers WHERE id = 1",
		"WITH x AS (DELETE FROM public.customers RETURNING id) SELECT id FROM x",
		"WiTh x AS (DELETE FROM public.customers RETURNING id) SELECT id FROM x",
		"SELECT id INTO copied_customers FROM public.customers",
		"SELECT id FROM public.customers FOR UPDATE",
		"SELECT id FROM public.customers FOR /* evasion */ UPDATE",
		"SELECT nextval('customer_seq')",
		"SELECT public.side_effect()",
		"SELECT id FROM public.customers WHERE id IN (SELECT id FROM public.customers)",
		"SELECT id FROM /* inline */ public.customers",
		"/* prefix */ SELECT id FROM public.customers",
	} {
		status, _ := fixture.request(http.MethodPost, "/api/v1/task-read-grants", fixture.adminToken,
			body(invalid, time.Now().Add(time.Hour)))
		require.Equal(t, http.StatusUnprocessableEntity, status)
	}
	status, _ := fixture.request(http.MethodPost, "/api/v1/task-read-grants", fixture.adminToken,
		body(sqlText, time.Now().Add(25*time.Hour)))
	require.Equal(t, http.StatusUnprocessableEntity, status)
	status, _ = fixture.request(http.MethodPost, "/api/v1/task-read-grants", fixture.adminToken,
		`{"agent_id":"agent-1","datasource_id":"ds-1","sql":"SELECT 1","issuer_user_id":"spoof"}`)
	require.Equal(t, http.StatusBadRequest, status)

	status, raw := fixture.request(http.MethodPost, "/api/v1/task-read-grants", fixture.adminToken,
		body(sqlText, time.Now().Add(time.Hour)))
	require.Equal(t, http.StatusOK, status, raw)
	var created struct {
		Data struct {
			ID           string `json:"id"`
			IssuerUserID string `json:"issuer_user_id"`
			APIKey       string `json:"api_key"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(raw), &created))
	require.NotEmpty(t, created.Data.ID)
	require.NotEmpty(t, created.Data.IssuerUserID)
	require.True(t, strings.HasPrefix(created.Data.APIKey, "asql_"))
	require.NotContains(t, raw, sqlText)

	agent, err := auth.NewAuthenticator(fixture.store.Agents()).Authenticate(
		t.Context(), created.Data.APIKey)
	require.NoError(t, err)
	require.Equal(t, created.Data.ID, agent.TaskGrantID)
	require.Equal(t, "readonly", agent.Level)

	status, raw = fixture.request(http.MethodGet, fmt.Sprintf("/api/v1/task-read-grants/%s", created.Data.ID),
		fixture.adminToken, "")
	require.Equal(t, http.StatusOK, status)
	require.NotContains(t, raw, created.Data.APIKey)
	require.NotContains(t, raw, sqlText)
	status, raw = fixture.request(http.MethodGet, "/api/v1/task-read-grants", fixture.adminToken, "")
	require.Equal(t, http.StatusOK, status)
	require.NotContains(t, raw, created.Data.APIKey)

	status, raw = fixture.request(http.MethodPost,
		fmt.Sprintf("/api/v1/task-read-grants/%s/revoke", created.Data.ID), fixture.adminToken, "")
	require.Equal(t, http.StatusOK, status, raw)
	require.Contains(t, raw, "revoked_at")
	_, err = auth.NewAuthenticator(fixture.store.Agents()).Authenticate(t.Context(), created.Data.APIKey)
	require.ErrorIs(t, err, auth.ErrInvalidCredentials)
	status, _ = fixture.request(http.MethodGet, "/api/v1/task-read-grants", "", "")
	require.Equal(t, http.StatusUnauthorized, status)
}

func taskGrantTestToken(t *testing.T, fixture *adminFixture, tenantID, name string, permissions []string) string {
	t.Helper()
	ctx := context.Background()
	if tenantID != rbac.DefaultTenantID {
		_, err := fixture.store.RBAC().CreateTenant(ctx, model.Tenant{ID: tenantID, Name: tenantID, Status: "active"})
		require.NoError(t, err)
	}
	userID, roleID := "user-"+name, "role-"+name
	_, err := fixture.store.RBAC().CreateUser(ctx, model.User{
		ID: userID, TenantID: tenantID, Username: name, DisplayName: name,
		PasswordHash: "opaque-test-hash", Status: "active",
	})
	require.NoError(t, err)
	_, err = fixture.store.RBAC().CreateRole(ctx, model.Role{
		ID: roleID, TenantID: tenantID, Name: name, Permissions: permissions,
	})
	require.NoError(t, err)
	require.NoError(t, fixture.store.RBAC().SetUserRoles(ctx, tenantID, userID, []string{roleID}))
	token, _, err := issuePrincipalToken(DeriveTokenKey([]byte(adminTestSecret)), time.Now(),
		"jti-"+name, rbac.Principal{UserID: userID, TenantID: tenantID, Username: name})
	require.NoError(t, err)
	return "Bearer " + token
}

func TestTaskReadGrantRequiresBothPermissionsAndHidesCrossTenantRecords(t *testing.T) {
	fixture := newAdminFixture(t)
	requestBody := `{"agent_id":"agent-1","datasource_id":"ds-1","sql":"SELECT id FROM public.customers LIMIT 1","expires_at":"` +
		time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano) + `"}`
	for _, test := range []struct {
		name        string
		permissions []string
	}{
		{"strategy-only", []string{rbac.PermissionStrategyManage}},
		{"query-only", []string{rbac.PermissionQueryExecute}},
	} {
		token := taskGrantTestToken(t, fixture, rbac.DefaultTenantID, test.name, test.permissions)
		status, _ := fixture.request(http.MethodPost, "/api/v1/task-read-grants", token, requestBody)
		require.Equal(t, http.StatusForbidden, status, test.name)
	}
	both := taskGrantTestToken(t, fixture, rbac.DefaultTenantID, "both",
		[]string{rbac.PermissionStrategyManage, rbac.PermissionQueryExecute})
	status, raw := fixture.request(http.MethodPost, "/api/v1/task-read-grants", both, requestBody)
	require.Equal(t, http.StatusOK, status, raw)
	var created struct {
		Data struct {
			ID           string `json:"id"`
			IssuerUserID string `json:"issuer_user_id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(raw), &created))
	require.Equal(t, "user-both", created.Data.IssuerUserID)
	other := taskGrantTestToken(t, fixture, "tenant-task-other", "other",
		[]string{rbac.PermissionStrategyManage, rbac.PermissionQueryExecute})
	status, _ = fixture.request(http.MethodGet,
		"/api/v1/task-read-grants/"+created.Data.ID, other, "")
	require.Equal(t, http.StatusNotFound, status)
	status, raw = fixture.request(http.MethodGet, "/api/v1/task-read-grants", other, "")
	require.Equal(t, http.StatusOK, status)
	require.NotContains(t, raw, created.Data.ID)
	status, _ = fixture.request(http.MethodPost, "/api/v1/task-read-grants", other, requestBody)
	require.Equal(t, http.StatusUnprocessableEntity, status)
}

func TestTaskReadGrantRevocationAuditRecordsActualOperator(t *testing.T) {
	fixture := newAdminFixture(t)
	permissions := []string{rbac.PermissionStrategyManage, rbac.PermissionQueryExecute}
	issuerToken := taskGrantTestToken(t, fixture, rbac.DefaultTenantID, "grant-issuer", permissions)
	revokerToken := taskGrantTestToken(t, fixture, rbac.DefaultTenantID, "grant-revoker", permissions)
	sqlText := "SELECT id FROM public.customers WHERE id = 708140123 LIMIT 1"
	body := fmt.Sprintf(`{"agent_id":"%s","datasource_id":"%s","sql":"%s","expires_at":"%s"}`,
		fixture.agent.ID, fixture.datasource.ID, sqlText, time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano))
	status, raw := fixture.request(http.MethodPost, "/api/v1/task-read-grants", issuerToken, body)
	require.Equal(t, http.StatusOK, status, raw)
	var created struct {
		Data struct {
			ID     string `json:"id"`
			APIKey string `json:"api_key"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(raw), &created))
	require.NotEmpty(t, created.Data.ID)
	require.NotEmpty(t, created.Data.APIKey)

	status, raw = fixture.request(http.MethodPost,
		"/api/v1/task-read-grants/"+created.Data.ID+"/revoke", revokerToken, "")
	require.Equal(t, http.StatusOK, status, raw)

	for _, check := range []struct {
		action string
		actor  string
	}{
		{action: "task_read_grant_create", actor: "user-grant-issuer"},
		{action: "task_read_grant_revoke", actor: "user-grant-revoker"},
	} {
		page, err := fixture.store.AuditLogs().FilteredPage(t.Context(),
			model.AuditFilter{Action: &check.action}, 1, 10)
		require.NoError(t, err)
		require.Len(t, page.List, 1)
		entry := page.List[0]
		require.Equal(t, "user", *entry.ActorType)
		require.Equal(t, check.actor, *entry.ActorID)
		require.Nil(t, entry.SQLRaw)
		require.NotNil(t, entry.DetailsJSON)
		var details struct {
			GrantID      string `json:"grant_id"`
			IssuerUserID string `json:"issuer_user_id"`
		}
		require.NoError(t, json.Unmarshal([]byte(*entry.DetailsJSON), &details))
		require.Equal(t, created.Data.ID, details.GrantID)
		require.Equal(t, "user-grant-issuer", details.IssuerUserID)
		encoded, err := json.Marshal(entry)
		require.NoError(t, err)
		require.NotContains(t, string(encoded), created.Data.APIKey)
		require.NotContains(t, string(encoded), sqlText)
	}
	require.NotContains(t, fixture.logs.String(), created.Data.APIKey)
	require.NotContains(t, fixture.logs.String(), sqlText)
}

type failingTaskGrantRecorder struct{}

func (failingTaskGrantRecorder) Record(context.Context, model.AuditLog) (model.AuditLog, error) {
	return model.AuditLog{}, errors.New("injected audit failure")
}

type cancelingTaskGrantRecorder struct{ cancel context.CancelFunc }

func (recorder cancelingTaskGrantRecorder) Record(context.Context, model.AuditLog) (model.AuditLog, error) {
	recorder.cancel()
	return model.AuditLog{}, errors.New("injected audit failure after disconnect")
}

func TestTaskReadGrantAuditFailureNeverReturnsUsableKey(t *testing.T) {
	fixture := newAdminFixture(t)
	fixture.runtime.ManagementAudit = failingTaskGrantRecorder{}
	body := fmt.Sprintf(`{"agent_id":"%s","datasource_id":"%s","sql":"SELECT id FROM public.customers LIMIT 1","expires_at":"%s"}`,
		fixture.agent.ID, fixture.datasource.ID, time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano))
	status, raw := fixture.request(http.MethodPost, "/api/v1/task-read-grants", fixture.adminToken, body)
	require.Equal(t, http.StatusInternalServerError, status)
	require.NotContains(t, raw, "asql_")
	grants, err := fixture.store.TaskReadGrants().List(context.Background())
	require.NoError(t, err)
	require.Len(t, grants, 1)
	require.NotNil(t, grants[0].RevokedAt)
	_, err = fixture.store.Agents().GetByAPIKeyHash(context.Background(), grants[0].KeyHash)
	require.ErrorIs(t, err, store.ErrAgentNotFound)
}

func TestTaskReadGrantAuditFailureRevokesAfterRequestCanceled(t *testing.T) {
	fixture := newAdminFixture(t)
	requestContext, cancel := context.WithCancel(context.Background())
	defer cancel()
	fixture.runtime.ManagementAudit = cancelingTaskGrantRecorder{cancel: cancel}
	body := fmt.Sprintf(`{"agent_id":"%s","datasource_id":"%s","sql":"SELECT id FROM public.customers LIMIT 1","expires_at":"%s"}`,
		fixture.agent.ID, fixture.datasource.ID, time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano))
	request := httptest.NewRequest(http.MethodPost, "/api/v1/task-read-grants", strings.NewReader(body)).WithContext(requestContext)
	request.Header.Set("Authorization", fixture.adminToken)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	fixture.handler.ServeHTTP(response, request)
	require.Equal(t, http.StatusInternalServerError, response.Code)
	require.NotContains(t, response.Body.String(), "asql_")
	grants, err := fixture.store.TaskReadGrants().List(context.Background())
	require.NoError(t, err)
	require.Len(t, grants, 1)
	require.NotNil(t, grants[0].RevokedAt)
}

func TestTaskReadGrantRevokeAuditFailureKeepsGrantRevoked(t *testing.T) {
	fixture := newAdminFixture(t)
	body := fmt.Sprintf(`{"agent_id":"%s","datasource_id":"%s","sql":"SELECT id FROM public.customers LIMIT 1","expires_at":"%s"}`,
		fixture.agent.ID, fixture.datasource.ID, time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano))
	status, raw := fixture.request(http.MethodPost, "/api/v1/task-read-grants", fixture.adminToken, body)
	require.Equal(t, http.StatusOK, status, raw)
	var created struct {
		Data struct {
			ID     string `json:"id"`
			APIKey string `json:"api_key"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(raw), &created))
	require.NotEmpty(t, created.Data.ID)
	fixture.runtime.ManagementAudit = failingTaskGrantRecorder{}
	status, raw = fixture.request(http.MethodPost,
		"/api/v1/task-read-grants/"+created.Data.ID+"/revoke", fixture.adminToken, "")
	require.Equal(t, http.StatusInternalServerError, status)
	require.NotContains(t, raw, created.Data.APIKey)
	grant, err := fixture.store.TaskReadGrants().Get(context.Background(), created.Data.ID)
	require.NoError(t, err)
	require.NotNil(t, grant.RevokedAt)
	_, err = auth.NewAuthenticator(fixture.store.Agents()).Authenticate(t.Context(), created.Data.APIKey)
	require.ErrorIs(t, err, auth.ErrInvalidCredentials)
}

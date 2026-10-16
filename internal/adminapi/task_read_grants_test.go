package adminapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/auth"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/rbac"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

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

func taskGrantRequestBody(t *testing.T, fixture *adminFixture, sqlText string, expiresAt time.Time, key string) string {
	t.Helper()
	encoded, err := json.Marshal(map[string]any{
		"agent_id": fixture.agent.ID, "datasource_id": fixture.datasource.ID,
		"sql": sqlText, "expires_at": expiresAt.UTC().Format(time.RFC3339Nano),
		"idempotency_key": key,
	})
	require.NoError(t, err)
	return string(encoded)
}

func submitTaskGrantRequest(t *testing.T, fixture *adminFixture, token, sqlText, key string) string {
	t.Helper()
	status, raw := fixture.request(http.MethodPost, "/api/v1/task-read-grant-requests", token,
		taskGrantRequestBody(t, fixture, sqlText, time.Now().Add(time.Hour), key))
	require.Equal(t, http.StatusOK, status, raw)
	var response struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(raw), &response))
	require.NotEmpty(t, response.Data.ID)
	require.NotContains(t, raw, sqlText)
	return response.Data.ID
}

func approveAndIssueTaskGrant(t *testing.T, fixture *adminFixture, requester, reviewer, sqlText string) (string, string, string) {
	t.Helper()
	requestID := submitTaskGrantRequest(t, fixture, requester, sqlText, uuid.NewString())
	status, raw := fixture.request(http.MethodPost,
		"/api/v1/task-read-grant-requests/"+requestID+"/approve", reviewer, "")
	require.Equal(t, http.StatusOK, status, raw)
	status, raw = fixture.request(http.MethodPost,
		"/api/v1/task-read-grant-requests/"+requestID+"/issue", requester, "")
	require.Equal(t, http.StatusOK, status, raw)
	var response struct {
		Data struct {
			ID     string `json:"id"`
			APIKey string `json:"api_key"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(raw), &response))
	require.NotEmpty(t, response.Data.ID)
	require.Equal(t, requestID, response.Data.ID, "one stable ID must link request, grant, and audit")
	require.True(t, strings.HasPrefix(response.Data.APIKey, "asql_"))
	require.NotContains(t, raw, sqlText)
	return requestID, response.Data.ID, response.Data.APIKey
}

func TestTaskReadGrantApprovalLifecycleAndSQLBoundary(t *testing.T) {
	fixture := newAdminFixture(t)
	reviewer := taskGrantTestToken(t, fixture, rbac.DefaultTenantID, "reviewer-lifecycle",
		[]string{rbac.PermissionStrategyManage, rbac.PermissionQueryExecute})
	sqlText := "   select id FROM public.customers LIMIT 1"
	for _, invalid := range []string{
		"DELETE FROM public.customers WHERE id = 1",
		"SELECT 1; DELETE FROM public.customers WHERE id = 1",
		"WITH x AS (DELETE FROM public.customers RETURNING id) SELECT id FROM x",
		"SELECT id INTO copied_customers FROM public.customers",
		"SELECT id FROM public.customers FOR UPDATE",
		"SELECT id FROM public.customers FOR /* evasion */ UPDATE",
		"SELECT nextval('customer_seq')",
		"SELECT public.side_effect()",
		"SELECT id FROM public.customers WHERE id IN (SELECT id FROM public.customers)",
		"SELECT id FROM /* inline */ public.customers",
		"/* prefix */ SELECT id FROM public.customers",
	} {
		status, _ := fixture.request(http.MethodPost, "/api/v1/task-read-grant-requests", fixture.adminToken,
			taskGrantRequestBody(t, fixture, invalid, time.Now().Add(time.Hour), uuid.NewString()))
		require.Equal(t, http.StatusUnprocessableEntity, status, invalid)
	}
	status, _ := fixture.request(http.MethodPost, "/api/v1/task-read-grant-requests", fixture.adminToken,
		taskGrantRequestBody(t, fixture, sqlText, time.Now().Add(25*time.Hour), uuid.NewString()))
	require.Equal(t, http.StatusUnprocessableEntity, status)
	status, _ = fixture.request(http.MethodPost, "/api/v1/task-read-grants", fixture.adminToken,
		taskGrantRequestBody(t, fixture, sqlText, time.Now().Add(time.Hour), uuid.NewString()))
	require.Equal(t, http.StatusGone, status, "legacy direct issuance must stay disabled")

	requestID := submitTaskGrantRequest(t, fixture, fixture.adminToken, sqlText, uuid.NewString())
	status, raw := fixture.request(http.MethodGet,
		"/api/v1/task-read-grant-requests/"+requestID, fixture.adminToken, "")
	require.Equal(t, http.StatusOK, status)
	require.NotContains(t, raw, sqlText, "requester and list endpoints must not expose SQL")
	status, raw = fixture.request(http.MethodGet,
		"/api/v1/task-read-grant-requests/"+requestID, reviewer, "")
	require.Equal(t, http.StatusOK, status)
	require.Contains(t, raw, sqlText, "a separate reviewer must be able to inspect the exact SQL")
	status, raw = fixture.request(http.MethodGet, "/api/v1/task-read-grant-requests", reviewer, "")
	require.Equal(t, http.StatusOK, status)
	require.NotContains(t, raw, sqlText, "list must not disclose SQL")
	status, _ = fixture.request(http.MethodPost,
		"/api/v1/task-read-grant-requests/"+requestID+"/approve", fixture.adminToken, "")
	require.Equal(t, http.StatusConflict, status, "requester cannot approve own request")
	status, _ = fixture.request(http.MethodPost,
		"/api/v1/task-read-grant-requests/"+requestID+"/issue", fixture.adminToken, "")
	require.Equal(t, http.StatusConflict, status, "unapproved request cannot issue a key")
	status, raw = fixture.request(http.MethodPost,
		"/api/v1/task-read-grant-requests/"+requestID+"/approve", reviewer, "")
	require.Equal(t, http.StatusOK, status, raw)
	status, raw = fixture.request(http.MethodPost,
		"/api/v1/task-read-grant-requests/"+requestID+"/issue", fixture.adminToken, "")
	require.Equal(t, http.StatusOK, status, raw)
	var issued struct {
		Data struct {
			ID     string `json:"id"`
			APIKey string `json:"api_key"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(raw), &issued))
	require.True(t, strings.HasPrefix(issued.Data.APIKey, "asql_"))
	agent, err := auth.NewAuthenticator(fixture.store.Agents()).Authenticate(t.Context(), issued.Data.APIKey)
	require.NoError(t, err)
	require.Equal(t, issued.Data.ID, agent.TaskGrantID)
	require.Equal(t, "readonly", agent.Level)

	status, raw = fixture.request(http.MethodGet, "/api/v1/task-read-grants/"+issued.Data.ID, fixture.adminToken, "")
	require.Equal(t, http.StatusOK, status)
	require.NotContains(t, raw, issued.Data.APIKey)
	require.NotContains(t, raw, sqlText)
	status, raw = fixture.request(http.MethodGet, "/api/v1/task-read-grant-requests/"+requestID, fixture.adminToken, "")
	require.Equal(t, http.StatusOK, status)
	require.NotContains(t, raw, issued.Data.APIKey)
	require.NotContains(t, raw, sqlText)
	status, raw = fixture.request(http.MethodPost, "/api/v1/task-read-grants/"+issued.Data.ID+"/revoke", fixture.adminToken, "")
	require.Equal(t, http.StatusOK, status, raw)
	_, err = auth.NewAuthenticator(fixture.store.Agents()).Authenticate(t.Context(), issued.Data.APIKey)
	require.ErrorIs(t, err, auth.ErrInvalidCredentials)
}

func TestTaskReadGrantIdempotencyTenantIsolationAndSingleIssue(t *testing.T) {
	fixture := newAdminFixture(t)
	requester := taskGrantTestToken(t, fixture, rbac.DefaultTenantID, "requester-only",
		[]string{rbac.PermissionQueryExecute})
	reviewer := taskGrantTestToken(t, fixture, rbac.DefaultTenantID, "reviewer-only",
		[]string{rbac.PermissionStrategyManage, rbac.PermissionQueryExecute})
	other := taskGrantTestToken(t, fixture, "tenant-task-other", "other-task",
		[]string{rbac.PermissionStrategyManage, rbac.PermissionQueryExecute})
	sqlText := "SELECT id FROM public.customers LIMIT 1"
	key := uuid.NewString()
	body := taskGrantRequestBody(t, fixture, sqlText, time.Now().Add(time.Hour), key)
	status, raw := fixture.request(http.MethodPost, "/api/v1/task-read-grant-requests", requester, body)
	require.Equal(t, http.StatusOK, status, raw)
	var created struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(raw), &created))
	status, raw = fixture.request(http.MethodPost, "/api/v1/task-read-grant-requests", requester, body)
	require.Equal(t, http.StatusOK, status, raw)
	require.Contains(t, raw, created.Data.ID)
	status, _ = fixture.request(http.MethodPost, "/api/v1/task-read-grant-requests", requester,
		taskGrantRequestBody(t, fixture, sqlText+" ", time.Now().Add(time.Hour), key))
	require.Equal(t, http.StatusConflict, status)
	status, _ = fixture.request(http.MethodGet, "/api/v1/task-read-grant-requests/"+created.Data.ID, other, "")
	require.Equal(t, http.StatusNotFound, status)
	status, raw = fixture.request(http.MethodGet, "/api/v1/task-read-grant-requests", other, "")
	require.Equal(t, http.StatusOK, status)
	require.NotContains(t, raw, created.Data.ID)
	status, _ = fixture.request(http.MethodPost,
		"/api/v1/task-read-grant-requests/"+created.Data.ID+"/approve", other, "")
	require.Equal(t, http.StatusNotFound, status)
	status, _ = fixture.request(http.MethodPost,
		"/api/v1/task-read-grant-requests/"+created.Data.ID+"/approve", requester, "")
	require.Equal(t, http.StatusForbidden, status)
	status, raw = fixture.request(http.MethodPost,
		"/api/v1/task-read-grant-requests/"+created.Data.ID+"/approve", reviewer, "")
	require.Equal(t, http.StatusOK, status, raw)
	status, _ = fixture.request(http.MethodPost,
		"/api/v1/task-read-grant-requests/"+created.Data.ID+"/issue", reviewer, "")
	require.Equal(t, http.StatusConflict, status)
	status, raw = fixture.request(http.MethodPost,
		"/api/v1/task-read-grant-requests/"+created.Data.ID+"/issue", requester, "")
	require.Equal(t, http.StatusOK, status, raw)
	var issued struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(raw), &issued))
	require.NotEmpty(t, issued.Data.ID)
	status, _ = fixture.request(http.MethodPost,
		"/api/v1/task-read-grant-requests/"+created.Data.ID+"/issue", requester, "")
	require.Equal(t, http.StatusConflict, status)
	status, _ = fixture.request(http.MethodPost, "/api/v1/task-read-grants/"+issued.Data.ID+"/revoke", other, "")
	require.Equal(t, http.StatusNotFound, status)
	status, raw = fixture.request(http.MethodPost, "/api/v1/task-read-grants/"+issued.Data.ID+"/revoke", requester, "")
	require.Equal(t, http.StatusOK, status, raw)
	status, raw = fixture.request(http.MethodGet, "/api/v1/task-read-grant-requests/"+created.Data.ID, requester, "")
	require.Equal(t, http.StatusOK, status, raw)
	require.Contains(t, raw, "grant_revoked_at")
	grants, err := fixture.store.TaskReadGrants().List(context.Background())
	require.NoError(t, err)
	require.Len(t, grants, 1)
}

func TestTaskReadGrantAuditRecordsActorsWithoutSecrets(t *testing.T) {
	fixture := newAdminFixture(t)
	permissions := []string{rbac.PermissionStrategyManage, rbac.PermissionQueryExecute}
	requester := taskGrantTestToken(t, fixture, rbac.DefaultTenantID, "grant-requester", permissions)
	reviewer := taskGrantTestToken(t, fixture, rbac.DefaultTenantID, "grant-reviewer", permissions)
	sqlText := "SELECT id FROM public.customers WHERE id = 708140123 LIMIT 1"
	requestID, grantID, key := approveAndIssueTaskGrant(t, fixture, requester, reviewer, sqlText)
	status, raw := fixture.request(http.MethodPost,
		"/api/v1/task-read-grants/"+grantID+"/revoke", reviewer, "")
	require.Equal(t, http.StatusOK, status, raw)
	for _, check := range []struct{ action, actor string }{
		{"task_read_grant_request", "user-grant-requester"},
		{"task_read_grant_approve", "user-grant-reviewer"},
		{"task_read_grant_issue", "user-grant-requester"},
		{"task_read_grant_revoke", "user-grant-reviewer"},
	} {
		page, err := fixture.store.AuditLogs().FilteredPage(t.Context(),
			model.AuditFilter{Action: &check.action}, 1, 10)
		require.NoError(t, err)
		require.Len(t, page.List, 1, check.action)
		entry := page.List[0]
		require.Equal(t, check.actor, *entry.ActorID)
		require.Nil(t, entry.SQLRaw)
		require.NotNil(t, entry.DetailsJSON)
		encoded, err := json.Marshal(entry)
		require.NoError(t, err)
		require.NotContains(t, string(encoded), key)
		require.NotContains(t, string(encoded), sqlText)
		if check.action != "task_read_grant_revoke" {
			require.Contains(t, *entry.DetailsJSON, requestID)
		}
	}
	linked, err := fixture.store.AuditLogs().FilteredPage(t.Context(),
		model.AuditFilter{TaskGrantID: &grantID}, 1, 10)
	require.NoError(t, err)
	require.Equal(t, int64(4), linked.Total, "grant ID must reach the pre-issuance decisions")
	require.NotContains(t, fixture.logs.String(), key)
	require.NotContains(t, fixture.logs.String(), sqlText)
}

type failingTaskGrantRecorder struct{}

func (failingTaskGrantRecorder) Record(context.Context, model.AuditLog) (model.AuditLog, error) {
	return model.AuditLog{}, errors.New("injected audit failure")
}

func TestTaskReadGrantRevokeAuditFailureKeepsGrantRevoked(t *testing.T) {
	fixture := newAdminFixture(t)
	reviewer := taskGrantTestToken(t, fixture, rbac.DefaultTenantID, "failure-reviewer",
		[]string{rbac.PermissionStrategyManage, rbac.PermissionQueryExecute})
	_, grantID, key := approveAndIssueTaskGrant(t, fixture, fixture.adminToken, reviewer,
		"SELECT id FROM public.customers LIMIT 1")
	fixture.runtime.ManagementAudit = failingTaskGrantRecorder{}
	status, raw := fixture.request(http.MethodPost,
		fmt.Sprintf("/api/v1/task-read-grants/%s/revoke", grantID), fixture.adminToken, "")
	require.Equal(t, http.StatusInternalServerError, status)
	require.NotContains(t, raw, key)
	grant, err := fixture.store.TaskReadGrants().Get(context.Background(), grantID)
	require.NoError(t, err)
	require.NotNil(t, grant.RevokedAt)
	_, err = auth.NewAuthenticator(fixture.store.Agents()).Authenticate(t.Context(), key)
	require.ErrorIs(t, err, auth.ErrInvalidCredentials)
}

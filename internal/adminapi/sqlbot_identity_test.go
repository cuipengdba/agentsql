package adminapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/rbac"
	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/stretchr/testify/require"
)

func sqlbotIdentityFixture(t *testing.T) (*adminFixture, store.SQLBotBinding, string, *SQLBotDelegationVerifier) {
	t.Helper()
	f := newAdminFixture(t)
	f.agent.Level = "readonly"
	_, err := f.store.Agents().Update(context.Background(), f.agent)
	require.NoError(t, err)
	b, credential, err := f.store.SQLBotBindings().Create(context.Background(), store.SQLBotBinding{
		WorkspaceID: "1", DatasourceName: "analytics", SQLBotUserID: "42", AgentSQLUserID: rbac.BootstrapUserID,
		AgentID: f.agent.ID, DatasourceID: f.datasource.ID, SchemaName: "public", Enabled: true,
	}, rbac.BootstrapUserID)
	require.NoError(t, err)
	v := NewSQLBotDelegationVerifier(DeriveTokenKey([]byte(adminTestSecret)), f.store, rbac.NewService(f.store.RBAC()))
	return f, b, credential, v
}

func TestSQLBotResolveAuthenticatesExactMappingAndLiveIdentity(t *testing.T) {
	f, b, credential, v := sqlbotIdentityFixture(t)
	body, _ := json.Marshal(map[string]any{"binding_id": b.ID, "workspace_id": b.WorkspaceID, "datasource_name": b.DatasourceName, "sqlbot_user_id": b.SQLBotUserID})
	status, response := f.request(http.MethodPost, "/api/v1/integrations/sqlbot/resolve", "Bearer "+credential, string(body))
	require.Equal(t, http.StatusOK, status, response)
	var envelope struct {
		Data struct {
			Token   string `json:"user_token"`
			AgentID string `json:"agent_id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(response), &envelope))
	require.NotEmpty(t, envelope.Data.Token)
	require.NotContains(t, response, credential)
	require.NotContains(t, response, f.agentKey)
	principal, current, err := v.VerifySQLBot(context.Background(), envelope.Data.Token, b.AgentID)
	require.NoError(t, err)
	require.Equal(t, b.AgentSQLUserID, principal.UserID)
	require.Equal(t, b.ID, current.ID)
	_, _, err = v.VerifySQLBot(context.Background(), envelope.Data.Token, "other-agent")
	require.ErrorIs(t, err, ErrInvalidAdminToken)
	status, _ = f.request(http.MethodGet, "/api/v1/auth/me", "Bearer "+envelope.Data.Token, "")
	require.Equal(t, http.StatusUnauthorized, status)
	for _, bad := range []string{strings.ReplaceAll(string(body), `"42"`, `"43"`), strings.ReplaceAll(string(body), `"workspace_id":"1"`, `"workspace_id":"2"`), strings.ReplaceAll(string(body), "analytics", "other")} {
		status, _ = f.request(http.MethodPost, "/api/v1/integrations/sqlbot/resolve", "Bearer "+credential, bad)
		require.Equal(t, http.StatusForbidden, status)
	}
	status, _ = f.request(http.MethodPost, "/api/v1/integrations/sqlbot/resolve", f.adminToken, string(body))
	require.Equal(t, http.StatusUnauthorized, status)
	_, err = v.parse(envelope.Data.Token, time.Now().Add(3*time.Minute))
	require.ErrorIs(t, err, ErrInvalidAdminToken)
	_, _, err = v.VerifySQLBot(context.Background(), envelope.Data.Token+"x", b.AgentID)
	require.ErrorIs(t, err, ErrInvalidAdminToken)
	_, newCredential, err := f.store.SQLBotBindings().Rotate(context.Background(), b.ID, b.Revision, rbac.BootstrapUserID)
	require.NoError(t, err)
	require.NotEqual(t, credential, newCredential)
	_, _, err = v.VerifySQLBot(context.Background(), envelope.Data.Token, b.AgentID)
	require.ErrorIs(t, err, ErrInvalidAdminToken, "rotation revokes already issued tokens")
	status, _ = f.request(http.MethodPost, "/api/v1/integrations/sqlbot/resolve", "Bearer "+credential, string(body))
	require.Equal(t, http.StatusUnauthorized, status)
	page, err := f.store.AuditLogs().Page(context.Background(), 1, 100)
	require.NoError(t, err)
	found := false
	for _, a := range page.List {
		if a.Action != nil && *a.Action == "sqlbot_identity_issue" {
			found = true
			require.NotNil(t, a.ActorID)
			require.Equal(t, b.AgentSQLUserID, *a.ActorID)
			require.NotContains(t, *a.DetailsJSON, envelope.Data.Token)
			require.NotContains(t, *a.DetailsJSON, credential)
		}
	}
	require.True(t, found)
}

func TestSQLBotIdentityRevokesOnUserAgentAndBindingChanges(t *testing.T) {
	f, b, _, v := sqlbotIdentityFixture(t)
	token, _, err := v.issue(b, time.Now())
	require.NoError(t, err)
	u, err := f.store.RBAC().GetUser(context.Background(), b.TenantID, b.AgentSQLUserID)
	require.NoError(t, err)
	u.Status = "disabled"
	_, err = f.store.RBAC().UpdateUser(context.Background(), u)
	require.NoError(t, err)
	_, _, err = v.VerifySQLBot(context.Background(), token, b.AgentID)
	require.ErrorIs(t, err, ErrInvalidAdminToken)
	u.Status = "active"
	_, err = f.store.RBAC().UpdateUser(context.Background(), u)
	require.NoError(t, err)
	f.agent.Level = "dml"
	_, err = f.store.Agents().Update(context.Background(), f.agent)
	require.NoError(t, err)
	_, _, err = v.VerifySQLBot(context.Background(), token, b.AgentID)
	require.ErrorIs(t, err, ErrInvalidAdminToken)
	f.agent.Level = "readonly"
	_, err = f.store.Agents().Update(context.Background(), f.agent)
	require.NoError(t, err)
	_, _, err = v.VerifySQLBot(context.Background(), token, b.AgentID)
	require.NoError(t, err)
	b.Enabled = false
	_, err = f.store.SQLBotBindings().Update(context.Background(), b, rbac.BootstrapUserID)
	require.NoError(t, err)
	_, _, err = v.VerifySQLBot(context.Background(), token, b.AgentID)
	require.ErrorIs(t, err, ErrInvalidAdminToken)
}

func TestSQLBotResolveRequiresDurableIssuanceAudit(t *testing.T) {
	f, b, credential, _ := sqlbotIdentityFixture(t)
	f.runtime.ManagementAudit = nil
	body, _ := json.Marshal(map[string]any{"binding_id": b.ID, "workspace_id": b.WorkspaceID, "datasource_name": b.DatasourceName, "sqlbot_user_id": b.SQLBotUserID})
	status, response := f.request(http.MethodPost, "/api/v1/integrations/sqlbot/resolve", "Bearer "+credential, string(body))
	require.Equal(t, http.StatusServiceUnavailable, status)
	require.NotContains(t, response, "SQB1.")
}

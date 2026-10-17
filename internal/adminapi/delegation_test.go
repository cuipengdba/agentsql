package adminapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/rbac"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestHumanDelegationVerifierRequiresLiveAuthorizedSession(t *testing.T) {
	fixture := newAdminFixture(t)
	cfg := adminTestConfig("unused.db")
	cfg.MCP.Delegation.RequireHumanToken = true
	handler, err := NewHandler(Deps{
		Runtime: fixture.runtime, Config: cfg, AdminUsername: "admin", AdminPassword: "password",
		TokenKey: DeriveTokenKey([]byte(adminTestSecret)),
	}, zerolog.Nop())
	require.NoError(t, err)
	fixture.handler = handler
	verifier, err := NewHumanDelegationVerifier(
		DeriveTokenKey([]byte(adminTestSecret)), fixture.store.AdminSessions(), rbac.NewService(fixture.store.RBAC()),
	)
	require.NoError(t, err)
	_, err = verifier.Verify(context.Background(), "forged", "agent-1")
	require.ErrorIs(t, err, ErrInvalidAdminToken)
	// A signed token without a persisted login session is insufficient.
	_, err = verifier.Verify(context.Background(), fixture.adminToken[len("Bearer "):], "agent-1")
	require.ErrorIs(t, err, ErrInvalidAdminToken)

	status, body := fixture.request(http.MethodPost, "/api/v1/auth/login", "", `{"username":"admin","password":"password"}`)
	require.Equal(t, http.StatusOK, status)
	var login struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &login))
	require.NotEmpty(t, login.Data.Token)
	// The ordinary console bearer is deliberately unusable as an MCP human
	// credential; an explicit short-lived, Agent-bound token must be issued.
	_, err = verifier.Verify(context.Background(), login.Data.Token, "agent-1")
	require.ErrorIs(t, err, ErrInvalidAdminToken)
	status, body = fixture.request(http.MethodPost, "/api/v1/auth/delegation-token", "Bearer "+login.Data.Token, `{"agent_id":"agent-1"}`)
	require.Equal(t, http.StatusOK, status)
	var issued struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &issued))
	require.NotEmpty(t, issued.Data.Token)
	_, err = parseDelegationToken(verifier.key, issued.Data.Token, time.Now().Add(31*time.Minute))
	require.ErrorIs(t, err, ErrInvalidAdminToken, "a delegation cannot survive its 30-minute cap")
	page, err := fixture.store.AuditLogs().Page(context.Background(), 1, 100)
	require.NoError(t, err)
	foundIssueAudit := false
	for _, entry := range page.List {
		if entry.Action != nil && *entry.Action == "mcp_human_delegation_issue" {
			foundIssueAudit = true
			require.NotNil(t, entry.DetailsJSON)
			require.NotContains(t, *entry.DetailsJSON, issued.Data.Token)
			require.Equal(t, rbac.BootstrapUserID, *entry.ActorID)
		}
	}
	require.True(t, foundIssueAudit, "issuance must have a durable management audit")
	principal, err := verifier.Verify(context.Background(), issued.Data.Token, "agent-1")
	require.NoError(t, err)
	require.Equal(t, rbac.BootstrapUserID, principal.UserID)
	require.Equal(t, rbac.DefaultTenantID, principal.TenantID)
	require.True(t, principal.Has(rbac.PermissionQueryExecute))
	_, err = verifier.Verify(context.Background(), issued.Data.Token, "other-agent")
	require.ErrorIs(t, err, ErrInvalidAdminToken)
	status, _ = fixture.request(http.MethodGet, "/api/v1/auth/me", "Bearer "+issued.Data.Token, "")
	require.Equal(t, http.StatusUnauthorized, status)
	user, err := fixture.store.RBAC().GetUser(context.Background(), rbac.DefaultTenantID, rbac.BootstrapUserID)
	require.NoError(t, err)
	user.Status = "disabled"
	_, err = fixture.store.RBAC().UpdateUser(context.Background(), user)
	require.NoError(t, err)
	_, err = verifier.Verify(context.Background(), issued.Data.Token, "agent-1")
	require.ErrorIs(t, err, ErrInvalidAdminToken)
	user.Status = "active"
	_, err = fixture.store.RBAC().UpdateUser(context.Background(), user)
	require.NoError(t, err)
	role, err := fixture.store.RBAC().GetRole(context.Background(), rbac.DefaultTenantID, rbac.AdminRoleID)
	require.NoError(t, err)
	permissions := append([]string(nil), role.Permissions...)
	role.Permissions = nil
	for _, permission := range permissions {
		if permission != rbac.PermissionQueryExecute {
			role.Permissions = append(role.Permissions, permission)
		}
	}
	_, err = fixture.store.RBAC().UpdateRole(context.Background(), role)
	require.NoError(t, err)
	_, err = verifier.Verify(context.Background(), issued.Data.Token, "agent-1")
	require.ErrorIs(t, err, ErrInvalidAdminToken)
	role.Permissions = permissions
	_, err = fixture.store.RBAC().UpdateRole(context.Background(), role)
	require.NoError(t, err)
	_, err = verifier.Verify(context.Background(), issued.Data.Token, "agent-1")
	require.NoError(t, err)
	recorder := fixture.runtime.ManagementAudit
	fixture.runtime.ManagementAudit = nil
	status, body = fixture.request(http.MethodPost, "/api/v1/auth/delegation-token", "Bearer "+login.Data.Token, `{"agent_id":"agent-1"}`)
	require.Equal(t, http.StatusServiceUnavailable, status)
	require.NotContains(t, body, `"token"`)
	fixture.runtime.ManagementAudit = recorder

	status, _ = fixture.request(http.MethodPost, "/api/v1/auth/logout", "Bearer "+login.Data.Token, "")
	require.Equal(t, http.StatusOK, status)
	_, err = verifier.Verify(context.Background(), issued.Data.Token, "agent-1")
	require.ErrorIs(t, err, ErrInvalidAdminToken)
}

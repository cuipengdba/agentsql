package mcpserver

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/rbac"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type testHumanVerifier struct {
	principal rbac.Principal
	err       error
	calls     int
}

func (verifier *testHumanVerifier) Verify(_ context.Context, token, agentID string) (rbac.Principal, error) {
	verifier.calls++
	if token != "human-token" || agentID != "agent-1" || verifier.err != nil {
		return rbac.Principal{}, errors.New("invalid human token")
	}
	return verifier.principal, nil
}

func TestHumanDelegationMiddlewareBindsBothIdentities(t *testing.T) {
	verifier := &testHumanVerifier{principal: rbac.Principal{TenantID: "tenant-1", UserID: "user-1"}}
	agent := model.Agent{ID: "agent-1", TenantID: "tenant-1"}
	calls := 0
	next := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls++
		human, ok := delegatedHumanFromContext(request.Context())
		require.True(t, ok)
		require.Equal(t, "user-1", human.userID)
		require.Equal(t, "tenant-1", human.tenantID)
		writer.WriteHeader(http.StatusNoContent)
	})
	serve := func(required bool, header []string) int {
		request := httptest.NewRequest(http.MethodPost, "/mcp", nil)
		request = request.WithContext(context.WithValue(request.Context(), requestIdentityContextKey{}, requestIdentity{agent: agent, plainKey: "agent-key"}))
		for _, value := range header {
			request.Header.Add(humanDelegationHeader, value)
		}
		recorder := httptest.NewRecorder()
		humanDelegationMiddleware(required, verifier, next).ServeHTTP(recorder, request)
		return recorder.Code
	}
	require.Equal(t, http.StatusUnauthorized, serve(true, nil))
	require.Equal(t, http.StatusUnauthorized, serve(true, []string{"Bearer forged"}))
	require.Equal(t, http.StatusUnauthorized, serve(true, []string{"Bearer human-token", "Bearer human-token"}))
	require.Equal(t, http.StatusUnauthorized, serve(false, []string{"Bearer human-token"}))
	require.Zero(t, calls)
	require.Equal(t, http.StatusNoContent, serve(true, []string{"Bearer human-token"}))
	require.Equal(t, 1, calls)

	verifier.principal.TenantID = "other-tenant"
	require.Equal(t, http.StatusUnauthorized, serve(true, []string{"Bearer human-token"}))
	verifier.principal.TenantID = "tenant-1"
	agent.TaskGrantID = "grant-1"
	agent.TaskGrantIssuerUserID = "other-user"
	require.Equal(t, http.StatusUnauthorized, serve(true, []string{"Bearer human-token"}))
	agent.TaskGrantIssuerUserID = "user-1"
	require.Equal(t, http.StatusNoContent, serve(true, []string{"Bearer human-token"}))
	verifier.err = errors.New("revoked")
	require.Equal(t, http.StatusUnauthorized, serve(true, []string{"Bearer human-token"}))
	require.Equal(t, 2, calls)
}

func TestHumanDelegationStatefulSessionCannotChangeUser(t *testing.T) {
	fixture := newMCPFixture(t, "readonly")
	cfg := statefulHTTPTestConfig(1_000)
	cfg.Server.ConsoleEnabled = true
	cfg.MCP.Delegation.RequireHumanToken = true
	verifier := &testHumanVerifier{principal: rbac.Principal{
		TenantID: fixture.agent.TenantID, UserID: "user-1",
	}}
	handler, err := NewHTTPHandler(fixture.runtime, cfg, zerolog.Nop(), WithHumanDelegation(verifier))
	require.NoError(t, err)
	serve := func(body, sessionID string) *httptest.ResponseRecorder {
		request := newMCPRequest(fixture.handlers.apiKey, http.MethodPost, body, sessionID)
		request.Header.Set(humanDelegationHeader, "Bearer human-token")
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder
	}
	initialized := serve(initializeRequest, "")
	require.Equal(t, http.StatusOK, initialized.Code, initialized.Body.String())
	sessionID := initialized.Header().Get(mcpSessionIDHeader)
	require.NotEmpty(t, sessionID)
	require.Equal(t, http.StatusOK, serve(listToolsRequest, sessionID).Code)

	verifier.principal.UserID = "user-2"
	swapped := serve(listToolsRequest, sessionID)
	require.Equal(t, http.StatusForbidden, swapped.Code, swapped.Body.String())
	require.Contains(t, swapped.Body.String(), "session user mismatch")
	require.Equal(t, http.StatusOK, serve(initializeRequest, "").Code, "the new human can create a separate session")
}

func TestDelegatedSessionExposesB5AfterHumanVerification(t *testing.T) {
	fixture := newMCPFixture(t, "dml")
	cfg := statefulHTTPTestConfig(1_000)
	cfg.Server.ConsoleEnabled = true
	cfg.MCP.Delegation.RequireHumanToken = true
	verifier := &testHumanVerifier{principal: rbac.Principal{TenantID: fixture.agent.TenantID, UserID: "user-1"}}
	spy := &b5ServiceSpy{}
	handler, err := NewHTTPHandler(fixture.runtime, cfg, zerolog.Nop(),
		WithHumanDelegation(verifier), WithB5Sessions(B5Options{B5Sessions: true, B5TxPostgres: true, Service: spy}))
	require.NoError(t, err)
	serve := func(body, sessionID string, includeHuman bool) *httptest.ResponseRecorder {
		request := newMCPRequest(fixture.handlers.apiKey, http.MethodPost, body, sessionID)
		if includeHuman {
			request.Header.Set(humanDelegationHeader, "Bearer human-token")
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder
	}
	initialized := serve(initializeRequest, "", true)
	require.Equal(t, http.StatusOK, initialized.Code, initialized.Body.String())
	sessionID := initialized.Header().Get(mcpSessionIDHeader)
	require.NotEmpty(t, sessionID)
	listed := serve(listToolsRequest, sessionID, true)
	require.Contains(t, listed.Body.String(), `"open_session"`)
	opened := serve(toolCall("open_session", map[string]any{}), sessionID, true)
	require.Contains(t, opened.Body.String(), `"session_id":"as-session"`)
	require.Equal(t, []string{"open"}, spy.calls)
	require.Equal(t, []string{"user-1"}, spy.humanUsers)
	unauthorized := serve(toolCall("open_session", map[string]any{}), sessionID, false)
	require.Equal(t, http.StatusUnauthorized, unauthorized.Code)
	require.Equal(t, []string{"open"}, spy.calls)
}

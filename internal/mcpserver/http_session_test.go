package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

const mcpSessionIDHeader = "Mcp-Session-Id"

func TestStreamableHTTPStatefulSessionLifecycleAndTenantBinding(t *testing.T) {
	fixture := newMCPFixture(t, "dml")
	handler, _, err := newHTTPHandlerWithRegistry(fixture.runtime, statefulHTTPTestConfig(1_000), zerolog.Nop())
	require.NoError(t, err)

	initialized := serveMCPWithSession(t, handler, fixture.handlers.apiKey, http.MethodPost, initializeRequest, "")
	require.Equal(t, http.StatusOK, initialized.Code, initialized.Body.String())
	sessionID := initialized.Header().Get(mcpSessionIDHeader)
	require.NotEmpty(t, sessionID, "initialize must return a transport session ID by default")
	t.Logf("initialize response: status=%d Content-Type=%q Mcp-Session-Id=%q", initialized.Code, initialized.Header().Get("Content-Type"), sessionID)

	listed := serveMCPWithSession(t, handler, fixture.handlers.apiKey, http.MethodPost, listToolsRequest, sessionID)
	require.Equal(t, http.StatusOK, listed.Code, listed.Body.String())
	toolNames := decodeToolNames(t, listed.Body.Bytes())
	require.Len(t, toolNames, 7, "the current non-B5 server surface has seven tools")
	t.Logf("tools/list request: Mcp-Session-Id=%q; response: status=%d tools=%d", sessionID, listed.Code, len(toolNames))

	called := serveMCPWithSession(t, handler, fixture.handlers.apiKey, http.MethodPost, listSourcesRequest, sessionID)
	require.Equal(t, http.StatusOK, called.Code, called.Body.String())
	require.Contains(t, called.Body.String(), "ds-allowed")
	t.Logf("tools/call request: Mcp-Session-Id=%q; response: status=%d contains_allowed_datasource=true", sessionID, called.Code)

	missing := serveMCPWithSession(t, handler, fixture.handlers.apiKey, http.MethodPost, listToolsRequest, "")
	require.Equal(t, http.StatusBadRequest, missing.Code, missing.Body.String())

	forged := serveMCPWithSession(t, handler, fixture.handlers.apiKey, http.MethodPost, listToolsRequest, "forged-session-id")
	require.Equal(t, http.StatusNotFound, forged.Code, forged.Body.String())
	require.Contains(t, forged.Body.String(), "session not found")

	keyB, hashB, err := store.GenerateAPIKey()
	require.NoError(t, err)
	_, err = fixture.runtime.Store.Agents().Create(context.Background(), model.Agent{
		ID: "agent-session-b", Name: "Agent Session B", Status: "active", APIKeyHash: hashB, Level: "dml",
	})
	require.NoError(t, err)
	crossTenant := serveMCPWithSession(t, handler, keyB, http.MethodPost, listToolsRequest, sessionID)
	require.Equal(t, http.StatusForbidden, crossTenant.Code, crossTenant.Body.String())
	require.Contains(t, crossTenant.Body.String(), "session user mismatch")

	unauthenticated := serveMCPWithSession(t, handler, "", http.MethodPost, listToolsRequest, sessionID)
	require.Equal(t, http.StatusUnauthorized, unauthenticated.Code, unauthenticated.Body.String())

	deleted := serveMCPWithSession(t, handler, fixture.handlers.apiKey, http.MethodDelete, "", sessionID)
	require.Equal(t, http.StatusNoContent, deleted.Code, deleted.Body.String())
	afterDelete := serveMCPWithSession(t, handler, fixture.handlers.apiKey, http.MethodPost, listToolsRequest, sessionID)
	require.Equal(t, http.StatusNotFound, afterDelete.Code, afterDelete.Body.String())
}

func TestStreamableHTTPStatefulToolsListWithB5(t *testing.T) {
	fixture := newMCPFixture(t, "dml")
	service := &b5ServiceSpy{}
	handler, err := NewHTTPHandler(
		fixture.runtime,
		statefulHTTPTestConfig(100),
		zerolog.Nop(),
		WithB5Sessions(B5Options{B5Sessions: true, B5TxPostgres: true, Service: service}),
	)
	require.NoError(t, err)

	initialized := serveMCPWithSession(t, handler, fixture.handlers.apiKey, http.MethodPost, initializeRequest, "")
	sessionID := initialized.Header().Get(mcpSessionIDHeader)
	require.NotEmpty(t, sessionID)
	listed := serveMCPWithSession(t, handler, fixture.handlers.apiKey, http.MethodPost, listToolsRequest, sessionID)
	require.Equal(t, http.StatusOK, listed.Code, listed.Body.String())
	names := decodeToolNames(t, listed.Body.Bytes())
	require.Len(t, names, 15, "seven base tools plus eight B5 tools must be listed")
	for _, name := range b5ToolNames() {
		require.Contains(t, names, name)
	}
}

func TestStreamableHTTPExplicitStatelessRollback(t *testing.T) {
	fixture := newMCPFixture(t, "dml")
	handler, err := NewHTTPHandler(fixture.runtime, httpTestConfig(100), zerolog.Nop())
	require.NoError(t, err)

	initialized := serveMCPWithSession(t, handler, fixture.handlers.apiKey, http.MethodPost, initializeRequest, "")
	require.Equal(t, http.StatusOK, initialized.Code, initialized.Body.String())
	require.Empty(t, initialized.Header().Get(mcpSessionIDHeader))

	listed := serveMCPWithSession(t, handler, fixture.handlers.apiKey, http.MethodPost, listToolsRequest, "")
	require.Equal(t, http.StatusOK, listed.Code, listed.Body.String())
	require.Len(t, decodeToolNames(t, listed.Body.Bytes()), 7)
	called := serveMCPWithSession(t, handler, fixture.handlers.apiKey, http.MethodPost, listSourcesRequest, "ignored-by-stateless")
	require.Equal(t, http.StatusOK, called.Code, called.Body.String())
	require.Contains(t, called.Body.String(), "ds-allowed")

	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		response := serveMCPWithSession(t, handler, fixture.handlers.apiKey, method, "", "ignored-by-stateless")
		require.Equal(t, http.StatusMethodNotAllowed, response.Code, response.Body.String())
		require.Equal(t, "POST", response.Header().Get("Allow"))
	}
}

func TestStreamableHTTPSessionIdleTimeout(t *testing.T) {
	fixture := newMCPFixture(t, "dml")
	cfg := statefulHTTPTestConfig(1_000)
	cfg.MCP.HTTP.SessionTimeoutMS = 20
	handler, err := NewHTTPHandler(fixture.runtime, cfg, zerolog.Nop())
	require.NoError(t, err)

	initialized := serveMCPWithSession(t, handler, fixture.handlers.apiKey, http.MethodPost, initializeRequest, "")
	sessionID := initialized.Header().Get(mcpSessionIDHeader)
	require.NotEmpty(t, sessionID)
	require.Eventually(t, func() bool {
		response := serveMCPWithSession(t, handler, fixture.handlers.apiKey, http.MethodPost, listToolsRequest, sessionID)
		return response.Code == http.StatusNotFound
	}, time.Second, 25*time.Millisecond)
}

func serveMCPWithSession(
	t *testing.T,
	handler http.Handler,
	key string,
	method string,
	body string,
	sessionID string,
) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, "/mcp", strings.NewReader(body))
	if key != "" {
		request.Header.Set("Authorization", "Bearer "+key)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("MCP-Protocol-Version", legacyMCPProtocolVersion)
	if sessionID != "" {
		request.Header.Set(mcpSessionIDHeader, sessionID)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func decodeToolNames(t *testing.T, body []byte) []string {
	t.Helper()
	var response struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal(body, &response), string(body))
	names := make([]string, 0, len(response.Result.Tools))
	for _, tool := range response.Result.Tools {
		names = append(names, tool.Name)
	}
	return names
}

package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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
	t.Logf("initialize response: status=%d Content-Type=%q session_id_present=true", initialized.Code, initialized.Header().Get("Content-Type"))
	ready := serveMCPWithSession(t, handler, fixture.handlers.apiKey, http.MethodPost, `{"jsonrpc":"2.0","method":"notifications/initialized"}`, sessionID)
	require.Equal(t, http.StatusAccepted, ready.Code, ready.Body.String())

	listed := serveMCPWithSession(t, handler, fixture.handlers.apiKey, http.MethodPost, listToolsRequest, sessionID)
	require.Equal(t, http.StatusOK, listed.Code, listed.Body.String())
	toolNames := decodeToolNames(t, listed.Body.Bytes())
	require.Len(t, toolNames, 7, "the current non-B5 server surface has seven tools")
	t.Logf("tools/list response: status=%d tools=%d", listed.Code, len(toolNames))

	called := serveMCPWithSession(t, handler, fixture.handlers.apiKey, http.MethodPost, listSourcesRequest, sessionID)
	require.Equal(t, http.StatusOK, called.Code, called.Body.String())
	require.Contains(t, called.Body.String(), "ds-allowed")
	t.Logf("tools/call response: status=%d contains_allowed_datasource=true", called.Code)

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

func TestStreamableHTTPInitializeWithoutProtocolHeader(t *testing.T) {
	fixture := newMCPFixture(t, "dml")
	handler, err := NewHTTPHandler(fixture.runtime, statefulHTTPTestConfig(100), zerolog.Nop())
	require.NoError(t, err)

	request := newMCPRequest(fixture.handlers.apiKey, http.MethodPost, initializeRequest, "")
	request.Header.Del("MCP-Protocol-Version")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.NotEmpty(t, response.Header().Get(mcpSessionIDHeader))
}

func TestStreamableHTTPConcurrentSessionsAreIsolated(t *testing.T) {
	fixture := newMCPFixture(t, "dml")
	handler, err := NewHTTPHandler(fixture.runtime, statefulHTTPTestConfig(1_000), zerolog.Nop())
	require.NoError(t, err)

	sessionA := serveMCPWithSession(t, handler, fixture.handlers.apiKey, http.MethodPost, initializeRequest, "").Header().Get(mcpSessionIDHeader)
	sessionB := serveMCPWithSession(t, handler, fixture.handlers.apiKey, http.MethodPost, initializeRequest, "").Header().Get(mcpSessionIDHeader)
	require.NotEmpty(t, sessionA)
	require.NotEmpty(t, sessionB)
	require.NotEqual(t, sessionA, sessionB)

	var wait sync.WaitGroup
	errors := make(chan string, 2)
	for _, sessionID := range []string{sessionA, sessionB} {
		sessionID := sessionID
		wait.Add(1)
		go func() {
			defer wait.Done()
			response := serveMCPWithSession(t, handler, fixture.handlers.apiKey, http.MethodPost, listToolsRequest, sessionID)
			if response.Code != http.StatusOK {
				errors <- response.Body.String()
			}
		}()
	}
	wait.Wait()
	close(errors)
	for message := range errors {
		require.Fail(t, "concurrent session request failed", message)
	}

	deleted := serveMCPWithSession(t, handler, fixture.handlers.apiKey, http.MethodDelete, "", sessionA)
	require.Equal(t, http.StatusNoContent, deleted.Code, deleted.Body.String())
	require.Equal(t, http.StatusNotFound, serveMCPWithSession(t, handler, fixture.handlers.apiKey, http.MethodPost, listToolsRequest, sessionA).Code)
	require.Equal(t, http.StatusOK, serveMCPWithSession(t, handler, fixture.handlers.apiKey, http.MethodPost, listToolsRequest, sessionB).Code)
}

func TestStreamableHTTPSessionDoesNotSurviveHandlerRestart(t *testing.T) {
	fixture := newMCPFixture(t, "dml")
	first, err := NewHTTPHandler(fixture.runtime, statefulHTTPTestConfig(100), zerolog.Nop())
	require.NoError(t, err)
	sessionID := serveMCPWithSession(t, first, fixture.handlers.apiKey, http.MethodPost, initializeRequest, "").Header().Get(mcpSessionIDHeader)
	require.NotEmpty(t, sessionID)

	restarted, err := NewHTTPHandler(fixture.runtime, statefulHTTPTestConfig(100), zerolog.Nop())
	require.NoError(t, err)
	response := serveMCPWithSession(t, restarted, fixture.handlers.apiKey, http.MethodPost, listToolsRequest, sessionID)
	require.Equal(t, http.StatusNotFound, response.Code, response.Body.String())
	require.Contains(t, response.Body.String(), "session not found")
	require.Equal(t, MCPSessionExpiredValue, response.Header().Get(MCPSessionExpiredHeader))
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
	request := newMCPRequest(key, method, body, sessionID)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func newMCPRequest(key string, method string, body string, sessionID string) *http.Request {
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
	return request
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

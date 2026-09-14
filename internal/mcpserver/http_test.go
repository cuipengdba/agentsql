package mcpserver

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/config"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/cuipengdba/agentsql/internal/version"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

const (
	initializeRequest  = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`
	listToolsRequest   = `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`
	listSourcesRequest = `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"list_datasources","arguments":{}}}`
)

func TestT241HealthAndReadinessBypassAgentAuthentication(t *testing.T) {
	fixture := newMCPFixture(t, "dml")
	webCalls := 0
	webConsole := http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		webCalls++
		writer.WriteHeader(http.StatusTeapot)
	})
	handler, err := NewHTTPHandler(
		fixture.runtime,
		httpTestConfig(100),
		zerolog.Nop(),
		WithWebConsole(webConsole),
	)
	require.NoError(t, err)

	health := httptest.NewRecorder()
	handler.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	require.Equal(t, http.StatusOK, health.Code)
	require.Equal(t, "application/json", health.Header().Get("Content-Type"))
	require.JSONEq(t, fmt.Sprintf(`{"status":"ok","version":%q}`, version.Version), health.Body.String())
	require.Zero(t, webCalls)

	ready := httptest.NewRecorder()
	handler.ServeHTTP(ready, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	require.Equal(t, http.StatusOK, ready.Code)
	require.JSONEq(t, `{"status":"ready"}`, ready.Body.String())
	require.Zero(t, webCalls)

	unauthorized := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	handler.ServeHTTP(unauthorized, request)
	require.Equal(t, http.StatusUnauthorized, unauthorized.Code)
	require.JSONEq(t, `{"error":"unauthorized"}`, unauthorized.Body.String())
	require.Zero(t, webCalls)
}

func TestT241ReadinessFailsClosedWithoutStore(t *testing.T) {
	recorder := httptest.NewRecorder()
	readinessHandler(nil).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
	require.JSONEq(t, `{"status":"not ready"}`, recorder.Body.String())
}

func TestStreamableHTTPEndToEndSevenTools(t *testing.T) {
	fixture := newMCPFixture(t, "dml")
	handler, registry, err := newHTTPHandlerWithRegistry(fixture.runtime, httpTestConfig(100), zerolog.Nop())
	require.NoError(t, err)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	for _, body := range []string{initializeRequest, listToolsRequest, listSourcesRequest} {
		status, responseBody, err := doMCPRequest(server.Client(), server.URL+"/mcp", fixture.handlers.apiKey, http.MethodPost, body)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, status, responseBody)
		if body == listToolsRequest {
			for _, name := range []string{
				"list_datasources", "list_schema", "explain_query", "query",
				"execute_write", "request_approval", "get_approval_result",
			} {
				require.Equal(t, 1, strings.Count(responseBody, `"`+name+`"`), name)
			}
			require.NotContains(t, responseBody, "execute_raw_sql")
		}
		if body == listSourcesRequest {
			require.Contains(t, responseBody, "ds-allowed")
			require.NotContains(t, responseBody, "ds-hidden")
			require.NotContains(t, responseBody, "database-password")
			require.NotContains(t, responseBody, "db.internal")
		}
	}
	require.Equal(t, 1, registry.serverCount())
}

func TestHTTPAuthenticationFailuresStopBeforeSDK(t *testing.T) {
	tests := []struct {
		name          string
		authorization string
		disableAgent  bool
		invalidLevel  bool
	}{
		{name: "missing header"},
		{name: "wrong scheme", authorization: "Basic abc"},
		{name: "non exact Bearer spacing", authorization: "Bearer  asql_wrong"},
		{name: "wrong key", authorization: "Bearer asql_wrong"},
		{name: "disabled Agent", disableAgent: true},
		{name: "invalid Agent level", invalidLevel: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newMCPFixture(t, "dml")
			key := fixture.handlers.apiKey
			if test.disableAgent || test.invalidLevel {
				agent, err := fixture.runtime.Store.Agents().Get(context.Background(), fixture.agent.ID)
				require.NoError(t, err)
				if test.disableAgent {
					agent.Status = "disabled"
				}
				if test.invalidLevel {
					agent.Level = "invalid"
				}
				_, err = fixture.runtime.Store.Agents().Update(context.Background(), agent)
				require.NoError(t, err)
			} else if test.authorization != "" {
				key = strings.TrimPrefix(test.authorization, "Bearer ")
			}
			handler, registry, err := newHTTPHandlerWithRegistry(fixture.runtime, httpTestConfig(100), zerolog.Nop())
			require.NoError(t, err)
			request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(initializeRequest))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Accept", "application/json, text/event-stream")
			if test.authorization != "" {
				request.Header.Set("Authorization", test.authorization)
			} else if test.disableAgent || test.invalidLevel {
				request.Header.Set("Authorization", "Bearer "+key)
			}
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			require.Equal(t, http.StatusUnauthorized, recorder.Code)
			require.Equal(t, `{"error":"unauthorized"}`, recorder.Body.String())
			require.Zero(t, registry.serverCount())
			require.Zero(t, fixture.provider.calls())
			page, err := fixture.runtime.Store.AuditLogs().Page(context.Background(), 1, 1)
			require.NoError(t, err)
			require.Zero(t, page.Total)
		})
	}
}

func TestHTTPMultiTenantFiftyConcurrentRequestsDoNotCross(t *testing.T) {
	fixture := newMCPFixture(t, "dml")
	keyB, hashB, err := store.GenerateAPIKey()
	require.NoError(t, err)
	agentB, err := fixture.runtime.Store.Agents().Create(context.Background(), model.Agent{
		ID: "agent-b", Name: "Agent B", Status: "active", APIKeyHash: hashB, Level: "dml",
	})
	require.NoError(t, err)
	_, err = fixture.runtime.Store.Datasources().Create(context.Background(), model.Datasource{
		ID: "ds-b", Name: "B", DBType: "postgres", Host: "b.internal", Port: 5432,
		Database: "b", Username: "b", ConnLimit: 5, StmtTimeoutMS: 5_000, RowLimit: 100,
	}, "database-password")
	require.NoError(t, err)
	_, err = fixture.runtime.Store.Policies().Create(context.Background(), model.Policy{
		ID: "agent-b-policy", AgentID: agentB.ID, DatasourceID: "ds-b",
		ObjectType: "table", ObjectName: "public.customers", Action: "allow",
	})
	require.NoError(t, err)

	handler, registry, err := newHTTPHandlerWithRegistry(fixture.runtime, httpTestConfig(1_000), zerolog.Nop())
	require.NoError(t, err)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	type outcome struct {
		expected  string
		forbidden string
		body      string
		err       error
		status    int
	}
	results := make(chan outcome, 50)
	var wait sync.WaitGroup
	for index := 0; index < 50; index++ {
		key, expected, forbidden := fixture.handlers.apiKey, "ds-allowed", "ds-b"
		if index%2 == 1 {
			key, expected, forbidden = keyB, "ds-b", "ds-allowed"
		}
		wait.Add(1)
		go func(id int, key, expected, forbidden string) {
			defer wait.Done()
			body := strings.Replace(listSourcesRequest, `"id":3`, fmt.Sprintf(`"id":%d`, id+10), 1)
			status, responseBody, err := doMCPRequest(server.Client(), server.URL+"/mcp", key, http.MethodPost, body)
			results <- outcome{expected: expected, forbidden: forbidden, body: responseBody, err: err, status: status}
		}(index, key, expected, forbidden)
	}
	wait.Wait()
	close(results)
	for result := range results {
		require.NoError(t, result.err)
		require.Equal(t, http.StatusOK, result.status, result.body)
		require.Contains(t, result.body, result.expected)
		require.NotContains(t, result.body, result.forbidden)
	}
	require.Equal(t, 2, registry.serverCount())
	require.Equal(t, 2, registry.buildCount())
}

func TestHTTPStatusMatrix(t *testing.T) {
	t.Run("rate limited", func(t *testing.T) {
		fixture := newMCPFixture(t, "dml")
		handler, _, err := newHTTPHandlerWithRegistry(fixture.runtime, httpTestConfig(1), zerolog.Nop())
		require.NoError(t, err)
		first := httptest.NewRecorder()
		handler.ServeHTTP(first, authorizedRequest(http.MethodPost, initializeRequest, fixture.handlers.apiKey))
		second := httptest.NewRecorder()
		handler.ServeHTTP(second, authorizedRequest(http.MethodPost, listToolsRequest, fixture.handlers.apiKey))
		require.Equal(t, http.StatusTooManyRequests, second.Code)
		require.Equal(t, `{"error":"rate limited"}`, second.Body.String())
	})
	t.Run("method not allowed", func(t *testing.T) {
		fixture := newMCPFixture(t, "dml")
		handler, err := NewHTTPHandler(fixture.runtime, httpTestConfig(100), zerolog.Nop())
		require.NoError(t, err)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, authorizedRequest(http.MethodGet, "", fixture.handlers.apiKey))
		require.Equal(t, http.StatusMethodNotAllowed, recorder.Code)
	})
	t.Run("malformed JSON", func(t *testing.T) {
		fixture := newMCPFixture(t, "dml")
		handler, err := NewHTTPHandler(fixture.runtime, httpTestConfig(100), zerolog.Nop())
		require.NoError(t, err)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, authorizedRequest(http.MethodPost, "{", fixture.handlers.apiKey))
		require.Equal(t, http.StatusBadRequest, recorder.Code)
	})
	t.Run("request too large", func(t *testing.T) {
		fixture := newMCPFixture(t, "dml")
		handler, err := NewHTTPHandler(fixture.runtime, httpTestConfig(100), zerolog.Nop())
		require.NoError(t, err)
		recorder := httptest.NewRecorder()
		body := strings.Repeat("x", maxMCPRequestBodyBytes+1)
		handler.ServeHTTP(recorder, authorizedRequest(http.MethodPost, body, fixture.handlers.apiKey))
		require.Equal(t, http.StatusRequestEntityTooLarge, recorder.Code)
	})
	t.Run("not found", func(t *testing.T) {
		fixture := newMCPFixture(t, "dml")
		handler, err := NewHTTPHandler(fixture.runtime, httpTestConfig(100), zerolog.Nop())
		require.NoError(t, err)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/v1", nil))
		require.Equal(t, http.StatusNotFound, recorder.Code)
		require.Equal(t, `{"error":"not found"}`, recorder.Body.String())
	})
}

func TestAgentServerRegistryCacheKeyAndCapacity(t *testing.T) {
	fixture := newMCPFixture(t, "dml")
	registry := newAgentServerRegistry(fixture.runtime, zerolog.Nop(), 10)
	agent := fixture.agent
	agent.UpdatedAt = time.Time{}
	first, err := registry.getOrCreate(agent, fixture.handlers.apiKey)
	require.NoError(t, err)
	reused, err := registry.getOrCreate(agent, fixture.handlers.apiKey)
	require.NoError(t, err)
	require.True(t, first == reused)
	agent.UpdatedAt = time.Now().UTC()
	rebuilt, err := registry.getOrCreate(agent, fixture.handlers.apiKey)
	require.NoError(t, err)
	require.False(t, first == rebuilt)
	require.Equal(t, 2, registry.buildCount())

	registry = newAgentServerRegistry(fixture.runtime, zerolog.Nop(), 10)
	for index := 0; index <= agentServerRegistryLimit; index++ {
		candidate := fixture.agent
		candidate.ID = fmt.Sprintf("agent-%03d", index)
		candidate.UpdatedAt = time.Time{}
		_, err := registry.getOrCreate(candidate, fixture.handlers.apiKey)
		require.NoError(t, err)
	}
	require.Equal(t, 1, registry.serverCount())
	require.Equal(t, agentServerRegistryLimit+1, registry.buildCount())
}

func TestRecoverMiddlewareReturnsSafe500(t *testing.T) {
	var logs bytes.Buffer
	logger := zerolog.New(&logs)
	handler := recoverMiddleware(logger, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("secret stack detail")
	}))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/mcp", nil))
	require.Equal(t, http.StatusInternalServerError, recorder.Code)
	require.Equal(t, `{"error":"internal error"}`, recorder.Body.String())
	require.NotContains(t, recorder.Body.String(), "secret stack detail")
	require.Contains(t, logs.String(), "stack")
}

func TestAccessLogContainsMetadataButNoKeyOrSQL(t *testing.T) {
	var logs bytes.Buffer
	logger := zerolog.New(&logs)
	handler := accessLogMiddleware(logger, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	}))
	identity := requestIdentity{
		agent:    model.Agent{ID: "agent-log"},
		plainKey: "asql_must_not_be_logged",
	}
	request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader("SELECT secret FROM vault"))
	request.Header.Set("Authorization", "Bearer "+identity.plainKey)
	request = request.WithContext(context.WithValue(request.Context(), requestIdentityContextKey{}, identity))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	require.Contains(t, logs.String(), "agent-log")
	require.Contains(t, logs.String(), "POST")
	require.Contains(t, logs.String(), "/mcp")
	require.Contains(t, logs.String(), "204")
	require.NotContains(t, logs.String(), identity.plainKey)
	require.NotContains(t, logs.String(), "SELECT secret")
}

func TestNewHTTPHandlerFailsClosed(t *testing.T) {
	_, err := NewHTTPHandler(nil, httpTestConfig(10), zerolog.Nop())
	require.Error(t, err)
	fixture := newMCPFixture(t, "dml")
	cfg := httpTestConfig(10)
	cfg.Defaults.QPSPerAgent = 0
	_, err = NewHTTPHandler(fixture.runtime, cfg, zerolog.Nop())
	require.Error(t, err)
}

func authorizedRequest(method, body, key string) *http.Request {
	request := httptest.NewRequest(method, "/mcp", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("MCP-Protocol-Version", "2025-06-18")
	return request
}

func doMCPRequest(
	client *http.Client,
	url string,
	key string,
	method string,
	body string,
) (int, string, error) {
	request, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		return 0, "", err
	}
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("MCP-Protocol-Version", "2025-06-18")
	response, err := client.Do(request)
	if err != nil {
		return 0, "", err
	}
	defer response.Body.Close()
	contents, err := io.ReadAll(response.Body)
	if err != nil {
		return 0, "", err
	}
	return response.StatusCode, string(contents), nil
}

func httpTestConfig(qps int) config.Config {
	return config.Config{
		Server: config.ServerConfig{HTTPListen: "127.0.0.1:8650"},
		Store:  config.StoreConfig{SQLitePath: "test.db"},
		Defaults: config.DefaultsConfig{
			StatementTimeoutMS:    5_000,
			RowLimit:              1_000,
			MaxConnsPerDatasource: 5,
			QPSPerAgent:           qps,
		},
		Theme: config.ThemeConfig{Default: "dark"},
	}
}

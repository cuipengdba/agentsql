package mcpserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type bearerTransport struct {
	base http.RoundTripper
	key  string
}

func (transport bearerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	copy := request.Clone(request.Context())
	copy.Header.Set("Authorization", "Bearer "+transport.key)
	return transport.base.RoundTrip(copy)
}

func modernRequest(key, body string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, modernMCPPath, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("MCP-Protocol-Version", modernMCPProtocolVersion)
	request.Header.Set("Mcp-Method", "tools/call")
	request.Header.Set("Mcp-Name", "list_datasources")
	return request
}

func modernToolCall(name string, arguments map[string]any) string {
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{
			"name": name, "arguments": arguments,
			"_meta": map[string]any{
				"io.modelcontextprotocol/protocolVersion":    modernMCPProtocolVersion,
				"io.modelcontextprotocol/clientCapabilities": map[string]any{},
				"io.modelcontextprotocol/clientInfo": map[string]any{
					"name": "agentsql-test", "version": "1",
				},
			},
		},
	})
	if err != nil {
		panic(err)
	}
	return string(body)
}

func modernDiscoverCall() string {
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "server/discover",
		"params": map[string]any{
			"_meta": map[string]any{
				"io.modelcontextprotocol/protocolVersion":    modernMCPProtocolVersion,
				"io.modelcontextprotocol/clientCapabilities": map[string]any{},
				"io.modelcontextprotocol/clientInfo": map[string]any{
					"name": "agentsql-test", "version": "1",
				},
			},
		},
	})
	if err != nil {
		panic(err)
	}
	return string(body)
}

func TestModernHTTPDiscoverAdvertisesOnlyStatelessVersion(t *testing.T) {
	fixture := newMCPFixture(t, "readonly")
	handler, err := NewHTTPHandler(fixture.runtime, statefulHTTPTestConfig(100), zerolog.Nop())
	require.NoError(t, err)
	request := modernRequest(fixture.handlers.apiKey, modernDiscoverCall())
	request.Header.Set("Mcp-Method", "server/discover")
	request.Header.Del("Mcp-Name")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var envelope struct {
		Result struct {
			SupportedVersions []string `json:"supportedVersions"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &envelope))
	require.Equal(t, []string{modernMCPProtocolVersion}, envelope.Result.SupportedVersions)
}

func TestModernHTTPIgnoresLegacySessionAndReplayHeaders(t *testing.T) {
	fixture := newMCPFixture(t, "readonly")
	handler, err := NewHTTPHandler(fixture.runtime, statefulHTTPTestConfig(100), zerolog.Nop())
	require.NoError(t, err)
	request := modernRequest(fixture.handlers.apiKey, modernToolCall("list_datasources", map[string]any{}))
	request.Header.Set("Mcp-Session-Id", "forged-legacy-session")
	request.Header.Set("Last-Event-ID", "forged-replay-position")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Empty(t, response.Header().Get("Mcp-Session-Id"))
	require.Contains(t, response.Body.String(), "ds-allowed")
}

func TestModernHTTPProtocolErrorsAndAllowedMethods(t *testing.T) {
	fixture := newMCPFixture(t, "readonly")
	handler, err := NewHTTPHandler(fixture.runtime, statefulHTTPTestConfig(100), zerolog.Nop())
	require.NoError(t, err)
	body := modernToolCall("list_datasources", map[string]any{})
	for _, test := range []struct {
		name string
		head string
		code int
	}{
		{name: "missing version", head: "", code: mcp.CodeHeaderMismatch},
		{name: "old version", head: "2025-11-25", code: mcp.CodeUnsupportedProtocolVersion},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := modernRequest(fixture.handlers.apiKey, body)
			if test.head == "" {
				request.Header.Del("Mcp-Protocol-Version")
			} else {
				request.Header.Set("Mcp-Protocol-Version", test.head)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			require.Equal(t, http.StatusBadRequest, response.Code)
			var envelope struct {
				JSONRPC string `json:"jsonrpc"`
				Error   struct {
					Code int `json:"code"`
					Data struct {
						Supported []string `json:"supported"`
					} `json:"data"`
				} `json:"error"`
			}
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &envelope))
			require.Equal(t, "2.0", envelope.JSONRPC)
			require.Equal(t, test.code, envelope.Error.Code)
			if test.code == mcp.CodeUnsupportedProtocolVersion {
				require.Equal(t, []string{modernMCPProtocolVersion}, envelope.Error.Data.Supported)
			}
		})
	}
	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			request := httptest.NewRequest(method, modernMCPPath, nil)
			request.Header.Set("Authorization", "Bearer "+fixture.handlers.apiKey)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			require.Equal(t, http.StatusMethodNotAllowed, response.Code, response.Body.String())
			require.Equal(t, "POST", response.Header().Get("Allow"))
		})
	}
}

func TestTaskGrantMCPServerRegistersOnlyQuery(t *testing.T) {
	fixture := newMCPFixture(t, "dml")
	agent := fixture.agent
	agent.TaskGrantID = "task-grant"
	server, err := buildBoundServerWithVersion(agent, fixture.handlers.apiKey, fixture.runtime, zerolog.Nop(), "test",
		B5Options{B5Sessions: true, B5TxPostgres: true, Service: &b5ServiceSpy{}})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverErrors := make(chan error, 1)
	go func() { serverErrors <- server.sdk.Run(ctx, serverTransport) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "agentsql-task-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = session.Close()
		cancel()
		select {
		case <-serverErrors:
		case <-time.After(time.Second):
		}
	})
	listed, err := session.ListTools(ctx, nil)
	require.NoError(t, err)
	require.Len(t, listed.Tools, 1)
	require.Equal(t, "query", listed.Tools[0].Name)
	for _, name := range []string{"execute_approved", "execute_write", "request_approval", "open_session", "begin_transaction"} {
		_, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: map[string]any{}})
		require.Error(t, err, name)
	}
}

func TestTaskReadKeyHTTPRoutesBindToolsSessionsAndRevocation(t *testing.T) {
	fixture := newMCPFixture(t, "dml")
	ctx := context.Background()
	const issuerID = "task-issuer"
	_, err := fixture.runtime.Store.RBAC().CreateUser(ctx, model.User{
		ID: issuerID, TenantID: store.DefaultTenantID, Username: issuerID,
		PasswordHash: "test-only-hash", Status: "active",
	})
	require.NoError(t, err)
	taskKey, taskHash, err := store.GenerateAPIKey()
	require.NoError(t, err)
	digest := sha256.Sum256([]byte("SELECT phone FROM public.customers LIMIT 1"))
	grant, err := fixture.runtime.Store.TaskReadGrants().Create(ctx, model.TaskReadGrant{
		ID: "task-mcp", TenantID: store.DefaultTenantID, IssuerUserID: issuerID,
		AgentID: fixture.agent.ID, DatasourceID: "ds-allowed",
		SQLSHA256: hex.EncodeToString(digest[:]), KeyHash: taskHash,
		ExpiresAt: time.Now().Add(time.Hour),
	})
	require.NoError(t, err)
	handler, err := NewHTTPHandler(
		fixture.runtime, statefulHTTPTestConfig(100), zerolog.Nop(),
		WithB5Sessions(B5Options{B5Sessions: true, B5TxPostgres: true, Service: &b5ServiceSpy{}}),
	)
	require.NoError(t, err)

	// Both endpoints resolve the temporary key as the original Agent with a
	// separate key hash, and expose only the ordinary read-only query tool.
	taskLegacy := serveMCPWithSession(t, handler, taskKey, http.MethodPost, initializeRequest, "")
	require.Equal(t, http.StatusOK, taskLegacy.Code, taskLegacy.Body.String())
	taskSession := taskLegacy.Header().Get(mcpSessionIDHeader)
	require.NotEmpty(t, taskSession)
	taskTools := serveMCPWithSession(t, handler, taskKey, http.MethodPost, listToolsRequest, taskSession)
	require.Equal(t, http.StatusOK, taskTools.Code, taskTools.Body.String())
	require.Equal(t, []string{"query"}, decodeToolNames(t, taskTools.Body.Bytes()))

	modernList := strings.Replace(modernDiscoverCall(), "server/discover", "tools/list", 1)
	modernTaskRequest := func(key string) *http.Request {
		request := modernRequest(key, modernList)
		request.Header.Set("Mcp-Method", "tools/list")
		request.Header.Del("Mcp-Name")
		return request
	}
	modernTask := httptest.NewRecorder()
	handler.ServeHTTP(modernTask, modernTaskRequest(taskKey))
	require.Equal(t, http.StatusOK, modernTask.Code, modernTask.Body.String())
	require.Equal(t, []string{"query"}, decodeToolNames(t, modernTask.Body.Bytes()))
	for _, toolName := range []string{"execute_approved", "execute_write", "request_approval", "open_session", "begin_transaction"} {
		legacyCall, err := json.Marshal(map[string]any{
			"jsonrpc": "2.0", "id": 3, "method": "tools/call",
			"params": map[string]any{"name": toolName, "arguments": map[string]any{}},
		})
		require.NoError(t, err)
		legacyRejected := serveMCPWithSession(t, handler, taskKey, http.MethodPost, string(legacyCall), taskSession)
		require.Equal(t, http.StatusOK, legacyRejected.Code, legacyRejected.Body.String())
		require.Contains(t, legacyRejected.Body.String(), `"error"`, toolName)

		modernCall := modernRequest(taskKey, modernToolCall(toolName, map[string]any{}))
		modernCall.Header.Set("Mcp-Name", toolName)
		modernRejected := httptest.NewRecorder()
		handler.ServeHTTP(modernRejected, modernCall)
		require.Equal(t, http.StatusBadRequest, modernRejected.Code, modernRejected.Body.String())
		require.Contains(t, modernRejected.Body.String(), `"error"`, toolName)
	}

	// A permanent key for the same Agent cannot reuse the task key's legacy
	// transport session, or vice versa.
	permanentLegacy := serveMCPWithSession(t, handler, fixture.handlers.apiKey, http.MethodPost, initializeRequest, "")
	require.Equal(t, http.StatusOK, permanentLegacy.Code, permanentLegacy.Body.String())
	permanentSession := permanentLegacy.Header().Get(mcpSessionIDHeader)
	require.NotEmpty(t, permanentSession)
	for _, attempt := range []struct {
		key, session string
	}{
		{fixture.handlers.apiKey, taskSession},
		{taskKey, permanentSession},
	} {
		crossKey := serveMCPWithSession(t, handler, attempt.key, http.MethodPost, listToolsRequest, attempt.session)
		require.Equal(t, http.StatusForbidden, crossKey.Code, crossKey.Body.String())
	}

	// Task-key query attempts must reach the SQL pipeline even when ordinary
	// query tool preflight would reject the statement or datasource. Otherwise
	// these denials would have no durable grant/issuer audit record.
	auditBefore, err := fixture.runtime.Store.AuditLogs().Page(ctx, 1, 10)
	require.NoError(t, err)
	for _, attempt := range []struct {
		datasourceID string
		sql          string
	}{
		{datasourceID: "ds-allowed", sql: "DELETE FROM public.customers"},
		{datasourceID: "ds-hidden", sql: "SELECT phone FROM public.customers LIMIT 1"},
		{datasourceID: "ds-missing", sql: "SELECT phone FROM public.customers LIMIT 1"},
		{datasourceID: "", sql: "SELECT phone FROM public.customers LIMIT 1"},
	} {
		call := modernRequest(taskKey, modernToolCall("query", map[string]any{
			"datasource_id": attempt.datasourceID, "sql": attempt.sql,
		}))
		call.Header.Set("Mcp-Name", "query")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, call)
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		require.Contains(t, response.Body.String(), `"decision":"deny"`)
	}
	auditAfter, err := fixture.runtime.Store.AuditLogs().Page(ctx, 1, 10)
	require.NoError(t, err)
	require.EqualValues(t, auditBefore.Total+4, auditAfter.Total)
	for _, log := range auditAfter.List[:4] {
		require.Equal(t, string(model.DecisionDeny), log.Decision)
		require.Equal(t, fixture.agent.ID, *log.AgentID)
		require.Equal(t, "query", *log.MCPTool)
		require.Nil(t, log.SQLRaw)
		require.NotNil(t, log.DetailsJSON)
		require.Contains(t, *log.DetailsJSON, `"task_grant_id":"task-mcp"`)
		require.Contains(t, *log.DetailsJSON, `"task_grant_issuer_id":"task-issuer"`)
	}

	_, err = fixture.runtime.Store.TaskReadGrants().Revoke(ctx, grant.ID)
	require.NoError(t, err)
	afterModernRevoke := httptest.NewRecorder()
	handler.ServeHTTP(afterModernRevoke, modernTaskRequest(taskKey))
	require.Equal(t, http.StatusUnauthorized, afterModernRevoke.Code, afterModernRevoke.Body.String())
	afterLegacyRevoke := serveMCPWithSession(t, handler, taskKey, http.MethodPost, listToolsRequest, taskSession)
	require.Equal(t, http.StatusUnauthorized, afterLegacyRevoke.Code, afterLegacyRevoke.Body.String())
	permanentStillValid := serveMCPWithSession(t, handler, fixture.handlers.apiKey, http.MethodPost, listToolsRequest, permanentSession)
	require.Equal(t, http.StatusOK, permanentStillValid.Code, permanentStillValid.Body.String())
	require.Contains(t, decodeToolNames(t, permanentStillValid.Body.Bytes()), "execute_approved")
}

func TestModernHTTPGoSDKInteroperabilityAndB5Isolation(t *testing.T) {
	fixture := newMCPFixture(t, "dml")
	handler, err := NewHTTPHandler(
		fixture.runtime, statefulHTTPTestConfig(100), zerolog.Nop(),
		WithB5Sessions(B5Options{B5Sessions: true, B5TxPostgres: true, Service: &b5ServiceSpy{}}),
	)
	require.NoError(t, err)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client := mcp.NewClient(&mcp.Implementation{Name: "agentsql-modern-test", Version: "1"}, nil)
	httpClient := server.Client()
	httpClient.Transport = bearerTransport{base: httpClient.Transport, key: fixture.handlers.apiKey}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint: server.URL + modernMCPPath, HTTPClient: httpClient,
	}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = session.Close() })
	require.Equal(t, modernMCPProtocolVersion, session.InitializeResult().ProtocolVersion)
	listed, err := session.ListTools(ctx, nil)
	require.NoError(t, err)
	require.Len(t, listed.Tools, 12)
	for _, tool := range listed.Tools {
		require.NotContains(t, b5ToolNames(), tool.Name)
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "list_datasources", Arguments: map[string]any{}})
	require.NoError(t, err)
	encoded, err := json.Marshal(result)
	require.NoError(t, err)
	require.Contains(t, string(encoded), "ds-allowed")
	require.NotContains(t, string(encoded), "ds-hidden")
	// The old endpoint still requires and returns a transport session ID and
	// keeps the B5 tool surface when configured.
	legacy := serveMCPWithSession(t, handler, fixture.handlers.apiKey, http.MethodPost, initializeRequest, "")
	require.Equal(t, http.StatusOK, legacy.Code, legacy.Body.String())
	sessionID := legacy.Header().Get(mcpSessionIDHeader)
	require.NotEmpty(t, sessionID)
	legacyTools := serveMCPWithSession(t, handler, fixture.handlers.apiKey, http.MethodPost, listToolsRequest, sessionID)
	require.Equal(t, http.StatusOK, legacyTools.Code, legacyTools.Body.String())
	require.Len(t, decodeToolNames(t, legacyTools.Body.Bytes()), 20)
}

func TestModernHTTPRejectsAmbiguousOrInvalidProtocolBeforeToolDispatch(t *testing.T) {
	fixture := newMCPFixture(t, "dml")
	handler, err := NewHTTPHandler(fixture.runtime, statefulHTTPTestConfig(100), zerolog.Nop())
	require.NoError(t, err)
	valid := modernToolCall("list_datasources", map[string]any{})
	tests := []struct {
		name    string
		body    string
		prepare func(*http.Request)
		status  int
	}{
		{name: "missing version header", body: valid, prepare: func(request *http.Request) { request.Header.Del("MCP-Protocol-Version") }, status: http.StatusBadRequest},
		{name: "duplicate version header", body: valid, prepare: func(request *http.Request) { request.Header.Add("MCP-Protocol-Version", modernMCPProtocolVersion) }, status: http.StatusBadRequest},
		{name: "forged version header", body: valid, prepare: func(request *http.Request) { request.Header.Set("MCP-Protocol-Version", "2025-11-25") }, status: http.StatusBadRequest},
		{name: "forged version metadata", body: strings.Replace(valid, `"io.modelcontextprotocol/protocolVersion":"2026-07-28"`, `"io.modelcontextprotocol/protocolVersion":"2025-11-25"`, 1), status: http.StatusBadRequest},
		{name: "mismatched name", body: valid, prepare: func(request *http.Request) { request.Header.Set("Mcp-Name", "query") }, status: http.StatusBadRequest},
		{name: "duplicate version metadata", body: strings.Replace(valid, `"io.modelcontextprotocol/protocolVersion":"2026-07-28"`, `"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/protocolVersion":"2025-11-25"`, 1), status: http.StatusBadRequest},
		{name: "missing version metadata", body: strings.Replace(valid, `,"io.modelcontextprotocol/protocolVersion":"2026-07-28"`, "", 1), status: http.StatusBadRequest},
		{name: "missing client capabilities", body: strings.Replace(valid, `"io.modelcontextprotocol/clientCapabilities":{},`, "", 1), status: http.StatusBadRequest},
		{name: "duplicate tool name", body: strings.Replace(valid, `"name":"list_datasources"`, `"name":"list_datasources","name":"query"`, 1), status: http.StatusBadRequest},
		{name: "legacy initialize", body: initializeRequest, prepare: func(request *http.Request) {
			request.Header.Set("Mcp-Method", "initialize")
			request.Header.Del("Mcp-Name")
		}, status: http.StatusBadRequest},
		{name: "malformed body", body: `{"jsonrpc":`, status: http.StatusBadRequest},
		{name: "oversize body", body: strings.Repeat("x", maxMCPRequestBodyBytes+1), status: http.StatusRequestEntityTooLarge},
		{name: "cross origin", body: valid, prepare: func(request *http.Request) { request.Header.Set("Origin", "https://evil.example") }, status: http.StatusForbidden},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := modernRequest(fixture.handlers.apiKey, test.body)
			if test.prepare != nil {
				test.prepare(request)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			require.Equal(t, test.status, response.Code, response.Body.String())
			require.NotContains(t, response.Body.String(), "ds-allowed")
		})
	}
	require.Zero(t, fixture.executor.snapshot().execute)
}

func TestModernHTTPAuthenticatesEveryRequestAndSharesRateLimit(t *testing.T) {
	fixture := newMCPFixture(t, "readonly")
	handler, err := NewHTTPHandler(fixture.runtime, httpTestConfig(1), zerolog.Nop())
	require.NoError(t, err)
	body := modernToolCall("list_datasources", map[string]any{})
	first := httptest.NewRecorder()
	handler.ServeHTTP(first, modernRequest(fixture.handlers.apiKey, body))
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	second := httptest.NewRecorder()
	handler.ServeHTTP(second, modernRequest(fixture.handlers.apiKey, body))
	require.Equal(t, http.StatusTooManyRequests, second.Code, second.Body.String())
	legacy := httptest.NewRecorder()
	handler.ServeHTTP(legacy, authorizedRequest(http.MethodPost, initializeRequest, fixture.handlers.apiKey))
	require.Equal(t, http.StatusTooManyRequests, legacy.Code, legacy.Body.String())
	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, modernRequest("invalid-key", body))
	require.Equal(t, http.StatusUnauthorized, unauthorized.Code)
	disabledAgent := fixture.agent
	disabledAgent.Status = "disabled"
	_, err = fixture.runtime.Store.Agents().Update(context.Background(), disabledAgent)
	require.NoError(t, err)
	disabled := httptest.NewRecorder()
	handler.ServeHTTP(disabled, modernRequest(fixture.handlers.apiKey, body))
	require.Equal(t, http.StatusUnauthorized, disabled.Code)
}

func TestModernHTTPTenantAndAuditAreBoundToCurrentAgentKey(t *testing.T) {
	fixture := newMCPFixture(t, "readonly")
	base := context.Background()
	_, err := fixture.runtime.Store.RBAC().CreateTenant(base, model.Tenant{ID: "tenant_b", Name: "Tenant B", Status: "active"})
	require.NoError(t, err)
	tenantB, err := store.WithTenant(base, "tenant_b")
	require.NoError(t, err)
	keyB, hashB, err := store.GenerateAPIKey()
	require.NoError(t, err)
	agentB, err := fixture.runtime.Store.Agents().Create(tenantB, model.Agent{
		ID: "agent-b", Name: "Agent B", Status: "active", APIKeyHash: hashB, Level: "readonly",
	})
	require.NoError(t, err)
	_, err = fixture.runtime.Store.Datasources().Create(tenantB, model.Datasource{
		ID: "ds-b", Name: "Tenant B", DBType: "postgres", Host: "db-b.internal", Port: 5432,
		Database: "b", Username: "reader", ConnLimit: 5, StmtTimeoutMS: 5000, RowLimit: 100,
	}, "test-password")
	require.NoError(t, err)
	_, err = fixture.runtime.Store.Policies().Create(tenantB, model.Policy{
		ID: "tenant-b-table", AgentID: agentB.ID, DatasourceID: "ds-b",
		ObjectType: "table", ObjectName: "public.customers", Action: "allow",
	})
	require.NoError(t, err)
	handler, err := NewHTTPHandler(fixture.runtime, statefulHTTPTestConfig(100), zerolog.Nop())
	require.NoError(t, err)
	call := func(key, name string, args map[string]any) string {
		t.Helper()
		request := modernRequest(key, modernToolCall(name, args))
		request.Header.Set("Mcp-Name", name)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		return response.Body.String()
	}
	aSources := call(fixture.handlers.apiKey, "list_datasources", map[string]any{})
	bSources := call(keyB, "list_datasources", map[string]any{})
	require.Contains(t, aSources, "ds-allowed")
	require.NotContains(t, aSources, "ds-b")
	require.Contains(t, bSources, "ds-b")
	require.NotContains(t, bSources, "ds-allowed")
	queryB := map[string]any{"datasource_id": "ds-b", "sql": "SELECT phone FROM public.customers LIMIT 1"}
	require.Contains(t, call(fixture.handlers.apiKey, "query", queryB), `"decision":"deny"`)
	require.Contains(t, call(keyB, "query", queryB), `"decision":"allow"`)
	bAudit, err := fixture.runtime.Store.AuditLogs().Page(tenantB, 1, 10)
	require.NoError(t, err)
	require.Positive(t, bAudit.Total)
	for _, entry := range bAudit.List {
		require.Equal(t, "tenant_b", entry.TenantID)
		require.Equal(t, &agentB.ID, entry.AgentID)
	}
}

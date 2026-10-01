package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	executor "github.com/cuipengdba/agentsql/internal/authorizedexecute"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type hardeningRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  struct {
		Content           json.RawMessage `json:"content"`
		IsError           bool            `json:"isError"`
		StructuredContent struct {
			Decision   string          `json:"decision"`
			ErrorCode  string          `json:"error_code"`
			ErrorStage string          `json:"error_stage"`
			Reason     string          `json:"reason"`
			Suggestion string          `json:"suggestion"`
			Data       json.RawMessage `json:"data"`
		} `json:"structuredContent"`
	} `json:"result"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func TestHTTPClassifiedDatabaseErrorUsesToolErrorContract(t *testing.T) {
	fixture := newMCPFixture(t, "dml")
	fixture.executor.explainErr = classifiedMCPError(
		executor.DBErrorKindObjectNotFound,
		executor.DBErrorCodeObjectNotFound,
		executor.DBStageExplain,
	)
	handler, _, err := newHTTPHandlerWithRegistry(fixture.runtime, httpTestConfig(100), zerolog.Nop())
	require.NoError(t, err)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	status, body, err := doMCPRequest(
		server.Client(), server.URL+"/mcp", fixture.handlers.apiKey, http.MethodPost,
		queryCall(`"db-error"`, "ds-allowed"),
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status, body)
	response := decodeHardeningRPCResponse(t, body)
	require.Nil(t, response.Error)
	require.True(t, response.Result.IsError)
	require.Equal(t, "error", response.Result.StructuredContent.Decision)
	require.Equal(t, string(executor.DBErrorCodeObjectNotFound), response.Result.StructuredContent.ErrorCode)
	require.Equal(t, string(executor.DBStageExplain), response.Result.StructuredContent.ErrorStage)
	require.Equal(t, (&executor.DBError{Code: executor.DBErrorCodeObjectNotFound}).Error(), response.Result.StructuredContent.Reason)
	require.Equal(t, executor.Suggestion(executor.DBErrorCodeObjectNotFound), response.Result.StructuredContent.Suggestion)
	require.Empty(t, response.Result.StructuredContent.Data)
	var content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	require.NoError(t, json.Unmarshal(response.Result.Content, &content))
	require.NotEmpty(t, content)
	var textResponse ToolResponse
	require.NoError(t, json.Unmarshal([]byte(content[0].Text), &textResponse))
	require.Equal(t, response.Result.StructuredContent.ErrorCode, textResponse.ErrorCode)
	require.Equal(t, response.Result.StructuredContent.ErrorStage, textResponse.ErrorStage)
	require.Equal(t, response.Result.StructuredContent.Decision, textResponse.Decision)
	for _, secret := range []string{
		"DRIVER_SECRET", "driver message", "detail", "hint", "InternalQuery",
		"postgres://", "db.internal", "5432", "admin", "password", "SELECT_secret", "param=value",
	} {
		require.NotContains(t, body, secret)
	}
}

func TestHTTPQuerySyntaxErrorUsesToolErrorContract(t *testing.T) {
	fixture := newMCPFixture(t, "dml")
	handler, _, err := newHTTPHandlerWithRegistry(fixture.runtime, httpTestConfig(100), zerolog.Nop())
	require.NoError(t, err)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	body := `{"jsonrpc":"2.0","id":"syntax","method":"tools/call","params":{"name":"query","arguments":{"datasource_id":"ds-allowed","sql":"SELECT FROM WHERE"}}}`

	status, responseBody, err := doMCPRequest(
		server.Client(), server.URL+"/mcp", fixture.handlers.apiKey, http.MethodPost, body,
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status, responseBody)
	response := decodeHardeningRPCResponse(t, responseBody)
	require.Nil(t, response.Error)
	require.True(t, response.Result.IsError)
	require.Equal(t, string(model.DecisionError), response.Result.StructuredContent.Decision)
	require.Equal(t, string(executor.DBErrorCodeSyntax), response.Result.StructuredContent.ErrorCode)
	require.Equal(t, string(executor.DBStageParse), response.Result.StructuredContent.ErrorStage)
	require.Equal(t, executor.NewDBError(
		executor.DBErrorKindSyntax,
		executor.DBErrorCodeSyntax,
		executor.DBStageParse,
	).Error(), response.Result.StructuredContent.Reason)
	require.Equal(t, executor.Suggestion(executor.DBErrorCodeSyntax), response.Result.StructuredContent.Suggestion)
	require.Empty(t, response.Result.StructuredContent.Data)
	for _, leak := range []string{"syntax error at or near", "not one parseable statement"} {
		require.NotContains(t, responseBody, leak)
	}

	page, err := fixture.runtime.Store.AuditLogs().Page(context.Background(), 1, 10)
	require.NoError(t, err)
	require.EqualValues(t, 1, page.Total)
	require.Len(t, page.List, 1)
	require.NotNil(t, page.List[0].ErrorCode)
	require.Equal(t, string(executor.DBErrorCodeSyntax), *page.List[0].ErrorCode)
	require.NotNil(t, page.List[0].ErrorMsg)
	require.Equal(t, response.Result.StructuredContent.Reason, *page.List[0].ErrorMsg)
	require.NotContains(t, *page.List[0].ErrorMsg, "syntax error at or near")
}

func TestHTTPMultiTenantTenAgentsFiftySynchronizedRequestsAreIsolated(t *testing.T) {
	const (
		agentCount       = 10
		requestsPerAgent = 5
	)
	fixture := newMCPFixture(t, "dml")
	type tenant struct {
		agentID     string
		key         string
		datasource  string
		executorSpy *mcpSpyExecutor
	}
	tenants := make([]tenant, 0, agentCount)
	executorsByDatasource := make(map[string]executor.Statement, agentCount)
	for index := 0; index < agentCount; index++ {
		agentID := fmt.Sprintf("tenant-agent-%02d", index)
		datasourceID := fmt.Sprintf("ds-tenant-%02d", index)
		plaintext, hash, err := store.GenerateAPIKey()
		require.NoError(t, err)
		_, err = fixture.runtime.Store.Agents().Create(context.Background(), model.Agent{
			ID: agentID, Name: agentID, Status: "active", APIKeyHash: hash, Level: "dml",
		})
		require.NoError(t, err)
		_, err = fixture.runtime.Store.Datasources().Create(context.Background(), model.Datasource{
			ID: datasourceID, Name: datasourceID, DBType: "postgres",
			Host: datasourceID + ".internal", Port: 5432, Database: datasourceID,
			Username: agentID, ConnLimit: 5, StmtTimeoutMS: 5_000, RowLimit: 100,
		}, "database-password-"+datasourceID)
		require.NoError(t, err)
		columns := "id"
		for _, policy := range []model.Policy{
			{ID: "table-" + agentID, AgentID: agentID, DatasourceID: datasourceID, ObjectType: "table", ObjectName: "public.customers", Action: "allow"},
			{ID: "column-" + agentID, AgentID: agentID, DatasourceID: datasourceID, ObjectType: "column", ObjectName: "public.customers", Columns: &columns, Action: "allow"},
		} {
			_, err = fixture.runtime.Store.Policies().Create(context.Background(), policy)
			require.NoError(t, err)
		}
		spy := &mcpSpyExecutor{queryResult: model.QueryResult{
			Columns: []string{"tenant_marker"}, Rows: [][]string{{datasourceID}}, RowCount: 1,
		}}
		executorsByDatasource[datasourceID] = spy
		tenants = append(tenants, tenant{
			agentID: agentID, key: plaintext, datasource: datasourceID, executorSpy: spy,
		})
	}
	fixture.provider.mu.Lock()
	fixture.provider.executorsByDatasource = executorsByDatasource
	fixture.provider.mu.Unlock()

	handler, registry, err := newHTTPHandlerWithRegistry(fixture.runtime, httpTestConfig(1_000), zerolog.Nop())
	require.NoError(t, err)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	type requestCase struct {
		idRaw        string
		kind         string
		tenant       tenant
		wantResponse bool
	}
	type outcome struct {
		requestCase
		status int
		body   string
		err    error
	}
	requests := make([]requestCase, 0, agentCount*requestsPerAgent)
	for tenantIndex, current := range tenants {
		requests = append(requests,
			requestCase{idRaw: fmt.Sprintf("%d", tenantIndex+1), kind: "list", tenant: current, wantResponse: true},
			requestCase{idRaw: fmt.Sprintf("%q", "query-"+current.agentID), kind: "query", tenant: current, wantResponse: true},
			requestCase{idRaw: "null", kind: "list", tenant: current, wantResponse: false},
			requestCase{idRaw: `""`, kind: "query", tenant: current, wantResponse: true},
			requestCase{idRaw: fmt.Sprintf("%d", -(tenantIndex + 1)), kind: "list", tenant: current, wantResponse: true},
		)
	}

	start := make(chan struct{})
	results := make(chan outcome, len(requests))
	var wait sync.WaitGroup
	for _, requestCase := range requests {
		requestCase := requestCase
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			body := listDatasourcesCall(requestCase.idRaw)
			if requestCase.kind == "query" {
				body = queryCall(requestCase.idRaw, requestCase.tenant.datasource)
			}
			status, responseBody, err := doMCPRequest(
				server.Client(), server.URL+"/mcp", requestCase.tenant.key, http.MethodPost, body,
			)
			results <- outcome{requestCase: requestCase, status: status, body: responseBody, err: err}
		}()
	}
	close(start)
	wait.Wait()
	close(results)

	for result := range results {
		require.NoError(t, result.err)
		if !result.wantResponse {
			// go-sdk v1.7.0 collapses a JSON null ID into a missing ID. Keep the
			// observed fail-closed behavior covered pending the production fix.
			require.Equal(t, http.StatusBadRequest, result.status, result.body)
			require.Contains(t, result.body, "missing id")
			assertSafeHTTPError(t, result.body)
			continue
		}
		require.Equal(t, http.StatusOK, result.status, result.body)
		response := decodeHardeningRPCResponse(t, result.body)
		require.Nil(t, response.Error, result.body)
		require.Equal(t, result.idRaw, string(response.ID), "JSON-RPC id type/value changed")
		require.Equal(t, "allow", response.Result.StructuredContent.Decision, result.body)
		if result.kind == "list" {
			var listed []publicDatasource
			require.NoError(t, json.Unmarshal(response.Result.StructuredContent.Data, &listed))
			require.Equal(t, []publicDatasource{{
				ID: result.tenant.datasource, Name: result.tenant.datasource, DBType: "postgres",
			}}, listed)
		} else {
			var data struct {
				Result *model.QueryResult `json:"result"`
			}
			require.NoError(t, json.Unmarshal(response.Result.StructuredContent.Data, &data))
			require.NotNil(t, data.Result)
			require.Equal(t, [][]string{{result.tenant.datasource}}, data.Result.Rows)
		}
		for _, other := range tenants {
			if other.datasource != result.tenant.datasource {
				require.NotContains(t, result.body, `"`+other.datasource+`"`)
			}
		}
	}
	require.Equal(t, agentCount, registry.serverCount())
	require.Equal(t, agentCount, registry.buildCount())
	for _, current := range tenants {
		require.Equal(t, 2, fixture.provider.datasourceCalls(current.datasource))
		calls := current.executorSpy.snapshot()
		require.Equal(t, 2, calls.explain)
		require.Equal(t, 2, calls.query)
		require.Zero(t, calls.execute)
	}

	auditBefore, err := fixture.runtime.Store.AuditLogs().Page(context.Background(), 1, 100)
	require.NoError(t, err)
	require.EqualValues(t, agentCount*2, auditBefore.Total)
	providerCallsBefore := fixture.provider.calls()
	buildsBefore := registry.buildCount()
	for index, current := range tenants {
		otherDatasource := tenants[(index+1)%len(tenants)].datasource
		idRaw := fmt.Sprintf("%q", "deny-"+current.agentID)
		body := fmt.Sprintf(
			`{"jsonrpc":"2.0","id":%s,"method":"tools/call","params":{"name":"list_schema","arguments":{"datasource_id":%q}}}`,
			idRaw,
			otherDatasource,
		)
		status, responseBody, err := doMCPRequest(server.Client(), server.URL+"/mcp", current.key, http.MethodPost, body)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, status, responseBody)
		response := decodeHardeningRPCResponse(t, responseBody)
		require.Equal(t, idRaw, string(response.ID))
		require.Equal(t, "deny", response.Result.StructuredContent.Decision)
		require.NotContains(t, responseBody, otherDatasource+".internal")
	}
	require.Equal(t, agentCount, registry.serverCount())
	require.Equal(t, buildsBefore, registry.buildCount(), "cached bound servers must be reused")
	require.Equal(t, providerCallsBefore, fixture.provider.calls(), "unauthorized datasource must fail before executor lookup")
	auditAfter, err := fixture.runtime.Store.AuditLogs().Page(context.Background(), 1, 100)
	require.NoError(t, err)
	require.Equal(t, auditBefore.Total, auditAfter.Total)
}

func TestHTTPBodyLimitAcceptsExactlyFourMiBAndRejectsOneByteMore(t *testing.T) {
	fixture := newMCPFixture(t, "dml")
	handler, _, err := newHTTPHandlerWithRegistry(fixture.runtime, httpTestConfig(100), zerolog.Nop())
	require.NoError(t, err)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	exact := sizedInitializeRequest(t, maxMCPRequestBodyBytes, `"exact-limit"`)
	require.Len(t, []byte(exact), maxMCPRequestBodyBytes)
	status, body, err := doMCPRequest(server.Client(), server.URL+"/mcp", fixture.handlers.apiKey, http.MethodPost, exact)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status, body)
	response := decodeHardeningRPCResponse(t, body)
	require.Equal(t, `"exact-limit"`, string(response.ID))
	require.Nil(t, response.Error, body)

	tooLarge := sizedInitializeRequest(t, maxMCPRequestBodyBytes+1, `"over-limit"`)
	require.Len(t, []byte(tooLarge), maxMCPRequestBodyBytes+1)
	status, body, err = doMCPRequest(server.Client(), server.URL+"/mcp", fixture.handlers.apiKey, http.MethodPost, tooLarge)
	require.NoError(t, err)
	require.Equal(t, http.StatusRequestEntityTooLarge, status, body)
	require.NotContains(t, strings.ToLower(body), "goroutine")
	require.Zero(t, fixture.provider.calls())
	auditPage, err := fixture.runtime.Store.AuditLogs().Page(context.Background(), 1, 1)
	require.NoError(t, err)
	require.Zero(t, auditPage.Total)

	status, body, err = doMCPRequest(server.Client(), server.URL+"/mcp", fixture.handlers.apiKey, http.MethodPost, listSourcesRequest)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status, body)
	require.Contains(t, body, "ds-allowed")
}

func TestHTTPMalformedInputsFailClosedAndNextRequestSucceeds(t *testing.T) {
	fixture := newMCPFixture(t, "dml")
	handler, _, err := newHTTPHandlerWithRegistry(fixture.runtime, httpTestConfig(100), zerolog.Nop())
	require.NoError(t, err)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client := &http.Client{Transport: &http.Transport{MaxConnsPerHost: 1, MaxIdleConnsPerHost: 1}}
	t.Cleanup(client.CloseIdleConnections)

	tests := []struct {
		name        string
		contentType string
		body        string
		wantStatus  int
		wantJSON    bool
	}{
		{name: "truncated JSON", contentType: "application/json", body: `{"jsonrpc":"2.0","id":"truncated"`, wantStatus: http.StatusBadRequest},
		{name: "illegal JSON", contentType: "application/json", body: `{not-json}`, wantStatus: http.StatusBadRequest},
		{name: "wrong Content-Type", contentType: "text/plain", body: listToolsRequest, wantStatus: http.StatusUnsupportedMediaType},
		{name: "invalid JSON-RPC version", contentType: "application/json", body: `{"jsonrpc":"1.0","id":"invalid","method":"tools/list","params":{}}`, wantStatus: http.StatusBadRequest},
		{name: "invalid empty method", contentType: "application/json", body: `{"jsonrpc":"2.0","id":"empty-method","method":"","params":{}}`, wantStatus: http.StatusBadRequest},
		{name: "invalid tool arguments", contentType: "application/json", body: `{"jsonrpc":"2.0","id":"bad-arguments","method":"tools/call","params":{"name":"list_datasources","arguments":"bad"}}`, wantStatus: http.StatusOK, wantJSON: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			status, contentType, responseBody, err := doRawMCPRequest(
				client, server.URL+"/mcp", fixture.handlers.apiKey, test.contentType, test.body,
			)
			require.NoError(t, err)
			require.Equal(t, test.wantStatus, status, responseBody)
			assertSafeHTTPError(t, responseBody)
			if test.wantJSON {
				require.Contains(t, contentType, "application/json")
				response := decodeHardeningRPCResponse(t, responseBody)
				if response.Error != nil {
					require.NotZero(t, response.Error.Code)
					require.NotEmpty(t, response.Error.Message)
				} else {
					require.True(t, response.Result.IsError, responseBody)
					require.NotEmpty(t, response.Result.Content, responseBody)
				}
			} else {
				// Transport-level errors currently come directly from go-sdk's
				// http.Error path. Their text/plain shape is reported as POSSIBLE_BUG.
				require.Contains(t, contentType, "text/plain")
				require.NotEmpty(t, strings.TrimSpace(responseBody))
			}
		})
	}

	require.Zero(t, fixture.provider.calls())
	auditPage, err := fixture.runtime.Store.AuditLogs().Page(context.Background(), 1, 1)
	require.NoError(t, err)
	require.Zero(t, auditPage.Total)

	valid := queryCall(`"recovered"`, "ds-allowed")
	status, _, body, err := doRawMCPRequest(
		client, server.URL+"/mcp", fixture.handlers.apiKey, "application/json", valid,
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status, body)
	response := decodeHardeningRPCResponse(t, body)
	require.Equal(t, `"recovered"`, string(response.ID))
	require.Equal(t, "allow", response.Result.StructuredContent.Decision)
	require.Equal(t, 1, fixture.provider.calls())
	auditPage, err = fixture.runtime.Store.AuditLogs().Page(context.Background(), 1, 1)
	require.NoError(t, err)
	require.EqualValues(t, 1, auditPage.Total)
}

func listDatasourcesCall(idRaw string) string {
	return fmt.Sprintf(
		`{"jsonrpc":"2.0","id":%s,"method":"tools/call","params":{"name":"list_datasources","arguments":{}}}`,
		idRaw,
	)
}

func queryCall(idRaw, datasourceID string) string {
	return fmt.Sprintf(
		`{"jsonrpc":"2.0","id":%s,"method":"tools/call","params":{"name":"query","arguments":{"datasource_id":%q,"sql":"SELECT id FROM public.customers WHERE id=1 LIMIT 1"}}}`,
		idRaw,
		datasourceID,
	)
}

func sizedInitializeRequest(t *testing.T, target int, idRaw string) string {
	t.Helper()
	prefix := fmt.Sprintf(
		`{"jsonrpc":"2.0","id":%s,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"boundary","version":"`,
		idRaw,
	)
	suffix := `"}}}`
	require.GreaterOrEqual(t, target, len(prefix)+len(suffix))
	return prefix + strings.Repeat("x", target-len(prefix)-len(suffix)) + suffix
}

func decodeHardeningRPCResponse(t *testing.T, body string) hardeningRPCResponse {
	t.Helper()
	var response hardeningRPCResponse
	require.NoError(t, json.Unmarshal([]byte(body), &response), body)
	require.Equal(t, "2.0", response.JSONRPC, body)
	return response
}

func doRawMCPRequest(
	client *http.Client,
	url string,
	key string,
	contentType string,
	body string,
) (int, string, string, error) {
	request, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		return 0, "", "", err
	}
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("MCP-Protocol-Version", "2025-06-18")
	response, err := client.Do(request)
	if err != nil {
		return 0, "", "", err
	}
	defer response.Body.Close()
	contents, err := io.ReadAll(response.Body)
	if err != nil {
		return 0, "", "", err
	}
	return response.StatusCode, response.Header.Get("Content-Type"), string(contents), nil
}

func assertSafeHTTPError(t *testing.T, body string) {
	t.Helper()
	lower := strings.ToLower(body)
	for _, forbidden := range []string{
		"goroutine ", "runtime/debug", ".go:", "database-password", "db.internal", "asql_",
	} {
		require.NotContains(t, lower, strings.ToLower(forbidden))
	}
}

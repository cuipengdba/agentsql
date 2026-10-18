package mcpserver

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/audit"
	"github.com/cuipengdba/agentsql/internal/auth"
	executor "github.com/cuipengdba/agentsql/internal/authorizedexecute"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/pipeline"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestHighGoV9010MCPRealE2E(t *testing.T) {
	host := os.Getenv("AGENTSQL_TEST_HIGHGO_HOST")
	password := os.Getenv("HIGHGO_RO_PASSWORD")
	if host == "" || password == "" {
		t.Skip("set AGENTSQL_TEST_HIGHGO_HOST and HIGHGO_RO_PASSWORD for isolated HighGo testing")
	}
	clientDir := os.Getenv("AGENTSQL_TEST_HIGHGO_CLIENT_DIR")
	permissionDir := os.Getenv("AGENTSQL_TEST_HIGHGO_PERMISSION_DIR")
	faultDir := os.Getenv("AGENTSQL_TEST_HIGHGO_FAULT_DIR")
	timeout := 30 * time.Second
	if clientDir != "" || permissionDir != "" || faultDir != "" {
		timeout = 8 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	fixture := newMCPFixture(t, "readonly")
	const datasourceID = "ds-highgo-v9010"
	const table = "public.agentsql_v170_verify"
	const allowSQL = "SELECT phone FROM " + table + " WHERE id = 1 LIMIT 1"
	const denySQL = "SELECT id FROM public.agentsql_v170_ungranted WHERE id = 1 LIMIT 1"
	_, err := fixture.runtime.Store.Datasources().Create(ctx, model.Datasource{
		ID: datasourceID, Name: "HighGo 9.0.10 PG test", DBType: "highgo", Host: host, Port: 5866,
		Database: "highgo", Username: "agentsql_v170_ro", ConnLimit: 2,
		StmtTimeoutMS: 5_000, RowLimit: 10,
	}, password)
	require.NoError(t, err)
	policy := model.Policy{ID: "highgo-table-read", AgentID: fixture.agent.ID,
		DatasourceID: datasourceID, ObjectType: "table", ObjectName: table, Action: "allow"}
	_, err = fixture.runtime.Store.Policies().Create(ctx, policy)
	require.NoError(t, err)
	gateway := executor.NewGateway(true)
	t.Cleanup(func() { require.NoError(t, gateway.CloseAll()) })
	fixture.runtime.Pipeline, err = pipeline.New(pipeline.Ports{
		Authenticator: auth.NewAuthenticator(fixture.runtime.Store.Agents()),
		Datasources:   fixture.runtime.Store.Datasources(), Policies: fixture.runtime.Store.Policies(),
		Executors: gateway, Approvals: fixture.runtime.Store.Approvals(),
		Audit: audit.NewRecorder(fixture.runtime.Store.AuditLogs()), Redactors: fixture.redactors,
	}, mcpTestSecret)
	require.NoError(t, err)
	server, err := NewServer(ctx, Options{APIKey: fixture.handlers.apiKey,
		Runtime: fixture.runtime, Logger: zerolog.Nop()})
	require.NoError(t, err)
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverErrors := make(chan error, 1)
	go func() { serverErrors <- server.sdk.Run(ctx, serverTransport) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "highgo-v9010-e2e", Version: "test"}, nil)
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
	call := func(name, sql string) string {
		t.Helper()
		arguments := map[string]any{"datasource_id": datasourceID, "sql": sql}
		if name == "execute_write" {
			arguments["reason"] = "verify read-only enforcement"
		}
		result, callErr := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: arguments})
		require.NoError(t, callErr)
		encoded, marshalErr := json.Marshal(result)
		require.NoError(t, marshalErr)
		return string(encoded)
	}
	allowed := call("query", allowSQL)
	require.Contains(t, allowed, `"decision":"allow"`)
	require.Contains(t, allowed, "138****5678")
	require.NotContains(t, allowed, "13812345678")
	denied := call("query", denySQL)
	require.Contains(t, denied, `"decision":"deny"`)
	unsafe := call("execute_write", "UPDATE "+table+" SET phone='changed' WHERE id=1")
	require.NotContains(t, unsafe, `"decision":"allow"`)
	page, err := fixture.runtime.Store.AuditLogs().Page(ctx, 1, 10)
	require.NoError(t, err)
	require.Equal(t, int64(3), page.Total)
	require.Equal(t, "allow", page.List[2].Decision)
	require.Equal(t, "deny", page.List[1].Decision)
	require.NoError(t, fixture.runtime.Store.Policies().Delete(ctx, policy.ID))
	revoked := call("query", allowSQL)
	require.Contains(t, revoked, `"decision":"deny"`)
	require.NotContains(t, revoked, "13812345678")
	_, err = fixture.runtime.Store.Policies().Create(ctx, policy)
	require.NoError(t, err)
	recovered := call("query", allowSQL)
	require.Contains(t, recovered, "138****5678")
	require.NotContains(t, recovered, "13812345678")
	signal := func(dir, name string) {
		t.Helper()
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("ready\n"), 0o600))
	}
	waitSignal := func(dir, name string) {
		t.Helper()
		for {
			_, statErr := os.Stat(filepath.Join(dir, name))
			if statErr == nil {
				return
			}
			if !os.IsNotExist(statErr) {
				require.NoError(t, statErr)
			}
			select {
			case <-ctx.Done():
				t.Fatal("timed out waiting for HighGo fault signal: " + name)
			case <-time.After(250 * time.Millisecond):
			}
		}
	}
	if permissionDir != "" {
		signal(permissionDir, "ready")
		waitSignal(permissionDir, "revoked")
		revokedGrant := call("query", allowSQL)
		require.NotContains(t, revokedGrant, `"decision":"allow"`)
		require.NotContains(t, revokedGrant, "13812345678")
		failureAudit, pageErr := fixture.runtime.Store.AuditLogs().Page(ctx, 1, 1)
		require.NoError(t, pageErr)
		require.Equal(t, "error", failureAudit.List[0].Decision)
		require.NotNil(t, failureAudit.List[0].ErrorCode)
		signal(permissionDir, "revoked_checked")
		waitSignal(permissionDir, "restored")
		recoveredGrant := call("query", allowSQL)
		require.Contains(t, recoveredGrant, `"decision":"allow"`)
		require.Contains(t, recoveredGrant, "138****5678")
		require.NotContains(t, recoveredGrant, "13812345678")
	}
	if faultDir != "" {
		signal(faultDir, "ready")
		waitSignal(faultDir, "down")
		outageResult, outageErr := session.CallTool(ctx, &mcp.CallToolParams{
			Name: "query", Arguments: map[string]any{"datasource_id": datasourceID, "sql": allowSQL},
		})
		if outageErr == nil {
			encoded, marshalErr := json.Marshal(outageResult)
			require.NoError(t, marshalErr)
			require.NotContains(t, string(encoded), `"decision":"allow"`)
			require.NotContains(t, string(encoded), "13812345678")
		}
		failureAudit, pageErr := fixture.runtime.Store.AuditLogs().Page(ctx, 1, 1)
		require.NoError(t, pageErr)
		require.Equal(t, "error", failureAudit.List[0].Decision)
		require.NotNil(t, failureAudit.List[0].ErrorCode)
		signal(faultDir, "down_checked")
		waitSignal(faultDir, "up")
		recoveredServer := call("query", allowSQL)
		require.Contains(t, recoveredServer, `"decision":"allow"`)
		require.Contains(t, recoveredServer, "138****5678")
		require.NotContains(t, recoveredServer, "13812345678")
	}
	if clientDir == "" {
		return
	}
	beforeClients, err := fixture.runtime.Store.AuditLogs().Page(ctx, 1, 30)
	require.NoError(t, err)
	handler, err := NewHTTPHandler(fixture.runtime, statefulHTTPTestConfig(100), zerolog.Nop())
	require.NoError(t, err)
	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()
	clientInfo, err := json.Marshal(map[string]string{
		"url": httpServer.URL + "/mcp", "api_key": fixture.handlers.apiKey,
		"datasource_id": datasourceID, "allow_sql": allowSQL,
		"deny_sql": denySQL, "deny_rule": "R010",
	})
	require.NoError(t, err)
	infoPath := filepath.Join(clientDir, "client.json")
	require.NoError(t, os.WriteFile(infoPath, clientInfo, 0o600))
	defer os.Remove(infoPath)
	for {
		if _, statErr := os.Stat(filepath.Join(clientDir, "clients_done")); statErr == nil {
			break
		} else if !os.IsNotExist(statErr) {
			require.NoError(t, statErr)
		}
		select {
		case <-ctx.Done():
			t.Fatal("timed out waiting for Inspector and Dify MCP clients")
		case <-time.After(250 * time.Millisecond):
		}
	}
	afterClients, err := fixture.runtime.Store.AuditLogs().Page(ctx, 1, 30)
	require.NoError(t, err)
	require.Equal(t, beforeClients.Total+4, afterClients.Total)
}

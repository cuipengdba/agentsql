package mcpserver

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
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

func TestVastbaseBuild9MCPRealE2E(t *testing.T) {
	host := os.Getenv("AGENTSQL_TEST_G100_HOST")
	if host == "" {
		t.Skip("set AGENTSQL_TEST_G100_HOST for seeded G100 Build 9 PG-mode testing")
	}
	password := os.Getenv("AGENTSQL_TEST_G100_PASSWORD")
	require.NotEmpty(t, password)
	port := 5432
	if raw := os.Getenv("AGENTSQL_TEST_G100_PORT"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		require.NoError(t, err)
		port = parsed
	}
	fixture := newMCPFixture(t, "readonly")
	testTimeout := 30 * time.Second
	faultDir := os.Getenv("AGENTSQL_TEST_G100_FAULT_DIR")
	clientDir := os.Getenv("AGENTSQL_TEST_G100_CLIENT_DIR")
	permissionDir := os.Getenv("AGENTSQL_TEST_G100_PERMISSION_DIR")
	boundedLoad := os.Getenv("AGENTSQL_TEST_G100_BOUNDED_LOAD") == "1"
	if faultDir != "" || clientDir != "" || permissionDir != "" || boundedLoad {
		testTimeout = 8 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	_, err := fixture.runtime.Store.Datasources().Create(ctx, model.Datasource{
		ID: "ds-g100", Name: "G100 Build 9 test", DBType: "vastbase", Host: host, Port: port,
		Database: "vastbase", Username: "agentsql_ro", ConnLimit: 2,
		StmtTimeoutMS: 5_000, RowLimit: 10,
	}, password)
	require.NoError(t, err)
	_, err = fixture.runtime.Store.Policies().Create(ctx, model.Policy{
		ID: "g100-contact-table", AgentID: fixture.agent.ID, DatasourceID: "ds-g100",
		ObjectType: "table", ObjectName: "agentsql_v060.contacts", Action: "allow",
	})
	require.NoError(t, err)
	gateway := executor.NewGateway(true)
	t.Cleanup(func() { require.NoError(t, gateway.CloseAll()) })
	fixture.runtime.Pipeline, err = pipeline.New(pipeline.Ports{
		Authenticator: auth.NewAuthenticator(fixture.runtime.Store.Agents()),
		Datasources:   fixture.runtime.Store.Datasources(),
		Policies:      fixture.runtime.Store.Policies(),
		Executors:     gateway,
		Approvals:     fixture.runtime.Store.Approvals(),
		Audit:         audit.NewRecorder(fixture.runtime.Store.AuditLogs()),
		Redactors:     fixture.redactors,
	}, mcpTestSecret)
	require.NoError(t, err)
	logger := zerolog.Nop()
	if faultDir != "" {
		logger = zerolog.New(os.Stderr)
	}
	server, err := NewServer(ctx, Options{
		APIKey: fixture.handlers.apiKey, Runtime: fixture.runtime, Logger: logger,
	})
	require.NoError(t, err)
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverErrors := make(chan error, 1)
	go func() { serverErrors <- server.sdk.Run(ctx, serverTransport) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "g100-e2e", Version: "test"}, nil)
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
		arguments := map[string]any{"datasource_id": "ds-g100", "sql": sql}
		if name == "execute_write" || name == "request_approval" {
			arguments["reason"] = "verify read-only enforcement"
		}
		result, callErr := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: arguments})
		require.NoError(t, callErr)
		encoded, marshalErr := json.Marshal(result)
		require.NoError(t, marshalErr)
		return string(encoded)
	}
	allowed := call("query", "SELECT phone FROM agentsql_v060.contacts WHERE id = 1 LIMIT 1")
	require.Contains(t, allowed, "138****5678")
	require.NotContains(t, allowed, "13812345678")
	denied := call("query", "SELECT secret FROM agentsql_v060.customers WHERE id = 1 LIMIT 1")
	require.Contains(t, denied, `"decision":"deny"`)
	write := call("execute_write", "UPDATE agentsql_v060.contacts SET phone = 'changed' WHERE id = 1")
	require.Contains(t, write, `"decision":"error"`)
	require.Contains(t, write, "DB_SYNTAX_ERROR")
	unchanged := call("query", "SELECT phone FROM agentsql_v060.contacts WHERE id = 1 LIMIT 1")
	require.Contains(t, unchanged, "138****5678")
	page, err := fixture.runtime.Store.AuditLogs().Page(ctx, 1, 10)
	require.NoError(t, err)
	require.Equal(t, int64(4), page.Total)
	require.Equal(t, "allow", page.List[3].Decision)
	require.Equal(t, "deny", page.List[2].Decision)
	require.Equal(t, "error", page.List[1].Decision)
	requested := call("request_approval", "SELECT phone FROM agentsql_v060.contacts WHERE id = 1 LIMIT 1")
	require.Contains(t, requested, `"decision":"approve"`)
	approvals, err := fixture.runtime.Store.Approvals().ListPage(ctx, "pending", 1, 10)
	require.NoError(t, err)
	require.Len(t, approvals.List, 1)
	approval := approvals.List[0]
	require.NotNil(t, approval.Seal)
	require.NotNil(t, approval.DatasourceFingerprint)
	_, err = fixture.runtime.Store.Approvals().DecidePending(ctx, approval.ID, "approved", "test-dba", nil, time.Now().UTC())
	require.NoError(t, err)
	execute := func() string {
		t.Helper()
		result, callErr := session.CallTool(ctx, &mcp.CallToolParams{
			Name: "execute_approved", Arguments: map[string]any{"approval_id": approval.ID},
		})
		require.NoError(t, callErr)
		encoded, marshalErr := json.Marshal(result)
		require.NoError(t, marshalErr)
		return string(encoded)
	}
	executed := execute()
	require.Contains(t, executed, `"decision":"allow"`)
	require.Contains(t, executed, "138****5678")
	require.NotContains(t, executed, "13812345678")
	completed, err := fixture.runtime.Store.Approvals().Get(ctx, approval.ID)
	require.NoError(t, err)
	require.Equal(t, "completed", completed.Status)
	require.NotNil(t, completed.ExecutionAuditID)
	require.Contains(t, execute(), `"decision":"deny"`)
	createApproved := func() model.Approval {
		t.Helper()
		require.Contains(t, call("request_approval", "SELECT phone FROM agentsql_v060.contacts WHERE id = 1 LIMIT 1"), `"decision":"approve"`)
		pending, listErr := fixture.runtime.Store.Approvals().ListPage(ctx, "pending", 1, 10)
		require.NoError(t, listErr)
		require.Len(t, pending.List, 1)
		item := pending.List[0]
		_, decideErr := fixture.runtime.Store.Approvals().DecidePending(ctx, item.ID, "approved", "test-dba", nil, time.Now().UTC())
		require.NoError(t, decideErr)
		return item
	}
	approval = createApproved()
	datasource, err := fixture.runtime.Store.Datasources().Get(ctx, "ds-g100")
	require.NoError(t, err)
	datasource.RowLimit = 9
	_, err = fixture.runtime.Store.Datasources().Update(ctx, datasource, password)
	require.NoError(t, err)
	require.Contains(t, execute(), `"decision":"error"`)
	changed, err := fixture.runtime.Store.Approvals().Get(ctx, approval.ID)
	require.NoError(t, err)
	require.Equal(t, "unknown", changed.Status)
	datasource.RowLimit = 10
	_, err = fixture.runtime.Store.Datasources().Update(ctx, datasource, password)
	require.NoError(t, err)
	approval = createApproved()
	require.NoError(t, fixture.runtime.Store.Policies().Delete(ctx, "g100-contact-table"))
	require.Contains(t, execute(), `"decision":"deny"`)
	revoked, err := fixture.runtime.Store.Approvals().Get(ctx, approval.ID)
	require.NoError(t, err)
	require.Equal(t, "failed", revoked.Status)
	if clientDir != "" {
		_, err = fixture.runtime.Store.Policies().Create(ctx, model.Policy{
			ID: "g100-contact-table-clients", AgentID: fixture.agent.ID, DatasourceID: "ds-g100",
			ObjectType: "table", ObjectName: "agentsql_v060.contacts", Action: "allow",
		})
		require.NoError(t, err)
		beforeClients, pageErr := fixture.runtime.Store.AuditLogs().Page(ctx, 1, 50)
		require.NoError(t, pageErr)
		handler, handlerErr := NewHTTPHandler(fixture.runtime, statefulHTTPTestConfig(100), logger)
		require.NoError(t, handlerErr)
		httpServer := httptest.NewServer(handler)
		defer httpServer.Close()
		clientInfo, marshalErr := json.Marshal(map[string]string{
			"url": httpServer.URL + "/mcp", "api_key": fixture.handlers.apiKey,
			"datasource_id": "ds-g100",
			"allow_sql":     "SELECT phone FROM agentsql_v060.contacts WHERE id = 1 LIMIT 1",
			"deny_sql":      "SELECT secret FROM agentsql_v060.customers WHERE id = 1 LIMIT 1",
			"deny_rule":     "R010",
		})
		require.NoError(t, marshalErr)
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
				t.Fatal("timed out waiting for Vastbase external MCP clients")
			case <-time.After(250 * time.Millisecond):
			}
		}
		afterClients, pageErr := fixture.runtime.Store.AuditLogs().Page(ctx, 1, 50)
		require.NoError(t, pageErr)
		require.Equal(t, beforeClients.Total+4, afterClients.Total)
		t.Log("Vastbase MCP Inspector and Dify calls persisted four additional audit rows")
	}
	if permissionDir != "" {
		if clientDir == "" {
			_, err = fixture.runtime.Store.Policies().Create(ctx, model.Policy{
				ID: "g100-contact-table-permissions", AgentID: fixture.agent.ID, DatasourceID: "ds-g100",
				ObjectType: "table", ObjectName: "agentsql_v060.contacts", Action: "allow",
			})
			require.NoError(t, err)
		}
		signalPermission := func(name string) {
			t.Helper()
			require.NoError(t, os.WriteFile(filepath.Join(permissionDir, name), []byte("ready\n"), 0o600))
		}
		waitPermission := func(name string) {
			t.Helper()
			path := filepath.Join(permissionDir, name)
			for {
				if _, statErr := os.Stat(path); statErr == nil {
					return
				} else if !os.IsNotExist(statErr) {
					require.NoError(t, statErr)
				}
				select {
				case <-ctx.Done():
					t.Fatal("timed out waiting for Vastbase permission signal: " + name)
				case <-time.After(250 * time.Millisecond):
				}
			}
		}
		signalPermission("ready")
		waitPermission("revoked")
		revokedResult, revokedErr := session.CallTool(ctx, &mcp.CallToolParams{
			Name: "query", Arguments: map[string]any{
				"datasource_id": "ds-g100", "sql": "SELECT phone FROM agentsql_v060.contacts WHERE id = 1 LIMIT 1",
			},
		})
		if revokedErr == nil {
			encoded, marshalErr := json.Marshal(revokedResult)
			require.NoError(t, marshalErr)
			require.NotContains(t, string(encoded), "13812345678")
			require.NotContains(t, string(encoded), `"decision":"allow"`)
		} else {
			t.Logf("Vastbase revoked SELECT failed at MCP transport: %v", revokedErr)
		}
		signalPermission("revoked_checked")
		waitPermission("restored")
		recoveredGrant := call("query", "SELECT phone FROM agentsql_v060.contacts WHERE id = 1 LIMIT 1")
		require.Contains(t, recoveredGrant, `"decision":"allow"`)
		require.Contains(t, recoveredGrant, "138****5678")
		require.NotContains(t, recoveredGrant, "13812345678")
		page, err = fixture.runtime.Store.AuditLogs().Page(ctx, 1, 20)
		require.NoError(t, err)
		require.Equal(t, "allow", page.List[0].Decision)
		t.Log("Vastbase SELECT grant revocation failed closed and recovery persisted a masked result")
	}
	if boundedLoad {
		_, err = fixture.runtime.Store.Policies().Create(ctx, model.Policy{
			ID: "g100-contact-table-load", AgentID: fixture.agent.ID, DatasourceID: "ds-g100",
			ObjectType: "table", ObjectName: "agentsql_v060.contacts", Action: "allow",
		})
		require.NoError(t, err)
		before, pageErr := fixture.runtime.Store.AuditLogs().Page(ctx, 1, 1)
		require.NoError(t, pageErr)
		type loadResult struct {
			elapsed time.Duration
			body    string
			err     error
		}
		results := make(chan loadResult, 12)
		active := make(chan struct{}, 2)
		var waiting sync.WaitGroup
		for range 12 {
			waiting.Add(1)
			go func() {
				defer waiting.Done()
				active <- struct{}{}
				defer func() { <-active }()
				started := time.Now()
				response, callErr := session.CallTool(ctx, &mcp.CallToolParams{Name: "query", Arguments: map[string]any{
					"datasource_id": "ds-g100", "sql": "SELECT phone FROM agentsql_v060.contacts WHERE id = 1 LIMIT 1",
				}})
				encoded, marshalErr := json.Marshal(response)
				if callErr == nil {
					callErr = marshalErr
				}
				results <- loadResult{elapsed: time.Since(started), body: string(encoded), err: callErr}
			}()
		}
		waiting.Wait()
		close(results)
		latencies := make([]time.Duration, 0, 12)
		for result := range results {
			require.NoError(t, result.err)
			require.Contains(t, result.body, `"decision":"allow"`)
			require.Contains(t, result.body, "138****5678")
			require.NotContains(t, result.body, "13812345678")
			latencies = append(latencies, result.elapsed)
		}
		require.Len(t, latencies, 12)
		after, pageErr := fixture.runtime.Store.AuditLogs().Page(ctx, 1, 1)
		require.NoError(t, pageErr)
		require.EqualValues(t, 12, after.Total-before.Total)
		sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
		t.Logf("Vastbase bounded load: 12/12 masked, audited; concurrency=2 pool=2 p50=%s p95=%s max=%s",
			latencies[5], latencies[11], latencies[11])
	}
	if faultDir == "" {
		return
	}
	_, err = fixture.runtime.Store.Policies().Create(ctx, model.Policy{
		ID: "g100-contact-table-recovery", AgentID: fixture.agent.ID, DatasourceID: "ds-g100",
		ObjectType: "table", ObjectName: "agentsql_v060.contacts", Action: "allow",
	})
	require.NoError(t, err)
	signal := func(name string) {
		t.Helper()
		require.NoError(t, os.WriteFile(filepath.Join(faultDir, name), []byte("ready\n"), 0o600))
	}
	waitSignal := func(name string) {
		t.Helper()
		path := filepath.Join(faultDir, name)
		for {
			if _, statErr := os.Stat(path); statErr == nil {
				return
			} else if !os.IsNotExist(statErr) {
				require.NoError(t, statErr)
			}
			select {
			case <-ctx.Done():
				t.Fatal("timed out waiting for Vastbase fault signal: " + name)
			case <-time.After(250 * time.Millisecond):
			}
		}
	}
	signal("ready")
	waitSignal("down")
	outageResult, outageErr := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "query", Arguments: map[string]any{
			"datasource_id": "ds-g100", "sql": "SELECT phone FROM agentsql_v060.contacts WHERE id = 1 LIMIT 1",
		},
	})
	if outageErr == nil {
		outage, marshalErr := json.Marshal(outageResult)
		require.NoError(t, marshalErr)
		require.NotContains(t, string(outage), "13812345678")
		require.NotContains(t, string(outage), `"decision":"allow"`)
	} else {
		t.Logf("Vastbase outage failed at MCP transport: %v", outageErr)
	}
	t.Log("Vastbase server outage did not return an allowed sensitive result")
	signal("down_checked")
	waitSignal("up")
	recovered := call("query", "SELECT phone FROM agentsql_v060.contacts WHERE id = 1 LIMIT 1")
	require.Contains(t, recovered, `"decision":"allow"`)
	require.Contains(t, recovered, "138****5678")
	require.NotContains(t, recovered, "13812345678")
	page, err = fixture.runtime.Store.AuditLogs().Page(ctx, 1, 20)
	require.NoError(t, err)
	require.Equal(t, "allow", page.List[0].Decision)
	t.Log("Vastbase server recovery returned a masked result with persisted audit")
}

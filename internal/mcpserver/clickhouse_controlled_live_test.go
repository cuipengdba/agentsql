package mcpserver

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/bootstrap"
	"github.com/cuipengdba/agentsql/internal/config"
	"github.com/cuipengdba/agentsql/internal/mask"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// This test reaches the production runtime over HTTP MCP against an isolated
// ClickHouse 26.8 TLS instance with only synthetic data.
func TestClickHouseControlledRealHTTPMCP(t *testing.T) {
	host := os.Getenv("AGENTSQL_TEST_CLICKHOUSE_HOST")
	if host == "" {
		t.Skip("set AGENTSQL_TEST_CLICKHOUSE_HOST for isolated ClickHouse HTTP MCP testing")
	}
	password := os.Getenv("AGENTSQL_TEST_CLICKHOUSE_RO_PASSWORD")
	ca := os.Getenv("AGENTSQL_TEST_CLICKHOUSE_CA")
	require.NotEmpty(t, password)
	require.NotEmpty(t, ca)
	port := 9440
	if raw := os.Getenv("AGENTSQL_TEST_CLICKHOUSE_PORT"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		require.NoError(t, err)
		port = parsed
	}
	clientDir := os.Getenv("AGENTSQL_TEST_CLICKHOUSE_CLIENT_DIR")
	auditDSN := os.Getenv("AGENTSQL_TEST_CLICKHOUSE_AUDIT_DSN")
	faultDir := os.Getenv("AGENTSQL_TEST_CLICKHOUSE_FAULT_DIR")
	if faultDir != "" {
		require.NotEmpty(t, auditDSN, "fault testing requires a separate audit database")
	}
	testTimeout := 90 * time.Second
	if clientDir != "" {
		testTimeout = 10 * time.Minute
	} else if faultDir != "" {
		testTimeout = 8 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	cfg := httpTestConfig(100)
	cfg.Store.SQLitePath = filepath.Join(t.TempDir(), "clickhouse-mcp.db")
	if auditDSN != "" {
		cfg.Store.Audit = &config.AuditStoreConfig{Separate: true, Driver: "postgres", DSN: auditDSN}
	}
	runtime, err := bootstrap.Assemble(ctx, cfg, mcpTestSecret)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, runtime.Close()) })
	key, hash, err := store.GenerateAPIKey()
	require.NoError(t, err)
	agent, err := runtime.Store.Agents().Create(ctx, model.Agent{ID: "clickhouse-http-agent", Name: "ClickHouse HTTP Agent",
		Status: "active", Level: "readonly", APIKeyHash: hash})
	require.NoError(t, err)
	datasource, err := runtime.Store.Datasources().Create(ctx, model.Datasource{ID: "clickhouse-http-datasource",
		Name: "ClickHouse HTTP test", DBType: "clickhouse", Host: host, Port: port, Database: "analytics",
		Username: "agent_reader", TLSMode: "strict", TLSCAFile: ca, TLSServerName: "clickhouse-v270",
		StmtTimeoutMS: 2000, ConnLimit: 1, RowLimit: 100,
		ReadControlJSON: os.Getenv("AGENTSQL_TEST_CLICKHOUSE_CONTROL")}, password)
	require.NoError(t, err)
	grant, err := runtime.Store.Policies().Create(ctx, model.Policy{ID: "clickhouse-http-table",
		AgentID: agent.ID, DatasourceID: datasource.ID, ObjectType: "table", ObjectName: "analytics.events", Action: "allow"})
	require.NoError(t, err)
	columns := "id,name,phone,region"
	_, err = runtime.Store.Policies().Create(ctx, model.Policy{ID: "clickhouse-http-columns",
		AgentID: agent.ID, DatasourceID: datasource.ID, ObjectType: "column", ObjectName: "analytics.events",
		Columns: &columns, Action: "allow"})
	require.NoError(t, err)
	_, err = runtime.Store.MaskRules().Create(ctx, model.MaskRule{ID: "clickhouse-http-mask",
		DatasourceID: &datasource.ID, SchemaName: "analytics", TableName: "events", ColumnName: "phone",
		SensitiveType: string(mask.TypePhone), Algo: string(mask.AlgoMask), Enabled: true})
	require.NoError(t, err)
	handler, err := NewHTTPHandler(runtime, cfg, zerolog.Nop())
	require.NoError(t, err)
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)
	httpClient := httpServer.Client()
	httpClient.Transport = bearerTransport{base: httpClient.Transport, key: key}
	client := mcp.NewClient(&mcp.Implementation{Name: "clickhouse-http-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: httpServer.URL + "/mcp", HTTPClient: httpClient}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = session.Close() })
	call := func(table string, fields []string, filter map[string]string) string {
		t.Helper()
		response, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "query_analytics", Arguments: map[string]any{
			"datasource_id": datasource.ID, "table": table, "fields": fields, "filter": filter, "limit": 2,
		}})
		require.NoError(t, err)
		encoded, err := json.Marshal(response)
		require.NoError(t, err)
		return string(encoded)
	}
	allowed := call("events", []string{"name", "phone"}, map[string]string{"region": "CN"})
	require.Contains(t, allowed, `"decision":"allow"`)
	require.Contains(t, allowed, "138****5678")
	require.NotContains(t, allowed, "13812345678")
	require.NotContains(t, allowed, "synthetic-secret-canary")
	if datasource.ReadControlJSON != "" {
		_, err = runtime.Store.Policies().Create(ctx, model.Policy{ID: "clickhouse-http-budget-table",
			AgentID: agent.ID, DatasourceID: datasource.ID, ObjectType: "table", ObjectName: "analytics.budget_events", Action: "allow"})
		require.NoError(t, err)
		budgetColumns := "name,region"
		_, err = runtime.Store.Policies().Create(ctx, model.Policy{ID: "clickhouse-http-budget-columns",
			AgentID: agent.ID, DatasourceID: datasource.ID, ObjectType: "column", ObjectName: "analytics.budget_events",
			Columns: &budgetColumns, Action: "allow"})
		require.NoError(t, err)
		budgetDenied := call("budget_events", []string{"name"}, map[string]string{"region": "bulk"})
		require.Contains(t, budgetDenied, `"decision":"error"`)
		require.NotContains(t, budgetDenied, "synthetic-secret-canary")
	}
	injected := call("events", []string{"name", "phone"}, map[string]string{"region": "CN' OR '1'='1"})
	require.Contains(t, injected, `"decision":"allow"`)
	require.NotContains(t, injected, "138****5678", "filter values must be bound, not injected into SQL")
	require.Contains(t, call("private_notes", []string{"note"}, map[string]string{"note": "x"}), `"decision":"deny"`)
	require.Contains(t, call("events", []string{"name"}, map[string]string{"$where": "1"}), `"decision":"deny"`)
	badCertificate := datasource
	badCertificate.TLSCAFile = ca + ".missing"
	_, err = runtime.Store.Datasources().Update(ctx, badCertificate, password)
	require.NoError(t, err)
	certificateDenied := call("events", []string{"name", "phone"}, map[string]string{"region": "CN"})
	require.Contains(t, certificateDenied, `"decision":"error"`)
	require.NotContains(t, certificateDenied, "13812345678")
	_, err = runtime.Store.Datasources().Update(ctx, datasource, password)
	require.NoError(t, err)
	require.Contains(t, call("events", []string{"name", "phone"}, map[string]string{"region": "CN"}), `"decision":"allow"`)
	expectedAudits := int64(11)
	if datasource.ReadControlJSON != "" {
		expectedAudits += 2
	}
	if faultDir != "" {
		signal := func(name string) {
			t.Helper()
			require.NoError(t, os.WriteFile(filepath.Join(faultDir, name), []byte("ready\n"), 0o600))
		}
		waitSignal := func(name string) {
			t.Helper()
			for {
				if _, statErr := os.Stat(filepath.Join(faultDir, name)); statErr == nil {
					return
				} else if !os.IsNotExist(statErr) {
					require.NoError(t, statErr)
				}
				select {
				case <-ctx.Done():
					t.Fatal("timed out waiting for ClickHouse fault signal: " + name)
				case <-time.After(250 * time.Millisecond):
				}
			}
		}
		signal("audit_ready")
		waitSignal("audit_down")
		auditFailed := call("events", []string{"name", "phone"}, map[string]string{"region": "CN"})
		require.Contains(t, auditFailed, `"decision":"error"`)
		require.NotContains(t, auditFailed, "138****5678")
		signal("audit_down_checked")
		waitSignal("audit_up")
		require.Contains(t, call("events", []string{"name", "phone"}, map[string]string{"region": "CN"}), `"decision":"allow"`)
		expectedAudits += 2
		signal("clickhouse_ready")
		waitSignal("clickhouse_down")
		businessFailed := call("events", []string{"name", "phone"}, map[string]string{"region": "CN"})
		require.Contains(t, businessFailed, `"decision":"error"`)
		require.NotContains(t, businessFailed, "138****5678")
		signal("clickhouse_down_checked")
		waitSignal("clickhouse_up")
		require.Contains(t, call("events", []string{"name", "phone"}, map[string]string{"region": "CN"}), `"decision":"allow"`)
		expectedAudits += 4
	}
	if clientDir != "" {
		info, err := json.Marshal(map[string]string{"url": httpServer.URL + "/mcp", "api_key": key,
			"datasource_id": datasource.ID})
		require.NoError(t, err)
		infoPath := filepath.Join(clientDir, "client.json")
		require.NoError(t, os.WriteFile(infoPath, info, 0o600))
		defer os.Remove(infoPath)
		for {
			if _, statErr := os.Stat(filepath.Join(clientDir, "clients_done")); statErr == nil {
				break
			} else if !os.IsNotExist(statErr) {
				require.NoError(t, statErr)
			}
			select {
			case <-ctx.Done():
				t.Fatal("timed out waiting for ClickHouse MCP Inspector and Dify calls")
			case <-time.After(250 * time.Millisecond):
			}
		}
		expectedAudits += 6
	}
	require.NoError(t, runtime.Store.Policies().Delete(ctx, grant.ID))
	require.Contains(t, call("events", []string{"name", "phone"}, map[string]string{"region": "CN"}), `"decision":"deny"`)
	page, err := runtime.Store.AuditLogs().Page(ctx, 1, 30)
	require.NoError(t, err)
	require.EqualValues(t, expectedAudits, page.Total, "all allows, denials and errors have durable preflight/terminal records")
	for _, event := range page.List {
		require.Nil(t, event.SQLRaw)
		if event.DetailsJSON != nil {
			require.False(t, strings.Contains(*event.DetailsJSON, "CN"))
		}
	}
}

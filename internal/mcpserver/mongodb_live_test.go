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

// This test reaches the production runtime and HTTP MCP transport, using an
// isolated MongoDB 7.0 TLS instance and synthetic data when explicitly set.
func TestMongoControlledReadRealHTTPMCP(t *testing.T) {
	host := os.Getenv("AGENTSQL_TEST_MONGO_HOST")
	if host == "" {
		t.Skip("set AGENTSQL_TEST_MONGO_HOST for isolated MongoDB 7.0 HTTP MCP testing")
	}
	password := os.Getenv("AGENTSQL_TEST_MONGO_RO_PASSWORD")
	ca := os.Getenv("AGENTSQL_TEST_MONGO_CA")
	require.NotEmpty(t, password)
	require.NotEmpty(t, ca)
	serverName := os.Getenv("AGENTSQL_TEST_MONGO_SERVER_NAME")
	if serverName == "" {
		serverName = host
	}
	port := 27017
	clientDir := os.Getenv("AGENTSQL_TEST_MONGO_CLIENT_DIR")
	auditDSN := os.Getenv("AGENTSQL_TEST_MONGO_AUDIT_DSN")
	auditFaultDir := os.Getenv("AGENTSQL_TEST_MONGO_AUDIT_FAULT_DIR")
	businessFaultDir := os.Getenv("AGENTSQL_TEST_MONGO_BUSINESS_FAULT_DIR")
	if auditFaultDir != "" || businessFaultDir != "" {
		require.NotEmpty(t, auditDSN, "fault testing requires an independent audit database")
	}
	if raw := os.Getenv("AGENTSQL_TEST_MONGO_PORT"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		require.NoError(t, err)
		port = parsed
	}
	testTimeout := 45 * time.Second
	if clientDir != "" || auditFaultDir != "" || businessFaultDir != "" {
		testTimeout = 8 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	cfg := httpTestConfig(100)
	cfg.Store.SQLitePath = filepath.Join(t.TempDir(), "mongo-mcp.db")
	if auditDSN != "" {
		cfg.Store.Audit = &config.AuditStoreConfig{Separate: true, Driver: "postgres", DSN: auditDSN}
	}
	runtime, err := bootstrap.Assemble(ctx, cfg, mcpTestSecret)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, runtime.Close()) })
	key, hash, err := store.GenerateAPIKey()
	require.NoError(t, err)
	agent, err := runtime.Store.Agents().Create(ctx, model.Agent{ID: "mongo-http-agent", Name: "Mongo HTTP Agent",
		Status: "active", Level: "readonly", APIKeyHash: hash})
	require.NoError(t, err)
	datasource, err := runtime.Store.Datasources().Create(ctx, model.Datasource{ID: "mongo-http-datasource",
		Name: "Mongo HTTP test", DBType: "mongodb", Host: host, Port: port, Database: "app",
		Username: "agent_reader", TLSMode: "strict", TLSCAFile: ca, TLSServerName: serverName,
		StmtTimeoutMS: 3000, ConnLimit: 2, RowLimit: 100}, password)
	require.NoError(t, err)
	grant, err := runtime.Store.Policies().Create(ctx, model.Policy{ID: "mongo-http-collection",
		AgentID: agent.ID, DatasourceID: datasource.ID, ObjectType: "table", ObjectName: "app.customers", Action: "allow"})
	require.NoError(t, err)
	_, err = runtime.Store.MaskRules().Create(ctx, model.MaskRule{ID: "mongo-http-phone-mask",
		DatasourceID: &datasource.ID, SchemaName: "app", TableName: "customers", ColumnName: "phone",
		SensitiveType: string(mask.TypePhone), Algo: string(mask.AlgoMask), Enabled: true})
	require.NoError(t, err)
	handler, err := NewHTTPHandler(runtime, cfg, zerolog.Nop())
	require.NoError(t, err)
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)
	httpClient := httpServer.Client()
	httpClient.Transport = bearerTransport{base: httpClient.Transport, key: key}
	client := mcp.NewClient(&mcp.Implementation{Name: "mongo-http-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: httpServer.URL + "/mcp", HTTPClient: httpClient}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = session.Close() })
	call := func(collection string, filter map[string]string) string {
		t.Helper()
		response, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "find_documents", Arguments: map[string]any{
			"datasource_id": datasource.ID, "collection": collection, "fields": []string{"name", "phone"},
			"filter": filter, "limit": 1,
		}})
		require.NoError(t, err)
		encoded, err := json.Marshal(response)
		require.NoError(t, err)
		return string(encoded)
	}
	allowed := call("customers", map[string]string{"name": "alice"})
	require.Contains(t, allowed, `"decision":"allow"`)
	require.Contains(t, allowed, "138****5678")
	require.NotContains(t, allowed, "13812345678")
	require.NotContains(t, allowed, "synthetic-secret-canary")
	wrongCertificate := datasource
	wrongCertificate.TLSCAFile = ca + ".missing"
	_, err = runtime.Store.Datasources().Update(ctx, wrongCertificate, password)
	require.NoError(t, err)
	certificateDenied := call("customers", map[string]string{"name": "alice"})
	require.Contains(t, certificateDenied, `"decision":"error"`)
	require.NotContains(t, certificateDenied, "13812345678")
	_, err = runtime.Store.Datasources().Update(ctx, datasource, password)
	require.NoError(t, err)
	recovered := call("customers", map[string]string{"name": "alice"})
	require.Contains(t, recovered, `"decision":"allow"`)
	require.Contains(t, recovered, "138****5678")
	signal := func(dir, name string) {
		t.Helper()
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("ready\n"), 0o600))
	}
	waitSignal := func(dir, name string) {
		t.Helper()
		for {
			if _, statErr := os.Stat(filepath.Join(dir, name)); statErr == nil {
				return
			} else if !os.IsNotExist(statErr) {
				require.NoError(t, statErr)
			}
			select {
			case <-ctx.Done():
				t.Fatal("timed out waiting for MongoDB fault signal: " + name)
			case <-time.After(250 * time.Millisecond):
			}
		}
	}
	expectedAudits := int64(9)
	if auditFaultDir != "" {
		before, pageErr := runtime.Store.AuditLogs().Page(ctx, 1, 30)
		require.NoError(t, pageErr)
		signal(auditFaultDir, "audit_ready")
		waitSignal(auditFaultDir, "audit_down")
		failed := call("customers", map[string]string{"name": "alice"})
		require.Contains(t, failed, `"decision":"error"`)
		require.NotContains(t, failed, "13812345678")
		require.NotContains(t, failed, "138****5678")
		signal(auditFaultDir, "audit_down_checked")
		waitSignal(auditFaultDir, "audit_up")
		restored := call("customers", map[string]string{"name": "alice"})
		require.Contains(t, restored, `"decision":"allow"`)
		require.Contains(t, restored, "138****5678")
		after, pageErr := runtime.Store.AuditLogs().Page(ctx, 1, 30)
		require.NoError(t, pageErr)
		require.EqualValues(t, before.Total+2, after.Total,
			"audit outage must not persist an allow; recovery must persist preflight and terminal records")
		expectedAudits += 2
	}
	if businessFaultDir != "" {
		before, pageErr := runtime.Store.AuditLogs().Page(ctx, 1, 30)
		require.NoError(t, pageErr)
		signal(businessFaultDir, "mongo_ready")
		waitSignal(businessFaultDir, "mongo_down")
		failed := call("customers", map[string]string{"name": "alice"})
		require.Contains(t, failed, `"decision":"error"`)
		require.NotContains(t, failed, "13812345678")
		require.NotContains(t, failed, "138****5678")
		signal(businessFaultDir, "mongo_down_checked")
		waitSignal(businessFaultDir, "mongo_up")
		restored := call("customers", map[string]string{"name": "alice"})
		require.Contains(t, restored, `"decision":"allow"`)
		require.Contains(t, restored, "138****5678")
		after, pageErr := runtime.Store.AuditLogs().Page(ctx, 1, 30)
		require.NoError(t, pageErr)
		require.EqualValues(t, before.Total+4, after.Total,
			"business outage and recovery must both persist preflight and terminal records")
		expectedAudits += 4
	}
	require.Contains(t, call("private_collection", map[string]string{"name": "alice"}), `"decision":"deny"`)
	require.Contains(t, call("customers", map[string]string{}), `"decision":"deny"`)
	require.NoError(t, runtime.Store.Policies().Delete(ctx, grant.ID))
	require.Contains(t, call("customers", map[string]string{"name": "alice"}), `"decision":"deny"`)
	page, err := runtime.Store.AuditLogs().Page(ctx, 1, 20)
	require.NoError(t, err)
	require.EqualValues(t, expectedAudits, page.Total, "each database attempt has preflight and terminal audit; each denial adds one")
	for _, event := range page.List {
		require.NotNil(t, event.DetailsJSON)
		require.False(t, strings.Contains(*event.DetailsJSON, "alice"))
	}
	if clientDir != "" {
		_, err = runtime.Store.Policies().Create(ctx, model.Policy{ID: "mongo-http-collection-clients",
			AgentID: agent.ID, DatasourceID: datasource.ID, ObjectType: "table", ObjectName: "app.customers", Action: "allow"})
		require.NoError(t, err)
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
				t.Fatal("timed out waiting for MongoDB MCP Inspector and Dify clients")
			case <-time.After(250 * time.Millisecond):
			}
		}
		clientsPage, err := runtime.Store.AuditLogs().Page(ctx, 1, 30)
		require.NoError(t, err)
		require.EqualValues(t, page.Total+6, clientsPage.Total,
			"two clients must each persist an allowed preflight, allowed terminal and denied attempt")
	}
}

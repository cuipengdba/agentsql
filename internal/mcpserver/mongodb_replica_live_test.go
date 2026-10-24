package mcpserver

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
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

// A real TLS replica set, policy, masking, and persistent audit through HTTP MCP.
func TestMongoReplicaControlledRealHTTPMCP(t *testing.T) {
	if os.Getenv("AGENTSQL_V370_MONGO_MCP_LIVE") != "1" {
		t.Skip("explicit MongoDB replica fixture required")
	}
	password, err := os.ReadFile("/fixtures/reader.pass")
	require.NoError(t, err)
	clientDir := os.Getenv("AGENTSQL_V370_MONGO_CLIENT_DIR")
	auditDSN := os.Getenv("AGENTSQL_V370_MONGO_AUDIT_DSN")
	auditFaultDir := os.Getenv("AGENTSQL_V370_MONGO_AUDIT_FAULT_DIR")
	businessFaultDir := os.Getenv("AGENTSQL_V370_MONGO_BUSINESS_FAULT_DIR")
	if auditFaultDir != "" || businessFaultDir != "" {
		require.NotEmpty(t, auditDSN, "fault tests require an independent audit store")
	}
	testTimeout := 60 * time.Second
	if clientDir != "" || auditFaultDir != "" || businessFaultDir != "" {
		testTimeout = 8 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	cfg := httpTestConfig(100)
	cfg.Store.SQLitePath = filepath.Join(t.TempDir(), "mongo-replica-mcp.db")
	if auditDSN != "" {
		cfg.Store.Audit = &config.AuditStoreConfig{Separate: true, Driver: "postgres", DSN: auditDSN}
	}
	runtime, err := bootstrap.Assemble(ctx, cfg, mcpTestSecret)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, runtime.Close()) })
	key, hash, err := store.GenerateAPIKey()
	require.NoError(t, err)
	agent, err := runtime.Store.Agents().Create(ctx, model.Agent{ID: "mongo-replica-agent", Name: "Mongo replica test", Status: "active", Level: "readonly", APIKeyHash: hash})
	require.NoError(t, err)
	ds, err := runtime.Store.Datasources().Create(ctx, model.Datasource{
		ID: "mongo-replica-http", Name: "Mongo replica HTTP", DBType: "mongodb", Host: "mongo1", Port: 27017,
		Database: "agv370", Username: "reader", TLSMode: "strict", TLSCAFile: "/fixtures/ca.pem",
		StmtTimeoutMS: 5000, ConnLimit: 3, RowLimit: 10,
		ReadControlJSON: `{"mongo":{"replica_set":"rs0","members":["mongo1:27017","mongo2:27017","mongo3:27017"],"indexes":[{"collection":"customers","name":"customer_id_1","fields":["customer_id"]}]}}`,
	}, string(password))
	require.NoError(t, err)
	grant, err := runtime.Store.Policies().Create(ctx, model.Policy{ID: "mongo-replica-grant", AgentID: agent.ID, DatasourceID: ds.ID, ObjectType: "table", ObjectName: "agv370.customers", Action: "allow"})
	require.NoError(t, err)
	_, err = runtime.Store.MaskRules().Create(ctx, model.MaskRule{ID: "mongo-replica-mask", DatasourceID: &ds.ID, SchemaName: "agv370", TableName: "customers", ColumnName: "phone", SensitiveType: string(mask.TypePhone), Algo: string(mask.AlgoMask), Enabled: true})
	require.NoError(t, err)
	handler, err := NewHTTPHandler(runtime, cfg, zerolog.Nop())
	require.NoError(t, err)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	httpClient := server.Client()
	httpClient.Transport = bearerTransport{base: httpClient.Transport, key: key}
	client := mcp.NewClient(&mcp.Implementation{Name: "mongo-replica-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: server.URL + "/mcp", HTTPClient: httpClient}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = session.Close() })
	call := func(collection string, filter map[string]string) string {
		response, callErr := session.CallTool(ctx, &mcp.CallToolParams{Name: "find_documents", Arguments: map[string]any{
			"datasource_id": ds.ID, "collection": collection, "fields": []string{"customer_id", "phone"}, "filter": filter, "limit": 1,
		}})
		require.NoError(t, callErr)
		encoded, marshalErr := json.Marshal(response)
		require.NoError(t, marshalErr)
		return string(encoded)
	}
	allowed := call("customers", map[string]string{"customer_id": "C7"})
	require.Contains(t, allowed, `"decision":"allow"`)
	require.Contains(t, allowed, "138****0007")
	require.NotContains(t, allowed, "13800000007")
	require.Contains(t, call("customers", map[string]string{"status": "active"}), `"decision":"error"`)
	require.Contains(t, call("private_collection", map[string]string{"customer_id": "C7"}), `"decision":"deny"`)
	wrongCA := ds
	wrongCA.TLSCAFile = "/fixtures/wrongca.pem"
	_, err = runtime.Store.Datasources().Update(ctx, wrongCA, string(password))
	require.NoError(t, err)
	require.Contains(t, call("customers", map[string]string{"customer_id": "C7"}), `"decision":"error"`)
	_, err = runtime.Store.Datasources().Update(ctx, ds, string(password))
	require.NoError(t, err)
	require.Contains(t, call("customers", map[string]string{"customer_id": "C7"}), `"decision":"allow"`)
	require.NoError(t, runtime.Store.Policies().Delete(ctx, grant.ID))
	require.Contains(t, call("customers", map[string]string{"customer_id": "C7"}), `"decision":"deny"`)
	page, err := runtime.Store.AuditLogs().Page(ctx, 1, 20)
	require.NoError(t, err)
	require.GreaterOrEqual(t, page.Total, int64(9))
	for _, event := range page.List {
		require.NotNil(t, event.DetailsJSON)
		require.False(t, strings.Contains(*event.DetailsJSON, "13800000007"))
	}
	if clientDir != "" || auditFaultDir != "" || businessFaultDir != "" {
		_, err = runtime.Store.Policies().Create(ctx, model.Policy{ID: "mongo-replica-clients-grant", AgentID: agent.ID, DatasourceID: ds.ID, ObjectType: "table", ObjectName: "agv370.customers", Action: "allow"})
		require.NoError(t, err)
	}
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
				t.Fatal("timed out waiting for MongoDB replica fault signal: " + name)
			case <-time.After(250 * time.Millisecond):
			}
		}
	}
	if auditFaultDir != "" {
		before := page.Total
		signal(auditFaultDir, "audit_ready")
		waitSignal(auditFaultDir, "audit_down")
		failed := call("customers", map[string]string{"customer_id": "C7"})
		require.Contains(t, failed, `"decision":"error"`)
		require.NotContains(t, failed, "138****0007")
		signal(auditFaultDir, "audit_down_checked")
		waitSignal(auditFaultDir, "audit_up")
		require.Contains(t, call("customers", map[string]string{"customer_id": "C7"}), `"decision":"allow"`)
		page, err = runtime.Store.AuditLogs().Page(ctx, 1, 30)
		require.NoError(t, err)
		require.EqualValues(t, before+2, page.Total)
	}
	if businessFaultDir != "" {
		before := page.Total
		signal(businessFaultDir, "mongo_ready")
		waitSignal(businessFaultDir, "mongo_down")
		failed := call("customers", map[string]string{"customer_id": "C7"})
		require.Contains(t, failed, `"decision":"error"`)
		require.NotContains(t, failed, "138****0007")
		signal(businessFaultDir, "mongo_down_checked")
		waitSignal(businessFaultDir, "mongo_up")
		require.Contains(t, call("customers", map[string]string{"customer_id": "C7"}), `"decision":"allow"`)
		page, err = runtime.Store.AuditLogs().Page(ctx, 1, 30)
		require.NoError(t, err)
		require.EqualValues(t, before+4, page.Total)
	}
	if clientDir != "" {
		info, err := json.Marshal(map[string]string{"url": server.URL + "/mcp", "api_key": key, "datasource_id": ds.ID})
		require.NoError(t, err)
		infoPath := filepath.Join(clientDir, "mongo-client.json")
		require.NoError(t, os.WriteFile(infoPath, info, 0o600))
		defer os.Remove(infoPath)
		for {
			if _, statErr := os.Stat(filepath.Join(clientDir, "mongo-clients_done")); statErr == nil {
				break
			} else if !os.IsNotExist(statErr) {
				require.NoError(t, statErr)
			}
			select {
			case <-ctx.Done():
				t.Fatal("timed out waiting for MongoDB replica MCP clients")
			case <-time.After(250 * time.Millisecond):
			}
		}
		clientsPage, err := runtime.Store.AuditLogs().Page(ctx, 1, 30)
		require.NoError(t, err)
		require.EqualValues(t, page.Total+6, clientsPage.Total)
	}
}

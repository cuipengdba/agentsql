package mcpserver

import (
	"context"
	"encoding/json"
	"net"
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

func TestMilvusScopedRealHTTPMCP(t *testing.T) {
	host := os.Getenv("AGENTSQL_TEST_MILVUS_HOST")
	if host == "" {
		t.Skip("set AGENTSQL_TEST_MILVUS_HOST for isolated Milvus HTTP MCP testing")
	}
	passwordBytes, err := os.ReadFile(os.Getenv("AGENTSQL_TEST_MILVUS_PASSWORD_FILE"))
	require.NoError(t, err)
	password := strings.TrimSpace(string(passwordBytes))
	clientDir := os.Getenv("AGENTSQL_TEST_MILVUS_CLIENT_DIR")
	auditDSN := os.Getenv("AGENTSQL_TEST_MILVUS_AUDIT_DSN")
	faultDir := os.Getenv("AGENTSQL_TEST_MILVUS_FAULT_DIR")
	if faultDir != "" {
		require.NotEmpty(t, auditDSN)
	}
	deadline := 90 * time.Second
	if clientDir != "" {
		deadline = 10 * time.Minute
	} else if faultDir != "" {
		deadline = 8 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()
	cfg := httpTestConfig(100)
	cfg.Store.SQLitePath = filepath.Join(t.TempDir(), "milvus-scoped-mcp.db")
	if auditDSN != "" {
		cfg.Store.Audit = &config.AuditStoreConfig{Separate: true, Driver: "postgres", DSN: auditDSN}
	}
	runtime, err := bootstrap.Assemble(ctx, cfg, mcpTestSecret)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, runtime.Close()) })
	key, hash, err := store.GenerateAPIKey()
	require.NoError(t, err)
	agent, err := runtime.Store.Agents().Create(ctx, model.Agent{ID: "milvus-scoped-http-agent", Name: "Milvus scoped HTTP Agent",
		Status: "active", Level: "readonly", APIKeyHash: hash})
	require.NoError(t, err)
	require.Equal(t, "tenant_default", agent.TenantID)
	ds, err := runtime.Store.Datasources().Create(ctx, model.Datasource{ID: "milvus-scoped-http-datasource",
		Name: "Milvus scoped HTTP", DBType: "milvus", Host: host, Port: 19530, Database: "default",
		Username: "agent_reader", TLSMode: "strict", TLSCAFile: os.Getenv("AGENTSQL_TEST_MILVUS_CA"), TLSServerName: host,
		StmtTimeoutMS: 5000, ConnLimit: 1, RowLimit: 20,
		ReadControlJSON: `{"milvus":{"tenant_field":"tenant_id","filter_fields":["status","region"]}}`}, password)
	require.NoError(t, err)
	require.NoError(t, runtime.PingDatasource(ctx, ds))
	grant, err := runtime.Store.Policies().Create(ctx, model.Policy{ID: "milvus-scoped-http-table",
		AgentID: agent.ID, DatasourceID: ds.ID, ObjectType: "table", ObjectName: "default.events_scoped", Action: "allow"})
	require.NoError(t, err)
	columns := "embedding,name,phone,tenant_id,status,region"
	_, err = runtime.Store.Policies().Create(ctx, model.Policy{ID: "milvus-scoped-http-columns",
		AgentID: agent.ID, DatasourceID: ds.ID, ObjectType: "column", ObjectName: "default.events_scoped",
		Columns: &columns, Action: "allow"})
	require.NoError(t, err)
	_, err = runtime.Store.MaskRules().Create(ctx, model.MaskRule{ID: "milvus-scoped-http-mask",
		DatasourceID: &ds.ID, SchemaName: "default", TableName: "events_scoped", ColumnName: "phone",
		SensitiveType: string(mask.TypePhone), Algo: string(mask.AlgoMask), Enabled: true})
	require.NoError(t, err)
	handler, err := NewHTTPHandler(runtime, cfg, zerolog.Nop())
	require.NoError(t, err)
	server := httptest.NewUnstartedServer(handler)
	if clientDir != "" {
		server.Listener, err = net.Listen("tcp", "0.0.0.0:29990")
		require.NoError(t, err)
	}
	server.Start()
	t.Cleanup(server.Close)
	endpoint := server.URL + "/mcp"
	if clientDir != "" {
		endpoint = "http://127.0.0.1:29990/mcp"
	}
	httpClient := server.Client()
	httpClient.Transport = bearerTransport{base: httpClient.Transport, key: key}
	client := mcp.NewClient(&mcp.Implementation{Name: "milvus-scoped-live-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: endpoint, HTTPClient: httpClient}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = session.Close() })
	call := func(filter map[string]string) string {
		t.Helper()
		response, callErr := session.CallTool(ctx, &mcp.CallToolParams{Name: "search_vectors", Arguments: map[string]any{
			"datasource_id": ds.ID, "collection": "events_scoped", "vector_field": "embedding",
			"vector": []float32{1, 0, 0, 0}, "fields": []string{"name", "phone"}, "filter": filter, "top_k": 20,
		}})
		require.NoError(t, callErr)
		encoded, marshalErr := json.Marshal(response)
		require.NoError(t, marshalErr)
		return string(encoded)
	}
	allowed := call(map[string]string{"region": "east", "status": "active"})
	require.Contains(t, allowed, `"decision":"allow"`)
	require.Contains(t, allowed, "default-east")
	require.Contains(t, allowed, "****")
	for _, forbidden := range []string{"tenant-a-east", "tenant-b-east", "tenant-a-inactive", "13500135000", "13800138000", "13900139000"} {
		require.NotContains(t, allowed, forbidden)
	}
	require.Contains(t, call(map[string]string{"status": "inactive"}), `"decision":"allow"`)
	require.Contains(t, call(map[string]string{"tenant_id": "tenant-a"}), `"decision":"deny"`)
	require.Contains(t, call(map[string]string{"secret": "active"}), `"decision":"deny"`)
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
					t.Fatal("timed out waiting for scoped Milvus fault signal: " + name)
				case <-time.After(250 * time.Millisecond):
				}
			}
		}
		filter := map[string]string{"region": "east", "status": "active"}
		signal("audit_ready")
		waitSignal("audit_down")
		failed := call(filter)
		require.Contains(t, failed, `"decision":"error"`)
		require.NotContains(t, failed, "default-east")
		signal("audit_down_checked")
		waitSignal("audit_up")
		require.Contains(t, call(filter), `"decision":"allow"`)
		signal("milvus_ready")
		waitSignal("milvus_down")
		failed = call(filter)
		require.Contains(t, failed, `"decision":"error"`)
		require.NotContains(t, failed, "default-east")
		signal("milvus_down_checked")
		waitSignal("milvus_up")
		recovered := false
		until := time.Now().Add(90 * time.Second)
		for time.Now().Before(until) {
			response := call(filter)
			if strings.Contains(response, `"decision":"allow"`) {
				recovered = true
				break
			}
			require.Contains(t, response, `"decision":"error"`)
			require.NotContains(t, response, "default-east")
			time.Sleep(time.Second)
		}
		require.True(t, recovered, "scoped Milvus search should recover")
	}
	if clientDir != "" {
		info, marshalErr := json.Marshal(map[string]string{"url": endpoint, "api_key": key, "datasource_id": ds.ID})
		require.NoError(t, marshalErr)
		require.NoError(t, os.WriteFile(filepath.Join(clientDir, "client.json"), info, 0o600))
		defer os.Remove(filepath.Join(clientDir, "client.json"))
		for {
			if _, statErr := os.Stat(filepath.Join(clientDir, "clients_done")); statErr == nil {
				break
			} else if !os.IsNotExist(statErr) {
				require.NoError(t, statErr)
			}
			select {
			case <-ctx.Done():
				t.Fatal("timed out waiting for external Milvus MCP clients")
			case <-time.After(250 * time.Millisecond):
			}
		}
	}
	require.NoError(t, runtime.Store.Policies().Delete(ctx, grant.ID))
	require.Contains(t, call(map[string]string{"region": "east"}), `"decision":"deny"`)
	page, err := runtime.Store.AuditLogs().Page(ctx, 1, 100)
	require.NoError(t, err)
	require.GreaterOrEqual(t, page.Total, int64(7))
	for _, event := range page.List {
		require.Nil(t, event.SQLRaw)
		if event.DetailsJSON != nil {
			require.NotContains(t, *event.DetailsJSON, "tenant-a")
			require.NotContains(t, *event.DetailsJSON, "13500135000")
		}
	}
}

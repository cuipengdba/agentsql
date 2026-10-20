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

func TestMilvusControlledRealHTTPMCP(t *testing.T) {
	host := os.Getenv("AGENTSQL_TEST_MILVUS_HOST")
	if host == "" {
		t.Skip("set AGENTSQL_TEST_MILVUS_HOST for isolated Milvus HTTP MCP testing")
	}
	passwordBytes, err := os.ReadFile(os.Getenv("AGENTSQL_TEST_MILVUS_PASSWORD_FILE"))
	require.NoError(t, err)
	password := strings.TrimSpace(string(passwordBytes))
	ca := os.Getenv("AGENTSQL_TEST_MILVUS_CA")
	clientDir := os.Getenv("AGENTSQL_TEST_MILVUS_CLIENT_DIR")
	auditDSN := os.Getenv("AGENTSQL_TEST_MILVUS_AUDIT_DSN")
	faultDir := os.Getenv("AGENTSQL_TEST_MILVUS_FAULT_DIR")
	if faultDir != "" {
		require.NotEmpty(t, auditDSN, "fault testing requires a separate audit database")
	}
	timeout := 2 * time.Minute
	if clientDir != "" {
		timeout = 10 * time.Minute
	} else if faultDir != "" {
		timeout = 8 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cfg := httpTestConfig(100)
	cfg.Store.SQLitePath = filepath.Join(t.TempDir(), "milvus-mcp.db")
	if auditDSN != "" {
		cfg.Store.Audit = &config.AuditStoreConfig{Separate: true, Driver: "postgres", DSN: auditDSN}
	}
	runtime, err := bootstrap.Assemble(ctx, cfg, mcpTestSecret)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, runtime.Close()) })
	key, hash, err := store.GenerateAPIKey()
	require.NoError(t, err)
	agent, err := runtime.Store.Agents().Create(ctx, model.Agent{ID: "milvus-http-agent", Name: "Milvus HTTP Agent",
		Status: "active", Level: "readonly", APIKeyHash: hash})
	require.NoError(t, err)
	ds, err := runtime.Store.Datasources().Create(ctx, model.Datasource{ID: "milvus-http-datasource",
		Name: "Milvus HTTP test", DBType: "milvus", Host: host, Port: 19530, Database: "default",
		Username: "agent_reader", TLSMode: "strict", TLSCAFile: ca, TLSServerName: host,
		StmtTimeoutMS: 5000, ConnLimit: 1, RowLimit: 20}, password)
	require.NoError(t, err)
	require.NoError(t, runtime.PingDatasource(ctx, ds), "admin connection test must verify the controlled TLS path")
	grant, err := runtime.Store.Policies().Create(ctx, model.Policy{ID: "milvus-http-table",
		AgentID: agent.ID, DatasourceID: ds.ID, ObjectType: "table", ObjectName: "default.events", Action: "allow"})
	require.NoError(t, err)
	columns := "embedding,name,phone,region"
	_, err = runtime.Store.Policies().Create(ctx, model.Policy{ID: "milvus-http-columns",
		AgentID: agent.ID, DatasourceID: ds.ID, ObjectType: "column", ObjectName: "default.events",
		Columns: &columns, Action: "allow"})
	require.NoError(t, err)
	_, err = runtime.Store.MaskRules().Create(ctx, model.MaskRule{ID: "milvus-http-mask",
		DatasourceID: &ds.ID, SchemaName: "default", TableName: "events", ColumnName: "phone",
		SensitiveType: string(mask.TypePhone), Algo: string(mask.AlgoMask), Enabled: true})
	require.NoError(t, err)
	handler, err := NewHTTPHandler(runtime, cfg, zerolog.Nop())
	require.NoError(t, err)
	var httpServer *httptest.Server
	clientEndpoint := ""
	if clientDir != "" {
		listener, listenErr := net.Listen("tcp", "0.0.0.0:29990")
		require.NoError(t, listenErr)
		httpServer = httptest.NewUnstartedServer(handler)
		httpServer.Listener = listener
		httpServer.Start()
		clientEndpoint = "http://127.0.0.1:29990/mcp"
	} else {
		httpServer = httptest.NewServer(handler)
		clientEndpoint = httpServer.URL + "/mcp"
	}
	t.Cleanup(httpServer.Close)
	httpClient := httpServer.Client()
	httpClient.Transport = bearerTransport{base: httpClient.Transport, key: key}
	client := mcp.NewClient(&mcp.Implementation{Name: "milvus-http-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: clientEndpoint, HTTPClient: httpClient}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = session.Close() })
	call := func(collection string, fields []string, vector []float32) string {
		t.Helper()
		response, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "search_vectors", Arguments: map[string]any{
			"datasource_id": ds.ID, "collection": collection, "vector_field": "embedding",
			"vector": vector, "fields": fields, "top_k": 2,
		}})
		require.NoError(t, err)
		encoded, err := json.Marshal(response)
		require.NoError(t, err)
		return string(encoded)
	}
	vector := []float32{0.1234567, 0.5, 0, 0}
	allowed := call("events", []string{"name", "phone"}, vector)
	require.Contains(t, allowed, `"decision":"allow"`)
	require.Contains(t, allowed, "****")
	for _, raw := range []string{"13800138000", "13900139000", "13700137000"} {
		require.NotContains(t, allowed, raw)
	}
	require.Contains(t, call("private_notes", []string{"name"}, vector), `"decision":"deny"`)
	require.Contains(t, call("events", []string{"secret"}, vector), `"decision":"deny"`)
	require.Contains(t, call("events", []string{"name"}, []float32{1, 0, 0}), `"decision":"error"`)
	badCA := ds
	badCA.TLSCAFile = ca + ".missing"
	_, err = runtime.Store.Datasources().Update(ctx, badCA, password)
	require.NoError(t, err)
	require.Contains(t, call("events", []string{"name", "phone"}, vector), `"decision":"error"`)
	_, err = runtime.Store.Datasources().Update(ctx, ds, password)
	require.NoError(t, err)
	require.Contains(t, call("events", []string{"name", "phone"}, vector), `"decision":"allow"`)
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
					t.Fatal("timed out waiting for Milvus fault signal: " + name)
				case <-time.After(250 * time.Millisecond):
				}
			}
		}
		signal("audit_ready")
		waitSignal("audit_down")
		auditFailed := call("events", []string{"name", "phone"}, vector)
		require.Contains(t, auditFailed, `"decision":"error"`)
		require.NotContains(t, auditFailed, "****")
		signal("audit_down_checked")
		waitSignal("audit_up")
		require.Contains(t, call("events", []string{"name", "phone"}, vector), `"decision":"allow"`)
		signal("milvus_ready")
		waitSignal("milvus_down")
		businessFailed := call("events", []string{"name", "phone"}, vector)
		require.Contains(t, businessFailed, `"decision":"error"`)
		require.NotContains(t, businessFailed, "****")
		signal("milvus_down_checked")
		waitSignal("milvus_up")
		// The health endpoint can become ready before the collection's query
		// channels finish loading. Every attempt must still fail closed until
		// search is available again.
		recovered := false
		deadline := time.Now().Add(90 * time.Second)
		for time.Now().Before(deadline) {
			result := call("events", []string{"name", "phone"}, vector)
			if strings.Contains(result, `"decision":"allow"`) {
				recovered = true
				break
			}
			require.Contains(t, result, `"decision":"error"`)
			require.NotContains(t, result, "****")
			time.Sleep(time.Second)
		}
		require.True(t, recovered, "Milvus search should recover after its collection is loaded")
	}
	if clientDir != "" {
		info, err := json.Marshal(map[string]string{"url": clientEndpoint, "api_key": key, "datasource_id": ds.ID})
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
				t.Fatal("timed out waiting for Milvus MCP Inspector and Dify calls")
			case <-time.After(250 * time.Millisecond):
			}
		}
	}
	require.NoError(t, runtime.Store.Policies().Delete(ctx, grant.ID))
	require.Contains(t, call("events", []string{"name", "phone"}, vector), `"decision":"deny"`)
	page, err := runtime.Store.AuditLogs().Page(ctx, 1, 100)
	require.NoError(t, err)
	require.GreaterOrEqual(t, page.Total, int64(9))
	for _, event := range page.List {
		require.Nil(t, event.SQLRaw)
		if event.DetailsJSON != nil {
			require.NotContains(t, *event.DetailsJSON, "0.1234567")
		}
	}
}

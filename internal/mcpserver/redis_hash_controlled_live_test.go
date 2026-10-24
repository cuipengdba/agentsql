package mcpserver

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/bootstrap"
	"github.com/cuipengdba/agentsql/internal/mask"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// Run against isolated TLS/ACL Redis and Valkey containers with synthetic data.
func TestRedisHashControlledRealHTTPMCP(t *testing.T) {
	ca := os.Getenv("AGENTSQL_TEST_HASH_CA")
	password := os.Getenv("AGENTSQL_TEST_HASH_READER_PASSWORD")
	if ca == "" || password == "" {
		t.Skip("set AGENTSQL_TEST_HASH_CA and AGENTSQL_TEST_HASH_READER_PASSWORD")
	}
	for _, dialect := range []string{"redis", "valkey"} {
		t.Run(dialect, func(t *testing.T) {
			host := os.Getenv("AGENTSQL_TEST_HASH_" + dialect + "_HOST")
			if host == "" {
				t.Skip("isolated Hash server host unavailable")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			cfg := httpTestConfig(100)
			cfg.Store.SQLitePath = filepath.Join(t.TempDir(), "hash-mcp.db")
			runtime, err := bootstrap.Assemble(ctx, cfg, mcpTestSecret)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, runtime.Close()) })
			key, hash, err := store.GenerateAPIKey()
			require.NoError(t, err)
			agent, err := runtime.Store.Agents().Create(ctx, model.Agent{ID: "hash-http-agent", Name: "Hash HTTP Agent",
				Status: "active", Level: "readonly", APIKeyHash: hash})
			require.NoError(t, err)
			datasource, err := runtime.Store.Datasources().Create(ctx, model.Datasource{ID: "hash-http-datasource",
				Name: "Hash HTTP test", DBType: dialect, Host: host, Port: 6379, Database: "0",
				Username: "agent_reader", TLSMode: "strict", TLSCAFile: ca, TLSServerName: host,
				StmtTimeoutMS: 3000, ConnLimit: 2, RowLimit: 1}, password)
			require.NoError(t, err)
			for _, grant := range []model.Policy{
				{ID: "hash-http-key", AgentID: agent.ID, DatasourceID: datasource.ID, ObjectType: "key", ObjectName: "db0:customers:1", Action: "allow"},
				{ID: "hash-http-phone", AgentID: agent.ID, DatasourceID: datasource.ID, ObjectType: "key_field", ObjectName: "db0:customers:1#phone", Action: "allow"},
				{ID: "hash-http-oversize-key", AgentID: agent.ID, DatasourceID: datasource.ID, ObjectType: "key", ObjectName: "db0:customers:oversize", Action: "allow"},
				{ID: "hash-http-oversize-field", AgentID: agent.ID, DatasourceID: datasource.ID, ObjectType: "key_field", ObjectName: "db0:customers:oversize#phone", Action: "allow"},
				{ID: "hash-http-wrongtype-key", AgentID: agent.ID, DatasourceID: datasource.ID, ObjectType: "key", ObjectName: "db0:customers:wrongtype", Action: "allow"},
				{ID: "hash-http-wrongtype-field", AgentID: agent.ID, DatasourceID: datasource.ID, ObjectType: "key_field", ObjectName: "db0:customers:wrongtype#phone", Action: "allow"},
			} {
				_, err := runtime.Store.Policies().Create(ctx, grant)
				require.NoError(t, err)
			}
			_, err = runtime.Store.MaskRules().Create(ctx, model.MaskRule{ID: "hash-http-mask",
				DatasourceID: &datasource.ID, SchemaName: "db0", TableName: "customers:1", ColumnName: "phone",
				SensitiveType: string(mask.TypePhone), Algo: string(mask.AlgoMask), Enabled: true})
			require.NoError(t, err)
			handler, err := NewHTTPHandler(runtime, cfg, zerolog.Nop())
			require.NoError(t, err)
			server := httptest.NewServer(handler)
			t.Cleanup(server.Close)
			httpClient := server.Client()
			httpClient.Transport = bearerTransport{base: httpClient.Transport, key: key}
			client := mcp.NewClient(&mcp.Implementation{Name: "hash-http-test", Version: "1"}, nil)
			session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: server.URL + "/mcp", HTTPClient: httpClient}, nil)
			require.NoError(t, err)
			t.Cleanup(func() { _ = session.Close() })
			call := func(keyName string, fields []string) string {
				response, callErr := session.CallTool(ctx, &mcp.CallToolParams{Name: "read_hash_fields", Arguments: map[string]any{
					"datasource_id": datasource.ID, "key": keyName, "fields": fields,
				}})
				require.NoError(t, callErr)
				encoded, marshalErr := json.Marshal(response)
				require.NoError(t, marshalErr)
				return string(encoded)
			}
			allowed := call("customers:1", []string{"phone"})
			require.Contains(t, allowed, `"decision":"allow"`)
			require.Contains(t, allowed, "138****5678")
			require.NotContains(t, allowed, "13812345678")
			denied := call("customers:1", []string{"secret"})
			require.Contains(t, denied, `"decision":"deny"`)
			require.NotContains(t, denied, "synthetic-secret-canary")
			require.Contains(t, call("customers:oversize", []string{"phone"}), `"decision":"error"`)
			require.Contains(t, call("customers:wrongtype", []string{"phone"}), `"decision":"error"`)
			wrongCA := datasource
			wrongCA.TLSCAFile = ca + ".missing"
			_, err = runtime.Store.Datasources().Update(ctx, wrongCA, password)
			require.NoError(t, err)
			require.Contains(t, call("customers:1", []string{"phone"}), `"decision":"error"`)
			_, err = runtime.Store.Datasources().Update(ctx, datasource, password)
			require.NoError(t, err)
			require.Contains(t, call("customers:1", []string{"phone"}), `"decision":"allow"`)
			page, err := runtime.Store.AuditLogs().Page(ctx, 1, 20)
			require.NoError(t, err)
			require.GreaterOrEqual(t, page.Total, int64(10))
			for _, event := range page.List {
				require.Equal(t, "read_hash_fields", *event.MCPTool)
				require.NotContains(t, *event.DetailsJSON, "13812345678")
			}
		})
	}
}

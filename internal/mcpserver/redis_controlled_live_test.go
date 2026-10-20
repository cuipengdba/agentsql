package mcpserver

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
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

// Run only against the isolated TLS Redis and Valkey fixtures with synthetic data.
func TestRedisValkeyControlledRealHTTPMCP(t *testing.T) {
	ca := os.Getenv("AGENTSQL_TEST_KEY_CA")
	password := os.Getenv("AGENTSQL_TEST_KEY_READER_PASSWORD")
	if ca == "" || password == "" {
		t.Skip("set AGENTSQL_TEST_KEY_CA and AGENTSQL_TEST_KEY_READER_PASSWORD")
	}
	auditDSN := os.Getenv("AGENTSQL_TEST_KEY_AUDIT_DSN")
	faultDir := os.Getenv("AGENTSQL_TEST_KEY_FAULT_DIR")
	clientDir := os.Getenv("AGENTSQL_TEST_KEY_CLIENT_DIR")
	if faultDir != "" {
		require.NotEmpty(t, auditDSN, "fault testing requires an independent audit database")
	}
	for _, dialect := range []string{"redis", "valkey"} {
		t.Run(dialect, func(t *testing.T) {
			host := os.Getenv("AGENTSQL_TEST_KEY_" + dialect + "_HOST")
			if host == "" {
				t.Skip("isolated key server host unavailable")
			}
			serverName := os.Getenv("AGENTSQL_TEST_KEY_" + dialect + "_SERVER_NAME")
			if serverName == "" {
				serverName = host
			}
			testTimeout := 60 * time.Second
			if faultDir != "" || clientDir != "" {
				testTimeout = 8 * time.Minute
			}
			ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
			defer cancel()
			cfg := httpTestConfig(100)
			cfg.Store.SQLitePath = filepath.Join(t.TempDir(), "key-mcp.db")
			if auditDSN != "" {
				cfg.Store.Audit = &config.AuditStoreConfig{Separate: true, Driver: "postgres", DSN: auditDSN}
			}
			runtime, err := bootstrap.Assemble(ctx, cfg, mcpTestSecret)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, runtime.Close()) })
			key, hash, err := store.GenerateAPIKey()
			require.NoError(t, err)
			agent, err := runtime.Store.Agents().Create(ctx, model.Agent{ID: "key-http-agent", Name: "Key HTTP Agent",
				Status: "active", Level: "readonly", APIKeyHash: hash})
			require.NoError(t, err)
			port := 6379
			if raw := os.Getenv("AGENTSQL_TEST_KEY_" + dialect + "_PORT"); raw != "" {
				port, err = strconv.Atoi(raw)
				require.NoError(t, err)
			}
			datasource, err := runtime.Store.Datasources().Create(ctx, model.Datasource{ID: "key-http-datasource",
				Name: "Key HTTP test", DBType: dialect, Host: host, Port: port, Database: "0",
				Username: "agent_reader", TLSMode: "strict", TLSCAFile: ca, TLSServerName: serverName,
				StmtTimeoutMS: 3000, ConnLimit: 2, RowLimit: 1}, password)
			require.NoError(t, err)
			grant, err := runtime.Store.Policies().Create(ctx, model.Policy{ID: "key-http-grant",
				AgentID: agent.ID, DatasourceID: datasource.ID, ObjectType: "key", ObjectName: "db0:customers:*", Action: "allow"})
			require.NoError(t, err)
			_, err = runtime.Store.MaskRules().Create(ctx, model.MaskRule{ID: "key-http-phone-mask",
				DatasourceID: &datasource.ID, SchemaName: "db0", TableName: "customers:1", ColumnName: "value",
				SensitiveType: string(mask.TypePhone), Algo: string(mask.AlgoMask), Enabled: true})
			require.NoError(t, err)
			handler, err := NewHTTPHandler(runtime, cfg, zerolog.Nop())
			require.NoError(t, err)
			server := httptest.NewServer(handler)
			t.Cleanup(server.Close)
			httpClient := server.Client()
			httpClient.Transport = bearerTransport{base: httpClient.Transport, key: key}
			client := mcp.NewClient(&mcp.Implementation{Name: "key-http-test", Version: "1"}, nil)
			session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: server.URL + "/mcp", HTTPClient: httpClient}, nil)
			require.NoError(t, err)
			t.Cleanup(func() { _ = session.Close() })
			call := func(keyName string) string {
				response, callErr := session.CallTool(ctx, &mcp.CallToolParams{Name: "read_key", Arguments: map[string]any{
					"datasource_id": datasource.ID, "key": keyName,
				}})
				require.NoError(t, callErr)
				encoded, marshalErr := json.Marshal(response)
				require.NoError(t, marshalErr)
				return string(encoded)
			}
			allowed := call("customers:1")
			require.Contains(t, allowed, `"decision":"allow"`)
			require.Contains(t, allowed, "138****5678")
			require.NotContains(t, allowed, "13812345678")
			denied := call("private:1")
			require.Contains(t, denied, `"decision":"deny"`)
			require.NotContains(t, denied, "synthetic-secret-canary")
			wrongCA := datasource
			wrongCA.TLSCAFile = ca + ".missing"
			_, err = runtime.Store.Datasources().Update(ctx, wrongCA, password)
			require.NoError(t, err)
			require.Contains(t, call("customers:1"), `"decision":"error"`)
			_, err = runtime.Store.Datasources().Update(ctx, datasource, password)
			require.NoError(t, err)
			recovered := call("customers:1")
			require.Contains(t, recovered, `"decision":"allow"`)
			require.Contains(t, recovered, "138****5678")
			if faultDir != "" {
				signal := func(name string) {
					require.NoError(t, os.WriteFile(filepath.Join(faultDir, dialect+"_"+name), []byte("ready\n"), 0o600))
				}
				waitSignal := func(name string) {
					for {
						_, statErr := os.Stat(filepath.Join(faultDir, dialect+"_"+name))
						if statErr == nil {
							return
						}
						require.True(t, os.IsNotExist(statErr), "fault signal error: %v", statErr)
						select {
						case <-ctx.Done():
							t.Fatal("timed out waiting for key fault signal: " + name)
						case <-time.After(250 * time.Millisecond):
						}
					}
				}
				before, pageErr := runtime.Store.AuditLogs().Page(ctx, 1, 40)
				require.NoError(t, pageErr)
				signal("audit_ready")
				waitSignal("audit_down")
				failed := call("customers:1")
				require.Contains(t, failed, `"decision":"error"`)
				require.NotContains(t, failed, "13812345678")
				require.NotContains(t, failed, "138****5678")
				signal("audit_down_checked")
				waitSignal("audit_up")
				recovered = call("customers:1")
				require.Contains(t, recovered, `"decision":"allow"`)
				require.Contains(t, recovered, "138****5678")
				after, pageErr := runtime.Store.AuditLogs().Page(ctx, 1, 40)
				require.NoError(t, pageErr)
				require.EqualValues(t, before.Total+2, after.Total)
				before = after
				signal("business_ready")
				waitSignal("business_down")
				failed = call("customers:1")
				require.Contains(t, failed, `"decision":"error"`)
				require.NotContains(t, failed, "13812345678")
				require.NotContains(t, failed, "138****5678")
				signal("business_down_checked")
				waitSignal("business_up")
				recovered = call("customers:1")
				require.Contains(t, recovered, `"decision":"allow"`)
				require.Contains(t, recovered, "138****5678")
				after, pageErr = runtime.Store.AuditLogs().Page(ctx, 1, 40)
				require.NoError(t, pageErr)
				require.EqualValues(t, before.Total+4, after.Total)
			}
			require.NoError(t, runtime.Store.Policies().Delete(ctx, grant.ID))
			require.Contains(t, call("customers:1"), `"decision":"deny"`)
			page, err := runtime.Store.AuditLogs().Page(ctx, 1, 40)
			require.NoError(t, err)
			require.GreaterOrEqual(t, page.Total, int64(8))
			for _, entry := range page.List {
				encoded, marshalErr := json.Marshal(entry)
				require.NoError(t, marshalErr)
				require.NotContains(t, string(encoded), "customers:1")
				require.NotContains(t, string(encoded), "13812345678")
			}
			if clientDir != "" {
				_, err = runtime.Store.Policies().Create(ctx, model.Policy{ID: "key-http-grant-clients",
					AgentID: agent.ID, DatasourceID: datasource.ID, ObjectType: "key", ObjectName: "db0:customers:*", Action: "allow"})
				require.NoError(t, err)
				info, marshalErr := json.Marshal(map[string]string{"url": server.URL + "/mcp", "api_key": key,
					"datasource_id": datasource.ID})
				require.NoError(t, marshalErr)
				infoPath := filepath.Join(clientDir, dialect+"-client.json")
				require.NoError(t, os.WriteFile(infoPath, info, 0o600))
				defer os.Remove(infoPath)
				for {
					_, statErr := os.Stat(filepath.Join(clientDir, dialect+"-clients_done"))
					if statErr == nil {
						break
					}
					require.True(t, os.IsNotExist(statErr), "client signal error: %v", statErr)
					select {
					case <-ctx.Done():
						t.Fatal("timed out waiting for Redis/Valkey Inspector and Dify clients")
					case <-time.After(250 * time.Millisecond):
					}
				}
				clientsPage, pageErr := runtime.Store.AuditLogs().Page(ctx, 1, 40)
				require.NoError(t, pageErr)
				require.EqualValues(t, page.Total+6, clientsPage.Total)
			}
		})
	}
}

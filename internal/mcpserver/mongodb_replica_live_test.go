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
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cfg := httpTestConfig(100)
	cfg.Store.SQLitePath = filepath.Join(t.TempDir(), "mongo-replica-mcp.db")
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
}

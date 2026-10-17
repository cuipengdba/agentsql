package mcpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/bootstrap"
	"github.com/cuipengdba/agentsql/internal/config"
	"github.com/cuipengdba/agentsql/internal/mask"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestMySQLColumnMCPRealServer(t *testing.T) {
	if os.Getenv("AGENTSQL_MYSQL_IDENTITY_E2E") != "1" {
		t.Skip("set AGENTSQL_MYSQL_IDENTITY_E2E=1 against the isolated MySQL fixture")
	}
	t.Setenv("AGENTSQL_TEST_MYSQL_IDENTITY_PASSWORD", "")
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	secret := []byte("0123456789abcdef0123456789abcdef")
	path := filepath.Join(t.TempDir(), "mysql-mcp.db")
	control, err := store.OpenWithSecret(ctx, path, secret)
	require.NoError(t, err)
	key, hash, err := store.GenerateAPIKey()
	require.NoError(t, err)
	agent, err := control.Agents().Create(ctx, model.Agent{ID: "mysql-mcp-agent", Name: "MySQL MCP Agent",
		Status: "active", Level: "readonly", APIKeyHash: hash})
	require.NoError(t, err)
	datasource, err := control.Datasources().Create(ctx, model.Datasource{ID: "mysql-mcp-datasource",
		Name: "MySQL MCP", DBType: "mysql", Host: "127.0.0.1", Port: 3306,
		Database: "app", Username: "reader", ConnLimit: 2, StmtTimeoutMS: 5000, RowLimit: 100}, "")
	require.NoError(t, err)
	_, err = control.Policies().Create(ctx, model.Policy{ID: "mysql-mcp-table", AgentID: agent.ID,
		DatasourceID: datasource.ID, ObjectType: "table", ObjectName: "app.customers", Action: "allow"})
	require.NoError(t, err)
	policy, err := control.Policies().Create(ctx, model.Policy{ID: "mysql-mcp-column", AgentID: agent.ID,
		DatasourceID: datasource.ID, ObjectType: "column", ObjectName: "app.customers", Action: "allow"})
	require.NoError(t, err)
	_, err = control.MaskRules().Create(ctx, model.MaskRule{ID: "mysql-mcp-mask", TableName: "customers",
		ColumnName: "phone", SensitiveType: string(mask.TypePhone), Algo: string(mask.AlgoMask), Enabled: true})
	require.NoError(t, err)
	require.NoError(t, control.Close())

	cfg := httpTestConfig(100)
	cfg.Store.SQLitePath = path
	cfg.ColumnAuthorization = config.ColumnAuthorizationConfig{Enabled: true, MySQLSingleTableEnabled: true,
		MySQLMetadataAccounts: map[string]config.MySQLMetadataAccount{datasource.ID: {
			Username: "identity", PasswordEnv: "AGENTSQL_TEST_MYSQL_IDENTITY_PASSWORD"}},
		InstanceID: "mysql-mcp-enrollment", LeaseMS: 5000, HeartbeatIntervalMS: 1000}
	enrollmentRuntime, err := bootstrap.Assemble(ctx, cfg, secret)
	require.NoError(t, err)
	_, err = enrollmentRuntime.BindMySQLColumnPolicy(ctx, policy.ID, policy.Revision, "app", "customers",
		[]bootstrap.MySQLColumnGrant{{Column: "id", Usage: "output"}, {Column: "id", Usage: "reference"},
			{Column: "phone", Usage: "output"}})
	require.NoError(t, err)
	require.NoError(t, enrollmentRuntime.Close())
	cfg.ColumnAuthorization.InstanceID = "mysql-mcp-serving"
	runtime, err := bootstrap.Assemble(ctx, cfg, secret)
	require.NoError(t, err)
	defer runtime.Close()
	require.Equal(t, bootstrap.B2StateActive, runtime.B2Status().State)
	handler, err := NewHTTPHandler(runtime, cfg, zerolog.Nop())
	require.NoError(t, err)
	server := httptest.NewServer(handler)
	defer server.Close()
	for _, body := range []string{initializeRequest, listToolsRequest} {
		status, response, err := doMCPRequest(server.Client(), server.URL+"/mcp", key, http.MethodPost, body)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, status, response)
	}
	allow := `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"query","arguments":{"datasource_id":"mysql-mcp-datasource","sql":"SELECT id, phone FROM app.customers WHERE id = 1 ORDER BY customers.id LIMIT 2"}}}`
	status, response, err := doMCPRequest(server.Client(), server.URL+"/mcp", key, http.MethodPost, allow)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status, response)
	require.Contains(t, response, "138****5678")
	require.NotContains(t, response, "13800005678")
	deny := `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"query","arguments":{"datasource_id":"mysql-mcp-datasource","sql":"SELECT phone FROM app.customers WHERE phone IS NOT NULL ORDER BY customers.id LIMIT 2"}}}`
	status, response, err = doMCPRequest(server.Client(), server.URL+"/mcp", key, http.MethodPost, deny)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status, response)
	require.Contains(t, response, "AUTH_COLUMN_GRANT_MISSING")
	require.NotContains(t, response, "13800005678")
	audits, err := runtime.Store.AuditLogs().FilteredPage(ctx, model.AuditFilter{}, 1, 100)
	require.NoError(t, err)
	require.GreaterOrEqual(t, audits.Total, int64(2))
}

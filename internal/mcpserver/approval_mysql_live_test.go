package mcpserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/audit"
	"github.com/cuipengdba/agentsql/internal/auth"
	executor "github.com/cuipengdba/agentsql/internal/authorizedexecute"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/pipeline"
	mysqlDriver "github.com/go-sql-driver/mysql"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// TestMySQLApprovedWriteRealE2E requires a disposable MySQL instance with
// agentsqltest.customers(id, phone) and a user limited to SELECT/UPDATE.
func TestMySQLApprovedWriteRealE2E(t *testing.T) {
	host := os.Getenv("AGENTSQL_TEST_APPROVAL_MYSQL_HOST")
	if host == "" {
		t.Skip("set AGENTSQL_TEST_APPROVAL_MYSQL_HOST for real approved-write testing")
	}
	password := os.Getenv("AGENTSQL_TEST_APPROVAL_MYSQL_PASSWORD")
	require.NotEmpty(t, password)
	port := 3306
	if raw := os.Getenv("AGENTSQL_TEST_APPROVAL_MYSQL_PORT"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		require.NoError(t, err)
		port = parsed
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	fixture := newMCPFixture(t, "dml")
	datasource, err := fixture.runtime.Store.Datasources().Get(ctx, "ds-allowed")
	require.NoError(t, err)
	datasource.DBType, datasource.Host, datasource.Port = "mysql", host, port
	datasource.Database, datasource.Username = "agentsqltest", "agentsql_rw"
	_, err = fixture.runtime.Store.Datasources().Update(ctx, datasource, password)
	require.NoError(t, err)
	require.NoError(t, fixture.runtime.Store.Policies().Delete(ctx, "table"))
	require.NoError(t, fixture.runtime.Store.Policies().Delete(ctx, "columns"))
	_, err = fixture.runtime.Store.Policies().Create(ctx, model.Policy{
		ID: "mysql-customers", AgentID: fixture.agent.ID, DatasourceID: "ds-allowed",
		ObjectType: "table", ObjectName: "agentsqltest.customers", Action: "allow",
	})
	require.NoError(t, err)
	gateway := executor.NewGateway(false)
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
	config := mysqlDriver.NewConfig()
	config.User, config.Passwd, config.Net = "agentsql_rw", password, "tcp"
	config.Addr, config.DBName = host+":"+strconv.Itoa(port), "agentsqltest"
	business, err := sql.Open("mysql", config.FormatDSN())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, business.Close()) })
	readPhone := func() string {
		t.Helper()
		var phone string
		require.NoError(t, business.QueryRowContext(ctx, "SELECT phone FROM agentsqltest.customers WHERE id=1").Scan(&phone))
		return phone
	}
	require.Equal(t, "before", readPhone())
	server, err := NewServer(ctx, Options{APIKey: fixture.handlers.apiKey, Runtime: fixture.runtime, Logger: zerolog.Nop()})
	require.NoError(t, err)
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverErrors := make(chan error, 1)
	go func() { serverErrors <- server.sdk.Run(ctx, serverTransport) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "mysql-approved-write-e2e", Version: "test"}, nil)
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
	call := func(name string, arguments map[string]any) string {
		t.Helper()
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: arguments})
		require.NoError(t, err)
		encoded, err := json.Marshal(result)
		require.NoError(t, err)
		return string(encoded)
	}
	requested := call("request_approval", map[string]any{
		"datasource_id": "ds-allowed", "sql": "UPDATE agentsqltest.customers SET phone='after' WHERE id=1",
		"reason": "approved-write integration test",
	})
	require.Contains(t, requested, `"decision":"approve"`)
	require.Equal(t, "before", readPhone(), "requesting approval must not execute SQL")
	pending, err := fixture.runtime.Store.Approvals().ListPage(ctx, "pending", 1, 10)
	require.NoError(t, err)
	require.Len(t, pending.List, 1)
	approval := pending.List[0]
	_, err = fixture.runtime.Store.Approvals().DecidePending(ctx, approval.ID, "approved", "test-dba", nil, time.Now().UTC())
	require.NoError(t, err)
	executeArgs := map[string]any{"approval_id": approval.ID}
	require.Contains(t, call("execute_approved", executeArgs), `"decision":"allow"`)
	require.Equal(t, "after", readPhone())
	completed, err := fixture.runtime.Store.Approvals().Get(ctx, approval.ID)
	require.NoError(t, err)
	require.Equal(t, "completed", completed.Status)
	require.NotNil(t, completed.ExecutionAuditID)
	require.Contains(t, call("execute_approved", executeArgs), `"decision":"deny"`)
	require.Equal(t, "after", readPhone())
}

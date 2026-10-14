package mcpserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/url"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/audit"
	"github.com/cuipengdba/agentsql/internal/auth"
	executor "github.com/cuipengdba/agentsql/internal/authorizedexecute"
	"github.com/cuipengdba/agentsql/internal/pipeline"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// TestPostgresApprovedWriteRealE2E requires a disposable PostgreSQL instance
// with public.customers(id, phone) and a user limited to SELECT/UPDATE on it.
func TestPostgresApprovedWriteRealE2E(t *testing.T) {
	host := os.Getenv("AGENTSQL_TEST_APPROVAL_PG_HOST")
	if host == "" {
		t.Skip("set AGENTSQL_TEST_APPROVAL_PG_HOST for real approved-write testing")
	}
	password := os.Getenv("AGENTSQL_TEST_APPROVAL_PG_PASSWORD")
	require.NotEmpty(t, password)
	port := 5432
	if raw := os.Getenv("AGENTSQL_TEST_APPROVAL_PG_PORT"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		require.NoError(t, err)
		port = parsed
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	fixture := newMCPFixture(t, "dml")
	datasource, err := fixture.runtime.Store.Datasources().Get(ctx, "ds-allowed")
	require.NoError(t, err)
	datasource.Host, datasource.Port, datasource.Database = host, port, "agentsqltest"
	datasource.Username = "agentsql_rw"
	_, err = fixture.runtime.Store.Datasources().Update(ctx, datasource, password)
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
	address := &url.URL{Scheme: "postgres", Host: host + ":" + strconv.Itoa(port), Path: "/agentsqltest"}
	address.User = url.UserPassword("agentsql_rw", password)
	query := address.Query()
	query.Set("sslmode", "disable")
	address.RawQuery = query.Encode()
	business, err := sql.Open("pgx", address.String())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, business.Close()) })
	readPhone := func() string {
		t.Helper()
		var phone string
		require.NoError(t, business.QueryRowContext(ctx, "SELECT phone FROM public.customers WHERE id=1").Scan(&phone))
		return phone
	}
	require.Equal(t, "before", readPhone())
	server, err := NewServer(ctx, Options{APIKey: fixture.handlers.apiKey, Runtime: fixture.runtime, Logger: zerolog.Nop()})
	require.NoError(t, err)
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverErrors := make(chan error, 1)
	go func() { serverErrors <- server.sdk.Run(ctx, serverTransport) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "approved-write-e2e", Version: "test"}, nil)
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
		"datasource_id": "ds-allowed", "sql": "UPDATE public.customers SET phone='after' WHERE id=1",
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

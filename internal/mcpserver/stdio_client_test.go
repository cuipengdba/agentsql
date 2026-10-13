package mcpserver

import (
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// Exercise the production stdio wire with the official MCP client transport.
// This also proves that a non-default Agent keeps its repository tenant.
func TestStandardStdioClientScopesTenant(t *testing.T) {
	fixture := newMCPFixture(t, "readonly")
	base := context.Background()
	_, err := fixture.runtime.Store.RBAC().CreateTenant(base, model.Tenant{
		ID: "stdio_tenant", Name: "Stdio Tenant", Status: "active",
	})
	require.NoError(t, err)
	scoped, err := store.WithTenant(base, "stdio_tenant")
	require.NoError(t, err)
	key, hash, err := store.GenerateAPIKey()
	require.NoError(t, err)
	agent, err := fixture.runtime.Store.Agents().Create(scoped, model.Agent{
		ID: "stdio-agent", Name: "Stdio Agent", Status: "active", APIKeyHash: hash, Level: "readonly",
	})
	require.NoError(t, err)
	_, err = fixture.runtime.Store.Datasources().Create(scoped, model.Datasource{
		ID: "stdio-ds", Name: "Stdio datasource", DBType: "postgres",
		Host: "stdio.internal", Port: 5432, Database: "stdio", Username: "reader",
		ConnLimit: 2, StmtTimeoutMS: 5_000, RowLimit: 100,
	}, "database-password")
	require.NoError(t, err)
	_, err = fixture.runtime.Store.Policies().Create(scoped, model.Policy{
		ID: "stdio-table", AgentID: agent.ID, DatasourceID: "stdio-ds",
		ObjectType: "table", ObjectName: "public.customers", Action: "allow",
	})
	require.NoError(t, err)
	columns := "id,phone"
	_, err = fixture.runtime.Store.Policies().Create(scoped, model.Policy{
		ID: "stdio-columns", AgentID: agent.ID, DatasourceID: "stdio-ds",
		ObjectType: "column", ObjectName: "public.customers", Columns: &columns, Action: "allow",
	})
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(base, 10*time.Second)
	defer cancel()
	server, err := NewServer(ctx, Options{APIKey: key, Runtime: fixture.runtime, Logger: zerolog.Nop()})
	require.NoError(t, err)
	serverInput, clientOutput := io.Pipe()
	clientInput, serverOutput := io.Pipe()
	serverErrors := make(chan error, 1)
	go func() { serverErrors <- server.RunStdioStreams(ctx, serverInput, serverOutput) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "stdio-client-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.IOTransport{Reader: clientInput, Writer: clientOutput}, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = session.Close()
		cancel()
		select {
		case <-serverErrors:
		case <-time.After(time.Second):
		}
	})

	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "list_datasources", Arguments: map[string]any{}})
	require.NoError(t, err)
	encoded, err := json.Marshal(result)
	require.NoError(t, err)
	require.Contains(t, string(encoded), "stdio-ds")
	require.NotContains(t, string(encoded), "ds-allowed")
}

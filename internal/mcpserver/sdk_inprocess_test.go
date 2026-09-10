package mcpserver

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestSDKInProcessListsAndCallsSevenTools(t *testing.T) {
	fixture := newMCPFixture(t, "dml")
	_, err := fixture.runtime.Store.Approvals().Create(context.Background(), model.Approval{
		ID: "sdk-owned", AgentID: stringPointerForMCP(fixture.agent.ID), Status: "pending",
	})
	require.NoError(t, err)
	server, err := NewServer(context.Background(), Options{
		APIKey: fixture.handlers.apiKey, Runtime: fixture.runtime,
		Logger: zerolog.Nop(), Version: "test",
	})
	require.NoError(t, err)
	server.handlers.executorFor = fixture.handlers.executorFor

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverErrors := make(chan error, 1)
	go func() {
		serverErrors <- server.sdk.Run(ctx, serverTransport)
	}()
	client := mcp.NewClient(&mcp.Implementation{Name: "agentsql-test", Version: "test"}, nil)
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

	listed, err := session.ListTools(ctx, nil)
	require.NoError(t, err)
	names := make([]string, 0, len(listed.Tools))
	for _, tool := range listed.Tools {
		names = append(names, tool.Name)
		encoded, marshalError := json.Marshal(tool)
		require.NoError(t, marshalError)
		require.Contains(t, string(encoded), "inputSchema")
		require.Contains(t, string(encoded), "description")
		require.True(t, strings.ContainsAny(string(encoded), "数据库查询写入审批权限"))
	}
	sort.Strings(names)
	require.Equal(t, []string{
		"execute_write",
		"explain_query",
		"get_approval_result",
		"list_datasources",
		"list_schema",
		"query",
		"request_approval",
	}, names)
	require.NotContains(t, names, "execute_raw_sql")

	calls := []mcp.CallToolParams{
		{Name: "list_datasources", Arguments: map[string]any{}},
		{Name: "list_schema", Arguments: map[string]any{"datasource_id": "ds-allowed", "table": "public.customers"}},
		{Name: "explain_query", Arguments: map[string]any{"datasource_id": "ds-allowed", "sql": "SELECT phone FROM public.customers LIMIT 1"}},
		{Name: "query", Arguments: map[string]any{"datasource_id": "ds-allowed", "sql": "SELECT phone FROM public.customers LIMIT 1"}},
		{Name: "execute_write", Arguments: map[string]any{"datasource_id": "ds-allowed", "sql": "UPDATE public.customers SET phone='x' WHERE id=1", "reason": "test"}},
		{Name: "request_approval", Arguments: map[string]any{"datasource_id": "ds-allowed", "sql": "SELECT phone FROM public.customers LIMIT 1", "reason": "test"}},
		{Name: "get_approval_result", Arguments: map[string]any{"approval_id": "sdk-owned"}},
	}
	for _, call := range calls {
		result, err := session.CallTool(ctx, &call)
		require.NoError(t, err, call.Name)
		require.NotNil(t, result, call.Name)
	}
}

func TestNewServerFailsClosedBeforeServing(t *testing.T) {
	fixture := newMCPFixture(t, "dml")
	_, err := NewServer(context.Background(), Options{Runtime: fixture.runtime})
	require.Error(t, err)
	_, err = NewServer(context.Background(), Options{
		APIKey: "asql_invalid", Runtime: fixture.runtime,
	})
	require.Error(t, err)
}

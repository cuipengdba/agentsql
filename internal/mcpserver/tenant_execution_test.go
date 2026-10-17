package mcpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// An Agent key must carry its persisted tenant through SDK dispatch, the
// protected query pipeline, approval reads, and durable audit writes.
func TestHTTPAgentTenantScopesExecutionAndAudit(t *testing.T) {
	fixture := newMCPFixture(t, "readonly")
	base := context.Background()
	_, err := fixture.runtime.Store.RBAC().CreateTenant(base, model.Tenant{
		ID: "tenant_b", Name: "Tenant B", Status: "active",
	})
	require.NoError(t, err)
	tenantB, err := store.WithTenant(base, "tenant_b")
	require.NoError(t, err)

	keyB, hashB, err := store.GenerateAPIKey()
	require.NoError(t, err)
	agentB, err := fixture.runtime.Store.Agents().Create(tenantB, model.Agent{
		ID: "agent-b", Name: "Agent B", Status: "active", APIKeyHash: hashB, Level: "readonly",
	})
	require.NoError(t, err)
	_, err = fixture.runtime.Store.Datasources().Create(tenantB, model.Datasource{
		ID: "ds-b", Name: "Tenant B datasource", DBType: "postgres",
		Host: "b.internal", Port: 5432, Database: "b", Username: "reader",
		ConnLimit: 5, StmtTimeoutMS: 5_000, RowLimit: 100,
	}, "database-password")
	require.NoError(t, err)
	_, err = fixture.runtime.Store.Policies().Create(tenantB, model.Policy{
		ID: "tenant-b-table", AgentID: agentB.ID, DatasourceID: "ds-b",
		ObjectType: "table", ObjectName: "public.customers", Action: "allow",
	})
	require.NoError(t, err)
	_, err = fixture.runtime.Store.Approvals().Create(tenantB, model.Approval{
		ID: "tenant-b-approval", AgentID: &agentB.ID, Status: "pending",
	})
	require.NoError(t, err)
	_, err = fixture.runtime.Store.Approvals().Create(base, model.Approval{
		ID: "tenant-a-approval", AgentID: &fixture.agent.ID, Status: "pending",
	})
	require.NoError(t, err)

	handler, err := NewHTTPHandler(fixture.runtime, httpTestConfig(100), zerolog.Nop())
	require.NoError(t, err)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	call := func(key, body string) string {
		t.Helper()
		status, response, callErr := doMCPRequest(server.Client(), server.URL+"/mcp", key, http.MethodPost, body)
		require.NoError(t, callErr)
		require.Equal(t, http.StatusOK, status, response)
		return response
	}

	bSources := call(keyB, listSourcesRequest)
	require.Contains(t, bSources, "ds-b")
	require.NotContains(t, bSources, "ds-allowed")
	aSources := call(fixture.handlers.apiKey, listSourcesRequest)
	require.Contains(t, aSources, "ds-allowed")
	require.NotContains(t, aSources, "ds-b")

	approvalB := `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"get_approval_result","arguments":{"approval_id":"tenant-b-approval"}}}`
	approvalA := `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"get_approval_result","arguments":{"approval_id":"tenant-a-approval"}}}`
	require.Contains(t, call(keyB, approvalB), `"decision":"allow"`)
	require.Contains(t, call(fixture.handlers.apiKey, approvalB), `"decision":"deny"`)
	require.Contains(t, call(keyB, approvalA), `"decision":"deny"`)

	queryB := `{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"query","arguments":{"datasource_id":"ds-b","sql":"SELECT phone FROM public.customers LIMIT 1"}}}`
	require.Contains(t, call(keyB, queryB), `"decision":"allow"`)
	require.Contains(t, call(fixture.handlers.apiKey, queryB), `"decision":"deny"`)
	bAudit, err := fixture.runtime.Store.AuditLogs().Page(tenantB, 1, 10)
	require.NoError(t, err)
	require.Positive(t, bAudit.Total)
	for _, entry := range bAudit.List {
		require.Equal(t, "tenant_b", entry.TenantID)
		require.Equal(t, &agentB.ID, entry.AgentID)
	}
	aAudit, err := fixture.runtime.Store.AuditLogs().Page(base, 1, 10)
	require.NoError(t, err)
	for _, entry := range aAudit.List {
		require.Equal(t, store.DefaultTenantID, entry.TenantID)
		require.NotNil(t, entry.AgentID)
		require.NotEqual(t, agentB.ID, *entry.AgentID)
	}
}

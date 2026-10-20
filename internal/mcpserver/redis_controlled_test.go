package mcpserver

import (
	"context"
	"encoding/json"
	"testing"

	executor "github.com/cuipengdba/agentsql/internal/authorizedexecute"
	"github.com/cuipengdba/agentsql/internal/mask"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestControlledKeyReadAuthorizationMaskAuditAndRevocation(t *testing.T) {
	fixture := newMCPFixture(t, "readonly")
	ctx := context.Background()
	datasource := model.Datasource{ID: "redis-controlled", Name: "Redis controlled", DBType: "redis", Host: "redis.test", Port: 6379,
		Database: "0", Username: "agent_reader", TLSMode: "strict", TLSCAFile: "test-ca.crt", StmtTimeoutMS: 2000, ConnLimit: 2}
	_, err := fixture.runtime.Store.Datasources().Create(ctx, datasource, "test-password")
	require.NoError(t, err)
	grant, err := fixture.runtime.Store.Policies().Create(ctx, model.Policy{ID: "key-grant", AgentID: fixture.agent.ID,
		DatasourceID: datasource.ID, ObjectType: "key", ObjectName: "db0:customers:*", Action: "allow"})
	require.NoError(t, err)
	redactor, err := mask.NewRedactor([]mask.Rule{{Schema: "db0", Table: "customers:1", Column: "value",
		SensitiveType: mask.TypePhone, Algorithm: mask.AlgoMask}})
	require.NoError(t, err)
	fixture.handlers.keyRedactor = func(context.Context, string) (mask.Redactor, error) { return redactor, nil }
	fixture.handlers.keyAuditIdentity = func(string, int, string) (string, error) { return "db0:hmac-sha256:test", nil }
	calls := 0
	fixture.handlers.keyRead = func(_ context.Context, selected model.Datasource, request executor.RedisReadRequest) (model.QueryResult, error) {
		calls++
		require.Equal(t, datasource.ID, selected.ID)
		require.Equal(t, "customers:1", request.Key)
		return model.QueryResult{Columns: []string{"value"}, Rows: [][]string{{"13812345678"}}, RowCount: 1}, nil
	}
	allowed := fixture.handlers.readKey(ctx, keyReadInput{DatasourceID: datasource.ID, Key: "customers:1"})
	require.Equal(t, "allow", allowed.Decision, allowed.Reason)
	encoded, err := json.Marshal(allowed)
	require.NoError(t, err)
	require.Contains(t, string(encoded), "138****5678")
	require.NotContains(t, string(encoded), "13812345678")
	require.Equal(t, 1, calls)
	page, err := fixture.runtime.Store.AuditLogs().Page(ctx, 1, 10)
	require.NoError(t, err)
	require.EqualValues(t, 2, page.Total)
	for _, event := range page.List {
		require.Equal(t, "read_key", *event.MCPTool)
		require.NotContains(t, *event.DetailsJSON, "customers:1")
		require.NotContains(t, *event.DetailsJSON, "13812345678")
		if event.Objects != nil {
			require.NotContains(t, *event.Objects, "customers:1")
		}
	}
	require.Equal(t, "deny", fixture.handlers.readKey(ctx, keyReadInput{DatasourceID: datasource.ID, Key: "private:1"}).Decision)
	require.Equal(t, "deny", fixture.handlers.readKey(ctx, keyReadInput{DatasourceID: datasource.ID, Key: "customers:*"}).Decision)
	require.Equal(t, 1, calls)
	require.NoError(t, fixture.runtime.Store.Policies().Delete(ctx, grant.ID))
	require.Equal(t, "deny", fixture.handlers.readKey(ctx, keyReadInput{DatasourceID: datasource.ID, Key: "customers:1"}).Decision)
	require.Equal(t, 1, calls)
}

func TestControlledKeyReadWithholdsOnTerminalAuditFailure(t *testing.T) {
	fixture := newMCPFixture(t, "readonly")
	ctx := context.Background()
	datasource := model.Datasource{ID: "redis-audit-fault", Name: "Redis audit fault", DBType: "redis", Host: "redis.test", Port: 6379,
		Database: "0", Username: "agent_reader", TLSMode: "strict", TLSCAFile: "test-ca.crt", StmtTimeoutMS: 2000, ConnLimit: 2}
	_, err := fixture.runtime.Store.Datasources().Create(ctx, datasource, "test-password")
	require.NoError(t, err)
	_, err = fixture.runtime.Store.Policies().Create(ctx, model.Policy{ID: "key-grant-fault", AgentID: fixture.agent.ID,
		DatasourceID: datasource.ID, ObjectType: "key", ObjectName: "db0:customers:1", Action: "allow"})
	require.NoError(t, err)
	redactor, err := mask.NewRedactor(nil)
	require.NoError(t, err)
	fixture.handlers.keyRedactor = func(context.Context, string) (mask.Redactor, error) { return redactor, nil }
	fixture.handlers.keyAuditIdentity = func(string, int, string) (string, error) { return "db0:hmac-sha256:test", nil }
	fixture.handlers.keyRead = func(context.Context, model.Datasource, executor.RedisReadRequest) (model.QueryResult, error) {
		require.NoError(t, fixture.runtime.Store.Close())
		return model.QueryResult{Columns: []string{"value"}, Rows: [][]string{{"private-value"}}, RowCount: 1}, nil
	}
	response := fixture.handlers.readKey(ctx, keyReadInput{DatasourceID: datasource.ID, Key: "customers:1"})
	require.Equal(t, "error", response.Decision)
	encoded, err := json.Marshal(response)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "private-value")
}

func TestControlledKeyReadWithholdsWhenRequestCancelledAfterBusinessRead(t *testing.T) {
	fixture := newMCPFixture(t, "readonly")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	datasource := model.Datasource{ID: "redis-cancel", Name: "Redis cancel", DBType: "redis", Host: "redis.test", Port: 6379,
		Database: "0", Username: "agent_reader", TLSMode: "strict", TLSCAFile: "test-ca.crt", StmtTimeoutMS: 2000, ConnLimit: 2}
	_, err := fixture.runtime.Store.Datasources().Create(ctx, datasource, "test-password")
	require.NoError(t, err)
	_, err = fixture.runtime.Store.Policies().Create(ctx, model.Policy{ID: "key-grant-cancel", AgentID: fixture.agent.ID,
		DatasourceID: datasource.ID, ObjectType: "key", ObjectName: "db0:customers:1", Action: "allow"})
	require.NoError(t, err)
	redactor, err := mask.NewRedactor(nil)
	require.NoError(t, err)
	fixture.handlers.keyRedactor = func(context.Context, string) (mask.Redactor, error) { return redactor, nil }
	fixture.handlers.keyAuditIdentity = func(string, int, string) (string, error) { return "db0:hmac-sha256:test", nil }
	fixture.handlers.keyRead = func(context.Context, model.Datasource, executor.RedisReadRequest) (model.QueryResult, error) {
		cancel()
		return model.QueryResult{Columns: []string{"value"}, Rows: [][]string{{"private-value"}}, RowCount: 1}, nil
	}
	response := fixture.handlers.readKey(ctx, keyReadInput{DatasourceID: datasource.ID, Key: "customers:1"})
	require.Equal(t, "error", response.Decision)
	encoded, err := json.Marshal(response)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "private-value")
}

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

func TestControlledHashReadRequiresExactKeyAndFieldThenMasksAndAudits(t *testing.T) {
	fixture := newMCPFixture(t, "readonly")
	ctx := context.Background()
	datasource := model.Datasource{ID: "hash-controlled", Name: "Hash controlled", DBType: "redis", Host: "redis.test", Port: 6379,
		Database: "0", Username: "agent_reader", TLSMode: "strict", TLSCAFile: "test-ca.crt", StmtTimeoutMS: 2000, ConnLimit: 2}
	_, err := fixture.runtime.Store.Datasources().Create(ctx, datasource, "test-password")
	require.NoError(t, err)
	_, err = fixture.runtime.Store.Policies().Create(ctx, model.Policy{ID: "hash-prefix", AgentID: fixture.agent.ID,
		DatasourceID: datasource.ID, ObjectType: "key", ObjectName: "db0:customers:*", Action: "allow"})
	require.NoError(t, err)
	_, err = fixture.runtime.Store.Policies().Create(ctx, model.Policy{ID: "hash-field-phone", AgentID: fixture.agent.ID,
		DatasourceID: datasource.ID, ObjectType: "key_field", ObjectName: "db0:customers:1#phone", Action: "allow"})
	require.NoError(t, err)
	redactor, err := mask.NewRedactor([]mask.Rule{{Schema: "db0", Table: "customers:1", Column: "phone",
		SensitiveType: mask.TypePhone, Algorithm: mask.AlgoMask}})
	require.NoError(t, err)
	fixture.handlers.keyRedactor = func(context.Context, string) (mask.Redactor, error) { return redactor, nil }
	fixture.handlers.keyAuditIdentity = func(string, int, string) (string, error) { return "db0:hmac-sha256:test", nil }
	calls := 0
	fixture.handlers.hashRead = func(_ context.Context, selected model.Datasource, request executor.RedisHashReadRequest) (model.QueryResult, error) {
		calls++
		require.Equal(t, datasource.ID, selected.ID)
		require.Equal(t, "customers:1", request.Key)
		require.Equal(t, []string{"phone"}, request.Fields)
		return model.QueryResult{Columns: []string{"phone"}, Rows: [][]string{{"13812345678"}}, RowCount: 1}, nil
	}
	input := hashReadInput{DatasourceID: datasource.ID, Key: "customers:1", Fields: []string{"phone"}}
	require.Equal(t, "deny", fixture.handlers.readHashFields(ctx, input).Decision, "prefix key grants must not expose Hash fields")
	require.Zero(t, calls)
	grant, err := fixture.runtime.Store.Policies().Create(ctx, model.Policy{ID: "hash-key-exact", AgentID: fixture.agent.ID,
		DatasourceID: datasource.ID, ObjectType: "key", ObjectName: "db0:customers:1", Action: "allow"})
	require.NoError(t, err)
	allowed := fixture.handlers.readHashFields(ctx, input)
	require.Equal(t, "allow", allowed.Decision, allowed.Reason)
	encoded, err := json.Marshal(allowed)
	require.NoError(t, err)
	require.Contains(t, string(encoded), "138****5678")
	require.NotContains(t, string(encoded), "13812345678")
	require.Equal(t, 1, calls)
	page, err := fixture.runtime.Store.AuditLogs().Page(ctx, 1, 10)
	require.NoError(t, err)
	for _, event := range page.List {
		require.Equal(t, "read_hash_fields", *event.MCPTool)
		require.NotContains(t, *event.DetailsJSON, "13812345678")
	}
	input.Fields = []string{"secret"}
	require.Equal(t, "deny", fixture.handlers.readHashFields(ctx, input).Decision)
	require.Equal(t, 1, calls)
	require.NoError(t, fixture.runtime.Store.Policies().Delete(ctx, grant.ID))
	input.Fields = []string{"phone"}
	require.Equal(t, "deny", fixture.handlers.readHashFields(ctx, input).Decision)
	require.Equal(t, 1, calls)
}

func TestControlledHashReadWithholdsValueWhenTerminalAuditFails(t *testing.T) {
	fixture := newMCPFixture(t, "readonly")
	ctx := context.Background()
	datasource := model.Datasource{ID: "hash-audit-fault", Name: "Hash audit fault", DBType: "valkey", Host: "valkey.test", Port: 6379,
		Database: "0", Username: "agent_reader", TLSMode: "strict", TLSCAFile: "test-ca.crt", StmtTimeoutMS: 2000, ConnLimit: 2}
	_, err := fixture.runtime.Store.Datasources().Create(ctx, datasource, "test-password")
	require.NoError(t, err)
	for _, grant := range []model.Policy{
		{ID: "hash-fault-key", AgentID: fixture.agent.ID, DatasourceID: datasource.ID, ObjectType: "key", ObjectName: "db0:customers:1", Action: "allow"},
		{ID: "hash-fault-field", AgentID: fixture.agent.ID, DatasourceID: datasource.ID, ObjectType: "key_field", ObjectName: "db0:customers:1#secret", Action: "allow"},
	} {
		_, err := fixture.runtime.Store.Policies().Create(ctx, grant)
		require.NoError(t, err)
	}
	redactor, err := mask.NewRedactor(nil)
	require.NoError(t, err)
	fixture.handlers.keyRedactor = func(context.Context, string) (mask.Redactor, error) { return redactor, nil }
	fixture.handlers.keyAuditIdentity = func(string, int, string) (string, error) { return "db0:hmac-sha256:test", nil }
	fixture.handlers.hashRead = func(context.Context, model.Datasource, executor.RedisHashReadRequest) (model.QueryResult, error) {
		require.NoError(t, fixture.runtime.Store.Close())
		return model.QueryResult{Columns: []string{"secret"}, Rows: [][]string{{"private-value"}}, RowCount: 1}, nil
	}
	response := fixture.handlers.readHashFields(ctx, hashReadInput{DatasourceID: datasource.ID, Key: "customers:1", Fields: []string{"secret"}})
	require.Equal(t, "error", response.Decision)
	encoded, err := json.Marshal(response)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "private-value")
}

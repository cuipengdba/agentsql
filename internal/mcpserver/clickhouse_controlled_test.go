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

func TestClickHouseControlledAuthorizationMaskAuditAndRevocation(t *testing.T) {
	fixture := newMCPFixture(t, "readonly")
	ctx := context.Background()
	datasource := model.Datasource{ID: "clickhouse-controlled", Name: "ClickHouse controlled", DBType: "clickhouse",
		Host: "clickhouse.test", Port: 9440, Database: "analytics", Username: "agent_reader", TLSMode: "strict",
		TLSCAFile: "test-ca.pem", StmtTimeoutMS: 2000, ConnLimit: 1, RowLimit: 100}
	_, err := fixture.runtime.Store.Datasources().Create(ctx, datasource, "test-password")
	require.NoError(t, err)
	grant, err := fixture.runtime.Store.Policies().Create(ctx, model.Policy{ID: "clickhouse-table", AgentID: fixture.agent.ID,
		DatasourceID: datasource.ID, ObjectType: "table", ObjectName: "analytics.events", Action: "allow"})
	require.NoError(t, err)
	columns := "name,phone,region"
	_, err = fixture.runtime.Store.Policies().Create(ctx, model.Policy{ID: "clickhouse-columns", AgentID: fixture.agent.ID,
		DatasourceID: datasource.ID, ObjectType: "column", ObjectName: "analytics.events", Columns: &columns, Action: "allow"})
	require.NoError(t, err)
	redactor, err := mask.NewRedactor([]mask.Rule{{Schema: "analytics", Table: "events", Column: "phone",
		SensitiveType: mask.TypePhone, Algorithm: mask.AlgoMask}})
	require.NoError(t, err)
	fixture.handlers.analyticsRedactor = func(context.Context, string) (mask.Redactor, error) { return redactor, nil }
	calls := 0
	fixture.handlers.analyticsRead = func(_ context.Context, selected model.Datasource, request executor.ClickHouseReadRequest) (model.QueryResult, error) {
		calls++
		require.Equal(t, datasource.ID, selected.ID)
		require.Equal(t, "events", request.Table)
		return model.QueryResult{Columns: []string{"name", "phone"}, Rows: [][]string{{"alice", "13812345678"}}, RowCount: 1}, nil
	}
	input := analyticsInput{DatasourceID: datasource.ID, Table: "events", Fields: []string{"name", "phone"},
		Filter: map[string]string{"region": "east"}, Limit: 2}
	response := fixture.handlers.queryAnalytics(ctx, input)
	require.Equal(t, "allow", response.Decision, response.Reason)
	require.Equal(t, 1, calls)
	data := response.Data.(pipelineData)
	require.Positive(t, data.AuditID)
	require.Equal(t, 1, data.Redact.MaskedCells)
	encoded, err := json.Marshal(data.Result)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "13812345678")
	page, err := fixture.runtime.Store.AuditLogs().Page(ctx, 1, 10)
	require.NoError(t, err)
	require.EqualValues(t, 2, page.Total)
	for _, event := range page.List {
		require.Equal(t, "query_analytics", *event.MCPTool)
		require.Equal(t, "analytics.events", *event.Objects)
		require.Nil(t, event.SQLRaw)
		require.NotContains(t, *event.DetailsJSON, "east")
	}
	require.NoError(t, fixture.runtime.Store.Policies().Delete(ctx, grant.ID))
	require.Equal(t, "deny", fixture.handlers.queryAnalytics(ctx, input).Decision)
	require.Equal(t, 1, calls)
}

func TestClickHouseControlledRejectsBroadGrantsAndUnownedColumnsBeforeDB(t *testing.T) {
	fixture := newMCPFixture(t, "readonly")
	ctx := context.Background()
	datasource := model.Datasource{ID: "clickhouse-closed", Name: "ClickHouse closed", DBType: "clickhouse",
		Host: "clickhouse.test", Port: 9440, Database: "analytics", Username: "agent_reader", TLSMode: "strict",
		TLSCAFile: "test-ca.pem", StmtTimeoutMS: 2000, ConnLimit: 1, RowLimit: 100}
	_, err := fixture.runtime.Store.Datasources().Create(ctx, datasource, "test-password")
	require.NoError(t, err)
	_, err = fixture.runtime.Store.Policies().Create(ctx, model.Policy{ID: "clickhouse-broad", AgentID: fixture.agent.ID,
		DatasourceID: datasource.ID, ObjectType: "schema", ObjectName: "analytics.*", Action: "allow"})
	require.NoError(t, err)
	calls := 0
	fixture.handlers.analyticsRead = func(context.Context, model.Datasource, executor.ClickHouseReadRequest) (model.QueryResult, error) {
		calls++
		return model.QueryResult{}, nil
	}
	input := analyticsInput{DatasourceID: datasource.ID, Table: "events", Fields: []string{"name"},
		Filter: map[string]string{"region": "east"}, Limit: 1}
	require.Equal(t, "deny", fixture.handlers.queryAnalytics(ctx, input).Decision)
	_, err = fixture.runtime.Store.Policies().Create(ctx, model.Policy{ID: "clickhouse-exact", AgentID: fixture.agent.ID,
		DatasourceID: datasource.ID, ObjectType: "table", ObjectName: "analytics.events", Action: "allow"})
	require.NoError(t, err)
	require.Equal(t, "deny", fixture.handlers.queryAnalytics(ctx, input).Decision, "explicit column policy is mandatory")
	columns := "name"
	_, err = fixture.runtime.Store.Policies().Create(ctx, model.Policy{ID: "clickhouse-column", AgentID: fixture.agent.ID,
		DatasourceID: datasource.ID, ObjectType: "column", ObjectName: "analytics.events", Columns: &columns, Action: "allow"})
	require.NoError(t, err)
	require.Equal(t, "deny", fixture.handlers.queryAnalytics(ctx, input).Decision, "filter column also needs permission")
	require.Zero(t, calls)
}

func TestClickHouseControlledWithholdsResultWhenTerminalAuditFails(t *testing.T) {
	fixture := newMCPFixture(t, "readonly")
	ctx := context.Background()
	datasource := model.Datasource{ID: "clickhouse-audit-fault", Name: "ClickHouse audit fault", DBType: "clickhouse",
		Host: "clickhouse.test", Port: 9440, Database: "analytics", Username: "agent_reader", TLSMode: "strict",
		TLSCAFile: "test-ca.pem", StmtTimeoutMS: 2000, ConnLimit: 1, RowLimit: 100}
	_, err := fixture.runtime.Store.Datasources().Create(ctx, datasource, "test-password")
	require.NoError(t, err)
	_, err = fixture.runtime.Store.Policies().Create(ctx, model.Policy{ID: "clickhouse-audit-table", AgentID: fixture.agent.ID,
		DatasourceID: datasource.ID, ObjectType: "table", ObjectName: "analytics.events", Action: "allow"})
	require.NoError(t, err)
	columns := "name,region"
	_, err = fixture.runtime.Store.Policies().Create(ctx, model.Policy{ID: "clickhouse-audit-columns", AgentID: fixture.agent.ID,
		DatasourceID: datasource.ID, ObjectType: "column", ObjectName: "analytics.events", Columns: &columns, Action: "allow"})
	require.NoError(t, err)
	redactor, err := mask.NewRedactor(nil)
	require.NoError(t, err)
	fixture.handlers.analyticsRedactor = func(context.Context, string) (mask.Redactor, error) { return redactor, nil }
	fixture.handlers.analyticsRead = func(context.Context, model.Datasource, executor.ClickHouseReadRequest) (model.QueryResult, error) {
		require.NoError(t, fixture.runtime.Store.Close())
		return model.QueryResult{Columns: []string{"name"}, Rows: [][]string{{"synthetic-private-value"}}, RowCount: 1}, nil
	}
	response := fixture.handlers.queryAnalytics(ctx, analyticsInput{DatasourceID: datasource.ID, Table: "events",
		Fields: []string{"name"}, Filter: map[string]string{"region": "CN"}, Limit: 1})
	require.Equal(t, "error", response.Decision)
	encoded, err := json.Marshal(response)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "synthetic-private-value")
}

func TestClickHouseControlledCancellationWritesErrorTerminal(t *testing.T) {
	fixture := newMCPFixture(t, "readonly")
	ctx := context.Background()
	datasource := model.Datasource{ID: "clickhouse-cancel", Name: "ClickHouse cancel", DBType: "clickhouse",
		Host: "clickhouse.test", Port: 9440, Database: "analytics", Username: "agent_reader", TLSMode: "strict",
		TLSCAFile: "test-ca.pem", StmtTimeoutMS: 10, ConnLimit: 1, RowLimit: 100}
	_, err := fixture.runtime.Store.Datasources().Create(ctx, datasource, "test-password")
	require.NoError(t, err)
	_, err = fixture.runtime.Store.Policies().Create(ctx, model.Policy{ID: "clickhouse-cancel-table", AgentID: fixture.agent.ID,
		DatasourceID: datasource.ID, ObjectType: "table", ObjectName: "analytics.events", Action: "allow"})
	require.NoError(t, err)
	columns := "name,region"
	_, err = fixture.runtime.Store.Policies().Create(ctx, model.Policy{ID: "clickhouse-cancel-columns", AgentID: fixture.agent.ID,
		DatasourceID: datasource.ID, ObjectType: "column", ObjectName: "analytics.events", Columns: &columns, Action: "allow"})
	require.NoError(t, err)
	fixture.handlers.analyticsRead = func(timed context.Context, _ model.Datasource, _ executor.ClickHouseReadRequest) (model.QueryResult, error) {
		<-timed.Done()
		return model.QueryResult{}, timed.Err()
	}
	response := fixture.handlers.queryAnalytics(ctx, analyticsInput{DatasourceID: datasource.ID, Table: "events",
		Fields: []string{"name"}, Filter: map[string]string{"region": "CN"}, Limit: 1})
	require.Equal(t, "error", response.Decision)
	page, err := fixture.runtime.Store.AuditLogs().Page(ctx, 1, 10)
	require.NoError(t, err)
	require.EqualValues(t, 2, page.Total)
	terminalFound := false
	for _, event := range page.List {
		terminalFound = terminalFound || event.Action != nil && *event.Action == "analytics_query_error"
	}
	require.True(t, terminalFound)
}

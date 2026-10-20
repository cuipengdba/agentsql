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

func TestMongoFindDocumentAuthorizationMaskAndAudit(t *testing.T) {
	fixture := newMCPFixture(t, "readonly")
	ctx := context.Background()
	datasource := model.Datasource{
		ID: "mongo-controlled", Name: "Mongo controlled", DBType: "mongodb", Host: "mongo.test",
		Port: 27017, Database: "app", Username: "reader", TLSMode: "strict",
		StmtTimeoutMS: 2000, ConnLimit: 2, RowLimit: 100,
	}
	_, err := fixture.runtime.Store.Datasources().Create(ctx, datasource, "test-password")
	require.NoError(t, err)
	_, err = fixture.runtime.Store.Policies().Create(ctx, model.Policy{
		ID: "mongo-table", AgentID: fixture.agent.ID, DatasourceID: datasource.ID,
		ObjectType: "table", ObjectName: "app.customers", Action: "allow",
	})
	require.NoError(t, err)
	redactor, err := mask.NewRedactor([]mask.Rule{{
		Schema: "app", Table: "customers", Column: "phone",
		SensitiveType: mask.TypePhone, Algorithm: mask.AlgoMask,
	}})
	require.NoError(t, err)
	fixture.handlers.mongoRedactor = func(context.Context, string) (mask.Redactor, error) { return redactor, nil }
	calls := 0
	fixture.handlers.mongoFind = func(_ context.Context, selected model.Datasource, request executor.MongoFindRequest) (model.QueryResult, error) {
		calls++
		require.Equal(t, datasource.ID, selected.ID)
		require.Equal(t, "customers", request.Collection)
		return model.QueryResult{
			Columns: []string{"name", "phone"}, Rows: [][]string{{"alice", "13812345678"}}, RowCount: 1,
		}, nil
	}
	input := mongoFindInput{DatasourceID: datasource.ID, Collection: "customers", Fields: []string{"name", "phone"}, Filter: map[string]string{"name": "alice"}, Limit: 2}
	response := fixture.handlers.findDocuments(ctx, input)
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
		require.Equal(t, "allow", event.Decision)
		require.Nil(t, event.SQLRaw)
		require.Equal(t, "app.customers", *event.Objects)
		require.NotContains(t, *event.DetailsJSON, "alice")
	}

	require.NoError(t, fixture.runtime.Store.Policies().Delete(ctx, "mongo-table"))
	revoked := fixture.handlers.findDocuments(ctx, input)
	require.Equal(t, "deny", revoked.Decision)
	require.Equal(t, 1, calls, "revoked collection must not reach MongoDB")
}

func TestMongoFindRejectsOperatorsFieldsAndWildcardBeforeDB(t *testing.T) {
	fixture := newMCPFixture(t, "readonly")
	ctx := context.Background()
	datasource := model.Datasource{ID: "mongo-closed", Name: "Mongo closed", DBType: "mongodb", Host: "mongo.test", Port: 27017,
		Database: "app", Username: "reader", TLSMode: "strict", StmtTimeoutMS: 2000, ConnLimit: 2, RowLimit: 100}
	_, err := fixture.runtime.Store.Datasources().Create(ctx, datasource, "test-password")
	require.NoError(t, err)
	_, err = fixture.runtime.Store.Policies().Create(ctx, model.Policy{ID: "mongo-wildcard", AgentID: fixture.agent.ID,
		DatasourceID: datasource.ID, ObjectType: "schema", ObjectName: "app.*", Action: "allow"})
	require.NoError(t, err)
	calls := 0
	fixture.handlers.mongoFind = func(context.Context, model.Datasource, executor.MongoFindRequest) (model.QueryResult, error) {
		calls++
		return model.QueryResult{}, nil
	}
	base := mongoFindInput{DatasourceID: datasource.ID, Collection: "customers", Fields: []string{"name"}, Filter: map[string]string{"name": "alice"}, Limit: 1}
	require.Equal(t, "deny", fixture.handlers.findDocuments(ctx, base).Decision)
	_, err = fixture.runtime.Store.Policies().Create(ctx, model.Policy{ID: "mongo-exact", AgentID: fixture.agent.ID,
		DatasourceID: datasource.ID, ObjectType: "table", ObjectName: "app.customers", Action: "allow"})
	require.NoError(t, err)
	fields := "name"
	_, err = fixture.runtime.Store.Policies().Create(ctx, model.Policy{ID: "mongo-fields", AgentID: fixture.agent.ID,
		DatasourceID: datasource.ID, ObjectType: "column", ObjectName: "app.customers", Columns: &fields, Action: "allow"})
	require.NoError(t, err)
	withPhone := base
	withPhone.Fields = []string{"phone"}
	require.Equal(t, "deny", fixture.handlers.findDocuments(ctx, withPhone).Decision)
	withOperator := base
	withOperator.Filter = map[string]string{"$where": "true"}
	require.Equal(t, "deny", fixture.handlers.findDocuments(ctx, withOperator).Decision)
	require.Zero(t, calls)
}

func TestMongoFindWithholdsResultWhenTerminalAuditFails(t *testing.T) {
	fixture := newMCPFixture(t, "readonly")
	ctx := context.Background()
	datasource := model.Datasource{ID: "mongo-audit-fault", Name: "Mongo audit fault", DBType: "mongodb", Host: "mongo.test", Port: 27017,
		Database: "app", Username: "reader", TLSMode: "strict", StmtTimeoutMS: 2000, ConnLimit: 2, RowLimit: 100}
	_, err := fixture.runtime.Store.Datasources().Create(ctx, datasource, "test-password")
	require.NoError(t, err)
	_, err = fixture.runtime.Store.Policies().Create(ctx, model.Policy{ID: "mongo-audit-table", AgentID: fixture.agent.ID,
		DatasourceID: datasource.ID, ObjectType: "table", ObjectName: "app.customers", Action: "allow"})
	require.NoError(t, err)
	redactor, err := mask.NewRedactor(nil)
	require.NoError(t, err)
	fixture.handlers.mongoRedactor = func(context.Context, string) (mask.Redactor, error) { return redactor, nil }
	fixture.handlers.mongoFind = func(context.Context, model.Datasource, executor.MongoFindRequest) (model.QueryResult, error) {
		require.NoError(t, fixture.runtime.Store.Close())
		return model.QueryResult{Columns: []string{"name"}, Rows: [][]string{{"private-value"}}, RowCount: 1}, nil
	}
	response := fixture.handlers.findDocuments(ctx, mongoFindInput{DatasourceID: datasource.ID, Collection: "customers",
		Fields: []string{"name"}, Filter: map[string]string{"name": "private-value"}, Limit: 1})
	require.Equal(t, "error", response.Decision)
	encoded, err := json.Marshal(response)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "private-value")
}

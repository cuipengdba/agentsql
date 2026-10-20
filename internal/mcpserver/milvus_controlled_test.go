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

func TestMilvusControlledAuthorizationMaskAuditAndRevocation(t *testing.T) {
	fixture := newMCPFixture(t, "readonly")
	ctx := context.Background()
	ds := model.Datasource{ID: "milvus-controlled", Name: "Milvus controlled", DBType: "milvus",
		Host: "milvus.test", Port: 19530, Database: "default", Username: "agent_reader", TLSMode: "strict",
		TLSCAFile: "test-ca.pem", StmtTimeoutMS: 5000, ConnLimit: 1, RowLimit: 20}
	_, err := fixture.runtime.Store.Datasources().Create(ctx, ds, "test-password")
	require.NoError(t, err)
	grant, err := fixture.runtime.Store.Policies().Create(ctx, model.Policy{ID: "milvus-table", AgentID: fixture.agent.ID,
		DatasourceID: ds.ID, ObjectType: "table", ObjectName: "default.events", Action: "allow"})
	require.NoError(t, err)
	columns := "embedding,name,phone"
	_, err = fixture.runtime.Store.Policies().Create(ctx, model.Policy{ID: "milvus-columns", AgentID: fixture.agent.ID,
		DatasourceID: ds.ID, ObjectType: "column", ObjectName: "default.events", Columns: &columns, Action: "allow"})
	require.NoError(t, err)
	redactor, err := mask.NewRedactor([]mask.Rule{{Schema: "default", Table: "events", Column: "phone",
		SensitiveType: mask.TypePhone, Algorithm: mask.AlgoMask}})
	require.NoError(t, err)
	fixture.handlers.vectorRedactor = func(context.Context, string) (mask.Redactor, error) { return redactor, nil }
	calls := 0
	fixture.handlers.vectorSearch = func(_ context.Context, selected model.Datasource, request executor.MilvusSearchRequest) (model.QueryResult, error) {
		calls++
		require.Equal(t, ds.ID, selected.ID)
		require.Equal(t, "events", request.Collection)
		return model.QueryResult{Columns: []string{"name", "phone"}, Rows: [][]string{{"alice", "13812345678"}}, RowCount: 1}, nil
	}
	input := vectorSearchInput{DatasourceID: ds.ID, Collection: "events", VectorField: "embedding",
		Vector: []float32{0.1234567, 0.5, 0, 0}, Fields: []string{"name", "phone"}, TopK: 2}
	response := fixture.handlers.searchVectors(ctx, input)
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
		require.Equal(t, "search_vectors", *event.MCPTool)
		require.Equal(t, "default.events", *event.Objects)
		require.Nil(t, event.SQLRaw)
		require.NotContains(t, *event.DetailsJSON, "0.1234567")
		require.NotContains(t, *event.DetailsJSON, "13812345678")
	}
	input.Fields = []string{"region"}
	require.Equal(t, "deny", fixture.handlers.searchVectors(ctx, input).Decision)
	require.Equal(t, 1, calls)
	input.Fields = []string{"name", "phone"}
	require.NoError(t, fixture.runtime.Store.Policies().Delete(ctx, grant.ID))
	require.Equal(t, "deny", fixture.handlers.searchVectors(ctx, input).Decision)
	require.Equal(t, 1, calls)
}

func TestMilvusControlledInvalidVectorRejectedBeforeDB(t *testing.T) {
	for _, request := range []executor.MilvusSearchRequest{
		{Collection: "events", VectorField: "embedding", Vector: []float32{}, Fields: []string{"name"}, TopK: 2},
		{Collection: "events", VectorField: "embedding", Vector: []float32{1}, Fields: []string{"embedding"}, TopK: 2},
		{Collection: "events;drop", VectorField: "embedding", Vector: []float32{1}, Fields: []string{"name"}, TopK: 2},
		{Collection: "events", VectorField: "embedding", Vector: []float32{1}, Fields: []string{"name"}, TopK: 21},
	} {
		require.Error(t, executor.ValidateMilvusSearch(request))
	}
}

func TestMilvusControlledTerminalAuditFailureWithholdsResult(t *testing.T) {
	fixture := newMCPFixture(t, "readonly")
	ctx := context.Background()
	ds := model.Datasource{ID: "milvus-audit-outage", Name: "Milvus audit outage", DBType: "milvus",
		Host: "milvus.test", Port: 19530, Database: "default", Username: "agent_reader", TLSMode: "strict",
		TLSCAFile: "test-ca.pem", StmtTimeoutMS: 5000, ConnLimit: 1, RowLimit: 20}
	_, err := fixture.runtime.Store.Datasources().Create(ctx, ds, "test-password")
	require.NoError(t, err)
	_, err = fixture.runtime.Store.Policies().Create(ctx, model.Policy{ID: "milvus-outage-table", AgentID: fixture.agent.ID,
		DatasourceID: ds.ID, ObjectType: "table", ObjectName: "default.events", Action: "allow"})
	require.NoError(t, err)
	columns := "embedding,phone"
	_, err = fixture.runtime.Store.Policies().Create(ctx, model.Policy{ID: "milvus-outage-columns", AgentID: fixture.agent.ID,
		DatasourceID: ds.ID, ObjectType: "column", ObjectName: "default.events", Columns: &columns, Action: "allow"})
	require.NoError(t, err)
	redactor, err := mask.NewRedactor([]mask.Rule{{Schema: "default", Table: "events", Column: "phone",
		SensitiveType: mask.TypePhone, Algorithm: mask.AlgoMask}})
	require.NoError(t, err)
	fixture.handlers.vectorRedactor = func(context.Context, string) (mask.Redactor, error) { return redactor, nil }
	fixture.handlers.vectorSearch = func(context.Context, model.Datasource, executor.MilvusSearchRequest) (model.QueryResult, error) {
		require.NoError(t, fixture.runtime.Store.Close())
		return model.QueryResult{Columns: []string{"phone"}, Rows: [][]string{{"13812345678"}}, RowCount: 1}, nil
	}
	response := fixture.handlers.searchVectors(ctx, vectorSearchInput{DatasourceID: ds.ID, Collection: "events",
		VectorField: "embedding", Vector: []float32{1, 0, 0, 0}, Fields: []string{"phone"}, TopK: 1})
	require.Equal(t, "error", response.Decision)
	encoded, err := json.Marshal(response)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "13812345678")
}

func TestMilvusControlledCancellationWritesErrorTerminal(t *testing.T) {
	fixture := newMCPFixture(t, "readonly")
	ctx := context.Background()
	ds := model.Datasource{ID: "milvus-cancel", Name: "Milvus cancel", DBType: "milvus",
		Host: "milvus.test", Port: 19530, Database: "default", Username: "agent_reader", TLSMode: "strict",
		TLSCAFile: "test-ca.pem", StmtTimeoutMS: 10, ConnLimit: 1, RowLimit: 20}
	_, err := fixture.runtime.Store.Datasources().Create(ctx, ds, "test-password")
	require.NoError(t, err)
	_, err = fixture.runtime.Store.Policies().Create(ctx, model.Policy{ID: "milvus-cancel-table", AgentID: fixture.agent.ID,
		DatasourceID: ds.ID, ObjectType: "table", ObjectName: "default.events", Action: "allow"})
	require.NoError(t, err)
	columns := "embedding,name"
	_, err = fixture.runtime.Store.Policies().Create(ctx, model.Policy{ID: "milvus-cancel-columns", AgentID: fixture.agent.ID,
		DatasourceID: ds.ID, ObjectType: "column", ObjectName: "default.events", Columns: &columns, Action: "allow"})
	require.NoError(t, err)
	fixture.handlers.vectorSearch = func(timed context.Context, _ model.Datasource, _ executor.MilvusSearchRequest) (model.QueryResult, error) {
		<-timed.Done()
		return model.QueryResult{}, timed.Err()
	}
	response := fixture.handlers.searchVectors(ctx, vectorSearchInput{DatasourceID: ds.ID, Collection: "events",
		VectorField: "embedding", Vector: []float32{1, 0, 0, 0}, Fields: []string{"name"}, TopK: 1})
	require.Equal(t, "error", response.Decision)
	page, err := fixture.runtime.Store.AuditLogs().Page(ctx, 1, 10)
	require.NoError(t, err)
	require.EqualValues(t, 2, page.Total)
	terminalFound := false
	for _, event := range page.List {
		terminalFound = terminalFound || event.Action != nil && *event.Action == "vector_search_error"
	}
	require.True(t, terminalFound)
}

package store

import (
	"context"
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestT14ListMethodsReturnNonNilEmptySlices(t *testing.T) {
	opened := openTestStore(t)
	datasources, err := opened.Datasources().List(context.Background())
	require.NoError(t, err)
	require.NotNil(t, datasources)
	require.Empty(t, datasources)
	policies, err := opened.Policies().ListByAgent(context.Background(), "missing")
	require.NoError(t, err)
	require.NotNil(t, policies)
	require.Empty(t, policies)
	maskRules, err := opened.MaskRules().ListByDatasource(context.Background(), "missing")
	require.NoError(t, err)
	require.NotNil(t, maskRules)
	require.Empty(t, maskRules)
}

func TestDatasourceRepositoryListIsSorted(t *testing.T) {
	opened := openTestStore(t)
	for _, id := range []string{"ds-z", "ds-a"} {
		_, err := opened.Datasources().Create(context.Background(), model.Datasource{
			ID: id, Name: id, DBType: "postgres", Host: "127.0.0.1", Port: 5432,
			Database: "app", Username: "agentsql", ConnLimit: 5,
			StmtTimeoutMS: 5_000, RowLimit: 1_000,
		}, "password")
		require.NoError(t, err)
	}
	listed, err := opened.Datasources().List(context.Background())
	require.NoError(t, err)
	require.Equal(t, []string{"ds-a", "ds-z"}, []string{listed[0].ID, listed[1].ID})
	require.NotEmpty(t, listed[0].PasswordEnc)
	_, err = opened.Datasources().List(nil)
	require.ErrorIs(t, err, ErrNilContext)
}

func TestPolicyRepositoryListByAgentIsParameterizedAndSorted(t *testing.T) {
	opened := openTestStore(t)
	for _, agentID := range []string{"agent-target", "' OR 1=1--"} {
		_, hash, generateError := GenerateAPIKey()
		require.NoError(t, generateError)
		_, err := opened.Agents().Create(context.Background(), model.Agent{
			ID: agentID, Name: agentID, Status: "active", APIKeyHash: hash, Level: "dml",
		})
		require.NoError(t, err)
	}
	for _, id := range []string{"ds-b", "ds-a"} {
		_, err := opened.Datasources().Create(context.Background(), model.Datasource{
			ID: id, Name: id, DBType: "postgres", Host: "127.0.0.1", Port: 5432,
			Database: "app", Username: "agentsql", ConnLimit: 5,
			StmtTimeoutMS: 5_000, RowLimit: 1_000,
		}, "password")
		require.NoError(t, err)
	}
	for _, stored := range []model.Policy{
		{ID: "p3", AgentID: "agent-target", DatasourceID: "ds-b", ObjectType: "table", ObjectName: "z.table", Action: "allow"},
		{ID: "p2", AgentID: "agent-target", DatasourceID: "ds-a", ObjectType: "table", ObjectName: "z.table", Action: "allow"},
		{ID: "p1", AgentID: "agent-target", DatasourceID: "ds-a", ObjectType: "table", ObjectName: "a.table", Action: "allow"},
		{ID: "other", AgentID: "' OR 1=1--", DatasourceID: "ds-a", ObjectType: "table", ObjectName: "other.table", Action: "allow"},
	} {
		_, err := opened.Policies().Create(context.Background(), stored)
		require.NoError(t, err)
	}
	listed, err := opened.Policies().ListByAgent(context.Background(), "agent-target")
	require.NoError(t, err)
	require.Equal(t, []string{"p1", "p2", "p3"}, []string{listed[0].ID, listed[1].ID, listed[2].ID})
	injection, err := opened.Policies().ListByAgent(context.Background(), "' OR 1=1--")
	require.NoError(t, err)
	require.Len(t, injection, 1)
	require.Equal(t, "other", injection[0].ID)
	_, err = opened.Policies().ListByAgent(nil, "agent-target")
	require.ErrorIs(t, err, ErrNilContext)
}

func TestMaskRuleRepositoryListByDatasourceIncludesGlobalAndSorts(t *testing.T) {
	opened := openTestStore(t)
	for _, stored := range []model.MaskRule{
		{ID: "specific-z", DatasourceID: pointer("ds-1"), TableName: "z", ColumnName: "phone", SensitiveType: "phone", Algo: "mask"},
		{ID: "global-a", DatasourceID: nil, TableName: "a", ColumnName: "email", SensitiveType: "email", Algo: "mask"},
		{ID: "other", DatasourceID: pointer("ds-2"), TableName: "b", ColumnName: "phone", SensitiveType: "phone", Algo: "mask"},
	} {
		_, err := opened.MaskRules().Create(context.Background(), stored)
		require.NoError(t, err)
	}
	listed, err := opened.MaskRules().ListByDatasource(context.Background(), "ds-1")
	require.NoError(t, err)
	require.Equal(t, []string{"global-a", "specific-z"}, []string{listed[0].ID, listed[1].ID})
	_, err = opened.MaskRules().ListByDatasource(nil, "ds-1")
	require.ErrorIs(t, err, ErrNilContext)
}

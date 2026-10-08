package pipeline

import (
	"context"
	"testing"

	executor "github.com/cuipengdba/agentsql/internal/authorizedexecute"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestYashanPipelineNarrowSelectAndStaticDeny(t *testing.T) {
	fixture := newPipelineFixture(t)
	fixture.datasources.datasource.DBType = "yashan"
	fixture.executor.dialect = "yashan"
	fixture.policies.policies = []model.Policy{{
		ID: "yashan-select", AgentID: "agent-1", DatasourceID: "datasource-1",
		ObjectType: "table", ObjectName: "app.customers", Action: "allow",
	}}

	response, err := fixture.pipeline.Process(context.Background(), requestWithSQL("SELECT phone FROM app.customers WHERE id = 1"))
	require.NoError(t, err)
	require.Equal(t, model.DecisionAllow, response.Decision)
	require.NotNil(t, response.Result)
	require.Equal(t, "138****5678", response.Result.Rows[0][0])
	calls := fixture.executor.callsSnapshot()
	require.Zero(t, calls.explain)
	require.Equal(t, 1, calls.query)
	require.Equal(t, 1, fixture.audit.calls())
	require.Equal(t, "allow", fixture.audit.last().Decision)

	denied, err := fixture.pipeline.Process(context.Background(), requestWithSQL("SELECT phone FROM app.secrets WHERE id = 1"))
	require.NoError(t, err)
	require.Equal(t, model.DecisionDeny, denied.Decision)
	require.Nil(t, denied.Result)
	require.Equal(t, 1, fixture.executor.callsSnapshot().query)
	require.Equal(t, 2, fixture.audit.calls())
}

func TestYashanPipelineRejectsUnsupportedSQLBeforeExecutor(t *testing.T) {
	fixture := newPipelineFixture(t)
	fixture.datasources.datasource.DBType = "yashan"
	fixture.executor.dialect = "yashan"

	response, err := fixture.pipeline.Process(context.Background(), requestWithSQL("SELECT NVL(phone, 'x') FROM app.customers"))
	require.NoError(t, err)
	require.Equal(t, model.DecisionError, response.Decision)
	require.Equal(t, string(executor.DBErrorCodeSyntax), response.ErrorCode)
	require.Zero(t, fixture.executors.calls())
	require.Equal(t, 1, fixture.audit.calls())
}

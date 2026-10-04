package pipeline

import (
	"context"
	"testing"

	executor "github.com/cuipengdba/agentsql/internal/authorizedexecute"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestSQLServerPipelineAllowsAuditsAndRedactsNarrowSelect(t *testing.T) {
	fixture := newPipelineFixture(t)
	fixture.datasources.datasource.DBType = "sqlserver"
	fixture.executor.dialect = "sqlserver"
	fixture.policies.policies = []model.Policy{{
		ID: "sqlserver-select", AgentID: "agent-1", DatasourceID: "datasource-1",
		ObjectType: "table", ObjectName: "dbo.customers", Action: "allow",
	}}

	response, err := fixture.pipeline.Process(
		context.Background(),
		requestWithSQL("SELECT TOP 1 [phone] FROM [dbo].[customers] WHERE [id] = 1"),
	)

	require.NoError(t, err)
	require.Equal(t, model.DecisionAllow, response.Decision)
	require.NotNil(t, response.Result)
	require.Equal(t, "138****5678", response.Result.Rows[0][0])
	calls := fixture.executor.callsSnapshot()
	require.Equal(t, 1, calls.explain)
	require.Equal(t, 1, calls.query)
	require.Zero(t, calls.execute+calls.openSession+calls.sessionExecute+calls.sessionQuery)
	require.Equal(t, 1, fixture.audit.calls())
	log := fixture.audit.last()
	require.NotNil(t, log.DBType)
	require.Equal(t, "sqlserver", *log.DBType)
	require.NotNil(t, log.Objects)
	require.Equal(t, "dbo.customers", *log.Objects)
}

func TestSQLServerPipelineFailsClosedBeforeExecutor(t *testing.T) {
	for _, sqlText := range []string{
		"UPDATE [dbo].[customers] SET [phone] = 'x'",
		"UPDATE [dbo].[customers] SET [phone] = 'x' WHERE [id] = 1",
		"DELETE FROM [dbo].[customers]",
		"EXEC xp_cmdshell 'whoami'",
		"SELECT * FROM OPENROWSET(BULK 'x', SINGLE_BLOB) AS x",
	} {
		t.Run(sqlText, func(t *testing.T) {
			fixture := newPipelineFixture(t)
			fixture.datasources.datasource.DBType = "sqlserver"
			fixture.executor.dialect = "sqlserver"

			response, err := fixture.pipeline.Process(context.Background(), requestWithSQL(sqlText))

			require.NoError(t, err)
			require.Equal(t, model.DecisionError, response.Decision)
			require.Equal(t, string(executor.DBErrorCodeSyntax), response.ErrorCode)
			require.Equal(t, string(executor.DBStageParse), response.ErrorStage)
			require.Zero(t, fixture.executors.calls())
			calls := fixture.executor.callsSnapshot()
			require.Zero(t, calls.explain+calls.query+calls.execute+calls.openSession)
			require.Equal(t, 1, fixture.audit.calls())
			log := fixture.audit.last()
			require.NotNil(t, log.DBType)
			require.Equal(t, "sqlserver", *log.DBType)
		})
	}
}

package pipeline

import (
	"context"
	"testing"

	executor "github.com/cuipengdba/agentsql/internal/authorizedexecute"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestPipelineEmptyAndIncompleteSQLStopsBeforeExecutor(t *testing.T) {
	for _, test := range []struct {
		name string
		sql  string
	}{
		{name: "empty", sql: ""},
		{name: "whitespace", sql: " \t\r\n "},
		{name: "empty order by", sql: "SELECT id FROM public.customers ORDER BY"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPipelineFixture(t)
			response, err := fixture.pipeline.Process(context.Background(), requestWithSQL(test.sql))
			require.NoError(t, err)
			require.Equal(t, model.DecisionError, response.Decision)
			require.Equal(t, string(executor.DBErrorCodeSyntax), response.ErrorCode)
			require.Equal(t, string(executor.DBStageParse), response.ErrorStage)
			require.Nil(t, response.Result)
			require.Zero(t, fixture.executors.calls())
			calls := fixture.executor.callsSnapshot()
			require.Zero(t, calls.explain+calls.query+calls.execute+calls.openSession)
			require.Zero(t, calls.sessionExplain+calls.sessionQuery+calls.sessionExecute)
			require.Equal(t, 1, fixture.audit.calls())
			require.Equal(t, "error", fixture.audit.last().Decision)
		})
	}
}

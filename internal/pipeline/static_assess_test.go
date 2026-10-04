package pipeline

import (
	"sort"
	"sync"
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestStaticAssessScenarios(t *testing.T) {
	tests := []struct {
		name         string
		input        StaticAssessInput
		decision     model.Decision
		expectedHits []string
		parseError   bool
	}{
		{name: "normal point query", input: StaticAssessInput{SQL: "SELECT id, name FROM public.customers WHERE id = 42 LIMIT 10", Dialect: "postgres", AgentLevel: "readonly"}, decision: model.DecisionAllow, expectedHits: []string{}},
		{name: "update without where", input: StaticAssessInput{SQL: "UPDATE orders SET status = 'closed'", Dialect: "mysql", AgentLevel: "dml"}, decision: model.DecisionDeny, expectedHits: []string{"R002", "R202"}},
		{name: "stacked statements", input: StaticAssessInput{SQL: "SELECT * FROM users WHERE id = 1; DELETE FROM users", Dialect: "mysql", AgentLevel: "dml"}, decision: model.DecisionDeny, expectedHits: []string{"R001", "R006"}},
		{name: "postgres sleep", input: StaticAssessInput{SQL: "SELECT * FROM public.orders WHERE id = 1 OR pg_sleep(10) IS NULL", Dialect: "postgres", AgentLevel: "readonly"}, decision: model.DecisionDeny, expectedHits: []string{"R007"}},
		{name: "postgres drop table", input: StaticAssessInput{SQL: "DROP TABLE public.orders", Dialect: "postgres", AgentLevel: "ddl"}, decision: model.DecisionDeny, expectedHits: []string{"R101"}},
		{name: "mysql kill", input: StaticAssessInput{SQL: "KILL 12", Dialect: "mysql", AgentLevel: "ddl"}, decision: model.DecisionDeny, expectedHits: []string{"R203"}},
		{name: "mysql bounded predicate without limit", input: StaticAssessInput{SQL: "UPDATE accounts SET balance = balance + 1 WHERE level = 'vip'", Dialect: "mysql", AgentLevel: "dml"}, decision: model.DecisionApprove, expectedHits: []string{"R202"}},
		{name: "readonly update", input: StaticAssessInput{SQL: "UPDATE orders SET status = 'closed' WHERE id = 1 LIMIT 1", Dialect: "mysql", AgentLevel: "readonly"}, decision: model.DecisionDeny, expectedHits: []string{"R003"}},
		{name: "SQL comment", input: StaticAssessInput{SQL: "SELECT /* guarded */ id FROM public.orders WHERE id = 1 LIMIT 1", Dialect: "postgres", AgentLevel: "readonly"}, decision: model.DecisionDeny, expectedHits: []string{"R006"}},
		{name: "malformed SQL", input: StaticAssessInput{SQL: "SELECT FROM WHERE (((", Dialect: "postgres", AgentLevel: "readonly"}, decision: model.DecisionDeny, expectedHits: []string{"PARSE"}, parseError: true},
		{name: "mysql union values", input: StaticAssessInput{SQL: "SELECT 1 UNION VALUES ROW(2)", Dialect: "mysql", AgentLevel: "readonly"}, decision: model.DecisionDeny, expectedHits: []string{"PARSE"}, parseError: true},
		{name: "mysql root values", input: StaticAssessInput{SQL: "VALUES ROW(1)", Dialect: "mysql", AgentLevel: "readonly"}, decision: model.DecisionDeny, expectedHits: []string{"PARSE"}, parseError: true},
		{name: "sqlserver bounded select", input: StaticAssessInput{SQL: "SELECT TOP 10 [id] FROM [dbo].[customers] WHERE [id] = 42", Dialect: "sqlserver", AgentLevel: "readonly"}, decision: model.DecisionAllow, expectedHits: []string{}},
		{name: "sqlserver update fails closed", input: StaticAssessInput{SQL: "UPDATE dbo.customers SET enabled = 0", Dialect: "sqlserver", AgentLevel: "dml"}, decision: model.DecisionDeny, expectedHits: []string{"PARSE"}, parseError: true},
		{name: "sqlserver xp_cmdshell fails closed", input: StaticAssessInput{SQL: "EXEC xp_cmdshell 'whoami'", Dialect: "sqlserver", AgentLevel: "ddl"}, decision: model.DecisionDeny, expectedHits: []string{"PARSE"}, parseError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assessment, parseError, err := StaticAssess(test.input)
			require.NoError(t, err)
			require.Equal(t, test.decision, assessment.Decision)
			if test.parseError {
				require.NotEmpty(t, parseError)
			} else {
				require.Empty(t, parseError)
			}
			ids := staticHitIDs(assessment.Hits)
			require.ElementsMatch(t, test.expectedHits, ids)
			require.Zero(t, assessment.EstScanRows)
			require.Len(t, assessment.StageLatency, len(pipelineStageNames()))
			for _, stage := range pipelineStageNames() {
				require.GreaterOrEqual(t, assessment.StageLatency[stage], int64(0))
				if stage != StageParse && stage != StageGuardStatic {
					require.Zero(t, assessment.StageLatency[stage])
				}
			}
		})
	}
}

func TestStaticAssessDefaultsEmptyAgentLevelToReadonly(t *testing.T) {
	assessment, parseError, err := StaticAssess(StaticAssessInput{
		SQL: "UPDATE orders SET status = 'closed' WHERE id = 1 LIMIT 1", Dialect: "mysql",
	})
	require.NoError(t, err)
	require.Empty(t, parseError)
	require.Equal(t, model.DecisionDeny, assessment.Decision)
	require.ElementsMatch(t, []string{"R003"}, staticHitIDs(assessment.Hits))
}

func TestStaticAssessRejectsInvalidInput(t *testing.T) {
	for _, input := range []StaticAssessInput{
		{Dialect: "postgres", AgentLevel: "readonly"},
		{SQL: "SELECT 1", Dialect: "oracle", AgentLevel: "readonly"},
		{SQL: "SELECT 1", Dialect: "postgres", AgentLevel: "root"},
	} {
		_, _, err := StaticAssess(input)
		require.Error(t, err)
	}
}

func TestStaticAssessIsConcurrentSafe(t *testing.T) {
	const workers = 50
	var wait sync.WaitGroup
	errors := make(chan error, workers)
	for index := 0; index < workers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			assessment, parseError, err := StaticAssess(StaticAssessInput{
				SQL: "SELECT id FROM public.customers WHERE id = 42 LIMIT 1", Dialect: "postgres", AgentLevel: "readonly",
			})
			if err != nil {
				errors <- err
				return
			}
			if parseError != "" || assessment.Decision != model.DecisionAllow {
				errors <- &staticAssessmentMismatch{decision: assessment.Decision, parseError: parseError}
			}
		}()
	}
	wait.Wait()
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
}

type staticAssessmentMismatch struct {
	decision   model.Decision
	parseError string
}

func (err *staticAssessmentMismatch) Error() string {
	return "unexpected concurrent static assessment: decision=" + string(err.decision) + " parse_error=" + err.parseError
}

func staticHitIDs(hits []model.RuleHit) []string {
	ids := make([]string, 0, len(hits))
	for _, hit := range hits {
		ids = append(ids, hit.RuleID)
	}
	sort.Strings(ids)
	return ids
}

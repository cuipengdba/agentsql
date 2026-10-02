package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	executor "github.com/cuipengdba/agentsql/internal/authorizedexecute"
	"github.com/cuipengdba/agentsql/internal/engine"
	"github.com/cuipengdba/agentsql/internal/mask"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/rules"
	"github.com/stretchr/testify/require"
)

var testPipelineSecret = []byte("0123456789abcdef0123456789abcdef")

func TestPipelineFailurePathsDoNotExecuteBusinessSQL(t *testing.T) {
	tests := []struct {
		name          string
		configure     func(*pipelineFixture)
		request       Request
		decision      model.Decision
		metadataCheck bool
	}{
		{
			name: "authentication failure",
			configure: func(fixture *pipelineFixture) {
				fixture.authenticator.err = errors.New("bad API key")
			},
			request:  defaultRequest(),
			decision: model.DecisionDeny,
		},
		{
			name:      "malformed SQL",
			configure: func(*pipelineFixture) {},
			request:   requestWithSQL("SELECT ("),
			decision:  model.DecisionError,
		},
		{
			name:      "update without where",
			configure: func(*pipelineFixture) {},
			request:   requestWithSQLAndSession("UPDATE public.orders SET total = 1"),
			decision:  model.DecisionDeny,
		},
		{
			name:          "unauthorized table",
			configure:     func(*pipelineFixture) {},
			request:       requestWithSQL("SELECT id FROM public.secrets LIMIT 1"),
			decision:      model.DecisionDeny,
			metadataCheck: true,
		},
		{
			name: "readonly agent write",
			configure: func(fixture *pipelineFixture) {
				fixture.authenticator.agent.Level = "readonly"
			},
			request:  requestWithSQL("UPDATE public.orders SET total = 1 WHERE id = 1"),
			decision: model.DecisionDeny,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPipelineFixture(t)
			test.configure(fixture)
			response, err := fixture.pipeline.Process(context.Background(), test.request)
			if test.name == "authentication failure" {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, test.decision, response.Decision)
			if test.name == "malformed SQL" {
				require.Equal(t, string(executor.DBErrorCodeSyntax), response.ErrorCode)
				require.Equal(t, string(executor.DBStageParse), response.ErrorStage)
				require.Nil(t, response.Result)
			}
			calls := fixture.executor.callsSnapshot()
			if test.metadataCheck {
				require.Equal(t, 1, fixture.executors.calls())
				require.Equal(t, 1, calls.explain)
			} else {
				require.Zero(t, fixture.executors.calls())
				require.Zero(t, calls.explain)
			}
			require.Zero(t, calls.query+calls.execute+calls.openSession)
			require.Zero(t, calls.sessionExplain+calls.sessionQuery+calls.sessionExecute)
			require.Equal(t, 1, fixture.audit.calls())
			if err != nil || test.name == "malformed SQL" {
				require.Equal(t, "error", fixture.audit.last().Decision)
			} else {
				require.Equal(t, "deny", fixture.audit.last().Decision)
			}
		})
	}
}

func TestPipelineRejectsMySQLValuesBeforeExecutor(t *testing.T) {
	for _, test := range []struct {
		name string
		sql  string
	}{
		{name: "union values right", sql: "SELECT 1 UNION VALUES ROW(2)"},
		{name: "union values left", sql: "VALUES ROW(1) UNION SELECT 2"},
		{name: "nested union values", sql: "SELECT 1 UNION (SELECT 2 UNION VALUES ROW(3))"},
		{name: "root values", sql: "VALUES ROW(1)"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPipelineFixture(t)
			fixture.datasources.datasource.DBType = "mysql"
			fixture.executor.dialect = "mysql"

			response, err := fixture.pipeline.Process(context.Background(), requestWithSQL(test.sql))

			require.NoError(t, err)
			require.Equal(t, model.DecisionError, response.Decision)
			require.Equal(t, string(executor.DBErrorCodeSyntax), response.ErrorCode)
			require.Equal(t, string(executor.DBStageParse), response.ErrorStage)
			require.Nil(t, response.Result)
			require.Zero(t, fixture.executors.calls(), "executor provider must not be reached")
			calls := fixture.executor.callsSnapshot()
			require.Zero(t, calls.explain+calls.query+calls.execute+calls.openSession)
			require.Zero(t, calls.sessionExplain+calls.sessionQuery+calls.sessionExecute)
			require.Equal(t, 1, fixture.audit.calls())
			require.Equal(t, "error", fixture.audit.last().Decision)
			require.NotNil(t, fixture.audit.last().ErrorCode)
			require.Equal(t, string(executor.DBErrorCodeSyntax), *fixture.audit.last().ErrorCode)
		})
	}
}

func TestPipelineDynamicApproveCreatesApprovalAndAuditAtomically(t *testing.T) {
	fixture := newPipelineFixture(t, WithRuleLayers(engine.RuleLayers{
		Agent: engine.RuleLayer{
			"R004": {Thresholds: map[string]float64{rules.ThresholdMaxScanRows: 10}},
		},
	}))
	fixture.executor.explain = model.ExplainInfo{EstScanRows: 11, SeqScan: true}
	request := defaultRequest()
	request.SessionID = "approve-session"
	response, err := fixture.pipeline.Process(context.Background(), request)
	require.NoError(t, err)
	require.Equal(t, model.DecisionApprove, response.Decision)
	require.NotEmpty(t, response.ApprovalID)
	require.Positive(t, response.AuditID)
	require.Equal(t, 1, fixture.approvals.calls())
	require.Equal(t, response.AuditID, *fixture.approvals.last().AuditID)
	calls := fixture.executor.callsSnapshot()
	require.Equal(t, 1, calls.openSession)
	require.Equal(t, 1, calls.sessionExplain)
	require.Zero(t, calls.explain)
	require.Zero(t, calls.query+calls.execute+calls.sessionQuery+calls.sessionExecute)
	require.Equal(t, 1, calls.sessionClose)
	require.Equal(t, "approve", fixture.audit.last().Decision)
}

func TestPipelineApprovalWorkflowFailuresCloseAsError(t *testing.T) {
	tests := []struct {
		name          string
		workflowError error
		emptyApproval bool
		emptyAudit    bool
	}{
		{name: "create failure", workflowError: errors.New("create pending failed")},
		{name: "empty approval ID", emptyApproval: true},
		{name: "audit insert failure", workflowError: errors.New("insert audit failed")},
		{name: "audit backfill failure", workflowError: errors.New("backfill audit failed")},
		{name: "commit failure", workflowError: errors.New("commit failed")},
		{name: "empty audit ID", emptyAudit: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			observer := &recordingDecisionObserver{}
			fixture := newPipelineFixture(t, WithObserver(observer), WithRuleLayers(engine.RuleLayers{
				Agent: engine.RuleLayer{
					"R004": {Thresholds: map[string]float64{rules.ThresholdMaxScanRows: 10}},
				},
			}))
			fixture.executor.explain = model.ExplainInfo{EstScanRows: 11, SeqScan: true}
			fixture.approvals.workflowErr = test.workflowError
			fixture.approvals.emptyApprovalID = test.emptyApproval
			fixture.approvals.emptyAuditID = test.emptyAudit
			limiter := &spyRateLimiter{}
			fixture.pipeline.limiter = limiter

			response, err := fixture.pipeline.Process(context.Background(), defaultRequest())

			require.Error(t, err)
			require.Equal(t, model.DecisionDeny, response.Decision)
			require.Empty(t, response.ApprovalID)
			require.Positive(t, response.AuditID)
			require.Zero(t, fixture.approvals.calls())
			require.Equal(t, 1, fixture.audit.calls())
			require.Equal(t, "error", fixture.audit.last().Decision)
			allowed, released, inFlight := limiter.snapshot()
			require.Equal(t, 1, allowed)
			require.Equal(t, 1, released)
			require.Zero(t, inFlight)
			decisions, _, stages := observer.snapshot()
			require.Len(t, decisions, 1)
			require.Equal(t, string(model.DecisionDeny), decisions[0].decision)
			require.GreaterOrEqual(t, stages[StageAudit], int64(0))
		})
	}
}

func TestPipelineAllowSelectQueriesRedactsAndAudits(t *testing.T) {
	fixture := newPipelineFixture(t)
	response, err := fixture.pipeline.Process(context.Background(), defaultRequest())
	require.NoError(t, err)
	require.Equal(t, model.DecisionAllow, response.Decision)
	require.NotNil(t, response.Result)
	require.Equal(t, "138****5678", response.Result.Rows[0][0])
	require.Equal(t, 1, response.Redact.MaskedCells)
	calls := fixture.executor.callsSnapshot()
	require.Equal(t, 1, calls.explain)
	require.Equal(t, 1, calls.query)
	require.Zero(t, calls.execute+calls.openSession+calls.sessionQuery+calls.sessionExecute)
	limit, explainDeadline, queryDeadline := fixture.executor.executionSettings()
	require.Equal(t, 2, limit)
	require.True(t, explainDeadline)
	require.True(t, queryDeadline)
	log := fixture.audit.last()
	require.Equal(t, "allow", log.Decision)
	require.NotNil(t, log.RowsReturned)
	require.Equal(t, 1, *log.RowsReturned)
	require.Equal(t, "agent-1", *log.AgentID)
	require.Equal(t, "public.customers", *log.Objects)
	require.Equal(t, "SELECT", *log.StmtType)
}

func TestPipelineR005ProductionDynamicAndRuntimeSignals(t *testing.T) {
	t.Run("dynamic plan exceeds datasource row limit", func(t *testing.T) {
		fixture := newPipelineFixture(t)
		fixture.executor.explain = model.ExplainInfo{EstScanRows: 3, SeqScan: true}

		response, err := fixture.pipeline.Process(
			context.Background(),
			requestWithSQL("SELECT phone FROM public.customers WHERE id > 0"),
		)

		require.NoError(t, err)
		require.Equal(t, model.DecisionWarn, response.Decision)
		require.Contains(t, ruleHitIDs(response.Assessment.Hits), "R005")
		require.Equal(t, int64(3), response.Assessment.EstScanRows)
		require.Equal(t, "warn", fixture.audit.last().Decision)
	})

	t.Run("actual row limit truncation overrides explicit SQL limit", func(t *testing.T) {
		fixture := newPipelineFixture(t)
		fixture.executor.explain = model.ExplainInfo{EstScanRows: 1, UsesIndex: true}
		fixture.executor.queryResult = model.QueryResult{
			Columns:   []string{"phone"},
			Rows:      [][]string{{"13812345678"}, {"13912345678"}},
			RowCount:  2,
			Truncated: true,
		}

		response, err := fixture.pipeline.Process(
			context.Background(),
			requestWithSQL("SELECT phone FROM public.customers WHERE id > 0 LIMIT 100"),
		)

		require.NoError(t, err)
		require.Equal(t, model.DecisionWarn, response.Decision)
		require.Contains(t, ruleHitIDs(response.Assessment.Hits), "R005")
		require.NotNil(t, response.Result)
		require.True(t, response.Result.Truncated)
		require.Equal(t, "warn", fixture.audit.last().Decision)
	})

	t.Run("small bounded result does not warn", func(t *testing.T) {
		fixture := newPipelineFixture(t)
		fixture.executor.explain = model.ExplainInfo{EstScanRows: 2, UsesIndex: true}
		fixture.executor.queryResult = model.QueryResult{
			Columns:  []string{"phone"},
			Rows:     [][]string{{"13812345678"}, {"13912345678"}},
			RowCount: 2,
		}

		response, err := fixture.pipeline.Process(
			context.Background(),
			requestWithSQL("SELECT phone FROM public.customers WHERE id > 0"),
		)

		require.NoError(t, err)
		require.Equal(t, model.DecisionAllow, response.Decision)
		require.NotContains(t, ruleHitIDs(response.Assessment.Hits), "R005")
		require.False(t, response.Result.Truncated)
		require.Equal(t, "allow", fixture.audit.last().Decision)
	})

	t.Run("unknown plan fails closed before query", func(t *testing.T) {
		fixture := newPipelineFixture(t)
		explainErr := errors.New("plan size unavailable")
		fixture.executor.explainErr = explainErr

		response, err := fixture.pipeline.Process(
			context.Background(),
			requestWithSQL("SELECT phone FROM public.customers WHERE id > 0"),
		)

		require.ErrorIs(t, err, explainErr)
		require.Equal(t, model.DecisionDeny, response.Decision)
		require.Nil(t, response.Result)
		calls := fixture.executor.callsSnapshot()
		require.Equal(t, 1, calls.explain)
		require.Zero(t, calls.query)
		require.Equal(t, "error", fixture.audit.last().Decision)
	})
}

func TestPipelineRedactsDirectAliasedSourceColumn(t *testing.T) {
	fixture := newPipelineFixture(t)
	fixture.executor.queryResult = model.QueryResult{
		Columns:  []string{"mobile"},
		Rows:     [][]string{{"13812345678"}},
		RowCount: 1,
	}

	response, err := fixture.pipeline.Process(
		context.Background(),
		requestWithSQL("SELECT phone AS mobile FROM public.customers WHERE id = 1 LIMIT 1"),
	)

	require.NoError(t, err)
	require.Equal(t, model.DecisionAllow, response.Decision)
	require.NotNil(t, response.Result)
	require.Equal(t, "138****5678", response.Result.Rows[0][0])
	require.Equal(t, map[int]mask.SensitiveType{0: mask.TypePhone}, response.Redact.TouchedColumns)
	require.Equal(t, 1, response.Redact.MaskedCells)
}

func TestPipelineFallsBackToLegacyRedactor(t *testing.T) {
	fixture := newPipelineFixture(t)
	legacy := &legacyRedactor{}
	fixture.redactors.redactor = legacy
	fixture.executor.queryResult = model.QueryResult{
		Columns:  []string{"mobile"},
		Rows:     [][]string{{"unchanged"}},
		RowCount: 1,
	}

	response, err := fixture.pipeline.Process(
		context.Background(),
		requestWithSQL("SELECT phone AS mobile FROM public.customers WHERE id = 1 LIMIT 1"),
	)

	require.NoError(t, err)
	require.Equal(t, 1, legacy.calls)
	require.Equal(t, "legacy", response.Result.Rows[0][0])
	require.Equal(t, 1, response.Redact.MaskedCells)
}

func TestResolveColumnSourcesRejectsUnsafeAlignment(t *testing.T) {
	tests := []struct {
		name        string
		refs        []model.DirectProjectionRef
		columnCount int
		expected    []mask.ColumnSource
	}{
		{
			name: "single star suffix resolves from end",
			refs: []model.DirectProjectionRef{
				{Column: "phone", Offset: 0, FromEnd: true, Source: model.ObjectRef{Table: "customers"}},
			},
			columnCount: 4,
			expected: []mask.ColumnSource{
				{}, {}, {}, {Column: "phone", Source: model.ObjectRef{Table: "customers"}},
			},
		},
		{
			name: "multiple stars preserve only safe edges",
			refs: []model.DirectProjectionRef{
				{Column: "id", Offset: 0},
				{Column: "email", Offset: 0, FromEnd: true},
			},
			columnCount: 5,
			expected: []mask.ColumnSource{
				{Column: "id"}, {}, {}, {}, {Column: "email"},
			},
		},
		{
			name:        "offset beyond result columns invalidates all sources",
			refs:        []model.DirectProjectionRef{{Column: "phone", Offset: 1}},
			columnCount: 1,
			expected:    nil,
		},
		{
			name: "left and right overlap invalidates all sources",
			refs: []model.DirectProjectionRef{
				{Column: "phone", Offset: 0},
				{Column: "email", Offset: 0, FromEnd: true},
			},
			columnCount: 1,
			expected:    nil,
		},
		{
			name:        "empty reference invalidates all sources",
			refs:        []model.DirectProjectionRef{{Offset: 0}},
			columnCount: 1,
			expected:    nil,
		},
		{
			name:        "empty result invalidates all sources",
			refs:        []model.DirectProjectionRef{{Column: "phone", Offset: 0}},
			columnCount: 0,
			expected:    nil,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.expected, resolveColumnSources(test.refs, test.columnCount))
		})
	}
}

func TestPipelineUsesDefaultRowLimit(t *testing.T) {
	fixture := newPipelineFixture(t)
	fixture.datasources.datasource.RowLimit = 0
	_, err := fixture.pipeline.Process(context.Background(), defaultRequest())
	require.NoError(t, err)
	limit, _, _ := fixture.executor.executionSettings()
	require.Equal(t, defaultRowLimit, limit)
}

func TestPipelineExplainOnlyEvaluatesWithoutExecution(t *testing.T) {
	fixture := newPipelineFixture(t)
	request := defaultRequest()
	request.ExplainOnly = true
	request.MCPTool = "explain_query"
	response, err := fixture.pipeline.Process(context.Background(), request)
	require.NoError(t, err)
	require.Equal(t, model.DecisionAllow, response.Decision)
	require.Nil(t, response.Result)
	require.Equal(t, int64(1), response.Assessment.EstScanRows)
	calls := fixture.executor.callsSnapshot()
	require.Equal(t, 1, calls.explain)
	require.Zero(t, calls.query+calls.execute+calls.sessionQuery+calls.sessionExecute)
	require.Equal(t, "explain_query", *fixture.audit.last().MCPTool)
}

func TestPipelineRequireApprovalOnlyRaisesNonDeny(t *testing.T) {
	t.Run("allow becomes approve", func(t *testing.T) {
		fixture := newPipelineFixture(t)
		request := defaultRequest()
		request.RequireApproval = true
		request.MCPTool = "request_approval"
		response, err := fixture.pipeline.Process(context.Background(), request)
		require.NoError(t, err)
		require.Equal(t, model.DecisionApprove, response.Decision)
		require.NotEmpty(t, response.ApprovalID)
		calls := fixture.executor.callsSnapshot()
		require.Equal(t, 1, calls.explain)
		require.Zero(t, calls.query+calls.execute+calls.sessionQuery+calls.sessionExecute)
		require.Equal(t, 1, fixture.approvals.calls())
	})
	t.Run("deny remains deny", func(t *testing.T) {
		fixture := newPipelineFixture(t)
		request := requestWithSQLAndSession("UPDATE public.orders SET total = 1")
		request.RequireApproval = true
		response, err := fixture.pipeline.Process(context.Background(), request)
		require.NoError(t, err)
		require.Equal(t, model.DecisionDeny, response.Decision)
		require.Empty(t, response.ApprovalID)
		require.Zero(t, fixture.executors.calls())
		require.Zero(t, fixture.approvals.calls())
	})
}

func TestPipelineAllowedWriteUsesExecuteWithoutRedactor(t *testing.T) {
	fixture := newPipelineFixture(t)
	response, err := fixture.pipeline.Process(
		context.Background(),
		requestWithSQL("UPDATE public.orders SET total = 1 WHERE id = 1"),
	)
	require.NoError(t, err)
	require.Equal(t, model.DecisionAllow, response.Decision)
	require.NotNil(t, response.Result)
	require.Equal(t, 1, response.Result.RowCount)
	calls := fixture.executor.callsSnapshot()
	require.Equal(t, 1, calls.explain)
	require.Equal(t, 1, calls.execute)
	require.Zero(t, calls.query+calls.sessionQuery+calls.sessionExecute)
	require.Zero(t, fixture.redactors.calls())
	require.Equal(t, 1, *fixture.audit.last().RowsReturned)
}

func TestPipelineColumnPolicyDenyOnlyChecksObjectExistence(t *testing.T) {
	fixture := newPipelineFixture(t)
	columns := "id"
	fixture.policies.policies = append(fixture.policies.policies, model.Policy{
		ID:           "column-policy",
		AgentID:      "agent-1",
		DatasourceID: "datasource-1",
		ObjectType:   "column",
		ObjectName:   "public.customers",
		Columns:      &columns,
		Action:       "allow",
	})
	response, err := fixture.pipeline.Process(context.Background(), defaultRequest())
	require.NoError(t, err)
	require.Equal(t, model.DecisionDeny, response.Decision)
	require.Contains(t, response.Assessment.Reason, "public.customers.phone")
	require.Equal(t, 1, fixture.executors.calls())
	calls := fixture.executor.callsSnapshot()
	require.Equal(t, 1, calls.explain)
	require.Zero(t, calls.query+calls.execute)
	require.Equal(t, "deny", fixture.audit.last().Decision)
}

func TestPipelineProjectionColumnAuthorization(t *testing.T) {
	t.Run("predicate-only N106 column is allowed", func(t *testing.T) {
		fixture := newPipelineFixture(t, WithRuleLayers(engine.RuleLayers{
			Agent: engine.RuleLayer{
				"R008": {Thresholds: map[string]float64{
					rules.ThresholdQPS:           10_000,
					rules.ThresholdMaxConcurrent: 100,
				}},
			},
		}))
		columns := "id,name,amount"
		fixture.policies.policies = append(fixture.policies.policies, model.Policy{
			ID: "orders-columns", AgentID: "agent-1", DatasourceID: "datasource-1",
			ObjectType: "column", ObjectName: "public.orders", Columns: &columns, Action: "allow",
		})
		response, err := fixture.pipeline.Process(
			context.Background(),
			requestWithSQL("SELECT id,name,amount FROM public.orders WHERE user_id=1 LIMIT 10"),
		)
		require.NoError(t, err)
		require.Equal(t, model.DecisionAllow, response.Decision)
		for _, hit := range response.Assessment.Hits {
			require.NotEqual(t, "POLICY_COLUMN", hit.RuleID)
		}
		require.Equal(t, 1, fixture.executor.callsSnapshot().query)
	})

	t.Run("unauthorized projection is denied before query", func(t *testing.T) {
		fixture := newPipelineFixture(t)
		fixture.policies.policies = append(fixture.policies.policies,
			model.Policy{ID: "employees-table", AgentID: "agent-1", DatasourceID: "datasource-1", ObjectType: "table", ObjectName: "public.employees", Action: "allow"},
			model.Policy{ID: "employees-columns", AgentID: "agent-1", DatasourceID: "datasource-1", ObjectType: "column", ObjectName: "public.employees", Columns: stringPointer("id"), Action: "allow"},
		)
		response, err := fixture.pipeline.Process(context.Background(), requestWithSQL("SELECT salary FROM public.employees"))
		require.NoError(t, err)
		require.Equal(t, model.DecisionDeny, response.Decision)
		require.Contains(t, response.Assessment.Reason, "salary")
		require.Zero(t, fixture.executor.callsSnapshot().query)
	})

	t.Run("star projection is denied by exact column ACL", func(t *testing.T) {
		fixture := newPipelineFixture(t)
		columns := "id"
		fixture.policies.policies = append(fixture.policies.policies, model.Policy{
			ID: "orders-columns", AgentID: "agent-1", DatasourceID: "datasource-1",
			ObjectType: "column", ObjectName: "public.orders", Columns: &columns, Action: "allow",
		})
		response, err := fixture.pipeline.Process(context.Background(), requestWithSQL("SELECT public.orders.* FROM public.orders"))
		require.NoError(t, err)
		require.Equal(t, model.DecisionDeny, response.Decision)
		require.Contains(t, response.Assessment.Reason, "SELECT *")
		require.Equal(t, 1, fixture.executors.calls())
		calls := fixture.executor.callsSnapshot()
		require.Equal(t, 1, calls.explain)
		require.Zero(t, calls.query+calls.execute)
	})

	t.Run("star projection is allowed with table-only grant", func(t *testing.T) {
		fixture := newPipelineFixture(t)
		response, err := fixture.pipeline.Process(context.Background(), requestWithSQL("SELECT * FROM public.orders LIMIT 1"))
		require.NoError(t, err)
		require.Equal(t, model.DecisionAllow, response.Decision)
		require.Equal(t, 1, fixture.executor.callsSnapshot().query)
	})

	t.Run("wildcard table grant keeps broad column semantics", func(t *testing.T) {
		fixture := newPipelineFixture(t)
		columns := "id"
		fixture.policies.policies = append(fixture.policies.policies,
			model.Policy{ID: "schema-grant", AgentID: "agent-1", DatasourceID: "datasource-1", ObjectType: "schema", ObjectName: "public.*", Action: "allow"},
			model.Policy{ID: "orders-columns", AgentID: "agent-1", DatasourceID: "datasource-1", ObjectType: "column", ObjectName: "public.orders", Columns: &columns, Action: "allow"},
		)
		response, err := fixture.pipeline.Process(context.Background(), requestWithSQL("SELECT * FROM public.orders LIMIT 1"))
		require.NoError(t, err)
		require.Equal(t, model.DecisionAllow, response.Decision)
		require.Equal(t, 1, fixture.executor.callsSnapshot().query)
	})

	t.Run("join keeps v0.1 table-only behavior", func(t *testing.T) {
		fixture := newPipelineFixture(t)
		ordersColumns, customerColumns := "id", "id"
		fixture.policies.policies = append(fixture.policies.policies,
			model.Policy{ID: "orders-columns", AgentID: "agent-1", DatasourceID: "datasource-1", ObjectType: "column", ObjectName: "public.orders", Columns: &ordersColumns, Action: "allow"},
			model.Policy{ID: "customer-columns", AgentID: "agent-1", DatasourceID: "datasource-1", ObjectType: "column", ObjectName: "public.customers", Columns: &customerColumns, Action: "allow"},
		)
		response, err := fixture.pipeline.Process(context.Background(), requestWithSQL(
			"SELECT o.total,c.phone FROM public.orders o JOIN public.customers c ON c.id=o.customer_id LIMIT 1",
		))
		require.NoError(t, err)
		require.Equal(t, model.DecisionAllow, response.Decision)
		require.Equal(t, 1, fixture.executor.callsSnapshot().query)
	})
}

func TestPipelineWarnExecutesAndAuditsWarn(t *testing.T) {
	fixture := newPipelineFixture(t)
	fixture.executor.transaction = rules.TransactionState{
		InTransaction: true,
		AgeMS:         6_000,
		IdleMS:        6_000,
	}
	response, err := fixture.pipeline.Process(context.Background(), defaultRequest())
	require.NoError(t, err)
	require.Equal(t, model.DecisionWarn, response.Decision)
	require.Equal(t, "warn", fixture.audit.last().Decision)
	require.Equal(t, 1, fixture.executor.callsSnapshot().query)
}

func TestPipelineExecutionErrorIsAuditedAsError(t *testing.T) {
	fixture := newPipelineFixture(t)
	expected := errors.New("query failed")
	fixture.executor.queryErr = expected
	response, err := fixture.pipeline.Process(context.Background(), defaultRequest())
	require.ErrorIs(t, err, expected)
	require.Equal(t, model.DecisionDeny, response.Decision)
	require.Equal(t, string(executor.DBErrorCodeGatewayInternal), response.ErrorCode)
	require.Equal(t, "网关内部错误", response.ErrorMessage)
	require.Nil(t, response.Result)
	log := fixture.audit.last()
	require.Equal(t, "error", log.Decision)
	require.NotNil(t, log.ErrorMsg)
	require.Equal(t, "网关内部错误", *log.ErrorMsg)
	require.NotContains(t, *log.ErrorMsg, expected.Error())
}

func TestPipelineDynamicAndRedactionErrorsNeverReturnData(t *testing.T) {
	t.Run("explain error", func(t *testing.T) {
		fixture := newPipelineFixture(t)
		expected := errors.New("explain failed")
		fixture.executor.explainErr = expected
		response, err := fixture.pipeline.Process(context.Background(), defaultRequest())
		require.ErrorIs(t, err, expected)
		require.Nil(t, response.Result)
		calls := fixture.executor.callsSnapshot()
		require.Equal(t, 1, calls.explain)
		require.Zero(t, calls.query+calls.execute+calls.sessionQuery+calls.sessionExecute)
		require.Equal(t, "error", fixture.audit.last().Decision)
	})
	t.Run("redactor builder error", func(t *testing.T) {
		const raw = "T41_PIPELINE_RAW_SENTINEL_b7a3"
		fixture := newPipelineFixture(t)
		fixture.executor.queryResult = model.QueryResult{Columns: []string{"name"}, Rows: [][]string{{raw}}, RowCount: 1}
		expected := mask.ErrHashKeyRequired
		fixture.redactors.err = expected
		response, err := fixture.pipeline.Process(context.Background(), defaultRequest())
		require.ErrorIs(t, err, expected)
		require.Nil(t, response.Result)
		require.Equal(t, 1, fixture.executor.callsSnapshot().query)
		log := fixture.audit.last()
		require.Equal(t, "error", log.Decision)
		require.Equal(t, 1, *log.RowsReturned)
		encoded, marshalErr := json.Marshal(log)
		require.NoError(t, marshalErr)
		require.NotContains(t, string(encoded), raw)
	})
	for _, test := range []struct {
		name string
		rows [][]string
	}{
		{name: "short result row", rows: [][]string{{"secret"}}},
		{name: "long result row", rows: [][]string{{"secret", "extra", "cell"}}},
		{name: "nil result row", rows: [][]string{nil}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPipelineFixture(t)
			fixture.executor.queryResult = model.QueryResult{
				Columns: []string{"a", "b"}, Rows: test.rows, RowCount: 1,
			}
			response, err := fixture.pipeline.Process(context.Background(), defaultRequest())
			require.ErrorIs(t, err, errNonRectangularResult)
			require.Equal(t, model.DecisionDeny, response.Decision)
			require.Nil(t, response.Result)
			require.Empty(t, response.Redact)
			require.Zero(t, fixture.redactors.calls(), "rectangle validation must precede redactor construction")
			require.Equal(t, "error", fixture.audit.last().Decision)
		})
	}
}

func TestPipelineAuditErrorFailsRequest(t *testing.T) {
	fixture := newPipelineFixture(t)
	expected := errors.New("pq: password authentication failed for postgres://secret@audit")
	fixture.audit.err = expected
	response, err := fixture.pipeline.Process(context.Background(), defaultRequest())
	require.ErrorIs(t, err, ErrAuditUnavailable)
	require.Equal(t, model.DecisionDeny, response.Decision)
	require.Nil(t, response.Result)
	require.NotContains(t, err.Error(), expected.Error())
	require.NotContains(t, response.Assessment.Reason, "postgres://")
	require.NotContains(t, response.Assessment.Reason, "password authentication")
}

func TestPipelineNilContextIsAuditedWithoutTouchingDatabase(t *testing.T) {
	fixture := newPipelineFixture(t)
	response, err := fixture.pipeline.Process(nil, defaultRequest())
	require.ErrorIs(t, err, ErrInvalidRequest)
	require.Equal(t, model.DecisionDeny, response.Decision)
	require.Zero(t, fixture.executors.calls())
	require.Equal(t, 1, fixture.audit.calls())
	require.Equal(t, "error", fixture.audit.last().Decision)
}

func TestPipelineStageLatencyHasEightNonNegativeKeys(t *testing.T) {
	fixture := newPipelineFixture(t)
	response, err := fixture.pipeline.Process(context.Background(), defaultRequest())
	require.NoError(t, err)
	stages := pipelineStageNames()
	require.Len(t, response.Assessment.StageLatency, len(stages))
	for _, stage := range stages {
		latency, exists := response.Assessment.StageLatency[stage]
		require.True(t, exists, stage)
		require.GreaterOrEqual(t, latency, int64(0), stage)
	}
}

func TestAuditMappingIncludesFrozenFields(t *testing.T) {
	conversation := "conversation-1"
	clientIP := "127.0.0.1"
	modelName := "test-model"
	request := Request{
		DatasourceID:   "datasource-1",
		SQL:            "SELECT phone FROM public.customers",
		SessionID:      "session-1",
		ConversationID: &conversation,
		MCPTool:        "query",
		ClientIP:       &clientIP,
		ModelName:      &modelName,
	}
	agent := &model.Agent{ID: "agent-1"}
	datasource := &model.Datasource{ID: "datasource-1", DBType: "postgres"}
	ast := &model.AST{
		StmtType:   "SELECT",
		Normalized: "SELECT phone FROM public.customers",
		Tables:     []model.ObjectRef{{Schema: "public", Table: "customers"}},
		Explain:    &model.ExplainInfo{EstScanRows: 12},
	}
	response := Response{
		ErrorCode: string(executor.DBErrorCodeGatewayInternal),
		Assessment: model.Assessment{
			Risk: model.RiskWarn,
			Hits: []model.RuleHit{{
				RuleID: "R005", Risk: model.RiskWarn, Decision: model.DecisionWarn,
				Message: "warning", Suggestion: "limit rows",
			}},
		},
	}
	result := &model.QueryResult{RowCount: 3}
	operationError := errors.New("sample error")
	log, err := mapAuditLog(
		request,
		agent,
		datasource,
		ast,
		response,
		result,
		"error",
		operationError,
		time.Now().Add(-time.Millisecond),
	)
	require.NoError(t, err)
	require.Equal(t, "agent-1", *log.AgentID)
	require.Equal(t, "datasource-1", *log.DatasourceID)
	require.Equal(t, "session-1", *log.SessionID)
	require.Equal(t, conversation, *log.ConversationID)
	require.Equal(t, "query", *log.MCPTool)
	require.Equal(t, "postgres", *log.DBType)
	require.Equal(t, request.SQL, *log.SQLRaw)
	require.Equal(t, ast.Normalized, *log.SQLNorm)
	require.Equal(t, "SELECT", *log.StmtType)
	require.Equal(t, "public.customers", *log.Objects)
	require.Equal(t, "error", log.Decision)
	require.Equal(t, int(model.RiskWarn), *log.RiskLevel)
	require.Equal(t, int64(12), *log.EstRows)
	require.Equal(t, 3, *log.RowsReturned)
	require.GreaterOrEqual(t, *log.LatencyMS, int64(0))
	require.Equal(t, clientIP, *log.ClientIP)
	require.Equal(t, modelName, *log.ModelName)
	require.Equal(t, "网关内部错误", *log.ErrorMsg)
	require.Equal(t, string(executor.DBErrorCodeGatewayInternal), *log.ErrorCode)
	var hits []model.RuleHit
	require.NoError(t, json.Unmarshal([]byte(*log.RuleHits), &hits))
	require.Equal(t, response.Assessment.Hits, hits)
}

func TestMapAuditLogPersistsOnlyStableErrorCodesForErrorDecisions(t *testing.T) {
	for _, test := range []struct {
		name          string
		decision      string
		code          string
		wantErrorCode bool
	}{
		{name: "error with stable code", decision: "error", code: string(executor.DBErrorCodeTimeout), wantErrorCode: true},
		{name: "commit outcome unknown", decision: "error", code: string(executor.DBErrorCodeCommitOutcomeUnknown), wantErrorCode: true},
		{name: "audit unavailable", decision: "error", code: string(executor.DBErrorCodeAuditUnavailable), wantErrorCode: true},
		{name: "allow", decision: "allow", code: string(executor.DBErrorCodeTimeout)},
		{name: "deny", decision: "deny", code: string(executor.DBErrorCodeTimeout)},
		{name: "driver code rejected", decision: "error", code: "42P01"},
		{name: "empty", decision: "error"},
	} {
		t.Run(test.name, func(t *testing.T) {
			log, err := mapAuditLog(
				Request{}, nil, nil, nil, Response{ErrorCode: test.code}, nil,
				test.decision, nil, time.Now(),
			)
			require.NoError(t, err)
			if test.wantErrorCode {
				require.Equal(t, test.code, *log.ErrorCode)
			} else {
				require.Nil(t, log.ErrorCode)
			}
		})
	}
}

func TestMergeAssessmentFourStatePriorityAndReplacement(t *testing.T) {
	first := model.Assessment{Hits: []model.RuleHit{
		{RuleID: "R003", Decision: model.DecisionDeny, Risk: model.RiskDeny, Message: "deny", Suggestion: "fix deny"},
		{RuleID: "R005", Decision: model.DecisionWarn, Risk: model.RiskWarn, Message: "old warn", Suggestion: "old"},
	}}
	second := model.Assessment{
		EstScanRows: 50,
		Hits: []model.RuleHit{
			{RuleID: "R004", Decision: model.DecisionApprove, Risk: model.RiskApprove, Message: "approve", Suggestion: "fix approve"},
			{RuleID: "R005", Decision: model.DecisionWarn, Risk: model.RiskWarn, Message: "new warn", Suggestion: "new"},
		},
	}
	merged := mergeAssessment(first, second)
	require.Equal(t, model.DecisionDeny, merged.Decision)
	require.Equal(t, model.RiskDeny, merged.Risk)
	require.Equal(t, "deny", merged.Reason)
	require.Equal(t, []string{"R003", "R004", "R005"}, ruleHitIDs(merged.Hits))
	require.Equal(t, "new warn", merged.Hits[2].Message)
	require.Equal(t, int64(50), merged.EstScanRows)
}

func TestStaticRuleSetCannotTouchPanicMetadataProvider(t *testing.T) {
	fixture := newPipelineFixture(t)
	response, err := fixture.pipeline.Process(context.Background(), defaultRequest())
	require.NoError(t, err)
	require.Equal(t, model.DecisionAllow, response.Decision)
	all, err := assembleRules("postgres", &validationLimiter{}, panicMetadataProvider{})
	require.NoError(t, err)
	static, dynamic := splitRules(all)
	require.Equal(t, []string{"R004", "R005", "R105", "R106", "R107"}, ruleIDs(dynamic))
	for _, id := range ruleIDs(static) {
		require.False(t, isDynamicRuleID(id), id)
	}
	all, err = assembleRules("mysql", &validationLimiter{}, panicMetadataProvider{})
	require.NoError(t, err)
	_, dynamic = splitRules(all)
	require.Equal(t, []string{"R004", "R005", "R204"}, ruleIDs(dynamic))
}

func TestPipelineUsesBoundSessionOnlyWhenRequested(t *testing.T) {
	t.Run("stateless", func(t *testing.T) {
		fixture := newPipelineFixture(t)
		_, err := fixture.pipeline.Process(context.Background(), defaultRequest())
		require.NoError(t, err)
		calls := fixture.executor.callsSnapshot()
		require.Zero(t, calls.openSession+calls.sessionExplain+calls.sessionQuery+calls.sessionExecute)
		require.Equal(t, 1, calls.explain)
		require.Equal(t, 1, calls.query)
	})
	t.Run("bound session", func(t *testing.T) {
		fixture := newPipelineFixture(t)
		request := defaultRequest()
		request.SessionID = "session-1"
		_, err := fixture.pipeline.Process(context.Background(), request)
		require.NoError(t, err)
		calls := fixture.executor.callsSnapshot()
		require.Equal(t, 1, calls.openSession)
		require.Equal(t, 1, calls.sessionExplain)
		require.Equal(t, 1, calls.sessionQuery)
		require.Zero(t, calls.sessionClose)
		require.Zero(t, calls.explain+calls.query+calls.execute+calls.sessionExecute)
	})
}

func TestPipelineDynamicExplicitDenyOnlyReadsExplain(t *testing.T) {
	fixture := newPipelineFixture(t, WithRuleLayers(engine.RuleLayers{
		Agent: engine.RuleLayer{"R004": {ExplicitDeny: true}},
	}))
	request := defaultRequest()
	request.SessionID = "deny-session"
	response, err := fixture.pipeline.Process(context.Background(), request)
	require.NoError(t, err)
	require.Equal(t, model.DecisionDeny, response.Decision)
	calls := fixture.executor.callsSnapshot()
	require.Equal(t, 1, fixture.executors.calls())
	require.Equal(t, 1, calls.openSession)
	require.Equal(t, 1, calls.sessionExplain)
	require.Zero(t, calls.explain)
	require.Zero(t, calls.query+calls.execute+calls.sessionQuery+calls.sessionExecute)
	require.Equal(t, 1, calls.sessionClose)
	require.Equal(t, "deny", fixture.audit.last().Decision)
}

func TestPipelineReleasesR008ReservationOnEveryFinishedRequest(t *testing.T) {
	fixture := newPipelineFixture(t)
	limiter := &spyRateLimiter{}
	fixture.pipeline.limiter = limiter
	for index := 0; index < 2; index++ {
		_, err := fixture.pipeline.Process(context.Background(), defaultRequest())
		require.NoError(t, err)
	}
	allowed, released, inFlight := limiter.snapshot()
	require.Equal(t, 2, allowed)
	require.Equal(t, 2, released)
	require.Zero(t, inFlight)
	fixture.audit.err = errors.New("audit unavailable")
	_, err := fixture.pipeline.Process(context.Background(), defaultRequest())
	require.Error(t, err)
	allowed, released, inFlight = limiter.snapshot()
	require.Equal(t, 3, allowed)
	require.Equal(t, 3, released)
	require.Zero(t, inFlight)
}

func TestPipelineConcurrentRequestsAreRaceSafe(t *testing.T) {
	fixture := newPipelineFixture(t)
	const requests = 50
	var wait sync.WaitGroup
	errorsChannel := make(chan error, requests)
	for index := 0; index < requests; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := fixture.pipeline.Process(context.Background(), defaultRequest())
			errorsChannel <- err
		}()
	}
	wait.Wait()
	close(errorsChannel)
	for err := range errorsChannel {
		require.NoError(t, err)
	}
	require.Equal(t, requests, fixture.audit.calls())
}

func TestPipelineNewRejectsNilPortsAndInvalidSecret(t *testing.T) {
	fixture := newPipelineFixture(t)
	var nilAuthenticator *fakeAuthenticator
	var nilDatasourceReader *fakeDatasourceReader
	var nilPolicyLoader *fakePolicyLoader
	var nilExecutorProvider *fakeExecutorProvider
	var nilApprovalWriter *fakeApprovalWriter
	var nilAuditRecorder *fakeAuditRecorder
	var nilRedactorBuilder *fakeRedactorBuilder
	tests := []struct {
		name   string
		mutate func(*Ports)
	}{
		{name: "authenticator", mutate: func(ports *Ports) { ports.Authenticator = nilAuthenticator }},
		{name: "datasource reader", mutate: func(ports *Ports) { ports.Datasources = nilDatasourceReader }},
		{name: "policy loader", mutate: func(ports *Ports) { ports.Policies = nilPolicyLoader }},
		{name: "executor provider", mutate: func(ports *Ports) { ports.Executors = nilExecutorProvider }},
		{name: "approval writer", mutate: func(ports *Ports) { ports.Approvals = nilApprovalWriter }},
		{name: "audit recorder", mutate: func(ports *Ports) { ports.Audit = nilAuditRecorder }},
		{name: "redactor builder", mutate: func(ports *Ports) { ports.Redactors = nilRedactorBuilder }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ports := fixture.ports()
			test.mutate(&ports)
			_, err := New(ports, testPipelineSecret)
			require.ErrorIs(t, err, ErrInvalidPorts)
		})
	}
	_, err := New(fixture.ports(), []byte("short"))
	require.ErrorIs(t, err, ErrInvalidSecret)
}

type pipelineFixture struct {
	pipeline      *Pipeline
	authenticator *fakeAuthenticator
	datasources   *fakeDatasourceReader
	policies      *fakePolicyLoader
	executors     *fakeExecutorProvider
	approvals     *fakeApprovalWriter
	audit         *fakeAuditRecorder
	redactors     *fakeRedactorBuilder
	ruleOverrides *fakeRuleOverrideReader
	executor      *spyExecutor
}

func newPipelineFixture(t *testing.T, options ...Option) *pipelineFixture {
	t.Helper()
	redactor, err := mask.NewRedactor([]mask.Rule{{
		Column:        "phone",
		SensitiveType: mask.TypePhone,
		Algorithm:     mask.AlgoMask,
	}})
	require.NoError(t, err)
	spy := &spyExecutor{
		dialect:     "postgres",
		explain:     model.ExplainInfo{EstScanRows: 1, UsesIndex: true},
		queryResult: model.QueryResult{Columns: []string{"phone"}, Rows: [][]string{{"13812345678"}}, RowCount: 1},
		execResult:  model.QueryResult{RowCount: 1},
		hasIndex:    true,
	}
	fixture := &pipelineFixture{
		authenticator: &fakeAuthenticator{agent: model.Agent{ID: "agent-1", Status: "active", Level: "dml"}},
		datasources: &fakeDatasourceReader{datasource: model.Datasource{
			ID: "datasource-1", DBType: "postgres", RowLimit: 2, StmtTimeoutMS: 5_000,
		}},
		policies: &fakePolicyLoader{policies: []model.Policy{
			{ID: "p1", AgentID: "agent-1", DatasourceID: "datasource-1", ObjectType: "table", ObjectName: "public.orders", Action: "allow"},
			{ID: "p2", AgentID: "agent-1", DatasourceID: "datasource-1", ObjectType: "table", ObjectName: "public.customers", Action: "allow"},
		}},
		executors:     &fakeExecutorProvider{executor: spy},
		approvals:     &fakeApprovalWriter{},
		audit:         &fakeAuditRecorder{},
		redactors:     &fakeRedactorBuilder{redactor: redactor},
		ruleOverrides: nil,
		executor:      spy,
	}
	fixture.approvals.audit = fixture.audit
	constructed, err := New(fixture.ports(), testPipelineSecret, options...)
	require.NoError(t, err)
	fixture.pipeline = constructed
	return fixture
}

func (fixture *pipelineFixture) ports() Ports {
	return Ports{
		Authenticator: fixture.authenticator,
		Datasources:   fixture.datasources,
		Policies:      fixture.policies,
		Executors:     fixture.executors,
		Approvals:     fixture.approvals,
		Audit:         fixture.audit,
		Redactors:     fixture.redactors,
		RuleOverrides: fixture.ruleOverrides,
	}
}

func defaultRequest() Request {
	return Request{
		APIKey:       "asql_test",
		DatasourceID: "datasource-1",
		SQL:          "SELECT phone FROM public.customers WHERE id = 1 LIMIT 1",
		MCPTool:      "query",
	}
}

func requestWithSQL(sql string) Request {
	request := defaultRequest()
	request.SQL = sql
	return request
}

func requestWithSQLAndSession(sql string) Request {
	request := requestWithSQL(sql)
	request.SessionID = "must-not-open"
	return request
}

type fakeAuthenticator struct {
	mu    sync.Mutex
	agent model.Agent
	err   error
}

func (fake *fakeAuthenticator) Authenticate(context.Context, string) (model.Agent, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return fake.agent, fake.err
}

type fakeDatasourceReader struct {
	mu         sync.Mutex
	datasource model.Datasource
	err        error
}

func (fake *fakeDatasourceReader) Get(context.Context, string) (model.Datasource, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return fake.datasource, fake.err
}

type fakePolicyLoader struct {
	mu       sync.Mutex
	policies []model.Policy
	err      error
}

type fakeRuleOverrideReader struct {
	mu    sync.Mutex
	rules []model.Rule
	err   error
	calls int
}

func (fake *fakeRuleOverrideReader) List(context.Context, string) ([]model.Rule, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.calls++
	return append([]model.Rule(nil), fake.rules...), fake.err
}

func (fake *fakeRuleOverrideReader) setEnabled(id, dbType string, enabled bool) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.rules = []model.Rule{{ID: id, DBType: dbType, Enabled: enabled}}
}

func (fake *fakePolicyLoader) ListByAgentAndDatasource(
	context.Context,
	string,
	string,
) ([]model.Policy, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return append([]model.Policy(nil), fake.policies...), fake.err
}

type fakeExecutorProvider struct {
	mu       sync.Mutex
	executor rawTestExecutor
	err      error
	count    int
}

func (fake *fakeExecutorProvider) GetOrOpen(model.Datasource, []byte) (rawTestExecutor, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.count++
	return fake.executor, fake.err
}

func (fake *fakeExecutorProvider) AuthorizedExecute(
	ctx context.Context,
	_ model.Datasource,
	_ []byte,
	sqlText string,
	sessionID string,
) (executor.Statement, error) {
	fake.mu.Lock()
	fake.count++
	delegate, err := fake.executor, fake.err
	fake.mu.Unlock()
	if err != nil || delegate == nil {
		return nil, err
	}
	bound := &testBoundStatement{delegate: delegate, sql: sqlText}
	if sessionID != "" {
		bound.session, err = delegate.OpenSession(ctx, sessionID)
		if err != nil {
			return nil, err
		}
	}
	return bound, nil
}

type testBoundStatement struct {
	delegate rawTestExecutor
	session  rawTestSession
	sql      string
}

func (*testBoundStatement) ReleaseReservation() {}

func (bound *testBoundStatement) Dialect() string { return bound.delegate.Dialect() }
func (bound *testBoundStatement) Explain(ctx context.Context) (model.ExplainInfo, error) {
	if bound.session != nil {
		return bound.session.Explain(ctx, bound.sql)
	}
	return bound.delegate.Explain(ctx, bound.sql)
}
func (bound *testBoundStatement) Query(ctx context.Context, limit int) (model.QueryResult, error) {
	if bound.session != nil {
		return bound.session.Query(ctx, bound.sql, limit)
	}
	return bound.delegate.Query(ctx, bound.sql, limit)
}
func (bound *testBoundStatement) Execute(ctx context.Context) (model.QueryResult, error) {
	if bound.session != nil {
		return bound.session.Execute(ctx, bound.sql)
	}
	return bound.delegate.Execute(ctx, bound.sql)
}
func (bound *testBoundStatement) ExecuteTransactional(ctx context.Context, before func(model.QueryResult) error) (model.QueryResult, error) {
	var tx rawTestWriteTx
	var err error
	if bound.session != nil {
		tx, err = bound.session.BeginWriteTx(ctx)
	} else {
		tx, err = bound.delegate.BeginWriteTx(ctx)
	}
	if err != nil {
		return model.QueryResult{}, err
	}
	result, err := tx.Execute(ctx, bound.sql)
	if err != nil {
		_ = tx.Rollback(ctx)
		return model.QueryResult{}, err
	}
	if before != nil {
		if err := before(result); err != nil {
			_ = tx.Rollback(ctx)
			return model.QueryResult{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return model.QueryResult{}, executor.ErrCommitOutcomeUnknown
	}
	return result, nil
}

type rawTestExecutor interface {
	Dialect() string
	OpenSession(context.Context, string) (rawTestSession, error)
	BeginWriteTx(context.Context) (rawTestWriteTx, error)
	Explain(context.Context, string) (model.ExplainInfo, error)
	Query(context.Context, string, int) (model.QueryResult, error)
	Execute(context.Context, string) (model.QueryResult, error)
}

type rawTestSession interface {
	BeginWriteTx(context.Context) (rawTestWriteTx, error)
	Explain(context.Context, string) (model.ExplainInfo, error)
	Query(context.Context, string, int) (model.QueryResult, error)
	Execute(context.Context, string) (model.QueryResult, error)
	TransactionState() (rules.TransactionState, error)
	MysqlTransactionState() (rules.MysqlTransactionState, error)
	Close() error
}

type rawTestWriteTx interface {
	Execute(context.Context, string) (model.QueryResult, error)
	Commit(context.Context) error
	Rollback(context.Context) error
}

func (bound *testBoundStatement) metadata() any {
	if bound.session != nil {
		return bound.session
	}
	return bound.delegate
}
func (bound *testBoundStatement) TableHasIndex(schema, table string) (bool, error) {
	return bound.metadata().(rules.MetadataProvider).TableHasIndex(schema, table)
}
func (bound *testBoundStatement) TableRowCount(schema, table string) (int64, error) {
	return bound.metadata().(rules.MetadataProvider).TableRowCount(schema, table)
}
func (bound *testBoundStatement) TransactionState() (rules.TransactionState, error) {
	return bound.metadata().(rules.TransactionMetadataProvider).TransactionState()
}
func (bound *testBoundStatement) MysqlTransactionState() (rules.MysqlTransactionState, error) {
	return bound.metadata().(rules.MysqlTransactionMetadataProvider).MysqlTransactionState()
}
func (bound *testBoundStatement) Close() error {
	if bound.session != nil {
		return bound.session.Close()
	}
	return nil
}

func (fake *fakeExecutorProvider) calls() int {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return fake.count
}

type fakeApprovalWriter struct {
	mu              sync.Mutex
	created         []model.Approval
	err             error
	workflowErr     error
	emptyApprovalID bool
	emptyAuditID    bool
	audit           *fakeAuditRecorder
}

func (fake *fakeApprovalWriter) CreatePendingWithAudit(
	ctx context.Context,
	approval model.Approval,
	log model.AuditLog,
) (model.Approval, model.AuditLog, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.workflowErr != nil {
		return model.Approval{}, model.AuditLog{}, fake.workflowErr
	}
	if fake.emptyApprovalID || fake.emptyAuditID {
		if fake.emptyApprovalID {
			approval.ID = ""
		}
		if fake.emptyAuditID {
			log.ID = 0
		} else {
			log.ID = 1
		}
		return approval, log, nil
	}
	if fake.audit == nil {
		return model.Approval{}, model.AuditLog{}, errors.New("audit recorder is unavailable")
	}
	recorded, err := fake.audit.Record(ctx, log)
	if err != nil {
		return model.Approval{}, model.AuditLog{}, err
	}
	approval.AuditID = int64Pointer(recorded.ID)
	fake.created = append(fake.created, approval)
	return approval, recorded, nil
}

func (fake *fakeApprovalWriter) Create(
	_ context.Context,
	approval model.Approval,
) (model.Approval, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.err != nil {
		return model.Approval{}, fake.err
	}
	fake.created = append(fake.created, approval)
	return approval, nil
}

func (fake *fakeApprovalWriter) calls() int {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return len(fake.created)
}

func (fake *fakeApprovalWriter) last() model.Approval {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return fake.created[len(fake.created)-1]
}

type fakeAuditRecorder struct {
	mu                sync.Mutex
	logs              []model.AuditLog
	err               error
	errAtAttempt      int
	attempts          int
	next              int64
	onRecord          func()
	onRecorded        func(model.AuditLog)
	waitForContextEnd bool
}

func (fake *fakeAuditRecorder) Record(
	ctx context.Context,
	log model.AuditLog,
) (model.AuditLog, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.attempts++
	if fake.onRecord != nil {
		fake.onRecord()
	}
	if fake.waitForContextEnd {
		<-ctx.Done()
		return model.AuditLog{}, ctx.Err()
	}
	if fake.err != nil && (fake.errAtAttempt == 0 || fake.attempts == fake.errAtAttempt) {
		return model.AuditLog{}, fake.err
	}
	fake.next++
	log.ID = fake.next
	fake.logs = append(fake.logs, log)
	if fake.onRecorded != nil {
		fake.onRecorded(log)
	}
	return log, nil
}

func (fake *fakeAuditRecorder) calls() int {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return len(fake.logs)
}

func (fake *fakeAuditRecorder) last() model.AuditLog {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return fake.logs[len(fake.logs)-1]
}

func (fake *fakeAuditRecorder) snapshot() []model.AuditLog {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return append([]model.AuditLog(nil), fake.logs...)
}

type fakeRedactorBuilder struct {
	mu       sync.Mutex
	redactor mask.Redactor
	err      error
	count    int
}

type legacyRedactor struct {
	calls int
}

func (redactor *legacyRedactor) Apply(result model.QueryResult) (model.QueryResult, mask.RedactReport) {
	redactor.calls++
	result.Rows[0][0] = "legacy"
	return result, mask.RedactReport{MaskedCells: 1}
}

func (fake *fakeRedactorBuilder) calls() int {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return fake.count
}

func (fake *fakeRedactorBuilder) RedactorFor(context.Context, string) (mask.Redactor, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.count++
	return fake.redactor, fake.err
}

type executorCalls struct {
	openSession    int
	beginWriteTx   int
	explain        int
	query          int
	execute        int
	sessionExplain int
	sessionQuery   int
	sessionExecute int
	sessionClose   int
}

type spyExecutor struct {
	mu               sync.Mutex
	dialect          string
	calls            executorCalls
	explain          model.ExplainInfo
	explainErr       error
	openSessionErr   error
	queryResult      model.QueryResult
	queryErr         error
	execResult       model.QueryResult
	executeErr       error
	hasIndex         bool
	tableRows        int64
	transaction      rules.TransactionState
	transactionErr   error
	mysqlTransaction rules.MysqlTransactionState
	lastQueryLimit   int
	explainDeadline  bool
	queryDeadline    bool
	beginErr         error
	commitErr        error
	rollbackErr      error
	events           []string
}

func (spy *spyExecutor) Dialect() string        { return spy.dialect }
func (*spyExecutor) Ping(context.Context) error { return nil }

func (spy *spyExecutor) OpenSession(context.Context, string) (rawTestSession, error) {
	spy.mu.Lock()
	defer spy.mu.Unlock()
	spy.calls.openSession++
	if spy.openSessionErr != nil {
		return nil, spy.openSessionErr
	}
	return &spySession{parent: spy}, nil
}

func (spy *spyExecutor) BeginWriteTx(context.Context) (rawTestWriteTx, error) {
	spy.mu.Lock()
	defer spy.mu.Unlock()
	spy.calls.beginWriteTx++
	spy.events = append(spy.events, "begin")
	if spy.beginErr != nil {
		return nil, spy.beginErr
	}
	return &spyWriteTx{parent: spy, execute: spy.Execute}, nil
}

func (spy *spyExecutor) Explain(ctx context.Context, _ string) (model.ExplainInfo, error) {
	spy.mu.Lock()
	defer spy.mu.Unlock()
	spy.calls.explain++
	_, spy.explainDeadline = ctx.Deadline()
	return spy.explain, spy.explainErr
}

func (spy *spyExecutor) Query(ctx context.Context, _ string, limit int) (model.QueryResult, error) {
	spy.mu.Lock()
	defer spy.mu.Unlock()
	spy.calls.query++
	spy.lastQueryLimit = limit
	_, spy.queryDeadline = ctx.Deadline()
	return spy.queryResult, spy.queryErr
}

func (spy *spyExecutor) Execute(context.Context, string) (model.QueryResult, error) {
	spy.mu.Lock()
	defer spy.mu.Unlock()
	spy.calls.execute++
	spy.events = append(spy.events, "execute")
	return spy.execResult, spy.executeErr
}

func (*spyExecutor) Close() error { return nil }

func (spy *spyExecutor) TableHasIndex(string, string) (bool, error) {
	spy.mu.Lock()
	defer spy.mu.Unlock()
	return spy.hasIndex, nil
}

func (spy *spyExecutor) TableRowCount(string, string) (int64, error) {
	spy.mu.Lock()
	defer spy.mu.Unlock()
	return spy.tableRows, nil
}

func (spy *spyExecutor) TransactionState() (rules.TransactionState, error) {
	spy.mu.Lock()
	defer spy.mu.Unlock()
	return spy.transaction, spy.transactionErr
}

func (spy *spyExecutor) MysqlTransactionState() (rules.MysqlTransactionState, error) {
	spy.mu.Lock()
	defer spy.mu.Unlock()
	return spy.mysqlTransaction, nil
}

func (spy *spyExecutor) callsSnapshot() executorCalls {
	spy.mu.Lock()
	defer spy.mu.Unlock()
	return spy.calls
}

func (spy *spyExecutor) executionSettings() (int, bool, bool) {
	spy.mu.Lock()
	defer spy.mu.Unlock()
	return spy.lastQueryLimit, spy.explainDeadline, spy.queryDeadline
}

func (spy *spyExecutor) appendEvent(event string) {
	spy.mu.Lock()
	defer spy.mu.Unlock()
	spy.events = append(spy.events, event)
}

func (spy *spyExecutor) eventSnapshot() []string {
	spy.mu.Lock()
	defer spy.mu.Unlock()
	return append([]string(nil), spy.events...)
}

type spySession struct {
	parent *spyExecutor
}

func (session *spySession) BeginWriteTx(context.Context) (rawTestWriteTx, error) {
	session.parent.mu.Lock()
	defer session.parent.mu.Unlock()
	session.parent.calls.beginWriteTx++
	session.parent.events = append(session.parent.events, "begin")
	if session.parent.beginErr != nil {
		return nil, session.parent.beginErr
	}
	return &spyWriteTx{parent: session.parent, execute: session.Execute}, nil
}

func (session *spySession) Query(ctx context.Context, sql string, limit int) (model.QueryResult, error) {
	session.parent.mu.Lock()
	defer session.parent.mu.Unlock()
	session.parent.calls.sessionQuery++
	session.parent.lastQueryLimit = limit
	_, session.parent.queryDeadline = ctx.Deadline()
	return session.parent.queryResult, session.parent.queryErr
}

func (session *spySession) Execute(ctx context.Context, sql string) (model.QueryResult, error) {
	session.parent.mu.Lock()
	defer session.parent.mu.Unlock()
	session.parent.calls.sessionExecute++
	session.parent.events = append(session.parent.events, "execute")
	return session.parent.execResult, session.parent.executeErr
}

func (session *spySession) Explain(ctx context.Context, sql string) (model.ExplainInfo, error) {
	session.parent.mu.Lock()
	defer session.parent.mu.Unlock()
	session.parent.calls.sessionExplain++
	_, session.parent.explainDeadline = ctx.Deadline()
	return session.parent.explain, session.parent.explainErr
}

func (session *spySession) TransactionState() (rules.TransactionState, error) {
	return session.parent.TransactionState()
}

func (session *spySession) MysqlTransactionState() (rules.MysqlTransactionState, error) {
	return session.parent.MysqlTransactionState()
}

func (session *spySession) Close() error {
	session.parent.mu.Lock()
	defer session.parent.mu.Unlock()
	session.parent.calls.sessionClose++
	return nil
}

func (session *spySession) TableHasIndex(schema, table string) (bool, error) {
	return session.parent.TableHasIndex(schema, table)
}

func (session *spySession) TableRowCount(schema, table string) (int64, error) {
	return session.parent.TableRowCount(schema, table)
}

type spyWriteTx struct {
	mu      sync.Mutex
	parent  *spyExecutor
	execute func(context.Context, string) (model.QueryResult, error)
	done    bool
}

func (tx *spyWriteTx) Execute(ctx context.Context, sql string) (model.QueryResult, error) {
	return tx.execute(ctx, sql)
}
func (tx *spyWriteTx) Commit(context.Context) error {
	tx.mu.Lock()
	defer tx.mu.Unlock()
	if tx.done {
		return nil
	}
	tx.done = true
	tx.parent.appendEvent("commit")
	return tx.parent.commitErr
}
func (tx *spyWriteTx) Rollback(context.Context) error {
	tx.mu.Lock()
	defer tx.mu.Unlock()
	if tx.done {
		return nil
	}
	tx.done = true
	tx.parent.appendEvent("rollback")
	return tx.parent.rollbackErr
}

func ruleIDs(input []engine.Rule) []string {
	ids := make([]string, 0, len(input))
	for _, rule := range input {
		ids = append(ids, rule.ID())
	}
	return ids
}

func ruleHitIDs(input []model.RuleHit) []string {
	ids := make([]string, 0, len(input))
	for _, hit := range input {
		ids = append(ids, hit.RuleID)
	}
	return ids
}

type spyRateLimiter struct {
	mu         sync.Mutex
	allowed    int
	released   int
	inFlight   int
	releaseErr error
}

func (limiter *spyRateLimiter) Allow(string, float64, int) (rules.RateLimitResult, error) {
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	limiter.allowed++
	limiter.inFlight++
	return rules.RateLimitResult{Allowed: true}, nil
}

func (limiter *spyRateLimiter) Release(string) error {
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	if limiter.inFlight == 0 {
		return errors.New("release without reservation")
	}
	limiter.inFlight--
	limiter.released++
	return limiter.releaseErr
}

func (limiter *spyRateLimiter) snapshot() (allowed, released, inFlight int) {
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	return limiter.allowed, limiter.released, limiter.inFlight
}

var (
	_ IdentityAuthenticator                  = (*fakeAuthenticator)(nil)
	_ DatasourceReader                       = (*fakeDatasourceReader)(nil)
	_ PolicyLoader                           = (*fakePolicyLoader)(nil)
	_ ExecutorProvider                       = (*fakeExecutorProvider)(nil)
	_ ApprovalWriter                         = (*fakeApprovalWriter)(nil)
	_ AuditRecorder                          = (*fakeAuditRecorder)(nil)
	_ RedactorBuilder                        = (*fakeRedactorBuilder)(nil)
	_ rawTestExecutor                        = (*spyExecutor)(nil)
	_ rawTestSession                         = (*spySession)(nil)
	_ rules.TransactionMetadataProvider      = (*spyExecutor)(nil)
	_ rules.TransactionMetadataProvider      = (*spySession)(nil)
	_ rules.MysqlTransactionMetadataProvider = (*spyExecutor)(nil)
	_ rules.MysqlTransactionMetadataProvider = (*spySession)(nil)
	_ rules.RateLimiter                      = (*spyRateLimiter)(nil)
)

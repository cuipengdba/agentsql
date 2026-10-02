package pipeline

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/config"
	"github.com/cuipengdba/agentsql/internal/engine"
	"github.com/cuipengdba/agentsql/internal/eventbus"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/rules"
	"github.com/stretchr/testify/require"
)

func TestProcessDemoNonSelectBarrierNeverTouchesBusinessDatabase(t *testing.T) {
	tests := []struct {
		name     string
		dialect  string
		sql      string
		wantHit  string
		alsoWant string
		wantStmt model.StmtType
	}{
		{name: "insert", dialect: "postgres", sql: "INSERT INTO public.orders (id) VALUES (1)", wantHit: "DEMO_NON_SELECT", wantStmt: "INSERT"},
		{name: "update without where", dialect: "postgres", sql: "UPDATE public.orders SET status = 'paid'", wantHit: "DEMO_NON_SELECT", alsoWant: "R002", wantStmt: "UPDATE"},
		{name: "delete without where", dialect: "postgres", sql: "DELETE FROM public.orders", wantHit: "DEMO_NON_SELECT", alsoWant: "R002", wantStmt: "DELETE"},
		{name: "drop table", dialect: "postgres", sql: "DROP TABLE public.orders", wantHit: "DEMO_NON_SELECT", wantStmt: "DDL"},
		{name: "alter table", dialect: "postgres", sql: "ALTER TABLE public.orders ADD COLUMN demo_flag boolean", wantHit: "DEMO_NON_SELECT", wantStmt: "DDL"},
		{name: "truncate", dialect: "postgres", sql: "TRUNCATE TABLE public.orders", wantHit: "DEMO_NON_SELECT", wantStmt: "DDL"},
		{name: "drop database without table object", dialect: "postgres", sql: "DROP DATABASE demo_copy", wantHit: "DEMO_NON_SELECT", wantStmt: "DDL"},
		{name: "drop role without table object", dialect: "postgres", sql: "DROP ROLE demo_writer", wantHit: "DEMO_NON_SELECT", wantStmt: "ADMIN"},
		{name: "set", dialect: "postgres", sql: "SET work_mem = '64MB'", wantHit: "DEMO_NON_SELECT", wantStmt: "ADMIN"},
		{name: "show", dialect: "mysql", sql: "SHOW DATABASES", wantHit: "DEMO_NON_SELECT", wantStmt: "ADMIN"},
		{name: "kill", dialect: "mysql", sql: "KILL 12", wantHit: "DEMO_NON_SELECT", wantStmt: "ADMIN"},
		{name: "multiple statements", dialect: "postgres", sql: "SELECT 1; DELETE FROM public.orders", wantHit: "DEMO_NON_SELECT", alsoWant: "R001", wantStmt: "UNKNOWN"},
		{name: "comment carries write", dialect: "postgres", sql: "/* harmless-looking */ UPDATE public.orders SET status = 'paid' WHERE id = 1", wantHit: "DEMO_NON_SELECT", alsoWant: "R006", wantStmt: "UPDATE"},
		{name: "parse error", dialect: "postgres", sql: "SELECT FROM WHERE (((", wantHit: "DEMO_PARSE", wantStmt: "UNKNOWN"},
		{name: "postgres select into", dialect: "postgres", sql: "SELECT id INTO public.orders_copy FROM public.orders", wantHit: "DEMO_NON_SELECT", wantStmt: "SELECT"},
		{name: "explain analyze", dialect: "postgres", sql: "EXPLAIN ANALYZE SELECT id FROM public.orders", wantHit: "DEMO_NON_SELECT", wantStmt: "SELECT"},
		{name: "mysql explain analyze", dialect: "mysql", sql: "EXPLAIN ANALYZE SELECT id FROM orders", wantHit: "DEMO_NON_SELECT", wantStmt: "SELECT"},
		{name: "unknown statement", dialect: "postgres", sql: "FETCH FORWARD 1 FROM demo_cursor", wantHit: "DEMO_NON_SELECT", wantStmt: "UNKNOWN"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture, request := newDemoPipelineFixture(t, test.dialect, config.DemoAgentDML, "dml")
			request.SQL = test.sql
			request.SessionID = "must-not-open"
			hub, err := eventbus.New(eventbus.Options{HistorySize: 1, SubscriberBuffer: 1})
			require.NoError(t, err)
			defer hub.Close()
			events, cancel := hub.Subscribe()
			defer cancel()
			fixture.audit.onRecorded = func(log model.AuditLog) {
				hub.Publish(eventbus.Event{Audit: log})
			}

			response, err := fixture.pipeline.ProcessDemo(context.Background(), request)

			require.NoError(t, err)
			require.Equal(t, model.DecisionDeny, response.Decision)
			require.Contains(t, ruleHitIDs(response.Assessment.Hits), test.wantHit)
			if test.alsoWant != "" {
				require.Contains(t, ruleHitIDs(response.Assessment.Hits), test.alsoWant)
			}
			require.Equal(t, test.wantStmt, response.Assessment.StmtType)
			require.Positive(t, response.AuditID)
			require.Equal(t, 1, fixture.audit.calls())
			auditLog := fixture.audit.last()
			require.Equal(t, "deny", auditLog.Decision)
			require.Equal(t, config.DemoAgentDML, requireStringPointer(t, auditLog.AgentID))
			require.Equal(t, request.DatasourceID, requireStringPointer(t, auditLog.DatasourceID))
			require.Equal(t, string(test.wantStmt), requireStringPointer(t, auditLog.StmtType))
			var auditedHits []model.RuleHit
			require.NoError(t, json.Unmarshal([]byte(requireStringPointer(t, auditLog.RuleHits)), &auditedHits))
			require.Contains(t, ruleHitIDs(auditedHits), test.wantHit)
			select {
			case event := <-events:
				require.Equal(t, response.AuditID, event.Audit.ID)
			case <-time.After(time.Second):
				t.Fatal("timed out waiting for persisted audit event")
			}
			assertDemoBusinessDatabaseUntouched(t, fixture)
		})
	}
}

func TestProcessDemoBoundedUpdateAndDeleteCreateApprovalWithoutExecution(t *testing.T) {
	for _, sql := range []string{
		"UPDATE public.orders SET status = 'paid' WHERE id = 1",
		"DELETE FROM public.orders WHERE id = 1",
	} {
		t.Run(sql, func(t *testing.T) {
			fixture, request := newDemoPipelineFixture(t, "postgres", config.DemoAgentDML, "dml")
			request.SQL = sql

			response, err := fixture.pipeline.ProcessDemo(context.Background(), request)

			require.NoError(t, err)
			require.Equal(t, model.DecisionApprove, response.Decision)
			require.Contains(t, ruleHitIDs(response.Assessment.Hits), "DEMO_WRITE_APPROVAL")
			require.NotEmpty(t, response.ApprovalID)
			require.Positive(t, response.AuditID)
			require.Zero(t, fixture.executors.calls())
			require.Equal(t, 1, fixture.approvals.calls())
			require.Equal(t, "approve", fixture.audit.last().Decision)
		})
	}
}

func TestProcessDemoSafeSelectExecutesRedactsAndAudits(t *testing.T) {
	for _, sql := range []string{
		"SELECT phone FROM public.customers WHERE id = 1 LIMIT 1",
		"SELECT 1",
	} {
		t.Run(sql, func(t *testing.T) {
			fixture, request := newDemoPipelineFixture(t, "postgres", config.DemoAgentRO, "readonly")
			request.SQL = sql

			response, err := fixture.pipeline.ProcessDemo(context.Background(), request)

			require.NoError(t, err)
			require.Equal(t, model.DecisionAllow, response.Decision)
			require.NotNil(t, response.Result)
			require.NotEmpty(t, response.Assessment.Normalized)
			require.Equal(t, model.StmtType("SELECT"), response.Assessment.StmtType)
			require.Positive(t, response.Redact.MaskedCells)
			require.Positive(t, response.AuditID)
			require.Equal(t, 1, fixture.executors.calls())
			calls := fixture.executor.callsSnapshot()
			require.Equal(t, 1, calls.explain)
			require.Equal(t, 1, calls.query)
			require.Zero(t, calls.execute+calls.beginWriteTx+calls.openSession)
			require.Equal(t, 1, fixture.audit.calls())
		})
	}
}

func TestProcessDemoScopeChecksDenyAndAuditBeforeParsingOrExecution(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*pipelineFixture, *Request)
	}{
		{
			name: "non demo agent",
			configure: func(fixture *pipelineFixture, _ *Request) {
				fixture.authenticator.agent.ID = "agent-production"
			},
		},
		{
			name: "profile level mismatch",
			configure: func(fixture *pipelineFixture, _ *Request) {
				fixture.authenticator.agent.Level = "dml"
			},
		},
		{
			name: "datasource outside allowlist",
			configure: func(fixture *pipelineFixture, request *Request) {
				fixture.datasources.datasource.ID = "ds-production"
				request.DatasourceID = "ds-production"
			},
		},
		{
			name: "demo config not installed",
			configure: func(fixture *pipelineFixture, _ *Request) {
				fixture.pipeline.demo = config.DemoConfig{}
			},
		},
		{
			name: "non query tool semantics",
			configure: func(_ *pipelineFixture, request *Request) {
				request.MCPTool = "execute_write"
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture, request := newDemoPipelineFixture(t, "postgres", config.DemoAgentRO, "readonly")
			test.configure(fixture, &request)

			response, err := fixture.pipeline.ProcessDemo(context.Background(), request)

			require.NoError(t, err)
			require.Equal(t, model.DecisionDeny, response.Decision)
			require.Contains(t, ruleHitIDs(response.Assessment.Hits), "DEMO_SCOPE")
			require.Positive(t, response.AuditID)
			require.Equal(t, "deny", fixture.audit.last().Decision)
			assertDemoBusinessDatabaseUntouched(t, fixture)
		})
	}
}

func TestProcessDemoForcesR008AtConfiguredQPS(t *testing.T) {
	disabled := false
	fixture, request := newDemoPipelineFixture(
		t,
		"postgres",
		config.DemoAgentRO,
		"readonly",
		WithRuleLayers(engine.RuleLayers{Agent: engine.RuleLayer{
			"R008": {Enabled: &disabled},
		}}),
	)
	clock := demoFixedClock{now: time.Unix(1_700_000_000, 0)}
	limiter, err := rules.NewTokenBucketLimiter(clock)
	require.NoError(t, err)
	fixture.pipeline.limiter = limiter

	for attempt := 1; attempt <= 3; attempt++ {
		response, processErr := fixture.pipeline.ProcessDemo(context.Background(), request)
		require.NoError(t, processErr)
		if attempt < 3 {
			require.Equal(t, model.DecisionAllow, response.Decision)
			continue
		}
		require.Equal(t, model.DecisionDeny, response.Decision)
		require.Contains(t, ruleHitIDs(response.Assessment.Hits), "R008")
		require.Positive(t, response.AuditID)
	}
	require.Equal(t, 2, fixture.executors.calls(), "the rate-limited third request must stop before GetOrOpen")
	require.Equal(t, 3, fixture.audit.calls())
	require.Equal(t, "deny", fixture.audit.last().Decision)
}

func TestProcessDemoAddsDynamicR005WithDatasourceRowLimitOnly(t *testing.T) {
	const sql = "SELECT id, status, amount FROM public.orders WHERE status = 'paid'"
	demoFixture, demoRequest := newDemoPipelineFixture(t, "postgres", config.DemoAgentRO, "readonly")
	demoRequest.SQL = sql
	demoFixture.executor.explain = model.ExplainInfo{EstScanRows: 21, SeqScan: true}
	demoFixture.executor.queryResult = model.QueryResult{
		Columns:   []string{"id", "status", "amount"},
		Rows:      [][]string{{"1", "paid", "10.00"}},
		RowCount:  20,
		Truncated: true,
	}

	demoResponse, err := demoFixture.pipeline.ProcessDemo(context.Background(), demoRequest)

	require.NoError(t, err)
	require.Equal(t, model.DecisionWarn, demoResponse.Decision)
	require.Contains(t, ruleHitIDs(demoResponse.Assessment.Hits), "R005")
	require.Equal(t, int64(21), demoResponse.Assessment.EstScanRows)
	require.NotNil(t, demoResponse.Result)
	require.True(t, demoResponse.Result.Truncated)
	limit, _, _ := demoFixture.executor.executionSettings()
	require.Equal(t, 20, limit)

	production := newPipelineFixture(t)
	production.datasources.datasource.RowLimit = 20
	production.executor.explain = model.ExplainInfo{EstScanRows: 21, SeqScan: true}
	productionResponse, err := production.pipeline.Process(context.Background(), requestWithSQL(sql))
	require.NoError(t, err)
	require.Equal(t, model.DecisionWarn, productionResponse.Decision)
	require.Contains(t, ruleHitIDs(productionResponse.Assessment.Hits), "R005")
}

func TestDemoSemanticReadOnlyFailsClosedForUnsafeASTShapes(t *testing.T) {
	tests := []struct {
		name string
		ast  *model.AST
	}{
		{name: "nil", ast: nil},
		{name: "unknown", ast: &model.AST{StmtType: "UNKNOWN"}},
		{name: "multi select", ast: &model.AST{StmtType: "SELECT", IsMulti: true, Operations: []string{"SELECT"}}},
		{name: "select into", ast: &model.AST{StmtType: "SELECT", Operations: []string{"SELECT", "SELECT INTO"}}},
		{name: "explain analyze", ast: &model.AST{StmtType: "SELECT", Operations: []string{"EXPLAIN ANALYZE", "SELECT"}}},
		{name: "future operation", ast: &model.AST{StmtType: "SELECT", Operations: []string{"SELECT", "FUTURE SIDE EFFECT"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.False(t, isDemoSemanticReadOnly(test.ast))
		})
	}
	require.True(t, isDemoSemanticReadOnly(&model.AST{
		StmtType:   "SELECT",
		Operations: []string{"SELECT", "NESTING_DEPTH:0", "UNION_COUNT:0", "SELECT_COLUMN:id"},
	}))
}

func newDemoPipelineFixture(
	t *testing.T,
	dialect string,
	agentID string,
	level string,
	options ...Option,
) (*pipelineFixture, Request) {
	t.Helper()
	demo := config.DemoConfig{
		Enabled:              true,
		AllowedDatasourceIDs: []string{config.DemoDatasourcePG, config.DemoDatasourceMySQL},
		QPSPerAgent:          2,
	}
	options = append(options, WithDemoConfig(demo))
	fixture := newPipelineFixture(t, options...)
	datasourceID := config.DemoDatasourcePG
	if dialect == "mysql" {
		datasourceID = config.DemoDatasourceMySQL
	}
	fixture.authenticator.agent = model.Agent{ID: agentID, Status: "active", Level: level}
	fixture.datasources.datasource = model.Datasource{
		ID: datasourceID, DBType: dialect, RowLimit: 20, StmtTimeoutMS: 5_000,
	}
	fixture.policies.policies = []model.Policy{{
		ID:           "demo-policy",
		AgentID:      agentID,
		DatasourceID: datasourceID,
		ObjectType:   "table",
		ObjectName:   "*",
		Action:       "allow",
	}}
	fixture.executor.dialect = dialect
	return fixture, Request{
		APIKey:       "server-mapped-demo-key",
		DatasourceID: datasourceID,
		SQL:          "SELECT phone FROM public.customers WHERE id = 1 LIMIT 1",
		MCPTool:      "query",
	}
}

func assertDemoBusinessDatabaseUntouched(t *testing.T, fixture *pipelineFixture) {
	t.Helper()
	require.Zero(t, fixture.executors.calls(), "GetOrOpen must not be called")
	calls := fixture.executor.callsSnapshot()
	require.Zero(t, calls.openSession, "OpenSession must not be called")
	require.Zero(t, calls.explain+calls.sessionExplain, "Explain must not be called")
	require.Zero(t, calls.query+calls.sessionQuery, "Query must not be called")
	require.Zero(t, calls.execute+calls.sessionExecute, "Execute must not be called")
	require.Zero(t, calls.beginWriteTx, "BeginWriteTx must not be called")
	require.NotContains(t, fixture.executor.eventSnapshot(), "begin")
}

func requireStringPointer(t *testing.T, value *string) string {
	t.Helper()
	require.NotNil(t, value)
	return *value
}

type demoFixedClock struct {
	now time.Time
}

func (clock demoFixedClock) Now() time.Time {
	return clock.now
}

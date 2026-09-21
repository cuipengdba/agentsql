package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/executor"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestClassifyExecutionBarrierFailClosed(t *testing.T) {
	tests := []struct {
		name string
		ast  *model.AST
		want executionBarrier
	}{
		{name: "nil", want: barrierDeny},
		{name: "select", ast: &model.AST{StmtType: "SELECT"}, want: barrierRead},
		{name: "insert", ast: &model.AST{StmtType: "INSERT"}, want: barrierTransactionalWrite},
		{name: "explain analyze update", ast: &model.AST{StmtType: "UPDATE", Operations: []string{"EXPLAIN ANALYZE", "UPDATE"}}, want: barrierTransactionalWrite},
		{name: "ddl", ast: &model.AST{StmtType: "DDL"}, want: barrierPreIntent},
		{name: "admin", ast: &model.AST{StmtType: "ADMIN", Operations: []string{"VACUUM"}}, want: barrierPreIntent},
		{name: "begin", ast: &model.AST{StmtType: "ADMIN", Operations: []string{"BEGIN"}}, want: barrierDeny},
		{name: "set transaction", ast: &model.AST{StmtType: "ADMIN", RawSQL: "SET LOCAL TRANSACTION READ ONLY"}, want: barrierDeny},
		{name: "mysql autocommit", ast: &model.AST{StmtType: "ADMIN", RawSQL: "SET @@session.autocommit = 0"}, want: barrierDeny},
		{name: "unknown", ast: &model.AST{StmtType: "UNKNOWN"}, want: barrierDeny},
		{name: "unsupported", ast: &model.AST{StmtType: "MERGE"}, want: barrierDeny},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.want, classifyExecutionBarrier(test.ast))
		})
	}
}

func TestAuditCallUsesDatasourceTimeout(t *testing.T) {
	fixture := newPipelineFixture(t)
	fixture.datasources.datasource.StmtTimeoutMS = 10
	fixture.audit.waitForContextEnd = true
	started := time.Now()
	response, err := fixture.pipeline.Process(context.Background(), defaultRequest())
	require.ErrorIs(t, err, ErrAuditUnavailable)
	require.Nil(t, response.Result)
	require.Less(t, time.Since(started), time.Second)
}

func TestTransactionalWriteAuditBarrier(t *testing.T) {
	t.Run("commit follows audit", func(t *testing.T) {
		fixture := newPipelineFixture(t)
		fixture.audit.onRecord = func() { fixture.executor.appendEvent("audit") }
		response, err := fixture.pipeline.Process(context.Background(), requestWithSQL(
			"UPDATE public.orders SET total = 1 WHERE id = 1",
		))
		require.NoError(t, err)
		require.NotNil(t, response.Result)
		require.Equal(t, []string{"begin", "execute", "audit", "commit"}, fixture.executor.eventSnapshot())
	})

	t.Run("audit failure rolls back", func(t *testing.T) {
		fixture := newPipelineFixture(t)
		fixture.audit.err = errors.New("postgres://user:password@audit:5432 unavailable")
		fixture.audit.onRecord = func() { fixture.executor.appendEvent("audit") }
		response, err := fixture.pipeline.Process(context.Background(), requestWithSQL(
			"UPDATE public.orders SET total = 1 WHERE id = 1",
		))
		require.ErrorIs(t, err, ErrAuditUnavailable)
		require.Nil(t, response.Result)
		require.Equal(t, []string{"begin", "execute", "audit", "rollback"}, fixture.executor.eventSnapshot())
		require.NotContains(t, err.Error(), "password")
	})

	t.Run("commit failure preserves audit and hides result", func(t *testing.T) {
		fixture := newPipelineFixture(t)
		fixture.executor.commitErr = errors.New("commit transport secret")
		fixture.audit.onRecord = func() { fixture.executor.appendEvent("audit") }
		response, err := fixture.pipeline.Process(context.Background(), requestWithSQL(
			"UPDATE public.orders SET total = 1 WHERE id = 1",
		))
		require.ErrorIs(t, err, ErrBusinessCommitUncertain)
		require.Nil(t, response.Result)
		require.Positive(t, response.AuditID)
		require.Equal(t, 2, fixture.audit.calls())
		require.Equal(t, []string{"begin", "execute", "audit", "commit", "audit"}, fixture.executor.eventSnapshot())
		logs := fixture.audit.snapshot()
		require.Equal(t, "allow", logs[0].Decision)
		require.Equal(t, "error", logs[1].Decision)
		require.Equal(t, string(executor.DBErrorCodeCommitOutcomeUnknown), requireStringPointer(t, logs[1].ErrorCode))
		require.Equal(t, "数据库提交结果未知，请勿自动重试", requireStringPointer(t, logs[1].ErrorMsg))
		require.Nil(t, logs[1].RowsReturned)
		assertAuditPhase(t, logs[1], "outcome", logs[0].ID)
		require.NotContains(t, err.Error(), "transport secret")
	})

	t.Run("commit failure and outcome audit failure preserve do-not-retry contract", func(t *testing.T) {
		fixture := newPipelineFixture(t)
		fixture.executor.commitErr = errors.New("commit transport secret")
		fixture.audit.err = errors.New("outcome audit secret")
		fixture.audit.errAtAttempt = 2

		response, err := fixture.pipeline.Process(context.Background(), requestWithSQL(
			"UPDATE public.orders SET total = 1 WHERE id = 1",
		))
		require.ErrorIs(t, err, ErrBusinessCommitUncertain)
		require.ErrorIs(t, err, ErrAuditUnavailable)
		require.Equal(t, string(executor.DBErrorCodeCommitOutcomeUnknown), response.ErrorCode)
		require.Equal(t, string(executor.DBStageCommit), response.ErrorStage)
		require.Contains(t, response.Suggestion, "请勿自动重试")
		require.Nil(t, response.Result)
		require.Equal(t, 1, fixture.audit.calls())
		require.Equal(t, 2, fixture.audit.attempts)
		require.NotContains(t, err.Error(), "secret")
	})
}

func TestDDLIntentPrecedesBusinessExecution(t *testing.T) {
	fixture := newPipelineFixture(t)
	fixture.authenticator.agent.Level = "ddl"
	fixture.policies.policies = append(fixture.policies.policies, model.Policy{
		ID: "ddl-table", AgentID: "agent-1", DatasourceID: "datasource-1",
		ObjectType: "table", ObjectName: "public.orders_archive", Action: "allow",
	})
	fixture.audit.onRecord = func() { fixture.executor.appendEvent("audit") }
	conversationID := "ddl-conversation"
	request := requestWithSQL("CREATE TABLE public.orders_archive(id bigint)")
	request.SessionID = "ddl-session"
	request.ConversationID = &conversationID
	response, err := fixture.pipeline.Process(context.Background(), request)
	require.NoError(t, err)
	require.Equal(t, []string{"audit", "execute", "audit"}, fixture.executor.eventSnapshot())
	require.Positive(t, response.AuditID)
	logs := fixture.audit.snapshot()
	require.Len(t, logs, 2)
	require.Equal(t, "allow", logs[0].Decision)
	require.Equal(t, "allow", logs[1].Decision)
	require.Nil(t, logs[0].RowsReturned)
	require.NotNil(t, logs[1].RowsReturned)
	require.Equal(t, "ddl-session", requireStringPointer(t, logs[0].SessionID))
	require.Equal(t, "ddl-session", requireStringPointer(t, logs[1].SessionID))
	require.Equal(t, conversationID, requireStringPointer(t, logs[0].ConversationID))
	require.Equal(t, conversationID, requireStringPointer(t, logs[1].ConversationID))
	assertAuditPhase(t, logs[0], "intent", 0)
	assertAuditPhase(t, logs[1], "outcome", logs[0].ID)
	require.Equal(t, logs[1].ID, response.AuditID)

	failed := newPipelineFixture(t)
	failed.authenticator.agent.Level = "ddl"
	failed.policies.policies = append(failed.policies.policies, model.Policy{
		ID: "ddl-table", AgentID: "agent-1", DatasourceID: "datasource-1",
		ObjectType: "table", ObjectName: "public.orders_archive", Action: "allow",
	})
	failed.audit.err = errors.New("audit database secret")
	failed.audit.onRecord = func() { failed.executor.appendEvent("audit") }
	response, err = failed.pipeline.Process(context.Background(), requestWithSQL(
		"CREATE TABLE public.orders_archive(id bigint)",
	))
	require.ErrorIs(t, err, ErrAuditUnavailable)
	require.Nil(t, response.Result)
	require.Equal(t, []string{"audit"}, failed.executor.eventSnapshot())

	executionFailed := newPipelineFixture(t)
	executionFailed.authenticator.agent.Level = "ddl"
	executionFailed.policies.policies = append(executionFailed.policies.policies, model.Policy{
		ID: "ddl-table", AgentID: "agent-1", DatasourceID: "datasource-1",
		ObjectType: "table", ObjectName: "public.orders_archive", Action: "allow",
	})
	databaseError := &executor.DBError{
		Kind: executor.DBErrorKindObjectNotFound, Code: executor.DBErrorCodeObjectNotFound,
		Stage: executor.DBStageExecute, DriverCode: "42P01",
	}
	executionFailed.executor.executeErr = databaseError
	executionFailed.audit.onRecord = func() { executionFailed.executor.appendEvent("audit") }
	response, err = executionFailed.pipeline.Process(context.Background(), requestWithSQL(
		"CREATE TABLE public.orders_archive(id bigint)",
	))
	require.NoError(t, err)
	require.Nil(t, response.Result)
	require.Positive(t, response.AuditID)
	require.Equal(t, model.DecisionError, response.Decision)
	require.Equal(t, string(executor.DBErrorCodeObjectNotFound), response.ErrorCode)
	require.Equal(t, string(executor.DBStageExecute), response.ErrorStage)
	require.Equal(t, 2, executionFailed.audit.calls())
	require.Equal(t, []string{"audit", "execute", "audit"}, executionFailed.executor.eventSnapshot())
	failedLogs := executionFailed.audit.snapshot()
	require.Equal(t, "allow", failedLogs[0].Decision)
	require.Equal(t, "error", failedLogs[1].Decision)
	require.Equal(t, string(executor.DBErrorCodeObjectNotFound), requireStringPointer(t, failedLogs[1].ErrorCode))
	require.Equal(t, databaseError.Error(), requireStringPointer(t, failedLogs[1].ErrorMsg))
	require.Nil(t, failedLogs[1].RowsReturned)
	assertAuditPhase(t, failedLogs[0], "intent", 0)
	assertAuditPhase(t, failedLogs[1], "outcome", failedLogs[0].ID)
}

func TestDDLOutcomeAuditFailureIsFailClosedAndNoThirdAuditIsPossible(t *testing.T) {
	fixture := newPipelineFixture(t)
	fixture.authenticator.agent.Level = "ddl"
	fixture.policies.policies = append(fixture.policies.policies, model.Policy{
		ID: "ddl-table", AgentID: "agent-1", DatasourceID: "datasource-1",
		ObjectType: "table", ObjectName: "public.orders_archive", Action: "allow",
	})
	fixture.audit.err = errors.New("outcome audit unavailable")
	fixture.audit.errAtAttempt = 2

	response, err := fixture.pipeline.Process(context.Background(), requestWithSQL(
		"CREATE TABLE public.orders_archive(id bigint)",
	))
	require.ErrorIs(t, err, ErrAuditUnavailable)
	require.Nil(t, response.Result)
	require.Equal(t, string(executor.DBErrorCodeAuditUnavailable), response.ErrorCode)
	require.Equal(t, StageAudit, response.ErrorStage)
	require.Equal(t, 1, fixture.audit.calls(), "only the persisted intent remains")
	require.Equal(t, 2, fixture.audit.attempts)

	run := newPipelineRun(fixture.pipeline, defaultRequest(), runModeProduction)
	run.agent = &fixture.authenticator.agent
	run.datasource = &fixture.datasources.datasource
	fixture.audit.err = nil
	fixture.audit.errAtAttempt = 0
	require.NoError(t, run.audit(context.Background(), "allow", nil, auditPhaseIntent))
	require.NoError(t, run.audit(context.Background(), "allow", nil, auditPhaseOutcome))
	require.Error(t, run.audit(context.Background(), "allow", nil, auditPhaseOutcome))
}

func assertAuditPhase(t *testing.T, log model.AuditLog, phase string, relatedAuditID int64) {
	t.Helper()
	require.NotNil(t, log.DetailsJSON)
	var details struct {
		AuditPhase     string `json:"audit_phase"`
		RelatedAuditID int64  `json:"related_audit_id"`
	}
	require.NoError(t, json.Unmarshal([]byte(*log.DetailsJSON), &details))
	require.Equal(t, phase, details.AuditPhase)
	require.Equal(t, relatedAuditID, details.RelatedAuditID)
}

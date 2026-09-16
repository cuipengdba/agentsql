package pipeline

import (
	"context"
	"errors"
	"testing"
	"time"

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
		require.Equal(t, 1, fixture.audit.calls())
		require.Equal(t, []string{"begin", "execute", "audit", "commit"}, fixture.executor.eventSnapshot())
		require.NotContains(t, err.Error(), "transport secret")
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
	response, err := fixture.pipeline.Process(context.Background(), requestWithSQL(
		"CREATE TABLE public.orders_archive(id bigint)",
	))
	require.NoError(t, err)
	require.Equal(t, []string{"audit", "execute"}, fixture.executor.eventSnapshot())
	require.Positive(t, response.AuditID)
	require.Nil(t, fixture.audit.last().RowsReturned)

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
	executionFailed.executor.executeErr = errors.New("business DDL failed")
	executionFailed.audit.onRecord = func() { executionFailed.executor.appendEvent("audit") }
	response, err = executionFailed.pipeline.Process(context.Background(), requestWithSQL(
		"CREATE TABLE public.orders_archive(id bigint)",
	))
	require.ErrorContains(t, err, "business DDL failed")
	require.Nil(t, response.Result)
	require.Positive(t, response.AuditID)
	require.Equal(t, 1, executionFailed.audit.calls())
	require.Equal(t, []string{"audit", "execute"}, executionFailed.executor.eventSnapshot())
}

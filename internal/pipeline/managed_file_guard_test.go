package pipeline

import (
	"context"
	"errors"
	"testing"

	executor "github.com/cuipengdba/agentsql/internal/authorizedexecute"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/parser"
	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/stretchr/testify/require"
)

type managedFileAccessProbe struct {
	allowed bool
	err     error
	calls   int
}

func (probe *managedFileAccessProbe) PublishedTableOwner(_ context.Context, binding, datasource, agent, owner, table string) (bool, error) {
	probe.calls++
	if binding != "binding" || datasource != "managed-read" || agent != "agent" || owner != "human" || table != "agentsql_file_11111111111111111111111111111111" {
		return false, errors.New("wrong owner scope")
	}
	return probe.allowed, probe.err
}

func TestManagedFileSQLRequiresPublishedOwnerAndDelegation(t *testing.T) {
	probe := &managedFileAccessProbe{allowed: true}
	ref := model.ObjectRef{Schema: "agentsql_files", Table: "agentsql_file_11111111111111111111111111111111"}
	sql := "SELECT * FROM agentsql_files.agentsql_file_11111111111111111111111111111111"
	pgParser, err := parser.NewParser("postgres")
	require.NoError(t, err)
	parsed, err := pgParser.Parse(sql)
	require.NoError(t, err)
	require.Equal(t, []model.ObjectRef{ref}, parsed.Tables)
	run := &pipelineRun{pipeline: &Pipeline{managedFile: probe}, agent: &model.Agent{ID: "agent", TenantID: store.DefaultTenantID},
		datasource: &model.Datasource{ID: "managed-read", DBType: "postgres"},
		request: Request{SQL: sql,
			DelegatedUserID: "human", DelegatedBindingID: "binding", DelegatedTenantID: store.DefaultTenantID},
		ast: parsed}
	require.NoError(t, run.checkManagedFileOwnership(context.Background()))
	require.Equal(t, 1, probe.calls)
	for _, mutate := range []func(){
		func() { run.request.DelegatedUserID = "" },
		func() { run.request.DelegatedBindingID = "" },
		func() { run.request.DelegatedTenantID = "another-tenant" },
		func() { probe.allowed = false },
		func() { probe.err = errors.New("control store unavailable") },
		func() { run.ast.Tables[0].Schema = "" },
	} {
		run.request.DelegatedUserID = "human"
		run.request.DelegatedBindingID = "binding"
		run.request.DelegatedTenantID = store.DefaultTenantID
		run.ast.Tables[0] = ref
		probe.allowed, probe.err = true, nil
		mutate()
		var denied *executor.AuthError
		require.ErrorAs(t, run.checkManagedFileOwnership(context.Background()), &denied)
	}
	run.ast.Tables = nil
	run.request.SQL = "SELECT * FROM agentsql_files.agentsql_file_11111111111111111111111111111111"
	var denied *executor.AuthError
	require.ErrorAs(t, run.checkManagedFileOwnership(context.Background()), &denied, "missing parser reference fails closed")
	run.request.SQL = "SELECT 1"
	require.NoError(t, run.checkManagedFileOwnership(context.Background()))
}

func TestManagedFileSQLDeniedAndAuditedBeforeBusinessExecutor(t *testing.T) {
	fixture := newPipelineFixture(t)
	request := requestWithSQL("SELECT amount FROM agentsql_files.agentsql_file_11111111111111111111111111111111 LIMIT 1")
	response, err := fixture.pipeline.Process(context.Background(), request)
	require.NoError(t, err)
	require.Equal(t, model.DecisionDeny, response.Decision)
	require.Zero(t, fixture.executors.calls())
	require.Equal(t, 1, fixture.audit.calls())
	require.Equal(t, "deny", fixture.audit.last().Decision)
}

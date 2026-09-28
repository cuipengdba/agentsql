package pipeline

import (
	"context"
	"testing"

	executor "github.com/cuipengdba/agentsql/internal/authorizedexecute"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

type configuredMySQLB2Controller struct{ beginCalls int }

func (controller *configuredMySQLB2Controller) Begin(context.Context, model.Agent, model.Datasource) (ColumnAuthorizationSnapshot, error) {
	controller.beginCalls++
	return nil, &executor.AuthError{Reason: executor.ReasonColumnAuthUnsupported}
}

func (*configuredMySQLB2Controller) ColumnAuthorizationEnabled(model.Datasource) bool { return false }
func (*configuredMySQLB2Controller) ColumnAuthorizationConfigured() bool              { return true }

func TestMySQLB2RoutingRejectsOnlyColumnPolicies(t *testing.T) {
	t.Run("ordinary table policy keeps table-level protection", func(t *testing.T) {
		controller := &configuredMySQLB2Controller{}
		fixture := newPipelineFixture(t, WithColumnAuthorization(controller))
		fixture.datasources.datasource.DBType = "mysql"
		fixture.executor.dialect = "mysql"

		response, err := fixture.pipeline.Process(context.Background(), defaultRequest())

		require.NoError(t, err)
		require.NotEqual(t, string(executor.ReasonColumnAuthUnsupported), response.ErrorCode)
		require.Zero(t, controller.beginCalls)
		require.NotZero(t, fixture.executors.calls())
	})

	t.Run("column policy returns stable unsupported response", func(t *testing.T) {
		controller := &configuredMySQLB2Controller{}
		fixture := newPipelineFixture(t, WithColumnAuthorization(controller))
		fixture.datasources.datasource.DBType = "mysql"
		fixture.executor.dialect = "mysql"
		columns := "phone"
		fixture.policies.policies = append(fixture.policies.policies, model.Policy{
			ID: "mysql-columns", AgentID: "agent-1", DatasourceID: "datasource-1",
			ObjectType: "column", ObjectName: "public.customers", Columns: &columns, Action: "allow",
		})

		response, err := fixture.pipeline.Process(context.Background(), defaultRequest())

		require.Error(t, err)
		require.Equal(t, string(executor.ReasonColumnAuthUnsupported), response.ErrorCode)
		require.Equal(t, "MySQL 不支持 B2 列级授权", response.ErrorMessage)
		require.Contains(t, response.Suggestion, "表级保护")
		require.Zero(t, controller.beginCalls)
		require.Zero(t, fixture.executors.calls())
	})
}

func TestB2FailurePresentationIsActionableAndStable(t *testing.T) {
	code, message, suggestion := internalFailurePresentation(&executor.AuthError{Reason: executor.ReasonBinderModeRequired})
	require.Equal(t, executor.DBErrorCode(executor.ReasonBinderModeRequired), code)
	require.Contains(t, message, "无法安全证明")
	require.Contains(t, suggestion, "NATIVE_C_V1")

	code, message, suggestion = internalFailurePresentation(&executor.AuthError{Reason: executor.ReasonColumnAuthUnsupported})
	require.Equal(t, executor.DBErrorCode(executor.ReasonColumnAuthUnsupported), code)
	require.Contains(t, message, "MySQL")
	require.Contains(t, suggestion, "表级保护")
}

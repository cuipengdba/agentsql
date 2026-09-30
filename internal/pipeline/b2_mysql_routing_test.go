package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"

	executor "github.com/cuipengdba/agentsql/internal/authorizedexecute"
	"github.com/cuipengdba/agentsql/internal/mask"
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

type enabledB2Controller struct {
	beginCalls int
	snapshot   ColumnAuthorizationSnapshot
}

func (controller *enabledB2Controller) Begin(context.Context, model.Agent, model.Datasource) (ColumnAuthorizationSnapshot, error) {
	controller.beginCalls++
	return controller.snapshot, nil
}

func (*enabledB2Controller) ColumnAuthorizationEnabled(model.Datasource) bool { return true }

type routingB2Snapshot struct {
	redactor mask.Redactor
}

func (*routingB2Snapshot) Policies() []model.Policy         { return nil }
func (snapshot *routingB2Snapshot) Redactor() mask.Redactor { return snapshot.redactor }
func (*routingB2Snapshot) RevisionDigest() string           { return "test-revision" }
func (*routingB2Snapshot) FinalCheck(context.Context) error { return nil }
func (*routingB2Snapshot) Close() error                     { return nil }

type routingB2ExecutorProvider struct {
	delegate rawTestExecutor
	calls    int
}

func (provider *routingB2ExecutorProvider) AuthorizedExecute(
	_ context.Context,
	_ model.Datasource,
	_ []byte,
	sqlText string,
	_ string,
) (executor.Statement, error) {
	provider.calls++
	if strings.Contains(sqlText, "no_such_table") {
		return nil, testDBError(
			executor.DBErrorKindObjectNotFound,
			executor.DBErrorCodeObjectNotFound,
			executor.DBStageExplain,
			"42P01",
		)
	}
	return &routingDeniedColumnStatement{
		Statement: &testBoundStatement{delegate: provider.delegate, sql: sqlText},
		outcome: executor.AuthorizedSelectResult{
			Allowed: false,
			Reason:  executor.ReasonAgentDenied,
		},
	}, nil
}

type routingDeniedColumnStatement struct {
	executor.Statement
	outcome  executor.AuthorizedSelectResult
	executed bool
}

func (statement *routingDeniedColumnStatement) Execute(context.Context) (model.QueryResult, error) {
	statement.executed = true
	return model.QueryResult{}, nil
}

func (statement *routingDeniedColumnStatement) ColumnAuthorizationResult() (executor.AuthorizedSelectResult, bool) {
	return statement.outcome, statement.executed
}

type functionQualificationError struct{}

func (functionQualificationError) Error() string { return "AUTH_FUNCTION_QUALIFICATION_REQUIRED" }
func (functionQualificationError) AuthorizationReason() string {
	return "AUTH_FUNCTION_QUALIFICATION_REQUIRED"
}

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

		require.NoError(t, err)
		require.Equal(t, model.DecisionDeny, response.Decision)
		require.Equal(t, string(executor.ReasonColumnAuthUnsupported), response.ErrorCode)
		require.Equal(t, "MySQL 列级授权在 v0.4 中暂不支持", response.ErrorMessage)
		require.Contains(t, response.Suggestion, "表级保护")
		require.Zero(t, controller.beginCalls)
		require.Zero(t, fixture.executors.calls())
	})
}

func TestPostgresB2BinderDistinguishesMissingAndUnauthorizedObjects(t *testing.T) {
	redactor, err := mask.NewRedactor(nil)
	require.NoError(t, err)

	for _, test := range []struct {
		name             string
		sql              string
		decision         model.Decision
		errorCode        string
		reasonContains   string
		reasonNotContain string
		errorMessage     string
		suggestion       string
		auditDecision    string
	}{
		{
			name:             "missing table is a database object error",
			sql:              "SELECT id FROM public.no_such_table",
			decision:         model.DecisionError,
			errorCode:        string(executor.DBErrorCodeObjectNotFound),
			reasonContains:   "不存在",
			reasonNotContain: "不在 Agent 的允许范围内",
			errorMessage:     "表或对象不存在",
			suggestion:       "请检查对象名称和当前数据库",
			auditDecision:    "error",
		},
		{
			name:           "existing unauthorized table keeps R010 denial",
			sql:            "SELECT id, note FROM public.internal_notes",
			decision:       model.DecisionDeny,
			errorCode:      string(executor.ReasonAgentDenied),
			reasonContains: "不在 Agent 的允许范围内",
			auditDecision:  "deny",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			controller := &enabledB2Controller{snapshot: &routingB2Snapshot{redactor: redactor}}
			fixture := newPipelineFixture(t, WithColumnAuthorization(controller))
			provider := &routingB2ExecutorProvider{delegate: fixture.executor}
			fixture.pipeline.ports.Executors = provider

			response, processErr := fixture.pipeline.Process(
				context.Background(),
				requestWithSQL(test.sql),
			)

			require.NoError(t, processErr)
			require.Equal(t, test.decision, response.Decision)
			require.Equal(t, test.errorCode, response.ErrorCode)
			require.Equal(t, test.errorMessage, response.ErrorMessage)
			require.Equal(t, test.suggestion, response.Suggestion)
			require.Contains(t, ruleHitIDs(response.Assessment.Hits), "R010")
			require.Contains(t, response.Assessment.Reason, test.reasonContains)
			if test.reasonNotContain != "" {
				require.NotContains(t, response.Assessment.Reason, test.reasonNotContain)
			}
			require.Equal(t, 1, controller.beginCalls)
			require.Equal(t, 1, provider.calls)
			require.Equal(t, test.auditDecision, fixture.audit.last().Decision)
		})
	}
}

func TestDeniedSelectBindingExceptionOnlyAppliesToR010(t *testing.T) {
	selectAST := &model.AST{StmtType: model.StmtType("SELECT")}
	r010 := model.RuleHit{RuleID: "R010", Decision: model.DecisionDeny}
	otherDeny := model.RuleHit{RuleID: "R001", Decision: model.DecisionDeny}

	require.True(t, shouldBindDeniedSelect(selectAST, model.Assessment{
		Decision: model.DecisionDeny,
		Hits:     []model.RuleHit{r010},
	}))
	require.False(t, shouldBindDeniedSelect(selectAST, model.Assessment{
		Decision: model.DecisionDeny,
		Hits:     []model.RuleHit{r010, otherDeny},
	}))
	require.False(t, shouldBindDeniedSelect(
		&model.AST{StmtType: model.StmtType("UPDATE")},
		model.Assessment{Decision: model.DecisionDeny, Hits: []model.RuleHit{r010}},
	))
	require.False(t, shouldBindDeniedSelect(selectAST, model.Assessment{
		Decision: model.DecisionDeny,
		Hits:     []model.RuleHit{otherDeny},
	}))
}

func TestFunctionQualificationFailureIsAuditedAuthorizationDeny(t *testing.T) {
	fixture := newPipelineFixture(t)
	fixture.executors.err = functionQualificationError{}

	response, err := fixture.pipeline.Process(context.Background(), defaultRequest())

	require.NoError(t, err)
	require.Equal(t, model.DecisionDeny, response.Decision)
	require.Equal(t, string(executor.ReasonFunctionQualification), response.ErrorCode)
	require.Contains(t, response.ErrorMessage, "pg_catalog")
	require.Contains(t, response.Suggestion, "pg_catalog.count")
	require.Equal(t, "deny", fixture.audit.last().Decision)
	require.Nil(t, response.Result)
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

	code, message, suggestion = internalFailurePresentation(&executor.AuthError{Reason: executor.ReasonFunctionQualification})
	require.Equal(t, executor.DBErrorCode(executor.ReasonFunctionQualification), code)
	require.Contains(t, message, "pg_catalog")
	require.Contains(t, suggestion, "pg_catalog.count")
}

func TestExpectedAuthorizationFailureExcludesInternalFaults(t *testing.T) {
	for _, reason := range []executor.Reason{
		executor.ReasonColumnAuthUnsupported,
		executor.ReasonFunctionQualification,
		executor.ReasonRelationShape,
		executor.ReasonColumnGrantMissing,
	} {
		require.True(t, expectedAuthorizationFailure(&executor.AuthError{Reason: reason}), reason)
	}

	for _, reason := range []executor.Reason{
		executor.ReasonDatabaseFailure,
		executor.ReasonMaskCapabilityMissing,
		executor.ReasonAuthorizationProofInvalid,
		executor.ReasonAuditUnavailable,
		executor.ReasonFinalFenceFailed,
		executor.ReasonDeliverySealFailed,
	} {
		require.False(t, expectedAuthorizationFailure(&executor.AuthError{Reason: reason}), reason)
	}
	require.False(t, expectedAuthorizationFailure(errors.New("unexpected internal failure")))
}

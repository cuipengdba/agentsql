package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	executor "github.com/cuipengdba/agentsql/internal/authorizedexecute"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/rules"
	"github.com/stretchr/testify/require"
)

func TestPipelineClassifiedDatabaseErrorsBecomeBusinessResponses(t *testing.T) {
	readRequest := defaultRequest()
	sessionRequest := defaultRequest()
	sessionRequest.SessionID = "classified-error-session"
	writeRequest := requestWithSQL("UPDATE public.orders SET total = 1 WHERE id = 1")

	tests := []struct {
		name      string
		request   Request
		database  *executor.DBError
		configure func(*pipelineFixture, *executor.DBError)
	}{
		{
			name: "get or open authentication failure", request: readRequest,
			database: testDBError(executor.DBErrorKindAuthentication, executor.DBErrorCodeAuthentication, executor.DBStageConnect, "28P01"),
			configure: func(fixture *pipelineFixture, databaseError *executor.DBError) {
				fixture.executors.err = databaseError
			},
		},
		{
			name: "get or open database missing", request: readRequest,
			database: testDBError(executor.DBErrorKindDatabaseNotFound, executor.DBErrorCodeDatabaseNotFound, executor.DBStagePing, "3D000"),
			configure: func(fixture *pipelineFixture, databaseError *executor.DBError) {
				fixture.executors.err = databaseError
			},
		},
		{
			name: "get or open unreachable", request: readRequest,
			database: testDBError(executor.DBErrorKindConnection, executor.DBErrorCodeConnection, executor.DBStageConnect, "08006"),
			configure: func(fixture *pipelineFixture, databaseError *executor.DBError) {
				fixture.executors.err = databaseError
			},
		},
		{
			name: "open session", request: sessionRequest,
			database: testDBError(executor.DBErrorKindConnection, executor.DBErrorCodeConnection, executor.DBStageAcquire, "08006"),
			configure: func(fixture *pipelineFixture, databaseError *executor.DBError) {
				fixture.executor.openSessionErr = databaseError
			},
		},
		{
			name: "explain object missing", request: readRequest,
			database: testDBError(executor.DBErrorKindObjectNotFound, executor.DBErrorCodeObjectNotFound, executor.DBStageExplain, "42P01"),
			configure: func(fixture *pipelineFixture, databaseError *executor.DBError) {
				fixture.executor.explainErr = databaseError
			},
		},
		{
			name: "dynamic metadata", request: readRequest,
			database: testDBError(executor.DBErrorKindResource, executor.DBErrorCodeResource, executor.DBStageMetadata, "53300"),
			configure: func(fixture *pipelineFixture, databaseError *executor.DBError) {
				fixture.executor.transactionErr = databaseError
			},
		},
		{
			name: "query column missing", request: readRequest,
			database: testDBError(executor.DBErrorKindColumnNotFound, executor.DBErrorCodeColumnNotFound, executor.DBStageQuery, "42703"),
			configure: func(fixture *pipelineFixture, databaseError *executor.DBError) {
				fixture.executor.queryErr = databaseError
			},
		},
		{
			name: "read rows timeout", request: readRequest,
			database: testDBError(executor.DBErrorKindTimeout, executor.DBErrorCodeTimeout, executor.DBStageReadRows, "57014"),
			configure: func(fixture *pipelineFixture, databaseError *executor.DBError) {
				fixture.executor.queryErr = databaseError
			},
		},
		{
			name: "query interrupted", request: readRequest,
			database: testDBError(executor.DBErrorKindInterrupted, executor.DBErrorCodeInterrupted, executor.DBStageQuery, "57014"),
			configure: func(fixture *pipelineFixture, databaseError *executor.DBError) {
				fixture.executor.queryErr = databaseError
			},
		},
		{
			name: "execute constraint", request: writeRequest,
			database: testDBError(executor.DBErrorKindConstraint, executor.DBErrorCodeConstraint, executor.DBStageExecute, "23505"),
			configure: func(fixture *pipelineFixture, databaseError *executor.DBError) {
				fixture.executor.executeErr = databaseError
			},
		},
		{
			name: "execute read only", request: writeRequest,
			database: testDBError(executor.DBErrorKindReadOnly, executor.DBErrorCodeReadOnly, executor.DBStageExecute, "25006"),
			configure: func(fixture *pipelineFixture, databaseError *executor.DBError) {
				fixture.executor.executeErr = databaseError
			},
		},
		{
			name: "begin transaction", request: writeRequest,
			database: testDBError(executor.DBErrorKindTransaction, executor.DBErrorCodeTransaction, executor.DBStageBeginTx, "25000"),
			configure: func(fixture *pipelineFixture, databaseError *executor.DBError) {
				fixture.executor.beginErr = databaseError
			},
		},
		{
			name: "execute error with rollback error", request: writeRequest,
			database: testDBError(executor.DBErrorKindConstraint, executor.DBErrorCodeConstraint, executor.DBStageExecute, "23503"),
			configure: func(fixture *pipelineFixture, databaseError *executor.DBError) {
				fixture.executor.executeErr = databaseError
				fixture.executor.rollbackErr = testDBError(
					executor.DBErrorKindConnection,
					executor.DBErrorCodeConnection,
					executor.DBStageRollback,
					"08006",
				)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPipelineFixture(t)
			test.configure(fixture, test.database)

			response, err := fixture.pipeline.Process(context.Background(), test.request)

			require.NoError(t, err)
			assertClassifiedDatabaseResponse(t, response, test.database)
			require.Equal(t, 1, fixture.audit.calls())
			log := fixture.audit.last()
			require.Equal(t, string(model.DecisionError), log.Decision)
			require.NotNil(t, log.ErrorMsg)
			require.Equal(t, test.database.Error(), *log.ErrorMsg)
			require.NotContains(t, *log.ErrorMsg, test.database.DriverCode)
			require.NotNil(t, log.ErrorCode)
			require.Equal(t, string(test.database.Code), *log.ErrorCode)
		})
	}
}

func TestPipelineParserFailuresBecomeSyntaxBusinessResponses(t *testing.T) {
	tests := []struct {
		dialect string
		sql     string
		leak    string
	}{
		{dialect: "postgres", sql: "SELECT FROM WHERE", leak: "syntax error at or near"},
		{dialect: "mysql", sql: "SELEC * FROM agentsql.allowed_rows", leak: "syntax error at position"},
	}

	for _, test := range tests {
		t.Run(test.dialect, func(t *testing.T) {
			fixture := newPipelineFixture(t)
			fixture.datasources.datasource.DBType = test.dialect
			fixture.executor.dialect = test.dialect

			response, err := fixture.pipeline.Process(
				context.Background(),
				requestWithSQL(test.sql),
			)

			require.NoError(t, err)
			require.Equal(t, model.DecisionError, response.Decision)
			require.Equal(t, model.DecisionError, response.Assessment.Decision)
			require.Equal(t, string(executor.DBErrorCodeSyntax), response.ErrorCode)
			require.Equal(t, string(executor.DBStageParse), response.ErrorStage)
			require.Equal(t, executor.NewDBError(
				executor.DBErrorKindSyntax,
				executor.DBErrorCodeSyntax,
				executor.DBStageParse,
			).Error(), response.ErrorMessage)
			require.Equal(t, executor.Suggestion(executor.DBErrorCodeSyntax), response.Suggestion)
			require.Nil(t, response.Result)
			require.Zero(t, fixture.executors.calls())
			require.NotContains(t, response.ErrorMessage, test.leak)
			require.NotContains(t, response.Suggestion, test.leak)

			require.Equal(t, 1, fixture.audit.calls())
			log := fixture.audit.last()
			require.Equal(t, string(model.DecisionError), log.Decision)
			require.NotNil(t, log.ErrorCode)
			require.Equal(t, string(executor.DBErrorCodeSyntax), *log.ErrorCode)
			require.NotNil(t, log.ErrorMsg)
			require.Equal(t, response.ErrorMessage, *log.ErrorMsg)
			require.NotContains(t, *log.ErrorMsg, test.leak)
		})
	}
}

func TestPipelineDatabaseErrorPreservesStaticAuthorizationAndDynamicRuleResults(t *testing.T) {
	fixture := newPipelineFixture(t)
	fixture.executor.transaction = rules.TransactionState{
		InTransaction: true,
		AgeMS:         6_000,
		IdleMS:        6_000,
	}
	databaseError := testDBError(
		executor.DBErrorKindColumnNotFound,
		executor.DBErrorCodeColumnNotFound,
		executor.DBStageQuery,
		"42703",
	)
	fixture.executor.queryErr = databaseError

	response, err := fixture.pipeline.Process(context.Background(), defaultRequest())

	require.NoError(t, err)
	assertClassifiedDatabaseResponse(t, response, databaseError)
	require.Equal(t, model.RiskWarn, response.Assessment.Risk)
	require.Equal(t, model.StmtType("SELECT"), response.Assessment.StmtType)
	require.Contains(t, ruleHitIDs(response.Assessment.Hits), "R107")
}

func TestPipelineDatabaseErrorUsesIndependentAuditContextAfterDeadline(t *testing.T) {
	fixture := newPipelineFixture(t)
	databaseError := testDBError(
		executor.DBErrorKindTimeout,
		executor.DBErrorCodeTimeout,
		executor.DBStageExplain,
		"57014",
	)
	fixture.executor.explainErr = databaseError
	requestContext, cancel := context.WithCancel(context.Background())
	cancel()

	response, err := fixture.pipeline.Process(requestContext, defaultRequest())

	require.NoError(t, err)
	assertClassifiedDatabaseResponse(t, response, databaseError)
	require.Equal(t, 1, fixture.audit.calls())
	require.Positive(t, response.AuditID)
}

func TestPipelineDatabaseErrorDoesNotHideAuditOrReservationFailure(t *testing.T) {
	t.Run("audit failure", func(t *testing.T) {
		fixture := newPipelineFixture(t)
		fixture.executor.queryErr = testDBError(
			executor.DBErrorKindColumnNotFound,
			executor.DBErrorCodeColumnNotFound,
			executor.DBStageQuery,
			"42703",
		)
		fixture.audit.err = errors.New("audit DSN secret")

		response, err := fixture.pipeline.Process(context.Background(), defaultRequest())

		require.ErrorIs(t, err, ErrAuditUnavailable)
		require.NotEqual(t, model.DecisionError, response.Decision)
		require.Equal(t, string(executor.DBErrorCodeAuditUnavailable), response.ErrorCode)
		require.Nil(t, response.Result)
	})

	t.Run("reservation release failure", func(t *testing.T) {
		fixture := newPipelineFixture(t)
		fixture.executor.queryErr = testDBError(
			executor.DBErrorKindColumnNotFound,
			executor.DBErrorCodeColumnNotFound,
			executor.DBStageQuery,
			"42703",
		)
		releaseError := errors.New("release failed with secret")
		limiter := &spyRateLimiter{releaseErr: releaseError}
		fixture.pipeline.limiter = limiter

		response, err := fixture.pipeline.Process(context.Background(), defaultRequest())

		require.ErrorIs(t, err, releaseError)
		require.NotEqual(t, model.DecisionError, response.Decision)
		require.Equal(t, string(executor.DBErrorCodeGatewayInternal), response.ErrorCode)
		require.Equal(t, "网关内部错误", response.ErrorMessage)
		require.Nil(t, response.Result)
		require.Equal(t, "网关内部错误", *fixture.audit.last().ErrorMsg)
		require.Equal(t, string(executor.DBErrorCodeGatewayInternal), *fixture.audit.last().ErrorCode)
	})
}

func TestPipelineCommitOutcomeUnknownIsNeverBusinessConverted(t *testing.T) {
	fixture := newPipelineFixture(t)
	commitDatabaseError := testDBError(
		executor.DBErrorKindConnection,
		executor.DBErrorCodeConnection,
		executor.DBStageCommit,
		"08006",
	)
	fixture.executor.commitErr = commitDatabaseError

	response, err := fixture.pipeline.Process(
		context.Background(),
		requestWithSQL("UPDATE public.orders SET total = 1 WHERE id = 1"),
	)

	require.ErrorIs(t, err, ErrBusinessCommitUncertain)
	require.NotEqual(t, model.DecisionError, response.Decision)
	require.Equal(t, string(executor.DBErrorCodeCommitOutcomeUnknown), response.ErrorCode)
	require.Equal(t, "数据库提交结果未知", response.ErrorMessage)
	require.Contains(t, response.Suggestion, "请勿自动重试")
	require.Nil(t, response.Result)
}

func TestPipelineClassifiedDatabaseErrorIsObservedAsError(t *testing.T) {
	observer := &recordingDecisionObserver{}
	fixture := newPipelineFixture(t, WithObserver(observer))
	fixture.executor.queryErr = testDBError(
		executor.DBErrorKindColumnNotFound,
		executor.DBErrorCodeColumnNotFound,
		executor.DBStageQuery,
		"42703",
	)

	response, err := fixture.pipeline.Process(context.Background(), defaultRequest())

	require.NoError(t, err)
	require.Equal(t, model.DecisionError, response.Decision)
	decisions, _, _ := observer.snapshot()
	require.Len(t, decisions, 1)
	require.Equal(t, string(model.DecisionError), decisions[0].decision)
}

func TestPipelineResponseErrorFieldsUseStableJSONNames(t *testing.T) {
	response := Response{
		ErrorCode:    string(executor.DBErrorCodeObjectNotFound),
		ErrorStage:   string(executor.DBStageExplain),
		ErrorMessage: "表或对象不存在",
		Suggestion:   "请检查对象名称和当前数据库",
	}
	encoded, err := json.Marshal(response)
	require.NoError(t, err)
	var fields map[string]any
	require.NoError(t, json.Unmarshal(encoded, &fields))
	require.Equal(t, response.ErrorCode, fields["error_code"])
	require.Equal(t, response.ErrorStage, fields["error_stage"])
	require.Equal(t, response.ErrorMessage, fields["error_message"])
	require.Equal(t, response.Suggestion, fields["suggestion"])
}

func TestBusinessDatabaseErrorRejectsUnknownJoinedFailure(t *testing.T) {
	databaseError := testDBError(
		executor.DBErrorKindExecution,
		executor.DBErrorCodeExecution,
		executor.DBStageExecute,
		"XX000",
	)
	unknown := errors.New("unknown cleanup failure")
	got, ok := businessDatabaseError(errors.Join(databaseError, unknown))
	require.False(t, ok)
	require.Nil(t, got)
}

func assertClassifiedDatabaseResponse(
	t *testing.T,
	response Response,
	databaseError *executor.DBError,
) {
	t.Helper()
	require.Equal(t, model.DecisionError, response.Decision)
	require.Equal(t, model.DecisionError, response.Assessment.Decision)
	require.Equal(t, string(databaseError.Code), response.ErrorCode)
	require.Equal(t, string(databaseError.Stage), response.ErrorStage)
	require.Equal(t, databaseError.Error(), response.ErrorMessage)
	require.Equal(t, executor.Suggestion(databaseError.Code), response.Suggestion)
	require.Equal(t, databaseError.Error(), response.Assessment.Reason)
	require.Equal(t, executor.Suggestion(databaseError.Code), response.Assessment.Suggestion)
	require.Nil(t, response.Result)
	require.Empty(t, response.Redact)
	require.Empty(t, response.ApprovalID)
}

func testDBError(
	kind executor.DBErrorKind,
	code executor.DBErrorCode,
	stage executor.DBStage,
	driverCode string,
) *executor.DBError {
	return &executor.DBError{Kind: kind, Code: code, Stage: stage, DriverCode: driverCode}
}

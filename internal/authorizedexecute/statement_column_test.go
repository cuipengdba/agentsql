package authorizedexecute

import (
	"context"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/columnauth"
	"github.com/cuipengdba/agentsql/internal/mask"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestAuthorizedExecuteIsSoleColumnRouteAndRejectsDML(t *testing.T) {
	redactor, err := mask.NewRedactor(nil)
	require.NoError(t, err)
	request := ColumnAuthorizationRequest{
		Agent: model.Agent{ID: "agent", Status: "active", Level: "readonly"}, Redactor: redactor,
		PreliminaryAllowed: true, ControlRevisionDigest: "revision",
		DurableAudit: func(context.Context, ColumnAuthorizationAudit, *model.QueryResult, mask.RedactReport) error {
			return nil
		},
		FinalFence: func(context.Context) error { return nil },
	}
	gateway := NewGateway(false)
	datasource := model.Datasource{ID: "ds", DBType: "postgres"}
	statement, err := gateway.AuthorizedExecute(WithColumnAuthorization(context.Background(), request), datasource, make([]byte, 32), "SELECT 1", "")
	require.NoError(t, err)
	require.IsType(t, &columnBoundStatement{}, statement)
	require.NoError(t, statement.Close())

	statement, err = gateway.AuthorizedExecute(WithColumnAuthorization(context.Background(), request), datasource, make([]byte, 32), "UPDATE t SET c=1", "")
	require.Nil(t, statement)
	var authError *AuthError
	require.ErrorAs(t, err, &authError)
	require.Equal(t, ReasonStatementClassDenied, authError.Reason)
}

func TestColumnPreflightDenyAuditsWithoutOpeningBusinessDatabase(t *testing.T) {
	redactor, err := mask.NewRedactor(nil)
	require.NoError(t, err)
	var recorded ColumnAuthorizationAudit
	request := ColumnAuthorizationRequest{
		Agent: model.Agent{ID: "agent", Status: "active", Level: "readonly"}, Redactor: redactor,
		PreliminaryAllowed: false, ControlRevisionDigest: "revision",
		DurableAudit: func(_ context.Context, audit ColumnAuthorizationAudit, candidate *model.QueryResult, _ mask.RedactReport) error {
			require.Nil(t, candidate)
			recorded = audit
			return nil
		},
		FinalFence: func(context.Context) error { return nil },
	}
	gateway := NewGateway(false)
	statement, err := gateway.AuthorizedExecute(WithColumnAuthorization(context.Background(), request), model.Datasource{ID: "unreachable", DBType: "postgres"}, make([]byte, 32), "SELECT 1", "")
	require.NoError(t, err)
	_, err = statement.Execute(context.Background())
	require.NoError(t, err)
	outcome, ok := statement.(ColumnAuthorizedStatement).ColumnAuthorizationResult()
	require.True(t, ok)
	require.False(t, outcome.Allowed)
	require.Equal(t, ReasonAgentDenied, outcome.Reason)
	require.Equal(t, ColumnAuditVersion, recorded.Version)
	require.Equal(t, "deny", recorded.Decision)
	require.NoError(t, statement.Close())
}

func TestBindBeforePreliminaryDenyExceptionIsNarrow(t *testing.T) {
	valid := authorizedSelectRequest{ColumnAuthorizationRequest: ColumnAuthorizationRequest{
		Agent:                     model.Agent{ID: "agent", Status: "active", Level: "readonly"},
		PreliminaryAllowed:        false,
		BindBeforePreliminaryDeny: true,
	}}
	require.False(t, shouldFinishSelectPreflight(valid, columnauth.ReasonAgentDenied))

	disabled := valid
	disabled.BindBeforePreliminaryDeny = false
	require.True(t, shouldFinishSelectPreflight(disabled, columnauth.ReasonAgentDenied))

	expired := valid
	expiredAt := time.Now().Add(-time.Minute)
	expired.Agent.ExpiresAt = &expiredAt
	require.True(t, shouldFinishSelectPreflight(expired, columnauth.ReasonAgentDenied))
	require.True(t, shouldFinishSelectPreflight(valid, columnauth.ReasonDatasourceUnsupported))
	require.True(t, shouldFinishSelectPreflight(valid, columnauth.ReasonStatementDenied))
}

func TestFixedSelectProviderRoutesControlledReadThroughFacade(t *testing.T) {
	provider := &testColumnAuthorizationProvider{}
	gateway := NewGateway(true, WithColumnAuthorizationProvider(provider))
	ctx := WithReservationScope(context.Background(), "admin-user", "admin-user")
	statement, err := gateway.AuthorizedExecute(ctx, model.Datasource{ID: "unreachable", DBType: "postgres"}, make([]byte, 32), `SELECT "phone" FROM "s"."t" LIMIT 1`, "")
	require.NoError(t, err)
	_, ok := statement.(ColumnAuthorizedStatement)
	require.True(t, ok)
	_, err = statement.Query(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, "admin-user", provider.actor)
	require.Equal(t, 1, provider.audits)
	require.Equal(t, 1, provider.closes)
	require.NoError(t, statement.Close())
	require.Equal(t, 1, provider.closes)
}

type testColumnAuthorizationProvider struct {
	actor  string
	audits int
	closes int
}

func (provider *testColumnAuthorizationProvider) BeginColumnAuthorization(_ context.Context, actor string, _ model.Datasource) (ColumnAuthorizationRequest, func() error, error) {
	provider.actor = actor
	request := ColumnAuthorizationRequest{
		Agent: model.Agent{ID: actor, Status: "active", Level: "readonly"}, PreliminaryAllowed: false,
		DurableAudit: func(context.Context, ColumnAuthorizationAudit, *model.QueryResult, mask.RedactReport) error {
			provider.audits++
			return nil
		},
		FinalFence: func(context.Context) error { return nil },
	}
	return request, func() error { provider.closes++; return nil }, nil
}

var _ ColumnAuthorizationProvider = (*testColumnAuthorizationProvider)(nil)

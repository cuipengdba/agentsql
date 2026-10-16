package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/mask"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/stretchr/testify/require"
)

type fakeTaskReadGrantReader struct {
	grant        model.TaskReadGrant
	calls        int
	failAt       int
	consumeCalls int
}

func (reader *fakeTaskReadGrantReader) Consume(
	_ context.Context, id, tenantID, agentID, datasourceID, sqlSHA256 string, now time.Time,
) error {
	reader.consumeCalls++
	if id != reader.grant.ID || tenantID != reader.grant.TenantID ||
		agentID != reader.grant.AgentID || datasourceID != reader.grant.DatasourceID ||
		sqlSHA256 != reader.grant.SQLSHA256 || reader.grant.RevokedAt != nil ||
		!now.Before(reader.grant.ExpiresAt) || reader.grant.MaxUses == nil ||
		reader.grant.UsedCount >= *reader.grant.MaxUses {
		return store.ErrTaskReadGrantInvalid
	}
	reader.grant.UsedCount++
	return nil
}

func (reader *fakeTaskReadGrantReader) Validate(
	_ context.Context, id, tenantID, agentID, datasourceID, sqlSHA256 string, now time.Time,
) (model.TaskReadGrant, error) {
	reader.calls++
	if reader.calls == reader.failAt || id != reader.grant.ID ||
		tenantID != reader.grant.TenantID || agentID != reader.grant.AgentID ||
		datasourceID != reader.grant.DatasourceID || sqlSHA256 != reader.grant.SQLSHA256 ||
		reader.grant.RevokedAt != nil || !now.Before(reader.grant.ExpiresAt) ||
		(reader.grant.MaxUses != nil && reader.grant.UsedCount >= *reader.grant.MaxUses) {
		return model.TaskReadGrant{}, store.ErrTaskReadGrantInvalid
	}
	return reader.grant, nil
}

func newTaskReadPipelineFixture(t *testing.T, options ...Option) (*pipelineFixture, *fakeTaskReadGrantReader) {
	t.Helper()
	fixture := newPipelineFixture(t, options...)
	fixture.authenticator.agent.TenantID = store.DefaultTenantID
	fixture.authenticator.agent.TaskGrantID = "grant-one"
	fixture.authenticator.agent.TaskGrantIssuerUserID = "issuer-one"
	fixture.authenticator.agent.Level = "readonly"
	fixture.datasources.datasource.TenantID = store.DefaultTenantID
	request := defaultRequest()
	digest := sha256.Sum256([]byte(request.SQL))
	reader := &fakeTaskReadGrantReader{grant: model.TaskReadGrant{
		ID: "grant-one", TenantID: store.DefaultTenantID, IssuerUserID: "issuer-one",
		AgentID: "agent-1", DatasourceID: "datasource-1",
		SQLSHA256: hex.EncodeToString(digest[:]), ExpiresAt: time.Now().Add(time.Hour),
	}}
	fixture.pipeline.ports.TaskReadGrants = reader
	return fixture, reader
}

func TestTaskReadGrantAllowsOnlyExactSelectAndLinksAuditWithoutSQL(t *testing.T) {
	fixture, reader := newTaskReadPipelineFixture(t)
	response, err := fixture.pipeline.Process(context.Background(), defaultRequest())
	require.NoError(t, err)
	require.Equal(t, model.DecisionAllow, response.Decision)
	require.NotNil(t, response.Result)
	require.Equal(t, 4, reader.calls, "admission, pre-execute, pre-audit, and post-audit checks")
	require.Equal(t, 1, fixture.executor.callsSnapshot().query)
	log := fixture.audit.last()
	require.Nil(t, log.SQLRaw)
	require.Nil(t, log.SQLNorm)
	require.NotNil(t, log.DetailsJSON)
	require.JSONEq(t, `{"task_grant_id":"grant-one","task_grant_issuer_id":"issuer-one"}`, *log.DetailsJSON)
}

func TestTaskReadGrantBudgetDeniesSecondResultBeforeExecution(t *testing.T) {
	fixture, reader := newTaskReadPipelineFixture(t)
	one := int64(1)
	reader.grant.MaxUses = &one
	first, err := fixture.pipeline.Process(context.Background(), defaultRequest())
	require.NoError(t, err)
	require.Equal(t, model.DecisionAllow, first.Decision)
	require.NotNil(t, first.Result)
	second, err := fixture.pipeline.Process(context.Background(), defaultRequest())
	require.NoError(t, err)
	require.Equal(t, model.DecisionDeny, second.Decision)
	require.Nil(t, second.Result)
	require.EqualValues(t, 1, reader.grant.UsedCount)
	require.Equal(t, 1, reader.consumeCalls)
	require.Equal(t, 1, fixture.executor.callsSnapshot().query)
	require.Equal(t, "deny", fixture.audit.last().Decision)
}

func TestTaskReadGrantBudgetAuditFailureDoesNotConsume(t *testing.T) {
	fixture, reader := newTaskReadPipelineFixture(t)
	one := int64(1)
	reader.grant.MaxUses = &one
	fixture.audit.err = errors.New("audit write failed")
	response, err := fixture.pipeline.Process(context.Background(), defaultRequest())
	require.ErrorIs(t, err, ErrAuditUnavailable)
	require.Nil(t, response.Result)
	require.EqualValues(t, 0, reader.grant.UsedCount)
	require.Zero(t, reader.consumeCalls)
}

func TestTaskReadGrantDenyMatrixNeverExecutesUnauthorizedSQL(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*pipelineFixture, *fakeTaskReadGrantReader, *Request)
	}{
		{"changed SQL", func(_ *pipelineFixture, _ *fakeTaskReadGrantReader, request *Request) {
			request.SQL += " "
		}},
		{"changed datasource", func(fixture *pipelineFixture, _ *fakeTaskReadGrantReader, request *Request) {
			request.DatasourceID = "different"
			fixture.datasources.datasource.ID = "different"
		}},
		{"cross tenant", func(fixture *pipelineFixture, _ *fakeTaskReadGrantReader, _ *Request) {
			fixture.authenticator.agent.TenantID = "another-tenant"
		}},
		{"permanent policy removed", func(fixture *pipelineFixture, _ *fakeTaskReadGrantReader, _ *Request) {
			fixture.policies.policies = nil
		}},
		{"revoked", func(_ *pipelineFixture, reader *fakeTaskReadGrantReader, _ *Request) {
			now := time.Now()
			reader.grant.RevokedAt = &now
		}},
		{"expired", func(_ *pipelineFixture, reader *fakeTaskReadGrantReader, _ *Request) {
			reader.grant.ExpiresAt = time.Now().Add(-time.Second)
		}},
		{"write statement", func(_ *pipelineFixture, reader *fakeTaskReadGrantReader, request *Request) {
			request.SQL = "UPDATE public.customers SET phone = 'x' WHERE id = 1"
			digest := sha256.Sum256([]byte(request.SQL))
			reader.grant.SQLSHA256 = hex.EncodeToString(digest[:])
		}},
		{"write CTE despite exact digest", func(_ *pipelineFixture, reader *fakeTaskReadGrantReader, request *Request) {
			request.SQL = "WITH x AS (DELETE FROM public.customers RETURNING id) SELECT id FROM x"
			digest := sha256.Sum256([]byte(request.SQL))
			reader.grant.SQLSHA256 = hex.EncodeToString(digest[:])
		}},
		{"side effect function despite exact digest", func(_ *pipelineFixture, reader *fakeTaskReadGrantReader, request *Request) {
			request.SQL = "SELECT nextval('customer_seq')"
			digest := sha256.Sum256([]byte(request.SQL))
			reader.grant.SQLSHA256 = hex.EncodeToString(digest[:])
		}},
		{"subquery despite exact digest", func(_ *pipelineFixture, reader *fakeTaskReadGrantReader, request *Request) {
			request.SQL = "SELECT id FROM public.customers WHERE id IN (SELECT id FROM public.customers)"
			digest := sha256.Sum256([]byte(request.SQL))
			reader.grant.SQLSHA256 = hex.EncodeToString(digest[:])
		}},
		{"approval", func(_ *pipelineFixture, _ *fakeTaskReadGrantReader, request *Request) {
			request.RequireApproval = true
		}},
		{"revoked before execute", func(_ *pipelineFixture, reader *fakeTaskReadGrantReader, _ *Request) {
			reader.failAt = 2
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			fixture, reader := newTaskReadPipelineFixture(t)
			request := defaultRequest()
			test.mutate(fixture, reader, &request)
			response, err := fixture.pipeline.Process(context.Background(), request)
			require.NoError(t, err)
			require.Equal(t, model.DecisionDeny, response.Decision)
			require.Nil(t, response.Result)
			require.Zero(t, fixture.executor.callsSnapshot().query)
			require.Zero(t, fixture.executor.callsSnapshot().execute)
			require.Equal(t, "deny", fixture.audit.last().Decision)
		})
	}
}

func TestTaskReadGrantRevokedAfterQueryNeverDeliversResult(t *testing.T) {
	fixture, reader := newTaskReadPipelineFixture(t)
	reader.failAt = 3
	response, err := fixture.pipeline.Process(context.Background(), defaultRequest())
	require.NoError(t, err)
	require.Equal(t, 1, fixture.executor.callsSnapshot().query)
	require.Equal(t, model.DecisionDeny, response.Decision)
	require.Nil(t, response.Result)
	require.Equal(t, "deny", fixture.audit.last().Decision)
}

func TestTaskReadGrantDeniedSQLAuditKeepsAuthenticatedIssuer(t *testing.T) {
	fixture, _ := newTaskReadPipelineFixture(t)
	request := defaultRequest()
	request.SQL += " " // The task key authenticates, but the exact digest does not match.

	response, err := fixture.pipeline.Process(context.Background(), request)
	require.NoError(t, err)
	require.Equal(t, model.DecisionDeny, response.Decision)
	require.Nil(t, response.Result)
	require.Zero(t, fixture.executor.callsSnapshot().query)
	logs := fixture.audit.snapshot()
	require.Len(t, logs, 1)
	require.Equal(t, "deny", logs[0].Decision)
	require.Nil(t, logs[0].SQLRaw)
	require.Nil(t, logs[0].SQLNorm)
	require.NotNil(t, logs[0].DetailsJSON)
	require.JSONEq(t, `{"task_grant_id":"grant-one","task_grant_issuer_id":"issuer-one"}`, *logs[0].DetailsJSON)
}

func TestTaskReadGrantRevokedDuringFirstAuditAppendsDenyOutcome(t *testing.T) {
	for _, test := range []struct {
		name         string
		outcomeFails bool
	}{
		{name: "outcome recorded"},
		{name: "outcome audit failure closes result", outcomeFails: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture, reader := newTaskReadPipelineFixture(t)
			firstAuditStarted := make(chan struct{})
			releaseFirstAudit := make(chan struct{})
			first := true
			fixture.audit.onRecord = func() {
				if !first {
					return
				}
				first = false
				close(firstAuditStarted)
				<-releaseFirstAudit
			}
			if test.outcomeFails {
				fixture.audit.err = errors.New("outcome audit unavailable")
				fixture.audit.errAtAttempt = 2
			}
			type outcome struct {
				response Response
				err      error
			}
			completed := make(chan outcome, 1)
			go func() {
				response, err := fixture.pipeline.Process(context.Background(), defaultRequest())
				completed <- outcome{response: response, err: err}
			}()
			select {
			case <-firstAuditStarted:
			case <-time.After(5 * time.Second):
				close(releaseFirstAudit)
				t.Fatal("first audit did not start")
			}
			// The first audit is blocked after all three pre-audit grant checks.
			// Revocation completes before it can return to the pipeline.
			now := time.Now().UTC()
			reader.grant.RevokedAt = &now
			close(releaseFirstAudit)
			var result outcome
			select {
			case result = <-completed:
			case <-time.After(5 * time.Second):
				t.Fatal("task query did not complete after audit release")
			}
			require.Equal(t, 4, reader.calls)
			require.Equal(t, 1, fixture.executor.callsSnapshot().query)
			require.Nil(t, result.response.Result)
			if test.outcomeFails {
				require.ErrorIs(t, result.err, ErrAuditUnavailable)
				require.Equal(t, model.DecisionDeny, result.response.Decision)
				require.Len(t, fixture.audit.snapshot(), 1)
				return
			}
			require.NoError(t, result.err)
			require.Equal(t, model.DecisionDeny, result.response.Decision)
			logs := fixture.audit.snapshot()
			require.Len(t, logs, 2)
			require.Equal(t, "allow", logs[0].Decision)
			require.Equal(t, "deny", logs[1].Decision)
			require.Nil(t, logs[0].SQLRaw)
			require.Nil(t, logs[1].SQLRaw)
			require.NotNil(t, logs[1].DetailsJSON)
			require.JSONEq(t, `{"audit_phase":"outcome","related_audit_id":1,"task_grant_id":"grant-one","task_grant_issuer_id":"issuer-one"}`, *logs[1].DetailsJSON)
			require.Equal(t, logs[1].ID, result.response.AuditID)
		})
	}
}

func TestTaskReadGrantRevokedBeforeB2BeginNeverOpensDatabase(t *testing.T) {
	redactor, err := mask.NewRedactor(nil)
	require.NoError(t, err)
	controller := &enabledB2Controller{snapshot: &routingB2Snapshot{redactor: redactor}}
	fixture, reader := newTaskReadPipelineFixture(t, WithColumnAuthorization(controller))
	provider := &routingB2ExecutorProvider{delegate: fixture.executor}
	fixture.pipeline.ports.Executors = provider
	reader.failAt = 2 // admission succeeds; the B2 execution gate sees revocation.

	response, err := fixture.pipeline.Process(context.Background(), defaultRequest())
	require.NoError(t, err)
	require.Equal(t, model.DecisionDeny, response.Decision)
	require.Nil(t, response.Result)
	require.Equal(t, 2, reader.calls)
	require.Zero(t, controller.beginCalls)
	require.Zero(t, provider.calls)
	require.Equal(t, "deny", fixture.audit.last().Decision)
}

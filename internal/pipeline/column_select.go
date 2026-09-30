package pipeline

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	executor "github.com/cuipengdba/agentsql/internal/authorizedexecute"
	"github.com/cuipengdba/agentsql/internal/lockrank"
	"github.com/cuipengdba/agentsql/internal/mask"
	"github.com/cuipengdba/agentsql/internal/model"
)

func (run *pipelineRun) processColumnAuthorizedSelect(
	ctx context.Context,
	bindBeforePreliminaryDeny bool,
) (Response, error) {
	phaseStarted := time.Now()
	previousPhase := "preflight_reservation"
	phaseObserver, _ := run.pipeline.observer.(interface {
		ObserveB2Phase(string, time.Duration)
	})
	observePhase := func(phase executor.SelectPhase) {
		now := time.Now()
		if phaseObserver != nil {
			phaseObserver.ObserveB2Phase(previousPhase, now.Sub(phaseStarted))
		}
		phaseStarted, previousPhase = now, string(phase)
	}
	ctx = lockrank.WithTracker(ctx)
	snapshot, err := run.pipeline.column.Begin(ctx, *run.agent, *run.datasource)
	if err != nil {
		return run.finish(ctx, err)
	}
	if isNilInterface(snapshot) {
		return run.finish(ctx, fmt.Errorf("column authorization controller returned nil snapshot"))
	}
	redactor := snapshot.Redactor()
	if isNilInterface(redactor) {
		_ = snapshot.Close()
		return run.finish(ctx, &executor.AuthError{Reason: executor.ReasonMaskCapabilityMissing})
	}
	rowLimit, err := normalizedRowLimit(run.datasource.RowLimit)
	if err != nil {
		_ = snapshot.Close()
		return run.finish(ctx, err)
	}
	tenant := run.agent.ID
	if run.agent.Owner != nil && strings.TrimSpace(*run.agent.Owner) != "" {
		tenant = strings.TrimSpace(*run.agent.Owner)
	}
	ctx = executor.WithReservationScope(ctx, run.agent.ID, tenant)
	lockedContext, cancelLocked := executor.WithLockDeadline(ctx, executor.DefaultRequestWall)
	defer cancelLocked()
	columnContext := executor.WithColumnAuthorization(lockedContext, executor.ColumnAuthorizationRequest{
		Agent: *run.agent, Policies: snapshot.Policies(), Redactor: redactor, RowLimit: rowLimit,
		PreliminaryAllowed:        run.response.Decision == model.DecisionAllow || run.response.Decision == model.DecisionWarn,
		BindBeforePreliminaryDeny: bindBeforePreliminaryDeny,
		ControlRevisionDigest:     snapshot.RevisionDigest(), Limits: executor.DefaultLimits,
		DurableAudit: func(auditContext context.Context, detail executor.ColumnAuthorizationAudit, candidate *model.QueryResult, report mask.RedactReport) error {
			copyDetail := detail
			run.response.ColumnAuth = &copyDetail
			if candidate != nil {
				copied := cloneQueryResult(*candidate)
				run.executionResult = &copied
				run.response.Result = &copied
				run.response.Redact = report
			} else {
				run.response.Decision = model.DecisionDeny
				run.response.Assessment.Decision = model.DecisionDeny
				run.response.Assessment.Risk = model.RiskDeny
				if bindBeforePreliminaryDeny {
					run.response.ErrorCode = detail.Reason
					run.response.ErrorStage = StageGuardStatic
				} else {
					run.response.Assessment.Reason = detail.Reason
				}
			}
			return run.audit(auditContext, string(run.response.Decision), nil, auditPhaseSingle)
		},
		FinalFence: func(fenceContext context.Context) error {
			return snapshot.FinalCheck(fenceContext)
		},
		Observe: observePhase,
	})
	statement, err := run.pipeline.ports.Executors.AuthorizedExecute(
		columnContext, *run.datasource, append([]byte(nil), run.pipeline.secret...), run.request.SQL, "",
	)
	if err != nil {
		_ = snapshot.Close()
		return run.finish(ctx, err)
	}
	columnStatement, ok := statement.(executor.ColumnAuthorizedStatement)
	if !ok || isNilInterface(columnStatement) {
		_ = statement.Close()
		_ = snapshot.Close()
		return run.finish(ctx, &executor.AuthError{Reason: executor.ReasonAuthorizationProofInvalid})
	}
	_, err = columnStatement.Execute(columnContext)
	if phaseObserver != nil {
		phaseObserver.ObserveB2Phase(previousPhase, time.Since(phaseStarted))
	}
	selected, outcomeOK := columnStatement.ColumnAuthorizationResult()
	statementCloseErr := columnStatement.Close()
	closeErr := snapshot.Close()
	if err != nil {
		if statementCloseErr != nil {
			err = errors.Join(err, statementCloseErr)
		}
		if closeErr != nil {
			err = errors.Join(err, closeErr)
		}
		return run.finish(ctx, err)
	}
	if !outcomeOK {
		return run.finish(ctx, &executor.AuthError{Reason: executor.ReasonAuthorizationProofInvalid})
	}
	if statementCloseErr != nil {
		return run.finish(ctx, statementCloseErr)
	}
	if closeErr != nil {
		return run.finish(ctx, closeErr)
	}
	if !selected.Allowed {
		run.response.Decision = model.DecisionDeny
		run.response.Assessment.Decision = model.DecisionDeny
		run.response.Assessment.Risk = model.RiskDeny
		if bindBeforePreliminaryDeny {
			run.response.ErrorCode = string(selected.Reason)
			run.response.ErrorStage = StageGuardStatic
		} else {
			run.response.Assessment.Reason = string(selected.Reason)
		}
		return run.finish(ctx, nil)
	}
	if !selected.Seal.Verify(selected.Encoded) {
		return run.finish(ctx, &executor.AuthError{Reason: executor.ReasonDeliverySealFailed})
	}
	result := cloneQueryResult(*run.response.Result)
	run.response.Result = &result
	run.executionResult = &result
	return run.finish(ctx, nil)
}

func cloneQueryResult(source model.QueryResult) model.QueryResult {
	result := source
	result.Columns = append([]string(nil), source.Columns...)
	result.Rows = make([][]string, len(source.Rows))
	for index := range source.Rows {
		result.Rows[index] = append([]string(nil), source.Rows[index]...)
	}
	return result
}

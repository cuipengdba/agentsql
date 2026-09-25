package b5coordinator

import (
	"context"
	"errors"
	"time"

	"github.com/cuipengdba/agentsql/internal/b5"
	"github.com/cuipengdba/agentsql/internal/b5terminal"
	"github.com/cuipengdba/agentsql/internal/store"
)

const (
	operationExecuting uint32 = iota
	operationCompleted
	operationQuiescing
	operationTerminal
)

func (coordinator *Coordinator) Execute(ctx context.Context, request ExecuteRequest) (Result, error) {
	live, err := coordinator.validateLive(ctx, request.Session, request.TransactionID)
	if err != nil {
		return Result{}, err
	}

	live.mu.Lock()
	if live.record.Status == b5.TransactionRollbackOnly {
		live.mu.Unlock()
		return coordinator.snapshot(live, b5.ErrorTxRollbackOnly, b5.EffectMarkRollbackOnly), fail(b5.ErrorTxRollbackOnly, ErrRollbackOnly)
	}
	if live.record.Status != b5.TransactionActive || live.record.Phase != b5.PhaseActive {
		live.mu.Unlock()
		return Result{}, fail(b5.ErrorTxNotActive, ErrTerminal)
	}
	if live.operation != nil {
		live.mu.Unlock()
		return Result{}, fail(b5.ErrorSessionBusy, ErrBusy)
	}
	if request.Ordinal != live.record.StatementCount || request.Ordinal < 0 || request.Ordinal >= len(live.plan.Statements) || live.plan.Statements[request.Ordinal].OperationID != request.OperationID {
		live.mu.Unlock()
		return coordinator.forceRollback(ctx, live, request.RequestID, b5.ErrorTxActivePlanMismatch, ErrWrongOrdinal)
	}
	statement := live.plan.Statements[request.Ordinal]
	now := coordinator.clock.Now()
	deadline := now.Add(live.plan.Limits.StatementTimeout)
	if deadline.After(live.record.WallDeadline) {
		deadline = live.record.WallDeadline
	}
	live.operationGeneration++
	op := &operation{generation: live.operationGeneration, done: make(chan statementCompletion, 1)}
	live.operation = op
	deadlinePointer := &deadline
	next, persistErr := coordinator.transactions.CASProgress(ctx, live.record.TransactionID, live.record.Revision, live.record.Status, live.record.Phase, store.B5TransactionProgress{StatementDeadline: &deadlinePointer})
	if persistErr == nil {
		live.record = next
	}
	capability := live.capability
	live.mu.Unlock()
	if persistErr != nil {
		return coordinator.forceRollback(ctx, live, request.RequestID, b5.ErrorTxRollbackOnly, persistErr)
	}

	go func() {
		result, executeErr := capability.Execute(ctx, statement)
		op.state.CompareAndSwap(operationExecuting, operationCompleted)
		op.done <- statementCompletion{result: result, err: executeErr}
	}()

	watchdog := time.NewTimer(live.plan.Limits.OperationWatchdog)
	statementWait := deadline.Sub(coordinator.clock.Now())
	if statementWait < 0 {
		statementWait = 0
	}
	statementTimer := time.NewTimer(statementWait)
	defer watchdog.Stop()
	defer statementTimer.Stop()
	select {
	case completion := <-op.done:
		return coordinator.completeStatement(ctx, live, op, statement, request.RequestID, completion)
	case <-watchdog.C:
		return coordinator.watchdogStatement(ctx, live, op, request.RequestID, b5.ErrorTxOperationWatchdog)
	case <-statementTimer.C:
		return coordinator.watchdogStatement(ctx, live, op, request.RequestID, b5.ErrorTxStatementTimeout)
	case <-ctx.Done():
		return coordinator.watchdogStatement(context.Background(), live, op, request.RequestID, b5.ErrorTxOperationWatchdog)
	}
}

func (coordinator *Coordinator) completeStatement(ctx context.Context, live *liveTransaction, op *operation, statement PlannedStatement, requestID string, completion statementCompletion) (Result, error) {
	if !op.state.CompareAndSwap(operationExecuting, operationCompleted) && op.state.Load() != operationCompleted {
		return coordinator.waitTerminal(ctx, live)
	}
	if completion.err != nil || !completion.result.CapabilityCertain || completion.result.Decision == DecisionDeny || completion.result.Decision == DecisionApprove || completion.result.Decision == DecisionMask || completion.result.Decision == DecisionUnknown {
		code := b5.ErrorTxRollbackOnly
		if !completion.result.CapabilityCertain {
			code = b5.ErrorAuthCatalogRace
		}
		return coordinator.forceRollback(ctx, live, requestID, code, errors.Join(completion.err, ErrCapabilityUnknown))
	}
	if completion.result.AffectedRows < 0 {
		return coordinator.forceRollback(ctx, live, requestID, b5.ErrorTxExecutionLimitExceeded, errors.New("negative affected rows"))
	}
	live.mu.Lock()
	exceeds := completion.result.AffectedRows > live.plan.Limits.MaxAffectedRows-live.affectedRows
	sequence := live.record.TransactionSeq + 1
	live.mu.Unlock()
	if exceeds {
		return coordinator.forceRollback(ctx, live, requestID, b5.ErrorTxExecutionLimitExceeded, errors.New("affected-row budget exceeded"))
	}
	audit, auditErr := coordinator.audit.Barrier(ctx, AuditEvent{Kind: AuditStatement, TransactionID: live.record.TransactionID, SessionID: live.record.SessionID, RequestID: requestID, Sequence: sequence, PlanDigest: live.plan.Digest, StatementOrdinal: statement.Ordinal, Action: statement.Action, AffectedRows: completion.result.AffectedRows, EvidenceDigest: completion.result.EvidenceDigest})
	if auditErr != nil || audit.Durability != b5.DurabilityDurable {
		return coordinator.forceRollback(ctx, live, requestID, b5.ErrorAuditStatementBarrierFailed, auditErr)
	}

	live.mu.Lock()
	if live.operation != op || op.state.Load() != operationCompleted {
		live.mu.Unlock()
		return coordinator.waitTerminal(ctx, live)
	}
	count := live.record.StatementCount + 1
	txSequence := live.record.TransactionSeq + 1
	idle := coordinator.clock.Now().Add(live.plan.Limits.IdleTimeout)
	if idle.After(live.record.WallDeadline) {
		idle = live.record.WallDeadline
	}
	var noDeadline *time.Time
	digest := append([]byte(nil), audit.EventDigest[:]...)
	next, err := coordinator.transactions.CASProgress(ctx, live.record.TransactionID, live.record.Revision, live.record.Status, live.record.Phase, store.B5TransactionProgress{IdleDeadline: &idle, StatementDeadline: &noDeadline, StatementCount: &count, TransactionSeq: &txSequence, PreviousEventDigest: &digest})
	if err == nil {
		live.record = next
		live.affectedRows += completion.result.AffectedRows
		live.auditDurability = audit.Durability
		live.evidence = append(live.evidence, OperationEvidence{Ordinal: statement.Ordinal, OperationID: statement.OperationID, AffectedRows: completion.result.AffectedRows, StatementEvidenceDigest: completion.result.EvidenceDigest, AuditEventDigest: audit.EventDigest})
		live.operation = nil
		op.state.Store(operationTerminal)
	}
	live.mu.Unlock()
	if err != nil {
		return coordinator.forceRollback(ctx, live, requestID, b5.ErrorTxRollbackOnly, err)
	}
	return coordinator.snapshot(live, b5.ErrorNone, b5.EffectKeepActive), nil
}

func (coordinator *Coordinator) watchdogStatement(ctx context.Context, live *liveTransaction, op *operation, requestID string, code b5.ErrorCode) (Result, error) {
	if !op.state.CompareAndSwap(operationExecuting, operationQuiescing) {
		if op.state.Load() == operationCompleted {
			completion := <-op.done
			statement := live.plan.Statements[live.record.StatementCount]
			return coordinator.completeStatement(ctx, live, op, statement, requestID, completion)
		}
		return coordinator.waitTerminal(ctx, live)
	}
	observation := live.terminal.ObserveWallDeadline(live.record.ConnectionGeneration, live.record.OwnerEpoch)
	if observation.Decision != b5terminal.TimeoutWonBeforeCommit {
		return coordinator.waitTerminal(ctx, live)
	}
	emission, cancelErr := live.capability.Cancel(ctx)
	if emission != b5terminal.CancelNotSentProven {
		coordinator.prepareWatchdogTerminal(ctx, live)
		disposition, discardErr := live.capability.Discard(ctx)
		resolution := b5terminal.TerminalResolution{Schema: b5terminal.ConnectionDispositionSchema, SchemaVersion: b5terminal.ConnectionDispositionVersion, Outcome: b5.OutcomeNotCommitted, Disposition: disposition}
		_ = live.terminal.Complete(observation.Attempt, resolution)
		return coordinator.finishDiscard(ctx, live, requestID, code, resolution, errors.Join(cancelErr, discardErr))
	}
	grace := time.NewTimer(live.plan.Limits.QuiesceGrace)
	defer grace.Stop()
	select {
	case <-op.done:
		if err := live.terminal.BeginTimeoutRollback(observation.Attempt); err != nil {
			return coordinator.waitTerminal(ctx, live)
		}
		coordinator.prepareWatchdogTerminal(ctx, live)
		return coordinator.finishRollbackAttempt(ctx, live, requestID, code, observation.Attempt, errors.Join(cancelErr, context.DeadlineExceeded))
	case <-grace.C:
		coordinator.prepareWatchdogTerminal(ctx, live)
		disposition, discardErr := live.capability.Discard(ctx)
		resolution := b5terminal.TerminalResolution{Schema: b5terminal.ConnectionDispositionSchema, SchemaVersion: b5terminal.ConnectionDispositionVersion, Outcome: b5.OutcomeNotCommitted, Disposition: disposition}
		_ = live.terminal.Complete(observation.Attempt, resolution)
		return coordinator.finishDiscard(ctx, live, requestID, code, resolution, discardErr)
	}
}

func (coordinator *Coordinator) prepareWatchdogTerminal(ctx context.Context, live *liveTransaction) {
	_ = coordinator.advanceIfPossible(ctx, live, b5.TransactionRollbackOnly, b5.PhaseRollbackOnly)
	_ = coordinator.advanceIfPossible(ctx, live, b5.TransactionActive, b5.PhaseRollingBack)
}

func (coordinator *Coordinator) Commit(ctx context.Context, request FinishRequest) (Result, error) {
	live, err := coordinator.validateLive(ctx, request.Session, request.TransactionID)
	if err != nil {
		return Result{}, err
	}
	live.mu.Lock()
	if live.record.Status == b5.TransactionRollbackOnly {
		live.mu.Unlock()
		return coordinator.forceRollback(ctx, live, request.RequestID, b5.ErrorTxRollbackOnly, ErrRollbackOnly)
	}
	if live.operation != nil {
		live.mu.Unlock()
		return Result{}, fail(b5.ErrorSessionBusy, ErrBusy)
	}
	if live.record.StatementCount != len(live.plan.Statements) {
		live.mu.Unlock()
		return Result{}, fail(b5.ErrorTxPlanIncomplete, ErrWrongOrdinal)
	}
	sequence := live.record.TransactionSeq + 1
	live.mu.Unlock()
	audit, auditErr := coordinator.audit.Barrier(ctx, AuditEvent{Kind: AuditCommitIntent, TransactionID: live.record.TransactionID, SessionID: live.record.SessionID, RequestID: request.RequestID, Sequence: sequence, PlanDigest: live.plan.Digest})
	if auditErr != nil || audit.Durability != b5.DurabilityDurable {
		return coordinator.forceRollback(ctx, live, request.RequestID, b5.ErrorAuditCommitIntentFailed, auditErr)
	}
	live.mu.Lock()
	commitDigest := append([]byte(nil), audit.EventDigest[:]...)
	next, progressErr := coordinator.transactions.CASProgress(ctx, live.record.TransactionID, live.record.Revision, live.record.Status, live.record.Phase, store.B5TransactionProgress{TransactionSeq: &sequence, PreviousEventDigest: &commitDigest})
	if progressErr == nil {
		live.record = next
	}
	live.mu.Unlock()
	if progressErr != nil {
		return coordinator.forceRollback(ctx, live, request.RequestID, b5.ErrorAuditCommitIntentFailed, progressErr)
	}
	attempt, won := live.terminal.TryCommit(live.record.ConnectionGeneration, live.record.OwnerEpoch)
	if !won {
		return coordinator.waitTerminal(ctx, live)
	}
	if err := coordinator.advance(ctx, live, b5.TransactionActive, b5.PhaseCommitting); err != nil {
		disposition, discardErr := live.capability.Discard(ctx)
		resolution := b5terminal.TerminalResolution{Schema: b5terminal.ConnectionDispositionSchema, SchemaVersion: b5terminal.ConnectionDispositionVersion, Outcome: b5.OutcomeUnknown, Disposition: disposition}
		_ = live.terminal.Complete(attempt, resolution)
		return coordinator.finishDiscard(ctx, live, request.RequestID, b5.ErrorTxDBOutcomeUnknown, resolution, errors.Join(err, discardErr))
	}
	terminal, terminalErr := live.capability.FinishCommit(ctx, live.terminal, attempt)
	return coordinator.finishTerminal(ctx, live, request.RequestID, terminal, terminalErr)
}

func (coordinator *Coordinator) Rollback(ctx context.Context, request FinishRequest) (Result, error) {
	live, err := coordinator.validateLive(ctx, request.Session, request.TransactionID)
	if err != nil {
		return Result{}, err
	}
	return coordinator.forceRollback(ctx, live, request.RequestID, b5.ErrorNone, nil)
}

func (coordinator *Coordinator) forceRollback(ctx context.Context, live *liveTransaction, requestID string, code b5.ErrorCode, cause error) (Result, error) {
	live.mu.Lock()
	status, phase := live.record.Status, live.record.Phase
	live.operation = nil
	live.mu.Unlock()
	if status == b5.TransactionActive && phase == b5.PhaseActive {
		_ = coordinator.advance(ctx, live, b5.TransactionRollbackOnly, b5.PhaseRollbackOnly)
	}
	attempt, won := live.terminal.TryRollback(live.record.ConnectionGeneration, live.record.OwnerEpoch)
	if !won {
		return coordinator.waitTerminal(ctx, live)
	}
	if err := coordinator.advance(ctx, live, b5.TransactionActive, b5.PhaseRollingBack); err != nil {
		disposition, discardErr := live.capability.Discard(ctx)
		resolution := b5terminal.TerminalResolution{Schema: b5terminal.ConnectionDispositionSchema, SchemaVersion: b5terminal.ConnectionDispositionVersion, Outcome: b5.OutcomeNotCommitted, Disposition: disposition}
		_ = live.terminal.Complete(attempt, resolution)
		return coordinator.finishDiscard(ctx, live, requestID, code, resolution, errors.Join(cause, err, discardErr))
	}
	return coordinator.finishRollbackAttempt(ctx, live, requestID, code, attempt, cause)
}

func (coordinator *Coordinator) finishRollbackAttempt(ctx context.Context, live *liveTransaction, requestID string, code b5.ErrorCode, attempt b5terminal.TerminalAttempt, cause error) (Result, error) {
	proof := b5terminal.NoCommitEverSent{Schema: b5terminal.NoCommitEverSentSchema, SchemaVersion: b5terminal.NoCommitEverSentVersion, TransactionGeneration: attempt.TransactionGeneration, OwnerGeneration: attempt.OwnerGeneration, HistoryContinuous: true, UniqueSocketOwner: true}
	terminal, terminalErr := live.capability.FinishRollback(ctx, live.terminal, attempt, proof)
	rollbackAuditErr := coordinator.recordRollbackAudit(ctx, live, requestID, code, terminal.Resolution.Outcome, terminal.EvidenceDigest)
	result, finishErr := coordinator.finishTerminal(ctx, live, requestID, terminal, errors.Join(cause, terminalErr, rollbackAuditErr))
	if code != b5.ErrorNone {
		result.Code = code
		if finishErr == nil {
			finishErr = cause
		}
		if finishErr == nil {
			finishErr = ErrRollbackOnly
		}
		finishErr = fail(code, finishErr)
	}
	return result, finishErr
}

// recordRollbackAudit records the DB rollback fact before the terminal
// outcome. A post-effect audit failure cannot change the already-known DB
// outcome, but it remains visible through the returned durability/error axes.
func (coordinator *Coordinator) recordRollbackAudit(ctx context.Context, live *liveTransaction, requestID string, code b5.ErrorCode, outcome b5.DBOutcome, evidence [32]byte) error {
	live.mu.Lock()
	sequence := live.record.TransactionSeq + 1
	record := live.record
	live.mu.Unlock()
	audit, auditErr := coordinator.audit.Barrier(ctx, AuditEvent{Kind: AuditRollback, TransactionID: record.TransactionID, SessionID: record.SessionID, RequestID: requestID, Sequence: sequence, PlanDigest: live.plan.Digest, DBOutcome: outcome, ErrorCode: code, EvidenceDigest: evidence})
	if auditErr != nil {
		return auditErr
	}
	digest := append([]byte(nil), audit.EventDigest[:]...)
	live.mu.Lock()
	next, persistErr := coordinator.transactions.CASProgress(ctx, live.record.TransactionID, live.record.Revision, live.record.Status, live.record.Phase, store.B5TransactionProgress{TransactionSeq: &sequence, PreviousEventDigest: &digest})
	if persistErr == nil {
		live.record = next
		live.auditDurability = audit.Durability
	}
	live.mu.Unlock()
	return persistErr
}

func (coordinator *Coordinator) finishDiscard(ctx context.Context, live *liveTransaction, requestID string, code b5.ErrorCode, resolution b5terminal.TerminalResolution, cause error) (Result, error) {
	terminal := TerminalResult{Resolution: resolution, CancelEmission: b5terminal.CancelSendStartedOrIndeterminate}
	result, err := coordinator.finishTerminal(ctx, live, requestID, terminal, cause)
	result.Code = code
	if code != b5.ErrorNone {
		if err == nil {
			err = cause
		}
		if err == nil {
			err = context.DeadlineExceeded
		}
		err = fail(code, err)
	}
	return result, err
}

func (coordinator *Coordinator) finishTerminal(ctx context.Context, live *liveTransaction, requestID string, terminal TerminalResult, terminalErr error) (Result, error) {
	_ = coordinator.advanceIfPossible(ctx, live, b5.TransactionTerminal, b5.PhaseTerminal)
	sequence := live.record.TransactionSeq + 1
	audit, auditErr := coordinator.audit.Barrier(ctx, AuditEvent{Kind: AuditTerminal, TransactionID: live.record.TransactionID, SessionID: live.record.SessionID, RequestID: requestID, Sequence: sequence, PlanDigest: live.plan.Digest, DBOutcome: terminal.Resolution.Outcome, EvidenceDigest: terminal.EvidenceDigest})
	durability := audit.Durability
	if durability == b5.DurabilityUnspecified {
		durability = b5.DurabilityLost
	}
	code, effect := terminalCode(terminal.Resolution.Outcome, durability, terminal.Resolution.Disposition, terminal.Resolution.Consistency.Verdict)
	result := Result{TransactionID: live.record.TransactionID, Status: b5.TransactionTerminal, Phase: b5.PhaseTerminal, Code: code, Effect: effect, DBOutcome: terminal.Resolution.Outcome, AuditDurability: durability, ConnectionDisposition: terminal.Resolution.Disposition}
	live.mu.Lock()
	result.NextOrdinal = live.record.StatementCount
	result.AffectedRows = live.affectedRows
	result.Evidence = append([]OperationEvidence(nil), live.evidence...)
	live.auditDurability = durability
	live.terminalResult = &result
	live.mu.Unlock()
	return result, errors.Join(terminalErr, auditErr)
}

func terminalCode(outcome b5.DBOutcome, durability b5.AuditDurability, disposition b5.ConnectionDisposition, consistency b5.EvidenceConsistency) (b5.ErrorCode, b5.TxEffect) {
	if consistency == b5.EvidenceContradictory || consistency == b5.EvidenceUnknownSchema {
		if outcome == b5.OutcomeUnknown {
			return b5.ErrorTxCommitEvidenceContradiction, b5.EffectTerminalUnknown
		}
		return b5.ErrorTxRollbackEvidenceContradictionNoCommit, b5.EffectTerminalNotCommitted
	}
	if disposition != b5.DispositionReleased {
		switch outcome {
		case b5.OutcomeCommitted:
			return b5.ErrorTxCommittedConnectionQuarantined, b5.EffectTerminalCommitted
		case b5.OutcomeNotCommitted:
			return b5.ErrorTxNotCommittedConnectionQuarantined, b5.EffectTerminalNotCommitted
		default:
			return b5.ErrorTxUnknownConnectionQuarantined, b5.EffectTerminalUnknown
		}
	}
	switch durability {
	case b5.DurabilityAuditPending:
		switch outcome {
		case b5.OutcomeCommitted:
			return b5.ErrorTxCommittedAuditPending, b5.EffectTerminalCommitted
		case b5.OutcomeNotCommitted:
			return b5.ErrorTxNotCommittedAuditPending, b5.EffectTerminalNotCommitted
		default:
			return b5.ErrorTxDBOutcomeUnknownAuditPending, b5.EffectTerminalUnknown
		}
	case b5.DurabilityLost:
		switch outcome {
		case b5.OutcomeCommitted:
			return b5.ErrorTxCommittedAuditDurabilityLost, b5.EffectTerminalCommitted
		case b5.OutcomeNotCommitted:
			return b5.ErrorTxNotCommittedAuditDurabilityLost, b5.EffectTerminalNotCommitted
		default:
			return b5.ErrorTxDBOutcomeUnknownAuditDurabilityLost, b5.EffectTerminalUnknown
		}
	default:
		switch outcome {
		case b5.OutcomeCommitted:
			return b5.ErrorNone, b5.EffectTerminalCommitted
		case b5.OutcomeNotCommitted:
			return b5.ErrorNone, b5.EffectTerminalNotCommitted
		default:
			return b5.ErrorTxDBOutcomeUnknown, b5.EffectTerminalUnknown
		}
	}
}

func (coordinator *Coordinator) waitTerminal(ctx context.Context, live *liveTransaction) (Result, error) {
	_, err := live.terminal.Wait(ctx)
	if err != nil {
		return Result{}, err
	}
	live.mu.Lock()
	defer live.mu.Unlock()
	if live.terminalResult == nil {
		return Result{}, b5terminal.ErrInvalidOwnerState
	}
	return *live.terminalResult, nil
}

// Expire performs the durable deadline scan used by an owner watchdog. It
// never touches a row owned by another epoch because only locally-held live
// capabilities can be terminated; other rows remain for the owner-crash
// reaper/reconciliation path.
func (coordinator *Coordinator) Expire(ctx context.Context, limit int) (int, error) {
	now := coordinator.clock.Now()
	rows, err := coordinator.transactions.ListExpired(ctx, now, limit)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, row := range rows {
		live, ok := coordinator.getLive(row.TransactionID)
		if !ok || live.record.OwnerEpoch != row.OwnerEpoch {
			continue
		}
		code := b5.ErrorTxIdleTimeout
		if !row.WallDeadline.After(now) {
			code = b5.ErrorTxMaxDuration
		} else if row.StatementDeadline != nil && !row.StatementDeadline.After(now) {
			code = b5.ErrorTxStatementTimeout
		}
		_, _ = coordinator.forceRollback(ctx, live, "watchdog:"+row.TransactionID, code, context.DeadlineExceeded)
		count++
	}
	return count, nil
}

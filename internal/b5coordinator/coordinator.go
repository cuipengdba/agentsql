package b5coordinator

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cuipengdba/agentsql/internal/b5"
	"github.com/cuipengdba/agentsql/internal/b5dml"
	"github.com/cuipengdba/agentsql/internal/b5terminal"
	"github.com/cuipengdba/agentsql/internal/store"
)

type Clock interface{ Now() time.Time }
type wallClock struct{}

func (wallClock) Now() time.Time { return time.Now().UTC() }

type Config struct {
	Transactions TransactionStore
	Approvals    ApprovalStore
	Sessions     SessionGate
	Engine       Engine
	Audit        Auditor
	Clock        Clock
}

type Coordinator struct {
	transactions TransactionStore
	approvals    ApprovalStore
	sessions     SessionGate
	engine       Engine
	audit        Auditor
	clock        Clock
	mu           sync.RWMutex
	live         map[string]*liveTransaction
}

type liveTransaction struct {
	mu                  sync.Mutex
	record              store.B5Transaction
	plan                Plan
	approval            ApprovalLease
	terminal            *b5terminal.TerminalOwner
	capability          Capability
	operation           *operation
	operationGeneration uint64
	affectedRows        int64
	evidence            []OperationEvidence
	auditDurability     b5.AuditDurability
	terminalResult      *Result
}

type operation struct {
	generation uint64
	done       chan statementCompletion
	state      atomic.Uint32
}
type statementCompletion struct {
	result StatementResult
	err    error
}

func New(config Config) (*Coordinator, error) {
	if config.Transactions == nil || config.Engine == nil || config.Audit == nil || config.Sessions == nil {
		return nil, errors.New("b5coordinator: incomplete configuration")
	}
	if config.Clock == nil {
		config.Clock = wallClock{}
	}
	return &Coordinator{transactions: config.Transactions, approvals: config.Approvals, sessions: config.Sessions, engine: config.Engine, audit: config.Audit, clock: config.Clock, live: make(map[string]*liveTransaction)}, nil
}

func (coordinator *Coordinator) Begin(ctx context.Context, request BeginRequest, analyzer Analyzer) (Result, error) {
	if ctx == nil || request.TransactionID == "" || request.RequestID == "" || request.Session.SessionID == "" || request.Session.OwnerEpoch == 0 {
		return Result{}, fail(b5.ErrorTxPlanRequired, ErrInvalidPlan)
	}
	if err := coordinator.sessions.Validate(ctx, request.Session); err != nil {
		return Result{}, fail(sessionFailureCode(err), errors.Join(ErrWrongOwner, err))
	}
	plan, err := Preflight(ctx, request.Plan, analyzer)
	if err != nil {
		return Result{}, err
	}
	if plan.RequiresApproval && (request.ApprovalID == "" || coordinator.approvals == nil) {
		return Result{TransactionID: request.TransactionID, Code: b5.ErrorTxApprovalRequired, Effect: b5.EffectNoTxChange}, fail(b5.ErrorTxApprovalRequired, ErrApprovalRequired)
	}

	now := coordinator.clock.Now()
	var approval ApprovalLease
	if plan.RequiresApproval {
		approval, err = coordinator.approvals.Consume(ctx, ApprovalConsumeRequest{ApprovalID: request.ApprovalID, TransactionID: request.TransactionID, RequestID: request.RequestID, PlanDigest: plan.Digest, OwnerEpoch: request.Session.OwnerEpoch, Now: now})
		if err != nil {
			return Result{TransactionID: request.TransactionID, Code: b5.ErrorTxApprovalPlanMismatch, Effect: b5.EffectNoTxChange}, fail(b5.ErrorTxApprovalPlanMismatch, errors.Join(ErrApprovalInvalid, err))
		}
	}

	record := store.B5Transaction{
		TransactionID: request.TransactionID, SessionID: request.Session.SessionID,
		DatasourceID: plan.DatasourceID, Status: b5.TransactionPending, Phase: b5.PhaseReady,
		PlanDigest: append([]byte(nil), plan.Digest[:]...), OwnerEpoch: request.Session.OwnerEpoch,
		IdleDeadline: now.Add(plan.Limits.IdleTimeout), WallDeadline: now.Add(plan.Limits.WallTimeout),
		ConnectionGeneration: 1,
	}
	if request.ApprovalID != "" {
		value := request.ApprovalID
		record.ApprovalID = &value
	}
	record, err = coordinator.transactions.Create(ctx, record)
	if err != nil {
		coordinator.failApproval(ctx, approval, request.TransactionID)
		return Result{}, err
	}
	live := &liveTransaction{record: record, plan: plan, approval: approval, terminal: b5terminal.NewTerminalOwner(record.ConnectionGeneration, request.Session.OwnerEpoch)}
	if err = coordinator.installLive(live); err != nil {
		coordinator.failApproval(ctx, approval, request.TransactionID)
		return Result{}, err
	}
	keep := false
	defer func() {
		if !keep {
			coordinator.removeLive(request.TransactionID)
		}
	}()

	if err = coordinator.advance(ctx, live, b5.TransactionPending, b5.PhasePlanReady); err != nil {
		return coordinator.beginFailure(ctx, live, nil, nil, err)
	}
	if err = coordinator.advance(ctx, live, b5.TransactionPending, b5.PhaseApprovalConsumed); err != nil {
		return coordinator.beginFailure(ctx, live, nil, nil, err)
	}

	begin := b5dml.NewBeginMachine(record.ConnectionGeneration, request.Session.OwnerEpoch, live.terminal)
	pinned, backend, pinErr := coordinator.engine.Pin(ctx, plan, begin)
	if pinErr != nil || pinned == nil {
		return coordinator.beginFailure(ctx, live, begin, pinned, errors.Join(pinErr, errors.New("pin failed")))
	}
	if !begin.PinConnection() {
		return coordinator.beginFailure(ctx, live, begin, pinned, errors.New("pin did not produce a fresh begin resource"))
	}
	if err = coordinator.persistBackend(ctx, live, backend); err != nil {
		return coordinator.beginFailure(ctx, live, begin, pinned, err)
	}
	if err = coordinator.advance(ctx, live, b5.TransactionPending, b5.PhaseConnectionPinned); err != nil {
		return coordinator.beginFailure(ctx, live, begin, pinned, err)
	}

	evidence, beginErr := pinned.BeginNative(ctx)
	if !begin.ObserveBegin(evidence) || begin.ResourceState() != b5dml.BeginNativeBegun || beginErr != nil {
		return coordinator.beginFailure(ctx, live, begin, pinned, errors.Join(beginErr, errors.New("native BEGIN not proven")))
	}
	if err = coordinator.advance(ctx, live, b5.TransactionPending, b5.PhaseNativeBegun); err != nil {
		return coordinator.beginFailure(ctx, live, begin, pinned, err)
	}
	if err = pinned.FixContext(ctx, live.record.WallDeadline); err != nil {
		return coordinator.beginFailure(ctx, live, begin, pinned, err)
	}
	if err = coordinator.advance(ctx, live, b5.TransactionPending, b5.PhaseContextFixed); err != nil {
		return coordinator.beginFailure(ctx, live, begin, pinned, err)
	}
	sealedDigest, sealErr := pinned.BindAndSeal(ctx, plan)
	if sealErr != nil || subtle.ConstantTimeCompare(sealedDigest[:], plan.Digest[:]) != 1 {
		return coordinator.beginFailure(ctx, live, begin, pinned, errors.Join(sealErr, errors.New("sealed manifest mismatch")))
	}
	capability, activateErr := pinned.Activate()
	if activateErr != nil || capability == nil {
		return coordinator.beginFailure(ctx, live, begin, pinned, errors.Join(activateErr, ErrCapabilityUnknown))
	}
	live.capability = capability
	if err = coordinator.advance(ctx, live, b5.TransactionPending, b5.PhaseSealedInTx); err != nil {
		return coordinator.beginFailure(ctx, live, begin, pinned, err)
	}
	if err = coordinator.advance(ctx, live, b5.TransactionPending, b5.PhaseBeginAuditing); err != nil {
		return coordinator.beginFailure(ctx, live, begin, pinned, err)
	}
	audit, auditErr := coordinator.audit.Barrier(ctx, AuditEvent{Kind: AuditTxBegin, TransactionID: request.TransactionID, SessionID: request.Session.SessionID, RequestID: request.RequestID, Sequence: 1, PlanDigest: plan.Digest})
	if auditErr != nil || audit.Durability != b5.DurabilityDurable {
		return coordinator.beginFailure(ctx, live, begin, pinned, errors.Join(auditErr, errors.New("tx_begin barrier not durable")))
	}
	live.auditDurability = audit.Durability
	beginSequence := uint64(1)
	beginDigest := append([]byte(nil), audit.EventDigest[:]...)
	if next, progressErr := coordinator.transactions.CASProgress(ctx, live.record.TransactionID, live.record.Revision, live.record.Status, live.record.Phase, store.B5TransactionProgress{TransactionSeq: &beginSequence, PreviousEventDigest: &beginDigest}); progressErr != nil {
		return coordinator.beginFailure(ctx, live, begin, pinned, progressErr)
	} else {
		live.record = next
	}
	if err = coordinator.advance(ctx, live, b5.TransactionActive, b5.PhaseActive); err != nil {
		return coordinator.beginFailure(ctx, live, begin, pinned, err)
	}
	if plan.RequiresApproval {
		if err = coordinator.approvals.MarkActive(ctx, approval, request.TransactionID); err != nil {
			return coordinator.forceRollback(ctx, live, request.RequestID, b5.ErrorTxApprovalConsumedBeginFailed, err)
		}
	}
	keep = true
	return coordinator.snapshot(live, b5.ErrorNone, b5.EffectKeepActive), nil
}

func (coordinator *Coordinator) installLive(live *liveTransaction) error {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if _, exists := coordinator.live[live.record.TransactionID]; exists {
		return ErrBusy
	}
	coordinator.live[live.record.TransactionID] = live
	return nil
}
func (coordinator *Coordinator) removeLive(id string) {
	coordinator.mu.Lock()
	delete(coordinator.live, id)
	coordinator.mu.Unlock()
}
func (coordinator *Coordinator) getLive(id string) (*liveTransaction, bool) {
	coordinator.mu.RLock()
	value, ok := coordinator.live[id]
	coordinator.mu.RUnlock()
	return value, ok
}

func (coordinator *Coordinator) advance(ctx context.Context, live *liveTransaction, status b5.TransactionStatus, phase b5.TransactionPhase) error {
	live.mu.Lock()
	defer live.mu.Unlock()
	next, err := coordinator.transactions.CASState(ctx, live.record.TransactionID, live.record.Revision, live.record.Status, live.record.Phase, status, phase)
	if err == nil {
		live.record = next
	}
	return err
}

func (coordinator *Coordinator) persistBackend(ctx context.Context, live *liveTransaction, backend BackendIdentity) error {
	live.mu.Lock()
	defer live.mu.Unlock()
	pid := backend.PID
	started := backend.StartedAt
	digest := append([]byte(nil), backend.SecretDigest[:]...)
	pidPointer, startedPointer := &pid, &started
	connection, lease := backend.ConnectionGeneration, backend.LeaseGeneration
	if connection == 0 {
		connection = live.record.ConnectionGeneration
	}
	next, err := coordinator.transactions.CASProgress(ctx, live.record.TransactionID, live.record.Revision, live.record.Status, live.record.Phase, store.B5TransactionProgress{BackendPID: &pidPointer, BackendSecretDigest: &digest, BackendStartedAt: &startedPointer, ConnectionGeneration: &connection, LeaseGeneration: &lease})
	if err == nil {
		live.record = next
	}
	return err
}

func (coordinator *Coordinator) beginFailure(ctx context.Context, live *liveTransaction, begin *b5dml.BeginMachine, pinned BeginCapability, cause error) (Result, error) {
	_ = coordinator.advanceIfPossible(ctx, live, b5.TransactionTerminal, b5.PhaseBeginFailTerminating)
	var terminal TerminalResult
	if begin != nil {
		decision := begin.Cleanup(b5terminal.ReleaseChecks{}, false)
		if pinned != nil {
			terminal, _ = pinned.CleanupBegin(ctx, live.terminal, decision)
		}
	}
	_ = coordinator.advanceIfPossible(ctx, live, b5.TransactionTerminal, b5.PhaseTerminal)
	coordinator.failApproval(ctx, live.approval, live.record.TransactionID)
	result := coordinator.snapshot(live, b5.ErrorTxApprovalConsumedBeginFailed, b5.EffectTerminalNotCommitted)
	result.DBOutcome = b5.OutcomeNotCommitted
	result.ConnectionDisposition = terminal.Resolution.Disposition
	if result.ConnectionDisposition == b5.DispositionUnknown && begin != nil && begin.ResourceState() == b5dml.BeginACKLostOrIndeterminate {
		result.ConnectionDisposition = b5.DispositionDiscardUnconfirmed
	}
	return result, fail(b5.ErrorTxApprovalConsumedBeginFailed, cause)
}

func (coordinator *Coordinator) advanceIfPossible(ctx context.Context, live *liveTransaction, status b5.TransactionStatus, phase b5.TransactionPhase) error {
	err := coordinator.advance(ctx, live, status, phase)
	if errors.Is(err, store.ErrB5InvalidTransition) || errors.Is(err, store.ErrB5CASConflict) {
		return err
	}
	return err
}

func (coordinator *Coordinator) failApproval(ctx context.Context, approval ApprovalLease, transactionID string) {
	if approval.ID != "" && coordinator.approvals != nil {
		_ = coordinator.approvals.MarkBeginFailed(ctx, approval, transactionID)
	}
}

func (coordinator *Coordinator) validateLive(ctx context.Context, session SessionAuthorization, transactionID string) (*liveTransaction, error) {
	if err := coordinator.sessions.Validate(ctx, session); err != nil {
		return nil, fail(sessionFailureCode(err), errors.Join(ErrWrongOwner, err))
	}
	live, ok := coordinator.getLive(transactionID)
	if !ok {
		return nil, fail(b5.ErrorTxNotActive, ErrTerminal)
	}
	live.mu.Lock()
	defer live.mu.Unlock()
	if live.record.SessionID != session.SessionID || live.record.OwnerEpoch != session.OwnerEpoch {
		return nil, fail(b5.ErrorSessionOwnerEpochStale, ErrWrongOwner)
	}
	if len(live.record.PlanDigest) != 32 || subtle.ConstantTimeCompare(live.record.PlanDigest, live.plan.Digest[:]) != 1 {
		return nil, fail(b5.ErrorTxActivePlanMismatch, ErrInvalidPlan)
	}
	return live, nil
}

func (coordinator *Coordinator) snapshot(live *liveTransaction, code b5.ErrorCode, effect b5.TxEffect) Result {
	live.mu.Lock()
	defer live.mu.Unlock()
	result := Result{TransactionID: live.record.TransactionID, Status: live.record.Status, Phase: live.record.Phase, Code: code, Effect: effect, NextOrdinal: live.record.StatementCount, AffectedRows: live.affectedRows, AuditDurability: live.auditDurability, Evidence: append([]OperationEvidence(nil), live.evidence...)}
	if live.terminalResult != nil {
		terminal := *live.terminalResult
		terminal.Code = code
		terminal.Effect = effect
		terminal.Evidence = result.Evidence
		terminal.NextOrdinal = result.NextOrdinal
		terminal.AffectedRows = result.AffectedRows
		return terminal
	}
	return result
}

func beginAuthorizationDigest(plan [32]byte, approval ApprovalLease, ownerEpoch uint64, transactionID, requestID string) [32]byte {
	h := sha256.New()
	h.Write([]byte(BeginAuthSchemaID))
	h.Write(plan[:])
	h.Write(approval.ApprovedBoundsDigest[:])
	writeHashUint(h, approval.Generation)
	writeHashUint(h, ownerEpoch)
	h.Write([]byte(transactionID))
	h.Write([]byte(requestID))
	var result [32]byte
	copy(result[:], h.Sum(nil))
	return result
}

type hashWriter interface{ Write([]byte) (int, error) }

func writeHashUint(writer hashWriter, value uint64) {
	var data [8]byte
	for index := 7; index >= 0; index-- {
		data[index] = byte(value)
		value >>= 8
	}
	_, _ = writer.Write(data[:])
}

func (coordinator *Coordinator) Status(ctx context.Context, session SessionAuthorization, transactionID string) (Result, error) {
	live, err := coordinator.validateLive(ctx, session, transactionID)
	if err != nil {
		return Result{}, err
	}
	return coordinator.snapshot(live, b5.ErrorNone, b5.EffectKeepActive), nil
}

func (coordinator *Coordinator) Recover(ctx context.Context, transactionID string) (Result, error) {
	record, err := coordinator.transactions.Get(ctx, transactionID)
	if err != nil {
		return Result{}, err
	}
	// A process restart cannot reconstruct a live physical capability. Pending
	// and active rows therefore remain reconciliation-only and are never
	// replayed. COMMITTING is especially never retried.
	result := Result{TransactionID: record.TransactionID, Status: record.Status, Phase: record.Phase, NextOrdinal: record.StatementCount}
	switch record.Phase {
	case b5.PhaseCommitting:
		result.Code, result.Effect, result.DBOutcome = b5.ErrorTxDBOutcomeUnknown, b5.EffectTerminalUnknown, b5.OutcomeUnknown
	case b5.PhaseTerminal, b5.PhaseFinalFence:
		result.Effect = b5.EffectFenceOnly
	default:
		result.Code, result.Effect = b5.ErrorSessionOwnerLost, b5.EffectSessionTerminal
	}
	return result, nil
}

func (coordinator *Coordinator) String() string {
	return fmt.Sprintf("b5coordinator(flag-off,live=%d)", coordinator.liveCount())
}
func (coordinator *Coordinator) liveCount() int {
	coordinator.mu.RLock()
	defer coordinator.mu.RUnlock()
	return len(coordinator.live)
}

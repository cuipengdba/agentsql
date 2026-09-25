package b5coordinator

import (
	"context"
	"crypto/sha256"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/b5"
	"github.com/cuipengdba/agentsql/internal/b5dml"
	"github.com/cuipengdba/agentsql/internal/b5terminal"
	"github.com/cuipengdba/agentsql/internal/store"
)

type testAnalyzer struct {
	decisions map[string]Decision
	calls     atomic.Int32
}

type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (clock *testClock) Now() time.Time { clock.mu.Lock(); defer clock.mu.Unlock(); return clock.now }
func (clock *testClock) Advance(value time.Duration) {
	clock.mu.Lock()
	clock.now = clock.now.Add(value)
	clock.mu.Unlock()
}

func (analyzer *testAnalyzer) Analyze(_ context.Context, statement StatementRequest, ordinal int) (Analysis, error) {
	analyzer.calls.Add(1)
	decision := DecisionAllow
	if value, ok := analyzer.decisions[statement.OperationID]; ok {
		decision = value
	}
	relation := b5dml.RelationIdentity{DatasourceID: "ds", DatabaseOID: 1, RelationOID: 42, RelationKind: 'r', Schema: "public", Name: "items", CatalogFingerprint: "catalog"}
	column := b5dml.ColumnIdentity{Relation: relation, Attnum: 2, Name: "value", TypeOID: 23, TypeModifier: -1}
	tree := sha256.Sum256([]byte("tree:" + statement.OperationID))
	manifest := sha256.Sum256([]byte("manifest:" + statement.OperationID))
	closure := sha256.Sum256([]byte("closure:" + statement.OperationID))
	return Analysis{Facts: b5dml.StatementFacts{Dialect: b5dml.DialectPostgreSQL, Action: b5dml.ActionUpdate, Shape: b5dml.ShapeSimple, Target: relation, Writes: []b5dml.WriteTarget{b5dml.ColumnWrite(column, b5dml.WriteSourceExplicit)}}, TreeDigest: tree, ManifestDigest: manifest, ClosureDigest: closure, Decision: decision, EstimatedRows: 1, EstimatedWork: uint64(ordinal + 1), Artifact: statement.OperationID}, nil
}

type testEngine struct {
	pins       atomic.Int32
	capability *testCapability
	pinErr     error
}

func (engine *testEngine) Pin(_ context.Context, _ Plan, _ *b5dml.BeginMachine) (BeginCapability, BackendIdentity, error) {
	engine.pins.Add(1)
	if engine.pinErr != nil {
		return nil, BackendIdentity{}, engine.pinErr
	}
	return engine.capability, BackendIdentity{PID: 123, StartedAt: time.Now().UTC(), ConnectionGeneration: 1, LeaseGeneration: 1, SecretDigest: sha256.Sum256([]byte("secret"))}, nil
}

type testCapability struct {
	mu                                     sync.Mutex
	planDigest                             [32]byte
	executeGate                            chan struct{}
	cancelClosesGate                       bool
	cancelEmission                         b5terminal.CancelEmission
	executeDecision                        Decision
	executeCertain                         bool
	executeErr                             error
	commitResolution                       b5terminal.TerminalResolution
	rollbackResolution                     b5terminal.TerminalResolution
	pendingEffects, businessEffects        int64
	executes, rollbacks, commits, discards int
}

func newTestCapability() *testCapability {
	return &testCapability{executeCertain: true, executeDecision: DecisionAllow, cancelEmission: b5terminal.CancelNotSentProven, commitResolution: b5terminal.TerminalResolution{Schema: b5terminal.ConnectionDispositionSchema, SchemaVersion: b5terminal.ConnectionDispositionVersion, Consistency: b5terminal.ConsistencyResult{Verdict: b5terminal.VerdictConsistent}, Outcome: b5.OutcomeCommitted, Disposition: b5.DispositionReleased}, rollbackResolution: b5terminal.TerminalResolution{Schema: b5terminal.ConnectionDispositionSchema, SchemaVersion: b5terminal.ConnectionDispositionVersion, Consistency: b5terminal.ConsistencyResult{Verdict: b5terminal.VerdictConsistent}, Outcome: b5.OutcomeNotCommitted, Disposition: b5.DispositionReleased}}
}
func (capability *testCapability) BeginNative(context.Context) (b5dml.BeginAttemptEvidence, error) {
	return b5dml.BeginAttemptEvidence{Write: b5terminal.WriteEvidence{Phase: b5terminal.WriteFullFrame, BytesWritten: 1, FrameBytes: 1}, Reply: b5dml.BeginReplyPositiveACK, Correlation: b5terminal.CorrelationStrongCurrentOperation, ServerStatus: b5terminal.ServerReadyIdleInTransaction}, nil
}
func (*testCapability) FixContext(context.Context, time.Time) error { return nil }
func (capability *testCapability) BindAndSeal(_ context.Context, plan Plan) ([32]byte, error) {
	capability.planDigest = plan.Digest
	return plan.Digest, nil
}
func (capability *testCapability) Activate() (Capability, error) { return capability, nil }
func (capability *testCapability) Execute(ctx context.Context, _ PlannedStatement) (StatementResult, error) {
	capability.mu.Lock()
	capability.executes++
	gate := capability.executeGate
	capability.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return StatementResult{}, ctx.Err()
		}
	}
	capability.mu.Lock()
	defer capability.mu.Unlock()
	if capability.executeErr == nil {
		capability.pendingEffects++
	}
	return StatementResult{AffectedRows: 1, EvidenceDigest: sha256.Sum256([]byte("statement")), Decision: capability.executeDecision, CapabilityCertain: capability.executeCertain}, capability.executeErr
}
func (capability *testCapability) Cancel(context.Context) (b5terminal.CancelEmission, error) {
	capability.mu.Lock()
	defer capability.mu.Unlock()
	if capability.cancelClosesGate && capability.executeGate != nil {
		close(capability.executeGate)
		capability.executeGate = nil
	}
	return capability.cancelEmission, nil
}
func (capability *testCapability) FinishCommit(_ context.Context, owner *b5terminal.TerminalOwner, attempt b5terminal.TerminalAttempt) (TerminalResult, error) {
	capability.mu.Lock()
	capability.commits++
	capability.businessEffects += capability.pendingEffects
	capability.pendingEffects = 0
	resolution := capability.commitResolution
	capability.mu.Unlock()
	_ = owner.OpenCommitSendPermit(attempt)
	_ = owner.Complete(attempt, resolution)
	return TerminalResult{Resolution: resolution, EvidenceDigest: sha256.Sum256([]byte("commit"))}, nil
}
func (capability *testCapability) FinishRollback(_ context.Context, owner *b5terminal.TerminalOwner, attempt b5terminal.TerminalAttempt, _ b5terminal.NoCommitEverSent) (TerminalResult, error) {
	capability.mu.Lock()
	capability.rollbacks++
	capability.pendingEffects = 0
	resolution := capability.rollbackResolution
	capability.mu.Unlock()
	_ = owner.Complete(attempt, resolution)
	return TerminalResult{Resolution: resolution, EvidenceDigest: sha256.Sum256([]byte("rollback"))}, nil
}
func (capability *testCapability) Discard(context.Context) (b5.ConnectionDisposition, error) {
	capability.mu.Lock()
	defer capability.mu.Unlock()
	capability.discards++
	capability.pendingEffects = 0
	return b5.DispositionDiscarded, nil
}
func (capability *testCapability) CleanupBegin(ctx context.Context, owner *b5terminal.TerminalOwner, decision b5dml.BeginCleanupDecision) (TerminalResult, error) {
	if decision.TerminalOwned {
		return capability.FinishRollback(ctx, owner, decision.Attempt, b5terminal.NoCommitEverSent{Schema: b5terminal.NoCommitEverSentSchema, SchemaVersion: b5terminal.NoCommitEverSentVersion, TransactionGeneration: decision.Attempt.TransactionGeneration, OwnerGeneration: decision.Attempt.OwnerGeneration, HistoryContinuous: true, UniqueSocketOwner: true})
	}
	disposition, err := capability.Discard(ctx)
	return TerminalResult{Resolution: b5terminal.TerminalResolution{Outcome: b5.OutcomeNotCommitted, Disposition: disposition}}, err
}

func testPlan(statements ...string) PlanRequest {
	requests := make([]StatementRequest, len(statements))
	for index, sql := range statements {
		requests[index] = StatementRequest{OperationID: string(rune('a' + index)), SQL: sql, Reason: "test"}
	}
	limits := DefaultResourceLimits()
	limits.OperationWatchdog = time.Second
	return PlanRequest{TenantID: "tenant", PrincipalID: "principal", AgentID: "agent", DatasourceID: "ds", KeyRevision: 1, DatasourceRevision: 1, PolicyRevision: 1, Dialect: "postgres", ServerMajor: 18, Isolation: "serializable", BinderABI: b5dml.BinderABI, ClosurePolicy: "closed-v1", Statements: requests, Limits: limits}
}
func newTestCoordinator(t *testing.T, capability *testCapability, audit *MemoryAuditor, approvals *MemoryApprovalStore) (*Coordinator, *MemoryTransactionStore) {
	t.Helper()
	transactions := NewMemoryTransactionStore()
	coordinator, err := New(Config{Transactions: transactions, Approvals: approvals, Sessions: StaticSessionGate{SessionID: "session", OwnerEpoch: 7}, Engine: &testEngine{capability: capability}, Audit: audit})
	if err != nil {
		t.Fatal(err)
	}
	return coordinator, transactions
}
func beginTest(t *testing.T, coordinator *Coordinator, analyzer Analyzer, plan PlanRequest, approval string) Result {
	t.Helper()
	result, err := coordinator.Begin(context.Background(), BeginRequest{Session: SessionAuthorization{SessionID: "session", OwnerEpoch: 7}, TransactionID: "tx", RequestID: "begin", ApprovalID: approval, Plan: plan}, analyzer)
	if err != nil {
		t.Fatalf("begin: %v result=%+v", err, result)
	}
	return result
}

func TestPreflightRejectsEntireIllegalPlanBeforePin(t *testing.T) {
	illegal := []struct {
		sql  string
		code b5.ErrorCode
	}{{"SELECT 1", b5.ErrorTxSelectUnsupported}, {"UPDATE items SET value=1; DELETE FROM items", b5.ErrorTxControlStatementDenied}, {"BEGIN", b5.ErrorTxControlStatementDenied}, {"SAVEPOINT x", b5.ErrorTxControlStatementDenied}, {"SET search_path=public", b5.ErrorTxControlStatementDenied}, {"PREPARE x AS SELECT 1", b5.ErrorTxControlStatementDenied}, {"CREATE TABLE x(id int)", b5.ErrorTxControlStatementDenied}}
	for _, test := range illegal {
		t.Run(string(test.code)+test.sql, func(t *testing.T) {
			capability := newTestCapability()
			engine := &testEngine{capability: capability}
			coordinator, err := New(Config{Transactions: NewMemoryTransactionStore(), Sessions: StaticSessionGate{SessionID: "session", OwnerEpoch: 7}, Engine: engine, Audit: &MemoryAuditor{}})
			if err != nil {
				t.Fatal(err)
			}
			_, err = coordinator.Begin(context.Background(), BeginRequest{Session: SessionAuthorization{SessionID: "session", OwnerEpoch: 7}, TransactionID: "tx", RequestID: "begin", Plan: testPlan("UPDATE items SET value=1", test.sql)}, &testAnalyzer{})
			if ErrorCode(err) != test.code {
				t.Fatalf("code=%s err=%v", ErrorCode(err), err)
			}
			if engine.pins.Load() != 0 {
				t.Fatal("illegal plan pinned a connection")
			}
		})
	}
}

func TestApprovalMustBeConsumedBeforePinAndNeverRefunds(t *testing.T) {
	capability := newTestCapability()
	engine := &testEngine{capability: capability, pinErr: errors.New("pin fault")}
	approvals := NewMemoryApprovalStore()
	auditor := &MemoryAuditor{}
	coordinator, err := New(Config{Transactions: NewMemoryTransactionStore(), Approvals: approvals, Sessions: StaticSessionGate{SessionID: "session", OwnerEpoch: 7}, Engine: engine, Audit: auditor})
	if err != nil {
		t.Fatal(err)
	}
	plan := testPlan("UPDATE items SET value=1")
	analyzer := &testAnalyzer{decisions: map[string]Decision{"a": DecisionApprove}}
	preflight, err := Preflight(context.Background(), plan, analyzer)
	if err != nil {
		t.Fatal(err)
	}
	_, err = coordinator.Begin(context.Background(), BeginRequest{Session: SessionAuthorization{SessionID: "session", OwnerEpoch: 7}, TransactionID: "missing", RequestID: "begin", Plan: plan}, analyzer)
	if ErrorCode(err) != b5.ErrorTxApprovalRequired || engine.pins.Load() != 0 {
		t.Fatalf("missing approval err=%v pins=%d", err, engine.pins.Load())
	}
	approvals.Put(MemoryApproval{ID: "approval", PlanDigest: preflight.Digest, ExpiresAt: time.Now().Add(time.Minute)})
	_, err = coordinator.Begin(context.Background(), BeginRequest{Session: SessionAuthorization{SessionID: "session", OwnerEpoch: 7}, TransactionID: "tx", RequestID: "begin", ApprovalID: "approval", Plan: plan}, analyzer)
	if err == nil {
		t.Fatal("pin fault was accepted")
	}
	stored, _ := approvals.Get("approval")
	if stored.Status != "consumed_begin_failed" {
		t.Fatalf("approval status=%s", stored.Status)
	}
}

func TestSequentialPlanCommit(t *testing.T) {
	capability := newTestCapability()
	coordinator, _ := newTestCoordinator(t, capability, &MemoryAuditor{}, nil)
	begin := beginTest(t, coordinator, &testAnalyzer{}, testPlan("UPDATE items SET value=1", "UPDATE items SET value=2"), "")
	if begin.NextOrdinal != 0 || begin.Phase != b5.PhaseActive {
		t.Fatalf("begin=%+v", begin)
	}
	for ordinal, id := range []string{"a", "b"} {
		result, err := coordinator.Execute(context.Background(), ExecuteRequest{Session: SessionAuthorization{SessionID: "session", OwnerEpoch: 7}, TransactionID: "tx", RequestID: id, OperationID: id, Ordinal: ordinal})
		if err != nil || result.NextOrdinal != ordinal+1 {
			t.Fatalf("execute %d result=%+v err=%v", ordinal, result, err)
		}
	}
	result, err := coordinator.Commit(context.Background(), FinishRequest{Session: SessionAuthorization{SessionID: "session", OwnerEpoch: 7}, TransactionID: "tx", RequestID: "commit"})
	if err != nil || result.DBOutcome != b5.OutcomeCommitted || result.Effect != b5.EffectTerminalCommitted || capability.businessEffects != 2 {
		t.Fatalf("commit=%+v err=%v effects=%d", result, err, capability.businessEffects)
	}
}

func TestStatementAuditFailureRollsBackAllEffects(t *testing.T) {
	capability := newTestCapability()
	audit := &MemoryAuditor{Fail: func(event AuditEvent) error {
		if event.Kind == AuditStatement {
			return errors.New("audit down")
		}
		return nil
	}}
	coordinator, _ := newTestCoordinator(t, capability, audit, nil)
	beginTest(t, coordinator, &testAnalyzer{}, testPlan("UPDATE items SET value=1"), "")
	result, err := coordinator.Execute(context.Background(), ExecuteRequest{Session: SessionAuthorization{SessionID: "session", OwnerEpoch: 7}, TransactionID: "tx", RequestID: "a", OperationID: "a", Ordinal: 0})
	if ErrorCode(err) != b5.ErrorAuditStatementBarrierFailed || result.DBOutcome != b5.OutcomeNotCommitted || capability.businessEffects != 0 || capability.rollbacks != 1 {
		t.Fatalf("result=%+v err=%v capability=%+v", result, err, capability)
	}
}

func TestStatementTimeoutCancelTaintsAndDiscards(t *testing.T) {
	capability := newTestCapability()
	capability.executeGate = make(chan struct{})
	capability.cancelEmission = b5terminal.CancelFullPacketWritten
	coordinator, _ := newTestCoordinator(t, capability, &MemoryAuditor{}, nil)
	plan := testPlan("UPDATE items SET value=1")
	plan.Limits.StatementTimeout = 20 * time.Millisecond
	plan.Limits.OperationWatchdog = time.Second
	beginTest(t, coordinator, &testAnalyzer{}, plan, "")
	result, err := coordinator.Execute(context.Background(), ExecuteRequest{Session: SessionAuthorization{SessionID: "session", OwnerEpoch: 7}, TransactionID: "tx", RequestID: "a", OperationID: "a", Ordinal: 0})
	if err == nil || result.DBOutcome != b5.OutcomeNotCommitted || result.ConnectionDisposition != b5.DispositionDiscarded || capability.rollbacks != 0 || capability.discards != 1 {
		t.Fatalf("result=%+v err=%v rollback=%d discard=%d", result, err, capability.rollbacks, capability.discards)
	}
	close(capability.executeGate)
}

func TestOperationWatchdogQuiescesThenUsesSingleTypedRollback(t *testing.T) {
	capability := newTestCapability()
	capability.executeGate = make(chan struct{})
	capability.cancelClosesGate = true
	coordinator, _ := newTestCoordinator(t, capability, &MemoryAuditor{}, nil)
	plan := testPlan("UPDATE items SET value=1")
	plan.Limits.OperationWatchdog = 20 * time.Millisecond
	plan.Limits.StatementTimeout = time.Second
	beginTest(t, coordinator, &testAnalyzer{}, plan, "")
	result, err := coordinator.Execute(context.Background(), ExecuteRequest{Session: SessionAuthorization{SessionID: "session", OwnerEpoch: 7}, TransactionID: "tx", RequestID: "a", OperationID: "a", Ordinal: 0})
	if ErrorCode(err) != b5.ErrorTxOperationWatchdog || result.DBOutcome != b5.OutcomeNotCommitted || capability.rollbacks != 1 || capability.discards != 0 {
		t.Fatalf("result=%+v err=%v rollback=%d discard=%d", result, err, capability.rollbacks, capability.discards)
	}
}

func TestIdleAndWallDeadlineWatchdogs(t *testing.T) {
	for _, test := range []struct {
		name    string
		advance time.Duration
	}{
		{name: "idle", advance: 16 * time.Second},
		{name: "wall", advance: 61 * time.Second},
	} {
		t.Run(test.name, func(t *testing.T) {
			clock := &testClock{now: time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)}
			capability := newTestCapability()
			transactions := NewMemoryTransactionStore()
			coordinator, err := New(Config{Transactions: transactions, Sessions: StaticSessionGate{SessionID: "session", OwnerEpoch: 7}, Engine: &testEngine{capability: capability}, Audit: &MemoryAuditor{}, Clock: clock})
			if err != nil {
				t.Fatal(err)
			}
			beginTest(t, coordinator, &testAnalyzer{}, testPlan("UPDATE items SET value=1"), "")
			clock.Advance(test.advance)
			count, err := coordinator.Expire(context.Background(), 10)
			if err != nil || count != 1 || capability.rollbacks != 1 {
				t.Fatalf("count=%d err=%v rollbacks=%d", count, err, capability.rollbacks)
			}
			stored, err := transactions.Get(context.Background(), "tx")
			if err != nil || stored.Phase != b5.PhaseTerminal {
				t.Fatalf("stored=%+v err=%v", stored, err)
			}
		})
	}
}

func TestRuntimeDenyAndCommitIntentAuditFailureRollback(t *testing.T) {
	for _, test := range []struct {
		name      string
		configure func(*testCapability, *MemoryAuditor)
	}{
		{name: "runtime deny", configure: func(capability *testCapability, _ *MemoryAuditor) { capability.executeDecision = DecisionDeny }},
		{name: "commit intent audit", configure: func(_ *testCapability, audit *MemoryAuditor) {
			audit.Fail = func(event AuditEvent) error {
				if event.Kind == AuditCommitIntent {
					return errors.New("intent outage")
				}
				return nil
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			capability := newTestCapability()
			audit := &MemoryAuditor{}
			test.configure(capability, audit)
			coordinator, _ := newTestCoordinator(t, capability, audit, nil)
			beginTest(t, coordinator, &testAnalyzer{}, testPlan("UPDATE items SET value=1"), "")
			_, executeErr := coordinator.Execute(context.Background(), ExecuteRequest{Session: SessionAuthorization{SessionID: "session", OwnerEpoch: 7}, TransactionID: "tx", RequestID: "a", OperationID: "a", Ordinal: 0})
			if test.name == "runtime deny" {
				if executeErr == nil || capability.rollbacks != 1 || capability.businessEffects != 0 {
					t.Fatalf("execute err=%v rollback=%d effects=%d", executeErr, capability.rollbacks, capability.businessEffects)
				}
				return
			}
			if executeErr != nil {
				t.Fatal(executeErr)
			}
			result, commitErr := coordinator.Commit(context.Background(), FinishRequest{Session: SessionAuthorization{SessionID: "session", OwnerEpoch: 7}, TransactionID: "tx", RequestID: "commit"})
			if ErrorCode(commitErr) != b5.ErrorAuditCommitIntentFailed || result.DBOutcome != b5.OutcomeNotCommitted || capability.rollbacks != 1 || capability.commits != 0 {
				t.Fatalf("result=%+v err=%v rollback=%d commit=%d", result, commitErr, capability.rollbacks, capability.commits)
			}
		})
	}
}

func TestRecoverCommittingNeverRetriesCommit(t *testing.T) {
	transactions := NewMemoryTransactionStore()
	now := time.Now().UTC()
	_, err := transactions.Create(context.Background(), store.B5Transaction{TransactionID: "commit-crash", SessionID: "session", DatasourceID: "ds", Status: b5.TransactionActive, Phase: b5.PhaseCommitting, PlanDigest: make([]byte, 32), OwnerEpoch: 7, IdleDeadline: now.Add(time.Second), WallDeadline: now.Add(time.Minute), ConnectionGeneration: 1})
	if err != nil {
		t.Fatal(err)
	}
	capability := newTestCapability()
	coordinator, err := New(Config{Transactions: transactions, Sessions: StaticSessionGate{SessionID: "session", OwnerEpoch: 7}, Engine: &testEngine{capability: capability}, Audit: &MemoryAuditor{}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := coordinator.Recover(context.Background(), "commit-crash")
	if err != nil || result.DBOutcome != b5.OutcomeUnknown || result.Effect != b5.EffectTerminalUnknown || capability.commits != 0 {
		t.Fatalf("result=%+v err=%v commits=%d", result, err, capability.commits)
	}
}

func TestConcurrentOperationAndWrongOwnerCannotTouchCapability(t *testing.T) {
	capability := newTestCapability()
	capability.executeGate = make(chan struct{})
	coordinator, _ := newTestCoordinator(t, capability, &MemoryAuditor{}, nil)
	plan := testPlan("UPDATE items SET value=1")
	plan.Limits.OperationWatchdog = time.Second
	beginTest(t, coordinator, &testAnalyzer{}, plan, "")
	done := make(chan error, 1)
	go func() {
		_, err := coordinator.Execute(context.Background(), ExecuteRequest{Session: SessionAuthorization{SessionID: "session", OwnerEpoch: 7}, TransactionID: "tx", RequestID: "a", OperationID: "a", Ordinal: 0})
		done <- err
	}()
	time.Sleep(10 * time.Millisecond)
	_, err := coordinator.Execute(context.Background(), ExecuteRequest{Session: SessionAuthorization{SessionID: "session", OwnerEpoch: 7}, TransactionID: "tx", RequestID: "b", OperationID: "a", Ordinal: 0})
	if ErrorCode(err) != b5.ErrorSessionBusy {
		t.Fatalf("busy err=%v", err)
	}
	_, err = coordinator.Status(context.Background(), SessionAuthorization{SessionID: "session", OwnerEpoch: 8}, "tx")
	if ErrorCode(err) != b5.ErrorSessionNotFoundOrDenied {
		t.Fatalf("wrong owner err=%v", err)
	}
	close(capability.executeGate)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if capability.executes != 1 {
		t.Fatalf("executes=%d", capability.executes)
	}
}

func TestCommitUnknownIsReconciliationOnly(t *testing.T) {
	capability := newTestCapability()
	capability.commitResolution = b5terminal.TerminalResolution{Schema: b5terminal.ConnectionDispositionSchema, SchemaVersion: b5terminal.ConnectionDispositionVersion, Consistency: b5terminal.ConsistencyResult{Verdict: b5terminal.VerdictConsistent}, Outcome: b5.OutcomeUnknown, Disposition: b5.DispositionDiscarded}
	coordinator, transactions := newTestCoordinator(t, capability, &MemoryAuditor{}, nil)
	beginTest(t, coordinator, &testAnalyzer{}, testPlan("UPDATE items SET value=1"), "")
	_, err := coordinator.Execute(context.Background(), ExecuteRequest{Session: SessionAuthorization{SessionID: "session", OwnerEpoch: 7}, TransactionID: "tx", RequestID: "a", OperationID: "a", Ordinal: 0})
	if err != nil {
		t.Fatal(err)
	}
	result, _ := coordinator.Commit(context.Background(), FinishRequest{Session: SessionAuthorization{SessionID: "session", OwnerEpoch: 7}, TransactionID: "tx", RequestID: "commit"})
	if result.DBOutcome != b5.OutcomeUnknown || result.Effect != b5.EffectTerminalUnknown || capability.commits != 1 {
		t.Fatalf("result=%+v commits=%d", result, capability.commits)
	}
	recovered, err := New(Config{Transactions: transactions, Sessions: StaticSessionGate{SessionID: "session", OwnerEpoch: 7}, Engine: &testEngine{capability: newTestCapability()}, Audit: &MemoryAuditor{}})
	if err != nil {
		t.Fatal(err)
	}
	status, err := recovered.Recover(context.Background(), "tx")
	if err != nil || status.Phase != b5.PhaseTerminal {
		t.Fatalf("recover=%+v err=%v", status, err)
	}
}

package businessdb

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/cuipengdba/agentsql/internal/b5"
	"github.com/cuipengdba/agentsql/internal/b5coordinator"
	"github.com/cuipengdba/agentsql/internal/b5dml"
	"github.com/cuipengdba/agentsql/internal/b5terminal"
	"github.com/jackc/pgx/v5"
)

// B5PostgresAuthorization contains the non-database half of the DML lattice.
// Approval is deliberately not represented: it cannot fill a missing DML
// grant and is consumed by the coordinator before this adapter can pin.
type B5PostgresAuthorization struct {
	Policies             []b5dml.Policy
	Decision             b5coordinator.Decision
	PreliminaryAllowed   bool
	DatasourceSupported  bool
	ReservedTarget       bool
	PolicySnapshotDigest string
}

type B5PostgresAuthorizer interface {
	AuthorizeCandidate(context.Context, PostgresDMLEnrollment, b5coordinator.StatementRequest, int) (B5PostgresAuthorization, error)
}

type B5PostgresRuntime struct {
	executor     *PostgresExecutor
	authorizer   B5PostgresAuthorizer
	budgetLimits b5CatalogLimits
}

func NewB5PostgresRuntime(executor *PostgresExecutor, authorizer B5PostgresAuthorizer) (*B5PostgresRuntime, error) {
	if executor == nil || executor.pool == nil || authorizer == nil {
		return nil, errors.New("businessdb: incomplete B5 PostgreSQL runtime")
	}
	return &B5PostgresRuntime{executor: executor, authorizer: authorizer, budgetLimits: defaultB5CatalogLimits()}, nil
}

type b5PostgresArtifact struct {
	enrollment    PostgresDMLEnrollment
	authorization PostgresDMLAuthorization
}

func (runtime *B5PostgresRuntime) Analyze(ctx context.Context, statement b5coordinator.StatementRequest, ordinal int) (b5coordinator.Analysis, error) {
	budget := newB5CatalogBudget(runtime.budgetLimits)
	enrollment, err := runtime.executor.EnrollPostgresDML(ctx, statement.SQL, enrollmentDatasource(runtime.authorizer), budget)
	if err != nil {
		return b5coordinator.Analysis{}, err
	}
	authorization, err := runtime.authorizer.AuthorizeCandidate(ctx, enrollment, statement, ordinal)
	if err != nil {
		return b5coordinator.Analysis{}, err
	}
	datasourceID := enrollment.Facts.Target.DatasourceID
	if datasourceID == "" {
		return b5coordinator.Analysis{}, errors.New("businessdb: B5 enrollment lacks datasource identity")
	}
	auth := PostgresDMLAuthorization{PrincipalID: authorizationPrincipal(authorization.Policies), DatasourceID: datasourceID, Policies: append([]b5dml.Policy(nil), authorization.Policies...), PreliminaryAllowed: authorization.PreliminaryAllowed, DatasourceSupported: authorization.DatasourceSupported, ReservedTarget: authorization.ReservedTarget, PolicySnapshotDigest: authorization.PolicySnapshotDigest, Attestations: PostgresDMLBinderAttestations()}
	preflightAuthorization := b5dml.Authorize(b5dml.AuthorizationInput{
		PrincipalID: auth.PrincipalID, DatasourceID: auth.DatasourceID,
		Dialect: b5dml.DialectPostgreSQL, CurrentServerMajor: enrollment.Manifest.Capability.ServerMajor,
		Action: enrollment.Facts.Action, Target: enrollment.Facts.Target, Writes: enrollment.Facts.Writes, References: enrollment.Facts.References,
		Policies: auth.Policies, PreliminaryAllowed: auth.PreliminaryAllowed, DatasourceSupported: auth.DatasourceSupported,
		ReservedTarget: auth.ReservedTarget, CatalogConsistent: true, ClosureProven: true,
		PolicySnapshotDigest: auth.PolicySnapshotDigest, CatalogSnapshotDigest: enrollment.Catalog.Fingerprint,
		ClosureDigest: enrollment.ClosureDigest, PlanDigest: "agentsql.b5.preflight.pending-plan-digest.v1", Attestations: auth.Attestations,
	})
	if !preflightAuthorization.Allowed() {
		return b5coordinator.Analysis{}, &b5coordinator.Failure{Code: authorizationFailureCode(preflightAuthorization.Reason()), Cause: errors.New(string(preflightAuthorization.Reason()))}
	}
	manifestBytes, _ := json.Marshal(enrollment.Manifest)
	tree := sha256.Sum256([]byte(enrollment.Manifest.AnalyzedDigest))
	manifest := sha256.Sum256(manifestBytes)
	closure := sha256.Sum256([]byte(enrollment.ClosureDigest))
	return b5coordinator.Analysis{Facts: enrollment.Facts, TreeDigest: tree, ManifestDigest: manifest, ClosureDigest: closure, Decision: authorization.Decision, EstimatedRows: 0, EstimatedWork: uint64(enrollment.Manifest.WorkUnits + 1), Artifact: &b5PostgresArtifact{enrollment: enrollment, authorization: auth}}, nil
}

func authorizationFailureCode(reason b5dml.AuthorizationReason) b5.ErrorCode {
	switch reason {
	case b5dml.ReasonActionGrantMissing, b5dml.ReasonActionDenied:
		return b5.ErrorAuthDMLActionMissing
	case b5dml.ReasonWriteTargetGrantMissing, b5dml.ReasonWriteTargetDenied:
		return b5.ErrorAuthDMLWriteTargetGrantMissing
	case b5dml.ReasonReferenceGrantMissing, b5dml.ReasonReferenceDenied:
		return b5.ErrorAuthDMLReferenceGrantMissing
	case b5dml.ReasonReferenceFormUnsupported:
		return b5.ErrorAuthWholeRowUnsupported
	case b5dml.ReasonClosureUnproven:
		return b5.ErrorAuthImplicitObjectUnclosed
	case b5dml.ReasonReservedTarget:
		return b5.ErrorAuthInternalObjectDenied
	default:
		return b5.ErrorAuthDMLBinderRequired
	}
}

// enrollmentDatasource is intentionally optional only for static test
// authorizers. The authoritative datasource identity is still taken from the
// binder result and checked against the plan during Pin.
func enrollmentDatasource(authorizer B5PostgresAuthorizer) string {
	if typed, ok := authorizer.(interface{ DatasourceID() string }); ok {
		return typed.DatasourceID()
	}
	return "b5"
}
func authorizationPrincipal(policies []b5dml.Policy) string {
	for _, policy := range policies {
		if policy.PrincipalID != "" {
			return policy.PrincipalID
		}
	}
	return "b5-principal"
}

func (runtime *B5PostgresRuntime) Pin(ctx context.Context, plan b5coordinator.Plan, begin *b5dml.BeginMachine) (b5coordinator.BeginCapability, b5coordinator.BackendIdentity, error) {
	if plan.Dialect != "postgres" || plan.DatasourceID == "" {
		return nil, b5coordinator.BackendIdentity{}, errors.New("businessdb: invalid B5 PostgreSQL plan")
	}
	pooled, err := runtime.executor.pool.Acquire(ctx)
	if err != nil {
		return nil, b5coordinator.BackendIdentity{}, err
	}
	connection := pooled.Hijack()
	pgConn := connection.PgConn()
	secret := sha256.Sum256(pgConn.SecretKey())
	var started time.Time
	if err := connection.QueryRow(ctx, `SELECT pg_postmaster_start_time()`).Scan(&started); err != nil {
		_ = connection.Close(context.Background())
		return nil, b5coordinator.BackendIdentity{}, err
	}
	guard := b5terminal.NewCancelGuard()
	poolConfig := runtime.executor.pool.Config().ConnConfig
	connectionNonce := fmt.Sprintf("%x", sha256.Sum256(append([]byte(fmt.Sprintf("%d:%d", pgConn.PID(), time.Now().UnixNano())), pgConn.SecretKey()...)))
	capability := &b5PGCoordinatorCapability{runtime: runtime, connection: connection, guard: guard, plan: plan, begin: begin, identity: b5terminal.PGBackendIdentity{ServerIdentity: poolConfig.Host, Database: poolConfig.Database, PID: pgConn.PID(), BackendStart: started, ConnectionNonce: connectionNonce}}
	backend := b5coordinator.BackendIdentity{PID: int(pgConn.PID()), SecretDigest: secret, StartedAt: started, ConnectionGeneration: 1, LeaseGeneration: 1}
	return capability, backend, nil
}

type b5PGCoordinatorCapability struct {
	mu             sync.Mutex
	runtime        *B5PostgresRuntime
	connection     *pgx.Conn
	tx             pgx.Tx
	native         *PostgresDMLNativeTx
	guard          *b5terminal.CancelGuard
	terminal       *B5PGTypedTerminalAdapter
	plan           b5coordinator.Plan
	begin          *b5dml.BeginMachine
	identity       b5terminal.PGBackendIdentity
	active, closed bool
}

func (capability *b5PGCoordinatorCapability) BeginNative(ctx context.Context) (b5dml.BeginAttemptEvidence, error) {
	capability.mu.Lock()
	defer capability.mu.Unlock()
	frame := uint64(len("BEGIN") + 6)
	if capability.connection == nil {
		return missingBeginEvidence(frame), errors.New("businessdb: B5 connection unavailable")
	}
	isolation := pgx.Serializable
	switch capability.plan.Isolation {
	case "read_committed":
		isolation = pgx.ReadCommitted
	case "repeatable_read":
		isolation = pgx.RepeatableRead
	case "serializable":
	default:
		return missingBeginEvidence(frame), errors.New("businessdb: unsupported B5 isolation")
	}
	tx, err := capability.connection.BeginTx(ctx, pgx.TxOptions{IsoLevel: isolation, AccessMode: pgx.ReadWrite})
	if err != nil {
		return b5dml.BeginAttemptEvidence{Write: b5terminal.WriteEvidence{Phase: b5terminal.WriteIndeterminate, FrameBytes: frame}, Reply: b5dml.BeginReplyMissing, Correlation: b5terminal.CorrelationWeakOrAbsent, ServerStatus: b5terminal.ServerStatusNotObserved}, err
	}
	capability.tx = tx
	// pgx does not expose per-byte BEGIN accounting. A current positive reply
	// from this exclusive clean connection is stronger than local accounting,
	// so the frozen table permits WriteIndeterminate + strong correlation.
	return b5dml.BeginAttemptEvidence{Write: b5terminal.WriteEvidence{Phase: b5terminal.WriteIndeterminate, FrameBytes: frame}, Reply: b5dml.BeginReplyPositiveACK, Correlation: b5terminal.CorrelationStrongCurrentOperation, ServerStatus: b5terminal.ServerReadyIdleInTransaction}, nil
}
func missingBeginEvidence(frame uint64) b5dml.BeginAttemptEvidence {
	return b5dml.BeginAttemptEvidence{Write: b5terminal.WriteEvidence{Phase: b5terminal.WriteNotSent, FrameBytes: frame}, Reply: b5dml.BeginReplyMissing, Correlation: b5terminal.CorrelationNotApplicable, ServerStatus: b5terminal.ServerStatusNotObserved, ProtocolUntouched: true}
}
func (capability *b5PGCoordinatorCapability) FixContext(ctx context.Context, wall time.Time) error {
	capability.mu.Lock()
	defer capability.mu.Unlock()
	if capability.tx == nil {
		return errors.New("businessdb: native B5 transaction missing")
	}
	remaining := time.Until(wall)
	if remaining <= 0 {
		return context.DeadlineExceeded
	}
	milliseconds := remaining.Milliseconds()
	if milliseconds < 1 {
		milliseconds = 1
	}
	_, err := capability.tx.Exec(ctx, `SELECT set_config('statement_timeout',$1,true),set_config('idle_in_transaction_session_timeout',$2,true)`, fmt.Sprintf("%dms", milliseconds), fmt.Sprintf("%dms", milliseconds))
	return err
}
func (capability *b5PGCoordinatorCapability) BindAndSeal(ctx context.Context, plan b5coordinator.Plan) ([32]byte, error) {
	capability.mu.Lock()
	defer capability.mu.Unlock()
	if capability.tx == nil {
		return [32]byte{}, errors.New("businessdb: native B5 transaction missing")
	}
	native, err := AttachPostgresDMLNativeTx(ctx, capability.tx, capability.begin)
	if err != nil {
		return [32]byte{}, err
	}
	capability.native = native
	for _, statement := range plan.Statements {
		artifact, ok := statement.Artifact().(*b5PostgresArtifact)
		if !ok || artifact == nil {
			return [32]byte{}, errors.New("businessdb: invalid B5 DML enrollment")
		}
		auth := artifact.authorization
		auth.PlanDigest = fmt.Sprintf("%x", plan.Digest)
		prepared, decision, prepareErr := capability.runtime.executor.PrepareBoundPostgresDML(ctx, native, statement.SQL, &artifact.enrollment, auth, newB5CatalogBudget(capability.runtime.budgetLimits))
		if prepareErr != nil || !decision.Allowed() {
			return [32]byte{}, errors.Join(prepareErr, errors.New("businessdb: B5 DML authorization denied"))
		}
		if closeErr := prepared.Close(ctx); closeErr != nil {
			return [32]byte{}, closeErr
		}
	}
	return plan.Digest, nil
}
func (capability *b5PGCoordinatorCapability) Activate() (b5coordinator.Capability, error) {
	capability.mu.Lock()
	defer capability.mu.Unlock()
	if capability.native == nil || capability.closed {
		return nil, errors.New("businessdb: B5 plan not sealed")
	}
	capability.active = true
	return capability, nil
}
func (capability *b5PGCoordinatorCapability) Execute(ctx context.Context, statement b5coordinator.PlannedStatement) (b5coordinator.StatementResult, error) {
	capability.mu.Lock()
	if !capability.active || capability.closed || capability.native == nil {
		capability.mu.Unlock()
		return b5coordinator.StatementResult{}, errors.New("businessdb: B5 capability inactive")
	}
	artifact, ok := statement.Artifact().(*b5PostgresArtifact)
	if !ok {
		capability.mu.Unlock()
		return b5coordinator.StatementResult{}, errors.New("businessdb: invalid B5 statement artifact")
	}
	auth := artifact.authorization
	auth.PlanDigest = fmt.Sprintf("%x", capability.plan.Digest)
	native := capability.native
	capability.mu.Unlock()
	prepared, decision, err := capability.runtime.executor.PrepareBoundPostgresDML(ctx, native, statement.SQL, &artifact.enrollment, auth, newB5CatalogBudget(capability.runtime.budgetLimits))
	if err != nil {
		return b5coordinator.StatementResult{Decision: b5coordinator.DecisionDeny, CapabilityCertain: !IsPostgresDMLClosureRace(err)}, err
	}
	defer prepared.Close(context.Background())
	tag, err := prepared.Execute(ctx, newB5CatalogBudget(capability.runtime.budgetLimits))
	evidence := sha256.Sum256([]byte(fmt.Sprintf("%s:%d:%s", statement.OperationID, tag.RowsAffected(), decision.Digest())))
	return b5coordinator.StatementResult{AffectedRows: tag.RowsAffected(), EvidenceDigest: evidence, Decision: b5coordinator.DecisionAllow, CapabilityCertain: err == nil && !prepared.DiscardRequired()}, err
}
func (capability *b5PGCoordinatorCapability) Cancel(ctx context.Context) (b5terminal.CancelEmission, error) {
	capability.mu.Lock()
	if capability.closed || capability.connection == nil {
		capability.mu.Unlock()
		return b5terminal.CancelNotSentProven, errors.New("businessdb: B5 connection closed")
	}
	pgConn := capability.connection.PgConn()
	capability.mu.Unlock()
	err := pgConn.CancelRequest(ctx)
	emission := b5terminal.CancelFullPacketWritten
	if err != nil {
		emission = b5terminal.CancelSendStartedOrIndeterminate
	}
	capability.guard.ObserveEmission(emission)
	return emission, err
}
func (capability *b5PGCoordinatorCapability) FinishCommit(ctx context.Context, owner *b5terminal.TerminalOwner, attempt b5terminal.TerminalAttempt) (b5coordinator.TerminalResult, error) {
	terminal, err := capability.terminalAdapter()
	if err != nil {
		return b5coordinator.TerminalResult{}, err
	}
	result, finishErr := terminal.FinishCommit(ctx, owner, attempt)
	return coordinatorTerminalResult(result), finishErr
}
func (capability *b5PGCoordinatorCapability) FinishRollback(ctx context.Context, owner *b5terminal.TerminalOwner, attempt b5terminal.TerminalAttempt, proof b5terminal.NoCommitEverSent) (b5coordinator.TerminalResult, error) {
	terminal, err := capability.terminalAdapter()
	if err != nil {
		return b5coordinator.TerminalResult{}, err
	}
	result, finishErr := terminal.FinishRollback(ctx, owner, attempt, proof)
	return coordinatorTerminalResult(result), finishErr
}
func (capability *b5PGCoordinatorCapability) terminalAdapter() (*B5PGTypedTerminalAdapter, error) {
	capability.mu.Lock()
	if capability.terminal != nil {
		terminal := capability.terminal
		capability.mu.Unlock()
		return terminal, nil
	}
	if capability.connection == nil || capability.closed {
		capability.mu.Unlock()
		return nil, errors.New("businessdb: B5 connection unavailable")
	}
	pgConn, identity, guard := capability.connection.PgConn(), capability.identity, capability.guard
	capability.mu.Unlock()
	terminal, err := NewB5PGTypedTerminalAdapter(pgConn, identity, guard, B5PGXPhysicalHooks{ConfirmBackendAbsence: capability.confirmAbsence, OnLocalConnectionDiscard: func(b5terminal.PGBackendIdentity, error) {
		capability.mu.Lock()
		capability.closed = true
		capability.mu.Unlock()
	}}, b5terminal.PGAdapterOptions{})
	if err != nil {
		return nil, err
	}
	capability.mu.Lock()
	defer capability.mu.Unlock()
	if capability.terminal != nil {
		return capability.terminal, nil
	}
	capability.terminal = terminal
	return terminal, nil
}
func (capability *b5PGCoordinatorCapability) confirmAbsence(ctx context.Context, identity b5terminal.PGBackendIdentity) (bool, error) {
	var exists bool
	err := capability.runtime.executor.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_catalog.pg_stat_activity WHERE pid=$1 AND backend_start=$2)`, identity.PID, identity.BackendStart).Scan(&exists)
	return !exists, err
}
func (capability *b5PGCoordinatorCapability) Discard(ctx context.Context) (b5.ConnectionDisposition, error) {
	capability.mu.Lock()
	if capability.closed {
		capability.mu.Unlock()
		return b5.DispositionDiscarded, nil
	}
	connection := capability.connection
	capability.closed = true
	capability.mu.Unlock()
	if connection == nil {
		return b5.DispositionDiscardUnconfirmed, nil
	}
	err := connection.Close(ctx)
	absent, inventoryErr := capability.confirmAbsence(ctx, capability.identity)
	if absent {
		return b5.DispositionDiscarded, errors.Join(err, inventoryErr)
	}
	return b5.DispositionDiscardUnconfirmed, errors.Join(err, inventoryErr)
}
func (capability *b5PGCoordinatorCapability) CleanupBegin(ctx context.Context, owner *b5terminal.TerminalOwner, decision b5dml.BeginCleanupDecision) (b5coordinator.TerminalResult, error) {
	if decision.Action == b5dml.BeginCleanupFinishRollback && decision.TerminalOwned {
		proof := b5terminal.NoCommitEverSent{Schema: b5terminal.NoCommitEverSentSchema, SchemaVersion: b5terminal.NoCommitEverSentVersion, TransactionGeneration: decision.Attempt.TransactionGeneration, OwnerGeneration: decision.Attempt.OwnerGeneration, HistoryContinuous: true, UniqueSocketOwner: true}
		return capability.FinishRollback(ctx, owner, decision.Attempt, proof)
	}
	disposition, err := capability.Discard(ctx)
	return b5coordinator.TerminalResult{Resolution: b5terminal.TerminalResolution{Outcome: b5.OutcomeNotCommitted, Disposition: disposition}}, err
}
func coordinatorTerminalResult(result B5PGTerminalResult) b5coordinator.TerminalResult {
	encoded, _ := json.Marshal(result.Evidence)
	evidence := sha256.Sum256(encoded)
	proof := sha256.Sum256([]byte(fmt.Sprintf("%s:%s:%d", result.Resolution.Consistency.Verdict, result.Resolution.Disposition, result.Backend.PID)))
	return b5coordinator.TerminalResult{Resolution: result.Resolution, EvidenceDigest: evidence, DispositionProofDigest: proof, CancelEmission: result.CancelSnapshot.Emission}
}

type b5CatalogLimits struct{ relations, roundTrips, bytes, rows, columns, nodes, edges, paths, work int }

func defaultB5CatalogLimits() b5CatalogLimits {
	return b5CatalogLimits{256, 128, 4 << 20, 8192, 8192, 20000, 40000, 40000, 1000000}
}

type b5CatalogBudget struct {
	mu        sync.Mutex
	remaining b5CatalogLimits
}

func newB5CatalogBudget(limits b5CatalogLimits) *b5CatalogBudget {
	return &b5CatalogBudget{remaining: limits}
}
func (budget *b5CatalogBudget) charge(field *int, value int) error {
	budget.mu.Lock()
	defer budget.mu.Unlock()
	if value < 0 || value > *field {
		return errors.New("businessdb: B5 catalog budget exceeded")
	}
	*field -= value
	return nil
}
func (budget *b5CatalogBudget) ChargeRelations(v int) error {
	return budget.charge(&budget.remaining.relations, v)
}
func (budget *b5CatalogBudget) CheckViewDepth(v int) error {
	if v != 0 {
		return errors.New("businessdb: B5 view unsupported")
	}
	return nil
}
func (budget *b5CatalogBudget) ChargeCatalogRoundTrips(v int) error {
	return budget.charge(&budget.remaining.roundTrips, v)
}
func (budget *b5CatalogBudget) ChargeDefinitionBytes(v int) error {
	return budget.charge(&budget.remaining.bytes, v)
}
func (budget *b5CatalogBudget) ChargeBinderBytes(v int) error {
	return budget.charge(&budget.remaining.bytes, v)
}
func (budget *b5CatalogBudget) ChargeCatalogBytes(v int) error {
	return budget.charge(&budget.remaining.bytes, v)
}
func (budget *b5CatalogBudget) ChargeCatalogRows(v int) error {
	return budget.charge(&budget.remaining.rows, v)
}
func (budget *b5CatalogBudget) ChargeColumnMetadata(v int) error {
	return budget.charge(&budget.remaining.columns, v)
}
func (budget *b5CatalogBudget) ChargeNodes(v int) error {
	return budget.charge(&budget.remaining.nodes, v)
}
func (budget *b5CatalogBudget) ChargeEdges(v int) error {
	return budget.charge(&budget.remaining.edges, v)
}
func (budget *b5CatalogBudget) ChargePaths(v int) error {
	return budget.charge(&budget.remaining.paths, v)
}
func (budget *b5CatalogBudget) ChargeWork(v int) error {
	return budget.charge(&budget.remaining.work, v)
}

var _ b5coordinator.Analyzer = (*B5PostgresRuntime)(nil)
var _ b5coordinator.Engine = (*B5PostgresRuntime)(nil)
var _ b5coordinator.BeginCapability = (*b5PGCoordinatorCapability)(nil)
var _ b5coordinator.Capability = (*b5PGCoordinatorCapability)(nil)

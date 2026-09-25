// Package b5coordinator implements the feature-off B5 planned transaction
// coordinator.  It deliberately has no MCP, HTTP, feature-flag, credential or
// raw database dependency.  The only executable object it can receive is the
// narrow, transaction-bound Capability below.
package b5coordinator

import (
	"context"
	"errors"
	"time"

	"github.com/cuipengdba/agentsql/internal/b5"
	"github.com/cuipengdba/agentsql/internal/b5dml"
	"github.com/cuipengdba/agentsql/internal/b5terminal"
	"github.com/cuipengdba/agentsql/internal/store"
)

const (
	PlanSchemaID      = "agentsql.b5.plan.v4"
	PlanSchemaVersion = uint16(4)
	BeginAuthSchemaID = "agentsql.b5.begin-auth.v3"
	RequiredBinderABI = b5dml.BinderABI
)

var (
	ErrInvalidPlan       = errors.New("b5coordinator: invalid transaction plan")
	ErrPlanDenied        = errors.New("b5coordinator: plan denied")
	ErrApprovalRequired  = errors.New("b5coordinator: approval required")
	ErrApprovalInvalid   = errors.New("b5coordinator: approval invalid")
	ErrWrongOwner        = errors.New("b5coordinator: wrong owner or epoch")
	ErrBusy              = errors.New("b5coordinator: operation already active")
	ErrWrongOrdinal      = errors.New("b5coordinator: operation is not next in plan")
	ErrRollbackOnly      = errors.New("b5coordinator: transaction is rollback-only")
	ErrTerminal          = errors.New("b5coordinator: transaction is terminal")
	ErrCapabilityUnknown = errors.New("b5coordinator: capability result is uncertain")
)

type Decision uint8

const (
	DecisionUnknown Decision = iota
	DecisionAllow
	DecisionWarn
	DecisionApprove
	DecisionDeny
	DecisionMask
)

type ResourceLimits struct {
	MaxStatements     int
	MaxSQLBytes       int
	MaxPlanBytes      int
	MaxAffectedRows   int64
	MaxEstimatedWork  uint64
	IdleTimeout       time.Duration
	WallTimeout       time.Duration
	StatementTimeout  time.Duration
	OperationWatchdog time.Duration
	QuiesceGrace      time.Duration
}

func DefaultResourceLimits() ResourceLimits {
	return ResourceLimits{
		MaxStatements: 16, MaxSQLBytes: 256 << 10, MaxPlanBytes: 1 << 20,
		MaxAffectedRows: 10_000, MaxEstimatedWork: 1_000_000,
		IdleTimeout: 15 * time.Second, WallTimeout: 60 * time.Second,
		StatementTimeout: 5 * time.Second, OperationWatchdog: 300 * time.Millisecond,
		QuiesceGrace: 25 * time.Millisecond,
	}
}

type PlanRequest struct {
	TenantID, PrincipalID, AgentID, DatasourceID    string
	KeyRevision, DatasourceRevision, PolicyRevision uint64
	Dialect                                         string
	ServerMajor                                     int
	Isolation, BinderABI, ClosurePolicy             string
	Statements                                      []StatementRequest
	Limits                                          ResourceLimits
}

type StatementRequest struct {
	OperationID string
	SQL         string
	Reason      string
}

// Analysis is immutable candidate-phase evidence. Artifact may contain the
// binder enrollment used for same-transaction comparison, but it must not be
// serializable or executable and is never included in a digest.
type Analysis struct {
	Facts          b5dml.StatementFacts
	TreeDigest     [32]byte
	ManifestDigest [32]byte
	ClosureDigest  [32]byte
	Decision       Decision
	EstimatedRows  int64
	EstimatedWork  uint64
	Artifact       any
}

type Analyzer interface {
	Analyze(context.Context, StatementRequest, int) (Analysis, error)
}

type PlannedStatement struct {
	Ordinal        int
	OperationID    string
	SQL            string
	RawSQLDigest   [32]byte
	ReasonDigest   [32]byte
	TreeDigest     [32]byte
	ManifestDigest [32]byte
	ClosureDigest  [32]byte
	Action         b5.DMLAction
	Writes         []b5dml.WriteTarget
	References     []b5dml.Reference
	Decision       Decision
	EstimatedRows  int64
	EstimatedWork  uint64
	artifact       any
}

func (statement PlannedStatement) Artifact() any { return statement.artifact }

type Plan struct {
	SchemaID, TenantID, PrincipalID, AgentID, DatasourceID string
	SchemaVersion                                          uint16
	KeyRevision, DatasourceRevision, PolicyRevision        uint64
	Dialect                                                string
	ServerMajor                                            int
	Isolation, BinderABI, ClosurePolicy                    string
	Limits                                                 ResourceLimits
	Statements                                             []PlannedStatement
	Digest                                                 [32]byte
	RequiresApproval                                       bool
}

type ApprovalConsumeRequest struct {
	ApprovalID, TransactionID, RequestID string
	PlanDigest                           [32]byte
	OwnerEpoch                           uint64
	Now                                  time.Time
}

type ApprovalLease struct {
	ID                   string
	Generation           uint64
	ApprovedBoundsDigest [32]byte
	ExpiresAt            time.Time
}

type ApprovalStore interface {
	Consume(context.Context, ApprovalConsumeRequest) (ApprovalLease, error)
	MarkBeginFailed(context.Context, ApprovalLease, string) error
	MarkActive(context.Context, ApprovalLease, string) error
}

type BackendIdentity struct {
	PID                                   int
	SecretDigest                          [32]byte
	StartedAt                             time.Time
	ConnectionGeneration, LeaseGeneration uint64
}

type BeginCapability interface {
	BeginNative(context.Context) (b5dml.BeginAttemptEvidence, error)
	FixContext(context.Context, time.Time) error
	BindAndSeal(context.Context, Plan) ([32]byte, error)
	Activate() (Capability, error)
	CleanupBegin(context.Context, *b5terminal.TerminalOwner, b5dml.BeginCleanupDecision) (TerminalResult, error)
}

type Engine interface {
	Pin(context.Context, Plan, *b5dml.BeginMachine) (BeginCapability, BackendIdentity, error)
}

type StatementResult struct {
	AffectedRows      int64
	EvidenceDigest    [32]byte
	Decision          Decision
	CapabilityCertain bool
}

type TerminalResult struct {
	Resolution             b5terminal.TerminalResolution
	EvidenceDigest         [32]byte
	DispositionProofDigest [32]byte
	CancelEmission         b5terminal.CancelEmission
}

// Capability is transaction-bound and accepts only a statement already sealed
// into the plan. It intentionally has no method taking arbitrary SQL.
type Capability interface {
	Execute(context.Context, PlannedStatement) (StatementResult, error)
	Cancel(context.Context) (b5terminal.CancelEmission, error)
	FinishCommit(context.Context, *b5terminal.TerminalOwner, b5terminal.TerminalAttempt) (TerminalResult, error)
	FinishRollback(context.Context, *b5terminal.TerminalOwner, b5terminal.TerminalAttempt, b5terminal.NoCommitEverSent) (TerminalResult, error)
	Discard(context.Context) (b5.ConnectionDisposition, error)
}

type AuditKind string

const (
	AuditTxBegin      AuditKind = "tx_begin"
	AuditStatement    AuditKind = "tx_statement"
	AuditCommitIntent AuditKind = "tx_commit_intent"
	AuditRollback     AuditKind = "tx_rollback"
	AuditTerminal     AuditKind = "tx_terminal_outcome"
)

type AuditEvent struct {
	Kind                                AuditKind
	TransactionID, SessionID, RequestID string
	Sequence                            uint64
	PlanDigest                          [32]byte
	StatementOrdinal                    int
	Action                              b5.DMLAction
	AffectedRows                        int64
	DBOutcome                           b5.DBOutcome
	ErrorCode                           b5.ErrorCode
	EvidenceDigest                      [32]byte
}

type AuditResult struct {
	Durability  b5.AuditDurability
	EventDigest [32]byte
}

type Auditor interface {
	Barrier(context.Context, AuditEvent) (AuditResult, error)
}

type SessionAuthorization struct {
	SessionID, McpSessionID, PrincipalID, InstanceID string
	Method, RequestID, Proof                         string
	OwnerEpoch                                       uint64
	ExpectedSeq                                      *uint64
	BodyDigest                                       [32]byte
}

type SessionGate interface {
	Validate(context.Context, SessionAuthorization) error
}

type TransactionStore interface {
	store.B5TransactionStore
}

type BeginRequest struct {
	Session                              SessionAuthorization
	TransactionID, RequestID, ApprovalID string
	Plan                                 PlanRequest
}

type ExecuteRequest struct {
	Session                               SessionAuthorization
	TransactionID, RequestID, OperationID string
	Ordinal                               int
}

type FinishRequest struct {
	Session                  SessionAuthorization
	TransactionID, RequestID string
}

type OperationEvidence struct {
	Ordinal                 int      `json:"ordinal"`
	OperationID             string   `json:"operation_id"`
	AffectedRows            int64    `json:"affected_rows"`
	StatementEvidenceDigest [32]byte `json:"statement_evidence_digest"`
	AuditEventDigest        [32]byte `json:"audit_event_digest"`
}

type Result struct {
	TransactionID         string                   `json:"transaction_id"`
	Status                b5.TransactionStatus     `json:"status"`
	Phase                 b5.TransactionPhase      `json:"phase"`
	Code                  b5.ErrorCode             `json:"error_code,omitempty"`
	Effect                b5.TxEffect              `json:"tx_effect"`
	DBOutcome             b5.DBOutcome             `json:"db_outcome,omitempty"`
	AuditDurability       b5.AuditDurability       `json:"audit_durability,omitempty"`
	ConnectionDisposition b5.ConnectionDisposition `json:"connection_disposition,omitempty"`
	NextOrdinal           int                      `json:"next_ordinal"`
	AffectedRows          int64                    `json:"affected_rows"`
	Evidence              []OperationEvidence      `json:"evidence,omitempty"`
}

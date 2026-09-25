// Package b5 freezes the feature-off B5 cross-package wire and persistence ABI.
// It contains no coordinator, database connection, feature flag, or production
// entry-point integration.
package b5

import "time"

const (
	EventSchemaID      = "agentsql.audit.event.v4"
	EventSchemaVersion = uint16(4)

	DMLGrantProofSchemaID      = "agentsql.b5.dml-grant-proof.v1"
	DMLGrantProofSchemaVersion = uint16(1)
	ResultReceiptSchemaID      = "agentsql.b5.result-receipt.v1"
	ResultReceiptSchemaVersion = uint16(1)
	TerminalEvidenceSchemaID   = "agentsql.b5.terminal-evidence.v2"
	TerminalEvidenceVersion    = uint16(2)
	NoCommitProofSchemaID      = "agentsql.b5.no-commit-ever-sent.v1"
	NoCommitProofVersion       = uint16(1)
	DispositionProofSchemaID   = "agentsql.b5.connection-disposition-proof.v1"
	DispositionProofVersion    = uint16(1)
	BeginCleanupProofSchemaID  = "agentsql.b5.begin-cleanup-proof.v1"
	BeginCleanupProofVersion   = uint16(1)
	WALAppendReceiptSchemaID   = "agentsql.b5.wal-append-receipt.v1"
	WALAppendReceiptVersion    = uint16(1)
)

type SessionStatus string

const (
	SessionReady    SessionStatus = "READY"
	SessionActive   SessionStatus = "ACTIVE"
	SessionTerminal SessionStatus = "TERMINAL"
	SessionExpired  SessionStatus = "EXPIRED"
)

type TransactionStatus string

const (
	TransactionPending      TransactionStatus = "PENDING"
	TransactionActive       TransactionStatus = "ACTIVE"
	TransactionRollbackOnly TransactionStatus = "ROLLBACK_ONLY"
	TransactionTerminal     TransactionStatus = "TERMINAL"
)

type TransactionPhase string

const (
	PhaseReady                TransactionPhase = "READY"
	PhasePlanReady            TransactionPhase = "PLAN_READY"
	PhaseApprovalConsumed     TransactionPhase = "APPROVAL_CONSUMED"
	PhaseConnectionPinned     TransactionPhase = "CONNECTION_PINNED"
	PhaseNativeBegun          TransactionPhase = "NATIVE_BEGUN"
	PhaseContextFixed         TransactionPhase = "CONTEXT_FIXED"
	PhaseSealedInTx           TransactionPhase = "SEALED_IN_TX"
	PhaseBeginAuditing        TransactionPhase = "BEGIN_AUDITING"
	PhaseActive               TransactionPhase = "ACTIVE"
	PhaseBeginFailTerminating TransactionPhase = "BEGIN_FAIL_TERMINATING"
	PhaseRollbackOnly         TransactionPhase = "ROLLBACK_ONLY"
	PhaseCommitting           TransactionPhase = "COMMITTING"
	PhaseRollingBack          TransactionPhase = "ROLLING_BACK"
	PhaseTerminal             TransactionPhase = "TERMINAL"
	PhaseFinalFence           TransactionPhase = "FINAL_FENCE"
)

type TerminalOperation string

const (
	OperationUnknown  TerminalOperation = "UNKNOWN"
	OperationCommit   TerminalOperation = "COMMIT"
	OperationRollback TerminalOperation = "ROLLBACK"
)

type WritePhase string

const (
	WritePhaseUnknown  WritePhase = "UNKNOWN"
	WriteNotSent       WritePhase = "NOT_SENT"
	WriteZeroBytes     WritePhase = "ZERO_BYTES_WRITTEN"
	WritePartial       WritePhase = "PARTIAL_BYTES_WRITTEN"
	WriteFullFrame     WritePhase = "FULL_FRAME_WRITTEN"
	WriteIndeterminate WritePhase = "WRITE_INDETERMINATE"
)

type ProtocolReply string

const (
	ReplyUnknown                    ProtocolReply = "UNKNOWN"
	ReplyMissing                    ProtocolReply = "MISSING"
	ReplyCurrentPositiveACK         ProtocolReply = "CURRENT_POSITIVE_ACK"
	ReplyCurrentDefinitiveRejection ProtocolReply = "CURRENT_DEFINITIVE_REJECTION"
	ReplyWrongOperationTyped        ProtocolReply = "WRONG_OPERATION_TYPED_REPLY"
	ReplyStaleGenerationTyped       ProtocolReply = "STALE_GENERATION_TYPED_REPLY"
	ReplyUntypedOrMismatched        ProtocolReply = "UNTYPED_OR_MISMATCHED"
	ReplyDuplicateACKOrRFQ          ProtocolReply = "DUPLICATE_ACK_OR_RFQ"
)

type ReplyCorrelation string

const (
	CorrelationUnknown                ReplyCorrelation = "UNKNOWN"
	CorrelationNotApplicable          ReplyCorrelation = "NOT_APPLICABLE"
	CorrelationWeakOrAbsent           ReplyCorrelation = "WEAK_OR_ABSENT"
	CorrelationStrongCurrentOperation ReplyCorrelation = "STRONG_CURRENT_OPERATION"
)

type ServerTxStatus string

const (
	ServerStatusUnknown                ServerTxStatus = "UNKNOWN"
	ServerStatusNotObserved            ServerTxStatus = "NOT_OBSERVED"
	ServerReadyIdle                    ServerTxStatus = "IDLE"
	ServerReadyIdleInTransaction       ServerTxStatus = "IN_TRANSACTION"
	ServerReadyFailed                  ServerTxStatus = "FAILED"
	ServerStatusUnknownOrContradictory ServerTxStatus = "UNKNOWN_OR_CONTRADICTORY"
)

type EvidenceConsistency string

const (
	EvidenceUnknownSchema EvidenceConsistency = "UNKNOWN_SCHEMA"
	EvidenceConsistent    EvidenceConsistency = "CONSISTENT"
	EvidenceInsufficient  EvidenceConsistency = "INSUFFICIENT"
	EvidenceContradictory EvidenceConsistency = "CONTRADICTORY"
)

type DBOutcome string

const (
	OutcomeUnknown      DBOutcome = "UNKNOWN"
	OutcomeCommitted    DBOutcome = "COMMITTED"
	OutcomeNotCommitted DBOutcome = "NOT_COMMITTED"
)

type ConnectionDisposition string

const (
	DispositionUnknown            ConnectionDisposition = "UNKNOWN"
	DispositionReleased           ConnectionDisposition = "RELEASED"
	DispositionDiscarded          ConnectionDisposition = "DISCARDED"
	DispositionDiscardUnconfirmed ConnectionDisposition = "DISCARD_UNCONFIRMED"
)

type AuditDurability string

const (
	DurabilityUnspecified  AuditDurability = ""
	DurabilityDurable      AuditDurability = "DURABLE"
	DurabilityAuditPending AuditDurability = "AUDIT_PENDING"
	DurabilityLost         AuditDurability = "DURABILITY_LOST"
)

type AppendConfirmation string

const (
	AppendUnknown       AppendConfirmation = "UNKNOWN"
	AppendTimeout       AppendConfirmation = "TIMEOUT_UNCONFIRMED"
	AppendLateConfirmed AppendConfirmation = "LATE_CONFIRMED_AFTER_RESPONSE"
	AppendRecovered     AppendConfirmation = "RECOVERED_ON_RESTART"
)

type Reconciliation string

const (
	ReconciliationNone           Reconciliation = "NONE"
	ReconciliationReplayStaged   Reconciliation = "REPLAY_STAGED"
	ReconciliationPrimaryDurable Reconciliation = "PRIMARY_DURABLE"
)

type DeliveryStatus string

const (
	DeliveryPrepared     DeliveryStatus = "PREPARED"
	DeliverySendStarted  DeliveryStatus = "SEND_STARTED"
	DeliverySendComplete DeliveryStatus = "SEND_COMPLETED"
)

type DMLAction string

const (
	ActionUnknown DMLAction = "UNKNOWN"
	ActionInsert  DMLAction = "INSERT"
	ActionUpdate  DMLAction = "UPDATE"
	ActionDelete  DMLAction = "DELETE"
)

func (action DMLAction) String() string { return string(action) }

type GrantElement string

const (
	GrantElementUnknown     GrantElement = "UNKNOWN"
	GrantElementAction      GrantElement = "ACTION"
	GrantElementWriteTarget GrantElement = "WRITE_TARGET"
	GrantElementReference   GrantElement = "REFERENCE"
)

type GrantEffect string

const (
	GrantEffectUnknown GrantEffect = "UNKNOWN"
	GrantAllow         GrantEffect = "ALLOW"
	GrantDeny          GrantEffect = "DENY"
)

type TxEffect string

const (
	EffectNoTxChange           TxEffect = "NO_TX_CHANGE"
	EffectKeepActive           TxEffect = "KEEP_ACTIVE"
	EffectMarkRollbackOnly     TxEffect = "MARK_ROLLBACK_ONLY"
	EffectTerminalNotCommitted TxEffect = "TERMINAL_NOT_COMMITTED"
	EffectTerminalCommitted    TxEffect = "TERMINAL_COMMITTED"
	EffectTerminalUnknown      TxEffect = "TERMINAL_UNKNOWN"
	EffectSessionTerminal      TxEffect = "SESSION_TERMINAL"
	EffectFenceOnly            TxEffect = "FENCE_ONLY"
)

// ErrorCode is intentionally open for forward-compatible readers; constants
// freeze every v4 code currently emitted or persisted by B5.
type ErrorCode string

const (
	ErrorNone                                        ErrorCode = ""
	ErrorMCPProtocolUnsupported                      ErrorCode = "MCP_PROTOCOL_UNSUPPORTED"
	ErrorSessionProofRequired                        ErrorCode = "SESSION_PROOF_REQUIRED"
	ErrorSessionNotFoundOrDenied                     ErrorCode = "SESSION_NOT_FOUND_OR_DENIED"
	ErrorSessionOwnerEpochStale                      ErrorCode = "SESSION_OWNER_EPOCH_STALE"
	ErrorSessionWrongInstance                        ErrorCode = "SESSION_WRONG_INSTANCE"
	ErrorSessionRouteUnavailable                     ErrorCode = "SESSION_ROUTE_UNAVAILABLE"
	ErrorSessionOwnerLost                            ErrorCode = "SESSION_OWNER_LOST"
	ErrorSessionBusy                                 ErrorCode = "SESSION_BUSY"
	ErrorSessionLimitExceeded                        ErrorCode = "SESSION_LIMIT_EXCEEDED"
	ErrorSessionExpired                              ErrorCode = "SESSION_EXPIRED"
	ErrorSessionTerminalRecordExpired                ErrorCode = "SESSION_TERMINAL_RECORD_EXPIRED"
	ErrorSessionRenewDuringTx                        ErrorCode = "SESSION_RENEW_DURING_TX"
	ErrorIdempotencyRequired                         ErrorCode = "IDEMPOTENCY_REQUIRED"
	ErrorIdempotencyConflict                         ErrorCode = "IDEMPOTENCY_CONFLICT"
	ErrorRateLimitStateExhausted                     ErrorCode = "RATE_LIMIT_STATE_EXHAUSTED"
	ErrorTxNotActive                                 ErrorCode = "TX_NOT_ACTIVE"
	ErrorTxAlreadyActive                             ErrorCode = "TX_ALREADY_ACTIVE"
	ErrorTxIDMismatch                                ErrorCode = "TX_ID_MISMATCH"
	ErrorTxActivePlanMismatch                        ErrorCode = "TX_ACTIVE_PLAN_MISMATCH"
	ErrorTxPlanRequired                              ErrorCode = "TX_PLAN_REQUIRED"
	ErrorTxPlanMismatch                              ErrorCode = "TX_PLAN_MISMATCH"
	ErrorTxApprovalRequired                          ErrorCode = "TX_APPROVAL_REQUIRED"
	ErrorTxApprovalExpired                           ErrorCode = "TX_APPROVAL_EXPIRED"
	ErrorTxApprovalPlanMismatch                      ErrorCode = "TX_APPROVAL_PLAN_MISMATCH"
	ErrorTxApprovalConsumedBeginFailed               ErrorCode = "TX_APPROVAL_CONSUMED_BEGIN_FAILED"
	ErrorTxBeginManifestMismatch                     ErrorCode = "TX_BEGIN_MANIFEST_MISMATCH"
	ErrorTxBeginOutcomeUncertainConnectionQuarantine ErrorCode = "TX_BEGIN_OUTCOME_UNCERTAIN_CONNECTION_QUARANTINED"
	ErrorTxPlanIncomplete                            ErrorCode = "TX_PLAN_INCOMPLETE"
	ErrorTxRollbackOnly                              ErrorCode = "TX_ROLLBACK_ONLY"
	ErrorTxMaskUnsupported                           ErrorCode = "TX_MASK_UNSUPPORTED"
	ErrorTxSelectUnsupported                         ErrorCode = "TX_SELECT_UNSUPPORTED"
	ErrorTxReturningUnsupported                      ErrorCode = "TX_RETURNING_UNSUPPORTED"
	ErrorTxControlStatementDenied                    ErrorCode = "TX_CONTROL_STATEMENT_DENIED"
	ErrorTxDMLShapeUnsupported                       ErrorCode = "TX_DML_SHAPE_UNSUPPORTED"
	ErrorTxIdleTimeout                               ErrorCode = "TX_IDLE_TIMEOUT"
	ErrorTxMaxDuration                               ErrorCode = "TX_MAX_DURATION"
	ErrorTxStatementTimeout                          ErrorCode = "TX_STATEMENT_TIMEOUT"
	ErrorTxOperationWatchdog                         ErrorCode = "TX_OPERATION_WATCHDOG"
	ErrorTxOperationWatchdogOutcomeUnknown           ErrorCode = "TX_OPERATION_WATCHDOG_OUTCOME_UNKNOWN"
	ErrorTxCancelEmittedConnectionQuarantined        ErrorCode = "TX_CANCEL_EMITTED_CONNECTION_QUARANTINED"
	ErrorTxCommitEvidenceContradiction               ErrorCode = "TX_COMMIT_EVIDENCE_CONTRADICTION"
	ErrorTxRollbackEvidenceContradictionNoCommit     ErrorCode = "TX_ROLLBACK_EVIDENCE_CONTRADICTION_NO_COMMIT"
	ErrorTxRollbackEvidenceContradictionUnknown      ErrorCode = "TX_ROLLBACK_EVIDENCE_CONTRADICTION_UNKNOWN"
	ErrorTxRollbackUnconfirmed                       ErrorCode = "TX_ROLLBACK_UNCONFIRMED"
	ErrorTxLimitExceeded                             ErrorCode = "TX_LIMIT_EXCEEDED"
	ErrorPlanByteLimitExceeded                       ErrorCode = "PLAN_BYTE_LIMIT_EXCEEDED"
	ErrorTxExecutionLimitExceeded                    ErrorCode = "TX_EXECUTION_LIMIT_EXCEEDED"
	ErrorTxCommittedAuditPending                     ErrorCode = "TX_COMMITTED_AUDIT_PENDING"
	ErrorTxNotCommittedAuditPending                  ErrorCode = "TX_NOT_COMMITTED_AUDIT_PENDING"
	ErrorTxDBOutcomeUnknown                          ErrorCode = "TX_DB_OUTCOME_UNKNOWN"
	ErrorTxDBOutcomeUnknownAuditPending              ErrorCode = "TX_DB_OUTCOME_UNKNOWN_AUDIT_PENDING"
	ErrorTxCommittedAuditDurabilityLost              ErrorCode = "TX_COMMITTED_AUDIT_DURABILITY_LOST"
	ErrorTxNotCommittedAuditDurabilityLost           ErrorCode = "TX_NOT_COMMITTED_AUDIT_DURABILITY_LOST"
	ErrorTxDBOutcomeUnknownAuditDurabilityLost       ErrorCode = "TX_DB_OUTCOME_UNKNOWN_AUDIT_DURABILITY_LOST"
	ErrorAuditBarrierUnavailableBeforeTx             ErrorCode = "AUDIT_BARRIER_UNAVAILABLE_BEFORE_TX"
	ErrorAuditStatementBarrierFailed                 ErrorCode = "AUDIT_STATEMENT_BARRIER_FAILED"
	ErrorAuditCommitIntentFailed                     ErrorCode = "AUDIT_COMMIT_INTENT_FAILED"
	ErrorAuditEmergencyWALUnavailable                ErrorCode = "AUDIT_EMERGENCY_WAL_UNAVAILABLE"
	ErrorAuditEventUUIDConflict                      ErrorCode = "AUDIT_EVENT_UUID_CONFLICT"
	ErrorFinalFencePendingCommitted                  ErrorCode = "FINAL_FENCE_PENDING_COMMITTED"
	ErrorFinalFencePendingNotCommitted               ErrorCode = "FINAL_FENCE_PENDING_NOT_COMMITTED"
	ErrorFinalFencePendingUnknown                    ErrorCode = "FINAL_FENCE_PENDING_UNKNOWN"
	ErrorAuthDMLBinderRequired                       ErrorCode = "AUTH_DML_BINDER_REQUIRED"
	ErrorAuthDMLActionMissing                        ErrorCode = "AUTH_DML_ACTION_MISSING"
	ErrorAuthDMLWriteTargetGrantMissing              ErrorCode = "AUTH_DML_WRITE_TARGET_GRANT_MISSING"
	ErrorAuthDMLReferenceGrantMissing                ErrorCode = "AUTH_DML_REFERENCE_GRANT_MISSING"
	ErrorAuthImplicitObjectUnclosed                  ErrorCode = "AUTH_IMPLICIT_OBJECT_UNCLOSED"
	ErrorAuthClosureUnsupported                      ErrorCode = "AUTH_CLOSURE_UNSUPPORTED"
	ErrorAuthConstraintClosureUnsupported            ErrorCode = "AUTH_CONSTRAINT_CLOSURE_UNSUPPORTED"
	ErrorAuthTypeClosureUnsupported                  ErrorCode = "AUTH_TYPE_CLOSURE_UNSUPPORTED"
	ErrorAuthDefaultClosureUnsupported               ErrorCode = "AUTH_DEFAULT_CLOSURE_UNSUPPORTED"
	ErrorAuthExpressionClosureUnsupported            ErrorCode = "AUTH_EXPRESSION_CLOSURE_UNSUPPORTED"
	ErrorAuthRewriteClosureUnsupported               ErrorCode = "AUTH_REWRITE_CLOSURE_UNSUPPORTED"
	ErrorAuthRelationKindUnsupported                 ErrorCode = "AUTH_RELATION_KIND_UNSUPPORTED"
	ErrorAuthInternalObjectDenied                    ErrorCode = "AUTH_INTERNAL_OBJECT_DENIED"
	ErrorAuthWholeRowUnsupported                     ErrorCode = "AUTH_WHOLE_ROW_UNSUPPORTED"
	ErrorAuthCatalogRace                             ErrorCode = "AUTH_CATALOG_RACE"
	ErrorTxCommittedConnectionQuarantined            ErrorCode = "TX_COMMITTED_CONNECTION_QUARANTINED"
	ErrorTxNotCommittedConnectionQuarantined         ErrorCode = "TX_NOT_COMMITTED_CONNECTION_QUARANTINED"
	ErrorTxUnknownConnectionQuarantined              ErrorCode = "TX_UNKNOWN_CONNECTION_QUARANTINED"
	ErrorDialectTransactionUnsupported               ErrorCode = "DIALECT_TRANSACTION_UNSUPPORTED"
)

type SchemaRef struct {
	ID      string `json:"id"`
	Version uint16 `json:"version"`
}

type TerminalEvidenceRef struct {
	Schema      SchemaRef           `json:"schema"`
	Operation   TerminalOperation   `json:"operation"`
	WritePhase  WritePhase          `json:"write_phase"`
	Reply       ProtocolReply       `json:"reply"`
	Consistency EvidenceConsistency `json:"consistency"`
	Digest      string              `json:"digest"`
}

type CanonicalEvent struct {
	Schema                SchemaRef             `json:"schema"`
	EventUUID             string                `json:"event_uuid"`
	EventType             string                `json:"event_type"`
	OccurredAt            time.Time             `json:"occurred_at"`
	TenantID              string                `json:"tenant_id"`
	PrincipalID           string                `json:"principal_id"`
	AgentID               string                `json:"agent_id"`
	DatasourceID          string                `json:"datasource_id"`
	SessionID             string                `json:"session_id"`
	TransactionID         string                `json:"transaction_id"`
	RequestID             string                `json:"request_id"`
	OwnerEpoch            uint64                `json:"owner_epoch"`
	TransactionSequence   uint64                `json:"transaction_seq"`
	PreviousTxEventDigest string                `json:"previous_tx_event_digest"`
	Action                DMLAction             `json:"action,omitempty"`
	TransactionStatus     TransactionStatus     `json:"transaction_status"`
	TransactionPhase      TransactionPhase      `json:"transaction_phase"`
	DBOutcome             DBOutcome             `json:"db_outcome"`
	TerminalEvidence      *TerminalEvidenceRef  `json:"terminal_evidence,omitempty"`
	ConnectionDisposition ConnectionDisposition `json:"connection_disposition"`
	ErrorCode             ErrorCode             `json:"error_code,omitempty"`
	Effect                TxEffect              `json:"effect"`
}

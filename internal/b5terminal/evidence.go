package b5terminal

import (
	"crypto/subtle"

	"github.com/cuipengdba/agentsql/internal/b5"
)

// TerminalOperation is the operation selected by the single terminal-owner
// CAS. An operation value is never inferred from a driver error string.
type TerminalOperation = b5.TerminalOperation

const (
	OperationUnknown  = b5.OperationUnknown
	OperationCommit   = b5.OperationCommit
	OperationRollback = b5.OperationRollback
)

// WritePhase is connector-observed write progress for one terminal frame.
type WritePhase = b5.WritePhase

const (
	WritePhaseUnknown  = b5.WritePhaseUnknown
	WriteNotSent       = b5.WriteNotSent
	WriteZeroBytes     = b5.WriteZeroBytes
	WritePartial       = b5.WritePartial
	WriteFullFrame     = b5.WriteFullFrame
	WriteIndeterminate = b5.WriteIndeterminate
)

// WriteEvidence binds a phase to the connector's byte accounting. FrameBytes
// is the complete encoded terminal-frame length. BytesWritten is exact for all
// phases except WriteIndeterminate, where it is only the last known count.
type WriteEvidence struct {
	Phase        WritePhase
	BytesWritten uint64
	FrameBytes   uint64
}

type TerminalReplyKind uint8

const (
	ReplyKindUnknown TerminalReplyKind = iota
	ReplyNoReply
	ReplyCommitPositiveACK
	ReplyCommitDeferredErrorRejection
	ReplyRollbackPositiveACK
	ReplyRollbackDefinitiveRejection
	ReplyUntyped
)

type CorrelationStrength = b5.ReplyCorrelation

const (
	CorrelationUnknown                = b5.CorrelationUnknown
	CorrelationNotApplicable          = b5.CorrelationNotApplicable
	CorrelationWeakOrAbsent           = b5.CorrelationWeakOrAbsent
	CorrelationStrongCurrentOperation = b5.CorrelationStrongCurrentOperation
)

// CorrelationProof records the minimum facts needed to call a PostgreSQL
// reply current. PostgreSQL does not put a request ID in these replies, so a
// strong claim is valid only from an exclusive decoder at a clean boundary.
type CorrelationProof struct {
	Strength                CorrelationStrength
	CleanCommandBoundary    bool
	UniqueSocketOwner       bool
	DecoderAfterSendPermit  bool
	NoUnreadOrPendingAtSend bool
	MonotonicTranscript     bool
	TranscriptDigest        [32]byte
}

// TerminalReply is one decoded terminal reply. Multiple entries in Evidence
// are retained so duplicate ACKs and mutually exclusive positive/rejection
// replies can be rejected instead of silently choosing one.
type TerminalReply struct {
	Kind                  TerminalReplyKind
	Operation             TerminalOperation
	TransactionGeneration uint64
	AttemptGeneration     uint64
	Correlation           CorrelationProof
}

type ServerTxState = b5.ServerTxStatus

const (
	ServerStatusUnknown                = b5.ServerStatusUnknown
	ServerStatusNotObserved            = b5.ServerStatusNotObserved
	ServerReadyIdle                    = b5.ServerReadyIdle
	ServerReadyIdleInTransaction       = b5.ServerReadyIdleInTransaction
	ServerReadyFailed                  = b5.ServerReadyFailed
	ServerStatusUnknownOrContradictory = b5.ServerStatusUnknownOrContradictory
)

// ServerTxStatus is one ReadyForQuery observation. Evidence retains all
// observations so a duplicate RFQ or a status change is contradictory.
type ServerTxStatus struct {
	State                 ServerTxState
	TransactionGeneration uint64
	AttemptGeneration     uint64
}

// Evidence is the immutable input to CheckEvidence and ResolveTerminal.
type Evidence struct {
	Schema                string
	SchemaVersion         uint16
	Operation             TerminalOperation
	TransactionGeneration uint64
	AttemptGeneration     uint64
	OwnerGeneration       uint64
	Write                 WriteEvidence
	Replies               []TerminalReply
	ServerStatuses        []ServerTxStatus
}

type ConsistencyVerdict = b5.EvidenceConsistency

const (
	VerdictUnknown       = b5.EvidenceInsufficient
	VerdictConsistent    = b5.EvidenceConsistent
	VerdictContradiction = b5.EvidenceContradictory
)

type ConsistencyReason uint8

const (
	ReasonNone ConsistencyReason = iota
	ReasonUnknownSchema
	ReasonInvalidWriteAccounting
	ReasonInsufficientCorrelation
	ReasonUntypedReply
	ReasonServerStatusInsufficient
	ReasonWrongOperation
	ReasonStaleOrFutureGeneration
	ReasonDuplicateTerminalReply
	ReasonPositiveAndRejection
	ReasonDuplicateOrChangingRFQ
	ReasonReplyStatusContradiction
	ReasonExplicitLocalWriteContradiction
	ReasonInvalidStrongCorrelation
)

type ConsistencyResult struct {
	Verdict ConsistencyVerdict
	Reason  ConsistencyReason
}

type replyClass uint8

const (
	replyMissing replyClass = iota
	replyCurrentStrong
	replyCurrentWeak
	replyWrongOrStale
	replyUntyped
	replyDuplicate
)

// phaseReplyTable is the frozen five-row, six-column table from design v4.
// Cross-field checks are deliberately performed before indexing this table.
var phaseReplyTable = [5][6]ConsistencyVerdict{
	// missing, strong P/R, weak P/R, wrong/stale, untyped, duplicate
	{VerdictConsistent, VerdictContradiction, VerdictContradiction, VerdictContradiction, VerdictUnknown, VerdictContradiction}, // NotSent
	{VerdictConsistent, VerdictContradiction, VerdictContradiction, VerdictContradiction, VerdictUnknown, VerdictContradiction}, // Zero
	{VerdictConsistent, VerdictContradiction, VerdictContradiction, VerdictContradiction, VerdictUnknown, VerdictContradiction}, // Partial
	{VerdictConsistent, VerdictConsistent, VerdictUnknown, VerdictContradiction, VerdictUnknown, VerdictContradiction},          // Full
	{VerdictConsistent, VerdictConsistent, VerdictContradiction, VerdictContradiction, VerdictUnknown, VerdictContradiction},    // Indeterminate
}

// FrozenEvidenceTableRows and FrozenEvidenceTableCells make the reviewed
// table shape explicit for tests and schema-freeze review.
const (
	FrozenEvidenceTableRows  = 5
	FrozenEvidenceTableCells = 30
)

// CheckEvidence evaluates all evidence before DB outcome or connection
// disposition. It never uses error text.
func CheckEvidence(e Evidence) ConsistencyResult {
	if !validEvidenceHeader(e) || !validWrite(e.Write) {
		return ConsistencyResult{Verdict: VerdictUnknown, Reason: reasonForInvalidEvidence(e)}
	}

	reply, class, short := classifyReplies(e)
	if short.Reason != ReasonNone {
		return short
	}
	status, statusShort := classifyStatuses(e)
	if statusShort.Reason != ReasonNone {
		return statusShort
	}

	if class == replyCurrentStrong || class == replyCurrentWeak {
		if status != ServerStatusNotObserved && status != ServerReadyIdle {
			return ConsistencyResult{Verdict: VerdictContradiction, Reason: ReasonReplyStatusContradiction}
		}
	}
	if class == replyMissing && status != ServerStatusNotObserved && status != ServerReadyIdle {
		return ConsistencyResult{Verdict: VerdictUnknown, Reason: ReasonServerStatusInsufficient}
	}

	row := writePhaseRow(e.Write.Phase)
	verdict := phaseReplyTable[row][class]
	result := ConsistencyResult{Verdict: verdict}
	switch {
	case verdict == VerdictConsistent:
		result.Reason = ReasonNone
	case verdict == VerdictUnknown && class == replyUntyped:
		result.Reason = ReasonUntypedReply
	case verdict == VerdictUnknown:
		result.Reason = ReasonInsufficientCorrelation
	case class == replyCurrentStrong || class == replyCurrentWeak:
		result.Reason = ReasonExplicitLocalWriteContradiction
	default:
		result.Reason = ReasonDuplicateTerminalReply
	}
	_ = reply // retained for clarity: classification validates the selected reply.
	return result
}

func validEvidenceHeader(e Evidence) bool {
	return e.Schema == TerminalEvidenceSchema &&
		e.SchemaVersion == TerminalEvidenceVersion &&
		(e.Operation == OperationCommit || e.Operation == OperationRollback) &&
		e.TransactionGeneration != 0 && e.AttemptGeneration != 0 && e.OwnerGeneration != 0
}

func reasonForInvalidEvidence(e Evidence) ConsistencyReason {
	if e.Schema != TerminalEvidenceSchema || e.SchemaVersion != TerminalEvidenceVersion || e.Operation == OperationUnknown || e.TransactionGeneration == 0 || e.AttemptGeneration == 0 || e.OwnerGeneration == 0 {
		return ReasonUnknownSchema
	}
	return ReasonInvalidWriteAccounting
}

func validWrite(write WriteEvidence) bool {
	if writePhaseRow(write.Phase) < 0 || write.FrameBytes == 0 {
		return false
	}
	switch write.Phase {
	case WriteNotSent, WriteZeroBytes:
		return write.BytesWritten == 0
	case WritePartial:
		return write.BytesWritten > 0 && write.BytesWritten < write.FrameBytes
	case WriteFullFrame:
		return write.BytesWritten == write.FrameBytes
	case WriteIndeterminate:
		return write.BytesWritten <= write.FrameBytes
	default:
		return false
	}
}

func writePhaseRow(phase WritePhase) int {
	switch phase {
	case WriteNotSent:
		return 0
	case WriteZeroBytes:
		return 1
	case WritePartial:
		return 2
	case WriteFullFrame:
		return 3
	case WriteIndeterminate:
		return 4
	default:
		return -1
	}
}

func classifyReplies(e Evidence) (TerminalReply, replyClass, ConsistencyResult) {
	var none TerminalReply
	if len(e.Replies) == 0 || (len(e.Replies) == 1 && e.Replies[0].Kind == ReplyNoReply) {
		return none, replyMissing, ConsistencyResult{}
	}
	if len(e.Replies) > 1 {
		positive, rejection := false, false
		for _, reply := range e.Replies {
			positive = positive || isPositive(reply.Kind)
			rejection = rejection || isRejection(reply.Kind)
		}
		reason := ReasonDuplicateTerminalReply
		if positive && rejection {
			reason = ReasonPositiveAndRejection
		}
		return none, replyDuplicate, ConsistencyResult{Verdict: VerdictContradiction, Reason: reason}
	}

	reply := e.Replies[0]
	if reply.Kind == ReplyKindUnknown || reply.Kind > ReplyUntyped {
		return reply, replyUntyped, ConsistencyResult{Verdict: VerdictUnknown, Reason: ReasonUnknownSchema}
	}
	if reply.Kind == ReplyUntyped {
		return reply, replyUntyped, ConsistencyResult{}
	}
	if !replyKindMatchesOperation(reply.Kind, reply.Operation) || reply.Operation != e.Operation {
		return reply, replyWrongOrStale, ConsistencyResult{Verdict: VerdictContradiction, Reason: ReasonWrongOperation}
	}
	if reply.TransactionGeneration != e.TransactionGeneration || reply.AttemptGeneration != e.AttemptGeneration {
		return reply, replyWrongOrStale, ConsistencyResult{Verdict: VerdictContradiction, Reason: ReasonStaleOrFutureGeneration}
	}
	if reply.Correlation.Strength == CorrelationStrongCurrentOperation {
		if !validStrongCorrelation(reply.Correlation) {
			return reply, replyCurrentStrong, ConsistencyResult{Verdict: VerdictContradiction, Reason: ReasonInvalidStrongCorrelation}
		}
		return reply, replyCurrentStrong, ConsistencyResult{}
	}
	if reply.Correlation.Strength != CorrelationWeakOrAbsent && reply.Correlation.Strength != CorrelationNotApplicable {
		return reply, replyCurrentWeak, ConsistencyResult{Verdict: VerdictUnknown, Reason: ReasonUnknownSchema}
	}
	return reply, replyCurrentWeak, ConsistencyResult{}
}

func classifyStatuses(e Evidence) (ServerTxState, ConsistencyResult) {
	if len(e.ServerStatuses) == 0 || (len(e.ServerStatuses) == 1 && e.ServerStatuses[0].State == ServerStatusNotObserved) {
		return ServerStatusNotObserved, ConsistencyResult{}
	}
	if len(e.ServerStatuses) > 1 {
		return ServerStatusUnknownOrContradictory, ConsistencyResult{Verdict: VerdictContradiction, Reason: ReasonDuplicateOrChangingRFQ}
	}
	status := e.ServerStatuses[0]
	if status.State != ServerReadyIdle && status.State != ServerReadyIdleInTransaction && status.State != ServerReadyFailed && status.State != ServerStatusUnknownOrContradictory {
		return status.State, ConsistencyResult{Verdict: VerdictUnknown, Reason: ReasonUnknownSchema}
	}
	if status.TransactionGeneration != e.TransactionGeneration || status.AttemptGeneration != e.AttemptGeneration {
		return status.State, ConsistencyResult{Verdict: VerdictContradiction, Reason: ReasonStaleOrFutureGeneration}
	}
	return status.State, ConsistencyResult{}
}

func validStrongCorrelation(c CorrelationProof) bool {
	zero := [32]byte{}
	return c.CleanCommandBoundary && c.UniqueSocketOwner && c.DecoderAfterSendPermit &&
		c.NoUnreadOrPendingAtSend && c.MonotonicTranscript &&
		subtle.ConstantTimeCompare(c.TranscriptDigest[:], zero[:]) == 0
}

func replyKindMatchesOperation(kind TerminalReplyKind, operation TerminalOperation) bool {
	switch kind {
	case ReplyCommitPositiveACK, ReplyCommitDeferredErrorRejection:
		return operation == OperationCommit
	case ReplyRollbackPositiveACK, ReplyRollbackDefinitiveRejection:
		return operation == OperationRollback
	default:
		return false
	}
}

func isPositive(kind TerminalReplyKind) bool {
	return kind == ReplyCommitPositiveACK || kind == ReplyRollbackPositiveACK
}

func isRejection(kind TerminalReplyKind) bool {
	return kind == ReplyCommitDeferredErrorRejection || kind == ReplyRollbackDefinitiveRejection
}

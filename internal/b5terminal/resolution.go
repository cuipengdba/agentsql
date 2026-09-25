package b5terminal

import "github.com/cuipengdba/agentsql/internal/b5"

type DBOutcome = b5.DBOutcome

const (
	OutcomeUnknown        = b5.OutcomeUnknown
	OutcomeCommitted      = b5.OutcomeCommitted
	OutcomeCommitRejected = b5.OutcomeNotCommitted
	OutcomeNotCommitted   = b5.OutcomeNotCommitted
)

type ConnectionDisposition = b5.ConnectionDisposition

const (
	DispositionUnknown            = b5.DispositionUnknown
	DispositionReleased           = b5.DispositionReleased
	DispositionDiscarded          = b5.DispositionDiscarded
	DispositionDiscardUnconfirmed = b5.DispositionDiscardUnconfirmed
)

// NoCommitEverSent is independent coordinator evidence. Connector evidence may
// not manufacture it, particularly when that connector is contradictory.
type NoCommitEverSent struct {
	Schema                     string
	SchemaVersion              uint16
	TransactionGeneration      uint64
	OwnerGeneration            uint64
	HistoryContinuous          bool
	UniqueSocketOwner          bool
	CommitSendPermitEverOpened bool
}

func (proof NoCommitEverSent) ValidFor(e Evidence) bool {
	return proof.Schema == NoCommitEverSentSchema &&
		proof.SchemaVersion == NoCommitEverSentVersion &&
		proof.TransactionGeneration == e.TransactionGeneration &&
		proof.OwnerGeneration == e.OwnerGeneration &&
		proof.HistoryContinuous && proof.UniqueSocketOwner &&
		!proof.CommitSendPermitEverOpened
}

type ReleaseChecks struct {
	DrainComplete     bool
	SessionReset      bool
	HealthCheck       bool
	StatusRecheckIdle bool
	PoolTransferCAS   bool
}

func (checks ReleaseChecks) AllPassed() bool {
	return checks.DrainComplete && checks.SessionReset && checks.HealthCheck &&
		checks.StatusRecheckIdle && checks.PoolTransferCAS
}

type ResolutionContext struct {
	NoCommitProof           NoCommitEverSent
	CancelEmission          CancelEmission
	ReleaseChecks           ReleaseChecks
	BackendAbsenceConfirmed bool
}

type TerminalResolution struct {
	Schema        string
	SchemaVersion uint16
	Consistency   ConsistencyResult
	Outcome       DBOutcome
	Disposition   ConnectionDisposition
}

// ResolveTerminal is total over every Evidence value. Contradictory and
// unknown-schema evidence short-circuit before nominal outcome/disposition.
func ResolveTerminal(e Evidence, context ResolutionContext) TerminalResolution {
	consistency := CheckEvidence(e)
	proofValid := context.NoCommitProof.ValidFor(e)
	outcome := resolveOutcome(e, consistency, proofValid)
	disposition := resolveDisposition(e, context, consistency, proofValid)
	return TerminalResolution{Schema: ConnectionDispositionSchema, SchemaVersion: ConnectionDispositionVersion, Consistency: consistency, Outcome: outcome, Disposition: disposition}
}

func resolveOutcome(e Evidence, consistency ConsistencyResult, noCommit bool) DBOutcome {
	if consistency.Verdict == VerdictContradiction || consistency.Reason == ReasonUnknownSchema || consistency.Reason == ReasonInvalidWriteAccounting {
		if e.Operation == OperationRollback && noCommit {
			return OutcomeNotCommitted
		}
		return OutcomeUnknown
	}
	if consistency.Verdict == VerdictUnknown {
		if e.Operation == OperationRollback {
			if noCommit {
				return OutcomeNotCommitted
			}
			return OutcomeUnknown
		}
		if noTypedReply(e) && (e.Write.Phase == WriteNotSent || e.Write.Phase == WriteZeroBytes) {
			return OutcomeNotCommitted
		}
		return OutcomeUnknown
	}

	reply, ok := singleTypedReply(e)
	if e.Operation == OperationCommit {
		if ok {
			switch reply.Kind {
			case ReplyCommitPositiveACK:
				return OutcomeCommitted
			case ReplyCommitDeferredErrorRejection:
				return OutcomeCommitRejected
			}
		}
		if e.Write.Phase == WriteNotSent || e.Write.Phase == WriteZeroBytes {
			return OutcomeNotCommitted
		}
		return OutcomeUnknown
	}

	if noCommit {
		return OutcomeNotCommitted
	}
	return OutcomeUnknown
}

func resolveDisposition(e Evidence, context ResolutionContext, consistency ConsistencyResult, noCommit bool) ConnectionDisposition {
	if context.CancelEmission != CancelNotSentProven || consistency.Verdict != VerdictConsistent {
		return discardDisposition(context.BackendAbsenceConfirmed)
	}
	reply, ok := singleTypedReply(e)
	statusIdle := len(e.ServerStatuses) == 1 && e.ServerStatuses[0].State == ServerReadyIdle
	releaseCandidate := false
	if ok && statusIdle {
		switch reply.Kind {
		case ReplyCommitPositiveACK, ReplyCommitDeferredErrorRejection:
			releaseCandidate = e.Operation == OperationCommit
		case ReplyRollbackPositiveACK:
			releaseCandidate = e.Operation == OperationRollback && noCommit
		}
	}
	if releaseCandidate && context.ReleaseChecks.AllPassed() {
		return DispositionReleased
	}
	return discardDisposition(context.BackendAbsenceConfirmed)
}

func discardDisposition(confirmed bool) ConnectionDisposition {
	if confirmed {
		return DispositionDiscarded
	}
	return DispositionDiscardUnconfirmed
}

func singleTypedReply(e Evidence) (TerminalReply, bool) {
	if len(e.Replies) != 1 {
		return TerminalReply{}, false
	}
	reply := e.Replies[0]
	return reply, reply.Kind >= ReplyCommitPositiveACK && reply.Kind <= ReplyRollbackDefinitiveRejection
}

func noTypedReply(e Evidence) bool {
	_, ok := singleTypedReply(e)
	return !ok
}

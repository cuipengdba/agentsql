package b5dml

import (
	"sync"

	"github.com/cuipengdba/agentsql/internal/b5terminal"
)

type BeginResourceState uint8

const (
	BeginNoConnection BeginResourceState = iota
	BeginPinnedNotStarted
	BeginPinnedRejected
	BeginACKLostOrIndeterminate
	BeginNativeBegun
)

type BeginReplyKind uint8

const (
	BeginReplyMissing BeginReplyKind = iota
	BeginReplyPositiveACK
	BeginReplyDefinitiveRejection
	BeginReplyUntypedOrMismatched
)

type BeginAttemptEvidence struct {
	Write             b5terminal.WriteEvidence
	Reply             BeginReplyKind
	Correlation       b5terminal.CorrelationStrength
	ServerStatus      b5terminal.ServerTxState
	ProtocolUntouched bool
}

type ApprovalDisposition string

const ApprovalConsumedBeginFailed ApprovalDisposition = "consumed_begin_failed"

type BeginCleanupAction uint8

const (
	BeginCleanupNone BeginCleanupAction = iota
	BeginCleanupReleaseUnusedClaim
	BeginCleanupReleaseConnection
	BeginCleanupDiscardConfirmed
	BeginCleanupQuarantine
	BeginCleanupFinishRollback
)

type BeginCleanupDecision struct {
	Schema        string
	SchemaVersion uint16
	Resource      BeginResourceState
	Action        BeginCleanupAction
	Disposition   b5terminal.ConnectionDisposition
	Approval      ApprovalDisposition
	Attempt       b5terminal.TerminalAttempt
	TerminalOwned bool
}

// BeginMachine owns the resource-fact state for one consumed begin. Cleanup
// is idempotent and, for a native transaction, acquires S4a's generation-bound
// rollback owner exactly once.
type BeginMachine struct {
	mu                    sync.Mutex
	transactionGeneration uint64
	ownerGeneration       uint64
	terminal              *b5terminal.TerminalOwner
	state                 BeginResourceState
	evidence              BeginAttemptEvidence
	decision              *BeginCleanupDecision
}

func NewBeginMachine(transactionGeneration, ownerGeneration uint64, terminal *b5terminal.TerminalOwner) *BeginMachine {
	return &BeginMachine{
		transactionGeneration: transactionGeneration,
		ownerGeneration:       ownerGeneration,
		terminal:              terminal,
		state:                 BeginNoConnection,
	}
}

func (machine *BeginMachine) PinConnection() bool {
	machine.mu.Lock()
	defer machine.mu.Unlock()
	if machine.decision != nil || machine.state != BeginNoConnection {
		return false
	}
	machine.state = BeginPinnedNotStarted
	machine.evidence = BeginAttemptEvidence{
		Write: b5terminal.WriteEvidence{Phase: b5terminal.WriteNotSent, FrameBytes: 1},
		Reply: BeginReplyMissing, ServerStatus: b5terminal.ServerStatusNotObserved,
		ProtocolUntouched: true,
	}
	return true
}

func (machine *BeginMachine) ObserveBegin(evidence BeginAttemptEvidence) bool {
	machine.mu.Lock()
	defer machine.mu.Unlock()
	if machine.decision != nil || machine.state == BeginNoConnection || machine.state == BeginNativeBegun {
		return false
	}
	machine.evidence = evidence
	machine.state = classifyBegin(evidence)
	return true
}

func (machine *BeginMachine) ResourceState() BeginResourceState {
	machine.mu.Lock()
	defer machine.mu.Unlock()
	return machine.state
}

func (machine *BeginMachine) Cleanup(checks b5terminal.ReleaseChecks, backendAbsenceConfirmed bool) BeginCleanupDecision {
	machine.mu.Lock()
	defer machine.mu.Unlock()
	if machine.decision != nil {
		return *machine.decision
	}
	decision := BeginCleanupDecision{
		Schema: BeginCleanupProofSchema, SchemaVersion: BeginCleanupProofVersion, Resource: machine.state,
		Approval: ApprovalConsumedBeginFailed,
	}
	switch machine.state {
	case BeginNoConnection:
		decision.Action = BeginCleanupReleaseUnusedClaim
		decision.Disposition = b5terminal.DispositionUnknown
	case BeginPinnedNotStarted:
		if machine.evidence.ProtocolUntouched && checks.AllPassed() {
			decision.Action = BeginCleanupReleaseConnection
			decision.Disposition = b5terminal.DispositionReleased
		} else {
			setDiscardDecision(&decision, backendAbsenceConfirmed)
		}
	case BeginPinnedRejected:
		if checks.AllPassed() {
			decision.Action = BeginCleanupReleaseConnection
			decision.Disposition = b5terminal.DispositionReleased
		} else {
			setDiscardDecision(&decision, backendAbsenceConfirmed)
		}
	case BeginACKLostOrIndeterminate:
		// Never guess that BEGIN failed and never send ROLLBACK on an
		// indeterminate protocol stream.
		setDiscardDecision(&decision, backendAbsenceConfirmed)
	case BeginNativeBegun:
		if machine.terminal != nil {
			attempt, won := machine.terminal.TryRollback(machine.transactionGeneration, machine.ownerGeneration)
			if won {
				decision.Action = BeginCleanupFinishRollback
				decision.Attempt = attempt
				decision.TerminalOwned = true
				decision.Disposition = b5terminal.DispositionUnknown
				break
			}
		}
		// Another terminal owner or an invalid generation means this caller
		// has no cleanup capability. Retain/quarantine rather than touch it.
		decision.Action = BeginCleanupQuarantine
		decision.Disposition = b5terminal.DispositionDiscardUnconfirmed
	}
	machine.decision = &decision
	return decision
}

func classifyBegin(evidence BeginAttemptEvidence) BeginResourceState {
	if !validBeginWrite(evidence.Write) {
		return BeginACKLostOrIndeterminate
	}
	phase := evidence.Write.Phase
	switch evidence.Reply {
	case BeginReplyMissing:
		if phase == b5terminal.WriteNotSent || phase == b5terminal.WriteZeroBytes {
			return BeginPinnedNotStarted
		}
	case BeginReplyDefinitiveRejection:
		if beginReplyMayCover(phase, evidence.Correlation) && evidence.ServerStatus == b5terminal.ServerReadyIdle {
			return BeginPinnedRejected
		}
	case BeginReplyPositiveACK:
		if beginReplyMayCover(phase, evidence.Correlation) && evidence.ServerStatus == b5terminal.ServerReadyIdleInTransaction {
			return BeginNativeBegun
		}
	}
	return BeginACKLostOrIndeterminate
}

func beginReplyMayCover(phase b5terminal.WritePhase, correlation b5terminal.CorrelationStrength) bool {
	return (phase == b5terminal.WriteFullFrame || phase == b5terminal.WriteIndeterminate) &&
		correlation == b5terminal.CorrelationStrongCurrentOperation
}

func validBeginWrite(write b5terminal.WriteEvidence) bool {
	if write.FrameBytes == 0 {
		return false
	}
	switch write.Phase {
	case b5terminal.WriteNotSent, b5terminal.WriteZeroBytes:
		return write.BytesWritten == 0
	case b5terminal.WritePartial:
		return write.BytesWritten > 0 && write.BytesWritten < write.FrameBytes
	case b5terminal.WriteFullFrame:
		return write.BytesWritten == write.FrameBytes
	case b5terminal.WriteIndeterminate:
		return write.BytesWritten <= write.FrameBytes
	default:
		return false
	}
}

func setDiscardDecision(decision *BeginCleanupDecision, backendAbsenceConfirmed bool) {
	if backendAbsenceConfirmed {
		decision.Action = BeginCleanupDiscardConfirmed
		decision.Disposition = b5terminal.DispositionDiscarded
		return
	}
	decision.Action = BeginCleanupQuarantine
	decision.Disposition = b5terminal.DispositionDiscardUnconfirmed
}

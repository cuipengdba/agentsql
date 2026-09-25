package b5dml

import (
	"sync"
	"testing"

	"github.com/cuipengdba/agentsql/internal/b5terminal"
)

func TestBeginCleanupStateMatrix(t *testing.T) {
	t.Parallel()
	allChecks := b5terminal.ReleaseChecks{DrainComplete: true, SessionReset: true,
		HealthCheck: true, StatusRecheckIdle: true, PoolTransferCAS: true}
	tests := []struct {
		name        string
		pin         bool
		evidence    *BeginAttemptEvidence
		checks      b5terminal.ReleaseChecks
		absence     bool
		resource    BeginResourceState
		action      BeginCleanupAction
		disposition b5terminal.ConnectionDisposition
		rollback    bool
	}{
		{name: "no-connection", resource: BeginNoConnection, action: BeginCleanupReleaseUnusedClaim, disposition: b5terminal.DispositionUnknown},
		{name: "pinned-not-started-reuse", pin: true, checks: allChecks, resource: BeginPinnedNotStarted, action: BeginCleanupReleaseConnection, disposition: b5terminal.DispositionReleased},
		{name: "pinned-not-started-quarantine", pin: true, resource: BeginPinnedNotStarted, action: BeginCleanupQuarantine, disposition: b5terminal.DispositionDiscardUnconfirmed},
		{name: "full-rejection-reuse", pin: true, evidence: beginEvidence(b5terminal.WriteFullFrame, BeginReplyDefinitiveRejection, b5terminal.ServerReadyIdle), checks: allChecks,
			resource: BeginPinnedRejected, action: BeginCleanupReleaseConnection, disposition: b5terminal.DispositionReleased},
		{name: "ack-lost-quarantine", pin: true, evidence: beginEvidence(b5terminal.WriteFullFrame, BeginReplyMissing, b5terminal.ServerStatusNotObserved),
			resource: BeginACKLostOrIndeterminate, action: BeginCleanupQuarantine, disposition: b5terminal.DispositionDiscardUnconfirmed},
		{name: "ack-lost-confirmed-discard", pin: true, evidence: beginEvidence(b5terminal.WritePartial, BeginReplyMissing, b5terminal.ServerStatusNotObserved), absence: true,
			resource: BeginACKLostOrIndeterminate, action: BeginCleanupDiscardConfirmed, disposition: b5terminal.DispositionDiscarded},
		{name: "ack-contradicts-not-sent", pin: true, evidence: beginEvidence(b5terminal.WriteNotSent, BeginReplyPositiveACK, b5terminal.ServerReadyIdleInTransaction),
			resource: BeginACKLostOrIndeterminate, action: BeginCleanupQuarantine, disposition: b5terminal.DispositionDiscardUnconfirmed},
		{name: "native-begun", pin: true, evidence: beginEvidence(b5terminal.WriteFullFrame, BeginReplyPositiveACK, b5terminal.ServerReadyIdleInTransaction),
			resource: BeginNativeBegun, action: BeginCleanupFinishRollback, disposition: b5terminal.DispositionUnknown, rollback: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			owner := b5terminal.NewTerminalOwner(11, 3)
			machine := NewBeginMachine(11, 3, owner)
			if test.pin && !machine.PinConnection() {
				t.Fatal("pin failed")
			}
			if test.evidence != nil && !machine.ObserveBegin(*test.evidence) {
				t.Fatal("observe failed")
			}
			decision := machine.Cleanup(test.checks, test.absence)
			if decision.Schema != BeginCleanupProofSchema || decision.Resource != test.resource ||
				decision.Action != test.action || decision.Disposition != test.disposition ||
				decision.Approval != ApprovalConsumedBeginFailed || decision.TerminalOwned != test.rollback {
				t.Fatalf("decision = %+v", decision)
			}
			if test.rollback && (decision.Attempt.Operation != b5terminal.OperationRollback || owner.Snapshot().State != b5terminal.OwnerRollingBack) {
				t.Fatalf("rollback owner = %+v snapshot=%+v", decision.Attempt, owner.Snapshot())
			}
			if !test.rollback && owner.Snapshot().State != b5terminal.OwnerActive {
				t.Fatalf("non-transaction path touched terminal owner: %+v", owner.Snapshot())
			}
			if again := machine.Cleanup(test.checks, test.absence); again != decision {
				t.Fatalf("cleanup not idempotent: first=%+v second=%+v", decision, again)
			}
		})
	}
}

func TestNativeBeginCleanupCASRace(t *testing.T) {
	t.Parallel()
	for iteration := 0; iteration < 1_000; iteration++ {
		owner := b5terminal.NewTerminalOwner(11, 3)
		machine := NewBeginMachine(11, 3, owner)
		machine.PinConnection()
		machine.ObserveBegin(*beginEvidence(b5terminal.WriteFullFrame, BeginReplyPositiveACK, b5terminal.ServerReadyIdleInTransaction))
		start := make(chan struct{})
		decisions := make([]BeginCleanupDecision, 2)
		var group sync.WaitGroup
		group.Add(2)
		for index := range decisions {
			go func(index int) {
				defer group.Done()
				<-start
				decisions[index] = machine.Cleanup(b5terminal.ReleaseChecks{}, false)
			}(index)
		}
		close(start)
		group.Wait()
		if decisions[0] != decisions[1] || !decisions[0].TerminalOwned ||
			owner.Snapshot().AttemptGeneration != 1 || owner.Snapshot().State != b5terminal.OwnerRollingBack {
			t.Fatalf("iteration %d: decisions=%+v owner=%+v", iteration, decisions, owner.Snapshot())
		}
	}
}

func beginEvidence(phase b5terminal.WritePhase, reply BeginReplyKind, status b5terminal.ServerTxState) *BeginAttemptEvidence {
	evidence := &BeginAttemptEvidence{Write: b5terminal.WriteEvidence{Phase: phase, FrameBytes: 10},
		Reply: reply, Correlation: b5terminal.CorrelationStrongCurrentOperation, ServerStatus: status}
	switch phase {
	case b5terminal.WritePartial:
		evidence.Write.BytesWritten = 5
	case b5terminal.WriteFullFrame:
		evidence.Write.BytesWritten = 10
	}
	return evidence
}

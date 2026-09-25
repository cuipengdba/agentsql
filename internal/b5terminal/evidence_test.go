package b5terminal

import (
	"fmt"
	"testing"
)

func TestFrozenPhaseReplyTable(t *testing.T) {
	t.Parallel()
	if FrozenEvidenceTableRows != len(phaseReplyTable) {
		t.Fatalf("row count = %d, want %d", len(phaseReplyTable), FrozenEvidenceTableRows)
	}
	if FrozenEvidenceTableCells != FrozenEvidenceTableRows*len(phaseReplyTable[0]) {
		t.Fatalf("cell count = %d", FrozenEvidenceTableCells)
	}

	want := [5][6]ConsistencyVerdict{
		{VerdictConsistent, VerdictContradiction, VerdictContradiction, VerdictContradiction, VerdictUnknown, VerdictContradiction},
		{VerdictConsistent, VerdictContradiction, VerdictContradiction, VerdictContradiction, VerdictUnknown, VerdictContradiction},
		{VerdictConsistent, VerdictContradiction, VerdictContradiction, VerdictContradiction, VerdictUnknown, VerdictContradiction},
		{VerdictConsistent, VerdictConsistent, VerdictUnknown, VerdictContradiction, VerdictUnknown, VerdictContradiction},
		{VerdictConsistent, VerdictConsistent, VerdictContradiction, VerdictContradiction, VerdictUnknown, VerdictContradiction},
	}
	if phaseReplyTable != want {
		t.Fatalf("frozen phase/reply table changed: got %#v", phaseReplyTable)
	}
}

func TestEvidenceConsistencyExhaustivePhaseReplyStatus(t *testing.T) {
	t.Parallel()
	type replyCase struct {
		name    string
		build   func(Evidence) []TerminalReply
		byPhase [5]ConsistencyVerdict
		current bool
	}
	consistent := VerdictConsistent
	unknown := VerdictUnknown
	contradiction := VerdictContradiction
	replyCases := []replyCase{
		{name: "missing", build: func(Evidence) []TerminalReply { return nil }, byPhase: [5]ConsistencyVerdict{consistent, consistent, consistent, consistent, consistent}},
		{name: "positive-strong", build: func(e Evidence) []TerminalReply {
			return []TerminalReply{currentReply(e, ReplyCommitPositiveACK, strongCorrelation())}
		}, byPhase: [5]ConsistencyVerdict{contradiction, contradiction, contradiction, consistent, consistent}, current: true},
		{name: "rejection-strong", build: func(e Evidence) []TerminalReply {
			return []TerminalReply{currentReply(e, ReplyCommitDeferredErrorRejection, strongCorrelation())}
		}, byPhase: [5]ConsistencyVerdict{contradiction, contradiction, contradiction, consistent, consistent}, current: true},
		{name: "positive-weak", build: func(e Evidence) []TerminalReply {
			return []TerminalReply{currentReply(e, ReplyCommitPositiveACK, weakCorrelation())}
		}, byPhase: [5]ConsistencyVerdict{contradiction, contradiction, contradiction, unknown, contradiction}, current: true},
		{name: "rejection-weak", build: func(e Evidence) []TerminalReply {
			return []TerminalReply{currentReply(e, ReplyCommitDeferredErrorRejection, weakCorrelation())}
		}, byPhase: [5]ConsistencyVerdict{contradiction, contradiction, contradiction, unknown, contradiction}, current: true},
		{name: "wrong-operation", build: func(e Evidence) []TerminalReply {
			reply := currentReply(e, ReplyRollbackPositiveACK, strongCorrelation())
			reply.Operation = OperationRollback
			return []TerminalReply{reply}
		}, byPhase: allVerdicts(contradiction)},
		{name: "stale", build: func(e Evidence) []TerminalReply {
			reply := currentReply(e, ReplyCommitPositiveACK, strongCorrelation())
			reply.AttemptGeneration--
			return []TerminalReply{reply}
		}, byPhase: allVerdicts(contradiction)},
		{name: "future", build: func(e Evidence) []TerminalReply {
			reply := currentReply(e, ReplyCommitPositiveACK, strongCorrelation())
			reply.AttemptGeneration++
			return []TerminalReply{reply}
		}, byPhase: allVerdicts(contradiction)},
		{name: "untyped", build: func(e Evidence) []TerminalReply { return []TerminalReply{{Kind: ReplyUntyped, Operation: e.Operation}} }, byPhase: allVerdicts(unknown)},
		{name: "duplicate-ack", build: func(e Evidence) []TerminalReply {
			reply := currentReply(e, ReplyCommitPositiveACK, strongCorrelation())
			return []TerminalReply{reply, reply}
		}, byPhase: allVerdicts(contradiction)},
		{name: "positive-and-rejection", build: func(e Evidence) []TerminalReply {
			return []TerminalReply{currentReply(e, ReplyCommitPositiveACK, strongCorrelation()), currentReply(e, ReplyCommitDeferredErrorRejection, strongCorrelation())}
		}, byPhase: allVerdicts(contradiction)},
		{name: "invalid-strong-proof", build: func(e Evidence) []TerminalReply {
			proof := strongCorrelation()
			proof.NoUnreadOrPendingAtSend = false
			return []TerminalReply{currentReply(e, ReplyCommitPositiveACK, proof)}
		}, byPhase: allVerdicts(contradiction)},
	}

	type statusCase struct {
		name                string
		build               func(Evidence) []ServerTxStatus
		badForCurrent       bool
		alwaysContradiction bool
	}
	statusCases := []statusCase{
		{name: "not-observed", build: func(Evidence) []ServerTxStatus { return nil }},
		{name: "idle", build: func(e Evidence) []ServerTxStatus { return []ServerTxStatus{currentStatus(e, ServerReadyIdle)} }},
		{name: "idle-in-tx", build: func(e Evidence) []ServerTxStatus {
			return []ServerTxStatus{currentStatus(e, ServerReadyIdleInTransaction)}
		}, badForCurrent: true},
		{name: "failed", build: func(e Evidence) []ServerTxStatus { return []ServerTxStatus{currentStatus(e, ServerReadyFailed)} }, badForCurrent: true},
		{name: "unknown-contradictory", build: func(e Evidence) []ServerTxStatus {
			return []ServerTxStatus{currentStatus(e, ServerStatusUnknownOrContradictory)}
		}, badForCurrent: true},
		{name: "duplicate-rfq", build: func(e Evidence) []ServerTxStatus {
			status := currentStatus(e, ServerReadyIdle)
			return []ServerTxStatus{status, status}
		}, alwaysContradiction: true},
		{name: "stale-rfq", build: func(e Evidence) []ServerTxStatus {
			status := currentStatus(e, ServerReadyIdle)
			status.AttemptGeneration--
			return []ServerTxStatus{status}
		}, alwaysContradiction: true},
	}

	phases := []WritePhase{WriteNotSent, WriteZeroBytes, WritePartial, WriteFullFrame, WriteIndeterminate}
	combinations := 0
	for phaseIndex, phase := range phases {
		for _, replyCase := range replyCases {
			for _, statusCase := range statusCases {
				combinations++
				name := fmt.Sprintf("%v/%s/%s", phase, replyCase.name, statusCase.name)
				t.Run(name, func(t *testing.T) {
					evidence := baseEvidence(OperationCommit, phase)
					evidence.Replies = replyCase.build(evidence)
					evidence.ServerStatuses = statusCase.build(evidence)
					want := replyCase.byPhase[phaseIndex]
					if statusCase.alwaysContradiction {
						want = VerdictContradiction
					} else if statusCase.badForCurrent {
						if replyCase.current {
							want = VerdictContradiction
						} else if want == VerdictConsistent {
							want = VerdictUnknown
						}
					}
					got := CheckEvidence(evidence)
					if got.Verdict != want {
						t.Fatalf("verdict = %v (%v), want %v", got.Verdict, got.Reason, want)
					}
					if want != VerdictConsistent {
						resolved := ResolveTerminal(evidence, ResolutionContext{
							NoCommitProof:           validNoCommitProof(evidence),
							ReleaseChecks:           allReleaseChecks(),
							BackendAbsenceConfirmed: true,
						})
						if resolved.Disposition == DispositionReleased {
							t.Fatalf("non-consistent evidence was RELEASED: %+v", resolved)
						}
					}
				})
			}
		}
	}
	if combinations != 420 {
		t.Fatalf("covered %d combinations, want 420", combinations)
	}
}

func TestUnknownSchemaAndInvalidWriteFailClosed(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*Evidence)
	}{
		{name: "unknown schema", mutate: func(e *Evidence) { e.Schema = "agentsql.b5.terminal-evidence.v999" }},
		{name: "unknown schema version", mutate: func(e *Evidence) { e.SchemaVersion++ }},
		{name: "unknown phase", mutate: func(e *Evidence) { e.Write.Phase = WritePhaseUnknown }},
		{name: "zero with bytes", mutate: func(e *Evidence) { e.Write.Phase, e.Write.BytesWritten = WriteZeroBytes, 1 }},
		{name: "partial at frame boundary", mutate: func(e *Evidence) { e.Write.Phase, e.Write.BytesWritten = WritePartial, e.Write.FrameBytes }},
		{name: "full short write", mutate: func(e *Evidence) { e.Write.Phase, e.Write.BytesWritten = WriteFullFrame, e.Write.FrameBytes-1 }},
		{name: "unknown operation", mutate: func(e *Evidence) { e.Operation = OperationUnknown }},
		{name: "zero attempt generation", mutate: func(e *Evidence) { e.AttemptGeneration = 0 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			evidence := baseEvidence(OperationCommit, WriteFullFrame)
			test.mutate(&evidence)
			result := ResolveTerminal(evidence, ResolutionContext{ReleaseChecks: allReleaseChecks(), BackendAbsenceConfirmed: true})
			if result.Consistency.Verdict != VerdictUnknown || result.Outcome != OutcomeUnknown || result.Disposition != DispositionDiscarded {
				t.Fatalf("result = %+v", result)
			}
		})
	}
}

func TestKnownReplyWithoutReadyForQueryKeepsOutcomeAndDiscards(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		kind    TerminalReplyKind
		outcome DBOutcome
	}{
		{name: "positive ACK plus RFQ NotObserved", kind: ReplyCommitPositiveACK, outcome: OutcomeCommitted},
		{name: "definitive rejection plus RFQ NotObserved", kind: ReplyCommitDeferredErrorRejection, outcome: OutcomeCommitRejected},
		{name: "ACK-to-RFQ blackhole", kind: ReplyCommitPositiveACK, outcome: OutcomeCommitted},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			evidence := baseEvidence(OperationCommit, WriteFullFrame)
			evidence.Replies = []TerminalReply{currentReply(evidence, test.kind, strongCorrelation())}
			result := ResolveTerminal(evidence, ResolutionContext{ReleaseChecks: allReleaseChecks()})
			if result.Consistency.Verdict != VerdictConsistent || result.Outcome != test.outcome {
				t.Fatalf("result = %+v", result)
			}
			if result.Disposition != DispositionDiscardUnconfirmed {
				t.Fatalf("disposition = %v, want DISCARD_UNCONFIRMED", result.Disposition)
			}
		})
	}
}

func TestOutcomeAndDispositionShortCircuit(t *testing.T) {
	t.Parallel()
	t.Run("commit contradiction is unknown and never released", func(t *testing.T) {
		evidence := baseEvidence(OperationCommit, WriteNotSent)
		evidence.Replies = []TerminalReply{currentReply(evidence, ReplyCommitPositiveACK, strongCorrelation())}
		evidence.ServerStatuses = []ServerTxStatus{currentStatus(evidence, ServerReadyIdle)}
		result := ResolveTerminal(evidence, ResolutionContext{ReleaseChecks: allReleaseChecks(), BackendAbsenceConfirmed: true})
		if result.Outcome != OutcomeUnknown || result.Disposition != DispositionDiscarded {
			t.Fatalf("result = %+v", result)
		}
	})

	t.Run("rollback contradiction preserves only independent no-commit proof", func(t *testing.T) {
		evidence := baseEvidence(OperationRollback, WritePartial)
		evidence.Replies = []TerminalReply{currentReply(evidence, ReplyRollbackPositiveACK, strongCorrelation())}
		evidence.ServerStatuses = []ServerTxStatus{currentStatus(evidence, ServerReadyIdle)}
		without := ResolveTerminal(evidence, ResolutionContext{ReleaseChecks: allReleaseChecks(), BackendAbsenceConfirmed: true})
		if without.Outcome != OutcomeUnknown || without.Disposition != DispositionDiscarded {
			t.Fatalf("without proof = %+v", without)
		}
		with := ResolveTerminal(evidence, ResolutionContext{NoCommitProof: validNoCommitProof(evidence), ReleaseChecks: allReleaseChecks(), BackendAbsenceConfirmed: true})
		if with.Outcome != OutcomeNotCommitted || with.Disposition != DispositionDiscarded {
			t.Fatalf("with proof = %+v", with)
		}
	})

	t.Run("indeterminate only strong reply overrides", func(t *testing.T) {
		evidence := baseEvidence(OperationCommit, WriteIndeterminate)
		evidence.Replies = []TerminalReply{currentReply(evidence, ReplyCommitPositiveACK, strongCorrelation())}
		strong := ResolveTerminal(evidence, ResolutionContext{})
		if strong.Consistency.Verdict != VerdictConsistent || strong.Outcome != OutcomeCommitted {
			t.Fatalf("strong = %+v", strong)
		}
		evidence.Replies[0].Correlation = weakCorrelation()
		weak := ResolveTerminal(evidence, ResolutionContext{})
		if weak.Consistency.Verdict != VerdictContradiction || weak.Outcome != OutcomeUnknown {
			t.Fatalf("weak = %+v", weak)
		}
	})

	t.Run("nominal candidates require all lifecycle checks", func(t *testing.T) {
		evidence := baseEvidence(OperationCommit, WriteFullFrame)
		evidence.Replies = []TerminalReply{currentReply(evidence, ReplyCommitPositiveACK, strongCorrelation())}
		evidence.ServerStatuses = []ServerTxStatus{currentStatus(evidence, ServerReadyIdle)}
		released := ResolveTerminal(evidence, ResolutionContext{ReleaseChecks: allReleaseChecks()})
		if released.Disposition != DispositionReleased {
			t.Fatalf("released = %+v", released)
		}
		checks := allReleaseChecks()
		checks.HealthCheck = false
		discarded := ResolveTerminal(evidence, ResolutionContext{ReleaseChecks: checks})
		if discarded.Outcome != OutcomeCommitted || discarded.Disposition != DispositionDiscardUnconfirmed {
			t.Fatalf("failed health check = %+v", discarded)
		}
	})
}

func baseEvidence(operation TerminalOperation, phase WritePhase) Evidence {
	write := WriteEvidence{Phase: phase, FrameBytes: 8}
	switch phase {
	case WritePartial:
		write.BytesWritten = 3
	case WriteFullFrame:
		write.BytesWritten = 8
	case WriteIndeterminate:
		write.BytesWritten = 4
	}
	return Evidence{
		Schema:                TerminalEvidenceSchema,
		SchemaVersion:         TerminalEvidenceVersion,
		Operation:             operation,
		TransactionGeneration: 11,
		AttemptGeneration:     7,
		OwnerGeneration:       3,
		Write:                 write,
	}
}

func currentReply(e Evidence, kind TerminalReplyKind, correlation CorrelationProof) TerminalReply {
	return TerminalReply{
		Kind:                  kind,
		Operation:             e.Operation,
		TransactionGeneration: e.TransactionGeneration,
		AttemptGeneration:     e.AttemptGeneration,
		Correlation:           correlation,
	}
}

func currentStatus(e Evidence, state ServerTxState) ServerTxStatus {
	return ServerTxStatus{State: state, TransactionGeneration: e.TransactionGeneration, AttemptGeneration: e.AttemptGeneration}
}

func strongCorrelation() CorrelationProof {
	var digest [32]byte
	digest[0] = 1
	return CorrelationProof{
		Strength:                CorrelationStrongCurrentOperation,
		CleanCommandBoundary:    true,
		UniqueSocketOwner:       true,
		DecoderAfterSendPermit:  true,
		NoUnreadOrPendingAtSend: true,
		MonotonicTranscript:     true,
		TranscriptDigest:        digest,
	}
}

func weakCorrelation() CorrelationProof {
	return CorrelationProof{Strength: CorrelationWeakOrAbsent}
}

func allVerdicts(verdict ConsistencyVerdict) [5]ConsistencyVerdict {
	return [5]ConsistencyVerdict{verdict, verdict, verdict, verdict, verdict}
}

func validNoCommitProof(e Evidence) NoCommitEverSent {
	return NoCommitEverSent{
		Schema:                     NoCommitEverSentSchema,
		SchemaVersion:              NoCommitEverSentVersion,
		TransactionGeneration:      e.TransactionGeneration,
		OwnerGeneration:            e.OwnerGeneration,
		HistoryContinuous:          true,
		UniqueSocketOwner:          true,
		CommitSendPermitEverOpened: false,
	}
}

func allReleaseChecks() ReleaseChecks {
	return ReleaseChecks{DrainComplete: true, SessionReset: true, HealthCheck: true, StatusRecheckIdle: true, PoolTransferCAS: true}
}

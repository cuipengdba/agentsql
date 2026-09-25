package b5terminal

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestWallDeadlineCommitWindows(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		stage CommitStage
	}{
		{name: "before-send", stage: CommitStageBeforeSend},
		{name: "partial", stage: CommitStagePartialWrite},
		{name: "full", stage: CommitStageFullWriteAwaitingACK},
		{name: "ACK-window", stage: CommitStageACKObservedAwaitingRFQ},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			owner := NewTerminalOwner(11, 3)
			attempt, won := owner.TryCommit(11, 3)
			if !won {
				t.Fatal("commit did not win")
			}
			if test.stage > CommitStageBeforeSend {
				if err := owner.OpenCommitSendPermit(attempt); err != nil {
					t.Fatal(err)
				}
				if err := owner.AdvanceCommitStage(attempt, test.stage); err != nil {
					t.Fatal(err)
				}
			}
			observation := owner.ObserveWallDeadline(11, 3)
			if observation.Decision != TimeoutFinishCommitOnly || observation.Stage != test.stage {
				t.Fatalf("observation = %+v", observation)
			}
			if _, ok := owner.NoCommitProof(attempt); ok {
				t.Fatal("COMMITTING attempt produced NoCommitEverSent")
			}
			if !owner.Snapshot().WallDeadlineObserved {
				t.Fatal("wall deadline diagnostic not retained")
			}
		})
	}
}

func TestTimeoutWinsOnlyBeforeCommitCAS(t *testing.T) {
	t.Parallel()
	owner := NewTerminalOwner(11, 3)
	observation := owner.ObserveWallDeadline(11, 3)
	if observation.Decision != TimeoutWonBeforeCommit {
		t.Fatalf("decision = %v", observation.Decision)
	}
	if _, won := owner.TryCommit(11, 3); won {
		t.Fatal("commit won after timeout quiesced the generation")
	}
	proof, ok := owner.NoCommitProof(observation.Attempt)
	if !ok {
		t.Fatal("timeout winner lacks NoCommitEverSent proof")
	}
	evidence := baseEvidence(OperationRollback, WriteNotSent)
	evidence.TransactionGeneration = 11
	evidence.OwnerGeneration = 3
	evidence.AttemptGeneration = observation.Attempt.AttemptGeneration
	result := ResolveTerminal(evidence, ResolutionContext{NoCommitProof: proof, BackendAbsenceConfirmed: true})
	if result.Outcome != OutcomeNotCommitted {
		t.Fatalf("timeout outcome = %+v", result)
	}
	if err := owner.BeginTimeoutRollback(observation.Attempt); err != nil {
		t.Fatal(err)
	}
	if err := owner.Complete(observation.Attempt, result); err != nil {
		t.Fatal(err)
	}
	got, err := owner.Wait(context.Background())
	if err != nil || got != result {
		t.Fatalf("wait = %+v, %v", got, err)
	}
}

func TestCommitTimeoutCASRaceHasOneOwner(t *testing.T) {
	t.Parallel()
	for iteration := 0; iteration < 1_000; iteration++ {
		owner := NewTerminalOwner(11, 3)
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		var commitWon bool
		var timeout TimeoutObservation
		go func() {
			defer wg.Done()
			<-start
			_, commitWon = owner.TryCommit(11, 3)
		}()
		go func() {
			defer wg.Done()
			<-start
			timeout = owner.ObserveWallDeadline(11, 3)
		}()
		close(start)
		wg.Wait()
		if commitWon {
			if timeout.Decision != TimeoutFinishCommitOnly {
				t.Fatalf("iteration %d: commit won but timeout = %+v", iteration, timeout)
			}
			if owner.Snapshot().State != OwnerCommitting {
				t.Fatalf("iteration %d: state = %v", iteration, owner.Snapshot().State)
			}
		} else {
			if timeout.Decision != TimeoutWonBeforeCommit || owner.Snapshot().State != OwnerQuiescing {
				t.Fatalf("iteration %d: timeout = %+v state=%v", iteration, timeout, owner.Snapshot().State)
			}
		}
	}
}

func TestTerminalOwnerGenerationAndSingleCompletion(t *testing.T) {
	t.Parallel()
	owner := NewTerminalOwner(11, 3)
	if _, won := owner.TryRollback(10, 3); won {
		t.Fatal("stale transaction generation won")
	}
	if _, won := owner.TryRollback(11, 4); won {
		t.Fatal("wrong owner generation won")
	}
	attempt, won := owner.TryRollback(11, 3)
	if !won {
		t.Fatal("rollback did not win")
	}
	result := TerminalResolution{Outcome: OutcomeNotCommitted, Disposition: DispositionDiscarded}
	if err := owner.Complete(attempt, result); err != nil {
		t.Fatal(err)
	}
	if err := owner.Complete(attempt, result); err != ErrStaleTerminalAttempt {
		t.Fatalf("second completion error = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if got, err := owner.Wait(ctx); err != nil || got != result {
		t.Fatalf("wait = %+v, %v", got, err)
	}
}

package b5terminal

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
)

var (
	ErrStaleTerminalAttempt = errors.New("b5terminal: stale terminal attempt")
	ErrInvalidOwnerState    = errors.New("b5terminal: invalid terminal owner state")
)

type OwnerState uint8

const (
	OwnerActive OwnerState = iota
	OwnerQuiescing
	OwnerCommitting
	OwnerRollingBack
	OwnerTerminal
)

type CommitStage uint8

const (
	CommitStageBeforeSend CommitStage = iota
	CommitStagePartialWrite
	CommitStageFullWriteAwaitingACK
	CommitStageACKObservedAwaitingRFQ
)

type TerminalAttempt struct {
	TransactionGeneration uint64
	OwnerGeneration       uint64
	AttemptGeneration     uint64
	Operation             TerminalOperation
	TimeoutOwner          bool
}

type TimeoutDecision uint8

const (
	TimeoutLost TimeoutDecision = iota
	TimeoutWonBeforeCommit
	TimeoutFinishCommitOnly
)

type TimeoutObservation struct {
	Decision TimeoutDecision
	Attempt  TerminalAttempt
	Stage    CommitStage
}

type ownerSnapshot struct {
	state                  OwnerState
	txGeneration           uint64
	ownerGeneration        uint64
	attemptGeneration      uint64
	operation              TerminalOperation
	timeoutOwner           bool
	commitPermitEverOpened bool
	commitStage            CommitStage
	deadlineObserved       bool
}

type TerminalOwnerSnapshot struct {
	State                      OwnerState
	TransactionGeneration      uint64
	OwnerGeneration            uint64
	AttemptGeneration          uint64
	Operation                  TerminalOperation
	CommitSendPermitEverOpened bool
	CommitStage                CommitStage
	WallDeadlineObserved       bool
}

// TerminalOwner is a generation-bound single-owner CAS. The immutable
// snapshots make every race linearize at one atomic compare-and-swap.
type TerminalOwner struct {
	current   atomic.Pointer[ownerSnapshot]
	done      chan struct{}
	once      sync.Once
	mu        sync.Mutex
	result    TerminalResolution
	hasResult bool
}

func NewTerminalOwner(transactionGeneration, ownerGeneration uint64) *TerminalOwner {
	owner := &TerminalOwner{done: make(chan struct{})}
	owner.current.Store(&ownerSnapshot{
		state:           OwnerActive,
		txGeneration:    transactionGeneration,
		ownerGeneration: ownerGeneration,
	})
	return owner
}

func (owner *TerminalOwner) Snapshot() TerminalOwnerSnapshot {
	snapshot := owner.current.Load()
	return TerminalOwnerSnapshot{
		State:                      snapshot.state,
		TransactionGeneration:      snapshot.txGeneration,
		OwnerGeneration:            snapshot.ownerGeneration,
		AttemptGeneration:          snapshot.attemptGeneration,
		Operation:                  snapshot.operation,
		CommitSendPermitEverOpened: snapshot.commitPermitEverOpened,
		CommitStage:                snapshot.commitStage,
		WallDeadlineObserved:       snapshot.deadlineObserved,
	}
}

func (owner *TerminalOwner) TryCommit(transactionGeneration, ownerGeneration uint64) (TerminalAttempt, bool) {
	return owner.tryAcquire(transactionGeneration, ownerGeneration, OperationCommit, false)
}

func (owner *TerminalOwner) TryRollback(transactionGeneration, ownerGeneration uint64) (TerminalAttempt, bool) {
	return owner.tryAcquire(transactionGeneration, ownerGeneration, OperationRollback, false)
}

func (owner *TerminalOwner) tryAcquire(transactionGeneration, ownerGeneration uint64, operation TerminalOperation, timeout bool) (TerminalAttempt, bool) {
	for {
		old := owner.current.Load()
		if old.state != OwnerActive || old.txGeneration != transactionGeneration || old.ownerGeneration != ownerGeneration {
			return TerminalAttempt{}, false
		}
		next := *old
		next.attemptGeneration++
		next.operation = operation
		next.timeoutOwner = timeout
		if timeout {
			next.state = OwnerQuiescing
		} else if operation == OperationCommit {
			next.state = OwnerCommitting
		} else {
			next.state = OwnerRollingBack
		}
		if owner.current.CompareAndSwap(old, &next) {
			return attemptFrom(&next), true
		}
	}
}

// ObserveWallDeadline either wins from ACTIVE before a commit owner exists, or
// becomes diagnostic-only once COMMITTING has won. It never selects a timeout
// NOT_COMMITTED result from COMMITTING, regardless of the current write stage.
func (owner *TerminalOwner) ObserveWallDeadline(transactionGeneration, ownerGeneration uint64) TimeoutObservation {
	for {
		old := owner.current.Load()
		if old.txGeneration != transactionGeneration || old.ownerGeneration != ownerGeneration {
			return TimeoutObservation{Decision: TimeoutLost}
		}
		switch old.state {
		case OwnerActive:
			next := *old
			next.state = OwnerQuiescing
			next.operation = OperationRollback
			next.timeoutOwner = true
			next.attemptGeneration++
			if owner.current.CompareAndSwap(old, &next) {
				return TimeoutObservation{Decision: TimeoutWonBeforeCommit, Attempt: attemptFrom(&next), Stage: next.commitStage}
			}
		case OwnerCommitting:
			next := *old
			next.deadlineObserved = true
			if owner.current.CompareAndSwap(old, &next) {
				return TimeoutObservation{Decision: TimeoutFinishCommitOnly, Attempt: attemptFrom(&next), Stage: next.commitStage}
			}
		default:
			return TimeoutObservation{Decision: TimeoutLost, Attempt: attemptFrom(old), Stage: old.commitStage}
		}
	}
}

func (owner *TerminalOwner) BeginTimeoutRollback(attempt TerminalAttempt) error {
	for {
		old := owner.current.Load()
		if !matchesAttempt(old, attempt) || old.state != OwnerQuiescing || !old.timeoutOwner {
			return ErrStaleTerminalAttempt
		}
		next := *old
		next.state = OwnerRollingBack
		if owner.current.CompareAndSwap(old, &next) {
			return nil
		}
	}
}

func (owner *TerminalOwner) OpenCommitSendPermit(attempt TerminalAttempt) error {
	for {
		old := owner.current.Load()
		if !matchesAttempt(old, attempt) || old.state != OwnerCommitting || old.operation != OperationCommit {
			return ErrStaleTerminalAttempt
		}
		if old.commitPermitEverOpened {
			return nil
		}
		next := *old
		next.commitPermitEverOpened = true
		if owner.current.CompareAndSwap(old, &next) {
			return nil
		}
	}
}

func (owner *TerminalOwner) AdvanceCommitStage(attempt TerminalAttempt, stage CommitStage) error {
	if stage < CommitStageBeforeSend || stage > CommitStageACKObservedAwaitingRFQ {
		return ErrInvalidOwnerState
	}
	for {
		old := owner.current.Load()
		if !matchesAttempt(old, attempt) || old.state != OwnerCommitting {
			return ErrStaleTerminalAttempt
		}
		if stage < old.commitStage {
			return ErrInvalidOwnerState
		}
		if stage > CommitStageBeforeSend && !old.commitPermitEverOpened {
			return ErrInvalidOwnerState
		}
		if stage == old.commitStage {
			return nil
		}
		next := *old
		next.commitStage = stage
		if owner.current.CompareAndSwap(old, &next) {
			return nil
		}
	}
}

// NoCommitProof is emitted only for the timeout owner that won from ACTIVE.
// Once a commit send permit was ever opened, no API can produce a valid proof.
func (owner *TerminalOwner) NoCommitProof(attempt TerminalAttempt) (NoCommitEverSent, bool) {
	snapshot := owner.current.Load()
	if !matchesAttempt(snapshot, attempt) || !snapshot.timeoutOwner || snapshot.commitPermitEverOpened {
		return NoCommitEverSent{}, false
	}
	return NoCommitEverSent{
		Schema:                     NoCommitEverSentSchema,
		SchemaVersion:              NoCommitEverSentVersion,
		TransactionGeneration:      snapshot.txGeneration,
		OwnerGeneration:            snapshot.ownerGeneration,
		HistoryContinuous:          true,
		UniqueSocketOwner:          true,
		CommitSendPermitEverOpened: false,
	}, true
}

func (owner *TerminalOwner) Complete(attempt TerminalAttempt, result TerminalResolution) error {
	for {
		old := owner.current.Load()
		if !matchesAttempt(old, attempt) || old.state == OwnerActive || old.state == OwnerTerminal {
			return ErrStaleTerminalAttempt
		}
		next := *old
		next.state = OwnerTerminal
		if !owner.current.CompareAndSwap(old, &next) {
			continue
		}
		owner.mu.Lock()
		owner.result = result
		owner.hasResult = true
		owner.mu.Unlock()
		owner.once.Do(func() { close(owner.done) })
		return nil
	}
}

func (owner *TerminalOwner) Wait(ctx context.Context) (TerminalResolution, error) {
	select {
	case <-owner.done:
		owner.mu.Lock()
		defer owner.mu.Unlock()
		if !owner.hasResult {
			return TerminalResolution{}, ErrInvalidOwnerState
		}
		return owner.result, nil
	case <-ctx.Done():
		return TerminalResolution{}, ctx.Err()
	}
}

func attemptFrom(snapshot *ownerSnapshot) TerminalAttempt {
	return TerminalAttempt{
		TransactionGeneration: snapshot.txGeneration,
		OwnerGeneration:       snapshot.ownerGeneration,
		AttemptGeneration:     snapshot.attemptGeneration,
		Operation:             snapshot.operation,
		TimeoutOwner:          snapshot.timeoutOwner,
	}
}

func matchesAttempt(snapshot *ownerSnapshot, attempt TerminalAttempt) bool {
	return snapshot.txGeneration == attempt.TransactionGeneration &&
		snapshot.ownerGeneration == attempt.OwnerGeneration &&
		snapshot.attemptGeneration == attempt.AttemptGeneration &&
		snapshot.operation == attempt.Operation &&
		snapshot.timeoutOwner == attempt.TimeoutOwner
}

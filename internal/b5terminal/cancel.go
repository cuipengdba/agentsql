package b5terminal

import (
	"errors"
	"sync"
)

var ErrCancelTainted = errors.New("b5terminal: PostgreSQL connection is cancel-tainted")

type CancelEmission uint8

const (
	CancelNotSentProven CancelEmission = iota
	CancelSendStartedOrIndeterminate
	CancelFullPacketWritten
)

type MainCommand uint8

const (
	CommandRollback MainCommand = iota
	CommandCommit
	CommandReset
	CommandHealthProbe
	CommandStatement
)

type CancelDiagnostics struct {
	ReadyForQueryIdleObserved bool
	ExecuteExited             bool
	CancelSocketClosed        bool
	StatementTimeoutObserved  bool
}

type CancelSnapshot struct {
	Emission    CancelEmission
	Tainted     bool
	Diagnostics CancelDiagnostics
	Sent        map[MainCommand]uint64
}

// CancelGuard serializes the independent cancel send path with any attempt to
// start a new main-connection command. Standard PostgreSQL has no generation-
// bound cancel fence, so taint is monotonic and permanent.
type CancelGuard struct {
	mu          sync.Mutex
	emission    CancelEmission
	diagnostics CancelDiagnostics
	sent        map[MainCommand]uint64
}

func NewCancelGuard() *CancelGuard {
	return &CancelGuard{sent: make(map[MainCommand]uint64)}
}

func (guard *CancelGuard) ObserveEmission(emission CancelEmission) {
	guard.mu.Lock()
	defer guard.mu.Unlock()
	if emission > guard.emission {
		guard.emission = emission
	}
}

func (guard *CancelGuard) RecordMainSend(command MainCommand) error {
	guard.mu.Lock()
	defer guard.mu.Unlock()
	if guard.emission != CancelNotSentProven {
		return ErrCancelTainted
	}
	guard.sent[command]++
	return nil
}

func (guard *CancelGuard) ObserveReadyForQueryIdle() {
	guard.mu.Lock()
	guard.diagnostics.ReadyForQueryIdleObserved = true
	guard.mu.Unlock()
}

func (guard *CancelGuard) ObserveExecuteExit() {
	guard.mu.Lock()
	guard.diagnostics.ExecuteExited = true
	guard.mu.Unlock()
}

func (guard *CancelGuard) ObserveCancelSocketClosed() {
	guard.mu.Lock()
	guard.diagnostics.CancelSocketClosed = true
	guard.mu.Unlock()
}

func (guard *CancelGuard) ObserveStatementTimeout() {
	guard.mu.Lock()
	guard.diagnostics.StatementTimeoutObserved = true
	guard.mu.Unlock()
}

func (guard *CancelGuard) Snapshot() CancelSnapshot {
	guard.mu.Lock()
	defer guard.mu.Unlock()
	sent := make(map[MainCommand]uint64, len(guard.sent))
	for command, count := range guard.sent {
		sent[command] = count
	}
	return CancelSnapshot{
		Emission:    guard.emission,
		Tainted:     guard.emission != CancelNotSentProven,
		Diagnostics: guard.diagnostics,
		Sent:        sent,
	}
}

func (guard *CancelGuard) Disposition(backendAbsenceConfirmed bool) ConnectionDisposition {
	guard.mu.Lock()
	tainted := guard.emission != CancelNotSentProven
	guard.mu.Unlock()
	if !tainted {
		return DispositionUnknown
	}
	return discardDisposition(backendAbsenceConfirmed)
}

// GenerationBoundCancelFence is false for every v0.4 PostgreSQL path. A future
// true value requires a separately reviewed server protocol and attestation.
const GenerationBoundCancelFence = false

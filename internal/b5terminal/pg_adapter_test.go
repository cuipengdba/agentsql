package b5terminal

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

func TestPGAdapterTypedTerminalAndLifecycleMatrix(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		operation       TerminalOperation
		terminalReply   []byte
		writeLimit      int
		writeErr        error
		noCommit        bool
		wantPhase       WritePhase
		wantOutcome     DBOutcome
		wantConsistency ConsistencyVerdict
		wantDisposition ConnectionDisposition
		wantReleased    bool
	}{
		{
			name: "commit positive ack and idle", operation: OperationCommit,
			terminalReply: backendFrames(commandComplete("COMMIT"), readyForQuery('I')),
			wantPhase:     WriteFullFrame, wantOutcome: OutcomeCommitted, wantConsistency: VerdictConsistent,
			wantDisposition: DispositionReleased, wantReleased: true,
		},
		{
			name: "deferred commit rejection is not committed", operation: OperationCommit,
			terminalReply: backendFrames(errorResponse("23514"), readyForQuery('I')),
			wantPhase:     WriteFullFrame, wantOutcome: OutcomeCommitRejected, wantConsistency: VerdictConsistent,
			wantDisposition: DispositionReleased, wantReleased: true,
		},
		{
			name: "positive ack without rfq keeps committed and discards", operation: OperationCommit,
			terminalReply: backendFrames(commandComplete("COMMIT")),
			wantPhase:     WriteFullFrame, wantOutcome: OutcomeCommitted, wantConsistency: VerdictConsistent,
			wantDisposition: DispositionDiscarded,
		},
		{
			name: "rejection without rfq keeps rejected and discards", operation: OperationCommit,
			terminalReply: backendFrames(errorResponse("23514")),
			wantPhase:     WriteFullFrame, wantOutcome: OutcomeCommitRejected, wantConsistency: VerdictConsistent,
			wantDisposition: DispositionDiscarded,
		},
		{
			name: "missing ack with idle cannot manufacture result", operation: OperationCommit,
			terminalReply: backendFrames(readyForQuery('I')),
			wantPhase:     WriteFullFrame, wantOutcome: OutcomeUnknown, wantConsistency: VerdictConsistent,
			wantDisposition: DispositionDiscarded,
		},
		{
			name: "ack with idle in transaction is contradictory", operation: OperationCommit,
			terminalReply: backendFrames(commandComplete("COMMIT"), readyForQuery('T')),
			wantPhase:     WriteFullFrame, wantOutcome: OutcomeUnknown, wantConsistency: VerdictContradiction,
			wantDisposition: DispositionDiscarded,
		},
		{
			name: "ack with failed state is contradictory", operation: OperationCommit,
			terminalReply: backendFrames(commandComplete("COMMIT"), readyForQuery('E')),
			wantPhase:     WriteFullFrame, wantOutcome: OutcomeUnknown, wantConsistency: VerdictContradiction,
			wantDisposition: DispositionDiscarded,
		},
		{
			name: "duplicate ack is contradictory", operation: OperationCommit,
			terminalReply: backendFrames(commandComplete("COMMIT"), commandComplete("COMMIT"), readyForQuery('I')),
			wantPhase:     WriteFullFrame, wantOutcome: OutcomeUnknown, wantConsistency: VerdictContradiction,
			wantDisposition: DispositionDiscarded,
		},
		{
			name: "duplicate rfq is contradictory", operation: OperationCommit,
			terminalReply: backendFrames(commandComplete("COMMIT"), readyForQuery('I'), readyForQuery('I')),
			wantPhase:     WriteFullFrame, wantOutcome: OutcomeUnknown, wantConsistency: VerdictContradiction,
			wantDisposition: DispositionDiscarded,
		},
		{
			name: "partial commit frame is unknown", operation: OperationCommit,
			writeLimit: 3, writeErr: io.ErrUnexpectedEOF,
			wantPhase: WritePartial, wantOutcome: OutcomeUnknown, wantConsistency: VerdictConsistent,
			wantDisposition: DispositionDiscarded,
		},
		{
			name: "zero byte commit is not committed", operation: OperationCommit,
			writeLimit: -1, writeErr: io.ErrClosedPipe,
			wantPhase: WriteZeroBytes, wantOutcome: OutcomeNotCommitted, wantConsistency: VerdictConsistent,
			wantDisposition: DispositionDiscarded,
		},
		{
			name: "rollback ack with no commit proof", operation: OperationRollback, noCommit: true,
			terminalReply: backendFrames(commandComplete("ROLLBACK"), readyForQuery('I')),
			wantPhase:     WriteFullFrame, wantOutcome: OutcomeNotCommitted, wantConsistency: VerdictConsistent,
			wantDisposition: DispositionReleased, wantReleased: true,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			terminalStep := pgScriptStep{response: test.terminalReply, writeLimit: test.writeLimit, writeErr: test.writeErr}
			connection := newPGScriptConn([]pgScriptStep{
				terminalStep,
				{response: backendFrames(commandComplete("DISCARD ALL"), readyForQuery('I'))},
				{response: healthResponse()},
			})
			var released bool
			adapter := mustPGAdapter(t, connection, func(context.Context, net.Conn, PGBackendIdentity) error {
				released = true
				return nil
			}, func(context.Context, PGBackendIdentity) (bool, error) { return true, nil }, PGAdapterOptions{WatchdogTimeout: time.Second})
			owner := NewTerminalOwner(11, 17)
			attempt := acquireAttempt(t, owner, test.operation)
			proof := NoCommitEverSent{}
			if test.noCommit {
				proof = validNoCommitForAttempt(attempt)
			}
			var result PGTerminalResult
			var err error
			if test.operation == OperationCommit {
				result, err = adapter.FinishCommit(context.Background(), owner, attempt)
			} else {
				result, err = adapter.FinishRollback(context.Background(), owner, attempt, proof)
			}
			if err != nil {
				t.Fatal(err)
			}
			if result.Evidence.Write.Phase != test.wantPhase {
				t.Fatalf("write phase = %v (%d/%d), want %v", result.Evidence.Write.Phase, result.Evidence.Write.BytesWritten, result.Evidence.Write.FrameBytes, test.wantPhase)
			}
			if result.Resolution.Outcome != test.wantOutcome || result.Resolution.Consistency.Verdict != test.wantConsistency || result.Resolution.Disposition != test.wantDisposition {
				t.Fatalf("result = %+v, evidence = %+v", result.Resolution, result.Evidence)
			}
			if released != test.wantReleased {
				t.Fatalf("released = %v, want %v", released, test.wantReleased)
			}
			if test.wantReleased && !result.ReleaseChecks.AllPassed() {
				t.Fatalf("release checks = %+v", result.ReleaseChecks)
			}
			if !test.wantReleased && !errors.Is(result.DestroySignal, ErrPGConnectionDiscarded) {
				t.Fatalf("destroy signal = %v", result.DestroySignal)
			}
		})
	}
}

func TestPGAdapterKnownOutcomeSurvivesLifecycleFailure(t *testing.T) {
	t.Parallel()
	connection := newPGScriptConn([]pgScriptStep{
		{response: backendFrames(commandComplete("COMMIT"), readyForQuery('I'))},
		{response: backendFrames(errorResponse("XX000"), readyForQuery('I'))},
	})
	adapter := mustPGAdapter(t, connection, func(context.Context, net.Conn, PGBackendIdentity) error {
		t.Fatal("failed reset must not transfer")
		return nil
	}, func(context.Context, PGBackendIdentity) (bool, error) { return false, nil }, PGAdapterOptions{WatchdogTimeout: time.Second})
	owner := NewTerminalOwner(1, 2)
	attempt := acquireAttempt(t, owner, OperationCommit)
	result, err := adapter.FinishCommit(context.Background(), owner, attempt)
	if err != nil {
		t.Fatal(err)
	}
	if result.Resolution.Outcome != OutcomeCommitted || result.Resolution.Disposition != DispositionDiscardUnconfirmed {
		t.Fatalf("result = %+v", result)
	}
	if !result.ReleaseChecks.DrainComplete || result.ReleaseChecks.SessionReset {
		t.Fatalf("release checks = %+v", result.ReleaseChecks)
	}
}

func TestPGAdapterWatchdogFullFrameACKMissing(t *testing.T) {
	t.Parallel()
	client, server := net.Pipe()
	defer server.Close()
	go func() {
		buffer := make([]byte, 64)
		_, _ = server.Read(buffer)
		// Deliberately never return a PostgreSQL response.
	}()
	adapter := mustPGAdapter(t, client, nil, func(context.Context, PGBackendIdentity) (bool, error) { return false, nil }, PGAdapterOptions{
		WatchdogTimeout: 30 * time.Millisecond,
		ReconcileBudget: 5 * time.Millisecond,
	})
	owner := NewTerminalOwner(3, 4)
	attempt := acquireAttempt(t, owner, OperationCommit)
	started := time.Now()
	result, err := adapter.FinishCommit(context.Background(), owner, attempt)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > 150*time.Millisecond {
		t.Fatalf("watchdog returned after %v", elapsed)
	}
	if !result.WatchdogFired || result.Evidence.Write.Phase != WriteFullFrame || result.Resolution.Outcome != OutcomeUnknown || result.Resolution.Disposition != DispositionDiscardUnconfirmed {
		t.Fatalf("result = %+v", result)
	}
	if !errors.Is(result.DestroySignal, ErrPGConnectionDiscarded) {
		t.Fatalf("destroy signal = %v", result.DestroySignal)
	}
}

func TestPGAdapterCancelRequestPermanentlyTaintsAndSuppressesMainCommands(t *testing.T) {
	t.Parallel()
	mainConnection := newPGScriptConn(nil)
	adapter := mustPGAdapter(t, mainConnection, nil, func(context.Context, PGBackendIdentity) (bool, error) { return true, nil }, PGAdapterOptions{WatchdogTimeout: time.Second})
	cancelConnection := newPGScriptConn([]pgScriptStep{{}})
	emission, err := adapter.SendCancelRequest(context.Background(), func(context.Context) (net.Conn, error) {
		return cancelConnection, nil
	}, 123, 456)
	if err != nil || emission != CancelFullPacketWritten {
		t.Fatalf("cancel emission = %v, err = %v", emission, err)
	}
	owner := NewTerminalOwner(5, 6)
	attempt := acquireAttempt(t, owner, OperationRollback)
	result, err := adapter.FinishRollback(context.Background(), owner, attempt, validNoCommitForAttempt(attempt))
	if err != nil {
		t.Fatal(err)
	}
	if !result.CancelSnapshot.Tainted || result.Resolution.Outcome != OutcomeNotCommitted || result.Resolution.Disposition != DispositionDiscarded {
		t.Fatalf("result = %+v", result)
	}
	for _, command := range []MainCommand{CommandRollback, CommandCommit, CommandReset, CommandHealthProbe} {
		if result.CancelSnapshot.Sent[command] != 0 {
			t.Fatalf("tainted connection sent command %v", command)
		}
	}
	if got := len(mainConnection.writesSnapshot()); got != 0 {
		t.Fatalf("main connection writes = %d, want 0", got)
	}
	packet := cancelConnection.writesSnapshot()[0]
	if len(packet) != 16 || binary.BigEndian.Uint32(packet[4:8]) != pgCancelRequestCode {
		t.Fatalf("cancel packet = %x", packet)
	}
}

func TestPGAdapterRejectsStaleOwnerBeforePhysicalWrite(t *testing.T) {
	t.Parallel()
	connection := newPGScriptConn(nil)
	adapter := mustPGAdapter(t, connection, nil, nil, PGAdapterOptions{})
	owner := NewTerminalOwner(7, 8)
	attempt := acquireAttempt(t, owner, OperationCommit)
	attempt.AttemptGeneration++
	if _, err := adapter.FinishCommit(context.Background(), owner, attempt); !errors.Is(err, ErrStaleTerminalAttempt) {
		t.Fatalf("error = %v", err)
	}
	if len(connection.writesSnapshot()) != 0 {
		t.Fatal("stale owner touched physical connection")
	}
}

type pgScriptStep struct {
	response   []byte
	writeLimit int
	writeErr   error
}

type pgScriptConn struct {
	mu        sync.Mutex
	responses bytes.Buffer
	steps     []pgScriptStep
	writes    [][]byte
	closed    bool
	deadline  time.Time
}

func newPGScriptConn(steps []pgScriptStep) *pgScriptConn {
	return &pgScriptConn{steps: steps}
}

func (connection *pgScriptConn) Read(target []byte) (int, error) {
	connection.mu.Lock()
	defer connection.mu.Unlock()
	if connection.responses.Len() > 0 {
		return connection.responses.Read(target)
	}
	if connection.closed {
		return 0, net.ErrClosed
	}
	return 0, io.EOF
}

func (connection *pgScriptConn) Write(source []byte) (int, error) {
	connection.mu.Lock()
	defer connection.mu.Unlock()
	if connection.closed {
		return 0, net.ErrClosed
	}
	index := len(connection.writes)
	copyOfSource := append([]byte(nil), source...)
	connection.writes = append(connection.writes, copyOfSource)
	if index >= len(connection.steps) {
		return len(source), nil
	}
	step := connection.steps[index]
	if len(step.response) > 0 {
		_, _ = connection.responses.Write(step.response)
	}
	if step.writeLimit < 0 {
		return 0, step.writeErr
	}
	if step.writeLimit > 0 && step.writeLimit < len(source) {
		return step.writeLimit, step.writeErr
	}
	return len(source), step.writeErr
}

func (connection *pgScriptConn) Close() error {
	connection.mu.Lock()
	connection.closed = true
	connection.mu.Unlock()
	return nil
}

func (connection *pgScriptConn) LocalAddr() net.Addr  { return pgTestAddr("local") }
func (connection *pgScriptConn) RemoteAddr() net.Addr { return pgTestAddr("remote") }
func (connection *pgScriptConn) SetDeadline(value time.Time) error {
	connection.deadline = value
	return nil
}
func (connection *pgScriptConn) SetReadDeadline(time.Time) error  { return nil }
func (connection *pgScriptConn) SetWriteDeadline(time.Time) error { return nil }
func (connection *pgScriptConn) writesSnapshot() [][]byte {
	connection.mu.Lock()
	defer connection.mu.Unlock()
	return append([][]byte(nil), connection.writes...)
}

type pgTestAddr string

func (address pgTestAddr) Network() string { return "tcp" }
func (address pgTestAddr) String() string  { return string(address) }

func mustPGAdapter(t *testing.T, connection net.Conn, transfer func(context.Context, net.Conn, PGBackendIdentity) error, absence func(context.Context, PGBackendIdentity) (bool, error), options PGAdapterOptions) *PGTerminalAdapter {
	t.Helper()
	adapter, err := NewPGTerminalAdapter(PGPhysicalConnection{
		Conn: connection,
		Identity: PGBackendIdentity{
			ServerIdentity: "server-1", Database: "db-1", PID: 42,
			BackendStart: time.Unix(1, 0), ConnectionNonce: "nonce-1",
		},
		CleanCommandBoundary:  true,
		UniqueSocketOwner:     true,
		TransferToPool:        transfer,
		ConfirmBackendAbsence: absence,
	}, nil, options)
	if err != nil {
		t.Fatal(err)
	}
	return adapter
}

func acquireAttempt(t *testing.T, owner *TerminalOwner, operation TerminalOperation) TerminalAttempt {
	t.Helper()
	var attempt TerminalAttempt
	var ok bool
	if operation == OperationCommit {
		attempt, ok = owner.TryCommit(owner.Snapshot().TransactionGeneration, owner.Snapshot().OwnerGeneration)
	} else {
		attempt, ok = owner.TryRollback(owner.Snapshot().TransactionGeneration, owner.Snapshot().OwnerGeneration)
	}
	if !ok {
		t.Fatal("failed to acquire terminal owner")
	}
	return attempt
}

func validNoCommitForAttempt(attempt TerminalAttempt) NoCommitEverSent {
	return NoCommitEverSent{
		Schema: NoCommitEverSentSchema, SchemaVersion: NoCommitEverSentVersion,
		TransactionGeneration: attempt.TransactionGeneration, OwnerGeneration: attempt.OwnerGeneration,
		HistoryContinuous: true, UniqueSocketOwner: true,
	}
}

func backendFrames(frames ...[]byte) []byte {
	return bytes.Join(frames, nil)
}

func backendFrame(messageType byte, body []byte) []byte {
	frame := make([]byte, 5+len(body))
	frame[0] = messageType
	binary.BigEndian.PutUint32(frame[1:5], uint32(len(body)+4))
	copy(frame[5:], body)
	return frame
}

func commandComplete(tag string) []byte {
	return backendFrame('C', append([]byte(tag), 0))
}

func readyForQuery(status byte) []byte {
	return backendFrame('Z', []byte{status})
}

func errorResponse(sqlState string) []byte {
	body := append([]byte{'S'}, []byte("ERROR")...)
	body = append(body, 0, 'C')
	body = append(body, []byte(sqlState)...)
	body = append(body, 0, 0)
	return backendFrame('E', body)
}

func healthResponse() []byte {
	return backendFrames(
		backendFrame('T', []byte{0, 1}),
		backendFrame('D', []byte{0, 1, 0, 0, 0, 1, '1'}),
		commandComplete("SELECT 1"),
		readyForQuery('I'),
	)
}

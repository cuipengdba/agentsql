package b5terminal

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultPGTerminalWatchdog = 150 * time.Millisecond
	defaultPGReconcileBudget  = 25 * time.Millisecond
	defaultPGResetSQL         = "DISCARD ALL"
	defaultPGHealthSQL        = "SELECT 1"
	pgCancelRequestCode       = uint32(80877102)
)

var (
	ErrPGAdapterFinished        = errors.New("b5terminal: PostgreSQL adapter already finished")
	ErrPGInvalidPhysicalLease   = errors.New("b5terminal: invalid PostgreSQL physical connection lease")
	ErrPGUncleanCommandBoundary = errors.New("b5terminal: PostgreSQL connector is not at a clean command boundary")
	ErrPGTerminalWatchdog       = errors.New("b5terminal: PostgreSQL terminal watchdog expired")
	ErrPGProtocol               = errors.New("b5terminal: invalid PostgreSQL terminal protocol")
	ErrPGPoolTransfer           = errors.New("b5terminal: PostgreSQL pool transfer failed")
	ErrPGConnectionDiscarded    = errors.New("b5terminal: PostgreSQL physical connection must not be reused")
)

// PGBackendIdentity is the identity passed to independent backend inventory.
// PID alone is intentionally insufficient because PostgreSQL can reuse it.
type PGBackendIdentity struct {
	ServerIdentity  string
	Database        string
	PID             uint32
	BackendStart    time.Time
	ConnectionNonce string
}

func (identity PGBackendIdentity) exact() bool {
	return identity.ServerIdentity != "" && identity.Database != "" && identity.PID != 0 &&
		!identity.BackendStart.IsZero() && identity.ConnectionNonce != ""
}

// PGPhysicalConnection transfers exclusive ownership of one already
// authenticated physical connection to the adapter. TransferToPool performs
// the pool-generation CAS and assumes ownership only when it returns nil.
// ConfirmBackendAbsence must use an independent connection or inventory.
type PGPhysicalConnection struct {
	Conn                     net.Conn
	Identity                 PGBackendIdentity
	CleanCommandBoundary     bool
	UniqueSocketOwner        bool
	TransferToPool           func(context.Context, net.Conn, PGBackendIdentity) error
	ConfirmBackendAbsence    func(context.Context, PGBackendIdentity) (bool, error)
	OnLocalConnectionDiscard func(PGBackendIdentity, error)
}

type PGAdapterOptions struct {
	WatchdogTimeout time.Duration
	ReconcileBudget time.Duration
	ResetSQL        string
	HealthSQL       string
}

func (options PGAdapterOptions) withDefaults() PGAdapterOptions {
	if options.WatchdogTimeout <= 0 {
		options.WatchdogTimeout = defaultPGTerminalWatchdog
	}
	if options.ReconcileBudget <= 0 {
		options.ReconcileBudget = defaultPGReconcileBudget
	}
	if options.ResetSQL == "" {
		options.ResetSQL = defaultPGResetSQL
	}
	if options.HealthSQL == "" {
		options.HealthSQL = defaultPGHealthSQL
	}
	return options
}

// PGTerminalResult contains the S4a total-function result plus the physical
// lifecycle proof. DestroySignal is ErrPGConnectionDiscarded whenever this
// adapter has made the connection permanently non-reusable. The businessdb
// capability boundary converts it to driver.ErrBadConn for physical disposal;
// neither signal may be used to retry a terminal operation.
type PGTerminalResult struct {
	Evidence       Evidence
	Resolution     TerminalResolution
	ReleaseChecks  ReleaseChecks
	Backend        PGBackendIdentity
	WatchdogFired  bool
	CancelSnapshot CancelSnapshot
	DestroySignal  error
	LifecycleError error
}

// PGTerminalAdapter owns the physical connection from terminal-frame send
// through exactly one pool transfer or local destruction plus reconciliation.
type PGTerminalAdapter struct {
	lease   PGPhysicalConnection
	guard   *CancelGuard
	options PGAdapterOptions
	reader  *bufio.Reader

	finishMu sync.Mutex
	finished bool

	connMu      sync.Mutex
	closed      bool
	transferred bool

	destroyOnce      sync.Once
	destroyMu        sync.Mutex
	destroyConfirmed bool
	destroyErr       error

	watchdogFired atomic.Bool
}

func NewPGTerminalAdapter(lease PGPhysicalConnection, guard *CancelGuard, options PGAdapterOptions) (*PGTerminalAdapter, error) {
	if lease.Conn == nil || !lease.Identity.exact() || !lease.UniqueSocketOwner {
		return nil, ErrPGInvalidPhysicalLease
	}
	if !lease.CleanCommandBoundary {
		return nil, ErrPGUncleanCommandBoundary
	}
	if guard == nil {
		guard = NewCancelGuard()
	}
	return &PGTerminalAdapter{lease: lease, guard: guard, options: options.withDefaults(), reader: bufio.NewReader(lease.Conn)}, nil
}

func (adapter *PGTerminalAdapter) FinishCommit(ctx context.Context, owner *TerminalOwner, attempt TerminalAttempt) (PGTerminalResult, error) {
	if attempt.Operation != OperationCommit {
		return PGTerminalResult{}, ErrInvalidOwnerState
	}
	return adapter.finish(ctx, owner, attempt, NoCommitEverSent{})
}

func (adapter *PGTerminalAdapter) FinishRollback(ctx context.Context, owner *TerminalOwner, attempt TerminalAttempt, proof NoCommitEverSent) (PGTerminalResult, error) {
	if attempt.Operation != OperationRollback {
		return PGTerminalResult{}, ErrInvalidOwnerState
	}
	return adapter.finish(ctx, owner, attempt, proof)
}

func (adapter *PGTerminalAdapter) finish(ctx context.Context, owner *TerminalOwner, attempt TerminalAttempt, proof NoCommitEverSent) (PGTerminalResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	adapter.finishMu.Lock()
	defer adapter.finishMu.Unlock()
	if adapter.finished {
		return PGTerminalResult{}, ErrPGAdapterFinished
	}
	if err := owner.ValidateAttempt(attempt); err != nil {
		return PGTerminalResult{}, err
	}
	adapter.finished = true

	operation := attempt.Operation
	command := CommandRollback
	query := "ROLLBACK"
	if operation == OperationCommit {
		command = CommandCommit
		query = "COMMIT"
		if err := owner.OpenCommitSendPermit(attempt); err != nil {
			return PGTerminalResult{}, err
		}
	}

	evidence := Evidence{
		Schema:                TerminalEvidenceSchema,
		SchemaVersion:         TerminalEvidenceVersion,
		Operation:             operation,
		TransactionGeneration: attempt.TransactionGeneration,
		AttemptGeneration:     attempt.AttemptGeneration,
		OwnerGeneration:       attempt.OwnerGeneration,
		Write: WriteEvidence{
			Phase:      WriteNotSent,
			FrameBytes: uint64(len(encodeSimpleQuery(query))),
		},
	}

	watchdogDone := make(chan struct{})
	go adapter.runWatchdog(watchdogDone)
	defer close(watchdogDone)
	adapter.applyContextDeadline(ctx)

	var lifecycleErr error
	if err := ctx.Err(); err == nil {
		if err = adapter.guard.RecordMainSend(command); err == nil {
			evidence = adapter.exchangeTerminal(ctx, attempt, query, evidence)
		} else {
			lifecycleErr = err
		}
	} else {
		lifecycleErr = err
	}

	if operation == OperationCommit {
		adapter.recordCommitProgress(owner, attempt, evidence)
	}

	checks := ReleaseChecks{}
	cancel := adapter.guard.Snapshot()
	hypothetical := ResolveTerminal(evidence, ResolutionContext{
		NoCommitProof:  proof,
		CancelEmission: cancel.Emission,
		ReleaseChecks:  allPGReleaseChecks(),
	})
	if hypothetical.Disposition == DispositionReleased && !cancel.Tainted && !adapter.watchdogFired.Load() {
		checks, lifecycleErr = adapter.releaseLifecycle(ctx)
	}

	backendConfirmed := false
	if !checks.AllPassed() {
		backendConfirmed, lifecycleErr = adapter.destroyAndReconcile(lifecycleErr)
	}
	resolution := ResolveTerminal(evidence, ResolutionContext{
		NoCommitProof:           proof,
		CancelEmission:          adapter.guard.Snapshot().Emission,
		ReleaseChecks:           checks,
		BackendAbsenceConfirmed: backendConfirmed,
	})
	if err := owner.Complete(attempt, resolution); err != nil {
		return PGTerminalResult{}, err
	}

	result := PGTerminalResult{
		Evidence:       evidence,
		Resolution:     resolution,
		ReleaseChecks:  checks,
		Backend:        adapter.lease.Identity,
		WatchdogFired:  adapter.watchdogFired.Load(),
		CancelSnapshot: adapter.guard.Snapshot(),
		LifecycleError: lifecycleErr,
	}
	if resolution.Disposition != DispositionReleased {
		result.DestroySignal = ErrPGConnectionDiscarded
	}
	return result, nil
}

func (adapter *PGTerminalAdapter) exchangeTerminal(_ context.Context, attempt TerminalAttempt, query string, evidence Evidence) Evidence {
	frame := encodeSimpleQuery(query)
	written, started, indeterminate, err := adapter.writeFrame(frame)
	evidence.Write.BytesWritten = uint64(written)
	switch {
	case indeterminate:
		evidence.Write.Phase = WriteIndeterminate
	case !started:
		evidence.Write.Phase = WriteNotSent
	case written == 0:
		evidence.Write.Phase = WriteZeroBytes
	case written < len(frame):
		evidence.Write.Phase = WritePartial
	default:
		evidence.Write.Phase = WriteFullFrame
	}
	if err != nil || written != len(frame) {
		adapter.observeTerminalIOError(err)
		return evidence
	}

	hash := sha256.New()
	for {
		messageType, body, receiveErr := adapter.readBackendFrame()
		if receiveErr != nil {
			adapter.observeTerminalIOError(receiveErr)
			break
		}
		_, _ = hash.Write([]byte{messageType})
		var length [4]byte
		binary.BigEndian.PutUint32(length[:], uint32(len(body)+4))
		_, _ = hash.Write(length[:])
		_, _ = hash.Write(body)

		switch messageType {
		case 'C':
			tag := strings.TrimSuffix(string(body), "\x00")
			kind := ReplyUntyped
			if attempt.Operation == OperationCommit && tag == "COMMIT" {
				kind = ReplyCommitPositiveACK
			}
			if attempt.Operation == OperationRollback && tag == "ROLLBACK" {
				kind = ReplyRollbackPositiveACK
			}
			evidence.Replies = append(evidence.Replies, adapter.reply(attempt, kind))
		case 'E':
			kind := ReplyRollbackDefinitiveRejection
			if attempt.Operation == OperationCommit {
				kind = ReplyCommitDeferredErrorRejection
			}
			evidence.Replies = append(evidence.Replies, adapter.reply(attempt, kind))
		case 'Z':
			state := ServerStatusUnknownOrContradictory
			if len(body) == 1 {
				switch body[0] {
				case 'I':
					state = ServerReadyIdle
				case 'T':
					state = ServerReadyIdleInTransaction
				case 'E':
					state = ServerReadyFailed
				}
			}
			evidence.ServerStatuses = append(evidence.ServerStatuses, ServerTxStatus{
				State:                 state,
				TransactionGeneration: attempt.TransactionGeneration,
				AttemptGeneration:     attempt.AttemptGeneration,
			})
			adapter.drainBufferedTerminal(attempt, &evidence, hash)
			adapter.attachCorrelation(evidence.Replies, hash.Sum(nil))
			return evidence
		case 'N', 'A', 'S':
			// Asynchronous protocol messages do not prove a terminal result.
		default:
			// Row/data/copy/auth messages cannot be a terminal response at this
			// clean exclusive command boundary.
			evidence.Replies = append(evidence.Replies, adapter.reply(attempt, ReplyUntyped))
		}
		if adapter.guard.Snapshot().Tainted {
			break
		}
	}
	adapter.attachCorrelation(evidence.Replies, hash.Sum(nil))
	return evidence
}

func (adapter *PGTerminalAdapter) observeTerminalIOError(err error) {
	var networkError net.Error
	if errors.As(err, &networkError) && networkError.Timeout() {
		adapter.watchdogFired.Store(true)
		adapter.seizeAndClose(ErrPGTerminalWatchdog)
	}
}

func (adapter *PGTerminalAdapter) drainBufferedTerminal(attempt TerminalAttempt, evidence *Evidence, hash io.Writer) {
	for adapter.reader.Buffered() >= 5 {
		messageType, body, err := adapter.readBackendFrame()
		if err != nil {
			return
		}
		_, _ = hash.Write([]byte{messageType})
		var length [4]byte
		binary.BigEndian.PutUint32(length[:], uint32(len(body)+4))
		_, _ = hash.Write(length[:])
		_, _ = hash.Write(body)
		switch messageType {
		case 'Z':
			state := ServerStatusUnknownOrContradictory
			if len(body) == 1 {
				switch body[0] {
				case 'I':
					state = ServerReadyIdle
				case 'T':
					state = ServerReadyIdleInTransaction
				case 'E':
					state = ServerReadyFailed
				}
			}
			evidence.ServerStatuses = append(evidence.ServerStatuses, ServerTxStatus{
				State: state, TransactionGeneration: attempt.TransactionGeneration, AttemptGeneration: attempt.AttemptGeneration,
			})
		case 'C':
			tag := strings.TrimSuffix(string(body), "\x00")
			kind := ReplyUntyped
			if attempt.Operation == OperationCommit && tag == "COMMIT" {
				kind = ReplyCommitPositiveACK
			}
			if attempt.Operation == OperationRollback && tag == "ROLLBACK" {
				kind = ReplyRollbackPositiveACK
			}
			evidence.Replies = append(evidence.Replies, adapter.reply(attempt, kind))
		case 'E':
			kind := ReplyRollbackDefinitiveRejection
			if attempt.Operation == OperationCommit {
				kind = ReplyCommitDeferredErrorRejection
			}
			evidence.Replies = append(evidence.Replies, adapter.reply(attempt, kind))
		case 'N', 'A', 'S':
		default:
			evidence.Replies = append(evidence.Replies, adapter.reply(attempt, ReplyUntyped))
		}
	}
}

func (adapter *PGTerminalAdapter) reply(attempt TerminalAttempt, kind TerminalReplyKind) TerminalReply {
	return TerminalReply{
		Kind:                  kind,
		Operation:             attempt.Operation,
		TransactionGeneration: attempt.TransactionGeneration,
		AttemptGeneration:     attempt.AttemptGeneration,
		Correlation: CorrelationProof{
			Strength:                CorrelationStrongCurrentOperation,
			CleanCommandBoundary:    adapter.lease.CleanCommandBoundary,
			UniqueSocketOwner:       adapter.lease.UniqueSocketOwner,
			DecoderAfterSendPermit:  true,
			NoUnreadOrPendingAtSend: true,
			MonotonicTranscript:     true,
		},
	}
}

func (adapter *PGTerminalAdapter) attachCorrelation(replies []TerminalReply, digest []byte) {
	var value [32]byte
	copy(value[:], digest)
	for index := range replies {
		replies[index].Correlation.TranscriptDigest = value
	}
}

func (adapter *PGTerminalAdapter) writeFrame(frame []byte) (written int, started, indeterminate bool, resultErr error) {
	for written < len(frame) {
		started = true
		n, err := adapter.lease.Conn.Write(frame[written:])
		if n < 0 || n > len(frame)-written {
			return written, started, true, ErrPGProtocol
		}
		written += n
		if err != nil {
			return written, started, false, err
		}
		if n == 0 {
			return written, started, false, io.ErrNoProgress
		}
	}
	return written, started, false, nil
}

func (adapter *PGTerminalAdapter) readBackendFrame() (byte, []byte, error) {
	var header [5]byte
	if _, err := io.ReadFull(adapter.reader, header[:]); err != nil {
		return 0, nil, err
	}
	length := binary.BigEndian.Uint32(header[1:])
	if length < 4 || length > 16<<20 {
		return 0, nil, ErrPGProtocol
	}
	body := make([]byte, int(length)-4)
	if _, err := io.ReadFull(adapter.reader, body); err != nil {
		return 0, nil, err
	}
	return header[0], body, nil
}

func (adapter *PGTerminalAdapter) recordCommitProgress(owner *TerminalOwner, attempt TerminalAttempt, evidence Evidence) {
	switch evidence.Write.Phase {
	case WritePartial:
		_ = owner.AdvanceCommitStage(attempt, CommitStagePartialWrite)
	case WriteFullFrame:
		stage := CommitStageFullWriteAwaitingACK
		if len(evidence.Replies) > 0 && (evidence.Replies[0].Kind == ReplyCommitPositiveACK || evidence.Replies[0].Kind == ReplyCommitDeferredErrorRejection) {
			stage = CommitStageACKObservedAwaitingRFQ
		}
		_ = owner.AdvanceCommitStage(attempt, stage)
	}
}

func (adapter *PGTerminalAdapter) releaseLifecycle(ctx context.Context) (ReleaseChecks, error) {
	checks := ReleaseChecks{DrainComplete: true}
	if err := adapter.runLifecycleQuery(CommandReset, adapter.options.ResetSQL); err != nil {
		return checks, err
	}
	checks.SessionReset = true
	if err := adapter.runLifecycleQuery(CommandHealthProbe, adapter.options.HealthSQL); err != nil {
		return checks, err
	}
	checks.HealthCheck = true
	checks.StatusRecheckIdle = true
	if adapter.guard.Snapshot().Tainted {
		return checks, ErrCancelTainted
	}
	if adapter.reader.Buffered() != 0 {
		return checks, ErrPGUncleanCommandBoundary
	}
	if adapter.lease.TransferToPool == nil {
		return checks, ErrPGPoolTransfer
	}
	adapter.clearDeadline()
	if err := adapter.lease.TransferToPool(ctx, adapter.lease.Conn, adapter.lease.Identity); err != nil {
		return checks, errors.Join(ErrPGPoolTransfer, err)
	}
	adapter.connMu.Lock()
	if adapter.closed || adapter.watchdogFired.Load() || adapter.guard.Snapshot().Tainted {
		adapter.connMu.Unlock()
		return checks, errors.Join(ErrPGPoolTransfer, ErrPGConnectionDiscarded)
	}
	adapter.transferred = true
	adapter.connMu.Unlock()
	checks.PoolTransferCAS = true
	return checks, nil
}

func (adapter *PGTerminalAdapter) runLifecycleQuery(command MainCommand, query string) error {
	if err := adapter.guard.RecordMainSend(command); err != nil {
		return err
	}
	frame := encodeSimpleQuery(query)
	written, _, indeterminate, err := adapter.writeFrame(frame)
	if err != nil || indeterminate || written != len(frame) {
		if err == nil {
			err = io.ErrShortWrite
		}
		return err
	}
	commandComplete := false
	for {
		messageType, body, err := adapter.readBackendFrame()
		if err != nil {
			return err
		}
		switch messageType {
		case 'C':
			commandComplete = true
		case 'E':
			return ErrPGProtocol
		case 'Z':
			if !commandComplete || len(body) != 1 || body[0] != 'I' {
				return ErrPGProtocol
			}
			return nil
		case 'N', 'A', 'S', 'T', 'D':
			// SELECT health emits row description and data row.
		default:
			return ErrPGProtocol
		}
	}
}

func (adapter *PGTerminalAdapter) runWatchdog(done <-chan struct{}) {
	timer := time.NewTimer(adapter.options.WatchdogTimeout)
	defer timer.Stop()
	select {
	case <-done:
		return
	case <-timer.C:
		adapter.fireWatchdog()
	}
}

func (adapter *PGTerminalAdapter) fireWatchdog() {
	adapter.connMu.Lock()
	if adapter.closed || adapter.transferred {
		adapter.connMu.Unlock()
		return
	}
	adapter.watchdogFired.Store(true)
	_ = adapter.lease.Conn.SetDeadline(time.Now())
	_ = adapter.lease.Conn.Close()
	adapter.closed = true
	callback := adapter.lease.OnLocalConnectionDiscard
	identity := adapter.lease.Identity
	adapter.connMu.Unlock()
	if callback != nil {
		callback(identity, ErrPGTerminalWatchdog)
	}
}

func (adapter *PGTerminalAdapter) applyContextDeadline(ctx context.Context) {
	deadline := time.Now().Add(adapter.options.WatchdogTimeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	adapter.connMu.Lock()
	if !adapter.closed && !adapter.transferred {
		_ = adapter.lease.Conn.SetDeadline(deadline)
	}
	adapter.connMu.Unlock()
}

func (adapter *PGTerminalAdapter) clearDeadline() {
	adapter.connMu.Lock()
	if !adapter.closed && !adapter.transferred {
		_ = adapter.lease.Conn.SetDeadline(time.Time{})
	}
	adapter.connMu.Unlock()
}

func (adapter *PGTerminalAdapter) seizeAndClose(reason error) {
	adapter.connMu.Lock()
	callback := adapter.lease.OnLocalConnectionDiscard
	identity := adapter.lease.Identity
	discarded := false
	if !adapter.closed && !adapter.transferred {
		_ = adapter.lease.Conn.SetDeadline(time.Now())
		_ = adapter.lease.Conn.Close()
		adapter.closed = true
		discarded = true
	}
	adapter.connMu.Unlock()
	if discarded && callback != nil {
		callback(identity, reason)
	}
}

func (adapter *PGTerminalAdapter) destroyAndReconcile(cause error) (bool, error) {
	adapter.destroyOnce.Do(func() {
		adapter.seizeAndClose(ErrPGConnectionDiscarded)
		var confirmed bool
		var err error
		if adapter.lease.ConfirmBackendAbsence != nil {
			ctx, cancel := context.WithTimeout(context.Background(), adapter.options.ReconcileBudget)
			confirmed, err = adapter.lease.ConfirmBackendAbsence(ctx, adapter.lease.Identity)
			cancel()
		}
		adapter.destroyMu.Lock()
		adapter.destroyConfirmed = confirmed
		adapter.destroyErr = err
		adapter.destroyMu.Unlock()
	})
	adapter.destroyMu.Lock()
	confirmed, destroyErr := adapter.destroyConfirmed, adapter.destroyErr
	adapter.destroyMu.Unlock()
	return confirmed, errors.Join(cause, destroyErr)
}

// ObserveCancelEmission is the only supported way for the owner to publish an
// independently sent CancelRequest. Taint is permanent and immediately seizes
// the main connection, structurally preventing rollback/reset/health.
func (adapter *PGTerminalAdapter) ObserveCancelEmission(emission CancelEmission) {
	adapter.guard.ObserveEmission(emission)
	if emission != CancelNotSentProven {
		adapter.seizeAndClose(ErrCancelTainted)
	}
}

// SendCancelRequest emits the standard 16-byte PostgreSQL CancelRequest on an
// independent connection. A dial failure proves no packet was sent; once the
// write syscall starts the main connection is permanently tainted.
func (adapter *PGTerminalAdapter) SendCancelRequest(ctx context.Context, dial func(context.Context) (net.Conn, error), pid, secret uint32) (CancelEmission, error) {
	if dial == nil {
		return CancelNotSentProven, fmt.Errorf("dial cancel connection: %w", ErrPGInvalidPhysicalLease)
	}
	cancelConn, err := dial(ctx)
	if err != nil {
		return CancelNotSentProven, err
	}
	defer cancelConn.Close()
	packet := make([]byte, 16)
	binary.BigEndian.PutUint32(packet[0:4], uint32(len(packet)))
	binary.BigEndian.PutUint32(packet[4:8], pgCancelRequestCode)
	binary.BigEndian.PutUint32(packet[8:12], pid)
	binary.BigEndian.PutUint32(packet[12:16], secret)
	if deadline, ok := ctx.Deadline(); ok {
		_ = cancelConn.SetDeadline(deadline)
	}
	adapter.ObserveCancelEmission(CancelSendStartedOrIndeterminate)
	n, err := cancelConn.Write(packet)
	if n == len(packet) {
		adapter.guard.ObserveEmission(CancelFullPacketWritten)
		return CancelFullPacketWritten, err
	}
	if err == nil {
		err = io.ErrShortWrite
	}
	return CancelSendStartedOrIndeterminate, err
}

func encodeSimpleQuery(query string) []byte {
	bodyLength := len(query) + 1
	frame := make([]byte, 1+4+bodyLength)
	frame[0] = 'Q'
	binary.BigEndian.PutUint32(frame[1:5], uint32(4+bodyLength))
	copy(frame[5:], query)
	return frame
}

func allPGReleaseChecks() ReleaseChecks {
	return ReleaseChecks{DrainComplete: true, SessionReset: true, HealthCheck: true, StatusRecheckIdle: true, PoolTransferCAS: true}
}

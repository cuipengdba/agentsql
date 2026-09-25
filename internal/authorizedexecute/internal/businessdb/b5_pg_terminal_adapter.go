package businessdb

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"net"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/cuipengdba/agentsql/internal/b5terminal"
)

var ErrB5PGXTransactionNotActive = errors.New("businessdb: B5 pgx physical connection is not in a transaction")

// B5PGXPhysicalHooks bridges the flag-off typed adapter to a B5-owned physical
// pgconn pool. ReturnToPool must perform its pool-transfer generation CAS and
// assumes ownership of pgConn only when it returns nil.
type B5PGXPhysicalHooks struct {
	ReturnToPool             func(context.Context, *pgconn.PgConn, b5terminal.PGBackendIdentity) error
	ConfirmBackendAbsence    func(context.Context, b5terminal.PGBackendIdentity) (bool, error)
	OnLocalConnectionDiscard func(b5terminal.PGBackendIdentity, error)
	// WrapWire exists only for the feature-off fault matrix. It is applied
	// above pgx's TLS connection, so counts remain PostgreSQL frame bytes.
	WrapWire func(net.Conn) net.Conn
}

type B5PGTerminalResult struct {
	b5terminal.PGTerminalResult
	// BadConnection is driver.ErrBadConn only for physical disposal. Callers
	// must not return it through an operation API that could retry COMMIT.
	BadConnection error
}

// B5PGTypedTerminalAdapter is intentionally separate from WriteTx. It never
// wraps pgx.Tx.Commit/Rollback errors and has no production handler or flag
// registration in v0.4 S4b.
type B5PGTypedTerminalAdapter struct {
	terminal *b5terminal.PGTerminalAdapter
}

func NewB5PGTypedTerminalAdapter(pgConn *pgconn.PgConn, identity b5terminal.PGBackendIdentity, guard *b5terminal.CancelGuard, hooks B5PGXPhysicalHooks, options b5terminal.PGAdapterOptions) (*B5PGTypedTerminalAdapter, error) {
	if pgConn == nil || pgConn.IsClosed() || pgConn.IsBusy() {
		return nil, b5terminal.ErrPGInvalidPhysicalLease
	}
	if status := pgConn.TxStatus(); status != 'T' && status != 'E' {
		return nil, ErrB5PGXTransactionNotActive
	}
	if identity.PID == 0 {
		identity.PID = pgConn.PID()
	}
	if identity.PID != pgConn.PID() {
		return nil, fmt.Errorf("%w: backend PID mismatch", b5terminal.ErrPGInvalidPhysicalLease)
	}
	hijacked, err := pgConn.Hijack()
	if err != nil {
		return nil, err
	}
	if hijacked.Frontend == nil || hijacked.Frontend.ReadBufferLen() != 0 {
		_ = hijacked.Conn.Close()
		if hooks.OnLocalConnectionDiscard != nil {
			hooks.OnLocalConnectionDiscard(identity, b5terminal.ErrPGUncleanCommandBoundary)
		}
		return nil, b5terminal.ErrPGUncleanCommandBoundary
	}
	if hooks.WrapWire != nil {
		wrapped := hooks.WrapWire(hijacked.Conn)
		if wrapped == nil {
			_ = hijacked.Conn.Close()
			return nil, b5terminal.ErrPGInvalidPhysicalLease
		}
		hijacked.Conn = wrapped
	}

	lease := b5terminal.PGPhysicalConnection{
		Conn:                     hijacked.Conn,
		Identity:                 identity,
		CleanCommandBoundary:     true,
		UniqueSocketOwner:        true,
		ConfirmBackendAbsence:    hooks.ConfirmBackendAbsence,
		OnLocalConnectionDiscard: hooks.OnLocalConnectionDiscard,
	}
	if hooks.ReturnToPool != nil {
		lease.TransferToPool = func(ctx context.Context, connection net.Conn, backend b5terminal.PGBackendIdentity) error {
			hijacked.Conn = connection
			hijacked.TxStatus = 'I'
			reconstructed, constructErr := pgconn.Construct(hijacked)
			if constructErr != nil {
				return errors.Join(driver.ErrBadConn, constructErr)
			}
			if returnErr := hooks.ReturnToPool(ctx, reconstructed, backend); returnErr != nil {
				_ = reconstructed.Close(context.Background())
				return returnErr
			}
			return nil
		}
	}
	terminal, err := b5terminal.NewPGTerminalAdapter(lease, guard, options)
	if err != nil {
		_ = hijacked.Conn.Close()
		return nil, err
	}
	return &B5PGTypedTerminalAdapter{terminal: terminal}, nil
}

func (adapter *B5PGTypedTerminalAdapter) FinishCommit(ctx context.Context, owner *b5terminal.TerminalOwner, attempt b5terminal.TerminalAttempt) (B5PGTerminalResult, error) {
	result, err := adapter.terminal.FinishCommit(ctx, owner, attempt)
	return b5PGTerminalResult(result), err
}

func (adapter *B5PGTypedTerminalAdapter) FinishRollback(ctx context.Context, owner *b5terminal.TerminalOwner, attempt b5terminal.TerminalAttempt, proof b5terminal.NoCommitEverSent) (B5PGTerminalResult, error) {
	result, err := adapter.terminal.FinishRollback(ctx, owner, attempt, proof)
	return b5PGTerminalResult(result), err
}

func (adapter *B5PGTypedTerminalAdapter) ObserveCancelEmission(emission b5terminal.CancelEmission) {
	adapter.terminal.ObserveCancelEmission(emission)
}

func (adapter *B5PGTypedTerminalAdapter) SendCancelRequest(ctx context.Context, dial func(context.Context) (net.Conn, error), pid, secret uint32) (b5terminal.CancelEmission, error) {
	return adapter.terminal.SendCancelRequest(ctx, dial, pid, secret)
}

func b5PGTerminalResult(result b5terminal.PGTerminalResult) B5PGTerminalResult {
	typed := B5PGTerminalResult{PGTerminalResult: result}
	if result.Resolution.Disposition != b5terminal.DispositionReleased {
		typed.BadConnection = driver.ErrBadConn
	}
	return typed
}

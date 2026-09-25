package b5terminal

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const pgTerminalContainerGate = "AGENTSQL_B5_PG_TERMINAL_MATRIX"

type PGXPhysicalHooks struct {
	ReturnToPool             func(context.Context, *pgconn.PgConn, PGBackendIdentity) error
	ConfirmBackendAbsence    func(context.Context, PGBackendIdentity) (bool, error)
	OnLocalConnectionDiscard func(PGBackendIdentity, error)
	WrapWire                 func(net.Conn) net.Conn
}

// NewPGXTerminalAdapter is test-only here. The production pgx capability
// bridge lives in authorizedexecute/internal/businessdb.
func NewPGXTerminalAdapter(pgConn *pgconn.PgConn, identity PGBackendIdentity, guard *CancelGuard, hooks PGXPhysicalHooks, options PGAdapterOptions) (*PGTerminalAdapter, error) {
	if pgConn == nil || pgConn.IsClosed() || pgConn.IsBusy() {
		return nil, ErrPGInvalidPhysicalLease
	}
	if status := pgConn.TxStatus(); status != 'T' && status != 'E' {
		return nil, errors.New("pgx connection is not in a transaction")
	}
	if identity.PID == 0 {
		identity.PID = pgConn.PID()
	}
	if identity.PID != pgConn.PID() {
		return nil, ErrPGInvalidPhysicalLease
	}
	hijacked, err := pgConn.Hijack()
	if err != nil {
		return nil, err
	}
	if hijacked.Frontend == nil || hijacked.Frontend.ReadBufferLen() != 0 {
		_ = hijacked.Conn.Close()
		return nil, ErrPGUncleanCommandBoundary
	}
	if hooks.WrapWire != nil {
		wrapped := hooks.WrapWire(hijacked.Conn)
		if wrapped == nil {
			_ = hijacked.Conn.Close()
			return nil, ErrPGInvalidPhysicalLease
		}
		hijacked.Conn = wrapped
	}
	lease := PGPhysicalConnection{
		Conn: hijacked.Conn, Identity: identity, CleanCommandBoundary: true, UniqueSocketOwner: true,
		ConfirmBackendAbsence: hooks.ConfirmBackendAbsence, OnLocalConnectionDiscard: hooks.OnLocalConnectionDiscard,
	}
	if hooks.ReturnToPool != nil {
		lease.TransferToPool = func(ctx context.Context, connection net.Conn, backend PGBackendIdentity) error {
			hijacked.Conn = connection
			hijacked.TxStatus = 'I'
			reconstructed, constructErr := pgconn.Construct(hijacked)
			if constructErr != nil {
				return constructErr
			}
			return hooks.ReturnToPool(ctx, reconstructed, backend)
		}
	}
	return NewPGTerminalAdapter(lease, guard, options)
}

// TestPGTerminalContainerMatrix is the release-blocking real PostgreSQL
// matrix. It is opt-in because it starts four database containers. The test
// covers PG14/18 x TLS on/off x direct/proxy and executes positive commit,
// deferred-error rejection, and rollback on the typed physical adapter.
func TestPGTerminalContainerMatrix(t *testing.T) {
	if os.Getenv(pgTerminalContainerGate) != "1" {
		t.Skip(pgTerminalContainerGate + "=1 required for the real PG14/18 terminal matrix")
	}
	if testing.Short() {
		t.Skip("real PostgreSQL terminal matrix")
	}
	testcontainers.SkipIfProviderIsNotHealthy(t)

	for _, major := range []string{"14", "18"} {
		for _, tlsEnabled := range []bool{false, true} {
			major, tlsEnabled := major, tlsEnabled
			t.Run(fmt.Sprintf("pg%s/tls=%t", major, tlsEnabled), func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
				defer cancel()
				directDSN, container := startPGTerminalContainer(t, ctx, major, tlsEnabled)
				for _, proxied := range []bool{false, true} {
					proxied := proxied
					t.Run(fmt.Sprintf("proxy=%t", proxied), func(t *testing.T) {
						dsn := directDSN
						if proxied {
							dsn = startPGTCPProxy(t, directDSN)
						}
						runPGRealPositiveCommit(t, ctx, dsn, major, tlsEnabled, proxied)
						runPGRealDeferredRejection(t, ctx, dsn, major, tlsEnabled, proxied)
						runPGRealRollback(t, ctx, dsn, major, tlsEnabled, proxied)
						runPGRealFaultMatrix(t, ctx, dsn, major, tlsEnabled, proxied)
						runPGRealDelayedCancel(t, ctx, dsn, major, tlsEnabled, proxied)
						runPGRealStatementTimeoutCancel(t, ctx, dsn, major, tlsEnabled, proxied)
					})
				}
				runPGRealRestartAndPIDReuseDefense(t, ctx, directDSN, container, major, tlsEnabled)
			})
		}
	}
}

func runPGRealRestartAndPIDReuseDefense(t *testing.T, ctx context.Context, dsn string, container testcontainers.Container, major string, tlsEnabled bool) {
	t.Helper()
	t.Run("restart-failover-replacement-and-pid-reuse-defense", func(t *testing.T) {
		table := fmt.Sprintf("b5_terminal_restart_%s_%t", major, tlsEnabled)
		setup := pgMatrixObserver(t, ctx, dsn)
		if _, err := setup.Exec(ctx, "CREATE TABLE IF NOT EXISTS "+table+" (id bigint primary key)"); err != nil {
			t.Fatal(err)
		}
		if _, err := setup.Exec(ctx, "TRUNCATE "+table); err != nil {
			t.Fatal(err)
		}
		setup.Close(ctx)

		connection := pgMatrixObserver(t, ctx, dsn)
		if _, err := connection.Exec(ctx, "BEGIN"); err != nil {
			t.Fatal(err)
		}
		if _, err := connection.Exec(ctx, "INSERT INTO "+table+" VALUES (1)"); err != nil {
			t.Fatal(err)
		}
		before := pgMatrixIdentity(t, ctx, connection, "before-restart", major, tlsEnabled, false)
		adapter, err := NewPGXTerminalAdapter(connection.PgConn(), before, nil, PGXPhysicalHooks{}, PGAdapterOptions{WatchdogTimeout: 500 * time.Millisecond})
		if err != nil {
			t.Fatal(err)
		}
		stopTimeout := time.Second
		if err = container.Stop(ctx, &stopTimeout); err != nil {
			t.Fatal(err)
		}
		owner := NewTerminalOwner(1001, 1002)
		attempt := acquireAttempt(t, owner, OperationCommit)
		result, err := adapter.FinishCommit(ctx, owner, attempt)
		if err != nil {
			t.Fatal(err)
		}
		if result.Resolution.Outcome == OutcomeCommitted || result.Resolution.Disposition == DispositionReleased {
			t.Fatalf("restart result = %+v", result)
		}
		// A fresh postmaster models the replacement endpoint selected after a
		// restart/failover. Docker Desktop does not reliably re-publish a
		// stopped container's port, so the matrix deliberately avoids making
		// its correctness depend on that host-specific behavior.
		replacementDSN, _ := startPGTerminalContainer(t, ctx, major, tlsEnabled)
		afterConnection := pgMatrixObserver(t, ctx, replacementDSN)
		defer afterConnection.Close(ctx)
		if _, err = afterConnection.Exec(ctx, "CREATE TABLE "+table+" (id bigint primary key)"); err != nil {
			t.Fatal(err)
		}
		var count int
		if err = afterConnection.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("replacement truth count=%d err=%v", count, err)
		}
		after := pgMatrixIdentity(t, ctx, afterConnection, "after-restart", major, tlsEnabled, false)
		if after.BackendStart.Equal(before.BackendStart) {
			t.Fatalf("postmaster identity did not change across restart: before=%+v after=%+v", before, after)
		}
		// Force the observed PID field to the old value: backend start plus the
		// connection nonce still prevent a PID-reuse false match.
		after.PID = before.PID
		if after == before {
			t.Fatalf("exact backend identity collapsed under forced PID reuse: %+v", after)
		}
	})
}

func runPGRealStatementTimeoutCancel(t *testing.T, ctx context.Context, dsn, major string, tlsEnabled, proxied bool) {
	t.Helper()
	t.Run("statement-timeout-races-cancel", func(t *testing.T) {
		connection := pgMatrixObserver(t, ctx, dsn)
		if _, err := connection.Exec(ctx, "BEGIN"); err != nil {
			t.Fatal(err)
		}
		if _, err := connection.Exec(ctx, "SET LOCAL statement_timeout='35ms'"); err != nil {
			t.Fatal(err)
		}
		pid := connection.PgConn().PID()
		secretBytes := connection.PgConn().SecretKey()
		if len(secretBytes) != 4 {
			t.Fatalf("cancel secret length = %d", len(secretBytes))
		}
		secret := binary.BigEndian.Uint32(secretBytes)
		identity := pgMatrixIdentity(t, ctx, connection, "timeout-cancel", major, tlsEnabled, proxied)
		guard := NewCancelGuard()
		executeDone := make(chan error, 1)
		go func() {
			_, executeErr := connection.Exec(ctx, "SELECT pg_sleep(5)")
			executeDone <- executeErr
		}()
		time.Sleep(30 * time.Millisecond)
		config, err := pgx.ParseConfig(dsn)
		if err != nil {
			t.Fatal(err)
		}
		cancelConnection, err := (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(config.Host, fmt.Sprintf("%d", config.Port)))
		if err != nil {
			t.Fatal(err)
		}
		packet := make([]byte, 16)
		binary.BigEndian.PutUint32(packet[0:4], 16)
		binary.BigEndian.PutUint32(packet[4:8], pgCancelRequestCode)
		binary.BigEndian.PutUint32(packet[8:12], pid)
		binary.BigEndian.PutUint32(packet[12:16], secret)
		guard.ObserveEmission(CancelSendStartedOrIndeterminate)
		n, writeErr := cancelConnection.Write(packet)
		_ = cancelConnection.Close()
		if n == len(packet) {
			guard.ObserveEmission(CancelFullPacketWritten)
		}
		if writeErr != nil || n != len(packet) {
			t.Fatalf("cancel write=%d err=%v", n, writeErr)
		}
		select {
		case executeErr := <-executeDone:
			if executeErr == nil {
				t.Fatal("pg_sleep unexpectedly completed")
			}
			guard.ObserveStatementTimeout()
			guard.ObserveExecuteExit()
		case <-time.After(time.Second):
			t.Fatal("statement timeout/cancel did not quiesce")
		}

		adapter, err := NewPGXTerminalAdapter(connection.PgConn(), identity, guard, PGXPhysicalHooks{}, PGAdapterOptions{WatchdogTimeout: time.Second})
		if err != nil {
			t.Fatal(err)
		}
		owner := NewTerminalOwner(901, 902)
		attempt := acquireAttempt(t, owner, OperationRollback)
		result, err := adapter.FinishRollback(ctx, owner, attempt, validNoCommitForAttempt(attempt))
		if err != nil {
			t.Fatal(err)
		}
		if result.Resolution.Outcome != OutcomeNotCommitted || result.Resolution.Disposition != DispositionDiscardUnconfirmed || !result.CancelSnapshot.Tainted || !result.CancelSnapshot.Diagnostics.StatementTimeoutObserved {
			t.Fatalf("timeout/cancel result = %+v", result)
		}
		for _, command := range []MainCommand{CommandRollback, CommandCommit, CommandReset, CommandHealthProbe} {
			if result.CancelSnapshot.Sent[command] != 0 {
				t.Fatalf("timeout/cancel sent command %v", command)
			}
		}
	})
}

func runPGRealDelayedCancel(t *testing.T, ctx context.Context, dsn, major string, tlsEnabled, proxied bool) {
	t.Helper()
	t.Run("delayed-cancel-forced-discard", func(t *testing.T) {
		table := pgMatrixTable("cancel", major, tlsEnabled, proxied)
		observer := pgMatrixObserver(t, ctx, dsn)
		defer observer.Close(ctx)
		if _, err := observer.Exec(ctx, "CREATE TABLE IF NOT EXISTS "+table+" (id bigint primary key)"); err != nil {
			t.Fatal(err)
		}
		if _, err := observer.Exec(ctx, "TRUNCATE "+table); err != nil {
			t.Fatal(err)
		}

		connection := pgMatrixObserver(t, ctx, dsn)
		if _, err := connection.Exec(ctx, "BEGIN"); err != nil {
			t.Fatal(err)
		}
		if _, err := connection.Exec(ctx, "INSERT INTO "+table+" VALUES (1)"); err != nil {
			t.Fatal(err)
		}
		pid := connection.PgConn().PID()
		secretBytes := connection.PgConn().SecretKey()
		if len(secretBytes) != 4 {
			t.Fatalf("cancel secret length = %d", len(secretBytes))
		}
		secret := binary.BigEndian.Uint32(secretBytes)
		identity := pgMatrixIdentity(t, ctx, connection, "cancel", major, tlsEnabled, proxied)
		guard := NewCancelGuard()
		adapter, err := NewPGXTerminalAdapter(connection.PgConn(), identity, guard, PGXPhysicalHooks{
			ConfirmBackendAbsence: func(inventoryContext context.Context, value PGBackendIdentity) (bool, error) {
				for {
					var exists bool
					queryErr := observer.QueryRow(inventoryContext, "SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE pid=$1 AND backend_start=$2)", value.PID, value.BackendStart).Scan(&exists)
					if queryErr != nil || !exists {
						return !exists, queryErr
					}
					select {
					case <-inventoryContext.Done():
						return false, inventoryContext.Err()
					case <-time.After(5 * time.Millisecond):
					}
				}
			},
		}, PGAdapterOptions{WatchdogTimeout: time.Second, ReconcileBudget: 500 * time.Millisecond})
		if err != nil {
			t.Fatal(err)
		}
		config, err := pgx.ParseConfig(dsn)
		if err != nil {
			t.Fatal(err)
		}
		emission, err := adapter.SendCancelRequest(ctx, func(dialContext context.Context) (net.Conn, error) {
			raw, dialErr := (&net.Dialer{}).DialContext(dialContext, "tcp", net.JoinHostPort(config.Host, fmt.Sprintf("%d", config.Port)))
			if dialErr != nil {
				return nil, dialErr
			}
			return &pgDelayedWriteConn{Conn: raw, delay: 25 * time.Millisecond}, nil
		}, pid, secret)
		if err != nil || emission != CancelFullPacketWritten {
			t.Fatalf("cancel emission=%v err=%v", emission, err)
		}
		owner := NewTerminalOwner(701, 801)
		attempt := acquireAttempt(t, owner, OperationRollback)
		result, err := adapter.FinishRollback(ctx, owner, attempt, validNoCommitForAttempt(attempt))
		if err != nil {
			t.Fatal(err)
		}
		if result.Resolution.Outcome != OutcomeNotCommitted || result.Resolution.Disposition != DispositionDiscarded || !result.CancelSnapshot.Tainted {
			t.Fatalf("cancel result = %+v", result)
		}
		for _, command := range []MainCommand{CommandRollback, CommandCommit, CommandReset, CommandHealthProbe} {
			if result.CancelSnapshot.Sent[command] != 0 {
				t.Fatalf("cancel-tainted connection sent command %v", command)
			}
		}
		var count int
		if err = observer.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("cancel truth count=%d err=%v", count, err)
		}
	})
}

func runPGRealFaultMatrix(t *testing.T, ctx context.Context, dsn, major string, tlsEnabled, proxied bool) {
	t.Helper()
	table := pgMatrixTable("faults", major, tlsEnabled, proxied)
	observer := pgMatrixObserver(t, ctx, dsn)
	defer observer.Close(ctx)
	if _, err := observer.Exec(ctx, "CREATE TABLE IF NOT EXISTS "+table+" (id bigint primary key)"); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name            string
		mode            pgRealFaultMode
		wantPhase       WritePhase
		wantOutcome     DBOutcome
		wantConsistency ConsistencyVerdict
		wantCommitted   bool
	}{
		{name: "before-send-zero", mode: pgFaultZeroWrite, wantPhase: WriteZeroBytes, wantOutcome: OutcomeNotCommitted, wantConsistency: VerdictConsistent},
		{name: "partial-frame", mode: pgFaultPartialWrite, wantPhase: WritePartial, wantOutcome: OutcomeUnknown, wantConsistency: VerdictConsistent},
		{name: "full-frame-ack-missing", mode: pgFaultDropAllReplies, wantPhase: WriteFullFrame, wantOutcome: OutcomeUnknown, wantConsistency: VerdictConsistent, wantCommitted: true},
		{name: "positive-ack-rfq-not-observed", mode: pgFaultACKOnly, wantPhase: WriteFullFrame, wantOutcome: OutcomeCommitted, wantConsistency: VerdictConsistent, wantCommitted: true},
		{name: "ack-missing-rfq-idle", mode: pgFaultDropACK, wantPhase: WriteFullFrame, wantOutcome: OutcomeUnknown, wantConsistency: VerdictConsistent, wantCommitted: true},
		{name: "ack-rfq-in-transaction", mode: pgFaultRFQInTransaction, wantPhase: WriteFullFrame, wantOutcome: OutcomeUnknown, wantConsistency: VerdictContradiction, wantCommitted: true},
		{name: "ack-rfq-failed", mode: pgFaultRFQFailed, wantPhase: WriteFullFrame, wantOutcome: OutcomeUnknown, wantConsistency: VerdictContradiction, wantCommitted: true},
		{name: "duplicate-rfq", mode: pgFaultDuplicateRFQ, wantPhase: WriteFullFrame, wantOutcome: OutcomeUnknown, wantConsistency: VerdictContradiction, wantCommitted: true},
	}

	for index, test := range cases {
		test := test
		t.Run(test.name, func(t *testing.T) {
			rowID := int64(index + 1000)
			connection := pgMatrixObserver(t, ctx, dsn)
			if _, err := connection.Exec(ctx, "BEGIN"); err != nil {
				t.Fatal(err)
			}
			if _, err := connection.Exec(ctx, "INSERT INTO "+table+" VALUES ($1)", rowID); err != nil {
				t.Fatal(err)
			}
			identity := pgMatrixIdentity(t, ctx, connection, test.name, major, tlsEnabled, proxied)
			adapter, err := NewPGXTerminalAdapter(connection.PgConn(), identity, nil, PGXPhysicalHooks{
				WrapWire: func(value net.Conn) net.Conn { return &pgRealFaultConn{Conn: value, mode: test.mode} },
				ConfirmBackendAbsence: func(context.Context, PGBackendIdentity) (bool, error) {
					return false, nil
				},
			}, PGAdapterOptions{WatchdogTimeout: 500 * time.Millisecond, ReconcileBudget: 20 * time.Millisecond})
			if err != nil {
				t.Fatal(err)
			}
			owner := NewTerminalOwner(uint64(300+index), uint64(400+index))
			attempt := acquireAttempt(t, owner, OperationCommit)
			result, err := adapter.FinishCommit(ctx, owner, attempt)
			if err != nil {
				t.Fatal(err)
			}
			if result.Evidence.Write.Phase != test.wantPhase || result.Resolution.Outcome != test.wantOutcome || result.Resolution.Consistency.Verdict != test.wantConsistency || result.Resolution.Disposition != DispositionDiscardUnconfirmed {
				t.Fatalf("fault result = %+v evidence=%+v", result.Resolution, result.Evidence)
			}
			deadline := time.Now().Add(time.Second)
			var count int
			for {
				err = observer.QueryRow(ctx, "SELECT count(*) FROM "+table+" WHERE id=$1", rowID).Scan(&count)
				if err != nil || count == 1 || !test.wantCommitted || time.Now().After(deadline) {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			wantCount := 0
			if test.wantCommitted {
				wantCount = 1
			}
			if err != nil || count != wantCount {
				t.Fatalf("fault truth count=%d want=%d err=%v", count, wantCount, err)
			}
		})
	}

	t.Run("deferred-rejection-rfq-not-observed", func(t *testing.T) {
		deferred := pgMatrixTable("fault_deferred", major, tlsEnabled, proxied)
		if _, err := observer.Exec(ctx, "DROP TABLE IF EXISTS "+deferred); err != nil {
			t.Fatal(err)
		}
		if _, err := observer.Exec(ctx, "CREATE TABLE "+deferred+" (v bigint UNIQUE DEFERRABLE INITIALLY DEFERRED)"); err != nil {
			t.Fatal(err)
		}
		connection := pgMatrixObserver(t, ctx, dsn)
		if _, err := connection.Exec(ctx, "BEGIN"); err != nil {
			t.Fatal(err)
		}
		if _, err := connection.Exec(ctx, "INSERT INTO "+deferred+" VALUES (1), (1)"); err != nil {
			t.Fatal(err)
		}
		identity := pgMatrixIdentity(t, ctx, connection, "deferred-ack-only", major, tlsEnabled, proxied)
		adapter, err := NewPGXTerminalAdapter(connection.PgConn(), identity, nil, PGXPhysicalHooks{
			WrapWire: func(value net.Conn) net.Conn { return &pgRealFaultConn{Conn: value, mode: pgFaultACKOnly} },
		}, PGAdapterOptions{WatchdogTimeout: 500 * time.Millisecond})
		if err != nil {
			t.Fatal(err)
		}
		owner := NewTerminalOwner(501, 601)
		attempt := acquireAttempt(t, owner, OperationCommit)
		result, err := adapter.FinishCommit(ctx, owner, attempt)
		if err != nil {
			t.Fatal(err)
		}
		if result.Resolution.Outcome != OutcomeCommitRejected || result.Resolution.Consistency.Verdict != VerdictConsistent || result.Resolution.Disposition != DispositionDiscardUnconfirmed {
			t.Fatalf("deferred ACK-only result = %+v", result)
		}
		var count int
		if err = observer.QueryRow(ctx, "SELECT count(*) FROM "+deferred).Scan(&count); err != nil || count != 0 {
			t.Fatalf("deferred ACK-only truth count=%d err=%v", count, err)
		}
	})
}

func runPGRealPositiveCommit(t *testing.T, ctx context.Context, dsn, major string, tlsEnabled, proxied bool) {
	t.Helper()
	table := pgMatrixTable("commit", major, tlsEnabled, proxied)
	observer := pgMatrixObserver(t, ctx, dsn)
	defer observer.Close(ctx)
	_, err := observer.Exec(ctx, "CREATE TABLE IF NOT EXISTS "+table+" (id bigint primary key)")
	if err != nil {
		t.Fatal(err)
	}
	_, err = observer.Exec(ctx, "TRUNCATE "+table)
	if err != nil {
		t.Fatal(err)
	}

	connection := pgMatrixObserver(t, ctx, dsn)
	if _, err = connection.Exec(ctx, "BEGIN"); err != nil {
		t.Fatal(err)
	}
	if _, err = connection.Exec(ctx, "INSERT INTO "+table+" VALUES (1)"); err != nil {
		t.Fatal(err)
	}
	adapter, returned := pgMatrixAdapter(t, ctx, connection, "positive", major, tlsEnabled, proxied)
	owner := NewTerminalOwner(101, 201)
	attempt := acquireAttempt(t, owner, OperationCommit)
	result, err := adapter.FinishCommit(ctx, owner, attempt)
	if err != nil {
		t.Fatal(err)
	}
	if result.Resolution.Outcome != OutcomeCommitted || result.Resolution.Disposition != DispositionReleased || result.Resolution.Consistency.Verdict != VerdictConsistent {
		t.Fatalf("positive result = %+v", result)
	}
	pgMatrixCloseReturned(t, ctx, returned)
	var count int
	if err = observer.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&count); err != nil || count != 1 {
		t.Fatalf("positive truth count=%d err=%v", count, err)
	}
}

func runPGRealDeferredRejection(t *testing.T, ctx context.Context, dsn, major string, tlsEnabled, proxied bool) {
	t.Helper()
	table := pgMatrixTable("deferred", major, tlsEnabled, proxied)
	observer := pgMatrixObserver(t, ctx, dsn)
	defer observer.Close(ctx)
	_, err := observer.Exec(ctx, "DROP TABLE IF EXISTS "+table)
	if err != nil {
		t.Fatal(err)
	}
	_, err = observer.Exec(ctx, "CREATE TABLE "+table+" (v bigint, CONSTRAINT "+table+"_uq UNIQUE(v) DEFERRABLE INITIALLY DEFERRED)")
	if err != nil {
		t.Fatal(err)
	}

	connection := pgMatrixObserver(t, ctx, dsn)
	if _, err = connection.Exec(ctx, "BEGIN"); err != nil {
		t.Fatal(err)
	}
	if _, err = connection.Exec(ctx, "INSERT INTO "+table+" VALUES (1), (1)"); err != nil {
		t.Fatal(err)
	}
	adapter, returned := pgMatrixAdapter(t, ctx, connection, "deferred", major, tlsEnabled, proxied)
	owner := NewTerminalOwner(102, 202)
	attempt := acquireAttempt(t, owner, OperationCommit)
	result, err := adapter.FinishCommit(ctx, owner, attempt)
	if err != nil {
		t.Fatal(err)
	}
	if result.Resolution.Outcome != OutcomeCommitRejected || result.Resolution.Disposition != DispositionReleased || result.Resolution.Consistency.Verdict != VerdictConsistent {
		t.Fatalf("deferred result = %+v", result)
	}
	pgMatrixCloseReturned(t, ctx, returned)
	var count int
	if err = observer.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&count); err != nil || count != 0 {
		t.Fatalf("deferred truth count=%d err=%v", count, err)
	}
}

func runPGRealRollback(t *testing.T, ctx context.Context, dsn, major string, tlsEnabled, proxied bool) {
	t.Helper()
	table := pgMatrixTable("rollback", major, tlsEnabled, proxied)
	observer := pgMatrixObserver(t, ctx, dsn)
	defer observer.Close(ctx)
	_, err := observer.Exec(ctx, "CREATE TABLE IF NOT EXISTS "+table+" (id bigint primary key)")
	if err != nil {
		t.Fatal(err)
	}
	_, err = observer.Exec(ctx, "TRUNCATE "+table)
	if err != nil {
		t.Fatal(err)
	}

	connection := pgMatrixObserver(t, ctx, dsn)
	if _, err = connection.Exec(ctx, "BEGIN"); err != nil {
		t.Fatal(err)
	}
	if _, err = connection.Exec(ctx, "INSERT INTO "+table+" VALUES (1)"); err != nil {
		t.Fatal(err)
	}
	adapter, returned := pgMatrixAdapter(t, ctx, connection, "rollback", major, tlsEnabled, proxied)
	owner := NewTerminalOwner(103, 203)
	attempt := acquireAttempt(t, owner, OperationRollback)
	result, err := adapter.FinishRollback(ctx, owner, attempt, validNoCommitForAttempt(attempt))
	if err != nil {
		t.Fatal(err)
	}
	if result.Resolution.Outcome != OutcomeNotCommitted || result.Resolution.Disposition != DispositionReleased || result.Resolution.Consistency.Verdict != VerdictConsistent {
		t.Fatalf("rollback result = %+v", result)
	}
	pgMatrixCloseReturned(t, ctx, returned)
	var count int
	if err = observer.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rollback truth count=%d err=%v", count, err)
	}
}

func pgMatrixAdapter(t *testing.T, ctx context.Context, connection *pgx.Conn, label, major string, tlsEnabled, proxied bool) (*PGTerminalAdapter, **pgconn.PgConn) {
	t.Helper()
	identity := pgMatrixIdentity(t, ctx, connection, label, major, tlsEnabled, proxied)
	var returned *pgconn.PgConn
	adapter, err := NewPGXTerminalAdapter(connection.PgConn(), identity, nil, PGXPhysicalHooks{
		ReturnToPool: func(ctx context.Context, value *pgconn.PgConn, returnedIdentity PGBackendIdentity) error {
			if returnedIdentity != identity || value.PID() != identity.PID {
				return ErrPGPoolTransfer
			}
			results, queryErr := value.Exec(ctx, "SELECT 1").ReadAll()
			if queryErr != nil || len(results) != 1 || results[0].Err != nil {
				return fmt.Errorf("returned connection health: %w", errors.Join(queryErr, resultsError(results)))
			}
			returned = value
			return nil
		},
		ConfirmBackendAbsence: func(context.Context, PGBackendIdentity) (bool, error) {
			return false, nil
		},
	}, PGAdapterOptions{WatchdogTimeout: 2 * time.Second, ReconcileBudget: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	return adapter, &returned
}

func pgMatrixIdentity(t *testing.T, ctx context.Context, connection *pgx.Conn, label, major string, tlsEnabled, proxied bool) PGBackendIdentity {
	t.Helper()
	var backendStart time.Time
	if err := connection.QueryRow(ctx, "SELECT pg_postmaster_start_time()").Scan(&backendStart); err != nil {
		t.Fatal(err)
	}
	return PGBackendIdentity{
		ServerIdentity:  fmt.Sprintf("container-pg%s-tls-%t", major, tlsEnabled),
		Database:        "agentsql_terminal",
		PID:             connection.PgConn().PID(),
		BackendStart:    backendStart,
		ConnectionNonce: fmt.Sprintf("%s-pg%s-tls-%t-proxy-%t-%d", label, major, tlsEnabled, proxied, time.Now().UnixNano()),
	}
}

func resultsError(results []*pgconn.Result) error {
	for _, result := range results {
		if result.Err != nil {
			return result.Err
		}
	}
	return nil
}

func pgMatrixCloseReturned(t *testing.T, ctx context.Context, returned **pgconn.PgConn) {
	t.Helper()
	if returned == nil || *returned == nil {
		t.Fatal("physical connection was not returned")
	}
	if err := (*returned).Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func pgMatrixObserver(t *testing.T, ctx context.Context, dsn string) *pgx.Conn {
	t.Helper()
	connection, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	return connection
}

func pgMatrixTable(kind, major string, tlsEnabled, proxied bool) string {
	return fmt.Sprintf("b5_terminal_%s_%s_%t_%t", kind, major, tlsEnabled, proxied)
}

type pgRealFaultMode uint8

const (
	pgFaultZeroWrite pgRealFaultMode = iota + 1
	pgFaultPartialWrite
	pgFaultDropAllReplies
	pgFaultACKOnly
	pgFaultDropACK
	pgFaultRFQInTransaction
	pgFaultRFQFailed
	pgFaultDuplicateRFQ
)

// pgRealFaultConn sits above pgx's TLS connection. Therefore byte counts and
// mutations are PostgreSQL application frames for both TLS modes.
type pgRealFaultConn struct {
	net.Conn
	mode pgRealFaultMode

	mu             sync.Mutex
	terminalWrite  bool
	ackDelivered   bool
	filteredOutput bytes.Buffer
}

type pgDelayedWriteConn struct {
	net.Conn
	delay time.Duration
}

func (connection *pgDelayedWriteConn) Write(source []byte) (int, error) {
	time.Sleep(connection.delay)
	return connection.Conn.Write(source)
}

func (connection *pgRealFaultConn) Write(source []byte) (int, error) {
	connection.mu.Lock()
	terminal := !connection.terminalWrite && isCommitFrame(source)
	if terminal {
		connection.terminalWrite = true
	}
	mode := connection.mode
	connection.mu.Unlock()
	if !terminal {
		return connection.Conn.Write(source)
	}
	switch mode {
	case pgFaultZeroWrite:
		return 0, io.ErrClosedPipe
	case pgFaultPartialWrite:
		limit := 3
		if len(source) < limit {
			limit = len(source)
		}
		n, _ := connection.Conn.Write(source[:limit])
		return n, io.ErrUnexpectedEOF
	default:
		return connection.Conn.Write(source)
	}
}

func (connection *pgRealFaultConn) Read(target []byte) (int, error) {
	connection.mu.Lock()
	defer connection.mu.Unlock()
	if connection.filteredOutput.Len() > 0 {
		return connection.filteredOutput.Read(target)
	}
	if !connection.terminalWrite {
		return connection.Conn.Read(target)
	}
	if connection.mode == pgFaultDropAllReplies || (connection.mode == pgFaultACKOnly && connection.ackDelivered) {
		time.Sleep(25 * time.Millisecond)
		return 0, io.EOF
	}
	for {
		frame, err := readPGBackendWireFrame(connection.Conn)
		if err != nil {
			return 0, err
		}
		messageType := frame[0]
		switch connection.mode {
		case pgFaultACKOnly:
			if messageType == 'C' || messageType == 'E' {
				connection.ackDelivered = true
				_, _ = connection.filteredOutput.Write(frame)
			}
		case pgFaultDropACK:
			if messageType != 'C' && messageType != 'E' {
				_, _ = connection.filteredOutput.Write(frame)
			}
		case pgFaultRFQInTransaction:
			if messageType == 'Z' && len(frame) == 6 {
				frame[5] = 'T'
			}
			_, _ = connection.filteredOutput.Write(frame)
		case pgFaultRFQFailed:
			if messageType == 'Z' && len(frame) == 6 {
				frame[5] = 'E'
			}
			_, _ = connection.filteredOutput.Write(frame)
		case pgFaultDuplicateRFQ:
			_, _ = connection.filteredOutput.Write(frame)
			if messageType == 'Z' {
				_, _ = connection.filteredOutput.Write(frame)
			}
		default:
			_, _ = connection.filteredOutput.Write(frame)
		}
		if connection.filteredOutput.Len() > 0 {
			return connection.filteredOutput.Read(target)
		}
	}
}

func isCommitFrame(frame []byte) bool {
	return len(frame) == len(encodeSimpleQuery("COMMIT")) && bytes.Equal(frame, encodeSimpleQuery("COMMIT"))
}

func readPGBackendWireFrame(connection net.Conn) ([]byte, error) {
	header := make([]byte, 5)
	if _, err := io.ReadFull(connection, header); err != nil {
		return nil, err
	}
	length := binary.BigEndian.Uint32(header[1:5])
	if length < 4 || length > 16<<20 {
		return nil, ErrPGProtocol
	}
	body := make([]byte, int(length)-4)
	if _, err := io.ReadFull(connection, body); err != nil {
		return nil, err
	}
	return append(header, body...), nil
}

func startPGTerminalContainer(t *testing.T, ctx context.Context, major string, tlsEnabled bool) (string, testcontainers.Container) {
	t.Helper()
	request := testcontainers.ContainerRequest{
		Image:        "postgres:" + major,
		Env:          map[string]string{"POSTGRES_DB": "agentsql_terminal", "POSTGRES_USER": "agentsql", "POSTGRES_PASSWORD": "terminal-password"},
		ExposedPorts: []string{"5432/tcp"},
		WaitingFor: wait.ForAll(
			wait.ForListeningPort("5432/tcp"),
			wait.ForLog("database system is ready to accept connections").WithOccurrence(2),
		).WithDeadline(90 * time.Second),
	}
	if tlsEnabled {
		certificate, key := pgMatrixCertificate(t)
		request.Files = []testcontainers.ContainerFile{
			{Reader: strings.NewReader(certificate), ContainerFilePath: "/tmp/agentsql-server.crt", FileMode: 0644},
			{Reader: strings.NewReader(key), ContainerFilePath: "/tmp/agentsql-server.key", FileMode: 0600},
		}
		request.Entrypoint = []string{"bash", "-ceu", "chown postgres:postgres /tmp/agentsql-server.crt /tmp/agentsql-server.key; chmod 0600 /tmp/agentsql-server.key; exec /usr/local/bin/docker-entrypoint.sh postgres -c ssl=on -c ssl_cert_file=/tmp/agentsql-server.crt -c ssl_key_file=/tmp/agentsql-server.key"}
	}
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{ContainerRequest: request, Started: true})
	if err != nil {
		if container != nil {
			testcontainers.CleanupContainer(t, container)
		}
		t.Fatal(err)
	}
	testcontainers.CleanupContainer(t, container)
	host, err := container.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	port, err := container.MappedPort(ctx, "5432/tcp")
	if err != nil {
		t.Fatal(err)
	}
	sslMode := "disable"
	if tlsEnabled {
		sslMode = "require"
	}
	return fmt.Sprintf("postgres://agentsql:terminal-password@%s:%s/agentsql_terminal?sslmode=%s", host, port.Port(), sslMode), container
}

func pgMatrixCertificate(t *testing.T) (string, string) {
	t.Helper()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		t.Fatal(err)
	}
	template := x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: "localhost"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		KeyUsage:    x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:    []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &privateKey.PublicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	key := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey)})
	return string(certificate), string(key)
}

func startPGTCPProxy(t *testing.T, directDSN string) string {
	t.Helper()
	config, err := pgx.ParseConfig(directDSN)
	if err != nil {
		t.Fatal(err)
	}
	upstream := net.JoinHostPort(config.Host, fmt.Sprintf("%d", config.Port))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	go func() {
		for {
			client, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			server, dialErr := net.Dial("tcp", upstream)
			if dialErr != nil {
				_ = client.Close()
				continue
			}
			go proxyPGDirection(client, server, stop)
			go proxyPGDirection(server, client, stop)
		}
	}()
	t.Cleanup(func() {
		close(stop)
		_ = listener.Close()
	})
	config.Host = "127.0.0.1"
	config.Port = uint16(listener.Addr().(*net.TCPAddr).Port)
	return config.ConnString()
}

func proxyPGDirection(destination, source net.Conn, stop <-chan struct{}) {
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(destination, source)
		_ = destination.Close()
		_ = source.Close()
		close(done)
	}()
	select {
	case <-stop:
		_ = destination.Close()
		_ = source.Close()
	case <-done:
	}
}

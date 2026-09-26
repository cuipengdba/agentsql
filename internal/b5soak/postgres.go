//go:build agentsql_b5_soak

package b5soak

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cuipengdba/agentsql/internal/b5terminal"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type backendKey struct {
	PID     uint32
	Started time.Time
}

type claimMonitor struct {
	mu     sync.RWMutex
	active map[backendKey]struct{}
}

func newClaimMonitor() *claimMonitor {
	return &claimMonitor{active: make(map[backendKey]struct{})}
}

func (monitor *claimMonitor) drift(observed map[backendKey]struct{}) int64 {
	drift := int64(len(monitor.active)) - int64(len(observed))
	if drift != 0 {
		return drift
	}
	for key := range monitor.active {
		if _, exists := observed[key]; !exists {
			// Counts can match while identities differ; any mismatch is drift.
			return 1
		}
	}
	return 0
}

type pgRuntime struct {
	config             *pgx.ConnConfig
	observerConfig     *pgx.ConnConfig
	observerMu         sync.Mutex
	observer           *pgx.Conn
	table              string
	applicationName    string
	claims             *claimMonitor
	testInventoryFault atomic.Int64
}

func openPGRuntime(ctx context.Context, dsn, runID string) (*pgRuntime, string, bool, error) {
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, "", false, err
	}
	observerConfig := config.Copy()
	observerConfig.RuntimeParams["application_name"] = "agentsql-s10-observer"
	observer, err := pgx.ConnectConfig(ctx, observerConfig)
	if err != nil {
		return nil, "", false, err
	}
	var version string
	var tls bool
	if err := observer.QueryRow(ctx, `SELECT version(), COALESCE((SELECT ssl FROM pg_stat_ssl WHERE pid=pg_backend_pid()), false)`).Scan(&version, &tls); err != nil {
		_ = observer.Close(ctx)
		return nil, "", false, err
	}
	runtime := &pgRuntime{
		config: config, observerConfig: observerConfig, observer: observer, table: "agentsql_b5_soak_" + runID,
		applicationName: "agentsql-s10-" + runID, claims: newClaimMonitor(),
	}
	if _, err := observer.Exec(ctx, `CREATE TABLE `+runtime.table+` (terminal_id bigint PRIMARY KEY, transaction_id text NOT NULL, created_at timestamptz NOT NULL DEFAULT clock_timestamp())`); err != nil {
		_ = observer.Close(ctx)
		return nil, "", false, err
	}
	return runtime, version, tls, nil
}

func (runtime *pgRuntime) close(ctx context.Context) error {
	if runtime == nil {
		return nil
	}
	runtime.observerMu.Lock()
	defer runtime.observerMu.Unlock()
	var dropErr error
	if runtime.observer == nil || runtime.observer.IsClosed() {
		runtime.observer, dropErr = pgx.ConnectConfig(ctx, runtime.observerConfig.Copy())
	}
	if dropErr == nil {
		_, dropErr = runtime.observer.Exec(ctx, `DROP TABLE IF EXISTS `+runtime.table)
	}
	var closeErr error
	if runtime.observer != nil {
		closeErr = runtime.observer.Close(ctx)
		runtime.observer = nil
	}
	return errors.Join(dropErr, closeErr)
}

func (runtime *pgRuntime) inventory(ctx context.Context) (drift int64, reconnected bool, err error) {
	if runtime.testInventoryFault.Load() > 0 {
		runtime.testInventoryFault.Add(-1)
		runtime.observerMu.Lock()
		if runtime.observer != nil {
			_ = runtime.observer.Close(context.Background())
			runtime.observer = nil
		}
		runtime.observerMu.Unlock()
		return 0, false, errors.New("b5soak: injected observer disconnect")
	}
	runtime.claims.mu.RLock()
	defer runtime.claims.mu.RUnlock()
	observed := make(map[backendKey]struct{})
	reconnected, err = runtime.withObserver(ctx, func(observer *pgx.Conn) error {
		rows, queryErr := observer.Query(ctx, `SELECT pid, backend_start FROM pg_stat_activity WHERE datname=current_database() AND application_name=$1`, runtime.applicationName)
		if queryErr != nil {
			return queryErr
		}
		defer rows.Close()
		for rows.Next() {
			var pid uint32
			var started time.Time
			if scanErr := rows.Scan(&pid, &started); scanErr != nil {
				return scanErr
			}
			observed[backendKey{PID: pid, Started: started.UTC()}] = struct{}{}
		}
		return rows.Err()
	})
	if err != nil {
		return 0, reconnected, err
	}
	return runtime.claims.drift(observed), reconnected, nil
}

func (runtime *pgRuntime) databaseTruth(ctx context.Context) (uint64, bool, error) {
	var count uint64
	reconnected, err := runtime.withObserver(ctx, func(observer *pgx.Conn) error {
		return observer.QueryRow(ctx, `SELECT count(*) FROM `+runtime.table).Scan(&count)
	})
	return count, reconnected, err
}

func (runtime *pgRuntime) backendAbsent(ctx context.Context, backend b5terminal.PGBackendIdentity) (bool, error) {
	var exists bool
	_, err := runtime.withObserver(ctx, func(observer *pgx.Conn) error {
		return observer.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE pid=$1 AND backend_start=$2)`, backend.PID, backend.BackendStart).Scan(&exists)
	})
	return !exists, err
}

func (runtime *pgRuntime) withObserver(ctx context.Context, operation func(*pgx.Conn) error) (bool, error) {
	runtime.observerMu.Lock()
	defer runtime.observerMu.Unlock()
	reconnected := false
	if runtime.observer == nil || runtime.observer.IsClosed() {
		observer, err := pgx.ConnectConfig(ctx, runtime.observerConfig.Copy())
		if err != nil {
			return false, err
		}
		runtime.observer = observer
		reconnected = true
	}
	if err := operation(runtime.observer); err != nil {
		_ = runtime.observer.Close(context.Background())
		runtime.observer = nil
		return reconnected, err
	}
	return reconnected, nil
}

// injectInventoryFailures is a package-test hook. It is not configurable from
// the command and cannot alter a server or product flag.
func (runtime *pgRuntime) injectInventoryFailures(count int64) {
	if count > 0 {
		runtime.testInventoryFault.Add(count)
	}
}

func (runtime *pgRuntime) terminal(ctx context.Context, ordinal uint64, transactionID string) (b5terminal.PGTerminalResult, error) {
	var empty b5terminal.PGTerminalResult
	connectionConfig := runtime.config.Copy()
	connectionConfig.RuntimeParams["application_name"] = "agentsql-s10-bootstrap"
	connection, err := pgx.ConnectConfig(ctx, connectionConfig)
	if err != nil {
		return empty, err
	}
	ownedByPGX := true
	defer func() {
		if ownedByPGX {
			_ = connection.Close(context.Background())
		}
	}()
	if _, err := connection.Exec(ctx, "BEGIN"); err != nil {
		return empty, err
	}
	var backendStart time.Time
	if err := connection.QueryRow(ctx, `SELECT backend_start FROM pg_stat_activity WHERE pid=pg_backend_pid()`).Scan(&backendStart); err != nil {
		return empty, err
	}
	key := backendKey{PID: connection.PgConn().PID(), Started: backendStart.UTC()}
	runtime.claims.mu.Lock()
	if _, err := connection.Exec(ctx, `SELECT set_config('application_name', $1, false)`, runtime.applicationName); err != nil {
		runtime.claims.mu.Unlock()
		return empty, err
	}
	runtime.claims.active[key] = struct{}{}
	runtime.claims.mu.Unlock()
	claimActive := true
	removeClaimAndClose := func() {
		runtime.claims.mu.Lock()
		if claimActive {
			_ = connection.PgConn().Close(context.Background())
			delete(runtime.claims.active, key)
			claimActive = false
		}
		runtime.claims.mu.Unlock()
	}
	defer func() {
		if claimActive {
			removeClaimAndClose()
		}
	}()
	if _, err := connection.Exec(ctx, fmt.Sprintf(`INSERT INTO %s (terminal_id, transaction_id) VALUES ($1, $2)`, runtime.table), ordinal, transactionID); err != nil {
		return empty, err
	}
	identity := b5terminal.PGBackendIdentity{
		ServerIdentity: connection.Config().Host,
		Database:       connection.Config().Database,
		PID:            key.PID, BackendStart: key.Started,
		ConnectionNonce: transactionID,
	}
	adapter, err := newPGXAdapter(connection.PgConn(), identity, b5terminal.PGAdapterOptions{
		WatchdogTimeout: 5 * time.Second,
		ReconcileBudget: time.Second,
	}, func(closeContext context.Context, reconstructed *pgconn.PgConn) error {
		return reconstructed.Close(closeContext)
	}, func(inventoryContext context.Context, backend b5terminal.PGBackendIdentity) (bool, error) {
		return runtime.backendAbsent(inventoryContext, backend)
	})
	if err != nil {
		return empty, err
	}
	ownedByPGX = false
	owner := b5terminal.NewTerminalOwner(1, 1)
	attempt, ok := owner.TryCommit(1, 1)
	if !ok {
		return empty, errors.New("b5soak: terminal owner rejected commit")
	}
	// Holding the write lock makes the independent inventory snapshot atomic
	// with physical release/destruction and its ledger transition.
	runtime.claims.mu.Lock()
	result, finishErr := adapter.FinishCommit(ctx, owner, attempt)
	delete(runtime.claims.active, key)
	claimActive = false
	runtime.claims.mu.Unlock()
	return result, finishErr
}

func newPGXAdapter(
	pgConnection *pgconn.PgConn,
	identity b5terminal.PGBackendIdentity,
	options b5terminal.PGAdapterOptions,
	returnConnection func(context.Context, *pgconn.PgConn) error,
	confirmAbsence func(context.Context, b5terminal.PGBackendIdentity) (bool, error),
) (*b5terminal.PGTerminalAdapter, error) {
	if pgConnection == nil || pgConnection.IsClosed() || pgConnection.IsBusy() || pgConnection.TxStatus() != 'T' || identity.PID != pgConnection.PID() {
		return nil, b5terminal.ErrPGInvalidPhysicalLease
	}
	hijacked, err := pgConnection.Hijack()
	if err != nil {
		return nil, err
	}
	if hijacked.Frontend == nil || hijacked.Frontend.ReadBufferLen() != 0 {
		_ = hijacked.Conn.Close()
		return nil, b5terminal.ErrPGUncleanCommandBoundary
	}
	lease := b5terminal.PGPhysicalConnection{
		Conn: hijacked.Conn, Identity: identity, CleanCommandBoundary: true, UniqueSocketOwner: true,
		ConfirmBackendAbsence: confirmAbsence,
		TransferToPool: func(ctx context.Context, connection net.Conn, _ b5terminal.PGBackendIdentity) error {
			hijacked.Conn = connection
			hijacked.TxStatus = 'I'
			reconstructed, constructErr := pgconn.Construct(hijacked)
			if constructErr != nil {
				return constructErr
			}
			return returnConnection(ctx, reconstructed)
		},
	}
	return b5terminal.NewPGTerminalAdapter(lease, nil, options)
}

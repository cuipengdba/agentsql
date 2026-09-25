package b5session

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync/atomic"
	"time"
)

// PGInventory uses a separately supplied administrative pool. It never uses
// the socket being reconciled. Exact termination predicates include backend
// start time and the authenticated dial token, so PID reuse cannot kill an
// unrelated backend.
type PGInventory struct {
	db       *sql.DB
	serverID string
	epoch    atomic.Uint64
}

func NewPGInventory(ctx context.Context, db *sql.DB) (*PGInventory, error) {
	if ctx == nil || db == nil {
		return nil, ErrInventoryUnavailable
	}
	var serverID string
	if err := db.QueryRowContext(ctx, `SELECT system_identifier::text FROM pg_control_system()`).Scan(&serverID); err != nil {
		return nil, fmt.Errorf("postgres inventory server identity: %w", err)
	}
	result := &PGInventory{db: db, serverID: serverID}
	result.epoch.Store(uint64(time.Now().UTC().UnixNano()))
	return result, nil
}

func (inventory *PGInventory) ObservePermit(ctx context.Context, token string) (InventoryObservation, error) {
	if token == "" {
		return InventoryObservation{}, ErrInvalidDialIdentity
	}
	rows, err := inventory.db.QueryContext(ctx, `SELECT pid,datname,backend_start,application_name FROM pg_stat_activity WHERE backend_type='client backend' AND application_name=$1 ORDER BY pid`, token)
	if err != nil {
		return InventoryObservation{}, fmt.Errorf("%w: %v", ErrInventoryUnavailable, err)
	}
	defer rows.Close()
	observation := InventoryObservation{Epoch: inventory.nextEpoch(), ObservedAt: time.Now().UTC()}
	for rows.Next() {
		var backend BackendIdentity
		if err := rows.Scan(&backend.PID, &backend.Database, &backend.BackendStarted, &backend.DialToken); err != nil {
			return InventoryObservation{}, err
		}
		backend.ServerID = inventory.serverID
		backend.BackendStarted = normalizeBackendTime(backend.BackendStarted)
		observation.Backends = append(observation.Backends, backend)
	}
	if err := rows.Err(); err != nil {
		return InventoryObservation{}, fmt.Errorf("%w: %v", ErrInventoryUnavailable, err)
	}
	return observation, nil
}

func (inventory *PGInventory) Terminate(ctx context.Context, backend BackendIdentity) error {
	if !backend.Complete() || backend.ServerID != inventory.serverID {
		return ErrInvalidDialIdentity
	}
	var terminated bool
	err := inventory.db.QueryRowContext(ctx, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE pid=$1 AND datname=$2 AND backend_start=$3 AND application_name=$4 AND backend_type='client backend'`, backend.PID, backend.Database, normalizeBackendTime(backend.BackendStarted), backend.DialToken).Scan(&terminated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("terminate backend %d: %w", backend.PID, err)
	}
	if !terminated {
		return fmt.Errorf("terminate backend %d: %w", backend.PID, ErrBackendStillPresent)
	}
	return nil
}

func (inventory *PGInventory) nextEpoch() uint64 {
	for {
		old := inventory.epoch.Load()
		next := uint64(time.Now().UTC().UnixNano())
		if next <= old {
			next = old + 1
		}
		if inventory.epoch.CompareAndSwap(old, next) {
			return next
		}
	}
}

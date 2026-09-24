package businessdb

import (
	"context"
	"errors"
)

var errMySQLAuxiliaryCancelDisabled = errors.New("mysql auxiliary cancel is disabled")

// mysqlCancelRows is deliberately narrower than database/sql.Rows so the
// cancellation state machine can be tested without exporting a driver handle.
type mysqlCancelRows interface {
	Next() bool
	Err() error
	Close() error
}

type mysqlCancelTarget interface {
	ThreadID() uint64
	Rows() mysqlCancelRows
	Rollback(context.Context) error
	Discard() error
}

type mysqlCancelControl interface {
	KillQuery(context.Context, uint64) error
}

type mysqlAuxiliaryCanceller struct {
	enabled bool
	control mysqlCancelControl
}

// cancel follows the only safe auxiliary cancellation order: kill the query,
// drain and check the row stream, close it, rollback, then unconditionally
// discard the physical connection. S2 ships this adapter feature-off; S6 may
// wire a separately credentialed control pool after its threat-model signoff.
func (canceller mysqlAuxiliaryCanceller) cancel(ctx context.Context, target mysqlCancelTarget) error {
	if !canceller.enabled {
		return errMySQLAuxiliaryCancelDisabled
	}
	if canceller.control == nil || target == nil {
		return errors.New("mysql auxiliary cancel is unavailable")
	}
	var failures []error
	if err := canceller.control.KillQuery(ctx, target.ThreadID()); err != nil {
		failures = append(failures, err)
	}
	if rows := target.Rows(); rows != nil {
		for rows.Next() {
		}
		if err := rows.Err(); err != nil {
			failures = append(failures, err)
		}
		if err := rows.Close(); err != nil {
			failures = append(failures, err)
		}
	}
	if err := target.Rollback(ctx); err != nil {
		failures = append(failures, err)
	}
	if err := target.Discard(); err != nil {
		failures = append(failures, err)
	}
	return errors.Join(failures...)
}

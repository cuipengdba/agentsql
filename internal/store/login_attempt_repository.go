package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
)

const (
	sharedLoginAttemptLimit = 10
	sharedLoginBucketLimit  = 8192
	sharedLoginAdvisoryKey  = int64(0x4153514c4c4f4701)
)

// LoginAttemptRepository atomically accounts for password and LDAP attempts
// across gateways sharing one PostgreSQL metadata database. Keys are HMACs
// supplied by the caller; no tenant or account name is persisted here.
type LoginAttemptRepository struct{ repositoryBase }

// Allow consumes one attempt in a 60-second window measured by PostgreSQL's
// clock. A database failure returns an error, which callers must fail closed.
func (repository *LoginAttemptRepository) Allow(ctx context.Context, key [sha256.Size]byte) (allowed bool, err error) {
	if ctx == nil {
		return false, ErrNilContext
	}
	if repository == nil || repository.db == nil || repository.dialect != DialectPostgres {
		return false, errors.New("shared login attempt store is unavailable")
	}
	tx, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin shared login attempt: %w", err)
	}
	defer tx.Rollback()
	// One transaction-wide lock makes the global bucket cap and per-account
	// counter atomic, including when two gateways first see the same account.
	if _, err = tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1)", sharedLoginAdvisoryKey); err != nil {
		return false, fmt.Errorf("lock shared login budget: %w", err)
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM login_attempt_budgets WHERE started_at <= clock_timestamp() - interval '60 seconds'"); err != nil {
		return false, fmt.Errorf("expire shared login budgets: %w", err)
	}
	var attempts int
	err = tx.QueryRowContext(ctx, "SELECT attempts FROM login_attempt_budgets WHERE key_hash=$1", key[:]).Scan(&attempts)
	switch {
	case err == nil:
		if attempts >= sharedLoginAttemptLimit {
			return false, nil
		}
		_, err = tx.ExecContext(ctx, "UPDATE login_attempt_budgets SET attempts=attempts+1 WHERE key_hash=$1", key[:])
	case errors.Is(err, sql.ErrNoRows):
		var bucketCount int
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM login_attempt_budgets").Scan(&bucketCount); err != nil {
			return false, fmt.Errorf("count shared login budgets: %w", err)
		}
		if bucketCount >= sharedLoginBucketLimit {
			return false, nil
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO login_attempt_budgets(key_hash,started_at,attempts) VALUES($1,clock_timestamp(),1)", key[:])
	default:
		return false, fmt.Errorf("read shared login budget: %w", err)
	}
	if err != nil {
		return false, fmt.Errorf("consume shared login budget: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return false, fmt.Errorf("commit shared login budget: %w", err)
	}
	return true, nil
}

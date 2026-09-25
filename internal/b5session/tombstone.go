package b5session

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"time"
)

const MaxTombstoneTerminalCodeBytes = 96

// Tombstone is digest-only by construction: it has no SQL, plan, rows,
// response, reason, or secret field that a caller could accidentally retain.
type Tombstone struct {
	ID              string
	SessionIDDigest [32]byte
	TerminalCode    string
	FinalSeq        uint64
	OwnerEpoch      uint64
	EventDigest     [32]byte
	ExpiresAt       time.Time
}

type TombstoneBudget struct {
	MaxEntries, MaxChurnPerSecond int
	MaxBytes                      int64
}

func DigestSessionID(sessionID string) [32]byte { return sha256.Sum256([]byte(sessionID)) }

func tombstoneCharge(value Tombstone) (int, error) {
	if value.ID == "" || value.SessionIDDigest == ([32]byte{}) || value.EventDigest == ([32]byte{}) || value.TerminalCode == "" || len(value.TerminalCode) > MaxTombstoneTerminalCodeBytes || value.OwnerEpoch == 0 || value.ExpiresAt.IsZero() {
		return 0, ErrAdmissionDenied
	}
	bytes := 128 + len(value.ID) + len(value.TerminalCode)
	return (bytes + 255) &^ 255, nil
}

func (l *SQLLedger) ConfigureTombstones(ctx context.Context, budget TombstoneBudget, now time.Time) error {
	if budget.MaxEntries <= 0 || budget.MaxBytes <= 0 || budget.MaxChurnPerSecond <= 0 {
		return ErrAdmissionDenied
	}
	result, err := l.db.ExecContext(ctx, `INSERT INTO b5_s3_tombstone_budget(singleton,max_entries,max_bytes,max_churn_per_second,churn_window) VALUES(TRUE,$1,$2,$3,$4) ON CONFLICT(singleton) DO UPDATE SET max_entries=EXCLUDED.max_entries,max_bytes=EXCLUDED.max_bytes,max_churn_per_second=EXCLUDED.max_churn_per_second WHERE b5_s3_tombstone_budget.used_entries<=EXCLUDED.max_entries AND b5_s3_tombstone_budget.used_bytes<=EXCLUDED.max_bytes`, budget.MaxEntries, budget.MaxBytes, budget.MaxChurnPerSecond, now.UTC().Truncate(time.Second))
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return ErrAdmissionDenied
	}
	return nil
}

func (l *SQLLedger) PutTombstone(ctx context.Context, value Tombstone, now time.Time) error {
	charge, err := tombstoneCharge(value)
	if err != nil || !value.ExpiresAt.After(now) {
		return ErrAdmissionDenied
	}
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	window := now.UTC().Truncate(time.Second)
	result, err := tx.ExecContext(ctx, `UPDATE b5_s3_tombstone_budget SET used_entries=used_entries+1,used_bytes=used_bytes+$1,churn_window=CASE WHEN churn_window=$2 THEN churn_window ELSE $2 END,churn_count=CASE WHEN churn_window=$2 THEN churn_count+1 ELSE 1 END WHERE singleton AND used_entries+1<=max_entries AND used_bytes+$1<=max_bytes AND (churn_window<>$2 OR churn_count+1<=max_churn_per_second)`, charge, window)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return ErrAdmissionDenied
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO b5_s3_tombstones(tombstone_id,session_id_digest,terminal_code,final_seq,owner_epoch,event_digest,expires_at,charged_bytes) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, value.ID, value.SessionIDDigest[:], value.TerminalCode, value.FinalSeq, value.OwnerEpoch, value.EventDigest[:], value.ExpiresAt.UTC(), charge)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (l *SQLLedger) GCTombstones(ctx context.Context, now time.Time, limit int) (int, error) {
	if limit <= 0 || limit > 10000 {
		return 0, ErrAdmissionDenied
	}
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `DELETE FROM b5_s3_tombstones WHERE tombstone_id IN (SELECT tombstone_id FROM b5_s3_tombstones WHERE expires_at<=$1 ORDER BY expires_at,tombstone_id LIMIT $2 FOR UPDATE SKIP LOCKED) RETURNING charged_bytes`, now.UTC(), limit)
	if err != nil {
		return 0, err
	}
	count, bytes := 0, int64(0)
	for rows.Next() {
		var charge int64
		if scanErr := rows.Scan(&charge); scanErr != nil {
			rows.Close()
			return 0, scanErr
		}
		count++
		bytes += charge
	}
	if err = rows.Close(); err != nil {
		return 0, err
	}
	if count == 0 {
		return 0, nil
	}
	window := now.UTC().Truncate(time.Second)
	result, err := tx.ExecContext(ctx, `UPDATE b5_s3_tombstone_budget SET used_entries=used_entries-$1,used_bytes=used_bytes-$2,churn_window=CASE WHEN churn_window=$3 THEN churn_window ELSE $3 END,churn_count=CASE WHEN churn_window=$3 THEN churn_count+$1 ELSE $1 END WHERE singleton AND used_entries>=$1 AND used_bytes>=$2 AND (churn_window<>$3 OR churn_count+$1<=max_churn_per_second)`, count, bytes, window)
	if err != nil {
		return 0, err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return 0, ErrInvalidTransition
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return count, nil
}

func (l *SQLLedger) TombstoneUsage(ctx context.Context) (entries int, bytes int64, err error) {
	err = l.db.QueryRowContext(ctx, `SELECT used_entries,used_bytes FROM b5_s3_tombstone_budget WHERE singleton`).Scan(&entries, &bytes)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrAdmissionDenied
	}
	return
}

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/jackc/pgx/v5/pgconn"
	modernsqlite "modernc.org/sqlite"
)

const (
	defaultOutboxLease = 2 * time.Minute
	maxOutboxErrorText = 512
)

var (
	ErrOutboxEventAlreadyExists     = errors.New("management audit outbox event already exists")
	ErrUnsafeManagementAuditDetails = errors.New("management audit details contain prohibited secret material")
	unsafeDetailsPattern            = regexp.MustCompile(`(?i)key_b64|key_file|postgres(?:ql)?://|mysql://|password|passwd|secret|token|dsn`)
	unsafeErrorPattern              = regexp.MustCompile(`(?i)(postgres(?:ql)?|mysql|https?)://[^\s]+|(?:key_b64|key_file|password|passwd|secret|token|dsn)\s*[:=]\s*[^\s,;]+|\b[A-Za-z0-9_+/=-]{24,}\b`)
)

// ManagementAuditOutboxRepository persists and leases management audit deliveries.
type ManagementAuditOutboxRepository struct {
	repositoryBase
}

// Append writes an event in the caller's metadata transaction.
func (repository *ManagementAuditOutboxRepository) Append(ctx context.Context, tx *sql.Tx, event model.ManagementAuditOutbox) error {
	if err := repository.validate(ctx, "append"); err != nil {
		return err
	}
	if tx == nil {
		return fmt.Errorf("append management audit outbox: transaction is required")
	}
	if strings.TrimSpace(event.EventUUID) == "" || strings.TrimSpace(event.Action) == "" || strings.TrimSpace(event.ActorType) == "" || strings.TrimSpace(event.ActorID) == "" {
		return fmt.Errorf("append management audit outbox: event identity and actor fields are required")
	}
	if unsafeDetailsPattern.MatchString(event.DetailsJSON) {
		return fmt.Errorf("append management audit outbox: %w", ErrUnsafeManagementAuditDetails)
	}
	now := time.Now().UTC()
	createdAt := event.CreatedAt.UTC()
	if event.CreatedAt.IsZero() {
		createdAt = now
	}
	_, err := tx.ExecContext(ctx, repository.bind(`INSERT INTO management_audit_outbox (event_uuid,action,actor_type,actor_id,details_json,created_at,attempts,claimed_by,claimed_at,last_error,next_attempt_at,delivered_at) VALUES (?,?,?,?,?,?,0,NULL,NULL,NULL,?,NULL)`), event.EventUUID, event.Action, event.ActorType, event.ActorID, event.DetailsJSON, createdAt, now)
	if isNamedUniqueViolation(err, "management_audit_outbox_pkey", "management_audit_outbox.event_uuid") {
		return fmt.Errorf("append management audit outbox: %w", ErrOutboxEventAlreadyExists)
	}
	if err != nil {
		return fmt.Errorf("append management audit outbox: %w", err)
	}
	return nil
}

// ClaimBatch leases the earliest currently eligible events.
func (repository *ManagementAuditOutboxRepository) ClaimBatch(ctx context.Context, workerID string, batchLimit int, lease time.Duration) (events []model.ManagementAuditOutbox, err error) {
	if validateErr := repository.validate(ctx, "claim"); validateErr != nil {
		return nil, validateErr
	}
	if strings.TrimSpace(workerID) == "" {
		return nil, fmt.Errorf("claim management audit outbox: worker ID is required")
	}
	if batchLimit < 1 || batchLimit > 1000 {
		return nil, fmt.Errorf("claim management audit outbox: batch limit must be between 1 and 1000")
	}
	if lease <= 0 {
		lease = defaultOutboxLease
	}
	tx, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("claim management audit outbox: begin transaction: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	now := time.Now().UTC()
	query := `SELECT event_uuid,action,actor_type,actor_id,details_json,created_at,attempts,claimed_by,claimed_at,last_error,next_attempt_at,delivered_at FROM management_audit_outbox WHERE delivered_at IS NULL AND next_attempt_at<=? ORDER BY next_attempt_at,event_uuid LIMIT ?`
	if repository.dialect == DialectPostgres {
		query += ` FOR UPDATE SKIP LOCKED`
	}
	rows, err := tx.QueryContext(ctx, repository.bind(query), now, batchLimit)
	if err != nil {
		return nil, fmt.Errorf("claim management audit outbox: select batch: %w", err)
	}
	for rows.Next() {
		event, scanErr := scanManagementAuditOutbox(rows)
		if scanErr != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("claim management audit outbox: %w", scanErr)
		}
		events = append(events, event)
	}
	iterationErr, closeErr := rows.Err(), rows.Close()
	if iterationErr != nil || closeErr != nil {
		return nil, fmt.Errorf("claim management audit outbox: finish batch: %w", errors.Join(iterationErr, closeErr))
	}
	leaseUntil := now.Add(lease)
	for index := range events {
		result, updateErr := tx.ExecContext(ctx, repository.bind(`UPDATE management_audit_outbox SET claimed_by=?,claimed_at=?,next_attempt_at=? WHERE event_uuid=? AND delivered_at IS NULL`), workerID, now, leaseUntil, events[index].EventUUID)
		if updateErr != nil {
			return nil, fmt.Errorf("claim management audit outbox: lease event: %w", updateErr)
		}
		if updateErr = expectOneAffected(result, nil); updateErr != nil {
			return nil, fmt.Errorf("claim management audit outbox: lease event: %w", updateErr)
		}
		events[index].ClaimedBy = workerID
		events[index].ClaimedAt = timePointer(now)
		events[index].NextAttemptAt = leaseUntil
	}
	if err = tx.Commit(); err != nil {
		return nil, fmt.Errorf("claim management audit outbox: commit transaction: %w", err)
	}
	return events, nil
}

// MarkDelivered marks one event delivered.
func (repository *ManagementAuditOutboxRepository) MarkDelivered(ctx context.Context, eventUUID string) (err error) {
	if validateErr := repository.validate(ctx, "mark delivered"); validateErr != nil {
		return validateErr
	}
	if strings.TrimSpace(eventUUID) == "" {
		return fmt.Errorf("mark management audit delivered: event UUID is required")
	}
	tx, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("mark management audit delivered: begin transaction: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	result, err := tx.ExecContext(ctx, repository.bind(`UPDATE management_audit_outbox SET delivered_at=CURRENT_TIMESTAMP WHERE event_uuid=? AND delivered_at IS NULL`), eventUUID)
	if err != nil {
		return fmt.Errorf("mark management audit delivered: %w", err)
	}
	if err = expectOneAffected(result, nil); err != nil {
		return fmt.Errorf("mark management audit delivered: %w", ErrNotFound)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("mark management audit delivered: commit transaction: %w", err)
	}
	return nil
}

// MarkFailed releases a claim and schedules its next delivery attempt.
func (repository *ManagementAuditOutboxRepository) MarkFailed(ctx context.Context, eventUUID, lastErr string, backoff time.Duration) error {
	if err := repository.validate(ctx, "mark failed"); err != nil {
		return err
	}
	if strings.TrimSpace(eventUUID) == "" {
		return fmt.Errorf("mark management audit failed: event UUID is required")
	}
	if backoff < 0 {
		return fmt.Errorf("mark management audit failed: backoff must not be negative")
	}
	safeError := sanitizeOutboxError(lastErr)
	nextAttempt := time.Now().UTC().Add(backoff)
	result, err := repository.db.ExecContext(ctx, repository.bind(`UPDATE management_audit_outbox SET attempts=attempts+1,last_error=?,next_attempt_at=?,claimed_by=NULL,claimed_at=NULL WHERE event_uuid=? AND delivered_at IS NULL`), optionalText(safeError), nextAttempt, eventUUID)
	if err != nil {
		return fmt.Errorf("mark management audit failed: %w", err)
	}
	if err := expectOneAffected(result, nil); err != nil {
		return fmt.Errorf("mark management audit failed: %w", ErrNotFound)
	}
	return nil
}

// PendingStats returns pending count and age of the oldest event.
func (repository *ManagementAuditOutboxRepository) PendingStats(ctx context.Context) (count int, oldestAge time.Duration, err error) {
	if err := repository.validate(ctx, "read stats"); err != nil {
		return 0, 0, err
	}
	var oldest databaseTimestamp
	if err := repository.db.QueryRowContext(ctx, `SELECT COUNT(*),MIN(created_at) FROM management_audit_outbox WHERE delivered_at IS NULL`).Scan(&count, &oldest); err != nil {
		return 0, 0, fmt.Errorf("read management audit outbox stats: %w", err)
	}
	if count > 0 && oldest.valid {
		oldestAge = time.Since(oldest.time)
		if oldestAge < 0 {
			oldestAge = 0
		}
	}
	return count, oldestAge, nil
}

// Delivered reports whether one outbox event has been durably acknowledged.
func (repository *ManagementAuditOutboxRepository) Delivered(ctx context.Context, eventUUID string) (bool, error) {
	if err := repository.validate(ctx, "read delivery status"); err != nil {
		return false, err
	}
	if strings.TrimSpace(eventUUID) == "" {
		return false, fmt.Errorf("read management audit delivery status: event UUID is required")
	}
	var deliveredAt databaseTimestamp
	if err := repository.db.QueryRowContext(ctx, repository.bind(`SELECT delivered_at FROM management_audit_outbox WHERE event_uuid=?`), eventUUID).Scan(&deliveredAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, fmt.Errorf("read management audit delivery status: %w", ErrNotFound)
		}
		return false, fmt.Errorf("read management audit delivery status: %w", err)
	}
	return deliveredAt.valid, nil
}

func scanManagementAuditOutbox(scanner rowScanner) (model.ManagementAuditOutbox, error) {
	var event model.ManagementAuditOutbox
	var claimedBy, lastError sql.NullString
	var createdAt, claimedAt, nextAttemptAt, deliveredAt databaseTimestamp
	if err := scanner.Scan(&event.EventUUID, &event.Action, &event.ActorType, &event.ActorID, &event.DetailsJSON, &createdAt, &event.Attempts, &claimedBy, &claimedAt, &lastError, &nextAttemptAt, &deliveredAt); err != nil {
		return event, err
	}
	event.ClaimedBy, event.LastError = claimedBy.String, lastError.String
	var err error
	if event.CreatedAt, err = createdAt.required("management_audit_outbox.created_at"); err != nil {
		return model.ManagementAuditOutbox{}, err
	}
	if event.NextAttemptAt, err = nextAttemptAt.required("management_audit_outbox.next_attempt_at"); err != nil {
		return model.ManagementAuditOutbox{}, err
	}
	event.ClaimedAt, event.DeliveredAt = claimedAt.pointer(), deliveredAt.pointer()
	return event, nil
}

func (repository *ManagementAuditOutboxRepository) validate(ctx context.Context, operation string) error {
	if ctx == nil {
		return fmt.Errorf("%s management audit outbox: %w", operation, ErrNilContext)
	}
	if repository == nil || repository.db == nil {
		return fmt.Errorf("%s management audit outbox: repository is not initialized", operation)
	}
	return nil
}

func sanitizeOutboxError(message string) string {
	message = strings.TrimSpace(unsafeErrorPattern.ReplaceAllString(message, "[redacted]"))
	if len(message) > maxOutboxErrorText {
		message = message[:maxOutboxErrorText]
	}
	return message
}

func timePointer(value time.Time) *time.Time {
	return &value
}

func isNamedUniqueViolation(err error, postgresConstraint, sqliteColumn string) bool {
	if err == nil {
		return false
	}
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) {
		return postgresError.Code == "23505" && (postgresConstraint == "" || postgresError.ConstraintName == postgresConstraint)
	}
	var sqliteError *modernsqlite.Error
	if errors.As(err, &sqliteError) {
		return sqliteError.Code()&0xff == 19 && strings.Contains(strings.ToLower(sqliteError.Error()), strings.ToLower(sqliteColumn))
	}
	return false
}

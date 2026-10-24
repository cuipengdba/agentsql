package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// NotificationDeliveryRepository coordinates at-least-once webhook workers
// through the metadata database. Audit records remain in the audit database.
type NotificationDeliveryRepository struct{ repositoryBase }

type NotificationDeliveryClaim struct {
	LastSequence int64
	Failures     int
	Token        string
}

type NotificationDeliveryStatus struct {
	LastSequence int64
	Failures     int
	DeadLetter   bool
	RetryAfter   time.Time
	LastError    string
}

// Ensure creates a channel cursor at the supplied committed stream tail.
// Existing cursors are never silently reset or rebound to another config.
func (repository *NotificationDeliveryRepository) Ensure(ctx context.Context, channelID, configDigest string, initialSequence int64) error {
	tenantID, err := repository.requireTenant(ctx, "ensure notification delivery")
	if err != nil {
		return err
	}
	if !validNotificationDeliveryIdentity(channelID, configDigest) || initialSequence < 0 {
		return errors.New("ensure notification delivery: invalid identity or cursor")
	}
	_, err = repository.db.ExecContext(ctx, repository.bind(`
INSERT INTO notification_delivery_cursors(tenant_id,channel_id,last_seq,config_digest)
VALUES(?,?,?,?)
ON CONFLICT(tenant_id,channel_id) DO NOTHING`), tenantID, channelID, initialSequence, configDigest)
	if err != nil {
		return fmt.Errorf("ensure notification delivery cursor: %w", err)
	}
	var existing string
	if err := repository.db.QueryRowContext(ctx, repository.bind(`
SELECT config_digest FROM notification_delivery_cursors WHERE tenant_id=? AND channel_id=?`), tenantID, channelID).Scan(&existing); err != nil {
		return fmt.Errorf("read notification delivery configuration: %w", err)
	}
	if existing != configDigest {
		return errors.New("notification delivery configuration changed; resolve pending delivery before rebinding channel")
	}
	return nil
}

// Claim leases the next sequence to at most one live worker at a time.
// A lease expiry may duplicate a remote success; the delivery ID must be
// deduplicated by the receiver. A dead-letter cursor is never claimed.
func (repository *NotificationDeliveryRepository) Claim(ctx context.Context, channelID string, now time.Time, lease time.Duration) (*NotificationDeliveryClaim, error) {
	tenantID, err := repository.requireTenant(ctx, "claim notification delivery")
	if err != nil {
		return nil, err
	}
	if channelID == "" || now.IsZero() || lease < time.Second || lease > 10*time.Minute {
		return nil, errors.New("claim notification delivery: invalid lease")
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return nil, fmt.Errorf("generate notification delivery token: %w", err)
	}
	token := hex.EncodeToString(random)
	nowMS := now.UTC().UnixMilli()
	claim := &NotificationDeliveryClaim{Token: token}
	err = repository.db.QueryRowContext(ctx, repository.bind(`
UPDATE notification_delivery_cursors
SET lease_token=?, lease_until_ms=?, updated_at=CURRENT_TIMESTAMP
WHERE tenant_id=? AND channel_id=? AND dead_letter=?
  AND retry_after_ms<=? AND (lease_token IS NULL OR lease_until_ms<=?)
RETURNING last_seq,failures`), token, now.Add(lease).UTC().UnixMilli(),
		tenantID, channelID, false, nowMS, nowMS).Scan(&claim.LastSequence, &claim.Failures)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("claim notification delivery cursor: %w", err)
	}
	return claim, nil
}

// Acknowledge advances exactly one committed sequence while the lease is
// still owned. A stale worker cannot advance or skip the cursor.
func (repository *NotificationDeliveryRepository) Acknowledge(ctx context.Context, channelID string, claim NotificationDeliveryClaim, sequence int64, now time.Time) (bool, error) {
	if sequence < 1 || sequence != claim.LastSequence+1 || claim.Token == "" || now.IsZero() {
		return false, errors.New("acknowledge notification delivery: invalid claim or sequence")
	}
	return repository.updateOwned(ctx, "acknowledge notification delivery", channelID, claim, now, `
UPDATE notification_delivery_cursors
SET last_seq=?, lease_token=NULL, lease_until_ms=0, failures=0,
    retry_after_ms=0, last_error_reason='', updated_at=CURRENT_TIMESTAMP
WHERE tenant_id=? AND channel_id=? AND last_seq=? AND lease_token=? AND lease_until_ms>?`,
		sequence)
}

// Release returns an empty-stream lease without moving the cursor.
func (repository *NotificationDeliveryRepository) Release(ctx context.Context, channelID string, claim NotificationDeliveryClaim, now time.Time) (bool, error) {
	if claim.Token == "" || now.IsZero() {
		return false, errors.New("release notification delivery: invalid claim")
	}
	return repository.updateOwned(ctx, "release notification delivery", channelID, claim, now, `
UPDATE notification_delivery_cursors
SET lease_token=NULL, lease_until_ms=0, updated_at=CURRENT_TIMESTAMP
WHERE tenant_id=? AND channel_id=? AND last_seq=? AND lease_token=? AND lease_until_ms>?`, 0)
}

// Fail records a bounded, non-sensitive reason and either schedules the next
// attempt or enters dead-letter state. It does not consume the audit event.
func (repository *NotificationDeliveryRepository) Fail(ctx context.Context, channelID string, claim NotificationDeliveryClaim, reason string, now, retryAfter time.Time, maxFailures int) (bool, error) {
	if claim.Token == "" || now.IsZero() || retryAfter.Before(now) || maxFailures < 1 || maxFailures > 100 ||
		!safeNotificationFailureReason(reason) {
		return false, errors.New("fail notification delivery: invalid failure state")
	}
	tenantID, err := repository.requireTenant(ctx, "fail notification delivery")
	if err != nil {
		return false, err
	}
	result, err := repository.db.ExecContext(ctx, repository.bind(`
UPDATE notification_delivery_cursors
SET lease_token=NULL, lease_until_ms=0, failures=failures+1,
    retry_after_ms=?, dead_letter=CASE WHEN failures+1>=? THEN ? ELSE ? END,
    last_error_reason=?, updated_at=CURRENT_TIMESTAMP
WHERE tenant_id=? AND channel_id=? AND last_seq=? AND lease_token=? AND lease_until_ms>?`),
		retryAfter.UTC().UnixMilli(), maxFailures, true, false, reason,
		tenantID, channelID, claim.LastSequence, claim.Token, now.UTC().UnixMilli())
	if err != nil {
		return false, fmt.Errorf("fail notification delivery cursor: %w", err)
	}
	return changedRow(result)
}

// ReplayDeadLetter allows an audited administrator action to retry the same
// pending event. The caller is responsible for authorization and audit.
func (repository *NotificationDeliveryRepository) ReplayDeadLetter(ctx context.Context, channelID string) (bool, error) {
	tenantID, err := repository.requireTenant(ctx, "replay notification dead letter")
	if err != nil {
		return false, err
	}
	result, err := repository.db.ExecContext(ctx, repository.bind(`
UPDATE notification_delivery_cursors
SET dead_letter=?, failures=0, retry_after_ms=0, last_error_reason='',
    lease_token=NULL, lease_until_ms=0, updated_at=CURRENT_TIMESTAMP
WHERE tenant_id=? AND channel_id=? AND dead_letter=?`), false, tenantID, channelID, true)
	if err != nil {
		return false, fmt.Errorf("replay notification dead letter: %w", err)
	}
	return changedRow(result)
}

func (repository *NotificationDeliveryRepository) Status(ctx context.Context, channelID string) (NotificationDeliveryStatus, error) {
	tenantID, err := repository.requireTenant(ctx, "read notification delivery status")
	if err != nil {
		return NotificationDeliveryStatus{}, err
	}
	var status NotificationDeliveryStatus
	var dead bool
	var retryMS int64
	err = repository.db.QueryRowContext(ctx, repository.bind(`
SELECT last_seq,failures,dead_letter,retry_after_ms,last_error_reason
FROM notification_delivery_cursors WHERE tenant_id=? AND channel_id=?`), tenantID, channelID).
		Scan(&status.LastSequence, &status.Failures, &dead, &retryMS, &status.LastError)
	if err != nil {
		return NotificationDeliveryStatus{}, fmt.Errorf("read notification delivery status: %w", err)
	}
	status.DeadLetter = dead
	if retryMS > 0 {
		status.RetryAfter = time.UnixMilli(retryMS).UTC()
	}
	return status, nil
}

func (repository *NotificationDeliveryRepository) updateOwned(ctx context.Context, operation, channelID string, claim NotificationDeliveryClaim, now time.Time, query string, sequence int64) (bool, error) {
	tenantID, err := repository.requireTenant(ctx, operation)
	if err != nil {
		return false, err
	}
	args := []any{tenantID, channelID, claim.LastSequence, claim.Token, now.UTC().UnixMilli()}
	if sequence > 0 {
		args = append([]any{sequence}, args...)
	}
	result, err := repository.db.ExecContext(ctx, repository.bind(query), args...)
	if err != nil {
		return false, fmt.Errorf("%s cursor: %w", operation, err)
	}
	return changedRow(result)
}

func changedRow(result sql.Result) (bool, error) {
	rows, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return rows == 1, nil
}

func validNotificationDeliveryIdentity(channelID, digest string) bool {
	if channelID == "" || len(channelID) > 128 || len(digest) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(digest)
	return err == nil && len(decoded) == 32 && strings.ToLower(digest) == digest
}

func safeNotificationFailureReason(reason string) bool {
	switch reason {
	case "timeout", "ssrf_blocked", "connect_error", "http_status", "canceled", "send_error", "panic", "unknown":
		return true
	default:
		return false
	}
}

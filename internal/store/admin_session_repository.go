package store

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	// ErrAdminRefreshTokenInvalid is returned for an unknown, expired, or
	// otherwise unusable refresh token.
	ErrAdminRefreshTokenInvalid = errors.New("invalid admin refresh token")
	// ErrAdminRefreshTokenReplay identifies reuse of a rotated or revoked
	// refresh token. The repository revokes the whole refresh family before
	// returning this error.
	ErrAdminRefreshTokenReplay = errors.New("admin refresh token replay detected")
)

// AdminRefreshSession is the server-side identity bound to a refresh family.
// TokenHash is a SHA-256 digest encoded as lowercase hexadecimal; plaintext
// refresh tokens never cross into the store package.
type AdminRefreshSession struct {
	FamilyID  string
	TokenHash string
	TenantID  string
	UserID    string
	Username  string
	CreatedAt time.Time
	ExpiresAt time.Time
}

// AdminSessionRepository persists administrator access-token revocations and
// rotating refresh-token families in the metadata database.
type AdminSessionRepository struct{ repositoryBase }

// CreateRefreshSession creates one refresh family and its initial token. It
// also lazily removes records whose own expiry can no longer affect security.
func (repository *AdminSessionRepository) CreateRefreshSession(ctx context.Context, session AdminRefreshSession) error {
	if err := validateAdminRefreshSession(ctx, session); err != nil {
		return err
	}
	tx, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("create admin refresh session: begin transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if _, err := tx.ExecContext(ctx, repository.bind(`DELETE FROM admin_access_revocations WHERE expires_at<=?`), session.CreatedAt); err != nil {
		return fmt.Errorf("create admin refresh session: clean access revocations: %w", err)
	}
	if _, err := tx.ExecContext(ctx, repository.bind(`DELETE FROM admin_refresh_families WHERE expires_at<=?`), session.CreatedAt); err != nil {
		return fmt.Errorf("create admin refresh session: clean refresh families: %w", err)
	}
	if _, err := tx.ExecContext(ctx, repository.bind(`
INSERT INTO admin_refresh_families(family_id,tenant_id,user_id,username,expires_at,created_at)
VALUES(?,?,?,?,?,?)`), session.FamilyID, session.TenantID, session.UserID, session.Username, session.ExpiresAt, session.CreatedAt); err != nil {
		return fmt.Errorf("create admin refresh session: insert family: %w", err)
	}
	if _, err := tx.ExecContext(ctx, repository.bind(`
INSERT INTO admin_refresh_tokens(token_hash,family_id,expires_at,state,created_at)
VALUES(?,?,?,'active',?)`), session.TokenHash, session.FamilyID, session.ExpiresAt, session.CreatedAt); err != nil {
		return fmt.Errorf("create admin refresh session: insert token: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("create admin refresh session: commit: %w", err)
	}
	committed = true
	return nil
}

// ValidateAccessToken checks both its jti revocation record and, for tokens
// issued with refresh support, the persisted refresh-family state.
func (repository *AdminSessionRepository) ValidateAccessToken(ctx context.Context, jti, familyID string, now time.Time) (bool, error) {
	if ctx == nil || strings.TrimSpace(jti) == "" || now.IsZero() {
		return false, errors.New("validate admin access token: context, jti, and time are required")
	}
	var revoked int
	if err := repository.db.QueryRowContext(ctx, repository.bind(`
SELECT COUNT(*) FROM admin_access_revocations WHERE jti=? AND expires_at>?`), jti, now).Scan(&revoked); err != nil {
		return false, fmt.Errorf("validate admin access token: read revocation: %w", err)
	}
	if revoked != 0 {
		return false, nil
	}
	if familyID == "" {
		return true, nil
	}
	if !validLowerHex(familyID, 16) {
		return false, nil
	}
	var expires, revokedAt databaseTimestamp
	err := repository.db.QueryRowContext(ctx, repository.bind(`
SELECT expires_at,revoked_at FROM admin_refresh_families WHERE family_id=?`), familyID).Scan(&expires, &revokedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("validate admin access token: read refresh family: %w", err)
	}
	expiresAt, err := expires.required("admin_refresh_families.expires_at")
	if err != nil {
		return false, fmt.Errorf("validate admin access token: %w", err)
	}
	return revokedAt.pointer() == nil && now.Before(expiresAt), nil
}

// RotateRefreshToken consumes oldHash and inserts newHash atomically. Reuse of
// a consumed token revokes every token in the family before returning.
func (repository *AdminSessionRepository) RotateRefreshToken(ctx context.Context, oldHash, newHash string, now time.Time) (AdminRefreshSession, error) {
	if ctx == nil || !validLowerHex(oldHash, 32) || !validLowerHex(newHash, 32) || oldHash == newHash || now.IsZero() {
		return AdminRefreshSession{}, ErrAdminRefreshTokenInvalid
	}
	tx, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return AdminRefreshSession{}, fmt.Errorf("rotate admin refresh token: begin transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	result, err := tx.ExecContext(ctx, repository.bind(`
UPDATE admin_refresh_tokens
SET state='rotated',replaced_by_hash=?,used_at=?
WHERE token_hash=? AND state='active' AND expires_at>?
  AND EXISTS (
    SELECT 1 FROM admin_refresh_families f
    WHERE f.family_id=admin_refresh_tokens.family_id AND f.revoked_at IS NULL AND f.expires_at>?
  )`), newHash, now, oldHash, now, now)
	if err != nil {
		return AdminRefreshSession{}, fmt.Errorf("rotate admin refresh token: consume token: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return AdminRefreshSession{}, fmt.Errorf("rotate admin refresh token: read update result: %w", err)
	}
	if affected != 1 {
		return repository.rejectRefreshToken(ctx, tx, oldHash, now, affected, &committed)
	}

	var session AdminRefreshSession
	var tokenExpires, familyExpires databaseTimestamp
	err = tx.QueryRowContext(ctx, repository.bind(`
SELECT t.family_id,f.tenant_id,f.user_id,f.username,t.expires_at,f.expires_at
FROM admin_refresh_tokens t
JOIN admin_refresh_families f ON f.family_id=t.family_id
WHERE t.token_hash=?`), oldHash).Scan(&session.FamilyID, &session.TenantID, &session.UserID, &session.Username, &tokenExpires, &familyExpires)
	if err != nil {
		return AdminRefreshSession{}, fmt.Errorf("rotate admin refresh token: read family: %w", err)
	}
	tokenExpiry, err := tokenExpires.required("admin_refresh_tokens.expires_at")
	if err != nil {
		return AdminRefreshSession{}, fmt.Errorf("rotate admin refresh token: %w", err)
	}
	familyExpiry, err := familyExpires.required("admin_refresh_families.expires_at")
	if err != nil || !tokenExpiry.Equal(familyExpiry) || !now.Before(familyExpiry) {
		return AdminRefreshSession{}, ErrAdminRefreshTokenInvalid
	}
	session.TokenHash, session.CreatedAt, session.ExpiresAt = newHash, now, familyExpiry
	if _, err := tx.ExecContext(ctx, repository.bind(`
INSERT INTO admin_refresh_tokens(token_hash,family_id,expires_at,state,created_at)
VALUES(?,?,?,'active',?)`), newHash, session.FamilyID, session.ExpiresAt, now); err != nil {
		return AdminRefreshSession{}, fmt.Errorf("rotate admin refresh token: insert replacement: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return AdminRefreshSession{}, fmt.Errorf("rotate admin refresh token: commit: %w", err)
	}
	committed = true
	return session, nil
}

func (repository *AdminSessionRepository) rejectRefreshToken(ctx context.Context, tx *sql.Tx, tokenHash string, now time.Time, affected int64, committed *bool) (AdminRefreshSession, error) {
	if affected != 0 {
		return AdminRefreshSession{}, fmt.Errorf("rotate admin refresh token: conditional update affected %d rows", affected)
	}
	var familyID, state string
	var tokenExpires, familyExpires, familyRevoked databaseTimestamp
	err := tx.QueryRowContext(ctx, repository.bind(`
SELECT t.family_id,t.state,t.expires_at,f.expires_at,f.revoked_at
FROM admin_refresh_tokens t
JOIN admin_refresh_families f ON f.family_id=t.family_id
WHERE t.token_hash=?`), tokenHash).Scan(&familyID, &state, &tokenExpires, &familyExpires, &familyRevoked)
	if errors.Is(err, sql.ErrNoRows) {
		return AdminRefreshSession{}, ErrAdminRefreshTokenInvalid
	}
	if err != nil {
		return AdminRefreshSession{}, fmt.Errorf("rotate admin refresh token: inspect rejected token: %w", err)
	}
	replayed := state != "active"
	if err := repository.revokeFamilyTx(ctx, tx, familyID, now); err != nil {
		return AdminRefreshSession{}, err
	}
	if err := tx.Commit(); err != nil {
		return AdminRefreshSession{}, fmt.Errorf("rotate admin refresh token: commit family revocation: %w", err)
	}
	*committed = true
	if replayed {
		return AdminRefreshSession{}, ErrAdminRefreshTokenReplay
	}
	return AdminRefreshSession{}, ErrAdminRefreshTokenInvalid
}

// RevokeAccessAndRefreshFamily atomically records an access jti revocation and
// revokes its associated refresh family. Legacy access tokens without a family
// still receive a durable jti revocation.
func (repository *AdminSessionRepository) RevokeAccessAndRefreshFamily(ctx context.Context, jti, familyID string, accessExpires, now time.Time) error {
	if ctx == nil || strings.TrimSpace(jti) == "" || !now.Before(accessExpires) {
		return errors.New("revoke admin access token: context, jti, and future expiry are required")
	}
	if familyID != "" && !validLowerHex(familyID, 16) {
		return errors.New("revoke admin access token: invalid refresh family")
	}
	tx, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("revoke admin access token: begin transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if _, err := tx.ExecContext(ctx, repository.bind(`
INSERT INTO admin_access_revocations(jti,expires_at,revoked_at) VALUES(?,?,?)
ON CONFLICT(jti) DO UPDATE SET expires_at=excluded.expires_at,revoked_at=excluded.revoked_at`), jti, accessExpires, now); err != nil {
		return fmt.Errorf("revoke admin access token: persist revocation: %w", err)
	}
	if familyID != "" {
		result, err := tx.ExecContext(ctx, repository.bind(`
UPDATE admin_refresh_families SET revoked_at=COALESCE(revoked_at,?) WHERE family_id=?`), now, familyID)
		if err != nil {
			return fmt.Errorf("revoke admin access token: revoke refresh family: %w", err)
		}
		affected, err := result.RowsAffected()
		if err != nil || affected != 1 {
			return fmt.Errorf("revoke admin access token: refresh family is unavailable")
		}
		if _, err := tx.ExecContext(ctx, repository.bind(`
UPDATE admin_refresh_tokens SET state='revoked',revoked_at=COALESCE(revoked_at,?) WHERE family_id=?`), now, familyID); err != nil {
			return fmt.Errorf("revoke admin access token: revoke refresh tokens: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("revoke admin access token: commit: %w", err)
	}
	committed = true
	return nil
}

// RevokeRefreshFamily is used when identity revalidation fails after an
// otherwise successful rotation. No replacement token is returned to the
// caller unless this fail-closed cleanup succeeds.
func (repository *AdminSessionRepository) RevokeRefreshFamily(ctx context.Context, familyID string, now time.Time) error {
	if ctx == nil || !validLowerHex(familyID, 16) || now.IsZero() {
		return errors.New("revoke admin refresh family: invalid input")
	}
	tx, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("revoke admin refresh family: begin transaction: %w", err)
	}
	if err := repository.revokeFamilyTx(ctx, tx, familyID, now); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("revoke admin refresh family: commit: %w", err)
	}
	return nil
}

func (repository *AdminSessionRepository) revokeFamilyTx(ctx context.Context, tx *sql.Tx, familyID string, now time.Time) error {
	if _, err := tx.ExecContext(ctx, repository.bind(`
UPDATE admin_refresh_families SET revoked_at=COALESCE(revoked_at,?) WHERE family_id=?`), now, familyID); err != nil {
		return fmt.Errorf("revoke admin refresh family: update family: %w", err)
	}
	if _, err := tx.ExecContext(ctx, repository.bind(`
UPDATE admin_refresh_tokens SET state='revoked',revoked_at=COALESCE(revoked_at,?) WHERE family_id=?`), now, familyID); err != nil {
		return fmt.Errorf("revoke admin refresh family: update tokens: %w", err)
	}
	return nil
}

func validateAdminRefreshSession(ctx context.Context, session AdminRefreshSession) error {
	if ctx == nil || !validLowerHex(session.FamilyID, 16) || !validLowerHex(session.TokenHash, 32) ||
		strings.TrimSpace(session.TenantID) == "" || strings.TrimSpace(session.UserID) == "" || strings.TrimSpace(session.Username) == "" ||
		session.CreatedAt.IsZero() || !session.CreatedAt.Before(session.ExpiresAt) {
		return errors.New("create admin refresh session: invalid input")
	}
	return nil
}

func validLowerHex(value string, byteLength int) bool {
	if len(value) != byteLength*2 || strings.ToLower(value) != value {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == byteLength
}

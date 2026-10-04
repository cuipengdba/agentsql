package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
)

type HumanAuthRepository struct{ repositoryBase }

type MFARecord struct {
	TenantID         string
	UserID           string
	SecretCiphertext string
	Status           string
	LastCounter      int64
}

type LoginChallenge struct {
	Hash      string
	TenantID  string
	UserID    string
	Username  string
	ExpiresAt time.Time
}

type OIDCAuthRequest struct {
	StateHash    string
	Nonce        string
	PKCEVerifier string
	ReturnTo     string
	ExpiresAt    time.Time
}

func (repository *HumanAuthRepository) PutPendingMFA(ctx context.Context, record MFARecord, recoveryHashes []string) error {
	if ctx == nil || strings.TrimSpace(record.TenantID) == "" || strings.TrimSpace(record.UserID) == "" ||
		strings.TrimSpace(record.SecretCiphertext) == "" || len(recoveryHashes) == 0 {
		return errors.New("put pending MFA: invalid input")
	}
	tx, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("put pending MFA: %w", err)
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, repository.bind(`DELETE FROM user_mfa WHERE tenant_id=? AND user_id=?`), record.TenantID, record.UserID); err != nil {
		return fmt.Errorf("replace pending MFA: %w", err)
	}
	if _, err = tx.ExecContext(ctx, repository.bind(`INSERT INTO user_mfa(tenant_id,user_id,secret_ciphertext,status,last_counter) VALUES(?,?,?,'pending',-1)`), record.TenantID, record.UserID, record.SecretCiphertext); err != nil {
		return fmt.Errorf("insert pending MFA: %w", err)
	}
	for _, hash := range recoveryHashes {
		if !validLowerHex(hash, 32) {
			return errors.New("put pending MFA: invalid recovery hash")
		}
		if _, err = tx.ExecContext(ctx, repository.bind(`INSERT INTO user_mfa_recovery_codes(tenant_id,user_id,code_hash) VALUES(?,?,?)`), record.TenantID, record.UserID, hash); err != nil {
			return fmt.Errorf("insert MFA recovery code: %w", err)
		}
	}
	return tx.Commit()
}

func (repository *HumanAuthRepository) MFA(ctx context.Context, tenantID, userID string) (MFARecord, error) {
	if ctx == nil || tenantID == "" || userID == "" {
		return MFARecord{}, ErrNotFound
	}
	var value MFARecord
	err := repository.db.QueryRowContext(ctx, repository.bind(`SELECT tenant_id,user_id,secret_ciphertext,status,last_counter FROM user_mfa WHERE tenant_id=? AND user_id=?`), tenantID, userID).
		Scan(&value.TenantID, &value.UserID, &value.SecretCiphertext, &value.Status, &value.LastCounter)
	if errors.Is(err, sql.ErrNoRows) {
		return MFARecord{}, ErrNotFound
	}
	if err != nil {
		return MFARecord{}, fmt.Errorf("read MFA: %w", err)
	}
	return value, nil
}

func (repository *HumanAuthRepository) EnableMFA(ctx context.Context, tenantID, userID string, counter int64) error {
	result, err := repository.db.ExecContext(ctx, repository.bind(`UPDATE user_mfa SET status='enabled',last_counter=?,updated_at=CURRENT_TIMESTAMP WHERE tenant_id=? AND user_id=? AND status='pending'`), counter, tenantID, userID)
	if err != nil {
		return fmt.Errorf("enable MFA: %w", err)
	}
	return checkExactlyOne(result, "enable MFA")
}

func (repository *HumanAuthRepository) AdvanceMFACounter(ctx context.Context, tenantID, userID string, counter int64) error {
	result, err := repository.db.ExecContext(ctx, repository.bind(`UPDATE user_mfa SET last_counter=?,updated_at=CURRENT_TIMESTAMP WHERE tenant_id=? AND user_id=? AND status='enabled' AND last_counter<?`), counter, tenantID, userID, counter)
	if err != nil {
		return fmt.Errorf("advance MFA counter: %w", err)
	}
	return checkExactlyOne(result, "advance MFA counter")
}

func (repository *HumanAuthRepository) ConsumeRecoveryCode(ctx context.Context, tenantID, userID, codeHash string, now time.Time) error {
	result, err := repository.db.ExecContext(ctx, repository.bind(`UPDATE user_mfa_recovery_codes SET used_at=? WHERE tenant_id=? AND user_id=? AND code_hash=? AND used_at IS NULL`), now, tenantID, userID, codeHash)
	if err != nil {
		return fmt.Errorf("consume recovery code: %w", err)
	}
	return checkExactlyOne(result, "consume recovery code")
}

func (repository *HumanAuthRepository) DisableMFA(ctx context.Context, tenantID, userID string) error {
	result, err := repository.db.ExecContext(ctx, repository.bind(`DELETE FROM user_mfa WHERE tenant_id=? AND user_id=?`), tenantID, userID)
	if err != nil {
		return fmt.Errorf("disable MFA: %w", err)
	}
	return checkExactlyOne(result, "disable MFA")
}

func (repository *HumanAuthRepository) CreateLoginChallenge(ctx context.Context, value LoginChallenge) error {
	if ctx == nil || !validLowerHex(value.Hash, 32) || value.TenantID == "" || value.UserID == "" || value.Username == "" || value.ExpiresAt.IsZero() {
		return errors.New("create login challenge: invalid input")
	}
	if _, err := repository.db.ExecContext(ctx, repository.bind(`DELETE FROM auth_login_challenges WHERE expires_at<=?`), time.Now().UTC()); err != nil {
		return fmt.Errorf("clean login challenges: %w", err)
	}
	_, err := repository.db.ExecContext(ctx, repository.bind(`INSERT INTO auth_login_challenges(challenge_hash,tenant_id,user_id,username,expires_at) VALUES(?,?,?,?,?)`), value.Hash, value.TenantID, value.UserID, value.Username, value.ExpiresAt)
	if err != nil {
		return fmt.Errorf("create login challenge: %w", err)
	}
	return nil
}

func (repository *HumanAuthRepository) ConsumeLoginChallenge(ctx context.Context, hash string, now time.Time) (LoginChallenge, error) {
	if ctx == nil || !validLowerHex(hash, 32) || now.IsZero() {
		return LoginChallenge{}, ErrNotFound
	}
	tx, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return LoginChallenge{}, err
	}
	defer tx.Rollback()
	var value LoginChallenge
	var expires databaseTimestamp
	err = tx.QueryRowContext(ctx, repository.bind(`SELECT challenge_hash,tenant_id,user_id,username,expires_at FROM auth_login_challenges WHERE challenge_hash=? AND used_at IS NULL AND expires_at>?`), hash, now).
		Scan(&value.Hash, &value.TenantID, &value.UserID, &value.Username, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return LoginChallenge{}, ErrNotFound
	}
	if err != nil {
		return LoginChallenge{}, fmt.Errorf("read login challenge: %w", err)
	}
	value.ExpiresAt, err = expires.required("auth_login_challenges.expires_at")
	if err != nil {
		return LoginChallenge{}, err
	}
	result, err := tx.ExecContext(ctx, repository.bind(`UPDATE auth_login_challenges SET used_at=? WHERE challenge_hash=? AND used_at IS NULL`), now, hash)
	if err != nil {
		return LoginChallenge{}, err
	}
	if err = checkExactlyOne(result, "consume login challenge"); err != nil {
		return LoginChallenge{}, err
	}
	if err = tx.Commit(); err != nil {
		return LoginChallenge{}, err
	}
	return value, nil
}

func (repository *HumanAuthRepository) CreateOIDCRequest(ctx context.Context, value OIDCAuthRequest) error {
	if ctx == nil || !validLowerHex(value.StateHash, 32) || value.Nonce == "" || value.PKCEVerifier == "" || value.ExpiresAt.IsZero() {
		return errors.New("create OIDC request: invalid input")
	}
	if _, err := repository.db.ExecContext(ctx, repository.bind(`DELETE FROM oidc_auth_requests WHERE expires_at<=?`), time.Now().UTC()); err != nil {
		return fmt.Errorf("clean OIDC requests: %w", err)
	}
	_, err := repository.db.ExecContext(ctx, repository.bind(`INSERT INTO oidc_auth_requests(state_hash,nonce,pkce_verifier,return_to,expires_at) VALUES(?,?,?,?,?)`), value.StateHash, value.Nonce, value.PKCEVerifier, value.ReturnTo, value.ExpiresAt)
	if err != nil {
		return fmt.Errorf("create OIDC request: %w", err)
	}
	return nil
}

func (repository *HumanAuthRepository) ConsumeOIDCRequest(ctx context.Context, stateHash string, now time.Time) (OIDCAuthRequest, error) {
	if ctx == nil || !validLowerHex(stateHash, 32) || now.IsZero() {
		return OIDCAuthRequest{}, ErrNotFound
	}
	tx, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return OIDCAuthRequest{}, err
	}
	defer tx.Rollback()
	var value OIDCAuthRequest
	var expires databaseTimestamp
	err = tx.QueryRowContext(ctx, repository.bind(`SELECT state_hash,nonce,pkce_verifier,return_to,expires_at FROM oidc_auth_requests WHERE state_hash=? AND used_at IS NULL AND expires_at>?`), stateHash, now).
		Scan(&value.StateHash, &value.Nonce, &value.PKCEVerifier, &value.ReturnTo, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return OIDCAuthRequest{}, ErrNotFound
	}
	if err != nil {
		return OIDCAuthRequest{}, err
	}
	value.ExpiresAt, err = expires.required("oidc_auth_requests.expires_at")
	if err != nil {
		return OIDCAuthRequest{}, err
	}
	result, err := tx.ExecContext(ctx, repository.bind(`UPDATE oidc_auth_requests SET used_at=? WHERE state_hash=? AND used_at IS NULL`), now, stateHash)
	if err != nil {
		return OIDCAuthRequest{}, err
	}
	if err = checkExactlyOne(result, "consume OIDC request"); err != nil {
		return OIDCAuthRequest{}, err
	}
	if err = tx.Commit(); err != nil {
		return OIDCAuthRequest{}, err
	}
	return value, nil
}

func (repository *HumanAuthRepository) UserByExternalSubject(ctx context.Context, tenantID, provider, subject string) (model.User, error) {
	if ctx == nil || tenantID == "" || (provider != "oidc" && provider != "ldap") || subject == "" {
		return model.User{}, ErrNotFound
	}
	item, err := scanUser(repository.db.QueryRowContext(ctx, repository.bind(`
SELECT u.id,u.tenant_id,u.username,u.display_name,u.password_hash,u.status,u.auth_provider,u.external_subject,u.created_at,u.updated_at
FROM users u JOIN auth_identities i ON i.tenant_id=u.tenant_id AND i.user_id=u.id
WHERE i.tenant_id=? AND i.provider=? AND i.subject=?`), tenantID, provider, subject))
	if errors.Is(err, sql.ErrNoRows) {
		return model.User{}, ErrNotFound
	}
	return item, err
}

func (repository *HumanAuthRepository) ProviderForUser(ctx context.Context, tenantID, userID string) (string, error) {
	if ctx == nil || tenantID == "" || userID == "" {
		return "", ErrNotFound
	}
	var provider string
	err := repository.db.QueryRowContext(ctx, repository.bind(`SELECT provider FROM auth_identities WHERE tenant_id=? AND user_id=?`), tenantID, userID).Scan(&provider)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("read external identity provider: %w", err)
	}
	return provider, nil
}

func (repository *HumanAuthRepository) CreateExternalIdentity(ctx context.Context, user model.User, provider, subject string) (model.User, error) {
	if ctx == nil || user.ID == "" || user.TenantID == "" || user.Username == "" || (provider != "oidc" && provider != "ldap") || subject == "" {
		return model.User{}, errors.New("create external identity: invalid input")
	}
	tx, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return model.User{}, err
	}
	defer tx.Rollback()
	if user.Status == "" {
		user.Status = "active"
	}
	// The legacy auth_provider constraint accepts oidc. auth_identities is the
	// authoritative provider discriminator for both OIDC and LDAP identities.
	_, err = tx.ExecContext(ctx, repository.bind(`INSERT INTO users(id,tenant_id,username,display_name,password_hash,status,auth_provider,external_subject) VALUES(?,?,?,?,?,?,'oidc',?)`), user.ID, user.TenantID, user.Username, user.DisplayName, "!external-login-disabled!", user.Status, subject)
	if err != nil {
		return model.User{}, fmt.Errorf("create external user: %w", err)
	}
	_, err = tx.ExecContext(ctx, repository.bind(`INSERT INTO auth_identities(tenant_id,user_id,provider,subject) VALUES(?,?,?,?)`), user.TenantID, user.ID, provider, subject)
	if err != nil {
		return model.User{}, fmt.Errorf("create external identity: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return model.User{}, err
	}
	return repository.UserByExternalSubject(ctx, user.TenantID, provider, subject)
}

func checkExactlyOne(result sql.Result, operation string) error {
	affected, err := result.RowsAffected()
	if err != nil || affected != 1 {
		return fmt.Errorf("%s: state transition rejected", operation)
	}
	return nil
}

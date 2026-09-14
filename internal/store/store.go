// Package store persists AgentSQL metadata and immutable audit records in SQLite.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	_ "modernc.org/sqlite"
)

var (
	// ErrNotFound indicates that a requested store entity does not exist.
	ErrNotFound = errors.New("store entity not found")
	// ErrInvalidStorePath indicates that the SQLite path is empty.
	ErrInvalidStorePath = errors.New("SQLite path is required")
	// ErrNilContext indicates that a store operation received no context.
	ErrNilContext = errors.New("context is required")
)

// Store owns the SQLite connection and repository encryption state.
type Store struct {
	db     *sql.DB
	cipher *PasswordCipher
}

// Ping verifies that the metadata SQLite connection is reachable.
func (store *Store) Ping(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("ping store: %w", ErrNilContext)
	}
	if store == nil || store.db == nil {
		return fmt.Errorf("ping store: database is unavailable")
	}
	if err := store.db.PingContext(ctx); err != nil {
		return fmt.Errorf("ping store: %w", err)
	}
	return nil
}

// Open connects to SQLite, validates the encryption secret, and applies all
// embedded migrations before returning.
func Open(ctx context.Context, path string) (*Store, error) {
	if err := validateOpenInput(ctx, path); err != nil {
		return nil, err
	}
	passwordCipher, err := NewPasswordCipherFromEnv()
	if err != nil {
		return nil, fmt.Errorf("initialize datasource password encryption: %w", err)
	}
	return openWithCipher(ctx, path, passwordCipher)
}

// OpenWithSecret opens the metadata store with an explicitly injected
// encryption key. It avoids process-global environment mutation in bootstrap.
func OpenWithSecret(ctx context.Context, path string, secret []byte) (*Store, error) {
	if err := validateOpenInput(ctx, path); err != nil {
		return nil, err
	}
	passwordCipher, err := NewPasswordCipher(secret)
	if err != nil {
		return nil, fmt.Errorf("initialize datasource password encryption: %w", err)
	}
	return openWithCipher(ctx, path, passwordCipher)
}

func openWithCipher(ctx context.Context, path string, passwordCipher *PasswordCipher) (*Store, error) {
	database, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open SQLite database %q: %w", path, err)
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)

	if err := database.PingContext(ctx); err != nil {
		return nil, closeDatabaseAfterError(database, fmt.Errorf("ping SQLite database %q: %w", path, err))
	}
	if err := Migrate(ctx, database); err != nil {
		return nil, closeDatabaseAfterError(database, fmt.Errorf("migrate SQLite database %q: %w", path, err))
	}

	return &Store{db: database, cipher: passwordCipher}, nil
}

func validateOpenInput(ctx context.Context, path string) error {
	if ctx == nil {
		return fmt.Errorf("open store: %w", ErrNilContext)
	}
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("open store: %w", ErrInvalidStorePath)
	}
	return nil
}

// Close releases the SQLite connection.
func (store *Store) Close() error {
	if err := store.db.Close(); err != nil {
		return fmt.Errorf("close SQLite database: %w", err)
	}
	return nil
}

// Agents returns the agent repository.
func (store *Store) Agents() *AgentRepository {
	return &AgentRepository{db: store.db}
}

// Datasources returns the datasource repository.
func (store *Store) Datasources() *DatasourceRepository {
	return &DatasourceRepository{db: store.db, cipher: store.cipher}
}

// Policies returns the policy repository.
func (store *Store) Policies() *PolicyRepository {
	return &PolicyRepository{db: store.db}
}

// Rules returns the rule repository.
func (store *Store) Rules() *RuleRepository {
	return &RuleRepository{db: store.db}
}

// MaskRules returns the mask-rule repository.
func (store *Store) MaskRules() *MaskRuleRepository {
	return &MaskRuleRepository{db: store.db}
}

// AuditLogs returns the append-only audit-log repository.
func (store *Store) AuditLogs() *AuditLogRepository {
	return &AuditLogRepository{db: store.db}
}

// Approvals returns the approval repository.
func (store *Store) Approvals() *ApprovalRepository {
	return &ApprovalRepository{db: store.db}
}

// Dashboard returns read-only aggregate queries for the admin dashboard.
func (store *Store) Dashboard() *DashboardRepository {
	return &DashboardRepository{db: store.db}
}

func closeDatabaseAfterError(database *sql.DB, cause error) error {
	if err := database.Close(); err != nil {
		return fmt.Errorf("close SQLite database after failure: %w", errors.Join(cause, err))
	}
	return cause
}

func checkRowsAffected(result sql.Result, entity, id string) error {
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read affected rows for %s %q: %w", entity, id, err)
	}
	if count == 0 {
		return fmt.Errorf("%s %q: %w", entity, id, ErrNotFound)
	}
	return nil
}

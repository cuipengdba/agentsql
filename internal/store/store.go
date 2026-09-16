// Package store persists AgentSQL metadata and immutable audit records.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

var (
	// ErrNotFound indicates that a requested store entity does not exist.
	ErrNotFound = errors.New("store entity not found")
	// ErrInvalidStorePath indicates that the SQLite path is empty.
	ErrInvalidStorePath = errors.New("SQLite path is required")
	// ErrInvalidPostgresDSN indicates that the PostgreSQL DSN is empty.
	ErrInvalidPostgresDSN = errors.New("PostgreSQL DSN is required")
	// ErrInvalidMetadataDriver indicates an unsupported metadata-store driver.
	ErrInvalidMetadataDriver = errors.New("unsupported metadata store driver")
	// ErrNilContext indicates that a store operation received no context.
	ErrNilContext = errors.New("context is required")
)

// MetadataOptions configures a metadata-store connection.
type MetadataOptions struct {
	Driver          Dialect
	SQLitePath      string
	PostgresDSN     string
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
}

// Store owns the metadata connection and repository encryption state.
type Store struct {
	db     *sql.DB
	cipher *PasswordCipher
	driver Dialect
}

// Ping verifies that the metadata connection is reachable.
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
	return openMetadataWithCipher(ctx, MetadataOptions{
		Driver:     DialectSQLite,
		SQLitePath: path,
	}, passwordCipher)
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
	return openMetadataWithCipher(ctx, MetadataOptions{
		Driver:     DialectSQLite,
		SQLitePath: path,
	}, passwordCipher)
}

// OpenMetadata opens, verifies, and migrates the configured metadata store.
func OpenMetadata(ctx context.Context, options MetadataOptions, secret []byte) (*Store, error) {
	if err := validateMetadataOptions(ctx, options); err != nil {
		return nil, err
	}
	passwordCipher, err := NewPasswordCipher(secret)
	if err != nil {
		return nil, fmt.Errorf("initialize datasource password encryption: %w", err)
	}
	return openMetadataWithCipher(ctx, options, passwordCipher)
}

func openMetadataWithCipher(
	ctx context.Context,
	options MetadataOptions,
	passwordCipher *PasswordCipher,
) (*Store, error) {
	if err := validateMetadataOptions(ctx, options); err != nil {
		return nil, err
	}
	if passwordCipher == nil {
		return nil, fmt.Errorf("open metadata store: %w", ErrCipherUnavailable)
	}

	driverName, target := "", ""
	switch options.Driver {
	case DialectSQLite:
		driverName = "sqlite"
		target = options.SQLitePath
	case DialectPostgres:
		driverName = "pgx"
		target = options.PostgresDSN
	}

	database, err := sql.Open(driverName, target)
	if err != nil {
		if options.Driver == DialectSQLite {
			return nil, fmt.Errorf("open SQLite database %q: %w", options.SQLitePath, err)
		}
		return nil, fmt.Errorf("open %s metadata database: %w", options.Driver, err)
	}
	if options.Driver == DialectSQLite {
		database.SetMaxOpenConns(1)
		database.SetMaxIdleConns(1)
	} else if options.MaxOpenConns > 0 {
		database.SetMaxOpenConns(options.MaxOpenConns)
		database.SetMaxIdleConns(options.MaxIdleConns)
		database.SetConnMaxLifetime(options.ConnMaxLifetime)
	}

	if err := database.PingContext(ctx); err != nil {
		if options.Driver == DialectSQLite {
			return nil, closeDatabaseAfterError(
				database,
				options.Driver,
				fmt.Errorf("ping SQLite database %q: %w", options.SQLitePath, err),
			)
		}
		return nil, closeDatabaseAfterError(
			database,
			options.Driver,
			fmt.Errorf("ping %s metadata database: %w", options.Driver, err),
		)
	}
	if err := Migrate(ctx, database, options.Driver); err != nil {
		if options.Driver == DialectSQLite {
			return nil, closeDatabaseAfterError(
				database,
				options.Driver,
				fmt.Errorf("migrate SQLite database %q: %w", options.SQLitePath, err),
			)
		}
		return nil, closeDatabaseAfterError(
			database,
			options.Driver,
			fmt.Errorf("migrate %s metadata database: %w", options.Driver, err),
		)
	}

	return &Store{db: database, cipher: passwordCipher, driver: options.Driver}, nil
}

func validateMetadataOptions(ctx context.Context, options MetadataOptions) error {
	if ctx == nil {
		return fmt.Errorf("open metadata store: %w", ErrNilContext)
	}
	switch options.Driver {
	case DialectSQLite:
		if strings.TrimSpace(options.SQLitePath) == "" {
			return fmt.Errorf("open metadata store: %w", ErrInvalidStorePath)
		}
	case DialectPostgres:
		if strings.TrimSpace(options.PostgresDSN) == "" {
			return fmt.Errorf("open metadata store: %w", ErrInvalidPostgresDSN)
		}
	default:
		return fmt.Errorf("open metadata store with driver %q: %w", options.Driver, ErrInvalidMetadataDriver)
	}
	return nil
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

// Close releases the metadata connection.
func (store *Store) Close() error {
	if err := store.db.Close(); err != nil {
		if store.driver == DialectSQLite {
			return fmt.Errorf("close SQLite database: %w", err)
		}
		return fmt.Errorf("close %s metadata database: %w", store.driver, err)
	}
	return nil
}

// Agents returns the agent repository.
func (store *Store) Agents() *AgentRepository {
	return &AgentRepository{repositoryBase: repositoryBase{db: store.db, dialect: store.driver}}
}

// Datasources returns the datasource repository.
func (store *Store) Datasources() *DatasourceRepository {
	return &DatasourceRepository{
		repositoryBase: repositoryBase{db: store.db, dialect: store.driver},
		cipher:         store.cipher,
	}
}

// Policies returns the policy repository.
func (store *Store) Policies() *PolicyRepository {
	return &PolicyRepository{repositoryBase: repositoryBase{db: store.db, dialect: store.driver}}
}

// Rules returns the rule repository.
func (store *Store) Rules() *RuleRepository {
	return &RuleRepository{repositoryBase: repositoryBase{db: store.db, dialect: store.driver}}
}

// MaskRules returns the mask-rule repository.
func (store *Store) MaskRules() *MaskRuleRepository {
	return &MaskRuleRepository{repositoryBase: repositoryBase{db: store.db, dialect: store.driver}}
}

// AuditLogs returns the append-only audit-log repository.
func (store *Store) AuditLogs() *AuditLogRepository {
	return &AuditLogRepository{repositoryBase: repositoryBase{db: store.db, dialect: store.driver}}
}

// Approvals returns the approval repository.
func (store *Store) Approvals() *ApprovalRepository {
	return &ApprovalRepository{repositoryBase: repositoryBase{db: store.db, dialect: store.driver}}
}

// Dashboard returns read-only aggregate queries for the admin dashboard.
func (store *Store) Dashboard() *DashboardRepository {
	return &DashboardRepository{repositoryBase: repositoryBase{db: store.db, dialect: store.driver}}
}

func closeDatabaseAfterError(database *sql.DB, driver Dialect, cause error) error {
	if err := database.Close(); err != nil {
		if driver == DialectSQLite {
			return fmt.Errorf("close SQLite database after failure: %w", errors.Join(cause, err))
		}
		return fmt.Errorf("close %s metadata database after failure: %w", driver, errors.Join(cause, err))
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

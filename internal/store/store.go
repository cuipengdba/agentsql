// Package store persists AgentSQL metadata and immutable audit records.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
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
	// ErrInvalidAuditDriver indicates an unsupported independent audit driver.
	ErrInvalidAuditDriver = errors.New("unsupported audit store driver")
	// ErrNilContext indicates that a store operation received no context.
	ErrNilContext = errors.New("context is required")
)

// AuditOptions configures the optional independent audit connection.
type AuditOptions struct {
	Separate     bool
	Driver       Dialect
	PostgresDSN  string
	MaxOpenConns int
	MaxIdleConns int
	AutoMigrate  bool
}

// MetadataOptions configures the metadata connection and its audit target.
type MetadataOptions struct {
	Driver          Dialect
	SQLitePath      string
	PostgresDSN     string
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	AutoMigrate     bool
	Audit           AuditOptions
}

// Store owns the metadata and audit connections plus metadata-side encryption.
type Store struct {
	metaDB        *sql.DB
	auditDB       *sql.DB
	metaDriver    Dialect
	auditDriver   Dialect
	auditSeparate bool
	cipher        *PasswordCipher
	applyMu       sync.Mutex
	redactionMu   sync.Mutex
}

// MetadataRegistryView is the metadata-only surface used by registry and relay tooling.
type MetadataRegistryView interface {
	MigrationVersions(context.Context) (current, latest int, err error)
	RedactionKeys() *RedactionKeyRepository
	Outbox() *ManagementAuditOutboxRepository
	Close() error
}

// AuditStoreView is the audit-only surface used by management audit delivery.
type AuditStoreView interface {
	MigrationVersions(context.Context) (current, latest int, err error)
	AuditLogs() *AuditLogRepository
	Close() error
}

// ChainAccess is a domain-bound view of the database resources needed to
// observe and verify an audit chain. It deliberately exposes repositories and
// verifier construction rather than a general-purpose database handle.
type ChainAccess struct {
	db      *sql.DB
	dialect Dialect
	domain  string
}

// Chain returns the database view for one audit-chain domain. Management is
// always stored with metadata. Traffic uses the independent audit database
// when configured, and otherwise shares the metadata database.
func (store *Store) Chain(domain string) (*ChainAccess, error) {
	if store == nil || store.metaDB == nil || store.auditDB == nil {
		return nil, fmt.Errorf("open audit chain %q: store is unavailable", domain)
	}
	access := &ChainAccess{db: store.metaDB, dialect: store.metaDriver, domain: domain}
	switch domain {
	case "management":
	case "traffic":
		if store.auditSeparate {
			access.db = store.auditDB
			access.dialect = store.auditDriver
		}
	default:
		return nil, fmt.Errorf("open audit chain %q: unsupported domain", domain)
	}
	return access, nil
}

// State returns the read-only state repository for this chain database.
func (access *ChainAccess) State() *ChainStateRepository {
	if access == nil {
		return &ChainStateRepository{}
	}
	return &ChainStateRepository{repositoryBase: repositoryBase{db: access.db, dialect: access.dialect}}
}

// Verifier constructs the existing verifier against this chain database.
func (access *ChainAccess) Verifier(manifest ChainManifest) *ChainVerifier {
	if access == nil {
		return NewChainVerifier(nil, "", "", manifest)
	}
	return NewChainVerifier(access.db, access.dialect, access.domain, manifest)
}

type metadataOnlyStore struct {
	db       *sql.DB
	dialect  Dialect
	separate bool
	mu       sync.Mutex
}

type auditOnlyStore struct {
	db      *sql.DB
	dialect Dialect
}

// Ping verifies both targets. A shared connection is pinged exactly once.
func (store *Store) Ping(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("ping store: %w", ErrNilContext)
	}
	if store == nil || store.metaDB == nil {
		return fmt.Errorf("ping store: metadata database is unavailable")
	}
	if err := store.metaDB.PingContext(ctx); err != nil {
		return fmt.Errorf("ping metadata store: %w", err)
	}
	if store.auditDB == nil {
		return fmt.Errorf("ping store: audit database is unavailable")
	}
	if store.auditDB != store.metaDB {
		if err := store.auditDB.PingContext(ctx); err != nil {
			return fmt.Errorf("ping audit store: %w", err)
		}
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
		Driver:      DialectSQLite,
		SQLitePath:  path,
		AutoMigrate: true,
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
		Driver:      DialectSQLite,
		SQLitePath:  path,
		AutoMigrate: true,
	}, passwordCipher)
}

// OpenMetadata opens, verifies, and optionally migrates both configured stores.
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

// OpenMetadataOnly opens and prepares only the metadata target. Audit settings
// select the combined or separated migration stream but are never connected.
func OpenMetadataOnly(ctx context.Context, options MetadataOptions, secret []byte) (MetadataRegistryView, error) {
	if err := validateMetadataOnlyOptions(ctx, options); err != nil {
		return nil, err
	}
	if _, err := NewPasswordCipher(secret); err != nil {
		return nil, fmt.Errorf("initialize datasource password encryption: %w", err)
	}
	database, err := openDatabase(options.Driver, options.SQLitePath, options.PostgresDSN, options.MaxOpenConns, options.MaxIdleConns, options.ConnMaxLifetime)
	if err != nil {
		return nil, fmt.Errorf("open %s metadata database: %w", options.Driver, err)
	}
	if err := database.PingContext(ctx); err != nil {
		return nil, closeDatabasesAfterError(nil, database, fmt.Errorf("ping %s metadata database: %w", options.Driver, err))
	}
	if options.AutoMigrate {
		err = MigrateMetadata(ctx, database, options.Driver, options.Audit.Separate)
	} else {
		err = VerifyMetadataSchema(ctx, database, options.Driver, options.Audit.Separate)
	}
	if err != nil {
		return nil, closeDatabasesAfterError(nil, database, fmt.Errorf("prepare %s metadata database: %w", options.Driver, err))
	}
	return &metadataOnlyStore{db: database, dialect: options.Driver, separate: options.Audit.Separate}, nil
}

// OpenAuditOnly opens and prepares an independent PostgreSQL audit target.
func OpenAuditOnly(ctx context.Context, options AuditOptions) (AuditStoreView, error) {
	if err := validateAuditOnlyOptions(ctx, options); err != nil {
		return nil, err
	}
	database, err := openDatabase(options.Driver, "", options.PostgresDSN, options.MaxOpenConns, options.MaxIdleConns, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s audit database: %w", options.Driver, err)
	}
	if err := database.PingContext(ctx); err != nil {
		return nil, closeDatabasesAfterError(database, nil, fmt.Errorf("ping %s audit database: %w", options.Driver, err))
	}
	if options.AutoMigrate {
		err = MigrateAudit(ctx, database, options.Driver)
	} else {
		err = VerifyAuditSchema(ctx, database, options.Driver)
	}
	if err != nil {
		return nil, closeDatabasesAfterError(database, nil, fmt.Errorf("prepare %s audit database: %w", options.Driver, err))
	}
	return &auditOnlyStore{db: database, dialect: options.Driver}, nil
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

	metadataDB, err := openDatabase(options.Driver, options.SQLitePath, options.PostgresDSN, options.MaxOpenConns, options.MaxIdleConns, options.ConnMaxLifetime)
	if err != nil {
		return nil, fmt.Errorf("open %s metadata database: %w", options.Driver, err)
	}
	if err := metadataDB.PingContext(ctx); err != nil {
		return nil, closeDatabasesAfterError(nil, metadataDB, fmt.Errorf("ping %s metadata database: %w", options.Driver, err))
	}
	if options.AutoMigrate {
		err = MigrateMetadata(ctx, metadataDB, options.Driver, options.Audit.Separate)
	} else {
		err = VerifyMetadataSchema(ctx, metadataDB, options.Driver, options.Audit.Separate)
	}
	if err != nil {
		return nil, closeDatabasesAfterError(nil, metadataDB, fmt.Errorf("prepare %s metadata database: %w", options.Driver, err))
	}

	opened := &Store{
		metaDB:      metadataDB,
		auditDB:     metadataDB,
		metaDriver:  options.Driver,
		auditDriver: options.Driver,
		cipher:      passwordCipher,
	}
	if !options.Audit.Separate {
		return opened, nil
	}

	auditDB, err := openDatabase(
		options.Audit.Driver,
		"",
		options.Audit.PostgresDSN,
		options.Audit.MaxOpenConns,
		options.Audit.MaxIdleConns,
		0,
	)
	if err != nil {
		return nil, closeDatabasesAfterError(nil, metadataDB, fmt.Errorf("open %s audit database: %w", options.Audit.Driver, err))
	}
	if err := auditDB.PingContext(ctx); err != nil {
		return nil, closeDatabasesAfterError(auditDB, metadataDB, fmt.Errorf("ping %s audit database: %w", options.Audit.Driver, err))
	}
	if options.Audit.AutoMigrate {
		err = MigrateAudit(ctx, auditDB, options.Audit.Driver)
	} else {
		err = VerifyAuditSchema(ctx, auditDB, options.Audit.Driver)
	}
	if err != nil {
		return nil, closeDatabasesAfterError(auditDB, metadataDB, fmt.Errorf("prepare %s audit database: %w", options.Audit.Driver, err))
	}
	opened.auditDB = auditDB
	opened.auditDriver = options.Audit.Driver
	opened.auditSeparate = true
	return opened, nil
}

func openDatabase(
	dialect Dialect,
	sqlitePath string,
	postgresDSN string,
	maxOpenConns int,
	maxIdleConns int,
	connMaxLifetime time.Duration,
) (*sql.DB, error) {
	driverName, target := "sqlite", sqlitePath
	if dialect == DialectPostgres {
		driverName, target = "pgx", postgresDSN
	}
	database, err := sql.Open(driverName, target)
	if err != nil {
		return nil, err
	}
	if dialect == DialectSQLite {
		database.SetMaxOpenConns(1)
		database.SetMaxIdleConns(1)
		return database, nil
	}
	if maxOpenConns == 0 {
		maxOpenConns = 10
	}
	if maxIdleConns == 0 {
		maxIdleConns = 5
	}
	database.SetMaxOpenConns(maxOpenConns)
	database.SetMaxIdleConns(maxIdleConns)
	if connMaxLifetime > 0 {
		database.SetConnMaxLifetime(connMaxLifetime)
	}
	return database, nil
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
	if err := validatePool(options.MaxOpenConns, options.MaxIdleConns); err != nil {
		return fmt.Errorf("open metadata store: %w", err)
	}
	if !options.Audit.Separate {
		if options.Audit.Driver != "" || strings.TrimSpace(options.Audit.PostgresDSN) != "" ||
			options.Audit.MaxOpenConns != 0 || options.Audit.MaxIdleConns != 0 {
			return fmt.Errorf("open audit store: connection settings require separate=true")
		}
		return nil
	}
	if options.Audit.Driver != DialectPostgres {
		return fmt.Errorf("open audit store with driver %q: %w", options.Audit.Driver, ErrInvalidAuditDriver)
	}
	if strings.TrimSpace(options.Audit.PostgresDSN) == "" {
		return fmt.Errorf("open audit store: %w", ErrInvalidPostgresDSN)
	}
	if err := validatePool(options.Audit.MaxOpenConns, options.Audit.MaxIdleConns); err != nil {
		return fmt.Errorf("open audit store: %w", err)
	}
	return nil
}

func validateMetadataOnlyOptions(ctx context.Context, options MetadataOptions) error {
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
	if err := validatePool(options.MaxOpenConns, options.MaxIdleConns); err != nil {
		return fmt.Errorf("open metadata store: %w", err)
	}
	return nil
}

func validateAuditOnlyOptions(ctx context.Context, options AuditOptions) error {
	if ctx == nil {
		return fmt.Errorf("open audit store: %w", ErrNilContext)
	}
	if options.Driver != DialectPostgres {
		return fmt.Errorf("open audit store with driver %q: %w", options.Driver, ErrInvalidAuditDriver)
	}
	if strings.TrimSpace(options.PostgresDSN) == "" {
		return fmt.Errorf("open audit store: %w", ErrInvalidPostgresDSN)
	}
	if err := validatePool(options.MaxOpenConns, options.MaxIdleConns); err != nil {
		return fmt.Errorf("open audit store: %w", err)
	}
	return nil
}

func validatePool(maxOpenConns, maxIdleConns int) error {
	if maxOpenConns < 0 || maxIdleConns < 0 {
		return errors.New("connection pool values must not be negative")
	}
	if maxOpenConns > 0 && maxIdleConns > maxOpenConns {
		return errors.New("max_idle_conns must not exceed max_open_conns")
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

// Close releases the independent audit connection first, then metadata. Pointer
// identity is the only criterion used to avoid double-closing a shared pool.
func (store *Store) Close() error {
	if store == nil {
		return nil
	}
	var auditErr, metadataErr error
	if store.auditDB != nil && store.auditDB != store.metaDB {
		if err := store.auditDB.Close(); err != nil {
			auditErr = fmt.Errorf("close %s audit database: %w", store.auditDriver, err)
		}
	}
	if store.metaDB != nil {
		if err := store.metaDB.Close(); err != nil {
			metadataErr = fmt.Errorf("close %s metadata database: %w", store.metaDriver, err)
		}
	}
	return errors.Join(auditErr, metadataErr)
}

// Agents returns the agent repository.
func (store *Store) Agents() *AgentRepository {
	return &AgentRepository{repositoryBase: repositoryBase{db: store.metaDB, dialect: store.metaDriver}}
}

// Datasources returns the datasource repository.
func (store *Store) Datasources() *DatasourceRepository {
	return &DatasourceRepository{
		repositoryBase: repositoryBase{db: store.metaDB, dialect: store.metaDriver},
		cipher:         store.cipher,
	}
}

// Notifications returns the notification configuration repository.
func (store *Store) Notifications() *NotificationRepository {
	return &NotificationRepository{
		repositoryBase: repositoryBase{db: store.metaDB, dialect: store.metaDriver},
		cipher:         store.cipher,
	}
}

// Policies returns the policy repository.
func (store *Store) Policies() *PolicyRepository {
	return &PolicyRepository{repositoryBase: repositoryBase{db: store.metaDB, dialect: store.metaDriver}}
}

// Fence returns the reader/writer fence and runtime lease repository.
func (store *Store) Fence() *FenceRepository {
	return &FenceRepository{repositoryBase: repositoryBase{db: store.metaDB, dialect: store.metaDriver}}
}

// Rules returns the rule repository.
func (store *Store) Rules() *RuleRepository {
	return &RuleRepository{repositoryBase: repositoryBase{db: store.metaDB, dialect: store.metaDriver}}
}

// MaskRules returns the mask-rule repository.
func (store *Store) MaskRules() *MaskRuleRepository {
	return &MaskRuleRepository{
		repositoryBase: repositoryBase{db: store.metaDB, dialect: store.metaDriver},
		auditSeparate:  store.auditSeparate,
		outbox:         &ManagementAuditOutboxRepository{repositoryBase: repositoryBase{db: store.metaDB, dialect: store.metaDriver}},
		applyMu:        &store.applyMu,
	}
}

// RedactionKeys returns the metadata-side redaction key registry.
func (store *Store) RedactionKeys() *RedactionKeyRepository {
	return &RedactionKeyRepository{repositoryBase: repositoryBase{db: store.metaDB, dialect: store.metaDriver}, mu: &store.redactionMu}
}

// Outbox returns the metadata-side management audit outbox.
func (store *Store) Outbox() *ManagementAuditOutboxRepository {
	return &ManagementAuditOutboxRepository{repositoryBase: repositoryBase{db: store.metaDB, dialect: store.metaDriver}}
}

// AuditLogs returns the append-only repository bound to the audit target.
func (store *Store) AuditLogs() *AuditLogRepository {
	chainID := "management"
	if store.auditSeparate {
		chainID = "traffic"
	}
	return &AuditLogRepository{
		repositoryBase: repositoryBase{db: store.auditDB, dialect: store.auditDriver},
		chainID:        chainID,
	}
}

// ManagementAuditLogs returns the chain-aware repository for the metadata
// database. It differs from AuditLogs only when traffic uses a separate audit
// database.
func (store *Store) ManagementAuditLogs() *AuditLogRepository {
	if store == nil {
		return &AuditLogRepository{}
	}
	return &AuditLogRepository{
		repositoryBase: repositoryBase{db: store.metaDB, dialect: store.metaDriver},
		chainID:        "management",
	}
}

// Approvals uses metadata for approval rows and the configured audit target for
// the audit-first saga when those targets are separate.
func (store *Store) Approvals() *ApprovalRepository {
	return &ApprovalRepository{
		repositoryBase: repositoryBase{db: store.metaDB, dialect: store.metaDriver},
		auditDB:        store.auditDB,
		auditDialect:   store.auditDriver,
		auditSeparate:  store.auditSeparate,
	}
}

// B5Sessions exposes the feature-off B5 session persistence surface. No
// production handler calls this repository until a later activation slice.
func (store *Store) B5Sessions() *B5SessionRepository {
	return &B5SessionRepository{repositoryBase: repositoryBase{db: store.metaDB, dialect: store.metaDriver}}
}

func (store *Store) B5Transactions() *B5TransactionRepository {
	return &B5TransactionRepository{repositoryBase: repositoryBase{db: store.metaDB, dialect: store.metaDriver}}
}

func (store *Store) B5DMLGrants() *B5DMLGrantRepository {
	return &B5DMLGrantRepository{repositoryBase: repositoryBase{db: store.metaDB, dialect: store.metaDriver}}
}

func (store *Store) B5ResultReceipts() *B5ResultReceiptRepository {
	return &B5ResultReceiptRepository{repositoryBase: repositoryBase{db: store.metaDB, dialect: store.metaDriver}}
}

func (store *Store) B5TxEvents() *B5TxEventRepository {
	return &B5TxEventRepository{repositoryBase: repositoryBase{db: store.metaDB, dialect: store.metaDriver}}
}

// Dashboard returns aggregates composed from the metadata and audit targets.
func (store *Store) Dashboard() *DashboardRepository {
	return NewDashboardRepository(store.metaDB, store.auditDB, store.metaDriver, store.auditDriver)
}

func (store *metadataOnlyStore) MigrationVersions(ctx context.Context) (int, int, error) {
	return MetadataMigrationVersions(ctx, store.db, store.dialect, store.separate)
}

func (store *metadataOnlyStore) RedactionKeys() *RedactionKeyRepository {
	return &RedactionKeyRepository{repositoryBase: repositoryBase{db: store.db, dialect: store.dialect}, mu: &store.mu}
}

func (store *metadataOnlyStore) Outbox() *ManagementAuditOutboxRepository {
	return &ManagementAuditOutboxRepository{repositoryBase: repositoryBase{db: store.db, dialect: store.dialect}}
}

func (store *metadataOnlyStore) Close() error {
	if store == nil || store.db == nil {
		return nil
	}
	return store.db.Close()
}

func (store *auditOnlyStore) MigrationVersions(ctx context.Context) (int, int, error) {
	return AuditMigrationVersions(ctx, store.db, store.dialect)
}

func (store *auditOnlyStore) AuditLogs() *AuditLogRepository {
	return &AuditLogRepository{
		repositoryBase: repositoryBase{db: store.db, dialect: store.dialect},
		chainID:        "traffic",
	}
}

func (store *auditOnlyStore) Close() error {
	if store == nil || store.db == nil {
		return nil
	}
	return store.db.Close()
}

func closeDatabasesAfterError(auditDB, metadataDB *sql.DB, cause error) error {
	var auditErr, metadataErr error
	if auditDB != nil && auditDB != metadataDB {
		auditErr = auditDB.Close()
	}
	if metadataDB != nil {
		metadataErr = metadataDB.Close()
	}
	return errors.Join(cause, auditErr, metadataErr)
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

package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"
)

const sqliteSchemaMigrationsDDL = `CREATE TABLE IF NOT EXISTS schema_migrations (
  version    INTEGER PRIMARY KEY,
  applied_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);`

const postgresSchemaMigrationsDDL = `CREATE TABLE IF NOT EXISTS schema_migrations (
  version    BIGINT PRIMARY KEY,
  applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
);`

var errForeignKeysDisabled = errors.New("SQLite foreign key enforcement is disabled")

//go:embed migrations/sqlite/*.sql migrations/postgres/*.sql migrations/metadata/sqlite/*.sql migrations/metadata/postgres/*.sql migrations/audit/postgres/*.sql
var migrationFiles embed.FS

// Migrate applies each embedded migration exactly once in version order.
func Migrate(ctx context.Context, database *sql.DB, dialect Dialect) error {
	return migrateDirectory(ctx, database, dialect, "migrations/"+string(dialect))
}

// MigrateMetadata applies the metadata migration stream for the requested
// layout. The combined stream remains byte-for-byte unchanged when audit data
// shares the metadata database.
func MigrateMetadata(ctx context.Context, database *sql.DB, dialect Dialect, auditSeparate bool) error {
	directory := "migrations/" + string(dialect)
	if auditSeparate {
		directory = "migrations/metadata/" + string(dialect)
	}
	return migrateDirectory(ctx, database, dialect, directory)
}

// MigrateAudit applies the independent audit-only migration stream.
func MigrateAudit(ctx context.Context, database *sql.DB, dialect Dialect) error {
	if dialect != DialectPostgres {
		return fmt.Errorf("migrate audit store: unsupported audit dialect %q", dialect)
	}
	return migrateDirectory(ctx, database, dialect, "migrations/audit/"+string(dialect))
}

// VerifyMetadataSchema checks the deployed metadata schema without executing
// DDL. It is the fail-closed startup path used when auto_migrate is disabled.
func VerifyMetadataSchema(ctx context.Context, database *sql.DB, dialect Dialect, auditSeparate bool) error {
	directory := "migrations/" + string(dialect)
	if auditSeparate {
		directory = "migrations/metadata/" + string(dialect)
	}
	return verifyMigrationDirectory(ctx, database, dialect, directory, "metadata")
}

// VerifyAuditSchema checks the deployed independent audit schema without DDL.
func VerifyAuditSchema(ctx context.Context, database *sql.DB, dialect Dialect) error {
	if dialect != DialectPostgres {
		return fmt.Errorf("verify audit store: unsupported audit dialect %q", dialect)
	}
	return verifyMigrationDirectory(ctx, database, dialect, "migrations/audit/"+string(dialect), "audit")
}

// MetadataMigrationVersions returns current and code-latest metadata versions.
func MetadataMigrationVersions(ctx context.Context, database *sql.DB, dialect Dialect, auditSeparate bool) (int, int, error) {
	directory := "migrations/" + string(dialect)
	if auditSeparate {
		directory = "migrations/metadata/" + string(dialect)
	}
	return migrationVersions(ctx, database, directory)
}

// AuditMigrationVersions returns current and code-latest audit versions.
func AuditMigrationVersions(ctx context.Context, database *sql.DB, dialect Dialect) (int, int, error) {
	if dialect != DialectPostgres {
		return 0, 0, fmt.Errorf("read audit migration versions: unsupported audit dialect %q", dialect)
	}
	return migrationVersions(ctx, database, "migrations/audit/"+string(dialect))
}

func migrateDirectory(ctx context.Context, database *sql.DB, dialect Dialect, directory string) error {
	if ctx == nil {
		return fmt.Errorf("migrate store: %w", ErrNilContext)
	}
	if database == nil {
		return fmt.Errorf("migrate store: %w", errors.New("database is required"))
	}

	schemaDDL, err := schemaMigrationsDDLFor(dialect)
	if err != nil {
		return err
	}
	if dialect == DialectSQLite {
		if err := enableSQLiteForeignKeys(ctx, database); err != nil {
			return err
		}
	}

	if _, err := database.ExecContext(ctx, schemaDDL); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	dialectMigrations, err := fs.Sub(migrationFiles, directory)
	if err != nil {
		return fmt.Errorf("select %s migrations: %w", dialect, err)
	}
	filenames, err := fs.Glob(dialectMigrations, "*.sql")
	if err != nil {
		return fmt.Errorf("list embedded migrations: %w", err)
	}
	sort.Strings(filenames)
	for _, filename := range filenames {
		version, err := migrationVersion(filename)
		if err != nil {
			return fmt.Errorf("parse migration %q: %w", filename, err)
		}
		contents, err := fs.ReadFile(dialectMigrations, filename)
		if err != nil {
			return fmt.Errorf("read migration %q: %w", filename, err)
		}
		if err := applyMigration(ctx, database, dialect, version, string(contents)); err != nil {
			return fmt.Errorf("apply migration %d: %w", version, err)
		}
	}

	return nil
}

func verifyMigrationDirectory(
	ctx context.Context,
	database *sql.DB,
	dialect Dialect,
	directory string,
	label string,
) error {
	if ctx == nil {
		return fmt.Errorf("verify %s schema: %w", label, ErrNilContext)
	}
	if database == nil {
		return fmt.Errorf("verify %s schema: database is required", label)
	}
	if _, err := schemaMigrationsDDLFor(dialect); err != nil {
		return err
	}
	if dialect == DialectSQLite {
		if err := enableSQLiteForeignKeys(ctx, database); err != nil {
			return err
		}
	}
	current, latest, err := migrationVersions(ctx, database, directory)
	if err != nil {
		return fmt.Errorf("verify %s schema version: %w", label, err)
	}
	if current != latest {
		return fmt.Errorf("verify %s schema version: current=%d latest=%d", label, current, latest)
	}
	return nil
}

func migrationVersions(ctx context.Context, database *sql.DB, directory string) (int, int, error) {
	if ctx == nil {
		return 0, 0, fmt.Errorf("read migration versions: %w", ErrNilContext)
	}
	if database == nil {
		return 0, 0, fmt.Errorf("read migration versions: database is required")
	}
	latest, err := latestMigrationVersion(directory)
	if err != nil {
		return 0, 0, err
	}
	var current int
	if err := database.QueryRowContext(ctx, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&current); err != nil {
		return 0, latest, fmt.Errorf("read schema_migrations: %w", err)
	}
	return current, latest, nil
}

func latestMigrationVersion(directory string) (int, error) {
	dialectMigrations, err := fs.Sub(migrationFiles, directory)
	if err != nil {
		return 0, fmt.Errorf("select migrations: %w", err)
	}
	filenames, err := fs.Glob(dialectMigrations, "*.sql")
	if err != nil {
		return 0, fmt.Errorf("list embedded migrations: %w", err)
	}
	latest := 0
	for _, filename := range filenames {
		version, err := migrationVersion(filename)
		if err != nil {
			return 0, fmt.Errorf("parse migration %q: %w", filename, err)
		}
		if version > latest {
			latest = version
		}
	}
	if latest == 0 {
		return 0, fmt.Errorf("no embedded migrations in %q", directory)
	}
	return latest, nil
}

func schemaMigrationsDDLFor(dialect Dialect) (string, error) {
	switch dialect {
	case DialectSQLite:
		return sqliteSchemaMigrationsDDL, nil
	case DialectPostgres:
		return postgresSchemaMigrationsDDL, nil
	default:
		return "", fmt.Errorf("migrate store: unsupported metadata dialect %q", dialect)
	}
}

func enableSQLiteForeignKeys(ctx context.Context, database *sql.DB) error {
	if _, err := database.ExecContext(ctx, "PRAGMA foreign_keys = ON;"); err != nil {
		return fmt.Errorf("enable SQLite foreign keys: %w", err)
	}
	var foreignKeysEnabled int
	if err := database.QueryRowContext(ctx, "PRAGMA foreign_keys;").Scan(&foreignKeysEnabled); err != nil {
		return fmt.Errorf("verify SQLite foreign keys: %w", err)
	}
	if foreignKeysEnabled != 1 {
		return fmt.Errorf("verify SQLite foreign keys: %w", errForeignKeysDisabled)
	}
	return nil
}

func migrationVersion(filename string) (int, error) {
	base := path.Base(filename)
	prefix, _, found := strings.Cut(base, "_")
	if !found || prefix == "" {
		return 0, fmt.Errorf("migration filename %q: %w", base, errors.New("expected numeric prefix followed by underscore"))
	}
	version, err := strconv.Atoi(prefix)
	if err != nil {
		return 0, fmt.Errorf("parse migration version %q: %w", prefix, err)
	}
	if version <= 0 {
		return 0, fmt.Errorf("migration version %d: %w", version, errors.New("version must be positive"))
	}
	return version, nil
}

func applyMigration(ctx context.Context, database *sql.DB, dialect Dialect, version int, contents string) error {
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration %d: %w", version, err)
	}

	claimed, err := claimMigration(ctx, transaction, dialect, version)
	if err != nil {
		return fmt.Errorf("claim migration %d: %w", version, rollbackMigration(transaction, err))
	}
	if !claimed {
		if err := transaction.Rollback(); err != nil {
			return fmt.Errorf("rollback skipped migration %d: %w", version, err)
		}
		return nil
	}

	if _, err := transaction.ExecContext(ctx, contents); err != nil {
		return fmt.Errorf("execute migration %d: %w", version, rollbackMigration(transaction, err))
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit migration %d: %w", version, err)
	}
	return nil
}

func claimMigration(ctx context.Context, transaction *sql.Tx, dialect Dialect, version int) (bool, error) {
	switch dialect {
	case DialectSQLite:
		result, err := transaction.ExecContext(
			ctx,
			"INSERT OR IGNORE INTO schema_migrations(version) VALUES(?)",
			version,
		)
		if err != nil {
			return false, fmt.Errorf("insert SQLite migration claim: %w", err)
		}
		rowsAffected, err := result.RowsAffected()
		if err != nil {
			return false, fmt.Errorf("read SQLite migration claim result: %w", err)
		}
		return rowsAffected == 1, nil
	case DialectPostgres:
		var claimedVersion int
		err := transaction.QueryRowContext(
			ctx,
			`INSERT INTO schema_migrations(version, applied_at)
VALUES($1, now())
ON CONFLICT (version) DO NOTHING
RETURNING version`,
			version,
		).Scan(&claimedVersion)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("insert PostgreSQL migration claim: %w", err)
		}
		return claimedVersion == version, nil
	default:
		return false, fmt.Errorf("claim migration: unsupported metadata dialect %q", dialect)
	}
}

func rollbackMigration(transaction *sql.Tx, cause error) error {
	if err := transaction.Rollback(); err != nil {
		return errors.Join(cause, fmt.Errorf("rollback migration: %w", err))
	}
	return cause
}

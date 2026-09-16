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

//go:embed migrations/sqlite/*.sql migrations/postgres/*.sql
var migrationFiles embed.FS

// Migrate applies each embedded migration exactly once in version order.
func Migrate(ctx context.Context, database *sql.DB, dialect Dialect) error {
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

	dialectMigrations, err := fs.Sub(migrationFiles, "migrations/"+string(dialect))
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

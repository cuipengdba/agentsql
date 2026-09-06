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

const schemaMigrationsDDL = `CREATE TABLE IF NOT EXISTS schema_migrations (
  version    INTEGER PRIMARY KEY,
  applied_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);`

var errForeignKeysDisabled = errors.New("SQLite foreign key enforcement is disabled")

//go:embed migrations/*.sql
var migrationFiles embed.FS

// Migrate applies each embedded migration exactly once in version order.
func Migrate(ctx context.Context, database *sql.DB) error {
	if ctx == nil {
		return fmt.Errorf("migrate store: %w", ErrNilContext)
	}
	if database == nil {
		return fmt.Errorf("migrate store: %w", errors.New("database is required"))
	}

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

	if _, err := database.ExecContext(ctx, schemaMigrationsDDL); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	filenames, err := fs.Glob(migrationFiles, "migrations/*.sql")
	if err != nil {
		return fmt.Errorf("list embedded migrations: %w", err)
	}
	sort.Strings(filenames)
	for _, filename := range filenames {
		version, err := migrationVersion(filename)
		if err != nil {
			return fmt.Errorf("parse migration %q: %w", filename, err)
		}
		applied, err := isMigrationApplied(ctx, database, version)
		if err != nil {
			return fmt.Errorf("check migration %d: %w", version, err)
		}
		if applied {
			continue
		}

		contents, err := migrationFiles.ReadFile(filename)
		if err != nil {
			return fmt.Errorf("read migration %q: %w", filename, err)
		}
		if err := applyMigration(ctx, database, version, string(contents)); err != nil {
			return fmt.Errorf("apply migration %d: %w", version, err)
		}
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

func isMigrationApplied(ctx context.Context, database *sql.DB, version int) (bool, error) {
	var count int
	if err := database.QueryRowContext(
		ctx,
		"SELECT COUNT(*) FROM schema_migrations WHERE version = ?",
		version,
	).Scan(&count); err != nil {
		return false, fmt.Errorf("query schema_migrations version %d: %w", version, err)
	}
	return count == 1, nil
}

func applyMigration(ctx context.Context, database *sql.DB, version int, contents string) error {
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration %d: %w", version, err)
	}

	if _, err := transaction.ExecContext(ctx, contents); err != nil {
		return fmt.Errorf("execute migration %d: %w", version, rollbackMigration(transaction, err))
	}
	if _, err := transaction.ExecContext(
		ctx,
		"INSERT INTO schema_migrations (version) VALUES (?)",
		version,
	); err != nil {
		return fmt.Errorf("record migration %d: %w", version, rollbackMigration(transaction, err))
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit migration %d: %w", version, err)
	}
	return nil
}

func rollbackMigration(transaction *sql.Tx, cause error) error {
	if err := transaction.Rollback(); err != nil {
		return errors.Join(cause, fmt.Errorf("rollback migration: %w", err))
	}
	return cause
}

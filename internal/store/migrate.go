package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
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

//go:embed migrations/sqlite/*.sql migrations/sqlite/down/*.sql migrations/postgres/*.sql migrations/postgres/down/*.sql migrations/metadata/sqlite/*.sql migrations/metadata/sqlite/down/*.sql migrations/metadata/postgres/*.sql migrations/metadata/postgres/down/*.sql migrations/audit/postgres/*.sql
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

// RollbackMetadataMigration applies the checked-in down migration for the
// current metadata version. Repeating the same version is a no-op; skipping a
// newer applied version is rejected.
func RollbackMetadataMigration(ctx context.Context, database *sql.DB, dialect Dialect, auditSeparate bool, version int) error {
	if ctx == nil {
		return fmt.Errorf("rollback metadata migration: %w", ErrNilContext)
	}
	directory := "migrations/" + string(dialect)
	if auditSeparate {
		directory = "migrations/metadata/" + string(dialect)
	}
	matches, err := fs.Glob(migrationFiles, directory+"/down/"+fmt.Sprintf("%04d_*.sql", version))
	if err != nil || len(matches) != 1 {
		return fmt.Errorf("rollback metadata migration %d: down migration not found", version)
	}
	contents, err := fs.ReadFile(migrationFiles, matches[0])
	if err != nil {
		return fmt.Errorf("rollback metadata migration %d: %w", version, err)
	}
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin rollback metadata migration %d: %w", version, err)
	}
	if dialect == DialectPostgres {
		if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1)", int64(0x4153514c4d494702)); err != nil {
			return rollbackMigration(tx, err)
		}
	}
	var current int
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(version),0) FROM schema_migrations").Scan(&current); err != nil {
		return rollbackMigration(tx, err)
	}
	if current < version {
		return tx.Rollback()
	}
	if current != version {
		return rollbackMigration(tx, fmt.Errorf("cannot rollback version %d while current version is %d", version, current))
	}
	if _, err := tx.ExecContext(ctx, string(contents)); err != nil {
		return fmt.Errorf("execute down migration %d: %w", version, rollbackMigration(tx, err))
	}
	deleteSQL := "DELETE FROM schema_migrations WHERE version=?"
	if dialect == DialectPostgres {
		deleteSQL = "DELETE FROM schema_migrations WHERE version=$1"
	}
	if _, err := tx.ExecContext(ctx, deleteSQL, version); err != nil {
		return rollbackMigration(tx, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit down migration %d: %w", version, err)
	}
	return nil
}

// LatestCombinedSQLiteMigrationVersion returns the schema version required of
// a source database accepted by the SQLite-to-PostgreSQL搬迁器. The source is
// deliberately limited to the combined SQLite migration stream.
func LatestCombinedSQLiteMigrationVersion() (int, error) {
	return latestMigrationVersion("migrations/sqlite")
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
		preflightMaskRuleDuplicates := filename == "0003_discovery_drafts.sql"
		columnAuthorizationMigration := strings.HasSuffix(filename, "_column_authorization.sql")
		if err := applyMigrationWithOptions(
			ctx, database, dialect, version, string(contents), preflightMaskRuleDuplicates,
			columnAuthorizationMigration,
		); err != nil {
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
	return applyMigrationWithOptions(ctx, database, dialect, version, contents, false, false)
}

func applyMigrationWithOptions(
	ctx context.Context,
	database *sql.DB,
	dialect Dialect,
	version int,
	contents string,
	preflightMaskRuleDuplicates bool,
	columnAuthorizationMigration bool,
) error {
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration %d: %w", version, err)
	}
	if dialect == DialectPostgres {
		if _, err := transaction.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1)", int64(0x4153514c4d494702)); err != nil {
			return fmt.Errorf("lock migration %d: %w", version, rollbackMigration(transaction, err))
		}
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
	if preflightMaskRuleDuplicates {
		if err := rejectDuplicateMaskRuleKeys(ctx, transaction, dialect); err != nil {
			return fmt.Errorf("preflight mask_rules uniqueness: %w", rollbackMigration(transaction, err))
		}
	}
	legacyColumns := []legacyPolicyColumns(nil)
	if columnAuthorizationMigration {
		legacyColumns, err = preflightLegacyPolicyColumns(ctx, transaction)
		if err != nil {
			return fmt.Errorf("preflight column authorization: %w", rollbackMigration(transaction, err))
		}
	}

	if _, err := transaction.ExecContext(ctx, contents); err != nil {
		return fmt.Errorf("execute migration %d: %w", version, rollbackMigration(transaction, err))
	}
	if columnAuthorizationMigration {
		if err := backfillLegacyPolicyColumns(ctx, transaction, dialect, legacyColumns); err != nil {
			return fmt.Errorf("backfill column authorization: %w", rollbackMigration(transaction, err))
		}
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit migration %d: %w", version, err)
	}
	return nil
}

type legacyPolicyColumns struct {
	policyID string
	source   string
	tokens   []string
	digest   string
}

func preflightLegacyPolicyColumns(ctx context.Context, transaction *sql.Tx) ([]legacyPolicyColumns, error) {
	rows, err := transaction.QueryContext(ctx, `SELECT id, columns FROM policies WHERE columns IS NOT NULL ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("read legacy policy columns: %w", err)
	}
	defer rows.Close()
	result := make([]legacyPolicyColumns, 0)
	for rows.Next() {
		var policyID, source string
		if err := rows.Scan(&policyID, &source); err != nil {
			return nil, fmt.Errorf("scan legacy policy columns: %w", err)
		}
		if !utf8.ValidString(source) || strings.IndexByte(source, 0) >= 0 {
			return nil, fmt.Errorf("policy %q columns is not canonical UTF-8 text", policyID)
		}
		if strings.TrimSpace(source) == "" {
			continue
		}
		parts := strings.Split(source, ",")
		if len(parts) > 16384 {
			return nil, fmt.Errorf("policy %q columns exceeds staging token limit", policyID)
		}
		tokens := make([]string, len(parts))
		for index, part := range parts {
			tokens[index] = strings.TrimSpace(part)
			if tokens[index] == "" {
				return nil, fmt.Errorf("policy %q columns contains an empty token at ordinal %d", policyID, index+1)
			}
		}
		digest := sha256.Sum256([]byte(source))
		result = append(result, legacyPolicyColumns{
			policyID: policyID, source: source, tokens: tokens, digest: hex.EncodeToString(digest[:]),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate legacy policy columns: %w", err)
	}
	return result, nil
}

func backfillLegacyPolicyColumns(
	ctx context.Context,
	transaction *sql.Tx,
	dialect Dialect,
	legacy []legacyPolicyColumns,
) error {
	statement := `INSERT INTO policy_column_permission_staging
  (policy_id,token_ordinal,legacy_token,requested_usage,source_csv_sha256,bind_status)
VALUES(?,?,?,?,?,'pending')`
	if dialect == DialectPostgres {
		statement = `INSERT INTO policy_column_permission_staging
  (policy_id,token_ordinal,legacy_token,requested_usage,source_csv_sha256,bind_status)
VALUES($1,$2,$3,$4,$5,'pending')`
	}
	inserted := 0
	for _, policy := range legacy {
		for tokenIndex, token := range policy.tokens {
			for _, usage := range []string{"output", "reference"} {
				if _, err := transaction.ExecContext(ctx, statement, policy.policyID, tokenIndex+1, token, usage, policy.digest); err != nil {
					return fmt.Errorf("stage policy %q token %d usage %s: %w", policy.policyID, tokenIndex+1, usage, err)
				}
				inserted++
			}
		}
	}
	var count int
	if err := transaction.QueryRowContext(ctx, `SELECT count(*) FROM policy_column_permission_staging`).Scan(&count); err != nil {
		return fmt.Errorf("count staged policy columns: %w", err)
	}
	if count != inserted {
		return fmt.Errorf("staged policy column count mismatch: expected=%d actual=%d", inserted, count)
	}
	for _, policy := range legacy {
		var countForPolicy int
		var minDigest, maxDigest string
		query := `SELECT count(*),min(source_csv_sha256),max(source_csv_sha256) FROM policy_column_permission_staging WHERE policy_id=?`
		if dialect == DialectPostgres {
			query = `SELECT count(*),min(source_csv_sha256),max(source_csv_sha256) FROM policy_column_permission_staging WHERE policy_id=$1`
		}
		if err := transaction.QueryRowContext(ctx, query, policy.policyID).Scan(&countForPolicy, &minDigest, &maxDigest); err != nil {
			return fmt.Errorf("verify staged policy %q: %w", policy.policyID, err)
		}
		if countForPolicy != len(policy.tokens)*2 || minDigest != policy.digest || maxDigest != policy.digest {
			return fmt.Errorf("staged policy %q digest/count mismatch", policy.policyID)
		}
	}
	return nil
}

func rejectDuplicateMaskRuleKeys(ctx context.Context, transaction *sql.Tx, dialect Dialect) error {
	var query string
	switch dialect {
	case DialectSQLite:
		query = `
SELECT scope_key, column_key, GROUP_CONCAT(id, ',')
FROM (
  SELECT COALESCE(NULLIF(TRIM(datasource_id),''), '') AS scope_key,
         LOWER(TRIM(column_name)) AS column_key,
         id
  FROM mask_rules
  ORDER BY id
)
GROUP BY scope_key, column_key
HAVING COUNT(*) > 1
ORDER BY scope_key, column_key`
	case DialectPostgres:
		query = `
SELECT COALESCE(NULLIF(BTRIM(datasource_id),''), '') AS scope_key,
       LOWER(BTRIM(column_name)) AS column_key,
       STRING_AGG(id, ',' ORDER BY id)
FROM mask_rules
GROUP BY scope_key, column_key
HAVING COUNT(*) > 1
ORDER BY scope_key, column_key`
	default:
		return fmt.Errorf("unsupported metadata dialect %q", dialect)
	}

	rows, err := transaction.QueryContext(ctx, query)
	if err != nil {
		return fmt.Errorf("query duplicate normalized keys: %w", err)
	}
	defer rows.Close()
	conflicts := make([]string, 0)
	for rows.Next() {
		var scope, column, ids string
		if err := rows.Scan(&scope, &column, &ids); err != nil {
			return fmt.Errorf("scan duplicate normalized key: %w", err)
		}
		conflicts = append(conflicts, fmt.Sprintf("scope=%q column=%q row_ids=[%s]", scope, column, ids))
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate duplicate normalized keys: %w", err)
	}
	if len(conflicts) != 0 {
		return fmt.Errorf("duplicate normalized mask rule keys: %s", strings.Join(conflicts, "; "))
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

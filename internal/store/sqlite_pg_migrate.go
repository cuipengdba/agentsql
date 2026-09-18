package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	metadataMigrationLockKey int64 = 0x4153514c4d494701
	auditMigrationLockKey    int64 = 0x4153514c41554401
	migrationInsertBatchSize       = 32
)

// SQLiteToPostgresOptions describes a one-shot migration. PostgreSQL
// connections are supplied by the caller so DSNs never enter this package's
// errors or output.
type SQLiteToPostgresOptions struct {
	SourcePath string
	MetadataDB *sql.DB
	AuditDB    *sql.DB
	Separate   bool
	VerifyHash bool
}

type SQLiteToPostgresTableSummary struct {
	Table                  string
	SourceRows, TargetRows int64
	SHA256                 string
	MinID, MaxID           *int64
}

type SQLiteToPostgresSequenceSummary struct {
	LastValue    int64
	IsCalled     bool
	ExpectedNext int64
}

type SQLiteToPostgresApprovalSummary struct {
	SourceNonNullAuditIDs int64
	TargetNonNullAuditIDs int64
	SourceOrphans         int64
	TargetOrphans         int64
}

type SQLiteToPostgresSummary struct {
	Tables       []SQLiteToPostgresTableSummary
	Sequence     SQLiteToPostgresSequenceSummary
	Approvals    SQLiteToPostgresApprovalSummary
	Verification string
	VerifyHash   bool
}

type migrationColumnKind byte

const (
	migrationText  migrationColumnKind = 'T'
	migrationInt32 migrationColumnKind = 'I'
	migrationInt64 migrationColumnKind = 'L'
	migrationBool  migrationColumnKind = 'B'
	migrationTime  migrationColumnKind = 'Z'
)

type migrationColumn struct {
	name string
	kind migrationColumnKind
}

type migrationTable struct {
	name    string
	columns []migrationColumn
	pkIndex int
	target  migrationTableTarget
}

type migrationTableTarget byte

const (
	migrationTargetMetadata migrationTableTarget = 'M'
	migrationTargetAudit    migrationTableTarget = 'A'
)

var sqliteToPostgresTables = []migrationTable{
	{name: "agents", pkIndex: 0, target: migrationTargetMetadata, columns: []migrationColumn{
		{name: "id", kind: migrationText}, {name: "name", kind: migrationText},
		{name: "owner", kind: migrationText}, {name: "status", kind: migrationText},
		{name: "api_key_hash", kind: migrationText}, {name: "level", kind: migrationText},
		{name: "expires_at", kind: migrationTime}, {name: "created_at", kind: migrationTime},
		{name: "updated_at", kind: migrationTime},
	}},
	{name: "datasources", pkIndex: 0, target: migrationTargetMetadata, columns: []migrationColumn{
		{name: "id", kind: migrationText}, {name: "name", kind: migrationText},
		{name: "db_type", kind: migrationText}, {name: "host", kind: migrationText},
		{name: "port", kind: migrationInt32}, {name: "database", kind: migrationText},
		{name: "username", kind: migrationText}, {name: "password_enc", kind: migrationText},
		{name: "conn_limit", kind: migrationInt32}, {name: "stmt_timeout_ms", kind: migrationInt32},
		{name: "row_limit", kind: migrationInt32}, {name: "created_at", kind: migrationTime},
		{name: "updated_at", kind: migrationTime},
	}},
	{name: "rules", pkIndex: 0, target: migrationTargetMetadata, columns: []migrationColumn{
		{name: "id", kind: migrationText}, {name: "db_type", kind: migrationText},
		{name: "title", kind: migrationText}, {name: "risk_level", kind: migrationInt32},
		{name: "pattern_type", kind: migrationText}, {name: "definition", kind: migrationText},
		{name: "enabled", kind: migrationBool}, {name: "builtin", kind: migrationBool},
		{name: "created_at", kind: migrationTime}, {name: "updated_at", kind: migrationTime},
	}},
	{name: "mask_rules", pkIndex: 0, target: migrationTargetMetadata, columns: []migrationColumn{
		{name: "id", kind: migrationText}, {name: "datasource_id", kind: migrationText},
		{name: "table_name", kind: migrationText}, {name: "column_name", kind: migrationText},
		{name: "sensitive_type", kind: migrationText}, {name: "algo", kind: migrationText},
		{name: "created_at", kind: migrationTime}, {name: "updated_at", kind: migrationTime},
	}},
	{name: "policies", pkIndex: 0, target: migrationTargetMetadata, columns: []migrationColumn{
		{name: "id", kind: migrationText}, {name: "agent_id", kind: migrationText},
		{name: "datasource_id", kind: migrationText}, {name: "object_type", kind: migrationText},
		{name: "object_name", kind: migrationText}, {name: "columns", kind: migrationText},
		{name: "row_filter", kind: migrationText}, {name: "action", kind: migrationText},
		{name: "created_at", kind: migrationTime}, {name: "updated_at", kind: migrationTime},
	}},
	{name: "audit_logs", pkIndex: 0, target: migrationTargetAudit, columns: []migrationColumn{
		{name: "id", kind: migrationInt64}, {name: "ts", kind: migrationTime},
		{name: "agent_id", kind: migrationText}, {name: "datasource_id", kind: migrationText},
		{name: "session_id", kind: migrationText}, {name: "conversation_id", kind: migrationText},
		{name: "mcp_tool", kind: migrationText}, {name: "db_type", kind: migrationText},
		{name: "sql_raw", kind: migrationText}, {name: "sql_norm", kind: migrationText},
		{name: "stmt_type", kind: migrationText}, {name: "objects", kind: migrationText},
		{name: "decision", kind: migrationText}, {name: "rule_hits", kind: migrationText},
		{name: "risk_level", kind: migrationInt32}, {name: "est_rows", kind: migrationInt64},
		{name: "rows_returned", kind: migrationInt32}, {name: "latency_ms", kind: migrationInt64},
		{name: "client_ip", kind: migrationText}, {name: "model_name", kind: migrationText},
		{name: "error_msg", kind: migrationText},
	}},
	{name: "approvals", pkIndex: 0, target: migrationTargetMetadata, columns: []migrationColumn{
		{name: "id", kind: migrationText}, {name: "audit_id", kind: migrationInt64},
		{name: "agent_id", kind: migrationText}, {name: "sql_raw", kind: migrationText},
		{name: "reason", kind: migrationText}, {name: "status", kind: migrationText},
		{name: "approver", kind: migrationText}, {name: "decided_at", kind: migrationTime},
		{name: "created_at", kind: migrationTime}, {name: "updated_at", kind: migrationTime},
	}},
	{name: "notification_settings", pkIndex: 0, target: migrationTargetMetadata, columns: []migrationColumn{
		{name: "id", kind: migrationInt32}, {name: "enabled", kind: migrationBool},
		{name: "queue_size", kind: migrationInt32}, {name: "created_at", kind: migrationTime},
		{name: "updated_at", kind: migrationTime},
	}},
	{name: "notification_channels", pkIndex: 0, target: migrationTargetMetadata, columns: []migrationColumn{
		{name: "id", kind: migrationText}, {name: "position", kind: migrationInt32},
		{name: "enabled", kind: migrationBool}, {name: "kind", kind: migrationText},
		{name: "decisions", kind: migrationText}, {name: "include_sql", kind: migrationBool},
		{name: "allow_private_endpoints", kind: migrationBool}, {name: "webhook_present", kind: migrationBool},
		{name: "webhook_template", kind: migrationText}, {name: "webhook_url_enc", kind: migrationText},
		{name: "webhook_bearer_token_enc", kind: migrationText}, {name: "webhook_headers_enc", kind: migrationText},
		{name: "webhook_secret_enc", kind: migrationText}, {name: "syslog_present", kind: migrationBool},
		{name: "syslog_host", kind: migrationText}, {name: "syslog_port", kind: migrationInt32},
		{name: "syslog_transport", kind: migrationText}, {name: "syslog_facility", kind: migrationInt32},
		{name: "created_at", kind: migrationTime}, {name: "updated_at", kind: migrationTime},
	}},
}

type migrationRawCell struct{ source any }

func (cell *migrationRawCell) Scan(source any) error {
	if bytes, ok := source.([]byte); ok {
		cell.source = append([]byte(nil), bytes...)
	} else {
		cell.source = source
	}
	return nil
}

type migrationCell struct {
	kind  migrationColumnKind
	valid bool
	value any
}

type migrationDigest struct {
	rows       int64
	hash       [sha256.Size]byte
	primaryKey [sha256.Size]byte
	minID      *int64
	maxID      *int64
}

type migrationQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type migrationLock struct {
	connection *sql.Conn
	key        int64
}

// MigrateSQLiteToPostgres copies a latest combined SQLite snapshot into one
// combined PostgreSQL store or a separated metadata/audit pair.
func MigrateSQLiteToPostgres(ctx context.Context, options SQLiteToPostgresOptions) (summary SQLiteToPostgresSummary, resultErr error) {
	if ctx == nil {
		return summary, errors.New("migrate SQLite to PostgreSQL: context is required")
	}
	if strings.TrimSpace(options.SourcePath) == "" {
		return summary, errors.New("migrate SQLite to PostgreSQL: source path is required")
	}
	if options.MetadataDB == nil || (options.Separate && options.AuditDB == nil) {
		return summary, errors.New("migrate SQLite to PostgreSQL: target connection is required")
	}
	if !options.Separate {
		options.AuditDB = options.MetadataDB
	}
	summary.VerifyHash = options.VerifyHash

	sourceDB, err := openMigrationSource(options.SourcePath)
	if err != nil {
		return summary, err
	}
	defer func() {
		if err := sourceDB.Close(); err != nil && resultErr == nil {
			resultErr = errors.New("close SQLite migration source: database error")
		}
	}()
	if err := sourceDB.PingContext(ctx); err != nil {
		return summary, errors.New("open SQLite migration source: database error")
	}
	sourceTx, err := sourceDB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return summary, errors.New("begin SQLite source snapshot: database error")
	}
	defer func() { _ = sourceTx.Rollback() }()
	if err := verifyMigrationSource(ctx, sourceTx); err != nil {
		return summary, err
	}

	if err := prepareMigrationTargets(ctx, options); err != nil {
		return summary, err
	}
	metadataLock, err := acquireMigrationLock(ctx, options.MetadataDB, metadataMigrationLockKey, "metadata")
	if err != nil {
		return summary, err
	}
	defer func() {
		if err := metadataLock.release(ctx, "metadata"); err != nil && resultErr == nil {
			resultErr = err
		}
	}()
	var auditLock *migrationLock
	if options.Separate {
		auditLock, err = acquireMigrationLock(ctx, options.AuditDB, auditMigrationLockKey, "audit")
		if err != nil {
			return summary, err
		}
		defer func() {
			if err := auditLock.release(ctx, "audit"); err != nil && resultErr == nil {
				resultErr = err
			}
		}()
	}

	sourceDigests, err := digestTables(ctx, sourceTx, sqliteToPostgresTables, false)
	if err != nil {
		return summary, err
	}
	sourceRefs, sourceOrphans, err := sourceApprovalReferences(ctx, sourceTx)
	if err != nil {
		return summary, err
	}
	summary.Approvals.SourceNonNullAuditIDs = sourceRefs
	summary.Approvals.SourceOrphans = sourceOrphans

	if options.Separate {
		summary, err = migrateSeparatedTargets(ctx, metadataLock, auditLock, sourceTx, sourceDigests, summary)
	} else {
		summary, err = migrateCombinedTarget(ctx, metadataLock, sourceTx, sourceDigests, summary)
	}
	if err != nil {
		return summary, err
	}
	summary.Verification = "ok"
	return summary, nil
}

func openMigrationSource(path string) (*sql.DB, error) {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return nil, errors.New("open SQLite migration source: source is not a readable database file")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, errors.New("open SQLite migration source: resolve source path")
	}
	// Opaque avoids treating a Windows drive letter as a URI authority
	// (file://D:/...), which SQLite rejects. It also yields a valid file:/ URI
	// on Unix-like systems.
	location := &url.URL{Scheme: "file", Opaque: filepath.ToSlash(absolute)}
	query := location.Query()
	query.Set("mode", "ro")
	query.Add("_pragma", "query_only(1)")
	location.RawQuery = query.Encode()
	database, err := sql.Open("sqlite", location.String())
	if err != nil {
		return nil, errors.New("open SQLite migration source: database error")
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	return database, nil
}

func verifyMigrationSource(ctx context.Context, source *sql.Tx) error {
	latest, err := LatestCombinedSQLiteMigrationVersion()
	if err != nil {
		return errors.New("verify SQLite source schema: migration metadata unavailable")
	}
	var current int
	if err := source.QueryRowContext(ctx, "SELECT COALESCE(MAX(version),0) FROM schema_migrations").Scan(&current); err != nil {
		return errors.New("verify SQLite source schema: schema_migrations unavailable")
	}
	if current != latest {
		return fmt.Errorf("verify SQLite source schema: current=%d latest=%d", current, latest)
	}
	if err := verifyMigrationSourceTables(ctx, source); err != nil {
		return err
	}
	rows, err := source.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return errors.New("verify SQLite source foreign keys: database error")
	}
	if rows.Next() {
		var table, parent string
		var rowID sql.NullInt64
		var foreignKey int64
		if err := rows.Scan(&table, &rowID, &parent, &foreignKey); err != nil {
			_ = rows.Close()
			return errors.New("verify SQLite source foreign keys: database error")
		}
		_ = rows.Close()
		return fmt.Errorf("verify SQLite source foreign keys: violation table=%q rowid=%d foreign_key=%d", table, rowID.Int64, foreignKey)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return errors.New("verify SQLite source foreign keys: database error")
	}
	if err := rows.Close(); err != nil {
		return errors.New("verify SQLite source foreign keys: database error")
	}
	var orphanID string
	err = source.QueryRowContext(ctx, `SELECT a.id FROM approvals AS a LEFT JOIN audit_logs AS l ON l.id=a.audit_id WHERE a.audit_id IS NOT NULL AND l.id IS NULL ORDER BY a.id LIMIT 1`).Scan(&orphanID)
	if err == nil {
		return fmt.Errorf("verify SQLite source approvals: orphan table=approvals column=audit_id primary_key=%q", orphanID)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return errors.New("verify SQLite source approvals: database error")
	}
	return nil
}

func verifyMigrationSourceTables(ctx context.Context, source *sql.Tx) error {
	for _, table := range sqliteToPostgresTables {
		var tableCount int
		if err := source.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", table.name).Scan(&tableCount); err != nil || tableCount != 1 {
			return fmt.Errorf("verify SQLite source schema: required table=%s is unavailable", table.name)
		}
		rows, err := source.QueryContext(ctx, "PRAGMA table_info("+quoteMigrationIdentifier(table.name)+")")
		if err != nil {
			return fmt.Errorf("verify SQLite source schema: inspect table=%s", table.name)
		}
		index := 0
		for rows.Next() {
			var columnID, notNull, primaryKey int
			var name, columnType string
			var defaultValue sql.NullString
			if err := rows.Scan(&columnID, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
				_ = rows.Close()
				return fmt.Errorf("verify SQLite source schema: inspect table=%s", table.name)
			}
			if index >= len(table.columns) || columnID != index || name != table.columns[index].name {
				_ = rows.Close()
				return fmt.Errorf("verify SQLite source schema: columns differ for table=%s", table.name)
			}
			index++
		}
		rowErr, closeErr := rows.Err(), rows.Close()
		if rowErr != nil || closeErr != nil || index != len(table.columns) {
			return fmt.Errorf("verify SQLite source schema: columns differ for table=%s", table.name)
		}
	}
	return verifyMigrationManifestCoverage(ctx, source)
}

// verifyMigrationManifestCoverage makes a schema addition fail closed until it
// is assigned column types and a target in sqliteToPostgresTables. This avoids
// the historical failure mode where a new metadata table was silently skipped.
func verifyMigrationManifestCoverage(ctx context.Context, source *sql.Tx) error {
	rows, err := source.QueryContext(ctx, `
SELECT name
FROM sqlite_master
WHERE type='table' AND name NOT LIKE 'sqlite_%' AND name <> 'schema_migrations'
ORDER BY name COLLATE BINARY`)
	if err != nil {
		return errors.New("verify SQLite source migration manifest: database error")
	}
	defer rows.Close()
	actual := make(map[string]struct{}, len(sqliteToPostgresTables))
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return errors.New("verify SQLite source migration manifest: database error")
		}
		actual[name] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return errors.New("verify SQLite source migration manifest: database error")
	}
	if len(actual) != len(sqliteToPostgresTables) {
		return errors.New("verify SQLite source migration manifest: business table list differs")
	}
	for _, table := range sqliteToPostgresTables {
		if _, exists := actual[table.name]; !exists {
			return fmt.Errorf("verify SQLite source migration manifest: required table=%s is unavailable", table.name)
		}
	}
	return nil
}

func prepareMigrationTargets(ctx context.Context, options SQLiteToPostgresOptions) error {
	if err := options.MetadataDB.PingContext(ctx); err != nil {
		return targetMigrationError("ping metadata migration target")
	}
	if err := MigrateMetadata(ctx, options.MetadataDB, DialectPostgres, options.Separate); err != nil {
		return targetMigrationError("migrate metadata target")
	}
	current, latest, err := MetadataMigrationVersions(ctx, options.MetadataDB, DialectPostgres, options.Separate)
	if err != nil || current != latest {
		return targetMigrationError("verify metadata target schema version")
	}
	if !options.Separate {
		return nil
	}
	if err := options.AuditDB.PingContext(ctx); err != nil {
		return targetMigrationError("ping audit migration target")
	}
	if err := MigrateAudit(ctx, options.AuditDB, DialectPostgres); err != nil {
		return targetMigrationError("migrate audit target")
	}
	current, latest, err = AuditMigrationVersions(ctx, options.AuditDB, DialectPostgres)
	if err != nil || current != latest {
		return targetMigrationError("verify audit target schema version")
	}
	return nil
}

func acquireMigrationLock(ctx context.Context, database *sql.DB, key int64, role string) (*migrationLock, error) {
	connection, err := database.Conn(ctx)
	if err != nil {
		return nil, targetMigrationError("acquire " + role + " migration lock")
	}
	var acquired bool
	if err := connection.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", key).Scan(&acquired); err != nil {
		_ = connection.Close()
		return nil, targetMigrationError("acquire " + role + " migration lock")
	}
	if !acquired {
		_ = connection.Close()
		return nil, fmt.Errorf("acquire %s migration lock: concurrent migration is running", role)
	}
	return &migrationLock{connection: connection, key: key}, nil
}

func (lock *migrationLock) release(ctx context.Context, role string) error {
	if lock == nil || lock.connection == nil {
		return nil
	}
	var released bool
	err := lock.connection.QueryRowContext(ctx, "SELECT pg_advisory_unlock($1)", lock.key).Scan(&released)
	closeErr := lock.connection.Close()
	if err != nil || closeErr != nil || !released {
		return targetMigrationError("release " + role + " migration lock")
	}
	return nil
}

func migrateCombinedTarget(ctx context.Context, lock *migrationLock, source *sql.Tx, sourceDigests map[string]migrationDigest, summary SQLiteToPostgresSummary) (SQLiteToPostgresSummary, error) {
	target, err := lock.connection.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return summary, targetMigrationError("begin combined target transaction")
	}
	committed := false
	defer func() {
		if !committed {
			_ = target.Rollback()
		}
	}()
	targetDigests, err := digestTables(ctx, target, sqliteToPostgresTables, true)
	if err != nil {
		return summary, err
	}
	state := classifyMigrationGroup(sqliteToPostgresTables, sourceDigests, targetDigests)
	if state == migrationConflict {
		return summary, errors.New("classify combined target: conflict (target business tables are non-empty and not exact)")
	}
	if state == migrationEmpty {
		for _, table := range sqliteToPostgresTables {
			if err := copyMigrationTable(ctx, source, target, table); err != nil {
				return summary, err
			}
		}
	}
	verified, err := digestTables(ctx, target, sqliteToPostgresTables, true)
	if err != nil {
		return summary, err
	}
	if !digestsEqual(sqliteToPostgresTables, sourceDigests, verified) {
		return summary, errors.New("verify combined target: copied data is not exact")
	}
	refs, orphans, err := combinedApprovalReferences(ctx, target)
	if err != nil {
		return summary, err
	}
	summary.Approvals.TargetNonNullAuditIDs, summary.Approvals.TargetOrphans = refs, orphans
	if orphans != 0 {
		return summary, errors.New("verify combined target approvals: orphan audit references")
	}
	sequence, err := alignAuditSequence(ctx, target)
	if err != nil {
		return summary, err
	}
	if err := target.Commit(); err != nil {
		return summary, targetMigrationError("commit combined target transaction")
	}
	committed = true
	summary.Sequence = sequence
	summary.Tables = makeTableSummaries(sourceDigests, verified)
	return summary, nil
}

func migrateSeparatedTargets(ctx context.Context, metadataLock, auditLock *migrationLock, source *sql.Tx, sourceDigests map[string]migrationDigest, summary SQLiteToPostgresSummary) (SQLiteToPostgresSummary, error) {
	metadataTables := migrationTablesForTarget(migrationTargetMetadata)
	auditTables := migrationTablesForTarget(migrationTargetAudit)
	metadataTx, err := metadataLock.connection.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return summary, targetMigrationError("begin metadata target transaction")
	}
	metadataCommitted := false
	defer func() {
		if !metadataCommitted {
			_ = metadataTx.Rollback()
		}
	}()
	auditTx, err := auditLock.connection.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return summary, targetMigrationError("begin audit target transaction")
	}
	auditCommitted := false
	defer func() {
		if !auditCommitted {
			_ = auditTx.Rollback()
		}
	}()

	metadataDigests, err := digestTables(ctx, metadataTx, metadataTables, true)
	if err != nil {
		return summary, err
	}
	auditDigests, err := digestTables(ctx, auditTx, auditTables, true)
	if err != nil {
		return summary, err
	}
	metadataState := classifyMigrationGroup(metadataTables, sourceDigests, metadataDigests)
	auditState := classifyMigrationGroup(auditTables, sourceDigests, auditDigests)
	if metadataState == migrationConflict {
		return summary, errors.New("classify metadata target: conflict (target business tables are non-empty and not exact)")
	}
	if auditState == migrationConflict {
		return summary, errors.New("classify audit target: conflict (target business tables are non-empty and not exact)")
	}
	if metadataState == migrationEmpty {
		for _, table := range metadataTables {
			if err := copyMigrationTable(ctx, source, metadataTx, table); err != nil {
				return summary, err
			}
		}
	}
	if auditState == migrationEmpty {
		for _, table := range auditTables {
			if err := copyMigrationTable(ctx, source, auditTx, table); err != nil {
				return summary, err
			}
		}
	}
	verifiedMetadata, err := digestTables(ctx, metadataTx, metadataTables, true)
	if err != nil {
		return summary, err
	}
	verifiedAudit, err := digestTables(ctx, auditTx, auditTables, true)
	if err != nil {
		return summary, err
	}
	if !digestsEqual(metadataTables, sourceDigests, verifiedMetadata) || !digestsEqual(auditTables, sourceDigests, verifiedAudit) {
		return summary, errors.New("verify separated targets: copied data is not exact")
	}
	refs, orphans, err := separatedApprovalReferences(ctx, metadataTx, auditTx)
	if err != nil {
		return summary, err
	}
	summary.Approvals.TargetNonNullAuditIDs, summary.Approvals.TargetOrphans = refs, orphans
	if orphans != 0 {
		return summary, errors.New("verify separated target approvals: orphan audit references")
	}
	sequence, err := alignAuditSequence(ctx, auditTx)
	if err != nil {
		return summary, err
	}
	if err := auditTx.Commit(); err != nil {
		return summary, targetMigrationError("commit audit target transaction")
	}
	auditCommitted = true
	if err := metadataTx.Commit(); err != nil {
		return summary, targetMigrationError("commit metadata target transaction")
	}
	metadataCommitted = true
	allVerified := make(map[string]migrationDigest, len(verifiedMetadata)+len(verifiedAudit))
	for name, digest := range verifiedMetadata {
		allVerified[name] = digest
	}
	for name, digest := range verifiedAudit {
		allVerified[name] = digest
	}
	summary.Sequence = sequence
	summary.Tables = makeTableSummaries(sourceDigests, allVerified)
	return summary, nil
}

type migrationGroupState string

const (
	migrationEmpty    migrationGroupState = "empty"
	migrationExact    migrationGroupState = "exact"
	migrationConflict migrationGroupState = "conflict"
)

func classifyMigrationGroup(tables []migrationTable, source, target map[string]migrationDigest) migrationGroupState {
	empty := true
	for _, table := range tables {
		if target[table.name].rows != 0 {
			empty = false
			break
		}
	}
	if empty {
		return migrationEmpty
	}
	if digestsEqual(tables, source, target) {
		return migrationExact
	}
	return migrationConflict
}

func digestTables(ctx context.Context, queryer migrationQueryer, tables []migrationTable, postgres bool) (map[string]migrationDigest, error) {
	digests := make(map[string]migrationDigest, len(tables))
	for _, table := range tables {
		digest, err := digestMigrationTable(ctx, queryer, table, postgres)
		if err != nil {
			return nil, err
		}
		digests[table.name] = digest
	}
	return digests, nil
}

func digestMigrationTable(ctx context.Context, queryer migrationQueryer, table migrationTable, postgres bool) (migrationDigest, error) {
	rows, err := queryer.QueryContext(ctx, migrationSelectSQL(table, postgres))
	if err != nil {
		if postgres {
			return migrationDigest{}, targetMigrationError("read target table " + table.name)
		}
		return migrationDigest{}, fmt.Errorf("read source table %s: database error", table.name)
	}
	defer rows.Close()
	rowHash, primaryKeyHash := sha256.New(), sha256.New()
	digest := migrationDigest{}
	for rows.Next() {
		cells, err := scanMigrationRow(rows, table)
		if err != nil {
			return migrationDigest{}, err
		}
		writeCanonicalRow(rowHash, cells)
		writeCanonicalCell(primaryKeyHash, cells[table.pkIndex])
		digest.rows++
		if table.name == "audit_logs" {
			id := cells[table.pkIndex].value.(int64)
			if digest.minID == nil || id < *digest.minID {
				value := id
				digest.minID = &value
			}
			if digest.maxID == nil || id > *digest.maxID {
				value := id
				digest.maxID = &value
			}
		}
	}
	if err := rows.Err(); err != nil {
		if postgres {
			return migrationDigest{}, targetMigrationError("read target table " + table.name)
		}
		return migrationDigest{}, fmt.Errorf("read source table %s: database error", table.name)
	}
	copy(digest.hash[:], rowHash.Sum(nil))
	copy(digest.primaryKey[:], primaryKeyHash.Sum(nil))
	return digest, nil
}

func scanMigrationRow(scanner interface{ Scan(...any) error }, table migrationTable) ([]migrationCell, error) {
	raw := make([]migrationRawCell, len(table.columns))
	destinations := make([]any, len(raw))
	for index := range raw {
		destinations[index] = &raw[index]
	}
	if err := scanner.Scan(destinations...); err != nil {
		return nil, fmt.Errorf("scan table %s row: database error", table.name)
	}
	cells := make([]migrationCell, len(raw))
	primaryKey := "<unknown>"
	for _, index := range []int{table.pkIndex} {
		cell, err := normalizeMigrationCell(table.columns[index].kind, raw[index].source)
		if err == nil && cell.valid {
			primaryKey = fmt.Sprint(cell.value)
		}
	}
	for index, column := range table.columns {
		cell, err := normalizeMigrationCell(column.kind, raw[index].source)
		if err != nil {
			return nil, fmt.Errorf("convert table=%s column=%s primary_key=%q: %s", table.name, column.name, primaryKey, err)
		}
		if index == table.pkIndex && !cell.valid {
			return nil, fmt.Errorf("convert table=%s column=%s: primary key is null", table.name, column.name)
		}
		cells[index] = cell
	}
	return cells, nil
}

func normalizeMigrationCell(kind migrationColumnKind, source any) (migrationCell, error) {
	if source == nil {
		return migrationCell{kind: kind}, nil
	}
	cell := migrationCell{kind: kind, valid: true}
	switch kind {
	case migrationText:
		var value string
		switch typed := source.(type) {
		case string:
			value = typed
		case []byte:
			value = string(typed)
		default:
			return migrationCell{}, errors.New("invalid text type")
		}
		if !utf8.ValidString(value) {
			return migrationCell{}, errors.New("invalid UTF-8 text")
		}
		if strings.IndexByte(value, 0) >= 0 {
			return migrationCell{}, errors.New("text contains NUL")
		}
		cell.value = value
	case migrationInt32, migrationInt64:
		value, ok := migrationInteger(source)
		if !ok {
			return migrationCell{}, errors.New("invalid integer type")
		}
		if kind == migrationInt32 && (value < math.MinInt32 || value > math.MaxInt32) {
			return migrationCell{}, errors.New("integer exceeds PostgreSQL int32 range")
		}
		cell.value = value
	case migrationBool:
		if value, ok := source.(bool); ok {
			cell.value = value
			break
		}
		value, ok := migrationInteger(source)
		if !ok || (value != 0 && value != 1) {
			return migrationCell{}, errors.New("boolean must be SQLite integer 0 or 1")
		}
		cell.value = value == 1
	case migrationTime:
		var timestamp databaseTimestamp
		if err := timestamp.Scan(source); err != nil || !timestamp.valid {
			return migrationCell{}, errors.New("invalid timestamp")
		}
		cell.value = timestamp.time.UTC().Truncate(time.Microsecond)
	default:
		return migrationCell{}, errors.New("unsupported migration type")
	}
	return cell, nil
}

func migrationInteger(source any) (int64, bool) {
	switch value := source.(type) {
	case int64:
		return value, true
	case int:
		return int64(value), true
	case int32:
		return int64(value), true
	default:
		return 0, false
	}
}

func writeCanonicalRow(destination hash.Hash, cells []migrationCell) {
	for _, cell := range cells {
		writeCanonicalCell(destination, cell)
	}
}

func writeCanonicalCell(destination hash.Hash, cell migrationCell) {
	_, _ = destination.Write([]byte{byte(cell.kind)})
	if !cell.valid {
		_, _ = destination.Write([]byte{0})
		writeCanonicalLength(destination, 0)
		return
	}
	_, _ = destination.Write([]byte{1})
	var content string
	switch cell.kind {
	case migrationText:
		content = cell.value.(string)
	case migrationInt32, migrationInt64:
		content = strconv.FormatInt(cell.value.(int64), 10)
	case migrationBool:
		if cell.value.(bool) {
			content = "1"
		} else {
			content = "0"
		}
	case migrationTime:
		content = cell.value.(time.Time).UTC().Format("2006-01-02T15:04:05.000000Z")
	}
	writeCanonicalLength(destination, uint64(len(content)))
	_, _ = destination.Write([]byte(content))
}

func writeCanonicalLength(destination hash.Hash, length uint64) {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], length)
	_, _ = destination.Write(encoded[:])
}

func copyMigrationTable(ctx context.Context, source *sql.Tx, target *sql.Tx, table migrationTable) error {
	rows, err := source.QueryContext(ctx, migrationSelectSQL(table, false))
	if err != nil {
		return fmt.Errorf("copy source table %s: database error", table.name)
	}
	defer rows.Close()
	statements := make(map[int]*sql.Stmt, 2)
	defer func() {
		for _, statement := range statements {
			_ = statement.Close()
		}
	}()
	batch := make([][]any, 0, migrationInsertBatchSize)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		statement := statements[len(batch)]
		if statement == nil {
			prepared, err := target.PrepareContext(ctx, migrationInsertSQL(table, len(batch)))
			if err != nil {
				return targetMigrationError("prepare target insert for " + table.name)
			}
			statements[len(batch)] = prepared
			statement = prepared
		}
		arguments := make([]any, 0, len(batch)*len(table.columns))
		for _, row := range batch {
			arguments = append(arguments, row...)
		}
		if _, err := statement.ExecContext(ctx, arguments...); err != nil {
			return targetMigrationError("insert target table " + table.name)
		}
		batch = batch[:0]
		return nil
	}
	for rows.Next() {
		cells, err := scanMigrationRow(rows, table)
		if err != nil {
			return err
		}
		arguments := make([]any, len(cells))
		for index, cell := range cells {
			if cell.valid {
				arguments[index] = cell.value
			}
		}
		batch = append(batch, arguments)
		if len(batch) == migrationInsertBatchSize {
			if err := flush(); err != nil {
				return err
			}
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("copy source table %s: database error", table.name)
	}
	return flush()
}

func migrationSelectSQL(table migrationTable, postgres bool) string {
	columns := make([]string, len(table.columns))
	for index, column := range table.columns {
		columns[index] = quoteMigrationIdentifier(column.name)
	}
	prefix := quoteMigrationIdentifier(table.name)
	if postgres {
		prefix = `"public".` + prefix
	}
	order := quoteMigrationIdentifier(table.columns[table.pkIndex].name)
	if table.columns[table.pkIndex].kind == migrationText {
		if postgres {
			order += ` COLLATE "C"`
		} else {
			order += " COLLATE BINARY"
		}
	}
	return "SELECT " + strings.Join(columns, ",") + " FROM " + prefix + " ORDER BY " + order
}

func migrationInsertSQL(table migrationTable, rowCount int) string {
	columns := make([]string, len(table.columns))
	for index, column := range table.columns {
		columns[index] = quoteMigrationIdentifier(column.name)
	}
	values := make([]string, rowCount)
	parameter := 1
	for row := range rowCount {
		parameters := make([]string, len(table.columns))
		for column := range table.columns {
			parameters[column] = "$" + strconv.Itoa(parameter)
			parameter++
		}
		values[row] = "(" + strings.Join(parameters, ",") + ")"
	}
	override := ""
	if table.name == "audit_logs" {
		override = " OVERRIDING SYSTEM VALUE"
	}
	return "INSERT INTO \"public\"." + quoteMigrationIdentifier(table.name) + " (" + strings.Join(columns, ",") + ")" + override + " VALUES " + strings.Join(values, ",")
}

func quoteMigrationIdentifier(identifier string) string { return `"` + identifier + `"` }

func digestsEqual(tables []migrationTable, left, right map[string]migrationDigest) bool {
	for _, table := range tables {
		first, firstOK := left[table.name]
		second, secondOK := right[table.name]
		if !firstOK || !secondOK || first.rows != second.rows || first.hash != second.hash || first.primaryKey != second.primaryKey {
			return false
		}
	}
	return true
}

func migrationTablesForTarget(target migrationTableTarget) []migrationTable {
	var result []migrationTable
	for _, table := range sqliteToPostgresTables {
		if table.target == target {
			result = append(result, table)
		}
	}
	return result
}

func makeTableSummaries(source, target map[string]migrationDigest) []SQLiteToPostgresTableSummary {
	result := make([]SQLiteToPostgresTableSummary, 0, len(sqliteToPostgresTables))
	for _, table := range sqliteToPostgresTables {
		sourceDigest, targetDigest := source[table.name], target[table.name]
		result = append(result, SQLiteToPostgresTableSummary{
			Table: table.name, SourceRows: sourceDigest.rows, TargetRows: targetDigest.rows,
			SHA256: hex.EncodeToString(targetDigest.hash[:]), MinID: targetDigest.minID, MaxID: targetDigest.maxID,
		})
	}
	return result
}

func sourceApprovalReferences(ctx context.Context, source migrationQueryer) (int64, int64, error) {
	var references, orphans int64
	if err := source.QueryRowContext(ctx, "SELECT COUNT(*) FROM approvals WHERE audit_id IS NOT NULL").Scan(&references); err != nil {
		return 0, 0, errors.New("count SQLite source approval references: database error")
	}
	if err := source.QueryRowContext(ctx, `SELECT COUNT(*) FROM approvals AS a LEFT JOIN audit_logs AS l ON l.id=a.audit_id WHERE a.audit_id IS NOT NULL AND l.id IS NULL`).Scan(&orphans); err != nil {
		return 0, 0, errors.New("count SQLite source approval orphans: database error")
	}
	return references, orphans, nil
}

func combinedApprovalReferences(ctx context.Context, target migrationQueryer) (int64, int64, error) {
	var references, orphans int64
	if err := target.QueryRowContext(ctx, `SELECT COUNT(*) FROM "public"."approvals" WHERE "audit_id" IS NOT NULL`).Scan(&references); err != nil {
		return 0, 0, targetMigrationError("count target approval references")
	}
	if err := target.QueryRowContext(ctx, `SELECT COUNT(*) FROM "public"."approvals" AS a LEFT JOIN "public"."audit_logs" AS l ON l."id"=a."audit_id" WHERE a."audit_id" IS NOT NULL AND l."id" IS NULL`).Scan(&orphans); err != nil {
		return 0, 0, targetMigrationError("count target approval orphans")
	}
	return references, orphans, nil
}

func separatedApprovalReferences(ctx context.Context, metadata, audit migrationQueryer) (int64, int64, error) {
	approvalRows, err := metadata.QueryContext(ctx, `SELECT "audit_id" FROM "public"."approvals" WHERE "audit_id" IS NOT NULL ORDER BY "audit_id"`)
	if err != nil {
		return 0, 0, targetMigrationError("read metadata approval references")
	}
	defer approvalRows.Close()
	auditRows, err := audit.QueryContext(ctx, `SELECT "id" FROM "public"."audit_logs" ORDER BY "id"`)
	if err != nil {
		return 0, 0, targetMigrationError("read audit identifiers")
	}
	defer auditRows.Close()
	var references, orphans int64
	var auditID int64
	auditAvailable := auditRows.Next()
	if auditAvailable {
		if err := auditRows.Scan(&auditID); err != nil {
			return 0, 0, targetMigrationError("scan audit identifiers")
		}
	}
	for approvalRows.Next() {
		var approvalAuditID int64
		if err := approvalRows.Scan(&approvalAuditID); err != nil {
			return 0, 0, targetMigrationError("scan metadata approval references")
		}
		references++
		for auditAvailable && auditID < approvalAuditID {
			auditAvailable = auditRows.Next()
			if auditAvailable {
				if err := auditRows.Scan(&auditID); err != nil {
					return 0, 0, targetMigrationError("scan audit identifiers")
				}
			}
		}
		if !auditAvailable || auditID != approvalAuditID {
			orphans++
		}
	}
	if approvalRows.Err() != nil || auditRows.Err() != nil {
		return 0, 0, targetMigrationError("merge target approval references")
	}
	return references, orphans, nil
}

func alignAuditSequence(ctx context.Context, target *sql.Tx) (SQLiteToPostgresSequenceSummary, error) {
	const alignSQL = `SELECT setval(pg_get_serial_sequence('public.audit_logs','id'), GREATEST(COALESCE((SELECT MAX("id") FROM "public"."audit_logs"),1),1), COALESCE((SELECT MAX("id") FROM "public"."audit_logs"),0)>=1)`
	var ignored int64
	if err := target.QueryRowContext(ctx, alignSQL).Scan(&ignored); err != nil {
		return SQLiteToPostgresSequenceSummary{}, targetMigrationError("align audit_logs identity sequence")
	}
	var summary SQLiteToPostgresSequenceSummary
	if err := target.QueryRowContext(ctx, `SELECT "last_value","is_called" FROM "public"."audit_logs_id_seq"`).Scan(&summary.LastValue, &summary.IsCalled); err != nil {
		return summary, targetMigrationError("verify audit_logs identity sequence")
	}
	summary.ExpectedNext = expectedSequenceNext(summary.LastValue, summary.IsCalled)
	return summary, nil
}

func expectedSequenceNext(lastValue int64, isCalled bool) int64 {
	if isCalled {
		return lastValue + 1
	}
	return lastValue
}

func targetMigrationError(action string) error {
	return fmt.Errorf("%s: driver=postgres", action)
}

package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSQLiteToPostgresTableOrderIsFrozen(t *testing.T) {
	names := make([]string, 0, len(sqliteToPostgresTables))
	for _, table := range sqliteToPostgresTables {
		names = append(names, table.name)
	}
	require.Equal(t, []string{
		"agents", "datasources", "rules", "mask_rules", "policies", "relation_policy_bindings",
		"policy_column_permission_staging", "policy_column_permissions", "audit_logs", "approvals",
		"notification_settings", "notification_channels", "redaction_key_versions", "management_audit_outbox",
		"admin_access_revocations", "admin_refresh_families", "admin_refresh_tokens",
		"tenants", "users", "permissions", "roles", "user_roles", "role_permissions", "role_inheritance",
		"b5_sessions", "b5_transactions", "b5_dml_grants", "b5_result_receipts", "b5_tx_events",
	}, names)
}

func TestSQLiteToPostgresManifestIncludesDiscoveryDraftColumns(t *testing.T) {
	maskRuleTable := sqliteToPostgresTableByName(t, "mask_rules")
	require.Equal(t, []string{
		"id", "datasource_id", "table_name", "column_name", "sensitive_type", "algo",
		"created_at", "updated_at", "enabled", "range_bucket_width", "range_bucket_offset",
		"range_granularity", "schema_name",
	}, migrationColumnNames(maskRuleTable))
	require.Equal(t, migrationInt32, maskRuleTable.columns[9].kind)
	require.Equal(t, migrationInt32, maskRuleTable.columns[10].kind)
	require.Equal(t, migrationText, maskRuleTable.columns[11].kind)
	require.Equal(t, migrationText, maskRuleTable.columns[12].kind)
	require.Equal(t, []string{
		"id", "ts", "agent_id", "datasource_id", "session_id", "conversation_id",
		"mcp_tool", "db_type", "sql_raw", "sql_norm", "stmt_type", "objects",
		"decision", "rule_hits", "risk_level", "est_rows", "rows_returned", "latency_ms",
		"client_ip", "model_name", "error_msg", "action", "actor_type", "actor_id", "details_json", "error_code", "event_uuid",
	}, migrationColumnNames(sqliteToPostgresTableByName(t, "audit_logs")))
	require.Equal(t, []string{
		"id", "state", "commitment", "label", "config_revision", "created_at",
		"updated_at", "activated_at", "retired_at",
	}, migrationColumnNames(sqliteToPostgresTableByName(t, "redaction_key_versions")))
	require.Equal(t, []string{
		"event_uuid", "action", "actor_type", "actor_id", "details_json", "created_at",
		"attempts", "claimed_by", "claimed_at", "last_error", "next_attempt_at", "delivered_at",
	}, migrationColumnNames(sqliteToPostgresTableByName(t, "management_audit_outbox")))
}

func TestSQLiteToPostgresAuditDigestSelectsEventUUIDLast(t *testing.T) {
	audit := sqliteToPostgresTableByName(t, "audit_logs")
	require.Equal(t, "event_uuid", audit.columns[len(audit.columns)-1].name)
	for _, postgres := range []bool{false, true} {
		selectSQL := migrationSelectSQL(audit, postgres)
		require.Contains(t, selectSQL, `"error_code","event_uuid"`)
		require.Less(t, strings.Index(selectSQL, `"error_code"`), strings.Index(selectSQL, `"event_uuid"`))
	}
}

func migrationColumnNames(table migrationTable) []string {
	names := make([]string, len(table.columns))
	for index, column := range table.columns {
		names[index] = column.name
	}
	return names
}

func TestNormalizeMigrationCell(t *testing.T) {
	t.Run("boolean", func(t *testing.T) {
		zero, err := normalizeMigrationCell(migrationBool, int64(0))
		require.NoError(t, err)
		require.Equal(t, false, zero.value)
		one, err := normalizeMigrationCell(migrationBool, int64(1))
		require.NoError(t, err)
		require.Equal(t, true, one.value)
		_, err = normalizeMigrationCell(migrationBool, int64(2))
		require.ErrorContains(t, err, "0 or 1")
	})

	t.Run("timestamp UTC microseconds", func(t *testing.T) {
		withoutZone, err := normalizeMigrationCell(migrationTime, "2026-09-17 12:34:56.123456789")
		require.NoError(t, err)
		require.Equal(t, time.Date(2026, 9, 17, 12, 34, 56, 123456000, time.UTC), withoutZone.value)
		withZone, err := normalizeMigrationCell(migrationTime, "2026-09-17T20:34:56.987654321+08:00")
		require.NoError(t, err)
		require.Equal(t, time.Date(2026, 9, 17, 12, 34, 56, 987654000, time.UTC), withZone.value)
	})

	t.Run("integer ranges", func(t *testing.T) {
		_, err := normalizeMigrationCell(migrationInt32, int64(1<<31))
		require.ErrorContains(t, err, "int32")
		value, err := normalizeMigrationCell(migrationInt64, int64(1<<62))
		require.NoError(t, err)
		require.Equal(t, int64(1<<62), value.value)
	})

	t.Run("NULL and empty string differ", func(t *testing.T) {
		nullValue, err := normalizeMigrationCell(migrationText, nil)
		require.NoError(t, err)
		require.False(t, nullValue.valid)
		emptyValue, err := normalizeMigrationCell(migrationText, "")
		require.NoError(t, err)
		require.True(t, emptyValue.valid)
		require.Empty(t, emptyValue.value)
	})

	t.Run("unsafe text", func(t *testing.T) {
		_, err := normalizeMigrationCell(migrationText, []byte{0xff})
		require.ErrorContains(t, err, "UTF-8")
		_, err = normalizeMigrationCell(migrationText, "safe\x00unsafe")
		require.ErrorContains(t, err, "NUL")
	})
}

func TestCanonicalMigrationEncodingIsDeterministicAndUnambiguous(t *testing.T) {
	cells := []migrationCell{
		{kind: migrationText, valid: true, value: "a|b"},
		{kind: migrationText},
		{kind: migrationText, valid: true, value: ""},
		{kind: migrationBool, valid: true, value: true},
		{kind: migrationInt64, valid: true, value: int64(42)},
		{kind: migrationTime, valid: true, value: time.Date(2026, 9, 17, 1, 2, 3, 456789999, time.FixedZone("x", 8*3600))},
	}
	first, second := sha256.New(), sha256.New()
	writeCanonicalRow(first, cells)
	writeCanonicalRow(second, cells)
	require.Equal(t, first.Sum(nil), second.Sum(nil))

	different := sha256.New()
	changed := append([]migrationCell(nil), cells...)
	changed[1], changed[2] = changed[2], changed[1]
	writeCanonicalRow(different, changed)
	require.NotEqual(t, first.Sum(nil), different.Sum(nil))

	large := sha256.New()
	writeCanonicalCell(large, migrationCell{kind: migrationText, valid: true, value: strings.Repeat("x", 2<<20)})
	require.Len(t, large.Sum(nil), sha256.Size)
}

func TestClassifyMigrationGroup(t *testing.T) {
	tables := []migrationTable{
		sqliteToPostgresTableByName(t, "agents"),
		sqliteToPostgresTableByName(t, "datasources"),
	}
	source := map[string]migrationDigest{
		"agents":      {rows: 1, hash: [32]byte{1}, primaryKey: [32]byte{2}},
		"datasources": {rows: 1, hash: [32]byte{3}, primaryKey: [32]byte{4}},
	}
	empty := map[string]migrationDigest{"agents": {}, "datasources": {}}
	require.Equal(t, migrationEmpty, classifyMigrationGroup(tables, source, empty))
	exact := map[string]migrationDigest{"agents": source["agents"], "datasources": source["datasources"]}
	require.Equal(t, migrationExact, classifyMigrationGroup(tables, source, exact))
	conflict := map[string]migrationDigest{"agents": source["agents"], "datasources": {rows: 1, hash: [32]byte{9}}}
	require.Equal(t, migrationConflict, classifyMigrationGroup(tables, source, conflict))
}

func TestSQLiteToPostgresMigrationManifestCoversMetadataTables(t *testing.T) {
	metadataTables := migrationTablesForTarget(migrationTargetMetadata)
	auditTables := migrationTablesForTarget(migrationTargetAudit)
	require.NotEmpty(t, metadataTables)
	require.Equal(t, []migrationTable{sqliteToPostgresTableByName(t, "audit_logs")}, auditTables)
	require.Contains(t, migrationTableNames(metadataTables), "notification_settings")
	require.Contains(t, migrationTableNames(metadataTables), "notification_channels")

	ctx := context.Background()
	t.Run("latest source with derived audit chain objects", func(t *testing.T) {
		database := openSQLiteMigrationTestDB(t)
		require.NoError(t, Migrate(ctx, database, DialectSQLite))
		manifestAuditColumns := migrationColumnNames(sqliteToPostgresTableByName(t, "audit_logs"))
		actualAuditColumns := tableColumnNames(t, database, "audit_logs")
		require.Equal(t, manifestAuditColumns, actualAuditColumns[:len(manifestAuditColumns)])
		require.ElementsMatch(t, auditChainColumnNames, actualAuditColumns[len(manifestAuditColumns):])
		require.ElementsMatch(t, []string{"chain_state", "chain_verification"}, []string{
			derivedTableName(t, database, "chain_state"),
			derivedTableName(t, database, "chain_verification"),
		})

		_, err := database.ExecContext(ctx, `INSERT INTO audit_logs(decision) VALUES('allow')`)
		require.NoError(t, err)
		before := digestSQLiteMigrationSource(t, ctx, database)
		_, err = database.ExecContext(ctx, `
UPDATE audit_logs
SET chain_seq=1, prev_hash='prev', self_hash='self', chain_key_version=2, chain_format_version=3;
UPDATE chain_state SET head_seq=1, head_id=1, head_hash='self' WHERE chain_id='management';
UPDATE chain_verification SET result='ok', last_verified_head_seq=1 WHERE chain_id='management'`)
		require.NoError(t, err)
		after := digestSQLiteMigrationSource(t, ctx, database)
		require.Equal(t, before, after, "derived chain data must not affect copied table digests")

		transaction, err := database.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		require.NoError(t, err)
		require.NoError(t, verifyMigrationSource(ctx, transaction))
		require.NoError(t, transaction.Rollback())
	})

	t.Run("unknown business table remains fail closed", func(t *testing.T) {
		database := openSQLiteMigrationTestDB(t)
		require.NoError(t, Migrate(ctx, database, DialectSQLite))
		_, err := database.ExecContext(ctx, "CREATE TABLE pirate_table (id TEXT PRIMARY KEY)")
		require.NoError(t, err)
		transaction, err := database.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		require.NoError(t, err)
		require.ErrorContains(t, verifyMigrationSourceTables(ctx, transaction), "business table list differs")
		require.NoError(t, transaction.Rollback())
	})

	t.Run("unknown audit column remains fail closed", func(t *testing.T) {
		database := openSQLiteMigrationTestDB(t)
		require.NoError(t, Migrate(ctx, database, DialectSQLite))
		_, err := database.ExecContext(ctx, "ALTER TABLE audit_logs ADD COLUMN pirate_column TEXT")
		require.NoError(t, err)
		transaction, err := database.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		require.NoError(t, err)
		require.ErrorContains(t, verifyMigrationSourceTables(ctx, transaction), "columns differ for table=audit_logs")
		require.NoError(t, transaction.Rollback())
	})
}

func derivedTableName(t *testing.T, database *sql.DB, name string) string {
	t.Helper()
	var actual string
	require.NoError(t, database.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name=?", name).Scan(&actual))
	return actual
}

func digestSQLiteMigrationSource(t *testing.T, ctx context.Context, database *sql.DB) map[string]migrationDigest {
	t.Helper()
	transaction, err := database.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	require.NoError(t, err)
	digests, err := digestTables(ctx, transaction, sqliteToPostgresTables, false)
	require.NoError(t, err)
	require.NoError(t, transaction.Rollback())
	return digests
}

func sqliteToPostgresTableByName(t *testing.T, name string) migrationTable {
	t.Helper()
	for _, table := range sqliteToPostgresTables {
		if table.name == name {
			return table
		}
	}
	t.Fatalf("migration table %q is missing", name)
	return migrationTable{}
}

func migrationTableNames(tables []migrationTable) []string {
	names := make([]string, len(tables))
	for index, table := range tables {
		names[index] = table.name
	}
	return names
}

func TestExpectedSequenceNext(t *testing.T) {
	require.Equal(t, int64(1), expectedSequenceNext(1, false))
	require.Equal(t, int64(43), expectedSequenceNext(42, true))
}

func TestVerifyMigrationSourceRejectsNonLatestAndOrphan(t *testing.T) {
	ctx := context.Background()
	t.Run("non-latest", func(t *testing.T) {
		database, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "old.db"))
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, database.Close()) })
		require.NoError(t, database.PingContext(ctx))
		tx, err := database.BeginTx(ctx, nil)
		require.NoError(t, err)
		defer tx.Rollback()
		err = verifyMigrationSource(ctx, tx)
		require.ErrorContains(t, err, "schema_migrations")
	})

	t.Run("orphan", func(t *testing.T) {
		database, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "orphan.db"))
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, database.Close()) })
		require.NoError(t, Migrate(ctx, database, DialectSQLite))
		_, err = database.ExecContext(ctx, "PRAGMA foreign_keys=OFF")
		require.NoError(t, err)
		_, err = database.ExecContext(ctx, `INSERT INTO approvals(id,audit_id) VALUES('approval-locator',999)`)
		require.NoError(t, err)
		tx, err := database.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		require.NoError(t, err)
		defer tx.Rollback()
		err = verifyMigrationSource(ctx, tx)
		require.ErrorContains(t, err, "table=\"approvals\"")
		require.NotContains(t, err.Error(), "999")
	})

	t.Run("separated SQLite schema is not a combined source", func(t *testing.T) {
		database, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "split.db"))
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, database.Close()) })
		require.NoError(t, MigrateMetadata(ctx, database, DialectSQLite, true))
		tx, err := database.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		require.NoError(t, err)
		defer tx.Rollback()
		err = verifyMigrationSource(ctx, tx)
		require.ErrorContains(t, err, "current=11 latest=12")
	})
}

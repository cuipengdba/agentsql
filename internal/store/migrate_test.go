package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMigrateIsIdempotentAndMatchesFrozenSchema(t *testing.T) {
	opened := openTestStore(t)
	ctx := context.Background()

	require.NoError(t, Migrate(ctx, opened.metaDB, DialectSQLite))
	require.NoError(t, Migrate(ctx, opened.metaDB, DialectSQLite))

	migrationErrors := make(chan error, 2)
	for range 2 {
		go func() {
			migrationErrors <- Migrate(ctx, opened.metaDB, DialectSQLite)
		}()
	}
	for range 2 {
		require.NoError(t, <-migrationErrors)
	}

	var migrationCount int
	require.NoError(t, opened.metaDB.QueryRowContext(
		ctx,
		"SELECT COUNT(*) FROM schema_migrations WHERE version = 1",
	).Scan(&migrationCount))
	require.Equal(t, 1, migrationCount)

	var foreignKeysEnabled int
	require.NoError(t, opened.metaDB.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeysEnabled))
	require.Equal(t, 1, foreignKeysEnabled)

	expectedColumns := map[string][]string{
		"agents": {
			"id", "name", "owner", "status", "api_key_hash", "level", "expires_at",
			"created_at", "updated_at",
		},
		"datasources": {
			"id", "name", "db_type", "host", "port", "database", "username",
			"password_enc", "conn_limit", "stmt_timeout_ms", "row_limit", "created_at",
			"updated_at",
		},
		"policies": {
			"id", "agent_id", "datasource_id", "object_type", "object_name", "columns",
			"row_filter", "action", "created_at", "updated_at",
		},
		"rules": {
			"id", "db_type", "title", "risk_level", "pattern_type", "definition",
			"enabled", "builtin", "created_at", "updated_at",
		},
		"mask_rules": {
			"id", "datasource_id", "table_name", "column_name", "sensitive_type", "algo",
			"created_at", "updated_at",
		},
		"audit_logs": {
			"id", "ts", "agent_id", "datasource_id", "session_id", "conversation_id",
			"mcp_tool", "db_type", "sql_raw", "sql_norm", "stmt_type", "objects",
			"decision", "rule_hits", "risk_level", "est_rows", "rows_returned",
			"latency_ms", "client_ip", "model_name", "error_msg",
		},
		"approvals": {
			"id", "audit_id", "agent_id", "sql_raw", "reason", "status", "approver",
			"decided_at", "created_at", "updated_at",
		},
		"notification_settings": {
			"id", "enabled", "queue_size", "created_at", "updated_at",
		},
		"notification_channels": {
			"id", "position", "enabled", "kind", "decisions", "include_sql",
			"allow_private_endpoints", "webhook_present", "webhook_template",
			"webhook_url_enc", "webhook_bearer_token_enc", "webhook_headers_enc",
			"webhook_secret_enc", "syslog_present", "syslog_host", "syslog_port",
			"syslog_transport", "syslog_facility", "created_at", "updated_at",
		},
	}

	actualTables := businessTableNames(t, opened.metaDB)
	expectedTables := make([]string, 0, len(expectedColumns))
	for table := range expectedColumns {
		expectedTables = append(expectedTables, table)
	}
	sort.Strings(expectedTables)
	require.Equal(t, expectedTables, actualTables)

	for table, columns := range expectedColumns {
		t.Run(table+" columns", func(t *testing.T) {
			require.Equal(t, columns, tableColumnNames(t, opened.metaDB, table))
		})
	}

	require.Equal(t, []string{
		"idx_agents_keyhash",
		"idx_approvals_status",
		"idx_audit_agent_ts",
		"idx_audit_decision",
		"idx_audit_ts",
		"idx_policies_agent_ds",
	}, businessIndexNames(t, opened.metaDB))
}

func TestSQLiteMigrationClaimRollsBackWithDDL(t *testing.T) {
	opened := openTestStore(t)
	ctx := context.Background()

	err := applyMigration(ctx, opened.metaDB, DialectSQLite, 999, `
CREATE TABLE migration_should_rollback (id INTEGER PRIMARY KEY);
CREATE TABL invalid_syntax (id INTEGER PRIMARY KEY);`)
	require.Error(t, err)

	var tableCount int
	require.NoError(t, opened.metaDB.QueryRowContext(
		ctx,
		"SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'migration_should_rollback'",
	).Scan(&tableCount))
	require.Zero(t, tableCount)

	var migrationCount int
	require.NoError(t, opened.metaDB.QueryRowContext(
		ctx,
		"SELECT COUNT(*) FROM schema_migrations WHERE version = 999",
	).Scan(&migrationCount))
	require.Zero(t, migrationCount)
}

func TestSQLiteSeparatedMetadataMigrationOmitsAuditAndApprovalForeignKey(t *testing.T) {
	ctx := context.Background()
	database, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "metadata.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)

	require.NoError(t, MigrateMetadata(ctx, database, DialectSQLite, true))
	require.Equal(t, []string{
		"agents", "approvals", "datasources", "mask_rules", "notification_channels",
		"notification_settings", "policies", "rules",
	}, businessTableNames(t, database))
	require.Equal(t, []string{
		"idx_agents_keyhash", "idx_approvals_status", "idx_policies_agent_ds",
	}, businessIndexNames(t, database))

	rows, err := database.QueryContext(ctx, "PRAGMA foreign_key_list(approvals)")
	require.NoError(t, err)
	require.False(t, rows.Next())
	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())

	current, latest, err := MetadataMigrationVersions(ctx, database, DialectSQLite, true)
	require.NoError(t, err)
	require.Equal(t, 2, current)
	require.Equal(t, 2, latest)
	require.NoError(t, VerifyMetadataSchema(ctx, database, DialectSQLite, true))
}

func businessTableNames(t *testing.T, database *sql.DB) []string {
	t.Helper()
	rows, err := database.Query(`
SELECT name
FROM sqlite_master
WHERE type = 'table'
  AND name NOT LIKE 'sqlite_%'
  AND name <> 'schema_migrations'
ORDER BY name`)
	require.NoError(t, err)

	var names []string
	for rows.Next() {
		var name string
		require.NoError(t, rows.Scan(&name))
		names = append(names, name)
	}
	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())
	return names
}

func tableColumnNames(t *testing.T, database *sql.DB, table string) []string {
	t.Helper()
	rows, err := database.Query(fmt.Sprintf("PRAGMA table_info(%q)", table))
	require.NoError(t, err)

	var names []string
	for rows.Next() {
		var columnID, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		require.NoError(t, rows.Scan(
			&columnID,
			&name,
			&columnType,
			&notNull,
			&defaultValue,
			&primaryKey,
		))
		names = append(names, name)
	}
	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())
	return names
}

func businessIndexNames(t *testing.T, database *sql.DB) []string {
	t.Helper()
	rows, err := database.Query(`
SELECT name
FROM sqlite_master
WHERE type = 'index'
  AND name NOT LIKE 'sqlite_%'
ORDER BY name`)
	require.NoError(t, err)

	var names []string
	for rows.Next() {
		var name string
		require.NoError(t, rows.Scan(&name))
		names = append(names, name)
	}
	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())
	return names
}

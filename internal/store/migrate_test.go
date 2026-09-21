package store

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
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
			"created_at", "updated_at", "enabled", "range_bucket_width",
			"range_bucket_offset", "range_granularity", "schema_name",
		},
		"audit_logs": {
			"id", "ts", "agent_id", "datasource_id", "session_id", "conversation_id",
			"mcp_tool", "db_type", "sql_raw", "sql_norm", "stmt_type", "objects",
			"decision", "rule_hits", "risk_level", "est_rows", "rows_returned",
			"latency_ms", "client_ip", "model_name", "error_msg",
			"action", "actor_type", "actor_id", "details_json", "error_code",
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
		"ux_mask_rules_scope_column",
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

func TestSQLiteAuditErrorCodeMigrationFromV5(t *testing.T) {
	ctx := context.Background()
	database := openSQLiteMigrationTestDB(t)
	migrateSQLiteThroughVersion(t, ctx, database, "migrations/sqlite", 5)
	_, err := database.ExecContext(ctx, `INSERT INTO audit_logs(decision, error_msg) VALUES('error', 'safe legacy message')`)
	require.NoError(t, err)

	require.NoError(t, Migrate(ctx, database, DialectSQLite))
	current, latest, err := MetadataMigrationVersions(ctx, database, DialectSQLite, false)
	require.NoError(t, err)
	require.Equal(t, 6, current)
	require.Equal(t, 6, latest)
	require.Contains(t, tableColumnNames(t, database, "audit_logs"), "error_code")

	var legacyCode sql.NullString
	require.NoError(t, database.QueryRowContext(ctx, `SELECT error_code FROM audit_logs LIMIT 1`).Scan(&legacyCode))
	require.False(t, legacyCode.Valid)
	repository := &AuditLogRepository{repositoryBase: repositoryBase{db: database, dialect: DialectSQLite}}
	inserted, err := repository.Insert(ctx, model.AuditLog{
		Decision: "error", ErrorCode: stringPointerStoreTest("DB_OBJECT_NOT_FOUND"),
	})
	require.NoError(t, err)
	require.Equal(t, "DB_OBJECT_NOT_FOUND", *inserted.ErrorCode)
	page, err := repository.Page(ctx, 1, 10)
	require.NoError(t, err)
	require.Len(t, page.List, 2)
	require.Equal(t, "DB_OBJECT_NOT_FOUND", *page.List[0].ErrorCode)
	require.Nil(t, page.List[1].ErrorCode)
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
		"ux_mask_rules_scope_column",
	}, businessIndexNames(t, database))

	rows, err := database.QueryContext(ctx, "PRAGMA foreign_key_list(approvals)")
	require.NoError(t, err)
	require.False(t, rows.Next())
	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())

	current, latest, err := MetadataMigrationVersions(ctx, database, DialectSQLite, true)
	require.NoError(t, err)
	require.Equal(t, 5, current)
	require.Equal(t, 5, latest)
	require.NoError(t, VerifyMetadataSchema(ctx, database, DialectSQLite, true))
}

func TestSQLiteDiscoveryDraftMigrationFromV2(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		directory string
		hasAudit  bool
		migrate   func(context.Context, *sql.DB) error
	}{
		{name: "combined", directory: "migrations/sqlite", hasAudit: true, migrate: func(ctx context.Context, db *sql.DB) error {
			return Migrate(ctx, db, DialectSQLite)
		}},
		{name: "metadata", directory: "migrations/metadata/sqlite", migrate: func(ctx context.Context, db *sql.DB) error {
			return MigrateMetadata(ctx, db, DialectSQLite, true)
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			ctx := context.Background()
			database := openSQLiteMigrationTestDB(t)
			migrateSQLiteThroughVersion(t, ctx, database, testCase.directory, 2)
			_, err := database.ExecContext(ctx, `
INSERT INTO mask_rules(id,datasource_id,table_name,column_name,sensitive_type,algo)
VALUES('legacy','ds-1','users',' Email ','email','mask')`)
			require.NoError(t, err)
			if testCase.hasAudit {
				_, err = database.ExecContext(ctx, `INSERT INTO audit_logs(decision) VALUES('allow')`)
				require.NoError(t, err)
			}

			require.NoError(t, testCase.migrate(ctx, database))
			var current, enabled int
			require.NoError(t, database.QueryRowContext(ctx, "SELECT MAX(version) FROM schema_migrations").Scan(&current))
			if testCase.hasAudit {
				require.Equal(t, 6, current)
			} else {
				require.Equal(t, 5, current)
			}
			require.NoError(t, database.QueryRowContext(ctx, "SELECT enabled FROM mask_rules WHERE id='legacy'").Scan(&enabled))
			require.Equal(t, 1, enabled)
			var schemaName, tableName string
			require.NoError(t, database.QueryRowContext(ctx, "SELECT schema_name, table_name FROM mask_rules WHERE id='legacy'").Scan(&schemaName, &tableName))
			require.Empty(t, schemaName)
			require.Empty(t, tableName, "0005 preserves the pre-upgrade global-column runtime semantics")
			_, err = database.ExecContext(ctx, "UPDATE mask_rules SET enabled=2 WHERE id='legacy'")
			require.Error(t, err)
			var indexSQL string
			require.NoError(t, database.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='index' AND name='ux_mask_rules_scope_column'`).Scan(&indexSQL))
			require.Contains(t, strings.ToUpper(indexSQL), "UNIQUE INDEX")
			require.Contains(t, indexSQL, "schema_name")
			require.Contains(t, indexSQL, "table_name")
			_, err = database.ExecContext(ctx, `
INSERT INTO mask_rules(id,datasource_id,schema_name,table_name,column_name,sensitive_type,algo)
VALUES('duplicate',' ds-1 ','','','email','email','mask')`)
			require.Error(t, err)
			_, err = database.ExecContext(ctx, `
INSERT INTO mask_rules(id,datasource_id,schema_name,table_name,column_name,sensitive_type,algo)
VALUES('different-table',' ds-1 ','','other','email','email','mask')`)
			require.NoError(t, err)
			if testCase.hasAudit {
				var action, actorType, actorID, detailsJSON sql.NullString
				require.NoError(t, database.QueryRowContext(ctx, `
SELECT action, actor_type, actor_id, details_json FROM audit_logs LIMIT 1`).Scan(
					&action, &actorType, &actorID, &detailsJSON,
				))
				require.False(t, action.Valid)
				require.False(t, actorType.Valid)
				require.False(t, actorID.Valid)
				require.False(t, detailsJSON.Valid)
			}
		})
	}
}

func TestSQLiteRangeMigrationFromV3SupportsRepositoryScan(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		directory string
		hasAudit  bool
		migrate   func(context.Context, *sql.DB) error
	}{
		{name: "combined", directory: "migrations/sqlite", hasAudit: true, migrate: func(ctx context.Context, db *sql.DB) error {
			return Migrate(ctx, db, DialectSQLite)
		}},
		{name: "metadata", directory: "migrations/metadata/sqlite", migrate: func(ctx context.Context, db *sql.DB) error {
			return MigrateMetadata(ctx, db, DialectSQLite, true)
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			ctx := context.Background()
			database := openSQLiteMigrationTestDB(t)
			migrateSQLiteThroughVersion(t, ctx, database, testCase.directory, 3)
			_, err := database.ExecContext(ctx, `
INSERT INTO mask_rules(id,datasource_id,table_name,column_name,sensitive_type,algo,enabled)
VALUES('legacy-v3','ds-1','users','phone','phone','mask',1)`)
			require.NoError(t, err)

			require.NoError(t, testCase.migrate(ctx, database))
			var current int
			require.NoError(t, database.QueryRowContext(ctx, "SELECT MAX(version) FROM schema_migrations").Scan(&current))
			if testCase.hasAudit {
				require.Equal(t, 6, current)
			} else {
				require.Equal(t, 5, current)
			}
			require.Equal(t, []string{
				"id", "datasource_id", "table_name", "column_name", "sensitive_type", "algo",
				"created_at", "updated_at", "enabled", "range_bucket_width",
				"range_bucket_offset", "range_granularity", "schema_name",
			}, tableColumnNames(t, database, "mask_rules"))

			repository := &MaskRuleRepository{repositoryBase: repositoryBase{db: database, dialect: DialectSQLite}}
			stored, err := repository.Get(ctx, "legacy-v3")
			require.NoError(t, err)
			require.Nil(t, stored.RangeBucketWidth)
			require.Nil(t, stored.RangeBucketOffset)
			require.Nil(t, stored.RangeGranularity)
			require.Empty(t, stored.SchemaName)
			require.Empty(t, stored.TableName)
		})
	}
}

func TestRangeMigrationFilesAreByteIdentical(t *testing.T) {
	paths := []string{
		"migrations/sqlite/0004_range_redaction.sql",
		"migrations/postgres/0004_range_redaction.sql",
		"migrations/metadata/sqlite/0004_range_redaction.sql",
		"migrations/metadata/postgres/0004_range_redaction.sql",
	}
	want := "ALTER TABLE mask_rules ADD COLUMN range_bucket_width INTEGER;\n" +
		"ALTER TABLE mask_rules ADD COLUMN range_bucket_offset INTEGER;\n" +
		"ALTER TABLE mask_rules ADD COLUMN range_granularity TEXT;"
	for _, name := range paths {
		contents, err := fs.ReadFile(migrationFiles, name)
		require.NoError(t, err)
		require.Equal(t, want, strings.TrimSuffix(string(contents), "\n"), name)
	}
}

func TestMaskRuleScopeMigrationFilesMatchEachDialect(t *testing.T) {
	for _, pair := range [][2]string{
		{"migrations/sqlite/0005_mask_rule_scope.sql", "migrations/metadata/sqlite/0005_mask_rule_scope.sql"},
		{"migrations/postgres/0005_mask_rule_scope.sql", "migrations/metadata/postgres/0005_mask_rule_scope.sql"},
	} {
		combined, err := fs.ReadFile(migrationFiles, pair[0])
		require.NoError(t, err)
		metadata, err := fs.ReadFile(migrationFiles, pair[1])
		require.NoError(t, err)
		require.Equal(t, string(combined), string(metadata), pair)
	}
}

func TestSQLiteDiscoveryDraftMigrationRejectsDuplicateNormalizedKeys(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		directory string
		migrate   func(context.Context, *sql.DB) error
	}{
		{name: "combined", directory: "migrations/sqlite", migrate: func(ctx context.Context, db *sql.DB) error {
			return Migrate(ctx, db, DialectSQLite)
		}},
		{name: "metadata", directory: "migrations/metadata/sqlite", migrate: func(ctx context.Context, db *sql.DB) error {
			return MigrateMetadata(ctx, db, DialectSQLite, true)
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			ctx := context.Background()
			database := openSQLiteMigrationTestDB(t)
			migrateSQLiteThroughVersion(t, ctx, database, testCase.directory, 2)
			for _, values := range [][]any{
				{"dup-a", "ds-1", " Email "},
				{"dup-b", " ds-1 ", "email"},
			} {
				_, err := database.ExecContext(ctx, `
INSERT INTO mask_rules(id,datasource_id,table_name,column_name,sensitive_type,algo)
VALUES(?,?, 'users', ?, 'email', 'mask')`, values...)
				require.NoError(t, err)
			}

			err := testCase.migrate(ctx, database)
			require.ErrorContains(t, err, `scope="ds-1" column="email" row_ids=[dup-a,dup-b]`)
			var current int
			require.NoError(t, database.QueryRowContext(ctx, "SELECT MAX(version) FROM schema_migrations").Scan(&current))
			require.Equal(t, 2, current)
			require.NotContains(t, tableColumnNames(t, database, "mask_rules"), "enabled")
		})
	}
}

func openSQLiteMigrationTestDB(t *testing.T) *sql.DB {
	t.Helper()
	database, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "migration.db"))
	require.NoError(t, err)
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	return database
}

func migrateSQLiteThroughVersion(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	directory string,
	maximum int,
) {
	t.Helper()
	require.NoError(t, enableSQLiteForeignKeys(ctx, database))
	_, err := database.ExecContext(ctx, sqliteSchemaMigrationsDDL)
	require.NoError(t, err)
	directoryFS, err := fs.Sub(migrationFiles, directory)
	require.NoError(t, err)
	filenames, err := fs.Glob(directoryFS, "*.sql")
	require.NoError(t, err)
	sort.Strings(filenames)
	for _, filename := range filenames {
		version, err := migrationVersion(filename)
		require.NoError(t, err)
		if version > maximum {
			continue
		}
		contents, err := fs.ReadFile(directoryFS, filename)
		require.NoError(t, err)
		require.NoError(t, applyMigration(ctx, database, DialectSQLite, version, string(contents)))
	}
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

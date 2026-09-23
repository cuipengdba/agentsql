package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPostgres18SQLiteToPostgresMigrationE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("postgres:18 SQLite-to-PostgreSQL migration E2E is an integration test")
	}
	ctx := dockerTestContext(t)
	sourcePath := seedSQLiteMigrationSource(t, ctx)

	t.Run("combined exact rerun conflict and sequence", func(t *testing.T) {
		dsn := startPostgres18StoreContainer(t, ctx, "agentsql_migrate_combined", "combined-secret-password")
		target := openPostgresMigrationTarget(t, dsn)
		options := SQLiteToPostgresOptions{SourcePath: sourcePath, MetadataDB: target, VerifyHash: true}
		first, err := MigrateSQLiteToPostgres(ctx, options)
		require.NoError(t, err)
		assertMigrationSummary(t, first)
		assertMigratedSemanticValues(t, ctx, target)

		second, err := MigrateSQLiteToPostgres(ctx, options)
		require.NoError(t, err)
		require.Equal(t, first.Tables, second.Tables)
		var auditCount int64
		require.NoError(t, target.QueryRowContext(ctx, `SELECT COUNT(*) FROM "public"."audit_logs"`).Scan(&auditCount))
		require.Equal(t, int64(2), auditCount)

		var nextID int64
		require.NoError(t, target.QueryRowContext(ctx, `INSERT INTO "public"."audit_logs"("decision") VALUES('allow') RETURNING "id"`).Scan(&nextID))
		require.Equal(t, int64(13), nextID)
		_, err = MigrateSQLiteToPostgres(ctx, options)
		require.ErrorContains(t, err, "conflict")
		require.NoError(t, target.QueryRowContext(ctx, `SELECT COUNT(*) FROM "public"."audit_logs"`).Scan(&auditCount))
		require.Equal(t, int64(3), auditCount)
		var approvalForeignKey int
		require.NoError(t, target.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.table_constraints WHERE table_schema='public' AND table_name='approvals' AND constraint_type='FOREIGN KEY'`).Scan(&approvalForeignKey))
		require.Equal(t, 1, approvalForeignKey)
	})

	t.Run("separated audit-first partial rerun", func(t *testing.T) {
		metadataDSN := startPostgres18StoreContainer(t, ctx, "agentsql_migrate_metadata", "metadata-secret-password")
		auditDSN := startPostgres18StoreContainer(t, ctx, "agentsql_migrate_audit", "audit-secret-password")
		metadata := openPostgresMigrationTarget(t, metadataDSN)
		audit := openPostgresMigrationTarget(t, auditDSN)
		require.NoError(t, MigrateMetadata(ctx, metadata, DialectPostgres, true))
		require.NoError(t, MigrateAudit(ctx, audit, DialectPostgres))
		_, err := metadata.ExecContext(ctx, `
CREATE FUNCTION fail_migration_commit() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'injected metadata commit failure'; END $$;
CREATE CONSTRAINT TRIGGER fail_migration_commit
AFTER INSERT ON "public"."agents" DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION fail_migration_commit()`)
		require.NoError(t, err)
		options := SQLiteToPostgresOptions{
			SourcePath: sourcePath, MetadataDB: metadata, AuditDB: audit, Separate: true, VerifyHash: true,
		}
		_, err = MigrateSQLiteToPostgres(ctx, options)
		require.Error(t, err)
		require.NotContains(t, err.Error(), metadataDSN)
		require.NotContains(t, err.Error(), auditDSN)
		require.NotContains(t, err.Error(), "metadata-secret-password")
		require.NotContains(t, err.Error(), "audit-secret-password")
		var metadataAgents, auditRows int64
		require.NoError(t, metadata.QueryRowContext(ctx, `SELECT COUNT(*) FROM "public"."agents"`).Scan(&metadataAgents))
		require.NoError(t, audit.QueryRowContext(ctx, `SELECT COUNT(*) FROM "public"."audit_logs"`).Scan(&auditRows))
		require.Zero(t, metadataAgents)
		require.Equal(t, int64(2), auditRows)

		_, err = metadata.ExecContext(ctx, `DROP TRIGGER fail_migration_commit ON "public"."agents"; DROP FUNCTION fail_migration_commit()`)
		require.NoError(t, err)
		summary, err := MigrateSQLiteToPostgres(ctx, options)
		require.NoError(t, err)
		assertMigrationSummary(t, summary)
		assertMigratedMaskRuleRanges(t, ctx, metadata)
		assertMigratedRedactionStorage(t, ctx, metadata, audit)
		rerun, err := MigrateSQLiteToPostgres(ctx, options)
		require.NoError(t, err)
		require.Equal(t, summary.Tables, rerun.Tables)
		var approvalForeignKey int
		require.NoError(t, metadata.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.table_constraints WHERE table_schema='public' AND table_name='approvals' AND constraint_type='FOREIGN KEY'`).Scan(&approvalForeignKey))
		require.Zero(t, approvalForeignKey)
		require.Zero(t, summary.Approvals.TargetOrphans)
		var nextID int64
		require.NoError(t, audit.QueryRowContext(ctx, `INSERT INTO "public"."audit_logs"("decision") VALUES('allow') RETURNING "id"`).Scan(&nextID))
		require.Equal(t, int64(13), nextID)
	})
}

func assertMigratedSemanticValues(t *testing.T, ctx context.Context, target *sql.DB) {
	t.Helper()
	var apiKeyHash, passwordCipher string
	require.NoError(t, target.QueryRowContext(ctx, `SELECT "api_key_hash" FROM "public"."agents" WHERE "id"='agent-1'`).Scan(&apiKeyHash))
	require.NoError(t, target.QueryRowContext(ctx, `SELECT "password_enc" FROM "public"."datasources" WHERE "id"='ds-1'`).Scan(&passwordCipher))
	require.Equal(t, "opaque-hash-not-rehashed", apiKeyHash)
	require.Equal(t, "opaque-ciphertext", passwordCipher)
	var createdAt time.Time
	require.NoError(t, target.QueryRowContext(ctx, `SELECT "created_at" FROM "public"."agents" WHERE "id"='agent-1'`).Scan(&createdAt))
	require.Equal(t, time.Date(2026, 9, 17, 12, 0, 0, 987654000, time.UTC), createdAt.UTC())
	var sqlRawLength, sqlNormLength int
	require.NoError(t, target.QueryRowContext(ctx, `SELECT length("sql_raw"),length("sql_norm") FROM "public"."audit_logs" WHERE "id"=10`).Scan(&sqlRawLength, &sqlNormLength))
	require.Equal(t, 1<<20, sqlRawLength)
	require.Equal(t, 1<<20, sqlNormLength)
	var enabled, builtin sql.NullBool
	require.NoError(t, target.QueryRowContext(ctx, `SELECT "enabled","builtin" FROM "public"."rules" WHERE "id"='rule-2'`).Scan(&enabled, &builtin))
	require.False(t, enabled.Valid)
	require.False(t, builtin.Valid)
	var auditIDs []int64
	rows, err := target.QueryContext(ctx, `SELECT "id" FROM "public"."audit_logs" ORDER BY "id"`)
	require.NoError(t, err)
	for rows.Next() {
		var id int64
		require.NoError(t, rows.Scan(&id))
		auditIDs = append(auditIDs, id)
	}
	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())
	require.Equal(t, []int64{10, 12}, auditIDs)
	assertMigratedRedactionStorage(t, ctx, target, target)
	cipher, err := NewPasswordCipher([]byte(testSecret))
	require.NoError(t, err)
	notifications, err := (&NotificationRepository{
		repositoryBase: repositoryBase{db: target, dialect: DialectPostgres}, cipher: cipher,
	}).Get(ctx)
	require.NoError(t, err)
	require.Equal(t, completeNotificationConfig(), notifications)
	assertMigratedMaskRuleRanges(t, ctx, target)
}

func assertMigratedRedactionStorage(t *testing.T, ctx context.Context, metadata, audit *sql.DB) {
	t.Helper()
	var activeCount, retiredCount, outboxCount int
	require.NoError(t, metadata.QueryRowContext(ctx, `SELECT COUNT(*) FROM redaction_key_versions WHERE state='active'`).Scan(&activeCount))
	require.NoError(t, metadata.QueryRowContext(ctx, `SELECT COUNT(*) FROM redaction_key_versions WHERE state='retired' AND retired_at IS NOT NULL`).Scan(&retiredCount))
	require.NoError(t, metadata.QueryRowContext(ctx, `SELECT COUNT(*) FROM management_audit_outbox WHERE claimed_by='worker-1' AND attempts=2`).Scan(&outboxCount))
	require.Equal(t, 1, activeCount)
	require.Equal(t, 1, retiredCount)
	require.Equal(t, 1, outboxCount)
	var eventUUID sql.NullString
	require.NoError(t, audit.QueryRowContext(ctx, `SELECT event_uuid FROM audit_logs WHERE id=10`).Scan(&eventUUID))
	require.Equal(t, "migration-event-10", eventUUID.String)
	require.True(t, eventUUID.Valid)
	require.NoError(t, audit.QueryRowContext(ctx, `SELECT event_uuid FROM audit_logs WHERE id=12`).Scan(&eventUUID))
	require.False(t, eventUUID.Valid)
	var metadataIndex, auditIndex int
	require.NoError(t, metadata.QueryRowContext(ctx, `SELECT COUNT(*) FROM pg_indexes WHERE schemaname='public' AND indexname='ux_redaction_key_versions_active'`).Scan(&metadataIndex))
	require.NoError(t, audit.QueryRowContext(ctx, `SELECT COUNT(*) FROM pg_indexes WHERE schemaname='public' AND indexname='ux_audit_logs_event_uuid'`).Scan(&auditIndex))
	require.Equal(t, 1, metadataIndex)
	require.Equal(t, 1, auditIndex)
}

// assertMigratedMaskRuleRanges verifies that mask_rules range parameters survive
// a SQLite-to-PostgreSQL migration. It only touches metadata tables, so it is safe
// in the separated layout (audit_logs lives in the audit database).
func assertMigratedMaskRuleRanges(t *testing.T, ctx context.Context, target *sql.DB) {
	t.Helper()
	rows, err := target.QueryContext(ctx, `
SELECT "id", "schema_name", "table_name", "range_bucket_width", "range_bucket_offset", "range_granularity"
FROM "public"."mask_rules"
ORDER BY "id"`)
	require.NoError(t, err)
	type rangeValues struct {
		id            string
		schema, table sql.NullString
		width, offset sql.NullInt64
		granularity   sql.NullString
	}
	var ranges []rangeValues
	for rows.Next() {
		var values rangeValues
		require.NoError(t, rows.Scan(&values.id, &values.schema, &values.table, &values.width, &values.offset, &values.granularity))
		ranges = append(ranges, values)
	}
	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())
	require.Len(t, ranges, 3)
	require.Equal(t, "mask-1", ranges[0].id)
	require.False(t, ranges[0].schema.Valid, "SQL NULL schema survives SQLite-to-PostgreSQL migration")
	require.Equal(t, "users", ranges[0].table.String)
	require.False(t, ranges[0].width.Valid)
	require.False(t, ranges[0].offset.Valid)
	require.False(t, ranges[0].granularity.Valid)
	require.Equal(t, "range-date", ranges[1].id)
	require.Equal(t, "tenant", ranges[1].schema.String)
	require.Equal(t, "orders", ranges[1].table.String)
	require.False(t, ranges[1].width.Valid)
	require.False(t, ranges[1].offset.Valid)
	require.Equal(t, "quarter", ranges[1].granularity.String)
	require.Equal(t, "range-number", ranges[2].id)
	require.True(t, ranges[2].schema.Valid, "empty string remains distinct from SQL NULL")
	require.Empty(t, ranges[2].schema.String)
	require.Equal(t, "orders", ranges[2].table.String)
	require.Equal(t, int64(25), ranges[2].width.Int64)
	require.True(t, ranges[2].width.Valid)
	require.Equal(t, int64(0), ranges[2].offset.Int64)
	require.True(t, ranges[2].offset.Valid)
	require.False(t, ranges[2].granularity.Valid)
}

func seedSQLiteMigrationSource(t *testing.T, ctx context.Context) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "source.db")
	database, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	require.NoError(t, Migrate(ctx, database, DialectSQLite))
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO agents(id,name,owner,status,api_key_hash,level,expires_at,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?)`, []any{"agent-1", "Agent", nil, "active", "opaque-hash-not-rehashed", "readonly", "2026-09-17T20:34:56.123456789+08:00", "2026-09-17 12:00:00.987654321", "2026-09-17 12:00:01"}},
		{`INSERT INTO datasources(id,name,db_type,host,port,database,username,password_enc,conn_limit,stmt_timeout_ms,row_limit,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, []any{"ds-1", "DB", "postgres", "127.0.0.1", 5432, "app", "runtime", "opaque-ciphertext", 5, 5000, 1000, "2026-09-17 12:00:00", "2026-09-17 12:00:00"}},
		{`INSERT INTO rules(id,db_type,title,risk_level,pattern_type,definition,enabled,builtin,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, []any{"rule-1", "postgres", "Rule", 3, "ast_match", `{"shape":"text"}`, 1, 0, "2026-09-17 12:00:00", "2026-09-17 12:00:00"}},
		{`INSERT INTO rules(id,db_type,title,risk_level,pattern_type,definition,enabled,builtin,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, []any{"rule-2", "postgres", "Nullable", 1, "ast_match", "not-json", nil, nil, "2026-09-17 12:00:00", "2026-09-17 12:00:00"}},
		{`INSERT INTO mask_rules(id,datasource_id,table_name,column_name,sensitive_type,algo,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?)`, []any{"mask-1", nil, "users", "email", "email", "partial", "2026-09-17 12:00:00", "2026-09-17 12:00:00"}},
		{`INSERT INTO mask_rules(id,datasource_id,schema_name,table_name,column_name,sensitive_type,algo,range_bucket_width,range_bucket_offset) VALUES(?,?,?,?,?,?,?,?,?)`, []any{"range-number", "ds-1", "", "orders", "amount", "number", "range", 25, 0}},
		{`INSERT INTO mask_rules(id,datasource_id,schema_name,table_name,column_name,sensitive_type,algo,range_granularity) VALUES(?,?,?,?,?,?,?,?)`, []any{"range-date", "ds-1", "tenant", "orders", "created_at", "date", "range", "quarter"}},
		{`INSERT INTO policies(id,agent_id,datasource_id,object_type,object_name,columns,row_filter,action,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, []any{"policy-1", "agent-1", "ds-1", "table", "public.users", "", `{"tenant":1}`, "allow", "2026-09-17 12:00:00", "2026-09-17 12:00:00"}},
		{`INSERT INTO audit_logs(id,ts,agent_id,datasource_id,sql_raw,sql_norm,objects,decision,rule_hits,risk_level,est_rows,rows_returned,latency_ms,error_msg,event_uuid) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, []any{int64(10), "2026-09-17 12:00:00.123456789", "agent-1", "ds-1", strings.Repeat("S", 1<<20), strings.Repeat("N", 1<<20), `[{"table":"users"}]`, "deny", "[]", 3, int64(1 << 40), 0, int64(25), "", "migration-event-10"}},
		{`INSERT INTO audit_logs(id,ts,decision,objects,rule_hits,error_msg) VALUES(?,?,?,?,?,?)`, []any{int64(12), "2026-09-17T20:00:00.999999999+08:00", "approve", "not-json", nil, nil}},
		{`INSERT INTO approvals(id,audit_id,agent_id,sql_raw,reason,status,approver,decided_at,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, []any{"approval-1", int64(12), "agent-1", "UPDATE users SET active=1", "review", "pending", nil, nil, "2026-09-17 12:00:00", "2026-09-17 12:00:00"}},
		{`INSERT INTO redaction_key_versions(id,state,commitment,label,config_revision,created_at,updated_at,activated_at) VALUES(?,?,?,?,?,?,?,?)`, []any{"2", "active", testCommitment2, "active", "r1", "2026-09-17 12:00:00", "2026-09-17 12:00:00", "2026-09-17 12:00:00"}},
		{`INSERT INTO redaction_key_versions(id,state,commitment,label,config_revision,created_at,updated_at,retired_at) VALUES(?,?,?,?,?,?,?,?)`, []any{"10", "retired", strings.Repeat("a", 64), "retired", "r0", "2026-09-16 12:00:00", "2026-09-17 12:00:00", "2026-09-17 12:00:00"}},
		{`INSERT INTO management_audit_outbox(event_uuid,action,actor_type,actor_id,details_json,created_at,attempts,claimed_by,claimed_at,last_error,next_attempt_at,delivered_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, []any{"outbox-migration-1", "redaction.switch", "admin", "operator", `{"id":"2"}`, "2026-09-17 12:00:00", 2, "worker-1", "2026-09-17 12:01:00", "retry", "2026-09-17 12:03:00", nil}},
	}
	for _, statement := range statements {
		_, err := database.ExecContext(ctx, statement.query, statement.args...)
		require.NoError(t, err)
	}
	cipher, err := NewPasswordCipher([]byte(testSecret))
	require.NoError(t, err)
	require.NoError(t, (&NotificationRepository{
		repositoryBase: repositoryBase{db: database, dialect: DialectSQLite}, cipher: cipher,
	}).Replace(ctx, completeNotificationConfig()))
	require.NoError(t, database.Close())
	return path
}

func openPostgresMigrationTarget(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	database, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	database.SetMaxOpenConns(4)
	database.SetMaxIdleConns(2)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	return database
}

func assertMigrationSummary(t *testing.T, summary SQLiteToPostgresSummary) {
	t.Helper()
	require.Equal(t, "ok", summary.Verification)
	require.Len(t, summary.Tables, len(sqliteToPostgresTables))
	for _, table := range summary.Tables {
		require.Equal(t, table.SourceRows, table.TargetRows, table.Table)
		require.Len(t, table.SHA256, 64, table.Table)
	}
	var audit SQLiteToPostgresTableSummary
	seen := make(map[string]SQLiteToPostgresTableSummary, len(summary.Tables))
	for _, table := range summary.Tables {
		seen[table.Table] = table
		if table.Table == "audit_logs" {
			audit = table
		}
	}
	require.Contains(t, seen, "notification_settings")
	require.Contains(t, seen, "notification_channels")
	require.Contains(t, seen, "redaction_key_versions")
	require.Contains(t, seen, "management_audit_outbox")
	require.Equal(t, int64(1), seen["notification_settings"].SourceRows)
	require.Equal(t, int64(6), seen["notification_channels"].SourceRows)
	require.Equal(t, int64(2), seen["redaction_key_versions"].SourceRows)
	require.Equal(t, int64(1), seen["management_audit_outbox"].SourceRows)
	require.Equal(t, int64(10), *audit.MinID)
	require.Equal(t, int64(12), *audit.MaxID)
	require.Equal(t, int64(12), summary.Sequence.LastValue)
	require.True(t, summary.Sequence.IsCalled)
	require.Equal(t, int64(13), summary.Sequence.ExpectedNext)
	require.Equal(t, int64(1), summary.Approvals.SourceNonNullAuditIDs)
	require.Equal(t, int64(1), summary.Approvals.TargetNonNullAuditIDs)
	require.Zero(t, summary.Approvals.SourceOrphans)
	require.Zero(t, summary.Approvals.TargetOrphans)
}

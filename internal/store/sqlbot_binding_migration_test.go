package store

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"
)

// Exercise the immediately preceding release's two metadata layouts. The
// migration must retain existing approval data and fail verification before DDL.
func TestSQLBotSQLiteUpgradeFromV302(t *testing.T) {
	for _, separate := range []bool{false, true} {
		name, directory, previous := "combined", "migrations/sqlite", 23
		if separate {
			name, directory, previous = "metadata", "migrations/metadata/sqlite", 22
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			db := openSQLiteMigrationTestDB(t)
			migrateSQLiteThroughVersion(t, ctx, db, directory, previous)
			_, err := db.ExecContext(ctx, `INSERT INTO approvals(id,tenant_id,status) VALUES('pre-sqlbot-approval','tenant_default','pending')`)
			require.NoError(t, err)
			require.Error(t, VerifyMetadataSchema(ctx, db, DialectSQLite, separate))
			require.NoError(t, MigrateMetadata(ctx, db, DialectSQLite, separate))
			require.NoError(t, MigrateMetadata(ctx, db, DialectSQLite, separate))
			current, latest, err := MetadataMigrationVersions(ctx, db, DialectSQLite, separate)
			require.NoError(t, err)
			require.Equal(t, previous+1, current)
			require.Equal(t, current, latest)
			require.NoError(t, VerifyMetadataSchema(ctx, db, DialectSQLite, separate))
			var count int
			require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM approvals WHERE id='pre-sqlbot-approval'`).Scan(&count))
			require.Equal(t, 1, count)
			require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlbot_bindings`).Scan(&count))
			require.Zero(t, count)
		})
	}
}

// This is a real PostgreSQL 18 test, including independent metadata/audit
// databases. It does not claim production backup/restore or SQLBot business SQL
// coverage; those use separate release rehearsals.
func TestSQLBotPostgres18UpgradePersistenceAndAuditRollbackE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("PostgreSQL 18 containers are required")
	}
	for _, separate := range []bool{false, true} {
		name, directory, previous := "combined", "migrations/postgres", 23
		if separate {
			name, directory, previous = "separate", "migrations/metadata/postgres", 22
		}
		t.Run(name, func(t *testing.T) {
			ctx := dockerTestContext(t)
			dsn := startPostgres18StoreContainer(t, ctx, "sqlbot_upgrade", "synthetic-migration-password")
			db, err := sql.Open("pgx", dsn)
			require.NoError(t, err)
			migratePostgresThroughVersion(t, ctx, db, directory, previous)
			_, err = db.ExecContext(ctx, `INSERT INTO approvals(id,tenant_id,status) VALUES('pre-sqlbot-approval','tenant_default','pending')`)
			require.NoError(t, err)
			require.Error(t, VerifyMetadataSchema(ctx, db, DialectPostgres, separate))
			require.NoError(t, db.Close())
			options := MetadataOptions{Driver: DialectPostgres, PostgresDSN: dsn, AutoMigrate: true}
			if separate {
				auditDSN := startPostgres18StoreContainer(t, ctx, "sqlbot_upgrade_audit", "synthetic-audit-password")
				auditDB, err := sql.Open("pgx", auditDSN)
				require.NoError(t, err)
				migratePostgresThroughVersion(t, ctx, auditDB, "migrations/audit/postgres", 6)
				_, err = auditDB.ExecContext(ctx, `INSERT INTO audit_logs(decision,error_msg) VALUES('deny','pre-sqlbot-safe-audit')`)
				require.NoError(t, err)
				require.NoError(t, auditDB.Close())
				options.Audit = AuditOptions{Separate: true, Driver: DialectPostgres, PostgresDSN: auditDSN, AutoMigrate: true}
			}
			s, err := OpenMetadata(ctx, options, []byte(testSecret))
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, s.Close()) })
			current, latest, err := MetadataMigrationVersions(ctx, s.metaDB, DialectPostgres, separate)
			require.NoError(t, err)
			require.Equal(t, previous+1, current)
			require.Equal(t, current, latest)
			var count int
			require.NoError(t, s.metaDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM approvals WHERE id='pre-sqlbot-approval'`).Scan(&count))
			require.Equal(t, 1, count)
			if separate {
				auditCurrent, auditLatest, err := AuditMigrationVersions(ctx, s.auditDB, DialectPostgres)
				require.NoError(t, err)
				require.Equal(t, 6, auditCurrent)
				require.Equal(t, 6, auditLatest)
				require.NoError(t, s.auditDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_logs WHERE error_msg='pre-sqlbot-safe-audit'`).Scan(&count))
				require.Equal(t, 1, count)
			}

			input := sqlbotTestBinding(t, s)
			input.WorkspaceID, input.SQLBotUserID = "7518977644151443456", "7518977644151443457"
			created, credential, err := s.SQLBotBindings().Create(ctx, input, "migration-reviewer")
			require.NoError(t, err)
			_, _, err = s.SQLBotBindings().Create(ctx, input, "migration-reviewer")
			require.ErrorIs(t, err, ErrSQLBotBindingInvalid)
			require.NoError(t, s.Close())
			options.AutoMigrate, options.Audit.AutoMigrate = false, false
			s, err = OpenMetadata(ctx, options, []byte(testSecret))
			require.NoError(t, err, "already upgraded databases reopen without DDL")
			stored, err := s.SQLBotBindings().ResolveConnector(ctx, credential)
			require.NoError(t, err)
			require.Equal(t, input.WorkspaceID, stored.WorkspaceID)
			require.Equal(t, input.SQLBotUserID, stored.SQLBotUserID)
			require.Equal(t, created.ID, stored.ID)
			require.NotEqual(t, credential, stored.ConnectorTokenHash)
			rotated, newCredential, err := s.SQLBotBindings().Rotate(ctx, created.ID, created.Revision, "migration-reviewer")
			require.NoError(t, err)
			require.Equal(t, created.Revision+1, rotated.Revision)
			_, err = s.SQLBotBindings().ResolveConnector(ctx, credential)
			require.ErrorIs(t, err, ErrSQLBotBindingDenied)
			_, err = s.SQLBotBindings().ValidateRuntime(ctx, created.ID, created.Revision, created.AgentID, created.AgentSQLUserID)
			require.ErrorIs(t, err, ErrSQLBotBindingDenied)
			other, err := WithTenant(ctx, "other-tenant")
			require.NoError(t, err)
			_, err = s.SQLBotBindings().Get(other, created.ID)
			require.ErrorIs(t, err, ErrNotFound)

			_, err = s.metaDB.ExecContext(ctx, `CREATE FUNCTION fail_sqlbot_migration_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action LIKE 'sqlbot_binding.%' THEN RAISE EXCEPTION 'synthetic audit failure'; END IF; RETURN NEW; END $$`)
			require.NoError(t, err)
			_, err = s.metaDB.ExecContext(ctx, `CREATE TRIGGER fail_sqlbot_migration_audit BEFORE INSERT ON management_audit_outbox FOR EACH ROW EXECUTE FUNCTION fail_sqlbot_migration_audit()`)
			require.NoError(t, err)
			rotated.Enabled = false
			_, err = s.SQLBotBindings().Update(ctx, rotated, "migration-reviewer")
			require.Error(t, err)
			_, returned, err := s.SQLBotBindings().Rotate(ctx, rotated.ID, rotated.Revision, "migration-reviewer")
			require.Error(t, err)
			require.Empty(t, returned)
			require.Error(t, s.SQLBotBindings().Delete(ctx, rotated.ID, rotated.Revision, "migration-reviewer"))
			input.SQLBotUserID = "7518977644151443458"
			_, returned, err = s.SQLBotBindings().Create(ctx, input, "migration-reviewer")
			require.Error(t, err)
			require.Empty(t, returned)
			currentBinding, err := s.SQLBotBindings().ResolveConnector(ctx, newCredential)
			require.NoError(t, err, "failed audited mutations must not alter the binding")
			require.True(t, currentBinding.Enabled)
			require.Equal(t, rotated.Revision, currentBinding.Revision)
			require.NoError(t, s.metaDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlbot_bindings`).Scan(&count))
			require.Equal(t, 1, count)
			_, err = s.metaDB.ExecContext(ctx, `DROP TRIGGER fail_sqlbot_migration_audit ON management_audit_outbox`)
			require.NoError(t, err)
			_, err = s.metaDB.ExecContext(ctx, `DROP FUNCTION fail_sqlbot_migration_audit()`)
			require.NoError(t, err)
			_, err = s.SQLBotBindings().Update(ctx, rotated, "migration-reviewer")
			require.NoError(t, err)
			_, err = s.SQLBotBindings().ResolveConnector(ctx, newCredential)
			require.ErrorIs(t, err, ErrSQLBotBindingDenied)
			require.NoError(t, s.metaDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM management_audit_outbox WHERE action LIKE 'sqlbot_binding.%'`).Scan(&count))
			require.Equal(t, 3, count, "only committed create, rotate, and disable produce mutation audits")
		})
	}
}

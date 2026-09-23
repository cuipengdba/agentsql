package store

import (
	"context"
	"database/sql"
	"io/fs"
	"sort"
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestDiscoveryPostgresMetadataMigrationE2E(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		directory string
		hasAudit  bool
		migrate   func(context.Context, *sql.DB) error
	}{
		{name: "combined v2 to v3 and duplicate preflight", directory: "migrations/postgres", hasAudit: true, migrate: func(ctx context.Context, db *sql.DB) error {
			return Migrate(ctx, db, DialectPostgres)
		}},
		{name: "metadata v2 to v3 and duplicate preflight", directory: "migrations/metadata/postgres", migrate: func(ctx context.Context, db *sql.DB) error {
			return MigrateMetadata(ctx, db, DialectPostgres, true)
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			ctx := dockerTestContext(t)
			dsn := startPostgres18StoreContainer(t, ctx, "agentsql_t38_drafts", "draft-password")
			database, err := sql.Open("pgx", dsn)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, database.Close()) })

			migratePostgresThroughVersion(t, ctx, database, testCase.directory, 2)
			_, err = database.ExecContext(ctx, `
INSERT INTO mask_rules(id,datasource_id,table_name,column_name,sensitive_type,algo)
VALUES('legacy','ds-1','users',' Email ','email','mask')`)
			require.NoError(t, err)
			require.NoError(t, testCase.migrate(ctx, database))
			var enabled bool
			require.NoError(t, database.QueryRowContext(ctx, `SELECT enabled FROM mask_rules WHERE id='legacy'`).Scan(&enabled))
			require.True(t, enabled)
			var schemaName, tableName string
			require.NoError(t, database.QueryRowContext(ctx, `SELECT schema_name, table_name FROM mask_rules WHERE id='legacy'`).Scan(&schemaName, &tableName))
			require.Empty(t, schemaName)
			require.Empty(t, tableName)
			var managementAuditColumns int
			require.NoError(t, database.QueryRowContext(ctx, `
SELECT COUNT(*) FROM information_schema.columns
WHERE table_schema='public' AND table_name='audit_logs'
  AND column_name IN ('action','actor_type','actor_id','details_json')`).Scan(&managementAuditColumns))
			if testCase.hasAudit {
				require.Equal(t, 4, managementAuditColumns)
			} else {
				require.Zero(t, managementAuditColumns)
			}
			_, err = database.ExecContext(ctx, `
INSERT INTO mask_rules(id,datasource_id,schema_name,table_name,column_name,sensitive_type,algo)
VALUES('duplicate',' ds-1 ','','','email','email','mask')`)
			require.Error(t, err)
			_, err = database.ExecContext(ctx, `
INSERT INTO mask_rules(id,datasource_id,schema_name,table_name,column_name,sensitive_type,algo)
VALUES('different-table',' ds-1 ','','other','email','email','mask')`)
			require.NoError(t, err)

			resetPostgresPublicSchema(t, ctx, database)
			migratePostgresThroughVersion(t, ctx, database, testCase.directory, 2)
			for _, values := range [][]any{
				{"dup-a", "ds-1", " Email "},
				{"dup-b", " ds-1 ", "email"},
			} {
				_, err = database.ExecContext(ctx, `
INSERT INTO mask_rules(id,datasource_id,table_name,column_name,sensitive_type,algo)
VALUES($1,$2,'users',$3,'email','mask')`, values...)
				require.NoError(t, err)
			}
			err = testCase.migrate(ctx, database)
			require.ErrorContains(t, err, `scope="ds-1" column="email" row_ids=[dup-a,dup-b]`)
			var version int
			require.NoError(t, database.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&version))
			require.Equal(t, 2, version)
		})
	}

	t.Run("independent audit postgres v1 to v2", func(t *testing.T) {
		ctx := dockerTestContext(t)
		dsn := startPostgres18StoreContainer(t, ctx, "agentsql_t38_audit", "audit-password")
		database, err := sql.Open("pgx", dsn)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, database.Close()) })
		migratePostgresThroughVersion(t, ctx, database, "migrations/audit/postgres", 1)
		_, err = database.ExecContext(ctx, `INSERT INTO audit_logs(decision) VALUES('allow')`)
		require.NoError(t, err)
		require.NoError(t, MigrateAudit(ctx, database, DialectPostgres))
		var action, actorType, actorID, detailsJSON sql.NullString
		require.NoError(t, database.QueryRowContext(ctx, `
SELECT action, actor_type, actor_id, details_json FROM audit_logs LIMIT 1`).Scan(
			&action, &actorType, &actorID, &detailsJSON,
		))
		require.False(t, action.Valid)
		repository := &AuditLogRepository{repositoryBase: repositoryBase{db: database, dialect: DialectPostgres}}
		inserted, err := repository.Insert(ctx, model.AuditLog{
			Decision: "allow", Action: stringPointerStoreTest("discover"),
			ActorType: stringPointerStoreTest("admin"), ActorID: stringPointerStoreTest("root"),
			DetailsJSON: stringPointerStoreTest(`{"findings_count":1}`),
		})
		require.NoError(t, err)
		require.Equal(t, `{"findings_count":1}`, *inserted.DetailsJSON)
	})

	t.Run("separate metadata and audit layout", func(t *testing.T) {
		ctx := dockerTestContext(t)
		metadataDSN := startPostgres18StoreContainer(t, ctx, "agentsql_t38_meta", "meta-password")
		auditDSN := startPostgres18StoreContainer(t, ctx, "agentsql_t38_separate_audit", "audit-password")
		opened, err := OpenMetadata(ctx, MetadataOptions{
			Driver: DialectPostgres, PostgresDSN: metadataDSN, AutoMigrate: true,
			Audit: AuditOptions{Separate: true, Driver: DialectPostgres, PostgresDSN: auditDSN, AutoMigrate: true},
		}, []byte(testSecret))
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, opened.Close()) })
		metadataCurrent, metadataLatest, err := MetadataMigrationVersions(ctx, opened.metaDB, DialectPostgres, true)
		require.NoError(t, err)
		require.Equal(t, 7, metadataCurrent)
		require.Equal(t, 7, metadataLatest)
		auditCurrent, auditLatest, err := AuditMigrationVersions(ctx, opened.auditDB, DialectPostgres)
		require.NoError(t, err)
		require.Equal(t, 5, auditCurrent)
		require.Equal(t, 5, auditLatest)
	})
}

func migratePostgresThroughVersion(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	directory string,
	maximum int,
) {
	t.Helper()
	_, err := database.ExecContext(ctx, postgresSchemaMigrationsDDL)
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
		require.NoError(t, applyMigration(ctx, database, DialectPostgres, version, string(contents)))
	}
}

func resetPostgresPublicSchema(t *testing.T, ctx context.Context, database *sql.DB) {
	t.Helper()
	_, err := database.ExecContext(ctx, `DROP SCHEMA public CASCADE`)
	require.NoError(t, err)
	_, err = database.ExecContext(ctx, `CREATE SCHEMA public`)
	require.NoError(t, err)
}

func stringPointerStoreTest(value string) *string { return &value }

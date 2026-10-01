package store

import (
	"database/sql"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestPostgres18B2PolicyAtomicTriggerProcedureAndFenceE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("postgres:18 B2 S1 E2E is an integration test")
	}
	ctx := dockerTestContext(t)
	dsn := startPostgres18StoreContainer(t, ctx, "agentsql_b2_s1", "b2-password")
	opened, err := OpenMetadata(ctx, MetadataOptions{Driver: DialectPostgres, PostgresDSN: dsn, MaxOpenConns: 4, AutoMigrate: true}, []byte(testSecret))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, opened.Close()) })
	agent, datasource := createPolicyDependencies(t, opened)
	created, err := opened.Policies().Create(ctx, model.Policy{
		ID: "pg-b2", AgentID: agent.ID, DatasourceID: datasource.ID, ObjectType: "table", ObjectName: "public.orders", Action: "allow",
		RelationBinding:   &model.RelationPolicyBinding{ID: "pg-binding", RelationName: "orders"},
		ColumnPermissions: []model.PolicyColumnPermission{{RelationEnrollmentID: "e1", ColumnOrdinal: 1, ColumnName: "id", ColumnTypeDigest: "int8", Usage: "output"}},
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), created.Revision, "guarded repository batch bumps exactly once")

	_, err = opened.metaDB.ExecContext(ctx, `INSERT INTO policy_column_permissions
 (policy_id,relation_enrollment_id,column_ordinal,column_name,column_type_digest,usage,parent_revision)
 VALUES($1,$2,$3,$4,$5,$6,$7)`, created.ID, "e1", 2, "tenant_id", "int8", "reference", created.Revision)
	require.NoError(t, err)
	afterDirect, err := opened.Policies().Get(ctx, created.ID)
	require.NoError(t, err)
	require.Equal(t, created.Revision+1, afterDirect.Revision, "direct child DML trigger bumps parent")

	var procedureRevision int64
	err = opened.metaDB.QueryRowContext(ctx, `SELECT agentsql_replace_policy_permissions($1,$2,$3::jsonb)`, created.ID, afterDirect.Revision,
		`[{"relation_enrollment_id":"e1","column_ordinal":3,"column_name":"amount","column_type_digest":"numeric","usage":"output"}]`).Scan(&procedureRevision)
	require.NoError(t, err)
	require.Equal(t, afterDirect.Revision+1, procedureRevision)
	afterProcedure, err := opened.Policies().Get(ctx, created.ID)
	require.NoError(t, err)
	require.Len(t, afterProcedure.ColumnPermissions, 1)
	require.Equal(t, procedureRevision, afterProcedure.ColumnPermissions[0].ParentRevision)

	now := time.Now().UTC().Truncate(time.Microsecond)
	instance, err := opened.Fence().Heartbeat(ctx, RuntimeInstance{InstanceID: "pg-runtime", ProtocolVersion: 2, ArtifactDigest: "artifact"}, now, time.Minute)
	require.NoError(t, err)
	snapshot, err := opened.Fence().BeginRead(ctx, 2, instance.InstanceID, now)
	require.NoError(t, err)
	require.NoError(t, snapshot.FinalCheck(ctx, now.Add(time.Second)))
	require.NoError(t, snapshot.Close())

	require.NoError(t, RollbackMetadataMigration(ctx, opened.metaDB, DialectPostgres, false, 11))
	require.NoError(t, RollbackMetadataMigration(ctx, opened.metaDB, DialectPostgres, false, 10))
	require.NoError(t, RollbackMetadataMigration(ctx, opened.metaDB, DialectPostgres, false, 9))
	current, latest, err := MetadataMigrationVersions(ctx, opened.metaDB, DialectPostgres, false)
	require.NoError(t, err)
	require.Equal(t, 8, current)
	require.Equal(t, 11, latest)
	require.NoError(t, RollbackMetadataMigration(ctx, opened.metaDB, DialectPostgres, false, 9))
	require.NoError(t, Migrate(ctx, opened.metaDB, DialectPostgres))
}

func TestPostgres18B2LegacyPreflightRollsBackClaimAndDDL(t *testing.T) {
	if testing.Short() {
		t.Skip("postgres:18 B2 migration rollback E2E is an integration test")
	}
	ctx := dockerTestContext(t)
	dsn := startPostgres18StoreContainer(t, ctx, "agentsql_b2_preflight", "b2-password")
	database, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	migratePostgresThroughVersion(t, ctx, database, "migrations/postgres", 8)
	_, err = database.ExecContext(ctx, `INSERT INTO agents(id,name,api_key_hash) VALUES('a','a','h');
INSERT INTO datasources(id,name,db_type,host,port,database,username,password_enc) VALUES('d','d','postgres','h',5432,'d','u','p');
INSERT INTO policies(id,agent_id,datasource_id,object_type,object_name,columns,action) VALUES('p','a','d','table','public.t','id,,email','allow')`)
	require.NoError(t, err)
	err = Migrate(ctx, database, DialectPostgres)
	require.ErrorContains(t, err, "empty token")
	var current int
	require.NoError(t, database.QueryRowContext(ctx, `SELECT max(version) FROM schema_migrations`).Scan(&current))
	require.Equal(t, 8, current)
	var exists bool
	require.NoError(t, database.QueryRowContext(ctx, `SELECT to_regclass('public.policy_column_permissions') IS NOT NULL`).Scan(&exists))
	require.False(t, exists)
}

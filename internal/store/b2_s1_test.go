package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestB2PolicyParentChildrenAtomicCASAndCascade(t *testing.T) {
	opened := openTestStore(t)
	agent, datasource := createPolicyDependencies(t, opened)
	ctx := context.Background()
	policy := model.Policy{
		ID: "b2-policy", AgentID: agent.ID, DatasourceID: datasource.ID,
		ObjectType: "table", ObjectName: "public.orders", Action: "allow",
		RelationBinding: &model.RelationPolicyBinding{ID: "binding-1", RelationName: "orders"},
		ColumnPermissions: []model.PolicyColumnPermission{
			{RelationEnrollmentID: "enrollment-1", ColumnOrdinal: 1, ColumnName: "id", ColumnTypeDigest: "int8", Usage: "output"},
			{RelationEnrollmentID: "enrollment-1", ColumnOrdinal: 1, ColumnName: "id", ColumnTypeDigest: "int8", Usage: "reference"},
		},
	}
	created, err := opened.Policies().Create(ctx, policy)
	require.NoError(t, err)
	require.Equal(t, int64(1), created.Revision)
	require.Len(t, created.ColumnPermissions, 2)
	require.NotNil(t, created.RelationBinding)
	require.Equal(t, "binding-1", *created.RelationBindingID)

	first, second := created, created
	first.Action, second.Action = "deny", "allow"
	updated, err := opened.Policies().UpdateIfRevision(ctx, first, created.Revision)
	require.NoError(t, err)
	require.Equal(t, int64(2), updated.Revision)
	_, err = opened.Policies().UpdateIfRevision(ctx, second, created.Revision)
	require.ErrorIs(t, err, ErrRevisionMismatch)

	// A direct child write cannot be silent: the defensive trigger bumps the parent.
	_, err = opened.metaDB.ExecContext(ctx, `INSERT INTO policy_column_permissions
 (policy_id,relation_enrollment_id,column_ordinal,column_name,column_type_digest,usage,parent_revision)
 VALUES(?,?,?,?,?,?,?)`, created.ID, "enrollment-1", 2, "tenant_id", "int8", "output", updated.Revision)
	require.NoError(t, err)
	direct, err := opened.Policies().Get(ctx, created.ID)
	require.NoError(t, err)
	require.Greater(t, direct.Revision, updated.Revision)

	require.NoError(t, opened.Policies().DeleteIfRevision(ctx, direct.ID, direct.Revision))
	for _, table := range []string{"relation_policy_bindings", "policy_column_permissions", "policy_column_permission_staging"} {
		var count int
		require.NoError(t, opened.metaDB.QueryRowContext(ctx, `SELECT count(*) FROM `+table+` WHERE policy_id=?`, direct.ID).Scan(&count))
		require.Zero(t, count)
	}
}

func TestB2RepositoryRejectsNewWildcard(t *testing.T) {
	opened := openTestStore(t)
	agent, datasource := createPolicyDependencies(t, opened)
	star := "*"
	_, err := opened.Policies().Create(context.Background(), model.Policy{
		ID: "repo-star", AgentID: agent.ID, DatasourceID: datasource.ID,
		ObjectType: "table", ObjectName: "public.t", Columns: &star, Action: "allow",
	})
	require.ErrorContains(t, err, "wildcard")
	_, err = opened.Policies().Get(context.Background(), "repo-star")
	require.ErrorIs(t, err, ErrNotFound)
}

func TestB2StagingFinalizesWithDiscoveryThenControlBusinessRevalidation(t *testing.T) {
	opened := openTestStore(t)
	agent, datasource := createPolicyDependencies(t, opened)
	legacy := "id"
	created, err := opened.Policies().Create(context.Background(), model.Policy{
		ID: "staged", AgentID: agent.ID, DatasourceID: datasource.ID,
		ObjectType: "table", ObjectName: "public.t", Columns: &legacy, Action: "allow",
	})
	require.NoError(t, err)
	require.Len(t, created.ColumnStaging, 2)
	events := make([]string, 0, 2)
	finalized, err := opened.Policies().FinalizeColumnBindingTwoPhase(context.Background(), created.ID, created.Revision,
		func(context.Context) error { events = append(events, "discovery-released"); return nil },
		func(context.Context) (RevalidatedColumnBinding, error) {
			events = append(events, "control-then-business-revalidated")
			return RevalidatedColumnBinding{
				Binding: model.RelationPolicyBinding{ID: "binding-final", RelationName: "t", Status: "healthy"},
				Permissions: []model.PolicyColumnPermission{
					{RelationEnrollmentID: "e1", ColumnOrdinal: 1, ColumnName: "id", ColumnTypeDigest: "int8", Usage: "output"},
					{RelationEnrollmentID: "e1", ColumnOrdinal: 1, ColumnName: "id", ColumnTypeDigest: "int8", Usage: "reference"},
				},
			}, nil
		})
	require.NoError(t, err)
	require.Equal(t, []string{"discovery-released", "control-then-business-revalidated"}, events)
	require.Empty(t, finalized.ColumnStaging)
	require.Len(t, finalized.ColumnPermissions, 2)
	require.Equal(t, created.Revision+1, finalized.Revision)
}

func TestB2LegacyCSVStagingMigrationAndPreflightRollback(t *testing.T) {
	ctx := context.Background()
	for _, invalid := range []bool{false, true} {
		database, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "b2.db"))
		require.NoError(t, err)
		database.SetMaxOpenConns(1)
		migrateSQLiteThroughVersion(t, ctx, database, "migrations/sqlite", 8)
		_, err = database.ExecContext(ctx, `INSERT INTO agents(id,name,api_key_hash) VALUES('a','a','h')`)
		require.NoError(t, err)
		_, err = database.ExecContext(ctx, `INSERT INTO datasources(id,name,db_type,host,port,database,username,password_enc) VALUES('d','d','postgres','h',5432,'d','u','p')`)
		require.NoError(t, err)
		columns := "id, email"
		if invalid {
			columns = "id,,email"
		}
		_, err = database.ExecContext(ctx, `INSERT INTO policies(id,agent_id,datasource_id,object_type,object_name,columns,action) VALUES('p','a','d','table','public.t',?,'allow')`, columns)
		require.NoError(t, err)
		err = Migrate(ctx, database, DialectSQLite)
		if invalid {
			require.ErrorContains(t, err, "empty token")
			var version int
			require.NoError(t, database.QueryRowContext(ctx, `SELECT max(version) FROM schema_migrations`).Scan(&version))
			require.Equal(t, 8, version)
			var count int
			require.NoError(t, database.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info('policies') WHERE name='revision'`).Scan(&count))
			require.Zero(t, count)
		} else {
			var count, distinctDigests int
			require.NoError(t, database.QueryRowContext(ctx, `SELECT count(*),count(DISTINCT source_csv_sha256) FROM policy_column_permission_staging WHERE policy_id='p'`).Scan(&count, &distinctDigests))
			require.Equal(t, 4, count)
			require.Equal(t, 1, distinctDigests)
			var minOrdinal, maxOrdinal int
			require.NoError(t, database.QueryRowContext(ctx, `SELECT min(token_ordinal),max(token_ordinal) FROM policy_column_permission_staging WHERE policy_id='p'`).Scan(&minOrdinal, &maxOrdinal))
			require.Equal(t, 1, minOrdinal)
			require.Equal(t, 2, maxOrdinal)
		}
		require.NoError(t, database.Close())
	}
}

func TestB2SQLiteDownAndReapplyAreSafe(t *testing.T) {
	opened := openTestStore(t)
	ctx := context.Background()
	require.NoError(t, RollbackMetadataMigration(ctx, opened.metaDB, DialectSQLite, false, 16))
	require.NoError(t, RollbackMetadataMigration(ctx, opened.metaDB, DialectSQLite, false, 15))
	require.NoError(t, RollbackMetadataMigration(ctx, opened.metaDB, DialectSQLite, false, 14))
	require.NoError(t, RollbackMetadataMigration(ctx, opened.metaDB, DialectSQLite, false, 13))
	require.NoError(t, RollbackMetadataMigration(ctx, opened.metaDB, DialectSQLite, false, 12))
	require.NoError(t, RollbackMetadataMigration(ctx, opened.metaDB, DialectSQLite, false, 11))
	require.NoError(t, RollbackMetadataMigration(ctx, opened.metaDB, DialectSQLite, false, 10))
	require.NoError(t, RollbackMetadataMigration(ctx, opened.metaDB, DialectSQLite, false, 9))
	current, latest, err := MetadataMigrationVersions(ctx, opened.metaDB, DialectSQLite, false)
	require.NoError(t, err)
	require.Equal(t, 8, current)
	require.Equal(t, 16, latest)
	require.NoError(t, RollbackMetadataMigration(ctx, opened.metaDB, DialectSQLite, false, 9), "same down is idempotent")
	require.NoError(t, Migrate(ctx, opened.metaDB, DialectSQLite))
	current, latest, err = MetadataMigrationVersions(ctx, opened.metaDB, DialectSQLite, false)
	require.NoError(t, err)
	require.Equal(t, 16, current)
	require.Equal(t, 16, latest)
}

func TestB2FenceLeaseExpiryIsFailClosed(t *testing.T) {
	opened := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	instance, err := opened.Fence().Heartbeat(ctx, RuntimeInstance{InstanceID: "runtime-1", ProtocolVersion: 2, ArtifactDigest: "artifact"}, now, time.Minute)
	require.NoError(t, err)
	require.Equal(t, int64(1), instance.Revision)
	snapshot, err := opened.Fence().BeginRead(ctx, 2, instance.InstanceID, now.Add(time.Second))
	require.NoError(t, err)
	require.NoError(t, snapshot.FinalCheck(ctx, now.Add(30*time.Second)))
	require.ErrorIs(t, snapshot.FinalCheck(ctx, now.Add(2*time.Minute)), ErrFenceLost)
	require.NoError(t, snapshot.Close())
	_, err = opened.Fence().Heartbeat(ctx, instance, now.Add(2*time.Minute), time.Minute)
	require.ErrorIs(t, err, ErrFenceLost)
}

func TestB2Protocol3ActivationAndHeartbeatLifecycle(t *testing.T) {
	opened := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	readiness, err := opened.Fence().Protocol3Readiness(ctx)
	require.NoError(t, err)
	require.True(t, readiness.Ready())

	instance, err := opened.Fence().ActivateProtocol3(ctx, Protocol3Activation{
		InstanceID: "runtime-p3", ArtifactDigest: "artifact-p3", BinderReady: true,
		CatalogReady: true, ReservationReady: true, ExpectedETag: readiness.ETag, Now: now, Lease: time.Minute,
	})
	require.NoError(t, err)
	require.Equal(t, 3, instance.ProtocolVersion)
	require.Equal(t, "active", instance.Status)

	snapshot, err := opened.Fence().BeginRead(ctx, 3, instance.InstanceID, now.Add(time.Second))
	require.NoError(t, err)
	require.NoError(t, snapshot.FinalCheck(ctx, now.Add(30*time.Second)))
	require.ErrorIs(t, snapshot.FinalCheck(ctx, now.Add(2*time.Minute)), ErrFenceLost)
	require.NoError(t, snapshot.Close())

	_, err = opened.Fence().BeginRead(ctx, 2, instance.InstanceID, now.Add(time.Second))
	require.ErrorIs(t, err, ErrProtocolBlocked)
	_, err = opened.Fence().Heartbeat(ctx, instance, now.Add(2*time.Minute), time.Minute)
	require.ErrorIs(t, err, ErrFenceLost, "an expired runtime cannot resurrect itself")
}

func TestB2Protocol3ActivationGateLeavesFactoryFenceOff(t *testing.T) {
	opened := openTestStore(t)
	ctx := context.Background()
	agent, datasource := createPolicyDependencies(t, opened)
	legacy := "id"
	_, err := opened.Policies().Create(ctx, model.Policy{ID: "pending-b2", AgentID: agent.ID,
		DatasourceID: datasource.ID, ObjectType: "column", ObjectName: "public.t", Columns: &legacy, Action: "allow"})
	require.NoError(t, err)
	readiness, err := opened.Fence().Protocol3Readiness(ctx)
	require.NoError(t, err)
	require.False(t, readiness.Ready())
	require.NotZero(t, readiness.StagingRows)
	require.NotZero(t, readiness.IncompleteBindings)

	now := time.Now().UTC().Truncate(time.Microsecond)
	_, err = opened.Fence().ActivateProtocol3(ctx, Protocol3Activation{InstanceID: "blocked-p3", ExpectedETag: readiness.ETag,
		ArtifactDigest: "artifact", BinderReady: true, CatalogReady: true,
		ReservationReady: true, Now: now, Lease: time.Minute})
	require.ErrorIs(t, err, ErrActivationGate)
	var state string
	var maximum int
	require.NoError(t, opened.metaDB.QueryRowContext(ctx, `SELECT state,max_writer_protocol FROM control_plane_compat WHERE fence_key='global'`).Scan(&state, &maximum))
	require.Equal(t, "protocol2", state)
	require.Equal(t, 2, maximum)
	var runtimes int
	require.NoError(t, opened.metaDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM runtime_instances WHERE instance_id='blocked-p3'`).Scan(&runtimes))
	require.Zero(t, runtimes)
}

func TestB2Protocol3ReadinessIgnoresStagedMySQLUnsupportedMarker(t *testing.T) {
	opened := openTestStore(t)
	ctx := context.Background()
	agent, datasource := createPolicyDependencies(t, opened)
	datasource.ID = "b2-mysql-unsupported"
	datasource.DBType = "mysql"
	_, err := opened.Datasources().Create(ctx, datasource, "mysql-password")
	require.NoError(t, err)
	columns := "full_name"
	_, err = opened.Policies().Create(ctx, model.Policy{ID: "mysql-column-marker", AgentID: agent.ID,
		DatasourceID: datasource.ID, ObjectType: "column", ObjectName: "customers", Columns: &columns, Action: "allow"})
	require.NoError(t, err)

	readiness, err := opened.Fence().PrepareProtocol3Activation(ctx)
	require.NoError(t, err)
	require.True(t, readiness.Ready())
	require.Zero(t, readiness.StagingRows)
	require.Zero(t, readiness.IncompleteBindings)
}

func TestB2Protocol3ActivationRequiresEveryRuntimeAttestation(t *testing.T) {
	for _, test := range []struct {
		name        string
		binder      bool
		catalog     bool
		reservation bool
	}{
		{name: "binder", catalog: true, reservation: true},
		{name: "catalog", binder: true, reservation: true},
		{name: "reservation", binder: true, catalog: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			opened := openTestStore(t)
			now := time.Now().UTC().Truncate(time.Microsecond)
			readiness, readinessErr := opened.Fence().PrepareProtocol3Activation(context.Background())
			require.NoError(t, readinessErr)
			_, err := opened.Fence().ActivateProtocol3(context.Background(), Protocol3Activation{
				InstanceID: "blocked-" + test.name, ArtifactDigest: "artifact",
				ExpectedETag: readiness.ETag,
				BinderReady:  test.binder, CatalogReady: test.catalog, ReservationReady: test.reservation,
				Now: now, Lease: time.Minute,
			})
			require.ErrorIs(t, err, ErrActivationGate)
			var state string
			require.NoError(t, opened.metaDB.QueryRow(`SELECT state FROM control_plane_compat WHERE fence_key='global'`).Scan(&state))
			require.Equal(t, "protocol2", state)
		})
	}
}

func TestB2Protocol3RuntimeRegistrationCannotBypassActivationOrArtifactFence(t *testing.T) {
	opened := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	readiness, err := opened.Fence().PrepareProtocol3Activation(ctx)
	require.NoError(t, err)
	first, err := opened.Fence().ActivateProtocol3(ctx, Protocol3Activation{
		InstanceID: "runtime-a", ArtifactDigest: "artifact-a", BinderReady: true,
		CatalogReady: true, ReservationReady: true, ExpectedETag: readiness.ETag, Now: now, Lease: time.Minute,
	})
	require.NoError(t, err)

	_, err = opened.Fence().Heartbeat(ctx, RuntimeInstance{
		InstanceID: "unregistered", ProtocolVersion: 3, ArtifactDigest: first.ArtifactDigest,
	}, now.Add(time.Second), time.Minute)
	require.ErrorIs(t, err, ErrFenceLost, "protocol 3 registration must pass the activation gate")

	for _, instanceID := range []string{first.InstanceID, "runtime-b"} {
		readiness, readinessErr := opened.Fence().PrepareProtocol3Activation(ctx)
		require.NoError(t, readinessErr)
		_, err = opened.Fence().ActivateProtocol3(ctx, Protocol3Activation{
			InstanceID: instanceID, ArtifactDigest: "artifact-b", BinderReady: true,
			CatalogReady: true, ReservationReady: true, ExpectedETag: readiness.ETag, Now: now.Add(time.Second), Lease: time.Minute,
		})
		require.ErrorIs(t, err, ErrActivationGate, "a live artifact must not be replaced or joined by a different artifact")
	}

	readiness, err = opened.Fence().PrepareProtocol3Activation(ctx)
	require.NoError(t, err)
	second, err := opened.Fence().ActivateProtocol3(ctx, Protocol3Activation{
		InstanceID: "runtime-b", ArtifactDigest: first.ArtifactDigest, BinderReady: true,
		CatalogReady: true, ReservationReady: true, ExpectedETag: readiness.ETag, Now: now.Add(time.Second), Lease: time.Minute,
	})
	require.NoError(t, err)
	require.Equal(t, first.ArtifactDigest, second.ArtifactDigest)
}

func TestB2Protocol3ActivationRejectsFrozenControlFence(t *testing.T) {
	opened := openTestStore(t)
	ctx := context.Background()
	_, err := opened.metaDB.ExecContext(ctx, `UPDATE control_plane_compat SET state='frozen' WHERE fence_key='global'`)
	require.NoError(t, err)
	readiness, err := opened.Fence().PrepareProtocol3Activation(ctx)
	require.NoError(t, err)
	_, err = opened.Fence().ActivateProtocol3(ctx, Protocol3Activation{
		InstanceID: "blocked-frozen", ArtifactDigest: "artifact", BinderReady: true,
		CatalogReady: true, ReservationReady: true, ExpectedETag: readiness.ETag, Now: time.Now().UTC(), Lease: time.Minute,
	})
	require.ErrorIs(t, err, ErrActivationGate)
	var count int
	require.NoError(t, opened.metaDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM runtime_instances WHERE instance_id='blocked-frozen'`).Scan(&count))
	require.Zero(t, count)
}

func TestB2Protocol3ActivationRejectsStaleOrWeakETagWithoutMutation(t *testing.T) {
	opened := openTestStore(t)
	ctx := context.Background()
	prepared, err := opened.Fence().PrepareProtocol3Activation(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, prepared.ETag)
	require.Equal(t, '"', rune(prepared.ETag[0]))

	_, err = opened.metaDB.ExecContext(ctx, `UPDATE control_plane_compat SET revision=revision+1 WHERE fence_key='global'`)
	require.NoError(t, err)
	_, err = opened.Fence().ActivateProtocol3(ctx, Protocol3Activation{InstanceID: "stale-etag", ArtifactDigest: "artifact",
		ExpectedETag: prepared.ETag, BinderReady: true, CatalogReady: true, ReservationReady: true,
		Now: time.Now().UTC(), Lease: time.Minute})
	require.ErrorIs(t, err, ErrActivationGate)

	for _, etag := range []string{"", "*", `W/"weak"`, strings.Trim(prepared.ETag, `"`)} {
		_, err = opened.Fence().ActivateProtocol3(ctx, Protocol3Activation{InstanceID: "weak-etag", ArtifactDigest: "artifact",
			ExpectedETag: etag, BinderReady: true, CatalogReady: true, ReservationReady: true,
			Now: time.Now().UTC(), Lease: time.Minute})
		require.ErrorIs(t, err, ErrActivationGate, etag)
	}
	var state string
	var runtimes int
	require.NoError(t, opened.metaDB.QueryRowContext(ctx, `SELECT state FROM control_plane_compat WHERE fence_key='global'`).Scan(&state))
	require.NoError(t, opened.metaDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM runtime_instances`).Scan(&runtimes))
	require.Equal(t, "protocol2", state)
	require.Zero(t, runtimes)
}

func TestB2Protocol3RecoveryRequiresFreshPrepareAndReactivation(t *testing.T) {
	opened := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	firstPlan, err := opened.Fence().PrepareProtocol3Activation(ctx)
	require.NoError(t, err)
	first, err := opened.Fence().ActivateProtocol3(ctx, Protocol3Activation{InstanceID: "runtime-before-fault",
		ArtifactDigest: "signed-artifact", ExpectedETag: firstPlan.ETag, BinderReady: true, CatalogReady: true,
		ReservationReady: true, Now: now, Lease: time.Second})
	require.NoError(t, err)
	_, err = opened.Fence().BeginRead(ctx, 3, first.InstanceID, now.Add(2*time.Second))
	require.ErrorIs(t, err, ErrFenceLost)

	// The expired identity cannot heal itself. Recovery takes a fresh phase-one
	// snapshot and registers a new identity against the same artifact.
	_, err = opened.Fence().Heartbeat(ctx, first, now.Add(2*time.Second), time.Minute)
	require.ErrorIs(t, err, ErrFenceLost)
	recoveryPlan, err := opened.Fence().PrepareProtocol3Activation(ctx)
	require.NoError(t, err)
	require.NotEqual(t, firstPlan.ETag, recoveryPlan.ETag)
	recovered, err := opened.Fence().ActivateProtocol3(ctx, Protocol3Activation{InstanceID: "runtime-after-recovery",
		ArtifactDigest: first.ArtifactDigest, ExpectedETag: recoveryPlan.ETag, BinderReady: true, CatalogReady: true,
		ReservationReady: true, Now: now.Add(2 * time.Second), Lease: time.Minute})
	require.NoError(t, err)
	snapshot, err := opened.Fence().BeginRead(ctx, 3, recovered.InstanceID, now.Add(3*time.Second))
	require.NoError(t, err)
	require.NoError(t, snapshot.FinalCheck(ctx, now.Add(3*time.Second)))
	require.NoError(t, snapshot.Close())
}

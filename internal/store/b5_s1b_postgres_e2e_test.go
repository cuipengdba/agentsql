package store

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/b5"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	postgrescontainer "github.com/testcontainers/testcontainers-go/modules/postgres"
)

func TestB5S1bPostgres14And18MigrationAndStore(t *testing.T) {
	if testing.Short() {
		t.Skip("postgres:14/18 B5 S1b migration E2E is an integration test")
	}
	for _, image := range []string{"postgres:14", "postgres:18"} {
		image := image
		for _, separate := range []bool{false, true} {
			separate := separate
			name := image + "/combined"
			version := 10
			if separate {
				name = image + "/metadata"
				version = 9
			}
			t.Run(name, func(t *testing.T) {
				ctx := dockerTestContext(t)
				dsn := startB5PostgresContainer(t, ctx, image)
				db, err := sql.Open("pgx", dsn)
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, db.Close()) })
				require.NoError(t, MigrateMetadata(ctx, db, DialectPostgres, separate))

				for _, table := range []string{"b5_sessions", "b5_transactions", "b5_dml_grants", "b5_result_receipts", "b5_tx_events"} {
					var exists bool
					require.NoError(t, db.QueryRowContext(ctx, `SELECT to_regclass('public.' || $1) IS NOT NULL`, table).Scan(&exists))
					require.True(t, exists, table)
				}
				_, err = db.ExecContext(ctx, `INSERT INTO agents(id,name,api_key_hash) VALUES('b5-agent','b5','hash')`)
				require.NoError(t, err)
				now := time.Now().UTC().Truncate(time.Microsecond)
				store := &Store{metaDB: db, metaDriver: DialectPostgres}
				session, err := store.B5Sessions().Create(ctx, B5Session{
					SessionID: "pg-session", AgentID: "b5-agent", TenantID: "tenant", PrincipalID: "principal",
					OwnerInstanceID: "instance", OwnerEpoch: 1, ContinuationSchemaID: "agentsql.b5.continuation.v2",
					ContinuationSchemaVersion: 2, ContinuationKeyCiphertext: "ciphertext", ContinuationHMACDigest: bytesOf(32, 1),
					StickyRoute: "instance", Status: b5.SessionReady, IdleExpiresAt: now.Add(10 * time.Minute), AbsoluteExpiresAt: now.Add(time.Hour),
				})
				require.NoError(t, err)
				_, err = store.B5Sessions().CASStatus(ctx, session.SessionID, session.Revision, b5.SessionReady, b5.SessionActive, now.Add(9*time.Minute))
				require.NoError(t, err)

				// PostgreSQL DDL and the migration claim must roll back together.
				err = applyMigration(ctx, db, DialectPostgres, 99, `CREATE TABLE b5_atomic_pg_probe(id BIGINT); INSERT INTO table_that_does_not_exist VALUES(1);`)
				require.Error(t, err)
				var exists bool
				require.NoError(t, db.QueryRowContext(ctx, `SELECT to_regclass('public.b5_atomic_pg_probe') IS NOT NULL`).Scan(&exists))
				require.False(t, exists)

				require.NoError(t, RollbackMetadataMigration(ctx, db, DialectPostgres, separate, version))
				require.NoError(t, RollbackMetadataMigration(ctx, db, DialectPostgres, separate, version))
				require.NoError(t, MigrateMetadata(ctx, db, DialectPostgres, separate))
			})
		}
	}
}

func startB5PostgresContainer(t *testing.T, ctx context.Context, image string) string {
	t.Helper()
	const username = "agentsql"
	const password = "b5-password"
	container, err := postgrescontainer.Run(ctx, image,
		postgrescontainer.WithDatabase("agentsql_b5"),
		postgrescontainer.WithUsername(username),
		postgrescontainer.WithPassword(password),
		postgrescontainer.BasicWaitStrategies(),
	)
	if err != nil {
		if container != nil {
			testcontainers.CleanupContainer(t, container)
		}
		require.NoError(t, err, "start %s", image)
	}
	testcontainers.CleanupContainer(t, container)
	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "5432/tcp")
	require.NoError(t, err)
	return fmt.Sprintf("postgres://%s:%s@%s:%s/agentsql_b5?sslmode=disable", username, password, host, port.Port())
}

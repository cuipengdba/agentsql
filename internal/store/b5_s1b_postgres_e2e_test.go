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
			versions := []int{14, 13, 12, 11, 10}
			if separate {
				name = image + "/metadata"
				versions = []int{13, 12, 11, 10, 9}
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
				_, err = db.ExecContext(ctx, `INSERT INTO datasources(id,name,db_type,host,port,database,username,password_enc) VALUES('pg-datasource','b5','postgres','db',5432,'app','user','ciphertext')`)
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
				session, err = store.B5Sessions().CASStatus(ctx, session.SessionID, session.Revision, b5.SessionReady, b5.SessionActive, now.Add(9*time.Minute))
				require.NoError(t, err)
				session, err = store.B5Sessions().CASOwner(ctx, session.SessionID, session.Revision, "instance", 1, "instance-2", "instance-2", "ciphertext-2", bytesOf(32, 2))
				require.NoError(t, err)
				require.Equal(t, uint64(2), session.OwnerEpoch)
				sessionPage, err := store.B5Sessions().ListPage(ctx, B5SessionFilter{Status: "ACTIVE", Owner: "instance-2", Query: "pg-session"}, 1, 10)
				require.NoError(t, err)
				require.Equal(t, int64(1), sessionPage.Total)
				require.Equal(t, "pg-session", sessionPage.List[0].SessionID)

				transaction, err := store.B5Transactions().Create(ctx, B5Transaction{
					TransactionID: "pg-transaction", SessionID: session.SessionID, DatasourceID: "pg-datasource",
					Status: b5.TransactionActive, Phase: b5.PhaseActive, PlanDigest: bytesOf(32, 3), OwnerEpoch: session.OwnerEpoch,
					IdleDeadline: now.Add(time.Minute), WallDeadline: now.Add(time.Hour), TransactionSeq: 1,
					PreviousTxEventDigest: bytesOf(32, 4),
				})
				require.NoError(t, err)
				transactionPage, err := store.B5Transactions().ListPage(ctx, B5TransactionFilter{Status: "ACTIVE", Phase: "ACTIVE", DatasourceID: "pg-datasource", Query: "pg-transaction"}, 1, 10)
				require.NoError(t, err)
				require.Equal(t, int64(1), transactionPage.Total)
				require.Equal(t, transaction.TransactionID, transactionPage.List[0].TransactionID)
				require.NoError(t, store.B5TxEvents().Append(ctx, B5TxEvent{TransactionID: transaction.TransactionID, TransactionSeq: 1, EventUUID: bytesOf(16, 5), EventType: "tx_begin", EventSchemaID: "agentsql.b5.event.v4", EventSchemaVersion: 4, PreviousTxEventDigest: bytesOf(32, 4), EventDigest: bytesOf(32, 6), CanonicalEvent: []byte("event")}))
				events, err := store.B5TxEvents().ListByTransaction(ctx, transaction.TransactionID, 10)
				require.NoError(t, err)
				require.Len(t, events, 1)

				// PostgreSQL DDL and the migration claim must roll back together.
				err = applyMigration(ctx, db, DialectPostgres, 99, `CREATE TABLE b5_atomic_pg_probe(id BIGINT); INSERT INTO table_that_does_not_exist VALUES(1);`)
				require.Error(t, err)
				var exists bool
				require.NoError(t, db.QueryRowContext(ctx, `SELECT to_regclass('public.b5_atomic_pg_probe') IS NOT NULL`).Scan(&exists))
				require.False(t, exists)

				for _, version := range versions {
					require.NoError(t, RollbackMetadataMigration(ctx, db, DialectPostgres, separate, version))
				}
				require.NoError(t, RollbackMetadataMigration(ctx, db, DialectPostgres, separate, versions[len(versions)-1]))
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

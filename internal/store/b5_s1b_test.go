package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/b5"
	"github.com/stretchr/testify/require"
)

func TestB5S1bSQLiteStoreCRUDCASAndIdempotency(t *testing.T) {
	opened := openTestStore(t)
	ctx := context.Background()
	_, err := opened.metaDB.ExecContext(ctx, `INSERT INTO agents(id,name,api_key_hash) VALUES('b5-agent','b5','hash')`)
	require.NoError(t, err)
	_, err = opened.metaDB.ExecContext(ctx, `INSERT INTO datasources(id,name,db_type,host,port,database,username,password_enc) VALUES('b5-ds','b5','postgres','localhost',5432,'db','user','secret')`)
	require.NoError(t, err)
	_, err = opened.metaDB.ExecContext(ctx, `INSERT INTO policies(id,agent_id,datasource_id,object_type,object_name,action) VALUES('b5-policy','b5-agent','b5-ds','table','public.orders','allow')`)
	require.NoError(t, err)

	now := time.Now().UTC().Truncate(time.Microsecond)
	session, err := opened.B5Sessions().Create(ctx, B5Session{
		SessionID: "session-1", AgentID: "b5-agent", TenantID: "tenant-1", PrincipalID: "principal-1",
		OwnerInstanceID: "instance-1", OwnerEpoch: 1, ContinuationSchemaID: "agentsql.b5.continuation.v2",
		ContinuationSchemaVersion: 2, ContinuationKeyCiphertext: "kms-ciphertext", ContinuationHMACDigest: bytesOf(32, 1),
		StickyRoute: "instance-1", Status: b5.SessionReady, IdleExpiresAt: now.Add(10 * time.Minute), AbsoluteExpiresAt: now.Add(time.Hour),
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), session.Revision)
	session, err = opened.B5Sessions().CASStatus(ctx, session.SessionID, session.Revision, b5.SessionReady, b5.SessionActive, now.Add(9*time.Minute))
	require.NoError(t, err)
	session, err = opened.B5Sessions().CASOwner(ctx, session.SessionID, session.Revision, "instance-1", 1, "instance-2", "instance-2", "kms-ciphertext-epoch-2", bytesOf(32, 10))
	require.NoError(t, err)
	require.Equal(t, uint64(2), session.OwnerEpoch)
	require.Equal(t, "instance-2", session.OwnerInstanceID)
	_, err = opened.B5Sessions().CASStatus(ctx, session.SessionID, 1, b5.SessionReady, b5.SessionActive, now.Add(time.Minute))
	require.ErrorIs(t, err, ErrB5CASConflict)

	tx, err := opened.B5Transactions().Create(ctx, B5Transaction{
		TransactionID: "tx-1", SessionID: session.SessionID, DatasourceID: "b5-ds", Status: b5.TransactionPending,
		Phase: b5.PhaseReady, PlanDigest: bytesOf(32, 2), OwnerEpoch: 1, IdleDeadline: now.Add(15 * time.Second), WallDeadline: now.Add(time.Minute),
	})
	require.NoError(t, err)
	tx, err = opened.B5Transactions().CASState(ctx, tx.TransactionID, tx.Revision, b5.TransactionPending, b5.PhaseReady, b5.TransactionPending, b5.PhasePlanReady)
	require.NoError(t, err)
	require.Equal(t, b5.PhasePlanReady, tx.Phase)
	_, err = opened.B5Transactions().CASState(ctx, tx.TransactionID, tx.Revision, b5.TransactionPending, b5.PhasePlanReady, b5.TransactionActive, b5.PhaseTerminal)
	require.ErrorIs(t, err, ErrB5InvalidTransition)

	grant, err := opened.B5DMLGrants().Create(ctx, B5DMLGrant{
		GrantID: "grant-1", PolicyID: "b5-policy", PolicyRevision: 1, PrincipalID: "principal-1", DatasourceID: "b5-ds",
		Effect: b5.GrantAllow, Element: b5.GrantElementAction, Action: b5.ActionUpdate, DatabaseOID: 1, RelationOID: 42,
		RelationKind: "r", SchemaName: "public", RelationName: "orders", CatalogFingerprint: "catalog-v1",
		ProofSchemaID: b5.DMLGrantProofSchemaID, ProofSchemaVersion: b5.DMLGrantProofSchemaVersion, ProofDigest: bytesOf(32, 3),
	})
	require.NoError(t, err)
	grants, err := opened.B5DMLGrants().List(ctx, "principal-1", "b5-ds", b5.ActionUpdate, 10)
	require.NoError(t, err)
	require.Len(t, grants, 1)
	require.Equal(t, grant.GrantID, grants[0].GrantID)
	grant.CatalogFingerprint = "catalog-v2"
	grant, err = opened.B5DMLGrants().UpdateIfRevision(ctx, grant, grant.Revision)
	require.NoError(t, err)
	require.Equal(t, "catalog-v2", grant.CatalogFingerprint)
	_, err = opened.B5DMLGrants().UpdateIfRevision(ctx, grant, 1)
	require.ErrorIs(t, err, ErrB5CASConflict)

	key := B5ReceiptKey{SessionID: session.SessionID, RequestID: "request-1", EventUUID: bytesOf(16, 4), AttemptGeneration: 1}
	receipt := B5ResultReceipt{Key: key, SchemaID: b5.ResultReceiptSchemaID, SchemaVersion: b5.ResultReceiptSchemaVersion,
		BusinessEventDigest: bytesOf(32, 5), WALAppendReceiptDigest: bytesOf(32, 6), ReportedDurability: b5.DurabilityLost,
		AppendConfirmation: b5.AppendUnknown, Reconciliation: b5.ReconciliationNone, DeliveryStatus: b5.DeliveryPrepared}
	stored, inserted, err := opened.B5ResultReceipts().PutWriteOnce(ctx, receipt)
	require.NoError(t, err)
	require.True(t, inserted)
	_, inserted, err = opened.B5ResultReceipts().PutWriteOnce(ctx, receipt)
	require.NoError(t, err)
	require.False(t, inserted)
	conflict := receipt
	conflict.BusinessEventDigest = bytesOf(32, 9)
	_, _, err = opened.B5ResultReceipts().PutWriteOnce(ctx, conflict)
	require.ErrorIs(t, err, ErrB5ReceiptConflict)
	timeout := b5.AppendTimeout
	started := b5.DeliverySendStarted
	stored, err = opened.B5ResultReceipts().Advance(ctx, key, stored.Revision, B5ReceiptAdvance{AppendConfirmation: &timeout, DeliveryStatus: &started})
	require.NoError(t, err)
	unknown := b5.AppendUnknown
	_, err = opened.B5ResultReceipts().Advance(ctx, key, stored.Revision, B5ReceiptAdvance{AppendConfirmation: &unknown})
	require.ErrorIs(t, err, ErrB5InvalidTransition)

	require.NoError(t, opened.B5TxEvents().Append(ctx, B5TxEvent{TransactionID: tx.TransactionID, TransactionSeq: 1,
		EventUUID: key.EventUUID, EventType: "tx_statement", EventSchemaID: b5.EventSchemaID, EventSchemaVersion: b5.EventSchemaVersion,
		EventDigest: bytesOf(32, 7), CanonicalEvent: []byte(`{"schema":"v4"}`)}))
	event, err := opened.B5TxEvents().Get(ctx, tx.TransactionID, 1)
	require.NoError(t, err)
	require.Equal(t, b5.EventSchemaVersion, event.EventSchemaVersion)
	err = opened.B5TxEvents().Append(ctx, B5TxEvent{TransactionID: tx.TransactionID, TransactionSeq: 1, EventUUID: bytesOf(16, 8),
		EventType: "tx_statement", EventSchemaID: b5.EventSchemaID, EventSchemaVersion: b5.EventSchemaVersion, EventDigest: bytesOf(32, 8), CanonicalEvent: []byte("x")})
	require.Error(t, err)

	expired, err := opened.B5Transactions().ListExpired(ctx, now.Add(2*time.Minute), 10)
	require.NoError(t, err)
	require.Len(t, expired, 1)
	require.NoError(t, opened.B5DMLGrants().DeleteIfRevision(ctx, grant.GrantID, grant.Revision))
}

func TestB5S1bSQLiteCombinedAndMetadataDownReapply(t *testing.T) {
	for _, tc := range []struct {
		name     string
		separate bool
		version  int
	}{
		{name: "combined", version: 10},
		{name: "metadata", separate: true, version: 9},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "b5.db"))
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, db.Close()) })
			require.NoError(t, MigrateMetadata(ctx, db, DialectSQLite, tc.separate))
			for _, table := range []string{"b5_sessions", "b5_transactions", "b5_dml_grants", "b5_result_receipts", "b5_tx_events"} {
				var count int
				require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&count))
				require.Equal(t, 1, count)
			}
			require.NoError(t, MigrateMetadata(ctx, db, DialectSQLite, tc.separate), "up migration reentry")
			require.NoError(t, RollbackMetadataMigration(ctx, db, DialectSQLite, tc.separate, tc.version))
			require.NoError(t, RollbackMetadataMigration(ctx, db, DialectSQLite, tc.separate, tc.version), "down migration reentry")
			require.NoError(t, MigrateMetadata(ctx, db, DialectSQLite, tc.separate))
		})
	}
}

func TestB5S1bMigrationFailureIsAtomic(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "atomic.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	_, err = db.ExecContext(ctx, sqliteSchemaMigrationsDDL)
	require.NoError(t, err)
	err = applyMigration(ctx, db, DialectSQLite, 99, `CREATE TABLE b5_atomic_probe(id INTEGER); INSERT INTO table_that_does_not_exist VALUES(1);`)
	require.Error(t, err)
	var count int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='b5_atomic_probe'`).Scan(&count))
	require.Zero(t, count)
	err = db.QueryRowContext(ctx, `SELECT version FROM schema_migrations WHERE version=99`).Scan(&count)
	require.True(t, errors.Is(err, sql.ErrNoRows))
}

func bytesOf(length int, value byte) []byte {
	result := make([]byte, length)
	for index := range result {
		result[index] = value
	}
	return result
}

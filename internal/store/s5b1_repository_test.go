package store

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestAuditLogRepositoryFindByEventUUIDs(t *testing.T) {
	ctx := context.Background()
	opened := openTestStore(t)
	repository := opened.AuditLogs()

	firstUUID, secondUUID := "event-find-first", "event-find-second"
	first, err := repository.Insert(ctx, fullAuditLogForChainTest("find-first", &firstUUID))
	require.NoError(t, err)
	second, err := repository.Insert(ctx, fullAuditLogForChainTest("find-second", &secondUUID))
	require.NoError(t, err)
	_, err = repository.Insert(ctx, fullAuditLogForChainTest("find-without-uuid", nil))
	require.NoError(t, err)

	empty, err := repository.FindByEventUUIDs(ctx, nil)
	require.NoError(t, err)
	require.Empty(t, empty)

	found, err := repository.FindByEventUUIDs(ctx, []string{secondUUID, "missing", firstUUID})
	require.NoError(t, err)
	require.Equal(t, []model.AuditLog{first, second}, found)
}

func TestSQLiteApprovalAuditChainModes(t *testing.T) {
	ctx := context.Background()

	t.Run("active keyless chains and remains decidable", func(t *testing.T) {
		opened := openTestStore(t)
		manifest := fixedChainManifest{mode: "keyless", version: 0}
		activateEmptyChainForS5b1(t, ctx, opened.metaDB, DialectSQLite, "management", manifest)

		created, recorded, err := opened.Approvals().CreatePendingWithAudit(
			ctx,
			model.Approval{ID: "sqlite-active-keyless", Status: "pending"},
			model.AuditLog{Decision: "approve", SQLRaw: pointer("SELECT 1")},
		)
		require.NoError(t, err)
		require.Equal(t, recorded.ID, requireInt64Pointer(t, created.AuditID))
		rows := loadStoredAuditChainRows(t, ctx, opened.metaDB)
		require.Len(t, rows, 1)
		require.Equal(t, int64(1), rows[0].Sequence)
		assertStoredChainHash(t, recorded, rows[0], "management", requireStringPointer(t, loadChainState(t, ctx, opened.metaDB, "management").ChainInstanceID), nil)

		decided, err := opened.Approvals().DecidePending(
			ctx, created.ID, "approved", "sqlite-dba", nil, time.Now().UTC(),
		)
		require.NoError(t, err)
		require.Equal(t, "approved", decided.Status)
	})

	t.Run("active hmac chains and missing key fails closed", func(t *testing.T) {
		opened := openTestStore(t)
		key := []byte("0123456789abcdef0123456789abcdef")
		manifest := fixedChainManifest{mode: "hmac", version: 1, keys: map[int][]byte{1: key}}
		activateEmptyChainForS5b1(t, ctx, opened.metaDB, DialectSQLite, "management", manifest)

		repository := opened.Approvals()
		repository.auditKeys = manifest
		created, recorded, err := repository.CreatePendingWithAudit(
			ctx,
			model.Approval{ID: "sqlite-active-hmac", Status: "pending"},
			model.AuditLog{Decision: "approve"},
		)
		require.NoError(t, err)
		require.Equal(t, recorded.ID, requireInt64Pointer(t, created.AuditID))
		rows := loadStoredAuditChainRows(t, ctx, opened.metaDB)
		require.Len(t, rows, 1)
		require.Equal(t, 1, rows[0].KeyVersion)
		assertStoredChainHash(t, recorded, rows[0], "management", requireStringPointer(t, loadChainState(t, ctx, opened.metaDB, "management").ChainInstanceID), key)

		before := countAuditLogs(t, ctx, opened.metaDB)
		_, _, err = opened.Approvals().CreatePendingWithAudit(
			ctx,
			model.Approval{ID: "sqlite-active-hmac-no-key", Status: "pending"},
			model.AuditLog{Decision: "approve"},
		)
		require.ErrorContains(t, err, "key provider is unavailable")
		require.Equal(t, before, countAuditLogs(t, ctx, opened.metaDB))
		_, err = opened.Approvals().Get(ctx, "sqlite-active-hmac-no-key")
		require.ErrorIs(t, err, ErrNotFound)
	})

	t.Run("building inserts unchained and backfill covers it", func(t *testing.T) {
		opened := openTestStore(t)
		lease := beginBackfillTestBuild(t, ctx, opened.metaDB, DialectSQLite, "keyless", "approval-builder")
		created, recorded, err := opened.Approvals().CreatePendingWithAudit(
			ctx,
			model.Approval{ID: "sqlite-building", Status: "pending"},
			model.AuditLog{Decision: "approve"},
		)
		require.NoError(t, err)
		require.Equal(t, recorded.ID, requireInt64Pointer(t, created.AuditID))
		assertNullChainColumns(t, ctx, opened.metaDB, []int64{recorded.ID})

		service := NewBackfillService(opened.metaDB, DialectSQLite, "management", nil, BackfillConfig{})
		result, err := service.Run(ctx, lease.Owner, lease.Epoch)
		require.NoError(t, err)
		require.Equal(t, int64(1), result.Linked)
		rows := loadStoredAuditChainRows(t, ctx, opened.metaDB)
		require.Len(t, rows, 1)
		require.Equal(t, recorded.ID, rows[0].ID)
	})

	t.Run("disabled inserts ordinarily", func(t *testing.T) {
		opened := openTestStore(t)
		created, recorded, err := opened.Approvals().CreatePendingWithAudit(
			ctx,
			model.Approval{ID: "sqlite-disabled", Status: "pending"},
			model.AuditLog{Decision: "approve"},
		)
		require.NoError(t, err)
		require.Equal(t, recorded.ID, requireInt64Pointer(t, created.AuditID))
		assertNullChainColumns(t, ctx, opened.metaDB, []int64{recorded.ID})
	})
}

func TestSQLiteSeparatedApprovalSagaUsesActiveChain(t *testing.T) {
	ctx := context.Background()
	opened := openSeparatedSQLiteApprovalStore(t, ctx)
	_, err := opened.auditDB.ExecContext(ctx, "INSERT INTO chain_state (chain_id, status) VALUES ('traffic', 'DISABLED')")
	require.NoError(t, err)
	_, err = opened.auditDB.ExecContext(ctx, "INSERT INTO chain_verification (chain_id) VALUES ('traffic')")
	require.NoError(t, err)
	manifest := fixedChainManifest{mode: "keyless", version: 0}
	activateEmptyChainForS5b1(t, ctx, opened.auditDB, DialectSQLite, "traffic", manifest)

	repository := opened.Approvals()
	created, recorded, err := repository.CreatePendingWithAudit(
		ctx,
		model.Approval{ID: "saga-active", Status: "pending"},
		model.AuditLog{Decision: "approve"},
	)
	require.NoError(t, err)
	require.Equal(t, recorded.ID, requireInt64Pointer(t, created.AuditID))
	rows := loadStoredAuditChainRows(t, ctx, opened.auditDB)
	require.Len(t, rows, 1)
	require.Equal(t, int64(1), rows[0].Sequence)

	_, err = opened.metaDB.ExecContext(ctx, `
CREATE TRIGGER fail_s5b1_saga_metadata BEFORE INSERT ON approvals
WHEN NEW.id = 'saga-active-orphan'
BEGIN
  SELECT RAISE(ABORT, 'forced active saga metadata failure');
END`)
	require.NoError(t, err)
	_, _, err = repository.CreatePendingWithAudit(
		ctx,
		model.Approval{ID: "saga-active-orphan", Status: "pending"},
		model.AuditLog{Decision: "approve"},
	)
	require.ErrorContains(t, err, "forced active saga metadata failure")
	rows = loadStoredAuditChainRows(t, ctx, opened.auditDB)
	require.Len(t, rows, 2)
	require.Equal(t, []int64{1, 2}, []int64{rows[0].Sequence, rows[1].Sequence})
	var approvals int64
	require.NoError(t, opened.metaDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM approvals WHERE id = ?", "saga-active-orphan").Scan(&approvals))
	require.Zero(t, approvals)
}

func TestSQLiteConcurrentAppendBatchUsesLockedHead(t *testing.T) {
	ctx := context.Background()
	opened := openTestStore(t)
	manifest := fixedChainManifest{mode: "keyless", version: 0}
	activateEmptyChainForS5b1(t, ctx, opened.metaDB, DialectSQLite, "management", manifest)
	opened.metaDB.SetMaxOpenConns(4)
	opened.metaDB.SetMaxIdleConns(4)

	const writers = 32
	errorsByWriter := make([]error, writers)
	var waitGroup sync.WaitGroup
	start := make(chan struct{})
	for index := 0; index < writers; index++ {
		index := index
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			<-start
			_, errorsByWriter[index] = opened.AuditLogs().AppendBatch(
				ctx, []model.AuditLog{fullAuditLogForChainTest(fmt.Sprintf("sqlite-concurrent-%02d", index), nil)},
			)
		}()
	}
	close(start)
	waitGroup.Wait()
	for index, err := range errorsByWriter {
		require.NoError(t, err, "writer %d", index)
	}
	rows := loadStoredAuditChainRows(t, ctx, opened.metaDB)
	assertContiguousStoredChain(t, rows, writers)
	verification, err := NewChainVerifier(opened.metaDB, DialectSQLite, "management", manifest).Verify(ctx)
	require.NoError(t, err)
	require.True(t, verification.Valid)
	require.Equal(t, verificationValid, verification.Result)
}

func TestPostgres18ApprovalAuditActiveChainE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("postgres:18 approval active-chain E2E is an integration test")
	}
	ctx := dockerTestContext(t)
	opened := openPostgres18TestStore(t)

	t.Run("keyless", func(t *testing.T) {
		manifest := fixedChainManifest{mode: "keyless", version: 0}
		activateEmptyChainForS5b1(t, ctx, opened.metaDB, DialectPostgres, "management", manifest)
		created, recorded, err := opened.Approvals().CreatePendingWithAudit(
			ctx,
			model.Approval{ID: "pg-active-keyless", Status: "pending"},
			model.AuditLog{Decision: "approve"},
		)
		require.NoError(t, err)
		require.Equal(t, recorded.ID, requireInt64Pointer(t, created.AuditID))
		rows := loadStoredAuditChainRows(t, ctx, opened.metaDB)
		require.Len(t, rows, 1)
		require.Equal(t, int64(1), rows[0].Sequence)
		_, err = opened.Approvals().DecidePending(ctx, created.ID, "approved", "pg-dba", nil, time.Now().UTC())
		require.NoError(t, err)
	})

	resetPostgresActiveChainForS5b1(t, ctx, opened.metaDB)

	t.Run("hmac and missing key", func(t *testing.T) {
		key := []byte("0123456789abcdef0123456789abcdef")
		manifest := fixedChainManifest{mode: "hmac", version: 1, keys: map[int][]byte{1: key}}
		activateEmptyChainForS5b1(t, ctx, opened.metaDB, DialectPostgres, "management", manifest)
		repository := opened.Approvals()
		repository.auditKeys = manifest
		created, recorded, err := repository.CreatePendingWithAudit(
			ctx,
			model.Approval{ID: "pg-active-hmac", Status: "pending"},
			model.AuditLog{Decision: "approve"},
		)
		require.NoError(t, err)
		require.Equal(t, recorded.ID, requireInt64Pointer(t, created.AuditID))
		rows := loadStoredAuditChainRows(t, ctx, opened.metaDB)
		require.Len(t, rows, 1)
		require.Equal(t, 1, rows[0].KeyVersion)

		before := countAuditLogs(t, ctx, opened.metaDB)
		_, _, err = opened.Approvals().CreatePendingWithAudit(
			ctx,
			model.Approval{ID: "pg-active-hmac-no-key", Status: "pending"},
			model.AuditLog{Decision: "approve"},
		)
		require.ErrorContains(t, err, "key provider is unavailable")
		require.Equal(t, before, countAuditLogs(t, ctx, opened.metaDB))
	})
}

func activateEmptyChainForS5b1(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	dialect Dialect,
	chainID string,
	manifest fixedChainManifest,
) {
	t.Helper()
	provisioner := NewChainProvisioner(database, dialect, chainID, manifest, BackfillConfig{})
	require.NoError(t, provisioner.Provision(ctx, fmt.Sprintf("s5b1-%s-builder", chainID)))
	require.Equal(t, "ACTIVE", loadChainState(t, ctx, database, chainID).Status)
}

func resetPostgresActiveChainForS5b1(t *testing.T, ctx context.Context, database *sql.DB) {
	t.Helper()
	_, err := database.ExecContext(ctx, "DROP INDEX ux_audit_logs_chain_seq")
	require.NoError(t, err)
	for _, column := range []string{"chain_seq", "prev_hash", "self_hash", "chain_key_version", "chain_format_version"} {
		_, err = database.ExecContext(ctx, "ALTER TABLE audit_logs ALTER COLUMN "+column+" DROP NOT NULL")
		require.NoError(t, err)
	}
	_, err = database.ExecContext(ctx, "DELETE FROM approvals")
	require.NoError(t, err)
	resetChainBuildE2EState(t, ctx, database)
}

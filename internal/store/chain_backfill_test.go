package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestSQLiteChainBackfill(t *testing.T) {
	ctx := context.Background()

	t.Run("links 257 historical rows and preserves building", func(t *testing.T) {
		opened := openTestStore(t)
		seedHistoricalAuditRows(t, ctx, opened.AuditLogs(), 257, "complete")
		lease := beginBackfillTestBuild(t, ctx, opened.metaDB, DialectSQLite, "keyless", "builder-a")
		service := NewBackfillService(opened.metaDB, DialectSQLite, "management", nil, BackfillConfig{BatchSize: 50})

		result, err := service.Run(ctx, lease.Owner, lease.Epoch)
		require.NoError(t, err)
		require.Equal(t, BackfillResult{Linked: 257, RemainingUnchained: 0, HeadSeq: 257, Complete: true}, result)

		rows := loadStoredAuditChainRows(t, ctx, opened.metaDB)
		assertContiguousStoredChain(t, rows, 257)
		state := loadChainState(t, ctx, opened.metaDB, "management")
		require.Equal(t, "BUILDING", state.Status)
		require.Equal(t, int64(257), state.HeadSeq)
		require.Equal(t, rows[256].ID, requireInt64Pointer(t, state.HeadID))
		require.Equal(t, rows[256].SelfHash, requireStringPointer(t, state.HeadHash))
		require.Equal(t, rows[256].ID, requireInt64Pointer(t, state.LastBuiltID))
		require.Equal(t, int64(257), requireInt64Pointer(t, state.LastBuiltSeq))
		require.Equal(t, rows[256].SelfHash, requireStringPointer(t, state.LastBuiltHash))

		head, reason, err := service.VerifyPrefix(ctx, lease.Owner, lease.Epoch)
		require.NoError(t, err)
		require.Equal(t, int64(257), head)
		require.Empty(t, reason)
	})

	t.Run("resumes after a committed batch", func(t *testing.T) {
		opened := openTestStore(t)
		seedHistoricalAuditRows(t, ctx, opened.AuditLogs(), 137, "resume")
		lease := beginBackfillTestBuild(t, ctx, opened.metaDB, DialectSQLite, "keyless", "builder-a")
		service := NewBackfillService(opened.metaDB, DialectSQLite, "management", nil, BackfillConfig{BatchSize: 50})

		first, err := service.runBatch(ctx, lease.Owner, lease.Epoch)
		require.NoError(t, err)
		require.Equal(t, int64(50), first.Linked)
		require.False(t, first.Complete)

		result, err := service.Run(ctx, lease.Owner, lease.Epoch)
		require.NoError(t, err)
		require.Equal(t, int64(87), result.Linked)
		require.True(t, result.Complete)
		assertContiguousStoredChain(t, loadStoredAuditChainRows(t, ctx, opened.metaDB), 137)
	})

	t.Run("takeover preserves instance and resumes checkpoint", func(t *testing.T) {
		opened := openTestStore(t)
		seedHistoricalAuditRows(t, ctx, opened.AuditLogs(), 121, "takeover")
		firstLease := beginBackfillTestBuild(t, ctx, opened.metaDB, DialectSQLite, "keyless", "builder-a")
		firstService := NewBackfillService(opened.metaDB, DialectSQLite, "management", nil, BackfillConfig{BatchSize: 50})
		first, err := firstService.runBatch(ctx, firstLease.Owner, firstLease.Epoch)
		require.NoError(t, err)
		require.Equal(t, int64(50), first.Linked)

		_, err = opened.metaDB.ExecContext(ctx, `
UPDATE chain_state SET build_lease_until = ? WHERE chain_id = 'management'`, time.Now().UTC().Add(-time.Minute))
		require.NoError(t, err)
		buildRepository := newChainBuildTestRepository(opened.metaDB, DialectSQLite)
		secondLease, err := buildRepository.BeginBuild(ctx, "management", BuildRequest{
			Mode: "hmac", Owner: "builder-b", Lease: time.Minute,
		})
		require.NoError(t, err)
		require.Equal(t, firstLease.InstanceID, secondLease.InstanceID)
		require.Equal(t, firstLease.Epoch+1, secondLease.Epoch)

		secondService := NewBackfillService(opened.metaDB, DialectSQLite, "management", nil, BackfillConfig{BatchSize: 50})
		result, err := secondService.Run(ctx, secondLease.Owner, secondLease.Epoch)
		require.NoError(t, err)
		require.Equal(t, int64(71), result.Linked)
		rows := loadStoredAuditChainRows(t, ctx, opened.metaDB)
		assertContiguousStoredChain(t, rows, 121)
		head, reason, err := secondService.VerifyPrefix(ctx, secondLease.Owner, secondLease.Epoch)
		require.NoError(t, err)
		require.Equal(t, int64(121), head)
		require.Empty(t, reason)
		state := loadChainState(t, ctx, opened.metaDB, "management")
		require.Equal(t, firstLease.InstanceID, requireStringPointer(t, state.ChainInstanceID))
		require.Equal(t, "keyless", requireStringPointer(t, state.Mode))
	})

	t.Run("hmac verifies and missing keys fail closed", func(t *testing.T) {
		key := []byte("0123456789abcdef0123456789abcdef")
		opened := openTestStore(t)
		seedHistoricalAuditRows(t, ctx, opened.AuditLogs(), 73, "hmac")
		lease := beginBackfillTestBuild(t, ctx, opened.metaDB, DialectSQLite, "hmac", "builder-hmac")
		service := NewBackfillService(
			opened.metaDB,
			DialectSQLite,
			"management",
			fakeChainKeyProvider{keys: map[int][]byte{1: key}},
			BackfillConfig{BatchSize: 50},
		)
		result, err := service.Run(ctx, lease.Owner, lease.Epoch)
		require.NoError(t, err)
		require.Equal(t, int64(73), result.Linked)
		head, reason, err := service.VerifyPrefix(ctx, lease.Owner, lease.Epoch)
		require.NoError(t, err)
		require.Equal(t, int64(73), head)
		require.Empty(t, reason)
		wrongKeyService := NewBackfillService(
			opened.metaDB,
			DialectSQLite,
			"management",
			fakeChainKeyProvider{keys: map[int][]byte{1: []byte("abcdef0123456789abcdef0123456789")}},
			BackfillConfig{BatchSize: 50},
		)
		_, err = wrongKeyService.Run(ctx, lease.Owner, lease.Epoch)
		require.ErrorContains(t, err, backfillReasonSelfMismatch)

		for name, provider := range map[string]ChainKeyProvider{
			"provider error": fakeChainKeyProvider{err: errors.New("key service unavailable")},
			"empty key":      fakeChainKeyProvider{keys: map[int][]byte{1: nil}},
			"nil provider":   nil,
		} {
			t.Run(name, func(t *testing.T) {
				failureStore := openTestStore(t)
				seedHistoricalAuditRows(t, ctx, failureStore.AuditLogs(), 3, "missing-key")
				failureLease := beginBackfillTestBuild(t, ctx, failureStore.metaDB, DialectSQLite, "hmac", "builder")
				failureService := NewBackfillService(
					failureStore.metaDB, DialectSQLite, "management", provider, BackfillConfig{BatchSize: 2},
				)
				_, err := failureService.Run(ctx, failureLease.Owner, failureLease.Epoch)
				require.ErrorContains(t, err, backfillReasonKeyUnavailable)
				require.Empty(t, loadStoredAuditChainRows(t, ctx, failureStore.metaDB))
			})
		}
	})

	t.Run("tampered prefix stops resumed writes", func(t *testing.T) {
		opened := openTestStore(t)
		seedHistoricalAuditRows(t, ctx, opened.AuditLogs(), 80, "tamper")
		lease := beginBackfillTestBuild(t, ctx, opened.metaDB, DialectSQLite, "keyless", "builder")
		service := NewBackfillService(opened.metaDB, DialectSQLite, "management", nil, BackfillConfig{BatchSize: 50})
		first, err := service.runBatch(ctx, lease.Owner, lease.Epoch)
		require.NoError(t, err)
		require.Equal(t, int64(50), first.Linked)

		_, err = opened.metaDB.ExecContext(ctx, `
UPDATE audit_logs
SET self_hash = 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
WHERE chain_seq = 1`)
		require.NoError(t, err)
		_, err = service.Run(ctx, lease.Owner, lease.Epoch)
		require.ErrorContains(t, err, backfillReasonSelfMismatch)
		require.Len(t, loadStoredAuditChainRows(t, ctx, opened.metaDB), 50)
		require.Equal(t, int64(30), countUnchainedAuditRows(t, ctx, opened.metaDB))
	})

	t.Run("live appends and historical backfill share one sequence", func(t *testing.T) {
		opened := openTestStore(t)
		seedHistoricalAuditRows(t, ctx, opened.AuditLogs(), 90, "mixed-history")
		lease := beginBackfillTestBuild(t, ctx, opened.metaDB, DialectSQLite, "keyless", "builder")
		live := make([]model.AuditLog, 7)
		for index := range live {
			live[index] = fullAuditLogForChainTest(fmt.Sprintf("mixed-live-%d", index), nil)
		}
		inserted, err := opened.AuditLogs().AppendBatch(ctx, live)
		require.NoError(t, err)
		require.Len(t, inserted, 7)

		service := NewBackfillService(opened.metaDB, DialectSQLite, "management", nil, BackfillConfig{BatchSize: 50})
		result, err := service.Run(ctx, lease.Owner, lease.Epoch)
		require.NoError(t, err)
		require.Equal(t, int64(90), result.Linked)
		rows := loadStoredAuditChainRows(t, ctx, opened.metaDB)
		assertContiguousStoredChain(t, rows, 97)
		head, reason, err := service.VerifyPrefix(ctx, lease.Owner, lease.Epoch)
		require.NoError(t, err)
		require.Equal(t, int64(97), head)
		require.Empty(t, reason)
		require.Equal(t, "BUILDING", loadChainState(t, ctx, opened.metaDB, "management").Status)
	})
}

func TestPostgres18ChainBackfillE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("postgres:18 chain backfill E2E is an integration test")
	}
	ctx := dockerTestContext(t)
	opened := openPostgres18TestStore(t)
	resetChainBuildE2EState(t, ctx, opened.metaDB)
	repository := opened.AuditLogs()
	seedHistoricalAuditRows(t, ctx, repository, 500, "pg-backfill")
	lease := beginBackfillTestBuild(t, ctx, opened.metaDB, DialectPostgres, "keyless", "pg-builder")
	service := NewBackfillService(opened.metaDB, DialectPostgres, "management", nil, BackfillConfig{BatchSize: 100})

	start := make(chan struct{})
	var waitGroup sync.WaitGroup
	var backfillResult BackfillResult
	var backfillErr error
	waitGroup.Add(1)
	go func() {
		defer waitGroup.Done()
		<-start
		backfillResult, backfillErr = service.Run(ctx, lease.Owner, lease.Epoch)
	}()
	const liveWriters = 20
	liveErrors := make([]error, liveWriters)
	for index := 0; index < liveWriters; index++ {
		index := index
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			<-start
			_, liveErrors[index] = repository.Insert(ctx, fullAuditLogForChainTest(fmt.Sprintf("pg-live-%02d", index), nil))
		}()
	}
	close(start)
	waitGroup.Wait()
	require.NoError(t, backfillErr)
	require.True(t, backfillResult.Complete)
	for index, err := range liveErrors {
		require.NoError(t, err, "live writer %d", index)
	}

	require.Equal(t, int64(520), countAuditLogs(t, ctx, opened.metaDB))
	rows := loadStoredAuditChainRows(t, ctx, opened.metaDB)
	assertContiguousStoredChain(t, rows, 520)
	head, reason, err := service.VerifyPrefix(ctx, lease.Owner, lease.Epoch)
	require.NoError(t, err)
	require.Equal(t, int64(520), head)
	require.Empty(t, reason)
	require.Equal(t, "BUILDING", loadChainState(t, ctx, opened.metaDB, "management").Status)
}

func seedHistoricalAuditRows(
	t *testing.T,
	ctx context.Context,
	repository *AuditLogRepository,
	count int,
	label string,
) {
	t.Helper()
	logs := make([]model.AuditLog, count)
	for index := range logs {
		logs[index] = fullAuditLogForChainTest(fmt.Sprintf("%s-%03d", label, index), nil)
	}
	inserted, err := repository.AppendBatch(ctx, logs)
	require.NoError(t, err)
	require.Len(t, inserted, count)
}

func beginBackfillTestBuild(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	dialect Dialect,
	mode, owner string,
) BuildLease {
	t.Helper()
	lease, err := newChainBuildTestRepository(database, dialect).BeginBuild(ctx, "management", BuildRequest{
		Mode: mode, Owner: owner, Lease: time.Minute,
	})
	require.NoError(t, err)
	return lease
}

func assertContiguousStoredChain(t *testing.T, rows []storedAuditChainRow, count int) {
	t.Helper()
	require.Len(t, rows, count)
	for index, row := range rows {
		require.Equal(t, int64(index+1), row.Sequence, "chain row %d", index)
		if index > 0 {
			require.Equal(t, rows[index-1].SelfHash, row.PreviousHash, "chain row %d", index)
		}
	}
}

func countUnchainedAuditRows(t *testing.T, ctx context.Context, database *sql.DB) int64 {
	t.Helper()
	var count int64
	require.NoError(t, database.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_logs WHERE chain_seq IS NULL`).Scan(&count))
	return count
}

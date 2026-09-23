package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

type fixedChainManifest struct {
	mode    string
	version int
	keys    map[int][]byte
}

func (manifest fixedChainManifest) ExpectedMode(context.Context) (string, error) {
	return manifest.mode, nil
}

func (manifest fixedChainManifest) CurrentKeyVersion(context.Context) (int, error) {
	return manifest.version, nil
}

func (manifest fixedChainManifest) ChainKeyForVersion(_ context.Context, version int) ([]byte, error) {
	key, ok := manifest.keys[version]
	if !ok {
		return nil, fmt.Errorf("manifest key version %d is unavailable", version)
	}
	return key, nil
}

func TestSQLiteChainActivation(t *testing.T) {
	ctx := context.Background()
	keyless := fixedChainManifest{mode: "keyless", version: 0}

	t.Run("provision keyless installs contract and protects all rows", func(t *testing.T) {
		opened := openTestStore(t)
		seedHistoricalAuditRows(t, ctx, opened.AuditLogs(), 17, "activation-keyless")
		provisioner := NewChainProvisioner(
			opened.metaDB, DialectSQLite, "management", keyless, BackfillConfig{BatchSize: 5},
		)

		require.NoError(t, provisioner.Provision(ctx, "sqlite-keyless-builder"))
		state, err := provisioner.Status(ctx)
		require.NoError(t, err)
		require.Equal(t, "ACTIVE", state.Status)
		require.Nil(t, state.BuildOwner)
		require.Nil(t, state.BuildLeaseUntil)
		rows := loadStoredAuditChainRows(t, ctx, opened.metaDB)
		assertContiguousStoredChain(t, rows, 17)
		var minimumID int64
		require.NoError(t, opened.metaDB.QueryRowContext(ctx, "SELECT MIN(id) FROM audit_logs").Scan(&minimumID))
		require.Equal(t, minimumID, requireInt64Pointer(t, state.ProtectedSinceID))
		assertSQLiteChainContract(t, ctx, opened.metaDB, true)
		assertActivePlaceholderRollback(t, ctx, opened.AuditLogs())

		_, err = insertAuditLog(ctx, opened.metaDB, DialectSQLite, model.AuditLog{Decision: "allow"})
		require.ErrorContains(t, err, "audit chain contract: chain columns required")
		inserted, err := opened.AuditLogs().Insert(ctx, fullAuditLogForChainTest("active-keyless", nil))
		require.NoError(t, err)
		require.NotZero(t, inserted.ID)
		rows = loadStoredAuditChainRows(t, ctx, opened.metaDB)
		assertContiguousStoredChain(t, rows, 18)
		require.NotEqual(t, activeSelfHashPlaceholder, rows[len(rows)-1].SelfHash)

		// ACTIVE is an idempotent terminal state for the orchestrator.
		require.NoError(t, provisioner.Provision(ctx, "sqlite-keyless-builder"))
	})

	t.Run("incomplete coverage leaves building without contract", func(t *testing.T) {
		opened := openTestStore(t)
		seedHistoricalAuditRows(t, ctx, opened.AuditLogs(), 4, "activation-incomplete")
		lease := beginBackfillTestBuild(t, ctx, opened.metaDB, DialectSQLite, "keyless", "incomplete-builder")
		service := NewBackfillService(opened.metaDB, DialectSQLite, "management", keyless, BackfillConfig{})
		_, err := service.Run(ctx, lease.Owner, lease.Epoch)
		require.NoError(t, err)
		_, err = insertAuditLog(ctx, opened.metaDB, DialectSQLite, model.AuditLog{Decision: "allow"})
		require.NoError(t, err)

		err = service.Activate(ctx, lease.Owner, lease.Epoch, keyless)
		require.ErrorIs(t, err, ErrChainCoverageIncomplete)
		state, statusErr := service.Status(ctx)
		require.NoError(t, statusErr)
		require.Equal(t, "BUILDING", state.Status)
		assertSQLiteChainContract(t, ctx, opened.metaDB, false)
	})

	t.Run("contract failure rolls back DDL and active transition together", func(t *testing.T) {
		opened := openTestStore(t)
		seedHistoricalAuditRows(t, ctx, opened.AuditLogs(), 4, "activation-atomic")
		lease := beginBackfillTestBuild(t, ctx, opened.metaDB, DialectSQLite, "keyless", "atomic-builder")
		service := NewBackfillService(opened.metaDB, DialectSQLite, "management", keyless, BackfillConfig{})
		_, err := service.Run(ctx, lease.Owner, lease.Epoch)
		require.NoError(t, err)
		_, err = opened.metaDB.ExecContext(ctx, `
CREATE INDEX ux_audit_logs_chain_seq ON audit_logs(chain_seq)`)
		require.NoError(t, err)

		err = service.Activate(ctx, lease.Owner, lease.Epoch, keyless)
		require.ErrorContains(t, err, "ux_audit_logs_chain_seq")
		require.Equal(t, "BUILDING", loadChainState(t, ctx, opened.metaDB, "management").Status)
		var triggerCount int
		require.NoError(t, opened.metaDB.QueryRowContext(ctx, `
SELECT COUNT(*) FROM sqlite_master
WHERE type = 'trigger'
  AND name IN ('trg_audit_logs_chain_contract_insert','trg_audit_logs_chain_contract_update')`).Scan(&triggerCount))
		require.Zero(t, triggerCount, "contract triggers must roll back with the failed activation transaction")
		_, err = insertAuditLog(ctx, opened.metaDB, DialectSQLite, model.AuditLog{Decision: "allow"})
		require.NoError(t, err, "legacy NULL insert remains possible while activation did not commit")
	})

	t.Run("trusted mode and current key version reject downgrade", func(t *testing.T) {
		t.Run("keyless state cannot impersonate hmac", func(t *testing.T) {
			opened := openTestStore(t)
			seedHistoricalAuditRows(t, ctx, opened.AuditLogs(), 3, "activation-mode")
			lease := beginBackfillTestBuild(t, ctx, opened.metaDB, DialectSQLite, "keyless", "mode-builder")
			service := NewBackfillService(opened.metaDB, DialectSQLite, "management", keyless, BackfillConfig{})
			_, err := service.Run(ctx, lease.Owner, lease.Epoch)
			require.NoError(t, err)
			hmac := fixedChainManifest{
				mode: "hmac", version: 1,
				keys: map[int][]byte{1: []byte("0123456789abcdef0123456789abcdef")},
			}

			err = service.Activate(ctx, lease.Owner, lease.Epoch, hmac)
			require.ErrorIs(t, err, ErrChainModeDowngrade)
			require.Equal(t, "BUILDING", loadChainState(t, ctx, opened.metaDB, "management").Status)
			assertSQLiteChainContract(t, ctx, opened.metaDB, false)
		})

		t.Run("head row version must equal manifest current version", func(t *testing.T) {
			opened := openTestStore(t)
			key1 := []byte("0123456789abcdef0123456789abcdef")
			key2 := []byte("abcdef0123456789abcdef0123456789")
			buildManifest := fixedChainManifest{
				mode: "hmac", version: 1, keys: map[int][]byte{1: key1, 2: key2},
			}
			seedHistoricalAuditRows(t, ctx, opened.AuditLogs(), 3, "activation-version")
			lease := beginBackfillTestBuild(t, ctx, opened.metaDB, DialectSQLite, "hmac", "version-builder")
			service := NewBackfillService(opened.metaDB, DialectSQLite, "management", buildManifest, BackfillConfig{})
			_, err := service.Run(ctx, lease.Owner, lease.Epoch)
			require.NoError(t, err)
			currentVersionTwo := fixedChainManifest{
				mode: "hmac", version: 2, keys: map[int][]byte{1: key1, 2: key2},
			}

			err = service.Activate(ctx, lease.Owner, lease.Epoch, currentVersionTwo)
			require.ErrorIs(t, err, ErrChainModeDowngrade)
			require.Equal(t, "BUILDING", loadChainState(t, ctx, opened.metaDB, "management").Status)
			assertSQLiteChainContract(t, ctx, opened.metaDB, false)
		})
	})

	t.Run("hmac provision activates and rejects legacy null writes", func(t *testing.T) {
		opened := openTestStore(t)
		key := []byte("0123456789abcdef0123456789abcdef")
		hmac := fixedChainManifest{mode: "hmac", version: 1, keys: map[int][]byte{1: key}}
		seedHistoricalAuditRows(t, ctx, opened.AuditLogs(), 11, "activation-hmac")
		provisioner := NewChainProvisioner(
			opened.metaDB, DialectSQLite, "management", hmac, BackfillConfig{BatchSize: 4},
		)

		require.NoError(t, provisioner.Provision(ctx, "sqlite-hmac-builder"))
		state, err := provisioner.Status(ctx)
		require.NoError(t, err)
		require.Equal(t, "ACTIVE", state.Status)
		_, err = insertAuditLog(ctx, opened.metaDB, DialectSQLite, model.AuditLog{Decision: "allow"})
		require.ErrorContains(t, err, "audit chain contract: chain columns required")

		hmacRepository := &AuditLogRepository{
			repositoryBase: repositoryBase{db: opened.metaDB, dialect: DialectSQLite},
			chainID:        "management",
			keys:           hmac,
		}
		_, err = hmacRepository.Insert(ctx, fullAuditLogForChainTest("active-hmac", nil))
		require.NoError(t, err)
		rows := loadStoredAuditChainRows(t, ctx, opened.metaDB)
		assertContiguousStoredChain(t, rows, 12)
		require.Equal(t, 1, rows[len(rows)-1].KeyVersion)
	})

	t.Run("nil manifest is never implicit keyless", func(t *testing.T) {
		opened := openTestStore(t)
		provisioner := NewChainProvisioner(
			opened.metaDB, DialectSQLite, "management", nil, BackfillConfig{},
		)
		require.ErrorContains(t, provisioner.Provision(ctx, "builder"), "manifest is required")
		require.Equal(t, "DISABLED", loadChainState(t, ctx, opened.metaDB, "management").Status)
	})
}

func TestPostgres18ChainActivationE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("postgres:18 chain activation E2E is an integration test")
	}
	ctx := dockerTestContext(t)
	opened := openPostgres18TestStore(t)
	resetChainBuildE2EState(t, ctx, opened.metaDB)
	manifest := fixedChainManifest{mode: "keyless", version: 0}
	repository := opened.AuditLogs()
	seedHistoricalAuditRows(t, ctx, repository, 300, "pg-activation")
	provisioner := NewChainProvisioner(
		opened.metaDB, DialectPostgres, "management", manifest, BackfillConfig{BatchSize: 40},
	)

	start := make(chan struct{})
	var waitGroup sync.WaitGroup
	provisionErrors := make(chan error, 1)
	waitGroup.Add(1)
	go func() {
		defer waitGroup.Done()
		<-start
		provisionErrors <- provisioner.Provision(ctx, "pg-activation-builder")
	}()
	const concurrentAppends = 10
	appendErrors := make(chan error, concurrentAppends)
	for index := 0; index < concurrentAppends; index++ {
		index := index
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			<-start
			_, err := repository.Insert(ctx, fullAuditLogForChainTest(fmt.Sprintf("pg-activation-live-%d", index), nil))
			appendErrors <- err
		}()
	}
	close(start)
	waitGroup.Wait()
	require.NoError(t, <-provisionErrors)
	close(appendErrors)
	for err := range appendErrors {
		require.NoError(t, err)
	}

	state, err := provisioner.Status(ctx)
	require.NoError(t, err)
	require.Equal(t, "ACTIVE", state.Status)
	rows := loadStoredAuditChainRows(t, ctx, opened.metaDB)
	assertContiguousStoredChain(t, rows, 300+concurrentAppends)
	assertActivePlaceholderRollback(t, ctx, repository)

	var nonNullable int
	require.NoError(t, opened.metaDB.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM information_schema.columns
WHERE table_schema = current_schema() AND table_name = 'audit_logs'
  AND column_name IN ('chain_seq','prev_hash','self_hash','chain_key_version','chain_format_version')
  AND is_nullable = 'NO'`).Scan(&nonNullable))
	require.Equal(t, 5, nonNullable)
	var indexDefinition string
	require.NoError(t, opened.metaDB.QueryRowContext(ctx, `
SELECT indexdef FROM pg_indexes
WHERE schemaname = current_schema() AND tablename = 'audit_logs'
  AND indexname = 'ux_audit_logs_chain_seq'`).Scan(&indexDefinition))
	require.Contains(t, strings.ToUpper(indexDefinition), "UNIQUE INDEX")

	_, err = insertAuditLog(ctx, opened.metaDB, DialectPostgres, model.AuditLog{Decision: "allow"})
	require.Error(t, err)
	inserted, err := repository.Insert(ctx, fullAuditLogForChainTest("pg-active-normal", nil))
	require.NoError(t, err)
	require.NotZero(t, inserted.ID)
	rows = loadStoredAuditChainRows(t, ctx, opened.metaDB)
	assertContiguousStoredChain(t, rows, 301+concurrentAppends)
}

func assertSQLiteChainContract(t *testing.T, ctx context.Context, database *sql.DB, expected bool) {
	t.Helper()
	var triggerCount int
	err := database.QueryRowContext(ctx, `
SELECT COUNT(*) FROM sqlite_master
WHERE type = 'trigger'
  AND name IN ('trg_audit_logs_chain_contract_insert','trg_audit_logs_chain_contract_update')`).Scan(&triggerCount)
	require.NoError(t, err)
	var indexCount int
	err = database.QueryRowContext(ctx, `
SELECT COUNT(*) FROM sqlite_master
WHERE type = 'index' AND name = 'ux_audit_logs_chain_seq'
  AND sql LIKE 'CREATE UNIQUE INDEX%'`).Scan(&indexCount)
	require.NoError(t, err)
	if expected {
		require.Equal(t, 2, triggerCount)
		require.Equal(t, 1, indexCount)
		return
	}
	require.Zero(t, triggerCount)
	require.Zero(t, indexCount)
}

func assertActivePlaceholderRollback(t *testing.T, ctx context.Context, repository *AuditLogRepository) {
	t.Helper()
	beforeRows := countAuditLogs(t, ctx, repository.db)
	beforeState := loadChainState(t, ctx, repository.db, repository.chainID)
	transaction, err := repository.beginChainTransaction(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = transaction.Rollback(ctx) })
	locked, err := repository.lockChainState(ctx, transaction)
	require.NoError(t, err)
	require.Equal(t, "ACTIVE", locked.Status)
	previousHash := requireStringPointer(t, locked.HeadHash)
	_, err = insertActiveAuditLog(
		ctx,
		transaction,
		repository.dialect,
		fullAuditLogForChainTest("placeholder-rollback", nil),
		locked.HeadSeq+1,
		previousHash,
		0,
	)
	require.NoError(t, err)
	require.NoError(t, transaction.Rollback(ctx))

	require.Equal(t, beforeRows, countAuditLogs(t, ctx, repository.db))
	afterState := loadChainState(t, ctx, repository.db, repository.chainID)
	require.Equal(t, beforeState.HeadSeq, afterState.HeadSeq)
	require.Equal(t, beforeState.HeadHash, afterState.HeadHash)
	var placeholders int
	require.NoError(t, repository.db.QueryRowContext(
		ctx, repository.bind("SELECT COUNT(*) FROM audit_logs WHERE self_hash = ?"), activeSelfHashPlaceholder,
	).Scan(&placeholders))
	require.Zero(t, placeholders)
}

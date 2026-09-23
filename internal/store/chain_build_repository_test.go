package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestSQLiteChainBuildLifecycle(t *testing.T) {
	ctx := context.Background()

	t.Run("disabled to building", func(t *testing.T) {
		opened := openTestStore(t)
		repository := newChainBuildTestRepository(opened.metaDB, DialectSQLite)
		before := time.Now().UTC()
		lease, err := repository.BeginBuild(ctx, "management", BuildRequest{
			Mode: "keyless", Owner: "builder-a", Lease: time.Minute,
		})
		after := time.Now().UTC()
		require.NoError(t, err)
		require.Equal(t, "management", lease.ChainID)
		require.Equal(t, "builder-a", lease.Owner)
		require.Equal(t, 1, lease.Epoch)
		_, err = uuid.Parse(lease.InstanceID)
		require.NoError(t, err)
		require.WithinDuration(t, before.Add(time.Minute), lease.LeaseUntil, after.Sub(before)+time.Second)

		state := readChainBuildTestState(t, ctx, opened.metaDB, DialectSQLite, "management")
		require.Equal(t, "BUILDING", state.Status)
		require.Equal(t, lease.InstanceID, requireStringPointer(t, state.ChainInstanceID))
		require.Equal(t, "keyless", requireStringPointer(t, state.Mode))
		require.Equal(t, "builder-a", requireStringPointer(t, state.BuildOwner))
		require.Equal(t, lease.Epoch, state.BuildEpoch)
		require.WithinDuration(t, lease.LeaseUntil, requireTimePointer(t, state.BuildLeaseUntil), time.Microsecond)
		require.False(t, requireTimePointer(t, state.GenesisAt).Before(before.Add(-time.Second)))
		require.Zero(t, state.HeadSeq)
		require.Nil(t, state.HeadID)
		require.Nil(t, state.HeadHash)
		require.Nil(t, state.LastBuiltID)
		require.Nil(t, state.LastBuiltSeq)
		require.Nil(t, state.LastBuiltHash)
	})

	t.Run("concurrent begin has one winner and same owner is idempotent", func(t *testing.T) {
		opened := openTestStore(t)
		repository := newChainBuildTestRepository(opened.metaDB, DialectSQLite)
		const contenders = 8
		leases := make([]BuildLease, contenders)
		errs := make([]error, contenders)
		start := make(chan struct{})
		var waitGroup sync.WaitGroup
		for index := 0; index < contenders; index++ {
			index := index
			waitGroup.Add(1)
			go func() {
				defer waitGroup.Done()
				<-start
				leases[index], errs[index] = repository.BeginBuild(ctx, "management", BuildRequest{
					Mode: "keyless", Owner: fmt.Sprintf("builder-%d", index), Lease: time.Minute,
				})
			}()
		}
		close(start)
		waitGroup.Wait()

		winner := -1
		for index, err := range errs {
			if err == nil {
				require.Equal(t, -1, winner, "only one contender may acquire the initial lease")
				winner = index
				continue
			}
			require.ErrorIs(t, err, ErrBuildLeaseActive, "contender %d", index)
		}
		require.NotEqual(t, -1, winner)

		_, err := repository.BeginBuild(ctx, "management", BuildRequest{
			Mode: "keyless", Owner: "other-builder", Lease: time.Minute,
		})
		require.ErrorIs(t, err, ErrBuildLeaseActive)

		idempotent, err := repository.BeginBuild(ctx, "management", BuildRequest{
			Mode: "hmac", Owner: leases[winner].Owner, Lease: 10 * time.Minute,
		})
		require.NoError(t, err)
		require.Equal(t, leases[winner], idempotent)
	})

	t.Run("expired lease takeover preserves instance mode genesis head and checkpoint", func(t *testing.T) {
		opened := openTestStore(t)
		repository := newChainBuildTestRepository(opened.metaDB, DialectSQLite)
		first, err := repository.BeginBuild(ctx, "management", BuildRequest{
			Mode: "hmac", Owner: "builder-a", Lease: time.Minute,
		})
		require.NoError(t, err)
		const hash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		_, err = opened.metaDB.ExecContext(ctx, `
UPDATE chain_state
SET build_lease_until = ?, head_seq = 7, head_id = 70, head_hash = ?,
    last_built_id = 50, last_built_seq = 5, last_built_hash = ?
WHERE chain_id = 'management'`, time.Now().Add(-time.Minute), hash, hash)
		require.NoError(t, err)
		before := readChainBuildTestState(t, ctx, opened.metaDB, DialectSQLite, "management")

		taken, err := repository.BeginBuild(ctx, "management", BuildRequest{
			Mode: "keyless", Owner: "builder-b", Lease: 90 * time.Second,
		})
		require.NoError(t, err)
		require.Equal(t, first.InstanceID, taken.InstanceID)
		require.Equal(t, first.Epoch+1, taken.Epoch)
		require.Equal(t, "builder-b", taken.Owner)

		after := readChainBuildTestState(t, ctx, opened.metaDB, DialectSQLite, "management")
		require.Equal(t, before.ChainInstanceID, after.ChainInstanceID)
		require.Equal(t, before.Mode, after.Mode)
		require.Equal(t, before.GenesisAt, after.GenesisAt)
		require.Equal(t, before.HeadSeq, after.HeadSeq)
		require.Equal(t, before.HeadID, after.HeadID)
		require.Equal(t, before.HeadHash, after.HeadHash)
		require.Equal(t, before.LastBuiltID, after.LastBuiltID)
		require.Equal(t, before.LastBuiltSeq, after.LastBuiltSeq)
		require.Equal(t, before.LastBuiltHash, after.LastBuiltHash)
		require.Equal(t, "builder-b", requireStringPointer(t, after.BuildOwner))
	})

	t.Run("renew lease enforces fencing", func(t *testing.T) {
		opened := openTestStore(t)
		repository := newChainBuildTestRepository(opened.metaDB, DialectSQLite)
		lease, err := repository.BeginBuild(ctx, "management", BuildRequest{Mode: "keyless", Owner: "builder", Lease: time.Second})
		require.NoError(t, err)

		renewed, err := repository.RenewLease(ctx, "management", lease.Owner, lease.Epoch, time.Minute)
		require.NoError(t, err)
		require.Equal(t, lease.InstanceID, renewed.InstanceID)
		require.Equal(t, lease.Epoch, renewed.Epoch)
		require.True(t, renewed.LeaseUntil.After(lease.LeaseUntil))
		state := readChainBuildTestState(t, ctx, opened.metaDB, DialectSQLite, "management")
		require.WithinDuration(t, renewed.LeaseUntil, requireTimePointer(t, state.BuildLeaseUntil), time.Microsecond)

		_, err = repository.RenewLease(ctx, "management", lease.Owner, lease.Epoch+1, time.Minute)
		require.ErrorIs(t, err, ErrStaleBuildFencing)
	})

	t.Run("active and failed reject begin", func(t *testing.T) {
		for _, testCase := range []struct {
			status  string
			wantErr error
		}{
			{status: "ACTIVE", wantErr: ErrChainAlreadyActive},
			{status: "FAILED", wantErr: ErrBuildNotInProgress},
		} {
			t.Run(testCase.status, func(t *testing.T) {
				opened := openTestStore(t)
				repository := newChainBuildTestRepository(opened.metaDB, DialectSQLite)
				_, err := opened.metaDB.ExecContext(ctx, `UPDATE chain_state SET status = ? WHERE chain_id = 'management'`, testCase.status)
				require.NoError(t, err)
				_, err = repository.BeginBuild(ctx, "management", BuildRequest{Mode: "keyless", Owner: "builder"})
				require.ErrorIs(t, err, testCase.wantErr)
				if testCase.status == "FAILED" {
					require.ErrorContains(t, err, "reset")
				}
			})
		}
	})

	t.Run("reset clears state and derived audit columns then creates new instance", func(t *testing.T) {
		opened := openTestStore(t)
		repository := newChainBuildTestRepository(opened.metaDB, DialectSQLite)
		lease, err := repository.BeginBuild(ctx, "management", BuildRequest{Mode: "keyless", Owner: "builder"})
		require.NoError(t, err)
		const hash = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		_, err = opened.metaDB.ExecContext(ctx, `
INSERT INTO audit_logs (decision, chain_seq, prev_hash, self_hash, chain_key_version, chain_format_version)
VALUES ('allow', 1, ?, ?, 0, 1)`, hash, hash)
		require.NoError(t, err)
		_, err = opened.metaDB.ExecContext(ctx, `
UPDATE chain_state
SET head_seq = 1, head_id = (SELECT max(id) FROM audit_logs), head_hash = ?,
    last_built_id = (SELECT max(id) FROM audit_logs), last_built_seq = 1, last_built_hash = ?
WHERE chain_id = 'management'`, hash, hash)
		require.NoError(t, err)

		err = repository.ResetBuild(ctx, "management", lease.Owner, lease.Epoch+1)
		require.ErrorIs(t, err, ErrStaleBuildFencing)
		beforeReset := readChainBuildTestState(t, ctx, opened.metaDB, DialectSQLite, "management")
		require.Equal(t, "BUILDING", beforeReset.Status)
		require.NotNil(t, beforeReset.LastBuiltID)

		require.NoError(t, repository.ResetBuild(ctx, "management", lease.Owner, lease.Epoch))
		reset := readChainBuildTestState(t, ctx, opened.metaDB, DialectSQLite, "management")
		require.Equal(t, "DISABLED", reset.Status)
		require.Equal(t, lease.Epoch, reset.BuildEpoch)
		require.Zero(t, reset.HeadSeq)
		require.Nil(t, reset.ChainInstanceID)
		require.Nil(t, reset.Mode)
		require.Nil(t, reset.GenesisAt)
		require.Nil(t, reset.HeadID)
		require.Nil(t, reset.HeadHash)
		require.Nil(t, reset.BuildOwner)
		require.Nil(t, reset.BuildLeaseUntil)
		require.Nil(t, reset.LastBuiltID)
		require.Nil(t, reset.LastBuiltSeq)
		require.Nil(t, reset.LastBuiltHash)
		assertAllAuditChainColumnsNull(t, ctx, opened.metaDB)

		restarted, err := repository.BeginBuild(ctx, "management", BuildRequest{Mode: "hmac", Owner: "new-builder"})
		require.NoError(t, err)
		require.NotEqual(t, lease.InstanceID, restarted.InstanceID)
		require.Equal(t, lease.Epoch+1, restarted.Epoch)
		require.Zero(t, readChainBuildTestState(t, ctx, opened.metaDB, DialectSQLite, "management").HeadSeq)
	})

	t.Run("request validation and fencing helper", func(t *testing.T) {
		opened := openTestStore(t)
		repository := newChainBuildTestRepository(opened.metaDB, DialectSQLite)
		_, err := repository.BeginBuild(ctx, "management", BuildRequest{Mode: "invalid", Owner: "builder"})
		require.ErrorIs(t, err, ErrInvalidBuildMode)
		_, err = repository.BeginBuild(ctx, "management", BuildRequest{Mode: "keyless", Owner: " "})
		require.Error(t, err)
		_, err = repository.BeginBuild(ctx, "management", BuildRequest{Mode: "keyless", Owner: "builder", Lease: -time.Second})
		require.Error(t, err)

		owner := "builder"
		state := ChainState{Status: "BUILDING", BuildOwner: &owner, BuildEpoch: 3}
		require.NoError(t, VerifyFencing(state, owner, 3))
		require.ErrorIs(t, VerifyFencing(state, owner, 2), ErrStaleBuildFencing)
		state.Status = "DISABLED"
		require.ErrorIs(t, VerifyFencing(state, owner, 3), ErrStaleBuildFencing)
	})
}

func TestPostgres18ChainBuildLifecycleE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("postgres:18 chain-build lifecycle E2E is an integration test")
	}
	ctx := dockerTestContext(t)
	opened := openPostgres18TestStore(t)
	repository := newChainBuildTestRepository(opened.metaDB, DialectPostgres)

	t.Run("disabled to building persists SQL state", func(t *testing.T) {
		resetChainBuildE2EState(t, ctx, opened.metaDB)
		lease, err := repository.BeginBuild(ctx, "management", BuildRequest{Mode: "keyless", Owner: "pg-builder", Lease: time.Minute})
		require.NoError(t, err)
		state := readChainBuildTestState(t, ctx, opened.metaDB, DialectPostgres, "management")
		require.Equal(t, "BUILDING", state.Status)
		require.Equal(t, lease.InstanceID, requireStringPointer(t, state.ChainInstanceID))
		require.Equal(t, lease.Epoch, state.BuildEpoch)
		require.Equal(t, lease.Owner, requireStringPointer(t, state.BuildOwner))
	})

	t.Run("concurrent begin has a single winner", func(t *testing.T) {
		resetChainBuildE2EState(t, ctx, opened.metaDB)
		const contenders = 12
		errs := make([]error, contenders)
		var waitGroup sync.WaitGroup
		start := make(chan struct{})
		for index := 0; index < contenders; index++ {
			index := index
			waitGroup.Add(1)
			go func() {
				defer waitGroup.Done()
				<-start
				_, errs[index] = repository.BeginBuild(ctx, "management", BuildRequest{
					Mode: "keyless", Owner: fmt.Sprintf("pg-builder-%d", index), Lease: time.Minute,
				})
			}()
		}
		close(start)
		waitGroup.Wait()
		successes := 0
		for _, err := range errs {
			if err == nil {
				successes++
			} else {
				require.ErrorIs(t, err, ErrBuildLeaseActive)
			}
		}
		require.Equal(t, 1, successes)
		state := readChainBuildTestState(t, ctx, opened.metaDB, DialectPostgres, "management")
		require.Equal(t, 1, state.BuildEpoch)
	})

	t.Run("expired takeover preserves SQL state", func(t *testing.T) {
		resetChainBuildE2EState(t, ctx, opened.metaDB)
		first, err := repository.BeginBuild(ctx, "management", BuildRequest{Mode: "hmac", Owner: "pg-old", Lease: time.Minute})
		require.NoError(t, err)
		const hash = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
		genesis := readChainBuildTestState(t, ctx, opened.metaDB, DialectPostgres, "management").GenesisAt
		_, err = opened.metaDB.ExecContext(ctx, `
UPDATE chain_state
SET build_lease_until = now() - interval '1 minute',
    last_built_id = 9, last_built_seq = 8, last_built_hash = $1
WHERE chain_id = 'management'`, hash)
		require.NoError(t, err)

		taken, err := repository.BeginBuild(ctx, "management", BuildRequest{Mode: "keyless", Owner: "pg-new", Lease: time.Minute})
		require.NoError(t, err)
		require.Equal(t, first.InstanceID, taken.InstanceID)
		require.Equal(t, first.Epoch+1, taken.Epoch)
		state := readChainBuildTestState(t, ctx, opened.metaDB, DialectPostgres, "management")
		require.Equal(t, first.InstanceID, requireStringPointer(t, state.ChainInstanceID))
		require.Equal(t, "hmac", requireStringPointer(t, state.Mode))
		require.Equal(t, genesis, state.GenesisAt)
		require.Equal(t, int64(9), requireInt64Pointer(t, state.LastBuiltID))
		require.Equal(t, int64(8), requireInt64Pointer(t, state.LastBuiltSeq))
		require.Equal(t, hash, requireStringPointer(t, state.LastBuiltHash))
		require.Equal(t, "pg-new", requireStringPointer(t, state.BuildOwner))
	})
}

func newChainBuildTestRepository(database *sql.DB, dialect Dialect) *ChainBuildRepository {
	return &ChainBuildRepository{repositoryBase: repositoryBase{db: database, dialect: dialect}}
}

func readChainBuildTestState(t *testing.T, ctx context.Context, database *sql.DB, dialect Dialect, chainID string) ChainState {
	t.Helper()
	repository := &ChainStateRepository{repositoryBase: repositoryBase{db: database, dialect: dialect}}
	state, err := repository.Get(ctx, chainID)
	require.NoError(t, err)
	return state
}

func resetChainBuildE2EState(t *testing.T, ctx context.Context, database *sql.DB) {
	t.Helper()
	_, err := database.ExecContext(ctx, "DELETE FROM audit_logs")
	require.NoError(t, err)
	_, err = database.ExecContext(ctx, `
UPDATE chain_state
SET chain_instance_id = NULL, status = 'DISABLED', mode = NULL,
    head_seq = 0, head_id = NULL, head_hash = NULL, genesis_at = NULL,
    protected_since_id = NULL, build_owner = NULL, build_lease_until = NULL,
    build_epoch = 0, last_built_id = NULL, last_built_seq = NULL,
    last_built_hash = NULL, updated_at = now()
WHERE chain_id = 'management'`)
	require.NoError(t, err)
}

func assertAllAuditChainColumnsNull(t *testing.T, ctx context.Context, database *sql.DB) {
	t.Helper()
	var nonNullRows int
	err := database.QueryRowContext(ctx, `
SELECT count(*) FROM audit_logs
WHERE chain_seq IS NOT NULL OR prev_hash IS NOT NULL OR self_hash IS NOT NULL
   OR chain_key_version IS NOT NULL OR chain_format_version IS NOT NULL`).Scan(&nonNullRows)
	require.NoError(t, err)
	require.Zero(t, nonNullRows)
}

func requireStringPointer(t *testing.T, value *string) string {
	t.Helper()
	require.NotNil(t, value)
	return *value
}

func requireInt64Pointer(t *testing.T, value *int64) int64 {
	t.Helper()
	require.NotNil(t, value)
	return *value
}

func requireTimePointer(t *testing.T, value *time.Time) time.Time {
	t.Helper()
	require.NotNil(t, value)
	return *value
}

func TestChainBuildSentinelErrorsAreDistinct(t *testing.T) {
	errorsList := []error{
		ErrBuildLeaseActive,
		ErrStaleBuildFencing,
		ErrChainAlreadyActive,
		ErrBuildNotInProgress,
		ErrInvalidBuildMode,
	}
	for left := range errorsList {
		for right := range errorsList {
			if left == right {
				continue
			}
			require.False(t, errors.Is(errorsList[left], errorsList[right]))
		}
	}
}

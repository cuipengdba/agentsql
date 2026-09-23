package store

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const adversarialChainID = "management"

func TestAdversarialAuditChainSQLite(t *testing.T) {
	opened := openTestStore(t)
	runAuditChainAdversarialTests(t, opened, DialectSQLite)
}

func TestAdversarialPostgres18AuditChainE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("postgres:18 audit-chain adversarial E2E is an integration test")
	}
	opened := openPostgres18TestStore(t)
	runAuditChainAdversarialTests(t, opened, DialectPostgres)
}

func runAuditChainAdversarialTests(t *testing.T, opened *Store, dialect Dialect) {
	t.Helper()
	ctx := context.Background()
	key := []byte("0123456789abcdef0123456789abcdef")
	hmac := fixedChainManifest{mode: "hmac", version: 1, keys: map[int][]byte{1: key}}
	keyless := fixedChainManifest{mode: "keyless", version: 0}

	t.Run("mode_downgrade_preserves_active", func(t *testing.T) {
		for _, attack := range []struct {
			name     string
			manifest ChainManifest
		}{
			{name: "claims_keyless", manifest: keyless},
			{name: "hmac_current_version_zero", manifest: fixedChainManifest{mode: "hmac", version: 0}},
		} {
			t.Run(attack.name, func(t *testing.T) {
				resetAdversarialChainStore(t, ctx, opened.metaDB, dialect)
				provisionAdversarialChain(t, ctx, opened, dialect, hmac, 8, "mode-downgrade")
				valid, err := NewChainVerifier(opened.metaDB, dialect, adversarialChainID, hmac).Verify(ctx)
				require.NoError(t, err)
				require.Equal(t, verificationValid, valid.Result)

				outcome, verifyErr := NewChainVerifier(
					opened.metaDB, dialect, adversarialChainID, attack.manifest,
				).Verify(ctx)
				require.NoError(t, verifyErr)
				require.False(t, outcome.Valid)
				require.Equal(t, activationModeDowngrade, outcome.BreakReason)
				require.Equal(t, 2, ExitCodeForVerification(outcome, verifyErr))
				require.Equal(t, "ACTIVE", loadChainState(t, ctx, opened.metaDB, adversarialChainID).Status)
			})
		}
	})

	t.Run("epoch_fencing_rejects_old_owner_progress", func(t *testing.T) {
		resetAdversarialChainStore(t, ctx, opened.metaDB, dialect)
		seedHistoricalAuditRows(t, ctx, opened.AuditLogs(), 9, "fencing")
		builds := newChainBuildTestRepository(opened.metaDB, dialect)
		leaseA, err := builds.BeginBuild(ctx, adversarialChainID, BuildRequest{
			Mode: "keyless", Owner: "builder-a", Lease: time.Minute,
		})
		require.NoError(t, err)
		serviceA := NewBackfillService(
			opened.metaDB, dialect, adversarialChainID, keyless, BackfillConfig{BatchSize: 2, Lease: time.Minute},
		)
		first, err := serviceA.runBatch(ctx, leaseA.Owner, leaseA.Epoch)
		require.NoError(t, err)
		require.Equal(t, int64(2), first.Linked, "builder A must have advanced the build before takeover")

		execAdversarialSQL(t, ctx, opened.metaDB, dialect, `
UPDATE chain_state SET build_lease_until = ? WHERE chain_id = ?`, time.Now().UTC().Add(-time.Minute), adversarialChainID)
		leaseB, err := builds.BeginBuild(ctx, adversarialChainID, BuildRequest{
			Mode: "keyless", Owner: "builder-b", Lease: time.Minute,
		})
		require.NoError(t, err)
		require.Equal(t, leaseA.Epoch+1, leaseB.Epoch)
		require.Equal(t, leaseA.InstanceID, leaseB.InstanceID)

		_, err = builds.RenewLease(ctx, adversarialChainID, leaseA.Owner, leaseA.Epoch, time.Minute)
		require.ErrorIs(t, err, ErrStaleBuildFencing)
		_, err = serviceA.runBatch(ctx, leaseA.Owner, leaseA.Epoch)
		require.ErrorIs(t, err, ErrStaleBuildFencing, "old owner cannot append another backfill batch")
		_, _, err = serviceA.VerifyPrefix(ctx, leaseA.Owner, leaseA.Epoch)
		require.ErrorIs(t, err, ErrStaleBuildFencing, "old owner cannot advance through backfill verification")
		err = serviceA.Activate(ctx, leaseA.Owner, leaseA.Epoch, keyless)
		require.ErrorIs(t, err, ErrStaleBuildFencing)
		require.Equal(t, leaseB.Owner, requireStringPointer(t, loadChainState(
			t, ctx, opened.metaDB, adversarialChainID,
		).BuildOwner))
	})

	t.Run("active_contract_rejects_old_binary_null_chain_columns", func(t *testing.T) {
		resetAdversarialChainStore(t, ctx, opened.metaDB, dialect)
		provisionAdversarialChain(t, ctx, opened, dialect, keyless, 5, "contract")

		_, err := opened.metaDB.ExecContext(ctx, repositoryBase{dialect: dialect}.bind(`
INSERT INTO audit_logs (
  decision, chain_seq, prev_hash, self_hash, chain_key_version, chain_format_version
) VALUES (?, NULL, NULL, NULL, NULL, NULL)`), "allow")
		require.Error(t, err)
		if dialect == DialectSQLite {
			require.Contains(t, err.Error(), "audit chain contract")
		} else {
			message := strings.ToLower(err.Error())
			require.Contains(t, message, "null value")
			require.Contains(t, message, "chain_seq")
		}

		inserted, err := opened.AuditLogs().Insert(ctx, fullAuditLogForChainTest("contract-new-writer", nil))
		require.NoError(t, err)
		require.NotZero(t, inserted.ID)
		outcome, verifyErr := NewChainVerifier(opened.metaDB, dialect, adversarialChainID, keyless).Verify(ctx)
		require.NoError(t, verifyErr)
		require.Equal(t, verificationValid, outcome.Result)
	})

	t.Run("tail_truncation_with_state_and_verification_rollback_is_outside_trust_boundary", func(t *testing.T) {
		resetAdversarialChainStore(t, ctx, opened.metaDB, dialect)
		provisionAdversarialChain(t, ctx, opened, dialect, keyless, 10, "tail-rollback")
		verifier := NewChainVerifier(opened.metaDB, dialect, adversarialChainID, keyless)
		latest, err := verifier.VerifyAndPersist(ctx)
		require.NoError(t, err)
		require.Equal(t, verificationValid, latest.Result)
		rows := loadStoredAuditChainRows(t, ctx, opened.metaDB)
		const removed = 3
		observed := rows[len(rows)-removed-1]

		// Design v2 §1 promises exactly: "只能证明从观察到的 head 回溯到 genesis 的完整性，
		// 不能证明观察到的 head 就是外部世界曾见过的最新 head。" Therefore an attacker
		// who deletes the tail and rewinds both database-owned observations remains undetectable.
		// Detection requires an external WORM/SIEM record or an independently retained verifier anchor.
		execAdversarialSQL(t, ctx, opened.metaDB, dialect, "DELETE FROM audit_logs WHERE chain_seq > ?", observed.Sequence)
		rewindAdversarialHead(t, ctx, opened.metaDB, dialect, observed)
		rewindAdversarialVerification(t, ctx, opened.metaDB, dialect, latest.ObservedInstanceID, observed)

		outcome, verifyErr := verifier.Verify(ctx)
		require.NoError(t, verifyErr)
		require.True(t, outcome.Valid)
		require.Equal(t, verificationValid, outcome.Result)
		require.Equal(t, observed.Sequence, outcome.HeadSeq)
		require.Equal(t, 0, ExitCodeForVerification(outcome, verifyErr))
	})

	t.Run("consistent_database_snapshot_rollback_is_outside_trust_boundary", func(t *testing.T) {
		resetAdversarialChainStore(t, ctx, opened.metaDB, dialect)
		provisionAdversarialChain(t, ctx, opened, dialect, keyless, 6, "snapshot-early")
		verifier := NewChainVerifier(opened.metaDB, dialect, adversarialChainID, keyless)
		earlyOutcome, err := verifier.VerifyAndPersist(ctx)
		require.NoError(t, err)
		earlyState := loadChainState(t, ctx, opened.metaDB, adversarialChainID)
		earlyVerification := readVerificationRow(t, ctx, opened.metaDB, dialect, adversarialChainID)

		seedHistoricalAuditRows(t, ctx, opened.AuditLogs(), 4, "snapshot-later")
		later, err := verifier.VerifyAndPersist(ctx)
		require.NoError(t, err)
		require.Equal(t, earlyOutcome.HeadSeq+4, later.HeadSeq)

		// Design v2 §1 promises exactly: "只能证明从观察到的 head 回溯到 genesis 的完整性，
		// 不能证明观察到的 head 就是外部世界曾见过的最新 head。" Restoring audit_logs,
		// chain_state, and chain_verification to one earlier consistent database snapshot is thus
		// outside this chain's trust boundary. Detection requires an external WORM/SIEM history or
		// an independently retained verifier anchor newer than the restored snapshot.
		execAdversarialSQL(t, ctx, opened.metaDB, dialect, "DELETE FROM audit_logs WHERE id > ?", requireInt64Pointer(t, earlyState.HeadID))
		restoreAdversarialState(t, ctx, opened.metaDB, dialect, earlyState)
		restoreAdversarialVerification(t, ctx, opened.metaDB, dialect, earlyVerification)

		outcome, verifyErr := verifier.Verify(ctx)
		require.NoError(t, verifyErr)
		require.True(t, outcome.Valid)
		require.Equal(t, verificationValid, outcome.Result)
		require.Equal(t, earlyOutcome.HeadSeq, outcome.HeadSeq)
		require.Equal(t, 0, ExitCodeForVerification(outcome, verifyErr))
	})

	t.Run("incomplete_tail_truncation_is_detected_as_head_mismatch", func(t *testing.T) {
		resetAdversarialChainStore(t, ctx, opened.metaDB, dialect)
		provisionAdversarialChain(t, ctx, opened, dialect, keyless, 9, "tail-incomplete")
		execAdversarialSQL(t, ctx, opened.metaDB, dialect, "DELETE FROM audit_logs WHERE chain_seq > ?", int64(7))

		outcome, verifyErr := NewChainVerifier(opened.metaDB, dialect, adversarialChainID, keyless).Verify(ctx)
		require.NoError(t, verifyErr)
		require.False(t, outcome.Valid)
		require.Equal(t, backfillReasonHeadMismatch, outcome.BreakReason)
		require.Equal(t, 2, ExitCodeForVerification(outcome, verifyErr))
	})

	t.Run("hmac_missing_key_is_key_unavailable", func(t *testing.T) {
		resetAdversarialChainStore(t, ctx, opened.metaDB, dialect)
		provisionAdversarialChain(t, ctx, opened, dialect, hmac, 7, "missing-key")
		missing := fixedChainManifest{mode: "hmac", version: 1}

		outcome, verifyErr := NewChainVerifier(opened.metaDB, dialect, adversarialChainID, missing).Verify(ctx)
		require.Error(t, verifyErr)
		require.False(t, outcome.Valid)
		require.Equal(t, backfillReasonKeyUnavailable, outcome.BreakReason)
		require.Equal(t, 3, ExitCodeForVerification(outcome, verifyErr))
		require.Equal(t, "ACTIVE", outcome.Status)
		// AuditWriterReady/readyz separation is asserted at the monitoring boundary by
		// TestChainMonitorHMACMissingKeyOnlyLowersWriterReadiness and
		// TestReadinessRemainsReadyWhenHMACChainKeyMissing.
	})
}

func provisionAdversarialChain(
	t *testing.T,
	ctx context.Context,
	opened *Store,
	dialect Dialect,
	manifest ChainManifest,
	rows int,
	label string,
) {
	t.Helper()
	seedHistoricalAuditRows(t, ctx, opened.AuditLogs(), rows, label)
	require.NoError(t, NewChainProvisioner(
		opened.metaDB, dialect, adversarialChainID, manifest, BackfillConfig{BatchSize: 3},
	).Provision(ctx, "adversarial-"+label))
	require.Equal(t, "ACTIVE", loadChainState(t, ctx, opened.metaDB, adversarialChainID).Status)
}

func resetAdversarialChainStore(t *testing.T, ctx context.Context, database *sql.DB, dialect Dialect) {
	t.Helper()
	if dialect == DialectSQLite {
		for _, statement := range []string{
			"DROP TRIGGER IF EXISTS trg_audit_logs_chain_contract_insert",
			"DROP TRIGGER IF EXISTS trg_audit_logs_chain_contract_update",
			"DROP INDEX IF EXISTS ux_audit_logs_chain_seq",
		} {
			execAdversarialSQL(t, ctx, database, dialect, statement)
		}
	} else {
		execAdversarialSQL(t, ctx, database, dialect, "DROP INDEX IF EXISTS ux_audit_logs_chain_seq")
		for _, column := range []string{
			"chain_seq", "prev_hash", "self_hash", "chain_key_version", "chain_format_version",
		} {
			execAdversarialSQL(t, ctx, database, dialect, "ALTER TABLE audit_logs ALTER COLUMN "+column+" DROP NOT NULL")
		}
	}
	execAdversarialSQL(t, ctx, database, dialect, "DELETE FROM audit_logs")
	execAdversarialSQL(t, ctx, database, dialect, `
UPDATE chain_state
SET chain_instance_id = NULL, status = 'DISABLED', mode = NULL,
    head_seq = 0, head_id = NULL, head_hash = NULL, genesis_at = NULL,
    protected_since_id = NULL, build_owner = NULL, build_lease_until = NULL,
    build_epoch = 0, last_built_id = NULL, last_built_seq = NULL,
    last_built_hash = NULL, updated_at = ?
WHERE chain_id = ?`, time.Now().UTC(), adversarialChainID)
	execAdversarialSQL(t, ctx, database, dialect, `
UPDATE chain_verification
SET observed_instance_id = NULL, observed_head_hash = NULL, result = NULL,
    last_verified_head_seq = NULL, last_verified_at = NULL,
    break_seq = NULL, break_id = NULL, break_reason = NULL
WHERE chain_id = ?`, adversarialChainID)
}

func rewindAdversarialHead(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	dialect Dialect,
	head storedAuditChainRow,
) {
	t.Helper()
	execAdversarialSQL(t, ctx, database, dialect, `
UPDATE chain_state
SET head_seq = ?, head_id = ?, head_hash = ?,
    last_built_id = ?, last_built_seq = ?, last_built_hash = ?, updated_at = ?
WHERE chain_id = ?`,
		head.Sequence, head.ID, head.SelfHash,
		head.ID, head.Sequence, head.SelfHash, time.Now().UTC(), adversarialChainID,
	)
}

func rewindAdversarialVerification(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	dialect Dialect,
	instanceID string,
	head storedAuditChainRow,
) {
	t.Helper()
	execAdversarialSQL(t, ctx, database, dialect, `
UPDATE chain_verification
SET observed_instance_id = ?, observed_head_hash = ?, result = ?,
    last_verified_head_seq = ?, last_verified_at = ?,
    break_seq = NULL, break_id = NULL, break_reason = NULL
WHERE chain_id = ?`,
		instanceID, head.SelfHash, verificationValid, head.Sequence, time.Now().UTC(), adversarialChainID,
	)
}

func restoreAdversarialState(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	dialect Dialect,
	state ChainState,
) {
	t.Helper()
	execAdversarialSQL(t, ctx, database, dialect, `
UPDATE chain_state
SET chain_instance_id = ?, status = ?, mode = ?, head_seq = ?, head_id = ?, head_hash = ?,
    genesis_at = ?, protected_since_id = ?, build_owner = ?, build_lease_until = ?,
    build_epoch = ?, last_built_id = ?, last_built_seq = ?, last_built_hash = ?, updated_at = ?
WHERE chain_id = ?`,
		state.ChainInstanceID, state.Status, state.Mode, state.HeadSeq, state.HeadID, state.HeadHash,
		state.GenesisAt, state.ProtectedSinceID, state.BuildOwner, state.BuildLeaseUntil,
		state.BuildEpoch, state.LastBuiltID, state.LastBuiltSeq, state.LastBuiltHash, state.UpdatedAt,
		state.ChainID,
	)
}

func restoreAdversarialVerification(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	dialect Dialect,
	verification ChainVerification,
) {
	t.Helper()
	execAdversarialSQL(t, ctx, database, dialect, `
UPDATE chain_verification
SET observed_instance_id = ?, observed_head_hash = ?, result = ?,
    last_verified_head_seq = ?, last_verified_at = ?, break_seq = ?, break_id = ?, break_reason = ?
WHERE chain_id = ?`,
		verification.ObservedInstanceID, verification.ObservedHeadHash, verification.Result,
		verification.LastVerifiedHeadSeq, verification.LastVerifiedAt,
		verification.BreakSeq, verification.BreakID, verification.BreakReason, verification.ChainID,
	)
}

func execAdversarialSQL(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	dialect Dialect,
	query string,
	arguments ...any,
) {
	t.Helper()
	_, err := database.ExecContext(ctx, repositoryBase{dialect: dialect}.bind(query), arguments...)
	require.NoError(t, err)
}

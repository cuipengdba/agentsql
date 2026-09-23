package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSQLiteChainVerifierValidAndPersistence(t *testing.T) {
	ctx := context.Background()
	opened := openTestStore(t)
	manifest := fixedChainManifest{mode: "keyless", version: 0}
	seedHistoricalAuditRows(t, ctx, opened.AuditLogs(), 7, "verify-valid")
	provisioner := NewChainProvisioner(
		opened.metaDB, DialectSQLite, "management", manifest, BackfillConfig{BatchSize: 3},
	)
	require.NoError(t, provisioner.Provision(ctx, "verify-valid-builder"))
	state := loadChainState(t, ctx, opened.metaDB, "management")
	verifier := NewChainVerifier(opened.metaDB, DialectSQLite, "management", manifest)

	t.Run("read-only verification", func(t *testing.T) {
		outcome, err := verifier.Verify(ctx)
		require.NoError(t, err)
		require.True(t, outcome.Valid)
		require.Equal(t, verificationValid, outcome.Result)
		require.Equal(t, 0, ExitCodeForVerification(outcome, err))
		require.Equal(t, "management", outcome.ChainID)
		require.Equal(t, "ACTIVE", outcome.Status)
		require.Equal(t, requireStringPointer(t, state.ChainInstanceID), outcome.ObservedInstanceID)
		require.Equal(t, int64(7), outcome.HeadSeq)
		require.Equal(t, int64(7), outcome.TotalRows)
		require.Zero(t, outcome.Unchained)
		require.Zero(t, outcome.BreakSeq)
		require.Zero(t, outcome.BreakID)
		require.Empty(t, outcome.BreakReason)
	})

	t.Run("all nine columns persist", func(t *testing.T) {
		outcome, err := verifier.VerifyAndPersist(ctx)
		require.NoError(t, err)
		verification := readVerificationRow(t, ctx, opened.metaDB, DialectSQLite, "management")
		require.Equal(t, outcome.ChainID, verification.ChainID)
		require.Equal(t, outcome.ObservedInstanceID, requireStringPointer(t, verification.ObservedInstanceID))
		require.Equal(t, requireStringPointer(t, state.HeadHash), requireStringPointer(t, verification.ObservedHeadHash))
		require.Equal(t, outcome.Result, requireStringPointer(t, verification.Result))
		require.Equal(t, outcome.HeadSeq, requireInt64Pointer(t, verification.LastVerifiedHeadSeq))
		require.WithinDuration(t, outcome.VerifiedAt, requireTimePointer(t, verification.LastVerifiedAt), time.Microsecond)
		require.Nil(t, verification.BreakSeq)
		require.Nil(t, verification.BreakID)
		require.Nil(t, verification.BreakReason)
	})
}

func TestSQLiteChainVerifierTamperReasons(t *testing.T) {
	ctx := context.Background()
	manifest := fixedChainManifest{mode: "keyless", version: 0}
	tests := []struct {
		name          string
		rows          int
		mutate        func(t *testing.T, database *sql.DB, rows []storedAuditChainRow)
		reason        string
		breakRowIndex int
		breakSequence int64
	}{
		{
			name: "payload",
			rows: 3,
			mutate: func(t *testing.T, database *sql.DB, rows []storedAuditChainRow) {
				execVerificationTamper(t, database, `UPDATE audit_logs SET details_json = ? WHERE id = ?`, `{"tampered":true}`, rows[1].ID)
			},
			reason: backfillReasonSelfMismatch, breakRowIndex: 1, breakSequence: 2,
		},
		{
			name: "self hash",
			rows: 3,
			mutate: func(t *testing.T, database *sql.DB, rows []storedAuditChainRow) {
				execVerificationTamper(t, database, `UPDATE audit_logs SET self_hash = ? WHERE id = ?`, verificationTestHash("a"), rows[1].ID)
			},
			reason: backfillReasonSelfMismatch, breakRowIndex: 1, breakSequence: 2,
		},
		{
			name: "previous hash",
			rows: 3,
			mutate: func(t *testing.T, database *sql.DB, rows []storedAuditChainRow) {
				execVerificationTamper(t, database, `UPDATE audit_logs SET prev_hash = ? WHERE id = ?`, verificationTestHash("b"), rows[1].ID)
			},
			reason: backfillReasonPrevMismatch, breakRowIndex: 1, breakSequence: 2,
		},
		{
			name: "bad format",
			rows: 3,
			mutate: func(t *testing.T, database *sql.DB, rows []storedAuditChainRow) {
				execVerificationTamper(t, database, `UPDATE audit_logs SET self_hash = 'not-a-hash' WHERE id = ?`, rows[1].ID)
			},
			reason: backfillReasonBadFormat, breakRowIndex: 1, breakSequence: 2,
		},
		{
			name: "half chained",
			rows: 3,
			mutate: func(t *testing.T, database *sql.DB, rows []storedAuditChainRow) {
				execVerificationTamper(t, database, `DROP TRIGGER trg_audit_logs_chain_contract_update`)
				execVerificationTamper(t, database, `UPDATE audit_logs SET self_hash = NULL WHERE id = ?`, rows[1].ID)
			},
			reason: backfillReasonHalfChained, breakRowIndex: 1, breakSequence: 2,
		},
		{
			name: "duplicate sequence",
			rows: 3,
			mutate: func(t *testing.T, database *sql.DB, rows []storedAuditChainRow) {
				execVerificationTamper(t, database, `DROP INDEX ux_audit_logs_chain_seq`)
				execVerificationTamper(t, database, `UPDATE audit_logs SET chain_seq = 1 WHERE id = ?`, rows[1].ID)
			},
			reason: backfillReasonDuplicate, breakRowIndex: 1, breakSequence: 1,
		},
		{
			name: "sequence gap",
			rows: 2,
			mutate: func(t *testing.T, database *sql.DB, rows []storedAuditChainRow) {
				execVerificationTamper(t, database, `DROP INDEX ux_audit_logs_chain_seq`)
				execVerificationTamper(t, database, `UPDATE audit_logs SET chain_seq = 3 WHERE id = ?`, rows[1].ID)
			},
			reason: backfillReasonSequenceGap, breakRowIndex: 1, breakSequence: 2,
		},
		{
			name: "head mismatch",
			rows: 3,
			mutate: func(t *testing.T, database *sql.DB, rows []storedAuditChainRow) {
				execVerificationTamper(t, database, `UPDATE chain_state SET head_hash = ? WHERE chain_id = 'management'`, verificationTestHash("c"))
			},
			reason: backfillReasonHeadMismatch, breakRowIndex: 2, breakSequence: 3,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			opened := openTestStore(t)
			seedHistoricalAuditRows(t, ctx, opened.AuditLogs(), test.rows, "verify-tamper-"+test.name)
			provisioner := NewChainProvisioner(
				opened.metaDB, DialectSQLite, "management", manifest, BackfillConfig{},
			)
			require.NoError(t, provisioner.Provision(ctx, "tamper-builder"))
			rows := loadStoredAuditChainRows(t, ctx, opened.metaDB)
			test.mutate(t, opened.metaDB, rows)

			verifier := NewChainVerifier(opened.metaDB, DialectSQLite, "management", manifest)
			outcome, err := verifier.VerifyAndPersist(ctx)
			require.NoError(t, err)
			require.False(t, outcome.Valid)
			require.Equal(t, test.reason, outcome.Result)
			require.Equal(t, test.reason, outcome.BreakReason)
			require.Equal(t, test.breakSequence, outcome.BreakSeq)
			require.Equal(t, rows[test.breakRowIndex].ID, outcome.BreakID)
			require.Equal(t, 2, ExitCodeForVerification(outcome, err))
			assertPersistedVerificationBreak(t, ctx, opened.metaDB, DialectSQLite, outcome)
		})
	}
}

func TestSQLiteChainVerifierHMACKeyUnavailable(t *testing.T) {
	ctx := context.Background()
	good := fixedChainManifest{
		mode: "hmac", version: 1,
		keys: map[int][]byte{1: []byte("0123456789abcdef0123456789abcdef")},
	}
	tests := []struct {
		name     string
		manifest ChainManifest
	}{
		{name: "provider error", manifest: fixedChainManifest{mode: "hmac", version: 1}},
		{name: "empty key", manifest: fixedChainManifest{mode: "hmac", version: 1, keys: map[int][]byte{1: {}}}},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			opened := openTestStore(t)
			seedHistoricalAuditRows(t, ctx, opened.AuditLogs(), 2, "verify-key-"+test.name)
			provisioner := NewChainProvisioner(opened.metaDB, DialectSQLite, "management", good, BackfillConfig{})
			require.NoError(t, provisioner.Provision(ctx, "hmac-builder"))
			verifier := NewChainVerifier(opened.metaDB, DialectSQLite, "management", test.manifest)

			outcome, err := verifier.VerifyAndPersist(ctx)
			require.Error(t, err)
			require.Equal(t, backfillReasonKeyUnavailable, outcome.Result)
			require.Equal(t, 3, ExitCodeForVerification(outcome, err))
			assertPersistedVerificationBreak(t, ctx, opened.metaDB, DialectSQLite, outcome)
		})
	}
}

func TestSQLiteChainVerifierUnprotectedStates(t *testing.T) {
	ctx := context.Background()
	manifest := fixedChainManifest{mode: "keyless", version: 0}

	t.Run("building partial coverage", func(t *testing.T) {
		opened := openTestStore(t)
		seedHistoricalAuditRows(t, ctx, opened.AuditLogs(), 4, "verify-building")
		lease := beginBackfillTestBuild(t, ctx, opened.metaDB, DialectSQLite, "keyless", "partial-builder")
		service := NewBackfillService(
			opened.metaDB, DialectSQLite, "management", manifest, BackfillConfig{BatchSize: 1},
		)
		batch, err := service.runBatchAttempt(ctx, lease.Owner, lease.Epoch)
		require.NoError(t, err)
		require.Equal(t, int64(1), batch.Linked)

		outcome, err := NewChainVerifier(opened.metaDB, DialectSQLite, "management", manifest).Verify(ctx)
		require.NoError(t, err)
		require.Equal(t, "BUILDING", outcome.Status)
		require.Equal(t, verificationCoverageIncomplete, outcome.Result)
		require.Equal(t, int64(3), outcome.Unchained)
		require.Equal(t, 4, ExitCodeForVerification(outcome, err))
	})

	t.Run("disabled", func(t *testing.T) {
		opened := openTestStore(t)
		seedHistoricalAuditRows(t, ctx, opened.AuditLogs(), 3, "verify-disabled")
		outcome, err := NewChainVerifier(opened.metaDB, DialectSQLite, "management", manifest).Verify(ctx)
		require.NoError(t, err)
		require.Equal(t, "DISABLED", outcome.Status)
		require.Equal(t, verificationDisabled, outcome.Result)
		require.Equal(t, outcome.TotalRows, outcome.Unchained)
		require.Equal(t, 4, ExitCodeForVerification(outcome, err))
	})

	t.Run("failed", func(t *testing.T) {
		opened := openTestStore(t)
		execVerificationTamper(t, opened.metaDB, `UPDATE chain_state SET status = 'FAILED' WHERE chain_id = 'management'`)
		outcome, err := NewChainVerifier(opened.metaDB, DialectSQLite, "management", manifest).Verify(ctx)
		require.NoError(t, err)
		require.Equal(t, verificationFailed, outcome.Result)
		require.Equal(t, 4, ExitCodeForVerification(outcome, err))
	})
}

func TestSQLiteChainVerifierModeAuthority(t *testing.T) {
	ctx := context.Background()

	t.Run("state mode differs from trusted manifest", func(t *testing.T) {
		opened := openTestStore(t)
		keyless := fixedChainManifest{mode: "keyless", version: 0}
		seedHistoricalAuditRows(t, ctx, opened.AuditLogs(), 2, "verify-mode-state")
		require.NoError(t, NewChainProvisioner(
			opened.metaDB, DialectSQLite, "management", keyless, BackfillConfig{},
		).Provision(ctx, "keyless-builder"))
		hmac := fixedChainManifest{
			mode: "hmac", version: 1,
			keys: map[int][]byte{1: []byte("0123456789abcdef0123456789abcdef")},
		}
		outcome, err := NewChainVerifier(opened.metaDB, DialectSQLite, "management", hmac).Verify(ctx)
		require.NoError(t, err)
		require.Equal(t, activationModeDowngrade, outcome.Result)
		require.Equal(t, 2, ExitCodeForVerification(outcome, err))
	})

	t.Run("row version differs from current trusted version", func(t *testing.T) {
		opened := openTestStore(t)
		hmac := fixedChainManifest{
			mode: "hmac", version: 1,
			keys: map[int][]byte{1: []byte("0123456789abcdef0123456789abcdef")},
		}
		seedHistoricalAuditRows(t, ctx, opened.AuditLogs(), 2, "verify-mode-version")
		require.NoError(t, NewChainProvisioner(
			opened.metaDB, DialectSQLite, "management", hmac, BackfillConfig{},
		).Provision(ctx, "hmac-builder"))
		rows := loadStoredAuditChainRows(t, ctx, opened.metaDB)
		execVerificationTamper(t, opened.metaDB, `UPDATE audit_logs SET chain_key_version = 2 WHERE id = ?`, rows[1].ID)

		outcome, err := NewChainVerifier(opened.metaDB, DialectSQLite, "management", hmac).Verify(ctx)
		require.NoError(t, err)
		require.Equal(t, activationModeDowngrade, outcome.Result)
		require.Equal(t, int64(2), outcome.BreakSeq)
		require.Equal(t, rows[1].ID, outcome.BreakID)
		require.Equal(t, 2, ExitCodeForVerification(outcome, err))
	})
}

func TestSQLiteChainVerifierDoesNotOverwriteNewerObservation(t *testing.T) {
	ctx := context.Background()
	opened := openTestStore(t)
	manifest := fixedChainManifest{mode: "keyless", version: 0}
	seedHistoricalAuditRows(t, ctx, opened.AuditLogs(), 2, "verify-newer")
	require.NoError(t, NewChainProvisioner(
		opened.metaDB, DialectSQLite, "management", manifest, BackfillConfig{},
	).Provision(ctx, "newer-builder"))
	verifier := NewChainVerifier(opened.metaDB, DialectSQLite, "management", manifest)
	_, err := verifier.VerifyAndPersist(ctx)
	require.NoError(t, err)

	future := time.Now().UTC().Add(time.Hour)
	execVerificationTamper(t, opened.metaDB, `
UPDATE chain_verification
SET result = 'newer-result', last_verified_at = ?, break_seq = 99,
    break_id = 100, break_reason = 'newer-reason'
WHERE chain_id = 'management'`, future)
	outcome, err := verifier.VerifyAndPersist(ctx)
	require.NoError(t, err)
	require.True(t, outcome.VerifiedAt.Before(future))

	verification := readVerificationRow(t, ctx, opened.metaDB, DialectSQLite, "management")
	require.Equal(t, "newer-result", requireStringPointer(t, verification.Result))
	require.WithinDuration(t, future, requireTimePointer(t, verification.LastVerifiedAt), time.Microsecond)
	require.Equal(t, int64(99), requireInt64Pointer(t, verification.BreakSeq))
	require.Equal(t, int64(100), requireInt64Pointer(t, verification.BreakID))
	require.Equal(t, "newer-reason", requireStringPointer(t, verification.BreakReason))
}

func TestSQLiteChainVerifierOperationalFailures(t *testing.T) {
	ctx := context.Background()

	t.Run("manifest is mandatory and failure is persisted", func(t *testing.T) {
		opened := openTestStore(t)
		verifier := NewChainVerifier(opened.metaDB, DialectSQLite, "management", nil)
		outcome, err := verifier.VerifyAndPersist(ctx)
		require.ErrorContains(t, err, "trusted chain manifest is required")
		require.Equal(t, verificationRuntimeError, outcome.Result)
		require.Equal(t, 5, ExitCodeForVerification(outcome, err))
		verification := readVerificationRow(t, ctx, opened.metaDB, DialectSQLite, "management")
		require.Equal(t, verificationRuntimeError, requireStringPointer(t, verification.Result))
		require.NotNil(t, verification.LastVerifiedAt)
	})

	t.Run("missing chain state is an operational error", func(t *testing.T) {
		opened := openTestStore(t)
		execVerificationTamper(t, opened.metaDB, `DELETE FROM chain_state WHERE chain_id = 'management'`)
		manifest := fixedChainManifest{mode: "keyless", version: 0}
		verifier := NewChainVerifier(opened.metaDB, DialectSQLite, "management", manifest)
		outcome, err := verifier.VerifyAndPersist(ctx)
		require.ErrorIs(t, err, ErrNotFound)
		require.Equal(t, verificationRuntimeError, outcome.Result)
		require.Equal(t, 5, ExitCodeForVerification(outcome, err))
		verification := readVerificationRow(t, ctx, opened.metaDB, DialectSQLite, "management")
		require.Equal(t, verificationRuntimeError, requireStringPointer(t, verification.Result))
	})
}

func TestExitCodeForVerification(t *testing.T) {
	runtimeErr := errors.New("database unavailable")
	tests := []struct {
		name    string
		outcome ChainVerificationOutcome
		err     error
		want    int
	}{
		{name: "valid", outcome: ChainVerificationOutcome{Valid: true}, want: 0},
		{name: "half chained", outcome: verificationReasonOutcome(backfillReasonHalfChained), want: 2},
		{name: "sequence gap", outcome: verificationReasonOutcome(backfillReasonSequenceGap), want: 2},
		{name: "sequence duplicate", outcome: verificationReasonOutcome(backfillReasonDuplicate), want: 2},
		{name: "previous mismatch", outcome: verificationReasonOutcome(backfillReasonPrevMismatch), want: 2},
		{name: "self mismatch", outcome: verificationReasonOutcome(backfillReasonSelfMismatch), want: 2},
		{name: "head mismatch", outcome: verificationReasonOutcome(backfillReasonHeadMismatch), want: 2},
		{name: "bad format", outcome: verificationReasonOutcome(backfillReasonBadFormat), want: 2},
		{name: "mode downgrade", outcome: verificationReasonOutcome(activationModeDowngrade), want: 2},
		{name: "key unavailable", outcome: verificationReasonOutcome(backfillReasonKeyUnavailable), err: runtimeErr, want: 3},
		{name: "disabled", outcome: ChainVerificationOutcome{Status: "DISABLED", Result: verificationDisabled}, want: 4},
		{name: "coverage", outcome: ChainVerificationOutcome{Status: "BUILDING", Result: verificationCoverageIncomplete}, want: 4},
		{name: "failed", outcome: ChainVerificationOutcome{Status: "FAILED", Result: verificationFailed}, want: 4},
		{name: "runtime", outcome: ChainVerificationOutcome{Result: verificationRuntimeError}, err: runtimeErr, want: 5},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.want, ExitCodeForVerification(test.outcome, test.err))
		})
	}
}

func TestPostgres18ChainVerifierE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("postgres:18 chain verifier E2E is an integration test")
	}
	ctx := dockerTestContext(t)
	opened := openPostgres18TestStore(t)
	manifest := fixedChainManifest{mode: "keyless", version: 0}
	seedHistoricalAuditRows(t, ctx, opened.AuditLogs(), 200, "pg-verify")
	require.NoError(t, NewChainProvisioner(
		opened.metaDB, DialectPostgres, "management", manifest, BackfillConfig{BatchSize: 37},
	).Provision(ctx, "pg-verify-builder"))
	verifier := NewChainVerifier(opened.metaDB, DialectPostgres, "management", manifest)

	t.Run("valid observed head", func(t *testing.T) {
		outcome, err := verifier.VerifyAndPersist(ctx)
		require.NoError(t, err)
		require.True(t, outcome.Valid)
		require.Equal(t, verificationValid, outcome.Result)
		require.Equal(t, 0, ExitCodeForVerification(outcome, err))
		verification := readVerificationRow(t, ctx, opened.metaDB, DialectPostgres, "management")
		require.Equal(t, verificationValid, requireStringPointer(t, verification.Result))
	})

	t.Run("payload tamper is located and persisted", func(t *testing.T) {
		rows := loadStoredAuditChainRows(t, ctx, opened.metaDB)
		tampered := rows[122]
		execVerificationTamper(t, opened.metaDB, `UPDATE audit_logs SET details_json = ? WHERE id = ?`, `{"pg_tampered":true}`, tampered.ID)
		outcome, err := verifier.VerifyAndPersist(ctx)
		require.NoError(t, err)
		require.Equal(t, backfillReasonSelfMismatch, outcome.Result)
		require.Equal(t, tampered.Sequence, outcome.BreakSeq)
		require.Equal(t, tampered.ID, outcome.BreakID)
		require.Equal(t, 2, ExitCodeForVerification(outcome, err))
		assertPersistedVerificationBreak(t, ctx, opened.metaDB, DialectPostgres, outcome)
	})
}

func execVerificationTamper(t *testing.T, database *sql.DB, query string, arguments ...any) {
	t.Helper()
	query = repositoryBase{dialect: dialectForTestDatabase(database)}.bind(query)
	_, err := database.ExecContext(context.Background(), query, arguments...)
	require.NoError(t, err)
}

func readVerificationRow(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	dialect Dialect,
	chainID string,
) ChainVerification {
	t.Helper()
	repository := &ChainStateRepository{repositoryBase: repositoryBase{db: database, dialect: dialect}}
	verification, err := repository.GetVerification(ctx, chainID)
	require.NoError(t, err)
	return verification
}

func assertPersistedVerificationBreak(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	dialect Dialect,
	outcome ChainVerificationOutcome,
) {
	t.Helper()
	verification := readVerificationRow(t, ctx, database, dialect, outcome.ChainID)
	require.Equal(t, outcome.ObservedInstanceID, requireStringPointer(t, verification.ObservedInstanceID))
	require.Equal(t, outcome.Result, requireStringPointer(t, verification.Result))
	require.Equal(t, outcome.HeadSeq, requireInt64Pointer(t, verification.LastVerifiedHeadSeq))
	require.WithinDuration(t, outcome.VerifiedAt, requireTimePointer(t, verification.LastVerifiedAt), time.Microsecond)
	if outcome.BreakSeq == 0 {
		require.Nil(t, verification.BreakSeq)
	} else {
		require.Equal(t, outcome.BreakSeq, requireInt64Pointer(t, verification.BreakSeq))
	}
	if outcome.BreakID == 0 {
		require.Nil(t, verification.BreakID)
	} else {
		require.Equal(t, outcome.BreakID, requireInt64Pointer(t, verification.BreakID))
	}
	require.Equal(t, outcome.BreakReason, requireStringPointer(t, verification.BreakReason))
}

func verificationTestHash(character string) string {
	return strings.Repeat(character, 64)
}

func verificationReasonOutcome(reason string) ChainVerificationOutcome {
	return ChainVerificationOutcome{Result: reason, BreakReason: reason}
}

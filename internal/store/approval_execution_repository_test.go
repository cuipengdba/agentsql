package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestApprovalExecutionSQLiteUpgradeRollbackFromV051(t *testing.T) {
	for _, tc := range []struct {
		name              string
		separate          bool
		previous, current int
		directory         string
	}{
		{"combined", false, 16, 17, "migrations/sqlite"},
		{"metadata", true, 15, 16, "migrations/metadata/sqlite"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			db := openSQLiteMigrationTestDB(t)
			migrateSQLiteThroughVersion(t, ctx, db, tc.directory, tc.previous)
			_, err := db.ExecContext(ctx, `INSERT INTO approvals(id, tenant_id, status) VALUES('legacy', 'tenant_default', 'approved')`)
			require.NoError(t, err)
			require.NoError(t, MigrateMetadata(ctx, db, DialectSQLite, tc.separate))
			var seal sql.NullString
			require.NoError(t, db.QueryRowContext(ctx, `SELECT seal FROM approvals WHERE id='legacy'`).Scan(&seal))
			require.False(t, seal.Valid)
			require.NoError(t, RollbackMetadataMigration(ctx, db, DialectSQLite, tc.separate, tc.current+1))
			require.NoError(t, RollbackMetadataMigration(ctx, db, DialectSQLite, tc.separate, tc.current))
			var count int
			require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM approvals WHERE id='legacy'`).Scan(&count))
			require.Equal(t, 1, count)
			require.NoError(t, MigrateMetadata(ctx, db, DialectSQLite, tc.separate))
			require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM approvals WHERE id='legacy'`).Scan(&count))
			require.Equal(t, 1, count)
		})
	}
}

func TestSealedApprovalClaimSurvivesRestartAndCannotReplay(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "approval.db")
	opened, err := OpenWithSecret(ctx, path, []byte(testSecret))
	require.NoError(t, err)
	now := time.Date(2026, time.October, 14, 2, 0, 0, 0, time.UTC)
	agent, datasource, sqlRaw := "agent-1", "ds-1", "SELECT id FROM public.t WHERE id=1 LIMIT 1"
	norm, requestReason, assessmentReason, seal := sqlRaw, "inspect incident", "manual review", "test-seal"
	expires := now.Add(time.Hour)
	created, err := opened.Approvals().Create(ctx, model.Approval{
		ID: "sealed", AgentID: &agent, DatasourceID: &datasource,
		SQLRaw: &sqlRaw, SQLNorm: &norm, RequestReason: &requestReason,
		Reason: &assessmentReason, Seal: &seal, ExpiresAt: &expires, Status: "pending",
	})
	require.NoError(t, err)
	require.Equal(t, requestReason, *created.RequestReason)
	comment := "approved for this incident"
	decided, err := opened.Approvals().DecidePending(ctx, created.ID, "approved", "admin-1", &comment, now)
	require.NoError(t, err)
	require.Equal(t, assessmentReason, *decided.Reason)
	require.Equal(t, comment, *decided.DecisionComment)
	claimed, err := opened.Approvals().ClaimApproved(ctx, created.ID, agent, now.Add(time.Minute))
	require.NoError(t, err)
	require.Equal(t, "claimed", claimed.Status)
	require.ErrorIs(t, opened.Approvals().Delete(ctx, created.ID), ErrNotFound)
	require.NoError(t, opened.Close())

	reopened, err := OpenWithSecret(ctx, path, []byte(testSecret))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopened.Close()) })
	_, err = reopened.Approvals().ClaimApproved(ctx, created.ID, agent, now.Add(2*time.Minute))
	require.ErrorIs(t, err, ErrApprovalNotExecutable)
	auditID := int64(42)
	finished, err := reopened.Approvals().CompleteClaim(ctx, created.ID, agent, "completed", &auditID, nil, now.Add(3*time.Minute))
	require.NoError(t, err)
	require.Equal(t, "completed", finished.Status)
	require.Equal(t, auditID, *finished.ExecutionAuditID)
	_, err = reopened.Approvals().CompleteClaim(ctx, created.ID, agent, "completed", &auditID, nil, now.Add(4*time.Minute))
	require.ErrorIs(t, err, ErrApprovalNotExecutable)
}

func TestApprovalClaimRejectsExpiredOtherAgentAndLegacy(t *testing.T) {
	ctx := context.Background()
	opened := openTestStore(t)
	when := time.Date(2026, time.October, 14, 2, 0, 0, 0, time.UTC)
	agent, datasource, sqlRaw, seal := "agent-1", "ds-1", "SELECT id FROM public.t WHERE id=1 LIMIT 1", "test-seal"
	expires := when.Add(time.Minute)
	for _, row := range []model.Approval{
		{ID: "expired", AgentID: &agent, DatasourceID: &datasource, SQLRaw: &sqlRaw, SQLNorm: &sqlRaw, Seal: &seal, ExpiresAt: &expires, Status: "pending"},
		{ID: "legacy", AgentID: &agent, SQLRaw: &sqlRaw, Status: "pending"},
	} {
		_, err := opened.Approvals().Create(ctx, row)
		require.NoError(t, err)
	}
	_, err := opened.Approvals().DecidePending(ctx, "expired", "approved", "admin", nil, when.Add(2*time.Minute))
	require.ErrorIs(t, err, ErrApprovalNotPending)
	_, err = opened.Approvals().DecidePending(ctx, "expired", "approved", "admin", nil, when)
	require.NoError(t, err)
	_, err = opened.Approvals().ClaimApproved(ctx, "expired", agent, when.Add(2*time.Minute))
	require.ErrorIs(t, err, ErrApprovalNotExecutable)
	_, err = opened.Approvals().ClaimApproved(ctx, "expired", "other-agent", when)
	require.ErrorIs(t, err, ErrApprovalNotExecutable)
	_, err = opened.Approvals().DecidePending(ctx, "legacy", "approved", "admin", nil, when)
	require.NoError(t, err)
	_, err = opened.Approvals().ClaimApproved(ctx, "legacy", agent, when)
	require.True(t, errors.Is(err, ErrApprovalNotExecutable))
}

func TestSealedApprovalConcurrentClaimsHaveOneWinner(t *testing.T) {
	ctx := context.Background()
	opened := openTestStore(t)
	agent, datasource, sqlRaw, seal := "agent-1", "ds-1", "SELECT id FROM public.t WHERE id=1 LIMIT 1", "test-seal"
	now := time.Now().UTC()
	expires := now.Add(time.Hour)
	_, err := opened.Approvals().Create(ctx, model.Approval{
		ID: "concurrent-claim", AgentID: &agent, DatasourceID: &datasource,
		SQLRaw: &sqlRaw, SQLNorm: &sqlRaw, Seal: &seal, ExpiresAt: &expires, Status: "pending",
	})
	require.NoError(t, err)
	_, err = opened.Approvals().DecidePending(ctx, "concurrent-claim", "approved", "admin", nil, now)
	require.NoError(t, err)
	start := make(chan struct{})
	results := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			_, claimErr := opened.Approvals().ClaimApproved(ctx, "concurrent-claim", agent, now.Add(time.Second))
			results <- claimErr
		}()
	}
	close(start)
	first, second := <-results, <-results
	if first == nil {
		require.Error(t, second)
	} else {
		require.NoError(t, second)
	}
	stored, err := opened.Approvals().Get(ctx, "concurrent-claim")
	require.NoError(t, err)
	require.Equal(t, "claimed", stored.Status)
}

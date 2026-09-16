package store

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestApprovalRepositoryDecidePendingCAS(t *testing.T) {
	opened := openTestStore(t)
	repository := opened.Approvals()
	originalReason := "original"
	_, err := repository.Create(context.Background(), model.Approval{
		ID: "approval-cas", Reason: &originalReason, Status: "pending",
	})
	require.NoError(t, err)
	decidedAt := time.Date(2026, time.September, 16, 8, 0, 0, 0, time.UTC)

	updated, err := repository.DecidePending(
		context.Background(), "approval-cas", "approved", "admin-a", nil, decidedAt,
	)
	require.NoError(t, err)
	require.Equal(t, "approved", updated.Status)
	require.Equal(t, "admin-a", *updated.Approver)
	require.Equal(t, originalReason, *updated.Reason)
	require.Equal(t, decidedAt, *updated.DecidedAt)

	_, err = repository.DecidePending(
		context.Background(), "approval-cas", "rejected", "admin-b", pointer("replacement"), decidedAt,
	)
	require.ErrorIs(t, err, ErrApprovalNotPending)
	_, err = repository.DecidePending(
		context.Background(), "missing", "approved", "admin", nil, decidedAt,
	)
	require.ErrorIs(t, err, ErrNotFound)
}

func TestApprovalRepositoryDecidePendingConcurrentCAS(t *testing.T) {
	opened := openTestStore(t)
	repository := opened.Approvals()
	_, err := repository.Create(context.Background(), model.Approval{ID: "approval-race", Status: "pending"})
	require.NoError(t, err)

	type outcome struct {
		approval model.Approval
		err      error
	}
	ready := sync.WaitGroup{}
	ready.Add(2)
	start := make(chan struct{})
	results := make(chan outcome, 2)
	for _, decision := range []struct {
		status   string
		approver string
	}{{"approved", "admin-a"}, {"rejected", "admin-b"}} {
		decision := decision
		go func() {
			ready.Done()
			<-start
			approval, decideErr := repository.DecidePending(
				context.Background(), "approval-race", decision.status, decision.approver, nil, time.Now().UTC(),
			)
			results <- outcome{approval: approval, err: decideErr}
		}()
	}
	ready.Wait()
	close(start)
	first, second := <-results, <-results
	successes, conflicts := 0, 0
	for _, result := range []outcome{first, second} {
		switch {
		case result.err == nil:
			successes++
			require.NotEmpty(t, result.approval.Approver)
		case errors.Is(result.err, ErrApprovalNotPending):
			conflicts++
		default:
			require.NoError(t, result.err)
		}
	}
	require.Equal(t, 1, successes)
	require.Equal(t, 1, conflicts)
	stored, err := repository.Get(context.Background(), "approval-race")
	require.NoError(t, err)
	require.Contains(t, []string{"approved", "rejected"}, stored.Status)
	require.Contains(t, []string{"admin-a", "admin-b"}, *stored.Approver)
}

func TestApprovalRepositoryCreatePendingWithAuditTransaction(t *testing.T) {
	t.Run("success commits linked records", func(t *testing.T) {
		opened := openTestStore(t)
		approval, auditLog, err := opened.Approvals().CreatePendingWithAudit(
			context.Background(),
			model.Approval{ID: "approval-workflow", Status: "pending"},
			model.AuditLog{Decision: "approve", SQLRaw: pointer("SELECT 1")},
		)
		require.NoError(t, err)
		require.Positive(t, auditLog.ID)
		require.NotNil(t, approval.AuditID)
		require.Equal(t, auditLog.ID, *approval.AuditID)
		page, err := opened.AuditLogs().Page(context.Background(), 1, 10)
		require.NoError(t, err)
		require.Equal(t, int64(1), page.Total)
		require.Equal(t, "approve", page.List[0].Decision)
	})

	t.Run("audit failure rolls back pending approval", func(t *testing.T) {
		opened := openTestStore(t)
		_, err := opened.db.ExecContext(context.Background(), `
CREATE TRIGGER fail_approve_audit BEFORE INSERT ON audit_logs
WHEN NEW.decision = 'approve'
BEGIN
  SELECT RAISE(ABORT, 'forced audit failure');
END`)
		require.NoError(t, err)
		_, _, err = opened.Approvals().CreatePendingWithAudit(
			context.Background(),
			model.Approval{ID: "approval-audit-fail", Status: "pending"},
			model.AuditLog{Decision: "approve"},
		)
		require.ErrorContains(t, err, "forced audit failure")
		_, err = opened.Approvals().Get(context.Background(), "approval-audit-fail")
		require.ErrorIs(t, err, ErrNotFound)
		page, err := opened.AuditLogs().Page(context.Background(), 1, 10)
		require.NoError(t, err)
		require.Zero(t, page.Total)
	})

	t.Run("backfill failure rolls back approval and audit", func(t *testing.T) {
		opened := openTestStore(t)
		_, err := opened.db.ExecContext(context.Background(), `
CREATE TRIGGER fail_approval_backfill BEFORE UPDATE OF audit_id ON approvals
BEGIN
  SELECT RAISE(ABORT, 'forced backfill failure');
END`)
		require.NoError(t, err)
		_, _, err = opened.Approvals().CreatePendingWithAudit(
			context.Background(),
			model.Approval{ID: "approval-backfill-fail", Status: "pending"},
			model.AuditLog{Decision: "approve"},
		)
		require.ErrorContains(t, err, "forced backfill failure")
		_, err = opened.Approvals().Get(context.Background(), "approval-backfill-fail")
		require.ErrorIs(t, err, ErrNotFound)
		page, err := opened.AuditLogs().Page(context.Background(), 1, 10)
		require.NoError(t, err)
		require.Zero(t, page.Total)
	})
}

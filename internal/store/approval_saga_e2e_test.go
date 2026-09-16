package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestPostgres18SeparatedApprovalAuditFirstSagaE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("dual postgres:18 approval audit-first saga E2E is an integration test")
	}
	ctx := dockerTestContext(t)
	metadataDSN := startPostgres18StoreContainer(t, ctx, "agentsql_approval_meta", "metadata-password")
	auditDSN := startPostgres18StoreContainer(t, ctx, "agentsql_approval_audit", "audit-password")
	opened, err := OpenMetadata(ctx, MetadataOptions{
		Driver:      DialectPostgres,
		PostgresDSN: metadataDSN,
		AutoMigrate: true,
		Audit: AuditOptions{
			Separate: true, Driver: DialectPostgres,
			PostgresDSN: auditDSN, AutoMigrate: true,
		},
	}, []byte(testSecret))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, opened.Close()) })

	// This trigger makes the initial INSERT shape observable: saga approvals
	// must never first appear with a NULL audit_id and be backfilled later.
	_, err = opened.metaDB.ExecContext(ctx, `
CREATE FUNCTION require_saga_audit_id() RETURNS trigger AS $$
BEGIN
  IF NEW.id LIKE 'saga-%' AND NEW.audit_id IS NULL THEN
    RAISE EXCEPTION 'saga audit_id must be present on insert';
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql`)
	require.NoError(t, err)
	_, err = opened.metaDB.ExecContext(ctx, `
CREATE TRIGGER require_saga_audit_id
BEFORE INSERT ON approvals
FOR EACH ROW EXECUTE FUNCTION require_saga_audit_id()`)
	require.NoError(t, err)

	repository := opened.Approvals()
	agentID, sqlRaw, reason := "agent-saga", "UPDATE accounts SET active = TRUE", "manual review"
	request := model.Approval{
		ID: "saga-success", AgentID: &agentID, SQLRaw: &sqlRaw, Reason: &reason, Status: "pending",
	}
	auditIntent := model.AuditLog{AgentID: &agentID, SQLRaw: &sqlRaw, Decision: "approve"}
	created, recorded, err := repository.CreatePendingWithAudit(ctx, request, auditIntent)
	require.NoError(t, err)
	require.Positive(t, recorded.ID)
	require.NotNil(t, created.AuditID)
	require.Equal(t, recorded.ID, *created.AuditID)
	require.Equal(t, request.AgentID, created.AgentID)
	require.Equal(t, request.SQLRaw, created.SQLRaw)
	require.Equal(t, request.Reason, created.Reason)
	require.Nil(t, created.Approver)
	require.Nil(t, created.DecidedAt)

	var metadataAuditID int64
	require.NoError(t, opened.metaDB.QueryRowContext(
		ctx, "SELECT audit_id FROM approvals WHERE id = $1", request.ID,
	).Scan(&metadataAuditID))
	require.Equal(t, recorded.ID, metadataAuditID)
	storedAudit, err := getInsertedAuditLog(ctx, opened.auditDB, opened.auditDriver, recorded.ID)
	require.NoError(t, err)
	require.Equal(t, "approve", storedAudit.Decision)
	require.Equal(t, sqlRaw, *storedAudit.SQLRaw)

	t.Run("audit insert failure does not touch metadata", func(t *testing.T) {
		_, triggerErr := opened.auditDB.ExecContext(ctx, `
CREATE FUNCTION fail_saga_audit_insert() RETURNS trigger AS $$
BEGIN
  IF NEW.sql_raw = 'force-audit-error' THEN
    RAISE EXCEPTION 'forced separated audit failure';
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql`)
		require.NoError(t, triggerErr)
		_, triggerErr = opened.auditDB.ExecContext(ctx, `
CREATE TRIGGER fail_saga_audit_insert
BEFORE INSERT ON audit_logs
FOR EACH ROW EXECUTE FUNCTION fail_saga_audit_insert()`)
		require.NoError(t, triggerErr)

		before := approvalCount(t, ctx, opened, "saga-audit-failure")
		forcedSQL := "force-audit-error"
		failed, failedAudit, createErr := repository.CreatePendingWithAudit(
			ctx,
			model.Approval{ID: "saga-audit-failure", Status: "pending"},
			model.AuditLog{Decision: "approve", SQLRaw: &forcedSQL},
		)
		require.Error(t, createErr)
		require.Empty(t, failed.ID)
		require.Zero(t, failedAudit.ID)
		require.Equal(t, before, approvalCount(t, ctx, opened, "saga-audit-failure"))
		require.NotContains(t, createErr.Error(), "metadata-password")
		require.NotContains(t, createErr.Error(), "audit-password")
	})

	t.Run("metadata insert failure leaves immutable orphan audit", func(t *testing.T) {
		_, triggerErr := opened.metaDB.ExecContext(ctx, `
CREATE FUNCTION fail_saga_metadata_insert() RETURNS trigger AS $$
BEGIN
  IF NEW.id = 'saga-meta-failure' THEN
    RAISE EXCEPTION 'forced separated metadata failure';
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql`)
		require.NoError(t, triggerErr)
		_, triggerErr = opened.metaDB.ExecContext(ctx, `
CREATE TRIGGER fail_saga_metadata_insert
BEFORE INSERT ON approvals
FOR EACH ROW EXECUTE FUNCTION fail_saga_metadata_insert()`)
		require.NoError(t, triggerErr)

		before := auditCount(t, ctx, opened)
		orphanSQL := "DELETE FROM protected_rows"
		failed, failedAudit, createErr := repository.CreatePendingWithAudit(
			ctx,
			model.Approval{ID: "saga-meta-failure", Status: "pending"},
			model.AuditLog{Decision: "approve", SQLRaw: &orphanSQL},
		)
		require.Error(t, createErr)
		require.Empty(t, failed.ID)
		require.Zero(t, failedAudit.ID)
		require.Zero(t, approvalCount(t, ctx, opened, "saga-meta-failure"))
		require.Equal(t, before+1, auditCount(t, ctx, opened))

		page, pageErr := opened.AuditLogs().FilteredPage(
			ctx, model.AuditFilter{Decisions: []string{"approve"}, Keyword: orphanSQL}, 1, 10,
		)
		require.NoError(t, pageErr)
		require.Equal(t, int64(1), page.Total)
		require.Equal(t, orphanSQL, *page.List[0].SQLRaw)
	})

	t.Run("same audit replay is idempotent and different audit conflicts", func(t *testing.T) {
		before := auditCount(t, ctx, opened)
		replayed, replayedAudit, replayErr := repository.CreatePendingWithAudit(ctx, request, recorded)
		require.NoError(t, replayErr)
		require.Equal(t, created.ID, replayed.ID)
		require.Equal(t, recorded.ID, replayedAudit.ID)
		require.Equal(t, before, auditCount(t, ctx, opened))
		require.Equal(t, int64(1), approvalCount(t, ctx, opened, request.ID))

		conflictSQL := "UPDATE accounts SET active = FALSE"
		failed, failedAudit, conflictErr := repository.CreatePendingWithAudit(
			ctx, request, model.AuditLog{AgentID: &agentID, SQLRaw: &conflictSQL, Decision: "approve"},
		)
		require.ErrorIs(t, conflictErr, ErrApprovalReplayConflict)
		require.Empty(t, failed.ID)
		require.Zero(t, failedAudit.ID)
		require.Equal(t, before+1, auditCount(t, ctx, opened))
		require.Equal(t, int64(1), approvalCount(t, ctx, opened, request.ID))
	})

	t.Run("decision CAS has one winner", func(t *testing.T) {
		_, createErr := repository.Create(ctx, model.Approval{ID: "approval-saga-cas", Status: "pending"})
		require.NoError(t, createErr)
		auditsBeforeDecision := auditCount(t, ctx, opened)
		start := make(chan struct{})
		results := make(chan error, 2)
		for _, decision := range []string{"approved", "rejected"} {
			decision := decision
			go func() {
				<-start
				_, decideErr := repository.DecidePending(
					context.Background(), "approval-saga-cas", decision, "dba", nil, time.Now().UTC(),
				)
				results <- decideErr
			}()
		}
		close(start)
		successes, conflicts := 0, 0
		for range 2 {
			switch decideErr := <-results; {
			case decideErr == nil:
				successes++
			case errors.Is(decideErr, ErrApprovalNotPending):
				conflicts++
			default:
				require.NoError(t, decideErr)
			}
		}
		require.Equal(t, 1, successes)
		require.Equal(t, 1, conflicts)
		require.Equal(t, auditsBeforeDecision, auditCount(t, ctx, opened))
	})

	page, err := opened.AuditLogs().FilteredPage(
		ctx, model.AuditFilter{Decisions: []string{"approve"}, Keyword: sqlRaw}, 1, 1,
	)
	require.NoError(t, err)
	require.Equal(t, int64(1), page.Total)
	require.Len(t, page.List, 1)
	require.Equal(t, recorded.ID, page.List[0].ID)
}

func approvalCount(t *testing.T, ctx context.Context, opened *Store, id string) int64 {
	t.Helper()
	var count int64
	require.NoError(t, opened.metaDB.QueryRowContext(
		ctx, "SELECT COUNT(*) FROM approvals WHERE id = $1", id,
	).Scan(&count))
	return count
}

func auditCount(t *testing.T, ctx context.Context, opened *Store) int64 {
	t.Helper()
	var count int64
	require.NoError(t, opened.auditDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM audit_logs").Scan(&count))
	return count
}

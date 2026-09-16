package store

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestAuditAndApprovalDialectMatrix(t *testing.T) {
	forEachStore(t, func(t *testing.T, opened *Store) {
		ctx := context.Background()
		base := time.Date(2026, time.December, 31, 23, 58, 0, 0, time.UTC)
		fixtures := []struct {
			timestamp time.Time
			decision  string
			statement string
			raw       string
			objects   string
		}{
			{base, "allow", "SELECT", "SELECT MiXeD FROM t", "public.t"},
			{base.Add(time.Minute), "deny", "UPDATE", "UPDATE t SET v='100%_wow!'", "public.special_%!"},
			{base.Add(2 * time.Minute), "approve", "DELETE", "DELETE FROM t", "public.t"},
			{base.Add(3 * time.Minute), "warn", "SELECT", "SELECT 2", "other.t"},
		}
		for index, fixture := range fixtures {
			execStoreSQL(t, opened, `
INSERT INTO audit_logs (
  ts, agent_id, datasource_id, session_id, mcp_tool, sql_raw, sql_norm,
  stmt_type, objects, decision, risk_level
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				fixture.timestamp, "agent-matrix", "ds-matrix", "session-matrix", "query",
				fixture.raw, fixture.raw, fixture.statement, fixture.objects, fixture.decision, index+1,
			)
		}

		assertAuditTotal := func(filter model.AuditFilter, expected int64) {
			t.Helper()
			page, err := opened.AuditLogs().FilteredPage(ctx, filter, 1, 100)
			require.NoError(t, err)
			require.Equal(t, expected, page.Total)
		}
		assertAuditTotal(model.AuditFilter{Keyword: "mixed"}, 1)
		assertAuditTotal(model.AuditFilter{Keyword: "%_wow!"}, 1)
		assertAuditTotal(model.AuditFilter{ObjectLike: "_%!"}, 1)
		assertAuditTotal(model.AuditFilter{Decisions: []string{"deny", "approve"}}, 2)
		assertAuditTotal(model.AuditFilter{StmtTypes: []string{"SELECT", "DELETE"}}, 3)
		assertAuditTotal(model.AuditFilter{TimeStart: pointer(base.Add(time.Minute)), TimeEnd: pointer(base.Add(2 * time.Minute))}, 2)
		assertAuditTotal(model.AuditFilter{
			AgentID: pointer("agent-matrix"), DatasourceID: pointer("ds-matrix"),
			SessionID: pointer("session-matrix"), MCPTool: pointer("query"),
			RiskMin: pointer(2), RiskMax: pointer(3),
		}, 2)
		firstPage, err := opened.AuditLogs().FilteredPage(ctx, model.AuditFilter{}, 1, 2)
		require.NoError(t, err)
		secondPage, err := opened.AuditLogs().FilteredPage(ctx, model.AuditFilter{}, 2, 2)
		require.NoError(t, err)
		require.Equal(t, int64(4), firstPage.Total)
		require.Len(t, firstPage.List, 2)
		require.Len(t, secondPage.List, 2)
		require.Greater(t, firstPage.List[1].ID, secondPage.List[0].ID)

		const concurrentInserts = 12
		ids := make(chan int64, concurrentInserts)
		errorsChannel := make(chan error, concurrentInserts)
		var ready sync.WaitGroup
		ready.Add(concurrentInserts)
		start := make(chan struct{})
		for range concurrentInserts {
			go func() {
				ready.Done()
				<-start
				inserted, insertErr := opened.AuditLogs().Insert(ctx, model.AuditLog{Decision: "allow"})
				if insertErr != nil {
					errorsChannel <- insertErr
					return
				}
				ids <- inserted.ID
			}()
		}
		ready.Wait()
		close(start)
		concurrentIDs := make([]int64, 0, concurrentInserts)
		for range concurrentInserts {
			select {
			case insertErr := <-errorsChannel:
				require.NoError(t, insertErr)
			case id := <-ids:
				concurrentIDs = append(concurrentIDs, id)
			}
		}
		require.Len(t, concurrentIDs, concurrentInserts)
		sort.Slice(concurrentIDs, func(i, j int) bool { return concurrentIDs[i] < concurrentIDs[j] })
		for index, id := range concurrentIDs {
			require.Positive(t, id)
			if index > 0 {
				require.Greater(t, id, concurrentIDs[index-1])
			}
		}
		seen := make(map[int64]struct{}, len(fixtures)+concurrentInserts)
		for pageNumber := 1; ; pageNumber++ {
			page, pageErr := opened.AuditLogs().FilteredPage(ctx, model.AuditFilter{}, pageNumber, 3)
			require.NoError(t, pageErr)
			for _, log := range page.List {
				_, duplicate := seen[log.ID]
				require.False(t, duplicate, "audit ID %d appeared on more than one stable page", log.ID)
				seen[log.ID] = struct{}{}
			}
			if int64(len(seen)) == page.Total {
				require.Equal(t, int64(len(fixtures)+concurrentInserts), page.Total)
				break
			}
			require.NotEmpty(t, page.List)
		}

		auditLog, err := opened.AuditLogs().Insert(ctx, model.AuditLog{Decision: "approve"})
		require.NoError(t, err)
		approval, err := opened.Approvals().Create(ctx, model.Approval{
			ID: "approval-crud-matrix", AuditID: &auditLog.ID, Status: "pending",
		})
		require.NoError(t, err)
		decidedAt := base.Add(time.Hour)
		approval.Status = "approved"
		approval.Approver = pointer("dba")
		approval.DecidedAt = &decidedAt
		approval, err = opened.Approvals().Update(ctx, approval)
		require.NoError(t, err)
		require.Equal(t, "approved", approval.Status)
		approvalPage, err := opened.Approvals().ListPage(ctx, "approved", 1, 10)
		require.NoError(t, err)
		require.Equal(t, int64(1), approvalPage.Total)
		require.NoError(t, opened.Approvals().Delete(ctx, approval.ID))

		_, err = opened.Approvals().Create(ctx, model.Approval{ID: "approval-race-matrix", Status: "pending"})
		require.NoError(t, err)
		type decisionResult struct{ err error }
		decisions := make(chan decisionResult, 2)
		ready = sync.WaitGroup{}
		ready.Add(2)
		start = make(chan struct{})
		for _, status := range []string{"approved", "rejected"} {
			status := status
			go func() {
				ready.Done()
				<-start
				_, decideErr := opened.Approvals().DecidePending(
					ctx, "approval-race-matrix", status, "matrix-dba", nil, time.Now().UTC(),
				)
				decisions <- decisionResult{err: decideErr}
			}()
		}
		ready.Wait()
		close(start)
		successes, conflicts := 0, 0
		for range 2 {
			result := <-decisions
			if result.err == nil {
				successes++
			} else if errors.Is(result.err, ErrApprovalNotPending) {
				conflicts++
			} else {
				require.NoError(t, result.err)
			}
		}
		require.Equal(t, 1, successes)
		require.Equal(t, 1, conflicts)
		_, err = opened.Approvals().DecidePending(ctx, "approval-race-matrix", "approved", "dba", nil, time.Now().UTC())
		require.ErrorIs(t, err, ErrApprovalNotPending)
		_, err = opened.Approvals().DecidePending(ctx, "approval-missing", "approved", "dba", nil, time.Now().UTC())
		require.ErrorIs(t, err, ErrNotFound)

		created, recorded, err := opened.Approvals().CreatePendingWithAudit(
			ctx,
			model.Approval{ID: "approval-workflow-matrix", Status: "pending"},
			model.AuditLog{Decision: "approve", SQLRaw: pointer("SELECT 1")},
		)
		require.NoError(t, err)
		require.Positive(t, recorded.ID)
		require.NotNil(t, created.AuditID)
		require.Equal(t, recorded.ID, *created.AuditID)

		installApprovalFailureTriggers(t, opened)
		_, _, err = opened.Approvals().CreatePendingWithAudit(
			ctx,
			model.Approval{ID: "approval-audit-fail", Status: "pending"},
			model.AuditLog{Decision: "approve", SQLRaw: pointer("force-audit")},
		)
		require.ErrorContains(t, err, "forced audit failure")
		_, err = opened.Approvals().Get(ctx, "approval-audit-fail")
		require.ErrorIs(t, err, ErrNotFound)
		assertAuditTotal(model.AuditFilter{Keyword: "force-audit"}, 0)
		_, _, err = opened.Approvals().CreatePendingWithAudit(
			ctx,
			model.Approval{ID: "approval-backfill-fail", Status: "pending"},
			model.AuditLog{Decision: "approve", SQLRaw: pointer("force-backfill")},
		)
		require.ErrorContains(t, err, "forced backfill failure")
		_, err = opened.Approvals().Get(ctx, "approval-backfill-fail")
		require.ErrorIs(t, err, ErrNotFound)
		assertAuditTotal(model.AuditFilter{Keyword: "force-backfill"}, 0)
	})
}

func installApprovalFailureTriggers(t *testing.T, opened *Store) {
	t.Helper()
	if opened.metaDriver == DialectSQLite {
		execStoreSQL(t, opened, `
CREATE TRIGGER fail_matrix_approve_audit BEFORE INSERT ON audit_logs
WHEN NEW.decision = 'approve' AND NEW.sql_raw = 'force-audit'
BEGIN
  SELECT RAISE(ABORT, 'forced audit failure');
END`)
		execStoreSQL(t, opened, `
CREATE TRIGGER fail_matrix_approval_backfill BEFORE UPDATE OF audit_id ON approvals
WHEN NEW.id = 'approval-backfill-fail'
BEGIN
  SELECT RAISE(ABORT, 'forced backfill failure');
END`)
		return
	}
	execStoreSQL(t, opened, `
CREATE FUNCTION fail_matrix_approve_audit() RETURNS trigger AS $$
BEGIN
  IF NEW.decision = 'approve' AND NEW.sql_raw = 'force-audit' THEN
    RAISE EXCEPTION 'forced audit failure';
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql`)
	execStoreSQL(t, opened, `
CREATE TRIGGER fail_matrix_approve_audit BEFORE INSERT ON audit_logs
FOR EACH ROW EXECUTE FUNCTION fail_matrix_approve_audit()`)
	execStoreSQL(t, opened, `
CREATE FUNCTION fail_matrix_approval_backfill() RETURNS trigger AS $$
BEGIN
  IF NEW.id = 'approval-backfill-fail' THEN
    RAISE EXCEPTION 'forced backfill failure';
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql`)
	execStoreSQL(t, opened, `
CREATE TRIGGER fail_matrix_approval_backfill BEFORE UPDATE OF audit_id ON approvals
FOR EACH ROW EXECUTE FUNCTION fail_matrix_approval_backfill()`)
}

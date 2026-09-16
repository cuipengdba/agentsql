package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestApprovalRepositorySeparatedAuditFirstSaga(t *testing.T) {
	ctx := context.Background()
	opened := openSeparatedSQLiteApprovalStore(t, ctx)
	repository := opened.Approvals()
	agentID, sqlRaw, reason := "agent-unit", "UPDATE t SET value = 1", "review"
	request := model.Approval{
		ID: "saga-unit", AgentID: &agentID, SQLRaw: &sqlRaw, Reason: &reason, Status: "pending",
	}
	intent := model.AuditLog{AgentID: &agentID, SQLRaw: &sqlRaw, Decision: "approve"}

	created, recorded, err := repository.CreatePendingWithAudit(ctx, request, intent)
	require.NoError(t, err)
	require.Equal(t, recorded.ID, *created.AuditID)
	require.Equal(t, int64(1), sqliteRowCount(t, opened.auditDB, "audit_logs"))

	replayed, replayedAudit, err := repository.CreatePendingWithAudit(ctx, request, recorded)
	require.NoError(t, err)
	require.Equal(t, created.ID, replayed.ID)
	require.Equal(t, recorded.ID, replayedAudit.ID)
	require.Equal(t, int64(1), sqliteRowCount(t, opened.auditDB, "audit_logs"))

	_, _, err = repository.CreatePendingWithAudit(ctx, request, intent)
	require.ErrorIs(t, err, ErrApprovalReplayConflict)
	require.Equal(t, int64(2), sqliteRowCount(t, opened.auditDB, "audit_logs"))
	require.Equal(t, int64(1), sqliteRowCount(t, opened.metaDB, "approvals"))

	_, err = opened.metaDB.ExecContext(ctx, `
CREATE TRIGGER fail_separated_approval_insert BEFORE INSERT ON approvals
WHEN NEW.id = 'saga-orphan'
BEGIN
  SELECT RAISE(ABORT, 'forced separated metadata failure');
END`)
	require.NoError(t, err)
	orphanSQL := "DELETE FROM t"
	failed, failedAudit, err := repository.CreatePendingWithAudit(
		ctx,
		model.Approval{ID: "saga-orphan", Status: "pending"},
		model.AuditLog{Decision: "approve", SQLRaw: &orphanSQL},
	)
	require.ErrorContains(t, err, "forced separated metadata failure")
	require.Empty(t, failed.ID)
	require.Zero(t, failedAudit.ID)
	require.Equal(t, int64(3), sqliteRowCount(t, opened.auditDB, "audit_logs"))
	require.Equal(t, int64(1), sqliteRowCount(t, opened.metaDB, "approvals"))

	page, err := opened.AuditLogs().FilteredPage(
		ctx, model.AuditFilter{Decisions: []string{"approve"}, Keyword: orphanSQL}, 1, 10,
	)
	require.NoError(t, err)
	require.Equal(t, int64(1), page.Total)
}

func openSeparatedSQLiteApprovalStore(t *testing.T, ctx context.Context) *Store {
	t.Helper()
	metadataDB, err := openDatabase(
		DialectSQLite, filepath.Join(t.TempDir(), "metadata.db"), "", 0, 0, 0,
	)
	require.NoError(t, err)
	auditDB, err := openDatabase(
		DialectSQLite, filepath.Join(t.TempDir(), "audit.db"), "", 0, 0, 0,
	)
	require.NoError(t, err)
	require.NoError(t, MigrateMetadata(ctx, metadataDB, DialectSQLite, true))
	// SQLite is not a supported production audit-only target; the shared
	// migration supplies the same audit_logs schema for this fast saga test.
	require.NoError(t, MigrateMetadata(ctx, auditDB, DialectSQLite, false))
	opened := &Store{
		metaDB: metadataDB, auditDB: auditDB,
		metaDriver: DialectSQLite, auditDriver: DialectSQLite, auditSeparate: true,
	}
	t.Cleanup(func() { require.NoError(t, opened.Close()) })
	return opened
}

func sqliteRowCount(t *testing.T, database interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, table string) int64 {
	t.Helper()
	var count int64
	require.NoError(t, database.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM "+table).Scan(&count))
	return count
}

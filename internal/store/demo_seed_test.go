package store

import (
	"context"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestSeedHistoricalAuditsSQLiteInsertReplayApprovalAndSequence(t *testing.T) {
	ctx := context.Background()
	opened := openTestStore(t)
	audits, approvals := demoHistoricalSeedFixture()

	recorded, persisted, err := opened.SeedHistoricalAudits(ctx, audits, approvals)
	require.NoError(t, err)
	require.Len(t, recorded, 2)
	require.Len(t, persisted, 1)
	for index := range audits {
		require.True(t, audits[index].TS.Equal(recorded[index].TS))
		require.Equal(t, int64(index+1), recorded[index].ID)
	}
	require.NotNil(t, persisted[0].AuditID)
	require.Equal(t, recorded[1].ID, *persisted[0].AuditID)
	require.Equal(t, approvals[0].Status, persisted[0].Status)
	require.Equal(t, approvals[0].Reason, persisted[0].Reason)
	require.Equal(t, approvals[0].Approver, persisted[0].Approver)
	require.True(t, approvals[0].DecidedAt.Equal(*persisted[0].DecidedAt))

	replayedAudits, replayedApprovals, err := opened.SeedHistoricalAudits(ctx, audits, approvals)
	require.NoError(t, err)
	require.Equal(t, recorded, replayedAudits)
	require.Equal(t, persisted, replayedApprovals)
	require.Equal(t, int64(2), sqliteRowCount(t, opened.auditDB, "audit_logs"))
	require.Equal(t, int64(1), sqliteRowCount(t, opened.metaDB, "approvals"))

	var orphans int64
	require.NoError(t, opened.metaDB.QueryRowContext(ctx, `
SELECT COUNT(*) FROM approvals p
LEFT JOIN audit_logs a ON a.id = p.audit_id
WHERE p.audit_id IS NULL OR a.id IS NULL`).Scan(&orphans))
	require.Zero(t, orphans)

	production, err := opened.AuditLogs().Insert(ctx, model.AuditLog{Decision: "allow"})
	require.NoError(t, err)
	require.Equal(t, int64(3), production.ID)
	require.False(t, production.TS.IsZero())
}

func TestSeedHistoricalAuditsSQLiteFailsClosedOnChangedReplay(t *testing.T) {
	ctx := context.Background()
	opened := openTestStore(t)
	audits, approvals := demoHistoricalSeedFixture()
	_, _, err := opened.SeedHistoricalAudits(ctx, audits, approvals)
	require.NoError(t, err)

	_, err = opened.auditDB.ExecContext(ctx, `
UPDATE audit_logs SET model_name = 'tampered' WHERE session_id = ?`, *audits[0].SessionID)
	require.NoError(t, err)
	_, _, err = opened.SeedHistoricalAudits(ctx, audits, approvals)
	require.ErrorIs(t, err, ErrDemoSeedConflict)
	require.Equal(t, int64(2), sqliteRowCount(t, opened.auditDB, "audit_logs"))
	require.Equal(t, int64(1), sqliteRowCount(t, opened.metaDB, "approvals"))
}

func TestSeedHistoricalAuditsSQLiteRejectsNonDemoAudit(t *testing.T) {
	ctx := context.Background()
	opened := openTestStore(t)
	realSession := "production-session"
	_, err := opened.AuditLogs().Insert(ctx, model.AuditLog{
		SessionID: &realSession,
		Decision:  "allow",
	})
	require.NoError(t, err)
	audits, approvals := demoHistoricalSeedFixture()

	_, _, err = opened.SeedHistoricalAudits(ctx, audits, approvals)
	require.ErrorIs(t, err, ErrDemoSeedUnsafeDatabase)
	require.Equal(t, int64(1), sqliteRowCount(t, opened.auditDB, "audit_logs"))
	require.Zero(t, sqliteRowCount(t, opened.metaDB, "approvals"))
}

func TestSeedHistoricalAuditsSQLiteRollsBackInvalidBatch(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		mutate func([]model.AuditLog)
	}{
		{name: "invalid decision", mutate: func(audits []model.AuditLog) { audits[1].Decision = "invalid" }},
		{name: "zero timestamp", mutate: func(audits []model.AuditLog) { audits[1].TS = time.Time{} }},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			ctx := context.Background()
			opened := openTestStore(t)
			audits, approvals := demoHistoricalSeedFixture()
			testCase.mutate(audits)

			_, _, err := opened.SeedHistoricalAudits(ctx, audits, approvals)
			require.Error(t, err)
			require.Zero(t, sqliteRowCount(t, opened.auditDB, "audit_logs"))
			require.Zero(t, sqliteRowCount(t, opened.metaDB, "approvals"))
		})
	}
}

func TestSeedHistoricalAuditsSQLiteRejectsPartialAuditBatch(t *testing.T) {
	ctx := context.Background()
	opened := openTestStore(t)
	audits, approvals := demoHistoricalSeedFixture()
	_, err := insertHistoricalAuditLog(ctx, opened.auditDB, opened.auditDriver, audits[0])
	require.NoError(t, err)

	_, _, err = opened.SeedHistoricalAudits(ctx, audits, approvals)
	require.ErrorIs(t, err, ErrDemoSeedConflict)
	require.Equal(t, int64(1), sqliteRowCount(t, opened.auditDB, "audit_logs"))
	require.Zero(t, sqliteRowCount(t, opened.metaDB, "approvals"))
}

func demoHistoricalSeedFixture() ([]model.AuditLog, []model.Approval) {
	firstSession := DemoSeedSessionPrefix + "000001"
	secondSession := DemoSeedSessionPrefix + "000002"
	agentID := "demo-agent"
	allowSQL := "SELECT id FROM customers"
	approveSQL := "UPDATE customers SET active = TRUE"
	modelName := "demo-model"
	reason := "demo review"
	approver := "demo-dba"
	decidedAt := time.Date(2026, 8, 20, 12, 30, 0, 0, time.UTC)
	return []model.AuditLog{
		{
			TS: time.Date(2026, 8, 19, 10, 0, 0, 0, time.UTC), AgentID: &agentID,
			SessionID: &firstSession, SQLRaw: &allowSQL, Decision: "allow", ModelName: &modelName,
		},
		{
			TS: time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC), AgentID: &agentID,
			SessionID: &secondSession, SQLRaw: &approveSQL, Decision: "approve", ModelName: &modelName,
		},
	}, []model.Approval{
		{
			ID: "demo-approval-000002", AgentID: &agentID, SQLRaw: &approveSQL,
			Reason: &reason, Status: "approved", Approver: &approver, DecidedAt: &decidedAt,
		},
	}
}

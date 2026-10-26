package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestAuditStreamTenantCursorBackfillAndRollback(t *testing.T) {
	ctx := context.Background()
	opened, err := OpenWithSecret(ctx, filepath.Join(t.TempDir(), "audit-stream.db"), []byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, err)
	defer opened.Close()
	tenantA, err := WithTenant(ctx, DefaultTenantID)
	require.NoError(t, err)
	tenantB, err := WithTenant(ctx, "tenant_other")
	require.NoError(t, err)
	repository := opened.AuditLogs()
	a1, err := repository.Insert(tenantA, model.AuditLog{Decision: "allow"})
	require.NoError(t, err)
	b1, err := repository.Insert(tenantB, model.AuditLog{Decision: "deny"})
	require.NoError(t, err)
	a2, err := repository.Insert(tenantA, model.AuditLog{Decision: "deny"})
	require.NoError(t, err)
	assertStream := func() {
		t.Helper()
		latestA, latestErr := repository.LatestStreamSequence(tenantA)
		require.NoError(t, latestErr)
		require.Equal(t, int64(2), latestA)
		latestB, latestErr := repository.LatestStreamSequence(tenantB)
		require.NoError(t, latestErr)
		require.Equal(t, int64(1), latestB)
		pageA, pageErr := repository.StreamAfter(tenantA, 0, 1)
		require.NoError(t, pageErr)
		require.Len(t, pageA, 1)
		require.Equal(t, int64(1), pageA[0].Sequence)
		require.Equal(t, a1.ID, pageA[0].Audit.ID)
		pageA, pageErr = repository.StreamAfter(tenantA, 1, 10)
		require.NoError(t, pageErr)
		require.Len(t, pageA, 1)
		require.Equal(t, int64(2), pageA[0].Sequence)
		require.Equal(t, a2.ID, pageA[0].Audit.ID)
		pageB, pageErr := repository.StreamAfter(tenantB, 0, 10)
		require.NoError(t, pageErr)
		require.Len(t, pageB, 1)
		require.Equal(t, int64(1), pageB[0].Sequence)
		require.Equal(t, b1.ID, pageB[0].Audit.ID)
	}
	assertStream()

	transaction, err := opened.auditDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	_, err = transaction.ExecContext(ctx, "INSERT INTO audit_logs(tenant_id,decision) VALUES(?,?)", DefaultTenantID, "deny")
	require.NoError(t, err)
	require.NoError(t, transaction.Rollback())
	assertStream()

	require.NoError(t, RollbackMetadataMigration(ctx, opened.auditDB, DialectSQLite, false, 28))
	require.NoError(t, RollbackMetadataMigration(ctx, opened.auditDB, DialectSQLite, false, 27))
	require.NoError(t, RollbackMetadataMigration(ctx, opened.auditDB, DialectSQLite, false, 26))
	require.NoError(t, RollbackMetadataMigration(ctx, opened.auditDB, DialectSQLite, false, 25))
	require.NoError(t, Migrate(ctx, opened.auditDB, DialectSQLite))
	assertStream()
	// A damaged stream must stop at the first missing committed tenant sequence.
	_, err = opened.auditDB.ExecContext(ctx, "DELETE FROM audit_stream_events WHERE tenant_id=? AND seq=?", DefaultTenantID, 1)
	require.NoError(t, err)
	_, err = repository.StreamAfter(tenantA, 0, 10)
	require.ErrorContains(t, err, "sequence gap")
	// A missing tail record has no later row to expose the gap in a page.
	_, err = opened.auditDB.ExecContext(ctx, "DELETE FROM audit_stream_events WHERE tenant_id=? AND seq=?", DefaultTenantID, 2)
	require.NoError(t, err)
	_, err = repository.StreamAfter(tenantA, 1, 10)
	require.ErrorContains(t, err, "sequence gap")
}

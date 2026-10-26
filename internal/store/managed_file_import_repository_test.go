package store

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestManagedFileImportJournalTenantCASAndAuditRollback(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	b, _, err := s.SQLBotBindings().Create(ctx, sqlbotTestBinding(t, s), "operator")
	require.NoError(t, err)
	r := s.ManagedFileImports()
	hash := strings.Repeat("a", 64)
	item, err := r.Begin(ctx, b, hash, 32, 2, b.AgentSQLUserID)
	require.NoError(t, err)
	require.Len(t, item.ID, 32)
	require.Equal(t, "preparing", item.Status)
	require.Equal(t, b.ID, item.BindingID)
	require.Equal(t, hash, item.FileSHA256)
	require.Equal(t, int64(1), item.Revision)
	_, err = r.Begin(ctx, b, hash, 32, 1, b.AgentSQLUserID)
	require.Error(t, err, "one owner cannot reserve two unfinished imports")
	otherTenant, err := WithTenant(ctx, "another-tenant")
	require.NoError(t, err)
	_, err = r.Get(otherTenant, item.ID)
	require.ErrorIs(t, err, ErrNotFound)
	items, err := r.ListPending(otherTenant, 100)
	require.NoError(t, err)
	require.Empty(t, items)
	_, err = r.Advance(ctx, item.ID, item.Revision, "preparing", "published", b.AgentSQLUserID)
	require.ErrorIs(t, err, ErrManagedFileState)
	_, err = r.Advance(ctx, item.ID, item.Revision, "preparing", "staged", "other-user")
	require.ErrorIs(t, err, ErrManagedFileState)
	_, err = s.metaDB.Exec(`CREATE TRIGGER fail_managed_file_audit BEFORE INSERT ON management_audit_outbox WHEN NEW.action LIKE 'managed_file.%' BEGIN SELECT RAISE(FAIL,'audit unavailable'); END`)
	require.NoError(t, err)
	_, err = r.Advance(ctx, item.ID, item.Revision, "preparing", "staged", b.AgentSQLUserID)
	require.Error(t, err)
	current, err := r.Get(ctx, item.ID)
	require.NoError(t, err)
	require.Equal(t, "preparing", current.Status)
	require.Equal(t, item.Revision, current.Revision)
	items, err = r.ListPending(ctx, 100)
	require.NoError(t, err)
	require.Len(t, items, 1)
	_, err = s.metaDB.Exec(`DROP TRIGGER fail_managed_file_audit`)
	require.NoError(t, err)
	for _, next := range []string{"staged", "policy_ready", "published", "deleting", "deleted"} {
		current, err = r.Advance(ctx, item.ID, current.Revision, current.Status, next, b.AgentSQLUserID)
		require.NoError(t, err)
		require.Equal(t, next, current.Status)
		if next == "published" {
			pending, err := r.ListPending(ctx, 100)
			require.NoError(t, err)
			require.Empty(t, pending)
		}
	}
	items, err = r.ListPending(ctx, 100)
	require.NoError(t, err)
	require.Empty(t, items)
	_, err = s.metaDB.Exec(`CREATE TRIGGER fail_managed_file_audit_again BEFORE INSERT ON management_audit_outbox WHEN NEW.action LIKE 'managed_file.%' BEGIN SELECT RAISE(FAIL,'audit unavailable'); END`)
	require.NoError(t, err)
	_, err = r.Begin(ctx, b, hash, 32, 1, b.AgentSQLUserID)
	require.Error(t, err)
	items, err = r.ListPending(ctx, 100)
	require.NoError(t, err)
	require.Empty(t, items, "audit outage must roll back a new operation reservation")
	_, err = s.metaDB.Exec(`DROP TRIGGER fail_managed_file_audit_again`)
	require.NoError(t, err)
	var audits int
	require.NoError(t, s.metaDB.QueryRow(`SELECT COUNT(*) FROM management_audit_outbox WHERE action LIKE 'managed_file.%'`).Scan(&audits))
	require.Equal(t, 6, audits)
}

func TestManagedFileImportJournalRequiresCurrentBindingAndExactOwner(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	b, _, err := s.SQLBotBindings().Create(ctx, sqlbotTestBinding(t, s), "operator")
	require.NoError(t, err)
	r := s.ManagedFileImports()
	hash := strings.Repeat("0", 64)
	for _, test := range []struct {
		binding SQLBotBinding
		sha     string
		bytes   int64
		sheets  int
		actor   string
	}{
		{b, hash, 1, 1, "other-user"},
		{b, strings.Repeat("Z", 64), 1, 1, b.AgentSQLUserID},
		{b, hash, 0, 1, b.AgentSQLUserID},
		{b, hash, 1, 9, b.AgentSQLUserID},
	} {
		_, err = r.Begin(ctx, test.binding, test.sha, test.bytes, test.sheets, test.actor)
		require.ErrorIs(t, err, ErrManagedFileState)
	}
	journal, err := r.Begin(ctx, b, hash, 1, 1, b.AgentSQLUserID)
	require.NoError(t, err)
	journal, err = r.Advance(ctx, journal.ID, journal.Revision, "preparing", "staged", b.AgentSQLUserID)
	require.NoError(t, err)
	rotated, _, err := s.SQLBotBindings().Rotate(ctx, b.ID, b.Revision, "operator")
	require.NoError(t, err)
	_, err = r.Advance(ctx, journal.ID, journal.Revision, "staged", "policy_ready", b.AgentSQLUserID)
	require.ErrorIs(t, err, ErrManagedFileState)
	_, err = r.Begin(ctx, b, hash, 1, 1, b.AgentSQLUserID)
	require.ErrorIs(t, err, ErrManagedFileState)
	rotated.Enabled = false
	_, err = s.SQLBotBindings().Update(ctx, rotated, "operator")
	require.NoError(t, err)
	_, err = r.Begin(ctx, rotated, hash, 1, 1, b.AgentSQLUserID)
	require.ErrorIs(t, err, ErrManagedFileState)
}

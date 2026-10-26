package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func sqlbotTestBinding(t *testing.T, s *Store) SQLBotBinding {
	t.Helper()
	agent, ds := createPolicyDependencies(t, s)
	_, err := s.RBAC().CreateUser(context.Background(), model.User{ID: "sqlbot-human", TenantID: DefaultTenantID, Username: "sqlbot-human", DisplayName: "Human", PasswordHash: "test-hash", Status: "active"})
	require.NoError(t, err)
	return SQLBotBinding{WorkspaceID: "1", DatasourceName: "Sales", SQLBotUserID: "1", AgentSQLUserID: "sqlbot-human", AgentID: agent.ID, DatasourceID: ds.ID, SchemaName: "public", NumericColumns: []string{"amount"}, Enabled: true}
}

func TestSQLBotBindingIsolationCredentialRotationAndRevocation(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	r := s.SQLBotBindings()
	b, plain, err := r.Create(ctx, sqlbotTestBinding(t, s), "operator")
	require.NoError(t, err)
	require.Len(t, plain, 43)
	encoded, err := json.Marshal(b)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), plain)
	require.NotContains(t, string(encoded), b.ConnectorTokenHash)
	require.NotContains(t, string(encoded), "connector_token_hash")
	resolved, err := r.ResolveConnector(ctx, plain)
	require.NoError(t, err)
	require.Equal(t, b.ID, resolved.ID)
	_, err = r.ValidateRuntime(ctx, b.ID, b.Revision, b.AgentID, b.AgentSQLUserID)
	require.NoError(t, err)
	for _, pair := range [][2]string{{"another-agent", b.AgentSQLUserID}, {b.AgentID, "another-user"}} {
		_, err = r.ValidateRuntime(ctx, b.ID, b.Revision, pair[0], pair[1])
		require.ErrorIs(t, err, ErrSQLBotBindingDenied)
	}
	other, err := WithTenant(ctx, "another-tenant")
	require.NoError(t, err)
	_, err = r.Get(other, b.ID)
	require.ErrorIs(t, err, ErrNotFound)
	_, err = r.ValidateRuntime(other, b.ID, b.Revision, b.AgentID, b.AgentSQLUserID)
	require.ErrorIs(t, err, ErrSQLBotBindingDenied)
	items, err := r.List(other)
	require.NoError(t, err)
	require.Empty(t, items)
	_, _, err = r.Create(other, SQLBotBinding{WorkspaceID: "1", DatasourceName: "Sales", SQLBotUserID: "1", AgentSQLUserID: b.AgentSQLUserID, AgentID: b.AgentID, DatasourceID: b.DatasourceID, SchemaName: "public"}, "operator")
	require.ErrorIs(t, err, ErrSQLBotBindingInvalid)
	rotated, newPlain, err := r.Rotate(ctx, b.ID, b.Revision, "operator")
	require.NoError(t, err)
	require.NotEqual(t, plain, newPlain)
	require.Equal(t, b.Revision+1, rotated.Revision)
	_, err = r.ResolveConnector(ctx, plain)
	require.ErrorIs(t, err, ErrSQLBotBindingDenied)
	_, err = r.ValidateRuntime(ctx, b.ID, b.Revision, b.AgentID, b.AgentSQLUserID)
	require.ErrorIs(t, err, ErrSQLBotBindingDenied)
	_, err = r.ResolveConnector(ctx, newPlain)
	require.NoError(t, err)
	_, err = r.Update(ctx, b, "operator")
	require.ErrorIs(t, err, ErrSQLBotBindingStale)
	rotated.Enabled = false
	disabled, err := r.Update(ctx, rotated, "operator")
	require.NoError(t, err)
	_, err = r.ResolveConnector(ctx, newPlain)
	require.ErrorIs(t, err, ErrSQLBotBindingDenied)
	_, err = r.ValidateRuntime(ctx, disabled.ID, disabled.Revision, disabled.AgentID, disabled.AgentSQLUserID)
	require.ErrorIs(t, err, ErrSQLBotBindingDenied)
	require.NoError(t, r.Delete(ctx, disabled.ID, disabled.Revision, "operator"))
	_, err = r.Get(ctx, b.ID)
	require.ErrorIs(t, err, ErrNotFound)
	var count int
	require.NoError(t, s.metaDB.QueryRow(`SELECT COUNT(*) FROM management_audit_outbox WHERE action LIKE 'sqlbot_binding.%'`).Scan(&count))
	require.Equal(t, 4, count)
	var audit string
	rows, err := s.metaDB.Query(`SELECT details_json FROM management_audit_outbox WHERE action LIKE 'sqlbot_binding.%'`)
	require.NoError(t, err)
	defer rows.Close()
	for rows.Next() {
		require.NoError(t, rows.Scan(&audit))
		require.NotContains(t, audit, plain)
		require.NotContains(t, audit, newPlain)
		require.NotContains(t, audit, b.ConnectorTokenHash)
	}
}

func TestSQLBotChildBindingInheritsParentRevocation(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	r := s.SQLBotBindings()
	parent, _, err := r.Create(ctx, sqlbotTestBinding(t, s), "operator")
	require.NoError(t, err)
	child := parent
	child.DatasourceName = "isolated-import"
	child.ParentBindingID = parent.ID
	child.ParentRevision = parent.Revision
	child, token, err := r.Create(ctx, child, "operator")
	require.NoError(t, err)
	require.Equal(t, parent.ID, child.ParentBindingID)
	require.Equal(t, parent.Revision, child.ParentRevision)
	_, err = r.ResolveConnector(ctx, token)
	require.NoError(t, err)
	_, err = r.ValidateRuntime(ctx, child.ID, child.Revision, child.AgentID, child.AgentSQLUserID)
	require.NoError(t, err)
	_, err = s.metaDB.Exec(`CREATE TRIGGER fail_import_replay_audit BEFORE INSERT ON management_audit_outbox WHEN NEW.action='managed_file.replay' BEGIN SELECT RAISE(FAIL,'audit unavailable'); END`)
	require.NoError(t, err)
	require.Error(t, r.RecordImportReplay(ctx, child, child.AgentSQLUserID))
	_, err = s.metaDB.Exec(`DROP TRIGGER fail_import_replay_audit`)
	require.NoError(t, err)
	require.NoError(t, r.RecordImportReplay(ctx, child, child.AgentSQLUserID))
	var replayAudits int
	require.NoError(t, s.metaDB.QueryRow(`SELECT COUNT(*) FROM management_audit_outbox WHERE action='managed_file.replay' AND actor_id=?`, child.AgentSQLUserID).Scan(&replayAudits))
	require.Equal(t, 1, replayAudits)
	journal, err := s.ManagedFileImports().Begin(ctx, child, strings.Repeat("a", 64), 1, 1, child.AgentSQLUserID, strings.Repeat("c", 64))
	require.NoError(t, err)
	journal, err = s.ManagedFileImports().Advance(ctx, journal.ID, journal.Revision, "preparing", "cleanup_needed", child.AgentSQLUserID)
	require.NoError(t, err)
	_, err = s.ManagedFileImports().Advance(ctx, journal.ID, journal.Revision, "cleanup_needed", "deleted", child.AgentSQLUserID)
	require.NoError(t, err)

	grandchild := child
	grandchild.DatasourceName = "nested-import"
	grandchild.ParentBindingID = child.ID
	grandchild.ParentRevision = child.Revision
	_, _, err = r.Create(ctx, grandchild, "operator")
	require.ErrorIs(t, err, ErrSQLBotBindingDenied)

	_, _, err = r.Rotate(ctx, parent.ID, parent.Revision, "operator")
	require.NoError(t, err)
	_, err = r.ResolveConnector(ctx, token)
	require.ErrorIs(t, err, ErrSQLBotBindingDenied)
	_, err = r.ValidateRuntime(ctx, child.ID, child.Revision, child.AgentID, child.AgentSQLUserID)
	require.ErrorIs(t, err, ErrSQLBotBindingDenied)
	require.ErrorIs(t, r.RecordImportReplay(ctx, child, child.AgentSQLUserID), ErrSQLBotBindingDenied)
	_, err = s.ManagedFileImports().Begin(ctx, child, strings.Repeat("b", 64), 1, 1, child.AgentSQLUserID, strings.Repeat("d", 64))
	require.ErrorIs(t, err, ErrManagedFileState)

	// A child update cannot silently adopt the parent's new revision.
	child.Enabled = false
	_, err = r.Update(ctx, child, "operator")
	require.ErrorIs(t, err, ErrSQLBotBindingDenied)
	child.ParentBindingID, child.ParentRevision = "", 0 // Management JSON omits both fields.
	_, err = r.Update(ctx, child, "operator")
	require.ErrorIs(t, err, ErrSQLBotBindingDenied)
	child.ParentBindingID, child.ParentRevision = parent.ID, 1
	require.Equal(t, int64(1), child.ParentRevision)
	_, err = r.ResolveConnector(ctx, token)
	require.ErrorIs(t, err, ErrSQLBotBindingDenied)
	parent, err = r.Get(ctx, parent.ID)
	require.NoError(t, err)
	require.NoError(t, r.Delete(ctx, parent.ID, parent.Revision, "operator"))
	_, err = r.ValidateRuntime(ctx, child.ID, child.Revision, child.AgentID, child.AgentSQLUserID)
	require.ErrorIs(t, err, ErrSQLBotBindingDenied)
}

func TestSQLBotBindingMutationFailsClosedOnAuditOutage(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	r := s.SQLBotBindings()
	input := sqlbotTestBinding(t, s)
	b, plain, err := r.Create(ctx, input, "operator")
	require.NoError(t, err)
	_, err = s.metaDB.Exec(`CREATE TRIGGER fail_sqlbot_audit BEFORE INSERT ON management_audit_outbox WHEN NEW.action LIKE 'sqlbot_binding.%' BEGIN SELECT RAISE(FAIL,'injected audit outage'); END`)
	require.NoError(t, err)
	b.Enabled = false
	_, err = r.Update(ctx, b, "operator")
	require.Error(t, err)
	_, _, err = r.Rotate(ctx, b.ID, b.Revision, "operator")
	require.Error(t, err)
	err = r.Delete(ctx, b.ID, b.Revision, "operator")
	require.Error(t, err)
	current, err := r.ResolveConnector(ctx, plain)
	require.NoError(t, err)
	require.Equal(t, b.Revision, current.Revision)
	require.True(t, current.Enabled)
	input.SQLBotUserID = "2"
	_, returned, err := r.Create(ctx, input, "operator")
	require.Error(t, err)
	require.Empty(t, returned)
	items, err := r.List(ctx)
	require.NoError(t, err)
	require.Len(t, items, 1)
}

func TestSQLBotBindingRejectsAmbiguousAndInvalidConfigurations(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	r := s.SQLBotBindings()
	input := sqlbotTestBinding(t, s)
	_, _, err := r.Create(ctx, input, "operator")
	require.NoError(t, err)
	_, _, err = r.Create(ctx, input, "operator")
	require.ErrorIs(t, err, ErrSQLBotBindingInvalid)
	for _, mutate := range []func(*SQLBotBinding){
		func(b *SQLBotBinding) { b.WorkspaceID = "0" }, func(b *SQLBotBinding) { b.SQLBotUserID = "01" }, func(b *SQLBotBinding) { b.SchemaName = "public;DROP" }, func(b *SQLBotBinding) { b.AgentKeyEnv = "password=value" }, func(b *SQLBotBinding) { b.NumericColumns = []string{"amount", "amount"} }, func(b *SQLBotBinding) { b.DatasourceName = strings.Repeat("x", 201) }, func(b *SQLBotBinding) { b.AgentSQLUserID = "missing" },
	} {
		candidate := input
		candidate.SQLBotUserID = "2"
		mutate(&candidate)
		_, _, err = r.Create(ctx, candidate, "operator")
		require.ErrorIs(t, err, ErrSQLBotBindingInvalid)
	}
	for _, credential := range []string{"", strings.Repeat("x", 100), strings.Repeat("!", 43)} {
		_, err = r.ResolveConnector(ctx, credential)
		require.ErrorIs(t, err, ErrSQLBotBindingDenied)
	}
}

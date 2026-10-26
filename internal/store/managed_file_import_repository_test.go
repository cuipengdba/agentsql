package store

import (
	"context"
	"strings"
	"testing"

	"github.com/cuipengdba/agentsql/internal/managedfile"
	"github.com/cuipengdba/agentsql/internal/model"
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
	items, err = r.ListRecoverableByDatasource(otherTenant, b.DatasourceID, 100)
	require.NoError(t, err)
	require.Empty(t, items)
	items, err = r.ListRecoverableByDatasource(ctx, "another-datasource", 100)
	require.NoError(t, err)
	require.Empty(t, items)
	items, err = r.ListRecoverableByDatasource(ctx, b.DatasourceID, 100)
	require.NoError(t, err)
	require.Len(t, items, 1)
	_, err = r.Advance(ctx, item.ID, item.Revision, "preparing", "published", b.AgentSQLUserID)
	require.ErrorIs(t, err, ErrManagedFileState)
	_, err = r.Advance(ctx, item.ID, item.Revision, "preparing", "staged", "other-user")
	require.ErrorIs(t, err, ErrManagedFileState)
	operation, err := managedfile.ParseOperationID(item.ID)
	require.NoError(t, err)
	first, err := managedfile.TableName(operation, 0)
	require.NoError(t, err)
	second, err := managedfile.TableName(operation, 1)
	require.NoError(t, err)
	tables := []ManagedFileTableRecord{{Name: first, Rows: 1}, {Name: second, Rows: 2}}
	_, err = s.metaDB.Exec(`CREATE TRIGGER fail_managed_file_audit BEFORE INSERT ON management_audit_outbox WHEN NEW.action LIKE 'managed_file.%' BEGIN SELECT RAISE(FAIL,'audit unavailable'); END`)
	require.NoError(t, err)
	_, err = r.MarkStaged(ctx, item.ID, item.Revision, tables, b.AgentSQLUserID)
	require.Error(t, err)
	current, err := r.Get(ctx, item.ID)
	require.NoError(t, err)
	require.Equal(t, "preparing", current.Status)
	require.Equal(t, item.Revision, current.Revision)
	var recorded int
	require.NoError(t, s.metaDB.QueryRow(`SELECT COUNT(*) FROM managed_file_tables WHERE operation_id=?`, item.ID).Scan(&recorded))
	require.Zero(t, recorded, "audit outage must roll back table mappings")
	items, err = r.ListPending(ctx, 100)
	require.NoError(t, err)
	require.Len(t, items, 1)
	_, err = s.metaDB.Exec(`DROP TRIGGER fail_managed_file_audit`)
	require.NoError(t, err)
	_, err = r.MarkStaged(ctx, item.ID, item.Revision, []ManagedFileTableRecord{{Name: "agentsql_file_invalid", Rows: 1}}, b.AgentSQLUserID)
	require.ErrorIs(t, err, ErrManagedFileState)
	current, err = r.MarkStaged(ctx, item.ID, item.Revision, tables, b.AgentSQLUserID)
	require.NoError(t, err)
	allowed, err := r.PublishedTableOwner(ctx, b.ID, b.DatasourceID, b.AgentID, b.AgentSQLUserID, first)
	require.NoError(t, err)
	require.False(t, allowed, "staged tables are not published")
	_, err = r.Advance(ctx, item.ID, current.Revision, "staged", "policy_ready", b.AgentSQLUserID)
	require.ErrorIs(t, err, ErrManagedFileState, "policy publication has no evidence-bearing implementation yet")
	_, err = r.Advance(ctx, item.ID, current.Revision, "staged", "published", b.AgentSQLUserID)
	require.ErrorIs(t, err, ErrManagedFileState)
	for _, next := range []string{"cleanup_needed", "deleted"} {
		current, err = r.Advance(ctx, item.ID, current.Revision, current.Status, next, b.AgentSQLUserID)
		require.NoError(t, err)
		require.Equal(t, next, current.Status)
		allowed, err = r.PublishedTableOwner(ctx, b.ID, b.DatasourceID, b.AgentID, b.AgentSQLUserID, first)
		require.NoError(t, err)
		require.False(t, allowed)
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
	require.Equal(t, 4, audits)
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
	operation, err := managedfile.ParseOperationID(journal.ID)
	require.NoError(t, err)
	name, err := managedfile.TableName(operation, 0)
	require.NoError(t, err)
	journal, err = r.MarkStaged(ctx, journal.ID, journal.Revision, []ManagedFileTableRecord{{Name: name, Rows: 0}}, b.AgentSQLUserID)
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

func TestManagedFilePublishedOwnerPredicateRequiresCurrentBinding(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	b, _, err := s.SQLBotBindings().Create(ctx, sqlbotTestBinding(t, s), "operator")
	require.NoError(t, err)
	parent := b
	b.DatasourceName = "isolated-import"
	b.ParentBindingID, b.ParentRevision = parent.ID, parent.Revision
	b, _, err = s.SQLBotBindings().Create(ctx, b, "operator")
	require.NoError(t, err)
	r := s.ManagedFileImports()
	item, err := r.Begin(ctx, b, strings.Repeat("a", 64), 1, 1, b.AgentSQLUserID)
	require.NoError(t, err)
	operation, err := managedfile.ParseOperationID(item.ID)
	require.NoError(t, err)
	name, err := managedfile.TableName(operation, 0)
	require.NoError(t, err)
	item, err = r.MarkStaged(ctx, item.ID, item.Revision, []ManagedFileTableRecord{{Name: name, Rows: 1}}, b.AgentSQLUserID)
	require.NoError(t, err)
	_, err = r.Publish(ctx, item.ID, item.Revision, b.AgentSQLUserID)
	require.ErrorIs(t, err, ErrManagedFileState, "unbound staged tables cannot publish")
	policyID, err := managedfile.PolicyID(operation, 0)
	require.NoError(t, err)
	columns := "amount"
	created, err := s.Policies().Create(ctx, model.Policy{ID: policyID, AgentID: b.AgentID, DatasourceID: b.DatasourceID,
		ObjectType: "column", ObjectName: "agentsql_files." + name, Columns: &columns, Action: "allow"})
	require.NoError(t, err)
	stable, fingerprint := "1:2", "fixture-catalog"
	bound, err := s.Policies().FinalizeColumnBindingTwoPhase(ctx, policyID, created.Revision,
		func(context.Context) error { return nil },
		func(context.Context) (RevalidatedColumnBinding, error) {
			return RevalidatedColumnBinding{Binding: model.RelationPolicyBinding{ID: policyID + "-binding", SchemaName: "agentsql_files",
				RelationName: name, StableObjectID: &stable, CatalogFingerprint: &fingerprint, Status: "healthy"},
				Permissions: []model.PolicyColumnPermission{{RelationEnrollmentID: policyID + "-binding", ColumnOrdinal: 1,
					ColumnName: "amount", ColumnTypeDigest: "int8", Usage: "output"}}}, nil
		})
	require.NoError(t, err)
	require.NotNil(t, bound.RelationBinding)
	item, err = r.MarkPolicyReady(ctx, item.ID, item.Revision, b.AgentSQLUserID)
	require.NoError(t, err)
	require.Equal(t, "policy_ready", item.Status)
	// This store-level fixture stands in for the separate physical grant; the
	// runtime publication coordinator must verify that grant before Publish.
	item, err = r.Publish(ctx, item.ID, item.Revision, b.AgentSQLUserID)
	require.NoError(t, err)
	require.Equal(t, "published", item.Status)
	recoverable, err := r.ListRecoverableByDatasource(ctx, b.DatasourceID, 100)
	require.NoError(t, err)
	require.Empty(t, recoverable, "a healthy published table must never be selected for cleanup")
	for _, test := range []struct {
		datasource, agent, owner, table string
		allowed                         bool
	}{
		{b.DatasourceID, b.AgentID, b.AgentSQLUserID, name, true},
		{b.DatasourceID, b.AgentID, "another-user", name, false},
		{b.DatasourceID, "another-agent", b.AgentSQLUserID, name, false},
		{"another-datasource", b.AgentID, b.AgentSQLUserID, name, false},
		{b.DatasourceID, b.AgentID, b.AgentSQLUserID, "agentsql_file_11111111111111111111111111111111", false},
	} {
		allowed, err := r.PublishedTableOwner(ctx, b.ID, test.datasource, test.agent, test.owner, test.table)
		require.NoError(t, err)
		require.Equal(t, test.allowed, allowed)
	}
	allowed, err := r.PublishedTableOwner(ctx, "another-binding", b.DatasourceID, b.AgentID, b.AgentSQLUserID, name)
	require.NoError(t, err)
	require.False(t, allowed, "workspace binding is part of file ownership")
	otherTenant, err := WithTenant(ctx, "another-tenant")
	require.NoError(t, err)
	allowed, err = r.PublishedTableOwner(otherTenant, b.ID, b.DatasourceID, b.AgentID, b.AgentSQLUserID, name)
	require.NoError(t, err)
	require.False(t, allowed)
	_, _, err = s.SQLBotBindings().Rotate(ctx, parent.ID, parent.Revision, "operator")
	require.NoError(t, err)
	allowed, err = r.PublishedTableOwner(ctx, b.ID, b.DatasourceID, b.AgentID, b.AgentSQLUserID, name)
	require.NoError(t, err)
	require.False(t, allowed, "parent rotation must revoke an already published child table")
	recoverable, err = r.ListRecoverableByDatasource(ctx, b.DatasourceID, 100)
	require.NoError(t, err)
	require.Len(t, recoverable, 1)
	require.Equal(t, item.ID, recoverable[0].ID)
	require.Equal(t, "published", recoverable[0].Status)
	_, _, err = s.SQLBotBindings().Rotate(ctx, b.ID, b.Revision, "operator")
	require.NoError(t, err)
	allowed, err = r.PublishedTableOwner(ctx, b.ID, b.DatasourceID, b.AgentID, b.AgentSQLUserID, name)
	require.NoError(t, err)
	require.False(t, allowed)
}

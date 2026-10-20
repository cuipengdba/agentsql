package store

import (
	"context"
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/policybundle"
	"github.com/stretchr/testify/require"
)

func TestPolicyBundleReviewApplyDriftAndTenantIsolation(t *testing.T) {
	opened := openTestStore(t)
	agent, datasource := createPolicyDependencies(t, opened)
	repository := opened.PolicyBundles()
	ctx := context.Background()
	current, digest, err := repository.Current(ctx)
	require.NoError(t, err)
	require.Empty(t, current.Policies)
	desired := current
	desired.Policies = append(desired.Policies, policybundle.Entry{
		ID: "bundle-allow", AgentID: agent.ID, DatasourceID: datasource.ID,
		ObjectType: "table", ObjectName: "public.orders", Action: "allow",
	})
	proposal, err := repository.Create(ctx, desired, digest, "maker")
	require.NoError(t, err)
	require.Equal(t, "pending", proposal.Status)
	otherTenant, err := WithTenant(ctx, "other-tenant")
	require.NoError(t, err)
	_, err = repository.Get(otherTenant, proposal.ID)
	require.ErrorIs(t, err, ErrNotFound)
	_, err = repository.Create(otherTenant, desired, digest, "maker")
	require.ErrorIs(t, err, ErrPolicyBundleInvalid)
	_, err = repository.Review(ctx, proposal.ID, "maker", true)
	require.ErrorIs(t, err, ErrPolicyBundleReviewRequired)
	proposal, err = repository.Review(ctx, proposal.ID, "reviewer", true)
	require.NoError(t, err)
	require.Equal(t, "approved", proposal.Status)
	_, err = repository.Apply(ctx, proposal.ID, "maker")
	require.ErrorIs(t, err, ErrPolicyBundleReviewRequired)
	status, err := repository.Apply(ctx, proposal.ID, "reviewer")
	require.NoError(t, err)
	require.Equal(t, "approved", status.State)
	_, err = opened.Policies().Get(ctx, "bundle-allow")
	require.NoError(t, err)
	status, err = repository.Status(ctx)
	require.NoError(t, err)
	require.Equal(t, "approved", status.State)
	require.NoError(t, opened.Policies().Delete(ctx, "bundle-allow"))
	status, err = repository.Status(ctx)
	require.NoError(t, err)
	require.Equal(t, "drifted", status.State)
	var count int
	require.NoError(t, opened.metaDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM management_audit_outbox WHERE action LIKE 'policy_bundle.%'`).Scan(&count))
	require.Equal(t, 3, count)
}

func TestPolicyBundleApplyRollsBackWhenAuditOutboxFails(t *testing.T) {
	opened := openTestStore(t)
	agent, datasource := createPolicyDependencies(t, opened)
	repository := opened.PolicyBundles()
	ctx := context.Background()
	current, digest, err := repository.Current(ctx)
	require.NoError(t, err)
	desired := current
	desired.Policies = []policybundle.Entry{{
		ID: "bundle-rollback", AgentID: agent.ID, DatasourceID: datasource.ID,
		ObjectType: "table", ObjectName: "public.orders", Action: "allow",
	}}
	proposal, err := repository.Create(ctx, desired, digest, "maker")
	require.NoError(t, err)
	_, err = repository.Review(ctx, proposal.ID, "reviewer", true)
	require.NoError(t, err)
	_, err = opened.metaDB.ExecContext(ctx, `CREATE TRIGGER fail_bundle_audit BEFORE INSERT ON management_audit_outbox WHEN NEW.action='policy_bundle.apply' BEGIN SELECT RAISE(FAIL,'injected audit outage'); END`)
	require.NoError(t, err)
	_, err = repository.Apply(ctx, proposal.ID, "reviewer")
	require.Error(t, err)
	_, err = opened.Policies().Get(ctx, "bundle-rollback")
	require.ErrorIs(t, err, ErrNotFound)
	stored, err := repository.Get(ctx, proposal.ID)
	require.NoError(t, err)
	require.Equal(t, "approved", stored.Status)
	status, err := repository.Status(ctx)
	require.NoError(t, err)
	require.Equal(t, "uninitialized", status.State)
	_, err = opened.metaDB.ExecContext(ctx, `DROP TRIGGER fail_bundle_audit`)
	require.NoError(t, err)
	_, err = repository.Apply(ctx, proposal.ID, "reviewer")
	require.NoError(t, err)
}

func TestPolicyBundleApplyRejectsStaleSource(t *testing.T) {
	opened := openTestStore(t)
	agent, datasource := createPolicyDependencies(t, opened)
	repository := opened.PolicyBundles()
	ctx := context.Background()
	current, digest, err := repository.Current(ctx)
	require.NoError(t, err)
	desired := current
	desired.Policies = []policybundle.Entry{{ID: "planned", AgentID: agent.ID, DatasourceID: datasource.ID, ObjectType: "table", ObjectName: "public.orders", Action: "allow"}}
	proposal, err := repository.Create(ctx, desired, digest, "maker")
	require.NoError(t, err)
	_, err = repository.Review(ctx, proposal.ID, "reviewer", true)
	require.NoError(t, err)
	_, err = opened.Policies().Create(ctx, model.Policy{ID: "direct", AgentID: agent.ID, DatasourceID: datasource.ID, ObjectType: "table", ObjectName: "public.other", Action: "allow"})
	require.NoError(t, err)
	_, err = repository.Apply(ctx, proposal.ID, "reviewer")
	require.ErrorIs(t, err, ErrPolicyBundleStale)
	_, err = opened.Policies().Get(ctx, "planned")
	require.ErrorIs(t, err, ErrNotFound)
}

func TestPolicyBundleAppliesCreateUpdateDeleteTogether(t *testing.T) {
	opened := openTestStore(t)
	agent, datasource := createPolicyDependencies(t, opened)
	ctx := context.Background()
	for _, id := range []string{"update-me", "delete-me"} {
		_, err := opened.Policies().Create(ctx, model.Policy{ID: id, AgentID: agent.ID, DatasourceID: datasource.ID,
			ObjectType: "table", ObjectName: "public." + id, Action: "deny"})
		require.NoError(t, err)
	}
	repository := opened.PolicyBundles()
	current, digest, err := repository.Current(ctx)
	require.NoError(t, err)
	var updated policybundle.Entry
	for _, row := range current.Policies {
		if row.ID == "update-me" {
			updated = row
		}
	}
	require.NotEmpty(t, updated.ID)
	updated.Action = "allow"
	desired := current
	desired.Policies = []policybundle.Entry{updated, {ID: "create-me", AgentID: agent.ID, DatasourceID: datasource.ID,
		ObjectType: "table", ObjectName: "public.created", Action: "allow"}}
	proposal, err := repository.Create(ctx, desired, digest, "maker")
	require.NoError(t, err)
	_, err = repository.Review(ctx, proposal.ID, "reviewer", true)
	require.NoError(t, err)
	_, err = repository.Apply(ctx, proposal.ID, "reviewer")
	require.NoError(t, err)
	newPolicy, err := opened.Policies().Get(ctx, "create-me")
	require.NoError(t, err)
	require.Equal(t, int64(1), newPolicy.Revision)
	changed, err := opened.Policies().Get(ctx, "update-me")
	require.NoError(t, err)
	require.Equal(t, "allow", changed.Action)
	require.Equal(t, updated.Revision+1, changed.Revision)
	_, err = opened.Policies().Get(ctx, "delete-me")
	require.ErrorIs(t, err, ErrNotFound)
	status, err := repository.Status(ctx)
	require.NoError(t, err)
	require.Equal(t, "approved", status.State)
}

func TestPolicyBundleRejectsUnknownTenantReferences(t *testing.T) {
	opened := openTestStore(t)
	_, datasource := createPolicyDependencies(t, opened)
	ctx := context.Background()
	repository := opened.PolicyBundles()
	current, digest, err := repository.Current(ctx)
	require.NoError(t, err)
	desired := current
	desired.Policies = []policybundle.Entry{{ID: "unknown-agent", AgentID: "agent-in-other-tenant", DatasourceID: datasource.ID,
		ObjectType: "table", ObjectName: "public.orders", Action: "allow"}}
	_, err = repository.Create(ctx, desired, digest, "maker")
	require.ErrorIs(t, err, ErrPolicyBundleInvalid)
}

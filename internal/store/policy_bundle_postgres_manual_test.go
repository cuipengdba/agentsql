package store

import (
	"context"
	"os"
	"testing"

	"github.com/cuipengdba/agentsql/internal/policybundle"
	"github.com/stretchr/testify/require"
)

// The external DSN variant avoids testcontainers' reaper when Docker Desktop
// cannot publish its temporary port. It is run against a fresh test database.
func TestPolicyBundlePostgresManualE2E(t *testing.T) {
	dsn := os.Getenv("AGENTSQL_TEST_POLICY_BUNDLE_PG_DSN")
	if dsn == "" {
		t.Skip("set AGENTSQL_TEST_POLICY_BUNDLE_PG_DSN for manual PostgreSQL E2E")
	}
	ctx := context.Background()
	opened, err := OpenMetadata(ctx, MetadataOptions{Driver: DialectPostgres, PostgresDSN: dsn, AutoMigrate: true}, []byte(testSecret))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, opened.Close()) })
	agent, datasource := createPolicyDependencies(t, opened)
	repository := opened.PolicyBundles()
	current, digest, err := repository.Current(ctx)
	require.NoError(t, err)
	desired := current
	desired.Policies = append(desired.Policies, policybundle.Entry{ID: "pg-policy-bundle", AgentID: agent.ID, DatasourceID: datasource.ID,
		ObjectType: "table", ObjectName: "public.orders", Action: "allow"})
	proposal, err := repository.Create(ctx, desired, digest, "maker")
	require.NoError(t, err)
	_, err = repository.Review(ctx, proposal.ID, "maker", true)
	require.ErrorIs(t, err, ErrPolicyBundleReviewRequired)
	_, err = repository.Review(ctx, proposal.ID, "reviewer", true)
	require.NoError(t, err)
	_, err = opened.metaDB.ExecContext(ctx, `CREATE FUNCTION fail_policy_bundle_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action = 'policy_bundle.apply' THEN RAISE EXCEPTION 'injected audit outage'; END IF; RETURN NEW; END $$`)
	require.NoError(t, err)
	_, err = opened.metaDB.ExecContext(ctx, `CREATE TRIGGER fail_bundle_audit BEFORE INSERT ON management_audit_outbox FOR EACH ROW EXECUTE FUNCTION fail_policy_bundle_audit()`)
	require.NoError(t, err)
	_, err = repository.Apply(ctx, proposal.ID, "reviewer")
	require.Error(t, err)
	_, err = opened.Policies().Get(ctx, "pg-policy-bundle")
	require.ErrorIs(t, err, ErrNotFound)
	_, err = opened.metaDB.ExecContext(ctx, `DROP TRIGGER fail_bundle_audit ON management_audit_outbox`)
	require.NoError(t, err)
	_, err = opened.metaDB.ExecContext(ctx, `DROP FUNCTION fail_policy_bundle_audit()`)
	require.NoError(t, err)
	status, err := repository.Apply(ctx, proposal.ID, "reviewer")
	require.NoError(t, err)
	require.Equal(t, "approved", status.State)
	otherTenant, err := WithTenant(ctx, "other-tenant")
	require.NoError(t, err)
	_, err = repository.Get(otherTenant, proposal.ID)
	require.ErrorIs(t, err, ErrNotFound)
}

package store

import (
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestDiscoverySeparateAuditSagaE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("dual postgres:18 discovery audit-first saga E2E is an integration test")
	}
	ctx := dockerTestContext(t)
	metadataDSN := startPostgres18StoreContainer(t, ctx, "agentsql_discovery_meta", "metadata-password")
	auditDSN := startPostgres18StoreContainer(t, ctx, "agentsql_discovery_audit", "audit-password")
	opened, err := OpenMetadata(ctx, MetadataOptions{
		Driver: DialectPostgres, PostgresDSN: metadataDSN, AutoMigrate: true,
		Audit: AuditOptions{Separate: true, Driver: DialectPostgres, PostgresDSN: auditDSN, AutoMigrate: true},
	}, []byte(testSecret))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, opened.Close()) })

	repository := opened.MaskRules()
	buildAudit := func(marker string) func(DiscoveryApplyOutcome) (model.AuditLog, error) {
		return func(outcome DiscoveryApplyOutcome) (model.AuditLog, error) {
			action, actorType, actorID, datasource := "discover.apply", "admin", "admin", "ds-1"
			details := `{"marker":"` + marker + `"}`
			return model.AuditLog{DatasourceID: &datasource, Decision: "allow", Action: &action, ActorType: &actorType, ActorID: &actorID, DetailsJSON: &details}, nil
		}
	}

	t.Run("audit first success", func(t *testing.T) {
		outcome, recorded, err := repository.ApplyDiscoveryDraftsWithAudit(ctx, "ds-1", []DiscoveryDraft{{ID: "draft-success", ColumnName: "phone", SensitiveType: "phone", Algo: "mask"}}, buildAudit("success"))
		require.NoError(t, err)
		require.Positive(t, recorded.ID)
		require.Len(t, outcome.Created, 1)
		stored, err := repository.Get(ctx, "draft-success")
		require.NoError(t, err)
		require.False(t, stored.Enabled)
		require.Empty(t, stored.TableName)
	})

	t.Run("audit failure leaves metadata untouched", func(t *testing.T) {
		_, err := opened.auditDB.ExecContext(ctx, `
CREATE FUNCTION fail_discovery_audit_insert() RETURNS trigger AS $$
BEGIN
  IF NEW.details_json LIKE '%force-audit-error%' THEN
    RAISE EXCEPTION 'forced discovery audit failure';
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql`)
		require.NoError(t, err)
		_, err = opened.auditDB.ExecContext(ctx, `
CREATE TRIGGER fail_discovery_audit_insert BEFORE INSERT ON audit_logs
FOR EACH ROW EXECUTE FUNCTION fail_discovery_audit_insert()`)
		require.NoError(t, err)
		_, _, err = repository.ApplyDiscoveryDraftsWithAudit(ctx, "ds-1", []DiscoveryDraft{{ID: "draft-audit-fail", ColumnName: "email", SensitiveType: "email", Algo: "mask"}}, buildAudit("force-audit-error"))
		require.Error(t, err)
		_, getErr := repository.Get(ctx, "draft-audit-fail")
		require.ErrorIs(t, getErr, ErrNotFound)
	})

	t.Run("metadata failure leaves immutable orphan audit", func(t *testing.T) {
		_, err := opened.metaDB.ExecContext(ctx, `
CREATE FUNCTION fail_discovery_draft_insert() RETURNS trigger AS $$
BEGIN
  IF NEW.id = 'draft-meta-fail' THEN
    RAISE EXCEPTION 'forced discovery metadata failure';
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql`)
		require.NoError(t, err)
		_, err = opened.metaDB.ExecContext(ctx, `
CREATE TRIGGER fail_discovery_draft_insert BEFORE INSERT ON mask_rules
FOR EACH ROW EXECUTE FUNCTION fail_discovery_draft_insert()`)
		require.NoError(t, err)
		before := auditCount(t, ctx, opened)
		_, recorded, err := repository.ApplyDiscoveryDraftsWithAudit(ctx, "ds-1", []DiscoveryDraft{{ID: "draft-meta-fail", ColumnName: "mobile", SensitiveType: "phone", Algo: "mask"}}, buildAudit("orphan"))
		require.Error(t, err)
		require.Positive(t, recorded.ID)
		require.Equal(t, before+1, auditCount(t, ctx, opened))
		_, getErr := repository.Get(ctx, "draft-meta-fail")
		require.ErrorIs(t, getErr, ErrNotFound)
	})
}

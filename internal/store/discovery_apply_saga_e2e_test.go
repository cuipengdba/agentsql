package store

import (
	"sync"
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestDiscoverySeparateOutboxE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("dual postgres:18 discovery outbox E2E is an integration test")
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
	competitor, err := OpenMetadata(ctx, MetadataOptions{
		Driver: DialectPostgres, PostgresDSN: metadataDSN, AutoMigrate: true,
		Audit: AuditOptions{Separate: true, Driver: DialectPostgres, PostgresDSN: auditDSN, AutoMigrate: true},
	}, []byte(testSecret))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, competitor.Close()) })
	buildAudit := func(marker string) func(DiscoveryApplyOutcome) (model.AuditLog, error) {
		return func(outcome DiscoveryApplyOutcome) (model.AuditLog, error) {
			action, actorType, actorID, datasource := "discover.apply", "admin", "admin", "ds-1"
			details := `{"marker":"` + marker + `"}`
			return model.AuditLog{DatasourceID: &datasource, Decision: "allow", Action: &action, ActorType: &actorType, ActorID: &actorID, DetailsJSON: &details}, nil
		}
	}

	t.Run("rule and outbox commit together", func(t *testing.T) {
		outcome, recorded, err := repository.ApplyDiscoveryDraftsWithAudit(ctx, "ds-1", []DiscoveryDraft{{ID: "draft-success", ColumnName: "phone", SensitiveType: "phone", Algo: "mask"}}, buildAudit("success"))
		require.NoError(t, err)
		require.Zero(t, recorded.ID)
		require.Len(t, outcome.Created, 1)
		stored, err := repository.Get(ctx, "draft-success")
		require.NoError(t, err)
		require.False(t, stored.Enabled)
		require.Empty(t, stored.TableName)
		requireOutboxCount(t, ctx, opened.metaDB, 1)
		require.Zero(t, auditCount(t, ctx, opened))
	})

	t.Run("outbox failure rolls back metadata", func(t *testing.T) {
		_, err := opened.metaDB.ExecContext(ctx, `
CREATE FUNCTION fail_discovery_outbox_insert() RETURNS trigger AS $$
BEGIN
  IF NEW.details_json LIKE '%force-audit-error%' THEN
    RAISE EXCEPTION 'forced discovery outbox failure';
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql`)
		require.NoError(t, err)
		_, err = opened.metaDB.ExecContext(ctx, `
CREATE TRIGGER fail_discovery_outbox_insert BEFORE INSERT ON management_audit_outbox
FOR EACH ROW EXECUTE FUNCTION fail_discovery_outbox_insert()`)
		require.NoError(t, err)
		_, _, err = repository.ApplyDiscoveryDraftsWithAudit(ctx, "ds-1", []DiscoveryDraft{{ID: "draft-audit-fail", ColumnName: "email", SensitiveType: "email", Algo: "mask"}}, buildAudit("force-audit-error"))
		require.Error(t, err)
		_, getErr := repository.Get(ctx, "draft-audit-fail")
		require.ErrorIs(t, getErr, ErrNotFound)
		requireOutboxCount(t, ctx, opened.metaDB, 1)
		require.Zero(t, auditCount(t, ctx, opened))
	})

	t.Run("metadata failure leaves no outbox or audit", func(t *testing.T) {
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
		beforeOutbox := 1
		beforeAudit := auditCount(t, ctx, opened)
		_, recorded, err := repository.ApplyDiscoveryDraftsWithAudit(ctx, "ds-1", []DiscoveryDraft{{ID: "draft-meta-fail", ColumnName: "mobile", SensitiveType: "phone", Algo: "mask"}}, buildAudit("orphan"))
		require.Error(t, err)
		require.Zero(t, recorded.ID)
		requireOutboxCount(t, ctx, opened.metaDB, beforeOutbox)
		require.Equal(t, beforeAudit, auditCount(t, ctx, opened))
		_, getErr := repository.Get(ctx, "draft-meta-fail")
		require.ErrorIs(t, getErr, ErrNotFound)
	})

	t.Run("independent processes converge through unique constraint", func(t *testing.T) {
		repositories := []*MaskRuleRepository{opened.MaskRules(), competitor.MaskRules()}
		start := make(chan struct{})
		outcomes := make(chan DiscoveryApplyOutcome, len(repositories))
		errorsSeen := make(chan error, len(repositories))
		var wait sync.WaitGroup
		for index, competingRepository := range repositories {
			wait.Add(1)
			go func(index int, competingRepository *MaskRuleRepository) {
				defer wait.Done()
				<-start
				outcome, _, applyErr := competingRepository.ApplyDiscoveryDraftsWithAudit(ctx, "ds-1", []DiscoveryDraft{{
					ID: "cross-process-" + string(rune('a'+index)), TableName: "customers", ColumnName: "race_column",
					SensitiveType: "generic", Algo: "block",
				}}, buildAudit("cross-process"))
				outcomes <- outcome
				errorsSeen <- applyErr
			}(index, competingRepository)
		}
		close(start)
		wait.Wait()
		close(outcomes)
		close(errorsSeen)
		for applyErr := range errorsSeen {
			require.NoError(t, applyErr)
		}
		created, existing := 0, 0
		for outcome := range outcomes {
			created += len(outcome.Created)
			existing += len(outcome.Existing)
		}
		require.Equal(t, 1, created)
		require.Equal(t, 1, existing)
		rules, err := repository.ListByDatasource(ctx, "ds-1")
		require.NoError(t, err)
		matching := 0
		for _, rule := range rules {
			if rule.TableName == "customers" && rule.ColumnName == "race_column" {
				matching++
			}
		}
		require.Equal(t, 1, matching)
		requireOutboxCount(t, ctx, opened.metaDB, 3)
		require.Zero(t, auditCount(t, ctx, opened))
	})
}

package store

import (
	"context"
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestApplyDiscoveryDraftsSharedStoreRollsBackWhenAuditFails(t *testing.T) {
	opened := openTestStore(t)
	ctx := context.Background()
	_, err := opened.metaDB.ExecContext(ctx, `
CREATE TRIGGER fail_discovery_shared_audit
BEFORE INSERT ON audit_logs
WHEN NEW.details_json LIKE '%force-audit-error%'
BEGIN
  SELECT RAISE(FAIL, 'forced audit failure');
END`)
	require.NoError(t, err)
	action, actorType, actorID, datasource := "discover.apply", "admin", "admin", "ds-1"
	details := `{"marker":"force-audit-error"}`
	_, recorded, err := opened.MaskRules().ApplyDiscoveryDraftsWithAudit(ctx, datasource, []DiscoveryDraft{{ID: "rollback-draft", ColumnName: "phone", SensitiveType: "phone", Algo: "mask"}}, func(DiscoveryApplyOutcome) (model.AuditLog, error) {
		return model.AuditLog{DatasourceID: &datasource, Decision: "allow", Action: &action, ActorType: &actorType, ActorID: &actorID, DetailsJSON: &details}, nil
	})
	require.Error(t, err)
	require.Zero(t, recorded.ID)
	_, getErr := opened.MaskRules().Get(ctx, "rollback-draft")
	require.ErrorIs(t, getErr, ErrNotFound)
	page, pageErr := opened.AuditLogs().Page(ctx, 1, 10)
	require.NoError(t, pageErr)
	require.Zero(t, page.Total)
}

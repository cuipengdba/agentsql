package store

import (
	"context"
	"errors"
	"testing"

	"github.com/cuipengdba/agentsql/internal/mask"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestPlanDiscoveryDraftsUsesMaskRuntimeValidation(t *testing.T) {
	opened := openTestStore(t)
	ctx := context.Background()
	repository := opened.MaskRules()
	drafts := []DiscoveryDraft{
		{ID: "phone", ColumnName: "phone", SensitiveType: "phone", Algo: "mask"},
		{ID: "email", ColumnName: "email", SensitiveType: "email", Algo: "mask"},
		{ID: "idcard", ColumnName: "idcard", SensitiveType: "idcard", Algo: "mask"},
		{ID: "bankcard", ColumnName: "bankcard", SensitiveType: "bankcard", Algo: "mask"},
		{ID: "ip", ColumnName: "ip", SensitiveType: "ip", Algo: "mask"},
		{ID: "birthdate", ColumnName: "birthdate", SensitiveType: "birthdate", Algo: "mask"},
	}
	outcome, err := repository.planDiscoveryDrafts(ctx, opened.metaDB, "ds-1", drafts)
	require.NoError(t, err)
	require.Len(t, outcome.Created, len(drafts))
	for _, created := range outcome.Created {
		require.False(t, created.Enabled)
		require.Empty(t, created.TableName)
		require.NotNil(t, created.DatasourceID)
		require.Equal(t, "ds-1", *created.DatasourceID)
	}

	_, err = repository.planDiscoveryDrafts(ctx, opened.metaDB, "ds-1", []DiscoveryDraft{{ID: "unknown", ColumnName: "secret", SensitiveType: "future", Algo: "mask"}})
	require.ErrorIs(t, err, mask.ErrUnsupportedType)
	_, err = repository.planDiscoveryDrafts(ctx, opened.metaDB, "ds-1", []DiscoveryDraft{{ID: "hash", ColumnName: "secret", SensitiveType: "phone", Algo: "hash"}})
	require.ErrorIs(t, err, mask.ErrUnsupportedAlgorithm)
	require.ErrorIs(t, err, ErrInvalidDiscoveryDraft)
	_, err = repository.planDiscoveryDrafts(ctx, opened.metaDB, "ds-1", []DiscoveryDraft{{ID: "block", ColumnName: "secret", SensitiveType: "generic", Algo: "block"}})
	require.ErrorIs(t, err, mask.ErrUnsupportedAlgorithm)
	require.ErrorIs(t, err, ErrInvalidDiscoveryDraft)
	_, err = repository.planDiscoveryDrafts(ctx, opened.metaDB, "ds-1", []DiscoveryDraft{{ID: "range-number", ColumnName: "amount", SensitiveType: "number", Algo: "range"}})
	require.ErrorIs(t, err, mask.ErrInvalidRangeParams)
	require.ErrorIs(t, err, ErrInvalidDiscoveryDraft)
	_, err = repository.planDiscoveryDrafts(ctx, opened.metaDB, "ds-1", []DiscoveryDraft{{ID: "range-date", ColumnName: "created", SensitiveType: "date", Algo: "range"}})
	require.ErrorIs(t, err, mask.ErrUnsupportedAlgorithm)
	require.ErrorIs(t, err, ErrInvalidDiscoveryDraft)
}

func TestPlanDiscoveryDraftsUsesPhysicalScopeKey(t *testing.T) {
	opened := openTestStore(t)
	ctx := context.Background()
	repository := opened.MaskRules()
	scope := pointer("ds-1")
	_, err := repository.Create(ctx, model.MaskRule{
		ID: "existing", DatasourceID: scope, SchemaName: "sales", TableName: "customers",
		ColumnName: "phone", SensitiveType: "phone", Algo: "mask", Enabled: false,
	})
	require.NoError(t, err)

	outcome, err := repository.planDiscoveryDrafts(ctx, opened.metaDB, "ds-1", []DiscoveryDraft{
		{ID: "same", SchemaName: "sales", TableName: "customers", ColumnName: "phone", SensitiveType: "phone", Algo: "mask"},
		{ID: "other-schema", SchemaName: "Sales", TableName: "customers", ColumnName: "phone", SensitiveType: "phone", Algo: "mask"},
		{ID: "other-table", SchemaName: "sales", TableName: "accounts", ColumnName: "phone", SensitiveType: "phone", Algo: "mask"},
	})
	require.NoError(t, err)
	require.Equal(t, []string{"existing"}, []string{outcome.Existing[0].ID})
	require.Equal(t, []model.MaskRule{
		{ID: "other-schema", DatasourceID: scope, SchemaName: "Sales", TableName: "customers", ColumnName: "phone", SensitiveType: "phone", Algo: "mask", Enabled: false},
		{ID: "other-table", DatasourceID: scope, SchemaName: "sales", TableName: "accounts", ColumnName: "phone", SensitiveType: "phone", Algo: "mask", Enabled: false},
	}, outcome.Created)
	require.NoError(t, insertDiscoveryDraft(ctx, opened.metaDB, DialectSQLite, outcome.Created[1]))
	stored, err := repository.Get(ctx, "other-table")
	require.NoError(t, err)
	require.Equal(t, "sales", stored.SchemaName)
	require.Equal(t, "accounts", stored.TableName)
}

func TestApplyDiscoveryDraftsRejectsHashBeforeAuditOrRuleWrite(t *testing.T) {
	opened := openTestStore(t)
	ctx := context.Background()
	auditCalls := 0
	outcome, recorded, err := opened.MaskRules().ApplyDiscoveryDraftsWithAudit(
		ctx,
		"ds-1",
		[]DiscoveryDraft{{ID: "hash-draft", ColumnName: "secret", SensitiveType: "generic", Algo: "hash"}},
		func(DiscoveryApplyOutcome) (model.AuditLog, error) {
			auditCalls++
			return model.AuditLog{}, errors.New("must not be called")
		},
	)
	require.ErrorIs(t, err, ErrInvalidDiscoveryDraft)
	require.ErrorIs(t, err, mask.ErrUnsupportedAlgorithm)
	require.Empty(t, outcome.Created)
	require.Zero(t, recorded.ID)
	require.Zero(t, auditCalls)
	_, getErr := opened.MaskRules().Get(ctx, "hash-draft")
	require.ErrorIs(t, getErr, ErrNotFound)
	page, pageErr := opened.AuditLogs().Page(ctx, 1, 10)
	require.NoError(t, pageErr)
	require.Zero(t, page.Total)
}

func TestDiscoveryApplyRejectsBlockWithoutWriteOrAudit(t *testing.T) {
	opened := openTestStore(t)
	ctx := context.Background()
	auditCalls := 0
	outcome, recorded, err := opened.MaskRules().ApplyDiscoveryDraftsWithAudit(
		ctx,
		"ds-1",
		[]DiscoveryDraft{{ID: "block-draft", ColumnName: "secret", SensitiveType: "generic", Algo: "block"}},
		func(DiscoveryApplyOutcome) (model.AuditLog, error) {
			auditCalls++
			return model.AuditLog{}, errors.New("must not be called")
		},
	)
	require.ErrorIs(t, err, ErrInvalidDiscoveryDraft)
	require.ErrorIs(t, err, mask.ErrUnsupportedAlgorithm)
	require.Empty(t, outcome.Created)
	require.Zero(t, recorded.ID)
	require.Zero(t, auditCalls)
	_, getErr := opened.MaskRules().Get(ctx, "block-draft")
	require.ErrorIs(t, getErr, ErrNotFound)
	page, pageErr := opened.AuditLogs().Page(ctx, 1, 10)
	require.NoError(t, pageErr)
	require.Zero(t, page.Total)
}

func TestDiscoveryApplyRejectsRangeNumberAndDateWithoutWriteOrAudit(t *testing.T) {
	tests := []DiscoveryDraft{
		{ID: "range-draft", ColumnName: "phone", SensitiveType: "phone", Algo: "range"},
		{ID: "number-draft", ColumnName: "amount", SensitiveType: "number", Algo: "mask"},
		{ID: "date-draft", ColumnName: "created_at", SensitiveType: "date", Algo: "mask"},
	}
	for _, draft := range tests {
		t.Run(draft.ID, func(t *testing.T) {
			opened := openTestStore(t)
			ctx := context.Background()
			auditCalls := 0
			outcome, recorded, err := opened.MaskRules().ApplyDiscoveryDraftsWithAudit(
				ctx,
				"ds-1",
				[]DiscoveryDraft{draft},
				func(DiscoveryApplyOutcome) (model.AuditLog, error) {
					auditCalls++
					return model.AuditLog{}, errors.New("must not be called")
				},
			)
			require.ErrorIs(t, err, ErrInvalidDiscoveryDraft)
			require.Empty(t, outcome.Created)
			require.Zero(t, recorded.ID)
			require.Zero(t, auditCalls)
			_, getErr := opened.MaskRules().Get(ctx, draft.ID)
			require.ErrorIs(t, getErr, ErrNotFound)
			page, pageErr := opened.AuditLogs().Page(ctx, 1, 10)
			require.NoError(t, pageErr)
			require.Zero(t, page.Total)
		})
	}
}

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

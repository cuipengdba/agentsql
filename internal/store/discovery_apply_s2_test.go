package store

import (
	"context"
	"database/sql"
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestDiscoveryDraftRangePersistenceAndCompleteConflicts(t *testing.T) {
	forEachStore(t, func(t *testing.T, opened *Store) {
		ctx := context.Background()
		repository := opened.MaskRules()
		width10, zero := int64(10), int64(0)
		drafts := []DiscoveryDraft{
			{ID: "number-range", TableName: "orders", ColumnName: "amount", SensitiveType: "number", Algo: "range", RangeBucketWidth: &width10, RangeBucketOffset: &zero},
			{ID: "date-range", TableName: "orders", ColumnName: "created_at", SensitiveType: "date", Algo: "range"},
			{ID: "hash-draft", TableName: "customers", ColumnName: "name_hash", SensitiveType: "generic", Algo: "hash"},
			{ID: "block-draft", TableName: "customers", ColumnName: "notes", SensitiveType: "generic", Algo: "block"},
		}
		outcome, recorded, err := repository.ApplyDiscoveryDraftsWithAudit(ctx, "ds-1", drafts, testDiscoveryAudit("range-persist"))
		require.NoError(t, err)
		require.Len(t, outcome.Created, 4)
		require.Positive(t, recorded.ID)

		number, err := repository.Get(ctx, "number-range")
		require.NoError(t, err)
		require.False(t, number.Enabled)
		require.Equal(t, int64(10), *number.RangeBucketWidth)
		require.Nil(t, number.RangeBucketOffset, "canonical zero offset must persist as NULL")
		require.Nil(t, number.RangeGranularity)

		date, err := repository.Get(ctx, "date-range")
		require.NoError(t, err)
		require.False(t, date.Enabled)
		require.Nil(t, date.RangeBucketWidth)
		require.Nil(t, date.RangeBucketOffset)
		require.Equal(t, "month", *date.RangeGranularity)

		equivalent, err := repository.planDiscoveryDrafts(ctx, opened.metaDB, "ds-1", []DiscoveryDraft{
			{ID: "number-equivalent", TableName: "orders", ColumnName: "amount", SensitiveType: "number", Algo: "range", RangeBucketWidth: &width10, RangeBucketOffset: &zero},
			{ID: "date-equivalent", TableName: "orders", ColumnName: "created_at", SensitiveType: "date", Algo: "range", RangeGranularity: "month"},
		})
		require.NoError(t, err)
		require.Len(t, equivalent.Existing, 2)
		require.Empty(t, equivalent.Conflicts)

		width20 := int64(20)
		widthConflict, err := repository.planDiscoveryDrafts(ctx, opened.metaDB, "ds-1", []DiscoveryDraft{
			{ID: "number-conflict", TableName: "orders", ColumnName: "amount", SensitiveType: "number", Algo: "range", RangeBucketWidth: &width20},
		})
		require.NoError(t, err)
		require.Len(t, widthConflict.Conflicts, 1)
		require.Empty(t, widthConflict.Created)

		offset5 := int64(5)
		offsetConflict, err := repository.planDiscoveryDrafts(ctx, opened.metaDB, "ds-1", []DiscoveryDraft{
			{ID: "offset-conflict", TableName: "orders", ColumnName: "amount", SensitiveType: "number", Algo: "range", RangeBucketWidth: &width10, RangeBucketOffset: &offset5},
		})
		require.NoError(t, err)
		require.Len(t, offsetConflict.Conflicts, 1)
		require.Empty(t, offsetConflict.Created)

		year := "year"
		date.RangeGranularity = &year
		_, err = repository.Update(ctx, date)
		require.NoError(t, err)
		granularityConflict, err := repository.planDiscoveryDrafts(ctx, opened.metaDB, "ds-1", []DiscoveryDraft{
			{ID: "date-conflict", TableName: "orders", ColumnName: "created_at", SensitiveType: "date", Algo: "range", RangeGranularity: "month"},
		})
		require.NoError(t, err)
		require.Len(t, granularityConflict.Conflicts, 1)

		batchConflict, err := repository.planDiscoveryDrafts(ctx, opened.metaDB, "ds-1", []DiscoveryDraft{
			{ID: "batch-a", TableName: "ledger", ColumnName: "balance", SensitiveType: "number", Algo: "range", RangeBucketWidth: &width10},
			{ID: "batch-b", TableName: "ledger", ColumnName: "balance", SensitiveType: "number", Algo: "range", RangeBucketWidth: &width20},
		})
		require.NoError(t, err)
		require.Len(t, batchConflict.Conflicts, 1)
		require.Empty(t, batchConflict.Created)

		scope := "ds-1"
		first := model.MaskRule{ID: "constraint-a", DatasourceID: &scope, TableName: "barrier", ColumnName: "value", SensitiveType: "generic", Algo: "block"}
		second := first
		second.ID = "constraint-b"
		require.NoError(t, insertDiscoveryDraft(ctx, opened.metaDB, repository.dialect, first))
		err = insertDiscoveryDraft(ctx, opened.metaDB, repository.dialect, second)
		require.Error(t, err)
		require.True(t, IsMaskRuleConflict(err), "database unique constraint must be the final cross-process barrier")
	})
}

func TestDiscoveryApplySeparateOutboxIsAtomic(t *testing.T) {
	opened := openTestStore(t)
	opened.auditSeparate = true
	ctx := context.Background()
	repository := opened.MaskRules()

	outcome, recorded, err := repository.ApplyDiscoveryDraftsWithAudit(ctx, "ds-1", []DiscoveryDraft{
		{ID: "outbox-rule", TableName: "customers", ColumnName: "phone", SensitiveType: "phone", Algo: "mask"},
	}, testDiscoveryAudit("outbox-success"))
	require.NoError(t, err)
	require.Len(t, outcome.Created, 1)
	require.Zero(t, recorded.ID, "separate-store audit is queued, not synchronously inserted")
	requireOutboxCount(t, ctx, opened.metaDB, 1)
	var eventUUID, action string
	require.NoError(t, opened.metaDB.QueryRowContext(ctx, `SELECT event_uuid,action FROM management_audit_outbox`).Scan(&eventUUID, &action))
	_, err = uuid.Parse(eventUUID)
	require.NoError(t, err)
	require.Equal(t, "discover_apply", action)
	var auditRows int
	require.NoError(t, opened.metaDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_logs`).Scan(&auditRows))
	require.Zero(t, auditRows)

	conflict, conflictRecorded, err := repository.ApplyDiscoveryDraftsWithAudit(ctx, "ds-1", []DiscoveryDraft{
		{ID: "conflicting-rule", TableName: "customers", ColumnName: "phone", SensitiveType: "email", Algo: "mask"},
	}, func(outcome DiscoveryApplyOutcome) (model.AuditLog, error) {
		log, buildErr := testDiscoveryAudit("conflict")(outcome)
		details := `{"conflict":1}`
		log.DetailsJSON = &details
		return log, buildErr
	})
	require.NoError(t, err)
	require.Len(t, conflict.Conflicts, 1)
	require.Empty(t, conflict.Created)
	require.Zero(t, conflictRecorded.ID)
	_, getErr := repository.Get(ctx, "conflicting-rule")
	require.ErrorIs(t, getErr, ErrNotFound)
	requireOutboxCount(t, ctx, opened.metaDB, 2)
	var conflictDetails string
	require.NoError(t, opened.metaDB.QueryRowContext(ctx, `SELECT details_json FROM management_audit_outbox WHERE details_json LIKE '%conflict%'`).Scan(&conflictDetails))
	require.JSONEq(t, `{"conflict":1}`, conflictDetails)

	_, err = opened.metaDB.ExecContext(ctx, `
CREATE TRIGGER fail_discovery_outbox
BEFORE INSERT ON management_audit_outbox
WHEN NEW.details_json LIKE '%force-outbox-error%'
BEGIN
  SELECT RAISE(FAIL, 'forced outbox failure');
END`)
	require.NoError(t, err)
	_, _, err = repository.ApplyDiscoveryDraftsWithAudit(ctx, "ds-1", []DiscoveryDraft{
		{ID: "rolled-back-rule", TableName: "customers", ColumnName: "email", SensitiveType: "email", Algo: "mask"},
	}, testDiscoveryAudit("force-outbox-error"))
	require.Error(t, err)
	_, getErr = repository.Get(ctx, "rolled-back-rule")
	require.ErrorIs(t, getErr, ErrNotFound)
	requireOutboxCount(t, ctx, opened.metaDB, 2)
}

func requireOutboxCount(t *testing.T, ctx context.Context, database *sql.DB, expected int) {
	t.Helper()
	var count int
	require.NoError(t, database.QueryRowContext(ctx, `SELECT COUNT(*) FROM management_audit_outbox`).Scan(&count))
	require.Equal(t, expected, count)
}

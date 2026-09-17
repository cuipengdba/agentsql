package store

import (
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestPostgres18DemoHistoricalSeedE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("postgres:18 demo historical seed E2E is an integration test")
	}
	ctx := dockerTestContext(t)

	t.Run("combined", func(t *testing.T) {
		dsn := startPostgres18StoreContainer(t, ctx, "agentsql_demo_seed_combined", "combined-password")
		opened, err := OpenMetadata(ctx, MetadataOptions{
			Driver: DialectPostgres, PostgresDSN: dsn, AutoMigrate: true,
		}, []byte(testSecret))
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, opened.Close()) })

		audits, approvals := demoHistoricalSeedFixture()
		recorded, persisted, err := opened.SeedHistoricalAudits(ctx, audits, approvals)
		require.NoError(t, err)
		require.Len(t, recorded, 2)
		require.Len(t, persisted, 1)
		for index := range audits {
			require.True(t, audits[index].TS.Equal(recorded[index].TS))
			require.Equal(t, int64(index+1), recorded[index].ID)
		}
		require.Equal(t, recorded[1].ID, *persisted[0].AuditID)

		production, err := opened.AuditLogs().Insert(ctx, model.AuditLog{Decision: "allow"})
		require.NoError(t, err)
		require.Equal(t, int64(3), production.ID)
	})

	t.Run("separate audit-first retry", func(t *testing.T) {
		metadataDSN := startPostgres18StoreContainer(t, ctx, "agentsql_demo_seed_meta", "metadata-password")
		auditDSN := startPostgres18StoreContainer(t, ctx, "agentsql_demo_seed_audit", "audit-password")
		opened, err := OpenMetadata(ctx, MetadataOptions{
			Driver: DialectPostgres, PostgresDSN: metadataDSN, AutoMigrate: true,
			Audit: AuditOptions{
				Separate: true, Driver: DialectPostgres,
				PostgresDSN: auditDSN, AutoMigrate: true,
			},
		}, []byte(testSecret))
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, opened.Close()) })

		_, err = opened.metaDB.ExecContext(ctx, `
CREATE FUNCTION fail_demo_seed_approval() RETURNS trigger AS $$
BEGIN
  IF NEW.id = 'demo-approval-000002' THEN
    RAISE EXCEPTION 'forced demo approval failure';
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql`)
		require.NoError(t, err)
		_, err = opened.metaDB.ExecContext(ctx, `
CREATE TRIGGER fail_demo_seed_approval
BEFORE INSERT ON approvals
FOR EACH ROW EXECUTE FUNCTION fail_demo_seed_approval()`)
		require.NoError(t, err)

		audits, approvals := demoHistoricalSeedFixture()
		_, _, err = opened.SeedHistoricalAudits(ctx, audits, approvals)
		require.ErrorContains(t, err, "forced demo approval failure")
		require.Equal(t, int64(2), auditCount(t, ctx, opened))
		require.Zero(t, approvalCount(t, ctx, opened, approvals[0].ID))

		_, err = opened.metaDB.ExecContext(ctx, "DROP TRIGGER fail_demo_seed_approval ON approvals")
		require.NoError(t, err)
		recorded, persisted, err := opened.SeedHistoricalAudits(ctx, audits, approvals)
		require.NoError(t, err)
		require.Len(t, recorded, 2)
		require.Len(t, persisted, 1)
		require.Equal(t, int64(2), auditCount(t, ctx, opened))
		require.Equal(t, int64(1), approvalCount(t, ctx, opened, approvals[0].ID))
		require.Equal(t, recorded[1].ID, *persisted[0].AuditID)
		linked, err := getInsertedAuditLog(ctx, opened.auditDB, opened.auditDriver, *persisted[0].AuditID)
		require.NoError(t, err)
		require.Equal(t, *audits[1].SessionID, *linked.SessionID)
		require.True(t, audits[1].TS.Equal(linked.TS))

		production, err := opened.AuditLogs().Insert(ctx, model.AuditLog{Decision: "allow"})
		require.NoError(t, err)
		require.Equal(t, int64(3), production.ID)
	})
}

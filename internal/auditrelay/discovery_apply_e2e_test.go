package auditrelay

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	postgrescontainer "github.com/testcontainers/testcontainers-go/modules/postgres"
)

func TestDiscoveryApplySeparateStoreRelaysIdempotentlyPostgres18(t *testing.T) {
	if testing.Short() {
		t.Skip("dual postgres:18 discovery outbox relay E2E is an integration test")
	}
	testcontainers.SkipIfProviderIsNotHealthy(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	metadataDSN := startDiscoveryRelayPostgres18(t, ctx, "agentsql_discovery_relay_meta", "metadata-password")
	auditDSN := startDiscoveryRelayPostgres18(t, ctx, "agentsql_discovery_relay_audit", "audit-password")
	opened, err := store.OpenMetadata(ctx, store.MetadataOptions{
		Driver: store.DialectPostgres, PostgresDSN: metadataDSN, AutoMigrate: true,
		Audit: store.AuditOptions{Separate: true, Driver: store.DialectPostgres, PostgresDSN: auditDSN, AutoMigrate: true},
	}, []byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, opened.Close()) })

	width := int64(50)
	outcome, recorded, err := opened.MaskRules().ApplyDiscoveryDraftsWithAudit(ctx, "ds-1", []store.DiscoveryDraft{{
		ID: "relay-range", TableName: "orders", ColumnName: "amount",
		SensitiveType: "number", Algo: "range", RangeBucketWidth: &width,
	}}, func(outcome store.DiscoveryApplyOutcome) (model.AuditLog, error) {
		action, actorType, actorID, datasource := "discover_apply", "admin", "admin", "ds-1"
		details := `{"operation_id":"relay-operation","created":1,"rules":[{"table":"orders","column":"amount","sensitive_type":"number","algo":"range","range":{"bucket_width":50}}]}`
		return model.AuditLog{
			DatasourceID: &datasource, Decision: "allow", Action: &action,
			ActorType: &actorType, ActorID: &actorID, DetailsJSON: &details,
		}, nil
	})
	require.NoError(t, err)
	require.Len(t, outcome.Created, 1)
	require.Zero(t, recorded.ID, "separate apply must report queued audit, not synchronous delivery")
	stored, err := opened.MaskRules().Get(ctx, "relay-range")
	require.NoError(t, err)
	require.False(t, stored.Enabled)
	require.Equal(t, int64(50), *stored.RangeBucketWidth)

	page, err := opened.AuditLogs().Page(ctx, 1, 10)
	require.NoError(t, err)
	require.Zero(t, page.Total)

	relay, err := New(opened.Outbox(), opened.AuditLogs(), "discovery-e2e-worker", nil)
	require.NoError(t, err)
	delivered, err := relay.RunOnce(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, delivered)
	page, err = opened.AuditLogs().Page(ctx, 1, 10)
	require.NoError(t, err)
	require.EqualValues(t, 1, page.Total)
	require.Equal(t, "discover_apply", *page.List[0].Action)
	require.NotNil(t, page.List[0].EventUUID)
	require.Contains(t, *page.List[0].DetailsJSON, `"bucket_width":50`)
	done, err := opened.Outbox().Delivered(ctx, *page.List[0].EventUUID)
	require.NoError(t, err)
	require.True(t, done)

	delivered, err = relay.RunOnce(ctx)
	require.NoError(t, err)
	require.Zero(t, delivered)
	page, err = opened.AuditLogs().Page(ctx, 1, 10)
	require.NoError(t, err)
	require.EqualValues(t, 1, page.Total, "relay retry must not duplicate the event_uuid audit")
}

func startDiscoveryRelayPostgres18(t *testing.T, ctx context.Context, database, password string) string {
	t.Helper()
	container, err := postgrescontainer.Run(
		ctx,
		"postgres:18",
		postgrescontainer.WithDatabase(database),
		postgrescontainer.WithUsername("agentsql"),
		postgrescontainer.WithPassword(password),
		postgrescontainer.BasicWaitStrategies(),
	)
	if err != nil {
		if container != nil {
			testcontainers.CleanupContainer(t, container)
		}
		require.NoError(t, err)
	}
	testcontainers.CleanupContainer(t, container)
	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "5432/tcp")
	require.NoError(t, err)
	return fmt.Sprintf("postgres://agentsql:%s@%s:%s/%s?sslmode=disable", password, host, port.Port(), database)
}

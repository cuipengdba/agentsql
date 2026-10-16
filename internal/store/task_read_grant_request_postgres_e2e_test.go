package store

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestTaskReadGrantRequestPostgresSplitAuditE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("two PostgreSQL instances are required")
	}
	ctx := dockerTestContext(t)
	metadataDSN := startPostgres18StoreContainer(t, ctx, "agentsql_task_request_meta", "metadata-password")
	auditDSN := startPostgres18StoreContainer(t, ctx, "agentsql_task_request_audit", "audit-password")
	opened, err := OpenMetadata(ctx, MetadataOptions{
		Driver: DialectPostgres, PostgresDSN: metadataDSN, AutoMigrate: true,
		Audit: AuditOptions{Separate: true, Driver: DialectPostgres, PostgresDSN: auditDSN, AutoMigrate: true},
	}, []byte(testSecret))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, opened.Close()) })
	current, latest, err := MetadataMigrationVersions(ctx, opened.metaDB, DialectPostgres, true)
	require.NoError(t, err)
	require.Equal(t, 19, current)
	require.Equal(t, current, latest)
	agent, datasource := createPolicyDependencies(t, opened)
	for _, id := range []string{"pg-requester", "pg-reviewer"} {
		_, err := opened.RBAC().CreateUser(ctx, model.User{
			ID: id, TenantID: DefaultTenantID, Username: id, DisplayName: id,
			PasswordHash: "opaque-test-hash", Status: "active",
		})
		require.NoError(t, err)
	}
	sqlDigest := sha256.Sum256([]byte("SELECT id FROM public.orders LIMIT 1"))
	maxUses := int64(1)
	input := model.TaskReadGrantRequest{
		ID: "pg-request-one", TenantID: DefaultTenantID, RequesterUserID: "pg-requester",
		AgentID: agent.ID, DatasourceID: datasource.ID, SQLSHA256: hex.EncodeToString(sqlDigest[:]),
		SQLCiphertext: "encrypted-sql-test-value", IdempotencyKey: "pg-request-key-one",
		ExpiresAt: time.Now().Add(time.Hour), MaxUses: &maxUses,
	}
	repository := opened.TaskReadGrantRequests()
	created, err := repository.Create(ctx, input)
	require.NoError(t, err)
	_, err = repository.Review(ctx, created.ID, input.RequesterUserID, true)
	require.ErrorIs(t, err, ErrTaskReadGrantRequestConflict)
	_, err = repository.Review(ctx, created.ID, "pg-reviewer", true)
	require.NoError(t, err)
	_, hash, err := GenerateAPIKey()
	require.NoError(t, err)
	issued, err := repository.Issue(ctx, created.ID, input.RequesterUserID, "pg-task-grant", hash)
	require.NoError(t, err)
	require.Equal(t, "issued", issued.Status)
	grant, err := opened.TaskReadGrants().Get(ctx, "pg-task-grant")
	require.NoError(t, err)
	require.Equal(t, input.SQLSHA256, grant.SQLSHA256)
	require.NotNil(t, grant.MaxUses)
	require.EqualValues(t, 1, *grant.MaxUses)
	require.NoError(t, opened.TaskReadGrants().Consume(ctx, grant.ID, grant.TenantID, grant.AgentID, grant.DatasourceID, grant.SQLSHA256, time.Now()))
	require.ErrorIs(t, opened.TaskReadGrants().Consume(ctx, grant.ID, grant.TenantID, grant.AgentID, grant.DatasourceID, grant.SQLSHA256, time.Now()), ErrTaskReadGrantInvalid)
	consumed, err := opened.TaskReadGrants().Get(ctx, grant.ID)
	require.NoError(t, err)
	require.EqualValues(t, 1, consumed.UsedCount)
	pending, _, err := opened.Outbox().PendingStats(ctx)
	require.NoError(t, err)
	require.Equal(t, 3, pending, "request, approval, and issue must each enqueue one audit event")
	tenantCtx, err := WithTenant(ctx, DefaultTenantID)
	require.NoError(t, err)
	tenantPending, retrying, oldestAge, err := opened.Outbox().TenantPendingStats(tenantCtx)
	require.NoError(t, err)
	require.Equal(t, pending, tenantPending)
	require.Zero(t, retrying)
	require.GreaterOrEqual(t, oldestAge, time.Duration(0))
}

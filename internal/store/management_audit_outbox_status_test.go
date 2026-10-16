package store

import (
	"context"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestManagementAuditOutboxTenantPendingStats(t *testing.T) {
	opened := openTestStore(t)
	base := context.Background()
	for _, id := range []string{"outbox-a", "outbox-b"} {
		_, err := opened.RBAC().CreateTenant(base, model.Tenant{ID: id, Name: id, Status: "active"})
		require.NoError(t, err)
		ctx, err := WithTenant(base, id)
		require.NoError(t, err)
		tx, err := opened.metaDB.BeginTx(ctx, nil)
		require.NoError(t, err)
		require.NoError(t, opened.Outbox().Append(ctx, tx, model.ManagementAuditOutbox{
			EventUUID: id, Action: "test", ActorType: "user", ActorID: "tester",
			DetailsJSON: `{}`, CreatedAt: time.Now().Add(-6 * time.Minute),
		}))
		require.NoError(t, tx.Commit())
	}
	ctxA, err := WithTenant(base, "outbox-a")
	require.NoError(t, err)
	ctxB, err := WithTenant(base, "outbox-b")
	require.NoError(t, err)
	require.NoError(t, opened.Outbox().MarkFailed(base, "outbox-a", "safe retry", time.Minute))
	count, retrying, age, err := opened.Outbox().TenantPendingStats(ctxA)
	require.NoError(t, err)
	require.Equal(t, 1, count)
	require.Equal(t, 1, retrying)
	require.GreaterOrEqual(t, age, 5*time.Minute)
	count, retrying, age, err = opened.Outbox().TenantPendingStats(ctxB)
	require.NoError(t, err)
	require.Equal(t, 1, count)
	require.Zero(t, retrying)
	require.GreaterOrEqual(t, age, 5*time.Minute)
	require.NoError(t, opened.Outbox().MarkDelivered(base, "outbox-a"))
	count, retrying, age, err = opened.Outbox().TenantPendingStats(ctxA)
	require.NoError(t, err)
	require.Zero(t, count)
	require.Zero(t, retrying)
	require.Zero(t, age)
}

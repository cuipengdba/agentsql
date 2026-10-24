package mcpserver

import (
	"context"
	"strings"
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestB5RequestIdentityBindsHumanAgentAndTenant(t *testing.T) {
	legacyPrincipal, legacyTenant := b5RequestIdentity(context.Background(), "agent-1")
	require.Equal(t, "agent-1", legacyPrincipal)
	require.Equal(t, "agent-1", legacyTenant)
	ctx := context.WithValue(context.Background(), delegatedHumanContextKey{}, delegatedHuman{userID: "user-1", tenantID: "tenant-1"})
	principal, tenant := b5RequestIdentity(ctx, "agent-1")
	require.Equal(t, "tenant-1", tenant)
	require.NotEqual(t, legacyPrincipal, principal)
	require.True(t, strings.HasPrefix(principal, "delegated:user-1:"))
	repeated, _ := b5RequestIdentity(ctx, "agent-1")
	require.Equal(t, principal, repeated)
	for _, changed := range []struct {
		ctx   context.Context
		agent string
	}{
		{context.WithValue(ctx, delegatedHumanContextKey{}, delegatedHuman{userID: "user-2", tenantID: "tenant-1"}), "agent-1"},
		{context.WithValue(ctx, delegatedHumanContextKey{}, delegatedHuman{userID: "user-1", tenantID: "tenant-2"}), "agent-1"},
		{ctx, "agent-2"},
	} {
		other, _ := b5RequestIdentity(changed.ctx, changed.agent)
		require.NotEqual(t, principal, other)
	}
}

func TestB5RequestIdentityUsesAuthenticatedAgentTenant(t *testing.T) {
	ctx := context.WithValue(context.Background(), requestIdentityContextKey{}, requestIdentity{
		agent: model.Agent{ID: "agent-1", TenantID: "tenant-1"},
	})
	principal, tenant := b5RequestIdentity(ctx, "agent-1")
	require.Equal(t, "agent-1", principal)
	require.Equal(t, "tenant-1", tenant)

	// A mismatched bound identity must never choose another Agent's tenant.
	_, tenant = b5RequestIdentity(ctx, "agent-2")
	require.Equal(t, "agent-2", tenant)
}

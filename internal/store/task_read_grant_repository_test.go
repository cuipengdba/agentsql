package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestTaskReadGrantTenantScopeAndImmediateRevocation(t *testing.T) {
	opened := openTestStore(t)
	ctx := context.Background()
	agent, datasource := createPolicyDependencies(t, opened)
	_, err := opened.metaDB.ExecContext(ctx, `
INSERT INTO users (id, tenant_id, username, display_name, password_hash, status)
VALUES ('issuer-task', 'tenant_default', 'issuer-task', 'Issuer', 'test-hash', 'active')`)
	require.NoError(t, err)

	plain, hash, err := GenerateAPIKey()
	require.NoError(t, err)
	sqlText := "SELECT id FROM public.orders LIMIT 1"
	digest := sha256.Sum256([]byte(sqlText))
	grant, err := opened.TaskReadGrants().Create(ctx, model.TaskReadGrant{
		ID: "task-one", TenantID: DefaultTenantID, IssuerUserID: "issuer-task",
		AgentID: agent.ID, DatasourceID: datasource.ID,
		SQLSHA256: hex.EncodeToString(digest[:]), KeyHash: hash,
		ExpiresAt: time.Now().Add(time.Hour),
	})
	require.NoError(t, err)
	require.Empty(t, grant.RevokedAt)
	require.Equal(t, hash, grant.KeyHash)

	temporaryAgent, err := opened.Agents().GetByAPIKeyHash(ctx, HashAPIKey(plain))
	require.NoError(t, err)
	require.Equal(t, agent.ID, temporaryAgent.ID)
	require.Equal(t, grant.ID, temporaryAgent.TaskGrantID)
	require.Equal(t, "issuer-task", temporaryAgent.TaskGrantIssuerUserID)
	require.Equal(t, "readonly", temporaryAgent.Level)
	require.Equal(t, hash, temporaryAgent.APIKeyHash)

	check := func(tenant, agentID, datasourceID, sqlHash string, now time.Time) error {
		_, err := opened.TaskReadGrants().Validate(ctx, grant.ID, tenant, agentID, datasourceID, sqlHash, now)
		return err
	}
	require.NoError(t, check(DefaultTenantID, agent.ID, datasource.ID, grant.SQLSHA256, time.Now()))
	require.ErrorIs(t, check("other-tenant", agent.ID, datasource.ID, grant.SQLSHA256, time.Now()), ErrTaskReadGrantInvalid)
	require.ErrorIs(t, check(DefaultTenantID, "other-agent", datasource.ID, grant.SQLSHA256, time.Now()), ErrTaskReadGrantInvalid)
	require.ErrorIs(t, check(DefaultTenantID, agent.ID, "other-datasource", grant.SQLSHA256, time.Now()), ErrTaskReadGrantInvalid)
	otherDigest := sha256.Sum256([]byte(sqlText + " "))
	require.ErrorIs(t, check(DefaultTenantID, agent.ID, datasource.ID, hex.EncodeToString(otherDigest[:]), time.Now()), ErrTaskReadGrantInvalid)
	require.ErrorIs(t, check(DefaultTenantID, agent.ID, datasource.ID, grant.SQLSHA256, grant.ExpiresAt), ErrTaskReadGrantInvalid)
	for _, subject := range []struct {
		table string
		id    string
	}{
		{table: "users", id: "issuer-task"},
		{table: "agents", id: agent.ID},
		{table: "tenants", id: DefaultTenantID},
	} {
		_, err = opened.metaDB.ExecContext(ctx, "UPDATE "+subject.table+" SET status='disabled' WHERE id=?", subject.id)
		require.NoError(t, err)
		require.ErrorIs(t, check(DefaultTenantID, agent.ID, datasource.ID, grant.SQLSHA256, time.Now()), ErrTaskReadGrantInvalid, subject.table)
		_, err = opened.metaDB.ExecContext(ctx, "UPDATE "+subject.table+" SET status='active' WHERE id=?", subject.id)
		require.NoError(t, err)
		require.NoError(t, check(DefaultTenantID, agent.ID, datasource.ID, grant.SQLSHA256, time.Now()), subject.table)
	}

	otherCtx, err := WithTenant(ctx, "other-tenant")
	require.NoError(t, err)
	_, err = opened.TaskReadGrants().Get(otherCtx, grant.ID)
	require.ErrorIs(t, err, ErrNotFound)
	_, err = opened.TaskReadGrants().Create(otherCtx, model.TaskReadGrant{
		ID: "cross-tenant", TenantID: "other-tenant", IssuerUserID: "issuer-task",
		AgentID: agent.ID, DatasourceID: datasource.ID, SQLSHA256: grant.SQLSHA256,
		KeyHash: HashAPIKey("asql_other"), ExpiresAt: time.Now().Add(time.Hour),
	})
	require.ErrorIs(t, err, ErrTaskReadGrantInvalid)

	revoked, err := opened.TaskReadGrants().Revoke(ctx, grant.ID)
	require.NoError(t, err)
	require.NotNil(t, revoked.RevokedAt)
	require.ErrorIs(t, check(DefaultTenantID, agent.ID, datasource.ID, grant.SQLSHA256, time.Now()), ErrTaskReadGrantInvalid)
	_, err = opened.Agents().GetByAPIKeyHash(ctx, hash)
	require.ErrorIs(t, err, ErrAgentNotFound)
}

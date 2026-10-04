package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/b5"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/notify"
	"github.com/stretchr/testify/require"
)

func TestBusinessRepositoriesIsolateTenantRows(t *testing.T) {
	opened := openTestStore(t)
	base := context.Background()
	_, err := opened.RBAC().CreateTenant(base, model.Tenant{ID: "tenant_a", Name: "Tenant A", Status: "active"})
	require.NoError(t, err)
	_, err = opened.RBAC().CreateTenant(base, model.Tenant{ID: "tenant_b", Name: "Tenant B", Status: "active"})
	require.NoError(t, err)
	tenantA, err := WithTenant(base, "tenant_a")
	require.NoError(t, err)
	tenantB, err := WithTenant(base, "tenant_b")
	require.NoError(t, err)

	_, keyHash, err := GenerateAPIKey()
	require.NoError(t, err)
	agent, err := opened.Agents().Create(tenantA, model.Agent{
		ID: "tenant-agent", Name: "A", Status: "active", APIKeyHash: keyHash, Level: "readonly",
	})
	require.NoError(t, err)
	require.Equal(t, "tenant_a", agent.TenantID)
	require.Empty(t, mustAgents(t, opened, tenantB))
	_, err = opened.Agents().Get(tenantB, agent.ID)
	require.ErrorIs(t, err, ErrNotFound)
	agent.Name = "cross-tenant"
	_, err = opened.Agents().Update(tenantB, agent)
	require.ErrorIs(t, err, ErrNotFound)
	require.ErrorIs(t, opened.Agents().Delete(tenantB, agent.ID), ErrNotFound)

	datasource, err := opened.Datasources().Create(tenantA, model.Datasource{
		ID: "tenant-ds", Name: "A", DBType: "postgres", Host: "localhost", Port: 5432,
		Database: "a", Username: "a", ConnLimit: 1, StmtTimeoutMS: 1000, RowLimit: 10,
	}, "secret")
	require.NoError(t, err)
	require.Equal(t, "tenant_a", datasource.TenantID)
	require.Empty(t, mustDatasources(t, opened, tenantB))
	_, err = opened.Datasources().Get(tenantB, datasource.ID)
	require.ErrorIs(t, err, ErrNotFound)

	rule, err := opened.Rules().Create(tenantA, model.Rule{
		ID: "tenant-rule", DBType: "postgres", Title: "A", RiskLevel: 1,
		PatternType: "regex", Definition: "select", Enabled: true,
	})
	require.NoError(t, err)
	require.Equal(t, "tenant_a", rule.TenantID)
	rules, err := opened.Rules().List(tenantB, "")
	require.NoError(t, err)
	require.Empty(t, rules)
	_, err = opened.Rules().Get(tenantB, rule.ID)
	require.ErrorIs(t, err, ErrNotFound)

	mask, err := opened.MaskRules().Create(tenantA, model.MaskRule{
		ID: "tenant-mask", TableName: "customers", ColumnName: "email",
		SensitiveType: "email", Algo: "mask", Enabled: true,
	})
	require.NoError(t, err)
	require.Equal(t, "tenant_a", mask.TenantID)
	masks, err := opened.MaskRules().List(tenantB)
	require.NoError(t, err)
	require.Empty(t, masks)
	_, err = opened.MaskRules().Get(tenantB, mask.ID)
	require.True(t, errors.Is(err, ErrNotFound))

	policy, err := opened.Policies().Create(tenantA, model.Policy{
		ID: "tenant-policy", AgentID: agent.ID, DatasourceID: datasource.ID,
		ObjectType: "table", ObjectName: "public.orders", Action: "allow",
	})
	require.NoError(t, err)
	require.Equal(t, "tenant_a", policy.TenantID)
	policies, err := opened.Policies().List(tenantB)
	require.NoError(t, err)
	require.Empty(t, policies)
	_, err = opened.Policies().Get(tenantB, policy.ID)
	require.ErrorIs(t, err, ErrNotFound)
	policy.Action = "deny"
	_, err = opened.Policies().Update(tenantB, policy)
	require.ErrorIs(t, err, ErrNotFound)
	require.ErrorIs(t, opened.Policies().Delete(tenantB, policy.ID), ErrNotFound)

	approval, err := opened.Approvals().Create(tenantA, model.Approval{ID: "tenant-approval", Status: "pending"})
	require.NoError(t, err)
	require.Equal(t, "tenant_a", approval.TenantID)
	approvalPage, err := opened.Approvals().ListPage(tenantB, "", 1, 10)
	require.NoError(t, err)
	require.Empty(t, approvalPage.List)
	_, err = opened.Approvals().Get(tenantB, approval.ID)
	require.ErrorIs(t, err, ErrNotFound)
	approval.Status = "expired"
	_, err = opened.Approvals().Update(tenantB, approval)
	require.ErrorIs(t, err, ErrNotFound)
	require.ErrorIs(t, opened.Approvals().Delete(tenantB, approval.ID), ErrNotFound)

	auditLog, err := opened.AuditLogs().Insert(tenantA, model.AuditLog{Decision: "allow"})
	require.NoError(t, err)
	require.Equal(t, "tenant_a", auditLog.TenantID)
	auditPage, err := opened.AuditLogs().Page(tenantB, 1, 10)
	require.NoError(t, err)
	require.Empty(t, auditPage.List)

	require.NoError(t, opened.RedactionKeys().RegisterStandby(
		tenantA, "36", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "tenant-a", "r1", nil,
	))
	keys, err := opened.RedactionKeys().List(tenantB)
	require.NoError(t, err)
	require.Empty(t, keys)
	_, err = opened.RedactionKeys().Get(tenantB, "36")
	require.ErrorIs(t, err, ErrNotFound)

	require.NoError(t, opened.Notifications().Replace(tenantA, notify.Config{Enabled: true, QueueSize: 8}))
	notifications, err := opened.Notifications().Get(tenantB)
	require.NoError(t, err)
	require.Equal(t, notify.Config{}, notifications)

	now := time.Now().UTC()
	session, err := opened.B5Sessions().Create(tenantA, B5Session{
		SessionID: "tenant-session", AgentID: agent.ID, PrincipalID: "principal-a",
		OwnerInstanceID: "instance-a", OwnerEpoch: 1, ContinuationSchemaID: "agentsql.b5.continuation.v2",
		ContinuationSchemaVersion: 2, ContinuationKeyCiphertext: "ciphertext", ContinuationHMACDigest: make([]byte, 32),
		StickyRoute: "instance-a", Status: b5.SessionReady, IdleExpiresAt: now.Add(time.Minute), AbsoluteExpiresAt: now.Add(time.Hour),
	})
	require.NoError(t, err)
	_, err = opened.B5Sessions().Get(tenantB, session.SessionID)
	require.ErrorIs(t, err, ErrNotFound)
	sessionPage, err := opened.B5Sessions().ListPage(tenantB, B5SessionFilter{}, 1, 10)
	require.NoError(t, err)
	require.Empty(t, sessionPage.List)
	_, err = opened.B5Sessions().CASStatus(tenantB, session.SessionID, session.Revision, b5.SessionReady, b5.SessionActive, now.Add(30*time.Second))
	require.ErrorIs(t, err, ErrB5CASConflict)

	_, err = opened.metaDB.ExecContext(tenantA, `INSERT INTO chain_state(tenant_id,chain_id,status) VALUES(?,?,?)`, "tenant_a", "management", "DISABLED")
	require.NoError(t, err)
	managementChain, err := opened.Chain("management")
	require.NoError(t, err)
	_, err = managementChain.State().Get(tenantB, "management")
	require.ErrorIs(t, err, ErrNotFound)

	dashboard, err := opened.Dashboard().Summary(tenantB, 1)
	require.NoError(t, err)
	require.Zero(t, dashboard.KPI.ActiveAgents)
	require.Zero(t, dashboard.KPI.DatasourcesTotal)
	require.Zero(t, dashboard.KPI.TotalRequests)
}

func mustAgents(t *testing.T, opened *Store, ctx context.Context) []model.Agent {
	t.Helper()
	items, err := opened.Agents().List(ctx)
	require.NoError(t, err)
	return items
}

func mustDatasources(t *testing.T, opened *Store, ctx context.Context) []model.Datasource {
	t.Helper()
	items, err := opened.Datasources().List(ctx)
	require.NoError(t, err)
	return items
}

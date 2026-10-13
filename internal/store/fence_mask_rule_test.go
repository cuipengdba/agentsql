package store

import (
	"context"
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestControlSnapshotMaskRulesMatchScannerAndTenant(t *testing.T) {
	opened := openTestStore(t)
	base := context.Background()
	for _, tenant := range []model.Tenant{
		{ID: "mask_tenant_a", Name: "Mask tenant A", Status: "active"},
		{ID: "mask_tenant_b", Name: "Mask tenant B", Status: "active"},
	} {
		_, err := opened.RBAC().CreateTenant(base, tenant)
		require.NoError(t, err)
	}
	tenantA, err := WithTenant(base, "mask_tenant_a")
	require.NoError(t, err)
	tenantB, err := WithTenant(base, "mask_tenant_b")
	require.NoError(t, err)
	for _, item := range []struct {
		ctx  context.Context
		rule model.MaskRule
	}{
		{tenantA, model.MaskRule{ID: "mask-a", TableName: "customers", ColumnName: "email", SensitiveType: "email", Algo: "mask", Enabled: true}},
		{tenantA, model.MaskRule{ID: "mask-disabled", TableName: "customers", ColumnName: "phone", SensitiveType: "phone", Algo: "mask", Enabled: false}},
		{tenantB, model.MaskRule{ID: "mask-b", TableName: "customers", ColumnName: "name", SensitiveType: "name", Algo: "mask", Enabled: true}},
	} {
		_, err := opened.MaskRules().Create(item.ctx, item.rule)
		require.NoError(t, err)
	}

	tx, err := opened.metaDB.BeginTx(base, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback() })
	snapshot := &ControlSnapshot{tx: tx, dialect: DialectSQLite}
	for _, item := range []struct {
		ctx        context.Context
		wantID     string
		wantTenant string
	}{
		{tenantA, "mask-a", "mask_tenant_a"},
		{tenantB, "mask-b", "mask_tenant_b"},
	} {
		rules, err := snapshot.listEnabledMaskRules(item.ctx, "shared-datasource")
		require.NoError(t, err)
		require.Len(t, rules, 1)
		require.Equal(t, item.wantID, rules[0].ID)
		require.Equal(t, item.wantTenant, rules[0].TenantID)
	}
}

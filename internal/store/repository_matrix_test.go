package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestOrdinaryRepositoriesDialectMatrix(t *testing.T) {
	forEachStore(t, func(t *testing.T, opened *Store) {
		ctx := context.Background()
		_, hash, err := GenerateAPIKey()
		require.NoError(t, err)
		expiresAt := time.Date(
			2026, time.September, 17, 20, 30, 0, 0,
			time.FixedZone("UTC+8", 8*60*60),
		)
		agent, err := opened.Agents().Create(ctx, model.Agent{
			ID: "ag_matrix", Name: "Matrix Agent", Owner: pointer("DBA"),
			Status: "active", APIKeyHash: hash, Level: "readonly", ExpiresAt: &expiresAt,
		})
		require.NoError(t, err)
		require.Equal(t, time.UTC, agent.CreatedAt.Location())
		require.Equal(t, time.UTC, agent.UpdatedAt.Location())
		require.NotNil(t, agent.ExpiresAt)
		require.Equal(t, expiresAt.UTC().Truncate(time.Second), agent.ExpiresAt.UTC().Truncate(time.Second))
		byKey, err := opened.Agents().GetByAPIKeyHash(ctx, hash)
		require.NoError(t, err)
		require.Equal(t, agent.ID, byKey.ID)
		agents, err := opened.Agents().List(ctx)
		require.NoError(t, err)
		require.Len(t, agents, 1)
		agent.Name = "Updated Matrix Agent"
		agent.Status = "disabled"
		agent, err = opened.Agents().Update(ctx, agent)
		require.NoError(t, err)
		require.Equal(t, "disabled", agent.Status)

		datasource, err := opened.Datasources().Create(ctx, model.Datasource{
			ID: "ds_matrix", Name: "Matrix DB", DBType: "postgres", Host: "127.0.0.1",
			Port: 5432, Database: "app", Username: "gateway", ConnLimit: 17,
			StmtTimeoutMS: 12_345, RowLimit: 54_321,
		}, "matrix-password")
		require.NoError(t, err)
		require.Equal(t, 17, datasource.ConnLimit)
		require.Equal(t, 12_345, datasource.StmtTimeoutMS)
		require.Equal(t, 54_321, datasource.RowLimit)
		datasource.Port = 3306
		datasource.ConnLimit = 23
		datasource, err = opened.Datasources().Update(ctx, datasource, "updated-password")
		require.NoError(t, err)
		require.Equal(t, 23, datasource.ConnLimit)
		datasources, err := opened.Datasources().List(ctx)
		require.NoError(t, err)
		require.Len(t, datasources, 1)

		policy, err := opened.Policies().Create(ctx, model.Policy{
			ID: "policy_matrix", AgentID: agent.ID, DatasourceID: datasource.ID,
			ObjectType: "table", ObjectName: "public.orders", Columns: pointer("id,total"),
			RowFilter: pointer("tenant_id = 1"), Action: "allow",
		})
		require.NoError(t, err)
		matching, err := opened.Policies().ListByAgentAndDatasource(ctx, agent.ID, datasource.ID)
		require.NoError(t, err)
		require.Len(t, matching, 1)
		byAgent, err := opened.Policies().ListByAgent(ctx, agent.ID)
		require.NoError(t, err)
		require.Len(t, byAgent, 1)
		allPolicies, err := opened.Policies().List(ctx)
		require.NoError(t, err)
		require.Len(t, allPolicies, 1)
		policy.Action = "deny"
		policy.Columns = nil
		policy, err = opened.Policies().Update(ctx, policy)
		require.NoError(t, err)
		require.Equal(t, "deny", policy.Action)
		require.Nil(t, policy.Columns)

		rule, err := opened.Rules().Create(ctx, model.Rule{
			ID: "R_MATRIX", DBType: "all", Title: "Matrix rule", RiskLevel: 2,
			PatternType: "ast_match", Definition: `{}`, Enabled: true, Builtin: false,
		})
		require.NoError(t, err)
		require.True(t, rule.Enabled)
		require.False(t, rule.Builtin)
		rule.Enabled = false
		rule.Builtin = true
		rule, err = opened.Rules().Update(ctx, rule)
		require.NoError(t, err)
		require.False(t, rule.Enabled)
		require.True(t, rule.Builtin)
		rules, err := opened.Rules().List(ctx, "all")
		require.NoError(t, err)
		require.Len(t, rules, 1)
		execStoreSQL(t, opened, `
INSERT INTO rules (id, db_type, title, risk_level, pattern_type, definition)
VALUES (?, ?, ?, ?, ?, ?)`, "R_DEFAULT", "postgres", "Defaults", 3, "ast_match", `{}`)
		defaultRule, err := opened.Rules().Get(ctx, "R_DEFAULT")
		require.NoError(t, err)
		require.True(t, defaultRule.Enabled)
		require.False(t, defaultRule.Builtin)

		for _, maskRule := range []model.MaskRule{
			{ID: "mask_specific", DatasourceID: pointer(datasource.ID), TableName: "customers", ColumnName: "phone", SensitiveType: "phone", Algo: "mask"},
			{ID: "mask_global_null", TableName: "customers", ColumnName: "email", SensitiveType: "email", Algo: "mask"},
			{ID: "mask_global_empty", DatasourceID: pointer(""), TableName: "customers", ColumnName: "card", SensitiveType: "bankcard", Algo: "mask"},
			{ID: "mask_other", DatasourceID: pointer("ds_other"), TableName: "customers", ColumnName: "secret", SensitiveType: "block", Algo: "block"},
		} {
			_, err := opened.MaskRules().Create(ctx, maskRule)
			require.NoError(t, err)
		}
		effective, err := opened.MaskRules().ListByDatasource(ctx, datasource.ID)
		require.NoError(t, err)
		require.Len(t, effective, 3)
		maskSpecific, err := opened.MaskRules().Get(ctx, "mask_specific")
		require.NoError(t, err)
		maskSpecific.ColumnName = "mobile"
		maskSpecific, err = opened.MaskRules().Update(ctx, maskSpecific)
		require.NoError(t, err)
		require.Equal(t, "mobile", maskSpecific.ColumnName)
		allMasks, err := opened.MaskRules().List(ctx)
		require.NoError(t, err)
		require.Len(t, allMasks, 4)

		require.NoError(t, opened.MaskRules().Delete(ctx, "mask_specific"))
		require.NoError(t, opened.MaskRules().Delete(ctx, "mask_global_null"))
		require.NoError(t, opened.MaskRules().Delete(ctx, "mask_global_empty"))
		require.NoError(t, opened.MaskRules().Delete(ctx, "mask_other"))
		require.NoError(t, opened.Rules().Delete(ctx, "R_MATRIX"))
		require.NoError(t, opened.Rules().Delete(ctx, "R_DEFAULT"))
		require.NoError(t, opened.Policies().Delete(ctx, policy.ID))
		require.NoError(t, opened.Datasources().Delete(ctx, datasource.ID))
		require.NoError(t, opened.Agents().Delete(ctx, agent.ID))
		_, err = opened.Agents().Get(ctx, agent.ID)
		require.True(t, errors.Is(err, ErrNotFound))
	})
}

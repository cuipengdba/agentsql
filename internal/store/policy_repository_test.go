package store

import (
	"context"
	"strings"
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestPolicyRepositoryListByAgentAndDatasource(t *testing.T) {
	opened := openTestStore(t)
	agent, datasource := createPolicyDependencies(t, opened)
	_, apiKeyHash, err := GenerateAPIKey()
	require.NoError(t, err)
	_, err = opened.Agents().Create(context.Background(), model.Agent{
		ID:         "ag_policy_other",
		Name:       "Other Policy Agent",
		Status:     "active",
		APIKeyHash: apiKeyHash,
		Level:      "readonly",
	})
	require.NoError(t, err)
	_, err = opened.Datasources().Create(context.Background(), model.Datasource{
		ID:            "ds_policy_other",
		Name:          "Other Policy Database",
		DBType:        "postgres",
		Host:          "127.0.0.1",
		Port:          5432,
		Database:      "other",
		Username:      "agentsql",
		ConnLimit:     5,
		StmtTimeoutMS: 5000,
		RowLimit:      1000,
	}, "database-password")
	require.NoError(t, err)

	insertPolicy := func(
		id string,
		agentID string,
		datasourceID string,
		createdAt string,
		columns any,
		rowFilter any,
	) {
		t.Helper()
		_, err := opened.metaDB.ExecContext(context.Background(), `
INSERT INTO policies (
  id, agent_id, datasource_id, object_type, object_name, columns, row_filter,
  action, created_at, updated_at
)
VALUES (?, ?, ?, 'table', ?, ?, ?, 'allow', ?, ?)`,
			id,
			agentID,
			datasourceID,
			"public."+id,
			columns,
			rowFilter,
			createdAt,
			createdAt,
		)
		require.NoError(t, err)
	}

	insertPolicy("policy_z", agent.ID, datasource.ID, "2026-01-01 00:00:01", "id,total", "tenant_id = 1")
	insertPolicy("policy_b", agent.ID, datasource.ID, "2026-01-01 00:00:02", nil, nil)
	insertPolicy("policy_a", agent.ID, datasource.ID, "2026-01-01 00:00:02", nil, nil)
	insertPolicy("policy_other_agent", "ag_policy_other", datasource.ID, "2026-01-01 00:00:00", nil, nil)
	insertPolicy("policy_other_ds", agent.ID, "ds_policy_other", "2026-01-01 00:00:00", nil, nil)

	policies, err := opened.Policies().ListByAgentAndDatasource(
		context.Background(),
		agent.ID,
		datasource.ID,
	)
	require.NoError(t, err)
	require.Equal(t, []string{"policy_z", "policy_a", "policy_b"}, policyIDs(policies))
	require.Equal(t, "id,total", *policies[0].Columns)
	require.Equal(t, "tenant_id = 1", *policies[0].RowFilter)

	empty, err := opened.Policies().ListByAgentAndDatasource(
		context.Background(),
		"ag_missing",
		datasource.ID,
	)
	require.NoError(t, err)
	require.NotNil(t, empty)
	require.Empty(t, empty)

	planRows, err := opened.metaDB.QueryContext(context.Background(), `
EXPLAIN QUERY PLAN
SELECT id
FROM policies INDEXED BY idx_policies_agent_ds
WHERE agent_id = ? AND datasource_id = ?
ORDER BY created_at ASC, id ASC`, agent.ID, datasource.ID)
	require.NoError(t, err)
	foundIndex := false
	for planRows.Next() {
		var id, parent, unused int
		var detail string
		require.NoError(t, planRows.Scan(&id, &parent, &unused, &detail))
		if strings.Contains(detail, "idx_policies_agent_ds") {
			foundIndex = true
		}
	}
	require.NoError(t, planRows.Err())
	require.NoError(t, planRows.Close())
	require.True(t, foundIndex)
}

func policyIDs(policies []model.Policy) []string {
	ids := make([]string, 0, len(policies))
	for _, policy := range policies {
		ids = append(ids, policy.ID)
	}
	return ids
}

package store

import (
	"context"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestT16AgentRulePolicyApprovalListMethods(t *testing.T) {
	opened := openTestStore(t)
	for _, id := range []string{"agent-b", "agent-a"} {
		_, hash, err := GenerateAPIKey()
		require.NoError(t, err)
		_, err = opened.Agents().Create(context.Background(), model.Agent{ID: id, Name: id, Status: "active", APIKeyHash: hash, Level: "dml"})
		require.NoError(t, err)
	}
	agents, err := opened.Agents().List(context.Background())
	require.NoError(t, err)
	require.Len(t, agents, 2)
	require.Equal(t, "agent-a", agents[0].ID)

	for _, id := range []string{"R002", "R001"} {
		_, err = opened.Rules().Create(context.Background(), model.Rule{ID: id, DBType: "all", Title: id, RiskLevel: 1, PatternType: "ast", Definition: "{}", Enabled: true})
		require.NoError(t, err)
	}
	rules, err := opened.Rules().List(context.Background(), "")
	require.NoError(t, err)
	require.Equal(t, []string{"R001", "R002"}, []string{rules[0].ID, rules[1].ID})
	rules, err = opened.Rules().List(context.Background(), "postgres")
	require.NoError(t, err)
	require.NotNil(t, rules)
	require.Empty(t, rules)

	_, err = opened.Datasources().Create(context.Background(), model.Datasource{ID: "ds-1", Name: "DB", DBType: "postgres", Host: "db", Port: 5432, Database: "app", Username: "u", ConnLimit: 5, StmtTimeoutMS: 5000, RowLimit: 1000}, "password")
	require.NoError(t, err)
	_, err = opened.Policies().Create(context.Background(), model.Policy{ID: "p1", AgentID: "agent-a", DatasourceID: "ds-1", ObjectType: "table", ObjectName: "public.orders", Action: "allow"})
	require.NoError(t, err)
	policies, err := opened.Policies().List(context.Background())
	require.NoError(t, err)
	require.Len(t, policies, 1)
	_, err = opened.Approvals().Create(context.Background(), model.Approval{ID: "approval", AgentID: pointer("agent-a"), Status: "pending"})
	require.NoError(t, err)
	page, err := opened.Approvals().ListPage(context.Background(), "pending", 1, 20)
	require.NoError(t, err)
	require.Equal(t, int64(1), page.Total)
	require.Equal(t, "approval", page.List[0].ID)
	_, err = opened.Approvals().ListPage(nil, "", 1, 20)
	require.ErrorIs(t, err, ErrNilContext)
}

func TestDashboardSummaryHasFixedTrendAndFourStateCounts(t *testing.T) {
	opened := openTestStore(t)
	now := time.Now().UTC()
	for index, decision := range []string{"allow", "deny", "warn", "approve", "deny"} {
		timestamp := now.Add(-time.Duration(index) * 24 * time.Hour)
		_, err := opened.metaDB.ExecContext(context.Background(), `
INSERT INTO audit_logs (ts, agent_id, decision, rule_hits, est_rows)
VALUES (?, ?, ?, ?, ?)`, timestamp, "agent-a", decision, "[]", index+1)
		require.NoError(t, err)
	}
	_, err := opened.metaDB.ExecContext(context.Background(), `INSERT INTO agents (id,name,status,api_key_hash,level) VALUES (?,?,?,?,?)`, "agent-a", "Agent A", "active", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "dml")
	require.NoError(t, err)
	_, err = opened.metaDB.ExecContext(context.Background(), `INSERT INTO datasources (id,name,db_type,host,port,database,username,password_enc) VALUES (?,?,?,?,?,?,?,?)`, "ds-a", "DB", "postgres", "db", 5432, "app", "u", "cipher")
	require.NoError(t, err)
	summary, err := opened.Dashboard().Summary(context.Background(), 14)
	require.NoError(t, err)
	require.Equal(t, int64(5), summary.KPI.TotalRequests)
	require.Equal(t, int64(2), summary.KPI.Blocked)
	require.Equal(t, int64(1), summary.KPI.ActiveAgents)
	require.Equal(t, int64(1), summary.KPI.DatasourcesTotal)
	require.Len(t, summary.Trend14D, 14)
	var distributionTotal int64
	for _, item := range summary.DecisionDistribution {
		distributionTotal += item.Count
	}
	require.Equal(t, summary.KPI.TotalRequests, distributionTotal)
	require.Equal(t, int64(2), summary.BattleReport.BlockedCount)
	require.Equal(t, int64(7), summary.BattleReport.EstRowsSaved)
	for index := 1; index < len(summary.Trend14D); index++ {
		require.False(t, summary.Trend14D[index].Date == "")
	}
	_, err = opened.Dashboard().Summary(context.Background(), 0)
	require.Error(t, err)
}

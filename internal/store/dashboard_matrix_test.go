package store

import (
	"context"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestDashboardDialectMatrix(t *testing.T) {
	forEachStore(t, func(t *testing.T, opened *Store) {
		ctx := context.Background()
		_, hash, err := GenerateAPIKey()
		require.NoError(t, err)
		_, err = opened.Agents().Create(ctx, model.Agent{
			ID: "agent-known", Name: "Known Agent", Status: "active", APIKeyHash: hash, Level: "readonly",
		})
		require.NoError(t, err)
		_, err = opened.Datasources().Create(ctx, model.Datasource{
			ID: "dashboard-ds", Name: "Dashboard DB", DBType: "postgres", Host: "127.0.0.1",
			Port: 5432, Database: "app", Username: "gateway", ConnLimit: 5,
			StmtTimeoutMS: 5000, RowLimit: 1000,
		}, "password")
		require.NoError(t, err)
		_, err = opened.Approvals().Create(ctx, model.Approval{ID: "dashboard-pending", Status: "pending"})
		require.NoError(t, err)

		insert := func(timestamp time.Time, agentID, decision, hits string, estRows int64) {
			t.Helper()
			execStoreSQL(t, opened, `
INSERT INTO audit_logs (ts, agent_id, decision, rule_hits, est_rows)
VALUES (?, ?, ?, ?, ?)`, timestamp, agentID, decision, hits, estRows)
		}
		insert(time.Date(2026, time.December, 26, 0, 0, 0, 0, time.UTC), "agent-known", "allow", `[]`, 1)
		insert(time.Date(2026, time.December, 29, 23, 59, 59, 0, time.UTC), "agent-known", "deny", `[{
"RuleID":"R-PREV","Decision":"deny"}]`, 2)
		insert(time.Date(2026, time.December, 30, 0, 0, 0, 0, time.UTC), "agent-known", "allow", `[]`, 3)
		insert(time.Date(2026, time.December, 31, 23, 59, 59, 0, time.UTC), "agent-known", "deny", `[{
"RuleID":"R-MATRIX","Decision":"deny"}]`, 5)
		insert(time.Date(2027, time.January, 2, 23, 59, 59, 0, time.UTC), "agent-missing", "deny", `[{
"RuleID":"R-MATRIX","Decision":"deny"}]`, 4_000_000_000)
		insert(time.Date(2027, time.January, 3, 0, 0, 0, 0, time.UTC), "agent-known", "deny", `[{
"RuleID":"R-END","Decision":"deny"}]`, 11)

		repository := opened.Dashboard()
		repository.now = func() time.Time {
			return time.Date(2027, time.January, 2, 20, 0, 0, 0, time.FixedZone("UTC+8", 8*60*60))
		}
		summary, err := repository.Summary(ctx, 4)
		require.NoError(t, err)
		require.Equal(t, int64(3), summary.KPI.TotalRequests)
		require.Equal(t, int64(2), summary.KPI.Blocked)
		require.Equal(t, int64(1), summary.KPI.PendingApprovals)
		require.Equal(t, int64(1), summary.KPI.ActiveAgents)
		require.Equal(t, int64(1), summary.KPI.DatasourcesTotal)
		require.Equal(t, []TrendDay{
			{Date: "2026-12-30", Total: 1, Allow: 1},
			{Date: "2026-12-31", Total: 1, Deny: 1},
			{Date: "2027-01-01"},
			{Date: "2027-01-02", Total: 1, Deny: 1},
		}, summary.Trend14D)
		require.Equal(t, []RiskTopEntry{{RuleID: "R-MATRIX", Count: 2}}, summary.RiskTop)
		require.Equal(t, BattleReport{BlockedCount: 2, EstRowsSaved: 4_000_000_005}, summary.BattleReport)
		require.Len(t, summary.AgentRanking, 2)
		names := map[string]string{}
		for _, item := range summary.AgentRanking {
			names[item.AgentID] = item.Name
		}
		require.Equal(t, "Known Agent", names["agent-known"])
		require.Equal(t, "", names["agent-missing"])
		var distributionTotal int64
		for _, decision := range summary.DecisionDistribution {
			distributionTotal += decision.Count
		}
		require.Equal(t, summary.KPI.TotalRequests, distributionTotal)

		thirtyDays, err := repository.Summary(ctx, 30)
		require.NoError(t, err)
		require.Len(t, thirtyDays.Trend14D, 30)
		require.Equal(t, "2026-12-04", thirtyDays.Trend14D[0].Date)
		require.Equal(t, "2027-01-02", thirtyDays.Trend14D[29].Date)
		require.Equal(t, int64(5), thirtyDays.KPI.TotalRequests)
		require.Equal(t, int64(3), thirtyDays.KPI.Blocked)
		require.Equal(t, BattleReport{BlockedCount: 3, EstRowsSaved: 4_000_000_007}, thirtyDays.BattleReport)
	})
}

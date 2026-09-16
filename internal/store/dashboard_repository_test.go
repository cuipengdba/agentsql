package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDashboardSummaryUsesUTCNaturalDayWindow(t *testing.T) {
	opened := openTestStore(t)
	repository := opened.Dashboard()
	repository.now = func() time.Time {
		return time.Date(2026, time.September, 16, 18, 30, 0, 0, time.FixedZone("UTC+8", 8*60*60))
	}

	insertDashboardAudit := func(timestamp time.Time, decision, ruleHits string, estimatedRows int) {
		t.Helper()
		_, err := opened.metaDB.ExecContext(context.Background(), repository.meta.bind(`
INSERT INTO audit_logs (ts, agent_id, decision, rule_hits, est_rows)
VALUES (?, ?, ?, ?, ?)`), timestamp, "agent-boundary", decision, ruleHits, estimatedRows)
		require.NoError(t, err)
	}

	// days=3 produces current [Sep 14, Sep 17) and comparison [Sep 11, Sep 14).
	insertDashboardAudit(time.Date(2026, time.September, 11, 0, 0, 0, 0, time.UTC), "allow", `[]`, 1)
	insertDashboardAudit(time.Date(2026, time.September, 13, 23, 59, 59, 0, time.UTC), "deny", `[{"RuleID":"R-PREV","Decision":"deny"}]`, 2)
	insertDashboardAudit(time.Date(2026, time.September, 14, 0, 0, 0, 0, time.UTC), "allow", `[]`, 3)
	insertDashboardAudit(time.Date(2026, time.September, 14, 23, 59, 59, 0, time.UTC), "deny", `[{"RuleID":"R-CURRENT","Decision":"deny"}]`, 5)
	insertDashboardAudit(time.Date(2026, time.September, 16, 12, 0, 0, 0, time.UTC), "warn", `[]`, 7)
	insertDashboardAudit(time.Date(2026, time.September, 16, 23, 59, 59, 0, time.UTC), "deny", `[{"RuleID":"R-CURRENT","Decision":"deny"}]`, 11)
	insertDashboardAudit(time.Date(2026, time.September, 17, 0, 0, 0, 0, time.UTC), "deny", `[{"RuleID":"R-END","Decision":"deny"}]`, 13)

	summary, err := repository.Summary(context.Background(), 3)
	require.NoError(t, err)
	require.Equal(t, int64(4), summary.KPI.TotalRequests)
	require.Equal(t, int64(2), summary.KPI.Blocked)
	require.NotNil(t, summary.KPI.TotalRequestsDelta)
	require.Equal(t, 100.0, *summary.KPI.TotalRequestsDelta)
	require.NotNil(t, summary.KPI.BlockedDelta)
	require.Equal(t, 100.0, *summary.KPI.BlockedDelta)

	require.Equal(t, []string{"2026-09-14", "2026-09-15", "2026-09-16"}, []string{
		summary.Trend14D[0].Date, summary.Trend14D[1].Date, summary.Trend14D[2].Date,
	})
	require.Equal(t, TrendDay{Date: "2026-09-14", Total: 2, Deny: 1, Allow: 1}, summary.Trend14D[0])
	require.Equal(t, TrendDay{Date: "2026-09-15"}, summary.Trend14D[1])
	require.Equal(t, TrendDay{Date: "2026-09-16", Total: 2, Deny: 1, Warn: 1}, summary.Trend14D[2])

	var trendTotal, trendDeny int64
	for _, day := range summary.Trend14D {
		trendTotal += day.Total
		trendDeny += day.Deny
	}
	require.Equal(t, summary.KPI.TotalRequests, trendTotal)
	require.Equal(t, summary.KPI.Blocked, trendDeny)
	require.Equal(t, BattleReport{BlockedCount: 2, EstRowsSaved: 16}, summary.BattleReport)
	require.Equal(t, []RiskTopEntry{{RuleID: "R-CURRENT", Count: 2}}, summary.RiskTop)
	require.Len(t, summary.AgentRanking, 1)
	require.Equal(t, int64(2), summary.AgentRanking[0].BlockedCount)

	var distributionTotal int64
	for _, item := range summary.DecisionDistribution {
		distributionTotal += item.Count
	}
	require.Equal(t, summary.KPI.TotalRequests, distributionTotal)
}

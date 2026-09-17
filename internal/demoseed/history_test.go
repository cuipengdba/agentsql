package demoseed

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/stretchr/testify/require"
)

func TestGenerateHistoryExactDeterministicContract(t *testing.T) {
	anchor := time.Date(2026, 9, 17, 0, 0, 0, 0, time.UTC)
	firstAudits, firstApprovals, err := GenerateHistory(anchor)
	require.NoError(t, err)
	secondAudits, secondApprovals, err := GenerateHistory(anchor)
	require.NoError(t, err)
	require.Equal(t, firstAudits, secondAudits)
	require.Equal(t, firstApprovals, secondApprovals)
	require.Len(t, firstAudits, 300)
	require.Len(t, firstApprovals, 24)
	require.NoError(t, ValidateHistory(firstAudits, firstApprovals, anchor))

	days := map[string]int{}
	decisions := map[string]int{}
	dialects := map[string]int{}
	agents := map[string]int{}
	hits := map[string]int{}
	for index, auditLog := range firstAudits {
		ordinal := index + 1
		require.Equal(t, store.DemoSeedSessionPrefix+fmt.Sprintf("%06d", ordinal), *auditLog.SessionID)
		require.Equal(t, int64(8+(ordinal*17)%193), *auditLog.LatencyMS)
		require.Equal(t, "demo-model", *auditLog.ModelName)
		require.Equal(t, "", *auditLog.ErrorMsg)
		require.Contains(t, []string{"query", "execute_write", "request_approval"}, *auditLog.MCPTool)
		days[auditLog.TS.Format("2006-01-02")]++
		decisions[auditLog.Decision]++
		dialects[*auditLog.DBType]++
		agents[*auditLog.AgentID]++
		var ruleHits []model.RuleHit
		require.NoError(t, json.Unmarshal([]byte(*auditLog.RuleHits), &ruleHits))
		for _, hit := range ruleHits {
			hits[hit.RuleID]++
		}
	}
	require.Equal(t, map[string]int{"allow": 180, "deny": 60, "warn": 36, "approve": 24}, decisions)
	require.Equal(t, map[string]int{"postgres": 150, "mysql": 150}, dialects)
	require.Equal(t, map[string]int{"agent-demo-ro": 240, "agent-demo-dml": 60}, agents)
	require.Equal(t, map[string]int{"R002": 24, "R003": 12, "R010": 18, "R006": 6, "R005": 24, "R107": 6, "R204": 6, "R004": 18, "R009": 6}, hits)
	require.Len(t, days, 30)
	for _, count := range days {
		require.Equal(t, 10, count)
	}

	statuses := map[string]int{}
	for _, approval := range firstApprovals {
		require.Nil(t, approval.AuditID)
		statuses[approval.Status]++
	}
	require.Equal(t, map[string]int{"pending": 6, "approved": 6, "rejected": 6, "expired": 6}, statuses)
}

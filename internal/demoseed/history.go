package demoseed

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/cuipengdba/agentsql/internal/config"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/store"
)

const (
	historyDays      = 30
	auditsPerDay     = 10
	historyAudits    = historyDays * auditsPerDay
	historyApprovals = 24
)

// GenerateHistory creates the fixed 300/24 demo corpus without reading the
// clock or using randomness. The anchor day and ordinal fully determine every
// field.
func GenerateHistory(anchor time.Time) ([]model.AuditLog, []model.Approval, error) {
	anchor = time.Date(anchor.Year(), anchor.Month(), anchor.Day(), 0, 0, 0, 0, time.UTC)
	if anchor.IsZero() {
		return nil, nil, fmt.Errorf("generate demo history: anchor date is required")
	}
	audits := make([]model.AuditLog, 0, historyAudits)
	approvals := make([]model.Approval, 0, historyApprovals)
	warnDialectIndex := map[string]int{"postgres": 0, "mysql": 0}
	approveIndex := 0

	for ordinal := 1; ordinal <= historyAudits; ordinal++ {
		decision, ruleID := decisionAndRule(ordinal)
		dbType, datasourceID := "postgres", config.DemoDatasourcePG
		if ordinal%2 == 0 {
			dbType, datasourceID = "mysql", config.DemoDatasourceMySQL
		}
		if decision == "warn" {
			local := warnDialectIndex[dbType]
			if local < 12 {
				ruleID = "R005"
			} else if dbType == "postgres" {
				ruleID = "R107"
			} else {
				ruleID = "R204"
			}
			warnDialectIndex[dbType] = local + 1
		}

		agentID := "agent-demo-ro"
		if ordinal%5 == 0 {
			agentID = "agent-demo-dml"
		}
		dayIndex := (ordinal - 1) / auditsPerDay
		day := anchor.AddDate(0, 0, dayIndex-(historyDays-1))
		seconds := (ordinal * 7919) % (24 * 60 * 60)
		timestamp := day.Add(time.Duration(seconds) * time.Second)
		sessionID := fmt.Sprintf("%s%06d", store.DemoSeedSessionPrefix, ordinal)
		conversationID := fmt.Sprintf("demo-conversation-%03d", (ordinal-1)/3+1)
		clientIP := fmt.Sprintf("192.0.2.%d", 1+(ordinal-1)%254)
		modelName := "demo-model"
		objects := "customers"
		sqlRaw, sqlNorm, stmtType, tool := auditStatement(ordinal, decision, ruleID)
		risk := riskForDecision(decision)
		estRows := estimatedRows(ruleID)
		rowsReturned := 0
		if decision == "allow" || decision == "warn" {
			rowsReturned = 1 + (ordinal*7)%20
		}
		latency := int64(8 + (ordinal*17)%193)
		hitsJSON, err := marshalRuleHits(ruleID, decision, risk)
		if err != nil {
			return nil, nil, err
		}
		emptyError := ""
		auditLog := model.AuditLog{
			TenantID: store.DefaultTenantID, TS: timestamp, AgentID: stringPtr(agentID), DatasourceID: stringPtr(datasourceID),
			SessionID: stringPtr(sessionID), ConversationID: stringPtr(conversationID), MCPTool: stringPtr(tool),
			DBType: stringPtr(dbType), SQLRaw: stringPtr(sqlRaw), SQLNorm: stringPtr(sqlNorm),
			StmtType: stringPtr(stmtType), Objects: stringPtr(objects), Decision: decision,
			RuleHits: stringPtr(hitsJSON), RiskLevel: intPtr(risk), EstRows: int64Ptr(estRows),
			RowsReturned: intPtr(rowsReturned), LatencyMS: int64Ptr(latency), ClientIP: stringPtr(clientIP),
			ModelName: stringPtr(modelName), ErrorMsg: stringPtr(emptyError),
		}
		audits = append(audits, auditLog)

		if decision == "approve" {
			status := []string{"pending", "approved", "rejected", "expired"}[approveIndex/6]
			reason := fmt.Sprintf("Demo approval for %s", ruleID)
			approval := model.Approval{
				ID: fmt.Sprintf("demo-approval-%06d", ordinal), TenantID: store.DefaultTenantID, AgentID: stringPtr(agentID),
				SQLRaw: stringPtr(sqlRaw), Reason: stringPtr(reason), Status: status,
			}
			if status != "pending" {
				approver := "demo-reviewer"
				if status == "expired" {
					approver = "demo-system"
				}
				decidedAt := timestamp.Add(time.Duration(15+approveIndex) * time.Minute)
				approval.Approver = stringPtr(approver)
				approval.DecidedAt = &decidedAt
			}
			approvals = append(approvals, approval)
			approveIndex++
		}
	}
	if err := ValidateHistory(audits, approvals, anchor); err != nil {
		return nil, nil, err
	}
	return audits, approvals, nil
}

func decisionAndRule(ordinal int) (string, string) {
	switch {
	case ordinal <= 180:
		return "allow", ""
	case ordinal <= 240:
		index := ordinal - 181
		switch {
		case index < 24:
			return "deny", "R002"
		case index < 36:
			return "deny", "R003"
		case index < 54:
			return "deny", "R010"
		default:
			return "deny", "R006"
		}
	case ordinal <= 276:
		return "warn", ""
	case ordinal <= 294:
		return "approve", "R004"
	default:
		return "approve", "R009"
	}
}

func auditStatement(ordinal int, decision, ruleID string) (raw, normalized, stmtType, tool string) {
	if decision == "approve" {
		raw = fmt.Sprintf("UPDATE customers SET demo_flag = %d WHERE id = %d", ordinal%2, ordinal)
		return raw, "UPDATE customers SET demo_flag = ? WHERE id = ?", "UPDATE", "request_approval"
	}
	if decision == "deny" && (ruleID == "R002" || ruleID == "R003") {
		raw = "UPDATE customers SET demo_flag = 1"
		return raw, "UPDATE customers SET demo_flag = ?", "UPDATE", "execute_write"
	}
	raw = fmt.Sprintf("SELECT id, phone, email FROM customers WHERE id = %d", ordinal)
	return raw, "SELECT id, phone, email FROM customers WHERE id = ?", "SELECT", "query"
}

func marshalRuleHits(ruleID, decision string, risk int) (string, error) {
	hits := []model.RuleHit{}
	if ruleID != "" {
		hits = append(hits, model.RuleHit{
			RuleID: ruleID, Risk: model.RiskLevel(risk), Decision: model.Decision(decision),
			Message: "Deterministic Live Demo rule hit", Suggestion: "Review the Live Demo example",
		})
	}
	encoded, err := json.Marshal(hits)
	if err != nil {
		return "", fmt.Errorf("marshal demo audit rule hits: %w", err)
	}
	return string(encoded), nil
}

func riskForDecision(decision string) int {
	switch decision {
	case "deny":
		return 1
	case "approve":
		return 2
	case "warn":
		return 3
	default:
		return 4
	}
}

func estimatedRows(ruleID string) int64 {
	switch ruleID {
	case "R004":
		return 50000
	case "R005":
		return 2400
	case "R107", "R204":
		return 600
	default:
		return 0
	}
}

// ValidateHistory verifies both the aggregate contract and per-day coverage.
func ValidateHistory(audits []model.AuditLog, approvals []model.Approval, anchor time.Time) error {
	if len(audits) != historyAudits || len(approvals) != historyApprovals {
		return fmt.Errorf("demo history count mismatch: audits=%d approvals=%d", len(audits), len(approvals))
	}
	decisions := map[string]int{}
	dialects := map[string]int{}
	agents := map[string]int{}
	rulesCount := map[string]int{}
	days := map[string]int{}
	statuses := map[string]int{}
	for _, auditLog := range audits {
		decisions[auditLog.Decision]++
		if auditLog.DBType != nil {
			dialects[*auditLog.DBType]++
		}
		if auditLog.AgentID != nil {
			agents[*auditLog.AgentID]++
		}
		days[auditLog.TS.UTC().Format("2006-01-02")]++
		if auditLog.RuleHits != nil {
			var hits []model.RuleHit
			if err := json.Unmarshal([]byte(*auditLog.RuleHits), &hits); err != nil {
				return fmt.Errorf("decode generated rule hits: %w", err)
			}
			for _, hit := range hits {
				rulesCount[hit.RuleID]++
			}
		}
	}
	for _, approval := range approvals {
		statuses[approval.Status]++
	}
	wantDecision := map[string]int{"allow": 180, "deny": 60, "warn": 36, "approve": 24}
	wantDialect := map[string]int{"postgres": 150, "mysql": 150}
	wantAgents := map[string]int{"agent-demo-ro": 240, "agent-demo-dml": 60}
	wantRules := map[string]int{"R002": 24, "R003": 12, "R010": 18, "R006": 6, "R005": 24, "R107": 6, "R204": 6, "R004": 18, "R009": 6}
	wantStatuses := map[string]int{"pending": 6, "approved": 6, "rejected": 6, "expired": 6}
	if !sameCounts(decisions, wantDecision) || !sameCounts(dialects, wantDialect) ||
		!sameCounts(agents, wantAgents) || !sameCounts(rulesCount, wantRules) || !sameCounts(statuses, wantStatuses) {
		return fmt.Errorf("demo history distribution mismatch")
	}
	anchor = anchor.UTC()
	for offset := 0; offset < historyDays; offset++ {
		day := anchor.AddDate(0, 0, -offset).Format("2006-01-02")
		if days[day] != auditsPerDay {
			return fmt.Errorf("demo history day %s has %d audits, want %d", day, days[day], auditsPerDay)
		}
	}
	return nil
}

func sameCounts(got, want map[string]int) bool {
	if len(got) != len(want) {
		return false
	}
	for key, value := range want {
		if got[key] != value {
			return false
		}
	}
	return true
}

func stringPtr(value string) *string { return &value }
func intPtr(value int) *int          { return &value }
func int64Ptr(value int64) *int64    { return &value }

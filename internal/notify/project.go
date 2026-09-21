package notify

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
)

// Payload is the complete outbound whitelist. Adding any field here requires a
// security review; AuditLog must never be marshaled directly.
type Payload struct {
	AuditID       int64    `json:"audit_id"`
	Timestamp     string   `json:"timestamp"`
	Decision      string   `json:"decision"`
	RiskLevel     *int     `json:"risk_level,omitempty"`
	RuleIDs       []string `json:"rule_ids"`
	Datasource    Identity `json:"datasource"`
	Agent         Identity `json:"agent"`
	MCPTool       *string  `json:"mcp_tool,omitempty"`
	StatementType *string  `json:"stmt_type,omitempty"`
	EstimatedRows *int64   `json:"est_rows,omitempty"`
	RowsReturned  *int     `json:"rows_returned,omitempty"`
	LatencyMS     *int64   `json:"latency_ms,omitempty"`
	ErrorCode     string   `json:"error_code,omitempty"`
	SQLNorm       *string  `json:"sql_norm,omitempty"`
}

// Identity contains only the stable ID and an optional display name.
type Identity struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
}

func project(ctx context.Context, audit model.AuditLog, includeSQL bool, resolver NameResolver) Payload {
	payload := Payload{
		AuditID:       audit.ID,
		Timestamp:     audit.TS.UTC().Format(time.RFC3339Nano),
		Decision:      strings.ToLower(strings.TrimSpace(audit.Decision)),
		RiskLevel:     clone(audit.RiskLevel),
		RuleIDs:       ruleIDs(audit.RuleHits),
		MCPTool:       clone(audit.MCPTool),
		StatementType: clone(audit.StmtType),
		EstimatedRows: clone(audit.EstRows),
		RowsReturned:  clone(audit.RowsReturned),
		LatencyMS:     clone(audit.LatencyMS),
	}
	if audit.TS.IsZero() {
		payload.Timestamp = time.Unix(0, 0).UTC().Format(time.RFC3339)
	}
	if audit.AgentID != nil {
		payload.Agent.ID = strings.TrimSpace(*audit.AgentID)
	}
	if audit.DatasourceID != nil {
		payload.Datasource.ID = strings.TrimSpace(*audit.DatasourceID)
	}
	if resolver != nil {
		if payload.Agent.ID != "" {
			payload.Agent.Name = strings.TrimSpace(resolver.AgentName(ctx, payload.Agent.ID))
		}
		if payload.Datasource.ID != "" {
			payload.Datasource.Name = strings.TrimSpace(resolver.DatasourceName(ctx, payload.Datasource.ID))
		}
	}
	if includeSQL && audit.SQLNorm != nil {
		payload.SQLNorm = clone(audit.SQLNorm)
	}
	if stored := strings.TrimSpace(value(audit.ErrorCode)); stored != "" {
		payload.ErrorCode = stableErrorCode(stored)
	} else if audit.ErrorMsg != nil && strings.TrimSpace(*audit.ErrorMsg) != "" || payload.Decision == "error" {
		// Text inference is retained only for audit rows created before error_code
		// was persisted. New rows must use the stable stored enumeration above.
		payload.ErrorCode = mapErrorCode(value(audit.ErrorMsg))
	}
	return payload
}

func stableErrorCode(code string) string {
	switch code {
	case "DB_OBJECT_NOT_FOUND",
		"DB_COLUMN_NOT_FOUND",
		"DB_OBJECT_ALREADY_EXISTS",
		"DB_SYNTAX_ERROR",
		"DB_SEMANTIC_ERROR",
		"DB_DATA_EXCEPTION",
		"DB_CONSTRAINT_VIOLATION",
		"DB_RETRYABLE_CONFLICT",
		"DB_TRANSACTION_STATE",
		"DB_RESOURCE_EXHAUSTED",
		"DB_QUERY_TIMEOUT",
		"DB_QUERY_INTERRUPTED",
		"DB_PERMISSION_DENIED",
		"DB_READ_ONLY_VIOLATION",
		"DB_AUTHENTICATION_FAILED",
		"DB_DATABASE_NOT_FOUND",
		"DB_DATASOURCE_UNREACHABLE",
		"DB_EXECUTION_FAILED",
		"GATEWAY_INTERNAL",
		"AUDIT_UNAVAILABLE",
		"COMMIT_OUTCOME_UNKNOWN":
		return code
	default:
		return "unknown"
	}
}

func isIntentAudit(audit model.AuditLog) bool {
	if audit.DetailsJSON == nil || strings.TrimSpace(*audit.DetailsJSON) == "" {
		return false
	}
	var details struct {
		AuditPhase string `json:"audit_phase"`
	}
	if json.Unmarshal([]byte(*audit.DetailsJSON), &details) != nil {
		return false
	}
	return details.AuditPhase == string(auditPhaseIntentValue)
}

type auditPhaseValue string

const auditPhaseIntentValue auditPhaseValue = "intent"

func matchesDecision(config ChannelConfig, decision string) bool {
	decision = strings.ToLower(strings.TrimSpace(decision))
	for _, allowed := range config.Decisions {
		if decision == allowed {
			return true
		}
	}
	return false
}

func ruleIDs(encoded *string) []string {
	if encoded == nil || strings.TrimSpace(*encoded) == "" {
		return []string{}
	}
	var hits []map[string]any
	if json.Unmarshal([]byte(*encoded), &hits) != nil {
		return []string{}
	}
	result := make([]string, 0, len(hits))
	seen := make(map[string]struct{}, len(hits))
	for _, hit := range hits {
		var id string
		for _, key := range []string{"rule_id", "RuleID", "id", "ID"} {
			if text, ok := hit[key].(string); ok {
				id = strings.TrimSpace(text)
				if id != "" {
					break
				}
			}
		}
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	return result
}

func mapErrorCode(message string) string {
	lower := strings.ToLower(message)
	patterns := []struct {
		code  string
		terms []string
	}{
		{"rate_limited", []string{"rate limit", "too many requests", "qps", "status 429"}},
		{"row_limit", []string{"row limit", "too many rows", "result too large", "rows exceeded"}},
		{"timeout", []string{"timeout", "timed out", "deadline exceeded", "context deadline"}},
		{"permission_denied", []string{"permission denied", "access denied", "unauthorized", "forbidden", "authentication failed", "not authorized"}},
		{"syntax", []string{"syntax error", "parse error", "invalid syntax"}},
		{"connect_error", []string{"connection refused", "connect error", "dial tcp", "dial udp", "no such host", "network is unreachable", "connection reset"}},
	}
	for _, candidate := range patterns {
		for _, term := range candidate.terms {
			if strings.Contains(lower, term) {
				return candidate.code
			}
		}
	}
	return "unknown"
}

func payloadText(payload Payload) string {
	rules := "-"
	if len(payload.RuleIDs) > 0 {
		rules = strings.Join(payload.RuleIDs, ",")
	}
	datasource := payload.Datasource.ID
	if payload.Datasource.Name != "" {
		datasource += " (" + payload.Datasource.Name + ")"
	}
	agent := payload.Agent.ID
	if payload.Agent.Name != "" {
		agent += " (" + payload.Agent.Name + ")"
	}
	parts := []string{
		"AgentSQL notification",
		"decision: " + payload.Decision,
		"risk_level: " + pointerInt(payload.RiskLevel),
		"rule_ids: " + rules,
		"datasource: " + datasource,
		"agent: " + agent,
		"audit_id: " + strconv.FormatInt(payload.AuditID, 10),
		"timestamp: " + payload.Timestamp,
		"detail: /audit/" + strconv.FormatInt(payload.AuditID, 10),
	}
	if payload.ErrorCode != "" {
		parts = append(parts, "error_code: "+payload.ErrorCode)
	}
	if payload.SQLNorm != nil {
		parts = append(parts, "sql_norm: "+*payload.SQLNorm)
	}
	return strings.Join(parts, "\n")
}

func pointerInt(number *int) string {
	if number == nil {
		return "-"
	}
	return strconv.Itoa(*number)
}

func value(pointer *string) string {
	if pointer == nil {
		return ""
	}
	return *pointer
}

func clone[T any](pointer *T) *T {
	if pointer == nil {
		return nil
	}
	result := *pointer
	return &result
}

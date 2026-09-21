package notify

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/mask"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestProjectionAndFiveWebhookTemplatesUseOutboundWhitelist(t *testing.T) {
	audit := sensitiveAudit()
	payload := project(context.Background(), audit, false, staticNames{})
	require.Equal(t, []string{"R001", "R002"}, payload.RuleIDs)
	require.Equal(t, "permission_denied", payload.ErrorCode)
	require.Equal(t, "agent one", payload.Agent.Name)
	require.Equal(t, "primary", payload.Datasource.Name)
	require.Nil(t, payload.SQLNorm)

	for _, template := range []WebhookTemplate{WebhookGeneric, WebhookFeishu, WebhookDingTalk, WebhookWeCom, WebhookSlack} {
		t.Run(string(template), func(t *testing.T) {
			raw, err := renderWebhook(template, payload)
			require.NoError(t, err)
			text := string(raw)
			for _, expected := range []string{"deny", "R001", "R002", "ds-1", "primary", "agent-1", "agent one", "permission_denied"} {
				require.Contains(t, text, expected)
			}
			for _, forbidden := range []string{"SELECT credit_card", "raw SQL secret", "top-secret rule message", "never send this suggestion", "secret_table", "T38_RAW_SAMPLE_SENTINEL_7b19", "ErrorMsg", "SQLRaw", "Objects", "ClientIP", "DetailsJSON"} {
				require.NotContains(t, text, forbidden)
			}
			var document map[string]any
			require.NoError(t, json.Unmarshal(raw, &document))
			switch template {
			case WebhookGeneric:
				require.Equal(t, float64(42), document["audit_id"])
				require.NotContains(t, document, "sql_norm")
			case WebhookFeishu:
				require.Equal(t, "text", document["msg_type"])
			case WebhookDingTalk, WebhookWeCom:
				require.Equal(t, "markdown", document["msgtype"])
			case WebhookSlack:
				require.Contains(t, document, "text")
			}
		})
	}
}

func TestProjectionIncludeSQLOnlyAddsNormalizedSQL(t *testing.T) {
	payload := project(context.Background(), sensitiveAudit(), true, nil)
	require.NotNil(t, payload.SQLNorm)
	require.Equal(t, "SELECT credit_card FROM accounts WHERE id = ?", *payload.SQLNorm)
	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	require.Contains(t, string(raw), "sql_norm")
	require.NotContains(t, string(raw), "raw SQL secret")
}

func TestHashResultDoesNotExpandNotificationPayload(t *testing.T) {
	const raw = "T41_NOTIFY_RAW_SENTINEL_b7a3"
	redactor, err := mask.NewRedactor([]mask.Rule{{
		Column: "name", SensitiveType: mask.TypeGeneric, Algorithm: mask.AlgoHash,
	}}, mask.WithHashKey([]byte("notify-hash-key-0123456789abcdef012")))
	require.NoError(t, err)
	redacted, report := redactor.Apply(model.QueryResult{Columns: []string{"name"}, Rows: [][]string{{raw}}})
	require.Equal(t, 1, report.MaskedCells)
	require.Regexp(t, `^h\.[0-9a-f]{32}$`, redacted.Rows[0][0])

	audit := sensitiveAudit()
	details := `{"raw_result":"` + raw + `","fingerprint":"` + redacted.Rows[0][0] + `"}`
	audit.DetailsJSON = &details
	payload := project(context.Background(), audit, false, nil)
	webhook, err := renderWebhook(WebhookGeneric, payload)
	require.NoError(t, err)
	syslog, err := renderSyslog(16, payload, time.Unix(1, 0))
	require.NoError(t, err)
	for _, output := range []string{string(webhook), string(syslog), payloadText(payload)} {
		require.NotContains(t, output, raw)
		require.NotContains(t, output, redacted.Rows[0][0])
		require.NotContains(t, output, "raw_result")
		require.NotContains(t, output, "fingerprint")
	}
}

func TestDecisionFilterDefaultsAndValidation(t *testing.T) {
	original := Config{Enabled: true, Channels: []ChannelConfig{{
		ID: "hook", Enabled: true, Kind: ChannelWebhook,
		Decisions: []string{"deny"},
		Webhook:   &WebhookConfig{Template: WebhookGeneric, URL: "https://example.com/hook", Headers: map[string]string{"X-Test": "before"}},
	}}}
	config, err := normalizeConfig(original)
	require.NoError(t, err)
	channel := config.Channels[0]
	require.True(t, matchesDecision(channel, "deny"))
	require.False(t, matchesDecision(channel, "allow"))
	require.False(t, matchesDecision(channel, "warn"))
	require.False(t, matchesDecision(channel, "approve"))
	original.Channels[0].Decisions[0] = "allow"
	original.Channels[0].Webhook.Headers["X-Test"] = "after"
	require.Equal(t, []string{"deny"}, config.Channels[0].Decisions)
	require.Equal(t, "before", config.Channels[0].Webhook.Headers["X-Test"])

	defaults, err := normalizeDecisions(nil)
	require.NoError(t, err)
	require.Equal(t, []string{"deny", "error"}, defaults)

	_, err = normalizeDecisions([]string{"deny", "invented"})
	require.Error(t, err)
}

func TestErrorCodeMappingAlwaysUsesFixedEnumeration(t *testing.T) {
	tests := map[string]string{
		"dial tcp 1.2.3.4: connection refused":   "connect_error",
		"context deadline exceeded":              "timeout",
		"syntax error near DROP":                 "syntax",
		"permission denied for table users":      "permission_denied",
		"row limit exceeded":                     "row_limit",
		"too many requests from rate limiter":    "rate_limited",
		"driver disclosed unknown internal data": "unknown",
	}
	allowed := map[string]bool{"connect_error": true, "timeout": true, "syntax": true, "permission_denied": true, "row_limit": true, "rate_limited": true, "unknown": true}
	for input, expected := range tests {
		actual := mapErrorCode(input)
		require.Equal(t, expected, actual)
		require.True(t, allowed[actual])
	}
}

func TestProjectionPrefersPersistedStableErrorCodeAndValidatesIt(t *testing.T) {
	audit := sensitiveAudit()
	persisted := "DB_OBJECT_NOT_FOUND"
	audit.ErrorCode = &persisted
	payload := project(context.Background(), audit, false, nil)
	require.Equal(t, persisted, payload.ErrorCode, "stored code must win over legacy message inference")

	invalid := "SECRET_VENDOR_CODE: table=customers"
	audit.ErrorCode = &invalid
	payload = project(context.Background(), audit, false, nil)
	require.Equal(t, "unknown", payload.ErrorCode)
	encoded, err := json.Marshal(payload)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), invalid)
	require.NotContains(t, payloadText(payload), invalid)

	audit.ErrorCode = nil
	payload = project(context.Background(), audit, false, nil)
	require.Equal(t, "permission_denied", payload.ErrorCode, "historical rows still use text fallback")
}

type staticNames struct{}

func (staticNames) AgentName(_ context.Context, id string) string {
	if id == "agent-1" {
		return "agent one"
	}
	return ""
}

func (staticNames) DatasourceName(_ context.Context, id string) string {
	if id == "ds-1" {
		return "primary"
	}
	return ""
}

func sensitiveAudit() model.AuditLog {
	agentID := "agent-1"
	datasourceID := "ds-1"
	mcpTool := "query"
	statementType := "SELECT"
	sqlRaw := "raw SQL secret"
	sqlNorm := "SELECT credit_card FROM accounts WHERE id = ?"
	objects := `[{"schema":"private","table":"accounts"}]`
	ruleHits := `[{"RuleID":"R001","Risk":1,"Message":"top-secret rule message","Suggestion":"never send this suggestion"},{"rule_id":"R002","message":"also secret"}]`
	risk := 1
	estRows := int64(100)
	rowsReturned := 5
	latency := int64(12)
	clientIP := "192.168.1.2"
	errorMessage := "permission denied for secret_table while running SELECT credit_card"
	detailsJSON := `{"forbidden_sample":"T38_RAW_SAMPLE_SENTINEL_7b19"}`
	return model.AuditLog{
		ID: 42, TS: time.Date(2026, 9, 19, 1, 2, 3, 4, time.UTC), AgentID: &agentID,
		DatasourceID: &datasourceID, MCPTool: &mcpTool, SQLRaw: &sqlRaw, SQLNorm: &sqlNorm,
		StmtType: &statementType, Objects: &objects, Decision: "deny", RuleHits: &ruleHits,
		RiskLevel: &risk, EstRows: &estRows, RowsReturned: &rowsReturned, LatencyMS: &latency,
		ClientIP: &clientIP, ErrorMsg: &errorMessage, DetailsJSON: &detailsJSON,
	}
}

func assertNoSensitiveText(t *testing.T, text string) {
	t.Helper()
	for _, forbidden := range []string{"raw SQL secret", "top-secret", "never send", "secret_table"} {
		require.False(t, strings.Contains(text, forbidden))
	}
}

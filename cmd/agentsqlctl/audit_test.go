package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/stretchr/testify/require"
)

const (
	auditTestUUIDAllow = "11111111-1111-4111-8111-111111111111"
	auditTestUUIDError = "22222222-2222-4222-8222-222222222222"
)

func TestAuditQueryFiltersJSONAndLimitNotice(t *testing.T) {
	configPath := prepareAuditCLIStore(t)

	var output, commandError strings.Builder
	exitCode := run([]string{
		"audit", "query", "-c", configPath, "--format", "json",
		"--since", "2026-09-01T00:00:00Z", "--until", "2026-09-30T23:59:59Z",
		"--action", "query", "--actor", "alice", "--db", "prod-db",
		"--rule", "rule-pii", "--status", "success", "--uuid", auditTestUUIDAllow,
	}, &output, &commandError)
	require.Equal(t, 0, exitCode, commandError.String())
	var decoded auditQueryOutput
	require.NoError(t, json.Unmarshal([]byte(output.String()), &decoded))
	require.EqualValues(t, 1, decoded.Total)
	require.Len(t, decoded.Records, 1)
	require.Equal(t, auditTestUUIDAllow, *decoded.Records[0].EventUUID)
	require.Equal(t, "allow", decoded.Records[0].Decision)

	output.Reset()
	commandError.Reset()
	exitCode = run([]string{"audit", "query", "-c", configPath, "--limit", "1"}, &output, &commandError)
	require.Equal(t, 0, exitCode, commandError.String())
	require.Contains(t, output.String(), "ID")
	require.Contains(t, output.String(), "STATUS")
	require.Contains(t, commandError.String(), "showing 1 of 3")
	require.Contains(t, commandError.String(), "maximum 1000")
}

func TestAuditQueryStatusErrorAndActorFallback(t *testing.T) {
	configPath := prepareAuditCLIStore(t)

	var output, commandError strings.Builder
	require.Equal(t, 0, run([]string{
		"audit", "query", "-c", configPath, "--format", "json", "--status", "error",
		"--error-code", "DB_QUERY_TIMEOUT", "--actor", "bob",
	}, &output, &commandError), commandError.String())
	var decoded auditQueryOutput
	require.NoError(t, json.Unmarshal([]byte(output.String()), &decoded))
	require.EqualValues(t, 1, decoded.Total)
	require.Equal(t, "error", decoded.Records[0].Decision)

	output.Reset()
	commandError.Reset()
	require.Equal(t, 0, run([]string{
		"audit", "query", "-c", configPath, "--format", "json", "--actor", "legacy-agent",
	}, &output, &commandError), commandError.String())
	require.NoError(t, json.Unmarshal([]byte(output.String()), &decoded))
	require.EqualValues(t, 1, decoded.Total)
	require.Equal(t, "legacy-agent", *decoded.Records[0].AgentID)
}

func TestAuditReportCSVAndSummary(t *testing.T) {
	configPath := prepareAuditCLIStore(t)
	outputPath := filepath.Join(t.TempDir(), "compliance.csv")
	var output, commandError strings.Builder
	require.Equal(t, 0, run([]string{
		"audit", "report", "-c", configPath, "--out", outputPath, "--format", "csv",
	}, &output, &commandError), commandError.String())
	require.Contains(t, output.String(), "events=3")
	require.Empty(t, commandError.String())

	report, err := os.ReadFile(outputPath)
	require.NoError(t, err)
	require.True(t, bytes.HasPrefix(report, []byte{0xEF, 0xBB, 0xBF}))
	records, err := csv.NewReader(bytes.NewReader(report[3:])).ReadAll()
	require.NoError(t, err)
	require.Len(t, records, 4)
	require.Equal(t, auditReportColumns, records[0])
	require.Contains(t, []string{records[1][26], records[2][26], records[3][26]}, auditTestUUIDError)

	summaryContents, err := os.ReadFile(outputPath + ".summary.json")
	require.NoError(t, err)
	var summary auditReportSummary
	require.NoError(t, json.Unmarshal(summaryContents, &summary))
	require.Equal(t, 3, summary.TotalEvents)
	require.Equal(t, map[string]int{"error": 1, "success": 2}, summary.StatusDistribution)
	require.Equal(t, 2, summary.ActionDistribution["query"])
	require.Equal(t, 1, summary.ActionDistribution["execute"])
	require.Equal(t, []auditCount{{Value: "DB_QUERY_TIMEOUT", Count: 1}}, summary.TopErrorCodes)
	require.Equal(t, "alice", summary.TopActors[0].Value)
	require.Equal(t, 1, summary.TopActors[0].Count)
	require.NotNil(t, summary.TimeRange.Since)
	require.NotNil(t, summary.TimeRange.Until)
	require.Equal(t, "2026-09-01T10:00:00Z", summary.TimeRange.Since.Format(time.RFC3339))
	require.Equal(t, "2026-09-03T10:00:00Z", summary.TimeRange.Until.Format(time.RFC3339))

	commandError.Reset()
	require.Equal(t, 1, run([]string{"audit", "report", "-c", configPath, "--out", outputPath}, io.Discard, &commandError))
	require.Contains(t, commandError.String(), "already exists")
}

func TestAuditReportJSONLFiltersAndCeiling(t *testing.T) {
	configPath := prepareAuditCLIStore(t)
	outputPath := filepath.Join(t.TempDir(), "errors.jsonl")
	var output, commandError strings.Builder
	require.Equal(t, 0, run([]string{
		"audit", "report", "-c", configPath, "--out", outputPath, "--format", "jsonl", "--status", "error",
	}, &output, &commandError), commandError.String())
	report, err := os.ReadFile(outputPath)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(report)), "\n")
	require.Len(t, lines, 1)
	var record auditRecord
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &record))
	require.Equal(t, auditTestUUIDError, *record.EventUUID)

	commandError.Reset()
	exceededPath := filepath.Join(t.TempDir(), "too-small.csv")
	require.Equal(t, 1, run([]string{
		"audit", "report", "-c", configPath, "--out", exceededPath, "--limit", "2",
	}, io.Discard, &commandError))
	require.Contains(t, commandError.String(), "matching audit events (3) exceed --limit 2")
	require.NoFileExists(t, exceededPath)
}

func TestAuditCommandValidation(t *testing.T) {
	configPath := prepareAuditCLIStore(t)
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "query format", args: []string{"audit", "query", "--format", "csv"}, want: "table or json"},
		{name: "report format", args: []string{"audit", "report", "--out", "x", "--format", "pdf"}, want: "csv or jsonl"},
		{name: "missing out", args: []string{"audit", "report"}, want: "--out is required"},
		{name: "bad status", args: []string{"audit", "query", "--status", "denied"}, want: "success or error"},
		{name: "bad time", args: []string{"audit", "query", "--since", "yesterday"}, want: "RFC3339"},
		{name: "reverse time", args: []string{"audit", "query", "--since", "2026-10-02T00:00:00Z", "--until", "2026-10-01T00:00:00Z"}, want: "must not be after"},
		{name: "bad UUID", args: []string{"audit", "query", "--uuid", "not-a-uuid"}, want: "valid UUID"},
		{name: "query limit", args: []string{"audit", "query", "--limit", "1001"}, want: "between 1 and 1000"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			args := append(test.args, "-c", configPath)
			var commandError strings.Builder
			require.Equal(t, 1, run(args, io.Discard, &commandError))
			require.Contains(t, commandError.String(), test.want)
		})
	}
}

func TestAuditCSVSanitizesFormulaText(t *testing.T) {
	require.Equal(t, "'=cmd", auditCSVSanitize("=cmd"))
	require.Equal(t, "'  +cmd", auditCSVSanitize("  +cmd"))
	require.Equal(t, "safe", auditCSVSanitize("safe"))
}

func prepareAuditCLIStore(t *testing.T) string {
	t.Helper()
	t.Setenv("AGENTSQL_STORE_METADATA_DSN", "")
	t.Setenv("AGENTSQL_STORE_AUDIT_DSN", "")
	databasePath := filepath.Join(t.TempDir(), "agentsql.db")
	database, err := sql.Open("sqlite", databasePath)
	require.NoError(t, err)
	database.SetMaxOpenConns(1)
	require.NoError(t, store.MigrateMetadata(context.Background(), database, store.DialectSQLite, false))
	fixtures := []struct {
		timestamp, agent, datasource, decision, action, actor, rules, errorCode, eventUUID string
	}{
		{"2026-09-01T10:00:00Z", "agent-a", "prod-db", "allow", "query", "alice", `["rule-pii"]`, "", auditTestUUIDAllow},
		{"2026-09-02T10:00:00Z", "agent-b", "prod-db", "error", "execute", "bob", `["rule-timeout"]`, "DB_QUERY_TIMEOUT", auditTestUUIDError},
		{"2026-09-03T10:00:00Z", "legacy-agent", "archive-db", "deny", "query", "", `["rule-policy"]`, "", "33333333-3333-4333-8333-333333333333"},
	}
	for _, fixture := range fixtures {
		var actor, errorCode any
		if fixture.actor != "" {
			actor = fixture.actor
		}
		if fixture.errorCode != "" {
			errorCode = fixture.errorCode
		}
		_, err := database.Exec(`INSERT INTO audit_logs
  (ts, agent_id, datasource_id, decision, action, actor_type, actor_id, rule_hits, error_code, event_uuid)
VALUES (?, ?, ?, ?, ?, 'agent', ?, ?, ?, ?)`,
			fixture.timestamp, fixture.agent, fixture.datasource, fixture.decision, fixture.action,
			actor, fixture.rules, errorCode, fixture.eventUUID)
		require.NoError(t, err)
	}
	require.NoError(t, database.Close())
	configContents := strings.Replace(defaultConfigTemplate, "./data/agentsql.db", filepath.ToSlash(databasePath), 1)
	return writeControlConfig(t, configContents)
}

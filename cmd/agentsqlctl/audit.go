package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
	"unicode"

	"github.com/cuipengdba/agentsql/internal/config"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

const (
	auditQueryDefaultLimit  = 100
	auditQueryMaximumLimit  = 1000
	auditReportDefaultLimit = 10000
	auditReportMaximumLimit = 100000
)

var errAuditReportLimit = errors.New("audit report matching event limit exceeded")

var auditReportColumns = []string{
	"id", "ts", "agent_id", "datasource_id", "session_id", "conversation_id",
	"mcp_tool", "db_type", "sql_raw", "sql_norm", "stmt_type", "objects",
	"decision", "rule_hits", "risk_level", "est_rows", "rows_returned", "latency_ms",
	"client_ip", "model_name", "error_msg", "error_code", "action", "actor_type",
	"actor_id", "details_json", "event_uuid",
}

type auditFilterFlags struct {
	configPath string
	since      string
	until      string
	action     string
	actor      string
	database   string
	rule       string
	status     string
	errorCode  string
	eventUUID  string
}

type auditRecord struct {
	ID             int64     `json:"id"`
	TS             time.Time `json:"ts"`
	AgentID        *string   `json:"agent_id"`
	DatasourceID   *string   `json:"datasource_id"`
	SessionID      *string   `json:"session_id"`
	ConversationID *string   `json:"conversation_id"`
	MCPTool        *string   `json:"mcp_tool"`
	DBType         *string   `json:"db_type"`
	SQLRaw         *string   `json:"sql_raw"`
	SQLNorm        *string   `json:"sql_norm"`
	StmtType       *string   `json:"stmt_type"`
	Objects        *string   `json:"objects"`
	Decision       string    `json:"decision"`
	RuleHits       *string   `json:"rule_hits"`
	RiskLevel      *int      `json:"risk_level"`
	EstRows        *int64    `json:"est_rows"`
	RowsReturned   *int      `json:"rows_returned"`
	LatencyMS      *int64    `json:"latency_ms"`
	ClientIP       *string   `json:"client_ip"`
	ModelName      *string   `json:"model_name"`
	ErrorMsg       *string   `json:"error_msg"`
	ErrorCode      *string   `json:"error_code"`
	Action         *string   `json:"action"`
	ActorType      *string   `json:"actor_type"`
	ActorID        *string   `json:"actor_id"`
	DetailsJSON    *string   `json:"details_json"`
	EventUUID      *string   `json:"event_uuid"`
}

type auditQueryOutput struct {
	Total    int64         `json:"total"`
	Returned int           `json:"returned"`
	Limit    int           `json:"limit"`
	Records  []auditRecord `json:"records"`
}

type auditCount struct {
	Value string `json:"value"`
	Count int    `json:"count"`
}

type auditReportTimeRange struct {
	Since *time.Time `json:"since"`
	Until *time.Time `json:"until"`
}

type auditReportSummary struct {
	GeneratedAt        time.Time            `json:"generated_at"`
	Format             string               `json:"format"`
	TotalEvents        int                  `json:"total_events"`
	TimeRange          auditReportTimeRange `json:"time_range"`
	ActionDistribution map[string]int       `json:"action_distribution"`
	StatusDistribution map[string]int       `json:"status_distribution"`
	TopErrorCodes      []auditCount         `json:"top_error_codes"`
	TopActors          []auditCount         `json:"top_actors"`
	Columns            []string             `json:"columns"`
}

func newAuditCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "audit",
		Short: "Query audit logs and export compliance reports (planned for v0.5)",
		Args:  cobra.NoArgs,
	}
	command.AddCommand(newAuditQueryCommand())
	command.AddCommand(newAuditReportCommand())
	return command
}

func newAuditQueryCommand() *cobra.Command {
	var flags auditFilterFlags
	var format string
	var limit int
	command := &cobra.Command{
		Use:   "query",
		Short: "Query audit log records (planned for v0.5)",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if format != "table" && format != "json" {
				return errors.New("audit query --format must be table or json")
			}
			if limit < 1 || limit > auditQueryMaximumLimit {
				return fmt.Errorf("audit query --limit must be between 1 and %d", auditQueryMaximumLimit)
			}
			filter, err := flags.filter()
			if err != nil {
				return err
			}
			opened, err := openAuditCLI(command.Context(), flags.configPath)
			if err != nil {
				return err
			}
			defer opened.close()
			page, err := opened.reader.FilteredPage(command.Context(), filter, 1, limit)
			if err != nil {
				return safeStoreError("query audit logs", opened.target, err)
			}
			if format == "json" {
				output := auditQueryOutput{Total: page.Total, Returned: len(page.List), Limit: limit, Records: auditRecords(page.List)}
				encoder := json.NewEncoder(command.OutOrStdout())
				encoder.SetIndent("", "  ")
				if err := encoder.Encode(output); err != nil {
					return fmt.Errorf("write audit query JSON: %w", err)
				}
			} else if err := writeAuditTable(command.OutOrStdout(), page.List); err != nil {
				return err
			}
			if page.Total > int64(len(page.List)) {
				_, err = fmt.Fprintf(command.ErrOrStderr(), "notice: showing %d of %d audit events; raise --limit (maximum %d) or narrow the filters\n", len(page.List), page.Total, auditQueryMaximumLimit)
				if err != nil {
					return fmt.Errorf("write audit query limit notice: %w", err)
				}
			}
			return nil
		},
	}
	flags.bind(command)
	command.Flags().StringVar(&format, "format", "table", "output format: table or json")
	command.Flags().IntVar(&limit, "limit", auditQueryDefaultLimit, "maximum records to return (1-1000)")
	return command
}

func newAuditReportCommand() *cobra.Command {
	var flags auditFilterFlags
	var format, outputPath string
	var limit int
	command := &cobra.Command{
		Use:   "report",
		Short: "Export a compliance report and summary (planned for v0.5)",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if format != "csv" && format != "jsonl" {
				return errors.New("audit report --format must be csv or jsonl")
			}
			if strings.TrimSpace(outputPath) == "" {
				return errors.New("audit report --out is required")
			}
			if limit < 1 || limit > auditReportMaximumLimit {
				return fmt.Errorf("audit report --limit must be between 1 and %d", auditReportMaximumLimit)
			}
			filter, err := flags.filter()
			if err != nil {
				return err
			}
			opened, err := openAuditCLI(command.Context(), flags.configPath)
			if err != nil {
				return err
			}
			logs, err := collectAuditReport(command.Context(), opened.reader, filter, limit)
			closeErr := opened.close()
			if err != nil {
				if errors.Is(err, errAuditReportLimit) {
					return errors.Join(err, closeErr)
				}
				return safeStoreError("collect audit report", opened.target, errors.Join(err, closeErr))
			}
			if closeErr != nil {
				return safeStoreError("close audit report database", opened.target, closeErr)
			}
			report, err := renderAuditReport(format, logs)
			if err != nil {
				return err
			}
			summary := buildAuditReportSummary(format, logs)
			summaryBytes, err := json.MarshalIndent(summary, "", "  ")
			if err != nil {
				return fmt.Errorf("encode audit report summary: %w", err)
			}
			summaryBytes = append(summaryBytes, '\n')
			reportPath, summaryPath, err := writeAuditReportFiles(outputPath, report, summaryBytes)
			if err != nil {
				return err
			}
			if _, err := fmt.Fprintf(command.OutOrStdout(), "report written: path=%s format=%s events=%d summary=%s\n", reportPath, format, len(logs), summaryPath); err != nil {
				return fmt.Errorf("write audit report result: %w", err)
			}
			return nil
		},
	}
	flags.bind(command)
	command.Flags().StringVar(&format, "format", "csv", "report format: csv or jsonl")
	command.Flags().StringVar(&outputPath, "out", "", "report output file (must not already exist)")
	command.Flags().IntVar(&limit, "limit", auditReportDefaultLimit, "safety ceiling for matching records (1-100000)")
	return command
}

func (flags *auditFilterFlags) bind(command *cobra.Command) {
	command.Flags().StringVarP(&flags.configPath, "config", "c", defaultConfigPath, "path to the YAML configuration file")
	command.Flags().StringVar(&flags.since, "since", "", "inclusive start time in RFC3339 format")
	command.Flags().StringVar(&flags.until, "until", "", "inclusive end time in RFC3339 format")
	command.Flags().StringVar(&flags.action, "action", "", "exact audit_logs.action value")
	command.Flags().StringVar(&flags.actor, "actor", "", "exact actor_id or agent_id value")
	command.Flags().StringVar(&flags.database, "db", "", "exact audit_logs.datasource_id value")
	command.Flags().StringVar(&flags.rule, "rule", "", "literal substring in audit_logs.rule_hits")
	command.Flags().StringVar(&flags.status, "status", "", "derived result status: success or error")
	command.Flags().StringVar(&flags.errorCode, "error-code", "", "exact audit_logs.error_code value")
	command.Flags().StringVar(&flags.eventUUID, "uuid", "", "exact audit_logs.event_uuid value")
}

func (flags auditFilterFlags) filter() (model.AuditFilter, error) {
	var filter model.AuditFilter
	var err error
	if value := strings.TrimSpace(flags.since); value != "" {
		parsed, parseErr := time.Parse(time.RFC3339Nano, value)
		if parseErr != nil {
			return filter, fmt.Errorf("audit --since must be RFC3339: %w", parseErr)
		}
		filter.TimeStart = &parsed
	}
	if value := strings.TrimSpace(flags.until); value != "" {
		parsed, parseErr := time.Parse(time.RFC3339Nano, value)
		if parseErr != nil {
			return filter, fmt.Errorf("audit --until must be RFC3339: %w", parseErr)
		}
		filter.TimeEnd = &parsed
	}
	if filter.TimeStart != nil && filter.TimeEnd != nil && filter.TimeStart.After(*filter.TimeEnd) {
		return filter, errors.New("audit --since must not be after --until")
	}
	assignString := func(raw string, destination **string) {
		if value := strings.TrimSpace(raw); value != "" {
			*destination = &value
		}
	}
	assignString(flags.action, &filter.Action)
	assignString(flags.actor, &filter.Actor)
	assignString(flags.database, &filter.DatasourceID)
	assignString(flags.errorCode, &filter.ErrorCode)
	filter.RuleLike = strings.TrimSpace(flags.rule)
	if value := strings.TrimSpace(flags.eventUUID); value != "" {
		if _, err = uuid.Parse(value); err != nil {
			return filter, fmt.Errorf("audit --uuid must be a valid UUID: %w", err)
		}
		filter.EventUUID = &value
	}
	switch status := strings.ToLower(strings.TrimSpace(flags.status)); status {
	case "":
	case "error":
		filter.Decisions = []string{"error"}
	case "success":
		filter.Decisions = []string{"allow", "deny", "approve", "warn"}
	default:
		return filter, fmt.Errorf("audit --status must be success or error, got %q", flags.status)
	}
	return filter, nil
}

type auditCLIStore struct {
	database *sql.DB
	reader   *store.AuditLogReader
	target   config.ResolvedStoreTarget
}

func (opened *auditCLIStore) close() error {
	if opened == nil || opened.database == nil {
		return nil
	}
	return opened.database.Close()
}

func openAuditCLI(ctx context.Context, configPath string) (*auditCLIStore, error) {
	resolved, err := resolveConfigFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("load audit configuration: %w", err)
	}
	target := resolved.Audit
	if target.ReuseMetadata {
		target = resolved.Metadata
	}
	if target.Driver == store.DialectSQLite {
		info, statErr := os.Stat(target.SQLitePath)
		if statErr != nil {
			return nil, safeStoreError("inspect audit database", target, statErr)
		}
		if info.IsDir() {
			return nil, fmt.Errorf("inspect audit database: driver=sqlite target is not a database file")
		}
	}
	database, err := openResolvedDatabase(target)
	if err != nil {
		return nil, safeStoreError("open audit database", target, err)
	}
	if err := database.PingContext(ctx); err != nil {
		_ = database.Close()
		return nil, safeStoreError("ping audit database", target, err)
	}
	return &auditCLIStore{database: database, reader: store.NewAuditLogReader(database, target.Driver), target: target}, nil
}

func auditRecords(logs []model.AuditLog) []auditRecord {
	records := make([]auditRecord, 0, len(logs))
	for _, log := range logs {
		records = append(records, auditRecord{
			ID: log.ID, TS: log.TS.UTC(), AgentID: log.AgentID, DatasourceID: log.DatasourceID,
			SessionID: log.SessionID, ConversationID: log.ConversationID, MCPTool: log.MCPTool,
			DBType: log.DBType, SQLRaw: log.SQLRaw, SQLNorm: log.SQLNorm, StmtType: log.StmtType,
			Objects: log.Objects, Decision: log.Decision, RuleHits: log.RuleHits, RiskLevel: log.RiskLevel,
			EstRows: log.EstRows, RowsReturned: log.RowsReturned, LatencyMS: log.LatencyMS,
			ClientIP: log.ClientIP, ModelName: log.ModelName, ErrorMsg: log.ErrorMsg, ErrorCode: log.ErrorCode,
			Action: log.Action, ActorType: log.ActorType, ActorID: log.ActorID, DetailsJSON: log.DetailsJSON,
			EventUUID: log.EventUUID,
		})
	}
	return records
}

func writeAuditTable(destination io.Writer, logs []model.AuditLog) error {
	writer := tabwriter.NewWriter(destination, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(writer, "ID\tTIME (UTC)\tSTATUS\tACTION\tACTOR\tDB\tDECISION\tERROR_CODE\tUUID"); err != nil {
		return fmt.Errorf("write audit query table header: %w", err)
	}
	for _, log := range logs {
		if _, err := fmt.Fprintf(writer, "%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			log.ID, log.TS.UTC().Format(time.RFC3339Nano), auditStatus(log), auditDisplay(log.Action),
			auditDisplay(auditActor(log)), auditDisplay(log.DatasourceID), log.Decision,
			auditDisplay(log.ErrorCode), auditDisplay(log.EventUUID)); err != nil {
			return fmt.Errorf("write audit query table row: %w", err)
		}
	}
	if err := writer.Flush(); err != nil {
		return fmt.Errorf("flush audit query table: %w", err)
	}
	return nil
}

func auditDisplay(value *string) string {
	if value == nil || strings.TrimSpace(*value) == "" {
		return "-"
	}
	cleaned := strings.NewReplacer("\r", " ", "\n", " ", "\t", " ").Replace(*value)
	if len([]rune(cleaned)) > 48 {
		return string([]rune(cleaned)[:45]) + "..."
	}
	return cleaned
}

func auditStatus(log model.AuditLog) string {
	if log.Decision == "error" {
		return "error"
	}
	return "success"
}

func auditActor(log model.AuditLog) *string {
	if log.ActorID != nil && strings.TrimSpace(*log.ActorID) != "" {
		return log.ActorID
	}
	return log.AgentID
}

func collectAuditReport(ctx context.Context, reader *store.AuditLogReader, filter model.AuditFilter, limit int) ([]model.AuditLog, error) {
	const pageSize = 1000
	logs := make([]model.AuditLog, 0, min(limit, pageSize))
	for pageNumber := 1; ; pageNumber++ {
		page, err := reader.FilteredPage(ctx, filter, pageNumber, pageSize)
		if err != nil {
			return nil, err
		}
		if page.Total > int64(limit) {
			return nil, fmt.Errorf("%w: matching audit events (%d) exceed --limit %d; narrow the filters or raise --limit (maximum %d)", errAuditReportLimit, page.Total, limit, auditReportMaximumLimit)
		}
		logs = append(logs, page.List...)
		if len(logs) >= int(page.Total) || len(page.List) < pageSize {
			return logs, nil
		}
	}
}

func renderAuditReport(format string, logs []model.AuditLog) ([]byte, error) {
	var buffer bytes.Buffer
	if format == "jsonl" {
		encoder := json.NewEncoder(&buffer)
		for _, record := range auditRecords(logs) {
			if err := encoder.Encode(record); err != nil {
				return nil, fmt.Errorf("encode audit JSONL report: %w", err)
			}
		}
		return buffer.Bytes(), nil
	}
	buffer.Write([]byte{0xEF, 0xBB, 0xBF})
	writer := csv.NewWriter(&buffer)
	if err := writer.Write(auditReportColumns); err != nil {
		return nil, fmt.Errorf("write audit CSV header: %w", err)
	}
	for _, log := range logs {
		if err := writer.Write(auditCSVRecord(log)); err != nil {
			return nil, fmt.Errorf("write audit CSV row: %w", err)
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return nil, fmt.Errorf("flush audit CSV report: %w", err)
	}
	return buffer.Bytes(), nil
}

func auditCSVRecord(log model.AuditLog) []string {
	return []string{
		strconv.FormatInt(log.ID, 10), log.TS.UTC().Format(time.RFC3339Nano), auditCSVString(log.AgentID),
		auditCSVString(log.DatasourceID), auditCSVString(log.SessionID), auditCSVString(log.ConversationID),
		auditCSVString(log.MCPTool), auditCSVString(log.DBType), auditCSVString(log.SQLRaw), auditCSVString(log.SQLNorm),
		auditCSVString(log.StmtType), auditCSVString(log.Objects), auditCSVSanitize(log.Decision), auditCSVString(log.RuleHits),
		auditCSVInt(log.RiskLevel), auditCSVInt64(log.EstRows), auditCSVInt(log.RowsReturned), auditCSVInt64(log.LatencyMS),
		auditCSVString(log.ClientIP), auditCSVString(log.ModelName), auditCSVString(log.ErrorMsg), auditCSVString(log.ErrorCode),
		auditCSVString(log.Action), auditCSVString(log.ActorType), auditCSVString(log.ActorID), auditCSVString(log.DetailsJSON),
		auditCSVString(log.EventUUID),
	}
}

func auditCSVString(value *string) string {
	if value == nil {
		return ""
	}
	return auditCSVSanitize(*value)
}

func auditCSVInt(value *int) string {
	if value == nil {
		return ""
	}
	return strconv.Itoa(*value)
}

func auditCSVInt64(value *int64) string {
	if value == nil {
		return ""
	}
	return strconv.FormatInt(*value, 10)
}

func auditCSVSanitize(value string) string {
	for _, character := range value {
		switch character {
		case '\t', '\r', '\n', '=', '+', '-', '@', '\uFF1D', '\uFF0B', '\uFF0D', '\uFF20':
			return "'" + value
		}
		if !unicode.IsSpace(character) {
			break
		}
	}
	return value
}

func buildAuditReportSummary(format string, logs []model.AuditLog) auditReportSummary {
	summary := auditReportSummary{
		GeneratedAt: time.Now().UTC(), Format: format, TotalEvents: len(logs),
		ActionDistribution: make(map[string]int), StatusDistribution: make(map[string]int),
		Columns: append([]string(nil), auditReportColumns...),
	}
	errorCodes, actors := make(map[string]int), make(map[string]int)
	for index, log := range logs {
		timestamp := log.TS.UTC()
		if index == 0 || timestamp.Before(*summary.TimeRange.Since) {
			value := timestamp
			summary.TimeRange.Since = &value
		}
		if index == 0 || timestamp.After(*summary.TimeRange.Until) {
			value := timestamp
			summary.TimeRange.Until = &value
		}
		action := "<none>"
		if log.Action != nil && strings.TrimSpace(*log.Action) != "" {
			action = *log.Action
		}
		summary.ActionDistribution[action]++
		summary.StatusDistribution[auditStatus(log)]++
		if log.ErrorCode != nil && strings.TrimSpace(*log.ErrorCode) != "" {
			errorCodes[*log.ErrorCode]++
		}
		actor := "<unknown>"
		if value := auditActor(log); value != nil && strings.TrimSpace(*value) != "" {
			actor = *value
		}
		actors[actor]++
	}
	summary.TopErrorCodes = topAuditCounts(errorCodes, 10)
	summary.TopActors = topAuditCounts(actors, 10)
	return summary
}

func topAuditCounts(values map[string]int, limit int) []auditCount {
	result := make([]auditCount, 0, len(values))
	for value, count := range values {
		result = append(result, auditCount{Value: value, Count: count})
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].Count == result[right].Count {
			return result[left].Value < result[right].Value
		}
		return result[left].Count > result[right].Count
	})
	if len(result) > limit {
		result = result[:limit]
	}
	return result
}

func writeAuditReportFiles(outputPath string, report, summary []byte) (string, string, error) {
	reportPath := filepath.Clean(outputPath)
	summaryPath := reportPath + ".summary.json"
	if reportPath == summaryPath {
		return "", "", errors.New("audit report and summary paths must differ")
	}
	if err := writeNewAuditFile(reportPath, report); err != nil {
		return "", "", err
	}
	if err := writeNewAuditFile(summaryPath, summary); err != nil {
		removeErr := os.Remove(reportPath)
		return "", "", errors.Join(err, removeErr)
	}
	return reportPath, summaryPath, nil
}

func writeNewAuditFile(path string, contents []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("audit report output %q already exists", path)
		}
		return fmt.Errorf("create audit report output %q: %w", path, err)
	}
	written, writeErr := file.Write(contents)
	if writeErr == nil && written != len(contents) {
		writeErr = io.ErrShortWrite
	}
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		removeErr := os.Remove(path)
		return fmt.Errorf("write audit report output %q: %w", path, errors.Join(writeErr, closeErr, removeErr))
	}
	return nil
}

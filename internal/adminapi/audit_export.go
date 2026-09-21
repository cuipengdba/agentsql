package adminapi

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/cuipengdba/agentsql/internal/audit"
	"github.com/cuipengdba/agentsql/internal/model"
)

const (
	auditExportPageSize = 100
	auditExportRowLimit = 10000
)

var (
	errAuditExportLimit = errors.New("audit export exceeds 10000 rows")
	auditCSVHeader      = []string{
		"时间", "审计ID", "决策", "风险等级", "Agent ID", "数据源", "数据库类型", "会话ID", "对话ID", "MCP工具",
		"语句类型", "命中对象", "命中规则", "原始SQL", "归一化SQL", "预估行数", "返回行数", "耗时毫秒", "客户端IP", "模型",
		"动作", "执行者类型", "执行者ID", "错误信息", "error_code", "详情JSON",
	}
)

type exportAuditFilters struct {
	TimeStart    string `json:"time_start,omitempty"`
	TimeEnd      string `json:"time_end,omitempty"`
	AgentID      string `json:"agent_id,omitempty"`
	DatasourceID string `json:"datasource_id,omitempty"`
	SessionID    string `json:"session_id,omitempty"`
	MCPTool      string `json:"mcp_tool,omitempty"`
	Decisions    string `json:"decisions,omitempty"`
	StmtTypes    string `json:"stmt_types,omitempty"`
	RiskMin      string `json:"risk_min,omitempty"`
	RiskMax      string `json:"risk_max,omitempty"`
	Object       string `json:"object,omitempty"`
	HasKeyword   bool   `json:"has_keyword"`
}

type exportAuditDetails struct {
	Format  string             `json:"format"`
	Rows    int                `json:"rows"`
	Filters exportAuditFilters `json:"filters"`
}

func buildExportAuditFilters(filter model.AuditFilter) exportAuditFilters {
	result := exportAuditFilters{
		Decisions:  strings.Join(filter.Decisions, ","),
		StmtTypes:  strings.Join(filter.StmtTypes, ","),
		Object:     filter.ObjectLike,
		HasKeyword: strings.TrimSpace(filter.Keyword) != "",
	}
	if filter.TimeStart != nil {
		result.TimeStart = filter.TimeStart.UTC().Format(time.RFC3339)
	}
	if filter.TimeEnd != nil {
		result.TimeEnd = filter.TimeEnd.UTC().Format(time.RFC3339)
	}
	if filter.AgentID != nil && *filter.AgentID != "" {
		result.AgentID = *filter.AgentID
	}
	if filter.DatasourceID != nil && *filter.DatasourceID != "" {
		result.DatasourceID = *filter.DatasourceID
	}
	if filter.SessionID != nil && *filter.SessionID != "" {
		result.SessionID = *filter.SessionID
	}
	if filter.MCPTool != nil && *filter.MCPTool != "" {
		result.MCPTool = *filter.MCPTool
	}
	if filter.RiskMin != nil {
		result.RiskMin = strconv.Itoa(*filter.RiskMin)
	}
	if filter.RiskMax != nil {
		result.RiskMax = strconv.Itoa(*filter.RiskMax)
	}
	return result
}

func auditPeerIP(remoteAddr string) *string {
	value := strings.TrimSpace(remoteAddr)
	if value == "" {
		return nil
	}
	if host, _, err := net.SplitHostPort(value); err == nil {
		value = host
	}
	return &value
}

func normalizedAuditExportFormat(format string) string {
	if format == "" {
		return "jsonl"
	}
	return format
}

func (handler *Handler) recordAuditExportTrail(request *http.Request, format string, rows int, filter model.AuditFilter) {
	recorder := handler.deps.Runtime.ManagementAudit
	if recorder == nil {
		handler.logger.Warn().Str("action", audit.ActionAuditExport).
			Msg("audit export trail recorder unavailable")
		return
	}
	payload := exportAuditDetails{Format: format, Rows: rows, Filters: buildExportAuditFilters(filter)}
	encoded, err := json.Marshal(payload)
	if err != nil {
		handler.logger.Warn().Str("action", audit.ActionAuditExport).
			Str("error_type", fmt.Sprintf("%T", err)).
			Msg("audit export trail recording failed")
		return
	}
	action, actorType, actorID := audit.ActionAuditExport, "admin", handler.adminUser
	details := string(encoded)
	log := model.AuditLog{
		Decision:    "allow",
		Action:      &action,
		ActorType:   &actorType,
		ActorID:     &actorID,
		ClientIP:    auditPeerIP(request.RemoteAddr),
		DetailsJSON: &details,
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(request.Context()), 5*time.Second)
	defer cancel()
	if _, err := recorder.Record(ctx, log); err != nil {
		handler.logger.Warn().Str("action", audit.ActionAuditExport).
			Str("error_type", fmt.Sprintf("%T", err)).
			Msg("audit export trail recording failed")
	}
}

func (handler *Handler) collectAuditExportLogs(ctx context.Context, filter model.AuditFilter) ([]model.AuditLog, error) {
	logs := make([]model.AuditLog, 0, auditExportPageSize)
	for pageNumber := 1; ; pageNumber++ {
		page, err := handler.deps.Runtime.Store.AuditLogs().FilteredPage(ctx, filter, pageNumber, auditExportPageSize)
		if err != nil {
			return nil, err
		}
		if len(page.List) > auditExportRowLimit-len(logs) {
			return nil, errAuditExportLimit
		}
		logs = append(logs, page.List...)
		if len(page.List) < auditExportPageSize {
			return logs, nil
		}
	}
}

func sanitizeAuditCSVText(value string) string {
	for _, r := range value {
		switch r {
		case '\t', '\r', '\n':
			return "'" + value
		}
		if unicode.IsSpace(r) {
			continue
		}
		switch r {
		case '=', '+', '-', '@', '＝', '＋', '－', '＠':
			return "'" + value
		default:
			return value
		}
	}
	return value
}

func auditCSVRow(view auditView) []string {
	return []string{
		view.TS.UTC().Format(time.RFC3339Nano),
		strconv.FormatInt(view.ID, 10),
		sanitizeAuditCSVText(view.Decision),
		csvInt(view.RiskLevel),
		csvText(view.AgentID),
		csvText(view.DatasourceID),
		csvText(view.DBType),
		csvText(view.SessionID),
		csvText(view.ConversationID),
		csvText(view.MCPTool),
		csvText(view.StmtType),
		csvText(view.Objects),
		csvText(view.RuleHits),
		csvText(view.SQLRaw),
		csvText(view.SQLNorm),
		csvInt64(view.EstRows),
		csvInt(view.RowsReturned),
		csvInt64(view.LatencyMS),
		csvText(view.ClientIP),
		csvText(view.ModelName),
		csvText(view.Action),
		csvText(view.ActorType),
		csvText(view.ActorID),
		csvText(view.ErrorMsg),
		csvText(view.ErrorCode),
		csvText(view.DetailsJSON),
	}
}

func csvText(value *string) string {
	if value == nil || *value == "" {
		return ""
	}
	return sanitizeAuditCSVText(*value)
}

func csvInt(value *int) string {
	if value == nil {
		return ""
	}
	return strconv.Itoa(*value)
}

func csvInt64(value *int64) string {
	if value == nil {
		return ""
	}
	return strconv.FormatInt(*value, 10)
}

func renderAuditCSV(logs []model.AuditLog) ([]byte, error) {
	var buffer bytes.Buffer
	buffer.Write([]byte{0xEF, 0xBB, 0xBF})
	writer := csv.NewWriter(&buffer)
	if err := writer.Write(auditCSVHeader); err != nil {
		return nil, err
	}
	for _, log := range logs {
		if err := writer.Write(auditCSVRow(auditToView(log))); err != nil {
			return nil, err
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

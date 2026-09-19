package adminapi

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"strconv"
	"time"
	"unicode"

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
		"动作", "执行者类型", "执行者ID", "错误信息", "详情JSON",
	}
)

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

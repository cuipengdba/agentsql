package audit

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
)

const (
	exportPageSize = 500
	exportRowLimit = 100_000
)

// ExportJSONL streams filtered audit events as one JSON object per line.
func (service *Service) ExportJSONL(
	ctx context.Context,
	filter model.AuditFilter,
	writer io.Writer,
) (rows int, err error) {
	if ctx == nil {
		return 0, fmt.Errorf("export audit logs: context is required")
	}
	if isNilInterface(writer) {
		return 0, fmt.Errorf("export audit logs: writer is required")
	}
	if service == nil || isNilInterface(service.reader) {
		return 0, fmt.Errorf("export audit logs: reader is required")
	}

	buffered := bufio.NewWriter(writer)
	defer func() {
		if flushError := buffered.Flush(); flushError != nil {
			if err == nil {
				err = fmt.Errorf("flush audit JSONL: %w", flushError)
			} else {
				err = errors.Join(err, fmt.Errorf("flush audit JSONL: %w", flushError))
			}
		}
	}()

	for pageNumber := 1; ; pageNumber++ {
		if contextError := ctx.Err(); contextError != nil {
			return rows, contextError
		}
		page, pageError := service.reader.FilteredPage(
			ctx,
			filter,
			pageNumber,
			exportPageSize,
		)
		if pageError != nil {
			return rows, pageError
		}
		if page.Total > exportRowLimit {
			return rows, ErrExportLimit
		}
		if len(page.List) == 0 {
			return rows, nil
		}
		for _, log := range page.List {
			if contextError := ctx.Err(); contextError != nil {
				return rows, contextError
			}
			if rows >= exportRowLimit {
				return rows, ErrExportLimit
			}
			encoded, marshalError := json.Marshal(newAuditJSONLine(log))
			if marshalError != nil {
				return rows, fmt.Errorf("marshal audit JSONL row: %w", marshalError)
			}
			if _, writeError := buffered.Write(encoded); writeError != nil {
				return rows, fmt.Errorf("write audit JSONL row: %w", writeError)
			}
			if writeError := buffered.WriteByte('\n'); writeError != nil {
				return rows, fmt.Errorf("write audit JSONL newline: %w", writeError)
			}
			rows++
		}
		if int64(rows) >= page.Total || len(page.List) < exportPageSize {
			return rows, nil
		}
	}
}

type auditJSONLine struct {
	ID             int64   `json:"ID"`
	TS             string  `json:"TS"`
	AgentID        *string `json:"AgentID,omitempty"`
	DatasourceID   *string `json:"DatasourceID,omitempty"`
	SessionID      *string `json:"SessionID,omitempty"`
	ConversationID *string `json:"ConversationID,omitempty"`
	MCPTool        *string `json:"MCPTool,omitempty"`
	DBType         *string `json:"DBType,omitempty"`
	SQLRaw         *string `json:"SQLRaw,omitempty"`
	SQLNorm        *string `json:"SQLNorm,omitempty"`
	StmtType       *string `json:"StmtType,omitempty"`
	Objects        *string `json:"Objects,omitempty"`
	Decision       string  `json:"Decision"`
	RuleHits       *string `json:"RuleHits,omitempty"`
	RiskLevel      *int    `json:"RiskLevel,omitempty"`
	EstRows        *int64  `json:"EstRows,omitempty"`
	RowsReturned   *int    `json:"RowsReturned,omitempty"`
	LatencyMS      *int64  `json:"LatencyMS,omitempty"`
	ClientIP       *string `json:"ClientIP,omitempty"`
	ModelName      *string `json:"ModelName,omitempty"`
	ErrorMsg       *string `json:"ErrorMsg,omitempty"`
	ErrorCode      *string `json:"error_code,omitempty"`
	Action         *string `json:"Action,omitempty"`
	ActorType      *string `json:"ActorType,omitempty"`
	ActorID        *string `json:"ActorID,omitempty"`
	DetailsJSON    *string `json:"DetailsJSON,omitempty"`
}

func newAuditJSONLine(log model.AuditLog) auditJSONLine {
	return auditJSONLine{
		ID:             log.ID,
		TS:             log.TS.Format(time.RFC3339),
		AgentID:        log.AgentID,
		DatasourceID:   log.DatasourceID,
		SessionID:      log.SessionID,
		ConversationID: log.ConversationID,
		MCPTool:        log.MCPTool,
		DBType:         log.DBType,
		SQLRaw:         log.SQLRaw,
		SQLNorm:        log.SQLNorm,
		StmtType:       log.StmtType,
		Objects:        log.Objects,
		Decision:       log.Decision,
		RuleHits:       log.RuleHits,
		RiskLevel:      log.RiskLevel,
		EstRows:        log.EstRows,
		RowsReturned:   log.RowsReturned,
		LatencyMS:      log.LatencyMS,
		ClientIP:       log.ClientIP,
		ModelName:      log.ModelName,
		ErrorMsg:       log.ErrorMsg,
		ErrorCode:      log.ErrorCode,
		Action:         log.Action,
		ActorType:      log.ActorType,
		ActorID:        log.ActorID,
		DetailsJSON:    log.DetailsJSON,
	}
}

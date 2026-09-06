package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/cuipengdba/agentsql/internal/model"
)

var (
	// ErrInvalidPage indicates that a page number is less than one.
	ErrInvalidPage = errors.New("page must be at least 1")
	// ErrInvalidPageSize indicates that a page size is outside the safe range.
	ErrInvalidPageSize = errors.New("page_size must be between 1 and 1000")
)

// AuditPage is one immutable audit-log result page.
type AuditPage struct {
	Total    int64
	Page     int
	PageSize int
	List     []model.AuditLog
}

// AuditLogRepository provides append and paginated read operations only.
type AuditLogRepository struct {
	db *sql.DB
}

// Insert appends an audit log and returns the record with generated ID and timestamp.
func (repository *AuditLogRepository) Insert(ctx context.Context, auditLog model.AuditLog) (model.AuditLog, error) {
	result, err := repository.db.ExecContext(ctx, `
INSERT INTO audit_logs (
  agent_id, datasource_id, session_id, conversation_id, mcp_tool, db_type,
  sql_raw, sql_norm, stmt_type, objects, decision, rule_hits, risk_level,
  est_rows, rows_returned, latency_ms, client_ip, model_name, error_msg
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		optionalString(auditLog.AgentID),
		optionalString(auditLog.DatasourceID),
		optionalString(auditLog.SessionID),
		optionalString(auditLog.ConversationID),
		optionalString(auditLog.MCPTool),
		optionalString(auditLog.DBType),
		optionalString(auditLog.SQLRaw),
		optionalString(auditLog.SQLNorm),
		optionalString(auditLog.StmtType),
		optionalString(auditLog.Objects),
		auditLog.Decision,
		optionalString(auditLog.RuleHits),
		optionalInt(auditLog.RiskLevel),
		optionalInt64(auditLog.EstRows),
		optionalInt(auditLog.RowsReturned),
		optionalInt64(auditLog.LatencyMS),
		optionalString(auditLog.ClientIP),
		optionalString(auditLog.ModelName),
		optionalString(auditLog.ErrorMsg),
	)
	if err != nil {
		return model.AuditLog{}, fmt.Errorf("insert audit log: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return model.AuditLog{}, fmt.Errorf("read inserted audit log ID: %w", err)
	}
	inserted, err := repository.getInserted(ctx, id)
	if err != nil {
		return model.AuditLog{}, fmt.Errorf("read inserted audit log %d: %w", id, err)
	}
	return inserted, nil
}

// Page returns audit logs ordered newest first.
func (repository *AuditLogRepository) Page(ctx context.Context, page, pageSize int) (AuditPage, error) {
	if page < 1 {
		return AuditPage{}, fmt.Errorf("page audit logs: %w", ErrInvalidPage)
	}
	if pageSize < 1 || pageSize > 1000 {
		return AuditPage{}, fmt.Errorf("page audit logs: %w", ErrInvalidPageSize)
	}

	var total int64
	if err := repository.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM audit_logs").Scan(&total); err != nil {
		return AuditPage{}, fmt.Errorf("count audit logs: %w", err)
	}

	rows, err := repository.db.QueryContext(ctx, `
SELECT id, ts, agent_id, datasource_id, session_id, conversation_id, mcp_tool,
       db_type, sql_raw, sql_norm, stmt_type, objects, decision, rule_hits,
       risk_level, est_rows, rows_returned, latency_ms, client_ip, model_name,
       error_msg
FROM audit_logs
ORDER BY ts DESC, id DESC
LIMIT ? OFFSET ?`, pageSize, (page-1)*pageSize)
	if err != nil {
		return AuditPage{}, fmt.Errorf("query audit log page %d: %w", page, err)
	}

	list := make([]model.AuditLog, 0, pageSize)
	for rows.Next() {
		auditLog, err := scanAuditLog(rows)
		if err != nil {
			return AuditPage{}, fmt.Errorf("scan audit log page %d: %w", page, closeRowsAfterError(rows, err))
		}
		list = append(list, auditLog)
	}
	iterationError := rows.Err()
	closeError := rows.Close()
	if iterationError != nil || closeError != nil {
		return AuditPage{}, fmt.Errorf(
			"finish audit log page %d: %w",
			page,
			errors.Join(iterationError, closeError),
		)
	}

	return AuditPage{Total: total, Page: page, PageSize: pageSize, List: list}, nil
}

func (repository *AuditLogRepository) getInserted(ctx context.Context, id int64) (model.AuditLog, error) {
	auditLog, err := scanAuditLog(repository.db.QueryRowContext(ctx, `
SELECT id, ts, agent_id, datasource_id, session_id, conversation_id, mcp_tool,
       db_type, sql_raw, sql_norm, stmt_type, objects, decision, rule_hits,
       risk_level, est_rows, rows_returned, latency_ms, client_ip, model_name,
       error_msg
FROM audit_logs
WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return model.AuditLog{}, fmt.Errorf("get inserted audit log %d: %w", id, errors.Join(ErrNotFound, err))
	}
	if err != nil {
		return model.AuditLog{}, fmt.Errorf("get inserted audit log %d: %w", id, err)
	}
	return auditLog, nil
}

func scanAuditLog(scanner rowScanner) (model.AuditLog, error) {
	var auditLog model.AuditLog
	var timestamp databaseTimestamp
	var agentID, datasourceID, sessionID, conversationID sql.NullString
	var mcpTool, databaseType, sqlRaw, sqlNormalized, statementType sql.NullString
	var objects, ruleHits, clientIP, modelName, errorMessage sql.NullString
	var riskLevel, estimatedRows, rowsReturned, latencyMS sql.NullInt64
	if err := scanner.Scan(
		&auditLog.ID,
		&timestamp,
		&agentID,
		&datasourceID,
		&sessionID,
		&conversationID,
		&mcpTool,
		&databaseType,
		&sqlRaw,
		&sqlNormalized,
		&statementType,
		&objects,
		&auditLog.Decision,
		&ruleHits,
		&riskLevel,
		&estimatedRows,
		&rowsReturned,
		&latencyMS,
		&clientIP,
		&modelName,
		&errorMessage,
	); err != nil {
		return model.AuditLog{}, fmt.Errorf("scan audit log: %w", err)
	}

	auditLog.AgentID = stringPointer(agentID)
	auditLog.DatasourceID = stringPointer(datasourceID)
	auditLog.SessionID = stringPointer(sessionID)
	auditLog.ConversationID = stringPointer(conversationID)
	auditLog.MCPTool = stringPointer(mcpTool)
	auditLog.DBType = stringPointer(databaseType)
	auditLog.SQLRaw = stringPointer(sqlRaw)
	auditLog.SQLNorm = stringPointer(sqlNormalized)
	auditLog.StmtType = stringPointer(statementType)
	auditLog.Objects = stringPointer(objects)
	auditLog.RuleHits = stringPointer(ruleHits)
	auditLog.RiskLevel = intPointer(riskLevel)
	auditLog.EstRows = int64Pointer(estimatedRows)
	auditLog.RowsReturned = intPointer(rowsReturned)
	auditLog.LatencyMS = int64Pointer(latencyMS)
	auditLog.ClientIP = stringPointer(clientIP)
	auditLog.ModelName = stringPointer(modelName)
	auditLog.ErrorMsg = stringPointer(errorMessage)
	var err error
	auditLog.TS, err = timestamp.required("audit_logs.ts")
	if err != nil {
		return model.AuditLog{}, fmt.Errorf("scan audit log: %w", err)
	}
	return auditLog, nil
}

func closeRowsAfterError(rows *sql.Rows, cause error) error {
	if err := rows.Close(); err != nil {
		return errors.Join(cause, fmt.Errorf("close audit log rows: %w", err))
	}
	return cause
}

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

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

type auditLogExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// Insert appends an audit log and returns the record with generated ID and timestamp.
func (repository *AuditLogRepository) Insert(ctx context.Context, auditLog model.AuditLog) (model.AuditLog, error) {
	if repository == nil || repository.db == nil {
		return model.AuditLog{}, fmt.Errorf("insert audit log: repository is not initialized")
	}
	if err := validateAuditLogInsert(ctx, auditLog); err != nil {
		return model.AuditLog{}, err
	}
	return insertAuditLog(ctx, repository.db, auditLog)
}

func insertAuditLog(
	ctx context.Context,
	executor auditLogExecutor,
	auditLog model.AuditLog,
) (model.AuditLog, error) {
	result, err := executor.ExecContext(ctx, `
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
	if id <= 0 {
		return model.AuditLog{}, fmt.Errorf("read inserted audit log ID: invalid ID %d", id)
	}
	inserted, err := getInsertedAuditLog(ctx, executor, id)
	if err != nil {
		return model.AuditLog{}, fmt.Errorf("read inserted audit log %d: %w", id, err)
	}
	return inserted, nil
}

func validateAuditLogInsert(ctx context.Context, auditLog model.AuditLog) error {
	if ctx == nil {
		return fmt.Errorf("insert audit log: %w", ErrNilContext)
	}
	switch auditLog.Decision {
	case "allow", "deny", "approve", "warn", "error":
		return nil
	default:
		return fmt.Errorf("insert audit log: invalid decision %q", auditLog.Decision)
	}
}

// Page returns audit logs ordered newest first.
func (repository *AuditLogRepository) Page(ctx context.Context, page, pageSize int) (AuditPage, error) {
	return repository.FilteredPage(ctx, model.AuditFilter{}, page, pageSize)
}

// FilteredPage returns matching audit logs ordered newest first.
func (repository *AuditLogRepository) FilteredPage(
	ctx context.Context,
	filter model.AuditFilter,
	page int,
	pageSize int,
) (AuditPage, error) {
	if repository == nil || repository.db == nil {
		return AuditPage{}, fmt.Errorf("page audit logs: repository is not initialized")
	}
	if ctx == nil {
		return AuditPage{}, fmt.Errorf("page audit logs: %w", ErrNilContext)
	}
	if page < 1 {
		return AuditPage{}, fmt.Errorf("page audit logs: %w", ErrInvalidPage)
	}
	if pageSize < 1 || pageSize > 1000 {
		return AuditPage{}, fmt.Errorf("page audit logs: %w", ErrInvalidPageSize)
	}

	whereClause, filterArgs := buildAuditWhere(filter)
	var total int64
	if err := repository.db.QueryRowContext(
		ctx,
		"SELECT COUNT(*) FROM audit_logs"+whereClause,
		filterArgs...,
	).Scan(&total); err != nil {
		return AuditPage{}, fmt.Errorf("count audit logs: %w", err)
	}

	selectQuery := `
SELECT id, ts, agent_id, datasource_id, session_id, conversation_id, mcp_tool,
       db_type, sql_raw, sql_norm, stmt_type, objects, decision, rule_hits,
       risk_level, est_rows, rows_returned, latency_ms, client_ip, model_name,
       error_msg
FROM audit_logs` + whereClause + `
ORDER BY ts DESC, id DESC
LIMIT ? OFFSET ?`
	selectArgs := make([]any, 0, len(filterArgs)+2)
	selectArgs = append(selectArgs, filterArgs...)
	selectArgs = append(selectArgs, pageSize, int64(page-1)*int64(pageSize))
	rows, err := repository.db.QueryContext(ctx, selectQuery, selectArgs...)
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

func buildAuditWhere(filter model.AuditFilter) (clause string, args []any) {
	conditions := make([]string, 0, 12)
	args = make([]any, 0, 16)
	appendCondition := func(condition string, values ...any) {
		conditions = append(conditions, condition)
		args = append(args, values...)
	}
	if filter.TimeStart != nil {
		appendCondition("ts >= ?", *filter.TimeStart)
	}
	if filter.TimeEnd != nil {
		appendCondition("ts <= ?", *filter.TimeEnd)
	}
	if filter.AgentID != nil {
		appendCondition("agent_id = ?", *filter.AgentID)
	}
	if filter.DatasourceID != nil {
		appendCondition("datasource_id = ?", *filter.DatasourceID)
	}
	if filter.SessionID != nil {
		appendCondition("session_id = ?", *filter.SessionID)
	}
	if filter.MCPTool != nil {
		appendCondition("mcp_tool = ?", *filter.MCPTool)
	}
	if len(filter.Decisions) > 0 {
		appendCondition("decision IN ("+auditPlaceholders(len(filter.Decisions))+")", stringsToAny(filter.Decisions)...)
	}
	if len(filter.StmtTypes) > 0 {
		appendCondition("stmt_type IN ("+auditPlaceholders(len(filter.StmtTypes))+")", stringsToAny(filter.StmtTypes)...)
	}
	if filter.RiskMin != nil {
		appendCondition("risk_level >= ?", *filter.RiskMin)
	}
	if filter.RiskMax != nil {
		appendCondition("risk_level <= ?", *filter.RiskMax)
	}
	if filter.Keyword != "" {
		like := auditLikeArgument(filter.Keyword)
		appendCondition("(sql_raw LIKE ? ESCAPE '!' OR sql_norm LIKE ? ESCAPE '!')", like, like)
	}
	if filter.ObjectLike != "" {
		appendCondition("objects LIKE ? ESCAPE '!'", auditLikeArgument(filter.ObjectLike))
	}
	if len(conditions) == 0 {
		return "", args
	}
	return " WHERE 1=1 AND " + strings.Join(conditions, " AND "), args
}

func auditPlaceholders(count int) string {
	return strings.TrimSuffix(strings.Repeat("?,", count), ",")
}

func stringsToAny(values []string) []any {
	arguments := make([]any, len(values))
	for index, value := range values {
		arguments[index] = value
	}
	return arguments
}

func auditLikeArgument(value string) string {
	escaped := strings.NewReplacer(
		"!", "!!",
		"%", "!%",
		"_", "!_",
	).Replace(value)
	return "%" + escaped + "%"
}

func (repository *AuditLogRepository) getInserted(ctx context.Context, id int64) (model.AuditLog, error) {
	return getInsertedAuditLog(ctx, repository.db, id)
}

func getInsertedAuditLog(ctx context.Context, executor auditLogExecutor, id int64) (model.AuditLog, error) {
	auditLog, err := scanAuditLog(executor.QueryRowContext(ctx, `
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

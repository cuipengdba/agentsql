package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/cuipengdba/agentsql/internal/bootstrap"
	"github.com/cuipengdba/agentsql/internal/executor"
	"github.com/cuipengdba/agentsql/internal/mask"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/parser"
	"github.com/cuipengdba/agentsql/internal/pipeline"
	"github.com/cuipengdba/agentsql/internal/policy"
	"github.com/rs/zerolog"
)

const schemaRowLimit = 10_000

type ToolResponse struct {
	Decision   string `json:"decision"`
	ErrorCode  string `json:"error_code,omitempty"`
	Reason     string `json:"reason"`
	Suggestion string `json:"suggestion"`
	Data       any    `json:"data,omitempty"`
}

type publicDatasource struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	DBType string `json:"db_type"`
}

type publicSchemaColumn struct {
	Schema string `json:"schema"`
	Table  string `json:"table"`
	Column string `json:"column"`
}

type pipelineData struct {
	Result      *model.QueryResult `json:"result,omitempty"`
	Hits        []model.RuleHit    `json:"hits,omitempty"`
	EstScanRows int64              `json:"est_scan_rows,omitempty"`
	ApprovalID  string             `json:"approval_id,omitempty"`
	AuditID     int64              `json:"audit_id,omitempty"`
	Redact      mask.RedactReport  `json:"redact,omitempty"`
	Status      string             `json:"status,omitempty"`
}

type approvalData struct {
	ID        string     `json:"id"`
	Status    string     `json:"status"`
	Approver  *string    `json:"approver,omitempty"`
	DecidedAt *time.Time `json:"decided_at,omitempty"`
}

type toolHandlers struct {
	runtime     *bootstrap.Runtime
	agent       model.Agent
	apiKey      string
	logger      zerolog.Logger
	executorFor func(model.Datasource) (executor.Executor, error)
}

func (handlers *toolHandlers) listDatasources(ctx context.Context) ToolResponse {
	allowed, response := handlers.authorizedDatasources(ctx)
	if response != nil {
		return *response
	}
	data := make([]publicDatasource, 0, len(allowed))
	for _, datasource := range allowed {
		data = append(data, publicDatasource{ID: datasource.ID, Name: datasource.Name, DBType: datasource.DBType})
	}
	return allowToolResponse("已返回当前 Agent 获授权的数据源", data)
}

func (handlers *toolHandlers) listSchema(
	ctx context.Context,
	datasourceID string,
	table string,
) ToolResponse {
	datasource, response := handlers.authorizedDatasource(ctx, datasourceID)
	if response != nil {
		return *response
	}
	query, err := buildSchemaQuery(datasource.DBType, table)
	if err != nil {
		return denyToolResponse("table 必须是安全的表名或 schema.table", "请仅使用字母、数字、下划线和一个可选的点")
	}
	storedPolicies, err := handlers.runtime.Store.Policies().ListByAgentAndDatasource(
		ctx,
		handlers.agent.ID,
		datasource.ID,
	)
	if err != nil {
		return handlers.internalError("list_schema", err)
	}
	decision, err := policy.NewResolver().Resolve(storedPolicies, handlers.agent.Level)
	if err != nil {
		return handlers.internalError("list_schema", err)
	}
	databaseExecutor, err := handlers.getExecutor(datasource)
	if err != nil {
		return handlers.internalError("list_schema", err)
	}
	if isNilMCPDependency(databaseExecutor) {
		return handlers.internalError("list_schema", fmt.Errorf("executor lookup returned nil"))
	}
	result, err := databaseExecutor.Query(ctx, query, schemaRowLimit)
	if err != nil {
		var databaseError *executor.DBError
		if errors.As(err, &databaseError) {
			return databaseErrorToolResponse(databaseError)
		}
		return handlers.internalError("list_schema", err)
	}
	if result.Truncated {
		return errorToolResponse("数据源结构超过安全返回上限", "请指定 table 缩小结构查询范围")
	}
	columns, err := schemaColumnsFromResult(result)
	if err != nil {
		return handlers.internalError("list_schema", err)
	}
	filtered, err := policy.FilterColumnsForSchema(columns, decision)
	if err != nil {
		return handlers.internalError("list_schema", err)
	}
	data := make([]publicSchemaColumn, 0, len(filtered))
	for _, column := range filtered {
		data = append(data, publicSchemaColumn{
			Schema: column.Schema,
			Table:  column.Table,
			Column: column.Column,
		})
	}
	return allowToolResponse("已按 Agent 的表级和列级权限裁剪结构", data)
}

func (handlers *toolHandlers) getExecutor(datasource model.Datasource) (executor.Executor, error) {
	if handlers.executorFor != nil {
		return handlers.executorFor(datasource)
	}
	return handlers.runtime.ExecutorFor(datasource)
}

func (handlers *toolHandlers) explainQuery(
	ctx context.Context,
	datasourceID string,
	sql string,
) ToolResponse {
	if strings.TrimSpace(sql) == "" {
		return errorToolResponse("sql 不能为空", "请提供一条需要评估的 SQL")
	}
	return handlers.process(ctx, pipeline.Request{
		APIKey: handlers.apiKey, DatasourceID: datasourceID, SQL: sql,
		MCPTool: "explain_query", ExplainOnly: true,
	})
}

func (handlers *toolHandlers) query(
	ctx context.Context,
	datasourceID string,
	sql string,
) ToolResponse {
	datasource, response := handlers.authorizedDatasource(ctx, datasourceID)
	if response != nil {
		return *response
	}
	ast, response := parseForTool(datasource.DBType, sql)
	if response != nil {
		return *response
	}
	if ast.StmtType != model.StmtType("SELECT") {
		return denyToolResponse("query 仅用于 SELECT 查询", "请将写操作改用 execute_write，并补充 reason")
	}
	return handlers.process(ctx, pipeline.Request{
		APIKey: handlers.apiKey, DatasourceID: datasourceID, SQL: sql, MCPTool: "query",
	})
}

func (handlers *toolHandlers) executeWrite(
	ctx context.Context,
	datasourceID string,
	sql string,
	reason string,
) ToolResponse {
	if strings.TrimSpace(reason) == "" {
		return errorToolResponse("reason 不能为空", "请说明写操作目的、影响范围和回滚方案后重试")
	}
	datasource, response := handlers.authorizedDatasource(ctx, datasourceID)
	if response != nil {
		return *response
	}
	ast, response := parseForTool(datasource.DBType, sql)
	if response != nil {
		return *response
	}
	switch ast.StmtType {
	case model.StmtType("INSERT"), model.StmtType("UPDATE"), model.StmtType("DELETE"), model.StmtType("DDL"):
	default:
		return denyToolResponse("execute_write 只接受 INSERT、UPDATE、DELETE 或 DDL", "SELECT 请改用 query；管理命令请交由 DBA")
	}
	if ast.StmtType == model.StmtType("DDL") && handlers.agent.Level != "ddl" {
		return denyToolResponse("当前 Agent 没有 DDL 权限", "请改用具备 ddl 级别的 Agent，或提交 request_approval")
	}
	handlers.logger.Info().
		Str("tool", "execute_write").
		Str("agent_id", handlers.agent.ID).
		Str("datasource_id", datasourceID).
		Str("reason", reason).
		Msg("MCP write request")
	return handlers.process(ctx, pipeline.Request{
		APIKey: handlers.apiKey, DatasourceID: datasourceID, SQL: sql, MCPTool: "execute_write",
	})
}

func (handlers *toolHandlers) requestApproval(
	ctx context.Context,
	datasourceID string,
	sql string,
	reason string,
) ToolResponse {
	if strings.TrimSpace(reason) == "" {
		return errorToolResponse("reason 不能为空", "请说明审批原因、预计影响和回滚方案后重试")
	}
	handlers.logger.Info().
		Str("tool", "request_approval").
		Str("agent_id", handlers.agent.ID).
		Str("datasource_id", datasourceID).
		Str("reason", reason).
		Msg("MCP approval request")
	return handlers.process(ctx, pipeline.Request{
		APIKey: handlers.apiKey, DatasourceID: datasourceID, SQL: sql,
		MCPTool: "request_approval", RequireApproval: true,
	})
}

func (handlers *toolHandlers) getApprovalResult(ctx context.Context, approvalID string) ToolResponse {
	if strings.TrimSpace(approvalID) == "" {
		return errorToolResponse("approval_id 不能为空", "请提供 request_approval 返回的审批单号")
	}
	approval, err := handlers.runtime.Store.Approvals().Get(ctx, approvalID)
	if err != nil || approval.AgentID == nil || *approval.AgentID != handlers.agent.ID {
		return denyToolResponse("未找到可访问的审批单", "请检查审批单号，并确认它属于当前 Agent")
	}
	return allowToolResponse("已返回审批单当前状态", approvalData{
		ID: approval.ID, Status: approval.Status, Approver: approval.Approver, DecidedAt: approval.DecidedAt,
	})
}

func (handlers *toolHandlers) process(ctx context.Context, request pipeline.Request) ToolResponse {
	response, err := handlers.runtime.Pipeline.Process(ctx, request)
	if err != nil {
		return handlers.internalError(request.MCPTool, err)
	}
	if response.Decision == model.DecisionError {
		reason := response.ErrorMessage
		if reason == "" {
			reason = response.Assessment.Reason
		}
		return ToolResponse{
			Decision: string(model.DecisionError), ErrorCode: response.ErrorCode,
			Reason: reason, Suggestion: response.Suggestion,
		}
	}
	suggestion := response.Assessment.Suggestion
	if suggestion == "" {
		suggestion = "当前请求无需改写；请继续遵守最小权限和有界查询原则"
	}
	data := pipelineData{
		Result: response.Result, Hits: response.Assessment.Hits,
		EstScanRows: response.Assessment.EstScanRows, ApprovalID: response.ApprovalID,
		AuditID: response.AuditID, Redact: response.Redact,
	}
	if response.ApprovalID != "" {
		data.Status = "pending"
	}
	return ToolResponse{
		Decision: string(response.Decision), Reason: response.Assessment.Reason,
		Suggestion: suggestion, Data: data,
	}
}

func (handlers *toolHandlers) authorizedDatasources(
	ctx context.Context,
) ([]model.Datasource, *ToolResponse) {
	policies, err := handlers.runtime.Store.Policies().ListByAgent(ctx, handlers.agent.ID)
	if err != nil {
		response := handlers.internalError("list_datasources", err)
		return nil, &response
	}
	allowedIDs := make(map[string]struct{})
	for _, stored := range policies {
		if stored.Action == "allow" {
			allowedIDs[stored.DatasourceID] = struct{}{}
		}
	}
	datasources, err := handlers.runtime.Store.Datasources().List(ctx)
	if err != nil {
		response := handlers.internalError("list_datasources", err)
		return nil, &response
	}
	allowed := make([]model.Datasource, 0, len(allowedIDs))
	for _, datasource := range datasources {
		if _, exists := allowedIDs[datasource.ID]; exists {
			allowed = append(allowed, datasource)
		}
	}
	return allowed, nil
}

func (handlers *toolHandlers) authorizedDatasource(
	ctx context.Context,
	datasourceID string,
) (model.Datasource, *ToolResponse) {
	if strings.TrimSpace(datasourceID) == "" {
		response := errorToolResponse("datasource_id 不能为空", "请先调用 list_datasources 选择授权数据源")
		return model.Datasource{}, &response
	}
	datasources, response := handlers.authorizedDatasources(ctx)
	if response != nil {
		return model.Datasource{}, response
	}
	for _, datasource := range datasources {
		if datasource.ID == datasourceID {
			return datasource, nil
		}
	}
	denied := denyToolResponse("当前 Agent 无权访问该数据源", "请先调用 list_datasources，并仅使用返回的数据源 ID")
	return model.Datasource{}, &denied
}

func parseForTool(dialect, sql string) (*model.AST, *ToolResponse) {
	if strings.TrimSpace(sql) == "" {
		response := errorToolResponse("sql 不能为空", "请提供一条完整且单一的 SQL")
		return nil, &response
	}
	sqlParser, err := parser.NewParser(model.DBDialect(dialect))
	if err != nil {
		response := errorToolResponse("数据源方言不受支持", "请选择 PostgreSQL 或 MySQL 数据源")
		return nil, &response
	}
	ast, err := sqlParser.Parse(sql)
	if err != nil {
		response := errorToolResponse("SQL 无法安全解析", "请改写为一条语法完整、无堆叠语句的 SQL")
		return nil, &response
	}
	return ast, nil
}

func (handlers *toolHandlers) internalError(tool string, err error) ToolResponse {
	var databaseError *executor.DBError
	if errors.As(err, &databaseError) {
		return databaseErrorToolResponse(databaseError)
	}
	handlers.logger.Error().
		Str("tool", tool).
		Str("error_type", fmt.Sprintf("%T", err)).
		Msg("MCP tool failed")
	return errorToolResponse("AgentSQL 内部处理失败", "请稍后重试；若持续失败，请联系管理员并提供工具名")
}

func databaseErrorToolResponse(err *executor.DBError) ToolResponse {
	return ToolResponse{
		Decision: "error", ErrorCode: string(err.Code), Reason: err.Error(),
		Suggestion: executor.Suggestion(err.Code),
	}
}

func allowToolResponse(reason string, data any) ToolResponse {
	return ToolResponse{
		Decision: "allow", Reason: reason,
		Suggestion: "无需改写；请继续遵守最小权限原则", Data: data,
	}
}

func denyToolResponse(reason, suggestion string) ToolResponse {
	return ToolResponse{Decision: "deny", Reason: reason, Suggestion: suggestion}
}

func errorToolResponse(reason, suggestion string) ToolResponse {
	return ToolResponse{Decision: "error", Reason: reason, Suggestion: suggestion}
}

func isNilMCPDependency(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

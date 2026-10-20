package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"time"
	"unicode/utf8"

	"github.com/cuipengdba/agentsql/internal/auth"
	executor "github.com/cuipengdba/agentsql/internal/authorizedexecute"
	"github.com/cuipengdba/agentsql/internal/mask"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/policy"
	"github.com/cuipengdba/agentsql/internal/store"
)

func (handlers *toolHandlers) queryAnalytics(ctx context.Context, input analyticsInput) ToolResponse {
	const tool = "query_analytics"
	datasource, response := handlers.authorizedDatasource(ctx, input.DatasourceID)
	if response != nil {
		return handlers.auditDatasourcePreflightDenial(ctx, input.DatasourceID, tool, *response)
	}
	if datasource.DBType != "clickhouse" || datasource.Database == "" ||
		(datasource.TLSMode != "strict" && datasource.TLSMode != "verify-full") ||
		datasource.TrustServerCertificate || datasource.TLSCAFile == "" ||
		datasource.Username == "" || datasource.PasswordEnc == "" {
		return handlers.analyticsDeny(ctx, datasource, "ClickHouse 数据源缺少受控查询所需的严格 TLS、数据库或认证账号")
	}
	if _, delegated := delegatedHumanFromContext(ctx); delegated || handlers.agent.TaskGrantID != "" {
		return handlers.analyticsDeny(ctx, datasource, "当前身份模式未开放 ClickHouse 查询")
	}
	current, err := auth.NewAuthenticator(handlers.runtime.Store.Agents()).Authenticate(ctx, handlers.apiKey)
	if err != nil || current.ID != handlers.agent.ID || current.TenantID != handlers.agent.TenantID || current.Status != "active" {
		return handlers.analyticsDeny(ctx, datasource, "当前 Agent 身份已失效")
	}
	request := executor.ClickHouseReadRequest{Table: input.Table, Fields: input.Fields, Filter: input.Filter, Limit: input.Limit}
	if !validSchemaIdentifier(datasource.Database) || executor.ValidateClickHouseRead(request) != nil {
		return handlers.analyticsDeny(ctx, datasource, "ClickHouse 查询参数不符合受控范围")
	}
	policies, err := handlers.runtime.Store.Policies().ListByAgentAndDatasource(ctx, handlers.agent.ID, datasource.ID)
	if err != nil {
		return handlers.analyticsAuditUnavailable()
	}
	decision, err := policy.NewResolver().Resolve(policies, current.Level)
	if err != nil {
		return handlers.analyticsAuditUnavailable()
	}
	object := datasource.Database + "." + request.Table
	table := model.ObjectRef{Schema: datasource.Database, Table: request.Table}
	exactGrant := false
	for _, stored := range policies {
		if stored.RowFilter != nil || stored.LegacyUnrepresentable || stored.RelationBindingID != nil ||
			(stored.ObjectType != "column" && stored.Columns != nil) {
			return handlers.analyticsDeny(ctx, datasource, "ClickHouse 受控查询无法安全执行当前行过滤或绑定策略")
		}
		if stored.Action == "allow" && stored.ObjectType == "table" && stored.ObjectName == object {
			exactGrant = true
		}
	}
	if !exactGrant || policy.MatchesAnyTable(decision.DeniedTables, table) ||
		!policy.MatchesAnyTable(decision.AllowedTables, table) {
		return handlers.analyticsDeny(ctx, datasource, "当前 Agent 没有此 ClickHouse 表的精确授权")
	}
	allowedFields := decision.ColumnACL[object]
	if len(allowedFields) == 0 {
		return handlers.analyticsDeny(ctx, datasource, "ClickHouse 查询必须配置显式列授权")
	}
	for _, field := range request.Fields {
		if !mongoFieldGranted(allowedFields, field) {
			return handlers.analyticsDeny(ctx, datasource, "当前 Agent 没有请求字段的授权")
		}
	}
	for field := range request.Filter {
		if !mongoFieldGranted(allowedFields, field) {
			return handlers.analyticsDeny(ctx, datasource, "当前 Agent 没有过滤字段的授权")
		}
	}
	if _, err := handlers.analyticsAudit(ctx, datasource, object, "allow", "analytics_preflight", 0, 0); err != nil {
		return handlers.analyticsAuditUnavailable()
	}
	deadline := 2 * time.Second
	if datasource.StmtTimeoutMS > 0 && time.Duration(datasource.StmtTimeoutMS)*time.Millisecond < deadline {
		deadline = time.Duration(datasource.StmtTimeoutMS) * time.Millisecond
	}
	timed, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()
	var result model.QueryResult
	if handlers.analyticsRead != nil {
		result, err = handlers.analyticsRead(timed, datasource, request)
	} else {
		result, err = handlers.runtime.ClickHouseReadControlled(timed, datasource, request)
	}
	if err != nil || !analyticsResultMatches(result, request) || timed.Err() != nil {
		if _, auditErr := handlers.analyticsTerminalAudit(datasource, object, "error", "analytics_query_error", 0, 0); auditErr != nil {
			return handlers.analyticsAuditUnavailable()
		}
		return errorToolResponse("ClickHouse 受控查询失败或已取消", "请检查连接、权限、资源预算与审计记录")
	}
	var redactor mask.Redactor
	if handlers.analyticsRedactor != nil {
		redactor, err = handlers.analyticsRedactor(timed, datasource.ID)
	} else {
		redactor, err = handlers.runtime.NativeRedactorFor(timed, datasource.ID)
	}
	if err != nil || redactor == nil {
		_, _ = handlers.analyticsTerminalAudit(datasource, object, "error", "analytics_mask_error", 0, 0)
		return errorToolResponse("ClickHouse 脱敏规则不可用", "请联系管理员检查脱敏配置")
	}
	sources := make([]mask.ColumnSource, len(request.Fields))
	for i, field := range request.Fields {
		sources[i] = mask.ColumnSource{Column: field, Source: table}
	}
	scoped, ok := redactor.(mask.RelationSourceAwareRedactor)
	if !ok {
		_, _ = handlers.analyticsTerminalAudit(datasource, object, "error", "analytics_mask_error", 0, 0)
		return errorToolResponse("ClickHouse 脱敏能力不可用", "请联系管理员检查运行环境")
	}
	result, report := scoped.ApplyWithColumnSources(result, sources, []model.ObjectRef{table})
	if timed.Err() != nil {
		if _, auditErr := handlers.analyticsTerminalAudit(datasource, object, "error", "analytics_cancelled", 0, 0); auditErr != nil {
			return handlers.analyticsAuditUnavailable()
		}
		return errorToolResponse("ClickHouse 查询已取消", "请重新发起请求并核对审计记录")
	}
	final, err := handlers.analyticsTerminalAudit(datasource, object, "allow", "analytics_complete", result.RowCount, report.MaskedCells)
	if err != nil {
		return handlers.analyticsAuditUnavailable()
	}
	return allowToolResponse("已返回授权 ClickHouse 表的脱敏结果", pipelineData{Result: &result, AuditID: final.ID, Redact: report})
}

func analyticsResultMatches(result model.QueryResult, request executor.ClickHouseReadRequest) bool {
	if result.Truncated || result.RowCount != len(result.Rows) || len(result.Rows) > request.Limit || len(result.Columns) != len(request.Fields) {
		return false
	}
	for i, field := range request.Fields {
		if result.Columns[i] != field {
			return false
		}
	}
	bytes := 0
	for _, row := range result.Rows {
		if len(row) != len(request.Fields) {
			return false
		}
		for _, value := range row {
			bytes += len(value)
			if len(value) > 64*1024 || bytes > 1024*1024 || !utf8.ValidString(value) {
				return false
			}
		}
	}
	return true
}

func (handlers *toolHandlers) analyticsDeny(ctx context.Context, datasource model.Datasource, reason string) ToolResponse {
	audit, err := handlers.analyticsAudit(ctx, datasource, "", "deny", "analytics_deny", 0, 0)
	if err != nil {
		return handlers.analyticsAuditUnavailable()
	}
	response := denyToolResponse(reason, "请检查数据源、当前 Agent 的精确表和列授权")
	response.Data = pipelineData{AuditID: audit.ID}
	return response
}

func (*toolHandlers) analyticsAuditUnavailable() ToolResponse {
	return errorToolResponse("审计库不可用，已拒绝 ClickHouse 请求", "请稍后重试；若持续失败，请联系管理员")
}

func (handlers *toolHandlers) analyticsTerminalAudit(datasource model.Datasource, object, decision, phase string, rows, masked int) (model.AuditLog, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	tenantCtx, err := store.WithTenant(ctx, handlers.agent.TenantID)
	if err != nil {
		return model.AuditLog{}, err
	}
	return handlers.analyticsAudit(tenantCtx, datasource, object, decision, phase, rows, masked)
}

func (handlers *toolHandlers) analyticsAudit(ctx context.Context, datasource model.Datasource, object, decision, phase string, rows, masked int) (model.AuditLog, error) {
	if handlers == nil || handlers.runtime == nil || handlers.runtime.Store == nil || ctx == nil || datasource.ID == "" {
		return model.AuditLog{}, errors.New("ClickHouse audit unavailable")
	}
	detailsBytes, err := json.Marshal(struct {
		Phase       string `json:"phase"`
		Rows        int    `json:"rows"`
		MaskedCells int    `json:"masked_cells"`
	}{phase, rows, masked})
	if err != nil {
		return model.AuditLog{}, err
	}
	tool, hits, details := "query_analytics", "[]", string(detailsBytes)
	audit := model.AuditLog{AgentID: &handlers.agent.ID, DatasourceID: &datasource.ID, DBType: &datasource.DBType,
		MCPTool: &tool, Decision: decision, RuleHits: &hits, Action: &phase, DetailsJSON: &details, RowsReturned: &rows}
	if object != "" {
		audit.Objects = &object
	}
	return handlers.runtime.Store.AuditLogs().Insert(ctx, audit)
}

package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/cuipengdba/agentsql/internal/auth"
	executor "github.com/cuipengdba/agentsql/internal/authorizedexecute"
	"github.com/cuipengdba/agentsql/internal/mask"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/policy"
	"github.com/cuipengdba/agentsql/internal/store"
)

func (handlers *toolHandlers) findDocuments(ctx context.Context, input mongoFindInput) ToolResponse {
	const tool = "find_documents"
	datasource, response := handlers.authorizedDatasource(ctx, input.DatasourceID)
	if response != nil {
		return handlers.auditDatasourcePreflightDenial(ctx, input.DatasourceID, tool, *response)
	}
	if datasource.DBType != "mongodb" || datasource.Database == "" || datasource.Database == "admin" ||
		(datasource.TLSMode != "strict" && datasource.TLSMode != "verify-full") ||
		datasource.TrustServerCertificate || datasource.Username == "" {
		return handlers.mongoDeny(ctx, datasource, "MongoDB 数据源缺少受控只读所需的严格 TLS、数据库或认证账号")
	}
	if _, delegated := delegatedHumanFromContext(ctx); delegated || handlers.agent.TaskGrantID != "" {
		return handlers.mongoDeny(ctx, datasource, "当前身份模式未开放 MongoDB 集合读取")
	}
	current, err := auth.NewAuthenticator(handlers.runtime.Store.Agents()).Authenticate(ctx, handlers.apiKey)
	if err != nil || current.ID != handlers.agent.ID || current.TenantID != handlers.agent.TenantID || current.Status != "active" {
		return handlers.mongoDeny(ctx, datasource, "当前 Agent 身份已失效")
	}
	request := executor.MongoFindRequest{
		Collection: input.Collection, Fields: input.Fields, Filter: input.Filter, Limit: input.Limit,
	}
	if err := executor.ValidateMongoFind(request); err != nil {
		return handlers.mongoDeny(ctx, datasource, "MongoDB 查询参数不符合受控只读约束")
	}
	if !validSchemaIdentifier(datasource.Database) {
		return handlers.mongoDeny(ctx, datasource, "MongoDB 数据库名不符合受控只读约束")
	}
	policies, err := handlers.runtime.Store.Policies().ListByAgentAndDatasource(ctx, handlers.agent.ID, datasource.ID)
	if err != nil {
		return handlers.mongoAuditUnavailable()
	}
	decision, err := policy.NewResolver().Resolve(policies, handlers.agent.Level)
	if err != nil {
		return handlers.mongoAuditUnavailable()
	}
	objectName := datasource.Database + "." + request.Collection
	table := model.ObjectRef{Schema: datasource.Database, Table: request.Collection}
	exactGrant := false
	for _, stored := range policies {
		if stored.Action == "allow" && stored.ObjectType == "table" && stored.ObjectName == objectName {
			exactGrant = true
			break
		}
	}
	if !exactGrant || policy.MatchesAnyTable(decision.DeniedTables, table) ||
		!policy.MatchesAnyTable(decision.AllowedTables, table) {
		return handlers.mongoDeny(ctx, datasource, "当前 Agent 没有此 MongoDB 集合的精确授权")
	}
	if allowedFields, constrained := decision.ColumnACL[objectName]; constrained {
		for _, field := range request.Fields {
			if !mongoFieldGranted(allowedFields, field) {
				return handlers.mongoDeny(ctx, datasource, "当前 Agent 没有请求字段或过滤字段的授权")
			}
		}
		for field := range request.Filter {
			if !mongoFieldGranted(allowedFields, field) {
				return handlers.mongoDeny(ctx, datasource, "当前 Agent 没有请求字段或过滤字段的授权")
			}
		}
	}
	if _, err := handlers.mongoAudit(ctx, datasource, objectName, "allow", "mongo_find_preflight", 0, 0); err != nil {
		return handlers.mongoAuditUnavailable()
	}
	deadline := 2 * time.Second
	if datasource.StmtTimeoutMS > 0 && time.Duration(datasource.StmtTimeoutMS)*time.Millisecond < deadline {
		deadline = time.Duration(datasource.StmtTimeoutMS) * time.Millisecond
	}
	timed, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()
	var result model.QueryResult
	if handlers.mongoFind != nil {
		result, err = handlers.mongoFind(timed, datasource, request)
	} else {
		result, err = handlers.runtime.MongoFindControlled(timed, datasource, request)
	}
	if err != nil || !mongoResultMatches(result, request) {
		if _, auditErr := handlers.mongoTerminalAudit(datasource, objectName, "error", "mongo_find_error", 0, 0); auditErr != nil {
			return handlers.mongoAuditUnavailable()
		}
		return errorToolResponse("MongoDB 受控查询失败", "请检查数据源连接、权限和查询范围后重试")
	}
	var redactor mask.Redactor
	if handlers.mongoRedactor != nil {
		redactor, err = handlers.mongoRedactor(timed, datasource.ID)
	} else {
		redactor, err = handlers.runtime.NativeRedactorFor(timed, datasource.ID)
	}
	if err != nil || redactor == nil {
		_, _ = handlers.mongoTerminalAudit(datasource, objectName, "error", "mongo_find_mask_error", 0, 0)
		return errorToolResponse("MongoDB 脱敏规则不可用", "请联系管理员检查脱敏配置")
	}
	sources := make([]mask.ColumnSource, len(request.Fields))
	for index, field := range request.Fields {
		sources[index] = mask.ColumnSource{Column: field, Source: model.ObjectRef{Schema: datasource.Database, Table: request.Collection}}
	}
	var report mask.RedactReport
	if scoped, ok := redactor.(mask.RelationSourceAwareRedactor); ok {
		result, report = scoped.ApplyWithColumnSources(result, sources, []model.ObjectRef{table})
	} else {
		_, _ = handlers.mongoTerminalAudit(datasource, objectName, "error", "mongo_find_mask_error", 0, 0)
		return errorToolResponse("MongoDB 脱敏能力不可用", "请联系管理员检查运行环境")
	}
	final, err := handlers.mongoTerminalAudit(datasource, objectName, "allow", "mongo_find_complete", result.RowCount, report.MaskedCells)
	if err != nil {
		return handlers.mongoAuditUnavailable()
	}
	return allowToolResponse("已返回授权 MongoDB 集合的脱敏结果", pipelineData{
		Result: &result, AuditID: final.ID, Redact: report,
	})
}

func mongoFieldGranted(allowed []string, field string) bool {
	for _, candidate := range allowed {
		if candidate == field {
			return true
		}
	}
	return false
}

func mongoResultMatches(result model.QueryResult, request executor.MongoFindRequest) bool {
	if result.Truncated || result.RowCount != len(result.Rows) || len(result.Rows) > request.Limit ||
		len(result.Columns) != len(request.Fields) {
		return false
	}
	for index, field := range request.Fields {
		if result.Columns[index] != field {
			return false
		}
	}
	for _, row := range result.Rows {
		if len(row) != len(request.Fields) {
			return false
		}
	}
	return true
}

func (handlers *toolHandlers) mongoDeny(ctx context.Context, datasource model.Datasource, reason string) ToolResponse {
	audit, err := handlers.mongoAudit(ctx, datasource, "", "deny", "mongo_find_deny", 0, 0)
	if err != nil {
		return handlers.mongoAuditUnavailable()
	}
	response := denyToolResponse(reason, "请检查数据源配置和当前 Agent 的精确集合及字段授权")
	response.Data = pipelineData{AuditID: audit.ID}
	return response
}

func (handlers *toolHandlers) mongoAuditUnavailable() ToolResponse {
	return errorToolResponse("审计库不可用，已拒绝 MongoDB 请求", "请稍后重试；若持续失败，请联系管理员")
}

func (handlers *toolHandlers) mongoTerminalAudit(datasource model.Datasource, objectName, decision, action string, rows, masked int) (model.AuditLog, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	tenantCtx, err := store.WithTenant(ctx, handlers.agent.TenantID)
	if err != nil {
		return model.AuditLog{}, err
	}
	return handlers.mongoAudit(tenantCtx, datasource, objectName, decision, action, rows, masked)
}

func (handlers *toolHandlers) mongoAudit(ctx context.Context, datasource model.Datasource, objectName, decision, action string, rows, masked int) (model.AuditLog, error) {
	if handlers == nil || handlers.runtime == nil || handlers.runtime.Store == nil || ctx == nil {
		return model.AuditLog{}, errors.New("MongoDB audit is unavailable")
	}
	agentID, datasourceID, dbType, tool := handlers.agent.ID, datasource.ID, datasource.DBType, "find_documents"
	hits := "[]"
	detailsBytes, err := json.Marshal(struct {
		Phase       string `json:"phase"`
		Rows        int    `json:"rows"`
		MaskedCells int    `json:"masked_cells"`
	}{Phase: action, Rows: rows, MaskedCells: masked})
	if err != nil {
		return model.AuditLog{}, err
	}
	details := string(detailsBytes)
	if strings.TrimSpace(datasourceID) == "" {
		return model.AuditLog{}, errors.New("MongoDB audit has no datasource")
	}
	audit := model.AuditLog{
		AgentID: &agentID, DatasourceID: &datasourceID, DBType: &dbType, MCPTool: &tool,
		Decision: decision, RuleHits: &hits, Action: &action, DetailsJSON: &details,
		RowsReturned: &rows,
	}
	if objectName != "" {
		audit.Objects = &objectName
	}
	bounded, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return handlers.runtime.Store.AuditLogs().Insert(bounded, audit)
}

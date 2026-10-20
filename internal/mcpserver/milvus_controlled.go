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

func (handlers *toolHandlers) searchVectors(ctx context.Context, input vectorSearchInput) ToolResponse {
	const tool = "search_vectors"
	datasource, response := handlers.authorizedDatasource(ctx, input.DatasourceID)
	if response != nil {
		return handlers.auditDatasourcePreflightDenial(ctx, input.DatasourceID, tool, *response)
	}
	if datasource.DBType != "milvus" || datasource.Database != "default" ||
		(datasource.TLSMode != "strict" && datasource.TLSMode != "verify-full") ||
		datasource.TrustServerCertificate || datasource.TLSCAFile == "" ||
		datasource.Username == "" || datasource.Username == "root" || datasource.PasswordEnc == "" {
		return handlers.vectorDeny(ctx, datasource, "Milvus 数据源缺少受控检索所需的严格 TLS、default 数据库或非管理员账号")
	}
	if _, delegated := delegatedHumanFromContext(ctx); delegated || handlers.agent.TaskGrantID != "" {
		return handlers.vectorDeny(ctx, datasource, "当前身份模式未开放 Milvus 检索")
	}
	current, err := auth.NewAuthenticator(handlers.runtime.Store.Agents()).Authenticate(ctx, handlers.apiKey)
	if err != nil || current.ID != handlers.agent.ID || current.TenantID != handlers.agent.TenantID || current.Status != "active" {
		return handlers.vectorDeny(ctx, datasource, "当前 Agent 身份已失效")
	}
	request := executor.MilvusSearchRequest{Collection: input.Collection, VectorField: input.VectorField,
		Vector: input.Vector, Fields: input.Fields, TopK: input.TopK}
	if executor.ValidateMilvusSearch(request) != nil {
		return handlers.vectorDeny(ctx, datasource, "Milvus 向量检索参数不符合受控范围")
	}
	policies, err := handlers.runtime.Store.Policies().ListByAgentAndDatasource(ctx, handlers.agent.ID, datasource.ID)
	if err != nil {
		return handlers.vectorAuditUnavailable()
	}
	decision, err := policy.NewResolver().Resolve(policies, current.Level)
	if err != nil {
		return handlers.vectorAuditUnavailable()
	}
	object := datasource.Database + "." + request.Collection
	table := model.ObjectRef{Schema: datasource.Database, Table: request.Collection}
	exactGrant := false
	for _, stored := range policies {
		if stored.RowFilter != nil || stored.LegacyUnrepresentable || stored.RelationBindingID != nil ||
			(stored.ObjectType != "column" && stored.Columns != nil) {
			return handlers.vectorDeny(ctx, datasource, "Milvus 受控检索无法安全执行当前行过滤或绑定策略")
		}
		if stored.Action == "allow" && stored.ObjectType == "table" && stored.ObjectName == object {
			exactGrant = true
		}
	}
	if !exactGrant || policy.MatchesAnyTable(decision.DeniedTables, table) ||
		!policy.MatchesAnyTable(decision.AllowedTables, table) {
		return handlers.vectorDeny(ctx, datasource, "当前 Agent 没有此 Milvus collection 的精确授权")
	}
	allowed := decision.ColumnACL[object]
	if len(allowed) == 0 || !mongoFieldGranted(allowed, request.VectorField) {
		return handlers.vectorDeny(ctx, datasource, "Milvus 向量字段必须显式授权")
	}
	for _, field := range request.Fields {
		if !mongoFieldGranted(allowed, field) {
			return handlers.vectorDeny(ctx, datasource, "当前 Agent 没有返回字段的授权")
		}
	}
	if _, err := handlers.vectorAudit(ctx, datasource, object, "allow", "vector_preflight", 0, 0); err != nil {
		return handlers.vectorAuditUnavailable()
	}
	deadline := 5 * time.Second
	if datasource.StmtTimeoutMS > 0 && time.Duration(datasource.StmtTimeoutMS)*time.Millisecond < deadline {
		deadline = time.Duration(datasource.StmtTimeoutMS) * time.Millisecond
	}
	timed, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()
	var result model.QueryResult
	if handlers.vectorSearch != nil {
		result, err = handlers.vectorSearch(timed, datasource, request)
	} else {
		result, err = handlers.runtime.MilvusSearchControlled(timed, datasource, request)
	}
	if err != nil || !vectorResultMatches(result, request) || timed.Err() != nil {
		if _, auditErr := handlers.vectorTerminalAudit(datasource, object, "error", "vector_search_error", 0, 0); auditErr != nil {
			return handlers.vectorAuditUnavailable()
		}
		return errorToolResponse("Milvus 受控检索失败或已取消", "请检查连接、权限、资源预算与审计记录")
	}
	var redactor mask.Redactor
	if handlers.vectorRedactor != nil {
		redactor, err = handlers.vectorRedactor(timed, datasource.ID)
	} else {
		redactor, err = handlers.runtime.NativeRedactorFor(timed, datasource.ID)
	}
	if err != nil || redactor == nil {
		_, _ = handlers.vectorTerminalAudit(datasource, object, "error", "vector_mask_error", 0, 0)
		return errorToolResponse("Milvus 脱敏规则不可用", "请联系管理员检查脱敏配置")
	}
	scoped, ok := redactor.(mask.RelationSourceAwareRedactor)
	if !ok {
		_, _ = handlers.vectorTerminalAudit(datasource, object, "error", "vector_mask_error", 0, 0)
		return errorToolResponse("Milvus 脱敏能力不可用", "请联系管理员检查运行环境")
	}
	sources := make([]mask.ColumnSource, len(request.Fields))
	for i, field := range request.Fields {
		sources[i] = mask.ColumnSource{Column: field, Source: table}
	}
	result, report := scoped.ApplyWithColumnSources(result, sources, []model.ObjectRef{table})
	if timed.Err() != nil {
		if _, auditErr := handlers.vectorTerminalAudit(datasource, object, "error", "vector_cancelled", 0, 0); auditErr != nil {
			return handlers.vectorAuditUnavailable()
		}
		return errorToolResponse("Milvus 检索已取消", "请重新发起请求并核对审计记录")
	}
	final, err := handlers.vectorTerminalAudit(datasource, object, "allow", "vector_complete", result.RowCount, report.MaskedCells)
	if err != nil {
		return handlers.vectorAuditUnavailable()
	}
	return allowToolResponse("已返回授权 Milvus collection 的脱敏结果", pipelineData{Result: &result, AuditID: final.ID, Redact: report})
}

func vectorResultMatches(result model.QueryResult, request executor.MilvusSearchRequest) bool {
	if result.Truncated || result.RowCount != len(result.Rows) || len(result.Rows) > request.TopK || len(result.Columns) != len(request.Fields) {
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

func (handlers *toolHandlers) vectorDeny(ctx context.Context, datasource model.Datasource, reason string) ToolResponse {
	audit, err := handlers.vectorAudit(ctx, datasource, "", "deny", "vector_deny", 0, 0)
	if err != nil {
		return handlers.vectorAuditUnavailable()
	}
	response := denyToolResponse(reason, "请检查数据源、当前 Agent 的精确 collection 和字段授权")
	response.Data = pipelineData{AuditID: audit.ID}
	return response
}

func (*toolHandlers) vectorAuditUnavailable() ToolResponse {
	return errorToolResponse("审计库不可用，已拒绝 Milvus 请求", "请稍后重试；若持续失败，请联系管理员")
}

func (handlers *toolHandlers) vectorTerminalAudit(datasource model.Datasource, object, decision, phase string, rows, masked int) (model.AuditLog, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	tenantCtx, err := store.WithTenant(ctx, handlers.agent.TenantID)
	if err != nil {
		return model.AuditLog{}, err
	}
	return handlers.vectorAudit(tenantCtx, datasource, object, decision, phase, rows, masked)
}

func (handlers *toolHandlers) vectorAudit(ctx context.Context, datasource model.Datasource, object, decision, phase string, rows, masked int) (model.AuditLog, error) {
	if handlers == nil || handlers.runtime == nil || handlers.runtime.Store == nil || ctx == nil || datasource.ID == "" {
		return model.AuditLog{}, errors.New("Milvus audit unavailable")
	}
	detailsBytes, err := json.Marshal(struct {
		Phase       string `json:"phase"`
		Rows        int    `json:"rows"`
		MaskedCells int    `json:"masked_cells"`
	}{phase, rows, masked})
	if err != nil {
		return model.AuditLog{}, err
	}
	tool, hits, details := "search_vectors", "[]", string(detailsBytes)
	audit := model.AuditLog{AgentID: &handlers.agent.ID, DatasourceID: &datasource.ID, DBType: &datasource.DBType,
		MCPTool: &tool, Decision: decision, RuleHits: &hits, Action: &phase, DetailsJSON: &details, RowsReturned: &rows}
	if object != "" {
		audit.Objects = &object
	}
	return handlers.runtime.Store.AuditLogs().Insert(ctx, audit)
}

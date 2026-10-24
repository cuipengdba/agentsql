package mcpserver

import (
	"context"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/cuipengdba/agentsql/internal/auth"
	executor "github.com/cuipengdba/agentsql/internal/authorizedexecute"
	"github.com/cuipengdba/agentsql/internal/mask"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/policy"
	"github.com/cuipengdba/agentsql/internal/store"
)

func (handlers *toolHandlers) readHashFields(ctx context.Context, input hashReadInput) ToolResponse {
	const tool = "read_hash_fields"
	datasource, response := handlers.authorizedDatasource(ctx, input.DatasourceID)
	if response != nil {
		return handlers.auditDatasourcePreflightDenial(ctx, input.DatasourceID, tool, *response)
	}
	if (datasource.DBType != "redis" && datasource.DBType != "valkey") ||
		(datasource.TLSMode != "strict" && datasource.TLSMode != "verify-full") ||
		datasource.TrustServerCertificate || datasource.TLSCAFile == "" || datasource.Username == "" || datasource.PasswordEnc == "" {
		return handlers.hashDeny(ctx, datasource, "数据源缺少受控 Hash 读取所需的严格 TLS 或 ACL 凭据")
	}
	if _, delegated := delegatedHumanFromContext(ctx); delegated || handlers.agent.TaskGrantID != "" {
		return handlers.hashDeny(ctx, datasource, "当前身份模式未开放 Hash 字段读取")
	}
	current, err := auth.NewAuthenticator(handlers.runtime.Store.Agents()).Authenticate(ctx, handlers.apiKey)
	if err != nil || current.ID != handlers.agent.ID || current.TenantID != handlers.agent.TenantID || current.Status != "active" {
		return handlers.hashDeny(ctx, datasource, "当前 Agent 身份已失效")
	}
	database, err := executor.RedisDatabaseNumber(datasource)
	request := executor.RedisHashReadRequest{Key: input.Key, Fields: input.Fields}
	if err != nil || executor.ValidateRedisHashRead(request) != nil {
		return handlers.hashDeny(ctx, datasource, "数据库、键或字段不符合受控 Hash 读取约束")
	}
	policies, err := handlers.runtime.Store.Policies().ListByAgentAndDatasource(ctx, handlers.agent.ID, datasource.ID)
	if err != nil {
		return handlers.keyAuditUnavailable()
	}
	decision, err := policy.NewResolver().Resolve(policies, handlers.agent.Level)
	if err != nil {
		return handlers.keyAuditUnavailable()
	}
	if !policy.HashFieldsGranted(decision, database, input.Key, input.Fields) {
		return handlers.hashDeny(ctx, datasource, "当前 Agent 没有此精确键及全部 Hash 字段的授权")
	}
	var object string
	if handlers.keyAuditIdentity != nil {
		object, err = handlers.keyAuditIdentity(datasource.ID, database, input.Key)
	} else {
		object, err = handlers.runtime.KeyAuditObject(datasource.ID, database, input.Key)
	}
	if err != nil {
		return handlers.keyAuditUnavailable()
	}
	if _, err := handlers.keyAuditWithTool(ctx, datasource, tool, object, "allow", "hash_read_preflight", 0, 0); err != nil {
		return handlers.keyAuditUnavailable()
	}
	deadline := 2 * time.Second
	if datasource.StmtTimeoutMS > 0 && time.Duration(datasource.StmtTimeoutMS)*time.Millisecond < deadline {
		deadline = time.Duration(datasource.StmtTimeoutMS) * time.Millisecond
	}
	timed, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()
	var result model.QueryResult
	if handlers.hashRead != nil {
		result, err = handlers.hashRead(timed, datasource, request)
	} else {
		result, err = handlers.runtime.RedisHashReadControlled(timed, datasource, request)
	}
	if err != nil || !hashResultMatches(result, input.Fields) || timed.Err() != nil {
		if _, auditErr := handlers.hashTerminalAudit(datasource, object, "error", "hash_read_error", 0, 0); auditErr != nil {
			return handlers.keyAuditUnavailable()
		}
		return errorToolResponse("受控 Hash 读取失败", "请检查数据源、ACL、版本、键类型和字段后重试")
	}
	var redactor mask.Redactor
	if handlers.keyRedactor != nil {
		redactor, err = handlers.keyRedactor(timed, datasource.ID)
	} else {
		redactor, err = handlers.runtime.NativeRedactorFor(timed, datasource.ID)
	}
	if err != nil || redactor == nil {
		if _, auditErr := handlers.hashTerminalAudit(datasource, object, "error", "hash_read_mask_error", 0, 0); auditErr != nil {
			return handlers.keyAuditUnavailable()
		}
		return errorToolResponse("Hash 字段脱敏规则不可用", "请联系管理员检查脱敏配置")
	}
	scoped, ok := redactor.(mask.RelationSourceAwareRedactor)
	if !ok {
		if _, auditErr := handlers.hashTerminalAudit(datasource, object, "error", "hash_read_mask_error", 0, 0); auditErr != nil {
			return handlers.keyAuditUnavailable()
		}
		return errorToolResponse("Hash 字段脱敏能力不可用", "请联系管理员检查运行环境")
	}
	source := model.ObjectRef{Schema: "db" + strconv.Itoa(database), Table: input.Key}
	sources := make([]mask.ColumnSource, len(input.Fields))
	for index, field := range input.Fields {
		sources[index] = mask.ColumnSource{Column: field, Source: source}
	}
	result, report := scoped.ApplyWithColumnSources(result, sources, []model.ObjectRef{source})
	if timed.Err() != nil {
		if _, auditErr := handlers.hashTerminalAudit(datasource, object, "error", "hash_read_cancelled", 0, 0); auditErr != nil {
			return handlers.keyAuditUnavailable()
		}
		return errorToolResponse("受控 Hash 读取已取消", "请重新发起请求并核对审计记录")
	}
	final, err := handlers.hashTerminalAudit(datasource, object, "allow", "hash_read_complete", result.RowCount, report.MaskedCells)
	if err != nil {
		return handlers.keyAuditUnavailable()
	}
	return allowToolResponse("已返回授权 Hash 字段的脱敏结果", pipelineData{Result: &result, AuditID: final.ID, Redact: report})
}

func hashResultMatches(result model.QueryResult, fields []string) bool {
	if result.Truncated || len(result.Columns) != len(fields) || result.RowCount != len(result.Rows) || result.RowCount > 1 {
		return false
	}
	for index, field := range fields {
		if result.Columns[index] != field {
			return false
		}
	}
	if result.RowCount == 0 {
		return true
	}
	if len(result.Rows[0]) != len(fields) {
		return false
	}
	total := 0
	for _, value := range result.Rows[0] {
		total += len(value)
		if len(value) > 16*1024 || total > 64*1024 || !utf8.ValidString(value) {
			return false
		}
	}
	return true
}

func (handlers *toolHandlers) hashDeny(ctx context.Context, datasource model.Datasource, reason string) ToolResponse {
	audit, err := handlers.keyAuditWithTool(ctx, datasource, "read_hash_fields", "", "deny", "hash_read_deny", 0, 0)
	if err != nil {
		return handlers.keyAuditUnavailable()
	}
	response := denyToolResponse(reason, "请检查当前 Agent 的精确键及 Hash 字段授权")
	response.Data = pipelineData{AuditID: audit.ID}
	return response
}

func (handlers *toolHandlers) hashTerminalAudit(datasource model.Datasource, object, decision, phase string, rows, masked int) (model.AuditLog, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	tenantCtx, err := store.WithTenant(ctx, handlers.agent.TenantID)
	if err != nil {
		return model.AuditLog{}, err
	}
	return handlers.keyAuditWithTool(tenantCtx, datasource, "read_hash_fields", object, decision, phase, rows, masked)
}

package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
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

func (handlers *toolHandlers) readKey(ctx context.Context, input keyReadInput) ToolResponse {
	const tool = "read_key"
	datasource, response := handlers.authorizedDatasource(ctx, input.DatasourceID)
	if response != nil {
		return handlers.auditDatasourcePreflightDenial(ctx, input.DatasourceID, tool, *response)
	}
	if (datasource.DBType != "redis" && datasource.DBType != "valkey") ||
		(datasource.TLSMode != "strict" && datasource.TLSMode != "verify-full") ||
		datasource.TrustServerCertificate || datasource.TLSCAFile == "" || datasource.Username == "" || datasource.PasswordEnc == "" {
		return handlers.keyDeny(ctx, datasource, "数据源缺少受控键读取所需的严格 TLS 或 ACL 凭据")
	}
	if _, delegated := delegatedHumanFromContext(ctx); delegated || handlers.agent.TaskGrantID != "" {
		return handlers.keyDeny(ctx, datasource, "当前身份模式未开放键读取")
	}
	current, err := auth.NewAuthenticator(handlers.runtime.Store.Agents()).Authenticate(ctx, handlers.apiKey)
	if err != nil || current.ID != handlers.agent.ID || current.TenantID != handlers.agent.TenantID || current.Status != "active" {
		return handlers.keyDeny(ctx, datasource, "当前 Agent 身份已失效")
	}
	database, err := executor.RedisDatabaseNumber(datasource)
	if err != nil || !policy.ValidExactKey(input.Key) {
		return handlers.keyDeny(ctx, datasource, "数据库或键名不符合受控读取约束")
	}
	policies, err := handlers.runtime.Store.Policies().ListByAgentAndDatasource(ctx, handlers.agent.ID, datasource.ID)
	if err != nil {
		return handlers.keyAuditUnavailable()
	}
	decision, err := policy.NewResolver().Resolve(policies, handlers.agent.Level)
	if err != nil {
		return handlers.keyAuditUnavailable()
	}
	if !policy.KeyScopeGranted(decision.AllowedKeys, decision.DeniedKeys, database, input.Key) {
		return handlers.keyDeny(ctx, datasource, "当前 Agent 没有此键的授权")
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
	if _, err := handlers.keyAudit(ctx, datasource, object, "allow", "key_read_preflight", 0, 0); err != nil {
		return handlers.keyAuditUnavailable()
	}
	deadline := 2 * time.Second
	if datasource.StmtTimeoutMS > 0 && time.Duration(datasource.StmtTimeoutMS)*time.Millisecond < deadline {
		deadline = time.Duration(datasource.StmtTimeoutMS) * time.Millisecond
	}
	timed, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()
	request := executor.RedisReadRequest{Key: input.Key}
	var result model.QueryResult
	if handlers.keyRead != nil {
		result, err = handlers.keyRead(timed, datasource, request)
	} else {
		result, err = handlers.runtime.RedisReadControlled(timed, datasource, request)
	}
	if err != nil || !keyResultMatches(result) {
		if _, auditErr := handlers.keyTerminalAudit(datasource, object, "error", "key_read_error", 0, 0); auditErr != nil {
			return handlers.keyAuditUnavailable()
		}
		return errorToolResponse("受控键读取失败", "请检查数据源、ACL、版本和键类型后重试")
	}
	if timed.Err() != nil {
		if _, auditErr := handlers.keyTerminalAudit(datasource, object, "error", "key_read_cancelled", 0, 0); auditErr != nil {
			return handlers.keyAuditUnavailable()
		}
		return errorToolResponse("受控键读取已取消", "请重新发起请求并核对审计记录")
	}
	var redactor mask.Redactor
	if handlers.keyRedactor != nil {
		redactor, err = handlers.keyRedactor(timed, datasource.ID)
	} else {
		redactor, err = handlers.runtime.NativeRedactorFor(timed, datasource.ID)
	}
	if err != nil || redactor == nil {
		_, _ = handlers.keyTerminalAudit(datasource, object, "error", "key_read_mask_error", 0, 0)
		return errorToolResponse("键值脱敏规则不可用", "请联系管理员检查脱敏配置")
	}
	source := model.ObjectRef{Schema: "db" + strconv.Itoa(database), Table: input.Key}
	scoped, ok := redactor.(mask.RelationSourceAwareRedactor)
	if !ok {
		_, _ = handlers.keyTerminalAudit(datasource, object, "error", "key_read_mask_error", 0, 0)
		return errorToolResponse("键值脱敏能力不可用", "请联系管理员检查运行环境")
	}
	result, report := scoped.ApplyWithColumnSources(result, []mask.ColumnSource{{Column: "value", Source: source}}, []model.ObjectRef{source})
	if timed.Err() != nil {
		if _, auditErr := handlers.keyTerminalAudit(datasource, object, "error", "key_read_cancelled", 0, 0); auditErr != nil {
			return handlers.keyAuditUnavailable()
		}
		return errorToolResponse("受控键读取已取消", "请重新发起请求并核对审计记录")
	}
	final, err := handlers.keyTerminalAudit(datasource, object, "allow", "key_read_complete", result.RowCount, report.MaskedCells)
	if err != nil {
		return handlers.keyAuditUnavailable()
	}
	return allowToolResponse("已返回授权键的脱敏结果", pipelineData{Result: &result, AuditID: final.ID, Redact: report})
}

func keyResultMatches(result model.QueryResult) bool {
	if result.Truncated || len(result.Columns) != 1 || result.Columns[0] != "value" || result.RowCount != len(result.Rows) || result.RowCount > 1 {
		return false
	}
	if result.RowCount == 0 {
		return true
	}
	return len(result.Rows[0]) == 1 && len(result.Rows[0][0]) <= 64*1024 && utf8.ValidString(result.Rows[0][0])
}

func (handlers *toolHandlers) keyDeny(ctx context.Context, datasource model.Datasource, reason string) ToolResponse {
	audit, err := handlers.keyAudit(ctx, datasource, "", "deny", "key_read_deny", 0, 0)
	if err != nil {
		return handlers.keyAuditUnavailable()
	}
	response := denyToolResponse(reason, "请检查数据源、当前 Agent 的键授权和 ACL")
	response.Data = pipelineData{AuditID: audit.ID}
	return response
}

func (handlers *toolHandlers) keyAuditUnavailable() ToolResponse {
	return errorToolResponse("审计库不可用，已拒绝键读取", "请稍后重试；若持续失败，请联系管理员")
}

func (handlers *toolHandlers) keyTerminalAudit(datasource model.Datasource, object, decision, phase string, rows, masked int) (model.AuditLog, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	tenantCtx, err := store.WithTenant(ctx, handlers.agent.TenantID)
	if err != nil {
		return model.AuditLog{}, err
	}
	return handlers.keyAudit(tenantCtx, datasource, object, decision, phase, rows, masked)
}

func (handlers *toolHandlers) keyAudit(ctx context.Context, datasource model.Datasource, object, decision, phase string, rows, masked int) (model.AuditLog, error) {
	return handlers.keyAuditWithTool(ctx, datasource, "read_key", object, decision, phase, rows, masked)
}

func (handlers *toolHandlers) keyAuditWithTool(ctx context.Context, datasource model.Datasource, toolName, object, decision, phase string, rows, masked int) (model.AuditLog, error) {
	if handlers == nil || handlers.runtime == nil || handlers.runtime.Store == nil || ctx == nil || datasource.ID == "" {
		return model.AuditLog{}, errors.New("key audit unavailable")
	}
	detailsBytes, err := json.Marshal(struct {
		Phase       string `json:"phase"`
		Rows        int    `json:"rows"`
		MaskedCells int    `json:"masked_cells"`
	}{phase, rows, masked})
	if err != nil {
		return model.AuditLog{}, err
	}
	result := model.AuditLog{AgentID: &handlers.agent.ID, DatasourceID: &datasource.ID, DBType: &datasource.DBType,
		Decision: decision, Action: &phase, RowsReturned: &rows}
	tool, hits, details := toolName, "[]", string(detailsBytes)
	result.MCPTool, result.RuleHits, result.DetailsJSON = &tool, &hits, &details
	if object != "" {
		result.Objects = &object
	}
	bounded, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return handlers.runtime.Store.AuditLogs().Insert(bounded, result)
}

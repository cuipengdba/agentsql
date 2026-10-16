package adminapi

import (
	"errors"
	"net/http"
	"strings"

	executor "github.com/cuipengdba/agentsql/internal/authorizedexecute"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/parser"
	"github.com/cuipengdba/agentsql/internal/pipeline"
	"github.com/cuipengdba/agentsql/internal/policy"
	"github.com/cuipengdba/agentsql/internal/store"
)

type policyPreflightInput struct {
	AgentID      string `json:"agent_id"`
	DatasourceID string `json:"datasource_id"`
	SQL          string `json:"sql"`
}

type policyPreflightObject struct {
	Schema         string   `json:"schema"`
	Table          string   `json:"table"`
	State          string   `json:"state"`
	AllowPolicyIDs []string `json:"allow_policy_ids"`
	DenyPolicyIDs  []string `json:"deny_policy_ids"`
}

type policyPreflightView struct {
	Decision   string                  `json:"decision"`
	Reason     string                  `json:"reason"`
	StaticRule string                  `json:"static_rule,omitempty"`
	Objects    []policyPreflightObject `json:"objects"`
	LiveCheck  bool                    `json:"live_check"`
}

// policiesPreflight reads the current tenant's policy snapshot and parses SQL.
// It makes no business-database call and never predicts an executable allow.
func (handler *Handler) policiesPreflight(writer http.ResponseWriter, request *http.Request) {
	var input policyPreflightInput
	if decodeJSON(writer, request, &input) != nil ||
		strings.TrimSpace(input.AgentID) == "" || strings.TrimSpace(input.DatasourceID) == "" ||
		strings.TrimSpace(input.SQL) == "" ||
		executor.ValidatePreParse(input.SQL, nil, executor.DefaultLimits) != nil {
		handler.fail(writer, http.StatusUnprocessableEntity, "invalid preflight input")
		return
	}
	agent, err := handler.deps.Runtime.Store.Agents().Get(request.Context(), input.AgentID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && agent.Status != "active") {
		handler.notFound(writer)
		return
	}
	if err != nil {
		handler.internal(writer, err)
		return
	}
	datasource, err := handler.deps.Runtime.Store.Datasources().Get(request.Context(), input.DatasourceID)
	if errors.Is(err, store.ErrNotFound) {
		handler.notFound(writer)
		return
	}
	if err != nil {
		handler.internal(writer, err)
		return
	}
	rows, err := handler.deps.Runtime.Store.Policies().ListByAgentAndDatasource(request.Context(), agent.ID, datasource.ID)
	if err != nil {
		handler.internal(writer, err)
		return
	}
	decision, err := policy.NewResolver().Resolve(rows, agent.Level)
	if err != nil {
		handler.internal(writer, err)
		return
	}
	sqlParser, err := parser.NewParser(model.DBDialect(datasource.DBType))
	if err != nil {
		handler.ok(writer, policyPreflightView{Decision: "unknown", Reason: "当前数据源不支持离线解析；需通过真实网关验收", Objects: []policyPreflightObject{}, LiveCheck: true})
		return
	}
	ast, err := sqlParser.Parse(input.SQL)
	if err != nil || ast == nil || ast.IsMulti {
		handler.ok(writer, policyPreflightView{Decision: "deny", Reason: "SQL 无法证明为一条可解析语句", Objects: []policyPreflightObject{}, LiveCheck: true})
		return
	}
	view := policyPreflightView{Decision: "unknown", Reason: "表策略仅通过离线候选检查；列级授权、动态规则、业务库状态和审计仍须真实网关验证", Objects: make([]policyPreflightObject, 0, len(ast.Tables)), LiveCheck: true}
	for _, object := range ast.Tables {
		item := policyPreflightObject{Schema: object.Schema, Table: object.Table, State: "candidate", AllowPolicyIDs: []string{}, DenyPolicyIDs: []string{}}
		for _, row := range rows {
			if row.ObjectType == "column" || !policy.MatchesAnyTable([]string{row.ObjectName}, object) {
				continue
			}
			if row.Action == "deny" {
				item.DenyPolicyIDs = append(item.DenyPolicyIDs, row.ID)
			} else if row.Action == "allow" {
				item.AllowPolicyIDs = append(item.AllowPolicyIDs, row.ID)
			}
		}
		if policy.MatchesAnyTable(decision.DeniedTables, object) || !policy.MatchesAnyTable(decision.AllowedTables, object) {
			item.State = "deny"
			view.Decision = "deny"
			view.Reason = "至少一个对象被表策略拒绝或没有表级允许策略"
		}
		view.Objects = append(view.Objects, item)
	}
	if len(ast.Tables) == 0 {
		view.Reason = "未能从 SQL 确定物理表；不能推断授权"
	}
	// StaticAssess runs production static rules without metadata or execution.
	// Other dialects retain unknown rather than borrowing a different parser.
	if datasource.DBType == "postgres" || datasource.DBType == "mysql" || datasource.DBType == "sqlserver" {
		assessment, _, assessErr := pipeline.StaticAssess(pipeline.StaticAssessInput{
			SQL: input.SQL, Dialect: model.DBDialect(datasource.DBType), AgentLevel: agent.Level,
		})
		if assessErr != nil {
			handler.internal(writer, assessErr)
			return
		}
		if assessment.Decision == model.DecisionDeny {
			view.Decision = "deny"
			view.Reason = "静态规则拒绝"
			if len(assessment.Hits) > 0 {
				view.StaticRule = assessment.Hits[0].RuleID
			}
		}
	}
	handler.ok(writer, view)
}

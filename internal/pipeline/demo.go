package pipeline

import (
	"context"
	"fmt"
	"strings"

	"github.com/cuipengdba/agentsql/internal/config"
	"github.com/cuipengdba/agentsql/internal/engine"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/rules"
)

type pipelineRunMode uint8

const (
	runModeProduction pipelineRunMode = iota
	runModeDemo
)

// ProcessDemo runs the production pipeline with immutable Live Demo safety
// constraints. Only trusted server-side code should map a demo profile to the
// API key carried by request.
func (pipeline *Pipeline) ProcessDemo(ctx context.Context, request Request) (Response, error) {
	return pipeline.process(ctx, request, runModeDemo)
}

// WithDemoConfig installs the server-validated demo boundary on the pipeline.
// A disabled or empty configuration makes every ProcessDemo call fail closed.
func WithDemoConfig(demo config.DemoConfig) Option {
	return func(options *pipelineOptions) error {
		if options == nil {
			return ErrInvalidOption
		}
		options.demo = cloneDemoConfig(demo)
		return nil
	}
}

func cloneDemoConfig(source config.DemoConfig) config.DemoConfig {
	cloned := source
	cloned.AllowedDatasourceIDs = append([]string(nil), source.AllowedDatasourceIDs...)
	return cloned
}

func (run *pipelineRun) isDemo() bool {
	return run != nil && run.mode == runModeDemo
}

func (run *pipelineRun) demoScopeDeny() (model.RuleHit, bool) {
	if run == nil || run.pipeline == nil || run.agent == nil || run.datasource == nil {
		return demoScopeHit("演示请求缺少已认证的 Agent 或数据源元数据"), true
	}
	agentAllowed := run.agent.Status == "active" && (run.agent.ID == config.DemoAgentRO && run.agent.Level == "readonly" ||
		run.agent.ID == config.DemoAgentDML && run.agent.Level == "dml")
	if !agentAllowed {
		return demoScopeHit("认证身份不属于固定演示 Agent，或身份级别与演示档案不匹配"), true
	}
	if !run.pipeline.demo.AllowsDatasource(run.datasource.ID) {
		return demoScopeHit("目标数据源不在已启用演示环境的固定白名单中"), true
	}
	if run.request.MCPTool != "query" {
		return demoScopeHit("演示试运行仅接受 query 工具语义"), true
	}
	return model.RuleHit{}, false
}

func demoScopeHit(message string) model.RuleHit {
	return model.RuleHit{
		RuleID:     "DEMO_SCOPE",
		Risk:       model.RiskDeny,
		Decision:   model.DecisionDeny,
		Message:    message,
		Suggestion: "请使用服务端固定映射的演示身份、数据源和 query 工具重试",
	}
}

func demoParseHit(cause error) model.RuleHit {
	message := "演示 SQL 解析失败，已按拒绝处理"
	if cause != nil {
		message += ": " + cause.Error()
	}
	return model.RuleHit{
		RuleID:     "DEMO_PARSE",
		Risk:       model.RiskDeny,
		Decision:   model.DecisionDeny,
		Message:    message,
		Suggestion: "请修正为一条可解析的只读 SELECT 后重试",
	}
}

func demoNonSelectHit() model.RuleHit {
	return model.RuleHit{
		RuleID:     "DEMO_NON_SELECT",
		Risk:       model.RiskDeny,
		Decision:   model.DecisionDeny,
		Message:    "演示环境仅允许只读 SELECT",
		Suggestion: "请移除写入、DDL、管理、多语句或会产生副作用的 SQL",
	}
}

func demoWriteApprovalEligible(ast *model.AST) bool {
	if ast == nil || ast.IsMulti {
		return false
	}
	return ast.StmtType == model.StmtType("UPDATE") || ast.StmtType == model.StmtType("DELETE")
}

func demoWriteApprovalHit() model.RuleHit {
	return model.RuleHit{
		RuleID:     "DEMO_WRITE_APPROVAL",
		Risk:       model.RiskApprove,
		Decision:   model.DecisionApprove,
		Message:    "Live Demo 中的 UPDATE/DELETE 默认进入 DBA 人工审批",
		Suggestion: "请在审批页核对影响范围；审批完成前不会执行该语句",
	}
}

func (run *pipelineRun) appendStructuralHit(hit model.RuleHit) {
	if run == nil {
		return
	}
	assessment := &run.response.Assessment
	assessment.Hits = append(assessment.Hits, hit)
	if run.ast != nil {
		assessment.StmtType = run.ast.StmtType
		assessment.Normalized = run.ast.Normalized
		assessment.Objects = append([]model.ObjectRef(nil), run.ast.Tables...)
	}
	recomputeAssessmentDecision(assessment)
	assessment.StageLatency = run.stageLatency
	run.response.Decision = assessment.Decision
}

func isDemoSemanticReadOnly(ast *model.AST) bool {
	if ast == nil || ast.StmtType != model.StmtType("SELECT") || ast.IsMulti {
		return false
	}
	for _, operation := range ast.Operations {
		name, _, _ := strings.Cut(strings.ToUpper(strings.TrimSpace(operation)), ":")
		switch name {
		case "SELECT", "EXPLAIN", "SQL_COMMENT",
			rules.OperationNestingDepth, rules.OperationUnionCount, rules.OperationSelectColumn:
			continue
		default:
			// Unknown SELECT operations fail closed. This includes SELECT INTO,
			// EXPLAIN ANALYZE, output-file variants, and future side effects.
			return false
		}
	}
	return true
}

func (run *pipelineRun) requestRuleLayers() (engine.RuleLayers, error) {
	layers := mergeGlobalLayers(run.pipeline.layers, run.ruleLayer)
	if !run.isDemo() {
		return layers, nil
	}
	qps := run.pipeline.demo.EffectiveQPS()
	if qps <= 0 {
		return engine.RuleLayers{}, fmt.Errorf("demo qps must be positive")
	}
	rowLimit, err := normalizedRowLimit(run.datasource.RowLimit)
	if err != nil {
		return engine.RuleLayers{}, err
	}
	layers = forceAgentRuleConfig(layers, "R008", map[string]float64{
		rules.ThresholdQPS: float64(qps),
	})
	layers = forceAgentRuleConfig(layers, "R005", map[string]float64{
		rules.ThresholdRowLimit: float64(rowLimit),
	})
	return layers, nil
}

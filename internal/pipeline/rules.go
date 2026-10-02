package pipeline

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/cuipengdba/agentsql/internal/engine"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/rules"
)

// panicMetadataProvider is deliberately passed to the static gate. Any future
// metadata access by a rule not classified as dynamic fails tests immediately.
type panicMetadataProvider struct{}

func (panicMetadataProvider) TableHasIndex(string, string) (bool, error) {
	panic("static guard touched TableHasIndex")
}

func (panicMetadataProvider) TableRowCount(string, string) (int64, error) {
	panic("static guard touched TableRowCount")
}

func (panicMetadataProvider) TransactionState() (rules.TransactionState, error) {
	panic("static guard touched TransactionState")
}

func (panicMetadataProvider) MysqlTransactionState() (rules.MysqlTransactionState, error) {
	panic("static guard touched MysqlTransactionState")
}

// requestLimiter tracks the R008 reservation made by one Process call so it
// can be released before the final audit on every exit path.
type requestLimiter struct {
	delegate rules.RateLimiter
	key      string
	reserved bool
}

func (limiter *requestLimiter) Allow(
	key string,
	qps float64,
	maxConcurrent int,
) (rules.RateLimitResult, error) {
	if limiter == nil || isNilInterface(limiter.delegate) {
		return rules.RateLimitResult{}, fmt.Errorf("pipeline rate limiter is unavailable")
	}
	if limiter.reserved {
		return rules.RateLimitResult{}, fmt.Errorf("pipeline rate limiter reserved more than once")
	}
	result, err := limiter.delegate.Allow(key, qps, maxConcurrent)
	if err != nil {
		return rules.RateLimitResult{}, err
	}
	if result.Allowed {
		limiter.key = key
		limiter.reserved = true
	}
	return result, nil
}

func (*requestLimiter) Release(string) error {
	return fmt.Errorf("request limiter release is managed by pipeline")
}

func (limiter *requestLimiter) release() error {
	if limiter == nil || !limiter.reserved {
		return nil
	}
	limiter.reserved = false
	return limiter.delegate.Release(limiter.key)
}

func assembleRules(
	dialect model.DBDialect,
	limiter rules.RateLimiter,
	metadata any,
) ([]engine.Rule, error) {
	assembled := append([]engine.Rule{}, rules.NewGenericRules(limiter)...)
	switch dialect {
	case model.DBDialect("postgres"):
		provider, ok := metadata.(rules.MetadataProvider)
		if !ok || isNilInterface(provider) {
			return nil, fmt.Errorf("PostgreSQL metadata provider is unavailable")
		}
		assembled = append(assembled, rules.NewPostgresRules(provider)...)
	case model.DBDialect("mysql"):
		provider, ok := metadata.(rules.MysqlTransactionMetadataProvider)
		if !ok || isNilInterface(provider) {
			return nil, fmt.Errorf("MySQL transaction metadata provider is unavailable")
		}
		assembled = append(assembled, rules.NewMysqlRules(provider)...)
	default:
		return nil, fmt.Errorf("unsupported rule dialect %q", dialect)
	}
	return assembled, nil
}

func splitRules(all []engine.Rule) (static []engine.Rule, dynamic []engine.Rule) {
	return splitRulesForRun(all, false)
}

func splitRulesForRun(all []engine.Rule, _ bool) (static []engine.Rule, dynamic []engine.Rule) {
	static = make([]engine.Rule, 0, len(all))
	dynamic = make([]engine.Rule, 0, 6)
	for _, rule := range all {
		if isDynamicRuleID(rule.ID()) {
			dynamic = append(dynamic, rule)
			continue
		}
		static = append(static, rule)
	}
	return static, dynamic
}

func isDynamicRuleID(id string) bool {
	switch id {
	case "R004", "R005", "R105", "R106", "R107", "R204":
		return true
	default:
		return false
	}
}

func (run *pipelineRun) evaluateR005(runtimeResult *model.QueryResult) error {
	var selected []engine.Rule
	for _, rule := range rules.NewGenericRules(run.reservation) {
		if rule.ID() == "R005" {
			selected = append(selected, rule)
			break
		}
	}
	if len(selected) != 1 {
		return fmt.Errorf("R005 runtime rule is unavailable")
	}
	layers, err := run.requestRuleLayers()
	if err != nil {
		return err
	}
	evalContext := run.evalContext(panicMetadataProvider{})
	evalContext.RuntimeResult = runtimeResult
	runtimeAssessment, err := run.pipeline.engine.Evaluate(
		run.ast,
		evalContext,
		selected,
		projectRuleLayers(layers, selected),
	)
	if err != nil {
		return err
	}
	run.setAssessment(mergeAssessment(run.response.Assessment, runtimeAssessment))
	return nil
}

func projectRuleLayers(layers engine.RuleLayers, selected []engine.Rule) engine.RuleLayers {
	ids := make(map[string]struct{}, len(selected))
	for _, rule := range selected {
		ids[rule.ID()] = struct{}{}
	}
	return engine.RuleLayers{
		Global:     projectRuleLayer(layers.Global, ids),
		Datasource: projectRuleLayer(layers.Datasource, ids),
		Agent:      projectRuleLayer(layers.Agent, ids),
	}
}

func projectRuleLayer(source engine.RuleLayer, ids map[string]struct{}) engine.RuleLayer {
	if source == nil {
		return nil
	}
	projected := make(engine.RuleLayer)
	for id, config := range source {
		if _, exists := ids[id]; !exists {
			continue
		}
		projected[id] = cloneRuleConfig(config)
	}
	return projected
}

func cloneRuleLayers(source engine.RuleLayers) engine.RuleLayers {
	return engine.RuleLayers{
		Global:     cloneRuleLayer(source.Global),
		Datasource: cloneRuleLayer(source.Datasource),
		Agent:      cloneRuleLayer(source.Agent),
	}
}

func cloneRuleLayer(source engine.RuleLayer) engine.RuleLayer {
	if source == nil {
		return nil
	}
	cloned := make(engine.RuleLayer, len(source))
	for id, config := range source {
		cloned[id] = cloneRuleConfig(config)
	}
	return cloned
}

func cloneRuleConfig(source engine.RuleConfig) engine.RuleConfig {
	cloned := source
	if source.Enabled != nil {
		enabled := *source.Enabled
		cloned.Enabled = &enabled
	}
	if source.Thresholds != nil {
		cloned.Thresholds = make(map[string]float64, len(source.Thresholds))
		for name, value := range source.Thresholds {
			cloned.Thresholds[name] = value
		}
	}
	return cloned
}

func validatePipelineRuleLayers(layers engine.RuleLayers) error {
	known := make(map[string]struct{})
	limiter := &validationLimiter{}
	metadata := panicMetadataProvider{}
	for _, dialect := range []model.DBDialect{"postgres", "mysql"} {
		assembled, err := assembleRules(dialect, limiter, metadata)
		if err != nil {
			return err
		}
		for _, rule := range assembled {
			known[rule.ID()] = struct{}{}
		}
	}
	for layerName, layer := range map[string]engine.RuleLayer{
		"global": layers.Global, "datasource": layers.Datasource, "agent": layers.Agent,
	} {
		for id, config := range layer {
			if strings.TrimSpace(id) == "" {
				return fmt.Errorf("%s rule layer has empty ID", layerName)
			}
			if _, exists := known[id]; !exists {
				return fmt.Errorf("%s rule layer references unknown rule %q", layerName, id)
			}
			for name, value := range config.Thresholds {
				if strings.TrimSpace(name) == "" || math.IsNaN(value) || math.IsInf(value, 0) {
					return fmt.Errorf("%s rule %q has invalid threshold %q", layerName, id, name)
				}
			}
		}
	}
	return nil
}

type validationLimiter struct{}

func (*validationLimiter) Allow(string, float64, int) (rules.RateLimitResult, error) {
	return rules.RateLimitResult{Allowed: true}, nil
}

func (*validationLimiter) Release(string) error { return nil }

func mergeAssessment(first, second model.Assessment) model.Assessment {
	merged := second
	if merged.StmtType == "" {
		merged.StmtType = first.StmtType
	}
	if merged.Normalized == "" {
		merged.Normalized = first.Normalized
	}
	if merged.Objects == nil {
		merged.Objects = append([]model.ObjectRef{}, first.Objects...)
	}
	hitsByID := make(map[string]model.RuleHit, len(first.Hits)+len(second.Hits))
	for _, hit := range first.Hits {
		hitsByID[hit.RuleID] = hit
	}
	for _, hit := range second.Hits {
		hitsByID[hit.RuleID] = hit
	}
	ids := make([]string, 0, len(hitsByID))
	for id := range hitsByID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	merged.Hits = make([]model.RuleHit, 0, len(ids))
	for _, id := range ids {
		merged.Hits = append(merged.Hits, hitsByID[id])
	}
	recomputeAssessmentDecision(&merged)
	return merged
}

func recomputeAssessmentDecision(assessment *model.Assessment) {
	assessment.Decision = model.DecisionAllow
	assessment.Risk = model.RiskInfo
	assessment.Reason = "无规则命中"
	assessment.Suggestion = ""
	winningPriority := decisionPriority(model.DecisionAllow)
	for _, hit := range assessment.Hits {
		priority := decisionPriority(hit.Decision)
		if priority > winningPriority {
			winningPriority = priority
			assessment.Decision = hit.Decision
			assessment.Risk = riskForDecision(hit.Decision)
			assessment.Reason = hit.Message
			assessment.Suggestion = hit.Suggestion
		}
	}
}

func decisionPriority(decision model.Decision) int {
	switch decision {
	case model.DecisionDeny:
		return 3
	case model.DecisionApprove:
		return 2
	case model.DecisionWarn:
		return 1
	case model.DecisionAllow:
		return 0
	default:
		return -1
	}
}

func riskForDecision(decision model.Decision) model.RiskLevel {
	switch decision {
	case model.DecisionDeny:
		return model.RiskDeny
	case model.DecisionApprove:
		return model.RiskApprove
	case model.DecisionWarn:
		return model.RiskWarn
	default:
		return model.RiskInfo
	}
}

var (
	_ rules.MetadataProvider                 = panicMetadataProvider{}
	_ rules.TransactionMetadataProvider      = panicMetadataProvider{}
	_ rules.MysqlTransactionMetadataProvider = panicMetadataProvider{}
	_ rules.RateLimiter                      = (*requestLimiter)(nil)
	_ rules.RateLimiter                      = (*validationLimiter)(nil)
)

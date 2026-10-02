package engine

import (
	"errors"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strings"

	"github.com/cuipengdba/agentsql/internal/model"
)

const noRuleHitReason = "无规则命中"

// Engine evaluates rules without retaining state or performing external I/O.
type Engine struct{}

// Evaluate applies ordered rules and aggregates deny > approve > warn > allow.
func (Engine) Evaluate(
	ast *model.AST,
	evalContext EvalContext,
	rules []Rule,
	layers RuleLayers,
) (model.Assessment, error) {
	assessment := baseAssessment(ast)
	if err := validateAST(ast); err != nil {
		return failedAssessment(assessment, err)
	}

	ruleIndex, err := validateRules(rules)
	if err != nil {
		return failedAssessment(assessment, err)
	}
	if err := validateLayers(layers, ruleIndex); err != nil {
		return failedAssessment(assessment, err)
	}

	winningPriority := decisionPriority(model.DecisionAllow)
	for _, rule := range rules {
		if !ruleApplies(rule.Dialect(), ast.Dialect) {
			continue
		}

		settings := resolveRuleConfig(rule, layers)
		if !settings.enabled && !settings.explicitDeny {
			continue
		}

		contextForRule := cloneEvalContext(evalContext, ast, settings.thresholds)
		result, err := rule.Eval(contextForRule)
		if err != nil {
			return failedAssessment(
				assessment,
				fmt.Errorf("evaluate rule %q: %w", rule.ID(), errors.Join(ErrRuleEvaluation, err)),
			)
		}
		if err := validateRuleResult(rule.ID(), result); err != nil {
			return failedAssessment(assessment, err)
		}
		if settings.explicitDeny {
			result.Decision = model.DecisionDeny
			if strings.TrimSpace(result.Message) == "" {
				result.Message = "规则配置包含不可撤销的显式拒绝"
			}
			if strings.TrimSpace(result.Suggestion) == "" {
				result.Suggestion = "请联系安全管理员调整显式拒绝策略"
			}
		}
		if result.Decision == model.DecisionAllow {
			continue
		}

		hitRisk := rule.Level()
		if settings.explicitDeny {
			hitRisk = model.RiskDeny
		}
		assessment.Hits = append(assessment.Hits, model.RuleHit{
			RuleID:     rule.ID(),
			Risk:       hitRisk,
			Decision:   result.Decision,
			Message:    result.Message,
			Suggestion: result.Suggestion,
		})
		priority := decisionPriority(result.Decision)
		if priority > winningPriority {
			winningPriority = priority
			assessment.Decision = result.Decision
			assessment.Risk = riskForDecision(result.Decision)
			assessment.Reason = result.Message
			assessment.Suggestion = result.Suggestion
		}
	}

	return assessment, nil
}

type resolvedRuleConfig struct {
	enabled      bool
	explicitDeny bool
	thresholds   map[string]float64
}

func resolveRuleConfig(rule Rule, layers RuleLayers) resolvedRuleConfig {
	resolved := resolvedRuleConfig{
		enabled:    rule.Enabled(),
		thresholds: make(map[string]float64),
	}
	for _, layer := range []RuleLayer{layers.Global, layers.Datasource, layers.Agent} {
		config, exists := layer[rule.ID()]
		if !exists {
			continue
		}
		if config.Enabled != nil && !resolved.explicitDeny {
			resolved.enabled = *config.Enabled
		}
		for name, value := range config.Thresholds {
			resolved.thresholds[name] = value
		}
		if config.ExplicitDeny {
			resolved.explicitDeny = true
			resolved.enabled = true
		}
	}
	return resolved
}

func validateAST(ast *model.AST) error {
	if ast == nil {
		return fmt.Errorf("validate AST: %w", ErrInvalidInput)
	}
	if ast.Dialect != model.DBDialect("postgres") && ast.Dialect != model.DBDialect("mysql") {
		return fmt.Errorf("validate AST dialect %q: %w", ast.Dialect, ErrInvalidInput)
	}
	return nil
}

func validateRules(rules []Rule) (map[string]Rule, error) {
	index := make(map[string]Rule, len(rules))
	for position, rule := range rules {
		if isNilRule(rule) {
			return nil, fmt.Errorf("validate rule at position %d: %w", position, ErrInvalidRule)
		}
		id := rule.ID()
		if strings.TrimSpace(id) == "" || strings.TrimSpace(id) != id {
			return nil, fmt.Errorf("validate rule at position %d: empty ID: %w", position, ErrInvalidRule)
		}
		if _, exists := index[id]; exists {
			return nil, fmt.Errorf("validate duplicate rule %q: %w", id, ErrInvalidRule)
		}
		if !validRuleDialect(rule.Dialect()) {
			return nil, fmt.Errorf("validate rule %q dialect %q: %w", id, rule.Dialect(), ErrInvalidRule)
		}
		if rule.Level() < model.RiskDeny || rule.Level() > model.RiskInfo {
			return nil, fmt.Errorf("validate rule %q level %d: %w", id, rule.Level(), ErrInvalidRule)
		}
		index[id] = rule
	}
	return index, nil
}

func isNilRule(rule Rule) bool {
	if rule == nil {
		return true
	}
	value := reflect.ValueOf(rule)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func validateLayers(layers RuleLayers, rules map[string]Rule) error {
	namedLayers := []struct {
		name  string
		layer RuleLayer
	}{
		{name: "global", layer: layers.Global},
		{name: "datasource", layer: layers.Datasource},
		{name: "agent", layer: layers.Agent},
	}
	for _, named := range namedLayers {
		ruleIDs := sortedRuleConfigKeys(named.layer)
		for _, ruleID := range ruleIDs {
			config := named.layer[ruleID]
			if strings.TrimSpace(ruleID) == "" {
				return fmt.Errorf("validate %s layer empty rule ID: %w", named.name, ErrInvalidRuleConfig)
			}
			if _, exists := rules[ruleID]; !exists {
				return fmt.Errorf("validate %s layer missing rule %q: %w", named.name, ruleID, ErrInvalidRuleConfig)
			}
			thresholdNames := sortedThresholdKeys(config.Thresholds)
			for _, threshold := range thresholdNames {
				value := config.Thresholds[threshold]
				if strings.TrimSpace(threshold) == "" || math.IsNaN(value) || math.IsInf(value, 0) {
					return fmt.Errorf(
						"validate %s layer rule %q threshold %q: %w",
						named.name,
						ruleID,
						threshold,
						ErrInvalidRuleConfig,
					)
				}
			}
		}
	}
	return nil
}

func sortedRuleConfigKeys(layer RuleLayer) []string {
	keys := make([]string, 0, len(layer))
	for key := range layer {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedThresholdKeys(thresholds map[string]float64) []string {
	keys := make([]string, 0, len(thresholds))
	for key := range thresholds {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func validateRuleResult(ruleID string, result RuleResult) error {
	if decisionPriority(result.Decision) < 0 {
		return fmt.Errorf(
			"validate rule %q decision %q: %w",
			ruleID,
			result.Decision,
			ErrRuleEvaluation,
		)
	}
	if result.Decision != model.DecisionAllow && strings.TrimSpace(result.Message) == "" {
		return fmt.Errorf("validate rule %q empty message: %w", ruleID, ErrRuleEvaluation)
	}
	if result.Decision != model.DecisionAllow && strings.TrimSpace(result.Suggestion) == "" {
		return fmt.Errorf("validate rule %q empty suggestion: %w", ruleID, ErrRuleEvaluation)
	}
	return nil
}

func validRuleDialect(dialect model.DBDialect) bool {
	return dialect == model.DBDialect("postgres") ||
		dialect == model.DBDialect("mysql") ||
		dialect == DialectAll
}

func ruleApplies(ruleDialect, statementDialect model.DBDialect) bool {
	return ruleDialect == DialectAll || ruleDialect == statementDialect
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

func baseAssessment(ast *model.AST) model.Assessment {
	// T13 replaces this deterministic placeholder with measured pipeline timing.
	assessment := model.Assessment{
		Decision:     model.DecisionAllow,
		Risk:         model.RiskInfo,
		Hits:         []model.RuleHit{},
		Reason:       noRuleHitReason,
		StageLatency: map[string]int64{guardStage: 0},
	}
	if ast == nil {
		return assessment
	}
	assessment.StmtType = ast.StmtType
	assessment.Normalized = ast.Normalized
	assessment.Objects = append([]model.ObjectRef{}, ast.Tables...)
	if ast.Explain != nil {
		assessment.EstScanRows = ast.Explain.EstScanRows
	}
	return assessment
}

func failedAssessment(assessment model.Assessment, cause error) (model.Assessment, error) {
	assessment.Decision = model.DecisionDeny
	assessment.Risk = model.RiskDeny
	assessment.Reason = cause.Error()
	assessment.Suggestion = "请修正规则配置或评估异常后重试"
	return assessment, fmt.Errorf("engine evaluation failed: %w", cause)
}

func cloneEvalContext(source EvalContext, ast *model.AST, thresholds map[string]float64) EvalContext {
	cloned := source
	cloned.AST = cloneAST(ast)
	cloned.Agent = cloneAgent(source.Agent)
	cloned.Datasource = cloneDatasource(source.Datasource)
	cloned.Policy = clonePolicyDecision(source.Policy)
	cloned.Thresholds = cloneThresholds(thresholds)
	cloned.RuntimeResult = cloneQueryResult(source.RuntimeResult)
	return cloned
}

func cloneQueryResult(source *model.QueryResult) *model.QueryResult {
	if source == nil {
		return nil
	}
	cloned := *source
	cloned.Columns = append([]string{}, source.Columns...)
	cloned.Rows = make([][]string, len(source.Rows))
	for index := range source.Rows {
		cloned.Rows[index] = append([]string{}, source.Rows[index]...)
	}
	return &cloned
}

func cloneAgent(source *model.Agent) *model.Agent {
	if source == nil {
		return nil
	}
	cloned := *source
	if source.Owner != nil {
		owner := *source.Owner
		cloned.Owner = &owner
	}
	if source.ExpiresAt != nil {
		expiresAt := *source.ExpiresAt
		cloned.ExpiresAt = &expiresAt
	}
	return &cloned
}

func cloneDatasource(source *model.Datasource) *model.Datasource {
	if source == nil {
		return nil
	}
	cloned := *source
	return &cloned
}

func clonePolicyDecision(source *model.PolicyDecision) *model.PolicyDecision {
	if source == nil {
		return nil
	}
	cloned := *source
	cloned.AllowedTables = append([]string{}, source.AllowedTables...)
	cloned.DeniedTables = append([]string{}, source.DeniedTables...)
	if source.ColumnACL != nil {
		cloned.ColumnACL = make(map[string][]string, len(source.ColumnACL))
		for object, columns := range source.ColumnACL {
			cloned.ColumnACL[object] = append([]string{}, columns...)
		}
	}
	return &cloned
}

func cloneAST(source *model.AST) *model.AST {
	if source == nil {
		return nil
	}
	cloned := *source
	cloned.Tables = append([]model.ObjectRef{}, source.Tables...)
	cloned.Columns = append([]string{}, source.Columns...)
	cloned.DirectProjections = append([]model.DirectProjectionRef{}, source.DirectProjections...)
	cloned.ProjectionLineages = cloneProjectionLineages(source.ProjectionLineages)
	cloned.Functions = append([]string{}, source.Functions...)
	cloned.Operations = append([]string{}, source.Operations...)
	if source.Explain != nil {
		explain := *source.Explain
		cloned.Explain = &explain
	}
	return &cloned
}

func cloneProjectionLineages(source []model.ProjectionLineage) []model.ProjectionLineage {
	if source == nil {
		return nil
	}
	cloned := make([]model.ProjectionLineage, len(source))
	for lineageIndex := range source {
		cloned[lineageIndex] = source[lineageIndex]
		if source[lineageIndex].Arms == nil {
			continue
		}
		cloned[lineageIndex].Arms = make([]model.LineageArm, len(source[lineageIndex].Arms))
		for armIndex := range source[lineageIndex].Arms {
			cloned[lineageIndex].Arms[armIndex] = source[lineageIndex].Arms[armIndex]
			cloned[lineageIndex].Arms[armIndex].Dependencies = append(
				[]model.ColumnDependency(nil),
				source[lineageIndex].Arms[armIndex].Dependencies...,
			)
			cloned[lineageIndex].Arms[armIndex].PossibleRelations = append(
				[]model.ObjectRef(nil),
				source[lineageIndex].Arms[armIndex].PossibleRelations...,
			)
		}
	}
	return cloned
}

func cloneThresholds(source map[string]float64) map[string]float64 {
	cloned := make(map[string]float64, len(source))
	for name, value := range source {
		cloned[name] = value
	}
	return cloned
}

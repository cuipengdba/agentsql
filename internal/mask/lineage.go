package mask

import (
	"strings"

	"github.com/cuipengdba/agentsql/internal/model"
)

type effectiveLineageRule struct {
	rule  redactorRule
	scope string
}

type lineageArmDecision struct {
	rule       *effectiveLineageRule
	origin     model.ColumnOrigin
	relevant   []effectiveLineageRule
	exact      bool
	uncertain  bool
	forceBlock bool
}

type lineageColumnDecision struct {
	rule          redactorRule
	matched       bool
	block         bool
	unresolved    bool
	sensitiveType SensitiveType
}

// ApplyWithProjectionLineages is deliberately separate from all legacy apply
// paths. The production pipeline switches to this interface only after both
// dialects emit complete lineage.
func (redactor *resultRedactor) ApplyWithProjectionLineages(
	result model.QueryResult,
	aligned [][]model.LineageArm,
) (model.QueryResult, RedactReport) {
	copyResult := cloneQueryResult(result)
	report := RedactReport{}
	if redactor == nil || !redactor.hasRules() || len(copyResult.Columns) == 0 {
		return copyResult, report
	}

	for columnIndex, columnName := range copyResult.Columns {
		arms := []model.LineageArm(nil)
		if columnIndex < len(aligned) {
			arms = aligned[columnIndex]
		}
		if len(arms) == 0 {
			arms = []model.LineageArm{{
				Kind:      model.LineageOpaque,
				Operation: "alignment_failure",
				Status:    model.LineageOpaqueState,
			}}
		}
		decision := redactor.decideProjectionColumn(columnName, arms)
		if !decision.matched {
			continue
		}
		report.touchColumn(columnIndex, decision.sensitiveType)
		maskedCells := 0
		for rowIndex := range copyResult.Rows {
			if columnIndex >= len(copyResult.Rows[rowIndex]) {
				continue
			}
			value := copyResult.Rows[rowIndex][columnIndex]
			var masked string
			var changed bool
			if decision.block {
				masked, changed = applyUnresolvedScopedBlock(value)
			} else {
				masked, changed = applyRule(decision.rule, value, redactor.hasher)
			}
			if !changed {
				continue
			}
			copyResult.Rows[rowIndex][columnIndex] = masked
			report.MaskedCells++
			maskedCells++
		}
		if decision.unresolved && maskedCells > 0 {
			if report.UnresolvedScopedColumns == nil {
				report.UnresolvedScopedColumns = make(map[int]SensitiveType)
			}
			report.UnresolvedScopedColumns[columnIndex] = decision.sensitiveType
		}
	}
	return copyResult, report
}

func (redactor *resultRedactor) decideProjectionColumn(
	resultName string,
	arms []model.LineageArm,
) lineageColumnDecision {
	global, hasGlobal := redactor.globalResultRule(resultName)
	if len(arms) == 0 {
		if !hasGlobal {
			return lineageColumnDecision{}
		}
		return blockLineageDecision([]effectiveLineageRule{global}, true)
	}

	decisions := make([]lineageArmDecision, len(arms))
	allRelevant := make([]effectiveLineageRule, 0, len(arms)+1)
	if hasGlobal {
		allRelevant = append(allRelevant, global)
	}
	anyRelevant := hasGlobal
	anyUncertain := false
	for index, arm := range arms {
		decisions[index] = redactor.decideLineageArm(arm, global, hasGlobal)
		allRelevant = append(allRelevant, decisions[index].relevant...)
		anyRelevant = anyRelevant || len(decisions[index].relevant) > 0
		anyUncertain = anyUncertain || decisions[index].uncertain
	}
	if !anyRelevant {
		return lineageColumnDecision{}
	}

	if len(arms) == 1 {
		decision := decisions[0]
		if decision.forceBlock || !decision.exact || decision.rule == nil {
			return blockLineageDecision(allRelevant, decision.uncertain)
		}
		return exactLineageDecision(*decision.rule)
	}

	first := decisions[0]
	if first.forceBlock || !first.exact || first.rule == nil {
		return blockLineageDecision(allRelevant, anyUncertain)
	}
	for index := 1; index < len(decisions); index++ {
		current := decisions[index]
		if current.forceBlock || !current.exact || current.rule == nil ||
			!samePhysicalOrigin(first.origin, current.origin) ||
			!sameEffectiveRule(*first.rule, *current.rule) {
			return blockLineageDecision(allRelevant, anyUncertain)
		}
	}
	return exactLineageDecision(*first.rule)
}

func (redactor *resultRedactor) decideLineageArm(
	arm model.LineageArm,
	global effectiveLineageRule,
	hasGlobal bool,
) lineageArmDecision {
	decision := lineageArmDecision{}
	if arm.Status != model.LineageResolved && arm.Status != model.LineageSourceFree {
		decision.uncertain = true
		decision.relevant = redactor.rulesForDependencies(arm.Dependencies)
		decision.relevant = append(decision.relevant, redactor.rulesForPossibleRelations(arm.PossibleRelations)...)
		if len(arm.Dependencies) == 0 && len(arm.PossibleRelations) == 0 {
			decision.relevant = append(decision.relevant, redactor.allConfiguredRules()...)
		}
		if hasGlobal {
			decision.relevant = append(decision.relevant, global)
		}
		decision.forceBlock = len(decision.relevant) > 0
		return decision
	}
	if arm.Status == model.LineageSourceFree {
		validSourceFree := len(arm.Dependencies) == 0 &&
			(arm.Kind == model.LineageConstant ||
				(arm.Kind == model.LineageAggregate && isSourceFreeCount(arm.Operation)))
		if hasGlobal {
			decision.relevant = append(decision.relevant, global)
			if validSourceFree {
				decision.rule = &global
				decision.exact = true
			} else {
				decision.forceBlock = true
			}
			return decision
		}
		if !validSourceFree {
			decision.relevant = redactor.rulesForDependencies(arm.Dependencies)
			decision.forceBlock = len(decision.relevant) > 0
		}
		return decision
	}

	dependencyRules := redactor.rulesForDependencies(arm.Dependencies)
	decision.relevant = append(decision.relevant, dependencyRules...)
	if arm.Kind == model.LineageOpaque || arm.Kind == model.LineageWildcard || len(arm.Dependencies) == 0 {
		decision.relevant = append(decision.relevant, redactor.rulesForPossibleRelations(arm.PossibleRelations)...)
	}
	if hasGlobal {
		decision.relevant = append(decision.relevant, global)
	}

	if arm.Kind != model.LineageDirect && arm.Kind != model.LineageTransparent {
		decision.forceBlock = len(decision.relevant) > 0
		return decision
	}
	if arm.Kind == model.LineageTransparent && !isTransparentOperation(arm.Operation) {
		decision.forceBlock = len(decision.relevant) > 0
		return decision
	}
	if len(arm.Dependencies) != 1 || arm.Dependencies[0].Role != model.DependencyValue {
		decision.forceBlock = len(decision.relevant) > 0
		return decision
	}

	decision.origin = physicalOrigin(arm.Dependencies[0].Origin)
	sourceRule, hasSourceRule := redactor.ruleForOrigin(decision.origin)
	if hasSourceRule && hasGlobal && !sameEffectiveRule(sourceRule, global) {
		decision.forceBlock = true
		return decision
	}
	if hasSourceRule {
		decision.rule = &sourceRule
	} else if hasGlobal {
		decision.rule = &global
	} else {
		return decision
	}
	if arm.Kind == model.LineageTransparent && isEmptyConcatOperation(arm.Operation) && decision.rule.rule.algorithm == AlgoRange {
		decision.forceBlock = true
		return decision
	}
	decision.exact = true
	return decision
}

func (redactor *resultRedactor) allConfiguredRules() []effectiveLineageRule {
	rules := make([]effectiveLineageRule, 0, len(redactor.globalRules)+len(redactor.exactScopedRules)+len(redactor.wildcardScopedRules))
	for key, rule := range redactor.globalRules {
		rules = append(rules, effectiveLineageRule{rule: rule, scope: "global:" + key})
	}
	for key, rule := range redactor.exactScopedRules {
		rules = append(rules, effectiveLineageRule{rule: rule, scope: "exact:" + key})
	}
	for key, rule := range redactor.wildcardScopedRules {
		rules = append(rules, effectiveLineageRule{rule: rule, scope: "table:" + key})
	}
	return rules
}

func (redactor *resultRedactor) globalResultRule(resultName string) (effectiveLineageRule, bool) {
	column := normalizeColumnName(resultName)
	rule, matched := redactor.globalRules[column]
	if !matched {
		return effectiveLineageRule{}, false
	}
	return effectiveLineageRule{rule: rule, scope: "global:" + column}, true
}

func (redactor *resultRedactor) ruleForOrigin(origin model.ColumnOrigin) (effectiveLineageRule, bool) {
	column := normalizeColumnName(origin.Column)
	if column == "" {
		return effectiveLineageRule{}, false
	}
	relation := origin.Relation
	if relation.Table != "" {
		if relation.Schema != "" {
			key := exactScopedRuleKey(relation.Schema, relation.Table, column)
			if rule, matched := redactor.exactScopedRules[key]; matched {
				return effectiveLineageRule{rule: rule, scope: "exact:" + key}, true
			}
		}
		key := wildcardScopedRuleKey(relation.Table, column)
		if rule, matched := redactor.wildcardScopedRules[key]; matched {
			return effectiveLineageRule{rule: rule, scope: "table:" + key}, true
		}
	}
	if rule, matched := redactor.globalRules[column]; matched {
		return effectiveLineageRule{rule: rule, scope: "global:" + column}, true
	}
	return effectiveLineageRule{}, false
}

func (redactor *resultRedactor) rulesForDependencies(dependencies []model.ColumnDependency) []effectiveLineageRule {
	rules := make([]effectiveLineageRule, 0, len(dependencies))
	for _, dependency := range dependencies {
		if rule, matched := redactor.ruleForOrigin(physicalOrigin(dependency.Origin)); matched {
			rules = append(rules, rule)
		}
	}
	return rules
}

func (redactor *resultRedactor) rulesForPossibleRelations(relations []model.ObjectRef) []effectiveLineageRule {
	rules := make([]effectiveLineageRule, 0)
	for key, rule := range redactor.exactScopedRules {
		if scopedRuleRelationPossible(rule, relations) {
			rules = append(rules, effectiveLineageRule{rule: rule, scope: "exact:" + key})
		}
	}
	for key, rule := range redactor.wildcardScopedRules {
		if scopedRuleRelationPossible(rule, relations) {
			rules = append(rules, effectiveLineageRule{rule: rule, scope: "table:" + key})
		}
	}
	return rules
}

func exactLineageDecision(rule effectiveLineageRule) lineageColumnDecision {
	return lineageColumnDecision{
		rule:          rule.rule,
		matched:       true,
		sensitiveType: rule.rule.sensitiveType,
	}
}

func blockLineageDecision(rules []effectiveLineageRule, unresolved bool) lineageColumnDecision {
	return lineageColumnDecision{
		matched:       true,
		block:         true,
		unresolved:    unresolved,
		sensitiveType: preferredSensitiveType(rules),
	}
}

func preferredSensitiveType(rules []effectiveLineageRule) SensitiveType {
	selected := TypeGeneric
	found := false
	for _, rule := range rules {
		if !found || SensitiveTypeOrder(rule.rule.sensitiveType) < SensitiveTypeOrder(selected) {
			selected = rule.rule.sensitiveType
			found = true
		}
	}
	return selected
}

func sameEffectiveRule(left, right effectiveLineageRule) bool {
	return left.scope == right.scope &&
		left.rule.schema == right.rule.schema &&
		left.rule.table == right.rule.table &&
		left.rule.column == right.rule.column &&
		left.rule.sensitiveType == right.rule.sensitiveType &&
		left.rule.algorithm == right.rule.algorithm &&
		left.rule.rangeWidth == right.rule.rangeWidth &&
		left.rule.rangeOffset == right.rule.rangeOffset &&
		left.rule.rangeGranularity == right.rule.rangeGranularity
}

func samePhysicalOrigin(left, right model.ColumnOrigin) bool {
	return left.Relation.Schema == right.Relation.Schema &&
		left.Relation.Table == right.Relation.Table &&
		normalizeColumnName(left.Column) == normalizeColumnName(right.Column)
}

func physicalOrigin(origin model.ColumnOrigin) model.ColumnOrigin {
	origin.Relation.Alias = ""
	return origin
}

func isTransparentOperation(operation string) bool {
	switch strings.ToLower(strings.TrimSpace(operation)) {
	case "parentheses", "collate", "coalesce_null", "concat_empty":
		return true
	default:
		return false
	}
}

func isEmptyConcatOperation(operation string) bool {
	return strings.EqualFold(strings.TrimSpace(operation), "concat_empty")
}

func isSourceFreeCount(operation string) bool {
	switch strings.ToLower(strings.TrimSpace(operation)) {
	case "aggregate:count", "aggregate:count_star", "aggregate:count_constant":
		return true
	default:
		return false
	}
}

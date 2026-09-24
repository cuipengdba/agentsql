package mask

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/cuipengdba/agentsql/internal/model"
)

var (
	ErrIdentityMeetUndefined = errors.New("identity-only mask meet is undefined")
	ErrIdentityCapability    = errors.New("mask identity capability is unavailable")
	ErrIdentityPlanInvalid   = errors.New("mask identity plan is invalid")
)

// PhysicalColumn contains only catalog identity. It cannot carry a cell value
// and therefore keeps mask selection independent of sensitive data.
type PhysicalColumn struct {
	Schema    string
	Table     string
	Column    string
	InputType string
}

// Identity is the exact equality key for the S4 meet operation.
type Identity struct {
	AlgorithmID         string
	SemanticVersion     string
	KeyID               string
	KeyVersion          int
	CanonicalParameters string
	InputType           string
	OutputType          string
}

type IdentityRequest struct {
	OutputIndex int
	ResultName  string
	Sources     []PhysicalColumn
}

// IdentityPlan is produced by a redactor and can only be executed by that
// same redactor. The concrete transform stays private so an outer layer cannot
// replace a proven identity with a different rule.
type IdentityPlan struct {
	OutputIndex int
	Identity    Identity
	rule        redactorRule
}

type IdentityOnlyRedactor interface {
	Redactor
	BuildIdentityPlan([]IdentityRequest) ([]IdentityPlan, error)
	ApplyIdentityPlan(model.QueryResult, []IdentityPlan) (model.QueryResult, RedactReport, error)
}

func (redactor *resultRedactor) BuildIdentityPlan(requests []IdentityRequest) ([]IdentityPlan, error) {
	if redactor == nil {
		return nil, ErrIdentityCapability
	}
	ordered := append([]IdentityRequest(nil), requests...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].OutputIndex < ordered[j].OutputIndex })
	seenPositions := make(map[int]struct{}, len(ordered))
	plans := make([]IdentityPlan, 0, len(ordered))
	for _, request := range ordered {
		if request.OutputIndex < 0 {
			return nil, ErrIdentityPlanInvalid
		}
		if _, duplicate := seenPositions[request.OutputIndex]; duplicate {
			return nil, ErrIdentityPlanInvalid
		}
		seenPositions[request.OutputIndex] = struct{}{}
		matches := redactor.identityMatches(request)
		if len(matches) == 0 {
			continue
		}
		var selected redactorRule
		var identity Identity
		for index, match := range matches {
			candidate, err := redactor.ruleIdentity(match, matchedRuleSources(request, match))
			if err != nil {
				return nil, err
			}
			if index == 0 {
				selected, identity = match, candidate
				continue
			}
			if identity != candidate {
				return nil, ErrIdentityMeetUndefined
			}
		}
		plans = append(plans, IdentityPlan{OutputIndex: request.OutputIndex, Identity: identity, rule: selected})
	}
	return plans, nil
}

func (redactor *resultRedactor) identityMatches(request IdentityRequest) []redactorRule {
	resultName := normalizeColumnName(request.ResultName)
	matches := make(map[string]redactorRule)
	add := func(rule redactorRule) {
		matches[identityRuleKey(rule)] = rule
	}
	if rule, ok := redactor.globalRules[resultName]; ok {
		add(rule)
	}
	for _, source := range request.Sources {
		column := normalizeColumnName(source.Column)
		if source.Schema != "" {
			if rule, ok := redactor.exactScopedRules[exactScopedRuleKey(source.Schema, source.Table, column)]; ok {
				add(rule)
			}
		}
		if rule, ok := redactor.wildcardScopedRules[wildcardScopedRuleKey(source.Table, column)]; ok {
			add(rule)
		}
		if rule, ok := redactor.globalRules[column]; ok {
			add(rule)
		}
	}
	keys := make([]string, 0, len(matches))
	for key := range matches {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]redactorRule, 0, len(keys))
	for _, key := range keys {
		result = append(result, matches[key])
	}
	return result
}

func (redactor *resultRedactor) ruleIdentity(rule redactorRule, sources []PhysicalColumn) (Identity, error) {
	inputType := ""
	for _, source := range sources {
		if source.InputType == "" {
			return Identity{}, ErrIdentityPlanInvalid
		}
		if inputType == "" {
			inputType = source.InputType
		} else if inputType != source.InputType {
			return Identity{}, ErrIdentityMeetUndefined
		}
	}
	if inputType == "" {
		return Identity{}, ErrIdentityPlanInvalid
	}
	return redactor.identityForInputType(rule, inputType)
}

func (redactor *resultRedactor) identityForInputType(rule redactorRule, inputType string) (Identity, error) {
	if inputType == "" {
		return Identity{}, ErrIdentityPlanInvalid
	}
	identity := Identity{
		AlgorithmID: rule.algorithm.String(), SemanticVersion: "1",
		InputType: inputType, OutputType: "text",
		CanonicalParameters: canonicalRuleParameters(rule),
	}
	if rule.algorithm == AlgoHash {
		if redactor.active == nil {
			return Identity{}, ErrIdentityCapability
		}
		identity.KeyID = "agentsql-redaction"
		identity.KeyVersion = redactor.activeVersion
		if identity.KeyVersion == 0 {
			identity.KeyVersion = 1
		}
	}
	return identity, nil
}

// matchedRuleSources keeps the meet identity-only while excluding lineage
// contributors to which this particular scoped rule does not apply. A global
// rule selected by the result name applies to every contributor; a global
// rule selected by a physical column applies only to matching columns.
func matchedRuleSources(request IdentityRequest, rule redactorRule) []PhysicalColumn {
	resultName := normalizeColumnName(request.ResultName)
	result := make([]PhysicalColumn, 0, len(request.Sources))
	for _, source := range request.Sources {
		column := normalizeColumnName(source.Column)
		matched := false
		switch {
		case rule.table == "":
			matched = resultName == rule.column || column == rule.column
		case rule.schema == "":
			matched = source.Table == rule.table && column == rule.column
		default:
			matched = source.Schema == rule.schema && source.Table == rule.table && column == rule.column
		}
		if matched {
			result = append(result, source)
		}
	}
	return result
}

func (algorithm Algorithm) String() string { return string(algorithm) }

func canonicalRuleParameters(rule redactorRule) string {
	return strings.Join([]string{
		"type=" + string(rule.sensitiveType),
		"width=" + strconv.FormatInt(rule.rangeWidth, 10),
		"offset=" + strconv.FormatInt(rule.rangeOffset, 10),
		"granularity=" + string(rule.rangeGranularity),
	}, ";")
}

func identityRuleKey(rule redactorRule) string {
	return strings.Join([]string{rule.schema, rule.table, rule.column, string(rule.sensitiveType),
		string(rule.algorithm), canonicalRuleParameters(rule)}, "\x00")
}

func (redactor *resultRedactor) ApplyIdentityPlan(result model.QueryResult, plans []IdentityPlan) (model.QueryResult, RedactReport, error) {
	copyResult := cloneQueryResult(result)
	report := RedactReport{}
	if redactor == nil {
		return model.QueryResult{}, report, ErrIdentityCapability
	}
	seen := make(map[int]struct{}, len(plans))
	for _, plan := range plans {
		position := plan.OutputIndex
		if position < 0 || position >= len(copyResult.Columns) {
			return model.QueryResult{}, RedactReport{}, ErrIdentityPlanInvalid
		}
		if _, duplicate := seen[position]; duplicate {
			return model.QueryResult{}, RedactReport{}, ErrIdentityPlanInvalid
		}
		seen[position] = struct{}{}
		identity, err := redactor.identityForInputType(plan.rule, plan.Identity.InputType)
		if err != nil || identity != plan.Identity {
			return model.QueryResult{}, RedactReport{}, ErrIdentityPlanInvalid
		}
		report.touchColumn(position, plan.rule.sensitiveType)
		for rowIndex := range copyResult.Rows {
			if position >= len(copyResult.Rows[rowIndex]) {
				return model.QueryResult{}, RedactReport{}, fmt.Errorf("%w: non-rectangular result", ErrIdentityPlanInvalid)
			}
			masked, changed, hashFallback := applyRule(plan.rule, copyResult.Rows[rowIndex][position], redactor.active)
			if hashFallback {
				return model.QueryResult{}, RedactReport{}, ErrIdentityCapability
			}
			if changed {
				copyResult.Rows[rowIndex][position] = masked
				report.MaskedCells++
				if plan.rule.algorithm == AlgoHash {
					redactor.markHashVersion(&report)
				}
			}
		}
	}
	return copyResult, report, nil
}

var _ IdentityOnlyRedactor = (*resultRedactor)(nil)

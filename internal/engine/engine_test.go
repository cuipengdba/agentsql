package engine

import (
	"errors"
	"math"
	"sync"
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

var errFakeRule = errors.New("fake rule failure")

type fakeRule struct {
	id      string
	dialect model.DBDialect
	level   model.RiskLevel
	enabled bool
	eval    func(EvalContext) (RuleResult, error)
}

func (rule fakeRule) ID() string {
	return rule.id
}

func (rule fakeRule) Dialect() model.DBDialect {
	return rule.dialect
}

func (rule fakeRule) Level() model.RiskLevel {
	return rule.level
}

func (rule fakeRule) Enabled() bool {
	return rule.enabled
}

func (rule fakeRule) Eval(context EvalContext) (RuleResult, error) {
	return rule.eval(context)
}

func TestEmptyRuleSetAllowsWithNoHits(t *testing.T) {
	assessment, err := (Engine{}).Evaluate(testAST(), EvalContext{}, nil, RuleLayers{})

	require.NoError(t, err)
	require.Equal(t, model.DecisionAllow, assessment.Decision)
	require.Equal(t, model.RiskInfo, assessment.Risk)
	require.Empty(t, assessment.Hits)
	require.Equal(t, noRuleHitReason, assessment.Reason)
	require.Equal(t, map[string]int64{guardStage: 0}, assessment.StageLatency)
	require.Equal(t, model.StmtType("SELECT"), assessment.StmtType)
	require.Equal(t, "SELECT id FROM users", assessment.Normalized)
	require.Equal(t, []model.ObjectRef{{Table: "users"}}, assessment.Objects)
	require.Equal(t, int64(25), assessment.EstScanRows)
}

func TestAllowRuleProducesNoHit(t *testing.T) {
	assessment, err := (Engine{}).Evaluate(
		testAST(),
		EvalContext{},
		[]Rule{resultRule("allow", model.RiskInfo, model.DecisionAllow)},
		RuleLayers{},
	)

	require.NoError(t, err)
	require.Equal(t, model.DecisionAllow, assessment.Decision)
	require.Empty(t, assessment.Hits)
	require.Equal(t, noRuleHitReason, assessment.Reason)
}

func TestDenyIsNotOverriddenByLaterWarnOrAllow(t *testing.T) {
	rules := []Rule{
		resultRule("deny", model.RiskDeny, model.DecisionDeny),
		resultRule("warn", model.RiskWarn, model.DecisionWarn),
		resultRule("allow", model.RiskInfo, model.DecisionAllow),
	}

	assessment, err := (Engine{}).Evaluate(testAST(), EvalContext{}, rules, RuleLayers{})

	require.NoError(t, err)
	require.Equal(t, model.DecisionDeny, assessment.Decision)
	require.Equal(t, model.RiskDeny, assessment.Risk)
	require.Equal(t, []string{"deny", "warn"}, hitIDs(assessment.Hits))
	require.Equal(t, model.RuleHit{
		RuleID:     "deny",
		Risk:       model.RiskDeny,
		Decision:   model.DecisionDeny,
		Message:    "deny message",
		Suggestion: "deny suggestion",
	}, assessment.Hits[0])
	require.Equal(t, model.DecisionWarn, assessment.Hits[1].Decision)
	require.Equal(t, "deny message", assessment.Reason)
}

func TestApproveOutranksWarn(t *testing.T) {
	rules := []Rule{
		resultRule("warn", model.RiskWarn, model.DecisionWarn),
		resultRule("approve", model.RiskApprove, model.DecisionApprove),
	}

	assessment, err := (Engine{}).Evaluate(testAST(), EvalContext{}, rules, RuleLayers{})

	require.NoError(t, err)
	require.Equal(t, model.DecisionApprove, assessment.Decision)
	require.Equal(t, model.RiskApprove, assessment.Risk)
	require.Equal(t, []string{"warn", "approve"}, hitIDs(assessment.Hits))
	require.Equal(t, "approve message", assessment.Reason)
}

func TestRulesExecuteAndHitsRemainInInputOrder(t *testing.T) {
	var executionOrder []string
	rules := make([]Rule, 0, 3)
	for _, id := range []string{"first", "second", "third"} {
		ruleID := id
		rules = append(rules, fakeRule{
			id:      ruleID,
			dialect: DialectAll,
			level:   model.RiskWarn,
			enabled: true,
			eval: func(EvalContext) (RuleResult, error) {
				executionOrder = append(executionOrder, ruleID)
				return nonAllowResult(ruleID, model.DecisionWarn), nil
			},
		})
	}

	assessment, err := (Engine{}).Evaluate(testAST(), EvalContext{}, rules, RuleLayers{})

	require.NoError(t, err)
	require.Equal(t, []string{"first", "second", "third"}, executionOrder)
	require.Equal(t, executionOrder, hitIDs(assessment.Hits))
}

func TestThreeLayerOverridesMergeEnabledAndThresholds(t *testing.T) {
	var received EvalContext
	rule := fakeRule{
		id:      "layered",
		dialect: DialectAll,
		level:   model.RiskWarn,
		enabled: false,
		eval: func(context EvalContext) (RuleResult, error) {
			received = context
			return nonAllowResult("layered", model.DecisionWarn), nil
		},
	}
	layers := RuleLayers{
		Global: RuleLayer{
			"layered": {
				Enabled: boolPointer(true),
				Thresholds: map[string]float64{
					"scan_rows":   100,
					"union_count": 2,
				},
			},
		},
		Datasource: RuleLayer{
			"layered": {
				Enabled: boolPointer(false),
				Thresholds: map[string]float64{
					"scan_rows": 200,
				},
			},
		},
		Agent: RuleLayer{
			"layered": {
				Enabled: boolPointer(true),
				Thresholds: map[string]float64{
					"scan_rows": 300,
				},
			},
		},
	}

	assessment, err := (Engine{}).Evaluate(testAST(), EvalContext{AgentLevel: "readonly"}, []Rule{rule}, layers)

	require.NoError(t, err)
	require.Equal(t, model.DecisionWarn, assessment.Decision)
	require.Equal(t, "readonly", received.AgentLevel)
	require.Equal(t, map[string]float64{"scan_rows": 300, "union_count": 2}, received.Thresholds)
}

func TestExplicitDenyCannotBeDisabledOrOverridden(t *testing.T) {
	tests := []struct {
		name   string
		layers RuleLayers
	}{
		{
			name: "global deny survives later disables",
			layers: RuleLayers{
				Global:     RuleLayer{"sticky": {ExplicitDeny: true}},
				Datasource: RuleLayer{"sticky": {Enabled: boolPointer(false)}},
				Agent:      RuleLayer{"sticky": {Enabled: boolPointer(false)}},
			},
		},
		{
			name: "datasource deny survives agent disable",
			layers: RuleLayers{
				Datasource: RuleLayer{"sticky": {ExplicitDeny: true}},
				Agent:      RuleLayer{"sticky": {Enabled: boolPointer(false)}},
			},
		},
		{
			name: "agent deny overrides disabled defaults",
			layers: RuleLayers{
				Global: RuleLayer{"sticky": {Enabled: boolPointer(false)}},
				Agent:  RuleLayer{"sticky": {ExplicitDeny: true}},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			evaluationCount := 0
			rule := fakeRule{
				id:      "sticky",
				dialect: DialectAll,
				level:   model.RiskWarn,
				enabled: false,
				eval: func(EvalContext) (RuleResult, error) {
					evaluationCount++
					return RuleResult{Decision: model.DecisionAllow}, nil
				},
			}

			assessment, err := (Engine{}).Evaluate(testAST(), EvalContext{}, []Rule{rule}, test.layers)

			require.NoError(t, err)
			require.Equal(t, 1, evaluationCount)
			require.Equal(t, model.DecisionDeny, assessment.Decision)
			require.Len(t, assessment.Hits, 1)
			require.Equal(t, model.DecisionDeny, assessment.Hits[0].Decision)
			require.Equal(t, model.RiskDeny, assessment.Hits[0].Risk)
			require.NotEmpty(t, assessment.Hits[0].Message)
			require.NotEmpty(t, assessment.Hits[0].Suggestion)
		})
	}
}

func TestAnomaliesFailClosed(t *testing.T) {
	var typedNilRule *fakeRule
	tests := []struct {
		name        string
		ast         *model.AST
		rules       []Rule
		layers      RuleLayers
		targetError error
	}{
		{
			name:        "nil AST",
			ast:         nil,
			targetError: ErrInvalidInput,
		},
		{
			name:        "typed nil rule",
			ast:         testAST(),
			rules:       []Rule{typedNilRule},
			targetError: ErrInvalidRule,
		},
		{
			name: "duplicate rule ID",
			ast:  testAST(),
			rules: []Rule{
				resultRule("duplicate", model.RiskWarn, model.DecisionWarn),
				resultRule("duplicate", model.RiskWarn, model.DecisionWarn),
			},
			targetError: ErrInvalidRule,
		},
		{
			name: "missing configured rule",
			ast:  testAST(),
			layers: RuleLayers{
				Global: RuleLayer{"missing": {}},
			},
			targetError: ErrInvalidRuleConfig,
		},
		{
			name:  "invalid threshold",
			ast:   testAST(),
			rules: []Rule{resultRule("configured", model.RiskWarn, model.DecisionWarn)},
			layers: RuleLayers{
				Global: RuleLayer{
					"configured": {Thresholds: map[string]float64{"scan_rows": math.NaN()}},
				},
			},
			targetError: ErrInvalidRuleConfig,
		},
		{
			name: "rule evaluation error",
			ast:  testAST(),
			rules: []Rule{fakeRule{
				id:      "broken",
				dialect: DialectAll,
				level:   model.RiskDeny,
				enabled: true,
				eval: func(EvalContext) (RuleResult, error) {
					return RuleResult{}, errFakeRule
				},
			}},
			targetError: ErrRuleEvaluation,
		},
		{
			name: "invalid rule decision",
			ast:  testAST(),
			rules: []Rule{fakeRule{
				id:      "invalid-result",
				dialect: DialectAll,
				level:   model.RiskDeny,
				enabled: true,
				eval: func(EvalContext) (RuleResult, error) {
					return RuleResult{Decision: model.Decision("maybe")}, nil
				},
			}},
			targetError: ErrRuleEvaluation,
		},
		{
			name: "missing rule suggestion",
			ast:  testAST(),
			rules: []Rule{fakeRule{
				id:      "missing-suggestion",
				dialect: DialectAll,
				level:   model.RiskWarn,
				enabled: true,
				eval: func(EvalContext) (RuleResult, error) {
					return RuleResult{Decision: model.DecisionWarn, Message: "warning"}, nil
				},
			}},
			targetError: ErrRuleEvaluation,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assessment, err := (Engine{}).Evaluate(test.ast, EvalContext{}, test.rules, test.layers)

			require.Error(t, err)
			require.True(t, errors.Is(err, test.targetError))
			require.Equal(t, model.DecisionDeny, assessment.Decision)
			require.Equal(t, model.RiskDeny, assessment.Risk)
			require.NotEmpty(t, assessment.Reason)
			require.NotEmpty(t, assessment.Suggestion)
		})
	}
}

func TestDialectMismatchDoesNotRunRule(t *testing.T) {
	rule := fakeRule{
		id:      "mysql-only",
		dialect: model.DBDialect("mysql"),
		level:   model.RiskDeny,
		enabled: true,
		eval: func(EvalContext) (RuleResult, error) {
			return RuleResult{}, errFakeRule
		},
	}

	assessment, err := (Engine{}).Evaluate(testAST(), EvalContext{}, []Rule{rule}, RuleLayers{})

	require.NoError(t, err)
	require.Equal(t, model.DecisionAllow, assessment.Decision)
	require.Empty(t, assessment.Hits)
}

func TestEvaluationDoesNotMutateInputs(t *testing.T) {
	ast := testAST()
	owner := "DBA"
	agent := &model.Agent{Level: "readonly", Owner: &owner}
	datasource := &model.Datasource{Name: "primary"}
	policy := &model.PolicyDecision{
		AllowedTables: []string{"public.orders"},
		DeniedTables:  []string{"public.secrets"},
		ColumnACL:     map[string][]string{"public.orders": {"id"}},
		Level:         "readonly",
	}
	thresholds := map[string]float64{"scan_rows": 100}
	runtimeResult := &model.QueryResult{
		Columns: []string{"id"},
		Rows:    [][]string{{"1"}},
	}
	rule := fakeRule{
		id:      "mutating-fake",
		dialect: DialectAll,
		level:   model.RiskWarn,
		enabled: true,
		eval: func(context EvalContext) (RuleResult, error) {
			context.AST.Columns[0] = "changed"
			context.Agent.Level = "ddl"
			*context.Agent.Owner = "changed"
			context.Datasource.Name = "changed"
			context.Policy.AllowedTables[0] = "changed"
			context.Policy.DeniedTables[0] = "changed"
			context.Policy.ColumnACL["public.orders"][0] = "changed"
			context.Policy.ColumnACL["changed"] = []string{"changed"}
			context.Thresholds["scan_rows"] = 999
			context.RuntimeResult.Columns[0] = "changed"
			context.RuntimeResult.Rows[0][0] = "changed"
			return nonAllowResult("mutating-fake", model.DecisionWarn), nil
		},
	}
	layers := RuleLayers{
		Global: RuleLayer{
			"mutating-fake": {Thresholds: thresholds},
		},
	}

	_, err := (Engine{}).Evaluate(ast, EvalContext{
		Agent:         agent,
		Datasource:    datasource,
		Policy:        policy,
		RuntimeResult: runtimeResult,
	}, []Rule{rule}, layers)

	require.NoError(t, err)
	require.Equal(t, []string{"id"}, ast.Columns)
	require.Equal(t, "readonly", agent.Level)
	require.Equal(t, "DBA", *agent.Owner)
	require.Equal(t, "primary", datasource.Name)
	require.Equal(t, []string{"public.orders"}, policy.AllowedTables)
	require.Equal(t, []string{"public.secrets"}, policy.DeniedTables)
	require.Equal(t, map[string][]string{"public.orders": {"id"}}, policy.ColumnACL)
	require.Equal(t, float64(100), thresholds["scan_rows"])
	require.Equal(t, []string{"id"}, runtimeResult.Columns)
	require.Equal(t, [][]string{{"1"}}, runtimeResult.Rows)
}

func TestEngineIsSafeForConcurrentEvaluation(t *testing.T) {
	const evaluations = 64
	engine := Engine{}
	rules := []Rule{resultRule("concurrent", model.RiskWarn, model.DecisionWarn)}
	layers := RuleLayers{
		Global: RuleLayer{
			"concurrent": {Thresholds: map[string]float64{"scan_rows": 100}},
		},
	}

	var waitGroup sync.WaitGroup
	errorsChannel := make(chan error, evaluations)
	for index := 0; index < evaluations; index++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			assessment, err := engine.Evaluate(testAST(), EvalContext{}, rules, layers)
			if err != nil {
				errorsChannel <- err
				return
			}
			if assessment.Decision != model.DecisionWarn || len(assessment.Hits) != 1 {
				errorsChannel <- errors.New("unexpected concurrent assessment")
			}
		}()
	}
	waitGroup.Wait()
	close(errorsChannel)

	for err := range errorsChannel {
		require.NoError(t, err)
	}
}

func resultRule(id string, level model.RiskLevel, decision model.Decision) Rule {
	return fakeRule{
		id:      id,
		dialect: DialectAll,
		level:   level,
		enabled: true,
		eval: func(EvalContext) (RuleResult, error) {
			if decision == model.DecisionAllow {
				return RuleResult{Decision: decision}, nil
			}
			return nonAllowResult(id, decision), nil
		},
	}
}

func nonAllowResult(id string, decision model.Decision) RuleResult {
	return RuleResult{
		Decision:   decision,
		Message:    id + " message",
		Suggestion: id + " suggestion",
	}
}

func hitIDs(hits []model.RuleHit) []string {
	ids := make([]string, 0, len(hits))
	for _, hit := range hits {
		ids = append(ids, hit.RuleID)
	}
	return ids
}

func boolPointer(value bool) *bool {
	return &value
}

func testAST() *model.AST {
	return &model.AST{
		Dialect:    model.DBDialect("postgres"),
		Normalized: "SELECT id FROM users",
		StmtType:   model.StmtType("SELECT"),
		Tables:     []model.ObjectRef{{Table: "users"}},
		Columns:    []string{"id"},
		Explain:    &model.ExplainInfo{EstScanRows: 25},
	}
}

func TestCloneASTDeepCopiesDirectProjections(t *testing.T) {
	original := &model.AST{
		DirectProjections: []model.DirectProjectionRef{
			{Column: "phone", Offset: 0, Source: model.ObjectRef{Schema: "crm", Table: "customers"}},
		},
	}

	cloned := cloneAST(original)
	cloned.DirectProjections[0].Column = "changed"
	cloned.DirectProjections[0].Source.Table = "changed"

	require.Equal(t, "phone", original.DirectProjections[0].Column)
	require.Equal(t, model.ObjectRef{Schema: "crm", Table: "customers"}, original.DirectProjections[0].Source)
}

func TestCloneASTDeepCopiesProjectionLineages(t *testing.T) {
	original := &model.AST{
		ProjectionLineages: []model.ProjectionLineage{{
			SelectIndex: 0,
			OutputName:  "mobile",
			Arms: []model.LineageArm{{
				Kind:   model.LineageDirect,
				Status: model.LineageResolved,
				Dependencies: []model.ColumnDependency{{
					Origin: model.ColumnOrigin{
						Relation: model.ObjectRef{Schema: "crm", Table: "customers"},
						Column:   "phone",
					},
					Role: model.DependencyValue,
				}},
				PossibleRelations: []model.ObjectRef{{Schema: "crm", Table: "customers"}},
			}},
		}},
	}

	cloned := cloneAST(original)
	cloned.ProjectionLineages[0].OutputName = "changed"
	cloned.ProjectionLineages[0].Arms[0].Operation = "changed"
	cloned.ProjectionLineages[0].Arms[0].Dependencies[0].Origin.Column = "changed"
	cloned.ProjectionLineages[0].Arms[0].Dependencies[0].Origin.Relation.Table = "changed"
	cloned.ProjectionLineages[0].Arms[0].PossibleRelations[0].Table = "changed"

	require.Equal(t, "mobile", original.ProjectionLineages[0].OutputName)
	require.Empty(t, original.ProjectionLineages[0].Arms[0].Operation)
	require.Equal(t, "phone", original.ProjectionLineages[0].Arms[0].Dependencies[0].Origin.Column)
	require.Equal(t, "customers", original.ProjectionLineages[0].Arms[0].Dependencies[0].Origin.Relation.Table)
	require.Equal(t, "customers", original.ProjectionLineages[0].Arms[0].PossibleRelations[0].Table)
}

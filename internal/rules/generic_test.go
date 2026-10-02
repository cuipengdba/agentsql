package rules

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/engine"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/parser"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewGenericRules(t *testing.T) {
	rules := NewGenericRules(NewDefaultTokenBucketLimiter())
	require.Len(t, rules, 10)
	levels := []model.RiskLevel{
		model.RiskDeny,
		model.RiskDeny,
		model.RiskDeny,
		model.RiskApprove,
		model.RiskWarn,
		model.RiskDeny,
		model.RiskDeny,
		model.RiskDeny,
		model.RiskApprove,
		model.RiskDeny,
	}
	for index, rule := range rules {
		require.Equal(t, fmt.Sprintf("R%03d", index+1), rule.ID())
		require.Equal(t, engine.DialectAll, rule.Dialect())
		require.Equal(t, levels[index], rule.Level())
		require.True(t, rule.Enabled())
	}
}

func TestGenericRuleUsesEngineLayerThreshold(t *testing.T) {
	rule := genericRuleByID(t, "R004", nil)
	assessment, err := (engine.Engine{}).Evaluate(
		astWithExplain(11),
		engine.EvalContext{},
		[]engine.Rule{rule},
		engine.RuleLayers{Global: engine.RuleLayer{
			"R004": {Thresholds: map[string]float64{ThresholdMaxScanRows: 10}},
		}},
	)

	require.NoError(t, err)
	require.Equal(t, model.DecisionApprove, assessment.Decision)
	require.Len(t, assessment.Hits, 1)
	require.Equal(t, "R004", assessment.Hits[0].RuleID)
}

func TestR001MultiStatement(t *testing.T) {
	rule := genericRuleByID(t, "R001", nil)
	cases := []ruleCase{
		{name: "postgres stacked statements", ast: astWith("postgres", "SELECT", "SELECT 1; DROP TABLE t"), mutate: multi, want: model.DecisionDeny},
		{name: "mysql stacked statements", ast: astWith("mysql", "SELECT", "SELECT 1; SELECT 2"), mutate: multi, want: model.DecisionDeny},
		{name: "single select", ast: astWith("postgres", "SELECT", "SELECT 1"), want: model.DecisionAllow},
		{name: "single update", ast: astWith("mysql", "UPDATE", "UPDATE t SET a = 1 WHERE id = 2"), want: model.DecisionAllow},
		{name: "single ddl", ast: astWith("postgres", "DDL", "CREATE TABLE t(id int)"), want: model.DecisionAllow},
	}
	runRuleCases(t, rule, cases)
}

func TestR002UnsafeUpdateDelete(t *testing.T) {
	rule := genericRuleByID(t, "R002", nil)
	cases := []ruleCase{
		{name: "update without where", ast: astWith("postgres", "UPDATE", "UPDATE t SET a = 1"), want: model.DecisionDeny},
		{name: "delete tautological where", ast: astWith("mysql", "DELETE", "DELETE FROM t WHERE 1 = 1"), mutate: func(ast *model.AST) { ast.HasWhere, ast.WhereTautology = true, true }, want: model.DecisionDeny},
		{name: "select without where", ast: astWith("postgres", "SELECT", "SELECT * FROM t"), want: model.DecisionAllow},
		{name: "bounded update", ast: astWith("postgres", "UPDATE", "UPDATE t SET a = 1 WHERE id = 2"), mutate: hasWhere, want: model.DecisionAllow},
		{name: "bounded delete", ast: astWith("mysql", "DELETE", "DELETE FROM t WHERE id = 2"), mutate: hasWhere, want: model.DecisionAllow},
	}
	runRuleCases(t, rule, cases)
}

func TestR003ReadonlyAgent(t *testing.T) {
	rule := genericRuleByID(t, "R003", nil)
	cases := []ruleCase{
		{name: "readonly update", ast: astWith("postgres", "UPDATE", "UPDATE t SET a = 1 WHERE id = 2"), context: engine.EvalContext{AgentLevel: "readonly"}, want: model.DecisionDeny},
		{name: "readonly ddl", ast: astWith("mysql", "DDL", "ALTER TABLE t ADD COLUMN a int"), context: engine.EvalContext{AgentLevel: "readonly"}, want: model.DecisionDeny},
		{name: "readonly select", ast: astWith("postgres", "SELECT", "SELECT * FROM t"), context: engine.EvalContext{AgentLevel: "readonly"}, want: model.DecisionAllow},
		{name: "dml agent update", ast: astWith("mysql", "UPDATE", "UPDATE t SET a = 1 WHERE id = 2"), context: engine.EvalContext{AgentLevel: "dml"}, want: model.DecisionAllow},
		{name: "ddl agent ddl", ast: astWith("postgres", "DDL", "CREATE TABLE t(id int)"), context: engine.EvalContext{AgentLevel: "ddl"}, want: model.DecisionAllow},
	}
	runRuleCases(t, rule, cases)
}

func TestR004EstimatedScanRows(t *testing.T) {
	rule := genericRuleByID(t, "R004", nil)
	cases := []ruleCase{
		{name: "default threshold exceeded", ast: astWithExplain(100_001), want: model.DecisionApprove},
		{name: "custom threshold exceeded", ast: astWithExplain(501), context: thresholds(ThresholdMaxScanRows, 500), want: model.DecisionApprove},
		{name: "default threshold equal", ast: astWithExplain(100_000), want: model.DecisionAllow},
		{name: "custom threshold below", ast: astWithExplain(499), context: thresholds(ThresholdMaxScanRows, 500), want: model.DecisionAllow},
		{name: "explain absent", ast: astWith("postgres", "SELECT", "SELECT 1"), want: model.DecisionAllow},
	}
	runRuleCases(t, rule, cases)
}

func TestR005UnlimitedLargeResult(t *testing.T) {
	rule := genericRuleByID(t, "R005", nil)
	cases := []ruleCase{
		{name: "default large result", ast: astWithExplain(1_001), want: model.DecisionWarn},
		{name: "custom large result", ast: astWithExplain(11), context: thresholds(ThresholdRowLimit, 10), want: model.DecisionWarn},
		{name: "datasource row limit", ast: astWithExplain(3), context: engine.EvalContext{Datasource: &model.Datasource{RowLimit: 2}}, want: model.DecisionWarn},
		{name: "query has limit", ast: astWithExplain(50_000), mutate: func(ast *model.AST) { ast.HasLimit = true }, want: model.DecisionAllow},
		{name: "pure aggregate without group", ast: astWithExplain(50_000), mutate: func(ast *model.AST) { ast.IsPureAggregate = true }, want: model.DecisionAllow},
		{name: "grouped aggregate remains warning", ast: astWithExplain(50_000), mutate: func(ast *model.AST) { ast.IsPureAggregate, ast.HasGroupBy = false, true }, want: model.DecisionWarn},
		{name: "result at threshold", ast: astWithExplain(1_000), want: model.DecisionAllow},
		{name: "write statement", ast: astWith("postgres", "UPDATE", "UPDATE t SET a = 1 WHERE id = 2"), mutate: func(ast *model.AST) { ast.Explain = &model.ExplainInfo{EstScanRows: 50_000} }, want: model.DecisionAllow},
	}
	runRuleCases(t, rule, cases)
}

func TestR005RuntimeTruncationOverridesSQLLimit(t *testing.T) {
	rule := genericRuleByID(t, "R005", nil)
	ast := astWith("postgres", "SELECT", "SELECT * FROM t LIMIT 100")
	ast.HasLimit = true
	datasource := &model.Datasource{RowLimit: 20}

	truncated, err := rule.Eval(engine.EvalContext{
		AST: ast, Datasource: datasource, RuntimeResult: &model.QueryResult{Truncated: true},
	})
	require.NoError(t, err)
	require.Equal(t, model.DecisionWarn, truncated.Decision)

	bounded, err := rule.Eval(engine.EvalContext{
		AST: ast, Datasource: datasource, RuntimeResult: &model.QueryResult{Truncated: false},
	})
	require.NoError(t, err)
	require.Equal(t, model.DecisionAllow, bounded.Decision)
}

func TestR006ParserBypassSignals(t *testing.T) {
	rule := genericRuleByID(t, "R006", nil)
	cases := []ruleCase{
		{name: "comment injection", ast: astWith("postgres", "SELECT", "/**/ SELECT 1"), mutate: func(ast *model.AST) { ast.Operations = []string{"SQL_COMMENT"} }, want: model.DecisionDeny},
		{name: "stacked statement", ast: astWith("mysql", "SELECT", "SELECT 1; DROP TABLE t"), mutate: multi, want: model.DecisionDeny},
		{name: "unknown statement", ast: astWith("postgres", "UNKNOWN", "unknown"), want: model.DecisionDeny},
		{name: "normal select", ast: astWith("postgres", "SELECT", "SELECT 1"), want: model.DecisionAllow},
		{name: "normal update", ast: astWith("mysql", "UPDATE", "UPDATE t SET a = 1 WHERE id = 2"), want: model.DecisionAllow},
		{name: "known admin", ast: astWith("postgres", "ADMIN", "VACUUM t"), mutate: func(ast *model.AST) { ast.Operations = []string{"VACUUM"} }, want: model.DecisionAllow},
		{name: "comment on is not a text comment", ast: astWith("postgres", "DDL", "COMMENT ON TABLE t IS 'description'"), mutate: func(ast *model.AST) { ast.Operations = []string{"COMMENT ON"} }, want: model.DecisionAllow},
	}
	runRuleCases(t, rule, cases)
}

func TestR007DangerousFunctions(t *testing.T) {
	rule := genericRuleByID(t, "R007", nil)
	cases := []ruleCase{
		{name: "postgres pg sleep function", ast: astWith("postgres", "SELECT", "SELECT pg_sleep(5)"), mutate: func(ast *model.AST) { ast.Functions = []string{"PG_SLEEP"} }, want: model.DecisionDeny},
		{name: "postgres catalog pg sleep function", ast: astWith("postgres", "SELECT", "SELECT pg_catalog.pg_sleep(5)"), mutate: func(ast *model.AST) { ast.Functions = []string{"pg_catalog.pg_sleep"} }, want: model.DecisionDeny},
		{name: "mysql benchmark operation", ast: astWith("mysql", "SELECT", "SELECT BENCHMARK(10, 1 + 1)"), mutate: func(ast *model.AST) { ast.Operations = []string{"benchmark"} }, want: model.DecisionDeny},
		{name: "postgres normal function", ast: astWith("postgres", "SELECT", "SELECT count(*) FROM t"), mutate: func(ast *model.AST) { ast.Functions = []string{"count"} }, want: model.DecisionAllow},
		{name: "mysql normal function", ast: astWith("mysql", "SELECT", "SELECT now()"), mutate: func(ast *model.AST) { ast.Functions = []string{"now"} }, want: model.DecisionAllow},
		{name: "dialect scoped name", ast: astWith("postgres", "SELECT", "SELECT sleep(1)"), mutate: func(ast *model.AST) { ast.Functions = []string{"sleep"} }, want: model.DecisionAllow},
	}
	runRuleCases(t, rule, cases)
}

func TestR008RateLimit(t *testing.T) {
	t.Run("qps exhausted", func(t *testing.T) {
		clock := newFakeClock()
		limiter := mustLimiter(t, clock)
		rule := genericRuleByID(t, "R008", limiter)
		context := rateContext("ag_qps", 1, 5)
		assertRuleDecision(t, rule, context, model.DecisionAllow)
		require.NoError(t, limiter.Release("ag_qps"))
		assertRuleDecision(t, rule, context, model.DecisionDeny)
	})

	t.Run("concurrency exhausted", func(t *testing.T) {
		limiter := mustLimiter(t, newFakeClock())
		rule := genericRuleByID(t, "R008", limiter)
		context := rateContext("ag_concurrency", 10, 1)
		assertRuleDecision(t, rule, context, model.DecisionAllow)
		assertRuleDecision(t, rule, context, model.DecisionDeny)
		require.NoError(t, limiter.Release("ag_concurrency"))
	})

	t.Run("first request under limits", func(t *testing.T) {
		limiter := mustLimiter(t, newFakeClock())
		rule := genericRuleByID(t, "R008", limiter)
		assertRuleDecision(t, rule, rateContext("ag_first", 20, 5), model.DecisionAllow)
		require.NoError(t, limiter.Release("ag_first"))
	})

	t.Run("spec defaults allow first request", func(t *testing.T) {
		limiter := mustLimiter(t, newFakeClock())
		rule := genericRuleByID(t, "R008", limiter)
		context := engine.EvalContext{
			AST:   astWith("postgres", "SELECT", "SELECT 1"),
			Agent: &model.Agent{ID: "ag_defaults"},
		}
		assertRuleDecision(t, rule, context, model.DecisionAllow)
		require.NoError(t, limiter.Release("ag_defaults"))
	})

	t.Run("token refills with fake clock", func(t *testing.T) {
		clock := newFakeClock()
		limiter := mustLimiter(t, clock)
		rule := genericRuleByID(t, "R008", limiter)
		context := rateContext("ag_refill", 1, 5)
		assertRuleDecision(t, rule, context, model.DecisionAllow)
		require.NoError(t, limiter.Release("ag_refill"))
		clock.Advance(time.Second)
		assertRuleDecision(t, rule, context, model.DecisionAllow)
		require.NoError(t, limiter.Release("ag_refill"))
	})

	t.Run("agents have independent buckets", func(t *testing.T) {
		limiter := mustLimiter(t, newFakeClock())
		rule := genericRuleByID(t, "R008", limiter)
		assertRuleDecision(t, rule, rateContext("ag_a", 1, 5), model.DecisionAllow)
		assertRuleDecision(t, rule, rateContext("ag_b", 1, 5), model.DecisionAllow)
		require.NoError(t, limiter.Release("ag_a"))
		require.NoError(t, limiter.Release("ag_b"))
	})
}

func TestR009QueryComplexity(t *testing.T) {
	rule := genericRuleByID(t, "R009", nil)
	cases := []ruleCase{
		{name: "sql too long", ast: astWith("postgres", "SELECT", "SELECT 12345"), context: thresholds(ThresholdMaxSQLLength, 5), want: model.DecisionApprove},
		{name: "nesting too deep", ast: astWith("mysql", "SELECT", "SELECT 1"), mutate: operation(OperationNestingDepth + ":4"), context: thresholds(ThresholdMaxNesting, 3), want: model.DecisionApprove},
		{name: "too many unions", ast: astWith("postgres", "SELECT", "SELECT 1"), mutate: operation(OperationUnionCount + ":3"), context: thresholds(ThresholdMaxUnion, 2), want: model.DecisionApprove},
		{name: "complexity below limits", ast: astWith("postgres", "SELECT", "SELECT 1"), mutate: func(ast *model.AST) {
			ast.Operations = []string{OperationNestingDepth + ":2", OperationUnionCount + ":1"}
		}, context: engine.EvalContext{Thresholds: map[string]float64{ThresholdMaxSQLLength: 100, ThresholdMaxNesting: 3, ThresholdMaxUnion: 2}}, want: model.DecisionAllow},
		{name: "complexity equal limits", ast: astWith("mysql", "SELECT", "SELECT 1"), mutate: func(ast *model.AST) {
			ast.Operations = []string{OperationNestingDepth + ":3", OperationUnionCount + ":2"}
		}, context: engine.EvalContext{Thresholds: map[string]float64{ThresholdMaxSQLLength: 100, ThresholdMaxNesting: 3, ThresholdMaxUnion: 2}}, want: model.DecisionAllow},
		{name: "unrelated operations ignored", ast: astWith("postgres", "ADMIN", "VACUUM t"), mutate: operation("VACUUM"), want: model.DecisionAllow},
	}
	runRuleCases(t, rule, cases)
}

func TestR010UnauthorizedTables(t *testing.T) {
	rule := genericRuleByID(t, "R010", nil)
	cases := []ruleCase{
		{name: "explicit deny overrides allow", ast: astWithTable("public", "salary"), context: policy([]string{"public.salary"}, []string{"public.salary"}), want: model.DecisionDeny},
		{name: "schema deny overrides global allow", ast: astWithTable("public", "salary"), context: policy([]string{"*"}, []string{"public.*"}), want: model.DecisionDeny},
		{name: "table absent from allow list", ast: astWithTable("public", "orders"), context: policy([]string{"public.customers"}, nil), want: model.DecisionDeny},
		{name: "empty policy denies table", ast: astWithTable("public", "orders"), context: policy([]string{}, []string{}), want: model.DecisionDeny},
		{name: "exact table allowed", ast: astWithTable("public", "orders"), context: policy([]string{"public.orders"}, nil), want: model.DecisionAllow},
		{name: "schema wildcard allowed", ast: astWithTable("reporting", "daily"), context: policy([]string{"reporting.*"}, nil), want: model.DecisionAllow},
		{name: "global wildcard allowed", ast: astWithTable("private", "daily"), context: policy([]string{"*"}, nil), want: model.DecisionAllow},
		{name: "alias does not change authorization", ast: mutateAST(astWithTable("public", "orders"), func(ast *model.AST) { ast.Tables[0].Alias = "o" }), context: policy([]string{"public.orders"}, nil), want: model.DecisionAllow},
		{name: "statement without table", ast: astWith("postgres", "SELECT", "SELECT 1"), context: policy(nil, nil), want: model.DecisionAllow},
		{name: "column ACL rejects projected secret", ast: mutateAST(astWithTable("", "orders"), func(ast *model.AST) {
			ast.Columns = []string{"id", "secret"}
			ast.Operations = []string{OperationSelectColumn + ":id", OperationSelectColumn + ":secret"}
		}), context: engine.EvalContext{Policy: &model.PolicyDecision{AllowedTables: []string{"orders"}, ColumnACL: map[string][]string{"orders": {"id", "name"}}}}, want: model.DecisionDeny},
		{name: "column ACL allows projected columns", ast: mutateAST(astWithTable("", "orders"), func(ast *model.AST) {
			ast.Columns = []string{"id", "name", "predicate_only"}
			ast.Operations = []string{OperationSelectColumn + ":id", OperationSelectColumn + ":name"}
		}), context: engine.EvalContext{Policy: &model.PolicyDecision{AllowedTables: []string{"orders"}, ColumnACL: map[string][]string{"orders": {"id", "name"}}}}, want: model.DecisionAllow},
		{name: "select star is denied by exact column ACL", ast: mutateAST(astWithTable("public", "orders"), func(ast *model.AST) {
			ast.Columns = []string{"*", "id"}
			ast.Operations = []string{OperationSelectColumn + ":*"}
		}), context: engine.EvalContext{Policy: &model.PolicyDecision{AllowedTables: []string{"public.orders"}, ColumnACL: map[string][]string{"public.orders": {"id"}}}}, want: model.DecisionDeny},
		{name: "qualified star is denied by exact column ACL", ast: mutateAST(astWithTable("public", "orders"), func(ast *model.AST) {
			ast.Operations = []string{OperationSelectColumn + ":orders.*"}
		}), context: engine.EvalContext{Policy: &model.PolicyDecision{AllowedTables: []string{"public.orders"}, ColumnACL: map[string][]string{"public.orders": {"id"}}}}, want: model.DecisionDeny},
		{name: "missing projection signal does not fall back to predicate columns", ast: mutateAST(astWithTable("", "orders"), func(ast *model.AST) {
			ast.Columns = []string{"predicate_only"}
			ast.Operations = nil
		}), context: engine.EvalContext{Policy: &model.PolicyDecision{AllowedTables: []string{"orders"}, ColumnACL: map[string][]string{"orders": {"id"}}}}, want: model.DecisionAllow},
		{name: "schema wildcard is a broad column grant", ast: mutateAST(astWithTable("public", "orders"), func(ast *model.AST) {
			ast.Columns = []string{"secret"}
			ast.Operations = []string{OperationSelectColumn + ":secret"}
		}), context: engine.EvalContext{Policy: &model.PolicyDecision{AllowedTables: []string{"public.*"}, ColumnACL: map[string][]string{"public.orders": {"id"}}}}, want: model.DecisionAllow},
		{name: "table without column ACL is unchanged", ast: mutateAST(astWithTable("", "orders"), func(ast *model.AST) {
			ast.Columns = []string{"secret"}
			ast.Operations = []string{OperationSelectColumn + ":secret"}
		}), context: engine.EvalContext{Policy: &model.PolicyDecision{AllowedTables: []string{"orders"}, ColumnACL: map[string][]string{}}}, want: model.DecisionAllow},
		{name: "join keeps v0.1 table-only authorization", ast: mutateAST(astWithTable("public", "orders"), func(ast *model.AST) {
			ast.Tables = append(ast.Tables, model.ObjectRef{Schema: "public", Table: "customers"})
			ast.Operations = []string{OperationSelectColumn + ":secret"}
		}), context: engine.EvalContext{Policy: &model.PolicyDecision{
			AllowedTables: []string{"public.orders", "public.customers"},
			ColumnACL:     map[string][]string{"public.orders": {"id"}, "public.customers": {"id"}},
		}}, want: model.DecisionAllow},
		{name: "self join keeps v0.1 table-only authorization", ast: mutateAST(astWithTable("public", "orders"), func(ast *model.AST) {
			ast.Tables[0].Alias = "left_orders"
			ast.Tables = append(ast.Tables, model.ObjectRef{Schema: "public", Table: "orders", Alias: "right_orders"})
			ast.Operations = []string{OperationSelectColumn + ":secret"}
		}), context: engine.EvalContext{Policy: &model.PolicyDecision{
			AllowedTables: []string{"public.orders"},
			ColumnACL:     map[string][]string{"public.orders": {"id"}},
		}}, want: model.DecisionAllow},
	}
	runRuleCases(t, rule, cases)
}

func TestR010ParserProjectionSignalsKeepSafeCTEStar(t *testing.T) {
	rule := genericRuleByID(t, "R010", nil)
	for _, dialect := range []model.DBDialect{"postgres", "mysql"} {
		t.Run(string(dialect), func(t *testing.T) {
			approved, err := parser.NewParser(dialect)
			require.NoError(t, err)
			policy := &model.PolicyDecision{
				AllowedTables: []string{"orders", "public.orders"},
				ColumnACL:     map[string][]string{"orders": {"id", "name", "amount"}},
			}
			for _, test := range []struct {
				name string
				sql  string
				want model.Decision
			}{
				{name: "explicit CTE projection expanded by outer star", sql: "WITH o AS (SELECT id FROM orders WHERE id=1) SELECT * FROM o LIMIT 1", want: model.DecisionAllow},
				{name: "CTE inner star remains blocked", sql: "WITH o AS (SELECT * FROM orders WHERE id=1) SELECT * FROM o LIMIT 1", want: model.DecisionDeny},
				{name: "aggregate star is not row expansion", sql: "SELECT count(*) FROM orders", want: model.DecisionAllow},
			} {
				t.Run(test.name, func(t *testing.T) {
					ast, err := approved.Parse(test.sql)
					require.NoError(t, err)
					result, err := rule.Eval(engine.EvalContext{AST: ast, Policy: policy})
					require.NoError(t, err)
					require.Equal(t, test.want, result.Decision, ast.Operations)
				})
			}
		})
	}
}

func TestGenericRulesFailClosed(t *testing.T) {
	tests := []struct {
		name    string
		ruleID  string
		limiter RateLimiter
		context engine.EvalContext
	}{
		{name: "missing ast", ruleID: "R001", context: engine.EvalContext{}},
		{name: "missing agent level", ruleID: "R003", context: engine.EvalContext{AST: astWith("postgres", "SELECT", "SELECT 1")}},
		{name: "negative explain rows", ruleID: "R004", context: engine.EvalContext{AST: astWithExplain(-1)}},
		{name: "invalid threshold", ruleID: "R005", context: mergeContext(astWithExplain(2), thresholds(ThresholdRowLimit, -1))},
		{name: "unsupported dialect", ruleID: "R007", context: engine.EvalContext{AST: astWith("oracle", "SELECT", "SELECT 1")}},
		{name: "nil limiter", ruleID: "R008", context: rateContext("ag_nil", 1, 1)},
		{name: "fractional concurrency", ruleID: "R008", limiter: mustLimiter(t, newFakeClock()), context: rateContext("ag_fraction", 1, 1.5)},
		{name: "malformed complexity", ruleID: "R009", context: engine.EvalContext{AST: mutateAST(astWith("postgres", "SELECT", "SELECT 1"), operation(OperationUnionCount+":many"))}},
		{name: "missing policy", ruleID: "R010", context: engine.EvalContext{AST: astWithTable("public", "orders")}},
		{name: "malformed policy", ruleID: "R010", context: mergeContext(astWithTable("public", "orders"), policy([]string{"db.public.orders"}, nil))},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rule := genericRuleByID(t, test.ruleID, test.limiter)
			_, err := rule.Eval(test.context)
			require.Error(t, err)
		})
	}
}

func TestR008LimiterErrorFailsClosed(t *testing.T) {
	rule := genericRuleByID(t, "R008", errorLimiter{})
	_, err := rule.Eval(rateContext("ag_error", 1, 1))
	require.Error(t, err)
	require.ErrorContains(t, err, "limiter unavailable")
}

func TestR008TypedNilLimiterFailsClosed(t *testing.T) {
	var limiter *TokenBucketLimiter
	rule := genericRuleByID(t, "R008", limiter)
	_, err := rule.Eval(rateContext("ag_typed_nil", 1, 1))
	require.Error(t, err)
}

func TestNewTokenBucketLimiterRejectsTypedNilClock(t *testing.T) {
	var clock *fakeClock
	limiter, err := NewTokenBucketLimiter(clock)
	require.Error(t, err)
	require.Nil(t, limiter)
}

func TestTokenBucketLimiterConcurrentSafety(t *testing.T) {
	const requestCount = 64
	limiter := mustLimiter(t, newFakeClock())
	var wait sync.WaitGroup
	results := make(chan error, requestCount)
	for index := 0; index < requestCount; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			result, err := limiter.Allow("ag_concurrent", requestCount, requestCount)
			if err != nil {
				results <- err
				return
			}
			if !result.Allowed {
				results <- fmt.Errorf("unexpected denial: %s", result.Reason)
				return
			}
			results <- limiter.Release("ag_concurrent")
		}()
	}
	wait.Wait()
	close(results)
	for err := range results {
		require.NoError(t, err)
	}
}

type ruleCase struct {
	name    string
	ast     *model.AST
	mutate  func(*model.AST)
	context engine.EvalContext
	want    model.Decision
}

func runRuleCases(t *testing.T, rule engine.Rule, cases []ruleCase) {
	t.Helper()
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			ast := mutateAST(test.ast, test.mutate)
			context := test.context
			context.AST = ast
			assertRuleDecision(t, rule, context, test.want)
		})
	}
}

func assertRuleDecision(
	t *testing.T,
	rule engine.Rule,
	context engine.EvalContext,
	want model.Decision,
) {
	t.Helper()
	result, err := rule.Eval(context)
	require.NoError(t, err)
	require.Equal(t, want, result.Decision)
	if want == model.DecisionAllow {
		assert.Empty(t, result.Message)
		assert.Empty(t, result.Suggestion)
		return
	}
	assert.NotEmpty(t, result.Message)
	assert.NotEmpty(t, result.Suggestion)
}

func genericRuleByID(t *testing.T, id string, limiter RateLimiter) engine.Rule {
	t.Helper()
	for _, rule := range NewGenericRules(limiter) {
		if rule.ID() == id {
			return rule
		}
	}
	t.Fatalf("rule %s not found", id)
	return nil
}

func astWith(dialect, statementType, rawSQL string) *model.AST {
	return &model.AST{
		Dialect:  model.DBDialect(dialect),
		RawSQL:   rawSQL,
		StmtType: model.StmtType(statementType),
	}
}

func astWithExplain(rows int64) *model.AST {
	ast := astWith("postgres", "SELECT", "SELECT * FROM orders")
	ast.Explain = &model.ExplainInfo{EstScanRows: rows}
	return ast
}

func astWithTable(schema, table string) *model.AST {
	ast := astWith("postgres", "SELECT", "SELECT * FROM "+schema+"."+table)
	ast.Tables = []model.ObjectRef{{Schema: schema, Table: table}}
	return ast
}

func mutateAST(source *model.AST, mutate func(*model.AST)) *model.AST {
	cloned := *source
	cloned.Tables = append([]model.ObjectRef{}, source.Tables...)
	cloned.Functions = append([]string{}, source.Functions...)
	cloned.Operations = append([]string{}, source.Operations...)
	if source.Explain != nil {
		explain := *source.Explain
		cloned.Explain = &explain
	}
	if mutate != nil {
		mutate(&cloned)
	}
	return &cloned
}

func multi(ast *model.AST) {
	ast.IsMulti = true
}

func hasWhere(ast *model.AST) {
	ast.HasWhere = true
}

func operation(value string) func(*model.AST) {
	return func(ast *model.AST) {
		ast.Operations = []string{value}
	}
}

func thresholds(name string, value float64) engine.EvalContext {
	return engine.EvalContext{Thresholds: map[string]float64{name: value}}
}

func policy(allowed, denied []string) engine.EvalContext {
	return engine.EvalContext{Policy: &model.PolicyDecision{
		AllowedTables: allowed,
		DeniedTables:  denied,
	}}
}

func mergeContext(ast *model.AST, context engine.EvalContext) engine.EvalContext {
	context.AST = ast
	return context
}

func rateContext(agentID string, qps, concurrency float64) engine.EvalContext {
	return engine.EvalContext{
		AST:   astWith("postgres", "SELECT", "SELECT 1"),
		Agent: &model.Agent{ID: agentID},
		Thresholds: map[string]float64{
			ThresholdQPS:           qps,
			ThresholdMaxConcurrent: concurrency,
		},
	}
}

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, time.September, 8, 0, 0, 0, 0, time.UTC)}
}

func (clock *fakeClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

func (clock *fakeClock) Advance(duration time.Duration) {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	clock.now = clock.now.Add(duration)
}

func mustLimiter(t *testing.T, clock Clock) *TokenBucketLimiter {
	t.Helper()
	limiter, err := NewTokenBucketLimiter(clock)
	require.NoError(t, err)
	return limiter
}

type errorLimiter struct{}

func (errorLimiter) Allow(string, float64, int) (RateLimitResult, error) {
	return RateLimitResult{}, errors.New("limiter unavailable")
}

func (errorLimiter) Release(string) error {
	return nil
}

package pipeline

import (
	"fmt"
	"strings"
	"time"

	"github.com/cuipengdba/agentsql/internal/engine"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/parser"
)

// StaticAssessInput contains the isolated inputs accepted by the playground.
type StaticAssessInput struct {
	SQL        string
	Dialect    model.DBDialect
	AgentLevel string
}

// StaticAssess parses one SQL string and runs only the production static rule
// gate. It has no store, executor, approval, or audit dependencies.
func StaticAssess(in StaticAssessInput) (model.Assessment, string, error) {
	sql := strings.TrimSpace(in.SQL)
	if sql == "" {
		return model.Assessment{}, "", fmt.Errorf("static assess: SQL is required")
	}
	if in.Dialect != model.DBDialect("postgres") && in.Dialect != model.DBDialect("mysql") &&
		in.Dialect != model.DBDialect("sqlserver") {
		return model.Assessment{}, "", fmt.Errorf("static assess: unsupported dialect %q", in.Dialect)
	}
	level := in.AgentLevel
	if level == "" {
		level = "readonly"
	}
	if level != "readonly" && level != "dml" && level != "ddl" {
		return model.Assessment{}, "", fmt.Errorf("static assess: invalid agent level %q", in.AgentLevel)
	}

	stageLatency := newStaticStageLatency()
	sqlParser, err := parser.NewParser(in.Dialect)
	if err != nil {
		return model.Assessment{}, "", fmt.Errorf("static assess: create parser: %w", err)
	}
	parseStarted := time.Now()
	ast, parseErr := sqlParser.Parse(sql)
	stageLatency[StageParse] = elapsedMilliseconds(parseStarted)
	if parseErr != nil && ast != nil && ast.IsMulti && ast.StmtType == "" {
		// The frozen parser deliberately returns ErrUnparseable for stacking but
		// preserves IsMulti. UNKNOWN makes that structured signal valid input for
		// the production R001/R006 rules without parsing either child statement.
		ast.StmtType = model.StmtType("UNKNOWN")
	}
	if parseErr != nil && (ast == nil || !ast.IsMulti) {
		message := parseErr.Error()
		assessment := model.Assessment{
			Decision: model.DecisionDeny,
			Risk:     model.RiskDeny,
			Hits: []model.RuleHit{{
				RuleID:     "PARSE",
				Risk:       model.RiskDeny,
				Decision:   model.DecisionDeny,
				Message:    message,
				Suggestion: "请检查 SQL 语法；解析失败时不进入规则安检、更不会执行",
			}},
			Reason:       message,
			Suggestion:   "请检查 SQL 语法；解析失败时不进入规则安检、更不会执行",
			StageLatency: stageLatency,
		}
		if ast != nil {
			assessment.StmtType = ast.StmtType
			assessment.Normalized = ast.Normalized
			assessment.Objects = append([]model.ObjectRef{}, ast.Tables...)
		}
		return assessment, message, nil
	}
	if ast == nil {
		return model.Assessment{}, "", fmt.Errorf("static assess: parser returned nil AST")
	}

	allRules, err := assembleRules(in.Dialect, &validationLimiter{}, panicMetadataProvider{})
	if err != nil {
		return model.Assessment{}, "", fmt.Errorf("static assess: assemble rules: %w", err)
	}
	staticRules, _ := splitRules(allRules)
	evalContext := engine.EvalContext{
		AST:        ast,
		AgentLevel: level,
		Agent: &model.Agent{
			ID:     "playground-demo",
			Name:   "演示 Agent",
			Status: "active",
			Level:  level,
		},
		Datasource: &model.Datasource{
			ID:     "playground-demo-ds",
			Name:   "演示数据源",
			DBType: string(in.Dialect),
		},
		Policy: &model.PolicyDecision{
			AllowedTables: []string{"*"},
			DeniedTables:  []string{},
			ColumnACL:     map[string][]string{},
			Level:         level,
		},
		MetadataProvider: panicMetadataProvider{},
		Thresholds:       nil,
	}
	guardStarted := time.Now()
	assessment, err := (engine.Engine{}).Evaluate(ast, evalContext, staticRules, engine.RuleLayers{})
	stageLatency[StageGuardStatic] = elapsedMilliseconds(guardStarted)
	assessment.StageLatency = stageLatency
	assessment.EstScanRows = 0
	if err != nil {
		return assessment, "", fmt.Errorf("static assess: evaluate static rules: %w", err)
	}
	return assessment, "", nil
}

func newStaticStageLatency() map[string]int64 {
	stages := pipelineStageNames()
	latency := make(map[string]int64, len(stages))
	for _, stage := range stages {
		latency[stage] = 0
	}
	return latency
}

func elapsedMilliseconds(started time.Time) int64 {
	elapsed := time.Since(started).Milliseconds()
	if elapsed < 0 {
		return 0
	}
	return elapsed
}

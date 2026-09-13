package pipeline

import (
	"fmt"
	"strings"
	"testing"

	"github.com/cuipengdba/agentsql/internal/engine"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/parser"
)

func FuzzAssessFailClosed(f *testing.F) {
	for _, corpus := range loadDecisionCorpus(f) {
		f.Add(corpus.SQL, "postgres")
		f.Add(corpus.SQL, "mysql")
	}
	malformedSeeds := []string{
		"\x00\xffSELECT FROM",
		"))(((",
		strings.Repeat("(", 4_096),
		strings.Repeat(";", 4_096),
		"SELECT 1" + strings.Repeat(" UNION ALL SELECT 1", 256),
		"SELECT (((((((((((((((((((((((((1",
		"SELECT /* unclosed comment",
		"/* outer /* nested */ SELECT 1",
		"SeLeCt FrOm WhErE",
		"SELECT '😀数据库安全'",
		"SELECT /*!50000 SLEEP(1) */",
		"SELECT 1\x00; DROP TABLE t",
	}
	for _, sql := range malformedSeeds {
		f.Add(sql, "postgres")
		f.Add(sql, "mysql")
	}

	f.Fuzz(func(t *testing.T, sql, dialectName string) {
		defer func() {
			if recovered := recover(); recovered != nil {
				t.Fatalf("assessment panicked: dialect=%q sql=%q panic=%v", dialectName, sql, recovered)
			}
		}()
		decision, parseFailed, err := fuzzDecisionAssessment(sql, model.DBDialect(dialectName))
		if err != nil {
			t.Fatalf("fuzz assessment invariant failed: dialect=%q sql=%q error=%v", dialectName, sql, err)
		}
		if parseFailed && decision != model.DecisionDeny {
			t.Fatalf(
				"parse failure was not fail-closed: dialect=%q sql=%q decision=%q",
				dialectName,
				sql,
				decision,
			)
		}
	})
}

func fuzzDecisionAssessment(
	sql string,
	dialect model.DBDialect,
) (decision model.Decision, parseFailed bool, err error) {
	approvedParser, parserErr := parser.NewParser(dialect)
	if parserErr != nil {
		return model.DecisionDeny, true, nil
	}
	ast, parseErr := approvedParser.Parse(sql)
	parseFailed = parseErr != nil
	if parseErr != nil && ast != nil && ast.IsMulti && ast.StmtType == "" {
		ast.StmtType = model.StmtType("UNKNOWN")
	}
	if parseErr != nil && (ast == nil || !ast.IsMulti) {
		return model.DecisionDeny, true, nil
	}
	if ast == nil {
		return model.DecisionDeny, parseFailed, fmt.Errorf("parser returned nil AST without terminal parse failure")
	}
	fake := decisionFakeMeta{}
	allRules, assembleErr := assembleRules(dialect, &validationLimiter{}, fake)
	if assembleErr != nil {
		return model.DecisionDeny, parseFailed, fmt.Errorf("assemble fuzz rules: %w", assembleErr)
	}
	assessment, evaluateErr := (engine.Engine{}).Evaluate(
		ast,
		engine.EvalContext{
			AST:        ast,
			AgentLevel: "ddl",
			Agent: &model.Agent{
				ID: "decision-fuzz", Status: "active", Level: "ddl",
			},
			Datasource: &model.Datasource{
				ID: "decision-fuzz-ds", DBType: string(dialect),
			},
			Policy: &model.PolicyDecision{
				AllowedTables: []string{"*"},
				DeniedTables:  []string{},
				ColumnACL:     map[string][]string{},
				Level:         "ddl",
			},
			MetadataProvider: fake,
			Thresholds:       nil,
		},
		allRules,
		engine.RuleLayers{},
	)
	if evaluateErr != nil && assessment.Decision != model.DecisionDeny {
		return assessment.Decision, parseFailed, fmt.Errorf(
			"engine error was not fail-closed: decision=%q: %w",
			assessment.Decision,
			evaluateErr,
		)
	}
	return assessment.Decision, parseFailed, nil
}

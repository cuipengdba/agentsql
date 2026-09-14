package pipeline

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/cuipengdba/agentsql/internal/engine"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/parser"
	"github.com/cuipengdba/agentsql/internal/rules"
	"github.com/stretchr/testify/require"
)

const (
	decisionCorpusFilename      = "decision_cases.json"
	decisionCorpusExpectedCases = 252
	decisionCorpusExpectedRuns  = 353
	decisionFalsePositiveLimit  = 0.02
)

var decisionReportRuleIDs = []string{
	"R001", "R002", "R003", "R004", "R005", "R006", "R007", "R008", "R009", "R010",
	"R101", "R102", "R103", "R104", "R105", "R106", "R107",
	"R201", "R202", "R203", "R204", "PARSE",
}

type decisionCorpusCase struct {
	ID          string           `json:"id"`
	Dialects    []string         `json:"dialects"`
	Category    string           `json:"category"`
	AgentLevel  string           `json:"agent_level"`
	SQL         string           `json:"sql"`
	Expect      model.Decision   `json:"expect"`
	ExpectRules []string         `json:"expect_rules"`
	Point       string           `json:"point"`
	Note        string           `json:"note"`
	Tag         string           `json:"tag"`
	Meta        decisionCaseMeta `json:"meta"`
}

type decisionCaseMeta struct {
	EstScanRows int64              `json:"est_scan_rows"`
	NoIndex     bool               `json:"no_index"`
	LargeTable  bool               `json:"large_table"`
	PGTx        decisionPGTx       `json:"pg_tx"`
	MySQLTx     decisionMySQLTx    `json:"mysql_tx"`
	Policy      decisionPolicyMeta `json:"policy"`
}

type decisionPGTx struct {
	InTransaction bool  `json:"in_transaction"`
	AgeMS         int64 `json:"age_ms"`
	IdleMS        int64 `json:"idle_ms"`
}

type decisionMySQLTx struct {
	InTransaction bool  `json:"in_transaction"`
	AgeMS         int64 `json:"age_ms"`
	AffectedRows  int64 `json:"affected_rows"`
}

type decisionPolicyMeta struct {
	Mode          string              `json:"mode"`
	AllowedTables []string            `json:"allowed_tables"`
	ColumnACL     map[string][]string `json:"column_acl"`
}

type decisionFakeMeta struct {
	meta decisionCaseMeta
}

func (fake decisionFakeMeta) TableHasIndex(string, string) (bool, error) {
	return !fake.meta.NoIndex, nil
}

func (fake decisionFakeMeta) TableRowCount(string, string) (int64, error) {
	if fake.meta.LargeTable {
		return 10_000_000, nil
	}
	return 100, nil
}

func (fake decisionFakeMeta) TransactionState() (rules.TransactionState, error) {
	return rules.TransactionState{
		InTransaction: fake.meta.PGTx.InTransaction,
		AgeMS:         fake.meta.PGTx.AgeMS,
		IdleMS:        fake.meta.PGTx.IdleMS,
	}, nil
}

func (fake decisionFakeMeta) MysqlTransactionState() (rules.MysqlTransactionState, error) {
	return rules.MysqlTransactionState{
		InTransaction: fake.meta.MySQLTx.InTransaction,
		AgeMS:         fake.meta.MySQLTx.AgeMS,
		AffectedRows:  fake.meta.MySQLTx.AffectedRows,
	}, nil
}

type decisionOutcome struct {
	Key      string
	SQL      string
	Got      model.Decision
	Expected model.Decision
	Hits     []string
}

type decisionCorpusStats struct {
	Total              int
	Passed             int
	NormalTotal        int
	CategoryDecision   map[string]map[string]int
	RuleHits           map[string]int
	DialectTotal       map[string]int
	DialectNormal      map[string]int
	DialectFalsePos    map[string]int
	Misses             []decisionOutcome
	FalsePositives     []decisionOutcome
	RiskMismatches     []decisionOutcome
	MissingRuleDetails []string
}

func newDecisionCorpusStats() *decisionCorpusStats {
	return &decisionCorpusStats{
		CategoryDecision: make(map[string]map[string]int),
		RuleHits:         make(map[string]int),
		DialectTotal:     make(map[string]int),
		DialectNormal:    make(map[string]int),
		DialectFalsePos:  make(map[string]int),
	}
}

func TestDecisionCorpus(t *testing.T) {
	cases := loadDecisionCorpus(t)
	require.Len(t, cases, decisionCorpusExpectedCases)
	validateDecisionCorpusShape(t, cases)
	stats := newDecisionCorpusStats()

	for _, dialectName := range []string{"postgres", "mysql"} {
		dialectName := dialectName
		t.Run(dialectName, func(t *testing.T) {
			for _, corpus := range cases {
				corpus := corpus
				if !containsDecisionDialect(corpus.Dialects, dialectName) {
					continue
				}
				t.Run(corpus.ID, func(t *testing.T) {
					got, hitIDs := assessDecisionCase(t, corpus, model.DBDialect(dialectName))
					recordDecisionOutcome(t, stats, corpus, dialectName, got, hitIDs)
				})
			}
		})
	}

	require.Equal(t, decisionCorpusExpectedRuns, stats.Total)
	logDecisionCorpusReport(t, stats)
	if stats.NormalTotal == 0 {
		t.Fatal("decision corpus has no normal judgments; false-positive denominator is zero")
	}
	falsePositiveRate := float64(len(stats.FalsePositives)) / float64(stats.NormalTotal)
	violations := make([]string, 0, 3)
	if len(stats.Misses) != 0 {
		violations = append(violations, "danger misses:\n"+formatDecisionOutcomes(stats.Misses))
	}
	if falsePositiveRate >= decisionFalsePositiveLimit {
		violations = append(violations, fmt.Sprintf(
			"false-positive rate %.2f%% is not below %.2f%%:\n%s",
			falsePositiveRate*100,
			decisionFalsePositiveLimit*100,
			formatDecisionOutcomes(stats.FalsePositives),
		))
	}
	if len(stats.RiskMismatches) != 0 {
		violations = append(violations, "risk decision mismatches:\n"+formatDecisionOutcomes(stats.RiskMismatches))
	}
	if len(violations) > 0 {
		t.Fatalf("decision corpus quality gate failed:\n%s", strings.Join(violations, "\n"))
	}
}

func assessDecisionCase(
	t *testing.T,
	corpus decisionCorpusCase,
	dialect model.DBDialect,
) (model.Decision, map[string]struct{}) {
	t.Helper()
	approvedParser, err := parser.NewParser(dialect)
	require.NoError(t, err)
	ast, parseErr := approvedParser.Parse(corpus.SQL)
	if parseErr != nil && ast != nil && ast.IsMulti && ast.StmtType == "" {
		ast.StmtType = model.StmtType("UNKNOWN")
	}
	if parseErr != nil && (ast == nil || !ast.IsMulti) {
		return model.DecisionDeny, map[string]struct{}{"PARSE": {}}
	}
	require.NotNil(t, ast)
	if corpus.Meta.EstScanRows > 0 {
		ast.Explain = &model.ExplainInfo{EstScanRows: corpus.Meta.EstScanRows}
	}
	fake := decisionFakeMeta{meta: corpus.Meta}
	allRules, err := assembleRules(dialect, &validationLimiter{}, fake)
	require.NoError(t, err)
	policy := decisionPolicyForCase(t, corpus)
	assessment, err := (engine.Engine{}).Evaluate(
		ast,
		engine.EvalContext{
			AST:        ast,
			AgentLevel: corpus.AgentLevel,
			Agent: &model.Agent{
				ID: "decision-corpus", Status: "active", Level: corpus.AgentLevel,
			},
			Datasource: &model.Datasource{
				ID: "decision-corpus-ds", DBType: string(dialect),
			},
			Policy:           policy,
			MetadataProvider: fake,
			Thresholds:       nil,
		},
		allRules,
		engine.RuleLayers{},
	)
	require.NoError(t, err)
	hitIDs := make(map[string]struct{}, len(assessment.Hits))
	for _, hit := range assessment.Hits {
		hitIDs[hit.RuleID] = struct{}{}
	}
	return assessment.Decision, hitIDs
}

func decisionPolicyForCase(t *testing.T, corpus decisionCorpusCase) *model.PolicyDecision {
	t.Helper()
	decision := &model.PolicyDecision{
		AllowedTables: []string{},
		DeniedTables:  []string{},
		ColumnACL:     make(map[string][]string),
		Level:         corpus.AgentLevel,
	}
	switch corpus.Meta.Policy.Mode {
	case "allow_all":
		decision.AllowedTables = []string{"*"}
	case "acl":
		decision.AllowedTables = append([]string{}, corpus.Meta.Policy.AllowedTables...)
		for table, columns := range corpus.Meta.Policy.ColumnACL {
			decision.ColumnACL[table] = append([]string{}, columns...)
		}
	default:
		t.Fatalf("case %s has unsupported policy mode %q", corpus.ID, corpus.Meta.Policy.Mode)
	}
	return decision
}

func recordDecisionOutcome(
	t *testing.T,
	stats *decisionCorpusStats,
	corpus decisionCorpusCase,
	dialect string,
	got model.Decision,
	hitIDs map[string]struct{},
) {
	t.Helper()
	stats.Total++
	stats.DialectTotal[dialect]++
	if stats.CategoryDecision[corpus.Category] == nil {
		stats.CategoryDecision[corpus.Category] = make(map[string]int)
	}
	stats.CategoryDecision[corpus.Category][string(got)]++
	hits := sortedDecisionHitIDs(hitIDs)
	for _, id := range hits {
		stats.RuleHits[id]++
	}
	key := dialect + "/" + corpus.ID
	outcome := decisionOutcome{Key: key, SQL: corpus.SQL, Got: got, Expected: corpus.Expect, Hits: hits}
	missing := false
	for _, expectedRule := range corpus.ExpectRules {
		if _, exists := hitIDs[expectedRule]; exists {
			continue
		}
		missing = true
		detail := key + ":" + expectedRule
		stats.MissingRuleDetails = append(stats.MissingRuleDetails, detail)
		t.Errorf("missing expected rule %q: sql=%q got=%q hits=%v", expectedRule, corpus.SQL, got, hits)
	}
	if got == corpus.Expect && !missing {
		stats.Passed++
	}
	switch corpus.Category {
	case "danger":
		if got != model.DecisionDeny {
			stats.Misses = append(stats.Misses, outcome)
		}
	case "risk":
		if got != corpus.Expect {
			stats.RiskMismatches = append(stats.RiskMismatches, outcome)
		}
	case "normal":
		stats.NormalTotal++
		stats.DialectNormal[dialect]++
		if got != model.DecisionAllow {
			stats.FalsePositives = append(stats.FalsePositives, outcome)
			stats.DialectFalsePos[dialect]++
		}
	default:
		t.Fatalf("case %s has unsupported category %q", corpus.ID, corpus.Category)
	}
}

func logDecisionCorpusReport(t *testing.T, stats *decisionCorpusStats) {
	t.Helper()
	t.Logf("decision corpus total=%d passed=%d", stats.Total, stats.Passed)
	for _, category := range []string{"danger", "risk", "normal"} {
		counts := stats.CategoryDecision[category]
		t.Logf(
			"category=%s deny=%d approve=%d warn=%d allow=%d",
			category,
			counts[string(model.DecisionDeny)],
			counts[string(model.DecisionApprove)],
			counts[string(model.DecisionWarn)],
			counts[string(model.DecisionAllow)],
		)
	}
	t.Logf("miss ids=%s", sortedDecisionDetails(stats.Misses))
	t.Logf("false-positive ids=%s", sortedDecisionDetails(stats.FalsePositives))
	t.Logf("risk-mismatch ids=%s", sortedDecisionDetails(stats.RiskMismatches))
	sort.Strings(stats.MissingRuleDetails)
	t.Logf("missing-rule ids=%s", emptyDash(strings.Join(stats.MissingRuleDetails, ",")))
	for _, id := range decisionReportRuleIDs {
		t.Logf("rule-hit rule=%s count=%d", id, stats.RuleHits[id])
	}
	for _, dialect := range []string{"postgres", "mysql"} {
		normal := stats.DialectNormal[dialect]
		rate := float64(0)
		if normal > 0 {
			rate = float64(stats.DialectFalsePos[dialect]) / float64(normal)
		}
		t.Logf(
			"dialect=%s total=%d normal=%d false_positive=%d false_positive_rate=%.2f%%",
			dialect,
			stats.DialectTotal[dialect],
			normal,
			stats.DialectFalsePos[dialect],
			rate*100,
		)
	}
}

func loadDecisionCorpus(t testing.TB) []decisionCorpusCase {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	path := filepath.Join(filepath.Dir(currentFile), "..", "..", "tests", "corpus", decisionCorpusFilename)
	contents, err := os.ReadFile(path)
	require.NoError(t, err)
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	var cases []decisionCorpusCase
	require.NoError(t, decoder.Decode(&cases))
	var trailing any
	err = decoder.Decode(&trailing)
	require.True(t, errors.Is(err, io.EOF), fmt.Sprintf("unexpected trailing JSON in %s", decisionCorpusFilename))
	return cases
}

func validateDecisionCorpusShape(t *testing.T, cases []decisionCorpusCase) {
	t.Helper()
	seen := make(map[string]struct{}, len(cases))
	categoryCases := make(map[string]int)
	dialectRuns := make(map[string]int)
	runs := 0
	for _, corpus := range cases {
		require.NotEmpty(t, corpus.ID)
		require.NotContains(t, seen, corpus.ID, "duplicate case ID")
		seen[corpus.ID] = struct{}{}
		require.NotEmpty(t, corpus.Dialects, corpus.ID)
		runs += len(corpus.Dialects)
		for _, dialect := range corpus.Dialects {
			require.Contains(t, []string{"postgres", "mysql"}, dialect, corpus.ID)
			dialectRuns[dialect]++
		}
		require.Contains(t, []string{"danger", "risk", "normal"}, corpus.Category, corpus.ID)
		categoryCases[corpus.Category]++
		require.Contains(t, []string{"readonly", "dml", "ddl"}, corpus.AgentLevel, corpus.ID)
		require.Contains(t, []model.Decision{
			model.DecisionDeny, model.DecisionApprove, model.DecisionWarn, model.DecisionAllow,
		}, corpus.Expect, corpus.ID)
	}
	require.Equal(t, 76, categoryCases["danger"])
	require.Equal(t, 61, categoryCases["risk"])
	require.Equal(t, 115, categoryCases["normal"])
	require.Equal(t, 192, dialectRuns["postgres"])
	require.Equal(t, 161, dialectRuns["mysql"])
	require.Equal(t, decisionCorpusExpectedRuns, runs)
}

func containsDecisionDialect(dialects []string, candidate string) bool {
	for _, dialect := range dialects {
		if dialect == candidate {
			return true
		}
	}
	return false
}

func sortedDecisionHitIDs(hitIDs map[string]struct{}) []string {
	ids := make([]string, 0, len(hitIDs))
	for id := range hitIDs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func sortedDecisionDetails(outcomes []decisionOutcome) string {
	keys := make([]string, 0, len(outcomes))
	for _, outcome := range outcomes {
		keys = append(keys, outcome.Key)
	}
	sort.Strings(keys)
	return emptyDash(strings.Join(keys, ","))
}

func formatDecisionOutcomes(outcomes []decisionOutcome) string {
	ordered := append([]decisionOutcome{}, outcomes...)
	sort.Slice(ordered, func(left, right int) bool { return ordered[left].Key < ordered[right].Key })
	lines := make([]string, 0, len(ordered))
	for _, outcome := range ordered {
		lines = append(lines, fmt.Sprintf(
			"%s sql=%q got=%q expect=%q hits=%v",
			outcome.Key,
			outcome.SQL,
			outcome.Got,
			outcome.Expected,
			outcome.Hits,
		))
	}
	return emptyDash(strings.Join(lines, "\n"))
}

func emptyDash(value string) string {
	if value == "" {
		return "-"
	}
	return value
}

var (
	_ rules.MetadataProvider                 = decisionFakeMeta{}
	_ rules.TransactionMetadataProvider      = decisionFakeMeta{}
	_ rules.MysqlTransactionMetadataProvider = decisionFakeMeta{}
)

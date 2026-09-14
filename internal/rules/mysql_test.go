package rules

import (
	"errors"
	"fmt"
	"testing"

	"github.com/cuipengdba/agentsql/internal/engine"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/parser"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewMysqlRules(t *testing.T) {
	rules := NewMysqlRules(nil)
	require.Len(t, rules, 4)
	levels := []model.RiskLevel{
		model.RiskDeny,
		model.RiskApprove,
		model.RiskDeny,
		model.RiskApprove,
	}
	for index, rule := range rules {
		require.Equal(t, fmt.Sprintf("R%d", 201+index), rule.ID())
		require.Equal(t, model.DBDialect("mysql"), rule.Dialect())
		require.Equal(t, levels[index], rule.Level())
		require.True(t, rule.Enabled())
	}
}

func TestMysqlRulesAreFilteredForPostgres(t *testing.T) {
	assessment, err := (engine.Engine{}).Evaluate(
		astWith("postgres", "ADMIN", "FLUSH"),
		engine.EvalContext{},
		NewMysqlRules(nil),
		engine.RuleLayers{},
	)

	require.NoError(t, err)
	require.Equal(t, model.DecisionAllow, assessment.Decision)
	require.Empty(t, assessment.Hits)
}

func TestR201MysqlFileAccess(t *testing.T) {
	tests := []mysqlSQLRuleCase{
		{name: "load file function", sql: "SELECT LOAD_FILE('/etc/passwd')", want: model.DecisionDeny},
		{name: "into outfile", sql: "SELECT id FROM users INTO OUTFILE '/tmp/users.txt'", want: model.DecisionDeny},
		{name: "into dumpfile", sql: "SELECT id FROM users INTO DUMPFILE '/tmp/user.bin'", want: model.DecisionDeny},
		{name: "load data infile", sql: "LOAD DATA INFILE '/tmp/orders.csv' INTO TABLE orders", want: model.DecisionDeny},
		{name: "load data local infile", sql: "LOAD DATA LOCAL INFILE '/tmp/orders.csv' INTO TABLE orders", want: model.DecisionDeny},
		{name: "normal select", sql: "SELECT id FROM users LIMIT 10", want: model.DecisionAllow},
		{name: "normal insert", sql: "INSERT INTO users(id, name) VALUES (1, 'alice')", want: model.DecisionAllow},
		{name: "ordinary function", sql: "SELECT LOWER(name) FROM users", want: model.DecisionAllow},
	}
	runMysqlSQLRuleCases(t, mysqlRuleByID(t, "R201", nil), tests)
}

func TestR202MysqlDangerousWrites(t *testing.T) {
	tests := []mysqlSQLRuleCase{
		{name: "joined update", sql: "UPDATE users AS u JOIN orders AS o ON o.user_id = u.id SET u.flagged = 1 WHERE o.status = 'open'", want: model.DecisionApprove},
		{name: "multi table delete", sql: "DELETE u FROM users AS u JOIN orders AS o ON o.user_id = u.id WHERE o.status = 'closed'", want: model.DecisionApprove},
		{name: "single table update without limit", sql: "UPDATE users SET flagged = 1 WHERE id = 7", want: model.DecisionApprove},
		{name: "single table delete without limit", sql: "DELETE FROM users WHERE id = 7", want: model.DecisionApprove},
		{name: "single table update with limit", sql: "UPDATE users SET flagged = 1 WHERE id = 7 LIMIT 1", want: model.DecisionAllow},
		{name: "single table delete with limit", sql: "DELETE FROM users WHERE id = 7 LIMIT 1", want: model.DecisionAllow},
		{name: "insert is not a batch update", sql: "INSERT INTO users(id) VALUES (7)", want: model.DecisionAllow},
		{name: "select join is not a write", sql: "SELECT u.id FROM users AS u JOIN orders AS o ON o.user_id = u.id", want: model.DecisionAllow},
	}
	runMysqlSQLRuleCases(t, mysqlRuleByID(t, "R202", nil), tests)
}

func TestR202MultiTableTakesPriorityOverLimit(t *testing.T) {
	ast := astWith("mysql", "UPDATE", "structured AST fixture")
	ast.Tables = []model.ObjectRef{{Table: "users"}, {Table: "orders"}}
	ast.HasLimit = true
	ast.Operations = []string{"UPDATE"}

	rule := mysqlRuleByID(t, "R202", nil)
	assertMysqlRuleDecision(t, rule, engine.EvalContext{AST: ast}, model.DecisionApprove)
}

func TestR203MysqlHighRiskAdministration(t *testing.T) {
	tests := []mysqlSQLRuleCase{
		{name: "flush tables", sql: "FLUSH TABLES", want: model.DecisionDeny},
		{name: "kill connection", sql: "KILL 42", want: model.DecisionDeny},
		{name: "purge binary logs", sql: "PURGE BINARY LOGS TO 'bin.000001'", want: model.DecisionDeny},
		{name: "use database", sql: "USE app", want: model.DecisionAllow},
		{name: "set session", sql: "SET SESSION sql_mode = ''", want: model.DecisionAllow},
		{name: "set global", sql: "SET GLOBAL max_connections = 100", want: model.DecisionDeny},
		{name: "set global qualified variable", sql: "SET @@global.max_connections = 100", want: model.DecisionDeny},
		{name: "lock tables", sql: "LOCK TABLES users READ", want: model.DecisionAllow},
		{name: "unlock tables", sql: "UNLOCK TABLES", want: model.DecisionAllow},
		{name: "normal select", sql: "SELECT id FROM users LIMIT 1", want: model.DecisionAllow},
	}
	runMysqlSQLRuleCases(t, mysqlRuleByID(t, "R203", nil), tests)
}

func TestT231MySQLCommentSignalsReachR006(t *testing.T) {
	approvedParser, err := parser.NewParser(model.DBDialect("mysql"))
	require.NoError(t, err)
	tests := []struct {
		name string
		sql  string
		want model.Decision
	}{
		{name: "version conditional comment", sql: "SELECT /*!50000 id*/ FROM users", want: model.DecisionDeny},
		{name: "ordinary block comment", sql: "SELECT /* audit */ id FROM users", want: model.DecisionDeny},
		{name: "optimizer hint", sql: "SELECT /*+ INDEX(users idx_users_id) */ id FROM users", want: model.DecisionAllow},
	}
	rule := genericRuleByID(t, "R006", nil)
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ast, err := approvedParser.Parse(test.sql)
			require.NoError(t, err)
			assertMysqlRuleDecision(t, rule, engine.EvalContext{AST: ast}, test.want)
		})
	}
}

func TestMysqlRulesSkipUnstructuredMultiAndUnknown(t *testing.T) {
	for _, ruleID := range []string{"R201", "R202", "R203", "R204"} {
		for _, ast := range []*model.AST{
			{Dialect: "mysql", StmtType: "UNKNOWN", IsMulti: true},
			{Dialect: "mysql", StmtType: "UNKNOWN", Operations: []string{"UNKNOWN"}},
			{Dialect: "mysql", StmtType: "ADMIN"},
		} {
			rule := mysqlRuleByID(t, ruleID, nil)
			assertMysqlRuleDecision(t, rule, engine.EvalContext{AST: ast}, model.DecisionAllow)
		}
	}
	rule := mysqlRuleByID(t, "R203", nil)
	_, err := rule.Eval(engine.EvalContext{AST: &model.AST{Dialect: "mysql", StmtType: "ADMIN", Operations: []string{""}}})
	require.Error(t, err)
}

func TestR204MysqlLargeTransaction(t *testing.T) {
	tests := []struct {
		name     string
		fallback MysqlTransactionMetadataProvider
		context  engine.EvalContext
		want     model.Decision
	}{
		{
			name:     "transaction age exceeds default",
			fallback: &fakeMysqlTransactionMetadataProvider{state: MysqlTransactionState{InTransaction: true, AgeMS: defaultMysqlTransactionAgeMS + 1}},
			want:     model.DecisionApprove,
		},
		{
			name:     "affected rows exceed default",
			fallback: &fakeMysqlTransactionMetadataProvider{state: MysqlTransactionState{InTransaction: true, AffectedRows: defaultMysqlTransactionAffectedRows + 1}},
			want:     model.DecisionApprove,
		},
		{
			name:     "custom age threshold",
			fallback: &fakeMysqlTransactionMetadataProvider{state: MysqlTransactionState{InTransaction: true, AgeMS: 101}},
			context:  engine.EvalContext{Thresholds: map[string]float64{ThresholdMysqlTransactionAgeMS: 100}},
			want:     model.DecisionApprove,
		},
		{
			name:     "custom row threshold",
			fallback: &fakeMysqlTransactionMetadataProvider{state: MysqlTransactionState{InTransaction: true, AffectedRows: 101}},
			context:  engine.EvalContext{Thresholds: map[string]float64{ThresholdMysqlTransactionAffectedRows: 100}},
			want:     model.DecisionApprove,
		},
		{name: "metadata absent", want: model.DecisionAllow},
		{name: "metadata error", fallback: &fakeMysqlTransactionMetadataProvider{err: errors.New("session state unavailable")}, want: model.DecisionAllow},
		{name: "not in transaction", fallback: &fakeMysqlTransactionMetadataProvider{}, want: model.DecisionAllow},
		{
			name:     "below both thresholds",
			fallback: &fakeMysqlTransactionMetadataProvider{state: MysqlTransactionState{InTransaction: true, AgeMS: defaultMysqlTransactionAgeMS - 1, AffectedRows: defaultMysqlTransactionAffectedRows - 1}},
			want:     model.DecisionAllow,
		},
		{
			name:     "equal thresholds",
			fallback: &fakeMysqlTransactionMetadataProvider{state: MysqlTransactionState{InTransaction: true, AgeMS: defaultMysqlTransactionAgeMS, AffectedRows: defaultMysqlTransactionAffectedRows}},
			want:     model.DecisionAllow,
		},
		{
			name:     "invalid snapshot is unavailable",
			fallback: &fakeMysqlTransactionMetadataProvider{state: MysqlTransactionState{InTransaction: true, AgeMS: -1, AffectedRows: -1}},
			want:     model.DecisionAllow,
		},
		{
			name:     "wrong context provider is unavailable",
			fallback: &fakeMysqlTransactionMetadataProvider{state: MysqlTransactionState{InTransaction: true, AgeMS: defaultMysqlTransactionAgeMS + 1}},
			context:  engine.EvalContext{MetadataProvider: "wrong"},
			want:     model.DecisionAllow,
		},
	}
	rule := mysqlRuleByID(t, "R204", nil)
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.fallback != nil {
				rule = mysqlRuleByID(t, "R204", test.fallback)
			} else {
				rule = mysqlRuleByID(t, "R204", nil)
			}
			context := test.context
			context.AST = mysqlBoundaryAST("BEGIN")
			assertMysqlRuleDecision(t, rule, context, test.want)
		})
	}
}

func TestR204ContextMetadataOverridesConstructor(t *testing.T) {
	fallback := &fakeMysqlTransactionMetadataProvider{}
	override := &fakeMysqlTransactionMetadataProvider{state: MysqlTransactionState{
		InTransaction: true,
		AgeMS:         defaultMysqlTransactionAgeMS + 1,
	}}
	rule := mysqlRuleByID(t, "R204", fallback)
	context := engine.EvalContext{
		AST:              mysqlBoundaryAST("BEGIN"),
		MetadataProvider: override,
	}
	assertMysqlRuleDecision(t, rule, context, model.DecisionApprove)
}

func TestR204TypedNilMetadataIsUnavailable(t *testing.T) {
	var provider *fakeMysqlTransactionMetadataProvider
	rule := mysqlRuleByID(t, "R204", provider)
	assertMysqlRuleDecision(
		t,
		rule,
		engine.EvalContext{AST: mysqlBoundaryAST("BEGIN")},
		model.DecisionAllow,
	)
}

func TestR204TransactionBoundariesWithoutMetadataAllow(t *testing.T) {
	tests := []mysqlSQLRuleCase{
		{name: "begin", sql: "BEGIN", want: model.DecisionAllow},
		{name: "start transaction", sql: "START TRANSACTION", want: model.DecisionAllow},
		{name: "commit", sql: "COMMIT", want: model.DecisionAllow},
		{name: "rollback", sql: "ROLLBACK", want: model.DecisionAllow},
	}
	runMysqlSQLRuleCases(t, mysqlRuleByID(t, "R204", nil), tests)
}

func TestMysqlRulesFailClosedOnInvalidInputs(t *testing.T) {
	for _, ruleID := range []string{"R201", "R202", "R203", "R204"} {
		t.Run(ruleID+" missing AST", func(t *testing.T) {
			rule := mysqlRuleByID(t, ruleID, nil)
			_, err := rule.Eval(engine.EvalContext{})
			require.Error(t, err)
		})
		t.Run(ruleID+" direct postgres evaluation", func(t *testing.T) {
			rule := mysqlRuleByID(t, ruleID, nil)
			_, err := rule.Eval(engine.EvalContext{AST: astWith("postgres", "SELECT", "SELECT 1")})
			require.Error(t, err)
		})
	}

	provider := &fakeMysqlTransactionMetadataProvider{state: MysqlTransactionState{InTransaction: true}}
	rule := mysqlRuleByID(t, "R204", provider)
	_, err := rule.Eval(engine.EvalContext{
		AST:        mysqlBoundaryAST("BEGIN"),
		Thresholds: map[string]float64{ThresholdMysqlTransactionAgeMS: -1},
	})
	require.Error(t, err)
}

type mysqlSQLRuleCase struct {
	name string
	sql  string
	want model.Decision
}

func runMysqlSQLRuleCases(t *testing.T, rule engine.Rule, tests []mysqlSQLRuleCase) {
	t.Helper()
	approvedParser, err := parser.NewParser(model.DBDialect("mysql"))
	require.NoError(t, err)
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ast, err := approvedParser.Parse(test.sql)
			require.NoError(t, err)
			assertMysqlRuleDecision(t, rule, engine.EvalContext{AST: ast}, test.want)
		})
	}
}

func assertMysqlRuleDecision(
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

func mysqlRuleByID(
	t *testing.T,
	id string,
	meta MysqlTransactionMetadataProvider,
) engine.Rule {
	t.Helper()
	for _, rule := range NewMysqlRules(meta) {
		if rule.ID() == id {
			return rule
		}
	}
	t.Fatalf("MySQL rule %s not found", id)
	return nil
}

func mysqlBoundaryAST(operation string) *model.AST {
	ast := astWith("mysql", "ADMIN", operation)
	ast.Operations = []string{operation}
	return ast
}

type fakeMysqlTransactionMetadataProvider struct {
	state MysqlTransactionState
	err   error
}

func (provider *fakeMysqlTransactionMetadataProvider) MysqlTransactionState() (MysqlTransactionState, error) {
	if provider == nil {
		return MysqlTransactionState{}, errors.New("nil MySQL transaction metadata provider")
	}
	if provider.err != nil {
		return MysqlTransactionState{}, provider.err
	}
	return provider.state, nil
}

var _ MysqlTransactionMetadataProvider = (*fakeMysqlTransactionMetadataProvider)(nil)

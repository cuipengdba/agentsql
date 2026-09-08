package rules

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/cuipengdba/agentsql/internal/engine"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewPostgresRules(t *testing.T) {
	rules := NewPostgresRules(&fakeMetadataProvider{})
	require.Len(t, rules, 7)
	levels := []model.RiskLevel{
		model.RiskDeny,
		model.RiskApprove,
		model.RiskDeny,
		model.RiskDeny,
		model.RiskApprove,
		model.RiskApprove,
		model.RiskWarn,
	}
	for index, rule := range rules {
		require.Equal(t, fmt.Sprintf("R%d", 101+index), rule.ID())
		require.Equal(t, model.DBDialect("postgres"), rule.Dialect())
		require.Equal(t, levels[index], rule.Level())
		require.True(t, rule.Enabled())
	}
}

func TestPostgresRulesAreFilteredForMySQL(t *testing.T) {
	assessment, err := (engine.Engine{}).Evaluate(
		astWith("mysql", "DDL", "DROP TABLE t"),
		engine.EvalContext{},
		NewPostgresRules(nil),
		engine.RuleLayers{},
	)

	require.NoError(t, err)
	require.Equal(t, model.DecisionAllow, assessment.Decision)
	require.Empty(t, assessment.Hits)
}

func TestR101DangerousDrop(t *testing.T) {
	rule := postgresRuleByID(t, "R101", nil)
	cases := []postgresRuleCase{
		{name: "drop table", ast: postgresOperationAST("DDL", "DROP TABLE public.orders", "DROP TABLE", pgTable("public", "orders", "")), want: model.DecisionDeny},
		{name: "mixed case drop database", ast: postgresOperationAST("DDL", "dRoP\nDaTaBaSe app", "dRoP DaTaBaSe"), want: model.DecisionDeny},
		{name: "structured whitespace bypass", ast: postgresOperationAST("DDL", "DROP  \n TABLE public.orders", "  DROP \n TABLE  "), want: model.DecisionDeny},
		{name: "drop index allowed", ast: postgresOperationAST("DDL", "DROP INDEX public.idx_orders", "DROP INDEX"), want: model.DecisionAllow},
		{name: "drop view allowed", ast: postgresOperationAST("DDL", "DROP VIEW public.order_view", "DROP VIEW"), want: model.DecisionAllow},
		{name: "drop column allowed", ast: postgresOperationAST("DDL", "ALTER TABLE orders DROP COLUMN note", "ALTER TABLE", pgTable("public", "orders", "")), want: model.DecisionAllow},
	}
	runPostgresRuleCases(t, rule, cases)
}

func TestR102HeavyMaintenance(t *testing.T) {
	rule := postgresRuleByID(t, "R102", nil)
	cases := []postgresRuleCase{
		{name: "vacuum full", ast: postgresOperationAST("ADMIN", "VACUUM FULL public.orders", "VACUUM FULL", pgTable("public", "orders", "")), want: model.DecisionApprove},
		{name: "mixed case reindex", ast: postgresOperationAST("ADMIN", "rEiNdEx TABLE public.orders", "rEiNdEx", pgTable("public", "orders", "")), want: model.DecisionApprove},
		{name: "cluster whitespace bypass", ast: postgresOperationAST("ADMIN", "CLUSTER\npublic.orders", " CLUSTER\n", pgTable("public", "orders", "")), want: model.DecisionApprove},
		{name: "plain vacuum allowed", ast: postgresOperationAST("ADMIN", "VACUUM public.orders", "VACUUM", pgTable("public", "orders", "")), want: model.DecisionAllow},
		{name: "analyze allowed", ast: postgresOperationAST("ADMIN", "ANALYZE public.orders", "ANALYZE", pgTable("public", "orders", "")), want: model.DecisionAllow},
		{name: "select allowed", ast: postgresOperationAST("SELECT", "SELECT * FROM public.orders", "SELECT", pgTable("public", "orders", "o")), want: model.DecisionAllow},
	}
	runPostgresRuleCases(t, rule, cases)
}

func TestR103DangerousManagementFunctions(t *testing.T) {
	rule := postgresRuleByID(t, "R103", nil)
	cases := []postgresRuleCase{
		{name: "terminate backend", ast: postgresFunctionAST("SELECT pg_terminate_backend(42)", "pg_terminate_backend"), want: model.DecisionDeny},
		{name: "mixed catalog cancel backend", ast: postgresFunctionAST("SELECT PG_CATALOG.Pg_Cancel_Backend(42)", "PG_CATALOG.Pg_Cancel_Backend"), want: model.DecisionDeny},
		{name: "catalog file function whitespace", ast: postgresFunctionAST("SELECT\npg_catalog.pg_read_file('/tmp/a')", " pg_catalog . pg_read_file "), want: model.DecisionDeny},
		{name: "reload configuration", ast: postgresFunctionAST("SELECT pg_reload_conf()", "pg_reload_conf"), want: model.DecisionDeny},
		{name: "list server directory", ast: postgresFunctionAST("SELECT pg_catalog.pg_ls_dir('.')", "pg_catalog.pg_ls_dir"), want: model.DecisionDeny},
		{name: "normal aggregate allowed", ast: postgresFunctionAST("SELECT count(*) FROM public.orders", "count"), want: model.DecisionAllow},
		{name: "similarly named app function allowed", ast: postgresFunctionAST("SELECT app.pg_read_file('logical-name')", "app.pg_read_file"), want: model.DecisionAllow},
		{name: "ordinary operation allowed", ast: postgresOperationAST("ADMIN", "ANALYZE public.orders", "ANALYZE", pgTable("public", "orders", "")), want: model.DecisionAllow},
	}
	runPostgresRuleCases(t, rule, cases)
}

func TestR104CopyProgram(t *testing.T) {
	rule := postgresRuleByID(t, "R104", nil)
	cases := []postgresRuleCase{
		{name: "copy table to program", ast: postgresOperationAST("ADMIN", "COPY public.orders TO PROGRAM 'gzip'", "COPY PROGRAM", pgTable("public", "orders", "")), want: model.DecisionDeny},
		{name: "mixed case copy program", ast: postgresOperationAST("ADMIN", "cOpY public.orders FrOm PrOgRaM 'cat'", "cOpY pRoGrAm", pgTable("public", "orders", "")), want: model.DecisionDeny},
		{name: "copy program whitespace bypass", ast: postgresOperationAST("ADMIN", "COPY\npublic.orders TO\nPROGRAM 'gzip'", " COPY\n PROGRAM ", pgTable("public", "orders", "")), want: model.DecisionDeny},
		{name: "copy file allowed", ast: postgresOperationAST("ADMIN", "COPY public.orders TO '/tmp/orders.csv'", "COPY", pgTable("public", "orders", "")), want: model.DecisionAllow},
		{name: "copy stdin allowed", ast: postgresOperationAST("ADMIN", "COPY public.orders FROM STDIN", "COPY", pgTable("public", "orders", "")), want: model.DecisionAllow},
		{name: "select allowed", ast: postgresOperationAST("SELECT", "SELECT * FROM public.orders", "SELECT", pgTable("public", "orders", "")), want: model.DecisionAllow},
	}
	runPostgresRuleCases(t, rule, cases)
}

func TestR105UnindexedWrites(t *testing.T) {
	indexed := &fakeMetadataProvider{indexes: map[string]bool{
		"public.orders": true,
		"audit.events":  true,
	}}
	unindexed := &fakeMetadataProvider{indexes: map[string]bool{
		"public.orders": false,
		"audit.events":  false,
	}}
	cases := []struct {
		name     string
		fallback MetadataProvider
		context  engine.EvalContext
		ast      *model.AST
		want     model.Decision
	}{
		{name: "update without index", fallback: unindexed, ast: postgresOperationAST("UPDATE", "UPDATE public.orders SET status = 'x' WHERE id = 1", "UPDATE", pgTable("public", "orders", "")), want: model.DecisionApprove},
		{name: "mixed delete schema alias", fallback: unindexed, ast: postgresOperationAST("DELETE", "dElEtE FROM audit.events AS e\nWHERE e.id = 1", "dElEtE", pgTable("audit", "events", "e")), want: model.DecisionApprove},
		{name: "provider error approves", fallback: &fakeMetadataProvider{indexErr: errors.New("catalog unavailable")}, ast: postgresOperationAST("UPDATE", "UPDATE public.orders SET status = 'x' WHERE id = 1", "UPDATE", pgTable("public", "orders", "")), want: model.DecisionApprove},
		{name: "missing provider approves", ast: postgresOperationAST("DELETE", "DELETE FROM public.orders WHERE id = 1", "DELETE", pgTable("public", "orders", "")), want: model.DecisionApprove},
		{name: "wrong context provider approves", fallback: indexed, context: engine.EvalContext{MetadataProvider: "wrong"}, ast: postgresOperationAST("UPDATE", "UPDATE public.orders SET status = 'x' WHERE id = 1", "UPDATE", pgTable("public", "orders", "")), want: model.DecisionApprove},
		{name: "indexed update allowed", fallback: indexed, ast: postgresOperationAST("UPDATE", "UPDATE public.orders SET status = 'x' WHERE id = 1", "UPDATE", pgTable("public", "orders", "o")), want: model.DecisionAllow},
		{name: "indexed delete allowed", fallback: indexed, ast: postgresOperationAST("DELETE", "DELETE FROM audit.events WHERE id = 1", "DELETE", pgTable("audit", "events", "")), want: model.DecisionAllow},
		{name: "select needs no metadata", ast: postgresOperationAST("SELECT", "SELECT * FROM public.orders", "SELECT", pgTable("public", "orders", "")), want: model.DecisionAllow},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			rule := postgresRuleByID(t, "R105", test.fallback)
			context := test.context
			context.AST = test.ast
			assertPostgresRuleDecision(t, rule, context, test.want)
		})
	}
}

func TestR105ContextMetadataOverridesConstructor(t *testing.T) {
	fallback := &fakeMetadataProvider{indexes: map[string]bool{"public.orders": true}}
	override := &fakeMetadataProvider{indexes: map[string]bool{"public.orders": false}}
	rule := postgresRuleByID(t, "R105", fallback)
	context := engine.EvalContext{
		AST:              postgresOperationAST("UPDATE", "UPDATE public.orders SET a = 1", "UPDATE", pgTable("public", "orders", "")),
		MetadataProvider: override,
	}
	assertPostgresRuleDecision(t, rule, context, model.DecisionApprove)
}

func TestR106LargeTableAlter(t *testing.T) {
	provider := &fakeMetadataProvider{rows: map[string]int64{
		"public.orders": 100_001,
		"audit.events":  100_000,
		"public.small":  99,
	}}
	cases := []postgresRuleCase{
		{name: "large table default threshold", ast: postgresOperationAST("DDL", "ALTER TABLE public.orders ADD COLUMN note text", "ALTER TABLE", pgTable("public", "orders", "")), context: engine.EvalContext{MetadataProvider: provider}, want: model.DecisionApprove},
		{name: "mixed case schema alter", ast: postgresOperationAST("DDL", "aLtEr\nTABLE public.orders ADD COLUMN note text", " aLtEr \n TABLE ", pgTable("public", "orders", "o")), context: engine.EvalContext{MetadataProvider: provider}, want: model.DecisionApprove},
		{name: "custom threshold exceeded", ast: postgresOperationAST("DDL", "ALTER TABLE public.small ADD COLUMN note text", "ALTER TABLE", pgTable("public", "small", "")), context: engine.EvalContext{MetadataProvider: provider, Thresholds: map[string]float64{ThresholdLargeTableRows: 50}}, want: model.DecisionApprove},
		{name: "metadata error approves", ast: postgresOperationAST("DDL", "ALTER TABLE public.orders ADD COLUMN note text", "ALTER TABLE", pgTable("public", "orders", "")), context: engine.EvalContext{MetadataProvider: &fakeMetadataProvider{rowErr: errors.New("stats unavailable")}}, want: model.DecisionApprove},
		{name: "small table allowed", ast: postgresOperationAST("DDL", "ALTER TABLE public.small ADD COLUMN note text", "ALTER TABLE", pgTable("public", "small", "")), context: engine.EvalContext{MetadataProvider: provider}, want: model.DecisionAllow},
		{name: "equal threshold allowed", ast: postgresOperationAST("DDL", "ALTER TABLE audit.events ADD COLUMN note text", "ALTER TABLE", pgTable("audit", "events", "")), context: engine.EvalContext{MetadataProvider: provider}, want: model.DecisionAllow},
		{name: "create large table signal allowed", ast: postgresOperationAST("DDL", "CREATE TABLE public.orders(id bigint)", "CREATE TABLE", pgTable("public", "orders", "")), want: model.DecisionAllow},
	}
	runPostgresRuleCases(t, postgresRuleByID(t, "R106", nil), cases)
}

func TestR107LongTransactions(t *testing.T) {
	cases := []postgresRuleCase{
		{name: "long active transaction", ast: postgresOperationAST("SELECT", "SELECT * FROM public.orders", "SELECT"), context: transactionContext(TransactionState{InTransaction: true, AgeMS: 5_001}), want: model.DecisionWarn},
		{name: "idle transaction", ast: postgresOperationAST("SELECT", "SELECT\n1", "SELECT"), context: transactionContext(TransactionState{InTransaction: true, AgeMS: 6_000, IdleMS: 5_001}), want: model.DecisionWarn},
		{name: "custom long threshold", ast: postgresOperationAST("UPDATE", "uPdAtE public.orders SET a = 1", "UPDATE"), context: transactionContextWithThresholds(TransactionState{InTransaction: true, AgeMS: 101}, map[string]float64{ThresholdLongTransactionMS: 100}), want: model.DecisionWarn},
		{name: "custom idle threshold", ast: postgresOperationAST("SELECT", "SELECT 1", "SELECT"), context: transactionContextWithThresholds(TransactionState{InTransaction: true, AgeMS: 101, IdleMS: 51}, map[string]float64{ThresholdIdleTransactionMS: 50}), want: model.DecisionWarn},
		{name: "not in transaction allowed", ast: postgresOperationAST("SELECT", "SELECT 1", "SELECT"), context: transactionContext(TransactionState{}), want: model.DecisionAllow},
		{name: "short active transaction allowed", ast: postgresOperationAST("SELECT", "SELECT 1", "SELECT"), context: transactionContext(TransactionState{InTransaction: true, AgeMS: 4_999}), want: model.DecisionAllow},
		{name: "at thresholds allowed", ast: postgresOperationAST("SELECT", "SELECT 1", "SELECT"), context: transactionContext(TransactionState{InTransaction: true, AgeMS: 5_000, IdleMS: 5_000}), want: model.DecisionAllow},
	}
	runPostgresRuleCases(t, postgresRuleByID(t, "R107", nil), cases)
}

func TestPostgresRulesFailClosed(t *testing.T) {
	tests := []struct {
		name    string
		ruleID  string
		meta    MetadataProvider
		context engine.EvalContext
	}{
		{name: "missing ast", ruleID: "R101", context: engine.EvalContext{}},
		{name: "missing structured operation", ruleID: "R101", context: engine.EvalContext{AST: astWith("postgres", "DDL", "DROP TABLE t")}},
		{name: "direct mysql evaluation", ruleID: "R102", context: engine.EvalContext{AST: astWith("mysql", "ADMIN", "REINDEX TABLE t")}},
		{name: "invalid function signal", ruleID: "R103", context: engine.EvalContext{AST: postgresFunctionAST("SELECT 1", "pg_catalog..pg_read_file")}},
		{name: "invalid large table threshold", ruleID: "R106", context: engine.EvalContext{AST: postgresOperationAST("DDL", "ALTER TABLE t ADD COLUMN a int", "ALTER TABLE", pgTable("", "t", "")), MetadataProvider: &fakeMetadataProvider{rows: map[string]int64{"t": 1}}, Thresholds: map[string]float64{ThresholdLargeTableRows: -1}}},
		{name: "negative table rows", ruleID: "R106", context: engine.EvalContext{AST: postgresOperationAST("DDL", "ALTER TABLE t ADD COLUMN a int", "ALTER TABLE", pgTable("", "t", "")), MetadataProvider: &fakeMetadataProvider{rows: map[string]int64{"t": -1}}}},
		{name: "missing transaction provider", ruleID: "R107", context: engine.EvalContext{AST: postgresOperationAST("SELECT", "SELECT 1", "SELECT")}},
		{name: "transaction provider error", ruleID: "R107", meta: &fakeMetadataProvider{transactionErr: errors.New("state unavailable")}, context: engine.EvalContext{AST: postgresOperationAST("SELECT", "SELECT 1", "SELECT")}},
		{name: "negative transaction age", ruleID: "R107", meta: &fakeMetadataProvider{transaction: TransactionState{InTransaction: true, AgeMS: -1}}, context: engine.EvalContext{AST: postgresOperationAST("SELECT", "SELECT 1", "SELECT")}},
		{name: "wrong context provider", ruleID: "R107", meta: &fakeMetadataProvider{}, context: engine.EvalContext{AST: postgresOperationAST("SELECT", "SELECT 1", "SELECT"), MetadataProvider: struct{}{}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rule := postgresRuleByID(t, test.ruleID, test.meta)
			_, err := rule.Eval(test.context)
			require.Error(t, err)
		})
	}
}

func TestPostgresTypedNilMetadataFailsClosed(t *testing.T) {
	var provider *fakeMetadataProvider

	r105 := postgresRuleByID(t, "R105", provider)
	r105Result, err := r105.Eval(engine.EvalContext{AST: postgresOperationAST(
		"UPDATE",
		"UPDATE public.orders SET a = 1 WHERE id = 1",
		"UPDATE",
		pgTable("public", "orders", ""),
	)})
	require.NoError(t, err)
	require.Equal(t, model.DecisionApprove, r105Result.Decision)

	r107 := postgresRuleByID(t, "R107", provider)
	_, err = r107.Eval(engine.EvalContext{AST: postgresOperationAST(
		"SELECT",
		"SELECT 1",
		"SELECT",
	)})
	require.Error(t, err)
}

type postgresRuleCase struct {
	name    string
	ast     *model.AST
	context engine.EvalContext
	want    model.Decision
}

func runPostgresRuleCases(t *testing.T, rule engine.Rule, cases []postgresRuleCase) {
	t.Helper()
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			context := test.context
			context.AST = test.ast
			assertPostgresRuleDecision(t, rule, context, test.want)
		})
	}
}

func assertPostgresRuleDecision(
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

func postgresRuleByID(t *testing.T, id string, meta MetadataProvider) engine.Rule {
	t.Helper()
	for _, rule := range NewPostgresRules(meta) {
		if rule.ID() == id {
			return rule
		}
	}
	t.Fatalf("PostgreSQL rule %s not found", id)
	return nil
}

func postgresOperationAST(
	statementType string,
	rawSQL string,
	operation string,
	tables ...model.ObjectRef,
) *model.AST {
	ast := astWith("postgres", statementType, rawSQL)
	ast.Operations = []string{operation}
	ast.Tables = append([]model.ObjectRef{}, tables...)
	return ast
}

func postgresFunctionAST(rawSQL string, functions ...string) *model.AST {
	ast := postgresOperationAST("SELECT", rawSQL, "SELECT")
	ast.Functions = append([]string{}, functions...)
	return ast
}

func pgTable(schema, table, alias string) model.ObjectRef {
	return model.ObjectRef{Schema: schema, Table: table, Alias: alias}
}

func transactionContext(state TransactionState) engine.EvalContext {
	return engine.EvalContext{MetadataProvider: &fakeMetadataProvider{transaction: state}}
}

func transactionContextWithThresholds(
	state TransactionState,
	thresholds map[string]float64,
) engine.EvalContext {
	context := transactionContext(state)
	context.Thresholds = thresholds
	return context
}

type fakeMetadataProvider struct {
	indexes        map[string]bool
	rows           map[string]int64
	transaction    TransactionState
	indexErr       error
	rowErr         error
	transactionErr error
}

func (provider *fakeMetadataProvider) TableHasIndex(schema, table string) (bool, error) {
	if provider == nil {
		return false, errors.New("nil metadata provider")
	}
	if provider.indexErr != nil {
		return false, provider.indexErr
	}
	value, exists := provider.indexes[metadataTableKey(schema, table)]
	if !exists {
		return false, fmt.Errorf("index metadata not found for %s", metadataTableKey(schema, table))
	}
	return value, nil
}

func (provider *fakeMetadataProvider) TableRowCount(schema, table string) (int64, error) {
	if provider == nil {
		return 0, errors.New("nil metadata provider")
	}
	if provider.rowErr != nil {
		return 0, provider.rowErr
	}
	value, exists := provider.rows[metadataTableKey(schema, table)]
	if !exists {
		return 0, fmt.Errorf("row metadata not found for %s", metadataTableKey(schema, table))
	}
	return value, nil
}

func (provider *fakeMetadataProvider) TransactionState() (TransactionState, error) {
	if provider == nil {
		return TransactionState{}, errors.New("nil metadata provider")
	}
	if provider.transactionErr != nil {
		return TransactionState{}, provider.transactionErr
	}
	return provider.transaction, nil
}

func metadataTableKey(schema, table string) string {
	if schema == "" {
		return strings.ToLower(table)
	}
	return strings.ToLower(schema + "." + table)
}

var (
	_ MetadataProvider            = (*fakeMetadataProvider)(nil)
	_ transactionMetadataProvider = (*fakeMetadataProvider)(nil)
)

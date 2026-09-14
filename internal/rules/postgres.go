package rules

import (
	"fmt"
	"strings"

	"github.com/cuipengdba/agentsql/internal/engine"
	"github.com/cuipengdba/agentsql/internal/model"
)

const (
	// ThresholdLargeTableRows controls R106 large-table ALTER detection.
	ThresholdLargeTableRows = "large_table_rows"
	// ThresholdLongTransactionMS controls R107 active transaction age.
	ThresholdLongTransactionMS = "long_transaction_ms"
	// ThresholdIdleTransactionMS controls R107 idle-in-transaction age.
	ThresholdIdleTransactionMS = "idle_transaction_ms"

	defaultLargeTableRows    = defaultMaxScanRows
	defaultLongTransactionMS = 5_000
	defaultIdleTransactionMS = 5_000
)

var postgresDangerousFunctions = map[string]struct{}{
	"lo_export":            {},
	"lo_import":            {},
	"pg_cancel_backend":    {},
	"pg_ls_dir":            {},
	"pg_ls_logdir":         {},
	"pg_read_binary_file":  {},
	"pg_read_file":         {},
	"pg_reload_conf":       {},
	"pg_rotate_logfile":    {},
	"pg_stat_file":         {},
	"pg_terminate_backend": {},
}

// TransactionState is a side-effect-free snapshot used by R107.
type TransactionState struct {
	InTransaction bool
	AgeMS         int64
	IdleMS        int64
}

// MetadataProvider supplies the minimum read-only metadata required by T06.
// A real database-backed implementation is deferred to T10.
type MetadataProvider interface {
	TableHasIndex(schema, table string) (bool, error)
	TableRowCount(schema, table string) (int64, error)
}

// TransactionMetadataProvider extends PostgreSQL table metadata with session state.
type TransactionMetadataProvider interface {
	MetadataProvider
	TransactionState() (TransactionState, error)
}

// NewPostgresRules returns R101-R107 in stable evaluation order.
func NewPostgresRules(meta MetadataProvider) []engine.Rule {
	return []engine.Rule{
		r101Rule{genericRule: genericRule{id: "R101", level: model.RiskDeny}},
		r102Rule{genericRule: genericRule{id: "R102", level: model.RiskApprove}},
		r103Rule{genericRule: genericRule{id: "R103", level: model.RiskDeny}},
		r104Rule{genericRule: genericRule{id: "R104", level: model.RiskDeny}},
		r105Rule{genericRule: genericRule{id: "R105", level: model.RiskApprove}, meta: meta},
		r106Rule{genericRule: genericRule{id: "R106", level: model.RiskApprove}, meta: meta},
		r107Rule{genericRule: genericRule{id: "R107", level: model.RiskWarn}, meta: meta},
	}
}

// 攻击场景：删除 PostgreSQL 数据库、表或关键模式对象造成不可逆的数据丢失。
type r101Rule struct{ genericRule }

func (r101Rule) Dialect() model.DBDialect {
	return model.DBDialect("postgres")
}

func (r101Rule) Eval(context engine.EvalContext) (engine.RuleResult, error) {
	ast, err := requiredPostgresAST(context)
	if err != nil {
		return engine.RuleResult{}, fmt.Errorf("R101: %w", err)
	}
	if skipPostgresDialectRule(ast) {
		return allowResult(), nil
	}
	if containsPostgresOperation(
		ast.Operations,
		"DROP DATABASE",
		"DROP TABLE",
		"DROP SCHEMA",
		"DROP SEQUENCE",
		"DROP FUNCTION",
		"DROP PROCEDURE",
		"DROP VIEW",
		"DROP MATERIALIZED VIEW",
	) {
		return denyResult(
			"检测到高危 DROP 对象操作，执行后可能造成不可逆的数据或结构丢失",
			"请移除 DROP 操作；如需清理对象，请由 DBA 在受控变更流程中处理",
		), nil
	}
	return allowResult(), nil
}

// 攻击场景：持重锁维护操作长时间阻塞业务读写并扩大故障影响。
type r102Rule struct{ genericRule }

func (r102Rule) Dialect() model.DBDialect {
	return model.DBDialect("postgres")
}

func (r102Rule) Eval(context engine.EvalContext) (engine.RuleResult, error) {
	ast, err := requiredPostgresAST(context)
	if err != nil {
		return engine.RuleResult{}, fmt.Errorf("R102: %w", err)
	}
	if skipPostgresDialectRule(ast) {
		return allowResult(), nil
	}
	if containsPostgresOperation(ast.Operations, "VACUUM FULL", "REINDEX", "CLUSTER") {
		return approveResult(
			"检测到可能持有重锁的 PostgreSQL 维护操作",
			"请确认业务低峰窗口、锁等待与回滚方案，并提交 DBA 人工审批",
		), nil
	}
	return allowResult(), nil
}

// 攻击场景：调用后台终止、配置重载或服务器文件函数突破数据库边界。
type r103Rule struct{ genericRule }

func (r103Rule) Dialect() model.DBDialect {
	return model.DBDialect("postgres")
}

func (r103Rule) Eval(context engine.EvalContext) (engine.RuleResult, error) {
	ast, err := requiredPostgresAST(context)
	if err != nil {
		return engine.RuleResult{}, fmt.Errorf("R103: %w", err)
	}
	if skipPostgresDialectRule(ast) {
		return allowResult(), nil
	}
	for _, signal := range ast.Functions {
		schema, function, ok := postgresFunctionName(signal)
		if !ok {
			return engine.RuleResult{}, fmt.Errorf("R103: invalid PostgreSQL function signal %q", signal)
		}
		if schema != "" && schema != "pg_catalog" {
			continue
		}
		if _, blocked := postgresDangerousFunctions[function]; blocked {
			return denyResult(
				fmt.Sprintf("检测到 PostgreSQL 高权限管理或文件函数 %s", strings.TrimSpace(signal)),
				"请移除该函数，并改用受授权的查询接口；管理操作应由 DBA 独立执行",
			), nil
		}
	}
	return allowResult(), nil
}

// 攻击场景：COPY PROGRAM 在数据库服务器上启动外部程序或执行系统命令。
type r104Rule struct{ genericRule }

func (r104Rule) Dialect() model.DBDialect {
	return model.DBDialect("postgres")
}

func (r104Rule) Eval(context engine.EvalContext) (engine.RuleResult, error) {
	ast, err := requiredPostgresAST(context)
	if err != nil {
		return engine.RuleResult{}, fmt.Errorf("R104: %w", err)
	}
	if skipPostgresDialectRule(ast) {
		return allowResult(), nil
	}
	if containsPostgresOperation(ast.Operations, "COPY PROGRAM") {
		return denyResult(
			"检测到 COPY PROGRAM，可能在数据库服务器上执行外部命令",
			"请移除 PROGRAM 子句；数据导入导出应使用 DBA 批准的受控通道",
		), nil
	}
	return allowResult(), nil
}

// 攻击场景：无索引支撑的 UPDATE/DELETE 引发全表扫描、锁放大和长事务。
type r105Rule struct {
	genericRule
	meta MetadataProvider
}

func (r105Rule) Dialect() model.DBDialect {
	return model.DBDialect("postgres")
}

func (rule r105Rule) Eval(context engine.EvalContext) (engine.RuleResult, error) {
	ast, err := requiredPostgresAST(context)
	if err != nil {
		return engine.RuleResult{}, fmt.Errorf("R105: %w", err)
	}
	if skipPostgresDialectRule(ast) {
		return allowResult(), nil
	}
	if !isStatement(ast, "UPDATE", "DELETE") {
		return allowResult(), nil
	}
	if len(ast.Tables) == 0 {
		return r105Uncertain("AST 未提供 UPDATE/DELETE 目标表"), nil
	}
	provider, err := postgresMetadataProvider(context, rule.meta)
	if err != nil {
		return r105Uncertain(err.Error()), nil
	}
	for _, table := range ast.Tables {
		object, err := tableObjectName(table)
		if err != nil {
			return r105Uncertain(err.Error()), nil
		}
		hasIndex, err := provider.TableHasIndex(table.Schema, table.Table)
		if err != nil {
			return r105Uncertain(fmt.Sprintf("读取表 %s 的索引元数据失败: %v", object, err)), nil
		}
		if !hasIndex {
			return approveResult(
				fmt.Sprintf("表 %s 未发现可支撑 UPDATE/DELETE 的索引", object),
				"请为过滤列建立合适索引并重新确认执行计划；确需执行时提交 DBA 人工审批",
			), nil
		}
	}
	return allowResult(), nil
}

// 攻击场景：在大表上执行 ALTER 或非并发建索引，长时间持锁并占用大量资源。
type r106Rule struct {
	genericRule
	meta MetadataProvider
}

func (r106Rule) Dialect() model.DBDialect {
	return model.DBDialect("postgres")
}

func (rule r106Rule) Eval(context engine.EvalContext) (engine.RuleResult, error) {
	ast, err := requiredPostgresAST(context)
	if err != nil {
		return engine.RuleResult{}, fmt.Errorf("R106: %w", err)
	}
	if skipPostgresDialectRule(ast) {
		return allowResult(), nil
	}
	if containsPostgresOperation(ast.Operations, "CREATE INDEX CONCURRENTLY") {
		return allowResult(), nil
	}
	if !containsPostgresOperation(ast.Operations, "ALTER TABLE", "CREATE INDEX") {
		return allowResult(), nil
	}
	threshold, err := positiveThreshold(context, ThresholdLargeTableRows, defaultLargeTableRows)
	if err != nil {
		return engine.RuleResult{}, fmt.Errorf("R106: %w", err)
	}
	if len(ast.Tables) == 0 {
		return r106Uncertain("AST 未提供结构变更目标表"), nil
	}
	provider, err := postgresMetadataProvider(context, rule.meta)
	if err != nil {
		return r106Uncertain(err.Error()), nil
	}
	for _, table := range ast.Tables {
		object, err := tableObjectName(table)
		if err != nil {
			return r106Uncertain(err.Error()), nil
		}
		rowCount, err := provider.TableRowCount(table.Schema, table.Table)
		if err != nil {
			return r106Uncertain(fmt.Sprintf("读取表 %s 的行数元数据失败: %v", object, err)), nil
		}
		if rowCount < 0 {
			return engine.RuleResult{}, fmt.Errorf("R106: table %s has negative row count %d", object, rowCount)
		}
		if float64(rowCount) > threshold {
			return approveResult(
				fmt.Sprintf("大表 %s 约有 %d 行，超过结构变更阈值 %.0f 行", object, rowCount, threshold),
				"请评估锁表和表重写影响，优先采用在线变更方案，并提交 DBA 人工审批",
			), nil
		}
	}
	return allowResult(), nil
}

// 攻击场景：长事务或空闲事务持续占用快照和锁，阻塞清理并导致表膨胀。
type r107Rule struct {
	genericRule
	meta MetadataProvider
}

func (r107Rule) Dialect() model.DBDialect {
	return model.DBDialect("postgres")
}

func (rule r107Rule) Eval(context engine.EvalContext) (engine.RuleResult, error) {
	_, err := requiredPostgresAST(context)
	if err != nil {
		return engine.RuleResult{}, fmt.Errorf("R107: %w", err)
	}
	if skipPostgresDialectRule(context.AST) {
		return allowResult(), nil
	}
	longThreshold, err := positiveThreshold(
		context,
		ThresholdLongTransactionMS,
		defaultLongTransactionMS,
	)
	if err != nil {
		return engine.RuleResult{}, fmt.Errorf("R107: %w", err)
	}
	idleThreshold, err := positiveThreshold(
		context,
		ThresholdIdleTransactionMS,
		defaultIdleTransactionMS,
	)
	if err != nil {
		return engine.RuleResult{}, fmt.Errorf("R107: %w", err)
	}
	provider, err := postgresTransactionMetadataProviderForContext(context, rule.meta)
	if err != nil {
		return engine.RuleResult{}, fmt.Errorf("R107: %w", err)
	}
	state, err := provider.TransactionState()
	if err != nil {
		return engine.RuleResult{}, fmt.Errorf("R107: read transaction state: %w", err)
	}
	if state.AgeMS < 0 || state.IdleMS < 0 {
		return engine.RuleResult{}, fmt.Errorf(
			"R107: invalid transaction ages age_ms=%d idle_ms=%d",
			state.AgeMS,
			state.IdleMS,
		)
	}
	if !state.InTransaction {
		return allowResult(), nil
	}
	if float64(state.IdleMS) > idleThreshold {
		return warnResult(
			fmt.Sprintf("事务已空闲 %dms，超过阈值 %.0fms", state.IdleMS, idleThreshold),
			"请尽快提交或回滚事务，避免继续占用锁和旧快照",
		), nil
	}
	if float64(state.AgeMS) > longThreshold {
		return warnResult(
			fmt.Sprintf("事务已持续 %dms，超过阈值 %.0fms", state.AgeMS, longThreshold),
			"请缩小事务范围并尽快提交；大批量操作请拆分为可控批次",
		), nil
	}
	return allowResult(), nil
}

func requiredPostgresAST(context engine.EvalContext) (*model.AST, error) {
	ast, err := requiredAST(context)
	if err != nil {
		return nil, err
	}
	if normalizedDialect(ast.Dialect) != "postgres" {
		return nil, fmt.Errorf("PostgreSQL rule received dialect %q", ast.Dialect)
	}
	for _, operation := range ast.Operations {
		if normalizePostgresSignal(operation) == "" {
			return nil, fmt.Errorf("PostgreSQL AST contains an empty structured operation")
		}
	}
	return ast, nil
}

func skipPostgresDialectRule(ast *model.AST) bool {
	if ast == nil {
		return true
	}
	if len(ast.Operations) == 0 {
		return true
	}
	if isStatement(ast, "UNKNOWN") && !ast.IsMulti {
		return true
	}
	return false
}

func containsPostgresOperation(operations []string, candidates ...string) bool {
	normalizedOperations := make([]string, 0, len(operations))
	for _, operation := range operations {
		normalizedOperations = append(normalizedOperations, normalizePostgresSignal(operation))
	}
	for _, candidate := range candidates {
		if containsFold(normalizedOperations, normalizePostgresSignal(candidate)) {
			return true
		}
	}
	return false
}

func normalizePostgresSignal(signal string) string {
	return strings.ToUpper(strings.Join(strings.Fields(signal), " "))
}

func postgresFunctionName(signal string) (schema string, function string, ok bool) {
	parts := strings.Split(strings.TrimSpace(signal), ".")
	if len(parts) == 0 || len(parts) > 2 {
		return "", "", false
	}
	for index := range parts {
		parts[index] = strings.ToLower(strings.TrimSpace(parts[index]))
		if parts[index] == "" {
			return "", "", false
		}
	}
	if len(parts) == 1 {
		return "", parts[0], true
	}
	return parts[0], parts[1], true
}

func postgresMetadataProvider(
	context engine.EvalContext,
	fallback MetadataProvider,
) (MetadataProvider, error) {
	if context.MetadataProvider != nil {
		provider, ok := context.MetadataProvider.(MetadataProvider)
		if !ok || isNilInterface(provider) {
			return nil, fmt.Errorf("EvalContext.MetadataProvider does not implement rules.MetadataProvider")
		}
		return provider, nil
	}
	if isNilInterface(fallback) {
		return nil, fmt.Errorf("PostgreSQL metadata provider is unavailable")
	}
	return fallback, nil
}

func postgresTransactionMetadataProviderForContext(
	context engine.EvalContext,
	fallback MetadataProvider,
) (TransactionMetadataProvider, error) {
	if context.MetadataProvider != nil {
		provider, ok := context.MetadataProvider.(TransactionMetadataProvider)
		if !ok || isNilInterface(provider) {
			return nil, fmt.Errorf(
				"EvalContext.MetadataProvider does not provide PostgreSQL transaction state",
			)
		}
		return provider, nil
	}
	provider, ok := fallback.(TransactionMetadataProvider)
	if !ok || isNilInterface(provider) {
		return nil, fmt.Errorf("PostgreSQL transaction metadata provider is unavailable")
	}
	return provider, nil
}

func r105Uncertain(reason string) engine.RuleResult {
	return approveResult(
		"无法确认 UPDATE/DELETE 的索引支撑情况: "+reason,
		"请补充可靠的表索引元数据并确认执行计划；在确认前提交 DBA 人工审批",
	)
}

func r106Uncertain(reason string) engine.RuleResult {
	return approveResult(
		"无法确认结构变更目标表的规模: "+reason,
		"请补充可靠的表行数元数据并评估锁表影响；在确认前提交 DBA 人工审批",
	)
}

var (
	_ engine.Rule = r101Rule{}
	_ engine.Rule = r102Rule{}
	_ engine.Rule = r103Rule{}
	_ engine.Rule = r104Rule{}
	_ engine.Rule = r105Rule{}
	_ engine.Rule = r106Rule{}
	_ engine.Rule = r107Rule{}
)

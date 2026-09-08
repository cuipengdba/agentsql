package rules

import (
	"fmt"

	"github.com/cuipengdba/agentsql/internal/engine"
	"github.com/cuipengdba/agentsql/internal/model"
)

const (
	// ThresholdMysqlTransactionAgeMS controls R204 transaction age.
	ThresholdMysqlTransactionAgeMS = "mysql_transaction_age_ms"
	// ThresholdMysqlTransactionAffectedRows controls R204 cumulative affected rows.
	ThresholdMysqlTransactionAffectedRows = "mysql_transaction_affected_rows"

	defaultMysqlTransactionAgeMS        = 5_000
	defaultMysqlTransactionAffectedRows = defaultMaxScanRows
)

// MysqlTransactionState is the optional session snapshot consumed by R204.
type MysqlTransactionState struct {
	InTransaction bool
	AgeMS         int64
	AffectedRows  int64
}

// MysqlTransactionMetadataProvider supplies optional MySQL session metadata.
// Complete large-transaction detection depends on T10 session/connection metadata.
type MysqlTransactionMetadataProvider interface {
	MysqlTransactionState() (MysqlTransactionState, error)
}

// NewMysqlRules returns R201-R204 in stable evaluation order.
func NewMysqlRules(meta MysqlTransactionMetadataProvider) []engine.Rule {
	return []engine.Rule{
		r201Rule{genericRule: genericRule{id: "R201", level: model.RiskDeny}},
		r202Rule{genericRule: genericRule{id: "R202", level: model.RiskApprove}},
		r203Rule{genericRule: genericRule{id: "R203", level: model.RiskDeny}},
		r204Rule{genericRule: genericRule{id: "R204", level: model.RiskApprove}, meta: meta},
	}
}

// 攻击场景：利用 MySQL 文件函数或导入导出语句读取、覆盖服务器文件。
type r201Rule struct{ genericRule }

func (r201Rule) Dialect() model.DBDialect {
	return model.DBDialect("mysql")
}

func (r201Rule) Eval(context engine.EvalContext) (engine.RuleResult, error) {
	ast, err := requiredMysqlAST(context)
	if err != nil {
		return engine.RuleResult{}, fmt.Errorf("R201: %w", err)
	}
	if containsFold(ast.Operations, "LOAD_FILE") ||
		containsFold(ast.Operations, "INTO OUTFILE") ||
		containsFold(ast.Operations, "INTO DUMPFILE") ||
		(isStatement(ast, "ADMIN") && containsFold(ast.Operations, "LOAD")) {
		return denyResult(
			"检测到 MySQL 服务器文件读取或写入操作",
			"请移除 LOAD_FILE、INTO OUTFILE/DUMPFILE 或 LOAD DATA，并改用受控的数据传输接口",
		), nil
	}
	return allowResult(), nil
}

// 攻击场景：多表写或无 LIMIT 批量写扩大锁范围并误修改大量数据。
type r202Rule struct{ genericRule }

func (r202Rule) Dialect() model.DBDialect {
	return model.DBDialect("mysql")
}

func (r202Rule) Eval(context engine.EvalContext) (engine.RuleResult, error) {
	ast, err := requiredMysqlAST(context)
	if err != nil {
		return engine.RuleResult{}, fmt.Errorf("R202: %w", err)
	}
	if !isStatement(ast, "UPDATE", "DELETE") {
		return allowResult(), nil
	}
	if len(ast.Tables) >= 2 {
		return approveResult(
			fmt.Sprintf("检测到涉及 %d 张表的 MySQL %s", len(ast.Tables), ast.StmtType),
			"请将写操作收敛为单表、明确 JOIN 条件并设置 LIMIT；确需多表写时提交 DBA 人工审批",
		), nil
	}
	if !ast.HasLimit {
		return approveResult(
			fmt.Sprintf("MySQL %s 未设置 LIMIT，可能形成无界批量写", ast.StmtType),
			"请增加 LIMIT 并按可控批次执行；确需无界写入时提交 DBA 人工审批",
		), nil
	}
	return allowResult(), nil
}

// 攻击场景：刷新、终止连接或清理二进制日志影响数据库可用性与恢复能力。
type r203Rule struct{ genericRule }

func (r203Rule) Dialect() model.DBDialect {
	return model.DBDialect("mysql")
}

func (r203Rule) Eval(context engine.EvalContext) (engine.RuleResult, error) {
	ast, err := requiredMysqlAST(context)
	if err != nil {
		return engine.RuleResult{}, fmt.Errorf("R203: %w", err)
	}
	// T23: parser currently collapses SET GLOBAL and SET SESSION to SET.
	// Do not infer scope from RawSQL; no SET form is blocked until scope is structured.
	if containsFold(ast.Operations, "FLUSH") ||
		containsFold(ast.Operations, "KILL") ||
		containsFold(ast.Operations, "PURGE") {
		return denyResult(
			"检测到 MySQL 高危管理命令，可能影响连接、缓存或二进制日志",
			"请移除该管理命令；FLUSH、KILL 和 PURGE 应由 DBA 在受控运维流程中执行",
		), nil
	}
	return allowResult(), nil
}

// 攻击场景：长时间或大影响行数事务持续占用锁、undo 与连接资源。
// 完整大事务判定依赖 T10 会话/连接元数据；元数据不可用时不误拦。
type r204Rule struct {
	genericRule
	meta MysqlTransactionMetadataProvider
}

func (r204Rule) Dialect() model.DBDialect {
	return model.DBDialect("mysql")
}

func (rule r204Rule) Eval(context engine.EvalContext) (engine.RuleResult, error) {
	_, err := requiredMysqlAST(context)
	if err != nil {
		return engine.RuleResult{}, fmt.Errorf("R204: %w", err)
	}
	provider, ok := mysqlTransactionMetadataProvider(context, rule.meta)
	if !ok {
		return allowResult(), nil
	}
	state, err := provider.MysqlTransactionState()
	if err != nil || !state.InTransaction || state.AgeMS < 0 || state.AffectedRows < 0 {
		return allowResult(), nil
	}
	ageThreshold, err := positiveThreshold(
		context,
		ThresholdMysqlTransactionAgeMS,
		defaultMysqlTransactionAgeMS,
	)
	if err != nil {
		return engine.RuleResult{}, fmt.Errorf("R204: %w", err)
	}
	rowThreshold, err := positiveThreshold(
		context,
		ThresholdMysqlTransactionAffectedRows,
		defaultMysqlTransactionAffectedRows,
	)
	if err != nil {
		return engine.RuleResult{}, fmt.Errorf("R204: %w", err)
	}
	if float64(state.AgeMS) > ageThreshold {
		return approveResult(
			fmt.Sprintf("MySQL 事务已持续 %dms，超过阈值 %.0fms", state.AgeMS, ageThreshold),
			"请缩小事务范围并尽快提交或回滚；确需继续时提交 DBA 人工审批",
		), nil
	}
	if float64(state.AffectedRows) > rowThreshold {
		return approveResult(
			fmt.Sprintf("MySQL 事务累计影响 %d 行，超过阈值 %.0f 行", state.AffectedRows, rowThreshold),
			"请将写操作拆分为有 LIMIT 的小批次；确需大事务时提交 DBA 人工审批",
		), nil
	}
	return allowResult(), nil
}

func requiredMysqlAST(context engine.EvalContext) (*model.AST, error) {
	ast, err := requiredAST(context)
	if err != nil {
		return nil, err
	}
	if normalizedDialect(ast.Dialect) != "mysql" {
		return nil, fmt.Errorf("MySQL rule received dialect %q", ast.Dialect)
	}
	return ast, nil
}

func mysqlTransactionMetadataProvider(
	context engine.EvalContext,
	fallback MysqlTransactionMetadataProvider,
) (MysqlTransactionMetadataProvider, bool) {
	if context.MetadataProvider != nil {
		provider, ok := context.MetadataProvider.(MysqlTransactionMetadataProvider)
		if !ok || isNilInterface(provider) {
			return nil, false
		}
		return provider, true
	}
	if isNilInterface(fallback) {
		return nil, false
	}
	return fallback, true
}

var (
	_ engine.Rule = r201Rule{}
	_ engine.Rule = r202Rule{}
	_ engine.Rule = r203Rule{}
	_ engine.Rule = r204Rule{}
)

package rules

import (
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cuipengdba/agentsql/internal/engine"
	"github.com/cuipengdba/agentsql/internal/model"
	policyresolver "github.com/cuipengdba/agentsql/internal/policy"
)

const (
	// ThresholdMaxScanRows controls R004 estimated scan rows.
	ThresholdMaxScanRows = "max_scan_rows"
	// ThresholdRowLimit controls R005 large-result detection.
	ThresholdRowLimit = "row_limit"
	// ThresholdQPS controls R008 per-Agent requests per second.
	ThresholdQPS = "qps_per_agent"
	// ThresholdMaxConcurrent controls R008 per-Agent in-flight requests.
	ThresholdMaxConcurrent = "max_conns_per_datasource"
	// ThresholdMaxSQLLength controls R009 SQL byte length.
	ThresholdMaxSQLLength = "max_sql_length"
	// ThresholdMaxNesting controls R009 parser-reported nesting depth.
	ThresholdMaxNesting = "max_nesting_depth"
	// ThresholdMaxUnion controls R009 parser-reported UNION count.
	ThresholdMaxUnion = "max_union_count"

	// OperationNestingDepth prefixes the parser's structured nesting signal.
	OperationNestingDepth = "NESTING_DEPTH"
	// OperationUnionCount prefixes the parser's structured UNION signal.
	OperationUnionCount = "UNION_COUNT"
	// OperationSelectColumn prefixes a parser-reported outer projection column.
	OperationSelectColumn = "SELECT_COLUMN"

	defaultMaxScanRows   = 100_000
	defaultRowLimit      = 1_000
	defaultQPS           = 20
	defaultMaxConcurrent = 5
	defaultMaxSQLLength  = 1 << 20
	defaultMaxNesting    = 16
	defaultMaxUnion      = 16
)

const (
	// RateLimitQPS indicates that the token bucket is empty.
	RateLimitQPS RateLimitReason = "qps"
	// RateLimitConcurrency indicates that all in-flight slots are occupied.
	RateLimitConcurrency RateLimitReason = "concurrency"
)

// RateLimitReason identifies the exhausted part of a rate limit.
type RateLimitReason string

// RateLimitResult is the atomic admission result returned by a RateLimiter.
type RateLimitResult struct {
	Allowed bool
	Reason  RateLimitReason
}

// RateLimiter reserves one QPS token and one concurrent-request slot when it
// allows a request. The pipeline must call Release after that request finishes.
type RateLimiter interface {
	Allow(key string, qps float64, maxConcurrent int) (RateLimitResult, error)
	Release(key string) error
}

// Clock makes token refill deterministic in tests.
type Clock interface {
	Now() time.Time
}

type systemClock struct{}

func (systemClock) Now() time.Time {
	return time.Now()
}

type bucketState struct {
	tokens   float64
	last     time.Time
	inFlight int
}

// TokenBucketLimiter is an in-memory, concurrency-safe QPS and in-flight limiter.
type TokenBucketLimiter struct {
	mu      sync.Mutex
	clock   Clock
	buckets map[string]*bucketState
}

// NewTokenBucketLimiter constructs a limiter with an injectable clock.
func NewTokenBucketLimiter(clock Clock) (*TokenBucketLimiter, error) {
	if isNilInterface(clock) {
		return nil, fmt.Errorf("create token bucket limiter: nil clock")
	}
	return &TokenBucketLimiter{
		clock:   clock,
		buckets: make(map[string]*bucketState),
	}, nil
}

// NewDefaultTokenBucketLimiter constructs a limiter backed by the system clock.
func NewDefaultTokenBucketLimiter() *TokenBucketLimiter {
	return &TokenBucketLimiter{
		clock:   systemClock{},
		buckets: make(map[string]*bucketState),
	}
}

// Allow atomically reserves a token and concurrent-request slot.
func (limiter *TokenBucketLimiter) Allow(
	key string,
	qps float64,
	maxConcurrent int,
) (RateLimitResult, error) {
	if limiter == nil || isNilInterface(limiter.clock) {
		return RateLimitResult{}, fmt.Errorf("check rate limit: uninitialized limiter")
	}
	if strings.TrimSpace(key) == "" || strings.TrimSpace(key) != key {
		return RateLimitResult{}, fmt.Errorf("check rate limit: invalid key %q", key)
	}
	if qps <= 0 || math.IsNaN(qps) || math.IsInf(qps, 0) {
		return RateLimitResult{}, fmt.Errorf("check rate limit: invalid qps %v", qps)
	}
	if maxConcurrent <= 0 {
		return RateLimitResult{}, fmt.Errorf("check rate limit: invalid concurrency %d", maxConcurrent)
	}

	limiter.mu.Lock()
	defer limiter.mu.Unlock()

	now := limiter.clock.Now()
	capacity := math.Max(1, qps)
	state, exists := limiter.buckets[key]
	if !exists {
		state = &bucketState{tokens: capacity, last: now}
		limiter.buckets[key] = state
	} else {
		if now.Before(state.last) {
			return RateLimitResult{}, fmt.Errorf("check rate limit: clock moved backwards")
		}
		state.tokens = math.Min(capacity, state.tokens+now.Sub(state.last).Seconds()*qps)
		state.last = now
	}

	if state.inFlight >= maxConcurrent {
		return RateLimitResult{Reason: RateLimitConcurrency}, nil
	}
	if state.tokens < 1 {
		return RateLimitResult{Reason: RateLimitQPS}, nil
	}

	state.tokens--
	state.inFlight++
	return RateLimitResult{Allowed: true}, nil
}

// Release returns one concurrent-request slot after execution finishes.
func (limiter *TokenBucketLimiter) Release(key string) error {
	if limiter == nil {
		return fmt.Errorf("release rate limit: uninitialized limiter")
	}
	if strings.TrimSpace(key) == "" || strings.TrimSpace(key) != key {
		return fmt.Errorf("release rate limit: invalid key %q", key)
	}

	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	state, exists := limiter.buckets[key]
	if !exists || state.inFlight == 0 {
		return fmt.Errorf("release rate limit %q: no active reservation", key)
	}
	state.inFlight--
	return nil
}

type genericRule struct {
	id    string
	level model.RiskLevel
}

func (rule genericRule) ID() string {
	return rule.id
}

func (genericRule) Dialect() model.DBDialect {
	return engine.DialectAll
}

func (rule genericRule) Level() model.RiskLevel {
	return rule.level
}

func (genericRule) Enabled() bool {
	return true
}

// NewGenericRules returns R001-R010 in stable order with an injected limiter.
// The caller retains the limiter so it can release R008 reservations.
func NewGenericRules(limiter RateLimiter) []engine.Rule {
	return []engine.Rule{
		r001Rule{genericRule{id: "R001", level: model.RiskDeny}},
		r002Rule{genericRule{id: "R002", level: model.RiskDeny}},
		r003Rule{genericRule{id: "R003", level: model.RiskDeny}},
		r004Rule{genericRule{id: "R004", level: model.RiskApprove}},
		r005Rule{genericRule{id: "R005", level: model.RiskWarn}},
		r006Rule{genericRule{id: "R006", level: model.RiskDeny}},
		r007Rule{genericRule{id: "R007", level: model.RiskDeny}},
		r008Rule{genericRule: genericRule{id: "R008", level: model.RiskDeny}, limiter: limiter},
		r009Rule{genericRule{id: "R009", level: model.RiskApprove}},
		r010Rule{genericRule{id: "R010", level: model.RiskDeny}},
	}
}

// 攻击场景：使用堆叠语句在首条合法 SQL 后夹带破坏性操作。
type r001Rule struct{ genericRule }

func (r001Rule) Eval(context engine.EvalContext) (engine.RuleResult, error) {
	ast, err := requiredAST(context)
	if err != nil {
		return engine.RuleResult{}, fmt.Errorf("R001: %w", err)
	}
	if ast.IsMulti {
		return denyResult(
			"检测到多语句请求，存在堆叠执行风险",
			"请一次只提交一条 SQL，并移除分号后的附加语句",
		), nil
	}
	return allowResult(), nil
}

// 攻击场景：无过滤条件或恒真条件导致全表 UPDATE/DELETE。
type r002Rule struct{ genericRule }

func (r002Rule) Eval(context engine.EvalContext) (engine.RuleResult, error) {
	ast, err := requiredAST(context)
	if err != nil {
		return engine.RuleResult{}, fmt.Errorf("R002: %w", err)
	}
	if !isStatement(ast, "UPDATE", "DELETE") {
		return allowResult(), nil
	}
	if !ast.HasWhere {
		return denyResult(
			fmt.Sprintf("%s 缺少 WHERE 条件，可能影响整张表", ast.StmtType),
			"请增加能够限定目标行的 WHERE 条件后重试",
		), nil
	}
	if ast.WhereTautology {
		return denyResult(
			fmt.Sprintf("%s 的 WHERE 条件恒真，可能影响整张表", ast.StmtType),
			"请移除恒真表达式，并改用主键或明确业务条件限定目标行",
		), nil
	}
	return allowResult(), nil
}

// 攻击场景：只读 Agent 尝试执行写入、DDL 或管理语句。
type r003Rule struct{ genericRule }

func (r003Rule) Eval(context engine.EvalContext) (engine.RuleResult, error) {
	ast, err := requiredAST(context)
	if err != nil {
		return engine.RuleResult{}, fmt.Errorf("R003: %w", err)
	}
	level := strings.ToLower(strings.TrimSpace(context.AgentLevel))
	switch level {
	case "readonly":
		if !isStatement(ast, "SELECT") {
			return denyResult(
				fmt.Sprintf("只读 Agent 不允许执行 %s 语句", ast.StmtType),
				"请改用 SELECT 查询，或由管理员为 Agent 授予所需的写入级别",
			), nil
		}
	case "dml", "ddl":
		return allowResult(), nil
	default:
		return engine.RuleResult{}, fmt.Errorf("R003: invalid agent level %q", context.AgentLevel)
	}
	return allowResult(), nil
}

// 攻击场景：查询计划扫描过多行，可能拖垮数据库或造成资源争用。
type r004Rule struct{ genericRule }

func (r004Rule) Eval(context engine.EvalContext) (engine.RuleResult, error) {
	ast, err := requiredAST(context)
	if err != nil {
		return engine.RuleResult{}, fmt.Errorf("R004: %w", err)
	}
	threshold, err := positiveThreshold(context, ThresholdMaxScanRows, defaultMaxScanRows)
	if err != nil {
		return engine.RuleResult{}, fmt.Errorf("R004: %w", err)
	}
	if ast.Explain == nil {
		return allowResult(), nil
	}
	if ast.Explain.EstScanRows < 0 {
		return engine.RuleResult{}, fmt.Errorf("R004: negative estimated scan rows %d", ast.Explain.EstScanRows)
	}
	if float64(ast.Explain.EstScanRows) > threshold {
		return approveResult(
			fmt.Sprintf("预估扫描 %d 行，超过阈值 %.0f 行", ast.Explain.EstScanRows, threshold),
			"请增加选择性更高的过滤条件或合适索引；确需执行时提交人工审批",
		), nil
	}
	return allowResult(), nil
}

// 攻击场景：无 LIMIT 的大结果查询消耗网络、内存并扩大数据暴露面。
type r005Rule struct{ genericRule }

func (r005Rule) Eval(context engine.EvalContext) (engine.RuleResult, error) {
	ast, err := requiredAST(context)
	if err != nil {
		return engine.RuleResult{}, fmt.Errorf("R005: %w", err)
	}
	fallback := float64(defaultRowLimit)
	if context.Datasource != nil && context.Datasource.RowLimit > 0 {
		fallback = float64(context.Datasource.RowLimit)
	}
	threshold, err := positiveThreshold(context, ThresholdRowLimit, fallback)
	if err != nil {
		return engine.RuleResult{}, fmt.Errorf("R005: %w", err)
	}
	if !isStatement(ast, "SELECT") {
		return allowResult(), nil
	}
	if context.RuntimeResult != nil && context.RuntimeResult.Truncated {
		return warnResult(
			fmt.Sprintf("查询结果超过执行层行数上限 %.0f，返回结果已被截断", fallback),
			"请增加更严格的过滤条件或降低查询 LIMIT，避免依赖执行层截断",
		), nil
	}
	if ast.HasLimit || ast.Explain == nil || (ast.IsPureAggregate && !ast.HasGroupBy) {
		return allowResult(), nil
	}
	if ast.Explain.EstScanRows < 0 {
		return engine.RuleResult{}, fmt.Errorf("R005: negative estimated result rows %d", ast.Explain.EstScanRows)
	}
	if float64(ast.Explain.EstScanRows) > threshold {
		return warnResult(
			fmt.Sprintf("查询未设置 LIMIT，预估结果规模 %d 行超过行数阈值 %.0f", ast.Explain.EstScanRows, threshold),
			"请增加 LIMIT 缩小结果集；执行层仍会按行数上限截断返回",
		), nil
	}
	return allowResult(), nil
}

// 攻击场景：通过注释、堆叠语句或未知语法绕过语句分类与安全规则。
type r006Rule struct{ genericRule }

func (r006Rule) Eval(context engine.EvalContext) (engine.RuleResult, error) {
	ast, err := requiredAST(context)
	if err != nil {
		return engine.RuleResult{}, fmt.Errorf("R006: %w", err)
	}
	if ast.IsMulti {
		return denyResult(
			"检测到堆叠语句，无法作为单条安全请求处理",
			"请删除附加语句并一次只提交一条 SQL",
		), nil
	}
	if isStatement(ast, "UNKNOWN") {
		return denyResult(
			"SQL 语句类型未知，无法建立可信的安全判定",
			"请改写为受支持的 SELECT、INSERT、UPDATE、DELETE、DDL 或管理语句",
		), nil
	}
	if containsFold(ast.Operations, "SQL_COMMENT") {
		return denyResult(
			"SQL 包含注释，存在注释穿插或截断绕过风险",
			"请移除 SQL 注释并提交语义完整的单条语句",
		), nil
	}
	return allowResult(), nil
}

// 攻击场景：调用资源消耗型函数阻塞连接或制造数据库拒绝服务。
type r007Rule struct{ genericRule }

func (r007Rule) Eval(context engine.EvalContext) (engine.RuleResult, error) {
	ast, err := requiredAST(context)
	if err != nil {
		return engine.RuleResult{}, fmt.Errorf("R007: %w", err)
	}
	blacklist, exists := dangerousFunctions[normalizedDialect(ast.Dialect)]
	if !exists {
		return engine.RuleResult{}, fmt.Errorf("R007: unsupported dialect %q", ast.Dialect)
	}
	for _, signal := range appendSignals(ast.Functions, ast.Operations) {
		name := strings.ToLower(strings.TrimSpace(signal))
		if separator := strings.LastIndex(name, "."); separator >= 0 {
			name = name[separator+1:]
		}
		if _, blocked := blacklist[name]; blocked {
			return denyResult(
				fmt.Sprintf("检测到危险函数 %s，可能造成连接阻塞或资源耗尽", signal),
				"请移除该函数，并使用普通查询表达业务需求",
			), nil
		}
	}
	return allowResult(), nil
}

var dangerousFunctions = map[string]map[string]struct{}{
	"postgres": {
		"pg_sleep": {},
	},
	"mysql": {
		"benchmark": {},
		"sleep":     {},
	},
}

// 攻击场景：单个 Agent 以突发 QPS 或过高并发压垮数据库连接池。
type r008Rule struct {
	genericRule
	limiter RateLimiter
}

func (rule r008Rule) Eval(context engine.EvalContext) (engine.RuleResult, error) {
	_, err := requiredAST(context)
	if err != nil {
		return engine.RuleResult{}, fmt.Errorf("R008: %w", err)
	}
	if isNilInterface(rule.limiter) {
		return engine.RuleResult{}, fmt.Errorf("R008: nil rate limiter")
	}
	if context.Agent == nil || strings.TrimSpace(context.Agent.ID) == "" {
		return engine.RuleResult{}, fmt.Errorf("R008: missing agent identity")
	}
	qps, err := positiveThreshold(context, ThresholdQPS, defaultQPS)
	if err != nil {
		return engine.RuleResult{}, fmt.Errorf("R008: %w", err)
	}
	maxConcurrent, err := positiveIntegerThreshold(
		context,
		ThresholdMaxConcurrent,
		defaultMaxConcurrent,
	)
	if err != nil {
		return engine.RuleResult{}, fmt.Errorf("R008: %w", err)
	}
	result, err := rule.limiter.Allow(context.Agent.ID, qps, maxConcurrent)
	if err != nil {
		return engine.RuleResult{}, fmt.Errorf("R008: check limiter: %w", err)
	}
	if result.Allowed {
		return allowResult(), nil
	}
	switch result.Reason {
	case RateLimitQPS:
		return denyResult(
			fmt.Sprintf("Agent %s 已超过每秒 %.2f 次请求的 QPS 限制", context.Agent.ID, qps),
			"请降低请求频率并采用指数退避后重试",
		), nil
	case RateLimitConcurrency:
		return denyResult(
			fmt.Sprintf("Agent %s 已达到 %d 个并发请求的限制", context.Agent.ID, maxConcurrent),
			"请等待正在执行的请求完成后再重试",
		), nil
	default:
		return engine.RuleResult{}, fmt.Errorf("R008: limiter denied with unknown reason %q", result.Reason)
	}
}

// 攻击场景：超长、深层嵌套或大量 UNION 的 SQL 消耗解析与执行资源。
type r009Rule struct{ genericRule }

func (r009Rule) Eval(context engine.EvalContext) (engine.RuleResult, error) {
	ast, err := requiredAST(context)
	if err != nil {
		return engine.RuleResult{}, fmt.Errorf("R009: %w", err)
	}
	maxLength, err := positiveIntegerThreshold(context, ThresholdMaxSQLLength, defaultMaxSQLLength)
	if err != nil {
		return engine.RuleResult{}, fmt.Errorf("R009: %w", err)
	}
	maxNesting, err := positiveIntegerThreshold(context, ThresholdMaxNesting, defaultMaxNesting)
	if err != nil {
		return engine.RuleResult{}, fmt.Errorf("R009: %w", err)
	}
	maxUnion, err := positiveIntegerThreshold(context, ThresholdMaxUnion, defaultMaxUnion)
	if err != nil {
		return engine.RuleResult{}, fmt.Errorf("R009: %w", err)
	}
	nesting, unionCount, err := operationComplexity(ast.Operations)
	if err != nil {
		return engine.RuleResult{}, fmt.Errorf("R009: %w", err)
	}
	if len([]byte(ast.RawSQL)) > maxLength {
		return approveResult(
			fmt.Sprintf("SQL 长度 %d 字节超过阈值 %d 字节", len([]byte(ast.RawSQL)), maxLength),
			"请拆分 SQL、移除不必要的表达式，或提交人工审批",
		), nil
	}
	if nesting > maxNesting {
		return approveResult(
			fmt.Sprintf("SQL 嵌套深度 %d 超过阈值 %d", nesting, maxNesting),
			"请减少子查询嵌套，优先改写为清晰的 JOIN 或分步查询；确需执行时提交人工审批",
		), nil
	}
	if unionCount > maxUnion {
		return approveResult(
			fmt.Sprintf("SQL UNION 数量 %d 超过阈值 %d", unionCount, maxUnion),
			"请减少 UNION 分支或拆成多个有界查询；确需执行时提交人工审批",
		), nil
	}
	return allowResult(), nil
}

// 攻击场景：Agent 访问策略未授权或被显式拒绝的表。
type r010Rule struct{ genericRule }

func (r010Rule) Eval(context engine.EvalContext) (engine.RuleResult, error) {
	ast, err := requiredAST(context)
	if err != nil {
		return engine.RuleResult{}, fmt.Errorf("R010: %w", err)
	}
	if context.Policy == nil {
		return engine.RuleResult{}, fmt.Errorf("R010: missing policy decision")
	}
	if err := validatePolicyPatterns(context.Policy); err != nil {
		return engine.RuleResult{}, fmt.Errorf("R010: %w", err)
	}
	for _, table := range ast.Tables {
		object, err := tableObjectName(table)
		if err != nil {
			return engine.RuleResult{}, fmt.Errorf("R010: %w", err)
		}
		if matchesAnyPolicyObject(context.Policy.DeniedTables, table) {
			return denyResult(
				fmt.Sprintf("表 %s 被策略显式拒绝访问", object),
				"请移除对该表的访问，或联系管理员调整拒绝策略",
			), nil
		}
		if !matchesAnyPolicyObject(context.Policy.AllowedTables, table) {
			return denyResult(
				fmt.Sprintf("表 %s 不在 Agent 的允许范围内", object),
				"请仅访问已授权表，或联系管理员补充表级授权",
			), nil
		}
	}
	if isStatement(ast, "SELECT") && len(ast.Tables) == 1 {
		// v0.1 intentionally treats wildcard table grants as granting every
		// column of matching tables. Narrowing wildcard grants needs metadata.
		if policyresolver.HasBroadTableGrant(context.Policy.AllowedTables, ast.Tables[0]) {
			return allowResult(), nil
		}
		object, err := tableObjectName(ast.Tables[0])
		if err != nil {
			return engine.RuleResult{}, fmt.Errorf("R010: %w", err)
		}
		allowedColumns, constrained := context.Policy.ColumnACL[object]
		if constrained && !containsExactIdentifier(allowedColumns, "*") {
			projectedColumns, err := r010ProjectedColumns(ast)
			if err != nil {
				return engine.RuleResult{}, fmt.Errorf("R010: %w", err)
			}
			for _, column := range projectedColumns {
				if column == "*" || strings.HasSuffix(column, ".*") {
					return denyResult(
						fmt.Sprintf("表 %s 已启用列级白名单，不允许 SELECT *", object),
						"请显式列出已授权投影列，或联系管理员调整列级授权",
					), nil
				}
				if containsExactIdentifier(allowedColumns, column) {
					continue
				}
				return denyResult(
					fmt.Sprintf("列 %s.%s 不在 Agent 的列级白名单内", object, column),
					"请仅查询策略允许的列，或联系管理员补充列级授权",
				), nil
			}
		}
	}
	// v0.1 known limitation: JOINs and self-joins have no reliable projection
	// ownership, so column ACLs are not applied once table authorization passes.
	return allowResult(), nil
}

func r010ProjectedColumns(ast *model.AST) ([]string, error) {
	columns := make([]string, 0)
	seen := make(map[string]struct{})
	for _, operation := range ast.Operations {
		name, rawValue, found := strings.Cut(strings.TrimSpace(operation), ":")
		if !found || !strings.EqualFold(name, OperationSelectColumn) {
			continue
		}
		column := strings.TrimSpace(rawValue)
		if column == "" {
			return nil, fmt.Errorf("empty %s operation", OperationSelectColumn)
		}
		if _, exists := seen[column]; exists {
			continue
		}
		seen[column] = struct{}{}
		columns = append(columns, column)
	}
	return columns, nil
}

func containsExactIdentifier(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func requiredAST(context engine.EvalContext) (*model.AST, error) {
	if context.AST == nil {
		return nil, fmt.Errorf("missing AST")
	}
	if context.AST.Dialect != model.DBDialect("postgres") &&
		context.AST.Dialect != model.DBDialect("mysql") {
		return nil, fmt.Errorf("unsupported dialect %q", context.AST.Dialect)
	}
	statementType := string(context.AST.StmtType)
	if strings.TrimSpace(statementType) != statementType {
		return nil, fmt.Errorf("invalid statement type %q", context.AST.StmtType)
	}
	switch strings.ToUpper(statementType) {
	case "SELECT", "INSERT", "UPDATE", "DELETE", "DDL", "ADMIN", "UNKNOWN":
	default:
		return nil, fmt.Errorf("invalid statement type %q", context.AST.StmtType)
	}
	return context.AST, nil
}

func isStatement(ast *model.AST, candidates ...string) bool {
	for _, candidate := range candidates {
		if strings.EqualFold(string(ast.StmtType), candidate) {
			return true
		}
	}
	return false
}

func allowResult() engine.RuleResult {
	return engine.RuleResult{Decision: model.DecisionAllow}
}

func denyResult(message, suggestion string) engine.RuleResult {
	return engine.RuleResult{
		Decision:   model.DecisionDeny,
		Message:    message,
		Suggestion: suggestion,
	}
}

func approveResult(message, suggestion string) engine.RuleResult {
	return engine.RuleResult{
		Decision:   model.DecisionApprove,
		Message:    message,
		Suggestion: suggestion,
	}
}

func warnResult(message, suggestion string) engine.RuleResult {
	return engine.RuleResult{
		Decision:   model.DecisionWarn,
		Message:    message,
		Suggestion: suggestion,
	}
}

func positiveThreshold(
	context engine.EvalContext,
	name string,
	defaultValue float64,
) (float64, error) {
	value, exists := context.Thresholds[name]
	if !exists {
		value = defaultValue
	}
	if value <= 0 || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, fmt.Errorf("threshold %q must be a positive finite number", name)
	}
	return value, nil
}

func positiveIntegerThreshold(
	context engine.EvalContext,
	name string,
	defaultValue int,
) (int, error) {
	value, err := positiveThreshold(context, name, float64(defaultValue))
	if err != nil {
		return 0, err
	}
	maxInt := float64(^uint(0) >> 1)
	if math.Trunc(value) != value || value > maxInt {
		return 0, fmt.Errorf("threshold %q must be a positive integer", name)
	}
	return int(value), nil
}

func containsFold(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), target) {
			return true
		}
	}
	return false
}

func appendSignals(first, second []string) []string {
	signals := make([]string, 0, len(first)+len(second))
	signals = append(signals, first...)
	signals = append(signals, second...)
	return signals
}

func normalizedDialect(dialect model.DBDialect) string {
	return strings.ToLower(strings.TrimSpace(string(dialect)))
}

func operationComplexity(operations []string) (nestingDepth int, unionCount int, err error) {
	for _, operation := range operations {
		name, rawValue, found := strings.Cut(strings.TrimSpace(operation), ":")
		if !found {
			continue
		}
		if !strings.EqualFold(name, OperationNestingDepth) &&
			!strings.EqualFold(name, OperationUnionCount) {
			continue
		}
		value, parseErr := strconv.Atoi(strings.TrimSpace(rawValue))
		if parseErr != nil || value < 0 {
			return 0, 0, fmt.Errorf("invalid structured operation %q", operation)
		}
		switch {
		case strings.EqualFold(name, OperationNestingDepth):
			if value > nestingDepth {
				nestingDepth = value
			}
		case strings.EqualFold(name, OperationUnionCount):
			if value > unionCount {
				unionCount = value
			}
		}
	}
	return nestingDepth, unionCount, nil
}

func validatePolicyPatterns(policy *model.PolicyDecision) error {
	return policyresolver.ValidateTablePatterns(
		appendSignals(policy.AllowedTables, policy.DeniedTables),
	)
}

func tableObjectName(table model.ObjectRef) (string, error) {
	if strings.TrimSpace(table.Table) == "" {
		return "", fmt.Errorf("AST contains an empty table name")
	}
	if strings.TrimSpace(table.Table) != table.Table || strings.TrimSpace(table.Schema) != table.Schema {
		return "", fmt.Errorf("AST table reference contains surrounding whitespace")
	}
	if table.Schema == "" {
		return table.Table, nil
	}
	return table.Schema + "." + table.Table, nil
}

func matchesAnyPolicyObject(patterns []string, table model.ObjectRef) bool {
	return policyresolver.MatchesAnyTable(patterns, table)
}

func isNilInterface(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

var (
	_ engine.Rule = r001Rule{}
	_ engine.Rule = r002Rule{}
	_ engine.Rule = r003Rule{}
	_ engine.Rule = r004Rule{}
	_ engine.Rule = r005Rule{}
	_ engine.Rule = r006Rule{}
	_ engine.Rule = r007Rule{}
	_ engine.Rule = r008Rule{}
	_ engine.Rule = r009Rule{}
	_ engine.Rule = r010Rule{}
	_ RateLimiter = (*TokenBucketLimiter)(nil)
)

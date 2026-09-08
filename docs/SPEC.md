# AgentSQL 开发规格书 SPEC v2.0（Codex 唯一技术权威 / 放仓库 docs/SPEC.md）

> 2026-09-04 ｜ 路线：完全自研 ｜ 版本：v2.0（取代 v1.0，已并入界面视觉设计、统一任务编号 T01-T25）
> 铁律：**契约（第 3 章）冻结后不得擅改；一次只实现一个任务单；安全无法判定时一律 fail-closed。**

---

# 第 1 章 项目总览

## 1.1 是什么
AgentSQL（中文：智盾，AI-Native Database Security Gateway；仓库 agentsql-gateway，二进制 agentsql/agentsqlctl，Key 前缀 asql_）是 AI Agent 访问关系型数据库的安全网关，形态是**生产级数据库 MCP Server**。AI 客户端（Cursor/Claude/豆包/自研 Agent）通过 MCP 接入，每条 SQL 走六段流水线后才访问真实数据库：
```
Parse(解析AST) → Auth(身份权限) → Guard(规则+EXPLAIN) → Decide(allow/deny/approve)
→ Execute(限流/超时/行数上限) → Redact+Audit(脱敏+审计)
```

## 1.2 v0.1 范围（严格收敛）
- 数据库：**PostgreSQL、MySQL**；接入：MCP over **stdio + Streamable HTTP**
- 能力：API Key 认证、库/表/列三级权限、只读强制、21 条内置规则、EXPLAIN 风险评估、审计、双主题 Web 控制台（含六段安检流动画与拦截演示台）
- 元数据：SQLite 单文件；交付：单二进制（内嵌前端）+ Docker Compose
- **不做（v0.2+）**：行级权限、完整动态脱敏（v0.1 只做手机/邮箱+接口）、完整审批流（v0.1 只拦截记录）、SSO、集群 HA、Oracle/国产库、WebSocket 全局实时流、PDF 合规报告、在线 Live Demo

## 1.3 架构
```
AI Client ─MCP(stdio/http)→ ┌────────────────────────────┐
                            │ mcpserver(tools)→pipeline    │
Web 控制台 ─REST→ adminapi ─┤  auth/policy/parser/engine/   │
                            │  rules/executor/mask/audit    │
                            │  store(SQLite 元数据+审计)    │
                            └───────────┬──────────────────┘
                             pg/mysql wire│连接池
                                PostgreSQL / MySQL
```

# 第 2 章 工程规范（冻结）

## 2.1 技术栈与指定依赖（不许替换，新增需先报批）
> 下列是**允许使用的依赖白名单**；go.mod/go.sum 由 `go mod tidy` 按各任务**实际 import** 自然维护，**不要预先 require 未使用的依赖**（否则 go build 报 "updates to go.mod needed"）。每个任务收尾必须 `go mod tidy` 且 build/vet/test 全绿。
- Go 1.23+，module `github.com/cuipengdba/agentsql`（定名后全局替换）；**go.mod 的 go 指令自 T03 起提升为 `1.23.10`**（vitess v0.21.6 最低要求，仍属 1.23，本机工具链 1.27 可编译）
- MCP：`github.com/modelcontextprotocol/go-sdk`
- PG 解析：`github.com/pganalyze/pg_query_go/v5`（真实内核 parser，零绕过）
- MySQL 解析：**已锁定 `vitess.io/vitess/go/vt/sqlparser v0.21.6`**（T03 选型结论：xwb1989/sqlparser 停留在 2018、无 go.mod/tag、是 Vitess 旧分支，MySQL8 新语法会漏；Vitess 持续维护、有 ParseStrictDDL 严格模式契合 fail-closed、SplitStatements 处理多语句、Apache-2.0，依赖体积大可接受）。用返回 error 的 `sqlparser.New`+`ParseStrictDDL`，**禁用会 panic 的 NewTestParser**；选型 spike 程序不进正式仓库
- PG 驱动 `github.com/jackc/pgx/v5`(pgxpool)；MySQL 驱动 `github.com/go-sql-driver/mysql`
- 元数据库 `modernc.org/sqlite`（纯 Go，免 CGO，保证交叉编译）
- Web：`github.com/gin-gonic/gin`；配置 `gopkg.in/yaml.v3`；日志 `github.com/rs/zerolog`；CLI `github.com/spf13/cobra`
- 测试：testing + `github.com/stretchr/testify` + testcontainers-go（真实 PG16/MySQL8）
- 前端：React18 + TypeScript + Vite + Ant Design 5 + ECharts5 + framer-motion（六段流动画）+ axios + zustand + react-router-dom

## 2.2 目录结构（冻结）
```
agentsql/
├── cmd/agentsql(main.go)  cmd/agentsqlctl(main.go)
├── internal/
│   ├── config/ model/ store/(migrations/) auth/ policy/
│   ├── parser/ engine/ rules/(generic.go postgres.go mysql.go)
│   ├── executor/ mask/ audit/ pipeline/ mcpserver/ adminapi/ server/
├── web/(React)  migrations/  examples/  tests/corpus/
├── Makefile Dockerfile docker-compose.yml README.md
```

## 2.3 编码规范
- error 必须 `fmt.Errorf("...: %w", err)` 包装；禁止 `_` 吞错、禁止 panic 处理预期错误。
- 安全默认 fail-closed；启动期完整校验配置，非法即退出码 1。
- zerolog 结构化日志，连接串/密钥/SQL 中的敏感字面量脱敏后再记。
- 共享状态加锁或用 channel；每数据源独立连接池。
- 导出标识符写 godoc；每条规则注释写明"攻击/误操作场景"。
- 核心包 parser/engine/rules/policy/pipeline 覆盖率 ≥80%，每条规则必须有正例（该拦）与反例（不该拦）。

# 第 3 章 冻结契约

## 3.1 config.yaml
```yaml
server: { http_listen: "127.0.0.1:7780", console_enabled: true }
store:  { sqlite_path: "./data/agentsql.db" }
defaults: { statement_timeout_ms: 5000, row_limit: 1000, max_conns_per_datasource: 5, qps_per_agent: 20 }
theme: { default: "dark" }   # light/dark
```

## 3.2 核心 model（所有任务引用，字段冻结）
```go
package model
type DBDialect string // "postgres" | "mysql"
type StmtType string  // SELECT/INSERT/UPDATE/DELETE/DDL/ADMIN/UNKNOWN
type Decision string  // allow/deny/approve
type RiskLevel int    // 1拒绝 2审批 3告警 4提示

type AST struct {
    Dialect DBDialect; RawSQL, Normalized string; StmtType StmtType
    IsMulti bool; Tables []ObjectRef; Columns []string
    HasWhere, WhereTautology, HasLimit bool; Functions, Operations []string
    Explain *ExplainInfo
}
type ObjectRef struct{ Schema, Table, Alias string }
type ExplainInfo struct{ EstScanRows int64; EstCost float64; UsesIndex, SeqScan bool; Raw string }
type RuleContext struct{ Agent *Agent; Datasource *Datasource; AST *AST; Policy *PolicyDecision }
type RuleHit struct{ RuleID string; Risk RiskLevel; Message, Suggestion string }
type Assessment struct {
    Decision Decision; Risk RiskLevel; StmtType StmtType; Hits []RuleHit
    EstScanRows int64; Reason, Suggestion, Normalized string; Objects []ObjectRef
    StageLatency map[string]int64 // 六段各阶段耗时ms,供前端动画
}
type QueryResult struct{ Columns []string; Rows [][]string; RowCount int; Truncated bool; LatencyMS int64 }
```
Agent/Datasource/Policy/Approval 等存储模型见 3.3。

## 3.3 存储 Schema（store/migrations/0001_init.sql，冻结，照此建表）
SQLite（modernc.org/sqlite），迁移开头执行 `PRAGMA foreign_keys = ON;`。七张表完整 DDL 如下，T02 必须逐字段落地，不得增删改字段：
```sql
CREATE TABLE agents (
  id           TEXT PRIMARY KEY,                 -- ag_xxx
  name         TEXT NOT NULL,
  owner        TEXT,                             -- 责任人（企业版关联员工）
  status       TEXT NOT NULL DEFAULT 'active',   -- active/disabled
  api_key_hash TEXT NOT NULL,                    -- asql_ 密钥的 sha256，只存哈希
  level        TEXT NOT NULL DEFAULT 'readonly', -- readonly/dml/ddl
  expires_at   TIMESTAMP,
  created_at   TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at   TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_agents_keyhash ON agents(api_key_hash);

CREATE TABLE datasources (
  id              TEXT PRIMARY KEY,              -- ds_xxx
  name            TEXT NOT NULL,
  db_type         TEXT NOT NULL,                 -- postgres/mysql
  host            TEXT NOT NULL,
  port            INTEGER NOT NULL,
  database        TEXT NOT NULL,
  username        TEXT NOT NULL,
  password_enc    TEXT NOT NULL,                 -- AES-GCM 密文
  conn_limit      INTEGER DEFAULT 5,
  stmt_timeout_ms INTEGER DEFAULT 5000,
  row_limit       INTEGER DEFAULT 1000,
  created_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE policies (
  id            TEXT PRIMARY KEY,
  agent_id      TEXT NOT NULL REFERENCES agents(id),
  datasource_id TEXT NOT NULL REFERENCES datasources(id),
  object_type   TEXT NOT NULL,                   -- database/schema/table/column
  object_name   TEXT NOT NULL,                   -- 如 public.orders
  columns       TEXT,                            -- 允许列逗号分隔，* 为全部
  row_filter    TEXT,                            -- 行级条件，企业版启用
  action        TEXT NOT NULL,                   -- allow/deny
  created_at    TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at    TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_policies_agent_ds ON policies(agent_id, datasource_id);

CREATE TABLE rules (
  id           TEXT PRIMARY KEY,                 -- R001...
  db_type      TEXT NOT NULL,                    -- postgres/mysql/all
  title        TEXT NOT NULL,
  risk_level   INTEGER NOT NULL,                 -- 1拒绝 2审批 3告警 4提示
  pattern_type TEXT NOT NULL,                    -- ast_match/cost/rate/blacklist
  definition   TEXT NOT NULL,                    -- JSON 规则定义
  enabled      INTEGER DEFAULT 1,                -- 0/1
  builtin      INTEGER DEFAULT 0,
  created_at   TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at   TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE mask_rules (
  id             TEXT PRIMARY KEY,
  datasource_id  TEXT,
  table_name     TEXT NOT NULL,
  column_name    TEXT NOT NULL,
  sensitive_type TEXT NOT NULL,                  -- phone/idcard/bankcard/email
  algo           TEXT NOT NULL,                  -- mask/hash/range/block
  created_at     TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at     TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- 审计日志（核心）：只追加，Repository 层不提供 UPDATE/DELETE 接口
CREATE TABLE audit_logs (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  ts              TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  agent_id        TEXT,
  datasource_id   TEXT,
  session_id      TEXT,                          -- MCP 会话
  conversation_id TEXT,                          -- 上游 AI 会话 ID（透传）
  mcp_tool        TEXT,                          -- query/execute_write/list_schema...
  db_type         TEXT,
  sql_raw         TEXT,                          -- 原始 SQL
  sql_norm        TEXT,                          -- 去字面量归一化
  stmt_type       TEXT,                          -- SELECT/UPDATE/DDL...
  objects         TEXT,                          -- 涉及对象 JSON
  decision        TEXT NOT NULL,                 -- allow/deny/approve/error
  rule_hits       TEXT,                          -- 命中规则 JSON
  risk_level      INTEGER,
  est_rows        INTEGER,                       -- EXPLAIN 预估扫描行
  rows_returned   INTEGER,
  latency_ms      INTEGER,
  client_ip       TEXT,
  model_name      TEXT,
  error_msg       TEXT
);
CREATE INDEX idx_audit_ts ON audit_logs(ts);
CREATE INDEX idx_audit_agent_ts ON audit_logs(agent_id, ts);
CREATE INDEX idx_audit_decision ON audit_logs(decision);

CREATE TABLE approvals (
  id         TEXT PRIMARY KEY,
  audit_id   INTEGER REFERENCES audit_logs(id),
  agent_id   TEXT,
  sql_raw    TEXT,
  reason     TEXT,
  status     TEXT DEFAULT 'pending',             -- pending/approved/rejected/expired
  approver   TEXT,
  decided_at TIMESTAMP,
  created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_approvals_status ON approvals(status);
```
约束：
- 除 audit_logs 仅含 `ts`（只追加、不更新，故无 created_at/updated_at）外，其余各表含 created_at/updated_at；**audit_logs 只追加，Repository 层无 Update/Delete 接口**；
- API Key：`asql_` 前缀，仅存 sha256；数据源密码 AES-GCM 加密，密钥取环境变量 AGENTSQL_SECRET；
- 重复执行迁移必须幂等（CREATE TABLE IF NOT EXISTS / schema_migrations 记录版本）。

## 3.4 MCP Tools 契约（对 AI 暴露，冻结）
| tool | 入参 | 行为 |
|---|---|---|
| list_datasources | {} | 仅返回授权数据源 |
| list_schema | {datasource_id, table?} | 按列权限过滤 |
| explain_query | {datasource_id, sql} | 只 EXPLAIN 不执行 |
| query | {datasource_id, sql} | 仅 SELECT 类，走全流水线，结果脱敏 |
| execute_write | {datasource_id, sql, reason} | DML，默认关闭，需 dml 级别 |
| request_approval | {datasource_id, sql, reason} | 生成审批单 |
| get_approval_result | {approval_id} | 查审批状态 |
> 错误统一返回 `{decision,reason,suggestion}`，suggestion 用中文教 AI 自我改写。**不提供 execute_raw_sql 万能工具。**

## 3.5 管理 REST API（/api/v1，除 login 外 Bearer；统一响应 {code,msg,data}，分页 {total,page,page_size,list}）
POST /auth/login；agents CRUD + /agents/:id/rotate-key；datasources CRUD + /:id/ping；
policies GET/PUT；rules GET/PUT；audit 分页 GET + /audit/export；approvals 列表 + /:id/decide；
GET /dashboard/summary（KPI、14 天趋势、决策占比、风险 Top、Agent 排行、拦截战报文案）。

# 第 4 章 后端任务单 T01-T16

## T01 工程骨架与配置
cobra(version/serve)；config 加载与启动校验（缺 sqlite_path/非法端口/负超时必须退出码1）、自动建目录；model 落地；按 2.2 建空包；Makefile(build/test/vet/fmt/lint/docker-build/cross)、Dockerfile、example 配置；zerolog(LOG_LEVEL/LOG_FORMAT)。
测试：合法配置通过；3 个非法配置启动失败。验收：make build、serve 正常、目录与 2.2 一致。禁止：不写业务。

## T02 SQLite 存储与迁移
go:embed 迁移、schema_migrations 幂等；七表 Repository；AuditLog 仅 Insert+分页；GenerateAPIKey(sha256)；密码 AES-GCM(AGENTSQL_SECRET)。
测试：重复迁移幂等、CRUD 往返、audit 不可改、密钥无明文。

## T03 SQL 解析层 ★
Parser 接口 + NewParser(dialect)；PG 用 pg_query_go、MySQL 用锁定 parser，统一归一成 model.AST（语句类型/表带schema别名/多语句/WHERE及恒真/LIMIT/函数/DDL与管理动作 DROP TABLE/TRUNCATE/VACUUM FULL/GRANT/LOAD_FILE/INTO OUTFILE/Normalized）。解析失败或多语句返回 ErrUnparseable。tests/corpus 先建 40 条（PG20/MySQL20，含注释/大小写/换行/嵌套/恒真/多语句绕过）逐条断言。**禁止正则判型。**
验收（亲自）：`/**/ DROP/**/ TABLE x`、`uPdAte t set a=1 where 1=1 or 'a'='a'` 识别正确；乱码 SQL 返回 error 不 panic。

## T04 规则引擎框架
Rule 接口{ID,Dialect,Level,Enabled,Eval}；Engine 有序执行并聚合：有 RiskDeny→deny，否则 RiskApprove→approve，否则 RiskWarn→allow 带告警，否则 allow；规则开关/阈值读 store.rules，缺省用默认值，可被数据源/Agent 覆盖；输出 Assessment（含 StageLatency）。测试聚合优先级/顺序/覆盖/空集。

**四态语义（竞品对标硬约束 v2.1）**：Decision 固定四值且为一等公民——`deny` 拒绝执行、`approve` 转人工审批（不执行、进审批流）、`warn` 放行但在结果与审计中带告警标记、`allow` 放行；聚合优先级严格 `deny > approve > warn > allow`。市面草根安全 MCP 多为 allow/deny 两级，独立的 `approve`（转审批）是核心差异，禁止把引擎简化成布尔判断。

**层叠覆盖与 fail-closed**：规则解析顺序 全局缺省 ← 数据源级 ← Agent 级（后者覆盖前者同名规则的开关/阈值）；但任意层级的显式 `deny` 不可被任何层级覆盖放行（显式拒绝优先且不可撤销）；规则缺失或解析异常时 fail-closed 返回 error，不得静默放行。

**纯内聚、无副作用**：Engine 只做 `AST + EvalContext → Assessment`，不连数据库、不执行 SQL、不做网络/文件 IO；规则集与阈值由入参传入（读 store 发生在 Engine 之外），保证引擎可纯单测、结果确定、并发安全。

**可解释输出**：Assessment 至少含 最终 Decision、按执行顺序排列的命中明细 Hits（每条含 规则 ID/级别/Decision/message/suggestion）、StageLatency，为 T21 拦截战报与审计提供“为什么被拦、被哪条规则判”的数据，禁止只返回一个最终结论。

**规则与引擎分离**：T04 只实现 Engine、Rule 接口与内存假规则（用于测优先级/覆盖/空集），不得硬编码 R001–R204 业务规则（业务规则在 T05–T07）；空规则集 → allow 且 Hits 为空、Assessment 标注无命中。Rule.Eval 入参用 EvalContext 预留 Agent 级别、数据源、T08 策略决策、MetadataProvider 元数据等槽位，使 T05+ 货架规则（无 WHERE/恒真/堆叠/注释绕过/危险函数/COPY PROGRAM/OUTFILE/大扫描行转审批/限流/行数上限）无需改引擎签名即可挂载。

## T05 通用规则 R001-R010（internal/rules/generic.go）
R001 多语句 deny；R002 UPDATE/DELETE 无 WHERE 或恒真 deny；R003 只读 Agent 非 SELECT deny；R004 EXPLAIN 扫描行超阈值(默认10万) approve；R005 无 LIMIT 大结果 warn+执行层强制 limit；R006 注释注入/堆叠/未知语句 deny；R007 危险函数黑名单(按方言) deny；R008 token-bucket QPS/并发限流 deny；R009 SQL 长度/嵌套层数/UNION 数超阈 approve；R010 越权表 deny。每条≥2 正例 3 反例，message 给 DBA、suggestion 中文教 AI 改。

**R006 注释策略（v2.1 澄清）**：v0.1 对任何含注释的 SQL 一律 deny（parser 把注释标为 Operations 的 COMMENT），优先保证漏拦=0；AI Agent 生成的 SQL 本不应含注释，suggestion 指导其移除。已知保守点：PG/MySQL 优化器 hint（`/*+ ... */`）此时也会被拦，登记到 T23 误拦评估清单；若真实客户确有 hint 需求，再做“放行规范 hint、只拦危险注释模式”的精细化，v0.1 不设注释白名单。R008 限流器由 pipeline 在请求结束时 Release 占用，规则与限流判定都只走结构化 AST，不做 SQL 文本正则匹配（R009 长度除外，按字节计）。

## T06 PG 专项 R101-R107（postgres.go）
R101 DROP DATABASE/TABLE deny；R102 VACUUM FULL/REINDEX approve；R103 pg_terminate_backend/pg_reload_conf/pg_read_file 等 deny；R104 COPY...PROGRAM deny；R105 无索引 UPDATE/DELETE（经 MetadataProvider 接口，先给接口+内存假实现）approve；R106 大表 ALTER（表行数元数据）approve；R107 长事务风险 warn。每条≥4 测试覆盖别名/大小写绕过。

## T06.1 PG parser utility 语句分类补强（internal/parser，插在 T07 前）
**背景**：T03 parser 聚焦 DML 与核心 DDL，一批 PostgreSQL utility 被标成 StmtType=UNKNOWN，进而被 R006 一律 deny，产生过度拦截与信号错配（T06 端到端独立验证发现）。本单只补 parser 的语句分类与规范信号，不改规则判定语义。StmtType 仍只用 SELECT/INSERT/UPDATE/DELETE/DDL/ADMIN/UNKNOWN 七类。

必须修正的行为：
1. `CLUSTER [表 [USING 索引]]` → ADMIN、Operations 含 CLUSTER，使 R102 能 approve（不再被 R006 以 UNKNOWN deny 盖过）；
2. `DROP DATABASE` → DDL、Operations 含 "DROP DATABASE"（当前是单 token DROPDB），使 R101 精准命中；`CREATE DATABASE` → DDL；
3. `EXPLAIN` 按内层语句归类：`EXPLAIN SELECT...` 视为只读（StmtType=SELECT，不得 UNKNOWN）；`EXPLAIN ANALYZE` 在 Operations 标注其会真实执行，内层是写/DDL 时按内层类型归；
4. **文本注释信号与 COMMENT ON 语句解耦**：SQL 文本中的 `/* */`、`--` 注释信号改名为 `SQL_COMMENT`（R006 改为匹配 SQL_COMMENT，同步更新 generic.go R006 与 T05 相关测试）；`COMMENT ON ... IS ...` → DDL、Operations="COMMENT ON"，不再被当作文本注释拦截。

其余当前被标 UNKNOWN 的 utility 一次性补全：DDL 类＝CREATE/DROP/ALTER DATABASE、CREATE EXTENSION、ALTER SEQUENCE、REFRESH MATERIALIZED VIEW；ADMIN 类＝ALTER SYSTEM、ALTER ROLE、CREATE/DROP ROLE、CHECKPOINT、LOCK TABLE、LOAD（标高危，供后续规则）、DISCARD、PREPARE/EXECUTE/DEALLOCATE、CALL、NOTIFY/LISTEN/UNLISTEN。

已正确分类的保持不动：DROP SCHEMA/SEQUENCE/FUNCTION/TRIGGER/TYPE/MATVIEW、CREATE SCHEMA/INDEX/SEQUENCE/FUNCTION/TRIGGER、ALTER INDEX、SET/RESET/GRANT/REVOKE、TRUNCATE、BEGIN/COMMIT/ROLLBACK/SAVEPOINT、SELECT INTO。

**回归铁律**：T03 全部 corpus、T05/T06 全部测试必须仍全绿；每个新增映射≥1 条测试；不新增依赖、不动六段流水线其它段。
**本机验收**：CLUSTER→R102 approve；DROP DATABASE→R101 精准 deny；EXPLAIN SELECT→不被 R006 拦且正常 allow；COMMENT ON TABLE→DDL 不被当注释拦，而含 `/* */` 的 SQL 仍被 R006 拦；补分类语句不再 UNKNOWN；T03/T05/T06 测试零回归。

## T07 MySQL 专项 R201-R204（internal/rules/mysql.go）
对接 T05 引擎骨架（实现 engine.Rule 接口、Dialect()=model.DBDialect("mysql")、复用 generic.go 的 helper、加编译期断言），只对 MySQL AST 生效（引擎按方言过滤，PG AST 不调用）。下列 parser 信号已经主控端用 vitess 实测确认，按此匹配，不要凭空假设。

**R201 文件读写 deny（信号在 ast.Operations）**：SELECT 含 LOAD_FILE()（Operations 含 LOAD_FILE）；SELECT ... INTO OUTFILE（含 INTO OUTFILE）/ INTO DUMPFILE（含 INTO DUMPFILE）；LOAD DATA [LOCAL] INFILE（StmtType=ADMIN、Operations=[LOAD]）。命中其一即 deny，防任意文件读写与服务端落盘。

**R202 危险写 approve（用 ast.Tables 长度与 ast.HasLimit，均已由 parser 填充）**：DELETE/UPDATE 且 len(ast.Tables)>=2（多表/JOIN 写）→ approve；DELETE/UPDATE 且 !ast.HasLimit（无 LIMIT 的批量写）→ approve；单表且带 LIMIT 不命中、放行交后续规则。判型只走 AST 不用正则。注意与 PG R105（无索引）判据不同、勿混用；主键点更新无 LIMIT 也转人工属 v0.1 安全优先，误拦登记 T23。

**R203 高危管理命令 deny（注意 vitess 可解析边界，已实测）**：能解析、会到规则层、应 deny 的＝Operations 为 FLUSH、KILL、PURGE（PURGE BINARY LOGS）；SET GLOBAL 仅当 parser 能区分 GLOBAL/SESSION 时对 GLOBAL deny、SESSION 放行，区分不了则本单不拦 SET 并登记 T23，不得硬猜。vitess 在 Parse 阶段即报错、到不了规则层的＝RESET *、GRANT、REVOKE、SHUTDOWN、CREATE/DROP USER、SET PASSWORD、ALTER INSTANCE、HANDLER，这些由第一段 Parse fail-closed 天然拒绝，R203 不写匹配不到的死代码，但要补测试证明这些语句在 parser 层即 error。USE、SET SESSION、LOCK TABLES/UNLOCK TABLES 不拦（业务可能用、非破坏）。

**R204 大事务 approve（v0.1 立骨架，不硬编码拿不到的信息）**：parser 只暴露事务边界（BEGIN/START TRANSACTION→Operations=BEGIN，COMMIT/ROLLBACK），网关逐语句无状态、单条 AST 无法得知事务规模。v0.1 定义阈值键与事务元数据扩展接口（事务年龄/累计影响行数，思路对齐 R107 的 transactionMetadataProvider）：能拿到元数据且超阈值→approve；拿不到→不命中放行（不误拦），注释与交付说明标注"完整大事务判定依赖 T10 会话/连接元数据"。

**统一约束**：每条规则 Dialect 固定 mysql + 编译期断言；复用 generic.go 的 requiredAST/isStatement/containsFold/denyResult/approveResult/warnResult/allowResult/normalizedDialect 等 helper；无 regexp/IO/panic；不新增依赖、不改 parser 与既有规则；每条规则正反例测试（用上述真实语句），并保证 T03/T05/T06/T06.1 全部测试零回归。

## T08 权限策略引擎（internal/policy + store 补查询）
**边界**：表级越权判定已由 R010 实现（消费 PolicyDecision.AllowedTables/DeniedTables），本单不重写表级；policy 包负责"把多条存储策略记录合并成 PolicyDecision"和"R010 未覆盖的列级授权"。不连业务数据库、不碰 executor，也不在本单挂载引擎（统一在 T13 pipeline 编排）。

1) store 补 `ListByAgentAndDatasource(ctx, agentID, datasourceID) ([]model.Policy, error)`，走索引 idx_policies_agent_ds、按 created_at 稳定排序，并补仓储测试。
2) 新建 internal/policy：Resolver 把 []model.Policy（object_type∈database/schema/table/column、action∈allow/deny、object_name、columns）合并为 *model.PolicyDecision：
   - action=deny 的 table/schema/database 对象进 DeniedTables，action=allow 进 AllowedTables；保留 schema.table 与 `*`/`schema.*` 通配，且通配语义必须与 R010 的 matchesAnyPolicyObject 一致（抽公共匹配函数复用，不允许两套通配）；
   - object_type=column：ColumnACL[object_name]=该对象授权列集合（解析 columns、去重保序，标识符大小写按 SPEC 规范）；
   - deny 优先于 allow（同对象冲突进 Denied）；
   - **默认拒绝**：该 agent+数据源无任何策略记录时，返回字段为空但非 nil 的 PolicyDecision（R010 据此对所有表拒绝），绝不默认放行；
   - Level 不由本包猜测：由 Resolve 入参显式传入（readonly/dml/ddl，调用方 T09/T13 从 Agent 记录带入）原样写入 PolicyDecision.Level，非法 Level 返回 error。
3) 列级授权 `AuthorizeColumns(ast, decision)`：仅对 SELECT，逐列核对 ast.Columns 是否落在该表 ColumnACL；表级被 `*`/`schema.*` 全量授权、或该表 ColumnACL 含 `*` 时全列放行；返回未授权列供调用方 deny（message 点名 表.列）。另提供 `FilterColumnsForSchema(columns, decision)` 供 T14 list_schema 隐藏未授权列（只减不增）。某表无列级策略时按"表已授权即列不额外限制"处理，避免与表级白名单双重误伤，口径写进注释。
4) policy 核心为纯函数、无正则判型；store 访问从 Resolver 外层注入，使合并/列级逻辑可离线单测；不新增依赖。
**测试**：白名单命中与越权表、deny 黑名单优先、`*`/`schema.*` 通配、列级允许/未授权列 deny、list_schema 列裁剪、Level 透传与非法 Level error、空策略=全拒绝、多记录合并去重保序；并保证 T02/T05/T06/T06.1/T07 全部既有测试零回归。

## T09 API Key 认证（internal/auth + store 补查询）
**已有基础（复用，不重造）**：store/keys.go 的 GenerateAPIKey 已用 crypto/rand 32B + base64.RawURL、asql_ 前缀、sha256 hex 产出（明文,哈希）；agents 表有 api_key_hash、索引 idx_agents_keyhash、status(默认 active)、level、expires_at；model.Agent 字段齐备。数据源密码的 AES-GCM 与本单无关，不要混入。

1) store：
   - 从 GenerateAPIKey 抽出导出函数 `HashAPIKey(plaintext string) string`（sha256→小写 hex），GenerateAPIKey 内部改为调用它，保证哈希口径单一事实源；导出 APIKeyPrefix 常量（"asql_"）供前缀校验；validateAPIKeyHash 不变；补测试证明 HashAPIKey(GenerateAPIKey 明文) == GenerateAPIKey 返回的 hash。
   - AgentRepository 补 `GetByAPIKeyHash(ctx, hash) (model.Agent, error)`，走 idx_agents_keyhash 精确查询、复用 scanAgent；查无记录返回可 errors.Is 判定的哨兵 ErrAgentNotFound；补仓储测试。
2) 新建 internal/auth：定义最小读接口 AgentKeyReader（只含 GetByAPIKeyHash），*store.AgentRepository 天然实现，便于离线单测、auth 不直接写库。Authenticator.Authenticate(ctx, rawKey) (model.Agent, error) 严格按序：
   - rawKey 为空、或前缀不是 asql_ → 哨兵 ErrInvalidCredentials（fail-closed，不查库）；
   - actual=HashAPIKey(rawKey)，经 Reader 取记录；查询报错或查无 → 一律 ErrInvalidCredentials（对外不区分"不存在/密钥错"，防账号枚举）；
   - 用 crypto/hmac.Equal 在库内 expected 与 actual 之间做常量时间比对，不等 → ErrInvalidCredentials；
   - status != "active" → ErrAgentDisabled；expires_at 非 nil 且 now 到达/超过 → ErrKeyExpired；
   - 全部通过才返回该 Agent。now 用可注入时钟（默认 time.Now）以便过期用例固定时间。
3) fail-closed 铁律：依赖错误、空 status、任何异常都拒绝，绝不"查不到就匿名放行"。本单不签发 key（签发属管理侧 GenerateAPIKey）；轮换=Update 为新 hash，旧 hash 自然查不到而失效，补一条"旧 key 失效/新 key 通过"测试即可，不做专门轮换接口。
4) 不连业务数据库、不碰 HTTP/MCP 传输（请求头提取归 T13/T16，本单入参就是已提取的 rawKey 字符串）；不新增第三方依赖（仅标准库 crypto/hmac、crypto/sha256）。
**测试**：正确 key 返回 Agent；错误 key/空/错前缀/查无 全部 ErrInvalidCredentials；status 非 active→ErrAgentDisabled；未过期通过、已过期与恰好到期→ErrKeyExpired；覆盖 hmac 常量时间比对路径；轮换后旧 key 失败、新 key 通过；并保证 T02/T05/T06/T06.1/T07/T08 既有全部测试零回归。

## T10 受控执行器（executor）★首个连真实库（重点单）

### 目标与边界
新增 `internal/executor` 包，负责"受控地连接并执行**单条已通过决策**的 SQL"，返回 `model.QueryResult` / `model.ExplainInfo`。**不做** Parse/规则/决策（属 engine，T13 才编排）、**不做** MCP/HTTP、**不做**脱敏（T11）。deny 在任何情况下都不触达 executor（T13 保证），executor 自身再做只读防御（纵深防御）。
唯一允许的既有改动：把 rules 包 PG 的**非导出**接口 `transactionMetadataProvider` 导出为 `TransactionMetadataProvider`（postgres.go 内全部引用、postgres_test.go 的编译断言同步改名），**R101–R107 判定逻辑与既有测试一行不改、零回归**（MySQL 侧 `MysqlTransactionMetadataProvider` 已导出，无需动）。

### 统一抽象（executor/executor.go）
```go
type Executor interface {
    Dialect() string
    Ping(ctx context.Context) error
    Explain(ctx context.Context, sql string) (model.ExplainInfo, error)
    Query(ctx context.Context, sql string, rowLimit int) (model.QueryResult, error) // 只读 SELECT 类，列值统一转字符串
    Execute(ctx context.Context, sql string) (model.QueryResult, error)             // 写/DDL，RowCount=影响行数
    Close() error
}
```
`Manager`：`map[dsID]Executor`，**每数据源独立连接池**；`GetOrOpen(ds model.Datasource, secret []byte)(Executor,error)` 复用已开池、`sync.RWMutex` 并发安全、`Close(id)/CloseAll()`；ds.PasswordEnc 用既有 store 解密函数（AGENTSQL_SECRET）拿明文，**不另造加解密、错误与日志绝不带密码/DSN 凭据**。

### PostgreSQL 实现（pgx/v5 pgxpool，版本 v5.7.6）
- `pgxpool.New`：`MaxConns=ds.ConnLimit`（默认 5）；`ConnConfig.RuntimeParams` 设 `statement_timeout=<毫秒>`（默认 ds.StmtTimeoutMS，缺省 5000）；只读连接再设 `default_transaction_read_only=on`；建连后 Ping。
- **超时三层**：连接级 `statement_timeout`（服务端中断，SQLSTATE `57014`）＋ `context.WithTimeout(StmtTimeoutMS+余量)` 兜底，识别为 `ErrQueryTimeout`。
- **只读**：只读连接写操作由服务端报 SQLSTATE `25006`；executor 在 Query/Execute 入口再判"只读实例只走 Query"，双保险。
- **行数截断（不改写 SQL、不下推 LIMIT，避免改变语义、不与 R005 重复）**：遍历 Rows 最多读 `rowLimit+1` 行，读到第 rowLimit+1 行即置 `Truncated=true` 并停止；Columns 取 FieldDescriptions.Name。
- **Explain**：发 `EXPLAIN (FORMAT JSON) `+sql，得单行 JSON 文本，解析为 `[]struct{ Plan struct{ NodeType string \`json:"Node Type"\`; PlanRows float64 \`json:"Plan Rows"\`; TotalCost float64 \`json:"Total Cost"\`; IndexName string \`json:"Index Name"\` } }`，取 `[0].Plan` 根节点：`EstScanRows=int64(PlanRows)`、`EstCost=TotalCost`、`SeqScan = NodeType=="Seq Scan"`、`UsesIndex = IndexName!="" || NodeType 含 Index/Bitmap`；Raw 存原始 JSON；**解析失败返回 error（fail-closed 交决策层，不猜）**。把解析抽成纯函数 `parsePostgresExplainJSON([]byte)` 便于喂样本单测。
- **真实 MetadataProvider（实现导出后的 rules.TransactionMetadataProvider）**：
  - `TableHasIndex`：查 pg_index/pg_class/pg_namespace，存在 `indisvalid` 索引即 true；
  - `TableRowCount`：`reltuples::bigint`（规划器估算，与 R106 口径一致）；
  - `TransactionState()`：`pg_is_in_transaction()`＋`now()-xact_start`/`now()-state_change` 算 AgeMS/IdleMS；不在事务返回 InTransaction=false。

### MySQL 实现（database/sql + go-sql-driver/mysql v1.9.3）
- DSN 必须带 `parseTime=true&multiStatements=false`（**禁多语句，纵深防堆叠注入**，写断言守护）；`SetMaxOpenConns=ConnLimit`、`SetConnMaxLifetime`。
- **超时**：仅 SELECT 在首个 SELECT 后注入 optimizer hint `/*+ MAX_EXECUTION_TIME(n) */`（n 毫秒、幂等不重复注入、抽纯函数单测）；非 SELECT 不支持该 hint，**所有语句统一 context.WithTimeout 兜底**，识别 ErrQueryTimeout。
- **只读**：MySQL 普通账号无服务端只读事务开关（read_only 需高权限，不用），由 executor 入口防御（只读实例遇非 SELECT 返回 ErrReadOnlyViolated），与规则层 R003/R201 双保险。
- **行数截断**：同 PG 读 N+1 判 Truncated；Columns 取 `rows.Columns()`，值统一字符串化。
- **Explain**：发 `EXPLAIN `+sql 得结果集，列名大小写不敏感读 `type/key/rows`：多表 EstScanRows=各行 rows 之和（保守）；任一表 `type=ALL` 则 SeqScan=true；所有表 key 均 NULL/空 则 UsesIndex=false，否则 true；Raw 拼成可读文本。抽纯函数 `parseMysqlExplainRows(columns, values)` 单测。
- **真实 MysqlTransactionMetadataProvider**：表行数/索引查 information_schema.tables.table_rows 与 statistics；事务态查会话状态/innodb_trx 算 AgeMS、AffectedRows；**取不到或不确定按 R204 既有"缺元数据 allow 不误拦"口径返回空快照，不因此阻断**。

### 错误哨兵（executor/errors.go）
`ErrQueryTimeout`、`ErrReadOnlyViolated`、`ErrDatasourceUnreachable`，全部支持 `errors.Is`；错误信息脱敏（不含凭据）。

### 依赖白名单（锁定本机缓存已有版本，禁止超范围）
`jackc/pgx/v5 v5.7.6`、`go-sql-driver/mysql v1.9.3`、`testcontainers-go v0.37.0` 及其 nested modules `modules/postgres`、`modules/mysql`（nested module 需各自 require）。**testcontainers 只允许出现在 `*_test.go`，生产代码禁止 import；除上述不新增任何第三方库。**

### 测试（硬要求：无 Docker 必须 t.Skip，绝不 t.Fatal）
- **纯单测（不连库）**：Manager 并发开池/复用/关闭与池隔离；行数截断 N+1（可控行源）；PG EXPLAIN JSON 解析（SeqScan/Index/畸形报错各一样本）；MySQL EXPLAIN 解析（ALL 无 key vs 走索引）；MAX_EXECUTION_TIME 只注入 SELECT 且幂等；DSN 断言含 multiStatements=false；只读防御；哨兵 errors.Is。
- **E2E（executor_e2e_test.go，开头 helper 探测 Docker daemon，不可用/拉镜像失败即 `t.Skip("docker unavailable")` 打印原因）**：PG16 与 MySQL8 各四场景——①超时真中断（pg_sleep/SLEEP + 极小超时）②读超 rowLimit 截断 Truncated ③只读连接写被拒 ④无索引=全表扫描、建索引后 UsesIndex。
- 单位无 Docker、不编译不运行：Codex 只做文本级静态自审，E2E 写全并注明"未本地运行，需主控端 Docker 实跑"，禁止在 PowerShell 跑 go/docker 命令。

### 主控端验收（Docker 就绪后）
build/vet/非容器 test 全绿；启动 Docker 后 E2E 真正跑通 PG16/MySQL8 各四场景；独立验证两数据源连接池互不串、超时真中断、截断、只读写拒、EXPLAIN 全表/索引识别、真实元数据喂给 R105/R106/R107/R204 判定正确。主控端会提前 `docker pull postgres:16 mysql:8` 预热镜像。

## T11 脱敏引擎（mask）
Redactor 接口+配置实现；v0.1 实现手机号/邮箱 mask 并贯通"结果返回前脱敏"（按结果列名匹配，别名/表达式也生效）；身份证/银行卡/hash/区间留接口 TODO。测试正反例。

## T12 审计（audit）
响应前同步落库（落库失败则请求失败）；allow/deny/approve 都记；多条件分页 + JSONL 导出。测试三类记录、审计失败主请求失败、筛选导出。

## T13 六段式流水线 ★（pipeline）
编排 Parse→Auth→Policy→Engine(含 Explain)→Decision→Execute→Redact→Audit，记录每阶段耗时到 StageLatency；deny 不连库直接结构化拒绝并审计；approve(v0.1) 不执行、写 approvals+audit 返回单号；allow 执行→脱敏→审计→返回；全链路 context 超时。testcontainers 6 条 E2E：正常/无where更新拒/越权表拒/超扫描行审批/只读写拒/结果脱敏。
验收（亲自）：deny 路径在任何情况下都不触达 executor。

## T14 MCP stdio（mcpserver）
官方 go-sdk 暴露 7 tools，handler 全走 pipeline，list_schema 按列权限过滤；examples 给 Cursor/Claude Desktop 配置。用 Inspector 调通并在真实 Cursor 录屏。

## T15 MCP Streamable HTTP
/mcp 与 adminapi 共用端口；Bearer Key；会话隔离与限流。测试端到端、401、50 并发不串数据。

## T16 管理 REST API（adminapi）
实现 3.5 全部端点；httptest 覆盖 200/401/400；dashboard.summary 返回前端所需全部聚合（含拦截战报统计）。

# 第 5 章 前端与界面（T17-T22，演示驱动设计）

## 5.1 设计语言（全局统一）
- 双主题：**深色作战主题为默认与门面**（背景 #0d1421/#142033/#1c2b42，描边 #2a3b55），浅色用于长时间配置（#f5f7fa）。
- 品牌青蓝 #1677ff(浅)/#3b9eff(深)；语义：放行 #52c41a、告警 #faad14、审批 #fa8c16、拦截 #f5222d。
- 中文思源黑体/系统字体；SQL 与数字一律等宽（JetBrains Mono），数字 tabular-nums。
- 专业、克制、高信息密度，不做 3D/彩虹色；所有列表具备空态/加载骨架/错误重试；SQL 语法高亮+一键复制。
- 统一封装组件：PageContainer、StatCard、DecisionTag、RiskTag、SQLBlock、FilterBar、EmptyState、StageFlow(六段流)。

## T17 前端骨架 + 双主题
Vite+React18+TS+AntD5+ECharts+framer-motion+axios+zustand+router；登录页（深色居中+slogan+subtle 网格背景，禁用 AntD 默认页）；左侧深色导航+顶栏布局与路由；axios 拦截器带 token/统一报错；主题变量与切换器（dark/light，持久化）；web/dist 由后端 go:embed。验收 npm run build、登录进空总览、刷新不掉线。

## T18 总览大屏（深色作战中心，门面，精做）
四区：①顶部 5 张 KPI（总请求/拦截(红)/待审批/活跃 Agent/在线数据源，数字滚动+环比）；②左 2/3 近14天请求柱+拦截红线双轴 ECharts，右 1/3 决策占比环图；③实时风险事件流（30s 轮询，新事件顶部滑入，拦截红色高亮 2s）；④拦截战报卡片（"已拦截 N 次，避免约 X 万行风险"，数据来自 summary）+ Agent 被拦排行 + 高危 SQL 类型 Top5。控件：时间范围、自动刷新开关、主题切换、全屏。空/载/错三态齐全。

## T19 六段安检流组件 StageFlow（灵魂，先做单条播放版）
横向六节点 Parse→Auth→Guard→Decide→Execute→Audit，发光点沿连线移动，逐节点亮起显示结论与耗时（数据来自 Assessment.StageLatency/Hits）；放行最终变绿显示行数，拦截在问题节点变红、流动中断、轻微震动并弹出判词与 suggestion。SVG/framer-motion 轻量实现，不引重型库；导出为可复用组件，供 T20 详情与 T21 演示台调用。v0.1 不做全局 WebSocket 流（T27 后置）。

## T20 审计页 + 证据链时间线（门面，精做）
多条件筛选栏（时间/Agent/数据源/决策/风险/语句类型/对象/关键词，可折叠）+ 虚拟滚动表格（时间|Agent|数据源|类型|决策Tag|风险|SQL摘要|预估行|行数|耗时|会话）；右侧详情抽屉按"调查报告"排：基本信息、StageFlow 时间线（复用 T19）、SQL 原文/归一化双栏高亮、命中规则判词卡片、EXPLAIN 可视化（扫描行/索引/成本徽标）、脱敏样例；导出 JSONL；PDF 合规报告按钮先占位(T27)。验收灌入 1 万行流畅、字段完整、可导出。

## T21 拦截演示台 Playground（讲故事专用）
SQL 输入框 + 6 个剧本按钮（正常查询/无WHERE全表更新/越权查薪资/全表扫描风险/敏感字段脱敏/危险函数）；点"模拟 AI 请求"后用 StageFlow 播放逐段判定（调用 explain/校验接口，**不触达真实生产数据**），拦截时展示判词与改写建议。纯前端+现有校验接口，演示零风险。

## T22 四个配置页（规范高效）
Agent 管理（列表+步骤条新建向导：级别→数据源→表权限→生成 Key，Key 明文仅展示一次做成凭证卡片；禁用/轮换/删除二次确认）；数据源（表单+测试连接，删除需输名称）；权限矩阵（Agent×表：查/写/DDL 开关+列勾选+deny 列表，提交前 diff 预览）；规则配置（按通用/PG/MySQL 分组，开关+阈值抽屉+"防什么"说明+恢复默认）。验收与后端联调全通、Key 轮换即时生效。

# 第 6 章 收尾任务单 T23-T25

## T23 绕过语料回归与 fuzz ★
tests/corpus 扩到 ≥200 条(50危险/50慢查询风险/100正常)，批量回归出命中率/误拦率报表：**危险漏拦=0、误拦率<2%**；fuzz 随机畸形 SQL 喂 parser 跑 30 分钟不 panic、全 fail-closed。亲自补刁钻 SQL 考它。不达标不许开源。

## T24 打包部署
go:embed 内嵌 web/dist；交叉编译 linux amd64/arm64、darwin 单二进制；多阶段 Dockerfile + docker-compose(含示例 PG/MySQL)；systemd unit；agentsqlctl init。验收干净环境 compose up 后 5 分钟走完"加数据源→建 Agent→Cursor 连上→看到审计"。

## T25 可观测性
/metrics（请求总量、decision 分布、规则命中、耗时直方图、限流数、连接池）、/healthz、/readyz；examples 给 Grafana dashboard JSON。验收 Prometheus 可抓；只读转发 ≥1000 QPS、网关 P99 额外开销 <5ms。

# 第 7 章 v0.1 总验收（开源前全绿）
- go test 核心包覆盖率 ≥80%；真实 PG14/16/18 与 MySQL8 E2E 通过；
- 200 条语料危险漏拦 0、误拦 <2%、fuzz 无 panic；
- Cursor/Claude 各录屏：只读成功/越权拒/无WHERE更新拒/审计可查；
- 控制台 6 类页面（总览/审计/演示台/Agent/数据源/权限/规则）全部联调并打进单二进制；
- 干净环境 5 分钟跑通；性能达标。

# 第 8 章 后置任务（开源后，v0.2，不在 v0.1）
- T26 在线 Live Demo：演示只读账号、30 天假数据种子、每日重置、演示横幅、6 剧本引导；
- T27 大屏全局 WebSocket 实时流 + 审计 PDF 合规报告（等保话术）+ 行级权限/完整脱敏/SSO/国产库（企业版）。

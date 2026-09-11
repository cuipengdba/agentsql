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

> **T10 主控端真实库验收结论（2026-09-09）**：受控执行主干（连接池、语句超时、只读强制、N+1 行截断、EXPLAIN、表/索引元数据、错误脱敏、MySQL 多语句拦截）在真实 PG16/MySQL8 全部通过（E2E 8/8），已随 T10 合入（commit f44ab8a / tag t10）。**唯独"事务态检测"真实库下不合格，单列 T10.1 修复后才允许 T13 接线 R107/R204。** 验收另修两处测试夹具（已合入）：①无 Docker 时 testcontainers `NewDockerClient()` 在 Windows 直接 panic（rootless not supported），helper 必须 `recover` 后按不可用 Skip；②MySQL 超时禁用 `SLEEP()`（被 MAX_EXECUTION_TIME 中断时 SLEEP 返回 1、整条不报错），改用优化器无法用行数乘积化简的三表逐行聚合，稳定触发 3024。

## T10.1 事务态检测修复：会话-绑定连接 + 执行器权威事务状态机（重点小单，T13 前置）

### T10 事务态为何不合格（真实库实测，勿再走"查系统视图猜事务"老路）
- **缺陷 A（根本，PG/MySQL 共有）：连接池无会话绑定。** T10 的 Executor 每次 Query/Execute/事务态查询各自从池借还物理连接。AI 会话的 `BEGIN; SQL; …; COMMIT` 会散到不同连接，连接归还时未提交事务还会被池回滚/重置；事务态探测落在另一条空闲连接上，必然失真。实测 MySQL：`Execute("BEGIN")`→`Query(触 InnoDB 表)`→`MysqlTransactionState()` 仍 `InTransaction=false`（探测换了连接）。
- **缺陷 B（PG 判定 SQL 在 pgx 扩展协议下误报）。** T10 用 `xact_start < query_start` 区分显式/隐式事务；但 pgx 走 extended protocol，隐式探测语句的 `xact_start` 在 Parse/Bind 阶段建立、早于 Execute 阶段记录的 `query_start`（实测空闲语句 xact_start 比 query_start 早约 0.6ms、`state=active`），导致空闲/COMMIT 后**恒判 InTransaction=true**。`pg_is_in_transaction()` 是 PG17 才有、PG16 无此函数，不能依赖。
- **MySQL 能力边界（实测）**：`information_schema.innodb_trx` 只登记已访问 InnoDB 表的事务；纯 `BEGIN` 空转或只 `SELECT 常量` 不登记（漏判），访问真实 InnoDB 表/写事务才可见；`@@session.autocommit` 在 `BEGIN` 后值并不变（只有显式 `SET autocommit=0` 才变 0）。单靠系统视图无法可靠判事务。

### 目标与边界
把"事务是否进行中、持续多久、是否空闲、累计改了多少行"的**权威来源从"查数据库系统视图"改为"执行器在绑定连接上自己维护的事务状态机"**；系统视图仅作可选交叉校验，不再决定 `InTransaction`。**只动 `internal/executor` 包（可新增文件），不改 parser/rules/engine/policy/auth/store/model 的既有对外契约与已绿测试；不新增第三方依赖（pgxpool.Conn、database/sql 的 sql.Conn 均为现有驱动自带）。** 不提前做 T13 的 Agent 编排，只提供"会话绑定 + 可靠事务态"的能力与完整测试。

### 1) 会话-绑定连接（Session-bound connection）
- Executor 新增会话能力（在现有无状态 Query/Execute 之外**增量**提供，无状态接口保留给自动提交场景）：
  - `OpenSession(ctx, sessionID string) (Session, error)`：从该数据源池里**借一条物理连接并独占绑定**到 sessionID；PG 用 `pool.Acquire` 持有 `*pgxpool.Conn`，MySQL 用 `db.Conn(ctx)` 持有 `*sql.Conn`；同一 sessionID 重复 Open 返回同一绑定（或显式报错，二选一并写测试），不同 sessionID 绑定不同物理连接。
  - `Session` 接口：`Query/Execute/Explain`（签名与 Executor 对齐，但**全程走这同一条绑定连接**）、`TransactionState()/MysqlTransactionState()`（返回状态机结果）、`Close() error`。
  - `Close()`：**若状态机显示仍在事务中，先在该连接上 ROLLBACK 再归还池**（PG `conn.Release`、MySQL `conn.Close` 还池），杜绝带事务连接回池污染下一会话。
- 抽取一个内部"单连接执行内核"（PG 基于已 Acquire 的 Conn、MySQL 基于已取出的 sql.Conn），让"池化无状态路径"与"绑定会话路径"复用同一套超时/只读/截断/EXPLAIN/脱敏逻辑，**禁止复制两份**。
- Manager 增加 `map[sessionID]Session`（或在 Executor 内），`sync.RWMutex` 保护；`CloseAll` 一并释放所有会话。

### 2) 执行器权威事务状态机（内存态，PG/MySQL 一致）
- 每个绑定 Session 维护：`inTransaction bool`、`startedAt time.Time`、`lastStmtEnd time.Time`、`affectedRows int64`、`mu sync.RWMutex`（Session 可能并发查询，加锁）。
- **每条经 Session 执行的 SQL，执行前先做"事务控制语句分类"**：优先复用 T03 parser 已暴露的语句/操作类型（若 parser 已能区分 BEGIN/COMMIT/ROLLBACK 等则直接用，**以新增导出 helper 方式取用，不改 parser 既有 AST/Evaluate 契约**）；若 parser 覆盖不全，在 executor 内对**封闭关键字集合**做规范化识别（去注释/空白/大小写、允许 `WORK/CHAIN` 等修饰）：
  - 开启：`BEGIN`/`START TRANSACTION`/MySQL `SET autocommit=0`/`SET @@session.autocommit=0` → `inTransaction=true, startedAt=now, lastStmtEnd=now, affectedRows=0`（重复 BEGIN 幂等，不重置 startedAt）。
  - 结束：`COMMIT`/`ROLLBACK`/`END`/`ABORT`/MySQL `SET autocommit=1` → `inTransaction=false`，清零时间与计数。
  - `SAVEPOINT/RELEASE/ROLLBACK TO/SET TRANSACTION` 等事务内语句：不改 inTransaction，仅透传执行。
  - 写语句（INSERT/UPDATE/DELETE/MERGE，经 parser 分类）在 `inTransaction` 时：用执行返回的 RowsAffected **累加 affectedRows**（不再依赖 innodb_trx.trx_rows_modified）。
  - 每条语句结束（无论读写）刷新 `lastStmtEnd=now`。
- `TransactionState()`/`MysqlTransactionState()` 直接读状态机：
  - `InTransaction=inTransaction`；
  - `AgeMS = inTransaction ? now-startedAt : 0`；
  - PG `IdleMS = inTransaction ? now-lastStmtEnd : 0`（对应 R107 空闲事务）；MySQL `AffectedRows=affectedRows`（对应 R204 大事务）。
  - 返回既有 `rules.TransactionState`/`rules.MysqlTransactionState` 结构，**不改 rules 包结构定义**。
- **系统视图降级为可选交叉校验**：保留用 pg_stat_activity / innodb_trx 取"非经本网关开启的外部遗留事务"的辅助方法（命名如 `snapshotServerTransaction`），但**不参与** `InTransaction` 主判定；取不到按"缺元数据不阻断"返回，不报错。T10 那段 `xact_start<query_start`/`@@autocommit OR EXISTS(innodb_trx)` 不再作为权威依据。

### 3) 测试（硬要求）
- **纯单测（不连库）**：用可控假连接/假执行内核驱动状态机，覆盖状态转移全表——空闲 false；BEGIN→true 且 AgeMS 随 sleep 增长；事务内多次 UPDATE 的 affectedRows 正确累加；COMMIT/ROLLBACK→false 且清零；重复 BEGIN 幂等；SAVEPOINT 不改变状态；SET autocommit=0/1（MySQL）开关；Close 时仍在事务触发 ROLLBACK（用 spy 连接断言确实发了 ROLLBACK 且连接被归还）；事务控制关键字识别的大小写/注释/修饰变体。
- **真实 E2E（沿用 T10 已修好的 Docker 探测 helper，无 Docker 必须 t.Skip、panic 也兜底 Skip）**：PG16 与 MySQL8 各覆盖——①OpenSession 后空闲 InTransaction=false（**回归缺陷 B：PG 不得再恒 true**）②Session 内 BEGIN→访问真实表→TransactionState.InTransaction=true、AgeMS≥指定 sleep ③事务内 UPDATE 后 AffectedRows 正确 ④COMMIT/ROLLBACK 后 false ⑤BEGIN+UPDATE+sleep 后 IdleMS>0 ⑥Close 于事务中，再开新 Session 查不到任何遗留事务/数据已回滚 ⑦**两个并发 Session 绑定不同连接、各自 BEGIN/UPDATE，事务状态与数据互不串**（回归缺陷 A）。
- 既有 T10 全部单测/E2E 保持绿，零回归；覆盖率不低于 executor 现有水平。
- 单位无 Docker：Codex 只做文本级静态自审并注明"未本地编译/运行，需主控端验证"，禁止在 PowerShell 跑 go/docker。

### 4) 主控端验收
build/vet/全包 test 绿、零回归；真实 PG16/MySQL8 跑通上述 E2E 全矩阵，重点复现并确认缺陷 A/B 不再出现（PG 空闲不再恒 true、MySQL 绑定会话内 BEGIN 后稳定 true、并发会话不串）；独立 diag 验证 Close 回滚清理。**T10.1 通过后 R107/R204 的事务元数据才算可信，T13 方可接线。**

## T11 脱敏引擎（mask）

### 定位与边界
新增 `internal/mask`，纯函数式"结果集脱敏引擎"：executor 拿到 `model.QueryResult` 之后、返回 Agent 之前，对敏感列单元格打码。**不连数据库、不读 store、不做 pipeline 编排**（规则由 T13 从 `mask_rules`/Agent 配置加载后以 `[]mask.Rule` 注入，T11 只对入参负责）；不做 MCP/HTTP；**不新增第三方依赖**（仅标准库 regexp/strings）。v0.1 只实现 phone/email 两类 + mask 一种算法；idcard/bankcard 类型与 hash/range/block 算法只保留常量与"New 阶段明确报错"的扩展点，**绝不静默放行**（安全产品不能配了规则却不脱敏）。

### 冻结契约
```go
type SensitiveType string
const (
    TypePhone SensitiveType = "phone"
    TypeEmail SensitiveType = "email"
    // 预留 v0.2+：TypeIDCard="idcard"、TypeBankCard="bankcard"，v0.1 New 阶段报 ErrUnsupportedType
)
type Algorithm string
const (
    AlgoMask Algorithm = "mask"
    // 预留：AlgoHash="hash"、AlgoRange="range"、AlgoBlock="block"，v0.1 New 阶段报 ErrUnsupportedAlgorithm
)
type Rule struct {
    Column        string        // 结果集列名（别名/表达式列名），非空
    SensitiveType SensitiveType
    Algorithm     Algorithm
}
type RedactReport struct {
    TouchedColumns map[int]SensitiveType // 结果列下标 -> 命中类型（全空值列也记录）
    MaskedCells    int                   // 实际被改写的非空单元格数
}
type Redactor interface {
    Apply(result model.QueryResult) (model.QueryResult, RedactReport)
}
func NewRedactor(rules []Rule) (Redactor, error) // 重复列 / 未实现类型或算法 -> error
```

### 列名匹配（别名/表达式同样生效）
- 只依据**结果集列名** `QueryResult.Columns` 匹配，不依赖物理表列名：`SELECT phone AS p`、`SELECT concat(...) AS mobile`、视图/子查询列都按"最终结果列名"命中。
- 规范化：去首尾空白、去成对包裹引号（`"`/`` ` ``/`[]`）、统一小写后，与 `Rule.Column` 的同样规范化结果做**精确相等**；v0.1 不做下划线/拼音模糊（确定性优先、零误脱敏）。
- 一个结果列最多命中一条；NewRedactor 发现规范化后重复列名直接 error（fail-fast，不静默后覆盖）。

### 掩码算法（逐格、可复算）
- **maskPhone**：trim 后允许单个前导 `+` 与国家码 86（剥 86 后仍按 11 位）。标准 11 位 → 前3 + `****` + 后4（`13812345678→138****5678`）；长度 7–10 → 前3 + `*` + 后4；3–6 位 → 保留首字符其余 `*`；空串原样。
- **maskEmail**：按**最后一个** `@` 分 local/domain；local 非空 → 首字符 + `***`，`@domain` 原样（`ZhangSan@x.com→Z***@x.com`）；无 `@` 的非空值 → 首字符 + `***`。
- 空串 / 字面 `NULL` / `<nil>` 视为空值，原样不动、不计 MaskedCells。
- 只改字符串内容，不改列数、行数、列名，`RowCount/Truncated/LatencyMS` 原样复制。

### Apply 行为
- **深拷贝**入参 Rows/Columns 后再改，入参 `QueryResult` 及其底层切片不得被修改。
- 未命中列逐字节不变；命中列逐格套算法，累计 MaskedCells、记 TouchedColumns（整列空值也记命中类型，但不计格数）。
- rules 为空合法：返回等价深拷贝、report 零值。

### 文件
`types.go`（常量/Rule/Report/ErrUnsupported*）、`algorithm.go`（normalize 列名、maskPhone/maskEmail）、`redactor.go`（接口+实现+NewRedactor）、`redactor_test.go`、更新 `doc.go`。

### 测试（纯单测，不连库、不起 Docker）
phone 11位/短号/带 +86/空值；email 标准/无@/多点域名；别名列 `p`、表达式列、带引号列名命中；非敏感列与未命中格逐字节不变、行列数与 RowCount 不变；**入参不被修改**；重复列 New 报错；idcard/bankcard、hash/range/block 在 New 阶段返回明确 ErrUnsupported*；空 rules 返回等价副本；report 命中列与格数计数正确。既有全包零回归。

### 交付 / 主控验收
单位无 Go/Docker：文本级静态自审、注明"未本地编译，需主控端验证"，不跑 go/docker；打 `agentsql-in-T11-日期.zip` 并自证（文件数一致、zip 晚于源码、大小+SHA 前缀）。主控：tidy（**go.mod/go.sum 应无变化**）/gofmt/build/vet、mask 纯单测全过、全包零回归（**本单无 E2E、不起 Docker**）；独立 diag 构造含 phone/email/普通列的 QueryResult 验证掩码结果与 report。

## T12 审计服务（audit）+ 仓储多条件筛选扩展

### 定位与边界
- 新增 `internal/audit` 服务层：**Recorder 同步落审计（落库失败原样上抛、绝不吞错）**、多条件分页查询、JSONL 导出。复用 `store.AuditLogRepository`，服务层不直接持有 `*sql.DB`、不写裸 SQL（SQL 只允许出现在 store）。
- 小幅**扩展 store**：`AuditLogRepository` 增加 `FilteredPage`（动态 WHERE、全部占位符参数化）；原 `Page` 保留公开签名、内部改为委托"空 filter"，保证 T06 既有测试零回归。
- `model` 新增 `AuditFilter`（放最底层共享，避免 store 反向依赖 audit）。
- **只追加**硬约束：audit 与 store 都不得提供 audit_logs 的 update/delete 方法。
- 不做 pipeline 编排（T13 才把 Recorder 接进六段式）、不做 HTTP/MCP、不改 executor/mask/rules/engine/policy/auth/parser。**不新增第三方依赖**（仅 encoding/json、bufio、strings、io 标准库；测试用现有 modernc 内存 SQLite）；**不起 Docker、无 PG/MySQL E2E**（审计写本地 SQLite）。

### 冻结契约
```go
// model/audit_filter.go
type AuditFilter struct {
    TimeStart, TimeEnd          *time.Time
    AgentID, DatasourceID       *string // 非 nil 即精确匹配
    SessionID, MCPTool          *string
    Decisions                   []string // 非空: decision IN (...)
    StmtTypes                   []string // 非空: stmt_type IN (...)
    RiskMin, RiskMax            *int
    Keyword                     string // 非空: (sql_raw LIKE ? OR sql_norm LIKE ?)
    ObjectLike                  string // 非空: objects LIKE ?
}
func (f AuditFilter) IsEmpty() bool

// store：新增；原 Page(ctx,page,size) 改为 return r.FilteredPage(ctx, model.AuditFilter{}, page, size)
func (r *AuditLogRepository) FilteredPage(ctx context.Context, f model.AuditFilter, page, pageSize int) (AuditPage, error)

// audit
type Sink interface { Insert(ctx context.Context, l model.AuditLog) (model.AuditLog, error) } // *store.AuditLogRepository 天然满足
type Reader interface { FilteredPage(ctx context.Context, f model.AuditFilter, page, size int) (store.AuditPage, error) }
func NewRecorder(sink Sink) Recorder
type Recorder interface { Record(ctx context.Context, l model.AuditLog) (model.AuditLog, error) }
func NewService(reader Reader, sink Sink) *Service
func (s *Service) Record(ctx, model.AuditLog) (model.AuditLog, error)
func (s *Service) Page(ctx, model.AuditFilter, page, size int) (store.AuditPage, error)
func (s *Service) ExportJSONL(ctx context.Context, f model.AuditFilter, w io.Writer) (rows int, err error)
var ErrInvalidDecision = errors.New(...) // 支持 errors.Is；合法集合 allow/deny/approve/warn/error
```

### Record 规则
- ctx 为 nil 返回错误；`Decision` 必须属于 `{allow,deny,approve,warn,error}`，否则返回 `ErrInvalidDecision` 且**绝不调用 sink、不落库**。
- 其余字段不做静默篡改；TS 由 SQLite DEFAULT 与 Insert 回填。sink.Insert 的任何 error **原样向上返回**（不吞、不只打日志降级），由 T13 据此让主请求失败——本单必须用失败型 fake sink 写测试证明该错误透传。

### FilteredPage 动态 WHERE（参数化、防注入）
- 抽私有 `buildAuditWhere(f) (clause string, args []any)`，count 与 select 复用：从 `WHERE 1=1` 起，按非空条件依次 `AND ...`，**值一律走占位符、按序进 args，禁止字符串拼接用户值**。
- TimeStart `ts>=?`、TimeEnd `ts<=?`；AgentID/DatasourceID/SessionID/MCPTool 非 nil `col=?`；Decisions/StmtTypes 非空走 `IN (?,?...)`（占位符个数随长度）；RiskMin `risk_level>=?`、RiskMax `risk_level<=?`；Keyword `(sql_raw LIKE ? OR sql_norm LIKE ?)`、ObjectLike `objects LIKE ?`，LIKE 参数为 `%kw%` 并对 `% _` 做 ESCAPE 转义。
- COUNT(*) 带同一 WHERE 得 Total；SELECT 同 WHERE `ORDER BY ts DESC, id DESC LIMIT ? OFFSET ?`；空 filter 无 WHERE，等价原全量分页；page<1 / pageSize 越界复用 `ErrInvalidPage/ErrInvalidPageSize`。

### ExportJSONL
- 按 filter 内部以 pageSize=500 流式翻页直到取完，**硬上限 100000 行**（超出返回明确 `ErrExportLimit`，不静默截断）；每行 `json.Marshal` 一条 AuditLog + `"\n"`（指针字段 omitempty、TS 输出 RFC3339），`bufio.Writer` 收尾 Flush；ctx 取消及时停；w 为 nil 返回错误。返回实际写出行数。

### 文件
`model/audit_filter.go`；`store/audit_log_repository.go`（加 FilteredPage+buildAuditWhere、Page 委托）+ `store/audit_filter_test.go`；`audit/recorder.go`、`audit/service.go`、`audit/export.go`、更新 `audit/doc.go`、`audit/*_test.go`。

### 测试
- **store（内存 SQLite，复用现有 T06 建库/AGENTSQL_SECRET helper）**：插入 8 条不同 agent/decision/时间/stmt_type/risk 的记录，逐一与组合验证：单 agent、decision IN、时间区间、risk 区间、keyword 命中 sql_raw、空 filter=全量、分页 Total 与倒序、非法 page 报错；把 `' OR 1=1--` 当 Keyword 总数不变（参数化防注入证明）；`Page` 与 `FilteredPage(空)` 结果一致（T06 回归）。
- **audit（fake Sink/Reader 为主）**：allow/deny/approve 三类 Record 往返字段不丢；非法 decision 返回 ErrInvalidDecision 且 sink 零调用；失败型 sink 的 error 被 Record 原样透传；ExportJSONL 行数正确、每行能 Unmarshal 回 AuditLog、TS 为 RFC3339、filter 透传、nil writer 报错、达上限 ErrExportLimit。代码层确认无 audit update/delete。

### 交付 / 主控验收
单位无 Go/Docker：文本级静态自审（动态 SQL 全占位、rows.Close、错误透传、nil 防御），注明"未本地编译需主控验证"，不跑 go/docker；打 `agentsql-in-T12-日期.zip` 自证。主控：tidy（**go.mod/go.sum 无变化**）/gofmt/build/vet、store+audit 单测全过、全包零回归（**不起 Docker**）；独立 diag 开临时 SQLite 文件，Record 三类→FilteredPage 组合筛→ExportJSONL 落一个 .jsonl，逐行核对内容后删除。

## T13 六段式流水线 ★（pipeline，编排核心）

### 目标
把已建好的 parser/auth/policy/engine/rules/executor/mask/audit/store 串成**一次受保护 SQL 请求的唯一入口**：API Key+数据源 ID+原始 SQL+会话元数据 → 认证 → 取数据源与策略 → 解析 → 两闸风险评估 → 四态决策 →（仅放行才）执行 → 脱敏 → 同步审计 → 结构化响应。本单只做编排与编排级测试；**不做** HTTP/MCP（T14/T15）、**不做** DB 规则开关与脱敏规则的仓储 List/装配（T16，本单一律走端口注入）。

### 文件（internal/pipeline，替换 doc.go 占位）
- types.go：Request/Response、阶段名常量、端口接口、错误哨兵
- pipeline.go：Pipeline 结构、New、Process 主流程与逐段计时
- rules.go：内置规则装配、静态/动态规则分轮、两轮 Assessment 合并
- auditmap.go：Assessment+执行结果 → model.AuditLog（RuleHits 序列化）
- pipeline_test.go：fake 端口+spy executor 纯单测（不起 Docker）
- e2e_postgres_test.go：testcontainers 6 条 E2E，探测不到 Docker 必须 t.Skip
- 更新 doc.go 包注释

### 冻结契约
```go
type Request struct {
    APIKey         string
    DatasourceID   string
    SQL            string
    SessionID      string          // 空=无状态池路径;非空=OpenSession 绑定连接(支持事务)
    ConversationID *string
    MCPTool        string          // 来源工具名,写审计
    ClientIP       *string
    ModelName      *string
}
type Response struct {
    Decision   model.Decision
    Assessment model.Assessment  // Hits/Reason/Suggestion/StageLatency 完整
    Result     *model.QueryResult // 仅 allow/warn 且实际执行才有
    Redact     mask.RedactReport
    ApprovalID string             // 仅 approve
    AuditID    int64
}
// pipeline 只依赖这些最小端口,T16 的 server 用 store/executor 适配;单测用 fake
type IdentityAuthenticator interface { Authenticate(ctx context.Context, rawKey string) (model.Agent, error) }
type DatasourceReader interface { Get(ctx context.Context, id string) (model.Datasource, error) }
type PolicyLoader interface { ListByAgentAndDatasource(ctx context.Context, agentID, datasourceID string) ([]model.Policy, error) }
type ExecutorProvider interface { GetOrOpen(ds model.Datasource, secret []byte) (executor.Executor, error) }
type ApprovalWriter interface { Create(ctx context.Context, a model.Approval) (model.Approval, error) }
type AuditRecorder interface { Record(ctx context.Context, l model.AuditLog) (model.AuditLog, error) }
type RedactorBuilder interface { RedactorFor(ctx context.Context, datasourceID string) (mask.Redactor, error) }
```
构造 `func New(ports Ports, secret []byte, opts ...Option) (*Pipeline, error)`：Ports 聚合上述七端口，**任一 nil 在 New 阶段报错**；内部持有 policy.NewResolver()、零值 engine.Engine{}、rules.NewDefaultTokenBucketLimiter() 单例。secret 是 AGENTSQL_SECRET 的 32 字节密钥、由上层注入（pipeline 不读环境变量），供 ExecutorProvider.GetOrOpen 解密数据源密码。

### Process 八阶段（StageLatency 的 key 固定如下）
- **S1 auth**：Authenticate(APIKey)→Agent；失败 fail-closed 拒绝，不解析、不连业务库，审计 decision="error"。
- **S2 load**：DatasourceReader.Get 得 Datasource（DBType→dialect）；PolicyLoader 取策略行后 policy.Resolver.Resolve(rows, agent.Level)→*PolicyDecision；失败拒绝+审计 error，不连业务库。
- **S3 parse**：parser.NewParser(dialect).Parse(SQL)→*AST；畸形/不可解析拒绝+审计 error，不连业务库。
- **S4 guard_static**：第一闸，只跑不依赖连接的静态规则（见两闸）。结果为 deny → 直接跳 S8 审计返回，**全程不调 ExecutorProvider、不产生业务库连接**。
- **S5 guard_dynamic**：仅静态非 deny 才进入——ExecutorProvider.GetOrOpen（唯一允许的首次业务库接触）；需要会话则 OpenSession；对原 SQL 跑**只读 Explain** 填 AST.Explain；以真实 session 为 MetadataProvider 装配动态规则做第二闸，与第一闸 Hits 合并后按 deny>approve>warn>allow 重算最终 Assessment。此阶段只允许只读 EXPLAIN/元数据/事务态探测，绝不执行用户 SQL。
- **S6 execute**：deny 不执行（动态升级 deny 时也仅做过只读探测）；approve 在 v0.1 **不执行**，ApprovalWriter.Create 落 pending 审批单并回填 AuditID，Response.ApprovalID=单号；allow/warn 按 AST.StmtType 选 Query(SQL,rowLimit)（只读类）或 Execute(SQL)（其余），有 SessionID 走绑定 session 同名方法、无则走 executor 无状态方法，rowLimit=datasource.RowLimit（0 用默认 1000）；执行报错→审计 error 并返回错误、不脱敏。
- **S7 redact**：仅实际返回结果集时 RedactorBuilder.RedactorFor(dsID) 取脱敏器 Apply(Result)，带出 Redact 报告；写操作/无结果集跳过；RedactorBuilder 出错 fail-closed+审计 error。
- **S8 audit**：**所有分支**（allow/warn/deny/approve/error）返回前都 AuditRecorder.Record 同步落一条；审计返回错误则整个 Process 判失败（T12 语义）。

### 两闸评估与"deny 不触库"硬保证
- 规则装配：通用 rules.NewGenericRules(limiter)；PG 用 rules.NewPostgresRules(meta)、MySQL 用 rules.NewMysqlRules(meta)。
- 动态规则集合（需连接元数据/EXPLAIN）：通用 **R004**（依赖 AST.Explain）、PG **R105/R106/R107**（构造注入 meta）、MySQL **R204**（事务态）。**必须逐条读 rules 源码最终确认集合完整**：第一闸只放静态规则并传"任一方法被调即 panic 的 metadataProvider 桩"，用单测断言第一闸全程零触该桩——若漏划某动态规则进静态组，该测试立即变红，以此自证划分无遗漏。
- 第一闸 meta 传 nil 安全桩、不 Explain（AST.Explain=nil）；第二闸才用真实 session meta 且先 Explain。
- mergeAssessment 为纯函数：合并两闸 Hits（保持规则 ID 顺序、同 ID 以第二闸为准），按 deny>approve>warn>allow 重算 Decision/Risk/Reason/Suggestion，EstScanRows 取第二闸 Explain 值。
- 精确安全语义（写进注释与验收）：**deny/approve/error/静态拒绝路径下 Executor.Query/Execute 与 Session.Query/Execute 零调用**；"静态即 deny"时连 GetOrOpen/Explain/OpenSession 都零调用；仅"静态放行后被动态升级为 deny"时允许只读 Explain/元数据调用、仍零用户 SQL 执行。全部用 spy executor 计数断言。

### 审计字段映射（auditmap.go）
AgentID/DatasourceID/SessionID（非空才取指针）、ConversationID、MCPTool、DBType=datasource.DBType、SQLRaw=Request.SQL、SQLNorm=AST.Normalized、StmtType=string(AST.StmtType)、Objects=AST.Tables 规范化字符串、Decision=最终四态（解析/认证/内部错误用 "error"）、RuleHits=Hits 的 JSON、RiskLevel=int(Assessment.Risk)、EstRows=AST.Explain.EstScanRows、LatencyMS=总耗时、RowsReturned=结果行数、ClientIP/ModelName、ErrorMsg。approve 分支先 Record 拿 AuditID 再回填 Approval.AuditID。

### 超时与并发
透传调用方 ctx；S5/S6 用 datasource.StmtTimeoutMS（0 用默认 5000ms）派生子 ctx 用于 Explain/执行。Pipeline 无共享可变状态（limiter 自身并发安全），主控用 `go test -race` 验证 50 goroutine 并发无竞态。

### 测试
纯单测（fake 端口+spy executor，不起 Docker）至少覆盖：①认证失败拒绝且零业务库接触；②畸形 SQL 拒绝零接触；③无 WHERE UPDATE 静态 deny，断言 GetOrOpen/Explain/OpenSession/Query/Execute 全零；④越权表 deny 零接触；⑤readonly Agent 写（R003）deny；⑥静态 allow 但 Explain 超扫描行动态升级 approve，断言只调 Explain、未调 Query/Execute、生成审批单；⑦allow SELECT 走 Query 且结果被脱敏、审计 RowsReturned 正确；⑧warn 放行且审计 warn；⑨执行器报错→审计 error 并返回错误；⑩AuditRecorder 报错→Process 失败；⑪StageLatency 八 key 齐全非负；⑫mergeAssessment 四态优先级；⑬静态组 panic-provider 零触达自证；⑭有/无 SessionID 分别走 session 与无状态路径。
E2E（e2e_postgres_test.go，testcontainers 用本地 postgres:16；探测不到 Docker 必须 t.Skip 而非失败）6 条：E1 正常 SELECT 放行；E2 无 WHERE 更新被拒且目标表数据未变；E3 未授权表被拒；E4 大表全表扫描超阈值转审批不执行；E5 readonly 写被拒；E6 命中脱敏列结果被打码。建表/造数/内置 Agent+数据源+策略在测试内完成。

### 边界
不新增第三方依赖（testcontainers 已在 go.mod，go.mod/go.sum 必须不变）；不改 store（不补 List）、不改 engine/parser/rules/executor/mask/audit/auth/policy 公开签名（确需小补充先在交付说明列出并论证，禁止静默改）；不做 HTTP/MCP/前端；不读环境变量（secret 构造注入）。

### 交付 / 主控验收
单位无 Go/Docker：文本级静态自审（两闸划分、deny 零用户 SQL、每分支必落审计、错误透传、nil 防御、ctx 透传），注明"未本地编译、E2E 需主控 Docker 验证"，不跑 go/docker；打 agentsql-in-T13-日期.zip 自证（文件数、zip 晚于源码、大小+SHA-256 前缀）。主控：tidy（go.mod/go.sum 无变化）/gofmt/build/vet、`go test -race ./internal/pipeline` 纯单测全过且全包零回归，再启动 Docker 跑 6 条 E2E 全绿，并用 spy 独立复核 deny 路径零执行。

## T14 MCP stdio（internal/mcpserver + internal/bootstrap + cmd mcp，首个对外可用单）

### 目标与形态
用官方 `github.com/modelcontextprotocol/go-sdk` 实现 **stdio MCP Server**：AI 客户端（Cursor / Claude Desktop / Cherry Studio 等）以本地子进程方式拉起 `agentsql mcp`，经 JSON-RPC over stdio 完成 initialize / tools/list / tools/call，对 AI 暴露 3.4 冻结的 7 个工具；**每个执行类工具的 handler 都把请求交给 T13 的 pipeline.Process，网关自身不得存在任何"绕过流水线直接执行用户 SQL"的旁路**。本单完成后产品第一次能被真实 AI 客户端端到端调用。不做 Streamable HTTP（T15）、不做管理 REST（T16）、不做前端。

### 依赖（已获授权新增）
- 新增且**仅**新增一个直接依赖 `github.com/modelcontextprotocol/go-sdk`：`go get github.com/modelcontextprotocol/go-sdk@latest` 取当时最新稳定版，**确切版本号写进交付说明**，go.mod/go.sum 一并提交；除它及其传递依赖外不得引入其他第三方库。SDK 的具体类型/函数名以实际下载版本的 API 为准（不要凭记忆写签名），交付说明列出本单实际用到的 SDK 类型与函数；主控 tidy 后以主控 go.mod/go.sum 为最终准。

### 新增 / 改动清单
1) **internal/store 补 3 个只读方法**（纯新增、不改既有、全部占位符参数化、各配单测）
   - `DatasourceRepository.List(ctx) ([]model.Datasource, error)`：按 id 排序返回全部数据源；注释写明密文字段 PasswordEnc 不得对外泄漏（裁剪在 mcpserver 层做）。
   - `PolicyRepository.ListByAgent(ctx, agentID string) ([]model.Policy, error)`：返回该 Agent 全部策略行，按 datasource_id/object_name 排序，agentID 走占位符。
   - `MaskRuleRepository.ListByDatasource(ctx, datasourceID string) ([]model.MaskRule, error)`：条件 `datasource_id = ? OR datasource_id IS NULL`（数据源专属 + 全局规则），占位符参数化，按 table_name/column_name 排序。
   - 三者空结果返回非 nil 空切片；nil ctx 口径与既有仓储一致（ErrNilContext）；保证 store 既有测试零回归。

2) **pipeline 小幅向后兼容扩展**（改 internal/pipeline，T13 全部测试必须零回归）
   - `Request` 新增两个**零值不改变现状**的可选布尔字段：`ExplainOnly bool`、`RequireApproval bool`（默认 false 时行为与 T13 完全一致）。
   - ExplainOnly：两闸评估完成、进入 S6 执行**前**短路——不 Query/Execute、Result 保持 nil，照常走 S7（无结果则 RedactReport 空）与 S8 审计（MCPTool=explain_query），返回 Decision/Assessment（含 EstScanRows、Hits、StageLatency），实现"只评估不落地"。
   - RequireApproval：两闸聚合出最终决策后做**单向抬升**——deny 仍 deny（最高优先，不得被审批请求覆盖）；allow/warn 抬为 approve 并走既有 approve 分支（不执行、先审计后建审批单回填 AuditID）；本来就是 approve 的不变。
   - 各配单测：ExplainOnly 下 spy executor 的 Query/Execute 零调用但有审计；RequireApproval 把 allow 抬成 approve 且零执行、deny 不被抬升；默认 false 时既有纯单测与 6 条 E2E 全绿。

3) **新增 internal/bootstrap 装配包**（T15/T16 复用，一次写对）
   - 提供 `Assemble(ctx, cfg config.Config, secret []byte) (*Runtime, error)` 与 `Runtime{ Pipeline *pipeline.Pipeline; Executors *executor.Manager; Store *store.Store; Close() error }`（命名可微调，语义如此）：打开 metadata store；`executor.NewManager(false)`（**非全局只读**，读写交给 pipeline 的 Agent 级别与规则裁决）；`auth.NewAuthenticator(store.Agents())`；把七端口接到真实实现——Authenticator=认证器、Datasources=store.Datasources()、Policies=store.Policies()、Executors=Manager、Approvals=store.Approvals()、Audit=store.AuditLogs()。
   - Redactors 为本包新建 redactorBuilder：`RedactorFor(ctx, dsID)` 调 MaskRuleRepository.ListByDatasource → model.MaskRule 映射为 mask.Rule（字符串转 mask 常量；遇到 v0.1 不支持的 idcard/bankcard 或非 mask 算法**返回错误而非静默放行**）→ mask.NewRedactor；无规则用 NewRedactor(nil)（不脱敏、不报错）。
   - fail-closed：secret 非恰好 32 字节、store 打不开、任一依赖为 nil 都返回错误。用内存 SQLite 写装配单测（构造 Agent/数据源/策略/脱敏规则，Assemble 后跑一条 allow SELECT 验证脱敏生效），并覆盖三类构造失败。

4) **新增 internal/mcpserver（本单主体）**
   - `server.go`：用 go-sdk 构造 MCP server（名 agentsql、版本取自 cmd 的 version），`RunStdio(ctx, opts)` 以 SDK stdio transport 把 os.Stdin/os.Stdout 作为协议通道阻塞服务；opts 含绑定 APIKey、*Runtime、logger。
   - **stdio 红线**：stdout 只允许写 MCP JSON-RPC 帧；zerolog 与一切诊断/错误/调试输出**一律 stderr**；该路径禁止 fmt.Print 到 stdout，并用测试钉死"日志不落 stdout"。
   - **身份绑定 fail-closed**：stdio 为本地单连接单租户，APIKey 来自 `--api-key` 或环境变量 `AGENTSQL_API_KEY`；server 启动先 Authenticator.Authenticate 一次拿到 Agent，缺失/失效/过期/禁用即返回错误退出，不允许匿名、不允许逐工具匿名调用；认证所得 Agent 在 7 个 handler 内复用。
   - `tools.go`：按 3.4 冻结表注册 7 个工具，各自声明严格 JSON input schema（字段名、required、类型、中文 description，说明用途与"可能被拦截/转审批"的预期以引导模型正确选工具）与统一输出结构；**绝不注册 execute_raw_sql 类万能工具**。
   - `handlers.go`：入参校验（缺参/类型错返回 isError + 结构化中文错误，不 panic），7 个 handler 语义如下——
     * list_datasources{}：ListByAgent 得授权 datasource_id 集合，与 DatasourceRepository.List 取交集，只回 `{id,name,db_type}`（剔除 host/账号/PasswordEnc 等连接细节）。
     * list_schema{datasource_id, table?}：先校验数据源在授权集合内（不在→deny 结构化错误）；经 Executors.GetOrOpen 取连接，用**内置常量 SQL** 查 information_schema（PG 与 MySQL 各一段方言）；table 非空加表过滤；**table/schema 标识符只允许 `[A-Za-z0-9_]`（必要时含点），正则白名单校验后再绑定/安全拼接，杜绝注入**；列清单经 policy.FilterColumnsForSchema 按列权限**只减不增**；用完归还连接。
     * explain_query{datasource_id,sql}：pipeline.Request{ExplainOnly:true,MCPTool:"explain_query"}，回四态决策、命中规则、EstScanRows、suggestion，不回结果集。
     * query{datasource_id,sql}：handler 先用 parser 做工具语义门禁，StmtType 必须 SELECT（否则回 `{decision:"deny",reason:"query 仅用于查询，写操作请改用 execute_write",suggestion}`，不进执行）；通过则 Request{MCPTool:"query"} 全流程，回脱敏结果集 + 决策/告警。
     * execute_write{datasource_id,sql,reason}：reason **必填非空**（强制 AI 说明写理由，空则引导补全）；门禁 StmtType∈{INSERT,UPDATE,DELETE,DDL}（SELECT 误用引导去 query）；Request{MCPTool:"execute_write"} 全流程，readonly Agent 由 pipeline 拒（T13 已保证），reason 写 stderr 结构化日志。
     * request_approval{datasource_id,sql,reason}：reason 必填；Request{RequireApproval:true,MCPTool:"request_approval"}，回 approval_id 与 pending。
     * get_approval_result{approval_id}：ApprovalRepository.Get，且**只能查本 Agent 的审批单**（AgentID 不匹配按 not found 处理，防越权枚举），回 status/decided_at/approver。
   - `schema.go`：list_schema 的两方言 introspection 与标识符白名单单独成文件、纯函数可测。
   - **统一返回契约**：所有工具返回体统一 `{decision, reason, suggestion, data?}`，decision∈allow/warn/approve/deny/error；suggestion 一律中文、教 AI 自我改写；pipeline 的 error 映射 decision=error，**不得向模型泄漏内部堆栈、连接串或密钥**。
   - 并发：SDK 可能并发回调，handler 无共享可变态，只依赖 Runtime 的线程安全组件；`go test -race` 必须干净。

5) **cmd/agentsql 新增 `mcp` 子命令**
   - flags：`-c/--config`（同 serve，默认 config.yaml）、`--api-key`（缺省读 AGENTSQL_API_KEY）；secret **只从环境变量 AGENTSQL_SECRET 读，不放进命令行参数**（避免进进程列表）；流程 config.Load → bootstrap.Assemble → mcpserver.RunStdio 阻塞，SIGINT/SIGTERM 优雅 Close（关连接池与 store）；启动信息只走 stderr。version/serve 行为与其测试不变、零回归。

6) **examples/mcp/**：`cursor_mcp.json`、`claude_desktop_config.json` 两份可直接改路径的样例（command 指向 agentsql、args 含 `mcp -c`、env 放 AGENTSQL_SECRET/AGENTSQL_API_KEY 占位），注释提醒真实密钥不得提交。

### 测试（不起 Docker；用 SDK in-process/内存 transport + 内存 SQLite）
- store 三新方法（空结果非 nil、参数化、排序、全局规则 OR IS NULL）；pipeline 两新字段 + T13 全量零回归；bootstrap 装配（内存 SQLite 端到端一条脱敏 SELECT、三类 fail-closed）。
- mcpserver：用 SDK 内存 channel/in-process client 直调 7 工具，覆盖未授权数据源拒、list_schema 列裁剪、query 只 SELECT 门禁、execute_write 的 reason 必填与 readonly 拒、request_approval 抬升且零执行、get_approval_result 越权 not found、ExplainOnly 无结果集、错误体不含密钥/连接串、stdout 纯净、并发 `-race`；这些测试不依赖 npx 或真实 Cursor。

### 边界
不做 Streamable HTTP（T15）、不做管理 REST 与 DB 规则开关装配（T16：本单 pipeline 不传 WithRuleLayers，规则用代码默认 21 条）、不做前端；不改 7 张表结构；除 pipeline.Request 新增两个可选字段外不改既有公开签名（确需小补充先在交付说明列出论证，禁止静默改）；除 go-sdk 外不新增第三方依赖。

### 交付 / 主控验收
单位无 Go/Docker：文本级静态自审（stdout 纯净、fail-closed、执行类全走 pipeline 无旁路、标识符白名单、错误不泄密、零值字段不改变 T13 行为、ctx 透传、并发无共享可变态），注明"未本地编译，需主控 go test -race 与 SDK in-process 走查 7 工具"，不跑 go/docker；打 agentsql-in-T14-日期.zip 自证（文件数、zip 晚于源码、大小+SHA-256 前缀），交付说明写明 go-sdk 确切版本与实际用到的 SDK 类型清单。主控：go mod tidy（审查新增直接/间接依赖清单）、gofmt/build/vet、`go test -race ./...` 全过零回归、SDK in-process 走通 7 工具；人工用 `npx @modelcontextprotocol/inspector` 连一遍，并在真实 Cursor 完成一次只读成功 + 一次被拒、录屏存档。

## T15 MCP Streamable HTTP（internal/mcpserver/http.go + cmd serve，多租户网关入口）

### 目标与形态
在 T14 stdio（本地单连接单租户）之外，提供 **MCP Streamable HTTP 传输**：远程 AI 客户端/Agent 平台经 `POST http://host:port/mcp`、带 `Authorization: Bearer asql_xxx` 调用同一套 7 工具。HTTP 是**多租户**的：一个进程同时服务多个 Agent，每个请求携带不同 Key，必须**逐请求实时认证**、身份彼此隔离。本单只做无状态 HTTP MCP 与认证/限流/共用端口骨架；不做管理 REST（T16 挂 /api/v1）、不做前端、不做有状态 SSE 会话与服务端推送。**T14 stdio 的对外行为与全部测试必须零回归。**

### 传输选型（用 go-sdk v1.7.0 已验证的真实 API，不要凭记忆写签名）
- `mcp.NewStreamableHTTPHandler(getServer func(*http.Request) *mcp.Server, opts *mcp.StreamableHTTPOptions) *mcp.StreamableHTTPHandler`，返回值实现 `http.Handler`。
- opts 固定：`Stateless: true`（不读写 Mcp-Session-Id、每请求独立临时会话、GET/DELETE 由 SDK 回 405）、`JSONResponse: true`（直接回 application/json，不建立 text/event-stream 长连，最简且利于限流/反代）、`MaxRequestBodyBytes: 4<<20`（4MiB，超限 SDK 回 413）、`PropagateRequestCancellation: true`、`Logger` 传一个丢弃或转 stderr 的 slog（SDK 自身日志不得写 HTTP 响应体之外的业务通道）。
- SDK 文档明确 "It is OK for getServer to return the same server multiple times"——这是本单"每 Agent 缓存一个 *mcp.Server"方案的依据。

### 多租户身份模型（本单灵魂，必须严格按此实现）
stdio 是启动时认证一次、把单个 Agent 绑死在 toolHandlers 字段上；HTTP 不能这样做（否则并发请求会串身份）。采用**中间件逐请求认证 + 每 Agent 缓存绑定身份的 Server**：
1) **抽出可复用构造**：把 T14 `NewServer` 中"认证成功后，按一个已知 Agent 构造 *mcp.Server、注册 7 工具、其 toolHandlers 绑定该 Agent 与明文 key"的部分，重构为包内函数 `buildBoundServer(agent model.Agent, plainKey string, rt *bootstrap.Runtime, logger zerolog.Logger) (*Server, error)`（命名可微调，语义如此），做 level∈{readonly,dml,ddl} 与 runtime 完整性校验、cloneAgent 深拷贝。T14 的 `NewServer` 改为：入参校验 → Authenticator.Authenticate 一次 → buildBoundServer，**行为与 T14 完全一致**；`RunStdio` 不变。
2) **authMiddleware（标准库 http.Handler 包装，认证在进 SDK 之前）**：从 `Authorization` 头取 `Bearer <key>`（精确匹配前缀，首尾空格 Trim）；无头/方案非 Bearer/key 空/Authenticate 失败/Agent.Status!="active"/level 非法，一律回 `401`、JSON `{"error":"unauthorized"}`，**不区分具体原因、不回内部信息**，且直接 return、不调用下一层（因此不产生审计、不触达任何 executor）。认证通过则把"该 Agent 值 + 明文 key"用**包内不导出的 context key 类型**经 `req = req.WithContext(...)` 注入后再调下一层。
3) **agentServerRegistry（每 Agent 一个 Server，结构上隔离）**：内部 `sync.RWMutex` + `map[string]*mcp.Server`；键 = `agent.ID + "|" + strconv.FormatInt(agent.UpdatedAt.UnixNano(),10)`（级别/状态/Key 轮换导致 UpdatedAt 变化即自然重建，杜绝快照过期；UpdatedAt 为零值时退化为 agent.ID）。`getOrCreate(agent, plainKey)` 双检锁：命中直接返回，未命中调 buildBoundServer 建并缓存。设缓存条目上限 256，达到上限时清空整张缓存后重建（v0.1 简单策略，注释写明；被清的 Server 由 GC 回收，正在处理的请求因持有自己的引用不受影响）。
4) **getServer 回调**：`func(r *http.Request)*mcp.Server` 从 `r.Context()` 取中间件注入的 Agent/key（middleware 已保证存在；若缺失属编程错误，返回 nil，由 SDK 回 400，并在 stderr 记 error），调 registry.getOrCreate。**不同 Agent 拿到不同 Server 实例、其 toolHandlers.agent 互不共享，从根上杜绝并发串号；同一 Agent 的同一 Server 被并发回调是安全的（T14 已 -race 验证 handler 无共享可变态）。**
5) **限流中间件（认证之后、进 SDK 之前）**：每 Agent 一个 `golang.org/x/time/rate.Limiter`，速率取 `cfg.Defaults.QPSPerAgent`（burst 取相同值或 2 倍，注释说明），limiter 随 registry 同生命周期管理（同锁、同键、同清）。`Allow()` 为 false 时回 `429`、JSON `{"error":"rate limited"}`，不进 SDK。`golang.org/x/time` 已是 go-sdk 的传递依赖（go.sum 在 T14 已含 v0.15.0），本单 tidy 后它从 indirect 转 direct，**不属于新增第三方库**；除此之外不得引入任何新依赖（不引 gin 等 web 框架，只用标准库 net/http）。
6) **其他中间件**：①recover 兜底——defer recover 捕获 panic，回 `500 {"error":"internal error"}`、stderr 记堆栈，**绝不让单个请求 panic 拖垮进程或把堆栈写进响应体**；②访问日志走 stderr（method、path、agent_id、状态码、耗时 ms，不记 SQL 正文与密钥）；③可选 CORS 仅在配置显式开启时加，默认不加。

### 新增 / 改动清单
1) **internal/mcpserver/http.go（新增）**：
   - `NewHTTPHandler(rt *bootstrap.Runtime, cfg config.Config, logger zerolog.Logger) (http.Handler, error)`：fail-closed（rt/cfg 不合法返回错误）。内部用 `http.NewServeMux()`：`mux.Handle("/mcp", chain(authMiddleware, rateMiddleware, recoverMiddleware, logMiddleware, sdkHandler))`，其中 sdkHandler = NewStreamableHTTPHandler(getServer, opts)。**预留 `/api/v1` 的挂载位置与注释（T16 实现，本单不挂任何业务）**。非 /mcp 路径回 404 JSON。
   - 同文件实现 context key、agentServerRegistry、两（或三）个中间件，全部纯函数/小类型、可单测；认证器用 `auth.NewAuthenticator(rt.Store.Agents())`。
2) **internal/mcpserver/server.go（重构，非重写）**：按上节抽出 buildBoundServer，NewServer/RunStdio 保持 T14 语义；新增代码不改变 stdio 路径。tools.go/handlers.go/schema.go **不改**（HTTP 与 stdio 共用同一套 7 工具 handler，这正是抽出 buildBoundServer 的目的）。
3) **cmd/agentsql 的 `serve` 子命令（扩展为真正起 HTTP 服务）**：保留既有 config.Load/Validate 语义；secret **只从环境变量 AGENTSQL_SECRET 读、必须恰好 32 字节**（缺/错即报错退出，不进命令行参数）；流程 Load→Assemble→mcpserver.NewHTTPHandler→构造 `&http.Server{Addr: cfg.Server.HTTPListen, Handler: h, ReadHeaderTimeout: 10s}` 并 ListenAndServe；`signal.NotifyContext(Interrupt,SIGTERM)` 触发时先 `httpServer.Shutdown(ctx 10s 超时)` 再 `runtime.Close()` 优雅退出；启动监听地址/停止信息只走 stderr。**serve 不接收 --api-key**（HTTP 每请求自带 Bearer）。version/mcp 子命令与其测试不变。
4) **examples/mcp/**：新增一份 Streamable HTTP 客户端配置样例（如 `cursor_http_mcp.json`），url 指向 `http://127.0.0.1:8650/mcp`、headers 放 `Authorization: Bearer asql_xxx` 占位，注释提醒生产经 HTTPS 反向代理暴露、真实密钥不得提交。
5) 不改 7 张表结构；config 复用既有 server.http_listen 与 defaults.qps_per_agent，不新增配置项（CORS 默认关即可，不暴露开关）。

### 测试（httptest + 内存 SQLite，不起 Docker；全部 `go test -race` 干净）
- 端到端：httptest.NewServer 起 NewHTTPHandler，用裸 HTTP 依次 POST initialize / tools/list / tools/call(list_datasources)（Content-Type: application/json，Accept 含 application/json,text/event-stream，带正确 Bearer），断言 200、tools/list 恰好 7 工具且无 execute_raw_sql、list_datasources 返回本 Agent 授权源的结构化 JSON。
- 401 矩阵：无 Authorization 头、scheme 非 Bearer、key 错误、Agent 禁用(status 非 active) 四种各回 401、body 恰为 {"error":"unauthorized"} 且无内部信息；用 spy 断言这些请求**零审计写入、executor 零触达**。
- **多租户不串号（核心验收）**：建 agentA/agentB，分别只授权 ds-a / ds-b；起 50 个 goroutine、用两 key 高并发交替各发多次 list_datasources，-race 下断言每个响应严格只含调用方自己的数据源、A/B 全程零交叉、无 DATA RACE；并断言 registry 最终只建了 2 个 bound server（同 Agent 复用）。
- 429：把 cfg qps 调到极小，打满 burst 后下一请求 429；405：GET /mcp 回 405；400：畸形 JSON 体回 400；413：超过 4MiB 回 413；非 /mcp 路径 404。
- 缓存失效：构造 UpdatedAt 不同的同 ID Agent，断言第二次 getOrCreate 新建 server（键变化）；达到上限后清空重建逻辑可测。
- recover：用一个测试用注入 handler 触发 panic，断言回 500、进程不崩、响应体无堆栈。
- 回归：T14 stdio 全部用例（含 sdk_inprocess_test、stdout 纯净、fail-closed）与既有全包零回归。

### 边界
不做管理 REST 与"从库加载规则开关/阈值"（T16：本单 pipeline 仍不传 WithRuleLayers，用代码默认 21 规则）；不做有状态会话/SSE 长连/服务端主动通知（选 stateless）；不做 TLS 终止与证书（由 Nginx/Caddy 等反代负责，样例注释注明生产必须 HTTPS）；不做前端、不改表结构、不改 7 工具语义；除把已在依赖图的 golang.org/x/time 转为直接依赖外，不新增任何第三方库。

### 交付 / 主控验收
单位无 Go：文本级静态自审（认证与限流都在进 SDK 之前、每 Agent 独立 bound server 且 handler 不共享 Agent 态、context key 不导出不泄漏、401/429/405/400/413/500 路径、secret 只走环境变量、日志全 stderr、stdio 路径零改动语义），注明"未本地编译，需主控 go test -race、httptest 端到端与 50 并发不串号验证"，不跑 go/docker；打 agentsql-in-T15-日期.zip 自证（文件数、zip 晚于源码、大小+SHA-256 前缀），交付说明列出实际用到的 go-sdk Streamable HTTP API 与 x/time 的引用位置。主控：go mod tidy（确认仅 x/time 由 indirect 转 direct、无其他新增）、gofmt/build/vet、`go test -race ./...` 全过且 T14 零回归、httptest 端到端与 50 并发交错不串号、用 curl 真实打一次 HTTP MCP（正确 key 成功、错 key 401、超频 429）。

## T16 管理 REST API（internal/adminapi，标准库 net/http，零新增第三方依赖）
只做本单，不做前端（T17+）。技术选型已定死：**只用 Go 标准库 net/http，不引 gin/echo 等任何框架**；用 Go 1.22+ ServeMux 的方法路由（`mux.HandleFunc("GET /api/v1/agents", h)`）与路径参数（`r.PathValue("id")`）。先读真实签名再写：internal/store 各 Repository、internal/model（storage.go/audit_filter.go）、internal/bootstrap.Runtime、internal/mcpserver/http.go（T15 装配）、internal/config/config.go、cmd/agentsql/main.go 的 newServeCommand。不得凭名字猜字段。

### 16.1 管理端认证（与 /mcp 的 Agent Key 完全两套，互不影响）
- 凭证只走环境变量，不写进 yaml、不落库：`AGENTSQL_ADMIN_USER`（缺省 `admin`）、`AGENTSQL_ADMIN_PASSWORD`（非空）。比对用 `crypto/subtle.ConstantTimeCompare`，防时序侧信道。
- `POST /api/v1/auth/login`，请求体 `{username,password}`，成功签发**无状态**管理员 token（不建 sessions 表、不服务端存会话，追求最高性能与最简部署）。token 形态 `base64url(payload).base64url(sig)`：payload=JSON `{iat,exp,jti}`，签名 HMAC-SHA256，签名密钥由 `HMAC-SHA256(AGENTSQL_SECRET, []byte("agentsql-admin-token-v1"))` 派生（不直接用主密钥）；有效期 12 小时；响应 `{token,expires_at}`。用标准库 crypto/hmac、crypto/sha256、encoding/json 实现，禁止引入 jwt 第三方库。
- `adminAuthMiddleware`：除 `POST /api/v1/auth/login` 外，每个 /api/v1 请求校验 `Authorization: Bearer <admin-token>`：拆分精确（参照 T15 bearerKey 的严格风格，异常空格拒绝）、验签、校验 exp，任一失败统一 401 `{code:401,msg:"unauthorized"}`，不区分原因、不泄漏内部。注意：/mcp 用 T15 的 Agent key 中间件，/api/v1 用本中间件，前缀隔离，绝不能互相放行。
- `GET /api/v1/auth/me` 返回当前管理员用户名；`POST /api/v1/auth/logout` 无状态下直接返回 ok（客户端丢弃 token；服务端黑名单 v0.1 不做，注释说明留企业版）。

### 16.2 统一响应、分页、入参与安全
- 成功统一 `{code:0,msg:"ok",data:...}`；失败 `{code:<非0>,msg:<面向用户的安全信息>,data:null}`，HTTP 状态语义化：400 参数格式/分页越界/未知字段、401 未认证、403 不允许（如删内置规则）、404 不存在、409 状态或引用冲突、422 业务校验失败、500 内部错误。
- 列表分页统一 `{total,page,page_size,list}`；page 默认 1、page_size 默认 20、最大 100，越界 400。
- 所有 JSON 入参用 `json.Decoder` + `DisallowUnknownFields()`，请求体上限 1 MiB；统一 recover 中间件回 500、堆栈只进 stderr 日志；访问日志走 stderr（method/path/status/耗时/管理员，不记密码/token/SQL）。
- **出参脱敏铁律**：datasource 任何响应都不含 password_enc 与解密密码，只给 `has_password bool`；agent 响应不含 api_key_hash，明文 key 只在"创建/轮换"当次响应出现一次；任何响应/日志不得出现 AGENTSQL_SECRET、管理员密码、Agent 明文 key（当次新建除外）。

### 16.3 端点全清单（对齐 3.5，全部在 /api/v1 前缀下，除 login 外都要 admin token）
- agents（先补 `AgentRepository.List(ctx)`，按 created_at 稳定排序）：`GET /agents`、`POST /agents`（level∈readonly/dml/ddl 否则 422，创建即 GenerateAPIKey，明文仅当次返回）、`GET /agents/{id}`、`PUT /agents/{id}`（只改 name/owner/level/status/expires_at，不动 key）、`DELETE /agents/{id}`（该 agent 仍有 policies 时 409、提示先清理，禁止静默级联）、`POST /agents/{id}/rotate-key`（重生成 hash 并返回明文一次，旧 key 立即失效）。
- datasources（复用现有 CRUD 与 DecryptPassword）：`GET /datasources`、`POST`、`GET/{id}`、`PUT/{id}`、`DELETE/{id}`（仍被 policy 引用时 409）、`POST /datasources/{id}/ping`（只做一次轻量探活 `SELECT 1`，经 bootstrap.Runtime.ExecutorFor 取连接，返回 `{ok,latency_ms}` 或安全错误，不回显 DSN/密码；探活接口以可注入的接口形式声明，测试用 fake，不连真实业务库）。
- policies：`GET /policies?agent_id=&datasource_id=`（补 `PolicyRepository.List(ctx)` 全量；带 agent 时复用 ListByAgent、带两者复用 ListByAgentAndDatasource）、`POST /policies`、`PUT /policies/{id}`、`DELETE /policies/{id}`；object_type/action/columns 入参校验对齐 T08 口径。
- rules（补 `RuleRepository.List(ctx, dbType string)`）：`GET /rules?db_type=`、`PUT /rules/{id}`（builtin=1 只允许改 enabled 与 definition 内阈值，禁止改 id/pattern_type）、`POST /rules` 与 `DELETE /rules/{id}` 仅对 builtin=0 开放，对内置规则 403。
- mask_rules（复用现有 CRUD，补一个全量/按数据源 List，ListByDatasource 已有）：`GET /mask_rules?datasource_id=`、`POST`、`PUT/{id}`、`DELETE/{id}`，algo/sensitive_type 走 mask 包合法值校验。
- audit（复用 AuditLogRepository.FilteredPage，query 映射 model.AuditFilter：time_start/time_end/agent_id/datasource_id/session_id/mcp_tool/decisions(逗号分隔多值)/stmt_types(多值)/risk_min/risk_max/keyword/object + page/page_size）：`GET /audit` 分页；`GET /audit/export` 同筛选导出 JSONL（Content-Type application/x-ndjson、Content-Disposition attachment、上限 10000 条，超出用 page_size 分批拉取拼接，及时关 rows）。
- approvals（补 `ApprovalRepository.ListPage(ctx, status string, page, pageSize int)`，按 created_at DESC）：`GET /approvals?status=`、`POST /approvals/{id}/decide`（body `{decision: approve|reject, comment}`，仅 status=pending 可决策否则 409；写入 status/approver(=管理员名)/decided_at；MCP 侧 get_approval_result 轮询自然读到新状态，本单不做主动推送）。

### 16.4 GET /dashboard/summary（严格对齐前端 T18 四区，一次返回快照）
新增只读聚合（在 store 新建 `dashboard_repository.go`，或给 AuditLogRepository 增加聚合方法；**注意 store 的 SQLite SetMaxOpenConns(1)，每个聚合独立查询、遍历完立即 Close rows，严禁 rows 未关再发起第二个查询**）：
- `kpi`：total_requests、blocked(decision=deny)、pending_approvals(approvals.status=pending)、active_agents(agents.status=active)、datasources_total（v0.1 无心跳，online=已配置，字段名注明），以及每个计数相对"上一等长窗口"的环比百分比（无前序数据给 null）。
- `trend_14d`：长度固定 14，每天 `{date(YYYY-MM-DD),total,deny,warn,approve,allow}`，SQL 用 date(ts) 聚合，**无数据的日期由 Go 补 0**，保证前端折线/柱不断点。
- `decision_distribution`：四态 `[{decision,count}]`，和应等于窗口 total。
- `risk_top`：按 rule_hits 展开统计高危规则/SQL 类型 Top5（v0.1 对 rule_hits 文本做拆分计数，注释说明口径局限）。
- `agent_ranking`：被拦次数 Top（agent_id、name、blocked_count，按 blocked 降序）。
- `battle_report`：`{blocked_count, est_rows_saved=SUM(est_rows) WHERE decision='deny'}`，供前端文案"已拦截 N 次，避免约 X 万行风险"。
- 聚合全部只读、不依赖真实业务库；窗口默认近 14 天，可被 ?days= 覆盖（限定 1–90）。

### 16.5 装配：与 T15 共存同一端口（对 T15 最小增量、保证零回归）
- 新建 `internal/adminapi`：handler.go（NewHandler + 路由注册 + 中间件）、auth_token.go（签发/校验）、dto.go（请求/响应结构与脱敏）、dashboard.go（调 store 聚合拼装 summary）、各资源 handler 与 `*_test.go`，doc.go 补包说明。
- `adminapi.NewHandler(deps Deps, logger) (http.Handler,error)`，Deps 含 *bootstrap.Runtime、config.Config、管理员用户名、token 签名密钥派生结果、以及可注入的 datasource 探活接口（默认实现走 Runtime.ExecutorFor）。
- **用 functional option 扩展 T15，不改其既有调用方式**：给 mcpserver 增加 `NewHTTPHandler(rt,cfg,logger,opts ...HTTPOption)` 与 `WithAdminAPI(h http.Handler)`，在 T15 的 mux 上 `mux.Handle("/api/v1/", adminHandler)`；不传 option 时 T15 行为、路由与全部测试保持不变（T15 现有 NewHTTPHandler 三参调用与测试零改动，靠变长参数兼容）。/api/v1/* 走 admin，其余仍由 T15 mux 处理（/mcp、/ 404）。
- cmd serve：从环境读 AGENTSQL_ADMIN_USER/PASSWORD；当 `server.console_enabled=true`（默认 true）时构建 adminHandler 并以 WithAdminAPI 注入；console_enabled=false 时不挂 /api/v1（/mcp 不受影响）。**console 启用但缺 AGENTSQL_ADMIN_PASSWORD 时 serve 必须启动失败、退出码 1（fail-closed）**，并在测试覆盖。

### 16.6 测试（httptest + 内存 SQLite，全部 go test -race 干净；不起真实 PG/MySQL，探活用 fake）
登录正确 200 拿 token、错密码/缺密码环境 401 或启动失败；无/错/过期 admin token →401，且 /mcp 的 Agent key 不能用来访问 /api/v1、反之亦然；统一响应与分页结构、page_size 越界与未知字段 400；agents CRUD 往返、创建/轮换当次返回明文、之后 GET 不含 hash/明文、非法 level 422、删有策略 agent 409；datasources 响应无密码字段、ping fake 成功/失败两条路径；policies/rules/mask_rules CRUD、内置规则不可删 403；audit 多条件筛选分页与 export 的内容类型/行数上限；approvals decide 状态机（pending→approved/rejected、重复决策 409）；dashboard 灌入构造审计数据后断言 KPI 计数正确、trend_14d 长度恒为 14 且缺日补 0、四态分布之和=total、ranking 降序、est_rows_saved 求和正确。**回归铁律：T15 全部 HTTP/stdio 测试与全包既有测试零回归，`go test -race ./...` 零 FAIL、零 DATA RACE；gofmt/vet 为 0。**
### 16.7 边界
不做前端、不做 token 刷新/黑名单/多管理员 RBAC（企业版）、不做 WebSocket 实时推送（T27）、不新增 migration/不改七表 schema、不改 MCP 七工具语义、除标准库外不新增任何第三方依赖、dashboard 与所有 GET 只读。

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

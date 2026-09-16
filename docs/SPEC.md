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
server: { http_listen: "127.0.0.1:7780", console_enabled: true, event_stream: true, event_stream_max_connections: 100 }
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
- **源列兜底（v0.1-RC 收紧，详见 25.5.2）**：在上述“最终结果列名”匹配之外，对顶层 SELECT 的**直接列引用 / 直接别名**（`phone`、`c.phone AS mobile`、`schema.t.phone AS x`），若去限定后的裸源列名命中规则，也对该输出列整列打码；匹配优先级为“最终列名命中优先，未命中再查源列名”的“或”扩充，不改变本段既有行为。函数/表达式/聚合/CAST、UNION 分支、跨子查询/CTE/视图的血缘**不在 v0.1 兜底**，见 25.5.2 边界。

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

### 17.1 目标与边界
搭起控制台的"可运行骨架"并由后端单端口内嵌：能登录、能看到左侧导航+顶栏布局、能在各菜单页之间切换、刷新不掉线、深/浅主题可切换并持久化；为 T18-T22 预留好路由、菜单、API 层、统一组件与主题令牌。**本单只做骨架与登录闭环，不实现 T18 大屏图表、T19 StageFlow、T20 审计表格、T21 演示台、T22 配置业务逻辑**（这些页面本单只建"占位页"）。后端只新增"内嵌静态资源 + SPA 回退"，**T14 stdio、T15 HTTP MCP、T16 管理 API 的行为与全部测试零回归**。

### 17.2 工程与版本（钉死，禁止浮动升级大版本）
- 位置：前端源码全部在仓库根 `web/`；构建产物输出到 `internal/webui/dist` 由 Go 内嵌（go:embed 不能跨 `..`，故产物必须落在 embed.go 同级 dist）。
- 技术栈：Vite5 + React18 + TypeScript 严格模式 + Ant Design 5 + react-router-dom v6 + axios + zustand v4；ECharts5、framer-motion、@ant-design/icons 本单装进 package.json 但**不实际使用**（T18/T19 才用，本单只需保证装得上、build 得过）。**禁止引入 Tailwind、styled-components、redux、react-query、@reduxjs、dayjs 之外的日期库等任何额外依赖。**
- 固定版本（package.json 一律写死精确版本，**不许用 `^`/`~` 浮动**，避免主控端 npm install 解析到 React19/antd6/router7/zustand5 等不兼容大版本）：
  - dependencies：`react@18.3.1` `react-dom@18.3.1` `antd@5.21.6` `@ant-design/icons@5.5.1` `react-router-dom@6.26.2` `axios@1.7.7` `zustand@4.5.5` `echarts@5.5.1` `framer-motion@11.5.4`（antd 自带 dayjs，不另装）
  - devDependencies：`vite@5.4.8` `@vitejs/plugin-react@4.3.1` `typescript@5.6.2` `@types/react@18.3.5` `@types/react-dom@18.3.0`
- scripts：`dev`(vite)、`build`(`tsc --noEmit && vite build`)、`preview`、`typecheck`(`tsc --noEmit`)。
- `vite.config.ts`：`@vitejs/plugin-react`；`base:'/'`；`build.outDir='../internal/webui/dist'`、`emptyOutDir:true`；`server.port=5173`，`server.proxy` 把 `/api` 与 `/mcp` 反代到 `http://127.0.0.1:7780`（changeOrigin:true），实现 `npm run dev` 前后端联调。
- `tsconfig.json` 开 `strict`、`noUnusedLocals`、`noUnusedParameters`、`noImplicitReturns`，`moduleResolution:'bundler'`、`jsx:'react-jsx'`、`target:'ES2021'`、路径别名 `@ -> web/src`（同时配 vite alias 与 tsconfig paths）。
- index.html：`<html lang="zh-CN">`、`<title>AgentSQL 智盾控制台</title>`、挂载点 `#root`，不引任何 CDN（内网/离线可用）。

### 17.3 目录与文件清单（按此创建，命名固定）
```
web/
  package.json  tsconfig.json  tsconfig.node.json  vite.config.ts  index.html
  src/
    main.tsx                 # 挂载 React、包 ConfigProvider(主题) + RouterProvider
    App.tsx                  # 路由表：/login 与受保护布局路由
    vite-env.d.ts
    routes/menu.tsx          # 菜单+路由单一配置源(key/path/label/icon/element)
    theme/tokens.ts          # 5.1 色板/字号/圆角等设计令牌(深+浅两套)
    theme/useThemeStore.ts   # zustand persist：'dark'|'light'，默认 dark，写 localStorage
    store/authStore.ts       # zustand persist：token/expires_at/username + login/logout
    api/client.ts            # axios 实例 + 请求/响应拦截器 + 泛型 request
    api/types.ts             # ApiResponse<T>、PageResp<T>（snake_case，对齐 T16 出参）
    api/{auth,agents,datasources,policies,rules,maskRules,audit,approvals,dashboard}.ts
    layouts/MainLayout.tsx   # 左侧深色 Sider + 顶栏 + <Outlet/>
    layouts/AuthGuard.tsx    # 路由守卫：无有效 token -> /login
    components/PageContainer.tsx       # 统一页容器(标题/副标题/内容区)
    components/PlaceholderPage.tsx     # T18-T22 占位页("本模块 Txx 交付")
    pages/Login.tsx
    pages/{Overview,Audit,Playground,Agents,Datasources,Policies,Rules,Approvals,MaskRules,NotFound}.tsx
internal/webui/
  embed.go                   # //go:embed all:dist + SPA Handler
  dist/index.html            # 手写最小占位(保证未 npm build 时 go build 也能过)，主控 build 后覆盖
```

### 17.4 主题系统（默认深色作战主题）
- `theme/tokens.ts` 固化 5.1 色板：深色背景 `#0d1421/#142033/#1c2b42`、描边 `#2a3b55`；品牌青蓝 浅 `#1677ff`/深 `#3b9eff`；语义 放行`#52c41a` 告警`#faad14` 审批`#fa8c16` 拦截`#f5222d`；字体 中文系统黑体栈、SQL/数字等宽 `JetBrains Mono, Consolas, monospace`。
- 用 AntD5 `ConfigProvider` + `theme.darkAlgorithm/defaultAlgorithm` 切换；同时把品牌色/语义色注入 `:root` CSS 变量供自定义组件用。`useThemeStore` 用 zustand `persist` 存 localStorage key `agentsql.theme`，**默认 dark**；顶栏放切换器（图标按钮 Moon/Sun），切换即时生效、刷新保持。
- 全局做一层背景：深色时整页 `#0d1421`、内容卡片 `#142033`+1px `#2a3b55` 描边、克制无重阴影；浅色 `#f5f7fa`。

### 17.5 登录页与认证态（门面第一印象，禁用 AntD 默认登录页）
- 路由 `/login`，**深色居中卡片**：居中产品标识"AgentSQL 智盾"、slogan"AI 原生数据库安全网关 · 让每一次模型访问都可控、可审计"、用户名/密码输入框、登录按钮（loading 态）、整页 subtle 网格背景（**纯 CSS 渐变/linear-gradient 网格线实现，不引图片、不引 Lottie**）。
- 提交调 `POST /api/v1/auth/login`（body `{username,password}`）；成功后把 `data.token`、`data.expires_at` 存 authStore，并调一次 `GET /api/v1/auth/me` 取 username 存档，跳 `/`；失败用 antd message 显示后端 msg，不弹原生 alert。
- authStore（zustand+persist，localStorage key `agentsql.auth`）暴露 `token/username/expiresAt/isAuthenticated/login()/logout()/clear()`；**token 只存 localStorage，不写 cookie、不打 console.log、不放 URL**。

### 17.6 布局、路由与守卫
- MainLayout：左侧深色 Sider（可折叠，logo 区"AgentSQL 智盾"，菜单由 `routes/menu.tsx` 单一配置驱动，图标用 @ant-design/icons），菜单项与路由**一次性铺齐 T18-T22**：总览 `/`(DashboardOutlined)、审计 `/audit`(FileSearchOutlined)、演示台 `/playground`(ExperimentOutlined)、Agent `/agents`(RobotOutlined)、数据源 `/datasources`(DatabaseOutlined)、权限 `/policies`(SafetyOutlined)、规则 `/rules`(FilterOutlined)、审批 `/approvals`(AuditOutlined)、脱敏 `/mask-rules`(EyeInvisibleOutlined)。
- 顶栏：左侧折叠按钮+当前页面名，右侧主题切换器、当前管理员 username（来自 authStore/me）、退出按钮（调 `POST /api/v1/auth/logout`，无论成败都清本地登录态并回 /login）。
- AuthGuard：受保护路由包在 MainLayout 下；`!isAuthenticated`（无 token 或已过 expires_at）重定向 `/login`；已登录访问 `/login` 重定向 `/`；`*` 回 NotFound。**刷新页面从 localStorage 恢复登录态不掉线**；应用启动时若有 token 则静默 GET /auth/me 校验，401 即 clear 并跳登录。
- T17 除 Login/Overview 外，其余 9 个页面统一用 PlaceholderPage（显示页面名+"建设中，将在 T18-T22 交付"），保证菜单可点、路由可达、不报错；Overview 放一张欢迎卡 + 调 /auth/me 显示"当前管理员：xxx"，证明鉴权链路通。

### 17.7 axios 客户端与 API 层
- `api/client.ts`：`axios.create({baseURL:'/api/v1', timeout:15000})`；请求拦截器自动加 `Authorization: Bearer <token>`；响应拦截器统一解包——后端体形如 `{code,msg,data}`，HTTP 2xx 且 `code===0` 返回 `data`，否则 reject 一个带 msg 的 Error；HTTP 401 一律清登录态并跳 `/login`；其余非 2xx/业务错误用 antd `message.error(msg)` 统一提示（返回 422 校验错误时透传 msg）。导出泛型 `request<T>(...)`，所有 api 模块强类型返回 `Promise<T>`。
- `api/types.ts` 按 T16 出参定义 `ApiResponse<T>`、`PageResp<T>{total:number;page:number;page_size:number;list:T[]}` 及各 View 的 **snake_case** TS 接口（AgentView 含 api_key 仅创建时、DatasourceView 用 has_password 无密码、AuditView/ApprovalView/PolicyView/RuleView/MaskRuleView/DashboardSummary 字段对齐 internal/adminapi/dto.go 与 internal/store/dashboard_repository.go 的 json tag——**先读这些 Go 文件照抄 json 名，禁止凭空造字段**）。
- 本单只把 `auth.ts`（login/me/logout）接到真实后端；agents/datasources/policies/rules/maskRules/audit/approvals/dashboard 这 8 个模块**只定义函数签名、入参/返回类型和正确路径（路径严格照抄 17.8 后端 32 路由），函数体写好 request 调用但页面暂不调用**，保证 tsc 零错误，留给 T18/T22 联调。

### 17.8 后端内嵌（go:embed + SPA 回退，对 T14/T15/T16 零回归）
- 新增 `internal/webui/embed.go`：`//go:embed all:dist` 嵌入 `dist` 子树；导出 `func Handler() (http.Handler, error)`，用 `http.FS` 提供静态文件，并实现 **SPA 回退**：请求路径在 dist 中存在对应文件（如 `/assets/index-xxx.js`）则返回该文件；不存在的非 API 路径（前端路由如 `/audit`）一律 200 返回 `dist/index.html`，交给 react-router。给 index.html 加 `Cache-Control: no-cache`、带哈希的 `/assets/*` 加长缓存（T17 可简化，T24 精修）。
- **embed 铁律**：`//go:embed all:dist` 要求编译时 `internal/webui/dist` 目录存在且非空，否则 `go build ./...` 直接失败。因此必须随代码**手写提交一个最小 `internal/webui/dist/index.html` 占位页**（"AgentSQL console building…"），主控端 `npm run build` 会用真实产物覆盖它。
- `internal/mcpserver/http.go` 仿 T16 的 `WithAdminAPI` 再加一个 functional option `WithWebConsole(handler http.Handler) HTTPOption`，在 newHTTPHandlerWithRegistry 里 `mux.Handle("/", webHandler)`（仅当 option 非 nil）。Go1.22 ServeMux 中 `/mcp`、`/api/v1/` 比 `/` 更具体、优先匹配，因此**前端兜底绝不拦截 MCP 与管理 API**；不传 option 时（如测试）行为与现在完全一致。
- `cmd/agentsql/main.go` 的 serve：构建 webui.Handler 并追加 `mcpserver.WithWebConsole(...)` 到 httpOptions（与 WithAdminAPI 并列；console_enabled 关闭时不挂管理 API，但静态控制台是否挂载跟随同一开关或单独常量——本单统一为：console_enabled=true 时同时挂管理 API 与控制台静态资源）。stdio 命令 `agentsql mcp` 不起 HTTP、不涉及前端，保持不变。
- 给 webui 写单元测试：命中存在资源返回 200 且 Content-Type 正确；前端路由路径回退到 index.html(200)；不与 /api、/mcp 冲突的判断由 mcpserver 既有测试保证。

### 17.9 单位端（Codex）硬约束——无 Node 环境
- 单位机**没有 Node/npm/npx/tsc，严禁运行 `npm install`、`npm run build`、`npx create-vite`、`tsc` 等任何命令**（必失败，不要反复重试、不要因此改设计）；也**不要生成 package-lock.json**（锁文件由主控端 npm install 后生成提交）。
- 只交付：全部前端源码文本、package.json（版本写死）、配置文件、Go 的 embed.go/option/cmd 改动、手写占位 `internal/webui/dist/index.html`。
- 必须做**严格文本级静态自审**：TS 类型闭合、import 路径与别名一致、AntD5/v6 Router/zustand4/axios 的 API 用法正确（注意 antd5 不再需要 import 'antd/dist/xxx.css'、RouterProvider/createBrowserRouter 或 `<BrowserRouter>` 二选一并自洽、zustand4 persist 写法），交付说明里明确写"**未本地构建，需主控端执行 npm install + npm run build 验证**"。

### 17.10 主控端验收门（豆包本机执行，Codex 不做）
1. `cd web && npm install`（首次生成并提交 package-lock.json）→ `npm run typecheck` 0 错 → `npm run build` 成功，产物确实落到 `internal/webui/dist`（index.html + assets/，占位被覆盖）。
2. 后端 `gofmt -l` 空、`go vet ./...`=0、`go build ./...`=0；`go test -race ./...` 全绿，T14/T15/T16 零回归，新增 webui 测试通过。
3. 真实 serve 黑盒：`GET /` 返回构建后 index.html(200)；`GET /assets/<hash>.js` 200；`GET /audit` 这类前端深链也回 index.html(200)；`GET /api/v1/auth/me` 无 token 仍 401、`POST /mcp` 不受 `/` 兜底影响。
4. 浏览器手测（用户肉眼确认门面）：登录页深色网格质感；admin/Admin@12345 登录进总览并显示当前管理员；左侧菜单逐页可切；F5 刷新不掉线；深/浅主题切换后刷新仍保持；退出回登录页；未登录直接访问 /agents 被重定向到 /login。

### 17.11 本单明确不做
不做任何 ECharts 图表/动画（只装依赖）；不接 agents/datasources 等业务数据渲染（只留强类型 API 函数）；不做 StageFlow；不做登录页以外的精致视觉（T18/T20 精修）；不做用户/多管理员/RBAC/SSO；不引任何 17.2 之外的依赖；不改七表、不改 MCP 七工具语义、不改 T16 已冻结的响应契约。

## T18 总览大屏（深色作战中心，门面，精做）

### 18.1 目标与边界
把 T17 的 Overview 占位页替换为"深色作战中心"总览大屏，这是产品截图、宣讲、Demo 的第一门面，视觉必须达到"可直接截图发技术群/放进 PPT"的水准。**纯前端单：只改 `web/src`，不改任何 Go 代码、不改七表、不改 T16 已冻结响应契约、不动其它 8 个页面**。ECharts 与 framer-motion 在 T17 已装好，本单第一次真正使用，**禁止新增任何 npm 依赖**（不引 echarts-for-react、recharts、dayjs、number-animated 等，数字滚动用 framer-motion 或原生 requestAnimationFrame 自写 hook）。

### 18.2 数据来源（严格对齐 T16，字段名照抄 web/src/api/types.ts，禁止臆造）
- 主快照：`getDashboardSummary(days)` → `GET /api/v1/dashboard/summary?days=N`（N∈{7,14,30}，默认 14，后端合法区间 1–90），返回 `DashboardSummary{kpi,trend_14d,decision_distribution,risk_top,agent_ranking,battle_report}`。**注意 trend_14d 是历史命名，实际长度=days（后端已按天补零、日期升序），直接按返回数组渲染，不要写死 14 个槽位。**
- 实时事件流：`listAudit({page:1,page_size:20})` → `GET /api/v1/audit?...`，返回 `PageResp<AuditView>`，取 `list`（后端已按 created/id 倒序）。
- 字段口径：KPI 环比 `total_requests_change_pct/blocked_change_pct` 为 `number|null`（前值为 0 时后端给 null，UI 显示"—"而非 Infinity/NaN）；`decision_distribution` 固定顺序 allow/deny/approve/warn；`risk_top/agent_ranking` 最多 5 条、可能为空数组；`battle_report.est_rows_saved` 是被拦截语句的预估扫描行合计。

### 18.3 整体布局（复用 T17 的 PageContainer 外壳与主题令牌，深色）
自上而下、栅格自适应（AntD Row/Col，xl 断点多列、窄屏自动堆叠，禁止写死列数）：
1. **控件条**（PageContainer 的 extra 区或页面顶部一条）：时间范围 Segmented（近7天/近14天/近30天，对应 days=7/14/30）、自动刷新 Switch（默认开，30s）、"立即刷新"按钮（带 loading）、全屏按钮（Fullscreen API 对大屏根容器切换，兼容退出）。
2. **第一行 5 张 KPI 卡**（见 18.4）。
3. **第二行**：左 Col(xl=16) 趋势双轴图（18.5），右 Col(xl=8) 决策占比环图（18.6）。
4. **第三行**：左 Col(xl=16) 实时风险事件流（18.7），右 Col(xl=8) 上下叠放"拦截战报卡 + Agent 被拦排行 + 高危规则 Top5"（18.8）。
卡片统一用 T17 tokens 的深色 surface `#142033`、1px `#2a3b55` 描边、圆角 8–12、克制阴影；卡内标题 13–14px 次要文本色，内容主文本色；数字与 SQL 用等宽字体。

### 18.4 五张 KPI 卡（数字滚动 + 环比）
依次：总请求(total_requests)、拦截(blocked，**红色语义**)、待审批(pending_approvals，橙色)、活跃 Agent(active_agents)、在线数据源(datasources_total)。每卡：图标 + 中文标签 + 大号数值（进入与切换 days 时用自写 useCountUp 从旧值缓动到新值，600–800ms，千分位 toLocaleString）+ 环比小标（总请求/拦截各带 change_pct：正升红/绿按语义——拦截上升用红色↑、总请求上升用品牌色↑；null 显示"环比 —"；下降↓）。数值为 0 也要正常显示，不出 NaN。

### 18.5 近 N 天趋势图（ECharts 双轴：四态堆叠柱 + 拦截折线）
- x 轴 = trend_14d[].date（MM-DD）；左轴柱：按 allow/warn/approve/deny **堆叠柱状**，四态颜色严格用 tokens 语义（allow `#52c41a`、warn `#faad14`、approve `#fa8c16`、deny `#f5222d`，深色下适当提亮保证对比）；右轴叠加一条 deny 拦截红色折线（突出拦截趋势）。堆叠顺序固定 allow→warn→approve→deny，legend 中文（放行/告警/待审批/拦截 + 拦截趋势线）。
- tooltip 十字准星、深色底、显示当日总数与各态数值；grid 紧凑防标签溢出；**容器用 ResizeObserver 跟随侧栏折叠/窗口变化 resize，组件卸载必须 dispose() 释放实例**。
- 全部为 0 时不报错，显示"暂无数据"空态柱区。

### 18.6 决策占比环图（ECharts donut）
用 decision_distribution 四态做环形图，中心显示总次数；右侧或下方 legend 带中文标签 + 次数 + 百分比；颜色与 18.5 完全一致；总和为 0 时环图区显示 Empty 空态（"当前区间暂无决策记录"），不画满一圈、不显示 NaN%。

### 18.7 实时风险事件流（轮询，不做 WebSocket）
- 首次进入立即拉一次 `listAudit({page:1,page_size:20})`；自动刷新开启时每 30s 拉一次，**用"上一次请求未返回就不发下一次"的守卫避免堆叠**；切走页面（unmount）必须清掉定时器与在途请求（AbortController/忽略过期响应）。
- 列表按时间倒序，每条一行：时间(HH:mm:ss)、Agent（agent_id 截短或 name，缺省"—"）、决策 Tag（四态中文+语义色，**deny 整行左侧红色边条/浅红底，新出现的 deny 用 framer-motion 从顶部滑入并红色高亮 2s 后回归常态**）、语句类型 stmt_type、SQL 摘要（sql_raw 单行截断 + ellipsis，鼠标 Tooltip 看全，等宽小字）。最多保留 20 条 DOM。
- 自动刷新 Switch 关闭则停止轮询但保留当前列表；手动刷新按钮立即拉取并给顶部按钮 loading。轮询失败只在角落 message 轻提示，不清空已有列表（错态不白屏）。

### 18.8 战报卡 / Agent 被拦排行 / 高危规则 Top5
- **拦截战报卡**：突出展示"已拦截 **{battle_report.blocked_count}** 次 · 避免约 **{格式化 est_rows_saved}** 行风险扫描"，行数 ≥10000 显示"x.x 万行"（如 123456→12.3 万），用强对比数字 + 盾牌图标，做成全页最有"讲故事"张力的一张卡。
- **Agent 被拦排行**：agent_ranking 横向条形（名称 + blocked_count），name 为空时回退显示 agent_id 短码；空数组显示"暂无被拦截 Agent"。
- **高危规则 Top5**：risk_top 显示规则中文名 + 次数。**新增 `web/src/constants/ruleMeta.ts`：通读 internal/rules/generic.go、postgres.go、mysql.go，把全部内置规则（R0xx 通用 / R1xx PostgreSQL / R2xx MySQL）的 ID 整理成 `{id,title(简短中文),risk}` 映射**（规则没有内建 title，依据各规则 Eval 语义与 Message 概括，交付说明必须列出整张 id→中文对照表供主控逐条核对）；运行时查不到映射就回退显示原始 rule_id，绝不显示 undefined。
- 同时新增 `web/src/constants/labels.ts`：导出 decisionMeta（allow/deny/warn/approve → {中文label,主题色token名,AntD Tag color}）与 stmtType 中文映射，供本单与 T20 复用，颜色统一从 tokens 取，不在组件里散落硬编码色值。

### 18.9 三态、主题与健壮性（硬指标）
- 三态齐全：首次加载用 Skeleton/Spin 骨架；接口或数组为空用 AntD Empty 且文案友好（新部署空库也必须好看）；请求失败显示错误态 + "重试"按钮，不白屏、不把异常对象直接打印给用户。
- 全程跟随 T17 全局深/浅主题：ECharts 轴/图例/tooltip/坐标线文字色、分割线色随主题切换（监听 useThemeStore 的 mode 变化重新 setOption），浅色下文字仍清晰；不在 ECharts 里写死白底。
- 所有数字、百分比做容错（null/undefined/NaN/Infinity 全部兜底）；时间用原生或 antd 自带 dayjs 格式化，不另装库；列表 key 用稳定 id。

### 18.10 ECharts 使用约束
从已装的 `echarts@5.5.1` 引入（可用 `import * as echarts from 'echarts'` 全量以降低本单复杂度，bundle 体积 warning 已知、T24 再做按需/路由懒加载优化）；每个图封装成独立组件（如 TrendChart、DecisionDonut），用 useRef 拿 DOM、useEffect 里 init/setOption、窗口或容器尺寸变化 resize、卸载 dispose；**禁止**用 setInterval 无脑重 init（只在数据/主题变化时 setOption）。

### 18.11 单位端（Codex）硬约束——同 T17，无 Node
单位机没有 Node/npm/npx/tsc，**严禁运行 npm install/build/npx/tsc，不要改 package.json、不要生成 lock**；也不运行 go 命令（本单不改 Go）。只产出/修改 web/src 下源码文本，做严格文本级静态自审（TS strict 零 any、零未用变量、import 路径用 @ 别名、ECharts option 类型闭合、hooks 依赖与清理正确），交付开头注明"未本地构建，需主控端 npm run typecheck + build 验证"。

### 18.12 主控端验收门（豆包本机）
1. `cd web && npm run typecheck` 0 错、`npm run build` 成功且产物仍落 internal/webui/dist（Go 侧零改动，无需重新写 Go，但要确认 go:embed 仍能 build、`go test -race ./...` 零回归）。
2. 真实 serve：主控向元数据库注入一批覆盖四态、跨 7/14/30 天、含不同 rule_hits/agent 的演示 audit_logs（或经管理 API 造数），验证 5 KPI 数字滚动与环比、趋势堆叠+红线、环图、事件流轮询与 deny 滑入高亮、战报/排行/Top5 中文规则名全部正确渲染。
3. 空库场景：清空 audit_logs 后各区块为友好空态、无报错/NaN。
4. 交互：7/14/30 切换数据随之变化；自动刷新开关/手动刷新/全屏可用；折叠侧栏图表自适应；深/浅主题切换图表配色同步；F5 正常。
5. 视觉达到可截图发群水准（用户肉眼最终拍板）。

### 18.13 本单明确不做
不做 T19 StageFlow、不改审计/配置等其它页面（仍保持 T17 占位）；不做自定义日期区间选择器（只 7/14/30 快捷段）；不做 WebSocket/SSE 全局推送（T27+）；不做导出/PDF；不新增依赖、不改 Go、不改七表与 T16 契约；不向后端写数据（总览是只读页）。

## T19 六段安检流组件 StageFlow（灵魂组件·单条受控播放版）

### 19.1 目标与定位
做一个**可复用、受控、零后端依赖**的"一条 SQL 如何被层层把关"动画组件 StageFlow：发光点沿六段流水线从左向右推进，逐段点亮、显示结论与耗时，最终按四态+错误收束。它是 T20 审计详情抽屉、T21 拦截演示台共同复用的核心叙事组件，也是给客户/同行讲产品价值最直观的一块。本单**只做前端组件 + 一个静态样例自测区**，不联任何后端接口（"只校验不执行返回 Assessment"的 HTTP 接口 T21 才由后端补，本单不碰 Go）。

### 19.2 六节点与后端八阶段映射（顺序钉死，必须与 T13 真实执行顺序一致）
后端 T13 Process 是八阶段、StageLatency 的 key 固定为 `auth/load/parse/guard_static/guard_dynamic/execute/redact/audit`（见 internal/pipeline/types.go）。**安全语义是"先鉴权后解析"（认证失败不解析、不连库），因此前端节点顺序绝不能写成 Parse→Auth**。统一收敛为六节点，展示顺序与映射如下，组件内写死、不允许调用方乱序：

| # | StageKey | 中文 | 图标语义 | 合并的后端 StageLatency | 含义 |
|---|---|---|---|---|---|
| 1 | `auth` | 鉴权 | 钥匙/盾牌 | `auth`+`load` | 校验 Agent API Key、加载数据源与权限策略 |
| 2 | `parse` | 解析 | 代码/括号 | `parse` | SQL→AST，畸形/多语句在此 fail-closed |
| 3 | `guard` | 安检 | 滤网 | `guard_static`+`guard_dynamic` | 静态规则第一闸 + 只读 EXPLAIN 动态第二闸 |
| 4 | `decide` | 决策 | 天平 | 无（逻辑节点，耗时记 0、显示"—"） | 按 deny>approve>warn>allow 定最终四态 |
| 5 | `execute` | 执行 | 闪电/圆柱 | `execute` | 仅 allow/warn 受控执行；deny/approve 绝不触库 |
| 6 | `audit` | 留痕 | 文档/对勾 | `redact`+`audit` | 结果集脱敏、同步审计落库（**拦截/报错也照样留痕**） |

节点耗时为所合并 key 之和，缺 key 按 0；`decide` 不显示耗时。提供纯函数 `adaptAssessment` 把未来后端 Assessment（Decision/Hits/StageLatency/EstScanRows）映射成本组件数据，T21 直接复用。

### 19.3 受控数据契约（components/stageflow/types.ts，严格 TS、禁 any）
```ts
export type StageKey = 'auth'|'parse'|'guard'|'decide'|'execute'|'audit';
export type StageStatus = 'pending'|'active'|'pass'|'warn'|'block'|'skip'|'locked';
export type FlowDecision = 'allow'|'warn'|'approve'|'deny'|'error';
export interface StageStep { key: StageKey; label: string; status: StageStatus;
  latencyMs?: number; note?: string; }
export interface StageVerdictData { ruleId?: string; title?: string;
  message: string; suggestion?: string; risk?: number; }
export interface StageFlowData {
  decision: FlowDecision;
  steps: StageStep[];          // 必须且仅 6 个,顺序同 19.2
  blockAt?: StageKey;          // 拦截/错误中止节点
  verdict?: StageVerdictData;  // 拦截/审批判词
  rowsReturned?: number;       // 放行返回行数
  totalLatencyMs?: number;
  errorMsg?: string;
}
export interface StageFlowProps {
  data: StageFlowData;
  autoPlay?: boolean;          // 默认 true
  replayKey?: number|string;   // 变化即从头重播
  dense?: boolean;             // T20 抽屉紧凑态
  onStepChange?: (key: StageKey, index: number) => void;
  onFinish?: (decision: FlowDecision) => void;
}
```

### 19.4 节点状态机与视觉（颜色一律取 theme/tokens 与 constants/labels，禁散落硬编码色值）
- `pending` 未到：灰、低饱和、连线未点亮；`active` 进行中：品牌色描边脉冲发光、流动点恰好到达；`pass` 通过：绿勾；`warn` 告警通过：黄/橙感叹号；`block` 命中拦截/错误：红叉 + 轻微左右震动；`skip` 未执行：灰虚线 + "未执行"；`locked` 待审批解锁：橙虚线 + 小锁。
- 每个节点：圆形/圆角图标 + 中文节点名 + 一行 `note` 小字 + 耗时（`1.2ms`，decide 与无耗时显示"—"）。节点间用 SVG 连线，已走过段着色、未走段灰。

### 19.5 播放时序与动画（framer-motion + 轻量 SVG，禁 lottie/gsap/d3 等重型库）
- autoPlay 后从 steps[0] 起：节点先 active，一个发光圆点沿该节点到下一节点的连线移动；单段停留时长 `clamp(round(latencyMs),300,900)`，无 latency 用 360ms，保证总时长可控、不被真实耗时拖慢。
- 走到 `blockAt`：该节点 active→block、震动一次（x 方向 3 次小幅位移、0.25s 内结束）、流动点停在此处不再前进；其后节点**不再播放流动动画**，直接按 steps 给定终态渲染（见 19.6）。
- 全部 pass：末端汇总"返回 N 行 · 总耗时 X ms"。提供"重播"按钮（也可由 replayKey 触发）；组件卸载必须清理所有 rAF/setTimeout/订阅，切走页面无残留定时器、无 setState after unmount。

### 19.6 五种终态表现（stageSamples 各做一个，全部要能播）
- **allow 放行**：六节点依次转绿，末端绿色"已放行 · 返回 128 行"。
- **deny 拦截（静态规则命中，blockAt=guard）**：auth/parse 绿 → guard 变红震动、流动中断 → decide 静态标红"拦截" → execute=skip 灰锁（**零触库**）→ audit 仍 pass（留痕这次拦截，体现"拦截也审计"）；下方 StageVerdict 显示 R002 中文名+判词+改写建议。
- **approve 转人工审批（blockAt=decide）**：guard 黄 → decide 橙"转人工审批" → execute=locked 橙锁（待审批后才执行）→ audit pass（已落审批待办）；verdict 显示 R004 大范围扫描审批。
- **warn 告警放行**：整条走完，guard/decide 为 warn 黄，execute/audit 绿，末端"已放行，请注意结果集规模"。
- **error 错误（blockAt=parse）**：auth 绿 → parse 红震动显示 errorMsg，其后全部 skip，audit 记录该错误。

### 19.7 判词卡 StageVerdict
独立组件，输入 StageVerdictData：顶部风险色条 + 规则徽标（ruleId 经 `getRuleMeta` 出中文名，查不到回退原 id）+ 判词 message + "改写建议" suggestion（有则展示、前置灯泡图标）；deny 红、approve 橙、error 灰红三色系，供 T20/T21 复用。

### 19.8 响应式 / 主题 / 无障碍
- 宽屏横向六节点一条线；容器宽度不足（断点可参考 lg）自动转纵向时间线（节点在上、连线竖向），不得横向溢出或重叠；用 ResizeObserver 跟随侧栏折叠与窗口变化重排。
- 监听 useThemeStore.mode，深/浅主题下节点、连线、文字都清晰、对比度足够。
- 读取 `prefers-reduced-motion: reduce`，命中则关闭流动与震动、直接按终态顺序呈现（无障碍降级）；图标同时配合文字与颜色，不只靠颜色区分状态。

### 19.9 文件清单（只在 web/src 内新增/修改）
- 新增 `components/stageflow/types.ts`、`stageNodes.tsx`（六节点元数据与固定顺序、图标）、`adaptAssessment.ts`（八阶段→六节点纯映射 + Hits→verdict + 终态推断，导出可被 T21 直接调用的纯函数并在文件内给少量断言性注释样例）、`StageFlow.tsx`（主组件，含 SVG 连线/流动点）、`StageVerdict.tsx`、`stageSamples.ts`（19.6 五个静态样例，数据要真实可信、SQL 与判词贴合内置规则语义）。
- 修改 `pages/Playground.tsx`：把 T17 占位替换为"StageFlow 组件预览（自测）"区——顶部样例切换（放行/拦截/审批/告警/错误五个按钮或 Segmented）+ 重播，中间渲染 `<StageFlow data={sample}/>`，下方展示该样例对应的 StageFlowData JSON（折叠 `<details>`，便于主控核对）；**页面顶部用 Alert 注明"组件预览·T21 将接入真实模拟 AI 请求"**。
- 复用 `constants/ruleMeta.ts`、`constants/labels.ts`、`theme/tokens`、`components/PageContainer`；其余 8 个页面与 T18 大屏一律不动。

### 19.10 静态自审铁律（单位无 Node/Go，同 T17/T18）
严禁 npm/npx/tsc/build/go 命令（必失败、勿重试、不改设计）；不改 package.json、不新增任何依赖（framer-motion 11.5.4 已装、图标用 @ant-design/icons）；不改任何 Go、不改七表。TS strict 零 any、零未用变量、`@/` 别名导入；所有动画定时器/监听器/Observers 卸载清理；null/undefined/缺 latency/缺 verdict 全部兜底不崩。交付开头注明"未本地构建，需主控 typecheck+build 验证"。

### 19.11 主控验收门
npm typecheck/build 0 错且产物落 internal/webui/dist；Go 零改动、`go test -race ./...` 零回归；起 serve 在浏览器"演示台"菜单（Playground 预览区）逐个播放五个样例：放行全绿显行数、deny 在 guard 红震中断且 execute 灰锁、approve 在 decide 橙锁、warn 黄、error 在 parse 红；重播可用、切样例不串状态、窄屏转纵向、深浅主题清晰、开 reduced-motion 直接呈终态；卸载无泄漏、控制台零报错；动效达到"给客户讲一条 SQL 怎么被层层拦下"的演示级质感（用户肉眼拍板）。

### 19.12 本单明确不做
不改 Go、不加后端校验/explain 接口（T21）、不持久化 StageLatency 到七表（v1.x 再议）；不做 T20 审计表格与详情抽屉、不做 T21 的 SQL 输入框/6 剧本真实业务/调接口（本单 Playground 仅静态样例预览，T21 重构该页）；不做 WebSocket/SSE 全局实时流（T27+）；不引 lottie/gsap/d3 等新依赖；不动其余 8 个页面与 T18 大屏；不做多条请求并发流或全局拓扑图。

## T20 审计页 + 证据链时间线（门面，精做，纯前端不改 Go）

### 20.1 目标与数据来源（后端 T16 已全部就绪，本单只做前端）
把 T17 占位的"审计"页做成可截图、可给客户讲"每一次模型访问都追得回"的门面页：顶部多条件筛选、中间高吞吐表格、右侧"调查报告式"详情抽屉（复用 T19 StageFlow）、按筛选导出 JSONL。**不新增/不改任何 Go 接口、不加 package.json 依赖**（dayjs 是 antd 自带依赖，直接 import，不写进 package.json）。全部走现成接口：
- 列表 `GET /api/v1/audit`，query 见 `AuditQuery`（page、page_size、time_start、time_end、agent_id、datasource_id、session_id、mcp_tool、decisions、stmt_types、risk_min、risk_max、keyword、object），返回 `PageResp<AuditView>`（`{total,page,page_size,list}`，已按 ts DESC,id DESC 倒序）；**page_size 后端硬上限 100**。
- 导出 `GET /api/v1/audit/export`（同样筛选、不带分页），返回 `application/x-ndjson` 附件，**后端上限 10000 行，超出返回 422**，前端用既有 `exportAudit()` 拿 Blob 下载。
- 下拉来源：`listAgents({page_size:100})`、`listDatasources({page_size:100})`（均 PageResp，取 list 的 id+name，数据源可带 db_type）。
- 行结构 `AuditView`（snake_case）：id/ts/agent_id/datasource_id/session_id/conversation_id/mcp_tool/db_type/sql_raw/sql_norm/stmt_type/objects/decision/rule_hits(**JSON 字符串**)/risk_level/est_rows/rows_returned/latency_ms/client_ip/model_name/error_msg。

### 20.2 页面整体布局
PageContainer 包裹，自上而下：①可折叠筛选栏 AuditFilters；②工具条（左侧"共 N 条"+手动刷新，右侧"导出 JSONL""导出 PDF(占位)"）；③AuditTable 表格区（固定表头、固定操作列、纵向虚拟滚动，高度自适应剩余视口）；④分页条（总数、每页条数 20/50/100、上下页与跳页，服务端分页）；⑤右侧 AuditDetailDrawer 详情抽屉。深/浅主题同步 tokens，加载/空/错误三态齐全。

### 20.3 筛选栏（字段→query 映射钉死，值为空就不传该参数）
- 时间范围：antd DatePicker.RangePicker（showTime，dayjs）；确认查询时 time_start=起始.toISOString()、time_end=结束.toISOString()（RFC3339，UTC，后端 time.Parse(time.RFC3339)）。
- Agent：Select（options 来自 listAgents，显示 name、值为 id，可清空、支持搜索）；数据源同理（listDatasources）。
- 决策：多选 allow/deny/warn/approve/error，提交时 join(",") 赋给 decisions（**注意 decisionMeta 只定义了前四态，error 用 20.4 的兜底色**）。
- 语句类型：多选，选项取 constants/labels 的 stmtTypeLabels 九类（SELECT/INSERT/UPDATE/DELETE/MERGE/DDL/ADMIN/TRANSACTION/UNKNOWN），join(",") 给 stmt_types。
- 风险等级：区间选择 1–5（两个 Select 或 Slider range），分别给 risk_min/risk_max，做 min≤max 校验。
- 对象 object：输入框（模糊匹配 objects）；关键词 keyword：输入框（匹配 SQL 文本，后端已占位符防注入）。
- 按钮：查询（应用筛选并回到第 1 页）、重置（清空全部条件并重新拉第 1 页）、折叠/展开"更多条件"。筛选项变化到点击查询前不自动发请求（避免抖动），但分页/每页条数变化立即按当前已应用条件请求。

### 20.4 表格列与渲染（服务端分页 + 单页虚拟滚动，不一次性拉一万行）
列：时间(ts 本地时区 `MM-DD HH:mm:ss.SSS`，可悬浮看完整)、Agent(agent_id，缺失显"—")、数据源(datasource_id)、类型(stmt_type 用 statementLabel 中文 Tag)、决策(decision 用 getDecisionMeta 出彩色 Tag；**allow/deny/warn/approve 之外的值（含 error）兜底为中性灰红 Tag、文字取原值、绝不报错**)、风险(risk_level 用 1–5 小徽标/星点，空显"—")、SQL 摘要(sql_raw 单行截断+省略号，title 悬浮全文，deny/error 行 SQL 用语义色弱高亮)、预估行(est_rows 千分位)、行数(rows_returned)、耗时(latency_ms 带 ms)、会话(session_id 短码)、操作("查看"打开抽屉)。
- **性能策略（专业做法，照此实现，不要走偏）**：后端单页最多 100 行，故采用**服务端分页**（数据库 idx_audit_ts/agent/decision 索引支撑，1 万行乃至 10 万行都不占前端内存），表格在当前页内开 antd Table 虚拟滚动（`virtual` + 固定 `scroll.y` + 固定行高/ellipsis）保证单页 100 行滚动不掉帧。**禁止**前端循环拉全部 1 万行再做"假全量虚拟列表"。
- 翻页/切每页条数/重查都带 AbortController，旧请求作废（参考 T18 EventStream 的 controller 守卫，竞态时只认最后一次）；行 key 用 id。

### 20.5 详情抽屉（"调查报告"式分区，antd Drawer，宽 600–720、窄屏自适应）
按以下顺序排，分区标题清晰、信息密度专业：
1. **结论条**：顶部一条决策色横条 + 决策中文 Tag + 风险等级 + 时间 + 记录 id。
2. **基本信息**：Descriptions 两列——Agent、数据源、数据库类型(db_type)、MCP 工具(mcp_tool)、会话 session_id、对话 conversation_id、客户端 IP、模型 model_name（缺项显"—"）。
3. **六段安检时间线**：复用 T19 `<StageFlow data={...} dense />`（适配见 20.6），呈现该请求六节点终态；审计记录无逐阶段耗时，节点耗时统一显示"—"，不编造数字。
4. **SQL 原文 / 归一化双栏（上下两块也行，窄屏堆叠）**：等宽字体，左/上 sql_raw、右/下 sql_norm；做**轻量手写语法着色**（关键字/字符串/数字/注释上不同色），**禁止引 prism/highlight.js/d3 等任何库**；提供"复制原文/复制归一化"小按钮（navigator.clipboard 带降级）。
5. **命中规则判词**：rule_hits 解析后**逐条**用 T19 `<StageVerdict>` 渲染（Rxxx 徽标+中文名+判词+改写建议）；无命中显"未命中规则，正常放行"。
6. **执行评估（据实，不造假）**：用徽标/统计块展示真实存在的 est_rows 预估扫描行、rows_returned 实际返回、latency_ms 总耗时、stmt_type、objects 涉及对象；**audit 表没有 EXPLAIN 成本/索引明细，本单不展示索引/成本，不允许编造**，可在该块底部小字标注"详细 EXPLAIN 计划留存规划于 v1.1"。
7. **错误信息**：仅当 error_msg 非空时，红色 Alert 展示。

### 20.6 审计记录 → StageFlowData 轻量适配（新增纯函数 auditToFlow）
新增 `pages/audit/auditToFlow.ts`：输入 AuditView，输出 T19 的 StageFlowData。要点：decision 归一到 allow/warn/approve/deny/error（未知按 error）；`rule_hits` 是 JSON 字符串，**try/catch 解析**为 RuleHit[]（字段 RuleID/Risk/Decision/Message/Suggestion），非法/空时回退为空数组且不崩；构造 T19 adaptAssessment 需要的 AssessmentLike `{Decision, Hits, EstScanRows}`（**不传 StageLatency，使其耗时全 0**），再补 totalLatencyMs=latency_ms、rowsReturned=rows_returned、errorMsg；这样直接复用 T19 的节点状态机与五终态，保证审计页与演示台口径一致。

### 20.7 导出
- "导出 JSONL"按**当前已应用筛选**（不含分页）调 exportAudit，请求中按钮 loading；拿到 Blob 后用临时 `<a download>` 触发，文件名 `agentsql-audit-YYYYMMDD-HHmm.jsonl`。
- 后端 422（超 1 万行）时 message.warning 提示"导出上限 1 万行，请缩小时间范围或增加筛选"；其它失败 message.error。
- "导出 PDF"为占位按钮，点击 message.info("合规 PDF 报告将在后续版本提供")，不实现。

### 20.8 三态 / 主题 / 响应式 / 卸载
首次加载自动查第 1 页；加载中 Table skeleton/Spin、无数据友好空态（含"去接入 Agent/调整筛选"引导文案）、请求失败可点重试。颜色全部走 theme/tokens 与 constants/labels、ruleMeta，禁硬编码色值。表格区用 ResizeObserver 跟随侧栏折叠/窗口变化重算 scroll.y。所有请求 AbortController、定时器/监听器/Observer 卸载清理，关闭页面或切换菜单无 setState after unmount、无残留请求。

### 20.9 文件清单（只在 web/src 内新增/修改）
- 改 `pages/Audit.tsx`（占位替换为完整页，承担数据获取/分页/筛选状态/抽屉开关编排）。
- 新增 `pages/audit/AuditFilters.tsx`、`pages/audit/AuditTable.tsx`、`pages/audit/AuditDetailDrawer.tsx`、`pages/audit/auditToFlow.ts`、`pages/audit/sqlHighlight.tsx`（轻量着色纯函数/组件）。
- 复用：`api/audit.ts`(listAudit/exportAudit 直接用，不改)、`api/agents.ts`、`api/datasources.ts`、`components/stageflow/*`(T19)、`constants/labels.ts`、`constants/ruleMeta.ts`、`components/PageContainer`、`theme/*`。
- `styles.css` 仅追加 `.audit-*` 独立前缀样式，不得改动 T18/T19 既有选择器；其余 8 个页面一律不动。

### 20.10 静态自审铁律（单位无 Node/Go）
严禁 npm/npx/tsc/build/go（必失败、勿重试、不改设计）；不改 package.json/lock、不新增依赖（dayjs 随 antd 已有）；不改任何 Go、不改七表。TS strict 零 any、零未用变量、`@/` 别名；rule_hits 解析、缺失字段、空数组、未知 decision/stmt_type 全部兜底；请求竞态与卸载清理完备。交付开头注明"未本地构建，需主控 typecheck+build 验证"。

### 20.11 主控验收门
主控灌入 1 万条跨多天、覆盖五态/多 Agent/多数据源/多语句类型/不同风险与 rule_hits 的审计数据后：typecheck/build 0 错、Go 零改动 -race 零回归；total=10000、按 20/50/100 翻页正确且倒序、单页虚拟滚动流畅不卡；各筛选条件（含多选 decisions/stmt_types、风险区间、时间范围、关键词）结果计数正确、可重置；行渲染决策/类型/风险中文与颜色正确、未知值不崩；点开抽屉五个终态各验一条——StageFlow 节点状态正确且耗时显"—"、SQL 双栏着色、多条判词卡、执行评估只显真实字段、error 条显示错误；导出 JSONL 内容与筛选一致且可解析、超量有 422 提示、PDF 占位提示正确；深浅主题、窄屏、侧栏折叠、F5、卸载均正常，控制台零报错（用户肉眼拍板）。

### 20.12 本单明确不做
不改 Go/七表/接口；不做前端一次性拉全量的假虚拟列表；audit 表没有 EXPLAIN 索引/成本、没有脱敏后结果样本，**一律不编造展示**（详细计划留存/脱敏样本留存列入后续版本）；不实现 PDF（仅占位）；不做 WebSocket 实时推送(T27+)；不引任何新依赖/语法高亮库；不动其余 8 个页面与 T18 大屏、T19 组件内部逻辑（只复用）。

## T21 拦截演示台 Playground（讲故事专用，前后端都改，零触库）

### 21.1 目标与核心约束（为什么不能直接复用 pipeline.Process）
把 T19 的"StageFlow 静态样例自测区"升级为**真实跑安检引擎**的拦截演示台：输入 SQL 或点剧本 → 后端只做"解析 + 静态规则安检" → 前端用 T19 StageFlow 播放六段判定与判词/改写建议。演示**零风险三不：不认证 Agent APIKey、不连接任何真实数据库（不 EXPLAIN、不执行）、不写 audit_logs、不建审批**。
- 现有 `pipeline.Process` 即使 `ExplainOnly=true`，仍会走 auth（验 APIKey）、load（取真实数据源+策略）、guard_dynamic（`Executors.GetOrOpen` 连真实库跑 EXPLAIN）、finish（写一条审计）。**严禁直接拿 Process 做演示**。本单在 pipeline 包新增一个纯静态评估导出函数 `StaticAssess`，只跑 parse + 静态规则门，且与真实网关静态门**复用同一套 assembleRules / splitRules / engine.Evaluate**（保证"演示即真实静态判定"，禁止另写一套规则）；动态规则 R004/R105/R106/R107/R204（依赖 EXPLAIN/索引/事务态）由既有 splitRules 自然剔除。

### 21.2 后端：pipeline 新增 `static_assess.go`
导出签名（SQL 自身语法错误走 parseError 而非 Go error，以便 HTTP 200 呈现 error 终态；err 只表示入参非法或引擎内部异常）：
```go
type StaticAssessInput struct {
    SQL        string          // 必填，TrimSpace 后非空
    Dialect    model.DBDialect // 仅 "postgres" / "mysql"
    AgentLevel string          // readonly|dml|ddl，空串按 readonly
}
func StaticAssess(in StaticAssessInput) (assessment model.Assessment, parseError string, err error)
```
执行步骤与硬性要求：
1. `SQL=strings.TrimSpace(in.SQL)`，为空返回 err；Dialect 非 postgres/mysql 返回 err；AgentLevel 空置 readonly，且必须 ∈{readonly,dml,ddl}，否则 err。
2. 初始化 StageLatency，8 个键 auth/load/parse/guard_static/guard_dynamic/execute/redact/audit 全置 0（与 pipelineStageNames 一致）。
3. 计时 parse：`parser.NewParser(in.Dialect)` 后 `Parse(in.SQL)`。**Parse 失败**：记录 parse 耗时；组装 assessment（Decision=DecisionDeny、Risk=RiskDeny、Hits 放一条 `{RuleID:"PARSE",Risk:RiskDeny,Decision:DecisionDeny,Message:<parser 原始错误文本>,Suggestion:"请检查 SQL 语法；解析失败时不进入规则安检、更不会执行"}`、Reason=该错误、StageLatency 已填），返回 `(assessment, 错误文本, nil)`——注意是 nil err。
4. parse 成功记录 parse 耗时；`assembleRules(in.Dialect, validationLimiter{}, panicMetadataProvider{})`（**复用包内现成 validationLimiter 恒允许、panicMetadataProvider 保证静态规则一旦触元数据立即 panic 暴露问题**），再 `splitRules` 只取 static，dynamic 丢弃。
5. 构造 engine.EvalContext：AST=ast；AgentLevel=level；`Agent=&model.Agent{ID:"playground-demo",Name:"演示 Agent",Status:"active",Level:level}`（**R008 要求 Agent.ID 非空，否则 Evaluate 报错**）；`Datasource=&model.Datasource{ID:"playground-demo-ds",Name:"演示数据源",DBType:string(in.Dialect)}`；`Policy=&model.PolicyDecision{AllowedTables:[]string{"*"},DeniedTables:[]string{},ColumnACL:map[string][]string{},Level:level}`（**R010 要求 Policy 非 nil；给全局通配 "*" 使其对任意表放行，演示不体现表/列授权拦截**）；MetadataProvider=panicMetadataProvider{}；Thresholds=nil（全用规则内置默认阈值）。
6. 计时 guard_static：`(engine.Engine{}).Evaluate(ast, evalCtx, staticRules, engine.RuleLayers{})`；Evaluate 返回 error 必须作为内部 err 上抛（handler 500），不得吞；记录 guard_static 耗时并把整张 StageLatency 挂回 assessment.StageLatency。
7. 静态评估 EstScanRows 恒 0（无 EXPLAIN），**不得伪造扫描行/节点耗时/结果行数**。函数体内不得出现 ExecutorProvider/Store/Audit/Approvals 任何依赖（编译期保证零触库、零写库）。
8. 纯函数、无包级可变状态、可被并发安全调用。

### 21.3 后端：adminapi 新增 `playground.go` 并注册路由
- 在 NewHandler 的 mux 上、`return handler.recover(...)` 之前注册 `mux.HandleFunc("POST /api/v1/playground/assess", handler.playgroundAssess)`（自动被 adminAuth 包裹，需登录 Bearer，与其它业务接口一致）。
- 请求体 `{ "sql": string, "db_type": "postgres"|"mysql", "agent_level"?: "readonly"|"dml"|"ddl" }`，用既有 decodeJSON 解码；sql 空 / db_type 非法 → 422（handler.fail）。
- 调 `pipeline.StaticAssess`：入参 err 按 422/500 返回；成功用 handler.ok 返回下列 view。
- **响应 view 字段名刻意用 PascalCase，逐字对齐前端 adaptAssessment 的 AssessmentLike，不要转 snake_case（与 audit 的 snake_case auditView 不同）**：
  - `Decision`(allow|deny|warn|approve|error)、`Risk`(1-4 int)、`StmtType`、`Hits:[{RuleID,Risk:int,Decision,Message,Suggestion}]`、`EstScanRows`(恒 0)、`Reason`、`Suggestion`、`Normalized`、`Objects:[{Schema,Table,Alias}]`（直接复用 model.ObjectRef，其字段无 json tag，默认即 PascalCase）、`StageLatency:{auth,load,parse,guard_static,guard_dynamic,execute,redact,audit}`（只 parse/guard_static 可能非零）。
  - 附加元信息：`ParseError`(string，非空即解析失败)、`StaticOnly`(恒 true)、`DBType`、`AgentLevel`、`SQL`(回显 Trim 后文本)。
  - 正常评估 Decision 取 assessment.Decision；**parse 失败时 view.Decision 固定输出字符串 "error"**（model.Decision 无 error 常量，故在 view 层表达），Hits 用 StaticAssess 给的 PARSE 判词，ParseError=错误文本。
  - 新增 playgroundAssessView / playgroundHitView 两个结构体并按上述名字写 json tag。
- 该 handler 不访问 Store、不经过 Runtime.ExecutorFor；Deps 无需新增字段（StaticAssess 是纯函数）。

### 21.4 后端测试（必写，新增 static_assess_test.go 并在 adminapi_test 补用例）
- pipeline 表驱动用例，逐条断言 Decision 与"命中 RuleID 集合"（顺序无关）：正常点查=allow 且无 hit；UPDATE 无 WHERE=deny 含 R002；堆叠多语句=deny 含 R001、R006；pg_sleep=deny 含 R007；PG DROP TABLE=deny 含 R101；MySQL KILL=deny 含 R203；"有 WHERE 无 LIMIT 的 MySQL UPDATE 且 dml"=approve 含 R202、不含 R002；readonly 跑 UPDATE=deny 含 R003；含 `--` 或 `/* */` 注释=deny 含 R006；残缺 SQL=parseError 非空且 err==nil；空 SQL/坏方言/坏 level=err 非空。再补并发用例：50 goroutine 并发 StaticAssess，主控 `-race` 无 data race。
- adminapi：未带 token POST assess=401；坏 body=422；正常 body=200 且 code=0、data.StaticOnly===true、Decision 正确；**断言一次 assess 前后 audit_logs 行数不变**（证明不写审计）。

### 21.5 前端 API 与类型
- `api/types.ts` 增：`PlaygroundAssessRequest{sql:string;db_type:'postgres'|'mysql';agent_level?:'readonly'|'dml'|'ddl'}`、`PlaygroundHitView{RuleID:string;Risk:number;Decision:string;Message:string;Suggestion:string}`、`PlaygroundAssessView`（按 21.3 PascalCase 声明全部字段，且结构上满足 stageflow `adaptAssessment` 的 AssessmentLike：含 Decision/Hits?/StageLatency?/EstScanRows?）。
- 新增 `api/playground.ts`：`assessPlayground(body: PlaygroundAssessRequest, signal?: AbortSignal) => request<PlaygroundAssessView>({ method:'POST', url:'/playground/assess', data:body, signal })`。

### 21.6 六个演示剧本（钉死，必须真实命中；交付前逐条对照 rules 源码静态核对，主控会真实点击回归）
新增常量 `playgroundScenarios`，每项 `{key,label,dbType,agentLevel,sql,expect}`：
1. **正常点查** / postgres / readonly / `SELECT id, name FROM public.customers WHERE id = 42 LIMIT 10` / allow（无判词，六节点全 pass）。
2. **无 WHERE 全表更新** / mysql / dml / `UPDATE orders SET status = 'closed'` / deny，命中 R002（用 dml 避开只读 R003，聚焦"缺 WHERE 改全表"）。
3. **堆叠注入夹带删除** / mysql / dml / `SELECT * FROM users WHERE id = 1; DELETE FROM users` / deny，命中 R001、R006（展示"合法查询后夹带破坏语句被整体拒绝"；可能同时命中 R002，允许多判词）。
4. **危险函数慢查询** / postgres / readonly / `SELECT * FROM public.orders WHERE id = 1 OR pg_sleep(10) IS NULL` / deny，命中 R007（讲 AI 写出阻塞函数/时间盲注）。
5. **无 LIMIT 批量写转人工** / mysql / dml / `UPDATE accounts SET balance = balance + 1 WHERE level = 'vip'` / approve（有 WHERE 故 R002 不拦，无 LIMIT 经 R202 转人工，execute 节点 locked）。
6. **残缺 SQL 解析失败** / postgres / readonly / `SELECT FROM WHERE (((` / error（解析层 fail-closed，parse 节点 block、其后 skip、零触库）。
- 结局覆盖 allow×1 / deny×3 / approve×1 / error×1，PG 与 MySQL 各 3 条。**刻意不凑 warn**：纯静态层没有 warn 规则（R005 无 LIMIT 大结果、R107 长事务都要连库 EXPLAIN/会话态，属动态门），页面如实说明，禁止为凑 warn 造假。页面小字附"可自行尝试"：DROP TABLE x(R101)、KILL 12(R203)、COPY ... PROGRAM(R104)、带注释 SQL(R006)。

### 21.7 前端 Playground.tsx 重构（替换 T19 静态预览）
PageContainer title="拦截演示台" subtitle="输入 SQL，看一次 AI 请求如何被六段安全网关逐段判定"。自上而下：
1. **顶部 Alert（info 常驻）**："零风险演示：仅做 SQL 解析与静态规则安检，不连接真实数据库、不执行 SQL、不写审计；依赖执行计划/索引/事务态的动态规则与表/列级授权，在真实网关连库并配置策略(T22)后生效。"
2. **控制区 Card**：方言 Segmented（PostgreSQL/MySQL 受控）；Agent 级别 Segmented（只读 readonly / 读写 dml / DDL ddl，默认 readonly，tooltip 说明 R003 只读写拦截）；SQL 用 Input.TextArea（等宽字体、autoSize {minRows:3,maxRows:10}、spellCheck=false）；按钮行：主按钮"模拟 AI 请求"（ThunderboltOutlined、loading、Ctrl/⌘+Enter 触发）、"重置"。
3. **剧本区**：6 个剧本按钮横向 wrap，按 expect 用语义色描边（allow 绿/deny 红/approve 橙/error 灰红）；点击=一次性设置方言+级别+填入 SQL **并立即自动评估**（无需再点主按钮）。
4. **结果区**（评估成功后渲染，自增 replayKey 触发 StageFlow 重播）：
   - 一行：决策 Tag（getDecisionMeta；error 用与 T20 一致的灰红兜底）+ 语句类型 + 方言 + 回显 SQL（等宽、可复制）。
   - `<StageFlow data={flowData} autoPlay replayKey={replayKey} />`，其中 `flowData = adaptAssessment(view)`（**直接复用 T19 adaptAssessment，view 已满足 AssessmentLike，不得另写映射**）；判词与改写建议由 StageFlow 内部 StageVerdict 渲染，不重复造。
   - parse 失败（view.ParseError 非空或 Decision==='error'）时，在 StageFlow 上方再加红色 Alert 显示 view.ParseError 原文。
   - 折叠 `<details>`"查看评估原始 JSON"展示 JSON.stringify(view,null,2)，供主控核对。
5. **三态**：评估前 Empty 引导；请求中结果区 Spin；请求失败 message.error 且保留已输入 SQL 不丢。
6. **竞态/卸载**：AbortController 随每次请求携带，新请求/切剧本/卸载 abort 旧请求，只认最后一次，无 setState after unmount。

### 21.8 样式 / 主题 / 依赖
styles.css 仅追加 `.playground-*` 独立前缀，不改 T18/T19/T20 既有选择器（T19 的 .stage-preview-* 即便本页不再引用也保留不删）。颜色一律走 theme/tokens、constants/labels、ruleMeta，禁硬编码色值。**不新增任何 npm 依赖、不改 package.json/lock**，不引语法高亮库（SQL 回显等宽即可）。

### 21.9 静态自审铁律（单位无 Node/Go）
严禁 npm/npx/tsc/build/go（必失败、勿重试、不改设计）；TS strict 零 any、零未用变量/未用 import、统一 `@/` 别名；后端 view 的 json tag 与 21.3 逐字一致；Hits/Objects/StageLatency 缺失也要兜底不崩；交付开头注明"未本地编译/构建，需主控端 go build/vet/-race、npm typecheck/build 验证"，并附"6 剧本 × 预期命中规则"自检对照表。

### 21.10 主控验收门
后端：go build/vet 0、`go test ./... -race` 全绿（含 21.4 新测试，T14/T15/T16/T18-T20 零回归）；curl 对 6 剧本逐一打 /playground/assess，Decision 与命中 RuleID 与 21.6 完全一致、StageLatency 仅 parse/guard_static 非零、StaticOnly=true；残缺 SQL 返回 200 且 data.Decision="error"；空 SQL 422、无 token 401；评估前后 audit_logs 条数不变（零写审计）；全程不启动真实 PG/MySQL。前端：typecheck/build 0 错；真实浏览器逐一点 6 剧本——allow 六节点全 pass；三个 deny 在 guard/decide block、execute skip 零触库；approve 在 decide 转人工、execute locked；error 在 parse block、其后 skip 并显红色解析错误；判词中文名/改写建议正确、重播流畅、控制台零报错；手输 DROP TABLE/KILL/注释 SQL 也能正确判；深浅主题、窄屏、F5、卸载正常。

### 21.11 本单明确不做
不改 pipeline.Process 与 MCP 七工具语义、不改七表/迁移；不连真实数据库、不演示 EXPLAIN/索引/事务态等动态规则（动态门只在真实网关生效）；不做表级/列级授权拦截演示（需 T22 配策略，本单 Policy 用全局通配仅为让引擎不报错）；不做脱敏结果演示（redact 在执行后，演示不执行）；不硬凑 warn、不伪造扫描行/耗时/结果行；不做在线 Live Demo(T26)/WebSocket；不引新依赖；不动 T18 大屏、T19 组件内部（只复用调用）、T20 审计页与其余配置页。

## T22 四个配置页 + 规则运行时接线（规范高效，前后端都改）

### 22.1 目标、范围与一个必须补的后端缺口
把 T17 的四个占位页（Agent `/agents`、数据源 `/datasources`、权限 `/policies`、规则 `/rules`）替换为与 T16 管理 API 全联通的真实配置页，做到"能增删改查、操作有反馈、危险动作二次确认、保存即生效"。探查源码后有一个必须在本单补齐的缺口：**`rules` 表目前只被 adminapi 增删改查，生产 pipeline 从未加载它**（`pipeline.layers` 仅由构造选项 `WithRuleLayers` 注入，而 `bootstrap.Assemble` 没有传，规则引擎实际永远跑代码内置默认规则）。若只做前端，规则页的"启用/禁用"保存后对真实网关不生效，等于玩具页。因此本单 = 四个前端页面 + 一处最小后端接线（只接"启用/禁用"开关，立即生效、无需重启）。与之对照，`policies` 表已通过 `Ports.Policies` 在 Process 的 load 阶段读取并生效，权限页保存即生效无需后端改动。

职责边界（务必纠正旧版"查/写/DDL 矩阵"的误解）：**Agent 的 `level`（readonly/dml/ddl）决定"能力档位"（只读 / 可读写 DML / 可 DDL），在 Agent 页配置；权限页只决定"能碰哪些对象"（object_type=database/schema/table/column，action=allow/deny）以及列级白名单。权限页不出现查/写/DDL 三态开关。**

### 22.2 后端：把 rules 覆盖接进运行时（只做 Enabled，立即生效）
现状：`internal/engine` 的 `resolveRuleConfig` 已支持 Global/Datasource/Agent 三层 `RuleConfig{Enabled *bool; Thresholds map[string]float64; ExplicitDeny bool}` 覆盖，机制完备，缺的只是生产侧把 `rules` 表读出来喂进去。v0.1 只实现 Global 层的"启用/禁用"，不做阈值编辑、不做数据源/Agent 级分层。

1. `internal/pipeline/types.go`：`Ports` 新增一个端口（`store.RuleRepository` 已天然满足，勿新增 store 方法）：
   ```go
   // RuleOverrideReader 读取管理员在控制台维护的规则覆盖（rules 表）。
   type RuleOverrideReader interface {
       List(ctx context.Context, dbType string) ([]model.Rule, error)
   }
   ```
   并在 `Ports` 中加字段 `RuleOverrides RuleOverrideReader`。允许为 nil（nil 时视为无覆盖，保证既有测试/StaticAssess 不被破坏）。
2. 新增 `internal/pipeline/rule_overrides.go`：
   - `func ruleOverridesToLayer(rules []model.Rule, dialect string) engine.RuleLayer`：只保留 `r.DBType == "all" || r.DBType == dialect` 的记录；逐条映射 `layer[r.ID] = engine.RuleConfig{Enabled: &r.Enabled}`。同 ID 重复时后值覆盖前值。非法/空 ID 跳过且不 panic。
   - `func mergeGlobalLayers(base engine.RuleLayers, extra engine.RuleLayer) engine.RuleLayers`：返回新 layers，其中 Global = base.Global 与 extra 合并、**extra（本次请求从 rules 表读到的覆盖）优先级更高**，Datasource/Agent 原样保留。不得修改入参。
3. `internal/pipeline/pipeline.go`：
   - StageLoad（已取到 `run.datasource` 之后）：若 `pipeline.ports.RuleOverrides != nil`，调用 `List(ctx, "")` 取全量（store 的 List 仅支持单值精确匹配，故取全量在内存按 all/dialect 过滤，不改 store），经 `ruleOverridesToLayer(..., run.datasource.DBType)` 得到本次请求层，存入 `pipelineRun`（newPipelineRun 的结构体加一个字段，如 `ruleLayer engine.RuleLayer`）；读取失败按 load 阶段错误走 finish（与读 policy 失败同处理）。
   - GuardStatic 与 GuardDynamic 两处 `Evaluate(...)` 当前都用 `projectRuleLayers(pipeline.layers, xxx)`，统一改为先 `layers := mergeGlobalLayers(pipeline.layers, run.ruleLayer)` 再 `projectRuleLayers(layers, xxx)`，保证静态/动态规则都受开关控制。
4. `internal/bootstrap/bootstrap.go`：`pipeline.Ports{...}` 增加 `RuleOverrides: metadataStore.Rules()`。
5. **T21 的 `StaticAssess` 刻意不接覆盖层**（它是零连库、零读表的纯静态演示，继续传 `engine.RuleLayers{}`）；规则开关效果只在真实网关 `Process` 体现，这一点在规则页 UI 文案上写明。

### 22.3 后端测试（必写，单位无法运行，主控会 -race 全量回归）
- `rule_overrides_test.go`：方言过滤（all 两方言都生效、postgres 记录不进 mysql）、Enabled 真假映射、nil/空切片返回空层、同 ID 后者覆盖、mergeGlobalLayers 覆盖优先级且不改入参；
- 在 pipeline 测试补：注入一条 `R002` 的 `enabled=false` Global 覆盖后，原本命中 R002 拦截的无 WHERE UPDATE 不再被 R002 命中；恢复 enabled=true 后重新拦截；mysql 覆盖不影响 postgres；`RuleOverrides=nil` 时行为与现在完全一致（回归保护）；
- `bootstrap` 装配测试断言 Ports.RuleOverrides 非 nil。

### 22.4 前端共用：中文映射、目录与交互通则
- `constants/labels.ts` 追加配置域映射并导出对应 label 函数/常量（不改动已有 decision/stmt 内容）：
  - agentLevel：`readonly=只读`、`dml=读写(DML)`、`ddl=结构(DDL)`；
  - agentStatus：`active=启用`(success Tag)、`disabled=禁用`(default Tag)；
  - dbType：`postgres=PostgreSQL`、`mysql=MySQL`、`all=通用`；
  - policyAction：`allow=允许`(success)、`deny=拒绝`(error)；objectType：`database=库`、`schema=模式`、`table=表`、`column=列(列级白名单)`；
  - riskLevel：1–5 数字，风险越高色越重（参考 semantic 色，5 红、4 橙、3 黄、1–2 绿/蓝）。
- 四个页面统一用 `PageContainer`（title/subtitle/extra）；列表用 antd `Table`，服务端分页（默认 page_size=20，与后端 `paginate` 对齐；下拉"取全部"用 page_size=100）；所有异步按钮进 loading、结束用 `message.success/error` 反馈并刷新列表；危险操作（删除/轮换）用 `Popconfirm` 或二次确认 Modal；错误体取拦截器抛出的后端 `msg` 展示（client 已剥到 data，错误消息沿用现有 reject 形态）。
- 表单受控、卸载时丢弃未完成请求（沿用 T20/T21 的 AbortController 范式；判断请求取消只用 `error.code==='ERR_CANCELED'`，禁止 axios.isCancel 类型谓词）。不新增任何依赖。

### 22.5 Agent 页 `/agents`（agents.ts 已齐）
- 列表列：ID、名称、负责人(owner，空显 -)、级别(中文 Tag)、状态(中文 Tag)、过期时间(expires_at，空显"永不过期")、创建时间、操作（编辑 / 轮换密钥 / 删除）。extra 放"新建 Agent"主按钮。
- **新建用 antd Steps 两步向导（Modal 或 Drawer 内）**：第 1 步填 ID（必填，小写字母/数字/下划线/连字符，前端给规则提示）、名称（必填）、负责人（可选）、级别（Radio/Segmented 三选，默认 readonly，旁注中文解释三档能力）、过期时间（DatePicker 可选，可清空=永不过期，提交时转 RFC3339 或 null）；第 2 步提交成功后展示**凭证卡片**：`api_key` 仅此次返回（`asql_` 前缀），等宽字体 + 一键复制按钮（navigator.clipboard，失败回退选中文本）+ 醒目警示"密钥仅展示这一次，关闭后无法再查看，请立即妥善保存"。级别非法/缺字段时后端 422，前端按 msg 红字提示；ID 重复后端 409，提示"Agent 已存在"。
- 编辑 Drawer：名称/负责人/级别/状态/过期，**ID 只读、不显示密钥**；按 `AgentUpdateInput` 只传被改动字段（PATCH 语义）。
- 轮换密钥：Popconfirm"轮换后旧密钥立即失效，确认？"→ `rotateAgentKey` → 弹同款凭证卡片展示新 Key（仅一次）。
- 删除：先 Popconfirm；后端在该 Agent 仍有策略时返回 409 `agent has policies; remove them first`，前端捕获并 message.error"该 Agent 仍配置了权限策略，请先在权限页移除后再删除"。

### 22.6 数据源页 `/datasources`（datasources.ts 已齐，含 ping）
- 列表列：ID、名称、类型(中文 Tag)、主机:端口、数据库(database)、用户名、连接上限(conn_limit)、语句超时 ms(stmt_timeout_ms)、行数上限(row_limit)、密码(has_password ? "已配置" : "未配置")、操作（编辑 / 测试连接 / 删除）。
- 新建表单(Modal/Drawer)：ID、名称、类型 Select(postgres/mysql，切换时端口自动填默认 5432/3306，用户可改)、host、port(InputNumber 1–65535)、database、username、**password 新建必填（后端空则 422 password is required）**、conn_limit/stmt_timeout_ms/row_limit（默认 5 / 5000 / 1000，正整数）。
- 编辑：回显除密码外全部字段；密码框留空并 placeholder"留空表示不修改密码"（后端空则沿用旧密文，见 datasourcesUpdate）；其余字段空值后端会回退当前值，但前端仍回显以保证所见即所得。
- **测试连接**：行内按钮调 `pingDatasource(id)`，按钮 loading；成功 message.success(`连接成功，延迟 {latency_ms} ms`)；失败（连接不通时后端返回非 2xx，PingView 只在成功时返回 ok=true）用 message.error 展示后端 msg，页面不崩。注意：未配置真实库时失败是正常态，样式上要让"失败原因可读"。
- 删除：数据源仍被策略引用时后端 409 `datasource has policies; remove them first`，提示先清策略；确认方式为**输入数据源名称匹配后才可点删除**（Modal 内一个 Input，与当前名称完全一致才启用"确认删除"按钮），防误删生产源。

### 22.7 权限页 `/policies`（policies.ts 已齐；对象 ACL 形态）
- 顶部筛选：Agent 下拉（listAgents page_size=100）、数据源下拉（listDatasources page_size=100）；两者都选中后 `listPolicies({agent_id,datasource_id,page_size:100})` 拉该 Agent×数据源组合的策略；未选时给空状态引导"先选择 Agent 与数据源"。
- 策略表列：对象类型(中文 Tag)、对象名(object_name，等宽)、动作(allow 绿/deny 红 Tag)、列(columns，仅 column 类型有，逗号展示)、行过滤(row_filter，空显 -)、操作（编辑/删除）。
- 新增/编辑 Drawer 字段：Agent、数据源（编辑时锁定不可改）；object_type Select(database/schema/table/column)；object_name Input；action Radio(allow/deny)；**object_type=column 时**：object_name 必须精确到单表（前端禁止 `*` 与 `xxx.*`）、action 锁定为 allow（后端不支持 column deny，选 column 时禁用 deny 并旁注原因）、columns 用 `Select mode="tags"` 录入列名（至少 1 个，提交时 join 成逗号串，去空白/去重，对应后端 policyColumns）；row_filter 可选文本（透传字符串）。
- **对象名前端预校验（照抄后端 policy.ValidateTablePatterns / validateColumnObject，把 422 挡在前端）**：非空且首尾不得有空格；以 `.` 分隔最多 2 段，任一段非空；禁止 `*.x`（schema 段不能是 *）；database/schema/table 允许 `*`、`schema.*`、精确 `schema.table` 或裸表名；column 必须精确单表。不通过则红字提示并禁止提交。
- **提交前 diff 预览**：一个编辑会话内对该组合的新增/修改/删除先收集为待提交清单，弹层逐条展示"动作(新增/修改/删除) + 对象 + 变更前→变更后"，用户确认后再按顺序串行调用 create/update/delete；任一失败即停，message 报出失败对象与后端 msg，并显示已成功条数，列表回拉到最新。策略 ID 由前端生成：`pol_` + 小写字母数字（可用时间戳 base36 + 短随机），全局唯一、不含空格点号。
- 后端约束兜底：缺 id/agent_id/datasource_id/object_name 或 resolver 判非法返回 422 invalid policy、重复 409，前端都要展示 msg。

### 22.8 规则页 `/rules`（扩展 ruleMeta + rules.ts；内置目录左连接覆盖记录）
- 先扩展 `constants/ruleMeta.ts`：为 21 条补 `dbType`（R001–R010 = all；R101–R107 = postgres；R201–R204 = mysql）、`group`（通用防护 / PostgreSQL 专属 / MySQL 专属）、`dynamic`（仅 R004/R105/R106/R107/R204 为 true）、`builtin:true`、`patternType:'ast'`；保留现有 id/title/risk，不改 getRuleMeta 签名（只扩字段，兼容现有调用）。
- 顶部：方言 Segmented（全部/通用/PostgreSQL/MySQL）+ 关键字搜索（按 id/标题）。进入页面 `listRules({page_size:100})` 取覆盖记录建 map；**展示列表 = ruleMeta 的 21 条内置目录左连接覆盖 map**：有覆盖取覆盖.enabled，无覆盖视为默认启用。其后再追加覆盖记录里 `builtin=false` 的自定义规则。
- 每行：规则 ID、中文名、风险等级(1–5 Tag)、静态/动态(dynamic 标"动态·依赖执行计划/运行态"Tooltip)、启用 `Switch`、来源徽标(内置/自定义)、操作。
- **内置规则开关即点即存（无需批量保存按钮）**：无覆盖记录→`createRule({id,db_type:dbType,title,risk_level:risk,pattern_type:'ast',definition:'',enabled,builtin:true})`；已有→`updateRule(id,{enabled})`；成功 message 轻提示并更新本地 map。**"恢复默认"不调用 DELETE（后端对 builtin 返回 403 builtin rules cannot be deleted），而是 `updateRule(id,{enabled:true})` 回到内置默认启用态。** 内置规则的标题/风险/类型/定义只读展示（后端 builtin update 只接受 enabled/definition，v0.1 不开放 definition 编辑）。
- 自定义规则："新建自定义规则"Drawer（id、db_type Select all/postgres/mysql、title、risk_level 1–5 InputNumber、pattern_type 固定 ast、definition 文本域、enabled Switch）；列表中自定义规则可全字段编辑、可 Popconfirm 删除（DELETE）。内置行不显示删除按钮。
- 页面顶部一条 info Alert 说明："开关保存后对真实网关的下一次 SQL 请求立即生效、无需重启；拦截演示台(T21)为零连库纯静态演示，不读取此处开关。动态规则需真实连库执行 EXPLAIN/读取运行态才会触发。"

### 22.9 文件清单与样式
- 后端改：`internal/pipeline/types.go`（Ports 加 RuleOverrideReader/字段）、新增 `internal/pipeline/rule_overrides.go` 与 `rule_overrides_test.go`、`internal/pipeline/pipeline.go`（load 读取 + run 字段 + 两处 Evaluate 的 layer 合并）、pipeline 既有测试补用例、`internal/bootstrap/bootstrap.go`（接线 metadataStore.Rules()）。**不改七表/迁移、不改 engine、不改 T21 StaticAssess、不改 MCP 七工具。**
- 前端改：`constants/labels.ts`、`constants/ruleMeta.ts`、整体重写 `pages/Agents.tsx`、`pages/Datasources.tsx`、`pages/Policies.tsx`、`pages/Rules.tsx`（复杂页可在各自 `pages/<域>/` 子目录拆表单/表格组件，参照 T20 audit 子目录拆法）、`styles.css` 纯追加配置页类名；四个 api 模块与 types 已齐备原则上不改（如确需少量纯展示类型可在 types.ts 末尾追加，不得改动已有字段）。
- 样式沿用 tokens 深色主题、控件尺寸/圆角/间距与 T18–T21 一致；凭证卡片、矩阵 diff、开关行等新增类名统一前缀（如 `.cfg-`），不覆盖全局样式；浅/深主题都要可读。

### 22.10 静态自审铁律（单位无 Node/Go，无法编译）
- 交付前逐文件自查：TS 无未用导入/变量（TS6133 零容忍）、所有受控输入用原生 value setter 思路由 antd Form 托管即可、不给后端发多余/未知字段（后端 decodeJSON 开了 DisallowUnknownFields，**请求体只能含 DTO 已声明字段**）；Go 侧 gofmt 风格、接口为 nil 的分支齐全、不改坏既有测试。每个改动文件头不需要版权。必须在交付说明里注明"未本地编译/构建，需主控端验证"。

### 22.11 主控验收门
后端：gofmt、build/vet=0；新增单测 + 全量 `go test -race ./...` 全 ok；专项验证"禁用 R002 覆盖后无 WHERE UPDATE 不再被 R002 拦、恢复后重新拦截、方言隔离、nil 端口回归"。前端：npm typecheck 0 错、build 通过；起本地服务 + 全新 sqlite 做真实浏览器黑盒，逐页走通：建 Agent 拿到一次性 Key 并能复制/编辑/轮换/删除 409 路径；建数据源、测试连接失败态可读、编辑留空密码不丢密、删除需名称匹配；权限页选 Agent×数据源后增/改/删策略、对象名非法被前端拦、提交前 diff 可见、保存后用该 Agent 走一次网关验证策略生效；规则页内置开关 upsert、恢复默认走 enabled=true、内置无删除按钮、自定义规则可增删改，并实测"关掉某规则→真实网关该规则不再拦、打开即恢复"。控制台零有效报错。

### 22.12 本单明确不做
不做规则阈值（R004 扫描行数/R005 结果集/R008 QPS 等）的可视化编辑（留 T24/企业版）；不做数据源级/Agent 级规则分层（v0.1 只 Global）；不做策略模板/导入导出/批量授权向导；不给 T21 StaticAssess 接规则覆盖；不改路由与左侧菜单（T17 已注册）；不引新前端依赖、不改 go.mod/package.json；不动 T18 总览、T19 StageFlow 组件内部、T20 审计、T21 演示台的既有行为。

# 第 6 章 收尾任务单 T23-T25

## T23 绕过语料决策回归与 fuzz ★（开源前红线质量门）

### 23.1 目标与既有资产
- 本单做**端到端安检终态判定**的批量回归（喂 SQL→走完 Parse+全部静态/动态规则→比对最终 Decision 与命中规则），产出命中率/漏拦/误拦报表，并用 fuzz 保证畸形输入不 panic、永远 fail-closed。
- 主控已交付语料数据 `tests/corpus/decision_cases.json`（**245 条，Codex 只消费、不增删改期望**）：danger 77（全 deny）、risk 56（approve 41/warn 15）、normal 112（全 allow）；`dialects` 决定在哪个方言跑（`all` 已展开为 postgres+mysql，共 353 次判定）。
- 注意区分：既有 `tests/corpus/postgres.json|mysql.json` 是 **parser 层 AST 断言**语料（`internal/parser/corpus_test.go` 消费），**不要改动、不要混淆**；本单新增的是**决策层**语料与测试。
- R008 限流依赖运行态 QPS/并发，**不进**静态决策语料，仍由既有并发测试覆盖，语料中无 R008 用例属正常。

### 23.2 decision_cases.json 结构（DisallowUnknownFields，严格解码）
每条字段：
- `id`：Dxxx/Sxxx/Nxxx 唯一编号；`dialects`：运行方言子集 `["postgres"]|["mysql"]|["postgres","mysql"]`；
- `category`：`danger|risk|normal`；`agent_level`：`readonly|dml|ddl`；`sql`：原始 SQL（原样、不 trim 内部空白）；
- `expect`：终态 `deny|approve|warn|allow`；`expect_rules`：应当命中的规则 ID 数组（解析失败为 `["PARSE"]`；纯 allow 正常用例为 `[]`）；
- `point` 考点、`note` 上下文人类说明、`tag`（误拦候选/缺口待确认，可空）；
- `meta`：**确定性假元数据/策略注入**（不连真实库也能让动态规则确定触发）：
  - `est_scan_rows`：>0 时 harness 在 Parse 后设置 `ast.Explain=&model.ExplainInfo{EstScanRows:该值}`，模拟 T10 执行 EXPLAIN 回填的计划信号，驱动 R004（>10 万→approve）/R005（无 LIMIT 且 >1000→warn）；为 0 时 `ast.Explain` 保持 nil（R004/R005 不触发，正常聚合/无 LIMIT 点查因此 allow，用于反误杀）；
  - `no_index`：true 时假元数据对目标写表 `TableHasIndex` 返回 false（驱动 R105）；`large_table`：true 时 `TableRowCount` 返回 1000 万（驱动 R106），否则返回 100、有索引；
  - `pg_tx`：R107 用，映射 `rules.TransactionState{InTransaction,AgeMS,IdleMS}`；`mysql_tx`：R204 用，映射 `rules.MysqlTransactionState{InTransaction,AgeMS,AffectedRows}`；默认均为不在事务；
  - `policy`：`{"mode":"allow_all"}` → `model.PolicyDecision{AllowedTables:["*"], DeniedTables:[] , ColumnACL:map[string][]string{}, Level:agent_level}`；`{"mode":"acl","allowed_tables":[...],"column_acl":{...}}` → 按值构造，用于 R010 越权正反例（仅授权 orders 的 id/name/amount）。

### 23.3 决策回归 harness：新增 `internal/pipeline/decision_corpus_test.go`（`package pipeline`）
- 必须是包内测试（`package pipeline`），以便直接复用未导出的 `assembleRules`、`validationLimiter`；加载方式照搬 `internal/parser/corpus_test.go` 的 `loadCorpus`：`runtime.Caller(0)` 定位到 `../../tests/corpus/decision_cases.json`，`json.Decoder` + `DisallowUnknownFields()`，单 JSON 数组，解码后再 Decode 一次必须 `io.EOF`（拒绝尾部多余内容/多 JSON）。
- 实现一个测试内假元数据 `decisionFakeMeta`，**同时实现** `rules.MetadataProvider`、`rules.TransactionMetadataProvider`、`rules.MysqlTransactionMetadataProvider`：`TableHasIndex` 按 `no_index` 返回、`TableRowCount` 按 `large_table` 返回、`TransactionState()`/`MysqlTransactionState()` 回射 case.meta；不允许 panic、不允许访问网络/真实库。
- 单条判定函数 `assessDecisionCase(t, c, dialect) (got model.Decision, hitIDs map[string]struct{})`，步骤严格对齐生产 `StaticAssess`：
  1. `parser.NewParser(dialect).Parse(c.sql)`；
  2. 若 `parseErr != nil && ast!=nil && ast.IsMulti && ast.StmtType==""`：把 `ast.StmtType="UNKNOWN"`（与 static_assess.go 一致，让 R001/R006 吃结构化堆叠信号）；若 `parseErr!=nil && (ast==nil || !ast.IsMulti)`：按 fail-closed 直接得到 `Decision=deny`、命中 `PARSE`（复刻 static_assess.go 的 PARSE 分支），进入比对；
  3. 解析成功后，若 `meta.est_scan_rows>0` 则设置 `ast.Explain`；`assembleRules(dialect, &validationLimiter{}, fake)` 取**全部规则（不 split、静态+动态都跑）**；
  4. 构造 `engine.EvalContext{AST:ast, AgentLevel:c.agent_level, Agent:&model.Agent{Level:c.agent_level,Status:"active"}, Datasource:&model.Datasource{DBType:dialect}, Policy:按meta构造, MetadataProvider:fake, Thresholds:nil}`；`(engine.Engine{}).Evaluate(ast, ctx, allRules, engine.RuleLayers{})` 得 Assessment，取 `Decision` 与 `Hits[].RuleID` 集合。
- 顶层 `TestDecisionCorpus` 用 `t.Run(dialect).Run(c.id)` 展开 353 个子测，逐条比对并累计统计（见 23.4），**不得**用 t.Parallel（保持确定性输出顺序）。

### 23.4 判定口径与硬门槛（计数以"方言展开后的判定次数"为分母）
- **漏拦 miss（红线）**：`category=danger` 且 `got!="deny"`。必须为 **0**，出现任意一条即 `t.Fatalf` 并打印全部漏拦 id/sql/got/hits。
- **误拦 false-positive**：`category=normal` 且 `got!="allow"`。误拦率 = 误拦次数 / normal 判定次数，门槛 **<2%**，超过即 `t.Fatalf`；目标为 0。
- **风险不符 risk-mismatch**：`category=risk` 且 `got!=c.expect`（approve/warn 精确匹配，不允许互相折算）。v0.1 要求**全部相符**，不符即 `t.Fatalf` 并列明细。
- **规则缺失 missing-rule**：`c.expect_rules` 中任一 ID 不在 hitIDs 即记一条（防止"终态蒙对、却不是靠预期规则拦住"），逐条 `t.Errorf`；`PARSE` 用例要求 hitIDs 含 `PARSE`。
- 决策优先级以生产 `recomputeAssessmentDecision` 为准（deny>approve>warn>allow），测试不自行重算优先级，只消费 Assessment.Decision。

### 23.5 回归报表（`go test ./internal/pipeline -run TestDecisionCorpus -v`）
- 测试末尾用 `t.Logf` 打印：①总判定数/通过数；②category×decision 交叉表；③漏拦/误拦/风险不符/规则缺失四类明细 id 列表；④按规则 ID 的命中次数（R001…R204、PARSE）；⑤按方言计数与误拦率百分比。报表为纯文本日志，确定性、可重复、无随机顺序，便于主控与历史结果 diff。

### 23.6 fuzz fail-closed：新增 `internal/pipeline/decision_fuzz_test.go`
- `FuzzAssessFailClosed(f *testing.F)`：种子语料 = decision_cases 全部 sql（两方言各 Add 一遍）+ 畸形种子（`"\x00\xffSELECT FROM"`、`"))((("`、超长重复 `(`/`;`/`UNION ALL`、嵌套/未闭合注释、混合大小写、emoji/多字节、`/*!*/`、NUL 截断）。
- fuzz 体对任意 `(sql, dialect)` 跑完整 Parse+规则评估，断言：**绝不 panic**（含 nil 解引用、切片越界、整数溢出、正则回溯失控等）；**只要 Parse 返回 error，终态必须是 deny（fail-closed）**，绝不能在解析失败时得到 allow/approve/warn；评估过程不得访问网络/文件/真实库。
- 主控验收命令（实跑）：`go test -run=^$ -fuzz=FuzzAssessFailClosed -fuzztime=30m ./internal/pipeline/`，要求 30 分钟无 panic、无 fail-closed 违例；普通 `go test ./...` 时 fuzz target 只跑种子（不进入持续 fuzz）。

### 23.7 文件清单与边界
- 新增：`internal/pipeline/decision_corpus_test.go`、`internal/pipeline/decision_fuzz_test.go`；数据 `tests/corpus/decision_cases.json` 主控已给。
- **默认不改生产代码**。若回归暴露真实漏拦/误拦，允许对 `internal/rules/*`、`internal/pipeline/*`、`internal/parser/*` 做**最小**修复，但必须在交付说明单列"为通过语料改动的生产文件 + 对应失败用例 id + 原因"；**严禁反向修改语料期望去迁就代码缺陷**（确属语料标注错误的，列清单交主控确认后由主控改数据，Codex 不自行改 json）。
- 不改七表/迁移、不改 adminapi/MCP 协议、不改前端、不改 go.mod 依赖、不改既有 parser corpus 与其测试。

### 23.8 静态自审铁律（单位无法 go test）
逐文件文本级自审：导入均被使用、接口方法集与 `rules` 三个 provider 接口完全匹配（否则编译不过）、`assembleRules` 第二参传 `&validationLimiter{}`、metadata 任何分支不 panic、计数分母不为 0、断言门槛数值（漏拦 0、误拦 2%）写对。交付说明注明"未本地编译/构建/跑测，需主控端验证"。

### 23.9 主控验收门
gofmt、build/vet=0；`go test -race ./...` 全 ok；`-run TestDecisionCorpus -v` 报表满足漏拦 0、误拦率<2%、risk 全相符、无 missing-rule；fuzz 连续 30 分钟无 panic 且违例 0；不达标不许进入 T24、不许开源。

### 23.10 本单明确不做
不接真实数据库 EXPLAIN/真实元数据（沿用 T10 链路，本单只用确定性假元数据）；不做规则阈值可视化编辑；不改变 21 条规则既定语义（修 bug 除外且需报备）；不做语料在线管理/导入界面；不把 R008 纳入静态语料；不补 T24/T25 的打包与指标。

### 23.11 T23.1 决策回归缺陷修复（首跑质量门逼出的 10 项，最小改动）
首跑 `TestDecisionCorpus` 后，语料侧标注问题已由主控修正（现 246 条 / 353 次，PG192 / MySQL161），剩余失败全部是解析器/规则的真实缺陷，逐项修复到决策回归全绿。先给 `internal/model.AST` 增加两个字段并由两个 parser 填充：`HasGroupBy bool`（最外层含 GROUP BY）、`IsPureAggregate bool`（最外层投影全是聚合函数 count/sum/avg/min/max/stddev/string_agg 等、无 GROUP BY、无普通非聚合列）。

- **F1 方言规则对堆叠/UNKNOWN 健壮性**（rules/postgres.go `requiredPostgresAST`、rules/mysql.go `requiredMysqlAST`）：现状是 PG 多语句堆叠解析为 IsMulti+UNKNOWN+Operations 空，`requiredPostgresAST` 对空 Operations 返回 error，而 engine 任一规则 error 即整体 failedAssessment，导致 R101 先报错、R001/R006 的拦截记录不上（D001/D002/D004/D005/D006/D008 中断，347≠353）。目标：方言专属规则对 IsMulti 或 UNKNOWN 或 Operations 空一律 allowResult 跳过、不返回 error，堆叠/未知统一交 R001/R006；保留"Operations 含空串元素才报错"。验证：上述 6 条 PG 终态 deny 命中 R001（D004 含 R104、D005 含 R003），Total=353，且不跳过真实结构化语句。
- **F2 恒真条件补两类**（parser/postgres_tautology.go、parser/mysql.go `mysqlExpressionTautology`）：现状只认常量=常量/布尔/数字/AND/OR，漏 ①同列=同列（左右列引用归一后相同，如 id=id、col=col）②无相关子查询的 `EXISTS(SELECT 常量)`。命中即 WhereTautology=true，由 R002 deny。验证 D015/D016/D017 双方言 deny 命中 R002，D011–D014 保持 deny，`id=1` 与相关 EXISTS 不误判。
- **F3 R007 剥 schema 前缀**（rules/generic.go）：比对黑名单前取最后一个 '.' 之后的标识符。验证 D041 `pg_catalog.pg_sleep` 命中 R007，D042 嵌套仍拦，普通函数不误伤。
- **F4 R010 列级 ACL**（rules/generic.go，消费 PolicyDecision.ColumnACL 与 ast.Columns）：表级通过后，对配置了列白名单的表，明确引用到白名单外列即 deny；SELECT *（N104）/未配置该表列 ACL/列归属无法判定时不做列级硬拦（避免误杀）。验证 D054 选未授权列 secret 双方言 deny 命中 R010，D049–D053 表级越权仍 deny，N103/N106/N108/N109/N104 全 allow。
- **F5 R101 覆盖全部危险 DROP**（parser/postgres.go `postgresObjectType`、rules/postgres.go）：补齐 SCHEMA/SEQUENCE/FUNCTION(PROCEDURE)/VIEW/MATERIALIZED VIEW 的 DROP 信号，R101 对这些与 DATABASE/TABLE 一并 deny。验证 D059 DROP SCHEMA 命中 R101。
- **F6 R106 覆盖非在线 CREATE INDEX**（parser 增加 IndexStmt 信号区分 `CREATE INDEX` 与 `CREATE INDEX CONCURRENTLY`，读 concurrent 标志；rules/postgres.go）：大表普通 CREATE INDEX 转人工，CONCURRENTLY 不拦。验证 S035 approve 命中 R106，S032–S034/S036 ALTER 行为不变。
- **F7 parser 产出 NESTING_DEPTH/UNION_COUNT**（双方言；R009 已会读，判定不改）：Operations 追加 `NESTING_DEPTH:<n>`（最深派生表/子查询层数）、`UNION_COUNT:<n>`（UNION/UNION ALL 连接符个数，k 分支=k-1），命名对齐 rules 常量。验证 S014（18 层）、S015（19 分支=18 连接符）双方言 approve 命中 R009（默认阈值 16），浅查询不命中，并补 parser 单测锁定计数。
- **F8 R005 豁免纯聚合**（rules/generic.go + 新 AST 字段）：IsPureAggregate 且无 GROUP BY 时 R005 直接放行；HasGroupBy 维持原 warn。验证 N107 count(*)（注入 est=50000、无 LIMIT）双方言 allow 不命中 R005，S055 GROUP BY 仍 warn。
- **F9 MySQL 条件注释**（parser/mysql.go `mysqlHasComment`）：原始 SQL 出现 `/*!` 版本条件注释即判含注释交 R006 deny；`/*+` optimizer hint 不算（D034 放行）；--/#/普通块注释维持现状。验证 D031 命中 R006。
- **F10 SET 作用域 + R203**（parser/mysql.go、rules/mysql.go）：SET 语句区分 `SET GLOBAL`（含 @@global.）与 `SET SESSION`（SESSION/LOCAL/普通用户变量）并写入 Operations；R203 拦 SET GLOBAL，放 SET SESSION/USE/LOCK。验证 D076 deny 命中 R203，N072 SESSION allow，FLUSH/KILL/PURGE 仍 deny。

统一约束：只改 internal/parser、internal/rules、internal/model（加两字段）；不动决策 harness、语料 json、前端、API、存储；不重构、不改规则 ID 与默认阈值（嵌套/UNION 维持 16）；新增分支补 parser/rules 单元测试。单位无法编译，逐文件文本级静态自审（类型/字段名/import/switch 穷尽/nil 安全），交付注明"未本地编译/测试，需主控端验证"。完成标准（主控端）：`go test ./internal/pipeline -run TestDecisionCorpus -count=1` 全绿（246 条/353 次，danger miss=0、normal 误拦率<2%、risk 全匹配、无 missing-rule、Total=353），`go test ./...` 全绿，gofmt/vet 为 0。

### 23.12 T23.2 二轮回归缺陷修复（4 项，全部在 parser 层，规则语义不动）
T23.1 合入后 F1–F10 目标用例全绿、双方言 normal 误拦率 0%、danger 零漏拦；语料口径由主控二次校准为 252 条/353 次（PG192/MySQL161，danger76/risk61/normal115）。剩余 4 条 risk mismatch 全是 parser 结构缺陷，逐项修复；internal/rules 与 21 条规则语义一律不动。

- **G1 PostgreSQL UNION 计数**（parser/postgres.go `postgresSelectComplexity`）：现状递归只对 map key 恰为 "SelectStmt" 的子节点下钻，而 PG SetOperation 的左右分支键名是 `larg`/`rarg`，链式 UNION 只在最顶层计一次、深层漏计（S015 19 分支应 UNION_COUNT=18，实测不触发 R009）。目标：递归识别所有 SelectStmt 节点（含 larg/rarg 嵌套、valuesClause 等），每个 op=SETOP_UNION 节点计 1；嵌套深度仍只在 subquery/subselect/ctequery 边界递增（S014 不得回退）。补 parser 单测锁定：19 分支 UNION→UNION_COUNT=18、NESTING_DEPTH=0；18 层派生表→NESTING_DEPTH=18、UNION_COUNT=0；UNION 套派生表混合用例两者分别正确。
- **G2/G3 MySQL 多表 DML 连接条件**（parser/mysql.go `mysqlRootWhere`）：现状 Update/Delete 只看 typed.Where，而 `DELETE a FROM t1 a JOIN t2 b ON a.id=b.id`、`UPDATE t1 JOIN t2 ON … SET …` 的行限定在表连接 ON 上、Where 为 nil，被误判"无 WHERE"交 R002 deny（应交给 R202 按多表 JOIN 写 approve）。目标：Update/Delete 且 Where 为 nil 时，若表表达式存在带 ON 条件的 JOIN（非 cross/无约束连接），则 HasWhere=true、WhereTautology 按该 ON 表达式判定；单表无 WHERE（DELETE FROM t、UPDATE t SET x=1）与无 ON 的笛卡尔多表仍 HasWhere=false（R002 继续拦）。补单测：S040/S041 HasWhere=true，单表全删/全改仍 false。
- **G4 MySQL EXPLAIN 透传**（parser/mysql.go `mysqlStatementType`/`mysqlOperation`）：现状 `*sqlparser.ExplainStmt`/`ExplainTab` 被归为 ADMIN，致 `EXPLAIN SELECT…` 在只读档被 R003 当非 SELECT deny（S003；PG 侧 EXPLAIN 已正确透传）。目标：ExplainStmt 解引用其内部 Statement——内部为 Select/Union 时 StmtType=SELECT、Operation=SELECT（保留 EXPLAIN 信号，不影响 R004 大扫描判定）；ExplainTab（EXPLAIN 表名/列）维持 ADMIN。补单测：EXPLAIN SELECT→SELECT；EXPLAIN UPDATE 等仍非 SELECT 被拦；EXPLAIN tbl 维持 ADMIN。

统一约束：只改 internal/parser 与其测试（G 组无需新增 model 字段则不改 model）；不动 internal/rules、internal/pipeline 决策测试、tests/corpus 语料、前端/API/存储；不加依赖、不改阈值与规则 ID。逐文件文本级静态自审，注明"未本地编译/测试，需主控端验证"。完成标准：`go test ./internal/pipeline -run TestDecisionCorpus -count=1` 全绿（252 条/353 次，danger miss=0、normal 误拦率当前为 0 且必须 <2%、risk 全匹配、无 missing-rule、Total=353），`go test ./...` 全绿，gofmt/vet=0。

## T24 打包部署（拆 T24.1 代码 / T24.2 交付，顺序执行）

**现状基线（开工前先读，勿重复造轮子）**
- `cmd/agentsql/main.go`：cobra，已有 `version/serve/mcp`；serve 已做 signal 优雅停机（10s Shutdown）、`AGENTSQL_SECRET` 恰好 32 字节校验、admin 账号从 env、webui 走 go:embed。
- `cmd/agentsqlctl/main.go`：**空壳**（仅 `package main` + 空 `main()`），本单填充。
- `internal/config/config.go`：`Load(path)` 读**单文档 YAML**、`KnownFields(true)` 严格、`Validate()` fail-closed、会 `MkdirAll` sqlite 父目录；**不支持环境变量覆盖**（本单不改这一行为，容器用挂载配置解决监听地址）。
- `internal/store/migrate.go`：`Migrate(ctx,*sql.DB)` 按 embed 的 `migrations/*.sql` 版本号**幂等**迁移；`bootstrap.Assemble`→`store.OpenWithSecret` 启动时已自动迁移。
- `internal/mcpserver/http.go` `NewHTTPHandler`：`http.NewServeMux`，已挂 `/mcp`、`/api/v1/`、`/`(webconsole)，中间件链含 auth/rate/recover/accessLog；**当前无健康端点**。
- **硬约束：`github.com/pganalyze/pg_query_go/v5` 在本项目必须 cgo**（实测 `CGO_ENABLED=0` 时 `pg_query.ParseToJSON/SplitWithScanner/Normalize/Scan` 全部 undefined）。因此：禁用纯静态 `CGO_ENABLED=0` 构建；容器构建镜像与运行镜像都走 glibc（debian bookworm），**不得用 alpine/musl**；go 版本以 go.mod 的 **1.25** 为准（现 Dockerfile 写的 golang:1.23 错误）。
- 前端产物 `internal/webui/dist` 已随仓提交（.gitignore 显式放行），**镜像内不构建前端**，直接 COPY 仓内 dist。

### T24.1 运维 CLI + 探活端点（纯 Go；先做，主控可直接编译/测试验收）

- **H1 配置只校验不落盘**：给 `internal/config` 新增 `Parse(contents []byte) (Config, error)`，逻辑=现有 Load 的「单文档 Decode + KnownFields + 尾部文档检测 + Validate + console_enabled 默认值」，但**不做 MkdirAll、不读文件**；重构 `Load(path)` 为「读文件 → Parse → Clean 路径 → MkdirAll」，行为与现有 config_test 完全一致（不得改坏既有断言）。
- **H2 统一版本包**：新增 `internal/version/version.go`，`var Version = "dev"`；`cmd/agentsql` 与 `cmd/agentsqlctl` 的 version 都引用它，Makefile/Dockerfile 统一用 `-X github.com/cuipengdba/agentsql/internal/version.Version=$(VERSION)` 注入；删除 cmd/agentsql 内重复的 `var version`。
- **H3 agentsqlctl（cobra，SilenceErrors/SilenceUsage=true，子命令各自返回错误、main 统一非 0 退出）**：
  - `version`：打印 version.Version。
  - `init-config --output <path> [--force]`：把与 `examples/config.example.yaml` 同值的内置默认模板写到目标（内嵌字符串常量，不依赖运行目录）；父目录不存在则 0o750 创建；目标已存在且无 --force 则报错退出，不覆盖。
  - `check-config -c <path>`：读字节后调 `config.Parse`（**不建目录、不碰库**），通过打印 `config ok: <listen> sqlite=<path>` 并退出 0；失败打印具体校验错误、退出 1。
  - `migrate -c <path>`：要求 `AGENTSQL_SECRET` 恰好 32 字节，`config.Load`→`store.OpenWithSecret`→显式 `store.Migrate` 到最新，打印当前/最新 migration 版本后关闭；serve 本就自动迁移，此命令仅用于初始化与排障。
  - `health --url <default http://127.0.0.1:7780/healthz> [--timeout 3s]`：标准库 http.Client 发 GET，状态 200 且响应 JSON `status=="ok"` 退出 0 并回显 body，否则退出 1（供 Docker HEALTHCHECK / systemd 探活，不能引第三方依赖）。
  - 每个子命令补单测：init-config 产物能被 `config.Parse` 通过、重复写拒绝/--force 覆盖；check-config 对坏 YAML/非法端口/非正默认值非 0；health 用 `httptest.Server` 覆盖 200/503/超时。
- **H4 存活/就绪端点（改 `internal/mcpserver/http.go`）**：
  - 新增 `/healthz`（liveness）：**注册在 auth/rate 中间件之外、免鉴权**，恒返回 200 `{"status":"ok","version":"<version.Version>"}`。
  - 新增 `/readyz`（readiness）：免鉴权；通过 runtime 的 store 对 sqlite 做一次 1s 内 `PingContext`，成功 200 `{"status":"ready"}`，失败 503 `{"status":"not ready"}`；若该进程未装配 store（纯 MCP stdio 不挂 HTTP 时不涉及），HTTP 模式下应能拿到 store，拿不到按 503 fail-closed。为此允许在 `internal/store` 暴露一个 `Ping(ctx) error`（内部 `db.PingContext`），不要在 mcpserver 里直接摸驱动。
  - Go1.25 ServeMux 按最长前缀匹配，`/healthz`、`/readyz` 精确优先于 webconsole 的 `/`，不得被 SPA 回退吞掉。
  - 补 `http_test.go` 用例锁定：**不带 token** GET /healthz=200 且含 version、GET /readyz 在装配可用 store 时 200；不带 token GET /mcp 仍 401（证明探活没在鉴权上开口子）；/healthz 不命中 webconsole。
- **T24.1 统一约束**：只动 cmd/agentsql、cmd/agentsqlctl、internal/config、internal/version(新增)、internal/store(仅加 Ping)、internal/mcpserver/http.go 及其测试；不改六段流水线、21 规则、parser、前端、go.mod（**不新增第三方依赖**，health/CLI 全用标准库 + 已有 cobra/yaml）。单位无法编译，逐文件文本级静态自审，注明「未本地编译/测试，需主控端验证」。
- **T24.1 完成标准（主控端）**：gofmt/vet=0；`go test -race ./...` 全绿且既有 config/http/store 测试零回归；`go build ./cmd/...` 产出两二进制；手测 `agentsqlctl init-config/check-config/version/health` 链路；起 serve 后无 token /healthz=200、/readyz=200，/mcp 无 token 仍 401。

### T24.2 构建/容器/编排/部署交付（T24.1 合入后再做；以脚本、配置、文档为主）

- **I1 Makefile 修复**：两二进制都用 `-X .../internal/version.Version`；`build` 走平台原生 cgo（不再写 CGO_ENABLED=0）；新增 `webui`（cd internal/webui && npm ci && npm run build，仅发布前手动用，默认 build 不依赖）、`race`（go test -race ./...）、`release`（构建后对 bin 生成 SHA256SUMS）；**删除现有编不过的 `CGO_ENABLED=0` 四平台 cross**，改为单一 `docker-linux-amd64`（在 golang:1.25-bookworm 容器内 cgo 编 linux/amd64）并注释说明 pg_query 需要 cgo、跨平台交叉编译留 v0.2。
- **I2 Dockerfile 多阶段重写**：build 用 `golang:1.25-bookworm`（glibc+gcc；显式 `apt-get install -y --no-install-recommends gcc libc6-dev ca-certificates` 保险）；**COPY go.mod 和 go.sum** 再 `go mod download`（修当前漏 go.sum）；COPY . .（带仓内 dist，不跑 npm）；`CGO_ENABLED=1` 分别 build agentsql、agentsqlctl，`-trimpath -ldflags "-s -w -X .../version.Version=$VERSION"`。runtime 用 `debian:bookworm-slim`（**禁止 alpine**），装 ca-certificates、tzdata，建非root用户 agentsql 与 /var/lib/agentsql、/etc/agentsql，chown；拷两二进制与示例配置；`USER agentsql`、WORKDIR /var/lib/agentsql、EXPOSE 7780；`HEALTHCHECK --interval=15s --timeout=3s CMD ["agentsqlctl","health","--url","http://127.0.0.1:7780/healthz"]`；ENTRYPOINT ["agentsql"]，CMD ["serve","--config","/etc/agentsql/config.yaml"]。
- **I3 新增 .dockerignore**：排除 .git、bin、data、*.db*、node_modules（含 internal/webui/node_modules）、transfer、*.log、.diag、测试缓存；**显式保留 internal/webui/dist**。
- **I4 docker-compose.yml 重写（现为空 `services:{}`）**：服务 `agentsql`：build .（image agentsql:${VERSION:-v0.1}）、`ports: 7780:7780`、environment 读 `.env` 的 `AGENTSQL_SECRET`（缺失则 `${AGENTSQL_SECRET:?must set AGENTSQL_SECRET}` 直接报错）与 `AGENTSQL_ADMIN_USER/PASSWORD`；volumes 挂 `./data:/var/lib/agentsql` 与 `./examples/docker/config.yaml:/etc/agentsql/config.yaml:ro`；healthcheck 同 I2、`restart: unless-stopped`、`security_opt: [no-new-privileges:true]`。另用 `profiles: [demo]` 给**默认不启动**的示例 postgres:16 / mysql:8（带健康检查与示例库环境变量），`docker compose --profile demo up` 才起，供 Playground 连真库。
- **I5 配置样例**：新增 `examples/docker/config.yaml`（`http_listen: 0.0.0.0:7780`、console_enabled true、sqlite_path `/var/lib/agentsql/agentsql.db`、defaults 与示例一致）与 `examples/docker/.env.example`（32 字节 SECRET 占位、admin 账号）；补全 `examples/config.example.yaml` 字段注释（各默认值含义、env 名、容器内需 0.0.0.0、SECRET 丢失则数据源密码不可解密）。
- **I6 deploy/systemd/agentsql.service + docs/DEPLOY.md**：unit 用 Type=simple、User=agentsql、EnvironmentFile=-/etc/agentsql/agentsql.env、ExecStart=/usr/local/bin/agentsql serve -c /etc/agentsql/config.yaml、Restart=on-failure、NoNewPrivileges=true、ProtectSystem=strict + ReadWritePaths=/var/lib/agentsql、WantedBy=multi-user.target。DEPLOY.md 写清三种安装（docker compose 推荐 / 二进制 / systemd）、目录约定、**升级=换二进制后自动幂等迁移与回滚**、**备份=agentsql.db + AGENTSQL_SECRET 二者缺一不可**、探活、常见问题（127.0.0.1 容器不通要改 0.0.0.0、cgo/glibc、端口占用）。
- **I7 README 快速开始重写**：方式一 compose 三步（复制 .env 填 SECRET → `docker compose up -d` → 浏览器开 127.0.0.1:7780，用 env 里 admin 登录）；方式二二进制（make build → agentsqlctl init-config → export AGENTSQL_SECRET → agentsql serve）；附 examples/mcp 三个客户端接入链接。
- **T24.2 完成标准（主控端）**：Makefile 目标可跑、`make build` 出两二进制；本机 Docker daemon 可用时 `docker build` 成功且 `docker compose up -d` 健康转 healthy、浏览器能登录并 5 分钟内走完「加数据源→建 Agent→MCP 连上→看到审计」；Docker 不可用时对 compose/unit 做 YAML 与 `bash -n`/字段静态校验并注明容器实跑待补；文档命令逐条可执行。

**T24 统一不做**：不在镜像内构建前端、不做 buildx 多架构/镜像签名/Helm/K8s operator（留 v0.2 企业版）；不做配置热加载（改完重启）；不支持外部 PostgreSQL 当元数据库（v0.1 仅内嵌 sqlite）；不做在线自动升级；不改六段流水线与 21 规则语义；T24.1 不新增任何第三方 Go 依赖；Prometheus 指标属 T25，不在本单。

## T25 可观测性（/metrics + Grafana + 网关开销基线）
`/healthz`、`/readyz` 已在 T24.1 落地，T25 只做指标，不改任何判定、不碰 252 语料与前端。**依赖授权：用户已于 2026-09-15 明确授权新增 `github.com/prometheus/client_golang v1.22.0`；单位无网，Codex 只在 go.mod require 块手写这一行，禁止 go get / go mod tidy，go.sum 由主控联网补全并编译。**

### T25.0 设计原则（必须遵守）
- pipeline 包**不得 import prometheus**：只在 `internal/pipeline` 定义最小本地观察者接口，metrics 包用同签名方法隐式实现，依赖方向只允许 metrics→model、bootstrap 同时持有二者，杜绝包循环。
- 指标采集**绝不能影响主流程**：所有观察者调用必须 nil 安全 + panic 安全（metrics 指针接收者先判 nil；pipeline 侧再包一层 recover 双保险）。
- label 低基数：不得把 SQL 文本、Agent ID、数据源 ID 之外的高基数值塞进 label；HTTP 路由必须归一化（见 I5）。连接池是唯一允许 datasource label 的指标（数据源数量有界）。
- 不新增 config 字段：v0.1 的 `/metrics` 与 `/healthz` 同级、免鉴权、默认开放；stdio `mcp` 模式下 metrics 对象空转无副作用。

### I1 新增 internal/metrics 包（metrics.go / doc.go / metrics_test.go）
- `type PoolStat struct { DatasourceID, Dialect string; MaxOpen, InUse, Idle int }`。
- `func New(poolSnapshot func() []PoolStat) *Metrics`：创建**私有** `*prometheus.Registry`，注册内置 `NewGoCollector`、`NewProcessCollector` 与下列指标；poolSnapshot 可为 nil（此时连接池指标无样本）。
- 指标（命名、类型、label 固定如下）：
  1. `agentsql_http_requests_total` CounterVec[method,route,status]
  2. `agentsql_http_request_duration_seconds` HistogramVec[route]，自定义桶 `0.001,0.0025,0.005,0.01,0.025,0.05,0.1,0.25,0.5,1,2.5,5`
  3. `agentsql_decisions_total` CounterVec[decision,dialect,stmt_type]
  4. `agentsql_rule_hits_total` CounterVec[rule_id,decision,risk]
  5. `agentsql_pipeline_stage_duration_seconds` HistogramVec[stage]，桶 `0.0005,0.001,0.0025,0.005,0.01,0.025,0.05,0.1,0.25,0.5,1,2`
  6. `agentsql_rejected_total` CounterVec[reason]
  7. `agentsql_pool_connections` GaugeVec[datasource,dialect,state]（state∈max/inuse/idle），由自定义 `poolCollector` 在每次 Collect 时调用 poolSnapshot 即时采集，不做后台定时 Set。
- 方法（全部指针接收者、nil 接收者直接 return）：`ObserveHTTP(method,route,status string, seconds float64)`（同时加 counter 与 histogram）、`ObserveDecision(decision,dialect,stmtType string)`、`ObserveRuleHit(ruleID,decision,risk string)`、`ObserveStage(stage string, latencyMS int64)`（ms→s）、`IncRejected(reason string)`、`Handler() http.Handler`（`promhttp.HandlerFor(registry, HandlerOpts{ErrorHandling: promhttp.ContinueOnError})`）。
- metrics_test.go：注册无重复；各 Observe 后 `registry.Gather` 能取到对应样本且值正确；nil *Metrics 调用全部方法不 panic；poolCollector 按快照产出三个 state 的 gauge。

### I2 pipeline 观察者解耦（新增 internal/pipeline/observer.go，小改 types.go、pipeline.go）
- observer.go：定义 `type DecisionObserver interface { ObserveDecision(decision,dialect,stmtType string); ObserveRuleHit(ruleID,decision,risk string); ObserveStage(stage string, latencyMS int64) }` 与 `func WithObserver(observer DecisionObserver) Option`（传 nil 等价不观察，不报错）；`pipelineOptions` 增字段 `observer DecisionObserver`，`Pipeline` 结构增同名字段，`New` 赋值。
- pipeline.go 唯一打点位置在 `(*pipelineRun).finish`（现 473 行）内、`run.audit(...)` 完成、StageLatency 最终化之后、return 之前：dialect 取 `run.datasource.DBType`（datasource 为 nil 时用空串），stmtType 取 `string(run.response.Assessment.StmtType)`；依次回调 decision 一次、遍历 `Assessment.Hits` 回调每个 RuleHit（risk 用 `strconv.Itoa(int(hit.Risk))`）、遍历最终 stageLatency 回调每阶段；整段用 `func(){defer recover();...}()` 包住，观察者异常绝不改变返回的 Response/error。error 路径（decision=deny）也必须被统计（finish 是唯一汇聚出口，天然覆盖）。
- 新增 t25_observer_test.go：用 fake observer 分别跑一条 allow、一条 deny，断言三个回调被调用且 label 值正确；再构造一个会 panic 的 observer，断言 `Process` 结果与不装 observer 时完全一致。

### I3 executor 连接池快照（新增 internal/executor/pool_stats.go，小改 postgres.go/mysql.go）
- pool_stats.go：`type PoolStat struct { DatasourceID,Dialect string; MaxOpen,InUse,Idle int }`；包内可选接口 `type poolStatser interface { poolSnapshot() PoolStat }`；`func (m *Manager) SnapshotPools() []PoolStat`：RLock 遍历 executors，map key 作 DatasourceID、`Executor.Dialect()` 作 Dialect，对实现了 poolStatser 的执行器取快照，未实现的（如测试 fake）跳过，不得因此报错。
- postgres.go：结构体增 `maxConns int32`，构造时把归一化后的连接上限存入；`poolSnapshot()` 用 `pool.Stat()`：MaxOpen=int(s.maxConns)、InUse=int(stat.AcquiredConns())、Idle=int(stat.IdleConns())。
- mysql.go：结构体增 `maxConns int`（用 normalizedConnectionLimit 的结果，现 SetMaxOpenConns 处同步保存）；`poolSnapshot()` 用 `database.Stats()`：MaxOpen=s.maxConns、InUse=stats.InUse、Idle=stats.Idle。
- 单测：用一个实现/不实现 poolStatser 的 stub 验证 SnapshotPools 的收集与跳过逻辑。

### I4 bootstrap 装配（小改 bootstrap.go）
- `Assemble`（assembleWithExecutorProvider）在 NewManager 之后创建 hub：`metricsHub := metrics.New(func() []metrics.PoolStat { 把 manager.SnapshotPools() 的每条逐字段转成 metrics.PoolStat })`；`pipeline.New(...)` 追加 option `pipeline.WithObserver(metricsHub)`；`Runtime` 增字段 `Metrics *metrics.Metrics` 并返回。stdio 与 HTTP 两条装配路径都经过 Assemble，行为一致。

### I5 HTTP 暴露与中间件（小改 internal/mcpserver/http.go）
- mux 在 healthz/readyz 旁注册 `mux.HandleFunc("GET /metrics", metricsEndpoint(runtime))`：runtime.Metrics 非 nil 用其 Handler()，nil 返回 503 文本；该端点**免鉴权**（注册在 authMiddleware 之外）。
- 新增 `metricsMiddleware(hub, next)` 并在 newHTTPHandlerWithRegistry 返回前包住整个 mux：复用 `statusResponseWriter` 取 status，记录 method、归一化 route、秒级耗时并调 `ObserveHTTP`。
- route 归一化算法固定（写成纯函数 `classifyRoute(path string) string`，便于单测）：`/mcp`→`/mcp`；`/healthz`/`/readyz`/`/metrics` 原样；前缀 `/api/v1/` 时取段：集合类返回 `/api/v1/<资源>`（如 `/api/v1/agents`），带 ID 的归并为同一路由（`/api/v1/agents/{id}`、子动作 `/api/v1/agents/{id}/rotate-key`），ID 段（UUID、`asql_` 前缀、纯数字/长 hex）一律折叠为 `{id}`，**禁止透传真实 ID**；其余归 `/other`。
- 限流计数：rateMiddleware 中 `!registry.allow(...)` 分支在写 429 前调 `registry.runtime.Metrics.IncRejected("rate_limited")`（nil 安全）。
- 单测：无 token GET /metrics=200 且正文含 `agentsql_` 前缀；打一次 /mcp（无 token 401）后 http_requests_total 出现对应样本；classifyRoute 表驱动覆盖集合/详情/子动作/UUID/asql_/探测路径。

### I6 examples/observability 交付物
- `prometheus.yml`：global scrape_interval 15s；job_name=agentsql，metrics_path=/metrics，static_configs target `agentsql:7780`。
- `grafana_dashboard.json`：可直接 Import 的合法 dashboard（templating 数据源变量），6 个面板——①QPS by route：`sum(rate(agentsql_http_requests_total[1m])) by(route)`；②Decision 分布：`sum(rate(agentsql_decisions_total[1m])) by(decision)`；③规则命中 Top10：`topk(10, sum by(rule_id)(increase(agentsql_rule_hits_total[1h])))`；④HTTP P99：`histogram_quantile(0.99, sum(rate(agentsql_http_request_duration_seconds_bucket[5m])) by(le,route))`；⑤限流速率：`sum(rate(agentsql_rejected_total[5m])) by(reason)`；⑥连接池占用：`agentsql_pool_connections` by(datasource,state)。
- `docker-compose.observability.yml`：profile=`observability`，起 prometheus（挂 prometheus.yml）、grafana（挂 dashboard 到 provisioning/dashboards），与主网关 compose 网络互通；不并入默认 profile。
- `README.md`：抓样、导入面板、独立观测栈三步命令。

### I7 网关自身开销基线（新增 internal/pipeline/t25_bench_test.go + docs/perf/README.md）
- 用**内存 fake executor**（不连真实数据库，剥离真实 DB 耗时）构造只读 SELECT 的完整 pipeline，`BenchmarkPipelineReadOnlyParallel` 用 `b.RunParallel` 跑并发只读决策，b.ReportAllocs；另写一个默认 `Skip` 的真实库 benchmark（未设 AGENTSQL_BENCH_DSN 时跳过）。
- docs/perf/README.md 写清复现实验命令 `go test ./internal/pipeline -run '^$' -bench BenchmarkPipelineReadOnlyParallel -benchtime=2s -count=5` 与口径（这是六段流水线纯额外开销，不含真实 DB 时间）；**结果数值由主控在本机实跑后写入 docs/perf/t25_bench_result.md 存档，Codex 不得编造数字**。
- 验收口径：只读路径网关额外开销 P99 < 5ms / 请求（fake executor 口径，主控实测为准）；Prometheus 能抓 /metrics、Grafana 六面板出图由主控用 observability compose 验证。

### T25 白名单（白名单外一律不动）
新增：internal/metrics/{metrics.go,doc.go,metrics_test.go}、internal/executor/{pool_stats.go,pool_stats_test.go}、internal/pipeline/{observer.go,t25_observer_test.go,t25_bench_test.go}、internal/mcpserver/t25_metrics_test.go、examples/observability/{prometheus.yml,grafana_dashboard.json,docker-compose.observability.yml,README.md}、docs/perf/README.md。
修改：go.mod（仅 require 增 prometheus/client_golang v1.22.0 一行）、internal/pipeline/{types.go,pipeline.go}、internal/bootstrap/bootstrap.go、internal/mcpserver/http.go、internal/executor/{postgres.go,mysql.go}。
**受保护零改动**：tests/corpus/decision_cases.json 与 ExpectedCases=252/ExpectedRuns=353、所有规则与判定逻辑、web/ 前端源码与 internal/webui/dist、go.sum（主控补）、其余文件。单位无网无 Go，禁止 go get/tidy/build，只做文本级静态自审并在回传说明里注明“未本地构建/运行，需主控端验证”，gofmt 由主控执行。

## T25.1 审批页 + 脱敏页前端补齐（纯前端，不改 Go；补 T17 菜单占位遗留）

### 25.1.1 背景、定位与编号避让
T17 给 `/approvals`、`/mask-rules` 挂了 PlaceholderPage（ticket 误标 T22），而 T22 只交付 Agent/数据源/权限/规则四个配置页，这两页一直遗留为"建设中"占位。**后端 T16 接口、前端 api 层、types、左侧菜单均已就绪，本单只写两个页面 UI 并接线，不碰任何 Go 代码。** 编号避让：本单为 **T25.1**，不占用第 8 章 v0.2 的 T26（在线 Live Demo）/T27。
两条语义边界必须写进页面文案、不得过度承诺：①审批 decide 只改变审批单状态并留痕，**v0.1 点"通过"后不会自动重放该条 SQL**（自动重放/完整审批流属 v0.2）；②脱敏 v0.1 仅支持 `sensitive_type=phone/email`、`algo=mask`，其余枚举后端 `mask.NewRedactor` 直接返回错误，前端不得提供这些可选项。

### 25.1.2 改动文件白名单（白名单外一律不动）
- 修改：`web/src/constants/labels.ts`（**只追加**三个 meta，不改任何已有条目）、`web/src/pages/Approvals.tsx`（整体替换 4 行占位）、`web/src/pages/MaskRules.tsx`（整体替换 4 行占位）、`web/src/styles.css`（**只纯追加**类名，不改既有规则）。
- 新增（按需，可合并进页面文件；若拆则参照 `pages/audit/` 子目录）：`web/src/pages/approvals/ApprovalDetailDrawer.tsx`、`web/src/pages/approvals/ApprovalDecideModal.tsx`、`web/src/pages/maskrules/MaskRuleFormDrawer.tsx`。
- **禁止改动**：`routes/menu.tsx`（菜单/路由 T17 已注册好）、`api/approvals.ts`、`api/maskRules.ts`、`api/types.ts`（字段已齐，确需纯展示类型只允许文件末尾追加、不得改既有字段）、所有 Go 文件、`package.json`（不引任何依赖）、其余任何页面。

### 25.1.3 labels.ts 追加（形态对齐既有 agentLevelMeta，`as const`；取标签统一用既有 `configLabel`）
- `approvalStatusMeta`：`pending {label:"待审批",color:"orange"}`、`approved {label:"已通过",color:"success"}`、`rejected {label:"已拒绝",color:"error"}`、`expired {label:"已过期",color:"default"}`。
- `sensitiveTypeMeta`：`phone {label:"手机号",color:"blue"}`、`email {label:"邮箱",color:"cyan"}`。
- `maskAlgoMeta`：`mask {label:"打码",color:"blue"}`。

### 25.1.4 审批页 Approvals.tsx
数据：`listApprovals({status?,page,page_size})` → `PageResp<ApprovalView>`；`decideApproval(id,{decision:"approve"|"reject",comment?})`。ApprovalView 字段照抄 types：`id/audit_id?/agent_id?/sql_raw?/reason?/status/approver?/decided_at?/created_at/updated_at`。
- `PageContainer title="审批"`，subtitle="模型触发的转人工审批单；v0.1 记录审批结论与留痕，通过后不会自动重放 SQL（完整审批流在后续版本）"。
- 工具条：状态筛选 Segmented/Select（全部 "" / pending 待审批 / approved 已通过 / rejected 已拒绝 / expired 已过期），切换重置到第 1 页；"共 N 条"；刷新按钮（loading 时禁用）。
- **服务端分页**：默认 page_size=20，可选 20/50/100；严格照 `Audit.tsx` 的 mountedRef + AbortController + 自增 sequence 防竞态、卸载即取消（取消判定用 `pages/config/utils` 的 `isCanceled`，禁用 axios.isCancel）；失败显示 Alert + 重试并保留旧数据；Table 走 loading。
- 列（rowKey="id"）：①审批单号 id，等宽 `<code>`，可用 `copyText` 复制；②Agent=agent_id，空显 —；③状态 Tag（color/text 取 approvalStatusMeta + configLabel）；④待审 SQL=sql_raw，ellipsis+Tooltip 全文（等宽、保留换行），空 —；⑤原因/备注 reason，ellipsis，空 —；⑥审批人 approver，空 —；⑦创建时间 created_at 用 `formatDateTime`；⑧决定时间 decided_at 用 formatDateTime，空 —；⑨操作：**仅 status==="pending"** 显示 link「通过」(primary) 与「拒绝」(danger)，其余显 —。
- 通过/拒绝 Modal：Descriptions 显示单号/Agent/状态；SQL 只读滚动块（等宽 pre-wrap、最高 240px 滚动）；TextArea 审批意见 comment 可选；提交 `decideApproval(id,{decision: 通过?"approve":"reject", comment})`，按钮 loading；成功 message.success 并关闭、重拉当前页；后端 409 `approval is no longer pending` 用 `apiErrorMessage` 提示并刷新；非 pending 行不渲染按钮（纵深防重复）。
- 详情 Drawer（宽 600–720）：Descriptions 展示全部字段（含 audit_id、updated_at）+ SQL 全文 pre-wrap，只读。空态 Empty"暂无审批单"。

### 25.1.5 脱敏页 MaskRules.tsx
数据：`listMaskRules({datasource_id?,page,page_size})`/`createMaskRule`/`updateMaskRule(id,input)`/`deleteMaskRule(id)`；数据源下拉 `listDatasources({page:1,page_size:100})`。MaskRuleView/Input 照抄 types：`id/datasource_id?(可空=全局)/table_name/column_name/sensitive_type/algo`(+created_at/updated_at 只读展示)。
- `PageContainer title="脱敏"` subtitle="结果集敏感列打码规则：查询返回前对命中列打码（v0.1 支持手机号/邮箱）"，extra 主按钮「新增脱敏规则」(PlusOutlined)。
- 顶部 info Alert："规则按 数据源(留空=全局) + 表名 + 列名 精确匹配；v0.1 仅支持手机号、邮箱与打码算法；保存后对该数据源下一次查询生效。"
- 筛选：数据源 Select（allowClear、showSearch，optionFilterProp=label，label=`name · db_type`，清空=查全部含全局），切换重置第 1 页；刷新。
- 服务端分页（同审批页范式，20/50/100、防竞态、失败重试、loading）。列：规则 ID(等宽)、数据源 datasource_id（空显"全局" default Tag）、表名 table_name(`<code>`)、列名 column_name(`<code>`)、敏感类型 Tag(sensitiveTypeMeta)、算法 Tag(maskAlgoMeta)、更新时间 updated_at(formatDateTime)、操作（编辑/删除）。
- 新增/编辑 Drawer（宽 520，Form layout=vertical，destroyOnClose）：数据源 Select allowClear（placeholder"留空表示全局规则"）；规则 ID——新增必填、前端默认建议 `msk_`+时间戳 base36+6 位随机（可改，提示小写字母/数字/下划线/连字符），编辑只读回显；表名 table_name 必填（placeholder 精确表名如 users）；列名 column_name 必填（placeholder 命中列名如 phone，按列名精确匹配不做模糊）；敏感类型 Select 必填，**仅 phone 手机号/email 邮箱**，默认 phone；算法 Select 必填，**仅 mask 打码**，默认 mask，旁注"哈希/区间等算法在后续版本"。
- 前端预校验：id/table_name/column_name 必填且首尾无空格（违则 form.setFields 红字）；sensitive_type/algo 必须在枚举内。提交组装 MaskRuleInput **只含这 6 个字段、禁止多发**（后端 DisallowUnknownFields；datasource_id 为空传 null/省略）；成功 message、关 Drawer、刷新；422/409 用 apiErrorMessage 展示。
- 删除走 Popconfirm"删除后该列将不再脱敏，确认删除？"，成功刷新。空态 Empty"暂无脱敏规则，点右上角新增"。

### 25.1.6 样式 / 主题 / 健壮性
复用 PageContainer、tokens 深色主题与既有 `.cfg-`/`.audit-` 类；新类名统一前缀 `.apv-`（审批）、`.msk-`（脱敏），纯追加、不覆盖全局、不硬编码色值（取 theme/tokens 或 antd Tag 语义色）；深浅主题都可读；SQL 块等宽可换行不撑破；窄屏 Table `scroll={{x}}`。所有异步按钮进 loading；卸载丢弃未完成请求；取消只用 `error.code==='ERR_CANCELED'`；不新增依赖。

### 25.1.7 静态自审铁律（单位无 Node/Go）
TS6133 零容忍（无未用 import/变量）；请求体严格对齐 DTO、无多余字段；不改 Go、不跑 go/npm 构建；回传说明注明"未本地编译/构建，需主控端验证"。

### 25.1.8 主控验收门（豆包本机）
- web 目录 `npm typecheck`（或 tsc）0 错、`npm run build` 通过，dist 由主控重新构建后 go:embed 进单二进制；
- 起服务 + 全新 sqlite 浏览器黑盒：审批页——空态；主控向 sqlite `approvals` 造 pending/approved/rejected/expired 各一条，列表/状态 Tag/状态筛选/分页正确；通过一条 pending→approved 且 approver=当前管理员、decided_at 有值、操作按钮消失；拒绝→rejected；对已决单重复 decide 被 409 拦并友好提示；详情 Drawer 字段齐全。脱敏页——新增 phone 全局规则与 email 绑源规则各一条、列表正确；编辑改类型/列；Popconfirm 删除；留空=全局；非法枚举被前端拦下；刷新后持久。
- 两页不再出现"建设中/T22"占位；其余 7 个页面零回归；浏览器控制台无有效报错。

### 25.1.9 本单不做
审批通过后自动重放 SQL、多级/会签审批流、审批通知催办；身份证/银行卡与 hash/range/block 脱敏；脱敏正则/阈值自定义；任何 Go 后端改动；在线 Live Demo（第 8 章 T26）。

## T25.2 v0.1-RC1 静态评审问题修复（Codex 整体验证 VERIFY_REPORT 收口；分 A/B 两单串行）

### 25.2.1 背景、裁决与交付方式
t25.1 全量快照经 Codex 只读交叉评审产出 `VERIFY_REPORT.md`（无 P0，8 项 P1、5 项 P2）。主控已逐条对照源码裁决（非照单全收）。本单 **T25.2** 为 v0.1 发布候选（RC1）收口，**分 A、B 两组串行交付**：A 组为安全核心（纯 Go + 测试），先做、先验收合入；B 组为管理/展示/文档（Go 小改 + 前端 + 示例），在 A 合入后的代码上做。两组均沿用既定流程：Codex 只写文件 + 文本级静态自审（单位无 Go/Node/网络，必须注明"未本地构建/运行，需主控端验证"），主控本机编译、`go test -race`、252 语料回归、浏览器黑盒后合入。除本单明确改动外，不得改动任何冻结契约、规则判定、252 语料与前端依赖。

裁决总表：

| 报告编号 | 裁决 | 归属 | 一句话 |
|---|---|---|---|
| P1-01 会话跨请求复用 | **降级 v0.2**（v0.1 生产路径不可达：MCP/演示台均不暴露 SessionID/事务参数） | 仅文档澄清（B 组 README） | v0.1 不支持跨请求事务，SessionID 为内部预留 |
| P1-02 同秒轮换 Key 缓存 | 必修 | A5 | bound server 缓存键加入 API Key 哈希 |
| P1-03 列授权双口径 | 必修 | A1 | 列授权统一为 R010 投影列口径，删除旧 AuthorizeColumns |
| P1-04 脱敏丢表维度/同列挡死 | 必修（口径已定：v0.1 按列名） | A2 后端 + B7 前端 | 运行时按列名归并、不再 fail-closed；表维度 v0.2 |
| P1-05 审批并发非原子 | 必修 | A3 | 审批决定改原子 CAS，竞争方返回 409 |
| P1-06 过期时间无法清空 | 必修 | B1 | DTO 三态（缺失/显式 null/有值） |
| P1-07 大屏日界错位 | 必修 | B2 | 统一按 UTC 自然日 00:00 切窗 |
| P1-08 审批审计/建单非原子 | 必修 | A4 | 建单失败时审计/响应/指标口径一致 |
| P2-01 AbortSignal 未透传 | 修 | B6 | list API 接收并透传 axios signal |
| P2-02 访问日志缺 status | 修 | B3 | 管理 API 访问日志记录最终 HTTP 状态码 |
| P2-03 classifyRoute 误归类 | 修 | B4 | 无 ID 资源的未知 action 归 `/other` |
| P2-04 示例端口 8650 | 必修（影响接入） | B5 | 面向用户示例/文档统一默认 7780 |
| P2-05 前端无自动化测试 | 后置 v0.2 | 不做 | v0.1 以主控浏览器黑盒矩阵覆盖 |

### 25.2.2 全局口径冻结（A、B 都必须遵守）
1. **列授权口径（P1-03）**：v0.1 列级白名单**只约束最终返回给调用方的投影列（SELECT 目标列）**；WHERE/JOIN/ON/GROUP BY/ORDER BY 等谓词与关联引用列**不参与** v0.1 列级拦截。"借谓词推断敏感列"属 v0.2。多表 JOIN 的列级白名单 v0.1 不做（表级授权通过即放行投影），v0.2 再表带归属。
2. **脱敏口径（P1-04）**：v0.1 运行时匹配维度 = **数据源作用域（全局 + 本数据源合并）+ 结果集列名**；`table_name` 字段在 v0.1 **存储但不参与匹配**，表 + 列精确匹配属 v0.2。任何情况下脱敏编译失败都**不得让正常查询失败**（除"空列名/非法枚举"这类本应在保存期拦下的配置硬错误外）。
3. 不新增第三方依赖、不改 `go.mod`/`web/package.json`、不改 252 语料与既有规则判定结果。

### 25.2.3 A 组规格（安全核心，纯 Go + 测试）

**A1（P1-03）列授权统一为投影列口径**
- 现状：`internal/pipeline/pipeline.go` 静态闸（约 166–183 行）在引擎评估之外，又调用 `policy.AuthorizeColumns(run.ast, run.policy)`；该函数遍历 `ast.Columns`（含 WHERE/JOIN 全部引用、无表归属），并对 `ast.Tables` 每张表逐一套用，导致谓词列被当返回列误拦、JOIN 必误伤。列授权在 R010（`internal/rules/generic.go`，消费 `ast.Operations` 的 `SELECT_COLUMN:*` 投影列信号，仅单表约束生效）已正确实现，旧调用是冗余且口径错误的第二套。
- 目标：①删除 pipeline 静态闸中 `AuthorizeColumns` 调用及其追加 `POLICY_COLUMN` Hit 的整段逻辑（删除后不再 import 仅因此使用的符号）；②删除 `internal/policy/columns.go` 中的 `AuthorizeColumns` 函数；`FilterColumnsForSchema`（动态元数据 schema 裁剪用）及其仍被使用的辅助函数保留；③删除 `internal/policy/policy_test.go` 中仅针对 `AuthorizeColumns` 的测试用例（`TestAuthorizeColumns` 及其专用数据），不得删 `FilterColumnsForSchema` 的测试。
- 验收断言（新增 `internal/pipeline` 集成测试，走完整 `Pipeline.Process`，配单表列级 ACL）：N106 `SELECT id,name,amount FROM orders WHERE user_id=1` 在 id/name/amount 已授权时**必须 allow**（user_id 为谓词列不拦）；`SELECT salary FROM employees`（salary 未在投影白名单）必须被 R010 判 deny；两表 JOIN 在两表均有表级授权时不因列误拦；252 语料回归结果不变。

**A2（P1-04）脱敏按列名确定性归并、消除同列挡死 + 保存期唯一性**
- 运行时归并（`internal/bootstrap/bootstrap.go` 的 `redactorBuilder.RedactorFor` 与 `internal/mask/redactor.go`）：①最终生效规则按键 = 归一化列名（沿用 `normalizeColumnName`）；同一数据源加载到的"全局规则 + 绑源规则"中，**绑源规则覆盖同列全局规则**；同列同 `sensitive_type/algo` 多条去重；同列 type 冲突（phone 与 email）时绑源优先、同为一级时按确定性格式（稳定排序后取其一，建议 phone 优先），**任何情形都不得返回 `ErrDuplicateMaskColumn` 让查询失败**。②`NewRedactor` 对同列名改为确定性归并而非报错；"空列名 / 非 phone|email / 非 mask"仍 fail-closed（这些是保存期就该拦下的硬错误）。③`table_name` 不传入、不参与匹配。
- 保存期唯一约束（`internal/adminapi/handler.go` 的 `maskRulesCreate/maskRulesUpdate` + `validateMaskInput`）：v0.1 唯一性键 = **数据源作用域（全局记为空串）+ column_name**（忽略 table_name）。新增/编辑后若同作用域已存在同列名的另一条规则，返回 **409** 与明确中文提示（如"该数据源下此列名已存在脱敏规则，v0.1 同列仅支持一条规则"）。
- 验收断言（`internal/mask` + `internal/bootstrap` + adminapi 测试）：全局 `phone` 与绑源 `phone` 共存时编译成功且绑源生效；同列 phone/email 两条不报错、结果确定；历史遗留的 users.phone/orders.phone（同列不同表）不再让 RedactorFor 报错；新增同列第二条被 409；非法枚举仍 fail-closed。

**A3（P1-05）审批决定原子化（CAS）**
- `internal/store/approval_repository.go` 新增原子决定方法（如 `DecidePending(ctx, id, status, approver, comment, decidedAt)`）：单条 `UPDATE approvals SET status=?, approver=?, decided_at=?, reason=?, updated_at=CURRENT_TIMESTAMP WHERE id=? AND status='pending'`，以 `RowsAffected` 判定：=1 时回读返回更新后记录；=0 时返回可映射 409 的冲突错误（区分"记录不存在 404"与"已非 pending 409"，可先 Get 判存在再据 RowsAffected 判 409）。
- `internal/adminapi/handler.go` 的 `approvalsDecide` 改为调用该原子方法，删除"先 Get 判 pending 再无条件 Update"的读改写；`RowsAffected=0` 且单据存在 → **409 `approval is no longer pending`**；不存在 → 404；decision 非 approve/reject → 422 保持不变。
- 验收断言：两个并发决定同一 pending 单，**恰好一个 200、一个 409**，最终 status/approver 唯一确定（`-race` 下跑）；重复决定稳定 409。

**A4（P1-08）审批分支审计/建单口径一致**
- 现状（`internal/pipeline/pipeline.go` 的 `approve`）：先写一条 decision=approve 审计（`run.audited=true`），再 `Approvals.Create`；Create 失败或返回空 ID 时 `finish` 把响应改成 deny/error，但因 audited 短路不再补记，导致"响应/指标=deny、库里唯一审计=approve、且无审批单"。
- 目标（Codex 可在两种实现中择一，须满足行为契约）：①推荐调换顺序——先创建 pending 审批单（audit_id 暂空），成功后再写 approve 审计并把 audit_id 回填审批单；或②保留现顺序但 Create 失败/空 ID 时显式追加一条关联的 error 审计事件，使最终事实=error。
- 行为契约（测试必须断言）：当 `Approvals.Create` 返回错误或空 ID 时，最终 `Response.Decision` 非 approve、Prometheus 不记 approve、审计中**不存在**与响应矛盾的"唯一 approve 成功"记录（应为 error，或 approve 后紧跟可关联的 error 事件）；正常成功路径仍为"审批单 pending + approve 审计 + 返回 ApprovalID"。

**A5（P1-02）bound MCP server 缓存键加入 API Key 哈希**
- `internal/mcpserver/http.go` 的 `agentServerKey` 不得只依赖秒级 `UpdatedAt.UnixNano()`：改为键中包含 `agent.APIKeyHash`（只含哈希、绝不含明文 Key），推荐 `agent.ID + "|" + agent.APIKeyHash`（可再拼 UpdatedAt，但 APIKeyHash 必须在键中）。`servers` 与 `limiters` 两个 map 使用同一新键。轮换 Key 后新哈希 → 缓存未命中 → 用新明文 Key 重建 bound server；旧 server 随容量/淘汰自然回收。
- 验收断言（`internal/mcpserver/http_test.go`）：首次调用建 server；**同一 SQLite 秒内**轮换 Key（UpdatedAt 不变）后，新 Key 调工具成功、旧 Key 在认证层 401，且不复用绑定旧明文 Key 的 server；明文 Key 不出现在日志（沿用现有断言）。

### 25.2.4 A 组改动文件白名单（白名单外一律不动）
- 改：`internal/pipeline/pipeline.go`、`internal/policy/columns.go`、`internal/mask/redactor.go`、`internal/bootstrap/bootstrap.go`、`internal/adminapi/handler.go`、`internal/store/approval_repository.go`、`internal/mcpserver/http.go`。
- 测试（按需新增/修改）：`internal/pipeline/pipeline_test.go`、`internal/policy/policy_test.go`（仅删 AuthorizeColumns 用例）、`internal/mask/redactor_test.go`、`internal/bootstrap/*_test.go`、`internal/store/*approval*_test.go`、`internal/adminapi/*_test.go`、`internal/mcpserver/http_test.go`。
- 禁改：model 冻结结构（A5 复用已有 `Agent.APIKeyHash`，不新增字段）、规则引擎与 252 语料、parser、executor、所有前端、`go.mod`、examples、README（README 归 B 组）。

### 25.2.5 B 组规格（管理/展示/文档；A 合入后再做）
- **B1（P1-06）过期时间三态**：在 `internal/adminapi/dto.go` 为 `agentUpdateInput.ExpiresAt` 引入可区分三态的可空时间类型（如 `nullableTime`，实现 `UnmarshalJSON`：字段缺失 `Present=false`；显式 `null` → `Present=true, Value=nil`；RFC3339 有值 → `Present=true, Value=&t`，非法时间返回错误）。`handler.go` Agent 更新改为仅当 `Present` 时赋值（显式 null → 写 SQL NULL，复用仓储已支持的置空）；字段缺失保持原值。补"设过过期→编辑清空→永不过期"回归。`agentCreateInput` 维持现状。
- **B2（P1-07）大屏自然日日界**：`internal/store/dashboard_repository.go` 统一锚定 **UTC 今日 00:00**，窗口起点 = today−(days−1) 天 00:00、终点 = today+1 天 00:00（半开区间），趋势补零输出从起点到 today 共 days 个日期（含今天）；上一对比窗口按相同日界整体平移。断言：`sum(trend 各 decision)==KPI 对应计数`、当天数据进入趋势末柱、首日为完整自然日。
- **B3（P2-02）访问日志补 status**：管理 API 访问日志（`internal/adminapi/handler.go` 中间件，约 138–150 行）用 statusRecorder 包装 ResponseWriter 记录最终状态码，日志字段含 method/path/**status**/耗时/管理员；注意 panic/recover 路径不重复写 header。
- **B4（P2-03）classifyRoute**：`internal/mcpserver/http.go` 中 auth/audit/dashboard/playground 等无 `{id}` 资源，静态 action 不匹配时归 `/other`，不得归 `/{id}`；补表驱动用例。
- **B5（P2-04）默认端口统一 7780**：改 `examples/mcp/streamable_http_mcp.json` 的 url 为 `http://127.0.0.1:7780/mcp`；核对 `examples/config.example.yaml`、`README.md` 中面向用户的默认端口/接入示例，凡与最终默认 7780 不一致处统一（或明确要求同步改 `http_listen`）。**不改** SPEC 第 4 章 T15 的历史描述文本、**不改** `http_test.go` 中测试自选的监听端口。
- **B6（P2-01）请求取消透传**：`web/src/api/*.ts` 的各 list 函数增加可选 `signal?: AbortSignal` 并传入 axios config；`Agents/Datasources/Policies/Rules/Approvals/MaskRules.tsx` 调用时传 `controller.signal`（取消判定仍统一用 `isCanceled`）。
- **B7（P1-04 前端对齐列名口径）**：脱敏页与 `MaskRuleFormDrawer`：顶部 Alert/subtitle 文案改为"按 **数据源(留空=全局) + 列名** 匹配；v0.1 仅手机号/邮箱 + 打码；表 + 列精确匹配在后续版本"；表单中"表名"输入框 v0.1 **移除或置灰并标注"后续版本生效，v0.1 按列名匹配"**，提交体不再强造 table_name（历史值列表可只读展示并标"预留"）；其余字段/枚举/校验不变。
- **P1-01 文档澄清**：README 增加一句明确"v0.1 的 MCP 不暴露跨请求会话/事务参数，`SessionID` 为内部预留；多语句事务随受控写在 v0.2 提供"。

### 25.2.6 B 组改动文件白名单
- Go：`internal/adminapi/dto.go`、`internal/adminapi/handler.go`、`internal/store/dashboard_repository.go` 及其 `_test.go`、`internal/mcpserver/http.go`（仅 classifyRoute）及其测试。
- 文档/示例：`examples/mcp/streamable_http_mcp.json`、必要时 `examples/config.example.yaml`、`README.md`。
- 前端：`web/src/api/*.ts`（仅 list 签名加 signal）、`web/src/pages/{Agents,Datasources,Policies,Rules,Approvals,MaskRules}.tsx`、`web/src/pages/maskrules/MaskRuleFormDrawer.tsx`；不新增依赖、不改 types 既有字段（table_name 字段定义保留）。

### 25.2.7 主控验收门（A、B 分别过，最后 RC 总验）
- Go：`gofmt` 干净、`go vet ./...`、`go test -race -count=1 ./...` 全绿、双 cmd 可构建；252/353 决策语料结果与 t25.1 完全一致（危险漏拦 0、误拦不增）。
- A 组专项：本规格 A1–A5 所列新增测试全部通过（N106 完整流水线 allow、投影越权 deny、JOIN 不误拦；脱敏同列归并不挡查询 + 409；审批并发恰一 200 一 409；审批建单失败审计=error；同秒 Key 轮换新 Key 成功旧 Key 401）。
- B 组专项：过期可清空；大屏趋势合计=KPI 且含当天；访问日志含 status；未知 action 归 /other；示例端口 7780 可直接接入；切换列表/卸载无网络竞态覆盖；脱敏表单与文案为列名口径。
- 前端：`npm run typecheck` 0 错、`npm run build` 通过，主控重建 dist 并 go:embed；浏览器 9 页黑盒零回归、控制台无有效报错。
- RC 总验补做 VERIFY_REPORT 第 4 节剩余动态项：真实 PG16/MySQL8 E2E（超时/只读/截断/Explain，会话事务项除外）、MCP 双承载 + 50 并发不串号 + 401/429/4MiB/畸形 JSON、脱敏 JOIN/别名作用域、docker compose/systemd 部署演练、/metrics 七指标与 Grafana。全绿后打 tag `v0.1-rc1`。

### 25.2.8 本单明确不做
跨请求会话/多语句事务的 get-or-create 会话池（P1-01，随 v0.2 受控写）；谓词敏感列推断、JOIN 表带归属的列授权；脱敏表 + 列精确匹配、hash/range/block 算法与更多敏感类型；前端自动化测试框架（P2-05）；SSO/HA/国产库/审批自动重放等第 8 章 v0.2 项。

### 25.2.9 主控对 Codex 只读设计评审的裁决（2026-09-16，A 组实现以此为准；与 25.2.1–25.2.8 冲突处，以本小节为准）
Codex 对 A1–A5 做了带源码走查的只读评审（评审全文存档 `transfer/reviews/M1-A-design-review.md`，会话 rollout 可溯）。主控逐条裁决如下。总原则：安全判定遵循 fail-closed（宁可误拦不可漏放）；纯工程实现/并发/事务以 Codex 意见为准；产品版本范围（JOIN 列归属、迁移、限流语义）由主控按 v0.1"窄而深"口径定。

1. **A1 列授权——删静态闸 + 必须堵住 `SELECT *` 绕过（收紧，扩白名单允许改 R010）**。Codex 查实：R010 现状对单表显式投影列闭环，但 ①`SELECT *`/星号投影被无条件放行（即使该表配了精确列白名单，执行器仍返回全部列）；②无 `SELECT_COLUMN` 信号时回退到混入谓词列的 `ast.Columns`，会重新误拦谓词列；③`len(ast.Tables)!=1` 即整体放弃列 ACL。裁决：
   - **必修（本单）**：(a) 单表且该表配置了"非全列"的精确列白名单时，星号投影（`SELECT *`/`tbl.*`）一律 **deny（fail-closed）**，命中原因明确（如"该表已启用列级白名单，不允许 SELECT *，请显式列出已授权投影列"）；该表仅表级授权、无列白名单时 `*` 正常放行。(b) R010 无投影列信号时**不得回退到含谓词列的 `ast.Columns`**；改为该查询不触发列白名单拦截（列授权层放行，交由表级授权及其它规则）。星号必须能被投影信号独立识别：优先复用 parser 现有 AST 标记；仅当现状无法区分星号时，允许对 `internal/parser` 的**投影列提取**做最小补充（只增投影/星号信号，不得改 SQL 解析结果、不得动其它规则与语料），并在完成说明中论证必要性。
   - **v0.1 已知限制（本单不修，写测试固化当前行为 + 代码注释，README 文案归 B/T25.4）**：多表 JOIN/自连接不做投影列的表带归属（两表表级授权通过即放行投影，沿用 25.2.2 冻结口径）；`AllowedTables` 含 `*`/`schema.*` 的通配表授权语义为"管理员显式放权匹配表的全部列"，此时精确列白名单不收紧。元数据驱动的列归属/`*` 展开逐列校验列入 v0.2。
   - N106 的"必须 allow"明确指**列授权层不拒绝**；集成测试须受控隔离其它规则（低扫描量、R008 限流阈值拉高、无长事务），在此环境断言完整 `Pipeline.Process` 最终 allow 且无 `POLICY_COLUMN`。
   - **语料纪律**：不得修改 353 语料来迁就新判定。若 R010 收紧导致任何语料用例期望变化，必须在完成说明中逐条列出（用例 ID / SQL / 原期望 / 新实际），由主控判定"合理收紧"还是"误伤"后再定；预期仅"精确列白名单 + 星号投影"这一狭窄形态受影响，纯表级授权语料必须 0 变化。
2. **A2 脱敏——采纳 Codex 工程方案，v0.1 用应用层 409（不加迁移）**：(a) 归并前先对**全部原始规则**逐条校验（空列名/非法 type/非法 algo 仍 fail-closed），防止非法规则被高优先级规则覆盖后静默消失；(b) 作用域优先级（绑源覆盖全局）在 `redactorBuilder.RedactorFor` 处理，`NewRedactor` 只做同层确定性归并；在 `redactor.go` 暴露归一化列名的只读包装供 bootstrap/adminapi 复用，禁止复制归一化算法；(c) 同层冲突稳定取舍：phone 优先于 email，再以 type/algo 字典序兜底，同列同配置去重，**不再返回 `ErrDuplicateMaskColumn`**（该导出符号可保留于不在白名单的 types.go）；(d) 保存期把 ColumnName 存为规范值、把空白 `datasource_id` 规范化为 `nil`（全局；当前 `''` 不会被 `IS NULL` 当全局加载，是必须修的正确性 bug），按"作用域+规范列名"查重，create/update 冲突（update 排除自身 ID）返回 409；(e) **`table_name` 从本单起允许为空**（放宽 `validateMaskInput`；DB 列 `NOT NULL` 可存空串，无需改表），为 B7 铺路；(f) v0.1 不新增 `0002` 唯一索引（`mask_rules` 现仅主键、无 scope+column 唯一索引；应用层 List→校验→写入存在低频竞态，但运行时已确定性归并，竞态不影响安全、不会让查询失败，仅管理面可能留多条同列配置）；**唯一索引 + 历史重复数据清理列入 T25.3/v0.2**（新增迁移必须用新版本号，严禁改已执行的 0001）。
3. **A3 审批 CAS——完全采纳 Codex 方案**：新增 `DecidePending(ctx, id, status, approver, reason *string, decidedAt time.Time) (model.Approval, error)`；单条 `UPDATE ... SET status=?,approver=?,decided_at=?,reason=COALESCE(?,reason),updated_at=CURRENT_TIMESTAMP WHERE id=? AND status='pending'`；**在同一事务内用 `tx` 回读/判存在**（本仓 SQLite `SetMaxOpenConns(1)`，事务内改用 `*sql.DB` 回读会单连接死锁）；RowsAffected=1→tx 回读、commit、200；=0→同 tx `SELECT 1` 判存在，不存在 `ErrNotFound`→404、存在新增 sentinel `ErrApprovalNotPending`→409，rollback；SQL/扫描/commit 错→500。`reason=nil` 保留原原因（兼容现状），非空才替换。handler 不再预读；非法 decision→422。并发测试用 ready barrier + start channel 汇聚，`-race` 下断言恰一 200、一 409，且不共享可变测试变量/不复用 ResponseRecorder。
4. **A4 审批审计/建单——采纳 Codex 单事务方案（推翻 25.2.3 的非原子选项②作为最终实现），扩白名单**。新增可共享事务的工作流端口（在 `internal/pipeline/types.go`，如 `CreatePendingWithAudit(ctx, approval model.Approval, log model.AuditLog) (model.Approval, model.AuditLog, error)`），由共享同一 `*sql.DB` 的 store 实现，复用 `internal/store/audit_log_repository.go` 的审计插入（**不得在 approval_repository 复制一套会漂移的 audit INSERT**）。流程：事务外完成审批 ID 生成、审计模型构造与全部输入校验 → BEGIN → 插 pending approval（audit_id=NULL）→ 插 decision=approve 的 audit_logs 取 ID → `UPDATE approvals SET audit_id=?,updated_at=CURRENT_TIMESTAMP WHERE id=? AND status='pending'` 要求 RowsAffected=1 → tx 回读审批单与审计 → COMMIT；任一步失败/空 ID 一律 rollback（不留 pending 孤儿单、不留 approve 审计），再走正常失败收口尝试单写一条 decision=error 审计；若 DB 本身不可写导致 error 审计也失败，允许"无审计 + 响应 deny + 指标非 approve + 返回错误"，不得虚构审计。保持 R008 reservation 恰好释放一次、StageAudit 延迟、`audited` 状态与既有指标口径。测试：pipeline fake 全失败矩阵（Create 错/空 ID/审计写失败/回填失败/commit 失败各自的 Response、指标、审计事实、reservation 释放）+ store 层真实 SQLite 事务测试（不得只用内存 fake）。
5. **A5 缓存键——servers 加哈希、limiters 维持 per-Agent（修订 25.2.3"两 map 同键"）**：`agentServerKey = agent.ID + "|" + agent.APIKeyHash`（不含 UpdatedAt、绝不含明文）；**`servers` 用该键，`limiters` 仍只用 `agent.ID`**。理由：限流是 per-Agent 配额语义，若 limiter 随 Key 哈希变，Agent 被限流后只要轮换 Key 即获全新桶，构成限流绕过；旧 Key 401 已由认证层按哈希查库保证（到不了 limiter），故 limiter 按 Agent.ID 既不削弱"旧 Key 失效"，又防配额绕过。容量 256 的 map 清理注释更正为"有界、批量淘汰"（非及时回收）。明文 Key 不入日志的断言扩到轮换与 bound server 构建链路（捕获完整 handler logger，断言新旧明文均不出现）。
6. **实现顺序（采纳 Codex）**：A3 → A4（同改 approval repository，连续做）→ A2（先运行时归并后保存期校验）→ A5 → A1（最后删旧闸并跑完整流水线 + 353 语料回归，最易把"消误拦"做成"扩大绕过"）。

**A 组白名单增补（在 25.2.4 基础上）**：可改生产文件追加 `internal/pipeline/types.go`（A4 端口）、`internal/store/audit_log_repository.go`（A4 共享事务/复用插入）、`internal/rules/generic.go`（**仅限 R010 投影列授权函数**，落实本小节 1 的 fail-closed 与不回退谓词列，不得改 R001–R009/R1xx/R2xx 与任何阈值）；如 handler 直接 List 不足以完成 A2 保存期规范化/查重，可新增/修改 `internal/store/mask_rule_repository.go`；`internal/parser` 仅在论证 R010 无法识别星号后做最小投影信号补充（默认不改）。测试追加 `internal/store/approval_repository_test.go`（A3/A4 真实 SQLite）、必要时 `internal/rules/generic_test.go`（R010 星号 fail-closed 与无信号不回退）。其余禁改项维持 25.2.4：model 冻结结构、executor、`go.mod`、全部前端、examples、README（A 组不动，README 已知限制文案归 B/T25.4）、353 语料（受影响只许列报不许擅改）。

### 25.2.10 主控对 Codex B 组只读设计评审的裁决（2026-09-16，B 组实现以此为准；与 25.2.5–25.2.8 冲突处，以本小节为准）
Codex 对 B1–B7、P1-01 做了定向只读评审（全文存档 `transfer/reviews/M2-B-design-review.md`）：B1/B2/B4/B5/P1-01 认可，B3/B6/B7 有保留，无"不认可"项。工程实现采纳 Codex 方案；3 个须澄清问题由主控裁决如下。实现顺序：B1→B2→B3→B4→B6→B7→B5/P1-01。

1. **B1（过期时间三态，认可）**：在 `internal/adminapi/dto.go` 定义值类型 `nullableTime{Present bool; Value *time.Time}` 并实现 `UnmarshalJSON`——字段缺失不调用反序列化（Present=false，保持原值）；显式 `null` 置 Present=true、Value=nil（清空，写 SQL NULL，仓储 `optionalTime(nil)` 已支持）；合法 RFC3339 赋值；非法时间让 `decodeJSON` 返回 **400**（请求解码错误，非业务 422）。`agentsUpdate` 仅在 Present 时更新 `agent.ExpiresAt`；`agentCreateInput` 继续用 `*time.Time`，不受影响。测试：已有过期时间后分别 PUT `{}`/`{"expires_at":null}`/合法值/非法值，断言保持/清空/更新/400 且原值不变。
2. **B2（大屏 UTC 自然日，认可）**：窗口统一为半开区间 `today=UTC(now) 00:00`、`start=today-(days-1)d`、`end=today+1d`，对比窗 `[start-days, start)`；KPI、趋势、分布、排行、风险、战报全部复用同一 `[start,end)`。SQLite 审计 `ts` 由 `CURRENT_TIMESTAMP` 写为 UTC 文本 `YYYY-MM-DD HH:MM:SS`，分桶沿用 `date(substr(ts,1,19))`，边界参数也统一成同格式 UTC 文本（不混 RFC3339 后缀做文本比较）。在 `DashboardRepository` 增加**实例级**可选 `now func() time.Time`（nil 用 `time.Now`），测试注入固定时刻；不改 `store.go`、不引入包级可变时钟（避免并行测试竞态）。断言：趋势从 start 到 today 共 days 柱（含今天、零日补齐）、`Σ趋势.Total=KPI.TotalRequests`、`Σ趋势.Deny=KPI.Blocked`、对比窗 delta 正确、边界 start-ε/start/end-ε/end。纯后端改动，前端 `web/src/api/types.ts` 与大屏字段契约不变。
3. **B3（访问日志记最终 status，有保留→采纳其口径，回答澄清③）**：statusRecorder 须同时实现 `WriteHeader` 与 `Write`，记录第一次最终状态码与 wroteHeader（仅覆写 WriteHeader 会漏隐式 200）；保持 recover/访问日志在最外层、recorder 向内传给 adminAuth。**panic 发生在写 header 前→recover 写 500、日志记 500；panic 发生在已 WriteHeader/Write 之后→不得重复写 header 或错误 JSON，日志记录实际已提交的状态码（协议上无法再改 500，验收按此，不强求记 500）**。该中间件仅属管理 Handler，不得改动 MCP 流式 `/mcp` 的 ResponseWriter 链；可参考 `internal/mcpserver/http.go` 的 `statusResponseWriter` 范式但不共用/不改它。测试：200/401/404/405/业务错误/写 header 前 panic=500/写 header 后 panic 不重复提交，日志含 method/path/status/latency/admin。
4. **B4（classifyRoute，认可）**：`auth/audit/dashboard/playground` 明确为"无 ID 资源"，第二段只接受各自已知静态 action，未命中（含已知 action 后再多附一段）一律归 `/other`；仅 `agents/datasources/policies/rules/mask_rules/approvals` 允许第二段归 `/{id}`；未知资源、非 /api/v1 路径按现状归 `/other`。表驱动测试枚举：四类合法 action、四类未知 action、合法 action 后附加段、六类 ID 资源、ID 资源合法 action、ID 资源未知 action、未知资源、非 API 路径。只改 `classifyRoute` 及相邻资源分类 helper，不碰 A5 的缓存键/认证/限流/MCP recorder。
5. **B5（默认端口 7780，认可）**：经核查面向用户处仅 `examples/mcp/streamable_http_mcp.json` 残留 `http://127.0.0.1:8650/mcp`，改为 `7780`；`examples/config.example.yaml`、README、Docker、可观测性示例现状已是 7780，**不做无意义改动**；确认 `agentsqlctl init-config` 生成模板为 `127.0.0.1:7780`。不得改 SPEC 第 4 章/T15 历史文本、`http_test.go` 自选监听端口、cmd/Docker/部署文档。
6. **B6（AbortSignal 透传，有保留→采纳补强）**：各 list API 统一签名 `listX(params = {}, signal?: AbortSignal)`，把 signal 放进现有 axios request config；不改 params 形状、不改 axios client、不引依赖。Agents/Datasources/Policies/Rules/Approvals/MaskRules 六个主列表调用传 `controller.signal`，catch 继续用 `isCanceled` 静默处理取消。**补强：Policies 的 Agent/数据源选项加载、MaskRules 的数据源选项加载也必须各自 effect 建 AbortController、相关调用共享同一 signal、cleanup 中 abort**，否则卸载竞态不闭环。`audit/dashboard/playground` 已支持 signal，不重复改。
7. **B7（脱敏前端对齐列名口径，有保留→主控裁决澄清①）**：后端 A2 已允许空 `table_name`、空白数据源归一 nil、按"数据源+规范列名"查重 409。前端：(a) **允许把提交类型 `MaskRuleInput.table_name` 改为可选 `table_name?: string`（字段保留不删，v0.2 预留；`MaskRuleView.table_name: string` 保留用于只读展示历史值）**——以此解决"不再强造 table_name"与"必填类型"的形式冲突；(b) 实现前先核对 `migrations/0001_init.sql` 中 `mask_rules.table_name` 约束：若 NOT NULL 则新建提交空串 `""` 占位，若可 NULL 则可省略该字段；编辑时回传 `record.table_name` 原值（保留历史、不清空、绝不伪造表名）；(c) 表单移除表名输入框，或置灰并标注"预留字段，v0.1 按列名匹配、不参与表+列匹配"；subtitle/Alert 统一为"数据源（留空=全局）+ 列名；v0.1 仅手机号/邮箱 + 打码；表+列精确匹配在 v0.2 提供"；(d) `datasource_id` 留空提交 null；409 明确提示"该数据源/全局范围下此列名已有规则"并保留抽屉与已填内容；(e) 前端不得复制后端列名规范化算法，仅按契约提交与展示。
8. **P1-01（README，认可）**：在 README"MCP 客户端配置"节三个客户端链接之后、文档章节之前补一句："v0.1 的 MCP 不暴露跨请求会话或事务参数，`SessionID` 仅为内部预留；多语句事务随受控写能力在 v0.2 提供。"

**B 组白名单增补（在 25.2.6 基础上，回答澄清②）**：允许测试落盘 `internal/adminapi/*_test.go`（可新建 `dto_test.go` 或扩 `adminapi_test.go`，覆盖 B1/B3）；`internal/store/agent_repository.go` 仅作只读证据（其 SQL 已支持 nil→NULL），**不进生产改动白名单、不改 store.go**；`web/src/api/types.ts`、`web/src/pages/overview/`、配置工具文件不改，唯一例外是 B7 所在的脱敏 api 类型文件允许把 `MaskRuleInput.table_name` 改为可选；`internal/mcpserver/http_test.go` 仅允许新增 classifyRoute 表驱动用例，不得改其监听端口测试数据；前端 `npm run build` 会刷新 checked-in 的 `internal/webui/dist`（go:embed 产物），该 dist 变更属预期、随本单提交。其余禁改项维持 25.2.6/25.2.8：安全引擎/规则/353 语料/model 冻结结构/executor/go.mod 不动。验收：Go 侧 gofmt/vet/全量 `-race`/353 语料逐字不变/双构建，前端 `npm run typecheck` 0 错、`npm run build` 通过，主控重建 embed dist，并按评审"9 页黑盒重点路径"做浏览器零回归；合入 main 后打 tag `t25.2b`。

## T25.4 开源发布硬化（门 2 全绿后，GA 候选；Codex 单 + 主控安全扫描）
> 前置：T25.2 A/B 合入、门 2 动态总验全绿（条件单 T25.3 的 RC 修复清零）。本单只做"对外发布"所需的授权、安全默认值、版本、物料、容器，**不改安全引擎、规则、252 语料、冻结契约**。
- **授权定稿（AGPLv3 双授权）**：`LICENSE` 由 Apache-2.0 换为 GNU AGPLv3 标准全文（官方文本由主控放入，Codex 不得自行缩写或改写许可证正文）；新增 `COMMERCIAL-LICENSE.md` 说明商业双授权（闭源集成、SaaS 商用、企业模块、保修与 SLA 需商业授权）与**商标保留**（可 fork 源码，但不得用 AgentSQL 名称/Logo 对外发行）；README 许可章节与徽章同步。
- **安全默认值（开源头号事故点）**：禁止内置固定 `AGENTSQL_SECRET` 与弱管理员口令；密钥缺失或为文档示例值时拒绝启动并打印明确指引（或首启生成随机密钥仅提示一次）；管理员口令未通过环境变量/首启设置时不得开箱即用（最小实现：无默认口令、首启必须设置）；examples/README/compose 不得出现可用真实凭据。
- **版本与物料**：版本号 dev→`v0.1.0`（`--version`、启动横幅、version/health 输出一致，保留 `-X` 注入能力）；新增 `CHANGELOG.md` 与 Release Notes（功能、限制、已知问题、v0.2 路线、会话事务在 v0.2 的说明）；开源 README（徽章、特性、5 分钟 docker compose 起 SQLite 版、首次登录引导、MCP 7780 接入 json、功能与数据库兼容矩阵、截图位、文档/社区链接、AGPL 说明）。
- **容器/部署**：Dockerfile 非 root、管理面不默认绑定 0.0.0.0、SQLite 持久化 volume、health/readiness；`examples/config.example.yaml` 默认端口与 7780 一致。
- **发布前检查（主控执行）**：git 历史密钥/内部信息扫描、`.gitignore` 复核；仓库由私有转公开须负责人确认；干净环境 5 分钟从零跑通复跑；Cursor/Claude 真实接入录屏；正式 tag `v0.1.0` + GitHub Release（源码 zip/tar + 校验和，容器镜像可选）。

### 25.4.1 主控对 T25.4 工程硬化评审的裁决（Codex 只读评审 + 主控拍板）
Codex 只读评审确认方案可实施，且发现 4 个必须补的真实缺口（HTTP MCP 硬编码 `dev`、Makefile 默认注入 `dev`、`examples/docker/.env.example` 与 `docs/DEPLOY.md`/`README.md` 含可直接使用的公开示例密钥与公开管理员口令）。主控逐条裁决如下，本小节为 T25.4 工程实现的权威口径。

1. **新增集中启动策略 `internal/config/security.go`（+`security_test.go`），低层 cipher 不承载生产策略**：
   - `InsecureModeFromEnv() (bool, error)`：未设/空=`false`；仅精确值 `"1"`=`true`；任何其它非空值直接报错 `AGENTSQL_INSECURE must be unset or exactly "1"`（拼错的 true/yes 不得静默当安全模式）。
   - `ValidateStartupSecret(secret, insecure)`：空→缺失错误；长度≠32 字节→长度错误；非 insecure 且精确命中公开值集合→拒绝并提示改用随机值、本地测试可设 `AGENTSQL_INSECURE=1`。**insecure 也不放行空值或非 32 字节**。
   - `ValidateAdminPassword(username, password, insecure)`（仅 `serve && console_enabled` 调用；`console_enabled=false` 不要求管理员口令；stdio `mcp` 不要求）：空→缺失（insecure 也不放行空口令）；非 insecure 时，trim 后 rune 数 <12、或 trim+忽略大小写后等于用户名、或精确命中弱口令黑名单→拒绝并提示使用 ≥12 字符的唯一 passphrase、本地测试可设 `AGENTSQL_INSECURE=1`；insecure 仅放行“非空弱口令”。
   - 公开值/弱口令**精确匹配、区分大小写（口令按 trim+小写比较）、不做 trim 密钥/子串/正则/熵评分**。v0.1 公开密钥黑名单集合仅 `0123456789abcdef0123456789abcdef`（仓库文档与测试唯一公开过的 32 字节可用值；占位符 `REPLACE_WITH_32_BYTE_SECRET_DO_NOT_COMMIT` 长度 37 已被长度检查拦截）；弱口令黑名单至少：`admin/password/password123/admin123/12345678/qwerty123/changeme/agentsql/change_me_agentsql_admin_2026!`。集合设计为常量切片便于后续追加。
   - 调用顺序前移：解析 INSECURE → 校验 SECRET → `config.Load` →（serve 且 console 启用）校验管理员口令 → **最后**才 `Assemble`/open/迁移，避免先建库迁移后才因凭据失败。`serve`、`mcp`、`agentsqlctl migrate` 对 SECRET 共用同一校验；`init-config/check-config/health/version` 不打开加密 store、不校验。
   - insecure 启用时向 stderr/结构化日志打印显著警告，但**绝不打印密钥/口令明文**；stdio 的 stdout 必须保持纯 MCP 协议（警告只走 stderr）。
   - **INSECURE 语义永久限定为“允许公开测试凭据”**，不得扩展为关闭认证、安全规则或 fail-closed 的总开关（在代码注释中写明）。
   - `internal/store/crypto.go` 只保留密码学长度/缺失语义，不加黑名单；因此 adminapi/bootstrap/store/auth/executor/pipeline 等低层包测试无需扩散开关，仅 `cmd/agentsql/main_test.go`、`cmd/agentsqlctl/main_test.go` 中走完整入口的既有用例显式 `AGENTSQL_INSECURE=1`，并在两文件与新增 `internal/config/security_test.go` 中补启动矩阵正反例。
2. **版本一致性（批准 Codex 对“只改 version.go 不够”的不认可）**：`internal/version.Version="v0.1.0"`（保留 `-X` 覆盖）；`internal/mcpserver/server.go` 的 `buildBoundServer` 硬编码 `dev` 与空版本回退一律改用 `version.Version`；`Dockerfile` `ARG VERSION=v0.1.0`；docker-compose 两处默认 `v0.1.0`；`Makefile` `VERSION ?= v0.1.0`；serve 与 stdio 启动日志记录 `version.Version`。验收须确认 `agentsql(--version|version)`、`agentsqlctl(--version|version)`、`/healthz`、stdio 与 HTTP MCP 的 `serverInfo.version` 全部为 `v0.1.0`；Docker 的 `VERSION` 不得留空（空串会经 `-X` 覆盖代码默认）。
3. **容器/部署收窄**：容器内 `examples/docker/config.yaml` **保持** `0.0.0.0:7780`（容器内必须如此，端口映射才能进入，禁止改成回环）；安全边界放在宿主绑定——compose 主服务端口改 `127.0.0.1:7780:7780`，demo profile 的 PostgreSQL `5432` 与 MySQL `3306` 也绑定 `127.0.0.1`（示例弱密码不得随 profile 暴露到局域网）；非 root、SQLite volume、healthcheck 已具备，不改镜像结构。`examples/docker/.env.example` 的 SECRET 与管理员口令**留空 + 生成指引注释**（VERSION 改 v0.1.0）；`examples/config.example.yaml` 补“生产禁用公开值/INSECURE 仅供本地”注释；`docs/DEPLOY.md` 三处公开凭据与 v0.1 构建示例替换为“生成你自己的随机值”，并写明“更换 SECRET 会使既有数据源密码无法解密、非无损轮换”。
4. **物料**：按评审提纲重写开源 `README.md`（徽章/特性/5 分钟 compose+SQLite/首凭据 Bash+PowerShell 生成与登录/stdio+HTTP(7780) 两例 MCP 配置/功能矩阵/兼容矩阵 MySQL8 与 PG14–18、元数据库 v0.1 仅 SQLite/v0.1 已知限制含 JOIN 投影口径与会话事务在 v0.2/截图位 `docs/screenshots/`/AGPL+商业双授权+商标）；新增 `CHANGELOG.md`（Keep a Changelog：Unreleased、v0.1.0 的 Added/Security/Deployment/Compatibility、已知限制、密钥保管提示、后置路线；T29–T31 明确标为企业版后置，不承诺进 v0.2 首批）。许可证正文（LICENSE/COMMERCIAL-LICENSE.md）主控已定，Codex 不得改写，只在 README 引用。
5. **白名单**：新增 `internal/config/security.go`/`security_test.go`；可改 `cmd/agentsql/main.go`、`cmd/agentsqlctl/main.go` 及对应 `main_test.go`、`internal/mcpserver/server.go`（及必要测试）、`internal/version/version.go`、`Makefile`、`Dockerfile`、`docker-compose.yml`、`examples/docker/.env.example`、`examples/docker/config.yaml`（仅注释/保持绑定）、`examples/config.example.yaml`、`docs/DEPLOY.md`、`README.md`，新增 `CHANGELOG.md`、可选 `docs/screenshots/.gitkeep`。**禁改红线**：`internal/rules|parser|pipeline|model|executor|mask`、`tests/corpus/*`（353 语料逐字不变）、`migrations/*`、`web/**` 与 `internal/webui/dist/**`、`go.mod/go.sum`、`LICENSE`、`COMMERCIAL-LICENSE.md`。
6. **验收门**：gofmt clean、`go vet ./...`、`go test -race ./...` 全绿且 `git diff --exit-code -- tests/corpus` 无变化、双构建；启动矩阵（缺 SECRET/错长度/公开值拒、强随机 SECRET+强口令过、弱口令拒、INSECURE 仅放行长度正确的公开 SECRET 与非空弱口令但不放空/错长度、console=false 无口令过、错误与日志不含凭据明文）；版本七处一致；`docker compose config` 通过。主控独立复跑上述项并用强随机 SECRET+强口令起实例做浏览器黑盒（登录与 9 页正常），通过后合 main、tag `t25.4`。

## T25.5 门2（G1）v0.1 动态总验与 RC 收口（tag v0.1-rc1 前）
> 前置：T25.4 已合并（tag t25.4）。依据 Codex 只读盘点评审（`transfer/reviews/M5-G1-design-review.md`）与 G1-a0 只读评审（`transfer/reviews/G1-a0-design-review.md`），主控裁决如下。除 G1-a0/G1-d 含明确授权的小生产/部署改动外，门2 以补测试与可观测物料为主，**不改规则引擎、判定语义、353 语料、migrations、web/dist、go.mod/go.sum**。

### 25.5.1 门2 盘点结论与主控裁决
1. **覆盖率口径**：核心安全链 parser/engine/rules/policy/pipeline/executor/mask **每包 ≥80%（留 2–3% 裕量）**。主控 2026-09-16 实测（`transfer/logs/cover-v01.out`）total 77.4%；engine 93.7、rules 90.5、policy 92.6、pipeline 84.9、mask 96.2、mcpserver 83.8、auth 96.9、metrics 95.5 已达标；**parser 79.4、executor 72.3 必须补到 ≥80%**。外围 adminapi(54.3)/webui(63.6)/store(74.0)/audit(79.7)/bootstrap(76.3)/cmd 补关键 fail-closed 与错误路径，不强行每包 80，整体 total 尽量贴近 80；model 为纯结构体 0% 不计。
2. **真实库矩阵**：被防护业务库 **PostgreSQL 14/15/16/17/18（必测 PG18）+ MySQL8**；v0.1 元数据/审计库仅 SQLite（PG 元库为 v0.2 T28）。testcontainers 仅出现在 `*_test.go`、不新增第三方依赖、无 Docker `t.Skip`；**Docker 探测仅在 daemon/socket 不可用时 Skip，镜像拉取/等待/配置错误必须 Fail（不得假绿）**。矩阵成本控制：连接/SELECT/截断/只读拒写/Explain/元数据全版本跑；pipeline 全版本跑“放行 / Explain 动态门 / 结果脱敏”；静态拒绝类与完整事务状态机仅 PG14+PG18；MySQL8 新增完整 pipeline E2E（放行、Explain、只读拒写、脱敏 JOIN）。
3. **MCP HTTP 加固（纯测试）**：现有 `internal/mcpserver/http_test.go` 已覆盖 401/50 并发/429/413/畸形 JSON/Key 轮换；加固为 N=10 个独立 Agent/Key/数据源、每个并发 5 次同步起跑；逐响应解码 JSON-RPC 断言 `id` 严格等于本请求 id、数据源集合严格等于自身单元素集合且不含任何他租户 `ds-*`、registry `serverCount/buildCount==N` 且第二轮不增；畸形 JSON/413 后断言 executor、audit **零触达**，再发合法请求证明未污染；补“恰好 4MiB 合法通过 / 4MiB+1 返回 413”边界。
4. **429 / 连接限流口径裁决**：v0.1 “连接限流” = 每 Agent QPS token bucket（HTTP 429，已实现）+ 业务库连接池 `max_conns_per_datasource`（护后端）；**HTTP in-flight 全局并发连接硬上限列入 v0.2**，不阻塞 RC（T15/SPEC 701 仅要求 QPS）。
5. **脱敏 E2E**：门2 JOIN/列别名/表别名作用域 E2E 按现契约（最终列名）验证；**别名换名绕过另由 G1-a0 收紧契约（见 25.5.2）**。
6. **指标 / Grafana**：代码恰七项指标 `agentsql_http_requests_total`、`agentsql_http_request_duration_seconds`、`agentsql_decisions_total`、`agentsql_rule_hits_total`、`agentsql_pipeline_stage_duration_seconds`、`agentsql_rejected_total`、`agentsql_pool_connections`；扩 `t25_metrics_test.go` 驱动一次 allow、一次 deny(R002)、一次 429 并装入 pool snapshot，抓 `/metrics` 断言七 family、关键 label、非零值。Grafana 六面板 JSON/prometheus.yml/compose 已有，**补 provisioning（`provisioning/dashboards/agentsql.yml` provider + `provisioning/datasources/prometheus.yml` 自动数据源）并在 compose 只读挂载**，实现一键出图。
7. **部署物料**：compose 的 SQLite 数据卷由 bind mount(`./data`) 改为**命名 volume（agentsql-data）**，规避 Linux 宿主目录被 root 创建、容器内非 root `agentsql` 用户不可写；DEPLOY/README 同步；systemd unit 已非 root。干净环境 5 分钟计时、Linux 真机 systemd/UID 演练、Cursor/Claude 四场景录屏为**人工/环境门**（主控用本机 Docker Desktop 尽量覆盖 compose/Grafana，Linux systemd 与录屏由用户完成）。
8. **fuzz**：主控实跑 `go test -run=^$ -fuzz=FuzzAssessFailClosed -fuzztime=30m ./internal/pipeline/`，连续 30 分钟无 panic、解析失败必 deny。
9. **串行拆单（一次在途一单，不并行 subagent，稳定优先）**：
   - **G1-a0**：结果脱敏“直接列引用/别名”源列兜底（含授权的小生产改动，规格见 25.5.2）；
   - **G1-a1**：PG14–18+MySQL8 真实库矩阵 + JOIN/列别名/表别名脱敏 E2E（含 a0 的直接别名真实库用例）+ Docker 探测收紧（纯 `*_test.go`）；
   - **G1-b**：MCP HTTP 边界与多租户隔离加固（纯 `internal/mcpserver/*_test.go`）；
   - **G1-c**：覆盖率补齐（纯 `*_test.go`，以 G1-a1 后 coverage 为输入，核心包到 ≥80% 并留裕量）；
   - **G1-d**：metrics 七指标 HTTP 断言 + Grafana provisioning + 命名 volume（`*_test.go` + `examples/observability/**` + compose/DEPLOY 小改）。
   全部合入后主控统一跑 gofmt/vet/`go test -race ./...`、353 语料逐字不变、Docker 矩阵、coverage 复核、30 分钟 fuzz、compose/Grafana 演练，再交人工门，全绿才 tag `v0.1-rc1`。

### 25.5.2 G1-a0 规格：结果脱敏“直接列引用/别名”源列兜底（T11 契约收紧）
**定性**：T11 原契约“只按最终结果列名、不依赖物理列名”是刻意设计（确定性、零误脱敏）。本单是**契约收紧**而非修 bug：堵住安全网关最易被 PoC 一眼看穿的 `SELECT phone AS mobile` 直接换名绕过；复杂血缘明确留 v0.2。评审确认工作量 M、零新增误判、不改规则/语料。

**匹配语义（fail-closed 但零误判）**：
- 某输出列命中规则当且仅当：其**最终结果列名**规范化命中（现状，优先）**或**其对应的**顶层直接列引用源裸列名**（去 schema/表别名限定、按现有 normalizeColumnName 规范化）命中；两者为“或”扩充。
- 仅当投影项表达式**根节点就是列引用**时才建立源映射：裸列 `phone`、限定列 `c.phone` / `schema.t.phone`、以及它们直接加别名 `… AS x`。
- 函数/运算/CAST/聚合/字面量/常量、UNION 分支、子查询/CTE/视图跨层、`*`/`t.*` 展开、多星号中间无法定位的槽位：**不做源兜底**，回退最终列名匹配（v0.2 血缘项）。

**数据结构与挂载**：
- `internal/model` 新增 `type DirectProjectionRef struct { Column string; Offset int; FromEnd bool }`，在 AST 增加字段 `DirectProjections []DirectProjectionRef`（parser 输出原始 identifier，规范化仍由 mask 层做，parser 不得反向依赖 mask）。
- 星号对齐：无星号全部从左 Offset；恰一个星号时，星号前显式项从左定位、星号后从右定位（`SELECT *, phone AS mobile` 的 mobile 安全定位到末列）；多星号仅首星号前/末星号后可定位，中间槽位置空；任何越界/重叠/长度与 `len(QueryResult.Columns)` 不符，**整份源映射作废，回退最终列名**，绝不猜位置。

**方言提取（严格根节点判定）**：
- PostgreSQL（pg_query JSON）：顶层 targetList 每项 ResTarget，仅当 `val` 根节点为 ColumnRef 才记录，裸名取 fields 最后一个 String；FuncCall/A_Expr/A_Const/CAST/聚合/下标等不记录；`A_Star` 记星号不记源；**从原始根 node 提取，不从解包 EXPLAIN 的 analysis root 提取**（避免 `EXPLAIN SELECT phone` 误映射）；不得复用会递归进表达式的 `postgresWalkProjection`，新增严格 helper。
- MySQL（vitess）：仅普通 `*sqlparser.Select`（排除 Union/EXPLAIN 包装），投影项为 `*sqlparser.AliasedExpr` 且 `Expr` 动态类型**恰为** `*sqlparser.ColName` 才记录，裸名取 `ColName.Name.String()`、忽略 Qualifier；`*sqlparser.StarExpr` 记星号；不复用递归 Walk 的 `mysqlProjectedColumns`。

**接口与接线（向后兼容）**：
- mask 新增可选接口 `SourceAwareRedactor interface { Redactor; ApplyWithSourceColumns(result model.QueryResult, sources []string) (model.QueryResult, RedactReport) }`；旧 `Apply(result)` 委托 `ApplyWithSourceColumns(result, nil)`；**不修改基础 Redactor 接口签名**，pipeline 做类型断言、不支持则走旧 Apply（既有 fake/builder/handler 无需批量改）。
- 最终列名优先、源列兜底；命中后 report 仍按实际结果列下标记录 `TouchedColumns/MaskedCells`，深拷贝与“未命中逐字节不变”契约不变。
- pipeline（pipeline.go 329-345）：据 `run.ast.DirectProjections` 与实际结果列数解析位置化 `sources []string`，redactor 实现新接口则调用之，否则旧 Apply；`engine.cloneAST` 复制新 slice。

**白名单（仅这些文件）**：`internal/model/model.go`、`internal/parser/postgres.go`、`internal/parser/mysql.go`、`internal/mask/redactor.go`（可含 doc.go 注释）、`internal/pipeline/pipeline.go`、`internal/engine/engine.go`；测试 `internal/parser/*_test.go`、`internal/mask/redactor_test.go`、`internal/pipeline/pipeline_test.go`、`internal/engine/engine_test.go`；文档 `docs/SPEC.md`（本节）、`README.md`（边界说明，**不得宣称“任何别名/完整血缘 DLP 不可绕过”**）。不改 go.mod/go.sum、rules、tests/corpus、migrations、web/dist。

**测试（先写预期失败再实现）**：
- parser 双方言表驱动：`phone`、`c.phone AS mobile`、三段限定；literal/function/operator/aggregate/CAST 不产生直引；`*`、`t.*`、`*, phone AS mobile`、多星号中间槽为空；UNION、EXPLAIN 不产生错误映射。
- mask：最终名不命中但源 phone 命中→打码；最终名规则仍先生效；最终名与源名冲突时最终名优先；映射长度错/空槽/越界仅回退最终名；入参仍深拷贝、未命中列逐字节不变。
- pipeline：SQL `SELECT phone AS mobile …`、fake executor 返回 `Columns:["mobile"]`、规则仅 phone，断言被打码。
- engine：cloneAST 新 slice 不共享。
- 验收门：gofmt clean、`go vet ./...`、`go test -race ./...` 全绿、**353 语料逐字不变且 FP 0.00%**、`TestRedactorMatchesFinalColumnNames` 原样通过、双构建；合 main、打存档 tag。

**v0.1 已知边界（写入 README/DEPLOY 与发布话术）**：脱敏优先按最终结果列名，对可确定位置的顶层直接列引用额外按源裸列名兜底；函数/运算/聚合/CAST、UNION、跨子查询/CTE/视图的内部重命名、无法定位的多星号投影不做血缘兜底；v0.1 脱敏不是完整 DLP，防绕行需结合只读数据库账号、列级权限、安全视图与审批。**完整表达式数据流/视图/UNION/CTE 血缘列入 v0.2（需 catalog 元数据、逐槽位血缘图、敏感度传播与 schema 版本缓存）。**

### 25.5.3 门2各单验收结论与非阻塞已知项（主控台账）
- **G1-a0 已合入**（tag `g1-a0`，merge `dd3876e`）：源列兜底上线，run-acceptance **ALL_GREEN**、353 语料逐字不变 FP 0.00%、双构建通过。
- **G1-a1 已合入**（tag `g1-a1`，merge `9262bfe`）：主控实跑 Docker 矩阵，executor 与 pipeline 的 PostgreSQL 14/15/16/17/18（PG14/18 含超时与完整事务状态机）+ MySQL8 **全部真实通过**；JOIN/列别名/表别名作用域脱敏、a0 `phone AS mobile` 源兜底真实库回归、表达式不兜底的 v0.1 边界均断言；Docker 探测收紧为“仅 daemon 不可达 Skip，镜像/容器/等待错误硬失败”。主控修正一处**测试断言**（短/长表别名一致性深比较误把非确定性 `LatencyMS` 纳入，比较前归零；非生产改动）。
- **G1-b 已合入**（tag `g1-b`，merge `539e174`）：门2清单③全部覆盖且 `go test -race` 全绿——无/错/旧 key（轮换后旧 key 立即 401 且不重置限流桶）、10 租户×5 请求并发隔离（JSON-RPC id 数字/字符串/负数严格相等、数据源集合严格、不泄漏他租户、越权在 executor 查询前拒绝且不增构建/审计、registry buildCount/serverCount 符合缓存语义）、429 与窗口恢复、请求体恰好 4MiB 通过/4MiB+1 返 413 且零触达、畸形输入 fail-closed 与单连接污染后自愈、错误体不含堆栈/口令/内部主机。
- **G1-b 两条非阻塞已知项（均为 fail-closed 现状，不改变 RC 安全结论；响应规范化留 v0.2 polish，属小生产改动，不进门2纯测试单）**：
  1. JSON-RPC `id:null` 被 MCP go-sdk v1.7.0 折叠为“缺失 id”，HTTP 返回 400 `missing id`（即 null id 请求被拒绝，安全方向正确）；RC 接受“null id 被拒”，是否规范化回显 null 留 v0.2。
  2. 传输层错误（截断/非法 JSON、错误 Content-Type 等）当前经 go-sdk `http.Error` 返回 `text/plain` 而非统一 `application/json` 错误信封；状态码正确、不泄漏堆栈/口令/内部主机已断言，统一 JSON 错误体留 v0.2。
- **流程备注**：G1-b 的 Codex 进程在测试全部落盘后、生成收尾报告阶段异常中断（无 go/compile 进程、日志停更、无 IMPLEMENT_DONE）；主控经独立 gofmt/vet/`go test -race ./internal/mcpserver/`（全绿）+ 全量 run-acceptance（ALL_GREEN）接管验收，确认代码完整、白名单干净（仅 3 个 `*_test.go`）、go.mod/go.sum/语料零改动。
- **G1-c 已合入**（tag `g1-c`，merge `f5ad8aa`）：纯测试覆盖率补齐（5 文件 +1430 行，零生产改动、go.mod/go.sum/语料未动）。带 Docker 实测 **executor 72.3%→89.8%、parser 80.5%→87.9%**；至此核心安全链 parser/engine/rules/policy/pipeline/executor/mask 全部 ≥80% 且裕量充足。run-acceptance **ALL_GREEN**（gofmt/vet/`-race` 全包含容器矩阵、353 语料逐字不变 FP 0.00%、双构建）。executor 补：manager/open/配置校验/脱敏/rows 类型/连接池/超时/事务状态机的离线分支，以及 testcontainers PG18+MySQL8 bound session、关闭后/context 取消、错误路径与凭证脱敏；parser 补：panic recover、MySQL CTE/嵌套 JOIN/关键字/常量、PG 整数/节点/tautology/EXPLAIN 失败分支。`.gitignore` 新增忽略 `.gocache/`。
- **UNION INTO OUTFILE/DUMPFILE 调查结论（G1-c Codex 报 POSSIBLE_BUG，主控实测证伪 fail-open）**：当前 Vitess 把 `SELECT ... UNION SELECT ... INTO OUTFILE 'x'`（含 `(SELECT ... UNION SELECT ...) INTO OUTFILE` 括号形态）的 `Into` 挂在 `Union.Into`（左右子 Select.Into 均为 nil），产品 `Operations` 正确包含 `INTO OUTFILE`/`INTO DUMPFILE`，**R201 必拦，不存在漏拦**；已新增 `internal/parser/union_into_regression_test.go` 锁定“信号必在 + 文件名不进 normalized”。
- **第三条非阻塞已知项（v0.2 规范化 polish，非 fail-open、不泄漏敏感信息）**：UNION 形态的 `Normalized` 文本会丢弃整个 INTO 子句（顶层单 SELECT 正常渲染为 `into outfile ?`）；决策不依赖 normalized、文件名不残留，故拦截与敏感串脱敏均正确，仅审计 SQL 文本不完整。v0.2 与前述两条协议 polish 一并统一规范化处理。
- **G1-d 已合入**（tag `g1-d`，merge `6d0fbab`）：一键可观测栈 + 命名卷硬化 + 指标 HTTP 断言。
  - 部署：根 `docker-compose.yml` 为唯一 compose 权威；agentsql 由 bind mount `./data` 改为**命名卷 `agentsql-data`**（消除 Linux 宿主 root 权限文件）；新增 `profiles:["observability"]` 的 prometheus(v2.55.1)/grafana(11.2.2)，端口 9090/3000 全绑 `127.0.0.1`、与 agentsql 同网络抓 `agentsql:7780`；Grafana 经 provisioning（datasource uid=`prometheus` + dashboard provider）**零手工自动导入「AgentSQL 可观测性」6 面板**；删除独立 `examples/observability/docker-compose.observability.yml` 以免漂移；README/DEPLOY 更新为 `docker compose --profile observability up -d --build` 一条命令。
  - 测试：`t25_metrics_test.go` 扩真实 HTTP 指标断言（无 WHERE 更新 `R002` deny、429 `rate_limited`、401、标签有界不含 key/SQL），`metrics_test.go` 补 pool 采集器 max/inuse/idle 与 nil/panic 安全；测试 fixture 统一注入 metrics observer。run-acceptance **ALL_GREEN**、353 语料不变、零生产 Go/go.mod 改动。
  - 主控 Docker 动态实测（`down -v` 后干净环境）：冷构建 + 起栈 **238.7 秒（<5 分钟，含 go mod 下载）**；agentsql `healthy` 且**非 root（uid=999 agentsql）**；prometheus target **UP**；经真实 MCP Streamable HTTP（`Accept: application/json, text/event-stream`）验证 `SELECT 1` allow 真实执行、无 WHERE `UPDATE` 被 **R002 deny 且不触库**、突发流量触发 **429**；`/metrics` 七类指标齐全并全部入库 Prometheus（decisions allow=4/deny=1、rule_hits R002=1、8 个 pipeline 阶段、rejected rate_limited=41、pool max=5/idle=1）；Grafana 数据源与 6 面板自动装配成功。
  - 演示建数/喂数脚本与临时数据不入库（在仓库外 `transfer/smoke/`）；验证用容器栈与命名卷在验收机保留供录屏/观感，正式发布前 `docker compose --profile observability --profile demo down -v` 清理。
- **门2（G1）自动化门全部通过 → tag `v0.1-rc1`（私有仓发布候选，非正式 GA）**：
  - 五个串行单 g1-a0 / g1-a1 / g1-b / g1-c / g1-d 已全部合入 main 并分别打 tag（见上各条）。
  - **fuzz 失败闭环（清单⑤）**：`go test -run=^$ -fuzz=FuzzAssessFailClosed -fuzztime=30m ./internal/pipeline/` 跑满 30 分钟 **PASS**（exit 0，1801.6s）：48,147,748 次变异执行、2364 个扩展语料，**无 panic、无崩溃、无 fail-open**；任何解析失败/异常输入均回到 deny/error 闭环。
  - **最终全量独立验收**（主控 run-acceptance，记录 tag `G1-rc1`）**ALL_GREEN**：gofmt clean、`go vet`、`go test -race -count=1 ./...`、353 语料逐字不变（FP 0.00%）、`go test -short -count=1 ./...`、独占 P99 门禁、双 `go build` 全部 EXIT=0。
  - **P99 微基准测试编排修复（纯测试、零生产 Go 改动）**：`TestT25LatencyPercentile` 是 GOMAXPROCS 个 worker、约 20 万样本的 CPU 抢占型**独占**微基准；混在跨包并行的全量 `go test ./...` 中会与重测试/`-race` 余热争用 CPU 而偶发误报（实测独占 P99≈1.83ms、67k ops/s；全量并行时可飙到 7ms+、吞吐跌到 36k）。改为函数开头 `testing.Short()` 跳过：常规全量统一 `go test -short ./...`，性能门禁由验收脚本在收尾阶段对 `./internal/pipeline/` **独占、非 -short、非 -race** 运行（对主机瞬时负载最多冷却重试一次，两次独占皆失败才算失败）。修复后独占口径 **P99=1.83ms，为 5ms 预算的 2.7 倍裕量**。
  - **动态部署/可观测门（清单⑥⑦）**见 G1-d：干净环境 238.7 秒从零起栈、agentsql healthy 且非 root(uid=999)、命名卷 `agentsql-data`、Prometheus target UP、七类指标经真实 MCP 流量全部入库、Grafana 数据源+6 面板零手工自动装配；`down -v --remove-orphans` 卷/网络清理路径已实测。
  - **剩余仅人工/环境门（主控无法代做）**：第 7 章清单⑧ Cursor 与 Claude 桌面端四场景（只读成功 / 越权拒 / 无 WHERE 更新拒 / 审计可查）真实接入**录屏**；Linux 真机 systemd + 非 root UID 演练。人工门全绿后，方可由用户亲口下令 tag `v0.1.0`、生成 GitHub Release（zip/tar + 校验和）、私有仓转公开与对外宣传发布。

# 第 7 章 v0.1 总验收（开源前全绿）
- go test 核心包覆盖率 ≥80%；真实 PostgreSQL（兼容矩阵 PG14/15/16/17/18，必测最新 PG18）与 MySQL8 作为**被防护业务库** E2E 通过；v0.1 元数据/审计库仅 SQLite（外部 PG 元库为 v0.2 开源任务 T28）；
- 决策语料 252 条（353 次方言运行：PG192/MySQL161；danger76/risk61/normal115；判定 deny77/allow121/approve42/warn12）危险漏拦 0、误拦 <2%、fuzz 连续 30 分钟（4298 万次变异）无 panic 且 fail-closed；
- Cursor/Claude 各录屏：只读成功/越权拒/无WHERE更新拒/审计可查；
- 控制台 6 类页面（总览/审计/演示台/Agent/数据源/权限/规则）全部联调并打进单二进制；
- 干净环境 5 分钟跑通；性能达标。

# 第 8 章 后置任务（v0.1 GA 之后）

## 8.1 版本分层与商业模式（开源 vs 企业；授权 = AGPLv3 + 商业双授权 + 商标保留）
- **授权**：开源代码采用 GNU AGPLv3；内部使用、自托管、PoC、改码免费；凡对外提供网络服务（SaaS/云）或第三方分发的衍生作品，须按 AGPL 网络条款同样开源；闭源集成、SaaS 商用、企业模块、保修与 SLA 走商业授权。商标（AgentSQL 名称/Logo）保留，fork 不得冒用。
- **开源版（免费、可独立生产）**：MCP 双承载/认证/限流；完整 SQL 安全引擎（规则/评分/只读/拦截/审批/Explain/N+1）；被防护业务库 MySQL + PostgreSQL 14~18；基础脱敏、审计闭环、9 页控制台、实时大屏 WebSocket（T27）；元数据/审计库 SQLite（默认）与 PostgreSQL 15~18（基准 PG18、含审计独立 DSN，T28）；单节点部署、Prometheus、5 分钟上手。
- **企业版（商业 License + 私有化交付 + 年订阅/SLA）**：T29 国产/商业业务库矩阵、T30 合规与身份管控包、T31 HA/集中管控与规模交付，以及 T26 延伸的托管 Cloud/SaaS（后置）。
- **分界原则**：通用 MySQL/PG 场景与 PG18 控制面不收费（做事实标准、做传播）；政企信创、等保合规、规模化生产所需能力收费。

## 8.2 开源任务
- **T26 在线 Live Demo**：演示只读账号、30 天假数据种子、每日重置、演示横幅、6 剧本引导（开源演示；托管化/Cloud 商业化另计）。
- **T27 大屏全局 WebSocket 实时流**（开源，替代轮询，利于演示传播）。
- **T28 控制面 PostgreSQL18（开源，v0.2 第一批）**：
  - T28a 存储后端抽象：config 增 `metadata.driver=sqlite|postgres` + PG DSN/连接池，SQLite 仍默认；`store.Open` 工厂按驱动注册 sqlite/pgx，PG 去掉 `SetMaxOpenConns(1)`；迁移按方言拆 `migrations/sqlite`、`migrations/postgres`，PG 去 PRAGMA、占位符 `?→$1`；PG DDL 用 `BIGINT GENERATED ALWAYS AS IDENTITY`、`TIMESTAMPTZ DEFAULT now()`、`BOOLEAN`，数据源+列名唯一索引用 `NULLS NOT DISTINCT`；仓储统一 rebind，`audit_logs` 插入 `LastInsertId` 改 `RETURNING id`（联动 `approvals.audit_id`）；大屏 `date(substr(ts,1,19))` 改 `::date/date_trunc`；审批 CAS 改 `… RETURNING`。
  - T28b 审计独立 PG18（开源）：审计可配独立 DSN 指向另一 PG18，独立账号仅 `INSERT/SELECT`（无 UPDATE/DELETE），不配则同库；testcontainers 拉 `postgres:18` 让全套 store/仓储/大屏/审批/脱敏测试在 sqlite 与 pg18 双跑（含并发 CAS/审计/限流）；提供 SQLite→PG18 迁移命令（含不可变 `audit_logs`）；compose 增 `postgres:18`（元数据/审计可分服务）、健康检查/depends_on/volume/非 root、`pg_dump` 备份文档；声明兼容 PG15+、主推 PG18。

### 8.2.1 T27 大屏实时事件流（开源；实现选型 = SSE，达成原"WebSocket 实时流"目标）

**目标**：控制台总览大屏在网关产生决策的瞬间自动出现新事件、今日计数/分布即时跳动，无需手动刷新，用于演示传播与值守。替代 T18/T19 的纯轮询。

**协议裁决（主控技术裁决，纯工程选型）**：采用 **SSE（Server-Sent Events，`text/event-stream`，HTTP 长连接，服务端单向推送）**，不引入 WebSocket 第三方库。理由：本需求是严格单向（服务器→浏览器）的事件广播，SSE 用标准库 `net/http` 即可、天然走现有 HTTP 路由与管理端 **Bearer 鉴权中间件**、浏览器断线自动重连语义简单；WebSocket 双向能力本单用不到，却要新增依赖并自行处理鉴权/心跳/协议升级。未来若需要服务端主动下发（如多租户实时推送、审批在线会签）再升级 WS，SSE 事件契约保持不变。SPEC 早期文字称"WebSocket"为泛称，以本节 SSE 实现为准。

**事件源裁决**：以**成功落库后的审计记录 `model.AuditLog` 作为唯一实时事件**（不另造事件、不重复 pipeline 回调）。审计是流水线最终、最完整、且已持久化的事实（含 agent/datasource/tool/原始与归一 SQL/对象/decision/rule_hits/risk/est_rows/rows/latency/ts），只有成功写入审计才广播，保证"大屏看到的 = 审计可查的"。

**后端范围（单 T27-1）**：
- 新增 `internal/eventbus`：进程内 fan-out Hub（**不启动任何后台 goroutine**）。建议 API：`New(Options{HistorySize:200, SubscriberBuffer:64}) (*Hub,error)`、`Subscribe() (<-chan Event, func())`、`Publish(Event)`、`SubscriberCount() int`、`Close()`。**关键并发约束（设计评审修正）**：订阅注册、历史快照拷贝、历史入队必须在**同一互斥临界区**完成——持锁取按旧到新排列的历史副本，创建 `cap = len(history) + 64` 的 channel，先放入历史再登记订阅者，随后解锁；保证新订阅者先收完历史、实时事件严格排在历史之后，且注册瞬间不漏事件。这里的 **64 是"实时事件积压余量"，不是含历史回放的总容量**（固定 cap=64 装不下最多 200 条历史）。`Publish` 语义为"不等待任何消费者 I/O"：持锁做环形写入并对每个订阅者非阻塞发送（`select{case ch<-e:default:}`），实时积压超过 64 的慢消费者立即摘除并关闭其 channel，绝不阻塞审计热路径（临界区仅环形写 + 至多 1000 次非阻塞 send）。所有 send/delete/close 在同一把锁内，channel 只由 Hub 关闭；`cancel` 用 `sync.Once`/map 存在性保证幂等，严禁向已关闭 channel 发送或重复 close；`Close` 后 `Publish` 为 no-op、`Subscribe` 返回已关闭 channel 与 no-op cancel。环形历史仅内存、重启清空（更早历史走审计页）。
- **事件源必须覆盖两条审计写路径（设计评审修正；二者互斥、每条审计记录至多广播一次）**：① 普通决策经 `pipeline` 的 `Audit.Record` → `audit.Sink.Insert`：用 `publishingAuditSink` 装饰 `metadataStore.AuditLogs()`，内层 `Insert` 成功、拿到带 ID/时间戳的记录后再 `hub.Publish`；② **审批（approve）决策经 `ApprovalWorkflow.CreatePendingWithAudit`，它在数据库事务内直接调 `insertAuditLog`（见 `internal/store/approval_repository.go`），完全绕过 `audit.Sink`**：必须再用 `publishingApprovalWorkflow` 装饰 `metadataStore.Approvals()`，仅在 `CreatePendingWithAudit` **事务提交成功返回后**发布其返回的 `recorded`，`Create` 等其它方法只代理、不发布。普通路径靠 `run.audited` 防重，approve 路径置位 `run.audited` 后不会再走普通审计，故不重复广播。装饰器先返回内层错误，仅成功后在 `recover` 保护下 Publish，广播的任何 panic/错误都不得覆盖审计成功结果。**不得改挂 `pipeline.DecisionObserver`**：它只有低基数字段、没有审计 ID，且 `pipeline.finish` 在审计失败后仍调用 observer，会产生"未落库却已广播"的假事件。装饰器建议放 `internal/bootstrap/event_stream.go`（私有），Hub 经 `Runtime` 暴露，`Runtime.Close` 一并关闭 Hub。
- 管理端新增 **`GET /api/v1/stream`**：挂在已鉴权管理路由（与其它 `/api/v1/*` 同一 Bearer 中间件；无/错/过期令牌、或只有 `?token=` 而无 `Authorization` 头，一律 401；**仅支持同源控制台，不做 CORS、不做查询令牌旁路**）。
- SSE 处理顺序：① Bearer 中间件；② 握手前用 `http.NewResponseController(w)` 递归探测底层是否实现 `http.Flusher`，不支持则用现有 `apiResponse` JSON 信封返回 500；③ 非阻塞获取一个连接槽（用容量=最大连接数的 buffered channel 作信号量，拿到后立即 `defer` 释放，panic 也能归还；槽满返回 JSON 503）；④ `Subscribe` 并 `defer cancel()`；⑤ 写 SSE 响应头与 `event: hello` 并 Flush；⑥ 先消费历史、后实时，每帧写入即 Flush；⑦ 心跳；⑧ `r.Context().Done()`/channel 关闭/写或 Flush 失败即 return。**握手提交（已写 SSE 头/帧）之后出错只能断流，不得再写 JSON 信封。**
- **Flusher 不得被响应包装隐藏（设计评审修正）**：`internal/adminapi/handler.go` 的 `statusRecorder` 与 `internal/mcpserver/http.go` 的状态记录 wrapper 目前只匿名嵌入 `http.ResponseWriter`，会使 `w.(http.Flusher)` 断言失败；两处都要加 `Unwrap() http.ResponseWriter`，SSE 统一用 `http.NewResponseController(w).Flush()`；并让 mcpserver 的 `classifyRoute` 识别 `/api/v1/stream`（纳入已知 API 资源），避免该路由的 HTTP 指标被归到 `/other`。
- 响应头：`Content-Type: text/event-stream`、`Cache-Control: no-cache, no-store, no-transform`、`Connection: keep-alive`（仅 HTTP/1.1 有意义）、`X-Accel-Buffering: no`；该路由**排除任何 gzip/brotli 压缩**（压缩会攒帧使 Flush 失效）。**不要设置会杀死长连接的固定短 `http.Server.WriteTimeout`**；改为每帧写之前 `ResponseController.SetWriteDeadline(now+5s)`，写/Flush 失败即回收半开连接（心跳也用于触发半开 TCP 的写失败）。
- 帧协议（`data:` 必须是 `json.Marshal` 一次生成的**单行 JSON**，JSON 内换行自然转义）：握手 `event: hello` + `data: {"version":"...","demo":false}`；审计帧为 `event: audit`、`id: <audit.id>`、`data: <单行 JSON>`；心跳帧 `: ping`（间隔 25s，间隔须可注入以便确定性测试，**禁止测试里真等 25 秒**）。
- **事件体用显式安全 allow-list 投影 `auditToStreamView`（设计评审修正），不得原样发送完整 `auditView`**：后者含 `sql_raw/sql_norm/error_msg/client_ip/session_id/conversation_id`，原始 SQL 可能带口令、错误信息可能带 DSN/主机/驱动细节。SSE 只发安全摘要：`id, ts, agent_id, datasource_id, mcp_tool, db_type, stmt_type, objects, decision, rule_hits, risk_level, est_rows, rows_returned, latency_ms, model_name`（字段命名与 auditView 一致）；**不发** `sql_raw, sql_norm, error_msg, client_ip, session_id, conversation_id`，更不得含口令/DSN/SECRET/堆栈。审计页、审计导出与审计表仍保留完整数据，不因 SSE 删减。
- 配置：`server.event_stream`（bool，默认 true，显式 false 必须生效）与 `server.event_stream_max_connections`（int，默认 100，校验范围 1–1000）；`Parse` 用 `KnownFields(true)`，新字段必须进结构体，默认值在解码后、Validate 前按"YAML 是否显式给出"设置，避免零值覆盖显式 `false`/`0`；同步更新 `agentsqlctl init-config` 模板、示例 YAML 与配置测试。心跳 25s、缓冲 64、历史 200 本单不开放配置。`console_enabled=false`、`event_stream=false` 或 stdio（`mcp` 子命令）时不创建装饰器、不注册路由；但**启用流而 `Runtime.Events==nil` 时 `NewHandler` 应直接报错（fail-fast），不得静默漏事件**。
- 指标（可选、不阻塞）：`agentsql_eventstream_connections` gauge、`agentsql_eventstream_events_total`、慢消费者断开计数。
- 测试（全部 httptest/fake/临时 SQLite，**本单不依赖 Docker**）：
  - Hub：单/多订阅者扇出；只保留最近 200 条且顺序正确；历史之后实时事件无漏无重；幂等 cancel；`Close` 与 `Publish/Subscribe` 并发；慢消费者填满实时余量后被摘除、健康订阅者不受影响、`Publish` 在限定时间内返回。
  - `go test -race`：多发布者、多订阅/取消、Publish 对 cancel、Publish 对 Close，重点捕获 send-on-closed-channel、重复 close、环形索引与指针别名竞态。
  - 装饰器：普通 `Insert` 成功发布一次、失败不发布、Publish panic 不改变 Insert 成功结果；approve 事务提交成功发布一次、回滚不发布、不与普通 sink 重复。
  - SSE：无/错 Bearer 401、仅 query token 401；hello 必为第一帧；响应头正确；历史顺序；读到 hello 后再 Publish（以此作"订阅就绪"同步屏障，不写 sleep 型时序测试）能收到实时事件与 `id`；注入短 ticker 确定性验证心跳；不支持 Flusher 返回 JSON 500；连接满返回 503、首连接取消后槽位可复用；客户端取消/写错误/Flush 错误/写 panic 均注销并释放槽位。
  - 安全：把哨兵口令、DSN、SECRET、伪造堆栈分别放进 SQL/error/client IP，断言整条 SSE 字节流都不包含它们。
  - 装配：临时 SQLite 跑 allow/deny/approve，Hub 各收到且各一次；强制审计 Insert/事务失败时无事件且 pipeline 仍 fail-closed；`console_enabled=false`、`event_stream=false`、stdio 三种情形都不挂 stream。

**交付语义（设计评审补充）**：SSE 是进程内 **best-effort** 通知，不承诺 exactly-once——进程可能在数据库提交后、Publish 前崩溃，重连补发环形历史也可能重复；**审计表与审计 API 始终是权威数据源，前端必须按 `audit.id` 去重**。进程内 Hub 只覆盖单实例（多副本下 MCP 请求可能落到其它实例，粘性会话也无法根治）；跨实例广播（Redis/NATS/PG 通知 + 以审计 ID 为游标补偿）属 T31，不在本单。反向代理/CDN 部署需关闭该路径的 buffering/cache/compression 且 idle timeout 大于 25s（`X-Accel-Buffering:no` 不能控制所有 CDN）；浏览器同源 HTTP/1.1 连接数有限，前端整页只建一条共享 SSE 连接。

**前端范围（单 T27-2，React+antd+echarts，沿用现有依赖，不引新库）**：
- 新增 `useEventStream` hook：**用 `fetch` + `ReadableStream` 手动解析 SSE 帧**（不用浏览器原生 `EventSource`——它不能携带 `Authorization` 请求头；也**不接受 `?token=` 查询参数降级**，避免令牌进入 URL/代理/浏览器历史）；带管理端 Bearer；指数退避自动重连；页面 `visibilitychange` 回前台立即重连；暴露连接状态 `live | reconnecting | polling-fallback`。
- 总览页：`EventStream` 改为实时 prepend（上限 100 条，字段/样式与现有事件流一致）；今日 KPI、决策分布环图对新事件做**乐观增量**；趋势图（历史按天聚合）维持接口数据，事件到达时即时更新"今天"那个点；**每 30 秒拉一次汇总接口做校准（同时作为 SSE 不可用时的降级轮询）**，杜绝长时间漂移。
- 顶部/大屏角标显示连接状态（实时=绿点"实时"、重连中=黄点、降级=灰点"轮询"）。
- 不做：服务端下发/双向控制、跨实例广播（多副本属 T31）、超长历史回放、其它 8 个页面改造（只动总览与必要的 api/hook/types）。
- 验收：主控黑盒（起栈→登录打开总览→经真实 `/mcp` 制造 allow 与 R002 deny→大屏 1–2 秒内出现事件且计数/环图变化、审计页一致；断开后端再恢复，状态黄→绿并补发；带 `-race` 后端测试全绿；gofmt/vet/353/双构建全绿；SQLite 行为零回归）。

**前端实现约束（T27-2 只读评审结论 APPROVE_WITH_CHANGES，全部必须遵守）**：
- 连接所有与生命周期：总览页是 SSE 唯一连接所有者；hook 用 `startedRef + generationRef + disposed` 守卫，React.StrictMode 双 effect（启动→清理→再启动）下清理须复位 started、递增 generation、清退避定时器、`abort()` 并 `reader.cancel()`；所有异步分支在 setState/安排重连前校验 generation/disposed；`onAudit/onHello` 回调存入 ref、不进连接 effect 依赖（避免每次渲染重建连接）；离开总览彻底清理，返回时由新实例重建连接。
- 分帧：`TextDecoder({stream:true})` 增量解码，按行并以空行分帧，兼容 CRLF、`data:` 后有无单个空格、多行 data（按 `\n` 拼接）、跨 chunk 半事件与多字节；`: ping` 是 SSE 注释，不做业务分发、仅刷新 lastActivity；audit 帧 `JSON.parse` 后做最小运行时校验（id 为安全整数、ts/decision 为字符串、frame `id` 与 body `id` 一致），坏帧/坏 JSON 跳过且**不断流**。
- 鉴权与重连：每次连接前从 `useAuthStore.getState().token` 取最新 token；401 立即停止重连、`clear()` 并跳 `/login`（只处理一次）；退避 `min(30s, 1s·2^attempt)`（1/2/4/8/16/30s）再乘 0.8–1.2 抖动；503/5xx/网络错误/意外 EOF 计一次失败并退避，连续 6 次未稳定连接→`polling-fallback`；200 无 body、Content-Type 非 `text/event-stream`、404 等协议不匹配**立即 fallback 不重试**；连接稳定满一个心跳周期（约 30s）才清零失败计数；fallback 下页面重新可见或手动刷新允许单次 SSE 探测，成功回 live。
- 去重与回放：挂载期维护 `seenStreamIds`，以 SSE `id:`（并核对 body id）去重；**新连接回放的最近 200 条历史只进事件列表，绝不累加 KPI/环图/趋势**（以本次连接建立时刻为乐观增量门槛，更早事件不做聚合增量）；当前协议无"回放结束"标记，聚合为 best-effort，偏差由 30s summary 校准兜底；未来若需严格区分回放/实时，再增 `replay_end` 事件或 summary 水位（列入后续，不在本单）。
- 合并：EventStream 不得用轮询结果整体 `setEvents` 覆盖实时行；实时摘要行与 `listAudit` 完整行按数值 id merge（保留已补全的 `sql_raw` 等字段），按 ts、id 降序稳定排序并截断 100；轮询 `page_size` 提至 100；仅真正新到的实时 deny 进高亮集合且只高亮一次，历史/回放/补全不闪（framer-motion 对非新增项 `initial={false}`）；实时行无 `sql_raw` 时 SQL 列显示 `objects`→`stmt_type`→"—"，Tooltip 仅在轮询补全原文后显示。
- 乐观增量范围：仅对当前 summary 的"今天"趋势行、`kpi.total_requests`、deny→`kpi.blocked`、approve→`kpi.pending_approvals`、`decision_distribution` 做增量；切换 7/14/30 天递增代次，旧代次的事件/请求不得写入新窗口；**不**乐观修改环比、活跃 Agent、数据源数、排行、规则 Top5、战报（单条摘要无法可靠推导）。校准：每次拉 summary 前记事件序号 cut，服务端结果整体覆盖后仅重放 cut 之后的增量，避免请求窗内丢事件，瞬时双计数由下一次校准消除。
- 降级轮询：fallback 时必须真正开启轮询，`pollEnabled = autoRefresh || status==='polling-fallback'`，避免关闭自动刷新后显示"轮询"却不更新。
- 未知 decision：事件照常显示，但不塞入趋势四桶、不伪装成 allow/warn/approve/deny，分布环以服务端校准为准。
- 类型与组织：`api/types.ts` 新增独立 `AuditStreamEvent`（必填 `id:number/ts:string/decision:string`，其余可空，**不得含** sql_raw/sql_norm/error_msg/client_ip/session_id/conversation_id）、`StreamHello`、`StreamStatus`；不使用 Context，由 Overview 持有 hook、经 props 向 EventStream 传有界 `liveEvents` 与 `streamStatus`；连接状态徽标置于总览 `pageExtra`（自动刷新开关与"立即刷新"按钮之间）。
- 构建：前端改完在 `web/` 跑 `npm run build`（`tsc --noEmit && vite build`）生成新 dist，再重新 `go build` 经 go:embed 打包并重启后端，否则控制台仍是旧前端；不新增依赖、不改后端与其它页面。

### 8.2.2 T28 控制面 PostgreSQL 18（开源，v0.2 地基；开源与企业同等支持，**不得据此收费**）

**总原则（裁决）**：
1. **SQLite 仍是默认、零配置上手路径不变**；PG 是新增的可选生产控制面。SQLite 口径下 353 语料、全量测试、黑盒行为必须零回归。
2. T28 是**严格的存储方言对等**：两套 schema 语义/约束/索引完全等价，只做类型与占位符/自增键/日期函数的翻译；**不夹带任何行为增强**。v0.1 刻意未建的 `mask_rules (datasource_id,column_name)` 唯一索引（允许重复、运行时确定性归并）本单**不在 PG 侧新增**（`NULLS NOT DISTINCT` 唯一索引 + 历史重复数据清理另开一个小单，避免方言迁移夹带行为变更、也避免存量重复行导致 PG 迁移失败）。
3. **依赖不新增**：`github.com/jackc/pgx/v5`（v5.7.6，业务库执行器已用）与 testcontainers core/postgres/mysql（v0.37.0，门2 G1-a1 已用）**均已在 go.mod**；控制面基于 `database/sql`，PG 控制面通过 blank-import `github.com/jackc/pgx/v5/stdlib` 以驱动名 `pgx` 打开（不用业务库执行器的原生 pgxpool）。本批 T28 不新增第三方依赖、不引入 dockertest 等替代品。继续 `GOTOOLCHAIN=local`（本机 Go ≥1.25.0）、Windows 可编译（pgx 纯 Go 不破坏既有 cgo 现状）。
4. 兼容口径：**声明兼容 PostgreSQL 15+，基准与主推、CI 必测 PostgreSQL 18**；元数据库与审计库都支持 PG18（不使用 PG16 作为基准镜像）。

**配置（向后兼容）**：扩展 `store` 段，同时保留现有 `store.sqlite_path` 作为简写（老配置不改即可跑）：
```yaml
store:
  sqlite_path: /var/lib/agentsql/agentsql.db   # 兼容 v0.1；等价 metadata.driver=sqlite
  metadata:
    driver: sqlite          # sqlite | postgres，默认 sqlite
    sqlite_path: /var/lib/agentsql/agentsql.db  # driver=sqlite 必填
    dsn: ""                 # driver=postgres 必填，例 postgres://agentsql:***@host:5432/agentsql?sslmode=disable&TimeZone=Asia/Shanghai
    max_open_conns: 10
    max_idle_conns: 5
    conn_max_lifetime: 30m
  audit:
    separate: false         # true 时审计写入独立库
    driver: postgres        # 独立审计库驱动（v0.2 仅 postgres；false 时忽略，随元数据库）
    dsn: ""                 # 独立审计库 DSN（建议仅 GRANT SELECT,INSERT ON audit_logs 的运行账号）
    max_open_conns: 10
    max_idle_conns: 5
```
`config.Validate()` 改为**按驱动条件校验**（KnownFields 同步放开新字段）：sqlite 必须有 path；postgres 必须有非空 dsn；`audit.separate=true` 必须有 audit.dsn；非法 driver 报错 fail-closed。`agentsqlctl check-config` 覆盖新分支。

**T28a-1 方言骨架与迁移（只读评审 APPROVE_WITH_CHANGES，6 项裁决已并入）**：

依赖现状（已核实，不再新增）：`go 1.25.0`；`github.com/jackc/pgx/v5 v5.7.6` 与 testcontainers core/postgres/mysql `v0.37.0` **均已在 go.mod**（分别由业务库执行器与门2 G1-a1 引入），本单**不改 go.mod/go.sum、不引入 dockertest 等新依赖**。业务库执行器用原生 pgxpool，但**控制面仓储/事务全部基于 `database/sql`**，故控制面 PG 一律 blank-import `github.com/jackc/pgx/v5/stdlib` 后 `sql.Open("pgx", dsn)`，**不使用 pgxpool**；pgx 纯 Go，不新增 Windows/cgo 负担。

- Dialect/rebind（新增 `internal/store/dialect.go`）：`type Dialect string`，常量 `DialectSQLite="sqlite"`、`DialectPostgres="postgres"`，`ParseDialect(string) (Dialect,error)`；`rebind(d Dialect, q string) (string,error)`：sqlite **逐字返回**；postgres 按 `?` 出现顺序替换为 `$1,$2,…`，但必须**词法感知**——跳过单引号字符串字面量、双引号标识符、行/块注释、PostgreSQL dollar-quoted（`$$…$$`/`$tag…$tag`）内容，只替换 SQL 代码区占位符；未知方言返回错误。rebind 本单交付并配纯函数单测（仓储全面启用在 a-2）。
- 迁移双 embed：`//go:embed migrations/sqlite/*.sql migrations/postgres/*.sql`，运行期 `fs.Sub(fs, "migrations/"+string(dialect))` 枚举排序。现有 `migrations/0001_init.sql` **纯移动**到 `migrations/sqlite/0001_init.sql`（内容逐字等价、不改已发布语义），新增 `migrations/postgres/0001_init.sql`。
  - 权威签名 `Migrate(ctx, db *sql.DB, dialect Dialect) error`；所有现有调用点（含 `cmd/agentsqlctl/main.go` 的 migrate）机械传入 `DialectSQLite` 保持编译，不用变长参数掩盖未知方言，不夹带 a-3 的 config 分支。
  - sqlite 继续在事务外 `PRAGMA foreign_keys=ON` 并校验=1；PG 不执行任何 PRAGMA（外键默认强制）。
  - **原子认领版本（修复并发"每版本恰好一次"）**：每个版本在**同一事务**内先认领再执行——PG 用 `INSERT INTO schema_migrations(version) VALUES($1) ON CONFLICT (version) DO NOTHING RETURNING version`，仅认领成功（返回 1 行）才在该事务执行该版本 DDL，未认领则回滚跳过；sqlite 用 `INSERT OR IGNORE INTO schema_migrations(version) VALUES(?)` 并以 `Result.RowsAffected==1` 判定认领；DDL 或记账失败整体回滚（版本记录与业务表同生共死），并发 Migrate 不重复执行、不因版本主键冲突报错。
  - `schema_migrations`：sqlite 维持现状；PG 为 `version BIGINT PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT now()`（内部记账表）。
- PG 0001 严格语义等价翻译（以 sqlite 基线为准，七张业务表）：
  - 一律 `CREATE TABLE IF NOT EXISTS`；索引/外键**原名等价**，共 **6 个索引**（idx_agents_keyhash、idx_policies_agent_ds、idx_audit_ts、idx_audit_agent_ts、idx_audit_decision、idx_approvals_status）与 **3 条外键**（policies.agent_id→agents(id)、policies.datasource_id→datasources(id)、approvals.audit_id→audit_logs(id)，均默认 NO ACTION、**不新增级联**）；无 PRAGMA、无 AUTOINCREMENT、DDL 内无 `?`；表/列名均小写无需引号；**不新增** mask_rules 的 (datasource_id,column_name) 唯一约束或 NULLS NOT DISTINCT。
  - 自增：`audit_logs.id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY`；`approvals.audit_id BIGINT REFERENCES audit_logs(id)`（与 identity 对齐）。
  - 布尔：`rules.enabled BOOLEAN DEFAULT TRUE`、`rules.builtin BOOLEAN DEFAULT FALSE`（基线分别 DEFAULT 1 / DEFAULT 0），**两列均不加 NOT NULL**（沿用基线可空性）。
  - 时间：除 schema_migrations.applied_at 外**逐列继承基线可空性、不收紧**——业务表所有 created_at/updated_at 与 `audit_logs.ts` 用 `TIMESTAMPTZ DEFAULT now()`（**无 NOT NULL**）；`agents.expires_at`、`approvals.decided_at` 用可空 `TIMESTAMPTZ`。
  - 数值：datasources.port/conn_limit/stmt_timeout_ms/row_limit、rules.risk_level、audit_logs.risk_level/rows_returned 用 `INTEGER`（默认值 5/5000/1000 保留）；audit_logs.est_rows/latency_ms 用 `BIGINT`（对齐 SQLite 64 位 INTEGER、避免溢出）；其余业务字段 TEXT。
  - PG 严格类型不再容忍布尔整数/任意时间文本/隐式文本→数字，测试须显式覆盖默认值与类型，不依赖 sqlite 宽松转换。
- 连接工厂（`internal/store/store.go`）：`MetadataOptions{Driver Dialect; SQLitePath, PostgresDSN string; MaxOpenConns, MaxIdleConns int; ConnMaxLifetime time.Duration}`；`OpenMetadata(ctx, opts, secret []byte) (*Store, error)`（内部可有接受 `*PasswordCipher` 的变体）。sqlite 维持 `SetMaxOpenConns(1)/SetMaxIdleConns(1)`；PG 去掉单连接限制、按 opts 设 `SetMaxOpenConns/SetMaxIdleConns/SetConnMaxLifetime`。`Open/OpenWithSecret` 保留为薄封装、内部以 sqlite options 调工厂，**现有调用与测试零改动**；工厂不依赖 config（config→opts 是 a-3），本单即可用 opts 直连 PG18 测试。
- 测试：`internal/store` 新增 PG18 从零迁移 E2E（testcontainers `postgres:18`，import 仅出现在 `*_test.go`），两方言都断言 ①空库首次迁移后七业务表+schema_migrations+6 索引+3 外键齐备；②连续两次与并发两次 Migrate 均成功且 schema_migrations 中 version=1 恰好一行；③外键生效（非法 policy/approval 写入失败，sqlite PRAGMA 同样生效）；④identity 连续生成、approvals.audit_id 可引用；⑤rules.enabled 默认 true、builtin 默认 false；⑥时间列默认值/类型 timestamptz；⑦模拟 DDL 中途失败时业务表与版本记录同时回滚。跨包 `*_test.go` helper 不可复用，store 包内保留与门2**同口径**的 Docker 探测（daemon/socket 明确不可达 Skip；探测成功后的镜像拉取/启动/等待/清理错误硬失败），不把 testcontainers import 移入普通 `.go`。
- 边界：本单后 PG 库可 Ping/建表，但仓储仍含 `?`、`LastInsertId`、`INDEXED BY`（agent_repository）、sqlite 日期表达式，故 PG 暂不承载业务 CRUD（属 a-2）；config/bootstrap 仍走 sqlite 薄封装，`go build ./...`、既有 sqlite 测试与 run-acceptance 必须全程绿、353 语料零变化。

**T28a-2 仓储方言适配与双跑（只读评审 APPROVE_WITH_CHANGES，阻断裁决已并入）**：

总原则（硬约束）：
- SQLite 是行为基准：**SQLite 分支 SQL 文本逐字不变**（保留 `INDEXED BY`、`LastInsertId`、`RowsAffected`、`date(substr(ts,1,19))` 原样），PostgreSQL 另写模板；不靠"把 SQLite 也改成 RETURNING"求统一，避免已执行 SQL 与错误时序漂移。
- 方言下沉到仓储：各 repository 内嵌 `repositoryBase{db *sql.DB; dialect Dialect}`，`Store` 构造仓储时传入 `store.driver`；提供 `bind(q)`（即 rebind）与执行/取 id/CAS/日期/LIKE helper；**不得只在 Store 外层 rebind**（事务 `*sql.Tx` 内 SQL 同样要 bind）。
- 每条 SQL 在动态 WHERE/IN 列表/分页片段**全部拼接完成后恰好 rebind 一次**，禁止对子片段重复 rebind（否则 PG 占位符从 $1 重复编号）。
- 本单**不改 `internal/model`**（不把时间改指针、不把 bool 改三态），类型兼容只在 store 内 scanner 完成；不新增依赖。config/bootstrap/CLI 接线=a-3，metaDB/auditDB 拆分=b-1，数据搬迁=b-2。

1. 占位符与普通 CRUD：agents/datasources/policies/rules/mask_rules 的 INSERT/UPDATE/DELETE/SELECT 全部经 bind（`?`→`$n`）。两处 `INDEXED BY`（agent 按 api_key_hash、policy 按 agent+datasource）仅出现在 SQLite 模板，PG 模板删除该提示（PG 自选同名索引）。`updated_at=CURRENT_TIMESTAMP`、`TRIM/COALESCE/ORDER BY/LIMIT ? OFFSET ?` 两方言兼容。
2. 审计自增 ID：新增 `insertReturningID(ctx, exec, dialect, insertSQL, args...) (int64,error)`——SQLite 维持 `ExecContext`+`LastInsertId()`（SQL 文本不变）；PostgreSQL 对 INSERT 追加 `RETURNING id` 并 `QueryRowContext().Scan(&id)`（pgx/stdlib 的 LastInsertId 不可用）。覆盖 `insertAuditLog` 与 `CreatePendingWithAudit` 事务内插审计取 id、回填 `approvals.audit_id` 全链路。并发插入测试只断言 id 全为正、唯一、排序后严格递增，不要求连续、不按 goroutine 返回顺序比较。
3. 审批 CAS：`DecidePending` 条件 UPDATE 与 `CreatePendingWithAudit` 回填 UPDATE 抽 `approvalCAS(...) (matched bool,error)`——SQLite 保留 `UPDATE ... WHERE id=? AND status='pending'`+`RowsAffected`（0 时再 SELECT 区分不存在/已决定，错误分类维持现状）；PostgreSQL 用同一条件 UPDATE 加 `RETURNING id`，`sql.ErrNoRows` 即未命中再走存在性检查。注：pgx/stdlib **支持** RowsAffected，PG 用 RETURNING 是为把命中判定绑定到语句结果，而非驱动不支持。两方言业务错误分类（404/409）必须一致；并发下仅一个事务命中（PG 行锁释放后重检 status）。
4. scanner 归一（store 内，不改 model）：
   - 时间：`databaseTimestamp.Scan` 已兼容 PG time.Time 与 SQLite string/[]byte；time.Time 分支出口统一 `.UTC()`；错误信息去掉写死的 "SQLite" 字样。可空时间继续 `optionalTime`；默认时间列在 DB 为 NULL 时按现状 `required()` 报错（手工写 NULL 视为无效元数据），不静默归零。
   - 布尔：新增内部 `databaseBool` scanner，接受原生 bool、整数 0/1、字符串 "0"/"1"/"true"/"false"，其余报错；用于 rules.enabled/builtin（PG 原生 bool、SQLite 0/1）。
   - 整数：id/audit_id/est_rows/latency_ms 走 int64/sql.NullInt64；INTEGER→*int 转换补溢出检查；`COUNT(*)`/`SUM(CASE...)` 扫 int64。
   - 战果 `SUM(COALESCE(est_rows,0))`：PG 的 SUM(BIGINT) 返回 NUMERIC，PG 模板写 `CAST(COALESCE(SUM(est_rows),0) AS BIGINT)`、SQLite 模板原样，并测大值。
   - JSON/文本列（policies.columns/row_filter、rules.definition、audit_logs.objects/rule_hits）PG 仍 TEXT，string/NullString 即可，riskTop 仍在 Go json.Unmarshal。
5. 大屏/审计日期与时区（**阻断裁决：本单两方言都维持 UTC 自然日**）：现状 SQLite 以 `now.UTC()` 构造 `[start,end)`、用 `date(substr(ts,1,19))` 分组（UTC 日界线）。为同时满足"SQLite 行为不变 + 两方言逐字段一致"，本单 **PG 也按 UTC 分组**：分组键 `(ts AT TIME ZONE 'UTC')::date`，SELECT/GROUP/ORDER 同一表达式，并 `to_char((ts AT TIME ZONE 'UTC')::date,'YYYY-MM-DD')` 输出与 SQLite 一致的文本（或新增兼容 time.Time/string 的 databaseDate scanner）；时间范围参数两方言都传 time.Time。**可配置时区（如 Asia/Shanghai）日界线延后 a-3，届时两方言同步切换，本单绝不只给 PG 换日界线。** 趋势/决策分布/风险 Top/Agent 排行/战果/空日补零在 PG 与 SQLite 逐字段一致；覆盖月末、年末、start 含/end 排、恰等于边界记录。
6. LIKE 契约（**裁决：以 SQLite 现状 ASCII 大小写不敏感为准**）：审计模糊过滤 SQLite 保留 `LIKE ? ESCAPE '!'`，PostgreSQL 改用 `ILIKE ? ESCAPE '!'`；补 `%`、`_`、`!` 转义与 ASCII 大小写用例；Unicode 大小写折叠两库均不保证（已知限制，写入注释）。
7. 限流口径澄清：QPS/in-flight 限流是**内存令牌桶**（rules 通用限流、mcpserver HTTP registry），数据库只存 datasource 的 conn_limit/stmt_timeout_ms/row_limit 阈值、**无限流 SQL**。双跑只验证"两仓储读出相同阈值并驱动同一套内存限流"，不新增库级限流。
8. 测试双跑（sqlite 始终跑；postgres:18 复用 a-1 Docker 探测，daemon 明确不可达仅 Skip PG 子测试，探测成功后拉取/启动/迁移/清理失败硬失败；每组用例隔离库状态）：新增同包矩阵 helper（storeVariant + forEachStore，SQLite 用 t.TempDir()，PG 起 postgres:18 经 OpenMetadata），把 store/审批/脱敏/dashboard/审计过滤测试改 table-driven 双跑，至少覆盖：五类实体全 CRUD/List/GetByKeyHash 与两处 INDEXED BY 路径；rule 布尔 true/false 与默认；时间字段往返（UTC、truncate 比较）；audit 并发写 id 唯一严格递增；audit 分页/组合过滤/动态 IN/时间区间/LIKE 大小写与通配符转义/导出跨页与上限；approval 全 CRUD、pending CAS 并发仅一成、404/409；CreatePendingWithAudit 成功与审计失败/回填失败回滚；dashboard 趋势/分布/Top/排行/战果/缺名补空/30 天边界/SUM NUMERIC；datasource 三阈值往返；mask 数据源专属 + 全局/NULL 源列兜底。SQLite 侧结果与改造前逐字一致。

边界：本单完成后 PG 库可承载完整业务 CRUD/审批/大屏，但 driver/dsn 仍由测试直连（config/bootstrap 未接线，默认运行仍 SQLite）；a-3 才接通配置与装配，且不得在本单双跑门禁全绿前接通 PG 业务流量。

**T28a-3 配置/装配/CLI 接线**：
- `bootstrap.Assemble` 改用存储工厂按 `cfg.Store.Metadata.Driver` 打开元数据库；`agentsqlctl init-config/check-config/migrate/health/version` 适配 driver/dsn（`migrate` 可对指定 driver+DSN 仅执行迁移，供部署期用高权限账号建表）；新增 `examples/docker/config.postgres.yaml` 与保留 sqlite 示例；`Dockerfile`/compose 不破坏默认 sqlite 路径；双构建（agentsql/agentsqlctl）通过。
- 验收：SQLite 默认路径全量 run-acceptance ALL_GREEN、353 语料零变化；PG18 元数据库下黑盒走完登录→建数据源/Agent/policy/mask→真实 `/mcp` allow/deny/脱敏→审计页/大屏聚合→审批，全部与 SQLite 等价。

**T28b-1 审计独立 PG18 DSN（开源）**：
- `Store` 持有 `metaDB` 与 `auditDB` 两个 `*sql.DB`（`audit.separate=false` 时 auditDB=metaDB；Close 各自关闭，同库不重复关）。`AuditLogs()` 仓储绑 auditDB；`DashboardRepository` 拆成两个 querier——审计聚合（总量/拦截/趋势/分布/规则 Top/Agent 排行/战果）走 auditDB，元数据计数（active agents、datasources、pending approvals、agent id→name）走 metaDB，应用层合并；审计分页/导出（`audit/export.go`）走 auditDB。
- 独立审计库迁移只建 `schema_migrations` + `audit_logs` 及其索引；文档给出最小权限运行账号（建表迁移用一次性高权限账号；运行账号 `GRANT SELECT,INSERT ON audit_logs`，无 UPDATE/DELETE/DDL，契合审计不可变）。
- E2E：testcontainers 起**两个** `postgres:18`（元数据库 + 独立审计库），断言审计写入审计库、元数据库无 audit_logs 数据、控制台审计页/大屏/导出仍正确、停掉审计库时网关 fail-closed 并明确报错（不静默丢审计）；含并发 CAS/审计/限流双跑。

**T28b-2 迁移命令、部署与版本**：
- `agentsqlctl` 增 **SQLite→PostgreSQL 迁移**子命令：一次性把元数据七表 + 不可变 `audit_logs` 从 SQLite 拷到 PG（含自增 ID 序列 `SELECT setval(...)` 对齐、审计原 ID 不变、approvals.audit_id 关系保持）；**幂等可重跑**（目标已有数据时校验/拒绝覆盖，审计表只增不改）；结束打印每表行数与校验摘要（行数、audit 区间、可选 hash），并给"迁移后切换 config 重启"步骤。
- `docker-compose.yml` 增 `postgres:18` 控制面服务（profile `controlplane`；元数据/审计可分 `metadata-db`、`audit-db` 两服务）：命名卷、`pg_isready` 健康检查、`depends_on: service_healthy`、非 root、仅绑回环；agentsql 通过环境变量注入 DSN/SECRET；`docs/DEPLOY.md` 增 PG 控制面部署、最小权限账号、`pg_dump` 备份与恢复、PG15+ 兼容说明（基准 PG18）。
- 版本与物料：v0.2 发布单统一把版本七处抬到 **v0.2.0**，`CHANGELOG.md` 增 v0.2.0（Added：实时事件流 SSE、PG18 元数据/审计库、SQLite→PG 迁移、Live Demo；Compatibility：PG15+/基准 PG18、SQLite 仍默认）。开发过程用存档 tag `t27-*`/`t28-*`，不提前对外宣称 GA。

### 8.2.3 T26 在线 Live Demo（开源演示套件；托管 SaaS 商业化后置）

**目标**：访客零安装、零配置，在一个**只读、安全、每日重置**的公开演示里点 6 个剧本，直观看到"自然语言/AI 生成 SQL → 网关放行/拦截/脱敏 → 审计与大屏实时可查"，用于官网/GitHub 转化与朋友圈传播。本单交付**可自托管的开源演示套件**（compose 一键起）；多租户托管 Cloud 属 T26 商业化延伸，不在本单。

**安全边界（fail-closed，最高优先）**：
- 硬开关 `demo.enabled=true`（或 `AGENTSQL_DEMO=1`），默认关闭；仅演示构建/部署显式开启。开启后：全局固定横幅"演示环境·数据每日重置·禁止接入真实数据与真实数据库"；登录页与总览显著提示；不提供注册。
- 演示只连**预置的演示数据源**（白名单 ID），后端持有演示 Agent 的 API key，**前端任何时候拿不到 `asql_` key**；演示业务库账号只读；演示写操作一律在静态/动态门被拦截、**绝不触达真实业务库执行**；对演示通道单独收紧速率（复用 QPS 限流并给更小额度）。
- 演示环境的 SECRET/管理员口令由部署时注入（compose 示例只给绑回环的本地演示值；公开部署文档要求替换），不内置真实凭据。

**T26-1 演示数据与一键环境**：
- 演示业务库（沿用 `demo` profile 的 postgres + mysql，展示"被防护库可跨方言/跨版本"）：建贴近场景的表（如 `customers`(含 phone/email 敏感列)、`orders`、`products`），灌**确定性的近 30 天**数据（固定种子、可重复生成，含足够行数演示截断与脱敏）。
- 控制面种子（幂等 seed）：1 个演示数据源（MySQL 与 PG 各一）、1 个只读演示 Agent + 1 个 dml 演示 Agent、最小放权的几条 policy（含明确不放权的表用于"越权拒"剧本）、phone/email 脱敏规则；并预置**近 30 天 `audit_logs` 演示决策**（allow/deny/warn/approve 分布、各规则命中、延迟/行数），让大屏趋势/分布/Top/排行/审计页开箱即有内容。种子走与生产一致的仓储接口（不绕过校验直接拼库），敏感列密码等按正常加密路径写。
- 交付 `docker-compose.demo.yml`（或 demo profile 组合）一键起：业务库 + 控制面（默认 SQLite 控制面即可，PG18 控制面是 T28 可选项）+ 种子 + 可选 observability；幂等 `demo/seed.*` 与 `demo/reset.sh`（`down -v` → `up` → reseed），附宿主 cron 每日重置示例；**不做应用内定时重置**（交给 cron/编排，简单可靠）。

**T26-2 演示通道与 6 剧本引导**：
- demo 模式下把 Playground 从 T21 的"纯静态 assess 旁路"接成**受控真实通道**：仅对演示白名单数据源、以预置演示 Agent 身份走完整八阶段流水线并写审计（这样大屏 T27 实时流会点亮）；只读 SELECT 真实执行并经脱敏返回（受 row_limit）；写/DDL 走到拦截即返回 deny 与命中规则、不触库；非 demo 模式下 Playground 维持原静态旁路不变（生产面零改动）。
- 前端新增"6 个演示剧本"引导卡（可一键填入 SQL 并运行、显示"预期结果"）：①只读查询正常放行并返回数据；②无 WHERE 的 UPDATE/DELETE 被 R002 拦截；③查询含 phone/email 的表结果被打码脱敏；④`EXPLAIN` 全表扫描/高风险被动态门告警或拦截；⑤访问未授权表被越权拒绝；⑥跳转审计页/总览大屏，回看刚才每一次决策与规则命中（配合 T27 实时出现）。
- 横幅、演示模式徽标、剧本卡仅在 demo 标志下出现（前端经 `/healthz` 或启动配置接口只读获取 `demo.enabled` 与版本，不暴露敏感配置）。
- 物料：README 增"在线 Live Demo / 5 分钟本地演示（一条命令）"与 6 剧本 GIF/截图位；`docs/` 增演示部署与每日重置说明。
- 验收：干净环境一条命令起演示；6 剧本逐一得到预期（放行/拦截/脱敏/告警/越权拒/审计实时可查）；重置脚本能恢复到完全一致的初始态（行数/关键计数固定）；尝试访问白名单外数据源、尝试拿 key、尝试真实写库均失败（fail-closed）；非 demo 模式回归全绿。

## 8.3 企业版任务（商业）
- **T29 国产/商业业务库矩阵**：达梦 DM、人大金仓 KingbaseES、瀚高 HighGo、GaussDB、OceanBase、TiDB、Oracle、SQL Server 的方言解析、驱动适配、脱敏/规则方言与兼容矩阵（信创/政企进场壁垒）。
- **T30 合规与身份管控包**：审计 PDF 等保/数据安全法报告、审计防篡改（哈希链/签名/WORM）、外置 SIEM（syslog/Kafka/ES）、长期归档、操作水印；行级权限(RLS)、完整/动态脱敏与自定义算法、敏感数据自动发现与分类分级；SSO(OIDC/SAML)、LDAP/AD、MFA、多租户、RBAC/ABAC、多级会签审批、飞书/钉钉/企微/Jira 工单集成、告警 Webhook。
- **T31 HA/集中管控与规模交付**：多副本 HA、K8s Operator、水平扩展、多网关/多环境集中策略统管、备份恢复、容量/性能报表、Agent 行为异常分析(UEBA)；私有化安装包、等保合规模板、实施/培训、SLA。
- **后置**：AgentSQL Cloud 托管 SaaS（T26 延伸，按 Agent/数据源/审计量订阅）。

# AgentSQL 架构与工程规格

> 版本：v0.2 ｜ 本文描述 AgentSQL 的系统架构、对外契约、安全模型与兼容边界，面向使用者与贡献者。
> 完整可运行的配置样例见 `examples/`，完整建表 DDL 见 `internal/store/migrations/`；当本文与源码出现分歧时，以源码与测试为准并提 issue 修正本文。

---

## 1. 项目概述

### 1.1 是什么

AgentSQL（中文名：**智盾**，AI-Native Database Security Gateway）是夹在 **AI Agent 与关系型数据库之间的数据库安全网关**，以**生产级 MCP Server** 的形态交付。AI 客户端（Cursor、Claude Desktop、豆包、自研 Agent 等）通过 MCP 接入，Agent 生成的每一条 SQL 都必须经过解析、鉴权、规则评估、风险决策、受控执行、脱敏与审计，才能访问真实数据库。

- 网关二进制：`agentsql`（MCP 承载 + 管理 API + Web 控制台）
- 运维 CLI：`agentsqlctl`（生成配置、配置校验、数据库迁移、健康检查、演示种子等）
- Agent API Key 前缀：`asql_`（仅存储 SHA-256 哈希）

产品定位**窄而深**：只做「MCP 数据库安全网关」这一件事。**不做**泛化 BI 报表、不做 ORM、不做 Text2SQL 大模型，也**不替代数据库自身的账号与授权体系**。

### 1.2 核心链路

每条 SQL 经过六段逻辑流水线（内部实现拆为八个阶段，见第 3 章）：

```
Parse(解析 AST) → Auth(身份与权限) → Guard(静态规则 + 动态 EXPLAIN) → Decide(allow/deny/approve)
              → Execute(限流 / 超时 / 行数上限 / 受控写) → Redact + Audit(结果脱敏 + 审计落库)
```

### 1.3 安全边界（使用前务必阅读）

AgentSQL 是**网关层**防护，必须如实理解其边界：

- 只约束**经过网关的运行账号**；不阻止使用数据库 owner / superuser / DBA 凭据**绕过网关直连**的行为，也不取代数据库账号体系。生产部署应为网关配置最小权限的专用账号。
- 脱敏是**结果集按列打码**，不是完整 DLP。复杂表达式、聚合、`CAST`、`UNION`、CTE、视图重命名等场景可能无法回溯到源列；JOIN / 自连接为表级授权；不宣称「任何别名都不可绕过」。
- 审计是**应用层只追加（append-only）记录**，不是法规级 WORM，不防 DBA 直接改库；审计哈希链 / 签名 / WORM 保留锁属企业版路线图。
- MCP 层不提供通用的跨请求事务代理；多副本高可用、跨实例事件广播属企业版路线图。
- 安全默认 **fail-closed**：无法解析、无法判定、或审计 / 数据库等依赖不可用时，**拒绝执行**，而不是放行。

---

## 2. 部署与运行形态

- **MCP 双承载**：`stdio`（供本地 Agent 以子进程方式接入）与 **Streamable HTTP**（网关多租户入口，路径 `/mcp`，遵循 MCP 2025-06-18 规范）。
- **Web 控制台 + 管理 REST API** 由同一进程提供，默认仅监听回环 `127.0.0.1:7780`；一键自托管在线演示套件默认 `127.0.0.1:17880`。
- **控制面（元数据 / 审计存储）**：内置 **SQLite**（默认、零配置单文件）或 **PostgreSQL 15+**（基准与推荐 **PG18**）；审计库可通过独立 DSN 指向另一套 PostgreSQL，实现元数据与审计隔离。
- **交付物**：单二进制（前端经 `go:embed` 内嵌）、Docker / Docker Compose、Linux systemd unit；提供 Prometheus 指标与 Grafana 面板，以及 `/healthz`（进程存活）、`/readyz`（含存储就绪）探针。
- **构建约束**：PostgreSQL 解析依赖真实内核 `pg_query_go`，因此构建需要 **CGO 与 C 编译器**、运行需要 **glibc**；不支持 `CGO_ENABLED=0` 与 Alpine（musl）。Linux amd64 发布二进制建议用 Makefile 的容器交叉构建目标生成。

---

## 3. 架构与处理流水线

```
AI Client ──MCP(stdio / Streamable HTTP)──┐
                                          ├─► mcpserver(tools) ─┐
Web 控制台 ──REST(/api/v1)──► adminapi ───┘                     │
                                          ┌─────────────────────▼────────────────────┐
                                          │ pipeline（auth → load → parse →          │
                                          │   guard_static → guard_dynamic →         │
                                          │   execute → redact → audit）             │
                                          │ policy / parser / rules / engine /       │
                                          │ executor / mask / audit / eventbus       │
                                          │ store（SQLite 或 PostgreSQL 元数据/审计） │
                                          └─────────────────────┬────────────────────┘
                                                pg/mysql wire 连接池（受控账号）
                                                     PostgreSQL / MySQL
```

**八个阶段**：`auth`（Agent Key 认证）→ `load`（加载 Agent / 数据源 / 策略）→ `parse`（方言 AST）→ `guard_static`（AST 静态规则）→ `guard_dynamic`（获取受控连接、必要时 `EXPLAIN`）→ `execute`（受控执行）→ `redact`（结果脱敏）→ `audit`（审计落库）。

**两闸评估与「拦截不触库」**：静态闸与动态闸任一判定 `deny`，都在获取真实连接或执行之前返回，从结构上保证危险 / 越权 SQL **不接触业务库**。

- **只读 `query`**：仅允许语义只读的 `SELECT`；执行后审计，审计未成功不返回结果。
- **受控写 `execute_write`**：默认关闭，需要 `dml` 及以上等级的 Agent 与显式策略授权。写路径遵循**审计提交屏障**：可事务 DML 在业务事务内「先写审计再提交」，审计失败则回滚业务事务（业务库零变更）；DDL 等隐式提交 / 不可回滚语句在执行前先写入「授权意图」审计，意图审计失败则不执行。
- **人工审批 `approve`**：策略裁决为「需审批」时生成审批单，**不执行 SQL**；管理员在控制台裁决（approved / rejected / expired）。
- **实时事件流**：进程内 SSE 端点 `/api/v1/stream`（管理端 Bearer 鉴权、仅同源、事件体为安全字段 allow-list 投影），审计成功落库后推送；浏览器断线自动重连，持续失败降级为轮询；**审计表与审计 API 始终是权威数据源**，前端按审计 ID 去重。SSE 为单实例 best-effort，跨实例广播在路线图中。

---

## 4. 技术栈

**后端（Go 1.25）**

| 用途 | 依赖 |
|---|---|
| MCP 协议 | `modelcontextprotocol/go-sdk` v1.7（stdio + Streamable HTTP） |
| PostgreSQL 解析 | `pganalyze/pg_query_go/v5`（真实 PG 内核 parser，零绕过） |
| MySQL 解析 | `vitess.io/vitess` sqlparser（`ParseStrictDDL` 严格模式、多语句拆分，契合 fail-closed） |
| PostgreSQL 驱动 | `jackc/pgx/v5`（业务库用 pgxpool；控制面用 `database/sql` + pgx stdlib） |
| MySQL 驱动 | `go-sql-driver/mysql` |
| 控制面存储 | `modernc.org/sqlite`（SQLite）+ pgx（PostgreSQL） |
| HTTP | Go 标准库 `net/http`（管理 API 与 MCP HTTP，不引入 Web 框架） |
| 配置 / 日志 / CLI | `gopkg.in/yaml.v3`、`rs/zerolog`、`spf13/cobra` |
| 限流 / 指标 | `golang.org/x/time`、`prometheus/client_golang` |
| 测试 | `stretchr/testify` + `testcontainers-go`（真实 PostgreSQL 18 / MySQL 8） |

**前端（`web/`）**：React 18 + TypeScript 5 + Vite 5 + Ant Design 5 + ECharts 5 + framer-motion + zustand + react-router-dom + axios。前端不引入单测框架，前端质量门为 `tsc --noEmit` 类型检查与 `vite` 生产构建，构建产物由后端 `go:embed` 打入单二进制。

---

## 5. 代码结构

```
cmd/
  agentsql/        # 网关 / MCP / 控制台进程
  agentsqlctl/     # 运维 CLI（init-config / check-config / migrate / health / demo-seed 等）
internal/
  config/ model/ store/ auth/ policy/ parser/ engine/ rules/
  executor/ mask/ audit/ pipeline/ mcpserver/ adminapi/
  eventbus/ metrics/ demoseed/ bootstrap/ server/ webui/ version/
web/               # React 控制台源码（构建到 web/dist，由 internal/webui 内嵌）
deploy/systemd/    # systemd unit 与加固样例
examples/          # docker-compose、配置样例、环境变量样例
demo/              # 一键自托管在线演示套件与 reset 脚本
tests/corpus/      # SQL 解析与决策回归语料
website/           # 产品官网（静态站，不打入二进制）
docs/              # 文档
```

`internal/store/migrations/` 按「元数据 / 审计」布局与「sqlite / postgres」方言组织迁移脚本，两套方言 schema 语义、约束、索引等价。

---

## 6. 对外契约

### 6.1 配置（config.yaml）

主要配置段（完整带注释样例以 `examples/config.example.yaml`、`examples/docker/.env.example` 为准）：

```yaml
server:
  http_listen: "127.0.0.1:7780"
  console_enabled: true
  event_stream: true                 # SSE 实时事件流
  event_stream_max_connections: 100
store:
  # 简写（v0.1 兼容，默认零配置）：
  sqlite_path: "./data/agentsql.db"
  # 或显式控制面（v0.2）：
  # metadata: { driver: postgres, dsn: "...", max_open_conns: 10, max_idle_conns: 5 }
  # audit:    { separate: true, driver: postgres, dsn: "..." }   # 审计独立 PG
  # auto_migrate: false          # 生产建议：一次性 migrate 后关闭启动期 DDL
defaults:
  statement_timeout_ms: 5000
  row_limit: 1000
  max_conns_per_datasource: 5
  qps_per_agent: 20
theme: { default: "dark" }           # dark / light
demo: { enabled: false }             # 在线演示模式，默认关闭
```

- 密钥通过环境变量注入：`AGENTSQL_SECRET`（数据源密码 AES-GCM 主密钥）、控制面 DSN 可用 `AGENTSQL_STORE_METADATA_DSN` / `AGENTSQL_STORE_AUDIT_DSN` 注入；配置、日志、错误与探针输出**不回显 DSN 或密码**。
- 配置解析为严格模式（未知字段拒绝），非法配置启动即失败（fail-closed）。

### 6.2 核心枚举与模型

```go
type DBDialect string // "postgres" | "mysql"
type StmtType  string // SELECT / INSERT / UPDATE / DELETE / DDL / ADMIN / UNKNOWN
type Decision  string // allow / warn / approve / deny
type RiskLevel int    // 1 拒绝 / 2 审批 / 3 告警 / 4 提示
```

`AST` 携带方言、原始 / 归一化 SQL、语句类型、多语句标记、表对象、列、是否含 WHERE / WHERE 永真 / LIMIT、函数与操作、EXPLAIN 信息；`Assessment` 携带决策、风险等级、命中规则、预估扫描行数、中文理由与改写建议、各阶段耗时；`QueryResult` 为列、字符串化行、行数与截断标记（金额等定点十进制按定点渲染，不使用浮点）。

### 6.3 控制面数据模型

七张业务表加迁移账本 `schema_migrations`：

| 表 | 用途 / 关键不变量 |
|---|---|
| `agents` | Agent 注册与等级（readonly/dml/ddl）；`asql_` 密钥仅存 SHA-256 |
| `datasources` | 业务库连接；密码以 AES-GCM 密文存储（主密钥 `AGENTSQL_SECRET`）；含连接数 / 超时 / 行数上限 |
| `policies` | 库 / 表 / 列三级授权策略（allow/deny），关联 Agent 与数据源 |
| `rules` | 内置规则目录与运行时启用覆盖（不在此写入任意自定义规则语义） |
| `mask_rules` | 按数据源 / 表 / 列的脱敏规则与算法 |
| `audit_logs` | **审计核心，只追加**；仓储层不提供 UPDATE / DELETE 接口；独立审计库中仅此表与索引 |
| `approvals` | 审批单（pending/approved/rejected/expired），关联审计 ID |

SQLite 与 PostgreSQL 两套 DDL 语义等价（自增键、布尔、时间类型、占位符按方言翻译）；审计 ID 在 PG 上为 `BIGINT GENERATED ALWAYS AS IDENTITY`，数据迁移保留原审计 ID。

### 6.4 MCP 工具（对 AI 暴露，共七个）

| 工具 | 入参 | 行为 |
|---|---|---|
| `list_datasources` | `{}` | 仅返回当前 Agent 授权的数据源 |
| `list_schema` | `{datasource_id, table?}` | 返回 schema，按列权限过滤 |
| `explain_query` | `{datasource_id, sql}` | 只做 EXPLAIN，不执行 |
| `query` | `{datasource_id, sql}` | 仅语义只读 SELECT，走完整流水线，结果脱敏 |
| `execute_write` | `{datasource_id, sql, reason}` | 受控写，默认关闭，需 dml 及以上 + 授权 + 审计屏障 |
| `request_approval` | `{datasource_id, sql, reason}` | 生成人工审批单，不执行 |
| `get_approval_result` | `{approval_id}` | 查询审批状态 |

错误统一返回 `{decision, reason, suggestion}`，`suggestion` 用中文指导 AI 自我改写。**不提供 `execute_raw_sql` 之类的万能执行工具。**

### 6.5 管理 REST API

- 前缀 `/api/v1`，除 `POST /api/v1/auth/login` 外均需管理端 Bearer 令牌；统一响应信封 `{code,msg,data}`，分页为 `{total,page,page_size,list}`。
- 端点族：认证（login）、agents（含 rotate-key）、datasources（含连通性 ping）、policies、rules、audit（分页 / 筛选 / JSONL 导出）、approvals（列表 / 裁决）、dashboard/summary（KPI / 趋势 / 分布 / 排行）、playground（静态评估，演示模式另有受控试运行）、stream（SSE）。
- 探针：`/healthz`（存活）、`/readyz`（存储就绪，控制面与审计库双 Ping）、`/metrics`（Prometheus）。

---

## 7. 规则与决策

- **四种决策**：`allow`（放行）、`warn`（告警放行）、`approve`（转人工审批）、`deny`（拒绝，不触库）；风险分四级（拒绝 / 审批 / 告警 / 提示）。
- **规则分层**：通用规则（R001–R010，如无 WHERE 的更新 / 删除、WHERE 永真、只读 Agent 写库、库表黑名单、QPS 限流等）、PostgreSQL 专项（R1xx）、MySQL 专项（R2xx）；静态规则基于 AST 特征，动态规则结合受控 `EXPLAIN`（如全表扫描 R005）与运行时限流。
- **策略引擎**：按 Agent × 数据源解析库 / 表 / 列三级授权，叠加 Agent 等级（readonly/dml/ddl）；未显式授权默认拒绝。
- 内置规则可在控制台启用 / 停用（运行时覆盖，立即生效）；规则命中、风险等级、预估行数、归一化 SQL、各阶段延迟均写入审计，可在控制台与审计导出中追溯。
- 规则的权威清单与正反例以 `internal/rules/` 源码和 `tests/corpus/` 语料为准。

---

## 8. 兼容矩阵（v0.2）

| 维度 | 支持情况 |
|---|---|
| 被防护业务库 | MySQL 8.x；PostgreSQL 14 / 15 / 16 / 17 / **18** |
| 控制面 / 元数据库 | SQLite（默认）；PostgreSQL 15+，基准与推荐 **PG18**（开源免费） |
| 审计库 | 随元数据库，或独立 PostgreSQL（最小权限仅 INSERT/SELECT） |
| MCP 传输 | stdio；Streamable HTTP `/mcp`（MCP 2025-06-18） |
| 控制台 | 深色 / 浅色双主题，单二进制内嵌，默认仅回环 |
| 部署 | Docker / Docker Compose、Linux systemd 单二进制 |
| 可观测 | Prometheus 指标 + Grafana 面板 + 健康 / 就绪探针 |
| 演示 | 一键自托管 Live Demo（只读、每日重置、六剧本） |

**路线图中暂不支持**：Oracle、SQL Server，以及达梦 / 人大金仓 / 瀚高 / GaussDB / OceanBase / TiDB 等国产 / 商业数据库（企业版 T29）；企业 SSO / RBAC / 法规级 WORM（T30）；多副本 HA、K8s Operator、跨实例集中管控（T31）。

---

## 9. 质量与测试门

- 格式 / 静态检查：`gofmt`、`go vet`；全量 `go test -race` 干净；核心安全包（parser / engine / rules / policy / pipeline）覆盖率目标 ≥ 80%，每条规则同时具备正例（应拦截）与反例（不应拦截）。
- **决策回归语料**（`tests/corpus/`）：解析语料 + 252 条决策用例，按 PostgreSQL / MySQL 方言展开 353 次判定；要求危险漏拦为 0、误拦率低于 2%。
- **fuzz fail-closed**：流水线在数千万级变异 SQL 输入下不发生 panic，任何未预期输入均走向拒绝。
- **真实库 E2E**：用 testcontainers 拉起真实 PostgreSQL 18 与 MySQL 8，覆盖只读放行、越权拒绝、无 WHERE 写拦截、脱敏、审批、审计可查；控制面在 SQLite 与 PostgreSQL 两种布局下双跑。
- **「写攻击零触库」双证据**：以假执行器（fake executor）相关方法零调用，加上真实业务库执行前后行数 / 校验和不变，共同证明危险写与 DDL 不触库。
- **性能口径（避免误导）**：文档中的只读 P99 延迟是**剥离了真实数据库网络与 I/O 的网关 CPU 路径微基准**（假执行器、固定并发、内存态），用于守住网关自身 CPU 预算，**不代表端到端延迟**；受控写因新增事务与审计往返，真实延迟必然更高，只记录不套用该门限。

---

## 10. 授权、版本与路线图

- 源代码采用 **GNU AGPLv3**；商业授权、SLA 与商标保留见 [LICENSE](../LICENSE) 与 [COMMERCIAL-LICENSE.md](../COMMERCIAL-LICENSE.md)。商标 **AgentSQL** 及中文名 **智盾** 归版权人所有，fork / 衍生作品未经书面许可不得冒用其名称或标识。
- **开源版（免费、可独立用于生产）**：本规格第 8 章列出的全部兼容能力、完整 SQL 安全引擎（规则 / 评分 / 只读 / 拦截 / 审批 / EXPLAIN / N+1 风险）、结果脱敏、审计闭环、九页控制台、SSE 实时大屏、一键 Live Demo、SQLite 与 PostgreSQL 18 控制面（含独立审计库与迁移命令）、单节点部署与 Prometheus 可观测。
- **企业版（商业 License + 私有化交付 + 年订阅 / SLA）方向**：
  - **T29 国产 / 商业数据库矩阵**：达梦、人大金仓、瀚高、GaussDB、OceanBase、TiDB、Oracle、SQL Server 的方言解析、驱动适配与脱敏 / 规则兼容；
  - **T30 合规与身份管控**：等保 / 数据安全法报告、审计哈希链 / 签名 / WORM、外置 SIEM、长期归档、操作水印、敏感数据发现分级、SSO（OIDC/SAML）、LDAP/AD、MFA、多租户、RBAC/ABAC、多级会签与工单集成；
  - **T31 HA / 集中管控与规模交付**：多副本高可用、K8s Operator、水平扩展、多网关 / 多环境集中策略统管、备份恢复、容量性能报表、私有化安装包与等保模板、实施培训与 SLA。
  - **AgentSQL Cloud 托管 SaaS** 作为在线演示的商业化延伸，后置规划。
- **分界原则**：通用 MySQL / PostgreSQL 场景与 PostgreSQL 18 控制面免费（推动其成为事实标准），信创、合规与规模化生产所需能力走商业授权。
- 商业合作：**87326549@qq.com** ｜ **https://agentsql.cn**

---

## 11. 如何贡献

- 请先阅读 [CONTRIBUTING.md](../CONTRIBUTING.md)：贡献需符合「窄而深」的范围约定、Conventional Commits 提交规范与 DCO；改动安全引擎必须同步补充决策语料并跑通质量门；改动在线演示后使用 `demo/reset` 干净重建。
- 安全漏洞请按 [SECURITY.md](../SECURITY.md) 进行私密披露，不要公开 issue。
- 文档与宣传应坚持克制、如实：不夸大防护边界，不泄露凭据 / DSN / 内部信息。

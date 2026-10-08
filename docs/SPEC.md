# AgentSQL 架构与工程规格

> 版本：v0.5.0（待发布）｜本文描述 AgentSQL 的系统架构、对外契约、安全模型与兼容边界，面向使用者与贡献者。
> 完整可运行的配置样例见 `examples/`，完整建表 DDL 见 `internal/store/migrations/`；当本文与源码出现分歧时，以源码与测试为准并提 issue 修正本文。

---

## 1. 项目概述

### 1.1 是什么

AgentSQL（中文名：**智盾**，AI-Native Database Security Gateway）是面向 AI Agent 的数据库安全网关，以**生产级 MCP Server** 的形态交付。PostgreSQL/MySQL SQL 请求经解析、鉴权、规则评估、风险决策、受控执行、脱敏与审计后访问业务库；其他已注册数据源只在各自有界能力内接入，不自动继承这条完整 SQL 安全链路（见第 8 章）。

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
- 脱敏是**结果集按列处理**，不是完整 DLP。v0.3 能把顶层直接投影列精确归属到 `[schema.]table.column`；JOIN 裸列、`SELECT *`、CTE 外层等无法唯一归属但可能涉及受保护表的结果列，使用不可关闭的 fail-closed 安全兜底，将非空值固定阻断为 `***`。视图只按 SQL 中的引用名匹配，不展开视图、派生表或函数表的列血缘。
- 对敏感列做函数包裹并改名（如 `CONCAT(phone,'') AS x` 或 `lower(phone) AS y`）会绕过全局与表级列名脱敏；这是列名脱敏的固有边界，需配合 SQL 策略禁止相关函数，或对可预见的输出列使用 `block`。`INSERT` / `UPDATE` / DDL / `RETURNING` 不进入结果脱敏；`RETURNING` 走写屏障并只返回 `RowCount`。
- `hash` 是带专用密钥的 HMAC 不可逆指纹，不是加密。它在业务数据库返回结果后计算，不下推到数据库内的 JOIN/WHERE/GROUP BY。确定性指纹会暴露相等关系和频率；共享 key 会带来跨库关联风险，不可逆也不等于匿名。
- `block` 把命中列的每个非空结果值替换为固定 `***`，不输出原值字符、长度或等值关系，但仍保留行列形状、行数、列名、是否有结果，并因空值原样返回而泄漏该格为空/NULL。它与 `mask` / `hash` 的结果层泄漏等级一致，不是匿名化；输出是不保证数值、日期或 JSON schema 兼容的不透明字符串。
- `range` 把数值泛化为等宽区间、把日期截断到年/季/月，以保留粗粒度分布；它不是匿名化，同一桶内的值仍可关联，也不承诺 k-匿名。数值区间、日期时段与空值状态仍然可见，小桶、细粒度、小样本、多 offset 或辅助查询可能带来重识别风险，敏感场景应配合 `block` 或审批。
- 四种脱敏都在数据库执行后处理，不减少数据库读取，也不阻止数据库侧使用原值执行 WHERE/JOIN/GROUP BY；它们只改变向调用方返回的结果单元。
- 审计是**应用层只追加（append-only）记录**，不是法规级 WORM，不防 DBA 直接改库；v0.4 审计完整性链的证明范围与信任边界见下一节，签名 / WORM 保留锁仍属企业版路线图。
- PostgreSQL 的 B2 列级授权、B5 跨请求逻辑会话与计划事务均出厂默认开启；MySQL 不进入 B2 PostgreSQL 路径，且不支持 B5 跨请求事务。每个 operation 仍只允许一条顶层 SQL，并受预检计划和会话安全边界约束；多副本高可用、跨实例事件广播属企业版路线图。
- 安全默认 **fail-closed**：无法解析、无法判定、或审计 / 数据库等依赖不可用时，**拒绝执行**，而不是放行。

### 1.4 审计完整性链：信任边界与不检测范围

审计完整性链只能证明：在一次一致性快照中，从**观察到的 head** 回溯到 genesis，`chain_seq` 连续且无重复；每行参与规范化的审计字节与 `self_hash` 一致；`prev_hash` 正确连接前一行；数据库状态中的模式、行内密钥版本与数据库外可信 manifest 声明的模式、当前密钥版本一致。校验结果 `VALID_AT_OBSERVED_HEAD` 的含义严格限于这个观察头，不表示该头是外部世界曾见过的最新头，也不等同于法规级不可篡改存证。

链不能单独检测以下情况：攻击者删除尾部记录并把 `chain_state`、`chain_verification` 一并回滚到某个真实自洽的旧 head；把 `audit_logs`、链状态和校验记录整体恢复到较早的一致数据库快照；事件在进入审计写入路径前即被漏写；或者数据库控制权与链密钥同时失守。仅删除尾部而不回滚 head 等不完整攻击仍会被检出为 `head_mismatch`。

`keyless` 模式依赖数据库外没有额外秘密，能够发现观察范围内的意外损坏和未同步篡改，但掌握数据库写权限的攻击者可以重算链。`hmac` 模式把链钥作为数据库外的额外信任根：仅失守数据库、未取得链钥的攻击者不能为修改后的内容生成有效 HMAC；如果数据库与链钥同时失守，则仍不能提供上述保证。两种模式都不提供外部最新性锚点，因此都不检测自洽的尾部截断或整库快照回滚。

需要检测这些边界外攻击时，必须增加独立于数据库的可信前提，例如外部 WORM 保留、SIEM 持续接收，或由独立校验方保存并比对最新 head 锚点。企业版 WORM / SIEM 与长期合规归档是后续方向；当前实现不夸大为不可删除账本。

---

## 2. 部署与运行形态

- **MCP 双承载**：`stdio`（供本地 Agent 以子进程方式接入）与 **Streamable HTTP**（网关多租户入口，路径 `/mcp`，遵循 MCP 2025-06-18 规范）。
- **Streamable HTTP 断线重放**：stateful 模式默认启用关系库 EventStore，复用元数据库（SQLite 或 PostgreSQL）持久化旧协议 SSE 事件。客户端以 `GET /mcp`、`Accept: text/event-stream` 和 `Last-Event-ID: <streamID>_<index>` 重连时，只重放该 index 之后的连续事件；事件受 64 MiB 总 payload 上限和 30 分钟 TTL 约束，发生回收缺口时固定返回 HTTP 400 `failed to replay events`，不会返回部分后缀。go-sdk v1.7.0 的 session 表仍为进程内存态：进程重启后旧 `Mcp-Session-Id` 返回 404，并带 `Mcp-Session-Expired: 1`，客户端必须重新 `initialize`；这不是旧 session 的跨重启无缝恢复。
- **Web 控制台 + 管理 REST API** 由同一进程提供，默认仅监听回环 `127.0.0.1:7780`；一键自托管在线演示套件默认 `127.0.0.1:17880`。
- **控制面（元数据 / 审计存储）**：内置 **SQLite**（默认、零配置单文件）或 **PostgreSQL 15+**（基准与推荐 **PG18**）；审计库可通过独立 DSN 指向另一套 PostgreSQL，实现元数据与审计隔离。
- **交付物**：单二进制（前端经 `go:embed` 内嵌）、Docker / Docker Compose、Linux systemd unit；提供 Prometheus 指标与 Grafana 面板，以及 `/healthz`（进程存活）、`/readyz`（含存储就绪）探针。
- **构建约束**：PostgreSQL 解析依赖真实内核 `pg_query_go`，因此构建需要 **CGO 与 C 编译器**、运行需要 **glibc**；不支持 `CGO_ENABLED=0` 与 Alpine（musl）。Linux amd64 / arm64 原生发布二进制使用对应架构的受控构建目标生成；GHCR tag 提供两种架构的 multi-arch manifest。

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

**后端（`go.mod` 声明 Go 1.26.0；正式发布脚本固定 Go 1.26.8）**

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
redaction:
  hash_key: ""                       # 可选；非空时至少 32 字节
```

- `AGENTSQL_SECRET` 是数据源密码 AES-GCM 与控制台/JWT 等用途的主密钥，只从环境变量注入。脱敏哈希指纹使用独立的可选密钥，可来自 YAML `redaction.hash_key` 或 `AGENTSQL_REDACTION_HASH_KEY`；环境变量只要存在（包括空串）即覆盖 YAML，不得与 `AGENTSQL_SECRET` 复用或派生。非空 hash key 按字节计至少 32 字节，推荐由 secret manager/受限环境注入；配置、日志、错误、探针、响应、审计、metrics 与 panic **不得回显密钥、DSN 或密码**。
- 哈希能力 fail-fast 契约只约束 `hash`：无 enabled `hash` 时无 key 可正常启动（包括仅有 `mask` / `block` / `range` 或 disabled hash 草稿）；存在 enabled `hash` 而无有效 key 时，在对外服务前启动失败；显式非空但不足 32 字节时无论规则状态都装配失败。运行期无 key 新建或启用 `hash` 返回 HTTP `503` + `HASH_REDACTION_UNAVAILABLE` 且零写入；`block` 无密钥、无参数，`range` 无密钥但有类型专属参数，二者都不参与该门禁，启用时不会返回此 503。非法类型/算法组合返回 HTTP `422` + `INVALID_MASK_RULE`。
- 首版进程只有单一 hash key，没有 key version、双写、多版本验证或在线轮换/重算。换 key 后历史指纹不会自动重算，旧、新指纹不再相等；轮换必须在维护窗口完成存量盘点、下游回填与回滚准备。
- 配置解析为严格模式（未知字段拒绝），非法配置启动即失败（fail-closed）。

### 6.2 核心枚举与模型

```go
type DBDialect string // 业务库类型标识；注册表共 27 款，能进入 NewParser 的范围另受方言入口限制
type StmtType  string // SELECT / INSERT / UPDATE / DELETE / DDL / ADMIN / UNKNOWN
type Decision  string // allow / warn / approve / deny
type RiskLevel int    // 1 拒绝 / 2 审批 / 3 告警 / 4 提示
type SensitiveType string // phone | email | idcard | bankcard | ip | birthdate | generic | number | date
type Algorithm string // mask | hash | block | range
```

`AST` 携带方言、原始 / 归一化 SQL、语句类型、多语句标记、表对象、列、是否含 WHERE / WHERE 永真 / LIMIT、函数与操作、EXPLAIN 信息；`Assessment` 携带决策、风险等级、命中规则、预估扫描行数、中文理由与改写建议、各阶段耗时；`QueryResult` 为列、字符串化行、行数与截断标记（金额等定点十进制按定点渲染，不使用浮点）。

脱敏算法的维度区别：

| 算法 | 处理维度 | 输出契约 | 等值关联 | 密钥 / 参数 |
|---|---|---|---|---|
| `mask` | 保留可读前缀或格式的部分遮蔽 | 按六类格式处理；无法识别的非空值 fail-closed 为 `[REDACTED]` | 不支持 | 无 |
| `hash` | 不可逆 HMAC 指纹 | 同一 key 下相同字符串字节生成相同定长指纹 | 支持等值关联、去重、分组 | 专用 key 至少 32 字节；无其他规则参数 |
| `block` | 整值阻断 | 不识别格式；每个非空值固定为 `***`，不保留原文片段或长度 | 不支持 | 无密钥、无参数 |
| `range` | 粗粒度泛化 | 数值分桶为 `[lower,upper)`；日期截断为年/季/月；保留粗粒度分布但不是匿名化 | 同桶值可关联 | 无密钥；使用类型专属参数 |

九种敏感类型的算法能力矩阵：

| 敏感类型 | `mask` | `hash` | `block` | `range` |
|---|---:|---:|---:|---:|
| `phone` | ✅ | ✅ | ✅ | ❌ |
| `email` | ✅ | ✅ | ✅ | ❌ |
| `idcard` | ✅ | ✅ | ✅ | ❌ |
| `bankcard` | ✅ | ✅ | ✅ | ❌ |
| `ip` | ✅ | ✅ | ✅ | ❌ |
| `birthdate` | ✅ | ✅ | ✅ | ❌ |
| `generic`（通用敏感值） | ❌ | ✅ | ✅ | ❌ |
| `number`（数值） | ❌ | ✅ | ✅ | ✅ |
| `date`（日期） | ❌ | ✅ | ✅ | ✅ |

`number` / `date` 表示列语义，与前六类格式语义正交；`range` 仅对这两类开放。`birthdate` 仍使用生日专用 `mask`，不因可解析为日期而开放 `range`。`hash` / `block` 可处理九种类型中的任意非空字节串；管理员在 `range`、`hash`、`block` 间切换时无需把 `number` / `date` 改标为 `generic`。

#### 6.2.1 `range` 数值分桶契约

`number` 使用以下参数，不能携带 `granularity`：

- `bucket_width` 必填，必须是 `1..1_000_000_000` 的正整数。
- `bucket_offset` 可省略；省略时规范化为 `0`，必须是 `-1_000_000_000..1_000_000_000` 的整数。

分桶公式为 `lower = floor((v - offset)/width)*width + offset`、`upper = lower + width`。其中 `floor` 是数学意义上的向负无穷取整，不能用整数向零截断；例如 `width=10, offset=0` 时，`42` 输出 `[40,50)`、`10` 输出 `[10,20)`、`-3` 输出 `[-10,0)`。

数值必须用 `math/big.Rat` 做精确十进制有理数解析和计算，禁止使用 `float64`、`strconv.ParseFloat` 或 `big.Float` 再转回整数。非空输入在 `strings.TrimSpace` 后仅接受 ASCII 语法 `[+-]?(digits(.digits?)?|.digits)([eE][+-]?digits)?`，例如 `42`、`-3.5`、`.5`、`1.`、`1e3`；拒绝 `NaN` / `Inf` / `Infinity`、`0x1p2` 等十六进制、下划线、分数、千分位、货币符号和任何尾随字符。trim 后长度超过 128 字节，或科学计数法指数绝对值超过 1000，一律 fail-closed 为 `[REDACTED]`。

计算完成后先用 `big.Int` 比较 `lower`、`upper` 与 int64 上下限，禁止先强转再判断；任一中间值或结果边界溢出 int64 时输出 `[REDACTED]`。成功结果固定为无空格、无 `+`、无千分位、无指数的 ASCII 左闭右开字符串 `[lower,upper)`。

#### 6.2.2 `range` 日期截断契约

`date` 禁止携带 `bucket_width` 或 `bucket_offset`。`granularity` 可省略，省略时规范化为 `year`；显式值只能是 `year`、`quarter`、`month`，显式空串非法。

可解析输入如下：

- 完整日期：`2006-01-02`、`2006/01/02`、`2006.01.02`、`20060102`。
- 本地时间戳：`2006-01-02 15:04:05`，以及同布局带 1–9 位小数秒的形式。
- RFC3339 / RFC3339Nano 时间戳。
- 部分日期：`YYYY-MM` 可截断为年、季或月；`YYYY` 仅可截断为年。精度不足以生成所选粒度时 fail-closed，禁止凭空补 1 日或 1 月。

月、日必须真实有效（包括闰日校验），年份范围为 `0001..9999`。RFC3339 时间戳按输入**原始 offset 下的日历日**截断，禁止调用 `.UTC()` 或转换到服务器时区后换日；时分秒被忽略。输出固定为：`year` → `2006`，`quarter` → `2006Qn`（大写 `Q`、无连字符，`n=(month-1)/3+1`），`month` → `2006-01`。非空但布局不支持、日期非法或精度不足时输出 `[REDACTED]`。

#### 6.2.3 通用执行、计数与隐私契约

- 空值哨兵包括空串、trim 后为空、忽略大小写的 `NULL` / `<nil>`；它们保留原字符串原样返回，不计入 `MaskedCells`。非空值无论成功泛化还是 fail-closed 为 `[REDACTED]`，都计入 processed（即 `MaskedCells`），即使输出字面量与输入相同。
- `range` 无密钥、无盐、无独立配置或环境变量，不进入 hash 密钥的启动门禁，运行期不会返回 `503 HASH_REDACTION_UNAVAILABLE`；混合规则集只有在包含 `hash` 且无有效 key 时才不可用。
- 泛化结果是字符串，输出列不再保证兼容原数值或日期 schema。`range` 不下推为数据库 SQL；网关取回数据库结果后在内存中泛化，因此数据库仍使用原值执行 WHERE/JOIN/GROUP BY，也不会减少数据库读取。
- `RedactReport`、`AuditLog`、SSE 与通知载荷不新增算法专属字段；`TouchedColumns` 可以出现 `number` / `date`。
- 泛化不是匿名化：同桶值确定性相同、仍可关联，不承诺 k-匿名；数值区间、日期时段和空值状态仍可见。小桶、`month` 粒度、小样本、多 offset 或辅助查询可能导致重识别，敏感场景应配合 `block` 或审批。

`mask+generic` 非法。`block` 的空值判定沿用统一哨兵：空串、trim 后为空、忽略大小写的 `NULL` / `<nil>` 原样返回且不计入 `MaskedCells`；其他值即使原文恰为 `***` 或带首尾空格，也输出 `***` 并计数。固定 `***` 可能与真实原值碰撞且不自描述，不能根据输出是否变化判断是否执行了阻断。

规则管理 API / UI 原样展示 `algo=block`；MCP / Playground 的结果投影以及持久审计、SSE、通知不新增算法字段。`***` 与 mask 失败兜底 `[REDACTED]` 是不同字面量、不同算法语义，但结果投影本身不能可靠反推算法，本版不扩展结果或审计协议。

#### 6.2.4 表.列感知脱敏契约

脱敏规则有三档作用域，存储值与语义如下：

| 作用域 | `schema_name` | `table_name` | 语义 |
|---|---|---|---|
| 全局列规则 | `""` | `""` | 保持历史行为：按列名在对应数据源范围内统一保护 |
| 表.列规则 | `""` | 非空 | 保护该表的列；schema 通配 |
| 模式.表.列规则 | 非空 | 非空 | 仅保护该 `schema.table` 的列 |

`schema_name` 非空而 `table_name` 为空非法，管理 API 返回 HTTP `422`。应用路径把作用域空值保存为空串，不使用 SQL `NULL`。v0.3 的 `0005_mask_rule_scope.sql` 是一次性语义迁移：升级前的存量规则统一将 `schema_name` 和 `table_name` 固化为空串，显式保留升级前“全局列规则”的实际行为。

来源分析使用两个不可混用的表集合：

- **顶层 SELECT 关系绑定**：仅包含根 SELECT 的 `FROM` / `JOIN` 作用域中可见的物理关系和非物理来源，只用于精确归属顶层直接投影列。
- **`AST.Tables` 全语句物理表并集**：递归包含 CTE 和子查询内部的物理表，只用于未解析列的“可能关系”安全兜底，不得用来猜测精确归属。

顶层投影的来源状态为 Resolved 或未解析。显式限定列只在限定符唯一命中顶层物理绑定时 Resolved；裸列只在顶层 `FROM` 可见来源总数恰好为 1，且该来源是物理关系时 Resolved。JOIN 裸列、`SELECT *`、CTE 外层列、派生表或函数表的列均按未解析处理。关系起别名后，原表名被遮蔽；`schema.table` 两段限定只认未起别名的关系。视图按 SQL 中的关系引用名处理，不向视图定义、派生表或函数表内部展开列血缘。

对每个结果列，严格按以下六档顺序匹配，取第一条命中且每列只处理一次：

1. 若用户显式改名，且改名后的结果列名命中全局列规则，应用该规则。
2. 若来源为 Resolved，按来源列名查表级规则：先查 schema 精确规则，再查 `schema_name=""` 的 schema 通配规则。
3. 按结果列名查全局列规则。
4. 按来源列名查全局列规则。
5. 若来源未解析，且结果列名或非空的来源列名命中某条表级规则，并且该规则的表出现在 `AST.Tables` 可能关系集合中，执行 fail-closed 安全兜底：将该列的所有非空单元格固定阻断为 `***`，并记录 `RedactReport.UnresolvedScopedColumns`。
6. 均未命中时返回原文。

第 5 档不使用任一张表的业务算法，因为来源尚未确定。它也不“回退全局后放行”：那会使 JOIN 裸列、`SELECT *` 和 CTE 外层绕过仅为某表配置的规则。系统也不因此直接拒绝整条查询，以保持透明网关对 ORM 和工具生成 SQL 的可用性。该兜底不可关闭；误伤通过给投影列加表限定符，或把该列改为全局列规则消除。

标识符语义按方言区分：

- 列名沿用 `NormalizeColumnName`：去除首尾空白和单层引号、反引号或方括号，再统一小写。这是宽松归一；同表内的 `"Phone"` 与 `phone` 不单独区分。
- schema 和表名精确比较，不做 `ToLower` / `EqualFold`；因此 `customers` 与 `"Customers"` 是不同的表键。
- PostgreSQL 只保留 SQL 显式写出的 schema，未限定关系不硬填 `public`，不对规则键二次统一小写。发现生成的 PostgreSQL 草稿保持 `schema_name=""`。
- MySQL 首版按表名字符串精确比较，列名仍按上述宽松归一；`schema_name=""` 表示不比较库名，即当前库的 table-only 语义。

`RedactReport.UnresolvedScopedColumns` 是按结果列索引记录敏感类型的加法字段，仅在兜底实际替换了至少一个非空单元格时记录。MCP `query` 的 `redact.unresolved_scoped_columns` 会透出该报告；当前 Playground REST 投影只返回 `touched_columns` 和 `masked_cells`，Playground 与审计控制台均不展示该字段，首版也不增加独立指标管线或大屏面板。

### 6.3 控制面数据模型

七张业务表加迁移账本 `schema_migrations`：

| 表 | 用途 / 关键不变量 |
|---|---|
| `agents` | Agent 注册与等级（readonly/dml/ddl）；`asql_` 密钥仅存 SHA-256 |
| `datasources` | 业务库连接；密码以 AES-GCM 密文存储（主密钥 `AGENTSQL_SECRET`）；含连接数 / 超时 / 行数上限 |
| `policies` | 库 / 表 / 列三级授权策略（allow/deny），关联 Agent 与数据源 |
| `rules` | 内置规则目录与运行时启用覆盖（不在此写入任意自定义规则语义） |
| `mask_rules` | 按数据源 / schema / 表 / 列的三档作用域脱敏规则与算法 |
| `audit_logs` | **审计核心，只追加**；仓储层不提供 UPDATE / DELETE 接口；独立审计库中仅此表与索引 |
| `approvals` | 审批单（pending/approved/rejected/expired），关联审计 ID |

SQLite 与 PostgreSQL 两套 DDL 语义等价（自增键、布尔、时间类型、占位符按方言翻译）；审计 ID 在 PG 上为 `BIGINT GENERATED ALWAYS AS IDENTITY`，数据迁移保留原审计 ID。

`mask_rules` 的 v0.3 物理唯一键为 `(datasource_scope, schema_name, table_name, LOWER(TRIM(column_name)))`；其中 schema / table 保留原值精确区分，column 按归一化值判重。该约束包括 disabled 行，同物理键重复由管理 API 返回 HTTP `409` + `MASK_RULE_CONFLICT`。四套 metadata 迁移目录（SQLite / PostgreSQL 及其 metadata 布局）均使用 `0005_mask_rule_scope.sql`：新增 `schema_name`、将存量 schema / table 清为空串，并把旧按数据源+列名唯一索引重建为上述新键。audit 库不执行该迁移。

同一 datasource 有效范围内，启用的全局列规则与表级规则若列名相同但算法不一致，创建或更新返回 HTTP `409` + `MASK_RULE_SCOPE_CONFLICT`。`range` 除算法名外还比较敏感类型，以及数值规则的桶宽/规范化偏移或日期规则的规范化截断粒度。disabled 草稿豁免作用域算法冲突校验，但不豁免物理键唯一约束。

`mask_rules` 为 `range` 增加三个相互独立的可空列，不使用 JSON：`range_bucket_width INTEGER`、`range_bucket_offset INTEGER`、`range_granularity TEXT`；三列均无 `NOT NULL` 和数据库默认值，旧规则自然为 NULL。`algo=range` 或任一参数列非 NULL，都视为携带 range 参数并进入完整校验。创建与更新采用完整 PUT 语义；更新时请求中缺失的 range 参数写回 NULL，从 `range` 切换到其他算法或在 `number` / `date` 间切换时不得残留无关参数。数值规则省略 `bucket_offset` 时由 API 规范化为非 NULL 的 `0` 后持久化；存储层必须保留 NULL 与 0 的区别。

敏感发现以物理来源键 `(datasource, schema, table, column)` 识别候选列。应用候选时，PostgreSQL 和 MySQL 均生成 `schema_name=""`、`table_name=<真实表名>`、`algo=mask`、`enabled=false` 的 table-only 草稿；不同表的同名列是多条独立草稿。若候选列已被启用的全局列规则覆盖，结果标为 `CoveredByGlobal` 并跳过创建；disabled 全局草稿不阻挡新的 table-only 草稿。

### 6.4 MCP 工具（七个基础工具 + PostgreSQL B5 八个会话/计划事务工具）

| 工具 | 入参 | 行为 |
|---|---|---|
| `list_datasources` | `{}` | 仅返回当前 Agent 授权的数据源 |
| `list_schema` | `{datasource_id, table?}` | 返回 schema，按列权限过滤 |
| `explain_query` | `{datasource_id, sql}` | 只做 EXPLAIN，不执行 |
| `query` | `{datasource_id, sql}` | 仅语义只读 SELECT，走完整流水线，结果脱敏 |
| `execute_write` | `{datasource_id, sql, reason}` | 受控写，默认关闭，需 dml 及以上 + 授权 + 审计屏障 |
| `request_approval` | `{datasource_id, sql, reason}` | 生成人工审批单，不执行 |
| `get_approval_result` | `{approval_id}` | 查询审批状态 |

PostgreSQL B5 默认开启时另外注册 `open_session`、`close_session`、`get_session_status`、`begin_transaction`、`execute_transaction_statement`、`commit_transaction`、`rollback_transaction`、`get_transaction_status`；显式关闭 `mcp.sessions.enabled` 后仅保留上述七个基础工具。错误统一返回 `{decision, reason, suggestion}`，`suggestion` 用中文指导 AI 自我改写。**不提供 `execute_raw_sql` 之类的万能执行工具。**

### 6.5 管理 REST API

- 前缀 `/api/v1`，除 `POST /api/v1/auth/login` 与 `POST /api/v1/auth/refresh` 外均需管理端 Bearer 令牌；refresh 端点以轮换式 refresh token 认证。统一响应信封 `{code,msg,data}`，分页为 `{total,page,page_size,list}`。
- 端点族：认证（login、refresh、me、logout；服务端持久化撤销 access jti 与 refresh family）、agents（含 rotate-key）、datasources（含连通性 ping、敏感发现及 NoSQL 的 `native-ping` / `native-schema` / `native-query`）、`datasource-types`、policies、rules、mask_rules（三档作用域 CRUD）、audit（分页 / 筛选 / CSV/JSONL/PDF/ZIP 导出）、approvals（列表 / 裁决）、dashboard/summary（KPI / 趋势 / 分布 / 排行）、playground（静态评估，演示模式另有受控试运行）、stream（SSE）。NoSQL 原生命令只开放适配器白名单内的受控子集，详见 [NoSQL 支持范围](nosql-support.md)。
- 脱敏规则 `POST /api/v1/mask_rules` 与 `PUT /api/v1/mask_rules/{id}` 接受 `schema_name`、`table_name`、`column_name`、`sensitive_type`、`algo`、`enabled` 及可选 `datasource_id` / `range_*` 字段。作用域字段禁止首尾空白、NUL / 控制字符、`*`、`%` 和单字段内的 `.`，最长 128 个 Unicode 字符；保留原始大小写并允许中文等合法引号标识符。
- 探针：`/healthz`（存活）、`/readyz`（存储就绪，控制面与审计库双 Ping）、`/metrics`（Prometheus）。

---

## 7. 规则与决策

- **四种决策**：`allow`（放行）、`warn`（告警放行）、`approve`（转人工审批）、`deny`（拒绝，不触库）；风险分四级（拒绝 / 审批 / 告警 / 提示）。
- **规则分层**：通用规则（R001–R010，如无 WHERE 的更新 / 删除、WHERE 永真、只读 Agent 写库、库表黑名单、QPS 限流等）、PostgreSQL 专项（R1xx）、MySQL 专项（R2xx）；静态规则基于 AST 特征，动态规则结合受控 `EXPLAIN`（如全表扫描 R005）与运行时限流。
- **策略引擎**：按 Agent × 数据源解析库 / 表 / 列三级授权，叠加 Agent 等级（readonly/dml/ddl）；未显式授权默认拒绝。
- 内置规则可在控制台启用 / 停用（运行时覆盖，立即生效）；规则命中、风险等级、预估行数、归一化 SQL、各阶段延迟均写入审计，可在控制台与审计导出中追溯。
- 规则的权威清单与正反例以 `internal/rules/` 源码和 `tests/corpus/` 语料为准。

---

## 8. 兼容矩阵（v0.5.0）

| 维度 | 支持情况 |
|---|---|
| 完整防护业务库（🟢） | MySQL 8.0；PostgreSQL 14 / 15 / 16 / 17 / **18** |
| 受控只读注册项（🔵） | `oracle`、`dm`、`yashan`、`sqlserver`；连接/元数据及各自限定的只读子集，不继承 PostgreSQL/MySQL 完整防护闭环 |
| 连接级注册项（🔷） | 7 类 21 款 NoSQL / 向量数据源；ping、版本、Schema 与只读预览按适配器有界提供，不承诺完整检索或写入 |
| 待验证候选（🟡） | KingbaseES V9，等厂商环境；不计入 27 款注册表 |
| 控制面 / 元数据库 | SQLite（默认）；PostgreSQL 15+，基准与推荐 **PG18**（开源免费） |
| 审计库 | 随元数据库，或独立 PostgreSQL（最小权限仅 INSERT/SELECT） |
| MCP 传输 | stdio；Streamable HTTP `/mcp`（MCP 2025-06-18） |
| 控制台 | 深色 / 浅色双主题，单二进制内嵌，默认仅回环 |
| 部署 | Docker / Docker Compose、Linux systemd 单二进制 |
| 可观测 | Prometheus 指标 + Grafana 面板 + 健康 / 就绪探针 |
| 演示 | 一键自托管 Live Demo（只读、每日重置、六剧本） |
| 列级脱敏 | 支持全局列、表.列、模式.表.列三档作用域，唯一归属时精确匹配，未解析且可能涉及受保护表时固定阻断 `***`；九类型能力矩阵：六类支持 `mask` / `hash` / `block`，`generic` 支持 `hash` / `block`，`number` / `date` 支持 `hash` / `block` / `range`；无 key 时 `mask` / `block` / `range` 正常运行；discovery 只生成六类 `mask` table-only disabled 草稿 |

注册表的 27 款类型、类别与默认端口以 `internal/model/datasource_types.go` 为准；21 款 NoSQL / 向量的逐项对照及 HBase/Couchbase 端口例外见 [NoSQL 支持范围](nosql-support.md)。DM/Oracle 的受控 SELECT parser 仍未接通完整网关授权/脱敏链路；YashanDB 仅有 `NewYashanParser()` 离线入口，标准 `NewParser` 尚未注册它。SQL Server、达梦、Oracle、YashanDB 及其他协议候选的完整方言或商业版认证均未宣称；各自证据边界见 [Release Notes](release-notes-v0.5.md) 与 [SECURITY.md](../SECURITY.md)。

---

## 9. 质量与测试门

- 格式 / 静态检查目标：`gofmt`、`go vet`、全量 `go test -race`；核心安全包（parser / engine / rules / policy / pipeline）覆盖率目标 ≥ 80%，每条规则同时具备正例（应拦截）与反例（不应拦截）。这些是质量门要求，不表示当前全部完成。
- **决策回归语料**（`tests/corpus/`）：解析语料 + 252 条决策用例，按 PostgreSQL / MySQL 方言展开 353 次判定；要求危险漏拦为 0、误拦率低于 2%。
- **fuzz fail-closed 目标**：流水线对变异 SQL 不发生 panic，未预期输入走向拒绝；具体执行规模须以测试报告为准。
- **真实库 E2E**：用 testcontainers 拉起真实 PostgreSQL 18 与 MySQL 8，覆盖只读放行、越权拒绝、无 WHERE 写拦截、脱敏、审批、审计可查；控制面在 SQLite 与 PostgreSQL 两种布局下双跑。
- **「写攻击零触库」双证据**：以假执行器（fake executor）相关方法零调用，加上真实业务库执行前后行数 / 校验和不变，共同证明危险写与 DDL 不触库。
- **性能口径（避免误导）**：文档中的只读 P99 延迟是**剥离了真实数据库网络与 I/O 的网关 CPU 路径微基准**（假执行器、固定并发、内存态），用于守住网关自身 CPU 预算，**不代表端到端延迟**；受控写因新增事务与审计往返，真实延迟必然更高，只记录不套用该门限。
- **当前证据与保留项**：批六十一 PostgreSQL parser 的 5 ms P99 闸门在指定 Linux 环境两组各 5/5 通过，但发布日仍须按相同门槛独占复测；全仓 short tests 尚未形成 PASS。批六十七新增 parser malformed/并发错误路径与 pipeline 执行前拒绝测试，覆盖范围和离线限制见 [测试覆盖说明](test-coverage-notes.md)。

---

## 10. 授权、版本与路线图

- 源代码采用 **Apache License 2.0**；商业授权、SLA 与商标保留见 [LICENSE](../LICENSE)、[NOTICE](../NOTICE) 与 [COMMERCIAL-LICENSE.md](../COMMERCIAL-LICENSE.md)。商标 **AgentSQL** 及中文名 **智盾** 归版权人所有，fork / 衍生作品未经书面许可不得冒用其名称或标识。
- **当前待发布代码**：第 8 章的能力档位按各自边界提供；RBAC / 多租户 MVP、TOTP MFA、OIDC、LDAP/AD、审计 PDF/ZIP 与审计哈希链已有实现，不能再列作整体未交付。实际启用条件、未完成的 Agent 租户传播与身份系统终验见 [Release Notes](release-notes-v0.5.md)。
- **后续方向**：完整国产/商业数据库方言与厂商目标环境终验、法规级 WORM / 外置 SIEM、多副本 HA、K8s Operator、跨实例集中管控等仍需后续验证或实现；本规格不将其计作已交付能力。
- 商业合作：**87326549@qq.com** ｜ **https://agentsql.cn**

---

## 11. 如何贡献

- 请先阅读 [CONTRIBUTING.md](../CONTRIBUTING.md)：贡献需符合「窄而深」的范围约定、Conventional Commits 提交规范与 DCO；改动安全引擎必须同步补充决策语料并跑通质量门；改动在线演示后使用 `demo/reset` 干净重建。
- 安全漏洞请按 [SECURITY.md](../SECURITY.md) 进行私密披露，不要公开 issue。
- 文档与宣传应坚持克制、如实：不夸大防护边界，不泄露凭据 / DSN / 内部信息。

# AgentSQL v0.4 B5：MCP 多语句事务与跨请求会话——详细设计 v1

状态：**评审稿；只定义设计，不是生产放行结论**  
日期：2026-09-25  
代码基线：`feature/v0.4`，`HEAD=9cb1451`  
范围：B5 MCP 跨请求逻辑会话、受控 DML 事务、协议兼容、审计、资源治理、控制台、文档与 e2e。本文不修改生产代码，也不包含 git 提交。

## 0. 最终裁决摘要

1. **保留现有 HTTP MCP 的无状态传输；另建 AgentSQL 逻辑会话。** `Mcp-Session-Id`（若某协议版本存在）不是数据库事务句柄，也不是授权凭据。HTTP 继续 `Stateless: true`；stdio 的进程生命周期只作为清理边界。业务会话由显式工具返回 `session_id`，每次操作仍须通过当前 Agent/API key 鉴权。
2. **“多语句”是多个请求中的有序单语句，不是 stacked SQL。** 每个 `tools/call` 仍只接收一条 SQL；包含多个顶层语句、用户提交的 `BEGIN`/`COMMIT`/`ROLLBACK`/`SAVEPOINT`/`SET TRANSACTION` 一律拒绝。事务控制只能调用网关工具，由网关调用原生 driver transaction API。
3. **B5 v1 只支持计划式（planned）DML 事务。** `begin_transaction` 必须携带完整、定序、不可变的语句计划。网关在占用连接前完成解析、授权和审批判定；后续 `execute_write` 必须逐项匹配 `operation_id` 与 SQL digest。临时追加、改序、漏项都会强制回滚。
4. **活动事务仅允许受支持的 `INSERT`/`UPDATE`/`DELETE` 子集。** 首发不允许 SELECT、DDL、ADMIN、RETURNING、数据修改 CTE、MERGE、UPSERT、存储过程、临时对象、用户变量、服务端 PREPARE、savepoint 或嵌套事务。事务外的现有 `query`、单语句 DML、DDL 行为保持不变。
5. **任一 deny/error/结构不确定/审计失败使事务立即进入 rollback-only 并回滚。** `approve` 不能在持锁事务中等待：预检遇到 approve 时不打开业务事务；活动事务中若重校验新出现 approve，则立即回滚，客户端须在事务外申请完整计划审批后重新开始。mask 对写事务没有补权意义，遇到 mask 决策按不支持并回滚。
6. **连接从 native BEGIN 到 COMMIT/ROLLBACK 全程绑定同一 backend。** 空闲逻辑会话不占连接。事务默认最长 60 秒、idle-in-transaction 15 秒、单语句 5 秒；超时、请求取消、stdio EOF、显式关闭、进程关闭都触发有界回滚，回滚不能确认时销毁物理连接并保持 fail-closed。
7. **B5 DML 不是 B2 SELECT-only 的自然延伸。** B2 当前 `agentsql-binder-4.1`、`PostgresPreparedSelect` 和列权限语义只覆盖 SELECT，不能直接给 DML 背书。B5 新增 DML action grant 与列 usage（`write_target`、`reference`）；PostgreSQL 必须扩展 DB 侧 binder ABI，MySQL 必须完成受限 DML binder + catalog/MDL 证明。能力未就绪时返回 `AUTH_DML_BINDER_REQUIRED`，不得退化为“声称列授权”的表级检查。
8. **审计以 `transaction_id + transaction_seq` 串联。** 每条语句的决策/执行、begin、commit intent、commit outcome、rollback 都有独立耐久事件。commit intent 未耐久则回滚；business commit 后 outcome 审计失败无法物理撤销，返回 `TX_COMMIT_OUTCOME_UNKNOWN`，禁止自动重试，并进入运维对账。这是跨库原子性边界，不伪装成可回滚。
9. **跨请求会话只保存网关状态。** 支持固定 datasource、服务端身份、不可变客户端元数据、事务默认隔离级别、序号和幂等结果；不支持跨请求数据库 session variable、临时表、advisory lock、LISTEN、服务端 prepared statement。续期只能延长空闲逻辑会话，不能延长活动事务。
10. **活事务不可跨实例迁移。** `session_id` 绑定 `instance_id`，部署必须 sticky route；错实例返回 `SESSION_WRONG_INSTANCE`。实例退出必须先停止新事务、回滚存量事务再关池。多实例透明迁移不属于 B5 v1。

## 1. 背景、目标、非目标与术语

AgentSQL 当前把一次 MCP tool call 作为一个完整安全边界：单条 SQL 在同一次请求内完成解析、决策、执行和审计。这一边界适合独立查询/写入，却无法表达“几条写入要么全部提交、要么全部回滚”，也无法让无状态 HTTP 的后续请求安全地找到同一 native transaction。B5 的核心不是放开 SQL script，而是在不削弱现有单语句检查、B2 结果封印和 B6 审计屏障的前提下，增加一个网关理解并拥有的短生命周期事务协调器。

### 1.1 目标

- 给 MCP 客户端提供跨请求、可审计、可限额的逻辑会话。
- 在一个逻辑会话内，以同一物理 backend 和原生事务执行多条已授权 DML。
- 保证任何调用者都不能绕过现有 parser、rule、approval、audit、reservation 和 AuthorizedExecute 唯一执行入口。
- 对断连、超时、取消、审计不可用、DB 错误、commit 结果不明给出确定状态机与客户端行为。
- 兼容现有七个工具、stdio、无状态 HTTP 和现有单语句客户端。

### 1.2 非目标

- 不接收 SQL script，不提供通用数据库 shell。
- 不支持 XA/两阶段提交、跨 datasource 事务、跨实例迁移或事务恢复。
- 不把 MCP sampling/elicitation 当审批通道，不允许模型替人批准。
- 不在活动写事务中返回 SELECT 结果；B2 的结果封印路径保持独立。
- 不支持任意数据库 session 状态、用户 `SET`、临时对象或服务端 prepared statement。
- 不改变现有审批“批准不自动执行”的原则。

### 1.3 术语

| 术语 | 含义 |
|---|---|
| MCP protocol session | 协议/传输层 initialize 后的连接状态；HTTP 无状态模式中不跨请求保存 |
| AgentSQL logical session | B5 显式创建、绑定 Agent 和 datasource 的网关对象，简称“会话” |
| transaction | 会话内绑定一个 native DB transaction 和物理连接的活动事务 |
| plan | begin 前提交的有序单语句清单，包含 operation id、SQL digest、reason 等 |
| transport binding | stdio 进程或可选有状态 HTTP 连接的清理关联；不是授权主体 |
| rollback-only | 事务不能再执行或提交，只能回滚/查询终态 |
| backend identity | PostgreSQL backend PID 或 MySQL connection/thread identity，仅内部诊断使用 |

## 2. 真实代码现状（基于 9cb1451）

### 2.1 MCP 层

- `internal/mcpserver/doc.go` 明确描述为七个固定工具和“stateless multi-tenant Streamable HTTP”。
- `server.go` 使用 `mcp.NewServer(..., nil)`，注册 `list_datasources`、`list_schema`、`explain_query`、`query`、`execute_write`、`request_approval`、`get_approval_result`。没有 B5 session/transaction 工具，没有 initialized hook，也没有 sampling 调用。
- `tools.go` 的 query/execute schema 都要求单条 SQL、禁止 stacked statements。`protocolResult` 只有 `decision=error` 才设置 MCP `isError`。
- `handlers.go` 每次 tool call 独立构造 `pipeline.Request`，未设置 `SessionID`；`query` 只接受 SELECT，`execute_write` 只接受 INSERT/UPDATE/DELETE/DDL；DDL 要求 `agent.Level=ddl`；approval 只建记录，不执行。
- `http.go` 的 Streamable HTTP 固定 `Stateless: true`、JSON response、4 MiB body、请求取消传播；Bearer API key 每请求鉴权。无状态下 GET/DELETE `/mcp` 返回 405，不生成 `Mcp-Session-Id`。
- `stdio_transport.go` 是带大小限制的逐行 JSON frame；stdin/transport 关闭会结束 server，但目前没有业务会话清理器。
- 当前测试以 MCP `2025-06-18` 为产品事实；依赖的 go-sdk v1.7.0 源码还认识更新协议版本，但这只是依赖能力，不能等同于 AgentSQL 已承诺兼容。

结论：**当前 MCP 只支持每次 `tools/call` 一条语句；无 MCP 跨请求业务会话，也无 MCP 多请求事务。**

### 2.2 pipeline 与受控执行

- `internal/pipeline/types.go` 已有 `Request.SessionID`，但 MCP 不填它。
- `Process` 每请求重新鉴权、加载策略、解析、静态检查和动态决策；parser/rule 会拒绝多语句。
- transaction control（BEGIN/START/COMMIT/ROLLBACK/SAVEPOINT/SET TRANSACTION/SET AUTOCOMMIT）在 execution barrier 固定拒绝。
- SELECT 进入 read 路径；INSERT/UPDATE/DELETE 进入单语句 transactional write；DDL/多数 ADMIN 走 pre-intent audit。
- 当前 DML 是“begin native tx → 执行一条 → beforeCommit 内同步审计 → commit”。审计失败会 rollback；commit 返回错误映射到 `COMMIT_OUTCOME_UNKNOWN`。
- 请求 wall 默认 10 秒、statement 5 秒、lock 2 秒。
- B2 column path 明确以空 `sessionID` 调 `AuthorizedExecute`，不能加入现有 session。

结论：pipeline 已有可复用的单语句授权、审计和失败语义，但没有 transaction coordinator；不能通过循环调用现有 `Process` 拼出正确的跨请求事务。

### 2.3 authorizedexecute / businessdb

- `authorizedexecute.Statement` 是单调用 capability；Gateway 不暴露 raw DB、pool、connection 或 transaction。
- `AuthorizedExecute(..., sessionID)` 在 sessionID 非空时调用 `businessdb.Executor.OpenSession`。Manager 按 datasource 复用 pool。
- PostgreSQL 用 pgxpool，MySQL 用 `database/sql`；默认 datasource `ConnLimit=5`、statement timeout 5 秒。
- `OpenSession` 获取并长期占用一条物理连接，把新 Session 存入 executor 的 map；**没有 Get/Resume 既有 Session 的公开接口**，重复 session id 会失败。
- Session 内部有串行 `operationMu` 和事务词法状态机；Session 的 Query/Execute 技术上能发送原始事务控制 SQL，但上层 pipeline 已禁止。该状态机用于规则/观测，不是安全的事务所有权证明。
- `Session.BeginWriteTx`/`WriteTx` 能在固定连接上用原生事务执行，但当前 Statement 每次只执行一条并立即结束。
- Session close 若检测到活动事务会 rollback；rollback 失败时 PG hijack/close、MySQL 丢弃连接。
- pipeline 成功使用非空 SessionID 后只 `ReleaseReservation`，不 `Close`；deny/approve/explain 才 `Close`。由于没有 resume API，这是一段内部脚手架，不是可用的跨请求会话，并可能留下占用连接。

结论：**可复用的是“连接绑定和 native tx 原语”，不是现有 SessionID API 或 SQL 事务状态机。B5 必须建立拥有者明确的新 transaction capability。**

### 2.4 B2、审批、审计与 fence

- `internal/controlledread.Service` 是管理面 discovery 的 fail-closed reader：只暴露 typed `ListSchema`/`Sample` port，不接收调用者 SQL，使用独立限流和 12 秒总 deadline/2 秒 statement timeout；它没有 session/transaction 参数。B5 不得把该管理面 reader 变成第二个事务入口。
- `internal/columnauth` 只定义 SELECT 的 `output/reference` usage；DML 明确走旧表级/profile 路径，不能标称 B2 列授权。
- PostgreSQL `agentsql_binder` 当前 ABI 为 `agentsql-binder-4.1`。`prepared_manifest` 虽有 command_type，Go 端校验强制 `SELECT`；`PostgresPreparedSelect` 只执行 sealed SELECT。
- B2 路径在请求内完成业务结束、durable audit、最终 control fence、delivery seal 后才返回数据；长写事务不能直接套用该交付顺序。
- Approval 目前是单 SQL pending record；approved 不自动执行，也不是可复用 ticket。没有有序事务 plan、TTL、消费状态或 plan digest。
- `internal/model` 中 Datasource 已有连接数、statement timeout、row limit 等边界，Agent 已有 level/status/key hash；`AuditLog` 已有 `SessionID`，但没有 TransactionID、transaction sequence 或 final outcome 字段。
- audit store 的 `AppendBatch` 和 `internal/audit/group_commit.go` 已提供同步耐久、EventUUID 去重、按 chain 原子 append、过载/结果不明语义。B5 应复用，不另造异步“最终会写”的审计通道。
- `internal/store` 的审批创建与审计已有 combined-store 原子路径和 separate-store 补偿路径；fence/control snapshot 是短事务设计。B5 可扩充记录结构，但不能假定审批、审计、业务数据库天然同库原子。
- B2 的 control/business lock order 和 final fence 假设业务资源已释放；B5 不能在持有长 business tx 时反向获取 control lock。
- 现有 `docs/INTEGRATIONS.md` 明确协议版本 `2025-06-18`、HTTP stateless、所有 SQL 参数只接收单条语句；`docs/SPEC.md` 和 `USER_GUIDE.md` 也明确 MCP 不是跨请求事务代理。B5 落地必须同步修改这些产品事实，不能只改代码。

### 2.5 当前能力真值表

| 能力 | 当前 | B5 目标 |
|---|---:|---:|
| 每次 MCP call 单 SQL | 是 | 保持 |
| stacked SQL/script | 拒绝 | 继续拒绝 |
| HTTP 跨请求 MCP session | 否（stateless） | 仍否；用显式逻辑 session |
| stdio 生命周期 | 单进程连接 | 增加 EOF 清理 |
| 跨请求 native transaction | 否 | planned DML transaction |
| session 物理连接原语 | 内部存在、不能 resume | 重构为受控 tx capability |
| DML 表级策略 | 是 | 作为粗粒度前置门 |
| DML 列级授权 | 否 | 新 DML binder/grant，fail-closed |
| SELECT 列授权 | PG B2 | 事务外保持；不加入写事务 v1 |
| 事务关联审计 | 仅 SessionID | tx id + seq + final outcome |

## 3. 架构与不可破坏的不变量

```text
MCP transport (stdio / stateless HTTP)
        │ authenticate every operation
        ▼
MCP tool handlers ──► SessionManager (logical lease, quota, idempotency)
        │                         │
        │                         └── one active TransactionContext at most
        ▼
TransactionCoordinator (plan, approval, state machine, audit barriers)
        │
        ▼
pipeline authorization snapshot + DMLAuthorizer
        │ private sealed capability only
        ▼
authorizedexecute.TransactionGateway
        │
        ▼
businessdb native tx on one pinned physical connection
```

必须始终成立：

1. 原始 SQL 只能从现有 `AuthorizedExecute` facade 的扩展入口进入；MCP、session manager、approval store、console 都拿不到 raw executor。
2. 客户端不能提交事务控制 SQL；事务状态只由 coordinator 调原生 API 改变。
3. 一会话最多一个活动事务；一个活动事务同一时刻最多一个 operation。
4. 计划和执行都从 raw SQL 重新 parse/bind；客户端 AST、表名、列名、digest 只作待验证输入。
5. DB transaction 只能在全部计划项完成静态授权且审批已满足后开始。
6. 每条 DML 执行后，下一条执行前必须完成该条 durable audit；任一缺口触发 rollback。
7. commit intent durable 之前不得调用 DB commit；commit outcome 不确定时不得返回成功或自动重试。
8. 空闲逻辑 session 不占业务连接；活动事务始终持有同一 backend，不从 pool 重借。
9. 所有终止路径幂等；close/timeout/cancel 不能把活动连接直接放回池。
10. 任何无法证明 ownership、plan identity、binder capability、catalog identity、audit durability 或 transaction state 的情况都 fail-closed。

## 4. 会话与协议生命周期

### 4.1 两层会话的裁决

MCP initialize/initialized 只完成协议协商；不得隐式创建数据库会话或预占连接。原因是：

- 当前 HTTP 明确无状态，每个 POST 都可能落到新的 SDK 临时 protocol session；
- 老客户端不会发送 session header，直接切换 stateful handler 会破坏 GET/DELETE 与 header 语义；
- 新协议方向不应让产品业务能力依赖某一版 transport session header；
- 数据库事务还要绑定 Agent、API key、datasource、quota、instance 和审计，MCP transport id 不足以承担这些安全语义。

因此 B5 用普通 tools 暴露显式逻辑会话。`initialize` 响应可在版本允许时声明 experimental capability `io.agentsql/transactions`，但工具列表和结构化返回是唯一功能事实；`notifications/initialized` 只记 telemetry，不改变授权状态。

### 4.2 状态机与 lease

```text
ABSENT --open--> READY --begin--> TX_ACTIVE --commit--> READY
                       │    │          └--failure/deny/timeout--> ROLLBACK_ONLY
                       │    └--rollback-------------------------> READY
                       └--close/idle/absolute timeout-----------> CLOSED/EXPIRED

ROLLBACK_ONLY --forced rollback succeeds--> READY
              --rollback cannot be confirmed--> QUARANTINED then CLOSED
```

默认值和硬上限：

| 项 | 默认 | 硬上限/规则 |
|---|---:|---|
| session idle TTL | 10 min | 可配置但不得超过 30 min |
| session absolute TTL | 60 min | 不可续过创建时间 + 60 min |
| active tx idle TTL | 15 s | 可下调；最高 30 s |
| active tx wall | 60 s | 不可续期 |
| single statement | 5 s | 沿用 datasource 更小值 |
| rollback cleanup | 5 s | 独立 cleanup context，不继承已取消 request |
| closed tombstone | 15 min | 用于幂等重放和确定终态 |

`renew_session` 只在 READY 时更新 idle deadline；TX_ACTIVE 时返回 `SESSION_RENEW_DURING_TX`，防止续期绕过长事务上限。任一有效、鉴权成功的 status/execute 操作可触碰 session idle lease，但不能触碰 tx deadline。

### 4.3 创建、鉴权与清理

- `session_id = as_sess_<128-bit random base64url>`，`transaction_id = as_tx_<128-bit random base64url>`；二者都是标识符而非单独凭据。
- session 固定绑定 `instance_id`、Agent ID、API-key hash/version、tenant（若模型启用）、datasource ID、transport scope 和创建时 agent level。每次操作重新鉴权并做常量时间 owner 比对；外部统一返回 `SESSION_NOT_FOUND_OR_DENIED`，避免枚举。
- API key 轮换、Agent disabled/deleted、datasource disabled/secret revision 改变：READY session 关闭；活动事务立即 rollback。
- stdio：绑定该 server process 的 scope，EOF、transport close、server shutdown 必须回滚并关闭全部 scope session。
- HTTP stateless：正常 POST 结束或 TCP 断开不关闭 idle session，否则无法跨请求；但 operation 执行中 request context 取消会取消 DB statement 并强制回滚。
- 显式 `close_session` 幂等。READY 直接关闭；TX_ACTIVE 等价于“rollback then close”。rollback 不能确认则销毁物理连接，返回失败终态而不是保持可用。
- sweeper 使用 monotonic deadline；进程 shutdown 顺序固定为：拒绝 open/begin → 取消 active operation → 有界 rollback → 丢弃未确认连接 → flush audit → 关 pool。

### 4.4 协议版本与 MCP 能力

- B5 GA 至少完整回归并承诺当前产品版本 `2025-06-18`；`2025-11-25` 只有通过同一 conformance suite 后才加入 allowlist。SDK 认识某版本不构成产品承诺。
- 对不支持版本沿 SDK 标准 initialize negotiation 返回协议错误；不得静默降级后仍广告 transaction capability。
- 未来/无协议 session 的版本仍可工作，因为 `session_id` 是工具参数，不依赖 `Mcp-Session-Id`。
- B5 不发 sampling、roots、elicitation 或其他 server-to-client request；不以客户端声明 sampling 能力作为安全条件。人工审批继续走 control plane `request_*_approval` + polling。
- transaction expired 通知只可作 best-effort UX：stdio 或未来 stateful transport 可发，HTTP stateless 客户端用 `get_session`/审计查询；正确性不得依赖通知被收到。

官方协议依据：MCP initialize 生命周期与历史 session header 语义见 <https://modelcontextprotocol.io/specification/2025-06-18/basic/lifecycle>、<https://modelcontextprotocol.io/specification/2025-06-18/basic/transports>；协议演进见 <https://modelcontextprotocol.io/specification/draft/changelog>。实现以仓库锁定的 go-sdk 版本与合格测试为准。

## 5. 受控事务语义

### 5.1 为什么必须计划式

如果允许客户端先 BEGIN、再临时决定下一条 SQL，则直到某条规则返回 approve 才知道需要等待人；此时连接、行锁和 metadata lock 已持有，审批超时会放大拒绝服务。计划式 begin 让网关在拿连接前看到完整语句集合，并把审批绑定到确切顺序和 digest。

v1 `begin_transaction` 因此必须携带 1..32 个 plan item。每项包含：

```json
{
  "operation_id": "client-unique-op-1",
  "tool": "execute_write",
  "sql": "UPDATE accounts SET status='closed' WHERE id=42",
  "reason": "close dormant account"
}
```

canonical plan digest 覆盖：版本、Agent、datasource、dialect、隔离级别、顺序、operation id、raw SQL SHA-256、normalized AST digest、reason digest、policy/control revision、DML binder capability digest、approval id（如有）。reason 原文不进入 digest 日志展示，但其 digest 必须绑定。

### 5.2 begin 流程

1. 验证 session ownership、READY 状态、request id、expected session sequence、会话/事务 quota。
2. 对所有 raw SQL 做大小限制、single-statement parse 和 transaction-control deny；任何失败不打开 DB transaction。
3. 固定一个不可变 authorization snapshot；对每项执行 agent/profile、table policy、DML action/column usage、结构能力检查。
4. 对需要 EXPLAIN 的规则在短生命周期受控连接上完成预检；不得开始用户事务等待 EXPLAIN 或审批。
5. 汇总决策：
   - 任一 deny/error/unsupported：耐久记录 plan decision，返回失败，不占 transaction slot；
   - 任一 approve 且无有效 plan approval：创建/返回事务审批，仍不占 transaction slot；
   - 全部 allow/warn 或有完全匹配且未消费的 approval：继续；
   - mask：返回 `TX_MASK_UNSUPPORTED`，因为 mask 不能授权写入或过滤条件。
6. 原子消费 approval lease；占用 transaction slot 和 datasource connection slot。
7. 从 pool 取一条 connection，在该 connection 上重新 bind/capability check 全部 plan；任何差异释放/销毁连接并失败。
8. 调原生 BeginTx，写 durable `tx_begin`；审计失败则立即 rollback。
9. 返回 tx id、plan digest、下一 operation id、expected sequence 和 deadlines。

control policy snapshot 不跨请求持有 control DB 锁。事务采用“begin 时冻结的授权快照”，其最大撤销延迟由 60 秒 tx wall 限定；Agent/datasource/key 的 hard disable 每请求实时检查并触发 rollback。若产品要求策略变更对已开事务零延迟，必须另建非阻塞全局 revocation epoch/lease 服务，不能在持有 business tx 时反向拿 control lock。本项必须由安全与控制面负责人签收。

### 5.3 逐条执行

每次带 session/tx 的 `execute_write` 必须提供 `operation_id`、`request_id`、`expected_seq`，并满足：

- operation id 正好等于 plan 中下一项，SQL 和 reason digest 完全相等；
- 同一 session 没有并发 operation；
- agent/datasource 仍启用，tx idle/wall 未过期，连接/backend identity 未变；
- 重新解析并检查 capability seal、catalog fingerprint 和固定 snapshot；不能只相信 begin 缓存的 AST；
- 执行后受影响行数、总预算未超限；超限时即使 DB 已执行，也必须 rollback；
- durable 写入该 statement 的决策、执行结果、binder/catalog/policy digest 后，才推进 `next_ordinal` 并返回。

任一 DB statement error（包括 PostgreSQL aborted transaction、MySQL deadlock/lock timeout/duplicate key）、context cancel、审计错误或重校验不一致，都把 transaction 设为 rollback-only 并立即尝试全量 rollback。MySQL 即使错误后 native tx 仍可用，也不继续执行。

### 5.4 commit / rollback

`commit_transaction` 只有在全部 plan item 成功且顺序完全消费时可调用：

1. CAS 校验 request id、expected seq、tx id、plan completeness、deadline 和 state。
2. 写 durable `tx_commit_intent`，包含完整 statement audit digest、最后 sequence、总 affected rows、backend identity hash。
3. intent 失败/overload：不调用 commit，强制 rollback。
4. 调一次 native Commit。不得因 timeout、断连或 retry 自动再次 commit。
5. commit 明确成功：写 `tx_commit_outcome=committed`，释放连接/transaction quota，session 回 READY，缓存幂等结果。
6. commit 明确失败且 driver 证明未提交：写 rollback/failure outcome，丢弃连接，session 回 READY 或 CLOSED。
7. commit 返回网络错误/超时/driver 无法证明：状态 `OUTCOME_UNKNOWN`，丢弃连接，写 best-effort unknown outcome；向客户端返回 `TX_COMMIT_OUTCOME_UNKNOWN`、`retryable=false`。后续相同 request id 只能返回同一未知终态。
8. business commit 已成功但 outcome audit 失败：同样返回 `TX_COMMIT_OUTCOME_UNKNOWN`，触发高优告警和对账；绝不谎报 rollback。

`rollback_transaction` 幂等；未执行任何 item 也必须审计。rollback 明确成功后 session 可回 READY；rollback 不明或失败时关闭 session 并销毁连接，不允许继续。

### 5.5 混合决策与审批表

| 情况 | begin 前 | 活动事务中重校验 | 最终行为 |
|---|---|---|---|
| 全 allow | 打开事务 | 执行 | 可 commit |
| allow + warn | 打开，逐项审计 warn | 执行 | 可 commit |
| 任一 deny | 不打开 | 若新出现则 rollback | deny/rolled_back |
| 任一 approve，无票 | 不打开，返回 plan approval | 若新出现则 rollback | 客户端获批后新开事务 |
| approval 过期/已消费/digest 不符 | 不打开 | 不适用 | fail-closed |
| 任一 mask | 不打开 | rollback | `TX_MASK_UNSUPPORTED` |
| 审计不可用/overloaded | 不打开或 rollback | rollback | 不可继续 |

审批规则：

- 事务审批必须在业务事务外创建，绑定完整 plan digest、Agent、datasource、policy revision、最大 affected-row budget、过期时间；默认 TTL 10 分钟。
- approval 只能消费一次；消费与 begin lease 使用 control store CAS。begin 在消费后失败时记录 `consumed_begin_failed`，不得复用。
- approval 可以覆盖策略定义为“可人工批准”的风险，不能覆盖 agent/profile/table deny、缺失列 grant、binder/capability 不完整、stacked SQL、DDL/transaction control 或资源硬限制。
- 审批等待期间不占 session transaction slot、DB connection、business lock；session 自身可续期但受 absolute TTL 限制。

## 6. DML 授权方案及与 B2 的关系

### 6.1 权限模型

B5 先执行现有 Agent level、profile、table allow/deny、R003 等规则，再执行新的 DML 精确授权。新模型不是把 B2 的 `output` 生搬到 DML：

| DML | 必需 relation action | 必需列 usage |
|---|---|---|
| INSERT | `insert` | 所有显式 target 为 `write_target`；表达式读列为 `reference` |
| UPDATE | `update` | assignment target 为 `write_target`；RHS、WHERE 为 `reference` |
| DELETE | `delete` | WHERE/USING 可见列为 `reference`；删除行由 relation action 授权 |

新增控制面对象建议为：

```text
dml_relation_grants(
  agent_scope, datasource_id, relation_identity,
  action insert|update|delete, effect allow|deny, revision
)

dml_column_grants(
  agent_scope, datasource_id, relation_identity, column_identity,
  usage write_target|reference, effect allow|deny, revision
)
```

优先级固定为 explicit deny > 缺 relation action > 缺 column usage > allow。mask 永远不能补足 `write_target` 或 `reference`。审批也不能补足结构授权缺口。

### 6.2 v1 支持的封闭 DML 子集

两个方言首发共同支持的最小安全子集：永久普通 base table 上的单 target、非递归、无子查询/CTE 的 INSERT/UPDATE/DELETE；INSERT 必须显式列清单；表达式只允许 binder 版本化 allowlist 中的 literal、参数占位（当参数化能力另行落地后）、直接列引用、布尔/比较/基础算术。以下一律 `TX_UNSUPPORTED_STATEMENT` 或更具体授权错误：

- `RETURNING`、`MERGE`、PostgreSQL `UPDATE ... FROM`、`DELETE ... USING`；
- `ON CONFLICT` / `ON DUPLICATE KEY UPDATE` / `REPLACE`；
- data-modifying CTE、子查询、view target、partition root/child、继承；
- trigger、rule、RLS、generated/default/identity/auto-increment（不能完整授权隐式写时）、FK cascade、CHECK/表达式索引等隐式可执行闭包；
- CALL/DO/function side effect、COPY/LOAD、SELECT INTO、DDL、ADMIN；
- 临时/unlogged/foreign/federated/NDB 等非支持对象。

这是首发安全边界，不是 SQL parser 功能列表。扩大任一形态前必须让 binder 枚举所有显式/隐式读写对象和列，并新增真实数据库攻击测试。

### 6.3 PostgreSQL：需要扩展 B2 DB 侧 binder

明确裁决：**需要 DB 侧扩展。** 可以复用 B2 的扩展部署、capability attestation、analyzed tree walker、stable OID、catalog fingerprint、relation locking、sealed prepared execution框架；不能复用当前 SELECT-only contract 直接执行 DML。

需要一个新 ABI（实现时确定正式版本，设计占位 `agentsql-binder-dml-1`），至少输出：

- command type 与唯一 target relation OID；
- 每个 Var/target entry 的 relation OID、attnum、site、usage=`write_target|reference`；
- relation closure、checkAsUser、requiredPerms、rewritten query 数量；
- trigger/rule/RLS/default/generated/identity/constraint/index/function/operator/type/collation objects；
- analyzed digest、dependency digest、backend PID、transaction identity、plan generation/replan/invalidation；
- 可在 **coordinator 已持有的同一 native transaction/connection** 上 prepare、seal、execute、post-verify 的 capability。

Go 端新增 `PostgresPreparedDML` 私有类型，Execute 不接受 SQL，只消费 sealed statement；执行前后 manifest/capability/catalog 不一致则 rollback。当前 `PostgresPreparedSelect` 继续只服务 B2，绝不改名后混用。PG 14–18 每个 major 都要构建并签名匹配的 extension；ABI/hash 不符时 datasource 的 `b5_dml_transaction` capability 为 unsupported。

### 6.4 MySQL：不要求 server extension，但要求等价证明

MySQL v1 使用版本锁定的 Go binder、`information_schema`/`SHOW CREATE TABLE` inspector、同一连接 native transaction 与 metadata lock 行为证明：

1. begin 前在候选连接解析并建立 target/column/object manifest；
2. 只接受上述封闭子集和永久非分区 InnoDB base table；
3. 拒绝任何 trigger、default/generated/on-update/auto-increment、FK/CHECK、functional index、EVENT/routine/view 等隐式闭包；
4. 执行前取 `Fpre`；执行 DML 后由事务持有相关 MDL，再取 `Fpost`；二者、server UUID/version/sql_mode/lower_case_table_names/collation 必须一致；
5. catalog 权限不完整、metadata 查询超时、MDL 语义在目标 patch 版本未通过真实测试，一律 `AUTH_DML_BINDER_REQUIRED`；
6. MySQL 服务账号不能具有 DDL/admin、FILE、routine 创建等能力，作为纵深防御。

MySQL 8.0 与 8.4 的 MDL/fingerprint 故障矩阵是 release gate。若不能证明 catalog race 可在 commit 前发现并 rollback，该方言 B5 保持 feature-off，不得退化到 AST 名称匹配。

### 6.5 B2 SELECT-only 的组合规则

- 事务外 `query` 继续走现有 B2 SELECT path，完成 business end → audit → final fence → delivery seal。
- 活动 B5 写事务中的 SELECT v1 固定返回 `TX_SELECT_UNSUPPORTED` 并强制 rollback。理由是现有 B2 会另开 request-owned transaction/connection，看不到未提交写；若强塞入 pinned tx，又无法在返回结果前满足“business transaction 已结束”的 B2 seal 不变量。
- DML `RETURNING` 也视为写事务内数据输出，首发拒绝。
- 后续若要支持事务内 SELECT，必须单独设计“transaction-scoped delivery seal”、同连接 PG binder、mask、结果预算、审计和 commit/rollback 后可观察性，不能作为 B5 v1 小改动。
- B2 开启不等于自动开启 B5 DML 列授权；控制台必须分别显示 `b2_select_column_auth` 与 `b5_dml_transaction_auth` capability。

## 7. 连接、锁、超时与失败边界

### 7.1 连接绑定

- `begin_transaction` 从 datasource 现有 pool 获取一条连接；native tx 对象本身就是连接绑定凭据。
- `TransactionContext` 保存 backend identity hash，只作一致性校验/审计，不允许客户端指定。
- 每次 operation 通过同一私有 tx capability 执行；禁止按 tx id 去 pool 重新 acquire。
- COMMIT/ROLLBACK 后立即清除 SQL capability，再释放正常连接。任何 protocol/driver 状态不确定、取消未确认、rollback 失败都 close/hijack 连接，不能放回 pool。
- 不依赖 session SQL parser 对 BEGIN/COMMIT 的词法追踪来判断真实 tx 状态；状态由 native transaction 返回值与 coordinator CAS 决定。

### 7.2 锁与 policy revision

- 禁止跨请求持有 control-store transaction/read lock；否则策略发布、审批和审计可能被长事务阻塞。
- begin 在获取 business connection 前构造不可变 control snapshot。活动 tx 内仅使用 snapshot 和服务内 hard-revocation epoch，不形成 business → control 同时持锁。
- business relation/row/MDL lock 的寿命由 tx wall/idle hard deadline 限制。每个 DB session 还设置网关生成的安全参数：PG `statement_timeout`、`lock_timeout`、`idle_in_transaction_session_timeout` 使用 `SET LOCAL`；MySQL 使用 driver deadline、lock wait timeout allowlist 和外部 watchdog。用户不能提交 SET。
- policy 正常 revision 在 transaction 开始后不追溯；hard disable/revocation epoch 追溯并 rollback。最大正常撤销延迟 60 秒，必须在 UI/API 中可见。

### 7.3 确定性故障表

| 事件 | DB 行为 | session/tx 终态 | 客户端结果 |
|---|---|---|---|
| idle timeout | cleanup context rollback | READY；失败则 CLOSED | `TX_IDLE_TIMEOUT` |
| tx wall timeout | cancel current op + rollback | READY/CLOSED | `TX_MAX_DURATION` |
| statement timeout/cancel | driver cancel + rollback | READY/CLOSED | `TX_STATEMENT_TIMEOUT` |
| HTTP 请求在执行中断开 | cancel + rollback | READY/CLOSED | 原连接无响应；status 可查终态 |
| HTTP 请求间断开 | 不处理，等下次请求或 idle timeout | TX_ACTIVE | 正常跨请求语义 |
| stdio EOF | rollback + close session | CLOSED | 无响应，审计记录 transport close |
| DB deadlock/constraint/error | 全事务 rollback | READY/CLOSED | 固定 DB stage/error，禁止继续 |
| audit failure before commit | rollback | READY/CLOSED | `AUDIT_UNAVAILABLE/OVERLOADED` |
| commit outcome unknown | 丢弃连接 | OUTCOME_UNKNOWN tombstone | 不可重试 |
| rollback outcome unknown | 丢弃连接 | CLOSED/QUARANTINED | `TX_ROLLBACK_UNCONFIRMED` |
| server crash | DB 连接断开，由 DB rollback 未提交 tx | session 丢失；commit 窗口可 unknown | 通过审计 intent 对账 |

客户端取消 `commit_transaction` 不能取消已发出的 COMMIT 语义；coordinator 使用有界独立 commit context 完成一次判定，并缓存终态。若无法判定，只能 unknown。

## 8. 跨请求 session 状态与参数边界

允许保存：

- 服务端派生的 Agent/tenant/API key revision、datasource、dialect、instance；
- 创建时间、idle/absolute deadline、state、operation sequence；
- client name/version、conversation id、最多 16 个键值各 64 bytes 的不可信标签（只供审计；策略若使用必须显式标记 untrusted）；
- allowlist 中的默认 isolation；
- 当前 immutable plan、tx state、quota lease、幂等结果/tombstone。

不允许保存或跨请求复用：

- 任意 `SET`/session/system/user variable、search_path/sql_mode/time_zone/role 变化；
- temporary table/view/function、advisory lock、cursor、portal、LISTEN/NOTIFY；
- 客户端命名或服务端 DB prepared statement；
- 未提交 SELECT result、raw credential、可序列化 connection/tx handle；
- 客户端提供的 AST、authorization proof、catalog identity 或 backend id。

每次获取连接后，网关用固定模板设置/核验 timezone、role、search_path/sql_mode、charset/collation、timeout，结束时执行 driver reset 或直接销毁不可信连接。v1 SQL 仍为现有 raw SQL 字段，不借 B5 偷渡参数化接口；未来参数化必须定义 typed value limits、digest 和 audit redaction，再单独评审。

## 9. 数据结构与接口

### 9.1 内存状态

```go
type LogicalSession struct {
    ID, InstanceID, AgentID, TenantID, APIKeyRevision string
    DatasourceID, Dialect, TransportScope             string
    State SessionState // Ready, TxActive, Closing, Closed, Expired, Quarantined
    CreatedAt, LastActivity, IdleDeadline, AbsoluteDeadline time.Time
    Sequence uint64
    Tx *TransactionContext
    SessionLease ReservationLease
    Idempotency *BoundedResultCache
}

type TransactionContext struct {
    ID, PlanDigest, PolicyDigest, BinderDigest string
    State TxState // Beginning, Active, RollbackOnly, Committing, ...
    StartedAt, LastActivity, IdleDeadline, WallDeadline time.Time
    NextOrdinal, StatementCount int
    TotalSQLBytes, TotalAffectedRows int64
    BackendIdentityHash string
    Plan []PlannedStatement
    Native authorizedexecute.TransactionCapability // private, non-serializable
    TxLease, ConnectionLease ReservationLease
    AuditHead string
}

type PlannedStatement struct {
    Ordinal int
    OperationID, Tool, SQL, Reason string
    RawDigest, ASTDigest, AuthorizationDigest string
    Decision, DecisionReason string
    MaxAffectedRows int64
}
```

实际实现不得让 `SQL` 出现在通用日志/String 方法；状态 API 默认只返回 digest、normalized/truncated summary。ID cache 和计划大小都必须有硬上限。所有状态转换在 session shard lock/CAS 下完成，DB I/O 不持全局 map lock。

### 9.2 SessionManager

```go
type SessionManager interface {
    Open(context.Context, SessionPrincipal, OpenSessionRequest) (SessionView, error)
    Get(context.Context, SessionPrincipal, string) (SessionView, error)
    Renew(context.Context, SessionPrincipal, RenewSessionRequest) (SessionView, error)
    Close(context.Context, SessionPrincipal, CloseSessionRequest) (SessionView, error)
    WithOperation(context.Context, SessionPrincipal, OperationLeaseRequest,
        func(*LogicalSession) (OperationResult, error)) (OperationResult, error)
    CloseScope(context.Context, string, CloseReason) error
    Sweep(context.Context, time.Time) SweepReport
}
```

`WithOperation` 原子校验 owner、instance、sequence、request id、并发状态并占用 operation lease。相同 request id + 相同输入 digest 返回缓存结果；相同 id + 不同 digest 返回 `IDEMPOTENCY_CONFLICT`。

### 9.3 Pipeline / authorizedexecute 端口

现有 `Pipeline.Process` 与 `AuthorizedExecute` 签名保持，避免改变单语句客户端。新增独立 coordinator 端口：

```go
type TransactionCoordinator interface {
    Begin(context.Context, Principal, BeginTransactionRequest) (TransactionView, error)
    ExecuteNext(context.Context, Principal, ExecuteTransactionRequest) (ToolResponse, error)
    Commit(context.Context, Principal, EndTransactionRequest) (TransactionView, error)
    Rollback(context.Context, Principal, EndTransactionRequest) (TransactionView, error)
}

type TransactionGateway interface {
    Preflight(context.Context, DMLPlanRequest) (SealedDMLPlan, error)
    Begin(context.Context, SealedDMLPlan, TransactionOptions) (TransactionCapability, error)
}

type TransactionCapability interface {
    Execute(context.Context, SealedDMLStatement) (WriteResult, error)
    Commit(context.Context) error
    Rollback(context.Context) error
    BackendIdentityHash() string
    Discard(context.Context) error
}
```

`SealedDMLPlan`/`SealedDMLStatement` 均为包私有不可序列化 proof，绑定 request nonce、raw SQL digest、plan ordinal、binder/catalog/policy digest；Execute 不接受 raw SQL。businessdb 的 raw runner 继续在 `internal` 包内，不得新增公开 `Exec(sql)` 给 mcpserver/pipeline。

### 9.4 MCP 工具与兼容字段

新增工具：

| 工具 | 关键输入 | 关键输出 |
|---|---|---|
| `open_session` | datasource_id、client metadata、default isolation、request_id | session_id、seq、deadlines、capabilities |
| `get_session` | session_id | state、active tx summary、next seq/deadline |
| `renew_session` | session_id、expected_seq、request_id | new idle deadline/seq |
| `close_session` | session_id、expected_seq、request_id | terminal state |
| `request_transaction_approval` | datasource_id、完整 plan、reason、request_id | approval_id、plan_digest、expires_at |
| `begin_transaction` | session_id、完整 plan、isolation、approval_id?、expected_seq、request_id | tx id、next operation、deadlines |
| `commit_transaction` | session_id、tx id、expected_seq、request_id | committed/unknown、audit correlation |
| `rollback_transaction` | 同上 + reason | rolled_back/unconfirmed |

现有 `execute_write` 只新增可选字段 `session_id`、`transaction_id`、`operation_id`、`expected_seq`、`request_id`。五者全缺时完全走旧单语句路径；一旦出现 session_id，就要求 transaction 字段完整并走 B5 coordinator。现有 `query` 可接受 session_id 只用于显式返回 `TX_SELECT_UNSUPPORTED`，不允许默默换连接执行。

所有 mutation 类 B5 工具必须有 request_id；建议 UUID，最大 64 bytes。返回统一包含 `session_id`、`transaction_id`、`sequence`、`state`、`retryable`、`audit_correlation_id`，不得返回 backend PID/thread id。

## 10. 事务级审计与 B6 衔接

### 10.1 模型与事件

为 `model.AuditLog` 及 SQLite/PostgreSQL store 增加可索引字段：

```text
transaction_id, transaction_seq, transaction_event,
transaction_outcome, operation_id, plan_digest
```

旧记录字段为空，迁移可回滚且不重写历史 hash。新增 event 类型：

| event | 何时耐久 | 关键字段 |
|---|---|---|
| `tx_plan_decision` | begin 成功/失败前 | 每项 decision/reason、plan/policy/binder digest |
| `tx_begin` | native Begin 后、向客户端返回前 | backend hash、isolation、deadlines、approval |
| `tx_statement` | 每条执行后、返回前 | ordinal、SQL/AST/auth digest、decision、affected rows、DB stage |
| `tx_commit_intent` | native Commit 前 | 全部 statement event digest、totals |
| `tx_commit_outcome` | Commit 返回后 | committed/failed/unknown |
| `tx_rollback` | rollback 尝试后 | trigger、confirmed/unconfirmed、last ordinal |
| `session_close` | 显式/超时/transport close | close reason、是否遗留 unknown |

`transaction_seq` 从 1 严格递增；全局 hash chain 上允许不同 transaction 事件交错，事务内完整性由 seq、plan digest、前一 event digest 验证。每个事件用稳定 EventUUID 做 B6 去重，逻辑重试不得生成第二个语义事件。

### 10.2 耐久屏障

- 直接复用 `internal/audit/group_commit.go` 的同步 Insert/AppendBatch、bounded queue、EventUUID 和 chain atomicity；调用成功才算通过屏障。
- 活动事务遇 `AUDIT_OVERLOADED`/`AUDIT_UNAVAILABLE` 不在持锁状态等待长重试，立即 rollback。B6 内部有界 backend retry 可以保留。
- 不把整个事务审计缓存到 commit 才写；每条 statement 先耐久，降低不可解释窗口。
- commit intent 是允许 DB commit 的必要条件，但不能让两个独立数据库变成原子提交。outcome 写失败必须按 unknown 处理并告警。
- 审计 detail 保存完整 authorization/binder/catalog digest 和截断后的结构化理由；raw SQL 仍遵循现有审计脱敏/加密策略，不因 session view 泄露。

### 10.3 审批存储扩展

现有 Approval 需增加 `kind=statement|transaction_plan`、`plan_digest`、`expires_at`、`consumed_at`、`consume_request_id`、`policy_digest`；另表保存有序 item digest，不保存第二份可变 authorization proof。`CreatePendingWithAudit` 的原子/补偿语义继续使用；消费必须与 begin lease 做可恢复 CAS。B6 hash chain 事件引用 approval audit id。

## 11. 资源隔离与长事务治理

### 11.1 三层 reservation

复用现有 `authorizedexecute.ReservationPool` 的 agent/tenant/datasource/global 维度，但不能把现有“每语句约 52 MiB”reservation 整段持有 60 秒。拆成：

1. **session lease**：小内存状态，不占连接；
2. **transaction/connection lease**：begin 到终态，占一个 datasource tx slot 和一个 pool connection；
3. **statement memory lease**：每条 parse/bind/execute/audit 临时获取，返回后释放。

建议默认值（全部可下调，不能超过编译硬上限）：

| 资源 | 默认 | 硬上限/派生规则 |
|---|---:|---|
| sessions / agent | 4 | 16 |
| sessions / tenant | 32 | 128 |
| sessions / instance | 256 | 1024 |
| active tx / agent | 1 | 4 |
| active tx / tenant | 8 | 32 |
| active tx / instance | 32 | 128 |
| active tx / datasource | `min(2, ConnLimit-1)` | 不得超过 `ConnLimit-1` |
| statements / tx | 16 | 32 |
| SQL bytes / statement | 256 KiB | 沿用现有 hard limit |
| total SQL bytes / plan | 1 MiB | hard |
| total affected rows / tx | 10,000 | policy/datasource 可更小 |
| idempotency entries / session | 64 | 128；按 byte budget 再限 |

`ConnLimit<=1` 时 B5 capability 为 unavailable，除非另配隔离 transaction pool；必须至少为现有 stateless 请求保留一条连接。quota 获取顺序固定为 session → transaction → datasource connection → statement memory，释放逆序，避免配额死锁。

### 11.2 背压与滥用防护

- 达限直接返回稳定、可重试的 `SESSION_LIMIT_EXCEEDED`/`TX_LIMIT_EXCEEDED`，不在 MCP handler 无界排队。
- 同一 session 并发请求不排队，返回 `SESSION_BUSY`；避免恶意请求延长 idle deadline。
- begin 全 plan parsing 有 CPU/token/AST/relation/catalog round-trip budget；失败不占连接。
- SQL comment、超长 reason、label、request id、plan item 都计 byte budget；canonical encoder 必须 length-frame，不能字符串拼接。
- 长事务 metrics：active age、idle age、locks wait、pool slots、rollback reason、unknown outcome。达到 50%/80% wall deadline发 warning/critical telemetry；硬截止自动回滚。
- admin force close 只调用同一 coordinator rollback，不允许直接 kill map entry；若 driver 卡死，再通过 discard/connection cancel 能力处置并审计。

## 12. 错误模型

MCP transport/auth/JSON-RPC 错误仍使用协议错误或 HTTP 401/413/429/405。工具已进入业务处理后，返回结构化 ToolResponse；只有执行/状态/系统错误设 `isError=true`，正常 policy deny/approve 延续现有 decision envelope。

| code | isError | retryable | tx effect | 含义 |
|---|---:|---:|---|---|
| `SESSION_NOT_FOUND_OR_DENIED` | true | false | 无/回滚已触发 | 不存在、owner 不符或已关闭，统一防枚举 |
| `SESSION_EXPIRED` | true | false | rollback | session lease 到期 |
| `SESSION_BUSY` | true | true | 保持 | 同 session 已有 operation |
| `SESSION_LIMIT_EXCEEDED` | true | true | 无 | 会话配额满 |
| `SESSION_WRONG_INSTANCE` | true | true | 未知实例无操作 | 需 sticky route 到 instance hint |
| `SESSION_DATASOURCE_MISMATCH` | true | false | rollback | 请求 datasource 不匹配 |
| `SESSION_RENEW_DURING_TX` | true | false | 保持 | 活动事务不能续期 |
| `TX_NOT_ACTIVE` | true | false | 无 | 无活动事务 |
| `TX_ALREADY_ACTIVE` | true | false | 保持 | 会话已有事务 |
| `TX_ID_MISMATCH` | true | false | rollback | tx id 不匹配，按攻击/客户端 bug 处理 |
| `TX_PLAN_REQUIRED` | true | false | 无 | begin 缺完整 plan |
| `TX_PLAN_MISMATCH` | true | false | rollback | 顺序、SQL、reason 或 operation id 不符 |
| `TX_PLAN_INCOMPLETE` | true | false | 保持 | 未执行完就 commit |
| `TX_ROLLBACK_ONLY` | true | false | rollback | 只能查询终态/rollback |
| `TX_APPROVAL_REQUIRED` | false | false | 不开 tx | decision=approve，返回 approval id |
| `TX_APPROVAL_EXPIRED` | true | false | 不开 tx | 审批过期/消费 |
| `TX_APPROVAL_PLAN_MISMATCH` | true | false | 不开 tx | 审批与计划不一致 |
| `TX_MASK_UNSUPPORTED` | false | false | 不开/rollback | 写事务不能用 mask 补权 |
| `TX_UNSUPPORTED_STATEMENT` | false | false | 不开/rollback | 超出 DML 子集 |
| `TX_CONTROL_STATEMENT_DENIED` | false | false | 不开/rollback | 用户 SQL 事务控制 |
| `TX_SELECT_UNSUPPORTED` | false | false | rollback | 活动写事务内 SELECT |
| `TX_IDLE_TIMEOUT` | true | false | rollback | idle hard deadline |
| `TX_MAX_DURATION` | true | false | rollback | wall deadline |
| `TX_STATEMENT_TIMEOUT` | true | false | rollback | statement deadline |
| `TX_LIMIT_EXCEEDED` | true | true/false | 不开/rollback | slot 可重试；硬预算不可重试 |
| `TX_COMMIT_OUTCOME_UNKNOWN` | true | false | terminal unknown | 禁止自动重试 |
| `TX_ROLLBACK_UNCONFIRMED` | true | false | close/discard | rollback 无法确认 |
| `IDEMPOTENCY_REQUIRED` | true | false | 无 | mutation 缺 request id |
| `IDEMPOTENCY_CONFLICT` | true | false | rollback | request id 绑定不同输入 |
| `AUTH_DML_BINDER_REQUIRED` | false | false | 不开/rollback | 方言 binder/capability 不完整 |
| `AUTH_DML_ACTION_MISSING` | false | false | 不开/rollback | relation action grant 缺失 |
| `AUTH_DML_COLUMN_GRANT_MISSING` | false | false | 不开/rollback | write/reference grant 缺失 |
| `AUTH_CATALOG_RACE` | false | true（新事务） | rollback | catalog identity 改变 |
| `AUDIT_OVERLOADED` | true | true（新事务） | rollback | B6 队列/等待者过载 |
| `AUDIT_UNAVAILABLE` | true | true（新事务） | rollback | commit 前审计不可用 |

外部 response 不含原始 driver 文本、对象存在差异或 owner 细节；内部审计保存稳定 stage/classification。policy deny 即使可修改 SQL重试，也不能标 `retryable=true` 诱导自动重放写操作。

## 13. 控制台、管理 API、文档与运维

### 13.1 控制台必须有只读视图，并提供受控终止

B5 GA 需要管理员“Sessions / Transactions”页，否则长事务只能靠日志猜测。列表字段：session/tx 短 ID、Agent、datasource、instance、state、age/idle、next ordinal/总数、affected rows、deadline、approval/audit link、backend identity hash（仅超管）。默认不展示 raw SQL，只展示 statement type、对象摘要和 digest。

管理 API：

- `GET /api/v1/sessions`、`GET /api/v1/sessions/{id}`；
- `POST /api/v1/sessions/{id}/rollback`、`POST /api/v1/sessions/{id}/close`；
- 聚合多实例状态时必须标 instance/staleness，不能把抓不到的实例显示为“无事务”；
- 强制动作需要 admin RBAC、reason、request id 和独立审计。

### 13.2 配置、metrics 与告警

建议配置树 `mcp.sessions` 与 `mcp.transactions`，只允许小于硬上限；非法值启动失败，不自动扩大。feature flags 分方言：`b5_sessions`、`b5_tx_postgres`、`b5_tx_mysql`，默认 off。

至少导出：

- `agentsql_mcp_sessions{state,transport}`；
- `agentsql_mcp_transactions{state,dialect}`；
- begin/statement/commit/rollback latency；
- timeout/forced rollback/rollback unconfirmed/commit unknown counters；
- quota reject、session busy、plan mismatch、approval wait；
- pinned connections、pool headroom、transaction age histogram；
- transaction audit barrier latency/overload。

`commit_unknown>0`、`rollback_unconfirmed>0`、接近 datasource slot、审计 barrier error 必须告警并有 runbook。日志只记录 ID/digest/stage，不记录 API key、credential、完整 SQL/参数。

### 13.3 文档变更

- `docs/INTEGRATIONS.md`：新增 MCP 事务工作流、显式 session 工具、HTTP 无状态说明、stdio EOF、幂等和 unknown outcome；继续强调每 call 单 SQL。
- `docs/SPEC.md`：新增 B5 capability matrix、状态机、planned-only、DML 子集、SLO/limits、B2/B6 依赖。
- MCP 相关 README/示例：旧七工具示例保持；另给 open → begin(plan) → execute×N → commit 与 approval-before-begin 示例。
- 用户/部署文档：连接池 headroom、sticky routing、graceful shutdown、timeouts、feature flags、PG extension ABI、MySQL release gate、故障对账。
- 安全文档：session id 不是认证、禁止用户 tx control/session state、policy revocation 最长 60 秒边界。
- API/OpenAPI/前端文案：明确“关闭活动 session 会回滚”，commit unknown 不得重试。

## 14. 切片计划与验收门

每个 slice 独立 feature-off 合入；未满足出口条件不得打开下一个生产能力。

### S1：合同、状态机、schema 与 capability（Platform + Security）

- 冻结工具 JSON schema、错误码、状态转换表、canonical plan/event encoder、默认/硬限额。
- 加 audit/approval migration 与向后兼容读写；新增 capability probe，所有 B5 flags 默认 off。
- 给 HTTP stateless、stdio、协议版本建立 contract golden；单语句七工具完全不变。
- 出口：状态模型 exhaustive test、迁移 upgrade/rollback、owner/instance 防枚举、协议负责人和安全负责人签收。

### S2：逻辑会话、配额、幂等与清理（Runtime/SRE）

- 实现 SessionManager、sharded registry、lease/sweeper/tombstone、stdio scope close、graceful shutdown。
- 接入 session/tx/connection/statement reservation；不接业务 SQL。
- 出口：fake clock timeout、1000 并发 race、相同/冲突 request id、shutdown/EOF、内存/slot 泄漏测试通过。

### S3：单 backend 原生多语句 transaction capability（DB Core）

- 在 authorizedexecute 内新增不可序列化 TransactionCapability；重构 businessdb connection/tx 生命周期。
- PG/MySQL 实现 begin/execute sealed placeholder/commit/rollback/discard、backend identity、cancel、timeout；仍由测试授权器喂 sealed object。
- 出口：同 backend 证明、连接归还/销毁证明、commit/rollback fault injection、现有单语句行为零回归。

### S4：DML 精确授权与 binder（Security + PG/MySQL owners）

- 增 DML relation action/column grants、usage authorizer、控制面管理/API。
- PG 发布并 attestate 新 DML binder ABI，覆盖 14–18。
- MySQL 完成封闭 AST binder、catalog fingerprint/MDL proof，覆盖 8.0/8.4；若不满足则 MySQL flag 留 off。
- 出口：支持/拒绝矩阵 golden、catalog race/隐式对象攻击测试、缺权限 fail-closed、独立安全评审。

### S5：planned coordinator、审批与事务审计（Pipeline + Audit/B6）

- 实现全 plan 预检、approval ticket/CAS、逐项匹配、快照语义、rollback-only、commit intent/outcome。
- 接 B6 同步 group commit/EventUUID，添加 tx id/seq 查询和对账任务。
- 出口：每个状态/错误的审计完整性属性测试；audit overload/outage、approval expiry、commit unknown 故障注入通过。

### S6：MCP 工具、协议协商与向后兼容（MCP owner）

- 注册八个新工具，扩展 execute_write 可选字段；initialize capability、stdio/HTTP cleanup 与 response envelope。
- 保持 HTTP `Stateless:true`；验证 2025-06-18，其他版本逐个 qualify。
- 出口：旧客户端 golden byte/semantic compatibility、MCP inspector/conformance、4 MiB body/line cap、取消与并发测试。

### S7：控制台、管理 API、文档与运维（Frontend + Docs + SRE）

- live session/tx 视图、强制 rollback/close、metrics/alerts/runbook、全部文档变更。
- 出口：RBAC、敏感字段/SQL 泄露检查、多实例 staleness、无障碍/危险操作确认、文档示例 e2e。

### S8：真库 e2e、灰度与 GA（QA + Release）

- 完成第 15 节矩阵；feature-off → internal agents → 单 datasource canary → 方言分批开启。
- 灰度观测 pool headroom、p95 tx age、forced rollback、audit latency、unknown outcome；任一 unknown/unconfirmed 有未闭环根因即停止扩大。
- 出口：所有负责人签收、灾难演练/回滚演练、兼容报告、已知边界写入 release note。

## 15. 测试矩阵

### 15.1 层级矩阵

| 层 | 必测项 |
|---|---|
| unit | 状态机全转换、deadline/fake clock、plan/event canonical digest、single-statement splitter、owner/CAS/idempotency、quota |
| property/fuzz | SQL 分号/comment/dollar quote/Unicode、JSON schema、plan reorder/duplicate、event seq、并发 close/commit/timeout |
| component | coordinator + fake DB/audit/store；每个 failpoint 前后断电/取消；无 raw executor 逃逸 |
| store | SQLite + PostgreSQL migrations、approval consume CAS、audit chain/event UUID、group commit overload/unknown |
| MCP | stdio frame 与 HTTP POST；initialize/tools/list/tools/call/cancel；旧七工具兼容；unsupported protocol |
| real DB | PG 与 MySQL 版本/方言、真实锁/超时/cancel/commit/rollback/catalog race |
| UI/API | RBAC、分页、instance stale、force rollback、敏感字段、审计跳转 |

### 15.2 真库版本与部署组合

| 维度 | PR blocking | nightly/release blocking |
|---|---|---|
| PostgreSQL business DB | 14、18 | 14/15/16/17/18，各自 binder build/hash |
| MySQL business DB | 8.0 最新 patch、8.4 LTS | 全部产品声明 patch，大小写模式 0/1 |
| control/audit store | SQLite；PG 最新 | SQLite、PG 最低/最新；combined 与 separate store |
| MCP transport | stdio、stateless HTTP | 两者 + reverse proxy sticky/misroute/shutdown |
| protocol | 2025-06-18 | 所有产品 allowlist 版本；未知版本拒绝 |

MySQL 任一 release-gate 证明失败时，测试期望应是 capability unsupported，而不是跳过用例或转表级授权。

### 15.3 端到端场景

功能主线：

1. open → begin 两条 DML → execute×2 → commit；验证同 backend、数据原子可见、tx audit seq 完整。
2. 第二条 constraint error、policy deny、affected-row 超限、审计失败分别导致第一条也 rollback。
3. 显式 rollback（执行前/执行一半/全部执行后）均无业务提交，重复 rollback 返回同一终态。
4. warn + allow 可提交且各自审计；mask/approve/deny 混合遵循第 5.5 节。
5. approval 完整计划获批后一次性消费；改 SQL/顺序/reason/datasource/agent/policy digest、过期、重复消费全部拒绝。

传输与故障：

6. stdio EOF 在 statement 前、执行中、两请求间、commit 前分别强制清理。
7. HTTP 客户端在两请求间断开不立即回滚；在 statement 执行中取消则 rollback；idle 到期后再调用得到 tombstone 终态。
8. 请求在 commit 发出前取消、DB commit 后响应丢失、audit outcome store 失败分别验证明确 rollback 或 unknown，绝不自动重放。
9. rollback 网络故障、DB restart/failover、server SIGTERM/crash、pool close；连接不能回池，未提交数据由真库验证回滚。
10. 两个并发 execute、execute 与 close、commit 与 idle sweeper、相同/冲突 request id；race detector 和最终状态唯一。

恶意与边界：

11. `BEGIN; UPDATE`、注释隐藏分号、PG dollar quote、MySQL version comment、multiStatements DSN、COPY/LOAD、PREPARE/EXECUTE、SET、SAVEPOINT、DDL 全部 fail-closed。
12. stolen session/tx id、换 Agent/API key/datasource/instance、key rotation、disabled Agent、错误 expected_seq；不泄露对象存在性。
13. INSERT 无列清单、RETURNING、CTE、subquery、upsert、UPDATE FROM/DELETE USING、trigger/default/generated/FK cascade/view/partition/UDF 均按 capability 矩阵拒绝。
14. PG extension 缺失/ABI/hash/major 不符、prepared invalidation/replan/backend PID 改变、catalog DDL race；全部 rollback。
15. MySQL sql_mode/lctn/collation/server UUID 改变、metadata 权限缺失、MDL wait/DDL race、deadlock；全部 rollback 或 capability unsupported。
16. 1,025 sessions、quota 热点、32 条/1 MiB plan、超长 label/reason/request id、audit 4,096 queue 压力；有界拒绝，无内存/连接泄漏。
17. B2 SELECT 在事务外照常 mask/seal；活动 B5 tx 中 query/RETURNING 触发 rollback；B2/B5 capability 不混淆。
18. B6 多 transaction group commit 交错，按 transaction seq 可重建；EventUUID 重试不产生重复，hash chain verify 通过。

### 15.4 必须证明的负属性

- 仓库扫描证明 mcpserver/pipeline/adminapi 没有 raw `*sql.DB`/pgx connection/Exec 入口。
- 任何成功 commit 都有先于它耐久的 begin、全部 statement、commit intent；没有缺序号或重复 ordinal。
- 任何非 committed 事务对真库无部分 DML 可见（commit unknown 除外，必须归为待对账而非断言 rollback）。
- READY/closed session 不占 pool connection；每个 active tx 恰占一条。
- stacked SQL、用户 tx control、session variable、prepared state 无旁路。
- 旧 MCP client 不传任何新字段时 response decision/shape 和数据库语义不变。

## 16. 负责人签收清单

下列不是“知会”，而是对应 flag 开启前必须留档的 sign-off：

| 负责人 | 必须签收的边界/证据 |
|---|---|
| 产品/架构 owner | planned-only；每 call 单 SQL；v1 不支持 tx 内 SELECT/DDL/savepoint；多实例不迁移 |
| Security/Authorization owner | DML action + write/reference grant；approval 不补结构权限；60 秒普通 policy 撤销窗口；hard revocation 行为 |
| MCP/protocol owner | HTTP 保持 stateless；显式 handle 不等同 MCP session；2025-06-18 allowlist；sampling/notification 非依赖 |
| Pipeline owner | snapshot、混合决策、rollback-only、现有 Process 不回归、唯一执行 choke point |
| PostgreSQL owner | 新 DML binder ABI、PG14–18、同 tx sealed execution、catalog/implicit object 矩阵 |
| MySQL owner | 8.0/8.4 binder + MDL/fingerprint 证明；不能证明即 feature-off，不降级 |
| Audit/B6 owner | tx id/seq schema、barrier、EventUUID、chain interleaving、commit outcome unknown 对账 |
| Control store/Approval owner | plan approval TTL/single-use/CAS、迁移、combined/separate store 失败语义 |
| Runtime/SRE owner | quota 默认、pool 保留、idle/wall deadline、sticky route、shutdown/kill/unknown runbook |
| Frontend owner | session/tx 视图、RBAC、force rollback、raw SQL/敏感字段默认不展示 |
| Docs/Integrations owner | single SQL、显式 lifecycle、错误重试规则、部署前置、B2/B5 capability 分离 |
| QA/Release owner | 第 15 节真库/传输/故障/恶意矩阵和灰度停止条件 |

### 16.1 最需要负责人拍板的五项

1. **planned-only 是否接受。** 若产品坚持 ad-hoc BEGIN 后再决定 SQL，必须接受“遇 approve 即回滚重开”，且仍不能在事务内等人；不能删除 plan 安全门后直接发布。
2. **策略撤销语义。** 本设计选择 begin 快照 + hard revocation epoch，普通 policy 最迟 60 秒生效；零延迟需要新全局 lease/fence 基础设施。
3. **DML 列授权发布门。** PG 需要 DB extension 新 ABI；MySQL 需要受限 binder/MDL 证明。是否允许先 PG GA、MySQL feature-off，需产品/DB owner 明确。
4. **事务内 SELECT。** 本设计为保护 B2 seal 不变量而拒绝并 rollback；若业务场景必须 read-your-writes，应拆出独立后续设计。
5. **commit outcome unknown。** 两个数据库间没有原子提交；产品、审计、SRE 必须接受“不返回成功、不自动重试、靠 intent 对账”的诚实语义，或另立 XA/outbox 项目。

## 17. 已知风险、限制与缓解

| 风险/边界 | 后果 | 缓解/是否阻断 GA |
|---|---|---|
| 长事务占 pool/锁 | stateless 请求饥饿、DB lock contention | `ConnLimit-1` 保留、slot、15s idle/60s wall；超限阻断 |
| 实例 crash 位于 commit 窗口 | 客户端无法得知是否提交 | durable intent、unknown 对账、幂等业务 key；已知不可消除边界 |
| policy begin 后改变 | 最长 60s 仍按旧快照 | hard revocation、短 wall、UI 显示；需安全签收 |
| PG extension 运维成本 | 每 major/patch ABI 管理 | capability hash、feature-off、滚动部署手册；PG GA 前阻断 |
| MySQL metadata/implicit write 复杂 | 错误列授权/TOCTOU | 极小语法子集、存在即拒绝、MDL 真库证明；失败则方言不上线 |
| 审计 store 抖动 | 事务频繁 rollback | B6 bounded group commit、容量告警；不降低 fail-closed |
| session id 泄露 | 攻击者尝试劫持 | 每请求 Bearer + owner/key revision/instance 绑定、防枚举；ID 不作凭据 |
| HTTP 无 transport close | 遗留 idle tx 到 TTL | 15s idle sweeper、显式 close、控制台 force rollback |
| 不支持 tx 内 SELECT | 部分工作流需拆分 | 明确错误；事务外 B2 query；后续独立设计 |
| 不支持 session vars/prepared | ORM/驱动透明代理不可用 | B5 定位 MCP tool workflow，不伪装数据库协议代理 |
| 控制台多实例聚合不全 | 运维误判 | instance/staleness 显示、不能把 unreachable 当 empty |

## 18. 评审结论模板

B5 可以进入实现的必要条件：第 0 节裁决无未决冲突；第 16 节对应 owner 已签收；S1 合同与迁移先落地且 feature-off。B5 可以按方言进入 GA 的必要条件：该方言 S4 binder/capability 完整、S8 对应真库矩阵全部通过、审计/回滚故障演练完成、控制台与 runbook 可用。

在上述条件前，仓库对外事实仍是：**MCP 单请求单语句、HTTP 无状态、没有跨请求事务。** 不得仅因 businessdb 已有 Session/WriteTx 原语，就在文档、UI 或 release note 宣称 B5 已实现。

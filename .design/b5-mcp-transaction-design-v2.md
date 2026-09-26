# AgentSQL v0.4 B5：MCP 多语句事务与跨请求会话——详细设计 v2

状态：**P0 修订评审稿；所有生产能力仍 feature-off**  
日期：2026-09-25（Asia/Shanghai）  
代码基线：`feature/v0.4`，`HEAD=9cb14511674ff3dd580bdf710941085fab63bdc9`  
输入：`b5-mcp-transaction-design-v1.md`、`b5-v1-review.md`、`b5-s0-findings.md` 与 `.design/s0-b5/` 探针。  
约束：本文只定义设计；不代表实现完成或生产放行。

## 0. 发布裁决与 P0 关闭表

B5 GA 采用 **PostgreSQL-first**。首发仍是 planned-only、每个 MCP call 一条 SQL 的短写事务；HTTP 仍无状态。`b5_sessions`、`b5_tx_postgres`、`b5_tx_mysql` 均默认 off。PostgreSQL 只有在新 DML binder ABI、隐式对象拒绝闭包、typed terminal、共享 admission、审计 WAL 和真库矩阵全部通过后才能逐 datasource 开启。

**MySQL 对 v0.4 GA 默认 off，并对外报告 `unsupported`。** 在 DML binder/MDL closure、服务端 timeout attestation、真实单向网络分区、终结 connector 四类 release gate 全部通过前，不允许通过配置、降级授权或“实验模式”绕过这一结论。

| P0 | v2 关闭方式 | S0 依据 | 关闭状态 |
|---|---|---|---|
| P0-1 digest 循环 | `plan_digest` 永不包含 approval id；审批只绑定该 digest；消费后另算 `begin_authorization_digest` | 设计裁决 | **设计关闭**，实现由 S1/S6 验证 |
| P0-2 handle/路由 | 256-bit continuation secret + HMAC proof、强一致共享目录、owner epoch fencing、目录粘性路由；未认证或 owner 不匹配不触碰事务 | 四进程共享目录 20 个 session 仅 20 行且 owner 唯一 | **设计关闭**，生产 HA/故障恢复由 S2/S3 验证 |
| P0-3 terminal outcome | DB outcome、连接处置、驱动证据三轴 typed 返回；禁止解析错误文本 | MySQL 四个报文窗口可区分 `NOT_COMMITTED/UNKNOWN`，连接 ID 变化证明 discard | **设计关闭**，驱动实现由 S4 验证 |
| P0-4 MySQL 有界终结 | retained socket、150ms watchdog、deadline + Close + `driver.ErrBadConn`；ACK 丢失为 unknown；完整 gate 前 feature-off | 真实 MySQL 8.4.11 在约 150–169ms 返回并换物理连接 | **范围关闭**：v0.4 GA 不含 MySQL |
| P0-5 MCP 取消/版本 | SDK 前产品 allowlist；legacy in-flight 300ms 独立 watchdog；idle/wall watchdog 不依赖 request context | 2025-06-18/2025-11-25 断连不取消，2026-07-28 才取消；未知 initialize 可被 SDK 静默降级 | **设计关闭**，协议 conformance 由 S2/S9 验证 |
| P0-6 审计边界 | `COMMITTED`、`COMMITTED_AUDIT_PENDING`、`DB_OUTCOME_UNKNOWN` 分离；独立 emergency WAL + 幂等 reconciliation；锁图和 250ms 等待预算 | WAL 约 120B/事件、fsync 平均 1.532ms；重复重放仍一条 | **设计关闭**，durability/SRE gate 由 S7 验证 |
| P0-7 quota/tombstone | 所有 admission 与 DB 连接预算集群化；active plan 按字节租约；tombstone 只保留 digest 并限条数/字节/churn | 本地 limit=10 被四实例放大为 40；共享计数精确限 10/8MiB/6 connection；compact tombstone 约 131B/项 | **设计关闭**，性能与 crash reclaim 由 S3 验证 |

“设计关闭”只表示合同不再自相矛盾，不表示 release gate 已通过。

## 1. GA 范围和不支持范围

### 1.1 首发允许

- 显式 AgentSQL logical session；一会话最多一个 active transaction。
- 完整计划先提交、审批先完成，再占用业务连接。
- PostgreSQL 14–18 上经正式 DML ABI 封印的极小 `INSERT/UPDATE/DELETE` 子集。
- 每个 operation 仍只有一条顶层 SQL；多条操作按计划跨 MCP 请求依次执行。
- 同一 backend 上原生 transaction、顺序执行、commit 或整笔 rollback。
- 事务外的 B2 `query`/SELECT 维持现有路径和 delivery seal。

### 1.2 首发固定拒绝

- stacked SQL、SQL script，以及客户端提交的 `BEGIN/START/COMMIT/ROLLBACK/SAVEPOINT/SET TRANSACTION`。
- active B5 transaction 内的 SELECT；返回 `TX_SELECT_UNSUPPORTED` 并使事务 rollback-only。B2 SELECT 只在事务外运行。
- 对外 DML `RETURNING`。S0 已证明 PG14/18 同 backend `DML ... RETURNING` 技术可行，但它违反当前 B2 数据交付边界；gateway 仍返回 `TX_RETURNING_UNSUPPORTED`。
- DDL、ADMIN、MERGE、UPSERT、CTE、子查询、`UPDATE ... FROM`、`DELETE ... USING`、COPY/LOAD、CALL/DO。
- trigger、rule、RLS、view、partition/inheritance、FK/cascade、用户函数、非平凡 default、identity/sequence、generated expression 等未闭合隐式对象。
- session/system/user variable、临时对象、advisory lock、cursor/portal、LISTEN/NOTIFY。
- 客户端或服务端命名 prepared statement；B5 内部 sealed prepared capability 不属于用户可见 prepared 功能。
- 跨 datasource、跨实例迁移、XA/2PC、事务恢复和自动重试 commit。

### 1.3 v1 到 v2 的范围变化

1. 从“裸 session id + API key”升级为 continuation capability proof；共享目录和 owner fencing 成为前置基础设施。
2. 从进程内 quota/sticky 假设升级为集群 admission、全局 DB 连接 token 和 byte/churn budget。
3. terminal 接口从 `error` 改成证据驱动的 typed result。
4. 审计从二义的 `commit_outcome_unknown` 拆成 DB outcome 与 audit durability 两个正交轴，并强制 emergency WAL。
5. B5 的 audit/lock/fence 等待上限改为 250ms 专用预算，不继承 B6 当前 2s/5s 默认值。
6. PostgreSQL DML ABI 已有 S0 可行性证据，但首发 SQL 形态比技术能力更窄；MySQL 从“可并行争取 GA”收缩为默认 off/unsupported。
7. MCP 不再相信 SDK 协商或断连传播；产品前置 allowlist 和独立 watchdog 是安全边界。

## 2. 架构与不可破坏的不变量

```text
MCP pre-SDK protocol gate / per-request authentication
                 │
                 ▼
shared session directory ── owner epoch / route / cluster admission
                 │                 │
                 │        wrong owner: return only, no cleanup
                 ▼
owner SessionManager ── continuation HMAC / seq / idempotency
                 │
                 ▼
planned TransactionCoordinator
      │ control snapshot + approval CAS（释放 control DB 锁）
      ▼
sealed DML plan / PostgreSQL binder ABI
      ▼
one native business transaction on one physical backend
      │
      ├── bounded append to audit-chain terminal sink
      └── typed commit/rollback + release/discard
                 │ business resource released
                 ▼
final control fence / response delivery
```

必须始终成立：

1. MCP、console、approval、session directory 都拿不到 raw DB executor；SQL 只经 sealed `TransactionCapability` 执行。
2. `session_id`、`transaction_id`、route hint 都是标识符，不是授权；只有当前 principal 加有效 continuation proof 才能操作 session。
3. 未认证、proof 无效、owner epoch 不符或落到错误实例的请求，不得 cancel、rollback、close、touch lease 或改变受害事务任何状态。
4. 一会话最多一个事务、一个事务同一时刻最多一个 operation、一个事务只绑定一个 backend。
5. control snapshot/approval/admission 的数据库锁必须在 native business BEGIN 前释放；active business tx 不同步回调 control store。
6. audit-chain 是 terminal sink：它在持有 chain 锁、audit transaction 或 coordinator mutex 时，不得调用 business、directory、approval、admission 或 final fence。
7. final control fence 只在 business terminal 调用返回且物理连接已 release/discard 后开始。
8. 所有 plan、proof、event 编码采用版本化、length-framed canonical encoder；不得用字符串拼接。
9. 所有终结判断来自驱动/connector 阶段证据；driver 错误文本只可诊断，绝不参与状态分类。
10. 无法证明 owner、closure、catalog、terminal phase、audit durability 或 connection disposition 时 fail closed。

## 3. P0-1：稳定 plan digest 与 begin authorization digest

### 3.1 两个 digest 的职责

`plan_digest` 表示“要执行什么、由谁、在哪个授权/结构快照下执行”。它在请求审批前即可确定，且**禁止**包含 `approval_id`、审批状态、消费时间、transaction id、session id、owner instance 或随机 lease id。

canonical payload 为 `agentsql.b5.plan.v2`：

| 分组 | 字段 |
|---|---|
| principal | tenant id、agent id、API-key revision（不含 key）、agent level/profile digest |
| datasource | datasource id/revision、dialect、server major、isolation、read-only=false |
| authorization | policy revision/digest、DML grant digest、binder ABI/hash、closure-policy version |
| budget | statement count、plan bytes、max total affected rows、每项 max affected rows |
| ordered item | ordinal、operation id、tool、raw SQL SHA-256、normalized analyzed-tree digest、reason digest、action、manifest digest |

```text
plan_payload = CanonicalEncode("agentsql.b5.plan.v2", fields_above)
plan_digest  = SHA-256(plan_payload)
```

同一字段缺失和空值使用不同 type tag；array 保持 ordinal 顺序；map key 使用规范序；整数固定无符号宽度；字符串先做 UTF-8 合法性检查但不做会改变 SQL 的 Unicode 正规化。raw SQL、reason 原文不出现在 digest 展示或通用日志中。

`begin_authorization_digest` 表示“一次具体 begin 被什么授权”。它只在审批 CAS/重校验完成后计算：

| 字段 | 说明 |
|---|---|
| schema | `agentsql.b5.begin-auth.v1` |
| plan_digest | 上述稳定 digest |
| approval binding | `approval_id`、approval decision digest、批准人/机制 digest、批准上限、expiry |
| consumption | consume generation、consume request id、CAS nonce、consumed_at |
| current authority | begin 时 hard-revocation epoch、API-key revision、owner epoch |
| execution binding | transaction id、backend-independent begin nonce、admission lease ids 的 digest |

```text
begin_authorization_digest = SHA-256(CanonicalEncode("agentsql.b5.begin-auth.v1", fields))
```

无须人工审批时，approval binding 使用 typed value `approval=not_required`，不能使用空字符串。

### 3.2 无循环计算流程

1. bounded parse/bind 全部 plan item，产生 manifest、policy/binder snapshot 和 `plan_digest`。
2. `request_transaction_approval` 创建记录 `{approval_id, approved_plan_digest, bounds, expires_at}`；审批人只签 `approved_plan_digest + bounds`。
3. `begin_transaction` 从 raw input 独立重算 `plan_digest`；不接收客户端声称的 digest 作为事实。
4. 若需要审批，按 `approval_id` 读取并常量时间比较 `approved_plan_digest`；验证 owner、范围、expiry、未消费状态。
5. control store 以 CAS 原子消费 approval，生成 consume generation/nonce；失败则不占 DB 连接。
6. 再检查 hard-revocation epoch、owner epoch 与 cluster admission；计算 `begin_authorization_digest`。
7. 业务连接上的 binder 再绑定一次；结果必须仍匹配 plan manifest/digest，随后才 native BEGIN。
8. begin 后任何审计事件引用两个 digest；approval id 只出现在 begin authorization/event 中，不回写 plan。

CAS 消费后 begin 失败，approval 终态为 `consumed_begin_failed`，不得复用。任一字段变化都创建新 plan/approval，而不是修改旧记录。

## 4. P0-2：敏感 session capability、owner epoch 与粘性路由

### 4.1 handle 与 continuation proof

`open_session` 成功只返回一次：

```text
session_id           = as_sess_<128-bit random>
continuation_secret  = base64url(256-bit CSPRNG)       // 仅本次响应可见
owner_epoch          = uint64
route_hint           = opaque signed instance hint
```

共享目录不存明文 secret；存 KMS/节点密钥加密的 per-session HMAC key、key id 和 verifier digest。secret/key 不写日志、审计、tombstone、metrics 或 status API。客户端遗失 secret 只能关闭/放弃 session 并新开；没有“按 session id 找回”。

除 `open_session` 外，每个 B5 call 都要求 `request_id` 和：

```text
proof_input = CanonicalEncode(
  "agentsql.b5.continuation.v1",
  method, session_id, owner_epoch, request_id, expected_seq, SHA-256(canonical_body_without_proof)
)
continuation_proof = HMAC-SHA-256(Ksession, proof_input)
```

验证使用常量时间比较。`request_id + body_digest` 进入 bounded idempotency cache；同 id 同 body 可返回同一结果，同 id 不同 body 返回 `IDEMPOTENCY_CONFLICT`。proof 本身不替代每请求 Bearer/API key 认证，principal、tenant、Agent、key revision 任一不符均统一返回 `SESSION_NOT_FOUND_OR_DENIED`。

### 4.2 共享强一致目录

v2 选择 S0 已验证的**共享强一致目录**，不把一致性 hash 单独当所有权事实。逻辑 schema：

```text
session_directory(
  session_id PK, principal_digest, datasource_id,
  owner_instance_id, owner_epoch, owner_lease_deadline,
  state_hint, route_key, encrypted_hmac_key, key_id,
  created_at, absolute_deadline, terminal_digest, version
)
```

- open 以唯一键 INSERT + cluster admission CAS 创建 owner；同 session id 不可能出现两 owner。
- owner 每次本地 mutation 先验证目录中的 `(instance_id, owner_epoch, lease)` fencing token，再取得本地 operation lease。
- owner heartbeat 与业务执行没有同步等待关系，不读取 business 状态、不持 business lock；失败不会让请求转去第二 owner，而是停止接收新 operation 并启动本地有界 cleanup。
- active transaction 永不迁移。owner crash/lease 丢失后目录将其标为 `owner_lost`；DB 断连接受服务端回滚，但若 crash 位于 COMMIT 窗口则终态仍可能 unknown。其他实例只能提供 tombstone/status，不能接管该 tx。
- READY session 也不透明迁移；客户端新开 session。该约束避免 secret/seq/幂等状态的分裂脑。
- 实例重启必须使用新 `instance_incarnation`；即使 instance name 相同，旧 epoch 也不能复活。

S0 四进程对同 20 个 id 并发 `INSERT ... ON CONFLICT` 得到 20 行、0 重复 owner，证明目录原子创建可行；尚未证明热点、跨 AZ 故障恢复和生产 SLO，这些是 S3 gate。

### 4.3 路由与“不得伤害受害事务”

目录感知 L7 router 在完成 API 认证和 bounded envelope/proof 校验后读取 owner，按 `owner_instance_id + instance_incarnation` 粘性转发。route hint 只作缓存优化，必须有集群签名且最终仍以目录为准。目录不可用时 fail closed，不随机散发请求。

处理顺序固定：

1. 协议 allowlist、body limit、API authentication；
2. 目录 lookup、continuation proof、principal/key revision；
3. owner instance/epoch 校验；
4. 只有实际 owner 才进入 SessionManager 和 coordinator。

步骤 1–3 失败的路径没有 TransactionCapability 引用，因此技术上不能调用 rollback。错误实例返回 `SESSION_WRONG_INSTANCE`（有效 proof 时可附 opaque route hint）；无效 proof 返回 `SESSION_NOT_FOUND_OR_DENIED`。owner mismatch、stale epoch、猜测 tx id、无效 API key都不得 touch idle deadline。只有“已认证 + proof 有效 + 当前 owner epoch”请求触发的 plan mismatch、hard revocation、timeout 或显式 close 才可 rollback。

### 4.4 session lifecycle

- `open_session` 固定 principal、tenant、API-key revision、datasource、dialect、transport scope、owner epoch 和 absolute deadline；空闲 READY session 不取业务连接。
- `renew_session` 只允许 READY；active tx 返回 `SESSION_RENEW_DURING_TX`，不能延长 tx idle/wall deadline。
- 经完整认证/proof/owner 校验的 `close_session`：READY 直接 terminal；ACTIVE 调同一 coordinator rollback，再关闭。不得删除 map entry代替 rollback。
- Agent/key/datasource hard disable 由无阻塞 revocation epoch 通知 owner；owner用本地 epoch比较触发 rollback，不能在持 business tx 时查询 control DB。
- stdio scope EOF 和 graceful shutdown 顺序固定为：停止 open/begin → cancel active operation → typed rollback → discard 未确认连接 → fsync pending WAL → business release → final fence/目录 terminal hint。
- HTTP POST 正常结束不会关闭跨请求 session；in-flight watchdog、tx idle/wall deadline 承担遗留清理。
- closed/expired session只留下第 11.4 节 compact tombstone；secret和完整 idempotency body立即清除。

## 5. P0-5：SDK 前协议 allowlist 与取消语义

### 5.1 产品级协议门

协议门位于 `NewStreamableHTTPHandler`/SDK 之前，先以 4MiB hard cap 缓冲并检查 header、initialize body 和请求 `_meta`，再重放给 SDK。首发唯一 allowlist 是 `2025-06-18`。header/body/meta 冲突、缺少可确定版本、未知 legacy、未知 future 一律 HTTP 400 + 稳定 `MCP_PROTOCOL_UNSUPPORTED`，SDK 不得替产品降级。

S0 证明：无 header 且 initialize body=`2025-07-01` 时，SDK v1.7.0 会 HTTP 200 并静默协商到 `2025-11-25`。因此“SDK 支持/协商成功”永远不能作为产品 allowlist 判据。

### 5.2 各版本断连/取消合同

| 协议版本 | S0 的 HTTP 断连行为 | v2 产品状态 | B5 cleanup 合同 |
|---|---|---|---|
| `2025-06-18` | 700ms 内 handler context **不取消** | 首发 allow | in-flight B5 call 使用独立 300ms operation watchdog；请求间只由 tx idle/wall watchdog 清理 |
| `2025-11-25` | 700ms 内 handler context **不取消** | 未 qualify，拒绝 | 加入 allowlist 后也必须沿用 300ms watchdog，不能依赖 SDK flag |
| `2026-07-28` | context 在本机小于 1ms 取消 | 未 qualify，拒绝 | 将 context cancel 当加速信号；独立 watchdog 仍是正确性边界 |
| 其他/无确定版本 | SDK 行为不构成承诺 | 拒绝 | 不创建或操作 session |

对 legacy HTTP，无法观察“两次 POST 之间的 TCP 断开”，因此不得承诺“断连立即 rollback”。准确承诺是：

- **in-flight transaction tool call**：从 handler 进入 coordinator 起启动与 request context 无关的 300ms hard watchdog；到期 cancel statement，并用独立 cleanup context rollback。要支持超过 300ms 的 DML，必须先升级并 qualify 可传播取消的协议/transport，本版本不放宽。
- **请求之间**：active tx idle 默认 15s、wall 默认 60s；到期回滚。HTTP 本来没有持续连接事件，文案只能承诺 deadline 内清理。
- stdio EOF、server shutdown：独立 cleanup context 立即触发，仍受 terminal watchdog 限制。

S0 的 PG14 实测在 `2025-06-18` request context 未取消时，300ms watchdog 触发，rollback 约 1ms，断连后约 305ms 验证业务行数为 0。这证明机制可行，不代表任意网络/数据库环境都恰为 305ms。

## 6. 事务状态机与 P0-3 typed terminal outcome

### 6.1 状态机

```text
READY --begin--> ACTIVE --all items--> COMMITTING --terminal--> READY/TOMBSTONE
                  │   │                    │
                  │   └ failure ----------┤
                  └ explicit/timeout --> ROLLING_BACK

terminal DB axis: COMMITTED | NOT_COMMITTED | UNKNOWN
audit axis:       DURABLE | AUDIT_PENDING | DURABILITY_LOST
connection axis:  RELEASED | DISCARDED | DISCARD_UNCONFIRMED
```

客户端可见终态不把三个轴压成一个 error：

- `COMMITTED`：DB commit 有确定 ACK，最终 audit 已耐久。
- `COMMITTED_AUDIT_PENDING`：DB 已知 committed，但 primary outcome audit 未落地，emergency WAL 已 fsync；不是 DB unknown。
- `DB_OUTCOME_UNKNOWN`：COMMIT 可能已执行，禁止自动重试；audit 状态单独报告 durable/pending/lost。
- `NOT_COMMITTED`：有 typed 证据证明未提交；audit 仍可能 pending。

### 6.2 coordinator 的 begin、execute 与终结流程

`begin_transaction`：

1. 验证 protocol/principal/proof/owner epoch、READY、request id、expected seq；从集群 admission 预留 plan bytes。
2. 对 1..32 个有序 item 做大小、single-statement、transaction-control、SQL shape、action/column grant 和 closure preflight；任一 deny/error/unknown 都不开 native tx。
3. 在短生命周期受控连接上完成需要的 explain/binder manifest；释放该连接。完整 plan产生第 3 节 `plan_digest`。
4. 混合决策规则：deny/error/unsupported 优先；mask 对写授权无意义并拒绝；approve 只能覆盖策略标记为可人工批准的风险，不能覆盖 agent/table deny、列 grant、binder/closure、资源硬限制或禁用 SQL 形态。
5. 需要审批且无有效 ticket时，创建/返回绑定 plan digest 的审批，不占 tx slot、DB connection或 business lock。
6. 有效 ticket按第 3.2 节 CAS消费；取得 cluster tx/DB connection lease，从 pool acquire一个 physical connection。
7. 在该 connection上重做 binder/manifest并常量时间比较 digest；设置网关生成的 role/search_path/timezone/timeout，取 backend identity，native Begin。
8. `tx_begin` audit barrier成功后才返回 ACTIVE；失败则 typed rollback/discard。control DB locks在步骤 6 结束前已全部释放。

`execute_write` 在 ACTIVE 中：

- operation id 必须正好等于下一 ordinal，raw SQL/AST/reason/body digest 与 plan完全相等；同 session并发请求不排队，返回 `SESSION_BUSY`。
- 每次从 raw SQL重新 parse，并检查 sealed capability、backend identity、manifest/catalog digest、hard-revocation epoch和 remaining deadline；客户端 AST/对象名永不可信。
- 只在私有 `TransactionCapability.Execute(SealedDMLStatement)` 上执行；受影响行数、总行数和 byte/work budget任一超限，即使 statement 已执行也全事务 rollback。
- statement decision/result audit在响应前耐久；成功后才原子推进 local seq/next ordinal。active tx期间不为推进 seq同步写 control directory。
- DB error、cancel、timeout、policy hard revoke、binder/catalog差异、audit barrier失败都进入 rollback-only并立即尝试 typed rollback；PG aborted transaction或MySQL仍可继续都不改变“整笔停止”。
- begin snapshot后的普通 policy revision不追溯，最长陈旧窗口受60s tx wall限制；新出现的人工 approve不能在持锁事务中等待，必须 rollback后在事务外重新审批。

`commit_transaction` 只有完整消费 plan才可进入 COMMITTING：先校验 seq/idempotency和deadline，写 primary `tx_commit_intent`，再且仅再调用一次 `FinishCommit`。不得因请求取消、超时或 response丢失再发 COMMIT。`rollback_transaction` 同样只调用一次 typed finish；重复请求读 tombstone。

### 6.3 terminal ABI

```go
type DBOutcome uint8
const (
    DBCommitted DBOutcome = iota + 1
    DBNotCommitted
    DBOutcomeUnknown
)

type ConnectionDisposition uint8
const (
    ConnectionReleased ConnectionDisposition = iota + 1
    ConnectionDiscarded
    ConnectionDiscardUnconfirmed
)

type EvidencePhase uint8
const (
    TerminalNotSent EvidencePhase = iota + 1
    TerminalZeroBytesWritten
    TerminalSentAcked
    TerminalSentAckMissing
    TerminalWriteIndeterminate
    RollbackAcked
    ServerTxKnownAborted
)

type TerminalResult struct {
    Operation  CommitOrRollback
    DBOutcome DBOutcome
    Connection ConnectionDisposition
    Evidence  EvidencePhase
    DriverClass StableDriverClass
    Elapsed time.Duration
}

type TransactionCapability interface {
    Execute(context.Context, SealedDMLStatement) (WriteResult, error)
    FinishCommit(TerminalContext) TerminalResult
    FinishRollback(TerminalContext) TerminalResult
}
```

`TerminalContext` 持有独立 deadline 和 connector/physical connection 控制权，不继承已取消 request context。实现必须穷举 `(operation,evidence)` 映射；未知枚举默认 `DBOutcomeUnknown + Discard`。禁止 `strings.Contains(err.Error(), ...)`、SQLSTATE 文本或本地 timeout 文本推导提交事实。

### 6.4 证据映射

| operation / evidence | DB outcome | connection |
|---|---|---|
| COMMIT `TerminalSentAcked` 且协议状态 clean | `COMMITTED` | `RELEASED`，仅 reset/health check 通过后 |
| COMMIT `TerminalZeroBytesWritten` | `NOT_COMMITTED` | `DISCARDED` |
| COMMIT `TerminalSentAckMissing` / partial / indeterminate | `UNKNOWN` | `DISCARDED` 或 `DISCARD_UNCONFIRMED` |
| COMMIT 前 server 已 typed-aborted 且无 COMMIT send | `NOT_COMMITTED` | 默认 `DISCARDED` |
| ROLLBACK `RollbackAcked` | `NOT_COMMITTED` | clean 才 `RELEASED` |
| ROLLBACK 已发但 ACK 丢失，且私有连接上从未发送 COMMIT | `NOT_COMMITTED` | `DISCARDED/DISCARD_UNCONFIRMED` |
| 无法证明上列任一条件 | `UNKNOWN` | 不得 release |

PG 实现从 pgx protocol phase、ReadyForQuery transaction status、write completion 和 socket close 产生 typed evidence；MySQL 实现由自定义 connector 产生。普通 `sql.Tx.Commit()` 返回的 error 本身不足以证明 `NOT_COMMITTED`。

## 7. P0-4：MySQL 有界终结及严格 feature-off

MySQL 候选实现必须由 AgentSQL 自有 connector 固定 `sql.Conn` 并保留真实 `net.Conn`：

1. 启动独立 terminal watchdog，默认 150ms；记录终结 packet 是零字节、完整写入还是写入不确定。
2. 正常 ACK 在 deadline 内到达则产生 `TerminalSentAcked`。
3. watchdog 到期依次 `SetDeadline(now)`、关闭 socket、使 `sql.Conn.Raw` 返回 `driver.ErrBadConn`，再关闭 `sql.Conn`；该连接永久不得回池。
4. terminal API 最迟在额外 25ms 本地调度余量内返回；超出即 `DBOutcomeUnknown + ConnectionDiscardUnconfirmed` 并告警。
5. 新 acquire 必须观测不同 connection id；pool metrics 必须证明故障连接未复用。

S0/MySQL 8.4.11 结果：COMMIT 写入前故障约 169ms、其余约 150ms；写入前为 `NOT_COMMITTED`，COMMIT 已发/ACK 丢失时旁路真相实际 committed，故必须是 `UNKNOWN`；`ErrBadConn` 后 connection id 均改变。该结果证明本地调用和 pool discard 可有界，**不证明单向黑洞中服务端锁在同一时间内释放**。

每个 MySQL datasource 还必须在建连时 attestate 目标 patch 可用的 transaction/idle、network read/write、TCP 失活和 lock wait timeout，且网关账号无 DDL/admin/FILE/routine 权限。timeout 不可设置、读回不一致或基础设施单向黑洞释放时间超标，capability 即 unsupported。

`b5_tx_mysql` 只有以下 gate 全绿才能由 MySQL owner 与架构 owner另行改默认值：

1. 8.0/8.4 版本锁定 DML binder 和所有显式列 usage；
2. trigger/FK/default/generated/partition/index/routine 等 MDL/隐式 closure 完整且有 TOCTOU 证明；
3. 自定义 terminal connector 四个 send/ACK fault window、discard 和服务端 timeout attestation；
4. 跨主机真实单向 client→server、server→client 黑洞、proxy reset、DB failover 下的锁释放与 unknown 分类。

任一缺失都返回 `DIALECT_TRANSACTION_UNSUPPORTED`；禁止 AST-only、表级授权或“最佳努力 rollback”降级。

## 8. PostgreSQL DML binder ABI、closure 与拒绝矩阵

### 8.1 正式 ABI

发布独立于 B2 SELECT 的 `agentsql-binder-dml-1`（最终 hash 由构建产物冻结）。它在 coordinator 已持有的**同一 connection、同一 native transaction、同一 backend PID**上完成 prepare → manifest → seal → execute；Go 私有类型 `PostgresPreparedDML` 的 Execute 不接受 raw SQL。

ABI 必须批量返回：

- command type、唯一 target OID、rewritten query 数、result relation；
- 每个 `(relation_oid,attnum,site)` 的 usage=`write_target|reference`；assignment/INSERT target 是 `write_target`，RHS/WHERE/join/RETURNING expression 是 `reference`；
- RETURNING 是否存在及其 reference 集，虽然 gateway 首发拒绝输出；
- relation/object closure：table、index、sequence、constraint、trigger、rule、RLS policy、function/operator/type/collation、partition/inheritance；
- analyzed digest、manifest digest、dependency/catalog digest、ABI/hash/PG major；
- backend PID、transaction identity、lock manifest、invalidation/replan generation。

执行前必须持有目标/引用 relation 的 OID locks；seal 后任何 manifest/catalog/backend 变化都 rollback。S0 临时 ABI 在 PG14/18 上验证了：原 binder 确实 SELECT-only；新 walker 能标出 target.value=`write_target` 以及 WHERE/join/RETURNING references；PID 前后一致；`UPDATE ... FROM ... RETURNING` 返回 `1:joined`；并发 ALTER TABLE 在约 259–262ms 以 `55P03` 失败。该证据支持 ABI 方向，但不放宽 gateway SQL 子集。

### 8.2 隐式对象 closure 原则

**执行可能触达的每个对象都必须在 begin manifest 中，具有明确 usage/action、稳定身份和执行前 lock；否则在执行前拒绝。** 执行后才观察到新 lock 只能证明安全缺口，不能继续事务。

S0 中 AFTER UPDATE trigger 写入 `audit_log`，执行后出现 `audit_log:RowExclusiveLock`，但它不在执行前 manifest；因此 trigger 首发必须拒绝。正式 binder 后续若宣称支持某类隐式对象，必须在 rewrite/analyze 后枚举其传递闭包、授权每个写目标/引用列，并有真实 DDL race 测试。

### 8.3 首发拒绝矩阵

| 检测对象/形态 | v0.4 行为 | 稳定错误码 | 将来开放所需证据 |
|---|---|---|---|
| trigger / event trigger | 拒绝 | `AUTH_IMPLICIT_OBJECT_UNCLOSED` | trigger body 传递读写/函数闭包、列 grant、锁与递归上限 |
| FK（含 NO ACTION）/cascade | 拒绝 | `AUTH_FK_CLOSURE_UNSUPPORTED` | referenced/child tables、deferred timing、cascade action/列闭包 |
| default/identity/sequence | 只允许显式给值且表上不存在会被触发的此类对象；否则拒绝 | `AUTH_DEFAULT_CLOSURE_UNSUPPORTED` | sequence/default expression/function manifest 与 grants |
| generated column / expression index / nontrivial CHECK | 拒绝 | `AUTH_EXPRESSION_CLOSURE_UNSUPPORTED` | expression 中函数/operator/collation 与引用列闭包 |
| rule、view target、RLS/security barrier | 拒绝 | `AUTH_REWRITE_CLOSURE_UNSUPPORTED` | rewrite 后全部 query、checkAsUser、policy expression 闭包 |
| partition/inheritance/foreign/unlogged/temp table | 拒绝 | `AUTH_RELATION_KIND_UNSUPPORTED` | leaf routing、attach/detach race、每 leaf identity/lock |
| user function、volatile expression、CALL/DO | 拒绝 | `AUTH_FUNCTION_CLOSURE_UNSUPPORTED` | side-effect classification 与传递依赖 |
| CTE/subquery/UPDATE FROM/DELETE USING | 拒绝 | `TX_DML_SHAPE_UNSUPPORTED` | 多 relation usage、cardinality 与 closure 测试 |
| RETURNING | binder 必须能识别；gateway 拒绝交付 | `TX_RETURNING_UNSUPPORTED` | transaction-scoped B2 delivery seal 独立设计 |
| catalog/lock/ABI/hash 不完整 | 拒绝/rollback | `AUTH_DML_BINDER_REQUIRED` | 对应 major 的签名构建和 attestation |

简单 `NOT NULL` 可作为内建、无额外对象的约束开放；其余 constraint 默认落入拒绝项。任何未知 `relkind`、node tag、dependency class 或 PG major 均 fail closed。

## 9. P0-6：审计 durability、emergency WAL 与 reconciliation

### 9.1 两个正交状态轴

DB outcome 与 audit durability 必须分开保存和返回：

| DB 证据 | primary audit | emergency WAL | 内部/运维状态 | 客户端语义 |
|---|---|---|---|---|
| committed | durable outcome | 不需要 | `COMMITTED` | success |
| committed | outcome 失败 | fsync 成功 | `COMMITTED_AUDIT_PENDING` | 已提交、不可重试、审计待补 |
| unknown | durable unknown event | 不需要 | `DB_OUTCOME_UNKNOWN` | 不可重试、需对账 |
| unknown | primary 失败 | fsync 成功 | `DB_OUTCOME_UNKNOWN_AUDIT_PENDING` | 不可重试、双重对账 |
| not committed | rollback audit 失败 | fsync 成功 | `NOT_COMMITTED_AUDIT_PENDING` | 可新建新事务，不得重放旧 request id |
| 任一 | primary 失败 | WAL 也失败 | `*_AUDIT_DURABILITY_LOST` | 高危终态；不能把已知 committed 改名 unknown |

`tx_commit_intent` 必须在 **primary audit store** 耐久后才能调用 business COMMIT；emergency WAL 不能替代这道 commit 授权屏障。primary intent 写失败时无条件 rollback，WAL 只记录 `rollback_pending/not_committed`。此时 rollback 可以在 DB 轴确定为 not committed，但同一 audit outage 下不能声称 rollback event 已进 primary store。

### 9.2 独立 emergency WAL

emergency WAL 是与 primary audit store 独立的 durability subsystem，不是内存队列。每个节点预分配专用文件/卷，mode 0600，记录采用 version + length + sequence + EventUUID + tx id digest + event kind + DB outcome + primary payload digest + timestamp + CRC；敏感字段加密。每条 append 后 `fdatasync/fsync` 才可报告 `AUDIT_PENDING`。

必须实现：容量 reservation、单调 sequence、torn-tail 截断恢复、CRC、轮转、加密/key rotation、磁盘满 failpoint、启动重放、健康度/lag/oldest-age metrics 和告警。begin 前必须为最坏的 intent/terminal 记录预留 WAL 空间；预留失败则不开事务。

S0 的紧凑记录约 120B/事件，100 次逐条 fsync 平均约 1.532ms，故它是容量/延迟基线而非 SLO；生产预算按每记录 256B 计费并保留 2 倍 headroom。

### 9.3 幂等 reconciliation

- primary event 和 emergency record 共享稳定 EventUUID；semantic retry 不生成新 UUID。
- reconciler 校验 CRC/sequence/payload digest，以 `EventUUID ON CONFLICT DO NOTHING`/等价 API 写 primary audit。
- 成功后追加 `tx_audit_reconciled`，含原 EventUUID、WAL sequence、attempt count、reconciled_at；不改写历史 chain。
- 重放可任意次；S0 对两个事件各重放两次，最终仍各一行。
- `DB_OUTCOME_UNKNOWN` 还需独立 resolver 依据 commit intent、业务幂等键和数据库事实产生 `tx_db_outcome_reconciled`；不得由 WAL 上传器猜测。

### 9.4 best-effort 的诚实边界

本地 emergency WAL 覆盖“primary audit outage、进程仍可写本地耐久盘”，不覆盖节点与本地盘同时永久丢失、磁盘/控制器谎报 fsync、密钥永久丢失或 business commit 后立刻发生整机灾难。跨故障域同步 WAL 会重新引入第二个远端可用性依赖，不在 v0.4 范围。

因此：commit 前 primary intent 不可用时始终 fail closed；若此时 WAL 也不可用，rollback 事实只能作为当次响应中的 best-effort 诊断并报 durability-lost。commit 后若 DB 已明确 committed，任何审计失败都不能物理撤销，也不能称为 DB unknown，只能报告 committed + audit durability 状态并最高级告警。这是产品/架构/Audit/SRE 必须签收的 best-effort 边界。

### 9.5 事务事件合同

所有事件带 `transaction_id, transaction_seq, EventUUID, plan_digest, begin_authorization_digest, previous_tx_event_digest`；事务内 seq从1严格递增，全局 audit chain可与其他事务交错。建议事件：

| event | 必须发生的时点 |
|---|---|
| `tx_plan_decision` | begin成功/拒绝响应前；含每项稳定 decision/reason/manifest digest |
| `tx_begin` | native Begin后、ACTIVE响应前 |
| `tx_statement` | 每条DML执行后、推进 ordinal/响应前 |
| `tx_commit_intent` | 唯一一次 native COMMIT前，且必须在primary audit耐久 |
| `tx_terminal_outcome` | typed finish后；保存DB/audit/connection/evidence四轴，不保存driver文本 |
| `tx_rollback` | rollback attempt后；保存trigger与confirmed/unconfirmed |
| `tx_audit_reconciled` | emergency event幂等进入primary后 |
| `tx_db_outcome_reconciled` | unknown由独立resolver获得新证据后；只追加不改历史 |
| `session_terminal` | close/expire/owner_lost后，business资源已释放 |

EventUUID由语义 event key派生或在状态转换CAS时一次生成并持久保存；audit/WAL retry复用它。raw SQL仍遵循既有脱敏/加密策略，status和通用日志只显示digest与受控摘要。

## 10. 固定锁序、250ms 等待预算与 final fence

### 10.1 唯一允许的等待图

```text
control snapshot / approval CAS / admission
              │ 完成并释放全部 control DB locks
              ▼
business native transaction
              │ bounded append only
              ▼
audit-chain terminal sink

business terminal returns + physical connection release/discard
              │
              ▼
final control fence / directory terminal hint / response delivery
```

锁 rank 固定为 `control(10) → business(20) → audit-chain(30)`。final fence 是 business release 后的新 phase，不与 business/audit lock 重叠。audit-chain 不得反向调用 rank 10/20；若 audit 与 control 共一个 PG，也必须使用无 FK/trigger 到 control 表的独立 schema/table、独立短 transaction，且 InsertBatch 时不持 coordinator mutex。

session owner heartbeat 不是 business 请求所等待的步骤；active tx 的共享目录 state_hint 允许滞后，最终状态在 connection 释放后更新。由此不会为了“及时显示状态”引入 business→control 反边。

### 10.2 B5 专用预算

| wait | hard budget | 超时动作 |
|---|---:|---|
| control row/approval/admission lock | 250ms | begin 失败，不开 business tx |
| business `lock_timeout` / MDL wait | 250ms | rollback-only |
| audit enqueue | 25ms | rollback-only |
| audit backend attempts + group commit | 200ms | rollback-only；不得继承现有 2s attempt/5s root |
| audit adapter margin | 25ms | 强制返回失败 |
| 单个 audit barrier 总计 | **250ms** | rollback；commit outcome 后转 emergency WAL |
| final fence（business 已释放） | 250ms | 不交付普通成功，进入 pending/retryable fence 状态 |

所有 budget 再取 `min(remaining tx wall, value)`；不允许重试越过 tx wall。statement execution budget与 lock wait 分开，legacy HTTP 又受 300ms in-flight watchdog 限制。

### 10.3 无反边/无 40P01 证明义务

在 AgentSQL 控制的组件图中，所有同步 wait edge 的 rank 严格递增，因此不存在回到已持 rank 的有向环，PG 无法由这些跨组件边形成 `40P01`。S0 的反例 business→control 与 control→business 在约 1006ms 真实触发 `40P01`；统一 control→business 后第二事务约 252ms 得到 `55P03`，无 deadlock。

该证明不声称用户 DML 与外部数据库客户端永不发生业务行锁死锁；真实 PG deadlock/serialization error 仍按 statement failure 全事务 rollback。release gate 必须用 lock-rank assertion、代码扫描和真库对照测试证明 audit sink/final fence 没有新增反边。

## 11. P0-7：集群 admission、连接/byte budget 与 tombstone

### 11.1 集群原子租约

所有 session、active tx、datasource connection、active plan bytes 都由共享 admission store 做条件 CAS，并带 `lease_id, owner_instance, owner_epoch, expires_at, generation`。进程内计数只作缓存/快速拒绝，不能放宽共享结果。lease reclaim 必须区分 owner heartbeat 失效与 terminal 完成，使用 generation fencing 防止旧实例归还/续租新 lease。

获取顺序：`session → active plan bytes → transaction → datasource DB connection → statement memory`。释放逆序，但 DB connection token 只在物理连接已 release/discard 后释放。任何 acquire 失败不等待排队，返回稳定 limit code。

### 11.2 DB 总连接预算

每 datasource 配置硬预算：

```text
db_hard_budget <= database max_connections - external_reserve
sum(instance stateless pool claims
    + pinned B5 connection leases
    + migration/health claims)
  + admin_emergency_reserve
  <= db_hard_budget
```

默认至少保留 `admin_emergency_reserve=2` 和 `stateless_reserve=max(1, 25% of db_hard_budget)`；B5 可用 token 为剩余值，且默认每 datasource最多 2 个 active tx。不能给每实例各配置同样 `ConnLimit` 后假定总和安全。S0 以共享 counter 将 12 个请求精确限制为 6 个真实、不同 backend PID，证明原子模型可行。

### 11.3 默认资源值

| 资源 | 默认 | 编译/配置硬上限或规则 |
|---|---:|---|
| sessions / agent | 4 | 16，集群级 |
| sessions / tenant | 32 | 128，集群级 |
| sessions / cluster | 256 | 1024 |
| active tx / agent | 1 | 4 |
| active tx / tenant | 8 | 32 |
| active tx / datasource | 2 | 受 DB token 公式更小值约束 |
| statements / tx | 16 | 32 |
| SQL bytes / statement | 256KiB | hard |
| active plan / tx | 1MiB | hard，含 SQL/reason/labels/canonical overhead |
| active plan bytes / tenant | 8MiB | 32MiB |
| active plan bytes / datasource | 32MiB | 128MiB |
| active plan bytes / cluster | 64MiB | 256MiB |
| affected rows / tx | 10,000 | policy/datasource 可更小 |
| idempotency cache / session | 64 entries / 256KiB | 128 / 512KiB；大结果只存 digest |
| session idle / absolute | 10min / 60min | 30min / 60min |
| tx idle / wall | 15s / 60s | 30s / 60s，不可续期 |
| B5 statement | datasource 更小值，最多 5s | legacy HTTP call 另受 300ms watchdog |
| cleanup rollback | 150ms connector + 850ms local总上限 | 独立 context；服务端锁释放另验 |

### 11.4 digest-only tombstone

tombstone 固定结构只含：`session/tx id digest(32B)、terminal code(enum)、DB/audit/connection axes(enum)、final seq、owner epoch、terminal event digest(32B)、expires_at`。禁止引用 raw SQL、reason、plan、rows、ToolResponse body、approval body、HMAC key或 idempotency result body。

默认 TTL 15min，同时受以下集群硬门约束：

- 总条数 50,000；总计费字节 16MiB；单项计费上限 256B；任一先到即淘汰最旧且把完整终态留在 durable audit/WAL。
- 每 principal 新建 120/min、每 tenant 2,000/min、全局 200/s token bucket；close/open churn 同样计费。
- active-plan 与 tombstone 分账；transaction terminal 后立即释放完整 plan bytes，再创建 compact tombstone。
- status 查不到已淘汰 tombstone 时返回 `SESSION_TERMINAL_RECORD_EXPIRED`，不得猜测 rollback。

S0 中 64 个保留 1MiB plan 的 tombstone 增量约 67,117,328B；50,000 个 digest-only 项约 6,570,000B（约 131B/项）。生产计费按 256B 而非依赖 Go 当前对象布局。

## 12. MCP/API 合同与稳定错误码

### 12.1 工具和关键字段

工具集沿用 v1：`open/get/renew/close_session`、`request_transaction_approval`、`begin/commit/rollback_transaction`，以及扩展后的 `execute_write`。所有非 open B5 请求增加 `owner_epoch`、`continuation_proof`；所有 B5 call 包括 get/status 都要求 `request_id`。mutation 还要求 `expected_seq`。

`begin_transaction` 返回 `transaction_id, plan_digest, begin_authorization_digest, next_operation_id, seq, idle_deadline, wall_deadline`。terminal 返回结构化：

```json
{
  "state": "committed_audit_pending",
  "db_outcome": "committed",
  "audit_state": "pending",
  "connection_disposition": "released",
  "retryable": false,
  "terminal_event_digest": "..."
}
```

不得返回 backend PID/thread id、raw driver text、secret、对象存在差异。重复 terminal request 只返回相同 tombstone axes，不重新 Commit/Rollback。

### 12.2 稳定错误码

| code | retryable | tx effect / 说明 |
|---|---:|---|
| `MCP_PROTOCOL_UNSUPPORTED` | false | SDK 前拒绝；未创建/触碰 session |
| `SESSION_NOT_FOUND_OR_DENIED` | false | 不存在/认证/proof/principal 失败统一返回；**不 rollback** |
| `SESSION_PROOF_REQUIRED` | false | schema层发现缺 proof；不查目录、不确认 id 存在、不 touch lease |
| `SESSION_OWNER_EPOCH_STALE` | true | 有效 capability 但 epoch stale；不 rollback |
| `SESSION_WRONG_INSTANCE` | true | 有效 capability 落错 owner；不 rollback |
| `SESSION_ROUTE_UNAVAILABLE` | true | 目录不可用；fail closed，不随机路由 |
| `SESSION_OWNER_LOST` | false | owner lease/crash；只查终态，不迁移 active tx |
| `SESSION_BUSY` | true | 当前 owner已有 operation；保持 tx |
| `SESSION_LIMIT_EXCEEDED` | true | cluster session admission 满 |
| `SESSION_EXPIRED` | false | 经 owner cleanup 后只读 terminal；不复活 |
| `SESSION_RENEW_DURING_TX` | false | ACTIVE 时不能延长 session/tx deadline |
| `SESSION_TERMINAL_RECORD_EXPIRED` | false | compact tombstone 已过期/被有界淘汰 |
| `IDEMPOTENCY_REQUIRED` / `IDEMPOTENCY_CONFLICT` | false | 不执行；有效 owner上的冲突可标 rollback-only |
| `TX_NOT_ACTIVE` / `TX_ALREADY_ACTIVE` | false | 状态不允许；不创建第二 native tx |
| `TX_ID_MISMATCH` | false | 仅有效 owner capability路径可见；标 rollback-only |
| `TX_PLAN_REQUIRED` / `TX_PLAN_MISMATCH` | false | begin 不开 tx；active mismatch rollback-only |
| `TX_APPROVAL_REQUIRED` | false | 不开 tx，返回 approval id |
| `TX_APPROVAL_EXPIRED` / `TX_APPROVAL_PLAN_MISMATCH` | false | 不开 tx |
| `TX_PLAN_INCOMPLETE` | false | 保持 active，允许执行剩余计划；不 commit |
| `TX_ROLLBACK_ONLY` | false | 只允许 status/rollback；其他操作返回同一终态 |
| `TX_MASK_UNSUPPORTED` | false | begin前拒绝；active重校验出现则 rollback |
| `TX_SELECT_UNSUPPORTED` / `TX_RETURNING_UNSUPPORTED` | false | active 请求则 rollback-only |
| `TX_CONTROL_STATEMENT_DENIED` / `TX_DML_SHAPE_UNSUPPORTED` | false | fail closed |
| `TX_IDLE_TIMEOUT` / `TX_MAX_DURATION` / `TX_STATEMENT_TIMEOUT` / `TX_OPERATION_WATCHDOG` | false | rollback |
| `TX_LIMIT_EXCEEDED` / `PLAN_BYTE_LIMIT_EXCEEDED` | slot 可重试 | begin 前拒绝；执行期硬超限 rollback |
| `TX_COMMITTED_AUDIT_PENDING` | false | 已知 committed，不得重放 |
| `TX_DB_OUTCOME_UNKNOWN` | false | 禁止自动重试，进入 resolver |
| `TX_NOT_COMMITTED_AUDIT_PENDING` | false | 已知未提交，旧 request id 不可复用 |
| `TX_ROLLBACK_UNCONFIRMED` | false | connection discard/unconfirmed，session closed |
| `AUDIT_OVERLOADED` / `AUDIT_UNAVAILABLE` | 新事务可重试 | commit 前 rollback；commit 后转 WAL pending |
| `AUDIT_EMERGENCY_WAL_UNAVAILABLE` | false | begin 前拒绝；terminal 后 durability-lost 告警 |
| `FINAL_FENCE_PENDING` | true（仅查询） | business 已终结且连接已释放；不重复 DB 操作，只重试/查询 fence |
| `AUTH_DML_BINDER_REQUIRED` | false | binder/ABI/closure 不完整 |
| `AUTH_DML_ACTION_MISSING` / `AUTH_DML_COLUMN_GRANT_MISSING` | false | relation action 或 write/reference grant 缺失 |
| `AUTH_IMPLICIT_OBJECT_UNCLOSED` | false | 未 manifest 的隐式对象 |
| `AUTH_CATALOG_RACE` | 新事务可重试 | 当前事务 rollback |
| `DIALECT_TRANSACTION_UNSUPPORTED` | false | MySQL gate 未全绿时固定返回 |

外部 `retryable=true` 只表示“可创建一个全新 request/transaction 再尝试”，绝不表示可自动重放 write/commit。

## 13. 更新后的实现切片与独立验收

所有 slice 都在 flags off 下合入；每片有可单独运行的出口测试。

### S1：合同、canonical encoder 与 schema（Architecture + Security）

- 冻结两个 digest schema、terminal/audit 三轴枚举、错误码、JSON schema 和 migration。
- golden 验证字段顺序、空值、Unicode、plan reorder、approval id 改变不影响 plan digest而影响 begin digest。
- 出口：跨 Go 版本/架构 golden 一致；migration upgrade/rollback；flags 默认 off。

### S2：pre-SDK protocol gate 与 continuation capability（MCP + Security）

- SDK 前 header/body/meta allowlist、body replay；secret/HMAC proof、idempotency envelope、300ms legacy watchdog。
- 出口：三协议 S0 场景、未知 initialize 静默降级攻击、proof steal/replay/body substitution 全部通过；旧七工具兼容。

### S3：共享目录、owner fencing 与 cluster admission（Runtime/SRE）

- 强一致 directory、owner epoch/incarnation、directory-aware route、session/plan/tx/connection leases、compact tombstone。
- 出口：至少四进程并发、owner 唯一、lease expiry/stale owner fencing、目录分区、crash reclaim、50k tombstone和 churn/byte cap；错误实例不能触发 rollback。

### S4：typed terminal 与物理连接生命周期（DB Core）

- PG terminal evidence；MySQL custom connector 作为 feature-off 实验实现；release/discard proof。
- 出口：每个 send/ACK 窗口 typed mapping、无错误文本分类、连接不复用、watchdog 上界、race/fuzz；当前单语句路径零回归。

### S5：PostgreSQL DML binder ABI（PG + Security）

- 正式 `agentsql-binder-dml-1`、write/reference/RETURNING manifest、same-backend sealed execution、OID lock与 closure detector。
- 出口：PG14–18 独立 build/hash、支持/拒绝矩阵、并发 DDL、trigger/FK/default/partition/UDF 攻击；未知 closure fail closed。

### S6：planned coordinator 与 approval（Pipeline/Architecture）

- 全 plan preflight、稳定 digest、approval CAS、begin auth digest、顺序/seq/rollback-only。
- 出口：approval 过期/重复消费/plan mutation/begin-failed 属性测试；任何等待审批路径不占业务连接。

### S7：audit axes、emergency WAL 与锁序（Audit/B6 + Runtime/SRE）

- tx events、250ms B5 adapter、预分配 WAL、恢复/轮转/加密/容量、reconciler 和 resolver。
- 出口：primary outage 在 intent 前/commit 后、WAL 磁盘满/torn write/node restart、EventUUID 重放、lock-rank assertion；真实反边测试不得出现 `40P01`。

### S8：MCP tools 与 final fence（MCP + Pipeline）

- 注册 B5 tools、扩展 execute_write、terminal axes response；business release 后 final fence。
- 出口：stdio/HTTP 全 lifecycle、response 丢失、重复 terminal、fence timeout、raw secret/SQL 泄漏检查。

### S9：运维面、文档与灰度（Runtime/SRE + Docs/UI）

- session/tx/audit-pending/unknown 视图、RBAC force rollback、metrics/alerts/runbook、文档 capability matrix。
- 出口：多实例 staleness 不显示为 empty；危险操作 proof；pool/WAL/directory/audit 告警演练。

### S10：真库/真网络 release gate（QA + 全体 owner）

- 执行第 14 节矩阵；PG internal canary后按 datasource 开启。
- MySQL gate 只收集证据，未全绿时测试期望必须是 unsupported。
- 出口：全部角色留档签收、灾难/回滚演练、release note 明示边界；任何未解释 unknown/unconfirmed 阻止扩大灰度。

## 14. 真库、协议与故障测试矩阵

### 14.1 PostgreSQL release-blocking

| 维度 | PR blocking | release blocking |
|---|---|---|
| major / ABI | PG14、PG18 | PG14/15/16/17/18 各自 extension build、签名 hash |
| DML | INSERT/UPDATE/DELETE 最小子集 | 每形态 write/reference grant、affected-row budget、同 backend、原子可见性 |
| closure | trigger、FK、default/identity、generated | 再加 rule/RLS/view/partition/inheritance/index/CHECK/UDF/unknown node |
| concurrency | ALTER target/source 250ms lock timeout | attach/detach、extension invalidation、replan、catalog race、外部 DML deadlock |
| terminal | commit/rollback before-send、ACK、ACK loss | proxy reset、PG restart/failover、client crash、connection release/discard |
| versions | binder manifest golden | 每 major analyzed digest/lock manifest 与 capability attestation |

必须复跑 S0 的 same PID、`DML...RETURNING` 内部探针和并发 DDL 证据；对外工具仍断言 RETURNING 被 gateway 拒绝。

### 14.2 MySQL release gate（v0.4 期望 unsupported）

| 维度 | 必须通过后才可讨论开启 |
|---|---|
| versions | MySQL 8.0 产品最低/最新 patch、8.4.11及声明 patch；`sql_mode`、大小写模式、collation、server UUID 组合 |
| binder/closure | 所有目标/引用列；trigger、FK/cascade、default/generated/on-update/auto-increment、functional index、view/routine/partition 拒绝或完整 manifest |
| MDL/TOCTOU | metadata 权限缺失、DDL race、MDL timeout、catalog fingerprint pre/post、deadlock |
| terminal | COMMIT/ROLLBACK 写入前、写入后 ACK 丢失；150ms deadline/Close/ErrBadConn；新 connection id |
| server timeout | session 值设置/读回/权限、idle transaction、read/write/TCP失活、锁释放上界 |
| network | 跨主机 client→server 单向黑洞、server→client 单向黑洞、NAT/proxy half-open、DB kill/failover |

COMMIT 已发/ACK 丢失的断言永远是 `UNKNOWN`，即使某次旁路观察为 committed 或 not committed。

### 14.3 MCP、audit、cluster 与端到端

- MCP v1.7.0：2025-06-18/2025-11-25/2026-07-28 断连实测；产品只 allow 2025-06-18；header/body/meta 冲突和未知版本均在 SDK 前拒绝。
- 2025-06-18 in-flight call 在 300ms watchdog 后 rollback；请求间断连只验证 15s idle/60s wall，不写“立即”。
- primary audit 在 intent 前、intent 后/commit 前、known commit 后、unknown commit 后逐窗故障；WAL fsync、磁盘满、torn tail、重复 replay、payload corruption。
- 四/八实例同时 open/begin/close：cluster session/plan byte/connection counter不超限，owner 唯一，旧 epoch无权操作，quota lease crash 可回收且不双还。
- 控制→业务、业务→audit、release→fence 对照；注入 audit sink 回调即由 rank assertion失败；真库不得产生跨组件 `40P01`。
- stolen id 无 secret、同 API key 不同客户端、stale proof、错实例、route hint篡改、key rotation：均不触发受害事务 rollback。
- stacked SQL、事务内 SELECT、RETURNING、DDL/savepoint/SET/session variable/prepared 全拒绝；事务外 B2 SELECT 完整回归。
- 旧客户端不传 B5 字段时，七个旧工具 response shape、决策和 DB 语义不变。

### 14.4 S0 证据登记与非 SLO 基线

| 探针 | 已吸收的事实 | 尚未证明/不得外推 |
|---|---|---|
| `mysql-terminal.go` | MySQL 8.4.11 四个 terminal 窗口、约 150–169ms 强制返回、`ErrBadConn` 后 connection id 改变、COMMIT ACK loss 为 unknown | 真实单向黑洞、服务端锁释放上界、全部产品 patch |
| `mcp-cancel.go` / `mcp-watchdog-pg.go` | 2025-06-18/2025-11-25 不传播断连取消，2026-07-28 传播；未知 initialize 会被 SDK 降级；300ms 独立 watchdog 后 PG rollback 可行 | legacy 请求间断连不可见；生产网络时延不保证 305ms |
| `pg-dml-binder.go` | PG14/18 新 ABI 方向、write/reference、same backend、RETURNING 技术可行、DDL 被 OID lock 阻止；trigger 暴露隐式 closure 缺口 | 正式 ABI/hash、所有 major、完整 closure |
| `audit-boundary.go` | intent outage fail closed；known commit + outcome outage 是 audit-pending；约 120B/事件、1.532ms/fsync；EventUUID 幂等重放 | 节点+盘共同丢失、云盘 fsync 语义、生产容量/SLO |
| `quota-tombstone.go` | 本地 limit=10 被四进程放大到 40；共享 admission 限 10、plan 8MiB、真实连接 6；owner 唯一；compact tombstone 约 131B | admission store 热点、跨 AZ 分区、生产 crash reclaim |
| `lock-order.go` | 反边约 1006ms 触发 `40P01`；统一顺序约 252ms 得到 `55P03` | 外部客户端造成的业务行锁 deadlock |
| `latency.go` | PG14/18 begin+rollback 平均约 1.38/1.48ms，insert+commit 约 3.48/3.38ms；MySQL insert+commit 约 8.264ms；watchdog create/stop 平均约 772ns | Docker Desktop loopback 数字不是 release SLO |

PG DML 探针的未优化 manifest + lock scan 约 25–30ms，正式 ABI 必须批量返回 closure，并对 catalog round-trip、返回字节与 walker work 设置预算。所有 S0 数据用于合同裁决和回归基线，不作为容量承诺。

## 15. 负责人签收清单

| 角色 | flag 开启前必须签收 |
|---|---|
| 架构 owner | PG-first、planned-only、active tx 不迁移、三轴 terminal/audit 模型、250ms 锁图、best-effort 原理边界 |
| PostgreSQL owner | DML ABI/hash、PG14–18、write/reference、same backend、DDL fence、完整拒绝矩阵和 PG terminal evidence |
| MySQL owner | v0.4 默认 off/unsupported；binder/MDL、connector、server timeout、单向分区四 gate；ACK 丢失 unknown |
| Security owner | continuation secret/HMAC、owner epoch、防枚举；approval digest 分层；DML grants与隐式 closure fail closed；policy snapshot撤销窗口 |
| MCP owner | SDK 前只 allow 2025-06-18；三版本取消事实；legacy 300ms in-flight与请求间 idle/wall 的准确文案 |
| Audit/B6 owner | committed/audit-pending/DB-unknown 分离；WAL格式/容量/fsync/recovery；EventUUID reconciliation；audit terminal-sink 无回调 |
| Runtime/SRE owner | 共享目录可用性、sticky route、cluster leases、DB总连接 reserve、plan/tombstone bytes/churn、crash/shutdown/告警 runbook |
| QA/Release owner | PG/MySQL 真库矩阵、真实单向网络、四进程以上 fanout、protocol 三版本、audit outage、锁序与负属性全部留证 |

### 15.1 仍需负责人拍板

1. 架构/产品是否接受首发 active transaction tool call 的 legacy HTTP 300ms 上限；若不接受，只能先 qualify 新协议/有状态 transport，不能移除 watchdog。
2. 产品是否接受事务内 SELECT 和对外 RETURNING 均拒绝；S0 的 RETURNING 技术可行证据不能替代 B2 delivery 设计。
3. Security 是否接受 begin snapshot 下普通 policy 最长 60s 生效、只有 hard-revocation epoch立即触发 cleanup。
4. Audit/SRE 是否接受本地 emergency WAL 的故障域边界，还是投资跨节点 durability；两者不能用“最终会写”模糊替代。
5. Runtime/SRE 需给每个 datasource确定 `db_hard_budget/external/admin/stateless reserve` 的实际值，而不是沿用每实例 ConnLimit。
6. MySQL/架构未来是否另立版本目标；v0.4 本文裁决不允许 MySQL GA。

## 16. 已知原理边界

- 独立 business DB 与 audit store 没有原子提交；intent + WAL + reconciliation 缩小窗口，不能创造 XA 语义。
- COMMIT bytes 可能已到服务端而 ACK 丢失时，客户端不可能仅靠 socket error判定结果；正确答案只能是 `DB_OUTCOME_UNKNOWN`。
- legacy stateless HTTP 在两请求之间没有“连接仍属于某 session”的可观察事实，只能靠 idle/wall deadline。
- active native transaction不能安全迁移到另一进程；owner crash 位于 commit窗口时只能依靠 durable intent和对账。
- 本地 WAL不覆盖节点与盘共同永久丢失；fsync latency基线不是云盘/生产 SLO。
- 通用 SQL 的隐式副作用闭包不可由表名匹配安全近似；未被正式 binder manifest覆盖的形态只能拒绝。
- 有向锁图证明只覆盖 AgentSQL 控制的 wait edge；外部事务仍可能造成业务级 deadlock，处理方式是全事务 rollback。
- sticky route、quota 与 directory依赖强一致 control基础设施；其不可用时 B5牺牲可用性而 fail closed。
- normal policy snapshot 在事务 wall 内可能陈旧；若要求零延迟撤销，需要独立的全局 fencing/lease 项目。

## 17. 最终放行条件

B5 可以开始实现，不等于可以启用。S1–S9 可在 flags off 下推进；只有 S10 的 PostgreSQL 全矩阵、八角色签收和 canary停止条件全部完成，`b5_tx_postgres` 才可逐 datasource开启。MySQL 在第 7 节四类 gate 全绿且另经设计复审前始终 `unsupported`。

在此之前，对外事实保持：**MCP 单 call 单语句、HTTP stateless、B2 SELECT 在事务外、没有可用的跨请求生产事务。**

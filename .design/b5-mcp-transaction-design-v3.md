# AgentSQL v0.4 B5：MCP 多语句事务与跨请求会话——详细设计 v3

状态：**设计 GO；仅允许全 feature-off 实现，尚未生产放行**  
日期：2026-09-25（Asia/Shanghai）  
代码基线：`feature/v0.4`，`HEAD=9cb14511674ff3dd580bdf710941085fab63bdc9`  
输入：`b5-mcp-transaction-design-v2.md`、`b5-v2-review.md`、`b5-s0-findings.md`。  
约束：本文只冻结设计合同；不代表实现、真库证据、owner 签收或 canary 已完成。

## 0. 裁决、范围与 v2 五个 P0 的关闭

B5 v0.4 GA 仍采用 **PostgreSQL-first**。`b5_sessions`、`b5_tx_postgres`、`b5_tx_mysql` 默认且迁移后仍为 off。本文关闭的是 v2 评审指出的五个设计缺口，因此可以进入全 flag-off 实现；任何生产 flag 的启用仍必须等待 S10、逐 datasource attestation、真库/真网络证据和角色签收。

MySQL 在 v0.4 对外固定返回 `DIALECT_TRANSACTION_UNSUPPORTED`。MySQL 的实验 connector 和测试可以在 flag-off 下开发，但其 gate 不阻塞 PostgreSQL GA，也不能据此打开 `b5_tx_mysql`。

| v2 review P0 | v3 的唯一合同 | 设计判定 |
|---|---|---|
| P0-1 native BEGIN 顺序 | pinned physical connection/token → native BEGIN → `SET LOCAL` → 同 tx bind/closure/lock/seal → 与 preflight 比较 → `tx_begin` barrier → ACTIVE；比较失败 typed rollback/discard，approval=`consumed_begin_failed`，dependency fence 到 terminal | **关闭** |
| P0-2 terminal evidence 非总函数 | 冻结 operation × write phase × reply/server status 的总映射；`Finish*` 独占完整物理连接生命周期；只有 idle + reset + health + status 全绿才 `RELEASED` | **关闭** |
| P0-3 owner loss/connection lease | 独立 connection lease 状态机；`DISCARD_UNCONFIRMED` 不释放预算；pool/migration/health 也先取得共享 claim 并约束 `MaxOpenConns` | **关闭** |
| P0-4 emergency WAL 不可重建 | WAL 保存完整版本化 canonical event、双链所需字段、AEAD、reservation/charge；相同 UUID 必较 payload digest；只有 fsync 确认才 `AUDIT_PENDING` | **关闭** |
| P0-5 watchdog 无 in-flight 终结协议 | 单 operation owner CAS；freeze → cancel → bounded quiesce；不能 quiesce 则 seize/close/discard 且绝不再发 ROLLBACK；只有 Execute 已退出且协议可继续才发一次 typed rollback | **关闭** |

“关闭”表示合同完整且无已知自相矛盾，不表示 release gate 已通过。未实现 typed phase 的 connector、未闭合的 PG major、任何 `UNKNOWN`/`DISCARD_UNCONFIRMED` 无解释增长，都阻止 canary 扩大。

## 1. GA 范围与固定拒绝面

### 1.1 首发允许

- 显式 AgentSQL logical session；每 session 最多一个 active transaction。
- 完整计划在事务外 preflight，所需审批在占用业务连接前完成。
- PostgreSQL 14–18 上经正式 DML ABI 封印的极小 `INSERT/UPDATE/DELETE` 子集。
- 每个 MCP operation 一条顶层 SQL；多个 write operation 依计划顺序、跨 MCP 请求在同一 native transaction/backend 上执行。
- 事务外的 B2 `query`/SELECT 继续走既有 delivery seal；不与 B5 native transaction 混用。

### 1.2 首发固定拒绝

- stacked SQL、SQL script、客户端事务控制语句，包括 `BEGIN/START/COMMIT/ROLLBACK/SAVEPOINT/RELEASE/SET TRANSACTION`。
- active B5 transaction 内 SELECT；返回 `TX_SELECT_UNSUPPORTED` 并标记 rollback-only。B2 SELECT 仅在事务外。
- 对外 DML `RETURNING`。binder 必须识别其 usage，但 gateway 返回 `TX_RETURNING_UNSUPPORTED`。
- DDL、ADMIN、MERGE、UPSERT、CTE、子查询、`UPDATE ... FROM`、`DELETE ... USING`、COPY/LOAD、CALL/DO。
- trigger、rule、RLS、view、partition/inheritance、FK/cascade、用户函数、未闭合 default/identity/sequence/generated/constraint/index expression 等隐式对象。
- session/system/user variable、临时对象、advisory lock、cursor/portal、LISTEN/NOTIFY。
- 用户 prepared statement 或服务端命名 prepared statement；内部 sealed capability 不形成用户功能。
- 跨 datasource、跨实例迁移、XA/2PC、savepoint、事务恢复、自动重试 write/COMMIT。
- 业务 DML 指向 AgentSQL control/audit 保留 schema 或保留 OID。

## 2. 架构、状态轴与不可破坏的不变量

```text
pre-SDK protocol/auth gate
          │
          ▼
shared directory + continuation proof + owner epoch
          │ valid current owner only
          ▼
plan preflight / approval CAS / admission（释放 control locks）
          │
          ▼
pinned connection lease → native BEGIN → SET LOCAL
          │
          ▼
same-tx bind / closure / OID locks / seal / compare
          │
          ▼
tx_begin audit barrier → ACTIVE → sequential Execute
          │
          ▼
typed FinishCommit/FinishRollback owns physical connection
          │ RELEASED or confirmed/unconfirmed discard
          ▼
final control fence / directory hint / response
```

业务终态只有三个正交轴：

```text
DBOutcome:              COMMITTED | NOT_COMMITTED | UNKNOWN
AuditDurability:        DURABLE | AUDIT_PENDING | DURABILITY_LOST
ConnectionDisposition:  RELEASED | DISCARDED | DISCARD_UNCONFIRMED
```

`EvidencePhase` 是产生这些轴的 proof metadata，不是第四个业务状态轴。

必须始终成立：

1. MCP、console、approval、directory 都拿不到 raw executor；SQL 只经 `TransactionCapability` 执行。
2. session/transaction id 和 route hint 只是标识；API authentication、principal binding、continuation proof、current owner epoch 缺一不可。
3. 未认证、proof 无效、wrong owner、stale epoch 路径没有 cleanup capability，不能 touch lease、deadline、socket 或受害事务。
4. 每 transaction 同时至多一个 operation owner；每 transaction 只绑定一个 physical backend；active transaction 不迁移。
5. control DB lock 在 business BEGIN 前全部释放；active business transaction 不同步回调 control store。
6. dependency/OID/catalog fence 从 same-tx seal 一直保持到 typed terminal 完成；不得在最后一条 Execute 后提前释放。
7. audit sink 只接受 immutable canonical event，不得持 audit lock 回调 business/control/directory/fence。
8. final fence 只在 `Finish*` 返回且连接已 `RELEASED`、`DISCARDED` 或进入有预算占用的 `DISCARD_UNCONFIRMED` quarantine 后执行。
9. 所有 digest/event/proof 都使用版本化、length-framed canonical encoding；缺失、空值、optional、not-applicable 是不同 type tag。
10. 错误文本、SQLSTATE 的人类文本、context error 文本只用于诊断，不参与 outcome 推导。
11. 无法证明 owner、closure、terminal phase、audit fsync 或 backend 终止时 fail closed。

## 3. 计划、审批与 digest

### 3.1 稳定 plan digest

`agentsql.b5.plan.v3` 包含 principal/tenant/agent/key revision、datasource revision/dialect/server major/isolation、policy/DML grant/binder ABI/closure-policy digest、所有资源上限，以及按 ordinal 排序的 operation id、raw SQL digest、analyzed-tree digest、reason digest、action 和 manifest digest。

它禁止包含 approval id/state、session/transaction id、owner instance、backend id、随机 lease id 或消费时间：

```text
plan_digest = SHA-256(CanonicalEncode("agentsql.b5.plan.v3", stable_fields))
```

### 3.2 begin authorization digest

审批只签 `plan_digest + approved bounds + expiry`。审批 CAS 成功后，使用 approval id/decision/consumer/nonce/generation、current hard-revoke epoch、API-key revision、owner epoch、transaction id、begin nonce 和 admission lease digest 计算：

```text
begin_authorization_digest = SHA-256(
  CanonicalEncode("agentsql.b5.begin-auth.v2", authorization_fields)
)
```

无需审批使用 typed `approval=not_required`，不用空字符串。

CAS 将审批从 `approved` 原子推进到不可逆的 `consumed_begin_pending`。此后任一 begin 失败都把展示终态收敛为 `consumed_begin_failed`；即使最终标签写回暂时失败，consume generation 已使 ticket 永不可复用。begin 成功才标 `consumed_active`。

## 4. P0-1：begin 唯一时序、状态机与 dependency fence

### 4.1 唯一允许的时序

事务外 preflight 可以使用短连接产生候选 manifest，但它不形成执行授权。审批消费后，最终 begin 的顺序只能是：

```text
client   coordinator       admission/pool       PG physical conn       audit
  │            │                   │                    │                 │
  │ begin      │                   │                    │                 │
  ├───────────>│ approval CAS      │                    │                 │
  │            ├──────────────────>│                    │                 │
  │            │ acquire pinned connection lease/token │                 │
  │            ├──────────────────>│──── dial/acquire ─>│                 │
  │            │                   │                    │ BEGIN           │
  │            │                   │                    ├────────────────>│PG
  │            │                   │                    │ SET LOCAL role  │
  │            │                   │                    │ search_path     │
  │            │                   │                    │ timezone        │
  │            │                   │                    │ statement_timeout
  │            │                   │                    │ lock_timeout    │
  │            │                   │                    │ bind/closure    │
  │            │                   │                    │ lock/seal       │
  │            │ compare actual manifest/digest with preflight           │
  │            │─────────────────────────────────────────────────────────>│ tx_begin barrier
  │ ACTIVE     │<─────────────────────────────────────────────────────────│ durable
  │<───────────│                   │                    │                 │
```

精确定义如下：

1. 验证 protocol、principal、proof、owner epoch、idempotency、READY 和 cluster limits；完成 plan preflight。
2. 消费 approval CAS，取得 transaction/plan/WAL reservation；control DB transaction 完成并释放所有 lock。
3. 取得 datasource capacity claim 下的 pinned connection lease，记录 lease generation、physical connection identity token 和 owner incarnation。
4. 在该 physical connection 上首先执行 native `BEGIN`，取得同一 backend identity；BEGIN 失败走 typed begin cleanup。
5. 在 native tx 内按固定顺序执行 `SET LOCAL ROLE`、固定 `search_path`、UTC timezone、`statement_timeout`、`lock_timeout`。禁止 session-level `SET`。
6. 在**同一 tx、同一 physical connection、同一 backend PID**内完成 DML bind、rewrite closure、dependency enumeration、OID/object lock、manifest、seal。
7. 常量时间比较 actual execution manifest/digest 与 preflight plan 所绑定的 manifest/digest；transaction/backend 动态字段不参与 stable digest，但必须单独一致。
8. 比较成功后写 `tx_begin` primary audit barrier；只有耐久成功才可发布 ACTIVE。
9. ACTIVE 对象持有 sealed statements、dependency fence、connection lease、WAL reservation 和单 operation gate，直到 terminal。

步骤 3 之后的任一失败均不把 approval 退回可用。步骤 4 之后的比较、`SET LOCAL`、closure、seal 或 audit 失败必须调用一次 typed `FinishRollback`；若 connector 已失去 typed protocol 状态则 seize/close 并 discard。结果写为 `consumed_begin_failed`。所有 relation/object locks 和 dependency fence 由 native transaction 持有到 `FinishRollback/FinishCommit` 返回，不允许显式提前 unlock。

### 4.2 coordinator 状态机

```text
READY
  └─preflight ok──────────────> PLAN_READY
       └─approval CAS─────────> APPROVAL_CONSUMED
            └─lease acquired─> CONNECTION_PINNED
                 └─BEGIN ACK─> NATIVE_BEGUN
                      └─SET LOCAL ok────> CONTEXT_FIXED
                           └─bind/lock/seal──> SEALED_IN_TX
                                └─compare ok──> BEGIN_AUDITING
                                     └─barrier durable──> ACTIVE

APPROVAL_CONSUMED..BEGIN_AUDITING --failure--> BEGIN_FAIL_TERMINATING
BEGIN_FAIL_TERMINATING --typed finish + disposition--> READY/TOMBSTONE

ACTIVE --all operations--> COMMITTING --FinishCommit once--> TERMINAL
ACTIVE --deny/error/timeout/close--> ROLLBACK_ONLY
ROLLBACK_ONLY --FinishRollback once--> TERMINAL
TERMINAL --connection lifecycle complete--> FINAL_FENCE --> READY/TOMBSTONE
```

`COMMITTING` 和 `ROLLING_BACK` 都是单向状态。任何 request retry、watchdog、shutdown 或 normal return 都必须竞争同一个 terminal CAS；获胜者之外的调用者只能等待/读取同一 terminal result，不能再次发送协议命令。

## 5. P0-2：typed terminal 总函数与完整连接生命周期

### 5.1 冻结枚举和边界

```go
type TerminalOperation uint8 // Commit | Rollback

type TerminalWritePhase uint8
const (
    TerminalNotSent TerminalWritePhase = iota + 1
    TerminalZeroBytesWritten
    TerminalPartialBytesWritten
    TerminalFullFrameWritten
    TerminalWriteIndeterminate
)

type ProtocolReply uint8
const (
    ReplyMissing ProtocolReply = iota + 1
    CommitPositiveACK
    CommitDefinitiveRejection
    RollbackPositiveACK
    RollbackDefinitiveRejection
    ReplyUntypedOrMismatched
)

type ServerTxStatus uint8
const (
    ServerStatusNotObserved ServerTxStatus = iota + 1
    ServerIdle          // PG ReadyForQuery 'I'
    ServerInTransaction // PG ReadyForQuery 'T'
    ServerFailed        // PG ReadyForQuery 'E'
    ServerStatusUnknownOrContradictory
)
```

- `TerminalNotSent`：terminal send permit 尚未交给 connector，没有 terminal write syscall，且 operation gate 已封住 socket。
- `TerminalZeroBytesWritten`：connector 已进入 send path，但其计数 wrapper 证明 terminal frame 的应用层字节接受数为 0；普通 `write returned error` 不自动等于零字节。
- `TerminalPartialBytesWritten`：精确知道 `0 < n < frame_len`。
- `TerminalFullFrameWritten`：完整 protocol frame 已交给 transport；不等于服务端执行。
- `TerminalWriteIndeterminate`：write/缓冲/TLS/proxy 层无法证明上述任一精确阶段。
- ACK 必须是 connector 对当前独占 terminal operation 解码出的 typed reply。SQLSTATE 可作为结构化诊断字段，但错误文本不能把 untyped error 升格为 definitive reply。
- PG deferred constraint 在 COMMIT 时返回的 ErrorResponse 是 `CommitDefinitiveRejection`；随后的 ReadyForQuery 通常为 `ServerIdle`。它是 `NOT_COMMITTED`，不是 `UNKNOWN`。

未知 enum、ABI 版本、reply 与 operation 不匹配、connector 不能产生 typed write phase 时，固定降级为 `DBOutcomeUnknown`，并进入 discard；禁止猜测。

### 5.2 DBOutcome 总映射

下表是按优先级求值的**总函数**，涵盖所有 `operation × write phase × reply`。server status 不得把 `UNKNOWN` 猜成 committed/not-committed；它只参与连接处置。

| operation | protocol reply | write phase | DBOutcome | 说明 |
|---|---|---|---|---|
| COMMIT | `CommitPositiveACK` | 任意已知 phase | `COMMITTED` | reply 是更强的服务端执行证据；phase 矛盾时仍不可 release |
| COMMIT | `CommitDefinitiveRejection` | 任意已知 phase | `NOT_COMMITTED` | 包含 PG deferred-constraint COMMIT failure |
| COMMIT | `ReplyMissing` | `TerminalNotSent` | `NOT_COMMITTED` | connector 证明 COMMIT 未进入 send path |
| COMMIT | `ReplyMissing` | `TerminalZeroBytesWritten` | `NOT_COMMITTED` | connector 证明零 terminal bytes |
| COMMIT | `ReplyMissing` | partial/full/indeterminate | `UNKNOWN` | 服务端可能已执行，禁止重试 |
| COMMIT | rollback ACK、rollback rejection、untyped/mismatched | 任意 | `UNKNOWN` | connector/protocol invariant 已破坏 |
| ROLLBACK | `CommitPositiveACK` 或 mismatched commit reply | 任意 | `UNKNOWN` | 表明可能存在先前 COMMIT；高危 invariant violation |
| ROLLBACK | rollback ACK/rejection、missing | 任意已知 phase | `NOT_COMMITTED`* | *仅当 operation controller 同时证明 `NoCommitEverSent=true` 且已夺取唯一 socket owner |
| ROLLBACK | `ReplyUntypedOrMismatched` 或不能证明 no-commit fence | 任意 | `UNKNOWN` | connector 无 typed phase 时不可用“rollback 大概成功”推断 |

`RollbackDefinitiveRejection` 只说明 ROLLBACK 命令未正常完成；在 `NoCommitEverSent`、独占 socket、随后强制 discard 的共同证明下，事务没有可执行 COMMIT 的主体，因此 DB 轴仍为 `NOT_COMMITTED`，但连接绝不 release。若事务曾进入 COMMIT send 状态，状态机禁止再调用 `FinishRollback`，只能保留原 COMMIT 的 result。

`ServerIdle` 而 commit ACK 缺失不能区分“COMMIT 成功”与“COMMIT 失败后 rollback”，所以仍为 `UNKNOWN`。反之，positive commit ACK 即使 ReadyForQuery 丢失，DB 轴仍为 `COMMITTED`，只是连接必须 discard。

### 5.3 ConnectionDisposition 总映射

首先计算 release candidate：

| operation/reply | server status | candidate |
|---|---|---|
| COMMIT + positive ACK | `ServerIdle` | yes |
| COMMIT + definitive rejection | `ServerIdle` | yes；PG deferred error 后 tx 已结束 |
| ROLLBACK + positive ACK | `ServerIdle` | yes |
| 其他任意组合 | 任意 | no |
| 任意 reply | failed/in-transaction/not-observed/unknown/contradictory | no |

candidate=yes 仍必须在同一 physical connection 上依次通过 connector protocol drain、driver state reset、session reset、health ping 和 status recheck；任何一步失败都转 discard。只有全部通过才是 `ConnectionReleased`。

candidate=no 或检查失败时，`Finish*` 必须从 pool hijack/摘除 handle，禁止再借出，然后关闭 physical socket并启动 backend termination reconciliation：

| 物理事实 | disposition |
|---|---|
| release candidate 且 drain/reset/health/status 全部通过，pool transfer CAS 成功 | `RELEASED` |
| pool non-reuse 已保证，且 typed backend exit、DB-side terminate/kill + inventory 对账、或 attested 失活上界后独立检查确认 backend 不存在 | `DISCARDED` |
| 本地 close/hijack 已执行但 backend 终止尚未确认，或确认通道不可用 | `DISCARD_UNCONFIRMED` |

本地 `Close()`、FIN/RST、`driver.ErrBadConn` 或新 connection id 只证明客户端/pool non-reuse，不单独证明服务端 backend 已终止。`DISCARD_UNCONFIRMED` 不是 `DISCARDED` 的日志别名。

### 5.4 `FinishCommit/FinishRollback` 所有权

`FinishCommit`/`FinishRollback` 接收独立 terminal deadline 和唯一 terminal CAS token，并**拥有从 terminal send 到 pool transfer/quarantine 的完整 physical connection 生命周期**。它们内部完成：封住 Execute → terminal frame → typed reply/status → drain/reset/health 或 hijack/close → disposition → connection lease transition。调用者不得在返回后再 `Release`、`Close` 或发送补偿 ROLLBACK。

返回结构至少包含：

```text
operation, DBOutcome, ConnectionDisposition,
write_phase, reply, server_tx_status,
backend_termination_proof, no_commit_ever_sent,
connector_abi, elapsed, evidence_digest
```

任意 connector 只能返回其 ABI 能证明的枚举。无法产生 typed phase 的实现固定返回 `UNKNOWN + DISCARD_UNCONFIRMED|DISCARDED`，且该 datasource capability 不得激活。

## 6. P0-5：单 owner operation、watchdog quiesce 与 forced discard

### 6.1 operation gate

每个 transaction 有一个不依赖 request context 的原子 gate：

```text
OP_IDLE
  └─CAS(request token)────────> OP_EXECUTING(token)
       ├─Execute 正常退出─────> OP_IDLE / OP_ROLLBACK_REQUIRED
       └─watchdog CAS 获胜────> OP_QUIESCING(token, generation)
              ├─Execute 已退出且 protocol synchronized
              │                 └─terminal CAS──> OP_ROLLBACK_OWNER
              └─grace 超时/driver state unknown
                                └─socket seize──> OP_DISCARD_OWNER

OP_ROLLBACK_OWNER / OP_DISCARD_OWNER ──single result──> OP_TERMINAL
```

同一 gate 同时负责 `SESSION_BUSY`、normal return/watchdog 竞态、shutdown 和 double terminal。operation token 包含 transaction generation、ordinal、physical connection generation；ABA token 一律拒绝。

### 6.2 watchdog 唯一协议

legacy HTTP 的 in-flight hard watchdog 默认 300ms。到期后的动作顺序固定：

1. watchdog 以 CAS 把 `OP_EXECUTING` 变为 `OP_QUIESCING`，原子禁止新 operation、commit 和 normal release。
2. 发出 statement cancel。PG 使用独立 CancelRequest 通道/driver cancellation primitive；**不得在正在 Execute 的 tx socket 上并发写 ROLLBACK**。
3. 在 `quiesce_grace=25ms` 内等待 Execute goroutine 明确退出，并取得 connector 的 read-loop ended、无 pending frame、protocol 可继续证明。
4. 若 Execute 未退出、driver 忽略 cancel、read loop/ReadyForQuery 不明确，watchdog 原子夺取 connection lifecycle owner，hijack/关闭 physical socket，进入 discard/quarantine；此路径**不再发送 ROLLBACK**。
5. 只有 Execute 已退出且 connector 明确报告 command boundary synchronized，terminal CAS 才允许发送**一次** typed ROLLBACK。其结果完全走第 5 节总映射。

socket seize 后迟到的 Execute result 只能记录 diagnostics；它不能推进 seq、写普通成功、release connection 或再次 terminal。若 normal Execute completion 先赢 CAS，watchdog 读取完成结果：成功但 deadline 已到时由同一 coordinator owner 进入 rollback-only；失败则走正常 rollback-only，watchdog 本身不并发终结。

在 Execute 阶段从未进入 COMMIT，operation controller 可产生 typed `NoCommitEverSent=true`。forced close 因此可以报告 DB 轴 `NOT_COMMITTED`，同时连接可能是 `DISCARD_UNCONFIRMED`；若 connector/gate 自身状态不可信，则保守升级为 `UNKNOWN`。

HTTP transport cancel 只作为提前触发 quiesce 的信号。2025-06-18 请求间没有可观察的持续连接，仍只承诺 tx idle 15s/tx wall 60s 内 cleanup；不得宣传“HTTP 断连立即 rollback”。stdio EOF 和 graceful shutdown 使用同一 gate：先拒绝新 operation，再 quiesce/forced discard，而不是创建一条旁路 Rollback goroutine。

## 7. P0-3：connection lease、owner loss 与共享 capacity fence

### 7.1 connection lease 独立状态机

connection lease 不从属于 session owner lease 的到期回收逻辑：

```text
IN_USE
  └─terminal/owner-loss/shutdown──> TERMINATING
       ├─typed RELEASED + pool transfer proof────────────> FREE
       ├─typed DISCARDED + backend termination confirmed─> FREE
       └─close sent, backend unconfirmed─────────────────> QUARANTINED

QUARANTINED
  └─reaper DB-side kill / inventory / attested check────> TERMINATING
       └─backend absence + generation CAS────────────────> FREE
```

合法 `FREE` 条件只有：

1. 第 5 节的 typed `RELEASED`，且 physical connection 已原子转交给仍有共享 capacity claim 的 pool；或
2. typed `DISCARDED`，pool non-reuse 与 backend termination 均已确认；或
3. reaper 通过 DB-side `pg_terminate_backend`/等价 kill、独立连接清单对账，确认 exact backend identity 已消失；或
4. datasource 已 attestate 服务端失活上界，等待该上界后仍执行独立 inventory 检查并确认不存在。

owner heartbeat 过期、directory 标记 `owner_lost`、本地 socket close、实例进程消失、DNS/LB 切换都不是 `FREE` 证据。`DISCARD_UNCONFIRMED` 必须保持 `QUARANTINED` 并继续占用 datasource capacity、告警和 reaper 扫描，不能因为 owner/session lease 过期自动返还 token。generation CAS 防止迟到 owner 释放新 lease。

共享记录至少保存：`lease_id/generation, datasource, capacity_parent, claim_class, owner instance/incarnation/epoch, physical/backend identity, connector ABI, state, last evidence digest, deadlines, quarantine reason`。backend identity 对 PG 至少绑定 server identity、database、backend PID 和 backend start/connection nonce，不能只靠可能复用的 PID。

### 7.2 pool、migration、health 的共享 claim

每条可能建立 DB backend 的路径都在启动或扩容**之前**注册共享 claim：

- `POOL_ENVELOPE`：按实例和 pool class 预留容量；实际 `MaxOpenConns <= claimed_capacity`，claim 失败则实例不能启动该 pool/扩大连接数。
- `DIRECT_PINNED`：不在 pool envelope 内的 B5 direct connection，每条占一个 claim；从 dial 前到 confirmed release/discard 持有。
- `MIGRATION`、`HEALTH`、`ADMIN`：显式有界 claim；禁止“偶尔才用”而不计费。

避免双重计费的规则是：pool 内 physical connection 的 child lease 引用其 `POOL_ENVELOPE`，不再加一个 direct unit，但 quarantine 会占住 envelope 内的 slot并禁止 pool 创建替代 backend；pool 外 direct connection 使用 `DIRECT_PINNED` 单独计费。pool connection 在 pinned/released 间的 ownership transfer 必须在同一 envelope 内 CAS。任何代码路径必须二选一，禁止既不计费或重复计费。

```text
sum(all POOL_ENVELOPE claimed_capacity
    + all DIRECT_PINNED units
    + MIGRATION/HEALTH/ADMIN direct units)
  + admin_emergency_reserve
  <= datasource_db_hard_budget
  <= database max_connections - external_reserve
```

缩池时先设置新的本地 `MaxOpenConns`、关闭多余 idle connections并确认 child leases 终结，再释放共享 delta。扩池时顺序相反：共享 CAS 成功后才增大 `MaxOpenConns`。control partition 时不得扩池；现存 claims 按 generation 保守保留，不能推测远端已释放。

### 7.3 owner loss

owner loss 只关闭逻辑 owner，不释放物理预算。reaper 对每个 `IN_USE` connection 先 fencing old instance，转 `TERMINATING`，再优先 DB-side terminate 和 inventory；无法确认则进入 `QUARANTINED`。其他实例不能接管 active tx，也不能在旧 backend 消失前用其 token 建新 backend。status 必须显示 `owner_lost_connection_quarantined`，不得显示“已回滚”。

## 8. continuation capability、幂等顺序与 hard revoke

### 8.1 key derivation、AEAD 和 rotation

`open_session` 一次性返回 256-bit CSPRNG `continuation_secret`。客户端与 owner 均按下式得到 proof key：

```text
salt     = SHA-256(CanonicalEncode("agentsql.b5.session-salt.v1", session_id))
Ksession = HKDF-SHA-256(
             IKM=continuation_secret,
             salt=salt,
             info=CanonicalEncode("agentsql.b5.session-key.v1",
                                  principal_digest, creation_owner_epoch),
             L=32)
```

directory 不保存 secret。它以 KMS 管理的 KEK 和 AES-256-GCM 保存 `Ksession`；每行使用随机 96-bit nonce。AAD 固定包含 schema version、session id、principal digest、datasource id、owner instance/incarnation/epoch 和 KEK id。任一 AAD 变化必须 decrypt-old/CAS-reencrypt，不能复制 ciphertext。

`verifier_digest = HMAC-SHA-256(Kverifier, session_id || Ksession || principal_digest)`，只用于解密后检测错误 key/row 绑定和 rotation 审计，不能单独认证请求，也不能返回客户端。KEK rotation 先发布新 key id，再逐行 decrypt/verify/rewrap；读路径在有界窗口接受 active+previous KEK。rewrap 不改变 `Ksession` 或客户端 secret。旧 KEK 只有在扫描确认无引用、缓存 TTL 结束后才能撤销。

解密后的 key cache 以 `(session_id, owner_epoch, key_id)` 为键，TTL 默认 60s、最大 60s；terminal、owner loss、hard revoke、rotation invalidation、实例 shutdown 都立即 best-effort zeroize 并删除。KMS/key unavailable 时不创建新 session、不 begin；已 ACTIVE transaction 仍受本地 wall watchdog，不能绕过 60s 上界。

proof 为：

```text
HMAC-SHA-256(Ksession,
  CanonicalEncode("agentsql.b5.continuation.v2",
    method, session_id, owner_epoch, request_id,
    expected_seq_tagged, SHA-256(canonical_body_without_proof)))
```

mutation 的 `expected_seq_tagged=Value(n)`；纯 status/get 的值必须是 typed `NotApplicable`，禁止用 0 或空字符串混淆。

### 8.2 idempotency 与 seq 的固定检查顺序

通过 protocol/API auth、directory/proof/principal/current-owner 验证后，顺序固定为：

1. 计算 canonical body digest，以 `(session, method, request_id)` 查询 bounded idempotency cache。
2. 命中且 body digest 相同：直接返回已保存 result/tombstone，即使成功请求已推进 current seq；不得再执行 current-seq check。
3. 命中但 digest 不同：`IDEMPOTENCY_CONFLICT`，固定 `tx_effect=KEEP_ACTIVE`，不执行、不 rollback。
4. 未命中：mutation 才检查 `expected_seq == current_seq`，然后以 CAS 建立 in-flight reservation；纯查询要求 tagged NA。
5. operation 完成后把 body digest、固定 axes/result digest 和 response 保存，再推进 mutation seq；二者由 session operation record 原子发布。

同 request id 的并发 loser 只等待 bounded result或返回 `SESSION_BUSY`，不能执行第二次。

### 8.3 hard revoke 的诚实上界

删除“立即撤销”的承诺。control plane 写入 durable、scope-keyed monotonic revoke feed：`scope, revoke_epoch, event_id, published_at`。owner 按持久 offset 消费；重复/乱序 epoch 取 max，丢消息通过 offset replay/snapshot 恢复。收到更高 epoch 后禁止新 operation，并走第 6 节 quiesce/rollback。

健康路径目标可以低于 5s，但**合同上界是 durable publish 后 60s**：control partition/feed outage 时不允许新 begin；已 ACTIVE transaction 不查询 control DB，而由不可续期的 60s tx wall watchdog 强制终结。进程 pause/owner loss后的真实 backend lock 释放另受第 7 节 quarantine/服务端失活证明约束，不能把“授权不再接受 operation”混写成“锁必在 60s 消失”。

## 9. PostgreSQL DML binder、closure 与显式矩阵

### 9.1 ABI 与 same-tx fence

正式 ABI 为 `agentsql-binder-dml-1`，每个 PG major 独立 build/hash/attestation。它在第 4 节已经 BEGIN 的同一 connection、transaction、backend 上批量返回 command/target、rewrite 数、`(relation_oid,attnum,site,usage=write_target|reference)`、RETURNING usage、object closure、analyzed/manifest/dependency digest、backend/transaction identity、lock manifest 和 invalidation generation。

seal 后 Execute 只接收 `PostgresPreparedDML` 私有 capability，不接收 raw SQL。所有 dependency locks 到 terminal 才释放。未知 PG major/node/dependency/relkind、catalog race、执行后出现未 manifest lock/object均 rollback；后者是安全缺口证据，不是继续执行的动态发现机制。

### 9.2 首发 closure 矩阵

| 对象/形态 | v0.4 行为 | lock / fingerprint / invalidation 合同 |
|---|---|---|
| heap target/reference relation | 最小子集允许 | relation OID lock；relkind/owner/schema/row security/rewrite fingerprint；DDL invalidation 即 rollback |
| plain non-partial、non-expression index | 允许 | 枚举 index OID，显式 lock；fingerprint key attnums、uniqueness、immediacy、access method/opclass/collation；变化使 seal 失效 |
| partial/expression index | 拒绝 | `AUTH_EXPRESSION_CLOSURE_UNSUPPORTED`；不得只锁 index 后忽略 predicate/expression |
| immediate NOT DEFERRABLE PK/UNIQUE，且由允许的 plain index 支撑 | 允许 | constraint+index OID lock/fingerprint；列 grant和 conflict failure纳入 statement result |
| deferrable constraint、exclusion、FK、CHECK（除内建 NOT NULL） | 拒绝 | `AUTH_CONSTRAINT_CLOSURE_UNSUPPORTED`；deferred COMMIT failure仍在 S4 terminal connector 测试 |
| domain type | 拒绝 | `AUTH_TYPE_CLOSURE_UNSUPPORTED`；避免 domain CHECK/cast 隐式执行 |
| cast/operator/function | 只允许 attested PG built-in immutable/无副作用 allowlist | OID + server-major build fingerprint；用户/extension 对象一律拒绝 |
| operator class/collation | 仅 attested `pg_catalog` built-in 与 datasource 固定默认 | OID/version fingerprint并随 index lock封印；用户定义/不确定版本拒绝 |
| default/identity/sequence/generated | 只有所有受影响列显式给值且不存在会触发的对象才允许；否则拒绝 | `AUTH_DEFAULT_CLOSURE_UNSUPPORTED` / `AUTH_EXPRESSION_CLOSURE_UNSUPPORTED` |
| trigger/event trigger | 拒绝 | `AUTH_IMPLICIT_OBJECT_UNCLOSED`；S0 的 `audit_log` 隐式写是必测负例 |
| rule/RLS/security barrier/view target | 拒绝 | `AUTH_REWRITE_CLOSURE_UNSUPPORTED` |
| partition/inheritance/foreign/unlogged/temp | 拒绝 | `AUTH_RELATION_KIND_UNSUPPORTED` |
| whole-row reference、system column、`table.*` | 拒绝 | `AUTH_WHOLE_ROW_UNSUPPORTED`；必须显式 attnum usage |
| CTE/subquery/UPDATE FROM/DELETE USING/RETURNING | gateway 拒绝 | `TX_DML_SHAPE_UNSUPPORTED` / `TX_RETURNING_UNSUPPORTED` |
| AgentSQL control/audit schema或 reserved OID | 无条件拒绝 | `AUTH_INTERNAL_OBJECT_DENIED`；approval 不能覆盖 |

简单 `NOT NULL` 是唯一无需额外对象 closure 的 constraint。任何允许项仍须由 PG14/15/16/17/18 的真实并发 DDL 和 manifest golden 逐版证明。

## 10. P0-4：完整 emergency WAL、事件 schema 与 reconciliation

### 10.1 durability 规则

DB outcome、audit durability 和 connection disposition 始终分别存储。`AUDIT_PENDING` 的充要条件是：primary event 未确认耐久，但 emergency WAL 对**完整同语义事件**的 append 与 `fdatasync/fsync` 已明确成功。只完成 `Write`、fsync 正在运行/超时、descriptor 状态未知、key 不可用或返回前进程崩溃，都只能是 `DURABILITY_LOST`，不能称 pending。

`tx_commit_intent` 必须先在 primary audit store 耐久，才可调用 business COMMIT；emergency WAL 永不替代 commit 授权屏障。intent 失败即 typed rollback。DB 已 positive COMMIT ACK 后，audit/WAL 失败不能改写成 DB unknown，必须是 `COMMITTED + DURABILITY_LOST`。

### 10.2 WAL record 与完整 canonical event

WAL 是独立预分配、mode 0600 的 durability subsystem。每条 record 格式固定为：

```text
RecordHeader {
  magic, wal_format_version, total_length, wal_sequence,
  reservation_id, charged_bytes,
  event_uuid, event_schema_id/version,
  key_id, nonce, ciphertext_length
}
Ciphertext = AEAD_Encrypt(Kwal[key_id], nonce,
                         plaintext=CanonicalEvent,
                         AAD=CanonicalEncode(header_without_crc_and_lengths_that_change))
Trailer { AEAD_tag, CRC32C }
```

CRC32C 用于 torn-tail/长度恢复，不能替代 AEAD。AEAD 固定 AES-256-GCM；nonce 在同 key 下不得复用。key id、rotation 状态和 reservation 都是 record 的 authenticated data。KMS rotation 保留旧 decrypt key 直到所有 segment replay/rewrap/销毁完成；key unavailable 时 begin reservation失败，terminal 后则报告 durability lost 并最高级告警。

`CanonicalEvent` 不是摘要占位符，而是可直接写回 primary 的完整 `agentsql.audit.event.v3`：

```text
event_uuid, event_kind, schema_id/version,
occurred_at, producer_instance/incarnation,
tenant/principal/datasource digests,
session_id_digest,
transaction_id_digest [tagged R/O/NA],
transaction_seq [tagged R/O/NA],
previous_tx_event_digest [tagged R/O/NA],
plan_digest [tagged R/O/NA],
begin_authorization_digest [tagged R/O/NA],
operation_id/ordinal [tagged R/O/NA],
DBOutcome [tagged R/O/NA],
write_phase/reply/server_tx_status [tagged R/O/NA],
ConnectionDisposition [tagged R/O/NA],
AuditDurability, trigger/reason stable enums,
evidence_digest, canonical kind-specific payload,
reservation_id, canonical_payload_bytes/charged_bytes,
event_payload_digest
```

`event_payload_digest = SHA-256(CanonicalEncode(all semantic event fields except the digest itself))`。WAL ciphertext、nonce、wal sequence、primary global-chain position不属于 semantic digest。raw SQL、secret、driver text不进事件；只保留受控字段和 digest。

### 10.3 required / optional / not-applicable

`R` 表示必须有 typed value；`O` 表示 schema 允许 typed `Absent`；`NA` 表示必须编码 `NotApplicable`，不得用空串/0/null 替代。

| event kind | tx id/seq/previous | plan digest | begin auth digest | DB/evidence/connection | kind payload |
|---|---|---|---|---|---|
| `tx_plan_decision` begin 前拒绝/待审批 | NA | R | NA | NA | decisions/manifests/reason R |
| `tx_plan_decision` 已分配 tx | R/R/R（seq=1，previous=genesis） | R | O | NA | decisions/manifests R |
| `tx_begin` | R/R/R | R | R | NA | backend identity digest、dependency digest R |
| `tx_statement` | R/R/R | R | R | DB=NA；statement evidence R；connection=NA | ordinal/action/affected rows/result digest R |
| `tx_commit_intent` | R/R/R | R | R | NA | idempotency/business fact key R |
| `tx_terminal_outcome` | R/R/R | R | R | 全部 R | terminal trigger/result R |
| `tx_rollback` | R/R/R | R | R | DB/evidence/connection R | rollback trigger、no-commit proof R |
| `tx_audit_reconciled` | R/R/R | R | R | original axes O | original EventUUID/digest、WAL seq、attempt R |
| `tx_db_outcome_reconciled` | R/R/R | R | R | DB R；evidence R；connection O | resolver evidence R |
| `session_terminal` 有 tx | R/R/R | R | R | terminal axes R | session reason R |
| `session_terminal` 无 tx | NA | NA | NA | DB/evidence/connection NA；audit R | session reason R |

事务事件的 seq 严格递增且 previous digest 指向前一 canonical event digest；pre-begin denial 和无事务 session 事件不伪造 transaction chain。

### 10.4 reservation 和最坏事件数

canonical event 最大 `16KiB`（含 envelope），超限在任何 business side effect 前拒绝。每个 begin 在占 business connection 前预留固定 `6 × 16KiB = 96KiB` emergency capacity：

1. 一个 in-flight `tx_statement`；
2. 一个 `tx_rollback`；
3. 一个 `tx_terminal_outcome`；
4. 一个 `session_terminal`；
5. 一个 durability diagnostic/recovery marker；
6. 一个版本化 headroom record。

commit path 所需数量不大于此值。primary `tx_commit_intent` 仍须 primary durable，但其后 terminal/session 记录已被 reservation覆盖。每次 Execute 前检查剩余 reservation 能覆盖 `statement + rollback + terminal + session`；否则不执行。reservation 以实际 canonical size计费，未用部分只在 terminal events durable或明确 durability-lost/quarantine 后释放，禁止多个 tx 超卖同一预留空间。

### 10.5 replay、双链与冲突

primary store 以 EventUUID 唯一。replay 遇到已有 UUID 时必须读取并常量时间比较 `event_payload_digest`：相同才是幂等成功；不同则 `AUDIT_EVENT_UUID_CONFLICT`，停止该 reservation/transaction 的自动 replay、保留原 WAL、触发最高级 security/SRE 告警。禁止 `ON CONFLICT DO NOTHING` 静默吞掉内容冲突。

primary 有两个不同的链：

- per-transaction chain 使用 event 内 `transaction_seq + previous_tx_event_digest`，表达业务语义顺序；晚到事件可先进入 staging，但只有 predecessor 连续后才标 verified。
- global ingestion chain 在 primary 真正插入时分配 `global_sequence/global_previous_digest`，覆盖 event payload digest、arrival source 和 replay metadata；它表达耐久接收顺序，不重写 canonical event。

重复 event 不追加第二个 global entry；晚到/乱序 event 可以在 global chain 中较晚出现，但 per-tx verifier 必须恢复连续序列。联合 verifier 同时验证 global 无断链、每 tx 无缺口/分叉、UUID-payload 唯一性。`tx_audit_reconciled` 仅在原 event 进入 primary并完成 per-tx staging/verification规则后追加，不改写历史。

fsync timeout 默认受 250ms audit barrier 限制。超时时不能假装取消正在内核中的 fsync：该 writer/segment 标记 wedged、禁止新 append，reservation 保留供恢复扫描；当前结果固定 `DURABILITY_LOST`。若 fsync 之后迟到成功，只能由恢复流程追加状态证据，不能倒改已返回的结果。

## 11. 锁图、跨 goroutine 结构约束与反边证明

### 11.1 唯一等待图

```text
control snapshot / approval / admission  rank 10
       │ complete transaction and release all control locks
       ▼
business native transaction              rank 20
       │ bounded immutable event append only
       ▼
audit-chain terminal sink                 rank 30

business terminal + connection disposition complete
       ▼ new phase; no rank 20/30 lock retained
final control fence / directory hint
```

`control → business → audit` 是唯一同步方向。final fence 是资源释放后的新 phase。该证明只覆盖 AgentSQL 控制的 wait edge，不宣称外部事务不会造成业务行锁 deadlock。

### 11.2 结构性约束

当前 context-local lockrank tracker 只能做诊断，不能证明 async/group-commit/cleanup 安全。S7 必须同时实现：

- phase capability 类型：`ControlSnapshotDone` 才能构造 `BusinessCapability`；`BusinessEvent` 只可降权复制成 immutable `AuditAppendCapability`，不能恢复 control/business handle。
- audit API 只接受 canonical bytes、reservation 和 deadline；接口不接受 callback、closure、executor、directory client或任意 `func`。
- audit worker 包不能 import business coordinator、directory、approval、admission、final-fence package；用静态 dependency rule和 allowlisted callsite manifest 检查。
- goroutine spawn 必须显式 move phase token；禁止用新 `context.Background()` 重置 rank。cleanup operation 由 transaction-owned atomic state产生 token，而非从 context 推测。
- group commit 在 backend `InsertBatch` 时不持 coordinator mutex；backend transaction只触及独立 audit schema/chain，无 FK/trigger/rule 到 control/business。
- final fence 构造函数要求 terminal result + completed disposition proof，类型上拿不到 live business connection。
- runtime assertion 保留作第二道防线：任何 rank 30 → rank 10/20、rank 20 → rank 10 或持 rank 调 callback 都 panic/fail test。

反边注入测试必须把 audit→control、audit→business、business→control callback分别加到 test double，并证明静态检查或 runtime assertion必失败；async goroutine和独立 cleanup context都要覆盖。真库测试断言 AgentSQL 组件边不产生 `40P01`，外部业务 deadlock则正常导致整笔 rollback。

## 12. 资源默认值、metadata 计费与 cardinality cap

### 12.1 默认值

| resource | default | hard/rule |
|---|---:|---|
| sessions / agent / tenant / cluster | 4 / 32 / 256 | 16 / 128 / 1024，集群级 |
| active tx / agent / tenant / datasource | 1 / 8 / 2 | 4 / 32 / DB claim更小值 |
| datasource connection reserve | admin emergency=2；stateless=`max(1,25% hard budget)` | 每datasource显式配置；先claim后设`MaxOpenConns` |
| statements / tx | 16 | 32 |
| SQL bytes / statement | 256KiB | hard |
| canonical active plan / tx | 1MiB | hard，含 SQL/reason/labels/encoding/metadata charge |
| active plan bytes tenant/datasource/cluster | 8/32/64MiB | 32/128/256MiB |
| affected rows / tx | 10,000 | policy/datasource可更小 |
| idempotency cache / session | 64 entries / 256KiB | 128 / 512KiB；大 response只存 digest |
| session idle / absolute | 10min / 60min | 30min / 60min |
| tx idle / wall | 15s / 60s | 30s / 60s，不可续期 |
| B5 statement | datasource更小值，最多5s | legacy in-flight另受300ms watchdog |
| watchdog quiesce grace | 25ms | hard 50ms；仍受300ms operation deadline |
| PG terminal local budget | 150ms + 25ms scheduler margin | server termination另计、未确认则 quarantine |
| audit barrier | 250ms | enqueue25 + backend200 + adapter25；取 remaining wall更小值 |
| final fence | 250ms | 只重试 fence，不重复 DB terminal |
| emergency WAL reservation / tx | 96KiB | 6 × 16KiB |
| tombstone TTL / entries | 15min / 50,000 | TTL 30min、entries 50,000 |
| tombstone charged bytes cluster | 128MiB | hard 256MiB |
| tombstone/session churn | principal 120/min；tenant 2,000/min；global 200/s | open/close/expire都计费 |
| principal/tenant limiter bucket cardinality | 10,000 / 2,000 | hard 50,000 / 10,000；idle TTL 30min |

所有预算取 `min(configured, remaining tx wall)`；重试不能跨过 wall deadline。S0 latency 只作 regression baseline，不固化为跨环境 SLO。

### 12.2 metadata charge

quota 不能把 Go heap struct 大小或 256B 常数当真实占用。directory row、idempotency entry、lease、tombstone、limiter bucket统一计费：

```text
charge_bytes = round_up_256(
  canonical_serialized_bytes
  + 512B row/allocator headroom
  + 3 * 256B index headroom
  + 256B cache/accounting headroom)
```

若实际 backend 有更多索引，按更大声明数计费；观测到 p99 actual allocation/row size超过 charge 的 75% 即阻止扩灰并上调。典型 compact tombstone 的 charge约 2KiB，而不是沿用 S0 的 131B heap或 v2 的256B。tombstone只存 id digests、terminal enums、final seq、owner epoch、event digest和 expiry；禁止引用 plan/SQL/reason/rows/response/secret。

principal/tenant token bucket 本身受上表 cardinality cap、TTL 和 metadata bytes总账约束。新高基数 principal 在 cap 满时进入不分配新状态的共享 deny bucket并返回 `RATE_LIMIT_STATE_EXHAUSTED`；不能通过随机身份制造无限 map/row。active plan、WAL reservation、tombstone、connection quarantine分别记账，禁止终结时把未释放项漏算。

## 13. MySQL 量化 gate（v0.4 始终 off）

以下只是未来按 `datasource + exact server patch + network profile` 的解锁门；失败不阻塞 PG：

1. 建连后设置并用同 session `SELECT @@SESSION...` 与 performance schema（有权限时）读回 `wait_timeout`、`interactive_timeout`、`net_read_timeout`、`net_write_timeout` 和 vendor/patch 提供的 idle-in-transaction/TCP失活机制。没有可证明的 active transaction失活机制、权限不足或读回不一致即 gate fail；不得虚构 MySQL 不支持的变量。
2. custom connector 在 terminal watchdog 150ms + 本地25ms margin内返回；每个 send/ACK fault window 10,000 次注入中 max 不得超过250ms、p99.9不得超过175ms，且0 misclassification、0 pool reuse。
3. cross-host client→server、server→client 单向黑洞、half-open proxy、NAT reset、DB kill/failover每项至少100次；从故障注入到独立 `performance_schema`/连接清单确认 backend和其 transaction locks消失的上界为5s。任一超过5s、无法观测或确认通道也分区，claim留 quarantine且 gate fail。
4. MySQL 8.0 产品最低/最新 patch与8.4声明 patch分别通过 binder/MDL closure、四 terminal窗口、server timeout和network矩阵；连续24h fault soak无 orphan/claim漂移。

明确失败阈值：任何 commit ACK-loss被误报 NOT_COMMITTED、任何故障连接复用、任何 backend/lock >5s 未消失、任何 timeout attestation 漂移、任何 unbounded terminal call，都保持 `DIALECT_TRANSACTION_UNSUPPORTED`。S0 8.4.11 的150–169ms本地结果和 connection id变化只作回归下界，不证明服务端释放。

## 14. MCP/API、稳定错误码与固定 effect

### 14.1 response 和 effect schema

terminal response 固定返回：

```json
{
  "state": "committed_audit_pending",
  "db_outcome": "committed",
  "audit_durability": "audit_pending",
  "connection_disposition": "released",
  "retry_same_request": false,
  "new_transaction_allowed": false,
  "tx_effect": "terminal_committed",
  "terminal_event_digest": "..."
}
```

公共错误不返回 backend PID、raw driver text、secret、对象存在差异。`retry_same_request` 只表示可用**同 request id、同 body**安全重取/重试；它从不授权自动重放 write/COMMIT。`new_transaction_allowed` 只表示当前调用的 DB outcome 已允许调用者在重新规划/授权后新建 transaction，不表示旧 request 可复用。

`tx_effect` 是固定 enum：

```text
NO_TX_CHANGE | KEEP_ACTIVE | MARK_ROLLBACK_ONLY |
TERMINAL_NOT_COMMITTED | TERMINAL_COMMITTED | TERMINAL_UNKNOWN |
SESSION_TERMINAL | FENCE_ONLY
```

### 14.2 稳定错误合同

| code | retry same | new tx allowed | fixed tx_effect |
|---|---:|---:|---|
| `MCP_PROTOCOL_UNSUPPORTED` | false | false | `NO_TX_CHANGE` |
| `SESSION_PROOF_REQUIRED` | false | false | `NO_TX_CHANGE` |
| `SESSION_NOT_FOUND_OR_DENIED` | false | false | `NO_TX_CHANGE` |
| `SESSION_OWNER_EPOCH_STALE` | false | false | `NO_TX_CHANGE` |
| `SESSION_WRONG_INSTANCE` | true | false | `NO_TX_CHANGE` |
| `SESSION_ROUTE_UNAVAILABLE` | true | false | `NO_TX_CHANGE` |
| `SESSION_OWNER_LOST` | false | false | `SESSION_TERMINAL` |
| `SESSION_BUSY` | true | false | `KEEP_ACTIVE` |
| `SESSION_LIMIT_EXCEEDED` | true | true | `NO_TX_CHANGE` |
| `SESSION_EXPIRED` / `SESSION_TERMINAL_RECORD_EXPIRED` | false | true | `SESSION_TERMINAL` |
| `SESSION_RENEW_DURING_TX` | false | false | `KEEP_ACTIVE` |
| `IDEMPOTENCY_REQUIRED` | false | false | `NO_TX_CHANGE` |
| `IDEMPOTENCY_CONFLICT` | false | false | `KEEP_ACTIVE` |
| `RATE_LIMIT_STATE_EXHAUSTED` | true | false | `NO_TX_CHANGE` |
| `TX_NOT_ACTIVE` / `TX_ALREADY_ACTIVE` | false | false | `NO_TX_CHANGE` |
| `TX_ID_MISMATCH` | false | true | `MARK_ROLLBACK_ONLY` |
| `TX_PLAN_REQUIRED` / `TX_PLAN_MISMATCH`（begin前） | false | true | `NO_TX_CHANGE` |
| `TX_ACTIVE_PLAN_MISMATCH` | false | true | `MARK_ROLLBACK_ONLY` |
| `TX_APPROVAL_REQUIRED` | false | true | `NO_TX_CHANGE` |
| `TX_APPROVAL_EXPIRED` / `TX_APPROVAL_PLAN_MISMATCH` | false | true | `NO_TX_CHANGE` |
| `TX_APPROVAL_CONSUMED_BEGIN_FAILED` | false | true | `TERMINAL_NOT_COMMITTED` |
| `TX_BEGIN_MANIFEST_MISMATCH` | false | true | `TERMINAL_NOT_COMMITTED` |
| `TX_PLAN_INCOMPLETE` | false | false | `KEEP_ACTIVE` |
| `TX_ROLLBACK_ONLY` | false | true | `MARK_ROLLBACK_ONLY` |
| `TX_MASK_UNSUPPORTED`（begin前） | false | true | `NO_TX_CHANGE` |
| `TX_SELECT_UNSUPPORTED` / `TX_RETURNING_UNSUPPORTED`（active） | false | true | `MARK_ROLLBACK_ONLY` |
| `TX_CONTROL_STATEMENT_DENIED` / `TX_DML_SHAPE_UNSUPPORTED`（begin前） | false | true | `NO_TX_CHANGE` |
| `TX_IDLE_TIMEOUT` / `TX_MAX_DURATION` / `TX_STATEMENT_TIMEOUT` | false | true | `TERMINAL_NOT_COMMITTED` |
| `TX_OPERATION_WATCHDOG` | false | true | `TERMINAL_NOT_COMMITTED` |
| `TX_OPERATION_WATCHDOG_OUTCOME_UNKNOWN` | false | false | `TERMINAL_UNKNOWN` |
| `TX_LIMIT_EXCEEDED` / `PLAN_BYTE_LIMIT_EXCEEDED`（begin前） | true | true | `NO_TX_CHANGE` |
| `TX_EXECUTION_LIMIT_EXCEEDED` | false | true | `MARK_ROLLBACK_ONLY` |
| `TX_COMMITTED_AUDIT_PENDING` | false | false | `TERMINAL_COMMITTED` |
| `TX_DB_OUTCOME_UNKNOWN` / `TX_DB_OUTCOME_UNKNOWN_AUDIT_PENDING` | false | false | `TERMINAL_UNKNOWN` |
| `TX_NOT_COMMITTED_AUDIT_PENDING` | false | true | `TERMINAL_NOT_COMMITTED` |
| `TX_ROLLBACK_UNCONFIRMED` | false | true | `TERMINAL_NOT_COMMITTED` |
| `AUDIT_BARRIER_UNAVAILABLE_BEFORE_TX` | true | true | `NO_TX_CHANGE` |
| `AUDIT_STATEMENT_BARRIER_FAILED` | false | true | `MARK_ROLLBACK_ONLY` |
| `AUDIT_COMMIT_INTENT_FAILED` | false | true | `TERMINAL_NOT_COMMITTED` |
| `TX_COMMITTED_AUDIT_DURABILITY_LOST` | false | false | `TERMINAL_COMMITTED` |
| `TX_NOT_COMMITTED_AUDIT_DURABILITY_LOST` | false | true | `TERMINAL_NOT_COMMITTED` |
| `TX_DB_OUTCOME_UNKNOWN_AUDIT_DURABILITY_LOST` | false | false | `TERMINAL_UNKNOWN` |
| `AUDIT_EMERGENCY_WAL_UNAVAILABLE`（begin前） | true | true | `NO_TX_CHANGE` |
| `AUDIT_EVENT_UUID_CONFLICT` | false | false | `FENCE_ONLY`；人工处置，不改既有DB axis |
| `FINAL_FENCE_PENDING_COMMITTED` | true（仅查询/fence） | false | `FENCE_ONLY` |
| `FINAL_FENCE_PENDING_NOT_COMMITTED` | true（仅查询/fence） | true | `FENCE_ONLY` |
| `FINAL_FENCE_PENDING_UNKNOWN` | true（仅查询/fence） | false | `FENCE_ONLY` |
| `AUTH_DML_BINDER_REQUIRED` / `AUTH_DML_ACTION_MISSING` / `AUTH_DML_COLUMN_GRANT_MISSING`（begin前） | false | true | `NO_TX_CHANGE` |
| `AUTH_IMPLICIT_OBJECT_UNCLOSED` / `AUTH_*_CLOSURE_UNSUPPORTED`（begin前） | false | true | `NO_TX_CHANGE` |
| `AUTH_INTERNAL_OBJECT_DENIED` / `AUTH_WHOLE_ROW_UNSUPPORTED` | false | true | `NO_TX_CHANGE` |
| `AUTH_CATALOG_RACE`（active） | false | true | `MARK_ROLLBACK_ONLY` |
| `TX_COMMITTED_CONNECTION_QUARANTINED` | false | false | `TERMINAL_COMMITTED` |
| `TX_NOT_COMMITTED_CONNECTION_QUARANTINED` | false | true | `TERMINAL_NOT_COMMITTED` |
| `TX_UNKNOWN_CONNECTION_QUARANTINED` | false | false | `TERMINAL_UNKNOWN` |
| `DIALECT_TRANSACTION_UNSUPPORTED` | false | false | `NO_TX_CHANGE` |

`TX_OPERATION_WATCHDOG` 和 `TX_ROLLBACK_UNCONFIRMED` 仅在 typed `NoCommitEverSent` 成立时可使用；否则必须使用 `TX_OPERATION_WATCHDOG_OUTCOME_UNKNOWN` 或 `TX_DB_OUTCOME_UNKNOWN`。每个稳定 code 的两个 boolean 和 effect 都由上表唯一确定，调用点不得覆盖。

不再对外使用二义的通用 `AUDIT_UNAVAILABLE`。commit 前分别使用 barrier/intent code；known commit 后使用 `TX_COMMITTED_AUDIT_PENDING` 或 `TX_COMMITTED_AUDIT_DURABILITY_LOST`；DB unknown 则使用对应的 unknown 专用 code。

## 15. 更新后的实现切片与依赖 DAG

所有 slice 只在 flags off 下合入。S1 可以先建 canonical encoder/migration骨架，但 terminal/WAL/binder相关 schema 必须标 `provisional`，直到上游 contract回填后才能 freeze。

```text
S4 terminal contract ─┐
S5 binder contract ───┼──> 回填并冻结 S1 contract/schema
S7 WAL/event contract ┘

S3 + S4 + S5 + S7 ───────> S6 coordinator
S2 + S6 ─────────────────> S8 MCP tools/final fence
S3 + S7 + S8 ────────────> S9 operations/docs/canary controls
S1 + S2 + ... + S9 ──────> S10 activation gate
```

### S1：canonical contract 与 schema（Architecture + Security）

- 冻结 plan/begin/event digest、typed absence、terminal总表、error/effect、JSON与migration。
- 出口：跨 Go version/arch golden；字段 reorder/Unicode/NA；migration upgrade/rollback；flags off。

### S2：pre-SDK gate、continuation 与 revocation feed（MCP + Security）

- exact protocol allowlist、bounded replay、HKDF/HMAC、AEAD/rotation/cache、idempotency-before-seq、durable revoke consumer。
- 出口：proof steal/replay/substitution、KMS rotation/outage、三协议、乱序/丢 revoke；60s诚实上界。

### S3：directory、connection claim 与 quarantine（Runtime/SRE）

- owner epoch/incarnation、sticky route、session/plan/tx claims、pool envelope/MaxOpenConns、connection lease独立状态机、metadata/cardinality cap。
- 出口：owner pause/GC/hang/crash、lease ABA/clock skew/control partition；backend未消失不返 token；50k tombstone与高基数主体攻击。

### S4：typed terminal 和 physical lifecycle（DB Core）

- PG14–18 protocol phase adapter、总函数、Finish lifecycle、seize/close/backend confirmation；MySQL仅实验/off。
- 出口：第16.1矩阵每格、untyped fallback、reset/health/status、pool non-reuse、race/fuzz。

### S5：PG DML binder ABI（PostgreSQL + Security）

- native-BEGIN-first、same-tx bind/closure/lock/seal、显式 closure矩阵、DML action/column grants。
- 出口：五个PG major独立hash/attestation、DDL race、reserved schema、未知对象fail closed。

### S6：planned coordinator、approval 与 operation CAS（Pipeline + Architecture）

- plan/approval CAS、begin状态机、dependency lifetime、single-operation CAS、quiesce/forced discard、rollback-only。
- 出口：approval mutation/重复消费/begin fail；watchdog/normal race；无 double terminal或并发协议写。

### S7：audit WAL、双链与 lock structure（Audit/B6 + Runtime/SRE）

- 完整 canonical record、AEAD/key rotation、6-record reservation、fsync semantics、UUID conflict、双链 verifier、rank capability/analyzer。
- 出口：第16.3矩阵、反边注入、node recovery、capacity/rotation runbook。

### S8：MCP tools 与 final fence（MCP + Pipeline）

- B5 tool schema、axes response、idempotent terminal/status、stdio/HTTP lifecycle、release后final fence。
- 出口：disconnect/EOF/shutdown、response loss、重复 terminal、fence timeout、旧七工具兼容和secret/SQL泄漏扫描。

### S9：运维面与灰度（Runtime/SRE + Docs/UI）

- owner/connection quarantine、audit pending/lost、unknown resolver、claim/WAL/feed metrics、RBAC处置和runbook。
- 出口：stale directory不显示 rollback；kill/对账 proof；canary stop rules和灾难演练。

### S10：真库/真网络 activation gate（QA + 全体 owner）

- 运行第16节全部矩阵并留证；先 internal canary再逐 datasource PG。
- 任一未解释 `UNKNOWN`、`DISCARD_UNCONFIRMED`、chain gap、claim drift 或 AgentSQL反边 `40P01` 阻止扩灰。MySQL预期始终 unsupported。

## 16. 真库、真网络与 fault-window 测试矩阵

### 16.1 PostgreSQL 14–18 terminal release-blocking matrix

每个 PG14/15/16/17/18、TLS on/off（生产使用项必须含 on）、direct/proxy路径都执行。fault proxy/connector必须记录精确应用层 write count、typed backend reply和ReadyForQuery byte；旁路观察只验真，不反向修改被测分类。

| operation / fault window | typed evidence | expected DBOutcome | expected connection |
|---|---|---|---|
| COMMIT before connector send permit | not-sent + missing | `NOT_COMMITTED` | discard；backend确认后 `DISCARDED`，否则 unconfirmed |
| COMMIT write invoked、0 bytes | zero + missing | `NOT_COMMITTED` | discard，never release |
| COMMIT partial frame | partial + missing/reset | `UNKNOWN` | discard/unconfirmed |
| COMMIT full frame、ACK全丢 | full + missing | `UNKNOWN` | discard/unconfirmed |
| COMMIT write accounting indeterminate | indeterminate + missing | `UNKNOWN` | discard/unconfirmed |
| COMMIT `CommandComplete` + RFQ `I` | positive ACK + idle | `COMMITTED` | reset/health/status全过才 `RELEASED` |
| COMMIT positive ACK、RFQ丢失 | positive ACK + not observed | `COMMITTED` | discard/unconfirmed |
| COMMIT positive ACK + RFQ `T/E`（注入矛盾） | positive ACK + contradictory | `COMMITTED` | discard；协议 invariant 告警 |
| deferred UNIQUE/FK constraint在COMMIT失败 + RFQ `I` | definitive rejection + idle | `NOT_COMMITTED` | checks全过可 `RELEASED` |
| COMMIT ErrorResponse、RFQ丢失 | definitive rejection + not observed | `NOT_COMMITTED` | discard/unconfirmed |
| ACK missing但只观察到RFQ `I` | missing + idle | full/partial时 `UNKNOWN` | discard；不得由 idle 猜结果 |
| ROLLBACK before-send / zero / partial / full ACK-loss | no-commit fence +对应phase | `NOT_COMMITTED` | discard/unconfirmed；从不release |
| ROLLBACK `CommandComplete` + RFQ `I` | rollback ACK + idle | `NOT_COMMITTED` | checks全过才 `RELEASED` |
| ROLLBACK ACK + RFQ `T/E` | rollback ACK + contradictory | `NOT_COMMITTED` | discard |
| rollback connector不能产生typed phase | untyped | `UNKNOWN` | discard；capability不得激活 |
| positive terminal ACK后reset/health/status任一步失败 | known DB axis | 保持known outcome | confirmed/unconfirmed discard |
| proxy reset / client half-close /单向blackhole | 各send/ACK窗口 | 按总表 | pool non-reuse；backend disappearance单独证明 |
| PG restart / primary failover / switchover | 各send/ACK窗口 | known ACK则known，否则按phase unknown | discard/unconfirmed；旧backend inventory对账 |
| client进程在DB ACK后、audit前崩溃 | durable intent，无outcome event | resolver不得猜；旁路对账后追加事实 | crash connection claim quarantine/对账 |

每例还必须断言：terminal frame最多一次；故障 physical connection从不再次借出；pool metrics/connection identity无复用；connection claim只在第7节证明后归还；deferred failure不是 commit unknown。

### 16.2 BEGIN、watchdog、MCP 与 shutdown

| case | required assertion |
|---|---|
| preflight后并发DDL | native BEGIN在前；same-tx locks阻止DDL或actual manifest比较失败并typed rollback；approval=`consumed_begin_failed` |
| bind/closure/seal/compare任一步失败 | 不到ACTIVE；dependency直到Finish返回；无connection/token泄漏 |
| DB lock-wait Execute | 300ms watchdog freeze；CancelRequest；25ms内退出才typed rollback，否则seize/close且不发ROLLBACK |
| slow statement | 与lock-wait相同；迟到Execute不得推进seq/返回成功 |
| driver忽略 request cancel | grace到期forced discard；无并发协议write；connection quarantine |
| HTTP断连 2025-06-18 | in-flight由独立watchdog处理；请求间只验15s idle/60s wall，不声称立即rollback |
| watchdog vs normal return逐指令race | 两个CAS胜序都跑；恰好一个terminal owner、一个result、零double release |
| stdio EOF / graceful shutdown | stop admission → freeze operations → quiesce/discard/typed rollback → WAL fsync → disposition → fence |
| 从未发送COMMIT的forced discard | typed `NoCommitEverSent`；`NOT_COMMITTED + DISCARDED/UNCONFIRMED`，若gate证据损坏则UNKNOWN |
| protocol 2025-11-25/2026-07-28/未知 | 均在SDK前拒绝；header/body/meta冲突拒绝；只allow 2025-06-18 |

### 16.3 Audit/WAL release-blocking matrix

- primary outage：`tx_begin` barrier前、statement side effect后、commit intent前、intent durable后/COMMIT前、positive DB ACK后、unknown commit后分别注入。
- WAL fault：record write前、partial/torn tail、write完成/fsync前、fsync hang、250ms fsync timeout、fsync明确失败、disk full、segment rotation、node restart。
- key fault：active encrypt key unavailable、旧 segment decrypt key unavailable、rotation中crash、wrong key/AAD、nonce reuse detector。
- identity fault：相同 EventUUID/相同 payload重复；相同UUID/不同payload高危冲突；不同UUID/同payload合法性；reservation id/charge篡改。
- replay：晚到、乱序、重复、predecessor缺失、per-tx分叉；同时验证global ingestion chain和per-tx chain；重复不得追加global entry。
- DB ACK后立即kill -9：intent必须存在；outcome缺口由resolver明确显示，不能默认committed/rolled back；connection claim进入quarantine直到backend对账。
- 容量：每tx 6-record reservation、并发耗尽、event接近16KiB、terminal后释放、wedged fsync不超卖。

硬断言：只有primary durable或WAL fsync confirmed可得到 `DURABLE/AUDIT_PENDING`；fsync timeout/key unavailable必为 `DURABILITY_LOST`。UUID内容冲突停止自动replay并保留原始证据。

### 16.4 Owner、claim、closure 与锁图

- 四/八实例：open/begin/close、owner唯一、wrong owner no-touch、owner epoch/incarnation ABA、clock skew。
- owner pause、GC stall、hang、crash，control partition但business DB可达；owner lease过期时backend未消失则token保持占用。
- pool envelope在实例启动/扩池前CAS；`MaxOpenConns`绝不超过claim；缩池先关闭/确认；migration/health/admin都计费。
- DB-side terminate成功/失败、inventory不可用、attested timeout到期；只有backend absence使quarantine→free。
- stolen id/secret、同API key不同client、stale proof、body substitution、KEK rotation/KMS outage；均不得伤害受害事务。
- PG每major closure：plain/partial/expression index、immediate/deferrable unique、exclusion/FK/CHECK、domain/cast/operator/opclass/collation、whole-row/system column、trigger/default/generated/RLS/rule/view/partition/UDF、reserved schema、concurrent DDL。
- control→business→audit、release→final fence；async batch/cleanup context；注入audit→control/audit→business/business→control反边时静态或runtime assertion必须失败。AgentSQL组件边不得产生`40P01`。

### 16.5 MySQL 实验矩阵的外部期望

实验层复跑8.0/8.4的before-send、zero/partial/full write、positive/rejected ACK、ACK loss、client→server/server→client单向黑洞、proxy/NAT half-open、DB failover、backend/lock disappearance、timeout attestation和24h claim soak。

但在所有上述测试期间，MCP/API层的期望始终是：

```text
DIALECT_TRANSACTION_UNSUPPORTED
b5_tx_mysql == off
no business connection acquired
```

四类 gate全绿后也必须另开设计复审和负责人签收，不能在v0.4配置热开。

### 16.6 S0 证据登记（仅回归基线）

| S0 probe | 已被v3吸收的事实 | 尚未证明/不得外推 |
|---|---|---|
| `mysql-terminal.go` | MySQL 8.4.11四窗口；本地约150–169ms；ACK loss必须UNKNOWN；`ErrBadConn`后connection id变化 | 单向黑洞的服务端锁释放、全部patch、生产SLO |
| `mcp-cancel.go` / `mcp-watchdog-pg.go` | 2025-06-18/2025-11-25断连不cancel，2026-07-28会cancel；未知initialize可被SDK降级；300ms独立timer可触发PG rollback | 未证明Execute仍in-flight时并发Rollback安全；v3因此要求quiesce/forced discard |
| `pg-dml-binder.go` | PG14/18 same PID、write/reference、RETURNING技术可行、OID lock阻止DDL；trigger暴露未闭合写 | 正式ABI、PG15–17、完整closure、对外RETURNING |
| `audit-boundary.go` | intent outage fail closed；known commit + audit outage是pending；约120B/record、1.532ms/fsync；同UUID重复可幂等 | v2 record不足以重建；disk hang/key loss/UUID内容冲突/生产介质SLO |
| `quota-tombstone.go` | 进程内limit被4x绕过；共享session/plan/connection admission有效；compact heap约131B/item | owner-loss backend回收、metadata/index真实charge、跨AZ热点 |
| `lock-order.go` | 反边约1006ms触发`40P01`；统一方向约252ms得到`55P03` | context-local rank不能覆盖async/cleanup；v3增加结构性capability |
| `latency.go` | PG14/18 begin/rollback与commit、MySQL、watchdog开销的同机方向性基线 | Docker Desktop loopback数据不是release SLO |

S0 从未证明 PG terminal send/ACK fault windows、owner loss后的backend消失或in-flight Execute可安全并发rollback；这些项目明确留在S4/S6/S10 release-blocking矩阵，不能因“设计关闭”跳过。

## 17. 按角色签收清单

| role | activation前必须签收 |
|---|---|
| Architecture | BEGIN唯一顺序、terminal总函数、connection fence、slice DAG、final fence、原理边界 |
| Product | planned-only；tx内SELECT/RETURNING/复杂DML拒绝；legacy 300ms；hard revoke诚实60s；PG-first/MySQL off |
| Security | digest/typed absence、HKDF/HMAC、AEAD/AAD/KMS rotation、idempotency顺序、防枚举、reserved schema、closure矩阵 |
| PostgreSQL owner | PG14–18 ABI/hash、same tx/backend、dependency lifetime、terminal phase/RFQ evidence、deferred-COMMIT failure、DDL race |
| MCP owner | pre-SDK 2025-06-18 allowlist、operation CAS/quiesce、HTTP/stdio/shutdown准确语义、无double terminal |
| Runtime/SRE | directory/route、owner loss、pool claim/MaxOpenConns、quarantine/reaper、metadata/cardinality、KMS/WAL/pool runbook |
| Audit/B6 | 完整canonical event、R/O/NA、AEAD/fsync、6-record reservation、UUID conflict、双链/replay、sink无callback |
| Control/Fence | approval CAS与consumed_begin_failed、durable revoke feed、无business→control反边、release后final fence |
| QA/Release | 第16节真库/真网络证据、负属性、canary stop；任何未知/未确认增长有解释且有owner |
| MySQL owner | v0.4只签off/unsupported；未来量化gate与独立复审 |

## 18. 仍需负责人拍板的产品/部署参数

以下不再改变五个P0的安全合同，但在实现或activation前必须给出环境决策：

1. Product/Architecture 是否接受 legacy HTTP active tool call 的300ms hard上限；不接受只能先qualify新协议/transport，不能删除watchdog。
2. Product 是否接受v0.4事务内SELECT和对外RETURNING均拒绝。
3. Security/Product 是否接受hard revoke的诚实合同上界为60s，而非“立即”；物理锁释放可能因quarantine更久。
4. Audit/SRE 是否接受本地WAL不覆盖节点+本地盘共同永久丢失；若要跨故障域同步durability需独立项目。
5. Runtime/SRE 为每个datasource填写`db_hard_budget`、external/admin reserve、每实例pool envelope、quarantine告警阈值；不得沿用相同per-instance ConnLimit。
6. SRE/Storage 根据目标介质实测决定250ms audit barrier是否可满足；不能用S0的1.532ms当生产SLO。
7. MySQL/Architecture 是否另立未来版本目标；v0.4没有开启路径。

## 19. 原理边界与最终放行

- business DB与audit store没有原子提交；primary intent + emergency WAL + reconciliation不是XA。
- COMMIT frame可能已到服务端而ACK丢失时，正确答案只能是`DB_OUTCOME_UNKNOWN`；禁止自动重试。
- positive DB ACK后的本地审计灾难不能撤销已提交事实；只能准确报告audit durability。
- legacy stateless HTTP在两请求间无session transport close事件，只能靠idle/wall deadline。
- active native transaction不能迁移；owner crash/网络黑洞中的backend终止必须由quarantine fence证明。
- 本地socket close不证明服务端锁释放；本地WAL fsync也不覆盖节点和盘共同丢失或设备谎报durability。
- 通用SQL的隐式对象闭包不能靠表名匹配近似；未被正式ABI覆盖的形态只能拒绝。
- lock DAG只证明AgentSQL控制的边无环；外部事务仍可能造成业务deadlock，处理方式是整笔rollback。
- strong directory/admission/KMS不可用时B5牺牲可用性并fail closed。

**实现准入结论：可以进入 S1–S9 的全 flag-off 实现。** 这不是生产启用许可。只有S10的PG14–18真库/真网络矩阵、上述角色签收、datasource参数、internal canary和停止条件全部完成，`b5_sessions`与`b5_tx_postgres`才可逐datasource开启。`b5_tx_mysql`在独立未来复审前始终off/unsupported。

在此之前，对外事实不变：MCP单call单语句、HTTP stateless、B2 SELECT在事务外、没有可用的跨请求生产事务。

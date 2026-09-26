# AgentSQL v0.4 B5：MCP 多语句事务与跨请求会话——详细设计 v4

状态：**设计 GO（仅限全 feature-flag-off 实现）；生产激活仍禁止**  
日期：2026-09-25（Asia/Shanghai）  
代码基线：`feature/v0.4`，`HEAD=9cb14511674ff3dd580bdf710941085fab63bdc9`  
输入：`b5-mcp-transaction-design-v3.md`、`b5-v3-review.md`、`b5-s0-findings.md`。  
约束：本文只冻结设计合同；未修改生产代码，不代表实现、真库证据、owner 签收或 canary 已完成。

## 0. v4 裁决与 v3 评审关闭表

B5 v0.4 继续 **PostgreSQL-first**。`b5_sessions`、`b5_tx_postgres`、`b5_tx_mysql` 默认且迁移后仍为 off；MySQL 在 v0.4 MCP/API 层固定返回 `DIALECT_TRANSACTION_UNSUPPORTED`，不得取得业务连接。v4 允许从合同切片开始全 flag-off 实现，但不是生产放行。

| v3 review | v4 冻结合同 | 设计结论 |
|---|---|---|
| P0-1 terminal 矛盾证据 | 在 DBOutcome/ConnectionDisposition 之前增加 `EvidenceConsistency` 总校验；矛盾短路，COMMIT=`UNKNOWN`，ROLLBACK 仅凭独立可信 `NoCommitEverSent` 保留 `NOT_COMMITTED`；一律禁止 `RELEASED` | **关闭** |
| P0-2 PG late CancelRequest | 标准 PG CancelRequest 一旦实际发出或不能证明零字节发出，physical connection 永久 cancel-tainted：forced discard，不发 ROLLBACK、不做同连接 reset/health；只有 generation-bound confirmed cancel fence 才可例外 | **关闭** |
| P0-3 WAL 字节模型/自指 fsync | reservation 按最大 framed record 计费并由事件状态机证明最多 6 条；从 canonical 业务事件删除自身 `AuditDurability`，以 append receipt、响应时派生值和 recovery event 表达 | **关闭** |
| P1-1 begin cleanup | 冻结未取连接/已 pin 未确认 BEGIN/已 BEGIN 三态 cleanup 表，保留 `consumed_begin_failed` | **吸收** |
| P1-2 pool owner-crash | 每个 backend 有 durable child identity；只释放/转移确定未使用 delta，未对账 child 继续占 claim | **吸收** |
| P1-3 slice 隐含环 | 拆为 S4a/S5a/S7a 合同 → S1b schema → S4b/S5b/S7b 实现 | **吸收** |
| P1-4 S10 定量门禁 | 冻结正常 UNKNOWN=0、fault 格子、quarantine 数量/年龄/占比、inventory 停止时间、claim drift=0 和人工恢复权限 | **吸收** |
| P2 | 统一 disposition ABI；nonce crash/rotation 不复用证明；60s 是授权上界；全部 typed proof 版本化 | **吸收** |

“关闭”只表示本文给出唯一且保守的设计映射。typed PG adapter、cancel fault proxy、WAL 介质、quarantine reaper 和 S10 矩阵没有实证前，任何生产 flag 都不得开启。

## 1. GA 范围与固定拒绝面

### 1.1 首发允许

- 显式 AgentSQL logical session；每 session 最多一个 active transaction。
- 完整计划在事务外 preflight，所需审批在占用业务连接前完成。
- PostgreSQL 14–18 上经正式 DML ABI 封印的极小 `INSERT/UPDATE/DELETE` 子集。
- 每个 MCP operation 一条顶层 SQL；多个 write operation 按计划顺序、跨 MCP 请求在同一 native transaction/backend 上执行。
- B2 `query`/SELECT 仅在事务外继续走既有 delivery seal，不进入 B5 native transaction。

### 1.2 首发固定拒绝

- stacked SQL、SQL script、客户端事务控制语句、SAVEPOINT/RELEASE。
- active B5 transaction 内 SELECT；返回 `TX_SELECT_UNSUPPORTED` 并标记 rollback-only。
- 对外 DML `RETURNING`；binder 可识别其 usage，但 gateway 固定拒绝。
- DDL、ADMIN、MERGE、UPSERT、CTE、子查询、`UPDATE ... FROM`、`DELETE ... USING`、COPY/LOAD、CALL/DO。
- trigger、rule、RLS、view、partition/inheritance、FK/cascade、用户函数、未闭合 default/identity/sequence/generated/constraint/index expression。
- session/system/user variable、临时对象、advisory lock、cursor/portal、LISTEN/NOTIFY。
- 用户 prepared statement/命名 prepared statement；内部 sealed capability 不形成用户功能。
- 跨 datasource、跨实例事务迁移、XA/2PC、事务恢复、write/COMMIT 自动重试。
- 业务 DML 指向 AgentSQL control/audit 保留 schema 或保留 OID。

## 2. 架构、状态轴、ABI 与不变量

```text
pre-SDK protocol/auth gate
  → shared directory + continuation proof + owner epoch
  → plan/approval/admission（释放 control locks）
  → pinned connection → native BEGIN → same-tx bind/lock/seal
  → tx_begin audit barrier → ACTIVE → sequential Execute
  → EvidenceConsistency → typed Finish* / forced discard
  → disposition proof → final control fence → response
```

业务响应的三个正交轴是：

```text
DBOutcome:              COMMITTED | NOT_COMMITTED | UNKNOWN
AuditDurability:        DURABLE | AUDIT_PENDING | DURABILITY_LOST
ConnectionDisposition:  RELEASED | DISCARDED | DISCARD_UNCONFIRMED
```

`ConnectionDisposition` 的 ABI 只允许以上三个名字。代码、schema、指标、审计和文档不得再使用 `ConnectionReleased`、`ConnectionDiscarded` 等别名。`EvidencePhase`、`EvidenceConsistency`、append receipt 是 proof metadata，不是新增业务轴。

必须始终成立：

1. MCP、console、approval、directory 都拿不到 raw executor；SQL 只经 `TransactionCapability`。
2. session/transaction id 与 route hint 只标识，不授权；authentication、principal binding、continuation proof、current owner epoch 缺一不可。
3. proof 无效、wrong owner、stale epoch 路径没有 cleanup capability，不能 touch socket、lease、deadline 或受害事务。
4. 每 transaction 同时至多一个 operation owner，只绑定一个 physical backend；active transaction 不迁移。
5. control DB lock 在 business BEGIN 前释放；active business tx 不同步回调 control store。
6. dependency fence 从 same-tx seal 保持到 terminal/disposition 完成。
7. audit sink 只接收 immutable canonical bytes，不得持 audit lock 回调 business/control/directory。
8. final fence 只在 disposition 已是 `RELEASED`、`DISCARDED` 或已登记占预算的 `DISCARD_UNCONFIRMED` 后执行。
9. 所有 digest/event/proof 使用版本化、length-framed canonical encoding；Absent、NA、空值、零值互不等价。
10. 错误文本不参与 outcome 推导；未知 enum/schema/ABI 一律 fail closed。

### 2.1 typed proof schema registry

| proof | frozen schema id | 最低字段 |
|---|---|---|
| begin cleanup | `agentsql.b5.begin-cleanup-proof.v1` | begin resource state、write phase/reply/RFQ、backend/lease generation、decision |
| terminal evidence | `agentsql.b5.terminal-evidence.v2` | operation、attempt generation、write phase、reply、correlation、RFQ、transcript digest |
| no-commit fence | `agentsql.b5.no-commit-ever-sent.v1` | tx/connection generation、terminal CAS history digest、socket-owner generation |
| cancel emission/fence | `agentsql.b5.cancel-emission-proof.v1` | operation generation、cancel state、bytes、PID/secret digest、fence capability/result |
| connection disposition | `agentsql.b5.connection-disposition-proof.v1` | non-reuse、reset/health 或 backend absence、inventory epoch、lease CAS |
| pool child/reaper | `agentsql.b5.pool-child-proof.v1` | envelope generation、child identity、dial permit、backend identity/state |
| WAL append receipt | `agentsql.b5.wal-append-receipt.v1` | event UUID/digest、target、segment/key/ordinal、confirmation state、reported time |
| quarantine | `agentsql.b5.quarantine-proof.v1` | lease/backend identity、reason、age、inventory attempts、capacity charge |

proof digest 必须连同 schema id/version 保存；只保存不可解释的裸 `evidence_digest` 不合格。reader 遇到未知 major version 时不得 release、不得把 UNKNOWN 降级。

## 3. 计划、审批、授权与 DML 合同

`agentsql.b5.plan.v4` 的 stable digest 包含 principal/tenant/agent/key revision、datasource revision/dialect/server major/isolation、policy、binder ABI、closure-policy、资源上限，以及按 ordinal 排序的 operation id、raw SQL digest、analyzed-tree digest、reason digest、DML action 与 manifest digest。它不得包含 approval/session/transaction/owner/backend/lease 随机身份。

```text
plan_digest = SHA-256(CanonicalEncode("agentsql.b5.plan.v4", stable_fields))
begin_authorization_digest = SHA-256(
  CanonicalEncode("agentsql.b5.begin-auth.v3",
    plan_digest, approved_bounds, approval_generation,
    hard_revoke_epoch, api_key_revision, owner_epoch,
    transaction_id, begin_nonce, admission_lease_digest))
```

审批 CAS 从 `approved` 不可逆推进为 `consumed_begin_pending`；begin 任意失败收敛为 `consumed_begin_failed`，成功才是 `consumed_active`。写回失败也不得恢复 ticket。

### 3.1 独立 DML authorization ABI

SELECT 的 B2 column authorization 不得被复用成 DML 近似。每条 DML 必须同时通过：

- `action_grant`: `INSERT | UPDATE | DELETE` 与 target relation 精确绑定；
- `write_target_grant`: 每个真正写入的 `(relation_oid, attnum)`；
- `reference_grant`: WHERE、表达式、冲突检查以及内部分析引用的每列；
- closure grant/deny：所有隐式对象必须由正式 ABI 闭合，否则拒绝。

approval 不能覆盖缺失 action/write-target/reference grant，不能覆盖 reserved schema/OID 拒绝。PG ABI 为 `agentsql-binder-dml-1`，PG14/15/16/17/18 分别 build/hash/attest；在同一 native transaction、同一 backend 上完成 bind、closure、OID lock、seal 和执行。首发继续拒绝 RETURNING、trigger、FK/cascade、default/generated、RLS/rule/view/partition/UDF、复杂 DML 形态。

## 4. BEGIN 唯一时序与独立 cleanup 总表

唯一允许顺序：protocol/proof/owner/idempotency → preflight → approval CAS/admission（释放 control lock）→ pin physical connection → native `BEGIN` → `SET LOCAL` → same-tx bind/closure/lock/seal → compare → `tx_begin` primary barrier → ACTIVE。

### 4.1 状态机

```text
READY → PLAN_READY → APPROVAL_CONSUMED → CONNECTION_PINNED
      → NATIVE_BEGUN → CONTEXT_FIXED → SEALED_IN_TX
      → BEGIN_AUDITING → ACTIVE

APPROVAL_CONSUMED..BEGIN_AUDITING --failure--> BEGIN_FAIL_TERMINATING
ACTIVE --deny/error/timeout--> ROLLBACK_ONLY
ACTIVE --commit CAS--> COMMITTING
ROLLBACK_ONLY --rollback CAS--> ROLLING_BACK
COMMITTING/ROLLING_BACK --single result--> TERMINAL → FINAL_FENCE
```

### 4.2 begin resource state 与 cleanup 表

begin cleanup 先按资源事实分类，禁止把“未建立 transaction”伪装成 `FinishRollback`：

| resource state | 证据 | cleanup | disposition | approval |
|---|---|---|---|---|
| `BEGIN_NO_CONNECTION` | connection/lease acquisition 前失败 | 不调用 DB terminal；释放尚未消费的连接 claim，按 WAL/admission 自身规则收口 | NA | `consumed_begin_failed` |
| `BEGIN_PINNED_NOT_STARTED` | 已 pin；BEGIN permit 未发或精确 zero bytes；reply missing | 不调用 rollback；同连接 protocol 未被触碰时才允许 reset/health + transfer | 全部检查通过可 `RELEASED`，否则 discard | `consumed_begin_failed` |
| `BEGIN_PINNED_REJECTED` | full BEGIN frame + 当前 BEGIN definitive rejection + RFQ `I` | 无 transaction，不调用 rollback | EvidenceConsistency、drain/reset/health/status 全绿才 `RELEASED` | `consumed_begin_failed` |
| `BEGIN_ACK_LOST_OR_INDETERMINATE` | partial/full/indeterminate + reply missing，或 BEGIN ACK/RFQ 相关矛盾 | 不猜是否已 BEGIN；不在不确定 protocol 上补发 rollback；摘池并关闭 | confirmed absence 才 `DISCARDED`，否则 `DISCARD_UNCONFIRMED` | `consumed_begin_failed` |
| `BEGIN_NATIVE_BEGUN` | full frame + correlated positive BEGIN ACK，或允许的强 correlation | 后续任一 SET/bind/seal/compare/audit 失败调用一次 typed `FinishRollback` | 走 terminal consistency/disposition 总表 | `consumed_begin_failed` |

BEGIN reply 的合法覆盖规则与第 5 节相同：positive/rejection 默认要求 full frame；`WriteIndeterminate` 只有强 correlation 才可接受。NotSent/Zero/Partial 与当前 typed BEGIN reply 是矛盾，固定 discard。begin 阶段尚未执行业务 statement 且 terminal CAS history 证明 COMMIT 从未开放，因此可产生 `NoCommitEverSent`；该证明不能来自 BEGIN connector 的自我声明。

## 5. P0-1：EvidenceConsistency、terminal 总函数与 disposition

### 5.1 冻结枚举

```text
TerminalOperation = COMMIT | ROLLBACK
TerminalWritePhase = NotSent | ZeroBytesWritten | PartialBytesWritten |
                     FullFrameWritten | WriteIndeterminate
ProtocolReply = Missing | CurrentPositiveACK | CurrentDefinitiveRejection |
                WrongOperationTypedReply | StaleGenerationTypedReply |
                UntypedOrMismatched | DuplicateACKOrRFQ
ReplyCorrelation = NotApplicable | WeakOrAbsent | StrongCurrentOperation
ServerTxStatus = NotObserved | Idle | InTransaction | Failed |
                 UnknownOrContradictory
EvidenceConsistency = CONSISTENT | INSUFFICIENT | CONTRADICTORY |
                      UNKNOWN_SCHEMA
```

`StrongCurrentOperation` 至少证明：同一 physical connection/tx generation、唯一 socket owner、当前 terminal attempt generation、send permit 后开始的 decoder transcript、send 前无 unread/pending frame、reply operation 类型匹配、单调 transcript offset、恰好一个 ACK/RFQ 序列及 transcript digest。标准 PG reply 没有 request id；该 proof 必须来自独占且从干净 command boundary 开始的 connector 状态机，不能靠 SQLSTATE/文本猜测。

### 5.2 write-phase × reply 一致性总表

下表在 DBOutcome 和 ConnectionDisposition **之前**求值并短路。`P/R` 表示与当前 operation 匹配的 positive/rejection typed reply。

| write phase | Missing | 当前 P/R + strong correlation | 当前 P/R + weak/absent correlation | wrong-operation / stale-generation | untyped/mismatched | duplicate ACK/RFQ |
|---|---|---|---|---|---|---|
| `NotSent` | `CONSISTENT` | `CONTRADICTORY` | `CONTRADICTORY` | `CONTRADICTORY` | `INSUFFICIENT` | `CONTRADICTORY` |
| `ZeroBytesWritten` | `CONSISTENT` | `CONTRADICTORY` | `CONTRADICTORY` | `CONTRADICTORY` | `INSUFFICIENT` | `CONTRADICTORY` |
| `PartialBytesWritten` | `CONSISTENT` | `CONTRADICTORY` | `CONTRADICTORY` | `CONTRADICTORY` | `INSUFFICIENT` | `CONTRADICTORY` |
| `FullFrameWritten` | `CONSISTENT` | `CONSISTENT` | `INSUFFICIENT` | `CONTRADICTORY` | `INSUFFICIENT` | `CONTRADICTORY` |
| `WriteIndeterminate` | `CONSISTENT` | `CONSISTENT`* | `CONTRADICTORY` | `CONTRADICTORY` | `INSUFFICIENT` | `CONTRADICTORY` |

`*` 是唯一 typed reply 覆盖不精确 write phase 的例外：强 correlation 是比本地 write accounting 更强的当前命令服务端证明。它不允许覆盖 NotSent/Zero/Partial，因为这些是明确、相反的本地事实。未知 phase/reply/correlation/schema 固定为 `UNKNOWN_SCHEMA`。

phase×reply 之后还必须执行跨字段一致性，二者取更严重结果（`UNKNOWN_SCHEMA > CONTRADICTORY > INSUFFICIENT > CONSISTENT`）：

- typed reply 的 operation 必须等于 terminal CAS operation；否则是 wrong-operation contradiction。
- reply/transcript generation 小于或大于当前 attempt 都是 stale/future contradiction，不能“挑最新一条”。
- 同一 attempt 只能有一个 terminal ACK/ErrorResponse 和一个 RFQ；第二个 ACK、互斥的 positive+rejection、第二个 RFQ 或 RFQ 状态变化都是 contradiction。
- matching positive/rejection + RFQ `Idle` 合法；RFQ `NotObserved` 不否定已相关的 reply，但禁止 release；matching reply + `InTransaction/Failed/UnknownOrContradictory` 是 contradiction。
- `ReplyMissing + RFQ Idle` 不是结果证明，保持按 write phase 求值且禁止 release；RFQ 不得补造 ACK。
- decoder 在 send permit 前已有 unread bytes/pending frame时，任何随后 typed reply 都不能取得 strong correlation，至少 `INSUFFICIENT`，若被标作 current 则为 contradiction。

`EvidenceConsistency != CONSISTENT` 时不得进入 nominal release candidate 表：

| consistency | operation | DBOutcome | ConnectionDisposition |
|---|---|---|---|
| `CONTRADICTORY` / `UNKNOWN_SCHEMA` | COMMIT | `UNKNOWN` | `DISCARDED` 或 `DISCARD_UNCONFIRMED`；永不 `RELEASED` |
| `CONTRADICTORY` / `UNKNOWN_SCHEMA` | ROLLBACK | 仅独立可信 `NoCommitEverSent` 成立时 `NOT_COMMITTED`，否则 `UNKNOWN` | `DISCARDED` 或 `DISCARD_UNCONFIRMED`；永不 `RELEASED` |
| `INSUFFICIENT` | COMMIT | 按无 typed reply 的 write phase：not-sent/zero 可 `NOT_COMMITTED`，其余 `UNKNOWN` | discard，永不 release |
| `INSUFFICIENT` | ROLLBACK | 独立 `NoCommitEverSent` 成立时 `NOT_COMMITTED`，否则 `UNKNOWN` | discard，永不 release |

`NoCommitEverSent` 必须来自 coordinator 的单调 terminal-CAS history、connection generation 与唯一 socket-owner ledger；它独立于被判矛盾的 connector reply/phase。只要 COMMIT send permit 曾开放、history 不连续、owner generation 不匹配或 schema 未识别，proof 即为 false。

### 5.3 nominal DBOutcome 总函数

只有 `EvidenceConsistency=CONSISTENT` 才应用：

| operation | reply | write phase | DBOutcome |
|---|---|---|---|
| COMMIT | current positive ACK | Full 或 Indeterminate+strong | `COMMITTED` |
| COMMIT | current definitive rejection | Full 或 Indeterminate+strong | `NOT_COMMITTED` |
| COMMIT | Missing | NotSent/Zero | `NOT_COMMITTED` |
| COMMIT | Missing | Partial/Full/Indeterminate | `UNKNOWN` |
| ROLLBACK | current positive ACK | Full 或 Indeterminate+strong | `NOT_COMMITTED`，且要求 `NoCommitEverSent` |
| ROLLBACK | current definitive rejection | Full 或 Indeterminate+strong | 仅 `NoCommitEverSent` 时 `NOT_COMMITTED`，否则 `UNKNOWN` |
| ROLLBACK | Missing | 任意 phase | 仅 `NoCommitEverSent` 时 `NOT_COMMITTED`，否则 `UNKNOWN` |

PG deferred constraint 的 COMMIT ErrorResponse 是当前 `definitive rejection`；若 consistency 与 RFQ 均正常，它是 `NOT_COMMITTED`。`ServerIdle` 不能把 ACK missing 的 COMMIT 从 UNKNOWN 猜成 known。

### 5.4 nominal release 与完整 physical lifecycle

release candidate 仅有三种：COMMIT positive+Idle、COMMIT definitive rejection+Idle、ROLLBACK positive+Idle；并且都要求 consistency=CONSISTENT、typed proof/current schema、没有 cancel taint。candidate 还必须在同一 physical connection 上依次通过 drain、driver/session reset、health ping、status recheck 和 pool-transfer generation CAS，才得到 `RELEASED`。

其他组合或任一步失败：先保证 pool non-reuse，再 close/hijack 并做 backend termination reconciliation。只有 exact backend identity 的 exit、DB-side terminate+inventory、或 attested 失活上界之后的独立 inventory absence 才是 `DISCARDED`；仅有本地 Close/FIN/RST/`ErrBadConn` 是 `DISCARD_UNCONFIRMED`。

`FinishCommit/FinishRollback` 拥有 terminal send 到 transfer/quarantine 的完整 lifecycle，返回 `agentsql.b5.terminal-evidence.v2` 和 `agentsql.b5.connection-disposition-proof.v1`。调用者不得补发 ROLLBACK、再次 Close/Release 或重算 outcome。

## 6. P0-2：watchdog、PG CancelRequest 与 late-cancel 基线

### 6.1 single-owner gate

```text
OP_IDLE --request CAS--> OP_EXECUTING(generation)
  normal completion CAS wins → OP_IDLE 或 OP_ROLLBACK_REQUIRED
  watchdog CAS wins         → OP_QUIESCING(generation)
      cancel not sent proven → Execute exit → one typed rollback owner
      cancel emitted/unknown → OP_CANCEL_TAINTED → seize/close/discard only
      no quiesce             → OP_DISCARD_OWNER
terminal/discard owner → OP_TERMINAL（single result）
```

normal Execute 在 watchdog CAS **之前**完成并赢得 CAS，且 `CancelEmission=NOT_SENT_PROVEN` 时，watchdog 不得再开 cancel socket；唯一 terminal owner 可 typed rollback。CAS 输家只能等待结果。

### 6.2 standard PostgreSQL CancelRequest 的冻结规则

PG CancelRequest 使用独立 TCP connection，只绑定 backend PID/secret，没有 statement generation、没有服务端 ACK，也没有“所有迟到 cancel 已消费”的 fence。因此标准 PG connector 固定声明 `generation_bound_cancel_fence=false`。

`CancelEmission` 分为：

- `NOT_SENT_PROVEN`：未打开 send permit，或计数 wrapper 证明 cancel packet 零字节且 send path 已封死；
- `SEND_STARTED_OR_INDETERMINATE`：无法证明零字节、partial、TLS/proxy buffering 不明；
- `FULL_PACKET_WRITTEN`：完整 CancelRequest 已交给 transport。

一旦进入后两者，该 physical connection 永久 `cancel-tainted`：

1. 禁止在主连接上发送 ROLLBACK/COMMIT、reset、health probe 或任何新 statement；
2. Execute 退出与 RFQ `I` 也不能解除 taint，因为它们不证明另一 TCP 路径中的 cancel 已消费；
3. lifecycle owner 只能摘池、close/hijack，并做 backend absence 对账；结果只能 `DISCARDED`/`DISCARD_UNCONFIRMED`；
4. `NoCommitEverSent` 可信时 DBOutcome 可为 `NOT_COMMITTED`，否则为 `UNKNOWN`；
5. 迟到 Execute/cancel 结果只写 diagnostic，不能推进 seq、release、reset 或产生第二 terminal。

只有未来 connector 提供强于标准 PG 的 `generation-bound confirmed cancel fence` 才可在 cancel 后 typed rollback。该能力必须由服务端把 cancel 与 statement generation 绑定，返回可认证 ACK，并证明 fence 后不存在能命中下一 command 的 pending cancel；仅等待、RFQ、读循环结束、关闭 cancel socket或 vendor 声明都不够。它需独立设计复审和 schema/attestation，v0.4 PG 路径按“不存在”实现。

### 6.3 watchdog 顺序

默认 legacy in-flight watchdog 300ms：CAS freeze → 尝试 cancel → 最多 25ms 等 Execute quiesce。若 cancel 仍为 `NOT_SENT_PROVEN` 且 Execute 已退出、protocol synchronized，可由唯一 owner typed rollback；若 cancel 已发/不确定或 grace 超时，只 forced discard，不发 rollback。HTTP transport cancel 只是提前触发信号；2025-06-18 请求间仍仅由 tx idle 15s/tx wall 60s 保证最终 cleanup。

## 7. connection lease、pool envelope 与 owner-crash reaper

### 7.1 connection lease

```text
IN_USE → TERMINATING
  → RELEASED + pool transfer proof                    → FREE
  → DISCARDED + exact backend absence                 → FREE
  → local non-reuse but backend unconfirmed           → QUARANTINED
QUARANTINED → reaper terminate/inventory → absence CAS → FREE
```

owner/session lease 过期、本地 socket close、实例进程消失、LB/DNS 切换均不是 `FREE` 证据。`DISCARD_UNCONFIRMED` 继续占 datasource capacity，generation CAS 防止迟到 owner 释放新 lease。PG backend identity 至少绑定 server identity、database、PID、backend start/connection nonce，不能只用可复用 PID。

### 7.2 pool envelope 与 durable child identity

每个 `POOL_ENVELOPE` 及其每次 dial permit、每个已建立 backend 都有 durable child record：

```text
envelope_id/generation, owner instance/incarnation,
claimed_capacity, local MaxOpenConns,
child_id/generation, dial_permit_id,
physical/backend identity, state,
last disposition/inventory proof, charged slot
```

顺序固定为：先取得 envelope/dial claim，再允许 dial；backend 建立后以 CAS 把 dial permit 转为 child identity；失败/超时 permit 在证明“没有 backend 被建立”前仍计费。pool 内 child 引用 envelope slot，不另收 direct unit；quarantine child 仍占该 slot且禁止建立替代 backend。pool 外 B5 direct connection 用 `DIRECT_PINNED` 单独计费。migration、health、admin 同样必须有显式 claim。

### 7.3 owner crash 的 reaper 合同

1. durable fencing old owner/envelope generation，拒绝其迟到的 dial/transfer/release CAS。
2. 从 child ledger 区分：`NEVER_ALLOCATED`、`DIAL_PERMIT_OUTSTANDING`、`BACKEND_LIVE_OR_UNKNOWN`、`BACKEND_ABSENT_CONFIRMED`。
3. `NEVER_ALLOCATED` 是确定未使用 delta，可在 fence 后缩减/转移；outstanding permit 只有证明未建 backend 后才能转为该状态。
4. 任何可能存活的 idle/in-use/quarantined backend 都按 `BACKEND_LIVE_OR_UNKNOWN` 计费；跨进程不能接管其 socket，必须 terminate 并以独立 inventory 确认 absence。
5. old envelope 只有所有 child/permit 对账后才能整体释放。对账不全时，新实例只能取得“全局未分配容量 + old envelope 中确定未使用 delta”，不得借走可能存活 child 的 slot。
6. inventory partition 时不缩 claim、不扩 pool；恢复后按 inventory epoch 和 child generation CAS 收敛。重复 reaper 幂等，迟到旧 owner 不能改变新 generation。

共享预算必须满足：

```text
sum(POOL_ENVELOPE claimed_capacity
    + DIRECT_PINNED
    + MIGRATION + HEALTH + ADMIN)
  + admin_emergency_reserve
  <= datasource_db_hard_budget
  <= max_connections - external_reserve
```

缩池：先降低本地 `MaxOpenConns`、关闭 child、确认 absence，再释放 delta；扩池顺序相反。owner-crash 真库测试必须覆盖可能仍存活的 idle backend，而不只覆盖 pinned transaction。

## 8. continuation、幂等、revoke 与锁图

### 8.1 continuation 与幂等

`open_session` 返回 256-bit CSPRNG secret；`Ksession` 用 HKDF-SHA-256 派生，directory 只保存 KMS KEK 加密后的 key，AES-256-GCM AAD 绑定 schema/session/principal/datasource/owner epoch/KEK id。proof 为：

```text
HMAC-SHA-256(Ksession,
  CanonicalEncode("agentsql.b5.continuation.v2",
    method, session_id, owner_epoch, request_id,
    expected_seq_tagged, SHA-256(body_without_proof)))
```

验证 auth/proof/current owner 后，先以 `(session, method, request_id)` 查 idempotency；同 body 返回既有结果，异 body 返回 `IDEMPOTENCY_CONFLICT`；只有 miss 才检查 current seq 并建立 in-flight CAS。write/COMMIT 永不因网络错误自动重放。

### 8.2 hard revoke 60s 的准确语义

durable monotonic revoke feed 发布后，健康 owner 收到更高 epoch 即拒绝新 operation并触发终结；feed/control 不可用时不允许新 begin，已 ACTIVE 仍由不可续期 60s tx wall 终结。

**60s 是“停止接受授权操作并触发 terminal/discard”的授权上界，不是 backend disappearance、锁释放或 quarantine 清零上界。** owner pause、网络黑洞、inventory partition 后物理 backend 可超过 60s，必须继续占 claim并按第 7 节证明消失。

### 8.3 唯一等待图

```text
control snapshot / approval / admission  rank 10
  --transaction complete并释放锁-->
business native transaction              rank 20
  --bounded immutable append only-->
audit-chain terminal sink                rank 30

business terminal + disposition complete
  --new phase，无 rank 20/30 lock--> final control fence
```

audit API 不接受 callback、executor、directory/control client；audit worker 包不得 import business/directory/approval/fence。group commit 调 backend 时不持 coordinator mutex。final fence 的构造函数只接受 completed terminal/disposition proof，类型上拿不到 live business connection。静态 dependency rule、phase capability 与 runtime assertion共同拒绝 audit→control、audit→business、business→control 反边。

## 9. P0-3：emergency WAL framed charge、非自指 durability 与 nonce

### 9.1 业务事件不描述“自身是否已存储”

v4 选择评审给出的第一种方案：从被保护 `CanonicalEvent` 删除其自身 `AuditDurability`。业务事件只描述已经发生的业务/DB/terminal/connection 事实；“这条事件存在哪里、何时确认”属于 `agentsql.b5.wal-append-receipt.v1`、primary receipt 和查询时派生状态。

派生规则：

| receipt fact at response deadline | response `AuditDurability` |
|---|---|
| primary insert/commit confirmed durable | `DURABLE` |
| primary 未确认，WAL record 的 fsync 在 deadline 前明确成功 | `AUDIT_PENDING` |
| WAL write/fsync timeout、失败、状态不明、key 不可用或进程先崩溃 | `DURABILITY_LOST` |

响应保存 `reported_durability_at_response` 作为 immutable result metadata，但不写回原业务事件。后续 late fsync/replay 只能改变 `append_confirmation_state/current_reconciliation_state` 并追加 `tx_durability_recovered` diagnostic；不得倒改已返回值或把原 record 静默解释成“当时 AUDIT_PENDING”。

### 9.2 record format 与最大字节 charge

```text
RecordHeader {
  magic, wal_format_version, header_length, total_length,
  segment_id, record_ordinal, reservation_id,
  event_uuid, event_schema_id/version, event_payload_digest,
  key_id, nonce, ciphertext_length, charged_bytes
}
Ciphertext = AES-256-GCM(Ksegment, nonce,
                         plaintext=CanonicalEvent,
                         AAD=canonical immutable header fields)
Trailer { AEAD_tag, CRC32C, commit_marker, padding }
```

CRC/length/commit marker 用于 torn-tail 恢复，不替代 AEAD。默认冻结：

```text
max_canonical_event_bytes              = 16,384 B
record_header_max                      =    512 B
max_ciphertext                         = 16,384 B
AEAD_tag                               =     16 B
CRC/trailer/commit-marker maximum      =     32 B
segment/index/accounting overhead      =    512 B per record
record_alignment                       =  4,096 B

max_record_charge = align_up(
  record_header_max
  + max_ciphertext
  + AEAD_tag
  + CRC/trailer/commit-marker maximum
  + segment/index/accounting overhead,
  record_alignment)
                  = align_up(17,456, 4,096)
                  = 20,480 B (20 KiB)

reservation = max_path_record_count × max_record_charge
            = 6 × 20,480
            = 122,880 B (120 KiB)
```

header/AEAD/alignment 任何增长必须先提高 `max_record_charge` 和 migration/version，不能侵占 headroom。allocator 可向上按 segment extent 预留，但逻辑 charge 不得小于 120KiB。容量测试使用真实最大 ciphertext、最大 header/trailer、index/accounting charge 和 padding 后的 **20KiB framed record**，不能只写接近 16KiB plaintext。

### 9.3 由事件状态机证明最多六条

primary 最后成功点之后，reservation 的最坏路径是“一个 statement 已产生业务 effect，但它的 primary audit 首次失败”。第一处失败立即把 tx rollback-only，禁止下一 statement，因此最多：

1. 当前 `tx_statement` 完整事件；
2. `tx_rollback` 事实；
3. `tx_terminal_outcome`（DBOutcome/evidence/disposition）；
4. `session_terminal`/transaction tombstone 所需完整事件；
5. 一条 `durability_diagnostic`（timeout/lost/wedged）；
6. 一条 `recovery/headroom`（late confirmation、segment recovery或版本化补偿）。

其他 primary-last-success 点不超过它：begin barrier 前没有业务 effect；commit intent 必须 primary durable才可发 COMMIT，之后只需 terminal/session/diagnostic/recovery；rollback-only 后不再允许 statement。每次 Execute 前必须验证剩余 reservation 足以覆盖 `statement + rollback + terminal + session + diagnostic + recovery` 的当时最大集合；不足则在业务 side effect 前拒绝。

### 9.4 fsync timeout → LOST → late success → restart 的精确语义

1. primary append 失败；WAL writer 写入完整 framed record并启动 fsync。
2. 250ms deadline 到而 fsync 未返回：writer/segment 标 `WEDGED_UNCONFIRMED`，禁止新 append，保留 reservation；API 固定返回 `DURABILITY_LOST`，receipt=`TIMEOUT_UNCONFIRMED`。
3. fsync 迟到返回成功：仅将内存/可持久 recovery ledger 标为 `LATE_CONFIRMED_AFTER_RESPONSE`；不得修改旧响应，不得发布普通 AUDIT_PENDING。
4. 随即 crash/restart：scanner 只按 length+commit marker+CRC+AEAD+event digest 验证。完整 record 以 source=`late_confirmed_recovery` 重放原业务事件；primary UUID 相同且 payload 相同才幂等成功。之后追加引用原 UUID/receipt 的 `tx_durability_recovered`。
5. 查询结果显示 `reported_durability_at_response=DURABILITY_LOST`、`append_confirmation_state=LATE_CONFIRMED|RECOVERED_ON_RESTART`、`current_reconciliation_state=PRIMARY_DURABLE`；历史响应不变。
6. 若 record torn/AEAD失败/digest不符，则绝不重放，保持 LOST，quarantine segment并最高级告警。即使进程在 late-success callback 持久化前崩溃，restart 也只能根据实际可验证 record决定 recovery，不根据“可能成功”猜测。

### 9.5 nonce 生成、持久化与不复用证明

每个 segment 创建独立随机 256-bit `Ksegment` 和 128-bit `segment_id`；key registry 以 CAS 保证 `key_id=(master_revision, segment_id)` 从未存在。manifest（segment id、wrapped Ksegment、key id、format）创建并 fsync 文件及父目录后才允许写 record。nonce 固定为 `domain32 || uint64_be(record_ordinal)`，ordinal 在单 writer segment 内从 0 单调递增，达到上限前强制 rotation。

关键规则：进程启动、非干净退出、writer ownership 变化或 rotation 后，旧 segment **永不重新开放 append**，只能只读 scan/seal；总是创建新 segment、新 Ksegment/key id，再从 ordinal 0 开始。因此同一 key 内 ordinal 不重复，crash 后也不会回退复用；rotation 也换 key。key registry/manifest 未 durable 前不使用 key；发现 duplicate segment/key id、ordinal 回退或 nonce detector 命中立即停止 WAL。旧 decrypt key 保留到所有 segment replay/销毁完成。

### 9.6 replay、双链与 UUID 冲突

CanonicalEvent 使用 `agentsql.audit.event.v4`，包含业务语义、DBOutcome、terminal evidence、ConnectionDisposition，但不含自身 AuditDurability、WAL nonce/sequence 或 primary global position。per-tx chain 用 `transaction_seq + previous_tx_event_digest`；primary ingestion chain按实际接收顺序独立构建。

同 EventUUID 已存在时必须常量时间比较 payload digest；相同才幂等，不同返回 `AUDIT_EVENT_UUID_CONFLICT`、停止该 reservation 自动 replay、保留 WAL并告警。晚到可 staging，predecessor 连续后才 verified；recovery event 不改写原事件。

## 10. 资源默认值与硬限制

| resource | default | hard/rule |
|---|---:|---|
| sessions / agent / tenant / cluster | 4 / 32 / 256 | 16 / 128 / 1024，集群级 |
| active tx / agent / tenant / datasource | 1 / 8 / 2 | 4 / 32 / DB claim 更小值 |
| datasource reserve | admin emergency=2；stateless=`max(1,25% hard budget)` | 每 datasource 显式配置 |
| statements / tx | 16 | 32 |
| SQL bytes / statement | 256KiB | hard |
| canonical active plan / tx | 1MiB | hard，含 metadata charge |
| active plan bytes tenant/datasource/cluster | 8/32/64MiB | 32/128/256MiB |
| affected rows / tx | 10,000 | policy/datasource 可更小 |
| idempotency / session | 64 entries / 256KiB | 128 / 512KiB；大响应只存 digest |
| session idle / absolute | 10min / 60min | 30min / 60min |
| tx idle / wall | 15s / 60s | 30s / 60s；wall 不可续期 |
| statement / legacy watchdog | datasource 更小值≤5s / 300ms | watchdog 独立于 request context |
| quiesce grace | 25ms | hard 50ms |
| PG terminal local budget | 150ms + 25ms margin | backend absence另计 |
| audit barrier | 250ms | 取 remaining tx wall 更小值 |
| final fence | 250ms | 只重试 fence，不重复 terminal |
| WAL max event / framed record / reservation | 16KiB / 20KiB / 120KiB | 6 framed records；不得超卖 |
| tombstone TTL / entries | 15min / 50,000 | 30min / 50,000 hard |
| tombstone charged bytes cluster | 128MiB | 256MiB hard |
| tombstone churn | principal 120/min；tenant 2,000/min；global 200/s | open/close/expire 都计费 |

metadata charge 使用 canonical serialized bytes + allocator/row/index/cache headroom 后向上取 256B；tombstone只保留 id digests、terminal enums、final seq、owner epoch、event digest、expiry，禁止引用 plan/SQL/reason/rows/response/secret。active plan、WAL reservation、tombstone、connection quarantine分账。

## 11. MCP/API、稳定错误码与 fixed effect

response 必须同时返回三个轴、固定 boolean/effect 与 terminal event digest。`retry_same_request` 只允许同 request id+同 body 重取结果，不授权重放 write/COMMIT；`new_transaction_allowed` 不代表旧请求可复用。

```text
tx_effect = NO_TX_CHANGE | KEEP_ACTIVE | MARK_ROLLBACK_ONLY |
            TERMINAL_NOT_COMMITTED | TERMINAL_COMMITTED |
            TERMINAL_UNKNOWN | SESSION_TERMINAL | FENCE_ONLY
```

### 11.1 稳定错误合同

| code | retry same | new tx | fixed effect |
|---|---:|---:|---|
| `MCP_PROTOCOL_UNSUPPORTED` | false | false | `NO_TX_CHANGE` |
| `SESSION_PROOF_REQUIRED` / `SESSION_NOT_FOUND_OR_DENIED` | false | false | `NO_TX_CHANGE` |
| `SESSION_OWNER_EPOCH_STALE` | false | false | `NO_TX_CHANGE` |
| `SESSION_WRONG_INSTANCE` / `SESSION_ROUTE_UNAVAILABLE` | true | false | `NO_TX_CHANGE` |
| `SESSION_OWNER_LOST` | false | false | `SESSION_TERMINAL` |
| `SESSION_BUSY` | true | false | `KEEP_ACTIVE` |
| `SESSION_LIMIT_EXCEEDED` | true | true | `NO_TX_CHANGE` |
| `SESSION_EXPIRED` / `SESSION_TERMINAL_RECORD_EXPIRED` | false | true | `SESSION_TERMINAL` |
| `SESSION_RENEW_DURING_TX` | false | false | `KEEP_ACTIVE` |
| `IDEMPOTENCY_REQUIRED` | false | false | `NO_TX_CHANGE` |
| `IDEMPOTENCY_CONFLICT` | false | false | `KEEP_ACTIVE` |
| `RATE_LIMIT_STATE_EXHAUSTED` | true | false | `NO_TX_CHANGE` |
| `TX_NOT_ACTIVE` / `TX_ALREADY_ACTIVE` | false | false | `NO_TX_CHANGE` |
| `TX_ID_MISMATCH` / `TX_ACTIVE_PLAN_MISMATCH` | false | true | `MARK_ROLLBACK_ONLY` |
| `TX_PLAN_REQUIRED` / `TX_PLAN_MISMATCH`（begin 前） | false | true | `NO_TX_CHANGE` |
| `TX_APPROVAL_REQUIRED` / `TX_APPROVAL_EXPIRED` / `TX_APPROVAL_PLAN_MISMATCH` | false | true | `NO_TX_CHANGE` |
| `TX_APPROVAL_CONSUMED_BEGIN_FAILED` | false | true | `TERMINAL_NOT_COMMITTED` |
| `TX_BEGIN_MANIFEST_MISMATCH` | false | true | `TERMINAL_NOT_COMMITTED` |
| `TX_BEGIN_OUTCOME_UNCERTAIN_CONNECTION_QUARANTINED` | false | true | `TERMINAL_NOT_COMMITTED` |
| `TX_PLAN_INCOMPLETE` | false | false | `KEEP_ACTIVE` |
| `TX_ROLLBACK_ONLY` | false | true | `MARK_ROLLBACK_ONLY` |
| `TX_MASK_UNSUPPORTED`（begin 前） | false | true | `NO_TX_CHANGE` |
| `TX_SELECT_UNSUPPORTED` / `TX_RETURNING_UNSUPPORTED`（active） | false | true | `MARK_ROLLBACK_ONLY` |
| `TX_CONTROL_STATEMENT_DENIED` / `TX_DML_SHAPE_UNSUPPORTED`（begin 前） | false | true | `NO_TX_CHANGE` |
| `TX_IDLE_TIMEOUT` / `TX_MAX_DURATION` / `TX_STATEMENT_TIMEOUT` | false | true | `TERMINAL_NOT_COMMITTED` |
| `TX_OPERATION_WATCHDOG` | false | true | `TERMINAL_NOT_COMMITTED` |
| `TX_OPERATION_WATCHDOG_OUTCOME_UNKNOWN` | false | false | `TERMINAL_UNKNOWN` |
| `TX_CANCEL_EMITTED_CONNECTION_QUARANTINED` | false | true | `TERMINAL_NOT_COMMITTED` |
| `TX_COMMIT_EVIDENCE_CONTRADICTION` | false | false | `TERMINAL_UNKNOWN` |
| `TX_ROLLBACK_EVIDENCE_CONTRADICTION_NO_COMMIT` | false | true | `TERMINAL_NOT_COMMITTED` |
| `TX_ROLLBACK_EVIDENCE_CONTRADICTION_UNKNOWN` | false | false | `TERMINAL_UNKNOWN` |
| `TX_ROLLBACK_UNCONFIRMED` | false | true | `TERMINAL_NOT_COMMITTED` |
| `TX_LIMIT_EXCEEDED` / `PLAN_BYTE_LIMIT_EXCEEDED`（begin 前） | true | true | `NO_TX_CHANGE` |
| `TX_EXECUTION_LIMIT_EXCEEDED` | false | true | `MARK_ROLLBACK_ONLY` |
| `TX_COMMITTED_AUDIT_PENDING` | false | false | `TERMINAL_COMMITTED` |
| `TX_NOT_COMMITTED_AUDIT_PENDING` | false | true | `TERMINAL_NOT_COMMITTED` |
| `TX_DB_OUTCOME_UNKNOWN` / `TX_DB_OUTCOME_UNKNOWN_AUDIT_PENDING` | false | false | `TERMINAL_UNKNOWN` |
| `TX_COMMITTED_AUDIT_DURABILITY_LOST` | false | false | `TERMINAL_COMMITTED` |
| `TX_NOT_COMMITTED_AUDIT_DURABILITY_LOST` | false | true | `TERMINAL_NOT_COMMITTED` |
| `TX_DB_OUTCOME_UNKNOWN_AUDIT_DURABILITY_LOST` | false | false | `TERMINAL_UNKNOWN` |
| `AUDIT_BARRIER_UNAVAILABLE_BEFORE_TX` | true | true | `NO_TX_CHANGE` |
| `AUDIT_STATEMENT_BARRIER_FAILED` | false | true | `MARK_ROLLBACK_ONLY` |
| `AUDIT_COMMIT_INTENT_FAILED` | false | true | `TERMINAL_NOT_COMMITTED` |
| `AUDIT_EMERGENCY_WAL_UNAVAILABLE`（begin 前） | true | true | `NO_TX_CHANGE` |
| `AUDIT_EVENT_UUID_CONFLICT` | false | false | `FENCE_ONLY` |
| `FINAL_FENCE_PENDING_COMMITTED` | true | false | `FENCE_ONLY` |
| `FINAL_FENCE_PENDING_NOT_COMMITTED` | true | true | `FENCE_ONLY` |
| `FINAL_FENCE_PENDING_UNKNOWN` | true | false | `FENCE_ONLY` |
| `AUTH_DML_BINDER_REQUIRED` / `AUTH_DML_ACTION_MISSING` | false | true | `NO_TX_CHANGE` |
| `AUTH_DML_WRITE_TARGET_GRANT_MISSING` / `AUTH_DML_REFERENCE_GRANT_MISSING` | false | true | `NO_TX_CHANGE` |
| `AUTH_IMPLICIT_OBJECT_UNCLOSED` / `AUTH_CLOSURE_UNSUPPORTED` | false | true | `NO_TX_CHANGE` |
| `AUTH_CONSTRAINT_CLOSURE_UNSUPPORTED` / `AUTH_TYPE_CLOSURE_UNSUPPORTED` | false | true | `NO_TX_CHANGE` |
| `AUTH_DEFAULT_CLOSURE_UNSUPPORTED` / `AUTH_EXPRESSION_CLOSURE_UNSUPPORTED` | false | true | `NO_TX_CHANGE` |
| `AUTH_REWRITE_CLOSURE_UNSUPPORTED` / `AUTH_RELATION_KIND_UNSUPPORTED` | false | true | `NO_TX_CHANGE` |
| `AUTH_INTERNAL_OBJECT_DENIED` / `AUTH_WHOLE_ROW_UNSUPPORTED` | false | true | `NO_TX_CHANGE` |
| `AUTH_CATALOG_RACE`（active） | false | true | `MARK_ROLLBACK_ONLY` |
| `TX_COMMITTED_CONNECTION_QUARANTINED` | false | false | `TERMINAL_COMMITTED` |
| `TX_NOT_COMMITTED_CONNECTION_QUARANTINED` | false | true | `TERMINAL_NOT_COMMITTED` |
| `TX_UNKNOWN_CONNECTION_QUARANTINED` | false | false | `TERMINAL_UNKNOWN` |
| `DIALECT_TRANSACTION_UNSUPPORTED` | false | false | `NO_TX_CHANGE` |

watchdog/cancel/rollback contradiction 的 NOT_COMMITTED code 只有 `NoCommitEverSent` proof v1 成立时可用；否则必须选 UNKNOWN code。调用点不得覆盖表中 boolean/effect。

## 12. 最终实现切片与无环依赖 DAG

合同与实现明确拆开，S1 不再同时充当上游合同和下游 schema：

```text
S4a terminal/EvidenceConsistency/cancel contract ─┐
S5a binder/BEGIN-cleanup/closure contract ────────┼──> S1b frozen canonical/schema
S7a WAL/event/receipt/reservation contract ───────┘             │
                                                                ├──> S2
                                                                ├──> S3
                                                                ├──> S4b
                                                                ├──> S5b
                                                                └──> S7b

S3 + S4b + S5b + S7b ──> S6 coordinator
S2 + S6 ────────────────> S8 MCP/final fence
S3 + S7b + S8 ─────────> S9 operations/canary controls
S1b..S9 ────────────────> S10 activation gate
```

### S4a：terminal/cancel 合同与 spike（DB Core + PostgreSQL）

- 冻结 evidence table、correlation、NoCommit proof、cancel-taint 和 ABI v2；做 PG wire-level typed phase/late-cancel spike。
- 出口：评审 P0-1/P0-2 的每个 contradiction/race 格都有唯一预期；不提交 production enable path。

### S5a：binder/begin 合同（PostgreSQL + Security）

- 冻结 action/write-target/reference ABI、begin cleanup 三态、closure 拒绝矩阵与五 major attestation 输入。
- 出口：BEGIN ACK-loss 与 implicit object 均有 fail-closed 映射。

### S7a：WAL/event 合同（Audit/B6 + Storage/SRE）

- 冻结 event v4、receipt v1、20KiB framed charge、120KiB reservation、late-fsync recovery、nonce/segment lifecycle。
- 出口：按真实编码器生成 maximum framed golden；storage owner 签字节账。

### S1b：canonical/schema freeze（Architecture + Security）

- 只在 S4a/S5a/S7a 出口均完成后冻结 enum、proof、JSON、migration、golden；所有 flags off。

### S2：pre-SDK gate、continuation、idempotency、revoke

- exact 2025-06-18 allowlist、HKDF/HMAC/AEAD rotation、bounded replay、idempotency-before-seq、durable revoke feed。

### S3：directory、claim、pool child 与 quarantine

- owner epoch/incarnation、sticky route、envelope/child/dial ledger、reaper、MaxOpenConns、metadata/cardinality cap。

### S4b：typed terminal physical lifecycle

- PG14–18 adapter、EvidenceConsistency 实现、Finish ownership、backend confirmation；标准 PG cancel 后 forced discard。MySQL仅实验/off。

### S5b：PG DML binder implementation

- native-BEGIN-first、same-tx bind/closure/lock/seal、action/write/reference grants、五 major独立hash。

### S7b：WAL、双链与结构锁序实现

- full record、receipt、nonce/key registry、fsync/recovery、UUID conflict、verifier、phase capability/analyzer。

### S6：planned coordinator

- approval CAS、begin cleanup、single operation/terminal owner、watchdog/cancel taint、rollback-only、dependency lifetime。

### S8：MCP tools 与 final fence

- axes response、idempotent status/terminal、HTTP/stdio/shutdown、disposition 后 fence、旧工具兼容。

### S9：运维与灰度控制

- quarantine/reaper、audit lost/recovery、unknown resolver、claim/WAL/feed metrics、RBAC runbook、自动 stop。

### S10：真库/真网络 activation gate

- 第 13 节全矩阵逐 datasource 留证并应用第 14 节量化阈值。MySQL 期望始终 unsupported。

## 13. PostgreSQL 真库/真网络 release-blocking 矩阵

所有 PG14/15/16/17/18，TLS on/off（生产路径必须 on），direct/proxy，client→server 与 server→client 单向黑洞、half-open、RST、restart/failover 都要执行。fault connector 记录应用层精确 write count、decoder transcript、typed reply/RFQ；旁路连接只验真，不能反向修改被测分类。

### 13.1 terminal phase × reply、stale/wrong/duplicate

| case | expected DBOutcome | expected disposition/assertion |
|---|---|---|
| COMMIT NotSent/Zero + Missing | `NOT_COMMITTED` | discard；不因 clean socket 猜 release |
| COMMIT Partial/Full/Indeterminate + Missing | `UNKNOWN` | discard/unconfirmed；禁止重试 |
| COMMIT Full + correlated positive + RFQ I | `COMMITTED` | 全部 reset/health/status/CAS 通过才 `RELEASED` |
| COMMIT Full + correlated definitive rejection + RFQ I | `NOT_COMMITTED` | checks 全过才 `RELEASED` |
| COMMIT Indeterminate + strong correlated P/R | ACK→committed，rejection→not committed | correlation proof完整；否则 contradiction+discard |
| COMMIT NotSent/Zero/Partial + 当前 P/R | `UNKNOWN` | `CONTRADICTORY`；never release |
| ROLLBACK NotSent/Zero/Partial + 当前 P/R | no-commit proof 时 `NOT_COMMITTED`，否则 `UNKNOWN` | `CONTRADICTORY`；never release |
| ROLLBACK Full + current positive + RFQ I | `NOT_COMMITTED` | no-commit proof + checks全过才 `RELEASED` |
| wrong-operation typed reply | COMMIT=`UNKNOWN`；ROLLBACK按no-commit fence | contradiction、discard |
| stale previous-generation reply | 同上 | transcript/generation告警、discard |
| duplicate ACK、duplicate RFQ、ACK后第二终态 reply | COMMIT=`UNKNOWN`；ROLLBACK按no-commit fence | contradiction、discard；零 pool reuse |
| current ACK + RFQ T/E/duplicate I | 按 contradiction短路，不保留 nominal release | discard；invariant告警 |
| ACK missing 仅 RFQ I | COMMIT按phase，full/partial=`UNKNOWN` | idle不覆盖缺失ACK；discard |
| unknown schema/enum/untyped connector | COMMIT=`UNKNOWN`；ROLLBACK按no-commit fence | datasource capability不能激活 |
| known outcome 后 reset/health/status fail | DB轴保持 nominal known（仅非 contradiction） | discard/unconfirmed |

对每个明确 write phase 都注入 current positive、current rejection、wrong operation、stale generation、untyped 与 duplicate；这不是抽样。每格断言 terminal frame最多一次、矛盾格 `RELEASED=0`、fault connection pool reuse=0、claim只凭 absence 归还。

### 13.2 CancelRequest late-arrival 与 watchdog

| case | required assertion |
|---|---|
| Execute 在 watchdog CAS 前成功退出，cancel尚未发送 | watchdog输CAS且不得发送cancel；唯一 owner typed rollback |
| 人工把 CancelRequest 延迟到 Execute 成功/RFQ 后 | cancel一旦send started即taint；不得rollback/reset/health；forced discard |
| cancel packet 与拟发送 ROLLBACK frame重排 | 标准PG路径根本不产生ROLLBACK frame；测试捕获到即失败 |
| cancel socket完整写入，服务端处理延迟 | 不等待不存在的ACK；主连接不发任何新command；close/quarantine |
| partial/indeterminate cancel write | 与已发送同等 taint；forced discard |
| statement_timeout 与 CancelRequest 同时 | error归因可记录但不能解除taint；单 terminal/discard owner |
| CancelRequest 迟到可能命中 terminal/reset/health | 被结构性消除：这些命令在tainted连接上计数必须为0 |
| Execute 不理 cancel/quiesce grace超时 | seize/close、无ROLLBACK、迟到result无效 |
| normal/watchdog逐指令 race | 两种CAS胜序均恰好一个result、零double terminal/release |
| future generation-bound fence double | 只有ACK精确绑定generation且pending cancel清空才允许rollback；v0.4生产PG不启用 |

### 13.3 BEGIN/binder/closure

- 未取连接失败、已 pin 未发 BEGIN、full BEGIN rejection、BEGIN ACK 丢失、BEGIN typed ACK 与 NotSent/Zero/Partial 矛盾、native begun 后 SET/bind/seal/compare/audit 分别失败。
- 每格核验是否调用 rollback、最终 disposition、backend 是否消失、claim 是否释放、approval 永远 `consumed_begin_failed`。
- preflight 后并发 DDL；same-tx locks阻止或 actual manifest mismatch并 cleanup。
- PG14–18 分别覆盖 action/write-target/reference golden；plain/partial/expression index、constraint、domain/cast/operator/opclass/collation、whole-row/system column、trigger/default/generated/FK/RLS/rule/view/partition/UDF、reserved schema。
- S0 trigger→`audit_log` 是固定负例，执行前必须拒绝；执行后才发现新 lock 必须 rollback并阻断激活。

### 13.4 WAL、fsync、nonce 与 replay

- primary 在 begin barrier前、statement effect后、commit intent前、intent durable后/COMMIT前、positive ACK后、unknown COMMIT后分别 outage。
- 最大 framed capacity：每个 record 真正占 20KiB；六条恰好120KiB成功；第七条失败；并发耗尽不超卖；header/trailer最大、跨4KiB边界、segment rotation均计费一致。
- write前、partial/torn tail、write后/fsync前、fsync hang、250ms timeout、明确失败、disk full、rotation、kill -9、restart。
- 精确场景：timeout返回LOST → late fsync success → immediate restart → 原事件以late recovery上传 → recovery event追加；原响应仍LOST，绝不出现原始 AUDIT_PENDING。
- key unavailable、wrong key/AAD、rotation crash、manifest未fsync crash、旧segment误 reopen、ordinal回退、duplicate segment id/nonce detector；任一复用企图停止writer。
- 同UUID/同payload幂等；同UUID/异payload停止；乱序、重复、predecessor缺失、per-tx分叉与global chain共同验证。

### 13.5 owner、pool、claim、锁图与网络

- 四/八实例 owner唯一、wrong-owner no-touch、epoch/incarnation ABA、clock skew、control partition。
- owner crash 时分别留下未使用 envelope delta、outstanding dial permit、可能存活 idle backend、active backend、quarantined backend；新实例只得确定未用 delta。
- DB-side terminate成功/失败、inventory不可用/恢复、PID复用、old owner迟到 release；absence前 claim 不得减少。
- `MaxOpenConns <= envelope`；扩池先claim，缩池先关child并确认；migration/health/admin计费。
- control→business→audit 与 release→fence；注入三种反边，静态检查或 runtime assertion 必失败；AgentSQL边不得产生`40P01`。
- DB ACK 后进程 kill、PG restart/primary failover、TLS proxy 单向黑洞；本地 close不得冒充backend absence。

### 13.6 MCP/version/shutdown 与 MySQL外部期望

- SDK 前仅 allow `2025-06-18`；header/body/meta冲突、2025-11-25、2026-07-28、未知版本均拒绝。产品若未来扩 allowlist 需另行签收。
- 2025-06-18 断连不视为立即 cancel；in-flight 由300ms watchdog，请求间由15s idle/60s wall。
- stdio EOF/shutdown：stop admission → freeze → cancel-taint或typed rollback → WAL receipt → disposition → fence。
- 整个 v0.4 MySQL 实验矩阵期间，外部始终断言：`DIALECT_TRANSACTION_UNSUPPORTED`、`b5_tx_mysql=off`、业务连接取得数=0。未来 connector/binder/MDL/network gate全绿也需新设计复审，不能热开。

## 14. S10 定量 activation/扩灰门禁

门禁按 datasource 独立计算；聚合指标不得掩盖单 datasource 失败。所有计数以 typed schema version 可解码为前提，无法解码本身触发 stop。

### 14.1 正常路径零容忍

每个 datasource 在进入下一灰度级前需满足连续至少 24h 且至少 10,000 个 terminal（流量不足则观察满72h）：

- 非 fault-injection 流量 `DBOutcome=UNKNOWN`：**0**；
- `EvidenceConsistency=CONTRADICTORY|UNKNOWN_SCHEMA`：**0**；
- `DISCARD_UNCONFIRMED` 新增：**0**；正常 release/discard 误分类：**0**；
- duplicate terminal、fault connection reuse、late result推进seq、cancel-tainted连接上 terminal/reset/health：均 **0**；
- per-tx/global chain gap、UUID payload conflict、nonce reuse、claim drift：均 **0**。

任何一项非零立即停止自动扩灰。不能以“有解释”豁免正常路径 UNKNOWN；要恢复必须先归类为真实注入流量或修复并重新开始完整观察窗。

### 14.2 fault-injection 期望格

| injected cell | expected result |
|---|---|
| COMMIT NotSent/Zero + Missing | 100% `NOT_COMMITTED`，0 UNKNOWN/COMMITTED |
| COMMIT Partial/Full/Indeterminate + Missing | 100% `UNKNOWN`，0 NOT_COMMITTED/COMMITTED |
| correlated positive/rejection + Full | 100% nominal known outcome；旁路真相0误分 |
| phase×typed reply contradiction、stale/wrong/duplicate | COMMIT 100% UNKNOWN；ROLLBACK仅no-commit proof时100% NOT_COMMITTED；100% non-release |
| standard PG CancelRequest emitted/indeterminate | 100% forced discard；ROLLBACK/reset/health frame数=0 |
| cancel未发且normal CAS先胜 | 100%唯一typed rollback owner；cancel packet数=0 |
| fsync timeout | 100% response LOST；late success/restart后旧响应改写数=0 |
| owner crash/inventory partition | absence前claim release数=0；新实例占用unknown child slot数=0 |

每一格的 observed count 必须精确等于注入请求数，格外 UNKNOWN/contradiction/quarantine 必须为0，旁路数据库真相误分类为0。PG major×TLS×direct/proxy 每格至少1,000次；单向黑洞、restart/failover等重型格每组合至少100次。未标注为期望的 UNKNOWN 一律失败。

### 14.3 quarantine、inventory 与 claim 阈值

设 datasource hard budget 为 `B`：

- 自动扩灰 stop threshold：`quarantine_count >= max(1, ceil(0.01 × B))`，或 `quarantine_charged_slots / B >= 1%`，任一即停；也就是说小池出现1个 quarantine就停止扩灰。
- inventory 健康时，任一 quarantine 年龄 `>30s` 停止新 B5 begin；`>5min` 自动把该 datasource B5 flag降回 off，既有 tx 仍按 watchdog处理。
- backend inventory 首次失败立即停止自动扩灰；连续不可用 `>30s` 停止新 B5 begin。恢复后至少两次独立 inventory 周期且间隔≥30s一致，才可申请人工恢复。
- `claim_drift = ledger_charged - independently_observed_expected` 必须精确为 **0**；正负任一非零立即停止新 begin和扩灰。不得用容差掩盖 backend/claim 泄漏。
- envelope child 未完全对账时，只允许确定未使用 delta；若计算出现负 delta、重复 child identity或未知 dial permit，立即 stop。

### 14.4 自动停止与人工恢复权限

stop 由 runtime gate 自动执行，权限只允许把范围缩小/flag关掉，不依赖人工在线。恢复/扩大只能由 Runtime/SRE on-call 与 QA/Release 两个不同主体双人批准；若涉及 evidence mapping、security/UUID/nonce 或 DB misclassification，还必须加入 Architecture 或 Security owner。Product 单独不能绕过安全门禁。恢复前需：根因记录、受影响 datasource 对账、claim drift=0、quarantine清零或每项有 backend absence proof、修复版本、对应 fault格重跑全绿，以及重新开始观察窗。

## 15. 按角色签收清单

| role | activation 前必须签收 |
|---|---|
| Architecture | EvidenceConsistency短路表、BEGIN cleanup、terminal/disposition总函数、无环slice DAG、原理边界 |
| Product | planned-only；tx内SELECT/RETURNING/复杂DML拒绝；legacy 300ms；60s仅授权上界；PG-first/MySQL off |
| DB Core | typed phase/correlation、NoCommit proof、完整Finish lifecycle、contradiction绝不release |
| PostgreSQL owner | PG14–18 ABI/hash、same tx/backend、RFQ/typed reply、deferred rejection、标准CancelRequest late-arrival forced discard |
| Security | digest/typed absence、action/write/reference grants、reserved object、proof versions、AEAD/AAD/KMS、nonce不复用证明 |
| MCP owner | pre-SDK allowlist、single operation/terminal CAS、HTTP/stdio/shutdown、无double terminal |
| Runtime/SRE | directory/route、pool child/envelope、owner-crash reaper、quarantine阈值、inventory stop、claim drift=0、RBAC恢复 |
| Audit/B6 | event v4不自指、receipt v1、UUID/double-chain、late-fsync/recovery、sink无callback |
| Storage/SRE | 20KiB framed charge、120KiB reservation、fsync/device边界、segment manifest/key/rotation/restart |
| Control/Fence | approval CAS/consumed_begin_failed、revoke feed、无business→control反边、disposition后fence |
| QA/Release | 第13节每格期望与第14节阈值、真网络证据、0误分类、自动stop/人工恢复演练 |
| MySQL owner | v0.4只签 off/unsupported；未来独立复审 |

## 16. 仍需负责人拍板的产品/部署参数

这些决策不改变三个 P0 的安全映射，但实现/activation 前必须填写：

1. Product/Architecture 是否接受 legacy HTTP active call 的300ms hard watchdog；不接受则只能先 qualify 新 transport/protocol，不能删除 watchdog。
2. Product 是否接受 v0.4 transaction 内 SELECT 与对外 RETURNING 均拒绝。
3. Security/Product 是否接受 hard revoke 60s 仅为授权上界，物理锁因 quarantine 可更久。
4. Audit/SRE 是否接受本地 WAL 不覆盖节点与本地盘共同永久丢失；跨故障域同步 durability 若需要，应立独立项目。
5. Runtime/SRE 为每 datasource 填 `db_hard_budget`、external/admin reserve、pool envelope、inventory实现与比本文更严或相等的阈值；不得用相同 per-instance ConnLimit。
6. Storage/SRE 以目标文件系统/介质验证250ms barrier、4KiB alignment、目录fsync与设备 durability；S0 的1.532ms不是生产SLO。
7. PostgreSQL/Architecture 是否永远采用标准PG cancel的forced-discard基线；若未来引入 generation-bound fence，必须新复审，不能按配置声称已有。
8. MySQL/Architecture 是否另立未来版本目标；v0.4没有开启路径。

## 17. 原理边界

- business DB 与 audit store 不原子；primary intent + local WAL + reconciliation 不是 XA。
- COMMIT 可能到达服务端而 ACK 丢失时只能是 `UNKNOWN`，禁止自动重试。
- 内部证据自相矛盾说明 connector/归因边界已破坏；即使某条 ACK 看似可信也不能复用连接。
- PG CancelRequest 没有 operation generation/ACK；主连接当前 RFQ 干净不能证明另一路 cancel 已消费。
- positive DB ACK 后的审计灾难不能撤销提交事实；audit durability 必须独立报告。
- response 时 LOST 可以后来恢复审计事实，但历史响应不可倒改；canonical业务事件不声称自身存储成功。
- active native transaction不能迁移；本地 Close/FIN/RST 不证明服务端 backend/锁消失。
- local WAL fsync 不覆盖节点+盘共同丢失或设备谎报 durability。
- 通用 SQL 的隐式对象闭包不能靠名称匹配近似；正式 ABI 未覆盖只能拒绝。
- lock DAG只证明 AgentSQL 控制的边无环；外部业务 deadlock仍可能发生，处理为整笔 rollback。
- strong directory/admission/KMS/inventory不可用时，B5牺牲可用性并 fail closed。

## 18. 最终实现准入

**可以进入全 feature-flag-off 实现。** 起始切片是 S4a、S5a、S7a，其中 S4a 的 EvidenceConsistency/PG typed-phase/late-cancel spike为首要路径；三项合同完成后才允许冻结 S1b。S3 的纯 claim/reaper spike可以并行，但任何持久 schema 必须等待 S1b。

在 S10 全矩阵、定量门禁、角色签收、datasource参数、internal canary全部完成前：`b5_sessions`、`b5_tx_postgres` 不得启用，`b5_tx_mysql` 始终 off/unsupported。对外事实仍是 MCP单call单语句、HTTP stateless、B2 SELECT事务外、没有可用的跨请求生产事务。

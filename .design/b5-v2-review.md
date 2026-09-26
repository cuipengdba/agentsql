# B5 v2 独立安全/架构评审

结论：**GO-WITH-REQUIRED-CHANGES**

已完整读取：

- `.design/b5-mcp-transaction-design-v2.md`
- `.design/b5-s0-findings.md`
- `.design/b5-v1-review.md`

并定点核对了 `internal/mcpserver`、`internal/authorizedexecute`、`internal/columnauth`、`internal/store`、`internal/lockrank` 中相关实现。

工作区是只读沙箱，写入 `.design/b5-v2-review.md` 被明确拒绝，因此完整评审如下。未修改代码、未提交 Git。

## 总体裁决

v2 的总体方向正确，明显修复了 v1 的核心架构问题：

- digest 已正确分层；
- 裸 session id 不再构成授权；
- wrong owner 明确不得 cleanup；
- DB outcome、audit durability、connection disposition 已分离；
- legacy MCP 不再相信 SDK 断连取消；
- PG 隐式对象采用默认拒绝；
- MySQL 收缩为 off；
- quota、连接和字节预算升级到集群级。

但不能接受文档当前“7 个 P0 均设计关闭”的表述。仍有五个 release-blocking 合同缺口，不能全部下放成“实现时验证”。

允许在所有 feature flag 保持 off、MySQL 固定 `unsupported` 的前提下继续开发和 spike；不允许冻结全部 S1 合同，也不允许启用 `b5_sessions`、`b5_tx_postgres` 或 `b5_tx_mysql`。

## 7 个原 P0 的关闭判定

| 原 P0 | 判定 | 评审意见 |
|---|---|---|
| P0-1 digest 循环 | **关闭** | `plan_digest` 不含 approval id，approval 只绑定 stable plan，CAS 后计算 `begin_authorization_digest`。依赖图无环且可实现。 |
| P0-2 capability/owner/routing | **部分关闭** | HMAC、principal binding、强一致目录、epoch/incarnation、sticky route、wrong-owner no-touch 足以防裸 ID 劫持和第二 owner 接管；但 owner 失联后的 DB connection lease 回收尚未与物理连接终止绑定。 |
| P0-3 typed terminal | **未关闭** | ABI 方向正确，但 evidence 映射不是穷尽函数；PG 所需 send/ACK 阶段证据也尚未证明可由当前接口产生。 |
| P0-4 MySQL 有界终结 | **以范围排除关闭 v0.4 GA** | S0 只证明本地 watchdog/Close/discard 有界，不证明服务端锁释放。MySQL 保持 off 是正确裁决。 |
| P0-5 MCP 取消/版本 | **部分关闭** | SDK 前 allowlist 正确；独立 watchdog 正确。但 watchdog 与仍在运行的 Execute 如何安全 quiesce/rollback/discard 没有定义。 |
| P0-6 audit durability | **未关闭** | 状态拆分和 intent 屏障正确；但 WAL 记录不足以重建完整事件，fsync/重放冲突合同也不完整。 |
| P0-7 quota/tombstone | **部分关闭** | 全局计数、byte/churn cap、digest-only tombstone 正确；但 stateless pool claim 和 orphan connection 的安全回收没有闭合。 |

## P0：必须修订后才能冻结或激活

### P0-1：PG binder 的 native BEGIN 顺序自相矛盾

位置：v2 §6.2 `begin_transaction` 步骤 7（约 L165）与 §8.1（约 L241-L244）。

§6.2 写的是：

> 在业务 connection 上重跑 binder/manifest……随后才 native BEGIN。

§8.1 则要求 prepare → manifest → seal → execute 全部位于：

> 同一 connection、同一 native transaction、同一 backend PID。

若按 §6.2 实现，最终 binder 与执行之间没有相同事务快照和持续持有的 OID lock，DDL TOCTOU 会重新出现。

现有 SELECT binder 已证明正确顺序可实现：先 `BeginTx`，再 lock relations、prepare、读取 manifest、seal，并把同一 tx/connection 交给执行，见 `postgres_binder.go:237-298`。

修复方向：

1. acquire pinned physical connection 和 connection token；
2. native BEGIN；
3. `SET LOCAL` role/search_path/timezone/statement/lock timeout；
4. 在同一 tx 中 bind、closure、lock、seal；
5. 与 preflight plan manifest/digest 比较；
6. `tx_begin` audit barrier；
7. 返回 ACTIVE。

比较失败必须 typed rollback/discard，并将 approval 标为 `consumed_begin_failed`。所有 dependency fence 必须保持到 terminal。

### P0-2：terminal evidence 不是总函数，无法唯一实现

位置：v2 §6.3、§6.4、S4。

当前问题：

1. 枚举有 `TerminalNotSent`，映射表没有对应行。
2. ROLLBACK 的 zero-write、partial/indeterminate、acked-error 等组合没有逐项定义。
3. `TerminalSentAcked` 没区分：
   - positive COMMIT ACK；
   - COMMIT ErrorResponse/ERR packet；
   - ACK 后 clean/dirty/unknown transaction status。
4. PG deferred constraint 在 COMMIT 时失败，是必须明确分类的实际情况。
5. “未知枚举默认 UNKNOWN+discard”不能替代所有已知枚举组合的穷尽映射。
6. 当前 PG 实现最终仍是 `pgx.Tx.Commit(ctx)`/`Rollback(ctx)` 风格，error 时 hijack/close；S0 没有提供 PG send/ACK fault-window 证据。仅写“由 pgx protocol phase 产生”仍可能是纸面承诺。

修复方向：

冻结总映射：

```text
operation
× terminal write phase
× protocol reply/server tx status
→ DBOutcome + ConnectionDisposition
```

至少拆分：

- positive commit ACK；
- definitive commit rejection/rolled-back；
- rollback ACK；
- ReadyForQuery idle/failed/in-transaction；
- zero bytes；
- partial/indeterminate write；
- ACK missing；
- close/discard confirmed/unconfirmed。

`FinishCommit/FinishRollback` 应拥有完整物理连接生命周期，内部完成 release、hijack 或 close。只有 reset/health/status 全部通过才允许 `ConnectionReleased`。

PG14–18 必须增加：

- before-send；
- zero/partial write；
- positive ACK；
- deferred-error ACK；
- ACK loss；
- proxy reset；
- PG restart/failover；
- pool non-reuse。

如果 connector 无法生成 typed phase，只能返回 `UNKNOWN + discard`，不能用错误文本或 SQLSTATE 文本补推断。

### P0-3：owner loss 与 connection lease reclaim 没有共享同一 fence

位置：v2 §4.2、§11.1、§11.2、S3。

目录唯一 owner、epoch/incarnation 和 wrong-owner no-touch 是成立的。缺口在 connection lease 的回收条件。

若 owner heartbeat 失效或租约过期就归还 datasource connection token，失联或挂死 owner 的业务 backend 可能仍然存活。S0 已明确：客户端本地 Close 不证明服务端在单向黑洞中及时收到断连并释放事务。

此时新实例可以得到新 token，导致：

- orphan backend 与新连接同时存在；
- 真实连接数突破预算；
- 原事务可能仍持锁；
- shared counter 虽然“正确”，却不再对应真实数据库资源。

修复方向：

connection lease 必须有独立状态机：

```text
in_use → terminating/quarantined → free
```

只有满足下列条件之一才能转为 free：

- typed `RELEASED`；
- typed `DISCARDED` 且确认 backend 已终止；
- DB-side kill/连接清单对账确认；
- 经过已经 attestate 的服务端失活上界并完成检查。

`DISCARD_UNCONFIRMED` 必须继续占用预算，不能因 owner lease 过期自动释放。

此外，stateless pool、migration、health connection claim 也必须在实例启动或扩池前注册共享 claim，并与实际 `MaxOpenConns` 联动。只给 pinned B5 connection 发 token，不能实现 §11.2 的总连接公式。

### P0-4：emergency WAL 记录不足以完成承诺的 reconciliation

位置：v2 §9.2、§9.3、§9.5。

WAL 格式列出了：

- EventUUID；
- tx id digest；
- event kind；
- DB outcome；
- primary payload digest；
- timestamp、CRC 等。

但没有完整 canonical event payload。仅凭 payload digest 无法重建：

- `plan_digest`；
- `begin_authorization_digest`；
- transaction seq；
- previous event digest；
- evidence phase；
- connection disposition；
- 完整 audit 状态。

因此，“使用同一 EventUUID 写回 primary audit”目前不可实现。

其他缺口：

- `ON CONFLICT DO NOTHING` 不能静默接受“同一 UUID、不同 payload”；必须读取已有记录并比较 semantic digest。
- `AUDIT_PENDING` 只能在实际 fsync 完成后报告。
- disk hang 超过 250ms 时，不能同时声称“hard budget”和“WAL 已耐久 pending”。
- 当前 audit store 是串行 global chain head；晚到事件如何保持 transaction seq/previous digest 可验证，需要明确。
- “为 intent/terminal 预留空间”没有覆盖最坏情况下的 rollback、terminal、session-terminal 等事件数。

修复方向：

WAL 应持久化：

- 完整版本化 canonical event；
- EventUUID；
- transaction seq；
- previous transaction event digest；
- payload digest；
- schema/key id；
- AEAD 或等价完整性保护；
- reservation id/容量计费。

重放遇到相同 EventUUID 时必须比较 payload digest；不相等属于高危冲突，不能视作成功。

增加以下 release-blocking 测试：

- disk hang，而不只是 disk full；
- fsync timeout；
- key unavailable；
- 相同 UUID、不同 payload；
- DB ACK 后立即进程崩溃；
- 晚到、乱序、重复 replay；
- global chain 与 per-transaction chain 联合验证。

fsync 未确认只能报告 `DURABILITY_LOST`，不能报告 `AUDIT_PENDING`。

### P0-5：legacy 300ms watchdog 缺少安全的 in-flight 终结协议

位置：v2 §5.2、§6.2 execute_write、S2。

S0 证明独立 timer 能在一个 PG 场景触发 rollback，但未证明：

> slow/blocked Execute 尚未返回时，另一 goroutine 可以安全地在同一 physical connection 上调用 Rollback。

直接“cancel statement，然后使用独立 cleanup context rollback”可能造成：

- 同一协议连接上的并发命令；
- Execute 与 ROLLBACK 交错；
- double terminal；
- 驱动状态损坏后误 release。

修复方向：

定义单 owner operation CAS 和 quiesce：

1. watchdog 原子禁止新 operation；
2. 发出 statement cancel；
3. 在小预算内等待 Execute 明确退出；
4. 如果无法 quiesce，先夺取/关闭 physical socket并 discard，不再发送 ROLLBACK；
5. 只有 Execute 已结束且 connector 明确可继续时，发送一次 typed rollback。

测试必须覆盖：

- DB lock-wait；
- slow statement；
- 驱动忽略 request cancellation；
- HTTP 断连；
- watchdog 与正常返回竞态；
- stdio EOF/shutdown；
- 从未发送 COMMIT 时的 `NOT_COMMITTED` 证据。

## P1：实现前应补齐

1. **MySQL gate 要量化**

   §7/§14.2 的四类 gate 分类合理，但“锁释放时间超标”缺少数字、观测点和重复次数。应按 datasource/patch 冻结：

   - session/server timeout 变量及读回方式；
   - client terminal API 上界；
   - server backend/lock disappearance 上界；
   - 网络故障矩阵和明确失败阈值。

   MySQL 保持 off 时，这不阻塞 PG GA。

2. **continuation key lifecycle**

   §4.1 应明确：

   - `Ksession` 与返回 secret 的 KDF；
   - directory ciphertext 的 AEAD/AAD，至少绑定 session、principal、owner epoch；
   - KMS/key rotation/rewrap；
   - 缓存清除；
   - verifier digest 的具体用途；
   - idempotency 命中与 current-seq 检查顺序。

   成功请求重放时，seq 已推进；如果先检查 current seq 再查 idempotency cache，同-body retry 会错误失败。非 mutation 请求在 proof 中如何编码 `expected_seq` 也需冻结。

3. **hard revocation 的“立即”没有可靠合同**

   当前只写“无阻塞通知”，却称 hard revoke 立即 cleanup。需定义：

   - durable monotonic feed；
   - 丢消息恢复；
   - 最大传播延迟；
   - 乱序处理；
   - control partition 行为。

   否则诚实上界仍是 transaction wall 60s，而不是“立即”。

4. **PG closure 矩阵仍需显式补项**

   当前 fail-closed 原则和 trigger/FK/default/generated/rule/RLS/view/partition/UDF 拒绝已较完整，但建议明确列出：

   - plain/partial/expression index；
   - domain/cast/operator class/collation；
   - deferrable/unique/exclusion constraint；
   - whole-row/system-column use；
   - control/audit 保留 schema/OID。

   对每类说明是 object lock、sealed-plan invalidation、fingerprint，还是直接拒绝。业务 DML 必须禁止指向内部 control/audit 对象。

5. **当前 lockrank 不能完成文档中的证明**

   现有 `internal/lockrank` 只有：

   ```text
   Control(10) → Business(20)
   ```

   而且 tracker 仅存在于单个 context。异步 group commit、独立 cleanup context 不会自动继承“业务锁已持有”的事实。

   S7 需要：

   - Audit rank/capability；
   - 静态 callsite manifest；
   - 禁止 callback 的接口边界；
   - 跨 goroutine 的结构性约束；
   - 注入反边时 assertion 必须失败的测试。

   “无 40P01”只能限定为 AgentSQL 控制的组件边，不能表述成数据库全局保证。

6. **审计事件字段不是所有阶段都可构造**

   §9.5 说所有事件都有 transaction id 和 `begin_authorization_digest`，但 `tx_plan_decision` 也覆盖 begin 前拒绝；此时这两个字段尚不存在。

   应为每种事件定义 required/optional/not-applicable schema，不能使用空字符串代替 typed absence。

7. **错误码 effect 不够确定**

   §12.2 中的：

   - “slot 可重试”；
   - “新事务可重试”；
   - “有效 owner 上冲突可标 rollback-only”

   都应改成机器可验证的固定 boolean 和固定 tx effect。

   `AUDIT_UNAVAILABLE` 在 commit 前和 known commit 后含义不同，应按 phase/state 或不同错误码消除歧义。

8. **资源计费还需覆盖 metadata 自身**

   tombstone 256B 是 charge，不是实际数据库行、索引或 allocator 占用。应按固定 serialized size 加保守 metadata/index headroom 计费。

   principal/tenant token-bucket 自身也必须有 TTL 和 cardinality cap，否则高基数身份可以制造无界限流状态。

9. **切片缺少依赖 DAG**

   S1 不应在 S4 terminal total table、S5 binder contract、S7 WAL event format 尚未确定时宣称冻结对应 schema。

   建议依赖关系：

```text
S4 terminal contract ─┐
S5 binder contract ───┼→ 回填并冻结 S1
S7 WAL/event contract ┘

S3 + S4 + S5 + S7 → S6 coordinator
S2 + S6            → S8 tools
S1–S9              → S10 activation gate
```

## P2：可随实现整理

1. 文中“三轴”有两种含义：

   - driver terminal：DB/connection/evidence；
   - 客户端终态：DB/audit/connection。

   审计事件又称“四轴。建议统一术语，把 evidence 定义为 proof metadata，不作为第四个业务状态轴。

2. 明确定义：

   - `TerminalNotSent` 与 `TerminalZeroBytesWritten` 的边界；
   - `Discarded` 与 `DiscardUnconfirmed` 的完成条件。

3. 300ms legacy watchdog、250ms audit barrier 和最多 5s statement 默认值应给出有效预算公式。legacy HTTP 下的实际 operation 上限就是 300ms，产品需要明确签收其可用性影响。

4. 所有 S0 时延只应作为 regression baseline，不能在测试中被错误固化为跨环境 SLO。

## 可开始的切片

在全部 flags off 下，可以开始：

- S1 中 canonical encoder、digest golden、enum/schema migration 骨架；terminal/WAL/event schema先标 provisional。
- S2 的 pre-SDK exact allowlist、bounded body replay、HMAC envelope 和负向 proof 测试。
- S3 的强一致目录、epoch/incarnation、wrong-owner no-touch、非连接类 admission 和 tombstone 原型。
- S4 的 PG/MySQL typed connector spike 和 fault injection；MySQL 始终 unsupported。
- S5 的 PG DML ABI、closure detector、拒绝矩阵，但必须先修正 BEGIN 顺序。
- S7 的 WAL/chain spike，但应先修订完整 record/replay schema。

现有 `internal/columnauth/authorizer.go` 明确拒绝所有非 SELECT，因此 S5 不能假定复用当前 column authorizer 即可支持 DML；需要独立 DML usage/action 授权合同。

## 不得激活的部分

以下五项 P0 未关闭前，不得激活 S6/S8 的任何生产能力，也不得进入 PG datasource canary：

1. PG binder/native BEGIN 唯一顺序；
2. terminal total evidence mapping 和 PG typed phase；
3. owner-loss connection quarantine/reclaim；
4. 可真正重建事件的 emergency WAL；
5. watchdog quiesce/forced-discard 协议。

此外：

- S9 运维面可以开发，但 stale directory 或缺失 tombstone 不得显示成“已回滚”。
- `b5_tx_mysql` 不得因 S4 connector spike 通过而开启。
- MySQL 必须等待 §7 四 gate 全绿并另行设计复审。

## 仍需真库、真网络实证

### PostgreSQL terminal

PG14/15/16/17/18：

- positive COMMIT ACK；
- COMMIT deferred-error；
- zero/partial send；
- ACK loss；
- 单向 reset/blackhole；
- restart/failover；
- client crash；
- connection pool non-reuse。

### MCP

- 2025-06-18 下 slow/blocked DML 的 watchdog-quiesce-discard；
- watchdog 与正常返回竞态；
- 2025-11-25/2026-07-28 继续在 SDK 前拒绝；
- header/body/meta 冲突；
- 请求间只验证 idle/wall，不宣称断连立即 rollback。

### Owner/admission

- control partition、business DB 仍可达；
- owner pause、GC stall、hang、crash；
- lease ABA；
- clock skew；
- KMS 不可用和 rotation；
- stale owner cleanup；
- DB backend 未消失前不得重新发 token。

### Audit

- intent 前后；
- DB ACK 前后；
- WAL write/fsync 各窗口；
- disk full、disk hang；
- torn tail；
- key loss；
- node restart；
- 同 UUID 不同 payload；
- 晚到/乱序 replay；
- global-chain verification。

### PG closure

按各 PG major 验证：

- trigger；
- FK；
- default/identity；
- generated；
- partial/expression index；
- RLS/rule/view；
- partition/inheritance；
- UDF/domain/constraint；
- concurrent DDL；
- reserved internal schema 拒绝。

### 锁图

- control→business→audit；
- release→final fence；
- async batch/cleanup context；
- 注入 audit→control 或 audit→business 回调时必须由 assertion/test 拒绝；
- 真库不得出现由 AgentSQL 组件边导致的 `40P01`。

### MySQL

保留 S0 四窗口回归，并补：

- 跨主机 client→server 单向黑洞；
- server→client 单向黑洞；
- backend/lock disappearance；
- server timeout attestation；
- DB failover。

全部通过前，测试预期应始终是 `DIALECT_TRANSACTION_UNSUPPORTED`。

## 负责人签收建议

- **Architecture**：唯一 BEGIN/bind 顺序、terminal total table、切片依赖 DAG、final fence。
- **Security**：digest schema、HMAC/KDF/key rotation、防枚举、idempotency ordering、hard-revoke 上界、PG closure。
- **PostgreSQL owner**：各 major ABI/hash、same tx/backend、dependency fence、terminal phase evidence、拒绝矩阵。
- **MCP owner**：SDK 前 allowlist、watchdog quiesce/forced-discard、stdio/shutdown、legacy 文案。
- **Runtime/SRE**：目录和 sticky route、owner-loss cleanup、connection quarantine、stateless pool claim、cluster budgets、KMS/WAL/pool runbook。
- **Audit/B6 owner**：完整 WAL event、fsync 语义、reservation、UUID 内容冲突、late replay/global-chain、无 callback 边界。
- **Control/Fence owner**：approval CAS、admission 不跨 business DB lock、final fence 只在 release/discard 后、hard-revoke feed。
- **QA/Release**：真库/真网络矩阵、所有负属性、canary stop condition；任何未解释的 `UNKNOWN` 或 `DISCARD_UNCONFIRMED` 阻止扩灰。
- **Product**：PG-first、tx 内 SELECT/RETURNING/复杂 DML 拒绝、legacy 300ms 上限、normal policy 最长 60s 陈旧、本地 WAL 故障域。
- **MySQL owner**：v0.4 只签收 off/unsupported；未来开启需独立复审。

## 最终意见

PG-first、MySQL off 是合理且必要的 GA 收缩。

v2 已把多数风险从模糊承诺改成可测试的工程方向，但尚未真正关闭全部 7 个 P0：

- P0-1 可认定关闭；
- P0-4 仅以 MySQL 排除出 v0.4 GA 的方式关闭；
- P0-2、P0-3、P0-5、P0-6、P0-7 仍有上述实质缺口。

修完五项 P0 后，可将结论升级为“设计 GO，进入全 flag-off 实现”。只有 S10、真库/真网络证据和逐项 owner 签收完成后，才允许按 datasource 开启 PostgreSQL。MySQL 继续保持 `unsupported`。
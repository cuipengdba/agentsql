# B5 v3 独立安全/架构评审

结论：**GO-WITH-REQUIRED-CHANGES**

v3 可以进入全 feature-flag-off 实现，但不能接受文首“五个 P0 均已设计关闭”的结论。我的判断是：

| v2 五个 P0 | 结论 |
|---|---|
| native BEGIN / binder TOCTOU | **关闭** |
| terminal 总函数 | **未彻底关闭** |
| connection lease / owner loss | **设计关闭，待实证** |
| emergency WAL | **未彻底关闭** |
| watchdog quiesce | **未彻底关闭，并引入 late-cancel 新窗口** |

因此：

- 可以开始全 flag-off 实现和 fault-injection spike。
- 不得冻结 S1 的 terminal/WAL 相关 schema。
- 不得启用 `b5_sessions`、`b5_tx_postgres`。
- `b5_tx_mysql` 继续强制 off/unsupported。
- 以下 P0 修订完成前，v3 不能升级为无条件设计 GO。

只读评审，未修改文件、未提交 Git；因沙箱只读，未写入 `.design/b5-v3-review.md`。

## P0

### P0-1：terminal 对矛盾证据的映射不安全，ConnectionDisposition 表与正文冲突

位置：[b5-mcp-transaction-design-v3.md](</D:/ruanjiansheji/agentsql-v04/.design/b5-mcp-transaction-design-v3.md:239>)、尤其 L243–276、L822–836。

问题：

1. DBOutcome 表把 `CommitPositiveACK + 任意已知 write phase` 一律判为 `COMMITTED`，包括 `TerminalNotSent`、`TerminalZeroBytesWritten`、`TerminalPartialBytesWritten`。
2. 文中承认这种 phase 矛盾“不可 release”，但 ConnectionDisposition candidate 表只看 reply + server status：`positive ACK + ServerIdle` 仍是 candidate=yes，经过 reset/health 后可能变成 `RELEASED`。
3. ROLLBACK 也有同类问题：`RollbackPositiveACK + TerminalNotSent + ServerIdle` 可以进入 release candidate。
4. 这是总函数最需要覆盖的“内部证据矛盾”组合。若 connector 同时声称没有发送 terminal frame，又声称收到当前 terminal 的 ACK，至少有一个证据被错误归因、来自旧命令或 connector 状态已破坏。不能选择相信 ACK 并继续复用连接。
5. 第 16.1 节覆盖了 ACK 与 RFQ 矛盾，却没有覆盖 ACK 与 write phase 矛盾。

修复：

- 在 DBOutcome/ConnectionDisposition 前增加冻结的 `EvidenceConsistency` 校验。
- `NotSent/Zero/Partial + 当前 terminal typed ACK/rejection` 等不可能组合固定为：
  - COMMIT：`UNKNOWN + discard`；
  - ROLLBACK：只有独立可信的 `NoCommitEverSent` 可保留 `NOT_COMMITTED`，但连接仍必须 discard；
  - 所有矛盾组合禁止 `RELEASED`。
- 明确哪些 phase 可以被 typed reply 合法覆盖。通常 positive/rejection reply 至少要求 `FullFrameWritten`；若允许 `WriteIndeterminate`，需写明 reply correlation 为更强证明。
- 在第 16.1 节增加所有 phase×reply contradiction、stale reply、wrong-operation reply、duplicate RFQ/ACK 测试。

这项不修，P0-2 “terminal 总函数”不能认定关闭。

### P0-2：PG CancelRequest 没有 operation generation，存在迟到取消命中 ROLLBACK 的新竞态

位置：[b5-mcp-transaction-design-v3.md](</D:/ruanjiansheji/agentsql-v04/.design/b5-mcp-transaction-design-v3.md:319>) L319–331、L850–856。

v3 的 freeze → CancelRequest → 等 Execute 退出 → typed ROLLBACK 方向正确，但仍遗漏 PostgreSQL 的协议事实：

- CancelRequest 走独立连接，携带 backend PID/secret，没有 statement generation，也没有服务器 ACK。
- watchdog 发出 CancelRequest 后，原 Execute 可能自然结束；CancelRequest 仍可能在网络中迟到。
- coordinator 看到 Execute 已退出、主连接已回到 RFQ `I`，随后开始 ROLLBACK；迟到的 CancelRequest 可能取消这个 ROLLBACK。
- “read-loop ended、无 pending frame、protocol synchronized”只能证明主连接当前边界干净，不能证明另一 TCP 路径上的 CancelRequest 已被消费或丢弃。

这会重新产生 terminal 命令受异步取消干扰、错误分类和潜在误 release。

修复：

- 对标准 PG CancelRequest，安全基线应是：**watchdog 一旦实际发出 out-of-band CancelRequest，该 physical connection 默认 forced discard，不再发送 ROLLBACK**。
- 只有 connector 能提供强于标准 PG CancelRequest 的 generation-bound、可确认 cancel fence，才允许取消后 typed rollback。
- Execute 在 watchdog CAS 前已经正常退出、且 CancelRequest 尚未发送时，仍可由唯一 terminal owner typed rollback。
- 增加 release-blocking 测试：
  - CancelRequest 人工延迟到 Execute 成功返回之后；
  - cancel 与 ROLLBACK frame 重排；
  - cancel socket write 完成但服务端处理延迟；
  - statement_timeout 与 CancelRequest 同时发生；
  - 迟到 cancel 不得命中 terminal/reset/health probe。

当前测试矩阵覆盖 watchdog/normal race，但没有覆盖这个跨连接 late-cancel 窗口。因此 P0-5 尚未彻底关闭。

### P0-3：WAL reservation 字节模型不足，且 AuditDurability 存在自指/迟到 fsync 语义缺口

位置：[b5-mcp-transaction-design-v3.md](</D:/ruanjiansheji/agentsql-v04/.design/b5-mcp-transaction-design-v3.md:472>) L472–517、L539–563、L861–869。

#### 3.1 `6 × 16KiB` 不能从当前格式推出足够

文档规定：

- canonical event 最大 16KiB；
- reservation 为 `6 × 16KiB = 96KiB`；
- WAL record 另外还有 header、nonce、AEAD tag、CRC32C、长度/对齐及潜在 segment framing。

如果 16KiB 是 CanonicalEvent 上限，则六条最大事件的物理 WAL 占用必然大于 96KiB。文中“含 envelope”没有明确包含 `RecordHeader + Trailer + alignment`，而 record 格式又把它们置于 CanonicalEvent 外。

记录数六条在“首次 statement audit 失败即 rollback-only”的路径下基本合理，但缺少由事件状态机生成的最大路径证明。

修复：

```text
max_record_charge =
  align(record_header
        + max_ciphertext
        + AEAD_tag
        + CRC/trailer
        + segment/index/accounting overhead)

reservation = max_path_record_count × max_record_charge
```

并由事件状态机枚举证明，任何 primary 最后成功点之后最多需要多少条：

- statement；
- rollback；
- terminal outcome；
- session terminal；
- durability diagnostic；
- recovery/headroom。

容量测试要使用最大 framed record，而不只是接近 16KiB 的 plaintext event。

#### 3.2 CanonicalEvent 内的 `AuditDurability` 与 fsync 结果相互依赖

要写 WAL 前必须先生成 immutable canonical payload，但：

- fsync 成功时，响应应为 `AUDIT_PENDING`；
- fsync timeout/失败时，响应应为 `DURABILITY_LOST`；
- CanonicalEvent 自身又包含 `AuditDurability`。

尤其 fsync timeout 后可能迟到成功：文档要求返回状态不能倒改，但 segment 中可能已经存在一个预先编码为 `AUDIT_PENDING` 的事件。恢复时是重放该事件、丢弃它，还是追加修正，目前没有唯一规则。

修复方式二选一：

- 从被保护业务事件中移除“其自身存储是否成功”的自指字段，将 durability 作为 WAL/primary receipt 与派生终态；或
- 明确定义 `reported_durability_at_response`、`append_confirmation_state` 和后续 recovery event，规定 timeout 后迟到 record 不得被当作原始 `AUDIT_PENDING` 静默重放。

还应增加“fsync timeout → API 返回 LOST → fsync 迟到成功 → restart/replay”的精确期望。

在这两个问题冻结前，P0-4 “完整可重建 WAL + reservation”不能认定彻底关闭。

## P1

### P1-1：BEGIN 失败清理还不是完整状态函数

位置：v3 L153–181。

唯一 BEGIN 顺序本身正确，并与现有 SELECT binder 一致。现有代码确实先 `BeginTx`，随后 lock、prepare/manifest、seal，并持有同一 tx/connection 返回，见 [postgres_binder.go](</D:/ruanjiansheji/agentsql-v04/internal/authorizedexecute/internal/businessdb/postgres_binder.go:237>)。

但状态机把 `APPROVAL_CONSUMED..BEGIN_AUDITING` 的失败统一导向“typed finish + disposition”。这些阶段实际有三种不同资源形态：

- 尚未取得 connection；
- connection 已 pinned，但 native BEGIN 没有成功；
- native transaction 已开始。

应冻结独立 begin-cleanup 表。无 transaction 时不能假装调用 `FinishRollback`；BEGIN ACK 丢失时也必须区分连接复用、discard、quarantine，并仍保持 `consumed_begin_failed`。

### P1-2：pool envelope 的 owner-crash 回收合同需补齐

位置：v3 L364–385。

`DISCARD_UNCONFIRMED` 继续占预算、backend absence 才 free 的主合同是正确的，P0-3 可认定设计关闭。

仍需明确：

- pool 中每个已建立 backend 是否都有 durable child identity record；
- owner crash 后如何区分 envelope 中“未实际使用的预留容量”和“可能仍存活的 idle backend”；
- envelope claim 何时可缩减或转移；
- 所有 child backend 未对账时，新实例能否只取得确定未使用的 delta。

保守地永不释放整个 envelope 是安全的，但会永久耗尽容量；实现前需要可恢复的 reaper 合同。

### P1-3：切片 DAG 无逻辑环，但粒度会造成工程上的隐含环

位置：v3 L751–797。

图本身无环，方向也符合 v2 review。但 `S4/S5/S7 → S1 freeze` 中的 S4/S5/S7 同时被描述为完整实现切片；这些实现又会依赖 S1 中的 enum、canonical types、schema 和 migration。

建议拆分：

```text
S4a terminal contract
S5a binder contract
S7a WAL/event contract
          ↓
S1b frozen canonical/schema
          ↓
S4b/S5b/S7b implementation
          ↓
S3 + S4b + S5b + S7b → S6
```

否则“上游合同完成”和“整个 slice 出口完成”容易被项目管理误读。

### P1-4：S10 的未知/未确认门禁要从“有解释”改为量化阈值

“任何未解释的 UNKNOWN/DISCARD_UNCONFIRMED 阻止扩灰”方向合理，但仅有解释不够。至少需要按 datasource 冻结：

- 正常无故障路径允许的 `UNKNOWN` 为 0；
- fault injection 的期望 UNKNOWN 格子；
- quarantine 数量/年龄/预算占比阈值；
- backend inventory 不可用的停止时间；
- claim drift 必须为 0；
- 自动扩灰停止和人工恢复权限。

## P2

- `RELEASED`、`ConnectionReleased`、`DISCARDED` 等命名应统一成一套 ABI 名称。
- WAL nonce 需冻结生成/持久化策略，不能只写“不得复用”；应说明 crash/rotation 后如何证明同 key 下不复用。
- `hard revoke durable publish 后 60s` 应明确这是“拒绝新 operation/触发终结”的授权上界，不是 backend disappearance 上界；文后已有说明，建议在定义处直接注明。
- BEGIN、terminal、WAL、quarantine 的 typed proof 建议都列出 schema version，避免未来仅凭 evidence digest 无法解释旧记录。

## 五项核心核验总结

### 1. native BEGIN

**通过。**

顺序唯一、dependency fence 生命周期正确、比较失败不退 approval、`consumed_begin_failed` 和 typed cleanup 原则均与 S0/现有 SELECT binder 一致。它消除了 v2 的 binder→BEGIN TOCTOU。

剩余只是补齐 BEGIN 未成功时的 cleanup 子状态。

### 2. terminal 总函数

**未通过。**

枚举、deferred COMMIT rejection、RFQ 三态、ACK missing、untyped fallback、完整 connection ownership 都已吸收；但矛盾 phase/reply 没有安全总映射，而且 disposition 表可能把矛盾证据连接重新放回 pool。

PG14–18 矩阵总体客观可验证，但须增加 contradiction 矩阵。当前生产接口仍只是：

- [executor.go](</D:/ruanjiansheji/agentsql-v04/internal/authorizedexecute/internal/businessdb/executor.go:47>) 的 `Commit/Rollback error`；
- [postgres.go](</D:/ruanjiansheji/agentsql-v04/internal/authorizedexecute/internal/businessdb/postgres.go:733>) 的 `pgx.Tx.Commit/Rollback`。

所以 S4 是真实的新 connector/adapter 工作，不能用包装旧 error 接口冒充完成。

### 3. connection lease

**设计通过，实证未完成。**

`IN_USE → TERMINATING/QUARANTINED → FREE`、backend absence、generation CAS、预算继占、pool/migration/health/admin claim 和 `MaxOpenConns` 联动均正确吸收 v2 P0。

PG backend identity、DB-side terminate、inventory partition、owner crash 和 envelope recovery仍必须真库验证。

### 4. emergency WAL

**大部分合同正确，但尚未通过。**

完整 canonical payload、AEAD、UUID payload 比较、双链、disk hang/fsync/key/kill-9/乱序矩阵均明显优于 v2，并与 S0 边界一致。

但 framed-record 容量和 durability 自指/迟到 fsync 仍需冻结，因此不能称 P0 完全关闭。

### 5. watchdog

**核心 CAS/quiesce 思路正确，但未通过。**

它已经消除了“Execute 尚未退出时直接并发 ROLLBACK”的原缺陷；forced discard、late Execute result、single terminal owner 也正确。

但 PG CancelRequest 的跨连接迟到问题会把风险转移到随后 ROLLBACK，必须采用“cancel 发出后默认 discard”或提出可证明的更强 cancel fence。

## 范围、拒绝面与 P1 吸收情况

以下边界已封闭且合理：

- PostgreSQL-first、MySQL API 层强制 unsupported；
- stacked SQL、事务控制、SAVEPOINT、DDL、session variable、用户 prepared statement拒绝；
- active transaction 内 SELECT、外部 RETURNING 拒绝；
- CTE、subquery、UPDATE FROM、DELETE USING、trigger、FK、default/generated、RLS/rule/view/partition/UDF 等未闭合对象拒绝；
- reserved control/audit schema/OID 无条件拒绝；
- continuation KDF/AEAD/rotation、idempotency-before-seq；
- hard revoke 删除“立即”承诺；
- closure 显式矩阵；
- audit rank/capability 与禁止 callback；
- typed absence；
- 固定 error boolean/effect；
- metadata/cardinality 计费；
- MySQL 量化 gate。

现有 [authorizer.go](</D:/ruanjiansheji/agentsql-v04/internal/columnauth/authorizer.go:173>) 仍明确 SELECT-only，因此 S5 必须建立独立 DML action/write-target/reference 授权合同。现有 [lockrank.go](</D:/ruanjiansheji/agentsql-v04/internal/lockrank/lockrank.go:15>) 也仍只有 Control/Business 两级，v3 正确地没有把它当成 audit/async 证明。

## 实现和激活建议

可以开始全 flag-off 实现，但第一阶段应是：

1. S4a：修正 evidence-consistency 和 terminal total table，完成 PG typed-phase spike。
2. S5a：冻结 DML ABI、BEGIN cleanup 子状态和 closure contract。
3. S7a：修正 WAL durability schema、framed reservation 公式和 replay 状态。
4. 回填并冻结 S1。
5. S3 可同时实现 directory/claim/quarantine 骨架。
6. 之后才进入 S6 coordinator；S8/S9 按修订后的 DAG 推进。

以下未关闭前不得激活任何 B5 生产能力：

- terminal contradiction 映射；
- PG typed phase 和完整 physical lifecycle；
- late CancelRequest 安全规则；
- WAL framed reservation 和 timeout/late-fsync 语义；
- connection quarantine 真库对账；
- PG14–18/TLS/proxy/restart/failover矩阵；
- 正式 DML ABI/closure 五 major attestation；
- S10 和 datasource 级 stop conditions。

## 签收建议

v3 的角色清单总体合理，建议补强责任边界：

- Architecture/DB Core：签 evidence-consistency 表，而不只是 nominal terminal 表。
- PostgreSQL owner：明确签 CancelRequest late-arrival 处置。
- Audit/B6 + Storage/SRE：签整条 framed-record charge、late fsync、设备 durability 边界。
- Runtime/SRE：签 pool envelope crash reclaim 和 quarantine 年龄/预算阈值。
- QA/Release：签每个矩阵格的期望结果和零误分类标准，不能只签“有解释”。
- Product：签 300ms legacy 可用性、tx 内 SELECT/RETURNING 拒绝、60s revoke 语义、本地 WAL 故障域。
- MySQL owner：v0.4 只签 off/unsupported；未来必须独立复审。

最终裁决：**GO-WITH-REQUIRED-CHANGES，仅允许全 flag-off 实现。** v3 已接近可实施设计，但现在仍不能声称五个 P0 被真正、彻底关闭。
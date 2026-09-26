# 独立评审结论：GO-WITH

**GO-WITH-REQUIRED-CHANGES。**

允许开始全 feature-flag-off 的合同、spike 和骨架实现；但我不同意 v4 当前“3 个 P0 均已彻底关闭”的结论：

| v3 P0 | 本次结论 |
|---|---|
| EvidenceConsistency / terminal 矛盾 | **设计关闭**，测试矩阵有一处补充项 |
| PG late CancelRequest | **设计关闭** |
| WAL reservation / durability 自指 | **仍有两个 P0 缺口，未彻底关闭** |

在以下 P0 修完前：

- 不得冻结 S1b terminal/WAL schema；
- 不得启用 `b5_sessions` 或 `b5_tx_postgres`；
- `b5_tx_mysql` 必须继续 off/unsupported；
- 不得把当前文档状态升级为无条件设计 GO。

完整评审未写入 `.design/b5-v4-review.md`，因为当前沙箱为只读；未改代码、未提交 Git。

## P0

### P0-1：20KiB/120KiB 的算术正确，但尚不能由物理格式推出

位置：[v4 L376](/D:/ruanjiansheji/agentsql-v04/.design/b5-mcp-transaction-design-v4.md:376)、[L393](/D:/ruanjiansheji/agentsql-v04/.design/b5-mcp-transaction-design-v4.md:393)、[L417](/D:/ruanjiansheji/agentsql-v04/.design/b5-mcp-transaction-design-v4.md:417)。

独立核算：

```text
512 header
+ 16,384 ciphertext
+ 16 AEAD tag
+ 32 trailer
= 16,944 B serialized frame before padding

align_up(16,944, 4,096) = 20,480 B
```

如果另外的 `segment/index/accounting overhead = 512B` 与 record 共用同一个 20KiB extent，20KiB 仍足够，因为 padding 有 3,536B。

但 v4 实际公式是：

```text
align_up(16,944 + 512, 4,096) = 20,480
```

这隐含了“index/accounting overhead 可以占用 record extent 内的 padding”。若 index、allocator metadata 或 segment accounting 是独立物理分配，则应是：

```text
align_up(16,944, 4,096) + 512 = 20,992 B
```

如果独立部分也按页计费，甚至可能达到 24KiB。因此当前 20KiB 只在一个未冻结的 allocator/layout 假设下成立。

此外，`RecordHeader` 只有字段名，没有固定宽度、编码、字符串上限或 extension 区规则；`header_max=512`、`trailer_max=32` 本身仍是声明，不是格式推导。`event_schema_id`、`key_id` 等如果采用可变长编码，必须分别封顶。

六条记录的路径方向合理，但 [L423–430](/D:/ruanjiansheji/agentsql-v04/.design/b5-mcp-transaction-design-v4.md:423) 的 `recovery/headroom` 仍是多个事件类别的合并占位，不是状态机中的唯一 emission。尤其 [L435](/D:/ruanjiansheji/agentsql-v04/.design/b5-mcp-transaction-design-v4.md:435) 规定 fsync timeout 后 writer/segment 禁止新 append，却没有冻结后续 rollback/terminal/session 事件是：

- 全部标 LOST 而不再写 WAL；
- 新开 segment；
- 还是合并进单个 recovery record。

若允许新 segment，需证明不会再次产生 diagnostic/recovery；若不允许，需明确哪些必需事件允许永久缺失。否则“最多六条”还不是总函数证明。

修复：

- 在 S7a 冻结逐字段二进制 layout、长度上限、padding、AEAD API 中 tag 是否独立，以及 index/accounting 究竟是同 extent 还是独立 charge。
- 用实际 encoder 定义 `EncodedRecordCharge()`，golden 断言最大输入不会超过 charge。
- 给出 `state × transition → emitted records` 表，逐路径计算最大 multiplicity。
- 明确 wedged writer 后是否允许 rotation；若允许，纳入最大路径。
- 若 P0-2 的响应 receipt 也进入 WAL，记录数必须从 6 重新计算。

### P0-2：自指已从 CanonicalEvent 移除，但“历史响应不倒改”缺少可持久实现

位置：[v4 L364–375](/D:/ruanjiansheji/agentsql-v04/.design/b5-mcp-transaction-design-v4.md:364)、[L432–439](/D:/ruanjiansheji/agentsql-v04/.design/b5-mcp-transaction-design-v4.md:432)。

移除 CanonicalEvent 自身的 `AuditDurability` 是正确选择，也与 S0 的 audit outage 实证一致。但 v4 只是说保存 `reported_durability_at_response` 为 immutable result metadata，没有冻结：

- 保存在哪个 durable store；
- 用什么 key 与 event UUID、request ID、attempt generation、WAL receipt 绑定；
- 必须在网络响应之前还是之后持久化；
- 写 metadata 失败时能否仍返回结果；
- restart 时它与 scanner 结果的 join 优先级。

关键崩溃窗口：

```text
fsync 250ms timeout
→ 客户端收到 LOST
→ fsync 迟到成功
→ callback/recovery ledger 尚未持久化即 crash
→ restart 只看到一个完整有效 record
```

scanner 能证明 record 现在有效，却不能仅凭该 record 知道客户端此前看到的是 LOST。如果 idempotency/status 在重启后按当前 primary/replay 状态重新派生，就会把历史结果变成 PENDING/DURABLE，违反文档承诺。

修复：

- 冻结 durable result receipt，例如以 `(session, request, event UUID, attempt generation)` 为键，绑定业务 event digest 和 WAL append receipt digest。
- `reported_durability_at_response` 必须 write-once，并在发送响应前持久化；失败时不得发送一个日后无法复现的终态响应。
- restart join 明确为：

```text
reported_at_response: write-once，永不升级
append_confirmation: UNKNOWN → TIMEOUT → LATE_CONFIRMED/RECOVERED
reconciliation: 单调推进
```

- 没有 durable result receipt 时，scanner 只能报告“历史响应未知/未报告”，不能推断当时是 AUDIT_PENDING。
- 增加 crash 点：result metadata 持久化前/后、响应发送前/后、late callback 前/后、recovery event 前/后。

这是新的 receipt/result 平面问题；若不补，它只是把原来的自指从 CanonicalEvent 移到了未定义持久性的 metadata。

## P1

### P1-1：timeout 错误码必须显式受 terminal CAS 和 NoCommit proof 约束

位置：[v4 L135–139](/D:/ruanjiansheji/agentsql-v04/.design/b5-mcp-transaction-design-v4.md:135)、[L518](/D:/ruanjiansheji/agentsql-v04/.design/b5-mcp-transaction-design-v4.md:518)、[L555](/D:/ruanjiansheji/agentsql-v04/.design/b5-mcp-transaction-design-v4.md:555)。

状态图暗示 timeout 只能从 `ACTIVE` 进入 rollback-only，但错误表把 `TX_IDLE_TIMEOUT/TX_MAX_DURATION/TX_STATEMENT_TIMEOUT` 固定为 `TERMINAL_NOT_COMMITTED`；后面的 proof 限制只明确点名 watchdog/cancel/rollback contradiction。

应明确：

- timeout 只能在 COMMIT send permit 尚未开放时赢 terminal CAS；
- 一旦进入 `COMMITTING`，wall deadline 只能缩短 FinishCommit 等待或记录 diagnostic，不能选择 timeout 的 NOT_COMMITTED code；
- 所有 timeout 的 `TERMINAL_NOT_COMMITTED` 都要求 `NoCommitEverSent`；
- 增加 wall deadline 命中 COMMIT before-send、partial、full、ACK-window 的竞态测试。

### P1-2：owner-crash reaper 仍有“连接已建、child CAS 未落盘”的不可恢复窗口

位置：[v4 L288–310](/D:/ruanjiansheji/agentsql-v04/.design/b5-mcp-transaction-design-v4.md:288)。

当前顺序是 permit → dial → child CAS。若进程在数据库 backend 建立后、child identity CAS 前崩溃，只剩 `DIAL_PERMIT_OUTSTANDING`，却没有可供 inventory 精确定位的 backend identity。

保守地永久占 claim 是安全的，但不满足可恢复 reaper，最终可耗尽 datasource。

修复：

- dial 前生成 durable `dial_permit_id`；
- 通过 PG startup parameter/application_name 或等价受认证字段把 permit/owner incarnation 带到服务器；
- inventory 必须能按该 token 找到并终止 backend，再以独立 inventory absence 转为 `NEVER_ALLOCATED/BACKEND_ABSENT_CONFIRMED`；
- 测试必须 kill 在 TCP connect、认证完成、startup 完成、child CAS 前后每个窗口。

### P1-3：terminal 测试矩阵漏了 ACK 已相关但 RFQ 未观察到

设计在 [v4 L195](/D:/ruanjiansheji/agentsql-v04/.design/b5-mcp-transaction-design-v4.md:195) 明确：相关 ACK/rejection + `RFQ NotObserved` 保留 known DB outcome，但禁止 release。

第 13.1 节覆盖了 ACK+RFQ I、ACK+T/E/duplicate、ACK missing+RFQ I，却没有显式覆盖：

- positive ACK + RFQ NotObserved；
- definitive rejection + RFQ NotObserved；
- 在 ACK 与 RFQ 之间断流/RST/blackhole。

把这两格加入 release-blocking 矩阵，期望 known DBOutcome + discard/unconfirmed。

### P1-4：S10 低流量条款允许绕过样本量

位置：[v4 L712](/D:/ruanjiansheji/agentsql-v04/.design/b5-mcp-transaction-design-v4.md:712)。

“24h 且 10,000 terminals；流量不足则观察 72h”可被理解为 72h 后不再要求样本量。时间不能替代竞态样本量。

修复为：

- 最少 24h 且最少 10,000 terminals；
- 72h 仍未达到样本量则继续等待，或使用经过标记的合成正常流量补足；
- fault-injection 标签必须不可由普通调用者伪造，防止正常 UNKNOWN 被归入注入流量；
- 明确定义 `independently_observed_expected` 的 inventory 来源、最大陈旧时间和 PID/connection nonce 关联。

其他 quarantine、inventory、claim drift=0、双人恢复和自动 stop 阈值是明确且可执行的。

### P1-5：DML 授权方向正确，但 S5a 仍需冻结政策合成规则

位置：[v4 L113–122](/D:/ruanjiansheji/agentsql-v04/.design/b5-mcp-transaction-design-v4.md:113)。

v4 正确没有复用 B2 SELECT authorizer。当前代码也明确是 SELECT-only：[authorizer.go L173](/D:/ruanjiansheji/agentsql-v04/internal/columnauth/authorizer.go:173)、[scope.go L13](/D:/ruanjiansheji/agentsql-v04/internal/columnauth/scope.go:13)。

但正式 ABI 还需冻结：

- allow/deny 优先级及多 policy 合成；
- `INSERT` omitted column/default 的拒绝语义；
- `DELETE` 的 write-target 集合语义；
- whole-row/system column、constraint/internal reads 的 reference 表达；
- policy revision/catalog identity 如何进入 proof digest；
- write path 禁止沿用 SELECT mask plan；
- action/write/reference grant 来自现有 policy schema还是新增独立 schema。

因此它与 B2 兼容，但不是“在现有 authorizer 上加三个枚举”即可完成。

## 原 3 个 P0 的逐项核验

### EvidenceConsistency：通过

[v4 L156–232](/D:/ruanjiansheji/agentsql-v04/.design/b5-mcp-transaction-design-v4.md:156) 已完成关键要求：

- 五种 write phase × Missing/P-R/weak/wrong-stale/untyped/duplicate 基本穷尽；
- NotSent/Zero/Partial + 当前 P/R 固定 contradiction；
- Indeterminate 只有 strong correlation 可由 typed reply 覆盖；
- stale/future、wrong operation、positive+rejection、duplicate ACK/RFQ 都短路；
- contradiction/unknown schema 在 nominal DBOutcome/disposition 前处理；
- COMMIT contradiction 固定 UNKNOWN；
- ROLLBACK 只有独立版本化 `NoCommitEverSent` 保留 NOT_COMMITTED；
- 所有非一致路径禁止 RELEASED。

这真正关闭了 v3 的原 terminal P0。当前生产接口仍只有 `Commit/Rollback error`，[executor.go L47](/D:/ruanjiansheji/agentsql-v04/internal/authorizedexecute/internal/businessdb/executor.go:47)、[postgres.go L733](/D:/ruanjiansheji/agentsql-v04/internal/authorizedexecute/internal/businessdb/postgres.go:733)，所以 S4b 必须是新 typed adapter，不能包装旧 error 接口冒充完成。

### PG CancelRequest：通过

[v4 L234–273](/D:/ruanjiansheji/agentsql-v04/.design/b5-mcp-transaction-design-v4.md:234) 的基线正确：

- send started、partial、indeterminate、full write 均永久 taint；
- tainted 连接不发 ROLLBACK/COMMIT/reset/health/新 statement；
- RFQ I、Execute 成功退出、关闭 cancel socket均不能解除 taint；
- lifecycle 只能摘池、关闭、inventory；
- future generation-bound fence 明确需要独立 schema/attestation/复审，v0.4 不存在该例外。

第 13.2 节的 delayed cancel、ROLLBACK reorder、statement timeout、grace timeout和双 CAS 矩阵足够关闭原 late-cancel P0。

### WAL：条件不通过

nonce 方案是可信的：每 segment 新 key、新随机 segment ID、registry CAS、manifest和父目录 fsync、单 writer ordinal、crash/ownership变化后旧 segment永不 reopen。只要实现严格遵守，crash/rotation 下同 key nonce 不复用可以证明。

但 framed charge 和历史 result receipt 仍有上述两个 P0，所以整个 WAL P0 尚不能签“关闭”。

## 其他结论

- BEGIN cleanup：通过。五个资源状态正确覆盖了三种资源形态，也没有把无 transaction 的路径伪装成 FinishRollback。
- 最终切片 DAG：正确且无环。[v4 L560–575](/D:/ruanjiansheji/agentsql-v04/.design/b5-mcp-transaction-design-v4.md:560) 的 a/b 拆分消除了 v3 的隐含工程环。
- PG-first/MySQL off：封闭。API、flag、连接取得数和未来复审要求一致，没有热开口。
- 拒绝面：与 S0 一致，特别是 trigger→`audit_log`、RETURNING、FK/default/generated、复杂 DML 均未被技术可行性误当成 GA 授权。
- 锁图方向正确；但当前代码只有 Control/Business 两级，[lockrank.go L15](/D:/ruanjiansheji/agentsql-v04/internal/lockrank/lockrank.go:15)，audit rank/phase capability仍是实质性新实现。
- 当前 audit canonical 仍是 V1 27字段格式，[canonical.go L37](/D:/ruanjiansheji/agentsql-v04/internal/auditchain/canonical.go:37)，不能直接充当 event v4/receipt v1。

## 可以开始什么

可以立即开始：

1. **S4a 优先**：EvidenceConsistency typed connector、timer/terminal CAS 合同、late-cancel fault proxy。
2. **S7a 并行**：精确 wire layout、真实 encoder golden、事件 emission 总表、durable result receipt。
3. **S5a 并行**：DML grant lattice、BEGIN cleanup、closure negative matrix。
4. **S3 仅做 spike**：特别是 pre-dial identity 和 crash-before-child-CAS inventory。

不得开始或不得冻结：

- P0 修复前不得冻结 S1b；
- 不得让 S6 依赖未冻结的 terminal/WAL ABI；
- 不得启用任何生产 B5 flag；
- typed PG terminal、真 late-cancel、WAL 介质测试、reaper/inventory、PG14–18 DML closure、S10 全矩阵未完成前不得 internal canary。

## 仍需真库/真网络证据

必须保留为 activation blocker：

- PG14–18、TLS on/off、direct/proxy 的 terminal phase/ACK/RFQ 矩阵；
- ACK 与 RFQ 之间的单向黑洞；
- CancelRequest 独立连接的真实延迟与重排；
- restart/failover、PID复用、backend absence；
- owner crash 在 dial/child CAS 各窗口；
- PG15/16/17 正式 DML ABI/hash，而不只是 S0 的 PG14/18 spike；
- 目标文件系统上的真实最大 frame、fsync hang、directory fsync、kill -9、rotation；
- LOST result receipt 的全部 crash window；
- S10 自动 stop、双人恢复和 claim drift 演练。

## 签收建议

现有角色清单总体合理，建议增加以下明确责任：

- Architecture + DB Core：EvidenceConsistency及 timeout/terminal CAS 优先级。
- Audit/B6 + Storage/SRE + Runtime owner：result receipt 的 durable-before-response 合同。
- Storage/SRE：逐字段 frame layout、物理/index charge、六条状态机证明。
- PostgreSQL + Runtime/SRE：dial permit 在服务端可 inventory 的身份协议。
- PostgreSQL + Security：DML grant lattice及五 major closure。
- QA/Release：补充 ACK+RFQ NotObserved、receipt crash、dial-before-child-CAS 矩阵。
- Product：确认低流量不能豁免样本量，以及本地 WAL 故障域边界。

最终裁决仍是：**GO-WITH，可做全 flag-off 实现；不可冻结关键 schema，不可激活。**
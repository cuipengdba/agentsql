# AgentSQL v0.4 B5 S0 实证结论

状态：**S0 完成；用于 B5 design v2 裁决，不代表生产能力已实现**  
日期：2026-09-25（Asia/Shanghai）  
代码基线：`feature/v0.4`，`HEAD=9cb14511674ff3dd580bdf710941085fab63bdc9`  
约束：未修改生产代码、未提交 Git；所有探针位于 `.design/s0-b5/`。

## 0. 环境、方法与结论摘要

真实环境：Docker Engine（Windows named pipe / Docker Desktop），PostgreSQL 14、PostgreSQL 18、MySQL **8.4.11**；MCP Go SDK 为仓库锁定的 `github.com/modelcontextprotocol/go-sdk v1.7.0`。PG DML ABI 是运行时从现有 extension 源码生成的临时派生镜像，源文件没有被修改。所有容器均带 `agentsql.b5.s0=true` label 和 `agentsql-b5-s0-` 名称前缀。

| 评审 P0 | 实证裁决 | GA 含义 |
|---|---|---|
| P0-1 approval digest 循环 | 设计修正必需（非真库问题） | `plan_digest` 必须不含 approval id；另设 `begin_authorization_digest` |
| P0-2 session capability / 多实例路由 | **条件可行** | 共享强一致目录或带签名的 instance routing proof；session id 本身不能授权，owner mismatch 不能触发回滚 |
| P0-3 typed terminal outcome | **条件可行** | 必须由驱动阶段证据产生枚举；错误文本不得参与判断 |
| P0-4 MySQL 有界终结 | **条件可行，但 MySQL GA 仍应 off** | 自定义可观测 connector + socket deadline + 独立 watchdog + 强制 discard 可限时；不能消除 commit unknown，且 MySQL DML binder/MDL 未在本 S0 证明 |
| P0-5 MCP 取消与版本 | **条件可行** | legacy HTTP 断连不传播取消；必须有独立 idle/wall watchdog，并在 SDK 前实施精确 allowlist |
| P0-6 审计耐久边界 | **条件可行** | 同一 audit store 宕机时 rollback/outcome 事件不能承诺耐久；需要明确 best-effort 或独立 emergency WAL；`committed/audit_pending` 不得写成 DB outcome unknown |
| P0-7 quota / tombstone | **进程内方案不可行；集群方案条件可行** | admission、连接数、plan/tombstone bytes 必须为集群级预算并带租约/回收；tombstone 禁止保留 plan/result body |

建议 GA 范围：**PostgreSQL-first**，先把 PG14/18 DML ABI、隐式对象拒绝矩阵、审计/终结状态机做成正式实现；`b5_tx_mysql` 在 MySQL binder + MDL closure、终结 connector、真实单向分区/DB timeout release gate 全部通过前必须默认且强制 off。

## 1. P0-4 / P0-3：MySQL 有界 commit、rollback、连接销毁和 typed outcome

### 问题

现有 `mysqlWriteTx.finish` 接收 context，但最终调用无 context 的 `sql.Tx.Commit()` / `Rollback()`，且没有保留可设 deadline、可强制销毁的物理 socket。需要证明终结是否能限时、连接是否真正 discard，以及 ACK 丢失时能否判定 committed/not_committed/unknown。

### 步骤

脚本：`s0-b5/mysql-terminal.go`。

1. 启动真实 MySQL 8.4 容器并核验 `SELECT VERSION()` 为 `8.4.11`。
2. 通过 `mysql.RegisterDialContext` 提供自定义 connector，包装并保留真实 `net.Conn`；用独立 `sql.Conn` 固定物理连接和 `CONNECTION_ID()`。
3. 在 COMMIT/ROLLBACK 报文写入 socket 前，以及报文写入后、读取 ACK 前分别注入故障。
4. 独立 watchdog 在 150ms 后依次执行 socket deadline、socket Close，并让 `sql.Conn.Raw` 返回 `driver.ErrBadConn`，明确要求 `database/sql` discard；用新连接 ID 验证没有复用故障连接。
5. 用另一条独立观察连接查询数据真相。另以 `ExecContext(SELECT SLEEP(5))` 验证 statement deadline。

### 真实结果

| 场景 | 终结返回 | 用时 | 旁路真相 | 新物理连接 | typed outcome |
|---|---|---:|---|---|---|
| COMMIT 写入前断开 | `driver: bad connection` | 169ms | not_committed | 9 → 10 | `NOT_COMMITTED` |
| COMMIT 已写、ACK 丢失 | `invalid connection` | 150ms | **committed** | 11 → 12 | **`UNKNOWN`** |
| ROLLBACK 写入前断开 | `driver: bad connection` | 150ms | not_committed | 13 → 14 | `NOT_COMMITTED` |
| ROLLBACK 已写、ACK 丢失 | `invalid connection` | 150ms | not_committed | 15 → 16 | `NOT_COMMITTED` |

`sql.Conn.Raw(... driver.ErrBadConn)` 在“已写、ACK 丢失”路径返回 `driver: bad connection`，随后连接 ID 改变，证明可强制 discard。5s `SLEEP` 在 200ms context deadline 返回 `context deadline exceeded`。

关键事实：**有界等待不等于可判定 commit**。COMMIT bytes 已交给 kernel 后，ACK 丢失时，调用端无法从驱动错误区分“服务端未提交”和“服务端已提交但响应丢失”；本次真相恰为已提交。只有 connector 自己证明终结报文零字节写出时才能返回 `NOT_COMMITTED`，普通 `sql.Tx.Commit` 错误一律不足以作此证明。

本探针在健康主机网络上以读丢失/强制断 socket 模拟分区窗口。真正单向黑洞中，本地 Close 不能证明服务端在同一时间界内收到 RST；因此服务端还必须配置并 attestate 会话级 transaction/idle/TCP timeout。客户端可以保证调用返回有界，但不能仅凭本地 socket 销毁承诺服务端锁立即释放。

### 对 GA 的结论

**终结机制条件可行，MySQL 方言 GA 不可据此放开。** design v2 必须把终结接口改为枚举结构，例如：

```text
TerminalOutcome = COMMITTED | NOT_COMMITTED | UNKNOWN
ConnectionDisposition = REUSABLE | DISCARDED | DISCARD_UNCONFIRMED
EvidencePhase = BEFORE_SEND | SENT_ACKED | SENT_ACK_MISSING
```

MySQL 实现必须拥有自定义 connector/socket、终结 watchdog 和显式 discard；禁止按错误字符串推断。COMMIT `SENT_ACK_MISSING` 必须是 `UNKNOWN`，禁止重试。MySQL DML binder/MDL 的 release gate 仍未证明，所以 `b5_tx_mysql` 必须 off。

## 2. P0-5：MCP 取消、版本协商和 legacy watchdog

### 问题

SDK 配置了 `PropagateRequestCancellation: true`，但评审指出该能力只适用于 `>=2026-07-28`。还需确认未知 legacy 版本是否被拒绝，以及 2025-06-18 下如何保证遗留事务最终回滚。

### 步骤

脚本：`s0-b5/mcp-cancel.go`、`s0-b5/mcp-watchdog-pg.go`。

1. 用 v1.7.0 的真实 `NewStreamableHTTPHandler` 注册阻塞工具，分别以三个协议版本发原始 HTTP 请求；handler 开始后立即关闭 TCP，观察 700ms 内 context。
2. 发送未知 legacy header、未知 future header、以及无 header 但 initialize body 声明未知 legacy 版本的请求。
3. 对 2025-06-18 再接真实 PG14：handler 中开始事务并插入数据，断开 HTTP；独立 300ms wall watchdog 使用与 request context 无关的 rollback context 回滚，再由旁路连接验真。

### 真实结果

| 协议 | HTTP 断连后 700ms 内 handler context 取消 |
|---|---|
| 2025-06-18 | **否** |
| 2025-11-25 | **否** |
| 2026-07-28 | **是**，本机观测小于 1ms |

版本行为：

- header=`2025-07-01`：HTTP 400，unsupported。
- header=`2099-01-01`：HTTP 400，SDK JSON-RPC `unsupported protocol version`。
- **无 header，initialize body=`2025-07-01`：HTTP 200，SDK 静默协商为 `2025-11-25`。**

SDK 源码也明确把 `shouldPropagateCancellation` 限制为 `usesNewProtocol`；本结果不是配置误差。

2025-06-18 + PG watchdog 的 request context 始终未取消，但 300ms watchdog 触发，真实 rollback 用时 1ms，断连后 305ms 验证业务行数为 0。

### 对 GA 的结论

**条件可行。** 产品 allowlist 必须位于 SDK 之前，同时检查 header 和 initialize body；仅依赖 SDK 的“supported versions”不等价于产品承诺。初始建议只 allow `2025-06-18`（若产品另行签收可逐个加入），未知值 fail closed。

2025-06-18 下无法把 HTTP 断连当成即时 cleanup signal。必须由 SessionManager 自己拥有、与 request context 解耦的：active-operation deadline、idle-in-transaction deadline、absolute tx wall deadline、实例 shutdown/stdio EOF cleanup。HTTP 两请求之间本来没有可观察的 transport close，只能靠 idle/wall watchdog；因此产品文案应承诺“deadline 内回滚”，不能承诺“legacy HTTP 断连立即回滚”。

## 3. PG DML binder：ABI、列 usage、prepared execution、OID 锁与隐式闭包

### 问题

现有 `agentsql-binder-4.1` 和 Go `PostgresPreparedSelect` 强制 SELECT-only。需要确认扩展 analyzed-tree ABI 支持 DML 的 `write_target/reference`、RETURNING、同 backend prepared execution 是否可行，以及 DML 锁和闭包是否仍满足 fence。

### 步骤

脚本：`s0-b5/pg-dml-binder.go`。

1. 在原始 cached binder 镜像的 PG14、PG18 上调用 `agentsql_catalog.prepare` 准备 UPDATE。
2. 运行时复制 extension 到临时构建目录，最小扩展 ABI：允许 INSERT/UPDATE/DELETE analyzed `Query`，由 `resultRelation + targetList.resno` 标记 `write_target`，表达式/WHERE/RETURNING 标记 `reference`；生成临时 ABI `agentsql-binder-4.1-dml-spike`。生产源码不变。
3. 在一个事务和同一 backend 上 prepare → manifest → seal → `EXECUTE UPDATE ... FROM ... RETURNING`，记录 PID、列 usage、relation OID locks；另一连接尝试 ALTER TABLE。
4. 给 target 安装 AFTER UPDATE trigger，触发写入 `audit_log`，比较执行前 manifest 与执行后的真实 relation locks。

### 真实结果

- 原始 PG14/18 binder 均真实拒绝：`AgentSQL supports SELECT only (SQLSTATE 0A000)`。
- 临时 DML ABI 在两版均成功；backend PID 前后相同（PG14 182/182，PG18 190/190）。
- `RETURNING` 均返回 `1:joined`。
- manifest 包含 `b5.target.value` 的 `write_target`；reference 包含 join/source、WHERE 和 RETURNING 所需的 `ref.id/ref.note`、`target.id/ref_id/value`。
- 执行前已有 `target:RowExclusiveLock`、`target/ref:AccessShareLock`；并发 `ALTER TABLE target` 在约 259–262ms 以 `55P03 lock timeout` 失败，OID fence 确实有效。
- 执行后 trigger 真实写入一行；`audit_log:RowExclusiveLock` 和 target/ref index locks 出现，但 **`audit_log` 不在 manifest relations 中**。这是真实的隐式写闭包缺口。

单次诊断路径时延（包含未优化的多条 manifest/lock 查询）：

| PG | prepare | manifest + lock scan | seal | execute + RETURNING |
|---|---:|---:|---:|---:|
| 14 | 2.243ms | 25.117ms | 2.192ms | 3.417ms |
| 18 | 2.333ms | 29.744ms | 1.187ms | 3.388ms |

### 对 GA 的结论

**基础 DML ABI 条件可行，完整安全闭包尚不可 GA。** 同 backend prepared DML、RETURNING 和 write/reference usage 在 PG14/18 都有直接可行性证据。但 v2 必须新增正式 DML ABI/hash、分版本 attestation，并在执行前对 trigger、FK/cascade、default/identity/sequence、generated expression、partition/index、rule/view 等建立闭包或明确拒绝；不能把执行后的锁差异仅作为事后发现后继续事务。

建议首个 PG GA 子集继续拒绝 RETURNING、trigger、FK/cascade、用户函数/default/generated、upsert、CTE、UPDATE FROM/DELETE USING，逐项有 release-gate 证据后再开放。RETURNING 的技术可行性不等于 B5 v1 应开放它。

## 4. P0-6：审计 outage、emergency WAL 与 reconciliation

### 问题

business DB 与 audit store 不可能天然原子。需区分 intent 前 outage、commit 后 outcome outage，并判断 rollback/outcome 事件的耐久承诺及 `committed/audit_pending` 与 DB outcome unknown。

### 步骤

脚本：`s0-b5/audit-boundary.go`。分别启动真实 PG14 business/audit 容器，在精确时序停止 audit 容器；独立 business 连接验真。另以 mode 0600 的 append-only 文件执行每条 `Write + Sync`，恢复 audit 后按 EventUUID `ON CONFLICT DO NOTHING` 重放两次。

### 真实结果

1. audit 在 commit intent 前宕机：intent 写失败；business rollback 成功、业务行数 0；但 rollback audit 同样失败。只有 emergency WAL 留下 `rollback_pending/not_committed`。
2. commit intent 已耐久，随后 audit 宕机：business commit 返回 nil、业务行数 1；outcome audit 失败。此时真状态是 **`committed/audit_pending`**。
3. audit 重启后，原 intent 仍存在；两个 emergency 事件各重复重放两次，最终仍各一行，证明 EventUUID 对账可幂等。
4. emergency WAL：本机逐条 fsync 100 次平均约 **1.532ms/条**，记录约 **120B/条**；两次故障路径单次 fsync 为 2.1–2.6ms。

### 对 GA 的结论

**条件可行，但 v1 的状态名必须修正。**

- intent 未耐久：禁止 business commit；可确定 rollback，但同一 outage 下不能承诺 rollback audit 已耐久。
- business commit 明确成功、outcome audit 失败：内部终态必须是 `COMMITTED_AUDIT_PENDING`，不是 `TX_COMMIT_OUTCOME_UNKNOWN`。API 可选择不给普通成功响应，但不得在状态/运维上丢失“已知 committed”事实。
- DB commit 返回不确定：才是 `DB_OUTCOME_UNKNOWN_AUDIT_PENDING`；禁止自动重试，靠 intent + 业务幂等键/数据库事实对账。

若采用 emergency WAL，它必须是独立 durability component，而不是“稍后内存队列”：预分配/容量上限、fsync、CRC/截断恢复、权限与加密、轮转、磁盘满策略、节点永久丢失、告警、幂等上传都要进入 SRE release gate。单节点本地 WAL 不能覆盖整机/磁盘同时丢失；若不承担这些成本，设计必须诚实标注终态 audit 为 best-effort。

## 5. P0-7 / P0-2：多实例 quota、连接预算、路由和 tombstone bytes

### 问题

进程内计数器会被实例数扇出；session sticky route 需要跨实例唯一 owner；15min tombstone 若引用 1MiB plan 会产生巨大保留；还缺 DB 总连接数和 plan bytes 的全局预算。

### 步骤

脚本：`s0-b5/quota-tombstone.go`。父进程启动真实 PG14，派生四个真实子进程：先各自执行进程内 limit=10，再用 PG 原子条件 UPDATE 实施集群 admission、plan bytes 和连接 token；连接 token 成功后实际建立独立 PG backend 并记录 PID。四进程还同时为相同 20 个 session 做 `INSERT ... ON CONFLICT` owner 目录。最后比较 64 个带 1MiB plan 的 tombstone 与 50,000 个 digest-only tombstone 的 GC 后 heap。

### 真实结果

- 进程内 quota：期望集群 10，四进程共放行 **40**（4x 绕过）。
- 集群 session admission：40 次竞争只放行 **10**。
- 集群 plan byte：16 × 1MiB 请求只放行 **8**，used 精确等于 8MiB。
- DB 总连接：请求 12，只允许 **6**；真实记录 **6 个不同 backend PID**。
- 共享路由目录：四进程读取相同 20 个 session 共 80 次，目录仅 20 行、无重复 owner。
- 64 个保留 1MiB plan 的 tombstone：heap 增量约 **67,117,328B**。
- 50,000 个只保留 deadline/status/32B digest 的 tombstone：heap 增量约 **6,570,000B**，约 **131B/项**。

### 对 GA 的结论

**进程内方案不可行；集群方案条件可行。** v2 必须选择并写死一种：强一致共享 session directory + admission lease，或由可信 HMAC/capability 把 instance route 编入 handle 并由负载均衡器执行；随机裸 session id 不够。目录/lease 自身要有 owner epoch、过期、实例 crash 回收和 fencing，错误实例只能返回 `SESSION_WRONG_INSTANCE`，未认证/owner mismatch 不能回滚受害事务。

DB 连接预算必须是 `sum(instance pools + pinned tx + admin reserve) <= datasource hard budget` 的集群 token/lease，而不是每实例相同 `ConnLimit`。共享 PG counter 证明原子 admission 可行，不证明其热点/故障恢复已达生产要求。

tombstone 的增长公式是 `arrival_rate × TTL × retained_bytes`；必须同时限制条数、字节、每 owner 创建速率和全局 churn。tombstone 只保留固定上限的 terminal code、digest、seq、过期时间，禁止引用 raw SQL、plan、rows 或幂等结果 body。plan bytes 在 active tx 结束时释放，tombstone 与 active-plan 分账。

## 6. B5/B6 锁序、group commit 与 fence

### 问题

B5 活动 business tx 要同步等待逐条 audit/commit intent；B6 group commit 会锁 audit chain；最终 fence 属 control plane。若任何路径同时存在 control→business 与 business→control，会形成循环。

### 步骤

脚本：`s0-b5/lock-order.go`。在真实 PG14 上用两个事务构造：T1 持 business 再等 control；T2 持 control 再等 business。对照组统一 control→business，并给第二等待者 250ms lock timeout。另检查现有 `internal/lockrank` 与 `internal/audit/group_commit.go`：当前 rank 只定义 Control(10)→Business(20)；group commit 在调用 backend `InsertBatch` 时没有持有 coordinator mutex，backend attempt 2s、root 5s、enqueue 100ms 均有预算。

### 真实结果

- 反向边图在 **1006ms** 后由 PG 检出真实 `40P01 deadlock`。
- 统一 control→business 时无 deadlock；第二事务在 **252ms** 得到 `55P03 lock timeout`。
- 现有 group commit 的进程 mutex 不跨 `InsertBatch`，这是正确基础；但 B5 在持 business tx 时同步等 audit，事实上新增了 `business → audit-chain` 等待边，不能继续把所有 audit/control 都笼统叫同一低 rank。

### 对 GA 的结论

v2 必须给出可执行锁图，而不是一句“没有 business→control”：

```text
control snapshot / approval CAS --完成并释放 DB 锁--> business tx
business tx --有界等待--> audit chain append
business terminal + connection released --> final control fence / delivery
```

audit chain 必须是 terminal sink：不得在持 chain row/transaction/mutex 时回调 business、session directory、approval 或 final fence。若 audit 与 control 共库，也必须证明 audit append 只触及独立表/chain locks，不获取会被 control→business 路径持有的锁；否则仍有反边。final fence 必须在 business commit/rollback 返回且物理连接释放后执行。

等待预算必须嵌套：statement/lock < audit attempt(当前默认 2s) ≤ audit root(5s) < tx wall(60s)，且所有 enqueue/backpressure 失败使 tx rollback-only；不能让 group-commit 重试越过 tx wall。

## 7. 同机无争用延迟/开销基线

脚本：`s0-b5/latency.go`，Docker Desktop loopback，无并发；每个 PG 项 200 次。数字是本机方向性基线，不是发布 SLO。

| 数据库 | 操作 | avg | p50 | p95 |
|---|---|---:|---:|---:|
| PG14 | BEGIN + ROLLBACK | 1.380ms | 1.558ms | 1.657ms |
| PG14 | BEGIN + INSERT + ROLLBACK | 2.162ms | 2.114ms | 2.685ms |
| PG14 | BEGIN + INSERT + COMMIT | 3.475ms | 3.224ms | 4.944ms |
| PG14 | tx 内逐条 UPDATE | 0.842ms | 0.656ms | 1.600ms |
| PG18 | BEGIN + ROLLBACK | 1.482ms | 1.572ms | 2.205ms |
| PG18 | BEGIN + INSERT + ROLLBACK | 2.146ms | 2.118ms | 2.673ms |
| PG18 | BEGIN + INSERT + COMMIT | 3.380ms | 3.201ms | 4.748ms |
| PG18 | tx 内逐条 UPDATE | 0.762ms | 0.544ms | 1.080ms |
| MySQL 8.4.11 | BEGIN + ROLLBACK | 1.400ms avg | — | — |
| MySQL 8.4.11 | BEGIN + INSERT + ROLLBACK | 4.800ms avg | — | — |
| MySQL 8.4.11 | BEGIN + INSERT + COMMIT | 8.264ms avg | — | — |

创建并立即停止一个独立 `time.AfterFunc` watchdog 的 100,000 次平均增量约 **772ns**；Windows 计时分辨率使单次 p50/p95 为 0，故只采用总时长平均值。真实 MCP watchdog 的 300ms 到期后 PG rollback 本身约 1ms。

DML binder 单次数字见第 3 节；其 manifest/lock scan 是诊断实现，约 25–30ms，说明正式 ABI 必须批量返回 manifest/closure 并设 catalog round-trip/byte/work budget。

## 8. design v2 必须并入的裁决

1. 修复 approval digest 循环：stable plan digest 不含 approval id；approval 只签 stable digest；begin 另算 authorization digest。
2. terminal API 返回 typed outcome + connection disposition + evidence phase；所有 commit error 都不得按文本猜测或自动重试。
3. MySQL 使用自定义 connector、socket deadline、独立 watchdog、`ErrBadConn` discard 和服务端 timeout attestation；未全矩阵通过前 flag off。
4. MCP allowlist 在 SDK 前；legacy HTTP 明确只由 idle/wall watchdog 保证最终回滚，不宣称断连即回滚。
5. PG 发布新 DML ABI/hash；write/reference/returning 分开；隐式对象 closure 未证明即拒绝。
6. audit 状态拆成 `COMMITTED_AUDIT_PENDING`、`NOT_COMMITTED_AUDIT_PENDING`、`DB_OUTCOME_UNKNOWN_AUDIT_PENDING`；定义 reconciliation event 和查询 API。
7. 若上 emergency WAL，必须作为独立 durability subsystem 建设；否则将缺失 final audit 明示为 best-effort 边界。
8. session route/admission/连接/plan/tombstone 都做集群级租约与 byte budget；handle 是 capability 或带 continuation proof，裸 ID 不授权。
9. 锁图引入 audit-chain rank/terminal-sink 规则；control snapshot 不跨 active business tx 持 DB 锁，final fence 在 business 资源释放后。
10. 将本 S0 的 fault windows、PG14/18 ABI、MCP 三版本、audit outage、四进程 fanout 和锁序加入 release-blocking e2e。

## 9. 必须由负责人签收的边界

- 产品/架构：planned-only、首发不支持 tx 内 SELECT/RETURNING/复杂 DML、PG-first、MySQL off。
- DB/MySQL owner：custom connector 与服务端 timeout；commit unknown；binder/MDL/implicit closure 未通过即 off。
- DB/PostgreSQL + Security owner：新 DML ABI、write/reference/action grants、PG14–18 hash、隐式对象拒绝矩阵。
- MCP owner：2025-06-18 allowlist、legacy 断连不取消、idle/wall deadline 承诺。
- Audit/B6 + SRE：三种 audit-pending/unknown 状态、emergency WAL 或 best-effort 选择、对账与告警、group commit 等待预算。
- Runtime/SRE：共享目录/route、owner epoch、quota lease、DB 总连接 reserve、plan/tombstone byte/churn budget、实例 crash 回收。
- Control/Fence owner：无 business→control 反边；final fence 时点；hard revocation 不获取 control DB 锁。
- QA/Release：真实单向网络黑洞与 DB failover 仍需加入 release gate；本机 socket fault 已覆盖 commit send/ACK 窗口，但不替代跨主机网络设备故障测试。

## 10. 可复现脚本

- `list-images.go`：Docker Engine 缓存镜像审计。
- `mysql-terminal.go`：MySQL 终结 fault windows、discard、typed outcome、基础延迟。
- `mcp-cancel.go`：三协议 HTTP 断连与未知版本协商。
- `mcp-watchdog-pg.go`：2025-06-18 独立 watchdog + 真实 PG rollback。
- `pg-dml-binder.go`：PG14/18 临时 DML ABI、列 usage、OID locks、RETURNING、隐式 trigger closure。
- `audit-boundary.go`：双 PG audit outage、emergency WAL、幂等 reconciliation。
- `quota-tombstone.go`：真实多进程 quota/routing/连接 budget 与 heap retention。
- `lock-order.go`：真实 deadlock/lock timeout 对照。
- `latency.go`：PG14/18 和 watchdog 基线。
- `cleanup.go`：只按专用 label/prefix 清理并审计本 S0 资源。

最终清理实测：清理器发现并删除了三次失败/中断运行遗留的专用临时容器（两个 audit PG、一个 PG14 binder）和一个 `agentsql-b5-s0-pgdml` 临时镜像；最终审计为 `containers=0 networks=0 volumes=0 images=0`。删除对象仅含本 spike 的临时测试数据，不可恢复；未枚举删除、未 prune、未触碰 demo 或无专用 label/prefix 的资源。

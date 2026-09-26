# AgentSQL v0.4 易部署 Binder：免装默认 / C 加速可选——设计 v1

状态：**设计 + S0 完成；允许进入全 feature-off 实现，不是生产放行**  
日期：2026-09-25（Asia/Shanghai）  
代码基线：`feature/v0.4`，`HEAD=407abb53574d3e8502ae698040eaa62bd82f4cd5`  
证据：[`easy-deploy-s0/S0-FINDINGS.md`](easy-deploy-s0/S0-FINDINGS.md)、[`easy-deploy-s0/raw`](easy-deploy-s0/raw/)  
约束：本文未修改生产代码、未提交 Git、未启用任何 flag。

## 0. 最终裁决

1. **C 扩展改为可选加速器是可行方向，但“纯 SQL”不能成为任意 SQL 的等价 analyzed-tree binder。** PostgreSQL 没有 SQL API 暴露 prepared statement 的 analyzed/rewrite tree、列 attnum usage、依赖代数或失效代数；EXPLAIN 是可能执行用户代码、受角色和 planner GUC 影响的计划，不是授权 manifest。
2. 默认免装模式定义为 **`CATALOG_CLOSED_V1`**：网关内置封闭 grammar parser/resolver，只接受它能完整证明的 schema-qualified、永久 ordinary base table 子集；SQL PREPARE 只做同 backend 的 server-acceptance 与 relation-lock 交叉验证，catalog 提供 OID/attnum、负向 implicit-object gate、Fpre/Fpost 与 OID 排序锁。任一不确定立即 fail closed。
3. 可选模式定义为 **`NATIVE_C_V1`**：沿用按 PG14/15/16/17/18 构建的 C analyzed-tree binder，覆盖普通 view、复杂 scope/lineage 和精确 expression OID 等纯 catalog 模式没有的信息。C 模式仍受 B2/B5 已冻结的对象/语法拒绝面约束；“加速器”不表示能绕过产品级 deny。
4. 两模式共享同一 `SemanticFacts` 与 authorizer。共同支持集上必须满足：`CATALOG_CLOSED_V1` 若允许，则 C binder 产生的 action/relation/output/reference/write-target 事实必须完全相等；任何 divergence 拒绝并使对应 capability unhealthy。纯 SQL 可以比 C 多拒绝，不能比 C 多允许。
5. **默认 v0.4 不再要求客户在 PostgreSQL 服务器安装任何东西。** 但默认可用面必须诚实收窄：ordinary base table 的封闭 SELECT 和极小 simple DML；view lineage、开放表达式、无法闭合的 scope 必须要求 C 或报 unsupported。不得用 plan/deparse 猜测来追求表面“全功能”。
6. C 扩展推荐交付是 **签名的 deb/rpm/apk + 派生 PostgreSQL Docker 镜像**；网关负责探测、验证和可选 `CREATE EXTENSION`，不声称能经 SQL 把 `.so` 安装到远端服务器。标准 RDS/Aurora、Azure Flexible Server、Cloud SQL 没有客户文件系统入口且只允许服务商支持清单中的扩展，当前真实退化只能是 `CATALOG_CLOSED_V1`。

## 1. 安全目标与不可变边界

本设计不降低 B2 v4.1 与 B5 v4 的授权语义：

- B2 仍按七段优先级执行：agent/profile/statement deny → dialect/datasource unsupported → relation deny → 缺 relation allow → identity/catalog/closure 不可证明 → 缺 output/reference column grant → mask meet/capability → allow。
- B5 仍使用独立 DML lattice：preliminary → dialect → reserved target → action deny/missing → identity/closure → write-target deny/missing → reference form/deny/missing → allow。B2 的 output 近似不得代替 write target。
- caller 只提供 identity、datasource、raw SQL、approval；不得提供可被信任的 AST、relation/column identity、lineage 或 proof。
- bind、catalog、authorize、execute 必须在同一私有能力域。任何模式都不向域外暴露 raw executor。
- 任一模式只对自己声明的 capability 负责；`unsupported` 不等于 allow，也不得被静默解释成表级授权成功。
- 同一请求只有锁内 Fpre/Fpost 是 authority。跨请求 enrollment/cache 只是候选提示。
- 全局锁序、sealed response、durable audit/final fence 和 B5 terminal 合同不因 binder 模式变化。

“代理免装”在本文中的精确定义是：只部署/升级 AgentSQL 网关，使用一个业务数据库账号即可进入默认能力；不是“默认模式支持全部 PostgreSQL 语义”。

## 2. S0 范围与原始结果

### 2.1 矩阵

S0 使用 official PostgreSQL 14.24、15.19、16.15、17.11、18.6 容器；每版同时测试 superuser 与普通 `NOSUPERUSER/NOCREATEDB/NOCREATEROLE` 登录。19 个 SELECT/DML 形态形成 190 个主格，另有每版/账号的 planner jitter、search_path、prepared ABA，以及每版并发 DDL 探针。

代表形态包括 direct table、JOIN、两层 view、CTE、LATERAL、whole-row、系统列、partition、inheritance `ONLY`、RLS、immutable function、INSERT omitted column、UPDATE FROM RETURNING、trigger 和 rule。全部 PREPARE/EXPLAIN 成功；这只说明 server 接受，不说明授权信息完整。

### 2.2 能力结论表

| 安全要素 | 纯 SQL/catalog 实证 | 裁决 |
|---|---|---|
| server parse/analyze/rewrite acceptance | PREPARE 可用，且在事务内取得 relation lock | 可作交叉验证，不是 manifest |
| 显式 base relation OID/列 identity | `pg_class/namespace/attribute/type` 可读，普通账号与 super fixture 一致 | 可证明 |
| 已知 relation 的 OID 排序锁 | 显式锁 + `pg_locks` 可核验；5/5 major 阻止并发 ALTER，解锁后 DDL 成功 | 可证明 |
| view relation closure | PREPARE 锁包含 view/base；`pg_depend` 有 rule-wide 边 | 可做保守 relation closure，不足以列授权 |
| view 逐 output/reference lineage | plan 只剩 base table；`pg_depend` 不能把列边分配给 output/clause site | **不能证明** |
| JOIN/CTE/LATERAL 精确 usage | plan Output 是 planner 需要的字符串集合，包含非最终输出列，且无 OID/attnum/usage | 任意 SQL **不能证明**；封闭本地 binder 可另证 |
| whole-row/system/composite | 可看到 `c.*`、`ctid/xmin` 文本，但没有稳定 typed Var attnum proof | 拒绝或 C |
| partition/inheritance | PREPARE 只锁 parent，planning 后才出现 child；`ONLY` 不能消除 catalog shape | v0.4 拒绝 |
| trigger 隐式 relation | trigger UPDATE 的 PREPARE/EXPLAIN/函数 `pg_depend` 都未出现 body 写入的 `audit_log` | 存在即拒绝 |
| rule 隐式 relation | rewrite rule 的 `audit_log` 出现在锁/plan | 可检测但 v0.4 仍存在即拒绝 |
| RLS/security qual | 普通账号与 super 计划稳定不同；catalog 可检测 RLS | 存在即拒绝；必须按执行 role bind |
| function/operator/cast/type OID | EXPLAIN 只有 deparse 字符串；无 analyzed OID | 封闭 builtin 子集或 C |
| analyzed digest | `pg_prepared_statements` 无 tree/digest；plan digest 10/10 GUC 抖动 | **不可与 C 等价** |
| dependency digest | prepared statement 无 catalog OID，不能 join `pg_depend`；procedure body依赖不完整 | **不可与 C 等价** |
| prepared invalidation/reparse | 10/10 DROP/recreate 后旧 statement 静默绑定新 OID，SQL 仅有 generic/custom plan 计数 | 不能跨请求作 identity proof |
| DML target relation | command/target relation 可由封闭 parser + catalog 确定 | 封闭子集可证明 |
| DML write_target/reference | UPDATE plan 未标出被写列；RETURNING、assignment、WHERE 混成字符串 | 任意 DML **不能证明**；封闭 parser 可证明 |
| EXPLAIN safety | immutable user function 在 plan 时实际 sleep，52.568–54.065ms | 未 allowlist 前不得调用 EXPLAIN |
| 普通账号 metadata | fixture 所需 catalog、`pg_get_*` 和 own locks 可读 | 一个账号路径可行；启动 self-test 必需 |
| `GENERIC_PLAN` | PG14/15 不支持，PG16–18 支持但仍是 plan | 不能作为跨版本安全基础 |

### 2.3 “任意 SQL”问题的直接答案

对问题 1(a)～(d) 的答案是：

- **(a) 否。** relation 锁可给部分 relation closure，catalog 可给对象静态边，但任意 SELECT/DML 的 output/reference/write 列、view 逐输出 lineage、whole-row/system/composite、procedural implicit closure 无法完整获得。
- **(b) 否。** SQL surface 不提供 analyzed tree；EXPLAIN digest 受计划影响；deparsed `pg_node_tree` 是 major-private 文本且不能用 SQL typed walk。不能制造与 C 的 `nodeToString(analyzed query_list)`/typed dependency manifest 等价的 digest。
- **(c) 条件可。** 对已经由封闭 binder 确定的 ordinary base relation，可读 OID/relkind/qualified name、按数值 OID 取锁并从 `pg_locks` 双向核验。对任意 view/partition/trigger closure 不完整，不能外推。
- **(d) 任意 DML 否；封闭 DML 子集可。** write target 必须由网关 raw AST + catalog 生成，PREPARE/EXPLAIN 只作 server acceptance/意外 relation lock 检查。

## 3. 双模式架构

```text
raw SQL + authenticated session
          │
          ▼
  statement pre-gate / resource limits
          │
          ▼
  BinderCapabilitySelector ───────────────┐
       │                                  │
       ├─ CATALOG_CLOSED_V1 (default)     ├─ NATIVE_C_V1 (optional)
       │  gateway parser + catalogs       │  analyzed Query walker
       │  PREPARE cross-check             │  server prepared seal
       └────────────────┬─────────────────┘
                        ▼
                 SemanticFacts
          relations / output / reference /
          action / write_target / objects
                        │
                        ▼
          same B2 authorizer or B5 lattice
                        │
                        ▼
       same-backend EXECUTE + Fpost + audit attestation
```

模式在取得 business transaction 前选择。一次请求中不得因 bind error 从 C 热切到 SQL，或反向切换；这样会把两次不同 parse/analyze 快照混成一个 proof。扩展缺失/不健康时，下一请求可重新选择纯 SQL，但只有其 raw SQL 自身命中 closed capability 才能继续。

### 3.1 统一接口

设计级接口如下；名称是合同，不要求 S1 原样照搬 Go syntax：

```text
Probe(SessionIdentity) -> CapabilityAttestation

Bind(BindRequest) -> BoundProgram {
  mode, statement_class, action,
  relations[], column_uses[], write_targets[], object_uses[],
  execution_handle,
  semantic_facts_digest,
  engine_evidence_digest,
  capability_attestation,
  lock_expectation,
  catalog_roots
}

VerifyPre(BoundProgram, CatalogFrame, ActualLocks) -> PreSeal
Execute(PreSeal) -> bounded result / statement result
VerifyPost(PreSeal, CatalogFrame, ActualLocks) -> FinalBinderProof
```

`semantic_facts_digest` 对共同 canonical facts 编码，可跨模式比较；`engine_evidence_digest` 是模式私有证据。C 的 analyzed digest 与纯 SQL 的 closed-AST/catalog digest 绝不能放进一个名为 analyzed digest 的字段并声称等价。

### 3.2 `CATALOG_CLOSED_V1` 算法

1. 网关 parser 在接触 business DB 前完成单语句分类、token/depth/work budget；不允许 client PREPARE/EXECUTE、事务控制或 stacked SQL。
2. 只接受 closed grammar；所有 relation 必须 schema-qualified，alias/column reference 无歧义。transaction 内固定 `SET LOCAL search_path=pg_catalog`，核验 `current_user/session_user/role_oid`。
3. 候选阶段从 catalog 解析 relation OID、namespace、relkind、persistence 和列 attnum/type/collation；仅产生 hint，结束事务。
4. 执行事务按候选 OID 数值全序，对 server-quoted schema/name 逐一取锁；立即核对 name→OID、relkind 与 own `pg_locks`。不允许按 caller 字符串拼 lock SQL。
5. 锁内重新从 raw SQL 完整 local bind，读取 Fpre，运行完整 shape/implicit-object gate，生成 canonical `SemanticFacts`。
6. 用内部随机名在同一 backend PREPARE 原 SQL。检查 statement class、固定 session identity，并验证 PREPARE 新增的数据 relation lock 与 closed binder 期望集合一致；意外 relation、缺锁或 catalog 变化回滚，最多从候选阶段重试一次。
7. **不运行授权用 EXPLAIN。** planner JSON、pg_get/deparse 和 prepared counters 都不进入 proof。
8. authorizer 只消费 `SemanticFacts`。允许后同名 EXECUTE；读取/编码期间保持 schema locks。
9. 执行后重扫 Fpost/actual locks；必须等于 Fpre/enrollment。随后按既有 B2/B5 顺序完成审计、终结和 fence。
10. DEALLOCATE/事务结束；跨请求 prepared statement 即使留在连接上也不得提供 identity authority。

### 3.3 `NATIVE_C_V1` 算法

保持 v4.1 errata 的 same-backend prepared handle、typed Query walker、精确 OID allowlist、plan generation seal、role/search_path 与 invalidation 检查。catalog negative gate、Fpre/Fpost、OID locks、authorizer 和 audit 使用与纯 SQL相同的外围组件。

C 模式的功能优势是 typed analyzed/rewrite information，不是绕开 catalog safety gate。扩展返回 capability/hash 不匹配、node unsupported、replan/invalidation 或 closure mismatch时仍拒绝。

## 4. 默认免装支持面

### 4.1 B2 SELECT

`CATALOG_CLOSED_V1` 的 v0.4 准入候选：

- 永久 ordinary base table；schema-qualified；可多 alias/self join。
- 显式列 projection，以及局部 resolver 已有 golden 的 INNER/LEFT/RIGHT/CROSS JOIN。
- 无歧义的 WHERE/ON/GROUP/HAVING/ORDER；只允许 column、typed literal、NULL、AND/OR/NOT、IS NULL，以及同一 exact builtin type 上的封闭算术/比较集合。
- `*` 只有在锁内按 attnum 展开、预算和 output usage 全部生成后才可单独打开。
- 非递归 CTE、derived/correlated subquery、LATERAL 不是首个纯 SQL slice；只有 S3 differential gate 单项通过后才增加 capability bit。它们不影响 direct/join 主线交付。
- function/aggregate/window、CAST/COLLATE、unknown coercion 默认拒绝；后续只能按 exact major/signature manifest 单项加入，不得按 `pg_catalog` 名字宽放。

首发纯 SQL 拒绝普通 view，因为“plan 展开到 base”不能恢复 exposed view identity、逐 output lineage 和逐层 ACL。安装 C 后可按 B2 v4 的普通非递归 view 路径处理。

### 4.2 B5 DML

`CATALOG_CLOSED_V1` 只考虑 B5 v4 已允许的 simple 形态：

- `INSERT INTO schema.table(explicit columns) VALUES(...)`；无 SELECT/upsert/RETURNING。若表无 default/identity/generated，省略列按 `write.implicit_null` 计 write target。
- `UPDATE schema.table SET col=closed_expression ... WHERE closed_predicate`；无 FROM/subquery/RETURNING。
- `DELETE FROM schema.table WHERE closed_predicate`；write target 是 relation-level row delete，不伪造成写全部列。
- parser 产 action、write targets 和 references；catalog 将 attnum/name/type/collation 封印。PREPARE/plan 不能提供或覆盖这些事实。
- ordinary non-expression/non-partial index 只有在 index AM/opclass/opfamily/support 全部命中 major-pinned builtin manifest，并把保守 index key internal reads 纳入 reference facts 后才可允许。第一切片可以更窄地拒绝非必要 index。

B5 不允许“退表级”。action/write/reference grant 缺一即拒绝；approval 也不能覆盖。complex DML 即使 C 技术上能识别，仍受 B5 v4 product matrix 拒绝，不能用安装扩展偷偷打开。

### 4.3 两模式都继续 unsupported 的对象

除非原 B2/B5 设计另立 release gate，v0.4 两模式都拒绝：recursive query/view、matview（S3m 未完成）、whole-row/system/composite、partition/inheritance/foreign/temp/unlogged/system relation、RLS、trigger、非受支持 rule、default/identity/generated、任一方向 FK、CHECK/exclusion、expression/partial index、用户 function/operator/cast/type/collation，以及 B5 RETURNING/upsert/MERGE/CTE/subquery/UPDATE FROM/DELETE USING。

## 5. 精度缺口与 fail-closed 矩阵

| 缺口/事件 | 默认动作 | 可退表级条件 | 可选 C | 误拒影响 |
|---|---|---|---|---|
| 普通 view 逐列 lineage 不可得 | `AUTH_BINDER_MODE_REQUIRED` | 仅调用本来就是 legacy table-profile，且所有关系无 active column policy/enrollment；审计标 `not_column_protected` | 是 | view-heavy workload 高 |
| JOIN/CTE/LATERAL scope 不在 closed capability | 拒绝 | 同上，绝不能在 B2 请求内静默退 | capability 覆盖时是 | reporting workload 中高 |
| whole-row/system/composite | `AUTH_COLUMN_SHAPE_UNSUPPORTED` | 否 | 当前 C 设计也拒绝 | 有意拒绝，不算实现回归 |
| function/operator/cast exact OID 不可证 | closed builtin 之外拒绝 | 仅 legacy 且无 column policy | C 可证 builtin；用户代码仍拒绝 | ORM cast/function 查询中高 |
| trigger/routine body closure 不可证 | 存在即拒绝 | B2 legacy 只在无 column policy；B5 否 | 当前同样拒绝 | schema eligibility 影响大 |
| RLS/rule/default/generated/FK/check 等 | 按 v4 存在即拒绝 | 同上 | 不因此放开 | 与既有 v4 一致 |
| partition/inheritance | 拒绝 | 仅 legacy table path | 当前仍拒绝 | 分区业务高 |
| plan digest 抖动 | 完全忽略 plan digest | 不适用 | C analyzed digest | 无误拒 |
| prepared ABA / invalidation不可见 | 不跨请求信 prepared；每请求锁内重 bind | 不适用 | C 有 generation seal | 增加 RTT，无语义误拒 |
| actual locks 超出 expected | rollback；fresh request 最多重试一次，再 `AUTH_BIND_CLOSURE_MISMATCH` | 否 | 可重新 C bind，但不能同请求热切 | 并发 DDL 时短时拒绝 |
| catalog query error/timeout/row cap | `AUTH_CATALOG_INCOMPLETE` | 否 | 否 | catalog 压力时拒绝 |
| C 缺失 | 选择纯模式，限 closed subset | 符合 legacy 条件才可 | 不适用 | 复杂 SQL 拒绝 |
| C hash/major/ABI mismatch | capability unhealthy；新请求仅可纯模式重 bind | 符合 legacy 条件才可 | 修复安装后恢复 | 扩展升级窗口拒绝 |
| 双模式 facts divergence | `AUTH_BINDER_DIVERGENCE`、拒绝并报警 | 否 | 否 | 应为零；activation P0 |

所谓“退表级”不是授权失败后的兜底。它只能是**请求分类前**选择已有 legacy table-profile 产品路径，并且控制面证明目标没有列级 policy；一旦进入 B2 column route 或存在列权限，退表级会绕过更细策略，禁止。

### 5.1 预计误拒

S0 没有客户 workload，不能给一个伪精确的全局百分比。以下只用于容量/产品预期，必须由 S6 replay 校准：

- 在“永久 ordinary base table、无 implicit object、closed expression”这个 eligible 集合内，direct CRUD SELECT/DML 的 grammar 误拒目标应低于 **5%**；若超过即不激活默认 allow。
- base-table reporting（join/CTE/LATERAL/aggregate）在首个 direct/join slice 的工程预估误拒为 **30–60%**；随着单项 differential capability 增加下降。
- view-heavy、partition-heavy 或函数型 workload 在免装 v1 的预估拒绝为 **70–100%**；这是明确要求 C/unsupported，不应被营销成偶发误拒。
- schema 有 trigger/default/FK/check/RLS 等对象时，拒绝来自既有 v4 implicit-object 安全边界，不应算纯 SQL binder 精度损失。实际客户覆盖率必须把“shape 不支持”和“schema 不 eligible”分开统计。

## 6. Capability 探测与 attestation

### 6.1 握手

每个 datasource/physical connection 初始化时执行：

1. 核验 `server_version_num`、database OID、`current_user/session_user`、role OID、编码、标准字符串设置；将 search_path 固定为 `pg_catalog`。所有业务 relation schema-qualified。
2. 运行普通账号 catalog self-test：所需 catalog 查询必须成功、fixture-free cross-field invariant 一致；错误不解释为空集。
3. 查询 `pg_available_extensions` 与 `pg_extension`。只有扩展已在 server filesystem 可用/已创建时才尝试 capability function。
4. C 返回的 ABI、server major、extension/build/node-manifest/allowlist hash 必须与网关嵌入 attestation 精确匹配。任何空值/未知版本拒绝 C mode。
5. 同时生成纯模式 capability：grammar manifest hash、catalog-query-pack hash、canonical encoder version、builtin OID/signature manifest hash、支持 shape bits。
6. selector 只在 request transaction 之前选择 mode，并把选择结果写入 request proof。

无需为默认模式创建第二个 inspector 账号；普通业务账号是唯一账号。它仍需业务对象的实际 SELECT/DML 权限和 schema USAGE。S0 证明 official PG14–18 fixture 可读取所需 catalog，但 provider fork/未来版本必须重跑 self-test。

### 6.2 审计字段

每次 allow/deny 至少落：

```text
binder_proof_schema, binder_mode, mode_selection_reason,
server_version_num, database_oid, datasource_server_identity,
session_user, current_user, role_oid, fixed_search_path_digest,
raw_sql_digest, statement_class, closed_shape_id,
semantic_facts_digest, engine_evidence_digest,
candidate_relation_oid_digest, actual_lock_digest,
catalog_fpre_digest, catalog_fpost_digest,
capability_digest,
  [catalog grammar/query/encoder/builtin manifest hashes] or
  [C build/extension/node/allowlist hashes],
cache_hint_hit, retry_count, divergence_status,
authorization_reason, policy/control revisions
```

审计不能记 raw predicate literal、DB error text 或完整 view/function body。`datasource_server_identity` 优先绑定配置中的 TLS endpoint/certificate 与 enrollment identity；最小普通账号通常不能读取 host-level system identifier，完全同构 restore 仍属于可信基础设施边界，不能用一个伪 epoch 掩盖。

## 7. 两模式授权结论一致性

一致性不是“两个 mode 对所有 SQL 都给相同 allow/deny”，因为纯模式有意是 C 模式的子集；正确性质是：

```text
Supported(SQL mode, q) ∧ Allow(SQL mode, q)
  ⇒ Supported(C mode, q)
  ∧ SemanticFacts(SQL,q) = SemanticFacts(C,q)
  ∧ AuthorizationDecision(SQL,q) = AuthorizationDecision(C,q)

Unsupported(SQL mode, q) 不推出 C deny；C 可处理其额外能力。
```

保证手段：

- S3/S4 golden 中每个纯模式 allow shape 必须在 PG14–18 对 C 输出逐字段差分；relation、attnum、usage、site、action、write source 任一差异失败。
- 安装 C 的 datasource 可低比例 shadow 运行纯 binder（只 bind、不执行）；差异只报警/停止扩灰，不能选“更宽松”的结果。
- authorizer 接口不接受 mode-specific shortcut。B2 七段顺序、mask meet 与 B5 lattice 完全共享。
- mode-specific digest 不能互换；共同 `semantic_facts_digest` 相等才说明事实相等。
- capability 变化、扩展升级、PG major/minor 升级使 enrollment `needs_rebind`，不能沿用旧 proof。

## 8. 缓存、失效与并发 DDL

### 8.1 可缓存

- gateway raw AST、token/work 计数和 closed-shape classification；key 包含 parser/grammar version + raw SQL digest。
- 每 major 的 immutable builtin OID/signature allowlist；它是签名 artifact 的一部分。
- enrollment catalog frame 与 canonical digest，作为 candidate hint。
- relation OID/qualified-name 候选、local semantic facts候选。
- connection-local prepared statement 作为执行性能优化；不作为 object identity/analyzed proof。

cache key 至少包含 datasource identity、database OID、server major、role OID、固定 search path digest、SQL digest、binder capability digest、catalog fingerprint。不得只按 SQL 文本缓存。

### 8.2 每请求仍必须做

- execution transaction 内按 OID 排序取锁并核对实际 OID/relkind/lock set；
- 从 raw SQL 重建或验证 closed facts；
- Fpre/Fpost typed catalog scan与 enrollment比较；
- session role/search_path核验；
- PREPARE/EXECUTE 同 backend；
- authorizer、audit、fence、seal。

PG 没有普通 SQL 可用的全局 DDL epoch。S0 的 prepared ABA 证明“prepared 还存在”不是缓存有效性。跨请求 catalog cache 永远不能省略锁内 Fpre/Fpost。

### 8.3 DDL race

流程保持候选事务 → 执行事务：候选 name/OID 只是 hint；执行事务按 server-quoted name 取锁后必须核对实际 OID等于候选。名称重用、kind变化、闭包增减、Fpre变化、PREPARE 意外 lock 均回滚并限次重试。锁保持到执行和 Fpost 结束。

## 9. 性能

S0 warmed Docker loopback、每格 20 warmup + 120 samples：

| 路径 | p95（10 个 major/account 格） | 与 ~86ms C 参考的表面差值 |
|---|---:|---:|
| lock + PREPARE + catalog + DEALLOCATE | 9.546–11.875ms | 低约 74–76ms |
| 已缓存 closed AST 的 lock + catalog revalidate | 6.477–8.876ms | 低约 77–80ms |

这些值没有包含业务执行、结果读取/mask/seal，也没有做 C 的 typed analyzed walk，因此不能宣称“纯 SQL 等价且快 8 倍”。可信结论是：**catalog/lock RTT 有足够余量；安全瓶颈是语义可见性，不是本机延迟。**

性能实现建议：

- catalog identity、implicit gate、shape、locks 合并为少量参数化 typed queries；禁止 per-column RTT。
- 默认每请求重新 PREPARE/DEALLOCATE 已低于现有 86ms 参考，不必为跨请求 prepared cache牺牲 ABA 安全。
- catalog cache 命中只省 local parse/encoding，不省 Fpre/Fpost。
- 生产 SLO 要覆盖远程 RTT、256 relation、catalog bloat、DDL lock contention 和 pool acquisition；S0 不能外推。
- EXPLAIN 不在授权路径，既避免额外 RTT，也避免 plan-time user code。

## 10. 可选 C 扩展交付

### 10.1 方案 A：网关探测 + 自动加载/CREATE EXTENSION

必须修正方案名称：**网关可自动探测/创建，不能仅凭数据库 SUPERUSER 经 SQL 安装 `.so`。** PostgreSQL 官方文档明确 `CREATE EXTENSION` 前 supporting files 必须已经安装；control 文件位于 `SHAREDIR/extension`，C library 位于 server `$libdir`。详见 [CREATE EXTENSION](https://www.postgresql.org/docs/17/sql-createextension.html) 与 [Packaging Related Objects](https://www.postgresql.org/docs/17/extend-extensions.html)。

可落地形态：

1. 网关发行包附带按 PG major/OS/arch/libc 分组的签名 extension archive。
2. self-managed 主机上由独立 privileged helper（SSH/Ansible/systemd package、Kubernetes init container/DaemonSet）把 `.control/.sql/.so` 放到 `pg_config --sharedir/extension` 和 `pkglibdir`。这一步不是数据库 SQL，也不是普通网关进程权限。
3. 网关连接后先查 `pg_available_extension_versions`；artifact 已存在且 exact hash/major匹配时，bootstrap credential 可自动 `CREATE EXTENSION agentsql_binder VERSION '0.4'`。
4. 创建后立即撤销 PUBLIC，运行账号只获固定 schema USAGE 与必要 function EXECUTE。运行账号不保留 superuser；OS installer、DB bootstrap、runtime 三权分离。
5. managed service 无 filesystem/helper 时直接跳过，不反复尝试 LOAD/CREATE。

扩展“随网关分发”只减少下载/版本选择，不能消除 server-side placement。禁止通过 large object、`COPY PROGRAM`、server-side `lo_export` 等 SQL 技巧写 library；那会要求更危险权限、破坏签名/路径/SELinux/只读文件系统边界，也在云服务不可用。

### 10.2 方案 B：预编译 deb/rpm/apk + 一键脚本 + Docker

artifact 矩阵至少是 PG14–18 × amd64/arm64；再按 glibc/musl 与 packaging ecosystem 区分：

- `agentsql-binder-pg14` … `agentsql-binder-pg18` 的 deb；
- 同名 rpm；
- 同名 apk；
- multi-arch `agentsql/postgres-binder:<PG-major>-<AgentSQL-version>` Docker manifests。

package 声明 exact PG major server dependency，post-install 只放文件/校验权限，不自动连接业务库。`agentsql-binder install --pg-config ...` 一键脚本负责：检测 major/arch/libc/package manager、验证签名/SBOM/hash、安装对应包、用 `pg_available_extensions` 自检；`CREATE EXTENSION` 是显式可选步骤。

PG C backend ABI 按 major 构建；每个 minor 升级仍要跑 CI/S0 compatibility，不因 SONAME 没变就自动签收。Alpine/musl 与 Debian/RHEL 不共享 `.so`。Windows server v0.4 accelerator 未提供 MSI 时明确 unsupported。

自建 PostgreSQL Docker 的首选形态是从 official `postgres:<major>` 派生，build stage 编译/验证，runtime 只 COPY 已签名 `.so/control/sql`；数据库初始化脚本可 `CREATE EXTENSION`，已有 data volume 则由 upgrade job 显式创建/升级。不要依赖 `/docker-entrypoint-initdb.d` 为已有集群补装。

### 10.3 比较与推荐

| 维度 | A 自动探测/创建 | B packages/images |
|---|---|---|
| 真正零 server touch | 否；files 必须先在 server | 否；明确承认安装步骤 |
| self-managed VM | helper可做到半自动 | 最稳定、可审计 |
| Kubernetes/自建 Docker PG | init helper可用 | 派生 image 最简单 |
| 版本/架构治理 | 容易把复杂度藏在 helper | package dependency/registry 清晰 |
| 最小权限 | 易误留 gateway superuser | OS install/bootstrap/runtime 易分权 |
| rollback/SBOM/签名 | 需自建 | 标准供应链能力好 |
| RDS/Aurora/Azure/Cloud SQL | 不可安装自定义 C | 同样不可 |

**推荐：B 为正式交付，A 只做 B 已把文件放好后的自动探测/可选创建。** 对 AgentSQL 自带的 PostgreSQL Docker，提供预装 multi-arch image 是 C mode 最接近“一键”的形态。

### 10.4 RDS/云数据库真实可用性

- Amazon RDS PostgreSQL 只支持 AWS 提供的扩展版本清单，并可再由 `rds.allowed_extensions` 收紧；`rds_superuser` 不是 host root。见 [RDS supported extensions](https://docs.aws.amazon.com/AmazonRDS/latest/UserGuide/PostgreSQL.Concepts.General.FeatureSupport.Extensions.html)。`agentsql_binder` 未被服务商收录时，A/B 都不能安装。
- Aurora PostgreSQL 同样按服务商发布的 [supported extension list](https://docs.aws.amazon.com/AmazonRDS/latest/AuroraPostgreSQLReleaseNotes/AuroraPostgreSQL.Extensions.html) 提供文件；客户不能上传任意 `.so`。
- Azure Database for PostgreSQL Flexible Server 明确要求 extension 在 `azure.extensions` allowlist，且[不能 bring your own extension](https://learn.microsoft.com/en-us/azure/postgresql/extensions/how-to-create-extensions)。
- Google Cloud SQL 明确写明[只能安装 Cloud SQL 支持的扩展，不能创建自己的扩展](https://docs.cloud.google.com/sql/docs/postgres/extensions)。

因此 managed cloud 的当前真实 C 可用率按常见标准产品是 **0**，除非云厂商正式收录/合作发布。TLE/可信语言扩展不能提供 PostgreSQL private `Query` walker、plan cache hook 和 C ABI，不能冒充本 binder。云客户的产品主路径必须是纯模式；需 view/复杂能力时只能选择 unsupported、迁移到 self-managed PG，或等待 provider partnership。

## 11. 分阶段实现切片

所有切片保持现有 flags off，直到完整 activation gate；本单不实施它们。

### S1：统一合同与 proof freeze

- 冻结 `BinderMode/CapabilityAttestation/SemanticFacts/BoundProgram/FinalBinderProof`、mode-specific error、audit schema。
- 把 common facts 与 engine evidence digest 分开。
- 冻结 fallback 只能在 request transaction 前选择。

验收：mock 两模式对共同 facts 得同一 B2/B5 decision；未知 mode/version fail closed；无生产 flag 变化。

### S2：catalog kernel

- 参数化读取 identity/columns/index/implicit/shape/dependencies；canonical encoder；OID-sorted lock 与 own-lock核验；Fpre/Fpost。
- 普通账号 capability self-test；PG14–18 golden；candidate/execute 两事务与 ABA/DDL race tests。

验收：复现本 S0 5×2；prepared ABA 不能命中 authority cache；catalog error/empty contradiction全部拒绝。

### S3：B2 closed binder

- gateway raw parser、scope/name/type resolver；首个 direct/self-join/base-table expression subset。
- PREPARE lock-set cross-check，不调用 EXPLAIN；生成 B2 output/reference facts。
- 后续 capability bits 单独加入 join、CTE/subquery/LATERAL、star、aggregate，每项先做 C differential。

验收：PG14–18 × 普通/super × search_path/role/ambiguity corpus；每个 allow 与 C facts逐字段相等；view固定要求 C。

### S4：B5 simple DML closed binder

- INSERT VALUES、simple UPDATE/DELETE 的 action/write/reference；omitted NULL；reserved target；index conservative closure。
- 接入现有 `b5dml` lattice/closure；复杂形态继续 product deny。

验收：五 major 与 C DML ABI差分；write/ref/implicit-null/row-delete exact；trigger/rule/default/FK等执行前拒绝。

### S5：可选 C 供应链

- 5 major × 2 arch 的 deb/rpm/apk pipeline、签名/SBOM/provenance；multi-arch Docker images。
- installer/self-test、bootstrap/runtime 权限拆分、upgrade/rollback；gateway capability probe。

验收：fresh/upgrade/rollback、wrong major/arch/libc、tamper、只读 FS；RDS detector不尝试安装。

### S6：双模式集成与 divergence gate

- selector、same authorizer、audit attestation、mode dashboards；shadow differential/replay。
- customer query corpus测 coverage/误拒；性能/DDL contention/remote RTT。

验收：共同支持集 divergence=0；纯 allow 不多于 C allow；mode切换不复用 proof；误拒达到第5.1目标。

### S7：故障与发布门

- concurrent DDL/ABA/restore/failover、catalog permission loss、extension upgrade midway、pool role/search_path contamination、control outage。
- 更新 B2/B5 S7/S10 release matrix、fallback演练和 managed-cloud 文档。

验收：任何信息缺口零数据/零写；审计可按 request 还原 mode/capability/facts；owner签收后才另立 activation change。

## 12. v0.4 发布边界

### 可进入默认免装实现候选

- PG14–18、一个普通数据库账号；ordinary permanent base tables。
- typed catalog identity/fingerprint、implicit/shape negative gate、OID locks、Fpre/Fpost。
- closed SELECT direct/self-join 与逐项签收的 base-table join/expression子集。
- closed simple INSERT VALUES/UPDATE/DELETE，严格复用 B5 action/write/reference lattice。
- 无扩展时稳定 capability discovery 与明确 `AUTH_BINDER_MODE_REQUIRED/UNSUPPORTED`。

### 仍要求 C 扩展

- 普通 view 的 exposed identity + base逐列 lineage/逐层 ACL；
- closed local resolver 尚未签收的 JOIN/CTE/subquery/LATERAL/set/distinct/window等；
- exact analyzed function/operator/cast/type/collation OID 与 plan-independent analyzed digest；
- 需要 C plan-generation/invalidation attestation 的同 backend prepared seal。

### 即使有 C 仍 unsupported

以第4.3节和 B2/B5 原设计为准；安装扩展不能扩大未签收的产品矩阵。

## 13. 未决风险与负责人签收

1. **closed parser divergence 是主要新增 P0。** PostgreSQL name/type/operator resolution 的任何近似都可能少算 reference。必须靠固定 search path、schema qualification、极窄 grammar、actual relation lock check 和 C differential共同关闭。
2. **纯模式 view 缺失会影响“功能最大”。** RDS 客户无法用 C；负责人需接受首发 view 返回 mode-required，或追加 gateway-side PostgreSQL analyzer 项目。不得用 EXPLAIN 字符串妥协。
3. **普通账号 catalog visibility 是 official-image S0，不是所有 provider/fork 契约。** 每 datasource启动 self-test，权限变化立即 unhealthy。
4. **完整同构 restore/恶意 DBA 属可信基础设施边界。** 最小账号无可靠 host system identifier；TLS endpoint/enrollment/control identity与每请求 locks/fingerprint降低普通 race，不解决能恢复全部证据的管理员攻击。
5. **性能数字非生产 SLO。** remote RTT、catalog规模、DDL争用和业务执行未覆盖。
6. **误拒率尚无客户 corpus。** 第5.1是工程目标/预估，不是证据；S6必须给真实 replay分布后才能做产品承诺。
7. **C artifact 供应链规模不小。** 5 major × 2 arch × distro/libc、多 minor CI、签名/rollback需要长期 owner；若不承担，C mode只能标 community/self-build，不得宣称官方易部署。
8. **云厂商收录不在 v0.4 控制内。** provider partnership可作为后续渠道，但默认架构不能依赖它。

建议 owner 裁决：接受“免装默认 = closed base-table 高安全子集；C = self-managed 的功能扩展；标准云数据库保持 closed/unsupported”，并授权 S1–S4 feature-off 实现。若产品要求 RDS 上普通 view/开放 SQL 与 C 完全同等，则本 S0 的答案是 **当前方案不可行**，需要新立 gateway-side full PostgreSQL analyzer，而不是继续堆 catalog/EXPLAIN 查询。


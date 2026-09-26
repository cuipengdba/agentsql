# AgentSQL v0.4 B2 — S0 empirical findings

Date: 2026-09-24  
Host: Windows, Docker Engine via `npipe:////./pipe/docker_engine`  
Scope: empirical database mechanisms only; no product code changes.

## Reproduction contract

- Inputs live under `.design/s0/`.
- PostgreSQL coverage is at least major versions 14 and 18; MySQL uses the
  cached `mysql:8` image and records `SELECT VERSION()`.
- Containers are ephemeral and removed after each experiment.
- Each section distinguishes observed fact from design inference and classifies
  the mechanism as **可技术关闭**, **需整体 unsupported**, or **属原理边界**.

## Results

Sections are appended as experiments complete.

### 1. FK 隐式触发器与入向边

**结论：可技术关闭，但 v3 的 PG 扫描 SQL 必须修正。** 外键是双向执行
依赖：DML 目标是被引用表时，约束的 `conrelid` 仍是 referencing table，
但 RESTRICT/CASCADE 动作由挂在 `confrelid` 上的 internal trigger 执行。只把
候选 OID 与 `pg_constraint.conrelid` 连接会稳定漏掉入向 FK。

**实测版本。** PostgreSQL 14.24 (`140024`) 与 18.6 (`180006`)；MySQL
8.4.11。精确可执行输入见 [01_pg_fk.sql](s0/01_pg_fk.sql) 与
[01_mysql_fk.sql](s0/01_mysql_fk.sql)，容器化 runner 为
`go run ./.design/s0/spike.go -experiment fk`。

**PG 证据（两个版本结果一致）。** 一个 `child(parent_id) REFERENCES
parent(id) ON UPDATE CASCADE ON DELETE RESTRICT` 产生四行 `pg_trigger`：

```text
trigger_relation  tgisinternal  conrelid     confrelid
s0_fk.parent      true          s0_fk.child  s0_fk.parent
s0_fk.parent      true          s0_fk.child  s0_fk.parent
s0_fk.child       true          s0_fk.child  s0_fk.parent
s0_fk.child       true          s0_fk.child  s0_fk.parent
```

以 parent 为候选，错误查询 `c.conrelid = parent_oid` 返回 `rows=0`；双向
查询返回 `child_parent_id_fkey, incoming`。更新 parent PK 后 child FK 从 1
实变为 2；删除 parent 分别报 SQLSTATE `23503`（PG14）与 `23001`（PG18），
直接证明 parent-side trigger 会执行。SQL 中的 OID 随实例变化，因此证据用
`regclass` 名称表达身份，不把样本 OID 当稳定值。

生产判定必须使用：

```sql
WITH target(relid) AS (SELECT unnest($1::oid[]))
SELECT c.*,
       CASE WHEN c.conrelid=t.relid AND c.confrelid=t.relid THEN 'self'
            WHEN c.conrelid=t.relid THEN 'outgoing'
            ELSE 'incoming' END AS direction
FROM pg_catalog.pg_constraint c
JOIN target t ON c.conrelid=t.relid OR c.confrelid=t.relid
WHERE c.contype='f';
```

不能用 `WHERE NOT t.tgisinternal` 代表“没有 trigger”：FK 的执行触发器恰为
internal；应独立扫描用户 trigger，并用上述 constraint 边扫描拒绝 FK。

**MySQL 证据。** 对 parent root，双方向 `KEY_COLUMN_USAGE` 查询返回：

```text
child_parent_fk  s0_fk.child  s0_fk.parent  incoming
```

入向条件必须包含
`REFERENCED_TABLE_SCHEMA=root.schema_name AND REFERENCED_TABLE_NAME=root.table_name`；
完整列级、双方向检测 SQL 在 `01_mysql_fk.sql`。

**对 v4 的具体建议。** 将 FK 从单 relation “implicit object” 改成闭包边：
PG 对每个 root 同时枚举 `conrelid/confrelid`，MySQL 同时枚举 referencing 与
`REFERENCED_*`；任一方向存在即首发 `AUTH_IMPLICIT_OBJECT_UNSUPPORTED`。
若以后支持 FK，授权/锁闭包必须纳入边的两端及动作语义，不能仅取消拒绝行。

### 2. PostgreSQL parse/analyze/rewrite 锁、显式排序锁与持锁闭包

**结论：可技术关闭。** SQL 层 `PREPARE` 在 `BEGIN READ ONLY` 中确实执行
服务端 parse/analyze/rewrite 而不执行 statement，并把 rewrite 涉及的用户
relation 的 `AccessShareLock` 持有到事务结束。不能依赖服务器的自然取锁顺序
来避免多请求死锁；执行事务应对候选 OID 排序后显式锁，并在同一事务重做 bind/
rewrite，再以 `pg_locks` 枚举实际闭包，闭包外用户 relation 一律拒绝。

**可复现输入。** [02_pg_locks.sql](s0/02_pg_locks.sql) 给出可用三个
`psql` session 重放的 SQL；自动并发 runner：
`go run ./.design/s0/spike.go -experiment pg-locks`。实测 PostgreSQL 14.24
与 18.6，结果一致。

fixture 是 `v_ab -> (base_a JOIN base_b)`。两个独立事务预先分别以
`AccessExclusiveLock` 阻塞 `base_a/base_b`，第三个只读事务执行：

```sql
PREPARE s0_order(integer) AS
SELECT secret FROM s0_lock.v_ab WHERE id=$1;
```

observer 从 `pg_locks` 得到的真实时间线是：

```text
step 1: v_ab AccessShare granted; base_a AccessShare waiting; base_b absent
step 2 (release base_a): v_ab/base_a granted; base_b AccessShare waiting
step 3 (release base_b): v_ab/base_a/base_b AccessShare all granted
```

这证明该查询的观测顺序是 view → view 定义左侧 base_a → base_b；这是事实
探针，不是可依赖的跨查询顺序契约。

执行事务先查询 `pg_class.oid` 和 server-quoted name，按数值 OID 排序后生成：

```text
PG14 OIDs 16386,16393,16400:
LOCK TABLE s0_lock.base_a,s0_lock.base_b,s0_lock.v_ab IN ACCESS SHARE MODE
PG18 OIDs 16386,16394,16402: 同一名字顺序
```

`LOCK TABLE` 返回后立即观察，三个候选在两个版本上均为
`AccessShareLock/granted=true`；随后 `PREPARE` 不增加 relation。OID 是该临时
实例的样本值，不应写入设计常量。

实际持锁闭包的枚举查询为：

```sql
SELECT l.relation, c.relkind, l.mode, l.granted, l.fastpath
FROM pg_catalog.pg_locks l
JOIN pg_catalog.pg_class c ON c.oid=l.relation
JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
WHERE l.pid=pg_backend_pid() AND l.locktype='relation' AND l.granted
  AND n.nspname NOT IN ('pg_catalog','information_schema','pg_toast');
```

把候选故意缩成 `{v_ab,base_a}` 再 `PREPARE`，两个版本的集合差查询均返回
唯一一行 `base_b`。因此判定必须是：`actual_user_relations - candidate_closure`
非空即 `AUTH_BIND_CLOSURE_MISMATCH`，rollback/discard；还应反向要求每个候选
OID 都有期望 mode 的 granted lock。

**对 v4 的具体建议。** 保留“两事务”边界：分析事务只产候选身份并结束；
执行事务以 `lock_timeout`、总 deadline 包围 OID-sorted 显式锁，立即核对候选，
随后在锁内重新 analyze/rewrite 并从 `pg_locks` 枚举闭包。审计记录排序候选与
实际闭包 digest。不要把第一次分析事务持有的锁误当成执行期 fence，也不要
把本 fixture 的自然锁顺序硬编码成 binder 规则。

### 3. PostgreSQL 多层物化视图、定义 lineage 与定义闭包锁

**结论：可技术关闭，但 v3 的统一 `LOCK TABLE` 做法不可行，必须按
`relkind` 修正。** 普通读取 matview 只访问已物化 heap，不会把 `_RETURN`
定义纳入 rewrite closure。定义 relation closure 可由 `_RETURN` rule 的
`pg_depend` 递归得到；逐输出列 lineage 可从已分析 `ev_action` 的
`TargetEntry.expr` 中遍历 `Var(varno,varattno,varlevelsup)` 得到。生产代码必须
使用按 PG 主版本构建的后端节点 walker，不能解析 deparse 文本或依赖正则。

**可复现输入。** [03_pg_matview.sql](s0/03_pg_matview.sql)；完整并发、节点
探针和锁 runner 为 `go run ./.design/s0/spike.go -experiment pg-matview`。
实测 PostgreSQL 14.24 与 18.6。

fixture：`mv1 = base_a JOIN base_b`，`mv2 = SELECT id,upper(secret) FROM mv1`。
在只读事务中 `PREPARE SELECT secret_upper FROM mv2` 后，两个版本的
`pg_locks` 都只有：

```text
mv2  relkind=m  AccessShareLock  granted=true
```

`mv1/base_a/base_b` 均不存在，故执行 SQL 的 rewrite closure 不能替代 matview
定义授权/定义锁闭包。

递归 `_RETURN` dependency 查询（完整 SQL 在 fixture）返回：

```text
base_a(r), base_b(r), mv1(m), mv2(m)
```

`pg_depend.refobjsubid` 还稳定返回 `mv1 -> base_a.id/secret, base_b.id/note`
与 `mv2 -> mv1.id/secret`，但它只给“整条 rule 使用了哪些列”，不能把依赖
分配给单个 output TargetEntry。

runner 对 `ev_action::text` 实际 analyzed tree 做了 evidence-only walker，按
rtable 的 varno 解引用每个非 junk TargetEntry，两个版本结果相同：

```text
mv1.id           -> base_a.id
mv1.secret       -> base_a.secret
mv1.note         -> base_b.note
mv2.id           -> mv1.id       -> base_a.id
mv2.secret_upper -> mv1.secret   -> base_a.secret
```

这里仅列 output lineage；完整授权 reference set 还必须遍历 join quals（本例
还引用 `base_b.id`）、WHERE/GROUP/ORDER 等所有表达式节点。版本探针发现
`ev_action` 的同类 rtable node 在 PG14 序列化为 `{RTE ...}`，PG18 为
`{RANGETBLENTRY ...}`，且 PG14 的 rule rtable 带 old/new 项、PG18 fixture 不带。
这直接否定跨版本文本格式假设；`.design/s0/spike.go` 中的小型正则 walker
仅用于显示证据，不是 v4 实现建议。v4 扩展应在服务端用对应主版本 headers
将 `pg_node_tree` 还原为 Node/Query 并走 typed walker，能力 digest 绑定 major、
extension hash 与 node manifest。

**锁实证。** 对 OID-sorted 四对象执行单条：

```sql
LOCK TABLE base_a,base_b,mv1,mv2 IN ACCESS SHARE MODE;
```

两个版本均报 SQLSTATE `42809`（PG14 文本为 `"mv1" is not a table or
view`，PG18 为 `cannot lock relation "mv1"`）。因此 v3 所写“候选 relation
统一 LOCK TABLE”不能覆盖 matview。以下按数值 OID 顺序逐对象执行的做法通过：

```text
relkind r/v: LOCK TABLE qualified_name IN ACCESS SHARE MODE
relkind m:   SELECT 1 FROM qualified_name LIMIT 0
```

两个版本在最后一步后，`base_a/base_b/mv1/mv2` 全部为 granted
`AccessShareLock`。`SELECT ... LIMIT 0` 是 AgentSQL 生成的无用户表达式内部
语句；仍须纳入总 deadline，并在每一步后/最终用 OID 核对 lock。

**对 v4 的具体建议。** enrollment/bind 时递归 rule dependency relation，
typed walker 同时产出逐 output lineage 与全 reference set；执行时对整个定义
闭包按 OID 排序并按 relkind 使用上述两种取锁原语，再 bind、枚举实际锁集合、
Fpre。若不愿引入 matview 专用取锁路径，则首发必须把所有 matview 整体标为
`AUTH_RELATION_SHAPE_UNSUPPORTED`，不能声称普通 view 机制自然覆盖。

### 4. 双方言隐式对象“存在即拒绝”扫描

**结论：可技术关闭（以拒绝而非支持这些对象为前提）。** 完整 fixture 与
判定 SQL 分别在 [04_pg_implicit.sql](s0/04_pg_implicit.sql) 和
[04_mysql_implicit.sql](s0/04_mysql_implicit.sql)；runner：
`go run ./.design/s0/spike.go -experiment implicit`。

**PostgreSQL 14.24/18.6 实测。** 同一 union-all detector 在两个版本逐类
返回：

| kind | 行数 | relation |
|---|---:|---|
| column_default | 1 | `t` |
| generated | 1 | `t` |
| rls_policy | 1 | `t` |
| view_rule (`_RETURN`) | 1 | `v` |
| trigger | 2 | `t`,`v` |
| instead_of_trigger | 1 | `v` |
| expression_index | 1 | `t` |
| partial_index | 1 | `t` |
| exclusion_constraint | 1 | `t` |

查询使用 `pg_trigger`（普通 trigger 要 `NOT tgisinternal`，INSTEAD OF 再检
`tgtype & 64`）、`pg_attrdef + pg_attribute.attgenerated`、`pg_policy`、
`pg_rewrite`、`pg_index.indexprs/indpred`、`pg_constraint.contype='x'`。
`trigger` 与 `instead_of_trigger` 有意重叠；findings digest 可去重 object OID，
但任一行都拒绝。RLS 还应额外交叉检查 `pg_class.relrowsecurity` 与
`relforcerowsecurity`，这样即使启用 RLS 却尚无 policy 也不能被空结果放行。
FK 必须另用第 1 节双向 constraint 扫描，不能被 `NOT tgisinternal` 排除。

**MySQL 8.4.11 实测。** 修正后的 detector 返回：

| kind | 行数 | object |
|---|---:|---|
| column_default | 2 | `t.payload`,`t.updated_at` |
| generated_column | 1 | `t.payload_len` |
| on_update_expression | 1 | `t.updated_at` |
| trigger | 1 | `t_bi` |
| view | 1 | `v` |
| expression_index | 1 | `t_expr_idx` |
| event | 1 | `ev_touch` |

functional index 的实际 `INFORMATION_SCHEMA.STATISTICS.EXPRESSION` 是
`lower(\`payload\`)`。MySQL 没有 PG 的 partial index、exclusion constraint、
RLS policy 或 INSTEAD OF trigger；这些是方言能力不存在，不能把对应空 catalog
结果解释为“对象已安全支持”。

实测还否定了 v3 风格的两个宽松判据：

1. `EXTRA LIKE '%GENERATED%'` 同时命中 `DEFAULT_GENERATED`，首次运行把四列
   错报成 generated；正确判据是
   `coalesce(GENERATION_EXPRESSION,'') <> ''`。
2. `INFORMATION_SCHEMA.COLUMNS` 会对 view 暴露列显示来自 base column 的
   default；首次修正后 `v.payload` 被错报。default/generated/ON UPDATE 扫描
   必须 join `INFORMATION_SCHEMA.TABLES` 并限定 `TABLE_TYPE='BASE TABLE'`。

EVENT 的 metadata 没有可靠 referenced-table edge，因此 fixture 的判定是：
root 所在 schema 只要有一个对 inspector 可见的 EVENT，就整体拒绝该 schema
的 B2，而不是尝试从 `EVENT_DEFINITION` 文本猜依赖。

**对 v4 的具体建议。** Fpre/Fpost 和 enrollment 复用完全相同的参数化 union；
结果为零才继续，任一 catalog 权限错误/行数超限/交叉字段矛盾均
`AUTH_CATALOG_INCOMPLETE`。首发把上述对象稳定映射为
`AUTH_IMPLICIT_OBJECT_UNSUPPORTED`。普通 view 的 `_RETURN` 是受支持 view
定义入口，不应与任意非 `_RETURN` rule 混为一谈：仅在 typed view binder、
lineage 和锁全部通过时允许 `_RETURN`；其他 rule 一律拒绝。

### 5. MySQL `LOCK INSTANCE FOR BACKUP` DDL 矩阵

**结论：可技术关闭，限定于本矩阵覆盖的持久对象；临时对象必须拒绝，所有
等待必须另有 client deadline。** 精确 setup/双 session 步骤见
[05_mysql_backup_lock.sql](s0/05_mysql_backup_lock.sql)，逐项 runner 是
`go run ./.design/s0/spike.go -experiment mysql-backup`。实测 MySQL 8.4.11，
持锁连接与 DDL 连接独立，DDL session `lock_wait_timeout=1`。

| 类别 | 操作 | 实测 |
|---|---|---|
| VIEW | CREATE / CREATE OR REPLACE / ALTER / DROP / RENAME TABLE view | 均阻塞，1205，约 1.0s |
| PROCEDURE | CREATE / ALTER / DROP | 均阻塞，1205；DROP 约 2.0s |
| stored FUNCTION | CREATE / ALTER / DROP | 均阻塞，1205；DROP 约 2.0s |
| TRIGGER | CREATE / DROP | 均阻塞，1205 |
| TRIGGER | ALTER | MySQL 无此语法，立即 1064；不是 backup-lock 放行 |
| EVENT | CREATE / ALTER / DROP | 均阻塞，1205 |
| TABLE | RENAME TABLE | 阻塞，1205 |
| TEMPORARY TABLE | CREATE | **允许**，2ms |
| loadable UDF | CREATE FUNCTION ... SONAME | 持锁时超过 3.5s client deadline |
| loadable UDF | DROP FUNCTION（不存在对象） | 阻塞，1205；解锁后立即 1305 |
| PLUGIN | INSTALL / UNINSTALL 已预装 rewriter | 均阻塞，1205 |
| COMPONENT | INSTALL / UNINSTALL 已预装 validate-password | 均阻塞，1205 |

UDF 路径没有遵守本 session 的 `lock_wait_timeout=1`，所以 runner 以独立
3.5s context 取消并关闭该物理连接。解锁后对照语句 1ms 返回 1126（缺少指定
`.so`），证明持锁时并未走到动态库加载错误，而是在 backup gate 等待。UDF
DROP 持锁时 1205，解锁后 1ms 才返回不存在的 1305。`rewriter` plugin 与
`component_validate_password` 均在持锁前真实安装成功；UNINSTALL 持锁时 1205，
解锁后均约 5ms 成功。另一个 plugin 的 INSTALL 解锁后 1126，而
`component_log_sink_json` 的 INSTALL 解锁后成功。这些 post-unlock control
区分了“被 backup lock 阻止”与“DDL 自身无效”。容器随后整体删除。

**对 v4 的具体建议。** MySQL 请求物理连接先取得 backup lock，再建立/扫描
root metadata，保持到 Fpost/rollback/结果封印。`lock_wait_timeout` 与 client
context 两层 timeout 都必须设置；UDF 证明不能只依赖前者。TEMPORARY object
DDL 不受保护，故任何 temporary table/view 或 session 临时对象进入语义均
`AUTH_RELATION_SHAPE_UNSUPPORTED`。`BACKUP_ADMIN` 只给专用执行连接角色，
不要给用户 SQL；每个目标 patch version 在升级前复跑同一矩阵。此结论不能
外推到未枚举的账户、tablespace、replication/admin DDL。

### 6. MySQL GTID/binlog watcher、metadata 权限与 DDL epoch

精确输入见 [06_mysql_change.sql](s0/06_mysql_change.sql)，自动 runner：
`go run ./.design/s0/spike.go -experiment mysql-change`。实测 MySQL 8.4.11，
`gtid_mode=ON`、`enforce_gtid_consistency=ON`、ROW binlog。

#### 6a. Fpost 指定水位与 watcher

**结论：可技术关闭。** Fpre 是 `mysql-bin.000003:1727`、GTID set
`server_uuid:1-12`。独立连接创建 view 后，Fpost 是同文件 `:2034`、GTID set
`server_uuid:1-13`。从 Fpre position 消费得到恰好：

```text
1727 Gtid  -> end 1806, SET @@SESSION.GTID_NEXT='server_uuid:13'
1806 Query -> end 2034, CREATE ... VIEW s0_change.v_after_fpre ...
```

watcher checkpoint 达到 2034，累积 set 为 `:1-13`，服务端
`GTID_SUBSET(fpost_set, watcher_seen_set)` 返回 1。因此 watcher 可以在 Fpost
取得指定水位后等待/证明“已消费到至少该水位”，而不是用固定 sleep 猜测。
专用 watcher 用户只授予 `REPLICATION SLAVE, REPLICATION CLIENT`，实测
`SHOW BINARY LOG STATUS` 与 `SHOW BINLOG EVENTS` 均成功。

v4 应持久化 `(server_uuid,binlog_file,end_log_pos,seen_gtid_set)`；每请求在取得
backup lock 后读 target waterline，只有 `GTID_SUBSET(target,seen)=1` 且 file/
position continuity 无 gap 才扫描 catalog。rotate 可跨文件继续，PURGE/RESET、
UUID 变化、无法证明连续性一律 freeze/re-enroll。

#### 6b. INFORMATION_SCHEMA 最小可见权限

**结论：若要求单一“看全 metadata 但无 DDL”角色，则需整体 unsupported；
MySQL 8.4.11 无法构造该角色。** 四个真实用户的行数如下：

| grants | trigger | view | routine | event | definition 可见 |
|---|---:|---:|---:|---:|---|
| none | 0 | 0 | 0 | 0 | 全 0 |
| `SELECT db.*` | 0 | 2 | 0 | 0 | 两个 view definition |
| SELECT + SHOW VIEW + SHOW_ROUTINE | 0 | 2 | 1 | 0 | view/routine 完整 |
| 上述 + TRIGGER + EVENT | 1 | 2 | 1 | 1 | 四类完整 |

关键矛盾不是 grant 写法：`TRIGGER` 与 `EVENT` 既控制 I_S 可见性，也授予对应
DDL。拥有完整 metadata 的 `u_full` 实测 `CREATE TRIGGER` 和 `CREATE EVENT`
都成功；同一用户因没有 `CREATE VIEW/CREATE ROUTINE`，对应 DDL 分别报 1142/
1044。故不存在“通过最小 grant 让 inspector 看见 trigger/event 定义但禁止其
创建/删除”的内建 privilege 组合。

v4 若继续支持 MySQL，只能把该 credential 作为 AgentSQL 内部高权限 capability
隔离：不进入通用 executor、不接受用户 SQL、独立连接池/密钥/审计。若安全
要求明确禁止 AgentSQL 持有任何 DDL-capable credential，则 MySQL B2 必须整体
unsupported，不能把不可见对象当作不存在。

#### 6c. object generation / DDL epoch

**结论：binlog 驱动的可观察 generation 可技术关闭；“DDL 与自建 epoch 表
原子提交”不可行；完全同构带外 ABA 属原理边界。** 两个反例：

1. `START TRANSACTION; CREATE VIEW v_gap ...` 返回后立即断开、模拟 gateway
   crash：`v_gap` 已存在，但 epoch 仍为 0。
2. `START TRANSACTION; UPDATE epoch=2; CREATE VIEW v_gap ...`，DDL 因重名报
   1050；DDL 的前置 implicit commit 已把 epoch 提交，最终 epoch=2，虽然 DDL
   失败。

所以 bump-after 有未记录 DDL crash window，bump-before 有 phantom generation；
stored procedure/named lock 不能消除 MySQL DDL implicit commit。可行实现是让
binlog GTID 成为事实序列：watcher 按 GTID 幂等写 control-plane
`object_generation/ddl_epoch`，请求等待 watcher 到当前 target，再做 backup-lock
内 Fpre/Fpost。受控 gateway 可降低延迟并登记意图，但其计数不是事实源。

管理员若能同时绕过 watcher/gateway、禁 binlog/清日志/恢复同构快照，公开接口
仍无法区分 ABA；这就是需显式签收的原理边界。无法接受隔离 credential、连续
日志与 DBA 信任三项中的任一项时，MySQL B2 整体 unsupported。

### 7. Canonical 编码碰撞、多字节标识符与大小写模式

**结论：编码与 l_c_t_n 0/1 可技术关闭；`lower_case_table_names=2` 在本目标
Linux 容器不能实证，v4 对实际值 2 应先 unsupported。** SQL 见
[07_mysql_canonical.sql](s0/07_mysql_canonical.sql)，Go 参考编码见
[07_canonical.go](s0/07_canonical.go)，runner：
`go run ./.design/s0/spike.go -experiment canonical`。

**`CONCAT_WS` 碰撞（MySQL 8.4.11）。** 三个 equality 均为 1：

```text
('a|b','c')              -> a|b|c == ('a','b|c')
('a',NULL,'b')           -> a|b   == ('a','b',NULL)
('expr:x|y','中文😀')     -> 同字节 == ('expr:x','y|中文😀')
```

因此 delimiter escaping、`CONCAT_WS` 跳过 NULL 的行为和 connection collation
都不适合作为安全 fingerprint。

**Go 侧编码。** 每条 record 先写 field count；每字段依次写固定 type tag、
一字节 NULL/present tag；present 再写 UTF-8 byte length 的 unsigned varint 和
原始 bytes。空串是 `present + length 0`，与 NULL 不同。对上述碰撞对产生的
hex 分别不同，runner 输出 `separator=false null=false expression=false`。
digest 必须对这串 bytes 使用固定 hash/version，不能先转回分隔文本。

**容量。** 30 个中文字符的 I_S 实测 `CHAR_LENGTH=30,LENGTH=90`，插入
`VARBINARY(64)` 报 1406/Data too long；`表😀` 作为 quoted table identifier
创建成功。64 个中文字符虽位于 SQL 字符数上限，当前 Linux/InnoDB fixture
因 filename encoding 报 1030/engine error 168，这不减弱 30 字符反例。
identity/canonical 列至少按最坏 64×4 UTF-8 bytes 再加 framing 分配；建议保存
原名为 `VARBINARY(256)`（或有明确上限的 BLOB），完整 tuple/hash 分列存储，
并在 Go 入库前检查 byte budget。

**`lower_case_table_names` 实测。** 每个模式使用全新 data directory：

| requested / actual | I_S 存储 | 原 casing 查询 | 全小写查询 |
|---|---|---|---|
| 0 / 0 | `CaseDb.CaseTbl` | 成功 | 1049 unknown database |
| 1 / 1 | `casedb.casetbl` | 成功 | 成功 |
| 2 / **0** | `CaseDb.CaseTbl` | 成功 | 1049 |

请求 2 在该 case-sensitive Linux container storage 上被 server 实际降为 0；
runner 读取的是 `@@lower_case_table_names`，不是根据 command line 猜测。因此本
证据不支持模式 2 的匹配语义。v4 每次连接必须读取实际变量：0 用原始 bytes
精确匹配；1 用 server 存储的小写 key（同时把原名纳入 digest）；2 在有真正
case-insensitive filesystem 的独立 CI 证据前标 datasource unsupported。I_S
默认字符串比较可能不区分大小写，fixture 使用 `BINARY` 才验证了实际存储值；
授权匹配不得依赖 I_S 列的默认 collation。

### 8. MySQL `KILL QUERY` 权限、事务/连接状态与结果释放

**结论：可技术关闭。** 精确 fixture/双连接步骤见
[08_mysql_cancel.sql](s0/08_mysql_cancel.sql)，runner：
`go run ./.design/s0/spike.go -experiment mysql-cancel`。实测 MySQL 8.4.11。

**辅助连接与权限。** 正在执行 query 的物理连接不能自行发送 KILL，必须有
第二条连接。对 app thread 的四组结果：

| killer | KILL 结果 | target 结果 |
|---|---|---|
| 不同账户、无 grant | 1095 “not owner” | root 随后清理，target 1317 |
| 不同账户、仅 PROCESS | 1095 | root 清理，target 1317 |
| 不同账户、CONNECTION_ADMIN | 成功 | 1317 Query interrupted |
| 同一 MySQL account 的辅助连接 | 成功 | 1317 |

最小权限方案是同 datasource credential 的专用 auxiliary physical connection；
无需给它 CONNECTION_ADMIN。若运维要求 separate account，则必须授予
`CONNECTION_ADMIN`，`PROCESS` 不够。target thread id 只能从被请求独占的物理
连接读取 `CONNECTION_ID()`，且 KILL SQL 只能拼入已验证的十进制整数。

**事务与连接。** target 先在事务内把 `v:0→1`，再取消长 SELECT。1317 返回
后 root 从 `INFORMATION_SCHEMA.INNODB_TRX` 仍看到该 connection 的活动事务；
target 同事务读到 1，独立 observer 仍读到 0。显式 rollback 成功，随后同一
物理 connection id 可执行查询。结论是 `KILL QUERY` 只中止当前 statement，
不替代 rollback，也不等同 `KILL CONNECTION`。

**结果释放时点。** go-sql-driver 实测 `QueryContext` 可以先返回 `err=nil` 和
非空 `Rows`；辅助连接取消后，1317 只在后续 `Rows.Next()`/`Rows.Err()` 出现。
runner 读到 `Next=false`、检查 `Rows.Err=1317`、调用 `Rows.Close()` 后，同一
连接 `SELECT 1` 成功。故“Query 返回 nil”绝不是可以发布/复用的边界：必须
drain 至终止（不得交付已读行）、检查 Rows.Err、Close、rollback/savepoint
rollback，再释放资源。任何 driver/context/frame 错误使协议同步性不能证明时，
直接 discard 物理连接；即使本次 1317 路径实测可复用，v4 采用“取消后一律
close physical connection”是更窄、更安全的策略。

`SELECT SLEEP(30)` 本身是 MySQL 特例：被 KILL 时可以作为函数返回而不报通用
1317；runner 最终使用 `WHERE SLEEP(30)=0`，避免把该特例当取消语义。

### 9. 同机容器 lock + catalog 路径延迟

**结论：可技术关闭（仅证明本机无争用基线，不是生产 SLO）。** SQL path 与
计时边界见 [09_latency.sql](s0/09_latency.sql)，runner：
`go run ./.design/s0/spike.go -experiment latency`。每引擎先 warmup 50 次，再
在同一 warmed physical connection 串行采集 500 次；百分位用 nearest-rank。
Windows host 经 Docker published port 访问容器，无并发 DDL、无业务负载。

| engine/path | 往返 | P50 | P95 | P99 |
|---|---:|---:|---:|---:|
| PG14 full BEGIN+LOCK+catalog+ROLLBACK | 4 | 3.171 ms | 4.530 ms | 5.584 ms |
| PG14 LOCK RTT | 1 | 0.542 ms | 1.102 ms | 1.539 ms |
| PG14 catalog RTT | 1 | 1.046 ms | 1.362 ms | 1.692 ms |
| PG18 full | 4 | 3.210 ms | 4.443 ms | 5.510 ms |
| PG18 LOCK RTT | 1 | 0.584 ms | 1.123 ms | 1.629 ms |
| PG18 catalog RTT | 1 | 1.053 ms | 1.588 ms | 1.730 ms |
| MySQL full backup-lock+3 temp-root ops+catalog+unlock | 6 | 6.636 ms | 9.756 ms | 10.645 ms |
| MySQL backup-lock RTT | 1 | 1.037 ms | 1.541 ms | 1.633 ms |
| MySQL catalog RTT | 1 | 2.153 ms | 3.250 ms | 3.805 ms |

这些结果说明 v3 的 200ms 最小 catalog+lock budget 在本机基线下有数量级余量，
但没有覆盖网络 RTT、catalog 大小、并发 DDL 等待、connection acquisition、
binlog watcher lag 或 256-relation 最坏闭包。上线阈值必须以目标部署压测重新
标定，lock timeout 仍需 fail-closed。

**额外红项：MySQL 1137。** 第一次按 v3 形状执行：临时
`agentsql_catalog_roots` → CTE `roots` → 多个 UNION arm 各 join roots，真实
MySQL 8.4.11 报 `1137 Can't reopen table: agentsql_catalog_roots`，没有产生延迟
样本。MySQL temporary table 在同一 statement 被多次引用仍受 reopen 限制；
CTE 没有自动物化消除此限制。通过的修正版只把 temp roots 作为唯一 outer
scan，各 I_S catalog 使用 correlated subquery。v4 必须采用这种 single-root-
scan 形状、JSON_TABLE 参数，或为每分支复制 temp table；不得直接实现 v3 的
CTE+UNION 文本。1137 必须加入 catalog golden 回归。

## 总结论与 v4 首发边界

### 机制结论汇总

| 机制 | 结论 | v4 必改/条件 |
|---|---|---|
| PG/MySQL FK | **可技术关闭** | 双向枚举 referencing/referenced；PG 不得过滤 internal FK trigger 后就宣称干净 |
| PG 自动锁、OID 排序显式锁、实际闭包 | **可技术关闭** | 两次 bind；OID 排序；候选/实际持锁集合双向核对 |
| PG matview | **可技术关闭（有条件）** | typed `ev_action` walker；定义闭包；matview 用 `SELECT ... LIMIT 0` 取锁，不能 `LOCK TABLE` |
| 隐式对象扫描 | **可技术关闭（靠存在即拒绝）** | 使用本报告修正查询；零行和完整权限同时成立才放行 |
| MySQL backup lock | **可技术关闭（已测对象）** | client deadline 必需；temporary 对象拒绝；升级逐 patch 重跑 |
| MySQL GTID/binlog waterline | **可技术关闭** | continuous watcher、GTID_SUBSET、position continuity、gap freeze |
| MySQL read-all/no-DDL inspector | **需整体 unsupported** | 该单角色不存在；只有接受隔离的 DDL-capable 内部 credential 才能继续 |
| MySQL SQL 内原子 DDL epoch | **不可行** | generation 改由 binlog GTID watcher 驱动 |
| MySQL 完全同构带外 ABA | **属原理边界** | DBA/restore/binlog 威胁模型书面签收；不接受即 MySQL B2 unsupported |
| canonical tuple | **可技术关闭** | Go byte framing；禁止 CONCAT_WS；容量按 bytes |
| `lower_case_table_names=2` | **需整体 unsupported（当前证据）** | 0/1 已测；2 需真实 case-insensitive filesystem CI 后再开 |
| MySQL 取消 | **可技术关闭** | 同账户 aux 或 CONNECTION_ADMIN；Rows.Err/Close + rollback；不确定即 discard conn |
| lock+catalog 时延 | **可技术关闭（基线）** | 生产压测重定 SLO；修复 temp-root 1137 查询形状 |

### 首发能做什么

**本 S0 证据只支持 SELECT-only 首发。** 虽然“无任何隐式对象的 plain table
DML”从目录上有希望，但本轮没有关闭 INSERT/UPDATE/DELETE 的 target/source、
conflict/duplicate-key、RETURNING、受影响行与写后失败矩阵；不能从 SELECT 的
锁/闭包实验外推。DML 应延后到单独真库矩阵通过，不能以 `plain_dml` 名称豁免。

可进入 v4 首发实现/后续验收的窄能力：

- PostgreSQL 14/18：base table 与普通非递归 view 的 SELECT；前提是按 major
  构建的 analyzed-tree binder、完整 OID/attnum lineage、OID-sorted relation
  locks、实际 `pg_locks` 闭包、锁内 Fpre/Fpost 全部启用。
- PostgreSQL matview SELECT 仅在第 3 节 relkind-specific lock 和服务端 typed
  definition walker 落地后可开；否则首发拒绝 matview 更稳妥。
- MySQL 8.4.11：只在 `lower_case_table_names in (0,1)`、backup lock、连续
  GTID/binlog watcher、隔离的内部 metadata credential、修正后的 single-root-
  scan catalog SQL、受控 DBA/restore 条件全部满足时，开放封闭表达式子集的
  base table/普通 view SELECT。
- 双方言任何 relation closure 都必须 enrollment；任一 trigger/default/
  generated/RLS/non-supported rule/INSTEAD OF trigger/FK/CHECK/exclusion/
  expression or partial index/MySQL ON UPDATE 命中即拒绝。MySQL root schema 有
  EVENT 也整体拒绝。

必须首发拒绝或延后：所有 DML；recursive query/view；PG matview（若专用路径未
实现）；MySQL temporary table/view、l_c_t_n=2、routine/UDF/plugin/component
执行；任何 catalog 不可见、watcher 落后/gap、GTID/UUID/restore 异常；以及
任何不能接受 DDL-capable inspector credential 或 DBA ABA 边界的 MySQL
datasource。MySQL 在这些治理条件未签收时不是“降级 SELECT”，而是整个 B2
unsupported。

## 清理与仓库核验

- 所有成功路径均显式 terminate container。异常退出留下的 4 个、带
  `org.testcontainers=true` 且创建时间对应本轮的容器，已按精确 ID 删除并
  同时删除匿名卷；最终只读审计显示 testcontainers-labeled spike 容器为 0。
- 两个本轮前已存在、无 testcontainers 标签的 demo 容器
  (`agentsql-demo-demo-postgres-1` / `agentsql-demo-demo-mysql-1`) 未触碰。
- 未修改产品代码，未创建 git commit；本轮产物仅为本 findings 与
  `.design/s0/` 下的证据脚本。一次中断生成的 workspace `go-build*` 临时目录
  已核对为本轮编译缓存并删除。

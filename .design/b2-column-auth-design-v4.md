# AgentSQL v0.4 B2 多表 JOIN / 自连接列级授权——设计 v4（定稿候选）

状态：**可据以开始 feature-off 实现；不是生产放行结论**  
日期：2026-09-24  
证据基线：[S0 empirical findings](b2-s0-findings.md) 与 [.design/s0](s0/)  
范围：本文件只定义设计、迁移、能力门、验收和发布边界，不包含产品代码变更。

## 0. v4 最终裁决

1. **B2 GA 只做 `SELECT` 列级授权。** `INSERT`、`UPDATE`、`DELETE`、`MERGE`、`RETURNING`、冲突更新、受影响行等均不属于 B2。它们继续走现有表级/profile 规则；这是经过 S0 后主动选择的产品范围边界，不是遗漏或待以风险豁免的缺陷。任何文档、能力位或 UI 均不得把“B2 列级授权”描述为覆盖 DML。
2. 默认发布方案是：**PostgreSQL 完整实现并作为 GA 主路径；MySQL 条件性实现且允许延后到后续发布。** PostgreSQL GA 核心仅为永久 ordinary base table 与普通非递归 view 的 `SELECT`。物化视图必须在 typed definition walker 与 `relkind='m'` 专用取锁路径均落地后才能单独开启，否则稳定拒绝。
3. MySQL B2 不是天然降级能力。只有第 3.4 节全部前置同时成立，才可开放封闭表达式子集上的永久 InnoDB base table/普通非递归 view `SELECT`；缺任一项时，**MySQL B2 在 GA 标记为 `unsupported`**，MySQL 继续使用既有表级规则。
4. 双方言中，只要执行/定义 relation closure 的任一表或 view 存在 trigger、可执行 default、generated、RLS、非受支持 rule、INSTEAD OF trigger、任一方向 FK、CHECK、exclusion、表达式索引或部分索引，即“存在即拒绝”。MySQL 的 `ON UPDATE`、functional index、root schema EVENT 同样拒绝。不得以“本次是 SELECT、对象不会触发”为由绕过。
5. 递归 CTE、递归 view 和 binder 发现的任意递归 route 首发稳定拒绝。
6. 全局跨库锁序唯一为 **control → business**。请求、enrollment、staging bind、`*` 展开和其他 mutation 都不得形成 business → control 的同时持锁。耗时 discovery 使用两阶段：第一阶段不同时持两类锁并释放候选资源；第二阶段取得 control 锁后重新取得 business 锁，并从原始输入完整重做 bind、closure、fingerprint 与 CAS 条件。
7. SELECT-only 后不再承诺不存在的“DML 跨库原子”。SELECT 路径的强语义是：授权审计屏障失败、控制面不可达、Fpost/最终 fence 失败或 seal 失败时，丢弃全部结果并且不返回任何数据。
8. protocol 3 只有在同一发布物具备 binder、catalog/锁、authorizer、mask、资源限制、审计、response seal、fence、迁移和 choke point 全部安全能力，且 safe fallback 已演练后才能声明。

## 1. 规范词与安全目标

文中的“必须/不得”是实现与验收硬要求；“支持”表示所有前置满足时可进入授权；“拒绝”表示稳定 fail-closed；“unsupported”表示该 datasource/对象类别不具备 B2 能力，不得悄悄降级为不完整的列授权。

B2 的保密性目标是：调用方只能观察已获授权的最终列值；用于过滤、连接、分组、排序、去重、窗口和 view 定义的列均需要 reference usage；输出位置需要 output usage；任何不能完整证明身份、lineage、catalog 稳定性、mask meet、审计和交付封印的请求都拒绝。

调用方唯一可提供的数据库程序输入是 caller identity、datasource ID、原始 SQL 和内部 approval ID。调用方不得提供可被信任的 AST、bound identity、lineage、catalog snapshot、protection plan 或授权 proof。

## 2. 冻结的不变量

### 2.1 私有 proof 与唯一执行门

`AuthorizedExecute` 在 `internal/authorizedexecute` 暴露唯一 public façade和statement router。`SELECT`进入本设计的私有 `AuthorizedSelect`；DML进入既有表级/profile authorizer的私有适配器，不读取或声称执行B2列级策略。parse、bind、lineage、catalog scan、authorization、mask plan 和 execution 都在该能力域内从原始 SQL重建，任一分支都拿不到域外raw executor。

`BoundProgram`、`CatalogFrame`、`ProtectionPlan`、`DeliverySeal` 使用不可导出字段和同请求 nonce/seal；序列化、跨请求复用、SQL hash、control revision、catalog digest 或 binder capability digest 任一不一致即无效。

所有 query、explain、approval-execute、demo/sample 和未来入口必须进入同一 `AuthorizedExecute`。用户 `EXPLAIN` 不受支持；系统如需 explain，只能对已经绑定并授权的内部对象调用固定 typed port。

### 2.2 每次 SELECT 的安全边界

一次可交付执行同时满足：

- 同一个 `ControlSnapshotTx` 内的 reader/writer fence 与全部 policy、binding、enrollment、mask revision；
- 一个请求独占的 business physical connection；
- 一个只读事务或可证明只读的 savepoint 边界；
- 完整 relation closure 的 schema 稳定锁；
- 同算法生成且相等的 enrollment、`Fpre`、`Fpost` canonical fingerprint；
- 一个不可变 ProtectionPlan；
- 一个尚未向 transport 输出任何字节的有界 sealed response；
- 成功的 durable authorization audit barrier；
- 交付前在原 `ControlSnapshotTx` 中完成的最终 fence/revision 复核。

任一项失败均 cancel、rollback、关闭或丢弃不再可信的物理连接、丢弃所有中间结果，并返回固定失败 envelope。

### 2.3 决策优先级

优先级固定为：agent/profile/statement-class deny → dialect/datasource unsupported → relation/table deny → 缺 relation allow → enrollment/catalog/closure 不可证明 → 缺 column usage grant → mask meet 不存在/能力缺失 → allow。mask 永远不能补足 reference 或 output grant。

## 3. GA 支持矩阵与拒绝矩阵

### 3.1 对象类型矩阵

| 对象 | PostgreSQL | MySQL | 说明 |
|---|---|---|---|
| 永久 ordinary base table | **GA** | **条件性 GA** | PG `relkind='r'` 且首发要求 `relpersistence='p'`；MySQL 仅永久、非 partition 的 InnoDB |
| 普通非递归 view | **GA** | **条件性 GA** | 必须 typed bind 定义，逐层 exposed ACL + 定义 lineage/base ACL；非 `_RETURN` rule 拒绝 |
| PostgreSQL matview | **条件性，可从 GA 移除** | 不适用 | 仅 typed `_RETURN.ev_action` walker + 定义闭包 + `relkind='m'` 专用取锁全部完成后开启 |
| recursive view / recursive CTE route | 拒绝 | 拒绝 | `AUTH_RECURSION_UNSUPPORTED` |
| partitioned table、partition child、inheritance | 拒绝 | 拒绝 | `AUTH_RELATION_SHAPE_UNSUPPORTED` |
| foreign table / 非 InnoDB / NDB / FEDERATED | 拒绝 | 拒绝 | `AUTH_RELATION_SHAPE_UNSUPPORTED` |
| temporary/session object | 拒绝 | 拒绝 | MySQL backup lock 不阻止 temporary DDL；PG 也不纳入首发稳定身份 |
| unlogged/typed table/system catalog | 拒绝 | 不适用/拒绝 | 首发仅明确 enrollment 的普通永久业务 relation |
| sequence、table function、SRF、JSON_TABLE、routine/UDF | 拒绝 | 拒绝 | `AUTH_OPAQUE_EXECUTABLE` |

“条件性 GA”不是默认开启。默认产品/发布组合应是 PostgreSQL 列授权 GA、MySQL B2 disabled/unsupported，直到负责人完成第 16 节签收且 MySQL 全部能力探针通过。

### 3.2 语句与语法矩阵

| 类别 | PostgreSQL GA | MySQL 条件性子集 | 拒绝 |
|---|---|---|---|
| statement | 单条 `SELECT` | 单条 `SELECT` | 所有 DML、DDL、CALL/DO、COPY/LOAD、CTAS/SELECT INTO、PREPARE/EXECUTE、多语句 |
| FROM | 支持对象上的 INNER/LEFT/RIGHT/CROSS JOIN、自连接 | INNER/LEFT/RIGHT/CROSS、自连接；仅 binder golden 覆盖的形态 | table function、opaque executable、未 enrollment 对象 |
| subquery | 非递归 CTE、derived/scalar/correlated subquery、经差分验证的 LATERAL | 非递归 CTE、derived/scalar/correlated，仅在封闭 binder 覆盖内 | `WITH RECURSIVE`、data-modifying CTE、无法证明的 lateral/name resolution |
| expression | binder 返回精确 OID 且在 versioned allowlist 的 `pg_catalog` scalar type/function/operator/cast；direct column、literal、布尔/算术/比较 | direct column、literal/NULL、同一精确 type+charset+collation 列比较、同型无损 literal 比较、`IS NULL`、AND/OR/NOT、简单同型数值算术 | 用户 function/operator/cast/type/collation、session/system-state function；MySQL 任意函数、CAST/COLLATE、跨类型隐式转换、JSON/geometry/temporal 隐式转换 |
| aggregate/window | allowlisted aggregate；partition/order 列算 reference | 仅 `count(*)`/`count(constant)`，仍为 `rowset_dependent` | ordered-set/hypothetical/WITHIN GROUP；MySQL 其他函数/aggregate 首发拒绝 |
| grouping/order | 普通 GROUP/HAVING/ORDER、window partition/order、LIMIT/OFFSET | 封闭表达式范围内的普通 GROUP/HAVING/ORDER、LIMIT/OFFSET | GROUPING SETS/ROLLUP/CUBE，无法完成 alias/ordinal 真绑定的形态 |
| set/distinct | UNION ALL；UNION/INTERSECT/EXCEPT；DISTINCT、PG DISTINCT ON | 首发可只开放 UNION ALL；其他 set/distinct 须有单独差分 golden 才启用 | 未实现 output+reference 双 usage 的 set/distinct |
| star/join sugar | 锁内展开 `*`、USING、NATURAL | backup lock 内展开 | metadata/CAS 未稳定或超预算即拒绝 |
| locking read | 不支持 | 不支持 | FOR UPDATE/SHARE/KEY SHARE/NO KEY UPDATE 及 MySQL locking read |

规则：DISTINCT key、DISTINCT ON key、非 `ALL` set comparison position、INTERSECT/EXCEPT 每个 arm 都需要 output 与 reference；UNION ALL 不因 set 本身增加 reference。alias/ordinal 必须在真实 name resolution 后，把已绑定依赖深复制到对应 clause site 并标记 reference，不能用 projection 的 output 决策代替。

### 3.3 跨方言“存在即拒绝”矩阵

以下对象只要存在于 query relation closure 或 view/matview definition closure 即拒绝，而不是判断本次 SELECT 是否会触发：

| 对象/状态 | PostgreSQL | MySQL | reason |
|---|---:|---:|---|
| 用户 trigger | 拒绝 | 拒绝 | `AUTH_IMPLICIT_OBJECT_UNSUPPORTED` |
| column default（包括显式 `DEFAULT NULL`） | 拒绝 | 拒绝 | 同上；MySQL 还须以 version-pinned `SHOW CREATE TABLE` parser补足 I_S 无法区分的显式 NULL default |
| generated / ON UPDATE | 拒绝 | 拒绝 | 同上 |
| RLS 或 FORCE RLS | 拒绝 | 方言无此对象 | 同上 |
| 非受支持 rule / INSTEAD OF trigger | 拒绝 | 方言无对应 rule | 同上 |
| FK，incoming/outgoing/self 任一方向 | 拒绝 | 拒绝 | 同上 |
| CHECK / exclusion | 拒绝 | CHECK 拒绝；无 exclusion | 同上 |
| expression/functional index、partial index | 拒绝 | functional 拒绝；无 partial | 同上 |
| EVENT | 不适用 | root schema 存在任一可见 EVENT 即拒绝 | 同上 |
| catalog 权限不完整、交叉字段矛盾、扫描超限 | 拒绝 | 拒绝 | `AUTH_CATALOG_INCOMPLETE` |

普通 view 的唯一 `_RETURN` rule 是受支持定义入口，不作为“rule 存在”本身拒绝；它必须由 typed walker 完整处理。matview 的 `_RETURN` 仅在专用能力开启时作同样处理。

### 3.4 MySQL B2 全部前置条件

MySQL datasource 只有同时满足以下条件才能把 B2 capability 置为 supported：

1. 目标 patch version 已跑过 S0 backup-lock DDL matrix；`LOCK INSTANCE FOR BACKUP` 可用，持锁与所有等待均有 client wall deadline。
2. `@@lower_case_table_names` 实际值只能为 0 或 1。0 按原始 bytes 匹配；1 按 server 存储的小写 key 匹配，同时把原名写入 digest；值 2 直接 unsupported。
3. GTID 开启、server UUID 稳定、binlog 未禁用；连续 watcher 可证明 file/position 无 gap，并能用 `GTID_SUBSET(target, seen)=1` 追过每次 Fpre 与 Fpost 指定水位。PURGE/RESET、UUID 变化、日志 gap、restore/failover 未 re-enroll 均 freeze。
4. 使用**隔离的 DDL-capable inspector credential**读取完整 TRIGGER/EVENT metadata。该 credential 只进入 catalog inspector 私有池，不进入用户 SQL executor、schema sample 或任何通用 query port；密钥、调用、连接池和审计独立。若组织政策不允许 AgentSQL 持有该凭据，MySQL B2 整体 unsupported。
5. DBA、restore/failover、binlog 与带外 DDL 威胁模型按第 16 节 M1 签收；服务账户无 DDL/admin/log purge 能力，gateway 与 watcher 职责分离。
6. root schemas 无 EVENT；请求语义和 session 中无 temporary table/view；只支持永久非 partition InnoDB base table 与普通 view。
7. 修正后的 single-root-scan catalog query、canonical byte encoding、封闭 MySQL binder、inbound packet cap、辅助取消连接和故障丢弃连接能力全部通过。

缺任一项时，对 MySQL 返回 datasource capability `b2_column_auth=unsupported`，不尝试半套列授权，也不把 catalog 空集当作安全；原有表级/profile 流程保持不变。

## 4. Enrollment、catalog 检测与 canonical fingerprint

### 4.1 状态与 fingerprint

```text
relation_enrollments
  id, datasource_id, dialect, server_id, database_id,
  schema_name_bytes, relation_name_bytes, relation_kind, stable_object_id,
  object_generation, catalog_fingerprint, implicit_scan_digest,
  binder_capability_digest, ddl_epoch, status, enrolled_at, revision

relation_enrollment_closure
  enrollment_id, path_ordinal, relation_identity, relation_fingerprint,
  PRIMARY KEY(enrollment_id, path_ordinal, relation_identity)
```

`status = pending_scan|healthy|needs_rebind|unsupported|revoked`。只有 `healthy` 可授权。enrollment、每请求 `Fpre`、`Fpost` 复用同一版本的字段清单、查询和 canonical encoder。

SQL 必须返回独立 typed columns，禁止以 `CONCAT_WS('|',...)` 或任意分隔文本作为摘要输入。Go canonical encoder 对 record 写 field-count；每字段写固定 type tag、NULL/present tag、present byte length varint 与原始 bytes；空串与 NULL 不同。记录按稳定 binary key 排序后使用 versioned hash。标识符至少按 64×4 UTF-8 bytes加 framing 预算，metadata 存储使用 `VARBINARY(256)` 或明确同等容量。

### 4.2 PostgreSQL 检测 SQL

S0 证据：[04_pg_implicit.sql](s0/04_pg_implicit.sql)、[01_pg_fk.sql](s0/01_pg_fk.sql)。生产实现以 closure OID array 为唯一参数；以下查询形状是规范性基线。结果任一行即拒绝；查询错误、超限或 `pg_class.relhas*`/RLS flags 与明细矛盾即 `AUTH_CATALOG_INCOMPLETE`。

```sql
WITH target(relid) AS (SELECT unnest($1::oid[])), findings AS (
  SELECT t.tgrelid AS relid, 'trigger'::text AS kind,
         t.oid AS object_oid, 0::int AS sub_id
  FROM pg_catalog.pg_trigger t JOIN target x ON x.relid=t.tgrelid
  WHERE NOT t.tgisinternal

  UNION ALL
  SELECT d.adrelid,
         CASE WHEN a.attgenerated<>'' THEN 'generated' ELSE 'column_default' END,
         d.oid, d.adnum::int
  FROM pg_catalog.pg_attrdef d
  JOIN pg_catalog.pg_attribute a ON a.attrelid=d.adrelid AND a.attnum=d.adnum
  JOIN target x ON x.relid=d.adrelid

  UNION ALL
  SELECT c.oid, 'rls', COALESCE(p.oid,c.oid), 0
  FROM pg_catalog.pg_class c JOIN target x ON x.relid=c.oid
  LEFT JOIN pg_catalog.pg_policy p ON p.polrelid=c.oid
  WHERE c.relrowsecurity OR c.relforcerowsecurity OR p.oid IS NOT NULL

  UNION ALL
  SELECT r.ev_class, 'rule', r.oid, 0
  FROM pg_catalog.pg_rewrite r
  JOIN pg_catalog.pg_class c ON c.oid=r.ev_class
  JOIN target x ON x.relid=r.ev_class
  WHERE NOT (r.rulename='_RETURN' AND c.relkind IN ('v','m') AND r.ev_type='1')

  UNION ALL
  SELECT q.conrelid, 'constraint:'||q.contype, q.oid, 0
  FROM pg_catalog.pg_constraint q JOIN target x ON x.relid=q.conrelid
  WHERE q.contype IN ('c','x')

  UNION ALL
  SELECT i.indrelid,
         CASE WHEN i.indexprs IS NOT NULL THEN 'expression_index'
              ELSE 'partial_index' END,
         i.indexrelid, 0
  FROM pg_catalog.pg_index i JOIN target x ON x.relid=i.indrelid
  WHERE i.indexprs IS NOT NULL OR i.indpred IS NOT NULL
)
SELECT relid,kind,object_oid,sub_id
FROM findings
ORDER BY relid,kind,object_oid,sub_id;
```

这是可执行的存在性 gate；每类对象的 fingerprint 另用参数化 typed query读取原始 catalog 字段，不授权把字段拼成字符串。

FK 必须独立双向枚举，不能因为 internal trigger 被用户-trigger 查询过滤就宣称安全：

```sql
WITH target(relid) AS (SELECT unnest($1::oid[]))
SELECT c.oid, c.conname, c.conrelid, c.confrelid, c.conkey, c.confkey,
       c.confupdtype, c.confdeltype, c.confmatchtype,
       CASE WHEN c.conrelid=t.relid AND c.confrelid=t.relid THEN 'self'
            WHEN c.conrelid=t.relid THEN 'outgoing' ELSE 'incoming' END AS direction
FROM pg_catalog.pg_constraint c
JOIN target t ON c.conrelid=t.relid OR c.confrelid=t.relid
WHERE c.contype='f';
```

fingerprint 还必须覆盖 `pg_class` identity/relkind/namespace/persistence/options、全部非 dropped `pg_attribute` identity/type/typmod/collation/nullability/identity/generated、受支持普通索引的全部 key part/access method/opclass/opfamily/collation、唯一 `_RETURN.ev_action::text`、typed walker digest 与 `pg_depend` 完整性辅助边。`pg_depend` 不能替代逐表达式 lineage。

### 4.3 MySQL 检测 SQL：single-root-scan

S0 证据：[04_mysql_implicit.sql](s0/04_mysql_implicit.sql)、[01_mysql_fk.sql](s0/01_mysql_fk.sql)、[09_latency.sql](s0/09_latency.sql)。v3 的 temp-root CTE + 多 `UNION` arm 会在 MySQL 8.4.11 报 1137，禁止实现该形状。

请求在 backup lock 后创建 connection-local root 表；大小按 bytes：

```sql
CREATE TEMPORARY TABLE agentsql_catalog_roots (
  schema_name VARBINARY(256) NOT NULL,
  table_name  VARBINARY(256) NOT NULL,
  PRIMARY KEY(schema_name,table_name)
) ENGINE=MEMORY;
```

值只通过参数插入；最多 256 roots。检测语句只能把 temp table 作为唯一 outer scan 一次，每个 catalog 使用 correlated subquery。下面是存在性 gate 的规范形状；实际 fingerprint fetch 使用相同 outer-scan 约束返回独立字段/JSON binary records，再由 Go canonical 编码，不拼接摘要：

```sql
SELECT r.schema_name, r.table_name,
  EXISTS (SELECT 1 FROM information_schema.TRIGGERS tr
          WHERE BINARY tr.EVENT_OBJECT_SCHEMA=r.schema_name
            AND BINARY tr.EVENT_OBJECT_TABLE=r.table_name) AS has_trigger,
  EXISTS (SELECT 1 FROM information_schema.COLUMNS c
          JOIN information_schema.TABLES bt
            ON bt.TABLE_SCHEMA=c.TABLE_SCHEMA AND bt.TABLE_NAME=c.TABLE_NAME
           AND bt.TABLE_TYPE='BASE TABLE'
          WHERE BINARY c.TABLE_SCHEMA=r.schema_name AND BINARY c.TABLE_NAME=r.table_name
            AND (c.COLUMN_DEFAULT IS NOT NULL
              OR COALESCE(c.GENERATION_EXPRESSION,'')<>''
              OR LOWER(c.EXTRA) LIKE '%on update%'
              OR LOWER(c.EXTRA) LIKE '%auto_increment%')) AS has_column_implicit,
  EXISTS (SELECT 1 FROM information_schema.TABLE_CONSTRAINTS tc
          WHERE BINARY tc.TABLE_SCHEMA=r.schema_name AND BINARY tc.TABLE_NAME=r.table_name
            AND tc.CONSTRAINT_TYPE='CHECK') AS has_check,
  EXISTS (SELECT 1 FROM information_schema.KEY_COLUMN_USAGE k
          WHERE k.REFERENCED_TABLE_NAME IS NOT NULL AND
            ((BINARY k.TABLE_SCHEMA=r.schema_name AND BINARY k.TABLE_NAME=r.table_name) OR
             (BINARY k.REFERENCED_TABLE_SCHEMA=r.schema_name AND
              BINARY k.REFERENCED_TABLE_NAME=r.table_name))) AS has_fk_either_direction,
  EXISTS (SELECT 1 FROM information_schema.STATISTICS s
          WHERE BINARY s.TABLE_SCHEMA=r.schema_name AND BINARY s.TABLE_NAME=r.table_name
            AND s.EXPRESSION IS NOT NULL) AS has_functional_index,
  EXISTS (SELECT 1 FROM information_schema.PARTITIONS p
          WHERE BINARY p.TABLE_SCHEMA=r.schema_name AND BINARY p.TABLE_NAME=r.table_name
            AND p.PARTITION_NAME IS NOT NULL) AS has_partition,
  EXISTS (SELECT 1 FROM information_schema.VIEWS v
          WHERE BINARY v.TABLE_SCHEMA=r.schema_name AND BINARY v.TABLE_NAME=r.table_name
            AND v.CHECK_OPTION<>'NONE') AS has_view_check_option,
  EXISTS (SELECT 1 FROM information_schema.VIEW_ROUTINE_USAGE u
          WHERE BINARY u.TABLE_SCHEMA=r.schema_name AND BINARY u.TABLE_NAME=r.table_name)
          AS has_view_routine,
  EXISTS (SELECT 1 FROM information_schema.EVENTS e
          WHERE BINARY e.EVENT_SCHEMA=r.schema_name) AS schema_has_event
FROM agentsql_catalog_roots r
ORDER BY r.schema_name,r.table_name;
```

default/generated/ON UPDATE 扫描必须限制 `TABLE_TYPE='BASE TABLE'`，避免把 view 暴露的 base default 误报；generated 的精确判据是 `COALESCE(GENERATION_EXPRESSION,'')<>''`，不得用宽泛 `EXTRA LIKE '%GENERATED%'`。`INFORMATION_SCHEMA.COLUMNS` 不能可靠区分“无 default”和显式 `DEFAULT NULL`，所以 inspector还必须取得 `SHOW CREATE TABLE`，交给与目标server版本绑定的DDL parser检测任意显式DEFAULT clause；parser不能完整处理即该relation unsupported。FK 的详细扫描同时匹配 referencing 与 `REFERENCED_*` 方向。EVENT 没有可靠 referenced-table edge，所以 root schema 任一 EVENT 都拒绝。

fingerprint 字段至少覆盖 TABLES、COLUMNS、VIEWS + `SHOW CREATE VIEW`、STATISTICS 全 key parts、KEY_COLUMN_USAGE、TABLE_CONSTRAINTS、CHECK_CONSTRAINTS、REFERENTIAL_CONSTRAINTS、VIEW_TABLE_USAGE/VIEW_ROUTINE_USAGE，以及 server UUID/version/lctn/sql_mode/charset/collation/GTID 和 UDF/routine identity environment digest。inspector 权限用真实 fixture 正向证明；空结果不能证明权限完整。

## 5. Binder、lineage 与 relation closure

### 5.1 PostgreSQL analyzed-tree binder

按 PG 14–18 主版本构建、签名和 capability attestation 的 `agentsql_binder` 扩展提供 parse/analyze/rewrite 和 typed `pg_node_tree` walker，不 plan、不 execute。capability digest 绑定 server major、extension ABI/hash、node manifest、allowlist hash。缺失、版本/hash 不匹配或返回超预算，datasource unhealthy。

扩展输出至少包括 query block、command type、recursive/data-modifying 标志；RTE relid/relkind/inh/requiredPerms；Var 的 rel/attnum/type/collation；function/operator/aggregate/window/cast 的真实 OID 与签名；target-list、quals、join、group/sort/distinct/window/limit；rewrite security quals；所有 node path 与 handled/unsupported 状态。

在调用 analyzer 前，raw preflight 先拒绝可能在 analyze 时触发用户代码的 function/type/cast/collation/operator。首发 function 必须显式 `pg_catalog` 且命中 exact signature allowlist；unknown literal 只能流向 allowlisted built-in type。preflight 不能证明即拒绝，不调用扩展。

### 5.2 PostgreSQL 两事务锁模型

S0 证据：[02_pg_locks.sql](s0/02_pg_locks.sql)。流程固定为：

1. 已持 control shared snapshot 后，在独立候选只读事务执行 preflight + analyze/rewrite，取得 candidate closure、OID、relkind、server-quoted qualified name 和 fingerprint；结束该事务。它自动取得的锁不作为执行期 fence。
2. 开执行只读事务，按数值 OID 全序逐对象取 schema 稳定锁。`relkind r/v` 使用 AgentSQL 生成的 `LOCK TABLE qualified_name IN ACCESS SHARE MODE`；启用 matview 时 `relkind m` 使用 AgentSQL 生成的 `SELECT 1 FROM qualified_name LIMIT 0`。所有语句受共同 lock/deadline；不得拼入用户 identifier。
3. 每一步立即按 OID 核对实际被锁 relation，最终反向要求所有 candidate OID 均有 granted `AccessShareLock`。
4. 在同一执行事务重新运行完整 preflight + analyze/rewrite + typed view/matview walk。枚举当前 backend 的全部非系统 granted relation locks。
5. `actual_user_relations - candidate_closure` 非空、candidate 缺锁、OID/name/kind/fingerprint 变化或 closure 改变，立即 rollback/discard，最多从步骤 1 整体重试一次；再次变化返回 `AUTH_BIND_CLOSURE_MISMATCH`/`AUTH_CATALOG_RACE`。
6. 只有第二次锁内 binder 结果参与授权。锁保持到 execute/read/mask/encode/Fpost/audit/read-only commit-or-rollback 全部结束。

名称只用于安全 quote 后发出内部 lock statement，身份始终是 system identifier + database OID + relation OID + attnum。按名称锁定后不核对 OID是禁止实现。

### 5.3 view 与 matview

普通 view 的 rewrite analyzed tree 逐 output TargetEntry 建 lineage，同时遍历 join/WHERE/GROUP/HAVING/ORDER/window 等 reference sites；逐层做 view exposed ACL 与底层 identity ACL。

matview 不会因读取 heap 自动展开定义。只有以下全部落地才可开启：

- 从唯一 `_RETURN.ev_action` 通过对应 PG major headers还原 typed Node/Query；
- walker 按 rtable/varno/varattno/varlevelsup 生成每个 output TargetEntry 的 lineage，并遍历全部 reference expression；
- `pg_depend` 递归得到 relation 完整性闭包，但不把它冒充逐输出 lineage；
- 执行事务对 base/view/matview 完整定义闭包按 OID 排序、按 relkind 使用第 5.2 节取锁原语，并核对实际锁集；
- 多层 matview fixture 在每个受支持 PG major 有 golden。

缺任一项，所有 matview 稳定返回 `AUTH_RELATION_SHAPE_UNSUPPORTED`。是否把这项纳入首个 GA 由负责人单独签收，不能阻塞 PG base/view GA。

### 5.4 MySQL 封闭 binder

MySQL binder 仅信 raw SQL 与 backup lock 内 catalog，实现 query-block scope、CTE/derived/view namespace、alias hiding、clause precedence、唯一列解析、`*`/USING/NATURAL 展开。支持 grammar 严格等于第 3.2 节条件子集；任何函数、UDF/routine、CAST/COLLATE、不确定 coercion 或无法证明的节点稳定拒绝。

同一 physical connection/backup lock 内的随机名 `PREPARE`/`DEALLOCATE PREPARE` 只能作为“server 也接受”的交叉验证，不能代替本地完整 binding。当前 API 无 caller parameters，`?`/named parameter 首发拒绝。每个目标 patch version 必须通过 server success/failure、歧义、table set 与本地 binding 的 differential suite。

## 6. 锁、事务、MySQL watcher 与全局顺序

### 6.1 唯一锁序

全局偏序是：

```text
control policy/fence lock → business connection/transaction/schema lock
```

请求使用 control-shared → business。metadata mutation 使用 control-exclusive → business。任何代码不得在持有 business lock/transaction 时再请求 control policy/fence lock。

enrollment、staging bind 与 `*` 展开可用以下两阶段降低 control-exclusive 持有时间：

1. discovery phase：可单独读取 control 候选或单独取得 business 候选，但两者不同时持有；记录的任何 closure/fingerprint 都是不可信 hint；释放全部锁/事务。
2. commit phase：取得 control-exclusive/父行锁并验证 ETag/revision/fence；随后取得 business lock；从原始 token/SQL 完整重做 bind、closure、implicit scan、fingerprint、DDL waterline/generation；保持 business lock到 metadata CAS提交，然后释放。不得用 phase 1 digest 代替任何一次重算。

这既消除请求与 mutation 的跨库死锁环，也保留最终 CAS 的 schema 稳定性。

### 6.2 PostgreSQL 请求事务

候选事务和执行事务均为 read-only；statement/lock timeout 是总 deadline 的子预算。执行事务按第 5.2 节锁、bind、Fpre、authorize、SELECT、读完结果、mask/encode、Fpost、audit，最后 commit 或 rollback。B2 不执行任何用户 DML。

### 6.3 MySQL 请求事务与 watcher

条件性 MySQL 路径固定为：

```text
control shared snapshot
  → exclusive physical connection
  → LOCK INSTANCE FOR BACKUP
  → START TRANSACTION READ ONLY
  → read Fpre target waterline
  → wait watcher >= Fpre, verify continuity/generation, scan/bind/Fpre
  → authorize/SELECT/read/mask/encode
  → scan Fpost and read Fpost target waterline
  → wait watcher >= Fpost, verify continuity/generation/Fpre==Fpost
  → audit/final fence/read-only COMMIT or ROLLBACK
  → UNLOCK INSTANCE
```

watcher checkpoint/generation 是由 binlog GTID 驱动的事实源；gateway intent 只降延迟、不能作为事实序列。DDL 与自建 epoch 表不可做 SQL 原子提交，v4 不再设计该机制。

受控 DDL gateway 同样遵守 control-exclusive → business：取得control exclusive并登记intent后执行DDL，读取DDL后的target waterline，等待独立watcher确认并写入event-derived generation，再清intent并释放control lock。crash或超时留下pending intent并freeze。即使gateway失效或发生带外DDL，请求也必须依靠backup lock + 当前target waterline + watcher事实序列检测，不能把gateway intent/CAS当安全依据。

watcher progress 使用独立、线性化、单调的 progress channel/store，不参与并会被 policy shared fence lock 阻塞的 MVCC snapshot；请求可在保持 policy snapshot 时读取更新后的 checkpoint。checkpoint 包含 server UUID、binlog file/end position、seen GTID set、continuity status、event-derived generation。倒退、gap、PURGE/RESET、UUID/restore变化立即 freeze。

backup lock 阻止 S0 已验证的持久对象 DDL；Fpost 指定水位并等待 watcher追过，关闭“Fpost 后 watcher 尚未看到变化”的窗口。temporary object 不受 backup lock 保护，因此始终拒绝。每个新 MySQL patch version 先复跑 DDL matrix，未覆盖的 DDL 不能外推为安全。

## 7. `AuthorizedExecute` 线性流程、审计与错误语义

1. 校验 raw size、pre-parse token/depth与并发 quota；开启 `ControlSnapshotTx`，同一 snapshot 读取并验证 reader/writer fence、policy/binding/enrollment/mask revision与 datasource capability。
2. façade parse并分类；DML移交既有表级/profile私有路径并退出本流程。本B2 SELECT分支若不是单条SELECT、命中静态profile deny或datasource unsupported，则不接触business DB并写固定deny audit。
3. 按方言执行候选 bind/执行事务锁流程；`Fpre` 必须等于 healthy enrollment。
4. authorizer 产生唯一 immutable ProtectionPlan；缺 usage、mask meet或 key/algorithm capability则不执行。
5. 执行只读 SELECT；低层 bounded reader 收满完整允许结果。任何错误/超限取消并丢弃。
6. 对每个最终输出位置按 plan 至多 mask 一次，写入 post-mask bounded buffer；再由最终 codec/transport transform 写入 encoded bounded buffer。
7. 仍持 business lock 时做 `Fpost`；PG 要求 fingerprint/closure等于 Fpre/enrollment；MySQL 还读取 Fpost waterline并等待 watcher追过后验证 continuity/generation与 fingerprint。
8. 在原 `ControlSnapshotTx` 中复核 reader/writer fence、全部 revision和控制面可达性。
9. 写 durable authorization audit barrier。审计存储不可用、超时或不能确认 durable，fail-closed：rollback/丢弃 sealed candidate，不返回数据。审计记录“授权且完成封印/待交付”，不虚构网络已送达。
10. 在原 control snapshot 做最后 no-op/revision read；结束 business read-only事务；生成一次性 delivery seal；结束 control transaction。
11. transport 只接收 `SealedAuthorizedResponse`。网络写失败不重执行 SELECT；可另记 best-effort delivery outcome，但它不影响已经满足的“未审计不交付”。

由于没有业务写入，本流程不需要也不宣称跨库 2PC。若未来 B2 DML 立项，必须另写设计和真库矩阵，不能复用本节措辞宣称原子提交。

approval 只保存 agent/datasource、raw SQL SHA-256、statement class、申请时 revision与 expiry。批准后执行必须用相同 raw SQL重新走以上全部步骤；approval 不是授权 proof。

### 7.1 固定诊断映射

授权前只返回稳定 auth reason；进入 DB 后的 syntax/semantic/data/resource/driver错误一律对外为 `EXECUTION_FAILED`。DB message、SQLSTATE/errno、DETAIL、HINT、CONTEXT、NOTICE、WARNING、schema/table/column/constraint/routine名、command tag、affected/matched/changed rows、last insert id全部不得进入 response。

pgx notice handler只计数并丢弃；MySQL 不执行 `SHOW WARNINGS/ERRORS`，关闭 multiStatements/local infile。内部受保护审计最多记 driver code、stage和 request ID，不记 server text、predicate literal或数据值。

## 8. Mask、response seal 与字节性质

### 8.1 identity-only meet 与单次执行

最终输出位置汇总所有 set arm、view层和匹配规则：deny 与任意项 meet 为 deny；allow 与 mask 为 mask；allow 与 allow为 allow；mask 只有 `(algorithm_id, semantic_version, key_id, key_version, canonical_parameters, input_type, output_type)` 全等才 meet为自身。不同算法、key/version或参数没有已证明 meet，返回 `AUTH_MASK_MEET_UNDEFINED`。

meet 阶段只合成 plan，不执行 mask。合成完成后，每个最终位置只调用一次确定的 mask transform；禁止逐层重复 mask、先按 arm mask再按 projection mask，或错误重试 mask。未来增加 mask 偏序必须有不可区分性/泄漏证明和独立评审。

### 8.2 封印性质

保证的是：**seal 前 transport 零字节；seal 后即使 socket 只写出部分内容，写出的也只能是已经授权、已经 mask、已经诊断净化并受最终 cap约束的 bytes。** 不承诺网络层原子写。

HTTP/MCP/admin handler 外层使用 transactional writer；`Header/WriteHeader/Write/Flush` 在 commit前只作用于私有 bounded buffer，SQL route 禁用 SSE、chunked progress、keepalive comment、early hints。stdio 先完整编码一个 bounded JSON-RPC frame再写。

post-mask cell/result 与最终 encoded envelope分别有独立上限。压缩、content framing、SDK wrapper和外层 middleware若会改变 bytes，必须禁用或纳入最终编码阶段和 cap；seal 后禁止任何可能重新引入未净化诊断或无界膨胀的 transform。

## 9. Fence、protocol 与 safe fallback

每次执行都在同一个 `ControlSnapshotTx` 中读取并验证 reader/writer fence，交付前在该 snapshot复核；不得使用 TTL/cache。control transaction断开、探活失败或 control plane不可达即拒绝 query/explain/approval execute并退出 ready。

metadata mutation 对相同 fence key取 exclusive lock。PostgreSQL metadata可使用 shared/exclusive advisory xact lock；SQLite 用 `BEGIN IMMEDIATE`，仅适合单实例/低并发。lease只用于运维可见性，不能让失联实例被忽略后继续服务。

protocol 3 capability只有当同一 binary/release artifact包含全部安全能力、启动自检与 datasource capability probe通过后才能声明。仅建表、parser或UI完成不得上报3。

safe fallback 必须在 activation 前构建、签名、部署并演练；仍然是 protocol 3，保留 fence、binder、catalog双扫描、锁、mask、audit、seal和choke point，只能关闭非安全UI/管理功能。asymmetric激活后不得回退 protocol 1/2；fallback失败则 freeze。

## 10. Metadata、迁移、revision、ETag 与 `*`

### 10.1 CSV staging 与 ordinal binding

combined 0009 / separate metadata 0008 只在 metadata transaction 中建 schema、约束、fence，并把 legacy CSV token原样写入不可授权 staging：

```text
policy_column_permission_staging
  policy_id, token_ordinal, legacy_token, requested_usage,
  source_csv_sha256, bind_status, error_code,
  PRIMARY KEY(policy_id, token_ordinal, requested_usage)

policy_column_permissions
  policy_id, relation_enrollment_id, column_ordinal, column_name,
  column_type_digest, usage, parent_revision,
  PRIMARY KEY(policy_id, relation_enrollment_id, column_ordinal, usage)
```

迁移在全局 migration/control lock 内 claim → preflight → DDL → CSV split/trim写 staging output+reference → count/hash校验 → protocol保持2 → commit。不得伪造 ordinal；有任一 staging row 的 datasource unhealthy。

binding worker 遵循第6.1节两阶段协议。commit phase 必须 control-exclusive → business lock，重新从 legacy token完整 bind、scan、fingerprint，然后以 policy revision + staging hash + enrollment revision/fingerprint + DDL waterline/generation 做 metadata CAS；一次性插入全部正式行、删除 staging、父 revision +1。失败整体 rollback，未完成持续 unhealthy。

### 10.2 父子 mutation 不可绕过

业务角色撤销对 policy、permission、binding、fence表的直接 DML。PostgreSQL 只授予固定 `search_path`、校验调用角色的 SECURITY DEFINER procedure EXECUTE；procedure在一事务锁父、检查 ETag/fence、替换子项、自动 bump父 revision。SQLite repository用单一 `BEGIN IMMEDIATE`等价实现，DB handle不导出。

防御 trigger保证任何子表 INSERT/UPDATE/DELETE 至少 bump父 revision；procedure用 transaction-local guard避免批量多 bump。direct-DML故障测试必须证明不能静默绕过。

### 10.3 强 ETag

只接受一个 canonical strong ETag：

1. `Header.Values("If-Match")` 恰有一个值；
2. HTTP栈完成标准header解析后，应用层取得的唯一值必须不经trim/规范化便逐字等于 `"policy-<canonical-base64url-id>-r<positive-decimal>"`；
3. wildcard `*`、`W/`、逗号/list、重复 header、空白变体、前导零、非 canonical base64url全部拒绝；
4. procedure只接收已解析 ID + revision，不接收原 header。

缺失返回428，非法400，revision过期412。`legacy_unrepresentable=true`时任何只带 legacy `columns` 的 PUT返回409，即使当前 usage碰巧对称；新旧字段同时出现但不逐项等价返回422。

### 10.4 `*`

所有新写入口拒绝 `*`。grandfathered `*`展开遵循两阶段锁序；最终 commit phase在 control-exclusive 后取得 business schema lock，从 catalog重新展开全部真实 ordinal/name/type digest，并保持锁到 metadata CAS提交。CAS比较 enrollment revision/fingerprint与DDL waterline/generation。失败不保存部分结果并保持 staging/needs_rebind。protocol3激活要求 staging=0且`*`=0。

## 11. Choke point 与语言级能力隔离

驱动、credential、DSN、任意 SQL string sink全部移入：

```text
internal/authorizedexecute/internal/businessdb/
  postgres/
  mysql/
```

Go 的嵌套 `internal` 规则使 `internal/authorizedexecute` 外的包无法导入这些能力。public façade只暴露 `AuthorizedExecute`、受限 health/schema typed ports与不能表达 caller SQL的固定操作。删除或私有化 `bootstrap.Runtime.ExecutorFor`；pipeline、controlledread、MCP schema、ping/demo不得保留 raw `Executor/Session/WriteTx`。

MySQL DDL-capable inspector也放在该嵌套能力域的独立子包/credential pool，只暴露参数化 catalog typed port；不能转换为通用 `Query(string)`。

CI 硬门：

- `go list -deps -json` import allowlist；只有嵌套 businessdb、metadata store所需实现能导入 database/sql、pgx、mysql driver；
- AST/SSA扫描 Query/Exec/Prepare/Raw/Conn/OpenDB/NewConnector和SQL string sink；
- 最终二进制符号/字符串 callsite manifest与签入 allowlist逐项匹配；
- credential类型不可导出为 interface；typed request不能表达任意 caller SQL；
- 所有入口负向语料证明经过同一门。

扫描是补充，不得替代嵌套 internal 的语言级隔离。审批后执行重新完整授权，不存在“批准即绕门”。

## 12. 资源预算、取消与总 deadline

默认上限可由部署收紧，不得放宽；计数使用 checked arithmetic，并在分配/扩展前检查：

| 资源 | 默认硬上限 |
|---|---:|
| raw SQL UTF-8 bytes | 256 KiB |
| caller参数 | 当前0；未来最多256个、单个64 KiB、合计1 MiB |
| pre-parse token / lexical nesting | 100,000 / 128 |
| AST node / query block | 50,000 / 256 |
| projection / 最终输出列 | 4,096 / 256 |
| relation / view depth / catalog round trip | 256 / 16 / 32 |
| definition SQL / binder JSON / raw catalog bytes | 1 MiB / 8 MiB / 8 MiB |
| catalog rows / column metadata | 50,000 / 16,384 |
| dependency edge / expanded path / work unit | 32,768 / 65,536 / 250,000 |
| DB protocol message/frame | 1 MiB |
| raw cell / row / result | 64 KiB / 1 MiB / 16 MiB |
| post-mask cell / result | 128 KiB / 16 MiB |
| final encoded envelope（含最终外层变换） | 20 MiB |
| statement execution wall | `min(datasource timeout, 5s)` |
| catalog+lock子预算 | `min(1s,max(200ms,statement deadline/4))` |
| **总持锁 wall** | `min(2s,max(500ms,request deadline/2))`，且是下述共同父 deadline |
| request总 wall | `min(10s, configured request deadline)` |
| in-flight agent / tenant / datasource | 4 / 32 / `min(conn_limit/2,16)` |
| MySQL backup-lock datasource | 2，另加10/s token bucket |

总持锁 wall从第一把 business schema/backup lock开始，覆盖 execute、全部读取、mask、最终编码、Fpost、MySQL watcher Fpost追水位、audit barrier和read-only commit/rollback/unlock；不是若干可相加的独立 timer。connection acquisition、候选分析和control snapshot仍受request总 wall。

work unit在 node visit、edge添加、view path复制、set-arm组合前递增。row limit只能按完整行正常截断并至多多读一行确认；任何 cell/byte/协议错误不得返回部分值或“截断成功”。

PG取消发送 CancelRequest，frame/同步性错误discard connection。MySQL取消使用同一 datasource account 的专用 auxiliary physical connection（或另账户具备 `CONNECTION_ADMIN`）对已验证十进制 `CONNECTION_ID()`执行 `KILL QUERY`；随后必须继续 drain到终止、检查 `Rows.Err()`、`Rows.Close()`、rollback/savepoint rollback。因为 `KILL QUERY`不结束事务，遗漏 rollback是安全错误。取消、driver/context/frame错误后一律关闭/discard target physical connection，不复用；辅助连接不可承载用户 SQL。

## 13. 审计记录

`column_auth.version=4` 至少记录 request/control/fence/policy/mask/enrollment revisions、dialect capability、binder capability/hash、plan digest、Fpre/Fpost digest、候选与实际锁集合digest、PG重试次数或MySQL Fpre/Fpost waterline/watcher checkpoint、预算计数、每个 bound use 的 identity/ordinal/site/view path与固定reason。

不得记录数据值、predicate literal、server message、DETAIL/NOTICE/WARNING、完整view SQL或inspector credential。明细最多256项/32 KiB；截断时保存完整 canonical digest并优先记录deny原因。

SELECT成功交付的必要条件是 pre-delivery authorization audit durable。网络交付结果可另记，但不得因无法写“delivered”审计而泄露未审计数据，也不得把 socket partial write描述成业务回滚问题。

## 14. 实现切片与同发布要求

S0 已完成并成为输入事实，不再把已实测否定的机制作为待验证选项。推荐顺序：`S1 → S2 → S3 → S4 → S5 → (S6 可延后) → S7 → S8`。全部保持 feature-off 到 activation。

### S1：scope、metadata、迁移、ETag 与 fence

内容：把 B2能力/API固定为SELECT-only；0009/0008 staging；typed migration lock/callback；父子procedure/role/trigger；strict ETag；legacy 409；每请求同snapshot fence；两阶段 enrollment/bind/star 锁序。

验收：DML仍可按既有表级/profile规则工作但永不进入列授权、审计也不得标记B2列保护；迁移故障点原子回滚；CSV不产生假ordinal；direct child DML自动bump；反序锁测试无环；控制面失联每请求拒绝；ETag恶意语料全过。

### S2：能力隔离、预算与 sealed transport 基座

内容：创建嵌套 `internal/businessdb`；收回raw handles；typed ports；pre-parse/work/cell/frame caps；共同父deadline；transactional writer；固定错误mapper；MySQL auxiliary cancel适配器可先feature-off。

验收：编译期非法import失败；CI callsite manifest；任意入口不能取得SQL sink；seal前抓包0 bytes；seal后只出现净化bytes；资源到限无部分结果；MySQL取消drain/rollback/discard连接测试。

### S3：PostgreSQL binder、catalog、锁与 enrollment

内容：PG14–18 extension；raw no-execute preflight；候选/执行两事务；OID排序与逐OID核验；base/view typed lineage；S0修正隐式对象/FK检测；Fpre/Fpost canonical encoder。matview作为独立可选子切片S3m。

验收：真实OID/type/cast/operator golden；base/self-join/多层view；closure外 relation必拒绝；双向锁集合核对；每类隐式对象存在即拒绝；catalog权限不足不等于空；PG14/18及所有声明major通过。

### S3m（可选）：PostgreSQL matview

内容：按major typed `_RETURN.ev_action` walker；逐output lineage与全reference set；recursive definition closure；relkind专用OID排序取锁。

验收：S0多层matview fixture转为production golden；`LOCK TABLE matview`失败路径不会被使用；缺walker/hash/锁能力稳定拒绝。未完成不阻塞base/view GA。

### S4：authorizer、mask 与 SELECT-only AuthorizedExecute

内容：identity grant index；output/reference sites；view逐层ACL；DISTINCT/set规则；identity-only mask meet；final-position single mask；只读execute/Fpost/audit/final fence/delivery seal；approval重授权。

验收：授权矩阵与mask全格；不同algorithm/key/params拒绝；每个位置mask调用计数恰为0或1；audit/fence/Fpost/encode失败均0 bytes；无DML port或DML capability位。

### S5：全入口集成、审计与 PostgreSQL GA gate

内容：MCP/HTTP/stdio/admin/demo/query/explain/approval统一choke point；固定诊断；post-mask/encoded caps；runtime capability/self-test；PG生产压测和safe observability。

验收：跨入口同SQL同reason/DB-stage；NOTICE/WARNING/tag/rowcount不外泄；SDK/中间件flush攻击0提前字节；锁/总deadline/并发quota低层强制；S1–S5同一artifact。

### S6（条件性、可延后）：MySQL B2

内容：lctn 0/1；single-root-scan catalog；isolated DDL-capable inspector；closed binder；backup lock；GTID watcher事实generation；Fpre/Fpost指定水位；EVENT/temporary拒绝；inbound packet cap与aux cancel。

验收：MySQL目标每个patch重跑S0 matrix；1137 regression；Unicode/canonical collision；metadata正向权限fixture；watcher lag/gap/PURGE/RESET/UUID/restore freeze；Fpost追水位；M1签收。任一失败只允许MySQL B2整体unsupported。

### S7：安全总验收

内容：完整逃逸语料、故障注入、并发DDL、升级/网络分区、DoS、migration路径、二进制能力审计。

验收：第15节P0处置全部达到声明状态；S0证据与目标版本匹配；PG-only或PG+MySQL的实际capability与文档一致；M2/M3和适用的M1签收完成。

### S8：fallback、性能与 activation

内容：protocol3 safe fallback构建/签名/演练；真实部署SLO与lock争用压测；dry-run deny报告；activation transaction和告警。

验收：同发布安全集合齐全；staging=`*`=0；enrollment healthy；若启用MySQL则watcher/inspector/威胁模型全绿；fallback演练后才激活asymmetric。

### 必须同发布的集合

PG B2 GA的 S1、S2、S3、S4、S5安全部分必须由同一发布者流程构建、签名并出现在同一 protocol3 artifact/activation gate；缺一只能feature-off。若首发纳入matview，S3m同属该集合。若首发纳入MySQL，S6全部安全能力也必须进入同一artifact；不得把watcher、inspector或single-root catalog作为“稍后补齐”的外部条件。

## 15. v3 NO-GO / P0-1～P0-15 逐项处置

“设计关闭”表示v4给出可实现机制与验收，不代表代码已完成。

| P0 | v4 处置 | 定稿状态 |
|---|---|---|
| P0-1 | 双方言FK按referencing/referenced双向枚举；internal FK trigger不被用户trigger过滤掩盖；所有隐式对象首发存在即拒绝；MySQL EVENT按root schema拒绝 | 设计关闭，待S3/S6验收 |
| P0-2 | 保留DISTINCT/set各arm output+reference双usage；MySQL未有golden可先只开UNION ALL | 规则关闭 |
| P0-3 | caller只交raw SQL；proof私有seal；PG真实analyzed OID且候选/执行两事务；MySQL封闭binder，不可证明即拒绝 | 设计关闭 |
| P0-4 | 普通view typed lineage；matview明确要求typed `_RETURN.ev_action` walker、逐列lineage、定义闭包和专用锁，否则整体拒绝 | 设计关闭；matview为可选签收项 |
| P0-5 | PG以OID/attnum；MySQL generation事实源改为binlog watcher而非不可能的DDL+epoch SQL原子；完整同构带外ABA只保留为收窄M1 | 可观察部分设计关闭；原理边界待签收 |
| P0-6 | PG候选只读事务结束后，执行事务按OID/relkind锁并核对OID，锁内重bind，实际闭包外relation回滚限次重试；MySQL backup lock+client deadline+Fpost watcher水位 | 设计关闭，待版本矩阵验收 |
| P0-7 | 授权只信请求内锁定Fpre/Fpost，不用跨请求catalog snapshot | 关闭 |
| P0-8 | alias/ordinal先真实bind，再深复制reference依赖；方言差分不一致即移出支持矩阵 | 关闭 |
| P0-9 | identity-only meet；最终位置只mask一次；全部诊断/tag/rowcount固定映射；post-mask/final cap；性质改为seal前0 bytes、seal后仅净化bytes | 设计关闭 |
| P0-10 | 全局control→business；mutation两阶段最终全量重做；SELECT-only撤回DML commit承诺；每请求同snapshot final fence | 设计关闭 |
| P0-11 | 单个canonical strong ETag；拒wildcard/weak/list/repeat；legacy-unrepresentable PUT=409；`*`最终阶段持business锁到CAS且不反序 | 设计关闭 |
| P0-12 | MySQL Fpre/Fpost都读指定waterline并等待独立单调watcher；backup lock覆盖实测持久DDL；EVENT/temporary与gap异常拒绝；gateway非事实源 | 设计关闭；M1仅剩不可区分攻击边界 |
| P0-13 | staging保留token；bind最终阶段control-exclusive→business、全量重bind/fingerprint、metadata CAS；子表procedure/role/trigger自动bump | 设计关闭 |
| P0-14 | 任意SQL/driver/credential移入嵌套`internal/authorizedexecute/internal/businessdb`，CI allowlist/SSA/binary仅为辅；approval后完整重授权 | 设计关闭 |
| P0-15 | 补pre-parse、参数、edge/path/work、cell、post-mask/final、共同总锁wall与并发；MySQL auxiliary `KILL QUERY`后drain/Rows.Err/Close/rollback并discard connection | 设计关闭 |

v3 NO-GO 的六个主阻断均有明确结果：入向FK已补；matview不再假定自然rewrite；PG锁模型改为两事务+relkind路径；MySQL补Fpost watcher水位且inspector条件不满足即整体unsupported；锁序统一；DML跨库原子承诺因SELECT-only被撤回。

## 16. 必须由负责人签收的清单

以下不是可由实现者自行默认接受的选项：

### 发布范围选择

- **GA 是否为 PG-only 列授权。** 默认建议“是”：PG base table +普通非递归view先GA，MySQL B2延后；MySQL继续表级规则。
- **PostgreSQL matview 是否进入首个GA。** 默认建议“不进入”；只有S3m全部验收且负责人愿意扩大支持面才开启。

### MySQL 专项

- **DDL-capable inspector credential。** 数据库安全负责人确认允许AgentSQL持有TRIGGER/EVENT可见且带相应DDL能力的隔离凭据，并签收密钥、私有连接池、不可达通用SQL、审计与轮换控制；不接受则MySQL B2 unsupported。
- **M1（收窄后的MySQL不可区分ABA）。** 只包含攻击者同时绕过gateway、绕过或伪造连续binlog/watcher，并恢复server UUID、GTID/position、catalog definition、generation等全部外部证据的完全同构状态。普通watcher延迟、权限不可见、EVENT漏扫、Fpost未追水位或未覆盖DDL不属于M1，必须技术关闭。数据库安全负责人、AgentSQL产品安全负责人、业务data owner三方签收DBA可信、禁止带外DDL/`sql_log_bin=0`/RESET/PURGE、restore/failover强制全量re-enroll；不签收则MySQL B2 unsupported。

### 通用残留边界

- **M2：可信基础设施。** 双方言信任DB server binary/system catalog、PG扩展部署链、TLS端点和AgentSQL密钥边界；要求artifact签名/hash attestation、最小权限与职责分离。基础设施安全负责人签收。
- **M3：可用性边界。** 短时PG relation lock/MySQL backup lock可能被滥用造成DoS；低层共同deadline、quota和datasource隔离限制放大，但不保证恶意DBA/数据库故障下可用性。SRE owner签收容量、报警和kill/freeze runbook。

所有签收附入release record并指向准确artifact、数据库patch版本和S0/S7证据；不能用一次签收永久覆盖后续版本升级。

## 17. 发布与实现起点

现在可开始：S1；S2；S3的PG base/view主线；S3纯matview spike可并行但不阻塞；S4 authorizer/mask纯逻辑；transactional writer与frame-cap适配。MySQL S6默认可延后，只有负责人先接受credential与威胁模型方向后才值得进入完整产品实现。

生产放行前必须证明：每种隐式对象在enrollment/Fpre/Fpost都可见且拒绝；PG所有声明major的binder和锁闭包；每个入口同因同reason且seal前0 bytes；control网络分区立即fail-closed；mask/encoded/audit/Fpost/fence失败无数据交付；migration与direct child DML故障测试；safe fallback activation前演练。

本设计的推荐首个可交付物是：**PG-only、base table +普通非递归view、SELECT-only列授权；matview关闭；MySQL B2标记unsupported。** 这是S0证据支持面最清楚、无需依赖额外组织风险接受的GA切片。

## 18. v4.1 errata：P0-A…P0-E 关闭决策

本节覆盖前文与下列决定冲突之处。S1 只落 metadata/configuration 与协议骨架；`internal/authorizedexecute` 当前尚不存在，A～D 的执行期实现和真库证明属于 S3/S4。

### P0-A：授权树与执行树必须是同一棵树

PostgreSQL 固定为同一只读事务、同一 physical backend 上的随机命名 server-side prepared statement/portal：`internal/authorizedexecute/internal/businessdb/postgres` 的 `PrepareBoundSelect` 调用 `agentsql_binder_prepare(name, raw_sql)`，扩展在 `PREPARE` 的 parse/analyze/rewrite hook 保存该 backend、事务和 statement name 对应的 analyzed tree、依赖闭包、role、`search_path`、catalog generation 与 digest；`internal/authorizedexecute/postgres_select.go` 的 `authorizePrepared` 只消费这个 opaque handle，`executePrepared` 只允许 `EXECUTE` 同名 portal。扩展在 plan/execute hook 发现 relcache/syscache invalidation、重新 parse/analyze/rewrite、generic/custom replan、依赖变化、role 或 `search_path` 变化时返回固定 fail-closed 错误并丢弃连接。未限定 relation 只认 analyzed RTE 的 catalog OID，不再按执行前名称重解析。落地与 PG14～18 hook 行为标为**待 S3 实证**。

检测/固定 SQL 形状（identifier 只能由内部随机名安全 quote；调用方不能提供）：

```sql
BEGIN TRANSACTION ISOLATION LEVEL REPEATABLE READ READ ONLY;
SELECT current_user, session_user, current_setting('search_path'),
       pg_catalog.pg_my_temp_schema(), pg_catalog.txid_current_if_assigned();
PREPARE "<internal-random-name>" AS <raw SELECT without caller PREPARE/EXECUTE>;
SELECT * FROM agentsql_catalog.prepared_manifest('<internal-random-name>');
-- authorize exactly the returned analyzed-tree digest and dependency closure
EXECUTE "<internal-random-name>";
SELECT * FROM agentsql_catalog.prepared_manifest('<internal-random-name>');
-- require identical digest/dependencies/role/search_path and replan_count=0
DEALLOCATE "<internal-random-name>";
COMMIT;
```

### P0-B：规划器隐式对象存在即拒绝

`internal/authorizedexecute/internal/businessdb/postgres/catalog.go` 的 `ScanImplicitObjects` 与 enrollment、Fpre、Fpost 共用同一查询和 canonical encoder；`internal/authorizedexecute/postgres_enrollment.go` 的 `Enroll` 及 `postgres_select.go` 的 `verifyFingerprint` 对任一 finding 返回 `AUTH_IMPLICIT_OBJECT_UNSUPPORTED`。AM/operator-class-family/operator/support/type I/O/collation 均按每个受支持 PG major 的 capability manifest 使用**精确 OID allowlist**，不是 namespace/name allowlist。

```sql
WITH target(relid) AS (SELECT unnest($1::oid[])), idx AS (
  SELECT i.indexrelid,i.indrelid,i.indclass
  FROM pg_catalog.pg_index i JOIN target t ON t.relid=i.indrelid
), findings AS (
  SELECT c.oid owner_oid,'relation_am'::text kind,c.relam object_oid
  FROM pg_catalog.pg_class c JOIN target t ON t.relid=c.oid
  WHERE c.relam<>0 AND NOT (c.relam=ANY($2::oid[]))
  UNION ALL
  SELECT x.indrelid,'index_am',ic.relam FROM idx x
  JOIN pg_catalog.pg_class ic ON ic.oid=x.indexrelid
  WHERE NOT (ic.relam=ANY($3::oid[]))
  UNION ALL
  SELECT x.indrelid,'opclass',oc.oid FROM idx x
  CROSS JOIN LATERAL unnest(x.indclass::oid[]) v(opclass_oid)
  JOIN pg_catalog.pg_opclass oc ON oc.oid=v.opclass_oid
  WHERE NOT (oc.oid=ANY($4::oid[]))
  UNION ALL
  SELECT x.indrelid,'opfamily',oc.opcfamily FROM idx x
  CROSS JOIN LATERAL unnest(x.indclass::oid[]) v(opclass_oid)
  JOIN pg_catalog.pg_opclass oc ON oc.oid=v.opclass_oid
  WHERE NOT (oc.opcfamily=ANY($5::oid[]))
  UNION ALL
  SELECT x.indrelid,'amop',o.oid FROM idx x
  CROSS JOIN LATERAL unnest(x.indclass::oid[]) v(opclass_oid)
  JOIN pg_catalog.pg_opclass oc ON oc.oid=v.opclass_oid
  JOIN pg_catalog.pg_amop o ON o.amopfamily=oc.opcfamily
  WHERE NOT (o.oid=ANY($6::oid[]))
  UNION ALL
  SELECT x.indrelid,'amproc',p.oid FROM idx x
  CROSS JOIN LATERAL unnest(x.indclass::oid[]) v(opclass_oid)
  JOIN pg_catalog.pg_opclass oc ON oc.oid=v.opclass_oid
  JOIN pg_catalog.pg_amproc p ON p.amprocfamily=oc.opcfamily
  WHERE NOT (p.oid=ANY($7::oid[]))
  UNION ALL
  SELECT 0,'type_io',f FROM pg_catalog.pg_type t
  CROSS JOIN LATERAL unnest(ARRAY[t.typinput,t.typoutput,t.typreceive,t.typsend]) f
  WHERE t.oid=ANY($8::oid[]) AND f<>0 AND NOT (f=ANY($9::oid[]))
  UNION ALL
  SELECT 0,'collation',c.oid FROM pg_catalog.pg_collation c
  WHERE c.oid=ANY($10::oid[])
    AND (c.collprovider NOT IN ('c','d') OR NOT (c.oid=ANY($11::oid[])))
  UNION ALL
  SELECT 0,'aggregate_support',f FROM pg_catalog.pg_aggregate a
  CROSS JOIN LATERAL unnest(ARRAY[a.aggtransfn,a.aggfinalfn,a.aggcombinefn,
    a.aggserialfn,a.aggdeserialfn,a.aggmtransfn,a.aggminvtransfn,a.aggmfinalfn]) f
  WHERE a.aggfnoid=ANY($12::oid[]) AND f<>0 AND NOT (f=ANY($13::oid[]))
  UNION ALL
  SELECT 0,'window_support',p.prosupport FROM pg_catalog.pg_proc p
  WHERE p.oid=ANY($14::oid[]) AND p.prosupport<>0
    AND NOT (p.prosupport=ANY($15::oid[]))
)
SELECT owner_oid,kind,object_oid FROM findings ORDER BY 1,2,3;
```

准确的 planner-used amop/amproc 收敛集合及各 major OID manifest 为**待 S3 实证**；无法证明精确集合即拒绝。

### P0-C：relation shape gate 不因 `ONLY` 豁免

`ScanRelationShapes` 与 `ScanImplicitObjects` 同落 `catalog.go`，由 enrollment/Fpre/Fpost 调用。binder 的 `RTE.inh=false` 或 SQL `ONLY` 不跳过 catalog gate；任一方向 inheritance、partition parent/child、typed table 或非允许 relation AM 都拒绝。

```sql
WITH target(relid) AS (SELECT unnest($1::oid[]))
SELECT c.oid,'relispartition',c.oid FROM pg_catalog.pg_class c JOIN target t ON t.relid=c.oid WHERE c.relispartition
UNION ALL SELECT p.partrelid,'partitioned_table',p.partrelid FROM pg_catalog.pg_partitioned_table p JOIN target t ON t.relid=p.partrelid
UNION ALL SELECT i.inhrelid,'inherits_parent',i.inhparent FROM pg_catalog.pg_inherits i JOIN target t ON t.relid=i.inhrelid
UNION ALL SELECT i.inhparent,'inherits_child',i.inhrelid FROM pg_catalog.pg_inherits i JOIN target t ON t.relid=i.inhparent
UNION ALL SELECT c.oid,'typed_table',c.reloftype FROM pg_catalog.pg_class c JOIN target t ON t.relid=c.oid WHERE c.reloftype<>0
UNION ALL SELECT c.oid,'relation_am',c.relam FROM pg_catalog.pg_class c JOIN target t ON t.relid=c.oid
  WHERE c.relam<>0 AND NOT (c.relam=ANY($2::oid[]))
ORDER BY 1,2,3;
```

### P0-D：拒绝 whole-row/system/composite，并补全 join contributor lineage

`agentsql_binder` 的 typed walker 与 `internal/authorizedexecute/postgres_lineage.go` 的 `BuildLineage` 对 `Var.varattno=0`（`SELECT t`、`count(t)` 等 whole-row）、`varattno<0`（`ctid/xmin/tableoid` 等系统列）、以及任一 expression/result 的 `typtype='c'`、`record`/anonymous composite 立即返回 `AUTH_COLUMN_SHAPE_UNSUPPORTED`。`USING`/`NATURAL` 的 merged/coalesced 输出必须列出左右两侧（外连接同样如此）全部 base/view contributor，缺一即拒绝，不能只采用可见别名一侧。

```sql
SELECT t.oid,t.typtype,t.typrelid FROM pg_catalog.pg_type t
WHERE t.oid=ANY($1::oid[])
  AND (t.typtype='c' OR t.oid='pg_catalog.record'::pg_catalog.regtype);
SELECT * FROM agentsql_catalog.prepared_vars('<internal-random-name>')
WHERE attnum<=0 OR is_whole_row OR result_is_composite OR contributor_complete IS NOT TRUE;
```

第二条是 typed extension API，SQL 文本扫描不算证明；PG14～18 node coverage、`JOIN USING` alias/coalesce 形态和 dropped-column 行为为**待 S3 实证**。

### P0-E：结束顺序与无反边证明

`internal/authorizedexecute/select.go` 的 `AuthorizedSelect` 顺序冻结为：持 business schema lock 完成 execute/read/mask/encode/Fpost → 结束只读 business transaction（commit/rollback并释放锁）→ `internal/audit` durable authorization barrier → 原 `ControlSnapshotTx` final fence/no-op revision read → seal → 结束 control transaction → transport。audit/fence/seal 失败丢弃全部 buffered bytes。这样 durable audit 和 final control fence 都发生在 business transaction 结束之后，代码中不存在 business→control 的同时持锁反边；S1 的 `internal/store/fence.go` 只提供 control snapshot/exclusive mutation骨架。

```sql
SELECT locktype,mode,granted,relation,classid,objid
FROM pg_catalog.pg_locks WHERE pid=$1 ORDER BY locktype,relation,classid,objid,mode;
SELECT pg_catalog.pg_advisory_xact_lock_shared($1,$2); -- control reader，事务开始时取得
SELECT pg_catalog.pg_advisory_xact_lock($1,$2);        -- metadata writer，只能在未持 business tx 时取得
```

静态锁序测试给 `ControlSnapshotTx`/`BusinessTx` 加 runtime lock-rank token：rank 10 control、rank 20 business；取得低 rank 时若请求仍持高 rank 立即 test fail。enrollment/binding/star discovery 不持双锁，commit phase只允许 control→business；业务事务结束后才执行 audit/final fence，因此没有 business→control 边。

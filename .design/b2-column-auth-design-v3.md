# AgentSQL v0.4 B2 多表 JOIN / 自连接列级授权——权威设计 v3

状态：**可实施安全基线；默认 feature-off；S0 及发布闸门全部通过、威胁模型残留签收后才可激活**  
日期：2026-09-24  
工作目录：`D:\ruanjiansheji\agentsql-v04`  
范围：本文件只定义设计、迁移、能力验证和验收，不包含产品代码变更。

## 0. v3 裁决

v3 撤回 v2“15 项均已关闭”的声明。新的结论是：

1. P0-2、P0-7、P0-8 的规则保持不变；其余复核项按本设计补齐。
2. PostgreSQL 的表达式真实身份不再由 raw parser 猜测：protocol 3 要求安装按 PG 14–18 主版本构建、签名和能力探测的 `agentsql_binder` 扩展。扩展只 parse/analyze/rewrite、不 plan、不 execute，并返回分析后 relation/type/collation/function/operator/cast OID。扩展缺失或版本/hash 不符，datasource `unhealthy`。
3. MySQL 没有等价公开 analyzed-tree API。首发只开放第 3 节列出的、能由封闭语义 binder 精确复现的子集；所有函数调用、UDF/存储函数、隐式跨类型转换和无法证明的表达式稳定拒绝。
4. trigger、default、generated、RLS、rule、INSTEAD OF trigger、CHECK/FK/排斥约束、表达式/部分索引、MySQL `ON UPDATE` 等采用“enrollment + 每请求锁内 `Fpre/Fpost` 双扫描”。首发未显式支持的对象只要存在于执行依赖闭包即拒绝，不等到它实际触发。
5. 递归 CTE 和递归视图首发全部拒绝。不存在“深度内就放行”的例外。
6. PostgreSQL 以 OID/attnum 和完整 relation 锁闭包关闭可观察 TOCTOU/ABA。MySQL 以 backup lock、受控 DDL gateway、对象 generation、连续 binlog watcher 和 catalog 双扫描关闭所有可观察变化。
7. MySQL 管理员在两个观察点之间用带外 DDL 完成“同名、同 kind、同定义、同列”的 drop/recreate，或恢复一份能同时伪造 gateway generation/binlog continuity 的实例，在 MySQL 8.0 公开接口下不可区分。它不是自动继承语义，而是第 16 节要求负责人书面签收的窄威胁模型边界；不接受该边界的 datasource 不支持 MySQL B2。
8. 安全发布单元不是 v2 的 S4+S5，而是 S1–S6 的安全部分整体发布；protocol 3 只在这些能力全部存在且 S0/S7 通过后上报。

## 1. 真实仓库与能力探查结论

本轮只读探查确认：

- `internal/pipeline/types.go` 的 `Request` 只有 SQL 字符串，没有 bind 参数；`Response` 直接携带 `QueryResult`。
- `internal/executor/executor.go` 的 `Executor/Session/WriteTx` 只有通用 `Query/Execute/Explain`，没有 catalog、schema lock、只读事务、savepoint、postcheck 或 sealed result 端口。
- PostgreSQL 使用 pgx v5.9.2；其 `pgproto3.Frontend.SetMaxBodyLen` 可在收包分配前限制消息体，但当前未设置。
- MySQL 使用 go-sql-driver/mysql v1.9.3。其 `maxAllowedPacket` 主要限制客户端写包；当前读包路径可先拼接大包，不能满足逐值分配前上限，必须加受维护的 inbound packet cap 或换成具备该能力的适配器。
- `internal/executor/rows.go::collectRows` 在驱动完整解码后才 stringify，只有限行，无单元格、raw、post-mask 或 encoded 上限。
- `internal/parser/postgres.go` 使用 `pg_query_go/v5` raw tree，不能给出 overload/implicit cast 后的 OID；`internal/parser/mysql.go` 使用 Vitess 8.0.30 语义选项，实施时须升到并测试 8.0.31。
- `internal/pipeline/pipeline.go` 仅在调用方给 `SessionID` 时获取物理连接；执行后才选 redactor，且仍有 legacy fallback。
- `internal/mcpserver/http.go` 把 SDK handler 直接接到 `http.ResponseWriter`；不能证明 SDK/中间件在封印前没有 flush。必须在 SDK 外再加有界 transactional writer。
- `internal/store/migrate.go::applyMigrationWithOptions` 只有固定布尔 preflight，尚无 typed callback/显式全局迁移锁；`policy_repository.go` 父项 CRUD 也不是父子 CAS。
- `internal/store/sqlite_pg_migrate.go` manifest 目前只有 `policies`，B2 表须按 FK 顺序加入。
- 生产 raw executor 暴露仍存在于 `bootstrap.Runtime.ExecutorFor`、pipeline、`controlledread.Service`、MCP schema 和 datasource ping。
- 当前环境没有 Docker 或目标数据库连接，因此本文件中的 catalog/锁 SQL是实现契约，不冒充真库验证结果；S0 必须在 PG 14–18、MySQL 8.0.31 和目标最新 8.0 上生成证据包。

相关官方能力依据：PostgreSQL `pg_rewrite.ev_action` 是已分析 Query 的 `nodeToString()` 表示，`pg_depend` 提供对象/列依赖；MySQL backup lock 阻止非临时对象文件创建、重命名和删除但允许临时表 DDL；MySQL binary log 记录包括 view/trigger/function 在内的 DDL。实现不得把这些文档陈述替代 S0 并发实测。

## 2. 冻结的安全不变量

### 2.1 唯一可信输入与不可伪造证明

`AuthorizedExecute` 只接受 caller identity、datasource ID、原始 SQL 和内部批准票据；当前版本不接受调用方传入 AST、lineage、catalog snapshot、source-free proof 或 protection plan。解析、binding、lineage 和 plan 全部在授权包内部重建。

新增类型放在 `internal/authorizedexecute/internalmodel`，不放在可由业务包构造的 `internal/model` 公共面。binder 返回的 `BoundProgram` 持有不可导出的 `binderSeal [32]byte`；authorizer 只接受同请求 binder 实例签发、SQL hash/catalog digest/control revision 均匹配的对象。序列化或复制后 seal 无效。

### 2.2 每请求线性化点

每次执行必须同时持有：

1. 控制面 fence transaction/lock；
2. 一个请求独占的业务数据库物理连接；
3. 业务数据库可回滚事务或已有事务内 savepoint；
4. 完整执行依赖闭包的 schema 稳定锁；
5. 锁内 catalog `Fpre`、执行后的 `Fpost`；
6. 一个不可变 ProtectionPlan；
7. 一个尚未向 transport 写入任何字节的 sealed response buffer。

任一能力缺失、控制面不可达、revision/fence/catalog 不一致、结果或编码超限，均 cancel + rollback/savepoint rollback + 丢弃全部 buffer。

### 2.3 策略优先级

优先级固定为：Agent/profile/语句类别 deny → 任一表策略 deny → 缺表 allow → enrollment/catalog/执行图不可证明 → 缺 column usage grant → mask meet 不存在/能力缺失 → allow。mask 只处理已经拥有 output grant 的最终值，永远不能补 reference grant。

## 3. 首发支持矩阵与稳定拒绝矩阵

### 3.1 双方言共同支持

在依赖闭包内所有 relation 已 enrollment、隐式对象扫描为干净、binder 完整的前提下：

| 类别 | 首发支持 |
|---|---|
| SELECT | 普通 SELECT；INNER/LEFT/RIGHT/CROSS JOIN；self-JOIN；非递归 CTE；derived/scalar/correlated subquery；双方言实际支持的 LATERAL；WHERE/ON/USING；普通 GROUP/HAVING/ORDER；窗口 partition/order；LIMIT/OFFSET；star/NATURAL 经锁内 catalog 展开 |
| 集合 | `UNION ALL`；`UNION`、`INTERSECT`、`EXCEPT` 按 output+reference 双授权；PG `DISTINCT ON`；普通 DISTINCT |
| DML | 仅 enrollment 为 `plain_dml` 的 base table：显式列 INSERT VALUES/SELECT、UPDATE、DELETE；PG conflict/RETURNING；MySQL duplicate-key update。目标/来源依赖图必须完整 |
| source-free | literal、NULL、当前版本无 caller 参数、结构节点；`count(*)/count(constant)` 是单独的 `rowset_dependent` proof，仍授权 query block 全部 relation |
| view | 普通非递归 view，逐层 exposed ACL + 定义 lineage/base ACL；PG matview 同样做双层 ACL并显式锁定义依赖闭包 |
| 类型 | PG binder 证明的 pg_catalog built-in scalar type/signature；MySQL 第 5.2 节封闭类型/操作集合 |

### 3.2 无条件首发拒绝

| 对象/语法 | 稳定 reason |
|---|---|
| `WITH RECURSIVE`、binder 标记 recursive/self-reference、递归 view route | `AUTH_RECURSION_UNSUPPORTED` |
| PG whole-row Var、system column `attnum < 0`、inheritance/partitioned/partition child、foreign table、typed table | `AUTH_RELATION_SHAPE_UNSUPPORTED` |
| MySQL partitioned/NDB/FEDERATED/非 InnoDB base table、TEMPORARY table/view | `AUTH_RELATION_SHAPE_UNSUPPORTED` |
| SELECT locking clause `FOR UPDATE/SHARE/KEY SHARE/NO KEY UPDATE` 及 MySQL locking read | `AUTH_LOCKING_CLAUSE_UNSUPPORTED` |
| trigger、非 `_RETURN` rule、INSTEAD OF trigger、RLS、generated column、可执行 default、ON UPDATE、CHECK/FK/排斥约束、表达式/部分索引 | `AUTH_IMPLICIT_OBJECT_UNSUPPORTED` |
| table/SRF/JSON_TABLE、用户函数/operator/cast/UDF/loadable function、sequence、session/system-state function | `AUTH_OPAQUE_EXECUTABLE` |
| MySQL 任意函数调用（含看似 built-in）、显式 COLLATE/CAST、跨类型隐式转换；PG 非 allowlist OID | `AUTH_EXPRESSION_IDENTITY_UNSUPPORTED` |
| grouping sets/ROLLUP/CUBE、ordered-set/hypothetical aggregate/WITHIN GROUP | 对应 `AUTH_*_UNSUPPORTED` |
| 用户 EXPLAIN/ANALYZE、CTAS/SELECT INTO、COPY/LOAD/REPLACE、cursor、PREPARE/EXECUTE、CALL/DO、DDL/ADMIN、多语句 | `AUTH_WRAPPER_UNSUPPORTED` 或更具体 reason |
| 无法在接收分配前施加 frame cap 的 datasource adapter | `AUTH_RESULT_CAPABILITY_MISSING` |

MySQL 的首发函数拒绝是有意收紧：即使名字与 built-in 相同，也不靠名字猜测 UDF/存储函数解析。后续每开放一个函数，都必须给出服务器版本、arity、输入/输出类型、collation/coercion 规则及与 `performance_schema.user_defined_functions`、`INFORMATION_SCHEMA.ROUTINES` 的冲突证明。

### 3.3 DISTINCT、set op 与 alias

v2 规则不变：DISTINCT key、DISTINCT ON key、非 ALL UNION 的比较位置及 INTERSECT/EXCEPT（含 ALL）每个 arm 同时需要 output 与 reference；UNION ALL 只不额外增加 set reference。分支谓词、顶层排序仍各自需要 reference。

alias/ordinal 必须由真实 binder 先按服务端 name-resolution 绑定，再在新 clause site 深复制依赖并强制为 reference；不能用 projection 的 output 决策替代。双方言真库差分不一致即拒绝该形态。

## 4. Enrollment 与每请求隐式对象双扫描

### 4.1 状态机与控制面记录

新增：

```text
relation_enrollments
  id, datasource_id, dialect, server_id, database_id,
  schema_name, relation_name, relation_kind, stable_object_id,
  object_generation, catalog_fingerprint, implicit_scan_digest,
  binder_capability_digest, ddl_epoch, status,
  enrolled_at, revision

relation_enrollment_closure
  enrollment_id, path_ordinal, relation_identity, relation_fingerprint,
  PRIMARY KEY(enrollment_id, path_ordinal, relation_identity)
```

`status` 只有 `pending_scan|healthy|needs_rebind|unsupported|revoked`。enrollment 在业务库锁内生成候选，随后保持锁到 metadata CAS 提交；CAS 条件包括旧 enrollment revision、父 policy revision、catalog fingerprint、DDL epoch 和 staging digest。失败不留下可授权的半记录。

每请求对 binder 给出的完整 relation closure 执行完全相同的 `Fpre` 与 `Fpost` 扫描；结果必须：零 unsupported row、digest 等于 enrollment、对象身份/列/定义一致。任何新对象、消失对象、definition 改变或扫描权限不足均置 `needs_rebind` 并拒绝。`Fpost` 在锁仍持有、结果已读取但未 commit/交付时执行。

### 4.2 PostgreSQL 14–18 精确扫描

输入 `$1` 是 closure 中去重、稳定排序的 relation OID 数组。实现使用下列字段；hash 输入使用 catalog 原始 `pg_node_tree::text`、OID 数组和标志，不只使用 `pg_get_expr` 文本：

```sql
WITH target(relid) AS (
  SELECT unnest($1::oid[])
), findings AS (
  SELECT t.tgrelid AS relid, 'trigger'::text AS kind, t.oid AS object_oid,
         0::int AS sub_id,
         concat_ws('|', t.tgname, t.tgenabled, t.tgtype,
                   t.tgfoid::text, t.tgattr::text, t.tgqual::text) AS raw
  FROM pg_catalog.pg_trigger t JOIN target x ON x.relid=t.tgrelid
  WHERE NOT t.tgisinternal

  UNION ALL
  SELECT d.adrelid, CASE WHEN a.attgenerated <> '' THEN 'generated' ELSE 'default' END,
         d.oid, d.adnum,
         concat_ws('|', a.attgenerated, d.adbin::text,
                   pg_catalog.pg_get_expr(d.adbin,d.adrelid,true))
  FROM pg_catalog.pg_attrdef d
  JOIN pg_catalog.pg_attribute a ON a.attrelid=d.adrelid AND a.attnum=d.adnum
  JOIN target x ON x.relid=d.adrelid

  UNION ALL
  SELECT c.oid, 'rls', p.oid, 0,
         concat_ws('|', c.relrowsecurity, c.relforcerowsecurity,
                   p.polcmd, p.polpermissive, p.polroles::text,
                   p.polqual::text, p.polwithcheck::text)
  FROM pg_catalog.pg_class c JOIN target x ON x.relid=c.oid
  LEFT JOIN pg_catalog.pg_policy p ON p.polrelid=c.oid
  WHERE c.relrowsecurity OR c.relforcerowsecurity OR p.oid IS NOT NULL

  UNION ALL
  SELECT r.ev_class, 'rule', r.oid, 0,
         concat_ws('|', r.rulename, r.ev_type, r.ev_enabled,
                   r.is_instead, r.ev_qual::text, r.ev_action::text)
  FROM pg_catalog.pg_rewrite r
  JOIN pg_catalog.pg_class c ON c.oid=r.ev_class
  JOIN target x ON x.relid=r.ev_class
  WHERE NOT (r.rulename='_RETURN' AND c.relkind IN ('v','m') AND r.ev_type='1')

  UNION ALL
  SELECT q.conrelid, 'constraint:'||q.contype, q.oid, 0,
         concat_ws('|', q.conname, q.contype, q.condeferrable, q.condeferred,
                   q.convalidated, q.conkey::text, q.confrelid::text,
                   q.confkey::text, q.conbin::text, q.conexclop::text)
  FROM pg_catalog.pg_constraint q JOIN target x ON x.relid=q.conrelid
  WHERE q.contype IN ('c','f','x')

  UNION ALL
  SELECT i.indrelid, 'expression_or_partial_index', i.indexrelid, 0,
         concat_ws('|', i.indclass::text, i.indcollation::text,
                   i.indexprs::text, i.indpred::text)
  FROM pg_catalog.pg_index i JOIN target x ON x.relid=i.indrelid
  WHERE i.indexprs IS NOT NULL OR i.indpred IS NOT NULL

  UNION ALL
  SELECT x.relid, 'inheritance_or_partition', COALESCE(h.inhrelid,h.inhparent), 0,
         concat_ws('|', c.relkind, c.relispartition, h.inhrelid::text,
                   h.inhparent::text, h.inhseqno::text, h.inhdetachpending::text)
  FROM target x JOIN pg_catalog.pg_class c ON c.oid=x.relid
  LEFT JOIN pg_catalog.pg_inherits h ON h.inhrelid=x.relid OR h.inhparent=x.relid
  WHERE c.relkind IN ('p','f') OR c.relispartition OR h.inhrelid IS NOT NULL
)
SELECT relid, kind, object_oid, sub_id, raw
FROM findings
ORDER BY relid, kind, object_oid, sub_id;
```

补充 catalog fingerprint 必须同时读取：`pg_class.oid/relkind/relname/relnamespace/reltype/reloftype/reloptions/relispopulated`；`pg_attribute.attnum/attname/atttypid/atttypmod/attcollation/attnotnull/attidentity/attgenerated/attisdropped`；view/matview 唯一 `_RETURN` rule OID、`ev_action::text`；`pg_depend.classid/objid/objsubid/refclassid/refobjid/refobjsubid/deptype`。普通 PK/UNIQUE 只在非表达式、非 partial、内建 access method/opclass/type/collation的 enrollment profile 下显式支持；上面列出的 constraint 一律首发拒绝。

扫描结果不是仅靠 `pg_class.relhas*` 快照判断；这些字段只作交叉校验。catalog 查询报错、行数超限或 `relhas*` 与明细矛盾均 `AUTH_CATALOG_INCOMPLETE`。

### 4.3 MySQL 8.0.31+ 精确扫描

每个请求在持有 backup lock 后创建内部 connection-scoped root 表；临时表只承载 AgentSQL 已绑定的 catalog key，不对用户 SQL 可见：

```sql
DROP TEMPORARY TABLE IF EXISTS agentsql_catalog_roots;
CREATE TEMPORARY TABLE agentsql_catalog_roots (
  schema_name VARBINARY(64) NOT NULL,
  table_name  VARBINARY(64) NOT NULL,
  PRIMARY KEY(schema_name,table_name)
) ENGINE=MEMORY;
-- 对每个 root 使用 INSERT ... VALUES(CAST(? AS BINARY),CAST(? AS BINARY))；
-- 最多 256 对，禁止 identifier/string 拼接。
```

临时表 DDL 本身也在 S0 backup-lock矩阵内验证。比较前按 `@@lower_case_table_names` 在 Go 中生成 canonical key，但原始名字另行进入 digest。`Fpre/Fpost` 均执行下列同一条查询：

```sql
WITH roots(schema_name, table_name) AS (
  SELECT schema_name,table_name FROM agentsql_catalog_roots
), findings AS (
  SELECT c.TABLE_SCHEMA, c.TABLE_NAME, 'column_implicit' AS kind,
         c.COLUMN_NAME AS object_name,
         CONCAT_WS('|', c.ORDINAL_POSITION, c.COLUMN_DEFAULT, c.EXTRA,
                   c.GENERATION_EXPRESSION) AS raw
  FROM INFORMATION_SCHEMA.COLUMNS c JOIN roots r
    ON r.schema_name=c.TABLE_SCHEMA AND r.table_name=c.TABLE_NAME
  WHERE c.GENERATION_EXPRESSION <> ''
     OR c.COLUMN_DEFAULT IS NOT NULL
     OR LOWER(c.EXTRA) LIKE '%default_generated%'
     OR LOWER(c.EXTRA) LIKE '%on update%'
     OR LOWER(c.EXTRA) LIKE '%auto_increment%'

  UNION ALL
  SELECT t.EVENT_OBJECT_SCHEMA, t.EVENT_OBJECT_TABLE, 'trigger', t.TRIGGER_NAME,
         CONCAT_WS('|', t.EVENT_MANIPULATION, t.ACTION_ORDER,
                   t.ACTION_CONDITION, t.ACTION_STATEMENT, t.ACTION_ORIENTATION,
                   t.ACTION_TIMING, t.SQL_MODE, t.DEFINER,
                   t.CHARACTER_SET_CLIENT, t.COLLATION_CONNECTION,
                   t.DATABASE_COLLATION)
  FROM INFORMATION_SCHEMA.TRIGGERS t JOIN roots r
    ON r.schema_name=t.EVENT_OBJECT_SCHEMA AND r.table_name=t.EVENT_OBJECT_TABLE

  UNION ALL
  SELECT tc.CONSTRAINT_SCHEMA, tc.TABLE_NAME,
         CONCAT('constraint:',tc.CONSTRAINT_TYPE), tc.CONSTRAINT_NAME,
         CONCAT_WS('|',cc.CHECK_CLAUSE,rc.UNIQUE_CONSTRAINT_SCHEMA,
                   rc.UNIQUE_CONSTRAINT_NAME,rc.MATCH_OPTION,
                   rc.UPDATE_RULE,rc.DELETE_RULE)
  FROM INFORMATION_SCHEMA.TABLE_CONSTRAINTS tc JOIN roots r
    ON r.schema_name=tc.TABLE_SCHEMA AND r.table_name=tc.TABLE_NAME
  LEFT JOIN INFORMATION_SCHEMA.CHECK_CONSTRAINTS cc
    ON cc.CONSTRAINT_SCHEMA=tc.CONSTRAINT_SCHEMA
   AND cc.CONSTRAINT_NAME=tc.CONSTRAINT_NAME
  LEFT JOIN INFORMATION_SCHEMA.REFERENTIAL_CONSTRAINTS rc
    ON rc.CONSTRAINT_SCHEMA=tc.CONSTRAINT_SCHEMA
   AND rc.CONSTRAINT_NAME=tc.CONSTRAINT_NAME
  WHERE tc.CONSTRAINT_TYPE IN ('CHECK','FOREIGN KEY')

  UNION ALL
  SELECT s.TABLE_SCHEMA, s.TABLE_NAME, 'functional_index', s.INDEX_NAME,
         CONCAT_WS('|',s.SEQ_IN_INDEX,s.COLLATION,s.SUB_PART,
                   s.INDEX_TYPE,s.EXPRESSION)
  FROM INFORMATION_SCHEMA.STATISTICS s JOIN roots r
    ON r.schema_name=s.TABLE_SCHEMA AND r.table_name=s.TABLE_NAME
  WHERE s.EXPRESSION IS NOT NULL

  UNION ALL
  SELECT p.TABLE_SCHEMA,p.TABLE_NAME,'partition',COALESCE(p.PARTITION_NAME,''),
         CONCAT_WS('|',p.PARTITION_METHOD,p.PARTITION_EXPRESSION,
                   p.SUBPARTITION_METHOD,p.SUBPARTITION_EXPRESSION)
  FROM INFORMATION_SCHEMA.PARTITIONS p JOIN roots r
    ON r.schema_name=p.TABLE_SCHEMA AND r.table_name=p.TABLE_NAME
  WHERE p.PARTITION_NAME IS NOT NULL

  UNION ALL
  SELECT v.TABLE_SCHEMA,v.TABLE_NAME,'view_routine',v.SPECIFIC_NAME,
         CONCAT_WS('|',v.SPECIFIC_SCHEMA,v.SPECIFIC_CATALOG)
  FROM INFORMATION_SCHEMA.VIEW_ROUTINE_USAGE v JOIN roots r
    ON r.schema_name=v.TABLE_SCHEMA AND r.table_name=v.TABLE_NAME

  UNION ALL
  SELECT v.TABLE_SCHEMA,v.TABLE_NAME,'view_check_option',v.TABLE_NAME,
         CONCAT_WS('|',v.CHECK_OPTION,v.IS_UPDATABLE)
  FROM INFORMATION_SCHEMA.VIEWS v JOIN roots r
    ON r.schema_name=v.TABLE_SCHEMA AND r.table_name=v.TABLE_NAME
  WHERE v.CHECK_OPTION <> 'NONE'
)
SELECT * FROM findings
ORDER BY TABLE_SCHEMA,TABLE_NAME,kind,object_name;
```

字段清单还必须包括并 hash：

- `INFORMATION_SCHEMA.TABLES`: `TABLE_TYPE,ENGINE,VERSION,ROW_FORMAT,TABLE_COLLATION,CREATE_OPTIONS`；
- `COLUMNS`: `ORDINAL_POSITION,COLUMN_NAME,COLUMN_TYPE,DATA_TYPE,IS_NULLABLE,COLUMN_DEFAULT,CHARACTER_SET_NAME,COLLATION_NAME,EXTRA,GENERATION_EXPRESSION,SRS_ID`；
- `VIEWS`: `VIEW_DEFINITION,CHECK_OPTION,IS_UPDATABLE,DEFINER,SECURITY_TYPE,CHARACTER_SET_CLIENT,COLLATION_CONNECTION`，以及完整 `SHOW CREATE VIEW`；
- `STATISTICS`: 所有 index key part，包括 `EXPRESSION`；
- `KEY_COLUMN_USAGE/TABLE_CONSTRAINTS/CHECK_CONSTRAINTS/REFERENTIAL_CONSTRAINTS`；
- `VIEW_TABLE_USAGE/VIEW_ROUTINE_USAGE`；
- `@@server_uuid,@@version,@@version_comment,@@lower_case_table_names,@@sql_mode,@@character_set_connection,@@collation_connection,@@GLOBAL.gtid_executed`；
- `performance_schema.user_defined_functions` 的全量 name/type/library digest，以及当前 database 的 `INFORMATION_SCHEMA.ROUTINES` name/type/sql_mode/security/data_access digest。

账户必须能完整看见 `TRIGGERS`、`VIEWS`、routine usage 和 UDF 表；capability probe 通过预置 fixture 验证“存在一个对象时确实看见一行”，不能用空结果证明权限。权限不足即 unhealthy。

`DEFAULT NULL` 在公开 catalog 中可能与无 default 等价；首发把它作为无可执行表达式支持。其他非 NULL default、auto_increment、ON UPDATE、generated 均拒绝。

## 5. 表达式真实身份 binder

### 5.1 PostgreSQL `agentsql_binder`

新增目录建议为 `dbext/postgres/agentsql_binder/`，按 PG 14/15/16/17/18 构建。SQL 面只有：

```sql
agentsql_binder.capabilities() -> jsonb
agentsql_binder.analyze(sql text, param_type_oids oid[]) -> jsonb
```

扩展以调用者权限运行，不是 SECURITY DEFINER；拒绝 utility/multi statement。内部使用 PostgreSQL 主版本对应的 raw parser、`parse_analyze_fixedparams` 和 query rewrite API，只走 parse/analyze/rewrite，不进入 planner/executor。输出 canonical JSON 至少含：

- 每个 Query block、command type、`hasRecursive`、CTE materialization/data-modifying 标志；
- 每个 RTE 的 `rtekind,relid,relkind,inh,requiredPerms,checkAsUser`，Var 的 `varno,varattno,varlevelsup,vartype,vartypmod,varcollid`；
- `FuncExpr.funcid`、`Aggref.aggfnoid`、`WindowFunc.winfnoid`；
- `OpExpr/DistinctExpr/NullIfExpr/ScalarArrayOpExpr` 的 `opno,opfuncid,inputcollid,opcollid`；
- coercion node kind、source/target type、`pg_cast.oid/castfunc/castcontext/castmethod`；`CoerceViaIO` 同时输出 source `typoutput` 与 target `typinput`；
- `ArrayCoerceExpr.elemfuncid`、domain/type/subscript handler、collation OID；
- target-list resno/resname/resjunk、sort/group/distinct/window refs、jointree quals、limit/offset、RETURNING、conflict clauses；
- rewrite 后 security quals/rules/view path；所有 node 的 stable path 和 handled/unsupported 标记。

服务启动时比较 `capabilities()` 中 server major、extension ABI、git hash、node manifest hash、allowlist hash；任一不符拒绝 protocol 3。binder JSON 自身受 8 MiB/50k node cap。

授权 allowlist 按 OID + 精确 arg/result OID 固化。只允许 `pg_catalog` 内建标量函数/operator/cast，且相关 type/collation/opclass 也必须是 `pg_catalog`；domain、enum、range、自定义 type、procedural function、security definer、非 immutable/stable 读取状态对象均拒绝。aggregate 是 `rowset_dependent`，不再放入 pure source-free。

调用服务端 analyzer 前还有一个不可省略的无执行 preflight，避免“仅 analyze”本身因用户 type input/coercion 执行可变代码：raw visitor 先拒绝所有非 allowlist function/type/cast语法；函数首发必须显式限定为 `pg_catalog.<name>`；candidate relation resolver先从 catalog取得列 type OID，并在发现 domain/用户 type/用户 collation 时拒绝；unknown literal 只能流向已列入 allowlist 的内建目标类型。operator 只有在 operand type 已确定且 `pg_catalog` 存在唯一精确签名时才进入 analyzer。显式 `OPERATOR(schema.op)` 的 schema 不是 `pg_catalog` 时拒绝。preflight 不能完成时不调用 extension，直接 `AUTH_EXPRESSION_IDENTITY_UNSUPPORTED`。

绑定流程为：raw parser + 上述 preflight 找 candidate roots/types → binder 候选分析（不 plan/execute）→ 按 OID 排序显式锁 relation closure → 锁内重跑 preflight/binder 得 `Fpre` → 候选与 `Fpre` 不同最多重试一次，仍不同则 `AUTH_CATALOG_RACE`。授权只信锁内结果。S0 用带副作用的恶意 type input/function fixture证明 binder 路径不会调用它们。

### 5.2 MySQL 封闭语义 binder

MySQL binder 位于 `internal/authorizedexecute/mysqlbind`，输入只能是 raw SQL 和锁内 catalog。它使用 Vitess AST，但不使用 v2 的名字集合：

1. 实现 query-block scope、CTE/derived/view namespace、alias hiding、双方言实测的 clause 优先级；每个 `ColName` 必须唯一映射到 schema/table/ordinal。
2. `*`/NATURAL/USING 只从锁内 `COLUMNS` 展开；列数/序号/名字/type/collation完全进入 digest。
3. 首发 value grammar 仅为 direct column、literal/NULL、同一精确 MySQL type+charset+collation 的列间比较、同类型列与可无损 literal 比较、`IS [NOT] NULL`、AND/OR/NOT、简单数值同类型算术、`count(*)/count(constant)`。禁止 CASE、CAST/COLLATE、JSON/geometry、temporal 隐式转换、字符串/数值混合、函数调用和不确定 coercion。
4. 在同一物理连接、backup lock 内执行随机名 `PREPARE`/`DEALLOCATE PREPARE` 作为服务端语义接受侧信道；它只用于“服务器也接受”的交叉验证，不能替代本地完整 binding。PREPARE 失败映射为固定 deny。当前 Request 无参数，因此 `?`/named parameter 首发拒绝。
5. differential suite 对每个支持节点比较 server 成功/失败、EXPLAIN 可见 table set、实际列歧义和本地 binding；存在版本差异即从 allow matrix 移除。

## 6. 稳定身份、DDL epoch 与锁协议

### 6.1 PostgreSQL

身份为 system identifier + database OID + relation OID + attnum；列名仅展示。持锁顺序固定为 relation OID 升序：

```sql
SET LOCAL lock_timeout = '<catalog-lock-budget>';
LOCK TABLE <每个 base/view/matview，按 OID 解析后安全 quote> IN ACCESS SHARE MODE;
```

实现不依赖“锁 root view 会递归锁全图”的隐式行为；binder/rewrite closure 中每个 relation 都显式锁，并查询 `pg_locks` 确认当前 backend 对每个 OID 至少持有允许的 relation lock。matview 的存储 relation 与 `_RETURN` definition closure 都锁定。DROP/ALTER/CREATE OR REPLACE 必须与这些锁冲突；S0 在 14–18 对每类 DDL 实测。

用户函数、用户 operator/type/cast 已在 binder 阶段拒绝，因此没有无法 `LOCK TABLE` 的可变 executable dependency。内建 OID 集随 server major/capability digest固定。锁从 `Fpre` 前一直持有到 `Fpost`、mask、encoded envelope、审计屏障和 commit/rollback 完成。

### 6.2 MySQL

每个请求独占连接，顺序为：

```sql
SET SESSION lock_wait_timeout = <小整数秒>;
LOCK INSTANCE FOR BACKUP;
START TRANSACTION;
-- Fpre / bind / authorize / execute / Fpost / seal / audit barrier
COMMIT; -- 或 ROLLBACK
UNLOCK INSTANCE;
```

临时业务对象首发拒绝。S0 必须逐项并发证明 `CREATE/ALTER/DROP/RENAME/TRUNCATE TABLE`、`CREATE OR REPLACE/ALTER/DROP VIEW`、trigger、routine、UDF/plugin/component、event、database、constraint/index 操作在目标版本 backup lock 下的行为；任何未被阻断且能改变执行图的类别加入稳定拒绝或由 generation 协议捕获。

新增独立 DDL 控制面：

```text
datasource_ddl_epoch(datasource_id, epoch, last_binlog_file, last_binlog_pos,
                     last_gtid_set, continuity_status, revision)
object_generations(datasource_id, canonical_schema, canonical_name, kind,
                   generation, create_event_gtid, definition_digest, status)
```

- 生产 DDL 只能经 `agentsql-ddl-gateway`：在执行 DDL 前登记 intent，执行成功后以 binlog GTID/position CAS 增加 datasource epoch 和受影响 object generation；未知影响范围则整个 datasource `needs_rebind`。
- 独立 binlog watcher 持续读取 Query/DDL events。事件与 gateway intent 对不上、日志被 purge/reset、position/GTID 不连续、`sql_log_bin=0` 能力暴露、server_uuid 改变、restore/failover 未重新 enrollment，立即把 datasource freeze。
- 每请求取得 backup lock 后记录 `(binary_log_file,position,@@GLOBAL.gtid_executed)` 水位；`Fpre` 前等待 watcher 的 processed position/GTID 覆盖该水位，超出 catalog wall budget即拒绝。`Fpre/Fpost` 同时比较 gateway epoch/generation、watcher continuity、GTID superset 和 catalog fingerprint。普通并发 DML可以推进全局 GTID；比较的是集合包含关系和 watcher已处理水位，不要求前后相等。任一可观察 DDL 都强制 re-enrollment/rebind；不再有“同规范名称自动继承”。
- 服务账户无 DDL、`SUPER`、`BINLOG_ADMIN`、`SYSTEM_VARIABLES_ADMIN`、plugin/component 或日志清理权限。DDL 管理账户与 gateway 身份分离。

同请求 ABA 由 backup lock 关闭；跨请求可观察 ABA 由 generation/binlog关闭。完全同构、带外且能规避/重置上述证据的 ABA 属第 16 节残留边界。

## 7. `AuthorizedExecute`、控制面 fence 与审批

### 7.1 控制面同一 snapshot

新增 `ControlSnapshotTx`，一次读取 Agent、datasource、父子 permissions、binding/enrollment、mask key/rule revision、compat fence、DDL watcher health。PostgreSQL metadata backend 在事务开始取共享 advisory xact lock；所有策略/fence/mask/enrollment mutation 取同 key 的独占 advisory xact lock。SQLite backend 使用 `BEGIN IMMEDIATE`，因此 B2 SQLite 控制面只适合单实例/低并发；无法接受串行化时必须迁到 PostgreSQL metadata。

请求结束前，在同一个 `ControlSnapshotTx` 内重读 fence/revision并执行探活查询；写者因锁不能在请求中间改变这些值。连接/事务失败就是 fence 失败。业务 DB commit、sealed envelope和审计完成前不释放控制锁；commit 后再做一次同事务 no-op/revision读取，成功才铸造一次性的 `deliverySeal`。随后立即结束 control transaction并把 sealed bytes交给 transport，不在慢客户端 socket 写期间持有控制锁。因 mutation 在 seal 前始终被共享锁阻挡，不存在“final fence 与 delivery-ready 之间被 writer 穿越”的窗口。

每次请求都做，禁止 TTL/cache。控制面失联的 protocol 1/2/3 实例都失去 query/write/explain/approval execute 服务能力并退出 ready；lease 只用于运维观察，不再决定安全实例全集。

### 7.2 线性流程

1. 校验 raw SQL/并发配额；打开 `ControlSnapshotTx` 并验证 reader/writer fence。
2. 内部 parse；静态 profile deny 时业务 DB 调用为 0，但仍记录固定审计。
3. 打开请求独占业务连接和可回滚 tx/savepoint；取得方言 schema lock。
4. 候选 bind → 锁 closure → `Fpre` 双扫描中的第一次 → 完整 binder → authorizer。
5. 编译唯一 ProtectionPlan；不存在 mask meet/缺 key/算法能力则不执行。
6. 可信 dynamic explain 如需要由系统生成；用户 EXPLAIN 永远拒绝。
7. 执行，低层有界读取；到限立即 cancel，drain/关闭坏连接，rollback。
8. 用同一 plan mask；生成固定响应对象并用最终 codec 编码到 sealed buffer。
9. 锁内执行 `Fpost`；必须与 `Fpre` 和 enrollment一致。重新验证 control snapshot/fence。
10. 写 intent/outcome audit 屏障；DML commit，read tx结束；仍持有控制锁时做最终 control no-op/revision读取并生成 `deliverySeal`。commit 前任一步失败都 rollback且无业务结果；commit 后 control连接意外失败属于 `COMMIT_OUTCOME_UNKNOWN`，不交付、不自动重试，并由 intent/outcome审计和对账任务收敛。
11. 结束 control transaction。只有持有有效 `deliverySeal` 的 `SealedAuthorizedResponse` 才能由 transport 原子复制 header/status/body。socket 写失败只影响交付，不重试 DML。

### 7.3 审批

`requestApproval` 只保存规范化意图：agent/datasource、raw SQL SHA-256、statement class、申请时 revision 和过期时间，不保存可复用 allow/catalog/plan。批准后的执行携带 approval ID 和同一 raw SQL；入口重新调用完整当前时点 `AuthorizedExecute`，重新认证、fence、parse/bind/catalog/policy/mask/lock。SQL hash 不同、过期、策略变严或对象变化都拒绝。

## 8. Mask 安全格、诊断净化与响应封印

### 8.1 mask meet

输出位置汇总所有 set arm、view layer和匹配规则：

- deny 与任何项 meet = deny；
- allow 与 mask meet = mask；
- allow 与 allow = allow；
- mask 只有在 `(algorithm_id, semantic_version, key_id, key_version, canonical_parameters, input_type, output_type)` 全部相等时 meet 为自身；
- 任意不同算法、不同 key/version 或不同参数，不声明“更强/更弱”，直接 `AUTH_MASK_MEET_UNDEFINED`。

未来若要加入 mask 间偏序，必须附不可区分性/信息泄露证明和测试，不能靠名字或输出长度排序。

### 8.2 诊断通道

对外只有固定 envelope：授权前 deny 用稳定 auth reason；授权后的所有 DB syntax/semantic/constraint/data/resource/error 统一为 `EXECUTION_FAILED`，不含 SQLSTATE、errno、message、DETAIL、HINT、CONTEXT、schema/table/column/constraint/routine 名。内部受保护日志最多记录 driver code、stage、request ID，不记录 server message或值。

- pgx 配置 `OnNotice` 为只计数并丢弃字段；notice/warning 不进入 Response/audit。
- MySQL 不执行 `SHOW WARNINGS/ERRORS`，关闭 multiStatements/local infile，`clientFoundRows=false`；driver error 文本不穿透。
- command tag、last insert id、matched/changed/affected rows首发均不对外；成功 DML只返回固定 `{status:"ok"}`。RETURNING 当作行结果走 mask。
- serialization error、mask error、audit error、postcheck error均返回同一固定失败 envelope并丢弃中间 buffer。

### 8.3 三层字节上限和 transport

默认上限见第 11 节：raw result、post-mask result、encoded envelope分别独立计数。PG 请求连接设置 `PgConn().Frontend().SetMaxBodyLen(max_db_frame_bytes)`；MySQL 必须使用带 inbound cumulative packet cap 的审计过适配器，v1.9.3 原样能力不足时 datasource unhealthy。

所有值通过 raw byte length 先检查再分配 string；每行、每值、累计均检查。mask 写入 bounded buffer；最终 JSON/MCP codec 写入另一 bounded buffer，成功后得到不可变 bytes+headers+digest。

HTTP 在 MCP SDK 和 admin handler 外包一层 `TransactionalResponseWriter`：`Header/WriteHeader/Write/Flush` 都只写私有 buffer；`http.Flusher` 在未 commit 前是 no-op 或返回错误；超限丢弃。SSE、chunked progress、keepalive comment和 early hints 对 SQL routes 禁用。stdio 每个完整 JSON-RPC frame先编码进 bounded buffer，再一次写出；不得边 encode 边写 stdout。

## 9. Metadata、迁移、revision、ETag 与 `*`

### 9.1 解决 CSV 名称无法原子得到 ordinal

迁移 `combined 0009`、`separate metadata 0008` 只做可在 metadata 原子完成的工作：建表/约束/fence，并把 legacy token 写入不可授权 staging，不伪造 ordinal。

```text
policy_column_permission_staging
  policy_id, token_ordinal, legacy_token, requested_usage,
  source_csv_sha256, bind_status, error_code,
  PRIMARY KEY(policy_id, token_ordinal, requested_usage)

policy_column_permissions
  policy_id, relation_enrollment_id, column_ordinal, column_name,
  column_type_digest, usage, parent_revision,
  PRIMARY KEY(policy_id, column_ordinal, usage)
```

迁移事务在 PostgreSQL advisory xact lock / SQLite exclusive migration transaction 内做：claim → preflight → DDL → `split(',')+trim` 写 staging output+reference → 行数/hash 校验 → fence 保持 protocol 2 → commit。存在任何 staging row 的 datasource始终 unhealthy，不能被运行时当作无 column policy。

随后 binding worker 对一个 datasource取得第 6 节 schema lock，解析真实列名/ordinal/type；保持业务 schema lock，开启 metadata mutation transaction，以 `policy revision + staging hash + enrollment fingerprint + ddl epoch` CAS，把全部 staging rows一次插入正式表、删除 staging、父 revision +1。任何失败整体 rollback；业务锁释放后仍有 staging就继续 unhealthy。

### 9.2 父子不可绕过 mutation

业务角色撤销对 `policies`、permission、binding、fence表的直接 DML。PostgreSQL 只授予 `agentsql_policy_mutate(...)` SECURITY DEFINER procedure 的 EXECUTE，procedure 固定 `search_path=pg_catalog,<metadata_schema>`、校验调用角色并在一事务内锁父行、检查 ETag/fence、替换子项、自动 bump parent revision。SQLite repository 在单一 `BEGIN IMMEDIATE` 中执行相同逻辑；应用 DB handle不导出。

防御性 trigger 仍安装：permission/staging INSERT/UPDATE/DELETE 后自动 bump父 revision；不允许父 object/action 与子项不一致；trigger 通过 transaction-local guard避免 procedure批量替换时多 bump，但 direct DML 故障测试必须证明至少 bump一次，不能静默绕过。

### 9.3 强 ETag

mutation 解析规则固定：

1. `Header.Values("If-Match")` 必须恰有一个值；
2. trim 后必须逐字匹配响应生成的 canonical 强 ETag：`"policy-<base64url(id)>-r<positive-decimal>"`；
3. 拒绝 `*`、`W/`、任何逗号/list、重复 header、空白变体、前导零和非 canonical base64url；
4. 缠绕到 procedure 的是已解析 policy ID + revision，不传原 header；缺失 428，格式不合法 400，过期 412。

`legacy_unrepresentable=true` 时，任何只带 legacy `columns` 的 PUT 一律 409，即使当前 usage 对称。新旧字段同时出现且不逐项等价为 422。

### 9.4 `*`

所有写入口拒绝新 `*`。grandfathered `*` 展开时，从取得业务 schema lock开始，一直保持到 metadata CAS 提交；CAS 比较 relation fingerprint/enrollment revision/DDL epoch。展开后的每个真实列写 ordinal/name/type digest。失败或锁超时保持 staging/needs_rebind，绝不保存部分展开。protocol 3 激活要求 `*` 和 staging 均为零。

## 10. Protocol fence 与 safe fallback

protocol 定义：1 legacy，2 bridge，3 B2。

- protocol 2 从第一天就必须执行第 7.1 节每请求不可缓存 reader/writer fence；控制面失联即拒绝服务。
- binary 只有在 S1–S5 和 S6 safety全部编译、启动 self-test/DB capability probe通过时才可在 `runtime_instances` 声明 reader=writer=3。仅实现 schema 或 parser 不得声明 3。
- activation transaction 取独占 control lock，验证所有当前服务身份的 capability attestation、staging=0、`*`=0、enrollment healthy、MySQL watcher continuous、safe fallback artifact hash已登记且演练成功，然后设置 min reader/writer=3、asymmetric。
- 失联实例不能因为 lease 过期而被“忽略后继续服务”：它每个请求拿不到 control snapshot，自然 fail-closed。lease只影响 dashboard。
- safe fallback 必须在 activation 前构建、签名、部署到待命环境并完成演练。它仍是 protocol 3，保留子表、fence、binder、catalog双扫描、锁、mask plan、response sealing和错误净化；只能关闭非安全 UI/新管理功能。
- asymmetric 激活后绝不回 protocol 1/2。fallback失败时 freeze datasource。

## 11. 资源预算与取消语义

默认硬上限均按请求共享，部署只能收紧：

| 资源 | 上限 |
|---|---:|
| raw SQL UTF-8 bytes | 256 KiB |
| bind 参数 | 当前 0；未来最多 256 个、单个 64 KiB、合计 1 MiB |
| parser token / lexical nesting | 100,000 / 128；在构造 AST 前计数 |
| AST node / query block | 50,000 / 256 |
| projection/output column | 4,096 / 256 |
| relation / view depth / catalog round trip | 256 / 16 / 32 |
| definition SQL / binder JSON / raw catalog bytes | 1 MiB / 8 MiB / 8 MiB |
| catalog row / column metadata | 50,000 / 16,384 |
| dependency edge / expanded path / work unit | 32,768 / 65,536 / 250,000 |
| DB protocol message/frame | 1 MiB |
| raw cell / raw row / raw result | 64 KiB / 1 MiB / 16 MiB |
| post-mask cell / result | 128 KiB / 16 MiB |
| final encoded envelope | 20 MiB |
| statement execution wall | `min(datasource timeout, 5s)` |
| catalog+lock wall | `min(1s,max(200ms,stmt_timeout/4))` |
| 总锁持有 wall | `min(2s,max(500ms,stmt_timeout/2))`，且不超过总执行 deadline |
| 请求总 wall | `min(10s, configured request deadline)` |
| 每 agent / tenant / datasource in-flight | 4 / 32 / `min(conn_limit/2,16)` |
| MySQL backup-lock in-flight per datasource | 2，且独立 token bucket 10/s |

work unit 在每次 node visit、edge添加、view path复制、set arm笛卡尔组合前递增；因此即使 unique relation 未超限，DAG 路径爆炸也会先拒绝。所有计数使用 checked arithmetic。

达到任一限制：取消 query context；PG 发 CancelRequest并因 frame-cap错误 discard连接；MySQL `KILL QUERY`/context cancel后关闭该物理连接；rollback/savepoint rollback；丢弃 result/mask/encoded buffer；释放锁。不得返回“已截断但成功”的部分单元格。row limit 可作为正常完整行截断，但必须多读至多一行来判定且仍受所有字节预算。

## 12. Choke point 与能力隔离

包依赖目标：

```text
transport/admin/mcp -> authorizedexecute public facade
authorizedexecute -> private businessdb capability + control repositories
executor drivers -> only private businessdb implementation
store -> metadata-only DB capability
```

除 `internal/authorizedexecute` 及其私有子包外，业务依赖图拿不到 datasource password、DSN、`*sql.DB`、`*sql.Conn`、pgx pool/conn、driver connector、`executor.Executor/Session/WriteTx` 或能接收 SQL string 的函数。删除/私有化 `bootstrap.Runtime.ExecutorFor`；runtime只暴露 `AuthorizedExecutor`、`DatasourceHealthPort`、`AuthorizedSchemaList`、固定模板 `InternalSampleRead`。

CI 同时做：

1. `go list -deps -json` allowlist：只有 executor/store/authorizedexecute私有包可导入 `database/sql`、pgx、mysql driver；测试包单独 allowlist；
2. AST/SSA 扫描 `Query/Exec/Prepare/Raw/Conn/OpenDB/NewConnector` 和字符串 SQL sink；
3. 对最终 `agentsql` 二进制做符号/字符串入口扫描，生成 raw DB callsite manifest，与签入 allowlist逐项匹配；
4. secret/credential类型定义在私有包，禁止作为 interface 返回；typed ports 的请求类型不能表达 caller SQL；
5. 跨入口负向语料证明 MCP query/write/explain/requestApproval、HTTP/stdio、demo、session、未来 REST 都走同一门。

`list_schema` 只返回当前 agent 已授权且 enrollment healthy 的对象/列。discovery sample 使用枚举值构造固定 query，不接受 SQL fragment/identifier string；ping 调驱动 Ping，不执行可替换 SQL。

## 13. 代码落点与接口草案

建议新增：

```text
internal/authorizedexecute/
  service.go              # 唯一 public AuthorizedExecute
  control_snapshot.go     # fence tx
  pgcatalog/, mysqlcatalog/
  pgbind/, mysqlbind/
  enrollment/
  authorizer/, plan/
  sealedresult/
internal/transportbuffer/
dbext/postgres/agentsql_binder/
cmd/agentsql-ddl-gateway/
cmd/agentsql-binlog-watcher/
```

核心私有端口：

```go
type Service interface {
    AuthorizedExecute(context.Context, Request) (SealedAuthorizedResponse, error)
}

type BusinessConnection interface {
    BeginGuarded(context.Context, GuardBudget) (GuardedTx, error)
}

type GuardedTx interface {
    LockAndBind(context.Context, RawStatement) (BoundProgram, CatalogFrame, error)
    ExecuteBound(context.Context, BoundProgram, ReadBudget) (BoundedResult, error)
    ScanPost(context.Context, BoundProgram) (CatalogFrame, error)
    Commit(context.Context) error
    Rollback(context.Context) error
}
```

接口不提供任意 `Query(string)` 给上层。PG/MySQL 方言细节封在 GuardedTx 实现中。现有 `Pipeline.Process/ProcessDemo` 只能成为 façade 兼容适配器，不能继续持有 executor。

## 14. 实现切片、验收与同发布要求

顺序：`S0 -> (S1 || S2) -> S3 -> S4 -> S5 -> S6 -> S7 -> S8`。始终 feature-off，直到 S8 activation。

### S0：能力与隐式对象枚举 spike（前置）

内容：PG binder 原型；第 4 节 catalog fixture；PG relation lock闭包；MySQL backup-lock DDL矩阵、PREPARE差分、binlog continuity；driver inbound cap；HTTP/MCP/stdio首字节探针。

验收产物：按 server patch version 的机器可读 capability JSON、每项 DDL 并发时间线、catalog SQL golden、binder OID golden、transport packet capture、最小 grants。任何红项必须转入拒绝矩阵，不能 waiver。

### S1：metadata、staging、enrollment、fence

内容：0009/0008；typed migration callback和迁移锁；staging→bind CAS；procedure/role/trigger；revision/严格 ETag；runtime fence；SQLite→PG manifest。

验收：迁移任意故障点原子回滚；CSV 名称不产生假 ordinal；direct child DML自动 bump；每请求 fence与失联拒绝；legacy_unrepresentable PUT=409；ETag恶意语料。

### S2：封闭 visitor 与双方言 binder

内容：node manifest、所有 reference site、PG extension正式实现、MySQL封闭语义 binder、递归/whole-row/system/locking clause拒绝、预算计数。

验收：PG真实 OID/implicit cast/operator golden；MySQL supported grammar差分；调用方无法伪造 proof；递归 CTE/view全部稳定拒绝。

### S3：authorizer、视图传播与 mask plan

内容：identity grant index；view/matview逐层 ACL；DISTINCT/set双 usage；mask identity-only meet；唯一 plan/digest。

验收：安全格全矩阵；不同算法/key/params拒绝；plan/redactor digest一致；任一 legacy redactor能力拒绝。

### S4：enrollment、catalog双扫描与 DDL generation

内容：双方言第 4 节扫描；PG闭包显式锁；MySQL backup lock、gateway、binlog watcher；needs_rebind/freeze。

验收：隐式对象每一类 fixture；锁内并发 DDL；可观察 drop/recreate/rename强制 re-enroll；binlog purge/reset/gap冻结；PG持锁集合核对。

### S5：唯一 AuthorizedExecute 与有界事务执行

内容：物理连接、tx/savepoint、pre/post、取消/rollback、低层 frame cap、逐值读取、审批重授权、raw handle收口。

验收：DML在 postcheck/mask/audit/fence失败时无落库；资源到限 cancel/rollback；CI capability隔离和最终二进制 callsite manifest。

### S6：安全 API/transport/审计

必须同发布部分：严格 ETag、legacy PUT、`*` 展开、固定错误映射、notice/warning/tag/rowcount净化、post-mask/encoded cap、HTTP/MCP/stdio transactional writer、审计屏障。纯 UI 展示可后置。

验收：每个 transport 在成功封印前抓包为 0 bytes；SSE/flush/keepalive恶意 handler测试；所有诊断只见固定 envelope；encoded膨胀超限零结果。

### S7：安全总验收 gate

内容：双方言版本矩阵、完整逃逸语料、跨入口同因、并发 DDL、故障注入、资源/DoS、升级/网络分区。

验收：第 15 节 P0矩阵全部达到声明状态；S0证据无过期；威胁残留签收完成。S7 是发布前 gate，不是发布后观察。

### S8：safe fallback、性能和 activation

内容：protocol 3 fallback构建与演练；真实 catalog SLO/锁争用；dry-run deny报告；activation checklist和告警。

验收：S1–S6 safety同一 release artifact；capability attestation全绿；staging=`*`=0；enrollment/watcher healthy；fallback演练成功后才激活 asymmetric。

### 必须同发布的集合

S1 fence/staging/ETag、S2 binder/default-deny、S3 authorizer/mask meet、S4 enrollment/catalog/锁/DDL generation、S5 choke point/rollback/bounds、S6 safety API/diagnostic/transport sealing必须在同一 protocol 3 artifact与同一 activation gate中。缺任何一个只允许 feature-off部署，不得接生产 B2流量。

## 15. v2 复核 P0-1～P0-15 处置对照

| P0 | v3 处置 | 状态 |
|---|---|---|
| P0-1 | 第 4 节 enrollment+每请求双扫描覆盖隐式执行对象；第 3 节补 locking/whole-row/system/inheritance/partition/foreign；递归全部拒绝 | 技术关闭（以 S0/S4验收为证） |
| P0-2 | 保留 DISTINCT/set op双授权矩阵 | 规则已关闭 |
| P0-3 | caller只交 raw SQL；proof私有seal；aggregate改 rowset-dependent；PG服务端 analyzed OID；MySQL不能证明即拒绝 | 技术关闭 |
| P0-4 | view/matview执行图扩到完整 closure；rule/RLS/generated/trigger等拒绝；matview definition闭包显式锁 | 技术关闭 |
| P0-5 | PG真实分析 OID；MySQL封闭语义 binder+generation，取消名称自动继承 | 可观察部分技术关闭；完全同构带外 ABA见边界 M1 |
| P0-6 | PG显式锁每个 closure relation并核对持锁；MySQL逐 DDL矩阵+backup lock；锁至封印/commit；总锁时限 | 技术关闭；MySQL M1须签收 |
| P0-7 | 每请求锁内 catalog，不使用跨请求 snapshot作授权依据 | 已关闭 |
| P0-8 | binder后复制 alias/ordinal依赖并真库差分 | 已关闭 |
| P0-9 | mask identity meet；诊断/tag/rowcount净化；raw/post-mask/encoded三限；全 transport buffered | 技术关闭 |
| P0-10 | 每请求同一 control snapshot持锁到交付；失联即拒绝；protocol 3能力声明和 fallback前置 | 技术关闭 |
| P0-11 | 单个强精确 canonical ETag；unrepresentable legacy PUT=409；`*` 锁保持到 metadata CAS | 技术关闭 |
| P0-12 | MySQL object generation+binlog continuity；所有可观察 DDL re-enroll；不再同名继承 | 可观察部分技术关闭；公开接口不可分辨部分为 M1 |
| P0-13 | staging保存名称，经业务锁 bind后 metadata CAS转正；procedure/受限 role+trigger自动 bump | 技术关闭 |
| P0-14 | credential/raw handle能力隔离；依赖/SSA/二进制扫描；批准后完整重授权 | 技术关闭 |
| P0-15 | 第 11 节补全 pre-parse、raw/param、edge/path/work、cell、post-mask/encoded、总时间/锁/并发，低层cap+取消回滚 | 技术关闭 |

“技术关闭”表示设计给出可实现机制和必过验收，不表示当前产品代码已经实现。任何 S0 事实与设计假设不符，必须收紧支持矩阵或重新评审。

## 16. 必须由负责人签收的威胁模型残留

### M1：MySQL 完全同构、带外 DDL ABA

精确定义：有 MySQL DDL/超级管理能力的主体，在 AgentSQL 两次请求观察之间，绕过 DDL gateway 与连续 binlog watcher，完成 drop/rename + create，使 `server_uuid + schema/name/kind + SHOW CREATE + 全部 I_S 字段 + gateway epoch/generation + 可见 GTID/binlog位置` 均恢复为先前值。典型途径包括同时控制/清除日志与generation表，或恢复克隆快照。MySQL 8.0 公开 SQL/I_S 接口没有不可伪造、永久对象 OID来区分此状态。

接受条件：数据库安全负责人、AgentSQL产品安全负责人、业务数据owner三方签收；DDL/admin账户被视为可信；禁止带外 DDL、`sql_log_bin=0`、RESET/PURGE及未登记restore；IAM把这些能力与服务账户隔离；watcher gap自动freeze；备份恢复/failover runbook强制全 datasource re-enrollment。任一条件做不到，MySQL B2为 unsupported，不得以风险接受配置静默放行。

### M2：恶意数据库超级管理员/被篡改服务器

双方言都信任数据库 server binary、system catalog、PG binder扩展及其签名部署链。能篡改 server/catalog/扩展、伪造网络端点或读取 AgentSQL密钥的超级管理员不在列级授权对抗范围。缓解：TLS端点校验、最小权限、扩展artifact签名/hash attestation、DB审计和职责分离。基础设施安全负责人签收。

### M3：可用性而非保密性

授权请求在短时持有 PG relation lock / MySQL global backup lock，攻击者可消耗其限额造成拒绝服务，但第 11 节硬时限、并发/tenant/datasource配额限制放大。设计不保证恶意DB管理员或数据库故障下的可用性；不允许以提高可用性为由绕过锁。SRE owner签收容量与告警阈值。

除 M1–M3 外，不保留“名称自动继承”“catalog最好努力”“旧实例失联仍服务”“诊断可透传”等隐含风险。

## 17. 审计与发布验收摘要

审计 `column_auth.version=3` 记录：control/fence/policy/mask/enrollment/DDL epoch revisions，binder capability/hash，plan digest，Fpre/Fpost digest，实际锁集合摘要，预算计数，每个 bound use 的 identity/ordinal/site/view path和固定 reason。不得记录值、predicate literal、server message、DETAIL/NOTICE/WARNING或完整 view SQL。明细256项/32 KiB，优先 deny，截断内容另存 canonical digest。

发布必须证明：

- 每种隐式对象在 enrollment、Fpre、Fpost 三处均能被看见并拒绝；权限不足不能伪装为空；
- PG 14–18 binder OID和锁闭包，MySQL 8.0.31及目标最新8.0的 DDL矩阵；
- 同一负向 SQL 从每个入口得到同 reason/DB阶段/零提前字节；
- fence网络分区后旧实例立即失去服务能力；
- 所有 rollback、mask meet、encoded超限、审计失败和 postcheck失败均不产生业务提交/部分结果；
- migration 8→9、7→8、SQLite→PG manifest及 direct-DML故障测试；
- safe fallback在 activation前演练；M1–M3签收附在发布记录。

## 18. 可开始实现的范围

现在可以立即开始且不承担生产放行含义的工作：S0全部；S1 staging/fence/repository/迁移骨架；S2 closed visitor和 PG binder原型/MySQL差分 harness；S3纯 authorizer/mask lattice；transport transactional writer与低层 frame-cap spike。这些都应保持 feature-off。

在 S0 未通过前，不应冻结 PG 扩展 ABI、MySQL支持表达式集合或声称 backup lock矩阵已证实；在 S1–S6 safety和 S7 未整体通过前，不得声明 protocol 3、激活 asymmetric usage或导入生产 B2流量。

## 19. 参考的数据库公开接口

- PostgreSQL catalogs：`pg_attribute`、`pg_class`、`pg_policy`、`pg_rewrite`、`pg_depend`、`pg_trigger`、`pg_constraint`、`pg_index`、`pg_inherits`，见 <https://www.postgresql.org/docs/18/catalogs-overview.html>。
- PostgreSQL `pg_rewrite.ev_action`：<https://www.postgresql.org/docs/18/catalog-pg-rewrite.html>。
- MySQL backup lock：<https://dev.mysql.com/doc/refman/8.0/en/lock-instance-for-backup.html>。
- MySQL `INFORMATION_SCHEMA.COLUMNS`：<https://dev.mysql.com/doc/refman/8.0/en/information-schema-columns-table.html>。
- MySQL `INFORMATION_SCHEMA.VIEWS`：<https://dev.mysql.com/doc/refman/8.0/en/information-schema-views-table.html>。
- MySQL loadable functions：<https://dev.mysql.com/doc/refman/8.0/en/performance-schema-user-defined-functions-table.html>。
- MySQL binary log/GTID DDL：<https://dev.mysql.com/doc/refman/8.0/en/binary-log.html>、<https://dev.mysql.com/doc/refman/8.0/en/replication-gtids-lifecycle.html>。

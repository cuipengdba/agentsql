# AgentSQL v0.4 B2 多表 JOIN / 自连接列级授权——权威设计 v2

状态：**实现基线；15 项 P0 全部关闭后方可发布**  
日期：2026-09-24  
工作目录：`D:\ruanjiansheji\agentsql-v04`  
范围：本文件只定义设计、迁移和验收，不包含产品代码变更。

## 0. 冻结结论

本版冻结以下安全裁决，实施不得用配置、规则 override 或兼容路径改变：

1. B2 是规则引擎之外不可关闭的授权门，唯一业务 SQL 入口命名为 `AuthorizedExecute`。`R010` 只可保留兼容解释，不再承担授权。
2. **Catalog 方案选择“每请求、同执行上下文绑定”**；不采用跨请求 schema snapshot。任何 SQL 中出现的关系（包括看似已限定的普通表）都必须在本请求内由数据库绑定为稳定身份后才能授权。
3. 支持的语法采用封闭 AST visitor：每个可能承载表达式、关系或子语句的节点必须标记为 `handled` 或 `unsupported -> deny`；不存在默认跳过。
4. `DISTINCT`、`DISTINCT ON`、`UNION`（非 ALL）、`INTERSECT`、`EXCEPT` 对参与比较的值要求 `output + reference`；`UNION ALL` 的分支值只要求 `output`。
5. `source_free` 必须有不可伪造的结构化证明；依赖为空本身不是证明。用户函数、表函数/SRF、UDF、自定义 operator/cast 默认 `opaque -> deny`。
6. 普通视图和 PostgreSQL 物化视图都执行“当前视图自身表/暴露列 ACL + 沿定义 lineage 的每层视图及底表表/列 ACL”双重授权；物化视图不再留作待确认。
7. PostgreSQL catalog 身份以数据库/system identity、relation OID、`pg_attribute.attnum`、`pg_rewrite`/`pg_depend` 绑定为核心；MySQL 以 `server_uuid + 规范化 database/relation/kind`、列序号、完整 `VIEW` 创建上下文及单调 GTID 版本为核心。
8. 授权、执行、mask、后验重验在可回滚事务/保存点与 schema 稳定锁内完成；结果完全缓冲，任何校验和审计屏障完成前禁止发送首字节。任何失败丢弃全部结果；DML 回滚。
9. PostgreSQL 14–18 为目标版本；MySQL B2 最低版本冻结为 **8.0.31**，并要求 `BACKUP_ADMIN`、`gtid_mode=ON`、binary log 可用。能力不满足时 datasource 为 `unhealthy`，不是降级运行。
10. 旧 `policies.columns` 在 B2 激活后停止写入；规范化子表是唯一权威来源。非对称 usage 激活前必须完成数据库级 reader/writer fence，不能与旧 writer 混跑，也不能回退到不理解子表的旧版本。
11. 当前模型没有持久化的显式 column deny；文中“列拒绝”仅指“缺 grant、未知、不完整或不一致”。最终优先级固定为：Agent/profile/语句类别 deny → 任一匹配表 deny → 缺表 allow → lineage/catalog/policy 不可证明 → 缺 usage grant → 已授权输出 mask → allow。

## 1. 已核对的真实代码基线

以下名称均来自当前仓库：

- `internal/model/model.go`：`model.AST`、`ProjectionLineage`、`LineageArm`、`ColumnDependency`、`ColumnOrigin`、`QueryResult`。现有 `QueryResult` 已是内存中的完整 `Columns/Rows`，但只有行数限制，没有授权事务、结果字节上限或 plan digest。
- `internal/model/storage.go`：`model.Policy.Columns *string` 仍是 CSV；`internal/model/placeholders.go` 的 `model.PolicyDecision` 只有 `AllowedTables`、`DeniedTables`、`ColumnACL`、`Level`。
- `internal/parser/parser.go`：只支持 PostgreSQL/MySQL；`internal/parser/lineage.go` 现有硬上限为深度 64、集合叶 256、投影 4096、依赖 16384、AST 节点 50000。PostgreSQL 使用 `pg_query_go/v5`，MySQL 使用 Vitess v0.22.4；`mysql.go` 当前以 `MySQLServerVersion: "8.0.30"` 初始化，实施 B2 时须改为并测试 8.0.31 语义。
- `internal/pipeline/pipeline.go`：真实顺序为 auth/load/parse/static/dynamic/execute/redact/audit；只有显式 `Request.SessionID` 时才开 session，mask redactor 在执行后构造，且仍可退回 `DirectProjections` 等 legacy 路径。
- `internal/executor/executor.go`：`Executor`、`Session`、`WriteTx` 没有 catalog、schema lock、读事务、保存点、后验重验或带 plan 的执行接口；因此不能在现接口上声称已关闭 TOCTOU。
- `internal/mask/redactor.go` 与 `internal/mask/lineage.go`：`ProjectionLineageAwareRedactor` 和 `decideProjectionColumn` 已有 B1 fail-closed 语义，但没有可序列化、可在执行前验证的不可变保护计划。
- `internal/store/migrate.go`：`applyMigrationWithOptions` 已把 `claimMigration`、可选 preflight、DDL 和 commit 放在一个 `sql.Tx` 内；B2 需推广成通用 migration callback，并增加显式迁移锁。当前 combined 最新为 0008，separate metadata 最新为 0007。
- `internal/store/policy_repository.go`：父 policy CRUD 目前是单条语句，不是父子事务；API DTO 在 `internal/adminapi/dto.go` 的 `policyInput/policyView`，路由在 `internal/adminapi/handler.go`。
- 当前用户提交 SQL 的生产入口是 `internal/mcpserver/handlers.go` 的 `query`、`executeWrite`、`explainQuery`、`requestApproval`，最终调用 `Pipeline.Process`；管理控制台 demo run 经 `ProcessDemo`。`pipeline.StaticAssess` 不执行 SQL。
- 现存直接 executor 使用包括：MCP `list_schema`、`controlledread.Service` 的固定模板 metadata/sample SQL、管理端 datasource ping 的 `SELECT 1`，以及 `bootstrap.Runtime.ExecutorFor`。它们不是任意业务 SQL，但必须改成强类型内部端口，不能继续暴露通用 executor。
- `internal/store/sqlite_pg_migrate.go` 的搬迁 manifest 当前只含 `policies`，B2 子表、binding、compat fence 表必须按父先子后加入。

## 2. 权威目标与安全不变量

### 2.1 授权单位

每次读取都归一为 `ColumnUse`：

```go
// 拟议类型，落点 internal/model/column_auth.go。
type ColumnUsage string
const (
    ColumnUsageOutput    ColumnUsage = "output"
    ColumnUsageReference ColumnUsage = "reference"
)

type ReferenceSite string
const (
    SiteWhere             ReferenceSite = "where"
    SiteJoinOn            ReferenceSite = "join_on"
    SiteUsing             ReferenceSite = "using"
    SiteGroupBy           ReferenceSite = "group_by"
    SiteHaving            ReferenceSite = "having"
    SiteOrderBy           ReferenceSite = "order_by"
    SiteDistinct          ReferenceSite = "distinct"
    SiteDistinctOn        ReferenceSite = "distinct_on"
    SiteSetOperation      ReferenceSite = "set_operation"
    SiteWindowPartition   ReferenceSite = "window_partition"
    SiteWindowOrder       ReferenceSite = "window_order"
    SiteAggregateFilter   ReferenceSite = "aggregate_filter"
    SiteAggregateOrder    ReferenceSite = "aggregate_order"
    SiteExpressionControl ReferenceSite = "expression_control"
    SiteLimit             ReferenceSite = "limit"
    SiteOffset            ReferenceSite = "offset"
    SiteFetch             ReferenceSite = "fetch"
    SiteDMLValueSource    ReferenceSite = "dml_value_source"
    SiteConflictTarget    ReferenceSite = "conflict_target"
    SiteReturning         ReferenceSite = "returning"
    SiteViewPredicate     ReferenceSite = "view_predicate"
)

type SourceFreeReason string
const (
    SourceFreeLiteral       SourceFreeReason = "literal"
    SourceFreeNull          SourceFreeReason = "null"
    SourceFreeParameter     SourceFreeReason = "parameter"
    SourceFreeCountStar     SourceFreeReason = "count_star"
    SourceFreeCountConstant SourceFreeReason = "count_constant"
    SourceFreePureBuiltin   SourceFreeReason = "pure_builtin"
)

type SourceFreeProof struct {
    Reason       SourceFreeReason
    FunctionID   string            // pure_builtin 时必填，数据库绑定后的身份
    Children     []SourceFreeProof // 参数证明；稳定排序
    RowsetBound  bool              // count(*)/count(constant) 必须为 true
}
```

`model.AST` 新增 `ReferenceLineages []ColumnReferenceLineage`、`Coverage ASTCoverage`；`LineageArm` 新增 `SourceFree *SourceFreeProof`。`LineageSourceFree` 当且仅当证明结构合法、依赖为空、节点类型与 reason 一致。`count(*)`/`count(constant)` 虽无列依赖，仍要求其查询块所有 relation 的表授权。

Catalog 绑定后，字符串来源被替换成稳定列身份：

```go
type RelationIdentity struct {
    Dialect, ServerID, DatabaseID string
    Schema, Name, Kind            string
    StableObjectID                string // PG: dbOID/relationOID；MySQL: server_uuid/db/name/kind
}
type ColumnIdentity struct {
    Relation RelationIdentity
    Ordinal  int    // PG attnum；MySQL ORDINAL_POSITION
    Name     string // catalog 真实名字
    TypeID   string // PG type OID/typmod；MySQL 完整 column type + charset/collation
}
type BoundColumnUse struct {
    QueryBlockID string
    Usage        ColumnUsage
    Site         ReferenceSite
    Column       ColumnIdentity
    BindingAlias string // 只用于诊断，不参与授权键
    ViewPath     []ColumnIdentity // 从外层暴露列到内层视图列
}
```

### 2.2 合并规则

- 一个表达式的 value 输入在输出位置要求 `output`；control/filter/group/order 输入要求 `reference`。
- 一个表达式整体被用于 WHERE/ON/GROUP/HAVING/ORDER/DISTINCT/LIMIT 等控制位置时，其所有物理依赖强制转换为 `reference`，不沿用原目标项的 `output` 判定。
- 一个来源、一个集合 arm、一个视图层或一个 usage 缺 grant，整条语句 deny；不删除条件、不裁列、不猜测。
- mask 只发生在已通过 `output` 的最终返回位置；reference 永不通过 mask 获权。
- 所有匹配的 exact/schema/global 表策略先聚合，任一 deny 永远胜出；不存在“更具体 allow 覆盖宽 deny”。表 allow 只解决表可达性，不能覆盖具体 relation 的列约束。
- 对一个 relation，只要存在有效 column policy 或与该 relation 相关但无法绑定/不一致的 policy，它就是 constrained；后者直接使 datasource/policy snapshot unhealthy，绝不能落入“无列策略，全列 allow”。

## 3. 封闭 AST visitor 契约（P0-1）

### 3.1 覆盖证明

双方言 parser 必须输出 `ASTCoverage`：每个 AST 节点记录 `node kind/path -> handled category | unsupported reason`。visitor 的类型 switch/default 分支只能调用 `denyUnsupported(kind,path)`，不能 `return nil` 或继续递归后声称完整。发布测试维护双方言的 node-kind manifest：升级 `pg_query_go`/Vitess 时，出现新 kind 会使测试失败。

`Coverage.Complete` 只有在以下条件全部成立时为 true：唯一根语句已识别；每个 query block 有输出、FROM binding、所有 clause reference；所有子查询/CTE（含未引用的数据修改 CTE）均完成；没有未标记节点；共享资源预算未超限。否则 `AUTH_AST_UNSUPPORTED` 或更具体 reason deny。

### 3.2 双方言共同的 handled 清单

以下节点必须提取依赖并写 site：

| 位置 | 目标行为 |
|---|---|
| SELECT target、表达式、CASE 控制分支 | value→output；控制条件→reference |
| WHERE、JOIN ON | 全部依赖→reference；ON 在该 JOIN 自己的 scope 解析 |
| USING/NATURAL | 每个参与 binding 的同名列→reference；合并列输出另查 output；NATURAL 必须用完整 catalog 列集合 |
| GROUP BY 普通项 | 绑定后全部→reference；同列被输出仍另需 output |
| HAVING | 过滤依赖→reference；聚合参数若仅用于谓词也转 reference |
| ORDER BY | 绑定后全部→reference |
| 窗口 PARTITION/ORDER、frame bound 中表达式 | 全部→reference；窗口函数 value 参数按所在输出/引用上下文传播 |
| aggregate FILTER、普通 aggregate 内 `ORDER BY` | FILTER/ORDER→reference；aggregate value 参数按上下文 |
| DISTINCT / DISTINCT ON | 见第 4 节的双授权规则 |
| LIMIT/OFFSET/FETCH | literal/parameter 为 source-free；表达式或子查询依赖全部→reference |
| 标量/EXISTS/IN/相关子查询 | 子查询所有 query block 完整访问；外层相关列按实际 site→reference |
| CTE、派生表、LATERAL | 沿 route 展开；名字重映射后仍回到绑定列身份 |
| VALUES / DML RHS | 读取型 value source→output；条件与冲突判断→reference |
| RETURNING | 按最终输出处理并生成 mask 计划 |

Alias/ordinal 的固定算法：先按该数据库真实 name-resolution 优先级绑定目标项；再在新 clause site 深复制已绑定依赖，将所有 usage 强制改为 `reference`。适用于 `ORDER BY alias/ordinal`、方言允许的 `GROUP BY alias/ordinal`、MySQL `HAVING alias`、集合顶层排序。无法证明 alias 与物理列的优先级时 `AUTH_ALIAS_BINDING_AMBIGUOUS`。

### 3.3 PostgreSQL 语句/节点清单

**handled：**普通 SELECT；INNER/LEFT/RIGHT/FULL/CROSS；CTE/递归 CTE（只有有限、完整 resolved 才可放行）；derived/LATERAL/subquery；普通 DISTINCT 和 DISTINCT ON；普通 GROUP BY；窗口；aggregate FILTER 与普通 aggregate 内 ORDER BY；LIMIT/OFFSET/FETCH；VALUES；`INSERT VALUES`、`INSERT ... SELECT`；`UPDATE ... FROM`；`DELETE ... USING`；`ON CONFLICT` 的 inference key、`DO UPDATE SET/WHERE`、`excluded` 路由；上述 DML 的 `RETURNING`；数据修改 CTE。数据修改 CTE 即使外层未引用，也完整执行表、列、函数和 statement profile 授权。

**显式 unsupported→deny：**

- grouping sets、`ROLLUP`、`CUBE`（初版不实现多重 grouping membership）；reason `AUTH_GROUPING_SETS_UNSUPPORTED`。
- ordered-set/hypothetical-set aggregate 和 `WITHIN GROUP`；reason `AUTH_ORDERED_SET_UNSUPPORTED`。普通 aggregate 内 ORDER BY 不在此列，必须处理。
- FROM 中 `RangeFunction`/table function、`TableFunc`、目标列表 SRF、`ROWS FROM`；reason `AUTH_TABLE_FUNCTION_UNSUPPORTED`。
- 用户提交的 `EXPLAIN`/`EXPLAIN ANALYZE`、CTAS、`SELECT INTO`、`DECLARE/FETCH/CLOSE CURSOR`；reason 分别为 `AUTH_WRAPPER_UNSUPPORTED`/`AUTH_CTAS_UNSUPPORTED`/`AUTH_CURSOR_UNSUPPORTED`。MCP `explain_query` 传入的是待授权内层 SQL，只有授权完成后系统才生成可信 EXPLAIN。
- `PREPARE/EXECUTE/DEALLOCATE`、`CALL`、`DO`、过程语言块、COPY/外部程序；统一在执行前 deny。
- 多语句永远 deny；不能逐条只授权第一条。
- 不在上面 handled 集合中的 DDL/ADMIN 在 B2 首发均 deny。尤其任何能嵌入查询的 DDL 不能退回现有规则放行。

### 3.4 MySQL 语句/节点清单

**handled：**普通 SELECT；INNER/LEFT/RIGHT/CROSS（FULL 在解析/授权阶段稳定 deny）；CTE、derived、相关子查询及目标版本支持的 LATERAL；DISTINCT；普通 GROUP/HAVING/ORDER（含 HAVING alias）；窗口；LIMIT/OFFSET；VALUES；UNION/INTERSECT/EXCEPT（要求服务端 >=8.0.31）；`INSERT VALUES/SELECT`；单表及 multi-table UPDATE/DELETE；UPDATE join；`ON DUPLICATE KEY UPDATE` 的输入新值、旧目标值、assignment RHS；上述语句中所有子查询。MySQL 没有 PostgreSQL 形式的 DML `RETURNING`，解析器若接受扩展语法仍按 unsupported deny。

**显式 unsupported→deny：**

- `WITH ROLLUP`、任何 cube/grouping-set 扩展；reason `AUTH_GROUPING_SETS_UNSUPPORTED`。
- `JSON_TABLE`、table function、UDF table source；reason `AUTH_TABLE_FUNCTION_UNSUPPORTED`。
- `REPLACE`、`LOAD DATA/XML`、`HANDLER`、用户变量赋值/动态 SQL；初版 deny，不能把它们误归为 INSERT。
- 用户 `EXPLAIN`、CTAS、PREPARE/EXECUTE/DEALLOCATE、CALL、存储程序、游标、事件语句、多语句均 deny。
- `FULL JOIN` 在调用 executor 前以 `AUTH_DIALECT_UNSUPPORTED` deny。

### 3.5 DML 读取语义

- INSERT/UPDATE assignment RHS、`INSERT ... SELECT` 分支、`ON CONFLICT DO UPDATE`/`ON DUPLICATE KEY UPDATE` 读取的源列至少需要 `output`；其中 CASE 条件、查找谓词、冲突 key、UPDATE FROM/DELETE USING join 条件需要 `reference`。
- `target.col = target.col + 1` 读取旧值，因此旧 `target.col` 需要 output；纯写目标列的 write-column grant 仍非 B2 范围。
- RETURNING 表达式按 SELECT 输出处理，同时使用相同 mask plan。任何 RETURNING 失败使 DML 回滚。
- 数据修改 CTE 的写动作遵循 Agent profile/表策略；其输入和 RETURNING 即使未被外层消费也必须授权。无法给某 DML 节点建立完整 source graph 时拒绝该形态。

## 4. DISTINCT、集合运算和 alias 的双授权（P0-2/P0-8）

规则冻结如下：

| 构造 | output | reference |
|---|---|---|
| 普通 SELECT 项 | value 来源需要 | 仅其 control/filter/order 来源需要 |
| `SELECT DISTINCT x...` | 每个返回位置的 value 来源需要 | 每个 distinct key 的同一来源另需 `SiteDistinct` |
| `DISTINCT ON (k...)` | 实际投影照常需要 | ON key 全部需 `SiteDistinctOn`；ORDER BY 另记 site |
| `UNION ALL` | 每个分支位置需要 | 不因 UNION ALL 本身增加 |
| `UNION`（非 ALL） | 每个分支位置需要 | 每个比较位置、每个分支同一来源另需 `SiteSetOperation` |
| `INTERSECT` / `EXCEPT`（含 ALL） | 每个分支位置需要 | 成员/计数比较使用真实值，所有分支另需 `SiteSetOperation` |

`SELECT DISTINCT masked_col` 只有 output 无 reference 时必须 deny；不能因为最终 mask 而允许数据库先按明文去重。集合输出的多个 arms 先逐 arm 验证 usage，再按 deny > mask > allow 合并。集合顶层 alias/ordinal 排序必须生成新的 `SiteOrderBy` reference，而不是复用 output 结果。

## 5. source-free、函数、operator 与 cast（P0-3）

### 5.1 结构化证明

合法 source-free 仅包括：字面量、NULL、bind parameter、经过验证的纯内建函数对 source-free 参数的组合、`count(*)` 和 `count(source-free constant)`。`SourceFreeProof` 与 AST node kind、参数个数、函数身份、rowset 绑定不一致即 `AUTH_SOURCE_FREE_INVALID`。

零参数函数不自动 source-free。序列、当前用户、读取 session/系统状态、security-definer、volatile/stable 或可能访问 SQL 的函数均不在 allowlist。视图定义也使用同一规则。

### 5.2 首发纯内建 allowlist

allowlist 是代码中的版本化表，不由管理员配置。PostgreSQL 项按 `pg_catalog` OID + 精确参数/返回 type OID 绑定；MySQL 项按服务端版本、内建语法 token、精确 arity 绑定，禁止同名 UDF。首发最小集合：

- 字符：`lower(text)`、`upper(text)`、`length/char_length/character_length`、`octet_length` 的内建标量签名；MySQL 另允许内建 `concat/concat_ws`。
- 数值：内建 `abs`、`ceil/ceiling`、`floor`、`round` 的数值签名。
- 聚合：内建 `count`、`sum`、`avg`、`min`、`max` 的原生标量类型签名；aggregate value/filter/order 仍按第 3 节采集。
- SQL 结构节点 `COALESCE`、`NULLIF`、CASE、布尔连接不是“按名字信任的函数”，由 visitor 逐子表达式处理。

未列入的 built-in 也先 deny，扩 allowlist 必须有“无数据库对象读取/无用户回调”的证明与双方言真库测试。PostgreSQL operator 必须绑定到 `pg_catalog.pg_operator` 的精确 OID；首发只允许内建布尔、比较、数值算术和内建文本连接签名。cast 必须绑定到版本化的 `pg_cast` 内建 source/target OID 对且 cast function 属于 `pg_catalog` allowlist；MySQL 只允许语法内建的标量 CAST 类型集合。用户定义函数、UDF、表函数、用户 operator、domain/user type cast 一律 `AUTH_OPAQUE_EXECUTABLE`。

## 6. 视图/物化视图的双层授权（P0-4）

对查询中的 `v.c`，传入 usage 为 U，顺序固定：

1. 检查视图 `v` 自身表策略；检查视图暴露列 `v.c` 的 U grant。
2. 展开该输出列的绑定 lineage。若 U=output，value 依赖向内传播 output，控制依赖传播 reference；若 U=reference，所有向内依赖都传播 reference。
3. 检查视图定义自己的 WHERE/JOIN/GROUP/HAVING/ORDER/DISTINCT/LIMIT 等内部引用，均为 reference。
4. 每遇到下一层视图，重复“该层自身表/列 + 再展开”；到底表后继续检查底表表/列。
5. 任一层 policy 缺失、定义不完整、函数 opaque、环、超限或 dependency 无稳定绑定即 deny。

PostgreSQL materialized view 正式采用同一双重模型：**物化视图自身表/列 grant + 其保存定义中所有底层表/列 grant**；refresh 时点不改变授权。MySQL 原生没有 materialized view，不模拟此 kind。MySQL `SQL SECURITY DEFINER/INVOKER` 和 PostgreSQL view owner/security_invoker 只影响数据库原生权限，不降低 AgentSQL 双层检查。

## 7. 每请求 Catalog 绑定与稳定身份（P0-5/P0-7）

### 7.1 接口

现有 `Executor/Session` 不能满足契约。新增的拟议端口必须由授权执行层独占：

```go
type AuthorizationCatalog interface {
    Bind(ctx context.Context, roots []RelationRef, budget CatalogBudget) (CatalogSnapshot, error)
}
type CatalogSnapshot struct {
    Version       CatalogVersion
    Relations     []RelationMetadata
    Complete      bool
    InputCount    int
    Digest        [32]byte
    DefinitionCtx DefinitionContext
}
type RelationMetadata struct {
    Requested      RelationRef
    Identity       RelationIdentity
    Columns        []ColumnMetadata // 严格按 ordinal；无洞/重复
    Definition     *BoundDefinition
    Dependencies   []BoundDependency
    Complete       bool
}
```

每个输入 root 恰有一个结果；结果多、少、重复、列序号不连续、definition 被截断、dependency 未绑定或 `Complete=false` 都返回稳定 deny，而不是部分成功。

### 7.2 PostgreSQL

- `ServerID` 取 PostgreSQL system identifier，`DatabaseID` 取当前 database OID；relation identity 为 database OID + `pg_class.oid` + `relkind`。
- 列 identity 使用 `pg_attribute.attnum/attname/atttypid/atttypmod/attcollation`，忽略 dropped column 但保留 ordinal 身份。
- view/matview 定义必须返回 `pg_rewrite` `_RETURN` rule OID、规范定义、`pg_depend` 的 `refobjid/refobjsubid` 依赖元组；AST 中每个 RangeVar/ColumnRef 必须与 dependency OID/attnum 对上。仅把 `pg_get_viewdef` 文本重新按当前 search_path 解析不构成绑定证明。
- 记录创建后数据库绑定的 operator/function/type OID；当前 `search_path` 只用于绑定用户提交的未限定 root，不得重解释已创建视图的依赖。

### 7.3 MySQL

- B2 只在 MySQL >=8.0.31、`gtid_mode=ON`、binary log 开启且执行账户具备 `BACKUP_ADMIN` 时 healthy。
- identity 为 `@@server_uuid + 按 @@lower_case_table_names 规范化后的 database/name + kind`。MySQL 没有可依赖的公开 relation OID，因此这是明确的**规范名称身份**。
- `CatalogVersion` 带 `@@GLOBAL.gtid_executed`；后一次必须是前一次的 superset，检测 reset/restore/倒退即 datasource unhealthy。GTID 是单调观察版本，不替代对象锁。
- 返回 `INFORMATION_SCHEMA.COLUMNS` 的 `ORDINAL_POSITION`、完整 type/charset/collation；视图返回完整 `SHOW CREATE VIEW`/`INFORMATION_SCHEMA.VIEWS` metadata、definer/security、`CHARACTER_SET_CLIENT`、`COLLATION_CONNECTION`、创建 SQL mode、当前 database、`lower_case_table_names`。定义中的每个 relation 再按同一规范身份递归绑定。
- MySQL 同名 drop/recreate 且定义完全相同在数据库公开接口上不可区分，因此策略身份语义明确为“规范名称继承”；只要可观察 fingerprint 改变就转 `needs_rebind`。rename 不继承到新名字。此限制在 UI/API 明示，不能伪称存在 OID。

### 7.4 policy identity 与变更语义

- policy 创建/迁移必须通过 catalog 绑定 `relation_binding_id`，不能只保存 `object_name` 字符串。查询也先绑定 identity，再查 grant。
- PostgreSQL relation rename/column rename保留 OID/attnum，policy 随对象身份继承并更新显示名；drop/recreate OID 改变，不继承，datasource `needs_rebind`。
- MySQL relation rename不继承；同规范名称 drop/recreate按上一节的名称身份继承，但 fingerprint 有可观察变化时必须管理员显式确认 rebind；列 grant 同时比较 ordinal、真实 name 和 type digest，任何不一致先 unhealthy。
- 任一已有 policy 无法绑定、同一 binding 的策略元数据冲突、或相关 unresolved policy 存在时，整个 datasource 的授权状态为 unhealthy。它不会被忽略为“没有列策略”。

## 8. TOCTOU、事务、结果与 mask 协议（P0-6/P0-9）

### 8.1 公共协议

`AuthorizedExecute` 的线性流程如下；步骤不可交换：

1. 在控制面同一 snapshot 读取 Agent、datasource、父子 policy、policy revision、compat fence、mask rules revision；静态语法/profile 明确 deny 时业务 DB 调用为 0。
2. 打开**请求级物理 session**，即使调用方没有 `SessionID`。建立可回滚事务；已有用户 transaction 时建立内部 savepoint，aborted/unknown transaction state 直接 deny。
3. 获取方言 schema 稳定锁；在锁内取 pre-execution catalog snapshot `Fpre`，完成绑定、视图展开和最终授权。
4. 在执行前编译唯一不可变 `ProtectionPlan`。plan 含对齐后的输出位置、算法、rule revision、catalog digest、policy revision 和 `PlanDigest=SHA-256(canonical plan)`。若有任一 mask 而 redactor 不实现新 `PlannedRedactor` 能力，deny，不执行 SQL。
5. 授权通过后才允许可信 EXPLAIN/动态规则。用户提交 EXPLAIN 已在 visitor 阶段拒绝。
6. 在同一 session/事务/锁内执行用户 SQL，将结果完整收进有界 buffer；不把 row channel 暴露给 REST/MCP/控制台。
7. 校验结果矩形、列数/顺序/type 与 plan；用同一个 plan 对 buffer mask，redactor 返回相同 `PlanDigest`。禁止重新计算决策或退回 `DirectProjections`、`SourceAwareRedactor`、`Redactor.Apply`。
8. 锁仍持有时重新绑定得到 `Fpost`。对象/列/定义/dependency digest 必须与 `Fpre` 相同；版本必须单调合法。任何 mismatch、catalog 错、mask 错、schema 错、buffer 超限都清空 buffer，并 rollback 到事务/保存点。
9. 写操作先写 intent/outcome audit 屏障再 commit，沿用当前 `executeTransactionalWrite` 的 fail-closed 意图；读操作最终 audit 成功后才把 Response 交给 transport。所有路径在返回前释放锁/结束事务。
10. HTTP/MCP encoder 只能接收已经 sealed 的 `AuthorizedResult`，没有流式 iterator。首字节、header flush、SSE row event 均禁止发生在第 9 步之前。

ABA 处理不是“前后内容 hash 相同就算安全”：关键窗口内对象锁禁止 DDL；PostgreSQL OID/attnum/dependency identity、MySQL backup lock + monot调 GTID 另作证据。锁能力无法证明时 datasource unhealthy。

### 8.2 PostgreSQL 锁协议

- 开事务后先解析 roots，按稳定排序执行 `LOCK TABLE <qualified root> IN ACCESS SHARE MODE`；锁 view 时 PostgreSQL 会递归锁 view 定义中的 relation，锁持有到事务结束。实现仍需枚举依赖并核对 `pg_locks`/catalog 完整性。
- 锁前可取候选 snapshot `F0`，锁后必须重取 `Fpre`；不一致最多重试一次，第二次 `AUTH_CATALOG_RACE`。授权只基于锁后的 `Fpre`。
- DML 后续在同一事务取得自身需要的 RowExclusive 等锁；自身锁模式兼容。B2 首发拒绝用户 DDL，因此不会出现“用户 SQL 合法改变被保护 fingerprint”。
- 该协议依据 PostgreSQL `LOCK TABLE` 的事务持有与视图递归锁语义；实现测试覆盖 14–18。

### 8.3 MySQL 锁协议

- 在 catalog 读取前执行 `LOCK INSTANCE FOR BACKUP`；它允许 DML但阻止非临时对象 DDL。临时表/临时 view 一律不在 B2 支持范围。
- 持有 backup lock 后 `START TRANSACTION`（已有 transaction 则 savepoint），再取 `Fpre`、授权、执行、缓冲、`Fpost`、commit/rollback，最后 `UNLOCK INSTANCE`。`LOCK INSTANCE` 不被当成用户可提交语句。
- `Fpost` 的 GTID set 必须包含 `Fpre`；其他连接的普通 DML可让 GTID 前进，不要求相等，但 relation fingerprint 在 backup lock 下必须相等。
- `BACKUP_ADMIN`、GTID、锁等待超时或 unlock 状态无法验证时 fail-closed；绝不退化成“同连接 + 两次 hash”。

### 8.4 ProtectionPlan 接口

```go
type ProtectionPlan struct {
    ID, Digest, PolicyRevision, MaskRevision, CatalogDigest string
    Columns []ProtectedOutput
}
type PlannedRedactor interface {
    ApplyPlan(model.QueryResult, ProtectionPlan) (model.QueryResult, mask.RedactReport, string, error)
}
```

从 `internal/mask/lineage.go` 抽出纯 planner，`decideProjectionColumn/decideLineageArm` 的现有安全格只保留一个实现。任一 mask 能力缺失、plan digest 不同、算法 key 不可用、列 type/数量/顺序不符、Apply 报错都返回 `AUTH_MASK_PLAN_FAILURE`，丢弃全部结果。对无 mask 的查询也校验 catalog/result schema digest，防止同名改绑。

## 9. 存储、迁移、版本 fence 与 API（P0-10～P0-13）

### 9.1 目标表

迁移文件为 combined SQLite/PG `0009_column_authorization.sql`，separate metadata SQLite/PG `0008_column_authorization.sql`。至少新增：

```text
relation_policy_bindings
  id, datasource_id, dialect, server_id, database_id,
  schema_name, relation_name, relation_kind,
  stable_object_id, binding_fingerprint, binding_revision, status

policy_column_permissions
  policy_id FK policies(id) ON DELETE CASCADE
  column_name (原样保存)
  column_ordinal
  usage CHECK output|reference
  PRIMARY KEY(policy_id, column_ordinal, usage)

control_plane_compat
  singleton_id=1, min_reader_protocol, min_writer_protocol,
  usage_mode=symmetric|asymmetric, revision

runtime_instances
  instance_id, reader_protocol, writer_protocol, heartbeat_at, expires_at
```

`policies` 增加 `relation_binding_id`、单调 `revision`。repository 每次 mutation 在同一事务检查 `min_writer_protocol`、`If-Match revision`、binding 状态、父子完整性并 `revision=revision+1`。

### 9.2 原子迁移与父子一致性

- 扩展 `applyMigrationWithOptions` 为 typed migration：取得 PostgreSQL advisory xact lock/SQLite 独占写事务后，在**同一事务**依次执行 version claim、legacy preflight、DDL、CSV backfill、约束/触发器安装、回填计数校验、schema version claim commit；任一步失败整体 rollback。不得在事务外先查再迁移。
- preflight 拒绝：column policy 缺 columns、空 token、非法 action/object、重复规范化 token、非法 `*` 状态；错误列出 policy ID，不静默跳过。
- PostgreSQL/SQLite 都加触发器：子项只能挂在 `object_type='column' AND action='allow'` 父项；有子项时父项不能改为非法类型/action；父 revision 更新与子替换同事务。生产 DB role 撤销对表的直接写权限，只授予 repository 使用的受限路径。
- runtime 用同一 metadata snapshot 批量读父/子/binding/revision；逐项检查父类型、action、FK、usage、ordinal/name、revision 和计数。任何孤儿、重复、不一致使 datasource unhealthy，不当作空 ACL。
- CSV backfill仅按旧契约 `split(',') + trim` 解释；旧格式从未能表示包含逗号或首尾空格的单个 identifier。B2 激活后子表保存真实 quoted identifier，允许逗号/空格，**停止写 `policies.columns`**。response 只在 output 集合可无损表示时合成 legacy 字段，否则省略并置 `legacy_unrepresentable=true`。

### 9.3 滚动升级与安全回退

协议级别冻结：legacy=1，bridge=2，B2=3。

1. 先部署 protocol 2 bridge 到所有实例。它能读无/有新表、能检查 fence；只允许对称 usage 写入，不启用 B2 放行。
2. 执行原子迁移，`min_reader=min_writer=2, usage_mode=symmetric`；回填每个 legacy 列为 output+reference。
3. catalog binding 工具在 schema 事务之外逐 datasource 获取带锁 snapshot，在 metadata 事务内按 fingerprint compare-and-set 写 binding；任何失败保留 `needs_rebind`。生成 grandfathered `*`、unbound policy、不可表示 legacy 数据阻断报告。
4. 所有 live instance 升到 protocol 3 且报告 healthy，且报告中 `*` 为 0、binding 全 healthy 后，单事务把 `min_reader=min_writer=3, usage_mode=asymmetric`。从此旧进程启动/ready/write 均失败。
5. 非对称 usage 激活后禁止回到 protocol 1/2。唯一安全回退是 protocol 3 的 B2-safe fallback binary：仍理解子表、强制门、catalog/锁、usage 和 plan，只关闭新 UI/管理功能。若强制门不可用，阻断相关 datasource/agent，不放行。

legacy CSV 在 bridge 阶段仅同步 output 集合；reference-only 绝不写入 CSV。激活后彻底停止 CSV 写入。数据库 fence 保证任何会把 output-only 当 both 的旧 binary 无法服务。

### 9.4 API、ETag 与 `*`

- 现有 `/api/v1/policies` 四个 endpoint 扩展 `column_permissions`、canonical binding view、`revision`；GET/单项 response 发送 `ETag: "policy-<id>-r<revision>"`。PUT/DELETE 必须带 `If-Match`，缺失 428，过期 412。
- legacy POST/PUT 仅带 `columns` 时，新建可映射为对称 output+reference；若当前 policy 已有任何非对称 usage，legacy PUT 返回 **409**；新旧字段语义冲突返回 **422**，不能取并集。
- 所有写入口统一拒绝新建 `column='*'`：REST、UI、discovery apply、seed/import、CLI、repository direct service。grandfathered `*` 只读且在激活前必须显式展开为 catalog 当时完整精确列集合；展开工具带 fingerprint/ETag，目录变化则重做。
- 激活前若仍存在 grandfathered `*`，发布闸门失败。其过渡读取语义必须诚实：`(*,usage)` 授予该 usage 的所有列，精确 grant 的缺失不能覆盖它为 deny。
- policy create/update 必须在线 catalog bind；无法 bind 返回 409/422 并把 datasource health 原因记录为稳定码，不保存游离字符串策略。

## 10. 唯一 `AuthorizedExecute` choke point（P0-14）

### 10.1 目标接口与封装

新增唯一业务接口：

```go
type AuthorizedExecutor interface {
    AuthorizedExecute(context.Context, pipeline.Request) (pipeline.Response, error)
}
```

现有 `Pipeline.Process`/`ProcessDemo` 只可成为该接口的薄适配器或被其取代。业务 package 不再拿到 `executor.Executor/Session/WriteTx`；`bootstrap.Runtime.ExecutorFor` 删除或降为授权执行 package 内部不可导出依赖。CI 加 import/lint 规则：除 executor、authorizedexecute 和强类型 internal DB ports 外，`.Query/.Execute/.Explain/OpenSession` 调用失败。

### 10.2 当前入口清单与目标路由

| 当前入口 | 真实文件 | v2 路由 |
|---|---|---|
| MCP query | `internal/mcpserver/handlers.go` | AuthorizedExecute(mode=query) |
| MCP execute_write | 同上 | AuthorizedExecute(mode=write) |
| MCP explain_query | 同上 | 先授权 inner SQL，再由系统生成 Explain |
| MCP request_approval | 同上 | 同一授权 snapshot；deny 不进审批 |
| HTTP/stdio MCP transport | `internal/mcpserver/http.go`, `tools.go` | 只能调用上述 handler |
| 管理控制台 demo run | `internal/adminapi/playground.go` | AuthorizedExecute(mode=demo)，同列拒绝原因 |
| session 请求 | `pipeline.Request.SessionID` | 同一门；事务/savepoint 协议不得旁路 |
| StaticAssess | `internal/pipeline/static_assess.go` | 明示 `column_auth=not_evaluated`，无执行能力 |
| MCP list_schema | `internal/mcpserver/handlers.go` | 强类型 `AuthorizedSchemaList`，复用 canonical policy snapshot/catalog，不接受 SQL |
| discovery sample | `internal/controlledread/service.go` | 管理员专用 typed `InternalSampleRead`；固定生成器、独立管理审计、无 caller SQL |
| datasource ping | `internal/adminapi/handler.go` | `Executor.Ping` 的 typed health port，不再 `SELECT 1` 通用查询 |

若实施时发现其他 raw SQL 入口，发布自动失败；不能标成“不支持 B2”后继续执行。未来 console/REST raw SQL 也必须依赖 `AuthorizedExecutor`。

## 11. Catalog/视图资源预算与完整性（P0-15）

所有限制按一个请求共享（用户 AST + 所有视图定义），不是每层重新计数：

| 资源 | 硬上限 |
|---|---:|
| 唯一 relation（root+view+base+matview） | 256 |
| 视图/物化视图最大深度 | 16 |
| 视图展开递归轮次 | 16 |
| catalog 往返次数 | 32 |
| 累计 definition SQL UTF-8 bytes | 1 MiB |
| 累计列 metadata 数 | 16,384 |
| catalog 行数 | 50,000 |
| 原始 catalog 响应 bytes | 8 MiB |
| AST 节点 | 50,000（沿用并共享） |
| 投影 | 4,096（沿用并共享） |
| 输出+引用依赖总数 | 16,384（不是各 16,384） |
| 完整结果 buffer | 16 MiB 且受 datasource row_limit |
| catalog+lock wall time | `min(1s, max(200ms, stmt_timeout/4))` |

超限 reason 分别稳定为 `AUTH_RELATION_LIMIT`、`AUTH_VIEW_DEPTH_LIMIT`、`AUTH_DEFINITION_BYTES_LIMIT`、`AUTH_COLUMN_LIMIT`、`AUTH_CATALOG_RESPONSE_LIMIT`、`AUTH_CATALOG_TIMEOUT` 等。循环不是等到深度耗尽，而是在 identity visited set 首次重复时 `AUTH_VIEW_CYCLE`。

Catalog 每一轮验证 request/result 一一对应、完整标记、序号、总数、checksum；部分成功、重复对象、缺列、截断、权限不足统一 deny。固定“最多两个查询”从验收删除；以 32 硬上限和双方言实测 typical round trips 作为指标。

## 12. 默认性能预算（按每请求 Catalog 重订）

旧 v1 的“显式 resolved JOIN 无目录 I/O、完整 fake pipeline P99<5ms”不再是生产热路径目标。默认发布预算：

- 静态语法/profile deny：P99 <5ms，business DB open/catalog/explain/query/execute 全为 0。
- 1–8 个普通 relation、同地域 DB：授权 lock+catalog P50 ≤20ms、P95 ≤75ms、P99 ≤200ms。
- 深度 ≤4 的视图链/NATURAL/star：lock+catalog P95 ≤150ms、P99 ≤400ms。
- 授权 CPU（不含 DB RTT/等待）P99 ≤5ms；policy/lineage 查找近线性。
- MySQL backup lock 和 PostgreSQL relation lock 等待包含在 catalog wall budget；超时 deny，不延长到用户 statement timeout。
- 发布压测以真实 PostgreSQL/MySQL catalog 路径为默认；内存 fake 只用于回归，不可用于宣称生产 SLO。

上述延迟值是默认 gate；部署实测可以收紧，不能放宽安全 hard timeout 而不经设计评审。

## 13. 审计契约

在现有 `model.AuditLog.DetailsJSON` 的 `column_auth.version=2` 写入：policy/mask revisions、`PlanDigest`、catalog pre/post digest、lock mode、完整性/预算计数、每个 output/reference 的 identity/ordinal/usage/site/view path、decision、reason、policy ID。不得写值、谓词常量、未 mask 内容或 view SQL。

稳定 reason 至少覆盖：`TABLE_DENY`、`TABLE_ALLOW_MISSING`、`POLICY_UNHEALTHY`、`COLUMN_USAGE_MISSING`、`LINEAGE_AMBIGUOUS`、`AST_UNSUPPORTED`、`SOURCE_FREE_INVALID`、`OPAQUE_EXECUTABLE`、`CATALOG_INCOMPLETE`、`CATALOG_MISMATCH`、`CATALOG_RACE`、`MASK_CAPABILITY_MISSING`、`MASK_PLAN_MISMATCH`、各资源超限。

明细最多 256 项、JSON 最多 32 KiB；优先保留 deny，其他稳定汇总，并写被截断 canonical detail 的 SHA-256。`truncated=true` 不影响授权完整性，只影响审计展示。

## 14. 测试与发布验收

### 14.1 Parser/visitor

- 双方言 node manifest/coverage golden：每个 expression-bearing node 为 handled 或 unsupported；default visitor 分支必 deny。
- 覆盖 DISTINCT ON、DISTINCT、所有 set op、LIMIT/OFFSET/FETCH、普通/grouping sets、aggregate ORDER BY/ordered-set、窗口、table function/SRF/JSON_TABLE、所有 DML source、UPDATE FROM/DELETE USING/conflict/duplicate/RETURNING、未引用修改 CTE、wrapper、多语句。
- alias/ordinal/HAVING alias 必须生成新 reference site；物理列与 alias 优先级歧义 deny。
- source-free 防伪：未知零参函数、security-definer、用户 function/operator/cast 即使零 dependency 也 deny。
- fuzz：不 panic；新增节点、依赖、deny 或视图层不能把决策放宽。

### 14.2 Authorizer/mask

- 决策优先级全矩阵；exact/schema/global 任一表 deny；unbound policy 不得变成 unconstrained。
- output-only/reference-only/both；DISTINCT/set op 双授权；UNION ALL 仅 output。
- 普通/物化视图每层 exposed ACL 与底层 ACL，内部 predicate reference。
- 同一 `ProtectionPlan`/digest 被 planner 和 redactor 使用；legacy redactor、类型/顺序错误、mask 错均零结果。

### 14.3 真库 TOCTOU/e2e

- PostgreSQL 14/18 完整，15/16/17 安全冒烟；MySQL 8.0.31 及当前支持的最新 8.0 小版本完整。
- 并发 ALTER VIEW、CREATE OR REPLACE、DROP+CREATE、rename、列 rename/type change；断言锁阻塞或请求 deny，不能返回一行。PG 验 OID/dependency，MySQL 验 backup lock/GTID 单调。
- DML mismatch、mask failure、postcheck failure、audit failure一律 rollback；验证目标表无变化。
- 超宽表、深 view、环、catalog 部分结果/重复/截断/超时均稳定 reason。
- PostgreSQL/MySQL JOIN/self-JOIN、USING/NATURAL/star、CTE/derived/lateral/scalar、DML source、所有 set op；物化视图仅 PG。

### 14.4 跨入口同因测试

同一组负向 SQL逐一从 MCP query、execute_write、explain_query、request_approval、HTTP/stdio transport、demo run、带 SessionID 路径提交；断言 decision、DB 调用阶段、reason、catalog/mask/audit一致。`list_schema` 不能展示未授权 view exposed 列；discovery/ping只能运行固定 typed 操作。静态 deny断言 open/catalog/explain/query/execute 全 0；catalog deny只允许 lock/catalog。

### 14.5 控制面

- SQLite/PG combined 8→9、metadata 7→8；claim/preflight/DDL/backfill/trigger/版本同事务故障注入。
- 父子非法 direct write、revision mismatch、读取半快照、迁移并发全部 fail-closed。
- protocol 1/2/3 滚动升级、live lease、asymmetric activation、旧 writer/reader启动阻断、B2-safe fallback 演练。
- legacy GET→PUT 非对称返回409；缺/旧 ETag；所有入口 `*` 拒绝；grandfathered 展开与上线阻断。
- quoted comma/leading-space identifiers只走新子表，legacy response 标 unrepresentable。

## 15. 更新后的实现切片、验收与回退

顺序冻结为 `S1 || S2 -> S3 -> (S4 + S5 同一发布单元) -> S6 -> S7 -> S8`。

### S1：规范化权限、identity、迁移与版本 fence（扩展原 S1）

内容：0009/0008 表和触发器；父子 repository 事务；revision/ETag；relation binding；protocol fence/live lease；CSV 停写计划；SQLite→PG manifest。  
验收：第 14.5 全部；binding 失败 datasource unhealthy；无 `*` 才能 activate asymmetric。  
回退：激活前可回 protocol 2 bridge，保留 additive 表；激活后只能回 protocol 3 safe fallback，绝不回 legacy。

### S2：封闭 visitor、source-free 与完整引用模型（重写原 S2）

内容：双方言 coverage manifest；全部 site；DML/集合/alias；handled/unsupported 表；共享预算。  
验收：第 14.1；B1 projection golden可按新增证明字段更新，但旧安全语义不放宽。  
回退：切片未接执行门，可回旧 parser binary；不得单独启用 B2 gate。

### S3：纯 authorizer + 共享 ProtectionPlan（扩展原 S3）

内容：稳定 identity grant index；视图边界传播；DISTINCT/set 双 usage；抽取 B1 planner；`PlannedRedactor`。  
验收：第 14.2、单调性性质测试、plan digest golden。  
回退：未接执行门时可停用新模块；已接门后回退必须连同 S4/S5 整体回 safe fallback。

### S4：唯一 AuthorizedExecute 与静态门（扩展原 S4）

内容：收口所有业务入口；移除 Runtime 通用 executor 暴露；typed ping/schema/discovery；static deny 零 DB。  
验收：跨入口同因、import/lint 禁直调、R010/global rules disabled/RequireApproval/ExplainOnly 均不能绕过。  
回退：与 S5 同一 release gate；不能只启 S4 后把 catalog pending 放行。

### S5：每请求 Catalog、对象锁、事务与全缓冲（扩展原 S5）

内容：PG OID/dependency catalog和递归 lock；MySQL backup lock/GTID/context；view/matview；pre/post fingerprint；DML rollback；buffer limits。  
验收：第 14.3、资源边界、真实 RTT；不满足 MySQL prerequisites 时 unhealthy。  
回退：S4+S5整体切回 protocol 3 safe fallback；不能退到 R010。

### S6：API/UI/审计（扩展原 S6）

内容：permissions DTO、ETag、409/422、`*` migration、health/report；column_auth v2 与 UI。  
验收：有损 round-trip、审计 digest/截断、旧审计 JSON、无值泄漏。  
回退：可隐藏新 UI，但 API fence、子表语义、强制门和审计 reason不得关闭。

### S7：双方言与所有入口安全总验收（扩展原 S7）

内容：完整逃逸语料、并发 DDL、DML rollback、entry parity、版本矩阵。  
验收：第 14 节全绿；任何“暂不支持”的入口必须被禁用且有测试。  
回退：不发布流量；测试失败不以 waiver 放行。SQLite只测控制面，不冒充第三 query dialect。

### S8：性能、升级与运维发布闸门（扩展原 S8）

内容：真实 catalog SLO、锁争用、宽/深压力、dry-run 新增 deny、滚动升级/fallback/runbook/告警。  
验收：第 12 节预算、protocol 3 activation checklist、`*`=0、binding healthy、MySQL capability healthy。  
回退：执行 safe fallback 演练；若回退不能保持授权上界则阻断 datasource，而非接受风险继续。

## 16. P0-1～P0-15 关闭矩阵

| P0 | 关闭方式 |
|---|---|
| P0-1 | 第 3 节建立双方言封闭 visitor、完整 handled/unsupported 清单与 coverage manifest，补齐所有指定 clause、DML 和 wrapper。 |
| P0-2 | 第 4 节冻结 DISTINCT/非 ALL 集合 output+reference、UNION ALL仅output。 |
| P0-3 | 第 5 节引入结构化 SourceFreeProof、最小纯内建 allowlist，用户 executable 默认 opaque deny。 |
| P0-4 | 第 6 节对每层视图/物化视图执行 exposed column ACL + 底层 lineage ACL 双重授权。 |
| P0-5 | 第 7 节要求 PG OID/attnum/dependency 与 MySQL规范 identity/列序号/创建上下文/GTID，不再信 ViewSQL 文本。 |
| P0-6 | 第 8 节用对象锁、可回滚事务、全缓冲、postcheck 和稳定身份/单调版本关闭 TOCTOU/ABA。 |
| P0-7 | 第 0/7/12 节选定每请求 catalog 绑定并按真实 DB 热路径重订延迟预算。 |
| P0-8 | 第 3.2/4 节要求 alias/ordinal 绑定后在新 clause site复制并强制 reference。 |
| P0-9 | 第 8.4 节要求执行前 PlannedRedactor 能力、同 plan digest；失败丢弃全部结果且禁流式首字节。 |
| P0-10 | 第 9.3 节定义 reader/writer protocol fence、bridge升级、CSV停写和 protocol 3安全回退。 |
| P0-11 | 第 9.4 节定义非对称 legacy PUT=409、所有写入口禁`*`、ETag/revision和`*`上线阻断/展开工具。 |
| P0-12 | 第 7.4/9.1 节把 policy 绑定 canonical identity，unbound即unhealthy，所有表deny优先并冻结rename/recreate语义。 |
| P0-13 | 第 9.2 节把 claim/preflight/DDL/backfill放进同一锁/事务，增加触发器和runtime父子/revision校验，并停止CSV表示新 quoted identifier。 |
| P0-14 | 第 10/14.4 节建立唯一 AuthorizedExecute、完整入口表、typed internal ports和逐入口同因e2e。 |
| P0-15 | 第 11 节给出关系/深度/定义字节/列/响应/轮次/wall time硬上限与逐请求一一完整性确认。 |

## 17. 剩余待确认项（不影响安全默认）

以下仅是部署/容量事实待确认，不再是语义选择：

1. 目标 MySQL 环境是否全部满足 8.0.31+、`BACKUP_ADMIN`、GTID 和 binary log；不满足的 datasource 按本设计保持 unhealthy，需升级环境而非降级协议。
2. 目标 PostgreSQL/MySQL 业务账号是否能读取所需 catalog/definition/context；权限不足同样 unhealthy。实施 spike 必须列出最小 DB grants。
3. 第 12 节默认 P50/P95/P99 在实际部署网络的基线数值；安全 hard timeout 和 fail-closed 行为已冻结，只允许实测后收紧或走正式设计变更。
4. 当前代码探查未发现 MCP、demo run、typed discovery/ping 之外的任意 SQL 入口；实施时 CI 全仓依赖扫描必须再次确认生成代码、未来 REST/console 和运维任务没有新增旁路。

不再待确认：物化视图必须双重授权；catalog 使用每请求绑定；SQLite不是第三查询方言；MySQL最低版本为8.0.31；非对称权限激活后不得回退到旧二进制。


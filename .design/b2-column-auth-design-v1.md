# AgentSQL v0.4 B2 多表 JOIN / 自连接列级授权——详细设计 v1

状态：设计稿 v1（仅设计，不包含实现）  
日期：2026-09-24  
工作目录：`D:\ruanjiansheji\agentsql-v04`

## 0. 结论先行

B2 不应继续把列授权塞进可配置的内置规则 `R010`。建议新增一个**不可关闭的强制授权门**，复用 B1 的 `model.ProjectionLineage` 作为输出血缘，并补充一份覆盖 `WHERE / JOIN ON / USING / GROUP BY / HAVING / ORDER BY` 的语句级引用血缘。授权先于 `EXPLAIN` 和业务 SQL 执行；任何表级 deny、列级 deny、来源歧义、视图无法展开或元数据不一致都直接 deny，审批、脱敏和规则开关都不能把 deny 提升为放行。

列授权配置继续隶属于既有 `Policy(agent_id, datasource_id, object_name)`，但把 `policies.columns` 的逗号字符串规范化为子表，并区分 `output` 与 `reference` 两种用途。现有列白名单迁移为两种用途均允许，保持升级不放宽且尽量兼容；缺省 deny 由“关系已配置列策略但没有对应列/用途”表达，不引入显式 column deny。最终列决策为：

`table/profile/rule deny > column deny > mask > allow`

其中 `mask` 不是授权补丁：列必须先通过授权，之后才由 B1 的同一份脱敏计划决定是否脱敏。引用列不会在数据库谓词中被“脱敏后再计算”；它必须有 `reference` 权限，否则 deny。

本设计建议拆成 **8 个可独立验收切片（S1～S8）**。

## 1. 真实代码基线与现状约束

以下结论来自当前仓库代码，不是目标态假设。

### 1.1 授权、策略、Agent profile 与规则引擎

- `internal/model/storage.go` 的 `model.Policy` 已有 `AgentID`、`DatasourceID`、`ObjectType`、`ObjectName`、`Columns *string`、`RowFilter *string`、`Action`。列名仍以逗号字符串存储。
- `internal/model/placeholders.go` 的 `model.PolicyDecision` 包含 `AllowedTables`、`DeniedTables`、`ColumnACL map[string][]string` 与 `Level`。
- `internal/policy/resolver.go` 的 `Resolver.Resolve` 合并策略。当前 column policy：
  - 必须精确到单表，不能是 `*` 或 `schema.*`；
  - 只支持 `action=allow`；
  - 多行策略按列并集；
  - 不会因为 column policy 自动授予表访问，仍需独立表级 allow。
- `internal/rules/generic.go` 的 `r003Rule` 执行 Agent level（`readonly` / `dml` / `ddl`）语句级约束；`r010Rule` 执行表授权与当前有限列授权。
- `r010Rule.Eval` 当前只在 `SELECT && len(ast.Tables)==1` 时检查投影列；源码明确注明 JOIN / self-join 在表授权通过后不应用 column ACL。
- `policy.HasBroadTableGrant` 使 `*` 或 `schema.*` 表授权跳过列 ACL；这是 B2 必须收紧的现状语义。
- `engine.Engine.Evaluate` 的聚合优先级是 deny > approve > warn > allow。
- `internal/pipeline/rule_overrides.go` 会把存储的 `Rule.Enabled` 投影成规则层配置；包括 `R010` 在内的内置规则实际上可被禁用。因此 B2 若仅实现成 `R011` 或改造后的 `R010`，不能满足“无放行开关”。
- `RowFilter` 当前只有存储、API、页面展示，没有运行时执行点；B2 不依赖它，也不宣称它提供行级安全。

### 1.2 B1 投影血缘与脱敏管线

- `internal/model/model.go` 的 `AST.ProjectionLineages []ProjectionLineage` 是 B1 的权威投影血缘。
- `ProjectionLineage` 按目标列表项保存 `SelectIndex`、`OutputName`、`Variadic`、`SetOp` 和全部 `Arms`。
- `LineageArm` 保存 `Kind`、`Operation`、`Status`、`Dependencies`、`PossibleRelations`。
- `ColumnDependency` 的 `Origin` 是物理 `[schema.]table.column`，`Role` 已有 `value / control / group / order / filter`。契约明确规定 `Origin.Relation.Alias` 不是物理身份，必须为空。
- `LineageKind` 覆盖 direct、transparent、composite、aggregate、window、constant、wildcard、opaque；`LineageStatus` 覆盖 resolved、source_free、ambiguous、opaque、unsupported。
- `LineageRoute` 当前包含 CTE、derived、scalar subquery、lateral，没有 view 路由。
- `internal/parser/mysql_lineage.go` 与 `postgres_lineage.go` 已处理表达式、聚合、窗口、集合操作、CTE、派生表、LATERAL、标量子查询以及部分 `USING/NATURAL` 合并列语义；硬限制定义在 `internal/parser/lineage.go`：深度 64、集合叶 256、投影 4096、依赖 16384、AST 节点 50000。
- 顶层普通查询的 `WHERE / ON / HAVING / ORDER BY` 并没有形成一份独立、完整、可供授权消费的清单。现有 `mysqlSubqueryClauseDependencies` / `postgresSubqueryClauseDependencies` 主要服务标量或 LATERAL 子查询的输出血缘，不能替代语句级引用血缘；JOIN `ON` 尤其缺失。
- `internal/pipeline/pipeline.go` 当前顺序是 auth → load → parse → `StageGuardStatic` → `StageGuardDynamic`（打开 executor/session、可选 EXPLAIN、动态规则）→ execute → redact → audit。
- 结果脱敏发生在执行之后。若 redactor 实现 `mask.ProjectionLineageAwareRedactor`，管线调用 `resolveProjectionLineages` 对齐结果列，再调用 `ApplyWithProjectionLineages`；否则退回 `DirectProjections` 兼容路径。
- `internal/mask/lineage.go` 的 `decideProjectionColumn` / `decideLineageArm` 已定义 B1 的精确命中和 fail-closed block 行为。该判定目前是 redactor 内部逻辑，没有可在执行前复用的公共“保护计划”。
- 视图在当前 parser 契约中仍被当作 SQL 中出现的物理关系名；`internal/parser/direct_projection_source_test.go` 也明确测试了该行为。仓库没有读取视图定义并追溯到底表的实现。B2 若要求“视图穿透”，必须新增受控元数据解析；不能假定 B1 代码已经具备底表展开。

### 1.3 解析器、JOIN 与方言

- `parser.NewParser` 只接受 `postgres` 和 `mysql`。
- PostgreSQL 使用 `github.com/pganalyze/pg_query_go/v5 v5.1.0`；MySQL 使用 `vitess.io/vitess/go/vt/sqlparser v0.22.4`。`internal/parser/doc.go` 仍写 v0.21.6，文档与 `go.mod` 不一致，实施时应以 `go.mod` 为准并修正文档。
- `AST.Tables` 保留物理关系及别名；CTE 名会从物理表集合中移除。表级匹配忽略别名。
- 无目录信息时，当前血缘对多来源裸列保守地保留所有 FROM binding，因此合法但需要目录才能唯一解析的裸列也可能是 ambiguous。
- PostgreSQL lineage builder 对 `USING/NATURAL` 合并列作 ambiguous 处理；MySQL 测试也要求合并裸列为 ambiguous。

### 1.4 存储、API、前端与审计

- 元数据存储方言只有 `store.DialectSQLite` 和 `store.DialectPostgres`；`ParseDialect` 不接受 MySQL。
- 合并存储迁移当前最新为 SQLite/PostgreSQL `0008`；分离 metadata 迁移最新为 `0007`；独立 audit PostgreSQL 流最新为 `0005`。
- `internal/store/policy_repository.go` 直接 CRUD `policies.columns`，没有事务式子表写入。
- 当前策略 API 是：
  - `GET /api/v1/policies`
  - `POST /api/v1/policies`
  - `PUT /api/v1/policies/{id}`
  - `DELETE /api/v1/policies/{id}`
  DTO 是 `internal/adminapi/dto.go` 的 `policyInput` / `policyView`。
- `web/src/pages/Policies.tsx` 对 column policy 只允许 `allow`，列名由 tags 输入；前端 API 类型的 `columns` 也是逗号字符串。
- `model.AuditLog.DetailsJSON` 已进入存储、导出、事件流和审计链的规范化内容。`pipeline.auditEventDetailsJSON` 当前只写 `audit_phase`、`related_audit_id`、`key_version`，适合向内增加版本化的 `column_auth` 对象，无需新增 audit 表字段。
- `web/src/pages/audit/AuditDetailDrawer.tsx` 当前只从 `details_json` 解析审计阶段，不展示列决策。
- 真库查询 executor 也只有 PostgreSQL 与 MySQL。SQLite 仅是控制面元数据仓库，不是业务 SQL parser/executor 方言。

## 2. 目标、非目标与安全原则

### 2.1 目标

1. 对所有可返回数据的查询块，逐输出列判定 allow / mask / deny。
2. 对不直接输出但影响结果的引用列逐项授权，至少覆盖 WHERE、JOIN ON、USING、GROUP BY、HAVING、ORDER BY，并覆盖窗口、聚合 FILTER、表达式控制分支、相关子查询和集合分支中的等价位置。
3. 正确处理多表 JOIN、自连接别名、同名列、UNION/INTERSECT/EXCEPT、聚合/表达式、派生表、CTE、LATERAL、标量子查询和视图链。
4. 保持表级 deny、Agent profile、现有规则、审批和 B1 脱敏的既有安全上界；B2 只能收紧，不能借由列 allow 或 mask 绕过更高层 deny。
5. 在执行前完成授权；静态可判定的 deny 不得打开业务数据库，需目录解析的 deny 最多只能执行受控元数据读取，不得 EXPLAIN 或执行用户 SQL。
6. 产生可解释、可审计、大小有界的列决策证据。

### 2.2 非目标

- 不实现行级策略；已有 `row_filter` 仍不在 B2 中生效。
- 不把脱敏当作完整 DLP，也不尝试消除所有统计推断风险；引用列权限负责显式控制谓词/排序/分组能力。
- 不提供按 JOIN 别名分别授权。同一物理表的两个别名共享同一组权限；别名只用于解析和审计展示。
- 不新增“遇到未知仍放行”“关闭列授权”“兼容模式放行”等开关。
- 不在 B2 中新增第三种业务查询方言；见第 10.5 节的验收阻塞项。
- 不设计每个 DML 写目标列的细粒度 write grant。读取型依赖（`INSERT … SELECT`、子查询、PostgreSQL `RETURNING` 等）仍须按本设计授权；目标列写权限留给后续能力。
- 不改变掩码算法、敏感类型和 `mask_rules` 的作用域模型。

### 2.3 默认安全原则

1. **deny 优先且不可提升**：profile deny、表 deny、列 deny 任一出现即整条语句 deny；approve/warn、调用方 `RequireApproval` 和 mask 均不能覆盖。
2. **归属不明 fail-closed**：只要一个需要授权的依赖不是 `resolved` 或可信 `source_free`，最终授权必须 deny。不能用 B1 的结果后置 `***` 替代执行前 deny。
3. **mask 不授予访问**：先证明列有对应 usage 的 allow，再计算是否 mask。
4. **所有来源都要授权**：表达式、多来源合并列、集合分支任一来源 deny，则输出 deny。
5. **所有非值依赖都要授权**：CASE 条件、聚合 FILTER、窗口排序/分区等即使不显示，也按 reference 检查。
6. **列 ACL 优先于宽表 grant**：`*` / `schema.*` 只授予表可达性；若具体关系存在列策略，列策略仍收紧该关系。此项刻意修复 `HasBroadTableGrant` 当前绕过列 ACL 的行为。
7. **无列策略时兼容表授权**：一个已获表授权、且没有任何列策略的关系继续视为全列 output/reference allow。升级不会要求所有既有表策略立刻补齐列清单。
8. **有列策略即双用途默认拒绝**：关系一旦存在列策略，未列出的列 deny；某列只配置 output 时，reference deny，反之亦然。

## 3. 权威列引用模型

### 3.1 保留并复用 B1 输出血缘

`AST.ProjectionLineages` 继续是输出列权威来源，不另建第二份输出解析器。B2 只增加授权所需信息，不改变 B1 的物理身份定义。

建议新增以下模型（名称为**拟议类型**，实施前以代码评审定名）：

```go
type ColumnUsage string
const (
    ColumnUsageOutput    ColumnUsage = "output"
    ColumnUsageReference ColumnUsage = "reference"
)

type ReferenceSite string
const (
    ReferenceWhere   ReferenceSite = "where"
    ReferenceJoinOn  ReferenceSite = "join_on"
    ReferenceUsing   ReferenceSite = "using"
    ReferenceGroup   ReferenceSite = "group_by"
    ReferenceHaving  ReferenceSite = "having"
    ReferenceOrder   ReferenceSite = "order_by"
    ReferenceWindow  ReferenceSite = "window"
    ReferenceControl ReferenceSite = "expression_control"
)

type ColumnReferenceLineage struct {
    QueryBlockID   string
    Site           ReferenceSite
    ExpressionIndex int
    Status         LineageStatus
    Dependencies   []ColumnDependency
    PossibleRelations []ObjectRef
}
```

并在 `model.AST` 增加 `ReferenceLineages []ColumnReferenceLineage`。`QueryBlockID` 必须是解析顺序生成的稳定路径，不能使用内存地址；它只用于诊断/审计，不参与授权键。

为显示自连接路径，建议给 `ColumnDependency` 增加只读诊断字段 `BindingAlias string`，但继续保证 `Origin.Relation.Alias == ""`。授权键永远是物理关系与列；别名不能成为提权边界。

### 3.2 依赖角色到权限用途的映射

| 血缘依赖 | 所需权限用途 | 说明 |
|---|---|---|
| 投影 arm 中 `DependencyValue` | output | 直接列、表达式参数、聚合输入、集合分支值来源 |
| 投影 arm 中 control/filter/group/order | reference | CASE 条件、聚合 FILTER、窗口分区/排序、影响输出的子查询谓词 |
| `ReferenceLineages` 的所有依赖 | reference | WHERE/ON/USING/GROUP/HAVING/ORDER 等投影外引用 |
| `LineageSourceFree` 常量、可信 `count(*)`/`count(constant)` | 无列权限 | 仍需语句涉及关系的表权限；`count(*)` 暴露行数属于表授权范围 |
| opaque / ambiguous / unsupported | 无可证明映射 | deny，不猜测 |

`ORDER BY 1`、`ORDER BY output_alias`、方言允许的 GROUP BY alias/ordinal 必须先解析到目标列表项，再复用该项依赖，不能把 `1` 或 alias 当物理列。若方言解析优先级无法证明，deny。

### 3.3 解析完整性约束

- 每个可执行查询块都必须有：输出 lineage、外部 clause references、FROM binding 图。
- JOIN `ON` 在 JOIN 节点自己的可见 scope 中解析，不能误用最终扁平 scope。
- `USING(k)` 生成左右各一个 `reference` 依赖；NATURAL JOIN 需要目录列集合求交，无法取目录则 deny。
- 子查询 clause references 必须独立进入 `ReferenceLineages`，同时在影响外层某个输出时按 B1 规则保留到对应 arm；授权层需去重，不能漏检。
- 去重键至少为 `(query_block, physical schema, table, column, usage, site, route, binding alias)`；审计可折叠，授权不能因为错误去重丢失用途。
- 沿用 B1 的五项硬限制，并给 references 增加总数上限，建议与 `lineageMaxDependencies=16384` 共用总预算，而不是再允许额外 16384。

## 4. 列级决策算法

### 4.1 单个物理列

输入：Agent、datasource、物理关系 `R`、列 `C`、用途 `U`、表策略、列策略、B1 mask plan。

1. Agent status/profile 不允许该语句：deny。
2. `R` 命中 `DeniedTables`：deny。
3. `R` 不命中 `AllowedTables`：deny。
4. `R` 没有任何列策略：授权结果 allow。
5. `R` 有列策略：仅当 `(C,U)` 或 `(*,U)` 存在 allow grant 时 allow，否则 deny。
6. 若 U=output 且授权为 allow，再交给 B1 保护计划：
   - 不匹配有效 mask rule：最终 allow；
   - 精确或 fail-closed 匹配有效 mask rule：最终 mask；
   - B1 计划本身无法形成安全处理：deny，不能返回原值。
7. 若 U=reference，mask rule 不改变谓词语义，也不授予权限；授权结果仍为 allow/deny。审计可以标记该列同时“受脱敏规则保护”，但决策不写成 mask。

### 4.2 输出列

对每个对齐后的输出位置，逐 arm、逐 dependency 判定：

- `LineageSourceFree` 且结构合法：allow。
- 每个 `DependencyValue` 需要 output grant；每个非 value 依赖需要 reference grant。
- 一个 arm 内合并为 deny > mask > allow。
- UNION/INTERSECT/EXCEPT 的多个 arms 再按 deny > mask > allow 合并。
- 任一 arm 的 status 为 ambiguous/opaque/unsupported，或 variadic 展开不完整：deny。
- mask 的具体算法、冲突处理和固定 block 仍以 B1 的 `decideProjectionColumn` 语义为唯一来源。实现时应把它抽为共享纯计划器，授权审计与实际 `ApplyWithProjectionLineages` 使用同一计划对象，禁止复制两套判断。

典型结果：

| 表达式 | 依赖 | 合并结果 |
|---|---|---|
| `c.name` | `customers.name(output)` | 该列的 allow/mask/deny |
| `concat(c.phone,'')` | `customers.phone(output)` | 有 mask rule 时 mask；无 output grant 时 deny |
| `CASE WHEN c.vip THEN c.name END` | `vip(reference)` + `name(output)` | 任一 deny 则 deny；否则按 name 的 mask 结果 |
| `sum(o.amount)` | `orders.amount(output)` | amount 未授权即 deny |
| `count(*)` | source-free | 表授权通过后 allow |
| `a.secret UNION ALL b.public` | 两个 output arms | 任一 deny 则整列/整语句 deny；任一 mask 则整列按 B1 安全合并 mask |

### 4.3 投影外引用

所有引用按 reference 用途判定。任一 deny 使整条语句 deny，不允许“只删掉条件”或改写 SQL。

- WHERE：包括相关子查询两侧列。
- ON：左右两边表达式中的全部物理列。
- USING：同名列在每个参与 binding 上都要 reference allow。
- GROUP BY：分组键全部要 reference allow；若同一列也输出，还需 output allow。
- HAVING：聚合参数按其在表达式中的角色判定，过滤条件列需要 reference allow。
- ORDER BY：物理排序列需要 reference allow；引用输出 alias/ordinal 时复用输出依赖。
- DISTINCT、窗口 PARTITION/ORDER、聚合 FILTER、CASE 条件虽不在用户列出的五类中，也按 control/reference 处理，防止旁路。

### 4.4 整条语句与现有决策的合并

- 列授权的 `mask` 不是 `model.Decision`；语句仍可是 allow/warn，返回前必须完成脱敏。
- 列 deny 追加一个不可配置的授权 hit，例如拟议 ID `AUTH_COLUMN`，并将 `Assessment.Decision` 设为 deny。
- 规则引擎的 approve/warn 与授权结果合并时继续使用 deny > approve > warn > allow。
- `RequireApproval` 只能把 allow/warn 提升为 approve，不能改变 deny。
- `ExplainOnly` 同样执行完整列授权；不能利用 explain-only 获取未授权对象的计划信息。

## 5. JOIN、自连接与查询结构语义

### 5.1 JOIN 类型

JOIN 类型只改变空值补齐和行组合，不改变授权原则。

| JOIN 类型 | 输出列 | 连接条件 | 特殊处理 |
|---|---|---|---|
| INNER | 每个物理来源均检查 output/reference | ON/USING 全检查 reference | 无 |
| LEFT | 左右来源同等授权 | ON/USING 两侧都检查 | 右侧可能为 NULL 不降低权限 |
| RIGHT | 左右来源同等授权 | ON/USING 两侧都检查 | 左侧可能为 NULL 不降低权限 |
| FULL | 左右来源同等授权 | ON/USING 两侧都检查 | PostgreSQL 支持；MySQL 解析失败即 deny |
| CROSS | 每个输出来源检查 | 无隐含 ON；若带方言扩展条件照常检查 | 不因无条件而豁免列授权 |

### 5.2 ON、USING、NATURAL 与同名列

- `ON a.id=b.customer_id`：`a.id` 与 `b.customer_id` 都需要 reference allow；投影它们时另需 output allow。
- `USING(id)`：左右每个 binding 的 `id` 都需 reference allow。投影合并后的 `id` 是多来源输出，左右都需 output allow；任一来源要求 mask 时按 B1 多来源规则 mask，算法/来源不能安全合并则 deny。
- `NATURAL JOIN`：必须从目录得到左右列集合并求交，等价生成全部 USING 依赖。目录不可用、视图列集合不完整或重名规则不确定时 deny。
- `SELECT id FROM a JOIN b ...`：若没有目录证明唯一归属，deny。不能仅因数据库最终能解析就猜测来源。
- `SELECT a.*, b.*`：目录展开后逐列判定；任何不可列举来源或未授权列使语句 deny。

### 5.3 自连接与别名

- `orders o1 JOIN orders o2` 产生两个 binding occurrence，但物理授权键相同。
- `o1.secret` 和 `o2.secret` 都由 `orders.secret` 权限控制；不存在“给 o1 allow、借 o2 提权”。
- 同一物理列在 ON 与输出中使用时必须同时具备 reference 与 output grant。
- 原表名被别名遮蔽的解析规则继续沿用 B1；错误使用被遮蔽名导致 opaque/parse error 时 deny。
- 审计显示 `public.orders.id via o1/o2`，但不把 alias 保存为策略对象。

### 5.4 表达式、聚合与集合操作

- 表达式的全部 value 来源需 output grant；control/filter/order/group 来源需 reference grant。
- 透明表达式与复合表达式都不能洗掉来源。
- 聚合函数输入需 output grant；`GROUP BY` 键需 reference grant；`count(*)`/`count(constant)` 维持 source-free 特例。
- UNION/INTERSECT/EXCEPT 按输出位置合并全部叶分支，任何分支 deny 即 deny。
- 集合查询的顶层 ORDER BY alias/ordinal映射到集合输出；无法映射时 deny。

### 5.5 派生表、CTE、LATERAL 与标量子查询

- 派生表/CTE 的输出名只是中间名字，授权必须沿 `RouteDerived` / `RouteCTE` 回到底层物理列。
- 列表重命名数量不匹配、重复输出名、递归 CTE 未收敛为 resolved 时 deny。
- LATERAL 与相关标量子查询保留外层 binding 路由；其内部 WHERE/ORDER/GROUP 等同时进入 reference lineage。
- 未被引用的 CTE 不新增列级 grant 要求，但当前 `AST.Tables`/R010 可能仍对其底表做表授权；B2 不放宽这个既有表级行为。

### 5.6 视图与物化视图穿透

当前代码没有底表视图穿透。B2 目标态需要新增强类型、只读的拟议接口 `AuthorizationCatalog`，由 PostgreSQL/MySQL executor/session 实现：

```go
type AuthorizationCatalog interface {
    ResolveRelations(ctx context.Context, refs []RelationRef) ([]RelationMetadata, error)
}

type RelationMetadata struct {
    PhysicalName ObjectRef
    Kind         string // base_table, view, materialized_view
    Columns      []string
    ViewSQL      string
    DefinitionFingerprint string
}
```

要求：

1. 无模式名关系在同一请求会话内按数据库真实规则解析：PostgreSQL search_path，MySQL 当前 database。
2. 视图定义用同一方言 parser 再解析，并把视图输出列映射到底层 lineage；新增 `RouteView`。
3. 视图对象本身必须通过表授权；所有可达底表也必须通过表授权；底层列再执行 output/reference 授权。这样安全视图不能成为隐藏受保护列的绕过路径。
4. 链式视图递归展开；检测环、权限不足、截断定义、临时对象、未知 table function、定义不可解析时 deny。
5. PostgreSQL materialized view 作为独立存储关系可选择“仅按物化视图自身列授权”或“同时要求定义底表授权”。为满足不弱化原则，本设计选择后者；该选择对运维影响较大，实施前标记**待确认**。
6. MySQL `SQL SECURITY DEFINER/INVOKER` 不改变 AgentSQL 授权；AgentSQL 始终按逻辑底层来源检查。
7. 元数据与执行必须来自同一 session。不能跨请求长期缓存 `SELECT *` 列集合或视图定义，否则视图/表变更可能造成旧授权放行新列。若未来缓存，必须有数据库可验证的版本/fingerprint 并在执行前重验。

## 6. 与现有管线的集成与顺序

### 6.1 拟议执行顺序

1. `StageAuth`：`Authenticate`，确认 Agent 状态。
2. `StageLoad`：加载 datasource、policy、rule overrides；`Resolver.Resolve` 形成表与列 permission index。
3. `StageParse`：两方言 parser 同时生成 `AST.Tables`、`ProjectionLineages`、`ReferenceLineages`。
4. **MandatoryAuthStatic（拟议，可纳入 `StageGuardStatic` 计时但单列子耗时）**：
   - 强制 Agent profile 基线；
   - 强制表 allow/deny；
   - 对 resolved 的输出/引用做列判定；
   - 任何确定 deny 立即审计并返回，executor 调用计数必须为 0；
   - 需要目录的 wildcard/view/NATURAL/未限定关系只记录 pending，不放行。
5. 现有静态规则：R001～R010 等继续运行；R010 在过渡期可保留用于兼容 hit，但不再是授权唯一执行点。
6. 若已有 deny，结束；否则打开 executor，并为目录解析与后续 SQL使用同一 session。没有用户 `SessionID` 时也需要请求级临时 session；现有 `Executor` 接口需要扩展或增加专用受控目录端口。
7. **MandatoryAuthResolve（拟议，发生在任何 EXPLAIN 之前）**：目录解析 wildcard/NATURAL/视图，重建完整 lineage，执行最终列授权和 B1 保护计划。目录错误按授权未知 deny，不返回数据库内部细节。
8. `StageGuardDynamic`：仅在授权通过后执行 `EXPLAIN` 与动态规则 R004/R105/R106/R107/R204。
9. 审批屏障；deny 不能进入审批。
10. `StageExecute`：执行 SQL。
11. `StageRedact`：使用第 7 步生成的同一份保护计划；校验运行时列数量/名称与计划一致。若不一致，整份结果 fail-closed，不得返回原值。
12. `StageAudit`：记录表/列决策、目录 fingerprint、mask 报告与已有字段。

### 6.2 优先级与职责边界

- 表 deny 永远先于列 allow/mask。
- 未获表 allow 时，列 grant 不能单独授予访问。
- profile 不允许的语句不能被列 grant 放行。
- mask rule 只改变授权后输出，不作用于 ON/WHERE 的数据库计算。
- `R010` 的规则开关不能关闭 MandatoryAuth。建议 S4 后让 `R010` 只生成兼容解释，最终在后续版本从可配置规则目录移除其授权职责。
- `StaticAssess`（`internal/pipeline/static_assess.go`）没有真实 policy/catalog，不能声称完成 B2 最终授权；应返回 `column_auth_status=not_evaluated|pending`。不得默认注入 `AllowedTables={"*"}` 后展示“列授权通过”。

## 7. 权限元数据、迁移与 API

### 7.1 目标模型

保留 `policies` 作为主体/数据源/对象/action 的父记录；新增规范化子表：

```text
policy_column_permissions
  policy_id    -> policies.id
  column_name  目录中的真实列名，保留大小写
  usage        output | reference
  PK(policy_id, column_name, usage)
```

约束：

- 仅允许挂在 `object_type='column' && action='allow'` 的父 policy；跨表约束由 repository/service 校验，数据库 FK 只保证父记录存在。
- 一个物理关系只要存在任一 column policy，就进入 constrained 状态；缺少的列或 usage 为 deny。
- 多个 column policy 对同一关系按 `(column,usage)` 并集。
- `column_name='*'` 仅为兼容现有数据；新 API/UI 禁止创建。既有 `*` 映射为对应 usage 的全列 grant，并在 UI 标记高风险。
- 表级 wildcard 不再压过具体关系的列策略。
- `policies.columns` 在 v1 保留并双写，便于旧版本回退；运行时以子表为权威。后续大版本再移除。

### 7.2 SQLite DDL（可进入现有迁移流）

合并流：`internal/store/migrations/sqlite/0009_column_permissions.sql`  
分离 metadata 流：`internal/store/migrations/metadata/sqlite/0008_column_permissions.sql`

```sql
CREATE TABLE policy_column_permissions (
  policy_id   TEXT NOT NULL,
  column_name TEXT NOT NULL,
  usage       TEXT NOT NULL CHECK (usage IN ('output','reference')),
  PRIMARY KEY (policy_id, column_name, usage),
  FOREIGN KEY (policy_id) REFERENCES policies(id) ON DELETE CASCADE,
  CHECK (column_name <> '' AND column_name = TRIM(column_name))
);

CREATE INDEX idx_policy_column_permissions_policy
  ON policy_column_permissions(policy_id);

WITH RECURSIVE split(policy_id, rest, value) AS (
  SELECT id, COALESCE(columns, '') || ',', ''
  FROM policies
  WHERE object_type = 'column'
  UNION ALL
  SELECT policy_id,
         SUBSTR(rest, INSTR(rest, ',') + 1),
         TRIM(SUBSTR(rest, 1, INSTR(rest, ',') - 1))
  FROM split
  WHERE rest <> ''
), names AS (
  SELECT DISTINCT policy_id, value AS column_name
  FROM split
  WHERE value <> ''
)
INSERT INTO policy_column_permissions(policy_id, column_name, usage)
SELECT policy_id, column_name, 'output' FROM names
UNION ALL
SELECT policy_id, column_name, 'reference' FROM names;
```

迁移前应在 `applyMigrationWithOptions` 增加应用层 preflight：拒绝 column policy 的 NULL/空列、空 token、非法 action/object、重复规范化 token。SQLite DDL 自身无法可靠抛出带行 ID 的诊断。

### 7.3 PostgreSQL DDL（可进入现有迁移流）

合并流：`internal/store/migrations/postgres/0009_column_permissions.sql`  
分离 metadata 流：`internal/store/migrations/metadata/postgres/0008_column_permissions.sql`

```sql
CREATE TABLE policy_column_permissions (
  policy_id   TEXT NOT NULL REFERENCES policies(id) ON DELETE CASCADE,
  column_name TEXT NOT NULL CHECK (column_name <> '' AND column_name = BTRIM(column_name)),
  usage       TEXT NOT NULL CHECK (usage IN ('output','reference')),
  PRIMARY KEY (policy_id, column_name, usage)
);

CREATE INDEX idx_policy_column_permissions_policy
  ON policy_column_permissions(policy_id);

WITH names AS (
  SELECT DISTINCT p.id AS policy_id, BTRIM(value) AS column_name
  FROM policies AS p
  CROSS JOIN LATERAL UNNEST(STRING_TO_ARRAY(COALESCE(p.columns, ''), ',')) AS value
  WHERE p.object_type = 'column' AND BTRIM(value) <> ''
)
INSERT INTO policy_column_permissions(policy_id, column_name, usage)
SELECT policy_id, column_name, 'output' FROM names
UNION ALL
SELECT policy_id, column_name, 'reference' FROM names;
```

同样执行应用层 preflight；不能悄悄跳过坏数据。

### 7.4 MySQL 参考 DDL与迁移版本约束

当前控制面**不支持 MySQL 元数据存储**，`migrate.go` 的 embed、`schemaMigrationsDDLFor`、`claimMigration` 和 `store.Dialect` 都没有 MySQL 分支。因此下面仅是需求要求的参考 DDL，**不能在 B2 中伪装成可运行迁移**：

```sql
CREATE TABLE policy_column_permissions (
  policy_id   VARCHAR(255) NOT NULL,
  column_name VARCHAR(255) NOT NULL,
  usage       ENUM('output','reference') NOT NULL,
  PRIMARY KEY (policy_id, column_name, usage),
  CONSTRAINT fk_policy_column_permissions_policy
    FOREIGN KEY (policy_id) REFERENCES policies(id) ON DELETE CASCADE,
  CONSTRAINT ck_policy_column_name_trimmed
    CHECK (column_name <> '' AND column_name = TRIM(column_name)),
  INDEX idx_policy_column_permissions_policy (policy_id)
) ENGINE=InnoDB;

INSERT INTO policy_column_permissions(policy_id, column_name, usage)
-- 需由迁移程序按现有 Go policyColumns 语义拆分并参数化批量写入；
-- 不使用依赖服务器版本的 JSON_TABLE/递归字符串拆分。
SELECT ?, ?, ?;
```

MySQL migration version：**N/A（待确认是否另立控制面 MySQL epic）**。若批准新增 `DialectMySQL`，它应从独立 `migrations/mysql/0001_init.sql` 开始，而不是假称与 SQLite/PG 的 `0009` 同版本。业务 MySQL datasource 不执行任何上述 DDL。

### 7.5 Repository 与 resolver

- 扩展 `model.Policy`，增加拟议 `ColumnPermissions []ColumnPermission`；保留 `Columns`。
- `PolicyRepository.Create/Update/Delete` 改成单事务写父表、删旧子项、写新子项，并同步生成稳定排序的 legacy CSV。
- `Get/List/ListByAgent/ListByAgentAndDatasource` 批量加载子项，避免 N+1。
- `Resolver.Resolve` 生成按关系与 usage 索引的 immutable grant set；保留 `ColumnACL` 一版供 R010/旧测试兼容，但 MandatoryAuth 不消费逗号字符串。
- `engine.clonePolicyDecision` 与所有测试 fake 必须深拷贝新 map/slice。
- `internal/store/sqlite_pg_migrate.go` 的 SQLite→PG 搬迁 manifest 要加入子表，且保证父表先于子表。

### 7.6 API（均为拟议扩展，不冒充现有接口）

复用现有四个 `/api/v1/policies` endpoint，不新增“放行开关”。请求/响应增加：

```json
{
  "object_type": "column",
  "object_name": "public.orders",
  "action": "allow",
  "column_permissions": [
    {"column": "id", "usages": ["reference"]},
    {"column": "amount", "usages": ["output", "reference"]}
  ]
}
```

兼容规则：

- 老字段 `columns:"id,amount"` 仍可读写，映射为每列 output+reference。
- 新旧字段同时出现时必须语义一致，否则 422；不能按并集偷偷扩权。
- 新客户端以 `column_permissions` 为准；response 同时返回稳定排序的 `columns` 兼容值。
- column policy 仍只允许 action=allow、精确表对象、至少一个 permission；`mask` 不作为 policy action，它由 `mask_rules` 计算。
- API 错误消息可以说明哪一项格式错误，但不能泄露数据库目录中调用者无权看到的对象。
- 可选新增只读 schema endpoint 供 UI 选择列；若复用 `controlledread.Service.ListColumns`，必须先扩展其当前“仅 BASE TABLE”限制，并隔离 sampling 权限。该 endpoint 名称与鉴权在实现前**待确认**。

## 8. 前端与审计最小方案

### 8.1 权限页

在现有 `web/src/pages/Policies.tsx` 上做最小改造：

- 保留 Agent + datasource 范围选择与变更预览。
- column policy 表单中每列显示两个权限：输出、引用；默认新选列不自动全选，由管理员显式选择至少一种。
- legacy CSV 记录显示“输出 + 引用（由旧格式迁移）”。
- `*` legacy 记录显示高风险警告且不能新建。
- 同时展示该列是否有匹配的 enabled mask rule；这是结果提示，不是授权选项。
- 明确文案：引用权限允许用于 WHERE/ON/GROUP/HAVING/ORDER，但不会返回该列；输出权限不自动授予引用权限。
- 提交预览按“新增/移除 output/reference”展示，避免只显示一串 CSV。

### 8.2 审计数据

扩展 `pipeline.auditEventDetails`，在现有 `DetailsJSON` 内加入版本化对象：

```json
{
  "audit_phase": "outcome",
  "key_version": 2,
  "column_auth": {
    "version": 1,
    "outcome": "deny",
    "outputs": [
      {
        "index": 0,
        "name": "mobile",
        "decision": "mask",
        "sources": ["public.customers.phone"],
        "routes": ["cte"],
        "policy_ids": ["pol_phone"]
      }
    ],
    "references": [
      {
        "site": "join_on",
        "decision": "deny",
        "sources": ["public.orders.customer_id"],
        "via_aliases": ["o"]
      }
    ],
    "catalog_fingerprints": [],
    "counts": {"allow": 0, "mask": 1, "deny": 1},
    "truncated": false
  }
}
```

约束：

- 只记录标识符、策略 ID、决策与原因码，不记录结果值、谓词常量或 mask 前内容。
- 稳定排序，便于审计链与测试；`DetailsJSON` 已被 `auditchain` 覆盖，无需改 canonical 字段数。
- 设置上限：建议最多 256 个明细且序列化后不超过 32 KiB；超限时保留 deny 明细、汇总计数、SHA-256 digest，并置 `truncated=true`。阈值实施前压测确认。
- deny reason 使用稳定枚举，例如 `TABLE_DENY`、`COLUMN_USAGE_MISSING`、`LINEAGE_AMBIGUOUS`、`VIEW_UNRESOLVED`、`CATALOG_MISMATCH`。

### 8.3 审计页

扩展 `AuditDetailDrawer` 的 `details_json` 安全解析：

- 新增“列级授权”区块，按输出列/引用列分组。
- 展示 decision 标签、物理来源、alias 路径、usage/site、policy ID、视图/CTE route。
- 明细被截断时展示计数和 digest。
- 老审计没有 `column_auth` 时显示“该事件产生于列级审计上线前”，不能误显示为全部 allow。

## 9. 配置、兼容性与升级行为

- 不新增 feature flag、环境变量或规则 enable 项。
- schema migration 是 additive；`auto_migrate=false` 时，现有 `VerifyMetadataSchema` 会因版本落后而 fail-closed 启动，这是预期行为。
- 迁移后旧 column CSV 自动变为 output+reference grant；旧表策略无 column policy 时行为不变。
- JOIN/自连接此前跳过 column ACL，升级后会被真实执行；这会产生预期的新增 deny。
- `*`/`schema.*` 表 grant 不再绕过具体表 column policy；这是预期收紧，需要 release note 明示。
- 现有列名比较保持“目录解析后的真实标识符”语义。PostgreSQL 非引号名按数据库折叠、引号名精确；MySQL 通过目录解析到实际列名后匹配，不能只用 Go `EqualFold` 模拟服务器规则。
- 回退：因为 v1 保留并双写 `policies.columns`，可停机回滚到旧二进制；新子表保留不会影响旧代码。回滚期间 JOIN 列授权会恢复旧缺口，因此只用于紧急回退，并须在审计/变更记录中明确风险。
- 不建议用配置关闭 MandatoryAuth 回退；若 B2 引发误拒，修复血缘/元数据或回滚二进制。

## 10. 测试计划

### 10.1 Parser / lineage 单元与性质测试

两个现有 query dialect 都覆盖：

- 每种 JOIN：INNER/LEFT/RIGHT/CROSS；PostgreSQL 再测 FULL；MySQL FULL 必须 parse deny。
- ON、USING、NATURAL；限定列、裸同名列、同表不同 schema。
- 自连接两个及三个别名、别名遮蔽、别名交换、相同列同时 output/reference。
- WHERE/GROUP/HAVING/ORDER，alias/ordinal 与物理列优先级。
- CASE、CAST、concat、函数、聚合、FILTER、窗口 PARTITION/ORDER。
- UNION/UNION ALL/INTERSECT/EXCEPT 与分支不等、分支 wildcard。
- CTE 重命名、重复列名、派生表列清单、LATERAL、相关标量子查询、递归 CTE。
- 视图链、物化视图、循环视图、不可读取定义、定义变更 fingerprint。
- 继续运行 B1 的 `mysql_lineage_test.go`、`postgres_lineage_test.go`、`projection_lineage_*` 与 hard-limit 测试。
- fuzz：任意 AST/策略不得 panic；解析/归属未知只能 deny；依赖集合增加不能把 deny 降为 allow。

### 10.2 Authorizer 决策矩阵

- table deny + column allow => deny。
- table 未 allow + column allow => deny。
- exact/schema/global table allow 与具体 column constraint 的组合。
- 无 column policy => 兼容 allow；有任一 column policy 后缺列/缺 usage => deny。
- output-only、reference-only、both；同一列在两个角色中出现。
- 多来源的 deny > mask > allow 合并；UNION 各分支；表达式 control/value 混合。
- source-free 常量/count 特例；伪造 source-free 且仍带 dependency 必须 deny。
- R010 disabled、rule layer disabled、RequireApproval、ExplainOnly 都不能绕过 MandatoryAuth。
- `PolicyDecision` clone、并发只读、输入 map 后续修改不影响判定。

### 10.3 必须全部阻断的负向逃逸语料

至少包含以下用例，并对静态 deny 断言 executor 的 open/explain/query/execute 全为 0；目录型 deny 只允许 catalog 调用：

1. 漏列：`SELECT allowed, secret FROM t`。
2. 绕过投影：`SELECT concat(secret,'') AS harmless FROM t`、CAST/JSON/函数包装。
3. 条件列：`SELECT allowed FROM t WHERE secret='x'`。
4. 排序/分组侧信道：`ORDER BY secret`、`GROUP BY secret`、`HAVING max(secret)>0`。
5. JOIN 条件：只投影允许列，但 `ON a.secret=b.id`。
6. USING/NATURAL：一侧同名列未授权。
7. 自连接提权：`o1.allowed` 投影、`o2.secret` 条件；交换别名、伪装原表名。
8. 裸同名列：目录不能唯一归属时不得任选一个允许来源。
9. wildcard：`*`、`a.*` 中夹带未授权列；运行时列集合与计划不一致。
10. UNION 分支：第一分支允许、第二分支 secret；分支表达式改名。
11. CTE 隐藏：`WITH x AS (SELECT secret AS public_name FROM t) SELECT public_name FROM x`。
12. 派生表/标量/LATERAL 隐藏受保护列。
13. 视图隐藏：视图把 secret 改成普通名称；链式视图；视图定义在授权后变化。
14. 聚合：`max(secret)`、`count(secret)`；只有 `count(*)` 可走 source-free。
15. output 授权但缺 reference 授权，以及反向组合。
16. broad table grant 与具体 column policy 并存时不能靠 broad grant 绕过。
17. 不可解析新 AST 节点、递归/依赖/投影超限、catalog 超时/权限拒绝。
18. PostgreSQL quoted case、MySQL 特殊标识符、逗号/空白 legacy 数据。
19. DML 读取逃逸：`INSERT ... SELECT secret`、UPDATE 子查询、PostgreSQL RETURNING 未授权列。
20. 审批和 explain-only 绕过。

### 10.4 真库 e2e

沿用现有 `internal/pipeline/e2e_postgres_test.go` 与 `e2e_mysql_test.go` 的 testcontainers 结构：

- PostgreSQL 14/18 至少跑完整 B2 矩阵，15/16/17 可跑冒烟；MySQL 8 跑完整矩阵。
- 建立 customers/orders/employee self-reference、同名列、跨 schema 表、普通视图、链式视图；PostgreSQL 增加 materialized view。
- 每个 JOIN 类型同时验证响应 decision、是否执行、返回 mask 值、`RedactReport`、audit details。
- 在同一连接上变更视图定义，验证旧 fingerprint 不得放行。
- 使用真实数据库报出的列顺序验证 `SELECT *` 与 USING 合并列对齐。
- 继续运行现有权限、profile、approval、B1 投影泄漏、mask scope、hash/block/range、audit barrier/chain 测试。

### 10.5 “三方言真库 JOIN/自连接 e2e”的现状冲突

当前产品只有 PostgreSQL/MySQL 两种业务查询方言；SQLite 只用于控制面存储，不经过 `parser.NewParser` 或 `executor.Executor` 执行业务 JOIN。因此不能诚实地交付“三种 query dialect 的真库 JOIN e2e”。

建议验收口径改为：

1. PostgreSQL 真库 JOIN/自连接 e2e；
2. MySQL 真库 JOIN/自连接 e2e；
3. SQLite + PostgreSQL 两种控制面迁移/repository matrix，以及 MySQL 参考 DDL 静态审查。

若“三方言”是硬性产品要求，则必须先明确第三方言并单独实现 parser、executor、错误分类、目录解析和规则矩阵；B2 在此之前应标记 blocked，而不是用 SQLite metadata 测试冒充第三查询方言。此项为**待确认/验收阻塞项**。

### 10.6 存储、API、前端与审计测试

- SQLite combined v8→v9、metadata v7→v8；PostgreSQL 同版本路径；空库、重复执行、`auto_migrate=false`。
- legacy CSV 回填 output+reference，非法数据 preflight 明确失败且事务回滚。
- policy CRUD 父子原子性、并发 update、删除 cascade、列表无 N+1、SQLite→PG 搬迁。
- API 老字段、新字段、冲突字段、unknown field、非法 usage、column `*` 新建拒绝。
- 前端编辑/预览/提交、legacy 标签、mask badge；AuditDetailDrawer 对新旧/损坏/截断 JSON。
- audit export、event stream、chain golden/verify，确保新增 details 不泄露值且链仍可验证。

## 11. 性能预算

现有基线：`TestParseProjectionLineageP99Budget` 对典型解析要求 P99 ≤5 ms；`TestT25LatencyPercentile` 对内存 fake 的完整管线要求独占环境 P99 <5 ms。

B2 预算分层：

| 路径 | 目标 |
|---|---|
| 显式、resolved JOIN，无目录 I/O | B2 新增 CPU P99 ≤1 ms；完整 fake pipeline 继续 P99 <5 ms |
| 静态 deny | P99 <5 ms，且零业务 DB 调用 |
| 已取得同请求目录的最终授权 | CPU P99 ≤1 ms |
| 冷目录/视图解析 | 另计 DB RTT；授权 CPU P99 ≤2 ms，目录查询次数每请求 ≤2 个批量查询 |
| 规模增长 | 列/依赖/集合臂近线性；10x 输入平均耗时增长不得超过 30x，沿用 B1 门限风格 |

冷目录 P99 的绝对毫秒值依赖部署 RTT，建议暂定同地域数据库 ≤25 ms、硬超时 `min(100ms, statement_timeout/10)`；此值需用真实部署数据**待确认**。超时一律 deny。

优化要求：

- policy 在 `StageLoad` 一次构建 hash index；单依赖 O(1) 查找。
- parser 在一次 AST walk 同时产出 output/reference lineage，避免第二次全树扫描。
- catalog 批量解析所有关系/列/视图，禁止逐列查询。
- 不跨请求缓存 wildcard 列表或视图定义，除非有执行前可验证版本；安全优先于命中率。
- 审计明细构建与序列化有数量/字节上限。

## 12. 实现切片与独立验收

### S1：权限语义与规范化存储

- 新增子表迁移、preflight、repository 事务、SQLite→PG 搬迁。
- 扩展 `model.Policy` / `PolicyDecision` / resolver 与 API 兼容 DTO。
- 验收：迁移矩阵、legacy 回填、CRUD 原子性；不接入运行时。

### S2：投影外引用血缘

- 两 parser 产出 `ReferenceLineages`；补 JOIN ON/USING、WHERE/GROUP/HAVING/ORDER、窗口/control。
- 验收：双方言 unit/fuzz/hard limit；B1 `ProjectionLineage` golden 不变化。

### S3：纯列授权器

- 新建不可变 `ColumnAuthorizer`，实现 usage 判定、合并格、原因码、bounded trace。
- 抽取 B1 保护计划供授权审计与 redactor 共用。
- 验收：完整决策矩阵与负向语料的纯单元测试。

### S4：静态强制门接入 pipeline

- 在规则开关之外接入 MandatoryAuthStatic；表/profile deny 与 resolved 列 deny 零 DB 调用。
- 处理 `RequireApproval` / `ExplainOnly` / R010 disabled。
- 验收：spy executor 断言、现有 pipeline/approval/audit barrier 不回归。

### S5：目录、wildcard、NATURAL 与视图穿透

- 实现 PostgreSQL/MySQL `AuthorizationCatalog`、请求级同 session 解析、RouteView、fingerprint。
- MandatoryAuthResolve 位于 EXPLAIN 前。
- 验收：真库 wildcard/NATURAL/view/CTE 负向用例；目录故障 fail-closed。

### S6：审计与 API/前端最小可用

- `DetailsJSON.column_auth`、导出/事件流、审计页展示；权限页 usage 编辑。
- 验收：无明文泄漏、大小上限、旧审计兼容、UI 测试。

### S7：双方言真库 JOIN/自连接总验收

- PostgreSQL/MySQL e2e、DML 读取依赖、视图变更、运行时列对齐。
- 全量权限/脱敏/审计链回归。
- 验收：第 10.3 节负向逃逸全部阻断。

### S8：性能、运维与发布闸门

- benchmark/P99、迁移演练、回滚演练、release notes、运维指标和告警。
- 验收：第 11 节预算；解决或正式豁免“第三查询方言”阻塞项。

## 13. 风险与回退

| 风险 | 影响 | 缓解/回退 |
|---|---|---|
| R010/甚至 R003 可被 rule override 关闭 | 授权绕过 | MandatoryAuth 独立于 engine rules；不可配置 |
| B1 当前无真实 view 底表展开 | 视图隐藏受保护列 | S5 强制目录展开；失败 deny |
| 顶层 ON/WHERE 等无独立权威血缘 | 条件列逃逸 | S2 建完整 reference lineage；负向语料锁定 |
| `HasBroadTableGrant` 绕过列 ACL | schema/global grant 提权 | B2 明确列约束优先；升级说明 |
| 元数据与执行 TOCTOU | 视图或 star 在判定后变化 | 同 session、fingerprint、运行时结果 schema 复核；不安全缓存禁用 |
| 合法裸列因无目录而 ambiguous | 误拒绝 | 目录可证明时解析；否则坚持 deny |
| legacy CSV 脏数据 | 迁移失败 | 预检列出 policy ID；事务回滚，管理员先修复 |
| policy 子表与 legacy CSV 漂移 | 不同版本行为不一致 | repository 单事务双写；新运行时只信子表；一致性启动检查 |
| 视图底表双重表授权影响现有安全视图 | 大量新增 deny | 发布前 dry-run 报告（只报告，不放行）；补策略后上线；紧急时回滚二进制 |
| 审计 details 过大 | 写放大、链吞吐下降 | 256 项/32 KiB 上限、摘要与 digest |
| 第三 query dialect 不存在 | 无法满足字面验收 | 产品决策修改口径或另立方言 epic |
| parser 文档版本落后 | 设计/测试基线混乱 | 以 go.mod 的 Vitess v0.22.4 为准，实施时修正文档 |

回退顺序：先停止新版本流量，回滚旧二进制；因 `policies.columns` 双写保留，旧版本可继续读取。不要删除子表、不要逆向迁移、不要通过关闭 MandatoryAuth 恢复流量。回退后 JOIN 列 ACL 缺口重新出现，必须在变更记录中标为临时风险并限制相关 Agent/API key。

## 14. 实施前待确认项

1. “三方言”具体指哪三个业务查询方言；当前代码只有 PostgreSQL/MySQL。
2. PostgreSQL materialized view 是否同时要求底表表/列授权；本设计默认要求。
3. 冷目录 P99 的部署 RTT 基线与最终硬超时。
4. 新 schema 浏览 API 是否复用 controlled-read，还是由 AuthorizationCatalog 提供独立管理端只读接口。
5. DML `RETURNING` 与 MySQL 方言扩展的精确覆盖边界；原则是不允许读取依赖绕过，写目标列细分仍非 B2。

除这些显式待确认点外，安全默认不依赖配置：无法证明即 deny。

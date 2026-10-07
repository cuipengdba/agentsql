# DM8 / Oracle dialect 能力与验证边界

> 状态：v0.5 批十、批十三、批二十、批二十一、批二十八与批三十二的累计说明；不是完整兼容或生产认证声明。最近 DM8 真库回归日期：2026-10-03。

## 批六十三：离线 parser 边界

本批仅验证 `internal/parser` 的 SQL 结构、AST 标志与列血缘，不连接 DM/Oracle 实例，也不改变执行器放行规则。新增表驱动用例覆盖：

1. Oracle `ROWNUM < n`、`<= n`、`= n` 在简单 `WHERE` 中识别为行数约束，不将 `ROWNUM` 伪列记为物理列。
2. `ROWNUM` 投影、反向比较、`>`、负数/小数、多重条件、`OR` 和 `JOIN ON` 形态保持 `ErrUnparseable`；DM 的未确认 `ROWNUM` 形态同样拒绝。
3. DM/Oracle 的 `ORDER BY` 投影别名和正整数序号不再产生虚假的物理列依赖；零、越界、小数序号及重复别名拒绝。
4. `NULL = NULL` 和 `NULL != NULL` 的真值保持未知，不能误报 `WhereTautology`；`NULL IS NULL` 仍识别为恒真。
5. Oracle 空字符串按 NULL 处理：`'' IS NULL` 为真，`'' IS NOT NULL` 为假，`'' = ''` 不判为恒真；DM 空字符串谓词的模式语义未由本批确认，恒真判断保持未知。
6. DM `LIMIT n OFFSET m`、`LIMIT m,n`、`TOP n` 与 Oracle `OFFSET/FETCH` 的既有受控形态增加离线回归；负数、不完整分页、DM `TOP` 混用尾部分页、Oracle `TOP/LIMIT` 与未支持的 `WITH TIES` 拒绝。
7. Oracle `ALTER SESSION`、`MERGE INTO` 和 DM `BACKUP/RESTORE` 保持只读 parser 的 `ErrUnparseable` 边界。

以上是本地 parser 行为测试，不构成对任意 DM 兼容模式或 Oracle 服务端版本的语法认证。

## v0.5 当前接入结论

| 路径 | DM8 | Oracle |
| --- | --- | --- |
| 控制面数据源登记与 Ping | `db_type=dm`，原生驱动；以指定 schema 建连 | `db_type=oracle`，`go-ora/v2`；`database` 填 service/PDB 名 |
| 底层只读查询 | `limitedSQLExecutor` 的单条窄 SELECT；`TOP`、`LIMIT` 与 `FETCH` 有界支持 | 同一路径的窄 SELECT；仅 `FETCH`/`ROWNUM`，拒绝 `TOP`/`LIMIT` |
| 元数据与采样 | 固定 `SYS.ALL_TAB_COLUMNS` 查询、绑定参数；typed sample 用 `LIMIT n` | 固定 `ALL_TAB_COLUMNS` 查询、绑定参数；typed sample 用 `FETCH FIRST n ROWS ONLY` |
| 解析与血缘 | `parser.NewParser("dm")` 的受控 SELECT 子集 | `parser.NewParser("oracle")` 的受控 SELECT 子集 |
| 完整网关授权、列级权限与脱敏 | 尚未接通：规则引擎只接受 PG/MySQL/SQL Server AST，`controlledread` 的 metadata 与采样 builder 也没有 DM 分支 | 同左 |
| INSERT / UPDATE / DELETE / DDL、写事务 | 底层 `Execute` 和 `BeginWriteTx` 拒绝 | 同左 |
| TLS / 通信加密 | 当前驱动适配没有实现可验证的配置；显式 TLS 选项会被拒绝 | 同左 |

因此，数据源登记、Ping、底层只读能力和独立 parser 不能等同于可用的 Gateway CRUD。DM/Oracle 上的创建、查询、更新、删除业务闭环都不得按 PostgreSQL 路径宣传或部署。生产使用所需的只读授权闭环、列元数据绑定、加密通信、取消与连接复用仍需分别验证；不存在自动回退到 PG/MySQL 方言的行为。

当前记录的 Oracle 真库版本是 **Oracle AI Database 26ai Free 23.26.3.0.0**（`FREEPDB1`）。这份记录不证明单独命名的 Oracle Database 23ai Free 镜像或商业版兼容。DM8 的 `-2501` 是已实测的认证失败分类；测试账号及口令仅属历史 fixture，不是部署凭据。`go.mod` 当前声明 Go 1.26.0；下文关于 Go 1.25 驱动选择的文字是当时的决策记录。

离线回归可运行 `go test -short ./internal/parser ./internal/authorizedexecute/internal/businessdb ./internal/adminapi`；DM/Oracle 真库 E2E 需要各自显式启用的测试环境，短测试通过不能替代真库验收。v0.5 的 PostgreSQL parser P99 发布门槛仍单独保持未通过状态，DM/Oracle 的这组测试不改变该门槛。

## 批三十二：受控 SELECT parser 与投影血缘

`parser.NewParser("dm")` 和 `parser.NewParser("oracle")` 现已注册独立的 Oracle-compatible 只读分析器。它是 v0.5 冻结 profile：DM 语法基线只采用批二十八在 `COMPATIBLE_MODE=0`、Pack3 实例得到的证据，Oracle 基线采用本文记录的 Oracle Free 23.26.3 路径；parser 不根据未知服务端版本自动扩语法。该注册只增加只读 AST/血缘分析能力，没有修改 `limitedSQLExecutor` 的查询放行或拒绝逻辑，也没有把 DM/Oracle 别名到 PostgreSQL/MySQL parser。

支持范围严格限定为以下无括号、单条 `SELECT` 子集：

- `SELECT [DISTINCT]`；投影项只能是直接列（最多 `schema.table.column`）、字符串/数字/`NULL`/布尔常量、`*` 或 `alias.*`，支持投影别名；
- 必须有 `FROM` 物理关系，只接受单表、多表逗号连接，或 `INNER/LEFT/RIGHT/FULL/CROSS JOIN`；非 CROSS JOIN 必须带无括号 `ON` 简单谓词；表别名采用 Oracle-compatible 的无 `AS` 形式；
- `WHERE`/`ON` 只接受直接列或常量之间的 `= != <> < <= > >= LIKE`、`IS [NOT] NULL`，以及 `AND`/`OR`；`ORDER BY` 只接受直接列与 `ASC/DESC`；
- DM 仅额外接受真库已证明的 `TOP n`、`LIMIT n`、`LIMIT n OFFSET m`、`LIMIT m,n` 和 `OFFSET ... ROWS FETCH ... ROWS ONLY`；Oracle 仅接受 `OFFSET/FETCH`，继续拒绝 `TOP/LIMIT`；
- 输出 `AST.Tables`、`AST.Columns`、`DirectProjections` 与 `ProjectionLineages`。直接列记录唯一物理来源；无限定多表列标记为 `ambiguous` 且不猜测 dependency；常量为 `source_free`；通配符为 variadic lineage。parser 不持有 catalog，所以 `*` 的“展开”只精确到对应物理表的通配列集合，不能虚构实际列名；真正逐列展开仍需调用方提供 metadata。

下列结构即使厂商 SQL 本身可能合法，也统一返回包含 `ErrUnparseable` 的明确错误：函数和任何括号、子查询、CTE、算术或 `CASE` 表达式、`GROUP BY/HAVING`、集合操作、`CONNECT BY`、`NATURAL/USING JOIN`、注释、bind、分号/多语句、非 `SELECT` 语句、无法解析到当前 `FROM` 作用域的限定列，以及所有未列出的结构。该 fail-closed profile 是完整厂商 grammar 的真子集，不代表完整 parser、列级授权/脱敏 pipeline 或厂商认证。

## 批二十八：DM8 真库端到端回归

### 环境与驱动

- 容器 `dm8-v06` 持续运行，镜像 `dm8_single:dm8_20241022_rev244896_x86_rh6_64`，端口映射 `127.0.0.1:5236->5236/tcp`；TCP 探测成功。
- disql 实测 `SYSDBA/"Agentsql@2026"@localhost:5236` 登录成功，实例 `STATUS$=OPEN`，版本 `03134284294-20241009-244896-20119 Pack3`，`SF_GET_CASE_SENSITIVE_FLAG()=1`，`COMPATIBLE_MODE=0`。
- 服务端 `ENABLE_ENCRYPT=0`、`AUTO_ENCRYPT=0`、`FORCE_CERTIFICATE_ENCRYPTION=0`，`AUTH_ENCRYPT_NAME` 与 `COMM_ENCRYPT_NAME` 均为空；本批没有启用 SSL 证书或通信加密 DSN 参数。驱动自身的登录口令握手配置与服务端通信加密不是同一开关。
- `go.mod` 已有 `github.com/godoes/gorm-dameng v0.7.2`，运行时 import 为 `github.com/godoes/gorm-dameng/dm8`、注册名为 `dm`，所以本批不新增或升级驱动。含原始 `@` 的密码通过真实 Go Ping；该版本按最后一个 `@` 分割主机，不能改用会保留字面 `%40` 的通用 URL userinfo 编码。
- 供应链仍 fail-closed：v0.7.2 的 Go module 压缩包和本机 module cache 不含 `LICENSE`、`COPYING` 或 `NOTICE`。上游当前主分支后来出现 MIT `LICENSE`，但不能据此反推 v0.7.2 发布物及其中整理的 DM 官方驱动源码已明确授权再分发；进入发行物前仍需法务/供应链确认。

### 真实差异与修复

| 适配面 | 批二十一离线假设 | 批二十八真实 DM8 结果与处理 |
| --- | --- | --- |
| 当前 schema | 构造后执行 `SELECT USER` 并把登录用户当 metadata owner | DSN `schema=` 由驱动执行 `SET SCHEMA`；真实结果可出现 `USER=只读账号`、`CURRENT_SCHID=owner schema`。改为 `SF_GET_SCHEMA_NAME_BY_ID(CURRENT_SCHID)`，未限定 schema 的列发现现在跟随 DSN owner |
| 分页 | DM typed 采样生成 `LIMIT n`；门禁接受 `TOP/LIMIT/FETCH/ROWNUM` | `LIMIT 2 OFFSET 1` 返回 2、3；`LIMIT 2,1` 返回 3；`OFFSET 1 ROWS FETCH NEXT 2 ROWS ONLY` 返回 2、3；`TOP 2` 返回 1、2；offset 0 返回首页，offset 99 和 2147483647 返回空集 |
| EXPLAIN 结果集 | `EXPLAIN FOR` 预期为 19 列 | 列顺序与 19 列定义一致。二级索引真实树为 `NSET2/PRJT2/BLKUP2/SSEK2`，归一化 `rows=1 cost=1`；非索引条件真实树为 `NSET2/PRJT2/SLCT2/CSCN2`，根估算 `rows=62 cost=1`，底层扫描 2505 行 |
| EXPLAIN 操作符范围 | 只接受离线样例白名单 | 系统视图真实计划另出现 `PIPE2`、`UNION ALL`、`ACTRL`、`DISTINCT`、`DSCN`、`HEAP TABLE SCAN` 等。本批没有仅凭一次样本扩大授权白名单；这些形态继续 fail-closed，避免把未知计划误判为安全 |
| 类型与值表示 | 保留 `DATA_TYPE` 厂商字符串 | 真库发现顺序为 `INT/BIGINT/VARCHAR/VARCHAR/DECIMAL/TIMESTAMP/DATE/VARBINARY/CLOB/BLOB`。`DECIMAL(12,2)` 值 `0.01` 经 v0.7.2 驱动读取为字符串 `.01`；本批记录真实输出，不擅自补零改变值表示 |
| 错误码 | `-2111` 未分类 | 真库确认 `-2111=Invalid column name`，现归一为 `DB_COLUMN_NOT_FOUND`；`-2501` 认证、`-2106` 对象、`-2007` 语法、`-5501/-5515` 权限分类也由真库复核 |

### 可重复端到端证据

`TestDMDiscoveryE2E` 使用 SYSDBA 仅做 fixture 管理，每次生成带纳秒后缀的唯一 owner 与只读账号，创建 2505 行测试表和二级索引；只读账号只显式获得 `CREATE SESSION` 与目标表 `SELECT`。测试顺序及真实结果如下：

1. owner 与只读账号均通过原生 Go 驱动连接；错误密码返回 `DB_AUTHENTICATION_FAILED/-2501`。
2. 只读账号以 owner 作为 DSN schema，真实观测 `USER=只读账号`、active schema=owner；`SYS.ALL_TABLES` 找到唯一 fixture 表，`SYS.ALL_TAB_COLUMNS` 找到 10 列及上述类型。
3. 受控查询正确读取 `O'Reilly @ 上海`；typed sample 的 `LIMIT 2` 返回 `[[1 K0001] [2 K0002]]`。
4. 分页得到首页 `[[1] [2] [3]]`、末页 `[[2504] [2505]]`、超大 offset 空集；大结果用 row limit 2000 返回 `rows=2000 truncated=true`。
5. 索引 EXPLAIN 归一化为 `1|0|NSET2|1|1`、`2|1|PRJT2|1|1`、`3|2|BLKUP2|1|1`、`4|3|SSEK2|1|1`；顺序扫描归一化为 `NSET2/PRJT2/SLCT2/CSCN2`。`Raw` 不含表名、索引名或 SQL 字面量。
6. 不存在列、对象和语法错误分别得到 `-2111/-2106/-2007`；只读账号直接尝试 INSERT 与 CREATE TABLE 分别得到 `-5501/-5515` 并归一为权限不足。
7. 池连接和独占 session 均能读取 fixture；测试 cleanup 关闭连接后删除两个唯一测试用户（owner 的 schema、表、索引和 2505 行随 `CASCADE` 删除），再查询 `SYS.DBA_USERS` 断言残留数为 0。

真实回归命令使用 Go 1.26 Linux/CGO 环境：

```text
AGENTSQL_DM_E2E=1 DM_HOST=host.docker.internal DM_PASSWORD=<secret> \
go test ./internal/authorizedexecute/internal/businessdb -run '^TestDMDiscoveryE2E$' -count=1 -v
```

### 未覆盖与阻塞

- 批二十八当时 `parser.NewParser("dm")` 仍返回 `ErrUnsupportedDialect`，所以该提交本身只证明窄 SELECT 门禁、对象查询、元数据和 EXPLAIN；批三十二已补上方所列的有界 parser/投影血缘，但不追溯改写批二十八的测试结论。
- 未验证 SSL、通信加密、LOB 内容读取/写入、时区转换、context cancel 后物理连接复用、写事务和完整 Gateway 授权/脱敏/审计闭环。
- Windows 主机的 Go 环境为 `CGO_ENABLED=0`，不能编译仓库既有 `pg_query_go` parser；本批用已有 `golang:1.26-bookworm` 镜像运行 Linux CGO 测试。businessdb 无过滤全量测试还包含自动启动 PostgreSQL/MySQL testcontainers 的用例，未向构建容器授予 Docker socket；DM/Oracle 定向离线集与 DM 真库 E2E 均独立通过。

## 0. 能力矩阵（批二十一基线，DM 由批二十八更新）

本节保留批二十一基线和 Oracle 口径；DM 的当前真库口径以上方批二十八章节为准。批二十一当时不连接外部数据库，DM EXPLAIN 结论仅来自官方文档样例；这些历史离线测试不追溯表述为真库实测。

| 能力 | DM8 | Oracle |
| --- | --- | --- |
| 连接与池 | 原生 `dm` 驱动、Ping、当前 schema、池和独占物理会话已实现 | `go-ora/v2`、Ping、`CURRENT_SCHEMA`、池和独占物理会话已实现 |
| metadata | 固定查询 `SYS.ALL_TAB_COLUMNS`；`?` bind；有界结果读取；批二十八修复为按 DSN active schema 选择默认 owner | 固定查询 `ALL_TAB_COLUMNS`；`:N` bind；有界结果读取 |
| 只读 SQL | 极窄单条 `SELECT`；池与 session 共用 fail-closed 门禁 | 同左 |
| 分页与伪列 | 门禁接受 `TOP`、`LIMIT`、`FETCH`、`ROWNUM`；typed 采样固定生成 `LIMIT n` | 门禁接受 `FETCH`、`ROWNUM`，明确拒绝 `TOP`、`LIMIT`；typed 采样固定生成 `FETCH FIRST n ROWS ONLY` |
| `DUAL` | 可作为普通对象出现在窄 SELECT 中，不做兼容模式改写 | 可作为普通对象出现在窄 SELECT 中；离线计划覆盖 `FAST DUAL` |
| 序列 | `NEXTVAL`、`CURRVAL` 均拒绝 | `NEXTVAL`、`CURRVAL` 均拒绝 |
| EXPLAIN | 批二十八已真库验证 19 列 `EXPLAIN FOR` 的索引/顺序扫描形态；文本样例与结构化结果共用 `ExplainInfo` 归一化；未知列、操作符、树形或数值 fail-closed | 结构化读取 `PLAN_TABLE`；校验唯一根、完整 parent、无环、非负有限估算值；识别普通、bitmap 和 domain index |
| 错误分类 | 认证、语法、权限、连接、列/对象/模式不存在、锁超时、唯一约束；批二十八新增真实 `-2111` 列不存在 | 认证、权限、语法、列/对象不存在、约束、中断、连接，以及资源忙/死锁、临时空间/共享池资源不足 |
| 类型 | 保留厂商 `DATA_TYPE` 字符串；尚无 canonical type 映射 | 同左；`NUMBER`、日期、LOB、JSON/BOOLEAN/Vector 等仍待独立语义设计 |
| 写入/事务/触发器 | 未实现；`Execute`、`WriteTx` 保持 fail-closed | 同左 |
| parser/授权闭环 | v0.5 受控 SELECT profile 的对象、列和投影血缘已实现，未知结构 fail-closed；尚未接入完整规则、列级授权和脱敏 pipeline | 同左；Oracle 分页只接受 `OFFSET/FETCH`，明确拒绝 `TOP/LIMIT` |

### 0.1 批二十、批二十一离线验证

- 方言 corpus 覆盖 DM `TOP/LIMIT/FETCH/ROWNUM`、Oracle `FETCH/ROWNUM/DUAL`、Oracle 对 `TOP/LIMIT` 的拒绝，以及字符串/双引号内关键字与真实注释的边界。
- typed 采样 SQL 通过纯 builder 黄金值验证；这只证明生成语句，不代表上层 `Gateway.Sample` 已接通 DM/Oracle 授权链路。
- 错误分类通过构造 `dm8.DmError` 与 `network.OracleError` 验证，不把模拟错误当作真库实测。
- Oracle EXPLAIN 通过内联 `model.QueryResult` fixture 验证普通索引、bitmap/domain index、`FAST DUAL`、多根、缺 parent、环、非法数值和控制字符；对象名仍不进入 `Raw`。
- DM EXPLAIN 文本 fixture 验证官方复杂连接树与显式二级索引树；结构化 fixture 验证官方 `EXPLAIN AS A1 FOR` 四节点结果。根节点 `[cost, rows, bytes]` 归一化为 `EstCost`/`EstScanRows`，`CSEK/SSEK/SSCN/BLKUP` 形成索引信号，`CSCN` 形成顺序扫描信号。
- DM 归一化 `Raw` 仅保留节点号、层级、操作符、估算行数与代价，不保留表名、索引名、谓词或 SQL 字面量；文本/结构化输入均限制大小、节点数和深度，并拒绝未知操作符、非法树形、负数与非有限代价。

### 0.2 DM EXPLAIN fixture 来源

- [达梦官方《数据查询语句》4.19.1 EXPLAIN](https://eco.dameng.com/document/dm/zh-cn/pm/check-phrases.html)：复杂 `NSET2/PRJT2/HASH LEFT SEMI JOIN2/CSCN2/CSEK2/BLKUP2/SSEK2` 文本树及 `[cost, rows, bytes]` 三元组。
- [达梦官方《数据查询语句》4.19.2 EXPLAIN FOR](https://eco.dameng.com/document/dm/zh-cn/pm/check-phrases.html)：19 列结果集定义对应的 `EXPLAIN AS A1 FOR` 四节点样例。
- [达梦官方《数据查询语句》4.22 指定索引查询](https://eco.dameng.com/document/dm/zh-cn/pm/check-phrases.html)：`BLKUP2 + SSCN` 与 `BLKUP2 + SSEK2` 文本树。
- [达梦官方《附录 4：执行计划操作符》](https://eco.dameng.com/document/dm/zh-cn/pm/dm8-admin-manual-appendix4.html)与[《查询优化》EXPLAIN FOR 列说明](https://eco.dameng.com/document/dm/zh-cn/pm/query-optimization.html)：操作符语义及 `LEVEL_ID/OPERATION/ROW_NUMS/COST` 字段含义。

### 0.3 明确限制与历史待办

- 窄门禁与批三十二 parser 都不是完整 SQL parser：函数、括号、子查询、CTE、bind、注释和尾部分号即使是合法 SQL 也拒绝；未引用标识符若与受限关键字重名也会 fail-closed。
- DM EXPLAIN、Ping、metadata、分页、权限和主要错误分类已在批二十八使用有效凭据复跑；context cancel 后连接复用、SSL/通信加密及更广操作符仍待验证。
- Oracle 本批没有外部实例；新增分页、错误码和计划形态均待 Oracle 真库回归。批十三已有环境证据不等于批二十重新实测。
- DM/Oracle 的 canonical 类型、LOB/时区/字符集、完整厂商 grammar、catalog 驱动的 `*` 逐列展开、列级授权/脱敏、事务、触发器、取消后连接复用仍未验证。

## 1. 批十初始结论（历史记录）

DM8 和 Oracle 都不能映射为现有 PostgreSQL/MySQL dialect。两者需要独立的连接、系统目录、SQL parser/规则能力、执行计划解析、错误分类和事务状态实现。

本批只把可信闭环做到以下边界：

- `db_type=dm` / `db_type=oracle` 已注册到 `businessdb.openExecutor`；
- 两个 dialect 都能建立 `database/sql` 连接池、执行有截止时间的 Ping、探测当前 schema、获取/释放独占 `sql.Conn` 会话，并关闭连接池；
- `businessdb.ListSchema` 使用固定 SQL、绑定参数和有界结果读取访问各自系统目录；
- DM8 与 Oracle 均已针对真实容器通过 Ping 和一个固定对象的列发现；
- 普通 Query、Execute、会话内 SQL、WriteTx、EXPLAIN 全部明确 fail-closed，返回统一且不包含驱动原文的数据库执行错误；只读执行器的写操作仍优先返回只读拒绝。

本批没有注册 DM/Oracle parser，没有放开控制面 datasource 校验，没有实现采样、业务查询、写入、事务状态、执行计划归一化、列级授权、脱敏或完整审计闭环。因此不能把本批结果表述为“AgentSQL 已支持 DM/Oracle”；准确表述是“连接与 typed metadata 最小切片已实测”。

## 2. 实测环境与证据

| 项目 | DM8 | Oracle |
| --- | --- | --- |
| 容器 | `dm8-v06`，镜像 `dm8_single:dm8_20241022_rev244896_x86_rh6_64`，`127.0.0.1:5236` | `oracle-v06`，镜像 `container-registry.oracle.com/database/free:latest`，`127.0.0.1:1521` |
| 健康状态 | 运行中 | 运行中且 Docker health=healthy |
| 服务端版本 | `DM Database Server 64 V8`；`03134284294-20241009-244896-20119` | `Oracle AI Database 26ai Free Release 23.26.3.0.0` |
| 关键配置 | `SF_GET_CASE_SENSITIVE_FLAG()=1`；`COMPATIBLE_MODE=0` | SID `FREE`；服务名 `FREE`；可写 PDB/服务 `FREEPDB1`；`compatible=23.6.0` |
| 许可证 | `V$LICENSE.EXPIRED_DATE=2026-10-15` | Free 镜像 |
| 原生客户端 | `/opt/dmdbms/bin/disql`；运行需将 `/opt/dmdbms/bin` 加入 `LD_LIBRARY_PATH` | `/opt/oracle/product/26ai/dbhomeFree/bin/sqlplus`；`/ as sysdba` 可用 |
| Go E2E | `TestDMDiscoveryE2E`：Ping；从 `SYS.ALL_TAB_COLUMNS` 发现自身 31 列 | `TestOracleDiscoveryE2E`：Ping；从 `SYS.DUAL` 发现列 |

DM `EXPLAIN` 返回多行文本树，例如 `#HASH LEFT SEMI JOIN2: [cost, rows, bytes]`、`#CSEK2`、`#CSCN2`，没有 PostgreSQL JSON envelope；官方另提供以 19 列结果集返回的 `EXPLAIN FOR`。当前驱动的标准 `database/sql` 查询通道不能读取普通 `EXPLAIN` 的内部文本，因此运行时适配选择 `EXPLAIN FOR`，离线文本 parser 用于逐字锁定官方样例 grammar，两者汇合到相同节点归一化器。Oracle 需要先执行 `EXPLAIN PLAN ... FOR ...`，再结构化读取 `PLAN_TABLE`。这些格式都不交给现有 PostgreSQL/MySQL parser 猜测解析。

## 3. 驱动与 DSN 决策

### 3.1 DM8

本批使用 `github.com/godoes/gorm-dameng/dm8` v0.7.2 中整理的 DM 官方 Go 驱动源码；驱动注册名为 `dm`，基于 `database/sql`，不要求 CGO。DM 官方文档给出的 DSN 基形为 `dm://user:password@host:port[/schema]?...`，默认端口为 5236。

实测发现 v0.7.2 的 DSN parser 会自行按字符串切分 userinfo，却不会反解 `url.UserPassword` 产生的百分号编码。密码含 `@` 时，常规 URL builder 会发送字面 `%40` 并导致 `-2501 用户名或密码错误`；驱动又使用最后一个 `@` 作为主机分隔，因此本实现为该版本保留密码内的原始 `@`。对无法无歧义表示的用户名冒号、`?`、控制字符和 schema 查询分隔符直接拒绝，不能猜测解码。升级驱动时必须保留该回归用例并重新核验 DSN 行为。

驱动来源和再分发许可证仍需法务/供应链准入；当前依赖不能仅因技术可用而视为已批准分发。

参考：[DM Go 编程指南](https://eco.dameng.com/document/dm/zh-cn/pm/go-rogramming-guide.html)、[godoes/gorm-dameng](https://github.com/godoes/gorm-dameng)。

### 3.2 Oracle

已知的 `github.com/godror/godror` 路径确实使用 ODPI-C/OCI，需要 CGO，并在运行时需要 Oracle Client。服务名应优先使用 Easy Connect 的 `host:port/service_name`；SID 只能通过完整 Connect Descriptor 明确表达，不能把 SID 和 service name 混用。

Oracle 在 2026 年提供了官方纯 Go `github.com/oracle/go-oracledb/v26`，但本批下载核验的 v26.0.0-beta / v26.0.1-beta 分别要求 Go 1.26.5 / 1.26.6，高于本仓库的 Go 1.25 基线。为了在当前基线完成真实连接与 metadata 验证，本批暂用纯 Go `github.com/sijms/go-ora/v2` v2.9.0，驱动名 `oracle`，DSN 由其 `BuildUrl` 生成；这是一项有边界的工程替代，不是最终生产驱动定案。

后续准入必须在三者中明确选择并记录：

1. `godror + OCI`：成熟但增加 CGO、Oracle Client 镜像、动态库、补丁和多架构供应链；
2. 官方 `go-oracledb/v26`：纯 Go，但当前是 beta 且要求升级项目 Go 版本；
3. `go-ora/v2`：当前最小切片已连通 26ai，但仍需安全、类型、取消、加密、故障和许可证矩阵，不能由一次 Ping 外推生产适用性。

参考：[godror README](https://github.com/godror/godror/blob/main/README.md)、[Oracle Database Driver for Go](https://github.com/oracle/go-oracledb)、[go-ora](https://github.com/sijms/go-ora)。

## 4. 方言差异清单

| 适配面 | PostgreSQL / MySQL 现状 | DM8（`COMPATIBLE_MODE=0`） | Oracle 26ai |
| --- | --- | --- | --- |
| 连接目标 | PG database；MySQL database | 5236；登录 schema；官方协议/驱动 | 1521；优先 service/PDB（本次 `FREEPDB1`），SID 需 descriptor |
| 驱动约束 | pgx 纯 Go；mysql 纯 Go | 本批驱动纯 Go；DSN 特殊字符解析有已确认限制；再分发待审 | godror 需 CGO+OCI；本批 go-ora 纯 Go；官方纯 Go 驱动暂超 Go 基线 |
| 占位符 | PG `$1`；MySQL `?` | `?` | `:1` / named bind |
| metadata | `information_schema.columns` | `SYS.ALL_TAB_COLUMNS` / `USER_TAB_COLUMNS` 等；建库参数不全在 `V$DM_INI` | `ALL_TAB_COLUMNS` / `USER_TAB_COLUMNS` / DBA 权限下 `DBA_*`；容器/PDB 会改变可见对象 |
| schema/catalog | PG schema；MySQL database 即 schema | 用户默认 schema，Oracle 风格但不等同 Oracle | 当前 schema、CDB/PDB、common/local user 和 service 共同决定可见范围 |
| 标识符 | PG/Oracle 风格双引号；MySQL 反引号 | 未加引号通常按初始化大小写规则折叠；本实例大小写敏感；双引号保留精确名称 | 未加引号通常折叠为大写；双引号保留大小写；标识符长度随版本/compatible 设置变化 |
| 类型 | PG 丰富原生类型；MySQL `column_type` | `VARCHAR`、`NUMBER/DEC`、`DATETIME/TIMESTAMP`、LOB、二进制和厂商类型；不能直接套 PG OID | `VARCHAR2/NVARCHAR2`、`NUMBER(p,s)`、`DATE/TIMESTAMP`、RAW、LOB、JSON/BOOLEAN/Vector 等；`NUMBER` 不能无条件转 float |
| 行限制 | `LIMIT n` | 支持 `TOP`/`LIMIT`/`FETCH` 的范围受版本和兼容模式影响，必须以 corpus 固化 | `FETCH FIRST n ROWS ONLY` 或 `ROWNUM`；不能拼接 PostgreSQL `LIMIT` |
| 分号/脚本 | 驱动通常接收单条无分号 SQL | disql 的 `/`、PL/SQL 风格块与单条驱动执行需分离 | OCI/驱动单条执行通常不接收 SQL*Plus 终止符；PL/SQL 块语义不同 |
| EXPLAIN | PG JSON；MySQL 表格 | `EXPLAIN` 多行专有文本树 | `EXPLAIN PLAN FOR` 写 plan table，再由 `DBMS_XPLAN` 或结构化 plan table 读取 |
| 错误码 | SQLSTATE / MySQL number 已分类 | DM 数字错误码需建立白名单分类；未知码归 execution | `ORA-xxxxx` / 驱动码需建立白名单分类；未知码归 execution |
| 只读/超时/取消 | 已有专用实现 | 需验证 session/transaction 只读、context cancel 后连接可复用性 | 需验证 `ALTER SESSION`/事务只读、call timeout、break/reset 和池复用 |

## 5. 代码设计

新增的 `DMExecutor` 和 `OracleExecutor` 都嵌入 `limitedSQLExecutor`：

- 构造器先校验 datasource 和连接池参数，再 `sql.Open`、设置池上限、Ping，并以固定查询探测当前 schema；任何一步失败都关闭池；`OpenSession` 可绑定和释放一个独占物理连接，但其 SQL/事务方法继续拒绝；
- `Dialect()` 精确返回 `dm` 或 `oracle`；`Manager.SnapshotPools` 可读取标准 `database/sql` 池统计；
- typed metadata 只接受 `SchemaTable` 身份，拒绝非法标识符，最多 256 个关系；查询模板固定，owner/table 全部使用 bind，不把输入拼入 SQL；结果仍受 256 列、单元格/行/总字节和 10,000 行上限约束；
- metadata 驱动错误在能力边界内转成无驱动原文的统一 `DBError`；
- DM `limitedDialectExplainer` 执行 `EXPLAIN FOR <SQL>`，严格读取官方 19 列并归一化为公共 `model.ExplainInfo`；池与 session 继续复用既有 `Explain` 超时、只读校验和错误通道；
- 尚未验证的能力统一在 `limitedSQLExecutor` 返回 `DBErrorCodeExecution`，未知执行计划格式也绝不产生零值计划或放行结论。

当前授权与发现调用关系保持不变：

```text
Gateway.ListSchema -> Manager/openExecutor -> DM/Oracle pool -> businessdb.ListSchema -> fixed catalog SQL

Gateway.Sample -> AuthorizedExecute -> parser/rules/EXPLAIN -> executor.Query
                                      (DM/Oracle 仅注册 v0.5 受控 SELECT profile；完整规则/列授权仍未接入)
```

特别地，本批没有让 `Gateway.Sample` 绕过 `AuthorizedExecute` 去调用底层数据库，也没有把 DM/Oracle 假装成 postgres/mysql。这样会牺牲完整敏感发现，但不会削弱授权、脱敏和审计边界。

## 6. 后续小批拆分

1. **控制面与完整 parser 准入**：受控 SELECT 的对象、列与投影血缘已实现；后续若要接入完整授权 pipeline，仍需显式评审 datasource API/store、规则集合、catalog `*` 展开，以及危险函数、CTE/子查询和 DML/DDL corpus。任何无法归一化的 AST 必须拒绝。
2. **EXPLAIN 真库验证与扩展**：用有效 DM 凭据比对普通 `EXPLAIN` 文本与 `EXPLAIN FOR` 结果集，核验计划记录生命周期，并仅依据新增官方/真库 fixture 扩充操作符白名单；Oracle 继续优先读取结构化 `PLAN_TABLE` 而非本地化 ASCII。
3. **只读查询与采样**：实现 dialect 自己的行限制器和仅 SELECT 分类；禁止尾部分号/多语句逃逸；接回 `Gateway.Sample` 后跑敏感类型识别、mask/hash/block/range 和截断用例。
4. **错误与取消**：按真实驱动错误类型建立认证、权限、对象不存在、语法、约束、死锁、资源、超时、取消分类；未知码固定 execution；验证取消后的物理连接处置。
5. **事务与写路径**：实现 Session/WriteTx、事务状态和 read-only 证据，再接审计 barrier；提交结果不确定时保持现有 fail-closed/unknown outcome 语义。
6. **驱动供应链**：决定 Oracle 生产驱动；审计 DM 驱动授权与升级来源；加入 Linux/Windows、amd64/arm64、TLS/加密、中文字符集、LOB、时区和故障恢复矩阵。
7. **完整验收**：使用非 SYS 最小权限账号和合成业务表跑“发现 -> 策略 -> 允许/拒绝 -> 脱敏 -> 审计”闭环。SYSDBA/SYSTEM 只用于本批环境调研，不是部署建议。

# DM8 / Oracle dialect 能力与验证边界

> 状态：v0.5 批十、批十三与批二十的累计说明；不是完整兼容或生产认证声明。最近离线深化日期：2026-10-03。

## 0. 当前能力矩阵（批二十）

本节是当前口径；后续章节保留批十的环境、驱动决策和历史设计背景。批二十不连接外部数据库，所有新增结论仅来自纯函数 corpus、结构化驱动错误 fixture 和 `PLAN_TABLE` 离线黄金样例。

| 能力 | DM8 | Oracle |
| --- | --- | --- |
| 连接与池 | 原生 `dm` 驱动、Ping、当前 schema、池和独占物理会话已实现 | `go-ora/v2`、Ping、`CURRENT_SCHEMA`、池和独占物理会话已实现 |
| metadata | 固定查询 `SYS.ALL_TAB_COLUMNS`；`?` bind；有界结果读取 | 固定查询 `ALL_TAB_COLUMNS`；`:N` bind；有界结果读取 |
| 只读 SQL | 极窄单条 `SELECT`；池与 session 共用 fail-closed 门禁 | 同左 |
| 分页与伪列 | 门禁接受 `TOP`、`LIMIT`、`FETCH`、`ROWNUM`；typed 采样固定生成 `LIMIT n` | 门禁接受 `FETCH`、`ROWNUM`，明确拒绝 `TOP`、`LIMIT`；typed 采样固定生成 `FETCH FIRST n ROWS ONLY` |
| `DUAL` | 可作为普通对象出现在窄 SELECT 中，不做兼容模式改写 | 可作为普通对象出现在窄 SELECT 中；离线计划覆盖 `FAST DUAL` |
| 序列 | `NEXTVAL`、`CURRVAL` 均拒绝 | `NEXTVAL`、`CURRVAL` 均拒绝 |
| EXPLAIN | 未实现，保持 fail-closed | 结构化读取 `PLAN_TABLE`；校验唯一根、完整 parent、无环、非负有限估算值；识别普通、bitmap 和 domain index |
| 错误分类 | 认证、语法、权限、连接，以及对象/模式不存在、锁超时、唯一约束 | 认证、权限、语法、列/对象不存在、约束、中断、连接，以及资源忙/死锁、临时空间/共享池资源不足 |
| 类型 | 保留厂商 `DATA_TYPE` 字符串；尚无 canonical type 映射 | 同左；`NUMBER`、日期、LOB、JSON/BOOLEAN/Vector 等仍待独立语义设计 |
| 写入/事务/触发器 | 未实现；`Execute`、`WriteTx` 保持 fail-closed | 同左 |
| 主 parser/授权闭环 | 未接入 DM parser、规则、列级授权和脱敏 pipeline | 未接入 Oracle parser、规则、列级授权和脱敏 pipeline |

### 0.1 批二十离线验证

- 方言 corpus 覆盖 DM `TOP/LIMIT/FETCH/ROWNUM`、Oracle `FETCH/ROWNUM/DUAL`、Oracle 对 `TOP/LIMIT` 的拒绝，以及字符串/双引号内关键字与真实注释的边界。
- typed 采样 SQL 通过纯 builder 黄金值验证；这只证明生成语句，不代表上层 `Gateway.Sample` 已接通 DM/Oracle 授权链路。
- 错误分类通过构造 `dm8.DmError` 与 `network.OracleError` 验证，不把模拟错误当作真库实测。
- Oracle EXPLAIN 通过内联 `model.QueryResult` fixture 验证普通索引、bitmap/domain index、`FAST DUAL`、多根、缺 parent、环、非法数值和控制字符；对象名仍不进入 `Raw`。

### 0.2 明确限制与待真库验证

- 窄门禁不是完整 SQL parser：函数、括号、子查询、CTE、bind、注释和尾部分号即使是合法 SQL 也拒绝；未引用标识符若与受限关键字重名也会 fail-closed。
- DM EXPLAIN 没有稳定、已采集的本实例 fixture，因此本批不解析、不宣称支持。
- DM `SYSDBA/SYSDBA` 仍已知返回 `-2501`；待提供有效凭据后复跑 Ping、metadata、分页查询、错误分类和取消/池复用。
- Oracle 本批没有外部实例；新增分页、错误码和计划形态均待 Oracle 真库回归。批十三已有环境证据不等于批二十重新实测。
- DM/Oracle 的 canonical 类型、LOB/时区/字符集、完整 parser、列级授权/脱敏、事务、触发器、取消后连接复用仍未验证。

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

DM `EXPLAIN` 返回多行文本树，例如 `#HASH LEFT JOIN2: [cost, rows, bytes]`、`#CSEK2`、`#CSCN2`，没有 PostgreSQL JSON envelope。Oracle 需要先执行 `EXPLAIN PLAN ... FOR ...`，再读取 `TABLE(DBMS_XPLAN.DISPLAY(...))`；本次实测输出为 ASCII 表格，包含 `Operation`、`Name`、`Rows`、`Cost (%CPU)`。这两种格式都不能交给现有 PostgreSQL/MySQL parser 猜测解析。

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
- 尚未验证的能力统一在 `limitedSQLExecutor` 返回 `DBErrorCodeExecution`，未知执行计划格式也绝不产生零值计划或放行结论。

当前授权与发现调用关系保持不变：

```text
Gateway.ListSchema -> Manager/openExecutor -> DM/Oracle pool -> businessdb.ListSchema -> fixed catalog SQL

Gateway.Sample -> AuthorizedExecute -> parser/rules/EXPLAIN -> executor.Query
                                      (DM/Oracle parser 尚未注册，因此 fail-closed)
```

特别地，本批没有让 `Gateway.Sample` 绕过 `AuthorizedExecute` 去调用底层数据库，也没有把 DM/Oracle 假装成 postgres/mysql。这样会牺牲完整敏感发现，但不会削弱授权、脱敏和审计边界。

## 6. 后续小批拆分

1. **控制面与 parser 准入**：在 datasource API/store 校验中显式接受新类型；为两种方言建立单语句 parser、注释/终止符、对象与列提取、危险函数、CTE/子查询、DML/DDL corpus。任何无法归一化的 AST 必须拒绝。
2. **EXPLAIN**：DM 建立有界文本 grammar；Oracle 优先读取结构化 `PLAN_TABLE` 列而非解析本地化 ASCII。限制原文大小、节点数、深度和数值范围；缺列、重复根、未知节点/数字溢出全部拒绝。
3. **只读查询与采样**：实现 dialect 自己的行限制器和仅 SELECT 分类；禁止尾部分号/多语句逃逸；接回 `Gateway.Sample` 后跑敏感类型识别、mask/hash/block/range 和截断用例。
4. **错误与取消**：按真实驱动错误类型建立认证、权限、对象不存在、语法、约束、死锁、资源、超时、取消分类；未知码固定 execution；验证取消后的物理连接处置。
5. **事务与写路径**：实现 Session/WriteTx、事务状态和 read-only 证据，再接审计 barrier；提交结果不确定时保持现有 fail-closed/unknown outcome 语义。
6. **驱动供应链**：决定 Oracle 生产驱动；审计 DM 驱动授权与升级来源；加入 Linux/Windows、amd64/arm64、TLS/加密、中文字符集、LOB、时区和故障恢复矩阵。
7. **完整验收**：使用非 SYS 最小权限账号和合成业务表跑“发现 -> 策略 -> 允许/拒绝 -> 脱敏 -> 审计”闭环。SYSDBA/SYSTEM 只用于本批环境调研，不是部署建议。

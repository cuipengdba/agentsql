# DM8 / Oracle dialect 深化边界（v0.5 批十三）

状态日期：2026-10-02。本文只记录已经由代码、单测或指定容器验证的事实；未验证项单独列出。

## 1. 环境证据

- `dm8-v06`：`dm8_single:dm8_20241022_rev244896_x86_rh6_64`，`127.0.0.1:5236`，容器为 Up。
- `oracle-v06`：`container-registry.oracle.com/database/free:latest`，`127.0.0.1:1521`，容器为 healthy。
- Oracle 使用 SYSTEM 登录 `FREEPDB1` 成功；`SESSION_USER` 与 `CURRENT_SCHEMA` 均为 `SYSTEM`，版本为 Oracle AI Database 26ai Free 23.26.3.0.0。
- DM 使用任务书给定的 `SYSDBA/SYSDBA` 登录返回 `-2501 Invalid username or password`。因此本批没有把 DM 真库查询或 EXPLAIN 标记为通过。

## 2. parser 调研与决定

批准 parser 当前只注册 `postgres` 与 `mysql`；规则装配、静态评估和列级授权路径也只接受这两个 dialect。DM/Oracle 不能安全别名到任一现有 dialect：

- 占位符不同：PostgreSQL `$N`、DM 驱动 `?`、Oracle 驱动 `:N`；普通 `Query` 接口又不接收参数，不能改写后猜测绑定。
- 分页存在 `LIMIT/OFFSET`、`FETCH FIRST/OFFSET`、历史 `ROWNUM` 等语义差异，自动拼接会改变排序、子查询或锁语义。
- Oracle 把空字符串视为 `NULL`；字符/数字/日期的隐式比较和 DM 兼容模式也不能沿用 PostgreSQL/MySQL 判断。
- 引号、保留字、别名可见性、大小写折叠、`DUAL` 和伪列均存在差异。

本批不修改主 parser，不把 DM/Oracle 宣称为已接入完整授权 pipeline。dialect 执行层只接受一个极窄的普通 `SELECT`：首词必须是 `SELECT`，禁止注释、分号、绑定标记、括号（函数和子查询）、`INTO`、写/DDL/事务关键字及 `NEXTVAL`。合法但不在此子集内的 SQL 也失败关闭。SQL 原文不做分页或占位符改写。

后续若接入 pipeline，需要独立 DM 与 Oracle parser/语义证明层，并同步提供对应规则集合；不能只在 `parser.NewParser` 增加别名。

## 3. 业务查询

已实现：

- DM 与 Oracle 的池级 `Query` 和物理 session `Query` 共用严格只读门禁。
- 必须传入正的 `rowLimit`；复用 `collectRows` 的列数、单元格、单行、总结果字节上限。
- 读取第 `rowLimit+1` 行时只设置 `Truncated=true`，不把额外行返回；随后关闭结果集。
- 使用 datasource statement timeout；超时、网络和驱动错误返回稳定 `DBError`，不暴露驱动消息。
- `Execute`、`BeginWriteTx` 与所有写事务仍保持 fail-closed。

实测：Oracle 池级查询从 `DUAL CONNECT BY` 产生三行，`rowLimit=2` 返回两行且 `Truncated=true`；物理 session 查询 `SYS.DUAL` 返回 `X`。DM 相同代码路径和单测已完成，但当前实例凭据不一致，真库待实测。

## 4. EXPLAIN 归一化

Oracle 已实现并在 `oracle-v06` 实测：

1. 在同一物理连接与数据库事务内生成不超过 30 字节的安全 `STATEMENT_ID`。
2. 执行 `EXPLAIN PLAN SET STATEMENT_ID='<内部值>' FOR <已验证 SELECT>`。
3. 固定读取 `PLAN_TABLE` 的 `ID,PARENT_ID,OPERATION,OPTIONS,CARDINALITY,COST`，最多 4096 个节点。
4. 要求唯一 ID、完整父节点、根节点 `ID=0` 且为 `SELECT STATEMENT`，根 cardinality/cost 必须为非负有限数。
5. 根 `CARDINALITY` 映射到 `EstScanRows`，根 `COST` 映射到 `EstCost`；`INDEX` 映射 `UsesIndex`，`TABLE ACCESS/FULL` 映射 `SeqScan`。
6. `Raw` 只保存节点编号、操作、选项、cardinality 和 cost，不读取对象名；成功或失败均回滚 PLAN_TABLE 写入。

未知列形态、非法树、非 SELECT 根、溢出、截断或解析失败统一 fail-closed。Oracle 真实计划归一化已通过，手工研究用 `STATEMENT_ID` 回滚后残留计数为 0。

DM EXPLAIN 未实现：当前无法使用给定凭据采集该镜像的真实输出，继续返回 execution/fail-closed；不得依据其它版本文本格式猜测解析。

## 5. 错误分类

分类只读取结构化驱动错误码：DM `dm8.DmError.ErrCode`，Oracle `network.OracleError.ErrCode`。未知驱动码统一 `execution`，不保留原始消息。

| dialect | 驱动码 | 稳定分类 |
| --- | --- | --- |
| DM | `-2501` | authentication |
| DM | `-2007` | syntax |
| DM | `-5501` 到 `-5999` | permission |
| DM | `6001/6060/9007/9008/20001` 或网络错误 | connection |
| Oracle | `1017` | authentication |
| Oracle | `1031` | permission |
| Oracle | 明确列出的常见 `ORA-009xx` 语法码 | syntax |
| Oracle | `904/942/1` | column-not-found / object-not-found / constraint |
| Oracle | 实例/会话终止码、`12100..12699` 或网络错误 | connection |

上下文 deadline/cancel 始终优先归类为 timeout/interrupted。单测覆盖 DM/Oracle 的连接、权限、语法、认证和未知码；Oracle 真库另验证错误口令为 authentication、无效 SELECT 为 syntax。

DM 错误码依据达梦官方《程序员手册》附录的区间说明及官方 FAQ 的 `-2007` 示例；Oracle 码依据 Oracle 官方错误帮助中的 ORA-00900、ORA-01017、ORA-01031 定义。

## 6. 未实现与后续小批

- DM 真库业务 SELECT：代码已实现，当前 `SYSDBA/SYSDBA` 被实例拒绝，待提供实际口令后复跑 `TestDMDiscoveryE2E`。
- DM EXPLAIN：未实现、未实测，保持 fail-closed。
- Oracle/DM INSERT、UPDATE、DELETE、DDL、显式事务：未实现，保持 fail-closed。
- DM/Oracle 的完整 parser、规则矩阵、列级授权/脱敏 pipeline：未实现；本批窄查询能力不能代表这些边界已经开放。
- 带函数、子查询、CTE、注释、绑定变量或括号的 SELECT：即使数据库语法有效也拒绝，留给独立 parser 小批。
- Oracle PLAN_TABLE 不存在或账号无权限时：返回 permission/object/execution 的稳定错误，不尝试创建 PLAN_TABLE。

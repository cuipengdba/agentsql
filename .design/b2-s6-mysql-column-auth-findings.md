# B2 S6：MySQL 列级授权 inspector 与支持边界实测

日期：2026-09-26（Asia/Shanghai）  
代码基线：`feature/v0.4@d5cd895`  
状态：feature-off；未接 MCP/HTTP handler；未激活 flag；未提交 commit。

## 1. 裁决

v0.4 的 MySQL 列级授权为 **tested unsupported**。稳定结果为：

- code：`AUTH_BINDER_MODE_REQUIRED`
- 中文说明：`当前版本 MySQL 列级授权未闭合，建议使用 PostgreSQL 或保持表级策略`

该结果适用于所有 MySQL 列授权请求，包括看似最简单的 schema-qualified 单表
`SELECT`。不得把 inspector 成功、`EXPLAIN FORMAT=JSON` 成功或
`information_schema` 可读解释为列授权证明；不得静默退化成表级允许，也不得以
“仅 base table”名义宣称与 PostgreSQL closed/native binder 同等闭合。

原因不是缺少一条 catalog SQL，而是 MySQL 8.x SQL 接口仍缺少以下授权级原语：

1. typed analyzed tree，以及 clause-site 的 output/reference/write 分类；
2. 每个 view 输出列到 base column 的逐列、逐表达式 lineage；
3. 可序列化且能覆盖完整 relation closure 的 catalog lock/fence；
4. 与 planner 选择无关、可稳定重算的 analyzed/catalog semantic digest；
5. prepared statement 的对象身份失效/重绑定代数。

## 2. inspector 隔离架构

实现位于 `internal/authorizedexecute/internal/businessdb/mysql_column_inspector.go`：

- 使用独立构造器、独立 `*sql.DB`、单连接池；不复用 `MySQLExecutor` 的代理池；
- 只暴露固定 `Probe` 与 `Close`，不实现 `Executor`、`Session` 或内部
  `mysqlRunner`，不提供任意 SQL、事务或 DDL 入口；
- DSN 固定 `multiStatements=false`，协议帧上限沿用 MySQL 连接硬化；
- probe 使用单条物理连接、5 秒上限和固定参数化 catalog SQL；唯一非 catalog
  语句是由已验证 identifier 生成的固定
  `EXPLAIN FORMAT=JSON SELECT 1 FROM schema.table WHERE 1=0`；
- 不启动事务、不执行 `LOCK INSTANCE FOR BACKUP`、不获取客户表 metadata lock、
  不读 binlog、不启动 GTID watcher、不执行 DDL；
- `SHOW GRANTS` 是必检事实。发现 `ALL PRIVILEGES`、`SUPER`、`FILE`、
  `PROCESS`、`RELOAD`、`BACKUP_ADMIN`、`CONNECTION_ADMIN`、复制权限、
  `SYSTEM_*`、DML/EXECUTE/LOCK TABLES、普通 `CREATE/ALTER/DROP/INDEX` 或
  `GRANT OPTION` 时，probe
  以 `AUTH_INSPECTOR_PRIVILEGE_EXCESS` 拒绝。

实测账号按 fixture 对象收窄；只有 `EVENT` 因 MySQL grant 粒度保留 schema 级：

```sql
GRANT SELECT ON agentsql.customer TO 's6_inspector'@'%';
GRANT SELECT ON agentsql.child TO 's6_inspector'@'%';
GRANT SELECT, SHOW VIEW ON agentsql.customer_public TO 's6_inspector'@'%';
GRANT TRIGGER ON agentsql.customer TO 's6_inspector'@'%';
GRANT EVENT ON agentsql.* TO 's6_inspector'@'%';
```

`TRIGGER` 与 `EVENT` 本身具有创建/删除相应对象的 DDL 能力，所以该凭据必须被视为
DDL-capable secret；它只能进入 inspector 私有池，不能进入代理执行、sample、MCP、
HTTP 或通用 query port。账号未授予 `BACKUP_ADMIN`、`CONNECTION_ADMIN`、
`PROCESS`、`REPLICATION CLIENT`、`SUPER`。另建仅有 `SELECT` 的普通代理账号，
测试证明其不能创建 trigger，且与 inspector 凭据分离。

## 3. 实测可得事实与缺失事实

| 类别 | 固定 probe 可得 | 能否作为列授权闭合证明 |
|---|---:|---:|
| server version/UUID、`lower_case_table_names` | 是 | 否，仅环境身份 |
| GTID mode/executed、log_bin、binlog format | 是 | 否，仅当前水位配置事实 |
| `information_schema.tables/columns` | 是 | 否，不能给出 analyzed usage |
| `information_schema.views` + `SHOW CREATE VIEW` | 是 | 否，定义文本不是 typed 逐列 lineage |
| `information_schema.triggers/events` | 有相应 DDL privilege 时是 | 否；任意 body 的执行闭包仍不透明 |
| incoming/outgoing FK 枚举 | 是 | 否，仅支持“存在即拒绝”的发现 |
| `EXPLAIN FORMAT=JSON` | 是 | 否，plan 不是 authority-bearing binder manifest |
| typed analyzed tree | 否 | 必需原语缺失 |
| per-output view lineage | 否 | 必需原语缺失 |
| serializable catalog lock | 否 | 必需原语缺失 |
| stable semantic digest | 否 | 必需原语缺失 |
| prepared invalidation algebra | 否 | 必需原语缺失 |

## 4. 真容器矩阵

测试：`TestMySQLColumnInspectorS6Matrix`。镜像在 2026-09-26 解析为：

| image | exact server | account | GTID | binlog | l_c_t_n | fixture / result |
|---|---|---|---|---|---:|---|
| `mysql:8.0` | 8.0.46 | 非 root inspector | OFF | ON / ROW | 0 | 3 columns、1 view、1 trigger、1 event、1 FK、JSON EXPLAIN 全部可见；unsupported |
| `mysql:8.4` | 8.4.11 | 非 root inspector | OFF | ON / ROW | 0 | 同上；unsupported |

负向矩阵在两个版本均通过：

- schema-qualified base-table `SELECT`：unsupported；
- 隐藏 base table/column 的 view：unsupported，无 view 绕过；
- 分号分隔多语句：unsupported，无首语句截断绕过；
- 带 trigger 隐式写 `audit_sink` 的表：unsupported，无隐式对象绕过。

容器均由各自 testcontainers handle 精确停止并删除，未执行 image/container/volume/
network prune。

## 5. 并发与客户库侵入评估

本实现的 probe 没有锁/快照尝试，因此不会为了检查而长持客户表锁：

- backup lock 可以阻挡部分 DDL，但需要 `BACKUP_ADMIN`，会影响客户 DDL，并且不覆盖
  temporary object；它不能补出 typed tree/view lineage。本实现明确不申请、不尝试；
- metadata lock 若通过 PREPARE/SELECT/事务长期持有，会等待或阻塞 DDL。固定 JSON
  EXPLAIN 仅在 5 秒 probe deadline 内执行，不建立长事务，不能被宣传为 catalog fence；
- GTID/binlog watcher 需要持续连接、复制/日志可见权限、purge/reset/UUID/gap/failover
  状态机与 re-enroll freeze。当前 probe 只读变量，不读日志、不授复制权限；因此
  watcher 字段固定为 inactive，不能把 `log_bin=ON` 当作 watcher proof。

若未来要重新评估 MySQL 支持，backup-lock patch matrix、连续无 gap watcher、restore/
failover/ABA 治理、完整 binder 与所有故障门必须作为联合前置条件，不能各自单独放行。

## 6. 企业版/后续路线（无时间表承诺）

可研究把版本钉死的 MySQL parser/analyzer 服务化，输出 typed analyzed manifest、view
逐列 lineage、对象身份与稳定 digest；服务必须和 catalog snapshot/fence、GTID watcher、
restore/failover freeze、能力 attestation 一起闭合。也可评估受控 server plugin，但必须
先解决升级 ABI、供应链、最小权限和客户库侵入。上述均是研究路线，不代表版本或日期承诺。

## 7. 后续闸门

- S7：inspector/watcher/lock/DDL 竞态与断连故障门；
- S10：运维探测、诊断与凭据轮换工具；
- GA：MySQL 保持 unsupported，直至全部联合前置条件签收；
- release：最终 GA 决策后再做版本 bump，本单不 bump。

## 8. 验证记录

在上述基线与环境上执行：

```text
gofmt -w <S6 Go files>                                      PASS
go build ./...                                               PASS
go vet ./...                                                 PASS
go test -race ./internal/authorizedexecute/internal/businessdb
  -run <S6 unit + MySQL 8.0/8.4 matrix>                      PASS (41.448s)
go test -p 1 ./... -count=1                                 PASS
```

并行的第一次 `go test ./...` 曾因全仓 CPU/IO 争用出现两个非 S6 时序/P99 抖动：
PG18 b5session reaper 的一次 220ms 检查尚未收敛，parser P99 为 MySQL 8.42ms / PG
16.20ms。两个目标单独复跑均通过（parser P99 MySQL 2.05ms / PG 4.20ms；PG14/18
reaper 各阶段约 30–40ms），随后 `-p 1` 的完整全仓复跑全部通过。未修改这些无关测试或
阈值。

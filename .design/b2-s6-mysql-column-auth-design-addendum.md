# B2 design v4 / S6 MySQL addendum

本文件是 [`b2-column-auth-design-v4.md`](b2-column-auth-design-v4.md) 的 S6
落地补充；实测明细见
[`b2-s6-mysql-column-auth-findings.md`](b2-s6-mysql-column-auth-findings.md)。

## 冻结的 v0.4 边界

MySQL B2 column authorization 的 capability 固定为 `unsupported`。任何 SQL shape、
任何 inspector probe 结果、任何表级 allow 都不得改变这一结论。公共裁决字段冻结为：

```text
supported = false
code = AUTH_BINDER_MODE_REQUIRED
message = 当前版本 MySQL 列级授权未闭合，建议使用 PostgreSQL 或保持表级策略
```

裁决对象不包含 executable handle，也不接受或执行 SQL；它只表达 datasource capability。
因此 schema-qualified 单表 SELECT、view、multi-statement 与 trigger/routine 隐式对象不会
产生不同的放行分支。

## inspector capability boundary

inspector 是独立的单连接 metadata pool，credential 与 proxy credential 分开管理。
允许的 privilege 集仅为 `SELECT, SHOW VIEW, TRIGGER, EVENT`；前三者按 enrollment
对象授予，只有 `EVENT` 因 MySQL 粒度为 schema grant。后两项使 secret
具有相应 DDL capability，所以 secret 不能进入通用 executor。代码层面 inspector 不实现
`Executor`/`Session`/`mysqlRunner`，也没有 raw SQL 方法。

固定 probe 可读取 server/GTID/binlog 环境变量、tables/columns/views、trigger/event
definition、FK 与固定 JSON EXPLAIN。它明确不能签发 binder proof，因为 MySQL SQL API
缺少 typed analyzed tree、逐输出 view lineage、可序列化 catalog lock、稳定 semantic
digest 与 prepared invalidation algebra。

## concurrency contract

v0.4 probe 禁止 backup lock、长事务 metadata lock、binlog read/watcher 和 DDL。所有
固定读取共享 5 秒 client deadline。`BACKUP_ADMIN`、`CONNECTION_ADMIN`、复制权限和
普通 CREATE/ALTER/DROP 等被 grant audit 拒绝。未来若引入 watcher 或 lock，必须经过 S7
故障门与独立运维签收；本 addendum 不预授权这些能力。

## future / enterprise research, no schedule

可研究版本钉死的 parser/analyzer service 或受控 server plugin，为每次请求产出 typed
manifest、view lineage、stable object identity/digest，并与 GTID continuity、restore/
failover freeze、catalog fence 组合证明。该路线没有版本或日期承诺，不能据此改变当前
GA unsupported 状态。

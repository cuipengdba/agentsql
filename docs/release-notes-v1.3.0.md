# AgentSQL v1.3.0 发布说明

**语言 / Language:** [简体中文](release-notes-v1.3.0.md) | [English](en/release-notes-v1.3.0.md)

## 范围

v1.3.0 为 MySQL 8.0 与 8.4 增加默认关闭的单表 InnoDB `SELECT` 列级授权子集。只有显式投影和受限的 `WHERE`、限定列名的 `ORDER BY`、有界 `LIMIT` 会进入新路径。JOIN、视图、生成列、函数、子查询等未证明语法在执行前拒绝。既有无列策略的 MySQL 查询仍走原表级路径；PostgreSQL B2 和其他数据库的支持口径不变。

新路径在每次请求中使用全新只读业务连接、单个事务及 MySQL metadata lock，固定查询绑定基础表；独立 PROCESS-only 账号读取 InnoDB `TABLE_ID`，并与业务连接的实例 UUID 核对。AgentSQL 根据真库列序号和类型指纹验证列权限、构造脱敏计划；原 SQL 在持锁事务中执行，Fpost 复核后结束事务，再写持久审计、验证控制面 fence、封存输出。任一环节不成立则不交付数据。

## 策略登记与部署

部署者需显式设置 `column_authorization.enabled: true`、`mysql_single_table_enabled: true`，并为每个 MySQL 数据源配置 `mysql_metadata_accounts`。元数据账号仅应有实例级 PROCESS；业务账号仅应有 SELECT。元数据密码通过环境变量读取，不放入 YAML。管理员用策略管理权限创建同一对象的 table allow 与 column allow 策略，再对列策略调用 `POST /api/v1/policies/{id}/bind-mysql`，携带 `If-Match` 和列的 output/reference 用途。AgentSQL 在真库两次发现、第二次持锁复核后提交对象 ID、目录指纹和列权限。策略修订或表重建后须重新登记。

完整配置和调用示例见 [部署文档](DEPLOY.md)。本版不把 MySQL 所有 SELECT、所有版本或列策略写入称为完整生产支持。

## 验收和发布节奏

MySQL 8.0.46/8.4.11 隔离真库的请求、脱敏、拒绝、审计、DDL 竞争和故障关闭已通过；Go/Web/govulncheck 同提交 CI、v1.2.0 正式包至本版的 SQLite 与 PostgreSQL 分库升级/备份恢复、15 项 Ed25519 签名资产及双架构 OCI 归档均已通过。正式 [GitHub Release](https://github.com/cuipengdba/agentsql/releases/tag/v1.3.0) 已发布；GHCR 三个精确版本标签的双架构 manifest 与六种匿名拉取均通过，详见[验收记录](v1.3.0-acceptance.md)。官网、公网 Demo 与 GHCR `latest` 按约定保持 v1.0.0，待 v1.5.0 统一更新。

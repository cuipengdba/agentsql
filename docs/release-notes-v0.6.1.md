# AgentSQL v0.6.1 发布说明

**语言 / Language:** [简体中文](release-notes-v0.6.1.md) | [English](en/release-notes-v0.6.1.md)

v0.6.1 修复 v0.6.0 在 PostgreSQL metadata/audit 分库时的管理审计路由：管理操作与流量操作均写入 audit 库的 `traffic` 链，metadata 库只保存 `management_audit_outbox` 待投递记录。管理 API 和 `agentsqlctl chain --domain management` 在分库配置下查询该 `traffic` 链；合并库仍使用 metadata 库的 `management` 链。

本次没有新增 migration，也不改变控制面 schema。v0.6.1 的 PostgreSQL 分库版本仍为 metadata `16`、audit `6`。除分库审计路由和版本标识外，不扩大 v0.6.0 的数据库支持或功能承诺。已在隔离的 PostgreSQL 分库环境验证服务启动、管理审计持久化、两库备份恢复与回退。

## 从 v0.6.0 更新到 v0.6.1

1. 停止全部 AgentSQL 实例与控制面写入，分别备份实际使用的 metadata 和 audit 数据库，并保留与备份配对的 `AGENTSQL_SECRET`、配置与旧程序。分库的两个备份应有协调恢复点。
2. 使用只读 SQL 分别确认两个 `schema_migrations` 的最大版本为 metadata `16`、audit `6`。如果版本不符，先核对部署历史；不要仅为了通过检查执行 DDL。
3. 替换为成套的 v0.6.1 CLI 与服务程序，保持原 DSN、SECRET 和 `store.auto_migrate: false`。启动后要求 `/readyz` 就绪，并实际触发一条管理审计，在 audit 库的 `audit_logs` 与 `traffic` 链中核对；metadata 库不应承载 `audit_logs`。涉及异步投递的操作还应核对 metadata 库的持久 outbox，不能把投递失败静默视为审计完成。

## 作为 v0.7 分库回退基线

从 v0.7 回退时，先停止 v0.7 服务，**同时恢复升级前的 metadata 16/audit 6 数据库备份及配对 SECRET**，再使用原分库配置启动 v0.6.1。只回退二进制不可用：v0.6.1 在 `auto_migrate: false` 下会拒绝 v0.7 的 metadata 17 schema，v0.7 写入的数据也不会自动反向迁移。恢复后再次核对两个版本表、`/readyz`、管理审计落库与审计链。回退会丢失备份时间点之后的新写入，需在维护窗口中按业务恢复计划处理。

原版 v0.6.0 的分库服务在启动 B2 管理审计时会写错 metadata 库并因缺少 `audit_logs` 退出，不能作为 PostgreSQL 分库场景下已验证的服务回退目标。不得把旧库恢复成功或旧 CLI 可读取 schema 当作旧服务回退成功。

# AgentSQL v0.8.0 发布说明

**语言 / Language:** [简体中文](release-notes-v0.8.0.md) | [English](en/release-notes-v0.8.0.md)

v0.8.0 将任务级临时只读 Key 改为“申请 → 异人核对精确 SQL 并审批 → 原申请人一次性领取”。现有 v0.7.0 Key 仍按到期和撤销规则生效；旧直接签发 API 返回 HTTP 410。新版同时改善 MCP 工具 schema 的客户端可移植性、控制台审计关联和接入故障排查。

## 功能与安全边界

| 能力 | 本版行为 |
| --- | --- |
| 任务授权申请 | 具备 `query.execute` 的同租户用户提交一个 Agent、一个数据源、一条精确的简单 `SELECT`、最长 24 小时有效期及幂等键。SQL 以配对密钥加密存放；列表和管理审计只展示 SHA-256。 |
| 审批与领取 | 另一位同时具备 `strategy.manage`、`query.execute` 的同租户用户在详情中查看 SQL 原文并批准或拒绝。批准后只有原申请人可一次性领取 Key。无权或已撤销 Key 拒绝；领取不扩张 Agent、对象、规则或数据库账号权限。 |
| 审计关联 | 授权 ID 关联申请、审批、签发与进入 SQL 流水线后的执行或拒绝。metadata/audit 分库时管理动作与 outbox 在 metadata 事务内提交，relay 随后投递到独立 audit 库；故障期间有待投递窗口，应监控积压并成套备份。 |
| MCP 接入 | `/mcp` 与 `/mcp/2026-07-28` 的工具 schema 已用 MCP Inspector `tools/list --strict` 验证。两个入口继续遵守各自既有协议边界。 |
| 控制台 | 任务授权的双人流程、一次性 Key 展示、授权 ID 审计筛选和认证、权限、协议、数据库、审计故障定位。 |

## 升级与回退

从 v0.7.0 升级前，停止写入并成套备份 metadata、audit（若分库）与配对的 `AGENTSQL_SECRET`。SQLite 使用一致性备份，不能只复制尚未合并 WAL 的主数据库文件。先运行新版 `agentsqlctl migrate --check`，由迁移负责人显式执行 `migrate`，再启动网关并检查 `/healthz`、`/readyz`、MCP allow/deny、脱敏与审计。生产配置保持 `auto_migrate: false`。若回退，先停止新版，恢复升级前的数据库与配对密钥，再运行 v0.7.0；不能让旧版直接读取新版 schema。

隔离演练覆盖 SQLite metadata schema 18 → 19 → 恢复 18，以及 PostgreSQL metadata/audit 分库 17/6 → 18/6 → 恢复 17/6。目标生产环境仍须独立演练备份、恢复和权限配置。既有任务 Key 的读操作在升级及恢复后保持可用。

## 验证范围

- Go 全仓 short、`go vet`、前端 54 项 Vitest、类型检查和生产构建通过；封板提交及同提交 CI 见 GitHub Release 正文。
- 隔离 Dify 1.17.1 工作流通过兼容 `/mcp` 的允许、R010 拒绝、脱敏和持久审计；MCP Inspector 2.11.0 对旧、新两个入口完成真实工具发现、允许、拒绝与 `tools/list --strict`。
- DM8 Pack3、KingbaseES V9R1C10 与 Vastbase G100 Build 9 在指定实例、协议模式、受限账号和合成数据内完成有界复验。结果不外推到其他版本、生产负载、完整方言或厂商认证。数据库支持数量与生产支持等级不变。
- 双账号控制台走查和任务 Key 真链路已完成；独立 audit 库停机与恢复的 outbox 投递结果见 [验收记录](v0.8-acceptance.md)。Claude Desktop 未做登录后的 GUI 实测。

正式资产、三份签名、GHCR 双架构与官网烟测，以 [v0.8.0 GitHub Release](https://github.com/cuipengdba/agentsql/releases/tag/v0.8.0) 的实际发布证据为准。详细操作见 [任务授权说明](task-read-grants.md)、[MCP 接入指南](INTEGRATIONS.md)、[部署指南](DEPLOY.md)。

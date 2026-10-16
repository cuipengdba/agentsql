# AgentSQL v0.7.0 发布说明

> 本文件描述版本功能与隔离验收范围。封板提交、对应 CI、正式发布日期、GitHub Release 资产验签、GHCR 匿名双架构验收及官网外部烟测属于 tag 后的发行证据，以 [GitHub Release](https://github.com/cuipengdba/agentsql/releases/tag/v0.7.0) 正文记录的实际结果为准；本文件不预记其通过结论，也不代表生产环境验收。执行顺序见[发布流程](RELEASE_PROCESS.md)。

## 本版范围

| 能力 | 边界 |
| --- | --- |
| MCP `2026-07-28` | 新增独立的 `/mcp/2026-07-28` 无状态 HTTP 入口。每个请求独立验证 Agent Key、租户和限流；旧 `/mcp`、stdio 及其已发布协议继续可用。新入口暂不开放 B5 跨请求逻辑会话与计划事务。 |
| 任务级临时只读授权 | 管理员同时具备策略管理和查询执行权限时，可为一个 Agent、一个数据源及一条完全匹配的简单 `SELECT` 签发最长 24 小时的临时 Key，并随时撤销。原 Agent 的状态、租户、对象授权、规则、脱敏、审计和数据库只读账号约束继续逐次生效。Key 仅在创建响应显示一次。 |
| 升级前检查 | `agentsqlctl migrate --check` 只读报告当前与目标 schema 版本；当前为 0、待迁移为 2、错误或不兼容为 1。生产部署仍须先备份数据库与配对密钥，再由 migration owner 显式迁移。 |
| 发布构建 | 修复隔离 `DOCKER_CONFIG` 时 Buildx 插件不可见的问题，增加发布前预检。 |
| 接入与运维 | 控制台接入向导检查 Agent、数据源和策略准备情况；增加迁移、审计故障与 MCP 协议拒绝排查手册。 |

任务授权的 SQL 范围、接口和审计字段见 [任务级临时只读授权](task-read-grants.md)。新旧 MCP 入口与客户端配置见 [MCP 接入指南](INTEGRATIONS.md)。升级步骤见 [部署文档](DEPLOY.md) 和 [v0.7 运维手册](v0.7-operations-runbook.md)。

流量审计覆盖进入 SQL 流水线的允许、拒绝和故障决策。MCP 工具层对缺失参数、无权数据源或错误工具用法的前置拒绝可能在进入流水线前返回，未必产生持久流量审计；此边界同样适用于新旧入口，详见接入指南。
任务密钥通过 MCP 协议、认证和工具参数校验并进入 `query` 处理器后，会送入 SQL 流水线；错误 SQL、无权或不存在的数据源会按授权身份留痕。协议或认证阶段的拒绝（例如错误版本头、畸形请求、过大请求体或已撤销密钥）发生在流水线前，不属于这项持久流量审计保证。
管理端创建和撤销临时授权与管理审计目前不能跨库存储原子提交；创建审计失败时不会返回密钥并会补偿撤销，撤销审计失败可能返回 500，但撤销本身已经生效。处理方式见任务授权文档。

## 升级注意事项

1. 停止旧服务，备份控制库及配对的 `AGENTSQL_SECRET`；PostgreSQL 分库部署同时备份 metadata 和 audit 库，并记录一致性恢复点。
2. 使用待部署的 v0.7.0 `agentsqlctl migrate --check` 检查待迁移状态，核实目标版本和数据库连接。`--check` 不执行 DDL。
3. 在升级窗口中使用 migration owner 执行 `agentsqlctl migrate`。保持 `store.auto_migrate: false` 的生产部署必须先完成迁移，再启动新版网关。
4. 检查 `/healthz`、`/readyz`、旧 MCP 入口和新入口；用真实测试 Agent 验证允许查询、越权拒绝、脱敏、审计关联与临时授权撤销。
5. 若迁移失败或需要回退，先停用新版，再恢复升级前成套数据库备份与配对密钥。SQLite 合并库可回到已验收的 v0.6.0；PostgreSQL metadata/audit 分库已在隔离环境用 v0.6.1 维护构建完成双库回退演练，其 [正式签名资产](https://github.com/cuipengdba/agentsql/releases/tag/v0.6.1) 已发布。v0.7.0 正式包的隔离升级回退复测须在发行前完成，结果以 GitHub Release 正文为准。具体生产部署须在目标环境单独验证备份与恢复，不能把隔离演练当作客户环境结果。原 v0.6.0 分库服务有启动缺陷，不能作为该拓扑的可用回退目标；不要让旧程序读取已迁移的新 schema。

## 验收状态

| 检查项 | 当前证据与状态 |
| --- | --- |
| 前端类型检查、测试与生产构建 | 本地类型检查、Vite 构建和 54 项 Vitest 测试通过；封板提交的 CI 结果以 GitHub Release 正文为准。 |
| MCP 新协议与旧入口回归 | 新入口 `/mcp/2026-07-28` 的 Go SDK 协议测试通过。MCP Inspector 0.16.8 GUI 与 Dify 1.17.1 GUI 均在兼容入口 `/mcp` 完成允许查询、手机号脱敏、R010 越权拒绝；四次请求分别关联持久流量审计 ID `1`、`2`、`3`、`4` 和各自 Agent 身份。新入口没有在这两个 GUI 中实测，不能把兼容入口结果外推给新入口。 |
| SQLite v0.6.0 → v0.7.0 → 备份恢复 | 隔离数据验证通过：待迁移退出 2、旧 schema 启动关闭、迁移后就绪、恢复后旧版就绪。正式包复测结果以 GitHub Release 正文为准。 |
| PostgreSQL 分库升级与恢复 | 隔离双库的旧 schema 拒绝启动、显式迁移、新版就绪、管理审计落入 audit 库已通过；另以 v0.6.1 维护构建实测双库备份、恢复配对密钥、恢复旧 schema 后旧服务就绪，其[正式签名资产](https://github.com/cuipengdba/agentsql/releases/tag/v0.6.1)已发布。范围见[验收记录](v0.7-acceptance.md)；最终 v0.7 正式包的回退复测结果以其 GitHub Release 正文为准。目标环境回退属于具体生产部署的单独验收。 |
| DM8、KingbaseES V9、Vastbase G100 真库 | 三者的指定版本、模式、只读账号与合成数据链路均已完成有界复验；不外推到其他版本或生产环境。 |
| 源码与 CI | 候选提交 `9429b25f800de1333d63feab72cc550fdb23f54f` 的 [CI](https://github.com/cuipengdba/agentsql/actions/runs/38815198187) 成功；正式封板提交及其 CI 以 v0.7.0 GitHub Release 正文和 tag 指向为准。 |
| 双架构安装包与 SBOM | 首轮本地干跑形成 15 项资产及 amd64/arm64 包和 SPDX SBOM；干跑签名是占位文件，不可发布。正式签名、回下载验签、GHCR 和官网的实际结果以 GitHub Release 正文及发布证据为准。 |

本版不增加数据库支持数量，也不把某个数据库升级为“完整生产支持”。没有真实客户环境、正式生产许可证或厂商认证的结论，不用容器内合成数据验收替代。Claude Desktop 因没有可用账号，仍未完成 UI 实测；客户端验收目标采用 Dify 与 MCP Inspector，并分别记录实际版本。

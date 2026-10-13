# AgentSQL v0.5.1 发布说明

## 本次更新

- MCP stdio 默认采用标准逐行 JSON 消息，供标准 MCP 客户端连接；已有自定义长度前缀客户端可显式使用 `agentsql mcp --sealed-stdio`。HTTP MCP 允许 `2025-06-18` 与 `2025-11-25` 两个已验证的有状态握手版本，并拒绝未知版本及相互冲突的版本声明。
- HTTP 和 stdio 的 Agent 身份均绑定到其持久化租户。请求被取消后，权威审计继续保留该租户范围。
- 更新 Claude Desktop 和 Dify 自托管接入步骤。Windows Claude Desktop 使用 `mcp-remote` 将 stdio 转发到 AgentSQL 的 Streamable HTTP 端点；Dify 1.17.1 需在 MCP 服务配置中显式加入 `MCP-Protocol-Version: 2025-06-18` 请求头。
- CI 修复离线编译测试所需模块的预取，将 Go 构建及漏洞扫描工具链对齐到 Go 1.26.9。
- 达梦和金仓真库验证脚本修复 MCP 会话头处理，并补充失败时的合成对象清理。
- 修复 PostgreSQL 控制面快照读取脱敏规则时的字段数量错误，并将规则查询限定在当前租户；快速启动镜像引用同步更新为 v0.5.1。
- Linux 双架构发行包与 GHCR 镜像构建纳入独立 YashanDB C 客户端 23.4.7.100 的 SHA-256 和架构检查；崖山数据库的 HTTP/MCP 与持久化审计组合仍未完成真库验证。

## 验证范围

- DM8 Pack3、`COMPATIBLE_MODE=0`：参数绑定、只读账号拒写、受控 HTTP MCP SELECT、列血缘、R010 表权限拒绝、手机号脱敏、解析失败关闭与持久化审计通过。
- KingbaseES V9R1C10、PG 模式：真实许可证启动、CRUD 和分页、标识符、pgx 参数绑定与 SCRAM、只读账号拒写、AgentSQL 受控查询、列血缘、脱敏、R006 拒绝和持久化审计通过。
- 使用 MCP 官方 Go 客户端通过真实管道验证标准 stdio；使用 `mcp-remote@0.14.3` 验证 Windows 风格桥接；使用 Dify 1.17.1 的 `MCPClient` 代码验证初始化、工具发现和带 Agent Key 的工具调用。
- 以 v0.5.0 容器创建 Agent 和 MCP Key，停止服务后复制 SQLite 数据卷并比对 SHA-256；v0.5.1 使用原卷成功读取原有身份和 Key，备份卷恢复后 v0.5.0 也成功启动并验证同一身份和 Key。

上述数据库验证只覆盖指定版本、模式和合成场景，不代表达梦或金仓全部方言、部署形态或完整生产支持。Claude Desktop 与 Dify 的客户端协议路径已验证；桌面 UI 与 Dify 全量自托管 UI 的人工操作不在本次自动化证据内。各数据库支持范围以 [README 矩阵](../README.md) 为准。

## 升级与接入

升级前停止服务并备份控制面数据库和配置，保留 Agent Key 与 `AGENTSQL_SECRET`。升级后检查 `/healthz`、管理端登录及 MCP 工具发现。新部署的标准 stdio 客户端使用默认 `agentsql mcp`，已有自定义帧协议客户端升级时加 `--sealed-stdio`。详细配置见 [MCP 接入指南](INTEGRATIONS.md)。

发行包、签名及校验方式以 [GitHub Releases](https://github.com/cuipengdba/agentsql/releases/tag/v0.5.1) 的正式发布资产为准。

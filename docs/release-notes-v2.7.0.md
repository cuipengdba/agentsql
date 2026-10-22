# AgentSQL v2.7.0 发布说明

**语言 / Language:** [简体中文](release-notes-v2.7.0.md) | [English](en/release-notes-v2.7.0.md)

状态：正式发布。封版 CI、升级恢复、签名资产、GHCR、官网和公网 Demo 的实际结果见[发布证据](v2.7.0-release-evidence.md)。

## 本版范围

- 为 ClickHouse 26.8.31.2 指定 TLS 单节点增加 Agent MCP `query_analytics`。调用方提交数据源 ID、当前数据库的 MergeTree 表名、显式列、String 列等值过滤和 1–100 行限制；不提交 SQL。服务端构造参数化查询，限制执行时间、读行/字节、内存、输出行和线程，并额外限制结果体积。
- 每次调用重新核验 Agent、租户、数据源、精确表授权和显式列白名单；过滤列也必须授权。`readonly=1` 的数据库最小权限账号只能 SELECT 授权表列。视图、Distributed、表函数、JOIN、聚合、写入和跨库查询均不属于这条受控路径。
- 严格 TLS 与指定 CA、来源列脱敏、持久预检和终态审计构成完整请求链。预检审计失败时不访问业务库；终态审计失败时不交付结果。
- 旧 ClickHouse 管理预览仍保留 9000 注册默认端口；控制台新建受控查询配置预填原生 TLS 9440。原有明文配置可继续管理预览，但无法调用 `query_analytics`。

## 验证边界

真实 HTTP MCP、MCP Inspector 0.16.8、Dify MCPClient 1.17.1、最小权限、参数绑定、错误 CA、权限撤销、独立审计库及业务库停机恢复已在单节点合成环境通过；详见[候选验收](v2.7.0-acceptance.md)。正式包升级与公开发布链的结果见[发布证据](v2.7.0-release-evidence.md)。

其他版本、集群/复制、生产证书轮换、长期负载、生产数据及完整生产支持未验证。公网 Demo 继续使用合成 PostgreSQL 数据。

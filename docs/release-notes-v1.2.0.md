# AgentSQL v1.2.0 发布说明（候选）

## 范围

v1.2.0 增加可选的 MCP OAuth 受保护资源入口 `/mcp/oauth`，面向 MCP 2026-07-28 的无状态 HTTP 调用。原 `/mcp` 和 `/mcp/2026-07-28` 入口保留。新入口要求外部授权服务器签发面向精确资源 URI 的 RS256 JWT Access Token，同时要求独立的 Agent Key 请求头。用户映射、租户和 `query.execute` 权限每次请求重新核验，Agent 权限不因 OAuth 增加。

本版限定同一个已启用的控制台 OIDC issuer 和租户，不声称支持任意授权服务器、Opaque Token、所有签名算法或动态客户端注册。客户端负责 PKCE、`resource` 参数和 IdP 客户端注册。具体实测组合与拒绝矩阵以[验收记录](v1.2.0-acceptance.md)为准。

## 配置与兼容

`mcp.oauth.enabled` 默认为 `false`。启用时配置 `issuer_url`、`resource_url`、`tenant_id` 和单个 `required_scope`；issuer 与 tenant 必须匹配 `auth.oidc`。生产 URI 使用 HTTPS，本地回环测试可用 HTTP。OAuth Access Token 放在标准 `Authorization: Bearer`，Agent Key 放在 `X-AgentSQL-Agent-Key`。控制台会话令牌和旧人员委托令牌不可替代 OAuth Access Token。

本版不预计修改数据库 schema。仍需按正式包完成 SQLite 与 PostgreSQL 分库升级、恢复和回退验证。数据库支持级别不因本版改变。

前端构建所用的间接依赖 `source-map-js` 更新至 1.2.2；本版锁文件经 `npm audit` 检查为 0 项漏洞。

## 发布节奏

本版独立 tag、正式 GitHub Release 与精确版本镜像。按既定计划，官网、公网 Demo 与 GHCR `latest` 到 v1.5.0 完成后再同步；本版部署须使用 v1.2.0 的精确标签和验签资产。未完成的验收不写作通过。

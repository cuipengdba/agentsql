# AgentSQL v2.6.0 发布说明

**语言 / Language:** [简体中文](release-notes-v2.6.0.md) | [English](en/release-notes-v2.6.0.md)

状态：候选版；正式发布资产、GitHub Release、GHCR、官网和公网 Demo 须在封版 CI 与升级恢复验收后更新。

## 本版范围

- 为 Redis 8.2.9 与 Valkey 8.1.10 指定 TLS 单实例增加 Agent MCP `read_key`。请求只接受数据源 ID 和精确键名，服务器固定执行 `TYPE` 与最多 64 KiB 的 `GETRANGE`。仅交付 UTF-8 字符串；超限、非字符串、通配符、原生命令及跨库请求拒绝。
- 新增独立 `key` 策略类型：`dbN:exact-key` 或有界前缀 `dbN:prefix*`。每次请求重验当前 Agent、租户、数据源和策略；拒绝优先。管理 API 只允许在 Redis/Valkey 数据源登记键策略，控制台提供相应录入提示。
- 对按键值来源配置的规则执行脱敏，预检与终态都写持久审计。审计对象使用按部署密钥和数据源隔离的 HMAC-SHA256 标识，不记录原始键名或值；审计或脱敏不可用时不交付数据。

## 验证边界

隔离环境使用 Redis 8.2.9 与 Valkey 8.1.10 的 Linux x86_64 Docker 镜像、测试 CA、TLS、最小 ACL 和合成数据。两款产品的真实 HTTP MCP 允许与拒绝、脱敏、错误 CA、策略撤销，以及独立审计库和业务库停机、恢复后再次脱敏读取已通过。MCP Inspector 0.16.8 与 Dify MCPClient 1.17.1 分别对两款数据库完成真实允许和拒绝调用；正式包升级与公开发布链的最终结果见[发布证据](v2.6.0-release-evidence.md)，测试细节见[候选验收](v2.6.0-acceptance.md)。

其他版本、Sentinel、Cluster、生产证书轮换、持久化策略、长期负载、二进制值、列表/哈希/集合/有序集合、写入及完整生产支持未验证。公网 Demo 继续使用合成 PostgreSQL 数据，不提供公网 Redis/Valkey。

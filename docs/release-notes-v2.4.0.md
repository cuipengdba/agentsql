# AgentSQL v2.4.0 发布说明

v2.4.0 为 openGauss 社区版 7.0.0 LTS 指定构建增加独立的 `opengauss` 数据源类型和受控只读 MCP 路径。适配器要求 `verify-full` TLS、精确服务端构建身份和只读会话；只接受明确 schema、`FROM ONLY` 普通物理单表、直接列投影、非恒真 WHERE、整数 LIMIT 1–10000 的 SELECT。无法证明来源、对象类型或语义的请求执行前拒绝。

在官方 openEuler 24.03 x86_64 归档加载的 7.0.0 LTS 单实例及合成数据中，专用只读账号完成真实 MCP 允许、R010 表拒绝、手机号脱敏、审批单次消费、持久审计和策略撤销。MCP Inspector 0.16.8 与 Dify MCPClient 1.17.1 分别完成允许及拒绝调用。错误 CA、错误证书主机名和明文连接失败；数据库 SELECT 撤权、业务库及独立审计库断线期间不交付原始结果。openGauss 合成数据另经 `gs_dump` 与 `gs_restore` 完成恢复验证。逐项环境与结果见[候选验收](v2.4.0-acceptance.md)。

## 安全与兼容边界

- 本版只覆盖 `openGauss 7.0.0 build 2397a86f` 指定社区版单实例；Docker Hub 的 7.0.0-RC3 仅曾用于预检。GaussDB 商业版、其他 openGauss 版本、主备、生产证书轮换、长期负载和客户部署均未验证。
- `opengauss` 不继承 PostgreSQL 的 B2 列级绑定和 B5 会话事务。JOIN、函数、聚合、视图、外部表、系统目录、写入及任意 SQL 均不开放。授权角色需要数据库自身最小权限；网关允许不替代数据库 GRANT。
- 本版不增加 AgentSQL metadata/audit schema。升级仍应对 SQLite 或 PostgreSQL metadata/audit 分库做成套备份、只读检查和隔离恢复演练。固定双网关部署仍需维护窗口，不能混跑新旧镜像。
- 这是一条指定环境的受控只读证据，不声明厂商认证或完整生产支持。公网 Live Demo 保持合成 PostgreSQL 数据，不连接 openGauss 测试库。

正式发行 CI、签名双架构资产、GHCR、官网和公网 Live Demo 的发布后结果记录在[正式发布证据](v2.4.0-release-evidence.md)。

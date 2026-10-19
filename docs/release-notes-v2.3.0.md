# AgentSQL v2.3.0 发布说明

v2.3.0 深化 SQL Server 2025 Developer 的受控只读能力。已有 TDS 连接、元数据和窄 SELECT 执行器，本版用指定 Linux x86_64 真库把严格证书校验、最小权限账号、Agent 表授权、执行计划、脱敏和持久审计接成一条可复核的 HTTP MCP 链路。无权表的静态 R010 在网关终止，避免最小权限账号对无权表执行 `SHOWPLAN` 时先产生数据库权限错误。

候选环境使用官方 SQL Server 2025 Developer 镜像、服务日志 build `17.00.5015`、本地测试 CA 签署的服务端证书和随机合成数据。读账号只获目标表 `SELECT` 与所在数据库 `SHOWPLAN`，SQL Server 服务端拒绝写入。真实 HTTP MCP 的允许查询返回遮蔽手机号；无权表 R010、SELECT/SHOWPLAN 撤权、独立审计库停机、SQL Server 停机均不交付原始结果。MCP Inspector 0.16.8 与 Dify MCPClient 1.17.1 分别完成允许及拒绝调用；错误 CA 和不匹配的证书主机名被拒绝。具体证据和失败后重跑过程见[候选验收](v2.3.0-acceptance.md)。

## 安全与兼容边界

- 只开放 parser、对象授权、计划与 executor 都确认的窄 SELECT；写入、批处理、存储过程、动态来源、复杂 T-SQL、系统目录和未确认结构保持拒绝。SQL Server 的 `SHOWPLAN` 是数据库级权限，本版对无权表 R010 先静态终止，不用 SA 管理账号替代运行账号。
- B2 列级授权与 B5 会话不扩展到 SQL Server。本版未改变 metadata/audit schema；从 v2.2.0 升级仍须先对 SQLite 或 PostgreSQL metadata/audit 分库做成套备份、只读检查和隔离恢复演练。固定双网关 Kubernetes 部署仍需维护窗口，不能混跑新旧镜像。
- 单个 Developer 容器的合成测试不等于其他 SQL Server 版本、Windows、Always On、生产证书生命周期、长期负载或客户生产部署认证；不声明完整生产支持或厂商认证。官网 Live Demo 继续使用固定合成 PostgreSQL 数据，不展示 SQL Server 真库实例。

正式发行 CI、安装包、签名、GHCR、官网和公网 Live Demo 的发布后结果见[正式发布证据](v2.3.0-release-evidence.md)。

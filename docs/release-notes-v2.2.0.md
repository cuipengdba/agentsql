# AgentSQL v2.2.0 发布说明

**语言 / Language:** [简体中文](release-notes-v2.2.0.md) | [English](en/release-notes-v2.2.0.md)

v2.2.0 将已有 Oracle 原生连接、受限 SELECT 解析器和执行器接入 AgentSQL 的完整只读 MCP 安全链路。此前 Oracle 虽可做底层连接、发现和少量查询，但没有通过网关方言、规则、授权、脱敏和持久审计的同链路证明；本版只按实际验证的 Oracle AI Database 26ai Free 指定实例声明能力。

本版候选实例为 `FREEPDB1`、服务端 `23.26.3.0.0`、`go-ora/v2`、x86_64 Docker 单实例。随机合成表和临时账号只获 `CREATE SESSION` 与目标表 `SELECT`。真实 HTTP MCP 允许查询返回遮蔽手机号；无权表 R010 拒绝、写入阻断、数据库 SELECT 撤销、Agent 策略撤销、独立审计库停机和 Oracle 断线均按不交付原始结果验证。MCP Inspector 和 Dify 也各自完成允许和拒绝调用。详细结果见[候选验收](v2.2.0-acceptance.md)。

## 安全与兼容边界

- Oracle 的 parser 与 executor 继续只接受已证明的窄 SELECT；写入、事务、函数、复杂 JOIN、子查询、CTE、系统目录和未确认结构保持拒绝。B2 列级授权与 B5 会话不扩展到 Oracle。
- 最小权限运行账号未具备经过验证的 `PLAN_TABLE` 权限，因此此 Oracle 网关路径不预执行 `EXPLAIN PLAN`，也不把管理账号的计划结果外推到它。R004/R005 计划估算不适用；执行行数上限仍有效。
- 本版没有 metadata/audit schema 变更。从 v2.1.0 升级仍须先对 SQLite 或 PostgreSQL metadata/audit 分库做成套备份、只读预检和隔离恢复演练。双网关 Kubernetes 仍需维护窗口，不能混跑新旧镜像。
- Oracle Free 指定容器的合成测试不等于 Oracle 19c、商业 23ai/26ai、RAC、Data Guard、TCPS、持续负载或客户生产部署认证；本版不声称完整生产支持或厂商认证。官网 Live Demo 继续使用固定合成 PostgreSQL 数据，不把它展示为 Oracle 真库实例。

正式发行 CI、安装包、签名、GHCR、官网和公网 Live Demo 的发布后结果见[正式发布证据](v2.2.0-release-evidence.md)。

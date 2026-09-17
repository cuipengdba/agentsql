# Changelog

本项目的重要变化记录于此，格式参考 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)。

## [v0.2.0] - 2026-09-17

> 发布准备稿：日期为发布准备日；若 GA 当日延后，以正式 tag 日期为准同步本行与 Release 标题。

### Added

- 控制台总览新增经 Bearer 鉴权的 SSE 实时事件流（`GET /api/v1/stream`），并在连接中断时自动降级为既有定时刷新路径；选型 SSE 而非 WebSocket，单向推送即可满足大屏需求。
- 元数据与审计控制面新增 PostgreSQL 支持（兼容 15+，开发、Compose 与 CI 基准为 PostgreSQL 18），支持 metadata 与 audit 分别放入独立数据库，审计库保留不可变审计 ID 与 `approvals.audit_id` 的应用级关系。
- 新增 SQLite 到 PostgreSQL 的数据迁移、校验与序列对齐命令 `agentsqlctl migrate-sqlite-to-postgres`。
- 新增可自托管的开源 Live Demo 套件：一条命令拉起仅含合成数据的 PostgreSQL/MySQL 演示库与网关，端口仅绑回环、数据每日重置、禁止接入真实数据。
- 演示台在原静态评估之外新增“真实试运行（Live）”模式与仅在 demo 模式注册的 `POST /api/v1/playground/run`，复用生产八段安检管线，内置 6 个真实受控剧本：正常放行、无 WHERE 写拦截、phone/email 脱敏、大结果扫描告警、越权表拒绝、审计/大屏回看。

### Changed

- 生产 PostgreSQL 部署采用一次性 migration 账号、最小权限运行账号与 `store.auto_migrate:false`。
- 独立审计库布局保留不可变审计 ID，以及 `approvals.audit_id` 与审计记录的应用级关系。

### Security

- Live 真实通道的演示 Agent Key 仅由服务端持有，不下发浏览器；请求体只接受白名单字段，夹带身份/方言等未知字段一律拒绝。
- Live 模式下非 SELECT 语句在触达数据库前由结构性硬屏障拦截（DEMO_NON_SELECT/DEMO_PARSE/DEMO_SCOPE），不进入目录、不可被规则配置覆盖；演示数据源走白名单，demo 限流固定 QPS=2，响应为不含密钥与 DSN 的白名单 DTO。

### Fixed

- 修复 PostgreSQL `numeric/decimal`（pgx v5 `pgtype.Numeric`）在结果表中被渲染为结构体文本（如 `{2274 -2 false finite true}`）的问题，改为基于 `big.Int` 与指数的定点十进制字符串（如 `22.74`），保留尾随零且不经过 float64；同时让 interval、jsonb、uuid、数组、枚举、bytea 等结构化值以人类可读形式返回。MySQL decimal 路径行为不变。

### Compatibility

- 被防护业务库仍为 MySQL 8 与 PostgreSQL 14、15、16、17、18；本版新增的 PostgreSQL 18 支持针对控制面（元数据/审计）库。
- PostgreSQL 控制面兼容 15+，基准与 CI 使用 PostgreSQL 18。
- SQLite 仍是默认零配置控制面，现有 SQLite 起手路径保持不变。

## [v0.1.0] - 2026-09-16

### Added

- 提供 MCP stdio 与 Streamable HTTP 两种承载，以及数据源发现、Schema 查看、Explain、查询、受控写入、申请审批和查询审批结果 7 个工具。
- 提供 PostgreSQL/MySQL SQL 解析、安全规则、Agent 能力档位、对象/列授权、限流、Explain 风险评估、受控执行与同步审计链路。
- 提供手机号/邮箱基础打码、人工审批闭环、Prometheus 指标、健康/就绪探针和内嵌 Web 管理控制台。
- 提供 SQLite 元数据/审计存储、Docker Compose、systemd 示例与运维 CLI。

### Security

- 启动时强制要求恰好 32 字节的 `AGENTSQL_SECRET`；生产模式拒绝公开示例值。
- 控制台启用时强制要求唯一的至少 12 字符管理员 passphrase，并拒绝用户名同值和已知弱口令。
- `AGENTSQL_INSECURE=1` 仅允许本地公开测试凭据，不会关闭认证、安全规则或 fail-closed 行为。
- API Key 仅持久化 SHA-256 哈希；数据源口令使用启动 SECRET 加密存储。
- 升级前必须成对备份 `agentsql.db` 与对应 SECRET。直接更换 SECRET 会让既有数据源口令无法解密，并非无损轮换。

### Deployment

- Docker 运行阶段使用非 root 用户，持久化 SQLite 数据目录，并内置 healthcheck。
- Compose 默认仅将 AgentSQL、可选 PostgreSQL demo 和 MySQL demo 端口绑定到宿主机 `127.0.0.1`。
- Dockerfile、Compose、Makefile 与两个二进制的默认版本统一为 `v0.1.0`。
- 示例环境文件不再携带可直接使用的 SECRET 或管理员口令。

### Compatibility

- 被防护业务库支持 MySQL 8 与 PostgreSQL 14、15、16、17、18。
- v0.1 元数据与审计后端仅支持 SQLite；PostgreSQL 元数据/审计库属于 v0.2 的 T28 路线。
- 已知限制：JOIN/自连接只做表级授权，不做投影列归属；`*`/`schema.*` 表授权表示匹配表全列授权；MCP 不暴露跨请求会话/事务参数，多语句事务能力后置到 v0.2。

## 后置路线（v0.2 之后）

- T29：国产/商业业务库矩阵（达梦、金仓 KingbaseES、瀚高 HighGo、GaussDB、OceanBase、TiDB、Oracle、SQL Server 等）；企业版后置。
- T30：合规与身份管控（等保报告导出、WORM、SIEM、行级安全、敏感发现、SSO/LDAP/MFA、多租户、RBAC、会签工单等）；企业版后置。
- T31：HA、集中管控与规模交付（K8s Operator、跨实例审计总线、UEBA、私有化与 SLA）；企业版后置。
- Cloud/SaaS 形态在企业版能力成熟后再评估，不进入近期开源路线。

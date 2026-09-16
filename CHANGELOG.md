# Changelog

本项目的重要变化记录于此，格式参考 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)。

## [v0.2.0] - Unreleased

### Added

- 控制台总览新增经 Bearer 鉴权的 SSE 实时事件流，并在连接中断时降级为既有刷新路径。
- 元数据与审计控制面新增 PostgreSQL 支持（兼容 15+，基准与 CI 为 PostgreSQL 18），并支持独立 metadata/audit 数据库。
- 新增 SQLite 到 PostgreSQL 的数据迁移、校验与序列对齐命令 `agentsqlctl migrate-sqlite-to-postgres`。
- 规划新增可自托管的开源 Live Demo 套件，提供只读、安全、每日重置的在线演示路径。

### Changed

- 生产 PostgreSQL 部署采用一次性 migration 账号、最小权限运行账号与 `store.auto_migrate:false`。
- 独立审计库布局保留不可变审计 ID，以及 `approvals.audit_id` 与审计记录的应用级关系。

### Compatibility

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

## 后置路线

- T26：在线 Live Demo。
- T27：控制台大屏 WebSocket 实时流。
- T28：PostgreSQL 18 元数据/审计库，属于开源 v0.2 路线。
- T29：国产/商业业务库矩阵；企业版后置，不承诺进入 v0.2 首批。
- T30：合规与身份管控；企业版后置，不承诺进入 v0.2 首批。
- T31：HA、集中管控与规模交付；企业版后置，不承诺进入 v0.2 首批。

# AgentSQL v1.7.0 发布说明

**语言 / Language:** [简体中文](release-notes-v1.7.0.md) | [English](en/release-notes-v1.7.0.md)

v1.7.0 为瀚高 HighGo 增加独立数据源类型和受限只读执行路径，并对金仓 KingbaseES V9R1C10 PG 模式补充有服务端身份校验的 TLS 和凭据轮换。支持声明限定为已验证的指定实例、协议模式、单实例与合成数据；本版不宣称完整生产支持。

## 新增与改进

- 控制台提供独立 `highgo` 类型，默认端口 5866，并明确显示只读 SQL 子集；旧 `postgres` 数据源不会自动改为瀚高类型。
- 每条新建连接都使用厂商目录函数校验精确版本，检查 PG 模式与该连接的默认只读事务状态。普通 PostgreSQL、其他瀚高版本或模式校验失败时拒绝连接；连接池重连也重新校验。
- 只允许单条、显式 schema、单表、直接列投影、非恒真 WHERE 和 LIMIT 的 SELECT。写入、DDL、JOIN、函数、聚合、排序、锁、CTE、多语句与无法证明来源的 SQL 被拒绝；数据库账号还须仅有最小 SELECT 权限。
- 表级策略、手机号脱敏、持久审计、元数据发现与 MCP 查询在该受限路径联通。PostgreSQL B2 列级绑定与 B5 写事务不扩展到瀚高。
- MCP Inspector CLI 与 Dify 1.17.1 MCPClient 对同一真库分别完成允许和拒绝请求；真实授权撤销、停库、独立审计库故障、锁等待超时和客户端取消均进行故障关闭与恢复验证。
- PostgreSQL 协议数据源可显式选择 `verify-full`，提供网关可读的绝对 CA 文件路径及可选证书主机名；验证配置无明文回退，错误 CA 和主机名均拒绝。金仓 V9R1C10 PG 模式隔离实例通过证书轮换、数据库账号密码轮换、MCP Inspector 与 Dify 1.17.1 的允许/拒绝/脱敏/持久审计，以及四 Agent 小样本并发基线。
- 管理 API 在显式更新 TLS 模式时整体替换 CA 和主机名配置，支持证书轮换与关闭模式切换，避免继承旧信任根。

## 兼容与限制

- 管理 API 与 MCP 工具格式未改变；metadata/audit schema 无新增迁移。正式安装仍须先备份 metadata、audit 和密钥，按 [验收记录](v1.7.0-acceptance.md)复核升级及恢复。
- 使用 pgx/v5 的 PostgreSQL wire protocol 是驱动选择，不代表继承 PostgreSQL 的完整语义或支持级别。
- 达梦 DM8 当前 Go 驱动的 TLS 握手不验证服务端证书，AgentSQL 继续拒绝达梦显式 TLS 配置。达梦和金仓的正式主备切换、生产负载及更多 SQL 方言尚未完成，均保持未验证；单实例断线恢复不等于 HA。证据与边界见[达梦/金仓验收记录](v1.7.0-dm-kingbase-depth-acceptance.md)。
- 未验证 HighGo Oracle/MySQL 模式、其他版本、分布式和 HA、TLS、连接池故障切换、扩展类型/OID、生产负载、厂商认证及长期许可。上述能力保持未验证。
- 已注册数据库类型由 28 款增加至 29 款；金仓 KingbaseES V9 仍单列为指定实例 PG 协议路径，不计入注册类型。

正式发布证据、提交 SHA、签名资产、镜像摘要、官网和 Live Demo 状态见 [v1.7.0 正式发布验收](v1.7.0-release-evidence.md)；瀚高与金仓的真库边界分别见其专项验收。

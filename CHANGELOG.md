# Changelog

本项目的重要变化记录于此，格式参考 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)。

## [v0.3.0] - 2026-09-XX

> 发布准备稿：版本号与日期待 GA 当日以正式 tag 为准同步。本版主题是“敏感数据保护纵深”，不改变默认拒绝的安全链路。

### Added

- **安全事件通知（T37）**：新增按决策过滤的安全事件外发，支持 Webhook 与 Syslog 通道，live-only、best-effort；默认关闭、默认不含 SQL 原文，通知失败不影响审计与 SQL 决策。Webhook 内置 SSRF fail-closed 防护（阻断内网与云元数据地址）、HMAC 与钉钉机器人签名、有界队列和投递指标；控制台提供通道设置、密钥留空保留、私网端点告警与投递健康。通知密钥以 AES-GCM 加密存储，配套 0002 迁移（SQLite/PostgreSQL 双流）并纳入 SQLite→PG 搬迁清单。
- **敏感列发现（T38）**：新增 PII 敏感列发现（分类器/扫描器/顾问与置信度状态机），候选列采样在表数、列数、样本行数上均有硬上限；样本仅在内存参与判定，不持久化、不返回页面、不进入审计正文或通知。新增发现管理 API 与“发现→脱敏草稿”apply saga，生成 `enabled=false` 草稿，控制台提供发现抽屉与一键草稿；发现仅在管理面提供，不作为 MCP 工具暴露，同步 deadline 12 秒。`mask_rules` 新增 `enabled` 标志与数据源/列唯一索引（0003 双流迁移，含重复预检），脱敏引擎只加载启用规则，管理操作补审计 action/actor/details，冲突返回 `ErrMaskRuleConflict`。
- **脱敏算法扩展（T40–T43）**：敏感类型从手机号/邮箱扩展到九类，新增身份证、银行卡、IP 地址、出生日期的部分遮蔽（`mask`），未识别组合 fail-closed；新增不可逆 HMAC-SHA256 指纹（`hash`，专用密钥、不可解密还原）、整值阻断（`block`，非空值统一替换为 `***`，不泄露长度与等值关系）、数值分桶/日期截断（`range`，仅用于 `number`/`date`，无需密钥保留粗粒度分布）。发现流程不推荐 `range`/`hash`/`block`/`generic`，需在脱敏规则页手工配置。
- **审计导出（T43/T45）**：审计导出新增 Excel/WPS 可直接打开、带公式注入防护的中文 CSV，以及面向机器的 JSONL；导出成功写入 `audit.export` 管理留痕。
- **表.列感知脱敏（T46）**：脱敏规则支持全局列、`表.列`、`模式.表.列`三档作用域，按来源表/来源列精确命中；新增 `schema_name` 与 0005 控制面迁移（SQLite/PostgreSQL 四条迁移流），存量规则一次性固化为全局列规则、保持升级前实际生效语义。解析器新增直接投影列来源关系解析，JOIN 中已用表名/别名限定的列可精确归属；对 JOIN 裸列、`SELECT *`、CTE 外层等无法确定列归属且可能涉及受保护表的情况 fail-closed 固定阻断为 `***`，不可关闭。敏感列发现改为生成 table-only 草稿（空模式、真实表名、`mask`、禁用），不同表的同名列分别成稿；PostgreSQL 空模式匹配任意模式、MySQL 空模式表示当前库，需要精确模式时人工收紧。控制台三档表单、规则列表新增模式/表列、发现结果按完整 `模式.表.列` 展示并对全局规则覆盖（CoveredByGlobal）去重。

### Changed

- 前端改为路由级代码分割，按 react/antd/echarts 等拆分无环 vendor chunk，降低首屏体积。
- 脱敏规则由“数据源 + 归一化列名”两级匹配升级为“数据源 + 模式 + 表 + 归一化列名”四元匹配；冲突区分物理键冲突（含禁用草稿，`MASK_RULE_CONFLICT`）与启用规则间的作用域/算法冲突（`MASK_RULE_SCOPE_CONFLICT`）。
- 敏感列发现草稿由数据源级列名草稿改为 table-only 的表.列草稿。

### Security

- 通知通道默认关闭、默认不含 SQL，Webhook SSRF fail-closed 并校验签名；敏感列发现样本不落盘、不作为 MCP 工具暴露。
- 表.列脱敏对无法确定归属的敏感列默认 fail-closed 阻断且不提供放行开关；`hash` 为不可逆指纹而非加密，`range`/`block` 在数据库执行后处理，不替代只读账号、列级权限与安全视图。
- v0.3 全部新增提交通过全历史密钥/凭据扫描，未发现真实密钥、私钥、云访问密钥或被跟踪的 `.env`。
- 发布前完成 govulncheck 与 npm audit 双扫描并加固：修复核心 PG 驱动 pgx 的 SQL 注入类公告 GO-2026-5004（pgx/v5 升级 v5.9.2，调用点为 PostgreSQL 执行器），间接依赖 grpc、edwards25519 升至含修复的补丁版本；前端清除全部会进入生产产物的 high 级公告（axios 升至 1.20.0、react-router-dom 升至 6.30.6）。残留 vitess GO-2026-4567 仅静态链接其 SQL 解析器、不运行任何 vitess 服务端，漏洞 sink 不可达（修复版本 v0.22.4 与 Go 1.27 构建不兼容，于后续版本跟进）；vite/esbuild 公告仅存在于本地开发服务器、不随生产静态产物交付，echarts 与 react-router 残留 moderate 在本产品纯 CSR、无 HTML 富文本渲染、无用户可控外部跳转的架构下不可达。

### Fixed

- 修复表.列脱敏中已解析到某张表的列可能被同一语句里其他表同名规则误伤的问题；未解析安全兜底从此只作用于真正无法确定归属的列。
- 修复 hash 算法门控后 demo seed 未按数据源校验演示脱敏规则、以及控制台脱敏变更重复弹出全局错误提示的问题。

### Compatibility

- 被防护业务库仍为 MySQL 8 与 PostgreSQL 14/15/16/17/18；控制面支持默认 SQLite 与 PostgreSQL 15+（基准 PostgreSQL 18），metadata 与 audit 可分库。
- 升级需顺序应用 0002（通知）、0003（发现草稿）、0004（range）、0005（表.列作用域）迁移；生产 PostgreSQL 建议 `store.auto_migrate:false` 由迁移账号显式执行，详见部署指南。
- 多表 JOIN 与自连接的授权仍为表级（列级白名单不随投影收紧），本版变化在脱敏层而非授权层。
- 首版不提供 hash 多密钥版本/在线轮换；发现流程不生成 `range`/`hash`/`block` 规则。

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
- T30：合规与身份管控（等保报告导出、WORM、SIEM、行级安全、SSO/LDAP/MFA、多租户、RBAC、会签工单等）；企业版后置。敏感列发现与表.列脱敏已在 v0.3 落地。
- T31：HA、集中管控与规模交付（K8s Operator、跨实例审计总线、UEBA、私有化与 SLA）；企业版后置。
- Cloud/SaaS 形态在企业版能力成熟后再评估，不进入近期开源路线。

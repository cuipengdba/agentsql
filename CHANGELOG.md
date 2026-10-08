# Changelog

本项目的重要变化记录于此，格式参考 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)。

## [v0.5.0] - 2026-10-16（待发布）

> 本条目是发布准备草稿，**尚未发布**，尚未创建 v0.5.0 tag 或 GitHub Release。相对已发布 v0.4.0 的详细证据、资产状态和发布日闸门见 [v0.5.0 Release Notes](docs/release-notes-v0.5.md)。

### Added

- v0.5 控制平面新增 TOTP MFA、OIDC Authorization Code + PKCE 和 LDAP/AD TLS bind 登录；外部组必须显式映射到租户角色，MFA/SSO/目录状态异常均 fail-closed，并复用 refresh token 轮换、重放整族吊销和 logout 撤销链路。

- 新增 DM、Oracle 和 YashanDB 独立 dialect 的有界连接/元数据能力；Oracle 完成严格只读子集和 EXPLAIN 归一化实测。DM8 Pack3、`COMPATIBLE_MODE=0` 的只读 SELECT、列血缘、R010 表策略、审计和手机号脱敏链路经批六十五真库验证通过，矩阵转绿；R006 未验证，多行分页/JOIN/函数/系统目录未测，写入未放开。
- 注册表新增 7 类 21 款 NoSQL / 向量连接级数据源（键值、文档、宽列、图、时序、OLAP、向量），加上 6 款关系型共 8 类 27 款；只提供有界 ping、版本、Schema 和只读预览，不扩展为完整 SQL 安全闭环。逐项类型与默认端口见 [NoSQL 支持范围](docs/nosql-support.md)。
- DM/Oracle 的受控 SELECT parser 补充 ROWNUM、投影别名、NULL 语义与分页等离线边界回归；YashanDB 增加 `NewYashanParser()` 离线 SELECT profile，批七十一接通标准 `NewParser`、通用规则与窄 SELECT 执行路径。旧未发布 dry-run 包内的独立客户端 23.4.7.100 通过原生驱动只读写入拒绝探针；真实数据库流水线测试通过 SELECT、列血缘、R010、手机号脱敏和三条测试审计，HTTP/MCP 与持久化审计仍待验证。
- 新增 parser 畸形输入、错误稳定性和并发隔离，以及 pipeline 空 SQL/不完整 SQL 的执行前拒绝测试；覆盖范围和离线依赖限制见 [测试覆盖说明](docs/test-coverage-notes.md)。
- 新增 RBAC / 多租户 MVP：本地用户、租户、角色、权限、多角色、角色继承与管理 API 逐路由授权；全部能力开放，无许可门控。
- 新增企业审计查询/报表与合规导出配套，以及无需克隆源码的自包含五分钟快速上手演示栈。
- 企业审计导出新增纯 Go、多页 CJK PDF 与合规 ZIP；ZIP 固定携带 CSV、JSONL、PDF、摘要及逐文件 SHA-256 `manifest.json`，CLI 可严格校验成员、大小和摘要，管理 API/控制台复用现有筛选、RBAC、10000 行上限与 `audit.export` 留痕。
- 新增英文首页、快速上手、管理、MCP 接入、安全与覆盖度文档。
- 新增金仓 + 瀚高联合案例验证记录、运营口径与演示 SQL，严格区分指定镜像协议路径实测、商业版终验和厂商认证。
- 新增 MCP Registry 描述、官网生态/兼容板块与社区入口，并系统补齐国产数据库研究、优先级、联合案例和口径文档。

### Changed

- 瀚高 HighGo 9.0.10 企业版在 PG 模式、单实例合成表范围，经批七十真库验证通过解析/列血缘、表级授权、受控只读 SELECT、手机号脱敏、R006 拒绝及 allow/deny 审计链路；用户于 2026-10-08 拍板，矩阵档位转为 🟢 指定实例防护链路实测通过。TLS、连接池故障切换、取消/超时、EXPLAIN、扩展类型/OID、系统目录差异、B2 列级授权、parse-error 审计分支（本轮未触发）、跨版本与生产负载未测；不构成厂商认证或生产支持承诺。见[真库验证记录](docs/highgo-verification.md)。
- OpenTenBase PostgreSQL 内核新增严格、版本受限的分布式计划归一化，并在指定 v2.5.0 单机 GTM/CN/DN 拓扑完成最小安全闭环；TXSQL/MySQL 内核仍待官方环境实测。
- PolarDB for PostgreSQL 指定社区镜像通过现有 `postgres` 路径完成实验性安全闭环，未新增别名或厂商识别开关。
- PolarDB-X 复用 `db_type=mysql`，增加从严格识别的 `LOGICAL EXECUTIONPLAN` 到 `EXPLAIN EXECUTE` 的两阶段 fail-closed 计划适配、兼容 corpus 与 opt-in E2E；不与 PolarDB for PostgreSQL 混称。
- OpenTenBase、TiDB、OceanBase 和 PolarDB-X 增加 fail-closed EXPLAIN 适配与回归 fixture；未知版本、计划列或节点继续拒绝。
- MCP Streamable HTTP 首次 `initialize` 可不预先携带 `MCP-Protocol-Version`；后续请求的版本、会话与 Agent/Key 绑定校验保持严格。
- 演示台场景卡对齐真实剧本与审批结果；业务错误口径进一步区分可预期授权失败、对象缺失与数据库执行错误。
- 官网搜索闸门调整为放行主站、保持 `/demo/` 禁止索引；实际部署状态仍需发布日外网复核。
- `scripts/release-dryrun.ps1` 提供一条命令生成/校验 15 个预期资产的 dry-run 路径，并提供生产准备与 `-ValidateOnly` 核对路径；正式签名、上传和发布仍待发布日闸门。批六十一的指定 Linux 环境 PostgreSQL parser P99 两组各 5/5 达标，发布日仍须独占复测。
- 自 v0.5 起，开源版许可证由 AGPLv3 调整为 Apache-2.0，并提供独立商业许可说明。

### Fixed

- 修复 MCP 首次握手协议版本门禁与主流客户端标准流程不兼容的问题。
- 修复 Demo 表外键与密封 JOIN 剧本冲突，并加固每日重置的 seed/gateway 启动时序。
- 修复 R005 生产动态告警缺失：通用路径按 EXPLAIN 评估，通用执行与 PostgreSQL 列级授权路径在 `row_limit` 实际截断时补充告警；同时修正前端规则元数据与文档口径。

### Known Issues

- Oracle 的完整网关授权/脱敏闭环、YashanDB HTTP/MCP 与持久化审计终验、KingbaseES V9 以及 GaussDB / TDSQL 等商业版的目标环境终验仍未完成。崖山 C 客户端再分发已获厂家口头授权；仓库无书面授权文件。
- 登录限速/锁定和 MCP transport session 跨进程持久化仍未交付；OIDC/LDAP 的真实企业 IdP/目录兼容矩阵需在目标环境逐项验收。

## [v0.4.0] - 2026-09-30

> 已正式发布并创建 `v0.4.0` tag。本版主题是“生产级列授权与跨请求事务安全”，在既有默认拒绝链路上补齐 PostgreSQL 列级授权、MCP 逻辑会话、审计完整性与一键体验。

### Added

- **MCP 双承载与会话化传输**：继续同时提供本机 `stdio` 与 Streamable HTTP `/mcp`，保留 `list_datasources`、`list_schema`、`explain_query`、`query`、`execute_write`、`request_approval`、`get_approval_result` 7 个基础工具；Streamable HTTP 默认启用有状态传输会话，`initialize` 返回 `Mcp-Session-Id`，也可显式回退 stateless。
- **B2 PostgreSQL 列级授权**：新增 PostgreSQL 14–18 的 SELECT 列级授权闭环，覆盖元数据、列级白名单、投影血缘、OID/catalog 绑定、请求预留、控制栅栏与审计；出厂默认开启，普通基表默认使用免安装扩展的 `CATALOG_CLOSED_V1`，view、matview 与复杂血缘可选用签名 `agentsql_binder` 原生扩展。授权越界、绑定不确定或运行期 lease 失效均 fail-closed；MySQL 不进入 B2 路径。
- **B5 跨请求逻辑会话与计划事务**：新增默认开启的 PostgreSQL 跨请求逻辑会话、强一致会话目录、owner epoch/sticky、计划封存、顺序 DML、终态 CAS/final fence、故障恢复与应急 WAL；增加 `open_session`、`close_session`、`get_session_status`、`begin_transaction`、`execute_transaction_statement`、`commit_transaction`、`rollback_transaction`、`get_transaction_status` 8 个 MCP 工具。MySQL 跨请求事务在 v0.4 固定不支持。
- **脱敏与敏感列发现增强**：结果层完整覆盖 `mask`、HMAC-SHA256 `hash`、整值 `block`、数值分桶/日期截断 `range` 四类算法；新增版本化 hash 密钥 manifest、登记/核验、启动强对账、重启式计划切换与审计发件箱。敏感列发现扩展到九类，并可按建议算法生成默认禁用的表.列脱敏草稿；样本仍只在内存使用，不落盘、不进入审计正文或通知。
- **审计完整性与导出**：新增可回填、可校验、带状态与 readiness 的审计哈希链，以及 `agentsqlctl chain status/verify/provision`、只读管理 API 和控制台完整性面板；审计导出继续提供带中文表头与公式注入防护的 CSV 和面向机器的 JSONL，并记录 `audit.export` 管理留痕。
- **通知外发**：提供按决策过滤的 Webhook 与 Syslog 通道，保持 live-only、best-effort、默认关闭且默认不包含 SQL 原文；Webhook 具备 SSRF fail-closed、签名、有界队列和投递指标，通知失败不改变 SQL 决策或权威审计记录。
- **5 分钟快速上手与演示栈**：新增无需克隆源码、只下载一个 Compose 文件即可启动的自包含 PostgreSQL/MySQL Demo，并提供带真实授权、脱敏、越权拒绝与审批结果的场景卡；Linux 安装器、发布包与 GHCR 镜像同时覆盖 `linux/amd64` 和 `linux/arm64`。
- **生态收录与兼容预研**：新增 MCP 官方 Registry 描述文件并完成 Registry / Glama 收录；补充电科金仓 KingbaseES、瀚高 HighGo、openGauss（GaussDB）、IvorySQL、TiDB、OceanBase、TDSQL、崖山数据库 YashanDB、达梦数据库 DM 共 9 家候选数据库的兼容预研与联合案例大纲。上述名单是规划与合作方向，不代表 v0.4 已支持。

### Changed

- 数据库错误统一进入 `decision=error` 业务信封并贯穿 Playground、Ping、MCP、审计、SSE 与通知，稳定记录 `error_code` / `error_stage`；可预期授权失败、SQL 语法错误、对象不存在、连接/超时等口径分离，控制台展示可执行的中文提示。
- PostgreSQL B2 列级授权与 B5 逻辑会话/计划事务由预研门控切换为出厂默认开启；`/healthz`、`/readyz` 与运维面同步报告激活、漂移、恢复和故障状态。
- 查询结果脱敏改用投影血缘对齐 MySQL/PostgreSQL 顶层直接投影；能唯一解析的列按物理来源精确匹配，无法确定归属且可能涉及受保护表时固定阻断为 `***`。
- 发布工程新增生产 Ed25519 签名器、可复核开发签名工具、架构感知安装器与 GHCR 多架构构建流程；公开文档、示例与产物版本统一为 v0.4.0。

### Security

- B2 使用 sealed transport、语言级能力隔离、资源上限、catalog/OID 锁与激活栅栏；原生扩展验证 ABI、能力与签名摘要，免扩展闭合模式不确定时拒绝，不静默退回表级放行。
- B5 事务在开始前封存完整有序 DML 计划，执行阶段不可替换 SQL；会话 continuation proof、owner fence、终态证据一致性、取消污染与崩溃恢复共同阻止跨 Agent、乱序和重复提交。
- SQL 解析器升级并对 `VALUES` table statement 等边界 fail-closed；脱敏血缘补充零泄漏负向集，hash 密钥材料不进入配置回显、日志、审计或指标。
- 审计链使用 canonical envelope 与哈希派生覆盖写入、审批和批量落盘路径；链状态异常可使相关写组件 readiness fail-closed，独立 verifier 支持固定退出码核验。

### Fixed

- 修复 MCP 有状态会话测试与 Demo reset helper 未先完成 `initialize` 握手的问题。
- 修复 Windows 下 B5 应急 WAL 目录 `fsync` 被拒绝、审计链监控错误复用已结束生命周期上下文，以及 MySQL 镜像漂移和容器冷启动超时导致的回归不稳定。
- 修复解析失败未返回稳定 `DB_SYNTAX_ERROR`、MySQL `INSERT ... VALUES` EXPLAIN 分类和数据库错误被误报为 HTTP 500；进一步区分预期授权失败与对象缺失提示。
- 修复 Demo 表外键与密封 JOIN 剧本冲突，并加固每日重置的 seed/gateway 启动时序。

### Compatibility

- 被防护业务库仍支持 MySQL 8 与 PostgreSQL 14/15/16/17/18；控制面支持默认 SQLite 与 PostgreSQL 15+。B2 列级授权和 B5 跨请求事务仅在 PostgreSQL 路径提供，MySQL 保持既有表级授权与单请求执行边界。
- 官方原生包支持 glibc 2.28+ 的 `linux/amd64`、`linux/arm64`，GHCR tag 为相同双架构 manifest；musl/Alpine、CentOS 7 与其他原生架构仍不支持。
- B2/B5 与有状态 HTTP 传输均提供显式回退开关；关闭 B5 不影响 7 个基础 MCP 工具，关闭 HTTP transport session 也不会隐式改变 B5 逻辑会话配置。

## [v0.3.0] - 2026-09-29

> 发布准备稿：目标 GA 日期为 2026-09-29，正式 tag 尚未创建。本版主题是“敏感数据保护纵深”，不改变默认拒绝的安全链路。

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

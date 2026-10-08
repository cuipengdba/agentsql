# AgentSQL 安全政策

## 支持版本

AgentSQL 仅为下表列出的维护系列评估和提供安全修复；是否为某个问题提供补丁，还会结合影响范围、修复风险和发布状态判断。

| 版本 | 状态 | 安全修复支持截止 |
| --- | --- | --- |
| v0.5.x（待发布） | 发布后支持 | 至下一主版本（v0.6.0）发布后 6 个月，且不早于 2027-10-31 |
| v0.4.x（当前已发布） | 支持 | 2027-04-30 |
| 0.3.x 及更早 | EOL | 不再提供安全修复 |

请先在可控环境复现，并尽量使用最新的受支持版本确认问题仍然存在。

## 数据库接入状态与安全边界（截至 2026-10-08）

以下分组描述 **AgentSQL v0.5.0 待发布代码**的接入范围与证据，不是数据库厂商的认证清单，也不改变上面的 AgentSQL 安全修复版本政策。注册表共 8 类 27 款（6 款关系型 + 21 款 NoSQL / 向量）；KingbaseES V9 是额外的待验证候选，不计入 27 款。

档位：🟢 解析、授权、受控执行、脱敏、审计链路在所述范围通过；DM8 绿色仅限指定 Pack3 实例与用例，不代表完整方言兼容。HighGo 绿色仅限 9.0.10 企业版 PG 模式、单实例合成表用例，不代表完整方言兼容。🔵 受控只读＝连接、元数据、只读查询子集；🔷 连接级＝ping、版本、Schema、只读预览；🟡 待验证＝等厂商环境。所有实测仅表示指定容器镜像、拓扑及合成用例通过，**非数据库厂商官方认证或生产支持承诺**。所有数据库必须经 AgentSQL 网关，使用专用最小权限账号；解析、目录、执行计划或授权事实不明时 fail-closed。协议兼容不等于安全语义相同；PostgreSQL B2 列级授权和 B5 受控事务不能因线协议相似而自动扩展到其他产品。

### 关系型（6 款已注册 + 1 款待验证）

- 🟢 **PostgreSQL 14–18**：原生 `postgres` 路径；B2/B5 仍受自身版本、目录和能力检查约束。
- 🟢 **MySQL 8.0**：原生 `mysql` 路径；不继承 PostgreSQL B2/B5 证明。
- 🔵 **Oracle 23ai**：独立 `oracle` 严格 SELECT 子集；不能据此推断企业版或完整 Oracle 方言已通过。
- 🟢 **达梦 DM8**：Pack3、`COMPATIBLE_MODE=0` 实例的只读 SELECT、列血缘、R010 表策略、审计和手机号脱敏实测通过；R006 未验证，多行分页/JOIN/函数/系统目录未测，写入未放开。
- 🔵 **崖山 YashanDB 23.4.1.109**：独立 `yashan` parser 与窄 SELECT 路径已接通；独立客户端真库流水线测试通过 SELECT、列血缘、R010、手机号脱敏及三条测试审计，最小权限账号的数据库写入拒绝另经原生探针验证。HTTP/MCP、持久化审计及最小权限账号与流水线的组合未测；EXPLAIN、写入继续失败关闭。
- 🔵 **SQL Server 2025（17.x）**：独立 `sqlserver` 严格只读子集；不提供完整 T-SQL、写入、B2/B5 或厂商认证。
- 🟡 **金仓 KingbaseES V9**：等厂商镜像与 license；旧第三方镜像因许可证过期未能验证。V9R1C10 目标环境拟于 10 月 8 日取得，取得前不声明接入通过，且须确认 PG 兼容模式。

### 键值 KV（🔷 连接级）

- **Redis、Valkey（复用 Redis 适配）、Memcached**：仅 ping、版本、Schema、只读预览；不声明完整命令集、写入控制或数据库原生授权已通过。

### 文档 Document（🔷 连接级）

- **MongoDB、Couchbase、CouchDB**：仅连接级只读预览；不声明完整文档查询或服务端安全语义已通过。

### 宽列 Wide-Column（🔷 连接级）

- **Cassandra、ScyllaDB、HBase**：仅连接级只读预览；不声明完整 CQL / HBase 操作或写入能力。

### 图 Graph（🔷 连接级）

- **Neo4j、JanusGraph、NebulaGraph**：仅连接级只读预览；不声明完整图查询或遍历能力。

### 时序 Time-Series（🔷 连接级）

- **InfluxDB、Prometheus、TimescaleDB**：仅连接级只读预览；不声明完整时序查询或 PostgreSQL B2/B5 能力。

### OLAP（🔷 连接级）

- **ClickHouse、Apache Doris、StarRocks**：仅连接级只读预览；不声明完整分析查询或写入能力。

### 向量 Vector（🔷 连接级）

- **Milvus、Qdrant、Weaviate**：仅连接级只读预览；不声明完整向量检索、索引或服务端安全能力。

### 其他协议候选与有界证据（不计入上述 27 款）

- **PolarDB for PostgreSQL 15**：指定社区镜像经 `postgres` 路径完成最小闭环；商业服务、HA、TLS 与 B2/B5 终验未覆盖。
- **IvorySQL 5.3**：指定社区版 PG 协议路径完成最小闭环；Oracle 兼容模式和 HighGo 商业版不继承此结论。
- **openGauss 7.0.0-RC3**：指定社区版 PG 协议路径完成最小闭环；GaussDB 商业版和 B2/B5 未终验。
- **HighGo SEE 4.5**：指定第三方镜像 PG 协议路径完成最小闭环；该旧记录与下述 V9.0.10 企业版真库验证分别成立。
- 🟢 **瀚高 HighGo 9.0.10 企业版**：PG 模式、单实例合成表范围内，解析/列血缘、表级授权、受控只读 SELECT、手机号脱敏、R006 拒绝、allow/deny 审计链路实测通过；复用 `postgres` 数据源路径。未测 TLS、连接池故障切换、取消/超时、EXPLAIN、扩展类型/OID、系统目录差异、B2 列级授权、parse-error 审计分支（本轮未触发）、跨版本与生产负载；不代表厂商认证或生产支持承诺。证据见[HighGo 9.0 真库验证](docs/highgo-verification.md)。
- **OpenTenBase v2.5.0**：指定单机 GTM/CN/DN 拓扑和已知计划形态完成最小闭环；生产拓扑、其他计划形态及 B2/B5 未证明。
- **TDSQL for PostgreSQL**：PG 协议候选，商业版待目标环境终验；不能用 OpenTenBase 结果替代。
- **TDSQL for MySQL**：MySQL 协议候选，商业版待目标环境终验；不与 TDSQL for PostgreSQL 混同。
- **TXSQL**：OpenTenBase 社区 MySQL 方向，独立 MySQL 路线；产品专用路径未实测，不能用 MySQL 8 或 OpenTenBase 结果替代。
- **TiDB 7.5.1 / OceanBase CE 4.4.2.1**：MySQL 兼容候选，EXPLAIN 适配与回归 fixture 已落地；fixture 不是目标环境完整闭环或厂商认证。
- **PolarDB-X**：旧官方 `2.0.1` 单容器经 `mysql` 路径有界实测；完整 CN/DN/CDC、商业服务、分布式语义及 B2/B5 未证明。

> **YashanDB 客户端再分发授权：**已获厂家口头授权（C 客户端再分发）；仓库无书面授权文件。本轮没有从服务器镜像复制或打包 C 客户端，也没有重建待发布的 Linux/GHCR 发行物。服务器镜像自带库在既有 Go 驱动真库探针中返回 `YAS-02143`；本机旧的未发布 dry-run 包内缓存的独立客户端 23.4.7.100 通过本轮原生驱动和真实数据库流水线测试。部署时用户自行取得独立客户端并在启动前设置 `LD_LIBRARY_PATH`；缺 Go 驱动或运行库时连接失败关闭。Go 驱动须通过 `CGO_ENABLED=1` 与 `-tags yashan` 编入二进制。上述流水线测试未覆盖 HTTP/MCP 接口或持久化审计库。

接入状态的可复现证据见[兼容性调研](docs/ecosystem-db-compat-research.md)、[v0.5 发布说明](docs/release-notes-v0.5.md)和[金仓/瀚高联合案例](docs/joint-case-kingbase-highgo.md)。产品线区分参见腾讯官方的 [TDSQL PostgreSQL 版](https://cloud.tencent.com/document/product/1129)、[TDSQL MySQL 版](https://cloud.tencent.com/product/dcdb)以及 OpenTenBase 的[独立 TXSQL 下载入口](https://docs.opentenbase.org/en/download/)；协议相容不等于安全语义相同。

## 私密报告漏洞

不要在公开 Issue、Discussion、Pull Request、日志网站或社交媒体中披露未修复漏洞、利用代码、真实凭据或敏感数据。

请使用以下任一私密渠道：

- 发送邮件至 [87326549@qq.com](mailto:87326549@qq.com)；
- 若仓库 Security 页面显示“Report a vulnerability”，使用 [GitHub 私密漏洞报告入口](https://github.com/cuipengdba/agentsql/security/advisories/new)；若入口不可用，请使用上述邮件。不要用公开 Issue 代替。

报告中请尽量包含：

- 受影响的 AgentSQL 版本、提交号及部署方式；
- 操作系统、数据库类型与版本，以及 MCP 接入方式；
- 问题描述、可复现步骤和最小化配置；
- 实际影响、攻击前提、所需权限及是否可绕过网关；
- 已观察到的日志或错误信息，并移除密钥、密码、DSN 和业务数据；
- 建议的修复或缓解措施（如有）；
- PoC 的安全获取方式。请优先提供最小、无害的复现；不要使用公开链接传播利用代码，也不要在未经确认前发送恶意载荷或真实数据。

目前不提供 PGP 公钥。如材料不适合直接通过邮件发送，请先发送不含敏感细节的说明，协商后续传输方式。

## 响应与披露流程

收到报告后，我们通常按以下流程处理：

1. 确认收到并检查报告是否完整。我们会尽力在 3 个工作日内给出初步响应。
2. 复现和评估影响，确认受影响版本、严重程度及临时缓解措施。我们会尽力在 7 个工作日内提供初步研判；复杂问题可能需要更长时间。
3. 在私密渠道中协调修复、回归验证、发布计划和披露时间。修复时间取决于问题复杂度与发布风险，不构成服务等级承诺。
4. 修复可用后协调公开披露；必要时先给用户合理的升级窗口，再发布安全公告与修复说明。

在不影响调查和用户安全的前提下，我们欢迎报告者参与验证。经报告者同意，公开公告中可以致谢；如希望匿名，请在报告中说明。请在双方协调披露前保持漏洞细节私密。

## 安全模型与边界

AgentSQL 是面向 AI Agent 的数据库安全网关和 MCP Server。它对经过网关的请求执行身份认证、SQL 解析、授权与规则评估、受控执行、结果脱敏和应用层审计，用于降低 Agent 误操作、越权访问和危险 SQL 的风险。

部署和风险评估时必须同时考虑以下边界：

- 保护范围只覆盖经过 AgentSQL 网关、并使用受控运行账号访问数据库的请求。能够绕过网关直连数据库的 owner、superuser、DBA 或其他高权限账号，不受 AgentSQL 规则保护。
- 数据库运行账号仍是最终权限边界。应使用最小权限、按用途隔离的账号；AgentSQL 不能补偿过度授权，也不能阻止数据库管理员在数据库侧直接操作。
- 结果脱敏是在结果层按列规则打码，用于降低特定字段直接暴露的风险，不是完整 DLP。复杂表达式、重命名、聚合、视图或其他数据变换可能超出列匹配能力，不能据此声称敏感数据已被全面发现或阻断。
- 审计记录属于应用层审计，不是不可篡改账本，也不是法规级 WORM、保留锁或合规存证。拥有底层存储或数据库高权限的人员可能修改或删除记录；有合规要求时应另行使用受控归档、外部 SIEM 或合规存储。
- PDF/ZIP 合规导出包含原始 SQL、错误和详情等高敏审计内容，应按敏感数据传输与留存。ZIP 的 `manifest.json` 使用逐文件 SHA-256 检测意外损坏、成员增删和载荷替换，但没有签名或外部时间锚；能同时修改载荷与清单的人可以生成新的自洽归档。`agentsqlctl audit verify-archive` 因此只报告包内一致性，不认证来源、所有者、生成时间或最新性。
- 规则、审批和 fail-closed 处理用于降低已知风险，但不能保证识别所有 SQL 语义、业务逻辑滥用、数据库漏洞或未知攻击。
- Streamable HTTP 的远程部署需要由运维方配置 HTTPS、网络访问控制和凭据管理；AgentSQL 的本机 HTTP 示例不代表公网安全部署方案。

发现可突破上述预期边界、造成权限提升、认证绕过、敏感信息泄露或审计规避的问题时，请按本政策私密报告。

## PolarDB-X（MySQL 协议路径）安全边界

PolarDB-X 通过 `db_type=mysql` 接入，不新增 `polardbx` 数据源类型，也不以服务端产品名绕过 MySQL parser、规则或执行器。该路径表示有界的 MySQL 协议兼容，不表示完整 PolarDB-X 方言支持、厂商认证或生产可用性承诺。

- 仅复用 AgentSQL 已能严格解析并归类的 MySQL 单语句子集。PolarDB-X 的路由 Hint、DRDS/TDDL 管理语句、分布式 DDL、存储过程及其他未识别扩展不会因协议兼容而自动获准；解析或分类不确定时失败关闭。
- discovery 使用固定的 `information_schema.tables` / `information_schema.columns` 查询与反引号、`LIMIT` 采样语句，并限定在当前 `DATABASE()`。目录权限不足、结果超限、返回列形状变化或跨库范围均失败关闭。
- 普通 `EXPLAIN` 若且仅若返回精确的单列 `LOGICAL EXECUTIONPLAN`，执行器才追加官方定义的 `EXPLAIN EXECUTE`，并按 MySQL `type` / `key` / `rows` 表格归一化 DN 计划。第二阶段错误、未知列、空计划、非法估算或超大结果不会降级为零风险或跳过 R004/R005。
- SELECT 继续接受执行器行数上限、context deadline 和单语句限制。`MAX_EXECUTION_TIME` Hint 只是纵深限制，不能替代客户端取消；目标版本忽略 Hint 时仍由 context 截止时间关闭连接/查询。
- INSERT / UPDATE / DELETE 复用 MySQL DML 解析、R002/R003/R006/R201--R204、显式写事务与错误脱敏。分布式事务、GSI、分区键更新、广播表及跨分片语义必须在目标拓扑另行验证；协议可执行不等于原子性、隔离级别或提交结果已获认证。
- MySQL 路径不提供 PostgreSQL B2/B5 native binder 证明。表/列策略、结果脱敏和应用审计仍由 AgentSQL 执行，不能替代 PolarDB-X 原生账号权限、网络隔离、TLS、审计或备份。
- 生产环境必须使用专用最小权限账号和目标版本要求的 TLS/认证配置。官方体验镜像的 `polardbx_root/123456` 仅用于隔离本地测试，禁止作为生产凭据；Kubernetes Operator 的 root 密码由 Secret 随机生成，不应写入仓库或日志。

版本与部署结论必须分别记录 CN/DN/CDC 拓扑、PolarDB-X 版本、驱动版本和测试范围。旧的 `polardbx/polardb-x:2.0.1` 单容器只能作为开发冒烟环境，不代表当前社区版、Operator 集群或阿里云商业服务。

## SQL Server 2025 安全边界

`db_type=sqlserver` 是独立的 SQL Server 2025（主版本 17）受限只读能力，不代表完整 T-SQL、DML、DDL、存储过程、SQL Agent、CLR、链接服务器或 B2/B5 支持。

- 传输必须加密。默认 `tls_mode=strict`，对应 TDS 8.0 严格加密与证书校验；`verify-full` 对应 `encrypt=true` 的兼容路径。没有关闭 TLS 的配置。`trust_server_certificate=true` 会跳过服务端身份验证，只能在隔离开发环境中与 `verify-full` 显式组合，生产环境禁止使用。
- 数据库账号必须遵循最小权限：仅授予目标库/模式/表的 `SELECT`、受控元数据可见性及估算计划所需 `SHOWPLAN`。不要授予 `sysadmin`、`CONTROL SERVER`、`db_owner`、写权限、DDL、存储过程执行、impersonation、外部数据源或文件访问权限。
- 网关只接受一条窄只读 `SELECT`。写操作（包括有 `WHERE` 的写操作）、过程调用、批处理、动态数据源、`xp_cmdshell`、OLE 自动化、`BULK`、`WAITFOR`、`DBCC`、会话级 `SET/USE` 和未证明安全的语法均失败关闭。解析失败时不会把原 SQL 交给驱动尝试。
- discovery 使用固定、参数化的 `sys.tables/sys.schemas/sys.columns/sys.types` 查询。SQL Server 的 metadata visibility 会按数据库权限隐藏对象；权限不足、目录结果超限或结果形状异常都作为失败处理，不扩大权限或回退到调用者提供的 SQL。
- 动态扫描规则使用 `SHOWPLAN_XML` 的估算计划；目标 `SELECT` 不在该阶段执行。计划 XML 解析有大小、深度和节点限制，无法验证的计划拒绝继续。`SHOWPLAN` 本身可能泄露对象与计划信息，因此只应授予专用网关账号，不应向终端用户暴露原始计划。
- 审计中的 `db_type` 会记录为 `sqlserver`，但仍属于应用层审计，保留与防篡改边界与本政策其他数据库相同。驱动原始错误、DSN、密码、证书路径和业务对象名不得写入面向客户端的错误或普通审计详情。
- SQL Server 路径不使用 PostgreSQL B2/B5 native binder，不提供 PostgreSQL 的列级证明。结果脱敏也不是数据库原生 RLS、Dynamic Data Masking 或完整 DLP 的替代品。

部署配置与驱动行为应以 Microsoft 的 [Go 驱动加密与证书](https://learn.microsoft.com/en-us/sql/connect/golang/encryption-certificates?view=sql-server-ver17)、[Go 驱动安全最佳实践](https://learn.microsoft.com/en-us/sql/connect/golang/security-best-practices?view=sql-server-ver17)及 [`SET SHOWPLAN_XML`](https://learn.microsoft.com/en-us/sql/t-sql/statements/set-showplan-xml-transact-sql?view=sql-server-ver17) 文档为准。

## MFA / OIDC / LDAP 安全边界

v0.5 的人员身份认证不会绕过 AgentSQL RBAC。OIDC/LDAP 组只能映射到指定租户内的显式角色 ID；映射为空、角色不存在、目录结果不唯一、外部服务不可用或验证状态无法读取时一律 fail-closed。TOTP 密钥加密保存，恢复码与 refresh token 只保存摘要；OIDC 使用 Authorization Code + PKCE、一次性 state/nonce、discovery 与 RS256 JWKS 校验；LDAP 只允许证书校验通过的 LDAPS 或 StartTLS，并通过用户 bind 验证密码。完整配置和限制见 [v0.5 身份认证指南](docs/AUTHENTICATION.md)。
## 在线升级安全边界 / Online Upgrade Security Boundary

`agentsqlctl upgrade check` 只从显式 `--manifest-url` 或 `AGENTSQL_UPGRADE_MANIFEST_URL` 读取 HTTPS JSON 清单（`version`、`url`、`sha256`），并报告更新、同版或旧版。`upgrade apply --backup-dir <新目录> --dry-run` 读取清单和同源 HTTPS 下载内容，限制大小，计算并比对 SHA-256，并检查当前可执行文件、配置文件与备份目录路径；它不创建备份、不停止服务、不替换二进制、不运行迁移。重定向、HTTP、降级安装、无效版本或摘要不一致均拒绝。`--yes` 目前不能启用实际安装；非 dry-run 的 `apply` 一律拒绝。

HTTPS 与 SHA-256 只提供传输校验和清单一致性。若清单及其摘要同时被篡改，校验仍可能成功；此流程没有签名、独立信任锚、发布者认证或防回滚证明。运行人员必须通过可信的独立渠道核对发布来源，在维护窗口另行备份程序、配置、数据库和匹配的密钥，验证恢复方案，并通过独立审核的部署流程安装。控制台的版本页只展示当前运行版本及预检指令，不提供远程安装。

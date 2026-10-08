# AgentSQL v0.5.0 Release Notes / 发布说明

> 发布闸门 / Release gate: **2026-10-16 16:00 CST (UTC+08:00)**
>
> 状态 / Status: **待发布 / Pending release**
>
> 版本 / Version: **v0.5.0**

## 中文

### 范围与口径

v0.5.0 相对已发布的 v0.4.0 增加了国产数据库 dialect 边界、7 类 21 款 NoSQL / 向量连接级数据源、RBAC / 多租户 MVP、MCP 会话兼容修复、英文文档和联合案例记录，并将开源许可证调整为 Apache-2.0。对比起点是 `v0.4.0`；此前使用的 `v0.4.0..1d6682d` 是阶段性快照，不能覆盖其后的 NoSQL、方言和发布工程提交。发布日须以最终封板提交重核完整变更范围。

本文中的“协议路径实测通过”仅指定版本、指定镜像/拓扑和合成数据用例，**不等于厂商认证、完整方言兼容、商业版支持或生产可用性承诺**。

### 新特性与变更

- **DM / Oracle / YashanDB 独立 dialect 边界**：新增独立数据源类型与有界连接/元数据路径；DM8 已在 `COMPATIBLE_MODE=0` 的 Pack3 实例完成窄查询、分页和已知 EXPLAIN 形态实测，Oracle 完成严格只读子集查询和 `PLAN_TABLE` EXPLAIN 归一化实测。DM/Oracle 新增 v0.5 冻结、fail-closed 的受控 SELECT parser：提取物理表、引用列和直接/常量/通配符投影血缘，不改变 executor 放行语义；函数、括号、子查询、CTE、复杂表达式和未列出的结构仍拒绝。YashanDB Go 驱动 v1.4.4 与官方 C 客户端 23.4.7.100 计划随 Linux 发行物分发。崖山客户端再分发依据为用户于 2026-10-03 声明已取得厂商授权，本仓库未收到书面授权文件。批三十提交 `33f2d56` 已修复 pool/physical session 普通 Query 边界并在真实 23.4.1.109 实例复验 fail-closed；EXPLAIN 和写路径仍不宣称可用。批三十一发现旧 dry-run 包未包含新驱动/客户端，必须由封板提交重新构建。
- **方言离线边界收尾**：批六十三为 DM/Oracle 补充 `ROWNUM`、投影别名/序号、NULL 与空字符串语义、分页和管理语句拒绝回归；批六十六提供 `NewYashanParser()` 的离线受控 SELECT profile。批七十一已将崖山接入标准 `NewParser` 和窄 SELECT 查询路径；随后使用独立客户端完成指定真库 SELECT、列血缘、R010、手机号脱敏与测试夹具三条审计验证，仍不构成厂商语法认证或 HTTP/MCP、持久化审计验收。
- **批七十一状态更新**：DM8 Pack3、`COMPATIBLE_MODE=0` 的只读 SELECT、列血缘、R010 表策略、审计和手机号脱敏经批六十五真库验证，矩阵转绿；R006 未验证，多行分页/JOIN/函数/系统目录未测，写入未放开。崖山 C 客户端再分发已获厂家口头授权，仓库无书面授权文件。本轮未重建发行物；旧批次关于 DM8 未接网关及崖山通用 Query 一律拒绝的表述以本条及矩阵为准。
- **NoSQL / 向量注册与连接级能力**：注册表为 6 款关系型加 7 类 21 款 NoSQL / 向量，共 8 类 27 款；21 款仅提供适配器白名单内的连接、健康、元数据及只读预览能力。类型、默认端口和 HBase/Couchbase 实际连接端口例外逐项见 [NoSQL 支持范围](nosql-support.md)。
- **OpenTenBase 双内核深化**：OpenTenBase v2.5.0 PostgreSQL 内核对特定远程计划实现严格、版本受限的规范化，指定单机 GTM/CN/DN 拓扑的最小安全闭环有实测记录。TXSQL/MySQL 内核是独立路线，本版未实测、未实现产品专用代码。
- **PolarDB 兼容路径**：指定 PolarDB for PostgreSQL 15 社区镜像通过现有 `postgres` 路径完成 Ping、discovery、R006 拒绝、EXPLAIN、只读查询、脱敏与审计闭环；未新增 PolarDB 别名或厂商识别开关。
- **PolarDB-X MySQL 协议路径**：保持 `db_type=mysql`，复用 MySQL parser、执行器、DML/事务和 discovery；普通计划精确匹配 `LOGICAL EXECUTIONPLAN` 后使用 `EXPLAIN EXECUTE` 获取 DN 的 MySQL 表格计划，任一未知形状继续 fail-closed。新增兼容 corpus 与显式环境门控的真库 E2E；不把它与 PolarDB for PostgreSQL 混称，也不把旧单容器冒烟扩大为当前集群或商业服务认证。
- **EXPLAIN 兼容适配**：增加 OpenTenBase、TiDB、OceanBase 和 PolarDB-X 的严格计划适配与回归 fixture；未知版本、列、节点或计划形态继续 fail-closed。代码 fixture 不借此宣称厂商环境完整闭环已通过。
- **RBAC / SSO / 多租户 MVP（#33）**：新增本地用户、租户、角色、权限、多角色和角色继承，并对管理 API 执行逐路由授权。MVP 完全开放、无许可门控；本版进一步加入 TOTP MFA、OIDC Authorization Code + PKCE 和 LDAP/AD TLS bind，存量业务元数据的全租户化已由批三十六补齐。
- **业务元数据全租户化（批三十六）**：Agent、数据源、策略及列权限、规则、脱敏规则与密钥、通知、审批、审计、管理审计 outbox、审计链及 B5 持久业务实体均增加租户所有权；无法可靠识别归属的历史行一律回填 `tenant_default`。控制台列表按令牌 `tid` 隔离，跨租户详情、更新和删除统一表现为 404，管理审计事件携带租户上下文。`schema_migrations`、RBAC 自身表、管理令牌/会话、MCP stream event、`control_plane_compat`、`runtime_instances` 等平台运行表未被误作业务元数据 tenant 化。Agent API Key 协议未改变；MCP 执行数据路径尚未把已认证 Agent 的租户显式传播到全部数据源/策略仓储调用，当前仍落在默认租户兼容边界，不能据此宣称 MCP 数据访问已完成跨租户隔离。
- **MCP 会话与断线重放**：Streamable HTTP 首次 `initialize` 可以不携带 `MCP-Protocol-Version`，后续请求继续严格校验协议版本、会话 ID 和 Agent/Key 绑定。stateful 旧协议默认使用 SQLite/PostgreSQL 持久化 EventStore，支持 `GET /mcp` + `Last-Event-ID` 连续重放；TTL/容量缺口 fail-closed。go-sdk session 仍不跨进程持久化，重启后旧 ID 以 `Mcp-Session-Expired: 1` 明确要求重新 initialize。
- **企业审计报表与合规导出**：增加 `agentsqlctl audit` 查询/报表能力及配套文档；这不改变“应用层只追加审计不等于法规级 WORM”的信任边界。
- **PDF 与可校验归档**：CLI、管理 API 和控制台新增纯 Go 多页 CJK PDF，以及固定包含 CSV、JSONL、PDF、摘要和 `manifest.json` 的 ZIP。离线校验严格拒绝目录穿越、重复/额外成员、大小或 SHA-256 不一致；清单未签名，只证明包内一致性，不认证来源、时间或最新性，也不替代 WORM/SIEM。
- **快速上手、Demo 与错误口径**：新增下载单个 Compose 文件即可启动的自包含五分钟演示栈；演示场景卡对齐真实剧本和审批结果，修复演示表外键/密封 JOIN 冲突与每日 reset 的 seed/gateway 时序，并区分可预期授权失败、对象缺失与数据库执行错误。
- **生态与官网配套**：新增 MCP Registry 描述、官网“生态与兼容”板块、社区入口与国产数据库分批研究/联合验证资料；主站搜索闸门已在仓库中调整为放行主站、保持 `/demo/` 禁止索引，仍需在发布部署后做外网复核。
- **英文文档**：增加英文首页、快速上手、管理、MCP 接入、安全和覆盖度文档。
- **已知问题修复与记录**：修正 R005 前端动态规则元数据；加固 bootstrap / MCP 测试诊断。批十七提交 `02565f5` 已闭环 R005：普通生产动态阶段按 EXPLAIN 评估，无界查询估算结果超过数据源 `row_limit` 时返回结构化 `R005` 风险命中；通用执行和 PostgreSQL 列级授权路径在实际截断时补充同一命中，并写入持久审计。
- **联合案例配套**：新增金仓 + 瀚高验证记录、运营口径和瀚高演示 SQL。瀚高指定第三方 SEE 镜像有协议路径实测记录；金仓 V9 仍待厂商环境。
- **发布与测试收尾**：`scripts/release-dryrun.ps1` 已定义 15 项资产的一键 dry-run、生产准备和 `-ValidateOnly` 核对路径，正式签名与发布仍待发布日。批六十七新增 parser malformed/并发错误路径及 pipeline 执行前拒绝测试；本次文档核对没有运行测试，覆盖边界与离线依赖限制见 [测试覆盖说明](test-coverage-notes.md)。

### Dialect 与生态适配矩阵

| 数据库 / 路径 | v0.5.0 状态 | 边界 |
| --- | --- | --- |
| PostgreSQL 14–18 | 原生支持 | 既有 PG parser / executor / discovery；B2/B5 仅在 PostgreSQL 路径提供 |
| MySQL 8 | 原生支持 | 表级授权与单请求执行；不声称 B2 列级授权 |
| DM8 | 🟢 指定实例防护链路实测通过 | Pack3、`COMPATIBLE_MODE=0`：只读 SELECT、列血缘、R010 表策略、审计、手机号脱敏链路通过；R006 未验证，多行分页/JOIN/函数/系统目录未测，写入未放开；不代表完整 grammar 或厂商认证 |
| Oracle Free 23.26.3 | 独立 dialect 有界实测 | 严格 SELECT 子集、元数据和 EXPLAIN 归一化有记录；v0.5 受控 SELECT 表/列/投影血缘已实现并对未知结构 fail-closed；完整 grammar、写入、列授权/脱敏 pipeline 未实现 |
| YashanDB 23.4.1.109 | 独立 dialect 受控只读接线 | 标准 parser、通用规则和窄 SELECT 路径已接通；独立客户端真库流水线测试通过 SELECT、列血缘、R010、手机号脱敏及三条测试审计；HTTP/MCP、持久化审计和最小权限账号与流水线组合未测；EXPLAIN / 写路径仍不宣称可用 |
| OpenTenBase v2.5.0（PG 内核） | 指定环境协议路径闭环通过 | 仅指定单机 GTM/CN/DN 和已知计划形态；非厂商认证 |
| TXSQL/MySQL 内核 | 待官方环境实测 | 未实现产品专用代码；不用普通 MySQL 结果替代 |
| PolarDB for PostgreSQL 15 | 指定社区镜像协议路径闭环通过 | 复用 `postgres`；商业服务拓扑、TLS、故障切换待联合终验 |
| PolarDB-X（MySQL 协议） | 官方 `2.0.1` 旧单容器有界 E2E 通过 | 复用 `mysql`；两阶段 EXPLAIN 严格适配；完整 CN/DN/CDC、TLS、分布式事务/GSI 与商业服务待终验 |
| openGauss 7.0.0-RC3 / IvorySQL 5.3 | 指定社区版协议路径闭环通过 | 复用 PG 路径；不替代 GaussDB / HighGo 商业版终验 |
| HighGo SEE 4.5 | 指定第三方镜像协议路径闭环通过 | 非官方镜像、非厂商认证；与下述 9.0.10 企业版验证分别成立 |
| HighGo 9.0.10 企业版 | 🟢 指定实例防护链路实测通过 | PG 模式、单实例合成表：解析/列血缘、表级授权、受控只读 SELECT、手机号脱敏、R006 拒绝及 allow/deny 审计实测通过；TLS、连接池故障切换、取消/超时、EXPLAIN、扩展类型/OID、系统目录差异、B2 列级授权、parse-error 审计分支（本轮未触发）、跨版本与生产负载未测；非厂商认证或生产支持承诺。见[真库验证](highgo-verification.md) |
| KingbaseES V9 | 待厂商环境 | 第三方旧镜像被过期 license 阻断；不声称连接/发现/查询通过 |
| TiDB 7.5.1 / OceanBase CE 4.4.2.1 | EXPLAIN 适配与回归已落地 | 本批不把 fixture/代码回归扩大为厂商环境完整认证 |

### 许可变更

**许可更换声明：**v0.4.0 及更早版本的既有源码、tag 与发布包维持其发布时适用的原许可并可继续按原许可使用；不重打历史 tag、不重发历史资产。自 v0.5.0 起，开源版切换为 [Apache License 2.0](../LICENSE)，并提供独立的 [商业许可说明](../COMMERCIAL-LICENSE.md)。Apache-2.0 的权利与义务以 `LICENSE` 和 `NOTICE` 为准；商标许可、企业能力、SLA 与支持条款以双方书面协议为准。该变更由提交 `391dcf0` 引入，不构成任何数据库厂商的授权、认证或背书。

### 已知边界与不承诺事项

- DM/Oracle parser 只覆盖文档列出的 v0.5 受控 SELECT profile；`*` 仅绑定到物理来源表，parser 无 catalog，不能虚构逐列名称。该能力不等于完整 SQL grammar 或已接入列级授权/脱敏 pipeline。
- YashanDB 的 `NewYashanParser()` 已接标准 `NewParser` 路由；指定真库与独立客户端的窄 SELECT、列血缘、R010、手机号脱敏及测试夹具三条审计已通过，HTTP/MCP、持久化审计及最小权限账号与流水线组合未测。注册表的 NoSQL 21 款均处于连接级，HBase 注册默认 `16020` 不是当前适配器可用的 ZooKeeper bootstrap 端口。
- KingbaseES V9R1C10 目标环境待厂商提供；GaussDB、TDSQL 等商业版仍待目标环境终验。HighGo 9.0.10 企业版仅在上表限定范围内转绿，未测边界继续失败关闭。
- R005 生产动态告警已闭环，但能力边界仍须准确表述：无界查询的执行前命中依赖受控 EXPLAIN；实际 `row_limit` 截断会在通用执行和 PostgreSQL 列级授权路径补充 `R005`。响应通过 `assessment.hits` 返回结构化命中，持久审计通过 `rule_hits` 保存；未知 EXPLAIN 或审计事实继续 fail-closed。`row_limit` 可在数据源管理入口配置，默认值为 1000；规则页不提供独立 R005 阈值编辑器。
- RBAC 仍是 MVP：控制台业务元数据已经按租户隔离，TOTP MFA、OIDC 与 LDAP/AD 已交付，但登录限速与锁定、外部身份系统的生产兼容矩阵仍是后续边界；MCP 执行数据路径的 Agent 租户传播也尚未完成。管理端本地与外部用户令牌复用持久化服务端撤销和 refresh token 轮换：refresh token 仅以 SHA-256 哈希存储、每次刷新都会轮换，重用已轮换/撤销的 token 会撤销整条 refresh family；状态不可读时 access 校验、刷新与登出均 fail-closed。
- MCP transport session 不跨进程保存，服务重启前的旧 session 不能无缝恢复；已提供持久化 SSE 断线重放和重启后 `Mcp-Session-Expired: 1` → 重新 initialize 路径。豆包、Claude Desktop / Inspector 真实客户端仍待账号和指定版本联调。
- “协议路径实测”不覆盖完整 SQL 方言、生产拓扑、HA/故障切换、TLS/认证矩阵、性能 SLA 或厂商支持责任。

### 版本 bump 文件清单与保留项

下列 42 个文件只调整版本默认值、镜像/tag/包名、客户端自报版本或对应文档示例，不改运行时功能：

- 入口与构建：`.github/ISSUE_TEMPLATE/bug_report.yml`、`CONTRIBUTING.md`、`Dockerfile`、`Makefile`、`README.md`、`README.en.md`、`internal/version/version.go`、`server.json`、`web/package.json`、`web/package-lock.json`。
- 发布脚本：`scripts/build-ghcr-multiarch.ps1`、`scripts/build-release-linux.sh`、`scripts/package-release.sh`。
- Compose 与示例：`docker-compose.yml`、`docker-compose.controlplane.yml`、`docker-compose.demo.yml`、`examples/docker/.env.example`、`examples/docker/demo.env.example`、`demo/reset.ps1`。
- 数据库扩展打包：`dbext/packaging/Dockerfile.packages`、`dbext/packaging/README.md`、`dbext/packaging/build-images.ps1`、`dbext/packaging/build-packages.ps1`、`dbext/packaging/images/Dockerfile`。
- 部署示例：`deploy/ecs-demo/README.md`、`deploy/ecs-demo/docker-compose.yml`、`deploy/quickstart/docker-compose.yml`、`deploy/quickstart/docker-compose.debug.yml`、`deploy/quickstart/gateway/Dockerfile`、`deploy/quickstart/postgres/Dockerfile`、`deploy/quickstart/qs-companion-verify.ps1`、`deploy/quickstart/qs-error-probe.ps1`、`deploy/quickstart/qs-inspect-notes.ps1`。
- 当前版本文档：`docs/DEPLOY.md`、`docs/GETTING_STARTED.md`、`docs/INTEGRATIONS.md`、`docs/SPEC.md`、`docs/USER_GUIDE.md`、`docs/en/GETTING_STARTED.md`、`docs/en/INTEGRATIONS.md`、`docs/en/QUICKSTART.md`、`docs/en/README.md`。

另同步修正 `docs/en/ADMIN.md`、`docs/en/COVERAGE.md`、`docs/en/SECURITY.md` 的“待发布 v0.5.0 / 已发布 v0.4.0”状态表述。历史版本事实、v0.4 行为契约与性能记录保留原文。`website/` 当前仍展示 v0.4.0 GA 并链接三份现存 v0.4.0 PDF；必须等 v0.5.0 Release 与新 PDF 实际就绪后在发布日切换，避免本批制造失效链接。

### v0.5.0 发布资产清单

v0.4.0 的 GitHub Release 已核实包含下列 15 个资产。v0.5.0 沿用同一命名与签名结构；截至本草稿，v0.5.0 Release 尚不存在，因此下表不填写未生成的 SHA-256。

| # | GitHub Release 资产 | 用途 | 状态 |
| ---: | --- | --- | --- |
| 1 | `agentsql-v0.5.0-linux-amd64.tar.gz` | amd64 glibc 2.28+ 原生包 | 发布日重建；旧 dry-run 包缺 Yashan runtime，不可用 |
| 2 | `agentsql-v0.5.0-linux-amd64.tar.gz.sha256` | amd64 外层 SHA-256 | 随 #1 重建 |
| 3 | `agentsql-v0.5.0-linux-arm64.tar.gz` | arm64 glibc 2.28+ 原生包 | 发布日重建；旧 dry-run 包缺 Yashan runtime，不可用 |
| 4 | `agentsql-v0.5.0-linux-arm64.tar.gz.sha256` | arm64 外层 SHA-256 | 随 #3 重建 |
| 5 | `agentsql-v0.5.0.spdx.json` | SPDX SBOM | 发布日从 sealed 双架构二进制生成 |
| 6 | `agentsql-v0.5.0.spdx.json.sig.json` | SBOM Ed25519 签名 | 待签名 |
| 7 | `ed25519-release-public-key.json` | 发布公钥 | 待复核/导出 |
| 8 | `go-version-metadata.txt` | Go 工具链与模块元数据 | 发布日重建；旧文件缺 `yashandb-go v1.4.4` |
| 9 | `install.sh` | 架构感知安装器 | 待从封板提交导出 |
| 10 | `provenance.json` | 产物来源记录 | `-ProductionPrepare` 命令已补；待发布日生成 |
| 11 | `provenance.json.sig.json` | provenance Ed25519 签名 | 待签名 |
| 12 | `SBOM-GENERATION.txt` | SBOM 生成说明 | `-ProductionPrepare` 命令已补；待发布日生成 |
| 13 | `SHA256SUMS` | 除自身及其签名外 13 项的统一校验和 | 待发布日生成 |
| 14 | `SHA256SUMS.sig.json` | 校验和 Ed25519 签名 | 待签名 |
| 15 | `VERIFYING-SIGNATURES.md` | 签名验证说明 | `-ProductionPrepare` 命令已补；待发布日生成 |

| 其他分发项 | 状态 | 发布日验收 |
| --- | --- | --- |
| `ghcr.io/cuipengdba/agentsql:v0.5.0` | 旧 OCI 有双架构 descriptor，但缺新 Yashan 内容；当前 HEAD 待发布日重建 | 发布日须匿名双架构 pull 并确认 `/healthz` 回报 `v0.5.0` |
| `ghcr.io/cuipengdba/agentsql:latest` | 待发布日移动 | 仅在精确 tag 验收后指向同一 digest |
| `v0.5.0-demo` / `v0.5.0-quickstart` 辅助镜像 | 旧归档证实 quickstart 只是主镜像换 tag；构建图已修复，当前 HEAD 待重建 | Buildx Bake 的 quickstart target 从同图 demo base 叠加两份配置；发布日需核对文件存在、双架构 descriptor、Compose seed/gateway/reset |
| `docs/release-notes-v0.5.md` | 已完成（本草稿） | 发布日复核日期、状态和 SHA-256 |
| `CHANGELOG.md` | 已更新为待发布条目 | 发布后才可改成“已发布” |
| 官网三份 v0.5.0 PDF | 待重导 | 按 `website/tools/build-docs-pdf.mjs` 重导并通过 `verify-static.sh`；本批不制造空链接 |

### 相对 v0.4.0 的发布流程

| 类型 | v0.5.0 步骤 |
| --- | --- |
| 复用 | 仓库公开与 PVR 复核；固定 Rocky Linux 8 digest 构建 amd64/arm64；恰好 15 个 Release 资产、SHA-256、SBOM/provenance 与 Ed25519 签名；先建 Release 草稿并回下载验证；GHCR 精确 tag → public → 匿名双架构 pull → 再移动 `latest`；Demo reset；官网 PDF/静态验收/部署；Release 草稿转正式；发布后无登录烟测 |
| v0.5 新增闸门 | 核对 Apache-2.0 / `NOTICE` / 商业许可口径；逐项复核 27 款注册与 dialect 矩阵，保留“协议路径实测 ≠ 厂商认证”；构建并演练 demo/quickstart 辅助镜像；复核 R005 闭环证据，DM/Oracle 完整网关与金仓/商业版终验不得误标完成；标准 runner 独占重跑 parser P99 连续五轮；由维护者确认 `SECURITY.md` 支持版本矩阵 |

### 发布检查清单

| 阶段 | 检查项 | 状态 | 证据 / 发布日动作 |
| --- | --- | --- | --- |
| 准备 | 仓库公开 | **已完成** | GitHub REST 只读查询返回 `private=false` / `visibility=public` |
| 准备 | v0.5.0 Release Notes、资产表、检查表 | **已完成** | 本文；状态仍为待发布 |
| 准备 | 版本常量与 CHANGELOG | **已完成** | 源码/构建/示例默认版本对齐 `v0.5.0`；历史口径保留 |
| 准备 | 完整变更基线复核 | **待发布日重核** | `v0.4.0..1d6682d` 仅是阶段性快照；以最终封板提交重核 NoSQL、方言及发布工程后续变化 |
| 安全 | 支持版本矩阵 | **已完成** | v0.5.x 支持至 v0.6.0 发布后 6 个月且不早于 2027-10-31；v0.4.x 支持至 2027-04-30；0.3.x 及更早 EOL |
| 安全 | Private Vulnerability Reporting（PVR） | **待发布日** | 匿名 API 不返回该设置；由仓库管理员在 Security settings 复核为开启 |
| 验证 | Go 构建、short tests、gofmt、`git diff --check` | **待发布日** | 批三十三 P99 两组均仅 3/5；批六十一在指定 Go 1.26.8 Linux/amd64 环境两组各 5/5 通过，复核组为 1.917/2.075/1.994/2.011/1.911 ms（见 `PARSER_PERF.md`）。全仓 short 仍未完成；标准 Linux runner 须独占重跑连续五轮及全仓测试 |
| 构建 | 固定 Rocky Linux 8 digest + Go 1.26.8 构建 amd64/arm64 | **待发布日** | `go.mod` 为 1.26；脚本设 `GOTOOLCHAIN=local` 防止隐式换工具链；两个二进制均须回报 `v0.5.0`，GLIBC 符号不高于 2.28 |
| 构建 | 崖山驱动与 C 客户端双架构打包 | **脚本已落地；当前产物阻塞** | `yashandb-go@v1.4.4` 与客户端 23.4.7.100 的固定版本/SHA 已在脚本；批三十一验证旧 tar/metadata 不含它们，必须由 sealed commit 双架构重建 |
| 供应链 | SBOM、provenance、Go metadata、生产 Ed25519 签名 | **dry-run/生产准备命令已补；实跑待发布日** | dry-run 生成不可发布占位；`-ProductionPrepare` 从干净工作树和正式公钥生成 11 个待签名文件；生产签名仍待发布日 |
| 校验 | SHA-256 与签名双重验证 | **校验逻辑已补；旧目录 15 项 FAIL，新候选待验证** | 旧目录真实在 Yashan metadata 闸门失败；发布日最终 15 项须通过三次密码学验签、sidecar/manifest 和 provenance commit 校验 |
| 草稿 | 建立 GitHub Release draft 并上传恰好 15 资产 | **待发布日** | 本批禁止创建；草稿中先验证名称、大小、SHA-256 和下载 |
| 镜像 | 推送 `v0.5.0`，设 GHCR public，匿名双架构 pull | **顺序缺口已修；构建/发布闸门待发布日** | `-Push` 不再移动 latest；精确 tag 验收后单独 `-PromoteLatest` 并比较 digest |
| Demo | 生成辅助镜像并执行 reset | **quickstart 构建缺口已修；实跑待发布日** | `-IncludeAuxiliaryTags` 用独立 quickstart target 加入 demo 配置；旧同 digest 归档不可作为通过证据 |
| 官网 | 重导 PDF、运行静态验收、部署 | **待发布日** | 先验 Release/镜像/Demo，再部署；复核 HTTPS、`www` 301、sitemap 和 security.txt |
| 搜索 | 放行主站、保持 `/demo/` noindex | **待发布日** | 主页 `index,follow`；`robots.txt` 为 `Allow: /` + `Disallow: /demo/`；demo meta 仍 `noindex,nofollow` |
| 发布 | Release draft → 正式 | **待发布日** | 只在上述闸门全绿后发布；本批禁止执行 |
| 发布后 | `releases/latest`、安装器、匿名镜像、官网、Demo 回归 | **待发布日** | 从无登录干净环境走完下载→校验→安装→健康/就绪→允许/拒绝/审批 |

### 发布链待办

1. **批三十一真实 dry-run 结论为阻塞。** 旧 15 项在 Yashan metadata 闸门 fail-closed，两个 tar 也缺两份客户端库；最终封板提交的完整重建仍待发布日依赖与外网。详细命令与输出见 [`release-day-checklist-v0.5.md`](release-day-checklist-v0.5.md)。
2. **辅助镜像缺口已修但未冒充实跑通过。** 旧三个 OCI 的平台 manifest digest 相同，证实旧 quickstart 只是换 tag；新 `quickstart` target 会加入 `config.demo.yaml` / `demo-seed.yaml`。`-Push` 与 `-PromoteLatest` 已拆开，避免精确 tag 验收前移动 latest。
3. **生产候选命令已补。** `-ProductionPrepare -PublicKeyPath` 要求完全干净工作树并输出 11 个非 dry-run 待签名文件；发布日补齐三份签名和 `SHA256SUMS` 后，才可用 `-ValidateOnly` 核对 15 项并逐份密码学验签。
4. PVR 设置、GHCR public、官网实时状态和搜索放行需管理员/外网证据；当前无法从匿名 API 完整取证的项均保持“待发布日”。
5. **支持矩阵已按 2026-10-03 用户定案更新。** v0.5.x 与 v0.4.x 的截止口径已同步到中英文安全文档，0.3.x 及更早标记为 EOL。

### PostgreSQL parser P99 调查（批十九）

- 口径：`TestParseProjectionLineageP99Budget/postgres` 测量完整 `Parser.Parse`，典型 SQL 为 8 个 `UNION ALL` 分支、每分支 8 列，每轮 2,000 样本；包含 `pg_query`、JSON AST 解码、Normalize 前扫描和 lineage 后处理，不包含闭包 DML 执行链路。
- 环境：`golang:1.25-bookworm`、Linux/amd64、Intel i7-6700、8 个逻辑 CPU。未优化独占 3 轮 P99 为 7.329/8.938/6.518 ms；补采的一轮 P50/P90/P99 为 2.554/4.138/5.671 ms。
- 根因：典型 SQL 无字面量，实际不会执行 `pg_query.Normalize`；优化前却每次执行拆句、ParseToJSON、Normalize 前扫描和注释扫描共 4 次 `pg_query` 前端调用。CPU profile 中两次 Scan 合计约 20.1%，两段 JSON 解码约 30.2%，lineage 约 10.3%，CGO 平坦耗时约 18.5%，没有独立类型推断阶段。
- 最小优化：无分号单语句跳过冗余拆句扫描；Normalize 与注释检测复用一次 token scan；JSON AST 从两段解码合并为一次并拒绝 NULL 根；表、CTE、列、函数收集合并为一次只读遍历。现有 `set_arms_8`（8 分支、每分支 1 列）benchmark 从 720,686–756,478 ns/op、110,278–110,279 B/op、1,255 allocs/op 改善到 495,068–544,081 ns/op、86,421 B/op、1,139 allocs/op。
- 结论：最终 5 轮 P50 为 1.702–1.861 ms、P90 为 3.146–3.289 ms，但 P99 为 5.576/4.906/4.744/5.485/5.643 ms，仅 2/5 通过。主体延迟约降低 27%–33%，尾部仍受 C parser/scan、JSON 大量分配及 GC/调度共同影响；保留 5 ms 发布日闸门，不放宽、不标记达标。
- 全仓并行 `go test -short ./... -count=1` 的功能包均通过，但性能门因 CPU 竞争失败（MySQL/PostgreSQL P99 为 19.518/12.546 ms），因此命令总体为 FAIL；发布判定仍只采用无并行负载的独占复跑，同时不能把这次全仓结果记为通过。

### R005 收尾与 PostgreSQL parser P99 复核（批三十三）

R005 的批十四调查文档和本发布说明没有跟进批十七提交 `02565f5`，属于披露滞后，不是运行时代码仍缺失。当前核对结果如下：

| 能力点 | 结论 | 代码 / 测试证据 |
| --- | --- | --- |
| 无界大结果执行前告警 | 已实现；生产动态规则包含 R005，受控 EXPLAIN 的估算结果严格大于有效 `row_limit` 时返回 `warn` | `internal/pipeline/rules.go` 的动态规则分类；`internal/rules/generic.go` 的 R005；`TestPipelineR005ProductionDynamicAndRuntimeSignals/dynamic plan exceeds datasource row limit` |
| 实际截断补告警 | 已实现；即使 SQL 自带更大 `LIMIT`，只要执行层报告 `Truncated=true`，通用执行与 PostgreSQL 列级授权路径均补充 R005 | `internal/pipeline/pipeline.go`、`internal/pipeline/column_select.go`；`TestR005RuntimeTruncationOverridesSQLLimit`、`TestPostgresColumnAuthorizationAuditAddsR005WithoutStaticExplain` |
| 结构化响应 | 已实现；`Response.Assessment.Hits` 返回含消息和建议的 `RuleHit{RuleID:"R005", Decision:"warn"}`，结果同时保留 `Truncated` | `internal/pipeline/types.go`、`internal/model/model.go`；上述 pipeline 测试。没有另设专用 HTTP 响应头或专用日志事件，不能超出 `assessment.hits` 口径宣称 |
| 持久审计与管理查询 | 已实现；同一 assessment 的完整 hits JSON 写入 `audit_logs.rule_hits`，`GET /api/v1/audit`、实时流和导出投影均返回 `rule_hits` | `internal/pipeline/auditmap.go`、`internal/adminapi/dto.go`、`internal/adminapi/handler.go`；pipeline 审计一致性测试 |
| 阈值入口与边界 | 数据源 `row_limit` 是默认告警/执行阈值，缺省为 1000，并由数据源管理 API 创建/更新；内部三层规则配置可覆盖 `row_limit` 阈值，但当前 Rules 页面/API 不提供独立阈值字段 | `internal/pipeline/types.go`、`internal/rules/generic.go`、`internal/adminapi/dto.go`、`internal/adminapi/handler.go`、`internal/pipeline/rule_overrides.go` |
| 不触发与 fail-closed | 阈值相等、小结果、显式受控 LIMIT、无分组纯聚合不因估算误报；实际截断仍优先告警。EXPLAIN、规则或审计事实不可取得时不降级放行 | `internal/rules/generic_test.go`、`internal/pipeline/pipeline_test.go` |

PostgreSQL parser 复核环境为 Docker 29.8.0、`golang:1.26-bookworm`（镜像 ID `a688600ca24f`，Go 1.26.8）、Linux/amd64 WSL2 kernel 6.18.40.1、Intel i7-6700、8 个逻辑 CPU、`GOMAXPROCS=8`。工作区只读挂载；命令为：

```text
docker run --rm --platform linux/amd64 -e GOMAXPROCS=8 -e GOTOOLCHAIN=local \
  -v "${PWD}:/src:ro" -w /src golang:1.26-bookworm \
  /usr/local/go/bin/go test ./internal/parser -run '^TestParseProjectionLineageP99Budget$/^postgres$' -count=5 -v
```

- 优化前 5 轮原始 typical P50/P90/P99（ms）：`1.456/2.794/5.780`、`1.340/2.565/4.092`、`1.345/2.579/3.709`、`1.327/2.603/4.518`、`1.412/2.748/5.827`；3/5 通过，命令退出 1。
- CPU/alloc profile 显示 JSON 通用树解码占 alloc_space 52.66%，`newLineageScope` 占 15.11%。本批只在 PostgreSQL lineage 内按需创建 scope 索引、CTE-only scope 和 JOIN 状态，不切换 protobuf AST、不改规则或血缘语义。
- 相同 `set_arms_8` benchmark：优化前 `451841 ns/op, 76605 B/op, 1042 allocs/op`；优化后三轮 `405681–411521 ns/op, 65873–65874 B/op, 833 allocs/op`。
- 优化后 5 轮原始 typical P50/P90/P99（ms）：`1.348/2.604/4.456`、`1.413/2.679/4.775`、`1.545/2.990/6.404`、`1.477/2.800/6.554`、`1.294/2.541/4.173`；仍仅 3/5 通过，命令退出 1。
- 功能与构建：`go test -short ./internal/parser ./internal/rules ./internal/pipeline -skip '^TestParseProjectionLineageP99Budget$' -count=1 -p 1` PASS；`go build ./...` PASS；修改文件 gofmt 与 `git diff --check` PASS。一次全仓 short 尝试未形成 PASS：`internal/adminapi` 在 Windows Docker 的 SQLite WAL `fsync` 关闭阶段达到 10 分钟超时，且只读源码挂载使 `internal/authorizedexecute` 的临时编译目录创建失败；这两项按环境/挂载限制如实保留，不能计为代码通过或本批语义回归。

因此**批三十三当时** P99 发布闸门为 **FAIL**。批六十一在指定 Go 1.26.8 Linux/amd64 环境的初测与复核两组各 5/5 通过，复核 P99 为 1.917/2.075/1.994/2.011/1.911 ms；详见 [性能复核](PARSER_PERF.md)。这不改写批三十三的原始失败，也不能代替发布日标准 Linux runner、无并行负载下的连续 5 轮 P99 ≤ 5 ms 验收；任一轮失败即停止发布。

发布前只读加固核验（批二十三）已记录于 [`security-scan-v0.5.md`](security-scan-v0.5.md)：前端生产依赖与 demo 功能链路通过，Go 源码可调用漏洞为 0 但有 4 个不可达模块级命中；F-04 的 208 处历史赋值类命中已由用户于 2026-10-03 复核为测试数据、示例或非敏感赋值并按不处置关闭。批六十一的指定环境 P99 复核通过后，全仓 short tests 与发布日标准 runner 复测仍待完成，整体不能标记为全绿。

## English

### Scope and evidence language

AgentSQL v0.5.0 adds bounded database-dialect work, connection-level support for 21 NoSQL/vector sources, an RBAC/multi-tenant MVP, MCP session compatibility fixes, English documentation, joint-case material, and an Apache-2.0 licensing change. The comparison starts at the released v0.4.0. The earlier `v0.4.0..1d6682d` range was an interim snapshot; release day must reconcile all later changes against the final sealed commit.

"Protocol-path tested" means only the named version, image/topology, and synthetic test cases. It is **not vendor certification, complete SQL-dialect compatibility, commercial-edition support, or a production-readiness commitment**.

### Highlights

- Independent dialect boundaries for DM, Oracle, and YashanDB. DM8 has real-instance evidence for the narrow query/pagination path and known EXPLAIN shapes on the documented `COMPATIBLE_MODE=0` Pack3 baseline; Oracle has evidence for the strict read-only subset and normalized `PLAN_TABLE` EXPLAIN. DM/Oracle now have a frozen v0.5, fail-closed controlled-SELECT parser that extracts physical tables, referenced columns, and direct/constant/wildcard projection lineage without changing executor admission behavior. Functions, parentheses, subqueries, CTEs, complex expressions, and unlisted structures remain rejected. Commit `33f2d56` fixed and revalidated the YashanDB pool/physical-session general-Query fail-closed boundary against a real 23.4.1.109 instance; EXPLAIN and writes remain unavailable. The planned Linux artifacts include `yashandb-go@v1.4.4` and the official 23.4.7.100 C client under the user's 2026-10-03 declaration of vendor redistribution authorization, but batch 31 proved that the stale local dry-run artifacts do not contain them and must be rebuilt from the sealed commit.
- The registry has 8 categories and 27 types: 6 relational plus 7 groups of 3 NoSQL/vector sources. The 21 native sources have bounded connection, health, schema, and read-only preview operations; their registered default ports and the HBase/Couchbase port exceptions are listed in [NoSQL support](nosql-support.md). Batch 71 wired `NewYashanParser()` through standard `NewParser` and the narrow SELECT path. A cached standalone C client passed real-server SELECT, column lineage, R010, phone masking, and three audits captured by a test fixture. HTTP/MCP and durable audit storage remain untested.
- Batch 71 moved DM8 to green for the tested Pack3, `COMPATIBLE_MODE=0` read-only SELECT, column lineage, R010 table policy, audit, and phone masking chain. R006, multirow pagination, JOIN, functions, and system catalogs remain untested; writes remain closed. Vendor oral authorization has been obtained for YashanDB C client redistribution, with no written authorization file in this repository. No release artifacts were rebuilt in this batch; these current statuses supersede earlier historical boundary descriptions.
- Batch 73 moved HighGo 9.0.10 Enterprise Edition to 🟢 for its live-tested PG-mode, single-instance synthetic table: parsing/column lineage, table-level authorization, controlled read-only SELECT, phone-number masking, R006 denial, and the allow/deny audit chain passed. TLS, pool failover, cancellation/timeouts, EXPLAIN, extended types/OIDs, system-catalog differences, B2 column authorization, the parse-error audit branch (not triggered), cross-version behavior, and production workloads remain untested. This is neither vendor certification nor a production support commitment. See the [verification record](highgo-verification.md).
- The release dry-run script defines and checks the 15 named assets in one invocation, with separate production preparation and validation paths. Batch 67 added parser malformed/concurrent failure tests and pipeline pre-execution rejection tests; [coverage notes](test-coverage-notes.md) state their limits and the offline dependency constraint. This documentation pass did not run Go tests.
- Strict, version-gated OpenTenBase v2.5.0 PostgreSQL-plan normalization and a scoped single-node GTM/CN/DN safety-loop record. TXSQL/MySQL is a separate, untested path with no product-specific implementation in this release.
- A scoped PolarDB for PostgreSQL 15 community-image exercise through the existing `postgres` path, with no PolarDB alias or vendor-identification switch.
- A bounded PolarDB-X path through `db_type=mysql`, with shape-gated `LOGICAL EXECUTIONPLAN` to `EXPLAIN EXECUTE` normalization, compatibility corpus, and opt-in E2E coverage. It is distinct from PolarDB for PostgreSQL and is not a full topology or commercial-service certification.
- Fail-closed EXPLAIN adapters and regression fixtures for OpenTenBase, TiDB, OceanBase, and PolarDB-X. Fixture coverage is not represented as full vendor-environment certification.
- An openly available RBAC/multi-tenant MVP: local users, tenants, roles, permissions, multiple roles, inheritance, and per-route authorization, plus opt-in TOTP MFA, OIDC Authorization Code + PKCE, and LDAP/AD TLS bind. Legacy business metadata tenant ownership is included; login lockout and production provider compatibility matrices remain deferred.
- MCP Streamable HTTP initialization now accepts the standards-compliant first request without a pre-sent protocol-version header; subsequent protocol, session, and Agent/Key checks remain strict.
- Audit query/reporting work, the self-contained five-minute quickstart stack, hardened demo reset sequencing, clearer authorization/database-error reporting, MCP Registry metadata, and ecosystem/website documentation are included in the `v0.4.0..1d6682d` change set.
- Audit reporting now includes a pure-Go multi-page CJK PDF and a ZIP containing CSV, JSONL, PDF, summary, and a per-file SHA-256 `manifest.json`. Strict offline verification rejects path traversal, duplicate/extra members, and size or digest mismatches. The unsigned manifest proves package consistency only; it does not authenticate origin, time, freshness, or WORM/SIEM retention.
- A new English documentation suite, R005 metadata/test-diagnostic fixes, and the KingbaseES + HighGo joint-case evidence and wording package. The production R005 gap was subsequently closed in `02565f5`: EXPLAIN-based large-result warnings run in the normal dynamic stage, and actual execution-layer truncation adds the same structured hit on both the generic and PostgreSQL column-authorization paths.
- **License change notice:** existing v0.4.0-and-earlier source, tags, and release packages remain available under the license that applied when they were published; historical tags and assets will not be recreated. Starting with v0.5.0, the open-source edition uses [Apache License 2.0](../LICENSE) plus a separate [commercial-license notice](../COMMERCIAL-LICENSE.md). This does not imply database-vendor authorization or endorsement.

### Known boundaries

- The DM/Oracle parser covers only the documented v0.5 controlled-SELECT profile. Wildcards are bound to physical source relations, but the parser has no catalog and does not invent concrete column names. This is not full SQL grammar support or integration with the column-authorization/redaction pipeline. KingbaseES V9, GaussDB, and TDSQL commercial target environments remain pending; HighGo's green tier is limited to the tested 9.0.10 Enterprise Edition case above.
- YashanDB's offline parser is wired through standard `NewParser`, and the narrow SELECT path has offline and real-server coverage. A cached standalone 23.4.7.100 client passed the native real-server bind, least-privilege SELECT, and write-denial probe; a separate real-server AgentSQL pipeline test passed SELECT, column lineage, R010, phone masking, and three test-fixture audits using the SYS database account. HTTP/MCP, durable audit storage, and a least-privilege account combined with the pipeline remain untested. None of the 21 NoSQL/vector sources inherit the full SQL protection pipeline. The registered HBase default of `16020` is rejected by the current adapter as a ZooKeeper bootstrap port.
- R005 is closed within its documented boundary. An unbounded query whose controlled EXPLAIN estimate exceeds the effective `row_limit`, or a query actually truncated by the execution layer, produces an `R005` warning in `assessment.hits`; the same hit is persisted in `rule_hits` and is available through the audit management API. There is no separate R005-specific response header or log event. Missing EXPLAIN or audit facts continue to fail closed.
- The PostgreSQL parser P99 release-day gate still requires a fresh run. In batch 33 both five-run sets passed only 3/5 at the 5 ms threshold; batch 61 passed two 5/5 sets in its specified Go 1.26.8 Linux/amd64 environment, with review P99 values of 1.917/2.075/1.994/2.011/1.911 ms. The full repository short-test run did not complete. Release still requires five consecutive passing P99 runs on the standard isolated Linux runner and completion of the other gates.
- Human authentication now includes TOTP MFA with encrypted secrets, replay-resistant counters and one-time recovery codes; OIDC Authorization Code + PKCE with discovery, one-shot state/nonce and RS256 JWKS validation; and certificate-validated LDAP/AD search plus user bind. OIDC/LDAP groups map to explicit tenant-scoped AgentSQL role IDs and fail closed on missing or invalid mappings. Local and federated sessions reuse persistent access revocation and refresh-token family rotation/replay revocation. See `docs/en/AUTHENTICATION.md` for configuration and boundaries.
- Stateful legacy-protocol SSE events are persisted in SQLite/PostgreSQL and can be replayed with `GET /mcp` plus `Last-Event-ID`; TTL/capacity gaps fail closed. MCP transport sessions themselves are still not persisted across processes or restarts. An old ID receives `Mcp-Session-Expired: 1` with the 404 and the client must initialize a new session. Real-account tests for Doubao and specific Claude Desktop/Inspector versions remain pending.
- Protocol-path results do not cover full dialects, production topologies, HA/failover, the complete TLS/authentication matrix, performance SLAs, or vendor support obligations.

### Release assets and release-day gates

The v0.5.0 GitHub Release must contain exactly the 15 named assets in the Chinese table. Batch 31 did not obtain a current green dry-run: the stale local set fails the new Yashan metadata/library gates, while a sealed-current rebuild requires release-day network access. `scripts/release-dryrun.ps1 -ProductionPrepare` now creates the 11 non-signature production candidates from a clean worktree and authorized public key; the release owner must add two content signatures, `SHA256SUMS`, and its signature, then cryptographically verify all three signatures and run `-ValidateOnly`. The GHCR script now builds a real quickstart target, validates OCI platforms, pushes exact tags without moving `latest`, and exposes a separate post-validation `-PromoteLatest` action. The full ordered checklist is in `release-day-checklist-v0.5.md`. No signing, tag, Release, image push, or publication was performed in batch 31.

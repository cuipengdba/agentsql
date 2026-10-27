# AgentSQL

**语言 / Language:** [简体中文](README.md) | [English](README.en.md)

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="brand/agentsql-wordmark-dark.svg">
  <img src="brand/agentsql-wordmark-light.svg" width="360" height="80" alt="AgentSQL">
</picture>

![Version v4.3.0](https://img.shields.io/badge/Version-v4.3.0-blue)
[![Go](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go)](go.mod)
[![License: Apache-2.0](https://img.shields.io/badge/License-Apache--2.0-blue)](LICENSE)

生态收录：[Glama](https://glama.ai/mcp/servers?query=AgentSQL)

已收录于 MCP 官方 Registry，可直接在 MCP Registry 搜索 AgentSQL 接入。

AgentSQL（中文品牌名：智盾，控制台显示为「AgentSQL 智盾控制台」）是面向 AI Agent 的数据库安全网关与生产级 MCP Server：让模型发出的每条 PostgreSQL/MySQL 请求在到达数据库前，经过身份认证、SQL 解析、授权与规则评估、受控执行、结果脱敏和审计。

AgentSQL 有两种使用形态：作为现有 SQLBot 的数据库安全网关，保留原问数界面并对数据库访问施加身份、权限、受控执行、脱敏和持久审计；或使用 AgentSQL 自有的问数、数据源、图表和管理页面独立运行。AgentSQL 的 Go 服务与 Web 页面均为独立实现。完整社区版功能、全部接入方式与现有业务系统零改动替换，须按[页面与接入验收矩阵](docs/v4.5.0-page-parity.md)逐项证明，不能由路由数量或演示页面推定。

v0.6.0 加入治理控制台、审批单次执行和 Vastbase G100 的受限只读适配；v0.7.0 加入独立的 MCP `2026-07-28` 入口与任务级临时只读授权；v0.8.0 加入异人审批；v0.9.0 为新申请加入 1–10 次结果预算、管理审计投递状态和策略离线预检。v1.0.0 收紧列策略的失败关闭，并加入可选的 Agent 与人员双身份 HTTP MCP 验证。v1.1.0 将双身份扩展到审批与 B5 会话，并加入委托令牌单独撤销。v1.2.0 增加可选的 MCP OAuth 受保护资源入口。v1.3.0 增加默认关闭的 MySQL 8.0/8.4 单表 InnoDB SELECT 列授权子集。v1.4.0 在 PostgreSQL metadata 部署中共享控制台密码和 LDAP 登录尝试预算，并验证双网关审计故障恢复。自 v0.5.0 起代码采用 Apache-2.0，历史 v0.4.0 发布物仍按其原许可使用。

v1.5.0 对达梦、金仓和 Vastbase 的指定实例深入验证真实 MCP 客户端、权限撤销、断库恢复及审计。v1.6.0 在相同指定实例和受控只读子集内补齐故障关闭。v1.7.0 为瀚高 9.0.10 企业版 PG 模式增加独立 `highgo` 类型及有界只读路径，并为金仓 V9R1C10 PG 模式补充证书校验与凭据轮换。v1.8.0 在指定 Vastbase G100 Build 9 PG 模式实例验证 `verify-full`、证书和账号轮换、MCP 双客户端、脱敏与审计；主备高可用和完整生产支持仍未验收，见[发布说明](docs/release-notes-v1.8.0.md)。v1.9.0 增加 Dify / MCP Inspector 接入说明、复用 Agent 与数据源的目标 SQL 离线解释及真实调用审计定位；见[发布说明](docs/release-notes-v1.9.0.md)。v2.0.0 增加固定双网关 Kubernetes 配置和 PostgreSQL 单表列策略正式登记 API，见[发布说明](docs/release-notes-v2.0.0.md)及[双网关操作指南](deploy/kubernetes/README.md)。

v2.1.0 在 YashanDB 23.4.1.109 指定实例、独立客户端 23.4.7.100 和受限只读 SQL 子集内，验证最小权限账号的真实 HTTP MCP、R010、脱敏、独立持久审计、MCP Inspector/Dify 以及断库恢复；并补齐已认证 SQL 工具的数据源可见性预检拒绝审计。其他崖山版本、TLS、主备和完整生产支持未验证，见[发布说明](docs/release-notes-v2.1.0.md)与[候选验收](docs/v2.1.0-acceptance.md)。

v2.2.0 在 Oracle AI Database 26ai Free、`FREEPDB1`、服务端 `23.26.3.0.0` 指定合成实例中，把原生驱动的窄 SELECT 路径接入真实 HTTP MCP、最小权限账号、R010、脱敏和持久审计；双客户端、权限撤销及业务库/独立审计库断线恢复也通过。运行账号未验证 `PLAN_TABLE` 写权限，本版 Oracle 路径不预执行计划；商业版、TCPS、主备及完整生产支持未验证，见[发布说明](docs/release-notes-v2.2.0.md)与[候选验收](docs/v2.2.0-acceptance.md)。

v2.3.0 在 SQL Server 2025 Developer 指定 Linux x86_64 合成实例中，使用校验证书的严格 TLS 和仅获目标表 `SELECT`、数据库级 `SHOWPLAN` 的临时账号，完成真实 HTTP MCP、R010、手机号脱敏、持久审计、双客户端及撤权和断线恢复验证。其他 SQL Server 版本、Windows、Always On、生产证书轮换、复杂 T-SQL 和完整生产支持未验证；公开 Live Demo 仍使用合成 PostgreSQL 数据。见[发布说明](docs/release-notes-v2.3.0.md)、[候选验收](docs/v2.3.0-acceptance.md)与[正式发布证据](docs/v2.3.0-release-evidence.md)。

v2.4.0 为 openGauss 社区版 7.0.0 LTS 指定构建增加独立 `opengauss` 类型，强制校验证书和只读会话，仅开放明确 schema、`FROM ONLY` 物理单表、有界直接列 SELECT。在最小权限合成实例中验证真实 MCP、R010、手机号脱敏、审批、持久审计、双客户端、权限撤销、断线恢复和 `gs_dump`/`gs_restore`。GaussDB 商业版、其他构建、主备、写入、B2/B5 和完整生产支持未验证。见[发布说明](docs/release-notes-v2.4.0.md)与[候选验收](docs/v2.4.0-acceptance.md)。

v2.5.0 为 MongoDB 7.0 指定单实例增加 Agent 级 `find_documents` 受控只读工具。它要求精确集合授权、严格 TLS 与已认证最小权限账号，只接受显式顶层标量字段、有界字符串等值过滤和 1–100 行限制；返回前脱敏，并在访问前后写入持久审计。MongoDB 7.0.43 合成实例、MCP Inspector 0.16.8、Dify MCPClient 1.17.1、策略撤销及审计库/业务库断线恢复已验证。副本集、分片、写入和完整生产支持未验证。见[发布说明](docs/release-notes-v2.5.0.md)与[候选验收](docs/v2.5.0-acceptance.md)。

v2.6.0 为 Redis 8.2.9 与 Valkey 8.1.10 的指定 TLS/ACL 单实例增加 Agent MCP `read_key` 受控读取。请求只接受精确键名；`key` 策略独立于 SQL 表授权，拒绝优先；服务器固定执行 `TYPE` 与不超过 64 KiB 的 `GETRANGE`，仅返回 UTF-8 字符串。值按来源脱敏，访问前后写入持久审计；MCP Inspector 0.16.8、Dify MCPClient 1.17.1、策略撤销和审计库/业务库断线恢复在合成数据上通过。Cluster、Sentinel、写入及完整生产支持未验证。见[发布说明](docs/release-notes-v2.6.0.md)、[候选验收](docs/v2.6.0-acceptance.md)与[正式发布证据](docs/v2.6.0-release-evidence.md)。

v2.7.0 为 ClickHouse 26.8.31.2 指定严格 TLS 单节点增加 Agent MCP `query_analytics`：只接收表名、显式列、String 等值条件及 1–100 行限制。网关逐次核验精确表列授权，服务端生成参数化查询并限制资源，在来源脱敏和持久预检、终态审计成功后交付。真实 HTTP MCP、MCP Inspector 0.16.8、Dify MCPClient 1.17.1、权限撤销及断库恢复已在合成数据上验证；集群、其他版本和完整生产支持未验证。见[发布说明](docs/release-notes-v2.7.0.md)与[候选验收](docs/v2.7.0-acceptance.md)。

v2.8.0 为 Milvus 2.6.25 指定 TLS 单机增加 Agent MCP `search_vectors`：只接收精确 collection、一个有限 FloatVector、显式标量字段和 1–20 的命中上限。网关逐次核验精确 collection 与字段授权、CA/主机名、维度及 COSINE 索引，在来源脱敏和持久预检、终态审计成功后交付。真实 HTTP MCP、MCP Inspector 0.16.8、Dify MCPClient 1.17.1、最小权限和撤权已在合成数据上验证；集群、其他版本与完整生产支持未验证。见[发布说明](docs/release-notes-v2.8.0.md)和[候选验收](docs/v2.8.0-acceptance.md)。

v2.9.0 增加租户策略包导出、差异计划、保守表级回放、异人审批和原子应用；批准基线可提示直接修改造成的漂移。外部保存的 Ed25519 密钥可对合规审计 ZIP 及声明的审计链头签署见证。策略包修改仅支持无 B2 绑定和列/行约束的表级 allow/deny；离线回放不代表真实 SQL 可执行，见[发布说明](docs/release-notes-v2.9.0.md)与[验收记录](docs/v2.9.0-acceptance.md)。

v3.0.0 将 30 款已注册数据库的受控 SQL、有界 SQL、专用读取工具和连接预览分层展示在管理端及 MCP 数据源清单，并加入只读 `agentsqlctl doctor` 与合成场景 MCP 验收脚本。能力声明不授予权限，也不扩大任何数据库的已验证版本、部署拓扑或生产支持范围。见[设计](docs/v3.0.0-design.md)与[发布说明](docs/release-notes-v3.0.0.md)。

v3.0.1 为官网、GitHub 文档与 Web 控制台增加中英文入口和英文界面，并修复左侧导航在有效视口高度不足时底部菜单无法滚动的问题。此补丁不修改数据库协议、权限、执行或审计契约；兼容范围沿用 v3.0.0 的证据。见[发布说明](docs/release-notes-v3.0.1.md)。

v3.0.2 补齐 GitHub 与官网的完整英文对应内容，新增当前运维手册与文档索引，修复浅色主题下左侧导航配色，并提供 SQLBot v1.10.2 PostgreSQL 执行层适配。接入仍执行原有授权、脱敏与持久审计；只覆盖验收所列版本和场景。见[SQLBot 接入指南](docs/SQLBOT.md)与[发布说明](docs/release-notes-v3.0.2.md)。

v3.2.0 把指定 MySQL 只读问数接入 SQLBot 个人绑定，打通 PostgreSQL B2 列授权与只读审批，并增加表级策略影响预览；详见[该版发布说明](docs/release-notes-v3.2.0.md)。v3.3.0 为指定达梦 DM8 Pack3 增加服务端证书验证与扫描成本边界。v3.4.0 深化指定金仓与 Vastbase 的受控读取、计划和运维验证，金仓写入与 Vastbase 列授权继续关闭；见[该版发布说明](docs/release-notes-v3.4.0.md)与[验收记录](docs/v3.4.0-acceptance.md)。v3.4.1 修复管理审计 SSE 的跨租户事件隔离；见[安全修复说明](docs/release-notes-v3.4.1.md)。v3.5.0 增加三网关部署样例和跨网关租户审计游标，详见[发布说明](docs/release-notes-v3.5.0.md)、[验收记录](docs/v3.5.0-acceptance.md)与[发布证据](docs/v3.5.0-release-evidence.md)。 v3.6.0 增加简单表策略跨环境签名交付、审计流可靠 Webhook 与外部固定的归档链式验证；见[发布说明](docs/release-notes-v3.6.0.md)与[验收记录](docs/v3.6.0-acceptance.md)。 v3.7.0 为指定 MongoDB 7.0.45 副本集增加批准索引的有界读取，并验证 Redis/Valkey Sentinel 的精确 Hash 字段授权与切主；见[发布说明](docs/release-notes-v3.7.0.md)和[验收记录](docs/v3.7.0-acceptance.md)。 v3.8.0 为指定 ClickHouse 加入可收紧资源预算，并为指定 Milvus 共享集合加入权威租户过滤；见[发布说明](docs/release-notes-v3.8.0.md)和[验收记录](docs/v3.8.0-acceptance.md)。

v3.9.0 为指定 Oracle 26ai Free 增加严格 TCPS 与可选执行计划预检，并深化指定 SQL Server 2025 的证书轮换和故障恢复验证；见[发布说明](docs/release-notes-v3.9.0.md)与[验收记录](docs/v3.9.0-acceptance.md)。

v4.0.0 增加完整发行资产的本地验签、离线运输包、固定字段只读诊断和跨版本升级/恢复流程；见[交付指南](docs/v4.0.0-delivery.md)与[验收记录](docs/v4.0.0-acceptance.md)。

v4.1.0 增加 Go 原生问数、普通账号登录兼容、PostgreSQL/MySQL 单轮查询、加密会话、双语界面及离线原生服务安装。这是 SQLBot 社区版替换计划的首个里程碑，尚未完成全部功能和接入方式兼容。见[问数指南](docs/CHATBI.md)与[发布说明](docs/release-notes-v4.1.0.md)。

v4.2.0 加入最多三轮授权问数上下文、术语与推荐问题、受控 CSV/XLSX 文件数据源及 PostgreSQL 窄范围行规则；见[发布说明](docs/release-notes-v4.2.0.md)。v4.3.0 加入 AgentSQL 自有图表、仪表板和嵌入应用管理，并在本机合成环境对照原版 SQLBot v1.10.2 的指定页面及嵌入问数流程。完整功能、所有页面与现有业务系统零改动替换尚未验收；见[本版发布说明](docs/release-notes-v4.3.0.md)与[验收记录](docs/v4.3.0-acceptance.md)。

**验证范围：**v1.0.0–v2.8.0 的数据库及安全能力分别见各版验收记录；v2.9.0 策略评审与审计见[验收记录](docs/v2.9.0-acceptance.md)；v3.0.0 能力目录、诊断与客户端见[验收记录](docs/v3.0.0-acceptance.md)。厂商结论只覆盖指定实例、模式及合成数据。以下命令须以 [GitHub Release](https://github.com/cuipengdba/agentsql/releases/tag/v4.3.0)、签名及镜像匿名拉取验收为准。

```text
AI Agent → LLM / MCP Client → AgentSQL 网关 → PostgreSQL / MySQL
                                  │
                                  └─ 鉴权 · 授权 · 规则 · 审批 · 脱敏 · 审计
```

## 核心特性

- 双 MCP 承载：本机 `stdio` 与 Streamable HTTP `/mcp`，提供 12 个基础数据库工具；默认开启的 PostgreSQL B5 会话/计划事务另提供 8 个工具。
- 默认拒绝的安全链路：API Key 认证、Agent 能力档位、对象/列授权、SQL AST 规则与 fail-closed 错误处理。
- 受控读写：只读保护、危险 SQL 拦截、Explain 风险评估、超时、连接/QPS/结果行数限制和人工审批。
- 基础数据保护：列级脱敏提供四种算法——六类 `mask` 部分遮蔽、九类均可用的带专用密钥 HMAC `hash` 指纹、无需密钥且把非空值统一替换为 `***` 的 `block` 整值阻断，以及仅用于 `number` / `date`、通过数值分桶或日期截断保留粗粒度分布且无需密钥的 `range`。数据源口令另由 32 字节 `AGENTSQL_SECRET` 加密保存。
- 可追溯运维：默认零配置 SQLite；可选 PostgreSQL 15+ 控制面（PostgreSQL 18 为基准，metadata 与 audit 可分库）；支持审计导出（面向机器的 JSONL，以及 Excel/WPS 可直接打开、带公式注入防护的中文 CSV）、审批决策闭环、Prometheus 指标与健康/就绪探针。
- 安全事件通知：按决策过滤并以 Webhook 或 Syslog 旁路外发；默认关闭、默认不含 SQL，通知失败不影响审计与 SQL 决策。
- 内嵌 Web 控制台：总览、审计、演示台、Agent、数据源、权限、规则、审批、脱敏规则、用户、角色、租户管理及首次接入向导；v3.0.2 支持中英文切换和可滚动左侧导航。
- v0.9.0 受控查询加固：新任务 Key 默认一次结果预算（最多 10 次）、原子扣减；总览展示本租户分库管理审计投递积压；权限页提供只给出拒绝或未知的 SQL 离线预检。
- v1.0.0 安全边界：有列策略的 SELECT 不退回表级放行；可选的 HTTP MCP 人员令牌逐请求验证并与 Agent Key、租户、任务授权绑定。
- v1.1.0 双身份闭环：委托审批单只能由原用户和 Agent 单次执行；B5 会话与计划绑定人类用户、Agent 和租户；新签发的委托令牌可单独撤销。
- v1.2.0 MCP OAuth：可选的 `/mcp/oauth` 受保护资源入口使用外部 OIDC/OAuth 授权服务器签发的资源专用访问令牌，并逐次绑定独立 Agent Key、用户映射、租户与本地权限；支持范围见发布说明。
- v1.3.0 MySQL 列授权：显式开启后，MySQL 8.0/8.4 指定 InnoDB 基表的受限单表 SELECT 使用独立只读业务账号与 PROCESS-only 元数据账号、表 ID 和 metadata lock 证明列权限；不扩大为所有 MySQL SELECT。
- v1.4.0 多网关登录预算：PostgreSQL metadata 部署对密码和 LDAP 登录使用共享的 60 秒 10 次预算；数据库故障返回 503，预算耗尽返回 429，SQLite 单实例仍使用本地预算。
- v2.0.0 固定双网关部署：两个 Kubernetes 网关共享 PostgreSQL metadata 与独立审计库，分开保存 B2 实例标识；无状态 MCP 只读请求通过 Service 转发，升级与回退需维护窗口。管理端可对 PostgreSQL 闭合绑定模式的单张普通表登记列策略。公开 Live Demo 仍采用单网关合成数据环境。
- v2.1.0 崖山指定实例深度验证：临时最小权限账号在真实 MCP 中完成窄 SELECT、R010、手机号脱敏及持久审计；数据库权限撤销、业务库和独立审计库断线均按无原始结果交付验收。已认证的 SQL 工具对不可见数据源的预检拒绝也会记录无原始 SQL 的审计。
- v2.2.0 Oracle Free 指定实例受控只读：临时最小权限账号经真实 MCP 和 Inspector/Dify 执行窄 SELECT、R010 与手机号脱敏；数据库权限撤销、业务库及独立审计库断线按无原始结果交付验收。此路径不预执行 `EXPLAIN PLAN`。
- v2.3.0 SQL Server 2025 Developer 指定实例受控只读：严格 TLS、最小权限账号、真实 MCP、R010、脱敏、审计、双客户端和两类故障关闭已用合成数据验证；不覆盖其他版本或完整生产支持。
- v2.4.0 openGauss 7.0.0 LTS 指定实例受控只读：独立类型、严格 TLS、物理单表窄 SELECT、真实 MCP、审批、脱敏、审计、双客户端与故障关闭在合成实例通过；不覆盖 GaussDB 商业版或完整生产支持。
- v2.5.0 MongoDB 7.0.43 指定实例受控只读：`find_documents` 只读取精确授权集合的显式顶层标量字段；严格 TLS、最小权限、脱敏、持久审计、双客户端与故障关闭在合成单实例通过。
- v2.6.0 Redis/Valkey 指定实例受控键读取：`read_key` 仅读取键级授权的 UTF-8 字符串；严格 TLS、最小 ACL、脱敏、持久审计、双客户端与故障关闭在合成单实例通过。
- v2.7.0 ClickHouse 指定实例受控分析查询：`query_analytics` 只读取精确授权的 MergeTree 单表与显式列；严格 TLS、只读角色、资源预算、脱敏、持久审计、双客户端与故障关闭在合成单节点通过。
- v2.8.0 Milvus 指定实例受控向量检索：`search_vectors` 只读取精确授权 collection 的一个 FloatVector 与显式标量字段；严格 TLS、最小权限、维度与索引校验、脱敏、持久审计及双客户端在合成单机通过。
- v2.9.0 租户策略治理：规范策略包、来源摘要、异人评审与事务应用，并通过批准基线提示漂移；审计 ZIP 可签署外部见证，其链头声明仍须独立核实。
- v3.0.0 统一能力声明：管理端、控制台和已授权 MCP 数据源清单使用同一四层目录；只读 `doctor` 和合成 MCP 验收脚本提供交付前证据，实际访问仍逐次授权、脱敏并持久审计。

## 生态与兼容

AgentSQL 作为数据库中立的安全网关，当前原生支持 PostgreSQL 与 MySQL。v0.6.0 新增 Vastbase G100 的指定版本只读子集；国产数据库的指定实例与协议路径验证范围见下表。

| 数据库 | 适配状态 | 说明 |
| --- | --- | --- |
| 海量数据 Vastbase G100 | 🔵 指定版本受控只读 | G100 V3.0 Build 9、PG 模式、单实例合成表：独立 `vastbase` 类型、强制只读连接、单表有界 SELECT、表授权、手机号脱敏、MCP 审批单次执行及持久审计实测；v1.8.0 补充 `verify-full`、证书及密码轮换和 Inspector/Dify 双客户端验证。临时许可仅用于隔离验收，HA、其他 Build 和生产负载未测；见[本版验收](docs/v1.8.0-acceptance.md)。 |
| 中电科金仓 KingbaseES | 🟢 PG 协议基础闭环实测通过 | 仅 V9R1C10、PG 模式、单实例合成表：可写 license 卷启动、ksql CRUD/分页/标识符、pgx/v5 `$1` 绑定 + SCRAM、只读角色拒写 `42501`、受控 SELECT allow + 手机号脱敏 + 列血缘 + R006 注释拒绝 + allow/deny 审计实测通过；见[真库验证记录](docs/kingbase-v9-verification.md) |
| 瀚高 HighGo | 🔵 指定版本受控只读 | 独立 `highgo` 类型：9.0.10 企业版 PG 模式、单实例合成表的有界 SELECT、表授权、脱敏、MCP 双客户端、权限撤销、故障关闭及持久审计通过；见[验收记录](docs/v1.7.0-acceptance.md) |
| IvorySQL | 已实测 | 社区版协议路径已实测通过 |
| openGauss 社区版 | 🔵 指定版本受控只读 | 独立 `opengauss` 类型，仅官方 7.0.0 LTS build 2397a86f 指定合成单实例：严格 TLS、普通物理单表窄 SELECT、最小权限 HTTP MCP、R010、脱敏、审批、持久审计和两类故障关闭通过；GaussDB 商业版、主备、其他构建及完整生产支持未验证，见[候选验收](docs/v2.4.0-acceptance.md) |
| TiDB | 适配中 | 执行计划（EXPLAIN）适配实现中 |
| OceanBase | 适配中 | 执行计划（EXPLAIN）适配实现中 |
| TDSQL for PostgreSQL | 待目标环境验证 | PG 协议候选；商业版未实测，不能用 OpenTenBase 结果替代 |
| TDSQL for MySQL | 待目标环境验证 | MySQL 协议候选；商业版未实测，不与 TDSQL for PostgreSQL 混同 |
| OpenTenBase | 适配中 | 执行计划兼容修复实现中 |
| 崖山数据库 YashanDB | 🔵 指定版本受控只读 | 23.4.1.109、`COMPAT_VECTOR=yashan`、独立客户端 23.4.7.100：最小权限账号经 HTTP MCP 完成受限 SELECT、R010、手机号脱敏、双客户端和独立持久审计；权限与故障关闭见[v2.1.0 验收](docs/v2.1.0-acceptance.md)。TLS、取消/超时、主备与完整生产支持未验证 |
| 达梦数据库 DM | 🟢 指定实例防护链路实测通过 | DM8 Pack3、`COMPATIBLE_MODE=0`：v0.7.0 有界真库复验了 HTTP MCP 查询、列血缘、R010 表策略、手机号脱敏和持久化审计；R006 注释在解析阶段失败关闭，多行分页/JOIN/函数/系统目录未测，写入未放开 |
| PolarDB for PostgreSQL | 协议路径已实测 | 指定 PG 15 社区镜像复用 `db_type=postgres`；非商业服务认证 |
| PolarDB-X | 旧官方镜像有界实测 | `2.0.1` 单容器复用 `db_type=mysql`，两阶段 EXPLAIN 已适配；当前完整拓扑与商业服务待终验 |

> "已实测"仅指对应版本、镜像/拓扑与用例，非数据库厂商官方认证；"适配中 / 待实测 / 待厂商环境"为进行中的工作，不代表当前版本已支持。欢迎各数据库厂商联系开展兼容性认证与合作：请通过交流群、仓库 Discussion 或邮件 87326549@qq.com 联系我们。

> **金仓 V9 绿色边界：**仅限上述 PG 模式、单实例合成表的只读受控基础闭环；实际 license 到期日未确认。v1.7.0 验证了同一 V9R1C10 PG 模式隔离实例的 `verify-full`、错误 CA/主机名拒绝、证书和账号密码轮换，以及 MCP 脱敏与审计。V9 Oracle 模式、原生驱动、V8、多行/深度分页、JOIN、聚合/函数、复杂表达式、类型 OID、系统目录、EXPLAIN、HA/故障切换、备份恢复、B2 列级授权及生产负载未测。显式 `GRANT CREATE SESSION` 被拒；会话能力经 `LOGIN` + `CONNECT` 验证。非厂商认证或生产支持承诺；详见[真库验证记录](docs/kingbase-v9-verification.md)和[验收记录](docs/v1.7.0-dm-kingbase-depth-acceptance.md)。

> **HighGo 支持边界：**仅限上述 9.0.10 企业版、PG 模式、单实例合成表的明确 schema、非恒真 WHERE、LIMIT 单表 SELECT。TLS、连接池故障切换、扩展类型/OID、其他模式及版本、生产负载、B2/B5 未验证；不代表厂商认证或完整生产支持。

> **崖山客户端授权与运行：**已获厂家口头授权（C 客户端再分发），仓库无书面授权文件。v0.5.1 的 Linux 发行包与 GHCR 镜像构建使用独立客户端 23.4.7.100，并校验匹配架构的 SHA-256；正式资产须通过双架构内容与运行库检查后发布。自行从源码构建时需取得同版客户端、启用 `yashan` 构建标签并配置 `LD_LIBRARY_PATH`；缺少驱动或运行库时明确失败关闭。v2.1.0 指定实例的真实 MCP 与故障注入证据见 [YashanDB 驱动说明](docs/yashan-dialect.md#v210-指定实例-http-mcp-候选验收)和[验收记录](docs/v2.1.0-acceptance.md)。

首批金仓 + 瀚高的证据边界、复现 SQL 与待厂商项见[联合案例验证记录](docs/joint-case-kingbase-highgo.md)；公众号与技术群文案见[社区运营口径](docs/joint-case-operations.md)。

## 5 分钟快速开始

以下三条路径都默认只在宿主机回环地址监听。自动安装器生成的是持久使用的**管理员密码**，不是一次性口令；远程访问请使用 SSH 本地转发，例如 `ssh -L 7780:127.0.0.1:7780 user@server`，不要直接向公网暴露 7780。

### 1. Linux 裸机一行安装（推荐）

v0.4.0 起提供 `linux/amd64` 与 `linux/arm64` 两种原生 glibc 包：`uname -m` 为 `x86_64` / `amd64` 时使用 amd64 包，为 `aarch64` / `arm64` 时使用 arm64 包。两者均要求 glibc 2.28+ 且 systemd 为 PID 1；安装器会自动选择本机架构、校验发布包外层与包内哈希，并安装为 systemd 服务。musl / Alpine 与 CentOS 7 仍不支持：

```bash
curl -fsSL https://github.com/cuipengdba/agentsql/releases/download/v4.3.0/install.sh | sudo sh -s -- install --version v4.3.0
```

管道和 CI 的非 TTY 输出不会显示自动生成的管理员密码；root 可在 `/etc/agentsql/agentsql.env` 查看。交互终端会在首次创建凭据时显示一次，也可显式加 `--show-password`。离线环境同时取得 tarball 与同名 `.sha256` 后，校验、解压并从包根安装：

以下是 v4.3.0 的 amd64 资产名示例；v0.4.0 起 arm64 主机使用同版本的 `-linux-arm64` 资产名。

```bash
sha256sum -c agentsql-v4.3.0-linux-amd64.tar.gz.sha256
tar -xzf agentsql-v4.3.0-linux-amd64.tar.gz
cd agentsql-v4.3.0-linux-amd64
sudo ./install.sh install
```

### 2. Docker 一行启动

脚本默认拉取 GitHub 最新稳定 Release 对应的精确 GHCR tag；该 tag 是 `linux/amd64` + `linux/arm64` 多架构镜像，Docker 会自动匹配运行架构。脚本生成权限为 `0600` 的 `.env`，使用 `agentsql-data` 命名卷，并固定绑定回环地址：

```bash
curl -fsSL https://raw.githubusercontent.com/cuipengdba/agentsql/v4.3.0/scripts/quickstart.sh -o quickstart.sh && sh quickstart.sh --version v4.3.0
```

GHCR 包必须由发布者设为 public，以上命令才能在未登录环境匿名拉取。先核对 `v4.3.0` 精确 tag 的公开状态与双架构 manifest；Docker 会从 manifest list 自动选择 `linux/amd64` 或 `linux/arm64`：

```bash
docker pull ghcr.io/cuipengdba/agentsql:v4.3.0
```

等价的单条 `docker run` 如下；随机值只通过当前 shell 环境传入，不写进命令行参数：

```bash
export AGENTSQL_SECRET="$(openssl rand -base64 24)" AGENTSQL_ADMIN_USER=admin AGENTSQL_ADMIN_PASSWORD="$(openssl rand -base64 18)"
docker run -d --name agentsql --restart unless-stopped --security-opt no-new-privileges:true -p 127.0.0.1:7780:7780 -e AGENTSQL_SECRET -e AGENTSQL_ADMIN_USER -e AGENTSQL_ADMIN_PASSWORD -v agentsql-data:/var/lib/agentsql ghcr.io/cuipengdba/agentsql:v4.3.0
```

### 3. 本地 Live Demo（一条命令，无需源码）

这套自包含环境只连接合成的 PostgreSQL/MySQL 演示数据，不连接真实数据库。无需克隆源码、无需编辑配置或构建镜像，只需下载一个 compose 文件并启动：

```bash
mkdir agentsql-demo && cd agentsql-demo
curl -fsSL -o docker-compose.yml https://raw.githubusercontent.com/cuipengdba/agentsql/v4.3.0/deploy/quickstart/docker-compose.yml
docker compose up -d
```

等待约 1–2 分钟，四个容器就绪后打开 <http://127.0.0.1:17880>：登录页已自动填好演示账号，直接点「登录」即可。「演示台」内置 5 个真实剧本：列授权 + 脱敏、列授权缺失、对象越权、DDL 越权、写操作转人工。彻底清理：

```bash
docker compose down -v
```

Demo 凭据均为固定演示值、写在 compose 中，仅用于本地体验，切勿连接真实数据。完整说明见 [5 分钟快速上手](docs/GETTING_STARTED.md) 与 [Live Demo 指南](docs/DEMO.md)。

## 其他安装方式（源码构建、离线与审计环境）

需要审计每一步、修改 Compose 配置或从源码构建时，可继续使用原有流程。Docker Compose 默认只启动 AgentSQL，元数据和审计写入命名卷 `agentsql-data` 中的 `/var/lib/agentsql/agentsql.db`：

```bash
cp examples/docker/.env.example .env
openssl rand -base64 24   # 填入 AGENTSQL_SECRET，输出恰好 32 个 ASCII 字节
openssl rand -base64 24   # 填入 AGENTSQL_ADMIN_PASSWORD
docker compose up -d --build
docker compose ps
```

PowerShell 可分别执行两次以下命令并填写两个变量：

```powershell
$bytes = [byte[]]::new(24); $rng = [System.Security.Cryptography.RandomNumberGenerator]::Create(); $rng.GetBytes($bytes); [Convert]::ToBase64String($bytes); $rng.Dispose()
```

访问 <http://127.0.0.1:7780>。Compose 也只在宿主机回环地址暴露端口。需要可观测性栈时，先在环境中设置自行生成并保存的随机值，再运行：

```bash
docker compose --profile observability up -d --build
```

完整说明见 [可观测性示例](examples/observability/README.md)。`AGENTSQL_SECRET` 必须与 `agentsql.db` 成对备份；直接更换 SECRET 会让既有数据源口令无法解密。生产控制面可将 metadata 与 audit 分别放入独立的 PostgreSQL 15+ 数据库，完整迁移、最小权限和回滚流程见 [部署指南](docs/DEPLOY.md#postgresql-控制面部署)。

源码二进制构建按 `go.mod` 需要 Go 1.26、cgo、C 编译器和 glibc 兼容环境；本地 `make build` 使用 `PATH` 中的 Go，须先确认版本。当前 Makefile 与正式发布脚本默认使用 Go 1.26.10；普通构建直接使用仓库已有的内嵌控制台产物，不需要 Node.js。

以下源码构建示例仅生成本机可执行文件。自行构建的文件没有经过正式发布资产的签名与分发验收：

```bash
make build VERSION=v4.3.0
./bin/agentsqlctl init-config -o config.yaml
export AGENTSQL_SECRET="$(openssl rand -base64 24)"
export AGENTSQL_ADMIN_USER='admin'
export AGENTSQL_ADMIN_PASSWORD="$(openssl rand -base64 24)"
./bin/agentsql serve -c config.yaml
```

Windows PowerShell：

```powershell
./bin/agentsqlctl.exe init-config -o config.yaml
$bytes = [byte[]]::new(24); $rng = [System.Security.Cryptography.RandomNumberGenerator]::Create(); $rng.GetBytes($bytes); $env:AGENTSQL_SECRET = [Convert]::ToBase64String($bytes); $rng.Dispose()
$env:AGENTSQL_ADMIN_USER = 'admin'
$passwordBytes = [byte[]]::new(24); $passwordRng = [System.Security.Cryptography.RandomNumberGenerator]::Create(); $passwordRng.GetBytes($passwordBytes); $env:AGENTSQL_ADMIN_PASSWORD = [Convert]::ToBase64String($passwordBytes); $passwordRng.Dispose()
./bin/agentsql.exe serve -c config.yaml
```

`agentsqlctl check-config -c config.yaml` 只校验配置，不要求凭据；`agentsqlctl migrate -c config.yaml` 会校验 SECRET 后执行元数据库迁移。

v0.5 审计报表支持 CSV、JSONL、纯 Go 生成的 PDF，以及包含四类载荷和逐文件 SHA-256 `manifest.json` 的 ZIP：

```powershell
./bin/agentsqlctl audit report -c config.yaml --format archive --out audit-compliance.zip
./bin/agentsqlctl audit verify-archive --in audit-compliance.zip
```

归档校验只证明包内一致性，不替代数字签名、外部时间戳、WORM 或 SIEM 保留。

## MCP 接入

先在控制台创建 Agent、配置数据源和授权策略，并保存只显示一次的 `asql_...` API Key。

### stdio

```json
{
  "mcpServers": {
    "agentsql": {
      "command": "/absolute/path/to/agentsql",
      "args": ["mcp", "--config", "/absolute/path/to/config.yaml"],
      "env": {
        "AGENTSQL_SECRET": "<YOUR_32_BYTE_SECRET>",
        "AGENTSQL_API_KEY": "<YOUR_AGENTSQL_API_KEY>"
      }
    }
  }
}
```

### Streamable HTTP

兼容 MCP 本地端点为 `http://127.0.0.1:7780/mcp`；非本机部署必须在前面配置 HTTPS 反向代理。

```json
{
  "mcpServers": {
    "agentsql-http": {
      "url": "http://127.0.0.1:7780/mcp",
      "headers": {
        "Authorization": "Bearer <YOUR_AGENTSQL_API_KEY>"
      }
    }
  }
}
```

网关始终提供 12 个基础 MCP 工具：`list_datasources`、`list_schema`、`explain_query`、`query`、`find_documents`、`read_key`、`query_analytics`、`search_vectors`、`execute_write`、`request_approval`、`get_approval_result`、`execute_approved`。`find_documents` 仅面向具备精确集合授权、严格 TLS、认证账号的 MongoDB 指定版本受控只读路径。`read_key` 仅面向 Redis/Valkey 指定 TLS/ACL 单实例、独立键策略和 UTF-8 字符串。`query_analytics` 仅面向 ClickHouse 指定 TLS 单节点、MergeTree 单表及精确表列授权。`search_vectors` 仅面向 Milvus 2.6.25 指定 TLS 单机、精确 collection/字段授权和单向量受控检索。v0.4.0 默认开启的 PostgreSQL B5 会话/计划事务还会注册 `open_session`、`close_session`、`get_session_status`、`begin_transaction`、`execute_transaction_statement`、`commit_transaction`、`rollback_transaction`、`get_transaction_status` 8 个工具；显式关闭 `mcp.sessions.enabled` 后只保留基础工具。

### v0.7.0 接入变化

- 支持 MCP `2026-07-28` 的客户端可使用独立的 `http://127.0.0.1:7780/mcp/2026-07-28`。每次请求重新验证 Agent API Key、租户及 QPS，要求版本 Header、`Mcp-Method`、适用时的 `Mcp-Name` 与请求体元数据一致；无需旧版 `initialize` 或传输会话。该入口只提供基础工具，暂不提供 B5 会话/计划事务工具；旧 `/mcp` 和 stdio 继续可用。请求示例见 [MCP 接入指南](docs/INTEGRATIONS.md)。
- 控制台「临时只读授权」可由同时具有策略管理和查询执行权限的管理员，给一个 Agent、一个数据源、一条精确匹配的简单 `SELECT` 签发最长 24 小时的任务 Key，并可撤销。Key 只显示一次、只能调用 `query`，不能扩大原 Agent 的权限；原有规则、脱敏和进入 SQL 流水线的审计继续适用。见 [任务级临时只读授权](docs/task-read-grants.md)。
- 「接入向导」增加 Agent 状态、数据库连接和策略存在性预检。预检不执行 SQL，策略存在不等于 SQL 已获授权；兼容入口 `/mcp` 已在真实 Dify 与 Inspector 客户端核对允许、拒绝和审计，新入口仍以 SDK 协议测试为证据。升级到 v0.7.0 前可用该版 `agentsqlctl migrate --check` 只读报告 schema 版本，退出码 `0` 为当前、`2` 为待迁移、`1` 为错误；备份及显式迁移步骤见 [部署指南](docs/DEPLOY.md#升级与回滚)。

### v0.8.0 任务授权与兼容性变化

- 任务 Key 现在由申请人提交精确 SQL、Agent、数据源和到期时间，另一位同租户审批人核对 SQL 后批准或拒绝；批准后原申请人一次性领取。旧直接签发 API 返回 HTTP 410，v0.7.0 已签发的 Key 继续按有效期与撤销规则生效。
- 授权 ID 关联申请、审批、签发以及进入 SQL 流水线后的允许或拒绝审计。metadata/audit 分库时管理审计先在 metadata 库的 outbox 可靠排队，再投递到 audit 库；应监控积压。
- Dify 1.17.1 真实工作流和 MCP Inspector 2.11.0 已完成指定链路复核，两个 MCP HTTP 入口的工具 schema 通过 Inspector 严格检查。升级前成套备份数据库与配对 `AGENTSQL_SECRET`，操作见 [v1.0.0 发布说明](docs/release-notes-v1.0.0.md)。

## 功能矩阵

| 能力 | 当前代码 | 说明 |
| --- | --- | --- |
| MCP stdio / Streamable HTTP | 支持 | HTTP 使用 Bearer API Key；stdio 绑定单个 Agent Key |
| MCP `2026-07-28` 无状态 HTTP | v0.7.0 | 独立路径 `/mcp/2026-07-28`，旧入口保持可用；新路径不注册 B5 工具 |
| 任务级临时只读授权 | v0.7.0 | 限 Agent、数据源、精确 SQL 与最长 24 小时；只允许 `query`，可撤销 |
| 任务授权异人审批与领取 | v0.8.0 | SQL 仅审批详情向有权审批者展示；原申请人一次性领取 Key |
| 接入预检与迁移版本检查 | v0.7.0 | 向导预检不执行 SQL；`migrate --check` 只读且不替代备份、真实请求验收 |
| Agent 与数据源管理 | 支持 | API Key 仅创建/轮换时返回明文，库内保存哈希 |
| PostgreSQL / MySQL SQL 解析 | 支持 | 结构化 AST 判定，不依赖文本正则代替解析 |
| 表级与列级授权 | 支持 | 默认拒绝；特殊 JOIN/通配语义见“已知限制” |
| 只读、危险语句、限流与 Explain 风险规则 | 支持 | 四态结果：allow / deny / approve / warn |
| 受控查询与写入 | 支持 | 超时、连接上限、结果截断与错误脱敏 |
| 人工审批 | 支持 | 建单、管理员决定、Agent 查询结果 |
| 列级脱敏 | 支持 | 九类型按能力矩阵使用 `mask` / `hash` / `block` / `range`；`range` 仅用于 `number` / `date`，以数值分桶或日期截断保留粗粒度分布且无需密钥 |
| 表.列感知脱敏与安全兜底 | 支持 | 规则支持全局列、`表.列`、`模式.表.列`三档作用域，按来源表/来源列精确命中；JOIN 裸列、`SELECT *`、CTE 外层等无法确定列归属且可能涉及受保护表时 fail-closed 固定阻断为 `***`，不可关闭。PostgreSQL 留空模式匹配任意模式，MySQL 留空模式表示当前库 |
| SQLite / PostgreSQL 控制面、审计导出与仪表盘 | 支持 | SQLite 默认零配置；PostgreSQL 可使用独立 metadata/audit 库 |
| Webhook / Syslog 通知外发 | 支持 | live-only、best-effort；默认仅 deny/error，审计库仍是权威记录 |
| Web 管理控制台 | 支持 | 可用 `console_enabled: false` 完全不挂载管理面 |
| 大屏实时事件流 | 支持 | SSE，默认开启，最多 100 条并发管理端连接 |

## 数据库兼容矩阵

v3.0.0 代码的注册表覆盖 **8 大类、30 款数据库**（9 款关系型 + 21 款 NoSQL / 向量）。openGauss 与瀚高 HighGo 均作为独立的限定只读类型计入；金仓 KingbaseES V9 另列为指定实例 PG 协议路径，不计入 30 款；分类表共 31 行。🟢 表示解析、授权、受控执行、脱敏、审计链路在所述范围通过；🔵 受控只读＝指定实例上的受限查询、脱敏与持久审计链路；🔷 连接级＝ping、版本、Schema、只读预览；🟡 待验证＝等厂商环境。指定实例闭环不代表完整生产支持。

| 类别 | 数据库 | 适配状态 | 边界说明 |
| --- | --- | --- | --- |
| 关系型 | PostgreSQL 14–18 | 🟢 已适配 · 完整防护 | 全链路实测通过 |
|  | MySQL 8.0 | 🟢 已适配 · 完整防护 | 全链路实测通过 |
|  | Oracle AI Database 26ai Free | 🔵 指定版本受控只读 | `FREEPDB1`、服务端 `23.26.3.0.0`：最小权限 HTTP MCP、R010、手机号脱敏、双客户端、权限撤销、独立审计故障及业务库断线恢复通过；`EXPLAIN PLAN` 不在网关账号路径，商业版及完整生产支持未验，见[验收记录](docs/v2.2.0-acceptance.md) |
|  | 达梦 DM8 | 🟢 指定场景闭环 | Pack3、COMPATIBLE_MODE=0：v1.5.0 完成客户端与权限撤销；v1.6.0 补齐审计故障、锁等待超时、取消与恢复复验；见[验收记录](docs/v1.6.0-acceptance.md) |
|  | 崖山 YashanDB | 🔵 已适配 · 指定版本受控只读 | 23.4.1.109 指定实例的最小权限 HTTP MCP、R010、脱敏、持久审计和故障关闭通过；其他版本及生产拓扑未验证 |
|  | SQL Server 2025 Developer | 🔵 指定版本受控只读 | Linux x86_64 单实例合成数据：严格 TLS、最小权限 HTTP MCP、R010、脱敏、双客户端、SELECT/SHOWPLAN 撤权及故障关闭通过；其他版本、Windows/Always On、生产证书轮换和完整生产支持未验证，见[验收记录](docs/v2.3.0-acceptance.md) |
|  | openGauss 社区版 7.0.0 LTS | 🔵 指定版本受控只读 | 独立 `opengauss` 类型，指定 build 2397a86f：`verify-full`、只读物理单表窄 SELECT、真实 MCP、R010、脱敏、双客户端、权限撤销与两类故障关闭通过；GaussDB 商业版、主备及完整生产支持未验证，见[验收记录](docs/v2.4.0-acceptance.md) |
|  | Vastbase G100 | 🔵 指定版本受控只读 | V3.0 Build 9 PG 模式的有界 SELECT、表授权、脱敏、持久审计、客户端、权限撤销与停库恢复实测；v1.6.0 补齐故障关闭复验，v1.8.0 验证 `verify-full`、证书及凭据轮换和双客户端真调用；正式主备、其他 Build 与生产负载未测，见[验收记录](docs/v1.8.0-acceptance.md)。 |
|  | 瀚高 HighGo | 🔵 指定版本受控只读 | 独立 `highgo` 类型：9.0.10 企业版 PG 模式、单实例合成表的有界 SELECT、客户端、权限撤销、审计与故障关闭；见[验收记录](docs/v1.7.0-acceptance.md) |
|  | 金仓 KingbaseES V9（另列） | 🟢 指定场景闭环 | V9R1C10 PG 模式：v1.5.0 完成客户端、权限撤销与网络隔离恢复；v1.6.0 补齐故障关闭复验，不计入 30 |
| 键值 | Redis | 🔵 指定版本受控只读 | 8.2.9 TLS/ACL 单实例：精确键策略、`read_key` UTF-8 字符串、脱敏、持久审计、双客户端与故障关闭通过；Cluster、Sentinel、写入及完整生产支持未验证，见[验收记录](docs/v2.6.0-acceptance.md) |
|  | Valkey | 🔵 指定版本受控只读 | 8.1.10 TLS/ACL 单实例与 Redis 同一窄读合同，独立真库及客户端验收；其他拓扑和完整生产支持未验证，见[验收记录](docs/v2.6.0-acceptance.md) |
|  | Memcached | 🔷 已适配 · 连接级 | ping / 版本 / Schema / 只读预览 |
| 文档 | MongoDB | 🔵 指定版本受控只读 | 7.0.43 单实例：精确集合与可选字段授权、严格 TLS、最小权限 `find`、显式投影和有界等值过滤、脱敏、持久审计、双客户端及故障关闭实测；副本集、分片、写入及完整生产支持未验证，见[验收记录](docs/v2.5.0-acceptance.md) |
|  | Couchbase | 🔷 已适配 · 连接级 | 同口径 |
|  | CouchDB | 🔷 已适配 · 连接级 | 同口径 |
| 宽列 | Cassandra | 🔷 已适配 · 连接级 | ping / 版本 / Schema / 只读预览 |
|  | ScyllaDB | 🔷 已适配 · 连接级 | 同口径 |
|  | HBase | 🔷 已适配 · 连接级 | 同口径 |
| 图 | Neo4j | 🔷 已适配 · 连接级 | ping / 版本 / Schema / 只读预览 |
|  | JanusGraph | 🔷 已适配 · 连接级 | 同口径 |
|  | NebulaGraph | 🔷 已适配 · 连接级 | 同口径 |
| 时序 | InfluxDB | 🔷 已适配 · 连接级 | ping / 版本 / Schema / 只读预览 |
|  | Prometheus | 🔷 已适配 · 连接级 | 同口径 |
|  | TimescaleDB | 🔷 已适配 · 连接级 | 同口径 |
| OLAP | ClickHouse | 🔵 指定版本受控只读 | 26.8.31.2 严格 TLS 单节点：`query_analytics` 的 MergeTree 单表显式列、String 等值过滤、精确表列授权、只读角色、资源限制、脱敏、持久审计、双客户端与断库恢复通过；其他版本、集群及完整生产支持未验证，见[验收记录](docs/v2.7.0-acceptance.md) |
|  | Apache Doris | 🔷 已适配 · 连接级 | 同口径 |
|  | StarRocks | 🔷 已适配 · 连接级 | 同口径 |
| 向量 | Milvus | 🔵 指定版本受控只读 | 2.6.25 TLS 单机 `default` 数据库：`search_vectors` 的精确 collection/字段授权、有限 FloatVector、COSINE 索引、脱敏、持久审计和双客户端在合成数据上通过；集群、其他版本及完整生产支持未验证，见[候选验收](docs/v2.8.0-acceptance.md) |
|  | Qdrant | 🔷 已适配 · 连接级 | 同口径 |
|  | Weaviate | 🔷 已适配 · 连接级 | 同口径 |

除 MongoDB、Redis、Valkey 与 Milvus 的指定版本受控只读外，其他 NoSQL / 向量目前是连接级、低阶支持；只读预览不代表完整查询、检索或服务端安全能力。21 款注册类型、默认端口及 HBase/Couchbase 的实际连接端口边界见 [NoSQL 支持范围](docs/nosql-support.md)。DM8、金仓、Vastbase 的指定实例在 v1.5.0 通过受控只读查询、真实 MCP 客户端、权限撤销、断库恢复、脱敏和持久审计；v1.6.0 在同一范围通过独立审计库故障、锁等待超时、客户端取消、数据库端终止及恢复复验，仍不声明完整生产支持。DM8 的 R006、多行分页、JOIN、函数和系统目录未验证，写入未放开。Oracle 在 v2.2.0 的 26ai Free 指定实例完成受控只读闭环，范围见[验收记录](docs/v2.2.0-acceptance.md)；商业版和完整生产支持未验证。YashanDB 在 v2.1.0 指定实例中，使用真实最小权限账号、HTTP MCP 与独立持久审计库完成窄 SELECT、R010、手机号脱敏、双客户端、权限撤销和故障关闭；写入、事务、EXPLAIN、TLS、主备及完整生产支持仍未验证，见 [YashanDB 方言边界](docs/yashan-dialect.md)和[本版验收](docs/v2.1.0-acceptance.md)。所有实测仅基于指定容器镜像与合成用例，**并非数据库厂商官方认证**；协议兼容不等于安全语义相同。所有数据库请求须经 AgentSQL 网关，并使用专用最小权限账号；解析、授权或能力事实不明时一律 fail-closed。详情见 [SECURITY.md](SECURITY.md)。

控制面存储另计：SQLite 默认零配置，可 combined 存储；PostgreSQL 15+ 可 combined 或独立 metadata / audit 库（PG18 为基准）。使用 `agentsqlctl migrate-sqlite-to-postgres` 可从 SQLite 迁至单库或独立双库。

## 已知限制与安全边界

- 脱敏按全局列、`表.列`、`模式.表.列`三档作用域在结果层匹配：能唯一归属到物理表的顶层直接投影列按来源表/来源列精确命中；PostgreSQL/MySQL 已验证的函数/表达式、聚合、`CAST`、`UNION`、CTE 与视图引用等派生输出，在能证明同源时按规则处理，来源不明而可能涉及受保护列时固定阻断为 `***`。这不是完整 DLP；数据库函数与视图内部语义、统计侧信道及绕过网关的直连仍需数据库最小权限、列级授权、安全视图与审批共同约束。
- `hash` 是不可逆指纹，不是加密，也不能解密还原。它在业务数据库返回结果后计算，不下推到数据库内的 JOIN/WHERE；确定性会暴露相等关系和频率，共享同一 key 还会带来跨库关联风险。
- `block` 不输出原值字符、长度或等值关系，但仍保留结果集行列形状、行数、列名、是否有结果，并因空值原样返回而暴露该格为空/NULL；它不是匿名化。`block` 在数据库执行后处理，不减少数据库读取，也不阻止数据库侧使用原值做 WHERE/JOIN/GROUP BY，只阻止结果单元对外返回。其固定 `***` 是不透明字符串，不保证数值、日期或 JSON 的 schema 兼容。
- PostgreSQL 有列策略的 SELECT（含多表 JOIN、自连接、谓词和通配投影）必须走 B2 列绑定路径；B2 不可用时执行前失败关闭。MySQL 8.0/8.4 只在显式开启并完成双账号登记的单表 InnoDB SELECT 子集进入独立列授权路径；其余有列策略的 MySQL 查询执行前拒绝。Vastbase 等仍无等价列级绑定证明。没有列策略时仍由表策略约束。
- `AllowedTables` 中的 `*` 或 `schema.*` 表示管理员显式授予匹配表的全部列；此时精确列白名单不再收紧。单表使用精确列白名单时，应显式列出投影列。
- PostgreSQL 的 B2 列级授权出厂默认开启；普通 schema-qualified 基表使用免扩展的 `CATALOG_CLOSED_V1`，原生 binder 仅用于可选增强。MySQL 不进入 B2 PostgreSQL 路径。
- B5 跨请求逻辑会话与 PostgreSQL 计划事务出厂默认开启；MySQL 跨请求事务不支持，配置 `mcp.transactions.mysql: true` 会拒绝启动。每个 operation 仍只允许一条顶层 SQL，并受预检计划和会话安全边界约束。

## 本地测试模式

`AGENTSQL_INSECURE=1` 永久只表示“允许公开测试凭据”：它可在本地开发/测试中放行长度正确的公开示例 SECRET 和非空弱管理员口令，但仍拒绝空 SECRET、非 32 字节 SECRET 与空管理员口令。它不会关闭认证、安全规则或 fail-closed 行为，生产环境不得设置。

## 文档与社区

- [五分钟快速上手](docs/GETTING_STARTED.md)
- [使用手册](docs/USER_GUIDE.md)
- [MCP 接入指南](docs/INTEGRATIONS.md)
- [当前版本文档索引](docs/README.md)
- [任务级临时只读授权](docs/task-read-grants.md)
- [v3.0.2 发布说明与验收记录](docs/release-notes-v3.0.2.md)
- [当前版本运维手册](docs/OPERATIONS.md)
- [敏感列发现与脱敏草稿](docs/DISCOVERY.md)
- [Webhook / Syslog 通知外发与安全配置](docs/NOTIFICATIONS.md)
- [部署、PostgreSQL 控制面、升级、备份与 systemd](docs/DEPLOY.md)
- [本地 Live Demo、定期重置与公开部署安全清单](docs/DEMO.md)
- [产品与工程规范](docs/SPEC.md)
- [安全策略与私密漏洞报告](SECURITY.md)
- [变更记录](CHANGELOG.md)
- [Issue 反馈](https://github.com/cuipengdba/agentsql/issues)

请不要在公开 Issue 中提交真实密钥、口令、连接串或未修复漏洞细节；未修复漏洞请按 [SECURITY.md](SECURITY.md) 走私密渠道。

### 加入社区（微信）

| 公众号 | 个人微信 |
| --- | --- |
| ![CP 的 PostgreSQL 厨房](website/public/assets/community/wechat-official-account.png) | ![崔鹏个人微信](website/public/assets/community/wechat-personal.png) |
| 扫码关注 | 扫码添加作者，备注「AgentSQL」 |

## 构建与贡献

```bash
go vet ./...
go test -race -count=1 ./...
make build VERSION=v4.3.0
```

`pg_query_go` 要求 cgo；不要使用 `CGO_ENABLED=0` 或 Alpine/musl 构建。提交改动前请阅读 [SPEC](docs/SPEC.md)，为行为变化补测试，并保持 `tests/corpus` 决策语料不被无意改写。

## 开源许可与商标

当前源码使用 [Apache License 2.0](LICENSE)，并附有 [NOTICE](NOTICE)。使用、修改和分发请遵守该许可。AgentSQL（含中文名“智盾”）名称与 Logo 的商标权保留；Apache-2.0 不授予商标使用权。历史 tag 和发布包仍适用其发布时的原许可。

v0.5 人员认证（MFA / OIDC / LDAP）的配置、威胁边界和迁移说明见 [身份认证指南](docs/AUTHENTICATION.md)。

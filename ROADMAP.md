# AgentSQL 路线图

> 本文区分已发布、正在开发和后续方向。开发分支上的功能、测试结果及计划日期均不是公开版本承诺；正式支持范围以对应版本的发布说明和可复核证据为准。

## 产品定位

AgentSQL 是面向 AI Agent 的数据库安全网关与 MCP Server。核心价值是把 Agent 身份、租户、数据库权限、受控执行、脱敏和审计串成闭环。开源社区版继续遵循仓库现有许可证；商业交付与 SLA 以书面约定为准。

默认拒绝是各数据库适配的前提。连接或 PG 协议兼容不等于完整生产支持，也不自动获得 PostgreSQL B2 列级授权或 B5 跨请求事务能力。厂商版本、兼容模式、驱动、许可和部署拓扑变化后需要重新验证。

## 版本状态

| 版本 | 状态 | 已确认或目标范围 |
| --- | --- | --- |
| v0.4.0 | 已发布 | PostgreSQL B2 列级授权、B5 会话事务、审计链、双架构交付与 MCP 基础能力。 |
| v0.5.0、v0.5.1 | 已发布 | 身份与租户治理、数据库适配及国产数据库指定版本真库验证；各项边界见对应发布说明。 |
| v0.6.0 | 已发布 | 审批封存与单次执行、管理控制台与接入向导、Vastbase G100 Build 9 PG 模式的受限只读适配；见 [v0.6.0 发布说明](docs/release-notes-v0.6.0.md)。 |
| v0.6.1 | 已发布（2026-10-16；[正式 Release](https://github.com/cuipengdba/agentsql/releases/tag/v0.6.1)） | 修复 v0.6.0 PostgreSQL metadata/audit 分库管理审计路由，作为该拓扑升级到 v0.7.0 后的可验证回退基线；不改变 schema 或数据库支持范围。以两版正式包进行的隔离回退复测须在 v0.7.0 发行前完成；具体生产部署另需目标环境验收。 |
| v0.7.0 | 已发布（[正式 Release](https://github.com/cuipengdba/agentsql/releases/tag/v0.7.0)） | 无状态 MCP 新协议入口、任务级临时只读授权、迁移预检与升级恢复、接入与运维诊断、三种国产数据库指定版本回归；范围见[发布说明](docs/release-notes-v0.7.0.md)。 |
| v0.8.0 | 已发布 | 任务授权申请/异人审批/一次性领取、授权 ID 审计关联、MCP Inspector 新旧入口真调用、接入排障，以及 DM8、金仓、Vastbase 有界回归和 v0.7 升级恢复；见[验收记录](docs/v0.8-acceptance.md)。 |
| v0.9.0 | 已发布 | 任务 Key 结果预算、管理审计投递状态和策略离线预检；见[发布说明](docs/release-notes-v0.9.0.md)。 |
| v1.0.0 | 已发布 | 列策略失败关闭、可选的 MCP 人员委托、运维解释和完整交付链；范围与门槛见[正式 Release](https://github.com/cuipengdba/agentsql/releases/tag/v1.0.0)及[验收记录](docs/v1.0-acceptance.md)。 |
| v1.1.0 | 已发布（[正式 Release](https://github.com/cuipengdba/agentsql/releases/tag/v1.1.0)） | 双身份扩展至 B5 会话与审批闭环、令牌单独撤销及登录限流；见[验收记录](docs/v1.1.0-acceptance.md)。 |
| v1.2.0 | 已发布（[正式 Release](https://github.com/cuipengdba/agentsql/releases/tag/v1.2.0)） | MCP OAuth/OIDC 标准互通与新旧协议实测；见[验收记录](docs/v1.2.0-acceptance.md)。 |
| v1.3.0 | 已发布（[正式 Release](https://github.com/cuipengdba/agentsql/releases/tag/v1.3.0)） | MySQL 8.0/8.4 可证明的单表 InnoDB SELECT 列策略子集及故障关闭；见[验收记录](docs/v1.3.0-acceptance.md)。 |
| v1.4.0 | 已发布（[正式 Release](https://github.com/cuipengdba/agentsql/releases/tag/v1.4.0)） | PostgreSQL 多网关共享登录尝试预算、双节点审计故障与恢复；见[验收记录](docs/v1.4.0-acceptance.md)。 |
| v1.5.0 | 已发布（[正式 Release](https://github.com/cuipengdba/agentsql/releases/tag/v1.5.0)） | 达梦 DM8 Pack3、金仓 V9R1C10 PG 模式和 Vastbase G100 Build 9 PG 模式的指定实例深度验收；官网与公网 Demo 从 v1.0.0 直接更新至 v1.5.0。范围和未验证项见[验收记录](docs/v1.5.0-acceptance.md)。 |
| v1.6.0 | 已发布（[正式 Release](https://github.com/cuipengdba/agentsql/releases/tag/v1.6.0)） | 对达梦、金仓与 Vastbase 指定实例补齐独立审计库故障、锁等待超时、客户端取消和恢复后的脱敏复验；见[验收记录](docs/v1.6.0-acceptance.md)。 |
| v1.7.0 | 已发布（[正式 Release](https://github.com/cuipengdba/agentsql/releases/tag/v1.7.0)） | 金仓 V9R1C10 PG 模式指定实例的证书校验、证书及凭据轮换；瀚高 HighGo 9.0.10 PG 模式有界只读路径。达梦可验证 TLS 与正式主备切换仍未验证；见[正式发布验收](docs/v1.7.0-release-evidence.md)、[达梦与金仓验收](docs/v1.7.0-dm-kingbase-depth-acceptance.md)及[瀚高验收](docs/v1.7.0-acceptance.md)。 |
| v1.8.0 | 已发布 | Vastbase G100 V3.0 Build 9 PG 模式指定实例的 `verify-full`、证书及凭据轮换与 MCP Inspector / Dify 真调用复验；正式双架构资产、升级恢复、GHCR、官网和 Demo 验收见[发布证据](docs/v1.8.0-release-evidence.md)。临时许可不代表正式许可和主备切换验收。 |
| v1.9.0 | 已发布（[正式 Release](https://github.com/cuipengdba/agentsql/releases/tag/v1.9.0)） | Dify / MCP Inspector 接入说明、目标 SQL 离线解释、真实调用审计定位；正式资产、升级、GHCR、官网与 Demo 见[发布证据](docs/v1.9.0-release-evidence.md)。未取得真实客户首次接入任务完成率。 |
| v2.0.0 | 已发布（[正式 Release](https://github.com/cuipengdba/agentsql/releases/tag/v2.0.0)） | 固定双网关 Kubernetes 部署及 PostgreSQL 单表列策略管理端登记；正式资产、双节点、升级恢复、GHCR、官网与 Demo 见[发布证据](docs/v2.0.0-release-evidence.md)。不承诺无停机滚动升级或任意水平扩容。 |
| v2.1.0 | 已发布（[正式 Release](https://github.com/cuipengdba/agentsql/releases/tag/v2.1.0)） | 崖山 23.4.1.109 指定实例的最小权限账号、真实 HTTP MCP、脱敏、R010、独立持久审计与故障关闭；范围和未验证项见[验收记录](docs/v2.1.0-acceptance.md)，正式交付见[发布证据](docs/v2.1.0-release-evidence.md)。 |
| v2.2.0 | 已发布（[正式 Release](https://github.com/cuipengdba/agentsql/releases/tag/v2.2.0)） | Oracle AI Database 26ai Free、`FREEPDB1`、服务端 `23.26.3.0.0` 指定实例的受控只读 MCP、最小权限、脱敏、R010、持久审计和故障关闭；见[验收记录](docs/v2.2.0-acceptance.md)与[正式发布证据](docs/v2.2.0-release-evidence.md)。商业版、预执行计划和完整生产支持未验证。 |
| v2.3.0 | 已发布（[正式 Release](https://github.com/cuipengdba/agentsql/releases/tag/v2.3.0)） | SQL Server 2025 Developer 指定 Linux x86_64 实例的严格 TLS、最小权限 HTTP MCP、窄 SELECT、R010、脱敏、持久审计、双客户端及故障关闭；见[候选验收](docs/v2.3.0-acceptance.md)与[正式发布证据](docs/v2.3.0-release-evidence.md)。其他版本、Windows/Always On、生产证书轮换和完整生产支持未验证。 |
| v2.4.0 | 已发布（[正式 Release](https://github.com/cuipengdba/agentsql/releases/tag/v2.4.0)） | openGauss 社区版 7.0.0 LTS 指定构建的独立类型、严格 TLS、窄单表 SELECT、真实 MCP 与故障关闭；正式资产、升级恢复、GHCR、官网和 Demo 验收见[发布证据](docs/v2.4.0-release-evidence.md)。其他版本、主备与完整生产支持未验证。 |
| v2.5.0 | 已发布（[正式 Release](https://github.com/cuipengdba/agentsql/releases/tag/v2.5.0)） | MongoDB 7.0.43 指定单实例的 Agent 级集合/字段受控只读、严格 TLS、脱敏、持久审计与故障关闭；正式资产、升级恢复、GHCR、官网和 Demo 验收见[发布证据](docs/v2.5.0-release-evidence.md)。副本集、分片、写入与完整生产支持未验证。 |
| v2.6.0 | 候选验收中 | Redis 8.2.9 / Valkey 8.1.10 指定 TLS/ACL 单实例的 Agent 级精确键受控读取；真库、双客户端、脱敏、持久审计、故障关闭与上一版数据升级候选已通过，正式发布与公开入口仍待验收。见[设计](docs/v2.6.0-design.md)和[候选验收](docs/v2.6.0-acceptance.md)。 |

## v1.8.0–v3.0.0 候选开发顺序

下表是产品方向，不等于已发布承诺。每版独立设计、隔离开发、真库与客户端验证、升级恢复、封版 CI、签名资产、GHCR、官网与 Live Demo 验收。上一版的未验证项不会自动因版本号提升而变成已支持；涉及许可或目标拓扑的缺口须先取得可复核条件，届时可调整范围并记录理由。

| 版本 | 主要目标 | 发布前关键证据 |
| --- | --- | --- |
| v1.9.0 | Dify 与 MCP Inspector 的接入体验、策略解释和向导 | 两客户端真实允许/拒绝调用、首次接入与排障任务完成率 |
| v2.0.0 | 多网关、Kubernetes、管理 API 与运维 | 双节点共享状态、故障转移、升级回滚、容量与审计一致性 |
| v2.1.0 | 崖山 YashanDB 指定版本深化 | 原生驱动、许可、权限/执行/脱敏/审计与故障关闭的真库证据 |
| v2.2.0 | Oracle 指定版本深化 | 原生窄方言、最小权限、授权、脱敏与审计；明确记录最小权限账号的预执行计划限制，按许可和实例范围声明 |
| v2.3.0 | SQL Server 指定版本深化 | 证书、TDS 权限、查询和故障恢复矩阵 |
| v2.4.0 | openGauss 指定版本适配 | PG 兼容差异、类型/计划、TLS、权限和执行边界 |
| v2.5.0 | MongoDB 受控只读能力 | 命令白名单、集合授权、字段脱敏与持久审计 |
| v2.6.0 | Redis/Valkey 受控命令能力 | ACL、键范围、危险命令拒绝、结果脱敏与审计 |
| v2.7.0 | ClickHouse 查询治理 | 查询子集、配额、角色、资源取消和审计终态 |
| v2.8.0 | Milvus 向量检索治理 | 集合/字段授权、过滤表达式、结果保护和审计 |
| v2.9.0 | 策略即代码、审计与多租户治理 | 变更评审、策略回放、租户隔离与审计完整性 |
| v3.0.0 | 统一治理与交付体验 | 跨适配器一致的能力声明、升级迁移、运维与客户端验收 |

## v0.7.0 验收与发布门槛

1. 旧 MCP 客户端与新协议入口都保持独立 Agent 身份、租户、限流和授权；进入 SQL 流水线的请求按原合同做持久审计，新入口暂不开放 B5。Dify 与 MCP Inspector 分别记录真实客户端调用版本、允许与拒绝结果。
2. 临时只读授权只能收窄原 Agent 权限；逐次检查精确 SQL、对象授权、期限、撤销和审计故障关闭。管理端密钥只展示一次，禁止在审计或响应历史里回显。
3. SQLite 验证从 v0.6.0 升级与回退；PostgreSQL metadata/audit 分库验证从 v0.6.1 维护基线升级与回退。两类拓扑均覆盖升级前检查、显式迁移、`auto_migrate:false` 启动保护及成套备份恢复。
4. DM8 Pack3、KingbaseES V9R1C10、Vastbase G100 Build 9 用各自独立实例验证连接、允许查询、越权拒绝、脱敏、审计和故障关闭；只按实际完成的版本与模式描述结果。
5. 代码、前端和升级文档以封板提交验收；发布构建、双架构资产、签名、GHCR 与官网在 tag 后分别核验，并在 GitHub Release 正文及发布证据中记录。未通过的门槛不得在网站或支持矩阵中写成已经发布。

## 后续方向

| 方向 | 启动条件 |
| --- | --- |
| 真实客户试点 | 用户取得愿意参与的目标客户、明确数据边界和书面测试授权后再排期。 |
| 数据库更深层生产支持 | 对具体产品版本逐类补齐权限、执行、审计、故障关闭、升级、许可证、TLS、负载与恢复证据；逐项提升支持级别。 |
| 兼容与竞品对照 | 按实际用户任务对照 Bytebase 等产品的能力、部署成本与安全边界，形成可追踪的功能取舍；避免以数据库数量作为唯一目标。 |
| 企业交付 | 根据试点反馈确定高可用、集中运维、合规归档、私有化支持和 SLA 的可验证范围。 |
| 国际化和生态 | 根据真实客户端版本及社区反馈扩展语言、文档与集成，不预先承诺认证或厂商合作。 |

## 参与与披露

功能和兼容性反馈请通过 [Issues](https://github.com/cuipengdba/agentsql/issues) 提交，开发与测试门见 [贡献指南](CONTRIBUTING.md)。未修复漏洞和真实秘密请按 [安全披露说明](SECURITY.md) 使用私密渠道，不放入公开 Issue 或讨论。

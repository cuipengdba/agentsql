# AgentSQL 路线图

**语言 / Language:** [简体中文](ROADMAP.md) | [English](ROADMAP.en.md)

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
| v2.6.0 | 已发布（[正式 Release](https://github.com/cuipengdba/agentsql/releases/tag/v2.6.0)） | Redis 8.2.9 / Valkey 8.1.10 指定 TLS/ACL 单实例的 Agent 级精确键受控读取；真库、双客户端、脱敏、持久审计、故障关闭、正式资产、上一版升级恢复、GHCR、官网和 Demo 验收见[发布证据](docs/v2.6.0-release-evidence.md)。其他版本、Cluster/Sentinel 与完整生产支持未验证。 |
| v2.7.0 | 已发布（[正式 Release](https://github.com/cuipengdba/agentsql/releases/tag/v2.7.0)） | ClickHouse 26.8.31.2 指定单节点原生 TLS 的受控查询，正式资产、真库、双客户端、升级恢复、GHCR、官网和 Demo 验收见[发布证据](docs/v2.7.0-release-evidence.md)；完整生产支持未验证。 |
| v2.8.0 | 已发布（[正式 Release](https://github.com/cuipengdba/agentsql/releases/tag/v2.8.0)） | Milvus 2.6.25 指定单节点 TLS 的受控向量检索，正式资产、真库、双客户端、升级恢复、GHCR、官网和 Demo 验收见[发布证据](docs/v2.8.0-release-evidence.md)；集群和完整生产支持未验证。 |
| v2.9.0 | 已发布（[正式 Release](https://github.com/cuipengdba/agentsql/releases/tag/v2.9.0)） | 租户策略包、异人评审、表级原子应用、保守离线回放、批准基线漂移提示与外部签名审计见证；正式资产、升级恢复、GHCR、官网和 Demo 验收见[发布证据](docs/v2.9.0-release-evidence.md)。数据库适配级别未扩大。 |
| v3.0.0 | 已发布（[正式 Release](https://github.com/cuipengdba/agentsql/releases/tag/v3.0.0)） | 30 款已注册数据库的统一能力目录、只读交付诊断、可复核 MCP 合成场景验收；正式资产、升级恢复、GHCR、官网和 Demo 验收见[发布证据](docs/v3.0.0-release-evidence.md)。不新增数据库驱动或完整生产支持声明。 |
| v3.0.1 | 已发布 | 官网、GitHub 项目文档和 Web 控制台的中英文入口，控制台菜单滚动修复；不改变数据库支持范围。见[设计](docs/v3.0.1-design.md)、[发布说明](docs/release-notes-v3.0.1.md)与[发布记录](docs/v3.0.1-release-evidence.md)。 |
| v3.0.2 | 已发布 | 全量中英文文档、当前运维入口、SQLBot v1.10.2 PostgreSQL 执行层适配、浅色导航与统一 Logo；见[发布说明](docs/release-notes-v3.0.2.md)和[验收记录](docs/v3.0.2-acceptance.md)。 |
| v3.1.0 | 已发布（[正式 Release](https://github.com/cuipengdba/agentsql/releases/tag/v3.1.0)） | SQLBot v2 普通登录用户绑定、PostgreSQL 业务接入、只读审批接续、中英文映射管理；见[设计](docs/v3.1.0-design.md)、[验收](docs/v3.1.0-acceptance.md)与[发布证据](docs/v3.1.0-release-evidence.md)。B2 开启时只读审批仍失败关闭。 |
| v3.2.0 | 已发布（[正式 Release](https://github.com/cuipengdba/agentsql/releases/tag/v3.2.0)） | PostgreSQL B2 列授权与只读审批安全组合、SQLBot MySQL 真模型问数、策略影响预览和派生结果保护；见[验收](docs/v3.2.0-acceptance.md)与[发布证据](docs/v3.2.0-release-evidence.md)。 |
| v3.3.0 | 已发布（[正式 Release](https://github.com/cuipengdba/agentsql/releases/tag/v3.3.0)） | 达梦指定版本的验证 TLS、查询子集、扫描成本、有界负载与恢复；见[设计](docs/v3.3.0-design.md)、[验收](docs/v3.3.0-acceptance.md)及[发布证据](docs/v3.3.0-release-evidence.md)。 |
| v3.4.0 | 发布候选 | 金仓受限单表 SELECT 列授权、写入关闭；Vastbase 固定构建的类型、物理扫描、验证 TLS、撤权和恢复，B2 列授权仍关闭。见[验收](docs/v3.4.0-acceptance.md)。 |
| v3.5.0 | 待开发 | 三副本多网关、跨实例事件、会话路由、滚动升级与容量治理。 |
| v3.6.0 | 待开发 | 策略跨环境交付、可靠事件投递与可验证的长期审计归档。 |
| v3.7.0 | 待开发 | MongoDB 有界读取及副本集、Redis/Valkey Hash 字段授权与 Sentinel 验证。 |
| v3.8.0 | 待开发 | ClickHouse 分析查询资源约束、Milvus 标量过滤及租户隔离深化。 |
| v3.9.0 | 待开发 | Oracle 和 SQL Server 的证书、计划、查询与指定拓扑验证。 |
| v4.0.0 | 待开发 | 原规划第十版的企业交付与长期维护基线：离线包、跨版本升级、诊断与统一验收。 |

## v3.1.0 设计与验收范围

按用户 2026-10-23 的指令依次实施 v3.1.0～v4.0.0 共十版；v3.1.0 已完成正式交付。原规划第十版编号为 v3.10.0，企业交付与长期维护内容现由 v4.0.0 承接。下表保留本版设计与验收目标，实际完成情况以[发布证据](docs/v3.1.0-release-evidence.md)为准；范围依据见[设计](docs/v3.1.0-design.md)、[验收](docs/v3.1.0-acceptance.md)与[升级指南](docs/v3.1.0-upgrade.md)。

| 功能 | v3.1.0 范围 | 发布前关键证据 |
| --- | --- | --- |
| SQLBot 业务接入 | 固定官方 v1.10.2，普通登录用户、PostgreSQL 和实际模型问数；安装器核对版本及源码哈希 | 真 SQLBot、模型、数据库完成自然语言查询、表格及指定图表；嵌入式 assistant / ask-token 和未验证图表不纳入声明 |
| 工作空间与人员映射 | 中英文控制台按租户管理工作空间、数据源和 SQLBot 用户到既有人员及专用只读 Agent 的绑定 | 跨租户、跨用户、跨工作空间负向验证；大整数 ID 精确保存；权限要求与审计覆盖 |
| 凭据与撤权 | 独立连接凭据一次展示、仅存摘要；独立 Agent Key；2 分钟限定用途人员令牌 | 轮换、禁用、删除、revision 变更与人员撤权使后续请求拒绝；无秘密泄漏 |
| 结构与查询治理 | 授权后的元数据进入模型；指定数据源只读请求经过既有权限、脱敏和审计 | 未授权字段不进入模型；安全拒绝无自动改写重试；故障不退回数据库直连 |
| 只读审批接续 | 用户主动提交，原用户与 Agent 接续封存 SQL，批准后单次执行 | 身份替换、修改 SQL、过期、撤权、重复执行均拒绝；持久审计可关联 |
| 配置与诊断 | v2 配置导出只包含端点、映射与环境变量名；静态配置检查与真实查询验证区分 | 导出无 Key / 连接凭据；环境变量冲突拒绝；多用户映射真实浏览器操作 |
| 升级与交付 | 合库 schema 23→24、分库 metadata 22→23、audit 保持 6；正式构建 Go 1.26.10 | 空库、两种拓扑升级和成套备份恢复；Go/Web/Python/文档与 CI、双架构签名资产、GHCR、官网及公网 Demo 全部通过 |

## v3.1.0 发布门槛

1. 管理权限、租户和人员隔离、凭据生命周期、过期及陈旧令牌、审计故障关闭有自动化与真实链路证据。
2. SQLBot 普通请求、模型线程、结构发现和审批路径使用同一已认证人员；缺失上下文拒绝，不能接受模型或浏览器自报身份。
3. 固定版本真模型业务场景通过；配置诊断通过或模拟 SQL 响应不替代业务验收。旧 v1 配置只按原工作空间级边界保留兼容说明。
4. 迁移与恢复验证分别覆盖合库和分库；旧程序不能直接打开新 schema，回退使用完整配套备份。
5. 封版提交的 CI、正式资产签名、公开镜像、官网、Live Demo 及公开烟测通过后才标记发布；现有数据库支持级别不扩大。
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

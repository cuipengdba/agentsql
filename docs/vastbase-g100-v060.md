# Vastbase G100 v0.6.0 适配边界

状态：已在真实 **Vastbase G100 V3.0 Build 9、PG 兼容模式、x86_64** 实例完成受限只读路径的开发验收；尚未达到完整生产支持或厂商认证。数据库类型独立使用 `vastbase`。下列结论仅适用于已测版本、模式和受限 SQL 子集。

## 当前实现

- 仅针对 G100 **Build 9 的 PG 兼容模式**使用 PG wire 连接。要求具备只读权限的数据库账号；连接参数强制 `default_transaction_read_only=on`，并在建连时检查 `pg_catalog.vb_version()` 的产品和 Build 9 标识、`sql_compatibility=PG`、只读事务参数为 `on`。G100 的 `version()` 返回 PostgreSQL 兼容字符串，不能据此识别产品。行数和语句超时仍由 AgentSQL 限制。
- 原始 SQL 只接收一个显式 schema 的单表 `SELECT`，必须带 `WHERE` 与 `LIMIT`，并拒绝解析器识别出的恒真条件；投影必须是可解析的直接列。拒绝函数、注释、通配符、JOIN、CTE、子查询、聚合、排序、锁行、`SELECT INTO`、写语句及多语句。
- 执行器再次解析 SQL；会话、事务及写入入口在数据库调用前拒绝。普通 PostgreSQL 专属 B2 列级绑定和 B5 事务协调不继承。配置了 B2 列级策略的 G100 查询拒绝执行。
- 元数据读取走固定的 `information_schema.columns` 查询及参数，不接收客户端任意 SQL。敏感发现只能使用元数据模式；采样待真库验收。
- 已在下述实例验证 `EXPLAIN (FORMAT JSON)`、PG 结果协议和绑定参数读取元数据；任何其他版本或模式不自动继承此结论。不兼容时停止执行，不降级为无计划检查或绕过解析。

## 真库验证记录（2026-10-14）

- 安装介质：`Vastbase-G100-3.0_Build9_30156-Linux-x86_64-no_mot-202601221034.tar.gz`；外层 SHA-256 为 `646d7166bd9b1e5b33e119dda6aaea708914d5f7fac016deb848ab47237b316a`。内层归档和数据库负载分别与安装包附带的 `.checksum`、`.sha256` 匹配。
- 拓扑：Windows Docker Desktop 上的 Rocky Linux 8.9 x86_64 独立容器，数据库报告 `Vastbase G100 V3.0 (Build 9) Release`、commit `30156`、`sql_compatibility=PG`。测试账号只获测试 schema 中两张表的 `SELECT` 权限；AgentSQL 连接同时设置只读事务。自动化测试容器共享数据库容器网络命名空间，使用 `127.0.0.1:5432`；数据库 HBA 仅允许回环连接，未为测试放开远端地址。
- 许可证：`vb_licensetool --view-temporary` 报告安装时生成的临时许可，有效期为 2026-10-14 至 2027-01-12，模块为 BASIC/VECTOR。未提供正式生产许可；本次只用于隔离容器验收。
- 合成数据：`agentsql_v060.contacts` 包含测试手机号，`agentsql_v060.customers` 用于未授权表和驱动层查询。无客户数据。
- `TestVastbaseBuild9Integration`：产品与模式校验、只读连接、绑定参数的固定元数据查询、有界单表查询、JSON EXPLAIN、写入/锁行/多语句拒绝通过。
- `TestVastbaseBuild9PipelineRealE2E`：表授权、手机号脱敏、未授权表拒绝、危险 SQL 拒绝及 SQLite 持久审计通过。`TestVastbaseBuild9MCPRealE2E`：真实 MCP 工具调用、申请/批准/单次执行、脱敏、重复执行拒绝、数据源变更和策略撤销拒绝通过。
- 尚未验证：正式生产许可证、TLS、不同操作系统和 CPU、其他 G100 Build、集群与故障转移、长时间负载、升级/回滚、目标客户端 Dify/MCP Inspector；Claude Desktop UI 也未实测。容器验收不能替代这些条件。

## 扩大为完整生产支持前必须完成

1. 获得正式许可证后核对授权范围；验证超时/取消、断库与恢复、列级策略拒绝以及数据库侧无副作用证据。
2. 用 Dify 和 MCP Inspector 各完成真实工具调用、拒绝和审计关联；如需宣称 Claude Desktop 兼容，再用已登录客户端单独验收。
3. 验证安装、升级、回滚及故障恢复，并保留产品、版本、模式、驱动、账号权限和拓扑记录；在此之前不得宣称完整生产支持。

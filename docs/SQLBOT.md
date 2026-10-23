# SQLBot 接入 AgentSQL

[中文](SQLBOT.md) | [English](en/SQLBOT.md)

v3.2.0 沿用配置版本 2 的真实登录用户绑定，并把指定 MySQL 只读问数加入 PostgreSQL 路径。适配目标固定为 SQLBot **v1.10.2**，源码基准为 `aa41fc1033bb974a703df9200a98ec8b9eec5f25`。完成的真链路项目见 [v3.2.0 验收记录](v3.2.0-acceptance.md)；兼容检查通过不等于所有模型、图表和 SQL 都经过验证。

```text
SQLBot 普通账号登录 → 工作空间与数据源权限 → 自然语言生成 SQL
  → 服务端可信用户 + 个人绑定 → 短期人员令牌
  → 本地 Agent Key + 人员令牌 → AgentSQL MCP
  → 当前绑定 / 人员 / 租户 / Agent 策略 → 执行 → 脱敏与持久化审计
  → SQLBot 数据结果与图表
```

## 支持范围与信任边界

- 本版托管绑定支持已验收的 **PostgreSQL/MySQL 只读查询**。不支持写操作、B5、动态数据源或 Excel 直连。SQLBot 自身的 PostgreSQL 元数据库仍由 SQLBot 使用，与业务数据库分开。
- 仅接受 SQLBot 服务端认证后的普通登录身份（`X-SQLBOT-TOKEN`）。拒绝 assistant、embedded 和 ask token 身份。不能把浏览器、模型、请求正文自行提供的用户 ID 当成认证身份。
- 一条绑定对应一个 SQLBot 工作空间、数据源名称和用户，并映射一个 AgentSQL 人员、只读 Agent 和 PostgreSQL 或 MySQL 数据源。MySQL 的发现 schema 必须等于该数据源的数据库名。不同人员需要不同数据范围时，应分配不同 Agent 策略。SQLBot 服务端和连接凭据属于可信部署边界：持有凭据的服务器能为该绑定指定的人员取得令牌。
- 每次操作重新解析绑定；每个 MCP 请求检查绑定状态和修订号、人员权限、租户状态及 Agent。禁用、修改和轮换使旧令牌失效。即使网关没有全局要求人员委托令牌，配置版本 2 仍验证其专用令牌；普通人员令牌原有规则不变。
- `schema` 限定元数据发现范围，**不单独构成 SQL 授权边界**；实际 SQL 数据范围仍由 Agent 策略和数据库权限控制。已有 SQLBot 元数据还会与当前人员在 AgentSQL 可见的表、列取交集，避免较宽管理员缓存进入模型上下文。
- 托管查询额外限制为单条 `SELECT`。PostgreSQL 拒绝 `SELECT INTO`、`EXPLAIN`、修改数据的 CTE、锁定读及 `FROM` 中的函数；MySQL 拒绝写入、多语句、锁定读、副作用函数和无法证明只读性的语法，允许经双重解析的反引号标识符。两种方言都只放行短名单中的函数。PostgreSQL B2 执行器要求函数显式使用 `pg_catalog` 限定名，例如 `pg_catalog.count(*)`；模型生成未限定 `COUNT(*)` 时会被拒绝，适配器不会自动改写。解析树限制为 128 层和 20,000 个节点。AgentSQL 原有策略、对象授权和数据库最小权限仍然生效。

## 1. 准备网关并建立个人绑定

1. 启动 v3.2.0 AgentSQL 管理控制台和 HTTP MCP，配置 PostgreSQL 或 MySQL 业务数据源及最小权限数据库账号。
2. 创建同租户、启用的 **readonly 普通 Agent**（不能是临时任务 Agent），配置允许的表、列、脱敏和审批策略。创建或选择具有 `query.execute` 权限的 AgentSQL 人员。
3. 在控制台左侧 **SQLBot** 页面建立绑定。创建、修改和轮换要求同时具备 `strategy.manage`、`datasource.manage`、`user.manage` 权限。填写 SQLBot 工作空间 ID、用户 ID、数据源名称、AgentSQL 人员、Agent、业务数据源、发现 schema 和可选数值列别名。
4. SQLBot 的 Snowflake ID 可能超过 JavaScript 安全整数，配置和 API 中工作空间 ID、用户 ID 必须使用**十进制字符串**；不要转为浮点数。数据源名称必须与 SQLBot 中完全一致。
5. 保存创建时显示的连接凭据。数据库仅保存其哈希；明文只在创建和轮换响应中返回。丢失后轮换。Agent Key 是另外的凭据，有独立生命周期。
6. 导出配置。导出文件只有绑定信息和环境变量名，**没有凭据**。控制台配置诊断的 `configuration_ready` 只说明配置条件满足，不代表真实业务问数已通过。

先用 MCP Inspector 等工具确认允许、拒绝、脱敏与审计，再接 SQLBot。业务数据库账号、网关管理令牌和连接凭据不得写进提示词、聊天记录或浏览器端代码。

## 2. 构建固定版本派生镜像

在 AgentSQL 仓库根目录执行：

```sh
docker build -f integrations/sqlbot/Dockerfile -t agentsql-sqlbot:1.10.2-v3.2.0 .
docker run --rm --entrypoint python3 agentsql-sqlbot:1.10.2-v3.2.0 /tmp/agentsql-sqlbot/install.py --sqlbot-app /opt/sqlbot/app --check
```

Dockerfile 固定上游镜像 digest；安装器校验四个上游文件的源码哈希后才修改。补丁覆盖数据库调用、问数线程身份传递、元数据过滤和审批操作页，禁止适配路径绕过网关直连业务库。策略拒绝或需要审批时终止本次问数，避免 SQLBot 自动修复 SQL 并重试。

`--check` 对照安装清单校验八个安装文件，发现版本或文件漂移时失败。清单是部署一致性诊断，不能代替镜像签名和来源验证。升级 SQLBot 前需重新适配、检查哈希并完成验收；不能跳过固定版本检查。保留上游版权、标识及许可证要求。

## 3. 挂载配置与服务器端凭据

以下为配置版本 2 示例；通常应直接使用控制台导出：

```json
{
  "version": 2,
  "endpoint": "https://your-agentsql.example/mcp",
  "timeout_seconds": 30,
  "min_interval_seconds": 0.6,
  "bindings": [
    {
      "id": "binding-id-from-agentsql-console",
      "workspace_id": "7518977644151443456",
      "datasource_name": "AgentSQL PostgreSQL",
      "sqlbot_user_id": "7518977644151443457",
      "agent_key_env": "AGENTSQL_SQLBOT_READONLY_KEY",
      "connector_token_env": "AGENTSQL_SQLBOT_CONNECTOR_TOKEN"
    }
  ]
}
```

- 将文件只读挂载进 SQLBot 容器，用 `AGENTSQL_SQLBOT_CONFIG` 指向容器内路径。通过受保护的部署环境或 secret 注入上面两个环境变量的实际值，不提交到 Git。
- 每次操作向同一网关源的 `/api/v1/integrations/sqlbot/resolve` 发送连接凭据，以及绑定 ID、工作空间 ID、数据源名称、可信用户 ID。四者必须匹配。成功取得两分钟专用人员令牌、当前修订号及数据源信息；Agent Key 始终来自本地环境，不由解析 API 返回。
- MCP 请求同时携带 Agent Key 和 `X-AgentSQL-User-Authorization: Bearer <短期令牌>`。令牌只用于当前绑定；不能替代管理登录、普通人员令牌或其他绑定。
- 非回环地址必须使用 HTTPS；HTTP 仅允许 `localhost`、`127.0.0.1` 和 `::1`。适配器拒绝重定向，不读取环境代理。私有 CA 可通过顶层 `ca_file` 指定。不要关闭证书校验。
- 保留 SQLBot 原有持久化目录、元数据库和模型配置。SQLBot 中的业务数据源可使用 `agentsql.invalid` 等占位主机，不填写真实业务数据库凭据；生产网络也应禁止 SQLBot 直接访问业务数据库。模型会看到获准的元数据和脱敏结果，应按组织数据要求选择模型部署位置。
- 修改绑定或轮换凭据后，更新服务器端 secret，并重建/重启使用新环境的容器，保留原数据卷。修改 SQLBot 数据源名称或人员映射后重新导出配置。配置文件按操作读取。
- `min_interval_seconds` 是单进程限速，不是集群级协调。超时、429、连接失败和策略拒绝均不会自动重试业务 SQL。

## 4. 验证完整问数

使用与绑定一致的 SQLBot 普通账号和工作空间，建立同名 PostgreSQL 或 MySQL 数据源，执行连接检查、表发现，再配置模型进行自然语言问数和图表生成。MySQL 表权限使用 `数据库.表名`；现有 SQLBot 工作空间及数据权限检查保留，执行 SQL 的最终许可由网关决定。

适配器向 SQLBot 返回 `fields`、`data`、`fields_info` 和内部扩展字段 `agentsql.audit_id`；SQLBot 聊天记录可能不保存该扩展字段，持久审计应到 AgentSQL 网关查询。查询列别名必须唯一。需要图表数值类型时，在绑定的 `numeric_columns` 中明确列出**查询结果别名**；仅对能精确、安全表示的有限数值做转换，脱敏值和不安全高精度数值保持字符串。不要用通用强制转换破坏财务精度或脱敏结果。

一次连接成功不能代替完整验收：至少检查真实模型生成 SQL、网关执行、脱敏结果回传、图表和持久化审计。发布实测范围见 [验收记录](v3.2.0-acceptance.md)。

## 5. 拒绝、审批与一次执行

拒绝反馈包含经过筛选的决策、审计 ID、审批 ID 和规则标识，不回传原始网关错误、凭据或隐藏数据。模型不会自动提交审批、改写被拒 SQL 后重试或自动执行已批准请求。

SQLBot 同源操作页：**`/api/v1/agentsql/console.html`**（配置了 context path 时加对应前缀），支持中英文。

1. 先使用 SQLBot 普通账号登录。页面可读取同源既有登录；也可临时输入本人的 SQLBot 登录令牌，输入值仅留在页面内存中。不要输入 Agent Key、连接凭据或网关管理令牌。
2. 加载本人当前工作空间的绑定并运行诊断。诊断只检查连接和授权元数据，不执行业务查询。
3. 已有审批 ID 时查询状态；需要新申请时，用户明确填写 SQL 和原因并勾选确认，点击提交。网关封存这份 SQL，提交本身不执行它。
4. 审批者在 AgentSQL 原有审批界面处理。批准后由原申请人员、原 Agent、原数据源执行，操作页只发送审批 ID，不允许替换 SQL。当前授权仍须有效，封存请求最多执行一次。

PostgreSQL B2 列级授权开启时，审批申请仅封存原 SQL、身份和数据源，不读取业务行；批准后由原申请人单次执行，并重跑实时 B2 列授权、对象绑定、脱敏和持久审计。无法证明当前权限、审计或租约时失败关闭，不会为了审批自动关闭 B2。该组合的真库测试见 [v3.2.0 验收记录](v3.2.0-acceptance.md)。
5. 结果在操作页单独显示；**不会自动改写原 SQLBot 对话或重新生成图表**。

所有操作 API 使用普通登录认证。审批提交、状态查询和执行均为 POST JSON；变更操作必须显式确认，不允许 GET 触发执行。自定义登录头、同源检查和不使用 Cookie 自动携带凭据共同限制跨站请求。操作页是绑定和 Agent 策略约束下的明确 SQL 操作入口，不应将其视为 SQLBot 模型自定义行过滤器的替代品。

## 故障排查与验收

| 现象 | 检查内容 |
| --- | --- |
| 没有可用绑定 | 普通登录状态；当前工作空间和用户 ID 的精确字符串；数据源名称；绑定启用状态 |
| 人员解析或 MCP 认证失败 | 连接凭据与 Agent Key 是否混用；轮换后环境是否更新；人员 `query.execute`、租户和 Agent 状态 |
| 表或列缺失 | 当前 Agent 表列权限、发现 schema、SQLBot 元数据缓存和数据源权限 |
| SQL 被拒绝 | Agent 策略、只读 SQL 子集、函数白名单；按审计 ID 排查，不以自动重试绕过 |
| 图表数值不正确 | 唯一列别名、`numeric_columns`、脱敏结果和高精度字符串 |
| 已批准请求不能执行 | 原申请人员/Agent/数据源、当前权限、审批状态及是否已经消费 |
| 兼容检查失败 | 上游版本、源码哈希或安装文件漂移；重新构建固定镜像并验收 |

验收至少覆盖两个用户、不同工作空间或数据范围、真实自然语言问数及图表、允许/拒绝、写 SQL 阻断、脱敏、审计、禁用/修改/轮换、人员撤权、重复执行与网关故障关闭。撤权阻止新的请求，不删除已保存于 SQLBot 的历史结果；其保留和清理由 SQLBot 管理。

适配器测试：`python -m unittest discover -s integrations/sqlbot -p 'test_*.py' -v`。路由测试依赖 FastAPI 等 SQLBot 运行时库；缺少时会跳过，应在固定 SQLBot 镜像内运行完整套件。测试和日志不得包含真实凭据或未授权业务数据。

## 历史配置版本 1 与迁移

v3.0.2 的配置版本 1 使用工作空间和数据源名称匹配，字段包括 `workspace_id`、`datasource_name`、`dialect: "pg"`、`datasource_id`、`schema`、`key_env`、`numeric_columns`。它是工作空间级 Agent Key 接入，**没有本版个人映射、短期人员令牌和托管审批操作页能力**；历史验证范围见 [v3.0.2 验收记录](v3.0.2-acceptance.md)。

迁移时先建立版本 2 的个人绑定、保存一次性凭据、导出配置，再切换派生镜像及服务器 secret 并完成验收。保留旧配置仅用于受控恢复；要求个人身份治理的部署不能退回版本 1 来绕过人员验证。没有在绑定中出现的 SQLBot 用户应直接失败关闭。

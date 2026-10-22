# AgentSQL 当前版本运维手册

**语言 / Language:** [简体中文](OPERATIONS.md) | [English](en/OPERATIONS.md)

适用版本：v3.0.2 及兼容的 3.0.x 维护版本。具体发行提交、工具链与验收结果以对应正式 Release 为准。本文代替日常入口中的 v0.7 运维手册；历史报告仍保留其当时的版本与事实。

## 值班检查与版本核对

在部署使用的同一配置、运行账号和环境变量下执行：

```bash
agentsqlctl version
agentsqlctl doctor --config /etc/agentsql/config.yaml --format json
curl --fail http://127.0.0.1:7780/healthz
curl --fail http://127.0.0.1:7780/readyz
```

`doctor` 只读检查配置、连接与已配置迁移流，不执行 DDL、不创建缺失 SQLite 文件，也不检查所有表数据的一致性。退出码 `0` 表示就绪，`2` 表示待迁移，`1` 表示阻断错误。不能把 `2` 或 `1` 当成可上线状态。`/healthz` 表示存活，`/readyz` 还检查服务依赖；以实际返回的子系统状态定位问题。

v3.0.2 没有新增 schema：SQLite 合并库最新版本为 23，PostgreSQL metadata 为 22、独立 audit 为 6。以当前二进制的 `doctor`/`migrate --check` 输出为准，不手工修改迁移版本表。

## 备份、升级与恢复

1. 核对目标 Release、Ed25519 公钥、签名清单和包哈希。保留上一版本程序、配置及完整恢复路径。
2. 在维护窗口停止写入，成套备份 metadata、audit（若分库）、配置、数据源加密使用的 `AGENTSQL_SECRET`，以及脱敏密钥、OIDC/LDAP 等相关外部配置。密钥独立加密保存，不能公开或随源码提交。
3. SQLite 使用 SQLite backup API/`.backup` 或在所有进程完全停止后备份数据库及必要 WAL 状态。不能在服务写入期间只复制主 `.db` 文件。
4. PostgreSQL 分库必须对 metadata/audit 做配套备份并在独立库恢复验证；使用迁移负责人账号执行 DDL，服务运行账号保持文档规定的最小权限。
5. 在隔离恢复副本上用上一正式程序和目标程序运行只读检查。确有迁移时，由 migration owner 显式执行下列命令后再检查：

```bash
agentsqlctl migrate --check --config /etc/agentsql/config.yaml
agentsqlctl migrate --config /etc/agentsql/config.yaml
agentsqlctl doctor --config /etc/agentsql/config.yaml --format json
```

6. 启动后核对版本、健康、就绪、管理员登录，以及一个最小权限 Agent 的允许、越权拒绝、脱敏和持久审计。保留审计 ID、时间、实际提交和拓扑，不在工单粘贴密钥或原始敏感结果。
7. 若必须回退，先停止新版，恢复配对数据库、配置和密钥，再运行对应旧程序；禁止让旧程序直接读取较新的 schema。恢复到备份会丢失备份时间之后的写入，按维护窗口的恢复方案处理。

部署和 PostgreSQL 权限、备份命令见[部署指南](DEPLOY.md#升级与回滚)。签名与包核验见[发行资产](RELEASE_ASSETS.md)。

## 审计故障与未知提交状态

出现 `AUDIT_UNAVAILABLE`、`AUDIT_OVERLOADED` 或就绪探针 503 时，核对 audit 连接、磁盘空间、连接池、写入延迟和管理审计 outbox 投递状态。保持故障关闭，恢复后再通过允许/拒绝请求及持久审计确认恢复。

写请求若返回 `COMMIT_OUTCOME_UNKNOWN`，不能自动重试；先人工核对业务库实际状态、事务状态和审计事实。只读请求也需确认原请求已结束后才以新请求重试。

## MCP、身份和权限排查

| 入口 | 当前行为 | 排查要点 |
| --- | --- | --- |
| `/mcp` | 有状态 Streamable HTTP，初始化后保留会话 ID | 每次携带 Agent Bearer、协商协议；后续请求携带 `Mcp-Session-Id`。适合现有 Dify、MCP Inspector 与 SQLBot 适配器。 |
| `/mcp/2026-07-28` | 无状态入口，每次独立认证 | 协议版本、`Mcp-Method`、适用时的 `Mcp-Name` 和请求 `_meta` 一致；没有旧入口 B5 会话工具。 |
| 可选 OAuth/人员委托 | 按部署显式配置开启 | 分别检查 Agent、人员、委托 token、租户和撤销状态；管理员登录 token 不能替代 Agent Key。 |

`401` 检查 Key 与会话状态，`429` 检查 Agent 限流，协议错误核对请求头和请求体，不通过切换入口绕过限制。权限变更后重新执行原本应被拒绝的精确对象/列请求，确认执行前拒绝及审计事实。详细示例见[MCP 接入指南](INTEGRATIONS.md)。

## 数据库、B2/B5 与脱敏故障

业务库的类型、版本、协议模式、TLS、账号权限和 SQL 子集必须满足对应适配证据。已注册类型不等于完整生产支持，详见[能力目录](USER_GUIDE.md)及各数据库验收记录。

B2 列绑定、B5 会话/计划事务或脱敏依赖不就绪时，根据 `/readyz` 的原因与审计定位，保持当前拒绝行为。不得关闭列策略、扩大表通配权限或关闭审计来让请求通过。B5 未知提交终态、租约与计划恢复需按实际会话和事务 ID 人工核对。

## 界面、语言与客户端集成

控制台登录页和顶部可切换中英文，选择保存在当前浏览器；`?lang=en` 或 `?lang=zh` 可设置初始语言。主题按钮控制整个界面，包括左侧导航。导航在短有效视口内独立滚动，可使用滚轮、触控板或键盘访问底部选项。

SQLBot 适配按[SQLBot 接入指南](SQLBOT.md)单独安装并验证，必须保持 AgentSQL 为查询执行入口。SQLBot 自身的工作空间权限和 AgentSQL 的 Agent/租户权限分别配置。

## 历史资料

[v0.7 运维报告](v0.7-operations-runbook.md)和历史版本验收是当时环境的记录。排查当前版本优先使用本文、当前部署指南和对应正式 Release；历史通过结论不能作为本次客户环境的验收。

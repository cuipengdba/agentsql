# SQLBot 接入 AgentSQL

**语言 / Language:** [简体中文](SQLBOT.md) | [English](en/SQLBOT.md)

适配对象：[DataEase SQLBot 官方 v1.10.2](https://github.com/dataease/SQLBot/tree/v1.10.2)，上游提交 `aa41fc1033bb974a703df9200a98ec8b9eec5f25`。该适配器随 AgentSQL v3.0.2 提供；v3.0.1 的 `list_schema` 尚未输出字段类型，不能直接替代新版完成字段发现。

## 接入方式与边界

SQLBot 自己提供 MCP Server，其数据库查询并不会因此调用其他 MCP Server。这里提供独立 Python 适配器和固定源码哈希的安装器，在构建 SQLBot 派生镜像时，把表/字段发现、连接检查与生成后的 SQL 执行送到 AgentSQL `/mcp`。不改动 SQLBot 的 Logo、版权或大模型生成逻辑。

```text
SQLBot 用户 → 工作空间/数据源权限 → SQLBot 生成 SQL
  → 固定工作空间与数据源映射 → AgentSQL Agent Key
  → MCP → 权限/规则 → 受控执行 → 脱敏 → 持久审计 → SQLBot 图表数据
```

首期适配 PostgreSQL 数据源、简单标识符和明确 schema。查询必须满足 AgentSQL 现有 SELECT 子集与权限；B2/B5 和数据库支持边界不因 SQLBot 接入而扩大。适配器没有业务库账号，也不建立业务库直连；缺失映射、撤销 Key、拒绝/告警/需审批/未知决策、无审计 ID 或网关不可用均失败关闭。它不自动重试 SQL，不接入写入和审批执行，不转发 SQLBot 密码。

SQLBot 内部元数据 PostgreSQL 仍是 SQLBot 自身存储。网关的 Agent 身份对应运营者配置的 SQLBot 工作空间和数据源，不等同于每个 SQLBot 最终用户的个人身份；需要用户级授权时必须配置独立映射与身份方案。外部动态数据源、Excel 业务读取及未配置数据源在此部署中拒绝直连。

## 1. 准备 AgentSQL

1. 使用 v3.0.2 或后续兼容版本的 AgentSQL，为专用只读数据库账号登记 PostgreSQL 数据源。
2. 创建专用只读 Agent，按所需 schema、表、列授予最小权限，配置脱敏规则和持久审计。不要为接入授予 `*`，不要使用控制台管理员 token。
3. 先用 MCP Inspector 验证 `list_datasources`、`list_schema`、一条允许 SELECT 和一条越权拒绝。`list_schema` 只对已授权列输出 `data_type`。
4. 将 Agent Key 放入 SQLBot 服务端的环境或 secrets 管理系统；不写进 SQLBot 页面、源码、镜像、URL 或配置 JSON。网关提供校验有效证书的 HTTPS；仅同机隔离测试允许回环 HTTP。

## 2. 构建派生镜像

在 AgentSQL 仓库根目录执行：

```bash
docker build -f integrations/sqlbot/Dockerfile -t agentsql-sqlbot:1.10.2 .
```

安装器只接受已核验的上游 `apps/db/db.py` 哈希，并在镜像内保留原文件备份；版本或文件不符立即停止。它替换六个高层入口，并阻断七个底层业务连接入口。不要跳过哈希检查或用旧安装器强行支持其他 SQLBot 版本。派生镜像遵守 SQLBot 上游许可、Logo 和版权要求；本项目没有把上游代码重新授权为 Apache-2.0。

## 3. 配置映射并部署

从 [config.example.json](../integrations/sqlbot/config.example.json) 复制配置。`workspace_id` 为 SQLBot 工作空间 ID，`datasource_name` 与页面中的名称精确一致，`datasource_id` 为 AgentSQL 数据源 ID。它们由服务端配置锁定；跨工作空间或重名的歧义映射会拒绝。

```json
{
  "version": 1,
  "endpoint": "https://your-agentsql.example/mcp",
  "timeout_seconds": 30,
  "min_interval_seconds": 0.6,
  "bindings": [{
    "workspace_id": 1,
    "datasource_name": "AgentSQL PostgreSQL",
    "dialect": "pg",
    "datasource_id": "ds-example",
    "schema": "public",
    "key_env": "AGENTSQL_SQLBOT_READONLY_KEY",
    "numeric_columns": ["total", "amount"]
  }]
}
```

将文件只读挂载到 SQLBot，例如 `/run/agentsql-sqlbot/config.json`，并设置 `AGENTSQL_SQLBOT_CONFIG` 为该路径；通过服务端环境提供 `AGENTSQL_SQLBOT_READONLY_KEY`。私有 CA 可在配置中指定只读 `ca_file`，不能关闭 TLS 校验。限流间隔是单进程内的保守请求节奏，多 worker 共享 Agent 时仍须按网关限流规划；HTTP 429 不自动重试。

沿用 SQLBot 官方部署的持久化目录与模型配置，把基础镜像替换为上面构建的派生镜像。限制 SQLBot 到业务库的网络直连，只允许其到 AgentSQL 的 HTTPS 和自身元数据存储；对外提供 SQLBot 时另行配置 TLS、账号和访问控制。

## 4. 在 SQLBot 中建立数据源

创建 PostgreSQL 数据源，工作空间及名称与映射一致；schema 填配置的 schema。连接表单中的业务库 host、用户名、密码不用于适配器，可使用 `agentsql.invalid` 与无权限占位值，不保存真实业务凭据。连接检查、选表与字段发现会调用网关；改名或更换工作空间后必须由运营者同步服务端映射。

正常查询返回 SQLBot 的 `fields`、`data`、`fields_info` 和 SQL 编码，并额外带 `agentsql.audit_id` 供关联。被脱敏成字符串的数值不会被错误标为数字；重复输出列名需使用唯一别名，避免图表结果覆盖。

网关当前行结果使用字符串。需要数值图表时，在服务端映射的 `numeric_columns` 中显式列出查询输出别名；适配器仅转换这些别名下可精确表示的有限数值。手机号、编号等默认保留字符串；脱敏值、混合类型及超出安全精度的数值不会强制转换。未声明的数值列仍可展示为表格文本。

## 验收与排查

必须对实际部署执行允许查询、越权拒绝、字段裁剪、敏感值脱敏与持久审计核对；再撤销 Agent Key、移除映射、断开网关，确认没有直连或自动重试。修改权限后重新发现并刷新 SQLBot 中缓存的表/字段元数据。

适配器错误只输出稳定错误和适用时的审计 ID，不回显 Key、DSN、原始响应或结果行。SQLBot 自身可能记录生成 SQL，需独立配置其日志访问和保留策略。官方源代码、镜像实际摘要、实测环境和未测项记录在本版验收报告；没有模型账号的执行层验证不等同于完整自然语言问数或所有图表类型验收。

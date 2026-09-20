# AgentSQL MCP 接入指南

> 发布状态：AgentSQL v0.3.0 即将发布。一键安装命令与 `ghcr.io/cuipengdba/agentsql` 镜像将在发布日可用；发布前可按本文「源码 / Live Demo」路径从本地构建体验。源码仓库为 `github.com/cuipengdba/agentsql`。

本文说明如何把 MCP 客户端接到 AgentSQL。身份、授权、规则、审批与能力边界见 [使用手册](USER_GUIDE.md)，首次部署见 [快速上手](GETTING_STARTED.md)；把决策事件外发到 Webhook、告警群或 Syslog 见 [通知外发指南](NOTIFICATIONS.md)。

## 选择传输方式

### Streamable HTTP：已运行服务时首选

安装器、systemd 或 Compose 已经运行 AgentSQL 时，直接连接：

- 端点：`http://127.0.0.1:7780/mcp`
- 身份头：`Authorization: Bearer <Agent API Key>`
- MCP 协议版本：`2025-06-18`
- `Accept`：`application/json, text/event-stream`
- 模式：stateless
- 单请求体上限：4 MiB

Agent API Key 不是控制台管理员 token。Agent 被禁用、过期、删除或轮换 Key 后，旧 Key 立即失效。

服务默认应只绑定回环地址。远程使用时，选择 SSH 本地转发或受控 TLS 反向代理；禁止让 `0.0.0.0:7780` 在公网裸奔。

```bash
# 适用前提：远端 AgentSQL 仅监听 127.0.0.1:7780；本机已配置 SSH 身份
ssh -N -L 7780:127.0.0.1:7780 user@agentsql-host
```

随后客户端仍连接 `http://127.0.0.1:7780/mcp`。使用反向代理时必须配置 TLS、访问控制和限流；不要记录 `Authorization` 头。

### stdio：单机桌面 Agent

stdio 会启动一套新的 AgentSQL 运行时：

```bash
# 适用前提：本机已安装 agentsql；当前用户可读绝对路径配置及控制面存储
AGENTSQL_SECRET='<与控制面配对的恰好 32 字节值>' \
AGENTSQL_API_KEY='<Agent API Key>' \
agentsql mcp --config /absolute/path/config.yaml
```

`--config` 可缩写为 `-c`，默认是当前目录的 `config.yaml`。Key 优先通过 `AGENTSQL_API_KEY` 提供；不要把 Key 放进 `--api-key`，否则会出现在进程参数中。stdio 与 HTTP 使用同一套七工具。

> **重要：stdio 不是连接已运行 systemd 服务的代理。** 它会新建一套运行时，必须能读取配置、metadata/audit 控制面存储，并使用与加密数据源凭据相同的 `AGENTSQL_SECRET`。安装器生成的 `/etc/agentsql/agentsql.env` 权限为 `0600 root:root`，普通桌面用户读不到；已经安装并运行服务时请直接使用 HTTP。

stdio 模式会关闭控制台与事件流；不要期待它提供控制台 UI。

## 七个 MCP 工具

所有 SQL 参数都只接受单条语句，不允许堆叠多语句。客户端必须读取结构化结果中的 `decision`。

| 工具 | 用途 | 入参 | 可能的 decision 与关键语义 |
| --- | --- | --- | --- |
| `list_datasources` | 列出当前 Agent 已获授权的数据源 | 无 | 成功返回公开标识信息，不返回 host、密码或连接参数；失败为 `error` |
| `list_schema` | 读取已授权结构并按列权限裁剪 | `datasource_id`；`table` 可选 | 成功通常为 `allow`；`table` 只接受字母、数字、下划线，以及至多一个点的 `table` 或 `schema.table`，不支持引号和特殊标识符；结果超过 10000 行时必须缩小到具体表 |
| `explain_query` | 解析、评估并做只读 EXPLAIN，不执行 SQL | `datasource_id`、`sql` | 可返回 `allow`、`deny`、`warn`、`approve` 或 `error`；即使 `allow` 也没有执行 SQL |
| `query` | 执行受保护的单条 `SELECT` | `datasource_id`、`sql` | 仅查询；结果自动按数据源 `row_limit` 限行并做结果层脱敏；危险查询可 `deny` 或 `approve` |
| `execute_write` | 提交单条 `INSERT`、`UPDATE`、`DELETE` 或 DDL | `datasource_id`、`sql`、必填 `reason` | 管理类语句不走此工具；DDL 要求 Agent 为 `ddl`；`allow`/`warn` 才执行，`approve` 会自动创建 pending 审批单但不执行 |
| `request_approval` | 主动把 SQL 送审 | `datasource_id`、`sql`、必填 `reason` | 只创建审批单，不执行 SQL；返回 `approval_id` |
| `get_approval_result` | 查询审批状态 | `approval_id` | 只能查询当前 Agent 自己的单；其他 Agent 的单号按未找到处理 |

`execute_write` 和 `request_approval` 的调用方 `reason` 当前会写日志；审批单保存的是规则评估原因，不是调用方原文。

## 调用时序

### 只读查询

```text
list_datasources
  → list_schema
  → explain_query
  → query
```

先确认数据源和可见结构，再评估，最后执行。`explain_query` 的 `allow` 只说明评估通过，不等于已经查询。

### 写操作：自动转审批

```text
execute_write(reason 必填)
  → decision="approve" + approval_id（系统已自动创建 pending 单）
  → 管理员在控制台审批
  → get_approval_result
  → approved 后由库外人工或另一个具备权限的流程执行
```

收到 `execute_write` 返回的 `approval_id` 后，不要再调用 `request_approval`，否则会重复送审。

审批不是执行器，也不是豁免票据。`approved` 只表示人工决策记录已通过：AgentSQL 不会自动执行原 SQL，也不会解锁或消费下一次 `execute_write`。如仍需变更，由管理员在库外执行，或让具备权限的独立流程重新发起并接受新的规则判断。

### 写操作：主动送审

```text
request_approval(reason 必填)
  → approval_id
  → 管理员在控制台审批
  → get_approval_result
```

主动送审适用于调用方明确希望先取得人工意见的场景；它从不执行 SQL。

## 正确处理返回值

- `decision:"allow"`：允许，执行类工具已经按其语义执行。
- `decision:"warn"`：成功返回并带告警；执行类工具可已执行，仍需记录警告。
- `decision:"deny"`：策略或规则拒绝；是成功返回的工具结果，不是传输错误。
- `decision:"approve"`：需要人工处理；SQL 没有执行。
- `decision:"error"`：处理失败；这是唯一会设置 MCP `IsError=true` 的决策。

因此，不要把 `deny`、`warn` 或 `approve` 当成网络错误自动重试。特别是写操作，盲目重试会产生重复审批或重复业务意图。

## 客户端配置骨架

以下 JSON 是 AgentSQL 连接字段骨架。Cursor、Claude Desktop、Cline 的配置文件路径、HTTP transport 键名（包括某些版本要求的 `type` 或 `transport`）以及 HTTP 支持版本，必须以各客户端官方 MCP 文档为准；不要根据本文猜测路径或版本。

### stdio 骨架

```json
{
  "mcpServers": {
    "agentsql": {
      "command": "agentsql",
      "args": ["mcp", "--config", "/absolute/path/config.yaml"],
      "env": {
        "AGENTSQL_SECRET": "<与控制面配对的恰好 32 字节值>",
        "AGENTSQL_API_KEY": "<Agent API Key>"
      }
    }
  }
}
```

前提：桌面客户端启动的进程能读取该绝对路径、控制面存储和相同的 SECRET。优先使用客户端的秘密注入能力，不要把真实值提交到配置仓库。

### Streamable HTTP 骨架

```json
{
  "mcpServers": {
    "agentsql": {
      "url": "http://127.0.0.1:7780/mcp",
      "headers": {
        "Authorization": "Bearer <Agent API Key>"
      }
    }
  }
}
```

- Cursor：使用该版本官方 MCP 文档确认 `mcpServers` 的配置文件位置和 HTTP transport 字段。
- Claude Desktop：使用官方 MCP 文档确认该版本是否支持 Streamable HTTP；若只支持 stdio，使用上面的 stdio 骨架。
- Cline：使用官方 MCP 文档确认配置入口、`mcpServers` 结构及 HTTP transport 字段。

这些客户端的 `command`、`args`、`env`（stdio）和 `url`、`headers`（HTTP）是 AgentSQL 对接所需信息；外层结构变化以客户端为准。

## 自研客户端

### initialize 裸 HTTP 示例

先设置 Key；以下请求适用于本机已运行的 Streamable HTTP 服务：

```bash
# 适用前提：本机 AgentSQL 已运行；AGENTSQL_API_KEY 已导出为有效 Agent Key
curl -fsS http://127.0.0.1:7780/mcp \
  -H "Authorization: Bearer ${AGENTSQL_API_KEY}" \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -H 'MCP-Protocol-Version: 2025-06-18' \
  --data '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"agentsql-smoke","version":"1.0.0"}}}'
```

服务是 stateless，不要依赖跨请求服务端会话。每个请求都发送 Bearer、正确的 `Accept` 和协议版本头，并把请求体控制在 4 MiB 内。

### Go SDK

建议使用 `github.com/modelcontextprotocol/go-sdk` 的 `StreamableClientTransport`，通过自定义 `http.RoundTripper` 为每次请求注入：

```text
Authorization: Bearer <Agent API Key>
MCP-Protocol-Version: 2025-06-18
Accept: application/json, text/event-stream
```

不要在日志中输出完整 Header，也不要把 `deny` 当作 transport error。

## 故障排查

### `401 unauthorized`

检查是否误用了管理员 token，或 Agent Key 是否错误。Agent 被禁用、过期、删除、轮换后，旧 Key 都会失效；轮换不会保留并行有效期。

### stdio 没有控制台 UI

这是预期行为。stdio 会关闭 `console_enabled` 与事件流。已安装服务应使用 HTTP，让控制台继续由 systemd 运行实例提供。

### 只能在服务器本机访问

默认回环绑定是安全设计。使用 SSH `-L`，或在受控 TLS 反代后访问；不要改成公网 `0.0.0.0` 裸奔。

### MCP 握手失败

确认端点是 `/mcp`，请求包含 `Content-Type: application/json`、`Accept: application/json, text/event-stream` 和 `MCP-Protocol-Version: 2025-06-18`。同时检查客户端是否真的支持 Streamable HTTP，以及请求体是否超过 4 MiB。

### 堆叠多语句被拒绝

这是 R001 的预期行为。一次只发送一条 SQL；不要在合法查询后附加第二条语句。

### 写操作提示缺少 `reason`

`execute_write` 与 `request_approval` 都要求非空 `reason`。这类工具层前置拒绝发生在进入流水线之前，可能没有审计记录；补充目的、影响范围和回滚方案后重新提交。

### stdio 连不上控制面或无法解密数据源

检查进程是否能读配置和 SQLite/PG 控制面，是否使用与数据源密文配对的同一个 `AGENTSQL_SECRET`。安装器的 `/etc/agentsql/agentsql.env` 是 `0600 root:root`；普通桌面用户读不到时不要复制或放宽生产秘密文件，改用已运行服务的 HTTP 端点。

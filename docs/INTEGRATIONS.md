# AgentSQL MCP 接入指南

> 发布状态：本文对应 AgentSQL v0.4.0。一键安装命令与 `ghcr.io/cuipengdba/agentsql:v0.4.0` 镜像可用于部署，也可按本文「源码 / Live Demo」路径从本地构建体验。源码仓库为 `github.com/cuipengdba/agentsql`。

本文说明如何把 MCP 客户端接到 AgentSQL。身份、授权、规则、审批与能力边界见 [使用手册](USER_GUIDE.md)，首次部署见 [快速上手](GETTING_STARTED.md)；把决策事件外发到 Webhook、告警群或 Syslog 见 [通知外发指南](NOTIFICATIONS.md)。

## 选择传输方式

### Streamable HTTP：已运行服务时首选

安装器、systemd 或 Compose 已经运行 AgentSQL 时，直接连接：

- 端点：`http://127.0.0.1:7780/mcp`
- 身份头：`Authorization: Bearer <Agent API Key>`
- MCP 协议版本：`2025-06-18`
- `Accept`：`application/json, text/event-stream`
- 模式：默认 stateful（initialize 返回 `Mcp-Session-Id`）；可显式回退 stateless
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

`--config` 可缩写为 `-c`，默认是当前目录的 `config.yaml`。Key 优先通过 `AGENTSQL_API_KEY` 提供；不要把 Key 放进 `--api-key`，否则会出现在进程参数中。stdio 与 HTTP 使用同一套工具注册规则：始终提供 7 个基础工具，默认开启 B5 时再提供 8 个会话/事务工具。

> **重要：stdio 不是连接已运行 systemd 服务的代理。** 它会新建一套运行时，必须能读取配置、metadata/audit 控制面存储，并使用与加密数据源凭据相同的 `AGENTSQL_SECRET`。安装器生成的 `/etc/agentsql/agentsql.env` 权限为 `0600 root:root`，普通桌面用户读不到；已经安装并运行服务时请直接使用 HTTP。

stdio 模式会关闭控制台与事件流；不要期待它提供控制台 UI。

## MCP 工具

所有 SQL 参数都只接受单条语句，不允许堆叠多语句。客户端必须读取结构化结果中的 `decision`。

以下 7 个基础工具始终注册：

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

v0.4.0 的 B5 跨请求逻辑会话与 PostgreSQL 计划事务出厂默认开启，因此默认 `tools/list` 还会返回以下 8 个工具：

| 工具 | 用途 |
| --- | --- |
| `open_session` / `close_session` / `get_session_status` | 创建、关闭和查询显式 AgentSQL 逻辑会话；不要把它与 HTTP `Mcp-Session-Id` 传输会话混用 |
| `begin_transaction` | 预检并封存完整有序 DML 计划，然后开始 PostgreSQL 事务；此步不执行语句 |
| `execute_transaction_statement` | 按计划 ordinal 执行一条已封存的 `INSERT`、`UPDATE` 或 `DELETE`，执行阶段不能替换 SQL |
| `commit_transaction` / `rollback_transaction` | 提交或回滚并完成终态栅栏 |
| `get_transaction_status` | 幂等查询事务 outcome、audit 与 disposition 状态 |

B5 每个 operation 仍只允许一条顶层 SQL，不支持事务内 `SELECT`、`RETURNING`、DDL 或 MySQL 跨请求事务。显式设置 `mcp.sessions.enabled: false` 会隐藏这 8 个工具，并要求同时关闭 `mcp.transactions.postgres`；实际可用集合始终以 `tools/list` 为准。

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

## Claude Desktop 接入（stdio）

Claude Desktop 使用 `claude_desktop_config.json` 中的 `mcpServers` 启动本机进程。`command` 建议填写 `agentsql` 可执行文件的绝对路径，避免桌面应用与终端的 `PATH` 不一致；Windows JSON 路径中的反斜杠需要写成 `\\`。

```json
{
  "mcpServers": {
    "agentsql": {
      "command": "/absolute/path/to/agentsql",
      "args": ["mcp", "--config", "/absolute/path/config.yaml"],
      "env": {
        "AGENTSQL_SECRET": "<与控制面配对的恰好 32 字节值>",
        "AGENTSQL_API_KEY": "<Agent API Key>"
      }
    }
  }
}
```

上述参数已经由仓库 CLI 实现确认：子命令是 `mcp`，配置参数是 `--config`（可缩写为 `-c`）。保存配置并重启 Claude Desktop 后，默认配置应能看到 7 个基础工具和 8 个 B5 工具；显式关闭 B5 时只显示基础工具。若没有出现，先确认桌面客户端启动的进程能够读取可执行文件、配置文件、metadata/audit 控制面存储，并使用与加密数据源凭据相同的 `AGENTSQL_SECRET`。

## Cursor 接入（stdio）

Cursor 可把相同的 `mcpServers` 片段放入用户级 `~/.cursor/mcp.json`，或项目级 `.cursor/mcp.json`。团队项目优先只提交不含秘密的配置模板；真实 Key 和 SECRET 使用 Cursor 当前版本提供的秘密注入方式或仅保存在未纳入版本控制的本地配置中。

```json
{
  "mcpServers": {
    "agentsql": {
      "command": "/absolute/path/to/agentsql",
      "args": ["mcp", "--config", "/absolute/path/config.yaml"],
      "env": {
        "AGENTSQL_SECRET": "<与控制面配对的恰好 32 字节值>",
        "AGENTSQL_API_KEY": "<Agent API Key>"
      }
    }
  }
}
```

项目级文件中的相对路径容易受工作目录影响，`command` 与 `--config` 的值均建议使用绝对路径。保存后按当前 Cursor 版本的方式刷新 MCP Server；若客户端版本的配置入口或外层字段发生变化，以 Cursor 官方 MCP 文档为准。

## 豆包及其他远程 MCP 客户端（Streamable HTTP）

客户端必须明确支持 **Streamable HTTP**，并允许为 MCP Server 添加自定义 Header。在豆包或其他客户端的 MCP Server 配置页填写：

| 配置项 | 公网演示 | 生产自托管 |
| --- | --- | --- |
| Server URL | `https://demo.agentsql.cn/mcp` | `https://<你的网关>/mcp` |
| Header 名 | `Authorization` | `Authorization` |
| Header 值 | `Bearer <演示环境 Agent API Key>` | `Bearer <自托管 Agent API Key>` |

演示 Key 是 Agent API Key，不是演示控制台的管理员 token；使用登录页预填的演示管理员账号时，应确认所选演示 Agent 对应的当前只读 Key。若演示 Key 不公开，则改用自托管环境验证，不要猜测或复用管理员 token。演示环境仅限本地接入测试，生产必须自托管并使用自己的密钥。若某客户端不支持自定义 Header、只支持旧版 SSE，或无法确认其 Streamable HTTP 版本，则不能按此方式直连；不要把 Key 放入 URL 查询参数。

下面是通用字段骨架，豆包及其他客户端的配置入口、外层 JSON 结构，以及某些版本要求的 `type` 或 `transport` 字段，以该客户端当前官方文档为准：

```json
{
  "mcpServers": {
    "agentsql": {
      "url": "https://demo.agentsql.cn/mcp",
      "headers": {
        "Authorization": "Bearer <Agent API Key>"
      }
    }
  }
}
```

已运行本机 AgentSQL 服务时，也可把 URL 换成 `http://127.0.0.1:7780/mcp`。非本机生产部署必须使用受控的 HTTPS 入口，不得把监听端口直接裸露到公网。

- Cursor：远程接入时，使用该版本官方 MCP 文档确认 HTTP transport 字段。
- Claude Desktop：使用官方 MCP 文档确认该版本是否支持 Streamable HTTP；若只支持 stdio，使用前面的 Claude Desktop 配置。
- Cline：使用官方 MCP 文档确认配置入口、`mcpServers` 结构及 HTTP transport 字段。

这些客户端的 `command`、`args`、`env`（stdio）和 `url`、`headers`（HTTP）是 AgentSQL 对接所需信息；外层结构与 transport 声明以客户端当前版本为准。

## 从 MCP Registry / Glama 查找

AgentSQL 已收录于 MCP 官方 Registry（状态为 active）。在 MCP Registry 搜索 **AgentSQL**，核对条目名称 `io.github.cuipengdba/agentsql`、版本和仓库地址后，再按客户端支持的方式接入；仓库没有给出可长期依赖的 Registry 条目直达 URL，因此本文不编造链接。

Glama 也已收录 AgentSQL，可在 [Glama 的 AgentSQL 搜索结果](https://glama.ai/mcp/servers?query=AgentSQL) 中查找。第三方目录中的端点、版本或 Header 示例可能滞后，最终以本仓库文档、实际部署配置和 MCP `tools/list` 返回为准。

## 客户端接入安全提醒

- 公网 demo 只连接合成演示库，用于功能体验和客户端连通性测试；不要向 demo 发送真实业务 SQL、数据或凭据。
- 生产环境必须自托管 AgentSQL，并使用自己的数据源、Agent 与 API Key；网关前配置 TLS、访问控制和限流。
- API Key、`AGENTSQL_SECRET` 和数据源口令不得写进公开配置、截图、日志或代码仓库。公开项目中只保留占位符，并在泄露后立即轮换。
- stdio 配置会在本机新建 AgentSQL 运行时；若已有受控服务，优先连接其 Streamable HTTP 端点，避免复制或放宽生产秘密文件权限。
- 客户端仅应获得完成任务所需的最小 Agent 能力和数据源授权；始终处理 `deny`、`warn`、`approve`、`error`，不要把安全决策当作网络失败自动重试。

## 自研客户端

### initialize 裸 HTTP 示例

先设置 Key；以下请求适用于本机已运行的 Streamable HTTP 服务：

```bash
# 适用前提：本机 AgentSQL 已运行；AGENTSQL_API_KEY 已导出为有效 Agent Key
curl -fsS -D /tmp/agentsql-mcp-headers http://127.0.0.1:7780/mcp \
  -H "Authorization: Bearer ${AGENTSQL_API_KEY}" \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -H 'MCP-Protocol-Version: 2025-06-18' \
  --data '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"agentsql-smoke","version":"1.0.0"}}}'
```

Streamable HTTP 传输默认是有状态的。读取 initialize 响应中的 `Mcp-Session-Id`，并在后续 `tools/list`、`tools/call`、GET 和 DELETE 请求中原样携带；服务端仍会对每个请求重新校验 Bearer。`mcp.http.stateful: false` 只用于兼容性回退，此时不会返回会话头且只接受 POST。无论哪种模式，每个请求都要发送 Bearer、正确的 `Accept` 和协议版本头，并把请求体控制在 4 MiB 内。

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

确认端点是 `/mcp`，请求包含 `Content-Type: application/json`、`Accept: application/json, text/event-stream` 和 `MCP-Protocol-Version: 2025-06-18`。默认有状态模式下还要确认 initialize 响应包含 `Mcp-Session-Id`，且后续请求携带相同的值；同时检查客户端是否真的支持 Streamable HTTP，以及请求体是否超过 4 MiB。

### 堆叠多语句被拒绝

这是 R001 的预期行为。一次只发送一条 SQL；不要在合法查询后附加第二条语句。

### 写操作提示缺少 `reason`

`execute_write` 与 `request_approval` 都要求非空 `reason`。这类工具层前置拒绝发生在进入流水线之前，可能没有审计记录；补充目的、影响范围和回滚方案后重新提交。

### stdio 连不上控制面或无法解密数据源

检查进程是否能读配置和 SQLite/PG 控制面，是否使用与数据源密文配对的同一个 `AGENTSQL_SECRET`。安装器的 `/etc/agentsql/agentsql.env` 是 `0600 root:root`；普通桌面用户读不到时不要复制或放宽生产秘密文件，改用已运行服务的 HTTP 端点。

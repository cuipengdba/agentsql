# MCP Streamable HTTP 会话修复设计

## 范围与现状

本设计只处理 MCP server 的传输会话、路由与错误响应，不改变 stdio / Streamable HTTP 两种既有协议形态，也不改变数据库查询、授权、审计或脱敏链路。

AgentSQL 当前有两类彼此独立的 session：

- MCP transport session：仅用于 Streamable HTTP，由 `Mcp-Session-Id` 标识。默认启用；可用 `mcp.http.stateful: false` 显式回退到 stateless。
- AgentSQL B5 logical session：由 `open_session` 创建，`session_id` 和 continuation proof 用于跨请求事务。它不是 transport session，不能把 `Mcp-Session-Id` 作为工具参数传入。

stdio 以一个进程连接作为一个 MCP 会话，不在协议线上使用 `Mcp-Session-Id`。HTTP `/mcp` 保持 Streamable HTTP；响应可使用 JSON，GET 可建立该传输定义的 SSE 流，但本次不新增已废弃的 HTTP+SSE 双端点传输。

## 调研结论与缺口

HTTP transport 使用仓库锁定的 `github.com/modelcontextprotocol/go-sdk`。SDK 在内存 map 中保存活动会话，默认用 `crypto/rand.Text` 生成全局唯一、可见 ASCII 的随机 session ID，并负责并发访问、DELETE 终止和 idle timeout 清理。AgentSQL 在 SDK 外层完成 Agent API Key 认证、每 Agent 限流、协议版本门禁和请求体限制。

会话建立时保存的 SDK `UserID` 是 Agent ID 与 API Key hash 的组合。后续请求先重新认证，再由 SDK 比对 `UserID`；因此其他 Agent 或轮换后的旧 Key 不能复用已有会话。日志只记录 HTTP 方法、路径、Agent ID、状态码和耗时，不记录 API Key、Authorization header 或 session ID。

已确认的兼容缺口是：旧门禁要求首次 `initialize` POST 也携带 `MCP-Protocol-Version`。MCP 2025-06-18 要求该头出现在初始化之后的请求中；initialize 自身通过 `params.protocolVersion` 协商。部分标准客户端不会在首次请求预先发送该头，因而会在 SDK 建会话前被拒绝。

## 目标流程

1. 客户端发送无 `Mcp-Session-Id` 的 `initialize`。`MCP-Protocol-Version` 在此请求上可省略；若提供，必须是服务端固定支持的 `2025-06-18`。`params.protocolVersion` 仍必须通过现有白名单。
2. 服务端认证 Agent，创建绑定该 Agent/Key 的 SDK session，在 initialize HTTP 响应中返回 `Mcp-Session-Id`。
3. 客户端发送 `notifications/initialized`、`tools/list`、`tools/call` 等后续 POST，并同时携带 Bearer、`MCP-Protocol-Version: 2025-06-18` 和原 session ID。
4. 多个 session 可并发使用，状态按随机 session ID 隔离。删除或超时一个 session 不影响其他 session。
5. 每个完成的 POST 重置 idle timeout。默认值为 600000 ms，可配置为正数且最大 1800000 ms。
6. 客户端用带 session ID 的 DELETE 主动关闭会话。进程重启后内存会话全部失效，客户端必须重新 initialize。

## 错误契约

会话错误遵循 MCP 2025-06-18 Streamable HTTP 的 HTTP 语义：

| 条件 | 响应 | 客户端动作 |
| --- | --- | --- |
| 非 initialize POST 缺少 session ID | HTTP 400 | 修正握手流程；不得静默创建新会话 |
| session ID 未知、已 DELETE、已超时或服务重启后失效 | HTTP 404 | 不复用旧 ID；发送无 session ID 的新 initialize |
| session 属于其他 Agent/Key | HTTP 403 | 拒绝复用；检查客户端凭据 |
| Bearer 缺失、失效或 Agent 不可用 | HTTP 401 | 重新取得有效 Agent Key |
| 后续 POST 缺失、重复或携带非白名单协议版本头 | 现有 `MCP_PROTOCOL_UNSUPPORTED` 协议门禁响应 | 使用协商后的 `2025-06-18` |

规范对失效会话明确要求 HTTP 404，因此这里不把 transport 错误伪装成 JSON-RPC `-32600`。进入 SDK 后的合法 JSON-RPC 请求仍由 SDK 按 MCP/JSON-RPC 格式响应。所有认证和会话不匹配路径均 fail-closed。

## 兼容矩阵

| 场景 | 计划行为 | 验证方式 |
| --- | --- | --- |
| stdio 单连接 | 保持既有进程级会话，不使用 session header | SDK in-process / stdio 单测 |
| HTTP 单会话 | initialize 建会话，后续 list/call 复用 | HTTP handler 单测 |
| HTTP 多会话 | ID 唯一；并发请求和关闭相互隔离 | 并发 handler 单测 |
| idle timeout | 到期清理，旧 ID 返回 404 | 短 timeout 单测 |
| 主动关闭 | DELETE 返回 204，之后旧 ID 返回 404 | handler 单测 |
| 服务重启 | 内存 session 不恢复，旧 ID 返回 404 | 重建 handler 单测 |
| 显式 stateless | 无 session header；只接受 POST | handler 单测 |
| 跨 Agent/Key 复用 | 返回 403 | handler 单测 |
| 豆包 Streamable HTTP | 标准流程应可连接 | 待真实账号/客户端实测 |
| Claude Desktop stdio | 保持现有配置和传输 | 本地 stdio 测试；真实客户端待实测 |
| Claude / Inspector Streamable HTTP | 标准流程应可连接；具体客户端版本能力不同 | 待对应真实版本实测 |

## 本次实现边界

本次修复：默认 stateful、可配置 idle timeout、initialize 返回 ID、后续请求校验、认证身份绑定、无效/过期/重启失效响应、显式 stateless 回退，以及首次 initialize 可不带协议版本头。

不在本次范围：迁移到其他传输、跨进程持久化或共享 session store、恢复服务重启前会话、SSE event store/断线重放、数据库风控行为、前端，以及任何客户端私有握手扩展。豆包和各版本 Claude 的真实行为没有本地账号/运行环境证据，均标记为待实测，不据此添加非标准分支。


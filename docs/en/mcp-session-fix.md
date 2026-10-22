# MCP Streamable HTTP session repair design

**Language:** [English](mcp-session-fix.md) | [Simplified Chinese](../mcp-session-fix.md)

## Scope and current situation

This design only handles the transmission session, routing and error response of the MCP server. It does not change the two existing protocol forms of stdio / Streamable HTTP, nor does it change the database query, authorization, auditing or masked links.

AgentSQL currently has two types of sessions that are independent of each other:

- MCP transport session: For Streamable HTTP only, identified by `Mcp-Session-Id`. Enabled by default; use `mcp.http.stateful: false` to explicitly fall back to stateless.
- AgentSQL B5 logical session: Created by `open_session`, `session_id` and continuation proof are used for cross-request transactions. It is not a transport session and `Mcp-Session-Id` cannot be passed in as a tool parameter.

stdio connects as a process as an MCP session and does not use `Mcp-Session-Id` on the protocol line. HTTP `/mcp` remains Streamable HTTP; the response can use JSON, and GET can establish the SSE stream defined by this transport, but the abandoned HTTP+SSE dual endpoint transport will not be added this time.

## Research conclusions and gaps

The HTTP transport uses repository-locked `github.com/modelcontextprotocol/go-sdk`. The SDK saves active sessions in the memory map, uses `crypto/rand.Text` by default to generate a globally unique, visible ASCII random session ID, and is responsible for concurrent access, DELETE termination and idle timeout cleanup. AgentSQL completes Agent API Key authentication, per-Agent current limiting, protocol version access control, and request body restriction at the outer layer of the SDK.

The SDK `UserID` saved when the session is established is a combination of Agent ID and API Key hash. Subsequent requests are re-authenticated first, and then compared with `UserID` by the SDK; therefore, other Agents or old keys after rotation cannot reuse existing sessions. The log only records the HTTP method, path, Agent ID, status code and time consumption, but does not record the API Key, Authorization header or session ID.

Confirmed compatibility gaps are: Legacy access control requires first `initialize` POST to also carry `MCP-Protocol-Version`. MCP 2025-06-18 requires this header to be present in requests after initialization; initialize itself is negotiated via `params.protocolVersion`. Some standard clients do not pre-send this header on the first request and will therefore be rejected before the SDK establishes a session.

## target process

1. Client sends `initialize` without `Mcp-Session-Id`. `MCP-Protocol-Version` can be omitted on this request; if provided, it must be `2025-06-18` that is fixedly supported by the server. `params.protocolVersion` must still pass the existing whitelist.
2. The server authenticates the Agent, creates an SDK session bound to the Agent/Key, and returns `Mcp-Session-Id` in the initialize HTTP response.
3. The client sends subsequent POSTs such as `notifications/initialized`, `tools/list`, `tools/call`, etc., and carries Bearer, `MCP-Protocol-Version: 2025-06-18` and the original session ID at the same time.
4. Multiple sessions can be used concurrently, and the status is isolated by random session ID. Deleting or timing out a session does not affect other sessions.
5. Each completed POST resets the idle timeout. Default value is 600000 ms, configurable to positive number up to 1800000 ms.
6. The client actively closes the session using DELETE with the session ID. After the process is restarted, all memory sessions will become invalid, and the client must re-initialize.

## wrong contract

Session errors follow the HTTP semantics of MCP 2025-06-18 Streamable HTTP:

| Conditions | response | client action |
| --- | --- | --- |
| Non-initialize POST missing session ID | HTTP 400 | Fix handshake flow; new sessions must not be created silently |
| The session ID is unknown, has been DELETE, has timed out, or will expire after the service is restarted. | HTTP 404 | Do not reuse old ID; send new initialize without session ID |
| session belongs to other Agent/Key | HTTP 403 | Deny reuse; check client credentials |
| Bearer is missing, invalid or Agent is unavailable | HTTP 401 | Retrieve a valid Agent Key |
| Subsequent POSTs are missing, duplicated, or carry non-whitelisted protocol version headers | Existing `MCP_PROTOCOL_UNSUPPORTED` protocol access control response | Use negotiated `2025-06-18` |

The specification explicitly requires HTTP 404 for expired sessions, so the transport error is not disguised as JSON-RPC `-32600` here. Legal JSON-RPC requests after entering the SDK are still responded to by the SDK in MCP/JSON-RPC format. All authentication and session mismatch paths are fail-closed.

## Compatibility matrix

| scene | planned behavior | Verification method |
| --- | --- | --- |
| stdio single connection | Keep existing process-level sessions without using session headers | SDK in-process/stdio single test |
| HTTP single session | initialize creates a session and reuses it in subsequent list/call | HTTP handler single test |
| HTTP multi-session | ID is unique; concurrent requests and shutdowns are isolated from each other | Concurrent handler single test |
| idle timeout | Expiration cleanup, old ID returns 404 | Short timeout single test |
| Actively close | DELETE returns 204, then the old ID returns 404 | handler single test |
| Service restart | Memory session is not restored, old ID returns 404 | Rebuild handler single test |
| explicit stateless | No session header; only accepts POST | handler single test |
| Cross-Agent/Key reuse | Return 403 | handler single test |
| Beanbao Streamable HTTP | Standard processes should be connectable | To be tested on real accounts/clients |
| Claude Desktop stdio | Keep existing configuration and transport | Local stdio test; real client to be tested |
| Claude / Inspector Streamable HTTP | Standard processes should be connectable; specific client versions have different capabilities | To be tested corresponding to the real version |

## This implementation boundary

This fix: default stateful, configurable idle timeout, initialize return ID, subsequent request verification, authentication identity binding, invalid/expired/restart failure response, explicit stateless fallback, and the first initialize can be without a protocol version header.

The session repair batch recorded on this page did not include SSE event store at that time; batch thirty-five has been supplemented with old protocol SSE disconnection replay. Still out of scope are migration to other transports, cross-process persistence or shared session stores, restoring sessions before a service restart, database risk behaviors, frontends, and any client-side private handshake extensions. There is no local account/running environment evidence for the real behavior of Doubao and various versions of Claude, and they are all marked for actual testing, and non-standard branches will not be added based on this.

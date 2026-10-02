> Source: [../INTEGRATIONS.md](../INTEGRATIONS.md)

# AgentSQL MCP Integration Guide

> Release status: This document applies to the pending AgentSQL v0.5.0 release. The one-command assets and the `ghcr.io/cuipengdba/agentsql:v0.5.0` image become available after release-day validation; before then, use the “Source / Live Demo” paths in this guide. The source repository is `github.com/cuipengdba/agentsql`.

This guide explains how to connect an MCP client to AgentSQL. See the [User Guide](../USER_GUIDE.md) for identity, authorization, rules, approval, and capability boundaries, and [Getting Started](GETTING_STARTED.md) for a first deployment. See the [Notification Guide](../NOTIFICATIONS.md) to forward decision events to Webhook endpoints, alert groups, or Syslog.

## Choose a Transport

### Streamable HTTP: Preferred for a Running Service

When AgentSQL is already running through the installer, systemd, or Compose, connect directly with:

- Endpoint: `http://127.0.0.1:7780/mcp`
- Identity header: `Authorization: Bearer <Agent API Key>`
- MCP protocol version: `2025-06-18`
- `Accept`: `application/json, text/event-stream`
- Mode: stateful by default (`initialize` returns `Mcp-Session-Id`), with an explicit stateless fallback
- Maximum request body: 4 MiB

An Agent API Key is not a console administrator token. Disabling, expiring, or deleting an Agent, or rotating its Key, invalidates the old Key immediately.

The service should bind only to the loopback address by default. For remote access, use SSH local forwarding or a controlled TLS reverse proxy. Never expose `0.0.0.0:7780` directly to the public internet.

```bash
# Requires remote AgentSQL to listen on 127.0.0.1:7780 and a configured SSH identity
ssh -N -L 7780:127.0.0.1:7780 user@agentsql-host
```

The client then continues to connect to `http://127.0.0.1:7780/mcp`. A reverse proxy must provide TLS, access control, and rate limiting. Do not log the `Authorization` header.

### stdio: A Local Desktop Agent

stdio starts a new AgentSQL runtime:

```bash
# Requires a local agentsql binary and access to the absolute config and control-plane storage paths
AGENTSQL_SECRET='<EXACTLY_32_BYTES_PAIRED_WITH_THE_CONTROL_PLANE>' \
AGENTSQL_API_KEY='<Agent API Key>' \
agentsql mcp --config /absolute/path/config.yaml
```

`--config` can be abbreviated to `-c` and defaults to `config.yaml` in the current directory. Prefer providing the Key through `AGENTSQL_API_KEY`. Do not place it in `--api-key`, where it would appear in process arguments. stdio and HTTP use the same tool-registration rules: seven base tools are always available, and eight session/transaction tools are added when B5 is enabled, as it is by default.

> **Important: stdio is not a proxy to an AgentSQL service already running under systemd.** It creates a separate runtime that must be able to read the configuration and metadata/audit control-plane storage, and it must use the same `AGENTSQL_SECRET` that encrypts the data source credentials. The installer creates `/etc/agentsql/agentsql.env` with `0600 root:root` permissions, so an ordinary desktop user cannot read it. If the installed service is already running, use HTTP directly.

stdio mode disables the console and event stream; do not expect it to provide the console UI.

## MCP Tools

Every SQL argument accepts exactly one statement; stacked statements are not allowed. Clients must inspect the `decision` field in the structured result.

The following seven base tools are always registered:

| Tool | Purpose | Inputs | Possible decisions and key semantics |
| --- | --- | --- | --- |
| `list_datasources` | List data sources authorized for the current Agent | None | A successful response contains public identifiers, never the host, password, or connection parameters; failures return `error` |
| `list_schema` | Read authorized schema information, filtered by column permissions | `datasource_id`; optional `table` | Success normally returns `allow`; `table` accepts only letters, digits, underscores, and at most one dot in `table` or `schema.table`, without quoted or special identifiers; narrow the request to a specific table if the result would exceed 10,000 rows |
| `explain_query` | Parse, evaluate, and run read-only EXPLAIN without executing the SQL | `datasource_id`, `sql` | Can return `allow`, `deny`, `warn`, `approve`, or `error`; SQL is not executed even when the result is `allow` |
| `query` | Execute one protected `SELECT` | `datasource_id`, `sql` | Queries only; the data source `row_limit` truncates results and result-level data masking is applied automatically; risky queries can return `deny` or `approve` |
| `execute_write` | Submit one `INSERT`, `UPDATE`, `DELETE`, or DDL statement | `datasource_id`, `sql`, required `reason` | Administrative statements do not use this tool; DDL requires the Agent’s `ddl` tier; only `allow`/`warn` executes, while `approve` creates a pending approval automatically without execution |
| `request_approval` | Proactively send SQL for approval | `datasource_id`, `sql`, required `reason` | Creates an approval request without executing SQL and returns `approval_id` |
| `get_approval_result` | Read approval status | `approval_id` | An Agent can query only its own requests; another Agent’s request ID is treated as not found |

The caller-supplied `reason` for `execute_write` and `request_approval` is currently written to logs. The approval record stores the rule-evaluation reason, not the caller’s original text.

Because B5 cross-request logical sessions and PostgreSQL planned transactions are enabled by default in v0.4.0, `tools/list` also returns these eight tools by default:

| Tool | Purpose |
| --- | --- |
| `open_session` / `close_session` / `get_session_status` | Create, close, and inspect an explicit AgentSQL logical session; do not confuse it with the HTTP `Mcp-Session-Id` transport session |
| `begin_transaction` | Preflight and seal a complete ordered DML plan, then begin a PostgreSQL transaction; this step executes no statement |
| `execute_transaction_statement` | Execute one sealed `INSERT`, `UPDATE`, or `DELETE` at its planned ordinal; the SQL cannot be replaced during execution |
| `commit_transaction` / `rollback_transaction` | Commit or roll back and complete the terminal-state fence |
| `get_transaction_status` | Idempotently query the transaction outcome, audit, and disposition status |

Each B5 operation still accepts only one top-level SQL statement. B5 does not support `SELECT`, `RETURNING`, or DDL within a transaction, or MySQL cross-request transactions. Explicitly setting `mcp.sessions.enabled: false` hides these eight tools and also requires `mcp.transactions.postgres` to be disabled. The actual available tool set is always determined by `tools/list`.

## Call Sequences

### Read-Only Query

```text
list_datasources
  → list_schema
  → explain_query
  → query
```

Confirm the data source and visible schema first, evaluate the SQL, and only then execute it. An `allow` from `explain_query` means the evaluation passed; it does not mean a query ran.

### Write: Automatic Approval Routing

```text
execute_write(reason is required)
  -> decision="approve" + approval_id (a pending request already exists)
  -> administrator decides in the console
  -> get_approval_result
  -> after approval, a human or separate authorized process acts outside this request
```

After `execute_write` returns an `approval_id`, do not call `request_approval` again; doing so creates a duplicate request.

Approval is neither an executor nor an exemption ticket. `approved` records a positive human decision only: AgentSQL does not automatically execute the original SQL, and it does not unlock or consume a later `execute_write`. If the change is still required, an administrator must execute it outside AgentSQL, or an independent authorized process must submit it again and accept a new rule evaluation.

### Write: Proactive Approval Request

```text
request_approval(reason is required)
  -> approval_id
  -> administrator decides in the console
  -> get_approval_result
```

Use proactive approval when the caller explicitly wants human input first. It never executes SQL.

## Handle Return Values Correctly

- `decision:"allow"`: allowed; an execution tool has performed the action defined by its semantics.
- `decision:"warn"`: returns successfully with a warning; an execution tool may already have run, and the warning must still be recorded.
- `decision:"deny"`: rejected by a policy or rule; this is a successful tool result, not a transport error.
- `decision:"approve"`: requires human handling; the SQL was not executed.
- `decision:"error"`: processing failed; this is the only decision that sets MCP `IsError=true`.

Do not automatically retry `deny`, `warn`, or `approve` as if they were network errors. Blindly retrying a write can create duplicate approvals or duplicate business intent.

## Claude Desktop Integration (stdio)

Claude Desktop starts a local process from `mcpServers` in `claude_desktop_config.json`. Use an absolute path for the `agentsql` executable in `command` so differences between the desktop application’s and terminal’s `PATH` do not matter. Backslashes in Windows JSON paths must be written as `\\`.

```json
{
  "mcpServers": {
    "agentsql": {
      "command": "/absolute/path/to/agentsql",
      "args": ["mcp", "--config", "/absolute/path/config.yaml"],
      "env": {
        "AGENTSQL_SECRET": "<EXACTLY_32_BYTES_PAIRED_WITH_THE_CONTROL_PLANE>",
        "AGENTSQL_API_KEY": "<Agent API Key>"
      }
    }
  }
}
```

The repository CLI confirms these parameters: the subcommand is `mcp`, and the configuration option is `--config` (or `-c`). After saving the configuration and restarting Claude Desktop, the default configuration should expose seven base tools and eight B5 tools. If B5 is explicitly disabled, only the base tools appear. If they do not appear, confirm that the process launched by the desktop client can read the executable, configuration, and metadata/audit control-plane storage, and that it uses the same `AGENTSQL_SECRET` as the encrypted data source credentials.

## Cursor Integration (stdio)

Cursor can use the same `mcpServers` fragment in the user-level `~/.cursor/mcp.json` or project-level `.cursor/mcp.json`. In a team project, commit only a template without secrets. Store the real Key and SECRET using the secret-injection mechanism supported by the current Cursor version or in an untracked local configuration.

```json
{
  "mcpServers": {
    "agentsql": {
      "command": "/absolute/path/to/agentsql",
      "args": ["mcp", "--config", "/absolute/path/config.yaml"],
      "env": {
        "AGENTSQL_SECRET": "<EXACTLY_32_BYTES_PAIRED_WITH_THE_CONTROL_PLANE>",
        "AGENTSQL_API_KEY": "<Agent API Key>"
      }
    }
  }
}
```

Relative paths in a project-level file are sensitive to the working directory, so use absolute paths for both `command` and `--config`. Refresh the MCP Server using the procedure for the current Cursor version. If the configuration entry point or outer fields have changed, follow Cursor’s official MCP documentation.

## Doubao (豆包) and Other Remote MCP Clients (Streamable HTTP)

The client must explicitly support **Streamable HTTP** and allow custom headers for an MCP Server. Enter the following on the MCP Server configuration page in Doubao or another client:

| Setting | Public demo | Production self-hosting |
| --- | --- | --- |
| Server URL | `https://demo.agentsql.cn/mcp` | `https://<your-gateway>/mcp` |
| Header name | `Authorization` | `Authorization` |
| Header value | `Bearer <demo Agent API Key>` | `Bearer <self-hosted Agent API Key>` |

The demo Key is an Agent API Key, not the demo console’s administrator token. When using the prefilled demo administrator account on the sign-in page, confirm the current read-only Key for the selected demo Agent. If the demo Key is not public, verify against a self-hosted environment instead; never guess it or reuse an administrator token. The demo environment is for integration testing only. Production must be self-hosted with your own secrets. A client that lacks custom-header support, supports only legacy SSE, or has an unknown Streamable HTTP version cannot connect this way. Never place the Key in a URL query parameter.

The following is a generic field skeleton. Use the client’s current official documentation for Doubao’s or another client’s configuration entry point, outer JSON structure, and any required `type` or `transport` field:

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

If AgentSQL is already running locally, you can replace the URL with `http://127.0.0.1:7780/mcp`. A non-local production deployment must use a controlled HTTPS entry point and must not expose the listening port directly to the public internet.

- Cursor: for remote access, confirm the HTTP transport fields in the official MCP documentation for that version.
- Claude Desktop: confirm in the official MCP documentation whether that version supports Streamable HTTP; if it supports only stdio, use the Claude Desktop configuration above.
- Cline: confirm the configuration entry point, `mcpServers` structure, and HTTP transport fields in the official documentation.

For these clients, `command`, `args`, and `env` (stdio), and `url` and `headers` (HTTP), are the information AgentSQL requires. Follow the client’s current documentation for the outer structure and transport declaration.

## Find AgentSQL in MCP Registry / Glama

AgentSQL is listed in the official MCP Registry with active status. Search for **AgentSQL**, verify the entry name `io.github.cuipengdba/agentsql`, version, and repository, and then connect using a method supported by the client. The repository does not provide a stable direct URL for the Registry entry, so this guide does not invent one.

AgentSQL is also listed on Glama. Find it in the [Glama AgentSQL search results](https://glama.ai/mcp/servers?query=AgentSQL). Endpoints, versions, or Header examples in third-party directories can lag behind. This repository’s documentation, the actual deployment configuration, and MCP `tools/list` are authoritative.

## Client Integration Security Reminders

- The public demo connects only to synthetic demo databases and is intended for feature evaluation and client connectivity testing. Do not send real business SQL, data, or credentials to it.
- Production must be self-hosted with your own data sources, Agents, and API Keys. Put TLS, access control, and rate limiting in front of the gateway.
- Never put an API Key, `AGENTSQL_SECRET`, or a data source password in public configuration, screenshots, logs, or a code repository. Keep placeholders only in public projects, and rotate a secret immediately after exposure.
- A stdio configuration creates a new local AgentSQL runtime. If a controlled service already exists, prefer its Streamable HTTP endpoint to avoid copying production secret files or loosening their permissions.
- Give a client only the minimum Agent capabilities and data source permissions it needs. Always handle `deny`, `warn`, `approve`, and `error`; do not treat security decisions as network failures and retry them automatically.

## Custom Clients

### Raw HTTP `initialize` Example

Set the Key first. The following request applies to a Streamable HTTP service already running locally:

```bash
# Requires a local running AgentSQL service and AGENTSQL_API_KEY exported as a valid Agent Key
curl -fsS -D /tmp/agentsql-mcp-headers http://127.0.0.1:7780/mcp \
  -H "Authorization: Bearer ${AGENTSQL_API_KEY}" \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  --data '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"agentsql-smoke","version":"1.0.0"}}}'
```

Streamable HTTP is stateful by default. Read `Mcp-Session-Id` from the `initialize` response and send it unchanged on later `tools/list`, `tools/call`, GET, and DELETE requests. The first `initialize` negotiates its version in `params.protocolVersion`, so its HTTP protocol-version header is optional; later POST requests must send `MCP-Protocol-Version: 2025-06-18`. The server still revalidates the Bearer token on every request. `mcp.http.stateful: false` is a compatibility fallback: it returns no session header and accepts POST only. In either mode, every request must include the Bearer token and correct `Accept` header, and a body no larger than 4 MiB.

### Go SDK

Use `StreamableClientTransport` from `github.com/modelcontextprotocol/go-sdk`, with a custom `http.RoundTripper` that injects the following on every request:

```text
Authorization: Bearer <Agent API Key>
MCP-Protocol-Version: 2025-06-18
Accept: application/json, text/event-stream
```

Do not log complete headers, and do not treat `deny` as a transport error.

## Troubleshooting

### `401 unauthorized`

Check whether an administrator token was used by mistake or the Agent Key is wrong. Disabling, expiring, or deleting an Agent, or rotating its Key, invalidates the old Key. Rotation has no overlap period.

### stdio Has No Console UI

This is expected. stdio disables `console_enabled` and the event stream. For an installed service, use HTTP so the systemd-managed instance continues to provide the console.

### Access Works Only on the Server Itself

Loopback binding is the secure default. Use SSH `-L` or a controlled TLS reverse proxy. Do not change it to a public, unprotected `0.0.0.0` binding.

### MCP Handshake Fails

Confirm that the endpoint is `/mcp` and the request includes `Content-Type: application/json`, `Accept: application/json, text/event-stream`, and `MCP-Protocol-Version: 2025-06-18`. In the default stateful mode, also confirm that the `initialize` response contains `Mcp-Session-Id` and later requests send the same value. Check that the client actually supports Streamable HTTP and that the request body does not exceed 4 MiB.

### Stacked Statements Are Rejected

This is the expected R001 behavior. Send only one SQL statement at a time; do not append a second statement after a valid query.

### A Write Reports a Missing `reason`

Both `execute_write` and `request_approval` require a non-empty `reason`. This tool-level rejection occurs before entering the pipeline and might not create an audit record. Add the purpose, affected scope, and rollback plan, then submit again.

### stdio Cannot Reach the Control Plane or Decrypt a Data Source

Check whether the process can read the configuration and SQLite/PG control plane and whether it uses the same `AGENTSQL_SECRET` paired with the encrypted data source credentials. The installer creates `/etc/agentsql/agentsql.env` as `0600 root:root`. If an ordinary desktop user cannot read it, do not copy the production secret file or loosen its permissions; use the running service’s HTTP endpoint instead.

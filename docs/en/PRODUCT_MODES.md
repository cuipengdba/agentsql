# Three ways to use AgentSQL

[Simplified Chinese](../PRODUCT_MODES.md) | [English](PRODUCT_MODES.md)

AgentSQL combines an **independent AI data Q&A agent and an independent MCP Server database security gateway**. Employees ask questions in AgentSQL's own UI. External AI clients call its database tools through MCP. Both entry points can share one gateway. The steps below use a self-hosted PostgreSQL datasource and a local model.

## Before you start: install and grant access

1. Download the official package for your architecture and verify the signature and hashes. Install the gateway from the package root. Add `--with-chatbi` for native Q&A. [Formal releases](https://github.com/cuipengdba/agentsql/releases) · [Deployment guide](DEPLOY.md)
2. In the gateway console at `http://127.0.0.1:7780`, create a least-privilege database account, datasource, Agent, and object/column grants. Create a personal binding for Q&A users; create a separate Agent API Key for an external MCP client. Do not use an administrator login token as an MCP Key.
3. Deploy an OpenAI-compatible local text model in a private environment. The offline package provides the pinned schema embedding model. Both service ports listen on loopback by default. Use an SSH tunnel or controlled HTTPS reverse proxy for remote access. [Native Q&A configuration](CHATBI.md)

```sh
sudo sh install.sh install --with-chatbi
systemctl is-active agentsql.service agentsql-chatbi.service
```

## Mode one: AgentSQL native Q&A only

1. Grant the user, datasource, tables/columns, and applicable row rules in the gateway. Configure the current personal binding and local model as described in [native Q&A configuration](CHATBI.md). A successful configuration alone does not prove a successful query.
2. Open `http://127.0.0.1:8000`, sign in with an AgentSQL Q&A account, select a bound datasource, and ask a natural-language question such as “Count orders by region.”
3. AgentSQL selects schema from current grants and asks the configured model for candidate SQL; the gateway checks and executes it. The UI shows the answer, SQL, row count, and audit ID. Review the SQL and business meaning before relying on the answer.
4. Reopening history or exporting rechecks current grants. After a binding or table/column grant is revoked, old results are no longer an authorization source. Try one allowed and one denied request and confirm both decisions in the gateway query audit.

**For employees who ask questions directly.** AgentSQL provides the native page and Q&A service. Each release lists the models, gateway datasources, permission shapes, and clients it verified. [Formal releases](https://github.com/cuipengdba/agentsql/releases)

### View and switch charts

Explicitly request a column, horizontal-bar, line or pie chart, for example “summarize amount and count by region, as a column chart.” For one proven category and up to four numeric measures, the server saves field-name-only default metadata; ambiguous or unsupported shapes fall back to a table.

Switch the native answer between a table, bars or lines, and choose a dimension and 1–4 measures from the current result. Pies require one nonnegative measure with a positive total. Identifiers are not default measures when other business measures exist. Missing points break lines, zero values remain drawable, and unsafe numbers stay in the raw table. Charts show at most the first 20 rows and disclose gateway truncation.

Manual selections apply to the current view. Reopening uses the question-generated chart configuration. New loads, history reads, and exports are authorized against current grants. [Formal releases](https://github.com/cuipengdba/agentsql/releases)

## Mode two: AgentSQL MCP Server security gateway only

1. Create a dedicated Agent, link the datasource and minimum grants, and save the `asql_...` API Key shown only once. The Q&A service need not run.
2. Point a Streamable HTTP MCP Client to local `http://127.0.0.1:7780/mcp` and inject the Bearer Key through the client's secure secret configuration. See the [MCP integration guide](INTEGRATIONS.md) for other transports and protocol differences.
3. Call `list_datasources` and `list_schema` to inspect visible scope, then call `query` for an allowed request. Test denial with an unauthorized table or column and check both allowed and denied gateway audit records. The client must not self-assert a user identity or bypass the Agent binding.

```json
{
  "mcpServers": {
    "agentsql": {
      "url": "http://127.0.0.1:7780/mcp",
      "headers": {"Authorization": "Bearer <AGENT_API_KEY>"}
    }
  }
}
```

**Use when:** your own AI agent or a verified MCP client needs centrally controlled database access. SQLBot may be an optional caller through the [fixed-version adapter](SQLBOT.md); arbitrary SQLBot deployments are not automatically zero-change integrations. The MCP gateway itself does not provide a natural-language Q&A UI; the external client manages its text model.

## Mode three: native Q&A and MCP Server together

1. Run `agentsql.service` and `agentsql-chatbi.service` in one deployment. Create a personal binding for Q&A users and a separate Agent Key for external MCP clients. Grant each identity separately; never share an administrator token.
2. Users query authorized data through the AgentSQL UI; external agents query their permitted data through MCP tools. Both entry points use the gateway's SQL checks, limits, masking, and durable query audit.
3. Run allowed and denied cases for each path. Review Agent, user or binding, datasource, SQL, decision, and time in the audit. Revoke one path's credential or binding and retest both paths to confirm the intended authorization boundary.

```text
AgentSQL native UI → Go Q&A agent ───────┐
                                       ├→ AgentSQL security gateway → currently authorized database
External AI agent → AgentSQL MCP Server ┘
```

**Use when:** employees and enterprise AI agents both need data access. The two entry points share a security core, while identities, credentials, and grants must be configured and verified separately. The public [Demo](https://agentsql.cn/en/demo/) uses a simulated Q&A model and cannot substitute for real-model and real-database acceptance.

## Current verified limits and further reading

- Check the installed release's exact commit, cases, and performance measurements for verified scope. [Formal releases](https://github.com/cuipengdba/agentsql/releases) · [Scoring contract](sqlbot-community-chatbi-score-v1103.md)
- Type 1 retains its historical bounded integration scope; see the [Type 1 deployment design](v4.6.0-design.md) for configuration. Register a real non-admin user, existing authorized gateway alias, private directory, and host certificate first. The catalog cannot supply database credentials or bypass current grants. This release does not expand that entry point's verified scope.
- Active role, multi-user, and non-empty allow-list rules support only the bounded forms in their existing documentation; allow-lists do not exempt row or column restrictions. Full Community page/API operations, wider connector parity, real cloud-model inference, and production-scale p95 still lack complete acceptance. Unsupported paths remain denied or configuration-only.
- The user's real business system has not been validated; this is not a claim of 100% compatibility or zero-change cutover. Production still requires database least privilege, network isolation, backups, and local acceptance.
- Detailed guides: [native Q&A configuration](CHATBI.md) · [MCP integration](INTEGRATIONS.md) · [gateway user guide](USER_GUIDE.md) · [security boundaries](../../SECURITY.en.md).

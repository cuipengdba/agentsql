# Three ways to use AgentSQL

[Simplified Chinese](../PRODUCT_MODES.md) | [English](PRODUCT_MODES.md)

AgentSQL v4.6.0 combines an **independent AI data Q&A agent and an independent MCP Server database security gateway**. AgentSQL provides its own Q&A UI and Go service; SQLBot is not required at runtime. The MCP Server can independently provide controlled database tools to other AI clients. Both entry points can also share one gateway. The steps below use a self-hosted deployment, an operator-registered PostgreSQL gateway datasource, and a local model; other databases or clients require their own verified scope.

## Before you start: install and grant access

1. Download the official package for your architecture from the [v4.6.0 Release](https://github.com/cuipengdba/agentsql/releases/tag/v4.6.0), and verify its signature and hashes. Install the gateway from the package root; add `--with-chatbi` when native Q&A is needed. See the [deployment guide](DEPLOY.md) for other installer options.
2. In the gateway console at `http://127.0.0.1:7780`, create a least-privilege database account, datasource, Agent, and object/column grants. Create a personal binding for Q&A users; create a separate Agent API Key for an external MCP client. Do not use an administrator login token as an MCP Key.
3. Provide an OpenAI-compatible local text model service in a private environment. v4.6.0 includes a fixed local schema embedding model, but **does not bundle text LLM weights**. See [native Q&A configuration](CHATBI.md) for the model and personal binding setup. The two local ports listen on loopback by default; use an SSH tunnel or controlled HTTPS reverse proxy for remote access.

```sh
sudo sh install.sh install --with-chatbi
systemctl is-active agentsql.service agentsql-chatbi.service
```

## Mode one: AgentSQL native Q&A only

1. Grant the user, datasource, tables/columns, and applicable row rules in the gateway. Configure the current personal binding and local model as described in [native Q&A configuration](CHATBI.md). A successful configuration alone does not prove a successful query.
2. Open `http://127.0.0.1:8000`, sign in with an AgentSQL Q&A account, select a bound datasource, and ask a natural-language question such as “Count orders by region.”
3. AgentSQL selects schema from current grants and asks the configured model for candidate SQL; the gateway checks and executes it. The UI shows the answer, SQL, row count, and audit ID. Review the SQL and business meaning before relying on the answer.
4. Reopening history or exporting rechecks current grants. After a binding or table/column grant is revoked, old results are no longer an authorization source. Try one allowed and one denied request and confirm both decisions in the gateway query audit.

**Use when:** employees ask questions directly in AgentSQL's own UI. SQLBot does not need to run. The v4.6.0 verified scope covers the local models, registered PostgreSQL gateway aliases, and bounded permission shapes stated in the release notes; complex active role/multi-user/non-empty allow-list rules are outside this release's executable scope.

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

## v4.6.0 limits and further reading

- Verified paths and performance measurements are in the [v4.6.0 Release](https://github.com/cuipengdba/agentsql/releases/tag/v4.6.0). There is no guarantee that every natural-language answer is correct, and a fixed test case does not establish support for every database.
- v4.6.0 adds bounded Type 1 advanced-assistant and third-party directory integration for fixed SQLBot Community v1.10.3. Follow the [Type 1 setup](v4.6.0-design.md) to register a real non-admin user, existing authorized gateway alias, private directory, and host certificate before enabling the separate assistant service. The catalog cannot supply database credentials or bypass current grants.
- Active role, multi-user, and non-empty allow-list rules remain planned for v4.7.0; remaining Community pages/APIs and wider connector parity remain planned for v4.8.0. Deferred paths stay denied or configuration-only.
- The user's real business system has not been validated; this is not a claim of 100% compatibility or zero-change cutover. Production still requires database least privilege, network isolation, backups, and local acceptance.
- Detailed guides: [native Q&A configuration](CHATBI.md) · [MCP integration](INTEGRATIONS.md) · [gateway user guide](USER_GUIDE.md) · [security boundaries](../../SECURITY.en.md).

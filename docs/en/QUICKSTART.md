> Sources: [Getting Started](../GETTING_STARTED.md), [Deployment Guide](../DEPLOY.md), and repository deployment files.

# AgentSQL Five-Minute Quick Start

> Release status: this page is prepared for AgentSQL v0.5.0, whose installation assets and image remain pending until release-day validation. Before the release exists, use a source build or the currently published stable release.

This guide takes you from installation to a first protected query and an audit record. For a longer walkthrough and acceptance checklist, see [Getting Started](GETTING_STARTED.md).

## 1. Choose One Installation Path

All paths bind the console to the host loopback address by default.

### Native Linux binary and systemd service

Use this on `linux/amd64` or `linux/arm64` with glibc 2.28+ and systemd as PID 1:

```bash
curl -fsSL https://github.com/cuipengdba/agentsql/releases/latest/download/install.sh | sudo sh -s -- install
```

The installer chooses the architecture and verifies release checksums. In a non-interactive terminal, the generated administrator password is not printed; root can read it from `/etc/agentsql/agentsql.env`.

### Docker quick-start script

```bash
curl -fsSL https://raw.githubusercontent.com/cuipengdba/agentsql/main/scripts/quickstart.sh -o quickstart.sh
sh quickstart.sh
```

The script pulls the exact stable GHCR tag, creates a mode-`0600` `.env`, uses the `agentsql-data` named volume, and publishes only `127.0.0.1:7780`.

### Direct GHCR image

```bash
docker pull ghcr.io/cuipengdba/agentsql:v0.5.0
export AGENTSQL_SECRET="$(openssl rand -base64 24)"
export AGENTSQL_ADMIN_USER=admin
export AGENTSQL_ADMIN_PASSWORD="$(openssl rand -base64 18)"
docker run -d --name agentsql --restart unless-stopped \
  --security-opt no-new-privileges:true \
  -p 127.0.0.1:7780:7780 \
  -e AGENTSQL_SECRET -e AGENTSQL_ADMIN_USER -e AGENTSQL_ADMIN_PASSWORD \
  -v agentsql-data:/var/lib/agentsql \
  ghcr.io/cuipengdba/agentsql:v0.5.0
```

The GHCR tag is a `linux/amd64` and `linux/arm64` multi-architecture image. The three exported values are passed through the current shell instead of being included as literal process arguments.

## 2. Check the Secure Defaults

Open <http://127.0.0.1:7780>. The generated/default configuration uses:

- loopback HTTP at `127.0.0.1:7780`;
- an enabled console and SSE event stream;
- SQLite control-plane storage, with automatic migrations;
- a 5,000 ms statement timeout, 1,000-row result limit, five connections per data source, and 20 requests per second per Agent in the generated template;
- stateful MCP HTTP sessions, PostgreSQL B5 logical sessions, and PostgreSQL planned transactions enabled by default.

Configuration is strict YAML: unknown fields stop startup. Run `agentsqlctl init-config -o config.yaml` to generate a template and `agentsqlctl check-config -c config.yaml` to validate it. Do not copy undocumented internal threshold names into the YAML.

## 3. Add a Data Source, Agent, and Policy

In the console:

1. Under **Data Sources**, add a PostgreSQL or MySQL database using a dedicated least-privilege account. Never use a database owner, superuser, or MySQL `root`. Test the connection.
2. Under **Agents**, create an Agent with the minimum capability tier: `readonly`, `dml`, or `ddl`. Save the API Key immediately; it is shown only once.
3. Under **Permissions**, create an `allow` policy for that Agent and data source. Start with one specific table and only the required columns. AgentSQL denies access when no matching authorization exists.

The current data-source form does not expose database TLS/CA/client-certificate or arbitrary DSN options. Protect remote database links with the deployment network or a controlled proxy.

## 4. Connect and Run the First Query

Configure an MCP client with the Agent API Key:

```json
{
  "mcpServers": {
    "agentsql": {
      "url": "http://127.0.0.1:7780/mcp",
      "headers": {
        "Authorization": "Bearer <YOUR_AGENTSQL_API_KEY>"
      }
    }
  }
}
```

Call `list_datasources`, then `list_schema`. Submit one authorized statement through the `query` tool, replacing the identifiers with a table and columns you actually granted:

```sql
SELECT id, name
FROM public.customers
WHERE id = 1
LIMIT 10;
```

AgentSQL accepts one top-level SQL statement per operation. An `allow` from `explain_query` means evaluation succeeded; it does not mean the query was executed.

## 5. Verify Masking and Audit

To verify result protection:

1. Create a masking rule for a real sensitive result column under **Masking Rules**. Use `mask` for a supported formatted type or `block` to replace every non-empty value with `***`.
2. Run an authorized query that projects that column directly and, for table-scoped rules, qualifies the column with its table or alias.
3. Confirm that the returned value is transformed and that the MCP response reports masking metadata.

To verify audit:

1. Open **Audit** and confirm the allowed query appears.
2. With a `readonly` Agent, submit an `UPDATE` without a `WHERE` clause through `execute_write` and provide a non-empty `reason`. R002 and/or R003 should deny it before database execution.
3. Confirm the `deny` event and its matched rule in **Audit**.

Tool-level validation can happen before the main pipeline, so a malformed call such as `execute_write` without `reason` is not guaranteed to create an audit record.

## 6. Health and Next Steps

```bash
curl -fsS http://127.0.0.1:7780/healthz
curl -fsS -o /dev/null -w '%{http_code}\n' http://127.0.0.1:7780/readyz
```

- [Administration and RBAC](ADMIN.md)
- [MCP transports, tools, and session handling](INTEGRATIONS.md)
- [Security model, rules, masking, and audit boundaries](SECURITY.md)
- [Detailed getting-started and acceptance guide](GETTING_STARTED.md)
- [Chinese deployment guide](../DEPLOY.md)

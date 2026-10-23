# Connect SQLBot to AgentSQL

[Simplified Chinese](../SQLBOT.md) | [English](SQLBOT.md)

v3.2.0 keeps configuration version 2 and its signed-in user bindings, and adds specified MySQL read-only questions alongside PostgreSQL. The adapter targets SQLBot **v1.10.2**, source baseline `aa41fc1033bb974a703df9200a98ec8b9eec5f25`. See the [v3.2.0 acceptance record](v3.2.0-acceptance.md) for completed live tests. Passing a compatibility check does not establish coverage for every model, chart, or SQL statement.

```text
SQLBot ordinary login → workspace and datasource permissions → natural-language SQL
  → trusted server user + personal binding → short-lived human token
  → local Agent Key + human token → AgentSQL MCP
  → current binding / user / tenant / Agent policy → execution → masking and durable audit
  → SQLBot results and charts
```

## Scope and trust boundaries

- Managed bindings support the accepted **PostgreSQL/MySQL read-only queries**. Writes, B5, dynamic datasources, and direct Excel access are excluded. SQLBot continues to use its own PostgreSQL metadata database, separately from business databases.
- Only ordinary SQLBot identities authenticated on its server through `X-SQLBOT-TOKEN` are accepted. Assistant, embedded, and ask-token identities are rejected. Browser, model, or request-body user IDs are not authentication evidence.
- Each binding maps one SQLBot workspace, datasource name, and user to one AgentSQL user, read-only Agent, and PostgreSQL or MySQL datasource. For MySQL, the discovery schema must equal the datasource database name. Assign different Agent policies when users need different data scopes. The SQLBot server and connector credential are trusted deployment components: a server holding a credential can obtain a token for the user fixed in that binding.
- Every operation resolves the current binding; every MCP request validates its status and revision, user permissions, tenant status, and Agent. Disabling, modifying, or rotating the binding invalidates old tokens. Configuration version 2 validates its dedicated human token even when human delegation is not globally required. Existing ordinary delegation rules remain unchanged.
- `schema` limits metadata discovery; **it is not an independent SQL authorization boundary**. Agent policies and database privileges control SQL data access. Existing SQLBot metadata is intersected with the current user's AgentSQL-visible tables and columns so broader administrator caches do not enter the model context.
- Managed queries require a single `SELECT`. PostgreSQL rejects `SELECT INTO`, `EXPLAIN`, modifying CTEs, locking reads, and functions in `FROM`. MySQL rejects writes, multiple statements, locking reads, side-effecting functions, and syntax without a read-only proof; backtick-quoted identifiers pass dual parsing. Both dialects use short function allowlists. The PostgreSQL B2 executor requires explicit `pg_catalog` qualification, such as `pg_catalog.count(*)`; an unqualified model-generated `COUNT(*)` is rejected rather than rewritten. The parse tree is limited to 128 levels and 20,000 nodes. Existing AgentSQL policies, object authorization, and least-privilege database access still apply.

## 1. Prepare the gateway and personal bindings

1. Start the v3.2.0 AgentSQL console and HTTP MCP endpoint. Configure the PostgreSQL or MySQL business datasource with a least-privilege database account.
2. Create an enabled, ordinary **readonly Agent** in the same tenant; temporary task Agents are excluded. Configure permitted tables, columns, masking, and approval policies. Create or select an AgentSQL user with `query.execute` permission.
3. Open **SQLBot** in the console navigation and create a binding. Creation, modification, and rotation require all three permissions: `strategy.manage`, `datasource.manage`, and `user.manage`. Enter SQLBot workspace ID, user ID, datasource name, AgentSQL user, Agent, business datasource, discovery schema, and optional numeric column aliases.
4. SQLBot Snowflake IDs can exceed JavaScript's safe integer range. Use exact **decimal strings** for workspace and user IDs in configuration and APIs, never floating-point conversion. The datasource name must exactly match SQLBot.
5. Save the connector credential returned on creation. Only its hash is stored; plaintext is returned only when creating or rotating it. Rotate if lost. The Agent Key is a separate credential with an independent lifecycle.
6. Export the configuration. Exports contain bindings and environment variable names, **not credentials**. Console diagnostics reporting `configuration_ready` establish configuration prerequisites, not successful live business queries.

Verify allowed queries, denials, masking, and audit with a tool such as MCP Inspector before connecting SQLBot. Never put database credentials, gateway administration tokens, or connector credentials into prompts, chats, or browser code.

## 2. Build the pinned derivative image

Run from the AgentSQL repository root:

```sh
docker build -f integrations/sqlbot/Dockerfile -t agentsql-sqlbot:1.10.2-v3.2.0 .
docker run --rm --entrypoint python3 agentsql-sqlbot:1.10.2-v3.2.0 /tmp/agentsql-sqlbot/install.py --sqlbot-app /opt/sqlbot/app --check
```

The Dockerfile pins the upstream image digest. The installer validates four upstream source hashes before changing files. Patches cover database dispatch, identity propagation into question workers, metadata filtering, and the approval operations page. Adapted paths cannot fall back to direct business-database connections. Policy denials and approval requirements terminate the question instead of triggering automatic SQL repair and retry.

`--check` compares eight installed files with the installation manifest and fails on version or file drift. This is a deployment consistency check, not a substitute for image signatures or provenance verification. An SQLBot upgrade requires updated patches, hash checks, and acceptance testing; do not bypass version checks. Preserve upstream copyright, branding, and license requirements.

## 3. Mount configuration and server credentials

Example configuration version 2; normally use the console export:

```json
{
  "version": 2,
  "endpoint": "https://your-agentsql.example/mcp",
  "timeout_seconds": 30,
  "min_interval_seconds": 0.6,
  "bindings": [
    {
      "id": "binding-id-from-agentsql-console",
      "workspace_id": "7518977644151443456",
      "datasource_name": "AgentSQL PostgreSQL",
      "sqlbot_user_id": "7518977644151443457",
      "agent_key_env": "AGENTSQL_SQLBOT_READONLY_KEY",
      "connector_token_env": "AGENTSQL_SQLBOT_CONNECTOR_TOKEN"
    }
  ]
}
```

- Mount the file read-only into SQLBot and set `AGENTSQL_SQLBOT_CONFIG` to its container path. Inject the two referenced environment values through protected deployment configuration or secrets; never commit them to Git.
- Each operation sends the connector credential and binding ID, workspace ID, datasource name, and trusted user ID to `/api/v1/integrations/sqlbot/resolve` on the same gateway origin. All four identifiers must match. Success returns a dedicated two-minute human token, current revision, and datasource information. The Agent Key always comes from the local environment and is never returned by the resolver.
- MCP requests carry the Agent Key and `X-AgentSQL-User-Authorization: Bearer <short-lived token>`. This token is scoped to its binding and cannot replace administration login, ordinary human tokens, or another binding's token.
- HTTPS is required except for `localhost`, `127.0.0.1`, and `::1`. Redirects and environment proxies are disabled. Set top-level `ca_file` for a private CA; do not disable certificate verification.
- Keep SQLBot's existing persistent volumes, metadata database, and model configuration. Its business datasource can use a placeholder host such as `agentsql.invalid`; do not configure real business-database credentials. Production networking should also block direct SQLBot access to business databases. Models receive authorized metadata and masked results; choose model deployment locations according to organizational data requirements.
- After changing a binding or rotating credentials, update server secrets and recreate/restart containers to load new environment values, preserving volumes. Re-export configuration after changing SQLBot datasource names or user mappings. The adapter reads configuration on each operation.
- `min_interval_seconds` provides per-process pacing, not cluster coordination. Timeouts, HTTP 429, connection failures, and policy denials do not automatically retry business SQL.

## 4. Verify complete questions

Sign in with the ordinary SQLBot user and workspace specified by the binding. Create a PostgreSQL or MySQL datasource with the matching name, run its connection check and table discovery, then configure the model for natural-language questions and charts. MySQL table permissions use `database.table`. Existing SQLBot workspace and data permission checks remain; the gateway makes the final SQL execution decision.

The adapter returns SQLBot's `fields`, `data`, and `fields_info` along with an internal `agentsql.audit_id` extension. SQLBot chat storage may discard that extension; inspect durable audit records at the AgentSQL gateway. Query column aliases must be unique. If charts need numeric types, explicitly list **result aliases** in `numeric_columns`. Only finite numbers that can be represented safely and precisely are converted; masked values and unsafe high-precision numbers remain strings. Avoid blanket conversion that breaks financial precision or masking.

A successful connection check is insufficient. Verify real model-generated SQL, gateway execution, masked result delivery, charts, and durable audit. See the [acceptance record](v3.2.0-acceptance.md) for release test coverage.

## 5. Denials, approval, and single execution

Denial feedback contains filtered decisions, audit IDs, approval IDs, and rule identifiers, without raw gateway errors, credentials, or hidden data. The model does not automatically submit approval requests, rewrite denied SQL for retry, or execute approved requests.

The same-origin SQLBot operations page is **`/api/v1/agentsql/console.html`**, with the configured context-path prefix when applicable. It supports Chinese and English.

1. Sign in with an ordinary SQLBot account. The page can use the existing same-origin login, or accept your personal SQLBot login token temporarily in page memory. Never enter an Agent Key, connector credential, or gateway administration token.
2. Load your current workspace's bindings and run diagnostics. Diagnostics check connectivity and authorized metadata without executing business queries.
3. Check an existing approval ID, or explicitly enter SQL and a reason, confirm, and submit a new request. The gateway seals that SQL; submission does not execute it.
4. A reviewer acts in the existing AgentSQL approval interface. After approval, the original user, Agent, and datasource execute it. The operations page sends only the approval ID, preventing SQL substitution. Current authorization must remain valid, and the sealed request can execute at most once.

With PostgreSQL B2 column authorization enabled, an approval request seals the original SQL, identity, and datasource without reading business rows. The original requester claims an approval once, and execution rechecks live B2 column permissions, object binding, masking, and durable audit. It fails closed if current permissions, audit, or lease cannot be proven, and it never disables B2. See the real-database test in the [v3.2.0 acceptance record](v3.2.0-acceptance.md).
5. Results appear separately on this page; **the original SQLBot conversation and chart are not automatically rewritten or regenerated**.

All operations APIs require ordinary login authentication. Approval submission, status, and execution use POST JSON; mutations require explicit confirmation, and GET cannot trigger execution. The custom login header, same-origin validation, and omission of automatic Cookie credentials restrict cross-site requests. This page is an explicit SQL entry point constrained by the binding and Agent policy, not a replacement for SQLBot model-specific row-filter rewriting.

## Troubleshooting and acceptance

| Symptom | Check |
| --- | --- |
| No binding available | Ordinary login; exact workspace/user ID strings; datasource name; enabled binding |
| Resolve or MCP authentication fails | Connector credential versus Agent Key; environment after rotation; user `query.execute`; tenant and Agent status |
| Tables or columns missing | Current Agent table/column permissions, discovery schema, SQLBot metadata cache, datasource permissions |
| SQL denied | Agent policy, the read-only SQL subset, function allowlist; investigate using the audit ID rather than automatic retry |
| Incorrect chart numeric types | Unique aliases, `numeric_columns`, masked values, high-precision strings |
| Approved request cannot execute | Original requester/Agent/datasource, current permissions, approval state, previous consumption |
| Compatibility check fails | Upstream version, source hashes, installed file drift; rebuild and test the pinned image |

Acceptance should cover two users, different workspaces or data scopes, real questions and charts, allow/deny, blocked writes, masking, audit, disabling/modification/rotation, revoked user permissions, replay prevention, and gateway failure closure. Revocation blocks new requests; it does not erase results already stored in SQLBot. SQLBot manages their retention and removal.

Adapter tests: `python -m unittest discover -s integrations/sqlbot -p 'test_*.py' -v`. Route tests depend on FastAPI and SQLBot runtime libraries and skip when unavailable; run the complete suite in the pinned SQLBot image. Keep real credentials and unauthorized business data out of tests and logs.

## Historical configuration version 1 and migration

v3.0.2 configuration version 1 matches workspace and datasource name and uses `workspace_id`, `datasource_name`, `dialect: "pg"`, `datasource_id`, `schema`, `key_env`, and `numeric_columns`. It is a workspace-level Agent Key integration and **does not provide this version's personal mapping, short-lived human token, or managed approval operations page**. Historical validation is recorded in [v3.0.2 acceptance](v3.0.2-acceptance.md).

To migrate, create version 2 personal bindings, save the one-time credentials, and export configuration. Then switch the derivative image and server secrets and complete acceptance testing. Retain old configuration only for controlled recovery; deployments requiring personal identity governance must not use version 1 to bypass identity verification. SQLBot users absent from the bindings must fail closed.

# SQLBot access AgentSQL

**Language:** [English](SQLBOT.md) | [Simplified Chinese](../SQLBOT.md)

Supported integration target: [official DataEase SQLBot v1.10.2](https://github.com/dataease/SQLBot/tree/v1.10.2), upstream commit `aa41fc1033bb974a703df9200a98ec8b9eec5f25`. This adapter is provided with AgentSQL v3.0.2. The v3.0.1 `list_schema` response does not include field types and cannot replace the newer version for field discovery.

## Access methods and boundaries

SQLBot provides its own MCP Server, and its database queries will not call other MCP Servers. An independent Python adapter and fixed source hash installer are provided here. When building a SQLBot derived image, table/field discovery, connection checking and generated SQL execution are sent to AgentSQL `/mcp`. No changes are made to SQLBot's logo, copyright or large model generation logic.

```text
SQLBot User → Workspace/Data Source Permissions → SQLBot Generate SQL
  → Fixed workspace and data source mapping → AgentSQL Agent Key
  → MCP → Permissions/Rules → Controlled Execution → masking → Persistent Auditing → SQLBot Chart Data
```

The initial adapter supports PostgreSQL datasources, simple identifiers and an explicit schema. Queries must satisfy AgentSQL's existing SELECT subset and authorization rules. This integration does not expand B2/B5 capabilities or database support boundaries. The adapter has no business database account and never connects directly to a business database. Missing mappings, revoked keys, deny/warn/approval-required/unknown decisions, a missing audit ID, or an unavailable gateway all fail closed. It does not retry SQL, perform writes or approval execution, or forward SQLBot passwords.

SQLBot internal metadata PostgreSQL is still stored by SQLBot itself. The gateway's Agent identity corresponds to the SQLBot workspace and data source configured by the operator, and is not equivalent to the personal identity of each SQLBot end user; when user-level authorization is required, independent mapping and identity schemes must be configured. External dynamic data sources, Excel business reading, and unconfigured data sources are denied direct connections in this deployment.

## 1. Prepare AgentSQL

1. Use v3.0.2 or a later compatible version of AgentSQL to register a PostgreSQL data source for a dedicated read-only database account.
2. Create a dedicated read-only Agent, grant the minimum permissions according to the required schema, tables, and columns, and configure masking rules and persistent auditing. Do not grant `*` for access and do not use the console administrator token.
3. First use MCP Inspector to verify `list_datasources`, `list_schema`, an allowed SELECT and an unauthorized rejection. `list_schema` Output `data_type` only for authorized columns.
4. Store the Agent Key in SQLBot's server environment or a secrets management system. Do not put it in the SQLBot UI, source code, image, URL or configuration JSON. Use HTTPS with a valid, verified certificate. Loopback HTTP is permitted only for isolated tests on the same host.

## 2. Build a derived image

Execute in the root directory of the AgentSQL repository:

```bash
docker build -f integrations/sqlbot/Dockerfile -t agentsql-sqlbot:1.10.2 .
```

The installer only accepts the verified upstream `apps/db/db.py` hash and retains the original file backup in the image; if the version or file does not match, it will stop immediately. It replaces six high-level entrances and blocks seven lower-level business connection entrances. Don't skip hash checking or force support for other SQLBot versions with an old installer. Derived images comply with SQLBot upstream license, logo, and copyright requirements; this project does not relicense the upstream code to Apache-2.0.

## 3. Configure mapping and deploy

Copy the configuration from [config.example.json](../../integrations/sqlbot/config.example.json). `workspace_id` is the SQLBot workspace ID, `datasource_name` is exactly the same as the name on the page, and `datasource_id` is the AgentSQL data source ID. They are locked by server-side configuration; ambiguous mappings across workspaces or with duplicate names are rejected.

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

Mount the file read-only to SQLBot, such as `/run/agentsql-sqlbot/config.json`, and set `AGENTSQL_SQLBOT_CONFIG` to this path; provide `AGENTSQL_SQLBOT_READONLY_KEY` through the server environment. Private CAs can specify read-only `ca_file` in the configuration and cannot turn off TLS verification. The current limiting interval is a conservative request rhythm within a single process. When multiple workers share the Agent, they must still be planned according to the gateway current limiting; HTTP 429 does not automatically retry.

Use the persistence directory and model configuration of SQLBot's official deployment, and replace the base image with the derived image built above. Limit the direct network connection of SQLBot to the business database, allowing it to only access AgentSQL's HTTPS and its own metadata storage; separately configure TLS, account and access control when providing SQLBot to the outside world.

## 4. Create a data source in SQLBot

Create a PostgreSQL data source, the workspace and name are consistent with the mapping; schema fills in the configured schema. The business database host, username, and password in the connection form are not used for the adapter. `agentsql.invalid` and non-permission placeholder values ​​can be used, and the real business credentials are not saved. Connection check, table selection and field discovery will call the gateway; after renaming or changing the workspace, the operator must synchronize the server mapping.

The normal query returns SQLBot's `fields`, `data`, `fields_info` and SQL encoding, with an additional `agentsql.audit_id` for correlation. Values ​​that are masked to strings will not be incorrectly labeled as numbers; unique aliases must be used to repeatedly output column names to avoid overwriting of chart results.

Gateway current row results using string. When numeric charts are required, query output aliases are explicitly listed in `numeric_columns` in the server-side mapping; the adapter only converts a limited number of numeric values ​​that can be accurately represented under these aliases. Mobile phone numbers, serial numbers, etc. are retained as strings by default; masked values, mixed types, and values ​​exceeding safe precision will not be forced to convert. Undeclared numeric columns can still be displayed as table text.

## Acceptance and inspection

It is necessary to perform allow query, unauthorized denial, field pruning, sensitive value masking and persistent audit verification on the actual deployment; then revoke the Agent Key, remove the mapping, disconnect the gateway, and confirm that there is no direct connection or automatic retry. Rediscover and refresh cached table/field metadata in SQLBot after modifying permissions.

Adapter errors only print the stable error and audit ID when applicable, and do not echo the Key, DSN, original response, or result lines. SQLBot itself may record generated SQL, and its log access and retention policies need to be configured independently. The official source code, actual summary of the image, actual test environment and untested items are recorded in this version of the acceptance report; execution layer verification without a model account is not equivalent to complete natural language questions or acceptance of all chart types.

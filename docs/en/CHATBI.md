# Native Go data Q&A service

[Simplified Chinese](../CHATBI.md) | [English](CHATBI.md)

v4.1.0 introduced a separate Go Q&A service; the released v4.2.0 adds multi-turn context, business terms, recommended questions and controlled CSV/XLSX file datasources. The service reuses AgentSQL gateway identity bindings, authorization, masking and audit; SQLBot and Python are not runtime dependencies. Current functionality includes ordinary-account login, PostgreSQL/MySQL questions, conversation history, SQL and table results, and Chinese/English and light/dark interfaces. This does not yet replace every SQLBot Community feature; see the [version design](v4.1.0-design.md) for the five-release goal.

## Installation and startup

Verify release signatures through a trusted channel, extract the Linux package for your architecture, and run from the package directory:

```sh
sudo sh install.sh install --with-chatbi
```

Offline installation uses the verified local package and does not require Python, Go or Docker. The operating system must still meet the native installer's Linux, glibc, systemd and base-command requirements. `--no-start` only preinstalls files; it does not mean the service is running. This package does not include model weights or a model server, and installation does not automatically grant database access; a complete offline model bundle is planned for subsequent releases.

| Component | Default location |
| --- | --- |
| Gateway console | `http://127.0.0.1:7780` |
| Q&A page and compatibility API | `http://127.0.0.1:8000` |
| Service | `agentsql-chatbi.service` |
| Q&A configuration | `/etc/agentsql/chatbi.yaml` |
| Encryption key and initial administrator password | `/etc/agentsql/chatbi.env`, readable only by root |
| Encrypted store | `/var/lib/agentsql/chatbi.db` |
| Separate Q&A binary | `/usr/local/lib/agentsql/chatbi/agentsql` |
| Upgrade backups | `/var/backups/agentsql/chatbi-upgrade.*` |

The initial account is `admin`; both workspace and user IDs are the string `"1"`. The password is randomly generated and is not printed in noninteractive installation logs. Remote access should use an HTTPS reverse proxy or SSH tunnel; never put management or database connection credentials in browser code. Gateway accounts and Q&A accounts are separate identity namespaces.

A dedicated demonstration instance can initialize a standard account with `agentsql chatbi init --config chatbi.yaml --admin-user demo --standard-user`. This removes the administrator flag and workspace management weight and only applies to a fresh store. Database access still requires a separate personal binding.

## Configure a model and personal datasources

First follow the [SQLBot personal binding instructions](SQLBOT.md) to configure a datasource, readonly Agent, person, table/column grants and personal binding in the gateway. Native Q&A reuses these server-side bindings and revalidates them on every operation. Workspace IDs, user IDs and datasource names must match the Q&A configuration. Datasource `10` below is a Q&A-side ID, not a gateway datasource ID.

Save the following private JSON with mode `0600`, replacing placeholder credentials with actual values. The model must expose an OpenAI-compatible `/chat/completions` endpoint; `api_domain` ends at `/v1`, without `/chat/completions`. Remote HTTP model endpoints require explicit `allow_http`; use HTTPS for remote production connections.

```json
{
  "models": [{
    "workspace": "1",
    "model": {
      "id": "1",
      "name": "Local model",
      "base_model": "local-model",
      "api_domain": "http://127.0.0.1:8080/v1",
      "api_key": "",
      "allow_http": true,
      "max_tokens": 2048
    }
  }],
  "bindings": [{
    "datasource": "10",
    "workspace": "1",
    "user": "1",
    "name": "Sales PostgreSQL",
    "dialect": "pg",
    "binding_id": "REPLACE_WITH_GATEWAY_BINDING_ID",
    "connector_token": "REPLACE_WITH_CONNECTOR_SECRET",
    "agent_key": "REPLACE_WITH_AGENT_KEY"
  }]
}
```

Inject `AGENTSQL_CHATBI_SECRET` into the command environment using your controlled operations terminal, then run:

```sh
agentsql chatbi configure --config /etc/agentsql/chatbi.yaml --from connections.private.json
```

Configuration and its local management audit commit together. Credentials are encrypted, and the next question reads the updated configuration. Successful configuration does not prove live queries work; validate allowed, denied, masked, audited and failure cases. The current service uses one default model per workspace and manages personal bindings through the CLI.

## Rehearse an identity migration

The `scripts/sqlbot-export-identities.py` exporter supports only the checked SQLBot Community v1.10.2 identity sources and `sqlbot-xpack 0.0.5.37`. It reads users, workspaces, password digests, the JWT secret and login RSA key inside the original instance environment. The export contains highly sensitive credentials: restrict permissions, encrypt transfers and retain it under your operational policy. Never add it to Git, tickets or Releases.

```sh
python scripts/sqlbot-export-identities.py --output identities.private.json
agentsql chatbi preflight --from identities.private.json
agentsql chatbi import --config chatbi.yaml --from identities.private.json
agentsql chatbi serve --config chatbi.yaml
```

`preflight` does not modify the source file; `import` only creates a new target. First provide a separate 32-byte `AGENTSQL_CHATBI_SECRET` in the environment and select isolated storage and listener paths in the configuration. The initial export contains no models or personal datasource mappings; add them with `configure` after import. Import preserves accounts, decimal large-integer IDs, login encryption keys and explicitly permitted legacy JWTs; weak password digests upgrade to bcrypt after successful login. Disabled users, removed memberships, revoked tokens and gateway bindings still deny access.

This tool does not export historical conversations, API keys, assistant secrets, files, dashboards or all permissions. `complete_migration` remains `false` and cannot substantiate a completed production replacement without business changes. Retain the original SQLBot service and validate in isolation first.

## Historical question metadata rehearsal

`scripts/sqlbot-export-history.py` reads chat IDs, owners, workspaces, datasources, titles, and question IDs/timestamps in a read-only transaction against the pinned SQLBot Community v1.10.2. It omits old SQL, result rows, charts, model prompts and credentials. Question text can contain sensitive business data; handle the export as private data. Import identities and configure personal datasource bindings first, then use an isolated AgentSQL installation:

```sh
python scripts/sqlbot-export-history.py --output history.private.json --limit 1000
agentsql chatbi history-preflight --from history.private.json
agentsql chatbi import-history --config chatbi.yaml --from history.private.json
```

The exporter creates a new file with `0600` permissions. A batch supports at most 1,000 chats and 200 question-bearing records per chat. Exceeding a limit, a missing target identity or binding, an ID conflict, or an audit failure rejects the batch and rolls back its transaction. Original record IDs remain available, but imported turns only display past questions: they never replay old SQL, return stale results or enter new model context. SQLBot's timestamps without a timezone are provisionally interpreted as UTC; verify this against the source system before migration. The export report lists skipped records and the next page cursor. `complete_migration` remains `false` and this is not full-history or zero-change replacement acceptance.

## v4.2.0 file data and Q&A boundaries

Administrators can upload CSV/XLSX files in AgentSQL's own Q&A page, preview sheets, select field types and publish into a preconfigured isolated PostgreSQL managed datasource. The original SQLBot v1.10.2 browser was also checked locally with synthetic CSV/XLSX import, sheet selection, controlled preview and deletion. Original file bytes are staged encrypted for the current user. The server reparses the original file and publishes through a separate child binding, exact table/column policies and durable audit. Browser-supplied connections, table names or SQL do not broaden authorization. When only some sheets are selected, unselected tables cannot be queried through that datasource.

The three-turn Q&A context uses only successful questions and SQL, never historical result rows. Terms, examples and recommended questions are retrieved only for the currently authorized datasource; SQL examples are model references, while final queries still pass through the controlled gateway. Historical results and CSV/XLSX exports requery and audit under the current identity, binding and policies. PostgreSQL row rules admit only the verified narrow single-table predicates; queries that cannot be proved are rejected. See the [v4.2.0 release notes](release-notes-v4.2.0.md) for details and open items.

## Upgrade, recovery and boundaries

A managed installation with the Q&A component automatically includes it in subsequent `install.sh upgrade --from PATH` operations. The installer stops Q&A before backing up its database, WAL, configuration, key, binary and unit; a startup or version-health failure restores that component's prior data and service state. Gateway and Q&A have separate backups; two component installations are not a distributed database transaction.

Retain the backed-up database, encryption key and matching binary version as one recovery set. Stop the service before restoration; a copy of an active SQLite main file alone is not a complete backup. Instances with custom store paths, secret-variable names or listener addresses do not use default-path automatic upgrades and need a separately reviewed backup and recovery procedure. Uninstall preserves configuration and data by default; only `--purge` deletes default configuration and data directories.

Historical data requests reexecute readonly SQL under current authorization to prevent cached results from bypassing revoked access, so historical values may change with business data. This differs from SQLBot's cached-result semantics and must converge in later compatibility releases. A conversation holds at most 200 questions; expensive data requests share concurrency slots and a four-minute timeout. The `max_concurrent_data_requests` setting defaults to 16 and accepts 1–16; use 1 on small servers. Oversized listings are rejected. Gateway denial or audit failure releases no results and does not trigger automatic SQL rewriting or retries.

Pending areas include complete charts, analysis and prediction, dashboards, embedding and assistant SDKs, SQLBot MCP, full management and database matrices, and complete data migration. Existing AgentSQL gateway MCP remains available but is not SQLBot MCP interface compatibility. Verify model-generated SQL and results against business meaning; controlled execution does not guarantee correct model understanding.

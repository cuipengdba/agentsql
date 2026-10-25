# v4.1.0 release notes: native Go Q&A foundations

**Language:** [Simplified Chinese](../release-notes-v4.1.0.md) | [English](release-notes-v4.1.0.md)

Candidate version; release acceptance is in progress. GitHub Release and the [acceptance record](v4.1.0-acceptance.md) determine the final status.

## New capabilities

| Feature | Current implementation |
| --- | --- |
| Go Q&A service | Separate listener and encrypted SQLite storage; model orchestration, users, conversations and queries require no Python/SQLBot runtime |
| SQLBot identity foundations | Ordinary accounts, encrypted form login, HS256 tokens and decimal large-integer IDs for pinned Community v1.10.2; explicit identity import |
| Live questions | OpenAI-compatible model endpoint; PostgreSQL/MySQL single-turn natural-language SQL, tables and audit IDs |
| Controlled queries | Existing personal bindings and dual-credential MCP; live identity and authorization resolution; denial, audit failure or local persistence failure releases no results |
| Q&A interface | Chinese/English, light/dark themes, conversation history, SQL display, mobile menu and scrolling sidebar |
| Operations and offline foundations | `chatbi init/configure/preflight/import/serve`; one command installs gateway and Q&A from a verified Linux package; systemd rollback after upgrade failure |
| Compatibility inventory | 144 static public API entries with pinned Git blob hashes; dynamic dependencies, SDKs, MCP and full migration remain separately tracked |

See the [native Q&A guide](CHATBI.md) for instructions. Existing gateway, MCP, governance console and database support boundaries remain available.

## Compatibility scope

This is the first milestone in the v4.1.0–v4.5.0 replacement plan. It does not claim all SQLBot features or completed replacement without changes to existing business applications. Charts, prediction, recommendations, multi-turn semantics, file data, assistant/embedded SDKs, SQLBot MCP, full management and migration follow in subsequent releases.

Historical reads currently revalidate and execute SQL to avoid replaying data after access is revoked, so results can change with the database; this is not equivalent to SQLBot historical caching semantics. New questions and historical data requests share bounded concurrency and timeouts; small servers can set `max_concurrent_data_requests` to 1.

Native offline service installation requires no Docker, Go or Python, but the operating system must provide the supported Linux/glibc/systemd foundation. This standard package does not include model weights or a model runtime; successful installation is not complete out-of-the-box disconnected Q&A.

## Migration and security

Identity exports contain password digests and authentication keys and must stay within private operations environments. The exporter checks original identity sources and dependency versions, preflight leaves the source database unchanged, and import only creates a new target. Reports explicitly return `complete_migration=false`. Retain the original instance, credentials and paired backups until final cutover acceptance.

The Go implementation is independently written from protocols and observed behavior; SQLBot upstream implementation code has not been copied into this project's Apache-licensed files. Official Community execution paths include the dynamic `sqlbot_xpack` dependency; inventory size is not proof that all Community features have been verified.

## Validation and publication

Candidate validation uses synthetic PostgreSQL/MySQL data, real local models, original SQLBot login identities, Edge and isolated Linux installation environments. Publication additionally requires current-commit CI, signed dual-architecture assets, images, website and Demo checks. See the [acceptance record](v4.1.0-acceptance.md).

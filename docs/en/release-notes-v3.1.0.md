# AgentSQL v3.1.0 release notes

**Language:** [Simplified Chinese](../release-notes-v3.1.0.md) | [English](release-notes-v3.1.0.md)

This is a release candidate. The [release record](v3.1.0-release-evidence.md) determines publication status and completed gates.

## SQLBot user-level integration

- Add SQLBot workspace, datasource and user mappings to existing AgentSQL people and read-only Agents. Workspace and user IDs remain strings to preserve full precision.
- Resolve the current mapping with a dedicated connector credential on each operation and obtain a two-minute integration identity token. MCP rechecks live user permissions, Agent state, binding revision and datasource scope per request.
- Disabling, editing, deleting or rotating a binding invalidates old tokens. Creating, editing and rotating require policy, user and datasource management permissions. Credentials are shown once; exports contain no secrets.
- Explicitly propagate the ordinary SQLBot signed-in identity into model workers and filter table/field metadata through the current user's gateway permissions before it reaches the model.
- Add a read-only approval page for explicit requests, status checks and execution of approved sealed requests. Rejection does not trigger model-driven SQL rewriting or automatic retries.
- The one-time approval execution flow passed in an isolated environment with B2 explicitly disabled. With PostgreSQL B2 column authorization enabled, the gateway rejects approval requests and does not downgrade authorization. B2-enabled read approvals remain for a later release.
- Add integration diagnostics and installation manifest checks. Upstream SQLBot v1.10.2 file hashes are pinned; mismatches stop installation.

## Upgrade

Add the SQLBot binding table: combined schema 24, separate metadata 23 and audit 6. Back up first, then explicitly migrate. Rollback restores a matched v3.0.2 backup. See the [upgrade guide](v3.1.0-upgrade.md).

## Scope

This release targets SQLBot v1.10.2 ordinary login, PostgreSQL and verified table/chart scenarios. Model, deployment and outcomes are recorded in [acceptance](v3.1.0-acceptance.md). Embedded assistant/ask-token, other SQLBot versions, MySQL integration and writes are outside this release. Legacy v1 configuration retains its workspace-level behavior; user-level capabilities require managed v2.

The configured schema filters discovery; existing policies remain authoritative for SQL object access. The SQLBot server is a trusted identity provider, while the separate Agent Key retains its original credential lifecycle. This release does not automatically expand database version, mode or production-support claims.

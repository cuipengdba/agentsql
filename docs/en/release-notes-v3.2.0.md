# AgentSQL v3.2.0 Release Notes

**Language:** [English](release-notes-v3.2.0.md) | [Simplified Chinese](../release-notes-v3.2.0.md)

Status: formally released. Delivery gates are recorded in the [release evidence](v3.2.0-release-evidence.md), and capability boundaries remain in the [acceptance record](v3.2.0-acceptance.md).

## Changes

| Area | Change |
| --- | --- |
| PostgreSQL B2 read approvals | A request seals the original SQL and identity without reading business rows. The original requester claims an approval once, and execution rechecks live B2 column permissions, object binding, masking, and durable audit. Unauthorized columns, revocation, audit failure, and replay fail closed. |
| SQLBot MySQL | Personal bindings for pinned SQLBot v1.10.2 can map a specified MySQL datasource. Table discovery, model questions, and charts use AgentSQL MCP. Valid backtick-quoted MySQL identifiers work; writes, multiple statements, and side-effecting functions are rejected. |
| Policy impact preview | Before applying a table-level policy bundle, the console shows affected Agents, datasources, objects, and columns. The preview comes from the current policy snapshot; database objects, live rules, and masking are rechecked at execution. |
| Result protection | PostgreSQL B2 rejects derived queries when restricted columns influence predicates, ordering, or functions. SQLBot chart numeric result aliases must be configured explicitly; masked or imprecisely convertible values remain text. |

## Integration and upgrade

For a SQLBot MySQL binding, `schema_name` must equal the AgentSQL datasource database name. Give the read-only Agent only the needed `database.table` permissions, and configure `numeric_columns` for numeric chart result aliases. See the [SQLBot guide](SQLBOT.md).

This release does not add any full-production-support claim for a database. Live acceptance covers specified versions, isolated synthetic data, and single-instance paths. Clusters, other database versions, real customer workloads, and disaster recovery retain their separate acceptance boundaries. Back up metadata and audit data before upgrading; recheck identity, permissions, allow/deny decisions, masking, and recovery afterward.

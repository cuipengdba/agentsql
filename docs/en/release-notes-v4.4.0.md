# v4.4.0 Release Notes: Administration and Datasource Relationships

**Language:** [English](release-notes-v4.4.0.md) | [Simplified Chinese](../release-notes-v4.4.0.md)

Use the [GitHub Release](https://github.com/cuipengdba/agentsql/releases/tag/v4.4.0) for the formal publication status, signed assets and installation instructions. This release is an administration and datasource increment in the SQLBot Community replacement plan. AgentSQL still supports its own Q&A service and an optional database security gateway for an existing SQLBot deployment. The later full feature and integration goal is tracked in [v4.5.0 PR #90](https://github.com/cuipengdba/agentsql/pull/90).

## Implemented and locally accepted scope

| Capability | Acceptance boundary |
| --- | --- |
| Accounts and workspaces | The bilingual AgentSQL Web interface adds account, user and workspace administration. Administrator login from the unchanged SQLBot v1.10.3 client was compared in an isolated control store. Password changes, disabling and membership revocation invalidate old tokens; complete original user, workspace and role workflows remain unverified. |
| Datasource remarks and table selection | Managed files and server-bound PostgreSQL/MySQL sources can edit table and field remarks and table selections within the currently visible schema. Selection is persisted with binding and management audit. A selected PostgreSQL remarks flow was compared with the original page; AgentSQL's own page and API also covered selected MySQL and XLSX round trips. Browser-supplied connection settings do not create new database connections. |
| Relationships and controlled Q&A | The original X6 page and AgentSQL's bilingual page read, saved and reloaded a relationship between two PostgreSQL 16 tables. Local Qwen generated an aggregate join that ran through the gateway with persistent audit under current grants. Deselecting the related table denied old relationship and result reads. This does not establish other databases or arbitrary SQL support. |
| Administration compatibility routes | Bounded implementations cover users, members, workspaces, model settings, system variables, account API keys and MCP management. Each route remains subject to current identity, membership, binding and management audit. Behavior without original page and API comparison is not claimed as fully compatible. |

## Security and known limits

Model-generated SQL still passes through the AgentSQL controlled gateway. Requests fail closed when current identity, datasource, table/column, row-rule or audit proof is unavailable. Historical results do not bypass revocation. Model interactions in the public Demo are explicitly simulated; local real-model testing does not mean a model is connected to the public Demo.

This release **does not claim** complete SQLBot Community page, API, MCP or embed coverage, or that any existing business system has been switched without changes. Arbitrary original connection creation and editing, a global multi-model catalog, the complete role and failure matrix, more databases and the full client matrix require later acceptance. The user will lead cutover of their actual business system after v4.5.0 is released, and any issues found will be planned separately.

## Upgrade and delivery

Back up the control store, audit, managed files and encryption keys before upgrading. Formal assets must be built from the source commit for this tag and checked with a separately trusted public key, signed checksum manifest and Ed25519 signatures. Verify disconnected installation on both architectures, upgrade from v4.3.0 and failure rollback according to the [release process](RELEASE_PROCESS.md). The PostgreSQL native binder resides on the database server and needs its own trusted package and a preflight using the actual runtime role. A successful application install does not imply production support for an unaccepted database mode or integration.

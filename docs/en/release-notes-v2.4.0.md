# AgentSQL v2.4.0 Release Notes

**Language:** [English](release-notes-v2.4.0.md) | [Simplified Chinese](../release-notes-v2.4.0.md)

v2.4.0 Adds independent `opengauss` data source type and controlled read-only MCP path for openGauss Community Edition 7.0.0 LTS specified build. Adapter requires `verify-full` TLS, precise server-side built identity, and read-only sessions; only accepts SELECT with explicit schema, `FROM ONLY` normal physical single table, direct column projection, non-constant WHERE, integer LIMIT 1–10000. Requests whose origin, object type, or semantics cannot be proven are rejected before execution.

In the 7.0.0 LTS single instance and synthetic data loaded in the official openEuler 24.03 x86_64 archive, the dedicated read-only account completes real MCP permission, R010 table rejection, mobile phone number masking, approval of single consumption, persistent auditing and policy revocation. MCP Inspector 0.16.8 and Dify MCPClient 1.17.1 allow and deny calls respectively. Wrong CA, wrong certificate host name and plain text connection failed; original results were not delivered during database SELECT revocation, business database and independent audit library disconnection. The openGauss synthetic data has been restored and verified by `gs_dump` and `gs_restore`. For the item-by-item environment and results, see [ candidate acceptance ](v2.4.0-acceptance.md).

## Security and Compatibility Boundaries

- This version only covers the `openGauss 7.0.0 build 2397a86f` designated community version single instance; Docker Hub's 7.0.0-RC3 was only used for pre-checking. GaussDB commercial edition, other openGauss editions, active/standby, production certificate rotation, long-term loads and customer deployments are not verified.
- `opengauss` does not inherit PostgreSQL's B2 column-level bindings and B5 session transactions. JOIN, functions, aggregates, views, external tables, system catalogs, writing and arbitrary SQL are not open. Authorization roles require minimum privileges on the database itself; the gateway allows no override of the database GRANT.
- This version does not add AgentSQL metadata/audit schema. Upgrading should still include a complete set of backups, read-only checks, and isolation recovery drills for the SQLite or PostgreSQL metadata/audit sub-repository. Fixed dual-gateway deployment still requires a maintenance window, and new and old images cannot be mixed.
- This is a controlled, read-only piece of evidence for a given environment and does not claim vendor certification or full production support. The public network Live Demo maintains synthetic PostgreSQL data and does not connect to the openGauss test library.

The post-release results of the officially released CI, signed dual-architecture assets, GHCR, official website and public website Live Demo are recorded in [. The official release evidence is ](v2.4.0-release-evidence.md).

# AgentSQL v2.7.0 Release Notes

**Language:** [English](release-notes-v2.7.0.md) | [Simplified Chinese](../release-notes-v2.7.0.md)

Status: Officially released. For the actual results of sealing CI, upgrade recovery, signature assets, GHCR, official website and public website demo, see [ release evidence ](v2.7.0-release-evidence.md).

## Scope of this edition

- Added Agent MCP `query_analytics` for ClickHouse 26.8.31.2 specifying TLS single node. The caller submits the data source ID, the current database's MergeTree table name, explicit columns, String column equality filtering, and a 1–100 row limit; no SQL is submitted. The server constructs parameterized queries that limit execution time, read rows/bytes, memory, output rows, and threads, and additionally limit result size.
- Each call re-verifies Agent, tenant, data source, exact table authorization and explicit column whitelisting; filter columns must also be authorized. The database minimum privilege account of `readonly=1` can only SELECT authorization table columns. Views, distributed, table functions, JOINs, aggregations, writes, and cross-database queries are not part of this controlled path.
- Strict TLS with designated CA, source column masking, persistent preflight, and final state auditing form a complete request chain. The business database will not be accessed when the preflight audit fails; the results will not be delivered when the final audit fails.
- The old ClickHouse admin preview still retains the registered default port of 9000; the new controlled query configuration in the console pre-populates native TLS 9440. The original clear text configuration can continue to manage previews, but `query_analytics` cannot be called.

## Validation boundaries

Real HTTP MCP, MCP Inspector 0.16.8, Dify MCPClient 1.17.1, minimum permissions, parameter binding, error CA, permission revocation, independent audit library and business database shutdown recovery have been passed in the single-node synthetic environment; see [ candidate acceptance ](v2.7.0-acceptance.md) for details. For the results of the official package upgrade and public release chain, see [ release evidence ](v2.7.0-release-evidence.md).

Other versions, clustering/replication, production certificate rotation, long-term loads, production data, and full production support are not verified. The public network demo continues to use synthetic PostgreSQL data.

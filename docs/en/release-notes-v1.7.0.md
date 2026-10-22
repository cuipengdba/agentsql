# AgentSQL v1.7.0 Release Notes

**Language:** [English](release-notes-v1.7.0.md) | [Simplified Chinese](../release-notes-v1.7.0.md)

v1.7.0 adds independent data source types and restricted read-only execution paths to HighGo HighGo, and adds TLS and credential rotation for server-side identity verification to KingbaseES KingbaseES V9R1C10 PG mode. Support claims are limited to specified instances, protocol modes, single instances, and synthetic data that have been verified; this release does not claim full production support.

## New additions and improvements

- The console provides an independent `highgo` type, default port 5866, and explicitly displays a read-only SQL subset; the old `postgres` data source will not automatically change to the HighGo type.
- Each new connection is verified for exact version using vendor catalog functions, checking PG mode and the connection's default read-only transaction status. Ordinary PostgreSQL, other HighGo versions or modes refuse to connect when verification fails; connection pool reconnection will also be re-verified.
- Only single-bar, explicit schema, single-table, direct column projection, non-constant WHERE and LIMIT SELECTs are allowed. Writes, DDL, JOIN, functions, aggregations, sorting, locks, CTEs, multiple statements, and SQL whose source cannot be proven are rejected; the database account must also have minimum SELECT permissions.
- Table-level policies, mobile phone number masking, persistent auditing, metadata discovery and MCP queries are connected through this restricted path. PostgreSQL B2 column-level binding and B5 write transactions do not extend to HighGo.
- MCP Inspector CLI and Dify 1.17.1 MCPClient complete allow and deny requests for the same real database respectively; real authorization revocation, library stop, independent audit library failure, lock wait timeout and client cancellation all perform fault closure and recovery verification.
- The PostgreSQL protocol data source can be explicitly selected as `verify-full`, providing an absolute CA file path readable by the gateway and an optional certificate hostname; verifying the configuration has no clear text fallback and rejects both the wrong CA and hostname. KingbaseES V9R1C10 PG mode isolation instance passes certificate rotation, database account password rotation, allow/deny/masking/persistent auditing of MCP Inspector and Dify 1.17.1, and four Agent small sample concurrency baselines.
- The management API replaces the CA and hostname configuration in its entirety when explicitly updating TLS mode, supporting certificate rotation and off-mode switching to avoid inheriting the old root of trust.

## Compatibility and limitations

- The management API and MCP tool formats have not changed; there are no new migrations to metadata/audit schema. For formal installation, you still need to back up metadata, audit and keys first, and review the upgrade and recovery according to [ acceptance record ](v1.7.0-acceptance.md).
- Using the PostgreSQL wire protocol of pgx/v5 is a driver choice and does not imply inheritance of the full semantics or support level of PostgreSQL.
- Dameng DM8 The current Go-driven TLS handshake does not verify the server certificate, and AgentSQL continues to reject Dameng's explicit TLS configuration. The formal active/standby switchover, production load and more SQL dialects of Dameng and KingbaseES have not yet been completed and remain unverified; single instance disconnection recovery is not equal to HA. For evidence and boundaries, see [Dameng/KingbaseES acceptance record ](v1.7.0-dm-kingbase-depth-acceptance.md).
- HighGo Oracle/MySQL mode, other versions, distributed and HA, TLS, connection pool failover, extension types/OIDs, production loads, vendor certifications, and long-term licensing are not verified. The above capabilities remain unproven.
- The number of registered database types has increased from 28 to 29; KingbaseES KingbaseES V9 is still listed separately as the PG protocol path of the specified instance and is not included in the registration type.

For the official release evidence, SHA submission, signed assets, image summary, official website and Live Demo status, please see [v1.7.0. For official release acceptance ](v1.7.0-release-evidence.md); the real database boundaries of HighGo and KingbaseES can be found in their special acceptance respectively.

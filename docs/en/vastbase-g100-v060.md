# Vastbase G100 v0.6.0 adaptation boundary

**Language:** [English](vastbase-g100-v060.md) | [Simplified Chinese](../vastbase-g100-v060.md)

Status: Completed development acceptance of restricted read-only path on real **Vastbase G100 V3.0 Build 9, PG compatibility mode, x86_64** instance; has not yet reached full production support or vendor certification. Database type independent use `vastbase`. The following conclusions apply only to the tested versions, modes, and restricted SQL subsets.

## Current implementation

- For G100 only**PG compatibility mode for Build 9**Use PG wire to connect. Requires a database account with read-only permissions; connection parameters are mandatory`default_transaction_read_only=on`, and check when establishing a connection`pg_catalog.vb_version()`product and Build 9 logos,`sql_compatibility=PG`, the read-only transaction parameters are`on`. G100's`version()`Returns a PostgreSQL compatible string from which the product cannot be identified. The number of rows and statement timeouts are still limited by AgentSQL.
- Raw SQL only accepts a single table `SELECT` with an explicit schema, which must be accompanied by `WHERE` and `LIMIT`, and rejects the constant truth conditions recognized by the parser; the projection must be a parsable direct column. Reject functions, comments, wildcards, JOIN, CTE, subqueries, aggregations, sorting, row locks, `SELECT INTO`, write statements and multiple statements.
- The executor parses the SQL again; session, transaction, and write entries are rejected before the database call. Normal PostgreSQL-specific B2 column-level binding and B5 transaction coordination are not inherited. G100 queries configured with B2 column-level policy are denied execution.
- The metadata reads the fixed `information_schema.columns` query and parameters, and does not receive any SQL from the client. Sensitive discovery can only use metadata mode; sampling is subject to acceptance by the real database.
- Read metadata has been verified for `EXPLAIN (FORMAT JSON)`, PG results protocol, and binding parameters in the instance described below; any other version or mode does not automatically inherit this conclusion. Stop execution on incompatibility, do not downgrade to unplanned checks or bypass parsing.

## real database verification record (2026-10-14)

- Installation media: `Vastbase-G100-3.0_Build9_30156-Linux-x86_64-no_mot-202601221034.tar.gz`; outer SHA-256 is `646d7166bd9b1e5b33e119dda6aaea708914d5f7fac016deb848ab47237b316a`. The inner archive and database load match the `.checksum` and `.sha256` included in the installation package respectively.
- Topology: Rocky Linux 8.9 x86_64 standalone container on Windows Docker Desktop, database reports `Vastbase G100 V3.0 (Build 9) Release`, commit `30156`, `sql_compatibility=PG`. The test account only obtains the `SELECT` permission for the two tables in the test schema; the AgentSQL connection also sets read-only transactions. The automated test container shares the database container network namespace and uses `127.0.0.1:5432`; the database HBA only allows loopback connections and does not release the remote address for testing.
- License: `vb_licensetool --view-temporary` A temporary license generated when the report is installed, valid from 2026-10-14 to 2027-01-12, module BASIC/VECTOR. No formal production license is provided; this time it is only used for isolation container acceptance.
- Synthetic data: `agentsql_v060.contacts` contains the test mobile phone number, `agentsql_v060.customers` is used for unauthorized table and driver layer queries. No customer data.
- `TestVastbaseBuild9Integration`: Product and schema verification, read-only connection, fixed metadata query of bound parameters, bounded single table query, JSON EXPLAIN, write/lock row/multi-statement rejection.
- `TestVastbaseBuild9PipelineRealE2E`: Table authorization, mobile phone number masking, unauthorized table rejection, dangerous SQL rejection and SQLite persistent audit passed. `TestVastbaseBuild9MCPRealE2E`: Real MCP tool invocation, application/approval/single execution, masking, repeated execution rejection, data source change and policy revocation rejection pass.
- Not yet verified: official production license, TLS, different operating systems and CPUs, other G100 Builds, clustering and failover, long-term load, upgrade/rollback, target client Dify/MCP Inspector; Claude Desktop UI has not been tested yet. Container acceptance does not replace these conditions.

## Must be completed before scaling to full production support

1. After obtaining the official license, check the authorization scope; verify timeout/cancellation, database interruption and recovery, column-level policy rejection, and no evidence of side effects on the database side.
2. Use Dify and MCP Inspector to complete real tool invocation, rejection, and audit correlation respectively; if you need to declare Claude Desktop compatibility, use the logged-in client for separate acceptance.
3. Verify installation, upgrade, rollback and failure recovery, and retain product, version, mode, driver, account permissions and topology records; no claims of full production support before this.

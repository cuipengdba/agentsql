# AgentSQL v1.6.0 Release Notes (Draft Candidate)

**Language:** [English](release-notes-v1.6.0.md) | [Simplified Chinese](../release-notes-v1.6.0.md)

v1.6.0 focuses on the fault shutdown evidence of the connected Dameng, KingbaseES and Vastbase specified instances. This article is only a candidate draft before the official release, signature assets, official website and Live Demo are all accepted.

## Improve

- The controlled query checks the request cancellation or timeout status again after reading the row to avoid delivering the collected sensitive rows to the client when the driver returns a null error. Covers PostgreSQL, Dameng/Oracle narrow SQL path and MySQL two result paths.
- Dameng connection increases the network request deadline, and directly closes the physical connection used by the request when the client cancels, avoiding driver synchronization rollback stagnation while waiting for locks. The Dameng read-only pool no longer retains idle connections; this security trade-off may increase connection establishment overhead.
- Stateless MCP 2026-07-28 endpoint passes client disconnects to executing read-only tools; legacy stateful endpoints maintain independent server deadlines and B5 session constraints. The clear `statement_timeout` returned by PostgreSQL compatible libraries such as KingbaseES is now recorded as a timeout, while ordinary cancellation is still recorded as an interruption.
- The three designated databases all add the same set of independent audit database failure, recovery, real query timeout, client cancellation, database-side active query termination and masking result review rerun tests. The associated synthetic data and temporary credentials are not distributed with the repository or release package.
- The Live Demo overview distinguishes between gateway survival, dependency readiness and recent connection checks for each demo data source; "Number of data sources" is changed to "Configured data sources".

## Compatibility and limitations

- The management API, MCP tool format, and database schema are unchanged; existing data source configurations do not need to be migrated. Upgrading still requires backing up metadata, audit, and keys and practicing recovery.
- This version of the real database conclusion applies only to the instances, protocol modes, and bounded read-only subsets listed in [v1.6.0 acceptance record ](v1.6.0-acceptance.md). The 28-model registration database or the three named manufacturers will not be automatically promoted to full production support.
- DM8 Other Compatibility Modes, KingbaseES Oracle Mode, Vastbase Other Builds, HA, TLS, Long Term Licensing, Complex SQL and Production Loads are not demonstrated by failure testing of this release.
- `/healthz` only indicates that the process is alive, and `/readyz` checks the status of the control plane, auditing, etc. that the gateway relies on; the demo business data source still requires Ping one by one and real MCP request acceptance.

When officially released, the corresponding submission, CI, signed assets, image summary, official website/Demo and upgrade recovery evidence will be added.

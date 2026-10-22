# AgentSQL v2.2.0 Release Notes

**Language:** [English](release-notes-v2.2.0.md) | [Simplified Chinese](../release-notes-v2.2.0.md)

v2.2.0 A fully read-only MCP secure link that connects existing Oracle native connections, restricted SELECT parsers, and executors to AgentSQL. Previously, although Oracle could do low-level connections, discovery, and a small number of queries, it did not have the same link certification through gateway dialects, rules, authorization, masking, and persistent auditing; this version only declares capabilities based on the actual verified Oracle AI Database 26ai Free specified instance.

Candidate instances for this version are `FREEPDB1`, server `23.26.3.0.0`, `go-ora/v2`, and x86_64 Docker single instance. Random synthesis table and temporary account only get `CREATE SESSION` and target table `SELECT`. Real HTTP MCP allows queries to return masked mobile phone numbers; unauthorized table R010 rejection, write blocking, database SELECT revocation, Agent policy revocation, independent audit library shutdown and Oracle disconnection are all verified by not delivering the original results. MCP Inspector and Dify also complete allow and deny calls respectively. For detailed results, see [ candidate acceptance ](v2.2.0-acceptance.md).

## Security and Compatibility Boundaries

- Oracle's parser and executor continue to accept only certified narrow SELECTs; writes, transactions, functions, complex JOINs, subqueries, CTEs, system catalogs, and unconfirmed structures remain rejected. B2 column-level authorization and B5 sessions do not extend to Oracle.
- The least privilege run account does not have authenticated `PLAN_TABLE` permissions, so this Oracle Gateway path does not pre-execute `EXPLAIN PLAN` or extrapolate the scheduled results of the administrative account to it. R004/R005 planning estimates are not applicable; the maximum number of execution lines is still in effect.
- There are no metadata/audit schema changes in this version. Upgrading from v2.1.0 still requires a complete set of backups, read-only preflights, and isolation recovery drills for the SQLite or PostgreSQL metadata/audit sub-database. Dual-gateway Kubernetes still requires a maintenance window, and new and old images cannot be mixed.
- Synthetic testing of Oracle Free named containers does not equate to Oracle 19c, commercial 23ai/26ai, RAC, Data Guard, TCPS, sustained load, or customer production deployment certification; this release does not claim full production support or vendor certification. The official website Live Demo continues to use fixed synthetic PostgreSQL data and does not display it as a real Oracle database instance.

The post-release results of the officially released CI, installation package, signature, GHCR, official website and public website Live Demo can be found in [, the official release evidence ](v2.2.0-release-evidence.md).

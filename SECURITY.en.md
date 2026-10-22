# AgentSQL Security Policy

**Language:** [English](SECURITY.en.md) | [Simplified Chinese](SECURITY.md)

## Supported version

AgentSQL only evaluates and provides security fixes for the maintenance series listed in the table below; whether to provide a patch for a problem will also be judged based on the impact scope, repair risk, and release status.

| version | Status | Security fix support ends |
| --- | --- | --- |
| v1.1.x–v1.8.x (subject to the official Release) | support | Fixed deadline has not yet been announced; this table will be updated when further announcements are made |
| v1.0.x (subject to the official Release) | support | Fixed deadline has not yet been announced; this table will be updated when further announcements are made |
| v0.9.x (released series) | support | Fixed deadline has not yet been announced; this table will be updated when further announcements are made |
| v0.8.x (subject to the official Release) | support | Fixed deadline has not yet been announced; this table will be updated when further announcements are made |
| v0.7.x (subject to the official Release) | support | Fixed deadline has not yet been announced; this table will be updated when further announcements are made |
| v0.6.x (released series) | support | Fixed deadline has not yet been announced; this table will be updated when further announcements are made |
| v0.5.x (released) | support | 2027-10-31 (Original policy: 6 months after v0.6.0 is published, and no earlier than this date) |
| v0.4.x (released) | support | 2027-04-30 |
| 0.3.x and earlier | EOL | Security fixes no longer available |

The release status of v0.6.1 to v0.9.0 is subject to [GitHub Releases](https://github.com/cuipengdba/agentsql/releases). Only verifiable official Releases are counted as released versions. This table only describes the security fix maintenance period of AgentSQL itself and does not constitute a complete production support commitment for the following databases.

Please reproduce it in a controlled environment first, and try to use the latest supported version to confirm that the problem still exists.

## v1.8.0 Vastbase G100 TLS Boundary

Type `vastbase` in G100 V3.0 Build 9 PG Mode Isolated Single Instance Authentication `verify-full`, correct and incorrect CA, hostname, clear text deny, certificate and credential rotation. This path still only accepts read-only SELECT with clear schema, single table, non-permanent WHERE and LIMIT, and must use the least privilege account, Agent table-level authorization, masking and persistent auditing. The server `ssl=on` and the read-only account are configured with `hostnossl ... reject` first; `require_ssl=on` of the specified Build rejects the current pgx protocol 3.0 startup package and is not an available configuration. Existing empty TLS modes are still reserved for compatibility and are not considered authenticated encrypted connections. Temporary licensing, single instance, and synthetic data results do not represent official licensing, HA, production capacity, or vendor certification. For evidence and other untested boundaries, see [v1.8.0 and acceptance ](docs/en/v1.8.0-acceptance.md).

## v1.7.0 HighGo Independent Access Boundary

`highgo` is an independently registered data source type, limited to HighGo 9.0.10 Enterprise Edition, PG mode, and single-instance synthetic data environment. The gateway verifies the manufacturer's precise version, PG mode and read-only transaction status for each new connection; only SELECT with explicit schema, single-table direct column projection, non-constant WHERE and LIMIT is allowed. Minimum privilege database accounts, Agent table-level policies, masking and persistent auditing are all necessary conditions for delivering results; audit failures, permission revocation, database shutdown, timeout and cancellation are all tested according to the designated instance of [ acceptance record ](docs/en/v1.7.0-acceptance.md). PostgreSQL B2 column-level binding and B5 write transactions do not extend to HighGo, other versions/modes, TLS, HA, production loads and vendor certifications are not verified. The following v0.5.0 grouping is in historical status and should not be used as the access list of v1.7.0.

## Database access status and security boundary (v0.5.0 GA)

The following groupings are stated based on the current code and corresponding version acceptance records, and are not a certification list for the database manufacturer; specific capabilities must still be checked according to the corresponding release notes. The registry has a total of 30 types in 8 categories (9 relational types + 21 NoSQL/vector types); KingbaseES V9 is separately verified in the specified PG mode synthesis instance, and is still not included in the 30 registration types.

Levels: 🟢 parsing, authorization, controlled execution, masking and audit passed within the stated scope. DM8 green status covers only the specified Pack3 instance and cases, not full dialect compatibility. HighGo green status covers only 9.0.10 Enterprise PG mode and synthetic tables on one instance, not full dialect compatibility. KingbaseES V9 green status covers only the basic controlled read-only security chain for V9R1C10 PG mode with synthetic tables on one instance. 🔵 Controlled read-only means connection, metadata and a read-only query subset; 🔷 connection level means ping, version, schema and read-only preview; 🟡 pending verification means a vendor environment is still needed. All measurements cover only the specified container images, topology and synthetic cases, **not official database vendor certification or a production support commitment**. Every database must be accessed through AgentSQL using a dedicated account with minimum permissions. Uncertain parsing, catalog, plans or authorization facts fail closed. Protocol compatibility does not establish identical security semantics; similar wire protocols do not automatically extend PostgreSQL B2 column authorization or B5 controlled transactions to another product.

### Relational (9 registered + 1 additional verification)

- 🟢 **PostgreSQL 14–18**: native `postgres` path; B2/B5 are still subject to their own version, directory and capability checks.
- 🟢 **MySQL 8.0**: native `mysql` path; does not inherit PostgreSQL B2/B5 certification.
- 🔵 **Oracle AI Database 26ai Free, server 23.26.3.0.0**: `FREEPDB1` specifies the temporary minimum privilege account of the synthetic instance to complete narrow SELECT, R010, mobile phone number masking, Inspector/Dify via HTTP MCP Calling and persistent auditing; database permission revocation, business database and independent audit library failures are all verified without original results. The running account has not verified `PLAN_TABLE` write permission, and this path does not pre-execute the plan; the commercial version, TCPS, active and backup, complex SQL and complete production support have not been verified, see [v2.2.0 candidate acceptance ](docs/en/v2.2.0-acceptance.md).
- 🟢 **Dameng DM8**: The read-only SELECT, column lineage, R010 table policy, audit and mobile phone number masking of Pack3 and `COMPATIBLE_MODE=0` instances passed the actual test; R006 was not verified, multi-row paging/JOIN/function/system directory was not tested, and writing was not released.
- 🔵 **YashanDB YashanDB 23.4.1.109**: Standalone `yashan` parser with narrow SELECT path connected; on Linux x86_64, `COMPAT_VECTOR=yashan` and standalone C client 23.4.7.100, minimum privilege account via real HTTP MCP Complete permission, R010 denial, mobile phone number masking and persistent audit, MCP Inspector/Dify complete permission and denial respectively. Database SELECT withdrawal, business database disconnection, and independent audit database shutdown all verify that no original rows are delivered. Only a controlled read-only subset of the specified instance and synthetic data; TLS, Cancel/Timeout, Master-Standby, Write, Transaction, and EXPLAIN are not verified or closed, see [v2.1.0 Candidate for Acceptance ](docs/en/v2.1.0-acceptance.md).
- 🔵 **SQL Server 2025 Developer (17.x)**: Specify Linux x86_64 single instance to use local test CA to strictly verify the certificate, so as to only obtain the account of target table `SELECT` and database level `SHOWPLAN` through HTTP MCP verification narrow SELECT, R010, mobile phone number masking, Inspector/Dify and persistent auditing; SELECT/SHOWPLAN withdrawal, business database and independent audit library shutdown are all verified without original results. Covers synthetic data only; other versions, Windows, Always On, Production Certificate Rotation, Full T-SQL, Write, B2/B5, Vendor Certification and Full Production Support are not verified, see [v2.3.0 Candidate for Acceptance ](docs/en/v2.3.0-acceptance.md).
- 🔵 **openGauss Community Edition 7.0.0 LTS**: The standalone `opengauss` type requires `verify-full` on official openEuler 24.03 x86_64 specified builds and synthetic single instances with read-only sessions, allowing only bounded direct column SELECT of `FROM ONLY` normal physical single tables. The minimum privilege account has completed R010, masking, approval, dual client and persistent audit through the real MCP, and the original results will not be delivered for SELECT revocation and two types of database outage failures. Other builds, GaussDB Commercial Edition, active/standby, write, B2/B5, production certificate rotation and full production support are not verified, see [v2.4.0 candidate for acceptance ](docs/en/v2.4.0-acceptance.md).
- 🔵 **Vastbase G100 V3.0 Build 9**: `vastbase` independent type completes bounded read-only, permissions, masking, auditing and certificate and credential rotation verification in the specified PG mode instance; the temporary license does not represent a formal license, and the active and backup, production load and full production support are not verified, see [v1.8.0 Release evidence ](docs/en/v1.8.0-release-evidence.md).
- 🔵 **HighGo HighGo 9.0.10**: `highgo` independent type completes bounded read-only, permissions, masking, client and faulty links in specified enterprise version PG mode synthetic instance; other versions and full production support are not verified, see [v1.7.0 release evidence ](docs/en/v1.7.0-release-evidence.md).
- 🟢 **KingbaseES KingbaseES V9 (PG mode, single instance)**: Only the PG protocol basic closed loop of the V9R1C10 synthesis table; successful startup under the writable license volume, ksql CRUD/paging/identifier, host pgx/v5 `$1` parameter binding + SCRAM, read-only role rejects writing `42501`, AgentSQL controlled SELECT allow + mobile phone number masking + column lineage + R006 comment rejection + allow/deny audit link passed the actual test; reuse `db_type=postgres`. See [ genuine library verification record ](docs/en/kingbase-v9-verification.md).

> **KingbaseES V9 Green border:** The actual license expiration date is not confirmed (the server only reports 180 days and a date field with unknown meaning). V9 Oracle mode, native Kingbase driver, V8 not tested; multi-row data/deep paging, JOIN, aggregation/function, complex expressions, type OID, system directory, EXPLAIN not tested; TLS/encrypted transmission, HA/failover, backup and recovery, B2 column-level authorization, multi-role concurrency, production load not tested. Explicit `GRANT CREATE SESSION` syntax rejected by server; session capabilities verified with `LOGIN` + `CONNECT`, SCRAM login and query. Green only refers to the above read-only controlled links and does not represent V9 full mode, long-term authorization, manufacturer certification or production support commitment; unknown capabilities continue to fail-closed.

### Key value KV (🔷 connection level)

- **Redis, Valkey (reused Redis adaptation), Memcached**: only ping, version, Schema, read-only preview; does not declare that the complete command set, write control or database native authorization has been passed.

### Document Document (🔷 connection level)

- **MongoDB, Couchbase, CouchDB**: Connection-level read-only preview; does not declare full document query or server-side security semantics passed.

### Wide-Column (🔷 connection level)

- **Cassandra, ScyllaDB, HBase**: Connection-level read-only preview; does not claim full CQL/HBase operations or write capabilities.

### Graph (🔷 connection level)

- **Neo4j, JanusGraph, NebulaGraph**: connection-level read-only preview; does not declare full graph query or traversal capabilities.

### Time-Series (🔷 connection level)

- **InfluxDB, Prometheus, TimescaleDB**: Connection-level read-only preview; does not declare full time series query or PostgreSQL B2/B5 capabilities.

### OLAP (🔷 connection level)

- **ClickHouse, Apache Doris, StarRocks**: Connection-level read-only preview; no full analysis query or write capabilities claimed.

### Vector Vector (🔷 connection level)

- **Milvus, Qdrant, Weaviate**: Connection-level read-only preview; does not declare full vector retrieval, indexing, or server-side security capabilities.

### Other agreement candidates and bounded evidence (not included in the above 27 items)

- **PolarDB for PostgreSQL 15**: The specified community mirror completes the minimum closed loop through the `postgres` path; commercial services, HA, TLS and B2/B5 are not covered in the final verification.
- **IvorySQL 5.3**: Specify the community version PG protocol path to complete the minimum closed loop; Oracle compatibility mode and HighGo commercial version do not inherit this conclusion.
- **openGauss 7.0.0-RC3 (historical preflight)**: Previously, only the minimum closed loop was completed in the designated community version PG protocol path; v2.4.0’s 7.0.0 LTS independent type and controlled read-only evidence are shown above. RC3 results are not a substitute for LTS, GaussDB Commercial Edition, or B2/B5 acceptance.
- **HighGo SEE 4.5**: Specify the third-party image PG protocol path to complete the minimum closed loop; this old record is established with the following V9.0.10 Enterprise Edition real database verification.
- 🟢 **HighGo HighGo 9.0.10 Enterprise Edition**: PG mode, single instance synthetic table range, parsing/column lineage, table-level authorization, controlled read-only SELECT, mobile phone number masking, R006 rejection, allow/deny audit link actual test pass; reuse `postgres` data source path. Not tested for TLS, connection pool failover, cancellation/timeout, EXPLAIN, extension type/OID, system catalog differences, B2 column-level authorization, parse-error audit branch (not triggered this round), cross-version and production load; does not represent vendor certification or production support commitment. For evidence, see [HighGo 9.0 real database verification ](docs/en/highgo-verification.md).
- **OpenTenBase v2.5.0**: Specify single-machine GTM/CN/DN topology and known plan form to complete the minimum closed loop; production topology, other plan forms and B2/B5 are not proven.
- **TDSQL for PostgreSQL**: PG protocol candidate, commercial version pending final verification in the target environment; OpenTenBase results cannot be used instead.
- **TDSQL for MySQL**: MySQL protocol candidate, the commercial version is subject to final verification in the target environment; not to be confused with TDSQL for PostgreSQL.
- **TXSQL**: OpenTenBase community MySQL direction, independent MySQL route; the product-specific path has not been tested and cannot be replaced by MySQL 8 or OpenTenBase results.
- **TiDB 7.5.1 / OceanBase CE 4.4.2.1**: MySQL compatible candidate, EXPLAIN adaptation and regression fixture has been implemented; the fixture is not a complete closed loop of the target environment or manufacturer certification.
- **PolarDB-X**: The old official `2.0.1` single container is bounded by the `mysql` path and tested; complete CN/DN/CDC, commercial services, distributed semantics and B2/B5 are not proven.

> **YashanDB client redistribution authorization:**’s existing record is verbal authorization from the manufacturer (C client redistribution), and there is no written authorization document in the repository. Official Linux packages and GHCR images must check the content and SHA-256 of the independent client 23.4.7.100 on an architecture-by-architecture basis, and the results are subject to the corresponding Release evidence. The library that comes with the server image returned `YAS-02143` in the early Go driver probe; this round uses an independent client to pass the real HTTP MCP and persistent audit test. When building from source code, you should obtain a client with matching architecture, set `LD_LIBRARY_PATH` before starting, and compile the Go driver with `CGO_ENABLED=1` and `-tags yashan`; if the driver or runtime library is missing, the connection fails and is closed. The candidate version completes the data source visibility preflight of certified SQL tools and rejects auditing. Unparsed SQL does not write the record; see the corresponding document for the acceptance boundary.

For reproducible evidence of the access status, see [ compatibility survey ](docs/en/ecosystem-db-compat-research.md), [v0.5 release notes ](docs/en/release-notes-v0.5.md) and [KingbaseES/HighGo joint case ](docs/en/joint-case-kingbase-highgo.md). For product line differentiation, see Tencent’s official [TDSQL PostgreSQL version ](https://cloud.tencent.com/document/product/1129), [TDSQL MySQL version ](https://cloud.tencent.com/product/dcdb), and OpenTenBase’s [ independent TXSQL download portal ](https://docs.opentenbase.org/en/download/); protocol compatibility does not mean the same security semantics.

## Report bugs privately

Do not disclose unpatched vulnerabilities, exploit code, real credentials, or sensitive data in public issues, discussions, pull requests, blog sites, or social media.

Please use one of the following private channels:

- Send email to [87326549@qq.com](mailto:87326549@qq.com);
- If the repository Security page displays "Report a vulnerability", use [GitHub private vulnerability reporting portal ](https://github.com/cuipengdba/agentsql/security/advisories/new); if the portal is unavailable, please use the above email. Do not use a public issue instead.

Please try to include in your report:

- Affected AgentSQL versions, commit numbers and deployment methods;
- Operating system, database type and version, and MCP access method;
- Problem description, reproducible steps and minimal configuration;
- Actual impact, attack prerequisites, required permissions and whether the gateway can be bypassed;
- Observed logs or error messages, and removal of keys, passwords, DSNs, and business data;
- Recommended fixes or mitigations (if any);
- Safe way to obtain PoC. Please prioritize providing minimal, harmless reproductions; do not use public links to spread exploit code, and do not send malicious payloads or real data without confirmation.

PGP public keys are not currently available. If the material is not suitable for sending directly by email, please send a description without sensitive details first and negotiate the subsequent transmission method.

## Response and Disclosure Process

After receiving the report, we usually handle it according to the following process:

1. Confirm receipt and check that the report is complete. We will try to provide an initial response within 3 working days.
2. Reproduce and assess the impact, identifying affected versions, severity, and interim mitigation measures. We will try to provide an initial review within 7 working days; complex issues may take longer.
3. Coordinate fixes, regression verification, release planning and disclosure timing in private channels. The repair time depends on the complexity of the problem and the release risk, and does not constitute a service level commitment.
4. Coordinate public disclosure after the fix is ​​available; if necessary, give users a reasonable upgrade window before issuing security announcements and fix instructions.

We welcome reporters to participate in verification without compromising investigation and user safety. With the consent of the reporter, acknowledgments can be made in the public announcement; if you wish to remain anonymous, please indicate this in the report. Please keep vulnerability details private until disclosure is coordinated by both parties.

## Security models and boundaries

AgentSQL is a database security gateway and MCP Server for AI Agents. It performs identity authentication, SQL parsing, authorization and rule evaluation, controlled execution, result masking and application layer auditing for requests passing through the gateway to reduce the risk of Agent misoperation, unauthorized access and dangerous SQL.

The following boundaries must also be considered during deployment and risk assessment:

- The protection scope only covers requests that pass through the AgentSQL gateway and use a controlled running account to access the database. Owner, superuser, DBA or other high-privilege accounts that can bypass the gateway and directly connect to the database are not protected by AgentSQL rules.
- The database running account is still the final permission boundary. Minimum privileges, purpose-isolated accounts should be used; AgentSQL cannot compensate for over-authorization, nor can it prevent database administrators from operating directly on the database side.
- Result masking is coding based on column rules at the result layer, which is used to reduce the risk of direct exposure of specific fields. It is not a complete DLP. Complex expressions, renames, aggregations, views, or other data transformations may exceed column matching capabilities and cannot be used to claim that sensitive data has been fully discovered or blocked.
- Audit records are application layer audits, not tamper-proof ledgers, nor regulatory-level WORM, retention locks, or compliance certificates. Personnel with high privileges on the underlying storage or database may modify or delete records; additional controlled archiving, external SIEM, or compliant storage should be used if compliance requirements apply.
- PDF/ZIP compliance export contains highly sensitive auditable content such as original SQL, errors and details, and should be transferred and retained as sensitive data. ZIP's `manifest.json` uses per-file SHA-256 to detect accidental corruption, member additions and deletions, and payload substitutions, but has no signatures or external time anchors; someone who can modify both the payload and the manifest can generate new self-consistent archives. `agentsqlctl audit verify-archive` therefore only reports intra-package consistency and does not certify source, owner, build time, or latestness.
- Rules, approvals, and fail-closed processing are designed to mitigate known risks, but are not guaranteed to identify all SQL semantics, business logic abuses, database vulnerabilities, or unknown attacks.
- Remote deployment of Streamable HTTP requires configuration of HTTPS, network access control, and credential management by the operation and maintenance party; AgentSQL's native HTTP example does not represent a public network security deployment solution.

When you find an issue that breaks the above expected boundaries, causing privilege escalation, authentication bypass, sensitive information leakage, or audit avoidance, please report it privately in accordance with this policy.

## PolarDB-X (MySQL protocol path) security boundary

PolarDB-X is accessed through `db_type=mysql`, does not add the `polardbx` data source type, and does not use the server product name to bypass MySQL parser, rules or executors. This path represents bounded MySQL protocol compatibility and does not represent full PolarDB-X dialect support, vendor certification, or production availability commitments.

- Only reuse the subset of MySQL single statements that AgentSQL can strictly parse and classify. PolarDB-X's routed hints, DRDS/TDDL management statements, distributed DDL, stored procedures, and other unrecognized extensions are not automatically approved for protocol compatibility; fail to close if parsing or classification is indeterminate.
- discovery uses fixed `information_schema.tables` / `information_schema.columns` queries with backticks, `LIMIT` sampling statements, and is limited to the current `DATABASE()`. Failed to close due to insufficient directory permissions, result overrun, return column shape change, or cross-database range.
- If and only if the ordinary `EXPLAIN` returns the exact single column `LOGICAL EXECUTIONPLAN`, the executor appends the officially defined `EXPLAIN EXECUTE` and normalizes the DN plan according to the MySQL `type` / `key` / `rows` table. Second-stage errors, unknown columns, empty schedules, illegal estimates, or oversized results will not be downgraded to zero risk or R004/R005 skipped.
- SELECT continues to accept executor row limits, context deadlines, and single-statement limits. `MAX_EXECUTION_TIME` Hint is only a depth restriction and cannot replace client cancellation; when the target version ignores Hint, the connection/query will still be closed by the context deadline.
- INSERT / UPDATE / DELETE reuses MySQL DML parsing, R002/R003/R006/R201--R204, explicit write transactions and error masking. Distributed transactions, GSI, partition key updates, broadcast tables, and cross-shard semantics must be verified separately in the target topology; protocol executability does not equate to atomicity, isolation level, or commit results being certified.
- The MySQL path does not provide PostgreSQL B2/B5 native binder certification. Table/column policies, result masking and application auditing are still performed by AgentSQL and are not a replacement for PolarDB-X native account permissions, network isolation, TLS, auditing or backups.
- Production environments must use dedicated minimum privilege accounts and the TLS/authentication configuration required by the target version. `polardbx_root/123456` of the official experience image is only used for isolated local testing and is prohibited from being used as production credentials; the root password of the Kubernetes Operator is randomly generated by Secret and should not be written to the repository or logs.

Version and deployment conclusions must record the CN/DN/CDC topology, PolarDB-X version, driver version and test scope respectively. The old `polardbx/polardb-x:2.0.1` single container can only be used as a development smoke environment and does not represent the current community edition, Operator cluster or Alibaba Cloud commercial services.

## SQL Server 2025 security boundaries

`db_type=sqlserver` is a standalone SQL Server 2025 (major version 17) limited read-only capability and does not represent full T-SQL, DML, DDL, stored procedures, SQL Agent, CLR, linked servers, or B2/B5 support.

- Transmission must be encrypted. The default is `tls_mode=strict`, which corresponds to TDS 8.0 strict encryption and certificate verification; `verify-full` corresponds to the compatible path of `encrypt=true`. There is no configuration to turn off TLS. `trust_server_certificate=true` skips server-side authentication and can only be explicitly combined with `verify-full` in an isolated development environment. Use in production environments is prohibited.
- The database account must follow least privileges: grant only `SELECT` to the target library/schema/table, controlled metadata visibility, and `SHOWPLAN` required for estimation planning. Do not grant `sysadmin`, `CONTROL SERVER`, `db_owner`, write permissions, DDL, stored procedure execution, impersonation, external data source, or file access permissions.
- The gateway only accepts a narrow read-only `SELECT`. Write operations (including those with `WHERE`), procedure calls, batch processing, dynamic data sources, `xp_cmdshell`, OLE Automation, `BULK`, `WAITFOR`, `DBCC`, session-level `SET/USE`, and unproven safe syntax all failed to close. When parsing fails, the original SQL will not be handed over to the driver for try.
- discovery uses a fixed, parameterized `sys.tables/sys.schemas/sys.columns/sys.types` query. SQL Server's metadata visibility will hide objects according to database permissions; insufficient permissions, directory results exceeding limits, or abnormal result shapes are handled as failures without expanding permissions or falling back to the SQL provided by the caller.
- Dynamic scan rules use the estimated schedule of `SHOWPLAN_XML`; target `SELECT` is not executed during this phase. Plan XML parsing has size, depth, and node limits, and plans that cannot be verified refuse to continue. `SHOWPLAN` itself may reveal object and plan information, so only dedicated gateway accounts should be granted and the original plan should not be exposed to end users.
- `db_type` in the audit will be recorded as `sqlserver`, but it still belongs to the application layer audit, and the retention and tamper-proof boundaries are the same as other databases in this policy. Driver origin errors, DSNs, passwords, certification paths, and business object names must not be written to client-facing errors or general audit details.
- The SQL Server path does not use the PostgreSQL B2/B5 native binder and does not provide column-level attestation for PostgreSQL. Result Masking is also not a replacement for database-native RLS, Dynamic Data Masking, or full DLP.

Deployment configuration and driver behavior should be based on Microsoft's [Go driver encryption and certificate ](https://learn.microsoft.com/en-us/sql/connect/golang/encryption-certificates?view=sql-server-ver17), [Go driver security best practices ](https://learn.microsoft.com/en-us/sql/connect/golang/security-best-practices?view=sql-server-ver17) and [`SET SHOWPLAN_XML`](https://learn.microsoft.com/en-us/sql/t-sql/statements/set-showplan-xml-transact-sql?view=sql-server-ver17) documents.

## MFA/OIDC/LDAP Security Boundary

Person authentication for v0.5 does not bypass AgentSQL RBAC. OIDC/LDAP groups can only be mapped to explicit role IDs within a specified tenant; fail-closed when the mapping is empty, the role does not exist, the directory result is not unique, the external service is unavailable, or the authentication status cannot be read. The TOTP key is encrypted and saved, and the recovery code and refresh token only save the digest; OIDC uses Authorization Code + PKCE, one-time state/nonce, discovery and RS256 JWKS verification; LDAP only allows LDAPS or StartTLS that passes the certificate verification, and verifies the password through user bind. For complete configuration and restrictions, see [v0.5 Identity Authentication Guide ](docs/en/AUTHENTICATION.md).
## Online Upgrade Security Boundary / Online Upgrade Security Boundary

`agentsqlctl upgrade check`Only from explicit`--manifest-url`or`AGENTSQL_UPGRADE_MANIFEST_URL`Read HTTPS JSON manifest (`version`、`url`、`sha256`) and report newer, same version, or older version.`upgrade apply --backup-dir <新目录> --dry-run`Reads manifests and originating HTTPS downloads, limits size, calculates and compares SHA-256, and checks current executable, configuration files, and backup directory paths; it does not create backups, stop services, replace binaries, or run migrations. Redirects, HTTP, downgraded installations, invalid versions, or digest inconsistencies are rejected.`--yes`Actual installation cannot currently be enabled; non-dry-run`apply`Always refuse.

HTTPS and SHA-256 only provide transport checksum manifest consistency. If both the manifest and its digest are tampered with, the verification may still succeed; this process has no signatures, independent trust anchors, publisher certifications, or rollback-proof proofs. Operators must verify release sources through trusted independent channels, perform separate backups of programs, configurations, databases, and matching keys during maintenance windows, verify recovery plans, and install through an independently audited deployment process. The version page of the console only displays the current running version and preflight instructions, and does not provide remote installation.

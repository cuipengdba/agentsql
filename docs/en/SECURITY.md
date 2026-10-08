> Sources: [Security Policy](../../SECURITY.md), [Chinese User Guide](../USER_GUIDE.md), [Architecture Specification](../SPEC.md), and the current rule implementation.

# AgentSQL Security Model

## Database Compatibility and Security Status (as of 2026-10-08)

This table describes evidence for the **pending v0.5.0 code**, not vendor certification, a production support commitment, or a change to AgentSQL's security-fix support policy. 🟢 means the parsing, authorization, controlled execution, masking, and audit chain passed within the stated scope; the DM8 and HighGo green tiers apply only to their named instances and cases. A tested protocol path means only the named image, topology, and synthetic cases were exercised. Every path still requires gateway routing, a dedicated least-privilege database account, and failure closed when parsing, catalog, plan, or authorization facts are uncertain. PostgreSQL B2 column authorization and B5 controlled transactions do not transfer to other products merely because they speak a similar protocol.

| Product and target | AgentSQL path and evidence | Remaining boundary |
| --- | --- | --- |
| PostgreSQL 14–18 | Native `postgres` path | B2/B5 retain their own version, catalog, and capability checks |
| PolarDB for PostgreSQL 15 | Named community image completed a minimal `postgres` path exercise | Commercial service, HA, TLS, and B2/B5 need separate validation |
| IvorySQL 5.3 | Named community edition completed a minimal PG protocol exercise | No inference for Oracle mode or commercial HighGo |
| openGauss 7.0.0-RC3 | Named community edition completed a minimal PG protocol exercise | No inference for commercial GaussDB or B2/B5 |
| HighGo SEE 4.5 | Named third-party image completed a minimal PG protocol exercise | This older record is separate from the V9.0.10 enterprise-edition live test below |
| 🟢 HighGo 9.0.10 Enterprise Edition | PG mode, single instance, synthetic table: parsing/column lineage, table-level authorization, controlled read-only SELECT, phone-number masking, R006 denial, and allow/deny audit chain passed a live test through the `postgres` data source path | TLS, pool failover, cancellation/timeouts, EXPLAIN, extended types/OIDs, system-catalog differences, B2 column authorization, parse-error audit branch (not triggered), cross-version behavior, and production workloads remain untested; no vendor certification or production support commitment. See the [HighGo verification record](../highgo-verification.md) |
| OpenTenBase v2.5.0 | Named single-node GTM/CN/DN topology and known plan shapes completed a minimal exercise | Production topology, other plans, and B2/B5 unproven |
| KingbaseES V9 | Vendor environment pending; an old third-party image could not run because its license expired | V9R1C10 target environment was expected on October 8; validate PG mode before any support claim |
| MySQL 8.0 | Native `mysql` path | No PostgreSQL B2/B5 proof |
| SQL Server 2025 (17.x) | Independent `sqlserver` strict read-only subset; see below | No full T-SQL, writes, B2/B5, or vendor certification |
| TDSQL for PostgreSQL | PG protocol candidate, awaiting commercial target environment | OpenTenBase results do not establish this product's status |
| TDSQL for MySQL | MySQL protocol candidate, awaiting commercial target environment | Separate from TDSQL for PostgreSQL |
| TXSQL | OpenTenBase community MySQL direction (TXSQL); separate MySQL route with no product-specific live test | MySQL 8 or OpenTenBase results do not establish this route |
| Oracle Database 23ai | Independent `oracle` strict SELECT subset | No claim for Enterprise Edition or full Oracle SQL |
| YashanDB 23.4.1.109 | Independent `yashan` slice; connectivity, catalog, and ordinary Query fail-closed behavior retested | EXPLAIN, writes, and full security pipeline unproven |
| DM8 | Independent `dm` bounded SELECT subset exercised | Full grammar, writes, and column security pipeline incomplete |
| TiDB 7.5.1 / OceanBase CE 4.4.2.1 | MySQL compatibility candidates; EXPLAIN adapters and regression fixtures exist | Fixtures do not establish a full target-environment exercise or certification |
| PolarDB-X | Old official `2.0.1` all-in-one image exercised through `mysql`; see below | Full CN/DN/CDC, commercial service, distributed semantics, and B2/B5 unproven |

> **YashanDB client redistribution authorization:** AgentSQL v0.5.0 release artifacts (Linux tarballs, systemd native packages, GHCR runtime images) bundle the YashanDB C client (client 23.4.7.100, with yashandb-go v1.4.4) runtime libraries for the matching architecture. They are redistributed on the basis that the project maintainer has declared vendor authorization; no written authorization file was supplied to this repository. This does not change the low-tier YashanDB support scope: connectivity and metadata discovery are retested, while ordinary Query, writes, transactions, and EXPLAIN remain fail-closed.

Reproducible AgentSQL evidence is in the [compatibility research](../ecosystem-db-compat-research.md), [v0.5 release notes](../release-notes-v0.5.md), and [KingbaseES/HighGo case record](../joint-case-kingbase-highgo.md). Product distinctions follow vendor material for [TDSQL for PostgreSQL](https://cloud.tencent.com/document/product/1129), [TDSQL for MySQL](https://cloud.tencent.com/product/dcdb), and OpenTenBase's [separate TXSQL download](https://docs.opentenbase.org/en/download/).

## Scope and Trust Boundaries

AgentSQL protects SQL requests that pass through the gateway and use its controlled database runtime account. It authenticates the caller, parses one SQL statement, evaluates authorization and safety rules, executes within configured limits, masks selected result columns, and writes application-level audit evidence.

It does not protect a connection that bypasses the gateway. A database owner, superuser, DBA, or any other principal that can connect directly remains outside AgentSQL's enforcement. The database runtime account is therefore the final authorization boundary and must have only the permissions the gateway actually needs.

AgentSQL also does not replace network isolation, TLS termination, secret management, database backups, native row-level security, change control, SIEM retention, or vulnerability management.

## Decision Pipeline

```text
request -> authentication -> parse -> authorization -> static rules
                                                    -> dynamic checks when required
                                                    -> decision
  allow/warn -> controlled execution -> result masking -> audit
  deny       -> no SQL execution                    -> audit
  approve    -> no SQL execution -> pending record -> audit
  error      -> fail closed                         -> best-effort audit
```

`approve` is not an execution token. An approved record does not run the original SQL and does not exempt a later retry from a fresh evaluation.

Tool-level validation may reject malformed calls before the pipeline, so not every client error is guaranteed to have an audit event.

## Built-in Rules

The generic rule set is evaluated in stable R001–R010 order:

| Rule | Security purpose | Normal decision when triggered |
| --- | --- | --- |
| R001 | Reject stacked/multiple SQL statements | `deny` |
| R002 | Reject `UPDATE`/`DELETE` without a restricting `WHERE`, including tautologies | `deny` |
| R003 | Stop a `readonly` Agent from writing or changing schema | `deny` |
| R004 | Route a query whose EXPLAIN scan estimate exceeds the threshold | `approve` |
| R005 | Warn about an unbounded large result when the required EXPLAIN data exists | `warn` |
| R006 | Reject stacked statements, unknown statement types, and SQL comments used as bypass signals | `deny` |
| R007 | Reject known resource-exhaustion functions such as PostgreSQL `pg_sleep` and MySQL `sleep`/`benchmark` | `deny` |
| R008 | Enforce per-Agent QPS and concurrency limits | `deny` |
| R009 | Route excessive SQL length, nesting, or UNION complexity | `approve` |
| R010 | Reject tables outside the allow policy, explicitly denied tables, and unauthorized single-table projections | `deny` |

PostgreSQL-specific rules R101–R107 cover high-risk DROP, lock-heavy operations, administrative/file functions, COPY PROGRAM, unindexed bulk writes, large-table DDL, and long/idle transactions. MySQL-specific R201–R204 cover file access, dangerous bulk writes, high-risk administration, and large transactions.

R005 runs in the normal production dynamic stage. An unbounded query whose controlled EXPLAIN estimate is strictly greater than the effective `row_limit` produces a structured `R005` warning; actual execution-layer truncation also adds R005 on both the generic and PostgreSQL column-authorization paths, even when the SQL contains a larger explicit `LIMIT`. The hit is returned in `assessment.hits`, persisted in audit `rule_hits`, and available through the audit management API. There is no separate R005-specific response header or log event. Missing EXPLAIN or audit facts continue to fail closed.

Built-in rules can be enabled or disabled, but their actions and internal thresholds are not editable in the current Rules page. R005 uses the data source's configurable `row_limit` as its default warning and execution threshold (1000 when the value is zero/unset; negative values are invalid). Custom rule definitions can be stored through CRUD APIs but are not assembled into the execution engine.

## Authorization and Fail-Closed Behavior

- No matching Agent/data-source authorization means deny.
- `*` and `schema.*` are broad grants that authorize all columns on matching tables; they are not safe defaults.
- PostgreSQL column authorization is enabled by default. MySQL does not use the PostgreSQL B2 path.
- Column allowlists are enforced for reliably attributable single-table projections. JOIN and self-join authorization remains table-level.
- Parse errors, missing policy decisions, invalid rule dependencies, and unsupported/unknown statement types do not fall through to execution.
- Each operation accepts only one top-level SQL statement.

`row_filter` is persisted and displayed but is not evaluated by the decision engine. It must not be described as row-level security. Use database-native RLS or security views.

## Result Masking

Masking happens after the database returns rows; it does not change stored values or prevent the database from using original values in `WHERE`, `JOIN`, `GROUP BY`, or other expressions.

| Algorithm | Supported sensitive types | Key required | Output property |
| --- | --- | --- | --- |
| `mask` | `phone`, `email`, `idcard`, `bankcard`, `ip`, `birthdate` | No | Preserves limited format-specific fragments |
| `hash` | All nine types | Yes, dedicated HMAC material of at least 32 bytes | Deterministic fingerprint within the same key/version |
| `block` | All nine types | No | Replaces every non-empty value with exactly `***` |
| `range` | `number`, `date` only | No | Numeric bucket or year/quarter/month truncation |

The nine types are the six formatted types above plus `generic`, `number`, and `date`.

Never reuse or derive the redaction hash key from `AGENTSQL_SECRET`. `hash` is an irreversible fingerprint, not encryption. Its determinism reveals equality/frequency, and sharing a key can enable correlation across data sets. `block` still reveals result shape, row count, column name, and empty/NULL state. `range` preserves coarse distributions and is not anonymization.

### Unresolved column lineage

For a direct top-level projection that can be mapped to a physical table, a table-scoped rule matches the source table and column. JOIN bare columns, `SELECT *`, and outer CTE projections can make lineage uncertain. If an unresolved result column matches a protected table-scoped column and that table may participate in the statement, AgentSQL fails closed for that cell by replacing every non-empty value with `***`; this fallback cannot be disabled.

Qualify protected columns with a table name or alias and avoid `SELECT *`. Complex expressions, aliases, aggregation, casts, UNIONs, and view-internal renames can still defeat source-column attribution. Result masking is therefore not complete DLP.

## Secrets and Authentication

- `AGENTSQL_SECRET` must be exactly 32 bytes. It encrypts data-source passwords and derives the console-token signing key.
- Agent API Keys are shown in plaintext only when created or rotated; storage contains a SHA-256 digest. Rotation invalidates the old Key immediately.
- Console users and MCP Agents are separate security domains.
- `AGENTSQL_REDACTION_HASH_KEY` or the versioned key manifest protects HMAC masking and must be stored separately from `AGENTSQL_SECRET`.
- `AGENTSQL_INSECURE=1` only permits known test credentials in local development. It does not disable authentication, rules, or fail-closed behavior and must not be set in production.

Back up the control-plane database and its matching `AGENTSQL_SECRET` together. Never put secrets, DSNs, API Keys, Authorization headers, or real business data in source control, screenshots, client configuration templates, or logs.

## MCP Transport Security

The default HTTP listener is loopback-only. For remote use, place AgentSQL behind a controlled HTTPS reverse proxy with access control and rate limiting, or use SSH local forwarding. Do not expose `0.0.0.0:7780` without those controls.

Streamable HTTP revalidates the Bearer Agent Key on every request. In stateful mode, transport sessions are in memory and are bound to the Agent ID and API-Key hash. A session cannot be reused by another Agent or after Key rotation. Session IDs must not be logged. Service restart invalidates all transport sessions.

stdio starts a separate AgentSQL runtime; it is not a proxy to an existing service. That runtime needs access to the same configuration, control-plane storage, and matching `AGENTSQL_SECRET`.

## Audit Guarantees and Limits

Audit records are application-level evidence for engineering review and behavior tracing. They are not regulatory-grade WORM storage, retention locks, or a tamper-proof ledger. Anyone with sufficient access to the underlying SQLite/PostgreSQL storage may change or delete records. Use controlled external archival or a SIEM when compliance requires stronger retention.

CSV exports neutralize common spreadsheet formula prefixes, but JSONL is the authoritative choice when exact long text matters. Notification delivery is best-effort and does not replace the audit store; notification failure does not change the SQL decision.

PDF and ZIP compliance exports contain sensitive audit material, including SQL, errors, and details, and must be protected in transit and at rest. An archive `manifest.json` records each payload's size and SHA-256; `agentsqlctl audit verify-archive` rejects unsafe names, missing/extra/duplicate members, size changes, and digest mismatches. This proves package consistency only. The manifest is not signed and has no external time or head anchor, so an attacker who can rewrite both payloads and manifest can create a different self-consistent archive. Verification does not authenticate the producer, ownership, generation time, freshness, or WORM retention.

## Vulnerability Reporting

Do not disclose unpatched vulnerabilities, exploit payloads, real credentials, or sensitive data in public Issues, Discussions, pull requests, logs, or social media. Report privately by either:

- email: [87326549@qq.com](mailto:87326549@qq.com);
- [GitHub private vulnerability reporting](https://github.com/cuipengdba/agentsql/security/advisories/new) when the repository Security page offers “Report a vulnerability”; otherwise use the email address above. Do not substitute a public Issue.

The authoritative [security policy](../../SECURITY.md) plans to support v0.5.x after release until six months after v0.6.0 is released, but not earlier than 2027-10-31; the currently released v0.4.x remains supported through 2027-04-30. Versions 0.3.x and earlier are EOL and no longer receive security fixes.

## PolarDB-X (MySQL Protocol Path) Security Boundary

PolarDB-X is configured as `db_type=mysql`. AgentSQL does not add a `polardbx` data-source type and does not use server-product detection to bypass the MySQL parser, rules, or executor. This is a bounded MySQL-protocol compatibility path, not a claim of complete PolarDB-X dialect support, vendor certification, or production readiness.

- Only the MySQL single-statement subset that AgentSQL can strictly parse and classify is eligible. PolarDB-X routing hints, DRDS/TDDL administrative statements, distributed DDL, stored procedures, and unrecognized extensions are not admitted merely because the wire protocol is compatible. Ambiguity fails closed.
- Discovery uses fixed queries over `information_schema.tables` and `information_schema.columns`, backtick quoting, and bounded `LIMIT` sampling within the current `DATABASE()`. Insufficient catalog permission, excessive results, an unexpected result shape, or cross-database scope fails closed.
- If and only if plain `EXPLAIN` returns exactly one `LOGICAL EXECUTIONPLAN` column, the executor issues the documented `EXPLAIN EXECUTE` form and normalizes the DN plan from MySQL `type`, `key`, and `rows` columns. A second-stage error, unknown columns, an empty plan, invalid estimates, or an oversized payload never falls back to zero risk or skips R004/R005.
- SELECT remains bounded by the executor row limit, context deadline, and one-statement rule. `MAX_EXECUTION_TIME` is defense in depth, not a substitute for client cancellation; the context deadline remains authoritative when a target version ignores the hint.
- INSERT, UPDATE, and DELETE reuse MySQL DML parsing, R002/R003/R006/R201--R204, explicit write transactions, and error redaction. Distributed transactions, GSIs, partition-key updates, broadcast tables, and cross-shard semantics require separate validation on the target topology. Protocol-level execution does not certify atomicity, isolation, or commit-outcome behavior.
- The MySQL path has no PostgreSQL B2/B5 native-binder proof. AgentSQL table/column policy, result masking, and application audit do not replace PolarDB-X account privileges, network isolation, TLS, native audit, or backups.
- Production deployments require a dedicated least-privilege account and the target version's TLS/authentication settings. The official demo image's `polardbx_root/123456` credential is only for isolated local testing and must not be used in production. Operator deployments generate the root password in a Kubernetes Secret; do not commit or log it.

Compatibility evidence must identify the CN/DN/CDC topology, PolarDB-X version, driver version, and exercised scope. The old `polardbx/polardb-x:2.0.1` all-in-one image is only a development smoke environment and does not represent current community releases, Operator clusters, or Alibaba Cloud commercial service.

## SQL Server 2025 Security Boundary

`db_type=sqlserver` is an independent, restricted read-only capability for SQL Server 2025 (major version 17). It is not a claim of complete T-SQL, DML, DDL, stored-procedure, SQL Agent, CLR, linked-server, or B2/B5 support.

- Transport encryption is mandatory. `tls_mode=strict` is the default and uses TDS 8.0 strict encryption with certificate validation. `verify-full` maps to the compatible `encrypt=true` path. There is no plaintext mode. `trust_server_certificate=true` skips server identity validation and is accepted only when explicitly combined with `verify-full` for isolated development; do not use it in production.
- Use a dedicated least-privilege login with only the required table `SELECT`, controlled metadata visibility, and `SHOWPLAN` permission for estimated plans. Do not grant `sysadmin`, `CONTROL SERVER`, `db_owner`, writes, DDL, procedure execution, impersonation, external-source, or file-access privileges.
- The gateway accepts one narrow read-only `SELECT`. All writes (with or without `WHERE`), procedure calls, batches, dynamic sources, `xp_cmdshell`, OLE automation, `BULK`, `WAITFOR`, `DBCC`, session-level `SET/USE`, and unproven syntax fail closed. A parse failure is never retried as raw driver SQL.
- Discovery uses fixed parameterized queries over `sys.tables`, `sys.schemas`, `sys.columns`, and `sys.types`. SQL Server metadata visibility still applies. Insufficient permission, excessive results, or an unexpected result shape fails closed; the gateway does not elevate or fall back to caller-supplied SQL.
- Dynamic scan assessment consumes an estimated `SHOWPLAN_XML`; it does not execute the target query. XML size, depth, and node count are bounded. Because plan access can reveal object and plan details, grant `SHOWPLAN` only to the dedicated gateway login and do not expose raw plans to end users.
- Audit records carry `db_type=sqlserver`, but remain application-level evidence with the same retention and tamper-resistance limits described above. Raw driver errors, DSNs, passwords, certificate paths, and business object names must not appear in client errors or ordinary audit detail.
- The SQL Server path does not use the PostgreSQL B2/B5 native binder and provides no PostgreSQL column-proof guarantee. Result masking is not a replacement for native RLS, Dynamic Data Masking, or complete DLP.

Use Microsoft's [Go driver encryption and certificate guidance](https://learn.microsoft.com/en-us/sql/connect/golang/encryption-certificates?view=sql-server-ver17), [Go driver security practices](https://learn.microsoft.com/en-us/sql/connect/golang/security-best-practices?view=sql-server-ver17), and [`SET SHOWPLAN_XML`](https://learn.microsoft.com/en-us/sql/t-sql/statements/set-showplan-xml-transact-sql?view=sql-server-ver17) as the operational references.

## MFA, OIDC, and LDAP boundary

Human federation does not bypass AgentSQL RBAC. OIDC groups and LDAP groups map only to explicit role IDs inside one configured tenant, and an empty, ambiguous, or invalid mapping fails closed. TOTP secrets are encrypted at rest; recovery codes and refresh tokens are stored only as digests. OIDC uses Authorization Code + PKCE, one-shot state, nonce, discovery, and RS256 JWKS verification. LDAP requires certificate-validated LDAPS or StartTLS and proves the submitted password with a user bind. See the [v0.5 authentication guide](AUTHENTICATION.md) for configuration and operational limitations.
## Online Upgrade Security Boundary

`agentsqlctl upgrade check` reads an HTTPS JSON manifest (`version`, `url`, `sha256`) from an explicit `--manifest-url` or `AGENTSQL_UPGRADE_MANIFEST_URL` and reports newer, equal, or older releases. `upgrade apply --backup-dir <new-directory> --dry-run` reads the manifest and a same-origin HTTPS artifact, bounds their sizes, checks the artifact SHA-256, and inspects the current executable, configuration file, and backup path. It creates no backup, stops no service, replaces no binary, and runs no migration. Redirects, HTTP, downgrade installation, malformed versions, and digest mismatches fail closed. `--yes` does not enable installation; `apply` without `--dry-run` is rejected.

HTTPS and SHA-256 establish transport and consistency with the fetched manifest. An attacker able to replace both manifest and digest can provide a self-consistent artifact. There is no signature, independent trust anchor, publisher authentication, or anti-rollback proof. Operators must verify the release through an independent trusted channel, separately back up the executable, configuration, database, and matching secrets, test restoration, and use a reviewed deployment procedure during a maintenance window. The console release page only displays the running version and preflight instructions.

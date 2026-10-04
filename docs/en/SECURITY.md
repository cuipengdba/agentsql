> Sources: [Security Policy](../../SECURITY.md), [Chinese User Guide](../USER_GUIDE.md), [Architecture Specification](../SPEC.md), and the current rule implementation.

# AgentSQL Security Model

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

## Vulnerability Reporting

Do not disclose unpatched vulnerabilities, exploit payloads, real credentials, or sensitive data in public Issues, Discussions, pull requests, logs, or social media. Report privately by either:

- email: [87326549@qq.com](mailto:87326549@qq.com);
- [GitHub private vulnerability reporting](https://github.com/cuipengdba/agentsql/security/advisories/new).

The authoritative [security policy](../../SECURITY.md) supports v0.5.x until six months after v0.6.0 is released, but not earlier than 2027-10-31; v0.4.x remains supported through 2027-04-30. Versions 0.3.x and earlier are EOL and no longer receive security fixes.

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

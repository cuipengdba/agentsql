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

R005 has an important boundary: the normal production pipeline does not currently run its EXPLAIN-dependent warning stage by default, so the guaranteed production control is execution-layer `row_limit` truncation. The Live Demo has a dedicated dynamic stage; its R005 behavior must not be generalized into a production guarantee.

Built-in rules can be enabled or disabled, but their actions and internal thresholds are not editable in the current Rules page. Custom rule definitions can be stored through CRUD APIs but are not assembled into the execution engine.

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

The Chinese [security policy](../../SECURITY.md) currently lists 0.3.x as the supported series while the repository is prepared for pending v0.5.0 and the externally published assets remain v0.4.0. This is an unresolved documentation discrepancy; confirm the supported security-fix series privately with the maintainers rather than assuming coverage.

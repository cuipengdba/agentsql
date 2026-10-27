# AgentSQL Architecture and Engineering Specifications

**Language:** [English](SPEC.md) | [Simplified Chinese](../SPEC.md)

> Current specification: v3.0.2 is released. This document also records v3.1.0 contracts under development and validation; new capabilities become released scope only after [version acceptance](v3.1.0-acceptance.md). Historical records retain their original facts. Start with the [documentation index](README.md).

> Version: v3.1.0 development specification | This document describes AgentSQL architecture, external contracts, security model and compatibility boundaries for users and contributors.
> For a complete runnable configuration example, see `examples/`, and for complete table creation DDL, see `internal/store/migrations/`; when there are differences between this article and the source code, the source code and testing shall prevail and an issue will be raised to correct this article.

---

## 1. Project Overview

### 1.1 What is

AgentSQL (Chinese name: **Zhidun**, AI-Native Database Security Gateway) is a database security gateway for AI Agent, delivered in the form of **production-grade MCP Server**. PostgreSQL/MySQL SQL requests access the business database after parsing, authentication, rule evaluation, risk decision-making, controlled execution, masking and auditing; other registered data sources can only be accessed within their respective bounded capabilities and do not automatically inherit this complete SQL security link (see Chapter 8).

- Gateway Binary: `agentsql` (MCP Bearer + Management API + Web Console)
- Operation and maintenance CLI: `agentsqlctl` (generate configuration, configuration verification, database migration, health check, demonstration seed, etc.)
- Agent API Key prefix: `asql_` (only SHA-256 hash is stored)

Product positioning **is narrow and deep**: it only does one thing: "MCP database security gateway". **does not make** generalized BI reports, does not make ORM, does not make Text2SQL large models, and **does not replace the database's own account and authorization system**.

### 1.2 Core link

Each SQL statement goes through six stages of logical pipeline (the internal implementation is divided into eight stages, see Chapter 3):

```
Parse(解析 AST) → Auth(身份与权限) → Guard(静态规则 + 动态 EXPLAIN) → Decide(allow/deny/approve)
              → Execute(限流 / 超时 / 行数上限 / 受控写) → Redact + Audit(结果脱敏 + 审计落库)
```

### 1.3 Safety Boundaries (Be sure to read before use)

AgentSQL is **gateway layer** protection, and its boundaries must be understood truthfully:

- Only constraint**Running account through the gateway**;Does not prevent use of database owner/superuser/DBA credentials**Direct connection bypassing the gateway**behavior does not replace the database account system. Production deployments should configure the gateway with a dedicated account with minimal permissions.
- Masking is **column processing within a result set**, not complete DLP. Direct top-level projections can be attributed to `[schema.]table.column`. The verified PostgreSQL/MySQL paths track protected sources through functions, expressions, aggregates, `CAST`, set operations, CTEs and derived tables. When the source is unknown and might include protected columns, non-null results are replaced with `***`. Unknown view or function internals are not fully proven.
- Derived outputs such as `CONCAT(phone,'') AS x` and `lower(phone) AS y` have been included in negative verification; paths such as statistical side channels, unknown functions, and direct library connections must still be constrained with database minimum permissions and column-level authorization. `INSERT` / `UPDATE` / DDL / `RETURNING` does not enter result masking; `RETURNING` passes the write barrier and only returns `RowCount`.
- `hash` is an HMAC irreversible fingerprint with a private key, not encryption. It is calculated after the business database returns the result and is not pushed down to JOIN/WHERE/GROUP BY in the database. Deterministic fingerprints will expose equality relationships and frequencies; shared keys will bring cross-database association risks, and being irreversible does not mean anonymity.
- `block` replaces each non-null result value of the hit column with a fixed `***`. It does not output the original value characters, length or equivalence relationship, but still retains the row and column shape, number of rows, column names, and whether there are results, and leaks the cell as empty/NULL because the null value is returned as it is. It is consistent with the result layer leakage level of `mask` / `hash` and is not anonymized; the output is an opaque string with no guarantee of numeric, date, or JSON schema compatibility.
- `range` generalizes values ​​into equal-width intervals and truncates dates to years/quarters/months to retain coarse-grained distribution; it is not anonymized, values ​​in the same bucket can still be associated, and it does not promise k-anonymity. Numeric ranges, date periods and null value status are still visible. Small buckets, fine granularity, small samples, multiple offsets or auxiliary queries may bring re-identification risks. Sensitive scenarios should cooperate with `block` or approval.
- The four types of masking are processed after the database is executed, and do not reduce database reads, nor prevent the database side from using the original value to execute WHERE/JOIN/GROUP BY; they only change the result unit returned to the caller.
- The audit is the **application layer append-only record**, which is not a regulatory-level WORM and does not prevent the DBA from directly changing the database; the v0.4 audit integrity chain's proof scope and trust boundary are shown in the next section. The signature/WORM retention lock is still part of the enterprise version roadmap.
- PostgreSQL's B2 column-level authorization, B5 cross-request logical sessions and planned transactions are all enabled by factory default; MySQL does not enter the B2 PostgreSQL path and does not support B5 cross-request transactions. Only one top-level SQL is still allowed per operation, subject to preflight plans and session security boundaries; the verified dual-gateway stateless read-only topology is shown in [Kubernetes reference ](../../deploy/kubernetes/README.en.md), which does not extend to the high-availability commitment of B5 stateful transactions.
- Security default **fail-closed**: When it cannot be parsed, cannot be determined, or audit/database and other dependencies are unavailable, **refuses to execute** instead of allowing it.

### 1.4 Audit integrity chain: trust boundary and non-detection scope

The audit integrity chain can only prove that: within a consistent snapshot, from**observed head**Going back to genesis,`chain_seq`Consecutive and without duplication; each row participates in the normalized audit bytes and`self_hash`consistent; consistent;`prev_hash`The previous row is correctly connected; the schema and inline key version in the database state are consistent with the schema and current key version declared in the trusted manifest outside the database. Verification result`VALID_AT_OBSERVED_HEAD`The meaning is strictly limited to this observation head, and it does not mean that this head is the latest head that the outside world has ever seen, nor does it mean that it is a regulatory-level, non-tamperable certificate.

The chain alone cannot detect the following situations: the attacker deletes the tail record and rolls back `chain_state` and `chain_verification` to a real and self-consistent old head; the entire `audit_logs`, chain state, and verification records are restored to an earlier consistent database snapshot; the event is missed before entering the audit write path; or the database control and the chain key are lost at the same time. Incomplete attacks such as only removing the tail without rolling back the head will still be detected as `head_mismatch`.

The `keyless` mode relies on no additional secrets outside the database and can detect accidental damage and unsynchronized tampering within the observation range, but an attacker with write permissions to the database can recalculate the chain. `hmac` mode uses the chain key as an additional root of trust outside the database: an attacker who only loses the database and does not obtain the chain key cannot generate a valid HMAC for the modified content; if the database and the chain key are lost at the same time, the above guarantee cannot be provided. Neither mode provides an external up-to-dateness anchor and therefore does not detect self-consistent tail truncation or full-base snapshot rollback.

When these out-of-boundary attacks need to be detected, database-independent trust prerequisites must be added, such as external WORM retention, SIEM continuous reception, or the latest head anchors saved and compared by an independent verifier. Enterprise version WORM/SIEM and long-term compliance archiving are the next direction; the current implementation is not exaggerated as an undeletable ledger.

---

## 2. Deployment and operation form

- **MCP dual-bears**: `stdio` (for local Agent to access as a child process) and **Streamable HTTP** (gateway multi-tenant entrance, path `/mcp`, follows MCP 2025-06-18 specification).
- **Streamable HTTP disconnection replay**: The stateful mode enables the relational library EventStore by default, and reuses the metadata database (SQLite or PostgreSQL) to persist old protocol SSE events. When the client reconnects with `GET /mcp`, `Accept: text/event-stream`, and `Last-Event-ID: <streamID>_<index>`, only the consecutive events after the index will be replayed; the events are subject to the 64 MiB total payload limit and the 30-minute TTL. When a recycling gap occurs, HTTP 400 `failed to replay events` will be returned, and partial suffixes will not be returned. The session table of go-sdk v1.7.0 is still in the process memory state: after the process is restarted, the old `Mcp-Session-Id` returns 404 with `Mcp-Session-Expired: 1`, and the client must re-`initialize`; this is not a seamless recovery of the old session across restarts.
- **Web console + management REST API** is provided by the same process and only listens to the loopback `127.0.0.1:7780` by default; the one-click self-hosted online demo suite defaults to `127.0.0.1:17880`.
- **control plane (metadata/audit storage)**: built-in **SQLite** (default, zero-configuration single file) or **PostgreSQL 15+** (benchmark and recommendation **PG18**); audit library available via standalone The DSN points to another set of PostgreSQL to isolate metadata and auditing.
- **deliverable**: single binary (the front end is embedded by `go:embed`), Docker / Docker Compose, Linux systemd unit; Prometheus indicators and Grafana panel are provided, as well as `/healthz` (process survival), `/readyz` (including storage readiness) probes.
- **build constraint**: PostgreSQL parsing relies on the real kernel `pg_query_go`, so the build requires **CGO and C compiler**, and the operation requires **glibc**; `CGO_ENABLED=0` and Alpine (musl) are not supported. Linux amd64 / arm64 native release binaries are generated using controlled build targets of the corresponding architectures; GHCR tag provides multi-arch manifests for both architectures.

---

## 3. Architecture and processing pipeline

```
AI Client ──MCP(stdio / Streamable HTTP)──┐
                                          ├─► mcpserver(tools) ─┐
Web 控制台 ──REST(/api/v1)──► adminapi ───┘                     │
                                          ┌─────────────────────▼────────────────────┐
                                          │ pipeline（auth → load → parse →          │
                                          │   guard_static → guard_dynamic →         │
                                          │   execute → redact → audit）             │
                                          │ policy / parser / rules / engine /       │
                                          │ executor / mask / audit / eventbus       │
                                          │ store（SQLite 或 PostgreSQL 元数据/审计） │
                                          └─────────────────────┬────────────────────┘
                                                pg/mysql wire 连接池（受控账号）
                                                     PostgreSQL / MySQL
```

**eight stages**: `auth` (Agent Key authentication) → `load` (load Agent/data source/policy) → `parse` (dialect AST) → `guard_static` (AST static rules) → `guard_dynamic` (obtain controlled connection, when necessary `EXPLAIN`) → `execute` (controlled execution) → `redact` (result masking) → `audit` (audit and drop-out).

**evaluation of two gates and "interception without touching the library"**: Either determination of static gate or dynamic gate `deny` will be returned before obtaining the real connection or executing, structurally ensuring that dangerous / unauthorized SQL **does not touch the business database**.

- **read-only `query`**: only semantic read-only `SELECT` is allowed; post-execution audit, no result will be returned if the audit is unsuccessful.
- **controlled write `execute_write`**: closed by default, requires `dml` and above level Agent and explicit policy authorization. The write path follows the **audit submission barrier**: Transactionable DML "writes the audit first and then submits" within the business transaction. If the audit fails, the business transaction will be rolled back (zero changes to the business database); implicit commit/non-rollback statements such as DDL must first write the "authorization intent" audit before execution. If the intent audit fails, it will not be executed.
- **Manual approval `approve`**: An approval form is generated when the policy ruling is "Approval required", **does not execute SQL**; the administrator makes the decision on the console. After approval, the original Agent can execute a single seal request based on the unexpired approval document number, and re-check the current permissions and rules during execution.
- **real-time event stream**: in-process SSE endpoint `/api/v1/stream` (management side Bearer authentication, only same origin, event body is security field allow-list projection), audit is pushed after successful fallback; browser disconnection automatically reconnects, continuous failure is downgraded to polling; **audit table and audit API It is always the authoritative data source**, and the front end deduplicates according to the audit ID. SSE is single-instance best-effort, and cross-instance broadcast is in the roadmap.

---

## 4. Technology stack

**Backend (`go.mod` declares Go 1.26.0; official release scripts pin Go 1.26.10)**

| Purpose | Depend on |
|---|---|
| MCP protocol | `modelcontextprotocol/go-sdk` v1.7（stdio + Streamable HTTP） |
| PostgreSQL parsing | `pganalyze/pg_query_go/v5` (real PG kernel parser, zero bypass) |
| MySQL parsing | `vitess.io/vitess` sqlparser (`ParseStrictDDL` strict mode, multi-statement splitting, compatible with fail-closed) |
| PostgreSQL driver | `jackc/pgx/v5` (pgxpool is used for the business database; `database/sql` + pgx stdlib is used for the control plane) |
| MySQL driver | `go-sql-driver/mysql` |
| Control plane storage | `modernc.org/sqlite`（SQLite）+ pgx（PostgreSQL） |
| HTTP | Go standard library `net/http` (management API and MCP HTTP, does not introduce web framework) |
| config/logs/cli | `gopkg.in/yaml.v3`、`rs/zerolog`、`spf13/cobra` |
| Current Limit/Indicator | `golang.org/x/time`、`prometheus/client_golang` |
| test | `stretchr/testify` + `testcontainers-go` (real PostgreSQL 18/MySQL 8) |

**Frontend (`web/`)**: React 18 + TypeScript 5 + Vite 8 + Ant Design 5 + ECharts 6 + framer-motion + zustand + react-router-dom + axios. Quality gates include `tsc --noEmit`, Vitest, English catalog checks and a Vite production build. Output goes to `internal/webui/dist` and is embedded in the single binary with `go:embed`.

---

## 5. Code structure

```text
cmd/
  agentsql/        # Gateway / MCP / console process
  agentsqlctl/     # Operations CLI (init-config / check-config / migrate / health / demo-seed)
internal/
  config/ model/ store/ auth/ policy/ parser/ engine/ rules/
  executor/ mask/ audit/ pipeline/ mcpserver/ adminapi/
  eventbus/ metrics/ demoseed/ bootstrap/ server/ webui/ version/
web/               # React console source (built to internal/webui/dist and embedded)
deploy/systemd/    # systemd units and hardening examples
examples/          # Docker Compose, configuration and environment examples
demo/              # Self-hosted demo and reset scripts
tests/corpus/      # SQL parser and decision regression corpus
website/           # Product website (static, not embedded in the binary)
docs/              # Documentation
```

`internal/store/migrations/` organizes migration scripts according to the "metadata/audit" layout and the "sqlite/postgres" dialect. The two dialect schema semantics, constraints, and indexes are equivalent.

---

## 6. External contracts

### 6.1 Configuration (config.yaml)

Main configuration sections (complete annotated examples are subject to `examples/config.example.yaml`, `examples/docker/.env.example`):

```yaml
server:
  http_listen: "127.0.0.1:7780"
  console_enabled: true
  event_stream: true                 # SSE real-time event streaming
  event_stream_max_connections: 100
store:
  # Abbreviation (v0.1 compatible, default zero configuration):
  sqlite_path: "./data/agentsql.db"
  # Or explicit control plane (v0.2):
  # metadata: { driver: postgres, dsn: "...", max_open_conns: 10, max_idle_conns: 5 }
  # audit: { separate: true, driver: postgres, dsn: "..." } # Audit independent PG
  # auto_migrate: false # Production suggestion: close startup DDL after one-time migration
defaults:
  statement_timeout_ms: 5000
  row_limit: 1000
  max_conns_per_datasource: 5
  qps_per_agent: 20
theme: { default: "dark" }           # dark / light
demo: { enabled: false }             # Online demonstration mode, closed by default
redaction:
  hash_key: ""                       # Optional; at least 32 bytes if not empty
```

- `AGENTSQL_SECRET` is the data source password AES-GCM master key for use with console/JWT etc., only injected from environment variables. The masked hash fingerprint uses an independent optional key, which can be from YAML `redaction.hash_key` or `AGENTSQL_REDACTION_HASH_KEY`; as long as the environment variable exists (including the empty string), it overrides YAML and cannot be reused or derived from `AGENTSQL_SECRET`. Non-empty hash key must be at least 32 bytes, recommended to be injected by secret manager/restricted environment; configuration, logs, errors, probes, responses, audits, metrics and panic **must not echo the key, DSN or password**.
- The hashing capacity fail-fast contract only constrains `hash`: when there is no enabled `hash`, it can start normally without a key (including only `mask` / `block` / `range` or disabled hash draft); when there is enabled `hash` but no valid key, the startup fails before external services; it is explicitly non-empty but less than 32 Bytes, assembly fails regardless of rule status. No key is created or enabled during runtime. `hash` returns HTTP `503` + `HASH_REDACTION_UNAVAILABLE` and zero writes; `block` has no key and no parameters, and `range` has no key but type-specific parameters. Neither of them participates in the access control, and this 503 will not be returned when enabled. Illegal type/algorithm combination returns HTTP `422` + `INVALID_MASK_RULE`.
- The first version process only has a single hash key, and there is no key version, double writing, multi-version verification or online rotation/recalculation. After changing the key, historical fingerprints will not be automatically recalculated, and the old and new fingerprints are no longer equal; rotation must complete inventory counting, downstream backfilling, and rollback preparations during the maintenance window.
- The configuration is parsed in strict mode (unknown fields are rejected), and the startup with illegal configuration fails (fail-closed).

### 6.2 Core enumerations and models

```go
type DBDialect string // 业务库类型标识；注册表共 30 款，能进入 NewParser 的范围另受方言入口限制
type StmtType  string // SELECT / INSERT / UPDATE / DELETE / DDL / ADMIN / UNKNOWN
type Decision  string // allow / warn / approve / deny
type RiskLevel int    // 1 拒绝 / 2 审批 / 3 告警 / 4 提示
type SensitiveType string // phone | email | idcard | bankcard | ip | birthdate | generic | number | date
type Algorithm string // mask | hash | block | range
```

`AST` carries dialect, original/normalized SQL, statement type, multi-statement tag, table object, column, whether it contains WHERE/WHERE Yongzhen/LIMIT, functions and operations, EXPLAIN information; `Assessment` carries decision-making, risk level, hit rules, estimated number of scanned rows, Chinese reasons and rewriting suggestions, and time consumption in each stage; `QueryResult` Renders columns, stringified rows, row numbers, and truncation markers (fixed-point decimals such as amounts are rendered in fixed-point, no floating point is used).

Dimensional differences of masking algorithms:

| algorithm | Handling Dimensions | export contract | Equivalent association | key/parameter |
|---|---|---|---|---|
| `mask` | Preserve partial masking of readable prefixes or formats | Processed according to six categories of format; unrecognized non-null value fail-closed is `[REDACTED]` | Not supported | None |
| `hash` | Irreversible HMAC fingerprint | The same string bytes under the same key generate the same fixed-length fingerprint | Supports equal value association, deduplication, and grouping | Private key at least 32 bytes; no other rule parameters |
| `block` | Integer blocking | The format is not recognized; each non-null value is fixed to `***`, and the original text fragment or length is not retained | Not supported | No keys, no parameters |
| `range` | Coarse-grained generalization | Value bucketing is `[lower,upper)`; dates are truncated to year/quarter/month; coarse-grained distribution is retained but not anonymized | Values in the same bucket can be associated | No key; uses type-specific parameters |

Algorithmic capability matrix for nine sensitivity types:

| Sensitive type | `mask` | `hash` | `block` | `range` |
|---|---:|---:|---:|---:|
| `phone` | ✅ | ✅ | ✅ | ❌ |
| `email` | ✅ | ✅ | ✅ | ❌ |
| `idcard` | ✅ | ✅ | ✅ | ❌ |
| `bankcard` | ✅ | ✅ | ✅ | ❌ |
| `ip` | ✅ | ✅ | ✅ | ❌ |
| `birthdate` | ✅ | ✅ | ✅ | ❌ |
| `generic` (universal sensitive value) | ❌ | ✅ | ✅ | ❌ |
| `number` (numeric value) | ❌ | ✅ | ✅ | ✅ |
| `date` (date) | ❌ | ✅ | ✅ | ✅ |

`number` / `date`Represents column semantics, orthogonal to the first six types of format semantics;`range`Open to these two categories only.`birthdate`Still using birthday only`mask`, not open because it can be parsed as a date`range`。`hash` / `block`Can handle any non-empty byte string of nine types; the administrator can`range`、`hash`、`block`No need to switch between`number` / `date`Change the label to`generic`。

#### 6.2.1 `range` numerical bucket contract

`number` uses the following parameters and cannot carry `granularity`:

- `bucket_width` is required and must be a positive integer of `1..1_000_000_000`.
- `bucket_offset` can be omitted; when omitted, it is normalized to `0`, which must be an integer of `-1_000_000_000..1_000_000_000`.

The bucketing formulas are `lower = floor((v - offset)/width)*width + offset` and `upper = lower + width`. Among them, `floor` is rounded towards negative infinity in the mathematical sense and cannot be truncated towards zero with integers; for example, when `width=10, offset=0` is used, `42` outputs `[40,50)`, `10` outputs `[10,20)`, and `-3` outputs `[-10,0)`.

Numerical values must be analyzed and calculated using `math/big.Rat` for precise decimal rational numbers. It is prohibited to use `float64`, `strconv.ParseFloat` or `big.Float` to convert back to integers. Non-empty input only accepts ASCII syntax `[+-]?(digits(.digits?)?|.digits)([eE][+-]?digits)?` after `strings.TrimSpace`, such as `42`, `-3.5`, `.5`, `1.`, `1e3`; rejects `NaN` / `Inf` / `Infinity`, `0x1p2`, etc. hexadecimal, underscore, fraction, thousandths, currency symbol and any trailing characters. If the length after trimming exceeds 128 bytes, or the absolute value of the scientific notation index exceeds 1000, it will always fail-closed to `[REDACTED]`.

After the calculation is completed, use `big.Int` to compare `lower`, `upper` with the upper and lower limits of int64. It is forbidden to force the conversion first and then judge; when any intermediate value or result boundary overflows int64, `[REDACTED]` is output. The successful result is fixed to the ASCII left-closed and right-open string `[lower,upper)` with no spaces, no `+`, no thousandths, and no exponent.

#### 6.2.2 `range` date truncation contract

`date` is prohibited from carrying `bucket_width` or `bucket_offset`. `granularity` can be omitted, and when omitted, it is normalized to `year`; the explicit value can only be `year`, `quarter`, `month`, and the explicit empty string is illegal.

Parsable input is as follows:

- Complete dates: `2006-01-02`, `2006/01/02`, `2006.01.02`, `20060102`.
- Local timestamp: `2006-01-02 15:04:05`, and the same format with 1–9 fractional seconds.
- RFC3339/RFC3339Nano timestamp.
- Partial date: `YYYY-MM` can be truncated to year, quarter or month; `YYYY` can only be truncated to year. Fail-closed when the accuracy is not sufficient to generate the selected granularity, it is forbidden to add 1 day or 1 month out of thin air.

The month and day must be real and valid (including leap day verification), and the year range is`0001..9999`. RFC3339 timestamp by input**Calendar day at original offset**Truncate, prohibit calling`.UTC()`Or convert to server time zone and then change day; hours, minutes and seconds are ignored. The output is fixed to:`year` → `2006`，`quarter` → `2006Qn`(capital`Q`, no hyphen,`n=(month-1)/3+1`），`month` → `2006-01`. Output when it is not empty but the layout does not support it, the date is illegal or the precision is insufficient.`[REDACTED]`。

#### 6.2.3 Universal execution, counting and privacy contracts

- Null value sentinels include empty strings, `NULL` / `<nil>` that are empty after trim and case-ignored; they retain the original strings and return them as they are, and are not included in `MaskedCells`. Non-null values ​​whether successfully generalized or fail-closed to `[REDACTED]` are counted as processed (i.e. `MaskedCells`), even if the output literal is the same as the input.
- `range` has no key, no salt, no independent configuration or environment variables, does not enter the startup access control of the hash key, and will not return `503 HASH_REDACTION_UNAVAILABLE` during runtime; the mixed rule set is only unavailable when it contains `hash` and has no valid key.
- The generalization result is a string, and the output column is no longer guaranteed to be compatible with the original value or date schema. `range` is not pushed down to database SQL; the gateway generalizes the results in memory after retrieving the database results, so the database still uses the original values ​​to perform WHERE/JOIN/GROUP BY, and database reads will not be reduced.
- `RedactReport`, `AuditLog`, SSE and notification payload do not add algorithm-specific fields; `TouchedColumns` can appear `number` / `date`.
- Generalization is not anonymization: values in the same bucket are deterministic and can still be related, and k-anonymity is not promised; numerical ranges, date periods, and null status are still visible. Small buckets, `month` granularity, small samples, multiple offsets or auxiliary queries may lead to re-identification. Sensitive scenarios should be coordinated with `block` or approval.

`mask+generic` is illegal. The null value judgment of `block` follows the unified sentinel: empty string, empty after trim, and `NULL` / `<nil>` that ignore case are returned as they are and are not included in `MaskedCells`; other values, even if the original text is exactly `***` or has leading and trailing spaces, `***` will be output and counted. Fixed `***` which may collide with the real original value and is not self-describing, unable to determine whether blocking is performed based on whether the output changes.

The rule management API/UI displays `algo=block` as it is; the result projection of MCP/Playground and persistent auditing, SSE, and notification do not add algorithm fields. `***` and mask failure `[REDACTED]` have different literals and different algorithm semantics, but the result projection itself cannot reliably reverse the algorithm. This version does not extend the results or audit protocol.

#### 6.2.4 Table. Column perception masking contract

masking rules have three scopes, and the stored values and semantics are as follows:

| Scope | `schema_name` | `table_name` | Semantics |
|---|---|---|---|
| Global column rules | `""` | `""` | Maintain historical behavior: unified protection by column name within the corresponding data source range |
| Table.Column Rules | `""` | Not empty | Protect columns of this table; schema wildcard |
| Schema.Table.Column Rules | Not empty | Not empty | Protect only the columns of this `schema.table` |

`schema_name`not empty`table_name`It is illegal to be empty, and the management API returns HTTP.`422`. Application paths save scope null values ​​as empty strings, without using SQL`NULL`。v0.3of`0005_mask_rule_scope.sql`It is a one-time semantic migration: the existing rules before the upgrade will be unified`schema_name`and`table_name`Solidify to an empty string to explicitly retain the actual behavior of the "global column rules" before the upgrade.

Provenance analysis uses two non-mixable table collections:

- **top-level SELECT relation binding**: only physical relations and non-physical sources visible in the `FROM` / `JOIN` scope of the root SELECT are used only for precise attribution of top-level directly projected columns.
- **`AST.Tables` Full statement physical table union**: Recursively includes CTE and the physical table inside the subquery. It is only used for the safety of "possible relationships" of unresolved columns and must not be used to guess the precise attribution.

The source status of the top-level projection is Resolved or unresolved. Explicitly qualified columns are only resolved if the qualifier uniquely hits the top-level physical binding; naked columns are only resolved if the total number of top-level `FROM` visible sources is exactly 1, and that source is a physical relationship. JOIN naked columns, `SELECT *`, CTE outer columns, columns of derived tables or function tables are treated as unresolved. After the relationship is aliased, the original table name is obscured; `schema.table` only recognizes relationships that have not been aliased. Views are processed by relational reference names in SQL and do not expand column lineage internally to view definitions, derived tables, or function tables.

For each result column, match strictly according to the following six-level order, take the first hit and process each column only once:

1. If the user explicitly changes the name, and the resulting column name after the change matches the global column rule, this rule will be applied.
2. If the source is Resolved, check the table-level rules according to the source column name: first check the schema precise rules, and then check the schema wildcard rules of `schema_name=""`.
3. Check the global column rules by the result column name.
4. Check global column rules by source column name.
5. If the source is not resolved, and the result column name or non-empty source column name hits a table-level rule, and the table of this rule appears in the `AST.Tables` possible relationship set, perform a fail-closed safety net: all non-empty cells in this column are fixedly blocked as `***`, and `RedactReport.UnresolvedScopedColumns` is recorded.
6. Return to the original text if neither hits.

Tier 5 does not use the business algorithm of either table because the source has not yet been determined. It also doesn't "fall back global and then pass": that would allow JOIN naked columns, `SELECT *` and CTE outer layers to bypass rules configured only for a certain table. The system also does not directly reject the entire query to maintain the availability of the transparent gateway to ORM and tool-generated SQL. This trap cannot be closed; accidental damage can be eliminated by adding a table qualifier to the projection column or changing the column to a global column rule.

Identifier semantics are differentiated by dialect:

- Column names follow `NormalizeColumnName`: remove leading and trailing blanks and single-layer quotation marks, backtick marks or square brackets, and unify lowercase letters. This is a loose normalization; `"Phone"` and `phone` in the same table are not separately distinguished.
- Exact comparison between schema and table name does not make `ToLower` / `EqualFold`; therefore `customers` and `"Customers"` are different table keys.
- PostgreSQL only retains the schema explicitly written in SQL, does not hard-fill `public` for unqualified relationships, and does not uniformly lowercase the rule keys twice. Found that the generated PostgreSQL draft holds `schema_name=""`.
- The first version of MySQL accurately compares table name strings, and column names are still normalized according to the above-mentioned looseness; `schema_name=""` means that the library name is not compared, which is the table-only semantics of the current library.

`RedactReport.UnresolvedScopedColumns`It is an addition field of sensitive type that is recorded according to the result column index, and is only recorded when at least one non-empty cell is actually replaced. MCP`query`of`redact.unresolved_scoped_columns`will reveal the report; currently the Playground REST projection only returns`touched_columns`and`masked_cells`, neither the Playground nor the audit console displays this field, and the first version does not add an independent indicator pipeline or large-screen panel.

### 6.3 Control plane data model

Core data tables are listed below; the migration directories contain the complete inventory. v3.1.0 uses combined schema 24, separate metadata schema 23 and audit schema 6. The v3.0.2 baseline is 23, 22 and 6 respectively.

| table | Purpose / Key Invariants |
|---|---|
| `agents` | Agent registration and level (readonly/dml/ddl); `asql_` key only exists SHA-256 |
| `datasources` | business database connection; password is stored in AES-GCM ciphertext (master key `AGENTSQL_SECRET`); including connection number/timeout/row limit |
| `policies` | Library/table/column three-level authorization policy (allow/deny), associate Agent and data source |
| `rules` | Built-in rules directory and runtime enablement overrides (do not write any custom rule semantics here) |
| `mask_rules` | Three-level scope masking rules and algorithms by data source/schema/table/column |
| `audit_logs` | **audit core, only append**; the warehousing layer does not provide UPDATE / DELETE interface; only this table and index in the independent audit library |
| `approvals` | Approval form (pending/approved/rejected/expired), associated audit ID |
| `sqlbot_bindings` | Tenant-scoped workspace, datasource and SQLBot user mapping to an existing human and readonly Agent; connector credentials use SHA-256 storage, and revisions revoke old tokens |

The two sets of DDL of SQLite and PostgreSQL are semantically equivalent (auto-increment keys, Boolean, time types, and placeholders are translated according to dialects); the audit ID is `BIGINT GENERATED ALWAYS AS IDENTITY` on the PG, and the original audit ID is retained during data migration.

`mask_rules`ofv0.3The physical unique key is`(datasource_scope, schema_name, table_name, LOWER(TRIM(column_name)))`; Among them, schema / table retains the original value for accurate distinction, and column is judged according to the normalized value. This constraint includes disabled rows, duplicated with physical keys returned by the management API over HTTP`409` + `MASK_RULE_CONFLICT`. Four sets of metadata migration directories (SQLite / PostgreSQL and their metadata layout) are used`0005_mask_rule_scope.sql`: New`schema_name`, clear the existing schema/table to an empty string, and rebuild the old unique index based on data source + column name into the above new key. The audit library does not perform this migration.

Within the valid scope of the same datasource, if the enabled global column rules and table-level rules have the same column names but inconsistent algorithms, creation or update will return HTTP `409` + `MASK_RULE_SCOPE_CONFLICT`. `range` In addition to the algorithm name, it is also sensitive to the type, as well as the bucket width/normalization offset for numeric rules or the normalization truncation granularity for date rules. disabled draft exempts scope algorithm conflict checking, but does not exempt physical key uniqueness constraints.

`mask_rules` adds three independent nullable columns to `range` without using JSON: `range_bucket_width INTEGER`, `range_bucket_offset INTEGER`, `range_granularity TEXT`; none of the three columns have `NOT NULL` and database default values, so the old rules are naturally NULL. `algo=range` or any parameter column is non-NULL, it will be regarded as carrying the range parameter and complete verification will be entered. Creation and update adopt complete PUT semantics; the range parameter missing in the request is written back to NULL during update. No irrelevant parameters should be left when switching from `range` to other algorithms or switching between `number` / `date`. When the numeric rule omits `bucket_offset`, it is normalized by the API to non-NULL `0` and then persisted; the storage layer must retain the difference between NULL and 0.

Sensitive discovery identifies candidate columns with the physical origin key `(datasource, schema, table, column)`. When applying candidates, both PostgreSQL and MySQL generate table-only drafts of `schema_name=""`, `table_name=<真实表名>`, `algo=mask`, and `enabled=false`; columns with the same name in different tables are multiple independent drafts. If the candidate column has been covered by an enabled global column rule, the result is marked as `CoveredByGlobal` and creation is skipped; disabled global drafts do not block new table-only drafts.

### 6.4 MCP tools (eight basic tools + PostgreSQL B5 eight session/planned transaction tools)

| Tools | Add ginseng | behavior |
|---|---|---|
| `list_datasources` | `{}` | Only return data sources authorized by the current Agent |
| `list_schema` | `{datasource_id, table?}` | Return schema, filter by column permissions |
| `explain_query` | `{datasource_id, sql}` | Just do EXPLAIN, don't execute |
| `query` | `{datasource_id, sql}` | Only semantic read-only SELECT, complete pipeline, result masking |
| `execute_write` | `{datasource_id, sql, reason}` | Controlled write, closed by default, requires dml and above + authorization + audit barrier |
| `request_approval` | `{datasource_id, sql, reason}` | Generate manual approval form and do not execute it |
| `get_approval_result` | `{approval_id}` | Check approval status |
| `execute_approved` | `{approval_id}` | The original Agent once claims the approved and unexpired seal request, and executes it after re-authorization and audit. |

When PostgreSQL B5 is enabled by default, `open_session`, `close_session`, `get_session_status`, `begin_transaction`, `execute_transaction_statement`, `commit_transaction`, `rollback_transaction`, and `get_transaction_status` are additionally registered; after explicitly closing `mcp.sessions.enabled`, only the above eight basic tools remain. Errors are uniformly returned to `{decision, reason, suggestion}`, and `suggestion` guides the AI ​​to rewrite itself in Chinese. **does not provide universal execution tools like `execute_raw_sql`.**

### 6.5 Management REST API

- The prefix is `/api/v1`. Management resources require an admin Bearer token and current permissions; login, refresh and external identity flows use their own authentication contracts. `POST /api/v1/integrations/sqlbot/resolve` accepts only a dedicated connector credential, not an admin token in its place. Responses use `{code,msg,data}`, with pagination `{total,page,page_size,list}`.
- Endpoint family: authentication (login, refresh, me, logout; server-side persistent revocation of access jti and refresh family), agents (including rotate-key), datasources (including connectivity ping, sensitive discovery and NoSQL `native-ping` / `native-schema` / `native-query`), `datasource-types`, policies, rules, mask_rules (three-level scope) CRUD), audit (paging/filtering/CSV/JSONL/PDF/ZIP export), approvals (list/ruling), dashboard/summary (KPI/trend/distribution/ranking), playground (static evaluation, demo mode also has controlled trial run), stream (SSE). The NoSQL native command only opens the controlled subset in the adapter whitelist. For details, see [NoSQL support scope ](nosql-support.md).
- masking rules `POST /api/v1/mask_rules` and `PUT /api/v1/mask_rules/{id}` accept `schema_name`, `table_name`, `column_name`, `sensitive_type`, `algo`, `enabled` and optional `datasource_id` / `range_*` fields. Scope fields prohibit leading and trailing blanks, NUL/control characters, `*`, `%` and `.` within a single field, up to 128 Unicode characters; retain the original case and allow legal quotation mark identifiers such as Chinese.
- Probes: `/healthz` (survival), `/readyz` (storage ready, double pinging of control plane and audit library), `/metrics` (Prometheus).

---

### 6.6 SQLBot v2 human bindings (v3.1.0 under development and validation)

The target is fixed to ordinary logged-in users of official SQLBot v1.10.2 and PostgreSQL. The installer verifies version and source hashes. Embedded assistant / ask-token flows, other SQLBot versions, other databases and unvalidated charts are outside this release's support claim.

- Each mapping precisely links a workspace ID, SQLBot datasource name, SQLBot user ID, existing AgentSQL human, dedicated readonly Agent and PostgreSQL datasource within one tenant. External IDs are stored as decimal strings so browsers preserve Snowflake integer precision. `schema_name` restricts schema discovery; existing policies still authorize SQL objects.
- The SQLBot server obtains identity from authenticated `current_user` and explicitly passes it to model worker threads. The connector credential represents trust in that server's identity assertions. Browser parameters, custom HTTP user headers and model output are not identity sources.
- Creating, editing and rotating mappings require `strategy.manage`, `user.manage` and `datasource.manage`. `/api/v1/integrations/sqlbot/bindings` provides tenant-scoped CRUD, enable/disable, diagnostics and credential rotation. `/api/v1/integrations/sqlbot/export` exports v2 configuration containing only the endpoint, bindings and environment variable names.
- The independent connector credential is shown only when created or rotated; the database stores SHA-256 only. After checking the live binding, human RBAC and Agent, `resolve` issues a two-minute SQLBot-specific human token that cannot log in to management APIs. Every MCP request checks the Agent Key, human token and binding revision, and restricts calls to read, discovery and readonly approval tools for the designated datasource.
- Disabling, deleting, editing or rotating a binding invalidates its old tokens on subsequent requests. Agent Keys retain their independent lifecycle; revoke the Key to stop all access by that Agent. Schema and fields are permission-filtered before reaching the model. Denials do not trigger automatic model SQL rewrites; approvals require an explicit user request and resume the sealed query once.
- Configuration diagnostics execute no query and do not prove network, model or business success. Release acceptance requires real SQLBot, a model and PostgreSQL, covering allow, deny, masking, audit, revocation, fail-closed behavior and approval replay. Failed management audits roll back the mutation transaction; runtime failures never fall back to a direct database connection. See the [v3.1.0 upgrade guide](v3.1.0-upgrade.md) for migration and full backup restoration.

---

## 7. Rules and Decisions

- **Four kinds of decisions**: `allow` (release), `warn` (release with alarm), `approve` (transfer to manual approval), `deny` (reject, do not touch the library); the risk is divided into four levels (rejection/approval/alarm/prompt).
- **rule layering**: general rules (R001–R010, such as update/deletion without WHERE, WHERE permanent, read-only Agent writing library, library table blacklist, QPS current limit, etc.), PostgreSQL specialization (R1xx), MySQL specialization (R2xx); static rules are based on AST characteristics, and dynamic rules are combined with controlled `EXPLAIN` (such as full table scan R005) and runtime current limiting.
- **policy engine**: three-level authorization based on Agent × data source parsing library/table/column, with superimposed Agent level (readonly/dml/ddl); without explicit authorization, it is rejected by default.
- Built-in rules can be enabled/disabled in the console (overridden at runtime, effective immediately); rule hits, risk levels, estimated number of rows, normalized SQL, and delays at each stage are all written into the audit and can be traced in the console and audit export.
- The authoritative list of rules and positive and negative examples are based on the `internal/rules/` source code and `tests/corpus/` corpus.

---

## 8. Current Compatibility and Capability Statement

The capability directory uses `internal/model/datasource_capabilities.go` as the executable definition. This directory is shared by the Management API, Web, and authorized MCP `list_datasources`; the declaration itself does not grant permissions.

| Ability level | current boundary |
| --- | --- |
| `controlled_sql` | Validated subset of controlled SQL for PostgreSQL 14–18, MySQL 8.0/8.4; column policies, approvals, and transactions are still subject to specific configuration, table structure, and authorization. |
| `bounded_sql` | Oracle, DM, YashanDB, SQL Server, Vastbase, HighGo, openGauss, etc. have their own specified versions and narrow SELECT; they do not inherit PostgreSQL B2/B5. |
| `controlled_read_tool` | MongoDB `find_documents`, Redis/Valkey `read_key`, ClickHouse `query_analytics`, Milvus `search_vectors`; each requires precise authorization, source masking and persistent auditing. |
| `connection_preview` | Other registration items only provide respective connections and metadata previews; there is no corresponding Agent business query tool and does not mean complete production support. |
| Control plane | v3.1.0 combined schema 24; separate metadata schema 23 and audit schema 6. Upgrade and recovery require acceptance for the target topology. |
| MCP and client | stdio, `/mcp` with handshake and independent stateless `/mcp/2026-07-28`; see [ access guide ](INTEGRATIONS.md) for specific tools and identity requirements. |
| console | Chinese and English, light/dark theme, independent scroll navigation, single binary embed. |
| SQLBot | v2 integration under development and validation, fixed to SQLBot v1.10.2, ordinary logged-in users and PostgreSQL; workspace/datasource/human mappings, separate connector credentials, short-lived human tokens, readonly approval, masking and persistent audit, with no direct database fallback. |

See `internal/model/datasource_types.go` for 30 registration types, ports and categories. For the complete itemized matrix and current evidence entry, see [README](../../README.en.md), [ current operation and maintenance manual ](OPERATIONS.md), and [ database support instructions ](nosql-support.md). KingbaseES V9 PG mode lists designated instances separately and is not calculated as a new registration type. Vendor versions, modes, clusters, active and standby, licenses and production loads must be verified one by one; document updates do not expand the scope of support.

---

## 9. Quality and Testing Gate

- Format/static check target: `gofmt`, `go vet`, full `go test -race`; core security package (parser/engine/rules/policy/pipeline) coverage target ≥ 80%, each rule has both positive examples (should be intercepted) and counterexamples (should not be intercepted). These are quality gate requirements and do not mean that they are all currently complete.
- **Decision regression corpus** (`tests/corpus/`): parsed corpus + 252 decision-making use cases, 353 judgments based on PostgreSQL/MySQL dialect; required dangerous omissions to be 0 and false blocking rate to be less than 2%.
- **fuzz fail-closed Target**: The pipeline will not panic for mutated SQL, and unexpected input will be rejected; the specific execution scale must be subject to the test report.
- **real database E2E**: Use testcontainers to pull up real PostgreSQL 18 and MySQL 8, covering read-only release, unauthorized rejection, no WHERE write interception, masking, approval, and auditability; the control surface runs dually in both SQLite and PostgreSQL layouts.
- **"Writing attacks zero-touch the library" dual evidence**: zero calls to methods related to the fake executor, plus the same number of lines/checksums before and after execution of the real business database, jointly proving that dangerous writes and DDL do not touch the library.
- **performance caliber (to avoid misleading)**: The read-only P99 latency in the document is **, which strips away the real database network and I/O. The gateway CPU path micro-benchmark** (fake executor, fixed concurrency, memory state) is used to guard the gateway's own CPU Budget, **does not represent the end-to-end delay**; due to the new transaction and audit round trip of controlled writing, the real delay must be higher, and this threshold is only recorded without applying it.
- **Current evidence and reserved items**: Batch 61 PostgreSQL parser's 5 ms P99 gate passed 5/5 in the two groups of designated Linux environments, but the release date must still be exclusively retested according to the same threshold; the historical batch did not form a full warehouse short tests PASS at that time; the current official version of Go/Web/vulnerability check status must check the submission and CI of the corresponding Release, and the historical to-do cannot be used as the current conclusion. Batch 67 added parser malformed/concurrency error path and pipeline rejection test before execution. For coverage and offline restrictions, see [ test coverage description ](test-coverage-notes.md).

---

## 10. Authorization, Versions and Roadmap

- The source code uses **Apache License 2.0**; see [LICENSE](../../LICENSE) and [NOTICE](../../NOTICE). Apache-2.0 does not grant trademark rights. The **AgentSQL** and **Zhidun** names and logos remain the property of their rights holder.
- **Version status**: v3.0.2 is released; see its [release evidence](v3.0.2-release-evidence.md). v3.1.0 is under development and validation; its new SQLBot v2 contract is described in section 6.6 and [version acceptance](v3.1.0-acceptance.md). Existing RBAC / multi-tenant MVP, TOTP MFA, OIDC, LDAP/AD, PDF/ZIP audit export and audit chain retain their established boundaries. Enablement requirements are in [identity guidance](AUTHENTICATION.md) and [MCP integration](INTEGRATIONS.md).
- **Current scope**: implement v3.1.0 only. Later versions await user instructions; real customer pilots await identified customers and testing authorization.
- **Follow-up direction**: The complete domestic/commercial database dialect and final verification of the manufacturer's target environment, regulatory-level WORM/external SIEM, multi-copy HA, K8s Operator, cross-instance centralized management and control, etc. still need to be subsequently verified or implemented; this specification does not count them as delivered capabilities.
- Business cooperation: **87326549@qq.com** | **https://agentsql.cn**

---

## 11. How to contribute

- Please read [CONTRIBUTING.md](../../CONTRIBUTING.en.md) first: Contributions must comply with the "narrow and deep" scope agreement, Conventional Commits submission specifications and DCO; changes to the security engine must simultaneously supplement the decision corpus and pass the quality gate; use `demo/reset` to rebuild cleanly after changing the online demonstration.
- Please use [SECURITY.md](../../SECURITY.en.md) to disclose security vulnerabilities privately. Do not make the issue public.
- Documentation and promotion should be restrained and truthful: no exaggeration of protection boundaries, no leakage of credentials/DSNs/internal information.

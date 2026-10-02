# YashanDB dialect boundary

This document records the verified v0.5 batch-eleven boundary for native
YashanDB. It is intentionally a separate `yashan` dialect: the tested server
uses YashanDB's native protocol and `COMPAT_VECTOR=yashan`, not PostgreSQL or
MySQL wire compatibility.

## Verified environment

- Image: `yashandb:yashandb-image-23.4.1.109-linux-x86_64`, loaded from the
  separately supplied archive.
- Container: `yashan-v06`, host port `1688`, remained running after deployment.
- Deployment log: `DeployYasdbCluster` progressed from `RUNNING` to `SUCCESS`,
  `return_code=0`, progress `100`.
- `yasql`: `/data/yashan/yasdb_home/23.4.1.109/bin/yasql`.
- Server: `Enterprise Edition Release 23.4.1.109 x86_64`; current user `SYS`;
  `COMPAT_VECTOR=yashan`.
- C headers: `include/yac_include.h` and `include/yacli.h`.
- Server-image runtime library: `lib/libyascli.so -> libyascli.so.0 ->
  libyascli.so.1.1.109`, with `libyas_infra.so.1.1.109` and the other 23.4.1.109
  server libraries in the same directory. The image did not provide a usable
  `LD_LIBRARY_PATH` to non-login `docker exec`; `yasql` requires that directory
  to be set explicitly in that situation.

Passwords are never interpolated unescaped. Both the official Go driver and
its DSN documentation require `\\`, `/`, and `@` in user/password components
to be escaped with `\\`. A real password containing `@` failed when naively
concatenated and succeeded when supplied separately to `yasql`; the Go E2E test
exercises the implemented driver escaping.

## Driver and reproducible build

The supported module is
`github.com/yashan-technologies/yashandb-go@v1.4.4`, registered as `yasdb`.
The task's v1.4.2 starting point was tested, but it returned `YAS-02143` for
valid credentials even with the official standalone client. v1.4.4 is the
first published version tested here that completed the connection and metadata
path. The old `git.yasdb.com/go/yasdb-go` endpoint did not resolve and is not
used. The driver requires Go 1.18+, CGO and the YashanDB C client. It compiles
its C shim from module sources and loads `libyascli.so` dynamically on Linux.

The client bundled in the 23.4.1.109 server image is not a stable Go-driver
runtime: `yasql` can use it, but Go driver v1.4.2 and v1.4.4 both returned
`YAS-02143` for two credentials that were independently verified with `yasql`.
The successful E2E path used the official standalone x86_64 client
23.4.7.100 (`libyascli.so.1.7.100` and `libyas_infra.so.1.7.100`) from the
official `yashandb-client` repository. That client connected to the
23.4.1.109 server with v1.4.4.

The native driver import is isolated behind `yashan && cgo` in
`yashan_driver.go`. Consequently the ordinary build remains independent of
the proprietary runtime:

```text
go build ./...
go test -short ./internal/authorizedexecute/internal/businessdb
```

The verified Linux native path uses `golang:1.25-bookworm` with GCC, mounts the
source and an ephemeral read-only extraction of the official standalone
client, and runs:

```text
export CGO_ENABLED=1
export LD_LIBRARY_PATH=/opt/yashan-client/lib
go build -tags yashan ./...
AGENTSQL_YASHAN_E2E=1 YASHAN_HOST=127.0.0.1 YASHAN_USER=SYS \
  YASHAN_SCHEMA=SYS YASHAN_TABLE=ALL_TAB_COLUMNS \
  YASHAN_PASSWORD='<from secret injection>' \
  go test -tags yashan -short ./internal/authorizedexecute/internal/businessdb
```

The final E2E used the real SYS password containing `@` and passed through
`openExecutor`, pool Ping, physical-session acquisition and bound
`ALL_TAB_COLUMNS` discovery. A separate least-privilege temporary user and its
two-column table also passed the same discovery path. That user, its table, the
downloaded client archive, build containers and temporary Docker volumes were
removed after testing. `yashan-v06` was deliberately left running.

Do not copy the client libraries into this repository or a distributable image.
The Go repository is Apache-2.0, but the C client redistribution, static-link,
CI-runner and base-image rights were not established. Written vendor approval
is required before any such distribution. The test copy must be ephemeral and
deleted after validation.

## Dialect findings

- Metadata: native `ALL_TABLES` and `ALL_TAB_COLUMNS` exist. The discovery SQL
  uses `ALL_TAB_COLUMNS`, `?` binds, fixed projections, fixed ordering and the
  shared 10,000-row/read-size limits. On 23.4.1.109 `COLUMN_ID` is zero-based,
  so the fixed query returns `TO_CHAR(COLUMN_ID + 1)`.
- Identifiers: unquoted identifiers are case-insensitive; double quotes retain
  case and permit otherwise invalid names. The authorization capability still
  accepts only its conservative identifier subset and double-quotes it.
- Pagination: `FETCH FIRST 2 ROWS ONLY` was verified. `ROWNUM` is also a native
  SQL construct. The current general-query path remains disabled, so no
  pagination rewrite is performed.
- NULL: in `yashan` mode an empty string evaluates as `NULL` (verified with a
  `CASE` expression). This differs from PostgreSQL and MySQL mode.
- Plans: `EXPLAIN SELECT ...` returns one `PLAN_DESCRIPTION` column across a
  variable number of formatted text rows. `AUTOTRACE` executes the statement
  and reports runtime statistics. No parser or stable authorization contract
  is implemented in this batch.
- Errors: server diagnostics use `YAS-` followed by five digits; for example a
  missing table returned `YAS-02012`. No exhaustive, version-qualified error
  classifier is implemented, so native diagnostics are reduced to safe generic
  errors at the current boundary.
- Types: YashanDB has its own type set and implicit-conversion rules despite
  substantial Oracle-compatible syntax. Discovery returns the server's
  `DATA_TYPE` text without inventing PostgreSQL/MySQL mappings.

Official references:

- <https://github.com/yashan-technologies/yashandb-go>
- <https://github.com/yashan-technologies/yashandb-client>
- <https://doc.yashandb.com/yashandb-en/23.4/en/All-Manuals/Reference-Manual/System-Views/ALL-Views/ALL_Views.html>
- <https://doc.yashandb.com/yashandb-en/23.4/en/All-Manuals/Development-Guide/SQL-Reference-Manual/SQL-Statements/EXPLAIN.html>
- <https://doc.yashandb.com/yashandb-en/23.4/en/All-Manuals/Performance-Tuning/Performance-Tuning-Features-and-Tools/AUTOTRACE.html>
- <https://doc.yashandb.com/yashandb-en/23.4/en/All-Manuals/Reference-Manual/Error-Codes.html>

## Implemented boundary

`openExecutor` recognizes `db_type=yashan`. `YashanExecutor` owns an
independent bounded pool, performs a deadline-bounded `Ping`, probes the current
schema, and can acquire/release a physical `sql.Conn` session. `ListSchema`
performs bounded native column discovery using only identities supplied by the
caller; SQL and placeholders remain inside the capability implementation.

The build without `-tags yashan` recognizes the datasource type but rejects
opening it with a safe connection error because no native driver is registered.
This is deliberate fail-closed behavior, not a protocol fallback.

## Deferred, fail-closed boundary

The following operations return safe errors and do not submit caller SQL:

- general `Query`;
- `Execute`;
- `BeginWriteTx` and transaction execution;
- `EXPLAIN` parsing and cost authorization;
- sampled reads (because `Sample` ultimately requires the disabled general
  query path);
- detailed `YAS-xxxxx` classification.

These require later small batches with parser qualification, transaction-state
semantics, stable cancellation testing, plan fixtures and versioned error-code
coverage. Unknown output or diagnostic formats must continue to fail closed.

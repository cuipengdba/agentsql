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

## Packaged driver and client runtime

Starting with AgentSQL v0.5.0, official Linux release tarballs, the systemd
installer payload and GHCR runtime images include the YashanDB Go driver and a
platform-matched YashanDB C client runtime. The redistribution basis recorded
for this release is the user's 2026-10-03 declaration that vendor authorization
has been obtained. No written authorization file was supplied to this
repository; this statement does not claim that the C client is covered by the
Go driver's Apache-2.0 license.

The official `agentsql` binary is compiled with `-tags yashan`, so the `yasdb`
driver is registered in `database/sql`. The C shim still loads
`libyascli.so` dynamically when a YashanDB connection is opened. The release
therefore carries the complete client `lib` directory rather than relying on a
library copied from a database-server image.

## Version and source evidence

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

The module is pinned in `go.mod`/`go.sum`; Go's module record resolves v1.4.4
to tag `refs/tags/v1.4.4`, commit
`93d0929514d7a33b7e081c692764ec1024fc8b75`, with module checksum
`h1:162fBTXk77Gski56O58ZU7Q7GDJK0pmX3HstmMnvZ58=`. Its repository carries an
Apache-2.0 `LICENSE`.

The packaged C client is version 23.4.7.100 from the vendor's official
`yashan-technologies/yashandb-client` repository, pinned to commit
`a72b24d63ba0e43820c43d7443c0e4fd0ab304fd`. Release builds download the
architecture-specific archive and fail closed unless its SHA-256 matches:

| Release architecture | Vendor archive | SHA-256 |
| --- | --- | --- |
| linux/amd64 | `yashandb-client-23.4.7.100-linux-x86_64.tar.gz` | `403ff0852712a7cfbaeb70269e4acf2391960dddcc374bee4227e68c4680d95f` |
| linux/arm64 | `yashandb-client-23.4.7.100-linux-aarch64.tar.gz` | `69752b7bac8962ab0470687052462c7d7f0edb279959e5416910935b984a43d4` |

The official client repository identifies these archives as YashanDB C driver
packages but does not publish a standalone license file alongside them. Their
redistribution here relies on the user-declared vendor authorization above.

The native driver import remains isolated behind `yashan && cgo` in
`yashan_driver.go`. The root image builds in `golang:1.26-bookworm` with GCC;
the native release builder also enables CGO and passes `-tags yashan`. No C
client header or link-time library is required because v1.4.4 compiles its shim
from module sources and uses `dlopen` at runtime.

The packaged runtime locations are:

- systemd installation: `/usr/local/lib/agentsql/yashandb`, with the unit
  setting `LD_LIBRARY_PATH`;
- release tarball: `lib/yashandb` below the extracted package root;
- official container: `/opt/yashandb-client/lib`, set in the image
  `LD_LIBRARY_PATH`.

For a directly extracted tarball, run from its package root:

```bash
export LD_LIBRARY_PATH="$PWD/lib/yashandb${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}"
./agentsql --version
./agentsql serve -c ./deploy/systemd/config.yaml
```

For source verification in the required container toolchain:

```bash
docker run --rm -v "$PWD:/src" -w /src golang:1.26-bookworm sh -lc '
  export PATH=/usr/local/go/bin:$PATH
  CGO_ENABLED=1 go build -tags yashan ./...
  CGO_ENABLED=1 go test -tags yashan -short \
    ./internal/authorizedexecute/internal/businessdb
'
```

Troubleshooting remains fail-closed. `libyascli.so: cannot open shared object
file` means the process does not see its packaged library directory. An
undefined symbol, wrong ELF class/architecture, or `YAS-02143` requires
checking the Go driver, standalone client, server version and CPU architecture;
do not mix in libraries copied from a server image.

The final E2E used the real SYS password containing `@` and passed through
`openExecutor`, pool Ping, physical-session acquisition and bound
`ALL_TAB_COLUMNS` discovery. A separate least-privilege temporary user and its
two-column table also passed the same discovery path. That user, its table, the
downloaded client archive, build containers and temporary Docker volumes were
removed after testing. `yashan-v06` was deliberately left running.

Batch 30 reproduced the batch-29 failure before changing business behavior. The
real AgentSQL path returned `columns=[1] rows=[[1]] err=<nil>` for
`SELECT 1 FROM DUAL`: this was a successful one-row query, not an empty result
or a swallowed driver error. `YashanExecutor` had anonymously embedded the
common `limitedSQLExecutor`, whose promoted `Query` validates a narrow SELECT
and then calls `database/sql.QueryContext`. The same path was also exposed by a
physical limited-dialect session. In the official v1.4.4 driver source,
`statement.go` implements `YasStmt.Query` at line 45 and `QueryContext` at line
57, while `rows.go` implements `YasRows.Next` at line 99. The driver's normal
result path therefore matched the observed row; the missing guard was in
AgentSQL rather than the driver.

The v0.5 boundary remains metadata-only. Batch 30 added Yashan-specific pool
and session `Query` guards that return a safe `DBStageQuery` execution error
before validation or driver submission. The typed metadata capability still
uses its fixed internal SQL directly. The post-fix real
`TestYashanDiscoveryE2E` passed in 0.68 seconds, including connection, Ping,
physical-session lifecycle, non-empty `SYS.ALL_TAB_COLUMNS` discovery, both
pool and session Query rejection, and EXPLAIN rejection.

| Capability | v0.5 boundary | Batch-30 verification |
| --- | --- | --- |
| Native connection and Ping | supported | real 23.4.1.109 E2E PASS |
| Physical session acquire/release | supported | real E2E PASS; session Query remains guarded |
| Typed `ListSchema` discovery | supported | real non-empty `SYS.ALL_TAB_COLUMNS` PASS |
| General pool/session `Query` | fail closed | pre-fix one-row result reproduced; post-fix real E2E rejection PASS |
| `EXPLAIN` | fail closed | real E2E rejection PASS |
| `Execute`, writes and transactions | fail closed | unchanged; no caller SQL is submitted by these paths |

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
- <https://doc.yashandb.com/yashandb/23.4.6/zh/All-Manuals/Development-Guide/Go-Driver/Go-Driver-Installation/Installing-Go-Driver-%28Linux%29.html>
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

Official v0.5 builds register `yasdb` through the `yashan` build tag and ship
the matching client runtime. A custom source build that omits the tag still
recognizes the datasource type but rejects opening it with a safe connection
error; this remains deliberate fail-closed behavior, not a protocol fallback.

## Deferred, fail-closed boundary

The following operations return safe errors and do not submit caller SQL:

- general pool and physical-session `Query`;
- `Execute`;
- `BeginWriteTx` and transaction execution;
- `EXPLAIN` parsing and cost authorization;
- sampled reads (because `Sample` ultimately requires the disabled general
  query path);
- detailed `YAS-xxxxx` classification.

These require later small batches with parser qualification, transaction-state
semantics, stable cancellation testing, plan fixtures and versioned error-code
coverage. Unknown output or diagnostic formats must continue to fail closed.

# DM8/Oracle dialect capabilities and verification boundaries

**Language:** [English](dm-oracle-dialect.md) | [Simplified Chinese](../dm-oracle-dialect.md)

> Status: v0.5 Description of historical batch capabilities; the latest DM8 real database closed loop is 2026-10-08 batch sixty-five, and batch seventy-one turns the DM8 matrix green according to its evidence. Green is limited to the read-only SELECT, column lineage, R010, audit, and mobile phone number masking of specified Pack3 and `COMPATIBLE_MODE=0` instances; R006 is not verified, multi-row paging/JOIN/function/system directory is not tested, and writing is not released. Not a full compatibility or production certification statement.

## Batch 63: Offline parser boundaries

This batch only verifies the SQL structure, AST flag, and column lineage of `internal/parser`. It does not connect to the DM/Oracle instance, nor does it change the executor release rules. Added table driven use case coverage:

1. Oracle `ROWNUM < n`, `<= n`, `= n` are recognized as row count constraints in simple `WHERE` and the `ROWNUM` pseudo column is not recorded as a physical column.
2. `ROWNUM` projection, reverse comparison, `>`, negative/decimal, multiple conditions, `OR` and `JOIN ON` forms remain `ErrUnparseable`; DM's unconfirmed `ROWNUM` form is also rejected.
3. DM/Oracle's `ORDER BY` projection aliases and positive integer ordinal numbers no longer produce spurious physical column dependencies; zero, out-of-bounds, decimal ordinal numbers, and duplicate aliases are rejected.
4. The true values ​​of `NULL = NULL` and `NULL != NULL` remain unknown, and `WhereTautology` cannot be falsely reported; `NULL IS NULL` is still recognized as always true.
5. Oracle's empty string is treated as NULL: `'' IS NULL` is true, `'' IS NOT NULL` is false, and `'' = ''` is not judged to be always true; the pattern semantics of the DM empty string predicate are not confirmed by this batch, and the constant truth judgment remains unknown.
6. Added offline regression to existing controlled forms of DM `LIMIT n OFFSET m`, `LIMIT m,n`, `TOP n` and Oracle `OFFSET/FETCH`; negative numbers, incomplete paging, DM `TOP` mixed tail paging, Oracle `TOP/LIMIT` and unsupported `WITH TIES` rejected.
7. Oracle `ALTER SESSION`, `MERGE INTO`, and DM `BACKUP/RESTORE` maintain the `ErrUnparseable` boundary of the read-only parser.

The above is a local parser behavioral test and does not constitute syntax certification for any DM compatibility mode or Oracle server version.

## v0.5 Current access conclusion

| path | DM8 | Oracle |
| --- | --- | --- |
| Control plane data source registration and ping | `db_type=dm`, native driver; establish connection with specified schema | `db_type=oracle`, `go-ora/v2`; `database` fill in the service/PDB name |
| Underlying read-only query | Single narrow SELECT of `limitedSQLExecutor`; bounded support for `TOP`, `LIMIT` and `FETCH` | Narrow SELECT for same path; `FETCH`/`ROWNUM` only, reject `TOP`/`LIMIT` |
| Metadata and Sampling | Fixed `SYS.ALL_TAB_COLUMNS` query and binding parameters; typed sample uses `LIMIT n` | Fixed `ALL_TAB_COLUMNS` query and binding parameters; typed sample uses `FETCH FIRST n ROWS ONLY` |
| Analysis and bloodline | Controlled SELECT subset of `parser.NewParser("dm")` | Controlled SELECT subset of `parser.NewParser("oracle")` |
| Gateway authorization and masking | The batch sixty-five real database verifies the R010 table strategy, column lineage, mobile phone number masking and audit link; the complete column-level authorization, R006, etc. are not verified. The "not connected" of the old batch below belongs to the historical status. | Full gateway authorization/masking closed loop still unproven |
| INSERT / UPDATE / DELETE / DDL, write transaction | Bottom layer `Execute` and `BeginWriteTx` rejected | Tongzuo |
| TLS/communication encryption | The current driver adaptation does not implement verifiable configuration; explicit TLS options are rejected | Tongzuo |

Therefore, data source registration, ping, underlying read-only capabilities, and a standalone parser cannot be equated to full Gateway CRUD. For the bounded read-only protection closed loop of DM8 specified instance, see [ batch sixty-five real database report ](dm8-verification.md); Oracle still follows the existing boundaries. Create, update, delete, full column-level authorization, encrypted communication, cancellation and connection reuse still need to be verified separately; there is no automatic fallback to the PG/MySQL dialect.

The currently documented real Oracle database version is **Oracle AI Database 26ai Free 23.26.3.0.0** (`FREEPDB1`). This record does not certify compatibility with individually named Oracle Database 23ai Free images or commercial editions. `-2501` of DM8 is a tested authentication failure classification; the test account and password are only historical fixtures, not deployment credentials. `go.mod` currently declares Go 1.26.0; the text below about Go 1.25 driver selection is a record of decisions made at that time.

Offline regression can run `go test -short ./internal/parser ./internal/authorizedexecute/internal/businessdb ./internal/adminapi`; DM/Oracle real database E2E requires their respective explicitly enabled test environments, and passing the short test cannot replace the real database acceptance. The PostgreSQL parser P99 release threshold for v0.5 remains individually failed, and this set of tests from DM/Oracle does not change that threshold.

## Batch 32: Controlled SELECT parser and projected lineage

`parser.NewParser("dm")` and `parser.NewParser("oracle")` now have registered standalone Oracle-compatible read-only analyzers. It is the frozen profile of v0.5: the DM syntax baseline only uses the evidence obtained in `COMPATIBLE_MODE=0` and Pack3 instances in batch 28, and the Oracle baseline uses the Oracle Free 23.26.3 path recorded in this article; the parser does not automatically expand the syntax according to the unknown server version. This registration only adds read-only AST/lineage analysis capabilities, does not modify the query release or rejection logic of `limitedSQLExecutor`, and does not alias DM/Oracle to the PostgreSQL/MySQL parser.

The support scope is strictly limited to the following subset without parentheses, single `SELECT`:

- `SELECT [DISTINCT]`; Projection items can only be direct columns (up to `schema.table.column`), strings/numbers/`NULL`/Boolean constants, `*` or `alias.*`, and projection aliases are supported;
- There must be a `FROM` physical relationship, and only single-table, multi-table comma joins, or `INNER/LEFT/RIGHT/FULL/CROSS JOIN` are accepted; non-CROSS JOIN must have a simple predicate without brackets `ON`; the table alias uses Oracle-compatible's non-`AS` form;
- `WHERE`/`ON` only accepts direct columns or constants between `= != <> < <= > >= LIKE`, `IS [NOT] NULL`, and `AND`/`OR`; `ORDER BY` only accepts direct columns and `ASC/DESC`;
- DM only additionally accepts `TOP n`, `LIMIT n`, `LIMIT n OFFSET m`, `LIMIT m,n` and `OFFSET ... ROWS FETCH ... ROWS ONLY` that have been proven by the real database; Oracle only accepts `OFFSET/FETCH` and continues to reject `TOP/LIMIT`;
- Output `AST.Tables`, `AST.Columns`, `DirectProjections` and `ProjectionLineages`. Direct columns record unique physical origin; unqualified multi-table columns are marked `ambiguous` and do not guess dependencies; constants are `source_free`; wildcards are variadic lineage. The parser does not hold a catalog, so the "expansion" of `*` is only accurate to the wildcard column set corresponding to the physical table, and cannot fabricate the actual column names; true column-by-column expansion still requires the caller to provide metadata.

The following constructs uniformly return explicit errors containing `ErrUnparseable` even though the vendor SQL itself may be legal: functions and any parentheses, subqueries, CTEs, arithmetic or `CASE` expressions, `GROUP BY/HAVING`, set operations, `CONNECT BY`, `NATURAL/USING JOIN`, comments, bind, semicolons/multistatements, non-`SELECT` statement, qualified columns that cannot be resolved to the current `FROM` scope, and all unlisted structures. This fail-closed profile is a true subset of the full vendor grammar and does not represent a full parser, level authorization/masking pipeline, or vendor certification.

## Batch 28: DM8 real database returns end-to-end

### environment and drivers

- Container `dm8-v06` continues to run, mirror `dm8_single:dm8_20241022_rev244896_x86_rh6_64`, port mapping `127.0.0.1:5236->5236/tcp`; TCP detection is successful.
- disql measured `SYSDBA/"Agentsql@2026"@localhost:5236` successfully logged in, instance `STATUS$=OPEN`, version `03134284294-20241009-244896-20119 Pack3`, `SF_GET_CASE_SENSITIVE_FLAG()=1`, `COMPATIBLE_MODE=0`.
- The server `ENABLE_ENCRYPT=0`, `AUTO_ENCRYPT=0`, `FORCE_CERTIFICATE_ENCRYPTION=0`, `AUTH_ENCRYPT_NAME` and `COMM_ENCRYPT_NAME` are all empty; the SSL certificate or communication encryption DSN parameters are not enabled in this batch. The driver's own login password handshake configuration and server-side communication encryption are not the same switch.
- `go.mod` already has `github.com/godoes/gorm-dameng v0.7.2`. When running, the import is `github.com/godoes/gorm-dameng/dm8` and the registration name is `dm`, so this batch does not add or upgrade the driver. Passwords containing the original `@` pass a real Go Ping; this version splits the host by the last `@` and cannot be repurposed to use the generic URL userinfo encoding which would preserve the literal `%40`.
- The supply chain remains fail-closed: v0.7.2's Go module tarball and native module cache do not contain `LICENSE`, `COPYING` or `NOTICE`. The current main branch of the upstream later appeared as MIT `LICENSE`, but this cannot be used to infer that the v0.7.2 release and the DM official driver source code compiled therein have been explicitly authorized for redistribution; legal/supply chain confirmation is still required before entering the release.

### Real differences and fixes

| Adaptation surface | Batch 21 offline hypothesis | Batch twenty-eight real DM8 results and processing |
| --- | --- | --- |
| current schema | After construction, execute `SELECT USER` and use the logged in user as the metadata owner | DSN `schema=` is executed by the driver `SET SCHEMA`; the real results may appear `USER=只读账号`, `CURRENT_SCHID=owner schema`. Changed to `SF_GET_SCHEMA_NAME_BY_ID(CURRENT_SCHID)`, column discovery for unqualified schema now follows DSN owner |
| Pagination | DM typed sampling generates `LIMIT n`; access control accepts `TOP/LIMIT/FETCH/ROWNUM` The | `LIMIT 2 OFFSET 1` returns 2, 3; `LIMIT 2,1` returns 3; `OFFSET 1 ROWS FETCH NEXT 2 ROWS ONLY` returns 2, 3; `TOP 2` returns 1, 2; offset 0 returns to the homepage, offset 99 and 2147483647 return the empty set |
| EXPLAIN result set | `EXPLAIN FOR` expected 19 columns | The column order is consistent with the 19 column definitions. The real tree of the secondary index is `NSET2/PRJT2/BLKUP2/SSEK2`, normalized `rows=1 cost=1`; the real tree of the non-index condition is `NSET2/PRJT2/SLCT2/CSCN2`, the root estimate is `rows=62 cost=1`, and the underlying scan is 2505 rows |
| EXPLAIN operator scope | Only accept offline samples whitelist | The real plan in the system view also appears `PIPE2`, `UNION ALL`, `ACTRL`, `DISTINCT`, `DSCN`, `HEAP TABLE SCAN`, etc. This batch does not expand the authorization whitelist based on only one sample; these forms remain fail-closed to avoid misjudgment of unknown plans as safe |
| Type and value representation | Reserved `DATA_TYPE` manufacturer string | The real database discovery sequence is `INT/BIGINT/VARCHAR/VARCHAR/DECIMAL/TIMESTAMP/DATE/VARBINARY/CLOB/BLOB`. The `DECIMAL(12,2)` value `0.01` is read as the string `.01` by the v0.7.2 driver; this batch records the real output, and no unauthorized zero-filling is allowed to change the value representation. |
| error code | `-2111` Uncategorized | The real database confirmed `-2111=Invalid column name`, now normalized to `DB_COLUMN_NOT_FOUND`. The classifications for `-2501` authentication, `-2106` objects, `-2007` syntax and `-5501/-5515` permissions were also verified against the real database. |

### Repeatable end-to-end evidence

`TestDMDiscoveryE2E` uses SYSDBA only for fixture management. Each time it generates a unique owner and read-only account with a nanosecond suffix, and creates 2505 rows of test tables and secondary indexes; the read-only account only explicitly obtains `CREATE SESSION` and the target table `SELECT`. The test sequence and actual results are as follows:

1. Both owner and read-only accounts are connected through the native Go driver; incorrect password returns `DB_AUTHENTICATION_FAILED/-2501`.
2. The read-only account uses owner as the DSN schema, and the actual observation is `USER=只读账号`, active schema=owner; `SYS.ALL_TABLES` finds the unique fixture table, and `SYS.ALL_TAB_COLUMNS` finds 10 columns and the above types.
3. The controlled query correctly reads `O'Reilly @ 上海`; typed sample's `LIMIT 2` returns `[[1 K0001] [2 K0002]]`.
4. Paginate to get the first page `[[1] [2] [3]]`, the last page `[[2504] [2505]]`, and the super large offset empty set; for large results, use row limit 2000 to return `rows=2000 truncated=true`.
5. Index EXPLAIN is normalized to `1|0|NSET2|1|1`, `2|1|PRJT2|1|1`, `3|2|BLKUP2|1|1`, `4|3|SSEK2|1|1`; sequential scan is normalized to `NSET2/PRJT2/SLCT2/CSCN2`. `Raw` does not contain table names, index names, or SQL literals.
6. Non-existent columns, objects, and syntax errors result in `-2111/-2106/-2007` respectively; read-only accounts directly try INSERT and CREATE TABLE and obtain `-5501/-5515` respectively, which are normalized to insufficient permissions.
7. Both pooled connections and exclusive sessions can read fixtures; test cleanup to close the connection and delete the two unique test users (the owner's schema, table, index and 2505 rows are deleted with `CASCADE`), and then query `SYS.DBA_USERS` to assert that the remaining number is 0.

Real regression command using Go 1.26 Linux/CGO environment:

```text
AGENTSQL_DM_E2E=1 DM_HOST=host.docker.internal DM_PASSWORD=<secret> \
go test ./internal/authorizedexecute/internal/businessdb -run '^TestDMDiscoveryE2E$' -count=1 -v
```

### Uncovered and blocked

- At the time of batch 28, `parser.NewParser("dm")` still returned `ErrUnsupportedDialect`, so the submission itself only proved narrow SELECT access control, object query, metadata and EXPLAIN; batch 32 had supplemented the bounded parser/projection lineage listed above, but did not retroactively rewrite the test conclusion of batch 28.
- Unverified SSL, communication encryption, LOB content reading/writing, time zone conversion, physical connection reuse after context cancel, write transactions and complete Gateway authorization/masking/auditing closed loop.
- The Go environment of the Windows host is `CGO_ENABLED=0`, and the existing `pg_query_go` parser in the repository cannot be compiled; this batch uses the existing `golang:1.26-bookworm` image to run the Linux CGO test. The businessdb unfiltered full test also includes use cases that automatically start PostgreSQL/MySQL testcontainers, and Docker sockets are not granted to the build containers; DM/Oracle directed offline sets and DM real database E2E pass independently.

## 0. Capability matrix (Batch 21 baseline, DM updated from Batch 28)

This section retains the Batch 21 baseline and Oracle caliber; DM’s current real database caliber is based on the previous Batch 28 chapter. Batch 21 did not connect to external databases at that time, and the DM EXPLAIN conclusions only came from official document samples; these historical offline tests are not retrospectively stated as real database actual measurements.

| Ability | DM8 | Oracle |
| --- | --- | --- |
| Connections and Pools | Native `dm` driver, Ping, current schema, pools and exclusive physical sessions implemented | `go-ora/v2`, Ping, `CURRENT_SCHEMA`, pool and exclusive physical sessions implemented |
| metadata | Fixed query `SYS.ALL_TAB_COLUMNS`; `?` bind; bounded result reading; batch twenty-eight fixed to select default owner by DSN active schema | Fixed query `ALL_TAB_COLUMNS`; `:N` bind; bounded result reading |
| Read-only SQL | Extremely narrow single `SELECT`; pool and session share fail-closed access control | Tongzuo |
| Pagination and pseudo columns | Access control accepts `TOP`, `LIMIT`, `FETCH`, `ROWNUM`; typed sampling is fixed to generate `LIMIT n` | Access control accepts `FETCH` and `ROWNUM`, explicitly rejects `TOP` and `LIMIT`; typed sampling is fixed to generate `FETCH FIRST n ROWS ONLY` |
| `DUAL` | Can appear in narrow SELECT as a normal object without compatibility mode rewriting | Can appear in narrow SELECT as normal object; offline plan covers `FAST DUAL` |
| sequence | `NEXTVAL`, `CURRVAL` are rejected | `NEXTVAL`, `CURRVAL` are rejected |
| EXPLAIN | Batch 28 has been verified by the real database. The index/sequential scan form of 19 columns `EXPLAIN FOR`; text samples and structured results share `ExplainInfo` normalization; unknown columns, operators, trees or values fail-closed | Structured read `PLAN_TABLE`; verifies unique root, complete parent, acyclic, non-negative finite estimate; recognizes normal, bitmap and domain index |
| Misclassification | Authentication, syntax, permissions, connection, column/object/schema does not exist, lock timeout, unique constraint; the new real `-2111` column in batch 28 does not exist | Authentication, permissions, syntax, column/object non-existence, constraints, interruptions, connections, and resource busy/deadlock, temporary space/shared pool resource shortage |
| Type | Manufacturer `DATA_TYPE` string reserved; no canonical type mapping yet | Same as left; `NUMBER`, date, LOB, JSON/BOOLEAN/Vector, etc. still need independent semantic design |
| write/transaction/trigger | Not implemented; `Execute`, `WriteTx` remain fail-closed | Tongzuo |
| parser/authorization closed loop | v0.5 The objects, columns and projection lineage of the controlled SELECT profile have been implemented, and the unknown structure is fail-closed; the complete rules, column-level authorization and masking pipeline have not yet been connected. | Same as left; Oracle paging only accepts `OFFSET/FETCH` and explicitly rejects `TOP/LIMIT` |

### 0.1 Batch 20 and Batch 21 offline verification

- Dialect corpus covers DM `TOP/LIMIT/FETCH/ROWNUM`, Oracle `FETCH/ROWNUM/DUAL`, Oracle rejection of `TOP/LIMIT`, and the boundary between keywords and real comments within strings/double quotes.
- The typed sampling SQL passes the pure builder golden value verification; this only proves that the statement is generated, and does not mean that the upper layer `Gateway.Sample` has connected to the DM/Oracle authorization link.
- Error classification is verified by constructing `dm8.DmError` and `network.OracleError`, and simulated errors are not regarded as real database measurements.
- Oracle EXPLAIN via inline`model.QueryResult`fixture verifies normal index, bitmap/domain index,`FAST DUAL`, multiple roots, missing parent, rings, illegal values ​​and control characters; the object name is still not entered`Raw`。
- The DM EXPLAIN text fixture verifies the official complex join tree and explicit secondary index tree; the structured fixture verifies the official `EXPLAIN AS A1 FOR` four-node result. The root node `[cost, rows, bytes]` is normalized to `EstCost`/`EstScanRows`, `CSEK/SSEK/SSCN/BLKUP` forms the index signal, and `CSCN` forms the sequential scanning signal.
- DM normalization `Raw` only retains node numbers, levels, operators, estimated rows and costs, but does not retain table names, index names, predicates or SQL literals; text/structured input limits the size, node number and depth, and rejects unknown operators, illegal tree shapes, negative numbers and non-limited costs.

### 0.2 DM EXPLAIN fixture source

- [Dameng official "Data Query Statement" 4.19.1 EXPLAIN](https://eco.dameng.com/document/dm/zh-cn/pm/check-phrases.html): complex `NSET2/PRJT2/HASH LEFT SEMI JOIN2/CSCN2/CSEK2/BLKUP2/SSEK2` text tree and `[cost, rows, bytes]` triplet.
- [Dameng official "Data Query Statement" 4.19.2 EXPLAIN FOR](https://eco.dameng.com/document/dm/zh-cn/pm/check-phrases.html): `EXPLAIN AS A1 FOR` four-node example corresponding to the 19-column result set definition.
- [Dameng official "Data Query Statement" 4.22 Specify the index to query ](https://eco.dameng.com/document/dm/zh-cn/pm/check-phrases.html): `BLKUP2 + SSCN` and `BLKUP2 + SSEK2` text trees.
- [Dameng official "Appendix 4: Execution Plan Operators" ](https://eco.dameng.com/document/dm/zh-cn/pm/dm8-admin-manual-appendix4.html) and [ "Query Optimization" EXPLAIN FOR column description ](https://eco.dameng.com/document/dm/zh-cn/pm/query-optimization.html): operator semantics and `LEVEL_ID/OPERATION/ROW_NUMS/COST` field meaning.

### 0.3 Clear restrictions and historical to-dos

- Narrow gate control and batch 32 parser are not complete SQL parsers: functions, brackets, subqueries, CTE, bind, comments and trailing semicolons are rejected even if they are legal SQL; unquoted identifiers will fail-closed if they have the same name as restricted keywords.
- DM EXPLAIN, Ping, metadata, paging, permissions and major error classification have been rerun with valid credentials in batch 28; connection reuse after context cancel, SSL/communication encryption and wider operators still need to be verified.
- Oracle does not have external instances in this batch; new paging, error codes and plan forms are waiting for the return of Oracle's real database. The existing environmental evidence for Batch 13 does not mean re-testing for Batch 20.
- DM/Oracle's canonical type, LOB/time zone/charset, full vendor grammar, catalog-driven `*` column-by-column expansion, column-level authorization/masking, transactions, triggers, connection reuse after cancellation remains unverified.

## 1. Initial conclusion of batch 10 (historical record)

Neither DM8 nor Oracle map to the existing PostgreSQL/MySQL dialect. Both require independent connections, system catalogs, SQL parser/rule capabilities, execution plan parsing, error classification and transaction state implementation.

This batch only limits the trusted closed loop to the following boundaries:

- `db_type=dm` / `db_type=oracle` has been registered to `businessdb.openExecutor`;
- Both dialects can establish the `database/sql` connection pool, perform pings with deadlines, detect the current schema, acquire/release exclusive `sql.Conn` sessions, and close the connection pool;
- `businessdb.ListSchema` uses fixed SQL, bound parameters, and bounded result reads to access respective system directories;
- Both DM8 and Oracle have been discovered against real containers via Ping and a fixed object's columns;
- Ordinary Query, Execute, in-session SQL, WriteTx, and EXPLAIN are all explicitly fail-closed and return a unified database execution error that does not include the original driver text; write operations of read-only executors still return read-only rejections first.

This batch did not register DM/Oracle parser, did not release control plane datasource verification, and did not implement sampling, business query, writing, transaction status, execution plan normalization, column-level authorization, masking or complete audit closed loop. Therefore, the results of this batch cannot be expressed as "AgentSQL has supported DM/Oracle"; the accurate expression is "the minimum slice of connection and typed metadata has been measured".

## 2. Actual measurement environment and evidence

| Project | DM8 | Oracle |
| --- | --- | --- |
| container | `dm8-v06`, mirror `dm8_single:dm8_20241022_rev244896_x86_rh6_64`, `127.0.0.1:5236` | `oracle-v06`, mirror `container-registry.oracle.com/database/free:latest`, `127.0.0.1:1521` |
| health status | Running | Running and Docker health=healthy |
| Server version | `DM Database Server 64 V8`；`03134284294-20241009-244896-20119` | `Oracle AI Database 26ai Free Release 23.26.3.0.0` |
| Key configuration | `SF_GET_CASE_SENSITIVE_FLAG()=1`；`COMPATIBLE_MODE=0` | SID `FREE`; service name `FREE`; writable PDB/service `FREEPDB1`; `compatible=23.6.0` |
| license | `V$LICENSE.EXPIRED_DATE=2026-10-15` | Free Mirror |
| native client | `/opt/dmdbms/bin/disql`; `/opt/dmdbms/bin` needs to be added to `LD_LIBRARY_PATH` for operation | `/opt/oracle/product/26ai/dbhomeFree/bin/sqlplus`; `/ as sysdba` available |
| Go E2E | `TestDMDiscoveryE2E`: Ping; Found self 31 columns from `SYS.ALL_TAB_COLUMNS` | `TestOracleDiscoveryE2E`: Ping; discover columns from `SYS.DUAL` |

DM `EXPLAIN` returns multi-line text trees, such as `#HASH LEFT SEMI JOIN2: [cost, rows, bytes]`, `#CSEK2`, `#CSCN2`, without PostgreSQL JSON envelope; the official also provides `EXPLAIN FOR` returned as a 19-column result set. The current driver's standard `database/sql` query channel cannot read the internal text of ordinary `EXPLAIN`, so `EXPLAIN FOR` is selected for runtime adaptation, the offline text parser is used to lock the official sample grammar verbatim, and the two are merged into the same node normalizer. Oracle needs to execute `EXPLAIN PLAN ... FOR ...` first and then structurally read `PLAN_TABLE`. None of these formats are left to guesswork by existing PostgreSQL/MySQL parsers.

## 3. Driver and DSN Decisions

### 3.1 DM8

This batch uses the DM official Go driver source code compiled in `github.com/godoes/gorm-dameng/dm8` v0.7.2; the driver registration name is `dm`, based on `database/sql`, and does not require CGO. The DSN base shape given by the DM official document is `dm://user:password@host:port[/schema]?...`, and the default port is 5236.

Actual testing found that the DSN parser of v0.7.2 will split userinfo according to strings on its own, but will not decode the percent code generated by `url.UserPassword`. When the password contains `@`, the regular URL builder will send the literal `%40` and result in `-2501 用户名或密码错误`; the driver in turn uses the last `@` as the host separator, so this implementation retains the original `@` within the password for this version. Username colons, `?`, control characters, and schema query delimiters that cannot be represented unambiguously are directly rejected, and decoding cannot be guessed. You must keep this regression case and re-verify DSN behavior when upgrading the driver.

Driver source and redistribution licenses still require legal/supply chain access; current dependencies cannot be considered approved for distribution simply because the technology is available.

Reference: [DM Go Programming Guide ](https://eco.dameng.com/document/dm/zh-cn/pm/go-rogramming-guide.html), [godoes/gorm-dameng](https://github.com/godoes/gorm-dameng).

### 3.2 Oracle

The known `github.com/godror/godror` path does use ODPI-C/OCI, requires CGO, and requires Oracle Client at runtime. The service name should preferably use `host:port/service_name` of Easy Connect; the SID can only be clearly expressed through the complete Connect Descriptor, and SID and service name cannot be mixed.

Oracle will provide the official pure Go `github.com/oracle/go-oracledb/v26` in 2026, but the v26.0.0-beta/v26.0.1-beta downloaded and verified in this batch require Go 1.26.5/1.26.6 respectively, which is higher than the Go 1.25 baseline of this repository. In order to complete the real connection and metadata verification at the current baseline, this batch temporarily uses pure Go `github.com/sijms/go-ora/v2` v2.9.0, the driver name is `oracle`, and the DSN is generated by its `BuildUrl`; this is a boundary engineering replacement and is not the final production driver finalization.

Subsequent admission must be explicitly selected and recorded among the three:

1. `godror + OCI`: mature but adding CGO, Oracle Client images, dynamic libraries, patches and multi-architecture supply chain;
2. Official `go-oracledb/v26`: pure Go, but currently in beta and requiring an upgrade to the project Go version;
3. `go-ora/v2`: The current minimum slice is connected to 26ai, but security, type, cancellation, encryption, fault and license matrices are still required and production suitability cannot be extrapolated from a single ping.

Reference: [godror README](https://github.com/godror/godror/blob/main/README.md), [Oracle Database Driver for Go](https://github.com/oracle/go-oracledb), [go-ora](https://github.com/sijms/go-ora).

## 4. List of Dialect Differences

| Adaptation surface | PostgreSQL/MySQL current situation | DM8（`COMPATIBLE_MODE=0`） | Oracle 26ai |
| --- | --- | --- | --- |
| connection target | PG database；MySQL database | 5236; login schema; official protocol/driver | 1521; priority service/PDB (this time `FREEPDB1`), SID requires descriptor |
| driving constraints | pgx pure Go; mysql pure Go | This batch of drivers is pure Go; DSN special character parsing has confirmed restrictions; redistribution is pending review | godror requires CGO+OCI; this batch of go-ora is pure Go; the official pure Go driver temporarily exceeds the Go baseline |
| placeholder | PG `$1`；MySQL `?` | `?` | `:1` / named bind |
| metadata | `information_schema.columns` | `SYS.ALL_TAB_COLUMNS` / `USER_TAB_COLUMNS`, etc.; incomplete database construction parameters are in `V$DM_INI` | `ALL_TAB_COLUMNS` / `USER_TAB_COLUMNS` / `DBA_*` under DBA authority; container/PDB will change visible objects |
| schema/catalog | PG schema; MySQL database is schema | User default schema, Oracle style but not equivalent to Oracle | The current schema, CDB/PDB, common/local user and service jointly determine the visible scope |
| identifier | PG/Oracle style double quotes; MySQL backticks | Unquoted values are usually folded according to initialization case rules; this example is case-sensitive; double quotes preserve the exact name | Unquoted usually fold to uppercase; double quotes preserve case; identifier length varies with version/compatible setting |
| Type | PG rich native types; MySQL `column_type` | `VARCHAR`, `NUMBER/DEC`, `DATETIME/TIMESTAMP`, LOB, binary and manufacturer type; cannot directly apply PG OID | `VARCHAR2/NVARCHAR2`, `NUMBER(p,s)`, `DATE/TIMESTAMP`, RAW, LOB, JSON/BOOLEAN/Vector, etc.; `NUMBER` cannot be converted to float unconditionally |
| row limit | `LIMIT n` | The range of support for `TOP`/`LIMIT`/`FETCH` is affected by the version and compatibility mode, and must be solidified with corpus | `FETCH FIRST n ROWS ONLY` or `ROWNUM`; cannot be spliced PostgreSQL `LIMIT` |
| semicolon/script | Drivers usually receive a single SQL statement without semicolon | Disql's `/`, PL/SQL style blocks and single driver execution need to be separated | OCI/driver single execution generally does not accept SQL*Plus terminators; PL/SQL block semantics are different |
| EXPLAIN | PG JSON; MySQL table | `EXPLAIN` Multi-line proprietary text tree | `EXPLAIN PLAN FOR` writes the plan table, which is then read by `DBMS_XPLAN` or structured plan table |
| error code | SQLSTATE / MySQL number classified | DM Numeric error codes need to be classified into a whitelist; unknown codes belong to execution | `ORA-xxxxx` / The driver code needs to be classified into a whitelist; unknown codes belong to execution |
| Read only/timeout/cancel | There is a dedicated implementation | Need to verify session/transaction read-only, connection reusability after context cancel | Need to verify `ALTER SESSION`/transaction read-only, call timeout, break/reset and pool reuse |

## 5. Code design

The new `DMExecutor` and `OracleExecutor` are both embedded in `limitedSQLExecutor`:

- The constructor first verifies the datasource and connection pool parameters, then `sql.Open`, sets the upper limit of the pool, pings, and detects the current schema with a fixed query; if any step fails, the pool is closed; `OpenSession` can bind and release an exclusive physical connection, but its SQL/transaction method continues to reject;
- `Dialect()` returns exactly `dm` or `oracle`; `Manager.SnapshotPools` can read standard `database/sql` pool statistics;
- typed metadata only accepts `SchemaTable` identities, rejects illegal identifiers, and has a maximum of 256 relationships; the query template is fixed, all owner/table uses bind, and the input is not spelled into SQL; the results are still subject to the 256 columns, cells/rows/total bytes, and 10,000 row limit;
- Metadata driver errors are converted into unified `DBError` without driver original text within the capability boundary;
- DM `limitedDialectExplainer` executes `EXPLAIN FOR <SQL>`, strictly reads the official 19 columns and normalizes them to public `model.ExplainInfo`; the pool and session continue to reuse the existing `Explain` timeout, read-only checksum error channel;
- Capabilities that have not yet been verified are unified in `limitedSQLExecutor` and returned to `DBErrorCodeExecution`. Unknown execution plan formats will never produce zero-value plans or release conclusions.

The current authorization and discovery call relationship remains unchanged:

```text
Gateway.ListSchema -> Manager/openExecutor -> DM/Oracle pool -> businessdb.ListSchema -> fixed catalog SQL

Gateway.Sample -> AuthorizedExecute -> parser/rules/EXPLAIN -> executor.Query
                                      (DM/Oracle only registers the v0.5 controlled SELECT profile; full rule/column authorization is still not accessed)
```

In particular, this batch does not let `Gateway.Sample` bypass `AuthorizedExecute` to call the underlying database, nor does it pretend DM/Oracle to be postgres/mysql. This sacrifices complete sensitive discovery but does not weaken authorization, masking, and audit boundaries.

## 6. Subsequent small batch splitting

1. **control plane and complete parser access**: Controlled SELECT objects, columns and projection lineage have been implemented; if you want to access the complete authorization pipeline later, you still need to explicitly review the datasource API/store, rule set, catalog `*` expansion, as well as dangerous functions, CTE/subqueries and DML/DDL corpus. Any AST that cannot be normalized must be rejected.
2. **EXPLAIN real database verification and extension**: Use valid DM credentials to compare ordinary `EXPLAIN` text and `EXPLAIN FOR` result sets, verify the plan record life cycle, and only expand the operator whitelist based on new official/real database fixtures; Oracle continues to prioritize reading structured `PLAN_TABLE` instead of localized ASCII.
3. **read-only query and sampling**: implement dialect's own row limiter and SELECT-only classification; disable trailing semicolon/multi-statement escape; take back `Gateway.Sample` and run sensitive type identification, mask/hash/block/range and truncation use cases.
4. **Error and Cancellation**: Establish authentication, permissions, object non-existence, syntax, constraints, deadlock, resources, timeout, cancellation classification according to the real driver error type; Unknown code fixed execution; Verify physical connection disposal after cancellation.
5. **transaction and write path**: implement Session/WriteTx, transaction status and read-only evidence, and then connect the audit barrier; maintain the existing fail-closed/unknown outcome semantics when the submission result is uncertain.
6. **driver supply chain**: Determine Oracle production driver; audit DM driver authorization and upgrade sources; add Linux/Windows, amd64/arm64, TLS/encryption, Chinese character set, LOB, time zone and fault recovery matrix.
7. **complete acceptance**: Use non-SYS minimum privilege account and synthetic business table to run the "Discover -> Policy -> Allow/Deny -> masking -> Audit" closed loop. SYSDBA/SYSTEM is only used for this batch of environment research and is not a deployment recommendation.

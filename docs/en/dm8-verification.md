# DM8 manufacturer license genuine library verification (batch sixty-five)

**Language:** [English](dm8-verification.md) | [Simplified Chinese](../dm8-verification.md)

Verification time: 2026-10-08 (Asia/Shanghai). This report only records the results of this machine `dm8_single:dm8_20241022_rev244896_x86_rh6_64`. The complete terminal transcript for one pass is located at `C:\Users\Administrator\AppData\Local\Temp\agentsql-dm8-b65-verify.log`; the script will overwrite this log every time it is rerun. The log does not contain database password, API key or license content.

## Reappearance and environment

Execute at `D:\ruanjiansheji\agentsql-v04`:

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/dm8-verify.ps1
```

A non-zero exit from the script indicates failure at any step. It only uses Docker local containers and local loopback addresses: first check `docker inspect dm8-v06`, `ps -eo pid,args`, `/opt/singlestartup.sh`, `/proc/<dmserver pid>/cwd`, `dm.ini`, server logs or the `dm.key` string in the installation binary; after checking the path and port, stop and retain the original container, and use the original license in the independent `dm8-b65` container The file is read-only bound to the discovered path. Follow up with `disql`, offline Go probe and AgentSQL container/API verification. The source code and Go module cache are mounted to `golang:1.26-bookworm` in read-only mode, and `--network none` and `GOPROXY=off` are built. When successful, deactivate the AgentSQL container of this round, delete the test user and two tables of this round; retain `dm8-b65` mounted with a read-only license, and the original `dm8-v06` remains stopped. The AgentSQL API only publishes to the ephemeral port of `127.0.0.1`.

Excerpts from the original text of this investigation:

```text
image=dm8_single:dm8_20241022_rev244896_x86_rh6_64; entrypoint=/opt/startup.sh; dmserver_ini=/opt/dmdbms/data/DAMENG/dm.ini; cwd=/opt/dmdbms/bin; license_target=/opt/dmdbms/bin/dm.key; port=5236; COMPATIBLE_MODE=0
Mount confirmed: D:\ruanjiansheji\db-images\dm8B01200017.key -> /opt/dmdbms/bin/dm.key (readonly)
License log: 32:2026-10-08 13:00:13.105 [INFO] database P0000000283 T0000000000000000283  License will expire on 2027-06-25
```

`dm.key`The evidence of the file name comes from the original instance log`file dm.key not found, use default license!`, and with the installation directory`libdmlic.so`within`dm.key`String review; installation directory from running`dmserver`the actual working directory. Instance configuration`PORT_NUM=5236`, Docker’s original mapping is`127.0.0.1:5236->5236/tcp`. The original license is 648 bytes, SHA-256 is`C2254529D5B3A5CD9D3E9256CD4501A52818FA26FACDD3AAD872B8CAFF0860F8`; The script is read-only and does not copy files.

## Instances, Login and CRUD

Use `/opt/dmdbms/bin/disql /NOLOG` and `LD_LIBRARY_PATH=/opt/dmdbms/bin` in the container, obtain the local configuration password from the `SYSDBA_PWD` environment item of the original container, and enter `CONN SYSDBA/"<redacted>"@127.0.0.1` via stdin. The historical `SYSDBA/SYSDBA` is not assumed as the password for this instance. Instance readiness and identity query:

```sql
SELECT 1 AS READY FROM DUAL;
SELECT * FROM V$VERSION;
SELECT ID_CODE();
SELECT NAME FROM V$DATABASE;
SELECT USER, SF_GET_SCHEMA_NAME_BY_ID(CURRENT_SCHID) AS SCHEMA_NAME FROM DUAL;
SELECT SERIES_NO, EXPIRED_DATE FROM V$LICENSE;
SELECT PARA_NAME, PARA_VALUE FROM V$DM_INI WHERE PARA_NAME='COMPATIBLE_MODE';
```

```text
Server[127.0.0.1:5236]:mode is normal, state is open
1 DM Database Server 64 V8
2 8.4
3 Enterprise Edition
4 DB Version: 0x7000c
5 03134284294-20241009-244896-20119
ID_CODE(): --03134284294-20241009-244896-20119 Pack3
NAME: DAMENG
USER SCHEMA_NAME: SYSDBA SYSDBA
SERIES_NO EXPIRED_DATE: 8B01200017 2027-06-25
PARA_NAME PARA_VALUE: COMPATIBLE_MODE 0
```

Therefore, this time we can confirm the database name `DAMENG`, logged-in user and current schema `SYSDBA`, and compatibility mode `0`; this is not an inference of the default user or default library of other DM instances. The license serial number and expiration date are visible in `V$LICENSE`, and the server log records the expiration date, and `-2501` is no longer reported when logging in.

This round of script runs the following SQL as `AGSQL_B65_134346` and then deletes the test table. The key original output is `INSERT/UPDATE/DELETE: affect rows 1`, updated `NOTE: updated`, and deleted `N: 0`.

```sql
CREATE TABLE AGSQL_B65_134346 (ID INT PRIMARY KEY, PHONE VARCHAR(20), NOTE VARCHAR(64));
INSERT INTO AGSQL_B65_134346 (ID,PHONE,NOTE) VALUES (1,'13800135678','created');
SELECT ID,PHONE,NOTE FROM AGSQL_B65_134346 WHERE ID=1;
UPDATE AGSQL_B65_134346 SET NOTE='updated' WHERE ID=1;
SELECT NOTE FROM AGSQL_B65_134346 WHERE ID=1;
DELETE FROM AGSQL_B65_134346 WHERE ID=1;
SELECT COUNT(*) AS N FROM AGSQL_B65_134346;
```

The same script made a syntax probe for this instance without modifying the data, and all five queries were successful:

```text
SQL=SELECT TOP 1 1 AS V FROM DUAL;                         -> V: 1
SQL=SELECT 1 AS V FROM DUAL LIMIT 1;                       -> V: 1
SQL=SELECT 1 AS V FROM DUAL FETCH FIRST 1 ROWS ONLY;      -> V: 1
SQL=SELECT 1 AS MixedCase FROM dual;                       -> MIXEDCASE: 1
SQL=SELECT 1 AS "MixedCase" FROM DUAL;                     -> MixedCase: 1
```

To the right of the arrow is a compact transcription of disql's original column names and values; see the transcript for the complete original `LINEID` output. This only proves that the single-line form is executable in this instance of `COMPATIBLE_MODE=0`, and does not prove that the semantics of each paged form are exactly the same in multi-line, sorted or other compatibility modes.

## Host driver and AgentSQL protection

The script builds the Windows Go probe offline from the project-locked `github.com/godoes/gorm-dameng` v0.7.2. When running, import `github.com/godoes/gorm-dameng/dm8`, and `database/sql` is registered as `dm`. It connects to `127.0.0.1:5236`, pings via `dm://<reader>:<redacted>@127.0.0.1:5236?schema=SYSDBA`, queries the current identity, reads the table with `?` binding, and attempts an unauthorized INSERT. Reading the account only gets `CREATE SESSION` and two test tables of `SELECT`. Raw output:

```text
driver=github.com/godoes/gorm-dameng/dm8 user=AGSQL_B65_R_134348 schema=SYSDBA bind_phone=13800135678 denied_write=true write_error=Error -5501: No insert permission for [SYSDBA.AGSQL_B65_134346] object
```

The same query is made by`parser.NewParser(model.DialectDM)`Parsing and verifying in offline Linux Go container`ProjectionLineages`middle`PHONE`physical origin. Raw output:

```text
parser_dialect=dm source=SYSDBA.AGSQL_B65_134346 lineage_PHONE=SYSDBA.AGSQL_B65_134346.PHONE
```

The health check of AgentSQL v0.5.0 is `ok`; after registering the data source of `db_type=dm`, target `dm8-b65:5236`, and schema `SYSDBA` via API, `ping` returns `{"ok":true,"latency_ms":10}`. The read-only Agent can only obtain the table policy of `SYSDBA.AGSQL_B65_134346`; the database account can also read another test table `SYSDBA.AGSQL_B65_D_134348`, which is used to verify the R010 policy of AgentSQL itself. Original key fields for MCP `query` and audit:

```json
{"data":{"audit_id":1,"est_scan_rows":1,"redact":{"MaskedCells":1,"TouchedColumns":{"1":"phone"}},"result":{"Columns":["ID","PHONE"],"RowCount":1,"Rows":[["2","138****5678"]],"Truncated":false}},"decision":"allow"}
{"data":{"audit_id":2,"est_scan_rows":1,"hits":[{"Decision":"deny","Risk":1,"RuleID":"R010"}]},"decision":"deny"}
{"decision":"error","error_code":"DB_SYNTAX_ERROR","error_stage":"parse"}
{"total":3,"list":[{"id":3,"decision":"error","error_code":"DB_SYNTAX_ERROR"},{"id":2,"db_type":"dm","stmt_type":"SELECT","objects":"SYSDBA.AGSQL_B65_D_134348","decision":"deny","rule_hits":"[{\"RuleID\":\"R010\"}]"},{"id":1,"db_type":"dm","sql_norm":"SELECT ID , PHONE FROM SYSDBA . AGSQL_B65_134346 WHERE ID = ?","stmt_type":"SELECT","objects":"SYSDBA.AGSQL_B65_134346","decision":"allow","rows_returned":1}]}
```

The JSON above is an excerpt of the preserved fields from the full response; the full response is in transcript. The third input is `SELECT /* batch65-r006 */ ID ...`. In the parser stage, `DB_SYNTAX_ERROR` is returned and an audit is generated. **does not hit R006** and cannot be counted as the passing evidence of R006. The successfully queried mobile phone number only appears in plain text in the `disql`/host raw data verification, and the AgentSQL MCP result is `138****5678`.

## AgentSQL defects discovered and minimal fixes

Two consecutive local wiring gaps were found, both of which have been minimally repaired in the corresponding `internal/` file. Before the fix, the DM data source was registered and Ping was successful, but the first MCP query returned `GATEWAY_INTERNAL`, and the original service log text was:

```text
datasource "dm8-b65" has unsupported dialect "dm"
```

join in`internal/pipeline/pipeline.go`of`dm`type gate and`internal/pipeline/rules.go`General rules for assembly,`internal/rules/generic.go`After the DM AST is accepted, parsing and auditing begin, but the response still fails. Original log text:

```text
validate AST dialect "dm": invalid engine input
```

DM AST/rule dialect later accepted at `internal/engine/engine.go`. After repair, the MCP SELECT at the top of this report is `decision=allow`, masking hit 1 cell, R010 is `decision=deny`, and all three audit items appear. This fix does not extend the syntax of `oracleCompatibleParser`, nor does it liberalize DM writing.

## Differences and verification boundaries

| Project | This DM8 real database evidence | Oracle/PostgreSQL comparison boundaries |
| --- | --- | --- |
| connect | This instance monitors 5236, the Go driver registration name is `dm`, `?` is successfully bound; the DSN is explicitly passed to `schema=SYSDBA` | The existing Oracle/PG adaptation and historical reports in the repository use their own drivers, ports and binding forms; this batch has not been connected to its real database for retesting. Oracle/PG connection strings cannot be used in place of DM. |
| Libraries and schemas | `V$DATABASE.NAME=DAMENG`, the current `USER`/schema is `SYSDBA` | The concepts of PG's database/schema and Oracle's service/PDB/schema cannot be equated accordingly; this batch has not been verified against the real database. |
| Version/Configuration | `V$VERSION`, `ID_CODE()`, `V$DM_INI` are measured as above, `COMPATIBLE_MODE=0` | Oracle/PG version views and compatible parameters cannot be reused directly; the control instances are not re-verified in this batch. |
| Analysis and bloodline | `dm` goes `oracleCompatibleParser`, simple SELECT of `PHONE` physical source checked; annotation SELECT rejected by parser | This only covers the current narrow SELECT subset and is not representative of the full Oracle grammar or PG grammar. |
| Pagination and identifiers | This time DM8 was successful for the single lines `TOP 1`, `LIMIT 1`, and `FETCH FIRST 1 ROWS ONLY`; `MixedCase` without quotation marks is displayed as `MIXEDCASE`, and the double quotation mark alias retains the case. | The repository already has Oracle/PG dialect adaptation and adopts their own paging and identifier rules; this batch has not been retested in its real database, nor has the DM multi-line paging semantics been tested. |
| Rules and masking | R010 table policy interception, mobile phone number mask, three audit tests passed | Oracle/PG's similar links were not retested in this batch; DM's R006 was first rejected by the parser due to comments and was not verified. |
| Permissions | DM `CREATE SESSION` + table level `SELECT` is readable, INSERT returns `-5501` | DM numeric error code is different from the error system of Oracle `ORA-` and PG SQLSTATE; this batch only tested `-5501`. |

The protocol access comparison comes from this successful DM driver probe and the current adaptation source code of the repository: DM use`github.com/godoes/gorm-dameng/dm8`（`go.mod`lockingv0.7.2）、`dm://...?...schema=SYSDBA`;Oracle path call`go-ora/v2` v2.9.0of`BuildUrl(host, port, database, ...)`;PostgreSQL path import`pgx/v5` v5.9.2. The three are independent connection paths in the project and cannot just replace the type name or SQL parser substitution. The last two items are code structure comparisons. This batch has not passed the protocol interconnection test to verify its real database behavior.

This batch does not separately measure DM multi-row paging semantics, complex JOIN, functions, system directories, EXPLAIN, communication encryption, timeout cancellation, connection reuse, HA, Oracle/PG instance differences and other compatibility modes. AgentSQL is validated with this fix for bounded read-only queries and the guard link described; universal writes, full column authorization, and all DM8 syntax are not validated. The license expiration date is `2027-06-25` reported by the database and is not a long-term licensing commitment.

**batch seventy-one matrix decision:** user has confirmed to change the DM8 gear to green. Green only means that the read-only SELECT, column kinship, R010 table policy, audit and mobile phone number masking link of the specified DM8 Pack3 and `COMPATIBLE_MODE=0` instance passed the actual test; R006 has not been verified, multi-line paging/JOIN/function/system directory has not been tested, and writing has not been released. No claim is made as to full DM8 compatibility or production certification. README, SECURITY, compatibility matrix and official website matrix have been synchronized according to this boundary.

## Build, clean and self-check

The final output of the complete run:

```text
PASS: SELECT, column lineage, R010 denial, phone mask, parse failure, and three audit rows
drop verification reader: ... executed successfully
drop verification table: ... executed successfully
drop ungranted-policy table: ... executed successfully
PASS: temporary DM8 user and tables removed; licensed DM8 container retained; AgentSQL container stopped
```

Offline`golang:1.26-bookworm`、`--network none`、`GOPROXY=off`, read-only source code and module cache,`go build ./...`exit 0;`go test -short ./internal/engine ./internal/rules ./internal/parser`Three guarantees passed. pipeline uses all non-test source code and`pipeline_test.go`、`rule_overrides_test.go`、`static_assess_test.go`、`t25_observer_test.go`A list of files consisting of`go test -short`, the result`ok command-line-arguments 0.111s`. complete`go test -short ./internal/pipeline ./internal/authorizedexecute/...` **Failed test build**:Offline cache missing`github.com/moby/sys/user v0.4.1`、`sequential v0.7.0`with`gopsutil/v4 v4.26.6`zip, test dependencies are written in a read-only cache`.lock`failed; this item was not recorded as code test passing. The above missing packages only appear in the test dependency path and have no impact.`go build ./...`With this real database E2E.

For the objects left over from previous failed debugging rounds, 13 tables that exactly match `AGSQL_B65_(D_)?[0-9]{6}` and 9 accounts that exactly match `AGSQL_B65_R_[0-9]{6}` were found through directory query; they were deleted one by one according to the exact name, and `drop_success_count=22` and `remaining_batch65_objects=0` were output. The successful script's own objects in this round are also deleted at the end. The original path of the read-only license of `dm8-b65` is retained and mounted, and the AgentSQL container is stopped. The original license ends with SHA-256 still `C2254529D5B3A5CD9D3E9256CD4501A52818FA26FACDD3AAD872B8CAFF0860F8`.

`git diff --check`Exit 0; new scripts and reports each check for no trailing whitespace. final`git status --short`In addition to the 4 allowed in this batch`internal/`In addition to modifying and adding new scripts and reports, the existing ones before the task is also listed`cmd/agentsql/b5-wal/`and`cmd/agentsql/\357\200\272memory\357\200\272.instance-id`Two untracked items; they were not touched in this batch. No git write operation has been performed, the license has not been copied or submitted, and the matrix gear has not been modified. All "passes" are based on the output of the above commands.

# YashanDB YashanDB batch 71 access verification

**Language:** [English](yashan-verification-b71.md) | [Simplified Chinese](../yashan-verification-b71.md)

Verification date: 2026-10-08 (Asia/Shanghai). This report is only for this machine
`yashandb:yashandb-image-23.4.1.109-linux-x86_64`, container `yashan-v06`,
YashanDB schema and synthetic SQL listed. **has been verbally authorized by the manufacturer (C client redistribution)**; repository
There is no written authorization document or authorization number, and this report does not fictionalize these documents.

## Mirrors, clients and connections

| Check items | Batch seventy-one results |
| --- | --- |
| input image | Local `D:\ruanjiansheji\db-images\yashandb-image-23.4.1.109-linux-x86_64.tar.gz` exists (500650748 bytes), and the corresponding image has been loaded in local Docker; it is not pulled online |
| Example | `yashan-v06` is running, host port 1688; historical server records are Enterprise Edition Release 23.4.1.109, `COMPAT_VECTOR=yashan`, current user `SYS` |
| Server tools | `yasql`, `include/yacli.h`, and `lib/libyascli.so` all exist in the image; probing only checks the existence of files and does not read licenses or credentials. |
| Server-side C library | The historical real database probe returns `YAS-02143` when using the server library and yashandb-go v1.4.4; it cannot be used reliably as a Go driver runtime library, and it is not copied from the image or packaged in this round. |
| standalone client | This machine's old, unreleased dry-run tarball cache independent client 23.4.7.100; only 24 library files of `lib/yashandb` were extracted to the system temporary directory for this round of testing, and the license was not copied. This old product cannot serve as current release acceptance |

The default connection probe uses the value of the `SYS_PASSWD` environment variable already in the container, which is only passed within the process.
Test; the terminal, transcript, and repository do not record this value. `SYS` is only used for creating and cleaning
Temporary users/tables for this round; the business query probe uses the newly created read-only user with minimum permissions. Account/
The current mode can be probed with `SELECT USER FROM DUAL`; here `SYS` is the current mode,
It cannot be applied according to the PostgreSQL database/schema meaning. Compatibility mode with historical examples
The parameter `COMPAT_VECTOR=yashan` prevails, and the compatibility mode is not switched in this round.

## Wiring and authentic evidence

Standard for `internal/parser/parser.go` `NewParser(model.DialectYashan)`
The YashanDB parser of batch66 has been connected. Dialect gates for pipelines, rules, engines are minimally wired.
`YashanExecutor` with physical session run narrow SELECT parser again before submitting SQL and
Controlled SELECT validation of executors; unsupported SQL fails to close before driving. write, transaction
Stay closed with EXPLAIN. When the Go driver is missing, `CGO_ENABLED=1` and `-tags yashan` are given.
Prompt; prompt to set `LD_LIBRARY_PATH` when independent C client is missing.

The native driver probe results of this round of `scripts/yashan-verify.ps1 -RequireLive`:

| probe | result |
| --- | --- |
| Go native driver Ping | PASS |
| `SELECT ? FROM DUAL` parameter binding | PASS |
| The temporary read-only account reads the synthesis table through `?` | PASS |
| INSERT the table with the same account | Rejected by the server, error code `YAS-02213`; only the error code is recorded, not the original diagnosis. |
| clean up | Temporary read-only user and synthetic table deletion PASS |

This proves that the designated independent C client and Go driver can connect to the local real database and have the minimum permissions in this round.
The account write was rejected by the database. Subsequently tagged `yashan`
`TestYashanDiscoveryE2E` Connect/Ping, via real `YashanExecutor`
Physical session SELECT, connection pool SELECT, `ALL_TAB_COLUMNS` column discovery of binding parameters,
And verify that the executor writes with EXPLAIN to continue rejecting. subsequently
`TestYashanPipelineRealE2E` Use real YashanDB connection and two synthetic tables to verify SELECT
allow, `SYS.表.PHONE` projected lineage, list of directional mobile phone number masking, another one can be used by the database
The table read by the account was rejected by R010, and there were three audit records with allow/deny/comment syntax errors.
The annotation was rejected at the parser stage, **does not count against the R006 hit**. This pipeline test uses real
Authentication/policy reading/audit recording ports for database executors, real desensitizers and test fixtures;
The business database account is `SYS`, and the minimum privilege account is verified by the above-mentioned native probe. therefore it proves
Pipeline port closed loop, HTTP/MCP interface, persistent audit library or production account not yet proven
configuration. Historical connection/metadata discovery evidence see
[ Dialect boundary record ](yashan-dialect.md), historical normal Query cannot be used to reject the test
Proof of passage in lieu of a new path.

## Build, test and transcript

The script uses local `golang:1.26-bookworm`, read-only mounted local Go module cache,
`--network none` and `GOPROXY=off` perform builds and short runnable tests. Results of this round:

| order or check | result |
| --- | --- |
| Offline `go build ./...` | PASS, exit 0 |
| Offline `CGO_ENABLED=1 go build -tags yashan ./...` | PASS, exit 0 |
| Offline `go test -short ./internal/parser ./internal/engine ./internal/rules` | PASS, exit 0 |
| Offline `go test -short` Join `pipeline` and `businessdb` | **failed**: local cache is missing `github.com/moby/sys/sequential v0.7.0`; not downloaded due to network ban constraints |
| Temporary Go overlay directed `TestYashan*` | `pipeline` and `businessdb` PASS; only exclude irrelevant Testcontainers test files and cannot replace the complete package short test |
| YashanDB parser standard entrance | `yashan_parser_test.go` directional assertion PASS in the above parser package test |
| real database native driver probe | PASS, script exit 0; this probe does not depend on the missing Testcontainers test dependency |
| real database `TestYashanDiscoveryE2E` with tag `yashan` | PASS; use temporary overlay to exclude irrelevant Testcontainers test files, verify real executor connection/session SELECT/metadata/write and EXPLAIN boundaries |
| real database `TestYashanPipelineRealE2E` with tag `yashan` | PASS; real YashanDB table/executor, real analysis/rule/masking and test fixture audit port, covering SELECT, bloodline, R010, masking, and three audits |

A non-zero exit from the verification script indicates that the check performed failed; missing complete package test dependencies are clearly documented
is NOT RUN and does not pretend to pass. Directed tests are temporarily generated by scripts and overlays are not
Run this round of YashanDB tests while changing the repository test files. Script fixed overwrite writing
`C:\Users\Administrator\AppData\Local\Temp\agentsql-yashan-b71-verify.log`，
It only contains the fixed PASS/NOT RUN text when successful, and the fixed FAIL stage information is recorded when failed; it does not contain password or license content. real database probe
`YASHAN_PASSWORD` is passed in from the memory of the calling process, and neither the script nor the log will be echoed.

## Running paths and boundaries

This round does not package the driver or client from the server image, nor does it rebuild the current Linux/GHCR
Issues. The Go driver needs to be statically registered through `CGO_ENABLED=1 -tags yashan` at build time;
It cannot be dynamically registered after the fact in a ready-made binary without that build tag. The C client is
Dynamic loading at runtime: The user obtains an independent 23.4.7.100 client and sets it to `lib`
The directory is placed at `LD_LIBRARY_PATH` before the process is started. Fails to close when components are missing and does not fall back to
Legacy libraries for Oracle, PostgreSQL, or server mirrors. Client bundling in existing packaging scripts
The solution still requires separate release construction, architecture and dependency acceptance.

| Items that have not been tested or remain closed | current boundary |
| --- | --- |
| HTTP/MCP interface and persistence audit library | real database pipeline testing uses the authentication, strategy and audit ports of the test fixture; interface calls and persistence auditing are to be done separately E2E |
| Pipeline combination for producing minimum privilege accounts | The native driver read-only account probe and the pipeline real database test passed respectively; the latter uses `SYS`, and the combination has not been verified yet |
| R006 annotation probe | parser rejects comments before rules; does not count R006 real database hits |
| Sequence pseudo column `CURRVAL` | Read-only SQL validation of YashanDB executor explicitly rejected, directed offline regression coverage |
| R004 and R005 plan estimates | There is no verified YashanDB EXPLAIN decoder, and the plan probe is skipped; the upper limit on the number of execution rows and the R005 alarm after truncation remain |
| Multi-line paging, complex JOIN, functions, system directories, TLS, cancellation, connection reuse and more error codes | This round of target real database verification has not been done; the known `YAS-02213` only proves the rejection of this write |
| INSERT/UPDATE/DELETE/DDL, transaction, EXPLAIN | AgentSQL path still fails to close After |

The AgentSQL container was not started this round; therefore, there is no AgentSQL container to be stopped in this round.
The temporary user/table created by the native probe and the two tables created by the pipeline test have been deleted.
`yashan-v06` is reserved for subsequent operations.
real database pipeline verification; the temporary client library directory is only used as local testing material and does not belong to the repository or
Issues.

## Batch 71 Change Documents

- Matrix and home page: `README.md`, `README.en.md`, `SECURITY.md`,
  `CHANGELOG.md`、`website/public/index.html`。
- Documents: `docs/dm8-verification.md`, `docs/dm-oracle-dialect.md`,
  `docs/SPEC.md`、`docs/release-notes-v0.5.md`、`docs/DEPLOY.md`、
  `docs/USER_GUIDE.md`、`docs/RELEASE_ASSETS.md`、`docs/yashan-dialect.md`、
  `docs/yashan-verification-b71.md`。
- Detection and verification script: `scripts/yashan-verify.ps1`,
  `scripts/yashan-native-probe.go`。
- Minimum wiring and testing: `internal/parser/parser.go`,
  `internal/parser/yashan_parser_test.go`、`internal/engine/engine.go`、
  `internal/rules/generic.go`、`internal/pipeline/pipeline.go`、
  `internal/pipeline/rules.go`、`internal/pipeline/yashan_test.go`、
  `internal/pipeline/yashan_live_test.go`、
  `internal/authorizedexecute/internal/businessdb/yashan.go`、
  `internal/authorizedexecute/internal/businessdb/yashan_test.go`、
  `internal/authorizedexecute/internal/businessdb/sql_dialect.go`。

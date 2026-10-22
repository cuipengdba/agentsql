# Batch 70: HighGo HighGo 9.0 Enterprise Edition real database verification

**Language:** [English](highgo-verification.md) | [Simplified Chinese](../highgo-verification.md)

Verification date: 2026-10-08 (Asia/Shanghai). Complete terminal record: `C:\Users\Administrator\AppData\Local\Temp\highgo-batch70-transcript.txt`. No git commands were executed in this batch, and the matrix, README, SECURITY.md or `internal/` business code was not modified.

## Input and mirroring

| Project | Actual value |
| --- | --- |
| Official installation package | `hgdb-9.0.10.2-0-5936-59427d7-20260921.linux.x86_64.bin` |
| Installation package size, SHA-256 | 670,185,691 bytes；`4B13BFACC55D753D42D21613020691AB2E20B3E84D275338B80C268AEA5D4BD9` |
| license | `9147d1c2_A20261008N4540397q0lLU.dat`, 2,837 bytes; SHA-256 `3A58CA130FAD31E48CBAD24959362F3F966F5F988B4A63B98560879F309C4A4E`. Read-only mount, no content copied or output |
| base image | This machine has cached `rockylinux:8`, image ID prefix `9794037624aa`; Rocky Linux 8.9, glibc 2.28 in the container |
| Build image | `agentsql-highgo:9.0.10-b70`；image ID `sha256:338d2708c76d283b90b81c64a45e16f76c18f2aa0a5f1c154857b1fd927b5a41`；3,579,883,725 bytes |
| Authorization metadata | official `licchk -T`: `2027-04-08`; `-U`: `highgo`; `-P`: `5866` ; `-D`: `highgo`; `-H`: `HighGo数据库管理系统V9.0-企业版-9.0.10`; `--license-status`: `normal` |

The task background is written as 5432; the port given by the license and the installer of **is 5866**, so press 5866 to start and test this round. `SELECT version()` reports PostgreSQL 14.23 kernel; the server startup log shows the product version and authorization validity period of HighGo V9.0 Enterprise Edition 9.0.10. The authorization log calls it trial version and is valid until 2027-04-08.

## Installation package exploration and construction basis

The first byte of the installation package is `#!/bin/sh`, and the header indicates `Makeself 2.7.1`; `--info` returns the compression method gzip, internal entry `./luancher.sh`, and `--list` displays `hgdb_install.jar`, built-in JDK and entry script. After using `--tar xOf` read-only extraction on `luancher.sh`, the original help text lists `-defaults-file [path] -auto` as a completely silent installation, and the interactive error handling differences of `-options` and `-options-system`. This batch uses `-defaults-file /tmp/highgo-install.properties -auto` which is clearly listed by the manufacturer.

`LicenseSelectPanelAutomationHelper` in the installer JAR reads `LICENSE_FILE_PATH`, `CustomTargetPanelAutomationHelper` reads `INSTALL_PATH`, and `DatabaseInitPanelAutomationHelper` reads `DB_INIT_ENABLED`, `DB_MODE`. `DefaultVariableUtil` in the JAR lists the default value of the PG port as `5866`, and the authorization check tool `-P` also returns 5866 for this license. The built-in `pack-Scripts` script states `mkdir -p $INSTALL_PATH/license` and then runs `cp -p $LICENSE_FILE_PATH $INSTALL_PATH/license/license.dat`, thereby confirming that the final authorization path is `/opt/highgo/license/license.dat`.

In order to satisfy read-only mounting and not copy authorization files, Dockerfile pre-creates a zero-byte placeholder file in the final path, and BuildKit secret **is read-only mounted to the same path**. The installer verifies authorization according to its original process, and the copy operation cannot write bytes to the target mount; the build wrapper script then checks that the installation binary exists, that the target is still a mount point, and that there is only one place in the installation directory, `license.dat`. After the image is exported, use `stat` to confirm that the target file is **0 bytes**. When running, use the same path to access the original license through `readonly` bind mount. The installation package is only added to the Docker build context as a temporary hard link, and the link is removed after the build; the original file is read-only.

On Rocky Linux 8.9 cached on this machine, the installer comes with Java 17.0.9 executable, and `ldd` is glibc 2.28; the `postgres` link after installation checks that there is no missing library. The installer requires running as a normal user, so the build creates `highgo` UID 2000 and runs the silent installation as that user. The installer startup script will return 0 even if Java installation fails. This batch additionally checks the actual products of `initdb`, `pg_ctl`, `postgres`, and `psql` to avoid false positives. All Docker build commands use `--network none` and `--pull=false`, and no base images or dependencies are pulled.

Key actual output from the build:

```text
Loaded 5 override(s) from /tmp/highgo-install.properties
License validated successfully: /opt/highgo/license/license.dat
Installation started
Total packs = 7
[ Unpacking finished ]
Installation finished
PASS installer binaries and sole license mount verified
postgres (PostgreSQL) 14.23
sha256:338d2708c76d283b90b81c64a45e16f76c18f2aa0a5f1c154857b1fd927b5a41 3579883725
PASS: offline HighGo image built without license bytes in image
```

When trying to run it as root for the first time, the installer prints `run as non-root user please` and ends directly without `initdb`; the script fails due to lack of product. The first post-check installed by ordinary users failed because the streamlined base image did not have `find`; it was rebuilt successfully after using Bash glob instead. Neither of these two non-zero builds is considered a pass.

## real database output

Key lines in startup log:

```text
starting PostgreSQL 14.23 on x86_64-pc-linux-gnu
listening on IPv4 address "0.0.0.0", port 5866
This is an trial version of HighGo Database Management System V9.0-Enterprise Edition-9.0.10, valid until 2027-04-08.
database system is ready to accept connections
```

`psql` in the container uses the synthesis table `public.agentsql_batch70_verify`, and the following real output has been obtained:

```text
SELECT version() = PostgreSQL 14.23 on x86_64-pc-linux-gnu, compiled by gcc (GCC) 10.4.0, 64-bit
current_database() | current_user = highgo | highgo
CREATE TABLE
INSERT 0 2
SELECT = 1|Alice|13812345678 ; 2|Bob|13911112222
UPDATE 1
ORDER BY id LIMIT 1 OFFSET 1 = 2
quoted alias = MiXeD ; unquoted alias = mixed
DELETE 1
SELECT count(*) = 1
```

Read-only role `agentsql_b70_ro` is granted only to CONNECT, public schema USAGE, and composite table SELECT. `pg_hba.conf` The initial TCP rule is `scram-sha-256`; the verification script appends this round of Docker private network segment as SCRAM host rule and reloads it. The host port and AgentSQL API port are only released to `127.0.0.1`.

Actual output from AgentSQL:

```text
Datasource id=hg-b70 db_type=postgres
SELECT decision=allow MaskedCells=1 row=1|Alice2|138****5678
ProjectionLineages[0]=id <- public.agentsql_batch70_verify.id
ProjectionLineages[1]=name <- public.agentsql_batch70_verify.name
ProjectionLineages[2]=phone <- public.agentsql_batch70_verify.phone
R006 decision=deny rule_ids=R006
Audit total=2 decisions=deny,allow
SQL cleanup owned_exit=0 table_exit=0 role_exit=0
```

AgentSQL's initial health probe has two temporary errors of connection closure, and then returns to healthy in the same round, and finally exits 0 for loop closure and cleanup; see the transcript for the complete sequence.

The block comment rule probe is accepted by the parser and indeed enters R006 and is rejected, so the audit generated in this round is exactly one allow and one deny. **did not generate parse-error**, and the audit branch was not triggered this round; it cannot be claimed that parse-error has been measured based on this. Masking and rules are both handled by AgentSQL, `column_authorization.enabled=false`, and do not contain B2 column-level authorization conclusions.

## Acceptance and Boundaries

| Verification items | status/evidence |
| --- | --- |
| Official package detection, silent installation and offline mirroring | PASS; see above, Docker build transcript |
| license real database startup, version, expiration date | PASS; the server log is consistent with `licchk` metadata |
| Container client CRUD, paging, identifier case | PASS; see output above |
| Host pgx/v5, `$1`, read-only write-deny code | PASS;`pgx_user=agentsql_b70_ro bind_phone=13812345678 denied_write_sqlstate=42501`;TCP host rule uses SCRAM (final default script 20:46:28) |
| AgentSQL `postgres` Data source, masking, MaskedCells | PASS; current source code built AgentSQL container health, data source `db_type=postgres`; SELECT `decision=allow`, `MaskedCells=1`, synthetic mobile phone number `138****5678` (20:46:37) |
| ProjectionLineages, R006 and allow/deny auditing | PASS; three-column physical source accurate; block annotation query `decision=deny`, hit contains `R006`; audit filtered by Agent `total=2`, `deny,allow` (20:46:45). parse-error not triggered |
| Offline `go build ./...`, `go test -short` | Final default script: `go build ./...` PASS, exit 0 (20:39:19); `go test -short ./internal/parser ./internal/mask ./scripts/highgo/pgx-probe` PASS: parser 12.308s, mask 88.924s, probe package has no test file (20:45:22). Workspace `.gocache/mod` is missing five modules and exits non-zero on the first attempt. It passes after using the local full cache read-only mount instead. |

delivery version`verify.ps1`of**Finally, complete execution by default**Exited at 20:46:52**0**. It is passed offline in the same execution`go build ./...`, short test, database startup and SQL, host pgx, AgentSQL and audit assertions, and print`PASS: HighGo SQL, pgx, AgentSQL masking, lineage, R006 and audit checks`. This time no parameters for temporary reuse of the binary are used.

Not covered yet: TLS, connection pool failover, cancellation/timeout, `EXPLAIN`, extension types and OIDs, system catalog differences, B2 column-level authorization, parse-error auditing, cross-version and production workloads. Passing a synthetic table query does not mean passing these abilities.

When the script succeeds or fails, the current round of AgentSQL containers, temporary tables and roles will be removed, and the current round of HighGo containers and the data in the containers will be deleted;**Build image retention**For review. If an error occurs during SQL cleanup, deleting this round of containers will still delete the objects in the synthetic library. The script returns non-zero if the step fails; the license source file is checked with SHA-256 before and after execution. None`internal/`Minimal fix, list of changed files is empty.

The first AgentSQL deployment attempt exited because the verification script generated a 48-character `AGENTSQL_SECRET` after the database, host pgx, offline Go build, and short test passed; AgentSQL requires exactly 32 bytes. This script exited with a non-zero value and was not recorded as a closed loop pass with `owned_exit=0 table_exit=0 role_exit=0`. The script has been changed to use a 32-character hex key of 16 random bytes. Then reuse the same batch of build products to complete a quick retest, and then use the default script of the delivery version to re-execute the full build, short test and closed loop; the latter exits 0. The temporary reuse product has now been deleted.

The rerun entry point is `scripts/highgo/run.ps1`, which first executes `build.ps1`, and then executes `verify.ps1` after the build exits 0; when reusing the built image alone, run `verify.ps1` directly. Both are appended to the above fixed transcript. After the check, the `highgo-b70-*` temporary container, network, volume, and one-time cache binary were removed, leaving only the `agentsql-highgo:9.0.10-b70` image.

## The user has decided to turn green (2026-10-08)

The user has decided to mark **HighGo 9.0.10 Enterprise Edition, PG mode, single-instance synthetic table range** as **🟢 The specified instance protection link passed**: parsing/column lineage, table-level authorization, controlled read-only SELECT, mobile phone number masking, R006 rejection and allow/deny The audit links all have the above genuine database evidence. TLS, connection pool failover, cancellation/timeout, EXPLAIN, extension type/OID, system directory difference, B2 column-level authorization, parse-error audit branch (not triggered in this round), cross-version and production load untested; green does not mean that these capabilities have been passed, nor is it manufacturer certification or production support commitment. Batch 70 only collects evidence, and Batch 73 synchronizes the matrix, README and SECURITY.md based on user decisions.

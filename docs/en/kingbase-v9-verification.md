# KingbaseES V9 real database verification: batch 72 (writable license volume)

**Language:** [English](kingbase-v9-verification.md) | [Simplified Chinese](../kingbase-v9-verification.md)

Verification date: 2026-10-08 (Asia/Shanghai). One-click script: [kingbase-v9-verify.ps1](../../scripts/kingbase-v9-verify.ps1). The complete terminal transcript of the final run: `C:\Users\Administrator\AppData\Local\Temp\kingbase-v9-batch72-transcript.txt` (7,877 bytes, 14 timestamped PASS, 0 FAIL, final exit code **0**). Early debugging failures have been covered by the final transcript, and their key failure conclusions are listed separately below. Batch 72 does not change the matrix, README, SECURITY.md or `internal/`; batch 74 is based on 2026-10-09. The user decides the synchronization matrix gear.

## Conclusion and Matrix Positions

**V9 passed the basic protection closed loop of `pg` mode this time.** can start the database normally under the writable license named volume; the log is `starting KingbaseES V009R001C010`, `SELECT version()` is the same, there is no floating mode write permission FATAL or product code FATAL. The V009R001C product code of the new license is accepted by the server. This is a compatibility conclusion derived from the successful startup, and is not a transcription of the license file content.

User **has made the decision to turn green on 2026-10-09: only V9R1C10, PG compatibility mode, single instance synthesis table, and read-only PG protocol basic closed loop of controlled links are marked 🟢.** writable license volume startup, ksql CRUD/paging/identifier, host pgx/v5 `$1` binding + SCRAM, read-only role write denial `42501`, AgentSQL controlled SELECT allow + mobile phone number masking + column lineage + R006 comment denial + allow/deny audit all correspond to the following real database evidence. This level does not extend to V9 full mode, V8, long-term licensing, vendor certification, or production support commitments; untested capabilities remain fail-closed.

## Option B: Image, license and container

| Project | The actual test of batch seventy-two |
| --- | --- |
| mirror tar | `D:\ruanjiansheji\db-images\jincang\KingbaseES_V009R001C010B0004_x86_64_Docker.tar`, this round of reuse has loaded the image on this machine |
| tag / image ID | `kingbase_v009r001c010b0004_single_x86:v1` / `sha256:0bce318e74adca7a3d619b55b336269017507fd679833b7ce5d8400289661724`; same as batch 64 and 69 |
| Mirror entrance and port | `/home/kingbase/docker-entrypoint.sh`, user `kingbase`, `54321/tcp` |
| Mirror license path | The original `/home/kingbase/install/kingbase/bin/license.dat` is moved to `/home/kingbase/install/kingbase/etc/license.dat` and a soft link is created when the manufacturer's entrance is run; this script is started according to the verified `initdb`/`sys_ctl` command and soft links the bin path to `/license/license.dat` |
| new license | `license_4_V009R001C-企业版-180天.dat`，5,036 bytes，SHA-256 `03793D90E5C23CD2F4303019D1EE6888D13D4679739923D5FDFCDBCDA648B1E9` |
| writable volume | `kingbase_v9_license_b72`, `--mount type=volume,source=kingbase_v9_license_b72,target=/license`; **is not bind mount** |
| Copy | After the disposable container `kingbase-v9-license-copy-b72` is attached to the volume, use `docker cp` to copy it from the host to `/license/license.dat`; the volume is set to `kingbase:kingbase` and `0600`; the size and SHA-256 are consistent with the source file, and the `kingbase` user's `test -w` passes; the copied container is deleted immediately |
| Startup and Networking | V9 is initialized with `-m pg`, and the database port is only published on the `127.0.0.1` temporary port; container inspect confirms that `/license` is `volume` and `RW=true` |
| Server licensing metadata | `get_license_validdays()`Return`180`；`get_license_info()`extracted from`2025-11-24`, but the field meaning**Not confirmed**, shall not be regarded as an expiration date. The server startup log does not give a confirmable expiration date;**Actual expiration date not verified**. The script only outputs the date and number of days, not the original function text or license content. |

The original license file is only read during the local verification process. The source file SHA-256 is recalculated at the end of the script and is still the hash in the table; there is no `docker commit`, building a mirror, or copying the license to the repository. The V9 data directory is only in the current round of containers. The container, temporary roles/tables, AgentSQL containers and data volumes, and license named volumes are eventually deleted. After the script is completed, check again from Docker and find that none of these current round objects exist. The offline Go build volume, cache volume and private network of this round are reserved for re-running; the V8/V9 containers and their volumes in batch 64 are not operated by this script.

## Compare with previous two failures

| batch | license access | Actual measurement results |
| --- | --- | --- |
| batch sixty-four | A writable copy of the old license | `productVersion check failed. server is 'V009R001B' but license is 'V009'`; not started |
| batch sixty-nine | New V009R001C license, read-only bind mount | `FATAL: License file should have write access mode in floating mode`; The write permission check failed first and the product code verification was not carried out. |
| batch seventy-two | A dedicated writable named volume for the new license | `kingbase` is writable; V9 starts and completes subsequent SQL, PG protocol, and AgentSQL closed loop; the above FATAL does not appear |

## Actual operating evidence

The following excerpts are from the fixed path transcript of the final run; the full output and timestamps of each step are available in that file.

```text
[2026-10-08T23:56:08+08:00] PASS: named volume=kingbase_v9_license_b72 target=/license copied_bytes/mode/owner=5036|600|kingbase:kingbase sha256=03793D90E5C23CD2F4303019D1EE6888D13D4679739923D5FDFCDBCDA648B1E9; kingbase user can write
[2026-10-08T23:57:01+08:00] PASS: database ready and license accepted by server startup
2026-10-08 15:56:59.257 UTC [151] LOG:  starting KingbaseES V009R001C010
[2026-10-08T23:57:01+08:00] PASS: server license metadata valid_days=180 reported_dates=2025-11-24 (date role unconfirmed; license text suppressed)
[2026-10-08T23:57:04+08:00] PASS: identifier comparison quoted=mixed unquoted=mixed
[2026-10-08T23:57:04+08:00] PASS: ksql version, PG mode, CRUD, LIMIT/OFFSET and identifier probes
host_pgx_user=agentsql_ro mode=pg phone=13812345678 denied_write=ERROR: permission denied for table agentsql_batch72_verify (SQLSTATE 42501) version=KingbaseES V009R001C010
[2026-10-08T23:57:13+08:00] PASS: host pgx/v5 $1 bind, SCRAM and readonly SQLSTATE 42501
[2026-10-08T23:58:50+08:00] PASS: offline go build ./...
[2026-10-08T23:59:18+08:00] SELECT decision=allow audit_id=1 masked_cells=1 row=1|Alice|138****5678
[2026-10-08T23:59:25+08:00] PASS: ProjectionLineages physical origins match the synthetic V9 table
[2026-10-08T23:59:25+08:00] R006 decision=deny audit_id=2 rule_ids=R006
[2026-10-08T23:59:25+08:00] Audit total=2 decisions=deny,allow
[2026-10-08T23:59:25+08:00] PASS: V9 PG protocol, readonly account, AgentSQL query, scoped masking, R006 and audit
[2026-10-08T23:59:30+08:00] PASS: temporary containers, synthetic role/table and license named volume removed
```

`ksql` actually executed `SELECT version()`, `SHOW database_mode` (`pg`), table creation, insertion, query, update (`UPDATE 1`), deletion (`DELETE 1` followed by `count(*)=0`), and `LIMIT 1 OFFSET 0`. The output header for both quoted `"MiXeD"` and unquoted `MiXeD` is `mixed`; therefore it is not claimed to be identical to PostgreSQL's identifier case behavior. The host pgx/v5 query uses the `$1` binding; the HBA's TCP entry is `scram-sha-256`, and the read-only role's write reject error code is `42501`.

This round of `pg` mode does not accept explicit `GRANT CREATE SESSION TO agentsql_ro`: independent exploration runs on `2026-10-08T23:33:46+08:00` and reports `syntax error at or near "SESSION"`. This time the script exits non-zero and cleans up. The final script uses the tested and available `CREATE ROLE ... LOGIN`, `GRANT CONNECT`, schema `USAGE` and single table `SELECT`; SCRAM login and query are successful, but UPDATE is rejected. **The explicit CREATE SESSION syntax failed, and the functional ability to create a session has been verified.**

AgentSQL uses the repository's existing `db_type=postgres` to connect to V9 in the least privilege role. There is no new `kingbase` type or modification to `internal/` in this round. Ordinary query `decision=allow`, `MaskedCells=1`, mobile phone number `138****5678`; the three columns of `ProjectionLineages` in the same SQL point to `public.agentsql_batch72_verify.id/name/phone` respectively; the annotation rule probe `decision=deny` and hits contains `R006`; there are exactly two audit items, namely allow and deny, without parse-error. Comments are accepted by the parser and this entry is not unvalidated.

## Offline build, test and cleanup

The final script is in the cached`golang:1.26-bookworm`container, use`--network none`、`GOPROXY=off`、`GOTOOLCHAIN=local`、`CGO_ENABLED=1`execute`go build ./...`(23:58:50 PASS), and build the AgentSQL executable file (23:59:11 PASS); dependencies are not downloaded. In a previous complete closed-loop operation, the same offline parameters were independently executed.`go test -short ./internal/parser ./internal/mask`, the output is`parser 12.133s`、`mask 89.797s`, and output PASS at 23:29:58. A subsequent short test was repeated for about 12 minutes but still yielded no results. The test container was stopped instead of exiting with zero; this time the script was cleaned up according to the failed path.**Do not count as test passed**. The final one-click script removes repeated short tests; there are no changes in this batch`internal/`, so there are no change packages that require regression testing. The final running evidence is based on the fixed path transcript, and the independent short test results come from the actual terminal output earlier in this round.

## untested boundary

| range | Status |
| --- | --- |
| Multi-row data and deep paging, JOIN, aggregate and general functions, complex expressions, type OIDs, system catalogs, `EXPLAIN` | Not tested; this round only has a single line of SQL and a `LIMIT/OFFSET` probe |
| Column-level authorization B2, multi-role concurrency, long connection/timeout cancellation, production load | Not tested |
| TLS/encrypted transmission, HA/failover, backup and recovery | Not tested |
| V9 Oracle mode, native Kingbase driver, V8 and other cross-version differences | Not tested; this round is clearly about connecting `pg` mode to the existing PostgreSQL protocol |
| Actual license expiration date and subsequent renewal behavior | Unconfirmed; only 180 days and an unspecified date reported by the server |

## Change list

- `scripts/kingbase-v9-verify.ps1`: Reuse the loaded image; change the read-only bind mount to a disposable container `docker cp` to a writable named volume, and verify the size, SHA-256, owner/permissions, write capabilities and container mount RW; include volume deletion in success and failure cleanup; add a server probe that only outputs license metadata; change the batch name to 72; the identifier comparison is recorded according to the real result.
- `docs/kingbase-v9-verification.md`: Updated to this time’s real database evidence, comparisons of the first two batches, boundaries and gear recommendations.
- `internal/`: **has no file changes and** has no minimum service wiring repairs.

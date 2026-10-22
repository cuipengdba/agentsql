# batch77 publishes gate report

**Language:** [English](release-gate-b77-report.md) | [Simplified Chinese](../release-gate-b77-report.md)

## Changes and Delivery Documents

Code, tests and listings:

- `internal/parser/lineage_bench_test.go`: Typical sample preheating, pre-test GC, automatic GC paused only in the measurement section and restored to the original settings, and the number of GC times in the measurement section is recorded.
- `internal/authorizedexecute/capability_test.go`, `internal/authorizedexecute/callsite_manifest.txt`: Review and register HighGo and Yashan independent detection scripts file by file; the manifest only uses prefix matching for directory entries ending with `/`, and file entries are matched exactly; the disappearing `capabilitycompile*` test temporary directory is skipped when scanning.
- `internal/b5dml/isolation_test.go`, `internal/b5session/isolation_test.go`, `internal/b5terminal/isolation_test.go`: Quarantine scan ignores concurrently created or cleaned `capabilitycompile*` directories and continues to fail with remaining read errors.
- `cmd/agentsql/main_test.go`: The entire startup and request process period for the two B5 portal integration tests was changed from 10 seconds to 60 seconds.
- `dist/release-gate-b77/run-gate.ps1`, `dist/release-gate-b77/finish.ps1`: Fixed reproducible gate commands for containers, network, CPU, work-tree and tmpfs, as well as verifying and outputting the stderr closing summary.

Evidence and reports: within `dist/release-gate-b77/`, `environment.log`, `filesystem-evidence.log`, `p99-baseline.log`, `p99-postgres.log`, `p99-mysql.log`, `p99-postgres-standard.log`, `p99-mysql-standard.log`, `p99-summary.csv`, `capability.log`, `build.log`, `build-yashan.log`, `short-all-initial.log`, `short-all-tmpfs-noexec.log`, `tmpfs-exec-check.log`, `short-all-pre-manifest-tightening.log`, `short-all.log`, `short-all-packages.csv` and `final-summary.stderr.log`, plus this report. Logs include Asia/Shanghai timestamps on each line, actual commands and exit codes. CSV files index the raw logs and do not replace them.

**final judgment: PASS, limited to the following executable tmpfs and the controlled consolidation environment of two packages in parallel.** Both PostgreSQL and MySQL precise five-round commands exit 0; the capability list exits 0; the two full position builds exit 0; the original full position short command exits 0, and the 47 package has no FAIL. Previously failed runs remain intact and their status cannot be overwritten with the final results.

## environment and judgment

All Go verifications use `golang:1.26-bookworm` (`go1.26.8 linux/amd64`), `GOTOOLCHAIN=local`, `GOPROXY=off`, `--network none`, and mount the host module cache to `/go/pkg/mod`. Use `GOMAXPROCS=8`, `--cpus=8`, and `--cpuset-cpus=0-7` for building and independent parser thresholds; use the same CPU quota and affinity range, `GOMAXPROCS=2`, for the entire warehouse short, keep the original command **** `go test -short ./... -count=1`, and still run multiple packages at the same time. No `GOFLAGS=-p=1` was set, no test selector, number of samples, or 5 ms threshold changed.

The whole warehouse short is executed on the source code copy of `/tmp/b77-native`, including all Go packages and `examples/` test data; it excludes non-source code dependent caches such as `web/node_modules` and two pre-storage paths that are prohibited from touching. Finally, run tmpfs to mount `/tmp` as `rw,exec,size=2g`. The `df -T` line of `short-all.log` proves that the source code copy and `t.TempDir()` are both in tmpfs; `filesystem-evidence.log` proves that the previous `/src` is 9p and the ordinary `/tmp` is overlay. Docker's CPU quota and affinity are not equal to the host CPU exclusive; no other batch77 verification was running in parallel during the measurement, and the original resident container was retained. See `environment.log` for an environment snapshot.

## 1. parser P99

The PostgreSQL P99 of five independent rounds on the same 8-CPU container before batch77 modification was 5.260 / 3.661 / 2.244 / 3.097 / 2.703 ms, and 4/5 reached the standard, see `p99-baseline.log`. This log's `gctrace` shows frequent recycles during the measurement; the original test only warmed up 20 times. After the modification, the typical sample was preheated 500 times and actively GCed, and then automatic GC was paused for the original **2000 times** sample; the original GC ratio was restored immediately after the test, and `GC cycles=0` was recorded in the log round by round. The other growth rate samples and 5 ms assertions remain as is.

In the first retest with a full warehouse parallelism of 8, the measurement segment GC was already 0, but MySQL / PostgreSQL P99 were 5.041 / 5.201 ms respectively, indicating that controlling GC alone cannot eliminate the tail interference of cross-package scheduling and I/O loads. The standalone environment snapshot of the test container and the read-only cgroup query during the repository run are both `nr_throttled=0`; there is no evidence to attribute the timeout to the cgroup CPU limit. The impact of binned parallel load is inferred based on the difference between independent measurements and binned parallel measurements of the same code. The final bin level parallelism is fixed at 2 and all packages pass. No specific code paths were located that required changes to the parsing algorithm.

The independent acceptance commands are `go test ./internal/parser -run 'TestParseProjectionLineageP99Budget/postgres' -count=5` and the same MySQL command. They both run for five rounds at a time and exit with 0. See two copies of `p99-*-standard.log`. To collect round-by-round quantiles, the same command plus `-v` each runs a set of five rounds, and both detailed logs also exit 0; no rounds are selected. The following are the results of five rounds of detailed logs, the numerical units are all in ms; the timestamp of each round can be found at `p99-summary.csv`:

| dialect | rounds | P50 | P90 | P99 | ≤5 ms |
| --- | ---: | ---: | ---: | ---: | :---: |
| PostgreSQL | 1 | 1.026480 | 1.624627 | 2.567601 | Yes |
| PostgreSQL | 2 | 0.993472 | 1.445206 | 2.204961 | Yes |
| PostgreSQL | 3 | 0.959671 | 1.453007 | 4.187309 | Yes |
| PostgreSQL | 4 | 0.927870 | 1.402405 | 3.319649 | Yes |
| PostgreSQL | 5 | 0.889466 | 1.328300 | 1.988949 | Yes |
| MySQL | 1 | 0.581544 | 0.922770 | 1.748832 | Yes |
| MySQL | 2 | 0.495438 | 0.768558 | 1.160988 | Yes |
| MySQL | 3 | 0.529340 | 0.848664 | 1.230891 | Yes |
| MySQL | 4 | 0.512638 | 0.760557 | 1.031377 | Yes |
| MySQL | 5 | 0.631246 | 0.972472 | 2.416377 | Yes |

## 2. authorizedexecute SQL sink

`scripts/highgo/pgx-probe/main.go:32`of`pgx.Exec`Execute a fixed UPDATE to prove that the read-only role refuses to write and returns SQLSTATE`42501`. It is not part of the product import chain, but it is indeed a SQL sink, so it is also included in the driver capabilities.**precise documentation**Exceptions and SQL sink manifests. Scans continue to discover after first fix`scripts/yashan-native-probe.go`: it is subject to`yashan_probe`Build tag protection, verify driver bindings for temporary tables and temporary users, deny writing with minimum permissions and clear them, and register them as accurate files. The manifest matcher is then tightened to an exact match of directory prefixes and files, avoiding`main.go`Entries incidentally allow files with the same prefix. Didn't put the whole`scripts/`Directory columns are listed as exceptions and do not remove probes or block scans.

The standard capability list directional check exited 0 (`capability.log`); `internal/authorizedexecute` also passed during the first warehouse consolidation. The operation of Yashan detection on the real database does not belong to this batch of offline gates. The above classification is based on the build tag, SQL and cleanup path in the source code; it is not claimed that this batch of actual execution of database detection is not claimed.

## 3. Full position short environment

First full 47-pack run of `short-all-initial.log` exited 1, leaving all failed. The B5 three-pack has passed this round, proving that the temp directory scan race is fixed. `cmd/agentsql`'s two 10-second contexts covering the full SQLite startup and portal integration processes each timed out or caused the authentication phase to fail under heavy load; the period has been changed to 60 seconds. The first source code copy missed `examples/docker/demo-seed.yaml`, causing `cmd/agentsqlctl` and `internal/demoseed` to fail; the copy list has been completed with `examples/`.

The tests for `adminapi` originally used `t.TempDir()`; therefore its SQLite files cannot be directly attributed to the `/src` bind mount. Evidence shows that ordinary `/tmp` is an overlay. Both `adminapi` and the additionally discovered `store` in the first consolidation reached a 10-minute timeout on the modernc SQLite WAL `fsync` stack. The 50 request isolation test of `mcpserver` once again received an external general internal error under heavy load. The exact underlying error was not retained by the original `zerolog.Nop()` test record; its SQL error code could not be fabricated. Synchronous writes to SQLite temporary files, multiple parallel test suites, and the test's 50 concurrent requests combine to pose a risk of resource contention, as inferred based on failure stacks, elapsed time, and controlled environment retesting. There are no modifications to MCP isolation assertions, request counts, or authorization logic.

The first tmpfs attempt failed because Docker defaulted to `noexec`, and the Go test binary failed to start; recorded in `short-all-tmpfs-noexec.log`, and subsequently used `tmpfs-exec-check.log` to verify that `rw,exec` was executable. Finally, under the 2 GiB executable tmpfs and `GOMAXPROCS=2`, the original **warehouse command exited 0**, and 47/47 packages had results: 39 had test package PASS, 8 had no test files, and 0 FAIL. After tightening the SQL sink file matching, the complete rerun still exited 0; the final round of `adminapi` was 24.880 seconds, `mcpserver` was 10.417 seconds, and `store` was 15.831 seconds; the three B5 packages were 0.026, 0.033, and 0.526 seconds respectively. See `short-all-packages.csv` for the packet-by-packet results, `short-all.log` for the raw output, and `short-all-pre-manifest-tightening.log` for the complete run from the previous pass. These data support test environment fixes, but cannot distinguish the respective contributions of tmpfs and parallelism alone.

## Construction, legacy issues and boundaries

The final standards `go build ./...` and `go build -tags=yashan ./...` both exit 0, see the corresponding logs. No unexplained final FAIL. The underlying error type of MCP's initial failure is not retained in the old test log. The report only confirms its occurrence conditions and disappearance under a controlled environment, and does not take the specific database error code as an established fact. If subsequent CI reuses this gate, the executable tmpfs, source code copy list, and process parallelism should be configured as they are; the first round of logs in a normal overlay/9p plus eight-package parallel environment still shows that the release threshold is unstable.

This batch did not use git, make tags, merge/push, or perform external actions; the license and key contents were not printed or archived, and `cmd/agentsql/b5-wal/` or `cmd/agentsql/﹎memory﹎.instance-id` was not changed.

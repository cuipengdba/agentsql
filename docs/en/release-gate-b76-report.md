# v0.5.0 release gate: batch76 network dependency supplement and full verification

**Language:** [English](release-gate-b76-report.md) | [Simplified Chinese](../release-gate-b76-report.md)

Verification date: 2026-10-09 (Asia/Shanghai); Baseline submission: `81c40aa`. **General judgment: FAIL, does not meet the requirement of Gate B for this batch of releases.** Both full warehouse builds passed, and the dependency and license verification passed; three of the five rounds of parser P99 took more than 5 ms, and the full warehouse short tests had 7 failed packages. There are no tag, merge, push, release, image push, official website deployment or PVR operations in this batch.

## environment and evidence

All Go containers use `golang:1.26-bookworm` (actual `go1.26.8 linux/amd64`), `GOTOOLCHAIN=local`, and `GOMAXPROCS=8`, and mount the writable host cache `C:\Users\Administrator\go\pkg\mod:/go/pkg/mod` and the writable source code `${PWD}:/src`. Use `GOPROXY=https://goproxy.cn,direct` and bridge network for dependency download; use `--network none` for build and test. The test container is additionally set up with `GOCACHE=/src/.gocache` to reuse the compilation cache and does not change the test command. For actual mounting measurements, see [mount-check.log](../../dist/release-gate-b76/mount-check.log). All log lines include local timestamps, commands, and exit codes.

Environment re-inspection decompresses the tracked files of `git archive HEAD` to the container's native writable directory `/tmp/b76-native`, overwrites `go.sum` in the download stage at that time, and then runs a single package test. The required host cache and source code mounting are still retained; the source code snapshot creation and cleanup records can be found in [native-workspace.log](../../dist/release-gate-b76/native-workspace.log). This also avoids two untracked paths that users have asked not to touch.

| gate | Conclusion | main evidence |
| --- | --- | --- |
| `go mod download -json all` | **PASS** | [download.log](../../dist/release-gate-b76/download.log) Exit 0; [dependency-summary.log](../../dist/release-gate-b76/dependency-summary.log) and [ New module version list ](../../dist/release-gate-b76/modules-added.txt) |
| License compliance | **PASS** | [license-audit.log](../../dist/release-gate-b76/license-audit.log), [ source code file header scan ](../../dist/release-gate-b76/source-license-headers.log) |
| `go build ./...` | **PASS** | [ first build.log](../../dist/release-gate-b76/build.log) and [ final build-final.log](../../dist/release-gate-b76/build-final.log) both exited 0 |
| `go build -tags=yashan ./...` | **PASS** | [ first build-yashan.log](../../dist/release-gate-b76/build-yashan.log) and [ final build-yashan-final.log](../../dist/release-gate-b76/build-yashan-final.log) both exited 0 |
| postgres parser P99 five consecutive rounds | **FAIL** | [p99.log](../../dist/release-gate-b76/p99.log) exit 1; [ five rounds of data CSV](../../dist/release-gate-b76/p99-summary.csv) |
| `go test -short ./... -count=1` | **FAIL** | [short-all.log](../../dist/release-gate-b76/short-all.log) exit 1; [ package by package CSV](../../dist/release-gate-b76/short-all-packages.csv) |
| rules/pipeline directed short tests | **PASS** | [short-targeted.log](../../dist/release-gate-b76/short-targeted.log) Two packs `ok`, exit 0 |
| Final `go.sum` full warehouse test compilation check | **PASS (compile only)** | [test-compile-final.log](../../dist/release-gate-b76/test-compile-final.log) 47/47 package has results, exit 0; not as short tests PASS |

## dependency changes

Before and after downloading the complete module diagram, the real`.zip`Files increased from 306 to 734, new**428 module versions**;Among them are`github.com/moby/sys/sequential v0.7.0`。[modules-added.txt](../../dist/release-gate-b76/modules-added.txt)Itemize paths and versions,[Before downloading](../../dist/release-gate-b76/module-zips-before.txt)and[After downloading](../../dist/release-gate-b76/module-zips-after.txt)Keep the original cached snapshot. 428 here is the execution`go mod download -json all`The number of cached versions of the entire module graph added later is not equivalent to the 428 missing items in the build alone.

The `go.mod` hash is unchanged. Downloading the full module diagram caused `go.sum` to temporarily add 878 lines and delete 125 lines; for the complete temporary differences, see [go-mod-sum.diff](../../dist/release-gate-b76/go-mod-sum.diff). After downloading [, the hash ](../../dist/release-gate-b76/go-hashes-after-download.txt) has been archived. The original `go.sum` already contains the checksum of `sequential v0.7.0`, so it will be temporarily rearranged and restored; the final hash of [ ](../../dist/release-gate-b76/go-hashes-final.txt) is exactly the same as at the beginning of the batch, and there is no Git difference between `go.mod`/`go.sum` in the end. After recovery, the two offline full warehouse builds and 47 package test compilation checks all exited 0, indicating that cache completion does not require persistent modification of the module list or checksum. The business logic code is not modified.

## License verification

The root directory `LICENSE` contains the Apache License 2.0 Sections 1–9, End of Terms paragraph, and appendices. Compared with [Apache official text ](https://www.apache.org/licenses/LICENSE-2.0.txt), the original file has different bytes due to different line breaks and formatting; after normalizing the blanks, **10,221 bytes are exactly the same as**, which can confirm that the full text content is complete. [ Verification log ](../../dist/release-gate-b76/license-audit.log) only records hashes, structures and comparison conclusions, and does not output license content.

`NOTICE` and `COMMERCIAL-LICENSE.md` both specify Apache-2.0, independent commercial authorization and 2026 Cui Peng copyright, and the trademark retention standards are consistent. The AGPL search for the tracked `.md`, `.go` files only hits the license migration record for `CHANGELOG.md`, the v0.4/v0.2 history, and the dated batch75 old report; there are no hits describing AGPLv3 as the current license for v0.5.0. The old license paragraph of `docs/release-notes-v0.2.0.md` has subsequent version switching instructions; the "pending decision" status reported by batch75 is a historical snapshot and cannot be reused as the current release copy. Scanning the first 15 lines of 638 tracked Go files, there is no old license file header banner that requires this batch of unification.

## parser P99 fifth wheel

Standard command: `go test ./internal/parser -run '^TestParseProjectionLineageP99Budget$/^postgres$' -count=5 -v`. This command exclusively occupies the verification load of this batch; the original service container of the host has not been shut down, so it cannot claim that the entire host has zero load. Five rounds are from a real run, with no extra runs added to achieve a passing result.

| rounds | P50 | P90 | P99 | ≤ 5 ms |
| ---: | ---: | ---: | ---: | :---: |
| 1 | 1.033803 ms | 2.061843 ms | **6.218622 ms** | No |
| 2 | 853.486 µs | 1.730673 ms | 2.493049 ms | Yes |
| 3 | 1.037639 ms | 2.107479 ms | **5.367101 ms** | No |
| 4 | 918.835 µs | 1.781067 ms | 2.879308 ms | Yes |
| 5 | 1.070586 ms | 2.228379 ms | **7.893833 ms** | No |

Conclusion: **passed the line in 2/5 rounds, and the threshold was FAIL** in five consecutive rounds. These data indicate tail latency jitter; the cause has not been proven, and overruns cannot be attributed to bloodline semantics or arbitrary erasure of failed rounds. The full position short test also measured mysql P99 6.65963 ms and postgres P99 12.825661 ms under parallel load, see its original log; this does not replace the above-mentioned exclusive five-round conclusion.

## Full position short tests: 47 packages of real results

The command exits 1. 47/47 packages all have Go original results: **32 test packages PASS, 7 test packages FAIL, 8 packages without test files**; test package pass rate **32/39 = 82.05%**, if no test file packages are counted as command success status, it is **40/47 = 85.11%**. A test file without a test file is not called a test PASS. The following is the packet-by-package status of the original command. Single-package re-inspection will not overwrite these results.

| package | Original command status | Duration/Description |
| --- | --- | --- |
| `cmd/agentsql` | PASS | 101.265s |
| `cmd/agentsql-b2-fallback` | No testing | no test files |
| `cmd/agentsql-b2-gate` | No testing | no test files |
| `cmd/agentsqlctl` | PASS | 214.241s |
| `internal/adminapi` | **FAIL** | 600.156s |
| `internal/audit` | PASS | 0.424s |
| `internal/auditchain` | PASS | 0.015s |
| `internal/auditrelay` | PASS | 0.034s |
| `internal/auth` | PASS | 3.746s |
| `internal/authorizedexecute` | **FAIL** | 127.690s |
| `internal/authorizedexecute/internal/businessdb` | PASS | 1.816s |
| `internal/b2release` | PASS | 0.029s |
| `internal/b5` | PASS | 0.016s |
| `internal/b5coordinator` | PASS | 0.942s |
| `internal/b5dml` | **FAIL** | 170.036s |
| `internal/b5session` | **FAIL** | 168.487s |
| `internal/b5terminal` | **FAIL** | 263.589s |
| `internal/b5wal` | PASS | 10.354s |
| `internal/bootstrap` | PASS | 166.588s |
| `internal/columnauth` | PASS | 0.016s |
| `internal/compliance` | PASS | 0.023s |
| `internal/config` | PASS | 1.330s |
| `internal/controlledread` | PASS | 0.145s |
| `internal/demoseed` | PASS | 23.714s |
| `internal/discovery` | PASS | 0.018s |
| `internal/engine` | PASS | 0.012s |
| `internal/eventbus` | PASS | 0.016s |
| `internal/lockrank` | PASS | 0.012s |
| `internal/mask` | PASS | 183.201s |
| `internal/mcpserver` | **FAIL** | 323.692s |
| `internal/metrics` | PASS | 0.031s |
| `internal/model` | PASS | 0.012s |
| `internal/notify` | PASS | 0.221s |
| `internal/parser` | **FAIL** | 9.159s |
| `internal/pipeline` | PASS | 0.669s |
| `internal/policy` | PASS | 0.023s |
| `internal/rbac` | PASS | 10.470s |
| `internal/redaction` | PASS | 0.009s |
| `internal/rules` | PASS | 0.063s |
| `internal/server` | No testing | no test files |
| `internal/store` | PASS | 556.314s |
| `internal/version` | No testing | no test files |
| `internal/webui` | PASS | 0.014s |
| `scripts` | No testing | no test files |
| `scripts/highgo` | No testing | no test files |
| `scripts/highgo/pgx-probe` | No testing | no test files |
| `scripts/releasesign` | No testing | no test files |

### Qualification of failed packets and re-examination of normal disks

| Original full position failure package | original failure | Single package/serial re-inspection | Qualitative |
| --- | --- | --- | --- |
| `internal/adminapi` | Go defaults to 10 minute timeout; stack stops at modernc SQLite WAL `fsync` | [ native disk single package ](../../dist/release-gate-b76/native-adminapi.log) **PASS**, 318.568s | The disk environment under bind mount/full position concurrency is sensitive; the original full position is still FAIL |
| `internal/authorizedexecute` | Two competency checklist checks failed | [ native disk single package ](../../dist/release-gate-b76/native-authorizedexecute.log) **FAIL**, the same two items | Tracked `scripts/highgo/pgx-probe/main.go` imported `pgx/v5`, line 32 `Exec` is not included in the audited SQL sink list; subsequent batch processing is required |
| `internal/b5dml`、`internal/b5session`、`internal/b5terminal` | All three isolation checks reported `/src/capabilitycompile...` and the temporary directory does not exist. | [ native disk `-p 1` serial retest ](../../dist/release-gate-b76/native-b5-serial.log) **three guarantees PASS** | Competition occurs in the shared temporary directory during full warehouse parallelization; the original full warehouse is still FAIL |
| `internal/mcpserver` | 50 synchronous request isolation assertion occurred `error`, expected `allow` | [ native disk single package ](../../dist/release-gate-b76/native-mcpserver.log) **PASS**, 153.062s | Unrecurred environment/timing-sensitive failure under full position parallelism; the root cause has not been confirmed, and the original full position is still FAIL |
| `internal/parser` | The P99 of mysql and postgres are 6.65963 and 12.825661 ms respectively. | Exclusive postgres five rounds as shown in the table above, still **FAIL** | Performance threshold not reached |

## legacy and borders

1. **release blocking**: exclusive parser P99 did not reach five consecutive rounds ≤5 ms; standard full position short tests still have 7 original FAIL packets. `authorizedexecute`'s ability/SQL sink manifest failure is reproducible reliably on healthy disks.
2. **environment/test concurrency issues**: The single package or serial normal disk re-examination of `adminapi`, three b5 packages and `mcpserver` passed; the standard full warehouse command will still be affected by the mounted disk `fsync`, shared temporary directory and concurrency timing. Subsequently, these test environments must be repaired or adjusted and the original standard commands must be re-run. Single-package PASS cannot be used instead of full-margin PASS.
3. This batch only adds new reports and generates logs/cache in the ignored directory; `go.mod`, `go.sum`, business logic and license files were not modified in the end. The two pre-stored untracked paths `cmd/agentsql/b5-wal/` and `cmd/agentsql/﹎memory﹎.instance-id` are not touched.

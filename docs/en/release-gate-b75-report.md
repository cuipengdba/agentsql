# Batch 75: v0.5.0 releases gate offline verification report

**Language:** [English](release-gate-b75-report.md) | [Simplified Chinese](../release-gate-b75-report.md)

Verification date: 2026-10-09 (CST, UTC+08:00). Baseline: `bfc743ad6f8ab3241b6051248e5d530ca630c4a0`, branch `feature/v0.4`. This batch is only for verification. The business code has not been modified, git write operations have not been performed, the Internet is not connected, and it is not signed. All container runs use `--network none --pull never`, and Go commands use `GOPROXY=off` and `GOTOOLCHAIN=local`. The original logs all have start/end times and exit codes, located at [`dist/release-gate-b75/`](../../dist/release-gate-b75).

**Overall judgment: Release B Gate failed.** The current offline environment lacks Go module cache, and the five rounds of P99, two full warehouse builds, and targeted three-package tests have not been completed; the full warehouse short tests command exited at 1.15. The asset dry-run was not executed under the network ban and file range constraints of this batch. The following PASS only represents the actual passing of the corresponding command, and does not mean that the published assets are available.

## Gates 1–7

| Project | Conclusion | Real evidence and reasons |
| --- | --- | --- |
| 1. parser P99 five consecutive rounds | **NOT RUN** | [`p99.log`](../../dist/release-gate-b75/p99.log): The network ban standard test command actually exits 1, `pg_query_go/v5`, `vitess` cache is missing, and stops in the parser setup stage. None of the five rounds has started; rounds 1–5 of P50/P90/P99 are all **N/A**, and it cannot be determined that “all five rounds of P99 are ≤5ms”. |
| 2a. `go build ./...` | **NOT RUN (not completed)** | [`build-all.log`](../../dist/release-gate-b75/build-all.log): Actual exit 1, multiple module searches failed under `GOPROXY=off`. |
| 2b. `go build -tags=yashan ./...` | **NOT RUN (not completed)** | [`build-yashan.log`](../../dist/release-gate-b75/build-yashan.log): Actual exit 1, both because the module cache is missing; it cannot be judged as a PASS built with the YashanDB tag. |
| 3. parser/rules/pipeline directed short tests | **NOT RUN (not completed)** | [`short-targeted.log`](../../dist/release-gate-b75/short-targeted.log): Actual exit 1, all three packages failed in setup phase, no test assertion results. |
| 4. Full position short tests | **FAIL (command exit 1)** | [`short-all.log`](../../dist/release-gate-b75/short-all.log), [ package-by-package results ](../../dist/release-gate-b75/short-all-packages.log), [ package enumeration ](../../dist/release-gate-b75/package-enumeration.log): 47/47 packages have results, 6 packages have actual test PASS, 6 packages have no test files, 35 packages have setup failed due to missing module cache, test **NOT RUN**;Assertion failed with no executed tests. 12/47 package-level successes (25.53%, including 6 non-test file packages); 6/41 (14.63%) of the test packages actually passed. `internal/b5dml` took 495.220 seconds and PASS, not a timeout. |
| 5. 15 assets dry-run | **NOT RUN** | [`dryrun.log`](../../dist/release-gate-b75/dryrun.log), [ script precondition ](../../dist/release-gate-b75/offline-prerequisites.log): `pwsh` is not in PATH; the original script directly calls Docker without network ban parameters, online `dnf`/`curl`/Go agent, and writes outside the specified output directory, so it cannot be executed according to the red line of this batch. `RELEASE_DRYRUN_ASSET_COUNT`, `SHA256SUMS`, Yashan redistribution logo and `IMAGES` have no real running results; this batch of 15 assets has not been produced. |
| 6. Rocky 8 dual-architecture executable, shell syntax | **PASS (only these two items)** | [`dual-arch.log`](../../dist/release-gate-b75/dual-arch.log): amd64 is `x86_64`, arm64 is `aarch64`, and exits 0 twice; the syntax check of `build-release-linux.sh` and `package-release.sh` of the two architectures exits 0. **NOT RUN** is officially built and packaged. Please see below for the required downloads and prerequisites. |
| 7. Existence of license file | **PASS (existence check)** | [`license.log`](../../dist/release-gate-b75/license.log): Both `LICENSE` and `NOTICE` tracked by the repository root exist, but `LICENSE.md` does not exist. This is inconsistent with the mission background "license file does not exist". The file content has not been read, so the license type has not been approved and is still waiting for the user's decision. |

[ environment mirror evidence ](../../dist/release-gate-b75/environment.log) shows that the machine has cached the script specified digest of Rocky 8 and Syft, Buildx v0.37.1 available. [ signature tool compilation log ](../../dist/release-gate-b75/releasesign-build.log) shows Go 1.26.8 Linux/amd64 and `scripts/releasesign` compilation exit 0; this does not mean that the signature verification is passed. `build-release-linux.sh` will run `dnf`, obtain the go.dev release list and Go tarball, `go mod download`, and obtain the dual-architecture YashanDB client; `package-release.sh` also requires the official binary, Go tool chain, YashanDB library and root `LICENSE`. These formal build prerequisites are not completed offline.

## Full position short tests package by package results

In the table, **NOT RUN** corresponds to the `[setup failed]` originally output by Go, because the offline module cache is missing; **PASS (no test)** only indicates that the package command is successful and `[no test files]` is not counted as a passed test execution. See [`short-all.log`](../../dist/release-gate-b75/short-all.log) for the complete error context.

| Package (omit `github.com/cuipengdba/agentsql/` prefix) | Status | Description |
| --- | --- | --- |
| `cmd/agentsql` | **NOT RUN** | setup failed; offline module cache is missing |
| `cmd/agentsql-b2-fallback` | **PASS** | No test files |
| `cmd/agentsql-b2-gate` | **PASS** | No test files |
| `cmd/agentsqlctl` | **NOT RUN** | setup failed; offline module cache is missing |
| `internal/adminapi` | **NOT RUN** | setup failed; offline module cache is missing |
| `internal/audit` | **NOT RUN** | setup failed; offline module cache is missing |
| `internal/auditchain` | **PASS** | Test passed, 0.031s |
| `internal/auditrelay` | **NOT RUN** | setup failed; offline module cache is missing |
| `internal/auth` | **NOT RUN** | setup failed; offline module cache is missing |
| `internal/authorizedexecute` | **NOT RUN** | setup failed; offline module cache is missing |
| `internal/authorizedexecute/internal/businessdb` | **NOT RUN** | setup failed; offline module cache is missing |
| `internal/b2release` | **NOT RUN** | setup failed; offline module cache is missing |
| `internal/b5` | **PASS** | Test passed, 0.095s |
| `internal/b5coordinator` | **NOT RUN** | setup failed; offline module cache is missing |
| `internal/b5dml` | **PASS** | Test passed, 495.220s |
| `internal/b5session` | **NOT RUN** | setup failed; offline module cache is missing |
| `internal/b5terminal` | **NOT RUN** | setup failed; offline module cache is missing |
| `internal/b5wal` | **NOT RUN** | setup failed; offline module cache is missing |
| `internal/bootstrap` | **NOT RUN** | setup failed; offline module cache is missing |
| `internal/columnauth` | **NOT RUN** | setup failed; offline module cache is missing |
| `internal/compliance` | **NOT RUN** | setup failed; offline module cache is missing |
| `internal/config` | **NOT RUN** | setup failed; offline module cache is missing |
| `internal/controlledread` | **NOT RUN** | setup failed; offline module cache is missing |
| `internal/demoseed` | **NOT RUN** | setup failed; offline module cache is missing |
| `internal/discovery` | **PASS** | Test passed, 0.029s |
| `internal/engine` | **NOT RUN** | setup failed; offline module cache is missing |
| `internal/eventbus` | **NOT RUN** | setup failed; offline module cache is missing |
| `internal/lockrank` | **NOT RUN** | setup failed; offline module cache is missing |
| `internal/mask` | **NOT RUN** | setup failed; offline module cache is missing |
| `internal/mcpserver` | **NOT RUN** | setup failed; offline module cache is missing |
| `internal/metrics` | **NOT RUN** | setup failed; offline module cache is missing |
| `internal/model` | **PASS** | Test passed, 0.030s |
| `internal/notify` | **NOT RUN** | setup failed; offline module cache is missing |
| `internal/parser` | **NOT RUN** | setup failed; offline module cache is missing |
| `internal/pipeline` | **NOT RUN** | setup failed; offline module cache is missing |
| `internal/policy` | **NOT RUN** | setup failed; offline module cache is missing |
| `internal/rbac` | **NOT RUN** | setup failed; offline module cache is missing |
| `internal/redaction` | **NOT RUN** | setup failed; offline module cache is missing |
| `internal/rules` | **NOT RUN** | setup failed; offline module cache is missing |
| `internal/server` | **PASS** | No test files |
| `internal/store` | **NOT RUN** | setup failed; offline module cache is missing |
| `internal/version` | **PASS** | No test files |
| `internal/webui` | **PASS** | Test passed, 0.009s |
| `scripts` | **PASS** | No test files |
| `scripts/highgo` | **NOT RUN** | setup failed; offline module cache is missing |
| `scripts/highgo/pgx-probe` | **NOT RUN** | setup failed; offline module cache is missing |
| `scripts/releasesign` | **PASS** | No test files |

## Gap list

The following is counted according to the actual evidence of the current machine: **A 5 items, B 4 items, C 3 items, local environment 2 items, a total of 14 items**. The specified digests for Rocky 8 and Syft have been cached, so they are not listed as local download gaps; if you change to a new release runner, you still need to access and verify them online.

### A. Must be online to obtain or restore outbound access (5 items)

1. The **Go module depends on**: parser's `pg_query_go/v5`, `vitess`, as well as the whole warehouse's testcontainers, `modernc.org/sqlite` and many other missing caches; 35 packages are stopped in the setup stage. It is necessary to prefetch and verify the complete cache according to the locked version and then rerun all Go gates.
2. **Official Go 1.26.8 dual-architecture tarball and go.dev verification list**: The release script is fixed to download and verify amd64 and arm64 files; the native amd64 Go container cannot replace the download verification of the official script.
3. **YashanDB client**: The publishing script requires fixed versions of x86_64, aarch64 client and two shared libraries. SHA-256 in the script needs to be obtained and verified.
4. **Rocky builds dependency package**: The script executes `dnf install` in the container. These rpm and repository metadata are not preset when the network is completely disabled, and the formal build cannot be completed according to the original script.
5. **officially releases service outbound connection**: GitHub tag/Release and back download, GHCR push and anonymous pull, official website deployment and verification, and PVR settings all require network. All of this batch has not been executed. The basic image metadata of the multi-architecture Buildx diagram also needs to be verified during the official build and can be parsed offline, which is currently not proven.

### B. Credentials or administrative rights required (4 items)

1. GitHub repository tag/Release write permission and `write:packages` scope.
2. GHCR package management rights, including public visibility settings.
3. Official website ECS deployment credentials, and related DNS/certificate permissions.
4. GitHub PVR sets access permissions.

### C. Must be approved by the user (3 items)

1. **license type**: The choice between Apache-2.0 and AGPLv3 still needs to be decided by the user. Currently `LICENSE` and `NOTICE` already exist, but their content is prohibited from being read in this batch, and the type cannot be determined based on this; it cannot be written as "License file is missing".
2. Official Ed25519 private key placement, storage and destruction strategy. No keys or signatures were generated for this batch.
3. 2026-10-09 Today’s specific release time window. The top of the existing list still reads 2026-10-16 16:00 CST, which is inconsistent with this user decision; there are no changes to the list in this batch.

### Local environment and working tree (2 items)

1. The current Windows session does not have `pwsh`, and the original 15 asset script cannot be started according to the specified command; PowerShell 7 needs to be provided in the release environment, and the network ban execution constraints of the script must be resolved first.
2. [git status log ](../../dist/release-gate-b75/git-state.log) shows that the baseline working tree has two untracked items located on paths that are prohibited from being touched by users; this batch has not been read or cleaned. The official list requires a clean sealed checkout, which must be rechecked in another compliant clean work tree. `git diff --check` Exit 0 simply means that differences were tracked without whitespace errors.

## Release judgment

As of the end of this batch, **v0.5.0 does not meet the release date. Gate B**: P99 has no data for five consecutive rounds, two full-position builds have no PASS, directional short tests have no PASS, and full-position short tests have exited 1, and the 15 asset dry-run has no product. Dual-architecture runtime with scripting syntax, license presence, and partial PASS compiled by signing tools cannot replace these required gates. After restoring the dependency and compliance environment, you must re-run the same fixed command and keep the new complete log.
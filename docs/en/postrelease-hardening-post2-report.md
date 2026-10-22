# Post-release hardening post2 report

**Language:** [English](postrelease-hardening-post2-report.md) | [Simplified Chinese](../postrelease-hardening-post2-report.md)

Date: 2026-10-11. The following container timestamps are UTC (Beijing time plus 8 hours). All Go acceptances use the local `golang:1.26-bookworm` image, `--network none`, the source code root `D:\ruanjiansheji\agentsql-v04` is read-only mounted to `/workspace`, the host module cache `C:\Users\Administrator\go\pkg\mod` is read-only mounted to `/go/pkg/mod`, the repository `.gocache` is mounted to `/gocache`, and set `GOCACHE=/gocache`, `GOPROXY=off`. The container working directory is `/workspace`. `dist/` was not deleted.

## Repair

| Project | root cause | Changes and Conclusions |
| --- | --- | --- |
| P0-2 | The two source code directory traversal tests entered the historical product `dist/` ignored by git, and misjudged the product copy as source code. | Only `filepath.SkipDir` is returned for the directory name `dist` in the `WalkDir` callback of `TestBusinessDriverImportsStayInsideCapabilityDomain` and `TestSQLSinkCallsitesMatchManifest`; the original skip list and judgment logic remain unchanged. Both tests PASS in the presence of `dist/`. |
| P0-3 | The compiler test creates a temporary directory in the root of the repository, and the read-only mount reports `read-only file system`. | Use `os.MkdirTemp("", "capabilitycompile")`; each subtest writes a temporary `go.mod` in the system temporary directory, points `replace` to the repository root, and executes `go test -mod=mod .` from that directory. The child process sets `GOWORK=off`, `GOPROXY=off`, and `GOSUMDB=off` to avoid querying the verification server for new temporary modules in an offline environment. Added forward use cases that allow import, and the original two prohibited capabilities continue to check for specific compilation errors. Temporary directory deleted by `t.Cleanup`. All three groups of sub-tests under read-only mounting are PASS. |
| P1-1 | `key_registry.go` There are extra blank lines at the end of the file. | Delete the empty line and `gofmt -l` will no longer list this file. |
| P1-2 | The single batch mode always points to the expired version directory; the chain mode first searches for the old version, and when multiple versions are found recursively, "Unable to uniquely determine" will be reported. | The two modes share `Get-CodexExecutable`: enumerate `codex.exe` in the directory below the installation root, and select deterministically in descending order of `LastWriteTimeUtc` of the executable file and in ascending order of the complete path; if there is no candidate, an error will occur when searching for the root path. Retain the original self-test coverage path restrictions in chain mode. The actual installation analysis, no candidate and double candidate selection point tests all passed. |

## Acceptance record

The following `go ...` commands are actually executed in the above offline, source code read-only container. Single commands use the same mount and environment settings; the complete acceptance sequence is executed and stops if the previous step fails. The timestamp is the actual container time printed before the command started.

| Time (UTC) | Actual test commands and conditions | result |
| --- | --- | --- |
| 2026-10-11 15:16:25 | `test -d dist`；`go test -short ./internal/authorizedexecute -run TestBusinessDriverImportsStayInsideCapabilityDomain -count=1 -v` | `dist=present`; Target test PASS; Container exited 0. |
| 2026-10-11 15:23:47 | `go test -short ./internal/authorizedexecute -run TestCompilerRejectsRawBusinessDatabaseCapabilities -count=1 -v` | Allow import, internal package rejection, and deleted API rejection are all PASS; the container exits with 0. |
| 2026-10-11 15:26:14 | `test -d dist`；`go test -short ./internal/authorizedexecute -run TestSQLSinkCallsitesMatchManifest -count=1 -v` | `DIST_PRESENT`;Target test PASS. |
| 2026-10-11 15:27:02 | `go build ./...` | `GO_BUILD_PASS`, exit 0. |
| 2026-10-11 15:28:50 | `go build -tags=yashan ./...` | `GO_BUILD_YASHAN_PASS`, exit 0. |
| 2026-10-11 15:30:39 | `go test -short ./internal/authorizedexecute ./internal/b5wal -count=1` | Both packages are `ok`; `GO_TEST_PASS`, exit 0. |
| 2026-10-11 15:33:01 | `go vet ./internal/authorizedexecute ./internal/b5wal` | No alarm; `GO_VET_PASS`, exit 0. |
| 2026-10-11 15:33:56 | `gofmt -l internal/authorizedexecute/capability_test.go internal/b5wal/key_registry.go` | The output is empty; the container exits 0. |
| 2026-10-11 23:13:08 +08:00 | PowerShell parses the runner AST and calls `Get-CodexExecutable` alone; then passes in the non-existent installation root call | Script parsing error 0; parsed to `C:\Users\Administrator\AppData\Local\OpenAI\Codex\bin\2e5e00daee91c61d\codex.exe`; no error candidates contain the searched root path. |
| 2026-10-11 23:13:57 +08:00 | Create two simulation versions in the system temporary directory, assign different `LastWriteTimeUtc` and then call `Get-CodexExecutable -Bin <临时根>` | Select the newer `b\codex.exe`; the temporary directory has been cleaned. |
| 2026-10-11 23:35:07 +08:00 | `git diff --check`; Check changed file names and report trailing blanks | `git diff --check` Output is empty, exit 0; change range is the following four files; no trailing blanks are reported. |

When the compiler test was moved to the temporary module for the first time, querying `sum.golang.org` under `--network none` failed due to the new module; after adding `GOSUMDB=off` that is limited to the test subprocess, rerun it according to the above table and pass. Warehouses `go.mod` and `go.sum` have not been modified.

## File scope and legacy

- `internal/authorizedexecute/capability_test.go`: Two traversals skip `dist`; the compilation test is moved to the system temporary directory and overrides allowed and prohibited imports.
- `internal/b5wal/key_registry.go`: Only delete excess blank lines at the end of the file.
- `scripts/codex-batch-runner.ps1`: Unify two modes of Codex executable parsing.
- `docs/postrelease-hardening-post2-report.md`: This report.

No new repository fixtures have been added. The runtime temporary `go.mod` and test source files are located in the system temporary directory and are cleaned up by `t.Cleanup`. `dist/` Reserved. Remaining issues: There are no failed items within the scope of this batch.

# v0.5 Known problem fix record (Batch 14/#36)

**Language:** [English](known-issues-fixes.md) | [Simplified Chinese](../known-issues-fixes.md)

## Batch 86 (WorkBuddy takeover self-inspection): Problem review conclusion

Source: WorkBuddy conducts end-to-end self-inspection on `feature/v0.4` under the premise of "no code and no damage to the environment"
(Original listing `codex-task-batch86-issues.md`, outside repository). Doubao independently reviewed each item and concluded as follows.
Review date: 2026-10-11. The following is a read-only verification. No code has been changed in this batch and no repairs have been performed.

### Priority list (after review)

| No. | original rating | Review conclusion | Rating adjustment | Dispose |
| --- | --- | --- | --- | --- |
| P0-1 | P0 | The principle of hidden danger is established, but the current access control of **does not reappear** | Downgrade to P2 reinforcement recommendations | Unified script exit code closing specifications |
| P0-2 | P0 | **confirms**: two local tests false failed (false failed, non-false green) | Maintain P0 | Plan B (SkipDir supplement `dist`) is the root cause, and can be superimposed on Plan A (clean up `dist`) |
| P0-3 | P0 | **confirms**: failed only under read-only `:ro` mount | Downgraded to P1 Robustness | Change the temporary directory to the temporary location of the system and ensure module resolution |
| P1-1 | P1 | **confirms**: gofmt historical legacy | Maintain P1 | There is an extra blank line at the end, please fix it next time you make changes. |
| P1-2 | P1 | **confirms**: runner hardcoded failure path | Maintain P1 | Change to dynamic detection and get the latest version |
| P2-1 | P2 | **confirms**: Environmental issues | Maintain P2 | Only record, no processing required |
| P2-2 | P2 | **confirms**: Environmental facts | Maintain P2 | The reachability of the real database must be compared with the negative example of the same network segment. |

### P0-1: The hidden danger of "false green" in access control - the principle is established, but the current script does not reproduce it

- Principle: When `go test` passes through the pipeline `| grep/tail`, the exit code at the end of the pipeline comes from the last command.
  Will swallow the non-zero exit code of `go test` - this is a real risk and deserves unified reinforcement.
- However, the authoritative access control script `dist/release-gate-b77/run-gate.ps1` does not have this problem:
  After line 36 `& docker ... 2>&1 | ForEach-Object {...} | Tee-Object`,
  Line 37 `$result = $LASTEXITCODE`, Line 39 `exit $result`;
  `ForEach-Object`/`Tee-Object` are cmdlets and do not modify `$LASTEXITCODE`.
  Therefore, the exit code is the real exit code of `docker` (that is, `go test` in the container).
- `scripts/check-capability-imports.ps1` to `$ErrorActionPreference='Stop'`
  And explicitly check `$LASTEXITCODE -ne 0` after `go list`, the violation is `throw`.
- There is no `go test` call under the source code `scripts/`, nor does it exist mentioned by WorkBuddy
  `b43-verify.sh` (not found in `scripts/tests/deploy/.github` and other directories).
- Conclusion: There is currently no actual defect of "testing FAIL but reporting green"; it is reserved as a general hardening item.
  It is recommended that all subsequent access controls explicitly record the exit code of each step and close it with accumulated variables.

### P0-2: `dist/` physical directory contamination caused two false test failures - confirmed

- The actual measured size of the repository root `dist/` is 9.43 GB. Although it is ignored by `.gitignore:15`, it physically exists.
- Contains 5 copies of historical product `.go` (`dist/release-b81/validation-source/scripts/...`).
  Among them, `highgo/pgx-probe/main.go` imports `jackc/pgx/v5` and `pgconn`,
  `yashan-native-probe.go` imports `database/sql`, both of which hit the prohibited list;
  Their whitelist paths are `scripts/highgo/pgx-probe/main.go`,
  The copy paths of `scripts/yashan-native-probe.go` and `dist` do not match and are deemed to have exceeded their authority.
- `TestBusinessDriverImportsStayInsideCapabilityDomain`
  and `TestSQLSinkCallsitesMatchManifest` both use `filepath.WalkDir(root)` to traverse the physical directory;
  The SkipDir list of `capability_test.go` (lines 38, 178) contains
  `.git/.gocache/.design/go-build*/capabilitycompile*`, **is missing `dist`**.
- This is a "false failure" rather than a code defect, and it occurs only in batch81 (after 20:30);
  The access control green at the time of release (10-09 12:39) was obtained before the pollution occurred and does not affect the released products.
- Repair plan (changed test file has been submitted, **is waiting for the user’s decision**):
  - Plan B (recommendation, radical cure): Add `entry.Name() == "dist"` to two SkipDir lists;
  - Option A (optional, reclaim space): Clean `dist/` 9.43 GB.
  It is recommended to do both A and B: B to prevent recurrence, and A to recycle the disk.

### P0-3: Compiler test false failure under read-only mount - Confirm (`:ro` only)

- `TestCompilerRejectsRawBusinessDatabaseCapabilities` line 138
  `os.MkdirTemp(root, "capabilitycompile")` creates a temporary directory in the source code root;
  When the repository is mounted with `:ro`, it will report `read-only file system`.
- The actual access control `run-gate.ps1` is mounted as `${root}:/src` (readable and writable, without `:ro`).
  Therefore, the normal release process is not affected; this item is a robustness issue.
- Repair direction (change has been submitted for testing, **is waiting for the user’s decision**): temporary directory is used instead
  `os.MkdirTemp("", "capabilitycompile")`; because the tested use cases need to be imported into this warehouse
  `internal/...` package, must also ensure module resolution (add `go.mod` to the temporary directory and end with
  `replace` points to the repository root, or equivalent `go.work`), you cannot change just one line.

### P1-1: gofmt historical legacy - confirmed

- `gofmt -l internal/b5wal/key_registry.go` hits; `gofmt -d` shows only
  There is an extra blank line at the end of the file (`}\n\n` → `}\n`). The file has been submitted and was not introduced by this change.
  Just correct it next time.

### P1-2: runner hardcoded failure path (time bomb) - confirmed

- `scripts/codex-batch-runner.ps1` Lines 231 and 400 are hardcoded
  `...\bin\f544b3844e0f14e9\codex.exe`, it is actually measured that this directory no longer exists;
  Now rely on lines 233-236 to recursively search for "exactly 1 codex.exe".
  If multiple version directories are left after the Codex upgrade, `Cannot identify unique codex.exe` will be thrown.
- Repair (modify the submitted script, **is waiting for the user’s decision on**): enumerate `bin\*\codex.exe` and press
  Use the latest version/time and remove the hard-coded path.

### P2-1/P2-2: Environmental items - confirm, record only

- P2-1: The backup copy `.git` points to the original main directory worktree. It will disappear when you switch back to the main project directory and no processing is required.
- P2-2: Docker Desktop (WSL2) transparently hijacks outbound traffic, naked TCP/`/dev/tcp` in the container
  The detection will be false positive for cross-network segment addresses; the "reachable" conclusion of the real database E2E must be based on addresses that do not exist in the same network segment.
  Negative control (OPEN is only credible when the control group is CLOSED).

### Recommended repair sequence

1. Can be done directly without any choice: P1-1 (gofmt), P1-2 (runner dynamic detection), P0-2 Plan B
   (SkipDir added `dist`), P0-3 (temporary directory moved out, pay attention to module analysis) - all are submitted files.
   It is recommended to merge into a batch to be implemented by Codex and submitted accurately after the bean bag is accepted.
2. Space operations that require user decision-making: P0-2 Plan A (cleaning `dist/` 9.43 GB), independent of step 1.
3. P2-1 and P2-2 are only used for environment records and do not cause code changes.

## Batch 36: Business metadata tenant ownership and boundaries

The console business metadata has been tenanted. Migration uniformly backfills historical rows that lack trusted attribution information
`tenant_default`; This is a deterministic compatibility agreement, not an inference of actual business ownership. console from
The access token `tid` is bound to the warehousing context and is not visible in the cross-tenant list. Details, updates and deletions return 404;
Management audit outbox and dropout events retain the same tenant context. The runtime/platform table retains its original structure.

The boundary that has not been closed is the MCP execution data path: the Agent API Key authentication protocol remains unchanged, the Agent
The entity can resolve the tenant it belongs to, but the tenant has not been explicitly propagated to all datasources/policy, etc.
Warehouse call. Legacy internal/MCP calls without explicit tenants are currently pinned at the `tenant_default` compatibility boundary,
Therefore, this batch of console isolation expansion should not be expressed as MCP data access has also been fully tenanted. The follow-up should be in
Machine authentication results are passed between tenants and MCP request context, and complement the cross-tenant execution attack matrix.

Record date: 2026-10-04.

## Batch 35: MCP SSE Replay and Restart Boundary

go-sdk v1.7.0of`StreamableHTTPHandler.sessions`It is a pure memory table, and the upstream still retains the TODO of session retention (#148), and does not close the`ServerSession`Reload the handler's interface. Therefore, this batch has not realized, nor does it claim to realize the "old`Mcp-Session-Id`Seamless life extension across processes.”

This batch is delivered with two clear paths: the stateful old protocol uses SQLite/PostgreSQL persistence EventStore to support `GET /mcp` + `Last-Event-ID` disconnection replay, and will fail-closed with HTTP 400 when the TTL/capacity purge creates a gap; after the process is restarted, the old session returns HTTP 404 and `Mcp-Session-Expired: 1`, and the client will re-`initialize` accordingly. Crash legacy events and cursors are lazily recycled by subsequent activities on a TTL basis, with no reliance on `SessionClosed` and no background cleanup thread.

Record date: 2026-10-02.

> Status update (2026-10-04, batch 33): The main body of this document retains the investigation, recurrence and disposal records of batch 14 at that time. Its R005 "to be repaired" conclusion has been closed by `02565f5` submitted by batch 17, and no longer represents the current product status; see `docs/release-notes-v0.5.md` and `docs/USER_GUIDE.md` for current capabilities and release caliber.

## Research scope and conclusion

- GitHub public REST query `cuipengdba/agentsql`: repository `has_issues=true`, `open_issues_count=0`; `state=open` and `state=all` both return empty arrays; directly reading issue `#36` returns 404. Therefore, there is currently no open issue that can be listed in the public repository. `#36` is recorded according to this task number, and the remote issue content cannot be fabricated based on this.
- We did `TODO` / `FIXME` / `XXX` on the project's own source code, configuration, scripts, tests and `docs/`, and no pending mark was found. There is a text record of previous scan results in `docs/v0.5-known-issues-task7.md`, which is not a new TODO; there is an upstream dependent FIXME in `web/node_modules`, which is not the project's own code.
- On the repository baseline at the time of batch 14, the clear known problem of the product was that the R005 production dynamic alarm was missing. There are also R005 front-end rule metadata errors, parser P99 environment-related fluctuations, and real MCP clients and Testcontainers/Ryuk items to be tested; among them, the R005 runtime gap was later closed by Batch 17.
- No Git commands were run in this batch.

## priority list

| priority | Project | Conclusion | Reason |
| --- | --- | --- | --- |
| P0 | R005 Large result set alarm may not be generated in ordinary production queries. | **batch fourteen has not been revised; batch seventeen `02565f5` has been closed** | The user-visible risk warning at batch 14 is missing; subsequent repairs also cover production dynamic EXPLAIN, general execution truncation, and PostgreSQL column-level authorization truncation, and no partial path patch is used to pretend to be a complete repair. |
| P1 | PostgreSQL parser lineage P99 occasionally exceeds 5 ms | **is related to the environment, only** is recorded | In the past, Windows Docker exclusive reappearance was still affected by scheduling, lacked a fixed CPU/alloc profile of Linux runner, and could not be stably attributed. Thresholds must not be relaxed to cover up failures. |
| P2 | R005 is marked as static in rules page metadata | **Fixed** | The judgment of R005 reads `AST.Explain`, and the front-end display of "static" is inconsistent with the implementation and user guide, which will mislead the administrator. |
| environment | Testcontainers/Ryuk, Windows Docker file system, host missing `rg`/Go/Node, real beanbag/Claude client | **is not repaired, only** is recorded | All require a running environment, tool chain or real client/account, and the production logic should not be changed accordingly. |

"To be tested/not implemented" in the database compatibility document is a declared product boundary, and defects that do not belong to #36 can be directly fixed. No changes have been made to Yashan, OpenTenBase, DM or Oracle dialect in this batch.

## P0: R005 production dynamic alarm missing (batch fourteen history; batch seventeen has been closed)

### phenomenon

`SELECT` without `LIMIT` A normal production pipeline may return `allow` without an R005 hit even if the EXPLAIN estimate exceeds the R005 threshold. The results are still truncated by the data source `row_limit`, so the data return cap is not violated, but the risk alerts, rule page expectations, and audit hits are incomplete.

### Recurrence path

1. Construct PostgreSQL or MySQL `SELECT` without `LIMIT`.
2. Have a controlled EXPLAIN return `EstScanRows` greater than the R005 threshold.
3. Call `Pipeline.Process`.
4. The existing regression use case for `internal/pipeline/demo_test.go` is clearly documented: Demo returns `warn` and hits R005, while production returns `allow` without R005.

### root cause

`internal/pipeline/rules.go` only puts R005 into the dynamic rule set in Demo mode; when R005 is executed in the static phase of production, `AST.Explain` is still empty, so the rule returns as allowed. The normal dynamic phase then executes EXPLAIN but does not evaluate R005 again.

In addition, the PostgreSQL column-level authorization SELECT path enabled by default enters a separate binding, authorization, masking, persistent audit, final fence and delivery seal closed loop after static gate control, without going through the common dynamic stage. Simply adding R005 to the dynamic collection can only repair the non-column-level authorization path and cannot overwrite the default production path.

### Follow-up repair plan

Separately design dynamic fact delivery for column-level authorization paths: the same controlled prepared statement/business snapshot must be reused, and it is prohibited to add a second unproven SQL entry for EXPLAIN; dynamic assessment must be determined before performing permission and persistent auditing, and enter the same audit record. Unknown plan format, missing facts, EXPLAIN, or audit failure all remain fail-closed.

### Acceptance criteria

- In the ordinary path and the default PostgreSQL column-level authorization path, queries without `LIMIT` and estimated to exceed the threshold hit R005 and return `warn`; with `LIMIT`, ungrouped pure aggregation, and scenarios with equal thresholds, there are no false positives.
- Rule overrides PostgreSQL/MySQL; unknown plan format and EXPLAIN errors must not skip gated execution.
- R005 hits, estimated rows, and final decisions are written to the same persistent audit; column-level authorization, masking, final fence, delivery seal, and `row_limit` have unchanged semantics.
- Provides real-failure to fixed-pass single testing with column-level authorization attack matrices and associated E2E.

This batch does not submit one-line classification changes that only cover part of the path, nor does it weaken authorization, masking, auditing, or query access control.

After batch 17 submits `02565f5`, R005 is included in the general production dynamic rule set; the general execution results and the PostgreSQL column-level authorization persistent audit callback will also re-evaluate R005 at `Truncated=true`. The structured hit goes into response `Assessment.Hits` and is serialized by `internal/pipeline/auditmap.go` to `rule_hits` for the same audit record. `internal/pipeline/pipeline_test.go`, `internal/pipeline/r005_column_select_test.go`, and `internal/rules/generic_test.go` cover boundaries where the estimated threshold is exceeded, actual truncation, not exceeded, and explicit LIMIT is still truncated by actual truncation.

## P2: R005 metadata error

### Phenomenon and recurrence

The rules page reads `web/src/constants/ruleMeta.ts`, among which `dynamic` of R005 was originally `false`, and the interface displays "static"; but the backend R005 clearly reads `AST.Explain.EstScanRows`, and the user guide also states that it relies on EXPLAIN.

### root cause

The frontend static metadata is not updated with R005's EXPLAIN dependency, and the user guide table documents the same discrepancy with "No (but actually depends on EXPLAIN)".

### Repair and acceptance

- Change `dynamic` to `true` for R005 and change the User Guide table to "Yes".
- Add `web/src/constants/ruleMeta.test.ts`, and lock R004 and R005 as dynamic rules.
- The same source code assertion failed before repair: `R005 is not classified as dynamic`; passed after repair: `R005 dynamic metadata PASS`.
- The current host has no Node, and the existing `vitest.cmd` returns `node is not recognized`; the security review does not allow downloading of new Node images, so the local execution of Vitest is marked as environment unsatisfied, and PASS is not falsely reported. CI/environments with Node should run `npm test -- src/constants/ruleMeta.test.ts`.

This fix only changes the display metadata and documents, and does not change the runtime rules, authorization, masking, auditing, query access, or database dialect.

## Environment and items to be measured

- `rg`, host Go, and host Node are not in PATH; use PowerShell to complete the read-only scan, and use the existing `golang:1.25-bookworm` container for Go verification.
- The native Windows Docker lifecycle of Testcontainers/Ryuk still requires an environment with native CGO and access to Docker named pipes; it is not handled by disabling security components or changing production code.
- Parser P99 needs to be released on Linux with a fixed CPU and no parallel load, run the runner again and collect the CPU/alloc profile.
- Doubao Streamable HTTP and Claude Desktop stdio still require real account/client verification; local SDK testing cannot replace real client conclusions.
- SQLite/WAL fsync thrashing on Windows Docker Desktop persistence file systems is recorded as an environmental fact only; production persistence must not be circumvented using tmpfs instead.

## self-examination evidence

- `go build ./...`: passed in the existing `golang:1.25-bookworm` container, exit code 0.
- `go test -short ./internal/pipeline ./internal/rules -count=1`: Passed, 0.584 seconds and 0.050 seconds respectively.
- `go test -short ./internal/pipeline -run '^TestProcessDemoAddsDynamicR005WithDatasourceRowLimitOnly$' -count=1 -v`: Passed in batch 14; this use case reproduced the difference between Demo hitting R005 and production not hitting. Batch 17 has updated the production assertion to also hit R005.
- R005 Metadata source code assertion: failed before repair and output `R005 is not classified as dynamic`; after repair, the source code and user guide consistency check passed.
- Change file trailing whitespace, conflict mark, end newline check: passed. The task redline prohibits any Git operations, so `git diff --check` was not run, and using the exact file check above instead, it cannot be represented as performing a Git check.
- The entire warehouse of product Go source code `gofmt -l` scanned out the existing file `internal/b5wal/key_registry.go` that was not touched by this batch. There are no Go file changes in this batch; the file has not been modified according to the "#36 only, no smooth reconstruction/format noise" constraint, so the gofmt access control of the whole warehouse cannot be marked as passed.
- The new Vitest use case has been deployed, but the host does not have a Node. The actual test of `vitest.cmd` returns `node is not recognized`; there is no false report that the test passed.

## Change files in this batch

- `web/src/constants/ruleMeta.ts`
- `web/src/constants/ruleMeta.test.ts`
- `docs/USER_GUIDE.md`
- `docs/known-issues-fixes.md`

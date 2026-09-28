# B5 S10 executable reliability evidence

All programs and tests in this document are feature-flag independent. They do
not enable `b5_sessions`, `b5_tx_postgres`, or `b5_tx_mysql`.

## Fault matrix

The executable matrix contains 16 logical cells. The PostgreSQL terminal suite
expands its nine logical cells across PG14/PG18, TLS on/off, and direct/proxy
paths; together with restart/PID-reuse cases this is exactly 116 leaf
PostgreSQL terminal subtests. The script consumes `go test -json`, rejects an
unexpected leaf count, and writes every leaf status to the matrix JSON instead
of treating a package exit code as the only evidence.
An additional zero-logical-cell supporting suite exhaustively enumerates all
write phases against current positive/rejection, wrong-operation, stale,
untyped, weak-correlation, duplicate reply, and RFQ status combinations; it
does not inflate the 16 design cells or the 116 real-PG leaf count.

| # | Injected cell | Required assertion | Executable evidence |
|---:|---|---|---|
| 1 | Disconnect before terminal send | `NOT_COMMITTED`, non-release, backend gone | `TestPGTerminalContainerMatrix/before-send-zero` |
| 2 | Terminal timeout | UNKNOWN only after a full terminal frame; watchdog destroys connection | `TestPGTerminalContainerMatrix/full-frame-ack-missing` |
| 3 | PostgreSQL CancelRequest | cancel-tainted, no rollback/reset/health frame, backend gone | `TestPGTerminalContainerMatrix/delayed-cancel-forced-discard` |
| 4 | Concurrent hostile stacked SQL | stable denial before coordinator/connection | `TestB5ToolLayerRejectsClosedSurfaceBeforeCoordinator/stacked` |
| 5 | Network partition | full frame plus missing replies is UNKNOWN; DB truth checked | `TestPGTerminalContainerMatrix/full-frame-ack-missing` |
| 6 | Half-open/partial frame | UNKNOWN, non-release, no committed row, backend gone | `TestPGTerminalContainerMatrix/partial-frame` |
| 7 | ACK loss with RFQ retained | UNKNOWN despite committed DB truth | `TestPGTerminalContainerMatrix/ack-missing-rfq-idle` |
| 8 | Watchdog false kill defense | normal positive commit never fires watchdog and releases cleanly | `TestPGTerminalContainerMatrix` positive commit path |
| 9 | Watchdog missed-kill defense | missing RFQ fires watchdog; no connection remains | `TestPGTerminalContainerMatrix/positive-ack-rfq-not-observed` |
| 10 | WAL write failure/fsync ambiguity | response remains LOST, writer wedges, one rotation only | `TestServiceReservationWedgeRotationAndSixRecordBound` |
| 11 | WAL disk full (`ENOSPC`) | LOST receipt, no fact retry, replacement segment only | `TestS10DiskFullReportsLostAndNeverRetriesFact` |
| 12 | Lease expiry | stale owner cannot mutate or touch the physical capability | `TestDirectoryTTLAndCloseCAS` |
| 13 | Claim drift/PID reuse | exact backend identity mismatch releases no capacity | `TestFenceRejectsLateOwnerAndPIDReuseDoesNotRelease` |
| 14 | Reaper false reclaim | inventory failure/live unknown child retains its charged slot | `TestReaperCrashWindowsAndConservativeInventoryFailure` |
| 15 | MySQL transaction request | stable `DIALECT_TRANSACTION_UNSUPPORTED`, coordinator untouched | `TestB5ToolLayerRejectsClosedSurfaceBeforeCoordinator` |
| 16 | Recovery authorization | distinct Runtime/SRE and QA/Release subjects plus clean reconciliation | `TestRecoveryRequiresDistinctRuntimeAndQAApprovers` |

Run the whole matrix from the repository root:

```powershell
./scripts/run-b5-s10-matrix.ps1 -Output artifacts/b5-s10-matrix.json
```

The PG suite requires Docker and sets only its test gate,
`AGENTSQL_B5_PG_TERMINAL_MATRIX=1`.
Every real-PG fault leaf asserts typed phase/outcome/consistency/disposition,
watchdog behavior where applicable, independent database truth, exact backend
absence, and zero remaining test backends after each direct/proxy group. The
MySQL leaf asserts stable `DIALECT_TRANSACTION_UNSUPPORTED` before coordinator
entry. The output schema is `agentsql.b5.s10-fault-matrix-report.v2` and has an
explicit `PASS`/`FAIL` status plus per-leaf status and elapsed time.

## Long SLO soak

The driver requires an explicit test-only gate and a PostgreSQL DSN. Defaults
are the non-negotiable S10 minimums: 24 hours and 10,000 terminals. Completion
requires both thresholds, so terminal volume never exempts elapsed time.

The release-owner test is default-skipped and cannot be shortened:

```powershell
$env:AGENTSQL_B5_SOAK_LONG = "1"
$env:AGENTSQL_B5_SOAK_DSN = "postgres://user:password@host:5432/database?sslmode=require"
go test -tags agentsql_b5_soak ./internal/b5soak -run '^TestLongS10Soak$' -count=1 -timeout 27h
```

The equivalent standalone-driver invocation is:

```powershell
$env:AGENTSQL_B5_SOAK = "1"
$env:AGENTSQL_B5_SOAK_DSN = "postgres://user:password@host:5432/database?sslmode=require"
go run -tags agentsql_b5_soak ./cmd/agentsql-soak --duration 24h --terminals 10000 --rate 1 `
  --datasource staging-pg18 --budget 100 `
  --output artifacts/b5-soak/report.json `
  --wal artifacts/b5-soak/terminal.wal
```

The JSON report is checkpointed periodically and contains normal-path UNKNOWN,
evidence contradictions, quarantine count/oldest age/charged ratio and the
1%/30s/5min thresholds, inventory availability and the 30s stop line, exact
claim drift, WAL continuity, bounded error classes, explicit invariant checks,
and a `RUNNING`/`PASS`/`FAIL` status. Inventory recovery eligibility is false
after a failure until two independent successful observations span at least
30 seconds. A short run may have `run_pass=true` for its requested smoke
thresholds, but `official_s10_qualified` remains false unless both 24h and
10,000 terminals are observed; terminal volume never exempts observation time.

Run the approximately 60-second, several-hundred-terminal smoke and retain its
report/WAL sample with:

```powershell
$stamp = [DateTime]::UtcNow.ToString("yyyyMMddTHHmmssZ")
$env:AGENTSQL_B5_SOAK_SMOKE = "1"
$env:AGENTSQL_B5_SOAK_SMOKE_DURATION = "60s"
$env:AGENTSQL_B5_SOAK_SMOKE_TERMINALS = "300"
$env:AGENTSQL_B5_SOAK_SMOKE_RATE = "8"
$env:AGENTSQL_B5_SOAK_SMOKE_OUTPUT_DIR = "artifacts/b5-soak-smoke/$stamp"
go test -tags agentsql_b5_soak ./internal/b5soak -run '^TestPostgres18SoakSmoke$' -count=1 -timeout 5m
```

The smoke also injects independent-inventory failure and requires automatic
admission pause followed by a scaled 250ms stop-line `FAIL` report. This is a
driver correctness test only; it does not weaken the production/report default
of 30 seconds and cannot qualify as the formal S10 observation window.

The driver never changes a product flag. Repeated `--recovery-approval
role:subject` values only evaluate and record two-person eligibility; they do
not perform recovery.

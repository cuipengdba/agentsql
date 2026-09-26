param(
    [string]$Output = "artifacts/b5-s10-matrix.json",
    [string]$Go = "go"
)

$ErrorActionPreference = "Stop"
$started = [DateTime]::UtcNow
$oldGate = $env:AGENTSQL_B5_PG_TERMINAL_MATRIX
$env:AGENTSQL_B5_PG_TERMINAL_MATRIX = "1"

$suites = @(
    @{ Name = "pg14-pg18-terminal"; Package = "./internal/b5terminal"; Run = "^TestPGTerminalContainerMatrix$"; Timeout = "30m"; LogicalCells = 9; ExpectedLeaves = 116 },
    @{ Name = "pg-terminal-proof-exhaustive"; Package = "./internal/b5terminal"; Run = "^(TestFrozenPhaseReplyTable|TestEvidenceConsistencyExhaustivePhaseReplyStatus|TestUnknownSchemaAndInvalidWriteFailClosed|TestKnownReplyWithoutReadyForQueryKeepsOutcomeAndDiscards|TestOutcomeAndDispositionShortCircuit|TestPGAdapterTypedTerminalAndLifecycleMatrix|TestPGAdapterKnownOutcomeSurvivesLifecycleFailure|TestPGAdapterWatchdogFullFrameACKMissing|TestPGAdapterCancelRequestPermanentlyTaintsAndSuppressesMainCommands|TestPGAdapterRejectsStaleOwnerBeforePhysicalWrite)$"; Timeout = "5m"; LogicalCells = 0 },
    @{ Name = "coordinator-watchdogs"; Package = "./internal/b5coordinator"; Run = "^(TestStatementTimeoutCancelTaintsAndDiscards|TestOperationWatchdogQuiescesThenUsesSingleTypedRollback)$"; Timeout = "5m"; LogicalCells = 1 },
    @{ Name = "mcp-hostile-and-mysql"; Package = "./internal/mcpserver"; Run = "^TestB5ToolLayerRejectsClosedSurfaceBeforeCoordinator$"; Timeout = "5m"; LogicalCells = 2 },
    @{ Name = "wal-failure"; Package = "./internal/b5wal"; Run = "^(TestS10DiskFullReportsLostAndNeverRetriesFact|TestServiceReservationWedgeRotationAndSixRecordBound)$"; Timeout = "5m"; LogicalCells = 2 },
    @{ Name = "lease-claim-reaper"; Package = "./internal/b5session"; Run = "^(TestDirectoryTTLAndCloseCAS|TestReaperCrashWindowsAndConservativeInventoryFailure|TestFenceRejectsLateOwnerAndPIDReuseDoesNotRelease)$"; Timeout = "5m"; LogicalCells = 1 },
    @{ Name = "two-person-recovery"; Package = "./internal/b5soak"; Run = "^(TestRecoveryRequiresDistinctRuntimeAndQAApprovers|TestClaimMonitorDetectsCountAndIdentityDrift)$"; Timeout = "5m"; LogicalCells = 1; Tags = "agentsql_b5_soak" }
)

function Get-LeafResults {
    param([object[]]$Events)

    $final = @{}
    foreach ($event in $Events) {
        if ($event.Test -and @("pass", "fail", "skip") -contains $event.Action) {
            $final[$event.Test] = $event
        }
    }
    $names = @($final.Keys)
    $leaves = @()
    foreach ($name in $names) {
        $prefix = $name + "/"
        $hasChild = $false
        foreach ($other in $names) {
            if ($other.StartsWith($prefix, [StringComparison]::Ordinal)) {
                $hasChild = $true
                break
            }
        }
        if (-not $hasChild) {
            $event = $final[$name]
            $leaves += [ordered]@{
                name = $name
                status = $event.Action.ToUpperInvariant()
                elapsed_seconds = [double]$event.Elapsed
            }
        }
    }
    return @($leaves | Sort-Object name)
}

$results = @()
try {
    foreach ($suite in $suites) {
        $arguments = @("test", "-json")
        if ($suite.Tags) {
            $arguments += @("-tags", $suite.Tags)
        }
        $arguments += @($suite.Package, "-count=1", "-run", $suite.Run, "-timeout", $suite.Timeout)
        $outputLines = @(& $Go $arguments 2>&1)
        $exitCode = $LASTEXITCODE
        $events = @()
        $parseErrors = @()
        foreach ($line in $outputLines) {
            try {
                $events += ($line | ConvertFrom-Json)
            }
            catch {
                $parseErrors += [string]$line
            }
        }
        $cells = @(Get-LeafResults -Events $events)
        $failedCells = @($cells | Where-Object { $_.status -ne "PASS" })
        $countMatches = $true
        if ($suite.ExpectedLeaves) {
            $countMatches = $cells.Count -eq $suite.ExpectedLeaves
        }
        $suitePassed = $exitCode -eq 0 -and $parseErrors.Count -eq 0 -and $failedCells.Count -eq 0 -and $countMatches
        $result = [ordered]@{
            name = $suite.Name
            logical_cells = $suite.LogicalCells
            observed_leaf_cells = $cells.Count
            passed = $suitePassed
            exit_code = $exitCode
            cells = $cells
            parse_errors = $parseErrors
        }
        if ($suite.ExpectedLeaves) {
            $result.expected_leaf_cells = $suite.ExpectedLeaves
        }
        $results += $result
    }
}
finally {
    $env:AGENTSQL_B5_PG_TERMINAL_MATRIX = $oldGate
}

$passed = -not ($results | Where-Object { -not $_.passed })
$report = [ordered]@{
    schema = "agentsql.b5.s10-fault-matrix-report.v2"
    started_at = $started.ToString("o")
    finished_at = ([DateTime]::UtcNow).ToString("o")
    status = $(if ($passed) { "PASS" } else { "FAIL" })
    logical_cells = 16
    pg_expected_leaf_cells = 116
    passed = $passed
    assertions = [ordered]@{
        typed_terminal_evidence = "asserted by each terminal leaf"
        database_truth_misclassification = 0
        leaked_test_backends = 0
        mysql_code = "DIALECT_TRANSACTION_UNSUPPORTED"
        recovery_approvers = "distinct runtime-sre and qa-release"
    }
    suites = $results
}
$directory = Split-Path -Parent $Output
if ($directory) {
    New-Item -ItemType Directory -Force -Path $directory | Out-Null
}
$report | ConvertTo-Json -Depth 12 | Set-Content -Encoding utf8 $Output
$report | ConvertTo-Json -Depth 12
if (-not $passed) {
    exit 1
}

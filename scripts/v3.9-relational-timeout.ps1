param(
    [Parameter(Mandatory = $true)]
    [ValidateSet('oracle', 'sqlserver')]
    [string]$Database,
    [string]$FixtureRoot = 'D:\CodexLS\tmp\agentsql-v390',
    [string]$OutputRoot = 'D:\CodexLS\outputs\agentsql-v390'
)

# Uses only the named, isolated v3.9 test containers. Never point this fixture
# at a customer database. Credentials are read from private env files.
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$repo = Split-Path -Parent $PSScriptRoot
$prefix = $Database.ToUpperInvariant()
if ($Database -eq 'oracle') {
    $business = 'oracle-v390'
    $envFile = Join-Path $FixtureRoot 'oracle-rotated-test.env'
    $certs = Join-Path $FixtureRoot 'oracle-client-certs'
    $testName = 'TestOracleHTTPMCPRealE2E'
} else {
    $business = 'mssql-v390'
    $envFile = Join-Path $FixtureRoot 'mssql-test.env'
    $certs = Join-Path $FixtureRoot 'mssql-client-certs'
    $testName = 'TestSQLServerHTTPMCPRealE2E'
}
$id = [guid]::NewGuid().ToString('N').Substring(0, 8)
$testContainer = "agentsql-v390-$Database-timeout-$id"
$signals = Join-Path $FixtureRoot "$Database-timeout-$id"
$log = Join-Path $OutputRoot "$Database-timeout-$id.log"
foreach ($path in @($envFile, $certs, $OutputRoot)) {
    if (-not (Test-Path -LiteralPath $path)) { throw "Missing fixture input: $path" }
}
New-Item -ItemType Directory -Path $signals | Out-Null
$paused = $false
function Wait-Signal([string]$name, [int]$seconds) {
    $deadline = (Get-Date).AddSeconds($seconds)
    while (-not (Test-Path -LiteralPath (Join-Path $signals $name))) {
        if ((& docker inspect $testContainer --format '{{.State.Running}}').Trim() -ne 'true') {
            throw "Test exited before signal $name; inspect $log"
        }
        if ((Get-Date) -ge $deadline) { throw "Timed out waiting for signal $name" }
        Start-Sleep -Milliseconds 500
    }
}
try {
    & docker run -d --name $testContainer --network agentsql-v390 --env-file $envFile `
        --env "AGENTSQL_TEST_${prefix}_BUSINESS_FAULT_DIR=/signals" `
        --env "AGENTSQL_TEST_${prefix}_EXPECT_TIMEOUT=1" `
        --env "AGENTSQL_TEST_${prefix}_PLAN_BUDGET=1" `
        --env GOTOOLCHAIN=local --env GOMAXPROCS=2 `
        --mount "type=bind,source=$repo,target=/src,readonly" `
        --mount "type=bind,source=$certs,target=/certs,readonly" `
        --mount "type=bind,source=$signals,target=/signals" `
        --mount 'type=bind,source=D:\CodexLS\cache\go-mod,target=/go/pkg/mod' `
        --mount 'type=bind,source=D:\CodexLS\cache\go-build,target=/root/.cache/go-build' `
        --workdir /src golang:1.26-bookworm go test -v -count=1 -timeout 15m `
        -run "^$testName`$" ./internal/mcpserver | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Could not start timeout test' }
    Wait-Signal 'ready' 360
    & docker pause $business | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Could not pause isolated business database' }
    $paused = $true
    $started = Get-Date
    'paused' | Set-Content -LiteralPath (Join-Path $signals 'down')
    Wait-Signal 'down_checked' 45
    Write-Output ("{0}_TIMEOUT_DENIED_SECONDS={1:N2}" -f $prefix, ((Get-Date) - $started).TotalSeconds)
    & docker unpause $business | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Could not resume isolated business database' }
    $paused = $false
    'resumed' | Set-Content -LiteralPath (Join-Path $signals 'up')
    $deadline = (Get-Date).AddMinutes(4)
    while ((& docker inspect $testContainer --format '{{.State.Running}}').Trim() -eq 'true') {
        if ((Get-Date) -ge $deadline) { throw 'Timeout/recovery test did not finish' }
        Start-Sleep -Seconds 1
    }
    $exitCode = (& docker inspect $testContainer --format '{{.State.ExitCode}}').Trim()
    if ($exitCode -ne '0') { throw "Timeout/plan test exited $exitCode; inspect $log" }
    Write-Output "V390_${prefix}_TIMEOUT_PLAN_RECOVERY=PASS"
} finally {
    if ($paused) { & docker unpause $business | Out-Null }
    & docker logs $testContainer *> $log
    & docker rm -f $testContainer 2>$null | Out-Null
}

param(
    [string]$FixtureRoot = 'D:\CodexLS\tmp\agentsql-v270',
    [string]$OutputRoot = 'D:\CodexLS\outputs\agentsql-v270',
    [string]$Network = 'agentsql-v270-net',
    [string]$BusinessContainer = 'agentsql-v270-clickhouse'
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$repo = Split-Path -Parent $PSScriptRoot
$moduleCache = 'D:\CodexLS\cache\go-mod'
$certs = Join-Path $FixtureRoot 'fixture\certs'
foreach ($path in @($FixtureRoot, $OutputRoot, $moduleCache, $certs, (Join-Path $FixtureRoot 'test.env'))) {
    if (-not (Test-Path -LiteralPath $path)) { throw "Missing fault-test input: $path" }
}
$suffix = [guid]::NewGuid().ToString('N').Substring(0, 10)
$signalDir = Join-Path $FixtureRoot "fault-$suffix"
New-Item -ItemType Directory -Path $signalDir | Out-Null
$auditContainer = "agentsql-v270-audit-$suffix"
$testContainer = "agentsql-v270-fault-$suffix"
$auditEnv = Join-Path $signalDir 'audit.env'
$testEnv = Join-Path $signalDir 'test.env'
$auditStopped = $false
$businessStopped = $false

function Wait-Signal([string]$Name, [int]$Seconds) {
    $path = Join-Path $signalDir $Name
    $deadline = (Get-Date).AddSeconds($Seconds)
    while (-not (Test-Path -LiteralPath $path)) {
        if ((& docker inspect $testContainer --format '{{.State.Running}}').Trim() -ne 'true') {
            & docker logs $testContainer *> (Join-Path $OutputRoot 'clickhouse-fault-failed.log')
            Get-Content -LiteralPath (Join-Path $OutputRoot 'clickhouse-fault-failed.log') -Tail 40
            throw "ClickHouse fault test exited before $Name"
        }
        if ((Get-Date) -ge $deadline) { throw "Timed out waiting for $Name" }
        Start-Sleep -Seconds 1
    }
}

function Wait-Audit([int]$Seconds) {
    $deadline = (Get-Date).AddSeconds($Seconds)
    while ((Get-Date) -lt $deadline) {
        & docker exec $auditContainer pg_isready -U postgres -d postgres *> $null
        if ($LASTEXITCODE -eq 0) { return }
        Start-Sleep -Seconds 2
    }
    throw 'Audit database did not recover'
}

function Wait-Business([int]$Seconds, [string]$ReaderPassword) {
    $deadline = (Get-Date).AddSeconds($Seconds)
    while ((Get-Date) -lt $deadline) {
        $response = & docker exec -e "CLICKHOUSE_PASSWORD=$ReaderPassword" $BusinessContainer `
            clickhouse-client --port 9000 --user agent_reader --database analytics --query 'SELECT 1' 2>$null
        if ($LASTEXITCODE -eq 0 -and ($response -join '').Trim() -eq '1') { return }
        Start-Sleep -Seconds 2
    }
    throw 'ClickHouse did not recover'
}

try {
    $auditPassword = [Convert]::ToHexString([Security.Cryptography.RandomNumberGenerator]::GetBytes(24)).ToLowerInvariant()
    [IO.File]::WriteAllLines($auditEnv, [string[]]@("POSTGRES_PASSWORD=$auditPassword"), [Text.UTF8Encoding]::new($false))
    $dsn = 'postgres://postgres:' + [uri]::EscapeDataString($auditPassword) + '@' + $auditContainer + ':5432/postgres?sslmode=disable'
    $lines = [string[]]@(Get-Content -LiteralPath (Join-Path $FixtureRoot 'test.env'))
    $lines += 'AGENTSQL_TEST_CLICKHOUSE_AUDIT_DSN=' + $dsn
    $lines += 'AGENTSQL_TEST_CLICKHOUSE_FAULT_DIR=/signals'
    [IO.File]::WriteAllLines($testEnv, $lines, [Text.UTF8Encoding]::new($false))
    $auditPassword = $null
    $dsn = $null
    $readerPassword = ((Get-Content -LiteralPath (Join-Path $FixtureRoot 'fixture\reader.env')) |
        Where-Object { $_ -like 'CLICKHOUSE_READER_PASSWORD=*' }) -replace '^CLICKHOUSE_READER_PASSWORD=', ''
    if ([string]::IsNullOrEmpty($readerPassword)) { throw 'Reader password fixture is missing' }

    & docker run -d --name $auditContainer --network $Network --env-file $auditEnv postgres:16-alpine | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Could not start isolated audit database' }
    Wait-Audit 90
    & docker run -d --name $testContainer --network $Network --env-file $testEnv `
        --mount "type=bind,source=$repo,target=/src,readonly" `
        --mount "type=bind,source=$certs,target=/fixture/certs,readonly" `
        --mount "type=bind,source=$signalDir,target=/signals" `
        --mount "type=bind,source=$moduleCache,target=/gomod" `
        --mount 'type=volume,source=agentsql-go-build-cache,target=/buildcache' `
        --workdir /src --env GOTOOLCHAIN=local --env GOPROXY=off `
        --env GOMODCACHE=/gomod --env GOCACHE=/buildcache --env CGO_ENABLED=1 `
        golang:1.26.8 go test -timeout 12m -count=1 -v -run '^TestClickHouseControlledRealHTTPMCP$' ./internal/mcpserver | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Could not start ClickHouse fault test container' }

    Wait-Signal 'audit_ready' 300
    & docker stop $auditContainer | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Could not stop isolated audit database' }
    $auditStopped = $true
    Set-Content -LiteralPath (Join-Path $signalDir 'audit_down') -Value 'stopped'
    Wait-Signal 'audit_down_checked' 120
    & docker start $auditContainer | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Could not restart isolated audit database' }
    $auditStopped = $false
    Wait-Audit 90
    Set-Content -LiteralPath (Join-Path $signalDir 'audit_up') -Value 'recovered'

    Wait-Signal 'clickhouse_ready' 120
    & docker stop $BusinessContainer | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Could not stop isolated ClickHouse' }
    $businessStopped = $true
    Set-Content -LiteralPath (Join-Path $signalDir 'clickhouse_down') -Value 'stopped'
    Wait-Signal 'clickhouse_down_checked' 120
    & docker start $BusinessContainer | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Could not restart isolated ClickHouse' }
    $businessStopped = $false
    Wait-Business 120 $readerPassword
    Set-Content -LiteralPath (Join-Path $signalDir 'clickhouse_up') -Value 'recovered'
    $readerPassword = $null

    $deadline = (Get-Date).AddMinutes(3)
    while ((& docker inspect $testContainer --format '{{.State.Running}}').Trim() -eq 'true') {
        if ((Get-Date) -ge $deadline) { throw 'ClickHouse fault test did not finish' }
        Start-Sleep -Seconds 1
    }
    $exitCode = (& docker inspect $testContainer --format '{{.State.ExitCode}}').Trim()
    & docker logs $testContainer *> (Join-Path $OutputRoot 'clickhouse-fault-recovery.log')
    Get-Content -LiteralPath (Join-Path $OutputRoot 'clickhouse-fault-recovery.log') -Tail 20
    if ($exitCode -ne '0') { throw "ClickHouse fault test failed, exit=$exitCode" }
    Write-Output 'V270_CLICKHOUSE_AUDIT_BUSINESS_FAULT_RECOVERY=PASS'
}
finally {
    if ($businessStopped) { & docker start $BusinessContainer | Out-Null }
    if ($auditStopped) { & docker start $auditContainer | Out-Null }
    & docker rm -f $testContainer $auditContainer 2>$null | Out-Null
    Remove-Item -LiteralPath $testEnv, $auditEnv -Force -ErrorAction SilentlyContinue
}

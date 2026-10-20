param(
    [string]$FixtureRoot = 'D:\CodexLS\tmp\agentsql-v260',
    [string]$OutputRoot = 'D:\CodexLS\outputs\agentsql-v260',
    [string]$Network = 'agentsql-v260-net'
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$repo = Split-Path -Parent $PSScriptRoot
$moduleCache = 'D:\CodexLS\cache\go-mod'
$buildCache = 'D:\CodexLS\cache\go-build-linux'
foreach ($path in @($FixtureRoot, $OutputRoot, $moduleCache, $buildCache)) {
    if (-not (Test-Path -LiteralPath $path)) { throw "Missing fault-test input: $path" }
}
$suffix = [guid]::NewGuid().ToString('N').Substring(0, 10)
$signalDir = Join-Path $FixtureRoot "fault-$suffix"
New-Item -ItemType Directory -Path $signalDir | Out-Null
$auditContainer = "agentsql-v260-audit-$suffix"
$testContainer = "agentsql-v260-fault-$suffix"
$auditEnv = Join-Path $signalDir 'audit.env'
$testEnv = Join-Path $signalDir 'test.env'
$auditStopped = $false
$businessStopped = ''

function Wait-Signal([string]$Name, [int]$Seconds) {
    $signalPath = Join-Path $signalDir $Name
    $deadline = (Get-Date).AddSeconds($Seconds)
    while (-not (Test-Path -LiteralPath $signalPath)) {
        $running = (& docker inspect $testContainer --format '{{.State.Running}}').Trim()
        if ($running -ne 'true') { throw "Key fault test exited before $Name" }
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

function Wait-Business([string]$Container, [string]$Client, [string]$Reader, [int]$Seconds) {
    $deadline = (Get-Date).AddSeconds($Seconds)
    while ((Get-Date) -lt $deadline) {
        $reply = & docker exec -e "REDISCLI_AUTH=$Reader" $Container $Client --tls --cacert /fixture/certs/ca.crt --user agent_reader PING 2>$null
        if ($LASTEXITCODE -eq 0 -and ($reply -join '').Trim() -eq 'PONG') { return }
        Start-Sleep -Seconds 2
    }
    throw "$Container did not recover"
}

try {
    $auditPassword = [Convert]::ToHexString([Security.Cryptography.RandomNumberGenerator]::GetBytes(24)).ToLowerInvariant()
    [IO.File]::WriteAllLines($auditEnv, [string[]]@("POSTGRES_PASSWORD=$auditPassword"), [Text.UTF8Encoding]::new($false))
    $dsn = 'postgres://postgres:' + [uri]::EscapeDataString($auditPassword) + '@' + $auditContainer + ':5432/postgres?sslmode=disable'
    $testLines = [string[]]@(Get-Content -LiteralPath (Join-Path $FixtureRoot 'test.env'))
    $testLines += 'AGENTSQL_TEST_KEY_AUDIT_DSN=' + $dsn
    $testLines += 'AGENTSQL_TEST_KEY_FAULT_DIR=/signals'
    [IO.File]::WriteAllLines($testEnv, $testLines, [Text.UTF8Encoding]::new($false))
    $auditPassword = $null
    $dsn = $null
    $reader = ((Get-Content -LiteralPath (Join-Path $FixtureRoot 'secrets.env')) | Where-Object { $_ -like 'READER_PASSWORD=*' }) -replace '^READER_PASSWORD=', ''

    & docker run -d --name $auditContainer --network $Network --env-file $auditEnv postgres:16-alpine | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Could not start isolated audit database' }
    Wait-Audit 90

    & docker run -d --name $testContainer --network $Network --env-file $testEnv `
        --mount "type=bind,source=$repo,target=/src,readonly" `
        --mount "type=bind,source=$FixtureRoot,target=/fixture,readonly" `
        --mount "type=bind,source=$signalDir,target=/signals" `
        --mount "type=bind,source=$moduleCache,target=/gomod" `
        --mount "type=bind,source=$buildCache,target=/gocache" `
        --workdir /src --env GOTOOLCHAIN=local --env GOPROXY=off `
        --env GOMODCACHE=/gomod --env GOCACHE=/gocache `
        golang:1.26.8 go test -timeout 12m -count=1 -v -run '^TestRedisValkeyControlledRealHTTPMCP$' ./internal/mcpserver | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Could not start key fault test container' }

    foreach ($dialect in @('redis', 'valkey')) {
        $business = if ($dialect -eq 'redis') { 'agentsql-v260-redis' } else { 'agentsql-v260-valkey' }
        $client = if ($dialect -eq 'redis') { 'redis-cli' } else { 'valkey-cli' }
        Wait-Signal "${dialect}_audit_ready" 300
        & docker stop $auditContainer | Out-Null
        if ($LASTEXITCODE -ne 0) { throw 'Could not stop isolated audit database' }
        $auditStopped = $true
        Set-Content -LiteralPath (Join-Path $signalDir "${dialect}_audit_down") -Value 'stopped'
        Wait-Signal "${dialect}_audit_down_checked" 120
        & docker start $auditContainer | Out-Null
        if ($LASTEXITCODE -ne 0) { throw 'Could not restart isolated audit database' }
        $auditStopped = $false
        Wait-Audit 90
        Set-Content -LiteralPath (Join-Path $signalDir "${dialect}_audit_up") -Value 'recovered'

        Wait-Signal "${dialect}_business_ready" 120
        & docker stop $business | Out-Null
        if ($LASTEXITCODE -ne 0) { throw "Could not stop $business" }
        $businessStopped = $business
        Set-Content -LiteralPath (Join-Path $signalDir "${dialect}_business_down") -Value 'stopped'
        Wait-Signal "${dialect}_business_down_checked" 120
        & docker start $business | Out-Null
        if ($LASTEXITCODE -ne 0) { throw "Could not restart $business" }
        $businessStopped = ''
        Wait-Business $business $client $reader 90
        Set-Content -LiteralPath (Join-Path $signalDir "${dialect}_business_up") -Value 'recovered'
    }
    $deadline = (Get-Date).AddMinutes(3)
    while ((& docker inspect $testContainer --format '{{.State.Running}}').Trim() -eq 'true') {
        if ((Get-Date) -ge $deadline) { throw 'Key fault test did not finish' }
        Start-Sleep -Seconds 1
    }
    $exitCode = (& docker inspect $testContainer --format '{{.State.ExitCode}}').Trim()
    & docker logs $testContainer *> (Join-Path $OutputRoot 'key-fault-recovery.log')
    Get-Content -LiteralPath (Join-Path $OutputRoot 'key-fault-recovery.log') -Tail 35
    if ($exitCode -ne '0') { throw "Key fault test failed, exit=$exitCode" }
    Write-Output 'V260_REDIS_VALKEY_AUDIT_BUSINESS_FAULT_RECOVERY=PASS'
}
finally {
    if ($businessStopped -ne '') { & docker start $businessStopped | Out-Null }
    if ($auditStopped) { & docker start $auditContainer | Out-Null }
    & docker rm -f $testContainer $auditContainer 2>$null | Out-Null
    Remove-Item -LiteralPath $testEnv, $auditEnv -Force -ErrorAction SilentlyContinue
}

param(
    [string]$FixtureRoot = 'D:\CodexLS\tmp\agentsql-v280',
    [string]$OutputRoot = 'D:\CodexLS\outputs\agentsql-v280',
    [string]$Network = 'agentsql-v280_default',
    [string]$BusinessContainer = 'agentsql-v280-milvus'
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$repo = Split-Path -Parent $PSScriptRoot
$certs = Join-Path $FixtureRoot 'certs'
$passwordFile = Join-Path $FixtureRoot 'reader-password.txt'
$moduleCache = 'D:\CodexLS\cache\go\mod'
$buildCache = 'D:\CodexLS\cache\go\build-linux'
foreach ($path in @($FixtureRoot, $OutputRoot, $certs, $passwordFile, $moduleCache, $buildCache)) {
    if (-not (Test-Path -LiteralPath $path)) { throw "Missing Milvus fault-test input: $path" }
}
$suffix = [guid]::NewGuid().ToString('N').Substring(0, 10)
$signalDir = Join-Path $FixtureRoot "fault-$suffix"
New-Item -ItemType Directory -Path $signalDir | Out-Null
$auditContainer = "agentsql-v280-audit-$suffix"
$testContainer = "agentsql-v280-fault-$suffix"
$auditStopped = $false
$businessStopped = $false

function Wait-Signal([string]$name, [int]$seconds) {
    $target = Join-Path $signalDir $name
    $deadline = (Get-Date).AddSeconds($seconds)
    while (-not (Test-Path -LiteralPath $target)) {
        if ((& docker inspect $testContainer --format '{{.State.Running}}').Trim() -ne 'true') {
            & docker logs $testContainer *> (Join-Path $OutputRoot 'milvus-fault-failed.log')
            Get-Content -LiteralPath (Join-Path $OutputRoot 'milvus-fault-failed.log') -Tail 35
            throw "Milvus fault test exited before $name"
        }
        if ((Get-Date) -ge $deadline) { throw "Timed out waiting for $name" }
        Start-Sleep -Seconds 1
    }
}

function Wait-Audit([int]$seconds) {
    $deadline = (Get-Date).AddSeconds($seconds)
    while ((Get-Date) -lt $deadline) {
        & docker exec $auditContainer pg_isready -U postgres -d postgres *> $null
        if ($LASTEXITCODE -eq 0) { return }
        Start-Sleep -Seconds 2
    }
    throw 'Audit database did not recover'
}

function Wait-Milvus([int]$seconds) {
    $deadline = (Get-Date).AddSeconds($seconds)
    while ((Get-Date) -lt $deadline) {
        $health = (& docker inspect $BusinessContainer --format '{{.State.Health.Status}}').Trim()
        if ($health -eq 'healthy') { return }
        Start-Sleep -Seconds 3
    }
    throw 'Milvus did not recover to healthy'
}

try {
    $auditPassword = [Convert]::ToHexString([Security.Cryptography.RandomNumberGenerator]::GetBytes(24)).ToLowerInvariant()
    $dsn = 'postgres://postgres:' + [uri]::EscapeDataString($auditPassword) + '@' + $auditContainer + ':5432/postgres?sslmode=disable'
    & docker run -d --name $auditContainer --network $Network -e "POSTGRES_PASSWORD=$auditPassword" postgres:16-alpine | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Could not start isolated audit database' }
    Wait-Audit 90
    & docker run -d --name $testContainer --network $Network `
        -v "${repo}:/src:ro" -v "${certs}:/fixtures/certs:ro" -v "${passwordFile}:/fixtures/password.txt:ro" `
        -v "${signalDir}:/signals" -v "${moduleCache}:/go/pkg/mod" -v "${buildCache}:/root/.cache/go-build" `
        -w /src -e GOTOOLCHAIN=local -e AGENTSQL_TEST_MILVUS_HOST=agentsql-v280-milvus `
        -e AGENTSQL_TEST_MILVUS_CA=/fixtures/certs/ca.pem -e AGENTSQL_TEST_MILVUS_PASSWORD_FILE=/fixtures/password.txt `
        -e "AGENTSQL_TEST_MILVUS_AUDIT_DSN=$dsn" -e AGENTSQL_TEST_MILVUS_FAULT_DIR=/signals `
        golang:1.26.0 go test -timeout 12m -count=1 -v -run '^TestMilvusControlledRealHTTPMCP$' ./internal/mcpserver | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Could not start Milvus fault test container' }
    $auditPassword = $null
    $dsn = $null

    Wait-Signal 'audit_ready' 300
    & docker stop $auditContainer | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Could not stop isolated audit database' }
    $auditStopped = $true
    Set-Content -LiteralPath (Join-Path $signalDir 'audit_down') -Value 'stopped'
    Wait-Signal 'audit_down_checked' 120
    & docker start $auditContainer | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Could not restart audit database' }
    $auditStopped = $false
    Wait-Audit 90
    Set-Content -LiteralPath (Join-Path $signalDir 'audit_up') -Value 'recovered'

    Wait-Signal 'milvus_ready' 120
    & docker stop $BusinessContainer | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Could not stop Milvus' }
    $businessStopped = $true
    Set-Content -LiteralPath (Join-Path $signalDir 'milvus_down') -Value 'stopped'
    Wait-Signal 'milvus_down_checked' 120
    & docker start $BusinessContainer | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Could not restart Milvus' }
    $businessStopped = $false
    Wait-Milvus 180
    Set-Content -LiteralPath (Join-Path $signalDir 'milvus_up') -Value 'recovered'

    $deadline = (Get-Date).AddMinutes(3)
    while ((& docker inspect $testContainer --format '{{.State.Running}}').Trim() -eq 'true') {
        if ((Get-Date) -ge $deadline) { throw 'Milvus fault test did not finish' }
        Start-Sleep -Seconds 1
    }
    $exitCode = (& docker inspect $testContainer --format '{{.State.ExitCode}}').Trim()
    & docker logs $testContainer *> (Join-Path $OutputRoot 'milvus-fault-recovery.log')
    Get-Content -LiteralPath (Join-Path $OutputRoot 'milvus-fault-recovery.log') -Tail 20
    if ($exitCode -ne '0') { throw "Milvus fault test failed, exit=$exitCode" }
    Write-Output 'V280_MILVUS_AUDIT_BUSINESS_FAULT_RECOVERY=PASS'
}
finally {
    if ($businessStopped) { & docker start $BusinessContainer | Out-Null }
    if ($auditStopped) { & docker start $auditContainer | Out-Null }
    & docker rm -f $testContainer $auditContainer 2>$null | Out-Null
}

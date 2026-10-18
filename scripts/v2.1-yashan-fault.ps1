param(
    [ValidateSet('audit', 'business')][string]$Target = 'audit',
    [string]$YashanContainer = 'yashan-v06',
    [string]$AuditContainer = 'agentsql-v210-audit',
    [string]$TempRoot = 'D:\CodexLS\tmp\agentsql-v210',
    [string]$ClientLibDir = 'D:\CodexLS\tmp\agentsql-v200\official-smoke\agentsql-v2.0.0-linux-amd64\lib\yashandb'
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$repo = Split-Path -Parent $PSScriptRoot
$moduleCache = 'D:\CodexLS\cache\agentsql-go-mod-linux'
$buildCache = 'D:\CodexLS\cache\agentsql-go-build-linux'
$clientLib = $ClientLibDir
foreach ($path in @($repo, $moduleCache, $buildCache, $clientLib)) {
    if (-not (Test-Path -LiteralPath $path -PathType Container)) { throw "Required local directory is missing: $path" }
}
foreach ($container in @($YashanContainer, $AuditContainer)) {
    if ((& docker inspect $container --format '{{.State.Running}}').Trim() -ne 'true') {
        throw "Required test container is not running: $container"
    }
}
New-Item -ItemType Directory -Path $TempRoot -Force | Out-Null
$faultDir = Join-Path $TempRoot ($Target + '-fault-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $faultDir | Out-Null
$dbName = 'agv21_' + [guid]::NewGuid().ToString('N').Substring(0, 10)
$testContainer = 'agentsql-v210-' + $Target + '-fault-' + [guid]::NewGuid().ToString('N').Substring(0, 8)
$env:YASHAN_PASSWORD = & docker exec $YashanContainer sh -c 'printf %s "$SYS_PASSWD"'
$auditPassword = & docker exec $AuditContainer sh -c 'printf %s "$POSTGRES_PASSWORD"'
if ([string]::IsNullOrWhiteSpace($env:YASHAN_PASSWORD) -or [string]::IsNullOrWhiteSpace($auditPassword)) {
    throw 'Test database credential is unavailable'
}
$env:AGENTSQL_TEST_YASHAN_AUDIT_DSN = "postgres://postgres:${auditPassword}@host.docker.internal:25432/${dbName}?sslmode=disable"
$faultContainer = if ($Target -eq 'audit') { $AuditContainer } else { $YashanContainer }
$faultVariable = if ($Target -eq 'audit') { 'AGENTSQL_TEST_YASHAN_AUDIT_FAULT_DIR=/fault' } else { 'AGENTSQL_TEST_YASHAN_BUSINESS_FAULT_DIR=/fault' }
$faultStopped = $false
try {
    & docker exec $AuditContainer psql -U postgres -d postgres -v ON_ERROR_STOP=1 -c "CREATE DATABASE $dbName" | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Could not create isolated audit test database' }
    & docker run -d --name $testContainer `
        --mount "type=bind,source=$repo,target=/src" `
        --mount "type=bind,source=$moduleCache,target=/gomod" `
        --mount "type=bind,source=$buildCache,target=/buildcache" `
        --mount "type=bind,source=$clientLib,target=/client/lib,readonly" `
        --mount "type=bind,source=$faultDir,target=/fault" `
        --workdir /src --env GOPROXY=off --env GOTOOLCHAIN=local `
        --env GOMODCACHE=/gomod --env GOCACHE=/buildcache --env CGO_ENABLED=1 `
        --env LD_LIBRARY_PATH=/client/lib --env AGENTSQL_YASHAN_E2E=1 `
        --env YASHAN_PASSWORD --env YASHAN_HOST=host.docker.internal `
        --env AGENTSQL_TEST_YASHAN_AUDIT_DSN --env $faultVariable `
        golang:1.26-bookworm go test -tags yashan -timeout 10m -count=1 -v `
        -run '^TestYashanHTTPMCPRealE2E$' ./internal/mcpserver | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Could not start audit fault MCP test' }
    $deadline = (Get-Date).AddMinutes(8)
    while (-not (Test-Path -LiteralPath (Join-Path $faultDir 'ready'))) {
        if ((& docker inspect $testContainer --format '{{.State.Running}}').Trim() -ne 'true') {
            & docker logs $testContainer
            throw 'Audit fault test exited before ready signal'
        }
        if ((Get-Date) -ge $deadline) { throw 'Timed out waiting for audit fault test' }
        Start-Sleep -Seconds 1
    }
    & docker stop $faultContainer | Out-Null
    if ($LASTEXITCODE -ne 0) { throw "Could not stop $Target database" }
    $faultStopped = $true
    Set-Content -LiteralPath (Join-Path $faultDir 'down') -Value 'stopped'
    $deadline = (Get-Date).AddMinutes(2)
    while (-not (Test-Path -LiteralPath (Join-Path $faultDir 'down_checked'))) {
        if ((& docker inspect $testContainer --format '{{.State.Running}}').Trim() -ne 'true') {
            & docker logs $testContainer
            throw 'MCP test exited during audit outage'
        }
        if ((Get-Date) -ge $deadline) { throw 'Timed out waiting for fail-closed audit check' }
        Start-Sleep -Seconds 1
    }
    & docker start $faultContainer | Out-Null
    if ($LASTEXITCODE -ne 0) { throw "Could not restart $Target database" }
    $faultStopped = $false
    $ready = $false
    for ($i = 0; $i -lt 30; $i++) {
        if ($Target -eq 'audit') {
            & docker exec $AuditContainer pg_isready -U postgres -d $dbName *> $null
            if ($LASTEXITCODE -eq 0) { $ready = $true; break }
        } else {
            $port = Test-NetConnection 127.0.0.1 -Port 1688 -WarningAction SilentlyContinue
            if ($port.TcpTestSucceeded) { $ready = $true; break }
        }
        Start-Sleep -Seconds 1
    }
    if (-not $ready) { throw "$Target database did not recover" }
    Set-Content -LiteralPath (Join-Path $faultDir 'up') -Value 'recovered'
    $deadline = (Get-Date).AddMinutes(2)
    while ((& docker inspect $testContainer --format '{{.State.Running}}').Trim() -eq 'true') {
        if ((Get-Date) -ge $deadline) { throw 'Audit fault MCP test did not finish' }
        Start-Sleep -Seconds 1
    }
    $testExit = (& docker inspect $testContainer --format '{{.State.ExitCode}}').Trim()
    & docker logs $testContainer
    if ($testExit -ne '0') { throw "Audit fault MCP test failed with exit=$testExit" }
    Write-Output "V210_YASHAN_${Target}_FAULT=PASS"
}
finally {
    if ($faultStopped) { & docker start $faultContainer | Out-Null }
    Remove-Item Env:\YASHAN_PASSWORD,Env:\AGENTSQL_TEST_YASHAN_AUDIT_DSN -ErrorAction SilentlyContinue
    & docker rm -f $testContainer 2>$null | Out-Null
}

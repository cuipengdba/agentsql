param(
    [ValidateSet('audit', 'business')][string]$Target = 'audit',
    [string]$OracleContainer = 'oracle-v06',
    [string]$AuditContainer = 'agentsql-v210-audit',
    [string]$TempRoot = 'D:\CodexLS\tmp\agentsql-v220'
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$repo = Split-Path -Parent $PSScriptRoot
$moduleCache = 'D:\CodexLS\cache\agentsql-go-mod-linux'
$buildCache = 'D:\CodexLS\cache\agentsql-go-build-linux'
foreach ($path in @($repo, $moduleCache, $buildCache)) {
    if (-not (Test-Path -LiteralPath $path -PathType Container)) { throw "Required directory is missing: $path" }
}
if ((& docker inspect $OracleContainer --format '{{.State.Health.Status}}').Trim() -ne 'healthy') {
    throw 'Oracle test container is not healthy'
}
if ((& docker inspect $AuditContainer --format '{{.State.Running}}').Trim() -ne 'true') {
    throw 'Independent PostgreSQL audit container is not running'
}
New-Item -ItemType Directory -Path $TempRoot -Force | Out-Null
$faultDir = Join-Path $TempRoot ($Target + '-fault-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $faultDir | Out-Null
$dbName = 'agv22_' + [guid]::NewGuid().ToString('N').Substring(0, 10)
$testContainer = 'agentsql-v220-' + $Target + '-' + [guid]::NewGuid().ToString('N').Substring(0, 8)
$oraclePasswordLine = & docker inspect $OracleContainer --format '{{range .Config.Env}}{{println .}}{{end}}' |
    Where-Object { $_ -like 'ORACLE_PWD=*' } | Select-Object -First 1
$auditPasswordLine = & docker inspect $AuditContainer --format '{{range .Config.Env}}{{println .}}{{end}}' |
    Where-Object { $_ -like 'POSTGRES_PASSWORD=*' } | Select-Object -First 1
if (-not $oraclePasswordLine -or -not $auditPasswordLine) { throw 'Test database credential is unavailable' }
$env:ORACLE_PASSWORD = $oraclePasswordLine.Substring('ORACLE_PWD='.Length)
$auditPassword = $auditPasswordLine.Substring('POSTGRES_PASSWORD='.Length)
$env:AGENTSQL_TEST_ORACLE_AUDIT_DSN = "postgres://postgres:${auditPassword}@host.docker.internal:25432/${dbName}?sslmode=disable"
$faultContainer = if ($Target -eq 'audit') { $AuditContainer } else { $OracleContainer }
$faultEnv = if ($Target -eq 'audit') {
    'AGENTSQL_TEST_ORACLE_AUDIT_FAULT_DIR=/fault'
} else {
    'AGENTSQL_TEST_ORACLE_BUSINESS_FAULT_DIR=/fault'
}
$network = if ($Target -eq 'audit') { "container:$OracleContainer" } else { 'bridge' }
$oracleHost = if ($Target -eq 'audit') { '127.0.0.1' } else {
    $inspect = & docker inspect $OracleContainer | ConvertFrom-Json
    [string]$inspect[0].NetworkSettings.Networks.bridge.IPAddress
}
if ([string]::IsNullOrWhiteSpace($oracleHost)) { throw 'Oracle test network address is unavailable' }
$faultStopped = $false
$createdDB = $false
try {
    & docker exec $AuditContainer psql -U postgres -d postgres -v ON_ERROR_STOP=1 -c "CREATE DATABASE $dbName" | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Could not create isolated audit database' }
    $createdDB = $true
    & docker run -d --name $testContainer --network $network `
        --mount "type=bind,source=$repo,target=/src" `
        --mount "type=bind,source=$moduleCache,target=/gomod" `
        --mount "type=bind,source=$buildCache,target=/buildcache" `
        --mount "type=bind,source=$faultDir,target=/fault" `
        --workdir /src --env GOPROXY=off --env GOTOOLCHAIN=local `
        --env GOMODCACHE=/gomod --env GOCACHE=/buildcache `
        --env AGENTSQL_ORACLE_E2E=1 --env ORACLE_PASSWORD --env "ORACLE_HOST=$oracleHost" `
        --env AGENTSQL_TEST_ORACLE_AUDIT_DSN --env $faultEnv `
        golang:1.26-bookworm /usr/local/go/bin/go test -timeout 17m -count=1 -v `
        -run '^TestOracleHTTPMCPRealE2E$' ./internal/mcpserver | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Could not start Oracle fault test container' }
    $deadline = (Get-Date).AddMinutes(8)
    while (-not (Test-Path -LiteralPath (Join-Path $faultDir 'ready'))) {
        if ((& docker inspect $testContainer --format '{{.State.Running}}').Trim() -ne 'true') {
            & docker logs $testContainer
            throw 'Oracle fault test exited before ready signal'
        }
        if ((Get-Date) -ge $deadline) { throw 'Timed out waiting for Oracle fault test' }
        Start-Sleep -Seconds 1
    }
    & docker stop $faultContainer | Out-Null
    if ($LASTEXITCODE -ne 0) { throw "Could not stop $Target test database" }
    $faultStopped = $true
    Set-Content -LiteralPath (Join-Path $faultDir 'down') -Value 'stopped'
    $deadline = (Get-Date).AddMinutes(2)
    while (-not (Test-Path -LiteralPath (Join-Path $faultDir 'down_checked'))) {
        if ((& docker inspect $testContainer --format '{{.State.Running}}').Trim() -ne 'true') {
            & docker logs $testContainer
            throw 'Oracle MCP test exited during database outage'
        }
        if ((Get-Date) -ge $deadline) { throw 'Timed out waiting for fail-closed check' }
        Start-Sleep -Seconds 1
    }
    & docker start $faultContainer | Out-Null
    if ($LASTEXITCODE -ne 0) { throw "Could not restart $Target test database" }
    $faultStopped = $false
    $ready = $false
    for ($i = 0; $i -lt 120; $i++) {
        if ($Target -eq 'audit') {
            & docker exec $AuditContainer pg_isready -U postgres -d $dbName *> $null
            $ready = $LASTEXITCODE -eq 0
        } else {
            $ready = (& docker inspect $OracleContainer --format '{{.State.Health.Status}}').Trim() -eq 'healthy'
        }
        if ($ready) { break }
        Start-Sleep -Seconds 5
    }
    if (-not $ready) { throw "$Target test database did not recover" }
    Set-Content -LiteralPath (Join-Path $faultDir 'up') -Value 'recovered'
    $deadline = (Get-Date).AddMinutes(3)
    while ((& docker inspect $testContainer --format '{{.State.Running}}').Trim() -eq 'true') {
        if ((Get-Date) -ge $deadline) { throw 'Oracle MCP fault test did not finish' }
        Start-Sleep -Seconds 1
    }
    $testExit = (& docker inspect $testContainer --format '{{.State.ExitCode}}').Trim()
    & docker logs $testContainer
    if ($testExit -ne '0') { throw "Oracle MCP fault test failed with exit=$testExit" }
    Write-Output "V220_ORACLE_${Target}_FAULT=PASS"
}
finally {
    if ($faultStopped) { & docker start $faultContainer | Out-Null }
    if ($createdDB -and $Target -eq 'audit' -and
        (& docker inspect $AuditContainer --format '{{.State.Running}}').Trim() -ne 'true') {
        & docker start $AuditContainer | Out-Null
    }
    if ($createdDB) {
        & docker exec $AuditContainer psql -U postgres -d postgres -v ON_ERROR_STOP=1 -c "DROP DATABASE IF EXISTS $dbName" *> $null
    }
    Remove-Item Env:\ORACLE_PASSWORD,Env:\AGENTSQL_TEST_ORACLE_AUDIT_DSN -ErrorAction SilentlyContinue
    & docker rm -f $testContainer 2>$null | Out-Null
}

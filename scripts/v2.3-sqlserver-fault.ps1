param(
    [ValidateSet('audit', 'business')][string]$Target = 'audit',
    [string]$SQLServerContainer = 'agentsql-v230-mssql',
    [string]$AuditContainer = 'agentsql-v210-audit',
    [string]$TempRoot = 'D:\CodexLS\tmp\agentsql-v230',
    [string]$EnvironmentFile = 'D:\CodexLS\tmp\agentsql-v230\mssql\mcp-test.env',
    [string]$CAFile = 'D:\CodexLS\tmp\agentsql-v230\mssql\ca.pem'
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$repo = Split-Path -Parent $PSScriptRoot
$moduleCache = 'D:\CodexLS\cache\agentsql-go-mod-linux'
$buildCache = 'D:\CodexLS\cache\agentsql-go-build-linux'
foreach ($path in @($repo, $TempRoot, $moduleCache, $buildCache)) {
    if (-not (Test-Path -LiteralPath $path -PathType Container)) { throw "Required directory is missing: $path" }
}
foreach ($path in @($EnvironmentFile, $CAFile)) {
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) { throw "Required file is missing: $path" }
}
foreach ($container in @($SQLServerContainer, $AuditContainer)) {
    if ((& docker inspect $container --format '{{.State.Running}}').Trim() -ne 'true') {
        throw "Required test database is not running: $container"
    }
}
$faultDir = Join-Path $TempRoot ($Target + '-fault-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $faultDir | Out-Null
$dbName = 'agv23_' + [guid]::NewGuid().ToString('N').Substring(0, 10)
$testContainer = 'agentsql-v230-' + $Target + '-' + [guid]::NewGuid().ToString('N').Substring(0, 8)
$auditPasswordLine = & docker inspect $AuditContainer --format '{{range .Config.Env}}{{println .}}{{end}}' |
    Where-Object { $_ -like 'POSTGRES_PASSWORD=*' } | Select-Object -First 1
if (-not $auditPasswordLine) { throw 'Audit test database credential is unavailable' }
$auditPassword = $auditPasswordLine.Substring('POSTGRES_PASSWORD='.Length)
$dsn = "postgres://postgres:${auditPassword}@host.docker.internal:25432/${dbName}?sslmode=disable"
$privateFaultEnv = Join-Path $faultDir 'fault.env'
$faultEnvName = if ($Target -eq 'audit') { 'AGENTSQL_TEST_SQLSERVER_AUDIT_FAULT_DIR' } else { 'AGENTSQL_TEST_SQLSERVER_BUSINESS_FAULT_DIR' }
$lines = @(Get-Content -LiteralPath $EnvironmentFile) + @("AGENTSQL_TEST_SQLSERVER_AUDIT_DSN=$dsn", "$faultEnvName=/fault")
[System.IO.File]::WriteAllText($privateFaultEnv, ($lines -join "`n") + "`n", [System.Text.UTF8Encoding]::new($false))
$auditPassword = $null
$dsn = $null
$faultContainer = if ($Target -eq 'audit') { $AuditContainer } else { $SQLServerContainer }
$network = if ($Target -eq 'audit') { "container:$SQLServerContainer" } else { 'agentsql-v230-net' }
$testHost = if ($Target -eq 'audit') { '127.0.0.1' } else { $SQLServerContainer }
$faultStopped = $false
$createdDB = $false
try {
    & docker exec $AuditContainer psql -U postgres -d postgres -v ON_ERROR_STOP=1 -c "CREATE DATABASE $dbName" | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Could not create isolated audit database' }
    $createdDB = $true
    & docker run -d --name $testContainer --network $network --env-file $privateFaultEnv `
        --env "AGENTSQL_SQLSERVER_HOST=$testHost" `
        --mount "type=bind,source=$repo,target=/src" `
        --mount "type=bind,source=$moduleCache,target=/gomod" `
        --mount "type=bind,source=$buildCache,target=/buildcache" `
        --mount "type=bind,source=$CAFile,target=/test-ca.pem,readonly" `
        --mount "type=bind,source=$faultDir,target=/fault" `
        --workdir /src --env GOPROXY=off --env GOTOOLCHAIN=local `
        --env GOMODCACHE=/gomod --env GOCACHE=/buildcache `
        golang:1.26-bookworm /usr/local/go/bin/go test -timeout 17m -count=1 -v `
        -run '^TestSQLServerHTTPMCPRealE2E$' ./internal/mcpserver | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Could not start SQL Server fault test container' }
    $deadline = (Get-Date).AddMinutes(8)
    while (-not (Test-Path -LiteralPath (Join-Path $faultDir 'ready'))) {
        if ((& docker inspect $testContainer --format '{{.State.Running}}').Trim() -ne 'true') {
            & docker logs $testContainer
            throw 'SQL Server fault test exited before ready signal'
        }
        if ((Get-Date) -ge $deadline) { throw 'Timed out waiting for SQL Server fault test' }
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
            throw 'SQL Server MCP test exited during database outage'
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
        } else {
            & docker exec $SQLServerContainer sh -c 'SQLCMDPASSWORD="$MSSQL_SA_PASSWORD" /opt/mssql-tools18/bin/sqlcmd -S localhost -U sa -C -Q "SELECT 1" -b' *> $null
        }
        $ready = $LASTEXITCODE -eq 0
        if ($ready) { break }
        Start-Sleep -Seconds 5
    }
    if (-not $ready) { throw "$Target test database did not recover" }
    Set-Content -LiteralPath (Join-Path $faultDir 'up') -Value 'recovered'
    $deadline = (Get-Date).AddMinutes(3)
    while ((& docker inspect $testContainer --format '{{.State.Running}}').Trim() -eq 'true') {
        if ((Get-Date) -ge $deadline) { throw 'SQL Server MCP fault test did not finish' }
        Start-Sleep -Seconds 1
    }
    $testExit = (& docker inspect $testContainer --format '{{.State.ExitCode}}').Trim()
    & docker logs $testContainer
    if ($testExit -ne '0') { throw "SQL Server MCP fault test failed with exit=$testExit" }
    Write-Output "V230_SQLSERVER_${Target}_FAULT=PASS"
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
    Remove-Item -LiteralPath $privateFaultEnv -Force -ErrorAction SilentlyContinue
    & docker rm -f $testContainer 2>$null | Out-Null
}

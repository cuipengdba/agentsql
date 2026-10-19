param(
    [string]$SQLServerContainer = 'agentsql-v230-mssql',
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
if ((& docker inspect $SQLServerContainer --format '{{.State.Running}}').Trim() -ne 'true') {
    throw 'The isolated SQL Server test container is not running'
}
$clientDir = Join-Path $TempRoot ('clients-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $clientDir | Out-Null
$testContainer = 'agentsql-v230-clients-' + [guid]::NewGuid().ToString('N').Substring(0, 8)
try {
    & docker run -d --name $testContainer --network "container:$SQLServerContainer" `
        --env-file $EnvironmentFile --env AGENTSQL_SQLSERVER_HOST=127.0.0.1 `
        --env AGENTSQL_TEST_SQLSERVER_CLIENT_DIR=/clients `
        --mount "type=bind,source=$repo,target=/src" `
        --mount "type=bind,source=$moduleCache,target=/gomod" `
        --mount "type=bind,source=$buildCache,target=/buildcache" `
        --mount "type=bind,source=$CAFile,target=/test-ca.pem,readonly" `
        --mount "type=bind,source=$clientDir,target=/clients" `
        --workdir /src --env GOPROXY=off --env GOTOOLCHAIN=local `
        --env GOMODCACHE=/gomod --env GOCACHE=/buildcache `
        golang:1.26-bookworm /usr/local/go/bin/go test -timeout 12m -count=1 -v `
        -run '^TestSQLServerHTTPMCPRealE2E$' ./internal/mcpserver | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Could not start SQL Server client test container' }
    $infoPath = Join-Path $clientDir 'client.json'
    $deadline = (Get-Date).AddMinutes(8)
    while (-not (Test-Path -LiteralPath $infoPath)) {
        if ((& docker inspect $testContainer --format '{{.State.Running}}').Trim() -ne 'true') {
            & docker logs $testContainer
            throw 'SQL Server MCP test exited before external clients connected'
        }
        if ((Get-Date) -ge $deadline) { throw 'Timed out waiting for SQL Server MCP gateway' }
        Start-Sleep -Seconds 1
    }
    $info = Get-Content -LiteralPath $infoPath -Raw | ConvertFrom-Json
    & (Join-Path $PSScriptRoot 'v1.5-mcp-client-probes.ps1') `
        -Network "container:$SQLServerContainer" -MCPURL $info.url -ApiKey $info.api_key `
        -DatasourceID $info.datasource_id -AllowSQL $info.allow_sql `
        -DenySQL $info.deny_sql -DenyRule $info.deny_rule
    if ($LASTEXITCODE -ne 0 -or -not $?) { throw 'SQL Server Inspector/Dify client probe failed' }
    Set-Content -LiteralPath (Join-Path $clientDir 'clients_done') -Value 'four calls completed'
    $deadline = (Get-Date).AddMinutes(2)
    while ((& docker inspect $testContainer --format '{{.State.Running}}').Trim() -eq 'true') {
        if ((Get-Date) -ge $deadline) { throw 'SQL Server MCP client test did not finish' }
        Start-Sleep -Seconds 1
    }
    $testExit = (& docker inspect $testContainer --format '{{.State.ExitCode}}').Trim()
    & docker logs $testContainer
    if ($testExit -ne '0') { throw "SQL Server MCP client test failed with exit=$testExit" }
    Write-Output 'V230_SQLSERVER_INSPECTOR_DIFY=PASS'
}
finally {
    Remove-Item -LiteralPath (Join-Path $clientDir 'client.json') -Force -ErrorAction SilentlyContinue
    & docker rm -f $testContainer 2>$null | Out-Null
}

param(
    [string]$Container = 'oracle-v06',
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
if ((& docker inspect $Container --format '{{.State.Health.Status}}').Trim() -ne 'healthy') {
    throw 'The isolated Oracle container is not healthy'
}
New-Item -ItemType Directory -Path $TempRoot -Force | Out-Null
$clientDir = Join-Path $TempRoot ('clients-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $clientDir | Out-Null
$testContainer = 'agentsql-v220-oracle-' + [guid]::NewGuid().ToString('N').Substring(0, 8)
$passwordLine = & docker inspect $Container --format '{{range .Config.Env}}{{println .}}{{end}}' |
    Where-Object { $_ -like 'ORACLE_PWD=*' } | Select-Object -First 1
if (-not $passwordLine) { throw 'Oracle test credential is unavailable' }
$env:ORACLE_PASSWORD = $passwordLine.Substring('ORACLE_PWD='.Length)
try {
    & docker run -d --name $testContainer --network "container:$Container" `
        --mount "type=bind,source=$repo,target=/src" `
        --mount "type=bind,source=$moduleCache,target=/gomod" `
        --mount "type=bind,source=$buildCache,target=/buildcache" `
        --mount "type=bind,source=$clientDir,target=/clients" `
        --workdir /src --env GOPROXY=off --env GOTOOLCHAIN=local `
        --env GOMODCACHE=/gomod --env GOCACHE=/buildcache `
        --env AGENTSQL_ORACLE_E2E=1 --env ORACLE_PASSWORD --env ORACLE_HOST=127.0.0.1 `
        --env AGENTSQL_TEST_ORACLE_CLIENT_DIR=/clients `
        golang:1.26-bookworm /usr/local/go/bin/go test -timeout 12m -count=1 -v `
        -run '^TestOracleHTTPMCPRealE2E$' ./internal/mcpserver | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Could not start Oracle live MCP test container' }
    $clientInfoPath = Join-Path $clientDir 'client.json'
    $deadline = (Get-Date).AddMinutes(8)
    while (-not (Test-Path -LiteralPath $clientInfoPath)) {
        if ((& docker inspect $testContainer --format '{{.State.Running}}').Trim() -ne 'true') {
            & docker logs $testContainer
            throw 'Oracle live MCP test exited before external clients connected'
        }
        if ((Get-Date) -ge $deadline) { throw 'Timed out waiting for Oracle MCP test gateway' }
        Start-Sleep -Seconds 1
    }
    $clientInfo = Get-Content -LiteralPath $clientInfoPath -Raw | ConvertFrom-Json
    & (Join-Path $PSScriptRoot 'v1.5-mcp-client-probes.ps1') `
        -Network "container:$Container" -MCPURL $clientInfo.url -ApiKey $clientInfo.api_key `
        -DatasourceID $clientInfo.datasource_id -AllowSQL $clientInfo.allow_sql `
        -DenySQL $clientInfo.deny_sql -DenyRule $clientInfo.deny_rule
    if ($LASTEXITCODE -ne 0) { throw 'Oracle Inspector/Dify client probe failed' }
    Set-Content -LiteralPath (Join-Path $clientDir 'clients_done') -Value 'four calls completed'
    $deadline = (Get-Date).AddMinutes(2)
    while ((& docker inspect $testContainer --format '{{.State.Running}}').Trim() -eq 'true') {
        if ((Get-Date) -ge $deadline) { throw 'Oracle live MCP test did not finish' }
        Start-Sleep -Seconds 1
    }
    $testExit = (& docker inspect $testContainer --format '{{.State.ExitCode}}').Trim()
    & docker logs $testContainer
    if ($testExit -ne '0') { throw "Oracle live MCP test failed with exit=$testExit" }
    Write-Output 'V220_ORACLE_INSPECTOR_DIFY=PASS'
}
finally {
    Remove-Item Env:\ORACLE_PASSWORD -ErrorAction SilentlyContinue
    Remove-Item -LiteralPath (Join-Path $clientDir 'client.json') -Force -ErrorAction SilentlyContinue
    & docker rm -f $testContainer 2>$null | Out-Null
}

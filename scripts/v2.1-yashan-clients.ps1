param(
    [string]$Container = 'yashan-v06',
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
    if (-not (Test-Path -LiteralPath $path -PathType Container)) {
        throw "Required local directory is missing: $path"
    }
}
if ((& docker inspect $Container --format '{{.State.Running}}').Trim() -ne 'true') {
    throw 'The isolated YashanDB container is not running'
}
New-Item -ItemType Directory -Path $TempRoot -Force | Out-Null
$clientDir = Join-Path $TempRoot ('clients-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $clientDir | Out-Null
$testContainer = 'agentsql-v210-yashan-' + [guid]::NewGuid().ToString('N').Substring(0, 8)
$env:YASHAN_PASSWORD = & docker exec $Container sh -c 'printf %s "$SYS_PASSWD"'
if ([string]::IsNullOrWhiteSpace($env:YASHAN_PASSWORD)) { throw 'YashanDB test credential is unavailable' }
try {
    & docker run -d --name $testContainer --network "container:$Container" `
        --mount "type=bind,source=$repo,target=/src" `
        --mount "type=bind,source=$moduleCache,target=/gomod" `
        --mount "type=bind,source=$buildCache,target=/buildcache" `
        --mount "type=bind,source=$clientLib,target=/client/lib,readonly" `
        --mount "type=bind,source=$clientDir,target=/clients" `
        --workdir /src `
        --env GOPROXY=off --env GOTOOLCHAIN=local --env GOMODCACHE=/gomod `
        --env GOCACHE=/buildcache --env CGO_ENABLED=1 --env LD_LIBRARY_PATH=/client/lib `
        --env AGENTSQL_YASHAN_E2E=1 --env YASHAN_PASSWORD --env YASHAN_HOST=127.0.0.1 `
        --env AGENTSQL_TEST_YASHAN_CLIENT_DIR=/clients `
        golang:1.26-bookworm go test -tags yashan -timeout 10m -count=1 -v `
        -run '^TestYashanHTTPMCPRealE2E$' ./internal/mcpserver | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Could not start YashanDB live MCP test container' }
    $clientInfoPath = Join-Path $clientDir 'client.json'
    $deadline = (Get-Date).AddMinutes(8)
    while (-not (Test-Path -LiteralPath $clientInfoPath)) {
        $running = (& docker inspect $testContainer --format '{{.State.Running}}').Trim()
        if ($running -ne 'true') {
            & docker logs $testContainer
            throw 'YashanDB live MCP test exited before external clients connected'
        }
        if ((Get-Date) -ge $deadline) { throw 'Timed out waiting for YashanDB MCP test gateway' }
        Start-Sleep -Seconds 1
    }
    $clientInfo = Get-Content -LiteralPath $clientInfoPath -Raw | ConvertFrom-Json
    & (Join-Path $PSScriptRoot 'v1.5-mcp-client-probes.ps1') `
        -Network "container:$Container" -MCPURL $clientInfo.url -ApiKey $clientInfo.api_key `
        -DatasourceID $clientInfo.datasource_id -AllowSQL $clientInfo.allow_sql `
        -DenySQL $clientInfo.deny_sql -DenyRule $clientInfo.deny_rule
    if ($LASTEXITCODE -ne 0) { throw 'YashanDB Inspector/Dify client probe failed' }
    Set-Content -LiteralPath (Join-Path $clientDir 'clients_done') -Value 'four calls completed'
    $deadline = (Get-Date).AddMinutes(2)
    while ((& docker inspect $testContainer --format '{{.State.Running}}').Trim() -eq 'true') {
        if ((Get-Date) -ge $deadline) { throw 'YashanDB live MCP test did not finish' }
        Start-Sleep -Seconds 1
    }
    $testExit = (& docker inspect $testContainer --format '{{.State.ExitCode}}').Trim()
    & docker logs $testContainer
    if ($testExit -ne '0') { throw "YashanDB live MCP test failed with exit=$testExit" }
    Write-Output 'V210_YASHAN_INSPECTOR_DIFY=PASS'
}
finally {
    Remove-Item Env:\YASHAN_PASSWORD -ErrorAction SilentlyContinue
    Remove-Item -LiteralPath (Join-Path $clientDir 'client.json') -Force -ErrorAction SilentlyContinue
    & docker rm -f $testContainer 2>$null | Out-Null
}

param(
    [string]$Instance = 'agentsql-v240-lts',
    [string]$EnvironmentFile = 'D:\CodexLS\tmp\agentsql-v240\test.env',
    [string]$CertDirectory = 'D:\CodexLS\tmp\agentsql-v240',
    [string]$TempRoot = 'D:\CodexLS\tmp\agentsql-v240'
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$repo = Split-Path -Parent $PSScriptRoot
$moduleCache = 'D:\CodexLS\cache\agentsql-go-mod-linux'
$buildCache = 'D:\CodexLS\cache\agentsql-go-build-linux'
$clientDir = Join-Path $TempRoot ('clients-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $clientDir | Out-Null
$testContainer = 'agentsql-v240-clients-' + [guid]::NewGuid().ToString('N').Substring(0, 8)
$network = "container:$Instance"

try {
    & docker run -d --name $testContainer --network $network --env-file $EnvironmentFile `
        --env AGENTSQL_TEST_OPENGAUSS_HOST=127.0.0.1 `
        --env AGENTSQL_TEST_OPENGAUSS_CLIENT_DIR=/clients `
        --mount "type=bind,source=$repo,target=/src" `
        --mount "type=bind,source=$CertDirectory,target=/certs,readonly" `
        --mount "type=bind,source=$moduleCache,target=/go/pkg/mod" `
        --mount "type=bind,source=$buildCache,target=/root/.cache/go-build" `
        --mount "type=bind,source=$clientDir,target=/clients" `
        --workdir /src --env GOPROXY=off --env GOTOOLCHAIN=local `
        golang:1.26.8 go test -timeout 12m -count=1 -v `
        -run '^TestOpenGauss700LTSMCPRealE2E$' ./internal/mcpserver | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Could not start openGauss client acceptance test' }
    $infoPath = Join-Path $clientDir 'client.json'
    $deadline = (Get-Date).AddMinutes(6)
    while (-not (Test-Path -LiteralPath $infoPath)) {
        if ((& docker inspect $testContainer --format '{{.State.Running}}').Trim() -ne 'true') {
            & docker logs $testContainer
            throw 'openGauss MCP test exited before external clients connected'
        }
        if ((Get-Date) -ge $deadline) { throw 'Timed out waiting for openGauss MCP gateway' }
        Start-Sleep -Seconds 1
    }
    $info = Get-Content -LiteralPath $infoPath -Raw | ConvertFrom-Json
    & (Join-Path $PSScriptRoot 'v1.5-mcp-client-probes.ps1') `
        -Network $network -MCPURL $info.url -ApiKey $info.api_key `
        -DatasourceID $info.datasource_id -AllowSQL $info.allow_sql `
        -DenySQL $info.deny_sql -DenyRule $info.deny_rule
    if ($LASTEXITCODE -ne 0 -or -not $?) { throw 'openGauss Inspector/Dify client probes failed' }
    Set-Content -LiteralPath (Join-Path $clientDir 'clients_done') -Value 'four calls completed'
    $deadline = (Get-Date).AddMinutes(3)
    while ((& docker inspect $testContainer --format '{{.State.Running}}').Trim() -eq 'true') {
        if ((Get-Date) -ge $deadline) { throw 'openGauss client acceptance test did not finish' }
        Start-Sleep -Seconds 1
    }
    $exit = (& docker inspect $testContainer --format '{{.State.ExitCode}}').Trim()
    & docker logs $testContainer
    if ($exit -ne '0') { throw "openGauss client acceptance test failed, exit=$exit" }
    Write-Output 'V240_OPENGAUSS_INSPECTOR_DIFY=PASS'
}
finally {
    Remove-Item -LiteralPath (Join-Path $clientDir 'client.json') -Force -ErrorAction SilentlyContinue
    & docker rm -f $testContainer 2>$null | Out-Null
}

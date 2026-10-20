param(
    [string]$FixtureRoot = 'D:\CodexLS\tmp\agentsql-v270',
    [string]$OutputRoot = 'D:\CodexLS\outputs\agentsql-v270',
    [string]$InspectorDirectory = 'D:\CodexLS\tools\mcp-inspector-016'
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$repo = Split-Path -Parent $PSScriptRoot
$probe = Join-Path $PSScriptRoot 'v2.7-clickhouse-dify-client-probe.py'
$moduleCache = 'D:\CodexLS\cache\go-mod'
$certs = Join-Path $FixtureRoot 'fixture\certs'
foreach ($path in @($FixtureRoot, $OutputRoot, $InspectorDirectory, $probe, $moduleCache, $certs, (Join-Path $FixtureRoot 'test.env'))) {
    if (-not (Test-Path -LiteralPath $path)) { throw "Missing client-test input: $path" }
}
$clientDir = Join-Path $FixtureRoot ('clients-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $clientDir | Out-Null
$testContainer = 'agentsql-v270-clients-' + [guid]::NewGuid().ToString('N').Substring(0, 8)
$network = "container:$testContainer"
$testEnv = Join-Path $clientDir 'test.env'
$lines = [string[]]@(Get-Content -LiteralPath (Join-Path $FixtureRoot 'test.env'))
$lines += 'AGENTSQL_TEST_CLICKHOUSE_CLIENT_DIR=/clients'
[IO.File]::WriteAllLines($testEnv, $lines, [Text.UTF8Encoding]::new($false))

function Invoke-Inspector([string]$Table, [string]$Decision, [object]$Info) {
    $output = @(& docker run --rm --network $network `
        --mount "type=bind,source=$InspectorDirectory,target=/tool,readonly" `
        --workdir /tool node:24-bookworm-slim node `
        node_modules/@modelcontextprotocol/inspector/cli/build/cli.js `
        --cli $Info.url --transport http --method tools/call --tool-name query_analytics `
        --tool-arg "datasource_id=$($Info.datasource_id)" "table=$Table" `
        'fields=["name","phone"]' 'filter={"region":"CN"}' 'limit=2' `
        --header "Authorization: Bearer $($Info.api_key)" 2>&1)
    if ($LASTEXITCODE -ne 0) { throw "Inspector $Decision exited nonzero" }
    $result = ($output -join "`n") | ConvertFrom-Json
    $content = $result.structuredContent
    if ($null -eq $content -or $content.decision -ne $Decision -or $null -eq $content.data.audit_id) {
        throw "Inspector $Decision returned an unexpected decision or no audit ID"
    }
    $encoded = $result | ConvertTo-Json -Depth 20 -Compress
    if ($encoded.Contains('13812345678') -or $encoded.Contains('synthetic-secret-canary')) {
        throw 'Unmasked ClickHouse data reached Inspector'
    }
    if ($Decision -eq 'allow' -and ($content.data.redact.MaskedCells -lt 1 -or -not $encoded.Contains('138****5678'))) {
        throw 'Inspector did not receive masked ClickHouse data'
    }
    if ($Decision -eq 'deny' -and $encoded.Contains('138****5678')) {
        throw 'Inspector denied query returned a row'
    }
    Write-Output ("INSPECTOR_{0}=PASS audit_id={1}" -f $Decision.ToUpperInvariant(), $content.data.audit_id)
}

try {
    & docker run -d --name $testContainer --network agentsql-v270-net --env-file $testEnv `
        --mount "type=bind,source=$repo,target=/src,readonly" `
        --mount "type=bind,source=$certs,target=/fixture/certs,readonly" `
        --mount "type=bind,source=$clientDir,target=/clients" `
        --mount "type=bind,source=$moduleCache,target=/gomod" `
        --mount 'type=volume,source=agentsql-go-build-cache,target=/buildcache' `
        --workdir /src --env GOTOOLCHAIN=local --env GOPROXY=off `
        --env GOMODCACHE=/gomod --env GOCACHE=/buildcache --env CGO_ENABLED=1 `
        golang:1.26.8 go test -timeout 12m -count=1 -v -run '^TestClickHouseControlledRealHTTPMCP$' ./internal/mcpserver | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Could not start ClickHouse MCP client test container' }
    $infoPath = Join-Path $clientDir 'client.json'
    $deadline = (Get-Date).AddMinutes(6)
    while (-not (Test-Path -LiteralPath $infoPath)) {
        if ((& docker inspect $testContainer --format '{{.State.Running}}').Trim() -ne 'true') {
            & docker logs $testContainer *> (Join-Path $OutputRoot 'clickhouse-clients-failed.log')
            Get-Content -LiteralPath (Join-Path $OutputRoot 'clickhouse-clients-failed.log') -Tail 40
            throw 'ClickHouse MCP test exited before external clients connected'
        }
        if ((Get-Date) -ge $deadline) { throw 'Timed out waiting for ClickHouse MCP gateway' }
        Start-Sleep -Seconds 1
    }
    $info = Get-Content -LiteralPath $infoPath -Raw | ConvertFrom-Json
    Invoke-Inspector 'events' 'allow' $info
    Invoke-Inspector 'private_notes' 'deny' $info
    $priorPreference = $ErrorActionPreference
    try {
        $ErrorActionPreference = 'Continue'
        $difyOutput = @(& docker run --rm --network $network `
            --mount "type=bind,source=$probe,target=/probe.py,readonly" `
            --workdir /app/api --entrypoint python --env PYTHONPATH=/app/api `
            --env "AGENTSQL_TEST_MCP_URL=$($info.url)" `
            --env "AGENTSQL_TEST_MCP_KEY=$($info.api_key)" `
            --env "AGENTSQL_TEST_DATASOURCE_ID=$($info.datasource_id)" `
            langgenius/dify-api:1.17.1 /probe.py 2>&1)
        $difyExit = $LASTEXITCODE
    }
    finally {
        $ErrorActionPreference = $priorPreference
    }
    if ($difyExit -ne 0 -or ($difyOutput -join "`n") -notmatch 'V270_DIFY_CLICKHOUSE=PASS') {
        throw 'Dify MCPClient failed for ClickHouse'
    }
    $difyOutput | Where-Object { $_ -match '^\{|^V270_|^DIFY_SESSION_' } | Write-Output
    Set-Content -LiteralPath (Join-Path $clientDir 'clients_done') -Value 'four calls completed'
    $deadline = (Get-Date).AddMinutes(3)
    while ((& docker inspect $testContainer --format '{{.State.Running}}').Trim() -eq 'true') {
        if ((Get-Date) -ge $deadline) { throw 'ClickHouse MCP client test did not finish' }
        Start-Sleep -Seconds 1
    }
    $exitCode = (& docker inspect $testContainer --format '{{.State.ExitCode}}').Trim()
    & docker logs $testContainer *> (Join-Path $OutputRoot 'clickhouse-clients.log')
    Get-Content -LiteralPath (Join-Path $OutputRoot 'clickhouse-clients.log') -Tail 15
    if ($exitCode -ne '0') { throw "ClickHouse MCP client test failed, exit=$exitCode" }
    Write-Output 'V270_CLICKHOUSE_INSPECTOR_DIFY=PASS'
}
finally {
    Remove-Item -LiteralPath (Join-Path $clientDir 'client.json'), $testEnv -Force -ErrorAction SilentlyContinue
    & docker rm -f $testContainer 2>$null | Out-Null
}

param(
    [ValidateSet('mongo', 'redis', 'valkey')][string]$Kind,
    [string]$FixtureRoot = 'D:\CodexLS\tmp\agentsql-v370',
    [string]$InspectorDirectory = 'D:\CodexLS\tools\mcp-inspector-016'
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$repo = Split-Path -Parent $PSScriptRoot
$probe = Join-Path $PSScriptRoot 'v3.7-native-dify-client-probe.py'
$moduleCache = 'D:\CodexLS\cache\go-mod'
$buildCache = 'D:\CodexLS\cache\go-build'
foreach ($path in @($FixtureRoot, $InspectorDirectory, $moduleCache, $buildCache, $probe)) {
    if (-not (Test-Path -LiteralPath $path)) { throw "Missing native client fixture: $path" }
}
$clientDir = Join-Path $FixtureRoot ('clients-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $clientDir | Out-Null
$testContainer = 'agentsql-v370-clients-' + [guid]::NewGuid().ToString('N').Substring(0, 8)
$network = "container:$testContainer"

function Invoke-Inspector([string]$decision, [object]$info) {
    $tool = if ($Kind -eq 'mongo') { 'find_documents' } else { 'read_hash_fields' }
    $masked = if ($Kind -eq 'mongo') { '138****0007' } else { '138****5678' }
    $raw = if ($Kind -eq 'mongo') { '13800000007' } else { '13812345678' }
    if ($Kind -eq 'mongo') {
        $collection = if ($decision -eq 'allow') { 'customers' } else { 'private_collection' }
        $toolArgs = @("datasource_id=$($info.datasource_id)", "collection=$collection", 'fields=["customer_id","phone"]', 'filter={"customer_id":"C7"}', 'limit=1')
    } else {
        $fields = if ($decision -eq 'allow') { 'fields=["phone"]' } else { 'fields=["secret"]' }
        $toolArgs = @("datasource_id=$($info.datasource_id)", 'key=customers:1', $fields)
    }
    $cliArgs = @('--cli', $info.url, '--transport', 'http', '--method', 'tools/call', '--tool-name', $tool)
    foreach ($arg in $toolArgs) { $cliArgs += @('--tool-arg', $arg) }
    $cliArgs += @('--header', "Authorization: Bearer $($info.api_key)")
    $output = @(& docker run --rm --network $network `
        --mount "type=bind,source=$InspectorDirectory,target=/tool,readonly" `
        --workdir /tool node:24-bookworm-slim node `
        node_modules/@modelcontextprotocol/inspector/cli/build/cli.js @cliArgs 2>&1)
    if ($LASTEXITCODE -ne 0) { throw "Inspector $Kind $decision failed" }
    $result = ($output -join "`n") | ConvertFrom-Json
    $content = $result.structuredContent
    if ($null -eq $content -or $content.decision -ne $decision -or $null -eq $content.data.audit_id) {
        throw "Inspector $Kind $decision returned no expected decision or audit ID"
    }
    $encoded = $result | ConvertTo-Json -Depth 20 -Compress
    if ($encoded.Contains($raw)) { throw "Unmasked $Kind value reached Inspector" }
    if ($decision -eq 'allow' -and ($content.data.redact.MaskedCells -ne 1 -or -not $encoded.Contains($masked))) {
        throw "Inspector did not receive masked $Kind value"
    }
    if ($decision -eq 'deny' -and $encoded.Contains($masked)) { throw "Inspector denied $Kind read returned a value" }
    Write-Output ("V370_INSPECTOR_{0}_{1}=PASS audit_id={2}" -f $Kind.ToUpperInvariant(), $decision.ToUpperInvariant(), $content.data.audit_id)
}

try {
    $mount = if ($Kind -eq 'mongo') { Join-Path $FixtureRoot 'mongo' } else { $FixtureRoot }
    $testName = if ($Kind -eq 'mongo') { '^TestMongoReplicaControlledRealHTTPMCP$' } else { "^TestRedisHashControlledRealHTTPMCP/$Kind`$" }
    $extra = if ($Kind -eq 'mongo') {
        @('-e', 'AGENTSQL_V370_MONGO_MCP_LIVE=1', '-e', 'AGENTSQL_V370_MONGO_CLIENT_DIR=/clients')
    } else {
        @('--env-file', (Join-Path $FixtureRoot "$Kind-mcp.env"), '-e', 'AGENTSQL_TEST_HASH_CLIENT_DIR=/clients')
    }
    & docker run -d --name $testContainer --network agentsql-v370-net `
        --mount "type=bind,source=$repo,target=/src" `
        --mount "type=bind,source=$mount,target=/fixtures,readonly" `
        --mount "type=bind,source=$moduleCache,target=/go/pkg/mod" `
        --mount "type=bind,source=$buildCache,target=/root/.cache/go-build" `
        --mount "type=bind,source=$clientDir,target=/clients" `
        @extra --workdir /src golang:1.26.10-bookworm `
        go test -timeout 10m -count=1 -v -run $testName ./internal/mcpserver | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Could not start native MCP client test' }
    $infoPath = Join-Path $clientDir "$Kind-client.json"
    if ($Kind -ne 'mongo') { $infoPath = Join-Path $clientDir "$Kind-hash-client.json" }
    $deadline = (Get-Date).AddMinutes(6)
    while (-not (Test-Path -LiteralPath $infoPath)) {
        if ((& docker inspect $testContainer --format '{{.State.Running}}').Trim() -ne 'true') {
            & docker logs $testContainer
            throw 'Native MCP test exited before external clients connected'
        }
        if ((Get-Date) -ge $deadline) { throw 'Timed out waiting for native MCP gateway' }
        Start-Sleep -Seconds 1
    }
    $info = Get-Content -LiteralPath $infoPath -Raw | ConvertFrom-Json
    Invoke-Inspector 'allow' $info
    Invoke-Inspector 'deny' $info
    $priorPreference = $ErrorActionPreference
    try {
        $ErrorActionPreference = 'Continue'
        $difyOutput = @(& docker run --rm --network $network `
            --mount "type=bind,source=$probe,target=/probe.py,readonly" `
            --workdir /app/api --entrypoint python --env PYTHONPATH=/app/api `
            --env "AGENTSQL_V370_CLIENT_KIND=$Kind" `
            --env "AGENTSQL_TEST_MCP_URL=$($info.url)" `
            --env "AGENTSQL_TEST_MCP_KEY=$($info.api_key)" `
            --env "AGENTSQL_TEST_DATASOURCE_ID=$($info.datasource_id)" `
            langgenius/dify-api:1.17.1 /probe.py 2>&1)
        $difyExit = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $priorPreference
    }
    if ($difyExit -ne 0 -or ($difyOutput -join "`n") -notmatch 'V370_DIFY_NATIVE=PASS') {
        throw "Dify MCPClient failed for ${Kind}: $($difyOutput -join ' ')"
    }
    $difyOutput | Where-Object { $_ -match '^\{|^V370_|^DIFY_SESSION_' } | Write-Output
    $doneName = if ($Kind -eq 'mongo') { 'mongo-clients_done' } else { "$Kind-hash-clients_done" }
    Set-Content -LiteralPath (Join-Path $clientDir $doneName) -Value 'four client calls completed'
    $deadline = (Get-Date).AddMinutes(3)
    while ((& docker inspect $testContainer --format '{{.State.Running}}').Trim() -eq 'true') {
        if ((Get-Date) -ge $deadline) { throw 'Native MCP client test did not finish' }
        Start-Sleep -Seconds 1
    }
    $exitCode = (& docker inspect $testContainer --format '{{.State.ExitCode}}').Trim()
    & docker logs $testContainer
    if ($exitCode -ne '0') { throw "Native MCP client test failed, exit=$exitCode" }
    Write-Output "V370_${Kind}_INSPECTOR_DIFY=PASS"
} finally {
    Remove-Item -LiteralPath (Join-Path $clientDir 'mongo-client.json'), (Join-Path $clientDir "$Kind-hash-client.json") -Force -ErrorAction SilentlyContinue
    & docker rm -f $testContainer 2>$null | Out-Null
}

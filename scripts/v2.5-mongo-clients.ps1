param(
    [string]$EnvironmentFile = 'D:\CodexLS\tmp\agentsql-v250\mongo-env-private.list',
    [string]$CertDirectory = 'D:\CodexLS\tmp\agentsql-v250\mongo-runtime-certs',
    [string]$TempRoot = 'D:\CodexLS\tmp\agentsql-v250',
    [string]$InspectorDirectory = 'D:\CodexLS\tools\mcp-inspector-016'
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$repo = Split-Path -Parent $PSScriptRoot
$moduleCache = 'D:\CodexLS\cache\agentsql-go-mod-linux'
$buildCache = 'D:\CodexLS\cache\agentsql-go-build-linux'
$probeScript = Join-Path $PSScriptRoot 'v2.5-mongo-dify-client-probe.py'
foreach ($path in @($EnvironmentFile, $CertDirectory, $InspectorDirectory, $moduleCache, $buildCache, $probeScript)) {
    if (-not (Test-Path -LiteralPath $path)) { throw "Missing MongoDB client test input: $path" }
}
$clientDir = Join-Path $TempRoot ('clients-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $clientDir | Out-Null
$testContainer = 'agentsql-v250-mcp-clients-' + [guid]::NewGuid().ToString('N').Substring(0, 8)
$network = "container:$testContainer"

function Invoke-Inspector([string]$collection, [string]$decision, [string]$url, [string]$apiKey, [string]$datasourceID) {
    $output = @(& docker run --rm --network $network `
        --mount "type=bind,source=$InspectorDirectory,target=/tool,readonly" `
        --workdir /tool node:24-bookworm-slim node `
        node_modules/@modelcontextprotocol/inspector/cli/build/cli.js `
        --cli $url --transport http --method tools/call --tool-name find_documents `
        --tool-arg "datasource_id=$datasourceID" "collection=$collection" `
        'fields=["name","phone"]' 'filter={"name":"alice"}' 'limit=1' `
        --header "Authorization: Bearer $apiKey" 2>&1)
    if ($LASTEXITCODE -ne 0) { throw "Inspector $decision failed: $($output -join ' ')" }
    $result = ($output -join "`n") | ConvertFrom-Json
    $content = $result.structuredContent
    if ($null -eq $content -or $content.decision -ne $decision) { throw "Inspector $decision returned an unexpected decision" }
    if ($null -eq $content.data.audit_id) { throw "Inspector $decision has no audit ID" }
    $encoded = $result | ConvertTo-Json -Depth 20 -Compress
    if ($encoded.Contains('13812345678') -or $encoded.Contains('synthetic-secret-canary')) {
        throw 'Unmasked MongoDB data reached Inspector'
    }
    if ($decision -eq 'allow') {
        if ($content.data.redact.MaskedCells -ne 1 -or -not $encoded.Contains('138****5678')) {
            throw 'Inspector did not receive the masked MongoDB phone'
        }
    } elseif ($encoded.Contains('138****5678')) {
        throw 'Inspector denied read returned a row'
    }
    Write-Output ("INSPECTOR_{0}=PASS audit_id={1}" -f $decision.ToUpperInvariant(), $content.data.audit_id)
}

try {
    & docker run -d --name $testContainer --network agentsql-v250-net --env-file $EnvironmentFile `
        --env AGENTSQL_TEST_MONGO_HOST=mongo-v250.agentsql.test `
        --env AGENTSQL_TEST_MONGO_SERVER_NAME=mongo-v250.agentsql.test `
        --env AGENTSQL_TEST_MONGO_CA=/mongo-certs/ca.crt `
        --env AGENTSQL_TEST_MONGO_CLIENT_DIR=/clients `
        --mount "type=bind,source=$repo,target=/src" `
        --mount "type=bind,source=$CertDirectory,target=/mongo-certs,readonly" `
        --mount "type=bind,source=$moduleCache,target=/go/pkg/mod" `
        --mount "type=bind,source=$buildCache,target=/root/.cache/go-build" `
        --mount "type=bind,source=$clientDir,target=/clients" `
        --workdir /src --env GOTOOLCHAIN=local --entrypoint sh golang:1.26.8 `
        -c 'export AGENTSQL_TEST_MONGO_RO_PASSWORD="$MONGO_RO_PASSWORD"; go test -timeout 10m -count=1 -v -run "^TestMongoControlledReadRealHTTPMCP$" ./internal/mcpserver' | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Could not start MongoDB MCP test container' }
    $infoPath = Join-Path $clientDir 'client.json'
    $deadline = (Get-Date).AddMinutes(6)
    while (-not (Test-Path -LiteralPath $infoPath)) {
        if ((& docker inspect $testContainer --format '{{.State.Running}}').Trim() -ne 'true') {
            & docker logs $testContainer
            throw 'MongoDB MCP test exited before external clients connected'
        }
        if ((Get-Date) -ge $deadline) { throw 'Timed out waiting for MongoDB MCP gateway' }
        Start-Sleep -Seconds 1
    }
    $info = Get-Content -LiteralPath $infoPath -Raw | ConvertFrom-Json
    Invoke-Inspector 'customers' 'allow' $info.url $info.api_key $info.datasource_id
    Invoke-Inspector 'private_collection' 'deny' $info.url $info.api_key $info.datasource_id

    $priorErrorPreference = $ErrorActionPreference
    try {
        $ErrorActionPreference = 'Continue'
        $difyOutput = @(& docker run --rm --network $network `
            --mount "type=bind,source=$probeScript,target=/probe.py,readonly" `
            --workdir /app/api --entrypoint python --env PYTHONPATH=/app/api `
            --env "AGENTSQL_TEST_MCP_URL=$($info.url)" `
            --env "AGENTSQL_TEST_MCP_KEY=$($info.api_key)" `
            --env "AGENTSQL_TEST_DATASOURCE_ID=$($info.datasource_id)" `
            langgenius/dify-api:1.17.1 /probe.py 2>&1)
        $difyExit = $LASTEXITCODE
    }
    finally {
        $ErrorActionPreference = $priorErrorPreference
    }
    if ($difyExit -ne 0 -or ($difyOutput -join "`n") -notmatch 'V250_DIFY_MONGO=PASS') {
        throw "Dify MCPClient failed: $($difyOutput -join ' ')"
    }
    $difyOutput | Where-Object { $_ -match '^\{|^V250_|^DIFY_SESSION_' } | Write-Output
    Set-Content -LiteralPath (Join-Path $clientDir 'clients_done') -Value 'four calls completed'
    $deadline = (Get-Date).AddMinutes(3)
    while ((& docker inspect $testContainer --format '{{.State.Running}}').Trim() -eq 'true') {
        if ((Get-Date) -ge $deadline) { throw 'MongoDB MCP client test did not finish' }
        Start-Sleep -Seconds 1
    }
    $exit = (& docker inspect $testContainer --format '{{.State.ExitCode}}').Trim()
    & docker logs $testContainer
    if ($exit -ne '0') { throw "MongoDB MCP client test failed, exit=$exit" }
    Write-Output 'V250_MONGO_INSPECTOR_DIFY=PASS'
}
finally {
    Remove-Item -LiteralPath (Join-Path $clientDir 'client.json') -Force -ErrorAction SilentlyContinue
    & docker rm -f $testContainer 2>$null | Out-Null
}

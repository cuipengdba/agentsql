param(
    [string]$FixtureRoot = 'D:\CodexLS\tmp\agentsql-v260',
    [string]$OutputRoot = 'D:\CodexLS\outputs\agentsql-v260',
    [string]$InspectorDirectory = 'D:\CodexLS\tools\mcp-inspector-016'
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$repo = Split-Path -Parent $PSScriptRoot
$probe = Join-Path $PSScriptRoot 'v2.6-key-dify-client-probe.py'
$moduleCache = 'D:\CodexLS\cache\go-mod'
$buildCache = 'D:\CodexLS\cache\go-build-linux'
foreach ($path in @($FixtureRoot, $OutputRoot, $InspectorDirectory, $probe, $moduleCache, $buildCache)) {
    if (-not (Test-Path -LiteralPath $path)) { throw "Missing client-test input: $path" }
}
$clientDir = Join-Path $FixtureRoot ('clients-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $clientDir | Out-Null
$testContainer = 'agentsql-v260-clients-' + [guid]::NewGuid().ToString('N').Substring(0, 8)
$network = "container:$testContainer"
$testEnv = Join-Path $clientDir 'test.env'
$lines = [string[]]@(Get-Content -LiteralPath (Join-Path $FixtureRoot 'test.env'))
$lines += 'AGENTSQL_TEST_KEY_CLIENT_DIR=/clients'
[IO.File]::WriteAllLines($testEnv, $lines, [Text.UTF8Encoding]::new($false))

function Wait-ClientInfo([string]$Dialect, [int]$Seconds) {
    $infoPath = Join-Path $clientDir "$Dialect-client.json"
    $deadline = (Get-Date).AddSeconds($Seconds)
    while (-not (Test-Path -LiteralPath $infoPath)) {
        if ((& docker inspect $testContainer --format '{{.State.Running}}').Trim() -ne 'true') {
            throw "Key MCP test exited before $Dialect clients connected"
        }
        if ((Get-Date) -ge $deadline) { throw "Timed out waiting for $Dialect MCP gateway" }
        Start-Sleep -Seconds 1
    }
    return Get-Content -LiteralPath $infoPath -Raw | ConvertFrom-Json
}

function Invoke-Inspector([string]$KeyName, [string]$Decision, [object]$Info) {
    $output = @(& docker run --rm --network $network `
        --mount "type=bind,source=$InspectorDirectory,target=/tool,readonly" `
        --workdir /tool node:24-bookworm-slim node `
        node_modules/@modelcontextprotocol/inspector/cli/build/cli.js `
        --cli $Info.url --transport http --method tools/call --tool-name read_key `
        --tool-arg "datasource_id=$($Info.datasource_id)" "key=$KeyName" `
        --header "Authorization: Bearer $($Info.api_key)" 2>&1)
    if ($LASTEXITCODE -ne 0) { throw "Inspector $Decision exited nonzero" }
    $result = ($output -join "`n") | ConvertFrom-Json
    $content = $result.structuredContent
    if ($null -eq $content -or $content.decision -ne $Decision -or $null -eq $content.data.audit_id) {
        throw "Inspector $Decision returned an unexpected decision or no audit ID"
    }
    $encoded = $result | ConvertTo-Json -Depth 20 -Compress
    if ($encoded.Contains('13812345678') -or $encoded.Contains('synthetic-secret-canary')) {
        throw 'Unmasked key data reached Inspector'
    }
    if ($Decision -eq 'allow' -and ($content.data.redact.MaskedCells -ne 1 -or -not $encoded.Contains('138****5678'))) {
        throw 'Inspector did not receive the masked key value'
    }
    if ($Decision -eq 'deny' -and $encoded.Contains('138****5678')) {
        throw 'Inspector denied read returned a value'
    }
    Write-Output ("INSPECTOR_{0}=PASS audit_id={1}" -f $Decision.ToUpperInvariant(), $content.data.audit_id)
}

try {
    & docker run -d --name $testContainer --network agentsql-v260-net --env-file $testEnv `
        --mount "type=bind,source=$repo,target=/src,readonly" `
        --mount "type=bind,source=$FixtureRoot,target=/fixture,readonly" `
        --mount "type=bind,source=$clientDir,target=/clients" `
        --mount "type=bind,source=$moduleCache,target=/gomod" `
        --mount "type=bind,source=$buildCache,target=/gocache" `
        --workdir /src --env GOTOOLCHAIN=local --env GOPROXY=off `
        --env GOMODCACHE=/gomod --env GOCACHE=/gocache `
        golang:1.26.8 go test -timeout 12m -count=1 -v -run '^TestRedisValkeyControlledRealHTTPMCP$' ./internal/mcpserver | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Could not start key MCP client test container' }

    foreach ($dialect in @('redis','valkey')) {
        $info = Wait-ClientInfo $dialect 300
        Invoke-Inspector 'customers:1' 'allow' $info
        Invoke-Inspector 'private:1' 'deny' $info
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
        if ($difyExit -ne 0 -or ($difyOutput -join "`n") -notmatch 'V260_DIFY_KEY=PASS') {
            throw "Dify MCPClient failed for $dialect"
        }
        $difyOutput | Where-Object { $_ -match '^\{|^V260_|^DIFY_SESSION_' } | Write-Output
        Set-Content -LiteralPath (Join-Path $clientDir "$dialect-clients_done") -Value 'four calls completed'
    }
    $deadline = (Get-Date).AddMinutes(3)
    while ((& docker inspect $testContainer --format '{{.State.Running}}').Trim() -eq 'true') {
        if ((Get-Date) -ge $deadline) { throw 'Key MCP client test did not finish' }
        Start-Sleep -Seconds 1
    }
    $exitCode = (& docker inspect $testContainer --format '{{.State.ExitCode}}').Trim()
    & docker logs $testContainer *> (Join-Path $OutputRoot 'key-clients.log')
    Get-Content -LiteralPath (Join-Path $OutputRoot 'key-clients.log') -Tail 15
    if ($exitCode -ne '0') { throw "Key MCP client test failed, exit=$exitCode" }
    Write-Output 'V260_REDIS_VALKEY_INSPECTOR_DIFY=PASS'
}
finally {
    foreach ($dialect in @('redis','valkey')) {
        Remove-Item -LiteralPath (Join-Path $clientDir "$dialect-client.json") -Force -ErrorAction SilentlyContinue
    }
    Remove-Item -LiteralPath $testEnv -Force -ErrorAction SilentlyContinue
    & docker rm -f $testContainer 2>$null | Out-Null
}

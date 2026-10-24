param(
    [Parameter(Mandatory = $true)]
    [ValidateSet('oracle', 'sqlserver')]
    [string]$Database,
    [string]$FixtureRoot = 'D:\CodexLS\tmp\agentsql-v390',
    [string]$OutputRoot = 'D:\CodexLS\outputs\agentsql-v390',
    [string]$InspectorDirectory = 'D:\CodexLS\tools\mcp-inspector-016'
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$repo = Split-Path -Parent $PSScriptRoot
$probe = Join-Path $PSScriptRoot 'v3.9-relational-dify-client-probe.py'
$moduleCache = 'D:\CodexLS\cache\go-mod'
$buildCache = 'D:\CodexLS\cache\go-build'
if ($Database -eq 'oracle') {
    $testEnv = Join-Path $FixtureRoot 'oracle-rotated-test.env'
    $certs = Join-Path $FixtureRoot 'oracle-client-certs'
    $testName = 'TestOracleHTTPMCPRealE2E'
} else {
    $testEnv = Join-Path $FixtureRoot 'mssql-test.env'
    $certs = Join-Path $FixtureRoot 'mssql-client-certs'
    $testName = 'TestSQLServerHTTPMCPRealE2E'
}
foreach ($path in @($repo, $FixtureRoot, $OutputRoot, $InspectorDirectory, $probe,
                   $moduleCache, $buildCache, $certs, $testEnv)) {
    if (-not (Test-Path -LiteralPath $path)) { throw "Missing client-test input: $path" }
}

$clientDir = Join-Path $FixtureRoot ("$Database-clients-" + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $clientDir | Out-Null
$testContainer = "agentsql-v390-$Database-clients-" + [guid]::NewGuid().ToString('N').Substring(0, 8)
$network = "container:$testContainer"
$clientEnv = "AGENTSQL_TEST_$($Database.ToUpperInvariant())_CLIENT_DIR=/clients"
if ($Database -eq 'sqlserver') { $clientEnv = 'AGENTSQL_TEST_SQLSERVER_CLIENT_DIR=/clients' }

function Invoke-Inspector([string]$Sql, [string]$Decision, [object]$Info) {
    $output = @(& docker run --rm --network $network `
        --mount "type=bind,source=$InspectorDirectory,target=/tool,readonly" `
        --workdir /tool node:24-bookworm-slim node `
        node_modules/@modelcontextprotocol/inspector/cli/build/cli.js `
        --cli $Info.url --transport http --method tools/call --tool-name query `
        --tool-arg "datasource_id=$($Info.datasource_id)" --tool-arg "sql=$Sql" `
        --header "Authorization: Bearer $($Info.api_key)" 2>&1)
    if ($LASTEXITCODE -ne 0) { throw "Inspector $Decision exited nonzero" }
    $result = ($output -join "`n") | ConvertFrom-Json
    $content = $result.structuredContent
    if ($null -eq $content -or $content.decision -ne $Decision -or $null -eq $content.data.audit_id) {
        throw "Inspector $Decision returned an unexpected decision or no audit ID"
    }
    $encoded = $result | ConvertTo-Json -Depth 20 -Compress
    if ($encoded.Contains('13800135678')) { throw 'Unmasked synthetic data reached Inspector' }
    if ($Decision -eq 'allow' -and
        ($content.data.redact.MaskedCells -lt 1 -or -not $encoded.Contains('138****5678'))) {
        throw 'Inspector did not receive masked data'
    }
    if ($Decision -eq 'deny' -and $encoded.Contains('138****5678')) {
        throw 'Inspector denied query returned a row'
    }
    Write-Output ("INSPECTOR_{0}=PASS audit_id={1}" -f $Decision.ToUpperInvariant(), $content.data.audit_id)
}

try {
    & docker run -d --name $testContainer --network agentsql-v390 --env-file $testEnv `
        --env $clientEnv --env GOTOOLCHAIN=local --env GOMAXPROCS=2 `
        --mount "type=bind,source=$repo,target=/src,readonly" `
        --mount "type=bind,source=$certs,target=/certs,readonly" `
        --mount "type=bind,source=$clientDir,target=/clients" `
        --mount "type=bind,source=$moduleCache,target=/go/pkg/mod" `
        --mount "type=bind,source=$buildCache,target=/root/.cache/go-build" `
        --workdir /src golang:1.26-bookworm `
        go test -timeout 15m -count=1 -v -run "^$testName`$" ./internal/mcpserver | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Could not start relational MCP client test container' }
    $infoPath = Join-Path $clientDir 'client.json'
    $deadline = (Get-Date).AddMinutes(6)
    while (-not (Test-Path -LiteralPath $infoPath)) {
        if ((& docker inspect $testContainer --format '{{.State.Running}}').Trim() -ne 'true') {
            & docker logs $testContainer *> (Join-Path $OutputRoot "$Database-clients-failed.log")
            throw "$Database MCP test exited before external clients connected"
        }
        if ((Get-Date) -ge $deadline) { throw "Timed out waiting for $Database MCP gateway" }
        Start-Sleep -Seconds 1
    }
    $info = Get-Content -LiteralPath $infoPath -Raw | ConvertFrom-Json
    Invoke-Inspector $info.allow_sql 'allow' $info
    Invoke-Inspector $info.deny_sql 'deny' $info

    $priorPreference = $ErrorActionPreference
    try {
        $ErrorActionPreference = 'Continue'
        $difyOutput = @(& docker run --rm --network $network `
            --mount "type=bind,source=$probe,target=/probe.py,readonly" `
            --workdir /app/api --entrypoint python --env PYTHONPATH=/app/api `
            --env "AGENTSQL_TEST_MCP_URL=$($info.url)" `
            --env "AGENTSQL_TEST_MCP_KEY=$($info.api_key)" `
            --env "AGENTSQL_TEST_DATASOURCE_ID=$($info.datasource_id)" `
            --env "AGENTSQL_TEST_ALLOW_SQL=$($info.allow_sql)" `
            --env "AGENTSQL_TEST_DENY_SQL=$($info.deny_sql)" `
            langgenius/dify-api:1.17.1 /probe.py 2>&1)
        $difyExit = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $priorPreference
    }
    if ($difyExit -ne 0 -or ($difyOutput -join "`n") -notmatch 'V390_DIFY_RELATIONAL=PASS') {
        throw 'Dify MCPClient failed for relational query'
    }
    $difyOutput | Where-Object { $_ -match '^\{|^V390_|^DIFY_SESSION_' } | Write-Output
    Set-Content -LiteralPath (Join-Path $clientDir 'clients_done') -Value 'four calls completed'

    $deadline = (Get-Date).AddMinutes(3)
    while ((& docker inspect $testContainer --format '{{.State.Running}}').Trim() -eq 'true') {
        if ((Get-Date) -ge $deadline) { throw "$Database MCP client test did not finish" }
        Start-Sleep -Seconds 1
    }
    $exitCode = (& docker inspect $testContainer --format '{{.State.ExitCode}}').Trim()
    & docker logs $testContainer *> (Join-Path $OutputRoot "$Database-clients.log")
    if ($exitCode -ne '0') { throw "$Database MCP client test failed, exit=$exitCode" }
    Write-Output ("V390_{0}_INSPECTOR_DIFY=PASS" -f $Database.ToUpperInvariant())
} finally {
    Remove-Item -LiteralPath (Join-Path $clientDir 'client.json') -Force -ErrorAction SilentlyContinue
    & docker rm -f $testContainer 2>$null | Out-Null
}

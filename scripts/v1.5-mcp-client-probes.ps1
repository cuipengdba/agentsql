param(
    [Parameter(Mandatory)][string]$Network,
    [string]$Gateway,
    [string]$MCPURL,
    [Parameter(Mandatory)][string]$ApiKey,
    [Parameter(Mandatory)][string]$DatasourceID,
    [Parameter(Mandatory)][string]$AllowSQL,
    [Parameter(Mandatory)][string]$DenySQL,
    [Parameter(Mandatory)][string]$DenyRule,
    [string]$InspectorDirectory = 'D:\CodexLS\tools\mcp-inspector-016'
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$dockerExe = @(Get-Command docker -CommandType Application -ErrorAction Stop | Select-Object -First 1)[0].Source
$probeScript = Join-Path $PSScriptRoot 'v1.5-dify-mcp-client-probe.py'
foreach ($path in @($InspectorDirectory, $probeScript)) {
    if (-not (Test-Path -LiteralPath $path)) { throw "Missing client probe input: $path" }
}
if (-not $MCPURL) {
    if (-not $Gateway) { throw 'Set either -Gateway or -MCPURL' }
    $MCPURL = "http://${Gateway}:7780/mcp"
}

function Invoke-Inspector([string]$sql, [string]$decision) {
    $output = @(& $dockerExe run --rm --network $Network `
        --mount "type=bind,source=$InspectorDirectory,target=/tool,readonly" `
        --workdir /tool node:24-bookworm-slim node `
        node_modules/@modelcontextprotocol/inspector/cli/build/cli.js `
        --cli $MCPURL --transport http --method tools/call --tool-name query `
        --tool-arg "datasource_id=$DatasourceID" --tool-arg "sql=$sql" `
        --header "Authorization: Bearer $ApiKey" 2>&1)
    if ($LASTEXITCODE -ne 0) { throw "MCP Inspector $decision call failed: $($output -join ' ')" }
    # Inspector may print a startup notice before its JSON result. Only the
    # JSON object is protocol evidence; never parse the notice as response.
    $jsonOutput = [regex]::Match(($output -join "`n"), '(?s)\{.*\}')
    if (-not $jsonOutput.Success) { throw "MCP Inspector $decision call produced no JSON result" }
    $result = ($jsonOutput.Value | ConvertFrom-Json)
    $content = $result.structuredContent
    if ($null -eq $content -or $content.decision -ne $decision) {
        throw "MCP Inspector returned an unexpected $decision decision"
    }
    if ($null -eq $content.data.audit_id) { throw 'MCP Inspector result has no audit ID' }
    $serialized = $result | ConvertTo-Json -Depth 20 -Compress
    if ($serialized.Contains('13800135678') -or $serialized.Contains('13812345678') -or $serialized.Contains('13800105678')) {
        throw 'Unmasked phone reached MCP Inspector'
    }
    if ($decision -eq 'allow') {
        if ($content.data.redact.MaskedCells -ne 1 -or -not $serialized.Contains('138****5678')) {
            throw 'MCP Inspector did not receive the masked synthetic phone'
        }
    } elseif (@($content.data.hits | Where-Object { $_.RuleID -eq $DenyRule }).Count -ne 1) {
        throw "MCP Inspector did not receive $DenyRule"
    }
    Write-Output ("INSPECTOR_{0}=PASS audit_id={1}" -f $decision.ToUpperInvariant(), $content.data.audit_id)
}

Invoke-Inspector $AllowSQL 'allow'
Invoke-Inspector $DenySQL 'deny'

$previousErrorPreference = $ErrorActionPreference
try {
    # Dify 1.17.1 can emit its HTTP 204 cleanup warning on stderr even when
    # both calls passed. Capture the process result before deciding success.
    $ErrorActionPreference = 'Continue'
    $difyOutput = @(& $dockerExe run --rm --network $Network `
        --mount "type=bind,source=$probeScript,target=/probe.py,readonly" `
        --workdir /app/api --entrypoint python `
        --env PYTHONPATH=/app/api `
        --env "AGENTSQL_TEST_MCP_URL=$MCPURL" `
        --env "AGENTSQL_TEST_MCP_KEY=$ApiKey" `
        --env "AGENTSQL_TEST_DATASOURCE_ID=$DatasourceID" `
        --env "AGENTSQL_TEST_ALLOW_SQL=$AllowSQL" `
        --env "AGENTSQL_TEST_DENY_SQL=$DenySQL" `
        --env "AGENTSQL_TEST_DENY_RULE=$DenyRule" `
        langgenius/dify-api:1.17.1 /probe.py 2>&1)
    $difyExitCode = $LASTEXITCODE
}
finally {
    $ErrorActionPreference = $previousErrorPreference
}
if ($difyExitCode -ne 0 -or ($difyOutput -join "`n") -notmatch 'V150_DIFY_MCP_CLIENT=PASS') {
    throw "Dify MCPClient calls failed: $($difyOutput -join ' ')"
}
$safeDify = @($difyOutput | Where-Object { $_ -match '^\{|^V150_|^DIFY_SESSION_' })
Write-Output $safeDify
Write-Output 'V150_INSPECTOR_AND_DIFY=PASS'

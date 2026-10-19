param(
    [string]$Instance = 'agentsql-v240-lts',
    [string]$Network = 'agentsql-v240-net',
    [string]$EnvironmentFile = 'D:\CodexLS\tmp\agentsql-v240\test.env',
    [string]$CertDirectory = 'D:\CodexLS\tmp\agentsql-v240',
    [string]$SignalRoot = 'D:\CodexLS\tmp\agentsql-v240'
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$repo = Split-Path -Parent $PSScriptRoot
$moduleCache = 'D:\CodexLS\cache\agentsql-go-mod-linux'
$buildCache = 'D:\CodexLS\cache\agentsql-go-build-linux'
$signalDir = Join-Path $SignalRoot ('permission-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $signalDir | Out-Null
$testContainer = 'agentsql-v240-permission-' + [guid]::NewGuid().ToString('N').Substring(0, 8)
$revoked = $false

function Wait-Signal([string]$Name, [int]$Seconds) {
    $path = Join-Path $signalDir $Name
    $deadline = (Get-Date).AddSeconds($Seconds)
    while (-not (Test-Path -LiteralPath $path)) {
        $running = (& docker inspect $testContainer --format '{{.State.Running}}').Trim()
        if ($running -ne 'true') {
            & docker logs $testContainer
            throw "openGauss MCP test exited before $Name"
        }
        if ((Get-Date) -ge $deadline) { throw "Timed out waiting for $Name" }
        Start-Sleep -Seconds 1
    }
}

try {
    & docker run -d --name $testContainer --network $Network --env-file $EnvironmentFile `
        --env AGENTSQL_TEST_OPENGAUSS_PERMISSION_DIR=/signals `
        --mount "type=bind,source=$repo,target=/src" `
        --mount "type=bind,source=$CertDirectory,target=/certs,readonly" `
        --mount "type=bind,source=$moduleCache,target=/go/pkg/mod" `
        --mount "type=bind,source=$buildCache,target=/root/.cache/go-build" `
        --mount "type=bind,source=$signalDir,target=/signals" `
        --workdir /src --env GOPROXY=off --env GOTOOLCHAIN=local `
        golang:1.26.8 go test -timeout 12m -count=1 -v `
        -run '^TestOpenGauss700LTSMCPRealE2E$' ./internal/mcpserver | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Could not start openGauss permission test' }
    Wait-Signal 'ready' 300
    & docker exec $Instance su - omm -c '/usr/local/opengauss/bin/gsql -d postgres -p 5432 -v ON_ERROR_STOP=1 -c "REVOKE SELECT ON public.agentsql_v240_verify FROM agentsql_v240_ro"' | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Could not revoke synthetic SELECT grant' }
    $revoked = $true
    Set-Content -LiteralPath (Join-Path $signalDir 'revoked') -Value 'revoked'
    Wait-Signal 'revoked_checked' 120
    & docker exec $Instance su - omm -c '/usr/local/opengauss/bin/gsql -d postgres -p 5432 -v ON_ERROR_STOP=1 -c "GRANT SELECT ON public.agentsql_v240_verify TO agentsql_v240_ro"' | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Could not restore synthetic SELECT grant' }
    $revoked = $false
    Set-Content -LiteralPath (Join-Path $signalDir 'restored') -Value 'restored'
    $deadline = (Get-Date).AddMinutes(3)
    while ((& docker inspect $testContainer --format '{{.State.Running}}').Trim() -eq 'true') {
        if ((Get-Date) -ge $deadline) { throw 'openGauss permission test did not finish' }
        Start-Sleep -Seconds 1
    }
    $exit = (& docker inspect $testContainer --format '{{.State.ExitCode}}').Trim()
    & docker logs $testContainer
    if ($exit -ne '0') { throw "openGauss permission test failed, exit=$exit" }
    Write-Output 'V240_OPENGAUSS_PERMISSION_REVOKE_RECOVERY=PASS'
}
finally {
    if ($revoked) {
        & docker exec $Instance su - omm -c '/usr/local/opengauss/bin/gsql -d postgres -p 5432 -v ON_ERROR_STOP=1 -c "GRANT SELECT ON public.agentsql_v240_verify TO agentsql_v240_ro"' | Out-Null
    }
    & docker rm -f $testContainer 2>$null | Out-Null
}

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
$signalDir = Join-Path $SignalRoot ('business-fault-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $signalDir | Out-Null
$testContainer = 'agentsql-v240-fault-' + [guid]::NewGuid().ToString('N').Substring(0, 8)
$stopped = $false

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
        --env AGENTSQL_TEST_OPENGAUSS_FAULT_DIR=/signals `
        --mount "type=bind,source=$repo,target=/src" `
        --mount "type=bind,source=$CertDirectory,target=/certs,readonly" `
        --mount "type=bind,source=$moduleCache,target=/go/pkg/mod" `
        --mount "type=bind,source=$buildCache,target=/root/.cache/go-build" `
        --mount "type=bind,source=$signalDir,target=/signals" `
        --workdir /src --env GOPROXY=off --env GOTOOLCHAIN=local `
        golang:1.26.8 go test -timeout 12m -count=1 -v `
        -run '^TestOpenGauss700LTSMCPRealE2E$' ./internal/mcpserver | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Could not start openGauss business fault test' }
    Wait-Signal 'ready' 300
    & docker stop $Instance | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Could not stop synthetic openGauss instance' }
    $stopped = $true
    Set-Content -LiteralPath (Join-Path $signalDir 'down') -Value 'stopped'
    Wait-Signal 'down_checked' 120
    & docker start $Instance | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Could not restart synthetic openGauss instance' }
    $stopped = $false
    $ready = $false
    for ($i = 0; $i -lt 60; $i++) {
        & docker exec $Instance su - omm -c '/usr/local/opengauss/bin/gsql -d postgres -p 5432 -t -A -c "SELECT 1"' *> $null
        $ready = $LASTEXITCODE -eq 0
        if ($ready) { break }
        Start-Sleep -Seconds 2
    }
    if (-not $ready) { throw 'Synthetic openGauss instance did not recover' }
    Set-Content -LiteralPath (Join-Path $signalDir 'up') -Value 'recovered'
    $deadline = (Get-Date).AddMinutes(3)
    while ((& docker inspect $testContainer --format '{{.State.Running}}').Trim() -eq 'true') {
        if ((Get-Date) -ge $deadline) { throw 'openGauss fault test did not finish' }
        Start-Sleep -Seconds 1
    }
    $exit = (& docker inspect $testContainer --format '{{.State.ExitCode}}').Trim()
    & docker logs $testContainer
    if ($exit -ne '0') { throw "openGauss business fault test failed, exit=$exit" }
    Write-Output 'V240_OPENGAUSS_BUSINESS_FAULT_RECOVERY=PASS'
}
finally {
    if ($stopped) { & docker start $Instance | Out-Null }
    & docker rm -f $testContainer 2>$null | Out-Null
}

param(
    [string]$AuditInstance = 'agentsql-v240-audit',
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
$signalDir = Join-Path $SignalRoot ('audit-fault-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $signalDir | Out-Null
$testContainer = 'agentsql-v240-audit-fault-' + [guid]::NewGuid().ToString('N').Substring(0, 8)
$stopped = $false
$createdDb = $false
$auditDb = 'agv240_' + [guid]::NewGuid().ToString('N').Substring(0, 10)
$auditEnv = Join-Path $signalDir 'audit-test.env'

function Wait-Signal([string]$Name, [int]$Seconds) {
    $path = Join-Path $signalDir $Name
    $deadline = (Get-Date).AddSeconds($Seconds)
    while (-not (Test-Path -LiteralPath $path)) {
        $running = (& docker inspect $testContainer --format '{{.State.Running}}').Trim()
        if ($running -ne 'true') {
            & docker logs $testContainer
            throw "openGauss audit fault test exited before $Name"
        }
        if ((Get-Date) -ge $deadline) { throw "Timed out waiting for $Name" }
        Start-Sleep -Seconds 1
    }
}

try {
    $passwordLine = [System.IO.File]::ReadAllLines((Join-Path $CertDirectory 'audit.env')) |
        Where-Object { $_ -like 'POSTGRES_PASSWORD=*' } | Select-Object -First 1
    if (-not $passwordLine) { throw 'Synthetic audit credential is unavailable' }
    $auditPassword = $passwordLine.Substring('POSTGRES_PASSWORD='.Length)
    $dsn = 'postgres://postgres:' + [uri]::EscapeDataString($auditPassword) +
        '@agentsql-v240-audit:5432/' + $auditDb + '?sslmode=disable'
    $lines = [string[]]@(Get-Content -LiteralPath $EnvironmentFile)
    $lines += 'AGENTSQL_TEST_OPENGAUSS_AUDIT_DSN=' + $dsn
    [System.IO.File]::WriteAllLines($auditEnv, $lines, [System.Text.UTF8Encoding]::new($false))
    $auditPassword = $null
    $dsn = $null
    & docker exec $AuditInstance psql -U postgres -d postgres -v ON_ERROR_STOP=1 -c "CREATE DATABASE $auditDb" | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Could not create isolated audit test database' }
    $createdDb = $true
    & docker run -d --name $testContainer --network $Network --env-file $auditEnv `
        --env AGENTSQL_TEST_OPENGAUSS_AUDIT_FAULT_DIR=/signals `
        --mount "type=bind,source=$repo,target=/src" `
        --mount "type=bind,source=$CertDirectory,target=/certs,readonly" `
        --mount "type=bind,source=$moduleCache,target=/go/pkg/mod" `
        --mount "type=bind,source=$buildCache,target=/root/.cache/go-build" `
        --mount "type=bind,source=$signalDir,target=/signals" `
        --workdir /src --env GOPROXY=off --env GOTOOLCHAIN=local `
        golang:1.26.8 go test -timeout 12m -count=1 -v `
        -run '^TestOpenGauss700LTSMCPRealE2E$' ./internal/mcpserver | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Could not start openGauss audit fault test' }
    Wait-Signal 'ready' 300
    & docker stop $AuditInstance | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Could not stop isolated audit database' }
    $stopped = $true
    Set-Content -LiteralPath (Join-Path $signalDir 'down') -Value 'stopped'
    Wait-Signal 'down_checked' 120
    & docker start $AuditInstance | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Could not restart isolated audit database' }
    $stopped = $false
    $ready = $false
    for ($i = 0; $i -lt 60; $i++) {
        & docker exec $AuditInstance pg_isready -U postgres -d agentsql_v240_audit *> $null
        $ready = $LASTEXITCODE -eq 0
        if ($ready) { break }
        Start-Sleep -Seconds 2
    }
    if (-not $ready) { throw 'Isolated audit database did not recover' }
    Set-Content -LiteralPath (Join-Path $signalDir 'up') -Value 'recovered'
    $deadline = (Get-Date).AddMinutes(3)
    while ((& docker inspect $testContainer --format '{{.State.Running}}').Trim() -eq 'true') {
        if ((Get-Date) -ge $deadline) { throw 'openGauss audit fault test did not finish' }
        Start-Sleep -Seconds 1
    }
    $exit = (& docker inspect $testContainer --format '{{.State.ExitCode}}').Trim()
    & docker logs $testContainer
    if ($exit -ne '0') { throw "openGauss audit fault test failed, exit=$exit" }
    Write-Output 'V240_OPENGAUSS_AUDIT_FAULT_RECOVERY=PASS'
}
finally {
    if ($stopped) { & docker start $AuditInstance | Out-Null }
    & docker rm -f $testContainer 2>$null | Out-Null
    if ($createdDb) {
        & docker exec $AuditInstance psql -U postgres -d postgres -v ON_ERROR_STOP=1 -c "DROP DATABASE IF EXISTS $auditDb WITH (FORCE)" *> $null
    }
    Remove-Item -LiteralPath $auditEnv -Force -ErrorAction SilentlyContinue
}

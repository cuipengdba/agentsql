param(
    [string]$EnvironmentFile = 'D:\CodexLS\tmp\agentsql-v250\mongo-env-private.list',
    [string]$CertDirectory = 'D:\CodexLS\tmp\agentsql-v250\mongo-runtime-certs',
    [string]$TempRoot = 'D:\CodexLS\tmp\agentsql-v250',
    [string]$MongoInstance = 'agentsql-v250-mongo',
    [string]$Network = 'agentsql-v250-net'
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$repo = Split-Path -Parent $PSScriptRoot
$moduleCache = 'D:\CodexLS\cache\agentsql-go-mod-linux'
$buildCache = 'D:\CodexLS\cache\agentsql-go-build-linux'
foreach ($path in @($EnvironmentFile, $CertDirectory, $moduleCache, $buildCache)) {
    if (-not (Test-Path -LiteralPath $path)) { throw "Missing MongoDB fault test input: $path" }
}
$suffix = [guid]::NewGuid().ToString('N').Substring(0, 10)
$signalDir = Join-Path $TempRoot "fault-$suffix"
New-Item -ItemType Directory -Path $signalDir | Out-Null
$auditContainer = "agentsql-v250-audit-$suffix"
$testContainer = "agentsql-v250-fault-test-$suffix"
$testEnv = Join-Path $signalDir 'test.env'
$auditEnv = Join-Path $signalDir 'audit.env'
$auditStopped = $false
$mongoStopped = $false

function Wait-Signal([string]$Name, [int]$Seconds) {
    $path = Join-Path $signalDir $Name
    $deadline = (Get-Date).AddSeconds($Seconds)
    while (-not (Test-Path -LiteralPath $path)) {
        $running = (& docker inspect $testContainer --format '{{.State.Running}}').Trim()
        if ($running -ne 'true') {
            & docker logs $testContainer
            throw "MongoDB fault test exited before $Name"
        }
        if ((Get-Date) -ge $deadline) { throw "Timed out waiting for $Name" }
        Start-Sleep -Seconds 1
    }
}

function Wait-Database([string]$Kind, [int]$Seconds) {
    $deadline = (Get-Date).AddSeconds($Seconds)
    while ((Get-Date) -lt $deadline) {
        if ($Kind -eq 'audit') {
            & docker exec $auditContainer pg_isready -U postgres -d postgres *> $null
        } else {
            & docker exec $MongoInstance mongosh --host mongo-v250.agentsql.test --tls `
                --tlsCAFile /etc/mongo-certs/ca.crt --quiet `
                --eval 'db.adminCommand({ping:1}).ok' *> $null
        }
        if ($LASTEXITCODE -eq 0) { return }
        Start-Sleep -Seconds 2
    }
    throw "Timed out waiting for $Kind database recovery"
}

try {
    $password = [Convert]::ToHexString([Security.Cryptography.RandomNumberGenerator]::GetBytes(24))
    [IO.File]::WriteAllLines($auditEnv, [string[]]@("POSTGRES_PASSWORD=$password"),
        [Text.UTF8Encoding]::new($false))
    $dsn = 'postgres://postgres:' + [uri]::EscapeDataString($password) +
        '@' + $auditContainer + ':5432/postgres?sslmode=disable'
    $lines = [string[]]@(Get-Content -LiteralPath $EnvironmentFile)
    $lines += 'AGENTSQL_TEST_MONGO_AUDIT_DSN=' + $dsn
    $lines += 'AGENTSQL_TEST_MONGO_AUDIT_FAULT_DIR=/signals'
    $lines += 'AGENTSQL_TEST_MONGO_BUSINESS_FAULT_DIR=/signals'
    [IO.File]::WriteAllLines($testEnv, $lines, [Text.UTF8Encoding]::new($false))
    $password = $null
    $dsn = $null

    & docker run -d --name $auditContainer --network $Network --env-file $auditEnv postgres:16-alpine | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Could not start isolated PostgreSQL audit database' }
    Wait-Database 'audit' 90

    & docker run -d --name $testContainer --network $Network --env-file $testEnv `
        --env AGENTSQL_TEST_MONGO_HOST=mongo-v250.agentsql.test `
        --env AGENTSQL_TEST_MONGO_SERVER_NAME=mongo-v250.agentsql.test `
        --env AGENTSQL_TEST_MONGO_CA=/mongo-certs/ca.crt `
        --mount "type=bind,source=$repo,target=/src" `
        --mount "type=bind,source=$CertDirectory,target=/mongo-certs,readonly" `
        --mount "type=bind,source=$moduleCache,target=/go/pkg/mod" `
        --mount "type=bind,source=$buildCache,target=/root/.cache/go-build" `
        --mount "type=bind,source=$signalDir,target=/signals" `
        --workdir /src --env GOTOOLCHAIN=local --env GOMAXPROCS=2 `
        --entrypoint sh golang:1.26.8 `
        -c 'export AGENTSQL_TEST_MONGO_RO_PASSWORD="$MONGO_RO_PASSWORD"; go test -timeout 12m -count=1 -v -run "^TestMongoControlledReadRealHTTPMCP$" ./internal/mcpserver' | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Could not start MongoDB fault test container' }

    Wait-Signal 'audit_ready' 300
    & docker stop $auditContainer | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Could not stop isolated audit database' }
    $auditStopped = $true
    Set-Content -LiteralPath (Join-Path $signalDir 'audit_down') -Value 'stopped'
    Wait-Signal 'audit_down_checked' 120
    & docker start $auditContainer | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Could not restart isolated audit database' }
    $auditStopped = $false
    Wait-Database 'audit' 90
    Set-Content -LiteralPath (Join-Path $signalDir 'audit_up') -Value 'recovered'

    Wait-Signal 'mongo_ready' 120
    & docker stop $MongoInstance | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Could not stop isolated MongoDB instance' }
    $mongoStopped = $true
    Set-Content -LiteralPath (Join-Path $signalDir 'mongo_down') -Value 'stopped'
    Wait-Signal 'mongo_down_checked' 120
    & docker start $MongoInstance | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Could not restart isolated MongoDB instance' }
    $mongoStopped = $false
    Wait-Database 'mongo' 120
    Set-Content -LiteralPath (Join-Path $signalDir 'mongo_up') -Value 'recovered'

    $deadline = (Get-Date).AddMinutes(3)
    while ((& docker inspect $testContainer --format '{{.State.Running}}').Trim() -eq 'true') {
        if ((Get-Date) -ge $deadline) { throw 'MongoDB fault test did not finish' }
        Start-Sleep -Seconds 1
    }
    $exit = (& docker inspect $testContainer --format '{{.State.ExitCode}}').Trim()
    & docker logs $testContainer
    if ($exit -ne '0') { throw "MongoDB fault test failed, exit=$exit" }
    Write-Output 'V250_MONGO_AUDIT_BUSINESS_FAULT_RECOVERY=PASS'
}
finally {
    if ($mongoStopped) { & docker start $MongoInstance | Out-Null }
    if ($auditStopped) { & docker start $auditContainer | Out-Null }
    & docker rm -f $testContainer $auditContainer 2>$null | Out-Null
    Remove-Item -LiteralPath $testEnv, $auditEnv -Force -ErrorAction SilentlyContinue
}

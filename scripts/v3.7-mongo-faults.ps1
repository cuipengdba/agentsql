param([string]$FixtureRoot = 'D:\CodexLS\tmp\agentsql-v370')

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$repo = Split-Path -Parent $PSScriptRoot
$faultDir = Join-Path $FixtureRoot ('faults-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $faultDir | Out-Null
$testContainer = 'agentsql-v370-faults-' + [guid]::NewGuid().ToString('N').Substring(0, 8)
$audit = 'agentsql-v370-audit-pg'
$mongo = @('agentsql-v370-mongo1', 'agentsql-v370-mongo2', 'agentsql-v370-mongo3')
$stopped = [System.Collections.Generic.List[string]]::new()
$auditPaused = $false

function Wait-Signal([string]$name) {
    $path = Join-Path $faultDir $name
    $deadline = (Get-Date).AddMinutes(6)
    while (-not (Test-Path -LiteralPath $path)) {
        if ((& docker inspect $testContainer --format '{{.State.Running}}').Trim() -ne 'true') {
            & docker logs $testContainer
            throw "MongoDB fault test exited before $name"
        }
        if ((Get-Date) -ge $deadline) { throw "Timed out waiting for $name" }
        Start-Sleep -Milliseconds 500
    }
}

try {
    $readyDeadline = (Get-Date).AddMinutes(2)
    do {
        & docker exec $audit pg_isready -U agentsql_audit -d agentsql_audit | Out-Null
        if ($LASTEXITCODE -eq 0) { break }
        if ((Get-Date) -ge $readyDeadline) { throw 'Audit PostgreSQL is not ready' }
        Start-Sleep -Seconds 2
    } while ($true)
    & docker run -d --name $testContainer --network agentsql-v370-net `
        --env-file (Join-Path $FixtureRoot 'audit/test.env') `
        --mount "type=bind,source=$repo,target=/src" `
        --mount "type=bind,source=$(Join-Path $FixtureRoot 'mongo'),target=/fixtures,readonly" `
        --mount "type=bind,source=$faultDir,target=/faults" `
        --mount 'type=bind,source=D:\CodexLS\cache\go-mod,target=/go/pkg/mod' `
        --mount 'type=bind,source=D:\CodexLS\cache\go-build,target=/root/.cache/go-build' `
        --workdir /src golang:1.26.10-bookworm `
        go test -timeout 10m -count=1 -v -run '^TestMongoReplicaControlledRealHTTPMCP$' ./internal/mcpserver | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Could not start MongoDB fault test' }
    Wait-Signal 'audit_ready'
    & docker pause $audit | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Could not pause audit database' }
    $auditPaused = $true
    Set-Content -LiteralPath (Join-Path $faultDir 'audit_down') -Value 'paused'
    Wait-Signal 'audit_down_checked'
    & docker unpause $audit | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Could not restore audit database' }
    $auditPaused = $false
    $readyDeadline = (Get-Date).AddMinutes(2)
    do {
        & docker exec $audit pg_isready -U agentsql_audit -d agentsql_audit | Out-Null
        if ($LASTEXITCODE -eq 0) { break }
        if ((Get-Date) -ge $readyDeadline) { throw 'Audit PostgreSQL did not recover' }
        Start-Sleep -Seconds 1
    } while ($true)
    Set-Content -LiteralPath (Join-Path $faultDir 'audit_up') -Value 'restored'
    Wait-Signal 'mongo_ready'
    foreach ($node in $mongo) {
        & docker stop -t 1 $node | Out-Null
        if ($LASTEXITCODE -ne 0) { throw "Could not stop $node" }
        $stopped.Add($node)
    }
    Set-Content -LiteralPath (Join-Path $faultDir 'mongo_down') -Value 'stopped'
    Wait-Signal 'mongo_down_checked'
    foreach ($node in $mongo) {
        & docker start $node | Out-Null
        if ($LASTEXITCODE -ne 0) { throw "Could not restore $node" }
        $stopped.Remove($node) | Out-Null
    }
    $primaryDeadline = (Get-Date).AddMinutes(2)
    do {
        $primary = $false
        foreach ($name in @('mongo1', 'mongo2', 'mongo3')) {
            $answer = & docker exec "agentsql-v370-$name" mongosh --quiet --tls --tlsCAFile /fixtures/ca.pem --host $name --eval 'db.hello().isWritablePrimary' 2>$null
            if ($LASTEXITCODE -eq 0 -and ($answer -join '').Trim() -eq 'true') { $primary = $true; break }
        }
        if ($primary) { break }
        if ((Get-Date) -ge $primaryDeadline) { throw 'MongoDB replica did not elect a primary' }
        Start-Sleep -Seconds 2
    } while ($true)
    Set-Content -LiteralPath (Join-Path $faultDir 'mongo_up') -Value 'restored'
    $deadline = (Get-Date).AddMinutes(3)
    while ((& docker inspect $testContainer --format '{{.State.Running}}').Trim() -eq 'true') {
        if ((Get-Date) -ge $deadline) { throw 'MongoDB fault test did not finish' }
        Start-Sleep -Seconds 1
    }
    $exitCode = (& docker inspect $testContainer --format '{{.State.ExitCode}}').Trim()
    & docker logs $testContainer
    if ($exitCode -ne '0') { throw "MongoDB fault test failed, exit=$exitCode" }
    Write-Output 'V370_MONGO_AUDIT_AND_BUSINESS_FAULTS=PASS'
} finally {
    if ($auditPaused) { & docker unpause $audit 2>$null | Out-Null }
    foreach ($node in $stopped) { & docker start $node 2>$null | Out-Null }
    & docker rm -f $testContainer 2>$null | Out-Null
}

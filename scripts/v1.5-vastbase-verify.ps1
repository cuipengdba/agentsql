param(
    [string]$Container = 'agentsql-v070-g100',
    [string]$GoCache = 'D:\CodexLS\cache\go',
    [string]$LogPath = 'D:\CodexLS\tmp\agentsql-v150\vastbase-verify.log',
    [switch]$FaultProbe,
    [switch]$SkipBaseline
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$repoRoot = Split-Path -Parent $PSScriptRoot
if ($SkipBaseline -and -not $FaultProbe) { throw '-SkipBaseline requires -FaultProbe' }
if (-not (Test-Path -LiteralPath $GoCache -PathType Container)) { throw "Go cache missing: $GoCache" }
$initialState = (& docker inspect $Container --format '{{.State.Status}}').Trim()
if ($LASTEXITCODE -ne 0 -or $initialState -notin @('running', 'exited')) {
    throw "Expected isolated Vastbase G100 container is unavailable: $Container"
}
New-Item -ItemType Directory -Path (Split-Path -Parent $LogPath) -Force | Out-Null
Start-Transcript -LiteralPath $LogPath -Force | Out-Null
$startedHere = $false
$password = $null
try {
    if ($initialState -ne 'running') {
        & docker start $Container | Out-Null
        if ($LASTEXITCODE -ne 0) { throw 'Could not start isolated Vastbase container' }
        $startedHere = $true
    }
    $initialProbe = @(& docker exec -u vastbase -e GAUSSHOME=/home/vastbase/local/vastbase `
        -e LD_LIBRARY_PATH=/home/vastbase/local/vastbase/lib $Container `
        /home/vastbase/local/vastbase/bin/vsql -d vastbase -X -q -t -A -c 'SELECT 1;' 2>&1)
    if ($LASTEXITCODE -ne 0 -or ($initialProbe -join '').Trim() -ne '1') {
        & docker exec -u vastbase -e GAUSSHOME=/home/vastbase/local/vastbase `
            -e LD_LIBRARY_PATH=/home/vastbase/local/vastbase/lib $Container `
            /home/vastbase/local/vastbase/bin/vb_ctl start -D /home/vastbase/data/vastbase `
            -Z single_node -l /home/vastbase/data/vastbase/server.log | Out-Null
        if ($LASTEXITCODE -ne 0) { throw 'Could not start Vastbase server in isolated container' }
    }
    $ready = $false
    for ($attempt = 0; $attempt -lt 90; $attempt++) {
        $state = (& docker inspect $Container --format '{{.State.Status}}').Trim()
        if ($state -ne 'running') { throw 'Vastbase container exited during readiness' }
        $probe = @(& docker exec -u vastbase -e GAUSSHOME=/home/vastbase/local/vastbase `
            -e LD_LIBRARY_PATH=/home/vastbase/local/vastbase/lib $Container `
            /home/vastbase/local/vastbase/bin/vsql -d vastbase -X -q -t -A -c 'SELECT 1;' 2>&1)
        if ($LASTEXITCODE -eq 0 -and ($probe -join '').Trim() -eq '1') { $ready = $true; break }
        Start-Sleep -Seconds 2
    }
    if (-not $ready) { throw 'Vastbase did not become ready within 180 seconds' }

    $bytes = [byte[]]::new(10)
    [System.Security.Cryptography.RandomNumberGenerator]::Fill($bytes)
    $password = 'Aa1!' + [Convert]::ToHexString($bytes).ToLowerInvariant()
    & docker exec -u vastbase -e GAUSSHOME=/home/vastbase/local/vastbase `
        -e LD_LIBRARY_PATH=/home/vastbase/local/vastbase/lib $Container `
        /home/vastbase/local/vastbase/bin/vsql -d vastbase -X -q -v ON_ERROR_STOP=1 `
        -c "ALTER ROLE agentsql_ro PASSWORD '$password';" | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Could not prepare isolated read-only account' }

    if (-not $SkipBaseline) {
        & docker run --rm --network "container:$Container" `
            -v "${repoRoot}:/src:ro" -v "${GoCache}:/go/cache" `
            -w /src -e GOCACHE=/go/cache/build -e GOMODCACHE=/go/cache/mod -e GOPROXY=off `
            -e AGENTSQL_TEST_G100_HOST=127.0.0.1 -e AGENTSQL_TEST_G100_PORT=5432 `
            -e "AGENTSQL_TEST_G100_PASSWORD=$password" `
            golang:1.26-bookworm go test -mod=readonly -p 1 -timeout 10m -count=1 -v `
            -run '^TestVastbaseBuild9(Integration|PipelineRealE2E|MCPRealE2E)$' `
            ./internal/authorizedexecute/internal/businessdb ./internal/pipeline ./internal/mcpserver
        if ($LASTEXITCODE -ne 0) { throw 'Vastbase G100 Build 9 live regression failed' }
        Write-Output 'V150_VASTBASE_BUILD9_BASELINE=PASS'
    }
    if ($FaultProbe) {
        $faultDir = Join-Path 'D:\CodexLS\tmp\agentsql-v150' ('vastbase-fault-' + [guid]::NewGuid().ToString('N'))
        New-Item -ItemType Directory -Path $faultDir -Force | Out-Null
        $faultContainer = 'agentsql-v150-g100-fault-' + [guid]::NewGuid().ToString('N').Substring(0, 8)
        & docker run -d --name $faultContainer --network "container:$Container" `
            -v "${repoRoot}:/src:ro" -v "${GoCache}:/go/cache" -v "${faultDir}:/fault" `
            -w /src -e GOCACHE=/go/cache/build -e GOMODCACHE=/go/cache/mod -e GOPROXY=off `
            -e AGENTSQL_TEST_G100_HOST=127.0.0.1 -e AGENTSQL_TEST_G100_PORT=5432 `
            -e "AGENTSQL_TEST_G100_PASSWORD=$password" -e AGENTSQL_TEST_G100_FAULT_DIR=/fault `
            golang:1.26-bookworm go test -mod=readonly -p 1 -timeout 10m -count=1 -v `
            -run '^TestVastbaseBuild9MCPRealE2E$' ./internal/mcpserver | Out-Null
        if ($LASTEXITCODE -ne 0) { throw 'Could not start Vastbase fault regression' }
        try {
            foreach ($phase in @('ready', 'down_checked')) {
                $deadline = (Get-Date).AddMinutes(8)
                while (-not (Test-Path -LiteralPath (Join-Path $faultDir $phase))) {
                    $running = (& docker inspect $faultContainer --format '{{.State.Running}}').Trim()
                    if ($LASTEXITCODE -ne 0 -or $running -ne 'true') {
                        & docker logs $faultContainer
                        throw "Vastbase fault regression exited before signal $phase"
                    }
                    if ((Get-Date) -ge $deadline) { throw "Timed out waiting for Vastbase fault signal $phase" }
                    Start-Sleep -Seconds 1
                }
                if ($phase -eq 'ready') {
                    & docker exec -u vastbase -e GAUSSHOME=/home/vastbase/local/vastbase `
                        -e LD_LIBRARY_PATH=/home/vastbase/local/vastbase/lib $Container `
                        /home/vastbase/local/vastbase/bin/vb_ctl stop -D /home/vastbase/data/vastbase -m fast | Out-Null
                    if ($LASTEXITCODE -ne 0) { throw 'Could not stop Vastbase server for fault probe' }
                    Set-Content -LiteralPath (Join-Path $faultDir 'down') -Value 'server stopped'
                }
            }
            & docker exec -u vastbase -e GAUSSHOME=/home/vastbase/local/vastbase `
                -e LD_LIBRARY_PATH=/home/vastbase/local/vastbase/lib $Container `
                /home/vastbase/local/vastbase/bin/vb_ctl start -D /home/vastbase/data/vastbase `
                -Z single_node -l /home/vastbase/data/vastbase/server.log | Out-Null
            if ($LASTEXITCODE -ne 0) { throw 'Could not restart Vastbase server after fault probe' }
            Set-Content -LiteralPath (Join-Path $faultDir 'up') -Value 'server restarted'
            $deadline = (Get-Date).AddMinutes(2)
            while ((& docker inspect $faultContainer --format '{{.State.Running}}').Trim() -eq 'true') {
                if ((Get-Date) -ge $deadline) { throw 'Vastbase recovery regression did not exit' }
                Start-Sleep -Seconds 1
            }
            $faultExit = (& docker inspect $faultContainer --format '{{.State.ExitCode}}').Trim()
            & docker logs $faultContainer
            if ($faultExit -ne '0') { throw "Vastbase fault regression failed: exit=$faultExit" }
            Write-Output 'V150_VASTBASE_BUILD9_FAULT_RECOVERY=PASS'
        }
        finally {
            & docker rm -f $faultContainer | Out-Null
        }
    }
}
finally {
    if ($startedHere) { & docker stop $Container | Out-Null }
    $password = $null
    Stop-Transcript | Out-Null
}

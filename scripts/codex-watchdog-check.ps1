$ErrorActionPreference = 'Stop'
$runtime = 'D:\ruanjiansheji'
$statePath = Join-Path $runtime 'current-batch.json'
$flagPath = Join-Path $runtime 'batch-done.flag'

function Report {
    param([int]$Code, [string]$Message)
    Write-Output ("code={0} {1}" -f $Code, $Message)
    exit $Code
}

function Parse-Time {
    param($Value)
    if ($null -eq $Value -or $Value -isnot [string] -or [string]::IsNullOrWhiteSpace($Value)) {
        throw 'Missing timestamp'
    }
    return [DateTimeOffset]::Parse($Value, [System.Globalization.CultureInfo]::InvariantCulture)
}

try {
    if (-not (Test-Path -LiteralPath $statePath -PathType Leaf)) { Report 3 'missing current-batch.json' }
    $stateFile = Get-Item -LiteralPath $statePath
    $state = Get-Content -LiteralPath $statePath -Raw -Encoding UTF8 | ConvertFrom-Json
    if ($null -eq $state -or $state.batch -isnot [int] -or $state.pid -isnot [int] -or $state.status -notin @('running', 'stalled', 'done', 'failed')) {
        Report 3 'invalid current-batch.json fields'
    }
    if ($state.batch -lt 1 -or $state.pid -lt 1 -or $state.head -notmatch '^[0-9a-fA-F]{40,64}$' -or [string]::IsNullOrWhiteSpace($state.task_file)) {
        Report 3 'invalid batch identity'
    }
    $startedAt = Parse-Time $state.started_at
    $expectedAt = Parse-Time $state.expected_done_at
    if ($expectedAt -le $startedAt) { Report 3 'invalid expected completion time' }
    $stateAge = ([DateTime]::UtcNow - $stateFile.LastWriteTimeUtc).TotalMinutes
    if ($stateAge -lt -2) { Report 3 'state file timestamp is in the future' }

    $outPath = Join-Path $runtime ("codex-batch{0}-run.log" -f $state.batch)
    $errPath = Join-Path $runtime ("codex-batch{0}-err.log" -f $state.batch)
    if (-not (Test-Path -LiteralPath $outPath -PathType Leaf) -or -not (Test-Path -LiteralPath $errPath -PathType Leaf)) {
        Report 3 'missing batch log'
    }
    $outTime = (Get-Item -LiteralPath $outPath).LastWriteTimeUtc
    $errTime = (Get-Item -LiteralPath $errPath).LastWriteTimeUtc
    if ($outTime -gt $errTime) { $lastLog = $outTime } else { $lastLog = $errTime }
    $logAge = ([DateTime]::UtcNow - $lastLog).TotalMinutes
    if ($logAge -lt -2) { Report 3 'log timestamp is in the future' }
    if ($null -ne $state.last_log_update) {
        $recordedLog = (Parse-Time $state.last_log_update).UtcDateTime
        if ([Math]::Abs(($recordedLog - $lastLog).TotalMinutes) -gt 2) { Report 3 'recorded log time disagrees with logs' }
    }

    $process = Get-Process -Id $state.pid -ErrorAction SilentlyContinue
    $alive = $null -ne $process -and $process.ProcessName -eq 'codex'
    $flagExists = Test-Path -LiteralPath $flagPath -PathType Leaf
    $flag = $null
    if ($flagExists) {
        $flag = Get-Content -LiteralPath $flagPath -Raw -Encoding UTF8 | ConvertFrom-Json
        if ($null -eq $flag -or $flag.batch -ne $state.batch -or $flag.status -notin @('done', 'failed')) {
            Report 3 'completion flag does not match current batch'
        }
    }

    if ($state.status -eq 'running') {
        if ($flagExists -or -not $alive) { Report 3 ("running state contradicts process/flag; batch={0} pid={1}" -f $state.batch, $state.pid) }
        if ($stateAge -gt 3) { Report 3 ("runner heartbeat stale; batch={0} pid={1} state_age_min={2:N1}" -f $state.batch, $state.pid, $stateAge) }
        if ($logAge -gt 30) { Report 2 ("log stalled; batch={0} pid={1} log_age_min={2:N1}" -f $state.batch, $state.pid, $logAge) }
        Report 0 ("running; batch={0} pid={1} log_age_min={2:N1} state_age_min={3:N1}" -f $state.batch, $state.pid, $logAge, $stateAge)
    }
    if ($state.status -eq 'stalled') {
        if ($flagExists) { Report 3 'stalled batch has completion flag' }
        Report 2 ("stalled; batch={0} pid={1} alive={2} log_age_min={3:N1}" -f $state.batch, $state.pid, $alive, $logAge)
    }
    if ($state.status -eq 'failed') {
        Report 2 ("failed; batch={0} pid={1} exit_code={2} flag={3}" -f $state.batch, $state.pid, $state.exit_code, $flagExists)
    }
    if ($alive -or -not $flagExists -or $flag.status -ne 'done' -or $state.exit_code -ne 0 -or $null -eq $state.ended_at) {
        Report 3 'done state contradicts process/flag/exit code'
    }
    [void](Parse-Time $state.ended_at)
    Report 1 ("done; batch={0} pid={1} exit_code=0 flag=pending" -f $state.batch, $state.pid)
} catch {
    Report 3 ("unknown state: {0}" -f ($_.Exception.Message -replace '[\r\n]+', ' '))
}

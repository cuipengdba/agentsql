param(
    [Parameter(Mandatory = $true)][ValidateRange(1, 2147483647)][int]$BatchNumber,
    [Parameter(Mandatory = $true)][ValidateNotNullOrEmpty()][string]$TaskFile,
    [ValidateRange(1, 2147483647)][int]$ExpectedMinutes = 90
)

$ErrorActionPreference = 'Stop'
$repo = 'D:\ruanjiansheji\agentsql-v04'
$runtime = 'D:\ruanjiansheji'
$codex = 'C:\Users\Administrator\AppData\Local\OpenAI\Codex\bin\f544b3844e0f14e9\codex.exe'
$statePath = Join-Path $runtime 'current-batch.json'
$flagPath = Join-Path $runtime 'batch-done.flag'
$pidPath = Join-Path $runtime ("codex-batch{0}.pid" -f $BatchNumber)
$outPath = Join-Path $runtime ("codex-batch{0}-run.log" -f $BatchNumber)
$errPath = Join-Path $runtime ("codex-batch{0}-err.log" -f $BatchNumber)
$utf8 = New-Object System.Text.UTF8Encoding($false)

function Save-State {
    param([object]$Value)
    $tempPath = "{0}.{1}.tmp" -f $statePath, $PID
    $backupPath = "{0}.{1}.bak" -f $statePath, $PID
    [System.IO.File]::WriteAllText($tempPath, ($Value | ConvertTo-Json -Depth 4), $utf8)
    if ([System.IO.File]::Exists($statePath)) {
        [System.IO.File]::Replace($tempPath, $statePath, $backupPath)
        [System.IO.File]::Delete($backupPath)
    } else {
        [System.IO.File]::Move($tempPath, $statePath)
    }
}

function Get-NewestLogTime {
    $outTime = [System.IO.File]::GetLastWriteTimeUtc($outPath)
    $errTime = [System.IO.File]::GetLastWriteTimeUtc($errPath)
    if ($outTime -gt $errTime) { return $outTime }
    return $errTime
}

if (-not (Test-Path -LiteralPath $codex -PathType Leaf)) { throw "Codex executable missing: $codex" }
if (-not (Test-Path -LiteralPath $TaskFile -PathType Leaf)) { throw "Task file missing: $TaskFile" }
if (Test-Path -LiteralPath $flagPath) { throw "Unprocessed completion flag: $flagPath" }
if (Test-Path -LiteralPath $statePath) {
    try {
        $old = Get-Content -LiteralPath $statePath -Raw -Encoding UTF8 | ConvertFrom-Json
        if ($old.status -in @('running', 'stalled')) {
            $oldProcess = Get-Process -Id ([int]$old.pid) -ErrorAction SilentlyContinue
            if ($null -ne $oldProcess) { throw "Previous batch may still be active: $($old.batch), PID $($old.pid)" }
        }
    } catch {
        throw "Cannot safely replace current batch state: $($_.Exception.Message)"
    }
}

# Read-only HEAD lookup. No index, worktree, or ref is changed.
$head = (& git -C $repo rev-parse HEAD 2>&1)
if ($LASTEXITCODE -ne 0 -or @($head).Count -ne 1 -or $head -notmatch '^[0-9a-fA-F]{40,64}$') {
    throw "Cannot identify repository HEAD: $head"
}
$taskBytes = [System.IO.File]::ReadAllBytes($TaskFile)
$taskPath = [System.IO.Path]::GetFullPath($TaskFile)

$psi = New-Object System.Diagnostics.ProcessStartInfo
$psi.FileName = $codex
$psi.WorkingDirectory = $repo
$psi.Arguments = 'exec --cd "' + $repo + '" --approve-for-me --color never -m gpt-6-sol -'
$psi.UseShellExecute = $false
$psi.CreateNoWindow = $true
$psi.RedirectStandardInput = $true
$psi.RedirectStandardOutput = $true
$psi.RedirectStandardError = $true
$psi.StandardOutputEncoding = [System.Text.Encoding]::UTF8
$psi.StandardErrorEncoding = [System.Text.Encoding]::UTF8

$process = New-Object System.Diagnostics.Process
$process.StartInfo = $psi
$outStream = New-Object System.IO.FileStream($outPath, [System.IO.FileMode]::Create, [System.IO.FileAccess]::Write, [System.IO.FileShare]::ReadWrite, 1, $true)
$errStream = New-Object System.IO.FileStream($errPath, [System.IO.FileMode]::Create, [System.IO.FileAccess]::Write, [System.IO.FileShare]::ReadWrite, 1, $true)
$stdoutTask = $null
$stderrTask = $null
$started = $false

try {
    [void]$process.Start()
    $started = $true
    # .NET stream copies run on I/O threads while Wait-Process blocks the PowerShell thread.
    $stdoutTask = $process.StandardOutput.BaseStream.CopyToAsync($outStream)
    $stderrTask = $process.StandardError.BaseStream.CopyToAsync($errStream)
    $startedAt = [DateTime]::UtcNow
    $state = [ordered]@{
        batch = $BatchNumber
        pid = $process.Id
        head = [string]$head
        task_file = $taskPath
        started_at = $startedAt.ToString('o')
        expected_done_at = $startedAt.AddMinutes($ExpectedMinutes).ToString('o')
        status = 'running'
        last_log_update = (Get-NewestLogTime).ToString('o')
        exit_code = $null
        ended_at = $null
    }
    [System.IO.File]::WriteAllText($pidPath, [string]$process.Id, $utf8)
    Save-State $state
    try {
        $process.StandardInput.BaseStream.Write($taskBytes, 0, $taskBytes.Length)
        $process.StandardInput.BaseStream.Flush()
    } finally {
        $process.StandardInput.BaseStream.Close()
    }

    while (-not $process.HasExited) {
        Wait-Process -Id $process.Id -Timeout 60 -ErrorAction SilentlyContinue
        $lastLog = Get-NewestLogTime
        $state.last_log_update = $lastLog.ToString('o')
        if (-not $process.HasExited -and ([DateTime]::UtcNow - $lastLog).TotalMinutes -gt 30) {
            $state.status = 'stalled'
        }
        Save-State $state
    }
    $process.WaitForExit()
    [void]$stdoutTask.GetAwaiter().GetResult() # Drain both I/O copies before final log timestamp.
    [void]$stderrTask.GetAwaiter().GetResult()
    $state.last_log_update = (Get-NewestLogTime).ToString('o')
    $state.exit_code = $process.ExitCode
    $state.ended_at = [DateTime]::UtcNow.ToString('o')
    if ($process.ExitCode -eq 0) { $state.status = 'done' } else { $state.status = 'failed' }
    Save-State $state
    [System.IO.File]::WriteAllText($flagPath, (@{ batch = $BatchNumber; status = $state.status } | ConvertTo-Json -Compress), $utf8)
    Write-Output ("batch={0} pid={1} status={2} exit_code={3}" -f $BatchNumber, $process.Id, $state.status, $process.ExitCode)
} catch {
    if ($started -and $null -ne $state) {
        try {
            $state.status = 'failed'
            $state.ended_at = [DateTime]::UtcNow.ToString('o')
            if ($process.HasExited) { $state.exit_code = $process.ExitCode }
            Save-State $state
            [System.IO.File]::WriteAllText($flagPath, (@{ batch = $BatchNumber; status = 'failed' } | ConvertTo-Json -Compress), $utf8)
        } catch { Write-Error "Could not persist failed state: $($_.Exception.Message)" }
    }
    throw
} finally {
    $outStream.Close()
    $errStream.Close()
    $process.Dispose()
}

param(
    [Parameter(Mandatory = $true, ParameterSetName = 'Single')][ValidateRange(1, 2147483647)][int]$BatchNumber,
    [Parameter(Mandatory = $true, ParameterSetName = 'Single')][ValidateNotNullOrEmpty()][string]$TaskFile,
    [Parameter(ParameterSetName = 'Single')][ValidateRange(1, 2147483647)][int]$ExpectedMinutes = 90,
    [Parameter(Mandatory = $true, ParameterSetName = 'Chain')][ValidateNotNullOrEmpty()][string]$ChainFile,
    [Parameter(ParameterSetName = 'Chain')][ValidateRange(1, 2147483647)][int]$StartBatch,
    [Parameter(ParameterSetName = 'Chain')][string]$CodexExecutable,
    [Parameter(ParameterSetName = 'Chain')][string]$LogDirectory
)

$ErrorActionPreference = 'Stop'
if ($PSCmdlet.ParameterSetName -eq 'Chain') {
    $chainUtf8 = New-Object System.Text.UTF8Encoding($false)

    function Resolve-ChainPath {
        param([string]$Path, [string]$Repository)
        if ([string]::IsNullOrWhiteSpace($Path)) { throw 'Empty chain path' }
        if ([System.IO.Path]::IsPathRooted($Path)) { return [System.IO.Path]::GetFullPath($Path) }
        return [System.IO.Path]::GetFullPath((Join-Path $Repository $Path))
    }
    function Resolve-CheckPath {
        param([string]$Path, [string]$Repository)
        if ([string]::IsNullOrWhiteSpace($Path) -or [System.IO.Path]::IsPathRooted($Path)) { throw 'Check path must be repository-relative' }
        $full = [System.IO.Path]::GetFullPath((Join-Path $Repository $Path))
        if (-not $full.StartsWith(($Repository.TrimEnd('\') + '\'), [StringComparison]::OrdinalIgnoreCase)) { throw 'Check path escapes repository' }
        Assert-SafeWritePath $full $Repository
        return $full
    }
    function Assert-SafeWritePath {
        param([string]$Path, [string]$Repository)
        $relative = $Path.Substring([Math]::Min($Path.Length, $Repository.Length)).TrimStart('\', '/')
        if ($Path.StartsWith(($Repository.TrimEnd('\') + '\'), [StringComparison]::OrdinalIgnoreCase) -and
            ($relative -match '^(?i:cmd[\\/]agentsql[\\/]b5-wal)([\\/]|$)' -or $relative -match '^(?i:cmd[\\/]agentsql[\\/]).*memory.*\.instance-id$')) {
            throw 'Path targets protected repository data'
        }
    }
    function Test-OrdinaryFile {
        param([string]$Path)
        if (-not [System.IO.File]::Exists($Path)) { return $false }
        return (([System.IO.File]::GetAttributes($Path) -band [System.IO.FileAttributes]::ReparsePoint) -eq 0)
    }
    function Write-ChainState {
        param([object]$Value, [string]$Path)
        $parent = [System.IO.Path]::GetDirectoryName($Path)
        if (-not [System.IO.Directory]::Exists($parent)) { [void][System.IO.Directory]::CreateDirectory($parent) }
        $temp = '{0}.{1}.tmp' -f $Path, $PID
        $backup = '{0}.{1}.bak' -f $Path, $PID
        [System.IO.File]::WriteAllText($temp, ($Value | ConvertTo-Json -Depth 20), $chainUtf8)
        if ([System.IO.File]::Exists($Path)) {
            [System.IO.File]::Replace($temp, $Path, $backup)
            [System.IO.File]::Delete($backup)
        } else { [System.IO.File]::Move($temp, $Path) }
    }
    function Append-Handoff {
        param([string]$Path, [string]$Text)
        $parent = [System.IO.Path]::GetDirectoryName($Path)
        if (-not [System.IO.Directory]::Exists($parent)) { [void][System.IO.Directory]::CreateDirectory($parent) }
        [System.IO.File]::AppendAllText($Path, $Text, $chainUtf8)
    }
    function Get-Snapshot {
        param([string]$Repository)
        $lines = @(& git -C $Repository status --porcelain=v1 --untracked-files=all)
        if ($LASTEXITCODE -ne 0) { throw 'git status snapshot failed' }
        return $lines
    }
    function Get-ChangedFiles {
        param([string[]]$Before, [string[]]$After)
        $seen = New-Object 'System.Collections.Generic.HashSet[string]' ([StringComparer]::Ordinal)
        foreach ($line in $Before) { [void]$seen.Add($line) }
        $changes = @()
        foreach ($line in $After) {
            if ($seen.Contains($line) -or $line.Length -lt 4) { continue }
            $xy = $line.Substring(0, 2)
            if ($xy -match 'D' -or $xy -eq '!!') { continue }
            $kind = if ($xy -eq '??') { 'untracked_added' } else { 'tracked_modified' }
            $changes += [ordered]@{ path = $line.Substring(3); kind = $kind; status = $xy }
        }
        return $changes
    }
    function Get-LogTime {
        param([string]$OutPath, [string]$ErrPath)
        $a = [System.IO.File]::GetLastWriteTimeUtc($OutPath)
        $b = [System.IO.File]::GetLastWriteTimeUtc($ErrPath)
        if ($a -gt $b) { return $a }
        return $b
    }
    function Test-ReadOnlyCommand {
        param([string]$Command)
        if ([string]::IsNullOrWhiteSpace($Command)) { return $false }
        # One literal command only. Block PowerShell composition and substitutions before allowlisting.
        if ($Command -notmatch '^[A-Za-z0-9_./:\\=+% -]+$' -or
            $Command -match '\b(git\s+(add|commit|merge|rebase|tag|push|reset|checkout|restore|stash|clean)|gh\s+(workflow|release\s+(create|edit|upload|delete))|docker\s+(rm|rmi|stop|start|kill|push))\b' -or
            $Command -match '--(output|ext-diff|textconv|no-index|push|load|input|web)(=|\s|$)' -or
            $Command -match '(b5-wal|instance-id|private.key|license|secret|token)') { return $false }
        if ($Command -match '^git\s+(status|diff|log|show|rev-parse|ls-remote)(\s|$)') { return $true }
        if ($Command -match '^docker\s+(inspect|ps|pull)(\s|$)' -or $Command -match '^docker\s+buildx\s+(ls|inspect|imagetools\s+inspect)(\s|$)') { return $true }
        if ($Command -match '^gh\s+release\s+view(\s|$)' -or $Command -match '^gh\s+api\s+(--method\s+GET\s+|GET\s+)' -or $Command -match '^gh\s+package(s)?\s+(view|list)(\s|$)') { return $true }
        if ($Command -match '^\.\\scripts\\[A-Za-z0-9_.-]*(verify|check|test|audit)[A-Za-z0-9_.-]*\.ps1(\s+[A-Za-z0-9_./:=\\-]+)*$') { return $true }
        return $false
    }
    function Invoke-CheckedCommand {
        param([string]$Command, [int]$TimeoutSeconds, [string]$Repository)
        if (-not (Test-ReadOnlyCommand $Command)) { throw 'Command rejected by read-only allowlist' }
        if ($TimeoutSeconds -lt 1) { throw 'Invalid command timeout' }
        $psi = New-Object System.Diagnostics.ProcessStartInfo
        $psi.FileName = 'powershell.exe'
        $psi.Arguments = '-NoProfile -NonInteractive -Command "' + $Command.Replace('"', '\"') + '"'
        $psi.WorkingDirectory = $Repository
        $psi.UseShellExecute = $false
        $psi.CreateNoWindow = $true
        $psi.RedirectStandardOutput = $true
        $psi.RedirectStandardError = $true
        $p = New-Object System.Diagnostics.Process
        $p.StartInfo = $psi
        try {
            [void]$p.Start()
            $stdoutTask = $p.StandardOutput.ReadToEndAsync()
            $stderrTask = $p.StandardError.ReadToEndAsync()
            if (-not $p.WaitForExit($TimeoutSeconds * 1000)) {
                & taskkill /PID $p.Id /T /F 2>$null | Out-Null
                throw 'Command timed out'
            }
            $p.WaitForExit()
            return [ordered]@{ exitCode = $p.ExitCode; stdout = $stdoutTask.Result; stderr = $stderrTask.Result }
        } finally { $p.Dispose() }
    }
    function Count-MarkerLines {
        param([string]$Text, [string]$Marker)
        if ([string]::IsNullOrEmpty($Marker)) { throw 'Empty marker' }
        $count = 0
        foreach ($line in ($Text -split '\r?\n')) { if ($line.Contains($Marker)) { $count++ } }
        return $count
    }
    function Get-SafeOutputTail {
        param([string]$Text)
        $tail = $Text
        if ($tail.Length -gt 2000) { $tail = $tail.Substring($tail.Length - 2000) }
        $lines = @()
        foreach ($line in ($tail -split '\r?\n')) {
            if ($line -match '(?i)(-----BEGIN .*PRIVATE KEY|(?:token|license|secret|password|private.?key|api.?key)\s*["'']?\s*[:=]|\b(?:ghp_|sk-)[A-Za-z0-9_-]{12,})') { $lines += '[REDACTED]' }
            else { $lines += $line }
        }
        return ($lines -join "`n")
    }
    function Invoke-ChainCheck {
        param([object]$Check, [string]$Repository)
        $result = [ordered]@{ type = [string]$Check.type; target = ''; passed = $false; actual = ''; timestamp = [DateTime]::UtcNow.ToString('o') }
        try {
            $type = [string]$Check.type
            if ($type -eq 'fileExists') {
                $result.target = [string]$Check.path
                $path = Resolve-CheckPath $Check.path $Repository
                $result.passed = Test-OrdinaryFile $path
                $result.actual = if ($result.passed) { 'file exists' } else { 'file missing' }
            } elseif ($type -eq 'fileCount' -or $type -eq 'exactFiles') {
                $result.target = [string]$Check.dir
                $dir = Resolve-CheckPath $Check.dir $Repository
                if (-not [System.IO.Directory]::Exists($dir)) { throw 'Directory missing' }
                $files = @([System.IO.Directory]::GetFiles($dir) | Where-Object { Test-OrdinaryFile $_ } | ForEach-Object { [System.IO.Path]::GetFileName($_) })
                if ($type -eq 'fileCount') {
                    if ($null -eq $Check.count -or [int]$Check.count -lt 0) { throw 'Invalid count' }
                    $result.actual = [string]$files.Count
                    $result.passed = ($files.Count -eq [int]$Check.count)
                } else {
                    if ($null -eq $Check.names) { throw 'Missing names' }
                    $names = @($Check.names)
                    if (@($names | Where-Object { $_ -isnot [string] -or [string]::IsNullOrEmpty($_) -or $_ -match '[\\/]' }).Count -gt 0) { throw 'Invalid basename' }
                    $actualNames = @($files | Sort-Object -CaseSensitive)
                    $expectedNames = @($names | Sort-Object -CaseSensitive)
                    $result.actual = ($actualNames -join ', ')
                    $result.passed = ($actualNames.Count -eq $expectedNames.Count -and [string]::Equals(($actualNames -join "`n"), ($expectedNames -join "`n"), [StringComparison]::Ordinal))
                }
            } elseif ($type -eq 'logContains') {
                $result.target = [string]$Check.file
                $path = Resolve-CheckPath $Check.file $Repository
                if (-not [System.IO.File]::Exists($path)) { throw 'Log file missing' }
                $min = if ($null -eq $Check.minCount) { 1 } else { [int]$Check.minCount }
                if ($min -lt 1) { throw 'Invalid minCount' }
                $count = Count-MarkerLines ([System.IO.File]::ReadAllText($path)) ([string]$Check.marker)
                $result.actual = [string]$count
                $result.passed = ($count -ge $min)
            } elseif ($type -eq 'commandSucceeds' -or $type -eq 'commandOutputs') {
                $result.target = [string]$Check.command
                $timeout = if ($null -eq $Check.timeoutSeconds) { 600 } else { [int]$Check.timeoutSeconds }
                $output = Invoke-CheckedCommand ([string]$Check.command) $timeout $Repository
                $tail = Get-SafeOutputTail ([string]$output.stdout)
                $stderrTail = Get-SafeOutputTail ([string]$output.stderr)
                $result.actual = "exitCode=$($output.exitCode); stdoutTail=$tail; stderrTail=$stderrTail"
                $result.passed = ($output.exitCode -eq 0)
                if ($type -eq 'commandOutputs') {
                    $min = if ($null -eq $Check.minCount) { 1 } else { [int]$Check.minCount }
                    if ($min -lt 1) { throw 'Invalid minCount' }
                    $count = Count-MarkerLines ([string]$output.stdout) ([string]$Check.marker)
                    $result.actual += "; markerLines=$count"
                    $result.passed = ($result.passed -and $count -ge $min)
                }
            } elseif ($type -eq 'jsonEquals') {
                $result.target = '{0}:{1}' -f $Check.file, $Check.jsonPath
                $path = Resolve-CheckPath $Check.file $Repository
                if (-not [System.IO.File]::Exists($path)) { throw 'JSON file missing' }
                $value = [System.IO.File]::ReadAllText($path) | ConvertFrom-Json
                $parts = ([string]$Check.jsonPath) -split '\.'
                foreach ($part in $parts) {
                    if ($part -notmatch '^([A-Za-z_][A-Za-z0-9_-]*)(\[\d+\])*$') { throw 'Invalid jsonPath' }
                    $name = $Matches[1]
                    $property = $value.PSObject.Properties[$name]
                    if ($null -eq $property) { throw 'JSON property missing' }
                    $value = $property.Value
                    foreach ($indexMatch in [regex]::Matches($part, '\[(\d+)\]')) {
                        $index = [int]$indexMatch.Groups[1].Value
                        if ($value -isnot [array] -or $index -ge $value.Count) { throw 'JSON array index missing' }
                        $value = $value[$index]
                    }
                }
                $actual = if ($null -eq $value) { 'null' } elseif ($value -is [bool]) { $value.ToString().ToLowerInvariant() } elseif ($value -is [string]) { $value } else { ConvertTo-Json -InputObject $value -Depth 20 -Compress }
                $result.actual = $actual
                $result.passed = [string]::Equals($actual, [string]$Check.value, [StringComparison]::Ordinal)
            } else { throw 'Unknown check type' }
        } catch { $result.actual = 'ERROR: ' + $_.Exception.Message; $result.passed = $false }
        return $result
    }
    function Get-CodexExecutable {
        param([string]$Override)
        if (-not [string]::IsNullOrWhiteSpace($Override)) {
            $overridePath = [System.IO.Path]::GetFullPath($Override)
            $testRoot = [System.IO.Path]::GetFullPath((Join-Path $chainRepo 'dist\runner-b80r-selftest'))
            if (-not $overridePath.StartsWith(($testRoot.TrimEnd('\') + '\'), [StringComparison]::OrdinalIgnoreCase)) { throw 'CodexExecutable override is limited to runner selftest' }
            if (-not [System.IO.File]::Exists($overridePath)) { throw "Codex executable missing: $overridePath" }
            return $overridePath
        }
        $preferred = 'C:\Users\Administrator\AppData\Local\OpenAI\Codex\bin\f544b3844e0f14e9\codex.exe'
        if ([System.IO.File]::Exists($preferred)) { return $preferred }
        $bin = 'C:\Users\Administrator\AppData\Local\OpenAI\Codex\bin'
        $found = @(Get-ChildItem -LiteralPath $bin -Filter codex.exe -Recurse -File -ErrorAction SilentlyContinue)
        if ($found.Count -ne 1) { throw "Cannot identify unique codex.exe in $bin" }
        return $found[0].FullName
    }
    function Write-BatchHandoff {
        param([object]$Chain, [object]$Batch, [string]$Result, [string]$TaskPath, [string]$OutPath, [string]$ErrPath, [string]$Detail, [string]$HandoffPath)
        $stamp = [DateTime]::UtcNow.ToString('o')
        $lines = @("`n## chain $($Chain.chainName) batch $($Batch.number) $Result $stamp", '', "Task file: $TaskPath", "Log files: $OutPath ; $ErrPath", '', 'checks:')
        foreach ($check in $Batch.checksResults) { $lines += ('- {0} {1}: {2}; actual: {3}' -f $check.type, $check.target, $check.passed, $check.actual) }
        $lines += @('', 'changedFiles:')
        foreach ($change in $Batch.changedFiles) { $lines += ('- {0}: {1}' -f $change.kind, $change.path) }
        $lines += @('', "Remaining issues: $Detail", '')
        Append-Handoff $HandoffPath (($lines -join "`n") + "`n")
    }

    $chainFilePath = [System.IO.Path]::GetFullPath($ChainFile)
    if (-not [System.IO.File]::Exists($chainFilePath)) { throw "Chain file missing: $chainFilePath" }
    $chain = [System.IO.File]::ReadAllText($chainFilePath) | ConvertFrom-Json
    if ([string]::IsNullOrWhiteSpace($chain.chainName) -or [string]::IsNullOrWhiteSpace($chain.repository) -or [string]::IsNullOrWhiteSpace($chain.model) -or @($chain.batches).Count -lt 1) { throw 'Invalid chain header' }
    if (-not [System.IO.Path]::IsPathRooted([string]$chain.repository)) { throw 'Repository path must be absolute' }
    $chainRepo = [System.IO.Path]::GetFullPath([string]$chain.repository)
    if (-not [System.IO.Directory]::Exists($chainRepo)) { throw 'Repository missing' }
    $chainStatePath = Resolve-ChainPath $chain.chainStatePath $chainRepo
    $handoffPath = Resolve-ChainPath $chain.handoffRecordPath $chainRepo
    $watchdogPath = Resolve-ChainPath $chain.watchdogLogPath $chainRepo
    foreach ($writePath in @($chainStatePath, $handoffPath, $watchdogPath)) { Assert-SafeWritePath $writePath $chainRepo }
    if ([string]::IsNullOrWhiteSpace($chain.model) -or [string]$chain.model -notmatch '^[A-Za-z0-9_.-]+$') { throw 'Invalid model name' }
    $chainCodex = Get-CodexExecutable $CodexExecutable
    $numbers = New-Object 'System.Collections.Generic.HashSet[int]'
    $labels = New-Object 'System.Collections.Generic.HashSet[string]' ([StringComparer]::OrdinalIgnoreCase)
    foreach ($entry in $chain.batches) {
        if ($null -eq $entry.number -or [int]$entry.number -lt 1 -or [string]::IsNullOrWhiteSpace($entry.logLabel) -or [string]$entry.logLabel -notmatch '^[A-Za-z0-9_-]+$' -or [string]::IsNullOrWhiteSpace($entry.taskFile) -or [int]$entry.expectedMinutes -lt 1 -or $null -eq $entry.checks) { throw 'Invalid batch entry' }
        if (-not $numbers.Add([int]$entry.number) -or -not $labels.Add([string]$entry.logLabel)) { throw 'Duplicate batch number or logLabel' }
    }
    if ([System.IO.File]::Exists($chainStatePath)) {
        try {
            $previous = [System.IO.File]::ReadAllText($chainStatePath) | ConvertFrom-Json
            if ($previous.status -in @('running', 'verifying')) {
                $active = @($previous.batches | Where-Object { $_.number -eq $previous.currentBatch })
                if ($active.Count -gt 0 -and $null -ne $active[0].pid -and $null -ne (Get-Process -Id ([int]$active[0].pid) -ErrorAction SilentlyContinue)) { throw 'Previous chain batch is still active' }
            }
        } catch { throw "Cannot safely replace chain state: $($_.Exception.Message)" }
    }
    $first = 0
    if ($PSBoundParameters.ContainsKey('StartBatch')) {
        $first = -1
        for ($i = 0; $i -lt @($chain.batches).Count; $i++) { if ([int]$chain.batches[$i].number -eq $StartBatch) { $first = $i; break } }
        if ($first -lt 0) { throw "StartBatch $StartBatch not found" }
    }
    $chainState = [ordered]@{ chainName = [string]$chain.chainName; startedAt = [DateTime]::UtcNow.ToString('o'); updatedAt = [DateTime]::UtcNow.ToString('o'); status = 'running'; currentBatch = $null; batches = @() }
    for ($i = $first; $i -lt @($chain.batches).Count; $i++) {
        $spec = $chain.batches[$i]
        if ($null -eq $spec.number -or [string]::IsNullOrWhiteSpace($spec.logLabel) -or $null -eq $spec.checks -or [int]$spec.expectedMinutes -lt 1) { throw 'Invalid batch entry' }
        $taskPath = Resolve-ChainPath $spec.taskFile $chainRepo
        Assert-SafeWritePath $taskPath $chainRepo
        if (-not [System.IO.File]::Exists($taskPath)) { throw "Task file missing: $taskPath" }
        $label = [string]$spec.logLabel
        if ($label -notmatch '^[A-Za-z0-9_-]+$') { throw 'Invalid logLabel' }
        $runtimeDir = [System.IO.Path]::GetDirectoryName($chainRepo)
        if (-not [string]::IsNullOrWhiteSpace($LogDirectory)) { $runtimeDir = Resolve-ChainPath $LogDirectory $chainRepo }
        if (-not [System.IO.Directory]::Exists($runtimeDir)) { [void][System.IO.Directory]::CreateDirectory($runtimeDir) }
        $outPath = Join-Path $runtimeDir ("codex-batch{0}-run.log" -f $label)
        $errPath = Join-Path $runtimeDir ("codex-batch{0}-err.log" -f $label)
        $pidPath = Join-Path $runtimeDir ("codex-batch{0}.pid" -f $label)
        foreach ($writePath in @($outPath, $errPath, $pidPath)) { Assert-SafeWritePath $writePath $chainRepo }
        $batchState = [ordered]@{ number = [int]$spec.number; logLabel = $label; status = 'running'; pid = $null; startedAt = [DateTime]::UtcNow.ToString('o'); endedAt = $null; exitCode = $null; checksResults = @(); beforeFiles = @(); afterFiles = @(); changedFiles = @() }
        $chainState.currentBatch = [int]$spec.number
        $chainState.batches += $batchState
        $chainState.updatedAt = [DateTime]::UtcNow.ToString('o')
        $batchState.beforeFiles = @(Get-Snapshot $chainRepo)
        Write-ChainState $chainState $chainStatePath
        Append-Handoff $watchdogPath ("$($chainState.updatedAt) batch=$($spec.number) status=starting`n")
        $psi = New-Object System.Diagnostics.ProcessStartInfo
        $psi.FileName = $chainCodex
        $psi.WorkingDirectory = $chainRepo
        $psi.Arguments = 'exec --cd "' + $chainRepo + '" --approve-for-me --color never -m ' + [string]$chain.model + ' -'
        $psi.UseShellExecute = $false
        $psi.CreateNoWindow = $true
        $psi.RedirectStandardInput = $true
        $psi.RedirectStandardOutput = $true
        $psi.RedirectStandardError = $true
        $psi.StandardOutputEncoding = [System.Text.Encoding]::UTF8
        $psi.StandardErrorEncoding = [System.Text.Encoding]::UTF8
        $p = New-Object System.Diagnostics.Process
        $p.StartInfo = $psi
        $outStream = New-Object System.IO.FileStream($outPath, [System.IO.FileMode]::Create, [System.IO.FileAccess]::Write, [System.IO.FileShare]::ReadWrite, 1, $true)
        $errStream = New-Object System.IO.FileStream($errPath, [System.IO.FileMode]::Create, [System.IO.FileAccess]::Write, [System.IO.FileShare]::ReadWrite, 1, $true)
        $stalled = $false
        try {
            [void]$p.Start()
            $stdoutTask = $p.StandardOutput.BaseStream.CopyToAsync($outStream)
            $stderrTask = $p.StandardError.BaseStream.CopyToAsync($errStream)
            $batchState.pid = $p.Id
            [System.IO.File]::WriteAllText($pidPath, [string]$p.Id, $chainUtf8)
            Write-ChainState $chainState $chainStatePath
            $bytes = [System.IO.File]::ReadAllBytes($taskPath)
            try { $p.StandardInput.BaseStream.Write($bytes, 0, $bytes.Length); $p.StandardInput.BaseStream.Flush() } finally { $p.StandardInput.BaseStream.Close() }
            while (-not $p.HasExited) {
                Wait-Process -Id $p.Id -Timeout 60 -ErrorAction SilentlyContinue
                $lastLog = Get-LogTime $outPath $errPath
                $chainState.updatedAt = [DateTime]::UtcNow.ToString('o')
                Append-Handoff $watchdogPath ("$($chainState.updatedAt) batch=$($spec.number) lastLog=$($lastLog.ToString('o'))`n")
                if (-not $p.HasExited -and ([DateTime]::UtcNow - $lastLog).TotalMinutes -ge 30) {
                    $stalled = $true
                    & taskkill /PID $p.Id /T /F 2>$null | Out-Null
                }
                Write-ChainState $chainState $chainStatePath
                if ($stalled) { break }
            }
            $p.WaitForExit()
            [void]$stdoutTask.GetAwaiter().GetResult()
            [void]$stderrTask.GetAwaiter().GetResult()
            $batchState.exitCode = $p.ExitCode
        } catch {
            $batchState.status = 'failed'
            $chainState.status = 'failed'
            $batchState.endedAt = [DateTime]::UtcNow.ToString('o')
            $chainState.updatedAt = $batchState.endedAt
            Write-ChainState $chainState $chainStatePath
            Write-BatchHandoff $chain $batchState 'BLOCKED' $taskPath $outPath $errPath $_.Exception.Message $handoffPath
            throw
        } finally { $outStream.Close(); $errStream.Close(); $p.Dispose() }
        $batchState.endedAt = [DateTime]::UtcNow.ToString('o')
        if ($stalled) {
            $batchState.status = 'stalled'; $chainState.status = 'stalled'
            Write-ChainState $chainState $chainStatePath
            Write-BatchHandoff $chain $batchState 'STALLED' $taskPath $outPath $errPath 'No log update for 30 minutes' $handoffPath
            exit 1
        }
        $errText = [System.IO.File]::ReadAllText($errPath)
        $errTail = if ($errText.Length -gt 4000) { $errText.Substring($errText.Length - 4000) } else { $errText }
        if ($batchState.exitCode -ne 0 -or -not $errTail.Contains('tokens used')) {
            $batchState.status = 'failed'; $chainState.status = 'failed'
            Write-ChainState $chainState $chainStatePath
            Write-BatchHandoff $chain $batchState 'BLOCKED' $taskPath $outPath $errPath "exitCode=$($batchState.exitCode); tokens used marker missing=$(-not $errTail.Contains('tokens used'))" $handoffPath
            exit 1
        }
        $batchState.status = 'verifying'; $chainState.status = 'verifying'
        Write-ChainState $chainState $chainStatePath
        foreach ($check in $spec.checks) {
            $checkResult = Invoke-ChainCheck $check $chainRepo
            $batchState.checksResults += $checkResult
            $chainState.updatedAt = [DateTime]::UtcNow.ToString('o')
            Write-ChainState $chainState $chainStatePath
            if (-not $checkResult.passed) {
                $batchState.status = 'verify_failed'; $chainState.status = 'verify_failed'
                Write-ChainState $chainState $chainStatePath
                Write-BatchHandoff $chain $batchState 'BLOCKED' $taskPath $outPath $errPath "Check failed: $($checkResult.type) $($checkResult.target): $($checkResult.actual)" $handoffPath
                exit 1
            }
        }
        $batchState.afterFiles = @(Get-Snapshot $chainRepo)
        $batchState.changedFiles = @(Get-ChangedFiles $batchState.beforeFiles $batchState.afterFiles)
        $batchState.status = 'verified'; $chainState.status = 'running'
        $chainState.updatedAt = [DateTime]::UtcNow.ToString('o')
        Write-ChainState $chainState $chainStatePath
        Write-BatchHandoff $chain $batchState 'VERIFIED' $taskPath $outPath $errPath 'None' $handoffPath
        Write-Output ("batch={0} status=VERIFIED" -f $spec.number)
    }
    $chainState.status = 'complete'; $chainState.currentBatch = $null; $chainState.updatedAt = [DateTime]::UtcNow.ToString('o')
    Write-ChainState $chainState $chainStatePath
    Write-Output ("chain={0} status=COMPLETE batches={1}" -f $chain.chainName, $chainState.batches.Count)
    exit 0
}
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

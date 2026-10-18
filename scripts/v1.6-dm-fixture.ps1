# Prepare an isolated DM8 read-only fixture for the v1.6 vendor fault tests.
# The output JSON contains a temporary database password; keep it outside Git.
[CmdletBinding()]
param(
    [ValidatePattern('^[A-Za-z0-9_.-]+$')]
    [string]$Container = 'dm8-b65',
    [string]$OutputPath = 'D:\CodexLS\tmp\agentsql-v160\dm8-target.json'
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$inspectionText = @(& docker inspect $Container --format '{{json .}}' 2>&1)
$inspectExit = $LASTEXITCODE
if ($inspectExit -ne 0 -or $inspectionText.Count -ne 1) {
    throw 'Expected isolated DM8 batch65 container is unavailable'
}
$inspection = $inspectionText[0] | ConvertFrom-Json
if ($inspection.State.Status -ne 'running' -or
    $inspection.Config.Labels.'agentsql.batch' -ne '65' -or
    $inspection.Config.Image -notmatch 'dm8_single') {
    throw 'Expected isolated DM8 batch65 container is unavailable'
}
$adminEntry = @($inspection.Config.Env | Where-Object { $_ -like 'SYSDBA_PWD=*' })
if ($adminEntry.Count -ne 1) { throw 'DM8 administrator credential is unavailable' }
$adminPassword = $adminEntry[0].Substring('SYSDBA_PWD='.Length)
$bytes = New-Object byte[] 8
[System.Security.Cryptography.RandomNumberGenerator]::Fill($bytes)
$readerPassword = 'Aa1!' + [Convert]::ToHexString($bytes).ToLowerInvariant()
$suffix = Get-Date -Format 'HHmmss'
$table = 'AGSQL_V160_' + $suffix
$reader = 'AGSQL_V160_R_' + $suffix

function Invoke-Disql {
    param([string]$Login, [string[]]$Statements)
    $start = New-Object System.Diagnostics.ProcessStartInfo
    $start.FileName = (Get-Command docker).Source
    $start.Arguments = "exec -i -e LD_LIBRARY_PATH=/opt/dmdbms/bin $Container /opt/dmdbms/bin/disql /NOLOG"
    $start.UseShellExecute = $false
    $start.RedirectStandardInput = $true
    $start.RedirectStandardOutput = $true
    $start.RedirectStandardError = $true
    $process = [System.Diagnostics.Process]::Start($start)
    try {
        $process.StandardInput.Write((@($Login) + $Statements + @('EXIT') -join "`n") + "`n")
        $process.StandardInput.Close()
        $output = $process.StandardOutput.ReadToEnd() + $process.StandardError.ReadToEnd()
        $process.WaitForExit()
        $safeOutput = $output.Replace($adminPassword, '[REDACTED]').Replace($readerPassword, '[REDACTED]')
        if ($process.ExitCode -ne 0 -or $safeOutput -match '\[-\d+\]|not connected|Error in line') {
            throw ('DM8 fixture SQL failed: ' + ($safeOutput -replace '\s+', ' ').Trim())
        }
        return $safeOutput
    }
    finally { $process.Dispose() }
}

$adminLogin = 'CONN SYSDBA/"' + $adminPassword + '"@127.0.0.1'
$readerLogin = 'CONN ' + $reader + '/"' + $readerPassword + '"@127.0.0.1'
Invoke-Disql $adminLogin @(
    "CREATE TABLE $table (ID INT PRIMARY KEY, PHONE VARCHAR(20));",
    "INSERT INTO $table (ID,PHONE) VALUES (1,'13800135678');",
    ('CREATE USER ' + $reader + ' IDENTIFIED BY "' + $readerPassword + '";'),
    "GRANT CREATE SESSION TO $reader;",
    "GRANT SELECT ON SYSDBA.$table TO $reader;"
) | Out-Null
$probe = Invoke-Disql $readerLogin @("SELECT PHONE FROM SYSDBA.$table WHERE ID=1;")
if ($probe -notmatch '13800135678') { throw 'DM8 read-only fixture query did not return the expected synthetic row' }

$binding = @($inspection.NetworkSettings.Ports.'5236/tcp')
if ($binding.Count -ne 1 -or $binding[0].HostIp -ne '127.0.0.1') {
    throw 'DM8 fixture is not bound to exactly one host loopback port'
}
$target = @(@{
    name = 'dm8-pack3'; db_type = 'dm'; host = '127.0.0.1'; port = [int]$binding[0].HostPort
    database = 'SYSDBA'; username = $reader; password = $readerPassword
    object_name = "SYSDBA.$table"; table_name = $table; column_name = 'PHONE'
    sql = "SELECT PHONE FROM SYSDBA.$table WHERE ID=1"
    clear_value = '13800135678'; mask_value = '138****5678'
})
$parent = Split-Path -Parent $OutputPath
New-Item -ItemType Directory -Path $parent -Force | Out-Null
[System.IO.File]::WriteAllText($OutputPath, (ConvertTo-Json -InputObject $target -Depth 5 -Compress))
Write-Output "DM8 isolated fixture ready; target file: $OutputPath"

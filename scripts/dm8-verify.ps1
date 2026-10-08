param(
    [string]$LicenseFile = 'D:\ruanjiansheji\db-images\dm8B01200017.key',
    [string]$SourceContainer = 'dm8-v06',
    [string]$VerifyContainer = 'dm8-b65',
    [string]$DockerExe = 'E:\Docker\DockerDesktop\resources\bin\docker.exe',
    [int]$ReadyTimeoutSeconds = 180,
    [string]$LogPath = (Join-Path $env:TEMP 'agentsql-dm8-b65-verify.log')
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$transcriptStarted = $false
$verifyReady = $false
$sourceWasRunning = $false

function Step([string]$message) {
    Write-Host ('[{0}] {1}' -f (Get-Date -Format 'yyyy-MM-ddTHH:mm:ssK'), $message)
}

function Docker([string[]]$arguments) {
    $previous = $ErrorActionPreference
    try {
        $ErrorActionPreference = 'Continue'
        $result = @(& $DockerExe @arguments 2>&1)
        $code = $LASTEXITCODE
    }
    finally { $ErrorActionPreference = $previous }
    if ($code -ne 0) {
        throw ('docker {0} exited {1}: {2}' -f $arguments[0], $code, ($result -join "`n"))
    }
    return $result
}

function Docker-Try([string[]]$arguments) {
    $previous = $ErrorActionPreference
    try {
        $ErrorActionPreference = 'Continue'
        $result = @(& $DockerExe @arguments 2>&1)
        return @{ ExitCode = $LASTEXITCODE; Output = ($result -join "`n") }
    }
    finally { $ErrorActionPreference = $previous }
}

function Inspect-Container([string]$name) {
    $result = Docker-Try @('inspect', $name, '--format', '{{json .}}')
    if ($result.ExitCode -ne 0) { return $null }
    return ($result.Output | ConvertFrom-Json)
}

function Run-Disql([string[]]$statements, [string]$password) {
    $inputLines = @(('CONN SYSDBA/"' + $password + '"@127.0.0.1')) + $statements + @('EXIT')
    # PowerShell's native pipeline writes CRLF on Windows. disql treats the CR
    # as part of a host/port token, yielding misleading -70028/-70064 errors.
    $start = New-Object System.Diagnostics.ProcessStartInfo
    $start.FileName = $DockerExe
    $start.Arguments = "exec -i -e LD_LIBRARY_PATH=/opt/dmdbms/bin $VerifyContainer /opt/dmdbms/bin/disql /NOLOG"
    $start.UseShellExecute = $false
    $start.RedirectStandardInput = $true
    $start.RedirectStandardOutput = $true
    $start.RedirectStandardError = $true
    $process = [System.Diagnostics.Process]::Start($start)
    $process.StandardInput.Write(($inputLines -join "`n") + "`n")
    $process.StandardInput.Close()
    $output = $process.StandardOutput.ReadToEnd() + $process.StandardError.ReadToEnd()
    $process.WaitForExit()
    return @{ ExitCode = $process.ExitCode; Output = $output.Replace($password, '[REDACTED]') }
}

function Require-Disql([string]$label, [string[]]$statements, [string]$expected, [string]$password) {
    $result = Run-Disql $statements $password
    if ($result.ExitCode -ne 0 -or $result.Output -match '\[-\d+\]|not connected|Error in line' -or
        ($expected -and $result.Output -notmatch $expected)) {
        throw ('disql {0} failed: {1}' -f $label, ($result.Output -replace '\s+', ' ').Trim())
    }
    Step ('{0}: {1}' -f $label, ($result.Output -replace '\s+', ' ').Trim())
    return $result
}

function Api([string]$method, [string]$path, $body, [string]$token) {
    $headers = @{}
    if ($token) { $headers.Authorization = "Bearer $token" }
    $request = @{ Uri = "$agentURL$path"; Method = $method; Headers = $headers; TimeoutSec = 30 }
    if ($null -ne $body) {
        $request.Body = $body | ConvertTo-Json -Depth 12 -Compress
        $request.ContentType = 'application/json'
    }
    $response = Invoke-RestMethod @request
    if ($response.code -ne 0) { throw "AgentSQL API $path returned code $($response.code)" }
    return $response.data
}

function Call-MCP([int]$id, [string]$tool, $arguments) {
    $payload = @{ jsonrpc = '2.0'; id = $id; method = 'tools/call'; params = @{ name = $tool; arguments = $arguments } } | ConvertTo-Json -Depth 12 -Compress
    $response = Invoke-WebRequest -Uri "$agentURL/mcp" -Method Post -Headers $mcpHeaders -ContentType 'application/json' -Body $payload -UseBasicParsing -TimeoutSec 30
    $body = $response.Content
    if ($body -notmatch '^\s*\{') {
        $line = @($body -split "`n" | Where-Object { $_ -match '^data: ' }) | Select-Object -Last 1
        if (-not $line) { throw 'MCP response was neither JSON nor a data event' }
        $body = $line.Substring(6)
    }
    return ($body | ConvertFrom-Json)
}

try {
    if (-not (Test-Path -LiteralPath $DockerExe -PathType Leaf)) {
        $dockerCommand = Get-Command docker -ErrorAction SilentlyContinue
        if (-not $dockerCommand) { throw 'Docker CLI not found' }
        $DockerExe = $dockerCommand.Source
    }
    if (-not (Test-Path -LiteralPath $LicenseFile -PathType Leaf)) { throw 'Supplied license file not found' }
    if ((Get-Item -LiteralPath $LicenseFile).Length -ne 648) { throw 'Unexpected supplied license byte count' }
    Start-Transcript -LiteralPath $LogPath -Force | Out-Null
    $transcriptStarted = $true
    Step ('Evidence log: {0}' -f $LogPath)

    Step 'Inspect existing container, process, startup script, config, and license log'
    $source = Inspect-Container $SourceContainer
    if ($null -eq $source) { throw 'Existing DM8 source container not found' }
    if ($source.Config.Entrypoint.Count -ne 1 -or $source.Config.Entrypoint[0] -ne '/opt/startup.sh') {
        throw 'Unrecognized DM8 entrypoint'
    }
    $bindings = @($source.HostConfig.PortBindings.'5236/tcp')
    if ($bindings.Count -ne 1 -or $bindings[0].HostIp -ne '127.0.0.1' -or $bindings[0].HostPort -ne '5236') {
        throw 'Existing DM8 port mapping is not loopback 5236:5236'
    }
    $sourceWasRunning = [bool]$source.State.Running
    if (-not $sourceWasRunning) {
        $verifyForDiscovery = Inspect-Container $VerifyContainer
        if ($null -eq $verifyForDiscovery -or -not $verifyForDiscovery.State.Running) {
            throw 'Neither source nor verification DM8 container is running for path discovery'
        }
    }
    $discoveryContainer = if ($sourceWasRunning) { $SourceContainer } else { $VerifyContainer }
    $image = [string]$source.Config.Image
    $processDeadline = (Get-Date).AddSeconds($ReadyTimeoutSeconds)
    do {
        $processes = (Docker @('exec', $discoveryContainer, 'ps', '-eo', 'pid,args')) -join "`n"
        $server = [regex]::Match($processes, '(?m)^\s*(\d+)\s+/opt/dmdbms/bin/dmserver\s+(/\S+/dm\.ini)\s+-noconsole')
        if ($server.Success) { break }
        Start-Sleep -Seconds 2
    } while ((Get-Date) -lt $processDeadline)
    if (-not $server.Success) { throw 'Cannot derive running dmserver command and dm.ini path' }
    $serverPID = $server.Groups[1].Value
    $iniPath = $server.Groups[2].Value
    $startup = (Docker @('exec', $discoveryContainer, 'cat', '/opt/singlestartup.sh')) -join "`n"
    if (-not $startup.Contains('cd /opt/dmdbms/bin') -or -not $startup.Contains($iniPath)) {
        throw 'Startup script does not establish the observed server working directory/ini'
    }
    $cwdResult = (Docker @('exec', '-u', 'dmdba', $discoveryContainer, 'readlink', '-f', "/proc/$serverPID/cwd") | Select-Object -First 1).Trim()
    if ($cwdResult -ne '/opt/dmdbms/bin') { throw "Unexpected server working directory: $cwdResult" }
    $licenseName = 'dm.key'
    $licenseLog = if ($sourceWasRunning) {
        Docker-Try @('exec', $SourceContainer, 'grep', '-in', '-E', 'file dm[.]key not found', '/opt/dmdbms/log/dm_DMSERVER.log')
    } else {
        Docker-Try @('exec', $VerifyContainer, 'grep', '-a', '-o', 'dm.key', '/opt/dmdbms/bin/libdmlic.so')
    }
    if ($licenseLog.ExitCode -ne 0 -or $licenseLog.Output -notmatch 'dm[.]key') { throw 'Cannot confirm dm.key lookup from installed DM8 binary/log' }
    $licenseTarget = "$cwdResult/$licenseName"
    $iniText = (Docker @('exec', $discoveryContainer, 'cat', $iniPath)) -join "`n"
    if ($iniText -notmatch '(?m)^\s*PORT_NUM\s*=\s*5236\b' -or $iniText -notmatch '(?m)^\s*COMPATIBLE_MODE\s*=\s*(\d+)\b') {
        throw 'Cannot verify active port and compatibility mode from dm.ini'
    }
    $compatibleMode = $Matches[1]
    Step "image=$image; entrypoint=/opt/startup.sh; dmserver_ini=$iniPath; cwd=$cwdResult; license_target=$licenseTarget; port=5236; COMPATIBLE_MODE=$compatibleMode"
    Step ('License filename evidence ({0}): {1}' -f $discoveryContainer, (($licenseLog.Output -split "`n" | Select-Object -Last 1).Trim()))
    Step ('Supplied license: bytes={0}; sha256={1}' -f (Get-Item -LiteralPath $LicenseFile).Length, (Get-FileHash -LiteralPath $LicenseFile -Algorithm SHA256).Hash)
    $envMap = @{}
    foreach ($entry in @($source.Config.Env)) {
        $parts = $entry -split '=', 2
        if ($parts.Count -eq 2) { $envMap[$parts[0]] = $parts[1] }
    }
    if (-not $envMap.ContainsKey('SYSDBA_PWD') -or -not $envMap.ContainsKey('MODE') -or $envMap['MODE'] -ne 'dmsingle') {
        throw 'Cannot derive administrator password and DM single-instance mode from container Env'
    }
    $adminPassword = $envMap['SYSDBA_PWD']

    Step 'Start separate DM8 verification container with read-only license bind mount'
    $existing = Inspect-Container $VerifyContainer
    if ($null -ne $existing) {
        $label = [string]$existing.Config.Labels.'agentsql.batch'
        if ($label -ne '65') { throw 'Verification container name exists without batch label' }
        $mounts = @($existing.Mounts | Where-Object { $_.Destination -eq $licenseTarget -and $_.Source -eq $LicenseFile -and $_.RW -eq $false })
        if ($mounts.Count -ne 1) { throw 'Existing verification container has a different license mount' }
        if ($existing.Config.Image -ne $image) { throw 'Existing verification container uses another image' }
    }
    if ($sourceWasRunning) { Docker @('stop', $SourceContainer) | Out-Null }
    if ($null -eq $existing) {
        $runArgs = @('run', '-d', '--name', $VerifyContainer, '--label', 'agentsql.batch=65',
            '--publish', '127.0.0.1:5236:5236', '--mount', "type=bind,source=$LicenseFile,target=$licenseTarget,readonly")
        foreach ($name in @('SYSDBA_PWD','DM_USER_PWD','PAGE_SIZE','CASE_SENSITIVE','UNICODE_FLAG','LENGTH_IN_CHAR','BUFFER','MODE','INSTANCE_NAME','CHG_PASSWD','EXTENT_SIZE','BLANK_PAD_MODE','LOG_SIZE')) {
            if ($envMap.ContainsKey($name)) { $runArgs += @('--env', "$name=$($envMap[$name])") }
        }
        $runArgs += $image
        Docker $runArgs | Out-Null
    }
    elseif (-not $existing.State.Running) { Docker @('start', $VerifyContainer) | Out-Null }
    $mounted = Inspect-Container $VerifyContainer
    if ($null -eq $mounted -or -not $mounted.State.Running) { throw 'Verification container is not running' }
    $actualMount = @($mounted.Mounts | Where-Object { $_.Destination -eq $licenseTarget -and $_.RW -eq $false })
    if ($actualMount.Count -ne 1) { throw 'Read-only license mount is absent' }
    Step ('Mount confirmed: {0} -> {1} (readonly)' -f $actualMount[0].Source, $actualMount[0].Destination)

    Step 'Wait for disql SYSDBA login and database readiness'
    $deadline = (Get-Date).AddSeconds($ReadyTimeoutSeconds)
    $login = $null
    do {
        $login = Run-Disql @('SELECT 1 AS READY FROM DUAL;') $adminPassword
        if ($login.ExitCode -eq 0 -and $login.Output -match 'READY' -and $login.Output -notmatch 'connection failure|error code|[-]2501') {
            $verifyReady = $true
            break
        }
        Start-Sleep -Seconds 3
    } while ((Get-Date) -lt $deadline)
    if (-not $verifyReady) {
        Step ('Last disql probe: {0}' -f $login.Output)
        $recent = Docker-Try @('exec', $VerifyContainer, 'grep', '-in', '-E', 'licen|dm[.]key|error|ready', '/opt/dmdbms/log/dm_DMSERVER.log')
        Step ('License/server log: {0}' -f (($recent.Output -split "`n" | Select-Object -Last 12) -join ' | '))
        throw 'SYSDBA login did not become ready'
    }
    Step ('disql login/SELECT succeeded: {0}' -f ($login.Output -replace '\s+', ' ').Trim())
    $licenseEvidence = Docker-Try @('exec', $VerifyContainer, 'grep', '-in', '-E', 'licen|dm[.]key', '/opt/dmdbms/log/dm_DMSERVER.log')
    Step ('License log: {0}' -f (($licenseEvidence.Output -split "`n" | Select-Object -Last 12) -join ' | '))
    Step 'Query version, actual database/user/schema, license view, and compatibility mode'
    $identity = Require-Disql 'identity' @(
        'SELECT * FROM V$VERSION;',
        'SELECT ID_CODE();',
        'SELECT NAME FROM V$DATABASE;',
        'SELECT USER, SF_GET_SCHEMA_NAME_BY_ID(CURRENT_SCHID) AS SCHEMA_NAME FROM DUAL;',
        'SELECT SERIES_NO, EXPIRED_DATE FROM V$LICENSE;',
        "SELECT PARA_NAME, PARA_VALUE FROM V`$DM_INI WHERE PARA_NAME='COMPATIBLE_MODE';"
    ) '03134284294-20241009-244896-20119[\s\S]*DAMENG[\s\S]*SYSDBA[\s\S]*8B01200017[\s\S]*2027-06-25[\s\S]*COMPATIBLE_MODE 0' $adminPassword

    Step 'Probe this DM8 instance pagination forms and identifier casing'
    Require-Disql 'TOP 1' @('SELECT TOP 1 1 AS V FROM DUAL;') '(?m)^1\s+1\s*$' $adminPassword | Out-Null
    Require-Disql 'LIMIT 1' @('SELECT 1 AS V FROM DUAL LIMIT 1;') '(?m)^1\s+1\s*$' $adminPassword | Out-Null
    Require-Disql 'FETCH FIRST 1' @('SELECT 1 AS V FROM DUAL FETCH FIRST 1 ROWS ONLY;') '(?m)^1\s+1\s*$' $adminPassword | Out-Null
    Require-Disql 'unquoted alias folds' @('SELECT 1 AS MixedCase FROM dual;') 'MIXEDCASE' $adminPassword | Out-Null
    Require-Disql 'quoted alias retains case' @('SELECT 1 AS "MixedCase" FROM DUAL;') 'MixedCase' $adminPassword | Out-Null

    $table = 'AGSQL_B65_' + (Get-Date -Format 'HHmmss')
    Step ("CRUD verification table=$table")
    Require-Disql 'create table' @("CREATE TABLE $table (ID INT PRIMARY KEY, PHONE VARCHAR(20), NOTE VARCHAR(64));") 'successfully' $adminPassword | Out-Null
    Require-Disql 'insert' @("INSERT INTO $table (ID,PHONE,NOTE) VALUES (1,'13800135678','created');") 'affect rows 1' $adminPassword | Out-Null
    Require-Disql 'select' @("SELECT ID,PHONE,NOTE FROM $table WHERE ID=1;") '13800135678' $adminPassword | Out-Null
    Require-Disql 'update' @("UPDATE $table SET NOTE='updated' WHERE ID=1;") 'affect rows 1' $adminPassword | Out-Null
    Require-Disql 'verify update' @("SELECT NOTE FROM $table WHERE ID=1;") 'updated' $adminPassword | Out-Null
    Require-Disql 'delete' @("DELETE FROM $table WHERE ID=1;") 'affect rows 1' $adminPassword | Out-Null
    Require-Disql 'verify delete' @("SELECT COUNT(*) AS N FROM $table;") '(?m)^1\s+0\s*$' $adminPassword | Out-Null
    Require-Disql 'seed guard sample' @("INSERT INTO $table (ID,PHONE,NOTE) VALUES (2,'13800135678','guard');") 'affect rows 1' $adminPassword | Out-Null
    $denyTable = 'AGSQL_B65_D_' + (Get-Date -Format 'HHmmss')
    Require-Disql 'create ungranted policy target' @("CREATE TABLE $denyTable (ID INT PRIMARY KEY);") 'successfully' $adminPassword | Out-Null
    Require-Disql 'seed ungranted policy target' @("INSERT INTO $denyTable (ID) VALUES (7);") 'affect rows 1' $adminPassword | Out-Null
    Step 'Create least-privilege reader for the driver and AgentSQL probes'
    $reader = 'AGSQL_B65_R_' + (Get-Date -Format 'HHmmss')
    $random = New-Object byte[] 16
    $rng = [Security.Cryptography.RandomNumberGenerator]::Create()
    try { $rng.GetBytes($random) } finally { $rng.Dispose() }
    $readerPassword = ([BitConverter]::ToString($random)).Replace('-', '').ToLowerInvariant()
    Require-Disql 'create reader' @('CREATE USER ' + $reader + ' IDENTIFIED BY "' + $readerPassword + '";') 'successfully' $adminPassword | Out-Null
    Require-Disql 'grant session' @("GRANT CREATE SESSION TO $reader;") 'successfully' $adminPassword | Out-Null
    Require-Disql 'grant table select' @("GRANT SELECT ON SYSDBA.$table TO $reader;") 'successfully' $adminPassword | Out-Null
    Require-Disql 'grant second table select to database reader' @("GRANT SELECT ON SYSDBA.$denyTable TO $reader;") 'successfully' $adminPassword | Out-Null
    Require-Disql 'reader privileges' @(
        "SELECT PRIVILEGE FROM SYS.DBA_SYS_PRIVS WHERE GRANTEE='$reader';",
        "SELECT PRIVILEGE FROM SYS.DBA_TAB_PRIVS WHERE GRANTEE='$reader';"
    ) 'CREATE SESSION[\s\S]*SELECT' $adminPassword | Out-Null

    Step 'Offline-build a Windows host probe using the pinned gorm-dameng module'
    $repoRoot = Split-Path $PSScriptRoot -Parent
    $moduleCache = Join-Path $env:USERPROFILE 'go\pkg\mod'
    if (-not (Test-Path -LiteralPath (Join-Path $moduleCache 'github.com\godoes\gorm-dameng@v0.7.2') -PathType Container)) {
        throw 'Pinned DM Go driver unavailable in offline module cache'
    }
    $probeSource = Join-Path $PSScriptRoot 'dm8-host-probe.tmp.go'
    $probeExe = Join-Path $PSScriptRoot 'dm8-host-probe.exe'
    $cacheVolume = 'dm8-b65-go-cache'
    Docker @('volume', 'create', $cacheVolume) | Out-Null
    try {
        @'
package main
import (
    "context"
    "database/sql"
    "fmt"
    "os"
    "time"
    _ "github.com/godoes/gorm-dameng/dm8"
)
func main() {
    ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second); defer cancel()
    user, password, table := os.Getenv("DM_VERIFY_USER"), os.Getenv("DM_VERIFY_PASSWORD"), os.Getenv("DM_VERIFY_TABLE")
    if user == "" || password == "" || table == "" { fmt.Fprintln(os.Stderr, "missing probe inputs"); os.Exit(2) }
    dsn := "dm://"+user+":"+password+"@127.0.0.1:5236?schema=SYSDBA"
    db, err := sql.Open("dm", dsn); if err != nil { fmt.Println("OPEN_FAIL", err); os.Exit(1) }; defer db.Close()
    db.SetMaxOpenConns(1)
    if err = db.PingContext(ctx); err != nil { fmt.Println("PING_FAIL", err); os.Exit(1) }
    var loginUser, schema, phone string
    if err = db.QueryRowContext(ctx, "SELECT USER, SF_GET_SCHEMA_NAME_BY_ID(CURRENT_SCHID)").Scan(&loginUser,&schema); err != nil { fmt.Println("IDENTITY_FAIL",err); os.Exit(1) }
    if err = db.QueryRowContext(ctx, "SELECT PHONE FROM "+table+" WHERE ID=?", 2).Scan(&phone); err != nil { fmt.Println("BIND_FAIL",err); os.Exit(1) }
    _, writeErr := db.ExecContext(ctx, "INSERT INTO "+table+" (ID,PHONE) VALUES (?,?)", 9000, "denied")
    fmt.Printf("driver=github.com/godoes/gorm-dameng/dm8 user=%s schema=%s bind_phone=%s denied_write=%t write_error=%v\n", loginUser,schema,phone,writeErr!=nil,writeErr)
    if loginUser!=user || schema!="SYSDBA" || phone!="13800135678" || writeErr==nil { os.Exit(1) }
}
'@ | Set-Content -LiteralPath $probeSource -Encoding UTF8
        Docker @('run', '--rm', '--network', 'none',
            '--mount', "type=bind,source=$repoRoot,target=/src,readonly",
            '--mount', "type=bind,source=$moduleCache,target=/gomod,readonly",
            '--mount', "type=bind,source=$PSScriptRoot,target=/out",
            '--mount', "type=volume,source=$cacheVolume,target=/buildcache",
            '--workdir', '/src', '--env', 'GOPROXY=off', '--env', 'GOMODCACHE=/gomod',
            '--env', 'GOCACHE=/buildcache', '--env', 'CGO_ENABLED=0', '--env', 'GOOS=windows',
            'golang:1.26-bookworm', 'go', 'build', '-o', '/out/dm8-host-probe.exe', './scripts/dm8-host-probe.tmp.go') | Out-Null
        $env:DM_VERIFY_USER = $reader
        $env:DM_VERIFY_PASSWORD = $readerPassword
        $env:DM_VERIFY_TABLE = $table
        $probeOutput = @(& $probeExe 2>&1)
        if ($LASTEXITCODE -ne 0) { throw ('Host DM driver probe failed: ' + ($probeOutput -join ' ')) }
        Step ('Host Go probe: ' + ($probeOutput -join ' '))
    }
    finally {
        Remove-Item Env:DM_VERIFY_USER,Env:DM_VERIFY_PASSWORD,Env:DM_VERIFY_TABLE -ErrorAction SilentlyContinue
        Remove-Item -LiteralPath $probeSource,$probeExe -ErrorAction SilentlyContinue
    }

    Step 'Offline-run the dm oracleCompatibleParser lineage probe with Linux CGO'
    $cacheVolume = 'dm8-b65-go-cache'
    Docker @('volume', 'create', $cacheVolume) | Out-Null
    $parserSource = Join-Path $PSScriptRoot 'dm8-parser-probe.tmp.go'
    try {
        @'
package main
import (
    "fmt"
    "os"
    "github.com/cuipengdba/agentsql/internal/model"
    "github.com/cuipengdba/agentsql/internal/parser"
)
func main() {
    table := os.Getenv("DM_VERIFY_TABLE")
    if table=="" { fmt.Println("MISSING_TABLE"); os.Exit(2) }
    p, err := parser.NewParser(model.DialectDM); if err != nil { fmt.Println("PARSER_FAIL",err); os.Exit(1) }
    ast, err := p.Parse("SELECT ID, PHONE FROM SYSDBA."+table+" WHERE ID=2")
    if err != nil { fmt.Println("PARSE_FAIL",err); os.Exit(1) }
    if ast.Dialect!=model.DialectDM || len(ast.Tables)!=1 || len(ast.ProjectionLineages)!=2 ||
        len(ast.ProjectionLineages[1].Arms)!=1 || len(ast.ProjectionLineages[1].Arms[0].Dependencies)!=1 ||
        ast.ProjectionLineages[1].Arms[0].Dependencies[0].Origin.Column!="PHONE" ||
        ast.ProjectionLineages[1].Arms[0].Dependencies[0].Origin.Relation.Table!=table {
        fmt.Printf("LINEAGE_FAIL tables=%v projections=%v\n",ast.Tables,ast.ProjectionLineages); os.Exit(1)
    }
    origin := ast.ProjectionLineages[1].Arms[0].Dependencies[0].Origin
    fmt.Printf("parser_dialect=%s source=%s.%s lineage_PHONE=%s.%s.%s\n",ast.Dialect,ast.Tables[0].Schema,ast.Tables[0].Table,
        origin.Relation.Schema,origin.Relation.Table,origin.Column)
}
'@ | Set-Content -LiteralPath $parserSource -Encoding UTF8
        $parserOutput = Docker @('run', '--rm', '--network', 'none',
            '--mount', "type=bind,source=$repoRoot,target=/src,readonly",
            '--mount', "type=bind,source=$moduleCache,target=/gomod,readonly",
            '--mount', "type=volume,source=$cacheVolume,target=/buildcache",
            '--workdir', '/src', '--env', 'GOPROXY=off', '--env', 'GOMODCACHE=/gomod',
            '--env', 'GOCACHE=/buildcache', '--env', 'CGO_ENABLED=1', '--env', "DM_VERIFY_TABLE=$table",
            'golang:1.26-bookworm', 'go', 'run', './scripts/dm8-parser-probe.tmp.go')
        Step ('DM parser lineage: ' + ($parserOutput -join ' '))
    }
    finally { Remove-Item -LiteralPath $parserSource -ErrorAction SilentlyContinue }

    Step 'Offline-build the current AgentSQL server from this checkout'
    $buildVolume = 'dm8-b65-go-build'
    Docker @('volume', 'create', $buildVolume) | Out-Null
    Docker @('volume', 'create', $cacheVolume) | Out-Null
    Docker @('run', '--rm', '--network', 'none',
        '--mount', "type=bind,source=$repoRoot,target=/src,readonly",
        '--mount', "type=bind,source=$moduleCache,target=/gomod,readonly",
        '--mount', "type=volume,source=$buildVolume,target=/out",
        '--mount', "type=volume,source=$cacheVolume,target=/buildcache",
        '--workdir', '/src', '--env', 'GOPROXY=off', '--env', 'GOMODCACHE=/gomod',
        '--env', 'GOCACHE=/buildcache', '--env', 'CGO_ENABLED=1',
        'golang:1.26-bookworm', 'go', 'build', '-o', '/out/agentsql', './cmd/agentsql') | Out-Null
    Step 'PASS: go build ./cmd/agentsql with --network none and GOPROXY=off'

    Step 'Start AgentSQL on a private Docker network with loopback-only host API'
    $network = 'dm8-b65-net'
    if ((Docker-Try @('network', 'inspect', $network)).ExitCode -ne 0) { Docker @('network', 'create', $network) | Out-Null }
    $dmNetworks = @((Inspect-Container $VerifyContainer).NetworkSettings.Networks.PSObject.Properties.Name)
    if ($dmNetworks -notcontains $network) { Docker @('network', 'connect', $network, $VerifyContainer) | Out-Null }
    $agentContainer = 'dm8-b65-agentsql'
    $agentDataVolume = 'dm8-b65-agentsql-data'
    $priorAgent = Inspect-Container $agentContainer
    if ($null -ne $priorAgent) {
        if ([string]$priorAgent.Config.Labels.'agentsql.batch' -ne '65') { throw 'AgentSQL container name exists without batch label' }
        Docker @('rm', '-f', $agentContainer) | Out-Null
    }
    if ((Docker-Try @('volume', 'inspect', $agentDataVolume)).ExitCode -eq 0) { Docker @('volume', 'rm', $agentDataVolume) | Out-Null }
    Docker @('volume', 'create', $agentDataVolume) | Out-Null
    $secretBytes = New-Object byte[] 16
    $rng = [Security.Cryptography.RandomNumberGenerator]::Create()
    try {
        $rng.GetBytes($secretBytes)
        $controlSecret = ([BitConverter]::ToString($secretBytes)).Replace('-', '').ToLowerInvariant()
        $rng.GetBytes($secretBytes)
        $controlAdminPassword = ([BitConverter]::ToString($secretBytes)).Replace('-', '').ToLowerInvariant()
    }
    finally { $rng.Dispose() }
    $configFile = Join-Path $env:TEMP ('dm8-b65-' + [Guid]::NewGuid().ToString('N') + '.yaml')
    try {
        @'
server:
  http_listen: "0.0.0.0:7780"
  console_enabled: true
  event_stream: false
store:
  sqlite_path: "/data/agentsql.db"
defaults:
  statement_timeout_ms: 5000
  row_limit: 100
  max_conns_per_datasource: 5
  qps_per_agent: 20
theme:
  default: "dark"
column_authorization:
  enabled: false
mcp:
  transactions:
    postgres: false
'@ | Set-Content -LiteralPath $configFile -Encoding ASCII
        Docker @('create', '--name', $agentContainer, '--label', 'agentsql.batch=65',
            '--network', $network, '--publish', '127.0.0.1::7780',
            '--mount', "type=volume,source=$buildVolume,target=/app,readonly",
            '--mount', "type=volume,source=$agentDataVolume,target=/data",
            '--env', "AGENTSQL_SECRET=$controlSecret", '--env', "AGENTSQL_ADMIN_PASSWORD=$controlAdminPassword",
            '--entrypoint', '/app/agentsql', 'debian:bookworm-slim', 'serve', '--config', '/data/config.yaml') | Out-Null
        Docker @('cp', $configFile, "${agentContainer}:/data/config.yaml") | Out-Null
    }
    finally { Remove-Item -LiteralPath $configFile -ErrorAction SilentlyContinue }
    Docker @('start', $agentContainer) | Out-Null
    $agentInspect = Inspect-Container $agentContainer
    $agentBinding = @($agentInspect.NetworkSettings.Ports.'7780/tcp')
    if ($agentBinding.Count -ne 1 -or $agentBinding[0].HostIp -ne '127.0.0.1') { throw 'AgentSQL API was not bound to host loopback' }
    $agentURL = 'http://127.0.0.1:' + $agentBinding[0].HostPort
    $healthy = $false
    for ($attempt = 0; $attempt -lt 30; $attempt++) {
        try {
            $health = Invoke-RestMethod -Uri "$agentURL/healthz" -Method Get -TimeoutSec 3
            if ($health.status -eq 'ok') { $healthy = $true; break }
        }
        catch { Start-Sleep -Seconds 2 }
    }
    if (-not $healthy) { throw ('AgentSQL startup failed: ' + ((Docker @('logs', '--tail', '40', $agentContainer)) -join ' ')) }
    Step ("AgentSQL health status=$($health.status) version=$($health.version) url=$agentURL")

    Step 'Register dm datasource, readonly AgentSQL key, table policy, and phone mask'
    $token = (Api 'POST' '/api/v1/auth/login' @{ username = 'admin'; password = $controlAdminPassword } '').token
    if (-not $token) { throw 'AgentSQL admin login returned no token' }
    $datasource = Api 'POST' '/api/v1/datasources' @{
        id = 'dm8-b65'; name = 'DM8 batch65'; db_type = 'dm'; host = $VerifyContainer
        port = 5236; database = 'SYSDBA'; username = $reader; password = $readerPassword
        conn_limit = 5; stmt_timeout_ms = 5000; row_limit = 100
    } $token
    Step ("AgentSQL datasource id=$($datasource.id) db_type=$($datasource.db_type) host=$($datasource.host) port=$($datasource.port)")
    $ping = Api 'POST' '/api/v1/datasources/dm8-b65/ping' $null $token
    Step ('AgentSQL datasource ping: ' + ($ping | ConvertTo-Json -Depth 5 -Compress))
    $agent = Api 'POST' '/api/v1/agents' @{ id = 'dm8-b65-agent'; name = 'DM8 batch65 readonly'; level = 'readonly' } $token
    $apiKey = $agent.api_key
    if (-not $apiKey) { throw 'AgentSQL did not return a new API key' }
    Api 'POST' '/api/v1/policies' @{
        id = 'dm8-b65-table'; agent_id = 'dm8-b65-agent'; datasource_id = 'dm8-b65'
        object_type = 'table'; object_name = "SYSDBA.$table"; action = 'allow'
    } $token | Out-Null
    Api 'POST' '/api/v1/mask_rules' @{
        id = 'dm8-b65-phone'; datasource_id = 'dm8-b65'; table_name = $table
        column_name = 'PHONE'; sensitive_type = 'phone'; algo = 'mask'
    } $token | Out-Null

    $mcpHeaders = @{
        Authorization = "Bearer $apiKey"
        Accept = 'application/json, text/event-stream'
        'MCP-Protocol-Version' = '2025-06-18'
    }
    $initialize = @{
        jsonrpc = '2.0'; id = 0; method = 'initialize'
        params = @{ protocolVersion = '2025-06-18'; capabilities = @{}; clientInfo = @{ name = 'batch65'; version = '1' } }
    } | ConvertTo-Json -Depth 10 -Compress
    $initialResponse = Invoke-WebRequest -Uri "$agentURL/mcp" -Method Post -Headers $mcpHeaders -ContentType 'application/json' -Body $initialize -UseBasicParsing
    $sessionID = $initialResponse.Headers['Mcp-Session-Id']
    if (-not $sessionID) { throw 'MCP initialize returned no transport session' }
    $mcpHeaders['Mcp-Session-Id'] = $sessionID

    Step 'Verify DM8 SELECT, parser lineage, phone mask, R010 denial, and audit'
    $allowedResponse = Call-MCP 1 'query' @{ datasource_id = 'dm8-b65'; sql = "SELECT ID, PHONE FROM SYSDBA.$table WHERE ID=2" }
    $allowed = $allowedResponse.result.structuredContent
    Step ('AgentSQL SELECT response: ' + ($allowed | ConvertTo-Json -Depth 12 -Compress))
    $deniedResponse = Call-MCP 2 'query' @{ datasource_id = 'dm8-b65'; sql = "SELECT ID FROM SYSDBA.$denyTable WHERE ID=7" }
    $denied = $deniedResponse.result.structuredContent
    Step ('AgentSQL R010 response: ' + ($denied | ConvertTo-Json -Depth 12 -Compress))
    $commentResponse = Call-MCP 3 'query' @{ datasource_id = 'dm8-b65'; sql = "SELECT /* batch65-r006 */ ID FROM SYSDBA.$table WHERE ID=2" }
    $comment = $commentResponse.result.structuredContent
    Step ('AgentSQL comment response: ' + ($comment | ConvertTo-Json -Depth 12 -Compress))
    $audits = Api 'GET' '/api/v1/audit?agent_id=dm8-b65-agent&page_size=20' $null $token
    Step ('AgentSQL audit response: ' + ($audits | ConvertTo-Json -Depth 12 -Compress))
    if ($allowed.decision -ne 'allow' -or $allowed.data.result.RowCount -ne 1 -or
        $allowed.data.result.Rows[0][1] -ne '138****5678' -or $allowed.data.redact.MaskedCells -ne 1) {
        throw 'AgentSQL DM8 SELECT or phone masking did not pass'
    }
    if ($denied.decision -ne 'deny' -or @($denied.data.hits | Where-Object { $_.RuleID -eq 'R010' }).Count -ne 1) {
        throw 'AgentSQL DM8 R010 policy denial did not pass'
    }
    if ($comment.decision -ne 'error' -or $comment.error_stage -ne 'parse' -or $comment.error_code -ne 'DB_SYNTAX_ERROR') {
        throw 'AgentSQL DM8 comment parse boundary did not fail closed'
    }
    $allowedAudit = @($audits.list | Where-Object { $_.decision -eq 'allow' -and $_.objects -eq "SYSDBA.$table" -and $_.rows_returned -eq 1 })
    $deniedAudit = @($audits.list | Where-Object { $_.decision -eq 'deny' -and $_.objects -eq "SYSDBA.$denyTable" -and $_.rule_hits -match 'R010' })
    $errorAudit = @($audits.list | Where-Object { $_.decision -eq 'error' -and $_.error_code -eq 'DB_SYNTAX_ERROR' })
    if ($audits.total -ne 3 -or $allowedAudit.Count -ne 1 -or $deniedAudit.Count -ne 1 -or $errorAudit.Count -ne 1) {
        throw 'AgentSQL DM8 audit records did not match all three probe decisions'
    }
    Step 'PASS: SELECT, column lineage, R010 denial, phone mask, parse failure, and three audit rows'

    Docker @('stop', $agentContainer) | Out-Null
    Require-Disql 'drop verification reader' @("DROP USER $reader CASCADE;") 'successfully' $adminPassword | Out-Null
    Require-Disql 'drop verification table' @("DROP TABLE $table;") 'successfully' $adminPassword | Out-Null
    Require-Disql 'drop ungranted-policy table' @("DROP TABLE $denyTable;") 'successfully' $adminPassword | Out-Null
    Step 'PASS: temporary DM8 user and tables removed; licensed DM8 container retained; AgentSQL container stopped'
}
catch {
    Step ('FAIL: ' + $_.Exception.Message)
    exit 1
}
finally {
    if (-not $verifyReady -and $sourceWasRunning) {
        $old = Inspect-Container $SourceContainer
        if ($null -ne $old -and -not $old.State.Running) {
            $new = Inspect-Container $VerifyContainer
            if ($null -ne $new -and $new.State.Running) { Docker @('stop', $VerifyContainer) | Out-Null }
            Docker @('start', $SourceContainer) | Out-Null
            Step 'Restored original DM8 container after failed verification'
        }
    }
    if ($transcriptStarted) { Stop-Transcript | Out-Null }
}

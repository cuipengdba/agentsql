param(
    [string]$ImageTar = 'D:\ruanjiansheji\db-images\jincang\KingbaseES_V009R001C010B0004_x86_64_Docker.tar',
    [string]$LicenseFile = 'D:\ruanjiansheji\db-images\jincang\license_4_V009R001C-企业版-180天.dat',
    [string]$DockerExe = 'E:\Docker\DockerDesktop\resources\bin\docker.exe',
    [string]$ContainerName = 'kingbase-v9-batch69',
    [int]$ReadyTimeoutSeconds = 120
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$env:DOCKER_CONFIG = Join-Path (Split-Path $PSScriptRoot -Parent) '.docker-config'
$transcriptPath = Join-Path $env:TEMP 'kingbase-v9-batch69-transcript.txt'
Start-Transcript -LiteralPath $transcriptPath -Force | Out-Null

function Step([string]$message) {
    Write-Output ("[{0}] {1}" -f (Get-Date -Format 'yyyy-MM-ddTHH:mm:ssK'), $message)
}

function Docker([string[]]$arguments) {
    $output = @(& $DockerExe @arguments 2>&1)
    if ($LASTEXITCODE -ne 0) {
        throw ("docker {0} failed (exit {1}): {2}" -f $arguments[0], $LASTEXITCODE, ($output -join "`n"))
    }
    return $output
}

function Docker-Exists([string[]]$arguments) {
    if ($arguments.Count -ne 3 -or $arguments[1] -ne 'inspect') { throw 'Unknown Docker existence check' }
    $kind = $arguments[0]
    $name = $arguments[2]
    switch ($kind) {
        'container' { $ids = Docker @('container', 'ls', '-aq', '--filter', "name=^/${name}$") }
        'volume'    { $ids = Docker @('volume', 'ls', '-q', '--filter', "name=^${name}$") }
        'network'   { $ids = Docker @('network', 'ls', '-q', '--filter', "name=^${name}$") }
        default     { throw "Unknown Docker object kind: $kind" }
    }
    return (@($ids | Where-Object { $_ }).Count -gt 0)
}

function Cleanup {
    Step 'Cleanup: stop AgentSQL and remove temporary V9 database container'
    if ($script:agentContainer -and (Docker-Exists @('container', 'inspect', $script:agentContainer))) {
        Docker @('rm', '-f', $script:agentContainer) | Out-Null
    }
    if ($script:ContainerName -and (Docker-Exists @('container', 'inspect', $script:ContainerName))) {
        # The database data directory is container-local. Removing the
        # container removes this run's synthetic role and table even if SQL
        # cleanup failed before the server became available.
        Docker @('rm', '-f', $script:ContainerName) | Out-Null
    }
    if ($script:agentDataVolume -and (Docker-Exists @('volume', 'inspect', $script:agentDataVolume))) {
        Docker @('volume', 'rm', $script:agentDataVolume) | Out-Null
    }
    if ($script:LicenseHash -and (Get-FileHash -LiteralPath $script:LicenseFile -Algorithm SHA256).Hash -ne $script:LicenseHash) {
        throw 'Source license SHA-256 changed during verification'
    }
    Step 'PASS: temporary containers and their synthetic role/table removed'
}

$script:agentContainer = 'kingbase-v9-agentsql-batch69'
$script:agentDataVolume = 'kingbase-v9-batch69-agentsql-data'
$script:LicenseHash = $null
trap {
    Step ("FAIL: {0}" -f $_.Exception.Message)
    try { Cleanup } catch { Step ("FAIL: cleanup: {0}" -f $_.Exception.Message) }
    Stop-Transcript | Out-Null
    exit 1
}

function Read-DatabaseLog {
    param([string]$dataDir)
    $tempLog = Join-Path $env:TEMP ('kingbase-batch69-' + [Guid]::NewGuid().ToString('N') + '.log')
    try {
        Docker @('cp', ("${ContainerName}:${dataDir}/logfile"), $tempLog) | Out-Null
        return (Get-Content -LiteralPath $tempLog -Raw)
    }
    finally {
        Remove-Item -LiteralPath $tempLog -ErrorAction SilentlyContinue
    }
}

if (-not (Test-Path -LiteralPath $DockerExe -PathType Leaf)) {
    $dockerCommand = Get-Command docker -ErrorAction SilentlyContinue
    if (-not $dockerCommand) { throw 'Docker CLI not found' }
    $DockerExe = $dockerCommand.Source
}
foreach ($required in @($ImageTar, $LicenseFile)) {
    if (-not (Test-Path -LiteralPath $required -PathType Leaf)) { throw "Input not found: $required" }
}
$script:LicenseHash = (Get-FileHash -LiteralPath $LicenseFile -Algorithm SHA256).Hash

Step 'Load supplied V9 image'
$load = Docker @('load', '-i', $ImageTar)
$load | ForEach-Object { Write-Output $_ }
$tagLine = @($load | Where-Object { $_ -match '^Loaded image: (.+)$' }) | Select-Object -First 1
if (-not $tagLine) { throw 'docker load did not return a tagged image' }
$imageTag = ([regex]::Match($tagLine, '^Loaded image: (.+)$')).Groups[1].Value
$image = (Docker @('image', 'inspect', $imageTag, '--format', '{{json .}}') | Select-Object -First 1) | ConvertFrom-Json
if ($image.Config.Entrypoint.Count -ne 2 -or $image.Config.Entrypoint[0] -ne '/bin/bash') {
    throw 'Unexpected image entrypoint; inspect before proceeding'
}
$entrypoint = [string]$image.Config.Entrypoint[1]
$ports = @($image.Config.ExposedPorts.PSObject.Properties.Name)
if ($ports.Count -ne 1 -or $ports[0] -notmatch '^([0-9]+)/tcp$') {
    throw 'Image must expose exactly one TCP port; inspect before proceeding'
}
$containerPort = [int]$Matches[1]
Step ("Image tag={0} id={1} digest={2} author={3}" -f $imageTag, $image.Id, ($image.RepoDigests -join ','), $image.Author)
Step ("Image entrypoint={0} exposed_port={1} user={2}" -f $entrypoint, $containerPort, $image.Config.User)
Step ("Image env={0}" -f ($image.Config.Env -join ';'))
if ($image.Id -eq 'sha256:0bce318e74adca7a3d619b55b336269017507fd679833b7ce5d8400289661724') {
    Step 'PASS: image ID equals batch64: image unchanged; only license input changed'
}
else { Step 'PASS: image ID differs from batch64; use newly loaded image' }

Step 'Inspect vendor entrypoint and initdb metadata'
$script = (Docker @('run', '--rm', '--entrypoint', '/bin/cat', $imageTag, $entrypoint)) -join "`n"
$dbPathMatch = [regex]::Match($script, '(?m)^\s*DB_PATH=(/[^\s]+)\s*$')
$dataDirMatch = [regex]::Match($script, '(?m)^\s*DATA_DIR=(/[^\s]+)\s*$')
$userMatch = [regex]::Match($script, '(?m)DB_USER=([A-Za-z_][A-Za-z0-9_]*)')
$databaseMatch = [regex]::Match($script, '(?m)local DB_NAME="([A-Za-z_][A-Za-z0-9_]*)"')
if (-not ($dbPathMatch.Success -and $dataDirMatch.Success -and $userMatch.Success -and $databaseMatch.Success)) {
    throw 'Cannot derive DB path, data directory, user and database from vendor entrypoint'
}
$dbPath = $dbPathMatch.Groups[1].Value
$dataDir = $dataDirMatch.Groups[1].Value
$dbUser = $userMatch.Groups[1].Value
$database = $databaseMatch.Groups[1].Value
$licenseInBin = "$dbPath/bin/license.dat"
$licenseInEtc = "$dbPath/etc/license.dat"
if (-not $script.Contains('mv ${DB_PATH}/bin/license.dat ${etc_PATH}/license.dat') -or
    -not $script.Contains('ln -s ${etc_PATH}/license.dat ${DB_PATH}/bin/license.dat') -or
    -not $script.Contains('${DB_PATH}/bin/initdb') -or
    -not $script.Contains('${DB_PATH}/bin/sys_ctl')) {
    throw 'Cannot confirm vendor license move/symlink and initdb/sys_ctl startup commands'
}
$initdbHelp = (Docker @('run', '--rm', '--entrypoint', "$dbPath/bin/initdb", $imageTag, '--help')) -join "`n"
$defaultMode = [regex]::Match($initdbHelp, 'set database mode\(default value is ([^)]+)\)')
if (-not $defaultMode.Success) { throw 'Cannot determine vendor default database mode' }
$licenseInImage = (Docker @('run', '--rm', '--entrypoint', '/usr/bin/stat', $imageTag, '-c', '%s', $licenseInBin) | Select-Object -First 1)
Step ("Vendor data_dir={0} default_user={1} default_database={2} default_mode={3}" -f $dataDir, $dbUser, $database, $defaultMode.Groups[1].Value)
Step ("Vendor license source={0} runtime={1} image_license_bytes={2}" -f $licenseInBin, $licenseInEtc, $licenseInImage)
Step ("Supplied license bytes={0} sha256={1}" -f (Get-Item -LiteralPath $LicenseFile).Length, $script:LicenseHash)

# The source license is mounted read-only. The vendor entrypoint moves its
# default license, so use its confirmed initdb/sys_ctl commands directly.
# A license requiring write access must fail closed here.
Step 'Check read-only source license mount and prepare dedicated container'
if (Docker-Exists @('container', 'inspect', $ContainerName)) { Docker @('rm', '-f', $ContainerName) | Out-Null }
$licenseMount = "type=bind,source=$LicenseFile,target=/license/license.dat,readonly"
$licenseStat = @(Docker @('run', '--rm', '--mount', $licenseMount, '--entrypoint', '/usr/bin/stat', $imageTag, '-c', '%s|%a|%U:%G', '/license/license.dat'))
if ($licenseStat.Count -ne 1 -or $licenseStat[0] -notmatch '^[0-9]+\|[0-7]+\|') { throw 'Unknown mounted license path or permissions' }
Step ("PASS: read-only license mount metadata bytes/mode/owner={0}" -f $licenseStat[0])

$bytes = New-Object byte[] 20
$rng = [Security.Cryptography.RandomNumberGenerator]::Create()
try { $rng.GetBytes($bytes) } finally { $rng.Dispose() }
$password = ([BitConverter]::ToString($bytes)).Replace('-', '').ToLowerInvariant()

# The vendor entrypoint moves bin/license.dat after initdb. Avoid that move;
# resolve bin/license.dat to the read-only mount before invoking the server.
$shell = 'set -e; DB_PATH=' + $dbPath + '; DATA_DIR=' + $dataDir +
    '; rm $DB_PATH/bin/license.dat' +
    '; ln -s /license/license.dat $DB_PATH/bin/license.dat' +
    '; $DB_PATH/bin/initdb -U' + $dbUser + ' -x $DB_PASSWORD -D $DATA_DIR -E UTF-8 -m pg' +
    '; $DB_PATH/bin/sys_ctl -D $DATA_DIR -l $DATA_DIR/logfile start' +
    '; exec tail -F $DATA_DIR/logfile'
Step 'Start V9 in explicit pg mode with loopback-only published port'
Docker @('run', '-d', '--name', $ContainerName, '--publish', "127.0.0.1::${containerPort}",
    '--mount', $licenseMount, '--env', "DB_PASSWORD=$password",
    '--entrypoint', '/bin/bash', $imageTag, '-c', $shell) | Out-Null
$container = (Docker @('inspect', $ContainerName, '--format', '{{json .}}') | Select-Object -First 1) | ConvertFrom-Json
$binding = @($container.NetworkSettings.Ports."${containerPort}/tcp")
if ($binding.Count -ne 1 -or $binding[0].HostIp -ne '127.0.0.1') { throw 'No exclusive loopback port binding found' }
$hostPort = [int]$binding[0].HostPort
Step ("Container={0} host=127.0.0.1:{1} container_port={2} license_mount=readonly" -f $ContainerName, $hostPort, $containerPort)

Step 'Wait for database, checking database logfile on every failure'
$deadline = (Get-Date).AddSeconds($ReadyTimeoutSeconds)
$ready = $false
do {
    $container = (Docker @('inspect', $ContainerName, '--format', '{{json .}}') | Select-Object -First 1) | ConvertFrom-Json
    if ($container.State.Status -eq 'exited') { break }
    $previousErrorAction = $ErrorActionPreference
    try {
        # A refused socket is expected while initdb is still running. In
        # Windows PowerShell, native stderr otherwise becomes a terminating
        # error before LASTEXITCODE can be examined.
        $ErrorActionPreference = 'Continue'
        $probe = @(& $DockerExe exec $ContainerName "$dbPath/bin/ksql" -X -w -A -t -v ON_ERROR_STOP=1 -U $dbUser -d $database -p "$containerPort" -c 'SELECT 1;' 2>&1)
        $probeExit = $LASTEXITCODE
    }
    finally { $ErrorActionPreference = $previousErrorAction }
    if ($probeExit -eq 0 -and ($probe -join "`n") -match '(?m)^1\s*$') { $ready = $true; break }
    Start-Sleep -Seconds 2
} while ((Get-Date) -lt $deadline)
if (-not $ready) {
    $dbLog = Read-DatabaseLog -dataDir $dataDir
    Step 'FAIL: database did not become ready; database logfile follows'
    Write-Output $dbLog.TrimEnd()
    throw 'V9 readiness/license verification failed; PG protocol and AgentSQL checks were not run'
}
Step 'PASS: database ready and license accepted by server startup'
$dbLog = Read-DatabaseLog -dataDir $dataDir
$licenseLogLines = @($dbLog -split "`n" | Where-Object { $_ -match '(?i)license|starting KingbaseES|expir' })
if ($licenseLogLines.Count -eq 0) { throw 'Database started but log provided no license/version lines' }
Step 'Server license/version log lines:'
$licenseLogLines | ForEach-Object { Write-Output $_.TrimEnd() }

$ksql = @('exec', $ContainerName, "$dbPath/bin/ksql", '-X', '-w', '-A', '-t', '-v', 'ON_ERROR_STOP=1', '-U', $dbUser, '-d', $database, '-p', "$containerPort")
foreach ($sql in @(
    'SELECT version();',
    'SHOW database_mode;',
    'CREATE TABLE public.agentsql_batch69_verify (id integer PRIMARY KEY, name varchar(40), phone varchar(20));',
    "INSERT INTO public.agentsql_batch69_verify VALUES (1, 'batch69', '13812345678');",
    'SELECT id, name, phone FROM public.agentsql_batch69_verify WHERE id = 1;',
    "UPDATE public.agentsql_batch69_verify SET name = 'updated' WHERE id = 1;",
    'SELECT id, name FROM public.agentsql_batch69_verify WHERE id = 1;',
    'SELECT id FROM public.agentsql_batch69_verify ORDER BY id LIMIT 1 OFFSET 0;',
    'SELECT id AS "MiXeD" FROM public.agentsql_batch69_verify WHERE id = 1;',
    'SELECT id AS MiXeD FROM public.agentsql_batch69_verify WHERE id = 1;',
    'DELETE FROM public.agentsql_batch69_verify WHERE id = 1;',
    'SELECT count(*) FROM public.agentsql_batch69_verify;'
)) {
    Step ("ksql: {0}" -f $sql)
    $sqlOutput = @(Docker ($ksql + @('-c', $sql)))
    $sqlOutput | ForEach-Object { Write-Output $_ }
    $joined = $sqlOutput -join "`n"
    if ($sql -eq 'SHOW database_mode;' -and $joined.Trim() -ne 'pg') { throw "Unexpected database mode: $joined" }
    if ($sql.StartsWith('UPDATE ') -and $joined.Trim() -ne 'UPDATE 1') { throw "UPDATE did not affect one row: $joined" }
    if ($sql.StartsWith('DELETE ') -and $joined.Trim() -ne 'DELETE 1') { throw "DELETE did not affect one row: $joined" }
    if ($sql -eq 'SELECT count(*) FROM public.agentsql_batch69_verify;' -and $joined.Trim() -ne '0') {
        throw "DELETE verification count was not zero: $joined"
    }
    if ($sql.StartsWith('SELECT id FROM public.agentsql_batch69_verify ORDER BY') -and $joined.Trim() -ne '1') { throw "LIMIT/OFFSET failed: $joined" }
}
foreach ($sql in @('SELECT id AS "MiXeD" FROM public.agentsql_batch69_verify WHERE id = 1 LIMIT 0;',
                  'SELECT id AS MiXeD FROM public.agentsql_batch69_verify WHERE id = 1 LIMIT 0;')) {
    $headerKsql = @($ksql | Where-Object { $_ -ne '-t' })
    $headers = @(Docker ($headerKsql + @('-c', $sql)))
    Step ("identifier header for {0}: {1}" -f $sql, ($headers -join '|'))
    if ($headers.Count -lt 1) { throw 'Identifier header missing' }
    if ($sql -match '"MiXeD"' -and $headers[0].Trim() -cne 'MiXeD') { throw 'Quoted identifier did not preserve case' }
    if ($sql -notmatch '"MiXeD"' -and $headers[0].Trim() -cne 'mixed') { throw 'Unquoted identifier did not fold to lower case' }
}
Step 'PASS: ksql version, PG mode, CRUD, LIMIT/OFFSET and identifier probes'

Step 'Create synthetic row and a minimum SELECT-only database role'
Docker ($ksql + @('-c', "INSERT INTO public.agentsql_batch69_verify VALUES (1, 'Alice', '13812345678');")) | Out-Null
$rolePasswordBytes = New-Object byte[] 20
$rng = [Security.Cryptography.RandomNumberGenerator]::Create()
try { $rng.GetBytes($rolePasswordBytes) } finally { $rng.Dispose() }
$rolePassword = ([BitConverter]::ToString($rolePasswordBytes)).Replace('-', '').ToLowerInvariant()
foreach ($sql in @(
    "CREATE ROLE agentsql_ro LOGIN PASSWORD '$rolePassword';",
    "GRANT CONNECT ON DATABASE $database TO agentsql_ro;",
    'GRANT USAGE ON SCHEMA public TO agentsql_ro;',
    'GRANT SELECT ON public.agentsql_batch69_verify TO agentsql_ro;'
)) { Docker ($ksql + @('-c', $sql)) | Out-Null }
Step 'PASS: readonly role created with CONNECT, USAGE and one-table SELECT only'
$hbaLines = @(Docker @('exec', $ContainerName, '/bin/grep', '-E', '^host[[:space:]]', "$dataDir/sys_hba.conf"))
$hbaLines | ForEach-Object { Step ("HBA: {0}" -f $_) }
if (-not ($hbaLines -match 'scram-sha-256')) { throw 'SCRAM authentication is not configured for TCP host connections' }

Step 'Verify host-side PostgreSQL protocol, bind parameter and database permission'
$goExe = Join-Path $env:USERPROFILE 'go\pkg\mod\golang.org\toolchain@v0.0.1-go1.26.0.windows-amd64\bin\go.exe'
if (-not (Test-Path -LiteralPath $goExe -PathType Leaf)) {
    $goCommand = Get-Command go -ErrorAction SilentlyContinue
    if (-not $goCommand) { throw 'Go 1.26 CLI not found for host-side pgx verification' }
    $goExe = $goCommand.Source
}
$probeSource = Join-Path $env:TEMP ('kingbase-batch69-' + [Guid]::NewGuid().ToString('N') + '.go')
$env:KINGBASE_TEST_DSN = "postgres://agentsql_ro:$rolePassword@127.0.0.1:$hostPort/${database}?sslmode=disable"
$previousGoToolchain = $env:GOTOOLCHAIN
$previousGoCache = $env:GOCACHE
try {
    @'
package main
import("context";"fmt";"os";"github.com/jackc/pgx/v5")
func main(){ctx:=context.Background();c,e:=pgx.Connect(ctx,os.Getenv("KINGBASE_TEST_DSN"));if e!=nil{fmt.Println("CONNECT_FAIL",e);os.Exit(1)};defer c.Close(ctx);var user,mode,version,phone string;e=c.QueryRow(ctx,"SELECT current_user, current_setting('database_mode'), version()").Scan(&user,&mode,&version);if e!=nil{fmt.Println("QUERY_FAIL",e);os.Exit(1)};e=c.QueryRow(ctx,"SELECT phone FROM public.agentsql_batch69_verify WHERE id=$1",1).Scan(&phone);if e!=nil{fmt.Println("BIND_FAIL",e);os.Exit(1)};tx,e:=c.Begin(ctx);if e!=nil{fmt.Println("BEGIN_FAIL",e);os.Exit(1)};_,writeErr:=tx.Exec(ctx,"UPDATE public.agentsql_batch69_verify SET name='bad' WHERE id=1");_ = tx.Rollback(ctx);if writeErr==nil{fmt.Println("WRITE_UNEXPECTEDLY_ALLOWED");os.Exit(1)};fmt.Printf("host_pgx_user=%s mode=%s phone=%s denied_write=%v version=%s\n",user,mode,phone,writeErr,version)}
'@ | Set-Content -LiteralPath $probeSource -Encoding UTF8
    $env:GOTOOLCHAIN = 'local'
    $env:GOPROXY = 'off'
    $env:GOCACHE = Join-Path $env:TEMP 'kingbase-batch69-host-gocache'
    $pgOutput = @(& $goExe run $probeSource 2>&1)
    if ($LASTEXITCODE -ne 0) { throw "host pgx verification failed: $($pgOutput -join ' ')" }
    $pgOutput | ForEach-Object { Write-Output $_ }
    if (($pgOutput -join "`n") -notmatch 'SQLSTATE 42501') { throw 'Readonly write denial did not report SQLSTATE 42501' }
    Step 'PASS: host pgx/v5 $1 bind, SCRAM and readonly SQLSTATE 42501'
}
finally {
    $env:GOTOOLCHAIN = $previousGoToolchain
    $env:GOCACHE = $previousGoCache
    Remove-Item Env:GOPROXY -ErrorAction SilentlyContinue
    Remove-Item Env:KINGBASE_TEST_DSN -ErrorAction SilentlyContinue
    Remove-Item -LiteralPath $probeSource -ErrorAction SilentlyContinue
}

Step 'Build current AgentSQL source offline in local Go/CGO Docker image'
$repoRoot = Split-Path $PSScriptRoot -Parent
$moduleCache = Join-Path $env:USERPROFILE 'go\pkg\mod'
if (-not (Test-Path -LiteralPath $moduleCache -PathType Container)) { throw 'Local Go module cache unavailable for offline build' }
$buildVolume = 'kingbase-batch69-go-build'
$cacheVolume = 'kingbase-batch69-go-cache'
Docker @('volume', 'create', $buildVolume) | Out-Null
Docker @('volume', 'create', $cacheVolume) | Out-Null
Docker @('run', '--rm', '--network', 'none',
    '--mount', "type=bind,source=$repoRoot,target=/src,readonly",
    '--mount', "type=bind,source=$moduleCache,target=/gomod,readonly",
    '--mount', "type=volume,source=$buildVolume,target=/out",
    '--mount', "type=volume,source=$cacheVolume,target=/buildcache",
    '--workdir', '/src', '--env', 'GOPROXY=off', '--env', 'GOTOOLCHAIN=local', '--env', 'GOMODCACHE=/gomod',
    '--env', 'GOCACHE=/buildcache', '--env', 'CGO_ENABLED=1',
    'golang:1.26-bookworm', 'go', 'build', './...') | Out-Null
Step 'PASS: offline go build ./...'
Docker @('run', '--rm', '--network', 'none',
    '--mount', "type=bind,source=$repoRoot,target=/src,readonly",
    '--mount', "type=bind,source=$moduleCache,target=/gomod,readonly",
    '--mount', "type=volume,source=$cacheVolume,target=/buildcache",
    '--workdir', '/src', '--env', 'GOPROXY=off', '--env', 'GOTOOLCHAIN=local', '--env', 'GOMODCACHE=/gomod',
    '--env', 'GOCACHE=/buildcache', '--env', 'CGO_ENABLED=1',
    'golang:1.26-bookworm', 'go', 'test', '-short', './internal/parser', './internal/mask') | ForEach-Object { Write-Output $_ }
Step 'PASS: offline go test -short ./internal/parser ./internal/mask'
Docker @('run', '--rm', '--network', 'none',
    '--mount', "type=bind,source=$repoRoot,target=/src,readonly",
    '--mount', "type=bind,source=$moduleCache,target=/gomod,readonly",
    '--mount', "type=volume,source=$buildVolume,target=/out",
    '--mount', "type=volume,source=$cacheVolume,target=/buildcache",
    '--workdir', '/src', '--env', 'GOPROXY=off', '--env', 'GOTOOLCHAIN=local', '--env', 'GOMODCACHE=/gomod',
    '--env', 'GOCACHE=/buildcache', '--env', 'CGO_ENABLED=1',
    'golang:1.26-bookworm', 'go', 'build', '-o', '/out/agentsql', './cmd/agentsql') | Out-Null
Step 'PASS: offline AgentSQL binary built'

Step 'Start current AgentSQL build on private Docker network with loopback-only HTTP port'
$network = 'kingbase-batch69-net'
if (-not (Docker-Exists @('network', 'inspect', $network))) { Docker @('network', 'create', $network) | Out-Null }
$networkNames = @($container.NetworkSettings.Networks.PSObject.Properties.Name)
if ($networkNames -notcontains $network) { Docker @('network', 'connect', $network, $ContainerName) | Out-Null }
$agentContainer = 'kingbase-v9-agentsql-batch69'
$agentDataVolume = 'kingbase-v9-batch69-agentsql-data'
if (Docker-Exists @('container', 'inspect', $agentContainer)) { Docker @('rm', '-f', $agentContainer) | Out-Null }
if (Docker-Exists @('volume', 'inspect', $agentDataVolume)) { Docker @('volume', 'rm', $agentDataVolume) | Out-Null }
Docker @('volume', 'create', $agentDataVolume) | Out-Null
$secretBytes = New-Object byte[] 16
$rng = [Security.Cryptography.RandomNumberGenerator]::Create()
try {
    $rng.GetBytes($secretBytes)
    $agentSecret = ([BitConverter]::ToString($secretBytes)).Replace('-', '').ToLowerInvariant()
    $rng.GetBytes($secretBytes)
    $adminPassword = ([BitConverter]::ToString($secretBytes)).Replace('-', '').ToLowerInvariant()
}
finally { $rng.Dispose() }
$configFile = Join-Path $env:TEMP ('kingbase-batch69-' + [Guid]::NewGuid().ToString('N') + '.yaml')
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
    Docker @('create', '--name', $agentContainer, '--network', $network,
        '--publish', '127.0.0.1::7780',
        '--mount', "type=volume,source=$buildVolume,target=/app,readonly",
        '--mount', "type=volume,source=$agentDataVolume,target=/data",
        '--env', "AGENTSQL_SECRET=$agentSecret", '--env', "AGENTSQL_ADMIN_PASSWORD=$adminPassword",
        '--entrypoint', '/app/agentsql', 'debian:bookworm-slim', 'serve', '--config', '/data/config.yaml') | Out-Null
    Docker @('cp', $configFile, "${agentContainer}:/data/config.yaml") | Out-Null
}
finally { Remove-Item -LiteralPath $configFile -ErrorAction SilentlyContinue }
Docker @('start', $agentContainer) | Out-Null
$agentInspect = (Docker @('inspect', $agentContainer, '--format', '{{json .}}') | Select-Object -First 1) | ConvertFrom-Json
$agentBinding = @($agentInspect.NetworkSettings.Ports.'7780/tcp')
if ($agentBinding.Count -ne 1 -or $agentBinding[0].HostIp -ne '127.0.0.1') { throw 'AgentSQL not bound exclusively to loopback' }
$agentURL = 'http://127.0.0.1:' + $agentBinding[0].HostPort
$healthy = $false
for ($attempt = 0; $attempt -lt 30; $attempt++) {
    try {
        $health = Invoke-RestMethod -Uri "$agentURL/healthz" -Method Get -TimeoutSec 3
        if ($health.status -eq 'ok') { $healthy = $true; break }
    }
    catch { Start-Sleep -Seconds 2 }
}
if (-not $healthy) { throw "AgentSQL did not become healthy: $((Docker @('logs', '--tail', '50', $agentContainer)) -join ' ')" }
Step ("PASS: AgentSQL health status={0} version={1} url={2}" -f $health.status, $health.version, $agentURL)

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

$token = (Api 'POST' '/api/v1/auth/login' @{ username = 'admin'; password = $adminPassword } '').token
if (-not $token) { throw 'AgentSQL admin login returned no token' }
$datasource = Api 'POST' '/api/v1/datasources' @{
    id = 'kb-v9'; name = 'Kingbase V9 batch69'; db_type = 'postgres'; host = $ContainerName
    port = $containerPort; database = $database; username = 'agentsql_ro'; password = $rolePassword
    conn_limit = 5; stmt_timeout_ms = 5000; row_limit = 100
} $token
Step ("Datasource id={0} db_type={1}" -f $datasource.id, $datasource.db_type)
$agent = Api 'POST' '/api/v1/agents' @{ id = 'kb-v9-agent'; name = 'Kingbase V9 readonly'; level = 'readonly' } $token
$apiKey = $agent.api_key
if (-not $apiKey) { throw 'AgentSQL did not return a new API key' }
Api 'POST' '/api/v1/policies' @{
    id = 'kb-v9-table'; agent_id = 'kb-v9-agent'; datasource_id = 'kb-v9'
    object_type = 'table'; object_name = 'public.agentsql_batch69_verify'; action = 'allow'
} $token | Out-Null
Api 'POST' '/api/v1/mask_rules' @{
    id = 'kb-v9-phone'; datasource_id = 'kb-v9'; table_name = 'agentsql_batch69_verify'
    column_name = 'phone'; sensitive_type = 'phone'; algo = 'mask'
} $token | Out-Null

$mcpHeaders = @{
    Authorization = "Bearer $apiKey"
    Accept = 'application/json, text/event-stream'
    'MCP-Protocol-Version' = '2025-06-18'
}
$initialize = @{
    jsonrpc = '2.0'; id = 0; method = 'initialize'
    params = @{ protocolVersion = '2025-06-18'; capabilities = @{}; clientInfo = @{ name = 'batch69'; version = '1' } }
} | ConvertTo-Json -Depth 10 -Compress
$initialResponse = Invoke-WebRequest -Uri "$agentURL/mcp" -Method Post -Headers $mcpHeaders -ContentType 'application/json' -Body $initialize -UseBasicParsing
$sessionID = $initialResponse.Headers['Mcp-Session-Id']
if (-not $sessionID) { throw 'MCP initialize returned no transport session' }
$mcpHeaders['Mcp-Session-Id'] = $sessionID

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

Step 'Verify AgentSQL SELECT, scoped phone masking, R006 denial and audit records'
$allowed = (Call-MCP 1 'query' @{
    datasource_id = 'kb-v9'; sql = 'SELECT id, name, phone FROM public.agentsql_batch69_verify WHERE id = 1 LIMIT 10'
}).result.structuredContent
$row = @($allowed.data.result.rows)[0]
Step ("SELECT decision={0} audit_id={1} masked_cells={2} row={3}" -f $allowed.decision, $allowed.data.audit_id, $allowed.data.redact.MaskedCells, ($row -join '|'))
Step 'Verify physical ProjectionLineages for the same SELECT through the registered postgres parser'
$lineageProbe = Join-Path $repoRoot 'scripts/kingbase_b69_lineage_probe_temp.go'
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
 p,e:=parser.NewParser(model.DialectPostgres); if e!=nil { panic(e) }
 ast,e:=p.Parse("SELECT id, name, phone FROM public.agentsql_batch69_verify WHERE id = 1 LIMIT 10"); if e!=nil { panic(e) }
 expected:=[]string{"id","name","phone"}
 if len(ast.ProjectionLineages)!=len(expected) { fmt.Println("LINEAGE_COUNT_FAIL"); os.Exit(1) }
 for i,col:=range expected {
  line:=ast.ProjectionLineages[i]
  if len(line.Arms)!=1 || len(line.Arms[0].Dependencies)!=1 { fmt.Println("LINEAGE_ARM_FAIL",i); os.Exit(1) }
  origin:=line.Arms[0].Dependencies[0].Origin
  if origin.Relation.Schema!="public" || origin.Relation.Table!="agentsql_batch69_verify" || origin.Column!=col { fmt.Println("LINEAGE_ORIGIN_FAIL",i,origin.Relation.Schema,origin.Relation.Table,origin.Column); os.Exit(1) }
  fmt.Printf("ProjectionLineages[%d]=%s <- %s.%s.%s\n",i,line.OutputName,origin.Relation.Schema,origin.Relation.Table,origin.Column)
 }
}
'@ | Set-Content -LiteralPath $lineageProbe -Encoding ASCII
    Docker @('run', '--rm', '--network', 'none',
        '--mount', "type=bind,source=$repoRoot,target=/src,readonly",
        '--mount', "type=bind,source=$moduleCache,target=/gomod,readonly",
        '--mount', "type=volume,source=$cacheVolume,target=/buildcache",
        '--workdir', '/src', '--env', 'GOPROXY=off', '--env', 'GOTOOLCHAIN=local', '--env', 'GOMODCACHE=/gomod',
        '--env', 'GOCACHE=/buildcache', '--env', 'CGO_ENABLED=1',
        'golang:1.26-bookworm', 'go', 'run', './scripts/kingbase_b69_lineage_probe_temp.go') | ForEach-Object { Write-Output $_ }
}
finally { Remove-Item -LiteralPath $lineageProbe -ErrorAction SilentlyContinue }
Step 'PASS: ProjectionLineages physical origins match the synthetic V9 table'
$deniedCall = Call-MCP 2 'query' @{
    datasource_id = 'kb-v9'; sql = 'SELECT /* batch69-r006 */ id FROM public.agentsql_batch69_verify WHERE id = 1 LIMIT 1'
}
$denied = $null
if ($deniedCall.PSObject.Properties['result'] -and $deniedCall.result.PSObject.Properties['structuredContent']) {
    $denied = $deniedCall.result.structuredContent
}
if (-not $denied) {
    Step 'FAIL: R006 probe returned no structured decision (possible parse-error)'
    $parseAudits = Api 'GET' '/api/v1/audit?agent_id=kb-v9-agent&page_size=20' $null $token
    Step ("Audit after unstructured R006 response total={0} decisions={1}" -f $parseAudits.total, (@($parseAudits.list | ForEach-Object { $_.decision }) -join ','))
    throw 'R006 parser/response not verified'
}
$ruleIDs = @($denied.data.hits | ForEach-Object { $_.RuleID })
Step ("R006 decision={0} audit_id={1} rule_ids={2}" -f $denied.decision, $denied.data.audit_id, ($ruleIDs -join ','))
$audits = Api 'GET' '/api/v1/audit?agent_id=kb-v9-agent&page_size=20' $null $token
$auditDecisions = @($audits.list | ForEach-Object { $_.decision })
Step ("Audit total={0} decisions={1}" -f $audits.total, ($auditDecisions -join ','))
if ($allowed.decision -ne 'allow' -or $allowed.data.redact.MaskedCells -ne 1 -or
    ($row -join '|') -notmatch '138\*\*\*\*5678' -or
    $denied.decision -ne 'deny' -or $ruleIDs -notcontains 'R006' -or
    $audits.total -ne 2 -or $auditDecisions -notcontains 'allow' -or $auditDecisions -notcontains 'deny') {
    throw 'AgentSQL V9 guard-chain assertions failed'
}
Step 'PASS: V9 PG protocol, readonly account, AgentSQL query, scoped masking, R006 and audit'
Cleanup
Stop-Transcript | Out-Null
exit 0

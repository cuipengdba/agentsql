param(
    [string]$LicenseFile = 'D:\ruanjiansheji\db-images\hangao\9147d1c2_A20261008N4540397q0lLU.dat',
    [string]$ImageTag = 'agentsql-highgo:9.0.10-b70',
    [int]$ReadyTimeoutSeconds = 180
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$transcript = 'C:\Users\Administrator\AppData\Local\Temp\highgo-batch70-transcript.txt'
Start-Transcript -LiteralPath $transcript -Append | Out-Null
$repo = Split-Path (Split-Path $PSScriptRoot -Parent) -Parent
$dbName = 'highgo-b70-db'
$agentName = 'highgo-b70-agentsql'
$networkName = 'highgo-b70-net'
$buildVolume = 'highgo-b70-buildout'
$cacheVolume = 'highgo-b70-gocache'
$agentVolume = 'highgo-b70-agentdata'
$outDir = Join-Path $PSScriptRoot '.out'
$configFile = Join-Path $env:TEMP 'highgo-b70-agentsql.yaml'
$moduleCache = Join-Path $env:USERPROFILE 'go\pkg\mod'
$script:dbReady = $false
$script:dbPassword = $null
$script:roPassword = $null
$script:status = 1
$licenseHash = $null

function Step([string]$message) {
    Write-Host ('[{0}] {1}' -f (Get-Date -Format 'yyyy-MM-ddTHH:mm:ssK'), $message)
}
function Docker-Try([string[]]$arguments) {
    $previous = $ErrorActionPreference
    try {
        $ErrorActionPreference = 'Continue'
        $lines = @(& docker.exe @arguments 2>&1 | ForEach-Object { "$_" })
        return @{ Code = $LASTEXITCODE; Lines = $lines }
    }
    finally { $ErrorActionPreference = $previous }
}
function Docker([string[]]$arguments) {
    $result = Docker-Try $arguments
    if ($result.Code -ne 0) { throw ('docker {0} exit {1}: {2}' -f $arguments[0], $result.Code, ($result.Lines -join ' ')) }
    return $result.Lines
}
function Docker-Has([string]$kind, [string]$name) {
    return ((Docker-Try @($kind,'inspect',$name)).Code -eq 0)
}
function New-Secret([int]$length = 24) {
    $bytes = New-Object byte[] $length
    $rng = [Security.Cryptography.RandomNumberGenerator]::Create()
    try { $rng.GetBytes($bytes) } finally { $rng.Dispose() }
    return ([BitConverter]::ToString($bytes)).Replace('-','').ToLowerInvariant()
}
function Sql-Try([string]$sql) {
    $previous = $ErrorActionPreference
    try {
        $ErrorActionPreference = 'Continue'
        $lines = @($sql | & docker.exe exec -i -e "PGPASSWORD=$script:dbPassword" $dbName `
            /opt/highgo/bin/psql -X -w -A -t -v ON_ERROR_STOP=1 -U highgo -d highgo -p 5866 2>&1 |
            ForEach-Object { "$_" })
        return @{ Code = $LASTEXITCODE; Lines = $lines }
    }
    finally { $ErrorActionPreference = $previous }
}
function Sql-Header([string]$sql) {
    $previous = $ErrorActionPreference
    try {
        $ErrorActionPreference = 'Continue'
        $lines = @($sql | & docker.exe exec -i -e "PGPASSWORD=$script:dbPassword" $dbName `
            /opt/highgo/bin/psql -X -w -A -v ON_ERROR_STOP=1 -U highgo -d highgo -p 5866 2>&1 |
            ForEach-Object { "$_" })
        if ($LASTEXITCODE -ne 0) { throw ('header SQL failed: {0}' -f ($lines -join ' ')) }
        return $lines[0]
    }
    finally { $ErrorActionPreference = $previous }
}
function Sql([string]$label, [string]$statement) {
    $r = Sql-Try $statement
    if ($r.Code -ne 0) { throw ('SQL {0} exit {1}: {2}' -f $label,$r.Code,($r.Lines -join ' ')) }
    Step ('SQL {0}: {1}' -f $label,($r.Lines -join ' | '))
    return $r.Lines
}
function Go-Run([string[]]$tail, [string[]]$extraEnv, [string[]]$extraMount) {
    $args = @('run','--rm','--pull=never','--network','none',
        '--mount',"type=bind,source=$repo,target=/src,readonly",
        '--mount',"type=bind,source=$moduleCache,target=/gomod,readonly",
        '--mount',"type=volume,source=$cacheVolume,target=/buildcache",
        '--workdir','/src','--env','GOPROXY=off','--env','GOTOOLCHAIN=local',
        '--env','GOMODCACHE=/gomod','--env','GOCACHE=/buildcache')
    foreach ($value in $extraEnv) { $args += @('--env',$value) }
    foreach ($value in $extraMount) { $args += @('--mount',$value) }
    $args += @('golang:1.26-bookworm','go') + $tail
    return Docker $args
}
function Api([string]$method, [string]$path, $body, [string]$token) {
    $headers = @{}
    if ($token) { $headers.Authorization = "Bearer $token" }
    $request = @{ Uri = "$script:agentUrl$path"; Method = $method; Headers = $headers; TimeoutSec = 30 }
    if ($null -ne $body) {
        $request.Body = $body | ConvertTo-Json -Depth 12 -Compress
        $request.ContentType = 'application/json'
    }
    $response = Invoke-RestMethod @request
    if ($response.code -ne 0) { throw "AgentSQL API $path returned code $($response.code)" }
    return $response.data
}
function Call-Mcp([int]$id, [string]$sql) {
    $payload = @{jsonrpc='2.0';id=$id;method='tools/call';params=@{name='query';arguments=@{datasource_id='hg-b70';sql=$sql}}} |
        ConvertTo-Json -Depth 12 -Compress
    $response = Invoke-WebRequest -Uri "$script:agentUrl/mcp" -Method Post -Headers $script:mcpHeaders `
        -ContentType 'application/json' -Body $payload -UseBasicParsing -TimeoutSec 30
    $body = $response.Content
    if ($body -notmatch '^\s*\{') {
        $line = @($body -split "`n" | Where-Object { $_ -match '^data: ' }) | Select-Object -Last 1
        if (-not $line) { throw 'MCP response missing JSON/data event' }
        $body = $line.Substring(6)
    }
    return ($body | ConvertFrom-Json)
}
function Cleanup {
    Step 'Cleanup: remove AgentSQL, SQL objects and temporary HighGo container'
    if (Docker-Has 'container' $agentName) { $null = Docker-Try @('rm','-f',$agentName) }
    if ($script:dbReady -and (Docker-Has 'container' $dbName)) {
        $dropOwned = Sql-Try 'DROP OWNED BY agentsql_b70_ro;'
        $dropTable = Sql-Try 'DROP TABLE IF EXISTS public.agentsql_batch70_verify;'
        $dropRole = Sql-Try 'DROP ROLE IF EXISTS agentsql_b70_ro;'
        Step ('SQL cleanup owned_exit={0} table_exit={1} role_exit={2}' -f $dropOwned.Code,$dropTable.Code,$dropRole.Code)
    }
    if (Docker-Has 'container' $dbName) { $null = Docker-Try @('rm','-f',$dbName) }
    foreach ($volume in @($agentVolume,$buildVolume,$cacheVolume)) {
        if (Docker-Has 'volume' $volume) { $null = Docker-Try @('volume','rm',$volume) }
    }
    if (Docker-Has 'network' $networkName) { $null = Docker-Try @('network','rm',$networkName) }
    if (Test-Path -LiteralPath $configFile) { Remove-Item -LiteralPath $configFile -Force }
    $probeExe = Join-Path $outDir 'highgo-pgx.exe'
    if (Test-Path -LiteralPath $probeExe) { Remove-Item -LiteralPath $probeExe -Force }
    if (Test-Path -LiteralPath $outDir) { Remove-Item -LiteralPath $outDir -Force }
    Step 'Cleanup complete; built image retained, ephemeral database data removed'
}

try {
    if (-not (Test-Path -LiteralPath $LicenseFile -PathType Leaf)) { throw 'license file missing' }
    $licenseHash = (Get-FileHash -LiteralPath $LicenseFile -Algorithm SHA256).Hash
    $licenseBytes = (Get-Item -LiteralPath $LicenseFile).Length
    Step "License bytes=$licenseBytes sha256=$licenseHash (content not printed)"
    $image = @(Docker @('image','inspect',$ImageTag,'--format','{{.Id}} {{.Size}}'))[0]
    Step "Image=$ImageTag $image"
    $blank = @(Docker @('run','--rm','--pull=never','--network','none','--entrypoint','/usr/bin/stat',
        $ImageTag,'-c','%s','/opt/highgo/license/license.dat'))[0]
    if ($blank.Trim() -ne '0') { throw 'Image contains nonempty license target' }
    Step 'PASS: image license target is an empty placeholder'
    $null = Docker @('image','inspect','golang:1.26-bookworm','--format','{{.Id}}')
    $null = Docker @('image','inspect','debian:bookworm-slim','--format','{{.Id}}')
    if (-not (Test-Path -LiteralPath $moduleCache -PathType Container)) { throw 'Cached Go module directory missing' }
    if (Docker-Has 'container' $dbName -or Docker-Has 'container' $agentName) {
        throw 'Existing batch70 container found; inspect before rerun'
    }
    $script:dbPassword = New-Secret
    $script:roPassword = New-Secret
    $null = Docker @('network','create',$networkName)
    $licenseMount = "type=bind,source=$LicenseFile,target=/opt/highgo/license/license.dat,readonly"
    $null = Docker @('run','-d','--pull=never','--name',$dbName,'--network',$networkName,
        '--publish','127.0.0.1::5866','--mount',$licenseMount,
        '--env',"HGDB_PASSWORD=$script:dbPassword",$ImageTag)
    $inspect = @(Docker @('inspect',$dbName,'--format','{{json .}}'))[0] | ConvertFrom-Json
    $binding = @($inspect.NetworkSettings.Ports.'5866/tcp')
    if ($binding.Count -ne 1 -or $binding[0].HostIp -ne '127.0.0.1') { throw 'Database host port is not loopback only' }
    $hostPort = [int]$binding[0].HostPort
    Step "HighGo container=$dbName host=127.0.0.1:$hostPort license_mount=readonly"
    $deadline = (Get-Date).AddSeconds($ReadyTimeoutSeconds)
    do {
        $probe = Sql-Try 'SELECT 1;'
        if ($probe.Code -eq 0 -and ($probe.Lines -join '').Trim() -eq '1') { $script:dbReady = $true; break }
        $state = @(Docker @('inspect',$dbName,'--format','{{.State.Status}}'))[0]
        if ($state -eq 'exited') { break }
        Start-Sleep -Seconds 2
    } while ((Get-Date) -lt $deadline)
    if (-not $script:dbReady) {
        Step 'FAIL: HighGo did not become ready; container log tail:'
        Docker @('logs','--tail','80',$dbName) | ForEach-Object { Write-Output $_ }
        throw 'HighGo readiness/license verification failed'
    }
    Step 'PASS: HighGo started without license or version FATAL'
    Sql 'version' 'SELECT version();' | Out-Null
    Sql 'database and role' 'SELECT current_database(),current_user;' | Out-Null
    Sql 'create table' 'CREATE TABLE public.agentsql_batch70_verify (id integer PRIMARY KEY, name varchar(40), phone varchar(20));' | Out-Null
    Sql 'insert' "INSERT INTO public.agentsql_batch70_verify VALUES (1,'Alice','13812345678'),(2,'Bob','13911112222');" | Out-Null
    Sql 'select' 'SELECT id,name,phone FROM public.agentsql_batch70_verify ORDER BY id;' | Out-Null
    Sql 'update' "UPDATE public.agentsql_batch70_verify SET name='Alice2' WHERE id=1;" | Out-Null
    Sql 'pagination' 'SELECT id FROM public.agentsql_batch70_verify ORDER BY id LIMIT 1 OFFSET 1;' | Out-Null
    Sql 'quoted identifier' 'SELECT id AS "MiXeD" FROM public.agentsql_batch70_verify LIMIT 0;' | Out-Null
    Sql 'unquoted identifier' 'SELECT id AS MiXeD FROM public.agentsql_batch70_verify LIMIT 0;' | Out-Null
    Sql 'delete' 'DELETE FROM public.agentsql_batch70_verify WHERE id=2;' | Out-Null
    Sql 'post-delete count' 'SELECT count(*) FROM public.agentsql_batch70_verify;' | Out-Null
    $quoted = Sql-Header 'SELECT id AS "MiXeD" FROM public.agentsql_batch70_verify LIMIT 0;'
    $unquoted = Sql-Header 'SELECT id AS MiXeD FROM public.agentsql_batch70_verify LIMIT 0;'
    Step ("identifier_headers quoted={0} unquoted={1}" -f $quoted,$unquoted)
    if ($quoted -cne 'MiXeD' -or $unquoted -cne 'mixed') { throw 'identifier case mismatch' }
    Sql 'create readonly role' "CREATE ROLE agentsql_b70_ro LOGIN PASSWORD '$script:roPassword';" | Out-Null
    Sql 'grant connect' 'GRANT CONNECT ON DATABASE highgo TO agentsql_b70_ro;' | Out-Null
    Sql 'grant schema usage' 'GRANT USAGE ON SCHEMA public TO agentsql_b70_ro;' | Out-Null
    Sql 'grant table select' 'GRANT SELECT ON public.agentsql_batch70_verify TO agentsql_b70_ro;' | Out-Null
    $hba = Docker @('exec',$dbName,'/bin/cat','/var/lib/highgo/data/pg_hba.conf')
    $hostRules = @($hba | Where-Object { $_ -match '^host\s' })
    Step ('HBA host rules: {0}' -f ($hostRules -join ' | '))
    if (($hostRules -join ' ') -notmatch 'scram-sha-256') { throw 'SCRAM host authentication not configured' }
    $ipam = @(Docker @('network','inspect',$networkName,'--format','{{json .IPAM.Config}}'))[0] | ConvertFrom-Json
    $subnet = [string]$ipam[0].Subnet
    if ($subnet -notmatch '^([0-9]{1,3}\.){3}[0-9]{1,3}/[0-9]{1,2}$') { throw 'Unknown Docker network subnet' }
    $hbaLine = "host all all $subnet scram-sha-256"
    $hbaLine | & docker.exe exec -i $dbName /bin/bash -c 'cat >> /var/lib/highgo/data/pg_hba.conf'
    if ($LASTEXITCODE -ne 0) { throw 'Appending test network SCRAM rule failed' }
    Docker @('exec',$dbName,'/opt/highgo/bin/pg_ctl','-D','/var/lib/highgo/data','reload') | Out-Null
    Step "HBA test network rule: $hbaLine"

    $null = Docker @('volume','create',$buildVolume)
    $null = Docker @('volume','create',$cacheVolume)
    $null = Docker @('volume','create',$agentVolume)
    New-Item -ItemType Directory -Path $outDir -Force | Out-Null
    Step 'Offline go build ./...'
    Go-Run @('build','./...') @('CGO_ENABLED=1') @() | ForEach-Object { Write-Output $_ }
    Step 'PASS: offline go build ./...'
    Go-Run @('test','-short','./internal/parser','./internal/mask','./scripts/highgo/pgx-probe') @('CGO_ENABLED=1') @() |
        ForEach-Object { Write-Output $_ }
    Step 'PASS: changed probe package and parser/mask short tests'
    Go-Run @('build','-o','/out/agentsql','./cmd/agentsql') @('CGO_ENABLED=1') @("type=volume,source=$buildVolume,target=/out") |
        ForEach-Object { Write-Output $_ }
    Go-Run @('build','-o','/out/highgo-pgx.exe','./scripts/highgo/pgx-probe') @('GOOS=windows','GOARCH=amd64','CGO_ENABLED=0') `
        @("type=bind,source=$outDir,target=/out") | ForEach-Object { Write-Output $_ }
    $env:HIGHGO_RO_DSN = "postgres://agentsql_b70_ro:$script:roPassword@127.0.0.1:$hostPort/highgo?sslmode=disable"
    try {
        & (Join-Path $outDir 'highgo-pgx.exe') 2>&1 | ForEach-Object { Write-Output $_ }
        if ($LASTEXITCODE -ne 0) { throw "host pgx probe exit $LASTEXITCODE" }
    }
    finally { Remove-Item Env:HIGHGO_RO_DSN -ErrorAction SilentlyContinue }
    Step 'PASS: host pgx/v5 $1 binding, SCRAM login and SQLSTATE 42501'

    $agentSecret = New-Secret 16
    $adminPassword = New-Secret
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
    $null = Docker @('create','--name',$agentName,'--network',$networkName,'--publish','127.0.0.1::7780',
        '--mount',"type=volume,source=$buildVolume,target=/app,readonly",
        '--mount',"type=volume,source=$agentVolume,target=/data",
        '--env',"AGENTSQL_SECRET=$agentSecret",'--env',"AGENTSQL_ADMIN_PASSWORD=$adminPassword",
        '--entrypoint','/app/agentsql','debian:bookworm-slim','serve','--config','/data/config.yaml')
    $null = Docker @('cp',$configFile,"${agentName}:/data/config.yaml")
    $null = Docker @('start',$agentName)
    $agentInspect = @(Docker @('inspect',$agentName,'--format','{{json .}}'))[0] | ConvertFrom-Json
    $agentBinding = @($agentInspect.NetworkSettings.Ports.'7780/tcp')
    if ($agentBinding.Count -ne 1 -or $agentBinding[0].HostIp -ne '127.0.0.1') { throw 'AgentSQL API not loopback only' }
    $script:agentUrl = 'http://127.0.0.1:' + $agentBinding[0].HostPort
    $healthy = $false
    for ($i=0; $i -lt 30; $i++) {
        try { $health = Invoke-RestMethod -Uri "$script:agentUrl/healthz" -TimeoutSec 3; if ($health.status -eq 'ok') { $healthy=$true; break } }
        catch { Start-Sleep -Seconds 2 }
    }
    if (-not $healthy) { throw 'AgentSQL health check failed' }
    Step "AgentSQL healthy at $script:agentUrl"
    $token = (Api 'POST' '/api/v1/auth/login' @{username='admin';password=$adminPassword} '').token
    if (-not $token) { throw 'AgentSQL login returned no token' }
    $source = Api 'POST' '/api/v1/datasources' @{
        id='hg-b70';name='HighGo 9.0 batch70';db_type='postgres';host=$dbName;port=5866;database='highgo';
        username='agentsql_b70_ro';password=$script:roPassword;conn_limit=5;stmt_timeout_ms=5000;row_limit=100
    } $token
    Step "Datasource id=$($source.id) db_type=$($source.db_type)"
    $agent = Api 'POST' '/api/v1/agents' @{id='hg-b70-agent';name='HighGo batch70 readonly';level='readonly'} $token
    $apiKey = $agent.api_key
    if (-not $apiKey) { throw 'AgentSQL agent API key missing' }
    Api 'POST' '/api/v1/policies' @{
        id='hg-b70-table';agent_id='hg-b70-agent';datasource_id='hg-b70';object_type='table';
        object_name='public.agentsql_batch70_verify';action='allow'
    } $token | Out-Null
    Api 'POST' '/api/v1/mask_rules' @{
        id='hg-b70-phone';datasource_id='hg-b70';table_name='agentsql_batch70_verify';
        column_name='phone';sensitive_type='phone';algo='mask'
    } $token | Out-Null
    $script:mcpHeaders = @{
        Authorization="Bearer $apiKey";Accept='application/json, text/event-stream';
        'MCP-Protocol-Version'='2025-06-18'
    }
    $initial = @{jsonrpc='2.0';id=0;method='initialize';params=@{
        protocolVersion='2025-06-18';capabilities=@{};clientInfo=@{name='batch70';version='1'}
    }} | ConvertTo-Json -Depth 10 -Compress
    $response = Invoke-WebRequest -Uri "$script:agentUrl/mcp" -Method Post -Headers $script:mcpHeaders `
        -ContentType 'application/json' -Body $initial -UseBasicParsing
    $sessionID = $response.Headers['Mcp-Session-Id']
    if (-not $sessionID) { throw 'MCP initialize returned no session ID' }
    $script:mcpHeaders['Mcp-Session-Id'] = $sessionID
    $allowCall = Call-Mcp 1 'SELECT id, name, phone FROM public.agentsql_batch70_verify WHERE id=1 LIMIT 10'
    $allowed = $allowCall.result.structuredContent
    $row = @($allowed.data.result.rows)[0]
    Step ('SELECT decision={0} MaskedCells={1} row={2}' -f $allowed.decision,$allowed.data.redact.MaskedCells,($row -join '|'))
    if ($allowed.decision -ne 'allow' -or $allowed.data.redact.MaskedCells -ne 1 -or
        ($row -join '|') -notmatch '138\*\*\*\*5678') { throw 'AgentSQL masking assertion failed' }
    Go-Run @('run','./scripts/highgo/lineage-probe.go') @('CGO_ENABLED=1') @() | ForEach-Object { Write-Output $_ }
    $denyCall = Call-Mcp 2 'SELECT /* batch70-r006 */ id FROM public.agentsql_batch70_verify WHERE id=1 LIMIT 1'
    $denied = $denyCall.result.structuredContent
    if (-not $denied) {
        Step 'Rule comment probe returned no structured decision; possible parser rejection'
        $auditAfterParse = Api 'GET' '/api/v1/audit?agent_id=hg-b70-agent&page_size=20' $null $token
        Step ('Audit after parser response total={0} decisions={1}' -f $auditAfterParse.total,
            (@($auditAfterParse.list | ForEach-Object { $_.decision }) -join ','))
        throw 'R006 deny probe unverified'
    }
    $ruleIDs = @($denied.data.hits | ForEach-Object { $_.RuleID })
    Step ('R006 decision={0} rule_ids={1}' -f $denied.decision,($ruleIDs -join ','))
    $audits = Api 'GET' '/api/v1/audit?agent_id=hg-b70-agent&page_size=20' $null $token
    $decisions = @($audits.list | ForEach-Object { $_.decision })
    Step ('Audit total={0} decisions={1}' -f $audits.total,($decisions -join ','))
    if ($denied.decision -ne 'deny' -or $ruleIDs -notcontains 'R006' -or $audits.total -ne 2 -or
        $decisions -notcontains 'allow' -or $decisions -notcontains 'deny') { throw 'R006/audit assertions failed' }
    Step 'PASS: HighGo SQL, pgx, AgentSQL masking, lineage, R006 and audit checks'
    $script:status = 0
}
catch {
    Step "FAIL: $($_.Exception.Message)"
}
finally {
    try { Cleanup } catch { Step "FAIL cleanup: $($_.Exception.Message)"; $script:status = 1 }
    if (Test-Path -LiteralPath $LicenseFile -PathType Leaf) {
        $afterHash = (Get-FileHash -LiteralPath $LicenseFile -Algorithm SHA256).Hash
        if ($licenseHash -and $afterHash -ne $licenseHash) { Step 'FAIL source license hash changed'; $script:status = 1 }
    }
    $script:dbPassword = $null
    $script:roPassword = $null
    Stop-Transcript | Out-Null
}
exit $script:status

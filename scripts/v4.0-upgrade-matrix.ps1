[CmdletBinding()]
param(
    [ValidateSet('v3.0.2','v3.5.0','v3.9.0')][string]$FromVersion,
    [Parameter(Mandatory=$true)][string]$PackageDirectory,
    [Parameter(Mandatory=$true)][string]$CandidateDirectory,
    [string]$FixtureRoot='D:\CodexLS\tmp\agentsql-v400',
    [string]$Python='D:\CodexLS\runtimes\agentsql-i18n\Scripts\python.exe'
)
$ErrorActionPreference='Stop'
$root=Join-Path $FixtureRoot "upgrade-$FromVersion"
if(Test-Path -LiteralPath $root){throw 'Upgrade fixture already exists; preserve it and select another FixtureRoot'}
$archive=Join-Path $PackageDirectory "agentsql-$FromVersion-linux-amd64.tar.gz"
$expected=((Get-Content -LiteralPath "$archive.sha256" -Raw).Trim() -split '\s+')[0]
$actual=(Get-FileHash -LiteralPath $archive -Algorithm SHA256).Hash.ToLowerInvariant()
if($expected -cne $actual){throw 'Official source package digest mismatch'}
Write-Output "SOURCE_VERSION=$FromVersion SHA256=$actual"
$old=Join-Path $PackageDirectory "agentsql-$FromVersion-linux-amd64"
if(-not (Test-Path -LiteralPath "$old\agentsqlctl")){& tar -xzf $archive -C $PackageDirectory;if($LASTEXITCODE -ne 0){throw 'Source package extraction failed'}}
$sqlite=Join-Path $root 'sqlite'
New-Item -ItemType Directory -Path $sqlite | Out-Null
[IO.File]::WriteAllText((Join-Path $sqlite 'isolated-fixture'),'synthetic v4 upgrade only')
$secret=[Convert]::ToBase64String([Security.Cryptography.RandomNumberGenerator]::GetBytes(24))
$pgPassword=[Convert]::ToHexString([Security.Cryptography.RandomNumberGenerator]::GetBytes(24)).ToLowerInvariant()
[IO.File]::WriteAllText("$root\runtime.env","AGENTSQL_SECRET=$secret`nPOSTGRES_PASSWORD=$pgPassword`nAGENTSQL_STORE_METADATA_DSN=postgres://postgres:${pgPassword}@localhost:5432/agentsql_meta?sslmode=disable`nAGENTSQL_STORE_AUDIT_DSN=postgres://postgres:${pgPassword}@localhost:5432/agentsql_audit?sslmode=disable`n",[Text.UTF8Encoding]::new($false))
Copy-Item -LiteralPath "$root\runtime.env" -Destination "$root\runtime-backup.env"
$envHash=(Get-FileHash "$root\runtime.env").Hash
$common=@'
server:
  http_listen: '0.0.0.0:7780'
  console_enabled: false
defaults:
  statement_timeout_ms: 5000
  row_limit: 1000
  max_conns_per_datasource: 5
  qps_per_agent: 20
theme:
  default: dark
'@
[IO.File]::WriteAllText("$sqlite\config.yaml",$common+"`nstore:`n  sqlite_path: '/out/metadata.db'`n",[Text.UTF8Encoding]::new($false))
$pgConfig=$common+"`nstore:`n  metadata:`n    driver: postgres`n  audit:`n    separate: true`n    driver: postgres`n"
[IO.File]::WriteAllText("$root\paired-pg.yaml",$pgConfig,[Text.UTF8Encoding]::new($false))
$suffix=$FromVersion.Replace('.','')+'-'+[Guid]::NewGuid().ToString('N').Substring(0,6)
$pg="agentsql-v400-upgrade-$suffix-pg"
$gateway="agentsql-v400-upgrade-$suffix-gateway"
function DockerChecked([string[]]$Arguments){ & docker @Arguments; if($LASTEXITCODE -ne 0){throw 'Isolated upgrade Docker command failed'} }
function Ctl([string]$binary,[string]$layout,[string]$mode){
 $mount=if($layout -eq 'sqlite'){$sqlite}else{$root}
 $network=if($layout -eq 'sqlite'){'none'}else{"container:$pg"}
 $config=if($layout -eq 'sqlite'){'/out/config.yaml'}else{'/out/paired-pg.yaml'}
 $dockerArgs=@('run','--rm','--network',$network,'--env-file',"$root\runtime.env",'-e','AGENTSQL_STORE_METADATA_DSN=','-e','AGENTSQL_STORE_AUDIT_DSN=','-v',"${mount}:/out",'-v',"${binary}:/bin-candidate:ro",'debian:bookworm-slim','/bin-candidate/agentsqlctl','migrate','--config',$config)
 if($layout -eq 'pg'){$dockerArgs=@('run','--rm','--network',$network,'--env-file',"$root\runtime.env",'-v',"${mount}:/out",'-v',"${binary}:/bin-candidate:ro",'debian:bookworm-slim','/bin-candidate/agentsqlctl','migrate','--config',$config)}
 if($mode -eq 'check'){$dockerArgs+='--check'}
 DockerChecked $dockerArgs
}
function Gateway([string]$binary,[string]$layout,[string]$expectedVersion){
 $mount=if($layout -eq 'sqlite'){$sqlite}else{$root};$network=if($layout -eq 'sqlite'){'none'}else{"container:$pg"};$config=if($layout -eq 'sqlite'){'/out/config.yaml'}else{'/out/paired-pg.yaml'}
 $dockerArgs=@('run','-d','--name',$gateway,'--network',$network,'--env-file',"$root\runtime.env")
 if($layout -eq 'sqlite'){$dockerArgs+=@('-e','AGENTSQL_STORE_METADATA_DSN=','-e','AGENTSQL_STORE_AUDIT_DSN=')}
 $dockerArgs+=@('-v',"${mount}:/out",'-v',"${binary}:/bin-candidate:ro",'debian:bookworm-slim','/bin-candidate/agentsql','serve','--config',$config)
 DockerChecked $dockerArgs | Out-Null
 try{
  for($i=0;$i -lt 60;$i++){
   if ((& docker inspect $gateway --format '{{.State.Running}}').Trim() -ne 'true') { throw 'Synthetic gateway exited before health; inspect the saved gateway log' }
   $health=& docker exec $gateway /bin-candidate/agentsqlctl health --url http://127.0.0.1:7780/healthz 2>$null
   if($LASTEXITCODE -eq 0){$report=$health|ConvertFrom-Json;if($report.version -cne $expectedVersion){throw 'Gateway version mismatch'};Write-Output "GATEWAY=$expectedVersion LAYOUT=$layout HEALTH=PASS";return}
   Start-Sleep -Seconds 1
  };throw 'Gateway health deadline'
 }finally{& docker logs $gateway *> "$root\$layout-$expectedVersion-gateway.log";& docker stop $gateway | Out-Null;& docker rm $gateway | Out-Null}
}
function Fixture([string]$mode){& $Python "$PSScriptRoot\v4.0-upgrade-fixture.py" $sqlite $mode;if($LASTEXITCODE -ne 0){throw "SQLite fixture failed: $mode"}}
Ctl $old 'sqlite' 'migrate';Fixture 'seed';Gateway $old 'sqlite' $FromVersion
Ctl $CandidateDirectory 'sqlite' 'migrate';Ctl $CandidateDirectory 'sqlite' 'check';Gateway $CandidateDirectory 'sqlite' 'v4.0.0';Fixture 'check'
Fixture 'mutate';Fixture 'restore';Ctl $old 'sqlite' 'check';Gateway $old 'sqlite' $FromVersion;Fixture 'check'
Write-Output "SQLITE_UPGRADE_RESTORE=$FromVersion PASS"

# Synthetic fixture data is memory-backed; paired backups remain on the host.
# This tests migrations and restore correctness, not disk/power-loss durability.
DockerChecked @('run','-d','--name',$pg,'--network','none','--tmpfs','/var/lib/postgresql:rw,size=1g','--env-file',"$root\runtime.env",'postgres:18') | Out-Null
try{
 for($i=0;$i -lt 120;$i++){& docker exec $pg pg_isready -h 127.0.0.1 -U postgres *> $null;if($LASTEXITCODE -eq 0){break};Start-Sleep -Seconds 2}
 if($LASTEXITCODE -ne 0){throw 'Isolated PostgreSQL not ready'}
 function SQL([string]$db,[string]$statement){$statement|& docker exec -i $pg psql -U postgres -d $db -v ON_ERROR_STOP=1 -At;if($LASTEXITCODE -ne 0){throw 'Synthetic SQL fixture failed'}}
 foreach($db in @('agentsql_meta','agentsql_audit')){DockerChecked @('exec',$pg,'createdb','-U','postgres',$db)}
 Ctl $old 'pg' 'migrate';Ctl $old 'pg' 'check'
 $metadata=@'
INSERT INTO agents(id,name,api_key_hash,level) VALUES('upgrade-agent','Synthetic upgrade reader','unusable-fixture-hash','readonly');
INSERT INTO datasources(id,name,db_type,host,port,database,username,password_enc) VALUES('upgrade-source','Synthetic source','postgres','invalid.example.test',5432,'fixture','synthetic_reader','opaque-encrypted-fixture');
INSERT INTO policies(id,agent_id,datasource_id,object_type,object_name,columns,action) VALUES('upgrade-policy','upgrade-agent','upgrade-source','table','public.synthetic','id,phone','select');
INSERT INTO mask_rules(id,datasource_id,table_name,column_name,sensitive_type,algo) VALUES('upgrade-mask','upgrade-source','synthetic','phone','phone','mask');
'@
 SQL 'agentsql_meta' $metadata | Out-Null
 SQL 'agentsql_audit' "INSERT INTO audit_logs(agent_id,datasource_id,decision,rows_returned,mcp_tool) VALUES('upgrade-agent','upgrade-source','deny',0,'query');" | Out-Null
 function CheckPG{
  $checks=@(
   @('agentsql_meta',"SELECT name||'|'||api_key_hash||'|'||level FROM agents WHERE id='upgrade-agent'",'Synthetic upgrade reader|unusable-fixture-hash|readonly'),
   @('agentsql_meta',"SELECT host||'|'||password_enc FROM datasources WHERE id='upgrade-source'",'invalid.example.test|opaque-encrypted-fixture'),
   @('agentsql_meta',"SELECT object_name||'|'||columns||'|'||action FROM policies WHERE id='upgrade-policy'",'public.synthetic|id,phone|select'),
   @('agentsql_meta',"SELECT column_name||'|'||algo FROM mask_rules WHERE id='upgrade-mask'",'phone|mask'),
   @('agentsql_audit',"SELECT decision||'|'||rows_returned FROM audit_logs WHERE agent_id='upgrade-agent'",'deny|0')
  )
  foreach($check in $checks){$result=SQL $check[0] $check[1];if($result.Trim() -cne $check[2]){throw 'PostgreSQL synthetic record preservation failed'}}
 }
 CheckPG;Gateway $old 'pg' $FromVersion
 foreach($db in @('agentsql_meta','agentsql_audit')){DockerChecked @('exec',$pg,'pg_dump','-U','postgres','-Fc','-d',$db,'-f',"/tmp/$db.dump");DockerChecked @('cp',"${pg}:/tmp/$db.dump","$root\$db.dump")}
 Ctl $CandidateDirectory 'pg' 'migrate';Ctl $CandidateDirectory 'pg' 'check';Gateway $CandidateDirectory 'pg' 'v4.0.0';CheckPG
 SQL 'agentsql_meta' "UPDATE agents SET name='temporary-v4-marker' WHERE id='upgrade-agent'" | Out-Null
 SQL 'agentsql_audit' "UPDATE audit_logs SET decision='temporary-v4-marker' WHERE agent_id='upgrade-agent'" | Out-Null
 foreach($db in @('agentsql_meta','agentsql_audit')){DockerChecked @('exec',$pg,'dropdb','-U','postgres',$db);DockerChecked @('exec',$pg,'createdb','-U','postgres',$db);DockerChecked @('exec',$pg,'pg_restore','-U','postgres','-d',$db,'--no-owner',"/tmp/$db.dump")}
 Ctl $old 'pg' 'check';Gateway $old 'pg' $FromVersion;CheckPG
 if((Get-FileHash "$root\runtime.env").Hash -cne $envHash -or (Get-FileHash "$root\runtime-backup.env").Hash -cne $envHash){throw 'Paired secret backup changed'}
 Write-Output "PAIRED_POSTGRES_UPGRADE_RESTORE=$FromVersion PASS"
 Get-FileHash "$sqlite\backup.db","$root\agentsql_meta.dump","$root\agentsql_audit.dump" | Select-Object Path,Hash
}finally{& docker stop $pg | Out-Null}

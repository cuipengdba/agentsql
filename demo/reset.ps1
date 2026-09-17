[CmdletBinding()]
param(
    [string]$EnvFile = ""
)

$ErrorActionPreference = "Stop"
$ProjectName = "agentsql-demo"
$env:COMPOSE_PROJECT_NAME = $ProjectName
$Stage = "startup"
$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$RepoRoot = Split-Path -Parent $ScriptDir
$ComposeFile = Join-Path $RepoRoot "docker-compose.demo.yml"
if ([string]::IsNullOrWhiteSpace($EnvFile)) {
    $EnvFile = Join-Path $ScriptDir "demo.env"
} elseif (-not [System.IO.Path]::IsPathRooted($EnvFile)) {
    $EnvFile = Join-Path (Get-Location) $EnvFile
}
$ComposeBase = @("compose", "--project-name", $ProjectName, "--file", $ComposeFile, "--env-file", $EnvFile)

function Get-EnvFileValue {
    param([string]$Name)
    foreach ($line in Get-Content -LiteralPath $EnvFile -Encoding UTF8) {
        if ($line -match "^$([regex]::Escape($Name))=(.*)$") {
            return $Matches[1].Trim()
        }
    }
    return ""
}

function Invoke-Compose {
    & docker @ComposeBase @args
    if ($LASTEXITCODE -ne 0) {
        throw "docker compose failed with exit code $LASTEXITCODE"
    }
}

function Invoke-Docker {
    & docker @args
    if ($LASTEXITCODE -ne 0) {
        throw "docker failed with exit code $LASTEXITCODE"
    }
}

function Assert-CountLines {
    param([string[]]$Lines)
    $clean = @($Lines | ForEach-Object { $_.Trim() } | Where-Object { $_ -ne "" })
    foreach ($expected in @("customers=128", "products=64", "orders=2400", "internal_notes=16")) {
        if ($clean -notcontains $expected) {
            throw "missing expected count $expected; got: $($clean -join ', ')"
        }
    }
}

try {
    if (-not (Test-Path -LiteralPath $EnvFile -PathType Leaf)) {
        throw "Missing $EnvFile. Copy examples/docker/demo.env.example to demo/demo.env and replace every credential."
    }

    $anchor = [Environment]::GetEnvironmentVariable("DEMO_ANCHOR_DATE", "Process")
    if ([string]::IsNullOrWhiteSpace($anchor)) {
        $anchor = Get-EnvFileValue "DEMO_ANCHOR_DATE"
    }
    if ([string]::IsNullOrWhiteSpace($anchor)) {
        $anchor = [DateTime]::UtcNow.ToString("yyyy-MM-dd")
    }
    $parsedAnchor = [DateTime]::MinValue
    if (-not [DateTime]::TryParseExact($anchor, "yyyy-MM-dd", [Globalization.CultureInfo]::InvariantCulture, [Globalization.DateTimeStyles]::None, [ref]$parsedAnchor)) {
        throw "DEMO_ANCHOR_DATE must be a real date in YYYY-MM-DD format"
    }
    $env:DEMO_ANCHOR_DATE = $anchor

    $gatewayPort = [Environment]::GetEnvironmentVariable("DEMO_GATEWAY_PORT", "Process")
    if ([string]::IsNullOrWhiteSpace($gatewayPort)) {
        $gatewayPort = Get-EnvFileValue "DEMO_GATEWAY_PORT"
    }
    if ([string]::IsNullOrWhiteSpace($gatewayPort)) {
        $gatewayPort = "17880"
    }

    $Stage = "remove-old-stack-and-volumes"
    Write-Host "[demo-reset] $Stage (anchor=$anchor)"
    Invoke-Compose "down" "-v" "--remove-orphans"

    $Stage = "build-and-start"
    Write-Host "[demo-reset] $Stage"
    Invoke-Compose "up" "-d" "--build" "--wait"

    $Stage = "http-health"
    Write-Host "[demo-reset] $Stage"
    $health = Invoke-RestMethod -Method Get -Uri "http://127.0.0.1:$gatewayPort/healthz"
    if ($health.status -ne "ok" -or $health.demo.enabled -ne $true) {
        throw "/healthz did not report status=ok and demo.enabled=true"
    }
    $ready = Invoke-RestMethod -Method Get -Uri "http://127.0.0.1:$gatewayPort/readyz"
    if ($ready.status -ne "ready") {
        throw "/readyz did not report status=ready"
    }

    $Stage = "verify-control-plane-seed"
    Write-Host "[demo-reset] $Stage"
    $verifyOutput = (Invoke-Compose "run" "--rm" "--no-deps" "demo-seed" `
        "demo-seed" "--config" "/etc/agentsql/config.demo.yaml" `
        "--manifest" "/etc/agentsql/demo-seed.yaml" `
        "--anchor-date" $anchor "--verify-only") -join "`n"
    foreach ($expected in @("DEMO_SEED_VERIFY_OK", "datasources=2", "agents=2", "policies=10", "mask_rules=4", "audits=300", "approvals=24")) {
        if ($verifyOutput -notmatch "(^| )$([regex]::Escape($expected))( |$)") {
            throw "verify-only output is missing $expected"
        }
    }
    # verify-only also validates 180/60/36/24 decisions, 24 linked approvals,
    # and zero missing/orphaned approval audit references.

    # Count SQL is piped over stdin (demo/shared/*.sql). Embedding the SQL in
    # `sh -ec "..."` gets quote-mangled by the Windows docker CLI and returns
    # empty output; stdin avoids every nested-quote path and needs no extra
    # mount because the query never touches the initdb directory.
    $sharedSqlDir = Join-Path $ScriptDir "shared"

    $Stage = "verify-postgres-counts"
    Write-Host "[demo-reset] $Stage"
    $pgContainer = ((Invoke-Compose "ps" "-q" "demo-postgres") | Select-Object -First 1).Trim()
    if ([string]::IsNullOrWhiteSpace($pgContainer)) { throw "demo-postgres container was not found" }
    $pgOwner = Get-EnvFileValue "DEMO_PG_OWNER_USER"
    if ([string]::IsNullOrWhiteSpace($pgOwner)) { $pgOwner = "agentsql_demo_owner" }
    $pgCountSql = Join-Path $sharedSqlDir "pg_counts.sql"
    $pgCounts = @(Get-Content -Raw -LiteralPath $pgCountSql | & docker exec -i $pgContainer psql -U $pgOwner -d agentsql_demo -tA)
    if ($LASTEXITCODE -ne 0) { throw "postgres count query failed with exit $LASTEXITCODE" }
    Assert-CountLines $pgCounts

    $Stage = "verify-mysql-counts"
    Write-Host "[demo-reset] $Stage"
    $mysqlContainer = ((Invoke-Compose "ps" "-q" "demo-mysql") | Select-Object -First 1).Trim()
    if ([string]::IsNullOrWhiteSpace($mysqlContainer)) { throw "demo-mysql container was not found" }
    $mysqlRootPassword = Get-EnvFileValue "DEMO_MYSQL_ROOT_PASSWORD"
    if ([string]::IsNullOrWhiteSpace($mysqlRootPassword)) { throw "DEMO_MYSQL_ROOT_PASSWORD is required for count verification" }
    $mysqlCountSql = Join-Path $sharedSqlDir "mysql_counts.sql"
    $mysqlCounts = @(Get-Content -Raw -LiteralPath $mysqlCountSql | & docker exec -i -e "MYSQL_PWD=$mysqlRootPassword" $mysqlContainer mysql -uroot --database agentsql_demo --batch --skip-column-names)
    if ($LASTEXITCODE -ne 0) { throw "mysql count query failed with exit $LASTEXITCODE" }
    Assert-CountLines $mysqlCounts

    Write-Host "[demo-reset] OK anchor=$anchor gateway=http://127.0.0.1:$gatewayPort"
} catch {
    Write-Error "[demo-reset] FAILED stage=$Stage`: $($_.Exception.Message)"
    exit 1
}

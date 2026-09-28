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
    param([string[]]$Lines, [string[]]$Expected = @("customers=128", "products=64", "orders=2400", "internal_notes=16"))
    $clean = @($Lines | ForEach-Object { $_.Trim() } | Where-Object { $_ -ne "" })
    foreach ($expected in $Expected) {
        if ($clean -notcontains $expected) {
            throw "missing expected count $expected; got: $($clean -join ', ')"
        }
    }
}

function Invoke-MCPTool {
    param(
        [string]$GatewayPort,
        [string]$APIKey,
        [string]$Name,
        [object]$Arguments
    )
    $body = [ordered]@{
        jsonrpc = "2.0"
        id = "$Name-$([Guid]::NewGuid().ToString('N'))"
        method = "tools/call"
        params = [ordered]@{ name = $Name; arguments = $Arguments }
    } | ConvertTo-Json -Depth 20 -Compress
    $headers = @{
        Authorization = "Bearer $APIKey"
        Accept = "application/json, text/event-stream"
        "MCP-Protocol-Version" = "2025-06-18"
    }
    $client = New-Object System.Net.WebClient
    $client.Encoding = [Text.Encoding]::UTF8
    foreach ($header in $headers.GetEnumerator()) { $client.Headers.Add($header.Key, $header.Value) }
    $client.Headers.Add("Content-Type", "application/json; charset=utf-8")
    try {
        $response = $client.UploadString("http://127.0.0.1:$GatewayPort/mcp", "POST", $body) | ConvertFrom-Json
    } finally {
        $client.Dispose()
    }
    if ($null -ne $response.error) {
        throw "MCP transport error for $Name"
    }
    return $response.result.structuredContent
}

function ConvertFrom-Base64Url {
    param([string]$Value)
    $value = $Value.Replace('-', '+').Replace('_', '/')
    switch ($value.Length % 4) {
        2 { $value += '==' }
        3 { $value += '=' }
    }
    return [Convert]::FromBase64String($value)
}

function ConvertTo-Base64Url {
    param([byte[]]$Value)
    return [Convert]::ToBase64String($Value).TrimEnd('=').Replace('+', '-').Replace('/', '_')
}

function Get-BigEndianUInt64 {
    param([UInt64]$Value)
    $bytes = [BitConverter]::GetBytes($Value)
    if ([BitConverter]::IsLittleEndian) { [Array]::Reverse($bytes) }
    return $bytes
}

function Add-LengthPrefixedBytes {
    param([System.Collections.Generic.List[byte]]$Target, [byte[]]$Value)
    foreach ($byte in (Get-BigEndianUInt64 ([UInt64]$Value.Length))) { $Target.Add($byte) }
    foreach ($byte in $Value) { $Target.Add($byte) }
}

function Get-HMACSHA256 {
    param([byte[]]$Key, [byte[]]$Value)
    $hmac = [Security.Cryptography.HMACSHA256]::new($Key)
    try { return $hmac.ComputeHash($Value) } finally { $hmac.Dispose() }
}

function Set-B5Signature {
    param([string]$Secret, [string]$Method, [System.Collections.Specialized.OrderedDictionary]$Arguments)
    $Arguments.continuation_proof = ""
    $Arguments.body_digest = ""
    $canonical = [ordered]@{ schema = "agentsql.b5.mcp-body.v1"; method = $Method; arguments = $Arguments } |
        ConvertTo-Json -Depth 20 -Compress
    # Windows PowerShell's ConvertTo-Json escapes apostrophes as \u0027;
    # Go encoding/json leaves them literal when it re-marshals the decoded
    # typed payload for B5BodyDigest. Normalize to that exact wire contract.
    $canonical = $canonical.Replace('\u0027', [string][char]0x27)
    $sha = [Security.Cryptography.SHA256]::Create()
    try { $bodyDigest = $sha.ComputeHash([Text.Encoding]::UTF8.GetBytes($canonical)) } finally { $sha.Dispose() }
    $Arguments.body_digest = ([BitConverter]::ToString($bodyDigest)).Replace('-', '').ToLowerInvariant()

    $secretBytes = ConvertFrom-Base64Url $Secret
    $sha = [Security.Cryptography.SHA256]::Create()
    try { $salt = $sha.ComputeHash([Text.Encoding]::UTF8.GetBytes([string]$Arguments.session_id)) } finally { $sha.Dispose() }
    $prk = Get-HMACSHA256 $salt $secretBytes
    $info = New-Object System.Collections.Generic.List[byte]
    foreach ($byte in [Text.Encoding]::UTF8.GetBytes("agentsql.b5.continuation-key.v2")) { $info.Add($byte) }
    $info.Add(1)
    $key = Get-HMACSHA256 $prk $info.ToArray()

    $message = New-Object System.Collections.Generic.List[byte]
    foreach ($field in @("agentsql.b5.continuation.v2", $Method, [string]$Arguments.session_id)) {
        Add-LengthPrefixedBytes $message ([Text.Encoding]::UTF8.GetBytes($field))
    }
    Add-LengthPrefixedBytes $message (Get-BigEndianUInt64 ([UInt64]$Arguments.owner_epoch))
    Add-LengthPrefixedBytes $message ([Text.Encoding]::UTF8.GetBytes([string]$Arguments.request_id))
    if ($Arguments.Contains("expected_seq")) {
        Add-LengthPrefixedBytes $message ([Text.Encoding]::UTF8.GetBytes("present"))
        Add-LengthPrefixedBytes $message (Get-BigEndianUInt64 ([UInt64]$Arguments.expected_seq))
    } else {
        Add-LengthPrefixedBytes $message ([Text.Encoding]::UTF8.GetBytes("absent"))
    }
    Add-LengthPrefixedBytes $message $bodyDigest
    $Arguments.continuation_proof = ConvertTo-Base64Url (Get-HMACSHA256 $key $message.ToArray())
}

function New-B5Continuation {
    param([object]$Session, [string]$RequestID)
    return [ordered]@{
        session_id = [string]$Session.session_id
        owner_epoch = [UInt64]$Session.owner_epoch
        request_id = $RequestID
        continuation_proof = ""
        body_digest = ""
    }
}

function Wait-DemoReady {
    param([string]$GatewayPort, [int]$Attempts = 30)
    for ($attempt = 1; $attempt -le $Attempts; $attempt++) {
        try {
            $value = Invoke-RestMethod -Method Get -Uri "http://127.0.0.1:$GatewayPort/readyz"
            if ($value.status -eq "ready") { return $value }
        } catch {}
        Start-Sleep -Seconds 1
    }
    throw "demo gateway did not become ready"
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
    $b2Mode = [string]$health.b2.datasource_modes.'ds-demo-pg'
    if ($health.b2.state -ne "active" -or $health.b2.protocol -ne 3 -or $b2Mode -ne "NATIVE_C_V1") {
        throw "B2 is not active in native mode (state=$($health.b2.state), protocol=$($health.b2.protocol), mode=$b2Mode)"
    }
    if ($health.b2.unsupported_datasources.'ds-demo-mysql' -ne "B2_DATASOURCE_DIALECT_UNSUPPORTED") {
        throw "B2 did not report the MySQL datasource as unsupported"
    }
    if ($null -eq $health.b5 -or $health.b5.ready -ne $true -or $health.b5.state -notin @("READY", "READY_WITH_DATASOURCE_ERRORS")) {
        throw "B5 readiness is not READY/READY_WITH_DATASOURCE_ERRORS"
    }
    $b5Readiness = [string]$health.b5.state
    if ($b5Readiness -eq "READY_WITH_DATASOURCE_ERRORS" -and $health.b5.reason -ne "B5_DATASOURCE_ERRORS_1") {
        throw "B5 datasource error readiness did not identify exactly the expected MySQL rejection"
    }

    $Stage = "verify-control-plane-seed"
    Write-Host "[demo-reset] $Stage"
    $verifyOutput = (Invoke-Compose "run" "--rm" "--no-deps" "demo-seed" `
        "demo-seed" "--config" "/etc/agentsql/config.demo.yaml" `
        "--manifest" "/etc/agentsql/demo-seed.yaml" `
        "--anchor-date" $anchor "--verify-only") -join "`n"
    foreach ($expected in @("DEMO_SEED_VERIFY_OK", "datasources=2", "agents=2", "policies=15", "mask_rules=4", "audits=300", "approvals=24", "b5_grants=5", "b2_mode=NATIVE_C_V1")) {
        if ($verifyOutput -notmatch "(^| )$([regex]::Escape($expected))( |$)") {
            throw "verify-only output is missing $expected"
        }
    }
    # verify-only also validates 180/60/36/24 decisions, 24 linked approvals,
    # and zero missing/orphaned approval audit references.

    # config.demo.yaml has no separate audit store, so the CLI's authoritative
    # auto mapping selects the management domain. Keep the demo manifest
    # explicitly keyless: no key material is read, passed, or printed.
    $Stage = "provision-keyless-audit-chain"
    Write-Host "[demo-reset] $Stage (domain=auto, mode=keyless)"
    $chainProvisionOutput = (Invoke-Compose "exec" "-T" "agentsql-demo" `
        "/usr/local/bin/agentsqlctl" "chain" "provision" `
        "--config" "/etc/agentsql/config.demo.yaml" `
        "--domain" "auto" "--mode" "keyless") -join "`n"
    foreach ($expected in @("status=ACTIVE", "result=VALID_AT_OBSERVED_HEAD")) {
        if ($chainProvisionOutput -notmatch "(^|\s)$([regex]::Escape($expected))(\s|$)") {
            throw "chain provision output is missing $expected"
        }
    }

    $Stage = "verify-keyless-audit-chain-status"
    Write-Host "[demo-reset] $Stage"
    $chainStatusOutput = (Invoke-Compose "exec" "-T" "agentsql-demo" `
        "/usr/local/bin/agentsqlctl" "chain" "status" `
        "--config" "/etc/agentsql/config.demo.yaml" "--domain" "auto") -join "`n"
    foreach ($expected in @("chain_id=management", "status=ACTIVE", "mode=keyless", "result=VALID_AT_OBSERVED_HEAD")) {
        if ($chainStatusOutput -notmatch "(^|\s)$([regex]::Escape($expected))(\s|$)") {
            throw "chain status output is missing $expected"
        }
    }

    $Stage = "verify-keyless-audit-chain"
    Write-Host "[demo-reset] $Stage"
    $chainVerifyOutput = (Invoke-Compose "exec" "-T" "agentsql-demo" `
        "/usr/local/bin/agentsqlctl" "chain" "verify" `
        "--config" "/etc/agentsql/config.demo.yaml" `
        "--domain" "auto" "--mode" "keyless") -join "`n"
    if ($chainVerifyOutput -notmatch "(^|\s)result=VALID_AT_OBSERVED_HEAD(\s|$)") {
        throw "chain verify output is missing result=VALID_AT_OBSERVED_HEAD"
    }

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
    Assert-CountLines $pgCounts @("customers=128", "products=64", "orders=2400", "internal_notes=16", "demo_b2_customers=128", "demo_b2_orders=2400", "demo_tx_accounts=4")

    $Stage = "verify-postgres-native-extension"
    Write-Host "[demo-reset] $Stage"
    $binderState = (& docker exec $pgContainer psql -U $pgOwner -d agentsql_demo -tA -c "SELECT extversion || ':' || (agentsql_catalog.capabilities()->>'abi') FROM pg_catalog.pg_extension WHERE extname='agentsql_binder'").Trim()
    if ($LASTEXITCODE -ne 0 -or $binderState -ne "0.4:agentsql-binder-4.2") {
        throw "agentsql_binder extension is missing or failed its runtime capability check (got=$binderState)"
    }

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

    $roKey = Get-EnvFileValue "AGENTSQL_DEMO_RO_KEY"
    $dmlKey = Get-EnvFileValue "AGENTSQL_DEMO_DML_KEY"
    if ([string]::IsNullOrWhiteSpace($roKey) -or [string]::IsNullOrWhiteSpace($dmlKey)) {
        throw "demo Agent API keys are required for MCP verification"
    }

    $Stage = "verify-b2-join-column-authorization"
    Write-Host "[demo-reset] $Stage (mode=$b2Mode)"
    $positiveSQL = "SELECT c.full_name,peer.region,o.status FROM public.demo_b2_customers c JOIN public.demo_b2_customers peer ON peer.id=c.id JOIN public.demo_b2_orders o ON o.customer_id=c.id WHERE o.id=1"
    $positive = Invoke-MCPTool $gatewayPort $roKey "query" ([ordered]@{ datasource_id = "ds-demo-pg"; sql = $positiveSQL })
    if ($positive.decision -ne "allow" -or $positive.data.result.RowCount -ne 1) {
        throw "B2 JOIN/self-join positive query was not allowed"
    }
    Start-Sleep -Milliseconds 600
    $negativeSQL = "SELECT c.email,peer.region,o.status FROM public.demo_b2_customers c JOIN public.demo_b2_customers peer ON peer.id=c.id JOIN public.demo_b2_orders o ON o.customer_id=c.id WHERE o.id=1"
    $negative = Invoke-MCPTool $gatewayPort $roKey "query" ([ordered]@{ datasource_id = "ds-demo-pg"; sql = $negativeSQL })
    if ($negative.decision -ne "deny" -or $negative.reason -ne "AUTH_COLUMN_GRANT_MISSING" -or $null -ne $negative.data.result) {
        throw "B2 JOIN/self-join unauthorized column did not fail closed"
    }

    $Stage = "verify-mysql-unsupported"
    Write-Host "[demo-reset] $Stage"
    Start-Sleep -Milliseconds 600
    $mysqlColumn = Invoke-MCPTool $gatewayPort $roKey "query" ([ordered]@{ datasource_id = "ds-demo-mysql"; sql = "SELECT full_name FROM customers WHERE id=1" })
    if ($mysqlColumn.decision -ne "error" -or $mysqlColumn.error_code -ne "AUTH_COLUMN_AUTHORIZATION_UNSUPPORTED" -or (($mysqlColumn.reason + $mysqlColumn.suggestion) -notmatch "[\u4e00-\u9fff]")) {
        throw "MySQL B2 unsupported response was not stable and Chinese"
    }
    Start-Sleep -Milliseconds 600
    $mysqlTx = [ordered]@{
        session_id = "not-used"; owner_epoch = [UInt64]1; request_id = "mysql-unsupported"
        continuation_proof = "not-used"; body_digest = ("00" * 32)
        transaction_id = "mysql-unsupported"; datasource_id = "ds-demo-mysql"; dialect = "mysql"
        server_major = 8; key_revision = [UInt64]1; datasource_revision = [UInt64]1; policy_revision = [UInt64]1
        statements = @([ordered]@{ operation_id = "never"; sql = "UPDATE customers SET full_name='x' WHERE id=1"; reason = "verify unsupported" })
    }
    $mysqlB5 = Invoke-MCPTool $gatewayPort $dmlKey "begin_transaction" $mysqlTx
    if ($mysqlB5.decision -ne "error" -or $mysqlB5.error_code -ne "DIALECT_TRANSACTION_UNSUPPORTED" -or (($mysqlB5.reason + $mysqlB5.suggestion) -notmatch "[\u4e00-\u9fff]")) {
        throw "MySQL B5 unsupported response was not stable and Chinese"
    }

    $Stage = "verify-b5-cross-request-commit"
    Write-Host "[demo-reset] $Stage"
    Start-Sleep -Milliseconds 600
    $opened = (Invoke-MCPTool $gatewayPort $dmlKey "open_session" ([ordered]@{})).data
    $begin = New-B5Continuation $opened "demo-b5-begin-commit"
    $begin["transaction_id"] = "demo-b5-commit"
    $begin["datasource_id"] = "ds-demo-pg"
    $begin["dialect"] = "postgres"
    $begin["server_major"] = 16
    $begin["key_revision"] = [UInt64]1
    $begin["datasource_revision"] = [UInt64]1
    $begin["policy_revision"] = [UInt64]1
    $begin["statements"] = @(
        [ordered]@{ operation_id = "balance"; sql = "UPDATE public.demo_tx_accounts SET balance=balance+10 WHERE id=1"; reason = "demo committed balance" },
        [ordered]@{ operation_id = "status"; sql = "UPDATE public.demo_tx_accounts SET status='committed' WHERE id=1"; reason = "demo committed status" }
    )
    Set-B5Signature $opened.continuation_secret "begin_transaction" $begin
    $begun = Invoke-MCPTool $gatewayPort $dmlKey "begin_transaction" $begin
    if ($begun.decision -ne "allow" -or $begun.data.status -ne "ACTIVE") { throw "B5 commit scenario did not begin: $($begun | ConvertTo-Json -Depth 10 -Compress)" }
    foreach ($operation in @(@("balance", 0), @("status", 1))) {
        Start-Sleep -Milliseconds 600
        $execute = New-B5Continuation $opened "demo-b5-execute-$($operation[1])"
        $execute["transaction_id"] = "demo-b5-commit"; $execute["operation_id"] = $operation[0]; $execute["ordinal"] = $operation[1]
        Set-B5Signature $opened.continuation_secret "execute_transaction_statement" $execute
        $executed = Invoke-MCPTool $gatewayPort $dmlKey "execute_transaction_statement" $execute
        if ($executed.decision -ne "allow" -or $executed.data.status -ne "ACTIVE") { throw "B5 commit scenario statement failed: $($executed | ConvertTo-Json -Depth 10 -Compress)" }
    }
    Start-Sleep -Milliseconds 600
    $commit = New-B5Continuation $opened "demo-b5-commit"
    $commit["transaction_id"] = "demo-b5-commit"
    Set-B5Signature $opened.continuation_secret "commit_transaction" $commit
    $committed = Invoke-MCPTool $gatewayPort $dmlKey "commit_transaction" $commit
    $commitTerminal = $committed.db_outcome -eq "COMMITTED" -and $committed.tx_effect -eq "TERMINAL_COMMITTED"
    $commitDispositionSafe = $committed.decision -eq "allow" -or
        ($committed.decision -eq "error" -and $committed.error_code -eq "TX_COMMITTED_CONNECTION_QUARANTINED" -and $committed.connection_disposition -eq "DISCARDED")
    if (-not $commitTerminal -or -not $commitDispositionSafe) { throw "B5 commit scenario did not reach a safe committed terminal: $($committed | ConvertTo-Json -Depth 10 -Compress)" }
    $committedRow = (& docker exec $pgContainer psql -U $pgOwner -d agentsql_demo -tA -c "SELECT balance || ':' || status FROM demo_tx_accounts WHERE id=1").Trim()
    if ($committedRow -ne "1010:committed") { throw "B5 committed values are not visible (got=$committedRow)" }

    $Stage = "verify-b5-cross-request-rollback"
    Write-Host "[demo-reset] $Stage"
    Start-Sleep -Milliseconds 600
    $openedRollback = (Invoke-MCPTool $gatewayPort $dmlKey "open_session" ([ordered]@{})).data
    $rollbackSQL = "UPDATE public.demo_tx_accounts SET balance=balance+7 WHERE id=2"
    $beginRollback = New-B5Continuation $openedRollback "demo-b5-begin-rollback"
    $beginRollback["transaction_id"] = "demo-b5-rollback"; $beginRollback["datasource_id"] = "ds-demo-pg"; $beginRollback["dialect"] = "postgres"
    $beginRollback["server_major"] = 16; $beginRollback["key_revision"] = [UInt64]1; $beginRollback["datasource_revision"] = [UInt64]1; $beginRollback["policy_revision"] = [UInt64]1
    $beginRollback["statements"] = @([ordered]@{ operation_id = "balance"; sql = $rollbackSQL; reason = "demo explicit rollback" })
    Set-B5Signature $openedRollback.continuation_secret "begin_transaction" $beginRollback
    if ((Invoke-MCPTool $gatewayPort $dmlKey "begin_transaction" $beginRollback).decision -ne "allow") { throw "B5 rollback scenario did not begin" }
    Start-Sleep -Milliseconds 600
    $executeRollback = New-B5Continuation $openedRollback "demo-b5-execute-rollback"
    $executeRollback["transaction_id"] = "demo-b5-rollback"; $executeRollback["operation_id"] = "balance"; $executeRollback["ordinal"] = 0
    Set-B5Signature $openedRollback.continuation_secret "execute_transaction_statement" $executeRollback
    if ((Invoke-MCPTool $gatewayPort $dmlKey "execute_transaction_statement" $executeRollback).decision -ne "allow") { throw "B5 rollback scenario statement failed" }
    Start-Sleep -Milliseconds 600
    $rollback = New-B5Continuation $openedRollback "demo-b5-rollback"
    $rollback["transaction_id"] = "demo-b5-rollback"
    Set-B5Signature $openedRollback.continuation_secret "rollback_transaction" $rollback
    $rolledBack = Invoke-MCPTool $gatewayPort $dmlKey "rollback_transaction" $rollback
    $rollbackTerminal = $rolledBack.db_outcome -eq "NOT_COMMITTED" -and $rolledBack.tx_effect -eq "TERMINAL_NOT_COMMITTED"
    $rollbackDispositionSafe = $rolledBack.decision -eq "allow" -or
        ($rolledBack.decision -eq "error" -and $rolledBack.error_code -eq "TX_NOT_COMMITTED_CONNECTION_QUARANTINED" -and $rolledBack.connection_disposition -eq "DISCARDED")
    if (-not $rollbackTerminal -or -not $rollbackDispositionSafe) { throw "B5 rollback scenario did not reach a safe not-committed terminal: $($rolledBack | ConvertTo-Json -Depth 10 -Compress)" }
    $rolledBackValue = (& docker exec $pgContainer psql -U $pgOwner -d agentsql_demo -tA -c "SELECT balance FROM demo_tx_accounts WHERE id=2").Trim()
    if ($rolledBackValue -ne "2000") { throw "B5 rollback leaked a write (got=$rolledBackValue)" }

    $Stage = "verify-b5-interruption-fail-closed"
    Write-Host "[demo-reset] $Stage"
    Start-Sleep -Milliseconds 600
    $openedInterrupted = (Invoke-MCPTool $gatewayPort $dmlKey "open_session" ([ordered]@{})).data
    $interruptSQL = "UPDATE public.demo_tx_accounts SET balance=balance+11 WHERE id=3"
    $beginInterrupted = New-B5Continuation $openedInterrupted "demo-b5-begin-interrupted"
    $beginInterrupted["transaction_id"] = "demo-b5-interrupted"; $beginInterrupted["datasource_id"] = "ds-demo-pg"; $beginInterrupted["dialect"] = "postgres"
    $beginInterrupted["server_major"] = 16; $beginInterrupted["key_revision"] = [UInt64]1; $beginInterrupted["datasource_revision"] = [UInt64]1; $beginInterrupted["policy_revision"] = [UInt64]1
    $beginInterrupted["statements"] = @([ordered]@{ operation_id = "balance"; sql = $interruptSQL; reason = "demo restart interruption" })
    Set-B5Signature $openedInterrupted.continuation_secret "begin_transaction" $beginInterrupted
    if ((Invoke-MCPTool $gatewayPort $dmlKey "begin_transaction" $beginInterrupted).decision -ne "allow") { throw "B5 interruption scenario did not begin" }
    Start-Sleep -Milliseconds 600
    $executeInterrupted = New-B5Continuation $openedInterrupted "demo-b5-execute-interrupted"
    $executeInterrupted["transaction_id"] = "demo-b5-interrupted"; $executeInterrupted["operation_id"] = "balance"; $executeInterrupted["ordinal"] = 0
    Set-B5Signature $openedInterrupted.continuation_secret "execute_transaction_statement" $executeInterrupted
    if ((Invoke-MCPTool $gatewayPort $dmlKey "execute_transaction_statement" $executeInterrupted).decision -ne "allow") { throw "B5 interruption scenario statement failed" }
    Invoke-Compose "restart" "agentsql-demo"
    $readyAfterRestart = Wait-DemoReady $gatewayPort
    if ($readyAfterRestart.b5.ready -ne $true -or $readyAfterRestart.b2.state -ne "active") { throw "demo did not recover B2/B5 readiness after restart" }
    $interruptedValue = (& docker exec $pgContainer psql -U $pgOwner -d agentsql_demo -tA -c "SELECT balance FROM demo_tx_accounts WHERE id=3").Trim()
    if ($interruptedValue -ne "3000") { throw "B5 interrupted transaction leaked a write (got=$interruptedValue)" }

    Write-Host "[demo-reset] OK anchor=$anchor gateway=http://127.0.0.1:$gatewayPort b2_mode=$b2Mode b5_readiness=$b5Readiness"
} catch {
    Write-Error "[demo-reset] FAILED stage=$Stage`: $($_.Exception.Message)"
    exit 1
}

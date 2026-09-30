$ErrorActionPreference = 'Stop'
$port = '17880'
$roKey = 'asql_jNu0HitTukZtOj0cHe95Zg6Y0iuFQKjiEgNB7LGGRXQ'

function Send-MCP {
  param([string]$Payload,[string]$SessionID)
  for ($attempt = 0; $attempt -lt 8; $attempt++) {
    $client = New-Object System.Net.WebClient
    $client.Encoding = [Text.Encoding]::UTF8
    $client.Headers.Add('Authorization', "Bearer $roKey")
    $client.Headers.Add('Accept', 'application/json, text/event-stream')
    $client.Headers.Add('MCP-Protocol-Version', '2025-06-18')
    $client.Headers.Add('Content-Type', 'application/json; charset=utf-8')
    if (-not [string]::IsNullOrWhiteSpace($SessionID)) {
      $client.Headers.Add('Mcp-Session-Id', $SessionID)
    }
    try {
      $raw = $client.UploadString("http://127.0.0.1:$port/mcp", 'POST', $Payload)
      return [pscustomobject]@{ Body = ($raw | ConvertFrom-Json); SessionID = $client.ResponseHeaders['Mcp-Session-Id'] }
    } catch [System.Net.WebException] {
      $code = 0
      if ($_.Exception.Response) { $code = [int]$_.Exception.Response.StatusCode }
      if ($code -eq 429) { Start-Sleep -Milliseconds 700; continue }
      throw
    } finally { $client.Dispose() }
  }
  throw 'gave up after rate-limit retries'
}

function Init-Session {
  $init = [ordered]@{
    jsonrpc='2.0'; id="init-$(New-Guid)"; method='initialize'
    params=[ordered]@{ protocolVersion='2025-06-18'; capabilities=[ordered]@{}; clientInfo=[ordered]@{ name='err-probe'; version='v0.4' } }
  } | ConvertTo-Json -Depth 20 -Compress
  $r = Send-MCP -Payload $init
  if ([string]::IsNullOrWhiteSpace($r.SessionID)) { throw 'no session id' }
  return $r.SessionID
}

function Call-Query {
  param([string]$Datasource,[string]$Sql)
  $sid = Init-Session
  $body = [ordered]@{
    jsonrpc='2.0'; id="q-$(New-Guid)"; method='tools/call'
    params=[ordered]@{ name='query'; arguments=[ordered]@{ datasource_id=$Datasource; sql=$Sql } }
  } | ConvertTo-Json -Depth 20 -Compress
  $resp = Send-MCP -Payload $body -SessionID $sid
  if ($resp.Body.error) { throw "MCP error: $($resp.Body.error | ConvertTo-Json -Compress)" }
  return $resp.Body.result.structuredContent
}

function Pace { Start-Sleep -Milliseconds 650 }

$results = @()

# --- Scenario 1a: bare count(*) => deny AUTH_FUNCTION_QUALIFICATION_REQUIRED ---
Pace
$r = Call-Query -Datasource 'ds-demo-pg' -Sql 'SELECT count(*) FROM public.customers'
$results += [pscustomobject]@{
  Scenario='1a bare count(*)'; Decision=$r.decision; Reason=$r.reason; ErrorCode=$r.error_code
  Pass = ($r.decision -eq 'deny' -and $r.error_code -eq 'AUTH_FUNCTION_QUALIFICATION_REQUIRED')
}

# --- Scenario 1b: pg_catalog.count(*) => allow ---
Pace
$r = Call-Query -Datasource 'ds-demo-pg' -Sql 'SELECT pg_catalog.count(*) FROM public.customers'
$results += [pscustomobject]@{
  Scenario='1b pg_catalog.count(*)'; Decision=$r.decision; Reason=$r.reason; ErrorCode=$r.error_code
  Pass = ($r.decision -eq 'allow')
}

# --- Scenario 2: non-existent table => clear "table does not exist" error (not R010) ---
Pace
$r = Call-Query -Datasource 'ds-demo-pg' -Sql 'SELECT id FROM public.no_such_table'
$results += [pscustomobject]@{
  Scenario='2 no_such_table'; Decision=$r.decision; Reason=$r.reason; ErrorCode=$r.error_code
  Pass = ($r.decision -eq 'error' -and $r.error_code -eq 'DB_OBJECT_NOT_FOUND' -and ($r.reason + $r.suggestion) -match '不存在')
}

# --- Scenario 3: MySQL column query => deny AUTH_COLUMN_AUTH_UNSUPPORTED (not error) ---
Pace
$r = Call-Query -Datasource 'ds-demo-mysql' -Sql 'SELECT full_name FROM customers WHERE id=1'
$results += [pscustomobject]@{
  Scenario='3 MySQL column'; Decision=$r.decision; Reason=$r.reason; ErrorCode=$r.error_code
  Pass = ($r.decision -eq 'deny' -and $r.error_code -eq 'AUTH_COLUMN_AUTH_UNSUPPORTED')
}

$results | Format-Table -AutoSize -Wrap | Out-String
$failed = @($results | Where-Object { -not $_.Pass })
"=== FAILED: $($failed.Count) / $($results.Count) ==="
if ($failed.Count -gt 0) { $failed | ForEach-Object { "FAIL: $($_.Scenario) -> $($_.Decision) $($_.ErrorCode) $($_.Reason)" } }

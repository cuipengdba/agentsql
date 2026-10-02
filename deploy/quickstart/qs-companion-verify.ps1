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

function Call-Query {
  param([string]$Datasource,[string]$Sql)
  $init = [ordered]@{
    jsonrpc='2.0'; id="init-$(New-Guid)"; method='initialize'
    params=[ordered]@{ protocolVersion='2025-06-18'; capabilities=[ordered]@{}; clientInfo=[ordered]@{ name='qs-verify'; version='v0.5' } }
  } | ConvertTo-Json -Depth 20 -Compress
  $r = Send-MCP -Payload $init
  Start-Sleep -Milliseconds 650
  $body = [ordered]@{
    jsonrpc='2.0'; id="q-$(New-Guid)"; method='tools/call'
    params=[ordered]@{ name='query'; arguments=[ordered]@{ datasource_id=$Datasource; sql=$Sql } }
  } | ConvertTo-Json -Depth 20 -Compress
  $resp = Send-MCP -Payload $body -SessionID $r.SessionID
  if ($resp.Body.error) { throw "MCP error: $($resp.Body.error | ConvertTo-Json -Compress)" }
  return $resp.Body.result.structuredContent
}

$results = @()

# Sealed JOIN happy path: allow + 2 masked cells
$r = Call-Query -Datasource 'ds-demo-pg' -Sql 'SELECT c.id, c.full_name, c.phone, c.email, c.region, o.status FROM public.demo_b2_customers c JOIN public.demo_b2_orders o ON o.customer_id=c.id WHERE o.id=1'
$masked = $r.data.redact.MaskedCells
$results += [pscustomobject]@{
  Scenario='sealed JOIN'; Decision=$r.decision; Reason=$r.reason; MaskedCells=$masked
  Pass = ($r.decision -eq 'allow' -and $masked -eq 2)
}

Start-Sleep -Milliseconds 650

# Existing but unauthorized table: deny R010 / AUTH_AGENT_DENIED
$r = Call-Query -Datasource 'ds-demo-pg' -Sql 'SELECT id, note FROM public.internal_notes'
$results += [pscustomobject]@{
  Scenario='internal_notes (exists, unauthorized)'; Decision=$r.decision; Reason=$r.reason; ErrorCode=$r.error_code
  Pass = ($r.decision -eq 'deny' -and $r.error_code -eq 'AUTH_AGENT_DENIED')
}

$results | Format-Table -AutoSize -Wrap | Out-String
$failed = @($results | Where-Object { -not $_.Pass })
"=== FAILED: $($failed.Count) / $($results.Count) ==="

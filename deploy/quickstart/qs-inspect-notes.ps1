$ErrorActionPreference = 'Continue'
$port = '17880'
$roKey = 'asql_jNu0HitTukZtOj0cHe95Zg6Y0iuFQKjiEgNB7LGGRXQ'

function Send-MCP {
  param([string]$Payload,[string]$SessionID)
  $client = New-Object System.Net.WebClient
  $client.Encoding = [Text.Encoding]::UTF8
  $client.Headers.Add('Authorization', "Bearer $roKey")
  $client.Headers.Add('Accept', 'application/json, text/event-stream')
  $client.Headers.Add('MCP-Protocol-Version', '2025-06-18')
  $client.Headers.Add('Content-Type', 'application/json; charset=utf-8')
  if (-not [string]::IsNullOrWhiteSpace($SessionID)) {
    $client.Headers.Add('Mcp-Session-Id', $SessionID)
  }
  $raw = $client.UploadString("http://127.0.0.1:$port/mcp", 'POST', $Payload)
  $client.Dispose()
  return [pscustomobject]@{ Body = ($raw | ConvertFrom-Json); SessionID = $client.ResponseHeaders['Mcp-Session-Id'] }
}

$init = [ordered]@{
  jsonrpc='2.0'; id="init-$(New-Guid)"; method='initialize'
  params=[ordered]@{ protocolVersion='2025-06-18'; capabilities=[ordered]@{}; clientInfo=[ordered]@{ name='qs-inspect'; version='v0.4' } }
} | ConvertTo-Json -Depth 20 -Compress
$r = Send-MCP -Payload $init
Start-Sleep -Milliseconds 650
$body = [ordered]@{
  jsonrpc='2.0'; id="q-$(New-Guid)"; method='tools/call'
  params=[ordered]@{ name='query'; arguments=[ordered]@{ datasource_id='ds-demo-pg'; sql='SELECT id, note FROM public.internal_notes' } }
} | ConvertTo-Json -Depth 20 -Compress
$resp = Send-MCP -Payload $body -SessionID $r.SessionID
$resp.Body.result.structuredContent | ConvertTo-Json -Depth 20

$ErrorActionPreference = 'Continue'
$base = 'http://127.0.0.1:17880/mcp'
$roKey = 'asql_jNu0HitTukZtOj0cHe95Zg6Y0iuFQKjiEgNB7LGGRXQ'
$script:rid = 200

function Mcp-Curl($key, $payload, $session) {
  $tmp = [System.IO.Path]::GetTempFileName()
  [System.IO.File]::WriteAllText($tmp, $payload, (New-Object System.Text.UTF8Encoding($false)))
  $h = @('-H','MCP-Protocol-Version: 2025-06-18','-H','Accept: application/json, text/event-stream','-H','Content-Type: application/json','-H',"Authorization: Bearer $key")
  if ($session) { $h += @('-H',"Mcp-Session-Id: $session") }
  $raw = (& curl.exe -s -i -X POST $base @h --data-binary "@$tmp") -join "`n"
  Remove-Item $tmp -Force
  $sid = $null
  if ($raw -match '(?m)^Mcp-Session-Id:\s*(.+?)\s*$') { $sid = $matches[1].Trim() }
  $data = $null
  $b = [regex]::Match($raw, '(?s)\r?\n\r?\n(.+)$')
  if ($b.Success) { $data = $b.Groups[1].Value.Trim() }
  return [pscustomobject]@{ Session = $sid; Data = $data }
}

$init = @{ jsonrpc='2.0'; id=1; method='initialize'; params=@{ protocolVersion='2025-06-18'; capabilities=@{}; clientInfo=@{ name='qs'; version='1' } } } | ConvertTo-Json -Depth 10 -Compress
$r = Mcp-Curl $roKey $init $null
Start-Sleep -Milliseconds 550
$notif = @{ jsonrpc='2.0'; method='notifications/initialized'; params=@{} } | ConvertTo-Json -Compress
Mcp-Curl $roKey $notif $r.Session | Out-Null
Start-Sleep -Milliseconds 550

$script:rid++
$body = @{ jsonrpc='2.0'; id=$script:rid; method='tools/call'; params=@{ name='query'; arguments=@{ datasource_id='ds-demo-pg'; sql='SELECT id, full_name, region FROM public.customers ORDER BY id LIMIT 3' } } } | ConvertTo-Json -Depth 10 -Compress
$q = Mcp-Curl $roKey $body $r.Session
Write-Output '=== TOOL RESPONSE ==='
Write-Output $q.Data

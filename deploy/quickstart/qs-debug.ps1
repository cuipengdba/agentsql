$ErrorActionPreference = 'Continue'
$base = 'http://127.0.0.1:17880/mcp'
$key = 'asql_jNu0HitTukZtOj0cHe95Zg6Y0iuFQKjiEgNB7LGGRXQ'

function Mcp($payload, $session) {
  $tmp = [System.IO.Path]::GetTempFileName()
  [System.IO.File]::WriteAllText($tmp, $payload, (New-Object System.Text.UTF8Encoding($false)))
  $h = @('-H','MCP-Protocol-Version: 2025-06-18','-H','Accept: application/json, text/event-stream','-H','Content-Type: application/json','-H',"Authorization: Bearer $key")
  if ($session) { $h += @('-H',"Mcp-Session-Id: $session") }
  $raw = (& curl.exe -s -i -X POST $base @h --data-binary "@$tmp") -join "`n"
  Remove-Item $tmp
  $sid = $null; if ($raw -match '(?m)^Mcp-Session-Id:\s*(.+?)\s*$') { $sid = $matches[1].Trim() }
  $data = $null; $b = [regex]::Match($raw, '(?s)\r?\n\r?\n(.+)$'); if ($b.Success) { $data = $b.Groups[1].Value.Trim() }
  return [pscustomobject]@{ Session=$sid; Data=$data }
}

$init = '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}'
$r = Mcp $init $null
Start-Sleep -Milliseconds 550
Mcp '{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}' $r.Session | Out-Null
Start-Sleep -Milliseconds 550
$q = '{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"query","arguments":{"datasource_id":"ds-demo-pg","sql":"SELECT id, full_name, region FROM public.customers ORDER BY id LIMIT 3"}}}'
$resp = Mcp $q $r.Session
Write-Output '=== RAW BODY ==='
Write-Output $resp.Data

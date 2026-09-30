$ErrorActionPreference = 'Continue'
$base = 'http://127.0.0.1:17880/mcp'
$roKey = 'asql_jNu0HitTukZtOj0cHe95Zg6Y0iuFQKjiEgNB7LGGRXQ'
$dmlKey = 'asql_CXhZHv684dPV-AEpd7cYmq-os0UVRFsad8YdzctqqyE'

function Mcp-Curl($key, $json, $session) {
  $tmp = [System.IO.Path]::GetTempFileName()
  [System.IO.File]::WriteAllText($tmp, $json, (New-Object System.Text.UTF8Encoding($false)))
  $h = @(
    '-H', 'MCP-Protocol-Version: 2025-06-18'
    '-H', 'Accept: application/json, text/event-stream'
    '-H', 'Content-Type: application/json'
    '-H', "Authorization: Bearer $key"
  )
  if ($session) { $h += @('-H', "Mcp-Session-Id: $session") }
  $raw = & curl.exe -s -i -X POST $base @h --data-binary "@$tmp"
  Remove-Item $tmp -Force
  $raw = ($raw -join "`n")
  $sid = $null
  if ($raw -match '(?m)^Mcp-Session-Id:\s*(.+?)\s*$') { $sid = $matches[1].Trim() }
  # Body is direct JSON (not SSE): take everything after the header block.
  $data = $null
  $b = [regex]::Match($raw, '(?s)\r?\n\r?\n(.+)$')
  if ($b.Success) { $data = $b.Groups[1].Value.Trim() }
  return [pscustomobject]@{ Session = $sid; Data = $data }
}

function New-Session($key) {
  $init = @{
    jsonrpc='2.0'; id=1; method='initialize'
    params=@{ protocolVersion='2025-06-18'; capabilities=@{}; clientInfo=@{ name='qs-test'; version='1.0' } }
  } | ConvertTo-Json -Depth 10 -Compress
  $r = Mcp-Curl $key $init $null
  Start-Sleep -Milliseconds 600
  $notif = @{ jsonrpc='2.0'; method='notifications/initialized'; params=@{} } | ConvertTo-Json -Compress
  Mcp-Curl $key $notif $r.Session | Out-Null
  Start-Sleep -Milliseconds 600
  return $r.Session
}

$session = New-Session $roKey
Write-Output "session: $session"
$listBody = @{ jsonrpc='2.0'; id=2; method='tools/list'; params=@{} } | ConvertTo-Json -Compress
$tools = Mcp-Curl $roKey $listBody $session
Write-Output '=== TOOLS ==='
$obj = $tools.Data | ConvertFrom-Json
foreach ($t in $obj.result.tools) { Write-Output ("- " + $t.name + " :: " + $t.description) }

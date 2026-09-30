$ErrorActionPreference = 'Continue'
$base = 'http://127.0.0.1:17880/mcp'
$roKey = 'asql_jNu0HitTukZtOj0cHe95Zg6Y0iuFQKjiEgNB7LGGRXQ'
$dmlKey = 'asql_CXhZHv684dPV-AEpd7cYmq-os0UVRFsad8YdzctqqyE'
$script:rid = 100

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

function New-Session($key) {
  $init = @{ jsonrpc='2.0'; id=1; method='initialize'; params=@{ protocolVersion='2025-06-18'; capabilities=@{}; clientInfo=@{ name='qs'; version='1' } } } | ConvertTo-Json -Depth 10 -Compress
  $r = Mcp-Curl $key $init $null
  Start-Sleep -Milliseconds 550
  $notif = @{ jsonrpc='2.0'; method='notifications/initialized'; params=@{} } | ConvertTo-Json -Compress
  Mcp-Curl $key $notif $r.Session | Out-Null
  Start-Sleep -Milliseconds 550
  return $r.Session
}

function Call-Tool($key, $session, $tool, $toolArgs) {
  $script:rid++
  $body = @{ jsonrpc='2.0'; id=$script:rid; method='tools/call'; params=@{ name=$tool; arguments=$toolArgs } } | ConvertTo-Json -Depth 10 -Compress
  $r = Mcp-Curl $key $body $session
  Start-Sleep -Milliseconds 550
  if ($r.Data) {
    $obj = $r.Data | ConvertFrom-Json
    if ($obj.result.structuredContent) { return $obj.result.structuredContent }
    if ($obj.result.content) {
      $txt = $obj.result.content[0].text
      if ($txt) {
        try { return ($txt | ConvertFrom-Json) } catch { return [pscustomobject]@{ decision='(text)'; reason=$txt } }
      }
    }
    return $obj
  }
  return $null
}

function Show($title, $res, [string[]]$fields) {
  Write-Output "--- $title ---"
  if ($null -eq $res) { Write-Output '  (no response)'; return }
  foreach ($f in $fields) {
    $v = $res.$f
    if ($v -is [string] -and $v.Length -gt 160) { $v = $v.Substring(0,160) + '...' }
    Write-Output ("  {0}: {1}" -f $f, ($v -join ','))
  }
  if ($res.data) {
    $d = $res.data
    if ($d.rows) { Write-Output ("  rows: " + ($d.rows | ConvertTo-Json -Depth 6 -Compress).Substring(0,[Math]::Min(300,($d.rows|ConvertTo-Json -Depth 6 -Compress).Length))) }
    if ($d.columns) { Write-Output ("  columns: " + ($d.columns -join ', ')) }
  }
}

# RO session
$s = New-Session $roKey
Write-Output "RO session: $s"

# 1. sealed JOIN: success + masking in one query
$sealed = 'SELECT c.id, c.full_name, c.phone, c.email, c.region, o.status FROM public.demo_b2_customers c JOIN public.demo_b2_orders o ON o.customer_id=c.id WHERE o.id=1'
$r1 = Call-Tool $roKey $s 'query' @{ datasource_id='ds-demo-pg'; sql=$sealed }
Show '1 PG sealed JOIN allow + mask' $r1 @('decision','reason')
if ($r1.data) { Write-Output ("  masked_cells: " + $r1.data.redact.MaskedCells + " | row: " + ($r1.data.result.Rows[0] -join ' | ')) }

# 2. ad-hoc query on a table with only table-level allow (no column enrollment) -> deny
$r2 = Call-Tool $roKey $s 'query' @{ datasource_id='ds-demo-pg'; sql='SELECT id, name FROM public.products WHERE id=1' }
Show '2 PG ad-hoc column deny' $r2 @('decision','error_code','reason')

# 3. column-level deny (internal_notes policy deny)
$r3 = Call-Tool $roKey $s 'query' @{ datasource_id='ds-demo-pg'; sql='SELECT id, note FROM public.internal_notes LIMIT 2' }
Show '3 PG table deny' $r3 @('decision','error_code','reason')

# 4. interception: DROP TABLE
$r4 = Call-Tool $roKey $s 'explain_query' @{ datasource_id='ds-demo-pg'; sql='DROP TABLE public.customers' }
Show '4 dangerous DROP blocked' $r4 @('decision','error_code','reason')

# 5. MySQL read (column-level not supported in v0.4 -> graceful message)
$r5 = Call-Tool $roKey $s 'query' @{ datasource_id='ds-demo-mysql'; sql='SELECT id, full_name, region FROM customers ORDER BY id LIMIT 3' }
Show '5 MySQL read (unsupported notice)' $r5 @('decision','reason')

# 6. MySQL write deny (dml agent denies all mysql)
$sd = New-Session $dmlKey
$r6 = Call-Tool $dmlKey $sd 'execute_write' @{ datasource_id='ds-demo-mysql'; sql='DELETE FROM customers WHERE id=1'; reason='test cleanup' }
Show '6 MySQL DML denied' $r6 @('decision','error_code','reason')

# 7. B5 transaction via execute_write (PG demo_tx_accounts)
$r7 = Call-Tool $dmlKey $sd 'execute_write' @{ datasource_id='ds-demo-pg'; sql="UPDATE public.demo_tx_accounts SET balance=balance+10 WHERE id=1"; reason='monthly settlement batch' }
Show '7 PG B5 write (approval/tx)' $r7 @('decision','error_code','reason','suggestion')
Write-Output '=== DONE ==='

# Run the opt-in MCP/audit/timeout/cancellation matrix against an isolated
# vendor instance. Target JSON and administrator credentials stay outside Git.
[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$TargetPath,
    [Parameter(Mandatory)][ValidatePattern('^[A-Za-z0-9_.-]+$')][string]$NetworkContainer,
    [Parameter(Mandatory)][ValidateRange(1, 65535)][int]$InternalPort,
    [Parameter(Mandatory)][string]$AdminUser,
    [Parameter(Mandatory)][string]$LockSQL,
    [string]$OutputPath = 'D:\CodexLS\outputs\agentsql-v160\vendor-fault.log'
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
if (-not $env:AGENTSQL_VENDOR_ADMIN_PASSWORD) {
    throw 'Set AGENTSQL_VENDOR_ADMIN_PASSWORD before running the vendor fault test'
}
$raw = Get-Content -LiteralPath $TargetPath -Raw
$targets = @($raw | ConvertFrom-Json)
if ($targets.Count -ne 1) { throw 'TargetPath must contain exactly one isolated vendor target' }
$target = $targets[0]
$target.port = $InternalPort
$target | Add-Member -NotePropertyName admin_user -NotePropertyValue $AdminUser -Force
$target | Add-Member -NotePropertyName admin_pass -NotePropertyValue $env:AGENTSQL_VENDOR_ADMIN_PASSWORD -Force
$target | Add-Member -NotePropertyName lock_sql -NotePropertyValue $LockSQL -Force
$env:AGENTSQL_VENDOR_AUDIT_FAULT_TARGETS = ConvertTo-Json -InputObject @($target) -Depth 5 -Compress
$parent = Split-Path -Parent $OutputPath
New-Item -ItemType Directory -Force -Path $parent | Out-Null
try {
    docker run --rm --network ('container:' + $NetworkContainer) `
        --mount type=bind,source=/var/run/docker.sock,target=/var/run/docker.sock `
        --mount type=bind,source=D:\ruanjiansheji\agentsql,target=/workspace `
        --mount type=bind,source=D:\CodexLS\cache\agentsql-go-mod-linux,target=/go/pkg/mod `
        --mount type=bind,source=D:\CodexLS\cache\agentsql-go-build-linux,target=/root/.cache/go-build `
        -e TESTCONTAINERS_HOST_OVERRIDE=host.docker.internal `
        -e AGENTSQL_VENDOR_AUDIT_FAULT_TARGETS `
        -w /workspace golang:1.26-bookworm `
        go test ./internal/adminapi -run '^TestVendorIndependentAuditFaultE2E$' -count=1 -timeout 2m -v *> $OutputPath
    $code = $LASTEXITCODE
    Get-Content -LiteralPath $OutputPath -Tail 25
    if ($code -ne 0) { throw "Vendor fault test failed with exit code $code; see $OutputPath" }
} finally {
    Remove-Item Env:AGENTSQL_VENDOR_AUDIT_FAULT_TARGETS -ErrorAction SilentlyContinue
}

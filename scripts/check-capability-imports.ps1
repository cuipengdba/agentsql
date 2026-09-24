$ErrorActionPreference = 'Stop'

$module = 'github.com/cuipengdba/agentsql/'
$template = '{{.ImportPath}}|{{range .Imports}}{{.}},{{end}}'
$packages = & go list -deps -f $template ./...
if ($LASTEXITCODE -ne 0) {
    throw 'go list dependency scan failed'
}

$forbidden = @(
    'database/sql',
    'database/sql/driver',
    'github.com/go-sql-driver/mysql'
)

foreach ($line in $packages) {
    $parts = $line -split '\|', 2
    $package = $parts[0]
    if (-not $package.StartsWith($module)) {
        continue
    }
    $imports = @()
    if ($parts.Count -eq 2 -and $parts[1]) {
        $imports = $parts[1] -split ','
    }
    $importsCapability = $false
    foreach ($imported in $imports) {
        if ($forbidden -contains $imported -or $imported.StartsWith('github.com/jackc/pgx/v5')) {
            $importsCapability = $true
            break
        }
    }
    if (-not $importsCapability) {
        continue
    }
    $relative = $package.Substring($module.Length)
    $allowed = $relative -eq 'internal/authorizedexecute/internal/businessdb' -or
        $relative.StartsWith('internal/authorizedexecute/internal/businessdb/') -or
        $relative -eq 'internal/store' -or $relative.StartsWith('internal/store/') -or
        $relative -eq 'cmd/agentsqlctl' -or $relative.StartsWith('cmd/agentsqlctl/')
    if (-not $allowed) {
        throw "$package directly imports a database capability"
    }
}

Write-Output 'capability import allowlist: ok'

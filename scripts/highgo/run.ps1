param(
    [string]$Installer = 'D:\ruanjiansheji\db-images\hangao\hgdb-9.0.10.2-0-5936-59427d7-20260921.linux.x86_64.bin',
    [string]$LicenseFile = 'D:\ruanjiansheji\db-images\hangao\9147d1c2_A20261008N4540397q0lLU.dat',
    [string]$ImageTag = 'agentsql-highgo:9.0.10-b70'
)

$ErrorActionPreference = 'Stop'
& powershell.exe -NoProfile -ExecutionPolicy Bypass -File (Join-Path $PSScriptRoot 'build.ps1') `
    -Installer $Installer -LicenseFile $LicenseFile -ImageTag $ImageTag
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
& powershell.exe -NoProfile -ExecutionPolicy Bypass -File (Join-Path $PSScriptRoot 'verify.ps1') `
    -LicenseFile $LicenseFile -ImageTag $ImageTag
exit $LASTEXITCODE

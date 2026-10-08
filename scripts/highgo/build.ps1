param(
    [string]$Installer = 'D:\ruanjiansheji\db-images\hangao\hgdb-9.0.10.2-0-5936-59427d7-20260921.linux.x86_64.bin',
    [string]$LicenseFile = 'D:\ruanjiansheji\db-images\hangao\9147d1c2_A20261008N4540397q0lLU.dat',
    [string]$ImageTag = 'agentsql-highgo:9.0.10-b70'
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$transcript = 'C:\Users\Administrator\AppData\Local\Temp\highgo-batch70-transcript.txt'
Start-Transcript -LiteralPath $transcript -Append | Out-Null
$context = $PSScriptRoot
$staged = Join-Path $context 'installer.bin'
$status = 1
try {
    if (-not (Test-Path -LiteralPath $Installer -PathType Leaf)) { throw 'Installer missing' }
    if (-not (Test-Path -LiteralPath $LicenseFile -PathType Leaf)) { throw 'License missing' }
    $info = Get-Item -LiteralPath $Installer
    $hash = (Get-FileHash -LiteralPath $Installer -Algorithm SHA256).Hash
    if ($info.Length -ne 670185691 -or $hash -ne '4B13BFACC55D753D42D21613020691AB2E20B3E84D275338B80C268AEA5D4BD9') {
        throw 'Installer size/hash differs from inspected package'
    }
    Write-Output "Installer=$($info.Name) bytes=$($info.Length) sha256=$hash"
    $null = & docker image inspect rockylinux:8 --format '{{.Id}}' 2>&1
    if ($LASTEXITCODE -ne 0) { throw 'Cached rockylinux:8 image missing; no pull allowed' }
    if (Test-Path -LiteralPath $staged) { throw 'Staging path already exists' }
    New-Item -ItemType HardLink -Path $staged -Target $Installer | Out-Null
    $env:DOCKER_BUILDKIT = '1'
    $args = @('build','--network=none','--pull=false','--no-cache','--progress=plain',
        '--secret',"id=highgo_license,src=$LicenseFile",'-t',$ImageTag,
        '-f',(Join-Path $PSScriptRoot 'Dockerfile'),$context)
    $previousErrorAction = $ErrorActionPreference
    try {
        $ErrorActionPreference = 'Continue'
        & docker @args 2>&1 | ForEach-Object { Write-Output "$_" }
        $buildExit = $LASTEXITCODE
    }
    finally { $ErrorActionPreference = $previousErrorAction }
    if ($buildExit -ne 0) { throw "docker build failed: $buildExit" }
    & docker image inspect $ImageTag --format '{{.Id}} {{.Size}}'
    if ($LASTEXITCODE -ne 0) { throw 'Built image inspection failed' }
    Write-Output 'PASS: offline HighGo image built without license bytes in image'
    $status = 0
}
catch {
    Write-Output "FAIL: $($_.Exception.Message)"
}
finally {
    if (Test-Path -LiteralPath $staged) { Remove-Item -LiteralPath $staged -Force }
    Stop-Transcript | Out-Null
}
exit $status

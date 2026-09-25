[CmdletBinding()]
param(
    [ValidateSet('14','15','16','17','18')]
    [string[]]$PostgresMajor = @('14','15','16','17','18'),
    [ValidateSet('linux/amd64','linux/arm64')]
    [string[]]$Platform = @('linux/amd64'),
    [ValidateSet('deb','rpm','apk')]
    [string[]]$Format = @('deb','rpm','apk'),
    [string]$Builder = 'agentsql-binder-builder'
)

$ErrorActionPreference = 'Stop'
$repo = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path
$dist = Join-Path $repo 'dist\binder'
$alpineByMajor = @{ '14'='3.15'; '15'='3.17'; '16'='3.19'; '17'='3.21'; '18'='3.23' }
if (-not (Get-Command docker -ErrorAction SilentlyContinue)) { throw 'docker CLI is required' }

foreach ($major in $PostgresMajor) {
    foreach ($platformName in $Platform) {
        $logicalArch = ($platformName -split '/')[1]
        foreach ($formatName in $Format) {
            $package = Get-ChildItem -File (Join-Path $dist "$formatName\pg$major\$logicalArch") | Where-Object Extension -eq ".$formatName" | Select-Object -First 1
            if (-not $package) { throw "package missing: PG$major/$logicalArch/$formatName" }
            if (-not $package.FullName.StartsWith($repo + [System.IO.Path]::DirectorySeparatorChar, [System.StringComparison]::OrdinalIgnoreCase)) {
                throw "package resolved outside repository: $($package.FullName)"
            }
            $relative = $package.FullName.Substring($repo.Length + 1).Replace('\','/')
            $tag = "agentsql-binder-verify:$formatName-pg$major-$logicalArch"
            $args = @('buildx','build','--builder',$Builder,'--platform',$platformName,
                '--file','dbext/packaging/verify/Dockerfile','--target',"verify-$formatName",
                '--build-arg',"PG_MAJOR=$major",'--build-arg',"PACKAGE_FILE=$relative",'--tag',$tag,'--load')
            if ($formatName -eq 'apk') { $args += @('--build-arg',"ALPINE_VERSION=$($alpineByMajor[$major])") }
            $args += $repo
            & docker @args
            if ($LASTEXITCODE -ne 0) { throw "verification image build failed: $tag" }
            & docker run --rm --platform $platformName $tag
            if ($LASTEXITCODE -ne 0) { throw "package runtime verification failed: $tag" }
        }
    }
}

Write-Host 'All requested package install, CREATE EXTENSION, and binder self-tests passed.'

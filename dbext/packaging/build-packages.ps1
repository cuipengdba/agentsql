[CmdletBinding()]
param(
    [ValidateSet('14','15','16','17','18')]
    [string[]]$PostgresMajor = @('14','15','16','17','18'),
    [ValidateSet('linux/amd64','linux/arm64')]
    [string[]]$Platform = @('linux/amd64','linux/arm64'),
    [ValidateSet('deb','rpm','apk')]
    [string[]]$Format = @('deb','rpm','apk'),
    [string]$Builder = 'agentsql-binder-builder',
    [switch]$SkipSign
)

$ErrorActionPreference = 'Stop'
$repo = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path
$dist = Join-Path $repo 'dist\binder'
$sourceRevision = (git -C $repo rev-parse HEAD).Trim()
$alpineByMajor = @{ '14'='3.15'; '15'='3.17'; '16'='3.19'; '17'='3.21'; '18'='3.23' }

if (-not (Get-Command docker -ErrorAction SilentlyContinue)) { throw 'docker CLI is required; compilation is intentionally container-only' }
New-Item -ItemType Directory -Force -Path $dist | Out-Null
$builderNames = @(docker buildx ls --format '{{.Name}}')
if ($LASTEXITCODE -ne 0) { throw 'unable to list buildx builders' }
if ($builderNames -notcontains $Builder) { docker buildx create --name $Builder --driver docker-container --use | Out-Null }
docker buildx use $Builder
docker buildx inspect --bootstrap | Out-Null

foreach ($major in $PostgresMajor) {
    foreach ($platformName in $Platform) {
        $arch = ($platformName -split '/')[1]
        foreach ($formatName in $Format) {
            $out = Join-Path $dist "$formatName\pg$major\$arch"
            New-Item -ItemType Directory -Force -Path $out | Out-Null
            $args = @('buildx','build','--builder',$Builder,'--platform',$platformName,
                '--file','dbext/packaging/Dockerfile.packages','--target',"package-$formatName",
                '--build-arg',"PG_MAJOR=$major",'--build-arg','PACKAGE_VERSION=0.5.0',
                '--build-arg','PACKAGE_RELEASE=1','--build-arg',"SOURCE_REVISION=$sourceRevision",
                '--output',"type=local,dest=$out")
            if ($formatName -eq 'apk') { $args += @('--build-arg',"ALPINE_VERSION=$($alpineByMajor[$major])") }
            $args += $repo
            & docker @args
            if ($LASTEXITCODE -ne 0) { throw "package build failed: PG$major $platformName $formatName" }
        }
    }
}

if (-not $SkipSign) {
    docker build --file (Join-Path $repo 'dbext\packaging\signing\Dockerfile') --tag agentsql-binder-dev-signer:0.4 $repo
    if ($LASTEXITCODE -ne 0) { throw 'signing image build failed' }
    docker run --rm --mount "type=bind,source=$dist,target=/out" agentsql-binder-dev-signer:0.4 /out
    if ($LASTEXITCODE -ne 0) { throw 'artifact signing or verification failed' }
}

Write-Host "Local artifacts: $dist"
Write-Host 'No package, image, or key was uploaded. DEV ONLY signing key material was destroyed by the signer container.'

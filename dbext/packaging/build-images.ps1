[CmdletBinding()]
param(
    [ValidateSet('14','15','16','17','18')]
    [string[]]$PostgresMajor = @('14','15','16','17','18'),
    [ValidateSet('linux/amd64','linux/arm64')]
    [string[]]$Platform = @('linux/amd64','linux/arm64'),
    [string]$Builder = 'agentsql-binder-builder',
    [switch]$SkipArchive,
    [switch]$SkipSign
)

$ErrorActionPreference = 'Stop'
$repo = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path
$dist = Join-Path $repo 'dist\binder'
$imageDist = Join-Path $dist 'images'
$sourceRevision = (git -C $repo rev-parse HEAD).Trim()
if (-not (Get-Command docker -ErrorAction SilentlyContinue)) { throw 'docker CLI is required; compilation is intentionally container-only' }
New-Item -ItemType Directory -Force -Path $imageDist | Out-Null
$builderNames = @(docker buildx ls --format '{{.Name}}')
if ($LASTEXITCODE -ne 0) { throw 'unable to list buildx builders' }
if ($builderNames -notcontains $Builder) { docker buildx create --name $Builder --driver docker-container --use | Out-Null }
docker buildx use $Builder
docker buildx inspect --bootstrap | Out-Null

foreach ($major in $PostgresMajor) {
    foreach ($platformName in $Platform) {
        $arch = ($platformName -split '/')[1]
        $package = Join-Path $dist "deb\pg$major\$arch\agentsql-binder-pg$($major)_0.5.0-1_$arch.deb"
        if (-not (Test-Path -LiteralPath $package -PathType Leaf)) {
            throw "Debian package missing for derived image: $package; run build-packages first"
        }
        $tag = "agentsql/postgres-binder:$major-0.4-$arch"
        $common = @('buildx','build','--builder',$Builder,'--platform',$platformName,
            '--file','dbext/packaging/images/Dockerfile','--build-arg',"PG_MAJOR=$major",
            '--build-arg',"TARGETARCH=$arch",'--build-arg',"SOURCE_REVISION=$sourceRevision",'--tag',$tag)
        & docker @common --load $repo
        if ($LASTEXITCODE -ne 0) { throw "local image build/load failed: $tag" }
        if (-not $SkipArchive) {
            $archive = Join-Path $imageDist "agentsql-postgres-binder-$major-0.4-$arch.oci.tar"
            & docker @common --output "type=oci,dest=$archive" $repo
            if ($LASTEXITCODE -ne 0) { throw "OCI archive build failed: $tag" }
        } else {
            $archive = Join-Path $imageDist "agentsql-postgres-binder-$major-0.4-$arch.docker.tar"
            docker save --output $archive $tag
            if ($LASTEXITCODE -ne 0) { throw "temporary image archive creation failed: $tag" }
        }
        $sbomPath = Join-Path $imageDist "agentsql-postgres-binder-$major-0.4-$arch.cdx.json"
        $archiveLeaf = Split-Path -Leaf $archive
        $archiveSource = if ($SkipArchive) { "docker-archive:/out/$archiveLeaf" } else { "oci-archive:/out/$archiveLeaf" }
        $sbom = & docker run --rm --mount "type=bind,source=$imageDist,target=/out,readonly" anchore/syft:v1.33.0 $archiveSource -o cyclonedx-json
        if ($LASTEXITCODE -ne 0) { throw "image SBOM generation failed: $tag" }
        [System.IO.File]::WriteAllLines($sbomPath, $sbom, [System.Text.UTF8Encoding]::new($false))
        if ($SkipArchive) { Remove-Item -LiteralPath $archive -Force }
    }
}

if (-not $SkipSign) {
    docker build --file (Join-Path $repo 'dbext\packaging\signing\Dockerfile') --tag agentsql-binder-dev-signer:0.4 $repo
    if ($LASTEXITCODE -ne 0) { throw 'signing image build failed' }
    docker run --rm --mount "type=bind,source=$dist,target=/out" agentsql-binder-dev-signer:0.4 /out
    if ($LASTEXITCODE -ne 0) { throw 'artifact signing or verification failed' }
}

Write-Host 'Loaded only architecture-specific local tags; no registry manifest was created or pushed.'
Write-Host "OCI archives and SBOMs: $imageDist"

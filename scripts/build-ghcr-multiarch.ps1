# Build the linux/amd64 + linux/arm64 GHCR image locally as OCI archives.
# Push is opt-in. Before using -Push, the release owner must run on their device:
#   gh auth refresh -h github.com -s write:packages
[CmdletBinding()]
param(
    [ValidatePattern('^v[0-9]+\.[0-9]+\.[0-9]+$')]
    [string]$Version = 'v0.5.0',

    [string]$Builder = 'agentsql-multiarch',

    [string]$Output,

    [string]$DigestOutput,

    [switch]$IncludeAuxiliaryTags,

    [switch]$Push
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$repositoryRoot = Split-Path -Parent $PSScriptRoot
if (-not $Output) {
    $Output = Join-Path $repositoryRoot "dist/ghcr-agentsql-$Version-oci.tar"
}
elseif (-not [System.IO.Path]::IsPathRooted($Output)) {
    $Output = Join-Path $repositoryRoot $Output
}
$Output = [System.IO.Path]::GetFullPath($Output)

$outputDirectory = Split-Path -Parent $Output
if (-not $DigestOutput) {
    $DigestOutput = Join-Path $outputDirectory "ghcr-agentsql-$Version-image-digests.json"
}
elseif (-not [System.IO.Path]::IsPathRooted($DigestOutput)) {
    $DigestOutput = Join-Path $repositoryRoot $DigestOutput
}
$DigestOutput = [System.IO.Path]::GetFullPath($DigestOutput)

if (-not (Get-Command docker -ErrorAction SilentlyContinue)) {
    throw 'Docker CLI is required.'
}
docker info *> $null
if ($LASTEXITCODE -ne 0) {
    throw 'Docker daemon is unavailable or the current user cannot access it.'
}
docker buildx version *> $null
if ($LASTEXITCODE -ne 0) {
    throw 'Docker Buildx is required.'
}

docker buildx inspect $Builder *> $null
if ($LASTEXITCODE -ne 0) {
    docker buildx create --name $Builder --driver docker-container --use | Out-Host
    if ($LASTEXITCODE -ne 0) {
        throw "Could not create buildx builder '$Builder'."
    }
}
else {
    docker buildx use $Builder
    if ($LASTEXITCODE -ne 0) {
        throw "Could not select buildx builder '$Builder'."
    }
}

docker buildx inspect --bootstrap | Out-Host
if ($LASTEXITCODE -ne 0) {
    throw "Buildx builder '$Builder' failed bootstrap."
}

$image = 'ghcr.io/cuipengdba/agentsql'
$platforms = 'linux/amd64,linux/arm64'
New-Item -ItemType Directory -Force -Path $outputDirectory | Out-Null
New-Item -ItemType Directory -Force -Path (Split-Path -Parent $DigestOutput) | Out-Null

function Get-ArchiveDigest {
    param([Parameter(Mandatory = $true)][string]$Archive)

    if (-not (Get-Command tar -ErrorAction SilentlyContinue)) {
        throw "Buildx metadata did not contain a digest and tar is unavailable to inspect $Archive."
    }
    $indexText = (& tar -xOf $Archive index.json) -join "`n"
    if ($LASTEXITCODE -ne 0) {
        throw "Could not read index.json from OCI archive $Archive."
    }
    $index = $indexText | ConvertFrom-Json
    if (-not $index.manifests -or $index.manifests.Count -ne 1 -or -not $index.manifests[0].digest) {
        throw "OCI archive $Archive does not contain exactly one tagged index descriptor."
    }
    return [string]$index.manifests[0].digest
}

function Invoke-MultiArchBuild {
    param(
        [Parameter(Mandatory = $true)][string[]]$Tags,
        [string]$Archive,
        [Parameter(Mandatory = $true)][string]$MetadataFile
    )

    $arguments = @(
        'buildx', 'build',
        '--platform', $platforms,
        '--build-arg', "VERSION=$Version",
        '--metadata-file', $MetadataFile
    )
    foreach ($tag in $Tags) {
        $arguments += @('--tag', $tag)
    }
    if ($Push) {
        $arguments += '--push'
    }
    else {
        $arguments += @('--output', "type=oci,dest=$Archive")
    }
    $arguments += $repositoryRoot

    & docker @arguments
    if ($LASTEXITCODE -ne 0) {
        throw "Multi-architecture build failed for $($Tags -join ', ')."
    }

    $metadata = Get-Content -LiteralPath $MetadataFile -Raw -Encoding UTF8 | ConvertFrom-Json
    $digestProperty = $metadata.PSObject.Properties['containerimage.digest']
    $digest = if ($digestProperty) { [string]$digestProperty.Value } else { $null }
    if (-not $digest -and -not $Push) {
        $digest = Get-ArchiveDigest -Archive $Archive
    }
    if ($digest -notmatch '^sha256:[0-9a-f]{64}$') {
        throw "Build metadata for $($Tags -join ', ') did not contain a valid sha256 digest."
    }

    foreach ($tag in $Tags) {
        [pscustomobject]@{
            tag       = $tag
            digest    = $digest
            platforms = @('linux/amd64', 'linux/arm64')
            output    = if ($Push) { 'registry' } else { [System.IO.Path]::GetFileName($Archive) }
        }
    }
}

$records = @()
$mainMetadata = "$Output.metadata.json"
$mainTags = @("${image}:$Version")
if ($Push) {
    # Preserve the existing release rule: latest moves only with an explicit push.
    $mainTags += "${image}:latest"
}
$records += Invoke-MultiArchBuild -Tags $mainTags -Archive $Output -MetadataFile $mainMetadata

if ($IncludeAuxiliaryTags) {
    foreach ($suffix in @('demo', 'quickstart')) {
        $auxiliaryArchive = Join-Path $outputDirectory "ghcr-agentsql-$Version-$suffix-oci.tar"
        $auxiliaryMetadata = "$auxiliaryArchive.metadata.json"
        $records += Invoke-MultiArchBuild -Tags @("${image}:$Version-$suffix") -Archive $auxiliaryArchive -MetadataFile $auxiliaryMetadata
    }
}

$digestDocument = [ordered]@{
    schemaVersion = 1
    image         = $image
    version       = $Version
    pushed        = [bool]$Push
    records       = @($records)
}
$utf8NoBom = New-Object System.Text.UTF8Encoding($false)
$digestJson = ($digestDocument | ConvertTo-Json -Depth 8) + "`n"
[System.IO.File]::WriteAllText($DigestOutput, $digestJson, $utf8NoBom)

if ($Push) {
    if (-not (Get-Command gh -ErrorAction SilentlyContinue)) {
        throw 'The image was pushed, but GitHub CLI is required to set package visibility.'
    }
    gh api --method PATCH /user/packages/container/agentsql -f visibility=public
    if ($LASTEXITCODE -ne 0) {
        throw 'The image was pushed, but setting GHCR package visibility failed.'
    }
    Write-Host "Pushed $($mainTags -join ', '); requested public package visibility."
}
else {
    Write-Host "Wrote multi-architecture OCI archive: $Output"
}
if ($IncludeAuxiliaryTags) {
    Write-Host "Included auxiliary tags ${image}:$Version-demo and ${image}:$Version-quickstart."
}
Write-Host "Wrote image digest record: $DigestOutput"

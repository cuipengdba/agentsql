# Build the linux/amd64 + linux/arm64 GHCR image locally as an OCI archive.
# Push is opt-in. Before using -Push, the release owner must run on their device:
#   gh auth refresh -h github.com -s write:packages
[CmdletBinding()]
param(
    [ValidatePattern('^v[0-9]+\.[0-9]+\.[0-9]+$')]
    [string]$Version = 'v0.5.0',

    [string]$Builder = 'agentsql-multiarch',

    [string]$Output,

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
$buildArguments = @(
    'buildx', 'build',
    '--platform', 'linux/amd64,linux/arm64',
    '--build-arg', "VERSION=$Version",
    '--tag', "${image}:$Version"
)

if ($Push) {
    $buildArguments += @('--tag', "${image}:latest", '--push')
}
else {
    $outputDirectory = Split-Path -Parent $Output
    New-Item -ItemType Directory -Force -Path $outputDirectory | Out-Null
    $buildArguments += @('--output', "type=oci,dest=$Output")
}
$buildArguments += $repositoryRoot

& docker @buildArguments
if ($LASTEXITCODE -ne 0) {
    throw 'Multi-architecture build failed.'
}

if ($Push) {
    if (-not (Get-Command gh -ErrorAction SilentlyContinue)) {
        throw 'The image was pushed, but GitHub CLI is required to set package visibility.'
    }
    gh api --method PATCH /user/packages/container/agentsql -f visibility=public
    if ($LASTEXITCODE -ne 0) {
        throw 'The image was pushed, but setting GHCR package visibility failed.'
    }
    Write-Host "Pushed ${image}:$Version and ${image}:latest; requested public package visibility."
}
else {
    Write-Host "Wrote multi-architecture OCI archive: $Output"
}

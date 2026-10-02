# Assemble and verify the v0.4-style 15-file AgentSQL release set without
# reading a signing key, pushing an image, changing git, or calling GitHub.
[CmdletBinding()]
param(
    [ValidatePattern('^v[0-9]+\.[0-9]+\.[0-9]+$')]
    [string]$Version = 'v0.5.0',

    [string]$OutputDirectory,

    [string]$AssetsDirectory,

    [ValidatePattern('^[0-9]+$')]
    [string]$SourceDateEpoch,

    [ValidatePattern('^[^\s]+@sha256:[0-9a-f]{64}$')]
    [string]$RockyImage = 'rockylinux:8@sha256:9794037624aaa6212aeada1d28861ef5e0a935adaf93e4ef79837119f2a2d04c',

    [ValidatePattern('^[^\s]+@sha256:[0-9a-f]{64}$')]
    [string]$SyftImage = 'anchore/syft@sha256:f94e5d9fce1f2278491a8e3a63bd5f6ddb81fdfdbb8bf7a1637565c1d5344357',

    [switch]$SkipImages,

    [switch]$ValidateOnly
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$repositoryRoot = [System.IO.Path]::GetFullPath((Split-Path -Parent $PSScriptRoot))
$utf8NoBom = New-Object System.Text.UTF8Encoding($false)

function Write-Utf8File {
    param(
        [Parameter(Mandatory = $true)][string]$Path,
        [Parameter(Mandatory = $true)][AllowEmptyString()][string]$Content
    )
    [System.IO.File]::WriteAllText($Path, ($Content -replace "`r`n", "`n"), $utf8NoBom)
}

function Write-JsonFile {
    param(
        [Parameter(Mandatory = $true)][string]$Path,
        [Parameter(Mandatory = $true)]$Value,
        [int]$Depth = 12
    )
    Write-Utf8File -Path $Path -Content (($Value | ConvertTo-Json -Depth $Depth) + "`n")
}

function Get-Sha256 {
    param([Parameter(Mandatory = $true)][string]$Path)
    return (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant()
}

function Get-ExpectedAssetNames {
    param([Parameter(Mandatory = $true)][string]$ReleaseVersion)
    return @(
        "agentsql-$ReleaseVersion-linux-amd64.tar.gz",
        "agentsql-$ReleaseVersion-linux-amd64.tar.gz.sha256",
        "agentsql-$ReleaseVersion-linux-arm64.tar.gz",
        "agentsql-$ReleaseVersion-linux-arm64.tar.gz.sha256",
        "agentsql-$ReleaseVersion.spdx.json",
        "agentsql-$ReleaseVersion.spdx.json.sig.json",
        'ed25519-release-public-key.json',
        'go-version-metadata.txt',
        'install.sh',
        'provenance.json',
        'provenance.json.sig.json',
        'SBOM-GENERATION.txt',
        'SHA256SUMS',
        'SHA256SUMS.sig.json',
        'VERIFYING-SIGNATURES.md'
    )
}

function Assert-NameSet {
    param(
        [Parameter(Mandatory = $true)][string[]]$Actual,
        [Parameter(Mandatory = $true)][string[]]$Expected,
        [Parameter(Mandatory = $true)][string]$Label
    )
    $actualSorted = @($Actual | Sort-Object)
    $expectedSorted = @($Expected | Sort-Object)
    if ($actualSorted.Count -ne $expectedSorted.Count -or
        (Compare-Object -ReferenceObject $expectedSorted -DifferenceObject $actualSorted)) {
        throw "$Label name set mismatch. Expected: $($expectedSorted -join ', '); actual: $($actualSorted -join ', ')."
    }
}

function Assert-SignatureDocument {
    param(
        [Parameter(Mandatory = $true)][string]$Directory,
        [Parameter(Mandatory = $true)][string]$ArtifactName,
        [Parameter(Mandatory = $true)][string]$SignatureName,
        [Parameter(Mandatory = $true)][bool]$DryRunSet
    )
    $signature = Get-Content -LiteralPath (Join-Path $Directory $SignatureName) -Raw -Encoding UTF8 | ConvertFrom-Json
    if ([string]$signature.signedArtifact -ne $ArtifactName) {
        throw "$SignatureName names '$($signature.signedArtifact)', expected '$ArtifactName'."
    }
    $actualHash = Get-Sha256 (Join-Path $Directory $ArtifactName)
    if ([string]$signature.artifactSHA256 -ne $actualHash) {
        throw "$SignatureName artifactSHA256 does not match $ArtifactName."
    }
    if ($DryRunSet) {
        if ($signature.dryRun -ne $true -or $signature.signed -ne $false) {
            throw "$SignatureName must be an explicit unsigned dry-run placeholder."
        }
        if ($signature.PSObject.Properties['signatureBase64']) {
            throw "$SignatureName dry-run placeholder must not contain signatureBase64."
        }
    }
    else {
        if ([string]$signature.algorithm -ne 'Ed25519' -or [string]$signature.signatureBase64 -eq '') {
            throw "$SignatureName is not a structurally valid Ed25519 signature document."
        }
    }
}

function Assert-NoYashanRedistribution {
    param(
        [Parameter(Mandatory = $true)][string]$Directory,
        [Parameter(Mandatory = $true)][string]$ReleaseVersion
    )

    $metadataPath = Join-Path $Directory 'go-version-metadata.txt'
    if (Select-String -LiteralPath $metadataPath -SimpleMatch 'github.com/yashan-technologies/yashandb-go' -Quiet) {
        throw 'go-version-metadata.txt includes the optional YashanDB Go driver; official assets must exclude it.'
    }

    foreach ($architecture in @('amd64', 'arm64')) {
        $tarName = "agentsql-$ReleaseVersion-linux-$architecture.tar.gz"
        $tarPath = Join-Path $Directory $tarName
        $entries = @(& tar -tzf $tarPath)
        if ($LASTEXITCODE -ne 0) {
            throw "Could not inspect archive entries in $tarName."
        }
        $forbidden = @($entries | Where-Object { $_ -match '(?i)(^|/)libyas(cli|_infra)(\.so|\.so\.|\.a$|\.dylib$|\.dll$)' })
        if ($forbidden.Count -gt 0) {
            throw "$tarName contains YashanDB client libraries: $($forbidden -join ', ')."
        }
    }
    Write-Host 'RELEASE_DRYRUN_YASHAN_REDISTRIBUTION=EXCLUDED'
}

function Test-ReleaseAssets {
    param(
        [Parameter(Mandatory = $true)][string]$Directory,
        [Parameter(Mandatory = $true)][string]$ReleaseVersion
    )
    if (-not (Test-Path -LiteralPath $Directory -PathType Container)) {
        throw "Asset directory does not exist: $Directory"
    }
    $expected = @(Get-ExpectedAssetNames $ReleaseVersion)
    $actualFiles = @(Get-ChildItem -LiteralPath $Directory -File)
    Assert-NameSet -Actual @($actualFiles.Name) -Expected $expected -Label 'Release asset'

    foreach ($architecture in @('amd64', 'arm64')) {
        $tarName = "agentsql-$ReleaseVersion-linux-$architecture.tar.gz"
        $sidecarName = "$tarName.sha256"
        $sidecar = (Get-Content -LiteralPath (Join-Path $Directory $sidecarName) -Raw -Encoding UTF8).Trim()
        if ($sidecar -notmatch '^([0-9a-fA-F]{64})  ([^\\/]+)$') {
            throw "$sidecarName is malformed."
        }
        if ($Matches[2] -ne $tarName -or $Matches[1].ToLowerInvariant() -ne (Get-Sha256 (Join-Path $Directory $tarName))) {
            throw "$sidecarName does not match $tarName."
        }
    }

    $manifestExpected = @($expected | Where-Object { $_ -notin @('SHA256SUMS', 'SHA256SUMS.sig.json') })
    $manifestNames = @()
    $manifestPath = Join-Path $Directory 'SHA256SUMS'
    foreach ($line in @(Get-Content -LiteralPath $manifestPath -Encoding UTF8)) {
        if ($line -notmatch '^([0-9a-f]{64})  ([^\\/]+)$') {
            throw "Malformed SHA256SUMS line: $line"
        }
        $name = $Matches[2]
        if ($manifestNames -contains $name) {
            throw "SHA256SUMS contains duplicate entry: $name"
        }
        $manifestNames += $name
        $actualHash = Get-Sha256 (Join-Path $Directory $name)
        if ($Matches[1] -ne $actualHash) {
            throw "SHA256SUMS mismatch for $name."
        }
    }
    Assert-NameSet -Actual $manifestNames -Expected $manifestExpected -Label 'SHA256SUMS'
    Assert-NoYashanRedistribution -Directory $Directory -ReleaseVersion $ReleaseVersion

    $publicDocument = Get-Content -LiteralPath (Join-Path $Directory 'ed25519-release-public-key.json') -Raw -Encoding UTF8 | ConvertFrom-Json
    $isDryRunSet = $publicDocument.PSObject.Properties['dryRun'] -and $publicDocument.dryRun -eq $true
    if ($isDryRunSet) {
        if ($publicDocument.signed -ne $false -or $publicDocument.PSObject.Properties['publicKeyBase64']) {
            throw 'Dry-run public-key placeholder must be unsigned and must not contain publicKeyBase64.'
        }
    }
    else {
        if ([string]$publicDocument.algorithm -ne 'Ed25519' -or [string]$publicDocument.publicKeyBase64 -eq '') {
            throw 'Release public-key document is malformed.'
        }
    }

    Assert-SignatureDocument -Directory $Directory -ArtifactName "agentsql-$ReleaseVersion.spdx.json" -SignatureName "agentsql-$ReleaseVersion.spdx.json.sig.json" -DryRunSet $isDryRunSet
    Assert-SignatureDocument -Directory $Directory -ArtifactName 'provenance.json' -SignatureName 'provenance.json.sig.json' -DryRunSet $isDryRunSet
    Assert-SignatureDocument -Directory $Directory -ArtifactName 'SHA256SUMS' -SignatureName 'SHA256SUMS.sig.json' -DryRunSet $isDryRunSet

    $report = foreach ($name in $expected) {
        $file = Get-Item -LiteralPath (Join-Path $Directory $name)
        [pscustomobject]@{
            Name   = $name
            Bytes  = $file.Length
            SHA256 = Get-Sha256 $file.FullName
        }
    }
    $report | Format-Table -AutoSize | Out-Host
    Write-Host "RELEASE_DRYRUN_ASSET_COUNT=$($report.Count)"
    Write-Host 'RELEASE_DRYRUN_SHA256SUMS=PASS'
    Write-Host "RELEASE_DRYRUN_SIGNATURE_MODE=$(if ($isDryRunSet) { 'UNSIGNED_PLACEHOLDERS' } else { 'SIGNED_STRUCTURE_ONLY' })"
    return $report
}

if ($ValidateOnly) {
    if (-not $AssetsDirectory) {
        throw '-ValidateOnly requires -AssetsDirectory.'
    }
    if (-not [System.IO.Path]::IsPathRooted($AssetsDirectory)) {
        $AssetsDirectory = Join-Path $repositoryRoot $AssetsDirectory
    }
    $AssetsDirectory = [System.IO.Path]::GetFullPath($AssetsDirectory)
    Test-ReleaseAssets -Directory $AssetsDirectory -ReleaseVersion $Version | Out-Null
    Write-Host 'RELEASE_DRYRUN_VALIDATE_ONLY=PASS'
    exit 0
}

if ($AssetsDirectory) {
    throw '-AssetsDirectory is valid only with -ValidateOnly.'
}
if (-not $OutputDirectory) {
    $OutputDirectory = Join-Path $repositoryRoot "dist/release-dryrun/$Version"
}
elseif (-not [System.IO.Path]::IsPathRooted($OutputDirectory)) {
    $OutputDirectory = Join-Path $repositoryRoot $OutputDirectory
}
$OutputDirectory = [System.IO.Path]::GetFullPath($OutputDirectory)
$repositoryPrefix = $repositoryRoot.TrimEnd([System.IO.Path]::DirectorySeparatorChar) + [System.IO.Path]::DirectorySeparatorChar
if (-not $OutputDirectory.StartsWith($repositoryPrefix, [System.StringComparison]::OrdinalIgnoreCase)) {
    throw 'OutputDirectory must remain inside the repository.'
}
if (Test-Path -LiteralPath $OutputDirectory) {
    throw "OutputDirectory already exists; refusing to overwrite it: $OutputDirectory"
}

if (-not $SourceDateEpoch) {
    $SourceDateEpoch = (& git -C $repositoryRoot show -s --format=%ct HEAD).Trim()
    if ($LASTEXITCODE -ne 0 -or $SourceDateEpoch -notmatch '^[0-9]+$') {
        throw 'Could not derive SOURCE_DATE_EPOCH from HEAD.'
    }
}

if (-not (Get-Command docker -ErrorAction SilentlyContinue)) {
    throw 'Docker CLI is required for a full release dry-run.'
}
$dockerConfig = Join-Path $repositoryRoot '.docker-config'
New-Item -ItemType Directory -Force -Path $dockerConfig | Out-Null
$env:DOCKER_CONFIG = $dockerConfig
docker info *> $null
if ($LASTEXITCODE -ne 0) {
    throw 'Docker daemon is unavailable or the current user cannot access it.'
}

$assetsPath = Join-Path $OutputDirectory 'assets'
$evidencePath = Join-Path $OutputDirectory 'evidence'
New-Item -ItemType Directory -Path $assetsPath -Force | Out-Null
New-Item -ItemType Directory -Path $evidencePath -Force | Out-Null

$releaseDnf = 'dnf -y install --nodocs --setopt=install_weak_deps=False gcc glibc-devel glibc-common curl binutils file findutils gawk tar gzip grep sed'
foreach ($architecture in @('amd64', 'arm64')) {
    Write-Host "==> Building and packaging linux/$architecture with $RockyImage"
    $containerCommand = "$releaseDnf && tr -d '\r' < /buildscripts/build-release-linux.sh | sh && tr -d '\r' < /buildscripts/package-release.sh | sh && /usr/local/go/bin/go version -m /src/bin/agentsql-linux-$architecture /src/bin/agentsqlctl-linux-$architecture > /src/dist/go-version-metadata-$architecture.txt"
    $dockerArguments = @(
        'run', '--rm', '--platform', "linux/$architecture",
        '-e', "ARCH=$architecture",
        '-e', "VERSION=$Version",
        '-e', 'GO_VERSION=1.25.14',
        '-e', 'GOPROXY=https://goproxy.cn,direct',
        '-e', "SOURCE_DATE_EPOCH=$SourceDateEpoch",
        '-v', "${repositoryRoot}:/src",
        '-w', '/src',
        '-v', "${PSScriptRoot}:/buildscripts:ro",
        $RockyImage, 'sh', '-c', $containerCommand
    )
    & docker @dockerArguments
    if ($LASTEXITCODE -ne 0) {
        throw "linux/$architecture release build failed."
    }
}

foreach ($architecture in @('amd64', 'arm64')) {
    foreach ($suffix in @('.tar.gz', '.tar.gz.sha256')) {
        $name = "agentsql-$Version-linux-$architecture$suffix"
        $source = Join-Path $repositoryRoot "dist/$name"
        if (-not (Test-Path -LiteralPath $source -PathType Leaf)) {
            throw "Expected package output is missing: $source"
        }
        Copy-Item -LiteralPath $source -Destination (Join-Path $assetsPath $name)
    }
}
Copy-Item -LiteralPath (Join-Path $repositoryRoot 'scripts/install.sh') -Destination (Join-Path $assetsPath 'install.sh')

$metadataParts = foreach ($architecture in @('amd64', 'arm64')) {
    $metadataPath = Join-Path $repositoryRoot "dist/go-version-metadata-$architecture.txt"
    if (-not (Test-Path -LiteralPath $metadataPath -PathType Leaf)) {
        throw "Go metadata output is missing: $metadataPath"
    }
    (Get-Content -LiteralPath $metadataPath -Raw -Encoding UTF8).TrimEnd()
}
Write-Utf8File -Path (Join-Path $assetsPath 'go-version-metadata.txt') -Content (($metadataParts -join "`n") + "`n")

$scanInput = Join-Path $evidencePath 'sbom-input'
New-Item -ItemType Directory -Path $scanInput -Force | Out-Null
Copy-Item -LiteralPath (Join-Path $repositoryRoot 'go.mod') -Destination $scanInput
Copy-Item -LiteralPath (Join-Path $repositoryRoot 'go.sum') -Destination $scanInput
Copy-Item -LiteralPath (Join-Path $repositoryRoot 'web/package.json') -Destination $scanInput
Copy-Item -LiteralPath (Join-Path $repositoryRoot 'web/package-lock.json') -Destination $scanInput
foreach ($architecture in @('amd64', 'arm64')) {
    Copy-Item -LiteralPath (Join-Path $repositoryRoot "bin/agentsql-linux-$architecture") -Destination $scanInput
    Copy-Item -LiteralPath (Join-Path $repositoryRoot "bin/agentsqlctl-linux-$architecture") -Destination $scanInput
}

$sbomName = "agentsql-$Version.spdx.json"
Write-Host "==> Generating SPDX SBOM with $SyftImage"
& docker run --rm -v "${scanInput}:/scan:ro" -v "${assetsPath}:/out" $SyftImage "dir:/scan" --source-name agentsql --source-version $Version '--enrich=[]' -o "spdx-json=/out/$sbomName"
if ($LASTEXITCODE -ne 0) {
    throw 'Syft SBOM generation failed.'
}
$sbomPath = Join-Path $assetsPath $sbomName
$sbom = Get-Content -LiteralPath $sbomPath -Raw -Encoding UTF8 | ConvertFrom-Json
$createdAt = [DateTimeOffset]::FromUnixTimeSeconds([long]$SourceDateEpoch).UtcDateTime.ToString('yyyy-MM-ddTHH:mm:ssZ')
$sbom.creationInfo.created = $createdAt
$sbom.documentNamespace = "https://github.com/cuipengdba/agentsql/releases/dry-run/$Version/$SourceDateEpoch"
Write-JsonFile -Path $sbomPath -Value $sbom -Depth 100

$packageCount = @($sbom.packages).Count
$relationshipCount = @($sbom.relationships).Count
$sbomInstructions = @"
AgentSQL $Version dry-run SBOM generation
========================================

Format: SPDX 2.3 JSON
Generator image: $SyftImage
Output: $sbomName
SOURCE_DATE_EPOCH: $SourceDateEpoch

The offline scan input contains go.mod, go.sum, web/package.json,
web/package-lock.json, and both AgentSQL Linux binaries for amd64 and arm64.
Online enrichment is disabled. This dry-run found $packageCount packages and
$relationshipCount relationships. Release day must rerun the pinned generator
against the sealed source and binaries before production signing.
"@
Write-Utf8File -Path (Join-Path $assetsPath 'SBOM-GENERATION.txt') -Content $sbomInstructions

$commit = (& git -C $repositoryRoot rev-parse HEAD).Trim()
$branch = (& git -C $repositoryRoot branch --show-current).Trim()
if ($LASTEXITCODE -ne 0 -or $commit -notmatch '^[0-9a-f]{40}$') {
    throw 'Could not read git source identity.'
}
$dirtyOutput = @(& git -C $repositoryRoot status --porcelain --untracked-files=no)
$inputNames = @('go.mod', 'go.sum', 'web/package.json', 'web/package-lock.json', 'Dockerfile', 'scripts/build-release-linux.sh', 'scripts/package-release.sh', 'scripts/install.sh', 'scripts/release-dryrun.ps1', 'scripts/build-ghcr-multiarch.ps1', 'Makefile')
$inputs = foreach ($name in $inputNames) {
    [ordered]@{ name = $name; sha256 = Get-Sha256 (Join-Path $repositoryRoot $name) }
}
$subjectNames = @(
    "agentsql-$Version-linux-amd64.tar.gz",
    "agentsql-$Version-linux-amd64.tar.gz.sha256",
    "agentsql-$Version-linux-arm64.tar.gz",
    "agentsql-$Version-linux-arm64.tar.gz.sha256",
    'install.sh', $sbomName, 'go-version-metadata.txt', 'SBOM-GENERATION.txt'
)
$subjects = foreach ($name in $subjectNames) {
    $file = Get-Item -LiteralPath (Join-Path $assetsPath $name)
    [ordered]@{ name = $name; size = $file.Length; digest = [ordered]@{ sha256 = Get-Sha256 $file.FullName } }
}
$provenance = [ordered]@{
    schemaVersion = 1
    documentType  = 'AgentSQL release dry-run provenance'
    version       = $Version
    dryRun        = $true
    releasable    = $false
    notice        = 'DRY RUN ONLY: unsigned, not for upload or publication.'
    source        = [ordered]@{
        repository      = 'https://github.com/cuipengdba/agentsql'
        branch          = $branch
        commit          = $commit
        sourceDateEpoch = [long]$SourceDateEpoch
        dirtyAtBuild    = ($dirtyOutput.Count -gt 0)
    }
    subjects      = @($subjects)
    inputs        = @($inputs)
    builder       = [ordered]@{
        kind       = 'local Docker dry-run'
        rockyImage = $RockyImage
        go         = '1.25.14'
        syftImage  = $SyftImage
        cgo        = $true
    }
    commands      = @(
        'build-release-linux.sh + package-release.sh (linux/amd64)',
        'build-release-linux.sh + package-release.sh (linux/arm64)',
        'syft dir:/scan --enrich=[] -o spdx-json'
    )
    signing       = [ordered]@{ status = 'not-performed'; reason = 'dry-run is forbidden from accessing a signing private key' }
    generatedAt   = $createdAt
}
Write-JsonFile -Path (Join-Path $assetsPath 'provenance.json') -Value $provenance -Depth 30

$publicPlaceholder = [ordered]@{
    schemaVersion = 1
    dryRun        = $true
    signed        = $false
    releasable    = $false
    artifact      = 'ed25519-release-public-key.json'
    notice        = 'DRY RUN ONLY: production public key export is a release-day manual gate.'
}
Write-JsonFile -Path (Join-Path $assetsPath 'ed25519-release-public-key.json') -Value $publicPlaceholder

function Write-UnsignedPlaceholder {
    param(
        [Parameter(Mandatory = $true)][string]$ArtifactName,
        [Parameter(Mandatory = $true)][string]$SignatureName
    )
    $placeholder = [ordered]@{
        schemaVersion  = 1
        dryRun        = $true
        signed        = $false
        releasable    = $false
        signedArtifact = $ArtifactName
        artifactSHA256 = Get-Sha256 (Join-Path $assetsPath $ArtifactName)
        notice         = 'DRY RUN ONLY: replace with a production Ed25519 signature and verify before release.'
    }
    Write-JsonFile -Path (Join-Path $assetsPath $SignatureName) -Value $placeholder
}

Write-UnsignedPlaceholder -ArtifactName $sbomName -SignatureName "$sbomName.sig.json"
Write-UnsignedPlaceholder -ArtifactName 'provenance.json' -SignatureName 'provenance.json.sig.json'

$verificationInstructions = @"
# Release signature verification ($Version dry-run)

This directory was produced by `scripts/release-dryrun.ps1`. It intentionally
contains unsigned placeholders and is **NOT FOR RELEASE**. The dry-run never
reads a private signing key.

On release day, an authorized maintainer must replace the public-key placeholder
and all three `*.sig.json` placeholders using the production Ed25519 process,
then verify the raw bytes of the SPDX SBOM, `provenance.json`, and `SHA256SUMS`
with `go run ./scripts/releasesign/main.go -mode verify`. Only verified files
may be uploaded to a GitHub Release.
"@
Write-Utf8File -Path (Join-Path $assetsPath 'VERIFYING-SIGNATURES.md') -Content $verificationInstructions

$expectedNames = @(Get-ExpectedAssetNames $Version)
$manifestNames = @($expectedNames | Where-Object { $_ -notin @('SHA256SUMS', 'SHA256SUMS.sig.json') } | Sort-Object)
$manifestLines = foreach ($name in $manifestNames) {
    "$(Get-Sha256 (Join-Path $assetsPath $name))  $name"
}
Write-Utf8File -Path (Join-Path $assetsPath 'SHA256SUMS') -Content (($manifestLines -join "`n") + "`n")
Write-UnsignedPlaceholder -ArtifactName 'SHA256SUMS' -SignatureName 'SHA256SUMS.sig.json'

Test-ReleaseAssets -Directory $assetsPath -ReleaseVersion $Version | Out-Null

if ($SkipImages) {
    Write-Utf8File -Path (Join-Path $evidencePath 'image-build-skipped.txt') -Content "Image build skipped explicitly; all GHCR build/push/public/anonymous-pull gates remain pending release day.`n"
    Write-Host 'RELEASE_DRYRUN_IMAGES=SKIPPED_BY_REQUEST'
}
else {
    $imageEvidence = Join-Path $evidencePath 'images'
    New-Item -ItemType Directory -Path $imageEvidence -Force | Out-Null
    $mainArchive = Join-Path $imageEvidence "ghcr-agentsql-$Version-oci.tar"
    $digestOutput = Join-Path $imageEvidence "ghcr-agentsql-$Version-image-digests.json"
    & (Join-Path $PSScriptRoot 'build-ghcr-multiarch.ps1') -Version $Version -Output $mainArchive -DigestOutput $digestOutput -IncludeAuxiliaryTags
    if ($LASTEXITCODE -ne 0) {
        throw 'Local multi-architecture image dry-run failed.'
    }
    Write-Host 'RELEASE_DRYRUN_IMAGES=PASS'
}

Write-Host "RELEASE_DRYRUN_OUTPUT=$OutputDirectory"
Write-Host 'RELEASE_DRYRUN_RESULT=PASS_UNSIGNED_NOT_FOR_RELEASE'

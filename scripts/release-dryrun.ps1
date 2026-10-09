# Assemble and verify the 15-file AgentSQL dry-run set, or prepare
# the 11 unsigned production candidates, without reading a private signing key,
# pushing an image, changing git, or calling GitHub.
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

    [switch]$ProductionPrepare,

    [string]$PublicKeyPath,

    [switch]$ValidateOnly
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$repositoryRoot = [System.IO.Path]::GetFullPath((Split-Path -Parent $PSScriptRoot))
$utf8NoBom = New-Object System.Text.UTF8Encoding($false)
$assetChecksReported = $false
trap {
    $reason = $_.Exception.Message
    if (-not $ProductionPrepare -and -not $script:assetChecksReported) {
        Write-Host 'RELEASE_DRYRUN_CHECKS_BEGIN'
        foreach ($name in @(Get-ExpectedAssetNames $Version)) {
            Write-Host "ASSET $name | FAIL | assembly or validation did not complete: $reason"
        }
        Write-Host 'RELEASE_DRYRUN_CHECKS_END'
        Write-Host 'RELEASE_DRYRUN_ITEM_PASS_COUNT=0'
        Write-Host 'RELEASE_DRYRUN_ITEM_FAIL_COUNT=15'
    }
    if ($ProductionPrepare) {
        Write-Host "RELEASE_PREPARE_ERROR=$reason"
        Write-Host 'RELEASE_PREPARE_RESULT=FAIL'
    }
    else {
        Write-Host "RELEASE_DRYRUN_ERROR=$reason"
        Write-Host 'RELEASE_DRYRUN_RESULT=FAIL'
    }
    exit 1
}

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

function Show-AssetInventory {
    param(
        [Parameter(Mandatory = $true)][string]$Directory,
        [Parameter(Mandatory = $true)][string]$ReleaseVersion
    )
    Write-Host 'RELEASE_DRYRUN_ASSETS_BEGIN'
    foreach ($name in @(Get-ExpectedAssetNames $ReleaseVersion)) {
        $path = Join-Path $Directory $name
        if (Test-Path -LiteralPath $path -PathType Leaf) {
            $item = Get-Item -LiteralPath $path
            Write-Host "$name | $($item.Length) bytes | $(Get-Sha256 $path) | PRESENT"
        }
        else {
            Write-Host "$name | - | - | MISSING"
        }
    }
    Write-Host 'RELEASE_DRYRUN_ASSETS_END'
}

function Show-AssetValidation {
    param(
        [Parameter(Mandatory = $true)][string]$Directory,
        [Parameter(Mandatory = $true)][string]$ReleaseVersion,
        [Parameter(Mandatory = $true)][string]$ExpectedCommit
    )
    $expected = @(Get-ExpectedAssetNames $ReleaseVersion)
    $failures = @()
    $script:assetChecksReported = $true
    Write-Host 'RELEASE_DRYRUN_CHECKS_BEGIN'
    foreach ($name in $expected) {
        $path = Join-Path $Directory $name
        try {
            if (-not (Test-Path -LiteralPath $path -PathType Leaf)) {
                throw 'missing file'
            }
            $item = Get-Item -LiteralPath $path
            if ($item.Length -le 0) { throw 'empty file' }
            $hash = Get-Sha256 $path
            switch -Regex ($name) {
                '^agentsql-.*-linux-(amd64|arm64)\.tar\.gz$' {
                    $architecture = $Matches[1]
                    $rootName = "agentsql-$ReleaseVersion-linux-$architecture"
                    $entries = @(& tar -tzf $path)
                    if ($LASTEXITCODE -ne 0) { throw 'tar archive cannot be read' }
                    foreach ($library in @('libyascli.so', 'libyas_infra.so')) {
                        $libraryPath = "$rootName/lib/yashandb/$library"
                        if ($entries -cnotcontains $libraryPath) { throw "missing $libraryPath" }
                        Write-Host "YASHAN_LIBRARY $name $libraryPath | PASS"
                    }
                    if ($entries -cnotcontains "$rootName/SHA256SUMS") { throw 'missing internal SHA256SUMS' }
                    break
                }
                '^agentsql-.*-linux-(amd64|arm64)\.tar\.gz\.sha256$' {
                    $tarName = $name.Substring(0, $name.Length - '.sha256'.Length)
                    $sidecar = (Get-Content -LiteralPath $path -Raw -Encoding UTF8).Trim()
                    if ($sidecar -cnotmatch '^([0-9a-f]{64})  ([^\\/]+)$') { throw 'malformed sidecar' }
                    if ($Matches[2] -cne $tarName -or $Matches[1] -cne (Get-Sha256 (Join-Path $Directory $tarName))) {
                        throw "checksum does not match $tarName"
                    }
                    break
                }
                '^agentsql-.*\.spdx\.json$' {
                    $sbom = Get-Content -LiteralPath $path -Raw -Encoding UTF8 | ConvertFrom-Json
                    if ([string]$sbom.spdxVersion -ne 'SPDX-2.3' -or [string]$sbom.documentNamespace -notmatch [regex]::Escape($ReleaseVersion)) {
                        throw 'SPDX version or document namespace mismatch'
                    }
                    break
                }
                '^agentsql-.*\.spdx\.json\.sig\.json$' {
                    $signature = Get-Content -LiteralPath $path -Raw -Encoding UTF8 | ConvertFrom-Json
                    $isPlaceholder = $signature.PSObject.Properties['dryRun'] -and $signature.dryRun -eq $true
                    Assert-SignatureDocument -Directory $Directory -ArtifactName "agentsql-$ReleaseVersion.spdx.json" -SignatureName $name -DryRunSet $isPlaceholder
                    break
                }
                '^provenance\.json\.sig\.json$|^SHA256SUMS\.sig\.json$' {
                    $signature = Get-Content -LiteralPath $path -Raw -Encoding UTF8 | ConvertFrom-Json
                    $artifact = $name.Substring(0, $name.Length - '.sig.json'.Length)
                    $isPlaceholder = $signature.PSObject.Properties['dryRun'] -and $signature.dryRun -eq $true
                    Assert-SignatureDocument -Directory $Directory -ArtifactName $artifact -SignatureName $name -DryRunSet $isPlaceholder
                    break
                }
                '^ed25519-release-public-key\.json$' {
                    $public = Get-Content -LiteralPath $path -Raw -Encoding UTF8 | ConvertFrom-Json
                    if ($public.PSObject.Properties['dryRun'] -and $public.dryRun -eq $true) {
                        if ($public.signed -ne $false -or $public.PSObject.Properties['publicKeyBase64']) { throw 'invalid unsigned public-key placeholder' }
                    }
                    elseif ([string]$public.algorithm -ne 'Ed25519' -or [string]$public.publicKeyBase64 -eq '') {
                        throw 'malformed release public key'
                    }
                    break
                }
                '^go-version-metadata\.txt$' {
                    $metadata = Get-Content -LiteralPath $path -Raw -Encoding UTF8
                    if ($metadata -notmatch 'github\.com/yashan-technologies/yashandb-go\s+v1\.4\.4') { throw 'missing yashandb-go v1.4.4' }
                    foreach ($architecture in @('amd64', 'arm64')) {
                        if ($metadata -notmatch [regex]::Escape("agentsql-linux-$architecture")) { throw "missing linux/$architecture metadata" }
                    }
                    break
                }
                '^install\.sh$' {
                    if ($hash -cne (Get-Sha256 (Join-Path $repositoryRoot 'scripts/install.sh'))) { throw 'does not match scripts/install.sh' }
                    break
                }
                '^provenance\.json$' {
                    $provenance = Get-Content -LiteralPath $path -Raw -Encoding UTF8 | ConvertFrom-Json
                    if ([string]$provenance.version -cne $ReleaseVersion -or [string]$provenance.source.commit -cne $ExpectedCommit) {
                        throw 'version or source commit mismatch'
                    }
                    break
                }
                '^SBOM-GENERATION\.txt$' {
                    $instructions = Get-Content -LiteralPath $path -Raw -Encoding UTF8
                    if ($instructions -notmatch 'SPDX 2\.3 JSON' -or $instructions -notmatch 'SOURCE_DATE_EPOCH: [0-9]+') {
                        throw 'missing SPDX or source epoch marker'
                    }
                    break
                }
                '^SHA256SUMS$' {
                    $manifestNames = @()
                    foreach ($line in @(Get-Content -LiteralPath $path -Encoding UTF8)) {
                        if ($line -cnotmatch '^([0-9a-f]{64})  ([^\\/]+)$') { throw "malformed manifest line: $line" }
                        $manifestHash = $Matches[1]
                        $manifestName = $Matches[2]
                        if ($manifestNames -ccontains $manifestName) { throw "duplicate manifest entry: $manifestName" }
                        $manifestNames += $manifestName
                        if ($manifestHash -cne (Get-Sha256 (Join-Path $Directory $manifestName))) { throw "checksum mismatch: $manifestName" }
                    }
                    $manifestExpected = @($expected | Where-Object { $_ -notin @('SHA256SUMS', 'SHA256SUMS.sig.json') })
                    Assert-NameSet -Actual $manifestNames -Expected $manifestExpected -Label 'SHA256SUMS'
                    break
                }
                '^VERIFYING-SIGNATURES\.md$' {
                    $instructions = Get-Content -LiteralPath $path -Raw -Encoding UTF8
                    if ($instructions -notmatch [regex]::Escape($ReleaseVersion) -or $instructions -notmatch 'SHA256SUMS') {
                        throw 'missing release version or manifest verification instructions'
                    }
                    break
                }
                default { throw "no validation rule for expected asset $name" }
            }
            Write-Host "ASSET $name | $($item.Length) bytes | $hash | PASS"
        }
        catch {
            $reason = $_.Exception.Message
            $failures += "$name`: $reason"
            Write-Host "ASSET $name | FAIL | $reason"
        }
    }
    Write-Host 'RELEASE_DRYRUN_CHECKS_END'
    Write-Host "RELEASE_DRYRUN_ITEM_PASS_COUNT=$($expected.Count - $failures.Count)"
    Write-Host "RELEASE_DRYRUN_ITEM_FAIL_COUNT=$($failures.Count)"
    if ($failures.Count -gt 0) { throw "Asset validation failed: $($failures -join '; ')" }
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

function Assert-YashanRedistribution {
    param(
        [Parameter(Mandatory = $true)][string]$Directory,
        [Parameter(Mandatory = $true)][string]$ReleaseVersion
    )

    $metadataPath = Join-Path $Directory 'go-version-metadata.txt'
    if (-not (Select-String -LiteralPath $metadataPath -Pattern 'github\.com/yashan-technologies/yashandb-go\s+v1\.4\.4' -Quiet)) {
        throw 'go-version-metadata.txt does not include yashandb-go v1.4.4.'
    }

    foreach ($architecture in @('amd64', 'arm64')) {
        $tarName = "agentsql-$ReleaseVersion-linux-$architecture.tar.gz"
        $tarPath = Join-Path $Directory $tarName
        $entries = @(& tar -tzf $tarPath)
        if ($LASTEXITCODE -ne 0) {
            throw "Could not inspect archive entries in $tarName."
        }
        foreach ($requiredLibrary in @('libyascli.so', 'libyas_infra.so')) {
            $expectedSuffix = "/lib/yashandb/$requiredLibrary"
            if (-not @($entries | Where-Object { $_.EndsWith($expectedSuffix, [System.StringComparison]::Ordinal) })) {
                throw "$tarName is missing the authorized YashanDB client library $requiredLibrary."
            }
        }
    }
    Write-Host 'RELEASE_DRYRUN_YASHAN_REDISTRIBUTION=INCLUDED_DRIVER_v1.4.4_CLIENT_23.4.7.100'
}

function Test-ReleaseAssets {
    param(
        [Parameter(Mandatory = $true)][string]$Directory,
        [Parameter(Mandatory = $true)][string]$ReleaseVersion,
        [Parameter(Mandatory = $true)][ValidatePattern('^[0-9a-f]{40}$')][string]$ExpectedCommit
    )
    if (-not (Test-Path -LiteralPath $Directory -PathType Container)) {
        throw "Asset directory does not exist: $Directory"
    }
    $expected = @(Get-ExpectedAssetNames $ReleaseVersion)
    $actualEntries = @(Get-ChildItem -LiteralPath $Directory -Force)
    if (@($actualEntries | Where-Object { $_.PSIsContainer }).Count -gt 0) {
        throw 'Release asset directory contains a subdirectory; expected exactly 15 files.'
    }
    Assert-NameSet -Actual @($actualEntries.Name) -Expected $expected -Label 'Release asset'
    foreach ($entry in $actualEntries) {
        if ($entry.Length -eq 0) {
            throw "Release asset is empty: $($entry.Name)"
        }
    }

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
    Assert-YashanRedistribution -Directory $Directory -ReleaseVersion $ReleaseVersion

    $provenanceDocument = Get-Content -LiteralPath (Join-Path $Directory 'provenance.json') -Raw -Encoding UTF8 | ConvertFrom-Json
    if ([string]$provenanceDocument.version -ne $ReleaseVersion) {
        throw "provenance.json version '$($provenanceDocument.version)' does not match '$ReleaseVersion'."
    }
    if ([string]$provenanceDocument.source.commit -ne $ExpectedCommit) {
        throw "provenance.json source commit '$($provenanceDocument.source.commit)' does not match expected commit '$ExpectedCommit'."
    }

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
    Write-Host "RELEASE_DRYRUN_ASSET_COUNT=$($report.Count)"
    Write-Host 'RELEASE_DRYRUN_SHA256SUMS=PASS'
    Write-Host "RELEASE_DRYRUN_SIGNATURE_MODE=$(if ($isDryRunSet) { 'UNSIGNED_PLACEHOLDERS' } else { 'SIGNED_STRUCTURE_ONLY' })"
    return $report
}

if ($ValidateOnly) {
    if ($ProductionPrepare -or $PublicKeyPath) {
        throw '-ValidateOnly cannot be combined with -ProductionPrepare or -PublicKeyPath.'
    }
    if (-not $AssetsDirectory) {
        throw '-ValidateOnly requires -AssetsDirectory.'
    }
    if (-not [System.IO.Path]::IsPathRooted($AssetsDirectory)) {
        $AssetsDirectory = Join-Path $repositoryRoot $AssetsDirectory
    }
    $AssetsDirectory = [System.IO.Path]::GetFullPath($AssetsDirectory)
    $validationCommit = (& git -C $repositoryRoot rev-parse HEAD).Trim()
    if ($LASTEXITCODE -ne 0 -or $validationCommit -notmatch '^[0-9a-f]{40}$') {
        throw 'Could not read the expected git commit for validation.'
    }
    Show-AssetValidation -Directory $AssetsDirectory -ReleaseVersion $Version -ExpectedCommit $validationCommit
    Test-ReleaseAssets -Directory $AssetsDirectory -ReleaseVersion $Version -ExpectedCommit $validationCommit | Out-Null
    Write-Host 'RELEASE_DRYRUN_IMAGES=NOT_CHECKED_VALIDATE_ONLY'
    Write-Host 'RELEASE_DRYRUN_GHCR_PUBLICATION=NOT_PERFORMED'
    Write-Host 'RELEASE_DRYRUN_GIT_TAG=NOT_CREATED'
    Write-Host 'RELEASE_DRYRUN_VALIDATE_ONLY=PASS'
    exit 0
}

if ($AssetsDirectory) {
    throw '-AssetsDirectory is valid only with -ValidateOnly.'
}
if ($ProductionPrepare -and -not $PublicKeyPath) {
    throw '-ProductionPrepare requires -PublicKeyPath containing the authorized release public-key document.'
}
if (-not $ProductionPrepare -and $PublicKeyPath) {
    throw '-PublicKeyPath is valid only with -ProductionPrepare.'
}
if (-not $OutputDirectory) {
    $outputKind = if ($ProductionPrepare) { 'release-prepare' } else { 'release-dryrun' }
    $OutputDirectory = Join-Path $repositoryRoot "dist/$outputKind/$Version"
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
    $SourceDateEpoch = [string][DateTimeOffset]::UtcNow.ToUnixTimeSeconds()
}

if ($ProductionPrepare) {
    $productionDirty = @(& git -C $repositoryRoot status --porcelain --untracked-files=no)
    if ($LASTEXITCODE -ne 0) {
        throw 'Could not verify the production source worktree state.'
    }
    if ($productionDirty.Count -gt 0) {
        throw "Production preparation requires a completely clean worktree. Found: $($productionDirty -join '; ')"
    }
    if (-not [System.IO.Path]::IsPathRooted($PublicKeyPath)) {
        $PublicKeyPath = Join-Path $repositoryRoot $PublicKeyPath
    }
    $PublicKeyPath = [System.IO.Path]::GetFullPath($PublicKeyPath)
    if (-not (Test-Path -LiteralPath $PublicKeyPath -PathType Leaf)) {
        throw "Release public-key document does not exist: $PublicKeyPath"
    }
    $productionPublicDocument = Get-Content -LiteralPath $PublicKeyPath -Raw -Encoding UTF8 | ConvertFrom-Json
    if ([string]$productionPublicDocument.algorithm -ne 'Ed25519' -or
        [string]$productionPublicDocument.keyClass -ne 'release' -or
        [string]$productionPublicDocument.keyId -notmatch '^sha256:[0-9a-f]{64}$' -or
        [string]$productionPublicDocument.publicKeyBase64 -eq '') {
        throw 'Release public-key document is not a structurally valid Ed25519 release key.'
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
        '-e', 'GO_VERSION=1.26.8',
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
$sbom.documentNamespace = if ($ProductionPrepare) {
    "https://github.com/cuipengdba/agentsql/releases/download/$Version/$sbomName"
}
else {
    "https://github.com/cuipengdba/agentsql/releases/dry-run/$Version/$SourceDateEpoch"
}
Write-JsonFile -Path $sbomPath -Value $sbom -Depth 100

$packageCount = @($sbom.packages).Count
$relationshipCount = @($sbom.relationships).Count
$sbomTitle = if ($ProductionPrepare) { "AgentSQL $Version production SBOM generation" } else { "AgentSQL $Version dry-run SBOM generation" }
$sbomClosing = if ($ProductionPrepare) {
    'This production candidate must be signed and verified before upload.'
}
else {
    'Release day must rerun the pinned generator against the sealed source and binaries before production signing.'
}
$sbomInstructions = @"
$sbomTitle
$('=' * $sbomTitle.Length)

Format: SPDX 2.3 JSON
Generator image: $SyftImage
Output: $sbomName
SOURCE_DATE_EPOCH: $SourceDateEpoch

The offline scan input contains go.mod, go.sum, web/package.json,
web/package-lock.json, and both AgentSQL Linux binaries for amd64 and arm64.
Online enrichment is disabled. This run found $packageCount packages and
$relationshipCount relationships. $sbomClosing
"@
Write-Utf8File -Path (Join-Path $assetsPath 'SBOM-GENERATION.txt') -Content $sbomInstructions

$commit = (& git -C $repositoryRoot rev-parse HEAD).Trim()
if ($LASTEXITCODE -ne 0 -or $commit -notmatch '^[0-9a-f]{40}$') {
    throw 'Could not read git source identity.'
}
$branchStatus = @(& git -C $repositoryRoot status --porcelain=1 --branch)
if ($LASTEXITCODE -ne 0 -or $branchStatus.Count -eq 0 -or $branchStatus[0] -notmatch '^## (.+?)(?:\.\.\..*)?$') {
    throw 'Could not read git branch from status.'
}
$branch = $Matches[1]
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
    documentType  = if ($ProductionPrepare) { 'AgentSQL official release provenance' } else { 'AgentSQL release dry-run provenance' }
    version       = $Version
    notice        = if ($ProductionPrepare) { 'Production candidate: sign and verify all required documents before upload.' } else { 'DRY RUN ONLY: unsigned, not for upload or publication.' }
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
        kind       = if ($ProductionPrepare) { 'local Docker production preparation' } else { 'local Docker dry-run' }
        rockyImage = $RockyImage
        go         = '1.26.8'
        syftImage  = $SyftImage
        cgo        = $true
    }
    commands      = @(
        'build-release-linux.sh + package-release.sh (linux/amd64)',
        'build-release-linux.sh + package-release.sh (linux/arm64)',
        'syft dir:/scan --enrich=[] -o spdx-json'
    )
    signing       = if ($ProductionPrepare) {
        [ordered]@{ status = 'detached-signature-required'; reason = 'the production assembler never reads a private signing key' }
    }
    else {
        [ordered]@{ status = 'not-performed'; reason = 'dry-run is forbidden from accessing a signing private key' }
    }
    generatedAt   = $createdAt
}
if (-not $ProductionPrepare) {
    $provenance.Insert(3, 'dryRun', $true)
    $provenance.Insert(4, 'releasable', $false)
}
Write-JsonFile -Path (Join-Path $assetsPath 'provenance.json') -Value $provenance -Depth 30

$publicAssetPath = Join-Path $assetsPath 'ed25519-release-public-key.json'
if ($ProductionPrepare) {
    Copy-Item -LiteralPath $PublicKeyPath -Destination $publicAssetPath
}
else {
    $publicPlaceholder = [ordered]@{
        schemaVersion = 1
        dryRun        = $true
        signed        = $false
        releasable    = $false
        artifact      = 'ed25519-release-public-key.json'
        notice        = 'DRY RUN ONLY: production public key export is a release-day manual gate.'
    }
    Write-JsonFile -Path $publicAssetPath -Value $publicPlaceholder
}

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

if (-not $ProductionPrepare) {
    Write-UnsignedPlaceholder -ArtifactName $sbomName -SignatureName "$sbomName.sig.json"
    Write-UnsignedPlaceholder -ArtifactName 'provenance.json' -SignatureName 'provenance.json.sig.json'
}

if ($ProductionPrepare) {
$verificationInstructions = @'
# Release signature verification ({VERSION})

These are detached Ed25519 signatures over the raw bytes of each named file.
Verify the SPDX SBOM, `provenance.json`, and `SHA256SUMS` with the checked-in
`scripts/releasesign/main.go` helper and `ed25519-release-public-key.json`
before uploading any asset. Also run `scripts/release-dryrun.ps1 -ValidateOnly`
against the completed 15-file directory from the sealed release commit.

From a repository checkout at tag {VERSION}, set `$AssetDirectory to the
download directory and run:

```powershell
go run ./scripts/releasesign/main.go -mode verify -public "$AssetDirectory/ed25519-release-public-key.json" -input "$AssetDirectory/{SBOM}" -signature "$AssetDirectory/{SBOM}.sig.json"
go run ./scripts/releasesign/main.go -mode verify -public "$AssetDirectory/ed25519-release-public-key.json" -input "$AssetDirectory/provenance.json" -signature "$AssetDirectory/provenance.json.sig.json"
go run ./scripts/releasesign/main.go -mode verify -public "$AssetDirectory/ed25519-release-public-key.json" -input "$AssetDirectory/SHA256SUMS" -signature "$AssetDirectory/SHA256SUMS.sig.json"
pwsh ./scripts/release-dryrun.ps1 -Version {VERSION} -ValidateOnly -AssetsDirectory $AssetDirectory
```
'@
    $verificationInstructions = $verificationInstructions.Replace('{VERSION}', $Version).Replace('{SBOM}', $sbomName)
}
else {
    $verificationInstructions = @'
# Release signature verification ({VERSION} dry-run)

This directory was produced by `scripts/release-dryrun.ps1`. It intentionally
contains unsigned placeholders and is **NOT FOR RELEASE**. The dry-run never
reads a private signing key.

On release day, an authorized maintainer must replace the public-key placeholder
and all three `*.sig.json` placeholders using the production Ed25519 process,
then verify the raw bytes of the SPDX SBOM, `provenance.json`, and `SHA256SUMS`
with `go run ./scripts/releasesign/main.go -mode verify`. Only verified files
may be uploaded to a GitHub Release.
'@
    $verificationInstructions = $verificationInstructions.Replace('{VERSION}', $Version)
}
Write-Utf8File -Path (Join-Path $assetsPath 'VERIFYING-SIGNATURES.md') -Content $verificationInstructions

if ($ProductionPrepare) {
    Write-Host 'RELEASE_PREPARE_UNSIGNED_ASSET_COUNT=11'
    Write-Host 'RELEASE_PREPARE_SIGNING_REQUIRED=SBOM_PROVENANCE_SHA256SUMS'
}
else {
    $expectedNames = @(Get-ExpectedAssetNames $Version)
    $manifestNames = @($expectedNames | Where-Object { $_ -notin @('SHA256SUMS', 'SHA256SUMS.sig.json') } | Sort-Object)
    $manifestLines = foreach ($name in $manifestNames) {
        "$(Get-Sha256 (Join-Path $assetsPath $name))  $name"
    }
    Write-Utf8File -Path (Join-Path $assetsPath 'SHA256SUMS') -Content (($manifestLines -join "`n") + "`n")
    Write-UnsignedPlaceholder -ArtifactName 'SHA256SUMS' -SignatureName 'SHA256SUMS.sig.json'

    Show-AssetValidation -Directory $assetsPath -ReleaseVersion $Version -ExpectedCommit $commit
    Test-ReleaseAssets -Directory $assetsPath -ReleaseVersion $Version -ExpectedCommit $commit | Out-Null
}

if ($SkipImages) {
    Write-Utf8File -Path (Join-Path $evidencePath 'image-build-skipped.txt') -Content "Image build skipped explicitly; all GHCR build/push/public/anonymous-pull gates remain pending release day.`n"
    Write-Host "$(if ($ProductionPrepare) { 'RELEASE_PREPARE_IMAGES' } else { 'RELEASE_DRYRUN_IMAGES' })=SKIPPED_BY_REQUEST"
}
else {
    $imageEvidence = Join-Path $evidencePath 'images'
    New-Item -ItemType Directory -Path $imageEvidence -Force | Out-Null
    $mainArchive = Join-Path $imageEvidence "ghcr-agentsql-$Version-oci.tar"
    $digestOutput = Join-Path $imageEvidence "ghcr-agentsql-$Version-image-digests.json"
    $localBuilder = 'agentsql-release-dryrun'
    $builderExists = $true
    try {
        & docker buildx inspect $localBuilder *> $null
        if ($LASTEXITCODE -ne 0) { $builderExists = $false }
    }
    catch {
        $builderExists = $false
    }
    if (-not $builderExists) {
        & docker buildx create --name $localBuilder --driver docker-container --use
        if ($LASTEXITCODE -ne 0) { throw "Could not create local Buildx builder '$localBuilder'." }
    }
    & (Join-Path $PSScriptRoot 'build-ghcr-multiarch.ps1') -Version $Version -Builder $localBuilder -Output $mainArchive -DigestOutput $digestOutput -IncludeAuxiliaryTags
    if ($LASTEXITCODE -ne 0) {
        throw 'Local multi-architecture image dry-run failed.'
    }
    Write-Host "$(if ($ProductionPrepare) { 'RELEASE_PREPARE_IMAGES' } else { 'RELEASE_DRYRUN_IMAGES' })=PASS"
}

if ($ProductionPrepare) {
    Write-Host "RELEASE_PREPARE_OUTPUT=$OutputDirectory"
    Write-Host 'RELEASE_PREPARE_RESULT=PASS_UNSIGNED_REQUIRES_SIGNING'
}
else {
    Write-Host "RELEASE_DRYRUN_OUTPUT=$OutputDirectory"
    Write-Host 'RELEASE_DRYRUN_GHCR_PUBLICATION=NOT_PERFORMED'
    Write-Host 'RELEASE_DRYRUN_GIT_TAG=NOT_CREATED'
    Write-Host 'RELEASE_DRYRUN_RESULT=PASS_UNSIGNED_NOT_FOR_RELEASE'
}

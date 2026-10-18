# Build the linux/amd64 + linux/arm64 GHCR image locally as OCI archives.
# Push is opt-in. -Push never moves latest; use -PromoteLatest only after the
# exact version tag has passed the public, anonymous, dual-architecture gates.
# Publishing requires a clean HEAD and the same annotated release tag locally
# and on GitHub. Promoting latest also requires the accepted main-image digest.
# GHCR package visibility has no REST API to change it; the owner must use the
# package settings web page to make a package public.
# Before using either publishing mode, the release owner must run on their device:
#   gh auth refresh -h github.com -s write:packages
[CmdletBinding()]
param(
    [ValidatePattern('^v[0-9]+\.[0-9]+\.[0-9]+$')]
    [string]$Version = 'v2.1.0',

    [string]$Builder = 'agentsql-multiarch',

    [string]$Output,

    [string]$DigestOutput,

    [switch]$IncludeAuxiliaryTags,

    [switch]$Push,

    [switch]$PromoteLatest,

    [ValidatePattern('^sha256:[0-9a-f]{64}$')]
    [string]$AcceptedMainDigest
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$repositoryRoot = Split-Path -Parent $PSScriptRoot
$sourceRevision = (& git -C $repositoryRoot rev-parse HEAD).Trim()
if ($LASTEXITCODE -ne 0 -or $sourceRevision -cnotmatch '^[0-9a-f]{40}$') {
    throw 'Could not identify the Git source revision for OCI labels.'
}

function Assert-PublishedReleaseSource {
    $currentHead = @(& git -C $repositoryRoot rev-parse HEAD)
    if ($LASTEXITCODE -ne 0 -or $currentHead.Count -ne 1 -or $currentHead[0] -cne $sourceRevision) {
        throw "Publishing source HEAD changed after script start; expected $sourceRevision."
    }
    $status = @(& git -C $repositoryRoot status --porcelain --untracked-files=all)
    if ($LASTEXITCODE -ne 0 -or $status.Count -ne 0) {
        throw 'Publishing requires a clean Git working tree, including no untracked files.'
    }

    $tagRef = "refs/tags/$Version"
    $localTagType = @(& git -C $repositoryRoot cat-file -t $tagRef)
    if ($LASTEXITCODE -ne 0 -or $localTagType.Count -ne 1 -or $localTagType[0] -cne 'tag') {
        throw "Publishing requires an annotated local tag $tagRef."
    }
    $localTagObject = @(& git -C $repositoryRoot rev-parse --verify $tagRef)
    $localTaggedCommit = @(& git -C $repositoryRoot rev-parse --verify "${tagRef}^{commit}")
    if ($LASTEXITCODE -ne 0 -or $localTagObject.Count -ne 1 -or $localTaggedCommit.Count -ne 1 -or
        $localTagObject[0] -cnotmatch '^[0-9a-f]{40}$' -or $localTaggedCommit[0] -cne $sourceRevision) {
        throw "Local annotated tag $tagRef does not resolve to HEAD $sourceRevision."
    }

    $originUrl = @(& git -C $repositoryRoot remote get-url origin)
    if ($LASTEXITCODE -ne 0 -or $originUrl.Count -ne 1 -or
        $originUrl[0] -notmatch '^(?:https://(?:[^@/]+@)?github\.com/|ssh://git@github\.com/|git@github\.com:)cuipengdba/agentsql(?:\.git)?/?$') {
        throw 'Publishing requires origin to be the cuipengdba/agentsql GitHub repository.'
    }

    $remoteRefs = @(& git -C $repositoryRoot ls-remote --exit-code --tags origin $tagRef "${tagRef}^{}")
    if ($LASTEXITCODE -ne 0 -or $remoteRefs.Count -ne 2) {
        throw "Publishing requires the annotated tag $tagRef on origin GitHub."
    }
    $remoteTagObject = ''
    $remoteTaggedCommit = ''
    foreach ($line in $remoteRefs) {
        $fields = $line -split "`t"
        if ($fields.Count -ne 2 -or $fields[0] -cnotmatch '^[0-9a-f]{40}$') {
            throw "Invalid GitHub tag reference returned for $tagRef."
        }
        if ($fields[1] -ceq $tagRef) {
            $remoteTagObject = $fields[0]
        }
        elseif ($fields[1] -ceq "${tagRef}^{}") {
            $remoteTaggedCommit = $fields[0]
        }
        else {
            throw "Unexpected GitHub tag reference returned for $tagRef."
        }
    }
    if ($remoteTagObject -cne $localTagObject[0] -or $remoteTaggedCommit -cne $sourceRevision) {
        throw "GitHub tag $tagRef differs from the local annotated tag or HEAD $sourceRevision."
    }
    Write-Host "RELEASE_SOURCE_VERIFIED=$Version $sourceRevision"
}

if ($Push -or $PromoteLatest) {
    Assert-PublishedReleaseSource
}
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

$image = 'ghcr.io/cuipengdba/agentsql'
if ($PromoteLatest) {
    if ($Push -or $IncludeAuxiliaryTags) {
        throw '-PromoteLatest cannot be combined with -Push or -IncludeAuxiliaryTags.'
    }
    if (-not $AcceptedMainDigest) {
        throw '-PromoteLatest requires -AcceptedMainDigest from the validated exact main image tag.'
    }

    function Get-RegistryDigest {
        param([Parameter(Mandatory = $true)][string]$Reference)

        $inspection = @(& docker buildx imagetools inspect $Reference)
        if ($LASTEXITCODE -ne 0) {
            throw "Could not inspect registry image $Reference."
        }
        $digestLines = @($inspection | Where-Object { $_ -match '^\s*Digest:\s+(sha256:[0-9a-f]{64})\s*$' })
        if ($digestLines.Count -ne 1) {
            throw "Registry image $Reference does not have one valid top-level sha256 digest."
        }
        return [regex]::Match($digestLines[0], 'sha256:[0-9a-f]{64}').Value
    }

    $exactReference = "${image}:$Version"
    $exactDigest = Get-RegistryDigest -Reference $exactReference
    if ($exactDigest -cne $AcceptedMainDigest) {
        throw "Exact tag $exactReference has digest $exactDigest, not accepted digest $AcceptedMainDigest."
    }
    $rawIndex = @(& docker buildx imagetools inspect --raw "${image}@$AcceptedMainDigest")
    if ($LASTEXITCODE -ne 0) {
        throw "Could not inspect raw multi-architecture index for $exactReference."
    }
    $index = ($rawIndex -join "`n") | ConvertFrom-Json
    if (-not $index.manifests) {
        throw "$exactReference does not contain a multi-architecture manifest index."
    }
    $targetPlatforms = @($index.manifests | Where-Object { $_.platform } |
        ForEach-Object { "$($_.platform.os)/$($_.platform.architecture)" })
    foreach ($requiredPlatform in @('linux/amd64', 'linux/arm64')) {
        if ($targetPlatforms -cnotcontains $requiredPlatform) {
            throw "$exactReference is missing required platform $requiredPlatform."
        }
    }
    $oldLatestDigest = Get-RegistryDigest -Reference "${image}:latest"
    Write-Host "LATEST_PREVIOUS_DIGEST=$oldLatestDigest"
    Write-Host "LATEST_TARGET_DIGEST=$AcceptedMainDigest"
    if ($oldLatestDigest -ceq $AcceptedMainDigest) {
        Write-Host "${image}:latest already has accepted digest $AcceptedMainDigest."
        exit 0
    }

    Assert-PublishedReleaseSource
    $preCreateExactDigest = Get-RegistryDigest -Reference $exactReference
    if ($preCreateExactDigest -cne $AcceptedMainDigest) {
        throw "Exact tag $exactReference changed to $preCreateExactDigest before promotion; no write was attempted."
    }
    $preCreateDigest = Get-RegistryDigest -Reference "${image}:latest"
    if ($preCreateDigest -cne $oldLatestDigest) {
        throw "latest changed from $oldLatestDigest to $preCreateDigest before promotion; no write was attempted."
    }

    try {
        & docker buildx imagetools create --tag "${image}:latest" "${image}@$AcceptedMainDigest"
        if ($LASTEXITCODE -ne 0) {
            throw "Could not promote accepted digest $AcceptedMainDigest to ${image}:latest."
        }
        $latestDigest = Get-RegistryDigest -Reference "${image}:latest"
        if ($latestDigest -cne $AcceptedMainDigest) {
            throw "latest digest $latestDigest does not match accepted digest $AcceptedMainDigest."
        }
    }
    catch {
        $promotionError = $_.Exception.Message
        try {
            $currentLatestDigest = Get-RegistryDigest -Reference "${image}:latest"
        }
        catch {
            throw "PromoteLatest failed: $promotionError Could not inspect current latest digest; automatic restore was skipped: $($_.Exception.Message)"
        }
        if ($currentLatestDigest -ceq $oldLatestDigest) {
            throw "PromoteLatest failed: $promotionError latest still has previous digest $oldLatestDigest; no restore was needed."
        }
        if ($currentLatestDigest -cne $AcceptedMainDigest) {
            throw "PromoteLatest failed: $promotionError latest now has third-party digest $currentLatestDigest; automatic restore was refused."
        }

        $restoreError = $null
        try {
            & docker buildx imagetools create --tag "${image}:latest" "${image}@$oldLatestDigest"
            if ($LASTEXITCODE -ne 0) {
                throw "Could not restore previous digest $oldLatestDigest."
            }
            $restoredDigest = Get-RegistryDigest -Reference "${image}:latest"
            if ($restoredDigest -cne $oldLatestDigest) {
                throw "Restore returned digest $restoredDigest instead of $oldLatestDigest."
            }
        }
        catch {
            $restoreError = $_.Exception.Message
        }
        if ($restoreError) {
            throw "PromoteLatest failed: $promotionError Automatic restore of previous latest digest $oldLatestDigest also failed: $restoreError"
        }
        throw "PromoteLatest failed: $promotionError Previous latest digest $oldLatestDigest was restored."
    }
    Write-Host "Promoted ${image}:latest to ${image}:$Version at $AcceptedMainDigest."
    exit 0
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

function Assert-OciArchivePlatforms {
    param([Parameter(Mandatory = $true)][string]$Archive)

    $indexText = (& tar -xOf $Archive index.json) -join "`n"
    if ($LASTEXITCODE -ne 0) {
        throw "Could not read index.json from OCI archive $Archive."
    }
    $index = $indexText | ConvertFrom-Json
    if (-not $index.manifests -or $index.manifests.Count -ne 1 -or
        [string]$index.manifests[0].mediaType -ne 'application/vnd.oci.image.index.v1+json') {
        throw "OCI archive $Archive does not contain exactly one tagged image index."
    }
    $indexDigest = [string]$index.manifests[0].digest
    if ($indexDigest -notmatch '^sha256:[0-9a-f]{64}$') {
        throw "OCI archive $Archive has an invalid tagged index digest."
    }
    $nestedMember = 'blobs/sha256/' + $indexDigest.Substring(7)
    $nestedText = (& tar -xOf $Archive $nestedMember) -join "`n"
    if ($LASTEXITCODE -ne 0) {
        throw "Could not read the tagged image index from OCI archive $Archive."
    }
    $nested = $nestedText | ConvertFrom-Json
    $actual = @(
        $nested.manifests |
            Where-Object { $_.platform.os -ne 'unknown' -and $_.platform.architecture -ne 'unknown' } |
            ForEach-Object { "$($_.platform.os)/$($_.platform.architecture)" } |
            Sort-Object
    )
    $expected = @('linux/amd64', 'linux/arm64')
    if ($actual.Count -ne $expected.Count -or
        (Compare-Object -ReferenceObject $expected -DifferenceObject $actual)) {
        throw "OCI archive $Archive platforms mismatch. Expected linux/amd64, linux/arm64; actual: $($actual -join ', ')."
    }
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
        '--build-arg', "REVISION=$sourceRevision",
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

    if ($Push) {
        Assert-PublishedReleaseSource
    }
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
    if (-not $Push) {
        Assert-OciArchivePlatforms -Archive $Archive
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
$records += Invoke-MultiArchBuild -Tags $mainTags -Archive $Output -MetadataFile $mainMetadata

if ($IncludeAuxiliaryTags) {
    $demoArchive = Join-Path $outputDirectory "ghcr-agentsql-$Version-demo-oci.tar"
    $quickstartArchive = Join-Path $outputDirectory "ghcr-agentsql-$Version-quickstart-oci.tar"
    $auxiliaryMetadata = Join-Path $outputDirectory "ghcr-agentsql-$Version-auxiliary.metadata.json"
    $bakePath = Join-Path $outputDirectory ('.agentsql-auxiliary-' + [guid]::NewGuid().ToString('N') + '.json')
    $quickstartContext = Join-Path $repositoryRoot 'deploy/quickstart/gateway'
    $quickstartDockerfile = @'
# syntax=docker/dockerfile:1
FROM agentsql-demo
USER root
COPY config.demo.yaml /etc/agentsql/config.demo.yaml
COPY demo-seed.yaml /etc/agentsql/demo-seed.yaml
RUN chown agentsql:agentsql /etc/agentsql/config.demo.yaml /etc/agentsql/demo-seed.yaml \
    && chmod 0644 /etc/agentsql/config.demo.yaml /etc/agentsql/demo-seed.yaml
USER agentsql
'@
    $bakeDocument = [ordered]@{
        target = [ordered]@{
            demo = [ordered]@{
                context    = $repositoryRoot
                dockerfile = 'Dockerfile'
                args       = [ordered]@{ VERSION = $Version; REVISION = $sourceRevision }
                platforms  = @('linux/amd64', 'linux/arm64')
                tags       = @("${image}:$Version-demo")
                output     = @(if ($Push) { 'type=registry' } else { "type=oci,dest=$demoArchive" })
            }
            quickstart = [ordered]@{
                context             = $quickstartContext
                'dockerfile-inline' = $quickstartDockerfile
                contexts            = [ordered]@{ 'agentsql-demo' = 'target:demo' }
                platforms           = @('linux/amd64', 'linux/arm64')
                tags                = @("${image}:$Version-quickstart")
                output              = @(if ($Push) { 'type=registry' } else { "type=oci,dest=$quickstartArchive" })
            }
        }
    }
    $bakeJson = ($bakeDocument | ConvertTo-Json -Depth 12) + "`n"
    [System.IO.File]::WriteAllText($bakePath, $bakeJson, (New-Object System.Text.UTF8Encoding($false)))
    try {
        if ($Push) {
            Assert-PublishedReleaseSource
        }
        & docker buildx bake --builder $Builder --file $bakePath --metadata-file $auxiliaryMetadata demo quickstart
        if ($LASTEXITCODE -ne 0) {
            throw 'Multi-architecture auxiliary image build failed.'
        }
    }
    finally {
        if (Test-Path -LiteralPath $bakePath) {
            Remove-Item -LiteralPath $bakePath -Force
        }
    }
    foreach ($auxiliary in @(
        [pscustomobject]@{ Tag = "${image}:$Version-demo"; Archive = $demoArchive },
        [pscustomobject]@{ Tag = "${image}:$Version-quickstart"; Archive = $quickstartArchive }
    )) {
        if ($Push) {
            $inspectOutput = @(& docker buildx imagetools inspect $auxiliary.Tag)
            if ($LASTEXITCODE -ne 0) {
                throw "Could not inspect pushed auxiliary image $($auxiliary.Tag)."
            }
            $digestLine = @($inspectOutput | Where-Object { $_ -match '^\s*Digest:\s+(sha256:[0-9a-f]{64})\s*$' })
            $auxiliaryDigest = if ($digestLine.Count -gt 0) { [regex]::Match($digestLine[0], 'sha256:[0-9a-f]{64}').Value } else { '' }
        }
        else {
            Assert-OciArchivePlatforms -Archive $auxiliary.Archive
            $auxiliaryDigest = Get-ArchiveDigest -Archive $auxiliary.Archive
        }
        if ($auxiliaryDigest -notmatch '^sha256:[0-9a-f]{64}$') {
            throw "Auxiliary image $($auxiliary.Tag) does not have a valid digest."
        }
        $records += [pscustomobject]@{
            tag       = $auxiliary.Tag
            digest    = $auxiliaryDigest
            platforms = @('linux/amd64', 'linux/arm64')
            output    = if ($Push) { 'registry' } else { [System.IO.Path]::GetFileName($auxiliary.Archive) }
        }
    }
}

$digestDocument = [ordered]@{
    schemaVersion = 1
    image         = $image
    version       = $Version
    sourceRevision = $sourceRevision
    pushed        = [bool]$Push
    records       = @($records)
}
$utf8NoBom = New-Object System.Text.UTF8Encoding($false)
$digestJson = ($digestDocument | ConvertTo-Json -Depth 8) + "`n"
[System.IO.File]::WriteAllText($DigestOutput, $digestJson, $utf8NoBom)

if ($Push) {
    $visibility = $null
    if (Get-Command gh -ErrorAction SilentlyContinue) {
        try {
            $visibilityOutput = @(& gh api --method GET /user/packages/container/agentsql --jq .visibility 2>$null)
            if ($LASTEXITCODE -eq 0 -and $visibilityOutput.Count -gt 0) {
                $visibility = [string]$visibilityOutput[0]
            }
        }
        catch {
            # Visibility lookup must not turn a successful image push into a failure.
        }
    }
    if ($visibility -and $visibility.Trim() -eq 'public') {
        Write-Host 'PACKAGE_VISIBILITY_PUBLIC'
    }
    else {
        Write-Host 'VISIBILITY_OWNER_ACTION_REQUIRED=web Package settings Danger Zone Change visibility'
    }
    Write-Host "Pushed exact release tags. latest was not changed."
}
else {
    Write-Host "Wrote multi-architecture OCI archive: $Output"
}
if ($IncludeAuxiliaryTags) {
    Write-Host "Included base demo tag ${image}:$Version-demo and configured quickstart tag ${image}:$Version-quickstart."
}
Write-Host "Wrote image digest record: $DigestOutput"

# Shared release provenance checks. This file is dot-sourced by the assembler,
# the 15-asset validator, and the production signer before it reads a secret key.

function Get-ReleaseInputNames {
    @(
        'go.mod', 'go.sum', 'web/package.json', 'web/package-lock.json',
        'Dockerfile', 'scripts/build-release-linux.sh', 'scripts/package-release.sh',
        'scripts/install.sh', 'scripts/release-dryrun.ps1',
        'scripts/build-ghcr-multiarch.ps1', 'Makefile'
    )
}

function Get-ReleaseSubjectNames {
    param([Parameter(Mandatory = $true)][string]$ReleaseVersion)
    @(
        "agentsql-$ReleaseVersion-linux-amd64.tar.gz",
        "agentsql-$ReleaseVersion-linux-amd64.tar.gz.sha256",
        "agentsql-$ReleaseVersion-linux-arm64.tar.gz",
        "agentsql-$ReleaseVersion-linux-arm64.tar.gz.sha256",
        'install.sh', "agentsql-$ReleaseVersion.spdx.json",
        'go-version-metadata.txt', 'SBOM-GENERATION.txt'
    )
}

function Assert-ReleaseNameSet {
    param(
        [Parameter(Mandatory = $true)][array]$Actual,
        [Parameter(Mandatory = $true)][array]$Expected,
        [Parameter(Mandatory = $true)][string]$Label
    )
    $names = [System.Collections.Generic.HashSet[string]]::new([System.StringComparer]::Ordinal)
    $expectedNames = [System.Collections.Generic.HashSet[string]]::new([System.StringComparer]::Ordinal)
    foreach ($name in $Expected) { [void]$expectedNames.Add([string]$name) }
    foreach ($name in $Actual) {
        if (-not $names.Add([string]$name)) { throw "$Label contains duplicate name: $name" }
        if (-not $expectedNames.Contains([string]$name)) { throw "$Label contains unexpected name: $name" }
    }
    if ($names.Count -ne $expectedNames.Count) {
        $missing = @($Expected | Where-Object { -not $names.Contains([string]$_) })
        throw "$Label name set is incomplete: $($missing -join ', ')"
    }
}

function Get-ReleaseCommitBlobSha256 {
    param(
        [Parameter(Mandatory = $true)][string]$Repository,
        [Parameter(Mandatory = $true)][string]$Commit,
        [Parameter(Mandatory = $true)][string]$Name
    )
    $git = (Get-Command git -CommandType Application -ErrorAction Stop | Select-Object -First 1).Source
    $start = [System.Diagnostics.ProcessStartInfo]::new()
    $start.FileName = $git
    $start.UseShellExecute = $false
    $start.RedirectStandardOutput = $true
    $start.RedirectStandardError = $true
    foreach ($argument in @('-C', $Repository, 'cat-file', 'blob', "${Commit}:$Name")) {
        [void]$start.ArgumentList.Add($argument)
    }
    $process = [System.Diagnostics.Process]::Start($start)
    try {
        $hasher = [System.Security.Cryptography.SHA256]::Create()
        try {
            $digest = $hasher.ComputeHash($process.StandardOutput.BaseStream)
        }
        finally {
            $hasher.Dispose()
        }
        $errorText = $process.StandardError.ReadToEnd()
        $process.WaitForExit()
        if ($process.ExitCode -ne 0) {
            throw "Cannot read sealed Git blob $Name from $Commit`: $errorText"
        }
        return [Convert]::ToHexString($digest).ToLowerInvariant()
    }
    finally {
        $process.Dispose()
    }
}

function Assert-ReleaseProvenance {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory = $true)][string]$Directory,
        [Parameter(Mandatory = $true)][string]$Repository,
        [Parameter(Mandatory = $true)][ValidatePattern('^v[0-9]+\.[0-9]+\.[0-9]+$')][string]$ReleaseVersion,
        [Parameter(Mandatory = $true)][ValidatePattern('^[0-9a-f]{40}$')][string]$ExpectedCommit,
        [Parameter(Mandatory = $true)][ValidateSet('Production', 'DryRun')][string]$Mode
    )

    $provenancePath = Join-Path $Directory 'provenance.json'
    $publicKeyPath = Join-Path $Directory 'ed25519-release-public-key.json'
    $instructionsPath = Join-Path $Directory 'VERIFYING-SIGNATURES.md'
    foreach ($path in @($provenancePath, $publicKeyPath, $instructionsPath)) {
        if (-not (Test-Path -LiteralPath $path -PathType Leaf)) {
            throw "Release provenance input is missing: $path"
        }
    }
    $document = Get-Content -LiteralPath $provenancePath -Raw -Encoding UTF8 | ConvertFrom-Json
    $publicKey = Get-Content -LiteralPath $publicKeyPath -Raw -Encoding UTF8 | ConvertFrom-Json
    $instructions = Get-Content -LiteralPath $instructionsPath -Raw -Encoding UTF8
    if ($document.schemaVersion -isnot [long] -or $document.schemaVersion -ne 1 -or
        $document.version -cne $ReleaseVersion) {
        throw 'Release provenance schema version or release version is wrong.'
    }
    if ($document.source.repository -cne 'https://github.com/cuipengdba/agentsql' -or
        $document.source.commit -cne $ExpectedCommit -or
        $document.source.branch -isnot [string] -or
        [string]::IsNullOrWhiteSpace([string]$document.source.branch) -or
        $document.source.dirtyAtBuild -isnot [bool] -or
        $document.source.sourceDateEpoch -isnot [long] -or
        $document.source.sourceDateEpoch -lt 0) {
        throw 'Release provenance source identity, epoch, or dirty state is invalid.'
    }
    if ($document.builder.go -cne '1.26.10' -or $document.builder.cgo -isnot [bool] -or
        $document.builder.cgo -ne $true -or
        $document.builder.rockyImage -cnotmatch '^[^\s]+@sha256:[0-9a-f]{64}$' -or
        $document.builder.syftImage -cnotmatch '^[^\s]+@sha256:[0-9a-f]{64}$') {
        throw 'Release provenance builder metadata is invalid.'
    }
    $expectedCommands = @(
        'build-release-linux.sh + package-release.sh (linux/amd64)',
        'build-release-linux.sh + package-release.sh (linux/arm64)',
        'syft dir:/scan --enrich=[] -o spdx-json'
    )
    if (@($document.commands).Count -ne $expectedCommands.Count) {
        throw 'Release provenance command list is incomplete.'
    }
    for ($index = 0; $index -lt $expectedCommands.Count; $index++) {
        if ($document.commands[$index] -cne $expectedCommands[$index]) {
            throw 'Release provenance command list differs from the assembler.'
        }
    }

    if ($Mode -eq 'Production') {
        if ($document.documentType -cne 'AgentSQL official release provenance' -or
            $document.notice -cne 'Production candidate: sign and verify all required documents before upload.' -or
            $document.builder.kind -cne 'local Docker production preparation' -or
            $document.signing.status -cne 'detached-signature-required' -or
            $document.signing.reason -cne 'the production assembler never reads a private signing key' -or
            $document.source.dirtyAtBuild -ne $false -or
            $document.PSObject.Properties['dryRun'] -or
            $document.PSObject.Properties['releasable']) {
            throw 'Release provenance is not a clean production preparation document.'
        }
        if ($publicKey.schemaVersion -isnot [long] -or $publicKey.schemaVersion -ne 1 -or
            $publicKey.algorithm -cne 'Ed25519' -or
            $publicKey.keyClass -cne 'release' -or
            [string]$publicKey.keyId -cnotmatch '^sha256:[0-9a-f]{64}$' -or
            $publicKey.PSObject.Properties['dryRun'] -or
            $publicKey.PSObject.Properties['signed'] -or
            $publicKey.PSObject.Properties['releasable']) {
            throw 'Production release public-key document is invalid.'
        }
        try {
            $encodedKey = [string]$publicKey.publicKeyBase64
            if ($encodedKey -cnotmatch '^[A-Za-z0-9+/]{43}$') {
                throw 'The production release public key is not raw Base64.'
            }
            $keyBytes = [Convert]::FromBase64String($encodedKey + '=')
            if ($keyBytes.Length -ne 32 -or
                $publicKey.keyId -cne ('sha256:' + [Convert]::ToHexString(
                        [System.Security.Cryptography.SHA256]::HashData($keyBytes)).ToLowerInvariant())) {
                throw 'The production release key ID does not match its public key.'
            }
        }
        catch {
            throw "Production release public key failed validation: $($_.Exception.Message)"
        }
        $verifyPrefix = 'go run ./scripts/releasesign/main.go -mode verify -public "$AssetDirectory/ed25519-release-public-key.json"'
        $expectedVerifyLines = foreach ($artifact in @("agentsql-$ReleaseVersion.spdx.json", 'provenance.json', 'SHA256SUMS')) {
            $verifyPrefix + ' -input "$AssetDirectory/' + $artifact + '" -signature "$AssetDirectory/' + $artifact + '.sig.json"'
        }
        $validateLine = 'pwsh ./scripts/release-dryrun.ps1 -Version ' + $ReleaseVersion + ' -ValidateOnly -AssetsDirectory $AssetDirectory'
        if (-not $instructions.StartsWith("# Release signature verification ($ReleaseVersion)`n", [System.StringComparison]::Ordinal) -or
            $instructions -notlike '*before uploading any asset*' -or
            -not $instructions.Contains('$env:GOTOOLCHAIN = ''local''') -or
            -not $instructions.Contains('$goVersion = go version') -or
            -not $instructions.Contains("-notmatch '^go version go1\.26\.'") -or
            @([regex]::Matches($instructions, 'scripts/releasesign/main\.go -mode verify')).Count -ne 3 -or
            @($expectedVerifyLines | Where-Object { -not $instructions.Contains($_) }).Count -gt 0 -or
            -not $instructions.Contains($validateLine) -or
            $instructions -like '*DRY RUN ONLY*' -or
            $instructions -like '*NOT FOR RELEASE*' -or
            $instructions -like '*unsigned placeholders*') {
            throw 'Production signature-verification instructions are invalid.'
        }
    }
    else {
        if ($document.documentType -cne 'AgentSQL release dry-run provenance' -or
            $document.notice -cne 'DRY RUN ONLY: unsigned, not for upload or publication.' -or
            $document.builder.kind -cne 'local Docker dry-run' -or
            $document.signing.status -cne 'not-performed' -or
            $document.signing.reason -cne 'dry-run is forbidden from accessing a signing private key' -or
            -not $document.PSObject.Properties['dryRun'] -or $document.dryRun -ne $true -or
            -not $document.PSObject.Properties['releasable'] -or $document.releasable -ne $false) {
            throw 'Release provenance is not an unsigned dry-run document.'
        }
        if ($publicKey.schemaVersion -isnot [long] -or $publicKey.schemaVersion -ne 1 -or
            $publicKey.dryRun -ne $true -or
            $publicKey.signed -ne $false -or $publicKey.releasable -ne $false -or
            $publicKey.PSObject.Properties['publicKeyBase64']) {
            throw 'Dry-run public-key placeholder is invalid.'
        }
        if (-not $instructions.StartsWith("# Release signature verification ($ReleaseVersion dry-run)`n", [System.StringComparison]::Ordinal) -or
            $instructions -notlike '*NOT FOR RELEASE*' -or
            $instructions -notlike '*unsigned placeholders*') {
            throw 'Dry-run signature-verification instructions are invalid.'
        }
    }

    $expectedSubjects = @(Get-ReleaseSubjectNames -ReleaseVersion $ReleaseVersion)
    $subjects = @($document.subjects)
    Assert-ReleaseNameSet -Actual @($subjects | ForEach-Object { [string]$_.name }) -Expected $expectedSubjects -Label 'Release provenance subjects'
    foreach ($subject in $subjects) {
        $name = [string]$subject.name
        $path = Join-Path $Directory $name
        if (-not (Test-Path -LiteralPath $path -PathType Leaf)) {
            throw "Release provenance subject is missing: $name"
        }
        $file = Get-Item -LiteralPath $path
        $sha256 = (Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash.ToLowerInvariant()
        if ($subject.size -isnot [long] -or $subject.size -ne $file.Length -or
            [string]$subject.digest.sha256 -cnotmatch '^[0-9a-f]{64}$' -or
            $subject.digest.sha256 -cne $sha256) {
            throw "Release provenance subject size or SHA-256 mismatch: $name"
        }
    }

    $expectedInputs = @(Get-ReleaseInputNames)
    $inputs = @($document.inputs)
    Assert-ReleaseNameSet -Actual @($inputs | ForEach-Object { [string]$_.name }) -Expected $expectedInputs -Label 'Release provenance inputs'
    foreach ($input in $inputs) {
        $name = [string]$input.name
        if ([string]$input.sha256 -cnotmatch '^[0-9a-f]{64}$') {
            throw "Release provenance input SHA-256 is malformed: $name"
        }
        if ($Mode -eq 'Production' -or $document.source.dirtyAtBuild -eq $false) {
            $committedSha256 = Get-ReleaseCommitBlobSha256 -Repository $Repository -Commit $ExpectedCommit -Name $name
            if ($input.sha256 -cne $committedSha256) {
                throw "Release provenance input differs from sealed Git blob: $name"
            }
        }
    }
}

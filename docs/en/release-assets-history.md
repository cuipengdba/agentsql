> Historical archive: retains the original plans and facts at each release point, not the current version operating guide.

# AgentSQL Release Asset List

**Language:** [English](release-assets-history.md) | [Simplified Chinese](../release-assets-history.md)

## v2.0.0 expected assets

`scripts/release-dryrun.ps1` defines the official version with exactly 15 assets: Linux amd64/arm64 installation packages and respective SHA256 files, SPDX SBOM and signature, provenance and signature, SHA256SUMS and signature, public key, installer, Go version metadata, SBOM generation instructions and signature verification guide. All assets must come from the same clean submission. After uploading, you should download it anonymously from GitHub and check the number of bytes, SHA256, Ed25519 signatures, source submissions, and actual content of the dual architecture; the results of v2.0.0 are recorded in [, the official release evidence ](v2.0.0-release-evidence.md).

## v1.2.0 expected assets

15 official assets must be submitted from the same clean board of v1.2.0:

```text
agentsql-v1.2.0-linux-amd64.tar.gz
agentsql-v1.2.0-linux-amd64.tar.gz.sha256
agentsql-v1.2.0-linux-arm64.tar.gz
agentsql-v1.2.0-linux-arm64.tar.gz.sha256
agentsql-v1.2.0.spdx.json
agentsql-v1.2.0.spdx.json.sig.json
ed25519-release-public-key.json
go-version-metadata.txt
install.sh
provenance.json
provenance.json.sig.json
SBOM-GENERATION.txt
SHA256SUMS
SHA256SUMS.sig.json
VERIFYING-SIGNATURES.md
```

The three Ed25519 signatures cover SBOM, provenance, and checklist respectively; the checklist covers the remaining 13 items. After the official upload, you must anonymously download and re-verify the public key ID, signature, hash and source submission.

## v1.0.0 expected assets

`scripts/release-dryrun.ps1` defines 15 formal assets, all of which must be submitted from the same clean seal board of v1.0.0:

```text
agentsql-v1.0.0-linux-amd64.tar.gz
agentsql-v1.0.0-linux-amd64.tar.gz.sha256
agentsql-v1.0.0-linux-arm64.tar.gz
agentsql-v1.0.0-linux-arm64.tar.gz.sha256
agentsql-v1.0.0.spdx.json
agentsql-v1.0.0.spdx.json.sig.json
ed25519-release-public-key.json
go-version-metadata.txt
install.sh
provenance.json
provenance.json.sig.json
SBOM-GENERATION.txt
SHA256SUMS
SHA256SUMS.sig.json
VERIFYING-SIGNATURES.md
```

The three Ed25519 signatures cover SBOM, provenance, and checklist respectively; the checklist covers the remaining 13 items. After the official upload, you must anonymously download and re-verify the public key ID, signature, hash and source submission.

## v0.9.0 expected assets

`scripts/release-dryrun.ps1` defines exactly 15 formal assets, all of which must be submitted from the same clean board of v0.9.0:

```text
agentsql-v0.9.0-linux-amd64.tar.gz
agentsql-v0.9.0-linux-amd64.tar.gz.sha256
agentsql-v0.9.0-linux-arm64.tar.gz
agentsql-v0.9.0-linux-arm64.tar.gz.sha256
agentsql-v0.9.0.spdx.json
agentsql-v0.9.0.spdx.json.sig.json
ed25519-release-public-key.json
go-version-metadata.txt
install.sh
provenance.json
provenance.json.sig.json
SBOM-GENERATION.txt
SHA256SUMS
SHA256SUMS.sig.json
VERIFYING-SIGNATURES.md
```

The three Ed25519 signatures cover SBOM, provenance, and checklist respectively; the checklist covers the remaining 13 items. After the official upload, you must anonymously download and re-verify the public key ID, signature, hash and source submission.

## v0.8.0 expected assets

`scripts/release-dryrun.ps1`of`Get-ExpectedAssetNames`is the actual name definition.v0.8.0Exactly 15 items should be uploaded:

```text
agentsql-v0.8.0-linux-amd64.tar.gz
agentsql-v0.8.0-linux-amd64.tar.gz.sha256
agentsql-v0.8.0-linux-arm64.tar.gz
agentsql-v0.8.0-linux-arm64.tar.gz.sha256
agentsql-v0.8.0.spdx.json
agentsql-v0.8.0.spdx.json.sig.json
ed25519-release-public-key.json
go-version-metadata.txt
install.sh
provenance.json
provenance.json.sig.json
SBOM-GENERATION.txt
SHA256SUMS
SHA256SUMS.sig.json
VERIFYING-SIGNATURES.md
```

All documents must be submitted from the same v0.8.0 board; the signature covers SBOM, provenance and checklist respectively, and the checklist covers the remaining 13 items. Public key ID, three copies of Ed25519 signature verification, source submission, dual-architecture content, YashanDB client, GHCR dual-architecture and anonymous pull must be verified according to the [ release process ](RELEASE_PROCESS.md). The v0.7.0 and v0.5.0 data below are for historical reference only.

## v0.7.0 expected assets and formal verification threshold

`scripts/release-dryrun.ps1` `Get-ExpectedAssetNames` is the only executable definition of the asset name. v0.7.0 The official GitHub Release should upload exactly the following 15 files; you cannot reuse old version files or upload local dry-run placeholder signatures:

```text
agentsql-v0.7.0-linux-amd64.tar.gz
agentsql-v0.7.0-linux-amd64.tar.gz.sha256
agentsql-v0.7.0-linux-arm64.tar.gz
agentsql-v0.7.0-linux-arm64.tar.gz.sha256
agentsql-v0.7.0.spdx.json
agentsql-v0.7.0.spdx.json.sig.json
ed25519-release-public-key.json
go-version-metadata.txt
install.sh
provenance.json
provenance.json.sig.json
SBOM-GENERATION.txt
SHA256SUMS
SHA256SUMS.sig.json
VERIFYING-SIGNATURES.md
```

The currently completed v0.7.0 local exercise has passed 15/15 file verifications, but the signature and public key are still clear dry-run placeholder files, and the image construction was skipped with `-SkipImages`; this product comes from an unsealed working tree and cannot be released. Ultimately, the source code, migrations, embedded console, and documentation must first be merged into the same commit and rebuilt from a new clean working tree of that commit. Check the complete `git status --porcelain --untracked-files=all` and HEAD before and after running `-ProductionPrepare`; the script rejects untracked non-ignored files and reviews key input hashes after the build. `-ProductionPrepare` is prohibited from being used together with `-SkipImages`. `provenance.json` must point to the final commit and `dirtyAtBuild=false`.

The official asset closure submission SHA, 15 actual summaries, independently verified release public key key ID, three Ed25519 signature verification exit codes, GitHub Release download results, and the dual-architecture digest and anonymous pull results of three accurate GHCR tags are all based on the [v0.7.0 GitHub Release](https://github.com/cuipengdba/agentsql/releases/tag/v0.7.0) text and release evidence. The above local walkthrough is not a replacement for these fields.

Formal preparation first generates 11 files to be signed; common source verification must confirm the official provenance status, the size and SHA-256 of 8 subjects, the SHA-256 of the Git objects in the 11 input and closure commits before signing, and reject dry-run tags and descriptions. use`scripts/releasesign/main.go`Sign SPDX SBOM and`provenance.json`, and then generate a file covering the remaining 13 files.`SHA256SUMS`After signing the list together, there were only 15 items. Make three signatures separately`-mode verify`Cryptographically verify signature and run again`-ValidateOnly`Review the name, abstract, YashanDB client, source and content structure; the same source verification must also be performed for download back and official website acceptance.`-ValidateOnly`of`SIGNED_STRUCTURE_ONLY`It cannot replace signature verification. Dual architecture OCI, GHCR precise tag anonymous pull,`latest`, GitHub Release download and official website are separate gates, see[Release process](RELEASE_PROCESS.md)and[Deployment Guide](DEPLOY.md)。

The v0.7.0 rollback assets must also be processed according to the topology: the SQLite merge library can be restored to the published v0.6.0 after restoring the pre-upgrade backup and pairing key; the rollback target of the PostgreSQL metadata/audit sub-library is the [v0.6.1 official maintenance version ](https://github.com/cuipengdba/agentsql/releases/tag/v0.6.1) that has passed the isolation drill. The isolation upgrade rollback retest results of the two official packages are subject to the v0.7.0 GitHub Release text. The original v0.6.0 with branch library startup defects cannot be used; the old version cannot directly read the new schema. Specific production deployments must also conduct a separate backup and recovery exercise in the target environment.

## v0.5.0 Historical assets and drill records

The baseline is [ public v0.4.0 GitHub Release](https://github.com/cuipengdba/agentsql/releases/tag/v0.4.0): Use `gh release view v0.4.0 --repo cuipengdba/agentsql --json assets` to check **15 uploaded assets**, which are consistent with the file name and size of local `dist/release/v0.4.0-official/`. v0.5.0 follows these 15 names and signature structures; the source code compressed package automatically generated by GitHub is not counted in the 15 uploaded assets.

The following table records the expected list in the preparatory stage of v0.5.0, **is not v0.7.0 and the statement** has been issued or verified. Sources correspond to `scripts/release-dryrun.ps1`, `scripts/build-release-linux.sh`, `scripts/package-release.sh` and `scripts/releasesign/main.go`. Each official Release must be regenerated with its final seal submission and accepted item by item.

The v0.4.0 community release materials on 2026-09-30 only outline the dual-architecture installation package, SHA-256, SBOM and digital signatures. **does not itemize the 15 uploaded files**; its AGPLv3 copywriting is only applicable to v0.4.0 at that time. The 15 names and generation/acceptance rules of v0.5.0 should be as shown in the table below and `Get-ExpectedAssetNames`. The signature covers SBOM, provenance, and summary lists; the archive package is verified by the sidecar and the signed summary list. "Each publication has a digital signature" cannot be understood to mean that each file has an independent signature. Old published materials may not be copied directly into v0.5 promotional copy.

| # | v0.5.0 Asset | Location and source of production | Must check items |
| ---: | --- | --- | --- |
| 1 | `agentsql-v0.5.0-linux-amd64.tar.gz` | Rocky 8 amd64；`build-release-linux.sh` → `package-release.sh` | Outer and inner SHA-256, ELF/GLIBC, versions; including `lib/yashandb/libyascli.so` and `libyas_infra.so` |
| 2 | `agentsql-v0.5.0-linux-amd64.tar.gz.sha256` | `package-release.sh` | The only row `64hex␠␠filename`, recalculation #1 |
| 3 | `agentsql-v0.5.0-linux-arm64.tar.gz` | Rocky 8 arm64; Same as #1 | Same as #1, and confirms AArch64 client runtime |
| 4 | `agentsql-v0.5.0-linux-arm64.tar.gz.sha256` | `package-release.sh` | Only row, recalculation #3 |
| 5 | `agentsql-v0.5.0.spdx.json` | Fixed digest's Syft, dependency on blocking and dual-architecture binary scanning | SPDX JSON, version, package and relationship, official namespace |
| 6 | `agentsql-v0.5.0.spdx.json.sig.json` | Official Ed25519 Signature Tool | Signature verification of #5 raw bytes |
| 7 | `ed25519-release-public-key.json` | Official release public key export | key class, key ID, public key; consistent with three signatures |
| 8 | `go-version-metadata.txt` | Dual architecture `go version -m` merge | Four binary numbers; `yashandb-go v1.4.4` |
| 9 | `install.sh` | Closure board submission `scripts/install.sh` | Same as source code SHA-256; syntax and offline installation |
| 10 | `provenance.json` | `release-dryrun.ps1 -ProductionPrepare` | Version, board commit, input summary, `dirtyAtBuild=false`; non-dry-run |
| 11 | `provenance.json.sig.json` | Official Ed25519 Signature Tool | Signature verification of #10 raw bytes |
| 12 | `SBOM-GENERATION.txt` | `release-dryrun.ps1 -ProductionPrepare` | Syft digest, epoch, scan input and actual SBOM are consistent |
| 13 | `SHA256SUMS` | #6, #11 generated after completion | Exactly covers **13** unique file names except itself and #14; recalculate item by item |
| 14 | `SHA256SUMS.sig.json` | Official Ed25519 signature tool, final signature | Verify #13 raw bytes |
| 15 | `VERIFYING-SIGNATURES.md` | `release-dryrun.ps1 -ProductionPrepare` | Three formal verification commands point to this version of the file |

These 15 items are the list of uploaded assets for AgentSQL; the YashanDB C client is not the 16th independent uploaded asset, but is included in two Linux tarballs at `lib/yashandb/`. `scripts/package-release.sh` verifies `yashandb-go v1.4.4`, client ELF schema and dynamic dependencies, and checks the archive for two required libraries. `scripts/install.sh` verifies the package manifest, installs the client into `/usr/local/lib/agentsql/yashandb`, and manages this directory during upgrades and rollbacks. The final GHCR image contains the client and is set to `LD_LIBRARY_PATH=/opt/yashandb-client/lib`.

Redistribution of YashanDB C client has been verbally authorized by the manufacturer; the repository does not have written authorization documents. The above packaging location is the target design of the existing script, not the product acceptance conclusion of batch 71. Batch 71 did not package the client from the server image, nor did it rebuild the distribution; the reproducible access path at that time was for the user to obtain an independent client with a matching architecture and set `LD_LIBRARY_PATH` before starting. See [DEPLOY.md](DEPLOY.md) for details. When the client is missing or fails to load, the YashanDB connection fails to close. The asset name is still the 15 items in the above table; the tarball and dry-run must be regenerated from the final commit before acceptance.

## v0.5.0 Evidence and judgment at the time

- `dist/release-dryrun/v0.5.0/assets/` has 15 **old dry-run candidate files**, but the provenance commit is `02565f5a40bad6b6b778db1043db98e3df194c3c`, which is not the current HEAD. Signature and public key files are explicitly unsigned placeholders and cannot be uploaded.
- The Go metadata of the old directory is missing `yashandb-go v1.4.4`; executing `-ValidateOnly` on `dist/release-dryrun/v0.5.0/assets/` returns non-zero, printing 15 lines of inventory and `RELEASE_DRYRUN_RESULT=FAIL`. The SHA-256 of older files, even if they match themselves, does not prove that they are eligible releases of the current version.
- **2026-10-05 Historical walkthrough:**`dist/release-dryrun/v0.5.0-20261005b/` was generated by commit `e3099a3737ff8dcfbf63b088cb949963e312e42a`, which output `RELEASE_DRYRUN_ASSET_COUNT=15`, `RELEASE_DRYRUN_SHA256SUMS=PASS`, `RELEASE_DRYRUN_IMAGES=PASS`, and `RELEASE_DRYRUN_RESULT=PASS_UNSIGNED_NOT_FOR_RELEASE`. The walkthrough's tarball contains the YashanDB C client, but it must be re-walked from the final submission according to the current full verification rules; historical results cannot serve as release acceptance. None of the 15 files in the history directory are empty; `VERIFYING-SIGNATURES.md` is 589 bytes.
- This historical walkthrough is evidence that **is unsigned, local, unpublishable**: `provenance.json` tagged `dryRun=true`, `releasable=false`, and the working tree was not in a clean state at build time. Digest JSON tag `pushed=false` for three OCI archives. Formal assets still need to be regenerated and signed and released through the final gate submission clean work tree.
- `*.sig.json` for dry-run is placeholder JSON. For the formal process, `scripts/releasesign/main.go -mode verify` must be run separately for cryptographic signature verification; `-ValidateOnly` only checks the structure and file summary of the formal signature and cannot replace the signature verification.

## v0.5.0 read-only verification and local reconstruction at that time

Windows PowerShell can run `.ps1` directly; when PowerShell 7 is installed, `powershell` can also be changed to `pwsh`:

```powershell
powershell -NoProfile -File .\scripts\release-dryrun.ps1 -Version v0.5.0 -ValidateOnly -AssetsDirectory dist\release-dryrun\v0.5.0\assets
```

The validator will first list the 15 expected names, byte count, local SHA-256 and `PRESENT`/`MISSING` line by line, and then verify the name set, sidecar, `SHA256SUMS`, Yashan Go dependencies and the two required client libraries, provenance commit and signature documents in the tarball. Any failure returns non-zero; **appearing on 15 lines does not equal passing**. `SHA256SUMS` does not include itself and its signature to avoid cyclic digests.

After sealing the board, execute a full local walkthrough in an environment with Docker and required build dependencies, and output to a new directory:

```powershell
powershell -NoProfile -File .\scripts\release-dryrun.ps1 -Version v0.5.0 -OutputDirectory dist\release-dryrun\v0.5.0-final
```

By default, dual-architecture packages, SPDX SBOM, provenance, 15 local dry-run files and three local OCI image archives are generated; no signing, no pushing, and no git tags are created. Only when `RELEASE_DRYRUN_SHA256SUMS=PASS`, `RELEASE_DRYRUN_ASSET_COUNT=15`, `RELEASE_DRYRUN_IMAGES=PASS` and `RELEASE_DRYRUN_RESULT=PASS_UNSIGNED_NOT_FOR_RELEASE` appear at the same time can it be called **local unsigned walkthrough through**. For the official release gate, see [RELEASE_PROCESS.md](RELEASE_PROCESS.md) and [ release date list ](release-day-checklist-v0.5.md).

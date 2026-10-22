# AgentSQL v0.5.0 final execution list on release day

**Language:** [English](release-day-checklist-v0.5.md) | [Simplified Chinese](../release-day-checklist-v0.5.md)

> Gate time: 2026-10-16 16:00 CST (Beijing time, UTC+08:00)
>
> Historical verification batches: Batch 33, 2026-10-04, baseline submission `e8777079a342dd9d92c29989e0cd37e35846553e`; Batch 31 released project evidence will continue to be retained
>
> Batch thirty-three boundaries: not signed, not created tag, not pushed, not created Release, not pushed image, not accessed the release site; no git write operation was performed.

> Notes at the end of the document: The P99 review of Batch 61, the dry-run script of Batch 62, and the test source code of Batch 67 have been included in the gate to be executed on the release date below; the "Batch 31 Status/Output" in Sections 2 and 3 are retained as historical evidence and do not mean that the current product has passed.

## 1. Batch 33 records and subsequent document updates (Batch 31 release project evidence retained)

- The unique executable definitions for 15 GitHub Release assets are located at`scripts/release-dryrun.ps1`of`Get-ExpectedAssetNames`;This article and`docs/release-notes-v0.5.md`Align with it.
- Both `scripts/build-release-linux.sh` and `scripts/package-release.sh` only accept `amd64` / `arm64`. The former requires the native architecture of the container to be consistent with `ARCH`, so arm64 relies on the native arm64 runner or Docker/QEMU; the network ban probe of the native Docker/QEMU has reported `x86_64` and `aarch64` respectively.
- `go.mod` is Go 1.26; the official script now pins Go 1.26.8 and sets `GOTOOLCHAIN=local`. The development default value of `Makefile` is still the historical `1.25.14`, and the scope of changes in this batch has not been modified; this default value must not be relied on on the release date. If you manually call make target, you must explicitly pass `GO_VERSION=1.26.8`.
- The existing `dist/release-dryrun/v0.5.0` comes from the old commit `02565f5...`, and Go metadata is missing `yashandb-go v1.4.4`. `-ValidateOnly` returns FAIL and old assets cannot be reused. Current rules require that both tars contain `lib/yashandb/libyascli.so` and `lib/yashandb/libyas_infra.so`.
- Batch 31 skipped the external network according to the red line of the mission at that time. The complete build of this blocking candidate is blocked because `dnf`, go.dev, Alibaba Cloud Go image, GitHub Raw YashanDB client and BuildKit basic image metadata all require external network; it cannot be written as PASS.
- The locally cached `golang:1.26-bookworm` is measured to be Go 1.26.8 amd64, but there is no arm64 platform image; the native release script must be downloaded and verified as a fixed version on the release date of the two architecture Go tarballs, and cannot be replaced by this batch of cache.
- The three old OCI archives do have amd64/arm64 descriptors, but the platform image digests of the three are exactly the same; the old `quickstart` just changed the tag of the main image and lacked the demo configuration required by Compose. The release script now uses the same Buildx Bake diagram to build the demo base and the quickstart target that superimposes two configurations, and actually parses the OCI descriptor.
- `scripts/build-ghcr-multiarch.ps1 -Push` now only pushes the precise version and the optional auxiliary tag, and `latest` will no longer be moved at the same time; after the precise tag anonymous dual architecture is accepted, `-PromoteLatest` will be executed separately.
- `scripts/release-dryrun.ps1 -ProductionPrepare` provides an executable assembly path for production candidates: it requires a completely clean working tree and a copy of the authorized public key, only generates 11 files to be signed, does not read the private key, and does not generate a signature. 15 items are formed after completing two content signatures, `SHA256SUMS` and its signature.
- The release notes for R005 were lagging behind batch seventeen submission `02565f5`. The code has covered ordinary production dynamic EXPLAIN alarms, general execution actual truncation alarms and PostgreSQL column-level authorization actual truncation alarms; the structured hit is located at `assessment.hits`, and the same hit is persisted to audit `rule_hits` and can be queried by the management audit API. Batch 33 has corrected the document and no longer lists this item as open P0.
- Only 3/5 rounds of Batch 33 records of PostgreSQL parser P99 meet P99 ≤ 5 ms; Batch 61 passed 5/5 of the two groups in the specified Go 1.26.8 Linux/amd64 environment, and the review group P99 was 1.917/2.075/1.994/2.011/1.911 ms. The latter pass does not erase the history of failures, nor does it replace five consecutive rounds of exclusive retesting on the standard Linux runner on the day of release; the average benchmark may not be used to replace the tail delay gate or relax the threshold.

## 2. 15 Asset Checklist

`SHA256SUMS` covers 13 other assets besides itself and `SHA256SUMS.sig.json`; there is also an independent payload `SHA256SUMS` in the two tars.

| # | assets | Source | Release date verification | Batch thirty-one status |
| ---: | --- | --- | --- | --- |
| 1 | `agentsql-v0.5.0-linux-amd64.tar.gz` | Rocky 8 amd64：`build-release-linux.sh` → `package-release.sh` | Outer sidecar; full file SHA-256 in the package; ELF x86-64; GLIBC ≤ 2.28; binary version; contains two copies of Yashan C required libraries | **blocks**: The current construction requires an external network, and old packets must be rechecked according to the current rules. |
| 2 | `agentsql-v0.5.0-linux-amd64.tar.gz.sha256` | `package-release.sh` | Strict `64hex␠␠filename`, recalculate matching | **blocks**: rebuild with #1 |
| 3 | `agentsql-v0.5.0-linux-arm64.tar.gz` | Rocky 8 arm64/QEMU: Same as above | Outer/in-package SHA-256; ELF AArch64; GLIBC ≤ 2.28; version; contains two copies of Yashan C required libraries | **blocks**: The current construction requires an external network, and old packets must be rechecked according to the current rules. |
| 4 | `agentsql-v0.5.0-linux-arm64.tar.gz.sha256` | `package-release.sh` | Same as #2 | **blocks**: rebuild with #3 |
| 5 | `agentsql-v0.5.0.spdx.json` | Fixed digest Syft pair of sealed modules + four binary builds | SPDX JSON parsable; namespace not dry-run; package/relationship record | **blocks**: Waiting for current dual-architecture binary |
| 6 | `agentsql-v0.5.0.spdx.json.sig.json` | `scripts/releasesign/main.go -mode sign` | `-mode verify` Passes for SBOM raw bytes | **Signatures are prohibited in this batch** |
| 7 | `ed25519-release-public-key.json` | Official Ed25519 release key export | schema/algorithm/keyClass/keyId; all three signatures use the same key ID | **This batch is prohibited from generating official keys** |
| 8 | `go-version-metadata.txt` | `go version -m`, four Linux binaries merged | Both architectures exist; `agentsql` contains `yashandb-go v1.4.4` | **blocked**: old file missing Yashan |
| 9 | `install.sh` | sealed commit `scripts/install.sh` | Consistent with source code SHA-256; `sh -n`; offline `--from` installation | Source file amd64/arm64 `sh -n` **PASS**; actual installation is waiting for new packages |
| 10 | `provenance.json` | `-ProductionPrepare` | version/commit/source epoch/input hash is correct; not dry-run; `dirtyAtBuild=false` | The command has been completed; production preparation has not been executed. |
| 11 | `provenance.json.sig.json` | Formal signing tool | `-mode verify` on provenance raw bytes passed | **Signatures are prohibited in this batch** |
| 12 | `SBOM-GENERATION.txt` | `-ProductionPrepare` | Fixed the number of Syft digest, epoch, package/relationship to be consistent with the actual number; dry-run must not be written | Command completed; not generated |
| 13 | `SHA256SUMS` | Generate another 13 items after signing #6/#11 | Exactly 13 unique basenames; item-by-item recalculation passes | Wait #1–#12/#15 |
| 14 | `SHA256SUMS.sig.json` | Formal signature tool, final signature | `-mode verify` Passes the raw bytes of the final manifest | **Signatures are prohibited in this batch** |
| 15 | `VERIFYING-SIGNATURES.md` | `-ProductionPrepare` | The command points to three real signatures of v0.5.0; no dry-run statement is allowed | Command completed; not generated |

## 3. Batch 31 real command and output summary

The following only records the commands that affect the release decision; read-only commands such as `Get-Content` / `git grep` used for research are not used as gate evidence.

| command | True output/judgment |
| --- | --- |
| `git branch --show-current`；`git show -s 33f2d56` | `feature/v0.4`; HEAD/front are both `33f2d568...` |
| `docker version`；`docker buildx version` | client/server `29.8.0`；Buildx `v0.37.1` |
| `docker run --rm --pull never --network none --platform linux/{amd64,arm64} rockylinux:8 ... uname -m` | `x86_64` / `aarch64`, two architectures operating capabilities exist |
| `./scripts/release-dryrun.ps1 -ValidateOnly -AssetsDirectory dist/release-dryrun/v0.5.0/assets` | **FAIL**：`go-version-metadata.txt does not include yashandb-go v1.4.4.` |
| `tar -tzf` Check old amd64/arm64 tar | Neither package has an entry for `lib/yashandb/` and is therefore not a current candidate. |
| Disable the network and run the old local `agentsql:v0.5.0` and check the Yashan path | The version is `v0.5.0`, but both libraries are `No such file or directory` and cannot be used as current candidates; new images must be re-verified. |
| Parse three old OCI `index.json` with nested index | Each copy has `linux/amd64`, `linux/arm64` and two copies of attestation; the three archived platform manifest digests are amd64 `774548d1...`, arm64 `97cb882b...`, proving that the old auxiliary tag does not form a quickstart layer |
| Current working tree `docker buildx build --network none --pull=false --target quickstart ...` | **is incomplete/non-compliant**: `resolve image config` / `load metadata for docker.io/...` still appears; the client PID is terminated after command line verification, and the temporary directory has been cleaned |
| Two-architecture Rocky network-banned container execution `sh -n scripts/{build-release-linux.sh,package-release.sh,install.sh,push-release-image.sh}` | amd64 `SHELL_SYNTAX=PASS`；arm64 `SHELL_SYNTAX=PASS` |
| Go 1.26.8 Disabled network container `GOTOOLCHAIN=local GOPROXY=off go build ./scripts/releasesign` | exit 0; only compile, no key generated, no signature |
| PowerShell AST parses two copies of `.ps1`; `git diff --check` | PASS |

## 4. Release day serial execution list

All commands are executed in the new working directory of the sealed commit. When encountering unknown output, name inconsistency, dirty worktree, summary inconsistency, single schema missing, or any non-zero command, stop immediately; tag/push must not continue.

### A. Credentials and environment (only check, not build)

1. The person in charge confirmed: GitHub `repo` and `write:packages` permissions, GHCR package management rights, official Ed25519 private key placement strategy, official website deployment credentials, and PVR setting access rights.
2. Ready for amd64 + arm64/QEMU, Docker/Buildx, PowerShell, Go 1.26.8, Node + Edge (PDF), and outbound HTTPS. `go.mod` is `go 1.26.0`; the release script fixed `GOTOOLCHAIN=local`, disabling silent downloading of another toolchain.
3. Use a new clone, check and archive the commit:

```powershell
git switch feature/v0.4
git rev-parse HEAD
git status --porcelain
git diff --check
```

Acceptance: `status` has no output; HEAD is the final approval submission and may not still be the baseline `33f2d56` for this batch.

### B. Code and Dual Architecture Build Gate

```powershell
docker run --rm --platform linux/amd64 -e GOMAXPROCS=8 -e GOTOOLCHAIN=local -v "${PWD}:/src:ro" -w /src golang:1.26.8-bookworm /usr/local/go/bin/go test ./internal/parser -run '^TestParseProjectionLineageP99Budget$/^postgres$' -count=5 -v
docker run --rm --platform linux/amd64 -e GOTOOLCHAIN=local -v "${PWD}:/src:ro" -w /src golang:1.26.8-bookworm /usr/local/go/bin/go test -short ./internal/rules ./internal/pipeline -count=1
docker run --rm --platform linux/amd64 -v "${PWD}:/src" -w /src golang:1.26.8-bookworm sh -c 'GOTOOLCHAIN=local go build ./... && GOTOOLCHAIN=local go build -tags=yashan ./...'
pwsh ./scripts/release-dryrun.ps1 -Version v0.5.0 -OutputDirectory dist/release-dryrun/v0.5.0-final
```

Acceptance: The parser command is PASS for 5 consecutive rounds and each round has P99 ≤ 5 ms. If any round fails, it stops immediately; rules/pipeline short tests PASS where R005 is located; Go build PASS twice; dry-run outputs `RELEASE_DRYRUN_ASSET_COUNT=15`, `RELEASE_DRYRUN_SHA256SUMS=PASS`, `RELEASE_DRYRUN_YASHAN_REDISTRIBUTION=INCLUDED_DRIVER_v1.4.4_CLIENT_23.4.7.100`, `RELEASE_DRYRUN_IMAGES=PASS`, and `PASS_UNSIGNED_NOT_FOR_RELEASE`. Both tars must check for `lib/yashandb/libyascli.so` and `lib/yashandb/libyas_infra.so`; the image must check for `/opt/yashandb-client/lib` and `LD_LIBRARY_PATH`. Save the complete log item by item. Uploading of dry-run placeholder signatures is prohibited.

Supplementary test gate (execution on release day): According to the [ test coverage instructions ](test-coverage-notes.md), check the parser malformed input/concurrency isolation and the real exit code of the pipeline rejection test before execution, and complete the full short tests. The test package compilation of `internal/pipeline` involves testcontainers dependency; when the module cache, CGO tool chain or Docker E2E environment is missing, "Incomplete/Not Executed" is recorded. Directed assertions or short tests must not be used to replace the full repository and real repository results. Batch 61 will only get three packages of PASS for parser/rules/pipeline, and all short positions are not completed.

### C. Formal Public Key, Production Candidate and 15 Assets

Generate the official key of this version in a controlled directory; the private key path must be outside the repository and output directory, and be kept or destroyed according to the person in charge's policy:

```powershell
$Key = 'X:\secure\agentsql-v0.5.0-ed25519.seed'
$Public = 'X:\secure\ed25519-release-public-key-v0.5.0.json'
go run ./scripts/releasesign/main.go -mode generate -key $Key -public $Public
pwsh ./scripts/release-dryrun.ps1 -Version v0.5.0 -ProductionPrepare -PublicKeyPath $Public -OutputDirectory dist/release-prepare/v0.5.0-final
$Assets = (Resolve-Path 'dist/release-prepare/v0.5.0-final/assets').Path
```

Acceptance: The script outputs `RELEASE_PREPARE_UNSIGNED_ASSET_COUNT=11`, `PASS_UNSIGNED_REQUIRES_SIGNING`, provenance commit equals sealed HEAD, `dirtyAtBuild=false`, and all instructions do not contain dry-run copy.

Sign the two content files, generate a manifest covering the remaining 13 items, and finally sign the manifest:

```powershell
go run ./scripts/releasesign/main.go -mode sign -key $Key -input "$Assets/agentsql-v0.5.0.spdx.json" -signature "$Assets/agentsql-v0.5.0.spdx.json.sig.json"
go run ./scripts/releasesign/main.go -mode sign -key $Key -input "$Assets/provenance.json" -signature "$Assets/provenance.json.sig.json"
$ManifestNames = @(
  'agentsql-v0.5.0-linux-amd64.tar.gz','agentsql-v0.5.0-linux-amd64.tar.gz.sha256',
  'agentsql-v0.5.0-linux-arm64.tar.gz','agentsql-v0.5.0-linux-arm64.tar.gz.sha256',
  'agentsql-v0.5.0.spdx.json','agentsql-v0.5.0.spdx.json.sig.json',
  'ed25519-release-public-key.json','go-version-metadata.txt','install.sh',
  'provenance.json','provenance.json.sig.json','SBOM-GENERATION.txt','VERIFYING-SIGNATURES.md'
) | Sort-Object
$Lines = foreach ($Name in $ManifestNames) {
  $Hash = (Get-FileHash -LiteralPath (Join-Path $Assets $Name) -Algorithm SHA256).Hash.ToLowerInvariant()
  "$Hash  $Name"
}
[IO.File]::WriteAllText((Join-Path $Assets 'SHA256SUMS'), (($Lines -join "`n") + "`n"), (New-Object Text.UTF8Encoding($false)))
go run ./scripts/releasesign/main.go -mode sign -key $Key -input "$Assets/SHA256SUMS" -signature "$Assets/SHA256SUMS.sig.json"
```

Final signature verification and topology verification:

```powershell
go run ./scripts/releasesign/main.go -mode verify -public "$Assets/ed25519-release-public-key.json" -input "$Assets/agentsql-v0.5.0.spdx.json" -signature "$Assets/agentsql-v0.5.0.spdx.json.sig.json"
go run ./scripts/releasesign/main.go -mode verify -public "$Assets/ed25519-release-public-key.json" -input "$Assets/provenance.json" -signature "$Assets/provenance.json.sig.json"
go run ./scripts/releasesign/main.go -mode verify -public "$Assets/ed25519-release-public-key.json" -input "$Assets/SHA256SUMS" -signature "$Assets/SHA256SUMS.sig.json"
pwsh ./scripts/release-dryrun.ps1 -Version v0.5.0 -ValidateOnly -AssetsDirectory $Assets
```

Acceptance: three times `verified`; `RELEASE_DRYRUN_ASSET_COUNT=15`; the final directory contains exactly 15 files. `-ValidateOnly` only checks the signature structure and cannot replace the first three cryptographic signature verifications.

### D. tag, Release draft and download

This repository v0.4.0 uses annotated tag; v0.5.0 follows this type. This can only be executed when A–C are all green:

```powershell
$Commit = git rev-parse HEAD
git tag -a v0.5.0 $Commit -m 'AgentSQL v0.5.0'
git show -s --format='%H %s' 'v0.5.0^{}'
git push origin refs/tags/v0.5.0
$AssetFiles = @(Get-ChildItem -LiteralPath $Assets -File | Sort-Object Name)
if ($AssetFiles.Count -ne 15) { throw "expected 15 assets, got $($AssetFiles.Count)" }
$AssetPaths = @($AssetFiles.FullName)
gh release create v0.5.0 --draft --verify-tag --title 'AgentSQL v0.5.0' --notes-file docs/release-notes-v0.5.md $AssetPaths
```

Credentials/external network: Git push permission, GitHub Release write permission, GitHub external network. Acceptance: tag points to sealed commit; draft is unpublished; draft contains exactly 15 items. Then download to a new directory and repeat C's three signature verifications and `-ValidateOnly`. Any byte differences in downloading are blocked.

### E. GHCR precise tag, auxiliary image and latest

Authorize first and push only the precise tag; this step will not move `latest`:

```powershell
gh auth refresh -h github.com -s write:packages
pwsh ./scripts/build-ghcr-multiarch.ps1 -Version v0.5.0 -IncludeAuxiliaryTags -Push -DigestOutput dist/release-prepare/v0.5.0-final/image-digests-pushed.json
```

Acceptance: `v0.5.0`, `v0.5.0-demo`, and `v0.5.0-quickstart` are all public and contain linux/amd64 + linux/arm64; the digest JSON top level `pushed=true` has three tag records. Pull the exact tag in two clean amd64/arm64 environments without GHCR login. The main image must be started and `/healthz` reports that `v0.5.0` and `/readyz` are ready; the quickstart tag must also pass:

```bash
docker run --rm --entrypoint sh ghcr.io/cuipengdba/agentsql:v0.5.0-quickstart -c 'test -f /etc/agentsql/config.demo.yaml && test -f /etc/agentsql/demo-seed.yaml && test "$(agentsql --version)" = v0.5.0'
```

Press `deploy/quickstart/docker-compose.yml` again to complete the seed → gateway healthy link and `pwsh ./demo/reset.ps1`. Move `latest` after all precise tag gates have passed:

```powershell
pwsh ./scripts/build-ghcr-multiarch.ps1 -Version v0.5.0 -PromoteLatest
```

Acceptance: The script confirms that the top-level digests of `latest` and `v0.5.0` are the same; pull `latest` anonymously again and do a version/health check.

### F. Official website, official release and post-release

1. `node website/tools/build-docs-pdf.mjs` re-directs three copies of v0.5.0 PDF and checks the website version, Release link, robots and `/demo/` noindex boundaries.
2. Press `website/deploy/README-DEPLOY.md` to perform stage A; run `bash ./deploy/verify-static.sh http://127.0.0.1:8080 stage-a`. After filing, DNS, certificate and quarantine are in place, then stage B and run `bash ./deploy/verify-static.sh https://agentsql.cn stage-b`.
3. The repository administrator manually confirms that PVR is turned on in GitHub Security settings.
4. After all the above are green, make the GitHub Release draft official; then recheck `releases/latest`, 15 asset downloads/verifications/offline installation, three GHCR tags, official website, and Demo allow/deny/approval links from a clean environment without logging in.

## 5. Prohibition and blocking

- Uploading of the dry-run directory is prohibited; its `dryRun=true` / `signed=false` placeholder documents, dry-run SBOM namespace and description are not release assets.
- It is forbidden to move `latest` before the `v0.5.0` precise mirror anonymous dual architecture is accepted.
- It is prohibited to write this batch of old assets, old OCI descriptors or script syntax PASS as the current HEAD build PASS.
- On the release day, external networks are still required: Rocky/Syft/Go/YashanDB client acquisition and verification, GitHub/GHCR, PVR, official website deployment and public network acceptance.
- Credentials are still required on the release day: official Ed25519 key placement strategy, Git tag/push, GitHub Release, `write:packages`, GHCR visibility, official website host/DNS/certificate permissions.
- `scripts/release-dryrun.ps1` defines and verifies 15 assets, but the dry-run result is not a formal signature; on the release day, the C gate must be completed with the current sealed commit reconstruction, signature, three cryptographic verification signatures and `-ValidateOnly`. The FAIL of an old asset may not be used as a conclusion for a new asset.
- NoSQL registration/port, DM/Oracle and YashanDB dialect boundaries must be compared [NoSQL support range ](nosql-support.md), [DM/Oracle boundaries ](dm-oracle-dialect.md), [YashanDB boundary ](yashan-dialect.md) Complete release copy review; HBase must not be The registered default port is written as a ZooKeeper bootstrap port that can be directly connected, and the offline parser must not be used as a complete gateway.
- `Makefile`of`GO_VERSION ?= 1.25.14`Not an officially released value; use`scripts/release-dryrun.ps1` / `-ProductionPrepare`, or explicitly override make as`1.26.8`,otherwise`GOTOOLCHAIN=local`Will fail-closed.

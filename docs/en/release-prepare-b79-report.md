# Batch 79 ProductionPrepare and signed re-inspection report

**Language:** [English](release-prepare-b79-report.md) | [Simplified Chinese](../release-prepare-b79-report.md)

**status: 15 local candidate assets and Section C verification passed; one item remains in the release source audit.** time is 2026-10-09 Asia/Shanghai. No git add/commit/tag/merge/push, upload, or publish was performed in this batch.

## Gate repair and operation mode

`scripts/release-dryrun.ps1`Only in ProductionPrepare`$productionDirty`Query Lieutenant General`git status --porcelain`Change to`git status --porcelain --untracked-files=no`. Keep the exit code check and throw an error when dirty. After the formal script changes, the tracked status is` M scripts/release-dryrun.ps1`;Negative test throws with exit code 1`Production preparation requires a completely clean worktree`,See`dist/release-b79/tracked-dirty-gate.log`. The two protected untracked paths have not been added or modified.

The current machine does not have the `pwsh` command, use Windows PowerShell to execute the same `.ps1`. Since commits are prohibited in this batch and the gate must reject any tracked modifications, ProductionPrepare is executed from the temporary same-directory copy of `scripts/release-dryrun-b79-run.ps1` when the tracked workspace is clean; this copy only contains the above-mentioned one-line fix, is built to be exactly the same as the SHA-256 of the official repaired version, and is subsequently deleted. The passed parameter is `-Version v0.5.0 -ProductionPrepare -PublicKeyPath D:\ruanjiansheji\secure\ed25519-release-public-key-v0.5.0.json -OutputDirectory dist/release-prepare/v0.5.0-final`, and there is no `-SkipImages`. The complete log is `dist/release-b79/production-prepare.log`. The first attempt was interrupted by the log wrapper treating Docker's normal stderr as a termination error; the log and partial directories were saved at `dist/release-b79/production-prepare-attempt-1.log` and `failed-attempt-1/` and were not used for signing. The second run after fixing the wrapper exited with code 0 at 14:53:01.

The second output is `RELEASE_PREPARE_UNSIGNED_ASSET_COUNT=11`, `RELEASE_PREPARE_IMAGES=PASS`, `RELEASE_PREPARE_RESULT=PASS_UNSIGNED_REQUIRES_SIGNING`. `provenance.json` for `source.commit=a41955e87d2e603d6ba4bd2dcd554f2665e86931`, `dirtyAtBuild=false`, documentation and provenance without walkthrough statements such as `DRY RUN ONLY`. `release-dryrun.ps1` appears in the production instructions only as the verification tool file name.

## Final 15 assets

Directory: `dist/release-prepare/v0.5.0-final/assets/`, exactly 15 normal files, no extra files or subdirectories.

| # | file name | Bytes |
| ---: | --- | ---: |
| 1 | `agentsql-v0.5.0-linux-amd64.tar.gz` | 103724113 |
| 2 | `agentsql-v0.5.0-linux-amd64.tar.gz.sha256` | 101 |
| 3 | `agentsql-v0.5.0-linux-arm64.tar.gz` | 94562868 |
| 4 | `agentsql-v0.5.0-linux-arm64.tar.gz.sha256` | 101 |
| 5 | `agentsql-v0.5.0.spdx.json` | 2852836 |
| 6 | `agentsql-v0.5.0.spdx.json.sig.json` | 451 |
| 7 | `ed25519-release-public-key.json` | 230 |
| 8 | `go-version-metadata.txt` | 50348 |
| 9 | `install.sh` | 46844 |
| 10 | `provenance.json` | 6495 |
| 11 | `provenance.json.sig.json` | 441 |
| 12 | `SBOM-GENERATION.txt` | 570 |
| 13 | `SHA256SUMS` | 1225 |
| 14 | `SHA256SUMS.sig.json` | 436 |
| 15 | `VERIFYING-SIGNATURES.md` | 1258 |

The signature sequence is SPDX, provenance, generate `SHA256SUMS` covering the other 13 items, sign `SHA256SUMS`. The official signer compiled using the repository `scripts/releasesign/main.go` does not use the development signature tool; `-mode verify` outputs `verified` three times, and the same key ID is `sha256:771e8b179a031f0e3ac3e09576979532808e39f5f2e6da9d103ddf24c91d6868`. `-ValidateOnly -AssetsDirectory` returns `RELEASE_DRYRUN_ASSET_COUNT=15`, `RELEASE_DRYRUN_SHA256SUMS=PASS`, `RELEASE_DRYRUN_ITEM_FAIL_COUNT=0`. Full output at `dist/release-b79/sign-and-verify.log`.

Simulate back to download and copy the final 15 files to `dist/release-b79/redownload/assets/` item by item, and the SHA-256 recalculated one by one is equal to the original; the copied `SHA256SUMS` has exactly 13 unique basenames, and the recalculated item by item all match. Record: `dist/release-b79/redownload-verify.log`, result `COPIED_ASSETS=15 MANIFEST_MATCH=13 PASS`. This is a local replication simulation without access to the remote Release.

## Dual-architecture tar evidence

directly from**Two tars in this batch**Each extraction`agentsql`、`agentsqlctl`, read the ELF header`e_machine`, the results are as follows; the log is`dist/release-b79/architecture-evidence.log`。

| Time (+08:00) | tar platform | binary | ELF `e_machine` | result |
| --- | --- | --- | ---: | --- |
| 14:53:44 | linux/amd64 | `agentsql` | 62（x86-64） | PASS |
| 14:53:46 | linux/amd64 | `agentsqlctl` | 62（x86-64） | PASS |
| 14:53:48 | linux/arm64 | `agentsql` | 183（AArch64） | PASS |
| 14:53:49 | linux/arm64 | `agentsqlctl` | 183（AArch64） | PASS |

`-ValidateOnly` also passes the YashanDB required library check of two tars; the ProductionPrepare local multi-architecture OCI image check also outputs `RELEASE_PREPARE_IMAGES=PASS`. There is no GHCR push or public pull for this batch.

## Key custody and legacy issues

Reuse the existing private key seed `D:\ruanjiansheji\secure\agentsql-v0.5.0-ed25519.seed` and public key document `D:\ruanjiansheji\secure\ed25519-release-public-key-v0.5.0.json`, both of which are still outside the repository `D:\ruanjiansheji\agentsql-v04`; the file time remains 12:50:33 and is not regenerated. The copy of the public key in the asset is the same SHA-256 as the secure public key. The private key and license content are not printed or recorded, and the git status does not contain the secure path.

**source audit legacy:** sealed HEAD `scripts/release-dryrun.ps1` is the pre-fix version. The hash of the script in `provenance.inputs` is `d3857a4627bb63aa5ecb6d069e76cfd1e612a853cd9b6bf3f759886f9fdbc9b7`; the hash of the actual running interim repair version and the final workspace script is `f00dd95883d51c0f6be1184287b8bf0cc91d9f625ae255e3f9a60d90164e5cd0`. This batch of prohibited submission requirements cannot make the fix enter sealed HEAD, so although these assets pass the above content, signature and architecture verification, **cannot directly release** without eliminating this source difference. The next batch must incorporate the gate fix into a new blocking commit, rebuild, sign, verify, and update the provenance commit from that commit. Tag, Release, asset upload, remote download and GHCR public acceptance are also reserved for the next batch.

The protected `cmd/agentsql/b5-wal/` and `cmd/agentsql/﹎memory﹎.instance-id` are still untracked and not included in git; the original untracked `docs/release-prepare-b78-report.md` has not been changed. The new report in this batch is this file, and the only tracked file changes are `scripts/release-dryrun.ps1`. `git diff --check` passed.

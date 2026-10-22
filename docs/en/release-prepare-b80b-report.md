# Batch 80b: v0.5.0 Production Candidate Rebuild and Signature Report

**Language:** [English](release-prepare-b80b-report.md) | [Simplified Chinese](../release-prepare-b80b-report.md)

**result: All 7 goals of this batch passed.** This article was published on 2026-10-09 Asia/Shanghai. The build closure is `fe36c7cdecb9a68426727153ff2dc90487c40ca7`. The final asset is located at `dist/release-prepare/v0.5.0-final/assets/`, exactly 15 normal files, no extra files or subdirectories. The complete time-stamped evidence is located at `dist/release-b80b/`.

## Build and sign

Run Windows PowerShell version 5.1 `scripts/release-dryrun.ps1 -Version v0.5.0 -ProductionPrepare -PublicKeyPath D:\ruanjiansheji\secure\ed25519-release-public-key-v0.5.0.json -OutputDirectory dist/release-prepare/v0.5.0-final` directly from the repository. 19:38:55 Completed, exit code 0, output `RELEASE_PREPARE_UNSIGNED_ASSET_COUNT=11`, `RELEASE_PREPARE_IMAGES=PASS`, `RELEASE_PREPARE_RESULT=PASS_UNSIGNED_REQUIRES_SIGNING`. See `dist/release-b80b/production-prepare.log` for complete output. The old `a41955e` candidate has been moved to the `dist/release-b80b/superseded-a41955e/` archive and is not part of the final assets of this batch.

Use the official signer compiled in the repository `scripts/releasesign/main.go` and the existing Ed25519 seed outside the repository to sign SPDX and proveance in sequence, generate `SHA256SUMS` covering the remaining 13 items, and then sign `SHA256SUMS`. The three `-mode verify` at 19:39:14 all output `verified`, and the key ID is `sha256:771e8b179a031f0e3ac3e09576979532808e39f5f2e6da9d103ddf24c91d6868`. See `dist/release-b80b/sign-and-verify.log` for the complete output of signature and verification.

## Final 15 assets

| File | Bytes |
| --- | ---: |
| `agentsql-v0.5.0-linux-amd64.tar.gz` | 103730667 |
| `agentsql-v0.5.0-linux-amd64.tar.gz.sha256` | 101 |
| `agentsql-v0.5.0-linux-arm64.tar.gz` | 94569304 |
| `agentsql-v0.5.0-linux-arm64.tar.gz.sha256` | 101 |
| `agentsql-v0.5.0.spdx.json` | 2852836 |
| `agentsql-v0.5.0.spdx.json.sig.json` | 451 |
| `ed25519-release-public-key.json` | 230 |
| `go-version-metadata.txt` | 50348 |
| `install.sh` | 46844 |
| `provenance.json` | 6495 |
| `provenance.json.sig.json` | 441 |
| `SBOM-GENERATION.txt` | 570 |
| `SHA256SUMS` | 1225 |
| `SHA256SUMS.sig.json` | 436 |
| `VERIFYING-SIGNATURES.md` | 1258 |

## verification evidence

- 19:39:15–19:39:30 Execute `-ValidateOnly -AssetsDirectory`, exit code 0, output `RELEASE_DRYRUN_ASSET_COUNT=15`, `RELEASE_DRYRUN_SHA256SUMS=PASS`, `RELEASE_DRYRUN_ITEM_FAIL_COUNT=0`; see `dist/release-b80b/validation.log` for complete item-by-item output.
- 19:39:32–19:39:39 Extract `agentsql` and `agentsqlctl` from each of the two tars in this batch and read the ELF header: amd64, both items are decimal `e_machine=62`, and arm64, both items are decimal `e_machine=183`. Both tars contain `lib/yashandb/libyascli.so` and `lib/yashandb/libyas_infra.so`. See `dist/release-b80b/architecture-evidence.log` for itemized time-stamped evidence.
- 19:39:40–19:39:44 Copied 15 files item by item to new `dist/release-b80b/redownload/assets/`, SHA-256 consistent with the original after each copy; `SHA256SUMS` of the copy has exactly 13 unique basenames, item by item hash matches. Output `COPIED_ASSETS=15 MANIFEST_MATCH=13 PASS`, see `dist/release-b80b/redownload-verify.log`. Release uploading is prohibited in this batch. This is a local download simulation.

## Source audit reconciliation

`provenance.json`of`source.commit=fe36c7cdecb9a68426727153ff2dc90487c40ca7`、`source.dirtyAtBuild=false`, there is no drill mode declaration. Must appear`scripts/release-dryrun.ps1`The input file name is not part of the walkthrough text.

| input file | Current block expected SHA-256 | Workspace file SHA-256 | provenance.inputs SHA-256 | result |
| --- | --- | --- | --- | --- |
| `scripts/release-dryrun.ps1` | `f00dd95883d51c0f6be1184287b8bf0cc91d9f625ae255e3f9a60d90164e5cd0` | `f00dd95883d51c0f6be1184287b8bf0cc91d9f625ae255e3f9a60d90164e5cd0` | `f00dd95883d51c0f6be1184287b8bf0cc91d9f625ae255e3f9a60d90164e5cd0` | PASS |

The corresponding input hash for the old `a41955e` candidate was `d3857a4627bb63aa5ecb6d069e76cfd1e612a853cd9b6bf3f759886f9fdbc9b7`; this difference has been eliminated with this new batch of assets. See `dist/release-b80b/source-audit-reconciliation.log` for the original timestamped table.

## Keys, repository boundaries and legacy

The reused private key seed and public key document are still located outside the repository at `D:\ruanjiansheji\secure\agentsql-v0.5.0-ed25519.seed` and `D:\ruanjiansheji\secure\ed25519-release-public-key-v0.5.0.json` respectively; only the existence and hash of the exported public key copy are checked, the key or license content is not printed, and the key is not regenerated. The tracked work tree is empty, `git diff --check` passed; `cmd/agentsql/b5-wal/` and `cmd/agentsql/﹎memory﹎.instance-id` are not tracked or touched, and there is no secure path in git status.

This batch does not perform git write operations, Release creation or upload, GHCR push, official website deployment and release promotion. The remaining work is only the tag, release, remote asset download, public network GHCR and official website release and acceptance after subsequent batches are authorized; the old `a41955e` is retained and cannot be released.

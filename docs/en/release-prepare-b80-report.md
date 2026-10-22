# Batch 80 new block reconstruction pre-check report (blocking)

**Language:** [English](release-prepare-b80-report.md) | [Simplified Chinese](../release-prepare-b80-report.md)

Status: **is not completed, and it is prohibited to publish the existing asset**. Pre-inspection time: 2026-10-09 16:21:02 +08:00 (Asia/Shanghai). This batch did not run ProductionPrepare, Sign, ValidateOnly, Schema Extraction or Download Revalidation, nor did it perform any upload or publish operations. The following table only records the old assets left in the target directory in the previous batch. **is not the batch80 reconstruction product**.

## Blocking reason

This batch requires both "directly running formal ProductionPrepare" and "complete all locally, not connected to the Internet". The existing official script cannot be completed without the Internet: `scripts/release-dryrun.ps1` first executes `dnf install` in the Docker container of the two architectures, and then calls `scripts/build-release-linux.sh`; the latter unconditionally accesses the YashanDB client URLs of `go.dev`, `mirrors.aliyun.com` and GitHub, and runs `go mod download`. Image building also resolves the image through Buildx. `dist/release-b79/production-prepare.log` records the actual process of accessing the Rocky/Debian image source, downloading dependencies, and parsing the Docker Hub image in the previous batch. The repository has no parameters to enable this formal command to be completed offline. To comply with the no-networking red line for this batch, the preflight was stopped before executing the command; no network connection was attempted.

There is also no `pwsh` executable on this machine; the previous batch used Windows PowerShell to execute the same `.ps1`. This difference has not yet entered the batch80 build because the no-networking condition has been blocked first.

## Source audit pre-check

| Project | SHA-256 / value |
| --- | --- |
| Current HEAD | `f40d2441f042430745cb2a3eeb887aa07f5234ff` |
| Current HEAD workspace `scripts/release-dryrun.ps1` (no changes to tracking files) | `f00dd95883d51c0f6be1184287b8bf0cc91d9f625ae255e3f9a60d90164e5cd0` |
| `source.commit` of existing `provenance.json` | `a41955e87d2e603d6ba4bd2dcd554f2665e86931` |
| Existing `provenance.inputs` `scripts/release-dryrun.ps1` | `d3857a4627bb63aa5ecb6d069e76cfd1e612a853cd9b6bf3f759886f9fdbc9b7` |

Conclusion: Currently the two script hashes are not equal, and the source audit difference **still exists**. The old catalog `dist/release-prepare/v0.5.0-final/assets/` remains intact, no source statements have been re-signed or rewritten.

## There are 15 old assets in the target directory (not deliverable as batch80)

| File | Bytes |
| --- | ---: |
| `agentsql-v0.5.0-linux-amd64.tar.gz` | 103724113 |
| `agentsql-v0.5.0-linux-amd64.tar.gz.sha256` | 101 |
| `agentsql-v0.5.0-linux-arm64.tar.gz` | 94562868 |
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

There are exactly 15 normal files in the existing directory, but their submission sources are old covers. Previous batch verification, ValidateOnly, ELF architecture, and downloaded evidence are not counted as batch80 acceptance. The four logs corresponding to this batch are located at `dist/release-b80/`, which clearly records that each step has not been executed; there is no successful operation of `RELEASE_PREPARE_*`, three successful operations of `verified`, `RELEASE_DRYRUN_*` or `COPIED_ASSETS=15 MANIFEST_MATCH=13 PASS`.

## Keys, workspaces and legacy issues

The private key path `D:\ruanjiansheji\secure\agentsql-v0.5.0-ed25519.seed` and the public key source path `D:\ruanjiansheji\secure\ed25519-release-public-key-v0.5.0.json` are both outside the repository and only check for existence; the private key and license content are not read, printed or recorded. `git status --porcelain --untracked-files=no` is empty, `git diff --check` passes; the protected `cmd/agentsql/b5-wal/` and `cmd/agentsql/﹎memory﹎.instance-id` remain untracked and untouched, and the git status has no secure content.

Remaining issues: You need to provide an approved offline build process and a full local dependency/image cache, or explicitly adjust the "no network" constraint before re-executing ProductionPrepare and the rest of the batch acceptance from `f40d244`. Existing catalogs may not be previously published, nor may old assets be rewritten as new cover sources.

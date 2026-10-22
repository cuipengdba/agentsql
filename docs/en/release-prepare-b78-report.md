# Batch 78 Release Asset Preparation Report

**Language:** [English](release-prepare-b78-report.md) | [Simplified Chinese](../release-prepare-b78-report.md)

**Conclusion: FAIL / Stopped.** 2026-10-09 12:51 (Asia/Shanghai), Official key generated, but production candidate script exited 1 on clean workspace check before build. Release assets are not generated, signed, or verified. This report cannot be used as a basis for batch79 release.

## Keys and evidence of execution

- Private key seed: `D:\ruanjiansheji\secure\agentsql-v0.5.0-ed25519.seed`, outside the repository, 44 bytes; key content is not recorded.
- Public key JSON: `D:\ruanjiansheji\secure\ed25519-release-public-key-v0.5.0.json`, outside the repository, 230 bytes; keyId is `sha256:771e8b179a031f0e3ac3e09576979532808e39f5f2e6da9d103ddf24c91d6868`.
- The creation time of both files is 2026-10-09 12:50:33 (Asia/Shanghai). Path-level checks confirm that they are all outside repository `D:\ruanjiansheji\agentsql-v04`. The public key has not been copied into the product directory.
- Complete line-by-line timestamped script log: `dist/release-b78/production-prepare.log`. The command was `powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\release-dryrun.ps1 -Version v0.5.0 -ProductionPrepare -PublicKeyPath D:\ruanjiansheji\secure\ed25519-release-public-key-v0.5.0.json -OutputDirectory dist/release-prepare/v0.5.0-final`; exit code 1. The script internally reads only git status checks; no git write operations are performed in this batch.

## choke point

The script outputs `RELEASE_PREPARE_RESULT=FAIL` because the following two untracked paths exist in the workspace, and the script requires that it be completely clean before starting the build:

```text
?? cmd/agentsql/b5-wal/
?? "cmd/agentsql/\357\200\272memory\357\200\272.instance-id"
```

These two paths are explicitly prohibited from being touched in this batch. No ignore rules have been deleted, moved, modified, or added; no clean workspace gates for scripts have been bypassed. `dist/release-prepare/v0.5.0-final` was not created. The abbreviation of sealed HEAD given by the user is `a41955e`; the script exited before reading the provenance commit, so there is no provenance file to verify in this batch.

## Authority 15 Asset List and Actual Status

The following are all expected names of **,** was not generated; the final directory does not exist, and the actual number of assets is 0.

| # | file name | Status |
| ---: | --- | --- |
| 1 | `agentsql-v0.5.0-linux-amd64.tar.gz` | Not generated |
| 2 | `agentsql-v0.5.0-linux-amd64.tar.gz.sha256` | Not generated |
| 3 | `agentsql-v0.5.0-linux-arm64.tar.gz` | Not generated |
| 4 | `agentsql-v0.5.0-linux-arm64.tar.gz.sha256` | Not generated |
| 5 | `agentsql-v0.5.0.spdx.json` | Not generated |
| 6 | `agentsql-v0.5.0.spdx.json.sig.json` | Not generated |
| 7 | `ed25519-release-public-key.json` | Not generated |
| 8 | `go-version-metadata.txt` | Not generated |
| 9 | `install.sh` | Not generated |
| 10 | `provenance.json` | Not generated |
| 11 | `provenance.json.sig.json` | Not generated |
| 12 | `SBOM-GENERATION.txt` | Not generated |
| 13 | `SHA256SUMS` | Not generated |
| 14 | `SHA256SUMS.sig.json` | Not generated |
| 15 | `VERIFYING-SIGNATURES.md` | Not generated |

## Acceptance results and remaining issues

- `RELEASE_PREPARE_UNSIGNED_ASSET_COUNT=11`, `PASS_UNSIGNED_REQUIRES_SIGNING`: Not reached.
- `provenance.source.commit`, `dirtyAtBuild=false`, description file has no dry-run copy: No file can be determined.
- SHA256SUMS 13 item-by-item comparison: not executed; no verifiable manifest.
- SPDX, provenance, SHA256SUMS three Ed25519 signature verifications: none were executed and `verified` cannot be reported.
- `-ValidateOnly`of`RELEASE_DRYRUN_ASSET_COUNT=15`: Not executed; 15 asset catalog does not exist.
- Binary architectures in linux/amd64 and linux/arm64 tar: There is no evidence of this batch of builds, and they cannot be replaced by old batches of products.
- Simulation back download: not executed; no final assets to copy or recalculate.

To continue Section C, an owner authorized to handle the two protected paths above must first provide a **verified clean working tree**. Then rerun production preparation, sign in Section C's order and reverify every item. Keep the existing official private key under controlled storage outside the repository. This batch did not tag, push, upload or publish.

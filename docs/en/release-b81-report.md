# Batch 81: v0.5.0 annotated tag, Release draft and remote asset re-inspection report

**Language:** [English](release-b81-report.md) | [Simplified Chinese](../release-b81-report.md)

**result: VERIFIED; Release remains draft and has not been converted to official.** was recorded on 2026-10-09 Asia/Shanghai. The complete timestamped logs are located at `dist/release-b81/tag-and-push.log`, `dist/release-b81/release-draft.log` and `dist/release-b81/redownload-verify.log`.

## Preconditions and tags

- The batch80b report has been submitted with `783b008fa9ca381f71509d5079af742933348717` before tag; this commit only adds `docs/release-prepare-b80b-report.md`, and its direct parent commit is the actual build commit `fe36c7cdecb9a68426727153ff2dc90487c40ca7`.
- The authoritative directory `dist/release-prepare/v0.5.0-final/assets/` contains exactly 15 files. `source.commit=fe36c7cdecb9a68426727153ff2dc90487c40ca7` and `dirtyAtBuild=false` in `provenance.json`; the SHA-256 of `scripts/release-dryrun.ps1` in `provenance.inputs` is `f00dd95883d51c0f6be1184287b8bf0cc91d9f625ae255e3f9a60d90164e5cd0`, which is consistent with the workspace script.
- The tracked working tree is clean before creation; only the untracked paths are protected `cmd/agentsql/b5-wal/`, `cmd/agentsql/﹎memory﹎.instance-id`, and `dist/` is ignored. Before creation, the `v0.5.0` tag does not exist locally or remotely, and the corresponding Release does not exist on the remote end.
- `git cat-file -t v0.5.0` outputs `tag`, the annotated tag object is `f2330ec3e65d40acd87fc34211c1ba7d78659cdc`, and the dereference target is `783b008fa9ca381f71509d5079af742933348717`. `git push origin refs/tags/v0.5.0` exits with code 0; subsequently `git ls-remote --tags origin v0.5.0` returns the same tag object.

## Release draft and account

- `gh auth status` confirms that the account `cuipengdba` is logged in, and the token scopes include `repo`; the token is not recorded in the log.
- `gh release create v0.5.0 --draft --verify-tag` Using `docs/release-notes-v0.5.md`, upload 15 files from the authoritative directory. `gh release view` returns `isDraft=true`, `isPrerelease=false`, `tagName=v0.5.0`, the number of assets is 15, and the name and number of bytes are consistent with the original.
- Draft URL: `https://github.com/cuipengdba/agentsql/releases/tag/untagged-a50cac24fbb6b6a1e819`. The following is the draft asset download URL returned by GitHub; anonymous downloads are not available during drafts.

| Asset name | Bytes | Download URL (draft) |
| --- | ---: | --- |
| `agentsql-v0.5.0-linux-amd64.tar.gz` | 103730667 | https://github.com/cuipengdba/agentsql/releases/download/untagged-a50cac24fbb6b6a1e819/agentsql-v0.5.0-linux-amd64.tar.gz |
| `agentsql-v0.5.0-linux-amd64.tar.gz.sha256` | 101 | https://github.com/cuipengdba/agentsql/releases/download/untagged-a50cac24fbb6b6a1e819/agentsql-v0.5.0-linux-amd64.tar.gz.sha256 |
| `agentsql-v0.5.0-linux-arm64.tar.gz` | 94569304 | https://github.com/cuipengdba/agentsql/releases/download/untagged-a50cac24fbb6b6a1e819/agentsql-v0.5.0-linux-arm64.tar.gz |
| `agentsql-v0.5.0-linux-arm64.tar.gz.sha256` | 101 | https://github.com/cuipengdba/agentsql/releases/download/untagged-a50cac24fbb6b6a1e819/agentsql-v0.5.0-linux-arm64.tar.gz.sha256 |
| `agentsql-v0.5.0.spdx.json` | 2852836 | https://github.com/cuipengdba/agentsql/releases/download/untagged-a50cac24fbb6b6a1e819/agentsql-v0.5.0.spdx.json |
| `agentsql-v0.5.0.spdx.json.sig.json` | 451 | https://github.com/cuipengdba/agentsql/releases/download/untagged-a50cac24fbb6b6a1e819/agentsql-v0.5.0.spdx.json.sig.json |
| `ed25519-release-public-key.json` | 230 | https://github.com/cuipengdba/agentsql/releases/download/untagged-a50cac24fbb6b6a1e819/ed25519-release-public-key.json |
| `go-version-metadata.txt` | 50348 | https://github.com/cuipengdba/agentsql/releases/download/untagged-a50cac24fbb6b6a1e819/go-version-metadata.txt |
| `install.sh` | 46844 | https://github.com/cuipengdba/agentsql/releases/download/untagged-a50cac24fbb6b6a1e819/install.sh |
| `provenance.json` | 6495 | https://github.com/cuipengdba/agentsql/releases/download/untagged-a50cac24fbb6b6a1e819/provenance.json |
| `provenance.json.sig.json` | 441 | https://github.com/cuipengdba/agentsql/releases/download/untagged-a50cac24fbb6b6a1e819/provenance.json.sig.json |
| `SBOM-GENERATION.txt` | 570 | https://github.com/cuipengdba/agentsql/releases/download/untagged-a50cac24fbb6b6a1e819/SBOM-GENERATION.txt |
| `SHA256SUMS` | 1225 | https://github.com/cuipengdba/agentsql/releases/download/untagged-a50cac24fbb6b6a1e819/SHA256SUMS |
| `SHA256SUMS.sig.json` | 436 | https://github.com/cuipengdba/agentsql/releases/download/untagged-a50cac24fbb6b6a1e819/SHA256SUMS.sig.json |
| `VERIFYING-SIGNATURES.md` | 1258 | https://github.com/cuipengdba/agentsql/releases/download/untagged-a50cac24fbb6b6a1e819/VERIFYING-SIGNATURES.md |

## Return to download and verify

- `gh release download v0.5.0 --dir dist/release-b81/redownload --clobber` is done in a completely new directory, resulting in exactly 15 normal files. An item-by-item comparison of name, byte count, and SHA-256, 15/15 identical to the authoritative original; item-by-item hash see `redownload-verify.log`.
- Use the downloaded`ed25519-release-public-key.json`Copy of public key and batch80b compiled repository`scripts/releasesign/main.go`validator, respectively`agentsql-v0.5.0.spdx.json`、`provenance.json`、`SHA256SUMS`implement`-mode verify`: Output all three times`verified`, the key IDs are all`sha256:771e8b179a031f0e3ac3e09576979532808e39f5f2e6da9d103ddf24c91d6868`. The SHA-256 of the public key copy is the same as the authoritative original, and therefore the verification anchor is the same.
- In an isolated copy of the `fe36c7cdecb9a68426727153ff2dc90487c40ca7` sparse source code, run the original `scripts/release-dryrun.ps1 -Version v0.5.0 -ValidateOnly -AssetsDirectory ...` against the same download directory. Exit code 0, output `RELEASE_DRYRUN_ASSET_COUNT=15`, `RELEASE_DRYRUN_SHA256SUMS=PASS`, `RELEASE_DRYRUN_ITEM_FAIL_COUNT=0`, `RELEASE_DRYRUN_VALIDATE_ONLY=PASS`. The reason for using a copy of the build commit is that the script requires provenance's `source.commit` to be strictly equal to the run directory HEAD; the current release tag HEAD is its docs-only child commit.
- Unauthenticated HTTP review: Official Release API `GET /repos/cuipengdba/agentsql/releases/tags/v0.5.0` returns 404, draft asset `SHA256SUMS` download URL returns 404. The public tag landing page returns 200, but does not include draft release notes; this is consistent with the status of the tag being visible and the Release draft being unpublished.

## Borders and legacy

- The b80b report has been submitted before tag (`783b008`); this b81 report is submitted separately by MainAgent after tag and draft acceptance. The tag points to `783b008`, and the provenance build submission `fe36c7c` is its direct parent submission, and the two are traceable; report submission will not move the tag.
- The first automated preflight stopped before the tag was created due to an incorrect input of the expected hash in the command; it passed after correction. The first signature verification call was missing parameters due to the PowerShell automatic variable `$input`, and stopped after 15 items of downloaded SHA-256 had passed; after renaming the parameter variable, all three signature verifications and ValidateOnly passed. Original failures and recoveries are retained in timestamped logs.
- This batch remains Release draft. It will be officially transferred to batch 83; GHCR has not been pushed, the official website has not been deployed, and it has not been promoted. The private key and license contents were not read or recorded; the two protected paths were not touched.
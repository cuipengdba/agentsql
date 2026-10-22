> Historical archive: retains the original plans and facts at each release point, not the current version operating guide.

# AgentSQL publishing process and tag meaning

**Language:** [English](release-process-history.md) | [Simplified Chinese](../release-process-history.md)

## v1.0.0 release execution sequence

According to [, each version of the delivery process ](RELEASE_ROUTINE.md) and [v1.0.0 is executed according to the acceptance record ](v1.0-acceptance.md); personnel entrustment, column policy failure closure and audit failure are blocking items.

1. Sealing board design, code, embedded console, official website and three copies of v1.0.0 PDF; submit Go/Web/official website, real MCP client and database regression at the same time and pass.
2. Use v0.9.0 official package and pairing key to practice SQLite merge library and PostgreSQL metadata/audit sub-library upgrade and complete recovery; there are no new migrations in this version.
3. Run dual-architecture dry-run and production preparation from a clean commit, sign SPDX, provenance, SHA256SUMS and complete three Ed25519 signature verifications; push annotated tag pointing to the same commit, publish 15 assets and anonymously download back for verification.
4. Push GHCR `v1.0.0`, `v1.0.0-demo`, `v1.0.0-quickstart` accurate tags, check amd64/arm64, OCI revision, anonymous pull and runtime health; then publish GitHub Release.
5. Back up and upgrade the independent Live Demo, verify the version, allow, deny, desensitize and persist audit on the public network; then deploy the official website copy, download and PDF, and perform static security verification on the public network. After all entrances are passed, `latest` is raised individually and pulled anonymously again.

If any blocking item fails, it will be repaired and re-accepted, and the candidate version will not be marked as released.

## v0.9.0 release execution sequence

1. Complete [ release instructions ](release-notes-v0.9.0.md), [ acceptance record ](v0.9-acceptance.md), official website and three copies of v0.9.0 PDF; run full warehouse test, real MCP/database regression and submit CI from the same clean board submission.
2. Run dual-architecture dry-run, `-ProductionPrepare`, Ed25519 three signatures and source/hash verification of 15 assets from this submission; use the v0.8.0 official signature package to practice the upgrade of the SQLite merge library and PostgreSQL metadata/audit sub-library and complete backup and recovery.
3. Push the annotated `v0.9.0` tag, create a GitHub Release draft, upload and anonymously download all assets for signature verification. Push the exact version tag of GHCR master image, demo and quickstart from the same tag, check public, amd64/arm64 manifest, anonymous pull, OCI revision and runtime health.
4. Public GitHub Release, complete backup and upgrade of independent Live Demo backend to the same exact demo image; use public network `/healthz`, `/readyz`, real demo query and audit verification version to be consistent with the link. After the Demo is accepted, the official website v0.9.0 page and PDF will be deployed; if the upgrade fails, the old backend and data snapshot will be restored, and the old backend will not be displayed as v0.9.0.
5. After the above entrance is passed, separately upgrade GHCR `latest` according to the accepted digest, and pull it anonymously again. Record the final submission, CI, 15 signed assets, image summary, official website and Demo external network smoke test.

Any failure in signing, upgrade recovery, anonymous pulling or demo testing prevents the release from being declared complete; fix the failed areas and re-submit for acceptance from a new block.

## v0.8.0 release execution sequence

For the functional and isolation acceptance of v0.8.0, see [ release notes ](release-notes-v0.8.0.md) and [ acceptance records ](v0.8-acceptance.md). The asset source, signature and public key thresholds of the release script follow the verified process below, but each item must be submitted for reconstruction and acceptance from the v0.8.0 final board, and the v0.7.0 product cannot be reused.

1. Complete the candidate source code, official website, three v0.8.0 PDFs and document seals; submit the CI to be fully green and run a complete dual-architecture dry-run and `-ProductionPrepare` in a clean working tree.
2. Use the trusted public key and the official private key outside the repository to sign in the order of SPDX, provenance, and SHA256SUMS, and pass three cryptographic verifications; `-ValidateOnly` checks 15 assets, submissions, content, and construction sources.
3. The local annotated tag points to the closure board submission, and the official packages v0.7.0 and v0.8.0 are used to review the upgrade and complete backup and recovery of the SQLite merge library and PostgreSQL metadata/audit sub-library. After passing the push tag, create a GitHub Release draft and upload 15 assets, and then publish it back for download and verification.
4. Push GHCR `v0.8.0`, `v0.8.0-demo`, `v0.8.0-quickstart` exact tags from the same commit; check public, amd64/arm64 manifest, anonymous pull, OCI revision and run health. Then officially release GitHub Release, deploy the official website v0.8.0 page and PDF, and do an external network smoke test.
5. After all the above entrances are passed, use the accepted main image digest to separately upgrade `latest` and pull it anonymously again. Record the post-release smoke test, source submission, asset summary, signature, public network image digest and official website version.

Stop publishing and fix when any candidate drifts, fails to sign, is missing assets, fails to build, or fails anonymous pull; the official website and `latest` will not switch in advance.

The release execution sequence of v0.7.0 and the historical record of the preparation phase of v0.5.0 are retained below. Historical dates, product summaries and commands cannot be used as acceptance results for v0.8.0.

## v0.7.0 release execution sequence

The four compatible entry GUI queries between Dify and MCP Inspector have evidence of isolation acceptance; for functions and rollback boundaries, see [ release notes ](release-notes-v0.7.0.md) and [ acceptance records ](v0.7-acceptance.md). For the names of the 15 assets, see [ release asset list ](RELEASE_ASSETS.md). [v0.6.1 Official assets ](https://github.com/cuipengdba/agentsql/releases/tag/v0.6.1), v0.7.0 closed submission and simultaneous submission CI, final rollback drill and tagged assets, GHCR, official website results must be written in [v0.7.0 GitHub Release](https://github.com/cuipengdba/agentsql/releases/tag/v0.7.0) text and release evidence; local uncommitted code or old PASS for dry-run cannot be replaced.

1. Check the code, real database and real MCP client for acceptance, and organize the version notes. The SQLite merge library can be restored back to the published v0.6.0 after restoring from the pre-upgrade database and paired `AGENTSQL_SECRET`. The PostgreSQL metadata/audit branch must target v0.6.1 as the old version; obtain its official assets before release, and use the signed assets of this version to complete the isolated dual database upgrade rollback retest. Specific production deployments must still verify the backup and recovery suite in the target environment. The original v0.6.0 branch service cannot be used as an available fallback target; the old version cannot directly read the new v0.7.0 schema.
2. Include all v0.7.0 source code, migrations, embedded console products and documentation in the final submission. It is recommended to build a new working tree from this submission, and check `git rev-parse HEAD` and `git status --porcelain --untracked-files=all` before and after construction. It is required that HEAD remains unchanged and there is no status output. `-ProductionPrepare` rejects tracked and untracked non-ignored files initially and checks the work tree, HEAD and critical input hashes again after the package, SBOM and image are built.
3. Running a complete dual-architecture dry-run from the board submission requires 15 local assets and three OCI archives to pass; the public key and signature are placeholder files and cannot be released. Then run `-ProductionPrepare` with the controlled official public key to generate 11 files to be signed; this mode is prohibited from being used with `-SkipImages`. Common provenance checksum is used to check official provenance status, size of 8 subjects with SHA-256, and SHA-256 of 11 build input-relative sealed Git objects before signing. Sign SPDX SBOM, `provenance.json`, generate `SHA256SUMS` covering the remaining 13 items, and finally sign the list. Run `scripts/releasesign/main.go -mode verify` three times, then use `-ValidateOnly` to verify exactly 15 items and double-check the source. `SIGNED_STRUCTURE_ONLY` does not represent cryptographic verification.
4. Confirm that the submission of `provenance.json` is consistent with the board submission, `dirtyAtBuild=false`, and check the binary version in tar, ELF/GLIBC, YashanDB client, package list and SBOM. First create a local annotated tag pointing to the same submission, and complete the upgrade and rollback retest of SQLite and PostgreSQL sub-library with the official v0.6.0/v0.6.1 package and the signed package of this version; after passing the test, push the tag, create a GitHub Release draft, and upload 15 assets. Download it back to the new directory and recalculate the summary, verification and review, and keep the draft temporarily.
5. Build and push the GHCR `v0.7.0`, `v0.7.0-demo`, `v0.7.0-quickstart` precise tags from the clean working tree submitted by the same board. The push script forces verification that both local and GitHub annotated tags point to the commit. Record the build push digest and compare it item by item with the public manifest digest; confirm that the three tags are all public and can be pulled in the amd64/arm64 environment without logging in. The OCI revisions of the six image configurations are all equal to the sealing tag SHA, and the acceptance of healthy and applicable business links has been completed. Prepare the v0.7.0 official website content and PDF, and verify the static resources in the pre-release environment; after passing the above gate, convert the GitHub Release draft to the official website, deploy the official website, and perform post-release smoke testing. Record the original `latest` digest. After the official release and the official website smoke test pass, use the accepted top-level digest of the main image as `-AcceptedMainDigest` to separately upgrade `latest` and check the results; in case of failure, only try to restore the old digest for the values ​​that still belong to this operation.

Local command example (the output directory must be a completely new path; the official public key path is provided by the release manager outside the repository):

```powershell
git rev-parse HEAD
git status --porcelain --untracked-files=all
pwsh ./scripts/release-dryrun.ps1 -Version v0.7.0 -OutputDirectory dist/release-dryrun/v0.7.0-final
$trustedReleasePublicKey = '<独立核验的发布公钥绝对路径>'
pwsh ./scripts/release-dryrun.ps1 -Version v0.7.0 -ProductionPrepare -PublicKeyPath $trustedReleasePublicKey -OutputDirectory dist/release-prepare/v0.7.0-final
```

The above commands will not automatically sign, create tags, publish GHCR or deploy the official website. For specific deployment and rollback operations, see [ Deployment Guide ](DEPLOY.md).

## History of v0.5.0 preparation phase

The following text retains the release engineering constraints and drill evidence at that time; the status of "local dry-run" and "unreleased" only refers to the records at that time.

### Versions and Assets

The version tag of GitHub Release is `v0.5.0`. For its 15 uploaded files, sources, verification rules and current exercise status, see [RELEASE_ASSETS.md](RELEASE_ASSETS.md). `v0.4.0`'s [ public Release](https://github.com/cuipengdba/agentsql/releases/tag/v0.4.0) is the asset naming baseline. Old tar or old SHA-256 cannot be renamed and reused. `docs/release-notes-v0.5.md`, `CHANGELOG.md` and `docs/release-day-checklist-v0.5.md` are the release copy and execution list of this version.

The repository did not have the `.github/workflows/` publishing workflow at the time. The build and release portals are distributed across `Makefile`, `scripts/`, and the release day manifest; a PASS for a local dry-run cannot be interpreted as a pass through the GitHub Actions or official release gates. CI workflows exist, but there are still no automated steps to take a local walkthrough directly into a production release.

v0.5.0 The target release gate recorded at that time was **2026-10-16 16:00 CST (Beijing time, UTC+08:00)**. This is historical schedule time, not proof that v0.7.0 has been boarded or passed through the gate.

### local dry-run

Run `powershell -NoProfile -File .\scripts\release-dryrun.ps1 -Version v0.5.0 -OutputDirectory dist\release-dryrun\v0.5.0-final` in the root directory of the repository. The script fixes Rocky 8 and Syft image digest, builds amd64/arm64 packages, SBOM, provenance and three local OCI archives; requires available Docker daemon, Buildx, dual-architecture running capabilities and build dependencies. Overwriting is refused if the output directory already exists. The dry-run public key and three signature files are clearly marked as `dryRun=true`, `signed=false`, and `releasable=false`.

To check only existing directories, run `powershell -NoProfile -File .\scripts\release-dryrun.ps1 -Version v0.5.0 -ValidateOnly -AssetsDirectory <目录>`. This mode only reads assets and git HEAD and does not build images. It displays 15 lines of inventory and SHA-256, and performs fail-closed verification of name collections, sidecars, 13-item manifests, Yashan content, provenance commits, and signed documents. The formal signature must also separately verify the three original files using `scripts/releasesign/main.go -mode verify`; the validator's `SIGNED_STRUCTURE_ONLY` is not a cryptographic signature verification success.

### Two auxiliary GHCR tags

`ghcr.io/cuipengdba/agentsql:v0.5.0-demo` and `ghcr.io/cuipengdba/agentsql:v0.5.0-quickstart` **are both container image tags, not git tag**. The Bake target of `v0.5.0-demo` uses the main repository `Dockerfile` and `VERSION=v0.5.0` to build the demo base; the target of `v0.5.0-quickstart` is based on the demo base in the same figure, and additionally puts in `/etc/agentsql/config.demo.yaml` and `/etc/agentsql/demo-seed.yaml`. Both should have `linux/amd64` and `linux/arm64`, and quickstart should have a different content layer than demo.

`scripts/build-ghcr-multiarch.ps1 -Version v0.5.0 -IncludeAuxiliaryTags` only generates local OCI archives and digest JSON by default; only explicit `-Push` will write to GHCR. `-Push` only processes the accurate version and auxiliary tags, and does not move `latest`; `-PromoteLatest` is a separate operation after the accurate tag on the release day has been accepted by the public and anonymous dual architecture. The OCI digest in the dry-run output is **local archive evidence**, which does not mean that GHCR already has these tags, has been made public, or can be pulled anonymously.

The complete local walkthrough for 2026-10-05 is at`dist/release-dryrun/v0.5.0-20261005b/`Exit code 0, all three OCI archives passed`linux/amd64`and`linux/arm64`descriptor check.`ghcr-agentsql-v0.5.0-image-digests.json`of`pushed=false`, the three local digests recorded are: main image`sha256:33f98bcbaf66dddc2a055a007931f2ecd25f41d284660663b3ec31bd957509d4`、demo `sha256:8bb2e984d4cdac125f32d01eef0adf609a6f79d0de74ee7f89172e99a97aea96`、quickstart `sha256:a2b81a0ce48d1b22ef7cab033884587522a90ad7322016f91b1452032fd34c2e`. This is a local unsigned walkthrough, not a GHCR remote state.

### v0.5.0 The official release gate at that time

First, rebuild the clean working tree submitted in the final sealing board, check 15 official documents, complete the cryptographic verification of three Ed25519 signatures, and then execute the tag according to the [ release date list ](release-day-checklist-v0.5.md), GitHub Release draft, asset back download, public and anonymous dual-architecture acceptance of the GHCR accurate tag, `latest` promotion and final release. If any asset is missing, the hash does not match, the signature fails, the schema is missing, the provenance error is submitted, or the release gate fails, it will stop; it cannot be replaced by the old dry-run file.

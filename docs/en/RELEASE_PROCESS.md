# AgentSQL current version release process

**Language:** [English](RELEASE_PROCESS.md) | [Simplified Chinese](../RELEASE_PROCESS.md)

The current release candidate baseline is v4.0.0. Its scope and remaining gates are in the [acceptance record](v4.0.0-acceptance.md); distribution and public results will be recorded after formal release. Each maintenance version follows the [per-version delivery process](RELEASE_ROUTINE.md) for design, code, testing, upgrade recovery, CI, signed assets, GitHub Release, GHCR, website, and Demo.

1. Check the scope of the previous official version and this version, and write design and acceptance use cases. The database supports declaration binding to specified versions, modes, operations and measured evidence.
2. Complete the code, embedded web, documentation, official website and PDF in the isolated branch. Perform Go/Web, applicable safe negative sets, true integration, and upgrade recovery checks.
3. Review and merge the PR, and wait for the corresponding submitted Go, Web, and govulncheck to be fully green. After sealing, build from a complete clean submission, and check the source code hash and HEAD before and after.
4. Run `release-dryrun.ps1 -ProductionPrepare` to generate 11 assets to be signed and three dual-architecture OCI archives; formally sign SPDX, provenance, and SHA256SUMS, complete three Ed25519 cryptographic signature verifications, and all 15 lists and dual-architecture content have passed. See [’s current asset list for ](RELEASE_ASSETS.md).
5. Create an annotated tag pointing to the commit, push the exact GHCR master/Demo/quickstart tag, verify public status, amd64/arm64 manifest, anonymous pull, version and OCI revision. Save the original `latest` and do not move it yet.
6. Create a GitHub draft Release and upload all signed assets, checking the name, quantity, source and hash. After making it public, re-download it anonymously and verify it.
7. Back up the old demo's data, configuration, and pairing keys; verify read-only diagnostics, allow/deny, masking, and persistent auditing on a separate candidate volume before switching with a rollback script. Check the public network version and the real call.
8. Deploy the same versions of Chinese and English official websites, downloads, PDFs and compatibility boundaries, and check HTTP, security headers, resources, versions, language switching and public browser performance.
9. After the official website and demo pass, separately upgrade GHCR `latest` according to the accepted main image digest, pull it anonymously and run it again. Complete the release record, actual command exit code, summary, environment and untested items.
10. Synchronize `main` of the standard workspace `D:\ruanjiansheji\agentsql` to the latest `origin/main` and check the clean status; auxiliary branches and existing local modifications must be retained.

If any required access fails, repair and re-verify the corresponding candidate. If the online switching fails, restore the old configuration and complete data backup, and do not write the candidate as passed. Historical publications, their licenses, abstracts and signatures remain unchanged.

For v3.2.0 formal delivery, see the [release record](v3.2.0-release-evidence.md). The [earlier release execution record](release-process-history.md) retains historical dates and versions and cannot replace this acceptance.

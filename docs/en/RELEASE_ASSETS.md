# AgentSQL’s current issued assets and signature verification

**Language:** [English](RELEASE_ASSETS.md) | [Simplified Chinese](../RELEASE_ASSETS.md)

Current release candidate: v3.8.0. Each maintenance release is rebuilt from its official closure commit, with the asset name's executable definition as `scripts/release-dryrun.ps1`; historical packages, placeholder signatures, or old provenance cannot be reused.

## formal assets

```text
agentsql-v3.8.0-linux-amd64.tar.gz
agentsql-v3.8.0-linux-amd64.tar.gz.sha256
agentsql-v3.8.0-linux-arm64.tar.gz
agentsql-v3.8.0-linux-arm64.tar.gz.sha256
agentsql-v3.8.0.spdx.json
agentsql-v3.8.0.spdx.json.sig.json
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

SPDX, provenance, and SHA256SUMS are each signed by Ed25519; the list covers the remaining 13 items. The public key must be checked against the key ID through an independent trusted channel. Download and read the included package `VERIFYING-SIGNATURES.md`, verify the three signatures using a trusted public key, and then verify the file hashes in the manifest; do not run the installation script before it has been verified.

Official packages also need to verify the binary version, OCI revision, actual Go toolchain, source commit and `dirtyAtBuild=false`. Both architectures include designated YashanDB clients and drivers that are authorized for redistribution; you cannot claim that all database production acceptance has been completed just because the libraries are included in the package. For installation system requirements, upgrades and recovery, see [ Deployment Guide ](DEPLOY.md).

## Maintenance version production preparation

```powershell
pwsh ./scripts/release-dryrun.ps1 -Version v3.8.0 -ProductionPrepare -PublicKeyPath '<trusted-public-key-absolute-path>'
pwsh ./scripts/release-dryrun.ps1 -Version v3.8.0 -ValidateOnly -AssetsDirectory '<signed-assets-directory>'
```

Successful production preparation only means that the assets to be signed and the three dual-architecture OCI archives have been generated. Formal signatures, cryptographic verification, GitHub upload, anonymous re-download, GHCR, and online acceptance must pass before a release is declared. The `-ValidateOnly` signature structure check does not replace verification with a trusted public key.

The actual v3.8.0 package, CI, signature, GHCR, website, and Demo results are in the [formal release evidence](v3.8.0-release-evidence.md) and [acceptance record](v3.8.0-acceptance.md). The [historical asset plan](release-assets-history.md) is retained for reference.

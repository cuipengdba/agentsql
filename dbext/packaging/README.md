# AgentSQL binder local supply chain (S5, feature off)

This directory builds the optional `agentsql_binder` server extension. Every
compile occurs in a Linux container against the target PostgreSQL major; the
Windows host never compiles or links the C module. Nothing in these scripts
pushes to a registry or package repository, and no AgentSQL feature flag is
changed.

## Matrix and output

`build-packages.ps1` covers PostgreSQL 14, 15, 16, 17, and 18 on
`linux/amd64` and `linux/arm64`, producing deb (PGDG/Debian), rpm
(PGDG/Rocky 9), and apk (major-specific Alpine repository) packages below
`dist/binder/{deb,rpm,apk}/pg<major>/<arch>/`. The package contains:

- `agentsql_binder.control` and `agentsql_binder--0.4.sql` in the target
  server's `sharedir/extension`;
- `agentsql_binder-0.4.so` plus the required `agentsql_binder.so` symlink in
  the target server's `pkglibdir`;
- a dependency pinned to the matching PostgreSQL server major.

Each package has a CycloneDX 1.5 SBOM (`.cdx.json`) listing GCC, make,
PostgreSQL server headers, libc/musl headers, and the runtime server dependency,
plus SLSA/in-toto-shaped local provenance (`.provenance.json`).

```powershell
./dbext/packaging/build-packages.ps1
# Focused developer build:
./dbext/packaging/build-packages.ps1 -PostgresMajor 16 -Platform linux/amd64 -Format deb
```

Buildx runs amd64 natively and arm64 through the builder's binfmt/QEMU support.
Alpine bases are mapped as PG14/3.15, PG15/3.17, PG16/3.19, PG17/3.21, and
PG18/3.23; repository retirement is a release-blocking maintenance event, not
a reason to silently substitute a different major.

Package runtime verification is also containerized:

```powershell
# Required amd64 matrix
./dbext/packaging/test-packages.ps1
# Required QEMU example (and optionally the full arm64 matrix)
./dbext/packaging/test-packages.ps1 -PostgresMajor 16 -Platform linux/arm64
```

## DEV ONLY signatures

At the end of a build, the local signer container generates a fresh RSA key,
valid for one day, signs packages/SBOMs/provenance/checksums with detached
ASCII signatures, exports only `DEV-ONLY-public-key.asc`, verifies every
signature, and destroys the private key on container exit. Re-running signing
rotates the development identity and replaces old development signatures.

**DEV ONLY: these signatures are never production trust. The production
private key must be generated, stored, and used offline by the release owner.
Promoting any package, image, public key, or checksum is a separate owner gate.**

`install-binder.sh` verifies the signature, checksum, SBOM target major and
architecture before invoking `dpkg -i`, `rpm -ivh`, or
`apk add --allow-untrusted`. `CREATE EXTENSION` is opt-in and requires an
explicit bootstrap DSN and unprivileged runtime role. Package installation
never connects to a database.

```sh
./dbext/packaging/install-binder.sh --dist ./dist/binder
./dbext/packaging/install-binder.sh --dist ./dist/binder \
  --create-extension --dsn "$BOOTSTRAP_DSN" --runtime-role agentsql_runtime
```

The bootstrap path requires a superuser bootstrap identity, rejects a runtime
role with superuser/CREATEDB/CREATEROLE, rejects an existing non-0.4 extension,
and refuses a pre-existing `agentsql_catalog` schema owned by another role.

## Derived images

```powershell
./dbext/packaging/build-images.ps1
./dbext/packaging/test-images.ps1
./dbext/packaging/test-images.ps1 -PostgresMajor 16 -Platform linux/arm64
```

The script locally loads architecture-specific tags such as
`agentsql/postgres-binder:16-0.5-amd64`, emits OCI archives and image SBOMs,
and never pushes. The image installs the already-built matching Debian package,
so its extension bytes are identical to the package matrix rather than a second
compile. Run `build-packages` first. Fresh data directories create and self-test the extension via
`/docker-entrypoint-initdb.d`. Existing volumes do not rerun that directory;
an owner-controlled upgrade job must explicitly run the versioned SQL.

Rollback means installing a previously retained, verified package/OCI archive
and applying its supported extension downgrade procedure. v0.4 has no downgrade
SQL, so a database that has created 0.4 must restore/roll forward rather than
pretend a package downgrade changed catalog objects.

## Server boundary and cloud fallback

`CREATE EXTENSION` can only register files already installed under PostgreSQL's
server-side `pkglibdir` and `sharedir`. A database superuser cannot safely push
this `.so` to a remote filesystem. The installer and gateway never use large
objects, `COPY PROGRAM`, or `lo_export` as a transfer mechanism.

Standard RDS/Aurora, Azure Database for PostgreSQL Flexible Server, and Google
Cloud SQL do not accept arbitrary customer C extensions. The gateway detector
therefore skips CREATE and selects `CATALOG_CLOSED_V1`. Runtime credentials are
not superusers; filesystem installer, database bootstrap, and runtime roles are
separate authorities.

The gateway cannot read or hash arbitrary bytes in a remote server filesystem.
It rejects missing/unloadable/corrupted modules and strictly compares the C
module's ABI, PG major, extension version, build, source, node-manifest, and
allowlist attestations. A host-root attacker able to replace executable server
code and forge those answers is outside the database trust boundary; detached
signature/SHA verification before privileged installation and host filesystem
controls are the corresponding supply-chain controls.

## Release gates

Before any real publication, the accountable owner must explicitly approve:

1. an offline production signing key and rotation/revocation policy;
2. whether each deb/rpm/apk and OCI image is released at all;
3. the target package and image registries and the upload operation;
4. PG minor compatibility, rollback, read-only filesystem, wrong
   major/architecture/libc, tamper, and QEMU arm64 evidence;
5. S6 divergence evidence and the S7 activation/failure gate.

This S5 implementation does not satisfy or activate S6/S7.

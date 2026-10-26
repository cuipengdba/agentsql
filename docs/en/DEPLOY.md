# AgentSQL Deployment Guide

**Language:** [English](DEPLOY.md) | [Simplified Chinese](../DEPLOY.md)

> Current guidance: Applies to v4.3.0 and compatible 4.3.x maintenance releases. The historical introduction version and acceptance record retain the original facts; for daily entry, see [ document index ](README.md).

AgentSQL is a database security gateway and production-grade MCP Server for AI Agents. Metadata and audit records are saved locally in SQLite by default, and the PostgreSQL control plane can also be used; business databases still use their respective PostgreSQL or MySQL connections.

For the current exact installed version, download link, and pre-upgrade checks, see [ Quick Start ](GETTING_STARTED.md) and [v2.4.0 Release Notes ](release-notes-v2.4.0.md). Fixed dual-gateway Kubernetes deployment See also [v2.4.0 Operations Guide ](../../deploy/kubernetes/README.en.md). The release commands and phased online records marked with v1.2.0 or other old version numbers at the end of this article are historical examples; the same version assets, signatures and image digests of the official Release must be used before execution.

## Directory convention

| path | Purpose |
| --- | --- |
| `/usr/local/bin/agentsql` | Gateway service binary |
| `/usr/local/bin/agentsqlctl` | Operation CLI |
| `/etc/agentsql/config.yaml` | Service configuration |
| `/etc/agentsql/agentsql.env` | Key and admin credentials, permissions should be `0600` |
| `/var/lib/agentsql/agentsql.db` | SQLite metadata and audit library |

Container deployment uses the same `/etc/agentsql` and `/var/lib/agentsql` paths. Compose uses the named volume `agentsql-data` to persist container data to avoid the Linux host bind mount from generating root permission files.

## Method 1: Docker Compose (recommended)

```bash
cp examples/docker/.env.example .env
```

Edit `.env`, replacing `AGENTSQL_SECRET` and `AGENTSQL_ADMIN_PASSWORD`. `AGENTSQL_SECRET` must be exactly 32 bytes and cannot be changed later.

Generate your own random values; do not copy values from the documentation, and do not submit the generated results to the repository:

```bash
openssl rand -base64 24                    # AGENTSQL_SECRET: output exactly 32 ASCII bytes
openssl rand -base64 24                    # Can be used as a strong administrator password
```

```powershell
$bytes = [byte[]]::new(24); $rng = [System.Security.Cryptography.RandomNumberGenerator]::Create(); $rng.GetBytes($bytes); [Convert]::ToBase64String($bytes); $rng.Dispose()
```

The PowerShell command uses a cryptographic RNG to generate 24 random bytes, which are encoded into exactly 32 ASCII bytes. Run it twice and fill in the SECRET and administrator password.

```bash
docker compose up -d --build
docker compose ps
```

Open <http://127.0.0.1:7780> in the browser and log in using the administrator account in `.env`. The demo database is not started by default; when PostgreSQL 16 and MySQL 8 are required, use:

```bash
docker compose --profile demo up -d --build
```

Console live event streaming is enabled by default via `server.event_stream: true` and allows up to
`server.event_stream_max_connections: 100` concurrent SSE connections (valid range 1–1000).
When reverse proxying this path you should turn off caching, response compression, and buffering, and set the idle timeout to greater than 25 seconds.

Use this when you need to start AgentSQL, Prometheus and the Grafana six-panel automatic provisioning at the same time:

```bash
AGENTSQL_SECRET='N7vK2mQ9xR4tY8pL6cW3sD5fH1jB0zUa' \
AGENTSQL_ADMIN_PASSWORD='S9afe-Admin-Passphrase-2026' \
docker compose --profile observability up -d --build
```

The above values ​​are only local examples that meet the verification rules. For actual deployment, please generate and properly store random values ​​as described above. Prometheus, like Grafana and AgentSQL, only binds to the host loopback address. Named volumes can be deleted explicitly with `docker compose down -v`; this command permanently clears SQLite metadata and audit records.

## masked hash key management

`hash` uses a proprietary HMAC key to generate an irreversible hash fingerprint; it is not encryption and has no ability to decrypt or restore the original text. This key is separate from `AGENTSQL_SECRET`, which is used for data source passwords, console/JWT, etc., and may not be reused or derived. Compatible single key can come from YAML `redaction.hash_key` or environment variable `AGENTSQL_REDACTION_HASH_KEY`; v0.4 multi-version mode uses inline `redaction.hash_keys` or absolute path `AGENTSQL_REDACTION_HASH_KEYS_FILE`. These sources are mutually exclusive. Each non-null material is counted as raw bytes, at least 32 bytes, and multi-version materials must also pass diversity and duplication checks.

`block` and `range` do not use any key, salt or HMAC parameters, and do not participate in hash key startup fail-fast. `mask`, `block` and `range` can all run normally without a key. Only `hash` requires the above-mentioned dedicated key.

The production environment should be injected from the secret manager, orchestration platform Secret, or permission-controlled environment file. Do not write into container images, Dockerfiles, Git, or command line parameters, and do not leave real values in the sample configuration. A random key can be generated:

```bash
openssl rand -base64 32
```

Compose has`AGENTSQL_REDACTION_HASH_KEY`Pass in the default service and controlplane profile as optional variables; pass in an empty string when not configured, only use`mask` / `block` / `range`The deployment can still start normally. Since the empty string also belongs to "environment variable existence", Compose deployment needs to enable`hash`, should be in the protected`.env`Or set in an external Secret`AGENTSQL_REDACTION_HASH_KEY`, don't just fill it in YAML`redaction.hash_key`. The YAML value is only used if the variable is not declared by the native binary or systemd environment.

The capability matrix for startup and management is as follows:

| enabled rule set | hash key status | Startup and running results |
| --- | --- | --- |
| Only `mask` / `block` / `range` (can be mixed) | Not configured or empty | Start and execute normally; `block` and `range` do not enter key access control |
| Only `mask` / `block` / `range`, and disabled `hash` draft | Not configured or empty | Normal startup; disabled hash can be saved and edited, but does not participate in execution |
| Contains enabled `hash` (can contain `mask` / `block` / `range` at the same time) | Not configured or empty | Failure to start before providing external services (fail-fast) will not degenerate into returning the original text, `***` or the entire `[REDACTED]` |
| Any legal set of enabled rules | At least 32 bytes | Start normally and execute all legal rules; the key is only used by `hash` |
| any set of rules | Explicit configuration is not empty but is less than 32 bytes | Configuration assembly fails immediately, error does not echo key |

While the service is running, create a new `hash` rule that is enabled by default in an instance without a key, or change the existing hash rule to enabled. The management API returns HTTP `503` and machine code `HASH_REDACTION_UNAVAILABLE`, and rejects it before writing to the library, ensuring zero writes. You can save it as disabled first; configure the key and restart the service before enabling it. Creating or enabling `block` will never return this error due to a missing hash key. When the combination of sensitive type and algorithm is illegal, HTTP `422` and machine code `INVALID_MASK_RULE` are returned.

### Security Boundary for Hash Fingerprints

1. AgentSQL uses HMAC-SHA256 with a private key instead of bare SHA-256. Low-entropy values ​​such as mobile phone numbers, ID cards, email addresses, and birthdays can be enumerated; naked SHA is easily subject to offline exhaustive enumeration or rainbow table reverse checking, while HMAC only binds the ability to verify candidate values ​​to the secret key.
2. HMAC is not a proof of anonymity. When an attacker obtains the key or is able to send self-selected candidate values ​​into the same hash oracle, it is still possible to lexiconize the low-entropy input; "irreversible" only means that there is no decryption function, which does not mean absolute anonymity. Minimum query permissions and key protection are still indispensable.
3. Deterministic fingerprints will inevitably expose equality relationships and frequencies: the same value under the same key will get the same fingerprint, which can be used for downstream equal value association, deduplication and grouping, and will also expose duplicate values, hotspots and distributions. Globally shared keys also create the risk of cross-data source and cross-table correlation tracking; environments or tenants that should not be correlated with each other must use different keys.
4. Replacing the key will change the fingerprint and break the equivalence association between the old and new results. v0.4 supports versioned history verification and restart plan switching, but does not have hot loading, double writing, online rotation or automatic recalculation; it is necessary to stop writing, drain, CAS active, unified manifest modification, complete restart, strong reconciliation and confirm `/readyz` 200 in the maintenance window. Before rotation, you must take stock of the enabled `hash` rules and existing data that relies on fingerprints, formulate a downstream backfill/dual version transition and rollback plan, and retain the old key as legacy rollback material until the acceptance is completed.
5. Cross-library equivalence only holds if the string bytes arriving at the HMAC are identical. Different date, time zone, decimal, leading and trailing blanks, upper and lower case or Unicode representation will produce different fingerprints; the system does not trim or format the equal value association.
6. `hash` is calculated after the business database returns the result to AgentSQL and before responding to the caller. It will not be pushed down to `JOIN`, `WHERE` or `GROUP BY` in the business database, nor will it change the internal comparison semantics of the database. It can only be used for equivalence association by downstream systems that obtain the returned fingerprints.
7. Keys must not enter responses, audits, logs, metrics, panics, or configuration echoes. YAML only works on deployments with controlled file permissions; keys are not written to the metadata/audit database or masking rules.

Upgrade to support`hash`version should be inventoried via the console, management API, or controlled query`mask_rules`middle`algo=hash`Inventory, focus on checking all enabled rules. When there is no available key, these rules must be deactivated or the original key must be safely injected before the upgrade is started; otherwise, the new version will fail-fast according to the above matrix. disabled draft will not prevent startup.

## PostgreSQL control plane deployment

The PostgreSQL control plane is compatible with PostgreSQL 15+, and the baseline version for development, Compose, and CI is PostgreSQL 18. SQLite is still the default zero-configuration backend; this section uses two independent databases to store six metadata tables and the immutable `audit_logs`. AgentSQL writes must be stopped during migration.

### Compose quick start and stop

Copy the `.env` example and fill in all required passwords, the original `AGENTSQL_SECRET`, and both DSNs. By default only the main file `docker-compose.yml` is loaded, so control plane variables are not read or parsed, only `agentsql` using SQLite is launched. The PostgreSQL control plane is defined in `docker-compose.controlplane.yml`, and three of the services still belong to the `controlplane` profile; all control plane operations must first load the main file, then load the control plane file, and keep the order of the two `-f` unchanged. After loading the control plane file, retained `${VAR:?message}` gives a clear error for unset or empty required variables before creating any containers.

The first switch must be performed in stages and the entire profile cannot be started directly:

```bash
# 1. Pure parsing preflight without creating a container.
docker compose -f docker-compose.yml -f docker-compose.controlplane.yml --profile controlplane config --quiet

# 2. Before switching, stop the default SQLite gateway that may be occupying 7780, and then start only the two PostgreSQL 18 databases.
docker compose -f docker-compose.yml -f docker-compose.controlplane.yml --profile controlplane stop agentsql
docker compose -f docker-compose.yml -f docker-compose.controlplane.yml --profile controlplane up -d --wait metadata-db audit-db

# 3. Follow the steps below to complete the migration, create a running account and authorize it.

# 4. After replacing the two STORE DSNs in .env with the running account, only start the control plane service.
docker compose -f docker-compose.yml -f docker-compose.controlplane.yml --profile controlplane up -d --build --wait agentsql-controlplane
```

`agentsql-controlplane` will wait for both databases to be healthy before starting. The host port only listens to `127.0.0.1:7780`, `127.0.0.1:55432` and `127.0.0.1:55433`; if the migration CLI is not running on the host, the `ports` mapping of the two databases can be deleted. Do not execute the entire profile `up` without a service name, otherwise the default `agentsql` without profile will also enter the merged model and compete with `agentsql-controlplane` for 7780. Be sure to stop the default `agentsql` before switching controls.

Subsequent control plane management commands such as `ps`, `logs`, `stop`, `down` must also carry the same two `-f` in the same order, otherwise Compose may regard the control plane container as an orphan, or may not see these services at all. For example:

```bash
docker compose -f docker-compose.yml -f docker-compose.controlplane.yml --profile controlplane ps
docker compose -f docker-compose.yml -f docker-compose.controlplane.yml --profile controlplane logs agentsql-controlplane
```

Stop the service but keep the named volumes:

```bash
docker compose -f docker-compose.yml -f docker-compose.controlplane.yml --profile controlplane stop agentsql-controlplane metadata-db audit-db
```

Do not execute `docker compose -f docker-compose.yml -f docker-compose.controlplane.yml --profile controlplane down -v` on data that still needs to be retained; merging `-v` in the model will also delete `agentsql-data`, `agentsql-metadata-pgdata`, and `agentsql-audit-pgdata`. Disable this command if any data needs to be retained.

You can run `sh scripts/compose-smoke.sh` to do Compose regression checking with pure `config` parsing; this script will not start the container.

To prevent the host from being unable to resolve `metadata-db` / `audit-db` in the Compose network, you can use the `agentsqlctl` subcommand actually provided by the repository in the control plane image. Build the image before executing the source code build for the first time; use configuration verification, storage health check and empty library initialization migration respectively:

```bash
docker compose -f docker-compose.yml -f docker-compose.controlplane.yml --profile controlplane build agentsql-controlplane
docker compose -f docker-compose.yml -f docker-compose.controlplane.yml --profile controlplane run --rm --no-deps --entrypoint agentsqlctl agentsql-controlplane check-config --config /etc/agentsql/config.yaml
docker compose -f docker-compose.yml -f docker-compose.controlplane.yml --profile controlplane run --rm --no-deps --entrypoint agentsqlctl agentsql-controlplane health --config /etc/agentsql/config.yaml
docker compose -f docker-compose.yml -f docker-compose.controlplane.yml --profile controlplane run --rm --no-deps --entrypoint agentsqlctl agentsql-controlplane migrate --config /etc/agentsql/config.yaml
```

`migrate` is suitable for initializing an empty control plane or upgrading schema; when migrating from existing SQLite, use `migrate-sqlite-to-postgres` as described in the next section. Do not confuse the two processes.

### Switch from SQLite

1. Create two databases `agentsql_metadata` and `agentsql_audit`, as well as their respective one-time migration owners. The migration owner should be the owner of the corresponding database and `public` schema, or have the CONNECT, USAGE, CREATE/DDL, table SELECT/INSERT, and sequence USAGE/SELECT/UPDATE permissions required for migration. When Compose is first initialized, the migration accounts corresponding to `POSTGRES_DB` and `POSTGRES_USER` will create the database and schema; external PostgreSQL can be initialized as follows by the cluster administrator. `\password` will interactively read the password without writing it to the command history:

```sql
CREATE ROLE agentsql_meta_migrator LOGIN;
\password agentsql_meta_migrator
CREATE DATABASE agentsql_metadata OWNER agentsql_meta_migrator;
\connect agentsql_metadata
ALTER SCHEMA public OWNER TO agentsql_meta_migrator;

\connect postgres
CREATE ROLE agentsql_audit_migrator LOGIN;
\password agentsql_audit_migrator
CREATE DATABASE agentsql_audit OWNER agentsql_audit_migrator;
\connect agentsql_audit
ALTER SCHEMA public OWNER TO agentsql_audit_migrator;
```

2. Stop the old AgentSQL and ensure that SQLite is no longer writing. Back up `agentsql.db` and save its paired `AGENTSQL_SECRET` in the same protected backup set. Losing the original SECRET will cause the migrated `password_enc` to be unable to be decrypted.

3. Let `AGENTSQL_STORE_METADATA_DSN` and `AGENTSQL_STORE_AUDIT_DSN` temporarily point to the two migration owners and run the migration. The host CLI does not automatically read Compose's `.env`; the caller must export the original `AGENTSQL_SECRET` and the two DSNs using the host address to the current shell. Use `127.0.0.1:55432` and `127.0.0.1:55433` when the host CLI connects to Compose; DSN is only injected through environment variables and should not be placed in command parameters or submitted to the configuration file. Bash/zsh uses `export NAME=...`, PowerShell uses `$env:NAME = '...'`:

```bash
export AGENTSQL_SECRET='<与 SQLite 备份配对的原值>'
export AGENTSQL_STORE_METADATA_DSN='<指向 127.0.0.1:55432 的 migration-owner DSN>'
export AGENTSQL_STORE_AUDIT_DSN='<指向 127.0.0.1:55433 的 migration-owner DSN>'
agentsqlctl migrate-sqlite-to-postgres \
  --source /var/lib/agentsql/agentsql.db \
  --target-config examples/docker/config.controlplane.yaml \
  --verify-hash
```

The migration command will create and verify the target schema, retain the existing audit ID, align `audit_logs_id_seq`, and output a table-by-table verification summary. Overwriting will be refused when the target library is not empty but the contents are not completely consistent.

4. Create independent metadata and audit running accounts, and apply minimum permissions in the next section. Replace the two DSNs with running accounts; `metadata-db:5432` and `audit-db:5432` should be connected within the Compose container, and external PostgreSQL should use their actual addresses and enable TLS verification suitable for production environments. Keep `store.auto_migrate: false` and continue to use the original `AGENTSQL_SECRET`. The migration owner should `NOLOGIN`, rotate the password, or revoke CONNECT outside the upgrade window; it will be temporarily restored during the next upgrade, and will be disabled again after the upgrade is completed, and the running account permissions will be reasserted for the new object.

5. Before switching, check the configuration and two database connections, and then start:

```bash
docker compose -f docker-compose.yml -f docker-compose.controlplane.yml --profile controlplane run --rm --no-deps --entrypoint agentsqlctl agentsql-controlplane check-config --config /etc/agentsql/config.yaml
docker compose -f docker-compose.yml -f docker-compose.controlplane.yml --profile controlplane run --rm --no-deps --entrypoint agentsqlctl agentsql-controlplane health --config /etc/agentsql/config.yaml
docker compose -f docker-compose.yml -f docker-compose.controlplane.yml --profile controlplane up -d --build --wait agentsql-controlplane
curl --fail http://127.0.0.1:7780/healthz
curl --fail http://127.0.0.1:7780/readyz
```

6. Accept two non-equivalent probes at the same time: `/healthz` should return HTTP 200, `status: "ok"` and the version number to be deployed, which only means that the process is alive; `/readyz` should return HTTP 200 and `status: "ready"`, which means that both the configured metadata and audit storage have passed the readiness check. This is followed by a read-only allow, a deny, and an approve process. Confirm that metadata changes only enter the metadata library, new audits only enter the audit library, the new `audit_logs.id` is greater than the maximum ID before migration, and when the audit write fails, the request is fail-closed as designed. Keep the original SQLite and SECRET backups during the verification period.

In sub-database mode, both management audit and traffic audit are written into the `traffic` chain of the audit library; the metadata library only retains `management_audit_outbox` to be delivered. When the Admin API queries `domain=management`, it will be mapped to this `traffic` chain. Therefore, the persistent audit records should be checked in the audit library when accepting management operations.

### Run account with minimum permissions

The following examples assume that the migration is complete. First, the DBA creates two login-only roles, `agentsql_meta_runtime` and `agentsql_audit_runtime`, and interactively sets independent strong passwords:

```sql
CREATE ROLE agentsql_meta_runtime LOGIN;
\password agentsql_meta_runtime
CREATE ROLE agentsql_audit_runtime LOGIN;
\password agentsql_audit_runtime
```

Then execute subsequent statements as the corresponding database owner or an administrator with authorized permissions; do not replace the example role name with migration owner.

metadata database:

```sql
REVOKE ALL ON DATABASE agentsql_metadata FROM PUBLIC;
GRANT CONNECT ON DATABASE agentsql_metadata TO agentsql_meta_runtime;

REVOKE ALL ON SCHEMA public FROM PUBLIC;
REVOKE ALL ON ALL TABLES IN SCHEMA public FROM PUBLIC;
REVOKE ALL ON ALL SEQUENCES IN SCHEMA public FROM PUBLIC;

GRANT USAGE ON SCHEMA public TO agentsql_meta_runtime;
GRANT SELECT, INSERT, UPDATE, DELETE
ON TABLE public.agents,
         public.datasources,
         public.policies,
         public.rules,
         public.mask_rules,
         public.approvals
TO agentsql_meta_runtime;
GRANT SELECT ON TABLE public.schema_migrations TO agentsql_meta_runtime;
```

audit database:

```sql
REVOKE ALL ON DATABASE agentsql_audit FROM PUBLIC;
GRANT CONNECT ON DATABASE agentsql_audit TO agentsql_audit_runtime;

REVOKE ALL ON SCHEMA public FROM PUBLIC;
REVOKE ALL ON ALL TABLES IN SCHEMA public FROM PUBLIC;
REVOKE ALL ON ALL SEQUENCES IN SCHEMA public FROM PUBLIC;

GRANT USAGE ON SCHEMA public TO agentsql_audit_runtime;
GRANT SELECT, INSERT ON TABLE public.audit_logs TO agentsql_audit_runtime;
GRANT USAGE, SELECT ON SEQUENCE public.audit_logs_id_seq TO agentsql_audit_runtime;
GRANT SELECT ON TABLE public.schema_migrations TO agentsql_audit_runtime;
```

The running account must not obtain the CREATE, ALTER, DROP, REFERENCES or TRIGGER permissions; the audit running account must not obtain the UPDATE, DELETE, TRUNCATE permissions of `audit_logs`, nor the sequence UPDATE permissions. The migration owner is DDL capable and is only used during migration and upgrade windows; upon completion it should disable login, rotate passwords or revoke CONNECT.

These permissions only restrict the AgentSQL running account. They do not protect against tampering by the database owner, DBA, or superuser, nor do they provide compliance-level WORM, retention locks, or non-removable media. There are no distributed transactions between independent metadata/audit databases.

### PostgreSQL backup and recovery

After stopping AgentSQL writing, generate two custom format backups respectively, and back up the current `AGENTSQL_SECRET` at the same time:

```bash
pg_dump -Fc --dbname "$AGENTSQL_STORE_METADATA_DSN" --file agentsql-metadata.dump
pg_dump -Fc --dbname "$AGENTSQL_STORE_AUDIT_DSN" --file agentsql-audit.dump
```

These are two independent dumps. **does not constitute a cross-database consistent snapshot**. If the business requires strict cross-database recovery points, the PostgreSQL platform needs to provide additional coordinated backup capabilities; AgentSQL does not claim dual-database consistent snapshots.

When restoring, first create two empty databases and corresponding schemas, and restore them separately with migration owner:

```bash
pg_restore --no-owner --no-privileges --dbname "$AGENTSQL_STORE_METADATA_DSN" agentsql-metadata.dump
pg_restore --no-owner --no-privileges --dbname "$AGENTSQL_STORE_AUDIT_DSN" agentsql-audit.dump
```

After recovery, re-execute the minimum permission GRANT to verify whether the `schema_migrations` versions of the two databases, the number of rows in each table, the minimum/maximum ID of `audit_logs`, `last_value/is_called` of `audit_logs_id_seq`, and all non-empty `approvals.audit_id` can find the corresponding ID in the audit database. Finally, change back to the running account DSN, start as `auto_migrate:false` and check `/readyz` and audit writing.

### Rollback to SQLite

Stop the control plane service, restore the original SQLite configuration, restore the paired backup of `agentsql.db` and the original `AGENTSQL_SECRET`, and then start the default `agentsql`. Do not treat new writes after the PostgreSQL switch as automatically flowing back into SQLite; there is no double-write or automatic reverse migration between dual databases and SQLite. PostgreSQL remains in place after rollback until reconciliation is complete.

## Method 2: Native binary

Building from source code relies on Go 1.26, C compiler and glibc compatible environment; the official release script of v4.3.0 uses Go 1.26.10. `pg_query_go` cgo must be enabled.

### Precompiled binary system requirements

The official native package is built in the Rocky Linux 8 (glibc 2.28) toolchain for the corresponding architecture. `linux/amd64` corresponds to `x86_64` / `amd64` of `uname -m`; `linux/arm64` native glibc package is provided from v0.4.0 onwards, corresponding to `aarch64` / `arm64`. The amd64 package is suitable for x86_64 RHEL/Rocky/Alma/CentOS 8 series, Alibaba Cloud Linux 3, Kirin V10, Unison UOS, Ubuntu 20.04, Debian 11 and newer versions, and has been verified on Alibaba Cloud Linux 3 (glibc 2.32); CentOS 7 (glibc 2.17) Explicitly not supported.

Precompiled binaries do not support musl (Alpine) or architectures other than amd64/arm64. For these environments, please use the amd64/arm64 multi-architecture container image based on Debian bookworm and including glibc, or run `make build` native compilation on the target machine. The v1.2.0 source code can be built, tested and packaged in the same architecture container using `make release-linux-amd64 VERSION=v1.2.0` or `make release-linux-arm64 VERSION=v1.2.0`; the build script will verify that the GLIBC symbol required by the product is not higher than 2.28. `ROCKY_IMAGE` must be fixed to the audited `@sha256` digest when officially released.

```bash
make build VERSION=v1.2.0
./bin/agentsqlctl init-config -o config.yaml
export AGENTSQL_SECRET="$(openssl rand -base64 24)"
export AGENTSQL_ADMIN_USER='admin'
export AGENTSQL_ADMIN_PASSWORD="$(openssl rand -base64 24)"
./bin/agentsql serve -c config.yaml
```

The above command will generate your own random value for the current process; please persist it via a password manager or a protected environment file. The repository already contains the embedded console product; only when the front-end source code changes, you need to run `make webui` first.

PowerShell native launch example:

```powershell
./bin/agentsqlctl.exe init-config -o config.yaml
$bytes = [byte[]]::new(24); $rng = [System.Security.Cryptography.RandomNumberGenerator]::Create(); $rng.GetBytes($bytes); $env:AGENTSQL_SECRET = [Convert]::ToBase64String($bytes); $rng.Dispose()
$env:AGENTSQL_ADMIN_USER = 'admin'
$passwordBytes = [byte[]]::new(24); $passwordRng = [System.Security.Cryptography.RandomNumberGenerator]::Create(); $passwordRng.GetBytes($passwordBytes); $env:AGENTSQL_ADMIN_PASSWORD = [Convert]::ToBase64String($passwordBytes); $passwordRng.Dispose()
./bin/agentsql.exe serve -c config.yaml
```

## One-click installation (recommended)

The one-click installer supports Linux amd64 (`uname -m` to `x86_64` / `amd64`) and arm64 (`aarch64` / `arm64`) native glibc packages; linux/arm64 native glibc packages are provided from v0.4.0 onwards. Both require glibc 2.28+, and a stock installation also requires systemd to be PID 1; Alpine/musl, CentOS 7, and other architectures will not enter the download, decompression, or system change stages. Recommended commands:

Before execution, check the assets, hash and signature of [v1.2.0 GitHub Release](https://github.com/cuipengdba/agentsql/releases/tag/v1.2.0); the release results are subject to the Release text and reviewable assets.

```bash
curl -fsSL https://github.com/cuipengdba/agentsql/releases/download/v1.2.0/install.sh | sudo sh -s -- install --version v1.2.0
```

The installer will create a restricted system account, loopback monitoring configuration, `0600` environment file and systemd unit, and verify the tarball outer sidecar, package `SHA256SUMS`, version number and ELF architecture layer by layer. The automatically generated administrator password is a continuously valid administrator password, not a one-time password. When stdout is TTY, a one-time password will be displayed when creating credentials for the first time; non-TTY such as pipes and CIs are not displayed by default. `/etc/agentsql/agentsql.env` can be viewed by root, or `--show-password` can be passed in explicitly. The v0.5.1 distribution package builds a standalone YashanDB C client that matches the architecture and is SHA-256 verified. The installer places it into `/usr/local/lib/agentsql/yashandb`; the official assets must also pass the package content check.

Commonly used life cycle commands (check the official assets and signatures of the target version before use):

```bash
sudo ./install.sh install --version v1.2.0
sudo ./install.sh upgrade --version v1.2.0
sudo ./install.sh uninstall
sudo ./install.sh uninstall --purge --yes
```

The default uninstall deletes the two binary, unit and managed YashanDB client directories, retaining `/etc/agentsql`, `/var/lib/agentsql` and system accounts. The upgrade snapshot is saved in `/var/backups/agentsql` dedicated to root. Even `--purge` will be retained. It should be archived or cleaned up by the administrator separately after it is confirmed that it is no longer needed. `--purge` only deletes the aforementioned two fixed directories, configuration and data, and does not follow the external database, certificate or other paths in the configuration; non-interactive environments must transmit `--yes` at the same time. The extra `--remove-user` will only delete the account if it was indeed created by the installer and no other files belong to it. In the snapshot left by the failed upgrade, `agentsql.db` and other files retain the agentsql owner (for rollback as they are), so `uninstall --purge --remove-user` will refuse to delete the account accordingly and name the remaining files; after confirming that the snapshot is no longer needed, first execute `sudo chown -R root:root /var/backups/agentsql` (or delete the directory after archiving) and then rerun the uninstall command to delete it together with the system account. This snapshot does not exist for hosts that are newly installed and have never failed to upgrade. `uninstall --purge --yes --remove-user` can clear binaries, units, configurations, data and accounts at once.

For offline installation, you can pass the decompressed package directory to `--from`, or pass in the tarball; when passing the tarball, `<tarball>.sha256` must be in the same directory. Executing `sudo ./install.sh install` from the publishing package root will automatically use the currently verified package without accessing the network. The following is an example of the v1.2.0 asset path format; the official Release download and signature verification must be completed before use:

```bash
sudo ./install.sh install --from /srv/releases/agentsql-v1.2.0-linux-amd64
sudo ./install.sh upgrade --from /srv/releases/agentsql-v1.2.0-linux-amd64.tar.gz
```

`--no-start` only for chroot or mirror pre-installation: allows writing files when systemd is not PID 1, but does not start or health check, and the installation results are not available until systemd starts successfully. The upgrade only automatically backs up `/var/lib/agentsql/agentsql.db` and its `-wal` and `-shm` by default; when using the PostgreSQL control plane or a custom SQLite path, you must first complete an external consistency backup and explicitly pass `--external-backup-done`. The installer will not claim that the external database has been backed up. If the health check after the external database upgrade fails, the installer will restore the binary, unit, configuration and environment files, but will keep the old service stopped; the operator must first restore the external database snapshot before starting the old version.

By default, the download only uses GitHub Release and will not switch mirrors when verification fails. Controlled networks can be set explicitly`AGENTSQL_DOWNLOAD_BASE`It is a self-built HTTPS asset root; this root needs to be`<base>/<version>/agentsql-<version>-linux-<amd64|arm64>.tar.gz[.sha256]`Provide source files, and the installer selects according to the local architecture. Installv1.2.0should be clear when`--version`,For example:

```bash
sudo sh ./install.sh install --version v1.2.0
```

If the installer finds `restorecon`, it will restore the SELinux default context of the binary, unit, configuration, and data directories, without calling `chcon` or turning off enforcing; when encountering a rejection, use `ausearch -m AVC` to investigate. The license for publishing the tarball is located at `LICENSE` at the root of the package, and at `/usr/share/licenses/agentsql/LICENSE` within the container image.

## Method 3: systemd

The following uses the root directory of the downloaded, decompressed and signed v1.2.0 Linux distribution package as the working directory to install the binary and service files:

```bash
sudo useradd --system --user-group --home-dir /var/lib/agentsql --shell /usr/sbin/nologin agentsql
sudo install -d -o root -g agentsql -m 0750 /etc/agentsql
sudo install -d -o agentsql -g agentsql -m 0750 /var/lib/agentsql
sudo install -m 0755 ./agentsql ./agentsqlctl /usr/local/bin/
sudo install -m 0644 deploy/systemd/agentsql.service /etc/systemd/system/agentsql.service
sudo install -o root -g agentsql -m 0640 deploy/systemd/config.yaml /etc/agentsql/config.yaml
```

If user `agentsql` already exists in the system, skip `useradd`. The systemd-specific configuration template has been fixed to only listen to `127.0.0.1:7780` and write SQLite data to `/var/lib/agentsql/agentsql.db`. There is no need to manually modify `sqlite_path`. The bare metal only listens to `127.0.0.1` by default; if remote access is required, SSH local forwarding or controlled reverse proxy (TLS/authentication) should be used. Do not directly change `http_listen` to `0.0.0.0` to expose the public network.

Redistribution of YashanDB C client has been verbally authorized by the manufacturer; the repository does not have written authorization documents. The v0.5.1 distribution package is built using the independent YashanDB C client 23.4.7.100, which is packaged according to the architecture after SHA-256 verification. When building from the source code, you need to obtain the same version of the client, use `CGO_ENABLED=1` and `-tags yashan` to compile the Go driver, and add the client `lib` directory before starting. `LD_LIBRARY_PATH`. The C library in the server image returns `YAS-02143` in the historical Go driver probe and is not used as a distribution runtime library.

v0.5.1 The client directory used to build the GHCR image is `/opt/yashandb-client/lib`. The final acceptance of the dual-architecture content of the public image shall prevail. When the Go driver or client is missing, the YashanDB connection fails to close. The standard parser and the narrow SELECT path have been wired; the independent client in the old unreleased dry-run package writes reject probes through direct connection to real database parameter binding and read-only accounts. In addition, the real database executor and test fixture audit port are used to verify SELECT, column lineage, R010, mobile phone number masking and three audits; HTTP/MCP, persistence audit and minimum privilege account and pipeline combination are still untested, and write, transaction and EXPLAIN still fail to close.

Create an environment file readable only by root:

```bash
sudo sh -c 'cat > /etc/agentsql/agentsql.env <<EOF
AGENTSQL_SECRET=<GENERATE_YOUR_OWN_32_BYTE_SECRET>
AGENTSQL_ADMIN_USER=admin
AGENTSQL_ADMIN_PASSWORD=<GENERATE_YOUR_OWN_STRONG_PASSPHRASE>
EOF'
sudo chmod 0600 /etc/agentsql/agentsql.env
sudo systemctl daemon-reload
sudo systemctl enable --now agentsql
sudo systemctl status agentsql
```

Replace the two placeholders with the randomly generated commands from earlier in this section before starting the service. The placeholder itself cannot be used for startup.

### Bare metal using the PostgreSQL control plane

SQLite is the default zero-configuration way to get started. If you want to put metadata and audit into the PostgreSQL independent audit library, first prepare the database, one-time migration owner and the minimum privilege running account according to the "PostgreSQL control plane deployment" earlier in this document. exist`/etc/agentsql/agentsql.env`append in`AGENTSQL_STORE_METADATA_DSN`、`AGENTSQL_STORE_AUDIT_DSN`;DSN is only provided through environment variables, do not write to configuration files. again`/etc/agentsql/config.yaml`of`store`Change to equivalent`metadata` / `audit`form, please refer to`examples/docker/config.postgres.yaml`and`examples/docker/config.controlplane.yaml`, but bare metal`server.http_listen`must remain as`127.0.0.1:7780`。

Before starting for the first time, run in a root or equivalent controlled environment where the above environment variables have been safely loaded:

```bash
agentsqlctl migrate --config /etc/agentsql/config.yaml
```

systemd then runs the service as user `agentsql`; the permission boundaries of the migrated account and the minimum-privileged running account, and the account disposal method after the migration is completed are the same as before.

## explore life

`/healthz` is an authentication-free survival check; `/readyz` will check within 1 second whether the configured metadata and audit storage are available.

```bash
agentsqlctl health --url http://127.0.0.1:7780/healthz
curl --fail http://127.0.0.1:7780/readyz
```

Only HTTP 200 and JSON`status`for`ok`hour,`agentsqlctl health`before returning success.

## Upgrades and rollbacks

Before upgrading, perform a full backup, inventory the enabled `hash` rules and confirm that the corresponding `AGENTSQL_REDACTION_HASH_KEY` has been safely injected, then stop the current instance, replace the two binary or container images, and restart. When `store.auto_migrate: true` (default value), AgentSQL startup will automatically and idempotently execute unapplied migrations on the currently configured SQLite or PostgreSQL metadata storage; the audit-only PostgreSQL deployed in the sub-database will go through an independent migration flow. When `auto_migrate: false` is started, only the version is checked and DDL is not executed. `agentsqlctl migrate` must be run by the migration owner first. enabled `hash` without key will refuse to start before serving external services.

### Pre-upgrade pre-check and explicit migration of `auto_migrate: false`

use**Version to be deployed**of`agentsqlctl`The same configuration and environment variables that will be used for the service, first check the configuration and database version:

```bash
agentsqlctl check-config --config /etc/agentsql/config.yaml
agentsqlctl migrate --check --config /etc/agentsql/config.yaml
```

`migrate --check` only reads `schema_migrations` and does not execute DDL; the merged storage outputs one row of `role=metadata`, and the independent audit library storage outputs `role=metadata` and `role=audit` respectively. Each row has `driver`, `current`, `latest`, `status`. The exit code `0` means that all configured migration flows are `status=current`; `2` means that at least one is `status=pending`, which is the expected result before the upgrade. **cannot start new services based on this.**; `1` Indicates errors in configuration, connection, missing version table, migration history, or advanced version. The cause should be investigated first. The check will fail if the SQLite path does not exist and an empty library will not be created. To install an empty library for the first time, directly execute `migrate` below, and then execute `--check`.

Production upgrades are performed in the following order; PostgreSQL's DSN is injected through a protected environment variable and cannot be written on the command line, config repository, or terminal log:

1. Confirm the binary to be deployed, the configuration and the above pre-check results, stop all AgentSQL instances and writes, and then make a consistent backup of the actual configured metadata and audit storage, while retaining the matching `AGENTSQL_SECRET`. The merged database is backed up once; when deploying separate databases, the two databases must be backed up separately and the coordinated recovery points must be recorded. Practice recovery first, and then enter the migration window.
2. PostgreSQL temporarily switches to a migration owner DSN with DDL permissions; SQLite uses a writable and backed-up database file. Keep the service stopped and run `agentsqlctl migrate --config /etc/agentsql/config.yaml`. This command will execute DDL; the independent audit library is migrated after the metadata, and there are no atomic transactions between the two libraries. If any step fails, do not start a new version. Keep the site and process it according to the backup recovery plan.
3. Continue to use the version of `agentsqlctl migrate --check --config /etc/agentsql/config.yaml` to be deployed. The exit code is required to be `0`, and each line is `current=latest`, `status=current`. Complete the minimum permissions of the running account for the new table, index, and sequence, then switch the DSN back to the running account, and perform the same check again to confirm that the running account can read both versions of the table.
4. Replace the service binary or image, check `/readyz` after startup, and accept the applicable allow, deny, masking, approval, and persistent audit writes. Stop the service when any key check fails, and restore the database according to the backup before the upgrade, corresponding to `AGENTSQL_SECRET` and the old version of the program; do not just roll back the binary and continue to use the new schema.

For the v0.8.0 upgrade, first stop writing to the old service and back up metadata, audit (if branched) and the paired `AGENTSQL_SECRET` in a complete set. The SQLite merge library must use consistent backups and cannot just copy the main database file that has WAL pages that are not merged. The isolation drill has verified that upgrading from v0.7.0 to v0.8.0, then restoring the pre-upgrade backup and starting from v0.7.0: SQLite metadata 18 → 19 → 18; PostgreSQL metadata/audit sub-library 17/6 → 18/6 → 17/6. Old programs cannot directly read the new version of the schema; production deployments must still be independently drilled and restored based on the target environment permissions and data volume.

For the v0.9.0 upgrade, first use the v0.8.0 official signature package and pairing key to make a complete set of backups in an isolation environment, then stop writing and run `agentsqlctl migrate --check`. SQLite merged library metadata 19 → 20; PostgreSQL independent metadata 18 → 19, audit-only remains 6. Added `max_uses` (can be empty) and `used_count` (default 0); the old authorized empty budget will continue to be valid. The rollback must restore the same snapshot of metadata, audit (when splitting the database) and `AGENTSQL_SECRET` before the upgrade, and then start it with v0.8.0; directly executing the old binary on the new version of the schema is not supported. SQLite must still be backed up consistently, and production environments need to separately verify table locks, backup recovery, and actual data volume.

For the v1.0.0 upgrade, first back up the metadata, audit (if branched) and paired `AGENTSQL_SECRET` of v0.9.0, and then use the new version of `agentsqlctl migrate --check` to verify the two migration flows. There is no new schema migration in this version. SQLite metadata remains at 20, PostgreSQL independent metadata remains at 19, and audit-only remains at 6. Check existing column policy and PostgreSQL B2 health before startup: SELECT with column policy configured but B2 unavailable will be rejected before v1.0.0 is executed. Human delegation is off by default; verify that the client can safely set the second HTTP request header before enabling it. The rollback must stop the new service and restore the complete backup set and the old program, not just replace the binary.

For the v1.2.0 upgrade, start from the same snapshot of the v1.1.0 official package and its metadata, audit (if branched), and `AGENTSQL_SECRET`. First run `agentsqlctl migrate --check`, stop writing and complete consistent backup, and then perform migration to the new version. This version is not expected to increase the schema: SQLite and PostgreSQL combined library will remain at 21, independent PostgreSQL metadata will remain at 20, and audit-only will remain at 6. Rolling back still requires stopping the service and restoring the entire set of pre-upgrade snapshots and v1.1.0 binaries, and then verifying the audit chain, approval status, and old MCP entry. Before enabling OAuth, you must configure the issuer and tenant that are exactly the same as the console OIDC, the resource URI for `/mcp/oauth`, the client's `agentsql:mcp` scope, and the independent Agent Key request header; first reject verification of missing/wrong credentials.

History v0.7.0 rollback: The SQLite merge library can return to v0.6.0 after restoring the pre-upgrade database and pairing key; the PostgreSQL metadata/audit sub-library falls back to [v0.6.1 and the official version ](https://github.com/cuipengdba/agentsql/releases/tag/v0.6.1). The original v0.6.0 branch service has a management audit routing defect and cannot be used as a fallback target for this topology. See [v0.7.0 release notes ](release-notes-v0.7.0.md) for historical details.

`agentsqlctl upgrade apply --dry-run` only verifies the downloaded product and file plan, does not check the database version, and does not replace the above pre-check or backup. `migrate --check` verifies the continuity of migrated version records, not a complete verification of database structure, data content, or cross-database consistency.

For complete on-call operations and troubleshooting procedures, see the [current operations guide](OPERATIONS.md).

### v0.3 `range` Control plane migration

v0.3To save numerical bucketing and date truncation parameters, in`mask_rules`Increase`range_bucket_width INTEGER`、`range_bucket_offset INTEGER`、`range_granularity TEXT`Three nullable columns. None of the three columns`NOT NULL`, there is no database default value, migration does not backfill, the three columns of the existing rules are naturally`NULL`. four servings`0004`The function and real DDL are as follows.

- `internal/store/migrations/sqlite/0004_range_redaction.sql`: Default merged SQLite control plane.

```sqlite
ALTER TABLE mask_rules ADD COLUMN range_bucket_width INTEGER;
ALTER TABLE mask_rules ADD COLUMN range_bucket_offset INTEGER;
ALTER TABLE mask_rules ADD COLUMN range_granularity TEXT;
```

- `internal/store/migrations/postgres/0004_range_redaction.sql`: Default merged PostgreSQL control plane.

```postgres
ALTER TABLE mask_rules ADD COLUMN range_bucket_width INTEGER;
ALTER TABLE mask_rules ADD COLUMN range_bucket_offset INTEGER;
ALTER TABLE mask_rules ADD COLUMN range_granularity TEXT;
```

- `internal/store/migrations/metadata/sqlite/0004_range_redaction.sql`: Independent SQLite metadata library deployed in sub-library.

```sqlite
ALTER TABLE mask_rules ADD COLUMN range_bucket_width INTEGER;
ALTER TABLE mask_rules ADD COLUMN range_bucket_offset INTEGER;
ALTER TABLE mask_rules ADD COLUMN range_granularity TEXT;
```

- `internal/store/migrations/metadata/postgres/0004_range_redaction.sql`: Independent PostgreSQL metadata library deployed in sub-library.

```postgres
ALTER TABLE mask_rules ADD COLUMN range_bucket_width INTEGER;
ALTER TABLE mask_rules ADD COLUMN range_bucket_offset INTEGER;
ALTER TABLE mask_rules ADD COLUMN range_granularity TEXT;
```

The audit-only library does not have `mask_rules`, so `internal/store/migrations/audit` does not add these three columns, and there is no corresponding `0004_range_redaction.sql`.

#### PostgreSQL upgrade impact and operation sequence

These three `ADD COLUMN` in PostgreSQL all add columns that are nullable and have no default value. They usually only update the system catalog and do not rewrite the `mask_rules` table; however, `ALTER TABLE` will still obtain the `ACCESS EXCLUSIVE` lock, possibly waiting for existing transactions and briefly blocking concurrent access during execution. Don't describe it as a "lockless upgrade". Even if the table is small, it is recommended to execute it during off-peak or maintenance windows and set an appropriate lock wait limit according to the change process.

The recommended sequence is: stop AgentSQL writing; back up the SQLite file, or back up the PostgreSQL metadata/audit library separately as mentioned above; apply migration (PostgreSQL uses migration owner); perform the read-only preflight in the next section and process the returned rows; finally change back to the minimum privilege running account, start the service and check `/readyz`. In sub-library deployment, only the metadata database will experience this DDL, but the paired control plane backup and the corresponding `AGENTSQL_SECRET` should still be retained.

Rollback doesn't just replace the binary. Stop the service first, and disable or remove the `range` rule that is not recognized by the old version before going back to the old version; schema rollback should remove these three columns, or directly restore the backup before the upgrade. Judging from the column structure alone, these three columns can be empty, have no default, and do not change the old columns. They are harmless to older versions that use explicit column lists. However, the older versions do not recognize the `range` algorithm and its parameters. It cannot be assumed that the enabled `range` rule can also run compatibly. Restoring a backup is the first choice when you need to completely return to the pre-upgrade state.

#### enabled `range` pre-check before starting the rule

Execute the following read-only query after `0004` has been applied and before restarting the new version. Only rules with `algo='range'` or any range parameter other than `NULL` enter this group of checks; the query only returns enabled dirty rules and does not modify the data. The result must be empty, otherwise `validateEnabledRules` will fail-fast when starting the assembly. disabled Dirty drafts do not prevent startup and can be fixed via the management API after service is restored.

The `number` rule requires that `range_bucket_width` be an integer of `1..1,000,000,000`; `range_bucket_offset` can be `NULL` (normalized to `0`), and the explicit value must be an integer of `-1,000,000,000..1,000,000,000`, and must not contain `range_granularity`. The `date` rule must not have width/offset; `range_granularity` can be `NULL` (normalized to `year`). Only explicit values ​​are allowed: `year`, `quarter`, and `month`. Empty strings will also be detected. Non-`range` algorithms carrying any range parameters are also dirty data.

SQLite：

```sqlite
SELECT id,
       datasource_id,
       column_name,
       sensitive_type,
       algo,
       range_bucket_width,
       range_bucket_offset,
       range_granularity
FROM mask_rules
WHERE enabled = 1
  AND (
    (
      algo = 'range'
      AND (
        sensitive_type NOT IN ('number', 'date')
        OR (
          sensitive_type = 'number'
          AND (
            range_bucket_width IS NULL
            OR typeof(range_bucket_width) <> 'integer'
            OR range_bucket_width < 1
            OR range_bucket_width > 1000000000
            OR (
              range_bucket_offset IS NOT NULL
              AND (
                typeof(range_bucket_offset) <> 'integer'
                OR range_bucket_offset < -1000000000
                OR range_bucket_offset > 1000000000
              )
            )
            OR range_granularity IS NOT NULL
          )
        )
        OR (
          sensitive_type = 'date'
          AND (
            range_bucket_width IS NOT NULL
            OR range_bucket_offset IS NOT NULL
            OR (
              range_granularity IS NOT NULL
              AND range_granularity NOT IN ('year', 'quarter', 'month')
            )
          )
        )
      )
    )
    OR (
      algo <> 'range'
      AND (
        range_bucket_width IS NOT NULL
        OR range_bucket_offset IS NOT NULL
        OR range_granularity IS NOT NULL
      )
    )
  )
ORDER BY id;
```

PostgreSQL's `INTEGER` column itself only holds integers, so there is no need for SQLite's storage type check:

```postgres
SELECT id,
       datasource_id,
       column_name,
       sensitive_type,
       algo,
       range_bucket_width,
       range_bucket_offset,
       range_granularity
FROM mask_rules
WHERE enabled IS TRUE
  AND (
    (
      algo = 'range'
      AND (
        sensitive_type NOT IN ('number', 'date')
        OR (
          sensitive_type = 'number'
          AND (
            range_bucket_width IS NULL
            OR range_bucket_width < 1
            OR range_bucket_width > 1000000000
            OR range_bucket_offset < -1000000000
            OR range_bucket_offset > 1000000000
            OR range_granularity IS NOT NULL
          )
        )
        OR (
          sensitive_type = 'date'
          AND (
            range_bucket_width IS NOT NULL
            OR range_bucket_offset IS NOT NULL
            OR (
              range_granularity IS NOT NULL
              AND range_granularity NOT IN ('year', 'quarter', 'month')
            )
          )
        )
      )
    )
    OR (
      algo <> 'range'
      AND (
        range_bucket_width IS NOT NULL
        OR range_bucket_offset IS NOT NULL
        OR range_granularity IS NOT NULL
      )
    )
  )
ORDER BY id;
```

#### SQLite → PostgreSQL migration

`migrate-sqlite-to-postgres`of`mask_rules`manifest is already there`enabled`later incorporated`range_bucket_width`、`range_bucket_offset`、`range_granularity`, generated from the same manifest.`SELECT`Write with destination. Relocation value-by-value fidelity: SQL`NULL`with explicit`range_bucket_offset=0`No confusion,`range_bucket_width=25`、`range_granularity='quarter'`Equivalent values ​​will also be carried as they are, without the need to manually fill in columns, fill in default values, or perform additional conversions.

Generally, rollback does not just replace the binary: stop the service, restore the binary or image to the old version, restore the SQLite or PostgreSQL backup before the upgrade, and then start the service. Don't let older versions directly read data that contains rules they don't recognize.

### v0.3 Table. Column-aware masking control plane migration (0005)

`0005_mask_rule_scope.sql` only modifies `mask_rules` in the AgentSQL control plane and does not execute DDL on the protected PostgreSQL/MySQL business database. It exists in four migration flows with metadata; the audit-only library does not have `mask_rules`, and therefore does not have `0005`:

| Deployment form | Actual migration files | SHA-256 |
| --- | --- | --- |
| Default merged SQLite (metadata + audit) | `internal/store/migrations/sqlite/0005_mask_rule_scope.sql` | `ea43d52616b75fcb3385d021b25a5bfbea08866ff46236026ba0413180c2784f` |
| Standalone metadata SQLite | `internal/store/migrations/metadata/sqlite/0005_mask_rule_scope.sql` | `ea43d52616b75fcb3385d021b25a5bfbea08866ff46236026ba0413180c2784f` |
| Default merged PostgreSQL (metadata + audit) | `internal/store/migrations/postgres/0005_mask_rule_scope.sql` | `eb7946179a70742d6d19a1c45d4c2901c846d201a9f1a52764bca123b6eea848` |
| Standalone metadata PostgreSQL | `internal/store/migrations/metadata/postgres/0005_mask_rule_scope.sql` | `eb7946179a70742d6d19a1c45d4c2901c846d201a9f1a52764bca123b6eea848` |

#### Actual DDL, execution order and inventory semantics

The two SQLite files are verbatim identical and are executed in a migration transaction in the following order:

```sqlite
ALTER TABLE mask_rules ADD COLUMN schema_name TEXT;

UPDATE mask_rules SET schema_name = '', table_name = '';

DROP INDEX IF EXISTS ux_mask_rules_scope_column;

CREATE UNIQUE INDEX ux_mask_rules_scope_column
ON mask_rules (
  COALESCE(NULLIF(TRIM(datasource_id),''),''),
  schema_name,
  table_name,
  LOWER(TRIM(column_name))
);
```

The two PostgreSQL files are also verbatim identical, with the only dialect difference being the use of `BTRIM`:

```postgres
ALTER TABLE mask_rules ADD COLUMN schema_name TEXT;

UPDATE mask_rules SET schema_name = '', table_name = '';

DROP INDEX IF EXISTS ux_mask_rules_scope_column;

CREATE UNIQUE INDEX ux_mask_rules_scope_column
ON mask_rules (
  COALESCE(NULLIF(BTRIM(datasource_id),''),''),
  schema_name,
  table_name,
  LOWER(BTRIM(column_name))
);
```

`schema_name` is nullable at the DDL layer and `TEXT` has no default value; the applied INSERT/UPDATE path will write the null scopes of `schema_name` and `table_name` as empty strings. The `UPDATE` being migrated will set both columns of all existing rows to `''` at the same time, instead of retaining the old `table_name`: v0.3 discarded `table_name` when running before, so this time it is solidified as a "global column rule" at once, maintaining the actual effective semantics before the upgrade. The datasource in the new unique key still puts `NULL`, empty strings and pure blanks into the same scope. The schema/table is accurately distinguished according to the original value. Only `column_name` is trimmed and normalized to lowercase.

The three-level storage semantics are as follows:

| `schema_name` | `table_name` | Scope |
| --- | --- | --- |
| `''` | `''` | Global column rules |
| `''` | Not empty | table-only rule; PostgreSQL matches any schema, MySQL represents the table in the current database |
| Not empty | Not empty | Exact `schema.table.column` rule |

`schema_name` is not empty and `table_name` is empty, which is not a legal scope. schema/table comparisons preserve case and match exactly; column continues to use relaxed normalized column names.

#### Execution method and version check

When `auto_migrate: true` is set, the default merged SQLite, independent metadata SQLite, default merged PostgreSQL, and independent metadata PostgreSQL will all execute their respective `0005` when starting to prepare metadata storage. It is recommended to continue using `auto_migrate: false` for production PostgreSQL: first stop writing, explicitly execute migration as migration owner, and then change back to the minimum privilege running account.

```bash
agentsqlctl migrate --config /etc/agentsql/config.yaml
```

The successful output of merged storage should be version 5 of the corresponding driver:

```text
migration driver=sqlite current=5 latest=5
```

or:

```text
migration driver=postgres current=5 latest=5
```

When metadata/audit splits the database, the metadata line should be `current=5 latest=5`; the migration flow of independent audit PostgreSQL is still `current=2 latest=2`:

```text
metadata migration driver=postgres current=5 latest=5
audit migration driver=postgres current=2 latest=2
```

#### PostgreSQL locks, time-consuming and pre-upgrade pre-checking

PostgreSQL's `ALTER TABLE ... ADD COLUMN` will acquire the `ACCESS EXCLUSIVE` lock; then `UPDATE` will rewrite all `mask_rules` stock rows. Ordinary `DROP INDEX` / `CREATE UNIQUE INDEX` are not concurrent versions. The new index needs to scan and sort the table data, and the relevant locks will not be released in advance before the migration transaction is submitted. `mask_rules` is typically small, but large tables, long transactions, slow storage, or lock waits may still extend maintenance windows and block concurrent reads and writes. You should first perform a drill with a production-scale copy in pre-distribution, record the time consumption and lock wait, stop AgentSQL writing during off-peak or maintenance windows before upgrading, and set lock wait/statement timeout according to platform specifications.

The database that actually carries metadata must be backed up before upgrading; for sub-database deployment, the paired control plane backup and the corresponding `AGENTSQL_SECRET` should still be retained as mentioned above. For example PostgreSQL:

```bash
pg_dump -Fc --dbname "$AGENTSQL_STORE_METADATA_DSN" --file agentsql-metadata-before-0005.dump
```

SQLite should use a consistent backup after stopping writing; when using the `sqlite3` CLI you can do:

```bash
sqlite3 /var/lib/agentsql/agentsql.db ".backup 'agentsql-before-0005.db'"
```

Before upgrading PostgreSQL, record the number of rules, the usage of old `table_name`, and confirm that there are no duplication of old keys caused by empty column names, NULL scopes, or bypassing constraints. `legacy_nonempty_table_names` is only used to evaluate the impact: these values ​​will be cleared by `0005`.

```postgres
SELECT COUNT(*) AS total_rules,
       COUNT(*) FILTER (WHERE COALESCE(table_name, '') <> '') AS legacy_nonempty_table_names,
       COUNT(*) FILTER (
         WHERE table_name IS NULL OR column_name IS NULL OR BTRIM(column_name) = ''
       ) AS invalid_rows
FROM mask_rules;

SELECT COALESCE(NULLIF(BTRIM(datasource_id),''),'') AS datasource_scope,
       LOWER(BTRIM(column_name)) AS column_key,
       COUNT(*) AS duplicate_count
FROM mask_rules
GROUP BY 1, 2
HAVING COUNT(*) > 1;
```

SQLite equivalent preflight:

```sqlite
SELECT COUNT(*) AS total_rules,
       SUM(CASE WHEN COALESCE(table_name, '') <> '' THEN 1 ELSE 0 END) AS legacy_nonempty_table_names,
       SUM(CASE
             WHEN table_name IS NULL OR column_name IS NULL OR TRIM(column_name) = '' THEN 1
             ELSE 0
           END) AS invalid_rows
FROM mask_rules;

SELECT COALESCE(NULLIF(TRIM(datasource_id),''),'') AS datasource_scope,
       LOWER(TRIM(column_name)) AS column_key,
       COUNT(*) AS duplicate_count
FROM mask_rules
GROUP BY 1, 2
HAVING COUNT(*) > 1;
```

Both `invalid_rows` and duplicate queries should return zero; otherwise stop the upgrade first and fix via a supported management API or audited manual changes. Don’t use a generic script that removes duplicate rows as a substitute for confirming rule intent on a case-by-case basis.

#### Check after migration and before startup

PostgreSQL uses the following read-only query to confirm column, index definition, and new key uniqueness. Column queries should see `schema_name`, `table_name`; the index definition should contain four parts: datasource, schema, table and normalized column; the last two exception queries should return zero rows.

```postgres
SELECT column_name, data_type, is_nullable, column_default
FROM information_schema.columns
WHERE table_schema = 'public'
  AND table_name = 'mask_rules'
  AND column_name IN ('schema_name', 'table_name')
ORDER BY ordinal_position;

SELECT indexname, indexdef
FROM pg_indexes
WHERE schemaname = 'public'
  AND tablename = 'mask_rules'
  AND indexname = 'ux_mask_rules_scope_column';

SELECT id, datasource_id, schema_name, table_name, column_name
FROM mask_rules
WHERE schema_name IS NULL
   OR table_name IS NULL
   OR (schema_name <> '' AND table_name = '')
   OR column_name IS NULL
   OR BTRIM(column_name) = '';

SELECT COALESCE(NULLIF(BTRIM(datasource_id),''),'') AS datasource_scope,
       schema_name,
       table_name,
       LOWER(BTRIM(column_name)) AS column_key,
       COUNT(*) AS duplicate_count
FROM mask_rules
GROUP BY 1, 2, 3, 4
HAVING COUNT(*) > 1;
```

SQLite can use `PRAGMA` and `sqlite_master` to do the same check:

```sqlite
PRAGMA table_info(mask_rules);

SELECT name, sql
FROM sqlite_master
WHERE type = 'index'
  AND tbl_name = 'mask_rules'
  AND name = 'ux_mask_rules_scope_column';

SELECT id, datasource_id, schema_name, table_name, column_name
FROM mask_rules
WHERE schema_name IS NULL
   OR table_name IS NULL
   OR (schema_name <> '' AND table_name = '')
   OR column_name IS NULL
   OR TRIM(column_name) = '';

SELECT COALESCE(NULLIF(TRIM(datasource_id),''),'') AS datasource_scope,
       schema_name,
       table_name,
       LOWER(TRIM(column_name)) AS column_key,
       COUNT(*) AS duplicate_count
FROM mask_rules
GROUP BY 1, 2, 3, 4
HAVING COUNT(*) > 1;
```

Finally, run `agentsqlctl migrate` again, assert metadata `current=5 latest=5`, change back to the running account, start as `auto_migrate:false` and check `/readyz`.

#### Rollback and manual down

The preferred rollback path is to stop the service, restore the pre-upgrade backup and its paired `AGENTSQL_SECRET`, and then start the old version. The repository does not have automatic down migration, you cannot just replace the binary and assume automatic downgrade.

Code confirms: Older versions used explicit column lists, so one more column was left empty`schema_name`Column will not cause the old SQL to immediately report "column number mismatch"; but the old version does not understand the schema/table scope, and the old unique key only allows one normalized column rule with the same name in each datasource scope. The new index allows different tables to have one column rule with the same name. When the old version loads multiple enabled rules with the same name, it will refuse to start as a duplicate column rule; even if there is only one scoped rule, the old version will ignore it.`table_name`, execute it according to global column rules. There are other version access control differences: the old version is in`auto_migrate:false`sometimes depending on the database`current=5`, code`latest=4`Refuses to start. Therefore, retain`0005`The schema only rolls back code that is not a supported path.

If the backup cannot be restored, it must be manually down while the new version is still running and the service has stopped writing. First export all the rules, delete, merge or rewrite the rules of `schema_name <> '' OR table_name <> ''` one by one, so that there is only one old key for each, and clearly accept that it will become a global column rule in the old version; table-only or schema.table semantics cannot be retained without loss. The following repeated query must return zero rows before continuing:

```postgres
SELECT COALESCE(NULLIF(BTRIM(datasource_id),''),'') AS datasource_scope,
       LOWER(BTRIM(column_name)) AS column_key,
       STRING_AGG(id, ',' ORDER BY id) AS rule_ids
FROM mask_rules
GROUP BY 1, 2
HAVING COUNT(*) > 1;
```

The structural steps of PostgreSQL manual down are as follows; it should be verified in advance and executed by the migration owner in the transaction:

```postgres
BEGIN;
DROP INDEX IF EXISTS ux_mask_rules_scope_column;
ALTER TABLE mask_rules DROP COLUMN schema_name;
CREATE UNIQUE INDEX ux_mask_rules_scope_column
ON mask_rules (
  COALESCE(NULLIF(BTRIM(datasource_id),''),''),
  LOWER(BTRIM(column_name))
);
DELETE FROM schema_migrations WHERE version = 5;
COMMIT;
```

SQLite first uses the equivalent `TRIM` above to repeat the query to confirm that the old key is unique, and then execute:

```sqlite
BEGIN IMMEDIATE;
DROP INDEX IF EXISTS ux_mask_rules_scope_column;
ALTER TABLE mask_rules DROP COLUMN schema_name;
CREATE UNIQUE INDEX ux_mask_rules_scope_column
ON mask_rules (
  COALESCE(NULLIF(TRIM(datasource_id),''),''),
  LOWER(TRIM(column_name))
);
DELETE FROM schema_migrations WHERE version = 5;
COMMIT;
```

If the SQLite CLI of the operation and maintenance environment does not support `DROP COLUMN`, do not temporarily assemble unverified table reconstruction scripts; restore the backup before the upgrade, or verify the equivalent process of "new old structured table, explicit column copy, rebuild index, and atomic table swap" in advance. After the manual down is completed, check `current=latest=4` with the old version `agentsqlctl migrate`, and then start the old service.

#### SQLite → PostgreSQL migration

`migrate-sqlite-to-postgres`of`mask_rules`manifest also contains`table_name`and`schema_name`, source`SELECT`, the destination INSERT and digest hash are generated from the same list of columns. Relocating two columns value by value, SQL`NULL`will not be confused with the empty string; the target PostgreSQL will first execute to metadata version 5, and then the target`0005`isomorphic`(datasource, schema, table, normalized column)`Unique index. That is, the rule data is copied value-by-value, and the index semantics are reconstructed by the target-side migration rather than copying SQLite's`sqlite_master`definition. The new key repeat query in this section should be executed before and after relocation, and the number of rule rows and scope distribution should be checked.

## Publish and distribute

The current release candidate is v4.3.0, and the maintenance version is executed according to the [ each version delivery process ](RELEASE_ROUTINE.md). For the current assets and verification steps, see [ for issuing assets ](RELEASE_ASSETS.md), and for actual results, see [ for issuing evidence ](v4.0.0-acceptance.md). Complete and clean submissions must be locked before release, and source code, testing and build input must be checked before and after execution; old packages or placeholder signatures cannot be reused.

```powershell
# A complete dual-architecture preview without signing or pushing.
pwsh ./scripts/release-dryrun.ps1 -Version v4.3.0

# Production ready: Independently verified controlled public keys must be used.
$trustedReleasePublicKey = '<trusted-public-key-absolute-path>'
pwsh ./scripts/release-dryrun.ps1 -Version v4.3.0 -ProductionPrepare -PublicKeyPath $trustedReleasePublicKey

# After signing and acceptance, push the exact version image from the same annotated tag.
pwsh ./scripts/build-ghcr-multiarch.ps1 -Version v4.3.0 -IncludeAuxiliaryTags -Push
```

Production preparation generates 11 assets to be signed, 15 in total after signing SBOM, provenance, and SHA256SUMS. Perform three Ed25519 signature verifications to check packages, source submissions, versions, dual architectures, runtime libraries and anonymous downloads. GHCR main image, Demo, quickstart must be public and contain amd64/arm64; pull, run and record the actual digest in a non-login environment. After the official website and demo are passed, use the accepted master image summary to separately upgrade `latest`, and re-verify anonymously. In the past, the phased strategy of suspending the official website for v1.1–v1.4 has ended and cannot be used as a basis for skipping the official website and demo in the current version.

`scripts/package-release.sh` does not overwrite the existing `bin/SHA256SUMS`; `SOURCE_DATE_EPOCH` controls reproducible tar times. GitHub production and installers use strict `vX.Y.Z`. The build must still comply with glibc, CGO and vendor runtime requirements, see Precompiled Binary System Requirements on this page.

## The iron rule of backup

Must be backed up in pairs:

1. `agentsql.db` SQLite file.
2. `AGENTSQL_SECRET` corresponding to this database.

Both are indispensable. When the key is lost, the encrypted data source password in SQLite cannot be decrypted. Stop writing or use the SQLite consistent backup mechanism before copying SQLite files; do not copy just the primary file being written.

Replacing `AGENTSQL_SECRET` will make the existing data source password unable to be decrypted. This is not a lossless key rotation. v0.1 does not have an online re-encryption process; when replacement is required, data source credential re-entry and rollable SQLite backup must be planned first.

## local test mode

`AGENTSQL_INSECURE=1` can only be set if local development or testing requires public test credentials for compatible repository history. It only allows "public test SECRET of correct length" and "non-empty weak administrator password"; SECRET is missing or is not 32 bytes, and the administrator password is empty will still refuse to start. This switch does not and must not turn off authentication, security rules, or fail-closed behavior, and is prohibited from being set in production environments.

## B2 PostgreSQL column-level authorization (enabled by default)

B2 column-level SELECT has the factory default `enabled: true` on PostgreSQL; ordinary schema-qualified base tables use the installation-free extension `CATALOG_CLOSED_V1` by default, and the native `agentsql_binder` is only used for optional enhancements of view / matview / complex lineage. MySQL does not enter the B2 PostgreSQL path. The following is the equivalent explicit configuration; only explicitly set to `false` if you need to fall back to existing table-level protection:

```yaml
column_authorization:
  enabled: true
  instance_id: gateway-prod-a
  lease_ms: 15000
  heartbeat_interval_ms: 5000
```

PG14–18 catalog binding, enrollment, control fence and request reservation are checked at startup; also verified when using native binder`agentsql_binder`ABI/capability and signed digest.`/healthz`and`/readyz`of`b2.state/reason`and management audit records clearly giving status. When the default enforcement activation fails or the running lease expires, the PostgreSQL B2 column-level entry fails-closed and readiness becomes unavailable, and will not be silently downgraded to table-level authorization; only explicit`enabled: false`Or dry-run before taking the existing table-level path.

## MCP transport session and B5 PostgreSQL session (enabled by default)

The Streamable HTTP transport session is independently configured via `mcp.http` and is enabled by default with an idle timeout of 10 minutes. It is not the same layer as the B5 logical session used by `open_session`/`close_session`: turning off `mcp.sessions.enabled` does not close the transport session, nor does setting `mcp.http.stateful: false` hide the B5 tool.

B5 cross-request logical sessions and PostgreSQL planned transactions are enabled by factory default; MySQL cross-request transactions are not supported and will refuse to start if `mcp.transactions.mysql: true` is configured. The equivalent explicit configuration is as follows; to roll back B5, the PostgreSQL transaction switches that rely on it must also be explicitly turned off:

```yaml
mcp:
  http:
    stateful: true
    session_timeout_ms: 600000
    event_store_enabled: true
    event_store_max_bytes: 67108864
    event_store_ttl_ms: 1800000
  sessions:
    enabled: true
  transactions:
    postgres: true
    mysql: false
```

`event_store_enabled`Only in stateful Streamable HTTP and protocol version earlier than`2026-07-28`When used by go-sdk. Old protocol clients can pass`GET /mcp` + `Last-Event-ID`Replay continuous SSE events during disconnection; when TTL or capacity recycling creates a gap, the server fails-closed and does not return some events. Process restart will not restore go-sdk's memory session: old`Mcp-Session-Id`Received 404 and`Mcp-Session-Expired: 1`must be re-`initialize`. Residual events are followed by`Open`/`Append`Lazy collection does not start cleaning goroutine.

Only one top-level SQL is still allowed per operation, subject to the preflight plan, session owner, connection finality, and audit barriers. `/healthz` and `/readyz` will report the B5 status; readiness fail-closed when the dependency is unavailable, and will not silently degrade MySQL or unsupported transaction forms.

## FAQ

### The container is still inaccessible after mapping the port

The listening address in the container must be `0.0.0.0:7780`. `127.0.0.1` is only bound to the container's own loopback interface, and the host port mapping cannot access it. Use `examples/docker/config.yaml`.

### Why can't I set `CGO_ENABLED=0` or use Alpine

The project's `pg_query_go` parser depends on cgo. Pure Go builds will lack necessary symbols; Alpine uses musl and does not comply with current glibc build and run constraints. The official container image is based on Debian bookworm and provides both linux/amd64 and linux/arm64 with multi-arch manifest; the official native glibc package is built using the Rocky Linux 8 (glibc 2.28) tool chain of the corresponding architecture.

### Port 7780 is occupied

Stop the occupying process, or adjust the host port mapping between `server.http_listen` and Compose in the configuration at the same time. Container internal ports should still be consistent with the configuration.

### Service prompt AGENTSQL_SECRET illegal

`AGENTSQL_SECRET` must be exactly 32 bytes, not 32 arbitrary Unicode characters. It is recommended to use 32 ASCII bytes and avoid shell newlines or extra quotes into the value itself.

### Existing data sources suddenly cannot be connected

Verify that the `AGENTSQL_SECRET` used by the service is exactly the same as when these data sources were created. When restoring a SQLite backup, the corresponding key must be restored simultaneously.

## v1.3.0 MySQL 8 single table column strategy SELECT subset

This feature is turned off by default and only covers the explicit column of MySQL 8.0/8.4, the single InnoDB base table `SELECT`. First prepare two independent accounts in the isolation test library: the business account is only granted to the `SELECT` of the required table, and the metadata account is only granted to the instance level `PROCESS`. Both must be connected to the same instance, and do not grant `PROCESS` to the business account. Example:

```sql
CREATE USER 'agentsql_reader'@'%' IDENTIFIED BY '<business-secret>';
GRANT SELECT ON app.customers TO 'agentsql_reader'@'%';
CREATE USER 'agentsql_identity'@'%' IDENTIFIED BY '<metadata-secret>';
GRANT PROCESS ON *.* TO 'agentsql_identity'@'%';
```

Put the business account into the normal data source and put the metadata password into the environment variable `AGENTSQL_MYSQL_IDENTITY_PASSWORD`. The configuration is accurately related to the data source ID:

```yaml
column_authorization:
  enabled: true
  mysql_single_table_enabled: true
  mysql_metadata_accounts:
    mysql-prod-1:
      username: agentsql_identity
      password_env: AGENTSQL_MYSQL_IDENTITY_PASSWORD
```

Create allow policies for `object_type: table` and `object_name: app.customers`, and allow policies for `object_type: column` and `object_name: app.customers` for the same Agent and data source. Read the `ETag` of the column policy response, and submit the real database column binding as an administrator or as an administrator with `strategy.manage` permissions:

```http
POST /api/v1/policies/{id}/bind-mysql
If-Match: "policy-id-r1"
Content-Type: application/json

{"schema":"app","table":"customers","grants":[{"column":"id","usage":"output"},{"column":"id","usage":"reference"},{"column":"phone","usage":"output"}]}
```

After the interface is discovered from the real database, it holds MDL verification again in the control transaction and submits healthy binding, object ID and column permissions; if the version changes or the table has been rebuilt, a conflict will be returned and the policy needs to be read again. When the switch is enabled, MySQL requests holding column policies only go to the column authorization path; fail to close in the absence of metadata credentials, permissions, health bindings, column authorization, auditing, or final control plane checks. Existing MySQL requests without column policies continue to use the original table-level path. After registration is complete, restart AgentSQL to enable the startup probe to recalculate the B2 readiness status.

## v1.4.0 Dual gateway shared login budget and upgrade

This release persists console passwords and LDAP login attempt budgets in PostgreSQL metadata: Tenants and accounts are normalized and the deployment key is used to calculate the HMAC, with only a 32-byte digest, window start, and count in the table. The same account can be used up to 10 times within 60 seconds from the first attempt; shared atomically between two gateways. When the metadata connection or writing fails, 503 is returned and does not fall back to the node memory. SQLite still only supports single-node local budgets. The budget table retains a maximum of 8192 unexpired account buckets. When the upper limit is reached, the new account fails to log in and is closed.

Both gateways must point to the same PostgreSQL metadata and audit, use consistent `AGENTSQL_SECRET`, identity, and audit configurations, and configure a different `mcp.sessions.instance_id` for each gateway; if column authorization is enabled, a different `column_authorization.instance_id` must also be configured. Before going online, check the key versions and configuration commitment values ​​of both nodes in a controlled environment, and perform cross-node login current limiting, audit failure shutdown, and chain verification after recovery. Do not mix v1.3.0 and v1.4.0 nodes to accept logins: the old nodes only have flow-limited memory buckets, which will bypass the shared budget.

When upgrading, first stop the login and SQL traffic of the old gateway, and make consistent backups of metadata, audit (if independent), and pairing keys. v1.3.0's SQLite/merged PostgreSQL metadata version is 21, standalone PostgreSQL metadata is 20, and audit-only is 6. Use the v1.4.0 official package to execute `agentsqlctl migrate --check` and confirm that only the expected version to be migrated is executed before executing `agentsqlctl migrate`; after migration, the SQLite/merged PostgreSQL metadata is 22, the independent PostgreSQL metadata is 21, and audit-only is still 6. Running the account requires SELECT, INSERT, UPDATE, and DELETE permissions on the budget table. Execute read-only `migrate --check` again, request all `current=latest`, and then start two v1.4.0 gateways one after another. The budget in memory of the old node will not be relocated, so the switch must be completed within the outage maintenance window.

When rolling back, stop all v1.4.0 gateways, restore the metadata, audit, and `AGENTSQL_SECRET` at the same point before the upgrade, then start the v1.3.0 program and review the audit chain and approval/authorization status. Merely replacing the old binary or merely restoring metadata does not constitute a rollback. Two-node reliability only covers the verified shared PostgreSQL control plane topology and does not change the business database support statement.

## v1.7.0: Certificate verification for PostgreSQL protocol data sources

New`postgres`When using the data source, the console bootstraps using`tls_mode: verify-full`. Mount the trusted CA certificate as a read-only file to**Each gateway container**, fill in the absolute path within the container in the data source`tls_ca_file`。`tls_server_name`Optional; when omitting the connection host name in the certificate, it must match the certificate SAN when filled in. The gateway will reject missing CAs, relative paths, unvalidated certificate trust switches, and unsupported TLS modes and will not`verify-full`Fallback to clear text. The server needs to enable TLS and configure the corresponding certificate; the firewall or database access rules should deny clear-text connections that do not meet the deployment requirements.

The above capabilities of KingbaseES KingbaseES V9R1C10 are only verified on PG mode, isolated single instance and synthetic data. Using the `postgres` protocol path does not mean that other modes or versions of KingbaseES obtain the full permissions and execution guarantees of PostgreSQL. An empty `tls_mode` of an existing data source still retains the old connection behavior, and **cannot be** considered as verified TLS; before migrating, prepare the CA, test server certificate hostname and failure rejection in the isolation environment, and then explicitly update the data source. Submit the new CA path and host name when rotating the certificate, and check the mounting and querying node by node; after updating the database account password, use the new connection to review the MCP query, masking and persistent auditing. The pinned DM8 Pack3 build in v3.3.0 uses a pinned driver copy to verify the server certificate. Set `ENABLE_ENCRYPT=1` and `MIN_SSL_VERSION=771` on the server. Give the gateway an absolute path to the trusted CA and the expected certificate name, and explicitly set `tls_mode=verify-full`, `tls_ca_file`, and `tls_server_name` on the datasource. Empty legacy mode remains compatible but unverified; wrong CAs, names, or encryption modes fail before execution. Acceptance covers one synthetic single-instance environment; see [v3.3.0 acceptance](v3.3.0-acceptance.md).

## v1.8.0: TLS deployment boundaries for Vastbase G100 Build 9

The independent `vastbase` data source only verified `verify-full` in G100 V3.0 Build 9, PG mode, single instance composition table. When creating a new data source, fill in the absolute path of the CA that is readable by the gateway, and ensure that the connection host or `tls_server_name` matches the certificate. The least privileged database account is only granted USAGE of the target schema and SELECT of the explicit table; AgentSQL still only accepts a bounded single-table read-only subset of the adapter. Bad CA, bad hostname, or connection failure must be rejected directly, and clear text cannot be returned.

The specified instance requires `ssl=on`, the certificate and private key permissions are `0600`, and the `hostnossl ... reject` that covers the actual source address is set for the read-only account before the ordinary `host` rule in `pg_hba.conf`; the corresponding `hostssl` authentication rule is only set in the controlled network segment. The `require_ssl=on` of this isolation instance will reject the protocol 3.0 startup package of the current pgx client, even if the TLS handshake is successful, so the actual measured configuration is `require_ssl=off` plus account-specific plaintext rejection rules. The correct certificate, wrong CA, wrong hostname, and clear text deny must all be tested separately after deployment and do not infer that the client is protected based on `SHOW ssl` alone.

This test uses a BASIC/VECTOR temporary license and does not represent a formal production license. Certificate and password rotation are passed in a single instance; master-slave replication, switch final state, load capacity and other builds have not yet been verified. For detailed evidence and uninspected scope, see [v1.8.0 and acceptance record ](v1.8.0-acceptance.md).

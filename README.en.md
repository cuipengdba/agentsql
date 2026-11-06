# AgentSQL

**Language:** [English](README.en.md) | [Simplified Chinese](README.md)

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="brand/agentsql-wordmark-dark.svg">
  <img src="brand/agentsql-wordmark-light.svg" width="360" height="80" alt="AgentSQL">
</picture>

![Version v4.5.0](https://img.shields.io/badge/Version-v4.5.0-blue)
[![Go](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go)](go.mod)
[![License: Apache-2.0](https://img.shields.io/badge/License-Apache--2.0-blue)](LICENSE)

Ecological inclusion: [Glama](https://glama.ai/mcp/servers?query=AgentSQL)

It has been included in the MCP official Registry and can be directly searched for AgentSQL access in the MCP Registry.

AgentSQL is a **two-in-one product: an independent AI data Q&A agent and an MCP Server database security gateway**. Its Go Q&A service and Web UI run without SQLBot. Its MCP Server independently exposes controlled database tools to external AI agents. Both entry points share identity, authorization, SQL checks, controlled execution, result masking, and durable audit.

### Independent AI data Q&A agent

Users ask questions in AgentSQL's own UI. The service selects table and column context under the user's current grants, asks the configured model to interpret the question and generate SQL, then sends that SQL to the AgentSQL gateway for verification and execution. It withholds data when authorization or a required dependency fails; history and exports recheck current permissions. This is AgentSQL's own Q&A path and does not require a SQLBot runtime. v4.5.0 acceptance covers bounded local-model, operator-registered PostgreSQL gateway-alias and specified original-client paths. Users must still check the business meaning of model-generated answers.

### Independent MCP Server security gateway

External MCP clients can connect directly to AgentSQL's stdio or HTTP MCP Server. A fixed-version SQLBot Q&A execution path can use the **verified adapter** to route database calls through AgentSQL MCP while retaining its front-end flow. AgentSQL rechecks identity mapping, least privilege, masking, and audit for each request. SQLBot is an optional integration, not a runtime dependency of AgentSQL Q&A; see the fixed-version scope in the [fixed-baseline SQLBot adapter guide](docs/en/SQLBOT.md).

```text
AgentSQL UI / API → native AI Q&A agent ──────────────┐
                                                       ├→ AgentSQL security core → authorized database
SQLBot (adapter) / MCP client → AgentSQL MCP Server ──┘
```

**Current scope:** The [v4.5.0 release notes](https://github.com/cuipengdba/agentsql/releases/tag/v4.5.0) define the verified ordinary Q&A, original Python MCP SDK, basic assistant, and Type 4 page-embed paths. Advanced assistants, complex active permission targets, and broad Community page/API parity are planned for v4.6.0, v4.7.0, and v4.8.0 respectively. Full SQLBot compatibility cannot be inferred. The user's actual business system has not been validated; no zero-change cutover is claimed. Earlier releases remain in [GitHub Releases](https://github.com/cuipengdba/agentsql/releases) and the [CHANGELOG](CHANGELOG.en.md).

## Core features

- **Native Q&A:** An independent Go service, Web UI, and model orchestration select currently authorized schema, generate SQL, and execute through the same security gateway. See the [Q&A guide](docs/en/CHATBI.md) and release notes for current-history, export, and embed boundaries.
- **MCP Server:** stdio, Streamable HTTP, and a separate stateless HTTP endpoint. Tools remain bound to Agent, human, or task grants; MCP connectivity never grants database access by itself.
- **Deny-by-default execution:** Table/column grants, bounded row rules, SQL parsing and risk rules, four-state decisions, read-only limits, budgets, and approval where required. Requests whose safety cannot be established are rejected before execution.
- **Data and audit:** Source-aware result masking; SQLite or PostgreSQL control and audit storage; management/query audit, health probes, and optional notifications support operations review.
- **Delivery and compatibility:** Signed dual-architecture offline packages and GHCR images. Database capabilities follow the specified versions, modes, and evidence in the matrix below; registration counts do not imply production support.

## Ecosystem and compatibility

Database capabilities follow the specified versions, deployment modes, and evidence in the [compatibility matrix](#database-compatibility-matrix) below. A successful connection or registry entry does not establish safe Q&A execution. Bounded paths for domestic databases, NoSQL, and vector stores are neither vendor certification nor full production support. Discuss target-environment validation in [GitHub Discussions](https://github.com/cuipengdba/agentsql/discussions).

## Quick start in 5 minutes

The following three paths all listen only on the host's loopback address by default. The automatic installer generates the persistent **administrator password**, not a one-time password; for remote access, please use SSH local forwarding, such as `ssh -L 7780:127.0.0.1:7780 user@server`, and do not expose 7780 directly to the public network.

### 1. Linux bare metal one-line installation (recommended)

v0.4.0 provides two native glibc packages: `linux/amd64` and `linux/arm64`: `uname -m` uses the amd64 package when it is `x86_64` / `amd64`, and uses the arm64 package when it is `aarch64` / `arm64`. Both require glibc 2.28+ and systemd as PID 1; the installer will automatically select the native architecture, verify the outer and inner package hashes of the release package, and install it as a systemd service. musl/Alpine and CentOS 7 are still not supported:

```bash
curl -fsSL https://github.com/cuipengdba/agentsql/releases/download/v4.5.0/install.sh | sudo sh -s -- install --version v4.5.0
```

Non-TTY output from pipelines and CIs does not show the automatically generated administrator password; root can view it at `/etc/agentsql/agentsql.env`. The interactive terminal is displayed once when credentials are first created, or `--show-password` can be added explicitly. After obtaining the tarball and the `.sha256` with the same name at the same time in the offline environment, verify, decompress and install from the package root:

The following is an example of the amd64 asset name of v4.5.0; arm64 hosts starting from v0.4.0 use the `-linux-arm64` asset name of the same version.

```bash
sha256sum -c agentsql-v4.5.0-linux-amd64.tar.gz.sha256
tar -xzf agentsql-v4.5.0-linux-amd64.tar.gz
cd agentsql-v4.5.0-linux-amd64
sudo ./install.sh install
```

### 2. Start Docker with one line

By default, the script pulls the exact GHCR tag corresponding to the latest stable Release from GitHub; the tag is `linux/amd64` + `linux/arm64` multi-architecture image, and Docker will automatically match the running architecture. The script generates `.env` with permission `0600`, uses `agentsql-data` to name the volume, and fixes the bound loopback address:

```bash
curl -fsSL https://raw.githubusercontent.com/cuipengdba/agentsql/v4.5.0/scripts/quickstart.sh -o quickstart.sh && sh quickstart.sh --version v4.5.0
```

The GHCR package must be set to public by the publisher so that the above command can be pulled anonymously in a non-logged-in environment. First check the public status of the precise tag `v4.5.0` and the dual-architecture manifest; Docker will automatically select `linux/amd64` or `linux/arm64` from the manifest list:

```bash
docker pull ghcr.io/cuipengdba/agentsql:v4.5.0
```

The equivalent single `docker run` is as follows; the random value is only passed in through the current shell environment and is not written into the command line parameters:

```bash
export AGENTSQL_SECRET="$(openssl rand -base64 24)" AGENTSQL_ADMIN_USER=admin AGENTSQL_ADMIN_PASSWORD="$(openssl rand -base64 18)"
docker run -d --name agentsql --restart unless-stopped --security-opt no-new-privileges:true -p 127.0.0.1:7780:7780 -e AGENTSQL_SECRET -e AGENTSQL_ADMIN_USER -e AGENTSQL_ADMIN_PASSWORD -v agentsql-data:/var/lib/agentsql ghcr.io/cuipengdba/agentsql:v4.5.0
```

### 3. Local Live Demo (one command, no source code required)

This self-contained environment only connects to synthetic PostgreSQL/MySQL demo data, not to the real database. No need to clone the source code, edit configurations or build images, just download a compose file and launch:

```bash
mkdir agentsql-demo && cd agentsql-demo
curl -fsSL -o docker-compose.yml https://raw.githubusercontent.com/cuipengdba/agentsql/v4.5.0/deploy/quickstart/docker-compose.yml
docker compose up -d
```

Wait for about 1–2 minutes. After the four containers are ready, open <http://127.0.0.1:17880>: the login page has automatically filled in the demo account, just click "Login". The "Demonstration Station" has 5 built-in real scripts: column authorization + masking, lack of column authorization, object override, DDL override, and writing operation to manual. Thorough cleaning:

```bash
docker compose down -v
```

Demo credentials are fixed demonstration values, written in compose, and are only used for local experience. Do not connect to real data. For complete instructions, see [5-minute quick start with ](docs/en/GETTING_STARTED.md) and [Live Demo guide](docs/en/DEMO.md).

## Other installation methods (source code build, offline and audit environment)

When you need to audit every step, modify Compose configuration, or build from source, you can continue to use the original process. Docker Compose only starts AgentSQL by default, and metadata and auditing are written to `/var/lib/agentsql/agentsql.db` in the named volume `agentsql-data`:

```bash
cp examples/docker/.env.example .env
openssl rand -base64 24   # Fill in AGENTSQL_SECRET and output exactly 32 ASCII bytes
openssl rand -base64 24   # Fill in AGENTSQL_ADMIN_PASSWORD
docker compose up -d --build
docker compose ps
```

PowerShell can execute the following command twice and fill in two variables:

```powershell
$bytes = [byte[]]::new(24); $rng = [System.Security.Cryptography.RandomNumberGenerator]::Create(); $rng.GetBytes($bytes); [Convert]::ToBase64String($bytes); $rng.Dispose()
```

Visit <http://127.0.0.1:7780>. Compose also only exposes ports at the host's loopback address. When an observability stack is required, first set a self-generated and saved random value in the environment, and then run:

```bash
docker compose --profile observability up -d --build
```

See [ observability example ](examples/observability/README.en.md) for a complete description. `AGENTSQL_SECRET` must be backed up in pairs with `agentsql.db`; directly replacing SECRET will make the existing data source password unable to be decrypted. The production control plane can put metadata and audit into independent PostgreSQL 15+ databases respectively. For complete migration, minimum permissions and rollback process, see [ deployment guide ](docs/en/DEPLOY.md#postgresql-control-plane-deployment).

The source binary build according to `go.mod` requires Go 1.26, cgo, C compiler and glibc compatible environment; local `make build` uses Go in `PATH`, and the version must be confirmed first. The current Makefile and official release script use Go 1.26.10 by default; ordinary builds directly use the existing embedded console products in the repository, without Node.js.

The following source build example only produces native executables. Self-built files have not been signed and distributed for formal release of assets:

```bash
make build VERSION=v4.5.0
./bin/agentsqlctl init-config -o config.yaml
export AGENTSQL_SECRET="$(openssl rand -base64 24)"
export AGENTSQL_ADMIN_USER='admin'
export AGENTSQL_ADMIN_PASSWORD="$(openssl rand -base64 24)"
./bin/agentsql serve -c config.yaml
```

Windows PowerShell：

```powershell
./bin/agentsqlctl.exe init-config -o config.yaml
$bytes = [byte[]]::new(24); $rng = [System.Security.Cryptography.RandomNumberGenerator]::Create(); $rng.GetBytes($bytes); $env:AGENTSQL_SECRET = [Convert]::ToBase64String($bytes); $rng.Dispose()
$env:AGENTSQL_ADMIN_USER = 'admin'
$passwordBytes = [byte[]]::new(24); $passwordRng = [System.Security.Cryptography.RandomNumberGenerator]::Create(); $passwordRng.GetBytes($passwordBytes); $env:AGENTSQL_ADMIN_PASSWORD = [Convert]::ToBase64String($passwordBytes); $passwordRng.Dispose()
./bin/agentsql.exe serve -c config.yaml
```

`agentsqlctl check-config -c config.yaml` only verifies the configuration and does not require credentials; `agentsqlctl migrate -c config.yaml` verifies SECRET and then performs metabase migration.

v0.5 audit reports support CSV, JSONL, pure Go-generated PDF, and ZIP containing four types of payloads and file-by-file SHA-256 `manifest.json`:

```powershell
./bin/agentsqlctl audit report -c config.yaml --format archive --out audit-compliance.zip
./bin/agentsqlctl audit verify-archive --in audit-compliance.zip
```

Archive verification only proves intra-package consistency and does not replace digital signatures, external timestamps, WORM or SIEM retention.

## MCP access

First create an Agent in the console, configure the data source and authorization policy, and save the `asql_...` API Key that will only be displayed once.

### stdio

```json
{
  "mcpServers": {
    "agentsql": {
      "command": "/absolute/path/to/agentsql",
      "args": ["mcp", "--config", "/absolute/path/to/config.yaml"],
      "env": {
        "AGENTSQL_SECRET": "<YOUR_32_BYTE_SECRET>",
        "AGENTSQL_API_KEY": "<YOUR_AGENTSQL_API_KEY>"
      }
    }
  }
}
```

### Streamable HTTP

Compatible MCP local endpoint is `http://127.0.0.1:7780/mcp`; non-native deployments must be configured with an HTTPS reverse proxy in front.

```json
{
  "mcpServers": {
    "agentsql-http": {
      "url": "http://127.0.0.1:7780/mcp",
      "headers": {
        "Authorization": "Bearer <YOUR_AGENTSQL_API_KEY>"
      }
    }
  }
}
```

The gateway always provides 12 base MCP tools: `list_datasources`, `list_schema`, `explain_query`, `query`, `find_documents`, `read_key`, `query_analytics`, `search_vectors`, `execute_write`, `request_approval`, `get_approval_result` and `execute_approved`. `find_documents` is limited to the controlled read-only path for specified MongoDB versions with exact collection grants, strict TLS and an authenticated account. `read_key` is limited to specified Redis/Valkey TLS/ACL single instances, separate key policies and UTF-8 strings. `query_analytics` is limited to the specified ClickHouse TLS single node, a single MergeTree table and exact table/column grants. `search_vectors` is limited to Milvus 2.6.25 in the specified TLS standalone deployment, exact collection/field grants and controlled single-vector search. v0.4.0 PostgreSQL B5 sessions/planned transactions are enabled by default and register eight additional tools: `open_session`, `close_session`, `get_session_status`, `begin_transaction`, `execute_transaction_statement`, `commit_transaction`, `rollback_transaction` and `get_transaction_status`. Explicitly disabling `mcp.sessions.enabled` leaves only the base tools.

### Stateless MCP and task grants

- `/mcp/2026-07-28` rechecks the Agent key, tenant, and protocol metadata on each request and excludes cross-request B5 tools. Existing `/mcp` and stdio remain available; see the [MCP integration guide](docs/en/INTEGRATIONS.md).
- A temporary task key is scoped to one Agent, datasource, and exact SQL. After a different approver approves it, the applicant collects it once; it is revocable and valid for at most 24 hours. Existing grants, masking, and audit still apply. See [task grants](docs/en/task-read-grants.md).
- The access wizard and `agentsqlctl migrate --check` are preflight tools. They do not authorize a query or replace database and paired-key backups.

## Capability matrix

| Product capability | v4.5.0 status | Use and boundary |
| --- | --- | --- |
| Independent AI data Q&A agent | Verified bounded path | Native Go/Web Q&A without SQLBot; operator-registered PostgreSQL gateway alias, local models, ordinary Q&A, and current-authorized history follow the [release notes](https://github.com/cuipengdba/agentsql/releases/tag/v4.5.0) |
| MCP Server security gateway | Independently usable | stdio and HTTP MCP; identity, object/column grants, rules, controlled execution, masking, and durable audit apply under current policy on each request |
| SQLBot integration | Optional, fixed baseline | A verified adapter routes SQLBot database execution into AgentSQL MCP; see the fixed versions and setup in the [SQLBot guide](docs/en/SQLBOT.md). This does not imply drop-in compatibility with arbitrary SQLBot deployments |
| Basic assistant and page embed | Verified bounded path | One-source basic guest assistant and Type 4 page embed; origin, identity, and revocation checks still apply |
| Advanced assistant and broad parity | Planned | Type 1/third-party directory, complex active permission targets, and remaining Community pages/APIs belong to v4.6.0–v4.8.0 and are not v4.5.0 support claims |

## Database Compatibility Matrix

The registry of v3.0.0 code covers the **8 category and 30 databases** (9 relational + 21 NoSQL/vector). openGauss and HighGo HighGo are both included as independent limited read-only types; KingbaseES KingbaseES V9 is also listed as the PG protocol path of the specified instance, which is not included in the 30 models; the classification table has a total of 31 rows. 🟢 Indicates that the parsing, authorization, controlled execution, masking, and audit links are passed within the stated range; 🔵 Controlled read-only = restricted query, masking, and persistent audit links on the specified instance; 🔷 Connection level = ping, version, Schema, read-only preview; 🟡 Pending verification = other vendor environments. Designated instance closures do not represent full production support.

| Category | database | Adaptation status | Boundary description |
| --- | --- | --- | --- |
| Relational | PostgreSQL 14–18 | 🟢 Adapted · Complete protection | Passed the actual test of the whole link |
|  | MySQL 8.0 | 🟢 Adapted · Complete protection | Passed the actual test of the whole link |
|  | Oracle AI Database 26ai Free | 🔵 Specify version controlled read-only | `FREEPDB1`, server `23.26.3.0.0`: minimum permissions HTTP MCP, R010, mobile phone number masking, dual clients, permission revocation, independent audit failure and business database disconnection recovery passed; `EXPLAIN PLAN` is not in the gateway account path, commercial version and full production support have not been verified, see [ acceptance record ](docs/en/v2.2.0-acceptance.md) |
|  | Dameng DM8 | 🟢 Specify scene closed loop | Pack3, COMPATIBLE_MODE=0: v1.5.0 completes client and permission revocation; v1.6.0 completes audit failure, lock waiting timeout, cancellation and recovery re-inspection; see [ acceptance record ](docs/en/v1.6.0-acceptance.md) |
|  | YashanDB YashanDB | 🔵 Adapted · Specified version controlled read-only | 23.4.1.109 The minimum permissions of the specified instance HTTP MCP, R010, masking, persistent auditing and fault shutdown pass; other versions and production topologies are not verified |
|  | SQL Server 2025 Developer | 🔵 Specify version controlled read-only | Linux x86_64 single instance synthetic data: strict TLS, least privilege HTTP MCP, R010, masking, dual client, SELECT/SHOWPLAN deprivation and fault shutdown passed; other versions, Windows/Always On, production certificate rotation and full production support are not verified, see [ acceptance record ](docs/en/v2.3.0-acceptance.md) |
|  | openGauss Community Edition 7.0.0 LTS | 🔵 Specify version controlled read-only | Independent `opengauss` type, specified build 2397a86f: `verify-full`, read-only physical single table narrow SELECT, real MCP, R010, masking, dual clients, permission revocation and two types of fault shutdown passed; GaussDB commercial version, active backup and full production support have not been verified, see [ acceptance record ](docs/en/v2.4.0-acceptance.md) |
|  | Vastbase G100 | 🔵 Specify version controlled read-only | V3.0 Build 9 PG mode's bounded SELECT, table authorization, masking, persistent audit, client, permission revocation and database shutdown recovery actual test; v1.6.0 completes fault shutdown retest, v1.8.0 verifies `verify-full`, certificate and credential rotation and dual client real call; formal master and backup, other Build Not tested with production load, see [ acceptance record ](docs/en/v1.8.0-acceptance.md). |
|  | HighGo HighGo | 🔵 Specify version controlled read-only | Standalone `highgo` Type: 9.0.10 Enterprise Edition PG mode, bounded SELECT of single instance composite table, client, permission revocation, auditing and fault closure; see [ acceptance record ](docs/en/v1.7.0-acceptance.md) |
|  | KingbaseES KingbaseES V9 (separate list) | 🟢 Specify scene closed loop | V9R1C10 PG mode: v1.5.0 completes client, permission revocation and network isolation recovery; v1.6.0 completes fault shutdown and retest, not counted in 30 |
| key value | Redis | 🔵 Specify version controlled read-only | 8.2.9 TLS/ACL single instance: precise key policy, `read_key` UTF-8 strings, masking, persistent auditing, dual clients and failover passed; Cluster, Sentinel, write and full production support not verified, see [ acceptance record ](docs/en/v2.6.0-acceptance.md) |
|  | Valkey | 🔵 Specify version controlled read-only | 8.1.10 TLS/ACL single instance and Redis have the same narrow reading contract, independent real database and client acceptance; other topologies and complete production support have not been verified, see [ acceptance record ](docs/en/v2.6.0-acceptance.md) |
|  | Memcached | 🔷 Adapted · Connection level | ping /version/schema/read-only preview |
| Documentation | MongoDB | 🔵 Specify version controlled read-only | 7.0.43 single instance: exact set and optional field authorization, strict TLS, least privilege `find`, explicit projection and bounded equivalent filtering, masking, persistent auditing, dual client and fail-close test; replica set, sharding, writing and full production support are not verified, see [ acceptance record ](docs/en/v2.5.0-acceptance.md) |
|  | Couchbase | 🔷 Adapted · Connection level | Same caliber |
|  | CouchDB | 🔷 Adapted · Connection level | Same caliber |
| wide column | Cassandra | 🔷 Adapted · Connection level | ping /version/schema/read-only preview |
|  | ScyllaDB | 🔷 Adapted · Connection level | Same caliber |
|  | HBase | 🔷 Adapted · Connection level | Same caliber |
| Figure | Neo4j | 🔷 Adapted · Connection level | ping /version/schema/read-only preview |
|  | JanusGraph | 🔷 Adapted · Connection level | Same caliber |
|  | NebulaGraph | 🔷 Adapted · Connection level | Same caliber |
| Timing | InfluxDB | 🔷 Adapted · Connection level | ping /version/schema/read-only preview |
|  | Prometheus | 🔷 Adapted · Connection level | Same caliber |
|  | TimescaleDB | 🔷 Adapted · Connection level | Same caliber |
| OLAP | ClickHouse | 🔵 Specify version controlled read-only | 26.8.31.2 Strict TLS single node: `query_analytics`’s MergeTree single table explicit column, String equivalent filtering, precise table column authorization, read-only role, resource restriction, masking, persistent audit, dual client and broken database recovery passed; other versions, clusters and full production support have not been verified, see [ acceptance record ](docs/en/v2.7.0-acceptance.md) |
|  | Apache Doris | 🔷 Adapted · Connection level | Same caliber |
|  | StarRocks | 🔷 Adapted · Connection level | Same caliber |
| Vector | Milvus | 🔵 Specify version controlled read-only | 2.6.25 TLS standalone `default` database: `search_vectors`'s exact collection/field authorization, limited FloatVector, COSINE index, masking, persistent auditing and dual clients passed on synthetic data; clustering, other versions and full production support are not verified, see [ Candidate Acceptance ](docs/en/v2.8.0-acceptance.md) |
|  | Qdrant | 🔷 Adapted · Connection level | Same caliber |
|  | Weaviate | 🔷 Adapted · Connection level | Same caliber |

Other NoSQL and vector entries have connection-level capabilities unless the matrix identifies a verified bounded-read path. A read-only preview does not establish full query or server-side security support. Every result applies only to its specified instance, synthetic cases, and documented limits; none is vendor certification. Business requests should pass through AgentSQL with a dedicated least-privilege database account. Unknown parsing, grants, or capability facts fail closed. See the [NoSQL scope](docs/en/nosql-support.md) and [security boundaries](SECURITY.en.md).

Control plane storage is additional: SQLite has zero configuration by default and can be combined for storage; PostgreSQL 15+ can be combined or independent metadata/audit libraries (PG18 is the baseline). Use `agentsqlctl migrate-sqlite-to-postgres` to migrate from SQLite to a single database or independent dual databases.

## Known limits and safe boundaries

- masking is matched at the result layer according to the three scopes of global columns, `表.列`, and `模式.表.列`: top-level directly projected columns that can be uniquely attributed to the physical table are accurately hit according to the source table/source column; PostgreSQL/MySQL verified functions/expressions, aggregations, `CAST`, `UNION`, and CTE Derived output such as view references will be processed according to the rules when the same source can be proven. When the source is unknown and may involve protected columns, the blocking is fixed to `***`. This is not a complete DLP; the internal semantics of database functions and views, statistical side channels, and direct connections that bypass gateways are still subject to database minimum permissions, column-level authorization, security views, and approvals.
- `hash` is an irreversible fingerprint, it is not encrypted and cannot be decrypted and restored. It is calculated after the business database returns the results and is not pushed down to JOIN/WHERE in the database; certainty will expose equality relationships and frequencies, and sharing the same key will also bring cross-database association risks.
- `block` does not output the original value characters, length or equivalence relationship, but still retains the row and column shape, number of rows, column names, and whether there are results in the result set, and exposes the cell as empty/NULL because the null value is returned as is; it is not anonymized. `block` performs post-processing in the database, which does not reduce database reading, nor prevent the database side from using the original value for WHERE/JOIN/GROUP BY. It only prevents the result unit from being returned externally. Its fixed `***` is an opaque string and does not guarantee schema compatibility for values, dates, or JSON.
- PostgreSQL SELECTs with column strategies (including multi-table JOIN, self-join, predicate and wildcard projection) must use the B2 column binding path; when B2 is unavailable, it will fail to close before execution. MySQL 8.0/8.4 only enters the independent column authorization path for the single-table InnoDB SELECT subset that is explicitly enabled and completes dual-account registration; other MySQL queries with column policies are rejected before execution. There is still no equivalent column-level binding proof for Vastbase etc. When there is no column policy, it is still governed by the table policy.
- `AllowedTables`in`*`or`schema.*`Indicates that the administrator explicitly grants all columns of the matching table; the precise column whitelist is no longer tightened at this time. When using a precise column whitelist for a single table, the projected columns should be listed explicitly.
- PostgreSQL's B2 column-level authorization is enabled by factory default; ordinary schema-qualified base tables use extension-free `CATALOG_CLOSED_V1`, and the native binder is only used for optional enhancements. MySQL does not enter the B2 PostgreSQL path.
- B5 cross-request logical sessions and PostgreSQL planned transactions are enabled by factory default; MySQL cross-request transactions are not supported and will refuse to start if `mcp.transactions.mysql: true` is configured. Only one top-level SQL is still allowed per operation, subject to preflight plans and session security boundaries.

## local test mode

`AGENTSQL_INSECURE=1` permanently only means "allow public test credentials": it allows public example SECRETs of correct length and non-empty weak admin passwords in local development/testing, but still rejects empty SECRETs, non-32 byte SECRETs, and empty admin passwords. It does not turn off authentication, security rules, or fail-closed behavior and should not be set in production environments.

## Documentation and Community

- [Get started quickly in five minutes](docs/en/GETTING_STARTED.md)
- [ User Manual](docs/en/USER_GUIDE.md)
- [MCP Access Guide](docs/en/INTEGRATIONS.md)
- [Current version document index](docs/en/README.md)
- [Task-level temporary read-only authorization](docs/en/task-read-grants.md)
- [v4.5.0 release notes](https://github.com/cuipengdba/agentsql/releases/tag/v4.5.0)
- [Current version operation and maintenance manual](docs/en/OPERATIONS.md)
- [ Sensitive column discovery and masking draft ](docs/en/DISCOVERY.md)
- [Webhook / Syslog notification outgoing and security configuration ](docs/en/NOTIFICATIONS.md)
- [ deployment, PostgreSQL control plane, upgrade, backup and systemd](docs/en/DEPLOY.md)
- [Local Live Demo, regular reset and public deployment security list](docs/en/DEMO.md)
- [Product and Engineering Specifications](docs/en/SPEC.md)
- [Security Policy and Privacy Vulnerability Report](SECURITY.en.md)
- [Change record](CHANGELOG.en.md)
- [Issue Feedback](https://github.com/cuipengdba/agentsql/issues)

Please do not submit real keys, passwords, connection strings or details of unfixed vulnerabilities in public issues; please use the private channel at [SECURITY.md](SECURITY.en.md) for unfixed vulnerabilities.

### Join the community (WeChat)

| Official account | Personal WeChat |
| --- | --- |
| ![CP's PostgreSQL Kitchen](website/public/assets/community/wechat-official-account.png) | ![Cui Peng personal WeChat ](website/public/assets/community/wechat-personal.png) |
| Scan the QR code to follow | Scan the QR code to add an author and remark "AgentSQL" |

## Build and Contribute

```bash
go vet ./...
go test -race -count=1 ./...
make build VERSION=v4.5.0
```

`pg_query_go` requires cgo; do not build with `CGO_ENABLED=0` or Alpine/musl. Please read [SPEC](docs/en/SPEC.md) before submitting changes to test for behavioral changes and keep the `tests/corpus` decision-making corpus from being unintentionally rewritten.

## Open-Source License and Trademarks

The current source is licensed under [Apache License 2.0](LICENSE), with a [NOTICE](NOTICE). Follow that license when using, modifying or distributing the software. Rights to the AgentSQL name (including its Chinese name, “Zhidun”) and logo are reserved; Apache-2.0 does not grant trademark rights. Historical tags and release assets retain the licenses that applied when they were published.

For configuration, threat boundary, and migration instructions for v0.5 personnel authentication (MFA / OIDC / LDAP), see [ Identity Authentication Guide ](docs/en/AUTHENTICATION.md).

# AgentSQL

**Language:** [English](README.en.md) | [Simplified Chinese](README.md)

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="brand/agentsql-wordmark-dark.svg">
  <img src="brand/agentsql-wordmark-light.svg" width="360" height="80" alt="AgentSQL">
</picture>

![Version v4.3.0](https://img.shields.io/badge/Version-v4.3.0-blue)
[![Go](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go)](go.mod)
[![License: Apache-2.0](https://img.shields.io/badge/License-Apache--2.0-blue)](LICENSE)
[![Commercial License](https://img.shields.io/badge/License-Commercial-orange)](COMMERCIAL-LICENSE.en.md)

Ecological inclusion: [Glama](https://glama.ai/mcp/servers?query=AgentSQL)

It has been included in the MCP official Registry and can be directly searched for AgentSQL access in the MCP Registry.

AgentSQL (Chinese brand name: Zhidun, the console is displayed as "AgentSQL Zhidun Console") is a database security gateway and production-level MCP Server for AI Agents: each PostgreSQL/MySQL request issued by the model undergoes identity authentication, SQL parsing, authorization and rule evaluation, controlled execution, result masking and auditing before reaching the database.

v0.6.0 adds governance console, approval single execution and restricted read-only adaptation of Vastbase G100; v0.7.0 adds independent MCP `2026-07-28` entrance and task-level temporary read-only authorization; v0.8.0 adds alien approval; v0.9.0 adds 1–10 for new applications Results budget, management audit delivery status and policy offline pre-checking. v1.0.0 Tighten the failure shutdown of the column policy, and add optional agent and personnel dual-identity HTTP MCP authentication. v1.1.0 extends dual identity to approval and B5 sessions, and adds separate revocation of delegation tokens. v1.2.0 Add optional MCP OAuth protected resource entry. v1.3.0 Adds the MySQL 8.0/8.4 single-table InnoDB SELECT column authorization subset that is turned off by default. v1.4.0 Share console passwords and LDAP login attempt budgets in PostgreSQL metadata deployments and verify dual gateway audit failure recovery. The code uses Apache-2.0 as of v0.5.0, and historical v0.4.0 releases are still used under their original license.

v1.5.0 conducts in-depth verification of real MCP clients, permission revocation, database outage recovery and auditing on designated instances of Dameng, KingbaseES and Vastbase. v1.6.0 Catch a failed shutdown within the same specified instance and controlled read-only subset. v1.7.0 adds independent `highgo` type and bounded read-only path to HighGo 9.0.10 Enterprise Edition PG mode, and adds certificate verification and credential rotation to KingbaseES V9R1C10 PG mode. v1.8.0 is used to verify `verify-full`, certificate and account rotation, MCP dual client, masking and auditing in the specified Vastbase G100 Build 9 PG mode instance; active and backup high availability and complete production support have not yet been accepted, see [ release notes ](docs/en/release-notes-v1.8.0.md). v1.9.0 adds Dify / MCP Inspector access instructions, target SQL offline interpretation of reused Agent and data source, and real call audit positioning; see [ release notes ](docs/en/release-notes-v1.9.0.md). v2.0.0 adds fixed dual-gateway Kubernetes configuration and PostgreSQL single-table column policy official registration API, see [ release note ](docs/en/release-notes-v2.0.0.md) and [ dual-gateway operation guide ](deploy/kubernetes/README.en.md).

v2.1.0 Verifies true HTTP MCP, R010, masking, independent persistent auditing, MCP Inspector/Dify, and database break recovery for minimum privilege accounts within YashanDB 23.4.1.109 designated instance, standalone client 23.4.7.100, and restricted read-only SQL subset; and completes data source visibility preflight rejection auditing for certified SQL tools. Other YashanDB versions, TLS, active/standby and full production support have not been verified, see [ release notes ](docs/en/release-notes-v2.1.0.md) and [ candidate acceptance ](docs/en/v2.1.0-acceptance.md).

v2.2.0 In the designated synthetic instances of Oracle AI Database 26ai Free, `FREEPDB1`, and server `23.26.3.0.0`, the narrow SELECT path of the native driver is connected to the real HTTP MCP, minimum privilege account, R010, masking and persistent auditing; dual clients, permission revocation and business database/independent audit library disconnection recovery also pass. The running account `PLAN_TABLE` write permission has not been verified. This version of Oracle path does not pre-execute the plan; the commercial version, TCPS, active and backup and complete production support have not been verified. See [ release notes ](docs/en/release-notes-v2.2.0.md) and [ candidate acceptance ](docs/en/v2.2.0-acceptance.md).

v2.3.0 In the designated Linux x86_64 synthetic instance of SQL Server 2025 Developer, using strict TLS with verification certificates and a temporary account that only obtains the target table `SELECT` and the database level `SHOWPLAN`, completes the real HTTP MCP, R010, mobile phone number masking, persistent auditing, dual client and revoke and disconnection recovery verification. Other SQL Server versions, Windows, Always On, Production Certificate Rotation, Complex T-SQL, and full production support are not verified; the public Live Demo still uses synthetic PostgreSQL data. See [ release instructions ](docs/en/release-notes-v2.3.0.md), [ candidate acceptance ](docs/en/v2.3.0-acceptance.md) and [ official release evidence ](docs/en/v2.3.0-release-evidence.md).

v2.4.0 adds an independent `opengauss` type to the specified build of openGauss Community Edition 7.0.0 LTS, forces verification of certificates and read-only sessions, and only opens explicit schema, `FROM ONLY` physical single table, and bounded direct column SELECT. Verify real MCP, R010, mobile phone number masking, approval, persistent auditing, dual clients, permission revocation, disconnection recovery and `gs_dump`/`gs_restore` in the least privilege synthetic instance. GaussDB Commercial Edition, other builds, active-standby, write, B2/B5, and full production support are not verified. See [ Release Notes ](docs/en/release-notes-v2.4.0.md) and [ Candidate Acceptance ](docs/en/v2.4.0-acceptance.md).

v2.5.0 Adds Agent-level `find_documents` controlled read-only tools to designated single instances of MongoDB 7.0. It requires precise set authorization, strict TLS and authenticated least privilege accounts, only accepts explicit top-level scalar fields, bounded string equivalent filtering, and a 1–100 row limit; desensitizes before returning, and writes persistent auditing before and after access. MongoDB 7.0.43 synthetic instance, MCP Inspector 0.16.8, Dify MCPClient 1.17.1, policy revocation and audit library/business database disconnection recovery have been verified. Replica sets, sharding, writes, and full production support are not verified. See [ Release Notes ](docs/en/release-notes-v2.5.0.md) and [ Candidate Acceptance ](docs/en/v2.5.0-acceptance.md).

v2.6.0 Adds Agent MCP `read_key` controlled read for the specified TLS/ACL single instance of Redis 8.2.9 and Valkey 8.1.10. Requests only accept exact key names; the `key` policy is independent of SQL table authorization and rejects take precedence; the server always performs `TYPE` and `GETRANGE` that do not exceed 64 KiB and returns only UTF-8 strings. Values ​​are masked by source, and persistent auditing is written before and after access; MCP Inspector 0.16.8, Dify MCPClient 1.17.1, policy revocation and audit library/business database disconnection recovery pass on synthetic data. Cluster, Sentinel, write and full production support are not verified. See [ release instructions ](docs/en/release-notes-v2.6.0.md), [ candidate acceptance ](docs/en/v2.6.0-acceptance.md) and [ official release evidence ](docs/en/v2.6.0-release-evidence.md).

v2.7.0 Specifies strict TLS single node to add Agent MCP `query_analytics` for ClickHouse 26.8.31.2: only accepts table name, explicit column, String and other value conditions and 1–100 row limit. The gateway verifies the accurate list authorization one by one, and the server generates parameterized queries and limits resources, which are delivered after successful source masking, persistent pre-checking, and final state audit. Real HTTP MCP, MCP Inspector 0.16.8, Dify MCPClient 1.17.1, permission revocation and database break recovery have been verified on synthetic data; clusters, other versions and full production support have not been verified. See [ Release Notes ](docs/en/release-notes-v2.7.0.md) and [ Candidate Acceptance ](docs/en/v2.7.0-acceptance.md).

v2.8.0 Added Agent MCP for Milvus 2.6.25 specified TLS standalone `search_vectors`: only accepts exact collections, a finite FloatVector, explicit scalar fields, and a hit cap of 1–20. The gateway verifies the accurate collection and field authorization, CA/host name, dimension and COSINE index one by one, and delivers it after successful source masking, persistent pre-checking and final state audit. Real HTTP MCP, MCP Inspector 0.16.8, Dify MCPClient 1.17.1, minimum privileges and revoke verified on synthetic data; clusters, other versions and full production support not verified. See [ Release Notes ](docs/en/release-notes-v2.8.0.md) and [ Candidate Acceptance ](docs/en/v2.8.0-acceptance.md).

v2.9.0 adds tenant policy package export, difference plan, conservative table-level playback, alien approval and atomic application; the approval baseline can prompt for drift caused by direct modification. The externally stored Ed25519 key provides a signed witness for the compliance audit ZIP and the audit chain header of the claim. Policy package modification only supports table-level allow/deny without B2 binding and column/row constraints; offline playback does not mean that real SQL is executable, see [ release note ](docs/en/release-notes-v2.9.0.md) and [ acceptance record ](docs/en/v2.9.0-acceptance.md).

v3.0.0 displays the controlled SQL, bounded SQL, special reading tools and connection previews of 30 registered databases in a hierarchical manner on the management side and MCP data source list, and adds read-only `agentsqlctl doctor` and synthetic scenario MCP acceptance scripts. Capability statements do not grant permissions and do not extend the scope of validated versions, deployment topologies, or production support for any database. See [ design ](docs/en/v3.0.0-design.md) and [ release notes ](docs/en/release-notes-v3.0.0.md).

v3.0.1 adds Chinese and English entrances and English interfaces to the official website, GitHub documents, and Web console, and fixes the problem that the bottom menu on the left navigation cannot be scrolled when the effective viewport height is insufficient. This patch does not modify database protocols, permissions, execution, or audit contracts; the compatibility scope follows evidence from v3.0.0. See [ release notes ](docs/en/release-notes-v3.0.1.md).

v3.0.2 completes the complete English correspondence between GitHub and the official website, adds the current operation and maintenance manual and document index, fixes the color of the left navigation under the light theme, and provides SQLBot v1.10.2 PostgreSQL execution layer adaptation. Access still implements the original authorization, masking and persistent audit; only the versions and scenarios listed in the acceptance are covered. See [SQLBot Access Guide ](docs/en/SQLBOT.md) and [ Release Notes ](docs/en/release-notes-v3.0.2.md).

v3.2.0 brought specified MySQL read-only questions into SQLBot personal bindings, combined PostgreSQL B2 column authorization with read approvals, and added table-level policy impact preview; see its [release notes](docs/en/release-notes-v3.2.0.md). v3.3.0 added verified transport and scan-cost bounds for a pinned DM8 Pack3 build. v3.4.0 deepens controlled reads, plans and operations for pinned Kingbase and Vastbase builds; Kingbase writes and Vastbase column authorization stay closed. See its [release notes](docs/en/release-notes-v3.4.0.md) and [acceptance record](docs/en/v3.4.0-acceptance.md). v3.4.1 fixes cross-tenant event isolation in management audit SSE; see the [security fix notes](docs/en/release-notes-v3.4.1.md). v3.5.0 adds a three-gateway deployment example and tenant-scoped cross-gateway audit cursors; see the [release notes](docs/en/release-notes-v3.5.0.md), [acceptance record](docs/en/v3.5.0-acceptance.md), and [release evidence](docs/en/v3.5.0-release-evidence.md). v3.6.0 adds signed cross-environment delivery of simple table policies, reliable webhooks from the committed audit stream, and externally pinned archive-chain verification; see the [release notes](docs/en/release-notes-v3.6.0.md) and [acceptance record](docs/en/v3.6.0-acceptance.md). v3.7.0 adds approved-index bounded reads on a specified MongoDB 7.0.45 replica set and verifies exact Hash field grants and failover on Redis/Valkey Sentinel; see the [release notes](docs/en/release-notes-v3.7.0.md) and [acceptance record](docs/en/v3.7.0-acceptance.md). v3.8.0 adds tighter resource budgets for specified ClickHouse and authoritative tenant filters for shared collections on specified Milvus; see the [release notes](docs/en/release-notes-v3.8.0.md) and [acceptance record](docs/en/v3.8.0-acceptance.md).

v3.9.0 adds verified TCPS and opt-in execution-plan checks for specified Oracle 26ai Free, and deepens certificate rotation and fault-recovery verification for specified SQL Server 2025; see the [release notes](docs/en/release-notes-v3.9.0.md) and [acceptance record](docs/en/v3.9.0-acceptance.md).

v4.0.0 adds local verification of all signed release assets, offline transport assembly, bounded read-only support bundles and cross-version upgrade/restore procedures. See the [delivery guide](docs/en/v4.0.0-delivery.md) and [acceptance record](docs/en/v4.0.0-acceptance.md).

v4.1.0 adds native Go data Q&A with ordinary-account login compatibility, PostgreSQL/MySQL single-turn queries, encrypted conversations, a bilingual interface and offline native-service installation. It is the first of five SQLBot Community replacement milestones; full feature and integration compatibility remains pending. See the [Q&A guide](docs/en/CHATBI.md) and [release notes](docs/en/release-notes-v4.1.0.md).

v4.2.0 added up to three authorized Q&A turns, terms and recommended questions, controlled CSV/XLSX file datasources, and narrow PostgreSQL row rules; see its [release notes](docs/en/release-notes-v4.2.0.md). v4.3.0 adds AgentSQL's own charts, dashboards and embedded application management, with selected original SQLBot v1.10.2 page and embedded Q&A flows compared in a synthetic local environment. Full feature and page parity and zero-change replacement of the user's application remain unverified; see the [current release notes](docs/en/release-notes-v4.3.0.md) and [acceptance record](docs/en/v4.3.0-acceptance.md).

**verification scope:**v1.0.0–v2.8.0. For the database and security capabilities, see the acceptance records of each version respectively; for v2.9.0 strategy review and audit, see [ acceptance records ](docs/en/v2.9.0-acceptance.md); v3.0.0 For the capability catalog, diagnostics and client see [ acceptance record ](docs/en/v3.0.0-acceptance.md). Vendor conclusions only cover specified instances, patterns, and synthetic data. The following commands are subject to [GitHub Release](https://github.com/cuipengdba/agentsql/releases/tag/v4.3.0), signature and image anonymous pull acceptance.

```text
AI Agent → LLM / MCP Client → AgentSQL Gateway → PostgreSQL / MySQL
                                  │
                                  └─ Authentication · Authorization · Rules · Approval · masking · Audit
```

## Core features

- Dual MCP bearer: native `stdio` and Streamable HTTP `/mcp`, providing 12 basic database tools; the PostgreSQL B5 session/planned transaction enabled by default provides 8 additional tools.
- Secure links denied by default: API Key authentication, Agent capability level, object/column authorization, SQL AST rules and fail-closed error handling.
- Controlled read and write: read-only protection, dangerous SQL blocking, Explain risk assessment, timeouts, connection/QPS/result row limits, and manual approval.
- Basic data protection: Column-level masking provides four algorithms - six types of `mask` partial masking, nine types of HMAC `hash` fingerprints with dedicated keys, `block` integer blocking that does not require a key and uniformly replaces non-null values with `***`, and only `number` / `date`, `range` that preserves coarse-grained distribution through numerical bucketing or date truncation without keys. The data source password is also encrypted and saved by 32 bytes `AGENTSQL_SECRET`.
- Traceable operation and maintenance: default zero-configuration SQLite; optional PostgreSQL 15+ control plane (PostgreSQL 18 is the baseline, metadata and audit can be divided into databases); supports audit export (machine-oriented JSONL, and Excel/WPS can be opened directly, Chinese CSV with formula injection protection), approval decision-making closed loop, Prometheus indicators and health/readiness probes.
- Security event notification: filtered by decision and sent out via Webhook or Syslog bypass; turned off by default, without SQL by default, and notification failure will not affect auditing and SQL decisions.
- Embedded Web console: Overview, audit, demo platform, Agent, data source, permissions, rules, approval, masking rules, users, roles, tenant management and first-time access wizard; v3.0.2 supports Chinese and English switching and scrollable left navigation.
- v0.9.0 Controlled query reinforcement: New task Key has a default one-time result budget (up to 10 times) and atomic deduction; the overview shows the audit delivery backlog of this tenant's sub-library management; the permissions page provides only rejected or unknown SQL offline pre-checks.
- v1.0.0 Security boundary: SELECT with column policy does not return to table-level release; optional HTTP MCP personnel token is verified on a per-request basis and bound to Agent Key, tenant, and task authorization.
- v1.1.0 Dual identity closed loop: the delegation approval order can only be executed once by the original user and Agent; B5 sessions and plans are bound to human users, Agents and tenants; newly issued delegation tokens can be revoked individually.
- v1.2.0 MCP OAuth: The optional `/mcp/oauth` protected resource entrance uses a resource-specific access token issued by an external OIDC/OAuth authorization server, and binds independent Agent Key, user mapping, tenant and local permissions in turn; see the release notes for the scope of support.
- v1.3.0 MySQL column authorization: After explicitly enabled, MySQL 8.0/8.4 specifies a restricted single-table SELECT of an InnoDB base table to use an independent read-only business account and a PROCESS-only metadata account, table ID, and metadata lock to prove column authorization; it is not expanded to all MySQL SELECTs.
- v1.4.0 Multi-gateway login budget: PostgreSQL metadata deployment uses a shared 60 second 10 budget for passwords and LDAP logins; database failure returns 503, budget exhaustion returns 429, SQLite single instance still uses local budget.
- v2.0.0 Fixed dual-gateway deployment: Two Kubernetes gateways share PostgreSQL metadata and independent audit libraries, and store B2 instance identifiers separately; stateless MCP read-only requests are forwarded through Service, and upgrades and rollbacks require maintenance windows. The management side can register column policies for a single ordinary table in PostgreSQL closed binding mode. The public Live Demo still uses a single gateway synthetic data environment.
- v2.1.0 YashanDB specified instance in-depth verification: the temporary minimum privilege account completes narrow SELECT, R010, mobile phone number masking and persistent auditing in the real MCP; database permission revocation, business database and independent audit library disconnection are all delivered for acceptance without original results. Preflight rejection of unseen data sources by certified SQL tools also logs audits without raw SQL.
- v2.2.0 Oracle Free designated instance is controlled and read-only: the temporary minimum permission account is executed by real MCP and Inspector/Dify to perform narrow SELECT, R010 and mobile phone number masking; database permissions are revoked, business database and independent audit library are disconnected and delivered for acceptance without original results. This path does not pre-execute `EXPLAIN PLAN`.
- v2.3.0 SQL Server 2025 Developer designated instance controlled read-only: strict TLS, least privilege account, real MCP, R010, masking, auditing, dual client and two types of failure shutdown verified with synthetic data; does not cover other versions or full production support.
- v2.4.0 openGauss 7.0.0 LTS designated instance controlled read-only: type independent, strict TLS, physical single table narrow SELECT, real MCP, approval, masking, auditing, dual client and failover on synthetic instances; does not cover GaussDB commercial edition or full production support.
- v2.5.0 MongoDB 7.0.43 Specified instance controlled read-only: `find_documents` reads only explicit top-level scalar fields of the exact authorization set; strict TLS, least privileges, masking, persistent auditing, dual clients and failover pass on synthetic single instances.
- v2.6.0 Redis/Valkey specified instance controlled key reading: `read_key` only reads key-level authorized UTF-8 strings; strict TLS, minimal ACLs, masking, persistent auditing, dual clients and failover pass on synthetic single instance.
- v2.7.0 ClickHouse specified instance controlled analysis query: `query_analytics` Read only MergeTree single table with explicit columns for precise authorization; strict TLS, read-only role, resource budget, masking, persistent auditing, dual clients and failover on synthetic single node pass.
- v2.8.0 Milvus specified instance controlled vector retrieval: `search_vectors` only reads a FloatVector and explicit scalar field of the precise authorization collection; strict TLS, least privileges, dimension and index verification, masking, persistent auditing and dual clients are passed on the synthetic single machine.
- v2.9.0 Tenant policy governance: standardize policy packages, source summaries, alien reviews and transaction applications, and prompt drift by approving baselines; audit ZIP can sign external witnesses, and its chain head statements must still be independently verified.
- v3.0.0 Unified Capability Statement: The management terminal, console and authorized MCP data source list use the same four-layer directory; read-only `doctor` and synthetic MCP acceptance scripts provide pre-delivery evidence, and actual access is still authorized one by one, masked and permanently audited.

## Ecological and compatible

As a database-neutral security gateway, AgentSQL currently natively supports PostgreSQL and MySQL. v0.6.0 adds a read-only subset of the specified version of Vastbase G100; the specified instance and protocol path verification range of domestic databases are shown in the table below.

| database | Adaptation status | Description |
| --- | --- | --- |
| Massive data Vastbase G100 | 🔵 Specify version controlled read-only | G100 V3.0 Build 9, PG mode, single instance synthetic table: independent `vastbase` type, forced read-only connection, single table bounded SELECT, table authorization, mobile phone number masking, MCP approval single execution and persistent audit test; v1.8.0 supplements `verify-full`, certificate and password rotation and Inspector/Dify dual client verification. The temporary license is only used for isolation acceptance, and HA, other Builds and production loads have not been tested; see [ for acceptance of this version and ](docs/en/v1.8.0-acceptance.md). Posted by |
| CETC KingbaseES KingbaseES | 🟢 The basic closed-loop test of PG protocol passed | Only V9R1C10, PG mode, single instance synthesis table: writable license volume boot, ksql CRUD/paging/identifier, pgx/v5 `$1` binding + SCRAM, read-only role deny writing `42501`, controlled SELECT allow + mobile phone number masking + column blood + R006 comment denial + allow/deny Passed the audit test; see [ real database verification record ](docs/en/kingbase-v9-verification.md) |
| HighGo HighGo | 🔵 Specify version controlled read-only | Standalone `highgo` Type: 9.0.10 Enterprise Edition PG mode, bounded SELECT of single instance synthetic table, table authorization, masking, MCP dual client, permission revocation, fault shutdown and persistent audit passing; see [ acceptance record ](docs/en/v1.7.0-acceptance.md) |
| IvorySQL | Measured | The community version protocol path has been tested and passed |
| openGauss Community Edition | 🔵 Specify version controlled read-only | Independent `opengauss` type, only official 7.0.0 LTS build 2397a86f specifies synthetic single instance: strict TLS, ordinary physical single table narrow SELECT, least privilege HTTP MCP, R010, masking, approval, persistent auditing and two types of fault shutdown passed; GaussDB Commercial version, active and standby, other builds and full production support are not verified, see [ candidate acceptance ](docs/en/v2.4.0-acceptance.md) |
| TiDB | Adapting | Execution plan (EXPLAIN) adaptation is being implemented |
| OceanBase | Adapting | Execution plan (EXPLAIN) adaptation is being implemented |
| TDSQL for PostgreSQL | To be verified by the target environment | PG protocol candidate; the commercial version has not been tested and cannot be replaced by OpenTenBase results. |
| TDSQL for MySQL | To be verified by the target environment | MySQL protocol candidate; the commercial version has not been tested and is not to be confused with TDSQL for PostgreSQL |
| OpenTenBase | Adapting | Execution plan compatibility fix is being implemented |
| YashanDB database YashanDB | 🔵 Specify version controlled read-only | 23.4.1.109, `COMPAT_VECTOR=yashan`, independent client 23.4.7.100: The minimum privilege account has completed restricted SELECT, R010, mobile phone number masking, dual client and independent persistent audit through HTTP MCP; for permissions and fault closure, see [v2.1.0 for acceptance ](docs/en/v2.1.0-acceptance.md). TLS, cancel/timeout, active/standby and full production support not verified |
| Dameng Database DM | 🟢 The designated instance protection link passed the actual test | DM8 Pack3, `COMPATIBLE_MODE=0`: v0.7.0 bounded real database re-verified HTTP MCP query, column lineage, R010 table policy, mobile phone number masking and persistence audit; R006 annotation failed to close during the parsing stage, multi-line paging/JOIN/function/system directory was not tested, and writing was not released |
| PolarDB for PostgreSQL | The protocol path has been tested | Designated PG 15 community image reuse `db_type=postgres`; non-commercial service certification |
| PolarDB-X | The old official image is bounded and tested | `2.0.1` single container reuses `db_type=mysql`, two-stage EXPLAIN has been adapted; the current complete topology and commercial services are pending final verification |

> "Tested" only refers to the corresponding version, mirror/topology and use case, and is not officially certified by the database manufacturer; "Adapting/To be tested/To be tested by the manufacturer's environment" is work in progress and does not mean that the current version is supported. All database manufacturers are welcome to contact us for compatibility certification and cooperation: please contact us through the communication group, repository Discussion or email 87326549@qq.com.

> **KingbaseES V9 green border:** is limited to the above-mentioned PG mode and the read-only controlled basic closed loop of the single-instance synthesis table; the actual license expiration date has not been confirmed. v1.7.0 Verified `verify-full`, erroneous CA/hostname rejection, certificate and account password rotation, and MCP masking and auditing for the same V9R1C10 PG mode isolation instance. V9 Oracle mode, native driver, V8, multi-row/deep paging, JOIN, aggregates/functions, complex expressions, type OIDs, system catalogs, EXPLAIN, HA/failover, backup and recovery, B2 column-level authorization and production load have not been tested. Explicit `GRANT CREATE SESSION` rejected; session capability verified with `LOGIN` + `CONNECT`. Non-manufacturer certification or production support commitment; see [ genuine library verification record ](docs/en/kingbase-v9-verification.md) and [ acceptance record ](docs/en/v1.7.0-dm-kingbase-depth-acceptance.md) for details.

> **HighGo support boundary:** is limited to the above 9.0.10 enterprise version, PG mode, clear schema of single instance synthetic table, non-constant WHERE, LIMIT single table SELECT. TLS, connection pool failover, extension types/OIDs, other modes and versions, production loads, B2/B5 Not verified; does not imply vendor certification or full production support.

> **YashanDB client authorization and operation:** has been verbally authorized by the manufacturer (C client redistribution), and there is no written authorization document in the repository. The Linux distribution package and GHCR image of v0.5.1 are built using the independent client 23.4.7.100, and the SHA-256 of the matching architecture is verified; the official assets must pass the dual-architecture content and runtime check before being released. When building from source code, you need to obtain the same version of the client, enable the `yashan` build tag and configure `LD_LIBRARY_PATH`; if the driver or runtime library is missing, it will explicitly fail to close. For the actual MCP and fault injection evidence of the specified instance v2.1.0, see [YashanDB driver description ](docs/en/yashan-dialect.md#v210-specified-instance-http-mcp-candidate-acceptance) and [ acceptance record ](docs/en/v2.1.0-acceptance.md).

For the first batch of KingbaseES + HighGo’s evidence boundaries, recurring SQL, and items to be made, please see [ joint case verification record ](docs/en/joint-case-kingbase-highgo.md); for the official account and technical group copy, please see [ community operation caliber ](docs/en/joint-case-operations.md).

## Quick start in 5 minutes

The following three paths all listen only on the host's loopback address by default. The automatic installer generates the persistent **administrator password**, not a one-time password; for remote access, please use SSH local forwarding, such as `ssh -L 7780:127.0.0.1:7780 user@server`, and do not expose 7780 directly to the public network.

### 1. Linux bare metal one-line installation (recommended)

v0.4.0 provides two native glibc packages: `linux/amd64` and `linux/arm64`: `uname -m` uses the amd64 package when it is `x86_64` / `amd64`, and uses the arm64 package when it is `aarch64` / `arm64`. Both require glibc 2.28+ and systemd as PID 1; the installer will automatically select the native architecture, verify the outer and inner package hashes of the release package, and install it as a systemd service. musl/Alpine and CentOS 7 are still not supported:

```bash
curl -fsSL https://github.com/cuipengdba/agentsql/releases/download/v4.3.0/install.sh | sudo sh -s -- install --version v4.3.0
```

Non-TTY output from pipelines and CIs does not show the automatically generated administrator password; root can view it at `/etc/agentsql/agentsql.env`. The interactive terminal is displayed once when credentials are first created, or `--show-password` can be added explicitly. After obtaining the tarball and the `.sha256` with the same name at the same time in the offline environment, verify, decompress and install from the package root:

The following is an example of the amd64 asset name of v4.3.0; arm64 hosts starting from v0.4.0 use the `-linux-arm64` asset name of the same version.

```bash
sha256sum -c agentsql-v4.3.0-linux-amd64.tar.gz.sha256
tar -xzf agentsql-v4.3.0-linux-amd64.tar.gz
cd agentsql-v4.3.0-linux-amd64
sudo ./install.sh install
```

### 2. Start Docker with one line

By default, the script pulls the exact GHCR tag corresponding to the latest stable Release from GitHub; the tag is `linux/amd64` + `linux/arm64` multi-architecture image, and Docker will automatically match the running architecture. The script generates `.env` with permission `0600`, uses `agentsql-data` to name the volume, and fixes the bound loopback address:

```bash
curl -fsSL https://raw.githubusercontent.com/cuipengdba/agentsql/v4.3.0/scripts/quickstart.sh -o quickstart.sh && sh quickstart.sh --version v4.3.0
```

The GHCR package must be set to public by the publisher so that the above command can be pulled anonymously in a non-logged-in environment. First check the public status of the precise tag `v4.3.0` and the dual-architecture manifest; Docker will automatically select `linux/amd64` or `linux/arm64` from the manifest list:

```bash
docker pull ghcr.io/cuipengdba/agentsql:v4.3.0
```

The equivalent single `docker run` is as follows; the random value is only passed in through the current shell environment and is not written into the command line parameters:

```bash
export AGENTSQL_SECRET="$(openssl rand -base64 24)" AGENTSQL_ADMIN_USER=admin AGENTSQL_ADMIN_PASSWORD="$(openssl rand -base64 18)"
docker run -d --name agentsql --restart unless-stopped --security-opt no-new-privileges:true -p 127.0.0.1:7780:7780 -e AGENTSQL_SECRET -e AGENTSQL_ADMIN_USER -e AGENTSQL_ADMIN_PASSWORD -v agentsql-data:/var/lib/agentsql ghcr.io/cuipengdba/agentsql:v4.3.0
```

### 3. Local Live Demo (one command, no source code required)

This self-contained environment only connects to synthetic PostgreSQL/MySQL demo data, not to the real database. No need to clone the source code, edit configurations or build images, just download a compose file and launch:

```bash
mkdir agentsql-demo && cd agentsql-demo
curl -fsSL -o docker-compose.yml https://raw.githubusercontent.com/cuipengdba/agentsql/v4.3.0/deploy/quickstart/docker-compose.yml
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
make build VERSION=v4.3.0
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

### v0.7.0 access change

- Clients that support MCP `2026-07-28` can use standalone `http://127.0.0.1:7780/mcp/2026-07-28`. Each request re-verifies the Agent API Key, tenant and QPS, requiring that the version header, `Mcp-Method`, and `Mcp-Name` when applicable are consistent with the request body metadata; no old version of `initialize` or transfer session is required. This portal only provides basic tools and does not currently provide B5 session/planned transaction tools; the old `/mcp` and stdio continue to be available. For request examples, see [MCP Access Guide ](docs/en/INTEGRATIONS.md).
- The console's "temporary read-only authorization" allows an administrator with both policy management and query execution permissions to issue a task key for up to 24 hours to an Agent, a data source, and an exact matching simple `SELECT`, and can be revoked. The Key is only displayed once, can only call `query`, and cannot expand the permissions of the original Agent; the original rules, masking, and auditing into the SQL pipeline continue to apply. See [ task-level temporary read-only authorization ](docs/en/task-read-grants.md).
- The "Access Wizard" adds Agent status, database connection and policy existence pre-check. Preflight does not execute SQL, and the existence of the policy does not mean that SQL has been authorized; compatible with the entrance`/mcp`Allow, deny, and audit have been checked in real Dify and Inspector clients, and new entries are still evidenced by SDK protocol testing. upgrade tov0.7.0This version was previously available`agentsqlctl migrate --check`Read-only report schema version, exit code`0`for the current,`2`To be migrated,`1`is an error; see backup and explicit migration steps[Deployment Guide](docs/en/DEPLOY.md#upgrades-and-rollbacks)。

### v0.8.0 Mission Authorization and Compatibility Changes

- The task key is now submitted by the applicant with precise SQL, Agent, data source and expiration time. Another approver of the same tenant checks the SQL and then approves or rejects it; after approval, the original applicant receives it all at once. The old direct issuance API returns HTTP 410, v0.7.0 The issued Key will continue to take effect according to the validity period and revocation rules.
- Authorization ID association application, approval, issuance, and allow or deny auditing after entering the SQL pipeline. When metadata/audit splits the database, management audits are reliably queued in the outbox of the metadata database and then delivered to the audit database; the backlog should be monitored.
- Dify 1.17.1 real workflow and MCP Inspector 2.11.0 have completed the specified link review, and the tool schema of the two MCP HTTP entries passed strict inspection by Inspector. Complete backup database and pairing `AGENTSQL_SECRET` before upgrading, see [v1.0.0 release notes ](docs/en/release-notes-v1.0.0.md) for operation.

## function matrix

| Ability | current code | Description |
| --- | --- | --- |
| MCP stdio / Streamable HTTP | support | HTTP uses Bearer API Key; stdio binds a single Agent Key |
| MCP `2026-07-28` Stateless HTTP | v0.7.0 | Independent path `/mcp/2026-07-28`, the old entrance remains available; the new path does not register the B5 tool |
| Task-level temporary read-only authorization | v0.7.0 | Limited to Agent, data source, exact SQL and up to 24 hours; only `query` allowed, can be revoked |
| Approval and collection of task authorization by different persons | v0.8.0 | SQL only displays the approval details to those with the authority to approve; the original applicant receives the Key at one time |
| Access pre-check and migration version check | v0.7.0 | The wizard preflight does not execute SQL; `migrate --check` is read-only and does not replace backup and real request acceptance. |
| Agent and data source management | support | API Key only returns plain text when creating/rotating, and the hash is stored in the library. |
| PostgreSQL/MySQL SQL parsing | support | Structured AST judgment, does not rely on text regularity instead of parsing |
| Table-level and column-level authorization | support | Denied by default; see "Known Limitations" for special JOIN/wildcard semantics |
| Read-only, dangerous statements, current limiting and Explain risk rules | support | Four state results: allow / deny / approve / warn |
| Controlled queries and writes | support | Timeouts, connection limits, result truncation and error masking |
| Manual approval | support | Order creation, administrator decision, Agent query results |
| cascade masking | support | Nine types are used according to the capability matrix `mask` / `hash` / `block` / `range`; `range` is only used for `number` / `date`, with numerical bucketing or date truncation to retain coarse-grained distribution and no key required |
| Table. List of sensory masking and safety precautions | support | The rules support three scopes: global column, `表.列`, and `模式.表.列`, with precise hits based on source tables/source columns; JOIN naked columns, `SELECT *`, CTE outer layer, etc. When the column ownership cannot be determined and a protected table may be involved, the fail-closed fixed blocking is `***` and cannot be closed. PostgreSQL leaves the pattern empty to match any pattern, MySQL leaves the pattern empty to represent the current database |
| SQLite / PostgreSQL control plane, audit export and dashboard | support | SQLite defaults to zero configuration; PostgreSQL can use independent metadata/audit libraries |
| Webhook / Syslog notification outgoing | support | live-only, best-effort; the default is only deny/error, the audit library is still the authoritative record |
| Web management console | support | Available `console_enabled: false` does not mount the management plane at all |
| Large screen real-time event streaming | support | SSE, enabled by default, up to 100 concurrent management connections |

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

Except for the designated version-controlled read-only versions of MongoDB, Redis, Valkey, and Milvus, other NoSQL/vectors currently have connection-level, low-level support; the read-only preview does not represent full query, retrieval, or server-side security capabilities. For the 21 registration types, default ports and actual connection port boundaries of HBase/Couchbase, see [NoSQL support range ](docs/en/nosql-support.md). Designated instances of DM8, KingbaseES, and Vastbase passed controlled read-only query, real MCP client, permission revocation, database break recovery, masking and persistent auditing in v1.5.0; v1.6.0 passed independent audit library failure, lock wait timeout, client cancellation, database side termination and recovery retest in the same scope, and still does not declare full production support. DM8's R006, multi-line paging, JOIN, functions and system directories are not verified and writing is not released. Oracle has completed the controlled read-only closed loop on the 26ai Free designated instance of v2.2.0. The scope is shown in the [ acceptance record ](docs/en/v2.2.0-acceptance.md); the commercial version and full production support have not been verified. In the designated instance of v2.1.0, YashanDB uses the real minimum privilege account, HTTP MCP and independent persistent audit library to complete narrow SELECT, R010, mobile phone number masking, dual clients, permission revocation and fault shutdown; write, transaction, EXPLAIN, TLS, primary and backup and complete production support have not yet been verified, see [YashanDB Dialect boundaries ](docs/en/yashan-dialect.md) and [ are accepted in this version as ](docs/en/v2.1.0-acceptance.md). All actual measurements are only based on specified container images and synthetic use cases. **is not officially certified by the database manufacturer as**; protocol compatibility does not mean the same security semantics. All database requests must go through the AgentSQL gateway and use a dedicated minimum-privilege account; all requests will fail-closed when parsing, authorization, or capability facts are unknown. See [SECURITY.md](SECURITY.en.md) for details.

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
- [v3.0.2 Release instructions and acceptance record](docs/en/release-notes-v3.0.2.md)
- [Current version operation and maintenance manual](docs/en/OPERATIONS.md)
- [ Sensitive column discovery and masking draft ](docs/en/DISCOVERY.md)
- [Webhook / Syslog notification outgoing and security configuration ](docs/en/NOTIFICATIONS.md)
- [ deployment, PostgreSQL control plane, upgrade, backup and systemd](docs/en/DEPLOY.md)
- [Local Live Demo, regular reset and public deployment security list](docs/en/DEMO.md)
- [Product and Engineering Specifications](docs/en/SPEC.md)
- [Security Policy and Privacy Vulnerability Report](SECURITY.en.md)
- [Change record](CHANGELOG.en.md)
- [Issue Feedback](https://github.com/cuipengdba/agentsql/issues)
- [Commercial authorization and enterprise version cooperation](COMMERCIAL-LICENSE.en.md)

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
make build VERSION=v4.3.0
```

`pg_query_go` requires cgo; do not build with `CGO_ENABLED=0` or Alpine/musl. Please read [SPEC](docs/en/SPEC.md) before submitting changes to test for behavioral changes and keep the `tests/corpus` decision-making corpus from being unintentionally rewritten.

## Licensing, Commercial Authorizations and Trademarks

**License change notice:** Source code, tags and release packages for v0.4.0 and earlier retain the licenses that applied when they were released; those assets are neither retagged nor reissued. Starting with v0.5.0, the open source edition uses the [Apache License 2.0](LICENSE), including the [NOTICE](NOTICE) file, with separate [commercial licensing terms](COMMERCIAL-LICENSE.en.md). Apache-2.0 permits closed source integration, commercial SaaS and redistribution. For trademark permission, enterprise modules, warranties or an SLA, see the commercial licensing terms. Rights to the AgentSQL name (including its Chinese name, "Zhidun") and logo are reserved. You may fork the source under its license, but may not use the AgentSQL / Zhidun name or logo to present a derivative release as the official product. Commercial licensing and enterprise cooperation: 87326549@qq.com | https://agentsql.cn.

For configuration, threat boundary, and migration instructions for v0.5 personnel authentication (MFA / OIDC / LDAP), see [ Identity Authentication Guide ](docs/en/AUTHENTICATION.md).

# Changelog

**Language:** [English](CHANGELOG.en.md) | [Simplified Chinese](CHANGELOG.md)

## [v3.0.2]

- Introduce a shared SVG AgentSQL logo across the website, GitHub README, login page, sidebar and browser icons.

- Complete the English mirrors of the README, documentation and official website, preserving section/table/code coverage. Add current operations and documentation indexes.
- Apply the selected light/dark theme to the entire console sidebar and retain independent menu scrolling.
- Add a pinned SQLBot v1.10.2 PostgreSQL execution adapter with workspace mapping, permission-filtered typed schema, masking, durable audit checks and no direct database fallback.
- Remove group QR images from current GitHub and website pages; retain the author and official-account contact channels.
- Preserve metadata/audit schemas 23/22/6 and all existing database support boundaries. See [release notes](docs/en/release-notes-v3.0.2.md).

## [v3.0.1]

- Add Chinese/English switching to the official website, Live Demo entry pages, and Web console. Keep the GitHub README in both languages; GitHub's own site language remains a user account preference.
- Translate the console shell and existing static UI copy, switch Ant Design, date, and number locales, and persist the user's language choice. Preserve raw SQL and query result values.
- Make the left navigation independently scrollable so its bottom entries remain reachable in short viewports and at high display scaling. No database protocol or schema changes; see [design](docs/en/v3.0.1-design.md), [release notes](docs/en/release-notes-v3.0.1.md), and [acceptance](docs/en/v3.0.1-acceptance.md).

## [v3.0.0]

- Add a single server-maintained capability catalog for all 30 registered datasource types, exposed through the management API, the datasource console, and authorized MCP datasource listings. The four levels distinguish controlled SQL, bounded SQL, dedicated read tools, and connection-only preview without expanding database support claims.
- Add read-only `agentsqlctl doctor` with structured migration readiness and fixed exit codes; it never creates missing SQLite files or persists a gateway instance ID. Add a synthetic MCP acceptance script for tool discovery, allow/deny, masking, and durable audit IDs.
- Build CI, signed Linux packages, and GHCR images with Go 1.26.10 after the Go vulnerability database identified a reachable `crypto/x509` issue in 1.26.9.
- Preserve v2.9.0 schema versions (combined metadata 23, separate metadata 22, audit 6); verify package upgrade and paired restore. See [design](docs/en/v3.0.0-design.md), [acceptance](docs/en/v3.0.0-acceptance.md), and [release notes](docs/en/release-notes-v3.0.0.md).

## [v2.9.0]

- Add canonical tenant policy bundles, bounded metadata-only diffs and replay probes, separate author/reviewer approval, atomic table-policy application, and drift status. Preserve existing B2 and column policies unchanged in exported bundles.
- Add externally held Ed25519 audit archive checkpoints with key generation, signed archive and chain-head declarations, trusted predecessor checking, and age limits. A checkpoint does not prove WORM storage or independently recompute a filtered archive's chain head.
- Increase combined metadata schema 22→23 and separated metadata schema 21→22; independent audit schema remains 6. See [design](docs/en/v2.9.0-design.md), [candidate acceptance](docs/en/v2.9.0-acceptance.md), and [release evidence](docs/en/v2.9.0-release-evidence.md).

## [v2.8.0]

- Add a bounded Milvus 2.6.25 `search_vectors` MCP capability for a TLS standalone instance and the `default` database. Require an authenticated non-root account, exact collection and vector/output field grants, finite single-vector input, dimension and COSINE index checks, source-aware masking, and durable preflight and terminal audit.
- Verify real HTTP MCP, MCP Inspector and Dify calls, database least privilege, policy revocation, wrong-CA denial, independent audit-store outage, Milvus outage, and recovery on synthetic data. Other Milvus versions, clusters, replication, hybrid search, writes, and full production support remain unverified. See the [candidate acceptance record](docs/en/v2.8.0-acceptance.md).

## [v2.7.0]

- Add a bounded ClickHouse 26.8.31.2 `query_analytics` MCP capability for a single TLS node and MergeTree table. Require exact table and explicit column grants, a read-only role, String equality filters, server-enforced resource limits, source-aware masking, and durable preflight and terminal audit.
- Verify real HTTP MCP, Inspector and Dify calls, policy revocation, parameter binding, wrong-CA denial, cancellation, and independent audit-store and business-database outages against synthetic data. Other ClickHouse versions, clusters, replication, writes, broad SQL, and full production support remain unverified. See the [candidate acceptance record](docs/en/v2.7.0-acceptance.md).

## [v2.6.0]

- Add a separate Redis 8.2.9 / Valkey 8.1.10 `read_key` capability for an exact UTF-8 string key under strict TLS, least-privilege ACL, Agent key policy, bounded result size, source-aware masking, and durable preflight/terminal audit.
- Verify real HTTP MCP calls with Inspector and Dify, policy revocation, wrong CA, Redis/Valkey and independent audit-store outages, AOF recovery, and fail-closed result delivery on isolated synthetic instances.
- Publish signed dual-architecture packages, exact GHCR tags, the official website and Live Demo, then promote the accepted image digest to `latest`. See the [release evidence](docs/en/v2.6.0-release-evidence.md) for tested versions and limits.

## [v2.5.0]

- Add a bounded MongoDB 7.0.43 single-instance `find_documents` capability with Agent collection/field authorization, strict TLS, least-privilege credentials, masked projection, durable audit and fail-closed error handling.
- Verify real MCP clients, policy and database privilege revocation, business and independent audit-store outages, and recovery on synthetic data. See the [release evidence](docs/en/v2.5.0-release-evidence.md); replica sets, sharding, writes and full production support remain unverified.

## [v2.4.0]

- Add a separate `opengauss` type for the checksum-verified openGauss 7.0.0 LTS build, with verified TLS, read-only connection checks, a bounded `FROM ONLY` physical-table SELECT parser, and no PostgreSQL B2/B5 inheritance.
- Verify a least-privilege account through real HTTP MCP, masking, R010 denial, single-use approval, durable audit, Inspector/Dify clients, SELECT revocation, business and audit outages, and `gs_dump`/`gs_restore` of synthetic data.
- Keep other builds, GaussDB commercial editions, HA, writes, broad SQL, production certificate rotation, and full production support outside the tested boundary. See the [candidate acceptance record](docs/en/v2.4.0-acceptance.md).

## [v2.3.0]

- Verify SQL Server 2025 Developer on one isolated Linux x86_64 instance with strict certificate validation, a login granted only target-table SELECT and database SHOWPLAN, real HTTP MCP masking and R010 denial, durable audit, and Inspector/Dify calls.
- Stop static SQL Server R010 denials before database SHOWPLAN so a reader without access to the denied table receives the policy decision without touching that table. Exercise SELECT/SHOWPLAN revocation, mismatched CA and certificate name, SQL Server outage, and independent audit-store outage.
- Keep complex T-SQL, writes, B2/B5, other server versions, Windows/Always On, production certificate rotation, and full production support outside the tested boundary. See the [candidate acceptance record](docs/en/v2.3.0-acceptance.md).

## [v2.2.0]

- Connect the existing Oracle Free native driver and narrow SELECT parser to the HTTP MCP authorization, masking, and durable audit path on one named `FREEPDB1` instance. Verify a least-privilege account, Inspector/Dify calls, policy and database privilege revocation, plus business and independent audit database outages.
- Keep Oracle pre-execution `EXPLAIN PLAN` disabled for the least-privilege account until its plan-object permissions are proven. Do not extend B2/B5, write operations, commercial-edition support, TCPS, HA, or full production support from this synthetic case. See the [candidate acceptance record](docs/en/v2.2.0-acceptance.md).

## [v2.1.0]

- Join the bounded YashanDB 23.4.1.109 read-only path with a real least-privilege database account, HTTP MCP, masking, policy denial and durable audit in one live regression.
- Verify MCP Inspector 0.16.8 and Dify MCPClient 1.17.1 against the isolated instance. Rehearse database SELECT revocation, business database outage and independent PostgreSQL audit-store outage without delivering unmasked results.
- Keep writes, transactions, EXPLAIN, unverified SQL forms, server-verified TLS, HA and complete production support outside this release's YashanDB claim. See the [candidate acceptance record](docs/en/v2.1.0-acceptance.md).

## [v2.0.0]

- Add a fixed two-replica Kubernetes deployment for stateless read-only MCP traffic backed by shared PostgreSQL metadata and a separate audit database. Keep B5 transactions, stateful sessions and event replay disabled in this topology.
- Add a management API to enroll one regular PostgreSQL table's explicit columns under a locked catalog proof and policy revision CAS. Enrollment SQL is analyzed without execution; unsupported native, join, view and wildcard cases fail closed.
- Bound concurrent RBAC bootstrap and B2 activation retries, and rehearse two-node failover, dependency outages, paired backup restore and a maintenance-window rollback to v1.9.0. The [v2.0.0 acceptance record](docs/en/v2.0.0-acceptance.md) limits these claims to the tested synthetic environment.

## [v1.9.0]

- Add an onboarding flow for Dify 1.17.1 and MCP Inspector 0.16.8 with client-reachable gateway URLs and key-free connection instructions.
- Reuse the selected Agent and datasource for connection and policy preflight; explain one SQL statement as an offline deny or unknown result, then link a real client audit ID to the audit console.
- Expand connection and failure guidance without changing Agent identity, authorization, execution, masking, or audit contracts. Real-client and release acceptance are recorded separately in the [v1.9.0 acceptance record](docs/en/v1.9.0-acceptance.md).

## [v1.8.0]

- Allow independent Vastbase G100 Build 9 PG-mode datasources to use `verify-full` with an absolute CA path and verified certificate hostname; reject missing or unsafe TLS options in the management API and guide new console configuration toward verified TLS.
- Verify an isolated G100 V3.0 Build 9 instance using a read-only account, correct/wrong CA, wrong hostname, plaintext rejection, certificate and credential rotation, masked MCP allow/R010 deny, durable audit, MCP Inspector and Dify 1.17.1. The bounded single-table SELECT contract remains unchanged.
- Document the measured `require_ssl=on` protocol 3.0 incompatibility and the tested `ssl=on` plus account-specific `hostnossl ... reject` deployment. Trial licensing, formal HA and production capacity remain unverified; see the [release notes](docs/en/release-notes-v1.8.0.md) and [acceptance record](docs/en/v1.8.0-acceptance.md).

## [v1.7.0]

- Add explicit CA and hostname verification for PostgreSQL-wire datasources; verify the bounded KingbaseES V9R1C10 PG-mode path against a real TLS instance, including wrong-certificate rejection, certificate and credential rotation, masked MCP allow/deny, and durable audit. DM8 server-verified TLS and HA remain unverified.
- Add an independent, read-only `highgo` datasource for the verified HighGo 9.0.10 Enterprise PG-mode instance. Check vendor identity, mode and read-only state on every new pool connection; deny PostgreSQL-only B2/B5 capabilities and SQL outside the bounded single-table SELECT subset.
- Verify live schema/query/EXPLAIN, minimum database privilege, masked MCP allow and policy deny, durable audit, and Inspector/Dify client interoperability. Exercise database grant revocation, outage/recovery, independent audit-store outage, lock timeout and client cancellation on the isolated instance.
- Keep other HighGo versions, modes, TLS, HA, extended types and production load unverified; see the [design](docs/en/v1.7.0-design.md), [release notes](docs/en/release-notes-v1.7.0.md) and [acceptance record](docs/en/v1.7.0-acceptance.md).

## [v1.6.0]

- Close audit-store outage, database lock timeout, client cancellation, fail-closed result delivery and masked recovery paths on bounded DM8 Pack3, KingbaseES V9R1C10 PG mode and Vastbase G100 Build 9 PG mode instances.
- Propagate cancellation through the stateless MCP endpoint, bound DM socket waits and expose distinct gateway readiness and datasource Ping states in the console.
- Verify MCP Inspector and Dify SDK allow/deny/masking on all three instances, and recheck signed dual-architecture packages, v1.5.0 upgrade and backup restore, GHCR anonymous pulls, website and Live Demo before promoting `latest`.

See the [v1.6.0 design](docs/en/v1.6.0-design.md), [release notes](docs/en/release-notes-v1.6.0.md) and [acceptance record](docs/en/v1.6.0-acceptance.md) for exact versions, evidence and remaining limits.

## [v1.5.0]

- Deepen bounded live verification for DM8 Pack3, KingbaseES V9R1C10 PG mode and Vastbase G100 Build 9 PG mode: minimum privilege, masked allow, policy deny, durable audit, permission revocation and database outage/recovery.
- Exercise an allow and a deny call per database through MCP Inspector 0.16.8 CLI and Dify 1.17.1 MCPClient. Keep per-vendor query timeout/cancel and separate audit-store outage explicitly unverified.
- Verify official v1.4.0 package upgrade with unchanged SQLite schema 22 and separated PostgreSQL metadata/audit schemas 21/6, preserved data, old-binary read-only checks and paired backup restore.
- Publish dual-architecture signed packages and exact GHCR tags, then update the public website, Live Demo and `latest` from v1.0.0 directly to v1.5.0.

See the [v1.5.0 design](docs/en/v1.5.0-design.md), [release notes](docs/en/release-notes-v1.5.0.md) and [acceptance record](docs/en/v1.5.0-acceptance.md) for exact versions, evidence and remaining limits.

## [v1.4.0]

- Share console password and LDAP login-attempt budgets across gateways backed by PostgreSQL metadata, using an HMAC account key, database-clock window and atomic transaction. Fail closed on metadata outage and bound the network wait.
- Verify two-gateway restart, rate-limit and metadata-outage behavior, and three-PostgreSQL audit outage/recovery with a durable audit chain and drained outbox.
- Add SQLite/combined PostgreSQL metadata migration 22 and separated PostgreSQL metadata migration 21; keep audit-only migration 6. Verify official v1.3.0 package upgrade and paired backup restoration.

See the [v1.4.0 design](docs/en/v1.4.0-design.md), [release notes](docs/en/release-notes-v1.4.0.md) and [acceptance record](docs/en/v1.4.0-acceptance.md). The public website, Live Demo and GHCR `latest` remain at v1.0.0 until v1.5.0.

## [v1.3.0]

- Add an opt-in, single-table InnoDB `SELECT` column-authorization subset for verified MySQL 8.0 and 8.4 builds. Keep unsupported expressions, joins, views and table shapes closed before execution.
- Bind enrollment and execution to a stable InnoDB table ID with separate PROCESS-only metadata and SELECT-only business accounts, a read-only transaction, metadata lock, post-execution catalog check and final audit/control fence.
- Verify allow, mask, deny, DDL races, same-name table rebuild, privilege revocation and durable MCP audit against isolated MySQL 8.0.46 and 8.4.11 instances; retain the prior table-only route when no column policy applies.

See the [v1.3.0 design](docs/en/v1.3.0-design.md), [release notes](docs/en/release-notes-v1.3.0.md) and [acceptance record](docs/en/v1.3.0-acceptance.md) for scope and evidence. The public website, Live Demo and GHCR `latest` remain at v1.0.0 until v1.5.0.

## [v1.2.0]

- Add an optional MCP OAuth protected resource at `/mcp/oauth` with discovery metadata and standard Bearer challenges. Preserve both existing Agent Key endpoints.
- Verify RS256 access tokens for the exact resource audience and required scope against the configured OIDC issuer's JWKS. Bind a separate Agent Key, active local OIDC user, tenant and live query permission on each request.
- Require the MCP OAuth issuer and tenant to match the configured console OIDC identity source; support loopback HTTP only for local OIDC testing. Document the bounded interoperability claim and rollback path.

See the [v1.2.0 design](docs/en/v1.2.0-design.md) and [release notes](docs/en/release-notes-v1.2.0.md) for scope and verification.

## [v1.1.0]

- Bind delegated approval requests and single-use execution to the verified human user, Agent and tenant; enforce owner checks in the atomic claim and preserve the old Agent-only path.
- Enable B5 logical sessions for delegated MCP requests with a persisted human/Agent/tenant principal and identity-bound continuation and transaction plan; reuse only the authenticated Agent's existing DML grants.
- Add per-token delegation revocation, console display of approval ownership, and bounded password/LDAP login attempts. Existing v1.0.0 delegation tokens remain valid until their original short expiry.

See the [v1.1.0 design](docs/en/v1.1.0-design.md) and [release notes](docs/en/release-notes-v1.1.0.md) for exact scope and verification.

## [v1.0.0]

- Fail closed for SELECTs with column policies unless PostgreSQL B2 can provide a verified binder path; do not fall back to table-only authorization for JOINs, self-joins, predicates, or wildcard projections.
- Add optional Agent-bound, short-lived human delegation tokens for both HTTP MCP endpoints, with live session/RBAC checks, stateful session identity binding, and audit attribution. Console access tokens cannot substitute for these tokens.
- Expose delegation issuance in the console, clarify column-policy limits, and record a repeatable design-to-Demo release routine. Keep database support claims within existing verified scopes.

See the [v1.0.0 release notes](docs/en/release-notes-v1.0.0.md) and [acceptance record](docs/en/v1.0-acceptance.md) for compatibility, evidence, and remaining limits.

## [v0.9.0]

- Bound newly requested task read keys to one returned result by default, with a configurable 1–10 result budget and atomic cross-instance consumption before delivery; preserve legacy unbounded grants.
- Expose tenant-scoped management-audit outbox backlog and retry status in the control console for separated audit stores.
- Add a policy SQL preflight that reports definite denial or unknown without running SQL or claiming permission to execute.
- Extend PostgreSQL/MySQL sensitive derived-output regression cases and correct current security documentation to match verified behavior and limits.
- Upgrade combined metadata schema to 20 and separated metadata schema to 19; carry task-key budget fields through SQLite-to-PostgreSQL migration.

Release scope and acceptance gates are recorded in the [v0.9.0 release notes](docs/en/release-notes-v0.9.0.md) and [acceptance record](docs/en/v0.9-acceptance.md). Final commit, CI, signed assets, GHCR, website, and Live Demo evidence is published with the GitHub Release.

## [v0.8.0]

- Require a same-tenant second reviewer to approve exact SQL before a requester can claim a task-scoped read-only API key; retire the direct-issue API with HTTP 410 while preserving existing grants.
- Keep request, review, and issue actions in metadata transactions with management-audit outbox events and authorization-ID-linked traffic audits.
- Make legacy and stateless MCP tool schemas portable to strict clients; add a control-console workflow and audit filter for the reviewed grants.
- Extend onboarding diagnostics for authentication, permission, protocol, database, and audit failures.
- Reverify Dify 1.17.1, MCP Inspector 2.11.0, DM8, KingbaseES V9, Vastbase G100, and isolated v0.7.0 upgrade and restore paths within the documented bounds.

Official board submission, CI, signature assets, GHCR and official website results are subject to [v0.8.0 GitHub Release](https://github.com/cuipengdba/agentsql/releases/tag/v0.8.0). For functions and limitations, see [ Release Notes ](docs/en/release-notes-v0.8.0.md) and [ Acceptance Record ](docs/en/v0.8-acceptance.md).

## [v0.7.0]

- Add a separate stateless MCP 2026-07-28 endpoint while retaining the existing MCP transports and their security boundaries.
- Add task-scoped temporary read-only grants bound to one Agent, tenant, datasource, exact SQL statement, and expiry, with revocation and management audit.
- Add read-only `agentsqlctl migrate --check` for upgrade preflight, and improve release Buildx plugin discovery in isolated Docker configurations.
- Add onboarding checks for Agent, datasource, and policy readiness, plus an operations runbook for migrations, audit outages, and MCP protocol rejection.
- Verify two allowed and two R010-denied queries in the MCP Inspector 0.16.8 and Dify 1.17.1 GUIs through the existing `/mcp` endpoint, including masked results and four linked persistent audits. The new protocol endpoint has separate Go SDK tests.

Official closure submission, simultaneous submission CI, release date, signature assets, GHCR and official website online results are subject to the [v0.7.0 GitHub Release](https://github.com/cuipengdba/agentsql/releases/tag/v0.7.0) text and release evidence. Functional evidence and support boundaries are provided in [v0.7.0 Release Notes ](docs/en/release-notes-v0.7.0.md) and [ Acceptance Record ](docs/en/v0.7-acceptance.md).

## [v0.6.1] - 2026-10-16

- Fixed the defect that when the PostgreSQL metadata/audit sub-library was written to the wrong database, the management audit could not be used as a rollback target; the management and traffic audit were written to the `traffic` chain of the audit database, and the metadata database was retained to be delivered to the outbox.
- Keep the metadata `16` and audit `6` schemas without adding migration or expanding the database support range. Isolated dual database environment has verified service startup, managed audit persistence, audit chain and complete backup and recovery.

For formal assets and applicable boundaries, see [v0.6.1 Release](https://github.com/cuipengdba/agentsql/releases/tag/v0.6.1) and [ Maintenance Notes](docs/en/release-notes-v0.6.1.md). Specific production deployments must still verify backup and recovery in the target environment.

## [v0.6.0] - 2026-10-14

- Add users, roles, tenant console and first-time access wizard, and tighten the tenant and permission delegation boundaries of the management API.
- Ordinary SQL approval adds archiving, expiration, single claim execution, reauthorization and final status audit; MCP basic tools add `execute_approved`.
- Add version-specific restricted read-only adaptation of Vastbase G100 V3.0 Build 9 PG mode and verify core MCP protection links with real examples.
- Expand the SQLite/PostgreSQL approval migration and SQLite→PostgreSQL upgrade path, and fix the misjudgment of empty targets caused by preset permissions.

For scope and unmeasured items, see [v0.6.0 Release Notes ](docs/en/release-notes-v0.6.0.md).

Important changes in this project are recorded here, and the format refers to [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/).

## [v0.5.1] - 2026-10-13

### Added

- HTTP MCP explicitly accepts the two stateful handshake versions `2025-06-18` and `2025-11-25`; still rejects the uncensored `2026-07-28` session-free protocol and conflicting version claims.
- Standard progressive JSON stdio becomes the default transport and can be used directly by standard clients such as Claude Desktop; the original length prefix transport remains as an explicit `--sealed-stdio` compatibility option.
- Supplemented the Claude Desktop local HTTP bridging and Dify self-hosted access guide; uses the `mcp-remote` bridge program required by Claude Desktop with the actual `MCPClient` code for Dify 1.17.1 to verify handshake, tool discovery, and tool invocation with Agent Key respectively. Dify 1.17.1 requires static configuration of the `MCP-Protocol-Version: 2025-06-18` request header.

### Fixed

- Fix the incorrect number of fields when reading masking rules from PostgreSQL control plane snapshots; masking rules are only read from the current tenant, use the v0.5.1 mirror for quick startup configuration, and fix the PostgreSQL extended initialization verification of new data volumes.
- The Agent identity of MCP HTTP and stdio is bound to the persistent tenant every time the tool is executed; authoritative auditing after canceling the request retains the tenant context to avoid falling into the default tenant.
- CI offline compilation fixture prefetch required modules, unify the Go version used for build and govulncheck to `1.26.9`, and correct the govulncheck Action parameters.
- The Dameng and KingbaseES real database verification script converts `Mcp-Session-Id` in the PowerShell response header array into a single string to avoid false positives that the session does not exist when the tool is called for the first time; the Dameng script cleans up the test users and tables it created when it fails.

### Verification

- DM8 Pack3, `COMPATIBLE_MODE=0` real database re-verification through driver parameter binding, read-only account write rejection, HTTP MCP controlled SELECT, lineage column, R010 unauthorized rejection, mobile phone number masking, parsing failure shutdown and three persistence audits; only for the above synthetic scenarios.
- KingbaseES V9R1C10 PG mode real database revalidation via license startup, CRUD/pagination/identifiers, pgx parameter binding with SCRAM, read-only account write denial, AgentSQL controlled SELECT, column lineage, masking, R006 deny and allow/deny auditing; only for the synthetic scenario described.

## [v0.5.0] - 2026-10-10

> v0.5.0Officially released on 2026-10-10 (Release non-draft, GHCR fully public). relatively publishedv0.4.0Detailed evidence, asset status and release date gate are available at[v0.5.0 Release Notes](docs/en/release-notes-v0.5.md)。

### Added

- v0.5 The control plane adds TOTP MFA, OIDC Authorization Code + PKCE and LDAP/AD TLS bind login; external groups must be explicitly mapped to tenant roles, MFA/SSO/directory status exceptions are all fail-closed, and refresh token rotation, replay family revocation and logout revocation links are reused.

- Added the bounded connection/metadata capabilities of independent dialects of DM, Oracle and YashanDB; Oracle completed strict read-only subset and EXPLAIN normalization testing. The read-only SELECT, column kinship, R010 table strategy, audit and mobile phone number masking link of DM8 Pack3 and `COMPATIBLE_MODE=0` passed the verification of the batch 65 real database, and the matrix turned green; R006 was not verified, multi-row paging/JOIN/function/system directory was not tested, and writing was not released.
- The registry adds 7 categories and 21 NoSQL/vector connection-level data sources (key value, document, wide column, graph, time series, OLAP, vector), plus 6 relational types, a total of 8 categories and 27 models; only bounded ping, version, Schema and read-only preview are provided, and it is not expanded to a complete SQL safety closed loop. For itemized types and default ports, see [NoSQL support range ](docs/en/nosql-support.md).
- DM/Oracle's controlled SELECT parser supplements offline boundary regressions such as ROWNUM, projected aliases, NULL semantics, and paging; YashanDB adds `NewYashanParser()` offline SELECT profile, and batch 71 connects to standard `NewParser`, general rules, and narrow SELECT execution paths. The independent client 23.4.7.100 in the old unreleased dry-run package passes the native driver read-only write rejection probe; the real database pipeline test passes SELECT, column descent, R010, mobile phone number masking and three test audits. HTTP/MCP and persistence audits still need to be verified.
- Added parser malformed input, error stability and concurrency isolation, as well as pipeline empty SQL/incomplete SQL pre-execution rejection tests; for coverage and offline dependency restrictions, see [ test coverage instructions ](docs/en/test-coverage-notes.md).
- Added RBAC/multi-tenant MVP: local users, tenants, roles, permissions, multiple roles, role inheritance and management API route-by-route authorization; all capabilities open, no permission gating.
- New enterprise audit query/report and compliance export packages are added, as well as a self-contained five-minute quick-start demonstration stack that does not require cloning the source code.
- Enterprise audit export adds pure Go, multi-page CJK PDF and compliant ZIP; ZIP fixedly carries CSV, JSONL, PDF, summary and file-by-file SHA-256 `manifest.json`, CLI can strictly verify members, size and summary, management API/console reuses existing filtering, RBAC, 10000 row limit and `audit.export` traces.
- Added English home page, quick start, management, MCP access, security and coverage documents.
- Added KingbaseES + HighGo joint case verification records, operation standards and demonstration SQL, strictly distinguishing between actual testing of specified image protocol paths, commercial version final verification and manufacturer certification.
- Added MCP Registry description, official website ecology/compatibility section and community entrance, and systematically completed domestic database research, priorities, joint cases and caliber documents.

### Changed

- KingbaseES KingbaseES V9 has been verified by seventy-two real libraries. The user decided on 2026-10-09 to convert **only to V9R1C10, PG mode, and the PG protocol basic closed-loop** matrix position of the single-instance synthesis table to 🟢: writable license volume startup, ksql CRUD/paging/identifier, pgx/v5 `$1` binding + SCRAM, read-only role write rejection `42501`, AgentSQL controlled SELECT allow + mobile phone number masking + column blood + R006 comment rejection + allow/deny audit measurement passed. Actual license expiration dates and other modes of V9, V8, complex queries, B2, TLS, HA and production loads have not yet been verified; not manufacturer certification or production support commitment. See [ real database verification record ](docs/en/kingbase-v9-verification.md).
- HighGo HighGo 9.0.10 Enterprise Edition is in the PG mode and single-instance synthetic table range. It has been verified by batch 70 real databases and passed the parsing/column lineage, table-level authorization, controlled read-only SELECT, mobile phone number masking, R006 rejection and allow/deny audit link; the user made the decision on 2026-10-08, and the matrix gear was changed to 🟢 The designated instance protection link passed the actual test. TLS, connection pool failover, cancellation/timeout, EXPLAIN, extension type/OID, system directory differences, B2 column-level authorization, parse-error audit branch (not triggered in this round), cross-version and production load have not been tested; do not constitute vendor certification or production support commitment. See [ real database verification record ](docs/en/highgo-verification.md).
- The OpenTenBase PostgreSQL kernel adds strict, version-limited distributed plan normalization, and completes the minimum safe closed loop in the designated v2.5.0 stand-alone GTM/CN/DN topology; the TXSQL/MySQL kernel is still to be tested in the official environment.
- The PolarDB for PostgreSQL designated community image completes the experimental security closed loop through the existing `postgres` path, without adding an alias or vendor identification switch.
- PolarDB-X reuses `db_type=mysql`, adds a two-stage fail-closed plan adaptation from strictly recognized `LOGICAL EXECUTIONPLAN` to `EXPLAIN EXECUTE`, and is compatible with corpus and opt-in E2E; not mixed with PolarDB for PostgreSQL.
- OpenTenBase, TiDB, OceanBase and PolarDB-X add fail-closed EXPLAIN adaptation and regression fixtures; unknown versions, plan columns or nodes continue to be rejected.
- MCP Streamable HTTP first time `initialize` does not need to carry `MCP-Protocol-Version` in advance; the version, session and Agent/Key binding verification of subsequent requests remain strict.
- The scene card of the demo stage aligns the real script with the approval results; the business error caliber further distinguishes expected authorization failures, missing objects and database execution errors.
- The official website search gate is adjusted to allow the main site and maintain `/demo/` to prohibit indexing; the actual deployment status still needs to be reviewed by the Japanese and foreign websites.
- `scripts/release-dryrun.ps1` provides a command to generate/verify a dry-run path for 15 expected assets and provides a production-ready verification path with `-ValidateOnly`; official signing, uploading, and release are still pending on the release day gate. For the designated Linux environment PostgreSQL parser P99 of batch 61, 5/5 of each group met the standard, and they still have to be retested exclusively on the release day.
- Starting from v0.5, the open source version license is adjusted from AGPLv3 to Apache-2.0, and independent commercial licensing instructions are provided.

### Fixed

- Fixed the issue where the MCP first handshake protocol version access control is incompatible with the mainstream client standard process.
- Fix the Demo table foreign key conflict with the sealed JOIN script, and strengthen the daily reset seed/gateway startup timing.
- Fix the missing production dynamic alarm in R005: the universal path is evaluated according to EXPLAIN, and the universal execution and PostgreSQL column-level authorization paths are supplemented with an alarm when `row_limit` is actually truncated; at the same time, the front-end rule metadata and document caliber are corrected.

### Known Issues

- Oracle's complete gateway authorization/masking closed loop, YashanDB HTTP/MCP and persistence audit finalization, KingbaseES V9's ability to exceed the above-mentioned PG protocol basic closed loop, and the final verification of the target environment of commercial versions such as GaussDB/TDSQL have not yet been completed. Redistribution of YashanDB C client has been verbally authorized by the manufacturer; the repository does not have written authorization documents.
- Login rate limiting/locking and MCP transport session cross-process persistence have not yet been delivered; the real enterprise IdP/directory compatibility matrix of OIDC/LDAP needs to be accepted item by item in the target environment.

## [v0.4.0] - 2026-09-30

> The `v0.4.0` tag has been officially released and created. The theme of this edition is "Production-level column authorization and cross-request transaction security", which complements the existing default deny link with PostgreSQL column-level authorization, MCP logical session, audit integrity and one-click experience.

### Added

- **MCP dual bearer and sessionized transmission**: continue to provide both native `stdio` and Streamable HTTP `/mcp`, retained `list_datasources`, `list_schema`, `explain_query`, `query`, `execute_write`, `request_approval`, `get_approval_result` 7 basic tools; Streamable HTTP enables stateful transmission sessions by default, `initialize` returns `Mcp-Session-Id`, and can also explicitly fall back to stateless.
- **B2 PostgreSQL column-level authorization**: Added SELECT column-level authorization closed loop for PostgreSQL 14–18, covering metadata, column-level whitelist, projected lineage, OID/catalog binding, request reservation, control fence and audit; it is enabled by factory default, and ordinary base tables use the installation-free extension `CATALOG_CLOSED_V1` by default. View, matview and complex lineage can select signatures `agentsql_binder` native extension. Authorization out of bounds, binding uncertainty, or runtime lease invalidation are all fail-closed; MySQL does not enter the B2 path.
- **B5 Cross-request logical session and planned transaction**: Added PostgreSQL cross-request logical session, strongly consistent session directory, owner epoch/sticky, planned archive, sequential DML, final state CAS/final fence, fault recovery and emergency WAL that are enabled by default; added `open_session`, `close_session`, `get_session_status`, `begin_transaction`, `execute_transaction_statement`, `commit_transaction`, `rollback_transaction`, `get_transaction_status` 8 MCP tools. MySQL cross-request transactions are not supported in v0.4.
- **masking and sensitive column discovery enhancement**: The result layer completely covers four types of algorithms: `mask`, HMAC-SHA256 `hash`, integer value `block`, numerical bucketing/date truncation `range`; new versioned hash key Manifest, registration/verification, start strong reconciliation, restart plan switching and audit outbox. Sensitive column discovery is expanded to nine categories, and table and column masking drafts that are disabled by default can be generated according to the recommended algorithm; samples are still only used in memory and will not be dropped to the disk, and will not enter the audit body or notification.
- **audit integrity and export**: Added backfillable, verifiable, audit hash chain with status and readiness, as well as `agentsqlctl chain status/verify/provision`, read-only management API and console integrity panel; audit export continues to provide CSV and machine-oriented JSONL with Chinese header and formula injection protection, and records `audit.export` management traces.
- **notification outgoing**: Provides Webhook and Syslog channels filtered by decision, remains live-only, best-effort, closed by default and does not contain the original SQL text by default; Webhook has SSRF fail-closed, signature, bounded queue and delivery indicators, and notification failure does not change the SQL decision or authoritative audit record.
- **5-minute quick start and demonstration stack**: Added a self-contained PostgreSQL/MySQL Demo that can be started without cloning the source code and only downloading a Compose file, and provides scenario cards with real authorization, masking, unauthorized rejection and approval results; the Linux installer, release package and GHCR image cover both `linux/amd64` and `linux/arm64`.
- **Ecological inclusion and compatibility pre-research**: Added MCP official Registry description file and completed Registry / Glama inclusion; supplemented Eke KingbaseES KingbaseES, HighGo HighGo, openGauss (GaussDB), IvorySQL, TiDB, OceanBase, TDSQL, YashanDB database YashanDB, Dameng database DM in total 9 Compatible pre-study and joint case outlines for candidate databases. The above list is the direction of planning and cooperation, and does not mean that v0.4 has supported it.

### Changed

- Database errors are unified into the `decision=error` business envelope and run through Playground, Ping, MCP, auditing, SSE and notifications, stably recording `error_code` / `error_stage`; authorization failures, SQL syntax errors, object non-existence, connection/timeout and other caliber separations can be expected, and the console displays executable Chinese prompts.
- PostgreSQL B2 column-level authorization and B5 logical session/planned transactions are switched from pre-research gate control to factory default enabled; `/healthz` and `/readyz` report activation, drift, recovery and fault status synchronously with the operation and maintenance plane.
- Query result masking uses projection lineage alignment MySQL/PostgreSQL top-level direct projection; columns that can be uniquely parsed are accurately matched according to physical sources. When the ownership cannot be determined and a protected table may be involved, the fixed blocking is `***`.
- The release project adds the production Ed25519 signer, the reviewable development signature tool, the architecture-aware installer and the GHCR multi-architecture build process; the public documents, examples and product versions are unified as v0.4.0.

### Security

- B2 uses sealed transport, language-level capability isolation, resource caps, catalog/OID locks and activation fences; native extension verification ABI, capabilities and signature digests, avoids rejection when the extension closure mode is uncertain, and does not silently return to table level for release.
- B5 transactions seal the complete ordered DML plan before starting, and the SQL cannot be replaced during the execution phase; session continuation proof, owner fence, final state evidence consistency, cancellation pollution and crash recovery jointly prevent cross-agent, out-of-order and repeated submissions.
- SQL parser upgrade and fail-closed boundaries such as `VALUES` table statement; masked lineage supplements zero-leak negative set, hash key material does not enter configuration echo, logs, audits, or metrics.
- The audit chain uses canonical envelope and hash derivation to cover the writing, approval and batch placement paths; abnormal chain status can make the relevant write components readiness fail-closed, and the independent verifier supports fixed exit code verification.

### Fixed

- Fixed an issue where the MCP stateful session test and the Demo reset helper did not complete the `initialize` handshake first.
- Fixed regression instability caused by B5 emergency WAL directory `fsync` being rejected under Windows, audit chain monitoring errors reusing end-of-life context, and MySQL image drift and container cold start timeout.
- Fixed parsing failure not returning stable `DB_SYNTAX_ERROR`, MySQL `INSERT ... VALUES` EXPLAIN classification and database errors being falsely reported as HTTP 500; further distinguish expected authorization failure and object missing prompts.
- Fix the Demo table foreign key conflict with the sealed JOIN script, and strengthen the daily reset seed/gateway startup timing.

### Compatibility

- The protected business database still supports MySQL 8 and PostgreSQL 14/15/16/17/18; the control plane supports the default SQLite and PostgreSQL 15+. B2 column-level authorization and B5 cross-request transactions are only provided in the PostgreSQL path, and MySQL maintains the existing table-level authorization and single-request execution boundaries.
- The official native package supports `linux/amd64` and `linux/arm64` of glibc 2.28+, and the GHCR tag is the same dual-architecture manifest; musl/Alpine, CentOS 7 and other native architectures are still not supported.
- Both B2/B5 and stateful HTTP transport provide explicit fallback switches; turning off B5 will not affect the seven basic MCP tools, and turning off HTTP transport session will not implicitly change the B5 logical session configuration.

## [v0.3.0] - 2026-09-29

> Release preparation: The target GA date is 2026-09-29, and the official tag has not yet been created. The theme of this edition is "Protection of Sensitive Data in Depth" and does not change the default denied secure links.

### Added

- **Security event notification (T37)**: Added security event outgoing filtered by decision, supports Webhook and Syslog channels, live-only, best-effort; is turned off by default, does not contain SQL original text by default, and notification failure does not affect auditing and SQL decisions. Webhook has built-in SSRF fail-closed protection (blocking intranet and cloud metadata addresses), HMAC and DingTalk robot signatures, bounded queues and delivery indicators; the console provides channel settings, key blank retention, private network endpoint alarms and delivery health. The notification key is stored in AES-GCM encryption, supporting 0002 migration (SQLite/PostgreSQL dual stream) and included in the SQLite→PG migration list.
- **sensitive column discovery (T38)**: Newly added PII sensitive column discovery (classifier/scanner/advisor and confidence state machine), candidate column sampling has a hard upper limit on the number of tables, columns, and sample rows; samples only participate in the determination in memory and are not persisted, do not return pages, and do not enter the audit body or notification. Added discovery management API and "Discover → Masked Draft" apply saga to generate `enabled=false` draft. The console provides discovery drawer and one-click draft; discovery is only provided on the management plane and is not exposed as an MCP tool. The synchronization deadline is 12 seconds. `mask_rules` adds the `enabled` flag and data source/column unique index (0003 dual-stream migration, including repeated pre-checking). The masking engine only loads the enabling rules, and the management operation supplements the audit action/actor/details. If there is a conflict, `ErrMaskRuleConflict` is returned.
- **masking algorithm expansion (T40–T43)**: Sensitive types are expanded from mobile phone number/email to nine categories, new partial masking of ID card, bank card, IP address, date of birth (`mask`), unrecognized combination fail-closed; new irreversible HMAC-SHA256 Fingerprint (`hash`, private key, cannot be decrypted and restored), integer blocking (`block`, non-null values are uniformly replaced by `***`, the length and equivalence relationship is not disclosed), numerical bucketing/date truncation (`range`, only used for `number`/`date`, no key is required to retain coarse-grained distribution). The discovery process is not recommended for `range`/`hash`/`block`/`generic`, which needs to be configured manually on the masking rules page.
- **audit export (T43/T45)**: Audit export adds Excel/WPS that can be opened directly, Chinese CSV with formula injection protection, and machine-oriented JSONL; the export is successfully written to `audit.export` management traces.
- **table. Column-aware masking (T46)**: masking rules support three scopes: global column, `表.列`, `模式.表.列`, precise hits based on source table/source column; new `schema_name` and 0005 control plane migration (SQLite/PostgreSQL Four migration flows), the existing rules are solidified into global column rules at one time, and the semantics that actually took effect before the upgrade are maintained. The parser has added direct projection column source relationship analysis, and columns that have been qualified by table names/aliases in JOIN can be accurately attributed; for JOIN bare columns, `SELECT *`, CTE outer layers, etc., the column ownership cannot be determined and may involve protected tables. The fail-closed fixed blocking is `***` and cannot be closed. Sensitive column discovery is changed to generate a table-only draft (empty pattern, real table name, `mask`, disabled), and columns with the same name in different tables are drafted separately; PostgreSQL empty pattern matches any pattern, MySQL empty pattern represents the current database, and is manually tightened when precise patterns are required. The three-level form of the console, the new mode/table column in the rule list, the discovery results are displayed according to the complete `模式.表.列`, and the global rule coverage (CoveredByGlobal) is used to remove duplication.

### Changed

- The front-end is changed to route-level code splitting, and loopless vendor chunks are split according to react/antd/echarts, etc. to reduce the size of the first screen.
- The masking rule is upgraded from the two-level matching of "data source + normalized column name" to the four-element matching of "data source + schema + table + normalized column name"; conflicts distinguish between physical key conflicts (including disabled drafts, `MASK_RULE_CONFLICT`) and scope/algorithm conflicts between enabled rules (`MASK_RULE_SCOPE_CONFLICT`).
- The sensitive column discovery draft is changed from the data source level column name draft to the table-only table.column draft.

### Security

- The notification channel is closed by default and does not contain SQL by default. Webhook SSRF is fail-closed and the signature is verified; samples found in sensitive columns are not placed on disk and are not exposed as MCP tools.
- Table. Column masking defaults to fail-closed blocking and does not provide a release switch for sensitive columns whose ownership cannot be determined; `hash` is an irreversible fingerprint rather than encryption. `range`/`block` is processed after the database is executed and does not replace read-only accounts, column-level permissions and security views.
- v0.3 All new submissions passed a full history key/credential scan and no real keys, private keys, cloud access keys, or tracked `.env` were found.
- Complete govulncheck and npm audit double scanning and hardening before release: fix the SQL injection announcement GO-2026-5004 of the core PG driver pgx (pgx/v5 upgrade v5.9.2, the call point is the PostgreSQL executor), indirectly rely on grpc, edwards25519 to upgrade to a patch version containing the repair; all front-end cleanups will enter the high-level announcement of the production product (axios upgrade to 1.20.0, react-router-dom upgraded to 6.30.6). The residual vitess GO-2026-4567 only statically links its SQL parser and does not run any vitess server. The vulnerability sink is unreachable (the repaired version v0.22.4 is incompatible with Go 1.27 builds and will be followed up in subsequent versions); the vite/esbuild announcement only exists in the local development server and is not delivered with the production static product. The residual echarts and react-router are moderate. This product is purely CSR and has no HTML. It is not reachable under architectures with rich text rendering and no user-controllable external jumps.

### Fixed

- Fixed the problem in table and column masking that columns that have been parsed into a certain table may be mistakenly damaged by rules with the same name in other tables in the same statement; the unresolved safety net will only work on columns whose ownership cannot be determined.
- Fixed the problem of demo seed not verifying the demo masking rules according to the data source after hash algorithm gating, and global error prompts repeatedly popping up when console masking changes were made.

### Compatibility

- The protected business libraries are still MySQL 8 and PostgreSQL 14/15/16/17/18; the control plane supports the default SQLite and PostgreSQL 15+ (baseline PostgreSQL 18), and metadata and audit can be divided into libraries.
- The upgrade needs to apply 0002 (notification), 0003 (discovery draft), 0004 (range), 0005 (table.column scope) migrations in sequence; production PostgreSQL recommends `store.auto_migrate:false` be explicitly executed by the migration account, see the deployment guide for details.
- Authorization for multi-table JOIN and self-join is still at the table level (column-level whitelists are not tightened with projection), and the changes in this version are at the masking layer rather than the authorization layer.
- The first version does not provide hash multi-key version/online rotation; the discovery process does not generate `range`/`hash`/`block` rules.

## [v0.2.0] - 2026-09-17

> Release preparation draft: The date is the release preparation day; if the GA date is postponed, the official tag date will be used as the date to synchronize the title of the bank and the Release.

### Added

- The console overview adds a Bearer-authenticated SSE real-time event stream (`GET /api/v1/stream`), which automatically degrades to the existing scheduled refresh path when the connection is interrupted; select SSE instead of WebSocket, and one-way push can meet the needs of large screens.
- The metadata and audit control plane has new PostgreSQL support (compatible with 15+, the development, Compose and CI benchmark is PostgreSQL 18), supports metadata and audit to be placed in independent databases, and the audit library retains the application-level relationship between the immutable audit ID and `approvals.audit_id`.
- Added SQLite to PostgreSQL data migration, verification and sequence alignment command `agentsqlctl migrate-sqlite-to-postgres`.
- Added a self-hosted open source Live Demo package: a single command to pull up the PostgreSQL/MySQL demo library and gateway containing only synthetic data. The port is only bound to loopback, the data is reset daily, and access to real data is prohibited.
- In addition to the original static evaluation, the demonstration platform adds a "real trial run (Live)" mode and `POST /api/v1/playground/run`, which is only registered in the demo mode. It reuses eight sections of the production security inspection pipeline and has 6 built-in real controlled scripts: normal release, no WHERE write interception, phone/email masking, large result scan alarm, unauthorized table rejection, audit/big screen review.

### Changed

- Production PostgreSQL deployments use a one-time migration account, a minimal-privilege run account, and `store.auto_migrate:false`.
- The independent audit library layout preserves the immutable audit ID and the application-level relationship of `approvals.audit_id` to the audit record.

### Security

- Demonstration of Live real channel Agent Key is only held by the server and is not sent to the browser; the request body only accepts whitelist fields, and unknown fields such as identity/dialect are rejected.
- In Live mode, non-SELECT statements are intercepted by structural hard barriers (DEMO_NON_SELECT/DEMO_PARSE/DEMO_SCOPE) before reaching the database. They do not enter the directory and cannot be overridden by rule configuration. The demo data source is whitelisted, the demo current limit is fixed at QPS=2, and the response is a whitelist DTO without keys and DSNs.

### Fixed

- Fix the problem that PostgreSQL `numeric/decimal` (pgx v5 `pgtype.Numeric`) is rendered as a structure text (such as `{2274 -2 false finite true}`) in the result table, and is changed to a fixed-point decimal string based on `big.Int` and index (such as `22.74`), retaining trailing zeros and not going through float64; at the same time, let Structured values such as interval, jsonb, uuid, array, enum, bytea, etc. are returned in human readable form. MySQL decimal path behavior remains unchanged.

### Compatibility

- The protected business libraries are still MySQL 8 and PostgreSQL 14, 15, 16, 17, and 18; the new PostgreSQL 18 in this version supports the control plane (metadata/audit) library.
- PostgreSQL control plane is compatible with 15+, benchmarks and CI use PostgreSQL 18.
- SQLite remains the default zero-configuration control plane, and the existing SQLite starting path remains unchanged.

## [v0.1.0] - 2026-09-16

### Added

- Provides two bearers, MCP stdio and Streamable HTTP, as well as 7 tools including data source discovery, Schema viewing, Explain, query, controlled writing, application approval and query approval results.
- Provides PostgreSQL/MySQL SQL parsing, security rules, Agent capability levels, object/column authorization, current limiting, Explain risk assessment, controlled execution and synchronous audit links.
- Provides basic coding of mobile phone numbers/emails, manual approval closed loop, Prometheus indicators, health/readiness probes and embedded web management console.
- Provides SQLite metadata/audit storage, Docker Compose, systemd examples and operation and maintenance CLI.

### Security

- Enforces exactly 32 bytes of `AGENTSQL_SECRET` on startup; production mode refuses to expose sample values.
- Enforces a unique administrator passphrase of at least 12 characters when the console is enabled, and rejects username equivalents and known weak passwords.
- `AGENTSQL_INSECURE=1` only allows local exposure of test credentials and does not turn off authentication, security rules, or fail-closed behavior.
- The API Key only persists the SHA-256 hash; the data source password is stored encrypted using SECRET.
- `agentsql.db` and the corresponding SECRET must be backed up in pairs before upgrading. Directly replacing SECRET will make the existing data source password unable to be decrypted, and it is not a lossless rotation.

### Deployment

- The Docker runtime uses a non-root user, persists the SQLite data directory, and has built-in healthcheck.
- Compose only binds the AgentSQL, optional PostgreSQL demo, and MySQL demo ports to host `127.0.0.1` by default.
- The default versions of Dockerfile, Compose, Makefile and the two binaries are unified to `v0.1.0`.
- The sample environment file no longer carries a directly usable SECRET or administrator password.

### Compatibility

- The protected business database supports MySQL 8 and PostgreSQL 14, 15, 16, 17, and 18.
- The v0.1 metadata and audit backend only supports SQLite; the PostgreSQL metadata/audit library belongs to the T28 route of v0.2.
- Known limitations: JOIN/self-connection only performs table-level authorization and does not perform projected column attribution; `*`/`schema.*` table authorization means matching table full column authorization; MCP does not expose cross-request session/transaction parameters, and multi-statement transaction capabilities are postcoded to v0.2.

## Post route (after v0.2)

- T29: Domestic/commercial business database matrix (Dameng, KingbaseES KingbaseES, HighGo HighGo, GaussDB, OceanBase, TiDB, Oracle, SQL Server, etc.); enterprise version is post-installed.
- T30: Compliance and identity control (MLA report export, WORM, SIEM, row-level security, SSO/LDAP/MFA, multi-tenancy, RBAC, countersigning work orders, etc.); Enterprise version is post-installed. Sensitive column discovery and table column masking have been implemented in v0.3.
- T31: HA, centralized management and control and scale delivery (K8s Operator, cross-instance audit bus, UEBA, privatization and SLA); enterprise version is post-installed.
- The Cloud/SaaS form will be evaluated after the capabilities of the enterprise version mature and will not enter the open source route in the near future.

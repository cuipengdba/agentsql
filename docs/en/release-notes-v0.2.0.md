# AgentSQL v0.2.0

**Language:** [English](release-notes-v0.2.0.md) | [Simplified Chinese](../release-notes-v0.2.0.md)

> Database security gateway/production-grade MCP Server for AI Agent. Let every PostgreSQL/MySQL request issued by the model go through a security gate of "Authentication → Parsing → Authorization → Rules → Approval → Execution → masking → Audit" before reaching the database.
>
> Chinese brand name: Zhidun (the console is displayed as "AgentSQL Zhidun Console").

```text
AI Agent → LLM / MCP Client → AgentSQL Gateway → PostgreSQL / MySQL
                                  │
                                  └─ Authentication · Authorization · Rules · Approval · masking · Audit
```

v0.2.0existv0.1On top of the core of the security gateway, it complements three capabilities that allow the product to be "available for online experience, production control, and real-time observation":**Self-hosted Live Demo, console real-time security large screen, PostgreSQL 18 control plane and independent audit library**。

---

## Highlights of this edition

### 1. One-click self-hosted Live Demo: six real controlled scripts (T26)

One command pulls up the PostgreSQL/MySQL demo library and gateway containing only synthetic data (the port is only bound to the loopback, can be reset periodically through external scheduled tasks, and no real data is connected). The demo station offers two modes:

- **static evaluation**: only performs SQL parsing and static rule security inspection, does not connect to the business database, does not execute SQL, does not write audits, and does not generate business database reading and writing;
- **real trial run (Live)**: Through `POST /api/v1/playground/run` registered only in demo mode, eight sections of the production security inspection pipeline are reused, and the Demo-specific identity/data source whitelist, read-only hard barrier and current limiting constraints are superimposed. There are six built-in scripts, so you can understand at a glance what the gateway has blocked and what has been released:
  1. Release normally (return 5 rows, bring back audit_id);
  2. No WHERE full table update → Hard interception before reaching the database, no underlying database error will be displayed;
  3. phone/email column result layer masking (`138****0001`, `u***@example.test`);
  4. Large result set scan → R005 alarm, estimated 600 rows, truncated by the upper limit and returned to 20 rows;
  5. Unauthorized access to sensitive table `internal_notes` → R010 rejects and does not return any content;
  6. Use audit_id to jump to the audit evidence chain with one click, or go to the overview screen to watch the real-time stream.

The demo key of the Live channel is only saved on the server and is not sent to the browser; non-SELECT statements are intercepted by structural hard barriers before hitting the database and cannot be overridden by rule configuration; the data source whitelist, strict field verification, demo current limiting (QPS=2) and the whitelist response without key/DSN jointly ensure the security of the demo environment.

> Six real running screenshots are provided with the repository, see README and `docs/images/demo-scenario-1.png`…`demo-scenario-6.png`.

### 2. Console real-time security large screen (T27)

A new Bearer-authenticated **SSE real-time event stream** (`GET /api/v1/stream`) has been added to the overview screen. Release/alarm/pending approval/interception events and interception trends are displayed on the screen in real time; when the network is interrupted, it is automatically downgraded to the original scheduled refresh without affecting availability. Enabled by default, the management side can have up to 100 concurrent connections. One-way push uses SSE instead of WebSocket, which is simpler and easier to pass the reverse proxy.

### 3. PostgreSQL 18 control plane and independent audit library (T28)

- In addition to the original SQLite, the metadata database and audit library have added **PostgreSQL support (compatible with 15+, the baseline and CI are PostgreSQL 18)**, available in both open source and enterprise versions;
- Metadata and audit may use **two separate databases**. The audit database preserves immutable audit IDs and their application-level association with `approvals.audit_id`. Writes use an audit-first commit barrier;
- Production deployment uses a one-time migration account + minimum permission running account + `store.auto_migrate:false`;
- Added `agentsqlctl migrate-sqlite-to-postgres`, migrating the default SQLite control plane to PostgreSQL (single database or independent dual database), with verification and sequence alignment.

SQLite is still the default zero-configuration control surface, and the original starting path remains completely unchanged.

### 4. Result value rendering repair

Fixed the problem that PostgreSQL `numeric/decimal` is rendered into structure text (such as `{2274 -2 false finite true}`) in the result table, and is uniformly displayed as fixed-point decimal `22.74` (based on `big.Int` and index, **does not pass float64**, trailing zeros retained); structured types such as interval, jsonb, uuid, array, enum, bytea, etc. are also returned in human-readable form. MySQL decimal behavior is unchanged.

---

## 5 minutes experience

**Live Demo (synthetic data, the most intuitive):**

```bash
cp examples/docker/demo.env.example demo/demo.env   # Follow the prompts to replace all placeholder credentials
bash ./demo/reset.sh                                 # Windows uses .\demo\reset.ps1
# Open http://127.0.0.1:17880
```

**production form (Docker Compose + SQLite, starting with zero configuration):**

```bash
cp examples/docker/.env.example .env                 # Fill in the self-generated 32-byte SECRET and administrator password
docker compose up -d --build
# Open http://127.0.0.1:7780
```

For complete deployment, PostgreSQL control plane, systemd, backup and upgrade, see [ deployment document ](DEPLOY.md). For Live Demo instructions, see [Live Demo document ](DEMO.md).

## MCP access

Supports both native **stdio** and **Streamable HTTP (`/mcp`, Bearer API Key)** bearers, providing 7 Controlled database tools: `list_datasources`, `list_schema`, `explain_query`, `query`, `execute_write`, `request_approval`, `get_approval_result`. For configuration fragments, see README "MCP Access".

## Upgrade from v0.1

- SQLite users: Just replace the binary/image, and the control plane will be automatically migrated. Before upgrading **, please back up `agentsql.db` and `AGENTSQL_SECRET`** in pairs (replacing SECRET will make the existing data source password unable to be decrypted, and it is not a lossless rotation).
- If you want to use the PostgreSQL control plane: Back up first, then use `agentsqlctl migrate-sqlite-to-postgres`, and press `docs/DEPLOY.md` to configure the one-time migration account and the minimum privilege running account.
- The supported scope of protected business libraries remains unchanged: MySQL 8 and PostgreSQL 14/15/16/17/18.

## Security Boundaries and Known Limitations (please read first)

AgentSQL is the security control of **gateway layer**. It is not a silver bullet. This version clearly does not promise the following capabilities:

- Least privilege only restricts**Database account through the gateway**;Does not prevent people with owner/superuser from bypassing the gateway and connecting directly.
- masking is based on the **result layer coding** based on column rules, and is not a complete DLP; functions/expressions/aggregations, UNIONs, cross-subqueries/CTE/view internal renames do not have complete lineage protection, and need to be combined with read-only accounts, column-level permissions, security views and approvals.
- JOIN/self-join only performs table-level authorization and does not infer the ownership of projected columns; `*`/`schema.*` means granting all columns of the entire table.
- The audit is immutable at the application layer. **is not the regulation-level WORM**, nor does it prevent DBA from directly changing the database. Class-guaranteed reports, WORM, SIEM, row-level security, SSO/LDAP/MFA, multi-tenancy, etc. belong to the enterprise version route.
- MCP does not expose cross-request session/transaction parameters.

## Enterprise Edition Route (Roadmap)

- **T29** Domestic/commercial business database matrix: Dameng, KingbaseES KingbaseES, HighGo HighGo, GaussDB, OceanBase, TiDB, Oracle, SQL Server;
- **T30** Compliance and identity control: MLA reporting, WORM, SIEM, sensitive discovery, SSO/LDAP/MFA, multi-tenancy, RBAC, countersigning work orders;
- **T31** High availability and centralized management and control: K8s Operator, cross-instance audit bus, UEBA, privatized delivery and SLA.

## Historical Release License

The v0.2.0 source was released under **GNU AGPLv3**. Historical tags and assets retain the license applicable at their release; their terms are not changed retroactively. Rights to the AgentSQL name and logo remain reserved.

> Note: The above is the license caliber (AGPLv3) at the time of v0.2.0 release. Since subsequent versions, the open source license has been switched to **Apache License 2.0**, which is currently based on the root directory [LICENSE](../../LICENSE).

## Asset checksum

At the time of release, the outer binary package `agentsql-v0.2.0-linux-amd64.tar.gz` and the SHA-256 of the same name `.sha256` are attached here (the fixed name `install.sh` is provided with the GitHub Release assets). The Source code (zip/tar.gz) automatically generated by GitHub is a snapshot of the platform source code and is not verified by the above SHA-256.

---

**related links**: [README](../../README.en.md) · [ Quick Start ](GETTING_STARTED.md) · [ User Manual ](USER_GUIDE.md) · [MCP Access Guide ](INTEGRATIONS.md) · [ Deployment Guide ](DEPLOY.md) · [Live Demo Guide ](DEMO.md) · [ Architecture and Engineering Specifications ](SPEC.md) · [ Change Record ](../../CHANGELOG.en.md)

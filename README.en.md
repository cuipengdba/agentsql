# AgentSQL

[中文](README.md) | English

![Next release: v0.5.0 pending](https://img.shields.io/badge/Next%20release-v0.5.0%20pending-blue)
[![Go](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go)](go.mod)
[![License: Apache-2.0](https://img.shields.io/badge/License-Apache--2.0-blue)](LICENSE)
[![Commercial License](https://img.shields.io/badge/License-Commercial-orange)](COMMERCIAL-LICENSE.md)

AgentSQL (Chinese brand name: 智盾, or “Zhidun”) is a database security gateway and production-grade MCP server for AI agents. Every PostgreSQL or MySQL request sent through the gateway passes through authentication, SQL parsing, authorization and rule evaluation, controlled execution, result masking, and application-level audit.

v0.5.0 is planned for **2026-10-16 16:00 CST (UTC+08:00)**, subject to the release gates. The current public Release is v0.4.0. The pending v0.5 code uses Apache-2.0; existing v0.4.0 artifacts retain their original license.

```text
AI agent -> LLM / MCP client -> AgentSQL gateway -> PostgreSQL / MySQL
                                      |
                                      +-- auth, policy, rules, approval, masking, audit
```

AgentSQL is not a BI tool, ORM, or Text-to-SQL system. It does not replace database-native permissions, network isolation, TLS termination, backups, or change management.

## Core Features

- Two MCP transports: local `stdio` and Streamable HTTP at `/mcp`.
- Default-deny controls: Agent API Keys, capability tiers, object and column authorization, SQL AST rules, and fail-closed error handling.
- Controlled reads and writes: read-only protection, dangerous SQL blocking, EXPLAIN-based checks, timeouts, QPS/connection/result limits, and human approval routing.
- Result protection: `mask`, keyed HMAC `hash`, fixed-value `block`, and numeric/date `range` masking algorithms. See the [security guide](docs/en/SECURITY.md) for the exact capability matrix and limitations.
- Traceable operations: SQLite by default; optional PostgreSQL 15+ control-plane storage; audit search/export, approval records, Prometheus metrics, and health/readiness probes.
- Embedded management console for overview, audit, Agent, data source, policy, rule, approval, masking, and integration operations.
- v0.5 RBAC APIs for local users, roles, permissions, and tenants. Existing gateway resources remain available only to the default tenant until those resources gain tenant ownership fields.

The seven base MCP tools are `list_datasources`, `list_schema`, `explain_query`, `query`, `execute_write`, `request_approval`, and `get_approval_result`. The PostgreSQL B5 session/planned-transaction feature is enabled by default and adds eight more tools. Always use `tools/list` as the authority for a running instance.

## Database Compatibility

The pending v0.5.0 code registers **8 categories and 27 databases** (6 relational + 21 NoSQL / vector). KingbaseES V9 is a separate vendor-environment candidate and is not included in the 27. Tiers describe bounded capabilities: 🟢 full protection = parsing, authorization, controlled execution, masking, and audit; 🔵 controlled read-only = connection, metadata, and a read-only query subset; 🔷 connection-level = ping, version, schema, and read-only preview; 🟡 pending validation = waiting for a vendor environment.

### Relational (6 registered + 1 pending)

- 🟢 PostgreSQL 14–18; MySQL 8.0
- 🔵 Oracle 23ai; Dameng DM8; YashanDB; SQL Server 2025
- 🟡 KingbaseES V9: waiting for a vendor image and license; the target environment was expected on October 8. Connection validation is not claimed before it is obtained.

### Key-value (KV)

- 🔷 Redis; Valkey (reuses the Redis adapter); Memcached

### Document

- 🔷 MongoDB; Couchbase; CouchDB

### Wide-column

- 🔷 Cassandra; ScyllaDB; HBase

### Graph

- 🔷 Neo4j; JanusGraph; NebulaGraph

### Time-series

- 🔷 InfluxDB; Prometheus; TimescaleDB

### OLAP

- 🔷 ClickHouse; Apache Doris; StarRocks

### Vector

- 🔷 Milvus; Qdrant; Weaviate

NoSQL and vector databases currently have bounded, connection-level support. A read-only preview does not imply full query, retrieval, or server-side security capabilities. The [NoSQL support notes](docs/nosql-support.md) list all 21 registered types and default ports, including the HBase and Couchbase connection-port exceptions. DM/Oracle have a narrow controlled-SELECT parser that is not wired into the full gateway authorization/masking pipeline; the YashanDB offline parser is not registered through the standard `NewParser` entry point. See the [DM/Oracle](docs/dm-oracle-dialect.md) and [YashanDB](docs/yashan-dialect.md) boundaries. All tests use specified container images and synthetic cases; they are **not official database-vendor certification**. Protocol compatibility does not imply identical security semantics. Every database request must pass through the AgentSQL gateway with a dedicated least-privilege account; unknown parsing, authorization, or capability facts fail closed. See [SECURITY.md](SECURITY.md) for details.

Control-plane storage is separate from this count: zero-configuration SQLite by default, or PostgreSQL 15+ in combined or separate metadata/audit databases (PG18 baseline). `agentsqlctl migrate-sqlite-to-postgres` migrates SQLite to one or two PostgreSQL databases.

## Quick Start

All examples bind to the host loopback address by default. For remote administration, use SSH local forwarding or a controlled TLS reverse proxy; do not expose port 7780 directly to the public internet.

### Native Linux installer

The installer supports `linux/amd64` and `linux/arm64`, glibc 2.28+, and systemd:

```bash
curl -fsSL https://github.com/cuipengdba/agentsql/releases/latest/download/install.sh | sudo sh -s -- install
```

### Docker quick-start script

```bash
curl -fsSL https://raw.githubusercontent.com/cuipengdba/agentsql/main/scripts/quickstart.sh -o quickstart.sh
sh quickstart.sh
```

### Pull the v0.5.0 GHCR image after release

```bash
docker pull ghcr.io/cuipengdba/agentsql:v0.5.0
```

The planned tag is a `linux/amd64` and `linux/arm64` multi-architecture image; pull it only after the release-day public-image gate passes. For a complete direct `docker run` example with generated secrets, or to connect your first data source and run a query, follow the [five-minute quick start](docs/en/QUICKSTART.md).

### Self-contained local demo

The demo uses only synthetic PostgreSQL/MySQL data and fixed demonstration credentials:

```bash
mkdir agentsql-demo && cd agentsql-demo
curl -fsSL -o docker-compose.yml https://raw.githubusercontent.com/cuipengdba/agentsql/main/deploy/quickstart/docker-compose.yml
docker compose up -d
```

When the four containers are healthy, open <http://127.0.0.1:17880>. Remove the demo and its volumes with `docker compose down -v`. Never connect this demo configuration to real data.

## MCP Connection

Create an Agent in the console, grant it the minimum required data-source policy, and save the `asql_...` API Key when it is shown. A local running service accepts Streamable HTTP at `http://127.0.0.1:7780/mcp`:

```json
{
  "mcpServers": {
    "agentsql": {
      "url": "http://127.0.0.1:7780/mcp",
      "headers": {
        "Authorization": "Bearer <YOUR_AGENTSQL_API_KEY>"
      }
    }
  }
}
```

Streamable HTTP is stateful by default. The client must retain the `Mcp-Session-Id` returned by `initialize` and send it on later requests. See the [MCP integration guide](docs/en/INTEGRATIONS.md) for the complete handshake, stdio setup, client examples, and error behavior.

Audit reporting supports CSV, JSONL, a pure-Go PDF, and a ZIP containing all report representations plus a per-file SHA-256 `manifest.json`:

```bash
agentsqlctl audit report -c config.yaml --format archive --out audit-compliance.zip
agentsqlctl audit verify-archive --in audit-compliance.zip
```

Archive verification proves package consistency only; it is not a digital signature, trusted timestamp, freshness proof, WORM store, or SIEM retention control.

## Documentation

- [English documentation index](docs/en/README.md)
- [Documentation coverage inventory and remaining gaps](docs/en/COVERAGE.md)
- [Five-minute quick start](docs/en/QUICKSTART.md)
- [Detailed getting-started guide](docs/en/GETTING_STARTED.md)
- [Administration, RBAC, and audit](docs/en/ADMIN.md)
- [MCP integration](docs/en/INTEGRATIONS.md)
- [Security model and boundaries](docs/en/SECURITY.md)
- [MFA, OIDC, and LDAP authentication](docs/en/AUTHENTICATION.md)
- [Chinese deployment guide](docs/DEPLOY.md)
- [Chinese user guide](docs/USER_GUIDE.md)

The specialized deployment and operations references that are not yet translated are explicitly identified in the [English documentation index](docs/en/README.md).

## Security

Use a dedicated least-privilege database account. Keep `AGENTSQL_SECRET`, Agent API Keys, administrator credentials, data-source passwords, and redaction hash keys out of source control and logs. Back up the control-plane database and its paired `AGENTSQL_SECRET` together.

Result masking is not complete DLP, and application-level audit is not regulatory-grade WORM storage. Read [Security](docs/en/SECURITY.md) before production deployment. Report vulnerabilities privately according to [SECURITY.md](SECURITY.md); do not publish unpatched details or real credentials in an Issue.

## License

Starting with v0.5.0, the repository's open-source edition is licensed under the [Apache License, Version 2.0](LICENSE) (see also [NOTICE](NOTICE)), with a separate [commercial license](COMMERCIAL-LICENSE.md). Existing v0.4.0 and earlier source, tags, and release artifacts keep the license under which they were published. Apache-2.0 permits closed-source integration, SaaS use, and redistribution. It does **not** grant trademark rights; AgentSQL and 智盾 names and logos remain subject to the trademark terms described in the commercial-license notice.

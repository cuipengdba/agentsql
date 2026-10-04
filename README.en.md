# AgentSQL

[中文](README.md) | English

![Release](https://img.shields.io/badge/Release-v0.5.0-blue)
[![Go](https://img.shields.io/badge/Go-1.25-00ADD8?logo=go)](go.mod)
[![License: Apache-2.0](https://img.shields.io/badge/License-Apache--2.0-blue)](LICENSE)
[![Commercial License](https://img.shields.io/badge/License-Commercial-orange)](COMMERCIAL-LICENSE.md)

AgentSQL (Chinese brand name: 智盾, or “Zhidun”) is a database security gateway and production-grade MCP server for AI agents. Every PostgreSQL or MySQL request sent through the gateway passes through authentication, SQL parsing, authorization and rule evaluation, controlled execution, result masking, and application-level audit.

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

AgentSQL currently supports PostgreSQL and MySQL as protected business databases. The following Chinese-database matrix is copied from the Chinese README and distinguishes tested protocol paths from work in progress.

| Database | Status | Notes |
| --- | --- | --- |
| KingbaseES | Pending vendor environment | The target V9R1C10 image is pending from the vendor; the available third-party older image has an expired license |
| HighGo | Protocol path tested | The PostgreSQL protocol path was tested with a third-party SEE image; commercial-edition validation is still pending |
| IvorySQL | Tested | Community-edition protocol path tested |
| openGauss (GaussDB) | Tested | Community-edition protocol path tested |
| TiDB | In progress | EXPLAIN adaptation is in progress |
| OceanBase | In progress | EXPLAIN adaptation is in progress |
| TDSQL | In progress | OpenTenBase adaptation is in progress; commercial edition pending test |
| OpenTenBase | In progress | Execution-plan compatibility fix is in progress |
| YashanDB | Pending test | x86 image is available; ARM image is a fallback |
| Dameng Database (DM) | Pending adaptation | DM8 container is running; a separate dialect is still required |
| PolarDB for PostgreSQL | Protocol path tested | A named PG 15 community image reuses `db_type=postgres`; this is not commercial-service certification |
| PolarDB-X | Bounded old-image test | The official `2.0.1` all-in-one image reuses `db_type=mysql` and passes the two-stage EXPLAIN path; current full-topology and commercial-service validation remain pending |

“Tested” means that a community-edition or container-image protocol path was exercised; it is not vendor certification. “In progress” and “pending test” do not mean the current release supports that database. The current native support matrix remains PostgreSQL 14–18 and MySQL 8.

See the [KingbaseES + HighGo case-validation record](docs/joint-case-kingbase-highgo.md) for evidence, reproducible SQL, and pending vendor items. Public messaging is defined in the [community operations wording](docs/joint-case-operations.md).

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

### Pull the published GHCR image

```bash
docker pull ghcr.io/cuipengdba/agentsql:v0.5.0
```

The published tag is a `linux/amd64` and `linux/arm64` multi-architecture image. For a complete direct `docker run` example with generated secrets, or to connect your first data source and run a query, follow the [five-minute quick start](docs/en/QUICKSTART.md).

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

## Documentation

- [English documentation index](docs/en/README.md)
- [Documentation coverage inventory and remaining gaps](docs/en/COVERAGE.md)
- [Five-minute quick start](docs/en/QUICKSTART.md)
- [Detailed getting-started guide](docs/en/GETTING_STARTED.md)
- [Administration, RBAC, and audit](docs/en/ADMIN.md)
- [MCP integration](docs/en/INTEGRATIONS.md)
- [Security model and boundaries](docs/en/SECURITY.md)
- [Chinese deployment guide](docs/DEPLOY.md)
- [Chinese user guide](docs/USER_GUIDE.md)

The specialized deployment and operations references that are not yet translated are explicitly identified in the [English documentation index](docs/en/README.md).

## Security

Use a dedicated least-privilege database account. Keep `AGENTSQL_SECRET`, Agent API Keys, administrator credentials, data-source passwords, and redaction hash keys out of source control and logs. Back up the control-plane database and its paired `AGENTSQL_SECRET` together.

Result masking is not complete DLP, and application-level audit is not regulatory-grade WORM storage. Read [Security](docs/en/SECURITY.md) before production deployment. Report vulnerabilities privately according to [SECURITY.md](SECURITY.md); do not publish unpatched details or real credentials in an Issue.

## License

The repository's open-source edition is licensed under the [Apache License, Version 2.0](LICENSE) (see also [NOTICE](NOTICE)), with a separate [commercial license](COMMERCIAL-LICENSE.md). Apache-2.0 permits closed-source integration, SaaS use, and redistribution. It does **not** grant trademark rights; AgentSQL and 智盾 names and logos remain subject to the trademark terms described in the commercial-license notice.

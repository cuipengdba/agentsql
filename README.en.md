# AgentSQL

**Language / 语言:** [English](README.en.md) | [简体中文](README.md)

![Version v3.0.1](https://img.shields.io/badge/Version-v3.0.1-blue)
[![Go](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go)](go.mod)
[![License: Apache-2.0](https://img.shields.io/badge/License-Apache--2.0-blue)](LICENSE)

AgentSQL (Chinese brand name: 智盾) is a self-hosted database security gateway and MCP server for AI agents. It puts identity, authorization, SQL checks, controlled execution, configurable result masking, and application-level audit between an agent and a database. It is listed in the MCP Registry.

```text
AI agent → MCP client → AgentSQL gateway → supported database
                         │
                         └─ authenticate · authorize · inspect · approve · mask · audit
```

v3.0.1 adds Chinese/English switching to the website and Web console, updates this English README, and makes the left console menu scroll when the available viewport height is short. It does not change the database protocol, permission, execution, or audit contracts established in v3.0.0. See the [v3.0.1 release notes](docs/release-notes-v3.0.1.md).

The [official website](https://agentsql.cn/en/) and [Live Demo safety page](https://agentsql.cn/en/demo/) are available in English. The detailed operations manuals and most historical acceptance records are currently in Chinese; this README identifies the relevant links and limits. The console has a language switch in its sign-in screen and header.

## What v3.0.0 includes

- A shared capability catalog for 30 registered databases in eight families, displayed in the management API, console, and authorized MCP data-source list. KingbaseES V9 is listed separately for a specified PG-protocol instance. A catalog entry is a capability statement, not a grant of access or a claim of full production support.
- Read-only `agentsqlctl doctor` diagnostics and reproducible synthetic MCP acceptance scenarios for delivery checks. See the [v3.0.0 design](docs/v3.0.0-design.md), [release notes](docs/release-notes-v3.0.0.md), and [acceptance evidence](docs/v3.0.0-acceptance.md) (Chinese).
- Local stdio, stateful Streamable HTTP at `/mcp`, and a separate stateless MCP 2026-07-28 endpoint at `/mcp/2026-07-28`. The stateless endpoint authenticates each request and does not offer cross-request B5 tools.
- API-key authentication, agent capability tiers, object and column authorization, SQL AST rules, optional human approval, execution limits, result masking, and persistent application-level audit on supported paths.
- An embedded Web console for overview, audit, agents, data sources, permissions, rules, approvals, masking, user/role/tenant management, and onboarding.
- SQLite as the zero-configuration control store; optional PostgreSQL 15+ control plane. PostgreSQL 18 is the baseline. A separate audit database is an optional layout.

## Database support

Support is defined per database version, protocol mode, operation, topology, and acceptance evidence. The [full compatibility matrix](README.md#生态与兼容) is currently maintained in Chinese. Important boundaries:

| Path | Verified scope and limits |
| --- | --- |
| PostgreSQL / MySQL | Main SQL gateway paths. Their dialect, column authorization, and deployment limits differ; consult the detailed guide and release evidence before production use. |
| Vastbase G100 | Specified G100 V3.0 Build 9 PG-mode synthetic instance with restricted read-only operations, masking, MCP checks, and audit. HA, other builds, and full production support are not validated. |
| HighGo / openGauss / YashanDB | Specified enterprise or community versions and synthetic read-only subsets. Each has its own TLS and failure-closure evidence; other versions and production topology are outside those claims. |
| KingbaseES | Specified V9R1C10 PG-protocol instance validated for its documented basic path; this is not blanket production support. |
| Oracle Free / SQL Server 2025 Developer | Specified synthetic instances with narrow read-only paths and separate protocol/TLS/approval limits. Commercial editions and full production support are not validated. |
| MongoDB / Redis / Valkey / ClickHouse / Milvus | Specialized bounded read tools (`find_documents`, `read_key`, `query_analytics`, `search_vectors`) on specified versions and synthetic instances. These are not general-purpose proxies or write APIs. |

The v3.0.0 catalog distinguishes controlled SQL, bounded SQL, dedicated read tools, and connection preview. New governance features do not widen any adapter's previously validated scope. Always inspect the relevant release notes and acceptance record before describing a database as supported for a workload.

## Quick start

The public [Live Demo](https://agentsql.cn/en/demo/) uses only synthetic PostgreSQL data, controlled scenarios, and periodic resets. Do not submit real business SQL, credentials, tokens, or personal data. The public demo runs a single gateway; it does not exercise every optional production topology.

For a self-hosted Linux installation, inspect the [deployment guide](docs/DEPLOY.md) and [getting-started guide](docs/GETTING_STARTED.md) (Chinese). Versioned multi-architecture artifacts are available at [GitHub Releases](https://github.com/cuipengdba/agentsql/releases/tag/v3.0.1):

```bash
docker pull ghcr.io/cuipengdba/agentsql:v3.0.1
```

The installer supports Linux amd64/arm64, glibc ≥ 2.28, systemd, and sudo/root. Back up the control database and matching secret keys before upgrades. Review [release evidence](docs/v3.0.0-release-evidence.md) before trusting an artifact or support claim.

## Security boundaries

- Default-deny behavior depends on the configured policy, agent capability, and database privileges. Database-side least privilege, read-only accounts, safe views, and network isolation remain necessary.
- Column masking is basic result protection, not complete DLP or anonymization. It cannot protect data accessed outside the gateway.
- Application-level audit is not a regulatory WORM log and cannot stop a database owner or superuser from changing underlying data. Some tool-level rejections happen before the persistent SQL traffic-audit path.
- The public Live Demo is non-production. Its availability, retention, and security settings are not an SLA.

## License and community

The open-source core is Apache License 2.0 from v0.5.0 onward. Earlier published versions retain their original licenses. Apache-2.0 permits commercial use, closed-source integration, SaaS use, and redistribution under its terms; trademark rights are separate. See [LICENSE](LICENSE) and [COMMERCIAL-LICENSE.md](COMMERCIAL-LICENSE.md). For trademark permission or enterprise capabilities, contact [87326549@qq.com](mailto:87326549@qq.com).

Issues and reproducible validation evidence are welcome in this repository.
